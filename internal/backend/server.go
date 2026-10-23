package backend

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// App is the main application struct holding all shared dependencies.
type App struct {
	Version         string
	ConfigStore     *ConfigStore
	Logger          *Logger
	IDStore         *IDStore
	Identity        *ClientIdentityService
	UALibrary       *UALibraryStore
	Auth            *AuthManager
	Upstream        *UpstreamPool
	UserStore       *UserStore
	WatchStore      *WatchStore
	HiddenLibraries *HiddenLibraryStore
	libraryCache    *upstreamLibraryCache
	PlaybackLimiter *PlaybackLimiter
	StreamResources *streamResourceStore
	loginLimiter    loginRateLimiter
	noticeThrottle  noticeThrottle
	// adminMutationMu serializes every handler that reads, validates and then
	// persists the config or the UA library, so two admin writes cannot each
	// base their change on a stale snapshot and overwrite one another.
	adminMutationMu sync.Mutex
	// uaJobs* own the lifecycle of the targeted login jobs UA changes start:
	// jobs derive from uaJobsCtx so Close cancels them, and uaJobsWG lets
	// Close wait for the in-flight ones instead of leaving background logins
	// behind (execution plan §7.3).
	uaJobsCtx    context.Context
	uaJobsCancel context.CancelFunc
	uaJobsWG     sync.WaitGroup
}

func NewApp() (*App, error) {
	configStore, err := LoadConfigStore()
	if err != nil {
		return nil, err
	}
	cfg := configStore.Snapshot()
	logger := NewLogger(LogConfig{DataDir: cfg.DataDir})
	logTimeoutNotice(logger, cfg.Timeouts)
	upstreamIDs := make([]string, len(cfg.Upstream))
	for i, u := range cfg.Upstream {
		upstreamIDs[i] = u.ID
	}
	idStore, err := NewIDStore(cfg.DataDir, logger, upstreamIDs...)
	if err != nil {
		return nil, err
	}
	identity := NewClientIdentityServiceFromDetectedConfig(logger)
	uaLibrary, uaLibErr := NewUALibraryStore(cfg.DataDir, logger)
	if uaLibErr != nil {
		logger.Warnf("UA library unavailable: %v (UA management disabled; resident mode will not send identity)", uaLibErr)
	}
	identity.SetUALibrary(uaLibrary)
	auth, err := NewAuthManager(configStore, identity, logger)
	if err != nil {
		return nil, err
	}
	upstream := NewUpstreamPool(cfg, logger)
	upstream.LoginAll()

	var userStore *UserStore
	var watchStore *WatchStore
	var hiddenLibraries *HiddenLibraryStore
	if db := idStore.DB(); db != nil {
		us, err := NewUserStore(db, logger)
		if err != nil {
			logger.Warnf("UserStore init failed: %v (multi-user disabled)", err)
		} else {
			userStore = us
		}
		wst, err := NewWatchStore(db, logger)
		if err != nil {
			logger.Warnf("WatchStore init failed: %v (per-user watch history disabled)", err)
		} else {
			watchStore = wst
		}
		hls, err := NewHiddenLibraryStore(db, logger)
		if err != nil {
			logger.Warnf("HiddenLibraryStore init failed: %v (home library hiding disabled)", err)
		} else {
			hiddenLibraries = hls
		}
	}

	// Created here, after every early return, so no error path can leak it: the
	// cancel func lives for the whole App lifetime and Close uses it.
	uaJobsCtx, uaJobsCancel := context.WithCancel(context.Background())

	return &App{
		ConfigStore:     configStore,
		Logger:          logger,
		IDStore:         idStore,
		Identity:        identity,
		UALibrary:       uaLibrary,
		Auth:            auth,
		Upstream:        upstream,
		UserStore:       userStore,
		WatchStore:      watchStore,
		HiddenLibraries: hiddenLibraries,
		libraryCache:    newUpstreamLibraryCache(),
		PlaybackLimiter: NewPlaybackLimiter(),
		StreamResources: newStreamResourceStore(),
		uaJobsCtx:       uaJobsCtx,
		uaJobsCancel:    uaJobsCancel,
	}, nil
}

// logTimeoutNotice warns when the per-operation timeouts are tighter than the API
// timeout. Those settings used to be ignored, so after an upgrade an upstream that
// logs in slowly can start failing with nothing else looking wrong.
func logTimeoutNotice(logger *Logger, timeouts TimeoutsConfig) {
	if logger == nil || (timeouts.Login >= timeouts.API && timeouts.HealthCheck >= timeouts.API) {
		return
	}
	logger.Infof("Timeouts are now enforced: login=%dms, healthCheck=%dms, api=%dms; "+
		"raise timeouts.login/healthCheck in the admin panel if an upstream connects slowly",
		timeouts.Login, timeouts.HealthCheck, timeouts.API)
}

func (a *App) Close() error {
	// Cancel targeted login jobs first and wait for them: they hold client
	// references and must not outlive the pool's health checks.
	if a.uaJobsCancel != nil {
		a.uaJobsCancel()
		a.uaJobsWG.Wait()
	}
	if a.Upstream != nil {
		a.Upstream.stopHealthChecks()
	}
	if a.IDStore != nil {
		if err := a.IDStore.Close(); err != nil && a.Logger != nil {
			a.Logger.Warnf("ID store close: %v", err)
		}
	}
	if a.Logger != nil {
		_ = a.Logger.Close()
	}
	return nil
}

func (a *App) Run() error {
	cfg := a.ConfigStore.Snapshot()
	server := &http.Server{
		Addr:              ":" + intToString(cfg.Server.Port),
		Handler:           a.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// bodyLimitMiddleware caps a request body at 2 MB but not the time it may take to
		// arrive, and an idle connection used to be held forever. WriteTimeout stays unset
		// on purpose: the streaming endpoints write for as long as playback lasts.
		ReadTimeout: 60 * time.Second,
		IdleTimeout: 120 * time.Second,
	}

	// Start periodic stream URL eviction
	evictCtx, evictCancel := context.WithCancel(context.Background())
	defer evictCancel()
	go func() {
		ticker := time.NewTicker(30 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-evictCtx.Done():
				return
			case <-ticker.C:
				a.IDStore.evictExpiredStreamState()
				a.loginLimiter.cleanup()
				if a.PlaybackLimiter != nil {
					a.PlaybackLimiter.Cleanup()
				}
			}
		}
	}()

	// Graceful shutdown on SIGINT/SIGTERM
	shutdownCh := make(chan os.Signal, 1)
	signal.Notify(shutdownCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(shutdownCh)
	go func() {
		<-shutdownCh
		a.Logger.Infof("Shutdown signal received, draining connections...")
		evictCancel()
		a.Upstream.stopHealthChecks()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()

	a.Logger.Infof("Go Emby-in-One listening on port %d", cfg.Server.Port)
	err := server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

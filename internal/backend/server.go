package backend

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
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
	Auth            *AuthManager
	Upstream        *UpstreamPool
	UserStore       *UserStore
	WatchStore      *WatchStore
	HiddenLibraries *HiddenLibraryStore
	libraryCache    *upstreamLibraryCache
	PlaybackLimiter *PlaybackLimiter
	loginLimiter    loginRateLimiter
	noticeThrottle  noticeThrottle
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
	identity := NewClientIdentityServiceFromDetectedConfig()
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

	return &App{
		ConfigStore:     configStore,
		Logger:          logger,
		IDStore:         idStore,
		Identity:        identity,
		Auth:            auth,
		Upstream:        upstream,
		UserStore:       userStore,
		WatchStore:      watchStore,
		HiddenLibraries: hiddenLibraries,
		libraryCache:    newUpstreamLibraryCache(),
		PlaybackLimiter: NewPlaybackLimiter(),
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
	if a.Upstream != nil {
		a.Upstream.stopHealthChecks()
	}
	if a.IDStore != nil {
		_ = a.IDStore.Close()
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

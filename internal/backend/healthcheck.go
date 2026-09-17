package backend

import (
	"context"
	"sync/atomic"
	"time"
)

// liveHealthCheckRunners counts the health-check goroutines that are currently running.
// restartHealthChecks supersedes a runner by cancelling it, and the only way to tell a
// runner that exited from one that was dropped without ever being cancelled is to count
// them: a superseded runner decrements this, a leaked one never does.
var liveHealthCheckRunners atomic.Int64

func liveHealthCheckRunnerCount() int {
	return int(liveHealthCheckRunners.Load())
}

type healthCheckRunner struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// restartHealthChecks swaps in a fresh health-check runner. Stopping the previous
// runner and installing the new one happen inside a single critical section: with the
// two steps split, two concurrent restarts (a config save racing a shutdown, or two
// admin requests) could interleave as "A stops, B stops, B installs, A installs",
// which dropped B's runner without ever cancelling it. Its goroutine then ran for the
// rest of the process lifetime, re-logging in to every offline upstream beside the
// live runner, leaking one more goroutine per lost race.
//
// The old runner is cancelled after the lock is released. Waiting for its done channel
// while holding p.mu would deadlock: the runner calls runHealthCheckCycle, which takes
// p.mu itself.
func (p *UpstreamPool) restartHealthChecks(timeouts TimeoutsConfig) {
	interval := time.Duration(timeouts.HealthInterval) * time.Millisecond
	var runner *healthCheckRunner
	var ctx context.Context
	if interval > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(context.Background())
		runner = &healthCheckRunner{cancel: cancel, done: make(chan struct{})}
	}

	p.mu.Lock()
	previous := p.health
	p.health = runner
	p.mu.Unlock()

	if previous != nil {
		previous.cancel()
	}
	if runner == nil {
		// interval <= 0 disables periodic health checks; the previous runner is gone.
		return
	}
	go func() {
		liveHealthCheckRunners.Add(1)
		defer liveHealthCheckRunners.Add(-1)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		defer close(runner.done)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				p.runHealthCheckCycle(ctx)
			}
		}
	}()
}

func (p *UpstreamPool) stopHealthChecks() {
	p.mu.Lock()
	runner := p.health
	p.health = nil
	p.mu.Unlock()
	if runner != nil {
		runner.cancel()
		<-runner.done
	}
}

func (p *UpstreamPool) runHealthCheckCycle(ctx context.Context) {
	p.mu.RLock()
	clients := append([]*UpstreamClient(nil), p.clients...)
	logger := p.logger
	p.mu.RUnlock()
	identity := p.identityService()
	offline := 0
	for _, client := range clients {
		if client == nil {
			continue
		}
		// Stream-line liveness probes run for online upstreams with fallback
		// bases configured; the offline re-login path below is skipped for them.
		// Probing offline upstreams' lines would mark them dead on a server that
		// is merely logged out, and the login probe already covers the API base.
		if client.IsOnline() {
			client.probeStreamBases(ctx)
			continue
		}
		// Skip passthrough upstreams without any captured client identity
		if client.Config.SpoofClient == "passthrough" && client.Config.APIKey == "" {
			if identity == nil || !identity.HasCapturedHeaders(client.serverKey) {
				if logger != nil {
					logger.Debugf("[%s] Health check: passthrough skip — no captured client identity yet", client.Name)
				}
				continue
			}
		}
		offline++
		if logger != nil {
			logger.Debugf("[%s] Health check: offline, attempting re-login...", client.Name)
		}
		wasBefore := client.IsOnline()
		probeCtx, cancelProbe := context.WithTimeout(ctx, client.healthCheckTimeout())
		client.Login(probeCtx, nil, identity)
		cancelProbe()
		isNow := client.IsOnline()
		if !wasBefore && isNow && logger != nil {
			logger.Infof("[%s] Health status changed: OFFLINE → ONLINE", client.Name)
		}
	}
	if logger != nil && offline > 0 {
		logger.Debugf("Health check cycle: %d/%d servers offline, retried", offline, len(clients))
	}
}

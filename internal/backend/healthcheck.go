package backend

import (
	"context"
	"time"
)

type healthCheckRunner struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func (p *UpstreamPool) restartHealthChecks(timeouts TimeoutsConfig) {
	p.stopHealthChecks()
	interval := time.Duration(timeouts.HealthInterval) * time.Millisecond
	if interval <= 0 {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	runner := &healthCheckRunner{cancel: cancel, done: make(chan struct{})}
	p.mu.Lock()
	p.health = runner
	p.mu.Unlock()
	go func() {
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
		if client == nil || client.IsOnline() {
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

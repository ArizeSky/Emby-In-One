package backend

import (
	"net/http"
	"sync"
)

// uaReconnectConcurrency caps how many targeted logins run at once after a UA
// change (execution plan §7.3: "定向登录并发上限 4").
const uaReconnectConcurrency = 4

// preparedUAChanges holds the client instances a UA change swaps into the
// pool: replacements for servers that already had one, additions for servers
// entering the pool. Servers not listed keep their current instance, so their
// stream-line failure marks and recovery debounce state survive the change.
type preparedUAChanges struct {
	replacements []*UpstreamClient
	additions    []*UpstreamClient
}

// prepareUAChanges builds fresh clients for the listed servers from the
// candidate config. Resident servers fix their identity from profiles — the
// copies about to be committed — instead of re-reading the library, so the
// pool never observes a half-committed UA change. A server whose resident
// reference is missing gets no snapshot and refuses to log in, exactly like a
// client built by Reload. No pool state is modified here, so a failed save can
// still abandon the change cleanly.
func (p *UpstreamPool) prepareUAChanges(cfg Config, ids []string, profiles map[string]ResidentUA) *preparedUAChanges {
	p.mu.RLock()
	oldByID := make(map[string]*UpstreamClient, len(p.clients))
	for _, c := range p.clients {
		if c != nil {
			oldByID[c.ID] = c
		}
	}
	p.mu.RUnlock()

	indexByID := make(map[string]int, len(cfg.Upstream))
	for i, upstream := range cfg.Upstream {
		indexByID[upstream.ID] = i
	}

	change := &preparedUAChanges{}
	for _, id := range ids {
		index, ok := indexByID[id]
		if !ok {
			continue
		}
		upstream := cfg.Upstream[index]
		newClient := newUpstreamClient(cfg, upstream, index, p.logger)
		newClient.onAuthError = p.handleUpstreamAuthError
		if upstream.SpoofClient == "resident" {
			if profile, ok := profiles[upstream.UAProfileID]; ok {
				newClient.residentIdentity = newResidentIdentitySnapshot(profile)
			} else {
				newClient.residentIdentity = nil
			}
		}
		if old, exists := oldByID[id]; exists {
			// A changed identity must not inherit the old session's token and
			// user; an unchanged one keeps the session alive.
			if !residentIdentityChanged(old, newClient) {
				old.mu.RLock()
				newClient.AccessToken = old.AccessToken
				newClient.UserID = old.UserID
				newClient.Online = old.Online
				newClient.LastError = old.LastError
				old.mu.RUnlock()
			}
			change.replacements = append(change.replacements, newClient)
		} else {
			change.additions = append(change.additions, newClient)
		}
	}
	return change
}

// publishUAChanges swaps the prepared clients into the pool under the write
// lock. No network request and no library access happens here. Transports of
// replaced clients that nothing else still uses have their idle connections
// closed, mirroring Reload, so a swap does not strand sockets until
// IdleConnTimeout.
func (p *UpstreamPool) publishUAChanges(change *preparedUAChanges) {
	if change == nil || (len(change.replacements) == 0 && len(change.additions) == 0) {
		return
	}
	p.mu.Lock()
	var retired []*UpstreamClient
	for i, client := range p.clients {
		if client == nil {
			continue
		}
		for _, replacement := range change.replacements {
			if replacement.ID == client.ID {
				retired = append(retired, client)
				p.clients[i] = replacement
				break
			}
		}
	}
	p.clients = append(p.clients, change.additions...)
	stillInUse := make(map[http.RoundTripper]bool, len(p.clients))
	for _, client := range p.clients {
		if client != nil && client.transport != nil {
			stillInUse[client.transport] = true
		}
	}
	p.mu.Unlock()

	for _, old := range retired {
		if old == nil || old.transport == nil || old.transport == sharedTransport || stillInUse[old.transport] {
			continue
		}
		if closer, ok := old.transport.(interface{ CloseIdleConnections() }); ok {
			closer.CloseIdleConnections()
		}
	}
}

// startUAReconnectJobs runs the targeted logins for a UA change: at most
// uaReconnectConcurrency at a time, each individually bounded by the login
// timeout inside Login itself, and the whole batch derived from the app's
// shutdown context so Close cancels and waits for it. The jobs outlive the
// HTTP request that started them. Each job re-resolves its client at execution
// time, so a pool rebuilt in the meantime is logged in as it is currently
// configured, and a server deleted in the meantime is skipped. A job writes
// its result to the instance it logs in — never to another server's client by
// ID (execution plan §7.3).
func (a *App) startUAReconnectJobs(serverIDs []string) {
	a.uaJobsWG.Add(1)
	go func() {
		defer a.uaJobsWG.Done()
		sem := make(chan struct{}, uaReconnectConcurrency)
		var batch sync.WaitGroup
		for _, id := range serverIDs {
			batch.Add(1)
			sem <- struct{}{}
			go func(serverID string) {
				defer batch.Done()
				defer func() { <-sem }()
				client := a.Upstream.ClientByID(serverID)
				if client == nil {
					return
				}
				client.Login(a.uaJobsCtx, nil, a.Identity)
			}(id)
		}
		batch.Wait()
	}()
}

// residentProfilesByID copies the library's current resident records keyed by
// ID, for prepareUAChanges. A nil library yields an empty map: every resident
// reference then resolves to "missing", which is the correct signal while the
// library cannot be read.
func residentProfilesByID(library *UALibraryStore) map[string]ResidentUA {
	profiles := map[string]ResidentUA{}
	if library == nil {
		return profiles
	}
	snapshot := library.Snapshot()
	for _, resident := range snapshot.Resident {
		profiles[resident.ID] = resident
	}
	return profiles
}

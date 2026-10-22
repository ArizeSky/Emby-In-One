package backend

import (
	"sort"
)

// uaExpectedBinding is the binding a panel saw when it loaded the assignment
// dialog. The server rejects the batch if any of them has changed since.
type uaExpectedBinding struct {
	ServerID    string `json:"serverId"`
	SpoofClient string `json:"spoofClient"`
	UAProfileID string `json:"uaProfileId"`
}

// uaAssignmentsInput is the incremental batch assignment payload. Only the
// servers named in assignServerIds / releaseServerIds are touched; every other
// server is left exactly as it was.
type uaAssignmentsInput struct {
	AssignServerIDs         []string            `json:"assignServerIds"`
	ReleaseServerIDs        []string            `json:"releaseServerIds"`
	ExpectedProfileRevision *uint64             `json:"expectedProfileRevision"`
	ExpectedBindings        []uaExpectedBinding `json:"expectedBindings"`
}

type uaBoundServer struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type uaServerSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	SpoofClient string `json:"spoofClient"`
	UAProfileID string `json:"uaProfileId"`
	Online      bool   `json:"online"`
}

type uaChangeResult struct {
	AffectedServerIDs     []string
	ReconnectingServerIDs []string
}

// boundServersForUA returns the servers currently bound to one resident profile.
// The binding lives only in cfg.Upstream[i].UAProfileID; it is never duplicated.
func (a *App) boundServersForUA(cfg Config, profileID string) []uaBoundServer {
	out := []uaBoundServer{}
	if profileID == "" {
		return out
	}
	for _, upstream := range cfg.Upstream {
		if upstream.SpoofClient == "resident" && upstream.UAProfileID == profileID {
			out = append(out, uaBoundServer{ID: upstream.ID, Name: upstream.Name})
		}
	}
	return out
}

func (a *App) uaServerSummaries(cfg Config) []uaServerSummary {
	onlineByID := map[string]bool{}
	for _, client := range a.Upstream.Clients() {
		onlineByID[client.ID] = client.Online
	}
	out := make([]uaServerSummary, 0, len(cfg.Upstream))
	for _, upstream := range cfg.Upstream {
		out = append(out, uaServerSummary{
			ID:          upstream.ID,
			Name:        upstream.Name,
			SpoofClient: upstream.SpoofClient,
			UAProfileID: upstream.UAProfileID,
			Online:      onlineByID[upstream.ID],
		})
	}
	return out
}

// swapUAClients swaps the listed servers' pool clients for fresh ones built
// from cfg (resident servers fixed to the given profile copies) and starts
// their targeted logins. It returns the IDs whose login could be attempted.
// Callers hold adminMutationMu, so the pool cannot be rebuilt between prepare
// and publish.
func (a *App) swapUAClients(cfg Config, serverIDs []string, profiles map[string]ResidentUA) []string {
	change := a.Upstream.prepareUAChanges(cfg, serverIDs, profiles)
	if len(change.replacements)+len(change.additions) != len(serverIDs) {
		// Defensive: an ID vanished between validation and publish (cannot
		// happen under adminMutationMu); a full reload still converges.
		a.Upstream.Reload(cfg)
	} else {
		a.Upstream.publishUAChanges(change)
	}
	reconnecting := make([]string, 0, len(serverIDs))
	for _, id := range serverIDs {
		client := a.Upstream.ClientByID(id)
		if client == nil {
			continue
		}
		// A released server switched to passthrough with no captured identity
		// must not send a credential login without any client identity: the
		// same invariant LoginAll, manual reconnect and the health check keep.
		if !a.Upstream.canAttemptIdentityLogin(client) {
			if a.Logger != nil {
				a.Logger.Infof("[%s] UA change skipped login — waiting for a usable identity", client.Name)
			}
			continue
		}
		reconnecting = append(reconnecting, id)
	}
	if len(reconnecting) > 0 {
		a.startUAReconnectJobs(reconnecting)
	}
	return reconnecting
}

// commitConfigForUAChanges persists a binding change atomically, swaps only
// the affected clients in the pool and starts their targeted logins. A save
// failure changes nothing. A login failure leaves the new binding in place
// and the server offline; it never reverts to the old identity.
func (a *App) commitConfigForUAChanges(nextCfg Config, serverIDs []string) ([]string, error) {
	if err := a.ConfigStore.SaveAndReplace(nextCfg); err != nil {
		return nil, err
	}
	return a.swapUAClients(nextCfg, serverIDs, residentProfilesByID(a.UALibrary)), nil
}

// applyResidentIdentityToServers swaps in new clients carrying the given
// resident profile for the listed servers and starts their targeted logins.
// It writes no file: the binding did not change, only the library content
// did, so a single action never spans both files (execution plan §7.3).
func (a *App) applyResidentIdentityToServers(cfg Config, profile ResidentUA, serverIDs []string) []string {
	return a.swapUAClients(cfg, serverIDs, map[string]ResidentUA{profile.ID: profile})
}

// applyUAAssignments applies an incremental batch of server bindings for one
// resident profile. All validation runs before anything is persisted, so a batch
// either applies in full or not at all.
func (a *App) applyUAAssignments(profileID string, input uaAssignmentsInput) (uaChangeResult, error) {
	result := uaChangeResult{}
	library := a.UALibrary
	if library == nil || !library.Available() {
		return result, errUALibraryUnavailable
	}
	profile, ok := library.ResidentByID(profileID)
	if !ok {
		return result, errUANotFound
	}
	if input.ExpectedProfileRevision != nil && *input.ExpectedProfileRevision != profile.Revision {
		return result, errUAConflict
	}

	cfg := a.ConfigStore.Snapshot()
	indexByID := map[string]int{}
	for i, upstream := range cfg.Upstream {
		indexByID[upstream.ID] = i
	}

	assignSet := map[string]bool{}
	for _, id := range input.AssignServerIDs {
		if assignSet[id] {
			return result, &httpError{message: "assignServerIds 含重复的服务器"}
		}
		assignSet[id] = true
		if _, ok := indexByID[id]; !ok {
			return result, &httpError{message: "服务器不存在：" + id}
		}
	}
	releaseSet := map[string]bool{}
	for _, id := range input.ReleaseServerIDs {
		if releaseSet[id] {
			return result, &httpError{message: "releaseServerIds 含重复的服务器"}
		}
		releaseSet[id] = true
		index, ok := indexByID[id]
		if !ok {
			return result, &httpError{message: "服务器不存在：" + id}
		}
		if cfg.Upstream[index].SpoofClient != "resident" || cfg.Upstream[index].UAProfileID != profileID {
			return result, &httpError{message: "服务器 " + cfg.Upstream[index].Name + " 未绑定到当前常驻 UA"}
		}
	}
	for id := range assignSet {
		if releaseSet[id] {
			return result, &httpError{message: "同一服务器不能同时分配和解除"}
		}
	}
	for _, binding := range input.ExpectedBindings {
		index, ok := indexByID[binding.ServerID]
		if !ok {
			return result, &httpError{message: "服务器不存在：" + binding.ServerID}
		}
		current := cfg.Upstream[index]
		if current.SpoofClient != binding.SpoofClient || current.UAProfileID != binding.UAProfileID {
			return result, errUAConflict
		}
	}

	nextCfg := cfg
	nextCfg.Upstream = append([]UpstreamConfig(nil), cfg.Upstream...)
	affected := make([]string, 0, len(assignSet)+len(releaseSet))
	for id := range assignSet {
		index := indexByID[id]
		nextCfg.Upstream[index].SpoofClient = "resident"
		nextCfg.Upstream[index].UAProfileID = profileID
		affected = append(affected, id)
	}
	for id := range releaseSet {
		index := indexByID[id]
		nextCfg.Upstream[index].SpoofClient = "passthrough"
		nextCfg.Upstream[index].UAProfileID = ""
		affected = append(affected, id)
	}
	if len(affected) == 0 {
		return result, nil
	}
	sort.Strings(affected)

	reconnecting, err := a.commitConfigForUAChanges(nextCfg, affected)
	if err != nil {
		return result, err
	}
	result.AffectedServerIDs = affected
	result.ReconnectingServerIDs = reconnecting
	return result, nil
}

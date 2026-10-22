package backend

import (
	"errors"
	"io"
	"net/http"
	"time"
)

func (a *App) uaLibraryUnavailable(w http.ResponseWriter) bool {
	if a.UALibrary == nil || !a.UALibrary.Available() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "UA 身份库不可用"})
		return true
	}
	return false
}

func (a *App) residentToDTO(resident ResidentUA, cfg Config, now time.Time) map[string]any {
	known, old, days := uaAge(resident.IdentityCapturedAt, now)
	return map[string]any{
		"id":                 resident.ID,
		"name":               resident.Name,
		"userAgent":          resident.Identity.UserAgent,
		"client":             resident.Identity.Client,
		"clientVersion":      resident.Identity.ClientVersion,
		"deviceName":         resident.Identity.DeviceName,
		"deviceId":           resident.Identity.DeviceID,
		"accept":             resident.Identity.Accept,
		"acceptLanguage":     resident.Identity.AcceptLanguage,
		"fingerprint":        resident.Fingerprint,
		"identityCapturedAt": resident.IdentityCapturedAt,
		"createdAt":          resident.CreatedAt,
		"updatedAt":          resident.UpdatedAt,
		"revision":           resident.Revision,
		"identityVersion":    resident.IdentityVersion,
		"isOld":              old,
		"ageKnown":           known,
		"ageDays":            days,
		"boundServers":       a.boundServersForUA(cfg, resident.ID),
	}
}

func recentToDTO(recent RecentUA, residentByFingerprint map[string]string) map[string]any {
	dto := map[string]any{
		"id":              recent.ID,
		"fingerprint":     recent.Fingerprint,
		"userAgent":       recent.Identity.UserAgent,
		"client":          recent.Identity.Client,
		"clientVersion":   recent.Identity.ClientVersion,
		"deviceName":      recent.Identity.DeviceName,
		"deviceId":        recent.Identity.DeviceID,
		"accept":          recent.Identity.Accept,
		"acceptLanguage":  recent.Identity.AcceptLanguage,
		"firstCapturedAt": recent.FirstCapturedAt,
		"lastCapturedAt":  recent.LastCapturedAt,
	}
	if id, ok := residentByFingerprint[recent.Fingerprint]; ok {
		dto["alreadyResidentId"] = id
	}
	return dto
}

// writeUAError maps a store/app error to the HTTP response contract: 404 for a
// missing record, 409 for a conflict or an in-use resident, 503 for an
// unavailable library, 400 for a validation error, 500 for a storage failure.
func (a *App) writeUAError(w http.ResponseWriter, err error) {
	var duplicate *uaDuplicateResidentError
	var httpErr *httpError
	switch {
	case errors.Is(err, errUALibraryUnavailable):
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": err.Error()})
	case errors.Is(err, errUANotFound):
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
	case errors.Is(err, errUAConflict):
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "conflict": true})
	case errors.Is(err, errUAInUse):
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
	case errors.As(err, &duplicate):
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "existingId": duplicate.ExistingID, "conflict": true})
	case errors.As(err, &httpErr):
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
	}
}

func (a *App) handleAdminUAList(w http.ResponseWriter, r *http.Request) {
	if a.uaLibraryUnavailable(w) {
		return
	}
	snapshot := a.UALibrary.Snapshot()
	cfg := a.ConfigStore.Snapshot()
	now := time.Now().UTC()

	residentByFingerprint := map[string]string{}
	for _, resident := range snapshot.Resident {
		residentByFingerprint[resident.Fingerprint] = resident.ID
	}

	resident := make([]map[string]any, 0, len(snapshot.Resident))
	for _, item := range snapshot.Resident {
		resident = append(resident, a.residentToDTO(item, cfg, now))
	}
	recent := make([]map[string]any, 0, len(snapshot.Recent))
	for _, item := range snapshot.Recent {
		recent = append(recent, recentToDTO(item, residentByFingerprint))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"recent":       recent,
		"resident":     resident,
		"servers":      a.uaServerSummaries(cfg),
		"recentLimit":  recentUALimit,
		"oldAfterDays": int(oldUAThreshold / (24 * time.Hour)),
		"serverTime":   now,
	})
}

func (a *App) handleAdminUAResidentSave(w http.ResponseWriter, r *http.Request) {
	if a.uaLibraryUnavailable(w) {
		return
	}
	var body struct {
		RecentID string `json:"recentId"`
		Name     string `json:"name"`
	}
	if err := decodeJSONBody(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid request body"})
		return
	}
	resident, alreadySaved, err := a.UALibrary.SaveRecent(saveRecentUAInput{RecentID: body.RecentID, Name: body.Name})
	if err != nil {
		a.writeUAError(w, err)
		return
	}
	cfg := a.ConfigStore.Snapshot()
	status := http.StatusCreated
	if alreadySaved {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{
		"success":      true,
		"alreadySaved": alreadySaved,
		"resident":     a.residentToDTO(resident, cfg, time.Now().UTC()),
	})
}

func (a *App) handleAdminUAResidentRename(w http.ResponseWriter, r *http.Request) {
	if a.uaLibraryUnavailable(w) {
		return
	}
	id := r.PathValue("id")
	var body struct {
		Name             string `json:"name"`
		ExpectedRevision uint64 `json:"expectedRevision"`
	}
	if err := decodeJSONBody(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid request body"})
		return
	}
	resident, err := a.UALibrary.RenameResident(renameResidentUAInput{ID: id, Name: body.Name, ExpectedRevision: body.ExpectedRevision})
	if err != nil {
		a.writeUAError(w, err)
		return
	}
	cfg := a.ConfigStore.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"success":               true,
		"applied":               true,
		"affectedServerIds":     []string{},
		"reconnectingServerIds": []string{},
		"resident":              a.residentToDTO(resident, cfg, time.Now().UTC()),
	})
}

func (a *App) handleAdminUAResidentReplace(w http.ResponseWriter, r *http.Request) {
	if a.uaLibraryUnavailable(w) {
		return
	}
	id := r.PathValue("id")
	var body struct {
		RecentID         string `json:"recentId"`
		ExpectedRevision uint64 `json:"expectedRevision"`
	}
	if err := decodeJSONBody(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid request body"})
		return
	}
	before, ok := a.UALibrary.ResidentByID(id)
	if !ok {
		a.writeUAError(w, errUANotFound)
		return
	}
	resident, err := a.UALibrary.ReplaceResident(replaceResidentUAInput{
		ResidentID:       id,
		RecentID:         body.RecentID,
		ExpectedRevision: body.ExpectedRevision,
	})
	if err != nil {
		a.writeUAError(w, err)
		return
	}

	cfg := a.ConfigStore.Snapshot()
	affected := []string{}
	reconnecting := []string{}
	if resident.IdentityVersion != before.IdentityVersion {
		for _, bound := range a.boundServersForUA(cfg, resident.ID) {
			affected = append(affected, bound.ID)
		}
		if len(affected) > 0 {
			// The binding did not change — only the library content did — so
			// this action writes no second file: the pool swaps the affected
			// clients directly and re-logs them in (execution plan §7.3).
			reconnecting = a.applyResidentIdentityToServers(cfg, resident, affected)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success":               true,
		"applied":               true,
		"affectedServerIds":     affected,
		"reconnectingServerIds": reconnecting,
		"resident":              a.residentToDTO(resident, cfg, time.Now().UTC()),
	})
}

func (a *App) handleAdminUAResidentDelete(w http.ResponseWriter, r *http.Request) {
	if a.uaLibraryUnavailable(w) {
		return
	}
	id := r.PathValue("id")
	var body struct {
		ExpectedRevision uint64 `json:"expectedRevision"`
	}
	if err := decodeJSONBody(r, &body); err != nil {
		// An empty DELETE body is not a malformed request: treat it as "no
		// revision supplied". Revision 0 never matches a stored record, so the
		// request still fails as a conflict instead of a 400 before the
		// record is even looked up.
		if !errors.Is(err, io.EOF) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid request body"})
			return
		}
	}
	cfg := a.ConfigStore.Snapshot()
	bound := a.boundServersForUA(cfg, id)
	if len(bound) > 0 {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":        "常驻 UA 正在被服务器引用，无法删除",
			"boundServers": bound,
		})
		return
	}
	if err := a.UALibrary.DeleteResident(id, body.ExpectedRevision); err != nil {
		a.writeUAError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "applied": true})
}

func (a *App) handleAdminUAResidentAssignments(w http.ResponseWriter, r *http.Request) {
	if a.uaLibraryUnavailable(w) {
		return
	}
	id := r.PathValue("id")
	var input uaAssignmentsInput
	if err := decodeJSONBody(r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid request body"})
		return
	}
	result, err := a.applyUAAssignments(id, input)
	if err != nil {
		a.writeUAError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":               true,
		"applied":               true,
		"affectedServerIds":     result.AffectedServerIDs,
		"reconnectingServerIds": result.ReconnectingServerIDs,
	})
}

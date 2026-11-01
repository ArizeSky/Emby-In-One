package backend

import (
	"sync"
	"time"
)

const playbackHeartbeatTimeout = 3 * time.Minute

// streamKey is the capacity unit of the concurrent playback limit: one user on
// one upstream. This is the existing product semantics and is deliberately not
// per HTTP stream or per item.
type streamKey struct {
	UserID   string
	ServerID string
}

// streamEntry is one capacity unit plus the ownership of the playback attempt
// that currently holds it.
//
// Version identifies the reservation that produced the current unit. It is
// bumped by a PlaybackInfo reservation (TryReserve) and by a stream request
// that starts a different item; repeated per-resource stream requests are
// idempotent touches and keep the version stable. ReleaseReservation compares
// the version so the failure cleanup of an older reservation cannot delete a
// later, successful one.
//
// Claim is the last client identity accepted for this unit and ClaimVersion the
// Version it was accepted at. A claim whose ClaimVersion no longer equals
// Version belongs to a superseded reservation: per design W05 it can no longer
// refresh or release the unit. That is what keeps a same-item replay safe —
// item ID alone is not enough, because a replay keeps the item.
type streamEntry struct {
	ItemID        string
	LastHeartbeat time.Time
	Version       uint64
	Claim         playbackSessionIdentity
	ClaimVersion  uint64

	// Reserved marks a unit whose current version came from a PlaybackInfo
	// reservation and whose new attempt has not claimed it yet. While it is set,
	// a per-resource stream request for another item is a late segment of the
	// attempt the reservation replaced, not the start of a new attempt, so it
	// must not bump the version or adopt a claim.
	Reserved bool

	// ReservedPlaySessionID is the virtual PlaySessionId the reservation's
	// PlaybackInfo response handed to the client, when the response carried one.
	// It is positive proof of the current attempt: a late event or segment of the
	// attempt the reservation replaced carries the previous PlaySessionId, even
	// when the item and the device SessionId are unchanged. Empty means the
	// response did not prove an identity, so event matching falls back to the
	// conservative contradiction rule in acceptsNewAttempt.
	ReservedPlaySessionID string
}

func (e *streamEntry) liveClaim() bool {
	return !e.Claim.empty() && e.ClaimVersion == e.Version
}

// acceptsNewAttempt reports whether identity may take over a frozen claim.
//
// A frozen claim belongs to the attempt a newer reservation replaced. The
// reservation's PlaySessionId is positive proof of the new attempt, because a
// same-item replay keeps both the item ID and the device SessionId; two
// different PlaySessionIds are the replay itself. Without that binding, only a
// direct contradiction with the frozen claim (a shared identity field with a
// different value) proves a later attempt. A matching identity is the
// superseded attempt, and an identity with disjoint or missing fields is merely
// unproven — "not the old session" is not the same as "the new one", so it is
// treated conservatively as the superseded attempt too.
func (e *streamEntry) acceptsNewAttempt(identity playbackSessionIdentity) bool {
	if identity.empty() {
		return false
	}
	if e.ReservedPlaySessionID != "" {
		return identity.PlaySessionID == e.ReservedPlaySessionID
	}
	return e.Claim.conflicts(identity)
}

// reservedFor reports whether identity carries the PlaySessionId the current
// reservation handed to the client. Unbound reservations return false.
func (e *streamEntry) reservedFor(identity playbackSessionIdentity) bool {
	return e.ReservedPlaySessionID != "" && identity.PlaySessionID == e.ReservedPlaySessionID
}

// boundReservation reports whether the current reservation proved the
// identity of its own attempt by handing a PlaySessionId to the client.
func (e *streamEntry) boundReservation() bool {
	return e.Reserved && e.ReservedPlaySessionID != ""
}

// reservationProvesOtherAttempt reports whether identity cannot belong to the
// attempt the current reservation bound with a PlaySessionId. A bound
// reservation only accepts that exact PlaySessionId: an anonymous event and an
// identity that merely omits the PlaySessionId are equally unproven, so they
// must not refresh the unit or write local state. Unbound reservations return
// false and keep the conservative claim-contradiction rules elsewhere.
func (e *streamEntry) reservationProvesOtherAttempt(identity playbackSessionIdentity) bool {
	return e.boundReservation() && !e.reservedFor(identity)
}

// playbackSlotReservation is the token returned to PlaybackInfo for the unit it
// reserved. The zero value means "no unit": no limit configured, admin, or the
// limiter is absent, and releasing it is a no-op.
type playbackSlotReservation struct {
	userID   string
	serverID string
	version  uint64
}

func (r playbackSlotReservation) valid() bool { return r.version != 0 }

// playbackEventKind classifies a session event for the ownership rules.
type playbackEventKind int

const (
	playbackEventStarted playbackEventKind = iota
	playbackEventProgress
	playbackEventStopped
)

// playbackSessionVerdict is how one session event relates to the user's unit.
//
// Tracked reports that a live unit exists for user+server, so the caller should
// trust the remaining fields. LocalProgress reports whether the event may write
// the local watch state: a stale event must not overwrite a newer session's
// position, and this decision is independent of whether a WatchStore exists.
// Release reports that this Stopped event ended the unit (already applied by
// the limiter).
type playbackSessionVerdict struct {
	Tracked       bool
	Accepted      bool
	Release       bool
	LocalProgress bool
}

// untrackedVerdict is returned when there is no unit to protect. Local progress
// keeps its legacy behaviour in that case.
func untrackedVerdict() playbackSessionVerdict {
	return playbackSessionVerdict{LocalProgress: true}
}

type PlaybackLimiter struct {
	mu          sync.Mutex
	streams     map[streamKey]*streamEntry
	nextVersion uint64
}

func NewPlaybackLimiter() *PlaybackLimiter {
	return &PlaybackLimiter{streams: make(map[streamKey]*streamEntry)}
}

func (l *PlaybackLimiter) bumpVersionLocked() uint64 {
	l.nextVersion++
	return l.nextVersion
}

func (l *PlaybackLimiter) activeCountLocked(serverID string, now time.Time) int {
	count := 0
	for key, entry := range l.streams {
		if key.ServerID == serverID && now.Sub(entry.LastHeartbeat) < playbackHeartbeatTimeout {
			count++
		}
	}
	return count
}

// dropExpiredLocked removes the unit for key when its heartbeat elapsed, so the
// caller evaluates the request as a new stream instead of refreshing a unit the
// capacity count already excludes.
func (l *PlaybackLimiter) dropExpiredLocked(key streamKey, now time.Time) *streamEntry {
	entry, ok := l.streams[key]
	if !ok {
		return nil
	}
	if now.Sub(entry.LastHeartbeat) >= playbackHeartbeatTimeout {
		delete(l.streams, key)
		return nil
	}
	return entry
}

// TryReserve reserves a capacity unit for a PlaybackInfo response and returns
// the reservation token that later failure cleanup must present.
//
// Same user on same server keeps a single unit (existing semantics): the unit
// is updated and a new Version freezes any claim of the previous attempt, so a
// late stop/progress of that attempt can no longer act on the unit. The
// capacity check only runs for a user who does not already hold the unit, and
// expired units are removed first.
func (l *PlaybackLimiter) TryReserve(userID, serverID, itemID string, maxConcurrent int) (playbackSlotReservation, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// maxConcurrent <= 0 keeps the documented "no limit" behaviour: nothing is
	// tracked, so there is no unit to leak and no ownership to enforce.
	if maxConcurrent <= 0 {
		return playbackSlotReservation{}, true
	}

	now := time.Now()
	key := streamKey{UserID: userID, ServerID: serverID}
	if entry := l.dropExpiredLocked(key, now); entry != nil {
		version := l.bumpVersionLocked()
		entry.Version = version
		entry.ItemID = itemID
		entry.LastHeartbeat = now
		entry.Reserved = true
		// The new reservation has not returned a PlaySessionId yet; the previous
		// attempt's binding must not survive into it.
		entry.ReservedPlaySessionID = ""
		return playbackSlotReservation{userID: userID, serverID: serverID, version: version}, true
	}

	if l.activeCountLocked(serverID, now) >= maxConcurrent {
		return playbackSlotReservation{}, false
	}
	version := l.bumpVersionLocked()
	l.streams[key] = &streamEntry{ItemID: itemID, LastHeartbeat: now, Version: version, Reserved: true}
	return playbackSlotReservation{userID: userID, serverID: serverID, version: version}, true
}

// TryStart is the legacy stream-path entry point. It keeps the historical
// bool-only contract for per-resource stream requests: like EnsureStreamSlot it
// never bumps the version of an existing unit, so repeated segments cannot
// invalidate the Started claim.
func (l *PlaybackLimiter) TryStart(userID string, serverID string, itemID string, maxConcurrent int) bool {
	_, ok := l.EnsureStreamSlot(userID, serverID, itemID, maxConcurrent, playbackSessionIdentity{})
	return ok
}

// EnsureStreamSlot registers or touches the unit used by a per-resource stream
// request. It returns the unit's version before this request (0 when no unit
// existed) and whether the request is allowed. It must be idempotent: HLS and
// direct streams call it for every segment, range request and subtitle fetch.
//
// A request for a different item than the unit currently holds starts a new
// playback attempt and therefore bumps the version and drops the old claim;
// otherwise the version and claim are kept so a Started claim survives
// repeated segment requests.
func (l *PlaybackLimiter) EnsureStreamSlot(userID string, serverID string, itemID string, maxConcurrent int, identity playbackSessionIdentity) (previousVersion uint64, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if maxConcurrent <= 0 {
		return 0, true
	}

	now := time.Now()
	key := streamKey{UserID: userID, ServerID: serverID}
	if entry := l.dropExpiredLocked(key, now); entry != nil {
		previousVersion = entry.Version
		sameItem := entry.ItemID == "" || itemID == "" || entry.ItemID == itemID

		if entry.liveClaim() {
			if identity.empty() {
				// Anonymous per-resource request: only a same-item touch. It must
				// not adopt the claim or reassign the item of an identified attempt.
				if sameItem {
					entry.LastHeartbeat = now
				}
				return previousVersion, true
			}
			if !entry.Claim.matches(identity) {
				// Belongs to another (or a superseded) attempt. Serve the bytes but
				// leave the unit and its claim untouched.
				return previousVersion, true
			}
			entry.LastHeartbeat = now
			if !sameItem {
				// The same identified play session moved on to another item, which
				// is a new attempt that must freeze any earlier event of the unit.
				version := l.bumpVersionLocked()
				entry.Version = version
				entry.ItemID = itemID
				entry.ClaimVersion = version
			}
			entry.Reserved = false
			return previousVersion, true
		}

		if !entry.Claim.empty() {
			// The claim was frozen by a newer reservation (or the same play
			// session restarted with a new PlaySessionId). A request that still
			// matches the old claim is a late segment and may not reclaim it.
			if identity.empty() || entry.Claim.matches(identity) {
				return previousVersion, true
			}
			// A segment may take the unit over only when it carries the
			// PlaySessionId the reservation handed out. Without that proof a
			// same-item replay cannot be told apart from a stale segment, so the
			// unit stays with the reservation instead of being recaptured.
			if sameItem && entry.reservedFor(identity) {
				entry.Claim = identity
				entry.ClaimVersion = entry.Version
				entry.LastHeartbeat = now
				entry.Reserved = false
			}
			return previousVersion, true
		}

		if entry.Reserved && !sameItem {
			// Fresh reservation for another item: this is a late segment of the
			// playback attempt the reservation replaced.
			return previousVersion, true
		}
		if entry.Reserved && entry.ReservedPlaySessionID != "" && !entry.reservedFor(identity) {
			// Same item, but not the PlaySessionId the reservation returned: a
			// stale segment of the replaced attempt. Serve it without handing it
			// ownership of the reservation's unit.
			return previousVersion, true
		}

		entry.LastHeartbeat = now
		if !identity.empty() {
			entry.Claim = identity
			entry.ClaimVersion = entry.Version
			entry.Reserved = false
		}
		if !sameItem {
			// A stream-only attempt (no PlaybackInfo reservation) started on
			// another item: bump the version so its earlier events go stale.
			version := l.bumpVersionLocked()
			entry.Version = version
			entry.ItemID = itemID
			if !identity.empty() {
				entry.ClaimVersion = version
			}
		}
		return previousVersion, true
	}

	if l.activeCountLocked(serverID, now) >= maxConcurrent {
		return 0, false
	}
	version := l.bumpVersionLocked()
	entry := &streamEntry{ItemID: itemID, LastHeartbeat: now, Version: version}
	if !identity.empty() {
		entry.Claim = identity
		entry.ClaimVersion = version
	}
	l.streams[key] = entry
	return 0, true
}

// BindReservationPlaySession records the PlaySessionId the reservation's
// PlaybackInfo response handed to the client. It is positive proof of the
// current playback attempt and survives the same-item replay case, where the
// item ID and the device SessionId are identical to the superseded attempt.
// It is a no-op for an invalid reservation or one a later reservation replaced.
func (l *PlaybackLimiter) BindReservationPlaySession(reservation playbackSlotReservation, playSessionID string) {
	if l == nil || !reservation.valid() || playSessionID == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	key := streamKey{UserID: reservation.userID, ServerID: reservation.serverID}
	entry, ok := l.streams[key]
	if !ok || entry.Version != reservation.version {
		return
	}
	entry.ReservedPlaySessionID = playSessionID
}

// ReleaseReservation releases the unit a PlaybackInfo call reserved. It
// deletes only the exact reservation it was given: when a later reservation
// already replaced this one, the older failure cleanup leaves the newer unit
// alone (W05).
func (l *PlaybackLimiter) ReleaseReservation(reservation playbackSlotReservation) bool {
	if l == nil || !reservation.valid() {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	key := streamKey{UserID: reservation.userID, ServerID: reservation.serverID}
	entry, ok := l.streams[key]
	if !ok || entry.Version != reservation.version {
		return false
	}
	delete(l.streams, key)
	return true
}

// ClaimStarted applies a Sessions/Playing start to the unit: it records the
// playback identity so later progress/stop events can prove ownership. An
// explicit start may also re-claim a unit whose claim was frozen by a newer
// reservation (the client re-announcing the same playback after a
// renegotiation). A live claim can only be refreshed by the same identity.
func (l *PlaybackLimiter) ClaimStarted(userID, serverID, itemID string, identity playbackSessionIdentity) playbackSessionVerdict {
	return l.trackSessionEvent(userID, serverID, itemID, identity, playbackEventStarted)
}

// TouchProgress applies a Sessions/Playing/Progress event. Progress is weaker
// evidence than Started: it may claim an unclaimed unit, but it never
// re-claims a frozen claim, so a late progress of a superseded same-item replay
// cannot refresh the new unit.
func (l *PlaybackLimiter) TouchProgress(userID, serverID, itemID string, identity playbackSessionIdentity) playbackSessionVerdict {
	return l.trackSessionEvent(userID, serverID, itemID, identity, playbackEventProgress)
}

// StoppedRelease applies a Sessions/Playing/Stopped event. The unit is released
// only when the event provably ends the attempt holding it:
//   - a live claim is released only by the same identity;
//   - a frozen (superseded) claim is never released by the old identity;
//   - an anonymous event (client without SessionId/PlaySessionId) is released
//     only when the unit has no claim history at all, which keeps legacy
//     clients working without letting an unattributable event delete a claimed
//     unit. When a client cannot identify itself, the unit expires through the
//     heartbeat timeout instead.
func (l *PlaybackLimiter) StoppedRelease(userID, serverID, itemID string, identity playbackSessionIdentity) playbackSessionVerdict {
	return l.trackSessionEvent(userID, serverID, itemID, identity, playbackEventStopped)
}

// trackSessionEvent applies the ownership rules shared by the session events.
func (l *PlaybackLimiter) trackSessionEvent(userID, serverID, itemID string, identity playbackSessionIdentity, kind playbackEventKind) playbackSessionVerdict {
	if l == nil {
		return untrackedVerdict()
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	key := streamKey{UserID: userID, ServerID: serverID}
	entry := l.dropExpiredLocked(key, now)
	if entry == nil {
		// No unit to protect (no limit, admins, or an expired unit). Local
		// progress keeps its legacy behaviour; nothing is released here.
		return untrackedVerdict()
	}

	verdict := playbackSessionVerdict{Tracked: true, LocalProgress: true}
	itemMatches := entry.ItemID == "" || itemID == "" || entry.ItemID == itemID

	if !itemMatches {
		// The event belongs to another item than the unit's current attempt.
		// A stop must never release it (the current item is still playing).
		if kind == playbackEventStopped {
			return verdict
		}
		// Only a live claim that matches the event may keep the unit alive and
		// move it to the new item. A late event of a superseded same-user
		// attempt (or of an unattributable client) must not refresh the unit
		// that the newer attempt now holds — item ID alone is not ownership.
		if identity.empty() || !entry.liveClaim() || !entry.Claim.matches(identity) {
			verdict.LocalProgress = false
			return verdict
		}
		version := l.bumpVersionLocked()
		entry.Version = version
		entry.ItemID = itemID
		entry.ClaimVersion = version
		entry.LastHeartbeat = now
		verdict.Accepted = true
		return verdict
	}

	liveClaim := entry.liveClaim()
	claimHistory := !entry.Claim.empty()

	switch kind {
	case playbackEventStarted:
		if liveClaim && !entry.Claim.matches(identity) {
			// Another attempt already claimed this unit: an anonymous or
			// different-identity start cannot take it over.
			verdict.LocalProgress = false
			return verdict
		}
		if !claimHistory && entry.reservationProvesOtherAttempt(identity) {
			// The reservation is already bound to the PlaySessionId it handed
			// out, so a start carrying any other identity (including one missing
			// the PlaySessionId) is the attempt the reservation replaced. It must
			// not become the first claimant of the new reservation.
			verdict.LocalProgress = false
			return verdict
		}
		if claimHistory && !liveClaim {
			// A newer reservation froze the previous attempt's claim. Only a
			// start that proves it belongs to the current attempt may re-claim
			// the unit; a late start of the superseded attempt on the same item
			// (a replay keeps the item, a new PlaySessionId) must not.
			if !entry.acceptsNewAttempt(identity) {
				verdict.LocalProgress = false
				return verdict
			}
		}
		if !identity.empty() {
			entry.Claim = identity
			entry.ClaimVersion = entry.Version
			entry.Reserved = false
		}
		entry.LastHeartbeat = now
		verdict.Accepted = true
		return verdict

	case playbackEventProgress:
		if liveClaim {
			if !identity.empty() && entry.Claim.matches(identity) {
				entry.LastHeartbeat = now
				entry.Reserved = false
				verdict.Accepted = true
				return verdict
			}
			verdict.LocalProgress = false
			return verdict
		}
		if claimHistory {
			// The claim was frozen by a newer reservation. Progress is weaker
			// evidence than a start, so it never takes the unit back: a late
			// progress of a superseded same-item replay would otherwise refresh
			// and re-own the new reservation.
			verdict.LocalProgress = false
			return verdict
		}
		if entry.reservationProvesOtherAttempt(identity) {
			// A bound reservation may only be claimed by its own PlaySessionId.
			// Without claim history the frozen claim cannot be consulted, so the
			// binding is the only available evidence; a different or partial
			// identity is the superseded attempt and must not become the first
			// claimant (and must not refresh the unit).
			verdict.LocalProgress = false
			return verdict
		}
		entry.LastHeartbeat = now
		if !identity.empty() {
			entry.Claim = identity
			entry.ClaimVersion = entry.Version
			entry.Reserved = false
		}
		verdict.Accepted = true
		return verdict

	default: // playbackEventStopped
		if liveClaim {
			if !identity.empty() && entry.Claim.matches(identity) {
				delete(l.streams, key)
				verdict.Accepted = true
				verdict.Release = true
				return verdict
			}
			verdict.LocalProgress = false
			return verdict
		}
		if claimHistory {
			// A frozen claim never proves that this stop owns the current
			// reservation, whether it matches the old attempt (same-item replay)
			// or is a different identity. This also covers an unattributable
			// stop on a unit some earlier attempt claimed. Keep the unit and let
			// the heartbeat timeout reclaim it.
			verdict.LocalProgress = false
			return verdict
		}
		if entry.boundReservation() && !entry.reservedFor(identity) {
			// The reservation handed out a PlaySessionId, so only that attempt
			// can end it. An anonymous (legacy) or different identity may be the
			// superseded attempt; keep the unit for the heartbeat timeout rather
			// than deleting a reservation it cannot prove it owns, and do not
			// overwrite the local position with an unattributable stop.
			verdict.LocalProgress = false
			return verdict
		}
		delete(l.streams, key)
		verdict.Accepted = true
		verdict.Release = true
		return verdict
	}
}

// ReleaseSwitchedSource releases the unit of the server a stream request just
// switched away from. refVersion is the target unit's version before this
// request (0 when the target had no unit).
//
// It must never blindly delete: a per-resource request may be a late segment of
// an older playback. The unit is released only when it belongs to the same item,
// is not newer than the target unit, and either carries the PlaySessionId its
// current reservation bound or has a live claim matching the request (or no
// claim at all). A frozen claim is never enough, even when the late segment
// matches it: a newer reservation may have frozen it while still keeping the
// item, and deleting the source unit then would drop the new attempt's slot.
// A request with no identity leaves the older unit to heartbeat expiry rather
// than risking a newer session's unit.
func (l *PlaybackLimiter) ReleaseSwitchedSource(userID, serverID, itemID string, identity playbackSessionIdentity, refVersion uint64) bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	key := streamKey{UserID: userID, ServerID: serverID}
	entry, ok := l.streams[key]
	if !ok {
		return false
	}
	if entry.ItemID != "" && itemID != "" && entry.ItemID != itemID {
		return false
	}
	if refVersion == 0 || entry.Version > refVersion {
		return false
	}
	// The current reservation's own PlaySessionId proves the current attempt
	// even if an older claim is still recorded. Otherwise a frozen claim belongs
	// to a superseded attempt and cannot authorize deleting the unit.
	if entry.reservedFor(identity) {
		delete(l.streams, key)
		return true
	}
	if !entry.liveClaim() && (!entry.Claim.empty() || entry.ClaimVersion != 0) {
		return false
	}
	if identity.empty() {
		if !entry.Claim.empty() || entry.ClaimVersion != 0 {
			return false
		}
	} else if !entry.Claim.matches(identity) {
		return false
	}
	delete(l.streams, key)
	return true
}

// Heartbeat refreshes a unit without any session identity (legacy helper). Like
// an anonymous progress event it only refreshes a unit without a live claim;
// session handlers use TouchProgress instead.
func (l *PlaybackLimiter) Heartbeat(userID string, serverID string) {
	_ = l.trackSessionEvent(userID, serverID, "", playbackSessionIdentity{}, playbackEventProgress)
}

// Stop releases a unit without any session identity (legacy helper). Session
// handlers use StoppedRelease, which proves ownership before deleting.
func (l *PlaybackLimiter) Stop(userID string, serverID string) {
	_ = l.trackSessionEvent(userID, serverID, "", playbackSessionIdentity{}, playbackEventStopped)
}

// CountForServer returns the number of active (non-expired) streams on a server.
func (l *PlaybackLimiter) CountForServer(serverID string) int {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.activeCountLocked(serverID, time.Now())
}

// Cleanup removes all expired stream entries.
func (l *PlaybackLimiter) Cleanup() {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	for key, entry := range l.streams {
		if now.Sub(entry.LastHeartbeat) >= playbackHeartbeatTimeout {
			delete(l.streams, key)
		}
	}
}

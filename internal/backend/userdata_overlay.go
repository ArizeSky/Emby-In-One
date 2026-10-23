// userdata_overlay.go rebuilds every UserData block in an outbound JSON body
// from the requesting user's own record. A response that keeps any field of the
// shared upstream account's watch state is a privacy leak, so the rebuild never
// "skips": the block is replaced first and only then rebuilt — from the local
// record when one exists, otherwise as Emby's clean default. The recursion only
// recognises UserData-shaped blocks, so an item's own content fields (Name,
// ImageTags, DateCreated, PremiereDate, ...) are never touched.
package backend

import (
	"errors"
	"net/http"
)

// errPrivacyDepthExceeded reports that an outbound JSON payload was nested too
// deeply for the privacy walker. It reports as a local-read failure so the
// caller answers 502 instead of shipping an unsanitised body.
var errPrivacyDepthExceeded = errors.New("response payload exceeds the privacy recursion depth")

// deniedUserStateKeys lists the UserData keys that describe a viewer's state.
// Every value in this list that an upstream DTO carries belongs to the shared
// account, so the rebuild re-derives each one locally and deletes anything it
// cannot derive (Likes/Dislikes/Rating — the proxy does not track them).
var deniedUserStateKeys = []string{
	"Played",
	"PlayedPercentage",
	"UnplayedItemCount",
	"PlaybackPositionTicks",
	"PlayCount",
	"IsFavorite",
	"LastPlayedDate",
	"Likes",
	"Dislikes",
	"Rating",
}

// privacyItem identifies the item a UserData block describes: a canonical
// virtual item id plus the alias ids the IDStore reports for the same media
// item. A block whose identity is unknown always gets the clean default state,
// never the shared upstream state.
type privacyItem struct {
	virtualID string
	aliases   []string
}

// privacyAliases resolves the record window for an item id: the id itself and
// every alias the IDStore groups it under. Nil input yields no aliases, so the
// rebuild writes clean defaults without ever touching the store.
func (a *App) privacyAliases(virtualID string) []string {
	if virtualID == "" {
		return nil
	}
	if a.IDStore != nil {
		return a.IDStore.AliasVirtualIDs(virtualID)
	}
	return []string{virtualID}
}

// localUserState reads the merged record for one item across every alias.
// (nil, nil) means "the user has never touched this item"; only an error
// reports that the record cannot be answered at all.
func (a *App) localUserState(userID string, item privacyItem) (*WatchProgress, error) {
	if a.WatchStore == nil {
		return nil, nil
	}
	aliases := item.aliases
	if len(aliases) == 0 && item.virtualID != "" {
		aliases = []string{item.virtualID}
	}
	// Favorite/played flags are unions; a favorite-only row must never displace
	// the most recently played positive position. Reads never migrate rows.
	var merged *WatchProgress
	var positionTime int64
	for _, alias := range aliases {
		p, err := a.WatchStore.GetProgress(userID, alias)
		if err != nil {
			return nil, err
		}
		if p == nil {
			continue
		}
		if merged == nil {
			merged = &WatchProgress{
				ProxyUserID:   p.ProxyUserID,
				VirtualItemID: p.VirtualItemID,
				ServerID:      p.ServerID,
			}
		}
		merged.IsFavorite = merged.IsFavorite || p.IsFavorite
		merged.Played = merged.Played || p.Played
		if p.LastPlayed > merged.LastPlayed {
			merged.LastPlayed = p.LastPlayed
		}
		if p.PositionTicks > 0 && (merged.PositionTicks == 0 || p.LastPlayed > positionTime) {
			merged.PositionTicks = p.PositionTicks
			merged.RuntimeTicks = p.RuntimeTicks
			positionTime = p.LastPlayed
		}
	}
	return merged, nil
}

// isUserDataBlock reports whether a map looks like a UserData DTO. It needs at
// least two watch-state keys, so an ordinary media item (Name, Ids, ...), a
// MediaSource, or a chapter list never matches.
func isUserDataBlock(m map[string]any) bool {
	if m == nil {
		return false
	}
	markers := 0
	for _, key := range []string{
		"PlaybackPositionTicks", "IsFavorite", "Played",
		"PlayedPercentage", "UnplayedItemCount", "LastPlayedDate", "PlayCount",
	} {
		if _, ok := m[key]; ok {
			markers++
		}
	}
	return markers >= 2
}

// durationPercentage converts a position/runtime pair into Emby's
// PlayedPercentage semantics: 100 when fully played, the position ratio
// otherwise, and 0 when there is nothing to express.
func durationPercentage(positionTicks, runtimeTicks int64, played bool) int64 {
	if played {
		return 100
	}
	if runtimeTicks <= 0 || positionTicks <= 0 {
		return 0
	}
	if positionTicks >= runtimeTicks {
		return 100
	}
	return int64(float64(positionTicks) / float64(runtimeTicks) * 100)
}

// rebuildUserDataBlock replaces a UserData block's contents with the values the
// local record yields. When record is nil (no local record / restricted user
// with no watch history yet) the clean Emby default is written. Shared watch
// fields the local record cannot answer are removed instead of kept, so a
// rebuilt block cannot leak the upstream account's state. Identity keys
// (ItemId, Key) carry through — they describe the item, not the viewer — and
// UserId is overwritten with the requesting user's own ID when one is known.
func rebuildUserDataBlock(block map[string]any, userID string, record *WatchProgress, virtualItemID string) map[string]any {
	positionTicks, runtimeTicks, lastPlayed := int64(0), int64(0), int64(0)
	played, favorite := false, false
	if record != nil {
		positionTicks = record.PositionTicks
		runtimeTicks = record.RuntimeTicks
		lastPlayed = record.LastPlayed
		played = record.Played
		favorite = record.IsFavorite
	}
	rebuilt := map[string]any{}
	// Identity keys survive; a rewritten ItemId inside the upstream block is
	// the proxy-facing id the rewriter minted, keep it verbatim.
	for _, key := range []string{"ItemId", "Key"} {
		if value, ok := block[key]; ok {
			rebuilt[key] = value
		}
	}
	if _, ok := block["UserId"]; ok {
		if userID != "" {
			rebuilt["UserId"] = userID
		} else if id, ok := block["UserId"]; ok {
			rebuilt["UserId"] = id
		}
	}
	if virtualItemID != "" {
		if _, ok := rebuilt["ItemId"]; ok {
			rebuilt["ItemId"] = virtualItemID
		}
	}
	rebuilt["PlaybackPositionTicks"] = positionTicks
	rebuilt["Played"] = played
	rebuilt["IsFavorite"] = favorite
	if played {
		rebuilt["PlayCount"] = int64(1)
	} else {
		rebuilt["PlayCount"] = int64(0)
	}
	rebuilt["PlayedPercentage"] = durationPercentage(positionTicks, runtimeTicks, played)
	rebuilt["UnplayedItemCount"] = int64(0)
	if stamp := lastPlayedUTC(lastPlayed); !stamp.IsZero() {
		rebuilt["LastPlayedDate"] = stamp.Format(embyDateTimeLayout)
		return rebuilt
	}
	return rebuilt
}

// clearUpstreamUserData resets UserData fields to a clean state, preventing the
// upstream admin's watch history from leaking to non-admin users. It covers
// both the standalone UserData DTO shape and an item that carries UserData.
func clearUpstreamUserData(payload any) {
	m, ok := payload.(map[string]any)
	if !ok {
		return
	}
	if isUserDataBlock(m) {
		defaults := map[string]any{}
		for key, value := range m {
			defaults[key] = value
		}
		cleaned := rebuildUserDataBlock(defaults, "", nil, "")
		for key, value := range cleaned {
			m[key] = value
		}
		for _, key := range []string{"LastPlayedDate"} {
			delete(m, key)
		}
	}
	if block, ok := m["UserData"].(map[string]any); ok {
		defaults := map[string]any{}
		for key, value := range block {
			defaults[key] = value
		}
		cleaned := rebuildUserDataBlock(defaults, "", nil, "")
		for key := range block {
			delete(block, key)
		}
		for key, value := range cleaned {
			block[key] = value
		}
		delete(block, "LastPlayedDate")
	}
}

// privacySanitizer walks outbound JSON bodies. Each map is either a UserData
// block (replaced wholesale) or an item/collection that carries child blocks;
// every other value passes through untouched.
type privacySanitizer struct {
	app   *App
	req   *http.Request
	depth int
	err   error
}

// maxPrivacyDepth caps the recursion so a hostile body cannot blow the stack.
const maxPrivacyDepth = 32

// sanitize walks one node. It mutates the input body in place; the surviving
// UserData blocks are always the requesting user's own state.
func (p *privacySanitizer) sanitize(value any, item privacyItem) {
	if p.err != nil || value == nil {
		return
	}
	switch typed := value.(type) {
	case map[string]any:
		p.sanitizeMap(typed, item)
	case []any:
		p.bump()
		for _, entry := range typed {
			p.sanitize(entry, item)
		}
		p.depth--
	}
}

func (p *privacySanitizer) bump() {
	if p.depth++; p.depth > maxPrivacyDepth && p.err == nil {
		p.err = &localWatchReadError{cause: errPrivacyDepthExceeded}
	}
}

func (p *privacySanitizer) sanitizeMap(node map[string]any, item privacyItem) {
	if len(node) == 0 || p.err != nil {
		return
	}
	if isUserDataBlock(node) {
		p.rebuild(node, item)
		return // leaf: no nested content to sanitise
	}
	// An item's own Id names the item its nested UserData describes. Walk the
	// payload after rewriteResponseIDs so the id is the proxy-facing one.
	if id, ok := node["Id"].(string); ok && id != "" {
		item = p.identity(id)
	}
	if _, present := node["UserData"]; present {
		if block, ok := node["UserData"].(map[string]any); ok {
			p.rebuild(block, p.resolveRecordItem(item, block))
		}
	}
	p.bump()
	for key, value := range node {
		if key == "UserData" {
			continue // already handled
		}
		p.sanitize(value, item)
	}
	p.depth--
}

// resolveRecordItem merges the caller's id hint with any ItemId the UserData
// block itself carries: both are windows on the same record.
func (p *privacySanitizer) identity(virtualID string) privacyItem {
	return privacyItem{virtualID: virtualID, aliases: p.app.privacyAliases(virtualID)}
}

// resolveRecordItem merges the caller's id hint with a block's own ItemId.
func (p *privacySanitizer) resolveRecordItem(enclosing privacyItem, block map[string]any) privacyItem {
	primary := enclosing.virtualID
	aliases := append([]string(nil), enclosing.aliases...)
	if primary == "" {
		for _, alias := range aliases {
			if alias != "" {
				primary = alias
				break
			}
		}
	}
	if id, ok := block["ItemId"].(string); ok && id != "" {
		seen := false
		if id == primary {
			seen = true
		}
		if !seen {
			for _, alias := range aliases {
				if alias == id {
					seen = true
					break
				}
			}
		}
		if !seen {
			aliases = append(aliases, id)
			if primary == "" {
				primary = id
			}
		}
	}
	return privacyItem{virtualID: primary, aliases: aliases}
}

// rebuild rebuilds one UserData block. It sets p.err when the record read must
// fail so the caller answers 502 instead of writing a body with stale values.
func (p *privacySanitizer) rebuild(block map[string]any, item privacyItem) {
	userID := p.app.clientFacingUserIDFor(p.req)
	record, err := p.app.localUserState(userID, item)
	if err != nil {
		p.err = &localWatchReadError{cause: err}
		return
	}
	rebuilt := rebuildUserDataBlock(block, userID, record, item.virtualID)
	for key := range block {
		delete(block, key)
	}
	for key, value := range rebuilt {
		block[key] = value
	}
}

// ————— public entry points ————————————————
//
// sanitizeResponseUserData rewrites payload in place for a restricted user and
// recurses through arrays, SearchHints, nested items (MediaSources, Parts,
// ...), and standalone UserData DTOs. It returns an error when the local store
// must be read but fails: the caller answers 502 rather than writing a body
// that still carries the shared state. Admins and anonymous callers pass
// through untouched (privacyPassThrough keeps the existing pass-through
// semantics for the audiences the shared state already belongs to).
func (a *App) sanitizeResponseUserData(r *http.Request, payload any) error {
	if a.responsePrivacyMode(r) == privacyPassThrough {
		return nil
	}
	w := a.privacySanitizer(r)
	w.sanitize(payload, privacyItem{})
	return w.err
}

// sanitizeResponseUserDataItems is the list form of sanitizeResponseUserData:
// each item's own Id names its record identity; an array element without an Id
// still gets clean default state for any UserData it carries.
func (a *App) sanitizeResponseUserDataItems(r *http.Request, items []map[string]any) error {
	if a.responsePrivacyMode(r) == privacyPassThrough {
		return nil
	}
	w := a.privacySanitizer(r)
	for _, item := range items {
		if item == nil {
			continue
		}
		id, _ := item["Id"].(string)
		if id == "" {
			w.sanitize(item, privacyItem{})
			continue
		}
		w.sanitize(item, w.identity(id))
	}
	return w.err
}

// sanitizeResponseUserDataScoped pins the virtual item id for a standalone
// response whose body may not carry the id (items on fallback passthrough
// paths, an upstream UserData write acknowledgement, ...).
func (a *App) sanitizeResponseUserDataScoped(r *http.Request, virtualItemID string, payload any) error {
	if a.responsePrivacyMode(r) == privacyPassThrough {
		return nil
	}
	w := a.privacySanitizer(r)
	w.sanitize(payload, w.identity(virtualItemID))
	return w.err
}

func (a *App) privacySanitizer(r *http.Request) *privacySanitizer {
	return &privacySanitizer{app: a, req: r}
}

// strictSanitizedUserData applies the per-user privacy rebuild and answers
// 502 on a failed local read: a handler must never degrade a read failure into
// shipping the shared upstream state. It writes the error response itself.
func (a *App) strictSanitizedUserData(w http.ResponseWriter, r *http.Request, virtualItemID string, payload any) bool {
	if err := a.sanitizeResponseUserDataScoped(r, virtualItemID, payload); err != nil {
		a.writeLocalWatchReadError(w, err)
		return false
	}
	return true
}

// strictSanitizedUserDataItems is the list form of strictSanitizedUserData.
func (a *App) strictSanitizedUserDataItems(w http.ResponseWriter, r *http.Request, items []map[string]any) bool {
	if err := a.sanitizeResponseUserDataItems(r, items); err != nil {
		a.writeLocalWatchReadError(w, err)
		return false
	}
	return true
}

// localWatchState keeps the alias-aware merge the existing call sites rely on
// (user_filter.go, media_nextup.go, ...). It is now a thin wrapper over
// localUserState so the merge semantics live in exactly one place.
func (a *App) localWatchState(userID, virtualItemID string) (*WatchProgress, error) {
	return a.localUserState(userID, privacyItem{virtualID: virtualItemID, aliases: a.privacyAliases(virtualItemID)})
}

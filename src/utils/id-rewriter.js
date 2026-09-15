const logger = require('./logger');

// Fields whose values are item/entity IDs that need rewriting
const SIMPLE_ID_FIELDS = new Set([
  'Id', 'ItemId', 'ParentId', 'SeriesId', 'SeasonId',
  'MediaSourceId', 'PlaylistItemId',
  'DisplayPreferencesId', 'ParentLogoItemId',
  'ParentBackdropItemId', 'ParentThumbItemId',
  'ChannelId', 'AlbumId', 'ArtistId',
  'PlaylistId', 'CollectionId', 'BoxSetId', 'ThemeSongId', 'ThemeVideoId',
  'InternalId', 'TopParentId', 'BaseItemId', 'CollectionItemId',
  'LiveStreamId', 'LibraryItemId', 'PresentationUniqueKey', 'RemoteId',
  'StreamId'
]);

// Fields whose values are image tags (not IDs) but are keyed by ID
const IMAGE_TAG_FIELDS = new Set([
  'ParentLogoImageTag', 'ParentThumbImageTag',
  'SeriesPrimaryImageTag', 'ParentPrimaryImageTag',
]);

// ServerId should be replaced with proxy's own ServerId
const SERVER_ID_FIELD = 'ServerId';

/**
 * Recursively rewrite IDs in a response object from an upstream server.
 * Optimized for high performance and low memory overhead.
 */
function rewriteResponseIds(obj, serverIndex, idManager, proxyServerId, proxyUserId, seen = new Set()) {
  if (obj == null || typeof obj !== 'object') return obj;
  
  // Circular reference protection
  if (seen.has(obj)) return obj;
  seen.add(obj);

  if (Array.isArray(obj)) {
    for (let i = 0; i < obj.length; i++) {
      rewriteResponseIds(obj[i], serverIndex, idManager, proxyServerId, proxyUserId, seen);
    }
    return obj;
  }

  const keys = Object.keys(obj);
  for (let i = 0; i < keys.length; i++) {
    const key = keys[i];
    const val = obj[key];

    // Fast tracking common fields
    if (key === SERVER_ID_FIELD) {
      if (typeof val === 'string') obj[key] = proxyServerId;
      continue;
    }

    if (key === 'UserId') {
      if (typeof val === 'string') obj[key] = proxyUserId || idManager.getOrCreateVirtualId(val, serverIndex);
      continue;
    }

    if (SIMPLE_ID_FIELDS.has(key)) {
      if (typeof val === 'string' && val !== "" && val !== "0") {
        obj[key] = idManager.getOrCreateVirtualId(val, serverIndex);
      }
      continue;
    }

    // Special blocks
    if (key === 'ImageTags' || key === 'BackdropImageTags' || key === 'ParentBackdropImageTags' || key === 'ImageBlurHashes') {
      continue;
    }

    if (key === 'Trickplay' && val && typeof val === 'object' && !Array.isArray(val)) {
      const newTrickplay = {};
      for (const trickKey in val) {
        const virtualKey = idManager.getOrCreateVirtualId(trickKey, serverIndex);
        newTrickplay[virtualKey] = val[trickKey];
      }
      obj[key] = newTrickplay;
      continue;
    }

    if (key === 'UserData' && val && typeof val === 'object') {
      if (val.ItemId) val.ItemId = idManager.getOrCreateVirtualId(val.ItemId, serverIndex);
      continue;
    }

    if (key === 'SessionId' || key === 'PlaySessionId') {
      if (typeof val === 'string') obj[key] = idManager.getOrCreateVirtualId(val, serverIndex);
      continue;
    }

    // Deep recursion for nested objects
    if (val != null && typeof val === 'object') {
      rewriteResponseIds(val, serverIndex, idManager, proxyServerId, proxyUserId, seen);
    }
  }

  return obj;
}

/**
 * Rewrite virtual IDs in a request body back to original IDs.
 * Returns { rewritten, serverIndex } where serverIndex is determined from the first resolved ID.
 */
function rewriteRequestIds(obj, idManager) {
  if (obj == null || typeof obj !== 'object') return { rewritten: obj, serverIndex: null };

  let detectedServerIndex = null;
  const seen = new Set();

  function rewrite(o) {
    if (o == null || typeof o !== 'object') return o;
    if (seen.has(o)) return o;
    seen.add(o);

    if (Array.isArray(o)) {
      for (let i = 0; i < o.length; i++) {
        o[i] = rewrite(o[i]);
      }
      return o;
    }

    for (const key of Object.keys(o)) {
      const val = o[key];

      if (SIMPLE_ID_FIELDS.has(key) && typeof val === 'string') {
        const resolved = idManager.resolveVirtualId(val);
        if (resolved) {
          o[key] = resolved.originalId;
          if (detectedServerIndex === null) detectedServerIndex = resolved.serverIndex;
        }
        continue;
      }

      if ((key === 'UserId' || key === 'SessionId' || key === 'PlaySessionId') && typeof val === 'string') {
        const resolved = idManager.resolveVirtualId(val);
        if (resolved) {
          o[key] = resolved.originalId;
          if (detectedServerIndex === null) detectedServerIndex = resolved.serverIndex;
        }
        continue;
      }

      if (key === 'MediaSourceId' && typeof val === 'string') {
        const resolved = idManager.resolveVirtualId(val);
        if (resolved) {
          o[key] = resolved.originalId;
          if (detectedServerIndex === null) detectedServerIndex = resolved.serverIndex;
        }
        continue;
      }

      if (typeof val === 'object' && val != null) {
        rewrite(val);
      }
    }

    return o;
  }

  const rewritten = rewrite(obj);
  return { rewritten, serverIndex: detectedServerIndex };
}

/**
 * Batch rewrite IDs for an array of items with corresponding server indices.
 * Modifies items in-place — no deep clone needed.
 */
function rewriteResponseArray(items, serverIndices, idManager, proxyServerId, proxyUserId) {
  for (let i = 0; i < items.length; i++) {
    const seen = new Set(); // Isolate per item to allow GC of earlier items
    rewriteResponseIds(items[i], serverIndices[i], idManager, proxyServerId, proxyUserId, seen);
  }
}

/**
 * Extract and resolve a virtual ID from a string (URL param, query param, etc.)
 * Returns { originalId, serverIndex } or null.
 */
function resolveIdParam(id, idManager) {
  if (!id) return null;
  return idManager.resolveVirtualId(id);
}

module.exports = {
  rewriteResponseIds,
  rewriteResponseArray,
  rewriteRequestIds,
  resolveIdParam,
};

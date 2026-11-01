/* Emby-in-One Web client - head theme bootstrap (T1).
 *
 * This tiny self-hosted script runs in <head>, before ui.css is applied. It
 * reads only this browser's validated appearance cache and prefers-color-scheme
 * so the first paint already has the right light/dark variant. It never calls
 * the network, never reads another account's cache and never hides the page.
 *
 * T4 may extend the cache with a same-origin immutable theme stylesheet; this
 * bootstrap deliberately ignores unknown fields so that a later theme.js can
 * validate and apply them without a flash.
 */
(function () {
  'use strict';

  var SCHEMA = 1;
  var MODES = { light: true, dark: true, system: true };
  var THEME_ID = /^[a-z0-9](?:[a-z0-9-]{1,46})[a-z0-9]$/;
  var RESERVED = { default: true, admin: true, web: true };
  var CACHE_KEYS = {
    anon: 'eio_theme_anon_v1',
    userPrefix: 'eio_theme_user_v1:',
  };

  function validThemeId(id) {
    if (typeof id !== 'string' || id.length < 3 || id.length > 48) return false;
    if (!THEME_ID.test(id)) return false;
    return id === 'builtin' || !RESERVED[id];
  }

  function validUserId(id) {
    return typeof id === 'string' && id.length > 0 && id.length <= 128 && !/[\u0000-\u001f]/.test(id);
  }

  function readRaw(storage, key) {
    try { return storage && typeof storage.getItem === 'function' ? storage.getItem(key) : null; }
    catch (e) { return null; }
  }

  function removeRaw(storage, key) {
    try { if (storage && typeof storage.removeItem === 'function') storage.removeItem(key); }
    catch (e) { /* storage denied: the in-memory default is still safe */ }
  }

  function identityFrom(storage) {
    // A user cache is only valid while this tab still has a session token.
    if (!readRaw(storage, 'eio_token')) return null;
    var id = readRaw(storage, 'eio_user_id');
    return validUserId(id) ? id : null;
  }

  function cacheKey(userId) {
    return userId ? CACHE_KEYS.userPrefix + encodeURIComponent(userId) : CACHE_KEYS.anon;
  }

  function normalizeRecord(raw, userId) {
    if (typeof raw !== 'string' || raw.length === 0 || raw.length > 2048) return null;
    var parsed;
    try { parsed = JSON.parse(raw); } catch (e) { return null; }
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return null;
    if (parsed.schema !== SCHEMA) return null;
    if (userId) {
      if (parsed.scope !== 'user' || parsed.userId !== userId) return null;
    } else if (parsed.scope !== 'anon' || parsed.userId != null) {
      return null;
    }
    var themeId = parsed.themeId == null ? null : parsed.themeId;
    if (themeId !== null && !validThemeId(themeId)) return null;
    var mode = parsed.mode == null ? 'dark' : parsed.mode;
    if (typeof mode !== 'string' || !MODES[mode]) return null;
    return { themeId: themeId, mode: mode };
  }

  function readCache(storage) {
    var userId = identityFrom(storage);
    var key = cacheKey(userId);
    var raw = readRaw(storage, key);
    if (raw == null) return { themeId: null, mode: 'dark', invalid: false, userId: userId };
    var record = normalizeRecord(raw, userId);
    if (!record) {
      removeRaw(storage, key);
      return { themeId: null, mode: 'dark', invalid: true, userId: userId };
    }
    return { themeId: record.themeId, mode: record.mode, invalid: false, userId: userId };
  }

  function systemVariant(win) {
    try {
      var media = win && typeof win.matchMedia === 'function'
        ? win.matchMedia('(prefers-color-scheme: dark)') : null;
      return media && media.matches ? 'dark' : 'light';
    } catch (e) {
      return 'dark';
    }
  }

  function resolveVariant(mode, win) {
    if (mode === 'light' || mode === 'dark') return mode;
    return systemVariant(win);
  }

  function apply(doc, record, variant) {
    var root = doc && doc.documentElement;
    if (!root || !root.dataset) return false;
    root.dataset.eioTheme = variant;
    root.dataset.eioThemeId = record.themeId || 'builtin';
    root.dataset.eioMode = record.mode;
    if (root.style) {
      try { root.style.colorScheme = variant; } catch (e) { /* older engines */ }
    }
    return true;
  }

  function bootstrap(win, doc, storage) {
    var record = readCache(storage);
    var variant = resolveVariant(record.mode, win);
    apply(doc, record, variant);
    return { record: record, variant: variant };
  }

  var EIOThemeBoot = {
    SCHEMA: SCHEMA,
    CACHE_KEYS: CACHE_KEYS,
    validThemeId: validThemeId,
    validUserId: validUserId,
    cacheKey: cacheKey,
    normalizeRecord: normalizeRecord,
    readCache: readCache,
    resolveVariant: resolveVariant,
    bootstrap: bootstrap,
  };

  var root = typeof window !== 'undefined' ? window : globalThis;
  root.EIOThemeBoot = EIOThemeBoot;

  // The browser document is present in production; unit-test harnesses may
  // provide a minimal document, in which case callers can bootstrap manually.
  if (typeof document !== 'undefined' && document && document.documentElement && document.documentElement.dataset) {
    try { bootstrap(root, document, root.localStorage); } catch (e) { /* safe fallback */ }
  }
})();

/* Emby-in-One Web client - appearance contract and lifecycle (T1).
 *
 * Responsibilities:
 *  - the built-in light/dark variants and the fixed token -> CSS variable map;
 *  - safe, namespaced anonymous/per-user cache with schema validation;
 *  - prefers-color-scheme changes only while mode=system;
 *  - account generation + abort so a late T4 response cannot overwrite a new
 *    login, logout or another account;
 *  - T4 dependency injection (site/catalog/preferences/save) without talking
 *    to the network in T1.
 *
 * This module never imports or calls the player/subtitle engine. Applying a
 * theme only writes html[data-*] and the known --token custom properties.
 */
(function () {
  'use strict';

  const SCHEMA = 1;
  const MODES = Object.freeze({ light: true, dark: true, system: true });
  const VARIANT_KEYS = Object.freeze([
    'bg0', 'bg1', 'bg2', 'bg3',
    'text1', 'text2', 'text3',
    'line', 'line2', 'accent', 'accentText',
  ]);
  const HEX = /^#[0-9a-f]{6}$/i;
  const THEME_ID = /^[a-z0-9](?:[a-z0-9-]{1,46})[a-z0-9]$/;
  const CSS_HREF = /^\/web\/themes\/([a-f0-9]{8,64})\/theme\.css$/;
  const BACKGROUND_URL = /^\/web\/themes\/([a-f0-9]{64})\/assets\/[A-Za-z0-9][A-Za-z0-9._-]*\.(?:png|jpe?g)$/;
  const RESERVED_IDS = Object.freeze({ default: true, admin: true, web: true });
  const CACHE_KEYS = Object.freeze({
    anon: 'eio_theme_anon_v1',
    userPrefix: 'eio_theme_user_v1:',
    site: 'eio_theme_site_v1',
  });
  const DEFAULT_SITE = Object.freeze({ themeId: 'builtin', mode: 'dark', revision: 0 });

  function freeze(value) {
    if (!value || typeof value !== 'object' || Object.isFrozen(value)) return value;
    for (const key of Object.keys(value)) freeze(value[key]);
    return Object.freeze(value);
  }

  const BUILTIN = freeze({
    id: 'builtin',
    name: '内置主题',
    author: 'Emby-in-One',
    description: '内置明亮与暗色外观',
    builtin: true,
    revision: 0,
    cssHref: null,
    appearance: { radius: 14, density: 'comfortable' },
    variants: {
      dark: {
        bg0: '#070a12', bg1: '#0d1322', bg2: '#151d31', bg3: '#1e2942',
        text1: '#f1f5f9', text2: '#94a3b8', text3: '#5b6b85',
        line: '#94a3b8', line2: '#94a3b8',
        accent: '#4f46e5', accentText: '#818cf8',
      },
      light: {
        bg0: '#f5f7fb', bg1: '#ffffff', bg2: '#eef2f8', bg3: '#e4eaf4',
        text1: '#0f172a', text2: '#475569', text3: '#64748b',
        line: '#0f172a', line2: '#0f172a',
        accent: '#4f46e5', accentText: '#4338ca',
      },
    },
  });

  /* ================= validation ================= */

  function validThemeId(id) {
    if (typeof id !== 'string' || id.length < 3 || id.length > 48) return false;
    if (!THEME_ID.test(id)) return false;
    return id === 'builtin' || !RESERVED_IDS[id];
  }

  function validMode(mode) {
    return typeof mode === 'string' && MODES[mode] === true;
  }

  function validUserId(id) {
    return typeof id === 'string' && id.length > 0 && id.length <= 128 && !/[\u0000-\u001f]/.test(id);
  }

  function validRevision(value) {
    return Number.isSafeInteger(value) && value >= 0;
  }

  function normalizeUserId(value) {
    return validUserId(value) ? value : null;
  }

  function normalizeCssHref(value) {
    if (value == null) return null;
    if (typeof value !== 'string' || value.length > 512) return null;
    return CSS_HREF.test(value) ? value : null;
  }

  // A background is only accepted when it is an immutable asset of the same
  // hashed theme package as the stylesheet. It is applied as a CSS variable
  // on page surfaces only (never to player/subtitle markup).
  function normalizeBackgroundUrl(value, cssHref) {
    if (value == null) return null;
    if (typeof value !== 'string' || value.length > 512) return null;
    if (/[\\\s"'#?]/.test(value)) return null;
    const background = BACKGROUND_URL.exec(value);
    if (!background) return null;
    const css = cssHref ? CSS_HREF.exec(cssHref) : null;
    if (!css || css[1] !== background[1]) return null;
    return value;
  }

  function normalizeVariant(input) {
    if (!input || typeof input !== 'object' || Array.isArray(input)) return null;
    const keys = Object.keys(input);
    if (keys.length !== VARIANT_KEYS.length) return null;
    const out = {};
    for (const key of VARIANT_KEYS) {
      const value = input[key];
      if (typeof value !== 'string' || !HEX.test(value)) return null;
      out[key] = value.toLowerCase();
    }
    for (const key of keys) if (!VARIANT_KEYS.includes(key)) return null;
    return out;
  }

  function normalizeAppearance(input) {
    if (input == null) return { radius: 14, density: 'comfortable' };
    if (typeof input !== 'object' || Array.isArray(input)) return null;
    const radius = input.radius == null ? 14 : input.radius;
    const density = input.density == null ? 'comfortable' : input.density;
    if (!Number.isInteger(radius) || radius < 4 || radius > 24) return null;
    if (density !== 'compact' && density !== 'comfortable') return null;
    for (const key of Object.keys(input)) {
      if (key !== 'radius' && key !== 'density') return null;
    }
    return { radius, density };
  }

  function normalizeTheme(input) {
    if (!input || typeof input !== 'object' || Array.isArray(input)) return null;
    if (!validThemeId(input.id)) return null;
    const controlsModule = (typeof window !== 'undefined' ? window : globalThis).EIOThemePlayerControls;
    const playerFields = controlsModule?.normalizeThemeFields(input, input.id === 'builtin');
    if (!playerFields) return null;
    const name = typeof input.name === 'string' ? input.name.trim().slice(0, 80) : '';
    if (!name) return null;
    const variants = input.variants;
    if (!variants || typeof variants !== 'object') return null;
    const light = normalizeVariant(variants.light);
    const dark = normalizeVariant(variants.dark);
    if (!light || !dark) return null;
    const appearance = normalizeAppearance(input.appearance);
    if (!appearance) return null;
    const cssHref = input.cssHref == null ? null : normalizeCssHref(input.cssHref);
    if (input.cssHref != null && !cssHref) return null;
    const backgroundUrl = normalizeBackgroundUrl(input.backgroundUrl, cssHref);
    if (input.backgroundUrl != null && !backgroundUrl) return null;
    const revision = validRevision(input.revision) ? input.revision : null;
    return {
      ...playerFields,
      id: input.id,
      name,
      author: typeof input.author === 'string' ? input.author.trim().slice(0, 80) : '',
      description: typeof input.description === 'string' ? input.description.trim().slice(0, 400) : '',
      builtin: input.id === 'builtin',
      revision,
      cssHref,
      backgroundUrl,
      appearance,
      variants: { light, dark },
    };
  }

  function normalizeSite(input) {
    if (!input || typeof input !== 'object' || Array.isArray(input)) return null;
    if (!validThemeId(input.themeId)) return null;
    const mode = input.mode == null ? 'dark' : input.mode;
    if (!validMode(mode)) return null;
    const revision = input.revision == null ? 0 : input.revision;
    if (!validRevision(revision)) return null;
    return { themeId: input.themeId, mode, revision };
  }

  function normalizePreference(input, scope, userId) {
    if (!input || typeof input !== 'object' || Array.isArray(input)) return null;
    if (input.schema !== SCHEMA) return null;
    if (input.scope != null && input.scope !== scope) return null;
    if (scope === 'user') {
      if (!validUserId(userId) || input.userId !== userId) return null;
    } else if (scope === 'anon') {
      if (input.userId != null) return null;
    } else {
      return null;
    }
    const themeId = input.themeId == null ? null : input.themeId;
    if (themeId !== null && !validThemeId(themeId)) return null;
    const mode = input.mode == null ? 'dark' : input.mode;
    if (!validMode(mode)) return null;
    const revision = input.revision == null ? 0 : input.revision;
    if (!validRevision(revision)) return null;
    const cssHref = input.cssHref == null ? null : normalizeCssHref(input.cssHref);
    if (input.cssHref != null && !cssHref) return null;
    const themeRevision = input.themeRevision == null ? null : input.themeRevision;
    if (themeRevision !== null && !validRevision(themeRevision)) return null;
    return {
      schema: SCHEMA,
      scope,
      userId: scope === 'user' ? userId : null,
      themeId,
      mode,
      revision,
      cssHref,
      themeRevision,
    };
  }

  function normalizeRemotePreference(input, identity) {
    if (!input || typeof input !== 'object' || Array.isArray(input)) return null;
    if (input.userId != null && input.userId !== identity) return null;
    return normalizePreference({
      schema: SCHEMA,
      scope: 'user',
      userId: identity,
      themeId: input.themeId == null ? null : input.themeId,
      mode: input.mode == null ? 'dark' : input.mode,
      revision: input.revision == null ? 0 : input.revision,
      cssHref: input.cssHref,
      themeRevision: input.themeRevision,
    }, 'user', identity);
  }

  /* ================= storage ================= */

  // Accessing window.localStorage itself can throw (SecurityError when the
  // browser blocks site storage), so the getter is only ever read here and a
  // denial degrades to an in-memory controller instead of breaking setup.
  function safeLocalStorage(win) {
    try { return (win && win.localStorage) || null; }
    catch (e) { return null; }
  }

  function readRaw(storage, key) {
    try { return storage && typeof storage.getItem === 'function' ? storage.getItem(key) : null; }
    catch (e) { return null; }
  }

  function writeRaw(storage, key, value) {
    try {
      if (!storage || typeof storage.setItem !== 'function') return false;
      storage.setItem(key, value);
      return true;
    } catch (e) {
      return false;
    }
  }

  function removeRaw(storage, key) {
    try { if (storage && typeof storage.removeItem === 'function') storage.removeItem(key); }
    catch (e) { /* denied storage must never break the page */ }
  }

  function userCacheKey(userId) {
    return CACHE_KEYS.userPrefix + encodeURIComponent(userId);
  }

  function cacheKeyFor(userId) {
    return userId ? userCacheKey(userId) : CACHE_KEYS.anon;
  }

  function identityFromStorage(storage) {
    if (!readRaw(storage, 'eio_token')) return null;
    return normalizeUserId(readRaw(storage, 'eio_user_id'));
  }

  function defaultPreference(scope, userId) {
    return {
      schema: SCHEMA,
      scope,
      userId: scope === 'user' ? userId : null,
      themeId: null,
      mode: 'dark',
      revision: 0,
      cssHref: null,
      themeRevision: null,
    };
  }

  /* ================= token mapping ================= */

  function rgba(hex, alpha) {
    const value = parseInt(hex.slice(1), 16);
    return 'rgba(' + ((value >> 16) & 255) + ', ' + ((value >> 8) & 255) + ', ' + (value & 255) + ', ' + alpha + ')';
  }

  function systemVariant(media) {
    if (!media) return 'dark';
    return media.matches ? 'dark' : 'light';
  }

  function channelLuminance(value) {
    const channel = value / 255;
    return channel <= 0.03928 ? channel / 12.92 : Math.pow((channel + 0.055) / 1.055, 2.4);
  }

  function relativeLuminance(hex) {
    const value = parseInt(hex.slice(1), 16);
    const r = channelLuminance((value >> 16) & 255);
    const g = channelLuminance((value >> 8) & 255);
    const b = channelLuminance(value & 255);
    return 0.2126 * r + 0.7152 * g + 0.0722 * b;
  }

  function contrastRatio(a, b) {
    const first = relativeLuminance(a);
    const second = relativeLuminance(b);
    const lighter = Math.max(first, second);
    const darker = Math.min(first, second);
    return (lighter + 0.05) / (darker + 0.05);
  }

  // accentText is a page/link emphasis colour, never button label text.
  // Button/nav foregrounds are derived from the button background itself so a
  // light custom accent gets black text and a dark accent keeps white text.
  function readableOn(accent) {
    const white = contrastRatio(accent, '#ffffff');
    const black = contrastRatio(accent, '#000000');
    return white >= black ? '#ffffff' : '#000000';
  }

  function variantToCssVars(variant, variantName, appearance, backgroundUrl) {
    const radius = appearance && Number.isInteger(appearance.radius) ? appearance.radius : 14;
    const density = appearance && appearance.density === 'compact' ? 'compact' : 'comfortable';
    const light = variantName === 'light';
    const background = typeof backgroundUrl === 'string' && BACKGROUND_URL.test(backgroundUrl)
      ? backgroundUrl
      : null;
    return {
      '--bg-0': variant.bg0,
      '--bg-1': variant.bg1,
      '--bg-2': variant.bg2,
      '--bg-3': variant.bg3,
      '--t1': variant.text1,
      '--t2': variant.text2,
      '--t3': variant.text3,
      '--line': rgba(variant.line, light ? 0.12 : 0.14),
      '--line-2': rgba(variant.line2, light ? 0.22 : 0.24),
      '--ac': variant.accent,
      '--ac2': variant.accentText,
      '--ac-text': variant.accentText,
      '--ac-on': readableOn(variant.accent),
      // Both gradient stops use the validated accent so --ac-on stays valid
      // across the whole primary control, independent of accentText.
      '--ac-grad': 'linear-gradient(135deg, ' + variant.accent + ', ' + variant.accent + ')',
      '--ac-glow': rgba(variant.accent, light ? 0.22 : 0.35),
      '--ac-soft': rgba(variant.accent, light ? 0.12 : 0.16),
      '--surface-2': light ? 'rgba(15, 23, 42, .05)' : 'rgba(30, 41, 66, .55)',
      '--surface-3': light ? 'rgba(15, 23, 42, .08)' : 'rgba(30, 41, 66, .82)',
      '--surface-menu': light ? 'rgba(255, 255, 255, .98)' : 'rgba(13, 19, 34, .96)',
      '--surface-toast': light ? 'rgba(255, 255, 255, .97)' : 'rgba(21, 29, 49, .94)',
      '--track': light ? 'rgba(71, 85, 105, .20)' : 'rgba(148, 163, 184, .28)',
      '--hover-overlay': light ? 'rgba(15, 23, 42, .06)' : 'rgba(255, 255, 255, .08)',
      '--r-card': radius + 'px',
      '--r-btn': Math.max(4, Math.round(radius * 0.72)) + 'px',
      // Page-only density: consumed by .eio-page/.eio-grid, never by player
      // controls, subtitles or media sizing.
      '--eio-density-space': density === 'compact' ? '10px' : '20px',
      '--eio-density-gap': density === 'compact' ? '10px' : '20px',
      // The background only reaches the login/main shell selectors in ui.css;
      // the scrim keeps opaque panels and token text readable over the image.
      '--eio-theme-bg-image': background ? 'url("' + background + '")' : 'none',
      '--eio-theme-bg-scrim': background ? rgba(variant.bg0, light ? 0.6 : 0.74) : 'transparent',
    };
  }

  /* ================= controller ================= */

  function createController(options) {
    options = options || {};
    const win = options.window || (typeof window !== 'undefined' ? window : null);
    const doc = options.document || (typeof document !== 'undefined' ? document : null);
    // An explicit storage value (including null, which disables caching) wins
    // over the browser; only an omitted option reads the guarded getter.
    const rawStorage = options.storage !== undefined
      ? (options.storage || null)
      : safeLocalStorage(win);
    const deps = options.deps || options.services || {};
    const getUserId = typeof options.getUserId === 'function'
      ? options.getUserId
      : function () { return identityFromStorage(rawStorage); };

    const subscribers = new Set();
    const catalog = new Map([['builtin', BUILTIN]]);
    let siteDefault = normalizeSite(options.siteDefault) || { ...DEFAULT_SITE };
    let matchMedia = options.matchMedia !== undefined
      ? options.matchMedia
      : (win && typeof win.matchMedia === 'function' ? win.matchMedia('(prefers-color-scheme: dark)') : null);
    let storageListener = null;
    let mediaListener = null;
    let pending = null;
    let pendingKind = null;
    let destroyed = false;

    const state = {
      generation: 0,
      // Monotonic per-identity user-intent sequence. A remote response may
      // absorb the server's opaque CAS revision but must never replace a
      // choice made after its request started.
      intentSeq: 0,
      // Monotonic request sequence. The server revision is an opaque random
      // positive number, so it can only be compared for equality, never
      // ordered. Only the newest request may absorb a revision; a late
      // response from an older request must not overwrite it.
      requestSeq: 0,
      identity: null,
      preference: null,
      resolved: null,
      forcedNotice: '',
      storageError: '',
      remoteError: '',
      remoteStatus: 'local',
    };

    function initialUserId() {
      try { return normalizeUserId(getUserId()); } catch (e) { return null; }
    }

    function cacheKey() {
      return cacheKeyFor(state.identity);
    }

    function readCached(identity) {
      const scope = identity ? 'user' : 'anon';
      const key = cacheKeyFor(identity);
      const raw = readRaw(rawStorage, key);
      if (raw == null) return { preference: defaultPreference(scope, identity), invalid: false };
      let parsed;
      try { parsed = JSON.parse(raw); } catch (e) { parsed = null; }
      const preference = normalizePreference(parsed, scope, identity);
      if (!preference) {
        removeRaw(rawStorage, key);
        return { preference: defaultPreference(scope, identity), invalid: true };
      }
      return { preference, invalid: false };
    }

    function writeCached(preference) {
      const ok = writeRaw(rawStorage, cacheKeyFor(preference.scope === 'user' ? preference.userId : null), JSON.stringify(preference));
      state.storageError = ok ? '' : '无法保存本机外观设置，本次切换仅当前页面有效';
      return ok;
    }

    function resolvePreference(preference) {
      let theme = null;
      let notice = '';
      if (preference.themeId == null) {
        theme = catalog.get(siteDefault.themeId) || null;
        if (!theme) {
          theme = BUILTIN;
          notice = '站点主题不可用，已恢复内置主题';
        }
      } else {
        theme = catalog.get(preference.themeId) || null;
        if (!theme) {
          theme = BUILTIN;
          notice = '主题不可用，已恢复内置主题';
        }
      }
      const mode = validMode(preference.mode) ? preference.mode : 'dark';
      const variantName = mode === 'system' ? systemVariant(matchMedia) : mode;
      let tokens = theme.variants[variantName];
      if (!tokens) {
        theme = BUILTIN;
        tokens = BUILTIN.variants[variantName] || BUILTIN.variants.dark;
        notice = '主题缺少该明暗样式，已恢复内置主题';
      }
      const cssHref = theme.cssHref || (theme.id === preference.themeId ? preference.cssHref : null);
      return {
        themeId: theme.id,
        requestedThemeId: preference.themeId,
        mode,
        variant: variantName,
        theme,
        variantTokens: tokens,
        notice,
        revision: preference.revision,
        cssHref: cssHref || null,
        backgroundUrl: theme.backgroundUrl || null,
      };
    }

    function applyResolved(resolved) {
      const root = doc && doc.documentElement;
      if (!root) return;
      if (!root.dataset) root.dataset = {};
      root.dataset.eioTheme = resolved.variant;
      root.dataset.eioThemeId = resolved.themeId;
      root.dataset.eioMode = resolved.mode;
      if (!root.style) return;
      const vars = variantToCssVars(resolved.variantTokens, resolved.variant, resolved.theme.appearance, resolved.theme.backgroundUrl);
      if (typeof root.style.setProperty === 'function') {
        for (const key of Object.keys(vars)) root.style.setProperty(key, vars[key]);
        try { root.style.colorScheme = resolved.variant; } catch (e) { /* older engines */ }
      }
    }

    function getNotice() {
      return state.forcedNotice || (state.resolved && state.resolved.notice)
        || state.remoteError || state.storageError || '';
    }

    function publicResolved() {
      const resolved = state.resolved;
      if (!resolved) return null;
      return {
        themeId: resolved.themeId,
        requestedThemeId: resolved.requestedThemeId,
        mode: resolved.mode,
        variant: resolved.variant,
        notice: getNotice(),
        revision: resolved.revision,
        cssHref: resolved.cssHref,
        backgroundUrl: resolved.backgroundUrl || null,
        tokens: { ...resolved.variantTokens },
      };
    }

    function snapshot() {
      return {
        generation: state.generation,
        identity: state.identity,
        preference: state.preference ? { ...state.preference } : null,
        resolved: publicResolved(),
        remoteStatus: state.remoteStatus,
      };
    }

    function emit(reason) {
      if (destroyed) return;
      const value = snapshot();
      for (const listener of Array.from(subscribers)) {
        try { listener(value, reason); } catch (e) { /* subscriber errors stay isolated */ }
      }
    }

    function applyPreference(preference, reason) {
      state.preference = preference;
      state.resolved = resolvePreference(preference);
      applyResolved(state.resolved);
      emit(reason);
      return state.resolved;
    }

    function abortPending(keepKind) {
      if (!pending) return;
      // Concurrent saves are allowed to settle so an older success can still
      // supply the server's exact revision; the latest intent is protected
      // separately, and a newer request owns the revision once it starts.
      if (keepKind && pendingKind === keepKind) return;
      try { pending.abort(); } catch (e) { /* already settled */ }
      pending = null;
      pendingKind = null;
    }

    function currentRevision() {
      const preference = state.preference;
      return preference && validRevision(preference.revision) ? preference.revision : 0;
    }

    // Keep the current theme/mode (which may be newer than a response) while
    // absorbing the server's exact opaque revision so the next CAS PUT is not
    // a false conflict. The revision may legitimately decrease.
    function mergeRevision(identity, revision, reason) {
      const current = state.preference || defaultPreference(identity ? 'user' : 'anon', identity);
      const nextRevision = validRevision(revision) ? revision : currentRevision();
      const merged = { ...current, revision: nextRevision };
      writeCached(merged);
      return applyPreference(merged, reason);
    }

    function setPreference(next) {
      if (destroyed) return publicResolved();
      next = next || {};
      const current = state.preference || defaultPreference(state.identity ? 'user' : 'anon', state.identity);
      let themeId = Object.prototype.hasOwnProperty.call(next, 'themeId') ? next.themeId : current.themeId;
      let mode = Object.prototype.hasOwnProperty.call(next, 'mode') ? next.mode : current.mode;
      state.forcedNotice = '';

      if (themeId !== null && !validThemeId(themeId)) {
        state.forcedNotice = '无效主题，已恢复内置主题';
        themeId = 'builtin';
      } else if (themeId !== null && !catalog.has(themeId)) {
        state.forcedNotice = '主题不可用，已恢复内置主题';
        themeId = 'builtin';
      }
      if (!validMode(mode)) {
        state.forcedNotice = state.forcedNotice || '无效明暗模式，已恢复默认';
        mode = 'dark';
      }

      const chosen = themeId ? catalog.get(themeId) : null;
      const preference = {
        schema: SCHEMA,
        scope: state.identity ? 'user' : 'anon',
        userId: state.identity,
        themeId,
        mode,
        revision: validRevision(current.revision) ? current.revision : 0,
        cssHref: chosen && chosen.cssHref ? chosen.cssHref : null,
        themeRevision: chosen && validRevision(chosen.revision) ? chosen.revision : null,
      };
      if (!state.preference
        || preference.themeId !== state.preference.themeId
        || preference.mode !== state.preference.mode) {
        state.intentSeq += 1;
      }
      writeCached(preference);
      return applyPreference(preference, 'local');
    }

    function resetBuiltin() {
      return setPreference({ themeId: 'builtin' });
    }

    function enterSession(userId) {
      if (destroyed) return state.generation;
      const identity = normalizeUserId(userId);
      state.generation += 1;
      state.intentSeq += 1;
      state.requestSeq = 0;
      abortPending();
      state.identity = identity;
      state.forcedNotice = '';
      state.remoteError = '';
      state.remoteStatus = 'local';
      const cached = readCached(identity);
      if (cached.invalid) state.forcedNotice = '外观缓存无效，已恢复内置主题';
      applyPreference(cached.preference, 'identity');
      return state.generation;
    }

    function syncIdentity(userId) {
      const identity = normalizeUserId(userId);
      if (identity === state.identity) return state.generation;
      return enterSession(identity);
    }

    function setCatalog(input) {
      if (!Array.isArray(input)) return false;
      const next = new Map([['builtin', BUILTIN]]);
      for (const item of input) {
        const theme = normalizeTheme(item);
        if (!theme || theme.id === 'builtin' || next.has(theme.id)) continue;
        next.set(theme.id, theme);
      }
      catalog.clear();
      for (const [id, theme] of next) catalog.set(id, theme);
      applyPreference(state.preference, 'catalog');
      return true;
    }

    function setSiteDefault(input) {
      const site = normalizeSite(input);
      if (!site) return false;
      siteDefault = site;
      applyPreference(state.preference, 'site');
      return true;
    }

    function withTimeout(promise, timeoutMs) {
      let timer = null;
      const timeout = new Promise(function (_, reject) {
        timer = setTimeout(function () {
          const error = new Error('theme timeout');
          error.name = 'AbortError';
          reject(error);
        }, timeoutMs);
      });
      return Promise.race([promise, timeout]).finally(function () {
        if (timer != null) clearTimeout(timer);
      });
    }

    function remoteController() {
      const AC = (win && win.AbortController) || (typeof AbortController !== 'undefined' ? AbortController : null);
      if (AC) return new AC();
      // Engines without AbortController still need observable cancellation:
      // T4 checks signal.aborted before committing a late payload, so an
      // opaque no-op controller would let an old account's response win.
      const signal = { aborted: false };
      return {
        signal,
        abort: function () { signal.aborted = true; },
      };
    }

    async function refreshRemote(remoteOptions) {
      remoteOptions = remoteOptions || {};
      if (destroyed) return publicResolved();
      if (!deps.loadSite && !deps.loadCatalog && !deps.loadPreferences) return publicResolved();
      const timeoutMs = Math.max(50, Number(remoteOptions.timeoutMs) || 3000);
      const generation = state.generation;
      const identity = state.identity;
      const intent = state.intentSeq;
      abortPending();
      state.requestSeq += 1;
      const requestSeq = state.requestSeq;
      const controller = remoteController();
      pending = controller;
      pendingKind = 'refresh';
      state.remoteError = '';
      state.remoteStatus = 'loading';
      emit('remote-start');
      const current = function () {
        return !destroyed && generation === state.generation && identity === state.identity;
      };
      let degraded = false;
      try {
        if (deps.loadSite) {
          const site = await withTimeout(Promise.resolve(deps.loadSite({
            signal: controller.signal, generation: generation, userId: identity,
          })), timeoutMs);
          if (!current()) return publicResolved();
          const normalized = normalizeSite(site);
          if (normalized) siteDefault = normalized;
          else { state.remoteError = '站点外观设置无效，已保留本机设置'; degraded = true; }
        }
        if (deps.loadCatalog) {
          const catalogResult = await withTimeout(Promise.resolve(deps.loadCatalog({
            signal: controller.signal, generation: generation, userId: identity,
          })), timeoutMs);
          if (!current()) return publicResolved();
          const themes = Array.isArray(catalogResult) ? catalogResult : catalogResult && catalogResult.themes;
          if (Array.isArray(themes)) setCatalog(themes);
          else { state.remoteError = '主题目录无效，已保留本机主题'; degraded = true; }
        }
        if (identity && deps.loadPreferences) {
          const remote = await withTimeout(Promise.resolve(deps.loadPreferences({
            signal: controller.signal, generation: generation, userId: identity,
          })), timeoutMs);
          if (!current()) return publicResolved();
          const preference = normalizeRemotePreference(remote, identity);
          if (preference) {
            if (requestSeq !== state.requestSeq) {
              // A newer request owns the revision now; this stale GET must not
              // write its older opaque revision back over the newer state.
              return publicResolved();
            }
            if (state.intentSeq !== intent) {
              // The user chose while the initial GET was in flight: keep that
              // local intent and absorb only the server's exact revision.
              mergeRevision(identity, preference.revision, 'remote-revision');
            } else {
              const merged = { ...preference };
              writeCached(merged);
              applyPreference(merged, 'remote');
            }
          } else {
            state.remoteError = '在线外观设置无效，已保留本机缓存';
            degraded = true;
          }
        }
        if (current()) {
          // A loader that rejected its payload (unavailable site, partial
          // catalog) must not be reported as a clean success; the local cache
          // is still applied and the notice explains the degradation.
          state.remoteStatus = degraded ? 'fallback' : 'ok';
          applyPreference(state.preference, 'remote-done');
        }
        return publicResolved();
      } catch (error) {
        if (error && error.name === 'AbortError') return publicResolved();
        if (current()) {
          state.remoteStatus = 'fallback';
          state.remoteError = '无法获取在线外观设置，已使用本机缓存';
          emit('remote-error');
        }
        return publicResolved();
      } finally {
        if (pending === controller) {
          pending = null;
          pendingKind = null;
        }
      }
    }

    async function saveRemote(remoteOptions) {
      remoteOptions = remoteOptions || {};
      if (destroyed || !state.identity || !deps.savePreferences) return { saved: false, local: true };
      const timeoutMs = Math.max(50, Number(remoteOptions.timeoutMs) || 3000);
      const generation = state.generation;
      const identity = state.identity;
      const intent = state.intentSeq;
      const sent = state.preference;
      const sentRevision = currentRevision();
      abortPending('save');
      state.requestSeq += 1;
      const requestSeq = state.requestSeq;
      const controller = remoteController();
      pending = controller;
      pendingKind = 'save';
      const current = function () {
        return !destroyed && generation === state.generation && identity === state.identity;
      };
      try {
        const result = await withTimeout(Promise.resolve(deps.savePreferences({
          themeId: sent.themeId,
          mode: sent.mode,
          expectedRevision: sentRevision,
          signal: controller.signal,
          generation: generation,
          userId: identity,
        })), timeoutMs);
        if (!current()) return { saved: false, stale: true };
        const preference = normalizeRemotePreference(result, identity);
        if (!preference) throw new Error('invalid appearance response');
        if (requestSeq !== state.requestSeq) {
          // A newer request owns the revision. This response may belong to an
          // older PUT/GET that interleaved with it, so absorbing its opaque
          // revision could move the CAS token backwards.
          return { saved: false, stale: true };
        }
        const revision = preference.revision;
        if (state.intentSeq !== intent) {
          // This response belongs to an older choice. Its success still proves
          // the server accepted its exact revision, so keep it for the queued
          // latest save without replacing the newer local choice.
          mergeRevision(identity, revision, 'remote-save-revision');
          return { saved: false, superseded: true, revision: revision };
        }
        const merged = { ...preference };
        writeCached(merged);
        applyPreference(merged, 'remote-save');
        return { saved: true, revision: revision };
      } catch (error) {
        if (current() && (!error || error.name !== 'AbortError')) {
          state.remoteError = '保存外观设置失败，已保留本机选择';
          emit('remote-save-error');
        }
        return { saved: false, error: error };
      } finally {
        if (pending === controller) {
          pending = null;
          pendingKind = null;
        }
      }
    }

    function getThemeOptions() {
      const options = [
        { id: null, label: '跟随站点', builtin: false },
        { id: 'builtin', label: '内置主题', builtin: true },
      ];
      for (const [id, theme] of catalog) {
        if (id === 'builtin') continue;
        options.push({ id, label: theme.name, builtin: false });
      }
      return options;
    }

    function subscribe(listener) {
      if (typeof listener !== 'function' || destroyed) return function () {};
      subscribers.add(listener);
      return function () { subscribers.delete(listener); };
    }

    function onMediaChange() {
      if (destroyed || !state.preference || state.preference.mode !== 'system') return;
      applyPreference(state.preference, 'system');
    }

    function onStorage(event) {
      if (destroyed || !event) return;
      if (event.storageArea && rawStorage && event.storageArea !== rawStorage) return;
      if (event.key !== cacheKey()) return;
      const cached = readCached(state.identity);
      state.forcedNotice = cached.invalid ? '外观缓存无效，已恢复内置主题' : '';
      if (cached.invalid) writeRaw(rawStorage, cacheKey(), JSON.stringify(cached.preference));
      applyPreference(cached.preference, 'storage');
    }

    function destroy() {
      if (destroyed) return;
      destroyed = true;
      abortPending();
      subscribers.clear();
      if (storageListener && win && typeof win.removeEventListener === 'function') {
        win.removeEventListener('storage', storageListener);
      }
      storageListener = null;
      if (mediaListener && matchMedia) {
        if (typeof matchMedia.removeEventListener === 'function') matchMedia.removeEventListener('change', mediaListener);
        else if (typeof matchMedia.removeListener === 'function') matchMedia.removeListener(mediaListener);
      }
      mediaListener = null;
    }

    const initialIdentity = normalizeUserId(options.initialUserId !== undefined ? options.initialUserId : initialUserId());
    state.identity = initialIdentity;
    const initialCache = readCached(initialIdentity);
    state.preference = initialCache.preference;
    if (initialCache.invalid) state.forcedNotice = '外观缓存无效，已恢复内置主题';
    state.resolved = resolvePreference(state.preference);
    applyResolved(state.resolved);
    if (Array.isArray(options.themes)) setCatalog(options.themes);

    if (matchMedia) {
      mediaListener = onMediaChange;
      if (typeof matchMedia.addEventListener === 'function') matchMedia.addEventListener('change', mediaListener);
      else if (typeof matchMedia.addListener === 'function') matchMedia.addListener(mediaListener);
    }
    if (win && typeof win.addEventListener === 'function') {
      storageListener = onStorage;
      win.addEventListener('storage', storageListener);
    }

    return {
      getGeneration: function () { return state.generation; },
      getIdentity: function () { return state.identity; },
      getPreference: function () { return state.preference ? { ...state.preference } : null; },
      getResolved: publicResolved,
      getNotice: getNotice,
      getSnapshot: snapshot,
      getThemeOptions: getThemeOptions,
      getSiteDefault: function () { return { ...siteDefault }; },
      getStorageError: function () { return state.storageError; },
      getRemoteStatus: function () { return state.remoteStatus; },
      isDestroyed: function () { return destroyed; },
      subscribe: subscribe,
      setPreference: setPreference,
      setMode: function (mode) { return setPreference({ mode: mode }); },
      setTheme: function (themeId) { return setPreference({ themeId: themeId }); },
      resetBuiltin: resetBuiltin,
      enterSession: enterSession,
      syncIdentity: syncIdentity,
      setCatalog: setCatalog,
      setSiteDefault: setSiteDefault,
      refreshRemote: refreshRemote,
      saveRemote: saveRemote,
      destroy: destroy,
    };
  }

  const EIOTheme = {
    SCHEMA: SCHEMA,
    BUILTIN: BUILTIN,
    MODES: MODES,
    VARIANT_KEYS: VARIANT_KEYS,
    DEFAULT_SITE: DEFAULT_SITE,
    CACHE_KEYS: CACHE_KEYS,
    validThemeId: validThemeId,
    validMode: validMode,
    validUserId: validUserId,
    validRevision: validRevision,
    normalizeVariant: normalizeVariant,
    normalizeTheme: normalizeTheme,
    normalizeSite: normalizeSite,
    normalizePreference: normalizePreference,
    normalizeRemotePreference: normalizeRemotePreference,
    normalizeCssHref: normalizeCssHref,
    systemVariant: systemVariant,
    relativeLuminance: relativeLuminance,
    contrastRatio: contrastRatio,
    readableOn: readableOn,
    variantToCssVars: variantToCssVars,
    userCacheKey: userCacheKey,
    cacheKeyFor: cacheKeyFor,
    identityFromStorage: identityFromStorage,
    createController: createController,
  };

  const root = typeof window !== 'undefined' ? window : globalThis;
  root.EIOTheme = EIOTheme;
})();

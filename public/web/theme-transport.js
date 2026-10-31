/* Emby-in-One Web client - server theme transport (T4).
 *
 * This module is the real EIOThemeDeps implementation for the T1 appearance
 * controller. It owns only:
 *  - the four /web theme endpoints (site, catalog, preference read/write);
 *  - strict validation of the public DTOs before anything reaches T1;
 *  - the immutable generated stylesheet <link> lifecycle;
 *  - 401 and late-response isolation.
 *
 * It never applies a theme itself and never keeps its own preference state:
 * theme.js stays the single controller (generation, cache, tokens, mode). The
 * public DTO is not trusted: tokens, hashes and resource URLs are revalidated
 * here, and a custom theme is only exposed to T1 through normalizeTheme().
 */
(function () {
  'use strict';

  const root = typeof window !== 'undefined' ? window : globalThis;
  const HASH = /^[a-f0-9]{64}$/;
  const CSS_URL = /^\/web\/themes\/([a-f0-9]{64})\/theme\.css$/;
  const ASSET_URL = /^\/web\/themes\/([a-f0-9]{64})\/assets\/([A-Za-z0-9][A-Za-z0-9._-]*)$/;
  const ASSET_NAME = /^assets\/([A-Za-z0-9][A-Za-z0-9._-]*)$/;
  const PREVIEW_IMAGE = /^\/web\/themes\/([a-f0-9]{64})\/preview\.(?:png|jpg|jpeg)$/;
  // The server's preview document is a fixed, script-free page; its only
  // supported query is the mode selector. Anything else is rejected.
  const PREVIEW_DOC = /^\/web\/themes\/([a-f0-9]{64})\/preview\.html\?mode=(?:light|dark)$/;
  const VARIANT_NAMES = Object.freeze(['light', 'dark']);

  function abortError() {
    const error = new Error('theme transport aborted');
    error.name = 'AbortError';
    return error;
  }

  function isAbort(error) {
    return !!error && error.name === 'AbortError';
  }

  function themeModule() {
    return root.EIOTheme || null;
  }

  function validThemeId(id) {
    const theme = themeModule();
    return !!theme && typeof theme.validThemeId === 'function' && theme.validThemeId(id);
  }

  function validMode(mode) {
    const theme = themeModule();
    return !!theme && typeof theme.validMode === 'function' && theme.validMode(mode);
  }

  function normalizeIdentity(value) {
    const theme = themeModule();
    if (theme && typeof theme.validUserId === 'function') {
      return theme.validUserId(value) ? value : null;
    }
    return typeof value === 'string' && value.length > 0 && value.length <= 128 ? value : null;
  }

  function isObject(value) {
    return !!value && typeof value === 'object' && !Array.isArray(value);
  }

  function safeText(value, max) {
    if (typeof value !== 'string') return '';
    return value.trim().slice(0, max);
  }

  /* ================= immutable resource URLs ================= */

  function sameOriginPath(value, options) {
    options = options || {};
    const origin = options.origin
      || (options.win && options.win.location && options.win.location.origin)
      || null;
    if (!origin) return true;
    try { return new URL(value, origin).origin === origin; }
    catch (e) { return false; }
  }

  function hasForbiddenUrlText(value) {
    return /[\\\s#]/.test(value);
  }

  // CSS and background resources are absolute same-origin paths with exactly
  // the content hash of the theme. No query, fragment, credential or
  // protocol-relative form is accepted, so remote CSS can never be redirected
  // or authenticated through the URL.
  function safeCssUrl(value, hash, options) {
    if (typeof value !== 'string' || value.length > 512) return null;
    if (hasForbiddenUrlText(value) || value.indexOf('?') !== -1) return null;
    const match = CSS_URL.exec(value);
    if (!match || match[1] !== hash) return null;
    return sameOriginPath(value, options) ? value : null;
  }

  function safeAssetUrl(value, hash, options) {
    if (value == null || value === '') return '';
    if (typeof value !== 'string' || value.length > 512) return null;
    if (hasForbiddenUrlText(value) || value.indexOf('?') !== -1) return null;
    const match = ASSET_URL.exec(value);
    if (!match || match[1] !== hash) return null;
    if (!/\.(?:png|jpe?g)$/i.test(match[2])) return null;
    return sameOriginPath(value, options) ? value : null;
  }

  function safePreviewUrl(value, hash, options) {
    if (value == null || value === '') return '';
    if (typeof value !== 'string' || value.length > 512) return null;
    if (value.indexOf('#') !== -1 || /[\\\s]/.test(value)) return null;
    if (/api_key|token|auth/i.test(value)) return null;
    let match = PREVIEW_IMAGE.exec(value);
    if (match) return match[1] === hash && sameOriginPath(value, options) ? value : null;
    match = PREVIEW_DOC.exec(value);
    if (match) return match[1] === hash && sameOriginPath(value, options) ? value : null;
    return null;
  }

  /* ================= public theme -> T1 theme ================= */

  function adaptDefinition(raw, hash, options) {
    if (!isObject(raw)) return null;
    const variants = raw.variants;
    if (!isObject(variants) || !isObject(variants.light) || !isObject(variants.dark)) return null;
    const backgroundImage = raw.backgroundImage == null ? '' : raw.backgroundImage;
    if (typeof backgroundImage !== 'string') return null;
    if (backgroundImage && !ASSET_NAME.test(backgroundImage)) return null;

    const backgroundUrl = safeAssetUrl(raw.backgroundUrl, hash, options);
    if (backgroundUrl == null) return null;
    if (backgroundImage) {
      // The definition and the public URL must describe the same immutable
      // asset; a mismatch is not something the client may guess around.
      if (backgroundUrl !== '/web/themes/' + hash + '/' + backgroundImage) return null;
    } else if (backgroundUrl) {
      return null;
    }

    const theme = themeModule();
    if (!theme || typeof theme.normalizeTheme !== 'function') return null;
    const adapted = theme.normalizeTheme({
      id: raw.id,
      name: raw.name,
      author: raw.author,
      description: raw.description,
      revision: null,
      cssHref: raw.cssHref,
      backgroundUrl: backgroundUrl || null,
      appearance: raw.appearance,
      variants: { light: variants.light, dark: variants.dark },
    });
    if (!adapted || adapted.id !== raw.id || adapted.cssHref !== raw.cssHref) return null;
    return adapted;
  }

  // adaptPublicTheme returns the exact object normalizeTheme() accepts:
  // top-level variants tokens, appearance and cssHref. A builtin entry has no
  // definition, so the caller keeps T1's builtin theme and adapts nothing.
  function adaptPublicTheme(raw, options) {
    if (!isObject(raw)) return null;
    const id = raw.id;
    if (id === 'builtin') return null;
    if (!validThemeId(id) || raw.builtin === true) return null;
    if (typeof raw.hash !== 'string' || !HASH.test(raw.hash)) return null;
    if (!Array.isArray(raw.variants) || raw.variants.length !== VARIANT_NAMES.length) return null;
    for (const name of VARIANT_NAMES) if (!raw.variants.includes(name)) return null;
    for (const name of raw.variants) if (VARIANT_NAMES.indexOf(name) === -1) return null;

    const cssHref = safeCssUrl(raw.cssUrl, raw.hash, options);
    if (!cssHref) return null;

    const definition = adaptDefinition({
      ...(isObject(raw.definition) ? raw.definition : {}),
      id,
      name: raw.name,
      author: raw.author,
      description: raw.description,
      cssHref,
      backgroundUrl: raw.backgroundUrl,
    }, raw.hash, options);
    if (!definition) return null;

    const previews = {};
    const fields = [['preview', 'previewUrl'], ['light', 'previewLightUrl'], ['dark', 'previewDarkUrl']];
    for (const pair of fields) {
      const safe = safePreviewUrl(raw[pair[1]], raw.hash, options);
      if (safe == null) return null;
      previews[pair[0]] = safe || null;
    }
    return { theme: definition, previews, hash: raw.hash, version: safeText(raw.version, 40) };
  }

  function parseCatalog(raw, options) {
    if (!isObject(raw) || !Array.isArray(raw.themes) || raw.themes.length > 128) return null;
    const themes = [];
    const seen = new Set();
    for (const entry of raw.themes) {
      if (!isObject(entry)) continue;
      if (entry.id === 'builtin' || entry.builtin === true) continue;
      if (seen.has(entry.id)) continue;
      const adapted = adaptPublicTheme(entry, options);
      if (!adapted) continue;
      seen.add(adapted.theme.id);
      themes.push(adapted.theme);
    }
    return themes;
  }

  function parseSite(raw, options) {
    const unusable = function (reason, available) {
      return {
        usable: false,
        reason,
        available: available === true,
        themeId: 'builtin',
        revision: 0,
        theme: null,
        previews: null,
      };
    };
    if (!isObject(raw)) return unusable('invalid', false);
    const revision = raw.revision;
    const positive = Number.isSafeInteger(revision) && revision >= 1;
    if (raw.available !== true || !positive) return unusable('unavailable', raw.available === true);
    const themeId = raw.themeId;
    if (!validThemeId(themeId)) return unusable('invalid', true);
    if (themeId === 'builtin') {
      // A builtin default has no public theme payload; a non-null mismatched
      // payload means the response is not the documented shape.
      if (raw.theme != null && !(isObject(raw.theme) && raw.theme.id === 'builtin')) return unusable('invalid', true);
      return { usable: true, reason: '', available: true, themeId, revision, theme: null, previews: null };
    }
    if (!isObject(raw.theme) || raw.theme.id !== themeId) return unusable('invalid', true);
    const adapted = adaptPublicTheme(raw.theme, options);
    if (!adapted || adapted.theme.id !== themeId) return unusable('invalid', true);
    return {
      usable: true,
      reason: '',
      available: true,
      themeId,
      revision,
      theme: adapted.theme,
      previews: adapted.previews,
    };
  }

  function parsePreference(raw) {
    if (!isObject(raw)) return null;
    const themeId = raw.themeId == null ? null : raw.themeId;
    if (themeId !== null && !validThemeId(themeId)) return null;
    if (!validMode(raw.mode)) return null;
    // GET seeds a positive revision so every later PUT has a valid CAS
    // expectation; zero is not a usable remote preference.
    if (!Number.isSafeInteger(raw.revision) || raw.revision < 1) return null;
    return { themeId, mode: raw.mode, revision: raw.revision };
  }

  /* ================= transport ================= */

  function create(options) {
    options = options || {};
    const win = options.window || (typeof window !== 'undefined' ? window : null);
    const doc = options.document || (win && win.document) || null;
    const urlOptions = { win, origin: options.origin || null };

    function token() {
      try { return typeof options.getToken === 'function' ? options.getToken() : null; }
      catch (e) { return null; }
    }

    function userId() {
      try { return normalizeIdentity(typeof options.getUserId === 'function' ? options.getUserId() : null); }
      catch (e) { return null; }
    }

    const defaultRequest = function (path, requestOptions) {
      requestOptions = requestOptions || {};
      const fetchFn = options.fetch || (win && win.fetch);
      if (typeof fetchFn !== 'function') return Promise.reject(new Error('theme transport: fetch unavailable'));
      const headers = { 'Content-Type': 'application/json' };
      const current = token();
      if (current && !requestOptions.anonymous) headers['X-Emby-Token'] = current;
      const init = {
        method: requestOptions.method || 'GET',
        headers,
        credentials: 'same-origin',
        cache: requestOptions.anonymous ? 'no-cache' : 'no-store',
      };
      if (requestOptions.signal) init.signal = requestOptions.signal;
      if (requestOptions.body != null) init.body = requestOptions.body;
      return Promise.resolve(fetchFn(path, init)).then(function (response) {
        if (!response || response.ok !== true) {
          const status = response ? response.status : 0;
          const read = response && typeof response.text === 'function'
            ? response.text().catch(function () { return ''; })
            : Promise.resolve('');
          return read.then(function (body) {
            let message = '';
            try { message = JSON.parse(body).message || ''; } catch (e) { message = ''; }
            const error = new Error(status + ': ' + (message || 'theme request failed'));
            error.status = status;
            throw error;
          });
        }
        if (response.status === 204) return null;
        return response.json();
      });
    };

    const request = typeof options.request === 'function' ? options.request : defaultRequest;
    const state = { site: null, siteTheme: null, destroyed: false };
    let detachCss = null;
    let lastUnauthorizedKey = '';

    function assertSave(identity, currentToken) {
      if (state.destroyed) throw abortError();
      if (userId() !== identity || token() !== currentToken) throw abortError();
    }

    // A 401 only ends the session when it belongs to the token and identity
    // that are still current. A response from an account that was already
    // switched away cannot log the new account out.
    function notifyUnauthorized(identity, requestToken) {
      if (!identity || !requestToken) return;
      if (userId() !== identity || token() !== requestToken) return;
      const key = identity + '\n' + requestToken;
      if (key === lastUnauthorizedKey) return;
      lastUnauthorizedKey = key;
      if (typeof options.onUnauthorized === 'function') {
        try { options.onUnauthorized({ identity, status: 401 }); }
        catch (e) { /* the app's own logout path stays isolated */ }
      }
    }

    function send(path, requestOptions, identity, requestToken) {
      return Promise.resolve(request(path, requestOptions)).catch(function (error) {
        if (error && error.status === 401) notifyUnauthorized(identity, requestToken);
        throw error;
      });
    }

    // Anonymous site default. available=false or revision 0 is a genuine
    // failure: the loader returns null (never a fabricated builtin success),
    // so T1 keeps its reliable local cache instead of overwriting it.
    function loadSite(ctx) {
      ctx = ctx || {};
      if (state.destroyed || (ctx.signal && ctx.signal.aborted)) return Promise.resolve(null);
      const identity = normalizeIdentity(ctx.userId);
      const requestToken = token();
      return send('/web/api/theme-site', {
        method: 'GET', anonymous: true, signal: ctx.signal,
      }, null, requestToken).then(function (raw) {
        if (state.destroyed || (ctx.signal && ctx.signal.aborted) || userId() !== identity) throw abortError();
        const site = parseSite(raw, urlOptions);
        state.site = site;
        state.siteTheme = site.usable && site.theme ? site.theme : null;
        if (!site.usable) return null;
        return { themeId: site.themeId, mode: 'dark', revision: site.revision };
      });
    }

    // Anonymous callers cannot use the authenticated catalog. They get the
    // minimal builtin + site-default catalog so a custom site default really
    // applies on the login page. A logged-in caller gets the full catalog.
    function loadCatalog(ctx) {
      ctx = ctx || {};
      if (state.destroyed || (ctx.signal && ctx.signal.aborted)) return Promise.resolve([]);
      const identity = normalizeIdentity(ctx.userId);
      if (!identity) return Promise.resolve(state.siteTheme ? [state.siteTheme] : []);
      const requestToken = token();
      return send('/web/api/themes', {
        method: 'GET', signal: ctx.signal,
      }, identity, requestToken).then(function (raw) {
        if (state.destroyed || (ctx.signal && ctx.signal.aborted) || userId() !== identity) throw abortError();
        return parseCatalog(raw, urlOptions);
      });
    }

    function loadPreferences(ctx) {
      ctx = ctx || {};
      if (state.destroyed || (ctx.signal && ctx.signal.aborted)) return Promise.resolve(null);
      const identity = normalizeIdentity(ctx.userId);
      if (!identity) return Promise.resolve(null);
      const requestToken = token();
      return send('/web/api/appearance', {
        method: 'GET', signal: ctx.signal,
      }, identity, requestToken).then(function (raw) {
        if (state.destroyed || (ctx.signal && ctx.signal.aborted) || userId() !== identity) throw abortError();
        return parsePreference(raw);
      });
    }

    // Full replace payload {themeId, mode, expectedRevision}, header token
    // only, no query credentials. Nothing is reported as saved until this
    // promise resolves with a validated server preference. A 409 is surfaced
    // as-is (status preserved) and is never retried here.
    function savePreferences(payload) {
      payload = payload || {};
      if (state.destroyed) return Promise.resolve({ local: true });
      const identity = normalizeIdentity(payload.userId) || userId();
      const requestToken = token();
      if (!identity || !requestToken) return Promise.resolve({ local: true });
      const themeId = payload.themeId == null ? null : payload.themeId;
      if (themeId !== null && !validThemeId(themeId)) return Promise.reject(new Error('theme transport: invalid theme id'));
      if (!validMode(payload.mode)) return Promise.reject(new Error('theme transport: invalid mode'));
      if (!Number.isSafeInteger(payload.expectedRevision) || payload.expectedRevision < 0) {
        return Promise.reject(new Error('theme transport: invalid revision'));
      }

      let expected = payload.expectedRevision;
      const signal = payload.signal;
      const seed = expected >= 1
        ? Promise.resolve()
        : send('/web/api/appearance', { method: 'GET', signal }, identity, requestToken).then(function (raw) {
          const seeded = parsePreference(raw);
          if (!seeded) throw new Error('theme transport: invalid appearance response');
          expected = seeded.revision;
        });

      return seed.then(function () {
        if (signal && signal.aborted) throw abortError();
        assertSave(identity, requestToken);
        return send('/web/api/appearance', {
          method: 'PUT',
          signal,
          body: JSON.stringify({ themeId, mode: payload.mode, expectedRevision: expected }),
        }, identity, requestToken);
      }).then(function (raw) {
        assertSave(identity, requestToken);
        const preference = parsePreference(raw);
        if (!preference) throw new Error('theme transport: invalid appearance response');
        return preference;
      });
    }

    /* ---------- immutable stylesheet lifecycle ---------- */

    function attach(controller, attachOptions) {
      attachOptions = attachOptions || {};
      if (!controller || typeof controller.subscribe !== 'function' || !controller.getSnapshot) {
        return function () {};
      }
      let disposed = false;
      let initialized = false;
      let lastIdentity = null;
      let lastGeneration = null;
      let link = null;
      let linkHref = null;
      let cssToken = 0;

      function removeLink() {
        const current = link;
        link = null;
        linkHref = null;
        if (!current) return;
        try {
          if (typeof current.remove === 'function') current.remove();
          else if (current.parentNode && typeof current.parentNode.removeChild === 'function') current.parentNode.removeChild(current);
        } catch (e) { /* a detached node is already harmless */ }
      }

      function validHref(value) {
        if (typeof value !== 'string') return null;
        const match = CSS_URL.exec(value);
        if (!match) return null;
        return safeCssUrl(value, match[1], urlOptions);
      }

      function sync() {
        if (disposed) return;
        const snapshot = controller.getSnapshot();
        if (!snapshot) { removeLink(); return; }
        const changed = !initialized
          || snapshot.identity !== lastIdentity
          || snapshot.generation !== lastGeneration;
        if (changed) {
          const hadPreviousIdentity = initialized;
          initialized = true;
          lastIdentity = snapshot.identity;
          lastGeneration = snapshot.generation;
          // Entering a session, logging out or switching account invalidates
          // every stylesheet that has not finished loading yet. The old link
          // is removed on the identity tick itself; a new link is only allowed
          // after the next authoritative snapshot (the refresh for that
          // identity), so a previous account's stylesheet can never be reused
          // while the new session is still loading.
          cssToken += 1;
          removeLink();
          if (hadPreviousIdentity) return;
        }
        const resolved = snapshot.resolved;
        const href = resolved ? validHref(resolved.cssHref) : null;
        if (!href) {
          if (link) { cssToken += 1; removeLink(); }
          return;
        }
        if (link && linkHref === href) return;
        cssToken += 1;
        const tokenForLink = cssToken;
        removeLink();
        if (!doc || !doc.head || typeof doc.createElement !== 'function') return;
        const element = doc.createElement('link');
        element.setAttribute('rel', 'stylesheet');
        element.setAttribute('href', href);
        element.setAttribute('data-eio-theme-css', '1');
        const onLoad = function () {
          if (tokenForLink === cssToken && !disposed) return;
          // A late stylesheet from a previous account must not stay applied.
          if (element.parentNode && typeof element.parentNode.removeChild === 'function') {
            element.parentNode.removeChild(element);
          }
        };
        const onError = function () {
          if (tokenForLink !== cssToken || disposed) return;
          removeLink();
          if (typeof attachOptions.onCssError === 'function') {
            try { attachOptions.onCssError({ href, identity: snapshot.identity }); }
            catch (e) { /* a UI callback must not break appearance */ }
          }
        };
        if (typeof element.addEventListener === 'function') {
          element.addEventListener('load', onLoad);
          element.addEventListener('error', onError);
        } else {
          element.onload = onLoad;
          element.onerror = onError;
        }
        link = element;
        linkHref = href;
        try { doc.head.appendChild(element); }
        catch (e) { removeLink(); }
      }

      const unsubscribe = controller.subscribe(sync);
      sync();
      return function detach() {
        if (disposed) return;
        disposed = true;
        cssToken += 1;
        removeLink();
        unsubscribe();
      };
    }

    const transport = {
      loadSite,
      loadCatalog,
      loadPreferences,
      savePreferences,
      attach(controller, attachOptions) {
        if (detachCss) detachCss();
        detachCss = attach(controller, attachOptions);
        return detachCss;
      },
      getSiteStatus() {
        if (!state.site) return null;
        return {
          available: state.site.available,
          usable: state.site.usable,
          reason: state.site.reason,
          themeId: state.site.themeId,
          revision: state.site.revision,
          custom: !!state.siteTheme,
        };
      },
      isDestroyed() { return state.destroyed; },
      destroy() {
        if (state.destroyed) return;
        state.destroyed = true;
        if (detachCss) detachCss();
        detachCss = null;
        state.site = null;
        state.siteTheme = null;
      },
    };
    // app.js and older wiring read the dependency object directly.
    transport.deps = transport;
    return transport;
  }

  root.EIOThemeTransport = {
    create,
    parseSite,
    parseCatalog,
    parsePreference,
    adaptPublicTheme,
    safeCssUrl,
    safeAssetUrl,
    safePreviewUrl,
    isAbort,
  };
})();

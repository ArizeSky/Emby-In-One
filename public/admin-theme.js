/* Theme management for the admin panel.
 *
 * This module is loaded by admin.html *before* admin.js and exposes a single
 * global, EIOAdminTheme. admin.js keeps only thin delegating methods; all of
 * the catalog state, the CAS/revision bookkeeping, the upload dialog and the
 * fixed-URL preview live here.
 *
 * Contract (frozen by internal/backend/theme_api_types.go):
 *
 *   GET    /admin/api/themes                        -> {available,themes,site,quota,health}
 *   POST   /admin/api/themes          application/zip -> 201 AdminTheme
 *   PUT    /admin/api/themes/{id}/package  zip + If-Match -> 200 AdminTheme
 *   PATCH  /admin/api/themes/{id}     {enabled,expectedRevision} -> 200 AdminTheme
 *   DELETE /admin/api/themes/{id}     If-Match -> {success:true}
 *   PUT    /admin/api/theme-site      {themeId,expectedRevision} -> Site
 *
 * Rules this module follows:
 *  - every request carries an AbortController signal and a generation/epoch;
 *    a late response never overwrites newer state or a different session.
 *  - mutations are never optimistic: a row changes only from the server's
 *    response, and a 409 refreshes the list instead of pretending success.
 *  - a background refresh never rewrites the revision/enabled snapshot of a
 *    row that has an in-flight mutation or an open update dialog.
 *  - previews use only the server's immutable previewLightUrl/previewDarkUrl
 *    strings. The module never builds a URL, never adds a token and never
 *    touches the panel DOM or injects theme CSS; admin.html owns the single
 *    sandboxed iframe.
 *  - nothing here is user-specific: the admin API is already authenticated by
 *    admin.js. There is no localStorage, no DOM query and no eval.
 */
(function (root) {
  'use strict';

  var BUILTIN_ID = 'builtin';
  // The server budget (ThemePackageMaxBytes) and the only client-side file
  // limit. It is checked before the request so an oversized ZIP is not sent.
  var MAX_PACKAGE_BYTES = 1536 * 1024;

  // --- small helpers -------------------------------------------------------

  function isObject(value) {
    return value !== null && typeof value === 'object' && !Array.isArray(value);
  }

  function toText(value) {
    return typeof value === 'string' ? value : '';
  }

  function toInt(value) {
    return typeof value === 'number' && isFinite(value) && value > 0 ? Math.floor(value) : 0;
  }

  function toArray(value) {
    return Array.isArray(value) ? value : [];
  }

  function clone(source, extra) {
    var out = {};
    if (isObject(source)) {
      for (var key in source) {
        if (Object.prototype.hasOwnProperty.call(source, key)) out[key] = source[key];
      }
    }
    if (isObject(extra)) {
      for (var name in extra) {
        if (Object.prototype.hasOwnProperty.call(extra, name)) out[name] = extra[name];
      }
    }
    return out;
  }

  function isAbortError(error) {
    return !!error && (error.name === 'AbortError' || error.code === 'ABORT_ERR');
  }

  function errorMessage(error, fallback) {
    if (error && typeof error.message === 'string' && error.message) return error.message;
    return fallback;
  }

  // --- state ---------------------------------------------------------------

  function newState() {
    return {
      loaded: false,
      loading: false,
      error: '',
      available: false,
      themes: [],
      site: { available: false, themeId: BUILTIN_ID, revision: 0 },
      quota: { maxThemes: 0, usedThemes: 0, maxBytes: 0, usedBytes: 0 },
      health: { available: false, recovered: 0, missing: 0, orphanDirs: 0, orphanBytes: 0, problems: [] },
      notice: '',
      actionError: '',
      pending: {},
      dialog: null,
      preview: null
    };
  }

  // The generation counter, the AbortControllers and the selected File must not
  // live inside the state object: Vue makes data() deeply reactive, and a
  // proxied AbortController breaks in real browsers. They are keyed by the
  // stable state object instead.
  var guards = new WeakMap();

  function guardsFor(state) {
    var entry = guards.get(state);
    if (!entry) {
      entry = { generation: 0, epoch: 0, loadController: null, mutations: {}, file: null };
      guards.set(state, entry);
    }
    return entry;
  }

  function abortLoad(state) {
    var entry = guardsFor(state);
    if (entry.loadController) {
      try { entry.loadController.abort(); } catch (err) { /* already settled */ }
      entry.loadController = null;
    }
  }

  function abortMutations(state) {
    var entry = guardsFor(state);
    for (var key in entry.mutations) {
      if (!Object.prototype.hasOwnProperty.call(entry.mutations, key)) continue;
      try { entry.mutations[key].abort(); } catch (err) { /* already settled */ }
    }
    entry.mutations = {};
  }

  function clearPending(state) {
    var keys = Object.keys(state.pending || {});
    for (var i = 0; i < keys.length; i++) delete state.pending[keys[i]];
  }

  // --- frozen DTO normalization -------------------------------------------

  // Every field is copied explicitly so the view never renders `undefined` and
  // an unknown key from the server cannot leak into the template.
  function normalizeTheme(entry) {
    if (!isObject(entry) || typeof entry.id !== 'string' || entry.id === '') return null;
    var variants = [];
    toArray(entry.variants).forEach(function (variant) {
      if (typeof variant === 'string' && variant !== '') variants.push(variant);
    });
    return {
      id: entry.id,
      name: toText(entry.name) || entry.id,
      author: toText(entry.author),
      description: toText(entry.description),
      version: toText(entry.version),
      hash: toText(entry.hash),
      variants: variants,
      builtin: entry.builtin === true,
      enabled: entry.enabled === true,
      available: entry.available === true,
      revision: toInt(entry.revision),
      packageBytes: toInt(entry.packageBytes),
      currentHash: toText(entry.currentHash),
      previousHash: toText(entry.previousHash),
      problem: toText(entry.problem),
      cssUrl: toText(entry.cssUrl),
      previewUrl: toText(entry.previewUrl),
      backgroundUrl: toText(entry.backgroundUrl),
      previewLightUrl: toText(entry.previewLightUrl),
      previewDarkUrl: toText(entry.previewDarkUrl)
    };
  }

  function normalizeSite(value) {
    if (!isObject(value)) return null;
    return {
      available: value.available === true,
      themeId: toText(value.themeId) || BUILTIN_ID,
      revision: toInt(value.revision)
    };
  }

  function normalizeQuota(value) {
    var quota = isObject(value) ? value : {};
    return {
      maxThemes: toInt(quota.maxThemes),
      usedThemes: toInt(quota.usedThemes),
      maxBytes: toInt(quota.maxBytes),
      usedBytes: toInt(quota.usedBytes)
    };
  }

  function normalizeHealth(value) {
    var health = isObject(value) ? value : {};
    var problems = [];
    toArray(health.problems).forEach(function (problem) {
      if (typeof problem === 'string' && problem !== '') problems.push(problem);
    });
    return {
      available: health.available === true,
      recovered: toInt(health.recovered),
      missing: toInt(health.missing),
      orphanDirs: toInt(health.orphanDirs),
      orphanBytes: toInt(health.orphanBytes),
      problems: problems
    };
  }

  function normalizeCatalog(payload) {
    if (!isObject(payload)) throw new Error('主题目录响应格式无效');
    var themes = [];
    toArray(payload.themes).forEach(function (entry) {
      var theme = normalizeTheme(entry);
      if (theme) themes.push(theme);
    });
    return {
      available: payload.available === true,
      themes: themes,
      site: normalizeSite(payload.site) || { available: false, themeId: BUILTIN_ID, revision: 0 },
      quota: normalizeQuota(payload.quota),
      health: normalizeHealth(payload.health)
    };
  }

  function findTheme(state, themeId) {
    var themes = toArray(state.themes);
    for (var i = 0; i < themes.length; i++) {
      if (themes[i].id === themeId) return themes[i];
    }
    return null;
  }

  function upsertTheme(state, entry) {
    var themes = toArray(state.themes).slice();
    for (var i = 0; i < themes.length; i++) {
      if (themes[i].id === entry.id) {
        themes[i] = entry;
        state.themes = themes;
        return;
      }
    }
    themes.push(entry);
    state.themes = themes;
  }

  function removeTheme(state, themeId) {
    var kept = [];
    var themes = toArray(state.themes);
    for (var i = 0; i < themes.length; i++) {
      if (themes[i].id !== themeId) kept.push(themes[i]);
    }
    state.themes = kept;
  }

  // A row is "protected" while a mutation for it is in flight or while the
  // update dialog holds a revision snapshot. A background refresh must not
  // rewrite that premise underneath the user.
  function protectedIds(state) {
    var keep = {};
    var keys = Object.keys(state.pending || {});
    for (var i = 0; i < keys.length; i++) {
      var index = keys[i].indexOf(':');
      if (index >= 0) keep[keys[i].slice(index + 1)] = true;
    }
    if (state.dialog && state.dialog.mode === 'update' && state.dialog.themeId) {
      keep[state.dialog.themeId] = true;
    }
    return keep;
  }

  function applyCatalog(state, catalog, reconcileId) {
    state.available = catalog.available;
    state.site = catalog.site;
    state.quota = catalog.quota;
    state.health = catalog.health;

    if (!catalog.available) {
      // A catalog that says available=false is not an empty catalog to be
      // cached: drop the rows so no stale action can be taken on them.
      state.themes = [];
      state.loaded = false;
      state.error = '主题服务当前不可用';
      state.dialog = null;
      state.preview = null;
      guardsFor(state).file = null;
      return;
    }

    var previous = {};
    toArray(state.themes).forEach(function (theme) { previous[theme.id] = theme; });
    var keep = protectedIds(state);
    var rows = [];
    var freshIds = {};
    for (var i = 0; i < catalog.themes.length; i++) {
      var fresh = catalog.themes[i];
      freshIds[fresh.id] = true;
      var known = previous[fresh.id];
      if (known && keep[fresh.id] && fresh.id !== reconcileId) {
        rows.push(clone(fresh, { revision: known.revision, enabled: known.enabled }));
      } else {
        rows.push(fresh);
      }
    }
    state.themes = rows;
    state.loaded = true;
    state.error = '';

    // The preview URL is immutable and deliberately snapshotted at open time;
    // close it only when its theme is no longer installed.
    if (state.preview && !freshIds[state.preview.themeId]) state.preview = null;

    // An update dialog whose target vanished (deleted elsewhere) cannot be
    // submitted; close it instead of keeping a stale revision around.
    if (state.dialog && state.dialog.mode === 'update' && !freshIds[state.dialog.themeId]) {
      state.dialog = null;
      guardsFor(state).file = null;
      state.actionError = '该主题已不存在，更新窗口已关闭';
    }
  }

  // --- catalog loading -----------------------------------------------------

  function refreshThemes(state, options) {
    var opts = options || {};
    var api = opts.api;
    var entry = guardsFor(state);
    var generation = ++entry.generation;
    abortLoad(state);
    var controller = null;
    if (typeof AbortController === 'function') {
      controller = new AbortController();
      entry.loadController = controller;
    }
    if (!opts.silent || !state.loaded) state.loading = true;
    if (!opts.silent) state.error = '';

    return Promise.resolve()
      .then(function () {
        return api('/admin/api/themes', controller ? { signal: controller.signal } : {});
      })
      .then(function (payload) {
        if (entry.generation !== generation) return null;
        if (entry.loadController === controller) entry.loadController = null;
        state.loading = false;
        applyCatalog(state, normalizeCatalog(payload), opts.reconcileId);
        return state;
      })
      .catch(function (error) {
        if (entry.loadController === controller) entry.loadController = null;
        if (entry.generation !== generation || isAbortError(error)) return null;
        state.loading = false;
        // Keep whatever was loaded: a transient failure must not erase the
        // list (or the dialog) the user is looking at.
        state.error = errorMessage(error, '加载主题目录失败');
        return null;
      });
  }

  function silentRefresh(state, opts) {
    return refreshThemes(state, { api: opts.api, silent: true }).then(function () { return true; });
  }

  // --- leaving the page / logging out -------------------------------------

  // Called when the panel navigates away from the themes page. In-flight
  // requests are cancelled and transient UI (dialog/preview/notices) is
  // dropped, but the list stays so returning to the page shows data
  // immediately.
  function leave(state) {
    var entry = guardsFor(state);
    entry.epoch += 1;
    entry.generation += 1;
    abortLoad(state);
    abortMutations(state);
    entry.file = null;
    clearPending(state);
    state.loading = false;
    state.dialog = null;
    state.preview = null;
    state.notice = '';
    state.actionError = '';
    state.error = '';
  }

  // Called on logout or a 401: nothing from the previous administrator may be
  // rendered for the next session.
  function reset(state) {
    leave(state);
    var fresh = newState();
    state.loaded = fresh.loaded;
    state.available = fresh.available;
    state.themes = fresh.themes;
    state.site = fresh.site;
    state.quota = fresh.quota;
    state.health = fresh.health;
  }

  // --- mutation plumbing ---------------------------------------------------

  function actionKey(kind, themeId) {
    return themeId ? kind + ':' + themeId : kind;
  }

  function themePath(themeId) {
    return '/admin/api/themes/' + encodeURIComponent(themeId);
  }

  function isPending(state, key) {
    return !!(state.pending && state.pending[key] === true);
  }

  function isThemePending(state, themeId) {
    return isPending(state, actionKey('patch', themeId)) ||
      isPending(state, actionKey('update', themeId)) ||
      isPending(state, actionKey('delete', themeId));
  }

  function dialogKey(dialog) {
    if (!dialog) return '';
    return dialog.mode === 'update' ? actionKey('update', dialog.themeId) : 'install';
  }

  function dialogReady(state) {
    var dialog = state.dialog;
    if (!dialog) return false;
    if (isPending(state, dialogKey(dialog))) return false;
    return !!guardsFor(state).file;
  }

  // One mutation at a time per key. Resolves {ok,data} / {ok:false,error} /
  // {canceled:true}; it never throws, so callers always clear their UI flags.
  function runMutation(state, key, options, request) {
    if (isPending(state, key)) return Promise.resolve({ ok: false, skipped: true });
    var opts = options || {};
    var entry = guardsFor(state);
    var epoch = entry.epoch;
    var controller = null;
    if (typeof AbortController === 'function') {
      controller = new AbortController();
      entry.mutations[key] = controller;
    }
    state.pending[key] = true;
    state.actionError = '';
    state.notice = '';

    function finish() {
      if (entry.mutations[key] === controller) delete entry.mutations[key];
      if (state.pending[key]) delete state.pending[key];
    }

    return Promise.resolve()
      .then(function () { return request(controller ? controller.signal : undefined, opts); })
      .then(function (payload) {
        finish();
        if (entry.epoch !== epoch) return { ok: false, canceled: true };
        return { ok: true, data: payload };
      })
      .catch(function (error) {
        finish();
        if (entry.epoch !== epoch || isAbortError(error)) return { ok: false, canceled: true };
        return { ok: false, error: error };
      });
  }

  function mutationErrorText(error, fallback) {
    var message = errorMessage(error, fallback);
    if (error && error.status === 409) return message + '（列表已刷新，本次修改未保存）';
    if (error && error.status === 503) return message + '（主题服务暂不可用，本次操作未确认成功）';
    if (error && error.status === 401) return message + '（登录已失效，请重新登录）';
    return message;
  }

  // A 409 means the premise (revision or default reference) is stale. Refresh
  // the catalog and, for an open update dialog, adopt the new revision so a
  // retry is possible. Nothing is written locally as if the mutation worked.
  function reconcileAfterConflict(state, options, themeId, message, useDialog) {
    return refreshThemes(state, { api: options.api, silent: true, reconcileId: themeId })
      .then(function () {
        if (useDialog && state.dialog && state.dialog.mode === 'update' && state.dialog.themeId === themeId) {
          var row = findTheme(state, themeId);
          if (row) state.dialog.expectedRevision = toInt(row.revision);
          state.dialog.error = message;
          return;
        }
        state.actionError = message;
      });
  }

  function reportFailure(state, error, fallback, options, themeId, useDialog) {
    var message = mutationErrorText(error, fallback);
    if (useDialog && state.dialog) state.dialog.error = message;
    else state.actionError = message;
    if (error && error.status === 409 && options && options.api) {
      return reconcileAfterConflict(state, options, themeId, message, useDialog);
    }
    return Promise.resolve();
  }

  // --- upload / update dialog ---------------------------------------------

  function openUploadDialog(state) {
    state.dialog = {
      mode: 'install', themeId: '', themeName: '', expectedRevision: 0,
      fileName: '', fileSize: 0, error: ''
    };
    guardsFor(state).file = null;
    state.actionError = '';
    state.notice = '';
  }

  function openUpdateDialog(state, theme) {
    if (!theme || theme.builtin) return false;
    state.dialog = {
      mode: 'update',
      themeId: theme.id,
      themeName: theme.name || theme.id,
      expectedRevision: toInt(theme.revision),
      fileName: '', fileSize: 0, error: ''
    };
    guardsFor(state).file = null;
    state.actionError = '';
    state.notice = '';
    return true;
  }

  function closeDialog(state) {
    state.dialog = null;
    guardsFor(state).file = null;
  }

  // Validates the picked ZIP before anything reaches the network. The server
  // remains authoritative (zip media type, CRC, budgets); this only applies the
  // one client-side limit the contract fixes at 1.5 MiB.
  function selectFile(state, file) {
    var dialog = state.dialog;
    if (!dialog) return false;
    if (isPending(state, dialogKey(dialog))) return false;
    var entry = guardsFor(state);
    entry.file = null;
    dialog.fileName = '';
    dialog.fileSize = 0;
    dialog.error = '';
    if (!file) return false;
    var size = typeof file.size === 'number' ? file.size : 0;
    var name = typeof file.name === 'string' && file.name ? file.name : 'theme.zip';
    if (size <= 0) {
      dialog.error = '请选择非空的 ZIP 主题包';
      return false;
    }
    if (size > MAX_PACKAGE_BYTES) {
      dialog.error = '主题包超过 1.5 MiB 限制（当前 ' + formatBytes(size) + '），请压缩后重试';
      return false;
    }
    entry.file = file;
    dialog.fileName = name;
    dialog.fileSize = size;
    return true;
  }

  function submitDialog(state, options) {
    var opts = options || {};
    var dialog = state.dialog;
    if (!dialog) return Promise.resolve(false);
    var key = dialogKey(dialog);
    if (isPending(state, key)) return Promise.resolve(false);
    var file = guardsFor(state).file;
    if (!file) {
      dialog.error = '请选择主题包 ZIP 文件';
      return Promise.resolve(false);
    }
    if (dialog.mode === 'update' && (!dialog.themeId || dialog.expectedRevision <= 0)) {
      dialog.error = '主题 revision 无效，请关闭窗口并刷新列表后重试';
      return Promise.resolve(false);
    }
    if (dialog.mode === 'install' && !canInstall(state)) {
      dialog.error = '主题数量配额已满，请先删除不再使用的主题';
      return Promise.resolve(false);
    }
    var path = dialog.mode === 'update'
      ? themePath(dialog.themeId) + '/package'
      : '/admin/api/themes';
    var method = dialog.mode === 'update' ? 'PUT' : 'POST';
    var headers = { 'Content-Type': 'application/zip' };
    if (dialog.mode === 'update') headers['If-Match'] = String(dialog.expectedRevision);
    dialog.error = '';

    return runMutation(state, key, opts, function (signal) {
      return opts.api(path, { method: method, headers: headers, body: file, signal: signal });
    }).then(function (result) {
      if (result.canceled) return false;
      if (!result.ok) {
        return reportFailure(state, result.error,
          dialog.mode === 'update' ? '更新主题包失败' : '上传主题包失败',
          opts, dialog.themeId, true).then(function () { return false; });
      }
      var entry = normalizeTheme(result.data);
      if (!entry) {
        if (state.dialog) state.dialog.error = '服务端返回的主题数据格式无效，请刷新后确认';
        return false;
      }
      upsertTheme(state, entry);
      guardsFor(state).file = null;
      state.dialog = null;
      state.notice = dialog.mode === 'update'
        ? '主题包已更新（revision ' + entry.revision + '）'
        : '主题已安装';
      return silentRefresh(state, opts);
    });
  }

  // --- row actions ---------------------------------------------------------

  function toggleTheme(state, theme, options) {
    if (!theme || theme.builtin) return Promise.resolve(false);
    var opts = options || {};
    var enabled = !theme.enabled;
    var key = actionKey('patch', theme.id);
    return runMutation(state, key, opts, function (signal) {
      return opts.api(themePath(theme.id), {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ enabled: enabled, expectedRevision: theme.revision }),
        signal: signal
      });
    }).then(function (result) {
      if (result.canceled) return false;
      if (!result.ok) {
        return reportFailure(state, result.error, '保存启用状态失败', opts, theme.id, false)
          .then(function () { return false; });
      }
      var entry = normalizeTheme(result.data);
      if (!entry) {
        state.actionError = '服务端返回的主题数据格式无效，请刷新后确认';
        return false;
      }
      upsertTheme(state, entry);
      state.notice = entry.enabled ? '主题已启用' : '主题已停用';
      return silentRefresh(state, opts);
    });
  }

  function setSiteDefault(state, theme, options) {
    if (!theme) return Promise.resolve(false);
    var opts = options || {};
    if (!theme.builtin && (!theme.enabled || !theme.available)) {
      state.actionError = '只有已启用且可用的主题可以设为站点默认';
      return Promise.resolve(false);
    }
    var expectedRevision = toInt(state.site && state.site.revision);
    var themeId = theme.builtin ? BUILTIN_ID : theme.id;
    return runMutation(state, 'site', opts, function (signal) {
      return opts.api('/admin/api/theme-site', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ themeId: themeId, expectedRevision: expectedRevision }),
        signal: signal
      });
    }).then(function (result) {
      if (result.canceled) return false;
      if (!result.ok) {
        return reportFailure(state, result.error, '更新站点默认主题失败', opts, theme.id, false)
          .then(function () { return false; });
      }
      var site = normalizeSite(result.data);
      if (!site) {
        state.actionError = '站点设置响应格式无效，请刷新后确认';
        return false;
      }
      state.site = site;
      state.notice = '站点默认主题已更新为 ' + site.themeId;
      return silentRefresh(state, opts);
    });
  }

  function deleteTheme(state, theme, options) {
    if (!theme || theme.builtin) return Promise.resolve(false);
    var opts = options || {};
    var key = actionKey('delete', theme.id);
    return runMutation(state, key, opts, function (signal) {
      return opts.api(themePath(theme.id), {
        method: 'DELETE',
        headers: { 'If-Match': String(theme.revision) },
        signal: signal
      });
    }).then(function (result) {
      if (result.canceled) return false;
      if (!result.ok) {
        return reportFailure(state, result.error, '删除主题失败', opts, theme.id, false)
          .then(function () { return false; });
      }
      if (state.preview && state.preview.themeId === theme.id) state.preview = null;
      removeTheme(state, theme.id);
      state.notice = '主题已删除';
      return silentRefresh(state, opts);
    });
  }

  // --- fixed preview -------------------------------------------------------

  // Only same-origin immutable theme resources are accepted. The value is used
  // verbatim in the iframe src, so nothing is ever appended (in particular no
  // token and no query parameter the server did not return).
  function safeResourceUrl(value) {
    if (typeof value !== 'string' || value === '') return '';
    if (value.indexOf('/web/themes/') !== 0) return '';
    if (value.indexOf('//') >= 0) return '';
    return value;
  }

  function openPreview(state, theme, mode) {
    if (!theme || theme.builtin) return false;
    var lightUrl = safeResourceUrl(theme.previewLightUrl);
    var darkUrl = safeResourceUrl(theme.previewDarkUrl);
    if (!lightUrl && !darkUrl) {
      state.actionError = '该主题没有生成预览文档';
      return false;
    }
    var chosen = mode === 'dark' ? 'dark' : 'light';
    if (chosen === 'light' && !lightUrl) chosen = 'dark';
    if (chosen === 'dark' && !darkUrl) chosen = 'light';
    state.preview = {
      themeId: theme.id,
      name: theme.name || theme.id,
      mode: chosen,
      lightUrl: lightUrl,
      darkUrl: darkUrl
    };
    return true;
  }

  function setPreviewMode(state, mode) {
    var preview = state.preview;
    if (!preview) return false;
    if (mode === 'dark') {
      if (!preview.darkUrl) return false;
      preview.mode = 'dark';
      return true;
    }
    if (mode === 'light') {
      if (!preview.lightUrl) return false;
      preview.mode = 'light';
      return true;
    }
    return false;
  }

  function closePreview(state) {
    state.preview = null;
  }

  function previewSrc(state) {
    var preview = state.preview;
    if (!preview) return '';
    return preview.mode === 'dark' ? preview.darkUrl : preview.lightUrl;
  }

  // --- view helpers --------------------------------------------------------

  function formatBytes(value) {
    var bytes = toInt(value);
    if (bytes < 1024) return bytes + ' B';
    if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + ' KiB';
    return (bytes / (1024 * 1024)).toFixed(2) + ' MiB';
  }

  function canInstall(state) {
    if (!state.available) return false;
    if (isPending(state, 'install')) return false;
    var quota = state.quota || {};
    var max = toInt(quota.maxThemes);
    if (max > 0 && toInt(quota.usedThemes) >= max) return false;
    return true;
  }

  function themeStateText(theme) {
    if (!theme) return '';
    if (theme.builtin) return '内置';
    if (!theme.available) return '不可用';
    if (!theme.enabled) return '已停用';
    return '已启用';
  }

  function themeStateClass(theme) {
    if (!theme) return 'bg-slate-100 text-slate-600';
    if (theme.builtin) return 'bg-blue-50 text-blue-600';
    if (!theme.available) return 'bg-red-50 text-red-600';
    if (!theme.enabled) return 'bg-slate-100 text-slate-600';
    return 'bg-green-50 text-green-600';
  }

  function isDefaultTheme(state, theme) {
    return !!theme && !!state.site && state.site.themeId === theme.id;
  }

  function quotaText(state) {
    var quota = state.quota || {};
    return toInt(quota.usedThemes) + ' / ' + toInt(quota.maxThemes) + ' 个主题 · ' +
      formatBytes(quota.usedBytes) + ' / ' + formatBytes(quota.maxBytes);
  }

  var api = {
    BUILTIN_ID: BUILTIN_ID,
    MAX_PACKAGE_BYTES: MAX_PACKAGE_BYTES,
    newState: newState,
    refreshThemes: refreshThemes,
    leave: leave,
    reset: reset,
    openUploadDialog: openUploadDialog,
    openUpdateDialog: openUpdateDialog,
    closeDialog: closeDialog,
    selectFile: selectFile,
    submitDialog: submitDialog,
    toggleTheme: toggleTheme,
    setSiteDefault: setSiteDefault,
    deleteTheme: deleteTheme,
    openPreview: openPreview,
    setPreviewMode: setPreviewMode,
    closePreview: closePreview,
    previewSrc: previewSrc,
    isPending: isPending,
    isThemePending: isThemePending,
    dialogReady: dialogReady,
    canInstall: canInstall,
    formatBytes: formatBytes,
    themeStateText: themeStateText,
    themeStateClass: themeStateClass,
    isDefaultTheme: isDefaultTheme,
    quotaText: quotaText
  };

  root.EIOAdminTheme = api;
})(typeof globalThis !== 'undefined' ? globalThis : this);

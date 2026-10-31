/* Upstream viewing activity and viewing-retention reminders (admin panel).
 *
 * This module is loaded by admin.html *before* admin.js and exposes a single
 * global, EIOAdminActivity. It owns everything that would otherwise bloat
 * admin.js: the seven-state badge mapping, remaining-time formatting from the
 * server's own serverNow/dueAt pair, the derived (filter/sort) list view, the
 * per-node settings dialog controller with generation/abort guards, and the
 * local-time <-> UTC RFC3339 conversion for the manual reference date.
 *
 * Rules it follows:
 *  - state comes from the backend `state` field; the browser clock never
 *    decides green/yellow/orange/red. client time is only used to format a
 *    date the user typed, or to render the stored observation in local time.
 *  - null timestamps render as "暂无记录", never as 1970-01-01.
 *  - the DTO has no account-scope digest, credential hash or user entries; the
 *    UI never invents any and never sends anything but the six policy fields.
 *  - a reminder is a local observation, not proof the upstream account stayed
 *    alive. That caveat is part of the tooltips/dialog text, not just docs.
 */
(function (root) {
  'use strict';

  var STATE_META = {
    normal: { label: '正常', tone: 'green', className: 'bg-green-50 text-green-700', rank: 3, risk: false },
    warning: { label: '留意', tone: 'yellow', className: 'bg-amber-50 text-amber-700', rank: 2, risk: true },
    urgent: { label: '紧急', tone: 'orange', className: 'bg-orange-50 text-orange-600', rank: 1, risk: true },
    overdue: { label: '超期', tone: 'red', className: 'bg-red-50 text-red-700', rank: 0, risk: true },
    disabled: { label: '未启用', tone: 'gray', className: 'bg-slate-100 text-slate-600', rank: 6, risk: false },
    unknown: { label: '未知', tone: 'gray', className: 'bg-slate-100 text-slate-600', rank: 4, risk: false },
    unavailable: { label: '不可用', tone: 'gray', className: 'bg-slate-100 text-slate-600', rank: 5, risk: false }
  };

  var STATE_KEYS = ['normal', 'warning', 'urgent', 'overdue', 'disabled', 'unknown', 'unavailable'];
  var FILTER_VALUES = ['all', 'attention'].concat(STATE_KEYS);
  var SORT_VALUES = ['default', 'risk'];

  var DISCLAIMER = '仅为本代理观测到的播放提醒，不代表上游账号保活成功，也不会自动播放。';

  // --- generic helpers -----------------------------------------------------

  function isObject(value) {
    return value !== null && typeof value === 'object' && !Array.isArray(value);
  }

  function pad(value) {
    return value < 10 ? '0' + value : String(value);
  }

  function activityOf(server) {
    if (!isObject(server)) return null;
    return isObject(server.activity) ? server.activity : null;
  }

  function normalizeFilter(filter) {
    return FILTER_VALUES.indexOf(filter) >= 0 ? filter : 'all';
  }

  function normalizeSort(sort) {
    return SORT_VALUES.indexOf(sort) >= 0 ? sort : 'default';
  }

  // --- state badges --------------------------------------------------------

  // The backend state is authoritative. Unavailability is checked first: a
  // stale/partial payload that still says "normal" must not turn green when the
  // store already reported available=false.
  function dtoState(dto) {
    if (!isObject(dto)) return 'unknown';
    // Availability is the gate: false means the store reported a problem, and a
    // payload that does not explicitly say available=true is malformed. Neither
    // may borrow a stale "normal" and render green.
    if (dto.available !== true) return 'unavailable';
    if (typeof dto.state === 'string' && Object.prototype.hasOwnProperty.call(STATE_META, dto.state)) return dto.state;
    return 'unknown';
  }

  function rowState(server) {
    return dtoState(activityOf(server));
  }

  function metaFor(state) {
    return Object.prototype.hasOwnProperty.call(STATE_META, state) ? STATE_META[state] : STATE_META.unknown;
  }

  function badgeFor(stateOrServer) {
    var state = typeof stateOrServer === 'string' ? stateOrServer : rowState(stateOrServer);
    var meta = metaFor(state);
    return { state: state, label: meta.label, tone: meta.tone, className: meta.className, risk: meta.risk, rank: meta.rank };
  }

  function isRiskState(state) {
    return metaFor(state).risk;
  }

  // Tiers whose position can be compared by how much time is left. disabled /
  // unknown / unavailable have no meaningful remaining time, so they keep their
  // original order inside their tier.
  var RATIO_SORT_STATES = { normal: true, warning: true, urgent: true, overdue: true };

  // Smaller = closer to (or past) the deadline. Milliseconds from the server's
  // own serverNow/dueAt pair are preferred; remainingRatio is the fallback
  // signal when a snapshot lacks one of the timestamps.
  function remainingSortSignal(dto) {
    if (!isObject(dto)) return null;
    var now = parseTime(dto.serverNow);
    var due = parseTime(dto.dueAt);
    if (now !== null && due !== null) return due - now;
    if (typeof dto.remainingRatio === 'number' && isFinite(dto.remainingRatio)) return dto.remainingRatio;
    return null;
  }

  function compareRiskRows(a, b) {
    var rankDiff = metaFor(a.state).rank - metaFor(b.state).rank;
    if (rankDiff !== 0) return rankDiff;
    if (RATIO_SORT_STATES[a.state] && RATIO_SORT_STATES[b.state]) {
      var left = remainingSortSignal(activityOf(a.server));
      var right = remainingSortSignal(activityOf(b.server));
      if (left !== null && right !== null && left !== right) return left - right;
      if (left !== null && right === null) return -1;
      if (left === null && right !== null) return 1;
    }
    return a.index - b.index;
  }

  // "attention" is the risk filter: only warning/urgent/overdue are listed.
  function matchesFilter(state, filter) {
    var normalized = normalizeFilter(filter);
    if (normalized === 'all') return true;
    if (normalized === 'attention') return isRiskState(state);
    return state === normalized;
  }

  // Derived, read-only view of the upstream list. The input array and its
  // order are never touched: each row remembers its original index, which is
  // what the existing reorder/edit actions keep using.
  function deriveList(upstreamList, options) {
    var list = Array.isArray(upstreamList) ? upstreamList : [];
    var filter = normalizeFilter(options && options.filter);
    var sort = normalizeSort(options && options.sort);
    var rows = [];
    for (var i = 0; i < list.length; i++) {
      var state = rowState(list[i]);
      if (!matchesFilter(state, filter)) continue;
      rows.push({ server: list[i], index: i, id: list[i] && (list[i].id || list[i].index), state: state });
    }
    if (sort === 'risk') {
      rows.sort(compareRiskRows);
    }
    return rows;
  }

  // Moving rows up/down writes the persisted upstream order, so it stays
  // available only in the plain view. Filters and risk sorting are display-only.
  function reorderAllowed(options) {
    return normalizeFilter(options && options.filter) === 'all' && normalizeSort(options && options.sort) === 'default';
  }

  function filterOptions() {
    return [
      { value: 'all', label: '全部节点' },
      { value: 'attention', label: '仅风险（留意/紧急/超期）' },
      { value: 'normal', label: '正常' },
      { value: 'warning', label: '留意' },
      { value: 'urgent', label: '紧急' },
      { value: 'overdue', label: '超期' },
      { value: 'unknown', label: '未知' },
      { value: 'disabled', label: '未启用' },
      { value: 'unavailable', label: '不可用' }
    ];
  }

  function sortOptions() {
    return [
      { value: 'default', label: '默认（服务器顺序）' },
      { value: 'risk', label: '按保活风险（超期在前）' }
    ];
  }

  // --- timestamps ----------------------------------------------------------

  function parseTime(value) {
    if (typeof value !== 'string' || !value.trim()) return null;
    var millis = Date.parse(value);
    return isFinite(millis) ? millis : null;
  }

  function formatLocal(value) {
    var millis = parseTime(value);
    if (millis === null) return '';
    var date = new Date(millis);
    return date.getFullYear() + '-' + pad(date.getMonth() + 1) + '-' + pad(date.getDate()) +
      ' ' + pad(date.getHours()) + ':' + pad(date.getMinutes()) + ':' + pad(date.getSeconds());
  }

  function utcOffsetLabel(date) {
    var minutes = -date.getTimezoneOffset();
    var sign = minutes < 0 ? '-' : '+';
    var abs = Math.abs(minutes);
    return 'UTC' + sign + pad(Math.floor(abs / 60)) + ':' + pad(abs % 60);
  }

  // Precise tooltip: local rendering plus the UTC instant the server stored.
  function formatExact(value) {
    var millis = parseTime(value);
    if (millis === null) return '暂无记录';
    var date = new Date(millis);
    return formatLocal(value) + ' (' + utcOffsetLabel(date) + ') / ' + date.toISOString().replace(/\.\d{3}Z$/, 'Z');
  }

  function evidenceText(server, field) {
    var dto = activityOf(server);
    if (!dto) return '暂无记录';
    var text = formatLocal(dto[field]);
    return text || '暂无记录';
  }

  function evidenceTitle(server, field) {
    var dto = activityOf(server);
    if (!dto) return '暂无记录';
    return formatExact(dto[field]);
  }

  function formatDuration(millis) {
    var abs = Math.abs(millis);
    if (!isFinite(abs)) return '';
    var days = Math.floor(abs / 86400000);
    var hours = Math.floor((abs % 86400000) / 3600000);
    var minutes = Math.floor((abs % 3600000) / 60000);
    if (days > 0) return days + ' 天' + (hours > 0 ? ' ' + hours + ' 小时' : '');
    if (hours > 0) return hours + ' 小时' + (minutes > 0 ? ' ' + minutes + ' 分钟' : '');
    if (minutes > 0) return minutes + ' 分钟';
    return '不足 1 分钟';
  }

  function ratioPercent(dto) {
    var ratio = dto && dto.remainingRatio;
    if (typeof ratio !== 'number' || !isFinite(ratio)) return null;
    return Math.round(ratio * 100) + '%';
  }

  // Remaining time is derived from the server's serverNow/dueAt pair, so a
  // wrong or shifted browser clock cannot change the status. It is a plain
  // rendering of the last snapshot: there is no timer and no polling here.
  function remainingText(dto) {
    if (!isObject(dto)) return '暂无保活状态';
    var state = dtoState(dto);
    if (state === 'disabled') return '未启用提醒';
    if (state === 'unavailable') return '保活数据不可用';
    if (state === 'unknown') return '暂无参考时间';
    var now = parseTime(dto.serverNow);
    var due = parseTime(dto.dueAt);
    if (now === null || due === null) return '剩余时间未知';
    var remaining = due - now;
    var percent = ratioPercent(dto);
    var suffix = percent === null ? '' : '（剩余 ' + percent + '）';
    if (remaining <= 0) return '已超期 ' + formatDuration(remaining) + suffix;
    return '剩余 ' + formatDuration(remaining) + suffix;
  }

  function remainingForServer(server) {
    return remainingText(activityOf(server));
  }

  function sourceLabel(dto) {
    if (!isObject(dto)) return '';
    if (dto.referenceSource === 'manual') return '人工校准';
    if (dto.referenceSource === 'observed') return '自动观测';
    return '';
  }

  function sourceForServer(server) {
    return sourceLabel(activityOf(server));
  }

  function intervalText(unit, value) {
    var text = toInteger(value);
    var amount = text === null ? 0 : text;
    if (unit === 'months') {
      return '每 ' + amount + ' 个 UTC 自然月（月末按目标月最后一天计算）';
    }
    return '每 ' + amount + ' 天（UTC 固定 24 小时）';
  }

  function serverTooltip(server) {
    var dto = activityOf(server);
    if (!dto) return '暂无观看/保活记录。' + DISCLAIMER;
    var lines = [];
    lines.push('最近活动：' + formatExact(dto.lastActivityAt));
    lines.push('有效观看：' + formatExact(dto.lastQualifiedAt));
    lines.push('保活提醒：' + badgeFor(dtoState(dto)).label + '（' + remainingText(dto) + '）');
    lines.push('基准时间：' + formatExact(dto.referenceAt) + (sourceLabel(dto) ? '（' + sourceLabel(dto) + '）' : ''));
    lines.push('周期：' + intervalText(dto.intervalUnit, dto.intervalValue));
    lines.push('截止时间：' + formatExact(dto.dueAt));
    if (dto.available === false && dto.message) lines.push('不可用原因：' + dto.message);
    lines.push('说明：最近活动是上游确认上报的播放事件；有效观看是同一可靠会话累计达到门槛。' + DISCLAIMER);
    return lines.join('\n');
  }

  // Rows for the settings dialog; keeps the template free of formatting logic.
  function detailRows(dto) {
    if (!isObject(dto)) return [];
    return [
      { label: '最近活动', value: formatExact(dto.lastActivityAt), title: formatExact(dto.lastActivityAt) },
      { label: '有效观看', value: formatExact(dto.lastQualifiedAt), title: '同一可靠会话累计达到最低时长，且上游确认上报；不是保活成功证明。' },
      { label: '基准时间', value: formatExact(dto.referenceAt) + (sourceLabel(dto) ? '（' + sourceLabel(dto) + '）' : '（无参考）') },
      { label: '截止时间', value: formatExact(dto.dueAt) },
      { label: '剩余', value: remainingText(dto) + (ratioPercent(dto) ? '，比例 ' + ratioPercent(dto) : '') },
      { label: '周期', value: intervalText(dto.intervalUnit, dto.intervalValue) }
    ];
  }

  // --- manual reference date (local input <-> UTC RFC3339) ------------------

  function utcToLocalInput(value) {
    var millis = parseTime(value);
    if (millis === null) return '';
    var date = new Date(millis);
    return date.getFullYear() + '-' + pad(date.getMonth() + 1) + '-' + pad(date.getDate()) +
      'T' + pad(date.getHours()) + ':' + pad(date.getMinutes()) + ':' + pad(date.getSeconds());
  }

  // <input type="datetime-local"> has no timezone. The value the user sees is
  // local time; the API stores UTC. Invalid calendar dates (for example
  // 2025-02-30) are rejected instead of being silently normalized.
  function localInputToRFC3339(value) {
    if (value === null || value === undefined) return null;
    var text = String(value).trim();
    if (!text) return null;
    var match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::(\d{2}))?$/.exec(text);
    if (!match) throw new Error('人工参考日期格式无效，请使用本地日期时间');
    var year = Number(match[1]);
    var month = Number(match[2]);
    var day = Number(match[3]);
    var hour = Number(match[4]);
    var minute = Number(match[5]);
    var second = Number(match[6] || '0');
    var date = new Date(year, month - 1, day, hour, minute, second, 0);
    if (date.getFullYear() !== year || date.getMonth() !== month - 1 || date.getDate() !== day ||
      date.getHours() !== hour || date.getMinutes() !== minute || date.getSeconds() !== second) {
      throw new Error('人工参考日期无效，请检查该日期是否存在');
    }
    return date.toISOString().replace(/\.\d{3}Z$/, 'Z');
  }

  // --- policy form ---------------------------------------------------------

  function toInteger(value) {
    if (typeof value === 'number') return Number.isInteger(value) ? value : null;
    if (value === null || value === undefined) return null;
    var text = String(value).trim();
    if (!/^-?\d+$/.test(text)) return null;
    var parsed = Number(text);
    return Number.isSafeInteger(parsed) ? parsed : null;
  }

  function isRevision(value) {
    // The backend only ever hands out positive opaque revisions (0 means the
    // plan never observed a stored revision), and 2^53-1 keeps them exact in JS.
    return typeof value === 'number' && Number.isSafeInteger(value) && value > 0;
  }

  function formFromDTO(dto) {
    return {
      enabled: dto.enabled === true,
      intervalUnit: dto.intervalUnit === 'months' ? 'months' : 'days',
      intervalValue: toInteger(dto.intervalValue) === null ? '' : toInteger(dto.intervalValue),
      minWatchSeconds: toInteger(dto.minWatchSeconds) === null ? '' : toInteger(dto.minWatchSeconds),
      // A null manual reference stays empty on purpose: never fill in "now".
      manualReferenceLocal: dto.manualReferenceAt ? utcToLocalInput(dto.manualReferenceAt) : '',
      expectedRevision: isRevision(dto.revision) ? dto.revision : null
    };
  }

  function integerInRange(value, min, max, label) {
    var parsed = toInteger(value);
    if (parsed === null) throw new Error(label + '必须是整数');
    if (parsed < min || parsed > max) throw new Error(label + '必须在 ' + min + ' 到 ' + max + ' 之间');
    return parsed;
  }

  function buildPayload(state) {
    if (!state || !state.form) throw new Error('保活设置尚未加载，无法保存');
    var form = state.form;
    var unit = form.intervalUnit === 'months' ? 'months' : (form.intervalUnit === 'days' ? 'days' : null);
    if (!unit) throw new Error('周期单位只能是 days 或 months');
    var intervalValue = integerInRange(form.intervalValue, 1, unit === 'days' ? 3650 : 120, '周期数值');
    var minWatchSeconds = integerInRange(form.minWatchSeconds, 10, 3600, '最低有效观看秒数');
    var manualReferenceAt = localInputToRFC3339(form.manualReferenceLocal);
    if (manualReferenceAt !== null) {
      var serverNow = parseTime(state.dto && state.dto.serverNow);
      var manual = parseTime(manualReferenceAt);
      if (serverNow !== null && manual !== null && manual > serverNow) {
        throw new Error('人工参考时间不能晚于服务器当前时间');
      }
    }
    if (!isRevision(form.expectedRevision)) throw new Error('保活设置版本未知，请刷新后再保存');
    return {
      enabled: form.enabled === true,
      intervalUnit: unit,
      intervalValue: intervalValue,
      minWatchSeconds: minWatchSeconds,
      manualReferenceAt: manualReferenceAt,
      expectedRevision: form.expectedRevision
    };
  }

  // --- settings dialog controller -----------------------------------------

  function newDialogState() {
    return {
      open: false,
      serverId: null,
      serverName: '',
      loading: false,
      loaded: false,
      error: '',
      notice: '',
      dto: null,
      form: null,
      saving: false,
      saveError: '',
      savedNotice: '',
      conflict: false
    };
  }

  // The generation counter and the AbortController must not live inside the
  // dialog state object: Vue makes data() deeply reactive, and a proxied
  // AbortController rejects `abort()` / its proxied signal rejects `fetch()`
  // with "Illegal invocation" in real browsers. They are kept in a WeakMap
  // keyed by the (stable) state object instead.
  var dialogGuards = new WeakMap();

  function guardsFor(state) {
    var guards = dialogGuards.get(state);
    if (!guards) {
      guards = { generation: 0, controller: null };
      dialogGuards.set(state, guards);
    }
    return guards;
  }

  function activityPath(serverId) {
    return '/admin/api/upstream/' + encodeURIComponent(String(serverId)) + '/activity';
  }

  function abortInflight(state) {
    var guards = guardsFor(state);
    if (guards.controller) {
      try { guards.controller.abort(); } catch (err) { /* already settled */ }
      guards.controller = null;
    }
  }

  function isCurrent(state, generation) {
    return state.open && guardsFor(state).generation === generation;
  }

  function isAbortError(error) {
    return !!error && (error.name === 'AbortError' || error.code === 'ABORT_ERR');
  }

  function errorMessage(error, fallback) {
    if (error && typeof error.message === 'string' && error.message) return error.message;
    return fallback;
  }

  function applyDTO(state, dto) {
    if (!isObject(dto)) throw new Error('保活状态响应格式无效');
    state.dto = dto;
    state.loaded = true;
    state.form = formFromDTO(dto);
    state.notice = dto.available === true
      ? ''
      : (dto.message || '保活数据当前不可用，只能刷新，不能以缺省版本保存');
  }

  function canSave(state) {
    if (!state || !state.open || !state.loaded || state.saving || state.loading) return false;
    if (state.conflict) return false;
    // Strictly available: a malformed DTO without available=true must not be
    // saved with a guessed/default revision.
    if (!isObject(state.dto) || state.dto.available !== true) return false;
    if (!isObject(state.form)) return false;
    return isRevision(state.form.expectedRevision);
  }

  function loadActivity(state, api, generation) {
    var expectedId = state.serverId;
    var controller = null;
    if (typeof AbortController === 'function') {
      controller = new AbortController();
      guardsFor(state).controller = controller;
    }
    state.loading = true;
    state.error = '';
    state.saveError = '';
    return Promise.resolve()
      .then(function () {
        // The request is issued from a microtask: if the dialog was closed (or
        // superseded) in between, no already-aborted/stale request may go out.
        if (!isCurrent(state, generation)) return null;
        return api(activityPath(expectedId), controller ? { signal: controller.signal } : {});
      })
      .then(function (dto) {
        if (!isCurrent(state, generation) || state.serverId !== expectedId) return null;
        try {
          applyDTO(state, dto);
        } catch (err) {
          state.loaded = false;
          state.dto = null;
          state.form = null;
          state.error = errorMessage(err, '保活状态响应格式无效');
        }
        return dto;
      })
      .catch(function (err) {
        if (!isCurrent(state, generation) || state.serverId !== expectedId || isAbortError(err)) return null;
        state.loaded = false;
        state.dto = null;
        state.form = null;
        state.error = errorMessage(err, '加载保活设置失败');
        return null;
      })
      .then(function (result) {
        if (guardsFor(state).generation === generation) state.loading = false;
        return result;
      });
  }

  function openActivity(state, options) {
    if (!state) return Promise.resolve(null);
    var serverId = options && options.serverId;
    abortInflight(state);
    var guards = guardsFor(state);
    guards.generation += 1;
    var generation = guards.generation;
    state.open = true;
    state.serverId = serverId === null || serverId === undefined ? '' : serverId;
    state.serverName = (options && options.serverName) || '';
    state.loaded = false;
    state.error = '';
    state.notice = '';
    state.dto = null;
    state.form = null;
    state.saving = false;
    state.saveError = '';
    state.savedNotice = '';
    state.conflict = false;
    state.loading = true;
    return loadActivity(state, options.api, generation);
  }

  // Refreshing is allowed even when the data was reported unavailable; it is
  // the only way to obtain a trustworthy revision again. It is refused while a
  // save is in flight: forcing a new generation would leave the old save's
  // completion unable to clear state.saving, which would stick the dialog.
  function refreshActivity(state, options) {
    if (!state || !state.open) return Promise.resolve(false);
    if (state.saving) return Promise.resolve(false);
    abortInflight(state);
    var guards = guardsFor(state);
    guards.generation += 1;
    var generation = guards.generation;
    state.savedNotice = '';
    state.saveError = '';
    state.conflict = false;
    return loadActivity(state, options.api, generation);
  }

  function closeActivity(state) {
    if (!state) return;
    abortInflight(state);
    guardsFor(state).generation += 1;
    state.open = false;
    state.loading = false;
    state.saving = false;
    state.error = '';
    state.saveError = '';
    state.savedNotice = '';
    state.conflict = false;
    state.form = null;
    state.dto = null;
    state.loaded = false;
    state.serverId = null;
    state.serverName = '';
    state.notice = '';
  }

  function saveActivity(state, options) {
    if (!canSave(state)) {
      if (state && state.open && !state.saving) {
        if (state.dto && state.dto.available !== true) {
          state.saveError = state.saveError || '保活数据当前不可用：请先刷新，不能以缺省版本保存';
        } else if (state.conflict) {
          state.saveError = state.saveError || '保活设置已在别处更新，请刷新后重试';
        }
      }
      return Promise.resolve(false);
    }
    var payload;
    try {
      payload = buildPayload(state);
    } catch (err) {
      state.saveError = errorMessage(err, '保活设置参数无效');
      state.savedNotice = '';
      return Promise.resolve(false);
    }
    var guards = guardsFor(state);
    var generation = guards.generation;
    var expectedId = state.serverId;
    var controller = null;
    if (typeof AbortController === 'function') {
      controller = new AbortController();
      guardsFor(state).controller = controller;
    }
    state.saving = true;
    state.saveError = '';
    state.savedNotice = '';
    state.conflict = false;
    return Promise.resolve()
      .then(function () {
        // Same microtask guard as loadActivity: closing the dialog must cancel
        // the save before it reaches the network, not after it was aborted.
        if (!isCurrent(state, generation)) return null;
        return options.api(activityPath(expectedId), {
          method: 'PUT',
          body: JSON.stringify(payload),
          signal: controller ? controller.signal : undefined
        });
      })
      .then(function (dto) {
        if (!isCurrent(state, generation) || state.serverId !== expectedId) return false;
        try {
          applyDTO(state, dto);
          state.savedNotice = '保活设置已保存';
        } catch (err) {
          state.saveError = errorMessage(err, '保存结果格式无效，请刷新确认');
        }
        return true;
      })
      .catch(function (err) {
        if (!isCurrent(state, generation) || state.serverId !== expectedId || isAbortError(err)) return false;
        if (err && err.status === 409) {
          // The form is stale. Keep the user's input visible, but require a
          // refresh before another save so the old revision is not resent.
          state.conflict = true;
          state.saveError = errorMessage(err, '保活设置已在别处更新，请刷新后重试');
        } else {
          state.saveError = errorMessage(err, '保存保活设置失败');
        }
        return false;
      })
      .then(function (saved) {
        if (guards.generation === generation) {
          state.saving = false;
          if (guards.controller === controller) guards.controller = null;
        }
        return saved;
      });
  }

  var api = {
    STATE_META: STATE_META,
    STATE_KEYS: STATE_KEYS,
    FILTER_VALUES: FILTER_VALUES,
    SORT_VALUES: SORT_VALUES,
    DISCLAIMER: DISCLAIMER,
    newDialogState: newDialogState,
    badgeFor: badgeFor,
    badgeForDTO: function (dto) { return badgeFor(dtoState(dto)); },
    dtoState: dtoState,
    rowState: rowState,
    isRiskState: isRiskState,
    matchesFilter: matchesFilter,
    deriveList: deriveList,
    reorderAllowed: reorderAllowed,
    filterOptions: filterOptions,
    sortOptions: sortOptions,
    evidenceText: evidenceText,
    evidenceTitle: evidenceTitle,
    formatLocal: formatLocal,
    formatExact: formatExact,
    formatDuration: formatDuration,
    remainingText: remainingText,
    remainingForServer: remainingForServer,
    sourceLabel: sourceLabel,
    sourceForServer: sourceForServer,
    intervalText: intervalText,
    serverTooltip: serverTooltip,
    detailRows: detailRows,
    utcToLocalInput: utcToLocalInput,
    localInputToRFC3339: localInputToRFC3339,
    buildPayload: buildPayload,
    canSave: canSave,
    openActivity: openActivity,
    refreshActivity: refreshActivity,
    closeActivity: closeActivity,
    saveActivity: saveActivity
  };

  root.EIOAdminActivity = api;
})(typeof globalThis !== 'undefined' ? globalThis : this);

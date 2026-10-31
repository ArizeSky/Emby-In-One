'use strict';
/* public/admin-activity.js behaviour (K4): seven-state badges from the backend
 * state, remaining time from serverNow/dueAt (never the browser clock), the
 * derived filter/sort list view, and the per-node settings dialog with its
 * generation/abort guards and non-optimistic saves.
 *
 * The module is loaded in a VM with no Vue and no panel DOM, so every assertion
 * below is about its own logic. admin.html wiring is checked statically at the
 * end; the panel-level wiring lives in tests/admin-panel.test.cjs.
 */
const test = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const MODULE_PATH = path.join(__dirname, '..', 'public', 'admin-activity.js');
const HTML_PATH = path.join(__dirname, '..', 'public', 'admin.html');

function loadModule() {
  const context = vm.createContext({ console, AbortController, DOMException });
  vm.runInContext(fs.readFileSync(MODULE_PATH, 'utf8'), context, { filename: 'admin-activity.js' });
  return { mod: context.EIOAdminActivity, context };
}

function dto(overrides) {
  return Object.assign({
    serverNow: '2025-01-01T00:00:00Z',
    available: true,
    state: 'normal',
    enabled: true,
    intervalUnit: 'days',
    intervalValue: 30,
    minWatchSeconds: 60,
    revision: 7,
    lastActivityAt: '2024-12-30T10:00:00Z',
    lastQualifiedAt: '2024-12-30T09:00:00Z',
    manualReferenceAt: null,
    referenceAt: '2024-12-30T09:00:00Z',
    referenceSource: 'observed',
    dueAt: '2025-01-29T09:00:00Z',
    remainingRatio: 0.94
  }, overrides || {});
}

function abortError() {
  const error = new Error('aborted');
  error.name = 'AbortError';
  return error;
}

// An api stub that records every call and never settles unless the test does.
function pendingApi() {
  const calls = [];
  const api = (requestPath, opts = {}) => new Promise((resolve, reject) => {
    const call = { path: requestPath, opts, resolve, reject };
    calls.push(call);
    if (opts.signal) {
      if (opts.signal.aborted) reject(abortError());
      else opts.signal.addEventListener('abort', () => reject(abortError()));
    }
  });
  return { api, calls, puts: () => calls.filter(c => c.opts.method === 'PUT') };
}

// Sequential stub: each request consumes the next queued response (a value or a
// function returning a promise).
function sequenceApi(responses) {
  const calls = [];
  const api = (requestPath, opts = {}) => {
    calls.push({ path: requestPath, opts });
    if (responses.length === 0) return Promise.reject(new Error('unexpected request ' + requestPath));
    const next = responses.shift();
    return typeof next === 'function' ? next(requestPath, opts) : Promise.resolve(next);
  };
  return { api, calls, puts: () => calls.filter(c => c.opts.method === 'PUT') };
}

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

const flush = () => new Promise(resolve => setTimeout(resolve, 0));

// --- states ---------------------------------------------------------------

test('seven backend states map to Chinese labels, tones and risk flags', () => {
  const { mod } = loadModule();
  const expected = {
    normal: ['正常', 'green', false],
    warning: ['留意', 'yellow', true],
    urgent: ['紧急', 'orange', true],
    overdue: ['超期', 'red', true],
    disabled: ['未启用', 'gray', false],
    unknown: ['未知', 'gray', false],
    unavailable: ['不可用', 'gray', false]
  };
  for (const [state, [label, tone, risk]] of Object.entries(expected)) {
    const badge = mod.badgeFor(state);
    assert.strictEqual(badge.label, label, state);
    assert.strictEqual(badge.tone, tone, state);
    assert.strictEqual(badge.risk, risk, state);
    assert.ok(badge.className, state + ' has no CSS class');
  }
  assert.deepStrictEqual(Array.from(mod.STATE_KEYS).sort(), Object.keys(expected).sort());
});

test('the backend state wins; the ratio never re-derives it in the browser', () => {
  const { mod } = loadModule();
  const cases = [
    [{ state: 'overdue', remainingRatio: 0.9 }, '超期'],
    [{ state: 'urgent', remainingRatio: 0.99 }, '紧急'],
    [{ state: 'warning', remainingRatio: 0 }, '留意'],
    [{ state: 'normal', remainingRatio: -2 }, '正常'],
    [{ state: 'disabled', remainingRatio: 0 }, '未启用']
  ];
  for (const [overrides, label] of cases) {
    assert.strictEqual(mod.badgeFor(dto(overrides).state).label, label, JSON.stringify(overrides));
    const server = { id: 's1', activity: dto(overrides) };
    assert.strictEqual(mod.badgeFor(server).label, label, JSON.stringify(overrides));
  }
  // Unknown strings, missing activity and unavailable data all stay gray.
  assert.strictEqual(mod.badgeFor('bogus').label, '未知');
  assert.strictEqual(mod.badgeFor(dto({ state: 'bogus' }).state).label, '未知');
  assert.strictEqual(mod.badgeFor({ id: 's1' }).label, '未知');
  assert.strictEqual(mod.badgeFor({ id: 's1', activity: { available: false } }).label, '不可用');
  // available=false wins over any state, so unknown data can never render green.
  assert.strictEqual(mod.badgeFor({ id: 's1', activity: { available: false, state: 'normal' } }).label, '不可用');
  assert.strictEqual(mod.rowState({ activity: { available: false, state: 'normal' } }), 'unavailable');
  assert.strictEqual(mod.rowState({ activity: { state: 'normal' } }), 'unavailable', 'a payload without available=true is not trusted');
  assert.strictEqual(mod.remainingText(dto({ available: false, state: 'normal' })), '保活数据不可用');
});

// --- remaining time -------------------------------------------------------

test('remaining time comes from serverNow and dueAt, not the browser clock', () => {
  const { mod, context } = loadModule();
  // A wildly wrong browser clock must change nothing.
  vm.runInContext('Date.now = () => 0;', context);

  const upcoming = dto({ serverNow: '2025-01-01T00:00:00Z', dueAt: '2025-02-01T00:00:00Z', remainingRatio: 0.5 });
  assert.strictEqual(mod.remainingText(upcoming), '剩余 31 天（剩余 50%）');

  const overdue = dto({ state: 'overdue', serverNow: '2025-03-01T00:00:00Z', dueAt: '2025-02-01T00:00:00Z', remainingRatio: 0 });
  assert.strictEqual(mod.remainingText(overdue), '已超期 28 天（剩余 0%）');

  assert.strictEqual(mod.remainingText(dto({ state: 'disabled' })), '未启用提醒');
  assert.strictEqual(mod.remainingText(dto({ state: 'unknown' })), '暂无参考时间');
  assert.strictEqual(mod.remainingText(dto({ state: 'unavailable', available: false })), '保活数据不可用');
  assert.strictEqual(mod.remainingText(dto({ serverNow: null, dueAt: null })), '剩余时间未知');
  assert.strictEqual(mod.remainingText(null), '暂无保活状态');
});

test('null or invalid timestamps render as 暂无记录, never 1970', () => {
  const { mod } = loadModule();
  assert.strictEqual(mod.formatExact(null), '暂无记录');
  assert.strictEqual(mod.formatExact(''), '暂无记录');
  assert.strictEqual(mod.formatExact('not-a-date'), '暂无记录');
  assert.strictEqual(mod.evidenceText({ activity: dto({ lastQualifiedAt: null }) }, 'lastQualifiedAt'), '暂无记录');
  assert.strictEqual(mod.evidenceText({ activity: dto({ lastActivityAt: null }) }, 'lastActivityAt'), '暂无记录');
  assert.strictEqual(mod.evidenceText({}, 'lastActivityAt'), '暂无记录');
  // A real observation is rendered; its tooltip carries the exact UTC instant.
  assert.strictEqual(mod.evidenceText({ activity: dto() }, 'lastActivityAt'), mod.formatLocal('2024-12-30T10:00:00Z'));
  assert.match(mod.formatExact('2024-12-30T10:00:00Z'), /\/ 2024-12-30T10:00:00Z$/);
  assert.match(mod.formatExact('2024-12-30T10:00:00Z'), /UTC[+-]\d{2}:\d{2}/);
});

test('activity and qualified watching stay separate and name their evidence', () => {
  const { mod } = loadModule();
  const server = { id: 's1', activity: dto({ referenceSource: 'manual', manualReferenceAt: '2024-12-31T00:00:00Z', referenceAt: '2024-12-31T00:00:00Z' }) };
  const tooltip = mod.serverTooltip(server);
  assert.match(tooltip, /最近活动：.*2024-12-30T10:00:00Z/);
  assert.match(tooltip, /有效观看：.*2024-12-30T09:00:00Z/);
  assert.match(tooltip, /人工校准/);
  assert.match(tooltip, /UTC 自然月|每 30 天/);
  assert.match(tooltip, /不代表上游账号保活成功/);

  const rows = mod.detailRows(dto());
  const byLabel = Object.fromEntries(rows.map(row => [row.label, row.value]));
  assert.ok(byLabel['最近活动'].includes('2024-12-30T10:00:00Z'));
  assert.ok(byLabel['有效观看'].includes('2024-12-30T09:00:00Z'));
  assert.match(byLabel['有效观看'], /不是保活成功证明|2024-12-30T09:00:00Z/);
  assert.strictEqual(mod.sourceLabel(dto({ referenceSource: 'manual' })), '人工校准');
  assert.strictEqual(mod.sourceLabel(dto({ referenceSource: 'observed' })), '自动观测');
  assert.strictEqual(mod.sourceLabel(dto({ referenceSource: '' })), '');
  assert.match(mod.intervalText('months', 3), /UTC 自然月/);
  assert.match(mod.intervalText('days', 30), /每 30 天/);
});

// --- derived list ---------------------------------------------------------

function listFixture() {
  return [
    { id: 'a', name: 'A', activity: dto({ state: 'normal' }) },
    { id: 'b', name: 'B', activity: dto({ state: 'overdue', dueAt: '2024-12-01T00:00:00Z' }) },
    { id: 'c', name: 'C', activity: dto({ state: 'warning' }) },
    { id: 'd', name: 'D', activity: dto({ state: 'unknown' }) },
    { id: 'e', name: 'E', activity: dto({ state: 'disabled' }) },
    { id: 'f', name: 'F' }
  ];
}

test('filtering and risk sorting build a derived list without touching upstreamList', () => {
  const { mod } = loadModule();
  const list = listFixture();
  const snapshot = JSON.parse(JSON.stringify(list.map(row => row.id)));

  const riskSorted = mod.deriveList(list, { filter: 'all', sort: 'risk' });
  // deriveList runs inside the VM realm, so its arrays do not share this
  // realm's Array.prototype. Array.from re-homes them before the strict compare.
  assert.deepStrictEqual(Array.from(riskSorted.map(row => row.id)), ['b', 'c', 'a', 'd', 'f', 'e']);
  assert.deepStrictEqual(Array.from(riskSorted.map(row => row.index)), [1, 2, 0, 3, 5, 4]);

  const attention = mod.deriveList(list, { filter: 'attention', sort: 'default' });
  assert.deepStrictEqual(Array.from(attention.map(row => row.id)), ['b', 'c']);
  assert.deepStrictEqual(Array.from(attention.map(row => row.index)), [1, 2]);

  assert.deepStrictEqual(Array.from(mod.deriveList(list, { filter: 'unknown', sort: 'default' }).map(row => row.id)), ['d', 'f']);
  assert.deepStrictEqual(Array.from(mod.deriveList(list, { filter: 'disabled', sort: 'default' }).map(row => row.id)), ['e']);
  assert.deepStrictEqual(Array.from(mod.deriveList(list, { filter: 'all', sort: 'default' }).map(row => row.index)), [0, 1, 2, 3, 4, 5]);

  // Display-only: the source order and the row objects are untouched.
  assert.deepStrictEqual(list.map(row => row.id), snapshot);
  assert.strictEqual(riskSorted[0].server, list[1]);

  assert.strictEqual(mod.reorderAllowed({ filter: 'all', sort: 'default' }), true);
  assert.strictEqual(mod.reorderAllowed({ filter: 'attention', sort: 'default' }), false);
  assert.strictEqual(mod.reorderAllowed({ filter: 'all', sort: 'risk' }), false);
  assert.strictEqual(mod.reorderAllowed({ filter: 'normal', sort: 'risk' }), false);
  // No upstreamList input, no mutation, no reorder call path exists in the module.
  assert.deepStrictEqual(Array.from(mod.deriveList(null, {})), []);
});

test('risk sort orders equal tiers by remaining time, then original index', () => {
  const { mod } = loadModule();
  const now = '2025-01-01T00:00:00Z';
  const list = [
    { id: 'normal-later', activity: dto({ state: 'normal', serverNow: now, dueAt: '2025-02-01T00:00:00Z', remainingRatio: 0.9 }) },
    { id: 'normal-sooner', activity: dto({ state: 'normal', serverNow: now, dueAt: '2025-01-10T00:00:00Z', remainingRatio: 0.2 }) },
    { id: 'normal-mid-a', activity: dto({ state: 'normal', serverNow: now, dueAt: '2025-01-20T00:00:00Z', remainingRatio: 0.5 }) },
    { id: 'normal-mid-b', activity: dto({ state: 'normal', serverNow: now, dueAt: '2025-01-20T00:00:00Z', remainingRatio: 0.5 }) },
    { id: 'disabled-a', activity: dto({ state: 'disabled', serverNow: now, dueAt: '2024-01-01T00:00:00Z', remainingRatio: 0 }) },
    { id: 'disabled-b', activity: dto({ state: 'disabled', serverNow: now, dueAt: '2026-01-01T00:00:00Z', remainingRatio: 1 }) }
  ];
  const sorted = mod.deriveList(list, { filter: 'all', sort: 'risk' });
  // Same normal tier: earliest due first; identical deadlines keep index order.
  assert.deepStrictEqual(Array.from(sorted.map(row => row.id)), [
    'normal-sooner', 'normal-mid-a', 'normal-mid-b', 'normal-later', 'disabled-a', 'disabled-b'
  ]);
  // disabled has no remaining time, so it is not reordered by its ratio/due date.
  assert.deepStrictEqual(Array.from(sorted.slice(4).map(row => row.index)), [4, 5]);
  // Same ordering signal fallback: ratio when the timestamps are missing.
  // A production DTO always carries available=true; without it the row would be
  // unavailable and would never take part in a ratio-tier comparison.
  const ratioOnly = [
    { id: 'r-high', activity: { available: true, state: 'warning', remainingRatio: 0.4 } },
    { id: 'r-low', activity: { available: true, state: 'warning', remainingRatio: 0.1 } }
  ];
  assert.deepStrictEqual(Array.from(mod.deriveList(ratioOnly, { filter: 'all', sort: 'risk' }).map(row => row.id)), ['r-low', 'r-high']);
});

// --- manual reference date ------------------------------------------------

test('manual date input converts local time to UTC RFC3339 and round-trips', () => {
  const { mod } = loadModule();
  const local = new Date(2025, 0, 15, 8, 5, 0);
  const input = '2025-01-15T08:05:00';
  const expected = local.toISOString().replace(/\.\d{3}Z$/, 'Z');
  assert.strictEqual(mod.localInputToRFC3339(input), expected);
  assert.strictEqual(mod.utcToLocalInput(expected), input);
  // An empty field is null (no manual reference), never "now".
  assert.strictEqual(mod.localInputToRFC3339(''), null);
  assert.strictEqual(mod.localInputToRFC3339(null), null);
  assert.strictEqual(mod.localInputToRFC3339('   '), null);
});

test('invalid and non-existent local dates are rejected', () => {
  const { mod } = loadModule();
  for (const value of ['2025-02-30T10:00:00', '2025-13-01T00:00', '2025-00-10T00:00', 'not-a-date', '2025-01-01 10:00']) {
    assert.throws(() => mod.localInputToRFC3339(value), /人工参考日期/, value);
  }
});

// --- dialog: loading and saving ------------------------------------------

test('a null manual reference stays empty and is saved as null with the server revision', async () => {
  const { mod } = loadModule();
  const state = mod.newDialogState();
  const seq = sequenceApi([
    dto({ manualReferenceAt: null, revision: 7 }),
    dto({ revision: 8 })
  ]);
  await mod.openActivity(state, { serverId: 'srv-1', serverName: 'A', api: seq.api });
  assert.strictEqual(state.loaded, true);
  assert.strictEqual(state.form.manualReferenceLocal, '', 'a null manual reference must not be filled with now');
  assert.strictEqual(state.form.expectedRevision, 7);
  assert.strictEqual(mod.canSave(state), true);

  state.form.manualReferenceLocal = '';
  assert.strictEqual(await mod.saveActivity(state, { api: seq.api }), true);
  const put = seq.puts()[0];
  assert.strictEqual(put.path, '/admin/api/upstream/srv-1/activity');
  const body = JSON.parse(put.opts.body);
  assert.deepStrictEqual(Object.keys(body).sort(), [
    'enabled', 'expectedRevision', 'intervalUnit', 'intervalValue', 'manualReferenceAt', 'minWatchSeconds'
  ]);
  assert.strictEqual(body.manualReferenceAt, null);
  assert.strictEqual(body.expectedRevision, 7);
  assert.strictEqual(body.intervalUnit, 'days');
  assert.strictEqual(body.intervalValue, 30);
  assert.strictEqual(body.minWatchSeconds, 60);
  assert.strictEqual(state.form.expectedRevision, 8);
  assert.strictEqual(state.savedNotice, '保活设置已保存');
});

test('a manual reference is sent as UTC RFC3339', async () => {
  const { mod } = loadModule();
  const state = mod.newDialogState();
  const manualLocal = '2024-12-20T10:30:00';
  const seq = sequenceApi([dto(), dto({ revision: 8, manualReferenceAt: mod.localInputToRFC3339(manualLocal) })]);
  await mod.openActivity(state, { serverId: 'srv-1', api: seq.api });
  state.form.manualReferenceLocal = manualLocal;
  state.form.intervalUnit = 'months';
  state.form.intervalValue = 3;
  state.form.minWatchSeconds = 120;
  assert.strictEqual(await mod.saveActivity(state, { api: seq.api }), true);
  const body = JSON.parse(seq.puts()[0].opts.body);
  assert.strictEqual(body.manualReferenceAt, mod.localInputToRFC3339(manualLocal));
  assert.match(body.manualReferenceAt, /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/);
  assert.strictEqual(body.intervalUnit, 'months');
  assert.strictEqual(body.intervalValue, 3);
  assert.strictEqual(body.minWatchSeconds, 120);
});

test('a future manual reference is refused against serverNow before any request', async () => {
  const { mod } = loadModule();
  const state = mod.newDialogState();
  const seq = sequenceApi([dto({ serverNow: '2025-01-01T00:00:00Z' })]);
  await mod.openActivity(state, { serverId: 'srv-1', api: seq.api });
  state.form.manualReferenceLocal = '2030-01-01T00:00:00';
  assert.strictEqual(await mod.saveActivity(state, { api: seq.api }), false);
  assert.strictEqual(seq.puts().length, 0, 'no PUT was issued for a future manual reference');
  assert.match(state.saveError, /不能晚于服务器当前时间/);
  assert.strictEqual(state.saving, false);
});

test('interval and minimum-watch ranges are enforced locally', async () => {
  const { mod } = loadModule();
  for (const [form, message] of [
    [{ intervalUnit: 'days', intervalValue: 0 }, /1 到 3650/],
    [{ intervalUnit: 'days', intervalValue: 3651 }, /1 到 3650/],
    [{ intervalUnit: 'months', intervalValue: 121 }, /1 到 120/],
    [{ intervalUnit: 'weeks', intervalValue: 4 }, /days 或 months/],
    [{ intervalValue: 12.5 }, /必须是整数/],
    [{ minWatchSeconds: 9 }, /10 到 3600/],
    [{ minWatchSeconds: 3601 }, /10 到 3600/]
  ]) {
    const state = mod.newDialogState();
    const seq = sequenceApi([dto()]);
    await mod.openActivity(state, { serverId: 'srv-1', api: seq.api });
    Object.assign(state.form, form);
    assert.strictEqual(await mod.saveActivity(state, { api: seq.api }), false, JSON.stringify(form));
    assert.strictEqual(seq.puts().length, 0, JSON.stringify(form));
    assert.match(state.saveError, message, JSON.stringify(form));
  }
});

test('a successful save shows only the server response, not the submitted form', async () => {
  const { mod } = loadModule();
  const state = mod.newDialogState();
  const pending = deferred();
  const seq = sequenceApi([dto({ state: 'normal', revision: 7 }), () => pending.promise]);
  await mod.openActivity(state, { serverId: 'srv-1', api: seq.api });
  state.form.enabled = false;
  state.form.intervalValue = 10;
  const saving = mod.saveActivity(state, { api: seq.api });
  assert.strictEqual(state.saving, true);
  assert.strictEqual(state.dto.state, 'normal', 'no optimistic state change while the PUT is in flight');
  assert.strictEqual(mod.canSave(state), false, 'saving blocks a duplicate submit');
  pending.resolve(dto({ state: 'warning', enabled: false, revision: 8, intervalValue: 10 }));
  assert.strictEqual(await saving, true);
  assert.strictEqual(state.dto.state, 'warning');
  assert.strictEqual(state.form.expectedRevision, 8);
  assert.strictEqual(state.form.intervalValue, 10);
  assert.strictEqual(state.saving, false);
});

test('409 keeps the form visible, requires a refresh, and never resends the stale revision', async () => {
  const { mod } = loadModule();
  const state = mod.newDialogState();
  const conflict = Object.assign(new Error('保活设置已在别处更新，请刷新后重试'), { status: 409 });
  const seq = sequenceApi([dto({ revision: 7 }), () => Promise.reject(conflict), dto({ revision: 9 }), dto({ revision: 10 })]);
  await mod.openActivity(state, { serverId: 'srv-1', api: seq.api });
  state.form.intervalValue = 45;
  assert.strictEqual(await mod.saveActivity(state, { api: seq.api }), false);
  assert.strictEqual(state.conflict, true);
  assert.strictEqual(state.form.intervalValue, 45, 'the user input survived the conflict');
  assert.match(state.saveError, /刷新/);
  assert.strictEqual(mod.canSave(state), false);

  assert.strictEqual(await mod.saveActivity(state, { api: seq.api }), false);
  assert.strictEqual(seq.puts().length, 1, 'a stale revision must not be resent after a 409');

  await mod.refreshActivity(state, { api: seq.api });
  assert.strictEqual(state.conflict, false);
  assert.strictEqual(state.form.expectedRevision, 9);
  assert.strictEqual(await mod.saveActivity(state, { api: seq.api }), true);
  assert.strictEqual(JSON.parse(seq.puts()[1].opts.body).expectedRevision, 9);
});

test('400 and generic save failures stay visible without changing the loaded snapshot', async () => {
  const { mod } = loadModule();
  for (const status of [400, 503]) {
    const state = mod.newDialogState();
    const failure = Object.assign(new Error(status === 400 ? '保活设置参数无效' : 'activity store write failed'), { status });
    const seq = sequenceApi([dto({ revision: 7 }), () => Promise.reject(failure), dto({ revision: 8 })]);
    await mod.openActivity(state, { serverId: 'srv-1', api: seq.api });
    state.form.minWatchSeconds = 90;
    assert.strictEqual(await mod.saveActivity(state, { api: seq.api }), false, String(status));
    assert.strictEqual(state.saveError, failure.message);
    assert.strictEqual(state.savedNotice, '');
    assert.strictEqual(state.dto.revision, 7, 'a failed save must not look applied');
    assert.strictEqual(state.conflict, false);
    assert.strictEqual(state.form.minWatchSeconds, 90);
    assert.strictEqual(mod.canSave(state), true, 'a non-conflict failure can be retried');
    assert.strictEqual(await mod.saveActivity(state, { api: seq.api }), true);
    assert.strictEqual(JSON.parse(seq.puts()[1].opts.body).expectedRevision, 7);
  }
});

test('unavailable GET data can be refreshed but never saved with a default revision', async () => {
  const { mod } = loadModule();
  const state = mod.newDialogState();
  const seq = sequenceApi([
    dto({ available: false, state: 'unavailable', revision: 0, message: 'activity store unavailable' }),
    dto({ available: true, state: 'normal', revision: 5 }),
    // The refreshed snapshot is saved against revision 5, so the PUT answers
    // with the next real revision instead of the fixture running dry.
    dto({ available: true, state: 'normal', revision: 6 })
  ]);
  await mod.openActivity(state, { serverId: 'srv-1', api: seq.api });
  assert.strictEqual(state.error, '', 'unavailable is a data state, not a request failure');
  assert.strictEqual(state.loaded, true);
  assert.strictEqual(state.form.expectedRevision, null, 'revision 0 is not a usable revision');
  assert.match(state.notice, /activity store unavailable/);
  assert.strictEqual(mod.canSave(state), false);
  assert.strictEqual(await mod.saveActivity(state, { api: seq.api }), false);
  assert.strictEqual(seq.puts().length, 0, 'no PUT with revision 0 was issued');
  assert.match(state.saveError, /刷新/);

  await mod.refreshActivity(state, { api: seq.api });
  assert.strictEqual(state.notice, '');
  assert.strictEqual(state.form.expectedRevision, 5);
  assert.strictEqual(await mod.saveActivity(state, { api: seq.api }), true);
  assert.strictEqual(JSON.parse(seq.puts()[0].opts.body).expectedRevision, 5);
  assert.strictEqual(state.form.expectedRevision, 6, 'the saved response advances the revision');
});

test('a DTO that lacks available=true is never saved with a guessed revision', async () => {
  const { mod } = loadModule();
  const state = mod.newDialogState();
  const malformed = dto({ revision: 7 });
  delete malformed.available;
  const seq = sequenceApi([malformed]);
  await mod.openActivity(state, { serverId: 'srv-1', api: seq.api });
  assert.strictEqual(state.loaded, true);
  assert.strictEqual(mod.rowState({ activity: malformed }), 'unavailable', 'missing available=true must not render green');
  assert.strictEqual(mod.canSave(state), false);
  assert.match(state.notice, /不可用/);
  assert.strictEqual(await mod.saveActivity(state, { api: seq.api }), false);
  assert.strictEqual(seq.puts().length, 0, 'no PUT was issued for an unavailable/malformed snapshot');
  assert.match(state.saveError, /刷新/);
});

test('refreshing while a save is in flight is refused instead of sticking saving', async () => {
  const { mod } = loadModule();
  const state = mod.newDialogState();
  const put = deferred();
  const seq = sequenceApi([dto({ revision: 7 }), () => put.promise, dto({ revision: 9 })]);
  await mod.openActivity(state, { serverId: 'srv-1', api: seq.api });
  const saving = mod.saveActivity(state, { api: seq.api });
  assert.strictEqual(await mod.refreshActivity(state, { api: seq.api }), false);
  assert.strictEqual(seq.calls.length, 2, 'no GET was issued while the PUT was in flight');
  assert.strictEqual(state.saving, true);
  assert.strictEqual(state.loading, false);
  put.resolve(dto({ revision: 8 }));
  assert.strictEqual(await saving, true);
  assert.strictEqual(state.saving, false, 'the save must clear its own pending flag');
  assert.strictEqual(state.form.expectedRevision, 8);
  await mod.refreshActivity(state, { api: seq.api });
  assert.strictEqual(state.form.expectedRevision, 9);
});

test('a GET failure is visible and offers a retry instead of a default form', async () => {
  const { mod } = loadModule();
  const state = mod.newDialogState();
  const failure = Object.assign(new Error('HTTP 500'), { status: 500 });
  const seq = sequenceApi([() => Promise.reject(failure), dto()]);
  await mod.openActivity(state, { serverId: 'srv-1', api: seq.api });
  assert.strictEqual(state.loading, false);
  assert.strictEqual(state.error, 'HTTP 500');
  assert.strictEqual(state.form, null);
  assert.strictEqual(mod.canSave(state), false);
  await mod.refreshActivity(state, { api: seq.api });
  assert.strictEqual(state.error, '');
  assert.strictEqual(state.loaded, true);
  assert.strictEqual(state.form.expectedRevision, 7);
});

test('invalid GET payloads are reported, not treated as data', async () => {
  const { mod } = loadModule();
  const state = mod.newDialogState();
  const seq = sequenceApi([null]);
  await mod.openActivity(state, { serverId: 'srv-1', api: seq.api });
  assert.strictEqual(state.loaded, false);
  assert.match(state.error, /响应格式无效/);
  assert.strictEqual(mod.canSave(state), false);
});

// --- dialog: races -------------------------------------------------------

test('switching to another node ignores the previous GET response', async () => {
  const { mod } = loadModule();
  const state = mod.newDialogState();
  const pendingFirst = pendingApi();
  const secondDTO = dto({ revision: 22, state: 'warning' });
  const first = mod.openActivity(state, { serverId: 'srv-a', serverName: 'A', api: pendingFirst.api });
  // The GET is issued from a microtask; flush so it is genuinely in flight when
  // the dialog switches nodes, then its late response must still be ignored.
  await flush();
  const second = mod.openActivity(state, {
    serverId: 'srv-b', serverName: 'B',
    api: () => Promise.resolve(secondDTO)
  });
  await second;
  assert.strictEqual(state.serverId, 'srv-b');
  assert.strictEqual(state.dto.revision, 22);
  pendingFirst.calls[0].resolve(dto({ revision: 1, state: 'overdue' }));
  await first;
  await flush();
  assert.strictEqual(state.serverId, 'srv-b');
  assert.strictEqual(state.dto.revision, 22, 'the stale GET overwrote the new node');
  assert.strictEqual(state.form.expectedRevision, 22);
  assert.strictEqual(state.loading, false);
});

test('closing aborts the in-flight GET and ignores its late response', async () => {
  const { mod } = loadModule();
  const state = mod.newDialogState();
  const pending = pendingApi();
  const opening = mod.openActivity(state, { serverId: 'srv-a', api: pending.api });
  // The request is issued from a promise microtask, so flush once before reading
  // the signal the module attached to it.
  await flush();
  assert.ok(pending.calls[0].opts.signal, 'the GET carried an AbortController signal');
  mod.closeActivity(state);
  assert.strictEqual(pending.calls[0].opts.signal.aborted, true, 'closing did not abort the GET');
  pending.calls[0].resolve(dto());
  await opening;
  await flush();
  assert.strictEqual(state.open, false);
  assert.strictEqual(state.dto, null);
  assert.strictEqual(state.form, null);
  assert.strictEqual(state.loading, false);

  // Closing before that microtask must not issue an already-aborted request.
  const earlyState = mod.newDialogState();
  const early = pendingApi();
  const earlyOpening = mod.openActivity(earlyState, { serverId: 'srv-a', api: early.api });
  mod.closeActivity(earlyState);
  assert.strictEqual(await earlyOpening, null);
  await flush();
  assert.strictEqual(early.calls.length, 0, 'a closed dialog still issued its stale GET');
});

test('an old PUT response cannot overwrite a dialog opened on another node', async () => {
  const { mod } = loadModule();
  const state = mod.newDialogState();
  const put = deferred();
  const seq = sequenceApi([dto({ revision: 7 }), () => put.promise]);
  await mod.openActivity(state, { serverId: 'srv-a', api: seq.api });
  state.form.intervalValue = 90;
  const saving = mod.saveActivity(state, { api: seq.api });
  // The PUT is issued from a microtask; flush so it is in flight before the
  // dialog switches nodes, which is what this test is about.
  await flush();
  assert.strictEqual(state.saving, true);

  const second = mod.openActivity(state, {
    serverId: 'srv-b', serverName: 'B',
    api: () => Promise.resolve(dto({ revision: 30, state: 'urgent' }))
  });
  await second;
  put.resolve(dto({ revision: 8, state: 'normal' }));
  assert.strictEqual(await saving, false);
  await flush();
  assert.strictEqual(state.serverId, 'srv-b');
  assert.strictEqual(state.dto.revision, 30);
  assert.strictEqual(state.dto.state, 'urgent');
  assert.strictEqual(state.form.expectedRevision, 30);
  assert.strictEqual(state.savedNotice, '');
});

test('closing while a PUT is in flight cancels it and records no success', async () => {
  const { mod } = loadModule();
  const state = mod.newDialogState();
  const pending = pendingApi();
  const load = mod.openActivity(state, { serverId: 'srv-a', api: pending.api });
  await flush();
  pending.calls[0].resolve(dto({ revision: 7 }));
  await load;
  const saving = mod.saveActivity(state, { api: pending.api });
  await flush();
  const putCall = pending.calls[1];
  assert.ok(putCall && putCall.opts.signal, 'the PUT carried an AbortController signal');
  mod.closeActivity(state);
  assert.strictEqual(putCall.opts.signal.aborted, true, 'closing did not abort the PUT');
  assert.strictEqual(await saving, false);
  assert.strictEqual(state.savedNotice, '');
  assert.strictEqual(state.saving, false);

  // Closing before the PUT microtask cancels the save without any request.
  const earlyState = mod.newDialogState();
  const early = pendingApi();
  const earlyLoad = mod.openActivity(earlyState, { serverId: 'srv-a', api: early.api });
  await flush();
  early.calls[0].resolve(dto({ revision: 7 }));
  await earlyLoad;
  const earlySaving = mod.saveActivity(earlyState, { api: early.api });
  mod.closeActivity(earlyState);
  assert.strictEqual(await earlySaving, false);
  await flush();
  assert.strictEqual(early.puts().length, 0, 'a closed dialog still issued its stale PUT');
});

// --- privacy --------------------------------------------------------------

test('UI text carries no account scope, key, hash or per-user entries', async () => {
  const { mod } = loadModule();
  const state = mod.newDialogState();
  const secret = 'SECRET_SCOPE_DIGEST_abc123';
  const seq = sequenceApi([
    dto({ accountScope: secret, keyHash: secret, users: [{ username: secret }], revision: 7 }),
    dto({ revision: 8 })
  ]);
  await mod.openActivity(state, { serverId: 'srv-1', api: seq.api });
  const surfaces = [
    mod.serverTooltip({ activity: state.dto }),
    JSON.stringify(mod.detailRows(state.dto)),
    JSON.stringify(mod.badgeFor(state.dto.state)),
    mod.remainingText(state.dto)
  ];
  for (const text of surfaces) {
    assert.ok(!text.includes(secret), 'a DTO extra leaked into the UI: ' + text);
  }
  assert.strictEqual(await mod.saveActivity(state, { api: seq.api }), true);
  const body = JSON.parse(seq.puts()[0].opts.body);
  assert.ok(!JSON.stringify(body).includes(secret));
});

// --- admin.html wiring (static) ------------------------------------------

test('admin.html loads the module first and keeps reorder on original indexes', () => {
  const html = fs.readFileSync(HTML_PATH, 'utf8');
  const activityAt = html.indexOf('<script src="admin-activity.js"></script>');
  const appAt = html.indexOf('<script src="admin.js"></script>');
  assert.ok(activityAt >= 0, 'admin.html does not load admin-activity.js');
  assert.ok(appAt >= 0, 'admin.html does not load admin.js');
  assert.ok(activityAt < appAt, 'admin-activity.js must load before admin.js');

  assert.match(html, /v-for="row in activityRows"/);
  assert.doesNotMatch(html, /v-for="\(s, idx\) in upstreamList"/, 'the servers table must render the derived view');
  assert.match(html, /reorder\(row\.index, row\.index-1\)/);
  assert.match(html, /reorder\(row\.index, row\.index\+1\)/);
  assert.match(html, /:disabled="!activityReorderEnabled \|\| row\.index===0"/);
  assert.match(html, /:key="row\.server\.id \|\| row\.server\.index"/);
  assert.match(html, /openActivity\(row\.server\)/);
  assert.match(html, /编辑与保存仍按原服务器顺序/);
});

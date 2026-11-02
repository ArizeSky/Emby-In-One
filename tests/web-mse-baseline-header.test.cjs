'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, '../public/vendor/web/libmedia/26.avplayer.js'), 'utf8');
const start = source.indexOf('eioHeaderOperation(t){');
const end = source.indexOf('/* startMux runs', start);
assert.ok(start >= 0 && end > start, 'actual shipped header operation');
const method = source.slice(start, end).trim();
function fixture() {
  const calls = [];
  const api = vm.runInNewContext('({' + method + '})', {
    Q: {
      ho: context => calls.push(['init', context]),
      bx: context => calls.push(['header', context])
    },
    eioMseError: code => Object.assign(new Error(code), { code })
  });
  api.eioPrepareOperation = () => { calls.push(['prepare']); return {}; };
  api.eioOperationSeal = () => calls.push(['seal']);
  api.eioOperationAbort = () => calls.push(['abort']);
  return { api, calls };
}
test('non-opt-in header preserves original init then writeHeader without requiring a budget scope', () => {
  const { api, calls } = fixture();
  const context = {};
  api.eioHeaderOperation({ oformatContext: context });
  assert.deepEqual(calls, [['init', context], ['header', context]]);
});
test('strict header retains prepare -> init -> writeHeader -> seal ordering', () => {
  const { api, calls } = fixture();
  const context = {};
  api.eioHeaderOperation({ eioOwned: {}, oformatContext: context });
  assert.deepEqual(calls, [['prepare'], ['init', context], ['header', context], ['seal']]);
});
test('strict refusal never falls through to unbudgeted mux init', () => {
  const { api, calls } = fixture();
  api.eioPrepareOperation = () => null;
  assert.throws(() => api.eioHeaderOperation({ eioOwned: {}, oformatContext: {} }), { code: 'EIO_PROBE_BUDGET_EXHAUSTED' });
  assert.deepEqual(calls, []);
});
test('raw MPEG and absent streams do not initialize a mux header', () => {
  const { api, calls } = fixture();
  api.eioHeaderOperation(null);
  api.eioHeaderOperation({ enableRawMpeg: true });
  assert.deepEqual(calls, []);
});

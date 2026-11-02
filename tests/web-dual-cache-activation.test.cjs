const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

// These cases execute the *patched vendored* AVPlayer pieces:
//  - the real `eioBindDualCache` method (RPC acknowledgement/error contract), and
//  - the real selected-MSE / non-MSE statement block extracted from `play`
//    (explicit activation before `playUseMSE`, fail-closed on bind failure).
//
// Scope boundary: this file does not run a whole player lifecycle, does not
// start MSE/decoder pipelines and does not prove playback or ownership. The
// play extraction is structurally delimited to the statement block that starts
// at `this.useMSE=await this.checkUseMSE(e)` and ends at the non-MSE
// `playUseDecoder` call. Runtime chrome confirmation remains a separate step.
const BUNDLE = path.join(__dirname, '../public/vendor/web/libmedia/avplayer.js');

function bundleText() {
  return fs.readFileSync(BUNDLE, 'utf8');
}

function sliceMethod(text, head) {
  const start = text.indexOf(head);
  assert.ok(start > -1, `vendored method ${head} is missing from the bundle`);
  let depth = 0;
  let seen = false;
  for (let i = start; i < text.length; i += 1) {
    const ch = text[i];
    if (ch === '{') {
      depth += 1;
      seen = true;
    } else if (ch === '}') {
      depth -= 1;
      if (seen && depth === 0) return text.slice(start, i + 1);
    }
  }
  throw new Error(`unbalanced vendored method ${head}`);
}

// ---------------------------------------------------------------- bind contract
function bindHarness(demuxReply, ioReply) {
  const text = bundleText();
  const source = sliceMethod(text, 'async eioBindDualCache(e){');
  const calls = { demux: [], io: [] };
  const Ue = {
    DemuxerThread: {
      async configureEioDualCache(taskId, config) {
        calls.demux.push({ taskId, config });
        return demuxReply(config, calls);
      }
    },
    IOThread: {
      async configureEioDualCache(taskId, config) {
        calls.io.push({ taskId, config });
        return ioReply(config, calls);
      }
    }
  };
  const api = vm.runInNewContext(`(function(){ return { ${source} }; })()`, { Ue });
  api.taskId = 77;
  return { api, calls, source };
}

const NUMERIC_CONFIG = {
  enabled: true,
  maxBytes: 100e6,
  maxForwardSeconds: 120,
  ioBytes: 12e6,
  demuxBytes: 48e6,
  mseBytes: 40e6,
  maxPacketBytes: 16e6
};

test('selected-MSE bind gives Demux the original numeric config and IO a distinct active clone', async () => {
  const { api, calls, source } = bindHarness(() => 1, () => 1);
  assert.ok(source.includes('configureEioDualCache(this.taskId,{...e,active:!0})'), 'the IO call must request activation explicitly');
  assert.ok(source.indexOf('DemuxerThread.configureEioDualCache') < source.indexOf('IOThread.configureEioDualCache'), 'Demux must still be configured before IO');

  const config = { ...NUMERIC_CONFIG };
  const result = await api.eioBindDualCache(config);

  assert.equal(result, true);
  assert.equal(api.eioDualCacheConfigError, null);
  assert.equal(calls.demux.length, 1);
  assert.equal(calls.io.length, 1);
  assert.equal(calls.demux[0].taskId, 77);
  assert.equal(calls.io[0].taskId, 77);
  assert.equal(calls.demux[0].config, config, 'Demux receives the original caller config');
  assert.notEqual(calls.io[0].config, config, 'IO receives a clone, never the caller object');
  assert.equal(calls.io[0].config.active, true);
  assert.deepEqual({ ...calls.io[0].config }, { ...NUMERIC_CONFIG, active: true });
  assert.deepEqual(config, NUMERIC_CONFIG, 'the caller numeric config is not mutated');
  assert.equal(Object.prototype.hasOwnProperty.call(config, 'active'), false, 'the caller config never gains an active field');
});

test('a caller config that already says active:false is still activated only in the IO clone', async () => {
  const { api, calls } = bindHarness(() => 1, () => 1);
  const config = { ...NUMERIC_CONFIG, active: false };
  assert.equal(await api.eioBindDualCache(config), true);
  assert.equal(calls.demux[0].config, config);
  assert.equal(calls.demux[0].config.active, false);
  assert.equal(calls.io[0].config.active, true);
  assert.equal(config.active, false);
});

test('a Demux acknowledgement other than the exact integer 1 stops before IO and fails closed', async () => {
  for (const ack of [0, 2, '1', true, undefined, null]) {
    const { api, calls } = bindHarness(() => ack, () => 1);
    const result = await api.eioBindDualCache({ ...NUMERIC_CONFIG });
    assert.equal(result, false, `Demux ack ${String(ack)} must not be treated as success`);
    assert.equal(calls.io.length, 0, `Demux ack ${String(ack)} must never configure IO`);
    assert.deepEqual({ ...api.eioDualCacheConfigError }, {
      code: 'EIO_DUAL_RPC_NOT_APPLIED',
      demux: false,
      io: false
    });
    assert.strictEqual(api.eioDualCacheConfigError.demux, false, 'attribution is a boolean, not the raw ack');
    assert.strictEqual(api.eioDualCacheConfigError.io, false);
  }
});

test('an IO acknowledgement other than the exact integer 1 reports the Demux success and fails closed', async () => {
  for (const ack of [0, 2, '1', true, undefined, null]) {
    const { api, calls } = bindHarness(() => 1, () => ack);
    const result = await api.eioBindDualCache({ ...NUMERIC_CONFIG });
    assert.equal(result, false, `IO ack ${String(ack)} must not be treated as success`);
    assert.equal(calls.io.length, 1, 'IO is configured exactly once, without retries');
    assert.deepEqual({ ...api.eioDualCacheConfigError }, {
      code: 'EIO_DUAL_RPC_NOT_APPLIED',
      demux: true,
      io: false
    });
    assert.strictEqual(api.eioDualCacheConfigError.demux, true);
    assert.strictEqual(api.eioDualCacheConfigError.io, false);
  }
});

test('RPC exceptions are attributed without retry, fake success or leaked error detail', async () => {
  let demuxCalls = 0;
  const demuxThrow = bindHarness(() => {
    demuxCalls += 1;
    throw new Error('demux fixture failure');
  }, () => 1);
  const demuxResult = await demuxThrow.api.eioBindDualCache({ ...NUMERIC_CONFIG });
  assert.equal(demuxResult, false);
  assert.equal(demuxCalls, 1, 'a thrown Demux RPC is not retried');
  assert.equal(demuxThrow.calls.io.length, 0);
  assert.deepEqual({ ...demuxThrow.api.eioDualCacheConfigError }, {
    code: 'EIO_DUAL_RPC_FAILED',
    demux: false,
    io: false
  });
  assert.deepEqual(Object.keys(demuxThrow.api.eioDualCacheConfigError).sort(), ['code', 'demux', 'io']);

  let ioCalls = 0;
  const ioThrow = bindHarness(() => 1, () => {
    ioCalls += 1;
    throw new Error('io fixture failure');
  });
  const config = { ...NUMERIC_CONFIG };
  const ioResult = await ioThrow.api.eioBindDualCache(config);
  assert.equal(ioResult, false);
  assert.equal(ioCalls, 1, 'a thrown IO RPC is not retried');
  assert.deepEqual({ ...ioThrow.api.eioDualCacheConfigError }, {
    code: 'EIO_DUAL_RPC_FAILED',
    demux: true,
    io: false
  });
  assert.deepEqual(Object.keys(ioThrow.api.eioDualCacheConfigError).sort(), ['code', 'demux', 'io']);
  assert.deepEqual(config, NUMERIC_CONFIG, 'a failed IO RPC does not mutate the config');
});

// ---------------------------------------------------------------- play block
function playBranchSource() {
  const text = bundleText();
  const head = 'this.useMSE=await this.checkUseMSE(e),this.audioEnded=!0,this.videoEnded=!0;';
  const tail = 'await this.playUseDecoder(e)};';
  const start = text.indexOf(head);
  const end = text.indexOf(tail, start);
  assert.ok(start > -1 && end > start, 'the real selected-MSE / non-MSE play block is missing from the bundle');
  const source = text.slice(start, end + tail.length);
  assert.ok(source.startsWith(head), 'the extraction starts at the real checkUseMSE result');
  assert.ok(source.endsWith(tail), 'the extraction ends at the real non-MSE decoder fallback');
  return source;
}

function playFake(overrides = {}) {
  const calls = [];
  const errors = [];
  const fake = Object.assign({
    useMSE: true,
    eioDualProbeInstalled: false,
    checkCalls: 0,
    configCalls: 0,
    config: { ...NUMERIC_CONFIG },
    bindResult: true,
    async checkUseMSE() {
      calls.push('check');
      this.checkCalls += 1;
      return this.useMSE;
    },
    getEioDualCacheActive() {
      calls.push('config');
      this.configCalls += 1;
      return this.config;
    },
    async eioBindDualCache(config) {
      calls.push('bind');
      this.boundConfig = config;
      return this.bindResult;
    },
    async playUseMSE() {
      calls.push('mse');
    },
    async playUseDecoder() {
      calls.push('decoder');
    },
    async eioAbortDualCache() {
      calls.push('abort');
      return { demux: 1, io: 1 };
    },
    onError(error) {
      calls.push(`error:${error.code}`);
      errors.push(error);
    }
  }, overrides);
  return { fake, calls, errors };
}

function runPlay(source, fake) {
  const run = vm.runInNewContext(`(async function(e){ ${source} return 'continued'; })`, { Error });
  return run.call(fake, {});
}

test('the real play block reports one activation error and never starts MSE or a decoder when bind is not true', async () => {
  const source = playBranchSource();
  for (const bindResult of [false, undefined, null, 0, 'true']) {
    const { fake, calls, errors } = playFake({ bindResult });
    const result = await runPlay(source, fake);
    assert.equal(result, undefined, `bind result ${String(bindResult)} must stop play before production`);
    assert.deepEqual(calls, ['check', 'config', 'bind', 'error:EIO_DUAL_ACTIVATION_FAILED']);
    assert.equal(errors.length, 1, 'the existing onError callback is invoked exactly once');
    assert.ok(errors[0] instanceof Error);
    assert.equal(errors[0].message, 'eio dual-cache activation failed');
    assert.equal(errors[0].code, 'EIO_DUAL_ACTIVATION_FAILED');
    assert.equal(fake.boundConfig, fake.config);
    assert.equal(fake.checkCalls, 1, 'checkUseMSE runs exactly once');
    assert.equal(fake.configCalls, 1, 'the config is obtained exactly once');
    assert.equal(calls.includes('mse'), false);
    assert.equal(calls.includes('decoder'), false);
  }
});

test('the real play block starts MSE once after a successful bind, with config once and after checkUseMSE', async () => {
  const source = playBranchSource();
  const { fake, calls, errors } = playFake({ bindResult: true });
  assert.equal(await runPlay(source, fake), 'continued');
  assert.deepEqual(calls, ['check', 'config', 'bind', 'mse']);
  assert.equal(errors.length, 0);
  assert.equal(fake.checkCalls, 1);
  assert.equal(fake.configCalls, 1);
  assert.equal(fake.boundConfig, fake.config);
  assert.ok(calls.indexOf('check') < calls.indexOf('config'));
  assert.ok(calls.indexOf('config') < calls.indexOf('bind'));
  assert.ok(calls.indexOf('bind') < calls.indexOf('mse'));
});

test('the real play block preserves the original MSE path when no dual config is active', async () => {
  const source = playBranchSource();
  const { fake, calls, errors } = playFake({ config: null });
  assert.equal(await runPlay(source, fake), 'continued');
  assert.deepEqual(calls, ['check', 'config', 'mse']);
  assert.equal(errors.length, 0);
  assert.equal(fake.checkCalls, 1);
  assert.equal(fake.configCalls, 1);
  assert.equal(calls.includes('bind'), false);
});

test('the real non-MSE branch keeps its disable-first fail-closed behaviour and never binds or reports activation errors', async () => {
  const source = playBranchSource();
  const failing = playFake({
    useMSE: false,
    eioDualProbeInstalled: true,
    async eioAbortDualCache() {
      this.abortCalls = (this.abortCalls || 0) + 1;
      return { demux: 1, io: 0 };
    }
  });
  assert.equal(await runPlay(source, failing.fake), undefined);
  assert.deepEqual(failing.calls, ['check', 'error:EIO_DUAL_IO_DISABLE_FAILED']);
  assert.equal(failing.fake.abortCalls, 1);
  assert.equal(failing.errors.length, 1);
  assert.equal(failing.errors[0].code, 'EIO_DUAL_IO_DISABLE_FAILED');
  assert.equal(failing.fake.eioDualCacheConfigError.code, 'EIO_DUAL_IO_DISABLE_FAILED');
  assert.equal(failing.fake.configCalls, 0, 'the non-MSE path never obtains the dual config');
  assert.equal(failing.calls.includes('mse'), false);
  assert.equal(failing.calls.includes('decoder'), false);

  const succeeding = playFake({
    useMSE: false,
    eioDualProbeInstalled: true,
    async eioAbortDualCache() {
      this.abortCalls = (this.abortCalls || 0) + 1;
      return { demux: 1, io: 1 };
    }
  });
  assert.equal(await runPlay(source, succeeding.fake), 'continued');
  assert.deepEqual(succeeding.calls, ['check', 'decoder']);
  assert.equal(succeeding.fake.abortCalls, 1);
  assert.equal(succeeding.errors.length, 0);
  assert.equal(succeeding.fake.configCalls, 0);
});

test('source contract: activation is requested only from the selected-MSE play caller', () => {
  const text = bundleText();
  const source = playBranchSource();
  const provisional = sliceMethod(text, 'getEioDualCacheProvisional(e){');

  assert.equal((source.match(/checkUseMSE/g) || []).length, 1, 'checkUseMSE appears once in the play block');
  assert.equal((source.match(/getEioDualCacheActive/g) || []).length, 1, 'the dual config is obtained once');
  assert.equal((source.match(/EIO_DUAL_ACTIVATION_FAILED/g) || []).length, 1, 'one activation error code in the play block');
  assert.equal((text.match(/EIO_DUAL_ACTIVATION_FAILED/g) || []).length, 1, 'no duplicate activation error outside play');
  assert.ok(source.indexOf('EIO_DUAL_ACTIVATION_FAILED') < source.indexOf('await this.playUseMSE(e)'), 'the fail-closed return precedes MSE production');
  assert.ok(source.indexOf('EIO_DUAL_ACTIVATION_FAILED') < source.indexOf('await this.playUseDecoder(e)'), 'the fail-closed return precedes decoder fallback');

  assert.equal(/eioBindDualCache|onError|EIO_DUAL_ACTIVATION_FAILED|checkUseMSE/.test(provisional), false, 'the provisional path never binds, reports or re-checks MSE');
  // Only the actual load-body markers moved: the body still starts at its
  // reader-buffer / provisional locals and ends at the LOADED transition.
  const loadStart = text.indexOf('let i=0,r=0,__eioProbeDual=null,__eioEarlyDomain=null;');
  const loadEnd = text.indexOf('this.status=4,this.fire(Ae.LOADED)', loadStart);
  assert.ok(loadStart > -1 && loadEnd > loadStart, 'the real load body was not found');
  const load = text.slice(loadStart, loadEnd);
  assert.equal(
    /eioBindDualCache|EIO_DUAL_ACTIVATION_FAILED|checkUseMSE/.test(load),
    false,
    'load never binds, fires the activation error or re-runs the MSE selection'
  );
  assert.ok(
    load.includes('__eioProbeDual=0===o?this.getEioDualCacheProvisional(e):null'),
    'load derives the provisional config only for the sniffed HTTP/file source'
  );
});

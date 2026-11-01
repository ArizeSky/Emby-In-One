const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

// Resume seeking through a Matroska Cues index: pick the newest cue whose time
// is at or before the target *and* that carries the sought track. The upstream
// loop inspected entry[i - 1] when cue[i] was in the future, so a subtitle cue
// between the wanted video cue and the target sent the reader to a later video
// cue (336233ms -> 344094ms instead of 333667ms in the reported file).
// This test executes the vendored seek method itself; it does not copy it.
//
// The reported-file fixture is an anonymized minimal reducer: it keeps only the
// reported cue times and positions, not the full HAR. Non-Cues behavior is
// covered too: BYTE seeks (flag 2) stay raw, the other flag bits (1/4/8/16) are
// treated like 0, and cue times are rescaled by TimestampScale before the
// comparison, while the target stays in AV_TIME_BASE microseconds.

const BUNDLE = path.join(__dirname, '../public/vendor/web/libmedia/573.avplayer.js');
const OLD_TAIL = 'this.context.cues.entry[Math.max(';
const NEW_BRANCH =
  'if(null!==(r=this.context.cues)&&void 0!==r&&r.entry.length){const e=t.privData;' +
  'let v=null,b=BigInt(0);for(let t=0;t<this.context.cues.entry.length;t++){' +
  'const a=this.context.cues.entry[t],u=(a.time||BigInt(0))*' +
  'BigInt(0|this.context.info.timestampScale)/BigInt(1e3);if(u>n)continue;' +
  'const c=a.pos;if(!c||!c.length)continue;' +
  'const l=c.find((t=>t.track===e.number));if(!l)continue;' +
  'if(null===v||u>b){v=l;b=u}}if(null!==v)d=v.pos+this.context.segmentStart}';

// Timestamps in the fixtures use TimestampScale = 1e6 ns (1ms/tick), so
// cue.time * timestampScale / 1000n yields microseconds, matching AV_TIME_BASE.
const SCALE = 1000000;
const NOPTS = BigInt('-9223372036854775808');
const UNSUPPORTED = -1n;
const SEGMENT_START = 52n; // observed: cluster pos 362100689 -> reader 362100741

const bundle = fs.readFileSync(BUNDLE, 'utf8');

const count = (needle) => bundle.split(needle).length - 1;

// The bundle is minified onto one line, so locate every seek method, keep the
// one that owns the Matroska Cues and cluster-index branches, and cut the real
// method out of the artifact.
const MARKERS = ['this.context.cues', 'this.context.clusterIndexes', 'this.context.segmentStart'];

function findSeekMethods() {
  const found = [];
  let at = bundle.indexOf('async seek(');
  while (at >= 0) {
    const end = bundle.indexOf('getAnalyzeStreamsCount', at);
    if (end > at) {
      const method = bundle.slice(at, end);
      if (
        MARKERS.every((marker) => method.includes(marker)) &&
        method.length < 2500 &&
        !method.slice('async seek('.length).includes('async seek(')
      ) {
        found.push({ at, method });
      }
    }
    at = bundle.indexOf('async seek(', at + 1);
  }
  return found;
}

function extractSeek() {
  const methods = findSeekMethods();
  assert.equal(methods.length, 1, 'exactly one Matroska seek method expected');

  const method = methods[0].method;
  assert.match(method, /^async seek\(e,t,a,i\)\{/, 'unexpected seek signature');
  assert.ok(method.includes('BigInt(g.E$)'), 'seek must keep the unsupported error path');

  const source = method.replace(/^async seek\(/, 'async function(');
  return vm.runInNewContext('(' + source + ')', {
    BigInt,
    Math,
    N: { k: (timestamp) => timestamp }, // avRescaleQ: fixtures already use AV_TIME_BASE
    x: { KR: {}, Dh: NOPTS },
    g: { E$: UNSUPPORTED },
  });
}

const seek = extractSeek();

function cue(timeMs, positions) {
  return {
    time: BigInt(timeMs),
    pos: positions.map(([track, pos]) => ({ track, pos: BigInt(pos) })),
  };
}

function context({ cues = null, clusters = [], segmentStart = SEGMENT_START, scale = SCALE } = {}) {
  const seeks = [];
  const formatContext = {
    ioReader: {
      getPos: () => 1234n,
      seek: async (pos) => {
        seeks.push(pos);
      },
    },
  };
  const instance = {
    context: { cues, clusterIndexes: clusters, segmentStart, info: { timestampScale: scale } },
  };
  const run = (ptsUs, track = 1, flags = 0) =>
    seek.call(instance, formatContext, { timeBase: {}, privData: { number: track } }, ptsUs, flags);
  return { run, seeks };
}

// The reported resume, minimized: video cue 333667 (track 1), subtitle cues
// 334290 (track 4) and 336450 (track 5) from the mixed track 1/4/5 Cues, and the
// next video cue only at 344094. Target 336233.
function reportedFile(clusters = []) {
  return context({
    cues: {
      entry: [
        cue(333667, [[1, 354323478]]),
        cue(334290, [[4, 354700000]]),
        cue(336450, [[5, 355100000]]),
        cue(344094, [[1, 362100689]]),
      ],
    },
    clusters,
  });
}

test('resume seek picks the newest same-track cue at or before the target', async () => {
  const { run, seeks } = reportedFile();
  assert.equal(await run(336233000n), 1234n);
  assert.deepEqual(seeks, [354323478n + SEGMENT_START]);
  // Guard the reported regression: the future video cue must not be used.
  assert.notDeepEqual(seeks, [362100689n + SEGMENT_START]);
  assert.equal(seeks[0], 354323530n);
});

test('the newest qualifying cue wins over older ones for the same track', async () => {
  const { run, seeks } = context({
    cues: { entry: [cue(1000, [[1, 100]]), cue(2000, [[1, 200]]), cue(3000, [[1, 300]])] },
  });
  assert.equal(await run(2500000n), 1234n);
  assert.deepEqual(seeks, [200n + SEGMENT_START]);
});

test('target equal to a cue timestamp selects that cue, including the first and last', async () => {
  const cues = { entry: [cue(1000, [[1, 100]]), cue(2000, [[1, 200]]), cue(3000, [[1, 300]])] };

  const first = context({ cues });
  await first.run(1000000n);
  assert.deepEqual(first.seeks, [100n + SEGMENT_START]);

  const middle = context({ cues });
  await middle.run(2000000n);
  assert.deepEqual(middle.seeks, [200n + SEGMENT_START]);

  const last = context({ cues });
  await last.run(3000000n);
  assert.deepEqual(last.seeks, [300n + SEGMENT_START]);
});

test('target later than the last cue uses the final same-track cue', async () => {
  const { run, seeks } = context({
    cues: { entry: [cue(1000, [[1, 100]]), cue(2000, [[1, 200]])] },
  });
  assert.equal(await run(9000000000n), 1234n);
  assert.deepEqual(seeks, [200n + SEGMENT_START]);
});

test('target before the first cue leaves Cues unset and keeps the original cluster fallback', async () => {
  const { run, seeks } = reportedFile([
    { time: 333667n, pos: 700n },
    { time: 344094n, pos: 800n },
  ]);
  assert.equal(await run(5000000n), 1234n);
  // The Cues branch selects nothing here, so the original fallback still runs
  // untouched; that fallback may itself land before or after the target.
  assert.deepEqual(seeks, [700n]);
});

test('cue timestamps in the future are never chosen', async () => {
  const { run, seeks } = context({
    cues: { entry: [cue(5000, [[1, 500]]), cue(9000, [[1, 900]])] },
  });
  const result = await run(3000000n);
  assert.equal(result, UNSUPPORTED, 'no cue at or before the target and no clusters');
  assert.deepEqual(seeks, []);
});

test('seeking audio picks audio positions from shared cue entries', async () => {
  const { run, seeks } = context({
    cues: { entry: [cue(1000, [[1, 100], [2, 900]]), cue(2000, [[1, 200], [2, 990]])] },
  });
  assert.equal(await run(1500000n, 2), 1234n);
  assert.deepEqual(seeks, [900n + SEGMENT_START]);
});

test('no cue for the sought track falls back to clusters instead of another track', async () => {
  const cues = { entry: [cue(1000, [[2, 900]]), cue(2000, [[2, 990]])] };

  const fallback = context({ cues, clusters: [{ time: 1000n, pos: 11n }, { time: 2000n, pos: 22n }] });
  assert.equal(await fallback.run(1500000n, 1), 1234n);
  assert.deepEqual(fallback.seeks, [11n]);

  const noClusters = context({ cues });
  assert.equal(await noClusters.run(1500000n, 1), UNSUPPORTED);
  assert.deepEqual(noClusters.seeks, []);
});

test('empty or missing cue lists keep the original fallback', async () => {
  const emptyCues = context({ cues: { entry: [] }, clusters: [{ time: 1000n, pos: 11n }] });
  assert.equal(await emptyCues.run(1500000n), 1234n);
  assert.deepEqual(emptyCues.seeks, [11n]);

  const noCues = context({ clusters: [{ time: 1000n, pos: 11n }, { time: 2000n, pos: 22n }] });
  assert.equal(await noCues.run(2500000n), 1234n);
  assert.deepEqual(noCues.seeks, [22n], 'cluster fallback keeps last-cluster behavior');
});

test('duplicate timestamps select deterministically without seeking forward', async () => {
  const cues = { entry: [cue(1000, [[1, 100]]), cue(1000, [[1, 200]]), cue(2000, [[1, 300]])] };
  const first = context({ cues });
  const second = context({ cues });
  await first.run(1500000n);
  await second.run(1500000n);
  assert.deepEqual(first.seeks, second.seeks);
  assert.deepEqual(first.seeks, [100n + SEGMENT_START]);
});

test('a single cue is used at or after its time and ignored before it', async () => {
  const cues = { entry: [cue(2000, [[1, 200]])] };

  const after = context({ cues, clusters: [{ time: 2000n, pos: 5n }] });
  assert.equal(await after.run(5000000n), 1234n);
  assert.deepEqual(after.seeks, [200n + SEGMENT_START]);

  const before = context({ cues, clusters: [{ time: 2000n, pos: 5n }] });
  assert.equal(await before.run(1000000n), 1234n);
  assert.deepEqual(before.seeks, [5n]);
});

test('BYTE seeks (flag 2) pass the raw offset through without consulting Cues', async () => {
  const { run, seeks } = context({
    cues: { entry: [cue(1000, [[1, 100]]), cue(2000, [[1, 200]])] },
  });
  // 869076 is a byte offset, not an AV_TIME_BASE timestamp.
  await run(869076n, 1, 2);
  assert.deepEqual(seeks, [869076n], 'raw byte offset must reach the reader as-is');
  // Neither cue-derived position (100/200 + segment start) may be selected.
  assert.ok(!seeks.includes(100n + SEGMENT_START));
  assert.ok(!seeks.includes(200n + SEGMENT_START));
});

test('other seek flag bits (1/4/8/16) behave like 0 for Cues selection', async () => {
  const cues = { entry: [cue(1000, [[1, 100]]), cue(2000, [[1, 200]]), cue(3000, [[1, 300]])] };
  const expected = [200n + SEGMENT_START];
  for (const flags of [0, 1, 4, 8, 16]) {
    const { run, seeks } = context({ cues });
    assert.equal(await run(2500000n, 1, flags), 1234n, `flags=${flags}`);
    assert.deepEqual(seeks, expected, `flags=${flags}`);
  }
});

test('cue times are rescaled by TimestampScale while the target stays AV_TIME_BASE microseconds', async () => {
  const scale = 2000000; // 2 ms per tick
  const cues = { entry: [cue(1000, [[1, 100]]), cue(2000, [[1, 200]])] };
  // 1000 ticks = 2_000_000us, 2000 ticks = 4_000_000us.

  const atFirst = context({ cues, scale });
  assert.equal(await atFirst.run(2000000n), 1234n);
  assert.deepEqual(atFirst.seeks, [100n + SEGMENT_START], 'equal target uses that cue');

  const between = context({ cues, scale });
  await between.run(3999999n);
  assert.deepEqual(between.seeks, [100n + SEGMENT_START], 'the 4_000_000us cue is still ahead');

  const atSecond = context({ cues, scale });
  await atSecond.run(4000000n);
  assert.deepEqual(atSecond.seeks, [200n + SEGMENT_START]);

  const beforeFirst = context({ cues, scale, clusters: [{ time: 0n, pos: 9n }] });
  await beforeFirst.run(1999999n);
  assert.deepEqual(beforeFirst.seeks, [9n], 'no cue qualifies yet, fallback unchanged');
});

test('the vendored bundle carries exactly the patched Cues branch', () => {
  assert.equal(count(NEW_BRANCH), 1, 'patched Cues branch must appear exactly once');
  assert.equal(count(OLD_TAIL), 0, 'previous-entry Cues selection must be gone');
  assert.ok(count('async seek(') >= 1, 'Matroska seek method expected');
  assert.equal(count('if(null===v||u>b){v=l;b=u}'), 1, 'candidate tracker must be unique');
  assert.equal(findSeekMethods().length, 1, 'one Matroska seek method expected');
});

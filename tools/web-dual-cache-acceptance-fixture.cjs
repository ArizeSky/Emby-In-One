#!/usr/bin/env node
'use strict';
/*
 * Offline acceptance fixture for the real libmedia vendor bundle and a real
 * browser engine. It never starts the application, never talks to an upstream,
 * never proxies arbitrary URLs and never reads application configuration.
 *
 * Only loopback HTTP, an explicit vendor root and one synthetic sample are
 * served. The page loads the real /vendor/avplayer.js, constructs a real
 * AVPlayer and reports only observed evidence. The dual-cache snapshot API is
 * not implemented in the current vendor baseline and is reported as such.
 */

const http = require('node:http');
const fs = require('node:fs');
const path = require('node:path');

const HOST = '127.0.0.1';
const PORT = 0;
const TTL_MS = 180 * 1000;
const MAX_REQUESTS = 5000;
const MAX_TOTAL_BYTES = 128 * 1024 * 1024;
const MAX_MEDIA_BYTES = 96 * 1024 * 1024;
const DEFAULT_VENDOR_ROOT = '/var/tmp/eio-dual-integrated-v2/public/vendor/web/libmedia';
const SAMPLE_PATH = process.env.EIO_FIXTURE_SAMPLE_PATH || '/var/tmp/eio-dual-v2-synthetic.mp4';

const state = {
  startedAt: Date.now(),
  vendorRoot: null,
  sampleSize: 0,
  port: null,
  server: null,
  ttlTimer: null,
  shuttingDown: false,
  shutdownReason: null,
  stats: {
    requests: 0,
    rejected: 0,
    mediaRequests: 0,
    mediaBytesSent: 0,
    vendorRequests: 0,
    vendorBytesSent: 0,
    documentBytesSent: 0,
    totalBytesSent: 0,
    mediaStatuses: { 200: 0, 206: 0, 416: 0, head: 0 },
  },
};

function log() {
  const args = Array.prototype.slice.call(arguments).map(function (value) {
    return typeof value === 'string' ? value : JSON.stringify(value);
  });
  console.log('[eio-fixture ' + new Date().toISOString() + '] ' + args.join(' '));
}

function safeError(error) {
  let name = 'Error';
  let message = '';
  try {
    name = error && error.name ? String(error.name) : typeof error;
    message = error && error.message != null ? String(error.message) : String(error);
  } catch (_) {
    message = 'unprintable error';
  }
  message = message.replace(/([?&])(token|key|api_?key|password|passwd|secret|sig|signature|auth)=[^&\s]*/gi,
    function (_m, separator, key) { return separator + key + '=[redacted]'; });
  message = message.replace(/https?:\/\/[^\s'"]+/gi, function (match) {
    try { const parsed = new URL(match); return parsed.origin + parsed.pathname; } catch (_) { return '[url]'; }
  });
  if (message.length > 300) message = message.slice(0, 300) + '...';
  return { name: name, message: message };
}

function fail(message, code) {
  console.error('[eio-fixture] ' + message);
  process.exit(code || 2);
}

if (!path.isAbsolute(SAMPLE_PATH)) fail('sample path must be absolute');
if (!fs.existsSync(SAMPLE_PATH)) {
  fail('synthetic sample missing: ' + SAMPLE_PATH + ' (see instructions; create it with local ffmpeg, do not fetch upstream)');
}
try {
  const sampleStat = fs.statSync(SAMPLE_PATH);
  if (!sampleStat.isFile() || sampleStat.size <= 0) fail('sample is not a non-empty file: ' + SAMPLE_PATH);
  state.sampleSize = sampleStat.size;
} catch (error) {
  fail('cannot stat sample: ' + safeError(error).message);
}

let vendorRoot;
try {
  const configured = process.env.EIO_FIXTURE_VENDOR_ROOT || DEFAULT_VENDOR_ROOT;
  if (!path.isAbsolute(configured)) fail('EIO_FIXTURE_VENDOR_ROOT must be an absolute local path');
  if (configured.indexOf('\0') >= 0 || configured.indexOf('\n') >= 0) fail('vendor root contains control characters');
  vendorRoot = fs.realpathSync(configured);
  if (!fs.statSync(vendorRoot).isDirectory()) fail('vendor root is not a directory: ' + vendorRoot);
  if (!fs.statSync(path.join(vendorRoot, 'avplayer.js')).isFile()) {
    fail('vendor root does not contain avplayer.js: ' + vendorRoot);
  }
} catch (error) {
  fail('vendor root unavailable: ' + safeError(error).message);
}
state.vendorRoot = vendorRoot;
try {
  if (!fs.statSync(path.join(vendorRoot, 'wasm')).isDirectory()) {
    log('warning: vendor root has no wasm directory; real decode may fail and must be reported as failure');
  }
} catch (_) {
  log('warning: vendor root has no wasm directory; real decode may fail and must be reported as failure');
}
log('vendor root: ' + vendorRoot);
log('sample: ' + SAMPLE_PATH + ' (' + state.sampleSize + ' bytes)');

function isInside(root, target) {
  return target === root || target.indexOf(root + path.sep) === 0;
}

function isLoopback(req) {
  const address = req.socket && req.socket.remoteAddress;
  return address === '127.0.0.1' || address === '::1' || address === '::ffff:127.0.0.1';
}

function baseHeaders(contentType, extra) {
  const headers = {
    'Cross-Origin-Opener-Policy': 'same-origin',
    'Cross-Origin-Embedder-Policy': 'require-corp',
    'Cross-Origin-Resource-Policy': 'same-origin',
    "Content-Security-Policy": "default-src 'none'; script-src 'self' 'unsafe-inline' 'unsafe-eval' 'wasm-unsafe-eval' blob:; worker-src 'self' blob:; connect-src 'self'; media-src 'self' blob:; style-src 'self' 'unsafe-inline'; img-src 'self' data:",
    'X-Content-Type-Options': 'nosniff',
    'Referrer-Policy': 'no-referrer',
    'Cache-Control': 'no-store',
    'Connection': 'close',
  };
  if (contentType) headers['Content-Type'] = contentType;
  return Object.assign(headers, extra || {});
}

const MIME = {
  '.js': 'text/javascript; charset=utf-8',
  '.mjs': 'text/javascript; charset=utf-8',
  '.wasm': 'application/wasm',
  '.json': 'application/json; charset=utf-8',
  '.map': 'application/json; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.html': 'text/html; charset=utf-8',
  '.txt': 'text/plain; charset=utf-8',
  '.md': 'text/markdown; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.png': 'image/png',
  '.jpg': 'image/jpeg',
  '.jpeg': 'image/jpeg',
  '.gif': 'image/gif',
  '.webp': 'image/webp',
  '.ico': 'image/x-icon',
  '.woff': 'font/woff',
  '.woff2': 'font/woff2',
  '.ttf': 'font/ttf',
  '.otf': 'font/otf',
  '.mp4': 'video/mp4',
};

function contentTypeFor(filePath) {
  return MIME[path.extname(filePath).toLowerCase()] || 'application/octet-stream';
}

function account(bytes, kind) {
  state.stats.totalBytesSent += bytes;
  if (kind === 'media') state.stats.mediaBytesSent += bytes;
  else if (kind === 'vendor') state.stats.vendorBytesSent += bytes;
  else if (kind === 'document') state.stats.documentBytesSent += bytes;
  if (state.stats.totalBytesSent > MAX_TOTAL_BYTES) scheduleShutdown('byte-cap');
  if (kind === 'media' && state.stats.mediaBytesSent > MAX_MEDIA_BYTES) scheduleShutdown('media-byte-cap');
}

function sendText(res, status, text, requestMethod) {
  const body = Buffer.from(String(text), 'utf8');
  const headers = baseHeaders('text/plain; charset=utf-8', { 'Content-Length': String(body.length) });
  res.writeHead(status, headers);
  if (requestMethod === 'HEAD') {
    res.end();
    return;
  }
  account(body.length, 'other');
  res.end(body);
}

function sendJson(res, status, value) {
  const body = Buffer.from(JSON.stringify(value), 'utf8');
  const headers = baseHeaders('application/json; charset=utf-8', { 'Content-Length': String(body.length) });
  res.writeHead(status, headers);
  account(body.length, 'other');
  res.end(body);
}

function sendHtml(res, html) {
  const body = Buffer.from(html, 'utf8');
  const headers = baseHeaders('text/html; charset=utf-8', { 'Content-Length': String(body.length) });
  res.writeHead(200, headers);
  account(body.length, 'document');
  res.end(body);
}

function streamFile(req, res, filePath, start, end, status, kind, extraHeaders) {
  const length = end - start + 1;
  const headers = baseHeaders(contentTypeFor(filePath), Object.assign({
    'Content-Length': String(length),
    'Accept-Ranges': 'bytes',
  }, extraHeaders || {}));
  res.writeHead(status, headers);
  if (req.method === 'HEAD' || length <= 0) {
    res.end();
    return;
  }
  const stream = fs.createReadStream(filePath, { start: start, end: end });
  let closed = false;
  stream.on('data', function (chunk) {
    if (closed) return;
    account(chunk.length, kind);
    if (!res.write(chunk)) {
      stream.pause();
      res.once('drain', function () { if (!closed) stream.resume(); });
    }
  });
  stream.on('end', function () { if (!closed) res.end(); });
  stream.on('error', function (error) {
    closed = true;
    log('stream error: ' + safeError(error).message);
    res.destroy();
  });
  res.on('close', function () {
    if (closed) return;
    closed = true;
    if (!res.writableEnded) stream.destroy();
  });
}

function parseRange(header, size) {
  if (!header) return { type: 'none' };
  const match = /^bytes=(\d*)-(\d*)$/.exec(String(header).trim());
  if (!match) return { type: 'invalid' };
  const first = match[1];
  const second = match[2];
  if (first === '' && second === '') return { type: 'invalid' };
  if (first === '') {
    const suffix = Number(second);
    if (!Number.isSafeInteger(suffix) || suffix <= 0) return { type: 'invalid' };
    return { type: 'ok', start: Math.max(0, size - suffix), end: size - 1 };
  }
  const start = Number(first);
  if (!Number.isSafeInteger(start) || start < 0 || start >= size) return { type: 'invalid' };
  let end = second === '' ? size - 1 : Number(second);
  if (!Number.isSafeInteger(end) || end < start) return { type: 'invalid' };
  if (end >= size) end = size - 1;
  return { type: 'ok', start: start, end: end };
}

function serveSample(req, res) {
  state.stats.mediaRequests += 1;
  if (req.method === 'HEAD') state.stats.mediaStatuses.head += 1;
  fs.stat(SAMPLE_PATH, function (error, stat) {
    if (error || !stat.isFile() || stat.size <= 0) {
      sendText(res, 404, 'sample unavailable', req.method);
      return;
    }
    const size = stat.size;
    const range = parseRange(req.headers.range, size);
    if (range.type === 'invalid') {
      state.stats.mediaStatuses[416] += 1;
      const headers = baseHeaders('text/plain; charset=utf-8', {
        'Content-Range': 'bytes */' + size,
        'Content-Length': '0',
        'Accept-Ranges': 'bytes',
      });
      res.writeHead(416, headers);
      res.end();
      return;
    }
    let status = 200;
    let start = 0;
    let end = size - 1;
    const extra = {};
    if (range.type === 'ok') {
      status = 206;
      start = range.start;
      end = range.end;
      extra['Content-Range'] = 'bytes ' + start + '-' + end + '/' + size;
    } else {
      state.stats.mediaStatuses[200] += 1;
    }
    if (status === 206) state.stats.mediaStatuses[206] += 1;
    streamFile(req, res, SAMPLE_PATH, start, end, status, 'media', extra);
  });
}

function serveVendor(req, res, relPath) {
  let decoded;
  try {
    decoded = decodeURIComponent(relPath);
  } catch (_) {
    sendText(res, 400, 'bad path encoding', req.method);
    return;
  }
  if (!decoded || decoded.indexOf('\0') >= 0 || decoded.indexOf('\\') >= 0) {
    sendText(res, 403, 'invalid vendor path', req.method);
    return;
  }
  const segments = decoded.split('/');
  for (let i = 0; i < segments.length; i += 1) {
    if (segments[i] === '' || segments[i] === '.' || segments[i] === '..') {
      sendText(res, 403, 'path traversal rejected', req.method);
      return;
    }
  }
  const candidate = path.resolve(vendorRoot, decoded);
  if (!isInside(vendorRoot, candidate)) {
    sendText(res, 403, 'outside vendor root', req.method);
    return;
  }
  fs.realpath(candidate, function (error, realPath) {
    if (error || !isInside(vendorRoot, realPath)) {
      sendText(res, error ? 404 : 403, error ? 'vendor file not found' : 'symlink outside vendor root', req.method);
      return;
    }
    fs.stat(realPath, function (statError, stat) {
      if (statError || !stat.isFile()) {
        sendText(res, 404, 'vendor file not found', req.method);
        return;
      }
      state.stats.vendorRequests += 1;
      streamFile(req, res, realPath, 0, stat.size - 1, 200, 'vendor');
    });
  });
}

function handleRequest(req, res) {
  if (!isLoopback(req)) {
    state.stats.rejected += 1;
    return sendText(res, 403, 'loopback only', req.method);
  }
  const rawUrl = req.url || '/';
  let pathname;
  try {
    pathname = decodeURIComponent(new URL(rawUrl, 'http://127.0.0.1').pathname);
  } catch (_) {
    state.stats.rejected += 1;
    return sendText(res, 400, 'bad url', req.method);
  }
  const rawPath = rawUrl.split('?')[0];
  if (rawPath.indexOf('\0') >= 0 || rawPath.indexOf('\\') >= 0 ||
      /(?:^|\/)\.\.(?:\/|$)/.test(rawPath) || pathname.indexOf('\0') >= 0) {
    state.stats.rejected += 1;
    return sendText(res, 403, 'path traversal rejected', req.method);
  }

  state.stats.requests += 1;
  if (state.stats.requests > MAX_REQUESTS) {
    state.stats.rejected += 1;
    sendText(res, 429, 'request cap reached', req.method);
    scheduleShutdown('request-cap');
    return;
  }

  if (req.method === 'POST') {
    if (pathname === '/__stop') {
      sendJson(res, 200, { ok: true, stopping: true, stats: state.stats });
      scheduleShutdown('http-stop');
      return;
    }
    return sendText(res, 405, 'method not allowed', req.method);
  }
  if (req.method !== 'GET' && req.method !== 'HEAD') {
    return sendText(res, 405, 'method not allowed', req.method);
  }

  if (pathname === '/' || pathname === '/index.html') {
    if (req.method === 'HEAD') {
      const length = Buffer.byteLength(PAGE_HTML);
      const headers = baseHeaders('text/html; charset=utf-8', { 'Content-Length': String(length) });
      res.writeHead(200, headers);
      res.end();
      return;
    }
    return sendHtml(res, PAGE_HTML);
  }
  if (pathname === '/__health') {
    return sendJson(res, 200, { ok: true, uptimeMs: Date.now() - state.startedAt, ttlMs: TTL_MS });
  }
  if (pathname === '/__stats') {
    return sendJson(res, 200, Object.assign({}, state.stats, {
      uptimeMs: Date.now() - state.startedAt,
      ttlMs: TTL_MS,
      statsStartedAt: state.startedAt,
      sampleBytes: state.sampleSize,
      vendorRootKind: 'explicit-local-vendor-root',
    }));
  }
  if (pathname === '/sample.mp4' || pathname === '/sample.mkv' || pathname === '/sample.php' || pathname === '/sample.ts' || pathname === '/sample.mpg') return serveSample(req, res);
  if (pathname.indexOf('/vendor/') === 0) return serveVendor(req, res, pathname.slice('/vendor/'.length));
  return sendText(res, 404, 'not found', req.method);
}

const PAGE_HTML = String.raw`<!doctype html>
<html lang="zh">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>EIO dual-cache v2 browser fixture</title>
<style>
  html,body{margin:0;background:#101418;color:#e6edf3;font:13px/1.5 ui-monospace,SFMono-Regular,Menlo,monospace}
  #bar{display:flex;gap:10px;align-items:center;padding:8px 12px;border-bottom:1px solid #2a323c}
  button{background:#1f6feb;color:#fff;border:0;border-radius:4px;padding:6px 14px;cursor:pointer}
  button:disabled{background:#30363d;color:#8b949e;cursor:default}
  #stage{width:640px;height:360px;margin:12px;background:#000;position:relative}
  #out{white-space:pre-wrap;word-break:break-word;max-width:980px;margin:12px;color:#9fb0c0;max-height:42vh;overflow:auto}
  #status{color:#8b949e}
</style>
</head>
<body>
<div id="bar"><button id="start">Start</button><span id="status">idle</span></div>
<div id="stage"></div>
<pre id="out"></pre>
<script>
// libmedia captures self.Worker when the vendor bundle evaluates, so install
// the observability proxy before that. It only wraps the native constructor;
// it does not rewrite vendor files or change player options.
(function () {
  'use strict';
  function probeSafeUrl(value) {
    try {
      var text = String(value);
      if (text.indexOf('blob:') === 0) return 'blob:';
      var parsed = new URL(text, location.href);
      if (parsed.origin === location.origin) return parsed.pathname;
      return parsed.protocol + '//' + parsed.host + parsed.pathname;
    } catch (_) { return 'unprintable-worker-url'; }
  }
  function probeSafeError(error) {
    var name = 'Error';
    var message = '';
    try {
      name = error && error.name ? String(error.name) : typeof error;
      message = error && error.message != null ? String(error.message) : String(error);
    } catch (_) { message = 'unprintable error'; }
    return { name: name, message: message.slice(0, 300) };
  }
  var state = {
    available: false,
    patched: false,
    created: 0,
    terminated: 0,
    live: 0,
    maxLive: 0,
    urls: [],
    probeError: null,
    installedBeforeVendor: true
  };
  state.install = function () {
    var NativeWorker = window.Worker;
    state.available = typeof NativeWorker === 'function';
    if (!state.available) {
      state.probeError = { name: 'MissingApi', message: 'window.Worker' };
      return state;
    }
    if (state.patched) return state;
    try {
      window.Worker = new Proxy(NativeWorker, {
        construct: function (target, args) {
          var instance = Reflect.construct(target, args);
          state.created += 1;
          state.live += 1;
          if (state.live > state.maxLive) state.maxLive = state.live;
          if (state.urls.length < 20) state.urls.push(probeSafeUrl(args && args[0]));
          try {
            var original = instance.terminate;
            if (typeof original === 'function') {
              instance.terminate = function () {
                if (!this.__eioTerminated) {
                  try { Object.defineProperty(this, '__eioTerminated', { value: true, configurable: true }); } catch (_) {}
                  state.terminated += 1;
                  state.live = Math.max(0, state.live - 1);
                }
                return original.apply(this, arguments);
              };
            }
          } catch (error) {
            state.probeError = probeSafeError(error);
          }
          return instance;
        }
      });
      state.patched = true;
    } catch (error) {
      state.patched = false;
      state.probeError = probeSafeError(error);
    }
    return state;
  };
  window.__eioWorkerProbe = state;
  state.install();
})();
</script>
<script src="/vendor/shared-media-ownership.js"></script>
<script src="/vendor/shared-media-domain.js"></script>
<script src="/vendor/owned-media-resources.js"></script>
<script src="/vendor/shared-media-release-registry.js"></script>
<script src="/vendor/media-resource-handles.js"></script>
<script src="/vendor/codecpar-leases.js"></script>
<script src="/vendor/native-playback-clock.js"></script>
<script src="/vendor/native-playback-clock-source.js"></script>
<script src="/vendor/payload-transfer.js"></script>
<script src="/vendor/dual-cache-snapshot-schema.js"></script>
<script src="/vendor/avplayer.js"></script>
<script>
(function () {
  'use strict';

  var DEFAULT_BUDGET = {
    maxBytes: 24000000,
    ioBytes: 3000000,
    demuxBytes: 5000000,
    mseBytes: 16000000,
    maxPacketBytes: 512000,
    maxForwardSeconds: 12
  };
  // Bounded wait for real HTMLVideo first-frame evidence in MSE mode.
  var MSE_FRAME_EVIDENCE_WAIT_MS = 10000;
  var workerProbeInstalled = false;
  var firstFrameResolve = null;
  var firstFramePromise = null;

  var st = {
    started: false,
    starting: false,
    disposedState: 'idle',
    disposeReason: null,
    disposedAt: null,
    disposePromise: null,
    startedAt: null,
    mediaStartedAt: null,
    mode: 'dual',
    budget: null,
    timeLimitSeconds: null,
    deadline: 0,
    core: null,
    container: null,
    samples: [],
    seeks: [],
    errors: [],
    events: [],
    firstFrameRendered: false,
    mseFrame: null,
    eioDualCacheOptionPassed: false,
    optionKeys: [],
    loadOk: false,
    loadUrl: null,
    playOk: false,
    engine: { created: 0, destroyed: 0, isMSE: null, isHls: null, isLive: null, durationMs: null, currentTimeRaw: null },
    workers: window.__eioWorkerProbe || { available: false, patched: false, created: 0, terminated: 0, live: 0, maxLive: 0, urls: [], probeError: null },
    snapshot: { status: 'pending', latest: null, history: [], lastPollAt: 0, inFlight: false, observedAt: null, error: null, missingApi: null, reason: null },
    serverStats: null,
    lastStatsPollAt: 0,
    sampleTimer: null,
    autoTimer: null,
    configWarnings: [],
    disposeInfo: null,
    maxMediaCurrentTime: 0,
    maxCoreTimeMs: null,
    lastSample: null
  };

  function now() { return Date.now(); }
  function delay(ms) { return new Promise(function (resolve) { setTimeout(resolve, ms); }); }
  function isFiniteNumber(value) { return typeof value === 'number' && isFinite(value); }
  function round3(value) { return isFiniteNumber(value) ? Math.round(value * 1000) / 1000 : null; }
  function clamp(value, low, high) { return Math.min(high, Math.max(low, value)); }

  function safeError(error) {
    var name = 'Error';
    var message = '';
    try {
      name = error && error.name ? String(error.name) : typeof error;
      message = error && error.message != null ? String(error.message) : String(error);
    } catch (_) {
      message = 'unprintable error';
    }
    message = message.replace(/([?&])(token|key|api_?key|password|passwd|secret|sig|signature|auth)=[^&\s]*/gi,
      function (_m, separator, key) { return separator + key + '=[redacted]'; });
    message = message.replace(/https?:\/\/[^\s'"]+/gi, function (match) {
      try { var parsed = new URL(match); return parsed.origin + parsed.pathname; } catch (_) { return '[url]'; }
    });
    if (message.length > 300) message = message.slice(0, 300) + '...';
    return { name: name, message: message };
  }

  function safeUrl(value) {
    try {
      var text = String(value);
      if (text.indexOf('blob:') === 0) return 'blob:';
      var parsed = new URL(text, location.href);
      if (parsed.origin === location.origin) return parsed.pathname;
      return parsed.protocol + '//' + parsed.host + parsed.pathname;
    } catch (_) {
      return 'unprintable-worker-url';
    }
  }

  function renderStatus(text) {
    var node = document.getElementById('status');
    if (node) node.textContent = text;
  }

  function refreshServerStats(force) {
    var t = performance.now();
    if (!force && t - st.lastStatsPollAt < 1000) return Promise.resolve(st.serverStats);
    st.lastStatsPollAt = t;
    return fetch('/__stats', { cache: 'no-store' }).then(function (response) {
      return response.ok ? response.json() : null;
    }).then(function (value) {
      if (value) st.serverStats = value;
      return st.serverStats;
    }).catch(function () { return st.serverStats; });
  }

  function resetFirstFrame() {
    firstFramePromise = new Promise(function (resolve) { firstFrameResolve = resolve; });
    firstFramePromise.catch(function () {});
  }

  function resolveFirstFrame() {
    if (firstFrameResolve) {
      var resolve = firstFrameResolve;
      firstFrameResolve = null;
      resolve();
    }
  }

  function trackWorker(instance, args) {
    var workers = st.workers;
    workers.created += 1;
    workers.live += 1;
    if (workers.live > workers.maxLive) workers.maxLive = workers.live;
    if (workers.urls.length < 20) workers.urls.push(safeUrl(args && args[0]));
    try {
      var original = instance.terminate;
      if (typeof original === 'function') {
        try {
          Object.defineProperty(instance, '__eioTerminateWrapped', { value: true, configurable: true });
        } catch (_) {}
        instance.terminate = function () {
          if (!this.__eioTerminated) {
            try { Object.defineProperty(this, '__eioTerminated', { value: true, configurable: true }); } catch (_) {}
            workers.terminated += 1;
            workers.live = Math.max(0, workers.live - 1);
          }
          return original.apply(this, arguments);
        };
      }
    } catch (error) {
      workers.probeError = safeError(error);
    }
  }

  function installWorkerProbe() {
    var shared = window.__eioWorkerProbe;
    if (shared && typeof shared.install === 'function') {
      st.workers = shared;
      shared.install();
      return;
    }
    // Fallback for pages without the pre-vendor probe. libmedia caches
    // self.Worker at module init, so this only sees later constructions.
    var workers = st.workers;
    var NativeWorker = window.Worker;
    workers.available = typeof NativeWorker === 'function';
    if (!workers.available) {
      workers.probeError = { name: 'MissingApi', message: 'window.Worker' };
      return;
    }
    if (workerProbeInstalled) {
      workers.patched = true;
      return;
    }
    try {
      var Probed = new Proxy(NativeWorker, {
        construct: function (target, args) {
          var instance = Reflect.construct(target, args);
          trackWorker(instance, args);
          return instance;
        }
      });
      window.Worker = Probed;
      workerProbeInstalled = true;
      workers.patched = true;
    } catch (error) {
      workers.patched = false;
      workers.probeError = safeError(error);
    }
  }

  function cloneSafe(value) {
    try {
      if (typeof structuredClone === 'function') return structuredClone(value);
    } catch (_) {}
    try { return JSON.parse(JSON.stringify(value)); } catch (_) { return null; }
  }

  function sanitizeSnapshot(value, depth, seen) {
    depth = depth || 0;
    seen = seen || new WeakSet();
    if (value == null) return null;
    if (typeof value === 'number') return isFinite(value) ? value : String(value);
    if (typeof value === 'bigint') {
      var asNumber = Number(value);
      return Number.isSafeInteger(asNumber) ? asNumber : String(value) + 'n';
    }
    if (typeof value === 'boolean' || typeof value === 'string') {
      return typeof value === 'string' && value.length > 500 ? value.slice(0, 500) + '...' : value;
    }
    if (typeof value !== 'object') return undefined;
    if (depth > 8 || seen.has(value)) return '[truncated]';
    seen.add(value);
    if (Array.isArray(value)) {
      var list = [];
      for (var i = 0; i < Math.min(value.length, 200); i += 1) {
        var item = sanitizeSnapshot(value[i], depth + 1, seen);
        if (item !== undefined) list.push(item);
      }
      return list;
    }
    var out = {};
    var keys = Object.keys(value);
    for (var k = 0; k < Math.min(keys.length, 200); k += 1) {
      var key = keys[k];
      var sanitized;
      try { sanitized = sanitizeSnapshot(value[key], depth + 1, seen); } catch (_) { sanitized = '[unreadable]'; }
      if (sanitized !== undefined) out[key] = sanitized;
    }
    return out;
  }

  function readSnapshotNow() {
    if (!st.core || st.disposedState === 'disposed' || st.disposedState === 'disposing') return;
    var t = performance.now();
    if (t - st.snapshot.lastPollAt < 500) return;
    st.snapshot.lastPollAt = t;
    var fn = st.core.getEioDualCacheSnapshot;
    if (typeof fn !== 'function') {
      st.snapshot.status = 'notImplemented';
      st.snapshot.missingApi = 'core.getEioDualCacheSnapshot';
      return;
    }
    if (st.snapshot.inFlight) return;
    st.snapshot.inFlight = true;
    Promise.resolve().then(function () { return fn.call(st.core); }).then(function (value) {
      if (value == null) {
        st.snapshot.status = 'incomplete';
        st.snapshot.reason = 'snapshot-null-or-undefined';
        return;
      }
      var clone = cloneSafe(value);
      var safe = clone == null ? null : sanitizeSnapshot(clone, 0, new WeakSet());
      if (safe == null) {
        st.snapshot.status = 'incomplete';
        st.snapshot.reason = 'snapshot-not-serializable';
        return;
      }
      st.snapshot.status = 'observed';
      st.snapshot.latest = safe;
      st.snapshot.observedAt = now();
      st.snapshot.reason = null;
      st.snapshot.history.push({ at: now(), snapshot: safe });
      if (st.snapshot.history.length > 60) st.snapshot.history.shift();
    }).catch(function (error) {
      st.snapshot.status = 'error';
      st.snapshot.error = safeError(error);
    }).then(function () { st.snapshot.inFlight = false; });
  }

  // Dual-cache acceptance is fail-closed until a frozen RPC schema is supplied.
  // Do not infer byte/time fields, units or totals from arbitrary keys: regex
  // and field-name matching cannot prove conservation and were removed.
  // §5.3 freeze: the schema and exact validator live in the canonical module
  // web-src/player-dual-cache-snapshot-schema.js (vendor copy served above).
  var FROZEN_DUAL_SNAPSHOT_SCHEMA =
    typeof window.EioDualCacheSnapshotSchema === 'object' &&
    typeof window.EioDualCacheSnapshotSchema.validate === 'function' &&
    window.EioDualCacheSnapshotSchema.KIND === 'eio-dual-cache-snapshot-schema/v1'
      ? window.EioDualCacheSnapshotSchema
      : null;

  function hasOwn(root, key) {
    return !!root && typeof root === 'object' && Object.prototype.hasOwnProperty.call(root, key);
  }

  function readRawAggregate(snapshot) {
    var out = {
      path: 'snapshot.aggregate',
      present: false,
      complete: null,
      incomplete: null,
      active: null,
      raw: null,
      usedForVerdict: false,
      note: 'raw RPC aggregate values only; complete/incomplete/active never imply feature completion'
    };
    if (!hasOwn(snapshot, 'aggregate')) return out;
    var aggregate = snapshot.aggregate;
    out.present = true;
    out.raw = aggregate == null ? null : aggregate;
    if (aggregate && typeof aggregate === 'object') {
      if (hasOwn(aggregate, 'complete')) out.complete = aggregate.complete;
      if (hasOwn(aggregate, 'incomplete')) out.incomplete = aggregate.incomplete;
      if (hasOwn(aggregate, 'active')) out.active = aggregate.active;
    }
    return out;
  }

  function analyzeSnapshot() {
    var result = {
      status: 'pending',
      ok: false,
      hardViolation: false,
      schema: {
        frozen: FROZEN_DUAL_SNAPSHOT_SCHEMA != null,
        exactValidation: false,
        source: 'frozen-literal-rpc-schema-required'
      },
      aggregate: readRawAggregate(st.snapshot.latest),
      missing: [],
      violations: [],
      notes: []
    };
    if (st.mode === 'baseline') {
      result.status = 'not-evaluated';
      result.notes.push('baseline mode: dual-cache ledger not evaluated');
      return result;
    }
    if (st.snapshot.status !== 'observed') {
      result.status = st.snapshot.status === 'error' ? 'error' : 'incomplete';
      result.missing.push('core.getEioDualCacheSnapshot:' + st.snapshot.status);
      return result;
    }
    if (FROZEN_DUAL_SNAPSHOT_SCHEMA == null) {
      result.status = 'schema-pending';
      result.missing.push('frozen-dual-cache-rpc-schema');
      result.notes.push('No frozen RPC schema is configured; dual acceptance stays schema-pending and cannot pass.');
      result.notes.push('Regex, field-name and unit inference were removed; raw aggregate values are evidence only.');
      return result;
    }
    result.schema.frozen = true;
    result.schema.source = FROZEN_DUAL_SNAPSHOT_SCHEMA.KIND;
    var exact = FROZEN_DUAL_SNAPSHOT_SCHEMA.validate(st.snapshot.latest);
    result.schema.exactValidation = exact.exactValidation === true;
    result.violations = exact.violations.slice();
    result.hardViolation = exact.violations.length > 0;
    result.complete = exact.complete === true;
    result.incompleteReasons = exact.incomplete.slice();
    if (exact.violations.length > 0) {
      result.status = 'violation';
      result.ok = false;
      return result;
    }
    result.status = 'validated';
    result.ok = true;
    result.notes.push('Frozen-schema exact validation passed; complete=' +
      String(exact.complete) + ' stays honest and is not a pass condition.');
    return result;
  }

  function callBoolOn(core, name) {
    try {
      if (!core || typeof core[name] !== 'function') return null;
      var value = core[name]();
      return typeof value === 'boolean' ? value : null;
    } catch (_) { return null; }
  }

  function callNumberOn(core, name) {
    try {
      if (!core || typeof core[name] !== 'function') return null;
      var value = core[name]();
      if (typeof value === 'bigint') value = Number(value);
      return isFiniteNumber(value) ? value : null;
    } catch (_) { return null; }
  }

  function readRawCurrentTime(core) {
    try {
      if (!core || core.currentTime == null) return null;
      var value = core.currentTime;
      if (typeof value === 'bigint') value = Number(value);
      return isFiniteNumber(value) ? value : null;
    } catch (_) { return null; }
  }

  function sample() {
    if (!st.core || st.disposedState === 'disposed') return st.lastSample;
    readSnapshotNow();
    refreshServerStats(false);
    var video = st.container ? st.container.querySelector('video') : null;
    var media = null;
    var ranges = [];
    var contiguous = null;
    if (video) {
      try {
        var buffered = video.buffered;
        if (buffered) {
          for (var i = 0; i < buffered.length; i += 1) {
            ranges.push({ start: round3(buffered.start(i)), end: round3(buffered.end(i)) });
          }
          contiguous = true;
          for (var j = 1; j < ranges.length; j += 1) {
            if (ranges[j].start - ranges[j - 1].end > 0.25) contiguous = false;
          }
        }
      } catch (_) { contiguous = null; }
      if (isFiniteNumber(video.currentTime) && video.currentTime > st.maxMediaCurrentTime) {
        st.maxMediaCurrentTime = video.currentTime;
      }
      media = {
        currentTime: round3(video.currentTime),
        duration: isFiniteNumber(video.duration) ? round3(video.duration) : null,
        paused: !!video.paused,
        ended: !!video.ended,
        readyState: video.readyState,
        errorCode: video.error ? (video.error.code || 0) : null,
        buffered: ranges,
        bufferedContiguous: contiguous
      };
    }
    st.engine.isMSE = callBoolOn(st.core, 'isMSE');
    st.engine.isHls = callBoolOn(st.core, 'isHls');
    st.engine.isLive = callBoolOn(st.core, 'isLive');
    st.engine.durationMs = callNumberOn(st.core, 'getDuration');
    st.engine.currentTimeRaw = readRawCurrentTime(st.core);
    var snap = {
      at: now(),
      elapsedMs: st.startedAt ? now() - st.startedAt : 0,
      media: media,
      engine: {
        isMSE: st.engine.isMSE,
        isHls: st.engine.isHls,
        isLive: st.engine.isLive,
        durationMs: st.engine.durationMs,
        currentTimeRaw: st.engine.currentTimeRaw
      },
      snapshot: {
        status: st.snapshot.status,
        observedAt: st.snapshot.observedAt,
        error: st.snapshot.error,
        aggregate: readRawAggregate(st.snapshot.latest)
      },
      workers: {
        created: st.workers.created,
        terminated: st.workers.terminated,
        live: st.workers.live
      }
    };
    st.lastSample = snap;
    st.samples.push(snap);
    if (st.samples.length > 240) st.samples.shift();
    renderStatus('sampling (' + st.mode + ', samples=' + st.samples.length + ', snapshot=' + st.snapshot.status + ')');
    return snap;
  }

  function withTimeout(promise, ms, label) {
    return new Promise(function (resolve, reject) {
      var timer = setTimeout(function () {
        var error = new Error('timeout:' + label);
        error.name = 'TimeoutError';
        reject(error);
      }, ms);
      Promise.resolve(promise).then(function (value) {
        clearTimeout(timer);
        resolve(value);
      }, function (error) {
        clearTimeout(timer);
        reject(error);
      });
    });
  }

  function withDeadline(promise, label) {
    var remaining = st.deadline - now();
    if (remaining <= 0) {
      var expired = new Error('deadline exceeded:' + label);
      expired.name = 'TimeoutError';
      return Promise.reject(expired);
    }
    return withTimeout(promise, remaining, label);
  }

  function ensureRunning() {
    if (st.disposedState !== 'idle') {
      var error = new Error('player disposed during start');
      error.name = 'AbortError';
      throw error;
    }
  }

  function findVideoElement() {
    if (!st.container) return null;
    var node = st.container.querySelector('video');
    return node || null;
  }

  // MSE first-frame evidence must come from the real HTMLVideoElement that the
  // vendor player creates. Engine selection (isMSE === true) only means MSE was
  // chosen; it is not proof that a frame was decoded, buffered or presented.
  // Confirmation requires video.readyState >= 2 plus an observed loadeddata
  // event, a presented requestVideoFrameCallback frame or real currentTime
  // progress, all within a bounded wait that cleans up its listeners.
  function observeMSEFirstFrame() {
    var video = findVideoElement();
    var info = {
      mode: 'mse-html-video',
      awaited: true,
      videoFound: !!video,
      waitMs: 0,
      readyState: null,
      hadLoadedData: false,
      frameCallback: null,
      videoErrorCode: null,
      startCurrentTime: null,
      maxCurrentTime: null,
      currentTimeAdvanced: false,
      confirmed: false,
      confirmedBy: [],
      timedOut: false,
      cleanupDone: false,
      error: null
    };
    st.mseFrame = info;
    if (!video) {
      info.timedOut = true;
      info.error = { name: 'MissingVideoElement', message: 'container has no HTMLVideoElement for MSE evidence' };
      return Promise.resolve(info);
    }
    var remaining = st.deadline ? st.deadline - now() : MSE_FRAME_EVIDENCE_WAIT_MS;
    var waitMs = Math.max(1000, Math.min(MSE_FRAME_EVIDENCE_WAIT_MS, remaining));
    info.waitMs = waitMs;
    var startTime = isFiniteNumber(video.currentTime) ? video.currentTime : 0;
    info.startCurrentTime = round3(startTime);
    info.readyState = typeof video.readyState === 'number' ? video.readyState : null;
    return new Promise(function (resolve) {
      var listeners = [];
      var pollTimer = null;
      var deadlineTimer = null;
      var frameCallbackId = null;
      var finished = false;

      function pushEvent(event, extra) {
        var entry = { at: now(), event: event };
        if (extra) Object.assign(entry, extra);
        st.events.push(entry);
        if (st.events.length > 100) st.events.shift();
      }

      function finish() {
        if (finished) return;
        finished = true;
        if (pollTimer) { clearTimeout(pollTimer); pollTimer = null; }
        if (deadlineTimer) { clearTimeout(deadlineTimer); deadlineTimer = null; }
        while (listeners.length) {
          var item = listeners.pop();
          try { item.target.removeEventListener(item.type, item.handler); } catch (_) {}
        }
        if (frameCallbackId != null && typeof video.cancelVideoFrameCallback === 'function') {
          try { video.cancelVideoFrameCallback(frameCallbackId); } catch (_) {}
          frameCallbackId = null;
        }
        info.cleanupDone = true;
        if (!info.confirmed) {
          info.timedOut = true;
          pushEvent('mse-first-frame-incomplete', {
            readyState: info.readyState,
            hadLoadedData: info.hadLoadedData,
            frameCallback: info.frameCallback != null,
            currentTimeAdvanced: info.currentTimeAdvanced,
            waitMs: info.waitMs
          });
        }
        resolve(info);
      }

      function evaluate() {
        if (finished) return;
        info.readyState = typeof video.readyState === 'number' ? video.readyState : null;
        info.videoErrorCode = video.error ? (video.error.code || 0) : null;
        var current = isFiniteNumber(video.currentTime) ? video.currentTime : null;
        if (current != null) info.maxCurrentTime = round3(current);
        if (current != null && current - startTime >= 0.05) info.currentTimeAdvanced = true;
        var hasData = info.readyState != null && info.readyState >= 2;
        if (hasData && (info.hadLoadedData || info.frameCallback != null || info.currentTimeAdvanced)) {
          var confirmedBy = [];
          if (info.hadLoadedData) confirmedBy.push('loadeddata');
          if (info.frameCallback != null) confirmedBy.push('requestVideoFrameCallback');
          if (info.currentTimeAdvanced) confirmedBy.push('currentTimeAdvance');
          info.confirmedBy = confirmedBy;
          info.confirmed = true;
          st.firstFrameRendered = true;
          pushEvent('mse-first-frame-confirmed', {
            readyState: info.readyState,
            confirmedBy: confirmedBy,
            maxCurrentTime: info.maxCurrentTime
          });
          finish();
          return;
        }
        pollTimer = setTimeout(evaluate, 100);
      }

      function addListener(type, handler) {
        try {
          video.addEventListener(type, handler, { passive: true });
          listeners.push({ target: video, type: type, handler: handler });
        } catch (error) {
          info.error = safeError(error);
        }
      }

      function onLoadedData() {
        info.hadLoadedData = true;
        evaluate();
      }

      addListener('loadeddata', onLoadedData);
      addListener('playing', evaluate);
      addListener('timeupdate', evaluate);

      if (typeof video.requestVideoFrameCallback === 'function') {
        try {
          frameCallbackId = video.requestVideoFrameCallback(function (_nowMs, metadata) {
            frameCallbackId = null;
            info.frameCallback = {
              at: now(),
              mediaTime: metadata && isFiniteNumber(metadata.mediaTime) ? round3(metadata.mediaTime) : null,
              presentedFrames: metadata && isFiniteNumber(metadata.presentedFrames) ? metadata.presentedFrames : null,
              width: metadata && isFiniteNumber(metadata.width) ? metadata.width : null,
              height: metadata && isFiniteNumber(metadata.height) ? metadata.height : null
            };
            evaluate();
          });
        } catch (error) {
          info.error = safeError(error);
        }
      }

      deadlineTimer = setTimeout(finish, waitMs);
      evaluate();
    });
  }

  function bindCoreEvents(core) {
    st.events.push({ at: now(), event: 'core-created' });
    if (typeof core.on !== 'function') {
      st.errors.push({ name: 'MissingApi', message: 'core.on' });
      return;
    }
    function push(event, extra) {
      var entry = { at: now(), event: event };
      if (extra) Object.assign(entry, extra);
      st.events.push(entry);
      if (st.events.length > 100) st.events.shift();
    }
    try {
      core.on('error', function (error) {
        var safe = safeError(error);
        st.errors.push(safe);
        push('error', { error: safe });
      });
      core.on('firstVideoRendered', function () {
        st.firstFrameRendered = true;
        push('firstVideoRendered');
        resolveFirstFrame();
      });
      core.on('firstAudioRendered', function () {
        if (callBoolOn(core, 'hasVideo') === false) {
          st.firstFrameRendered = true;
          push('firstAudioRendered');
          resolveFirstFrame();
        }
      });
      core.on('played', function () { push('played'); });
      core.on('paused', function () { push('paused'); });
      core.on('ended', function () { push('ended'); });
      core.on('time', function (ms) {
        var value = Number(ms);
        if (isFiniteNumber(value)) st.maxCoreTimeMs = st.maxCoreTimeMs == null ? value : Math.max(st.maxCoreTimeMs, value);
      });
    } catch (error) {
      st.errors.push(safeError(error));
    }
  }

  function normalizeBudget(input) {
    var budget = Object.assign({}, DEFAULT_BUDGET);
    if (input && typeof input === 'object') {
      Object.keys(DEFAULT_BUDGET).forEach(function (key) {
        var value = Number(input[key]);
        if (isFinite(value) && value > 0) budget[key] = value;
      });
    }
    return budget;
  }

  function queryBudget() {
    var params = new URLSearchParams(location.search);
    var out = {};
    Object.keys(DEFAULT_BUDGET).forEach(function (key) {
      if (params.has(key)) {
        var value = Number(params.get(key));
        if (isFinite(value) && value > 0) out[key] = value;
      }
    });
    return out;
  }

  function gate(id, required, ok, detail) {
    return { id: id, required: !!required, ok: !!ok, detail: detail == null ? null : String(detail) };
  }

  function buildResult() {
    var validation = analyzeSnapshot();
    var disposed = st.disposedState === 'disposed';
    var workersClean = st.workers.created > 0 && st.workers.live === 0 && st.workers.terminated >= st.workers.created;
    var mediaBytes = st.serverStats && isFiniteNumber(st.serverStats.mediaBytesSent) ? st.serverStats.mediaBytesSent : 0;
    var mediaRequests = st.serverStats && isFiniteNumber(st.serverStats.mediaRequests) ? st.serverStats.mediaRequests : 0;
    var dualRequired = st.mode === 'dual';
    var gates = [
      gate('engine-mse', true, st.engine.isMSE === true, 'isMSE=' + String(st.engine.isMSE)),
      gate('load', true, st.loadOk, 'loadOk=' + st.loadOk),
      gate('play', true, st.playOk, 'playOk=' + st.playOk),
      gate('media-served', true, mediaBytes > 0 && mediaRequests > 0, 'bytes=' + mediaBytes + ' requests=' + mediaRequests),
      gate('playback-advance', true, st.maxMediaCurrentTime > 0.05, 'maxCurrentTime=' + round3(st.maxMediaCurrentTime)),
      gate('first-frame-evidence', false, st.firstFrameRendered,
        'firstFrameRendered=' + st.firstFrameRendered +
        ' source=' + String(st.mseFrame ? st.mseFrame.mode : 'vendor-event') +
        ' readyState=' + String(st.mseFrame ? st.mseFrame.readyState : 'n/a') +
        ' confirmedBy=' + String(st.mseFrame ? st.mseFrame.confirmedBy.join(',') : 'n/a')),
      gate('no-errors', true, st.errors.length === 0, 'errors=' + st.errors.length),
      gate('snapshot-api', dualRequired, st.snapshot.status === 'observed', 'status=' + st.snapshot.status),
      gate('snapshot-ledger', dualRequired, validation.ok,
        'status=' + validation.status + ' missing=' + validation.missing.join(',')),
      gate('exact-schema', dualRequired, validation.schema.frozen && validation.schema.exactValidation,
        'frozen=' + validation.schema.frozen + ' exactValidation=' + validation.schema.exactValidation),
      // DOM Worker counts are observational only: libmedia may run pipelines
      // in-thread (USE_THREADS), and a worker created before the probe is not
      // thread evidence. Real RPC evidence must come from the snapshot API.
      gate('worker-created', false, st.workers.created > 0, 'domWorkersCreated=' + st.workers.created),
      gate('worker-cleaned', false, workersClean,
        'domWorkersCreated=' + st.workers.created + ' terminated=' + st.workers.terminated + ' live=' + st.workers.live),
      gate('engine-disposed', dualRequired, disposed, st.disposedState)
    ];
    var byId = {};
    gates.forEach(function (item) { byId[item.id] = item; });
    var coreGateIds = ['engine-mse', 'load', 'play', 'media-served', 'playback-advance', 'no-errors'];
    var coreOk = coreGateIds.every(function (id) { return byId[id].ok; });
    var verdict;
    if (!coreOk) verdict = 'fail';
    else if (st.mode === 'baseline') verdict = 'baseline-sanity-only';
    else if (st.snapshot.status !== 'observed') verdict = 'incomplete';
    else if (validation.status === 'schema-pending') verdict = 'schema-pending';
    else if (validation.hardViolation) verdict = 'fail';
    else if (validation.ok) verdict = 'pass';
    else verdict = 'incomplete';
    // A pass requires the frozen-schema exact validator plus every core gate;
    // it verifies the observed ledger window, never feature completion
    // (complete:false stays honest).
    var dualBudgetVerified = verdict === 'pass';
    return {
      schema: 'eio-dual-v2-browser-fixture/1',
      mode: st.mode,
      verdict: verdict,
      dualBudgetVerified: dualBudgetVerified,
      claims: {
        dualBudgetVerified: dualBudgetVerified,
        budgetPassClaimed: false,
        note: st.mode === 'baseline'
          ? 'Baseline mode is load/play sanity only; it never claims budget compliance.'
          : (dualBudgetVerified
            ? 'Pass means the frozen-schema exact validator held for the observed snapshot window plus all core gates; complete:false stays honest and is not a pass condition.'
            : 'Dual acceptance is fail-closed at schema-pending/incomplete until the frozen RPC schema and exact validator are supplied; raw aggregate.complete/incomplete/active values are evidence, not feature completion.')
      },
      acceptance: {
        state: validation.status,
        dualBudgetVerified: dualBudgetVerified,
        schemaFrozen: validation.schema.frozen,
        exactValidation: validation.schema.exactValidation,
        violations: validation.violations ? validation.violations.slice() : [],
        incompleteReasons: validation.incompleteReasons ?
          validation.incompleteReasons.slice() : [],
        note: validation.status === 'schema-pending'
          ? 'Pending a frozen RPC schema; no regex/field-name/unit inference can create a pass.'
          : null
      },
      budget: st.budget,
      configWarnings: st.configWarnings,
      timeLimitSeconds: st.timeLimitSeconds,
      startedAt: st.startedAt,
      disposedAt: st.disposedAt,
      elapsedMs: st.startedAt ? (st.disposedAt || now()) - st.startedAt : 0,
      loadOk: st.loadOk,
      loadUrl: st.loadUrl,
      loadUrlKind: st.loadUrl ? 'absolute-http-url' : null,
      playOk: st.playOk,
      firstFrameRendered: st.firstFrameRendered,
      maxMediaCurrentTime: round3(st.maxMediaCurrentTime),
      maxCoreTimeMs: st.maxCoreTimeMs,
      firstFrameEvidence: st.mseFrame ? sanitizeSnapshot(st.mseFrame, 0, new WeakSet()) : null,
      playerOptions: {
        mode: st.mode,
        eioDualCachePassed: st.eioDualCacheOptionPassed,
        keys: st.optionKeys.slice()
      },
      engine: {
        created: st.engine.created,
        destroyed: st.engine.destroyed,
        isMSE: st.engine.isMSE,
        isHls: st.engine.isHls,
        isLive: st.engine.isLive,
        durationMs: st.engine.durationMs,
        currentTimeRaw: st.engine.currentTimeRaw
      },
      workers: {
        available: st.workers.available,
        patched: st.workers.patched,
        installedBeforeVendor: st.workers.installedBeforeVendor === true,
        created: st.workers.created,
        terminated: st.workers.terminated,
        live: st.workers.live,
        maxLive: st.workers.maxLive,
        urls: st.workers.urls,
        probeError: st.workers.probeError
      },
      serverStats: st.serverStats,
      snapshot: {
        status: st.snapshot.status,
        missingApi: st.snapshot.missingApi,
        reason: st.snapshot.reason,
        error: st.snapshot.error,
        observedAt: st.snapshot.observedAt,
        latest: st.snapshot.latest,
        history: st.snapshot.history,
        historyCount: st.snapshot.history.length,
        schema: validation.schema,
        aggregate: validation.aggregate,
        validation: validation
      },
      seeks: st.seeks,
      errors: st.errors,
      events: st.events,
      samples: st.samples,
      lastSample: st.lastSample,
      dispose: st.disposeInfo,
      gates: gates,
      requiredGateFailures: gates.filter(function (item) { return item.required && !item.ok; })
        .map(function (item) { return item.id; }),
      informationalGateFailures: gates.filter(function (item) { return !item.required && !item.ok; })
        .map(function (item) { return item.id; }),
      notes: [
        'Only the real vendor avplayer.js and a real AVPlayer are used; no fake adapter, fake SourceBuffer, or fake snapshot is inserted.',
        'Player options: baseline mode never passes the eioDualCache option, so a baseline run cannot be silently rate-limited by the dual-cache flags; only dual mode passes eioDualCache {enabled:true, ...budget}.',
        'MSE first-frame evidence is observed on the real HTMLVideoElement: readyState >= 2 plus an observed loadeddata event, requestVideoFrameCallback frame or real currentTime progress within a bounded, cleaned-up wait; choosing the MSE engine alone is never reported as a rendered frame.',
        'Worker counts come from a construct/terminate proxy installed before /vendor/avplayer.js evaluates; libmedia caches self.Worker at module init. Counts are observational, vendor files are not rewritten, and DOM Worker count is not thread evidence: real in-thread/worker RPC evidence must come from the snapshot API.',
        'core.load receives an absolute HTTP URL built with new URL("/sample.mp4", location.origin).href, matching the real player adapter. The baseline failure passed a relative path, which resolves against blob: inside a libmedia worker and produced openStream ret -2 with zero media requests.',
        'Dynamic webpack chunks and wasm stay under /vendor/ (script src and wasmBaseUrl are same-origin); CSP keeps script/worker/connect/media sources self/blob only and never opens an external origin.',
        'Server byte counts are actual response body bytes written for /sample.mp4, not derived from HUD values.',
        'Dual acceptance is fail-closed: without a frozen RPC schema it reports schema-pending/incomplete and never derives ok from regex, field names, units or nonzero-looking numbers.',
        'Raw aggregate.complete/incomplete/active values plus the full sanitized snapshot and video.buffered ranges are retained as evidence only; they do not imply feature complete.',
        'CSP default-src none with self/blob-only script, worker, connect, media and img sources keeps the page and vendor WASM from external CDNs; COOP/COEP remain enabled.'
      ]
    };
  }

  function dispose(reason) {
    if (st.disposedState === 'disposed') return Promise.resolve(buildResult());
    if (st.disposedState === 'disposing') return st.disposePromise || Promise.resolve(buildResult());
    st.disposedState = 'disposing';
    st.disposePromise = runDispose(reason);
    return st.disposePromise;
  }

  async function runDispose(reason) {
    st.disposeReason = String(reason || 'manual');
    if (st.autoTimer) { clearTimeout(st.autoTimer); st.autoTimer = null; }
    if (st.sampleTimer) { clearInterval(st.sampleTimer); st.sampleTimer = null; }
    var info = { reason: st.disposeReason, destroyedAt: null, destroyError: null, workerState: null };
    try { sample(); } catch (_) {}
    var core = st.core;
    if (core) {
      try {
        if (typeof core.destroy !== 'function') throw new Error('core.destroy missing');
        await withTimeout(Promise.resolve().then(function () { return core.destroy(); }), 4000, 'destroy');
        st.engine.destroyed += 1;
      } catch (error) {
        info.destroyError = safeError(error);
        st.errors.push(safeError(error));
      }
    } else {
      info.destroyError = { name: 'MissingEngine', message: 'core was not created' };
    }
    st.core = null;
    await delay(500);
    await refreshServerStats(true);
    info.workerState = {
      created: st.workers.created,
      terminated: st.workers.terminated,
      live: st.workers.live
    };
    info.destroyedAt = now();
    st.disposedAt = info.destroyedAt;
    st.disposedState = 'disposed';
    st.disposeInfo = info;
    renderStatus('disposed (' + st.disposeReason + ')');
    var result = buildResult();
    var out = document.getElementById('out');
    if (out) {
      out.textContent = JSON.stringify({
        verdict: result.verdict,
        dualBudgetVerified: result.dualBudgetVerified,
        snapshot: result.snapshot.status,
        requiredFailures: result.requiredGateFailures,
        failedGates: result.gates.filter(function (item) { return !item.ok; })
      }, null, 2);
    }
    return result;
  }

  async function start(options) {
    options = options || {};
    if (st.disposedState === 'disposed' || st.disposedState === 'disposing') return buildResult();
    if (st.started || st.starting) return buildResult();
    st.starting = true;
    var params = new URLSearchParams(location.search);
    st.mode = params.get('baseline') === '1' ? 'baseline' : 'dual';
    var requestedLimit = Number(options.timeLimit != null ? options.timeLimit : params.get('timeLimit'));
    if (!isFinite(requestedLimit) || requestedLimit <= 0) requestedLimit = 20;
    st.timeLimitSeconds = clamp(requestedLimit, 2, 120);
    st.budget = normalizeBudget(Object.assign({}, queryBudget(), options.budget || {}));
    if (st.budget.ioBytes + st.budget.demuxBytes + st.budget.mseBytes > st.budget.maxBytes + 1) {
      st.configWarnings.push('layer caps sum exceeds maxBytes; this fixture records it and does not treat it as proof');
    }
    installWorkerProbe();
    st.container = document.getElementById('stage');
    if (st.container) st.container.innerHTML = '';
    st.startedAt = now();
    st.deadline = st.startedAt + st.timeLimitSeconds * 1000;
    st.autoTimer = setTimeout(function () { void dispose('time-limit'); }, st.timeLimitSeconds * 1000);
    resetFirstFrame();
    try {
      if (typeof AVPlayer !== 'function') throw new Error('AVPlayer global missing');
      st.engine.created += 1;
      var preLoadParam = Number(params.get('preLoadTime'));
      var playerOptions = {
        container: st.container,
        wasmBaseUrl: location.origin + '/vendor/wasm',
        enableWebCodecs: true,
        enableHardware: true,
        enableWorker: true,
        checkUseMSE: function () { return true; },
        preLoadTime: isFinite(preLoadParam) && preLoadParam > 0 ? preLoadParam : 2
      };
      // Baseline is the control run: it must not receive the dual-cache option
      // at all, otherwise its accepted flags would still be rate-limited.
      if (st.mode === 'dual') {
        playerOptions.eioDualCache = Object.assign({ enabled: true }, st.budget);
        st.eioDualCacheOptionPassed = true;
      }
      st.optionKeys = Object.keys(playerOptions);
      st.events.push({
        at: now(),
        event: 'player-options',
        mode: st.mode,
        eioDualCachePassed: st.eioDualCacheOptionPassed,
        keys: st.optionKeys.slice()
      });
      st.core = new AVPlayer(playerOptions);
      bindCoreEvents(st.core);
      // Match the real adapter: it normalizes every media URL with
      // new URL(url, location.origin).href before core.load. A relative path
      // resolves against blob: inside libmedia's worker and openStream fails.
      st.loadUrl = new URL('/sample.' + (options.ext || 'mp4'), location.origin).href;
      st.events.push({
        at: now(),
        event: 'load-url',
        url: st.loadUrl,
        kind: 'absolute-http-url'
      });
      await withDeadline(Promise.resolve(st.core.load(st.loadUrl, { ext: options.ext || 'mp4', maxProbeDuration: 2 })), 'load');
      ensureRunning();
      st.loadOk = true;
      await withDeadline(Promise.resolve(st.core.play({ audio: true, video: true, subtitle: true })), 'play');
      ensureRunning();
      st.playOk = true;
      st.engine.durationMs = callNumberOn(st.core, 'getDuration');
      st.engine.isMSE = callBoolOn(st.core, 'isMSE');
      if (st.engine.isMSE === true) {
        // MSE selection is not frame evidence: wait for the real video element.
        await observeMSEFirstFrame();
        ensureRunning();
      } else {
        // Canvas/vendor render path keeps the existing firstVideoRendered event.
        await withDeadline(firstFramePromise, 'first-frame');
        ensureRunning();
      }
      st.started = true;
      st.mediaStartedAt = now();
      await refreshServerStats(true);
      ensureRunning();
      sample();
      st.sampleTimer = setInterval(sample, 250);
      renderStatus('playing (' + st.mode + ')');
      st.starting = false;
      return buildResult();
    } catch (error) {
      st.errors.push(safeError(error));
      st.starting = false;
      renderStatus('start-failed');
      if (st.disposedState !== 'disposed' && st.disposedState !== 'disposing') await dispose('start-failure');
      return buildResult();
    }
  }

  function pause() {
    if (!st.core || st.disposedState !== 'idle') {
      return Promise.resolve({ ok: false, error: { name: 'NotRunning', message: 'player is not running' } });
    }
    return Promise.resolve().then(function () { return st.core.pause(); }).then(function () {
      sample();
      return { ok: true };
    }).catch(function (error) {
      var safe = safeError(error);
      st.errors.push(safe);
      return { ok: false, error: safe };
    });
  }

  function resume() {
    if (!st.core || st.disposedState !== 'idle') {
      return Promise.resolve({ ok: false, error: { name: 'NotRunning', message: 'player is not running' } });
    }
    if (typeof st.core.play !== 'function') {
      return Promise.resolve({ ok: false, error: { name: 'MissingApi', message: 'core.play' } });
    }
    // The real AVPlayer has no resume() method: play() unpauses a PAUSED player
    // (MSE path re-calls video.play()), so resume is a second play() call.
    return withTimeout(Promise.resolve().then(function () {
      return st.core.play({ audio: true, video: true, subtitle: true });
    }), 10000, 'resume-play').then(function () {
      sample();
      return { ok: true, method: 'core.play' };
    }).catch(function (error) {
      var safe = safeError(error);
      st.errors.push(safe);
      return { ok: false, error: safe };
    });
  }

  function seek(seconds) {
    var target = Number(seconds);
    if (!isFinite(target) || target < 0) {
      return Promise.resolve({ ok: false, target: seconds, error: { name: 'RangeError', message: 'seek target must be a non-negative number' } });
    }
    if (!st.core || st.disposedState !== 'idle') {
      return Promise.resolve({ ok: false, target: target, error: { name: 'NotRunning', message: 'player is not running' } });
    }
    var startedAt = now();
    return withDeadline(Promise.resolve(st.core.seek(BigInt(Math.round(target * 1000)))), 'seek').then(function () {
      var raw = readRawCurrentTime(st.core);
      var out = {
        ok: true,
        target: target,
        coreCurrentTimeRaw: raw,
        coreCurrentTimeSeconds: raw == null ? null : raw / 1000,
        elapsedMs: now() - startedAt
      };
      st.seeks.push(out);
      return delay(120).then(function () { sample(); return out; });
    }).catch(function (error) {
      var safe = safeError(error);
      st.errors.push(safe);
      var out = { ok: false, target: target, elapsedMs: now() - startedAt, error: safe };
      st.seeks.push(out);
      return out;
    });
  }

  var fixture = {
    start: start,
    sample: sample,
    pause: pause,
    resume: resume,
    seek: seek,
    dispose: dispose,
    defaults: Object.assign({}, DEFAULT_BUDGET),
    get core() { return st.core; },
    get result() { return buildResult(); }
  };
  Object.defineProperty(window, 'fixture', { value: fixture, configurable: true });

  var startButton = document.getElementById('start');
  if (startButton) {
    startButton.addEventListener('click', function () {
      startButton.disabled = true;
      start({}).catch(function (error) {
        st.errors.push(safeError(error));
        renderStatus('start-failed');
      });
    });
  }
  window.addEventListener('pagehide', function () { void dispose('pagehide'); });

  var params = new URLSearchParams(location.search);
  if (params.get('autorun') === '1') {
    setTimeout(function () { void start({ ext: params.get('ext') || undefined }); }, 0);
  }
})();
</script>
</body>
</html>`;

function scheduleShutdown(reason) {
  if (state.shuttingDown) return;
  state.shuttingDown = true;
  state.shutdownReason = reason;
  if (state.ttlTimer) {
    clearTimeout(state.ttlTimer);
    state.ttlTimer = null;
  }
  log('shutdown requested: ' + reason);
  const server = state.server;
  if (server) {
    try { server.close(); } catch (_) {}
    setTimeout(function () {
      if (typeof server.closeAllConnections === 'function') {
        try { server.closeAllConnections(); } catch (_) {}
      }
    }, 150);
  }
  setTimeout(function () {
    log('final stats: ' + JSON.stringify(state.stats));
    process.exit(0);
  }, 400);
}

const server = http.createServer(function (req, res) {
  try {
    handleRequest(req, res);
  } catch (error) {
    state.stats.rejected += 1;
    log('request failure: ' + safeError(error).message);
    try { sendText(res, 500, 'fixture internal error', req.method); } catch (_) {}
  }
});
server.on('clientError', function (_error, socket) {
  try { socket.end('HTTP/1.1 400 Bad Request\r\nConnection: close\r\n\r\n'); } catch (_) {}
});
server.on('error', function (error) { fail('server error: ' + safeError(error).message); });

server.listen(PORT, HOST, function () {
  state.server = server;
  state.startedAt = Date.now();
  state.port = server.address().port;
  state.ttlTimer = setTimeout(function () { scheduleShutdown('ttl'); }, TTL_MS);
  log('listening http://' + HOST + ':' + state.port + '/');
  log('TTL ' + Math.round(TTL_MS / 1000) + 's; request cap ' + MAX_REQUESTS + '; total byte cap ' + MAX_TOTAL_BYTES);
  log('open /?autorun=1 or call window.fixture.start({budget,timeLimit}) from an isolated browser session');
  log('baseline sanity URL: /?baseline=1 (never claims a budget pass)');
});

process.on('SIGINT', function () { scheduleShutdown('sigint'); });
process.on('SIGTERM', function () { scheduleShutdown('sigterm'); });
process.on('uncaughtException', function (error) {
  console.error('[eio-fixture] uncaught: ' + safeError(error).message);
  process.exit(70);
});
process.on('unhandledRejection', function (error) {
  console.error('[eio-fixture] unhandled rejection: ' + safeError(error).message);
  process.exit(71);
});

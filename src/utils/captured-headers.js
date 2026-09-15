/**
 * Stores real client headers captured during authentication.
 * Captures are isolated per proxy token so one device's identity does not
 * leak into another device's passthrough requests.
 * Also stores per-server "last successful login" headers for passthrough persistence.
 * Persists to data/captured-headers.json for survival across restarts.
 */
const fs = require('fs');
const path = require('path');
const logger = require('./logger');

const CAPTURE_KEYS = [
  'user-agent',
  'x-emby-client', 'x-emby-client-version',
  'x-emby-device-name', 'x-emby-device-id',
  'accept', 'accept-language',
];

const capturedByToken = new Map();
const serverHeaders = new Map(); // serverName → { headers, savedAt }
let captureSequence = 0;
let PERSIST_FILE = null;

function buildCapturedHeaders(reqHeaders = {}) {
  const captured = {};
  for (const key of CAPTURE_KEYS) {
    if (reqHeaders[key]) captured[key] = reqHeaders[key];
  }
  return captured;
}

function getLatestEntry() {
  let latest = null;
  for (const entry of capturedByToken.values()) {
    if (!latest || entry.sequence > latest.sequence) {
      latest = entry;
    }
  }
  return latest;
}

function buildInfo(entry) {
  if (!entry || !entry.headers) return null;
  const captured = entry.headers;
  return {
    userAgent: captured['user-agent'] || null,
    client: captured['x-emby-client'] || null,
    clientVersion: captured['x-emby-client-version'] || null,
    deviceName: captured['x-emby-device-name'] || null,
    deviceId: captured['x-emby-device-id'] || null,
    capturedAt: entry.capturedAt,
  };
}

function load() {
  if (!PERSIST_FILE || !fs.existsSync(PERSIST_FILE)) return;
  try {
    const raw = JSON.parse(fs.readFileSync(PERSIST_FILE, 'utf8'));

    // Load per-token captured headers
    const tokens = raw.tokens || raw; // backward compat: old format was flat object
    if (tokens && typeof tokens === 'object' && !Array.isArray(tokens)) {
      for (const [token, entry] of Object.entries(tokens)) {
        if (token === 'servers') continue; // skip the servers key in old flat format
        if (entry && entry.headers) {
          capturedByToken.set(token, entry);
          if (entry.sequence > captureSequence) captureSequence = entry.sequence;
        }
      }
    }

    // Load per-server success headers
    if (raw.servers && typeof raw.servers === 'object') {
      for (const [name, data] of Object.entries(raw.servers)) {
        if (data && data.headers) {
          serverHeaders.set(name, data);
        }
      }
      logger.info(`Loaded ${serverHeaders.size} server passthrough header(s) from disk`);
    }

    if (capturedByToken.size > 0) {
      logger.info(`Loaded ${capturedByToken.size} captured client header(s) from disk`);
    }
  } catch (e) {
    logger.warn(`Failed to load captured headers: ${e.message}`);
  }
}

function save() {
  if (!PERSIST_FILE) return;
  try {
    const obj = {
      tokens: Object.fromEntries(capturedByToken),
      servers: Object.fromEntries(serverHeaders),
    };
    fs.writeFileSync(PERSIST_FILE, JSON.stringify(obj, null, 2), { encoding: 'utf8', mode: 0o600 });
  } catch (e) {
    logger.warn(`Failed to save captured headers: ${e.message}`);
  }
}

const MAX_CAPTURED = 500;

module.exports = {
  init(dataDir) {
    const dir = dataDir || (fs.existsSync('/app/data') ? '/app/data' : path.resolve(__dirname, '..', '..', 'data'));
    if (!fs.existsSync(dir)) fs.mkdirSync(dir, { recursive: true });
    PERSIST_FILE = path.join(dir, 'captured-headers.json');
    load();
  },
  set(token, reqHeaders) {
    if (!token) return null;
    if (capturedByToken.size >= MAX_CAPTURED && !capturedByToken.has(token)) {
      let oldestKey = null, oldestSeq = Infinity;
      for (const [k, v] of capturedByToken) {
        if (v.sequence < oldestSeq) { oldestSeq = v.sequence; oldestKey = k; }
      }
      if (oldestKey) capturedByToken.delete(oldestKey);
    }
    const entry = {
      headers: buildCapturedHeaders(reqHeaders),
      capturedAt: new Date().toISOString(),
      sequence: ++captureSequence,
    };
    capturedByToken.set(token, entry);
    save();
    return entry.headers;
  },
  get(token) {
    if (!token) return null;
    return capturedByToken.get(token)?.headers || null;
  },
  delete(token) {
    if (!token) return false;
    const deleted = capturedByToken.delete(token);
    if (deleted) save();
    return deleted;
  },
  clear() {
    capturedByToken.clear();
    captureSequence = 0;
    save();
  },
  getLatest() {
    const latest = getLatestEntry();
    return latest ? latest.headers : null;
  },
  getInfo() {
    return buildInfo(getLatestEntry());
  },
  // Per-server success headers
  setServerHeaders(serverName, headers) {
    if (!serverName || !headers) return;
    serverHeaders.set(serverName, { headers: { ...headers }, savedAt: new Date().toISOString() });
    save();
  },
  getServerHeaders(serverName) {
    if (!serverName) return null;
    return serverHeaders.get(serverName)?.headers || null;
  },
};

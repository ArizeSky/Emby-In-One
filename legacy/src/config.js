const fs = require('fs');
const path = require('path');
const yaml = require('js-yaml');
const { v4: uuidv4 } = require('uuid');
const logger = require('./utils/logger');

// Write queue to serialize concurrent saveConfig calls
let writeQueue = Promise.resolve();

let CONFIG_PATH = path.resolve(__dirname, '..', 'config.yaml');

// In Docker, we prefer /app/config/config.yaml for easier volume mounting
const DOCKER_CONFIG_PATH = '/app/config/config.yaml';
if (fs.existsSync(DOCKER_CONFIG_PATH)) {
  CONFIG_PATH = DOCKER_CONFIG_PATH;
} else {
  // Check if we are in a directory structure that has config/config.yaml locally
  const localConfigDir = path.resolve(__dirname, '..', 'config', 'config.yaml');
  if (fs.existsSync(localConfigDir)) {
    CONFIG_PATH = localConfigDir;
  }
}

function loadConfig() {
  if (!fs.existsSync(CONFIG_PATH)) {
    logger.error(`Config file not found: ${CONFIG_PATH}`);
    logger.error('Copy config.example.yaml to config.yaml and edit it.');
    process.exit(1);
  }

  const raw = fs.readFileSync(CONFIG_PATH, 'utf8');
  const config = yaml.load(raw, { schema: yaml.JSON_SCHEMA });

  // Validate required fields
  if (!config.admin?.username || !config.admin?.password) {
    logger.error('Config: admin.username and admin.password are required.');
    process.exit(1);
  }

  // Allow starting with no upstream servers
  if (!config.upstream || !Array.isArray(config.upstream)) {
    config.upstream = [];
  }

  // Proxy pool initialization
  if (!config.proxies || !Array.isArray(config.proxies)) {
    config.proxies = [];
  }

  for (let i = 0; i < config.upstream.length; i++) {
    normalizeUpstream(config.upstream[i], i, config);
  }

  // Defaults
  config.server = config.server || {};
  config.server.port = config.server.port || 8096;
  config.server.name = config.server.name || 'Emby In One';

  // Auto-generate server ID if missing
  if (!config.server.id) {
    config.server.id = uuidv4().replace(/-/g, '');
    saveConfig(config);
    logger.info(`Generated server ID: ${config.server.id}`);
  }

  config.playback = config.playback || {};
  config.playback.mode = config.playback.mode || 'proxy';

  // Timeout defaults
  config.timeouts = config.timeouts || {};
  config.timeouts.api = config.timeouts.api || 30000;
  config.timeouts.global = config.timeouts.global || 15000;
  config.timeouts.login = config.timeouts.login || 10000;
  config.timeouts.healthCheck = config.timeouts.healthCheck || 10000;
  config.timeouts.healthInterval = config.timeouts.healthInterval || 60000;

  // 启动时自动迁移明文密码为哈希格式
  if (config.admin.password) {
    const { isHashed, hashPassword } = require('./auth');
    if (!isHashed(config.admin.password)) {
      config.admin.password = hashPassword(config.admin.password);
      saveConfig(config);
      logger.info('Admin password migrated to hashed storage.');
      logger.info('To reset password: edit config.yaml with a new plaintext password and restart, or run: node src/index.js --reset-password <new-password>');
    }
  }

  return config;
}

function normalizeUpstream(s, index, config) {
  if (!s.url) throw new Error(`Upstream server ${index + 1}: url is required`);
  try { new URL(s.url); } catch { throw new Error(`Upstream server ${index + 1}: invalid url "${s.url}"`); }
  if (!s.apiKey && !s.username) {
    throw new Error(`Upstream server ${index + 1}: either apiKey or username is required`);
  }
  s.url = (s.url || '');
  while (s.url.endsWith('/')) s.url = s.url.slice(0, -1);
  s.streamingUrl = s.streamingUrl || null;
  if (s.streamingUrl) {
    while (s.streamingUrl.endsWith('/')) s.streamingUrl = s.streamingUrl.slice(0, -1);
  }
  s.name = s.name || `Server ${index + 1}`;
  s.playbackMode = s.playbackMode || config.playback?.mode || 'proxy';
  s.spoofClient = s.spoofClient || 'none';
  s.followRedirects = s.followRedirects !== undefined ? s.followRedirects : true;
  s.proxyId = s.proxyId || null;
  s.priorityMetadata = s.priorityMetadata || false;
}

function saveConfig(config) {
  // Serialize writes through a promise chain to prevent race conditions
  writeQueue = writeQueue.then(() => {
    try {
      // Build a clean object for saving
      const toSave = {
        server: {
          port: config.server.port,
          name: config.server.name,
          id: config.server.id,
        },
        admin: {
          username: config.admin.username,
          password: config.admin.password,
        },
        playback: {
          mode: config.playback.mode,
        },
        timeouts: config.timeouts || {},
        proxies: config.proxies || [],
        upstream: config.upstream.map(s => {
          const entry = {
            name: s.name,
            url: s.url,
            spoofClient: s.spoofClient || 'none',
            followRedirects: s.followRedirects !== undefined ? s.followRedirects : true,
            proxyId: s.proxyId || null,
            priorityMetadata: s.priorityMetadata || false
          };
          if (s.apiKey) {
            entry.apiKey = s.apiKey;
          } else {
            entry.username = s.username;
            entry.password = s.password;
          }
          if (s.playbackMode && s.playbackMode !== config.playback.mode) {
            entry.playbackMode = s.playbackMode;
          }
          if (s.streamingUrl) {
            entry.streamingUrl = s.streamingUrl;
          }
          return entry;
        }),
      };

      const content = yaml.dump(toSave, { lineWidth: -1 });
      const tmpPath = CONFIG_PATH + '.tmp';
      fs.writeFileSync(tmpPath, content, 'utf8');
      try {
        fs.renameSync(tmpPath, CONFIG_PATH);
      } catch {
        // Windows: renameSync may fail if target exists; fallback to direct write
        fs.writeFileSync(CONFIG_PATH, content, 'utf8');
        try { fs.unlinkSync(tmpPath); } catch {}
      }
    } catch (err) {
      logger.error(`Failed to save config: ${err.message}`);
    }
  });
  return writeQueue;
}

module.exports = { loadConfig, saveConfig, normalizeUpstream, CONFIG_PATH };

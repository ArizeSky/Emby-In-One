const { v4: uuidv4 } = require('uuid');
const crypto = require('crypto');
const fs = require('fs');
const path = require('path');
const logger = require('./utils/logger');
const capturedHeaders = require('./utils/captured-headers');

/**
 * Check if a password string is in hashed format (hex32:hex128).
 */
function isHashed(str) {
  return /^[a-f0-9]{32}:[a-f0-9]{128}$/.test(str);
}

/**
 * Hash a plaintext password using scrypt with a random salt.
 * Returns "salt:hash" hex string.
 */
function hashPassword(plain) {
  const salt = crypto.randomBytes(16).toString('hex');
  const hash = crypto.scryptSync(plain, salt, 64).toString('hex');
  return `${salt}:${hash}`;
}

/**
 * Verify a plaintext password against a "salt:hash" string.
 * Also accepts plaintext stored passwords for migration.
 */
function verifyPassword(plain, stored) {
  if (!stored || !plain) return false;
  if (!isHashed(stored)) {
    return stored === plain;
  }
  const [salt, hash] = stored.split(':');
  const derived = crypto.scryptSync(plain, salt, 64).toString('hex');
  return crypto.timingSafeEqual(Buffer.from(hash, 'hex'), Buffer.from(derived, 'hex'));
}

// Token TTL: 48 hours
const TOKEN_TTL_MS = 48 * 60 * 60 * 1000;

/**
 * Proxy-level authentication manager.
 * Manages the proxy's own tokens (separate from upstream server tokens).
 * Tokens are persisted to disk so they survive restarts.
 */
function createAuthManager(config) {
  let TOKEN_FILE = path.resolve(__dirname, '..', 'data', 'tokens.json');

  const DOCKER_DATA_DIR = '/app/data';
  if (fs.existsSync(DOCKER_DATA_DIR)) {
    TOKEN_FILE = path.join(DOCKER_DATA_DIR, 'tokens.json');
  }

  const tokens = new Map();

  let proxyUserId;
  let savedHadProxyUserId = false;

  try {
    const dir = path.dirname(TOKEN_FILE);
    if (!fs.existsSync(dir)) fs.mkdirSync(dir, { recursive: true });
    if (fs.existsSync(TOKEN_FILE)) {
      const saved = JSON.parse(fs.readFileSync(TOKEN_FILE, 'utf8'));
      if (saved._proxyUserId) {
        proxyUserId = saved._proxyUserId;
        savedHadProxyUserId = true;
      }
      for (const [token, info] of Object.entries(saved)) {
        if (token !== '_proxyUserId') tokens.set(token, info);
      }
      logger.info(`Loaded ${tokens.size} persisted token(s)`);
    }
  } catch (err) {
    logger.warn(`Could not load persisted tokens: ${err.message}`);
  }

  if (!proxyUserId) {
    proxyUserId = uuidv4().replace(/-/g, '');
  }

  function saveTokens() {
    try {
      const obj = { _proxyUserId: proxyUserId };
      const now = Date.now();
      for (const [token, info] of tokens.entries()) {
        if (now - info.createdAt < TOKEN_TTL_MS) {
          obj[token] = info;
        }
      }
      fs.writeFileSync(TOKEN_FILE, JSON.stringify(obj, null, 2), { encoding: 'utf8', mode: 0o600 });
    } catch (err) {
      logger.warn(`Could not save tokens: ${err.message}`);
    }
  }

  if (!savedHadProxyUserId) {
    saveTokens();
  }

  function authenticate(username, password) {
    if (username !== config.admin.username || !verifyPassword(password, config.admin.password)) {
      return null;
    }

    if (!isHashed(config.admin.password)) {
      config.admin.password = hashPassword(password);
      const { saveConfig } = require('./config');
      saveConfig(config);
      logger.info('Admin password migrated to hashed storage');
    }

    const token = uuidv4().replace(/-/g, '');
    tokens.set(token, {
      userId: proxyUserId,
      username,
      createdAt: Date.now(),
    });

    saveTokens();
    logger.info(`User "${username}" authenticated, token=${token.substring(0, 8)}...`);

    return {
      User: buildUserObject(),
      SessionInfo: {
        UserId: proxyUserId,
        UserName: username,
        ServerId: config.server.id,
        Id: uuidv4().replace(/-/g, ''),
        DeviceId: 'proxy',
        DeviceName: 'Proxy Session',
        Client: 'Emby Aggregator',
        ApplicationVersion: '1.0.0',
        SupportsRemoteControl: false,
        PlayableMediaTypes: ['Audio', 'Video'],
        SupportedCommands: [],
      },
      AccessToken: token,
      ServerId: config.server.id,
    };
  }

  /**
   * Validate a token. Returns the token info or null.
   * Tokens expire after TOKEN_TTL_MS (48 hours).
   */
  function validateToken(token) {
    if (!token) return null;
    const info = tokens.get(token);
    if (!info) return null;
    if (Date.now() - info.createdAt >= TOKEN_TTL_MS) {
      tokens.delete(token);
      capturedHeaders.delete(token);
      saveTokens();
      return null;
    }
    return info;
  }

  /**
   * Revoke a token (logout).
   */
  function revokeToken(token) {
    if (!token) return false;
    const deleted = tokens.delete(token);
    if (deleted) {
      capturedHeaders.delete(token);
      saveTokens();
    }
    return deleted;
  }

  function getProxyUserId() {
    return proxyUserId;
  }

  function buildUserObject() {
    return {
      Name: config.admin.username,
      ServerId: config.server.id,
      Id: proxyUserId,
      HasPassword: true,
      HasConfiguredPassword: true,
      HasConfiguredEasyPassword: false,
      EnableAutoLogin: false,
      Policy: {
        IsAdministrator: true,
        IsHidden: false,
        IsDisabled: false,
        EnableUserPreferenceAccess: true,
        EnableContentDownloading: true,
        EnableRemoteAccess: true,
        EnableLiveTvAccess: true,
        EnableLiveTvManagement: true,
        EnableMediaPlayback: true,
        EnableAudioPlaybackTranscoding: true,
        EnableVideoPlaybackTranscoding: true,
        EnablePlaybackRemuxing: true,
        EnableContentDeletion: false,
        EnableSyncTranscoding: true,
        EnableMediaConversion: true,
        EnableAllDevices: true,
        EnableAllChannels: true,
        EnableAllFolders: true,
        EnablePublicSharing: true,
        InvalidLoginAttemptCount: 0,
        RemoteClientBitrateLimit: 0,
      },
      Configuration: {
        PlayDefaultAudioTrack: true,
        DisplayMissingEpisodes: false,
        EnableLocalPassword: false,
        HidePlayedInLatest: true,
        RememberAudioSelections: true,
        RememberSubtitleSelections: true,
        EnableNextEpisodeAutoPlay: true,
      },
    };
  }

  return {
    authenticate,
    validateToken,
    revokeToken,
    getProxyUserId,
    buildUserObject,
    hashPassword,
    verifyPassword,
  };
}

module.exports = { createAuthManager, hashPassword, verifyPassword, isHashed };
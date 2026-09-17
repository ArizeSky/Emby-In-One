const logger = require('../utils/logger');

/**
 * Middleware that extracts and validates the proxy auth token.
 * Injects req.proxyUser if authenticated.
 *
 * Some routes (like /System/Info/Public) don't require auth.
 */
function createAuthMiddleware(authManager) {
  return function authMiddleware(req, res, next) {
    // Extract token from various sources
    let token = null;
    let tokenSource = null;

    // 1. X-Emby-Token header
    token = req.headers['x-emby-token'];
    if (token) tokenSource = 'X-Emby-Token header';

    // 2. api_key query parameter
    if (!token) {
      token = req.query.api_key || req.query.ApiKey;
      if (token) tokenSource = 'api_key query';
    }

    // 3. Authorization header: Emby/MediaBrowser ... Token="xxx" or Token=xxx
    if (!token && req.headers['x-emby-authorization']) {
      const auth = req.headers['x-emby-authorization'];
      const match = auth.match(/Token="([^"]+)"/) || auth.match(/Token=([^,\s]+)/);
      if (match) { token = match[1]; tokenSource = 'X-Emby-Authorization header'; }
    }

    // 4. Standard Authorization header: MediaBrowser Token="xxx"
    if (!token && req.headers['authorization']) {
      const auth = req.headers['authorization'];
      const match = auth.match(/Token="([^"]+)"/) || auth.match(/Token=([^,\s]+)/);
      if (match) { token = match[1]; tokenSource = 'Authorization header'; }
    }

    if (token) {
      const userInfo = authManager.validateToken(token);
      if (userInfo) {
        req.proxyUser = userInfo;
        req.proxyToken = token;
        logger.debug(`Auth OK via ${tokenSource} path=${req.path}`);
      } else {
        logger.debug(`Token invalid via ${tokenSource} path=${req.path}`);
      }
    }

    next();
  };
}

/**
 * Middleware that requires authentication. Returns 401 if not authenticated.
 */
function requireAuth(req, res, next) {
  if (!req.proxyUser) {
    return res.status(401).json({
      message: 'Authentication required',
    });
  }
  next();
}

module.exports = { createAuthMiddleware, requireAuth };

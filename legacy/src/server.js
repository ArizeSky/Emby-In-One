const path = require('path');
const express = require('express');
const logger = require('./utils/logger');
const requestStore = require('./utils/request-store');
const { applyCorsHeaders } = require('./utils/cors-policy');

// Optional gzip compression — install with: npm install compression
let compression;
try { compression = require('compression'); } catch (_) { /* not installed, skip */ }

const { createAuthManager } = require('./auth');
const { createAuthMiddleware } = require('./middleware/auth-middleware');
const { createRequestContext } = require('./middleware/request-context');
const { createSystemRoutes } = require('./routes/system');
const { createUserRoutes } = require('./routes/users');
const { createItemRoutes } = require('./routes/items');
const { createLibraryRoutes } = require('./routes/library');
const { createPlaybackRoutes } = require('./routes/playback');
const { createSessionRoutes } = require('./routes/sessions');
const { createStreamingRoutes } = require('./routes/streaming');
const { createImageRoutes } = require('./routes/images');
const { createFallbackRoutes } = require('./routes/fallback');
const { createAdminRoutes } = require('./routes/admin');

function createApp(config, idManager, upstreamManager) {
  const app = express();
  const authManager = createAuthManager(config);

  // Store authManager on app for fallback route access
  app.set('authManager', authManager);

  // --- Global Middleware ---

  // Gzip compression (reduces JSON response sizes by ~70%)
  if (compression) {
    app.use(compression({ threshold: 1024 }));
    logger.info('Response compression enabled');
  }

  // CORS — admin API is same-origin only, Emby client routes stay permissive
  app.use((req, res, next) => {
    applyCorsHeaders(req, res);
    if (req.method === 'OPTIONS') {
      return res.status(200).end();
    }
    next();
  });

  // JSON body parser
  app.use(express.json({ limit: '10mb' }));

  // Auth middleware (extracts token, doesn't require it)
  app.use(createAuthMiddleware(authManager));

  // Store request-scoped client context for passthrough mode
  app.use((req, res, next) => {
    requestStore.run({
      headers: req.headers,
      proxyToken: req.proxyToken || null,
    }, () => next());
  });

  // Request context (ID resolution helpers)
  app.use(createRequestContext(idManager, upstreamManager));

  // Request logging
  app.use((req, res, next) => {
    const start = Date.now();
    const tokenSource = req.headers['x-emby-token'] ? 'X-Emby-Token'
      : (req.query.api_key || req.query.ApiKey) ? 'api_key'
      : req.headers['x-emby-authorization'] ? 'X-Emby-Authorization'
      : req.headers['authorization'] ? 'Authorization'
      : 'none';

    res.on('finish', () => {
      const ms = Date.now() - start;
      const level = res.statusCode >= 500 ? 'error' : res.statusCode >= 400 ? 'warn' : 'debug';
      logger[level](`${req.method} ${req.path} → ${res.statusCode} (${ms}ms) [auth:${tokenSource}]${
        res.statusCode === 401 ? ` headers=${JSON.stringify(Object.keys(req.headers))}` : ''
      }`);
    });

    logger.debug(`→ ${req.method} ${req.path} [auth:${tokenSource}]`);
    next();
  });

  // --- Routes (order matters: specific before generic) ---

  // Admin panel: static files + API (with security headers)
  app.use('/admin', (req, res, next) => {
    res.setHeader('X-Content-Type-Options', 'nosniff');
    res.setHeader('X-Frame-Options', 'DENY');
    res.setHeader('X-XSS-Protection', '1; mode=block');
    next();
  });
  app.use('/admin', express.static(path.resolve(__dirname, '..', 'public')));
  app.get('/admin', (req, res) => res.redirect('/admin/admin.html'));
  app.use('/admin', createAdminRoutes(config, idManager, upstreamManager, authManager));

  // Favicon: return 204 silently (browsers auto-request, no need to log 401)
  app.get('/favicon.ico', (req, res) => res.status(204).end());

  // System info (some endpoints don't need auth)
  app.use('/', createSystemRoutes(config));

  // User auth and info
  app.use('/', createUserRoutes(config, authManager, idManager, upstreamManager));

  // Images (before items to avoid conflicts, unauthenticated per Emby convention, virtual ID check provides access control)
  app.use('/', createImageRoutes(config, idManager, upstreamManager));

  // Streaming (before items for /Videos/:id/stream)
  app.use('/', createStreamingRoutes(config, idManager, upstreamManager));

  // Playback info and session reporting
  app.use('/', createPlaybackRoutes(config, authManager, idManager, upstreamManager));
  app.use('/', createSessionRoutes(config, authManager, idManager, upstreamManager));

  // Library routes (Shows, Search, Genres, etc.)
  app.use('/', createLibraryRoutes(config, authManager, idManager, upstreamManager));

  // Items routes
  app.use('/', createItemRoutes(config, authManager, idManager, upstreamManager));

  // Emby web client may request with /emby prefix
  app.use('/emby', createSystemRoutes(config));
  app.use('/emby', createUserRoutes(config, authManager, idManager, upstreamManager));
  app.use('/emby', createImageRoutes(config, idManager, upstreamManager));
  app.use('/emby', createStreamingRoutes(config, idManager, upstreamManager));
  app.use('/emby', createPlaybackRoutes(config, authManager, idManager, upstreamManager));
  app.use('/emby', createSessionRoutes(config, authManager, idManager, upstreamManager));
  app.use('/emby', createLibraryRoutes(config, authManager, idManager, upstreamManager));
  app.use('/emby', createItemRoutes(config, authManager, idManager, upstreamManager));

  // Root path: show error page (not an Emby client endpoint)
  app.get('/', (req, res) => {
    res.status(200).send(`<!DOCTYPE html><html lang="zh-CN"><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Emby-in-One</title><style>body{font-family:sans-serif;background:#f8fafc;display:flex;align-items:center;justify-content:center;min-height:100vh;margin:0}.box{background:#fff;border:1px solid #e2e8f0;border-radius:12px;padding:2.5rem 3rem;text-align:center;max-width:400px}.icon{font-size:3rem;margin-bottom:1rem}.title{font-size:1.4rem;font-weight:700;color:#1e293b;margin-bottom:.5rem}.sub{color:#64748b;font-size:.95rem;margin-bottom:1.5rem}.btn{display:inline-block;background:#2563eb;color:#fff;padding:.6rem 1.4rem;border-radius:8px;text-decoration:none;font-weight:600;font-size:.9rem}</style></head><body><div class="box"><div class="icon">⛔</div><div class="title">此地址仅供 Emby 客户端使用</div><div class="sub">请将 Emby 客户端连接地址设置为本地址。<br>管理面板请访问 <code>/admin</code> 路径。</div><a class="btn" href="/admin">前往管理面板</a></div></body></html>`);
  });

  // Fallback: generic proxy for unhandled routes
  app.use('/', createFallbackRoutes(config, authManager, idManager, upstreamManager));

  // Error handler
  app.use((err, req, res, _next) => {
    logger.error(`Unhandled error: ${err.message}`, { stack: err.stack });
    if (!res.headersSent) {
      res.status(500).json({ message: 'Internal server error' });
    }
  });

  // Start health checks
  upstreamManager.startHealthChecks(config.timeouts && config.timeouts.healthInterval);

  return app;
}

module.exports = { createApp };
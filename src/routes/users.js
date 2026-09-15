const { Router } = require('express');
const rateLimit = require('express-rate-limit');
const { requireAuth } = require('../middleware/auth-middleware');
const { rewriteResponseIds } = require('../utils/id-rewriter');
const capturedHeaders = require('../utils/captured-headers');
const logger = require('../utils/logger');

const loginLimiter = rateLimit({
  windowMs: 15 * 60 * 1000,
  max: 10,
  skipSuccessfulRequests: true,
  standardHeaders: true,
  legacyHeaders: false,
  message: { message: 'Too many login attempts, please try again later' },
});

function createUserRoutes(config, authManager, idManager, upstreamManager) {
  const router = Router();

  // POST /Users/AuthenticateByName
  router.post('/Users/AuthenticateByName', loginLimiter, (req, res) => {
    const { Username, Pw, Password } = req.body;
    const password = Pw || Password || '';

    const clientInfo = {
      userAgent: req.headers['user-agent'],
      client: req.headers['x-emby-client'],
      device: req.headers['x-emby-device-name'],
      deviceId: req.headers['x-emby-device-id'],
      version: req.headers['x-emby-client-version'],
    };
    logger.info(`Login attempt: user="${Username}" client="${clientInfo.client || 'unknown'}" device="${clientInfo.device || 'unknown'}" ip=${req.ip}`);
    logger.debug(`Login headers: UA="${clientInfo.userAgent}" DeviceId="${clientInfo.deviceId}" Version="${clientInfo.version}"`);

    const result = authManager.authenticate(Username, password);
    if (!result) {
      logger.warn(`Login failed: user="${Username}" ip=${req.ip} client="${clientInfo.client || 'unknown'}"`);
      return res.status(401).json({ message: 'Invalid username or password' });
    }

    logger.info(`Login success: user="${Username}" ip=${req.ip}`);

    capturedHeaders.set(result.AccessToken, req.headers);
    const ua = req.headers['user-agent'] || 'unknown';
    const client = req.headers['x-emby-client'] || '';
    logger.info(`Captured client headers for token ${result.AccessToken.substring(0, 8)}...: ${client ? client + ' / ' : ''}${ua}`);

    const justCaptured = capturedHeaders.get(result.AccessToken);
    for (const c of upstreamManager.clients) {
      if (!c.online && c.config.spoofClient === 'passthrough') {
        logger.info(`[${c.name}] Re-trying login with captured client headers...`);
        c.login(justCaptured).catch((err) => { logger.debug(`[${c.name}] Passthrough login retry failed: ${err.message}`); });
      }
    }

    res.json(result);
  });

  // GET /Users/Public
  router.get('/Users/Public', (req, res) => {
    res.json([{
      Name: config.admin.username,
      ServerId: config.server.id,
      Id: authManager.getProxyUserId(),
      HasPassword: true,
      HasConfiguredPassword: true,
      HasConfiguredEasyPassword: false,
      PrimaryImageTag: undefined,
    }]);
  });

  // GET /Users/:userId
  router.get('/Users/:userId', requireAuth, (req, res) => {
    res.json(authManager.buildUserObject());
  });

  // GET /Users/:userId/Views — merge views from all upstream servers
  router.get('/Users/:userId/Views', requireAuth, async (req, res) => {
    try {
      const onlineClients = upstreamManager.getOnlineClients();
      const results = await Promise.allSettled(
        onlineClients.map(async (client) => {
          const data = await client.request('GET', `/Users/${client.userId}/Views`);
          return { serverIndex: client.serverIndex, data };
        })
      );

      for (let i = 0; i < results.length; i++) {
        if (results[i].status === 'rejected') {
          const client = onlineClients[i];
          logger.warn(`[${client.name}] Views failed: ${results[i].reason?.message || 'unknown'}`);
        }
      }

      const successResults = results
        .filter(r => r.status === 'fulfilled')
        .map(r => r.value);

      const allViews = [];
      for (const { serverIndex, data } of successResults) {
        const items = data.Items || [];
        for (const item of items) {
          const rewritten = rewriteResponseIds(
            JSON.parse(JSON.stringify(item)),
            serverIndex, idManager, config.server.id, authManager.getProxyUserId()
          );
          const sourceClient = upstreamManager.getClient(serverIndex);
          if (sourceClient && onlineClients.length > 1) {
            rewritten.Name = `${rewritten.Name} (${sourceClient.name})`;
          }
          allViews.push(rewritten);
        }
      }

      res.json({
        Items: allViews,
        TotalRecordCount: allViews.length,
        StartIndex: 0,
      });
    } catch (err) {
      logger.error(`Error fetching views: ${err.message}`);
      res.status(500).json({ message: 'Failed to fetch views' });
    }
  });

  // GET /Users/:userId/GroupingOptions
  router.get('/Users/:userId/GroupingOptions', requireAuth, (req, res) => {
    res.json([]);
  });

  // POST /Users/:userId/Configuration
  router.post('/Users/:userId/Configuration', requireAuth, (req, res) => {
    res.status(204).end();
  });

  // POST /Users/:userId/Policy
  router.post('/Users/:userId/Policy', requireAuth, (req, res) => {
    res.status(204).end();
  });

  return router;
}

module.exports = { createUserRoutes };
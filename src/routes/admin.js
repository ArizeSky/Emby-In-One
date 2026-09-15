const { Router } = require('express');
const { v4: uuidv4 } = require('uuid');
const fs = require('fs');
const rateLimit = require('express-rate-limit');
const { requireAuth } = require('../middleware/auth-middleware');
const { saveConfig, normalizeUpstream } = require('../config');
const { EmbyClient } = require('../emby-client');
const logger = require('../utils/logger');
const { getLogFilePath } = require('../utils/logger');
const capturedHeaders = require('../utils/captured-headers');
const { hashPassword, verifyPassword } = require('../auth');

const adminLimiter = rateLimit({
  windowMs: 60 * 1000,
  max: 60,
  standardHeaders: true,
  legacyHeaders: false,
  message: { error: 'Too many requests' },
});

function createAdminRoutes(config, idManager, upstreamManager, authManager) {
  const router = Router();

  router.use(requireAuth);
  router.use(adminLimiter);

  function parseIndex(req, res) {
    const index = parseInt(req.params.index, 10);
    if (isNaN(index) || index < 0 || index >= config.upstream.length) {
      res.status(404).json({ error: 'Invalid server index' });
      return -1;
    }
    return index;
  }

  function assertValidUpstreamUrl(url) {
    if (url && !/^https?:\/\//i.test(url)) {
      throw new Error('URL must start with http:// or https://');
    }
  }

  async function createValidatedClient(serverConfig, index) {
    const draft = { ...serverConfig };
    normalizeUpstream(draft, index, config);
    assertValidUpstreamUrl(draft.url);
    if (draft.streamingUrl) assertValidUpstreamUrl(draft.streamingUrl);

    const client = new EmbyClient(draft, index, config.proxies || [], config.timeouts || {});
    await client.login();

    if (!client.online && draft.spoofClient !== 'passthrough') {
      throw new Error('Upstream validation failed');
    }
    if (!client.online && draft.spoofClient === 'passthrough') {
      logger.warn(`[${draft.name}] Passthrough server saved but offline — will retry when client headers are available`);
    }

    return { draft, client };
  }

  router.post('/api/logout', (req, res) => {
    const token = req.headers['x-emby-token'] ||
      req.query.api_key || req.query.ApiKey || null;
    if (token && authManager) {
      authManager.revokeToken(token);
    }
    res.json({ success: true });
  });

  router.get('/api/client-info', (req, res) => {
    res.json(capturedHeaders.getInfo());
  });

  router.get('/api/status', (req, res) => {
    const clients = upstreamManager.clients;
    res.json({
      serverName: config.server.name,
      serverId: config.server.id,
      port: config.server.port,
      playbackMode: config.playback.mode,
      idMappings: idManager.getStats(),
      upstreamCount: clients.length,
      upstreamOnline: clients.filter(c => c.online).length,
      upstream: clients.map(c => ({
        index: c.serverIndex,
        name: c.name,
        online: c.online,
        playbackMode: config.upstream[c.serverIndex]?.playbackMode || config.playback.mode,
      })),
    });
  });

  router.get('/api/upstream', (req, res) => {
    const list = config.upstream.map((s, index) => {
      let safeUrl = s.url;
      try { const u = new URL(s.url); safeUrl = u.origin + u.pathname + u.search; } catch {}
      return {
        index,
        name: s.name,
        url: safeUrl,
        username: s.username,
        authType: s.apiKey ? 'apiKey' : 'password',
        online: upstreamManager.getClient(index)?.online || false,
        playbackMode: s.playbackMode || config.playback.mode,
        spoofClient: s.spoofClient || 'none',
        followRedirects: s.followRedirects !== false,
        proxyId: s.proxyId || null,
        priorityMetadata: s.priorityMetadata || false,
      };
    });
    res.json(list);
  });

  router.post('/api/upstream', async (req, res) => {
    try {
      const { name, url, username, password, apiKey, playbackMode, spoofClient, followRedirects, proxyId, priorityMetadata, streamingUrl } = req.body;
      const index = config.upstream.length;
      const { draft, client } = await createValidatedClient({
        name,
        url,
        username,
        password,
        apiKey,
        playbackMode,
        spoofClient,
        followRedirects,
        proxyId,
        priorityMetadata,
        streamingUrl,
      }, index);

      config.upstream.push(draft);
      upstreamManager.clients.push(client);
      saveConfig(config);
      res.json({ success: true, index, name: client.name, online: client.online });
    } catch (err) {
      if (err.message === 'URL must start with http:// or https://') {
        return res.status(400).json({ error: err.message });
      }
      res.status(500).json({ error: err.message });
    }
  });

  router.put('/api/upstream/:index', async (req, res) => {
    try {
      const index = parseIndex(req, res);
      if (index === -1) return;
      const body = req.body;
      const currentConfig = config.upstream[index];
      const draft = { ...currentConfig };
      const allowedKeys = ['name', 'url', 'username', 'password', 'apiKey', 'playbackMode', 'spoofClient', 'followRedirects', 'proxyId', 'priorityMetadata', 'streamingUrl'];
      for (const key of allowedKeys) {
        if (body[key] !== undefined) {
          if ((key === 'password' || key === 'apiKey') && body[key] === '') continue;
          draft[key] = body[key];
        }
      }

      const validated = await createValidatedClient(draft, index);
      config.upstream[index] = validated.draft;
      upstreamManager.clients[index] = validated.client;
      saveConfig(config);
      res.json({ success: true, index, name: validated.client.name, online: validated.client.online });
    } catch (err) {
      if (err.message === 'URL must start with http:// or https://') {
        return res.status(400).json({ error: err.message });
      }
      res.status(500).json({ error: err.message });
    }
  });

  router.post('/api/upstream/reorder', (req, res) => {
    const { fromIndex, toIndex } = req.body;
    if (typeof fromIndex !== 'number' || typeof toIndex !== 'number' ||
        !Number.isInteger(fromIndex) || !Number.isInteger(toIndex)) {
      return res.status(400).json({ error: 'fromIndex and toIndex must be integers' });
    }
    if (fromIndex < 0 || fromIndex >= config.upstream.length || toIndex < 0 || toIndex >= config.upstream.length) {
      return res.status(400).json({ error: 'Index out of bounds' });
    }
    const item = config.upstream.splice(fromIndex, 1)[0];
    config.upstream.splice(toIndex, 0, item);
    const clientItem = upstreamManager.clients.splice(fromIndex, 1)[0];
    upstreamManager.clients.splice(toIndex, 0, clientItem);
    upstreamManager.clients.forEach((c, i) => c.serverIndex = i);
    saveConfig(config);
    res.json({ success: true });
  });

  router.delete('/api/upstream/:index', (req, res) => {
    const index = parseIndex(req, res);
    if (index === -1) return;
    const name = config.upstream[index].name;
    config.upstream.splice(index, 1);
    upstreamManager.clients.splice(index, 1);
    idManager.removeByServerIndex(index);
    idManager.shiftServerIndices(index);
    upstreamManager.clients.forEach((c, i) => c.serverIndex = i);
    saveConfig(config);
    logger.info(`Upstream server "${name}" (index ${index}) deleted`);
    res.json({ success: true });
  });

  router.post('/api/upstream/:index/reconnect', async (req, res) => {
    const index = parseIndex(req, res);
    if (index === -1) return;
    const client = upstreamManager.clients[index];
    if (client) await client.login();
    res.json({ success: true, online: client?.online });
  });

  router.get('/api/proxies', (req, res) => {
    const sanitized = (config.proxies || []).map(p => {
      let safeUrl = p.url;
      try {
        const u = new URL(p.url);
        if (u.username || u.password) { u.password = '****'; safeUrl = u.toString(); }
      } catch {}
      return { ...p, url: safeUrl };
    });
    res.json(sanitized);
  });

  router.post('/api/proxies', (req, res) => {
    const { url, name } = req.body;
    if (!url) return res.status(400).json({ error: 'url is required' });
    try {
      const parsed = new URL(url);
      if (!['http:', 'https:'].includes(parsed.protocol)) {
        return res.status(400).json({ error: 'Only http/https protocols allowed' });
      }
    } catch {
      return res.status(400).json({ error: 'Invalid URL format' });
    }
    const newProxy = { id: uuidv4().replace(/-/g, ''), name: name || 'Proxy', url };
    config.proxies = config.proxies || [];
    config.proxies.push(newProxy);
    saveConfig(config);
    res.json(newProxy);
  });

  router.delete('/api/proxies/:id', (req, res) => {
    const { id } = req.params;
    config.proxies = (config.proxies || []).filter(p => p.id !== id);
    saveConfig(config);
    res.status(204).end();
  });

  router.get('/api/settings', (req, res) => {
    res.json({
      serverName: config.server.name,
      port: config.server.port,
      playbackMode: config.playback.mode,
      adminUsername: config.admin.username,
      timeouts: config.timeouts || {},
    });
  });

  router.put('/api/settings', (req, res) => {
    const { serverName, playbackMode, adminUsername, adminPassword, currentPassword, timeouts } = req.body;
    if (serverName !== undefined) {
      if (typeof serverName !== 'string' || serverName.length < 1 || serverName.length > 100) {
        return res.status(400).json({ error: 'Invalid server name' });
      }
      config.server.name = serverName;
    }
    if (playbackMode !== undefined) {
      if (!['proxy', 'redirect'].includes(playbackMode)) {
        return res.status(400).json({ error: 'playbackMode must be "proxy" or "redirect"' });
      }
      config.playback.mode = playbackMode;
    }
    if (adminUsername !== undefined) {
      if (typeof adminUsername !== 'string' || adminUsername.length < 1 || adminUsername.length > 50) {
        return res.status(400).json({ error: 'Invalid admin username' });
      }
      config.admin.username = adminUsername;
    }
    if (adminPassword !== undefined && adminPassword !== '') {
      if (!currentPassword || !verifyPassword(currentPassword, config.admin.password)) {
        return res.status(403).json({ error: 'Current password is incorrect' });
      }
      config.admin.password = hashPassword(adminPassword);
    }
    if (timeouts && typeof timeouts === 'object') {
      config.timeouts = config.timeouts || {};
      for (const key of ['api', 'global', 'login', 'healthCheck', 'healthInterval']) {
        if (timeouts[key] !== undefined) {
          const val = parseInt(timeouts[key]);
          if (!isNaN(val) && val > 0) config.timeouts[key] = val;
        }
      }
    }
    saveConfig(config);
    res.json({ success: true });
  });

  const logBuffer = [];
  const MAX_LOGS = 500;
  const { format } = require('winston');
  const Transport = require('winston-transport');

  class BufferTransport extends Transport {
    log(info, callback) {
      logBuffer.push({
        timestamp: info.timestamp || new Date().toISOString(),
        level: info.level,
        message: info.message,
      });
      if (logBuffer.length > MAX_LOGS) logBuffer.shift();
      callback();
    }
  }

  if (!logger.transports.some(t => t.constructor.name === 'BufferTransport')) {
    logger.add(new BufferTransport({
      format: format.combine(format.timestamp(), format.simple()),
    }));
  }

  router.get('/api/logs', (req, res) => {
    const limit = parseInt(req.query.limit) || 100;
    res.json(logBuffer.slice(-limit));
  });

  router.get('/api/logs/download', (req, res) => {
    const logFile = getLogFilePath();
    if (!logFile) {
      return res.status(404).json({ error: 'Log file not found' });
    }
    const candidates = [logFile, logFile.replace(/\.log$/, '1.log')];
    const actualFile = candidates.find(f => fs.existsSync(f));
    if (!actualFile) {
      return res.status(404).json({ error: 'Log file not found' });
    }
    res.setHeader('Content-Type', 'text/plain; charset=utf-8');
    res.setHeader('Content-Disposition', 'attachment; filename="emby-in-one.log"');
    fs.createReadStream(actualFile).pipe(res);
  });

  router.delete('/api/logs', (req, res) => {
    const logFile = getLogFilePath();
    if (logFile && fs.existsSync(logFile)) {
      fs.writeFileSync(logFile, '', 'utf8');
      logger.info('Log file cleared by admin');
    }
    logBuffer.length = 0;
    res.json({ success: true });
  });

  return router;
}

module.exports = { createAdminRoutes };
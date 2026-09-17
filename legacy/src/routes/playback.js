const { Router } = require('express');
const { requireAuth } = require('../middleware/auth-middleware');
const { rewriteResponseIds } = require('../utils/id-rewriter');
const logger = require('../utils/logger');

function createPlaybackRoutes(config, authManager, idManager, upstreamManager) {
  const router = Router();

  // PlaySession mapping: playSessionVirtualId → { serverIndex, originalPlaySessionId, createdAt }
  const playSessions = new Map();
  const PLAY_SESSION_TTL_MS = 4 * 60 * 60 * 1000; // 4 hours

  function registerPlaySession(virtualPlaySessionId, serverIndex, originalPlaySessionId) {
    playSessions.set(virtualPlaySessionId, { serverIndex, originalPlaySessionId, createdAt: Date.now() });
  }

  function cleanupExpiredSessions() {
    const now = Date.now();
    for (const [id, entry] of playSessions.entries()) {
      if (now - entry.createdAt > PLAY_SESSION_TTL_MS) playSessions.delete(id);
    }
  }

  // Proactive cleanup every 30 minutes
  const cleanupInterval = setInterval(cleanupExpiredSessions, 30 * 60 * 1000);
  cleanupInterval.unref(); // Don't prevent process exit

  // GET /Items/:itemId/PlaybackInfo
  router.get('/Items/:itemId/PlaybackInfo', requireAuth, async (req, res) => {
    await handlePlaybackInfo(req, res);
  });

  // POST /Items/:itemId/PlaybackInfo
  router.post('/Items/:itemId/PlaybackInfo', requireAuth, async (req, res) => {
    await handlePlaybackInfo(req, res);
  });

  async function handlePlaybackInfo(req, res) {
    try {
      const virtualItemId = req.params.itemId;
      const resolved = req.resolveId(virtualItemId);
      if (!resolved) {
        return res.status(404).json({ message: 'Item not found' });
      }

      // Gather all instances (primary + secondary from merging)
      const instances = [
        { originalId: resolved.originalId, serverIndex: resolved.serverIndex, client: resolved.client },
        ...(resolved.otherInstances || []).map(inst => ({
          ...inst,
          client: upstreamManager.getClient(inst.serverIndex)
        }))
      ].filter(inst => inst.client && inst.client.online);

      const body = req.method === 'POST' ? { ...req.body } : {};
      const query = { ...req.query };

      if (body.MediaSourceId) {
        const msResolved = idManager.resolveVirtualId(body.MediaSourceId);
        if (msResolved) body.MediaSourceId = msResolved.originalId;
      }
      if (query.MediaSourceId) {
        const msResolved = idManager.resolveVirtualId(query.MediaSourceId);
        if (msResolved) query.MediaSourceId = msResolved.originalId;
      }

      // Fetch PlaybackInfo from all servers in parallel (longer timeout for PlaybackInfo)
      const playbackTimeout = (config.timeouts && config.timeouts.api) || 30000;
      const results = await Promise.allSettled(
        instances.map(async (inst) => {
          const params = { ...query, UserId: inst.client.userId };
          const path = `/Items/${inst.originalId}/PlaybackInfo`;
          const data = await inst.client.request(req.method, path, {
            params,
            data: req.method === 'POST' ? body : undefined,
            timeout: playbackTimeout,
          });
          return { ...inst, data };
        })
      );

      const successResults = results
        .filter(r => r.status === 'fulfilled')
        .map(r => r.value);

      if (successResults.length === 0) {
        return res.status(502).json({ message: 'Failed to fetch playback info from upstream' });
      }

      // Deep clone the base result — critical for correct stream URL rewriting
      const baseResult = successResults[0];
      const cloned = JSON.parse(JSON.stringify(baseResult.data));
      
      // Collect ALL MediaSources from all successful results
      const allMediaSources = [];
      for (const r of successResults) {
        const instMediaSources = r.data.MediaSources || [];
        for (const ms of instMediaSources) {
          // Process and rewrite each MediaSource for the proxy
          const originalMsId = ms.Id;
          const virtualMsId = originalMsId ? idManager.getOrCreateVirtualId(originalMsId, r.serverIndex) : virtualItemId;

          // Replace DirectStreamUrl
          if (ms.DirectStreamUrl) {
            // Save original full URL for later proxying
            // Use streamBaseUrl for servers with different streaming domains
            const streamBase = r.client.streamBaseUrl || r.client.baseUrl;
            const originalUrl = new URL(ms.DirectStreamUrl, streamBase).toString();
            idManager.setMediaSourceStreamUrl(virtualMsId, originalUrl);

            const containerMatch = ms.DirectStreamUrl.match(/\.([a-z0-9]+)(?:\?|$)/i);
            const container = containerMatch ? containerMatch[1] : (ms.Container || 'mp4');
            ms.DirectStreamUrl = `/Videos/${virtualItemId}/stream.${container}?MediaSourceId=${virtualMsId}&Static=true&api_key=${req.proxyToken}`;
          }

          // TranscodingUrl
          if (ms.TranscodingUrl) {
            const streamBase = r.client.streamBaseUrl || r.client.baseUrl;
            const originalUrl = new URL(ms.TranscodingUrl, streamBase).toString();
            idManager.setMediaSourceStreamUrl(virtualMsId + '_transcode', originalUrl);

            let tUrl = ms.TranscodingUrl;
            if (r.originalId) tUrl = tUrl.split(r.originalId).join(virtualItemId);
            if (originalMsId && originalMsId !== r.originalId) {
              tUrl = tUrl.split(originalMsId).join(virtualMsId);
            }
            try {
              const u = new URL(tUrl, 'http://dummy');
              u.searchParams.delete('api_key');
              u.searchParams.delete('ApiKey');
              if (req.proxyToken) u.searchParams.set('api_key', req.proxyToken);
              tUrl = u.pathname + u.search;
            } catch {}
            ms.TranscodingUrl = tUrl;
          }

          // Path and MediaStreams
          if (ms.Path && ms.Protocol === 'Http') {
            if (r.originalId) ms.Path = ms.Path.split(r.originalId).join(virtualItemId);
            if (originalMsId && originalMsId !== r.originalId) {
              ms.Path = ms.Path.split(originalMsId).join(virtualMsId);
            }
          }
          if (ms.MediaStreams) {
            for (const stream of ms.MediaStreams) {
              if (stream.DeliveryUrl) {
                if (r.originalId) stream.DeliveryUrl = stream.DeliveryUrl.split(r.originalId).join(virtualItemId);
                if (originalMsId && originalMsId !== r.originalId) {
                  stream.DeliveryUrl = stream.DeliveryUrl.split(originalMsId).join(virtualMsId);
                }
              }
            }
          }

          // Tag name with server name to distinguish multiple versions
          const client = upstreamManager.getClient(r.serverIndex);
          if (client && successResults.length > 1) {
            ms.Name = `${ms.Name || 'Version'} [${client.name}]`;
          }

          ms.Id = virtualMsId; // Ensure MS ID is virtualized
          allMediaSources.push(ms);
        }
      }

      cloned.MediaSources = allMediaSources;

      // Track play session of the base result (representative server)
      if (cloned.PlaySessionId) {
        const originalPlaySessionId = cloned.PlaySessionId;
        const virtualPlaySessionId = idManager.getOrCreateVirtualId(originalPlaySessionId, baseResult.serverIndex);
        registerPlaySession(virtualPlaySessionId, baseResult.serverIndex, originalPlaySessionId);
      }

      rewriteResponseIds(cloned, baseResult.serverIndex, idManager, config.server.id, authManager.getProxyUserId());
      res.json(cloned);
    } catch (err) {
      logger.error(`Error in PlaybackInfo: ${err.message}`);
      if (!res.headersSent) res.status(500).json({ message: 'Internal server error' });
    }
  }

  // POST /Users/:userId/PlayingItems/:itemId — report playback start
  router.post('/Users/:userId/PlayingItems/:itemId', requireAuth, async (req, res) => {
    try {
      const resolved = req.resolveId(req.params.itemId);
      if (!resolved) return res.status(404).json({ message: 'Item not found' });

      const params = { ...req.query };
      if (params.MediaSourceId) {
        const msResolved = idManager.resolveVirtualId(params.MediaSourceId);
        if (msResolved) params.MediaSourceId = msResolved.originalId;
      }

      const path = `/Users/${resolved.client.userId}/PlayingItems/${resolved.originalId}`;
      await resolved.client.request('POST', path, { params });
      res.status(204).end();
    } catch (err) {
      logger.error(`Error in POST PlayingItems: ${err.message}`);
      res.status(500).json({ message: 'Internal server error' });
    }
  });

  // DELETE /Users/:userId/PlayingItems/:itemId — report playback stop
  router.delete('/Users/:userId/PlayingItems/:itemId', requireAuth, async (req, res) => {
    try {
      const resolved = req.resolveId(req.params.itemId);
      if (!resolved) return res.status(404).json({ message: 'Item not found' });

      const params = { ...req.query };
      if (params.MediaSourceId) {
        const msResolved = idManager.resolveVirtualId(params.MediaSourceId);
        if (msResolved) params.MediaSourceId = msResolved.originalId;
      }

      const path = `/Users/${resolved.client.userId}/PlayingItems/${resolved.originalId}`;
      await resolved.client.request('DELETE', path, { params });
      res.status(204).end();
    } catch (err) {
      logger.error(`Error in DELETE PlayingItems: ${err.message}`);
      res.status(500).json({ message: 'Internal server error' });
    }
  });

  // POST /Users/:userId/Items/:itemId/UserData — update user data (played status, etc.)
  router.post('/Users/:userId/Items/:itemId/UserData', requireAuth, async (req, res) => {
    try {
      const resolved = req.resolveId(req.params.itemId);
      if (!resolved) return res.status(404).json({ message: 'Item not found' });

      const path = `/Users/${resolved.client.userId}/Items/${resolved.originalId}/UserData`;
      const data = await resolved.client.request('POST', path, { data: req.body });
      rewriteResponseIds(data, resolved.serverIndex, idManager, config.server.id, authManager.getProxyUserId());
      res.json(data);
    } catch (err) {
      logger.error(`Error in POST UserData: ${err.message}`);
      res.status(500).json({ message: 'Internal server error' });
    }
  });

  // POST /Users/:userId/FavoriteItems/:itemId
  router.post('/Users/:userId/FavoriteItems/:itemId', requireAuth, async (req, res) => {
    try {
      const resolved = req.resolveId(req.params.itemId);
      if (!resolved) return res.status(404).json({ message: 'Item not found' });

      const path = `/Users/${resolved.client.userId}/FavoriteItems/${resolved.originalId}`;
      const data = await resolved.client.request('POST', path);
      rewriteResponseIds(data, resolved.serverIndex, idManager, config.server.id, authManager.getProxyUserId());
      res.json(data);
    } catch (err) {
      logger.error(`Error in POST FavoriteItems: ${err.message}`);
      res.status(500).json({ message: 'Internal server error' });
    }
  });

  // DELETE /Users/:userId/FavoriteItems/:itemId
  router.delete('/Users/:userId/FavoriteItems/:itemId', requireAuth, async (req, res) => {
    try {
      const resolved = req.resolveId(req.params.itemId);
      if (!resolved) return res.status(404).json({ message: 'Item not found' });

      const path = `/Users/${resolved.client.userId}/FavoriteItems/${resolved.originalId}`;
      const data = await resolved.client.request('DELETE', path);
      rewriteResponseIds(data, resolved.serverIndex, idManager, config.server.id, authManager.getProxyUserId());
      res.json(data);
    } catch (err) {
      logger.error(`Error in DELETE FavoriteItems: ${err.message}`);
      res.status(500).json({ message: 'Internal server error' });
    }
  });

  return router;
}

module.exports = { createPlaybackRoutes };

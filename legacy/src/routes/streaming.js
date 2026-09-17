const { Router } = require('express');
const { requireAuth } = require('../middleware/auth-middleware');
const { proxyStream, buildStreamUrl } = require('../utils/stream-proxy');
const logger = require('../utils/logger');

function createStreamingRoutes(config, idManager, upstreamManager) {
  const router = Router();

  /**
   * Resolve item ID and get stream configuration.
   */
  function resolveStreamRequest(req) {
    const itemId = req.params.itemId;
    const resolved = req.resolveId(itemId);
    if (!resolved) return null;

    const serverConfig = config.upstream[resolved.serverIndex];
    const playbackMode = serverConfig?.playbackMode || config.playback.mode || 'proxy';

    return { ...resolved, playbackMode };
  }

  /**
   * Handle a stream request: either proxy or redirect.
   */
  async function handleStream(req, res, pathBuilder) {
    const resolved = resolveStreamRequest(req);
    if (!resolved) {
      return res.status(404).json({ message: 'Item not found' });
    }

    const params = { ...req.query };
    let virtualMsId = null;
    let actualClient = resolved.client;
    let actualServerIndex = resolved.serverIndex;
    let actualOriginalId = resolved.originalId;

    if (params.MediaSourceId) {
      virtualMsId = params.MediaSourceId;
      const msResolved = idManager.resolveVirtualId(params.MediaSourceId);
      if (msResolved) {
        params.MediaSourceId = msResolved.originalId;
        if (msResolved.serverIndex !== resolved.serverIndex) {
          const msClient = upstreamManager.getClient(msResolved.serverIndex);
          if (msClient && msClient.online) {
            actualClient = msClient;
            actualServerIndex = msResolved.serverIndex;
            const otherInst = (resolved.otherInstances || []).find(i => i.serverIndex === msResolved.serverIndex);
            if (otherInst) actualOriginalId = otherInst.originalId;
          }
        }
      }
    }
    if (params.PlaySessionId) {
      const psResolved = idManager.resolveVirtualId(params.PlaySessionId);
      if (psResolved) params.PlaySessionId = psResolved.originalId;
    }

    const upstreamPath = pathBuilder(actualOriginalId, req);
    let upstreamUrl;

    const isTranscode = req.path.includes('master.m3u8') || req.path.includes('main.m3u8') || req.path.includes('hls');
    const storedUrl = idManager.getMediaSourceStreamUrl(isTranscode ? virtualMsId + '_transcode' : virtualMsId);

    if (storedUrl) {
      try {
        const url = new URL(storedUrl);
        for (const [k, v] of Object.entries(params)) {
          if (v != null && k !== 'api_key' && k !== 'ApiKey') {
            url.searchParams.set(k, v);
          }
        }
        upstreamUrl = url.toString();
      } catch (e) {
        upstreamUrl = buildStreamUrl(actualClient, upstreamPath, params);
      }
    } else {
      upstreamUrl = buildStreamUrl(actualClient, upstreamPath, params);
    }

    const serverConfig = config.upstream[actualServerIndex];
    const playbackMode = serverConfig?.playbackMode || config.playback.mode || 'proxy';

    if (playbackMode === 'redirect') {
      return res.redirect(302, upstreamUrl);
    }

    try {
      const streamHeaders = actualClient.getRequestHeaders();
      logger.debug(`Stream headers for [${actualClient.name}]: ${JSON.stringify(streamHeaders)}`);
      req._proxyToken = req.proxyToken || req.query.api_key || '';
      await proxyStream(upstreamUrl, actualClient.accessToken, req, res, streamHeaders);
    } catch (err) {
      logger.error(`Stream error for ${req.path}: ${err.message}`);
      if (!res.headersSent) res.status(502).json({ message: 'Stream failed' });
    }
  }

  router.get('/Videos/:itemId/stream', requireAuth, async (req, res) => {
    await handleStream(req, res, (origId) => `/Videos/${origId}/stream`);
  });
  router.get('/Videos/:itemId/stream.:format', requireAuth, async (req, res) => {
    await handleStream(req, res, (origId, r) => `/Videos/${origId}/stream.${r.params.format}`);
  });

  router.get('/Audio/:itemId/stream', requireAuth, async (req, res) => {
    await handleStream(req, res, (origId) => `/Audio/${origId}/stream`);
  });
  router.get('/Audio/:itemId/stream.:format', requireAuth, async (req, res) => {
    await handleStream(req, res, (origId, r) => `/Audio/${origId}/stream.${r.params.format}`);
  });

  router.get('/Audio/:itemId/universal', requireAuth, async (req, res) => {
    await handleStream(req, res, (origId) => `/Audio/${origId}/universal`);
  });

  router.get('/Videos/:itemId/master.m3u8', requireAuth, async (req, res) => {
    await handleStream(req, res, (origId) => `/Videos/${origId}/master.m3u8`);
  });

  router.get('/Videos/:itemId/main.m3u8', requireAuth, async (req, res) => {
    await handleStream(req, res, (origId) => `/Videos/${origId}/main.m3u8`);
  });

  router.get('/Videos/:itemId/hls1/:playlistId/:segmentId.:format', requireAuth, async (req, res) => {
    await handleStream(req, res, (origId, r) =>
      `/Videos/${origId}/hls1/${r.params.playlistId}/${r.params.segmentId}.${r.params.format}`
    );
  });

  router.get('/Videos/:itemId/hls/:playlistFile', requireAuth, async (req, res) => {
    await handleStream(req, res, (origId, r) => `/Videos/${origId}/hls/${r.params.playlistFile}`);
  });

  router.get('/Videos/:itemId/:mediaSourceId/Subtitles/:subtitleIndex/:startTicks/Stream.:format', requireAuth, async (req, res) => {
    const { mediaSourceId, subtitleIndex, startTicks, format } = req.params;
    let origMediaSourceId = mediaSourceId;
    const msResolved = idManager.resolveVirtualId(mediaSourceId);
    if (msResolved) origMediaSourceId = msResolved.originalId;
    await handleStream(req, res, (origId) =>
      `/Videos/${origId}/${origMediaSourceId}/Subtitles/${subtitleIndex}/${startTicks}/Stream.${format}`
    );
  });

  router.get('/Videos/:itemId/:mediaSourceId/Subtitles/:subtitleIndex/Stream.:format', requireAuth, async (req, res) => {
    const { mediaSourceId, subtitleIndex, format } = req.params;
    let origMediaSourceId = mediaSourceId;
    const msResolved = idManager.resolveVirtualId(mediaSourceId);
    if (msResolved) origMediaSourceId = msResolved.originalId;
    await handleStream(req, res, (origId) =>
      `/Videos/${origId}/${origMediaSourceId}/Subtitles/${subtitleIndex}/Stream.${format}`
    );
  });

  router.get('/Videos/:itemId/:mediaSourceId/Attachments/:attachmentIndex', requireAuth, async (req, res) => {
    const { mediaSourceId, attachmentIndex } = req.params;
    let origMediaSourceId = mediaSourceId;
    const msResolved = idManager.resolveVirtualId(mediaSourceId);
    if (msResolved) origMediaSourceId = msResolved.originalId;
    await handleStream(req, res, (origId) =>
      `/Videos/${origId}/${origMediaSourceId}/Attachments/${attachmentIndex}`
    );
  });

  router.delete('/Videos/ActiveEncodings', requireAuth, async (req, res) => {
    try {
      const params = { ...req.query };
      let serverIndex = null;

      if (params.PlaySessionId) {
        const resolved = idManager.resolveVirtualId(params.PlaySessionId);
        if (resolved) {
          params.PlaySessionId = resolved.originalId;
          serverIndex = resolved.serverIndex;
        }
      }

      if (serverIndex !== null) {
        const client = upstreamManager.getClient(serverIndex);
        if (client) {
          await client.request('DELETE', '/Videos/ActiveEncodings', { params });
        }
      } else {
        const onlineClients = upstreamManager.getOnlineClients();
        await Promise.allSettled(
          onlineClients.map(c => c.request('DELETE', '/Videos/ActiveEncodings', { params }))
        );
      }

      res.status(204).end();
    } catch (err) {
      logger.error(`Error in DELETE ActiveEncodings: ${err.message}`);
      res.status(204).end();
    }
  });

  return router;
}

module.exports = { createStreamingRoutes };
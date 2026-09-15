const { Router } = require('express');
const { proxyStream, buildStreamUrl } = require('../utils/stream-proxy');
const logger = require('../utils/logger');

function createImageRoutes(config, idManager, upstreamManager) {
  const router = Router();

  // GET /Items/:itemId/Images/:imageType
  // GET /Items/:itemId/Images/:imageType/:imageIndex
  router.get('/Items/:itemId/Images/:imageType/:imageIndex?', async (req, res) => {
    try {
      const resolved = req.resolveId(req.params.itemId);
      if (!resolved) {
        logger.debug(`Image 404: virtualId=${req.params.itemId} not found in mappings`);
        return res.status(404).end();
      }

      const { imageType, imageIndex } = req.params;
      let path = `/Items/${resolved.originalId}/Images/${imageType}`;
      if (imageIndex != null) path += `/${imageIndex}`;

      const url = buildStreamUrl(resolved.client, path, req.query);

      logger.debug(`Image proxy: ${req.params.itemId} → [${resolved.client.name}] ${path}`);

      // Set caching headers
      res.set('Cache-Control', 'public, max-age=86400');

      const spoofHeaders = resolved.client.getRequestHeaders();
      await proxyStream(url, resolved.client.accessToken, req, res, spoofHeaders);
    } catch (err) {
      logger.error(`Error proxying image: ${err.message}`);
      if (!res.headersSent) res.status(502).end();
    }
  });

  // GET /Users/:userId/Images/:imageType
  router.get('/Users/:userId/Images/:imageType/:imageIndex?', (req, res) => {
    // Return empty/default for proxy user images
    res.status(404).end();
  });

  return router;
}

module.exports = { createImageRoutes };

const { Router } = require('express');
const { requireAuth } = require('../middleware/auth-middleware');
const { rewriteResponseIds } = require('../utils/id-rewriter');
const { proxyStream, buildStreamUrl } = require('../utils/stream-proxy');
const logger = require('../utils/logger');

function createFallbackRoutes(config, authManager, idManager, upstreamManager) {
  const router = Router();

  // Single combined regex for extracting item IDs from URL paths
  const PATH_ID_RE = /\/(?:Items|Videos|Audio|Shows|Users)\/([a-f0-9]{32})/i;

  /**
   * Try to extract an item ID from the URL path.
   */
  function extractIdFromPath(path) {
    const match = path.match(PATH_ID_RE);
    return match ? match[1] : null;
  }

  /**
   * Fallback handler: try to route to the correct upstream server.
   * Requires authentication to prevent unauthenticated upstream access.
   */
  router.use(requireAuth, async (req, res) => {
    try {
      // Try to find a virtual ID in the URL path
      const idFromPath = extractIdFromPath(req.path);
      let targetClient = null;
      let rewrittenPath = req.path;
      let serverIndex = null;

      if (idFromPath) {
        const resolved = idManager.resolveVirtualId(idFromPath);
        if (resolved) {
          targetClient = upstreamManager.getClient(resolved.serverIndex);
          serverIndex = resolved.serverIndex;
          // Replace virtual ID with original in the path
          rewrittenPath = req.path.split(idFromPath).join(resolved.originalId);
        }
      }

      // Also check query params for IDs
      const params = { ...req.query };
      for (const [key, value] of Object.entries(params)) {
        if (typeof value === 'string' && idManager.isVirtualId(value)) {
          const resolved = idManager.resolveVirtualId(value);
          if (resolved) {
            params[key] = resolved.originalId;
            if (!targetClient) {
              targetClient = upstreamManager.getClient(resolved.serverIndex);
              serverIndex = resolved.serverIndex;
            }
          }
        }
      }

      // Replace proxy user ID with upstream user ID in path
      if (authManager && targetClient) {
        const proxyUserId = authManager.getProxyUserId();
        if (rewrittenPath.includes(proxyUserId)) {
          rewrittenPath = rewrittenPath.replace(proxyUserId, targetClient.userId);
        }
      }

      // If no target found, try the first online server
      if (!targetClient) {
        const onlineClients = upstreamManager.getOnlineClients();
        if (onlineClients.length === 0) {
          return res.status(503).json({ message: 'No upstream servers available' });
        }
        targetClient = onlineClients[0];
        serverIndex = targetClient.serverIndex;
      }

      // Remove proxy's api_key from forwarded params
      delete params.api_key;
      delete params.ApiKey;

      logger.debug(`Fallback: ${req.method} ${req.path} → [${targetClient.name}] ${rewrittenPath}`);

      if (req.method === 'GET' || req.method === 'HEAD') {
        const data = await targetClient.request(req.method, rewrittenPath, { params });

        // If response is JSON, rewrite IDs in-place (no deep clone)
        if (data && typeof data === 'object') {
          rewriteResponseIds(data, serverIndex, idManager, config.server.id, authManager.getProxyUserId());
          return res.json(data);
        }

        // Guard: if upstream returned HTML instead of JSON (WAF/CDN error pages), don't forward raw HTML
        if (typeof data === 'string' && /^\s*<!DOCTYPE|^\s*<html/i.test(data)) {
          logger.warn(`Fallback: upstream [${targetClient.name}] returned HTML for GET ${req.path}, discarding`);
          return res.status(502).json({ message: 'Upstream returned non-JSON response' });
        }

        res.send(data);
      } else {
        // POST, DELETE, etc.
        const data = await targetClient.request(req.method, rewrittenPath, {
          params,
          data: req.body,
        });

        if (data && typeof data === 'object') {
          rewriteResponseIds(data, serverIndex, idManager, config.server.id, authManager.getProxyUserId());
          return res.json(data);
        }

        if (data) {
          if (typeof data === 'string' && /^\s*<!DOCTYPE|^\s*<html/i.test(data)) {
            logger.warn(`Fallback: upstream [${targetClient.name}] returned HTML for ${req.method} ${req.path}, discarding`);
            return res.status(502).json({ message: 'Upstream returned non-JSON response' });
          }
          res.send(data);
        } else {
          res.status(204).end();
        }
      }
    } catch (err) {
      if (err.response) {
        const status = err.response.status;
        logger.debug(`Fallback upstream error: ${status} for ${req.method} ${req.path}`);
        return res.status(status).json({ message: `Upstream returned ${status}` });
      }

      logger.error(`Fallback error: ${req.method} ${req.path} - ${err.message}`);
      if (!res.headersSent) {
        res.status(502).json({ message: 'Upstream request failed' });
      }
    }
  });

  return router;
}

module.exports = { createFallbackRoutes };

const { resolveIdParam } = require('../utils/id-rewriter');

/**
 * Middleware that parses request context:
 * - Resolves virtual IDs from URL params
 * - Provides helper methods for routing to the right upstream server
 */
function createRequestContext(idManager, upstreamManager) {
  return function requestContext(req, res, next) {
    req.ctx = {
      idManager,
      upstreamManager,
    };

    /**
     * Resolve a virtual ID to { originalId, serverIndex, client }.
     */
    req.resolveId = function resolveId(virtualId) {
      if (!virtualId) return null;
      const resolved = idManager.resolveVirtualId(virtualId);
      if (!resolved) return null;
      const client = upstreamManager.getClient(resolved.serverIndex);
      if (!client) return null;
      return { ...resolved, client };
    };

    /**
     * Get upstream user ID for a given server.
     */
    req.getUpstreamUserId = function getUpstreamUserId(serverIndex) {
      const client = upstreamManager.getClient(serverIndex);
      return client ? client.userId : null;
    };

    next();
  };
}

module.exports = { createRequestContext };

const { Router } = require('express');
const { requireAuth } = require('../middleware/auth-middleware');
const { rewriteRequestIds } = require('../utils/id-rewriter');
const logger = require('../utils/logger');

function createSessionRoutes(config, authManager, idManager, upstreamManager) {
  const router = Router();

  // POST /Sessions/Playing — report playback start
  router.post('/Sessions/Playing', requireAuth, async (req, res) => {
    try {
      const body = { ...req.body };
      let serverIndex = null;

      // Resolve ItemId
      if (body.ItemId) {
        const resolved = idManager.resolveVirtualId(body.ItemId);
        if (resolved) {
          body.ItemId = resolved.originalId;
          serverIndex = resolved.serverIndex;
        }
      }

      // Resolve MediaSourceId
      if (body.MediaSourceId) {
        const resolved = idManager.resolveVirtualId(body.MediaSourceId);
        if (resolved) {
          body.MediaSourceId = resolved.originalId;
          if (serverIndex === null) serverIndex = resolved.serverIndex;
        }
      }

      // Resolve PlaySessionId
      if (body.PlaySessionId) {
        const resolved = idManager.resolveVirtualId(body.PlaySessionId);
        if (resolved) {
          body.PlaySessionId = resolved.originalId;
          if (serverIndex === null) serverIndex = resolved.serverIndex;
        }
      }

      if (serverIndex === null) {
        return res.status(400).json({ message: 'Cannot determine target server' });
      }

      const client = upstreamManager.getClient(serverIndex);
      if (!client) return res.status(404).json({ message: 'Server not found' });

      await client.request('POST', '/Sessions/Playing', { data: body });
      res.status(204).end();
    } catch (err) {
      logger.error(`Error in POST Sessions/Playing: ${err.message}`);
      res.status(500).json({ message: 'Internal server error' });
    }
  });

  // POST /Sessions/Playing/Progress — report playback progress
  router.post('/Sessions/Playing/Progress', requireAuth, async (req, res) => {
    try {
      const body = { ...req.body };
      let serverIndex = null;

      if (body.ItemId) {
        const resolved = idManager.resolveVirtualId(body.ItemId);
        if (resolved) { body.ItemId = resolved.originalId; serverIndex = resolved.serverIndex; }
      }
      if (body.MediaSourceId) {
        const resolved = idManager.resolveVirtualId(body.MediaSourceId);
        if (resolved) { body.MediaSourceId = resolved.originalId; if (serverIndex === null) serverIndex = resolved.serverIndex; }
      }
      if (body.PlaySessionId) {
        const resolved = idManager.resolveVirtualId(body.PlaySessionId);
        if (resolved) { body.PlaySessionId = resolved.originalId; if (serverIndex === null) serverIndex = resolved.serverIndex; }
      }

      if (serverIndex === null) return res.status(204).end();

      const client = upstreamManager.getClient(serverIndex);
      if (!client) return res.status(204).end();

      await client.request('POST', '/Sessions/Playing/Progress', { data: body });
      res.status(204).end();
    } catch (err) {
      logger.error(`Error in POST Sessions/Playing/Progress: ${err.message}`);
      res.status(204).end(); // Don't fail the client for progress reports
    }
  });

  // POST /Sessions/Playing/Stopped — report playback stop
  router.post('/Sessions/Playing/Stopped', requireAuth, async (req, res) => {
    try {
      const body = { ...req.body };
      let serverIndex = null;

      if (body.ItemId) {
        const resolved = idManager.resolveVirtualId(body.ItemId);
        if (resolved) { body.ItemId = resolved.originalId; serverIndex = resolved.serverIndex; }
      }
      if (body.MediaSourceId) {
        const resolved = idManager.resolveVirtualId(body.MediaSourceId);
        if (resolved) { body.MediaSourceId = resolved.originalId; if (serverIndex === null) serverIndex = resolved.serverIndex; }
      }
      if (body.PlaySessionId) {
        const resolved = idManager.resolveVirtualId(body.PlaySessionId);
        if (resolved) { body.PlaySessionId = resolved.originalId; if (serverIndex === null) serverIndex = resolved.serverIndex; }
      }

      if (serverIndex === null) return res.status(204).end();

      const client = upstreamManager.getClient(serverIndex);
      if (!client) return res.status(204).end();

      await client.request('POST', '/Sessions/Playing/Stopped', { data: body });
      res.status(204).end();
    } catch (err) {
      logger.error(`Error in POST Sessions/Playing/Stopped: ${err.message}`);
      res.status(204).end();
    }
  });

  // POST /Sessions/Capabilities/Full — send to all servers
  router.post('/Sessions/Capabilities/Full', requireAuth, async (req, res) => {
    try {
      const onlineClients = upstreamManager.getOnlineClients();
      await Promise.allSettled(
        onlineClients.map(client =>
          client.request('POST', '/Sessions/Capabilities/Full', { data: req.body })
        )
      );
      res.status(204).end();
    } catch (err) {
      logger.error(`Error in POST Capabilities: ${err.message}`);
      res.status(204).end();
    }
  });

  // POST /Sessions/Capabilities
  router.post('/Sessions/Capabilities', requireAuth, async (req, res) => {
    try {
      const onlineClients = upstreamManager.getOnlineClients();
      await Promise.allSettled(
        onlineClients.map(client =>
          client.request('POST', '/Sessions/Capabilities', { data: req.body, params: req.query })
        )
      );
      res.status(204).end();
    } catch (err) {
      res.status(204).end();
    }
  });

  return router;
}

module.exports = { createSessionRoutes };

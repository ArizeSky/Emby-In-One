const { Router } = require('express');
const { requireAuth } = require('../middleware/auth-middleware');
const { rewriteResponseIds } = require('../utils/id-rewriter');
const { fetchSeriesScopedItems } = require('../utils/series-userdata');
const logger = require('../utils/logger');

function createItemRoutes(config, authManager, idManager, upstreamManager) {
  const router = Router();

  /**
   * Helper: fetch items from a specific server, rewriting IDs in the response.
   */
  async function fetchItemsFromServer(client, path, params) {
    const data = await client.request('GET', path, { params });
    // In-place rewrite to save memory on 0.5G RAM VPS
    rewriteResponseIds(data, client.serverIndex, idManager, config.server.id, authManager.getProxyUserId());
    return data;
  }

  /**
   * Helper: fetch from all servers and merge items.
   */
  async function fetchAndMergeFromAll(pathBuilder, params) {
    const startTime = Date.now();
    const onlineClients = upstreamManager.getOnlineClients();

    // Request with global timeout — generous for 5+ servers across different networks
    const globalTimeout = (config.timeouts && config.timeouts.global) || 15000;
    const controller = new AbortController();
    const tId = setTimeout(() => controller.abort(), globalTimeout);

    const results = await Promise.allSettled(
      onlineClients.map(async (client) => {
        const path = pathBuilder(client);
        const data = await client.request('GET', path, { params, signal: controller.signal });
        return { serverIndex: client.serverIndex, data };
      })
    );
    clearTimeout(tId);

    for (let i = 0; i < results.length; i++) {
      if (results[i].status === 'rejected') {
        const client = onlineClients[i];
        logger.warn(`[${client.name}] ${pathBuilder(client)} failed: ${results[i].reason?.message || 'unknown'}`);
      }
    }

    const successResults = results
      .filter(r => r.status === 'fulfilled')
      .map(r => r.value);

    // Merge items with interleaving and 20s fuse
    const merged = upstreamManager.mergeItemsResults(successResults, startTime, 20000);

    // In-place rewrite IDs in merged items
    if (merged._serverIndices) {
      for (let i = 0; i < merged.Items.length; i++) {
        // Break if total time exceeds 20s
        if (i % 100 === 0 && (Date.now() - startTime > 20000)) break;
        rewriteResponseIds(merged.Items[i], merged._serverIndices[i], idManager, config.server.id, authManager.getProxyUserId());
      }
      delete merged._serverIndices;
    }

    return merged;
  }

  // GET /Users/:userId/Items
  router.get('/Users/:userId/Items', requireAuth, async (req, res) => {
    try {
      const { ParentId, parentId, parentid } = req.query;
      const virtualParentId = ParentId || parentId || parentid;

      if (virtualParentId && virtualParentId !== '0' && virtualParentId !== 'root') {
        // Specific parent: route to the correct server
        const resolved = req.resolveId(virtualParentId);
        if (!resolved) {
          // If ParentId is not a virtual ID, and not 0/root, return empty
          return res.json({ Items: [], TotalRecordCount: 0, StartIndex: 0 });
        }

        const upstreamParams = { ...req.query };
        upstreamParams.ParentId = resolved.originalId;
        delete upstreamParams.parentId;
        delete upstreamParams.parentid;

        const path = `/Users/${resolved.client.userId}/Items`;
        const data = await fetchItemsFromServer(resolved.client, path, upstreamParams);
        return res.json(data);
      }

      // No specific parent or ParentId=0/root: query all servers and merge
      const params = { ...req.query };
      const merged = await fetchAndMergeFromAll(
        (client) => `/Users/${client.userId}/Items`,
        params
      );

      // Handle pagination
      const startIndex = parseInt(req.query.StartIndex) || 0;
      const limit = parseInt(req.query.Limit) || merged.Items.length;
      if (startIndex > 0 || limit < merged.Items.length) {
        merged.Items = merged.Items.slice(startIndex, startIndex + limit);
        merged.StartIndex = startIndex;
      }

      res.json(merged);
    } catch (err) {
      logger.error(`Error in GET /Users/:userId/Items: ${err.message}`);
      res.status(500).json({ message: 'Internal server error' });
    }
  });

  // GET /Users/:userId/Items/Resume
  router.get('/Users/:userId/Items/Resume', requireAuth, async (req, res) => {
    try {
      const params = { ...req.query };
      const virtualParentId = params.ParentId || params.parentId || params.parentid;

      if (virtualParentId) {
        const resolved = req.resolveId(virtualParentId);
        if (!resolved) {
          return res.json({ Items: [], TotalRecordCount: 0, StartIndex: 0 });
        }

        const selected = await fetchSeriesScopedItems({
          resolved,
          upstreamManager,
          fetchItems: async (inst) => {
            const upstreamParams = { ...params, ParentId: inst.originalId };
            delete upstreamParams.parentId;
            delete upstreamParams.parentid;

            return inst.client.request('GET', `/Users/${inst.client.userId}/Items/Resume`, {
              params: upstreamParams,
            });
          },
        });

        const items = selected.items || [];
        if (selected.serverIndex != null) {
          for (const item of items) {
            rewriteResponseIds(item, selected.serverIndex, idManager, config.server.id, authManager.getProxyUserId());
          }
        }

        return res.json({
          Items: items,
          TotalRecordCount: items.length,
          StartIndex: 0,
        });
      }

      // No ParentId: query all servers and merge
      const merged = await fetchAndMergeFromAll(
        (client) => `/Users/${client.userId}/Items/Resume`,
        params
      );
      res.json(merged);
    } catch (err) {
      logger.error(`Error in GET Items/Resume: ${err.message}`);
      res.status(500).json({ message: 'Internal server error' });
    }
  });

  // GET /Users/:userId/Items/Latest
  router.get('/Users/:userId/Items/Latest', requireAuth, async (req, res) => {
    try {
      const { ParentId } = req.query;

      if (ParentId) {
        const resolved = req.resolveId(ParentId);
        if (!resolved) return res.json([]);

        const upstreamParams = { ...req.query, ParentId: resolved.originalId };
        const path = `/Users/${resolved.client.userId}/Items/Latest`;
        const data = await resolved.client.request('GET', path, { params: upstreamParams });
        const items = Array.isArray(data) ? data : (data.Items || []);
        items.forEach(item => rewriteResponseIds(item, resolved.serverIndex, idManager, config.server.id, authManager.getProxyUserId()));
        return res.json(items);
      }

      // Query all servers with global timeout
      const onlineClients = upstreamManager.getOnlineClients();
      const latestTimeout = (config.timeouts && config.timeouts.global) || 15000;
      const controller = new AbortController();
      const tId = setTimeout(() => controller.abort(), latestTimeout);
      const results = await Promise.allSettled(
        onlineClients.map(async (client) => {
          const data = await client.request('GET', `/Users/${client.userId}/Items/Latest`, { params: req.query, signal: controller.signal });
          const items = Array.isArray(data) ? data : (data.Items || []);
          return { serverIndex: client.serverIndex, items };
        })
      );
      clearTimeout(tId);

      for (let i = 0; i < results.length; i++) {
        if (results[i].status === 'rejected') {
          const client = onlineClients[i];
          logger.warn(`[${client.name}] Items/Latest failed: ${results[i].reason?.message || 'unknown'}`);
        }
      }

      const allItems = [];
      for (const r of results) {
        if (r.status === 'fulfilled') {
          for (const item of r.value.items) {
            rewriteResponseIds(item, r.value.serverIndex, idManager, config.server.id, authManager.getProxyUserId());
            allItems.push(item);
          }
        }
      }

      res.json(allItems);
    } catch (err) {
      logger.error(`Error in GET Items/Latest: ${err.message}`);
      res.status(500).json({ message: 'Internal server error' });
    }
  });

  // GET /Users/:userId/Items/:itemId
  router.get('/Users/:userId/Items/:itemId', requireAuth, async (req, res) => {
    try {
      const virtualId = req.params.itemId;
      const resolved = req.resolveId(virtualId);
      if (!resolved) {
        return res.status(404).json({ message: 'Item not found' });
      }

      // Gather all instances (primary + secondary)
      const instances = [
        { originalId: resolved.originalId, serverIndex: resolved.serverIndex, client: resolved.client },
        ...(resolved.otherInstances || []).map(inst => ({
          ...inst,
          client: upstreamManager.getClient(inst.serverIndex)
        }))
      ].filter(inst => inst.client && inst.client.online);

      if (instances.length <= 1) {
        // Only one instance or others offline: standard single-server fetch
        const path = `/Users/${resolved.client.userId}/Items/${resolved.originalId}`;
        const data = await fetchItemsFromServer(resolved.client, path, req.query);
        return res.json(data);
      }

      // Multi-instance: fetch details from all and merge MediaSources
      const results = await Promise.allSettled(
        instances.map(async (inst) => {
          const path = `/Users/${inst.client.userId}/Items/${inst.originalId}`;
          const data = await inst.client.request('GET', path, { params: req.query });
          return { ...inst, data };
        })
      );

      const successResults = results
        .filter(r => r.status === 'fulfilled')
        .map(r => r.value);

      if (successResults.length === 0) {
        return res.status(502).json({ message: 'Upstream request failed' });
      }

      // Use the first successful result as the base metadata
      const base = successResults[0];
      const cloned = JSON.parse(JSON.stringify(base.data));

      // Combine MediaSources from all servers
      const allMediaSources = [];
      for (const result of successResults) {
        const msList = result.data.MediaSources || [];
        for (const ms of msList) {
          // Tag version name with server name
          const client = upstreamManager.getClient(result.serverIndex);
          if (client) {
            ms.Name = `${ms.Name || 'Version'} [${client.name}]`;
          }
          // Note: we don't rewrite stream URLs here, that's handled in PlaybackInfo
          // But we must ensure MediaSource Ids are virtualized correctly
          if (ms.Id) {
            ms.Id = idManager.getOrCreateVirtualId(ms.Id, result.serverIndex);
          }
          allMediaSources.push(ms);
        }
      }
      cloned.MediaSources = allMediaSources;

      // Rewrite all IDs in the merged response
      rewriteResponseIds(cloned, base.serverIndex, idManager, config.server.id, authManager.getProxyUserId());
      res.json(cloned);
    } catch (err) {
      logger.error(`Error in GET Items/:itemId: ${err.message}`);
      res.status(500).json({ message: 'Internal server error' });
    }
  });

  // GET /Items/:itemId (without user context)
  router.get('/Items/:itemId', requireAuth, async (req, res) => {
    try {
      const resolved = req.resolveId(req.params.itemId);
      if (!resolved) {
        return res.status(404).json({ message: 'Item not found' });
      }

      const data = await fetchItemsFromServer(resolved.client, `/Items/${resolved.originalId}`, req.query);
      res.json(data);
    } catch (err) {
      logger.error(`Error in GET /Items/:itemId: ${err.message}`);
      res.status(500).json({ message: 'Internal server error' });
    }
  });

  // GET /Items/:itemId/Similar
  router.get('/Items/:itemId/Similar', requireAuth, async (req, res) => {
    try {
      const resolved = req.resolveId(req.params.itemId);
      if (!resolved) {
        return res.json({ Items: [], TotalRecordCount: 0 });
      }

      const params = { ...req.query, UserId: resolved.client.userId };
      const data = await fetchItemsFromServer(resolved.client, `/Items/${resolved.originalId}/Similar`, params);
      res.json(data);
    } catch (err) {
      logger.error(`Error in GET Similar: ${err.message}`);
      res.status(500).json({ message: 'Internal server error' });
    }
  });

  // GET /Items/:itemId/ThemeMedia
  router.get('/Items/:itemId/ThemeMedia', requireAuth, async (req, res) => {
    try {
      const resolved = req.resolveId(req.params.itemId);
      if (!resolved) {
        return res.json({ ThemeVideosResult: { Items: [], TotalRecordCount: 0 }, ThemeSongsResult: { Items: [], TotalRecordCount: 0 }, SoundtrackSongsResult: { Items: [], TotalRecordCount: 0 } });
      }

      const params = { ...req.query, UserId: resolved.client.userId };
      const data = await fetchItemsFromServer(resolved.client, `/Items/${resolved.originalId}/ThemeMedia`, params);
      res.json(data);
    } catch (err) {
      logger.error(`Error in GET ThemeMedia: ${err.message}`);
      res.status(500).json({ message: 'Internal server error' });
    }
  });

  return router;
}

module.exports = { createItemRoutes };

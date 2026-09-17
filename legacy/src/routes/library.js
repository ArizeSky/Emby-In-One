const { Router } = require('express');
const { requireAuth } = require('../middleware/auth-middleware');
const { rewriteResponseIds } = require('../utils/id-rewriter');
const { fetchSeriesScopedItems } = require('../utils/series-userdata');
const logger = require('../utils/logger');

function createLibraryRoutes(config, authManager, idManager, upstreamManager) {
  const router = Router();

  // GET /Library/VirtualFolders — merge from all servers
  router.get('/Library/VirtualFolders', requireAuth, async (req, res) => {
    try {
      const startTime = Date.now();
      const results = await upstreamManager.requestAll('GET', '/Library/VirtualFolders', {}, 15000);

      const allFolders = [];
      for (const r of results) {
        if (Date.now() - startTime > 20000) break;
        const folders = Array.isArray(r.data) ? r.data : (r.data.Items || []);
        for (const folder of folders) {
          rewriteResponseIds(folder, r.serverIndex, idManager, config.server.id, authManager.getProxyUserId());
          const client = upstreamManager.getClient(r.serverIndex);
          if (client && upstreamManager.getOnlineClients().length > 1) {
            folder.Name = `${folder.Name} (${client.name})`;
          }
          allFolders.push(folder);
        }
      }
      res.json(allFolders);
    } catch (err) {
      logger.error(`Error in GET VirtualFolders: ${err.message}`);
      res.status(500).json({ message: 'Internal server error' });
    }
  });

  // GET /Library/SelectableRemoteLibraries
  router.get('/Library/SelectableRemoteLibraries', requireAuth, async (req, res) => {
    try {
      const startTime = Date.now();
      const results = await upstreamManager.requestAll('GET', '/Library/SelectableRemoteLibraries', {}, 15000);

      const allFolders = [];
      for (const r of results) {
        if (Date.now() - startTime > 20000) break;
        const folders = Array.isArray(r.data) ? r.data : (r.data.Items || []);
        for (const folder of folders) {
          rewriteResponseIds(folder, r.serverIndex, idManager, config.server.id, authManager.getProxyUserId());
          const client = upstreamManager.getClient(r.serverIndex);
          if (client && upstreamManager.getOnlineClients().length > 1) {
            folder.Name = `${folder.Name} (${client.name})`;
          }
          allFolders.push(folder);
        }
      }
      res.json(allFolders);
    } catch (err) {
      logger.error(`Error in SelectableRemoteLibraries: ${err.message}`);
      res.status(500).json({ message: 'Internal server error' });
    }
  });

  // GET /Library/MediaFolders
  router.get('/Library/MediaFolders', requireAuth, async (req, res) => {
    try {
      const startTime = Date.now();
      const results = await upstreamManager.requestAll('GET', '/Library/MediaFolders', {}, 15000);
      const merged = upstreamManager.mergeItemsResults(results, startTime, 20000);

      if (merged._serverIndices) {
        for (let i = 0; i < merged.Items.length; i++) {
          if (i % 100 === 0 && (Date.now() - startTime > 20000)) break;
          rewriteResponseIds(merged.Items[i], merged._serverIndices[i], idManager, config.server.id, authManager.getProxyUserId());
        }
        delete merged._serverIndices;
      }
      res.json(merged);
    } catch (err) {
      logger.error(`Error in GET MediaFolders: ${err.message}`);
      res.status(500).json({ message: 'Internal server error' });
    }
  });

  // GET /Shows/:seriesId/Seasons
  router.get('/Shows/:seriesId/Seasons', requireAuth, async (req, res) => {
    try {
      const startTime = Date.now();
      const seriesVirtualId = req.params.seriesId;
      const resolved = req.resolveId(seriesVirtualId);
      if (!resolved) return res.json({ Items: [], TotalRecordCount: 0 });

      const instances = [
        { originalId: resolved.originalId, serverIndex: resolved.serverIndex, client: resolved.client },
        ...(resolved.otherInstances || []).map(inst => ({
          ...inst,
          client: upstreamManager.getClient(inst.serverIndex)
        }))
      ].filter(inst => inst.client && inst.client.online);

      const results = await Promise.allSettled(
        instances.map(async (inst) => {
          const params = { ...req.query, UserId: inst.client.userId };
          const data = await inst.client.request('GET', `/Shows/${inst.originalId}/Seasons`, { params });
          return { serverIndex: inst.serverIndex, data };
        })
      );

      for (let i = 0; i < results.length; i++) {
        if (results[i].status === 'rejected') {
          const inst = instances[i];
          logger.warn(`[${inst.client.name}] Seasons failed: ${results[i].reason?.message || 'unknown'}`);
        }
      }

      const successResults = results.filter(r => r.status === 'fulfilled').map(r => r.value);
      const merged = upstreamManager.mergeSeasonsResults(successResults, startTime, 20000);

      if (merged._serverIndices) {
        for (let i = 0; i < merged.Items.length; i++) {
          if (i % 100 === 0 && (Date.now() - startTime > 20000)) break;
          rewriteResponseIds(merged.Items[i], merged._serverIndices[i], idManager, config.server.id, authManager.getProxyUserId());
        }
        delete merged._serverIndices;
      }
      res.json(merged);
    } catch (err) {
      logger.error(`Error in GET Seasons: ${err.message}`);
      res.status(500).json({ message: 'Internal server error' });
    }
  });

  // GET /Shows/:seriesId/Episodes
  router.get('/Shows/:seriesId/Episodes', requireAuth, async (req, res) => {
    try {
      const startTime = Date.now();
      const seriesVirtualId = req.params.seriesId;
      const resolved = req.resolveId(seriesVirtualId);
      if (!resolved) return res.json({ Items: [], TotalRecordCount: 0 });

      const params = { ...req.query };
      const instances = [
        { originalId: resolved.originalId, serverIndex: resolved.serverIndex, client: resolved.client },
        ...(resolved.otherInstances || []).map(inst => ({
          ...inst,
          client: upstreamManager.getClient(inst.serverIndex)
        }))
      ].filter(inst => inst.client && inst.client.online);

      const results = await Promise.allSettled(
        instances.map(async (inst) => {
          const upstreamParams = { ...params, UserId: inst.client.userId };
          if (params.SeasonId) {
            const sResolved = idManager.resolveVirtualId(params.SeasonId);
            if (sResolved) {
              if (sResolved.serverIndex === inst.serverIndex) {
                upstreamParams.SeasonId = sResolved.originalId;
              } else {
                const otherS = (sResolved.otherInstances || []).find(o => o.serverIndex === inst.serverIndex);
                if (otherS) upstreamParams.SeasonId = otherS.originalId;
                else delete upstreamParams.SeasonId;
              }
            } else {
              delete upstreamParams.SeasonId;
            }
          }
          const data = await inst.client.request('GET', `/Shows/${inst.originalId}/Episodes`, { params: upstreamParams });
          return { serverIndex: inst.serverIndex, data };
        })
      );

      for (let i = 0; i < results.length; i++) {
        if (results[i].status === 'rejected') {
          const inst = instances[i];
          logger.warn(`[${inst.client.name}] Episodes failed: ${results[i].reason?.message || 'unknown'}`);
        }
      }

      const successResults = results.filter(r => r.status === 'fulfilled').map(r => r.value);
      const merged = upstreamManager.mergeEpisodesResults(successResults, startTime, 20000);

      if (merged._serverIndices) {
        for (let i = 0; i < merged.Items.length; i++) {
          if (i % 100 === 0 && (Date.now() - startTime > 20000)) break;
          rewriteResponseIds(merged.Items[i], merged._serverIndices[i], idManager, config.server.id, authManager.getProxyUserId());
        }
        delete merged._serverIndices;
      }
      res.json(merged);
    } catch (err) {
      logger.error(`Error in GET Episodes: ${err.message}`);
      res.status(500).json({ message: 'Internal server error' });
    }
  });

  // GET /Shows/NextUp
  router.get('/Shows/NextUp', requireAuth, async (req, res) => {
    try {
      const params = { ...req.query };
      const virtualSeriesId = params.SeriesId;

      if (virtualSeriesId) {
        const resolved = req.resolveId(virtualSeriesId);
        if (!resolved) {
          return res.json({ Items: [], TotalRecordCount: 0 });
        }

        const selected = await fetchSeriesScopedItems({
          resolved,
          upstreamManager,
          fetchItems: async (inst) => {
            const upstreamParams = { ...params, UserId: inst.client.userId, SeriesId: inst.originalId };
            return inst.client.request('GET', '/Shows/NextUp', { params: upstreamParams });
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

      // No SeriesId: Global NextUp query
      const onlineClients = upstreamManager.getOnlineClients();
      const results = await Promise.allSettled(
        onlineClients.map(async (client) => {
          const params = { ...req.query, UserId: client.userId };
          const data = await client.request('GET', '/Shows/NextUp', { params });
          return { serverIndex: client.serverIndex, data };
        })
      );

      const merged = upstreamManager.mergeItemsResults(
        results.filter(r => r.status === 'fulfilled').map(r => r.value)
      );

      if (merged._serverIndices) {
        for (let i = 0; i < merged.Items.length; i++) {
          rewriteResponseIds(merged.Items[i], merged._serverIndices[i], idManager, config.server.id, authManager.getProxyUserId());
        }
        delete merged._serverIndices;
      }

      res.json(merged);
    } catch (err) {
      logger.error(`Error in GET NextUp: ${err.message}`);
      res.status(500).json({ message: 'Internal server error' });
    }
  });

  // GET /Search/Hints — parallel search across all servers
  router.get('/Search/Hints', requireAuth, async (req, res) => {
    try {
      const onlineClients = upstreamManager.getOnlineClients();
      const results = await Promise.allSettled(
        onlineClients.map(async (client) => {
          const params = { ...req.query, UserId: client.userId };
          const data = await client.request('GET', '/Search/Hints', { params });
          return { serverIndex: client.serverIndex, data };
        })
      );

      for (let i = 0; i < results.length; i++) {
        if (results[i].status === 'rejected') {
          const client = onlineClients[i];
          logger.warn(`[${client.name}] Search/Hints failed: ${results[i].reason?.message || 'unknown'}`);
        }
      }

      // Collect per-server hint arrays for interleaving
      const serverHintArrays = [];
      for (const r of results) {
        if (r.status === 'fulfilled') {
          const { serverIndex, data } = r.value;
          const hints = data.SearchHints || data.Items || [];
          serverHintArrays.push({ serverIndex, hints });
        }
      }

      // Interleave results from all servers and deduplicate
      const allHints = [];
      const seenKeys = new Map();

      function getHintKey(hint) {
        if (hint.ProviderIds && hint.ProviderIds.Tmdb) return `tmdb:${hint.ProviderIds.Tmdb}`;
        const type = hint.Type || '';
        if (type === 'Movie' || type === 'Series' || type === 'Audio' || type === 'MusicAlbum') {
          return `name:${(hint.Name || '').toLowerCase()}:${hint.ProductionYear || ''}`;
        }
        if (type === 'Episode' && hint.Series && hint.ParentIndexNumber != null && hint.IndexNumber != null) {
          return `ep:${hint.Series.toLowerCase()}:S${hint.ParentIndexNumber}E${hint.IndexNumber}`;
        }
        return null;
      }

      const maxLen = Math.max(0, ...serverHintArrays.map(a => a.hints.length));
      for (let i = 0; i < maxLen; i++) {
        for (const arr of serverHintArrays) {
          if (i >= arr.hints.length) continue;
          const hint = arr.hints[i];
          const key = getHintKey(hint);

          if (key) {
            const existingVirtualId = seenKeys.get(key);
            if (existingVirtualId) {
              // Duplicate: associate with existing virtual ID and skip
              idManager.associateAdditionalInstance(existingVirtualId, String(hint.Id || hint.ItemId), arr.serverIndex);
              continue;
            }
            const virtualId = idManager.getOrCreateVirtualId(String(hint.Id || hint.ItemId), arr.serverIndex);
            seenKeys.set(key, virtualId);
          }

          rewriteResponseIds(hint, arr.serverIndex, idManager, config.server.id, authManager.getProxyUserId());
          allHints.push(hint);
        }
      }

      res.json({
        SearchHints: allHints,
        TotalRecordCount: allHints.length,
      });
    } catch (err) {
      logger.error(`Error in GET Search/Hints: ${err.message}`);
      res.status(500).json({ message: 'Internal server error' });
    }
  });

  // GET /Genres, /MusicGenres, /Studios, /Persons, /Artists — merge from all
  for (const endpoint of ['Genres', 'MusicGenres', 'Studios', 'Persons', 'Artists', 'Artists/AlbumArtists']) {
    router.get(`/${endpoint}`, requireAuth, async (req, res) => {
      try {
        const onlineClients = upstreamManager.getOnlineClients();
        const results = await Promise.allSettled(
          onlineClients.map(async (client) => {
            const params = { ...req.query, UserId: client.userId };
            const data = await client.request('GET', `/${endpoint}`, { params });
            return { serverIndex: client.serverIndex, data };
          })
        );

        const merged = upstreamManager.mergeItemsResults(
          results.filter(r => r.status === 'fulfilled').map(r => r.value)
        );

        if (merged._serverIndices) {
          for (let i = 0; i < merged.Items.length; i++) {
            rewriteResponseIds(merged.Items[i], merged._serverIndices[i], idManager, config.server.id, authManager.getProxyUserId());
          }
          delete merged._serverIndices;
        }

        res.json(merged);
      } catch (err) {
        logger.error(`Error in GET ${endpoint}: ${err.message}`);
        res.status(500).json({ message: 'Internal server error' });
      }
    });
  }

  return router;
}

module.exports = { createLibraryRoutes };

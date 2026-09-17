const { EmbyClient } = require('./emby-client');
const logger = require('./utils/logger');

/**
 * Manages all upstream Emby server connections.
 */
function createUpstreamManager(config, idManager) {
  const clients = config.upstream.map((serverConfig, index) => {
    return new EmbyClient(serverConfig, index, config.proxies || [], config.timeouts || {});
  });

  /**
   * Login to all upstream servers.
   */
  async function loginAll() {
    const results = await Promise.allSettled(
      clients.map(client => client.login())
    );
    const online = clients.filter(c => c.online).length;
    logger.info(`Upstream login complete: ${online}/${clients.length} servers online`);
    if (online === 0) {
      logger.warn('No upstream servers are online!');
    }
  }

  /**
   * Get a specific client by server index.
   */
  function getClient(serverIndex) {
    return clients[serverIndex] || null;
  }

  /**
   * Get all online clients.
   */
  function getOnlineClients() {
    return clients.filter(c => c.online && (c.userId || c.config.apiKey));
  }

  /**
   * Get the client for a virtual ID by resolving it.
   */
  function getClientForVirtualId(virtualId) {
    const resolved = idManager.resolveVirtualId(virtualId);
    if (!resolved) return null;
    const client = clients[resolved.serverIndex];
    if (!client || !client.online) return null;
    return { client, originalId: resolved.originalId, serverIndex: resolved.serverIndex };
  }

  /**
   * Execute a request on all online servers in parallel with a strict global timeout.
   */
  async function requestAll(method, path, options = {}, globalTimeout) {
    globalTimeout = globalTimeout || (config.timeouts && config.timeouts.global) || 15000;
    const onlineClients = getOnlineClients();
    const controller = new AbortController();
    const timeoutId = setTimeout(() => controller.abort(), globalTimeout);

    try {
      const results = await Promise.allSettled(
        onlineClients.map(async (client) => {
          const data = await client.request(method, path, {
            ...options,
            signal: controller.signal
          });
          return { serverIndex: client.serverIndex, data };
        })
      );

      clearTimeout(timeoutId);

      for (let i = 0; i < results.length; i++) {
        if (results[i].status === 'rejected') {
          const client = onlineClients[i];
          const reason = results[i].reason?.message || String(results[i].reason);
          logger.warn(`[${client.name}] ${method} ${path} failed: ${reason}`);
        }
      }

      return results
        .filter(r => r.status === 'fulfilled')
        .map(r => r.value);
    } catch (err) {
      clearTimeout(timeoutId);
      return [];
    }
  }

  /**
   * Execute a request on all online servers, expecting Items responses,
   * and merge them with interleaving.
   */
  async function requestAllAndMergeItems(method, path, options = {}) {
    const startTime = Date.now();
    const globalTimeout = (config.timeouts && config.timeouts.global) || 15000;
    const results = await requestAll(method, path, options, globalTimeout);
    return mergeItemsResults(results, startTime, 20000);
  }

  /**
   * Merge multiple Items responses by interleaving and deduplicating.
   * Optimized with 5s global fuse.
   */
  function mergeItemsResults(results, startTime = Date.now(), timeLimit = 5000) {
    if (results.length === 0) return { Items: [], TotalRecordCount: 0, StartIndex: 0 };

    const allItemArrays = results.map(r => ({
      serverIndex: r.serverIndex,
      items: r.data.Items || r.data.items || [],
    }));

    const mergedItems = [];
    const serverIndices = [];
    const seenMap = new Map();

    function getItemKey(item) {
      if (item.ProviderIds && item.ProviderIds.Tmdb) return `tmdb:${item.ProviderIds.Tmdb}`;
      if (item.Type === 'Movie' || item.Type === 'Series') {
        return `name:${(item.Name || '').toLowerCase()}:${item.ProductionYear || ''}`;
      }
      if (item.Type === 'Episode' && item.SeriesName && item.ParentIndexNumber != null && item.IndexNumber != null) {
        return `ep:${item.SeriesName.toLowerCase()}:S${item.ParentIndexNumber}E${item.IndexNumber}`;
      }
      return null;
    }

    const maxLen = Math.max(...allItemArrays.map(a => a.items.length));
    for (let i = 0; i < maxLen; i++) {
      // Check timeout every row of interleaving
      if (Date.now() - startTime > timeLimit) break;

      for (const arr of allItemArrays) {
        if (i < arr.items.length) {
          const item = arr.items[i];
          const key = getItemKey(item);

          if (key) {
            const existingVirtualId = seenMap.get(key);
            if (existingVirtualId) {
              idManager.associateAdditionalInstance(existingVirtualId, item.Id, arr.serverIndex);
              continue;
            } else {
              const virtualId = idManager.getOrCreateVirtualId(item.Id, arr.serverIndex);
              seenMap.set(key, virtualId);
              mergedItems.push(item);
              serverIndices.push(arr.serverIndex);
            }
          } else {
            mergedItems.push(item);
            serverIndices.push(arr.serverIndex);
          }
        }
      }
    }

    return {
      Items: mergedItems,
      _serverIndices: serverIndices,
      TotalRecordCount: mergedItems.length,
      StartIndex: 0,
    };
  }

  /**
   * Merge multiple Seasons responses by IndexNumber.
   */
  function mergeSeasonsResults(results, startTime = Date.now(), timeLimit = 1000) {
    if (results.length === 0) return { Items: [], TotalRecordCount: 0, StartIndex: 0 };

    const mergedSeasons = [];
    const serverIndices = [];
    const seenIndexMap = new Map();

    for (const r of results) {
      if (Date.now() - startTime > timeLimit) break;

      const items = r.data.Items || r.data.items || [];
      for (const item of items) {
        const idx = item.IndexNumber;
        if (idx === undefined) {
          mergedSeasons.push(item);
          serverIndices.push(r.serverIndex);
          continue;
        }

        const existingVirtualId = seenIndexMap.get(idx);
        if (existingVirtualId) {
          idManager.associateAdditionalInstance(existingVirtualId, item.Id, r.serverIndex);
        } else {
          const virtualId = idManager.getOrCreateVirtualId(item.Id, r.serverIndex);
          seenIndexMap.set(idx, virtualId);
          mergedSeasons.push(item);
          serverIndices.push(r.serverIndex);
        }
      }
    }

    return {
      Items: mergedSeasons,
      _serverIndices: serverIndices,
      TotalRecordCount: mergedSeasons.length,
      StartIndex: 0,
    };
  }

  /**
   * Merge multiple Episodes responses by SeasonIndex:EpisodeIndex.
   * Optimized for 1000s of episodes and 20+ servers on low-end VPS.
   */
  function mergeEpisodesResults(results, startTime = Date.now(), timeLimit = 5000) {
    if (results.length === 0) return { Items: [], TotalRecordCount: 0, StartIndex: 0 };

    const slots = new Map(); // "season:episode" -> Array of { item, serverIndex }

    for (const r of results) {
      const items = r.data.Items || r.data.items || [];
      for (const item of items) {
        const sIdx = item.ParentIndexNumber; // Season idx
        const eIdx = item.IndexNumber; // Episode idx

        if (sIdx === undefined || eIdx === undefined) continue;

        const key = `${sIdx}:${eIdx}`;
        if (!slots.has(key)) slots.set(key, []);
        slots.get(key).push({ item, serverIndex: r.serverIndex });
      }
    }

    const finalItems = [];
    const finalIndices = [];

    // Sort keys: season then episode
    const sortedKeys = Array.from(slots.keys()).sort((a, b) => {
      const [as, ae] = a.split(':').map(Number);
      const [bs, be] = b.split(':').map(Number);
      return as - bs || ae - be;
    });

    let processedCount = 0;
    for (const key of sortedKeys) {
      // Short circuit every 100 items if we exceed time limit
      if (processedCount++ % 100 === 0 && (Date.now() - startTime > timeLimit)) {
        logger.warn(`Merging episodes timed out after ${processedCount} items`);
        break;
      }

      const instances = slots.get(key);

      // Pick the best representative using a scoring system
      const best = instances.reduce((prev, curr) => {
        const confP = clients[prev.serverIndex].config;
        const confC = clients[curr.serverIndex].config;

        // 1. Manual Priority
        if (confC.priorityMetadata && !confP.priorityMetadata) return curr;
        if (confP.priorityMetadata && !confC.priorityMetadata) return prev;

        // 2. Language (Chinese check)
        const hasChineseP = /[\u4e00-\u9fa5]/.test(prev.item.Overview || '');
        const hasChineseC = /[\u4e00-\u9fa5]/.test(curr.item.Overview || '');
        if (hasChineseC && !hasChineseP) return curr;
        if (hasChineseP && !hasChineseC) return prev;

        // 3. Overview Length
        if ((curr.item.Overview || '').length > (prev.item.Overview || '').length) return curr;

        // 4. Server Order
        return prev.serverIndex < curr.serverIndex ? prev : curr;
      });

      const virtualId = idManager.getOrCreateVirtualId(best.item.Id, best.serverIndex);

      // Associate all instances
      for (const inst of instances) {
        idManager.associateAdditionalInstance(virtualId, inst.item.Id, inst.serverIndex);
      }

      finalItems.push(best.item);
      finalIndices.push(best.serverIndex);
    }

    return {
      Items: finalItems,
      _serverIndices: finalIndices,
      TotalRecordCount: finalItems.length,
      StartIndex: 0,
    };
  }

  let healthIntervalId = null;

  /**
   * Run periodic health checks on all servers.
   */
  function startHealthChecks(intervalMs) {
    intervalMs = intervalMs || (config.timeouts && config.timeouts.healthInterval) || 60000;
    healthIntervalId = setInterval(async () => {
      await Promise.allSettled(clients.map(async (client) => {
        const wasOnline = client.online;
        await client.healthCheck();
        if (wasOnline && !client.online) {
          logger.warn(`[${client.name}] went offline`);
        } else if (!wasOnline && client.online) {
          logger.info(`[${client.name}] back online`);
        }
      }));
    }, intervalMs);
  }

  function stopHealthChecks() {
    if (healthIntervalId) {
      clearInterval(healthIntervalId);
      healthIntervalId = null;
    }
  }

  return {
    clients,
    loginAll,
    getClient,
    getOnlineClients,
    getClientForVirtualId,
    requestAll,
    requestAllAndMergeItems,
    mergeItemsResults,
    mergeSeasonsResults,
    mergeEpisodesResults,
    startHealthChecks,
    stopHealthChecks,
  };
}

module.exports = { createUpstreamManager };

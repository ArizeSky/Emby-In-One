function buildSeriesInstances(resolved, upstreamManager) {
  return [
    { originalId: resolved.originalId, serverIndex: resolved.serverIndex, client: resolved.client },
    ...(resolved.otherInstances || []).map((inst) => ({
      ...inst,
      client: upstreamManager.getClient(inst.serverIndex),
    })),
  ].filter((inst, index, all) => {
    if (!inst.client || !inst.client.online) return false;
    return all.findIndex((other) =>
      other.serverIndex === inst.serverIndex && other.originalId === inst.originalId
    ) === index;
  });
}

function belongsToSeries(item, originalIds) {
  if (!item || typeof item !== 'object') return false;

  return Boolean(
    (item.SeriesId && originalIds.has(item.SeriesId)) ||
    (item.ParentId && originalIds.has(item.ParentId)) ||
    (item.GrandparentId && originalIds.has(item.GrandparentId))
  );
}

function filterSeriesItems(items, originalIds) {
  return (items || []).filter((item) => belongsToSeries(item, originalIds));
}

async function fetchSeriesScopedItems({ resolved, upstreamManager, fetchItems }) {
  const instances = buildSeriesInstances(resolved, upstreamManager);
  const originalIds = new Set(instances.map((inst) => inst.originalId));

  for (const inst of instances) {
    try {
      const payload = await fetchItems(inst);
      const rawItems = Array.isArray(payload) ? payload : (payload.Items || payload.items || []);
      const filteredItems = filterSeriesItems(rawItems, originalIds);

      if (filteredItems.length > 0) {
        return {
          items: filteredItems,
          serverIndex: inst.serverIndex,
        };
      }
    } catch (_) {
      continue;
    }
  }

  return {
    items: [],
    serverIndex: null,
  };
}

module.exports = {
  buildSeriesInstances,
  belongsToSeries,
  filterSeriesItems,
  fetchSeriesScopedItems,
};

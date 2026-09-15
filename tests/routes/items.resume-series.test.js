const test = require('node:test');
const assert = require('node:assert/strict');
const { createItemRoutes } = require('../../src/routes/items');

function getRouteHandler(router, path, method = 'get') {
  const layer = router.stack.find((entry) => entry.route && entry.route.path === path && entry.route.methods[method]);
  if (!layer) throw new Error(`Route not found: ${method.toUpperCase()} ${path}`);
  return layer.route.stack[layer.route.stack.length - 1].handle;
}

async function invokeRoute(handler, req) {
  const res = {
    statusCode: 200,
    body: undefined,
    status(code) {
      this.statusCode = code;
      return this;
    },
    json(payload) {
      this.body = payload;
      return this;
    },
    end() {
      return this;
    },
  };

  await handler(req, res);
  return res;
}

function createDeps() {
  const clients = [];
  const upstreamManager = {
    clients,
    getClient(serverIndex) {
      return clients[serverIndex] || null;
    },
    getOnlineClients() {
      return clients.filter((client) => client.online);
    },
    mergeItemsResults(results) {
      const items = [];
      const indices = [];
      for (const result of results) {
        const list = result.data.Items || result.data.items || [];
        for (const item of list) {
          items.push(item);
          indices.push(result.serverIndex);
        }
      }
      return {
        Items: items,
        _serverIndices: indices,
        TotalRecordCount: items.length,
        StartIndex: 0,
      };
    },
  };

  const authManager = {
    getProxyUserId() {
      return 'proxy-user';
    },
  };

  const idManager = {
    getOrCreateVirtualId(originalId, serverIndex) {
      return `v${serverIndex}-${originalId}`;
    },
  };

  const config = {
    server: {
      id: 'proxy-server',
    },
    timeouts: {},
  };

  return {
    router: createItemRoutes(config, authManager, idManager, upstreamManager),
    upstreamManager,
  };
}

test('Resume with ParentId returns only primary-instance history and does not query secondary', async () => {
  const { router, upstreamManager } = createDeps();
  const handler = getRouteHandler(router, '/Users/:userId/Items/Resume');

  let secondaryCalls = 0;
  const primaryClient = {
    userId: 'user-a',
    online: true,
    async request() {
      return {
        Items: [
          {
            Id: 'ep-a-2',
            SeriesId: 'series-a',
            ParentId: 'season-a',
            SeriesName: '琉璃的宝石',
            ParentIndexNumber: 1,
            IndexNumber: 2,
            Source: 'primary',
          },
        ],
      };
    },
  };
  const secondaryClient = {
    userId: 'user-b',
    online: true,
    async request() {
      secondaryCalls += 1;
      return {
        Items: [
          {
            Id: 'ep-b-1',
            SeriesId: 'series-b',
            ParentId: 'season-b',
            SeriesName: 'Ruri no Houseki',
            ParentIndexNumber: 1,
            IndexNumber: 1,
            Source: 'secondary',
          },
        ],
      };
    },
  };

  upstreamManager.clients.push(primaryClient, secondaryClient);

  const req = {
    query: { ParentId: 'virtual-series' },
    params: { userId: 'proxy-user' },
    resolveId(virtualId) {
      assert.equal(virtualId, 'virtual-series');
      return {
        originalId: 'series-a',
        serverIndex: 0,
        client: primaryClient,
        otherInstances: [{ originalId: 'series-b', serverIndex: 1 }],
      };
    },
  };

  const res = await invokeRoute(handler, req);

  assert.equal(res.statusCode, 200);
  assert.deepEqual(res.body.Items.map((item) => item.Source), ['primary']);
  assert.equal(secondaryCalls, 0);
});

test('Resume falls back to the next instance when the primary payload belongs to another series', async () => {
  const { router, upstreamManager } = createDeps();
  const handler = getRouteHandler(router, '/Users/:userId/Items/Resume');

  const primaryClient = {
    userId: 'user-a',
    online: true,
    async request() {
      return {
        Items: [
          {
            Id: 'wrong-1',
            SeriesId: 'other-series',
            ParentId: 'other-season',
            SeriesName: 'Re:Zero',
            ParentIndexNumber: 1,
            IndexNumber: 1,
            Source: 'wrong-primary',
          },
        ],
      };
    },
  };
  const secondaryClient = {
    userId: 'user-b',
    online: true,
    async request() {
      return {
        Items: [
          {
            Id: 'ep-b-2',
            SeriesId: 'series-b',
            ParentId: 'season-b',
            SeriesName: 'Ruri no Houseki',
            ParentIndexNumber: 1,
            IndexNumber: 2,
            Source: 'secondary',
          },
        ],
      };
    },
  };

  upstreamManager.clients.push(primaryClient, secondaryClient);

  const req = {
    query: { ParentId: 'virtual-series' },
    params: { userId: 'proxy-user' },
    resolveId() {
      return {
        originalId: 'series-a',
        serverIndex: 0,
        client: primaryClient,
        otherInstances: [{ originalId: 'series-b', serverIndex: 1 }],
      };
    },
  };

  const res = await invokeRoute(handler, req);

  assert.equal(res.statusCode, 200);
  assert.deepEqual(res.body.Items.map((item) => item.Source), ['secondary']);
});

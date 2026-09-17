const test = require('node:test');
const assert = require('node:assert/strict');
const { createLibraryRoutes } = require('../../src/routes/library');

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
    resolveVirtualId() {
      return null;
    },
  };

  const config = {
    server: {
      id: 'proxy-server',
    },
    timeouts: {},
  };

  return {
    router: createLibraryRoutes(config, authManager, idManager, upstreamManager),
    upstreamManager,
  };
}

test('NextUp with SeriesId uses only the primary instance result set', async () => {
  const { router, upstreamManager } = createDeps();
  const handler = getRouteHandler(router, '/Shows/NextUp');

  let secondaryCalls = 0;
  const primaryClient = {
    userId: 'user-a',
    online: true,
    async request() {
      return {
        Items: [
          { Id: 'a-2', SeriesId: 'series-a', ParentId: 'season-a', IndexNumber: 2, Source: 'primary' },
          { Id: 'a-3', SeriesId: 'series-a', ParentId: 'season-a', IndexNumber: 3, Source: 'primary' },
          { Id: 'a-4', SeriesId: 'series-a', ParentId: 'season-a', IndexNumber: 4, Source: 'primary' },
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
          { Id: 'b-1', SeriesId: 'series-b', ParentId: 'season-b', IndexNumber: 1, Source: 'secondary' },
          { Id: 'b-2', SeriesId: 'series-b', ParentId: 'season-b', IndexNumber: 2, Source: 'secondary' },
          { Id: 'b-3', SeriesId: 'series-b', ParentId: 'season-b', IndexNumber: 3, Source: 'secondary' },
        ],
      };
    },
  };

  upstreamManager.clients.push(primaryClient, secondaryClient);

  const req = {
    query: { SeriesId: 'virtual-series' },
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
  assert.deepEqual(res.body.Items.map((item) => item.Source), ['primary', 'primary', 'primary']);
  assert.equal(secondaryCalls, 0);
});

test('Global NextUp without SeriesId keeps cross-server aggregation behavior', async () => {
  const { router, upstreamManager } = createDeps();
  const handler = getRouteHandler(router, '/Shows/NextUp');

  const primaryClient = {
    userId: 'user-a',
    online: true,
    async request() {
      return {
        Items: [{ Id: 'a-2', SeriesId: 'series-a', ParentId: 'season-a', IndexNumber: 2, Source: 'primary' }],
      };
    },
  };
  const secondaryClient = {
    userId: 'user-b',
    online: true,
    async request() {
      return {
        Items: [{ Id: 'b-5', SeriesId: 'series-b', ParentId: 'season-b', IndexNumber: 5, Source: 'secondary' }],
      };
    },
  };

  upstreamManager.clients.push(primaryClient, secondaryClient);

  const req = {
    query: {},
    resolveId() {
      return null;
    },
  };

  const res = await invokeRoute(handler, req);

  assert.equal(res.statusCode, 200);
  assert.deepEqual(res.body.Items.map((item) => item.Source), ['primary', 'secondary']);
});

const test = require('node:test');
const assert = require('node:assert/strict');
const { EmbyClient } = require('../../src/emby-client');
const { createAdminRoutes } = require('../../src/routes/admin');

function getRouteHandler(router, path, method = 'get') {
  const layer = router.stack.find((entry) => entry.route && entry.route.path === path && entry.route.methods[method]);
  if (!layer) throw new Error(`Route not found: ${method.toUpperCase()} ${path}`);
  return layer.route.stack[layer.route.stack.length - 1].handle;
}

async function invokeRoute(handler, req) {
  const res = {
    statusCode: 200,
    body: undefined,
    headers: {},
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
    setHeader(key, value) {
      this.headers[key.toLowerCase()] = value;
    },
  };

  await handler(req, res);
  return res;
}

function createDeps() {
  const config = {
    server: { id: 'proxy-server', name: 'Proxy', port: 8096 },
    admin: { username: 'admin', password: 'plain' },
    playback: { mode: 'proxy' },
    timeouts: {},
    proxies: [],
    upstream: [],
  };

  const upstreamManager = {
    clients: [],
    getClient(index) {
      return this.clients[index] || null;
    },
  };

  const router = createAdminRoutes(
    config,
    {
      getStats() {
        return { mappingCount: 0, persistent: false };
      },
      removeByServerIndex() {},
      shiftServerIndices() {},
    },
    upstreamManager,
    {
      revokeToken() {},
    }
  );

  return { config, upstreamManager, router };
}

test('POST /api/upstream does not mutate runtime config when validation/login fails', async () => {
  const { config, upstreamManager, router } = createDeps();
  const handler = getRouteHandler(router, '/api/upstream', 'post');

  const originalLogin = EmbyClient.prototype.login;
  EmbyClient.prototype.login = async function login() {
    throw new Error('login failed');
  };

  try {
    const res = await invokeRoute(handler, {
      body: {
        name: 'Server A',
        url: 'https://example.com',
        username: 'user',
        password: 'pass',
      },
      query: {},
      params: {},
    });

    assert.equal(res.statusCode, 500);
    assert.equal(config.upstream.length, 0);
    assert.equal(upstreamManager.clients.length, 0);
  } finally {
    EmbyClient.prototype.login = originalLogin;
  }
});

test('PUT /api/upstream/:index keeps previous config and client when validation/login fails', async () => {
  const { config, upstreamManager, router } = createDeps();
  const handler = getRouteHandler(router, '/api/upstream/:index', 'put');

  const existingConfig = {
    name: 'Existing',
    url: 'https://existing.example.com',
    username: 'old-user',
    password: 'old-pass',
    playbackMode: 'proxy',
    spoofClient: 'none',
    followRedirects: true,
    proxyId: null,
    priorityMetadata: false,
  };
  const oldClient = { marker: 'old-client' };
  config.upstream.push({ ...existingConfig });
  upstreamManager.clients.push(oldClient);

  const originalLogin = EmbyClient.prototype.login;
  EmbyClient.prototype.login = async function login() {
    throw new Error('login failed');
  };

  try {
    const res = await invokeRoute(handler, {
      params: { index: '0' },
      body: {
        name: 'Broken',
        url: 'https://broken.example.com',
        username: 'new-user',
        password: 'new-pass',
        spoofClient: 'official',
      },
      query: {},
    });

    assert.equal(res.statusCode, 500);
    assert.deepEqual(config.upstream[0], existingConfig);
    assert.equal(upstreamManager.clients[0], oldClient);
  } finally {
    EmbyClient.prototype.login = originalLogin;
  }
});

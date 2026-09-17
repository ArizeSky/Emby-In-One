const test = require('node:test');
const assert = require('node:assert/strict');
const capturedHeaders = require('../../src/utils/captured-headers');
const requestStore = require('../../src/utils/request-store');
const { EmbyClient } = require('../../src/emby-client');

test.beforeEach(() => {
  capturedHeaders.clear();
});

test('captured headers are isolated per proxy token and latest info is preserved', () => {
  capturedHeaders.set('token-a', {
    'user-agent': 'UA-A',
    'x-emby-client': 'Client-A',
    'x-emby-device-name': 'Device-A',
  });
  capturedHeaders.set('token-b', {
    'user-agent': 'UA-B',
    'x-emby-client': 'Client-B',
    'x-emby-device-name': 'Device-B',
  });

  assert.equal(capturedHeaders.get('token-a')['user-agent'], 'UA-A');
  assert.equal(capturedHeaders.get('token-b')['user-agent'], 'UA-B');
  assert.equal(capturedHeaders.getInfo().userAgent, 'UA-B');
});

test('passthrough headers use token-scoped captured identity when no live client headers exist', () => {
  capturedHeaders.set('token-a', {
    'user-agent': 'UA-A',
    'x-emby-client': 'Client-A',
    'x-emby-client-version': '1.0',
    'x-emby-device-name': 'Device-A',
    'x-emby-device-id': 'device-a',
  });
  capturedHeaders.set('token-b', {
    'user-agent': 'UA-B',
    'x-emby-client': 'Client-B',
    'x-emby-client-version': '2.0',
    'x-emby-device-name': 'Device-B',
    'x-emby-device-id': 'device-b',
  });

  const client = new EmbyClient({ name: 'A', url: 'https://example.com', spoofClient: 'passthrough' }, 0, [], {});

  const headersA = requestStore.run({ headers: {}, proxyToken: 'token-a' }, () => client._getPassthroughHeaders().headers);
  const headersB = requestStore.run({ headers: {}, proxyToken: 'token-b' }, () => client._getPassthroughHeaders().headers);
  const fallbackHeaders = requestStore.run({ headers: {}, proxyToken: null }, () => client._getPassthroughHeaders().headers);

  assert.equal(headersA['User-Agent'], 'UA-A');
  assert.equal(headersA['X-Emby-Client'], 'Client-A');
  assert.equal(headersB['User-Agent'], 'UA-B');
  assert.equal(headersB['X-Emby-Client'], 'Client-B');
  assert.match(fallbackHeaders['User-Agent'], /Infuse/);
});

test('live request headers override token-scoped captured identity', () => {
  capturedHeaders.set('token-a', {
    'user-agent': 'UA-A',
    'x-emby-client': 'Client-A',
  });

  const client = new EmbyClient({ name: 'A', url: 'https://example.com', spoofClient: 'passthrough' }, 0, [], {});

  const headers = requestStore.run({
    headers: {
      'user-agent': 'Live-UA',
      'x-emby-client': 'Live-Client',
      'x-emby-client-version': '9.9',
      'x-emby-device-name': 'Live Device',
      'x-emby-device-id': 'live-device',
    },
    proxyToken: 'token-a',
  }, () => client._getPassthroughHeaders().headers);

  assert.equal(headers['User-Agent'], 'Live-UA');
  assert.equal(headers['X-Emby-Client'], 'Live-Client');
});

test('deleting a token removes its captured identity', () => {
  capturedHeaders.set('token-a', { 'user-agent': 'UA-A' });
  capturedHeaders.delete('token-a');
  assert.equal(capturedHeaders.get('token-a'), null);
  assert.equal(capturedHeaders.getInfo(), null);
});

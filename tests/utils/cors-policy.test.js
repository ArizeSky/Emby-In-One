const test = require('node:test');
const assert = require('node:assert/strict');
const { applyCorsHeaders } = require('../../src/utils/cors-policy');

function createResponseRecorder() {
  const headers = {};
  return {
    header(name, value) {
      headers[name.toLowerCase()] = value;
      return this;
    },
    getHeader(name) {
      return headers[name.toLowerCase()];
    },
  };
}

test('admin api requests do not reflect arbitrary origins', () => {
  const res = createResponseRecorder();

  applyCorsHeaders({
    path: '/admin/api/status',
    headers: { origin: 'https://evil.example' },
    method: 'GET',
  }, res);

  assert.equal(res.getHeader('access-control-allow-origin'), undefined);
  assert.equal(res.getHeader('access-control-allow-methods'), 'GET, POST, PUT, DELETE, OPTIONS');
});

test('emby client routes remain permissive for cross-origin access', () => {
  const res = createResponseRecorder();

  applyCorsHeaders({
    path: '/System/Info/Public',
    headers: { origin: 'https://evil.example' },
    method: 'GET',
  }, res);

  assert.equal(res.getHeader('access-control-allow-origin'), '*');
});

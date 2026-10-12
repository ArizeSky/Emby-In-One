'use strict';
/* Session survival across transient startup failures.
 *
 * onMounted used to call doLogout() for every error from the /Users/{id}
 * check, so a restarting server or a network blip destroyed a perfectly valid
 * token. Only an explicit 401 may end the session.
 */
const test = require('node:test');
const assert = require('node:assert');
const { harness } = require('./web-ui-harness.cjs');

const userObject = { Name: 'user', Policy: { IsAdministrator: false } };

async function runMounted(h) {
  assert.ok(h.mountedHooks.length >= 1, 'onMounted hook was not registered');
  await h.mountedHooks[0]();
}

test('a transient 502 on the startup user check keeps the session', async () => {
  const h = harness({ reply: () => ({ __status: 502, __body: 'bad gateway' }) });
  await runMounted(h);
  assert.strictEqual(h.storage.getItem('eio_token'), 'token', 'token was destroyed by a transient failure');
  assert.strictEqual(h.storage.getItem('eio_user_id'), 'user', 'user id was destroyed by a transient failure');
  assert.strictEqual(h.app.page.value, 'login', 'the app shows the login form');
  assert.ok(h.app.loginError.value.includes('暂时无法连接'), 'the error explains the transient failure');
});

test('a network failure on the startup user check keeps the session', async () => {
  const h = harness({
    reply: () => { throw new TypeError('fetch failed'); },
  });
  await runMounted(h);
  assert.strictEqual(h.storage.getItem('eio_token'), 'token', 'token was destroyed by a network failure');
  assert.strictEqual(h.app.page.value, 'login');
});

test('an explicit 401 on the startup user check ends the session', async () => {
  const h = harness({ reply: () => ({ __status: 401, __body: '{"message":"Authentication required"}' }) });
  await runMounted(h);
  assert.strictEqual(h.storage.getItem('eio_token'), null, 'a rejected token must be dropped');
  assert.strictEqual(h.storage.getItem('eio_user_id'), null);
});

test('a successful startup check enters the app', async () => {
  const h = harness({ reply: url => (url.pathname.startsWith('/Users/') ? userObject : { Items: [] }) });
  await runMounted(h);
  assert.strictEqual(h.storage.getItem('eio_token'), 'token');
  assert.strictEqual(h.app.currentUser.value, userObject);
});

test('an admin token on the web client is dropped', async () => {
  const h = harness({ reply: url => (url.pathname.startsWith('/Users/') ? { Name: 'admin', Policy: { IsAdministrator: true } } : { Items: [] }) });
  await runMounted(h);
  assert.strictEqual(h.storage.getItem('eio_token'), null, 'an admin token must not keep a web session');
});

/**
 * AsyncLocalStorage-based store for passing request-scoped client context
 * through to upstream EmbyClient calls without threading arguments through
 * every route handler.
 *
 * Store shape:
 *   {
 *     headers: req.headers,
 *     proxyToken: req.proxyToken || null,
 *   }
 */
const { AsyncLocalStorage } = require('async_hooks');
module.exports = new AsyncLocalStorage();
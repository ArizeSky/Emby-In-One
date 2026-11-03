// Load the shipped webpack adapter and gate without substituting lifecycle behavior.
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
module.exports = function loadMuxLifecycle() {
  const factories = {}, cache = {};
  const self = { webpackChunkAVPlayer: { push: ([, modules]) => Object.assign(factories, modules) } };
  const context = vm.createContext({ self, setTimeout, clearTimeout });
  for (const file of ['mux-producers.js', 'mux-producer-lifecycle.js']) {
    vm.runInContext(fs.readFileSync(path.join(__dirname,
      '../../public/vendor/web/libmedia', file), 'utf8'), context, { filename: file });
  }
  function requireModule(id) {
    if (cache[id]) return cache[id].exports;
    const module = cache[id] = { exports: {} };
    factories[id](module, module.exports, requireModule);
    return module.exports;
  }
  return requireModule(90198);
};

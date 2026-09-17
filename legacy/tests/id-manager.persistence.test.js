const test = require('node:test');
const assert = require('node:assert/strict');
const os = require('os');
const path = require('path');
const Module = require('module');

const dbStoreByPath = new Map();

class FakeDatabase {
  constructor(dbPath) {
    this.dbPath = dbPath;
    if (!dbStoreByPath.has(dbPath)) {
      dbStoreByPath.set(dbPath, {
        idMappings: [],
        additionalInstances: [],
      });
    }
    this.store = dbStoreByPath.get(dbPath);
  }

  pragma() {}

  exec() {}

  prepare(sql) {
    const normalized = sql.replace(/\s+/g, ' ').trim().toLowerCase();
    const store = this.store;

    if (normalized.startsWith('insert or ignore into id_mappings')) {
      return {
        run(virtualId, originalId, serverIndex) {
          const exists = store.idMappings.some((row) => row.virtual_id === virtualId);
          if (!exists) store.idMappings.push({ virtual_id: virtualId, original_id: originalId, server_index: serverIndex });
        },
      };
    }

    if (normalized.startsWith('insert or ignore into id_additional_instances')) {
      return {
        run(virtualId, originalId, serverIndex) {
          const exists = store.additionalInstances.some((row) =>
            row.virtual_id === virtualId && row.original_id === originalId && row.server_index === serverIndex
          );
          if (!exists) store.additionalInstances.push({ virtual_id: virtualId, original_id: originalId, server_index: serverIndex });
        },
      };
    }

    if (normalized.startsWith('select original_id, server_index from id_mappings where virtual_id = ?')) {
      return {
        get(virtualId) {
          const row = store.idMappings.find((entry) => entry.virtual_id === virtualId);
          return row ? { original_id: row.original_id, server_index: row.server_index } : undefined;
        },
      };
    }

    if (normalized.startsWith('select virtual_id from id_mappings where original_id = ? and server_index = ?')) {
      return {
        get(originalId, serverIndex) {
          const row = store.idMappings.find((entry) => entry.original_id === originalId && entry.server_index === serverIndex);
          return row ? { virtual_id: row.virtual_id } : undefined;
        },
      };
    }

    if (normalized.startsWith('select count(*) as count from id_mappings')) {
      return {
        get() {
          return { count: store.idMappings.length };
        },
      };
    }

    if (normalized.startsWith('select virtual_id, original_id, server_index from id_mappings')) {
      return {
        all() {
          return store.idMappings.map((row) => ({ ...row }));
        },
      };
    }

    if (normalized.startsWith('select virtual_id, original_id, server_index from id_additional_instances')) {
      return {
        all() {
          return store.additionalInstances.map((row) => ({ ...row }));
        },
      };
    }

    if (normalized.startsWith('delete from id_mappings where server_index = ?')) {
      return {
        run(serverIndex) {
          store.idMappings = store.idMappings.filter((row) => row.server_index !== serverIndex);
        },
      };
    }

    if (normalized.startsWith('delete from id_additional_instances where server_index = ?')) {
      return {
        run(serverIndex) {
          store.additionalInstances = store.additionalInstances.filter((row) => row.server_index !== serverIndex);
        },
      };
    }

    if (normalized.startsWith('delete from id_additional_instances where virtual_id = ?')) {
      return {
        run(virtualId) {
          store.additionalInstances = store.additionalInstances.filter((row) => row.virtual_id !== virtualId);
        },
      };
    }

    if (normalized.startsWith('update id_mappings set server_index = server_index - 1 where server_index > ?')) {
      return {
        run(deletedIndex) {
          store.idMappings.forEach((row) => {
            if (row.server_index > deletedIndex) row.server_index -= 1;
          });
        },
      };
    }

    if (normalized.startsWith('update id_additional_instances set server_index = server_index - 1 where server_index > ?')) {
      return {
        run(deletedIndex) {
          store.additionalInstances.forEach((row) => {
            if (row.server_index > deletedIndex) row.server_index -= 1;
          });
        },
      };
    }

    throw new Error(`Unsupported SQL in fake database: ${sql}`);
  }
}

function withMockedIdManager(fn) {
  const originalLoad = Module._load;
  Module._load = function patchedLoad(request, parent, isMain) {
    if (request === 'better-sqlite3') return FakeDatabase;
    return originalLoad.call(this, request, parent, isMain);
  };

  const modulePath = require.resolve('../src/id-manager');
  delete require.cache[modulePath];
  const { createIdManager } = require('../src/id-manager');

  try {
    return fn(createIdManager);
  } finally {
    delete require.cache[modulePath];
    Module._load = originalLoad;
  }
}

test('otherInstances persist across id-manager restart', () => {
  withMockedIdManager((createIdManager) => {
    const dataDir = path.join(os.tmpdir(), 'emby-in-one-id-manager-test-1');
    const manager1 = createIdManager(dataDir);
    const virtualId = manager1.getOrCreateVirtualId('series-a', 0);
    manager1.associateAdditionalInstance(virtualId, 'series-b', 1);
  });

  withMockedIdManager((createIdManager) => {
    const dataDir = path.join(os.tmpdir(), 'emby-in-one-id-manager-test-1');
    const manager2 = createIdManager(dataDir);
    const [virtualId] = Array.from(dbStoreByPath.get(path.join(dataDir, 'mappings.db')).idMappings).map((row) => row.virtual_id);
    const resolved = manager2.resolveVirtualId(virtualId);
    assert.deepEqual(resolved.otherInstances, [{ originalId: 'series-b', serverIndex: 1 }]);
  });
});

test('persisted otherInstances survive index shifts and server deletion cleanup', () => {
  let virtualId;

  withMockedIdManager((createIdManager) => {
    const dataDir = path.join(os.tmpdir(), 'emby-in-one-id-manager-test-2');
    const manager = createIdManager(dataDir);
    virtualId = manager.getOrCreateVirtualId('series-a', 0);
    manager.associateAdditionalInstance(virtualId, 'series-c', 2);
    manager.shiftServerIndices(1);
  });

  withMockedIdManager((createIdManager) => {
    const dataDir = path.join(os.tmpdir(), 'emby-in-one-id-manager-test-2');
    const manager = createIdManager(dataDir);
    assert.deepEqual(manager.resolveVirtualId(virtualId).otherInstances, [{ originalId: 'series-c', serverIndex: 1 }]);
    manager.removeByServerIndex(1);
  });

  withMockedIdManager((createIdManager) => {
    const dataDir = path.join(os.tmpdir(), 'emby-in-one-id-manager-test-2');
    const manager = createIdManager(dataDir);
    assert.deepEqual(manager.resolveVirtualId(virtualId).otherInstances, []);
  });
});

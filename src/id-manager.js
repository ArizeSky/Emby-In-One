const { v4: uuidv4 } = require('uuid');
const path = require('path');
const fs = require('fs');
const logger = require('./utils/logger');

/**
 * ID Manager: maintains bidirectional mapping between virtual IDs and original IDs.
 * Uses SQLite for persistence when available, falls back to in-memory Map.
 */
function createIdManager(dataDir) {
  let db = null;
  let stmtInsert = null;
  let stmtGetByVirtual = null;
  let stmtGetByOriginal = null;
  let stmtCount = null;
  let stmtInsertAdditional = null;
  let stmtDeleteByServer = null;
  let stmtDeleteAdditionalByServer = null;
  let stmtDeleteAdditionalByVirtual = null;
  let stmtShiftByServer = null;
  let stmtShiftAdditionalByServer = null;

  const virtualToOriginal = new Map();
  const originalToVirtual = new Map();

  try {
    const Database = require('better-sqlite3');
    const dbDir = dataDir || path.resolve(__dirname, '..', 'data');
    if (!fs.existsSync(dbDir)) fs.mkdirSync(dbDir, { recursive: true });

    const dbPath = path.join(dbDir, 'mappings.db');
    db = new Database(dbPath);

    db.pragma('journal_mode = WAL');

    db.exec(`
      CREATE TABLE IF NOT EXISTS id_mappings (
        virtual_id TEXT PRIMARY KEY,
        original_id TEXT NOT NULL,
        server_index INTEGER NOT NULL
      );
      CREATE INDEX IF NOT EXISTS idx_original ON id_mappings(original_id, server_index);
      CREATE TABLE IF NOT EXISTS id_additional_instances (
        virtual_id TEXT NOT NULL,
        original_id TEXT NOT NULL,
        server_index INTEGER NOT NULL,
        UNIQUE(virtual_id, original_id, server_index)
      );
      CREATE INDEX IF NOT EXISTS idx_additional_virtual ON id_additional_instances(virtual_id);
    `);

    stmtInsert = db.prepare('INSERT OR IGNORE INTO id_mappings (virtual_id, original_id, server_index) VALUES (?, ?, ?)');
    stmtGetByVirtual = db.prepare('SELECT original_id, server_index FROM id_mappings WHERE virtual_id = ?');
    stmtGetByOriginal = db.prepare('SELECT virtual_id FROM id_mappings WHERE original_id = ? AND server_index = ?');
    stmtCount = db.prepare('SELECT COUNT(*) as count FROM id_mappings');
    stmtInsertAdditional = db.prepare('INSERT OR IGNORE INTO id_additional_instances (virtual_id, original_id, server_index) VALUES (?, ?, ?)');
    stmtDeleteByServer = db.prepare('DELETE FROM id_mappings WHERE server_index = ?');
    stmtDeleteAdditionalByServer = db.prepare('DELETE FROM id_additional_instances WHERE server_index = ?');
    stmtDeleteAdditionalByVirtual = db.prepare('DELETE FROM id_additional_instances WHERE virtual_id = ?');
    stmtShiftByServer = db.prepare('UPDATE id_mappings SET server_index = server_index - 1 WHERE server_index > ?');
    stmtShiftAdditionalByServer = db.prepare('UPDATE id_additional_instances SET server_index = server_index - 1 WHERE server_index > ?');

    const rows = db.prepare('SELECT virtual_id, original_id, server_index FROM id_mappings').all();
    for (const row of rows) {
      virtualToOriginal.set(row.virtual_id, {
        originalId: row.original_id,
        serverIndex: row.server_index,
        otherInstances: [],
      });
      originalToVirtual.set(`${row.original_id}:${row.server_index}`, row.virtual_id);
    }

    const additionalRows = db.prepare('SELECT virtual_id, original_id, server_index FROM id_additional_instances').all();
    for (const row of additionalRows) {
      const resolved = virtualToOriginal.get(row.virtual_id);
      if (!resolved) continue;
      const exists = resolved.otherInstances.some(inst => inst.originalId === row.original_id && inst.serverIndex === row.server_index);
      if (!exists) {
        resolved.otherInstances.push({
          originalId: row.original_id,
          serverIndex: row.server_index,
        });
      }
    }

    logger.info(`SQLite ID store initialized: ${rows.length} primary mapping(s), ${additionalRows.length} additional instance mapping(s) loaded`);
  } catch (err) {
    logger.warn(`SQLite not available (${err.message}), using in-memory ID store`);
    db = null;
  }

  function _compositeKey(originalId, serverIndex) {
    return `${originalId}:${serverIndex}`;
  }

  function getOrCreateVirtualId(originalId, serverIndex) {
    if (originalId == null || originalId === '') return originalId;

    if (virtualToOriginal.has(originalId)) {
      return originalId;
    }

    const key = _compositeKey(originalId, serverIndex);
    let virtualId = originalToVirtual.get(key);
    if (virtualId) return virtualId;

    if (stmtGetByOriginal) {
      try {
        const existing = stmtGetByOriginal.get(originalId, serverIndex);
        if (existing?.virtual_id) {
          virtualId = existing.virtual_id;
          if (!virtualToOriginal.has(virtualId)) {
            const info = stmtGetByVirtual.get(virtualId);
            virtualToOriginal.set(virtualId, {
              originalId: info.original_id,
              serverIndex: info.server_index,
              otherInstances: [],
            });
          }
          originalToVirtual.set(key, virtualId);
          return virtualId;
        }
      } catch (e) {
        logger.warn(`SQLite lookup failed: ${e.message}`);
      }
    }

    virtualId = uuidv4().replace(/-/g, '');
    virtualToOriginal.set(virtualId, { originalId, serverIndex, otherInstances: [] });
    originalToVirtual.set(key, virtualId);

    if (stmtInsert) {
      try {
        stmtInsert.run(virtualId, originalId, serverIndex);
      } catch (e) {
        logger.warn(`SQLite insert failed: ${e.message}`);
      }
    }

    return virtualId;
  }

  function setMediaSourceStreamUrl(virtualId, streamUrl) {
    const resolved = virtualToOriginal.get(virtualId);
    if (resolved) {
      resolved.streamUrl = streamUrl;
    }
  }

  function getMediaSourceStreamUrl(virtualId) {
    return virtualToOriginal.get(virtualId)?.streamUrl || null;
  }

  function associateAdditionalInstance(virtualId, originalId, serverIndex) {
    const resolved = virtualToOriginal.get(virtualId);
    if (!resolved) return;

    if (resolved.originalId === originalId && resolved.serverIndex === serverIndex) return;

    if (!resolved.otherInstances) resolved.otherInstances = [];

    const exists = resolved.otherInstances.some(inst => inst.originalId === originalId && inst.serverIndex === serverIndex);
    if (!exists) {
      resolved.otherInstances.push({ originalId, serverIndex });
      if (stmtInsertAdditional) {
        try {
          stmtInsertAdditional.run(virtualId, originalId, serverIndex);
        } catch (e) {
          logger.warn(`SQLite additional insert failed: ${e.message}`);
        }
      }
    }
  }

  function resolveVirtualId(virtualId) {
    if (!virtualId) return null;
    const resolved = virtualToOriginal.get(virtualId) || null;
    if (resolved && !resolved.otherInstances) {
      resolved.otherInstances = [];
    }
    return resolved;
  }

  function isVirtualId(id) {
    return virtualToOriginal.has(id);
  }

  function getStats() {
    return {
      mappingCount: stmtCount ? stmtCount.get().count : virtualToOriginal.size,
      persistent: db !== null,
    };
  }

  /**
   * Remove all ID mappings for a given server index.
   * Called when an upstream server is deleted.
   */
  function removeByServerIndex(serverIndex) {
    let removed = 0;
    const keysToDelete = [];
    for (const [virtualId, info] of virtualToOriginal.entries()) {
      if (info.serverIndex === serverIndex) {
        keysToDelete.push(virtualId);
      }
    }

    for (const virtualId of keysToDelete) {
      const info = virtualToOriginal.get(virtualId);
      if (!info) continue;
      originalToVirtual.delete(_compositeKey(info.originalId, info.serverIndex));
      virtualToOriginal.delete(virtualId);
      removed++;
      if (stmtDeleteAdditionalByVirtual) {
        try {
          stmtDeleteAdditionalByVirtual.run(virtualId);
        } catch (e) {
          logger.warn(`SQLite additional cleanup failed: ${e.message}`);
        }
      }
    }

    for (const info of virtualToOriginal.values()) {
      if (info.otherInstances) {
        info.otherInstances = info.otherInstances.filter(inst => inst.serverIndex !== serverIndex);
      }
    }

    if (stmtDeleteByServer) {
      try {
        stmtDeleteByServer.run(serverIndex);
      } catch (e) {
        logger.warn(`SQLite delete failed: ${e.message}`);
      }
    }
    if (stmtDeleteAdditionalByServer) {
      try {
        stmtDeleteAdditionalByServer.run(serverIndex);
      } catch (e) {
        logger.warn(`SQLite additional delete failed: ${e.message}`);
      }
    }

    logger.info(`Removed ${removed} ID mappings for server index ${serverIndex}`);
    return removed;
  }

  /**
   * Shift server indices down by 1 for all mappings where serverIndex > deletedIndex.
   * Called after an upstream server is deleted to keep indices consistent.
   */
  function shiftServerIndices(deletedIndex) {
    for (const [virtualId, info] of virtualToOriginal.entries()) {
      if (info.serverIndex > deletedIndex) {
        originalToVirtual.delete(_compositeKey(info.originalId, info.serverIndex));
        info.serverIndex--;
        originalToVirtual.set(_compositeKey(info.originalId, info.serverIndex), virtualId);
      }
      if (info.otherInstances) {
        for (const inst of info.otherInstances) {
          if (inst.serverIndex > deletedIndex) inst.serverIndex--;
        }
      }
    }

    if (stmtShiftByServer) {
      try {
        stmtShiftByServer.run(deletedIndex);
      } catch (e) {
        logger.warn(`SQLite update failed: ${e.message}`);
      }
    }
    if (stmtShiftAdditionalByServer) {
      try {
        stmtShiftAdditionalByServer.run(deletedIndex);
      } catch (e) {
        logger.warn(`SQLite additional update failed: ${e.message}`);
      }
    }

    logger.info(`Shifted server indices after deleting index ${deletedIndex}`);
  }

  return {
    getOrCreateVirtualId,
    associateAdditionalInstance,
    setMediaSourceStreamUrl,
    getMediaSourceStreamUrl,
    resolveVirtualId,
    isVirtualId,
    getStats,
    removeByServerIndex,
    shiftServerIndices,
    close() {
      if (db) {
        try { db.close(); } catch (e) { logger.warn(`DB close error: ${e.message}`); }
      }
    },
  };
}

module.exports = { createIdManager };
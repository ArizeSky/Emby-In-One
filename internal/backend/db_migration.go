package backend

import (
	"fmt"
	"strings"
)

func tableExists(db *sqliteDB, tableName string) (bool, error) {
	stmt, err := db.prepare("SELECT 1 FROM sqlite_master WHERE type='table' AND name=?")
	if err != nil {
		return false, err
	}
	defer stmt.finalize()
	if err := stmt.bindAll(tableName); err != nil {
		return false, err
	}
	hasRow, err := stmt.step()
	return hasRow, err
}

func tableHasColumn(db *sqliteDB, tableName, columnName string) (bool, error) {
	exists, err := tableExists(db, tableName)
	if err != nil || !exists {
		return false, err
	}
	stmt, err := db.prepare(fmt.Sprintf("PRAGMA table_info(%s)", tableName))
	if err != nil {
		return false, err
	}
	defer stmt.finalize()
	for {
		hasRow, err := stmt.step()
		if err != nil {
			return false, err
		}
		if !hasRow {
			break
		}
		name := stmt.columnText(1)
		if strings.EqualFold(name, columnName) {
			return true, nil
		}
	}
	return false, nil
}

// migrateDatabaseToServerID inspects the database for legacy server_index columns and
// performs an atomic migration to server_id using the current upstream ID order.
func migrateDatabaseToServerID(db *sqliteDB, upstreamIDs []string, logger *Logger) error {
	if db == nil {
		return nil
	}

	needsMigration := false
	for _, tbl := range []string{"id_mappings", "id_additional_instances", "user_servers", "user_watch_progress"} {
		hasIndex, err := tableHasColumn(db, tbl, "server_index")
		if err != nil {
			return err
		}
		if hasIndex {
			needsMigration = true
			break
		}
	}

	if !needsMigration {
		return nil
	}

	if logger != nil {
		logger.Infof("Migrating SQLite schema from server_index to server_id...")
	}

	return db.withWriteTx(func() error {
		if err := db.exec(`
			CREATE TEMP TABLE IF NOT EXISTS temp_server_index_map (
				server_index INTEGER PRIMARY KEY,
				server_id TEXT NOT NULL
			);
			DELETE FROM temp_server_index_map;
		`); err != nil {
			return fmt.Errorf("create temp index map: %w", err)
		}

		for idx, id := range upstreamIDs {
			if err := db.execParams(
				"INSERT OR REPLACE INTO temp_server_index_map (server_index, server_id) VALUES (?, ?)",
				idx, id,
			); err != nil {
				return fmt.Errorf("populate temp index map: %w", err)
			}
		}

		// 1. id_mappings
		hasIndex, err := tableHasColumn(db, "id_mappings", "server_index")
		if err != nil {
			return err
		}
		if hasIndex {
			if err := db.exec(`
				CREATE TABLE id_mappings_new (
					virtual_id TEXT PRIMARY KEY,
					original_id TEXT NOT NULL,
					server_id TEXT NOT NULL
				);
				INSERT INTO id_mappings_new (virtual_id, original_id, server_id)
				SELECT m.virtual_id, m.original_id, COALESCE(t.server_id, 'server-' || m.server_index)
				FROM id_mappings m
				LEFT JOIN temp_server_index_map t ON m.server_index = t.server_index;
				DROP TABLE id_mappings;
				ALTER TABLE id_mappings_new RENAME TO id_mappings;
				CREATE INDEX IF NOT EXISTS idx_original ON id_mappings(original_id, server_id);
			`); err != nil {
				return fmt.Errorf("migrate id_mappings: %w", err)
			}
		}

		// 2. id_additional_instances
		hasIndex, err = tableHasColumn(db, "id_additional_instances", "server_index")
		if err != nil {
			return err
		}
		if hasIndex {
			if err := db.exec(`
				CREATE TABLE id_additional_instances_new (
					virtual_id TEXT NOT NULL,
					original_id TEXT NOT NULL,
					server_id TEXT NOT NULL,
					UNIQUE(virtual_id, original_id, server_id)
				);
				INSERT OR IGNORE INTO id_additional_instances_new (virtual_id, original_id, server_id)
				SELECT a.virtual_id, a.original_id, COALESCE(t.server_id, 'server-' || a.server_index)
				FROM id_additional_instances a
				LEFT JOIN temp_server_index_map t ON a.server_index = t.server_index;
				DROP TABLE id_additional_instances;
				ALTER TABLE id_additional_instances_new RENAME TO id_additional_instances;
				CREATE INDEX IF NOT EXISTS idx_additional_virtual ON id_additional_instances(virtual_id);
			`); err != nil {
				return fmt.Errorf("migrate id_additional_instances: %w", err)
			}
		}

		// 3. user_servers
		hasIndex, err = tableHasColumn(db, "user_servers", "server_index")
		if err != nil {
			return err
		}
		if hasIndex {
			if err := db.exec(`
				CREATE TABLE user_servers_new (
					user_id TEXT NOT NULL,
					server_id TEXT NOT NULL,
					PRIMARY KEY (user_id, server_id),
					FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
				);
				INSERT OR IGNORE INTO user_servers_new (user_id, server_id)
				SELECT u.user_id, COALESCE(t.server_id, 'server-' || u.server_index)
				FROM user_servers u
				LEFT JOIN temp_server_index_map t ON u.server_index = t.server_index;
				DROP TABLE user_servers;
				ALTER TABLE user_servers_new RENAME TO user_servers;
			`); err != nil {
				return fmt.Errorf("migrate user_servers: %w", err)
			}
		}

		// 4. user_watch_progress
		hasIndex, err = tableHasColumn(db, "user_watch_progress", "server_index")
		if err != nil {
			return err
		}
		if hasIndex {
			userCol := "proxy_user_id"
			if hasUser, _ := tableHasColumn(db, "user_watch_progress", "proxy_user_id"); !hasUser {
				userCol = "user_id"
			}
			nameCol := "name"
			if hasName, _ := tableHasColumn(db, "user_watch_progress", "name"); !hasName {
				nameCol = "COALESCE(item_name, '')"
			}
			lastPlayedCol := "last_played"
			if hasLP, _ := tableHasColumn(db, "user_watch_progress", "last_played"); !hasLP {
				lastPlayedCol = "0"
			}

			migrateSQL := fmt.Sprintf(`
				CREATE TABLE user_watch_progress_new (
					proxy_user_id TEXT NOT NULL,
					virtual_item_id TEXT NOT NULL,
					server_id TEXT NOT NULL,
					original_item_id TEXT NOT NULL DEFAULT '',
					item_type TEXT NOT NULL DEFAULT '',
					series_virtual_id TEXT NOT NULL DEFAULT '',
					series_original_id TEXT NOT NULL DEFAULT '',
					series_name TEXT NOT NULL DEFAULT '',
					parent_index_number INTEGER NOT NULL DEFAULT 0,
					index_number INTEGER NOT NULL DEFAULT 0,
					name TEXT NOT NULL DEFAULT '',
					production_year INTEGER NOT NULL DEFAULT 0,
					provider_tmdb TEXT NOT NULL DEFAULT '',
					position_ticks INTEGER NOT NULL DEFAULT 0,
					runtime_ticks INTEGER NOT NULL DEFAULT 0,
					played INTEGER NOT NULL DEFAULT 0,
					is_favorite INTEGER NOT NULL DEFAULT 0,
					last_played INTEGER NOT NULL DEFAULT 0,
					PRIMARY KEY (proxy_user_id, virtual_item_id)
				);
				INSERT OR IGNORE INTO user_watch_progress_new (
					proxy_user_id, virtual_item_id, server_id,
					original_item_id, item_type, series_virtual_id,
					series_original_id, series_name, parent_index_number,
					index_number, name, production_year,
					provider_tmdb, position_ticks, runtime_ticks,
					played, is_favorite, last_played
				)
				SELECT %s, virtual_item_id, COALESCE(t.server_id, 'server-' || w.server_index),
					   COALESCE(original_item_id, ''),
					   COALESCE(item_type, ''),
					   COALESCE(series_virtual_id, ''),
					   COALESCE(series_original_id, ''),
					   COALESCE(series_name, ''),
					   COALESCE(parent_index_number, 0),
					   COALESCE(index_number, 0),
					   COALESCE(%s, ''),
					   COALESCE(production_year, 0),
					   COALESCE(provider_tmdb, ''),
					   COALESCE(position_ticks, 0),
					   COALESCE(runtime_ticks, 0),
					   COALESCE(played, 0),
					   COALESCE(is_favorite, 0),
					   COALESCE(%s, 0)
				FROM user_watch_progress w
				LEFT JOIN temp_server_index_map t ON w.server_index = t.server_index;
				DROP TABLE user_watch_progress;
				ALTER TABLE user_watch_progress_new RENAME TO user_watch_progress;
				CREATE INDEX IF NOT EXISTS idx_watch_user ON user_watch_progress(proxy_user_id);
				CREATE INDEX IF NOT EXISTS idx_watch_user_series ON user_watch_progress(proxy_user_id, series_name);
			`, userCol, nameCol, lastPlayedCol)
			if err := db.exec(migrateSQL); err != nil {
				return fmt.Errorf("migrate user_watch_progress: %w", err)
			}
		}

		_ = db.exec("DROP TABLE IF EXISTS temp_server_index_map;")
		if logger != nil {
			logger.Infof("SQLite database schema successfully migrated to server_id")
		}
		return nil
	})
}

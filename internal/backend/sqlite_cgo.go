package backend

/*
#cgo CFLAGS: -I${SRCDIR}/../../third_party/sqlite -DSQLITE_THREADSAFE=1 -DSQLITE_OMIT_LOAD_EXTENSION
#cgo linux CFLAGS: -D_GNU_SOURCE
#include <stdlib.h>
#include "../../third_party/sqlite/sqlite3.c"
static int bind_text_transient(sqlite3_stmt* stmt, int idx, const char* value) {
    return sqlite3_bind_text(stmt, idx, value, -1, SQLITE_TRANSIENT);
}
*/
import "C"

import (
	"errors"
	"fmt"
	"sync"
	"unsafe"
)

const (
	sqliteRow  = 100
	sqliteDone = 101
)

type sqliteDB struct {
	ptr *C.sqlite3

	// mu serializes every statement issued on this handle. All stores share one
	// SQLite connection, and both transactions and the cursor state of a
	// stepping SELECT are connection-scoped: without mutual exclusion a read
	// stepping rows while another store's transaction is open sees that
	// transaction's uncommitted rows (and its statement interleaving), and an
	// autocommit write issued while a transaction is open lands inside it and
	// is silently rolled back (or committed early) with it.
	//
	// Readers take the lock shared; single write statements and whole
	// transactions take it exclusive.
	mu sync.RWMutex
}

type sqliteStmt struct {
	ptr *C.sqlite3_stmt
}

func openSQLite(path string) (*sqliteDB, error) {
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))
	var db *C.sqlite3
	if rc := C.sqlite3_open(cPath, &db); rc != 0 {
		errMsg := C.GoString(C.sqlite3_errmsg(db))
		if db != nil {
			_ = closeSQLite(&sqliteDB{ptr: db})
		}
		return nil, fmt.Errorf("sqlite open: %s", errMsg)
	}
	handle := &sqliteDB{ptr: db}
	// Defense in depth: the write lock above already serializes writers, but a busy
	// timeout keeps a second connection (or a future one) from failing instantly.
	if err := handle.exec(`PRAGMA busy_timeout = 5000`); err != nil {
		_ = closeSQLite(handle)
		return nil, fmt.Errorf("sqlite busy_timeout: %w", err)
	}
	return handle, nil
}

// errSQLiteClosed is returned by every entry point used after closeSQLite, so
// late readers/writers fail cleanly instead of dereferencing the closed handle.
var errSQLiteClosed = errors.New("sqlite: connection is closed")

func (db *sqliteDB) ensureOpen() error {
	if db == nil {
		return errors.New("sqlite: nil database handle")
	}
	if db.ptr == nil {
		return errSQLiteClosed
	}
	return nil
}

// withReadLock runs fn while holding the connection's read lock. Use it for
// SELECT statements and their stepping loops: a read in progress must not
// interleave with another store's open transaction on this shared connection,
// or it can observe that transaction's uncommitted rows.
func (db *sqliteDB) withReadLock(fn func() error) error {
	if err := db.ensureOpen(); err != nil {
		return err
	}
	db.mu.RLock()
	defer db.mu.RUnlock()
	if db.ptr == nil {
		// The close below takes the exclusive lock, so a read that entered
		// before it still owns the handle; one that starts after sees closed.
		return errSQLiteClosed
	}
	return fn()
}

// withWriteLock runs fn while holding the connection's write lock exclusively.
// Use it for single write statements so they cannot interleave with another
// store's open transaction or with a read in progress.
func (db *sqliteDB) withWriteLock(fn func() error) error {
	if err := db.ensureOpen(); err != nil {
		return err
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.ptr == nil {
		return errSQLiteClosed
	}
	return fn()
}

// withWriteTx runs fn inside BEGIN IMMEDIATE ... COMMIT under the connection's
// exclusive lock. Every multi-statement write must go through here so other
// stores cannot slip autocommit statements into (or lose theirs to) our
// transaction, and no read can step rows inside it.
//
// Lock order is store.mu -> db.mu. Nothing inside fn may call back into a
// store, so db.mu is only ever held for one statement, one transaction, or
// one read.
func (db *sqliteDB) withWriteTx(fn func() error) error {
	return db.withWriteLock(func() error {
		if err := db.exec(`BEGIN IMMEDIATE`); err != nil {
			return err
		}
		if err := fn(); err != nil {
			_ = db.exec(`ROLLBACK`)
			return err
		}
		if err := db.exec(`COMMIT`); err != nil {
			_ = db.exec(`ROLLBACK`)
			return err
		}
		return nil
	})
}

// writeParams runs a parameterized write statement under the connection write lock.
func (db *sqliteDB) writeParams(query string, args ...any) error {
	return db.withWriteLock(func() error { return db.execParams(query, args...) })
}

func closeSQLite(db *sqliteDB) error {
	if db == nil || db.ptr == nil {
		return nil
	}
	// Close runs under the connection's exclusive lock (sqlite_cgo contract):
	// no reader or writer can be mid-statement, in-flight reads finish first,
	// and every later entry returns errSQLiteClosed instead of dereferencing
	// the closed handle. Callers converge: once ptr is nil, Close is a no-op.
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.ptr == nil {
		return nil
	}
	if rc := C.sqlite3_close(db.ptr); rc != 0 {
		return errors.New(C.GoString(C.sqlite3_errmsg(db.ptr)))
	}
	db.ptr = nil
	return nil
}

func (db *sqliteDB) exec(query string) error {
	if db == nil || db.ptr == nil {
		return errSQLiteClosed
	}
	cQuery := C.CString(query)
	defer C.free(unsafe.Pointer(cQuery))
	var errMsg *C.char
	if rc := C.sqlite3_exec(db.ptr, cQuery, nil, nil, &errMsg); rc != 0 {
		if errMsg != nil {
			defer C.sqlite3_free(unsafe.Pointer(errMsg))
			return errors.New(C.GoString(errMsg))
		}
		return errors.New(C.GoString(C.sqlite3_errmsg(db.ptr)))
	}
	return nil
}

// execParams runs a write query with parameterized arguments to prevent SQL injection.
func (db *sqliteDB) execParams(query string, args ...any) error {
	stmt, err := db.prepare(query)
	if err != nil {
		return err
	}
	defer stmt.finalize()
	if err := stmt.bindAll(args...); err != nil {
		return err
	}
	if _, err := stmt.step(); err != nil {
		return err
	}
	return nil
}

func (db *sqliteDB) prepare(query string) (*sqliteStmt, error) {
	if db == nil || db.ptr == nil {
		return nil, errSQLiteClosed
	}
	cQuery := C.CString(query)
	defer C.free(unsafe.Pointer(cQuery))
	var stmt *C.sqlite3_stmt
	if rc := C.sqlite3_prepare_v2(db.ptr, cQuery, -1, &stmt, nil); rc != 0 {
		return nil, errors.New(C.GoString(C.sqlite3_errmsg(db.ptr)))
	}
	return &sqliteStmt{ptr: stmt}, nil
}

func (stmt *sqliteStmt) finalize() {
	if stmt != nil && stmt.ptr != nil {
		C.sqlite3_finalize(stmt.ptr)
		stmt.ptr = nil
	}
}

func (stmt *sqliteStmt) bindAll(args ...any) error {
	for i, arg := range args {
		switch v := arg.(type) {
		case string:
			cValue := C.CString(v)
			if rc := C.bind_text_transient(stmt.ptr, C.int(i+1), cValue); rc != 0 {
				C.free(unsafe.Pointer(cValue))
				return fmt.Errorf("sqlite bind text: %d", int(rc))
			}
			C.free(unsafe.Pointer(cValue))
		case int:
			if rc := C.sqlite3_bind_int(stmt.ptr, C.int(i+1), C.int(v)); rc != 0 {
				return fmt.Errorf("sqlite bind int: %d", int(rc))
			}
		case int64:
			if rc := C.sqlite3_bind_int64(stmt.ptr, C.int(i+1), C.sqlite3_int64(v)); rc != 0 {
				return fmt.Errorf("sqlite bind int64: %d", int(rc))
			}
		default:
			return fmt.Errorf("unsupported sqlite bind type %T", arg)
		}
	}
	return nil
}

func (stmt *sqliteStmt) step() (bool, error) {
	rc := C.sqlite3_step(stmt.ptr)
	switch rc {
	case sqliteRow:
		return true, nil
	case sqliteDone:
		return false, nil
	default:
		return false, fmt.Errorf("sqlite step: %d", int(rc))
	}
}

func (stmt *sqliteStmt) columnText(index int) string {
	text := C.sqlite3_column_text(stmt.ptr, C.int(index))
	if text == nil {
		return ""
	}
	return C.GoString((*C.char)(unsafe.Pointer(text)))
}

func (stmt *sqliteStmt) columnInt(index int) int {
	return int(C.sqlite3_column_int(stmt.ptr, C.int(index)))
}

func (stmt *sqliteStmt) columnInt64(index int) int64 {
	return int64(C.sqlite3_column_int64(stmt.ptr, C.int(index)))
}

package backend

/*
#cgo CFLAGS: -I${SRCDIR}/../../third_party/sqlite -DSQLITE_THREADSAFE=1 -DSQLITE_OMIT_LOAD_EXTENSION=1
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
	"unsafe"
)

const (
	sqliteRow  = 100
	sqliteDone = 101
)

type sqliteDB struct {
	ptr *C.sqlite3
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
	return &sqliteDB{ptr: db}, nil
}

func closeSQLite(db *sqliteDB) error {
	if db == nil || db.ptr == nil {
		return nil
	}
	if rc := C.sqlite3_close(db.ptr); rc != 0 {
		return errors.New(C.GoString(C.sqlite3_errmsg(db.ptr)))
	}
	db.ptr = nil
	return nil
}

func (db *sqliteDB) exec(query string) error {
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

func (db *sqliteDB) prepare(query string) (*sqliteStmt, error) {
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

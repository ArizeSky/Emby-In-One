package backend

import "testing"

func TestSQLiteBuildOmitsLoadExtension(t *testing.T) {
	db, err := openSQLite(":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer func() {
		_ = closeSQLite(db)
	}()

	stmt, err := db.prepare("PRAGMA compile_options")
	if err != nil {
		t.Fatalf("prepare pragma compile_options: %v", err)
	}
	defer stmt.finalize()

	for {
		hasRow, err := stmt.step()
		if err != nil {
			t.Fatalf("step pragma compile_options: %v", err)
		}
		if !hasRow {
			break
		}
		if stmt.columnText(0) == "OMIT_LOAD_EXTENSION" {
			return
		}
	}

	t.Fatal("sqlite build missing OMIT_LOAD_EXTENSION compile option")
}
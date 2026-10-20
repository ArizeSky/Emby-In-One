package backend

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestRotatingFilePrivateMode verifies the log file and its rotated backups are
// created with PrivateFileMode, and that world-readable files left behind by an
// older build (which created them 0644) are tightened again on open and rotate.
func TestRotatingFilePrivateMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not enforced on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	// Simulate a previous run that wrote the log and one backup world-readable.
	if err := os.WriteFile(path, []byte("old line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".1", []byte("old backup\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	assertMode := func(name, p string) {
		t.Helper()
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("%s: stat: %v", name, err)
		}
		if got, want := info.Mode().Perm(), PrivateFileMode(); got != want {
			t.Errorf("%s: mode = %v, want %v", name, got, want)
		}
	}

	f := newRotatingFile(path, 16, 2)
	defer func() { _ = f.Close() }()

	// 9 existing bytes + 9 new bytes > maxSize 16, so this write rotates:
	// the old backup shifts to .2, the pre-rotation log becomes .1, and a fresh
	// log is opened. All three must end up with the private mode.
	if _, err := f.WriteString("new line\n"); err != nil {
		t.Fatal(err)
	}
	assertMode("log", path)
	assertMode("backup 1", f.backupPath(1))
	assertMode("backup 2", f.backupPath(2))

	if _, err := f.WriteString("after clear\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Clear(); err != nil {
		t.Fatal(err)
	}
	assertMode("log after clear", path)
}

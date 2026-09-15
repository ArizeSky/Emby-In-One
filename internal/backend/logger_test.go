package backend

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestLogger writes into a temp dir with a tiny rotation threshold, so tests
// never have to write megabytes. The console level is raised to keep `go test`
// output clean; the file level stays at debug.
func newTestLogger(t *testing.T, maxSize int64, keep int) (*Logger, string) {
	t.Helper()
	dir := t.TempDir()
	logger := NewLogger(LogConfig{
		DataDir:      dir,
		Level:        "error",
		FileLevel:    "debug",
		MaxSizeBytes: maxSize,
		KeepFiles:    keep,
	})
	t.Cleanup(func() { _ = logger.Close() })
	return logger, filepath.Join(dir, "emby-in-one.log")
}

func readLogFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

func TestLoggerRotatesBySizeAndKeepsBoundedBackups(t *testing.T) {
	const (
		maxSize    = 300
		keep       = 2
		lineCount  = 30
		messageLen = 100
	)
	logger, path := newTestLogger(t, maxSize, keep)

	for i := 1; i <= lineCount; i++ {
		logger.Infof("line-%02d %s", i, strings.Repeat("x", messageLen))
	}

	backups := []string{path + ".1", path + ".2"}
	for _, backup := range backups {
		info, err := os.Stat(backup)
		if err != nil {
			t.Fatalf("expected backup %s: %v", filepath.Base(backup), err)
		}
		if info.Size() == 0 {
			t.Fatalf("backup %s is empty", filepath.Base(backup))
		}
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Fatalf("keep=%d must not leave a third backup", keep)
	}
	// Every file stays within the threshold plus the single line that crossed it.
	lineLen := int64(len("2006-01-02 15:04:05 [INFO] ") + messageLen + 1)
	for _, name := range append([]string{path}, backups...) {
		info, err := os.Stat(name)
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if info.Size() > maxSize+lineLen {
			t.Fatalf("%s is %d bytes, past the %d threshold", filepath.Base(name), info.Size(), maxSize)
		}
	}

	live := readLogFile(t, path)
	if !strings.Contains(live, "line-30") {
		t.Fatalf("the newest line is missing from the live log: %q", live)
	}
	retained := live + readLogFile(t, path+".1") + readLogFile(t, path+".2")
	if strings.Contains(retained, "line-01") {
		t.Fatalf("the oldest line should have been dropped from the rotation window")
	}
	if !strings.Contains(retained, "line-30") {
		t.Fatalf("the newest line should be inside the rotation window")
	}
}

func TestLoggerClearFileRemovesBackupsAndKeepsWriting(t *testing.T) {
	logger, path := newTestLogger(t, 300, 2)
	for i := 1; i <= 30; i++ {
		logger.Infof("line-%02d %s", i, strings.Repeat("x", 100))
	}
	if readLogFile(t, path+".1") == "" {
		t.Fatalf("precondition failed: no rotation happened")
	}

	if err := logger.ClearFile(); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got := readLogFile(t, path); got != "" {
		t.Fatalf("live log was not truncated: %q", got)
	}
	for _, backup := range []string{path + ".1", path + ".2"} {
		if _, err := os.Stat(backup); !os.IsNotExist(err) {
			t.Fatalf("backup %s survived the clear", filepath.Base(backup))
		}
	}
	if entries := logger.Entries(0); len(entries) != 0 {
		t.Fatalf("in-memory buffer was not cleared: %d entries", len(entries))
	}

	logger.Infof("after-clear")
	if !strings.Contains(readLogFile(t, path), "after-clear") {
		t.Fatalf("logger stopped writing after a clear")
	}
}

func TestLogRotationSettingsFromEnv(t *testing.T) {
	configured := LogConfig{MaxSizeBytes: 2048, KeepFiles: 2}
	if got := logMaxSize(configured); got != 2048 {
		t.Fatalf("config size ignored: %d", got)
	}
	if got := logKeepFiles(configured); got != 2 {
		t.Fatalf("config keep ignored: %d", got)
	}

	t.Setenv("LOG_MAX_SIZE_MB", "1")
	if got := logMaxSize(configured); got != 1<<20 {
		t.Fatalf("LOG_MAX_SIZE_MB ignored: %d", got)
	}
	t.Setenv("LOG_KEEP", "5")
	if got := logKeepFiles(configured); got != 5 {
		t.Fatalf("LOG_KEEP ignored: %d", got)
	}
	t.Setenv("LOG_KEEP", "not-a-number")
	if got := logKeepFiles(configured); got != 2 {
		t.Fatalf("invalid LOG_KEEP should fall back to the config value: %d", got)
	}
}

func TestLogRotationSettingsDefaults(t *testing.T) {
	// Clear the overrides so the assertion does not depend on the caller's shell.
	t.Setenv("LOG_MAX_SIZE_MB", "")
	t.Setenv("LOG_KEEP", "")

	if got := logMaxSize(LogConfig{}); got != defaultLogMaxSize {
		t.Fatalf("default size = %d, want %d", got, defaultLogMaxSize)
	}
	if got := logKeepFiles(LogConfig{}); got != defaultLogKeep {
		t.Fatalf("default keep = %d, want %d", got, defaultLogKeep)
	}
}

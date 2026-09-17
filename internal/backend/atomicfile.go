package backend

import (
	"os"
	"path/filepath"
	"runtime"
)

// WriteFileAtomic writes data to path through a temporary file and an atomic rename, so
// an interrupted or failed write cannot leave a truncated file behind. It is exported for
// the CLI, which rewrites tokens.json while no server instance is running.
func WriteFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmpFile, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()
	cleanup := func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
	}
	if runtime.GOOS != "windows" {
		_ = tmpFile.Chmod(mode)
	}
	if _, err := tmpFile.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := tmpFile.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := replaceFile(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if runtime.GOOS != "windows" {
		_ = os.Chmod(path, mode)
	}
	return nil
}

func replaceFile(tmpPath, path string) error {
	if err := os.Rename(tmpPath, path); err == nil {
		return nil
	} else if runtime.GOOS != "windows" {
		return err
	}
	_ = os.Remove(path)
	return os.Rename(tmpPath, path)
}

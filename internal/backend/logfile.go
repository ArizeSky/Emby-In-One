package backend

import (
	"fmt"
	"os"
)

// rotatingFile appends to a log file and rotates it once it grows past maxSize,
// keeping up to keep backups next to it (app.log.1 is the newest). It is not
// goroutine-safe on its own: Logger serializes access with its mutex.
type rotatingFile struct {
	path    string
	maxSize int64
	keep    int
	file    *os.File
	written int64
}

// newRotatingFile returns a writer for path. A failed initial open leaves the
// writer in place with no handle, so every write is dropped instead of failing
// the caller: logging must never take the server down.
func newRotatingFile(path string, maxSize int64, keep int) *rotatingFile {
	writer := &rotatingFile{path: path, maxSize: maxSize, keep: keep}
	_ = writer.open()
	return writer
}

func (f *rotatingFile) open() error {
	file, err := os.OpenFile(f.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		f.file = nil
		f.written = 0
		return err
	}
	f.file = file
	// Continue counting where a previous run left off.
	f.written = 0
	if info, err := file.Stat(); err == nil {
		f.written = info.Size()
	}
	return nil
}

func (f *rotatingFile) WriteString(line string) (int, error) {
	if f.file == nil {
		return 0, os.ErrClosed
	}
	// Rotate before writing, but never on an empty file: a single line larger than
	// maxSize must not rotate forever.
	if f.maxSize > 0 && f.written > 0 && f.written+int64(len(line)) > f.maxSize {
		if err := f.rotate(); err != nil {
			return 0, err
		}
	}
	written, err := f.file.WriteString(line)
	f.written += int64(written)
	return written, err
}

func (f *rotatingFile) Close() error {
	if f.file == nil {
		return nil
	}
	err := f.file.Close()
	f.file = nil
	return err
}

// Clear truncates the current log and removes its backups.
func (f *rotatingFile) Clear() error {
	if f.file != nil {
		_ = f.file.Close()
		f.file = nil
	}
	if err := os.WriteFile(f.path, []byte{}, 0o644); err != nil {
		return err
	}
	f.removeBackups()
	return f.open()
}

func (f *rotatingFile) rotate() error {
	if f.file != nil {
		_ = f.file.Close()
		f.file = nil
	}
	for index := f.keep - 1; index >= 1; index-- {
		_ = os.Rename(f.backupPath(index), f.backupPath(index+1))
	}
	_ = os.Rename(f.path, f.backupPath(1))
	return f.open()
}

func (f *rotatingFile) removeBackups() {
	for index := 1; index <= f.keep; index++ {
		_ = os.Remove(f.backupPath(index))
	}
}

func (f *rotatingFile) backupPath(index int) string {
	return fmt.Sprintf("%s.%d", f.path, index)
}

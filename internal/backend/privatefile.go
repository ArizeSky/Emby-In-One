package backend

import (
	"os"
	"runtime"
)

// PrivateFileMode returns the file permission for files that carry credentials or
// per-user state: the token store, the captured client identity headers, and the
// state database. Windows has no equivalent of the POSIX owner-only bit, so it
// keeps the default there.
func PrivateFileMode() os.FileMode {
	if runtime.GOOS == "windows" {
		return 0o644
	}
	return 0o600
}

// chmodPrivate applies PrivateFileMode to files a previous run may have created
// world-readable, for example through a permissive umask or an install script.
// Failures are ignored: a missing file simply means there is nothing to tighten.
func chmodPrivate(paths ...string) {
	for _, path := range paths {
		_ = os.Chmod(path, PrivateFileMode())
	}
}

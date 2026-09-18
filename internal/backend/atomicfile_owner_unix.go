//go:build !windows

package backend

import (
	"os"
	"syscall"
)

// preserveOwner transfers the uid/gid of the file being replaced onto the
// temporary file before the rename, so an atomic write performed by root does
// not change the file's owner. It is a no-op when the target does not exist
// yet (nothing to preserve) or when the process is not root (chown to another
// user would fail anyway, and the service itself always runs unprivileged).
func preserveOwner(tmpPath, path string) {
	if os.Geteuid() != 0 {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return
	}
	_ = os.Chown(tmpPath, int(stat.Uid), int(stat.Gid))
}

//go:build !windows

package backend

import (
	"os"
	"os/user"
	"path/filepath"
	"syscall"
	"testing"
)

// TestWriteFileAtomicPreservesOwnerAsRoot covers the SSH-menu failure mode:
// root runs the binary (--reset-password), the atomic rename must not leave
// the rewritten config owned by root while the service runs as another user.
func TestWriteFileAtomicPreservesOwnerAsRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("owner preservation only applies to root-run writes")
	}
	target, err := user.Lookup("nobody")
	if err != nil {
		t.Skipf("no nobody user on this system: %v", err)
	}
	uid := atoiOrSkip(t, target.Uid)
	gid := atoiOrSkip(t, target.Gid)

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(path, uid, gid); err != nil {
		t.Skipf("cannot chown to nobody: %v", err)
	}

	if err := WriteFileAtomic(path, []byte("new"), 0o600); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("no uid/gid available on this platform")
	}
	if int(stat.Uid) != uid || int(stat.Gid) != gid {
		t.Fatalf("owner changed by atomic write: got uid=%d gid=%d, want uid=%d gid=%d",
			stat.Uid, stat.Gid, uid, gid)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "new" {
		t.Fatalf("content not written: %q, %v", string(data), err)
	}
}

func atoiOrSkip(t *testing.T, s string) int {
	t.Helper()
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			t.Skipf("non-numeric id %q", s)
		}
		n = n*10 + int(c-'0')
	}
	return n
}

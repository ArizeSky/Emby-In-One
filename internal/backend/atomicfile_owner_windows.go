//go:build windows

package backend

// preserveOwner is a no-op on Windows: file ownership is ACL-based and there
// is no unprivileged service account whose access a root-style write could
// break.
func preserveOwner(tmpPath, path string) {}

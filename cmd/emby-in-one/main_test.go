package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"emby-in-one/internal/backend"
)

var configPasswordPattern = regexp.MustCompile(`(?m)^  password: '([^']*(?:''[^']*)*)'$`)

func TestResetPasswordCLIRewritesConfigHash(t *testing.T) {
	dir := t.TempDir()
	config := "server:\n  port: 8096\n  name: \"Test Server\"\n  id: \"server-1\"\n\nadmin:\n  username: \"admin\"\n  password: \"old-password\"\n\nplayback:\n  mode: \"proxy\"\n\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\n\nproxies: []\nupstream: []\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(config), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestHelperProcessResetPassword", "--", "--reset-password", "NewPass123")
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("reset-password CLI timed out instead of exiting promptly; output=%s", string(output))
	}
	if err != nil {
		t.Fatalf("reset-password CLI failed: %v output=%s", err, string(output))
	}

	raw, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatalf("read config after reset-password: %v", err)
	}
	matches := configPasswordPattern.FindStringSubmatch(string(raw))
	if len(matches) != 2 {
		t.Fatalf("failed to locate admin password in config: %s", string(raw))
	}
	stored := matches[1]
	if stored == "NewPass123" {
		t.Fatalf("reset-password wrote plaintext password back to config")
	}
	if !backend.VerifyPassword("NewPass123", stored) {
		t.Fatalf("stored password hash does not validate the new password: %q", stored)
	}
}

func TestHelperProcessResetPassword(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	args := []string{"emby-in-one"}
	sep := -1
	for i, arg := range os.Args {
		if arg == "--" {
			sep = i
			break
		}
	}
	if sep >= 0 {
		args = append(args, os.Args[sep+1:]...)
	}
	os.Args = args
	main()
	os.Exit(0)
}

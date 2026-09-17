package main

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"emby-in-one/internal/backend"
)

var configPasswordPattern = regexp.MustCompile(`(?m)^  password: '([^']*(?:''[^']*)*)'$`)

func TestResetPasswordCLIRewritesConfigHash(t *testing.T) {
	dir := t.TempDir()
	config := "server:\n  port: 18096\n  name: \"Test Server\"\n  id: \"server-1\"\n\nadmin:\n  username: \"admin\"\n  password: \"old-password\"\n\nplayback:\n  mode: \"proxy\"\n\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\n\nproxies: []\nupstream: []\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(config), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Generous on purpose: the child has to start, run one scrypt and exit. Under -race that
	// is several times slower, and this assertion is about the command not hanging, not
	// about it being fast.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
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

// runCLI runs the binary's own main with args, feeding stdin, and returns its output.
func runCLI(t *testing.T, dir, stdin string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmdArgs := append([]string{"-test.run=TestHelperProcessResetPassword", "--"}, args...)
	cmd := exec.CommandContext(ctx, os.Args[0], cmdArgs...)
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
	cmd.Dir = dir
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("CLI timed out; output=%s", string(output))
	}
	return string(output), err
}

func writeResetConfig(t *testing.T, dir string, port int) {
	t.Helper()
	config := "server:\n  port: " + strconv.Itoa(port) + "\n  name: \"Test Server\"\n  id: \"server-1\"\n\n" +
		"admin:\n  username: \"admin\"\n  password: \"old-password\"\n\n" +
		"playback:\n  mode: \"proxy\"\n\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\n\nproxies: []\nupstream: []\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(config), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// TestResetPasswordRefusesWhileServerIsRunning covers the coordination the command used to
// skip entirely: a running instance keeps the tokens in memory and writes the whole file
// back on its next login or logout, restoring everything the reset just cleared.
func TestResetPasswordRefusesWhileServerIsRunning(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\n{}"))
			_ = conn.Close()
		}
	}()

	dir := t.TempDir()
	writeResetConfig(t, dir, port)

	output, err := runCLI(t, dir, "", "--reset-password", "NewPass123")
	if err == nil {
		t.Fatalf("reset-password should refuse while the server answers on port %d; output=%s", port, output)
	}
	if !strings.Contains(output, "still running") {
		t.Fatalf("failure should say the server is still running: %s", output)
	}
	raw, readErr := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if readErr != nil {
		t.Fatalf("read config: %v", readErr)
	}
	if !strings.Contains(string(raw), "old-password") {
		t.Fatal("config was rewritten even though the reset was refused")
	}

	// --force is the documented escape hatch.
	output, err = runCLI(t, dir, "", "--reset-password", "NewPass123", "--force")
	if err != nil {
		t.Fatalf("reset-password --force failed: %v output=%s", err, output)
	}
	raw, readErr = os.ReadFile(filepath.Join(dir, "config.yaml"))
	if readErr != nil {
		t.Fatalf("read config: %v", readErr)
	}
	matches := configPasswordPattern.FindStringSubmatch(string(raw))
	if len(matches) != 2 || !backend.VerifyPassword("NewPass123", matches[1]) {
		t.Fatalf("--force did not rewrite the password hash: %s", string(raw))
	}
}

// TestResetPasswordReadsFromStdin keeps the new password out of the process list, which is
// what the install and management scripts need.
func TestResetPasswordReadsFromStdin(t *testing.T) {
	dir := t.TempDir()
	writeResetConfig(t, dir, 18097)

	output, err := runCLI(t, dir, "StdinPass123\n", "--reset-password", "-")
	if err != nil {
		t.Fatalf("reset-password from stdin failed: %v output=%s", err, output)
	}
	raw, readErr := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if readErr != nil {
		t.Fatalf("read config: %v", readErr)
	}
	matches := configPasswordPattern.FindStringSubmatch(string(raw))
	if len(matches) != 2 {
		t.Fatalf("failed to locate admin password in config: %s", string(raw))
	}
	if !backend.VerifyPassword("StdinPass123", matches[1]) {
		t.Fatalf("stdin password was not applied (trailing newline handling?): %q", matches[1])
	}
}

// TestResetPasswordUsageErrors covers the argument shapes that must not silently do
// something other than what was asked.
func TestResetPasswordUsageErrors(t *testing.T) {
	dir := t.TempDir()
	writeResetConfig(t, dir, 18098)

	for _, args := range [][]string{
		{"--reset-password"},
		{"--reset-password", "one", "two"},
		{"--reset-password", "-"},
	} {
		output, err := runCLI(t, dir, "", args...)
		if err == nil {
			t.Fatalf("%v: expected a usage failure, got success; output=%s", args, output)
		}
	}
}

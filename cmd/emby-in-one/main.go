package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"emby-in-one/internal/backend"
)

// Version is set at build time via -ldflags "-X main.Version=..."
var Version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "--version" {
		_, _ = fmt.Fprintln(stdout, Version)
		return 0
	}
	if len(args) > 0 && args[0] == "--reset-password" {
		return runResetPassword(args[1:], stdout, stderr)
	}

	app, err := backend.NewApp()
	if err != nil {
		_, _ = io.WriteString(stderr, err.Error()+"\n")
		return 1
	}
	app.Version = Version
	defer app.Close()
	if err := app.Run(); err != nil {
		_, _ = io.WriteString(stderr, err.Error()+"\n")
		return 1
	}
	return 0
}

const resetPasswordUsage = "usage: emby-in-one --reset-password <new-password|-> [--force]\n"

// runResetPassword parses the reset command. A password of "-" is read from stdin, which
// keeps it out of the process list and out of the shell history.
func runResetPassword(args []string, stdout, stderr io.Writer) int {
	password := ""
	force := false
	for _, arg := range args {
		switch arg {
		case "--force":
			force = true
		case "-":
			password = "-"
		default:
			if password != "" {
				_, _ = io.WriteString(stderr, resetPasswordUsage)
				return 1
			}
			password = arg
		}
	}
	if password == "" {
		_, _ = io.WriteString(stderr, resetPasswordUsage)
		return 1
	}
	if password == "-" {
		raw, err := io.ReadAll(os.Stdin)
		if err != nil {
			_, _ = io.WriteString(stderr, "read password from stdin: "+err.Error()+"\n")
			return 1
		}
		password = strings.TrimRight(string(raw), "\r\n")
	}
	if err := resetPassword(password, force, stdout); err != nil {
		_, _ = io.WriteString(stderr, err.Error()+"\n")
		return 1
	}
	return 0
}

// ensureServiceStopped refuses to touch the token file while a server instance is running.
// A live instance holds the tokens in memory and writes the whole file back on its next
// login or logout, which would restore every token this command just cleared. The probe
// asks the configured port; anything that answers means the operator should stop the
// service first.
func ensureServiceStopped(cfg backend.Config, force bool) error {
	if force {
		return nil
	}
	address := "127.0.0.1:" + strconv.Itoa(cfg.Server.Port)
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + address + "/System/Info/Public")
	if err != nil {
		return nil // nothing listening on the configured port
	}
	_ = resp.Body.Close()
	return fmt.Errorf("emby-in-one is still running on %s; stop it first (systemctl stop emby-in-one) or pass --force", address)
}

func resetPassword(newPassword string, force bool, stdout io.Writer) error {
	if strings.TrimSpace(newPassword) == "" {
		return fmt.Errorf("new password cannot be empty")
	}
	store, err := backend.LoadConfigStore()
	if err != nil {
		return err
	}
	if err := ensureServiceStopped(store.Snapshot(), force); err != nil {
		return err
	}
	hashed, err := backend.HashPassword(newPassword)
	if err != nil {
		return err
	}
	if err := store.Mutate(func(cfg *backend.Config) error {
		cfg.Admin.Password = hashed
		return nil
	}); err != nil {
		return err
	}
	if err := store.Save(); err != nil {
		return err
	}

	// Clear all proxy tokens but preserve _proxyUserId. The write is atomic: a truncated
	// tokens.json makes AuthManager.load fail and the service refuse to start.
	tokenFile := filepath.Join(store.Snapshot().DataDir, "tokens.json")
	if _, err := os.Stat(tokenFile); err == nil {
		raw, readErr := os.ReadFile(tokenFile)
		minimal := []byte("{}\n")
		if readErr == nil {
			var parsed map[string]any
			if jsonErr := json.Unmarshal(raw, &parsed); jsonErr == nil {
				if proxyID, ok := parsed["_proxyUserId"]; ok {
					if out, marshalErr := json.MarshalIndent(map[string]any{"_proxyUserId": proxyID}, "", "  "); marshalErr == nil {
						minimal = out
					}
				}
			}
		}
		if err := backend.WriteFileAtomic(tokenFile, minimal, backend.PrivateFileMode()); err != nil {
			return fmt.Errorf("clear proxy tokens: %w", err)
		}
	}

	_, _ = io.WriteString(stdout, "Administrator password reset successfully.\n")
	return nil
}

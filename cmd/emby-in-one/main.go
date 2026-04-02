package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"emby-in-one/internal/backend"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "--reset-password" {
		if len(args) != 2 {
			_, _ = io.WriteString(stderr, "usage: emby-in-one --reset-password <new-password>\n")
			return 1
		}
		if err := resetPassword(args[1], stdout); err != nil {
			_, _ = io.WriteString(stderr, err.Error()+"\n")
			return 1
		}
		return 0
	}

	app, err := backend.NewApp()
	if err != nil {
		_, _ = io.WriteString(stderr, err.Error()+"\n")
		return 1
	}
	defer app.Close()
	if err := app.Run(); err != nil {
		_, _ = io.WriteString(stderr, err.Error()+"\n")
		return 1
	}
	return 0
}

func resetPassword(newPassword string, stdout io.Writer) error {
	if strings.TrimSpace(newPassword) == "" {
		return fmt.Errorf("new password cannot be empty")
	}
	store, err := backend.LoadConfigStore()
	if err != nil {
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
	// Clear all tokens so old sessions cannot persist after password change
	tokenFile := filepath.Join(store.Snapshot().DataDir, "tokens.json")
	if tokenFile == "" || tokenFile == "." || tokenFile == string(filepath.Separator) {
		tokenFile = filepath.Join("data", "tokens.json")
	}
	if _, err := os.Stat(tokenFile); err == nil {
		// Overwrite with only the proxyUserId preserved (no tokens)
		raw, readErr := os.ReadFile(tokenFile)
		minimal := []byte("{}\n")
		if readErr == nil {
			var parsed map[string]any
			if jsonErr := json.Unmarshal(raw, &parsed); jsonErr == nil {
				if proxyID, ok := parsed["_proxyUserId"]; ok {
					if out, err := json.MarshalIndent(map[string]any{"_proxyUserId": proxyID}, "", "  "); err == nil {
						minimal = out
					}
				}
			}
		}
		_ = os.WriteFile(tokenFile, minimal, backend.TokenFileMode())
	}
	_, _ = io.WriteString(stdout, "Administrator password reset successfully.\n")
	return nil
}
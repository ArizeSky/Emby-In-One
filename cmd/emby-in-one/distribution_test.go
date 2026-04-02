package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func repoRootPath() string {
	// cmd/emby-in-one → project root is two levels up
	return filepath.Clean(filepath.Join("..", ".."))
}

func existingPaths(paths ...string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			out = append(out, p)
		}
	}
	return out
}

func TestShellScriptsDoNotUseUTF8BOM(t *testing.T) {
	root := repoRootPath()
	candidates := existingPaths(
		filepath.Join(root, "install.sh"),
		filepath.Join(root, "emby-in-one-cli.sh"),
		filepath.Join(root, "Emby-In-One-Go", "install.sh"),
		filepath.Join(root, "Emby-In-One-Go", "emby-in-one-cli.sh"),
	)
	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
			t.Fatalf("script %s starts with UTF-8 BOM", path)
		}
	}
}

func TestInstallScriptMentionsStandaloneDistributionLayout(t *testing.T) {
	root := repoRootPath()
	installPath := filepath.Join(root, "install.sh")
	data, err := os.ReadFile(installPath)
	if err != nil {
		t.Fatalf("read install.sh: %v", err)
	}
	text := string(data)
	for _, fragment := range []string{"cmd", "internal", "third_party", "go.mod", "public"} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("install.sh does not mention standalone distribution fragment %q", fragment)
		}
	}
}

func TestStandaloneDistributionDockerfilesDoNotReferenceGoBackendPrefix(t *testing.T) {
	root := repoRootPath()
	candidates := existingPaths(filepath.Join(root, "Emby-In-One-Go", "Dockerfile"))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
		if _, err := os.Stat(filepath.Join(root, "cmd")); err == nil {
			if _, err := os.Stat(filepath.Join(root, "internal")); err == nil {
				candidates = append(candidates, filepath.Join(root, "Dockerfile"))
			}
		}
	}
	if len(candidates) == 0 {
		t.Skip("no standalone distribution Dockerfile found in this workspace")
	}
	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := string(data)
		if strings.Contains(text, "COPY go-backend/") {
			t.Fatalf("standalone Dockerfile %s still references go-backend/ paths", path)
		}
		for _, expected := range []string{"COPY go.mod ./", "COPY third_party ./third_party", "COPY cmd ./cmd", "COPY internal ./internal"} {
			if !strings.Contains(text, expected) {
				t.Fatalf("standalone Dockerfile %s missing %q", path, expected)
			}
		}
	}
}
func TestShellScriptsUseLFLineEndings(t *testing.T) {
	root := repoRootPath()
	candidates := existingPaths(
		filepath.Join(root, "install.sh"),
		filepath.Join(root, "emby-in-one-cli.sh"),
		filepath.Join(root, "Emby-In-One-Go", "install.sh"),
		filepath.Join(root, "Emby-In-One-Go", "emby-in-one-cli.sh"),
	)
	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if strings.Contains(string(data), "\r") {
			t.Fatalf("script %s contains CRLF line endings", path)
		}
	}
}

func TestInstallScriptsHaveSingleValidScriptDirAssignment(t *testing.T) {
	root := repoRootPath()
	candidates := existingPaths(
		filepath.Join(root, "install.sh"),
		filepath.Join(root, "Emby-In-One-Go", "install.sh"),
	)
	const want = `SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"`
	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := string(data)
		if strings.Count(text, want) != 1 {
			t.Fatalf("install script %s should contain exactly one valid SCRIPT_DIR assignment", path)
		}
		for _, bad := range []string{
			`SCRIPT_DIR="$(cd "$(dirname "# ── 5. 复制/下载项目文件 ──`,
			`# ── 6. 创建数据目录 ──")" && pwd)"`,
		} {
			if strings.Contains(text, bad) {
				t.Fatalf("install script %s contains corrupted fragment %q", path, bad)
			}
		}
	}
}

func TestAdminHTMLSaveServerHandlesUpstreamErrors(t *testing.T) {
	root := repoRootPath()
	candidates := existingPaths(
		filepath.Join(root, "public", "admin.html"),
		filepath.Join(root, "Emby-In-One-Go", "public", "admin.html"),
	)
	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := string(data)
		for _, fragment := range []string{
			"async saveServer()",
			"const res = await this.api(",
			"'/admin/api/upstream'",
			"res.error",
			"alert('保存失败：' + res.error)",
			"res.warning",
			"alert('提示：' + res.warning)",
			"await this.refreshServers();",
		} {
			if !strings.Contains(text, fragment) {
				t.Fatalf("admin.html %s saveServer is missing %q", path, fragment)
			}
		}
	}
}

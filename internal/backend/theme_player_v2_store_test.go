package backend

import (
	"strings"
	"testing"
)

// TestThemePlayerV2StoreV1ThenV2UpdateReopen 验证同一主题 ID 从 v1 升级到 v2 后，
// 重开存储仍保留 v2 的 palette，并且 v1 的历史定义不凭空获得 player controls。
// 同时确认 v1 的 CSS 渲染不泄漏 v2 的 --eio-progress-* 变量；golden 原测试保持独立不变。
func TestThemePlayerV2StoreV1ThenV2UpdateReopen(t *testing.T) {
	db := newThemeStoreTestDB(t)
	dataDir := t.TempDir()
	store := newThemeStoreTestStore(t, dataDir, db)

	v1Zip := themePlayerV1GoldenZIP(t, themePlayerV1GoldenManifestJSON, themePlayerV1GoldenThemeJSON)
	first, err := store.InstallUpdate(themePlayerV2TestMustParse(t, v1Zip), 0)
	if err != nil {
		t.Fatalf("install v1 package: %v", err)
	}
	if first.ThemeAPIVersion != 1 {
		t.Fatalf("v1 install theme API version = %d, want 1", first.ThemeAPIVersion)
	}
	v1Hash := first.CurrentHash

	v2Theme := themePlayerV2TestThemeWithBlock(t, themePlayerV1GoldenThemeJSON, themePlayerV2TestLowerBlock)
	v2Zip := themePlayerV1GoldenZIP(t, themePlayerV2TestManifest(t, 2), v2Theme)
	second, err := store.InstallUpdate(themePlayerV2TestMustParse(t, v2Zip), first.Revision)
	if err != nil {
		t.Fatalf("install v2 update of the same theme id: %v", err)
	}
	if second.ThemeAPIVersion != 2 {
		t.Fatalf("v2 update theme API version = %d, want 2", second.ThemeAPIVersion)
	}
	if second.Revision == first.Revision || second.Revision <= 0 || second.Revision > ThemeMaxRevision {
		t.Fatalf("v2 update revision = %d, want positive in range and different from %d", second.Revision, first.Revision)
	}
	v2Hash := second.CurrentHash
	if v2Hash == v1Hash {
		t.Fatalf("v2 update reused v1 hash %q", v1Hash)
	}

	// CloseSafety 不关闭共享 DB，cleanup 再次关闭必须安全。
	if err := store.CloseSafety(); err != nil {
		t.Fatalf("close safety: %v", err)
	}

	reopened := newThemeStoreTestStore(t, dataDir, db)

	v2Def, err := reopened.Definition(v2Hash)
	if err != nil {
		t.Fatalf("definition of v2 hash %q: %v", v2Hash, err)
	}
	if v2Def.PlayerControls == nil {
		t.Fatalf("v2 definition of %q lost its player controls palette", v2Hash)
	}
	themePlayerV2TestAssertControls(t, v2Def.PlayerControls, themePlayerV2TestLowerColors)

	v1Def, err := reopened.Definition(v1Hash)
	if err != nil {
		t.Fatalf("definition of v1 hash %q: %v", v1Hash, err)
	}
	if v1Def.PlayerControls != nil {
		t.Fatalf("v1 definition of %q unexpectedly gained player controls", v1Hash)
	}

	css, err := renderThemeCSS(v1Hash, v1Def)
	if err != nil {
		t.Fatalf("render v1 css: %v", err)
	}
	if strings.Contains(string(css), "--eio-progress-") {
		t.Fatalf("v1 css leaked v2 progress variables: %s", css)
	}
}

// TestThemePlayerV2RenderRejectsInvalidControls 验证 CSS 渲染拒绝不完整或自相矛盾的
// player controls 颜色：缺字段、played 与 track 相同、thumb 与 track 相同、非 HEX 值。
func TestThemePlayerV2RenderRejectsInvalidControls(t *testing.T) {
	db := newThemeStoreTestDB(t)
	store := newThemeStoreTestStore(t, t.TempDir(), db)

	v2Theme := themePlayerV2TestThemeWithBlock(t, themePlayerV1GoldenThemeJSON, themePlayerV2TestLowerBlock)
	v2Zip := themePlayerV1GoldenZIP(t, themePlayerV2TestManifest(t, 2), v2Theme)
	entry, err := store.InstallUpdate(themePlayerV2TestMustParse(t, v2Zip), 0)
	if err != nil {
		t.Fatalf("install v2 package: %v", err)
	}
	hash := entry.CurrentHash

	base, err := store.Definition(hash)
	if err != nil {
		t.Fatalf("definition of %q: %v", hash, err)
	}
	if base.PlayerControls == nil {
		t.Fatalf("v2 definition of %q has no player controls", hash)
	}

	played := "#3b82f6"
	thumb := "#3b82f6"
	track := "#111827"
	notHex := "red"

	// 合法基线：从已安装定义复制，保证未显式覆盖的颜色仍完整。
	valid := *base.PlayerControls
	valid.Played = &played
	valid.Thumb = &thumb
	valid.Track = &track

	def := base
	def.PlayerControls = &valid
	if _, err := renderThemeCSS(hash, def); err != nil {
		t.Fatalf("render complete controls: %v", err)
	}

	// 缺失字段：仅 Played 有指针，Thumb/Track 置空。
	missing := valid
	missing.Played = &played
	missing.Thumb = nil
	missing.Track = nil
	def.PlayerControls = &missing
	if _, err := renderThemeCSS(hash, def); err == nil {
		t.Fatal("render controls missing thumb/track: want error, got nil")
	}

	// played 与 track 取值相同。
	dupPlayed := valid
	dupPlayed.Played = &track
	def.PlayerControls = &dupPlayed
	if _, err := renderThemeCSS(hash, def); err == nil {
		t.Fatal("render controls with played == track: want error, got nil")
	}

	// thumb 与 track 取值相同。
	dupThumb := valid
	dupThumb.Thumb = &track
	def.PlayerControls = &dupThumb
	if _, err := renderThemeCSS(hash, def); err == nil {
		t.Fatal("render controls with thumb == track: want error, got nil")
	}

	// 非 HEX 颜色。
	badColor := valid
	badColor.Played = &notHex
	def.PlayerControls = &badColor
	if _, err := renderThemeCSS(hash, def); err == nil {
		t.Fatal("render controls with non-HEX colour: want error, got nil")
	}
}

// TestThemeAPIAdminMappingDoesNotFakeBuiltin 验证不可用的未知主题在管理端映射中
// 不会伪装成内置主题，且主题 API 版本保持 0。
func TestThemeAPIAdminMappingDoesNotFakeBuiltin(t *testing.T) {
	entry := ThemeCatalogEntry{
		Builtin:         false,
		Available:       false,
		ThemeAPIVersion: 0,
		ID:              "eio-unknown",
	}
	admin := themeAPIAdminThemeFrom(entry, nil)
	if admin.Builtin {
		t.Fatal("unavailable unknown theme mapped to builtin")
	}
	if admin.Available {
		t.Fatal("unavailable unknown theme mapped to available")
	}
	if admin.ThemeAPIVersion != 0 {
		t.Fatalf("unknown theme theme API version = %d, want 0", admin.ThemeAPIVersion)
	}

	builtin := themeAPIAdminThemeFrom(ThemeCatalogEntry{
		Builtin:         true,
		Available:       true,
		ThemeAPIVersion: 0,
		ID:              ThemeIDBuiltin,
	}, nil)
	if !builtin.Builtin {
		t.Fatal("builtin theme lost its builtin flag")
	}
	if builtin.ThemeAPIVersion != 0 {
		t.Fatalf("builtin theme theme API version = %d, want 0", builtin.ThemeAPIVersion)
	}
}

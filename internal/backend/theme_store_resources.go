package backend

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Theme resource storage: immutable hash directories, bounded staging,
// quota accounting, orphan purging, resource reads and the install/update
// publish path.
//
// Only validated image bytes are written. Paths come exclusively from the
// validated descriptor index and are never built from a request path. The
// filesystem rename and the database transaction are deliberately separate:
// the old current version stays valid until the database commit, and a failed
// commit removes the newly staged directory.

func themeStoreVersionDir(root, contentHash string) string {
	return filepath.Join(root, contentHash)
}

// themeStoreRemoveTree removes path without following symlinks and refuses to
// touch anything outside root. os.RemoveAll unlinks symlinks instead of
// traversing them, which is the required non-following behaviour.
func themeStoreRemoveTree(root, path string) error {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || filepath.IsAbs(rel) || rel == ".." ||
		strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("refusing to remove %q outside theme root", path)
	}
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return os.Remove(path)
	}
	return os.RemoveAll(path)
}

// themeStoreDeleteRemoveTree is the delete path's physical cleanup seam.
// Production always points at themeStoreRemoveTree; tests replace it to prove
// that a committed database delete survives a failed resource cleanup.
var themeStoreDeleteRemoveTree = themeStoreRemoveTree

// themeStoreTreeBytes reports the regular-file bytes below path. Symlinks are
// counted as links, never followed.
func themeStoreTreeBytes(path string) int64 {
	var total int64
	_ = filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total
}

func themeStoreDiskUsage(root string) (int64, error) {
	var total int64
	err := filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}

func themeStoreVersionPayloadBytes(version *themeStoreVersion) int64 {
	var total int64
	for _, descriptor := range version.allDescriptors() {
		total += int64(descriptor.Bytes)
	}
	return total
}

func themeStoreNormalizeVariant(variant ThemeVariant, name string) (ThemeVariant, error) {
	fields := []struct {
		field string
		value *string
	}{
		{"bg0", &variant.Bg0}, {"bg1", &variant.Bg1}, {"bg2", &variant.Bg2}, {"bg3", &variant.Bg3},
		{"text1", &variant.Text1}, {"text2", &variant.Text2}, {"text3", &variant.Text3},
		{"line", &variant.Line}, {"line2", &variant.Line2},
		{"accent", &variant.Accent}, {"accentText", &variant.AccentText},
	}
	for _, entry := range fields {
		normalized, err := normalizeThemeColor(*entry.value)
		if err != nil {
			return ThemeVariant{}, fmt.Errorf("%w: variant %s %s: %v", ErrThemeInvalid, name, entry.field, err)
		}
		*entry.value = normalized
	}
	if err := checkThemeVariantContrast(name, variant); err != nil {
		return ThemeVariant{}, fmt.Errorf("%w: %v", ErrThemeInvalid, err)
	}
	return variant, nil
}

// themeStoreCloneDefinition returns an independent copy of a validated
// definition, including the optional v2 playerControls pointers, so a caller
// can never mutate store state through a shared pointer.
func themeStoreCloneDefinition(definition ThemeDefinition) ThemeDefinition {
	out := cloneThemeDefinition(definition)
	out.PlayerControls = cloneThemePlayerControls(definition.PlayerControls)
	return out
}

// themeStoreValidateDefinition enforces the frozen theme.json contract on
// values loaded from the database or supplied in a package object. apiVersion
// is the manifest's themeApiVersion: v1 rejects the playerControls block, v2
// accepts it as an optional but complete block.
func themeStoreValidateDefinition(definition ThemeDefinition, apiVersion int) error {
	if !themeAPIVersionSupported(apiVersion) {
		return fmt.Errorf("%w: unsupported themeApiVersion %d", ErrThemeInvalid, apiVersion)
	}
	if len(definition.Variants) != 2 {
		return fmt.Errorf("%w: theme must define exactly light and dark variants", ErrThemeInvalid)
	}
	for _, name := range themeStoreThemeVariants() {
		variant, ok := definition.Variants[name]
		if !ok {
			return fmt.Errorf("%w: variant %s is missing", ErrThemeInvalid, name)
		}
		normalized, err := themeStoreNormalizeVariant(variant, name)
		if err != nil {
			return err
		}
		if normalized != variant {
			return fmt.Errorf("%w: variant %s colors are not canonical #RRGGBB", ErrThemeInvalid, name)
		}
	}
	radius := definition.Appearance.Radius
	if radius < ThemeAppearanceRadiusMin || radius > ThemeAppearanceRadiusMax {
		return fmt.Errorf("%w: appearance.radius must be %d..%d", ErrThemeInvalid,
			ThemeAppearanceRadiusMin, ThemeAppearanceRadiusMax)
	}
	if !validThemeDensity(definition.Appearance.Density) {
		return fmt.Errorf("%w: appearance.density is not supported", ErrThemeInvalid)
	}
	if definition.BackgroundImage != "" {
		cleaned, err := validateThemePackagePath(definition.BackgroundImage)
		if err != nil || cleaned != definition.BackgroundImage {
			return fmt.Errorf("%w: backgroundImage is not a normalized package path", ErrThemeInvalid)
		}
		if !strings.HasPrefix(cleaned, themeAssetPrefix) {
			return fmt.Errorf("%w: backgroundImage must be below assets/", ErrThemeInvalid)
		}
		if _, ok := themeImageMIMEForPath(cleaned); !ok {
			return fmt.Errorf("%w: backgroundImage must be a PNG or JPEG asset", ErrThemeInvalid)
		}
		if cleaned == themeEntryPreview || cleaned == themeEntryPreviewJ {
			return fmt.Errorf("%w: backgroundImage may not be the preview image", ErrThemeInvalid)
		}
	}
	if err := themeStoreValidatePlayerControlsForVersion(definition.PlayerControls, apiVersion); err != nil {
		return err
	}
	return nil
}

func themeStoreValidateStoredDescriptor(descriptor themeStoreAssetDescriptor, preview bool) error {
	expectedMIME, ok := themeImageMIMEForPath(descriptor.Path)
	if !ok {
		return fmt.Errorf("%w: unsupported resource path %q", ErrThemeCorrupt, descriptor.Path)
	}
	if preview {
		if descriptor.Path != themeEntryPreview && descriptor.Path != themeEntryPreviewJ {
			return fmt.Errorf("%w: invalid preview path %q", ErrThemeCorrupt, descriptor.Path)
		}
	} else {
		if _, err := validateThemePackagePath(descriptor.Path); err != nil ||
			!strings.HasPrefix(descriptor.Path, themeAssetPrefix) {
			return fmt.Errorf("%w: invalid asset path %q", ErrThemeCorrupt, descriptor.Path)
		}
	}
	if descriptor.MIME != expectedMIME {
		return fmt.Errorf("%w: %s MIME %q does not match its extension", ErrThemeCorrupt, descriptor.Path, descriptor.MIME)
	}
	if !themeStoreHashName(descriptor.SHA256) {
		return fmt.Errorf("%w: %s asset digest is invalid", ErrThemeCorrupt, descriptor.Path)
	}
	if descriptor.Bytes <= 0 || descriptor.Bytes > ThemeEntryMaxUncompressedBytes {
		return fmt.Errorf("%w: %s asset size %d is out of range", ErrThemeCorrupt, descriptor.Path, descriptor.Bytes)
	}
	if descriptor.Width <= 0 || descriptor.Height <= 0 ||
		descriptor.Width > ThemeImageMaxDimension || descriptor.Height > ThemeImageMaxDimension {
		return fmt.Errorf("%w: %s dimensions %dx%d are out of range", ErrThemeCorrupt,
			descriptor.Path, descriptor.Width, descriptor.Height)
	}
	if int64(descriptor.Width)*int64(descriptor.Height) > ThemeImageMaxPixels {
		return fmt.Errorf("%w: %s image is too large", ErrThemeCorrupt, descriptor.Path)
	}
	return nil
}

func themeStoreValidateVersion(version *themeStoreVersion) error {
	if version == nil {
		return fmt.Errorf("%w: nil theme version", ErrThemeCorrupt)
	}
	if version.ThemeID == "" || !themeStoreHashName(version.ContentHash) {
		return fmt.Errorf("%w: theme version identity is invalid", ErrThemeCorrupt)
	}
	if err := validateThemeManifest(version.Manifest); err != nil {
		return fmt.Errorf("%w: %v", ErrThemeCorrupt, err)
	}
	if version.Manifest.ID != version.ThemeID {
		return fmt.Errorf("%w: manifest id does not match version id", ErrThemeCorrupt)
	}
	if err := themeStoreValidateDefinition(version.Theme, version.Manifest.ThemeAPIVersion); err != nil {
		return fmt.Errorf("%w: %v", ErrThemeCorrupt, err)
	}
	if version.PackageBytes < 0 || version.PackageBytes > ThemePackageMaxUncompressedBytes {
		return fmt.Errorf("%w: package size is out of range", ErrThemeCorrupt)
	}
	assets := make([]ThemeAsset, 0, len(version.Assets))
	var previousPath string
	for _, descriptor := range version.Assets {
		if err := themeStoreValidateStoredDescriptor(descriptor, false); err != nil {
			return err
		}
		if previousPath != "" && descriptor.Path <= previousPath {
			return fmt.Errorf("%w: asset rows are not strictly sorted", ErrThemeCorrupt)
		}
		previousPath = descriptor.Path
		assets = append(assets, descriptor.themeAsset())
	}
	var preview *ThemeAsset
	if version.Preview != nil {
		if err := themeStoreValidateStoredDescriptor(*version.Preview, true); err != nil {
			return err
		}
		value := version.Preview.themeAsset()
		preview = &value
	}
	// Reuse the package fingerprint so a tampered descriptor or version row
	// cannot masquerade as an installed content hash.
	recomputed, err := themePackageContentHash(version.Manifest, version.Theme, preview, assets)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrThemeCorrupt, err)
	}
	if recomputed != version.ContentHash {
		return fmt.Errorf("%w: content hash does not match stored metadata", ErrThemeCorrupt)
	}
	return themeStoreValidateAssetReferences(version)
}

// themeStoreValidateAssetReferences enforces the conservative rule accepted in
// the package review: at most the single backgroundImage and the optional
// preview may be packaged, and every background asset is referenced.
func themeStoreValidateAssetReferences(version *themeStoreVersion) error {
	background := version.Theme.BackgroundImage
	found := false
	for _, descriptor := range version.Assets {
		if descriptor.Path == background {
			found = true
			continue
		}
		return fmt.Errorf("%w: asset %s is not referenced by theme.json", ErrThemeCorrupt, descriptor.Path)
	}
	if background != "" && !found {
		return fmt.Errorf("%w: backgroundImage does not reference a packaged asset", ErrThemeCorrupt)
	}
	return nil
}

// themeStoreResourcePath maps a validated logical resource name to a path under
// the immutable version directory. It never accepts traversal, absolute paths
// or an unregistered content hash.
func themeStoreResourcePath(root, contentHash, logical string) (string, error) {
	if !themeStoreHashName(contentHash) {
		return "", ErrThemeResourceNotFound
	}
	if _, err := validateThemePackagePath(logical); err != nil {
		return "", ErrThemeResourceNotFound
	}
	directory := themeStoreVersionDir(root, contentHash)
	full := filepath.Join(directory, filepath.FromSlash(logical))
	if !strings.HasPrefix(full, directory+string(os.PathSeparator)) {
		return "", ErrThemeResourceNotFound
	}
	return full, nil
}

// themeStoreVersionFilesValid verifies that every registered file exists as a
// plain regular file with the exact recorded size and SHA-256.
func themeStoreVersionFilesValid(root string, version *themeStoreVersion) error {
	for _, descriptor := range version.allDescriptors() {
		full, err := themeStoreResourcePath(root, version.ContentHash, descriptor.Path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(full)
		if err != nil {
			return fmt.Errorf("%w: %s is missing: %v", ErrThemeCorrupt, descriptor.Path, err)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s is not a plain file", ErrThemeCorrupt, descriptor.Path)
		}
		if info.Size() != int64(descriptor.Bytes) {
			return fmt.Errorf("%w: %s has size %d, expected %d", ErrThemeCorrupt,
				descriptor.Path, info.Size(), descriptor.Bytes)
		}
		payload, err := os.ReadFile(full)
		if err != nil {
			return fmt.Errorf("%w: cannot read %s: %v", ErrThemeCorrupt, descriptor.Path, err)
		}
		sum := sha256.Sum256(payload)
		if hex.EncodeToString(sum[:]) != descriptor.SHA256 {
			return fmt.Errorf("%w: %s content digest does not match", ErrThemeCorrupt, descriptor.Path)
		}
	}
	return nil
}

// themeStoreValidatedAsset re-validates package bytes through the frozen image
// decoder and ignores caller-supplied digests as authority. Only the decoded
// bytes and metadata are copied into the store.
func themeStoreValidatedAsset(asset ThemeAsset, preview bool) (themeStoreAssetDescriptor, error) {
	if len(asset.Data) == 0 {
		return themeStoreAssetDescriptor{}, fmt.Errorf("%w: %s has no bytes", ErrThemeInvalid, asset.Path)
	}
	if preview {
		if asset.Path != themeEntryPreview && asset.Path != themeEntryPreviewJ {
			return themeStoreAssetDescriptor{}, fmt.Errorf("%w: invalid preview path", ErrThemeInvalid)
		}
	} else {
		cleaned, err := validateThemePackagePath(asset.Path)
		if err != nil || cleaned != asset.Path || !strings.HasPrefix(cleaned, themeAssetPrefix) {
			return themeStoreAssetDescriptor{}, fmt.Errorf("%w: invalid asset path %q", ErrThemeInvalid, asset.Path)
		}
		if !themePackageEntryAllowed(cleaned) {
			return themeStoreAssetDescriptor{}, fmt.Errorf("%w: asset path is outside the package layout", ErrThemeInvalid)
		}
	}
	decoded, err := decodeThemeImageAsset(asset.Path, asset.Data)
	if err != nil {
		return themeStoreAssetDescriptor{}, fmt.Errorf("%w: %v", ErrThemeInvalid, err)
	}
	if asset.Bytes != 0 && asset.Bytes != decoded.Bytes {
		return themeStoreAssetDescriptor{}, fmt.Errorf("%w: %s declared size does not match its bytes", ErrThemeInvalid, asset.Path)
	}
	if asset.SHA256 != "" && !strings.EqualFold(asset.SHA256, decoded.SHA256) {
		return themeStoreAssetDescriptor{}, fmt.Errorf("%w: %s declared digest does not match its bytes", ErrThemeInvalid, asset.Path)
	}
	if asset.MIME != "" && asset.MIME != decoded.MIME {
		return themeStoreAssetDescriptor{}, fmt.Errorf("%w: %s declared MIME does not match its content", ErrThemeInvalid, asset.Path)
	}
	if asset.Width != 0 && asset.Width != decoded.Width {
		return themeStoreAssetDescriptor{}, fmt.Errorf("%w: %s declared width does not match its content", ErrThemeInvalid, asset.Path)
	}
	if asset.Height != 0 && asset.Height != decoded.Height {
		return themeStoreAssetDescriptor{}, fmt.Errorf("%w: %s declared height does not match its content", ErrThemeInvalid, asset.Path)
	}
	return themeStoreAssetDescriptor{
		Path:   decoded.Path,
		MIME:   decoded.MIME,
		Width:  decoded.Width,
		Height: decoded.Height,
		Bytes:  decoded.Bytes,
		SHA256: decoded.SHA256,
		data:   append([]byte(nil), decoded.Data...),
	}, nil
}

// themeStoreVersionFromPackage deep-copies and re-validates a parsed package.
// The returned ContentHash is recomputed from the copy; a non-empty caller hash
// must agree, but is never used as a directory name on its own.
func themeStoreVersionFromPackage(pkg *ThemePackage) (*themeStoreVersion, error) {
	if pkg == nil {
		return nil, fmt.Errorf("%w: nil theme package", ErrThemeInvalid)
	}
	manifest := pkg.Manifest
	if err := validateThemeManifest(manifest); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrThemeInvalid, err)
	}
	definition := themeStoreCloneDefinition(pkg.Theme)
	if err := themeStoreValidateDefinition(definition, manifest.ThemeAPIVersion); err != nil {
		return nil, err
	}
	version := &themeStoreVersion{
		ThemeID:  manifest.ID,
		Manifest: manifest,
		Theme:    definition,
	}
	if pkg.Preview != nil {
		descriptor, err := themeStoreValidatedAsset(*pkg.Preview, true)
		if err != nil {
			return nil, err
		}
		version.Preview = &descriptor
	}
	imageCount := len(pkg.Assets)
	if version.Preview != nil {
		imageCount++
	}
	if imageCount > ThemeMaxImages {
		return nil, fmt.Errorf("%w: package has %d images, limit %d", ErrThemeInvalid, imageCount, ThemeMaxImages)
	}
	version.Assets = make([]themeStoreAssetDescriptor, 0, len(pkg.Assets))
	seen := make(map[string]struct{}, len(pkg.Assets))
	for _, asset := range pkg.Assets {
		descriptor, err := themeStoreValidatedAsset(asset, false)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[descriptor.Path]; duplicate {
			return nil, fmt.Errorf("%w: duplicate asset %s", ErrThemeInvalid, descriptor.Path)
		}
		seen[descriptor.Path] = struct{}{}
		version.Assets = append(version.Assets, descriptor)
	}
	sort.Slice(version.Assets, func(i, j int) bool {
		return version.Assets[i].Path < version.Assets[j].Path
	})
	var totalPixels int64
	for _, descriptor := range version.Assets {
		totalPixels += int64(descriptor.Width) * int64(descriptor.Height)
	}
	if version.Preview != nil {
		totalPixels += int64(version.Preview.Width) * int64(version.Preview.Height)
	}
	if totalPixels > ThemePackageMaxPixels {
		return nil, fmt.Errorf("%w: package images exceed the pixel budget", ErrThemeInvalid)
	}
	if err := themeStoreValidateAssetReferences(version); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrThemeInvalid, err)
	}
	assets := make([]ThemeAsset, 0, len(version.Assets))
	for _, descriptor := range version.Assets {
		assets = append(assets, descriptor.themeAsset())
	}
	var preview *ThemeAsset
	if version.Preview != nil {
		value := version.Preview.themeAsset()
		preview = &value
	}
	contentHash, err := themePackageContentHash(manifest, definition, preview, assets)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrThemeInvalid, err)
	}
	if pkg.ContentHash != "" && !strings.EqualFold(pkg.ContentHash, contentHash) {
		return nil, fmt.Errorf("%w: package content hash does not match its validated data", ErrThemeInvalid)
	}
	version.ContentHash = contentHash
	packageBytes := int64(pkg.PackageBytes)
	if packageBytes <= 0 || packageBytes > ThemePackageMaxBytes {
		packageBytes = 0
		for _, descriptor := range version.allDescriptors() {
			packageBytes += int64(descriptor.Bytes)
		}
	}
	version.PackageBytes = packageBytes
	return version, nil
}

// themeStorePurgeUnreferencedLocked removes crash leftovers and unreferenced
// strict hash directories. Unknown entries are reported and never deleted.
// Callers hold s.mu for writing.
func (s *ThemeStore) themeStorePurgeUnreferencedLocked() (int, int64, error) {
	referenced := make(map[string]struct{}, len(s.entries)*2)
	for _, entry := range s.entries {
		if entry.current != nil {
			referenced[entry.current.ContentHash] = struct{}{}
		}
		if entry.previous != nil {
			referenced[entry.previous.ContentHash] = struct{}{}
		}
	}
	names, err := os.ReadDir(s.root)
	if err != nil {
		return 0, 0, err
	}
	removedDirs := 0
	var removedBytes int64
	for _, item := range names {
		name := item.Name()
		if _, keep := referenced[name]; keep {
			continue
		}
		removable := themeStoreHashName(name) ||
			strings.HasPrefix(name, ".staging-") ||
			strings.HasPrefix(name, ".trash-")
		if !removable {
			s.noteProblemLocked("theme root contains an unexpected entry %q", name)
			continue
		}
		full := filepath.Join(s.root, name)
		bytes := themeStoreTreeBytes(full)
		if err := themeStoreRemoveTree(s.root, full); err != nil {
			s.noteProblemLocked("cannot remove theme leftover %q: %v", name, err)
			continue
		}
		if bytes > 0 {
			removedBytes += bytes
		}
		removedDirs++
	}
	return removedDirs, removedBytes, nil
}

// themeStoreStageVersionLocked writes one immutable version directory through a
// private staging directory and renames it into place. When the exact content
// hash is already referenced (for example re-selecting the previous version),
// the existing directory is reused after a full integrity check.
func (s *ThemeStore) themeStoreStageVersionLocked(version *themeStoreVersion) (string, bool, error) {
	destination := themeStoreVersionDir(s.root, version.ContentHash)
	if info, err := os.Lstat(destination); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			if err := themeStoreRemoveTree(s.root, destination); err != nil {
				return "", false, fmt.Errorf("remove colliding theme resource: %w", err)
			}
		} else if s.versions[version.ContentHash] != nil {
			if err := themeStoreVersionFilesValid(s.root, version); err != nil {
				return "", false, err
			}
			return destination, false, nil
		} else if err := themeStoreRemoveTree(s.root, destination); err != nil {
			return "", false, fmt.Errorf("remove stale theme resource: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	staging, err := os.MkdirTemp(s.root, ".staging-")
	if err != nil {
		return "", false, err
	}
	cleanup := func() { _ = os.RemoveAll(staging) }
	for _, descriptor := range version.allDescriptors() {
		target := filepath.Join(staging, filepath.FromSlash(descriptor.Path))
		if !strings.HasPrefix(target, staging+string(os.PathSeparator)) {
			cleanup()
			return "", false, fmt.Errorf("%w: resource path escapes staging directory", ErrThemeCorrupt)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			cleanup()
			return "", false, err
		}
		file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			cleanup()
			return "", false, err
		}
		if _, err := file.Write(descriptor.data); err != nil {
			_ = file.Close()
			cleanup()
			return "", false, err
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			cleanup()
			return "", false, err
		}
		if err := file.Close(); err != nil {
			cleanup()
			return "", false, err
		}
	}
	// Make the renames durable across a crash where the platform supports it.
	if runtime.GOOS != "windows" {
		if directory, err := os.Open(staging); err == nil {
			_ = directory.Sync()
			_ = directory.Close()
		}
	}
	if err := os.Rename(staging, destination); err != nil {
		cleanup()
		return "", false, err
	}
	return destination, true, nil
}

// themeStorePruneVersionIndexLocked keeps only versions referenced as current or
// previous. Callers hold s.mu for writing.
func (s *ThemeStore) themeStorePruneVersionIndexLocked() {
	referenced := make(map[string]struct{}, len(s.entries)*2)
	for _, entry := range s.entries {
		if entry.current != nil {
			referenced[entry.current.ContentHash] = struct{}{}
		}
		if entry.previous != nil {
			referenced[entry.previous.ContentHash] = struct{}{}
		}
	}
	for hash := range s.versions {
		if _, keep := referenced[hash]; !keep {
			delete(s.versions, hash)
		}
	}
}

// ReadAsset returns one registered public resource as a copy. Unknown hashes or
// names are 404-level errors and never fall back to an arbitrary file path.
func (s *ThemeStore) ReadAsset(contentHash, name string) (ThemeResource, error) {
	if s == nil {
		return ThemeResource{}, ErrThemeStoreUnavailable
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.liveLocked(); err != nil {
		return ThemeResource{}, err
	}
	version := s.versions[contentHash]
	if version == nil {
		return ThemeResource{}, ErrThemeResourceNotFound
	}
	var descriptor *themeStoreAssetDescriptor
	if version.Preview != nil && version.Preview.Path == name {
		descriptor = version.Preview
	}
	if descriptor == nil {
		for index := range version.Assets {
			if version.Assets[index].Path == name {
				descriptor = &version.Assets[index]
				break
			}
		}
	}
	if descriptor == nil {
		return ThemeResource{}, ErrThemeResourceNotFound
	}
	full, err := themeStoreResourcePath(s.root, contentHash, descriptor.Path)
	if err != nil {
		return ThemeResource{}, err
	}
	// Re-check the on-disk type on every read: a file swapped for a symlink or
	// another special file after installation must not be followed, even when
	// the target bytes would satisfy the recorded digest.
	info, err := os.Lstat(full)
	if err != nil {
		return ThemeResource{}, fmt.Errorf("%w: %s is missing: %v", ErrThemeCorrupt, descriptor.Path, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return ThemeResource{}, fmt.Errorf("%w: %s is not a plain file", ErrThemeCorrupt, descriptor.Path)
	}
	payload, err := os.ReadFile(full)
	if err != nil {
		return ThemeResource{}, fmt.Errorf("%w: %s is missing: %v", ErrThemeCorrupt, descriptor.Path, err)
	}
	if len(payload) != descriptor.Bytes {
		return ThemeResource{}, fmt.Errorf("%w: %s size changed on disk", ErrThemeCorrupt, descriptor.Path)
	}
	sum := sha256.Sum256(payload)
	if hex.EncodeToString(sum[:]) != descriptor.SHA256 {
		return ThemeResource{}, fmt.Errorf("%w: %s content changed on disk", ErrThemeCorrupt, descriptor.Path)
	}
	return ThemeResource{
		ContentHash: contentHash,
		Path:        descriptor.Path,
		MIME:        descriptor.MIME,
		Bytes:       descriptor.Bytes,
		SHA256:      descriptor.SHA256,
		Data:        payload,
	}, nil
}

// Definition returns a deep copy of the validated theme definition for one
// installed version, for server-side CSS generation. Unknown hashes and closed
// stores are reported explicitly; a failure is never converted into a default
// definition.
func (s *ThemeStore) Definition(contentHash string) (ThemeDefinition, error) {
	if s == nil {
		return ThemeDefinition{}, ErrThemeStoreUnavailable
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.liveLocked(); err != nil {
		return ThemeDefinition{}, err
	}
	version := s.versions[contentHash]
	if version == nil {
		return ThemeDefinition{}, ErrThemeResourceNotFound
	}
	return cloneThemeDefinition(version.Theme), nil
}

// InstallUpdate installs a new theme or publishes a new version of an existing
// one. expectedRevision 0 means "first install only"; every update requires the
// exact current positive revision. The revision is re-checked inside the commit
// transaction. A failed database commit removes the newly staged directory and
// keeps the old current version.
func (s *ThemeStore) InstallUpdate(pkg *ThemePackage, expectedRevision int64) (ThemeCatalogEntry, error) {
	if s == nil {
		return ThemeCatalogEntry{}, ErrThemeStoreUnavailable
	}
	version, err := themeStoreVersionFromPackage(pkg)
	if err != nil {
		return ThemeCatalogEntry{}, err
	}
	if expectedRevision < 0 || expectedRevision > ThemeMaxRevision {
		return ThemeCatalogEntry{}, fmt.Errorf("%w: expected revision out of range", ErrThemeInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.liveLocked(); err != nil {
		return ThemeCatalogEntry{}, err
	}
	existing := s.entries[version.ThemeID]
	switch {
	case existing == nil && expectedRevision != 0:
		return ThemeCatalogEntry{}, fmt.Errorf("%w: theme %s is not installed", ErrThemeRevisionConflict, version.ThemeID)
	case existing != nil && expectedRevision == 0:
		return ThemeCatalogEntry{}, fmt.Errorf("%w: %s", ErrThemeExists, version.ThemeID)
	case existing != nil && existing.row.Revision != expectedRevision:
		return ThemeCatalogEntry{}, fmt.Errorf("%w: theme %s revision changed", ErrThemeRevisionConflict, version.ThemeID)
	}
	if existing == nil && len(s.entries) >= ThemeMaxCustomThemes {
		return ThemeCatalogEntry{}, fmt.Errorf("%w: %d custom themes installed", ErrThemeQuota, len(s.entries))
	}
	if err := s.db.withReadLock(func() error { return nil }); err != nil {
		return ThemeCatalogEntry{}, fmt.Errorf("%w: %v", ErrThemeStoreUnavailable, err)
	}
	purgedDirs, purgedBytes, err := s.themeStorePurgeUnreferencedLocked()
	if err != nil {
		return ThemeCatalogEntry{}, fmt.Errorf("purge theme leftovers: %w", err)
	}
	if purgedDirs > 0 {
		s.health.OrphanDirs += purgedDirs
		s.health.OrphanBytes += purgedBytes
	}

	oldCurrent := (*themeStoreVersion)(nil)
	oldPreviousHash := ""
	enabled := true
	installedAt := time.Now().UnixMilli()
	oldRevision := int64(0)
	if existing != nil {
		oldCurrent = existing.current
		oldPreviousHash = existing.row.PreviousHash
		enabled = existing.row.Enabled
		installedAt = existing.row.InstalledAt
		oldRevision = existing.row.Revision
	}
	sameCurrent := oldCurrent != nil && oldCurrent.ContentHash == version.ContentHash
	newBytes := int64(0)
	if !sameCurrent && s.versions[version.ContentHash] == nil {
		newBytes = themeStoreVersionPayloadBytes(version)
	}
	reclaimedHash := ""
	if newBytes > 0 {
		used, err := themeStoreDiskUsage(s.root)
		if err != nil {
			return ThemeCatalogEntry{}, fmt.Errorf("theme store usage: %w", err)
		}
		if used+newBytes > themeStoreMaxBytes {
			// Old previous versions are recovery aids, not the live version.
			// Reclaim one before staging so a successful publish can never
			// exceed the byte budget; failing to reclaim rejects the install.
			if existing != nil && existing.previous != nil && oldCurrent != nil &&
				existing.previous.ContentHash != oldCurrent.ContentHash &&
				existing.previous.ContentHash != version.ContentHash {
				reclaim := existing.previous.ContentHash
				if err := themeStoreRemoveTree(s.root, themeStoreVersionDir(s.root, reclaim)); err != nil {
					return ThemeCatalogEntry{}, fmt.Errorf("%w: cannot reclaim previous version: %v", ErrThemeQuota, err)
				}
				delete(s.versions, reclaim)
				existing.previous = nil
				existing.row.PreviousHash = ""
				reclaimedHash = reclaim
				s.noteProblemLocked("reclaimed previous version %s of %s for quota", reclaim, version.ThemeID)
				used, err = themeStoreDiskUsage(s.root)
				if err != nil {
					return ThemeCatalogEntry{}, fmt.Errorf("theme store usage: %w", err)
				}
			}
			if used+newBytes > themeStoreMaxBytes {
				return ThemeCatalogEntry{}, fmt.Errorf("%w: theme resources need %d bytes, %d used of %d",
					ErrThemeQuota, newBytes, used, themeStoreMaxBytes)
			}
		}
	}

	nextPrevious := ""
	if oldCurrent != nil && oldCurrent.ContentHash != version.ContentHash {
		nextPrevious = oldCurrent.ContentHash
	} else if sameCurrent {
		nextPrevious = oldPreviousHash
	}
	if nextPrevious == version.ContentHash {
		nextPrevious = ""
	}
	newRow := themeStoreCatalogRow{
		ID:           version.ThemeID,
		Name:         version.Manifest.Name,
		Author:       version.Manifest.Author,
		Description:  version.Manifest.Description,
		Version:      version.Manifest.Version,
		Enabled:      enabled,
		CurrentHash:  version.ContentHash,
		PreviousHash: nextPrevious,
		Revision:     nextThemeRevision(oldRevision),
		InstalledAt:  installedAt,
	}
	prune := make([]string, 0, 2)
	addPrune := func(hash string) {
		if hash == "" || hash == version.ContentHash || hash == nextPrevious {
			return
		}
		for _, existingHash := range prune {
			if existingHash == hash {
				return
			}
		}
		prune = append(prune, hash)
	}
	addPrune(oldPreviousHash)
	addPrune(reclaimedHash)

	directory, created, err := s.themeStoreStageVersionLocked(version)
	if err != nil {
		return ThemeCatalogEntry{}, fmt.Errorf("stage theme resources: %w", err)
	}
	err = s.db.withWriteTx(func() error {
		stored, found, err := themeStoreReadCatalogRow(s.db, version.ThemeID)
		if err != nil {
			return err
		}
		if found != (existing != nil) {
			return fmt.Errorf("%w: theme %s installation state changed", ErrThemeRevisionConflict, version.ThemeID)
		}
		if found {
			if stored.Revision != expectedRevision {
				return fmt.Errorf("%w: theme %s revision changed", ErrThemeRevisionConflict, version.ThemeID)
			}
		} else if expectedRevision != 0 {
			return fmt.Errorf("%w: theme %s is not installed", ErrThemeRevisionConflict, version.ThemeID)
		}
		if err := themeStoreWriteVersion(s.db, version); err != nil {
			return err
		}
		if err := themeStoreUpsertCatalogRow(s.db, newRow); err != nil {
			return err
		}
		for _, hash := range prune {
			if err := themeStoreDeleteVersion(s.db, hash); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if created {
			if removeErr := themeStoreRemoveTree(s.root, directory); removeErr != nil {
				s.noteProblemLocked("cannot remove failed theme publish %s: %v", version.ContentHash, removeErr)
			}
		}
		return ThemeCatalogEntry{}, fmt.Errorf("publish theme %s: %w", version.ThemeID, err)
	}

	entry := existing
	if entry == nil {
		entry = &themeStoreEntry{row: newRow}
		s.entries[version.ThemeID] = entry
	}
	entry.row = newRow
	entry.current = version
	entry.previous = nil
	if nextPrevious != "" {
		entry.previous = s.versions[nextPrevious]
	}
	entry.available = true
	entry.problem = ""
	s.versions[version.ContentHash] = version
	s.themeStorePruneVersionIndexLocked()
	for _, hash := range prune {
		if err := themeStoreRemoveTree(s.root, themeStoreVersionDir(s.root, hash)); err != nil {
			s.noteProblemLocked("cannot remove pruned theme version %s: %v", hash, err)
		}
	}
	return themeStoreCatalogEntryFromMirror(entry), nil
}

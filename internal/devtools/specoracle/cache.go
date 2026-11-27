package specoracle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

var errInvalidSnapshot = errors.New("specoracle: invalid declared cache snapshot")

// A manifest is the current snapshot, not a hint about a directory that may
// retain pages from earlier publications. Unmanifested legacy fixtures remain
// readable by standalone tools; CheckFamilies never qualifies them for parity.
func readBlocks(cacheDir string) ([]block, []string, error) {
	m, err := ReadManifest(cacheDir)
	if errors.Is(err, os.ErrNotExist) {
		return readLegacyBlocks(cacheDir)
	}
	if err != nil {
		return nil, nil, err
	}
	blocks, err := manifestBlocks(cacheDir, m)
	return blocks, nil, err
}

func manifestBlocks(cacheDir string, m Manifest) ([]block, error) {
	root, err := os.OpenRoot(cacheDir)
	if err != nil {
		return nil, fmt.Errorf("specoracle: open cache: %w", err)
	}
	defer func() { _ = root.Close() }()
	declared := map[string]bool{}
	for _, fam := range m.Families {
		declared[fam.Family] = true
	}
	seen := map[string]bool{}
	var blocks []block
	for _, entry := range m.Entries {
		if entry.File == "" {
			continue
		}
		parts := strings.Split(entry.File, "/")
		if len(parts) != 2 || !safeCachePart(parts[0]) || !safeCachePart(parts[1]) ||
			!strings.HasSuffix(parts[1], ".json") || path.Clean(entry.File) != entry.File || seen[entry.File] {
			return nil, fmt.Errorf("%w: unsafe or repeated file %q", errInvalidSnapshot, entry.File)
		}
		family := entry.Family
		if family == "" {
			family = parts[0]
		}
		if family != parts[0] || (len(declared) != 0 && !declared[family]) {
			return nil, fmt.Errorf("%w: file %s does not belong to declared family %s", errInvalidSnapshot, entry.File, family)
		}
		seen[entry.File] = true
		for _, rel := range []string{parts[0], entry.File} {
			info, err := root.Lstat(filepath.FromSlash(rel))
			if err != nil {
				return nil, fmt.Errorf("%w: stat %s: %w", errInvalidSnapshot, rel, err)
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("%w: symlink %s", errInvalidSnapshot, rel)
			}
			if (rel == parts[0] && !info.IsDir()) || (rel == entry.File && !info.Mode().IsRegular()) {
				return nil, fmt.Errorf("%w: non-regular cache path %s", errInvalidSnapshot, rel)
			}
		}
		data, err := root.ReadFile(filepath.FromSlash(entry.File))
		if err != nil {
			return nil, fmt.Errorf("%w: read %s: %w", errInvalidSnapshot, entry.File, err)
		}
		if entry.SHA256 != "" {
			sum := sha256.Sum256(data)
			if entry.SHA256 != hex.EncodeToString(sum[:]) {
				return nil, fmt.Errorf("%w: checksum %s", errInvalidSnapshot, entry.File)
			}
		}
		var doc map[string]any
		if err := json.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("%w: parse %s: %w", errInvalidSnapshot, entry.File, err)
		}
		paths, ok := doc["paths"].(map[string]any)
		if !ok || len(paths) == 0 {
			return nil, fmt.Errorf("%w: no paths in %s", errInvalidSnapshot, entry.File)
		}
		source := entry.URL
		if source == "" {
			source = entry.File
		}
		blocks = append(blocks, block{family: family, page: strings.TrimSuffix(parts[1], ".json"), source: source, doc: doc, canonical: entry.Canonical})
	}
	return blocks, nil
}

func safeCachePart(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, `/\:`)
}

type operationOwner struct{ family, source string }

// Duplicate pages still contribute every structural fact. Only a verified full
// definition chooses canonical ownership; directory order has no authority.
func canonicalOwners(blocks []block) (map[string]operationOwner, error) {
	owners := map[string]operationOwner{}
	for _, b := range blocks {
		if !b.canonical {
			continue
		}
		o := Oracle{Ops: map[string]Operation{}}
		o.add(b.doc, b.family, b.source)
		for key := range o.Ops {
			if prev, ok := owners[key]; ok && prev.family != b.family {
				return nil, fmt.Errorf("%w: conflicting owners for %s", errInvalidSnapshot, key)
			}
			owners[key] = operationOwner{b.family, b.source}
		}
	}
	return owners, nil
}

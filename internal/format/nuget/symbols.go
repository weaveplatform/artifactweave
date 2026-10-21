package nuget

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/archive"
	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const symbolPrefix = ".symbols/"

// SymbolSource is trusted package-index metadata, never an item property.
// It records every source, including duplicates recovered by trash restore,
// so deleting one owner cannot remove a surviving owner's symbols.
type SymbolSource struct {
	Path   string       `json:"path"`
	SHA256 string       `json:"sha256"`
	Files  []SymbolFile `json:"files"`
}

// SymbolFile identifies extracted content at its canonical repository path.
type SymbolFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// SymbolPath canonicalizes both names and the key. Microsoft's symbol key
// convention is case-insensitive; keeping one spelling also avoids divergent
// derived files when two target frameworks contain the same PDB.
func SymbolPath(name, key, file string) (string, bool) {
	if !strings.EqualFold(name, file) || !strings.HasSuffix(strings.ToLower(name), ".pdb") ||
		len(name) <= 4 || len(key) < 33 || len(key) > 40 {
		return "", false
	}
	clean, err := archive.Clean(name)
	if err != nil || clean != name || strings.ContainsAny(name, "/\\") {
		return "", false
	}
	for _, c := range key {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return "", false
		}
	}
	name = strings.ToLower(name)
	return symbolPrefix + name + "/" + strings.ToUpper(key) + "/" + name, true
}

// The archive entry cap matches nuspec inspection. Actual decompressed PDB
// bytes, not ZIP header sizes, are bounded per file and per symbol package.
var symbolLimits = archive.Limits{Entries: 1 << 20, File: 64 << 20, Inflated: 256 << 20}

func readSymbols(ctx context.Context, env format.Env, sha string) (map[string][]byte, error) {
	rc, info, err := env.Blobs.Open(ctx, sha)
	if err != nil {
		return nil, fmt.Errorf("nuget: open symbols: %w", err)
	}
	defer func() { _ = rc.Close() }()
	return readSymbolArchive(ctx, archive.ReaderAt(rc), info.Size, symbolLimits)
}

func readSymbolArchive(
	ctx context.Context,
	ra io.ReaderAt,
	size int64,
	lim archive.Limits,
) (map[string][]byte, error) {
	src := &firstErr{ra: ra}
	out := map[string][]byte{}
	left := lim.Inflated
	err := archive.Scan(src, size, archive.Zip, lim, func(e archive.Entry) error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("nuget: symbols: %w", err)
		}
		name, err := archive.Clean(e.Name)
		if err != nil || !e.Regular || !strings.HasSuffix(strings.ToLower(name), ".pdb") {
			return nil //nolint:nilerr // unsafe entries and non-PDBs produce no derived item
		}
		entryLimit := lim
		entryLimit.File = min(lim.File, left)
		body, err := archive.ReadEntry(e, entryLimit)
		if err != nil {
			return fmt.Errorf("nuget: symbol %s: %w", name, err)
		}
		left -= int64(len(body))
		key, ok := PDBKey(body)
		if !ok { // Unsupported or malformed PDBs remain in the original archive only.
			return nil
		}
		p, ok := SymbolPath(path.Base(name), key, path.Base(name))
		if ok {
			// Conflicts inside one archive are independent of ZIP entry order.
			if old, exists := out[p]; !exists || bytes.Compare(body, old) < 0 {
				out[p] = body
			}
		}
		return nil
	})
	if src.err != nil {
		return nil, fmt.Errorf("nuget: read symbols: %w", src.err)
	}
	if err != nil {
		return nil, fmt.Errorf("nuget: symbols archive: %w", err)
	}
	return out, nil
}

func symbolManifest(body map[string][]byte) []SymbolFile {
	out := make([]SymbolFile, 0, len(body))
	for p, b := range body {
		digest := sha256.Sum256(b)
		out = append(out, SymbolFile{Path: p, SHA256: hex.EncodeToString(digest[:])})
	}
	slices.SortFunc(out, func(a, b SymbolFile) int { return cmp.Compare(a.Path, b.Path) })
	return out
}

func symbolsOf(
	ctx context.Context,
	env format.Env,
	it types.Item,
	old []SymbolSource,
) (SymbolSource, error) {
	for _, s := range old {
		if s.Path == it.FullPath() && s.SHA256 == it.Actual.SHA256 {
			return s, nil
		}
	}
	var body map[string][]byte
	if in, ok := env.Inspection.(*inspection); ok && in.sha256 == it.Actual.SHA256 {
		body = in.symbols
	}
	if body == nil {
		var err error
		body, err = readSymbols(ctx, env, it.Actual.SHA256)
		if err != nil {
			return SymbolSource{}, err
		}
	}
	return SymbolSource{
		Path:   it.FullPath(),
		SHA256: it.Actual.SHA256,
		Files:  symbolManifest(body),
	}, nil
}

// sourcesOf keeps the manifest of unchanged archives while a normal package
// deploy refreshes the rest of its version's metadata.
func sourcesOf(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	members []member,
	old Metadata,
) ([]SymbolSource, error) {
	if c.Class != repo.Local {
		return nil, nil //nolint:nilnil // remote symbol serving belongs to P9
	}
	var out []SymbolSource
	for _, m := range members {
		if m.kind != symbolsFile {
			continue
		}
		s, err := symbolsOf(ctx, env, m.it, old.SymbolSources)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b SymbolSource) int { return cmp.Compare(a.Path, b.Path) })
	return out, nil
}

// syncSymbols scans trusted index metadata, not every archive. Only a changed
// winning source is opened. Sorting by source path makes collisions stable
// across deploy order, delete, restore and reindex; other owners survive.
func syncSymbols(ctx context.Context, env format.Env, c repo.Config) error {
	if c.Class != repo.Local {
		return nil
	}
	owners, err := symbolOwners(ctx, env.Store, c)
	if err != nil {
		return err
	}
	q := &aql.Query{Criteria: aql.And{
		aql.RepoSet{Refs: []aql.RepoRef{{Key: c.Key}}},
		&aql.FieldCriterion{Field: aql.FieldType, Op: aql.OpEq, Text: "file"},
		&aql.FieldCriterion{Field: aql.FieldPath, Op: aql.OpMatch, Text: symbolPrefix + "*"},
	}}
	hits, err := env.Store.Items().Search(ctx, q, aql.Options{})
	if err != nil {
		return fmt.Errorf("nuget: derived symbols: %w", err)
	}
	existing := map[string]types.Item{}
	for _, hit := range hits {
		p := hit.Item.FullPath()
		existing[p] = hit.Item
		if _, ok := owners[p]; !ok {
			if err := env.Underive(ctx, c, false, p); err != nil {
				return fmt.Errorf("nuget: remove symbol %s: %w", p, err)
			}
		}
	}
	var lastSHA string
	var body map[string][]byte
	// Group by source to decompress each changed archive once.
	paths := make([]string, 0, len(owners))
	for p := range owners {
		paths = append(paths, p)
	}
	slices.SortFunc(paths, func(a, b string) int {
		return cmp.Or(
			cmp.Compare(owners[a].source.SHA256, owners[b].source.SHA256),
			cmp.Compare(a, b),
		)
	})
	for _, p := range paths {
		o := owners[p]
		if it, ok := existing[p]; ok && it.Actual.SHA256 == o.file.SHA256 {
			continue
		}
		if lastSHA != o.source.SHA256 {
			body, err = readSymbols(ctx, env, o.source.SHA256)
			if err != nil {
				return err
			}
			lastSHA = o.source.SHA256
		}
		if _, err := env.Derive(
			ctx,
			format.Derived{Repo: c, Path: p, Body: bytes.NewReader(body[p]), By: "_system_"},
		); err != nil {
			return fmt.Errorf("nuget: derive symbol %s: %w", p, err)
		}
	}
	return nil
}

type symbolOwner struct {
	source SymbolSource
	file   SymbolFile
}

func symbolOwners(
	ctx context.Context,
	st store.Store,
	c repo.Config,
) (map[string]symbolOwner, error) {
	out := map[string]symbolOwner{}
	page := store.Page{Limit: store.MaxLimit}
	for {
		packages, next, err := st.Packages().List(ctx, c.Key, page)
		if err != nil {
			return nil, fmt.Errorf("nuget: symbol packages: %w", err)
		}
		for _, p := range packages {
			versions, err := st.Packages().Versions(ctx, p.ID)
			if err != nil {
				return nil, fmt.Errorf("nuget: symbol versions: %w", err)
			}
			for _, v := range versions {
				m, err := DecodeMetadata(v.Metadata)
				if err != nil {
					return nil, err
				}
				for _, s := range m.SymbolSources {
					for _, f := range s.Files {
						if old, ok := out[f.Path]; !ok || s.Path < old.source.Path {
							out[f.Path] = symbolOwner{source: s, file: f}
						}
					}
				}
			}
		}
		if next == "" {
			break
		}
		page.After = next
	}
	return out, nil
}

// Classify excludes regenerated symbols from replication; the .snupkg is
// the source of truth and travels through normal package admission.
func (Handler) Classify(_ repo.Config, p string) format.Class {
	if strings.HasPrefix(p, symbolPrefix) {
		return format.ClassDerived
	}
	return format.ClassUnique
}

func inspectSymbols(ctx context.Context, env format.Env, in *inspection) error {
	var err error
	in.symbols, err = readSymbols(ctx, env, in.sha256)
	if errors.Is(err, archive.ErrUnusable) {
		in.nuspec = nil
		in.invalid = err.Error()
		return nil
	}
	return err
}

// KindSymbols reconciles the PDBs of a repository after source commits.
// The work queue serializes a repository's runs and preserves an enqueue
// made during a run. Source transactions never lock derived items: that
// would invert lock order during deletion and cross-repository moves.
const KindSymbols = "nuget.symbols"

func queueSymbols(ctx context.Context, env format.Env, c repo.Config) error {
	if c.Class != repo.Local {
		return nil
	}
	if err := env.Enqueue(ctx, KindSymbols, c.Key, nil); err != nil {
		return fmt.Errorf("nuget: queue symbols: %w", err)
	}
	return nil
}

func symbolWork(ctx context.Context, env format.Env, key string) error {
	c, err := env.Store.Repos().Get(ctx, key)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil
	case err != nil:
		return fmt.Errorf("nuget: symbol repository: %w", err)
	case c.Class != repo.Local || !Serves(c.PackageType):
		return nil
	}
	if err := env.Store.Tx(
		ctx,
		func(ctx context.Context) error { return syncSymbols(ctx, env, c) },
	); err != nil {
		return fmt.Errorf("nuget: reconcile symbols: %w", err)
	}
	return nil
}

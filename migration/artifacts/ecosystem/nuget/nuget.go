package nuget

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/pkgindex"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Serves reports whether t is a package type of this format.
func Serves(t repo.PackageType) bool { return t == repo.NuGet }

// Handler is the NuGet format.
type Handler struct {
	// The generic format's hooks for what NuGet leaves alone; Admit adds
	// the package checks to its deploy policy.
	generic.Handler
}

// inspection is what Inspect read for a write of a .nupkg or .snupkg.
type inspection struct {
	// sha256 is the content read, so a hook can tell the inspection of its
	// own write from one of other content.
	sha256 string
	// nuspec is the package's, nil when the content is no valid package.
	nuspec *Nuspec
	// invalid says why the content is no valid package.
	invalid string
	symbols map[string][]byte
}

// Inspect reads a .nupkg or .snupkg before the write's transaction. Other
// content needs no reading.
func (Handler) Inspect(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	w format.Write,
) (any, error) {
	if w.Checksums.SHA256 == "" || kindOf(w.Path) == other {
		return nil, nil //nolint:nilnil // nothing to read is no failure
	}
	in, err := read(ctx, env, w.Checksums.SHA256)
	if err == nil && in.nuspec != nil && kindOf(w.Path) == symbolsFile && c.Class == repo.Local {
		err = inspectSymbols(ctx, env, in)
	}
	return in, err
}

func read(ctx context.Context, env format.Env, sha string) (*inspection, error) {
	n, err := ReadStored(ctx, env.Blobs, sha)
	switch {
	case errors.Is(err, ErrNotPackage):
		// The reason goes to the client in Admit's refusal, which names no
		// Go package.
		return &inspection{sha256: sha, invalid: strings.TrimPrefix(err.Error(), "nuget: ")}, nil
	case err != nil:
		return nil, err
	}
	return &inspection{sha256: sha, nuspec: n}, nil
}

// inspectionOf is the inspection of the write that stored it when there
// was one of this content, or else a read of the content, as a restore
// needs.
func inspectionOf(ctx context.Context, env format.Env, it types.Item) (*inspection, error) {
	if in, ok := env.Inspection.(*inspection); ok && in.sha256 == it.Actual.SHA256 {
		return in, nil
	}
	return read(ctx, env, it.Actual.SHA256)
}

// Deployed indexes a .nupkg or .snupkg stored at a canonical name of the
// package it holds, sets a .nupkg's nuget.* properties, and marks the
// package for its maxUniqueSnapshots cleanup when the version is a
// pre-release. A file that was another version's, such as one overwritten
// with other content, leaves that version first. A file stored anywhere
// but where the index reads (pkgindex.Stored) is not indexed.
func (h Handler) Deployed(ctx context.Context, env format.Env, c repo.Config, it types.Item) error {
	k := kindOf(it.FullPath())
	if it.Folder || it.Cached != pkgindex.Stored(c).Cached || k == other {
		return nil
	}
	in, err := inspectionOf(ctx, env, it)
	if err != nil {
		return err
	}
	n := in.nuspec
	ok := n != nil && canonical(it.FullPath(), k, n)
	var key versionKey
	if ok {
		key = keyOf(n)
	}
	if err := leave(ctx, env, c, it, key); err != nil {
		return err
	}
	if !ok {
		if k == symbolsFile {
			return queueSymbols(ctx, env, c)
		}
		return nil
	}
	if k == pkgFile {
		if err := setProperties(ctx, env.Store, it, n, it.Updated); err != nil {
			return err
		}
	}
	if err := sync(ctx, env, c, key, &member{it: it, kind: k, nuspec: n}); err != nil {
		return err
	}
	if k == symbolsFile {
		if err := queueSymbols(ctx, env, c); err != nil {
			return err
		}
	}
	if k == pkgFile && n.Version.IsPrerelease() {
		return markCleanup(ctx, env, c, it, key)
	}
	return nil
}

// leave drops it from the version it was a file of, unless that is key's:
// the version is synced again from its other files, and goes when none is
// left.
func leave(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	it types.Item,
	key versionKey,
) error {
	pv, linked, err := pkgindex.VersionOf(ctx, env.Store, it.ID)
	if err != nil || !linked {
		return err //nolint:wrapcheck // pkgindex wraps its own
	}
	p, err := env.Store.Packages().Get(ctx, pv.PackageID)
	if err != nil {
		return fmt.Errorf("nuget: package %d: %w", pv.PackageID, err)
	}
	was := versionKey{pkg: p.Name, version: pv.Version}
	if was == key {
		return nil
	}
	return syncWithout(ctx, env, c, was, it.ID)
}

// Deleted brings each version a delete or a move took a file from in line
// with the files it has left: pruning has dropped the versions left with
// none, and a version left with its .snupkg alone, or with a second
// package a restore brought back, is described anew. The removed files'
// links are gone by now, so their versions are found by the names their
// paths give.
func (Handler) Deleted(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	removed []types.Item,
) error {
	stored := pkgindex.Stored(c).Cached
	seen := map[versionKey]bool{}
	symbolsChanged := false
	for _, it := range removed {
		if it.Folder || it.Cached != stored {
			continue
		}
		symbolsChanged = symbolsChanged || kindOf(it.FullPath()) == symbolsFile
		for _, key := range keysOf(it.FullPath()) {
			if seen[key] {
				continue
			}
			seen[key] = true
			if err := sync(ctx, env, c, key, nil); err != nil {
				return err
			}
		}
	}
	if symbolsChanged {
		return queueSymbols(ctx, env, c)
	}
	return nil
}

// Reindex rebuilds the repository's NuGet index from its .nupkg and .snupkg
// files, a remote repository's from its cache, and sets each package's nuget.*
// properties from its nuspec again, as 7.x "re-annotate[s] the NuGet
// properties for each NuGet package according to its internal nuspec file"
// (calculatenugetrepositorymetadata). Which repositories the JFrog reindex
// operation accepts is the API's rule, answered in artifacts/adapters/http.
func (h Handler) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	return reindex(ctx, env, c)
}

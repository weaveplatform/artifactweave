// Package maven is the format of maven, gradle, ivy and sbt repositories
// (decision 0036): Maven's naming predicates, the coordinates a repository's
// layout reads from a path, and the package index of decision 0018, kept by
// the hooks on every write path and rebuilt by Reindex.
//
// A package is groupId:artifactId, from the layout's organisation and
// module; a version is the name of its version folder, so every unique
// build of 1.0-SNAPSHOT is a file of that one version; and versions order
// by types.CompareMercury, as Artifactory's metadata and searches do.
package maven

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/pkg/layout"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Serves reports the package types of Artifactory's Maven group that
// artifactweave serves. p2, which Artifactory also counts, is only ever a
// remote or virtual repository.
func Serves(t repo.PackageType) bool {
	switch t {
	case repo.Maven, repo.Gradle, repo.Ivy, repo.SBT:
		return true
	}
	return false
}

// Handler is the Maven format: its deploy rules (admit.go), its download
// policy (policy.go) and the package index. Snapshot naming and derived
// metadata come with #279 to #283.
type Handler struct {
	// The generic format's hooks, which Admit's own checks replace.
	generic.Handler
}

// Deployed indexes a file stored in a version: the version is synced with
// the tree, so its date and files are what a reindex would read.
func (Handler) Deployed(ctx context.Context, env format.Env, c repo.Config, it types.Item) error {
	if it.Folder {
		return nil
	}
	r, err := readerOf(ctx, env, c)
	if err != nil {
		return err
	}
	co, ok := r.read(it.FullPath())
	if !ok {
		return nil
	}
	return syncVersion(ctx, env, c, r, it.Cached, co)
}

// Deleting runs before a file is removed. The item service drops the file's
// version link with it; Deleted settles the rest.
func (Handler) Deleting(context.Context, format.Env, repo.Config, types.Item) error { return nil }

// Deleted syncs every version a removal touched with the tree: a version
// that kept files may have lost its earliest, which dates it.
func (Handler) Deleted(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	removed []types.Item,
) error {
	r, err := readerOf(ctx, env, c)
	if err != nil {
		return err
	}
	touched := map[string]Coordinates{}
	cached := false
	for _, it := range removed {
		if it.Folder {
			continue
		}
		if co, ok := r.read(it.FullPath()); ok {
			touched[co.Package+" "+co.Version] = co
			cached = it.Cached
		}
	}
	// In key order, so the index changes the same way on every store.
	for _, k := range slices.Sorted(maps.Keys(touched)) {
		if err := syncVersion(ctx, env, c, r, cached, touched[k]); err != nil {
			return err
		}
	}
	return nil
}

// Reindex rebuilds the repository's package index from its tree, in one
// transaction.
func (Handler) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	r, err := readerOf(ctx, env, c)
	if err != nil {
		return err
	}
	return reindex(ctx, env, c, r)
}

// Classify reports maven-metadata.xml as derived, since #279 writes it
// from the index and a replica recomputes it, except a snapshot folder's
// under deployer, which is the client's own; a non-unique snapshot and a
// Nexus index file as metadata, since a later deploy changes them under the
// same path; and every other file as unique.
func (Handler) Classify(c repo.Config, p string) format.Class {
	switch {
	case IsMetadata(p) && IsSnapshotMetadata(p) && c.SnapshotVersionBehavior == repo.Deployer:
		return format.ClassMetadata
	case IsMetadata(p):
		return format.ClassDerived
	case IsNonUniqueSnapshot(p), IsIndex(p):
		return format.ClassMetadata
	}
	return format.ClassUnique
}

// reader reads coordinates through a repository's layout. Without one it
// reads none.
type reader struct{ l *layout.Layout }

func (r reader) read(p string) (Coordinates, bool) {
	if r.l == nil {
		return Coordinates{}, false
	}
	return Read(r.l, p)
}

// readerOf reads through c's layout. Repositories created without one have
// maven-2-default, as Artifactory's REST API gives them (repo.Defaults). A
// layout that is not defined, or does not compile, reads no path, so the
// repository keeps no index, as the layout searches skip it; only a failure
// to read the descriptor fails the write.
func readerOf(ctx context.Context, env format.Env, c repo.Config) (reader, error) {
	name := cmp.Or(c.RepoLayoutRef, repo.LayoutMaven2)
	l, err := env.Layouts.Layout(ctx, name)
	switch {
	case errors.Is(err, layout.ErrPattern):
		return reader{}, nil
	case err != nil:
		return reader{}, fmt.Errorf("maven: layout of %s: %w", c.Key, err)
	}
	return reader{l}, nil
}

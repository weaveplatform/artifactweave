package maven

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/layout"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The kinds of derived work the Maven format queues (decisions 0033 and
// 0036). A key is "<repository key>/<name>": repository keys hold no slash.
const (
	// KindMetadata rebuilds a package's maven-metadata.xml files, its
	// artifact-level one and each snapshot folder's, keyed by
	// groupId:artifactId.
	KindMetadata = "maven.metadata"
	// KindPlugins rebuilds a group's plugin metadata, keyed by groupId.
	KindPlugins = "maven.plugins"
)

// WorkKinds are the kinds the Maven format queues.
func (Handler) WorkKinds() []string { return []string{KindMetadata, KindPlugins} }

// Work rebuilds the metadata one item names from the package index as it
// stands now, under deployer as under the other behaviours, since only a
// delete asks for it there. A repository that is gone, or no longer a local
// repository of the Maven group, owes nothing.
func (Handler) Work(ctx context.Context, env format.Env, it types.WorkItem) error {
	key, name, ok := strings.Cut(it.Key, "/")
	if !ok {
		return nil
	}
	c, err := env.Store.Repos().Get(ctx, key)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil
	case err != nil:
		return fmt.Errorf("maven: repository %s: %w", key, err)
	case !Serves(c.PackageType) || !derived(c, false):
		// The repository changed since the work was asked for.
		return nil
	}
	r, err := readerOf(ctx, env, c)
	if err != nil || r.l == nil {
		return err
	}
	switch it.Kind {
	case KindMetadata:
		return writePackageMetadata(ctx, env, c, r, name)
	case KindPlugins:
		return writePluginMetadata(ctx, env, c, r, name)
	}
	return nil
}

// Owed names the work a download of a maven-metadata.xml, or of its
// sidecar, waits for: a snapshot folder's belongs to its package, and any
// other may be its folder's artifact-level or plugin metadata, so it waits
// for both. Waiting for work no one asked for costs one lookup.
func (Handler) Owed(c repo.Config, cached bool, p string) []format.WorkRef {
	if !derived(c, cached) || !IsMetadata(p) {
		return nil
	}
	dir := path.Dir(p)
	if dir == "." {
		return nil
	}
	if IsSnapshotMetadata(p) {
		if name, ok := packageOf(path.Dir(dir)); ok {
			return []format.WorkRef{{Kind: KindMetadata, Key: workKey(c.Key, name)}}
		}
		return nil
	}
	var out []format.WorkRef
	if name, ok := packageOf(dir); ok {
		out = append(out, format.WorkRef{Kind: KindMetadata, Key: workKey(c.Key, name)})
	}
	return append(out, format.WorkRef{Kind: KindPlugins, Key: workKey(c.Key, layout.Org(dir))})
}

func workKey(repoKey, name string) string { return repoKey + "/" + name }

// packageOf names the package whose artifact-level metadata a folder holds:
// the folder is the artifactId, and its parent, with dots for slashes, the
// groupId, as the Maven and Gradle layouts both place them.
func packageOf(dir string) (string, bool) {
	parent := path.Dir(dir)
	if parent == "." || dir == "." {
		return "", false
	}
	return layout.Org(parent) + ":" + path.Base(dir), true
}

// groupOf names the group whose plugin metadata a plugin POM belongs in: its
// great-grandparent folder, as MavenPluginsMetadataCalculator takes it.
func groupOf(pom string) (string, bool) {
	d := path.Dir(path.Dir(path.Dir(pom)))
	if d == "." || d == "/" {
		return "", false
	}
	return layout.Org(d), true
}

// derived reports whether c's files have derived Maven metadata at all:
// local repositories' own files do, remote caches' do not.
func derived(c repo.Config, cached bool) bool {
	return !cached && c.Class == repo.Local
}

// queued reports whether a write in c queues Maven metadata: one where
// metadata is derived, except under the deployer snapshot behaviour, which
// leaves a deploy's metadata to the client: Artifactory 7.x's documentation
// of the setting says "Metadata will not be generated when selecting the
// Deployer option". A delete queues it under deployer too (Deleted).
func queued(c repo.Config, cached bool) bool {
	return derived(c, cached) && c.SnapshotVersionBehavior != repo.Deployer
}

// queueWrite asks for the metadata a file written at p changes: its
// package's, for a file of a version or for the client's own
// artifact-level metadata, which the run replaces with the server's.
// A snapshot folder's metadata from the client is the deploy rules' (#278).
func queueWrite(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	cached bool,
	p string,
	co Coordinates,
	indexed bool,
) error {
	if !queued(c, cached) {
		return nil
	}
	name := co.Package
	if !indexed {
		if !IsMetadata(p) || IsSnapshotMetadata(p) {
			return nil
		}
		var ok bool
		if name, ok = packageOf(path.Dir(p)); !ok {
			return nil
		}
	}
	return enqueue(ctx, env, KindMetadata, c.Key, name)
}

// queuePlugin asks for the plugin metadata of the group a plugin POM at p
// belongs to; the caller decides whether c owes it.
func queuePlugin(ctx context.Context, env format.Env, c repo.Config, p string) error {
	if !IsPom(p) {
		return nil
	}
	g, ok := groupOf(p)
	if !ok {
		return nil
	}
	return enqueue(ctx, env, KindPlugins, c.Key, g)
}

func enqueue(ctx context.Context, env format.Env, kind, repoKey, name string) error {
	if err := env.Enqueue(ctx, kind, workKey(repoKey, name), nil); err != nil {
		return fmt.Errorf("maven: queue %s of %s: %w", kind, name, err)
	}
	return nil
}

// isPlugin reports a file whose properties mark it a plugin POM.
func isPlugin(props types.Properties) bool {
	return slices.Contains(props[PluginProperty], "true")
}

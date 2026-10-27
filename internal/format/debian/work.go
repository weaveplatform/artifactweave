package debian

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type aptFolder struct{ distribution, component, architecture string }

type aptCoordinates struct{ distribution, component string }

// Work rebuilds apt metadata from the committed item tree. It never builds a
// remote repository's index from a partial cache: remote metadata is fetched
// from the upstream with the source/cache policy instead.
func (Handler) Work(ctx context.Context, env format.Env, work types.WorkItem) error {
	if work.Kind != KindIndex && work.Kind != KindReindexNoProps {
		return nil
	}
	c, err := env.Store.Repos().Get(ctx, work.Key)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil
	case err != nil:
		return fmt.Errorf("debian: repository: %w", err)
	case !Serves(c.PackageType) || !derives(c):
		return nil
	}
	if work.Kind == KindReindexNoProps {
		return reindex(ctx, env, c, false)
	}
	return buildIndex(ctx, env, c)
}

func buildIndex(ctx context.Context, env format.Env, c repo.Config) error {
	items, err := files(ctx, env.Store, c)
	if err != nil {
		return err
	}
	old, groups, err := collectAptGroups(ctx, env, items)
	if err != nil {
		return err
	}
	sourceGroups, err := collectSourceGroups(ctx, env, items)
	if err != nil {
		return err
	}
	generated := map[string][]byte{}
	byDistribution := map[string][]ReleaseEntry{}
	components := map[string]map[string]bool{}
	distArches := map[string]map[string]bool{}
	for folder, entries := range groups {
		index, err := Packages(entries)
		if err != nil {
			return err
		}
		compressed, err := GzipIndex(index)
		if err != nil {
			return err
		}
		base := folder.component + "/binary-" + folder.architecture + "/Packages"
		for _, artifact := range []ReleaseEntry{{Path: base, Body: index}, {Path: base + ".gz", Body: compressed}} {
			p := "dists/" + folder.distribution + "/" + artifact.Path
			generated[p] = artifact.Body
			byDistribution[folder.distribution] = append(
				byDistribution[folder.distribution], artifact,
			)
			sum := sha256.Sum256(artifact.Body)
			hashPath := "dists/" + folder.distribution + "/" + folder.component +
				"/binary-" + folder.architecture + "/by-hash/SHA256/" + fmt.Sprintf("%x", sum)
			generated[hashPath] = artifact.Body
		}
		if components[folder.distribution] == nil {
			components[folder.distribution] = map[string]bool{}
			distArches[folder.distribution] = map[string]bool{}
		}
		components[folder.distribution][folder.component] = true
		distArches[folder.distribution][folder.architecture] = true
	}
	for folder, entries := range sourceGroups {
		index, err := Sources(entries)
		if err != nil {
			return err
		}
		compressed, err := GzipIndex(index)
		if err != nil {
			return err
		}
		base := folder.component + "/source/Sources"
		for _, artifact := range []ReleaseEntry{{Path: base, Body: index}, {Path: base + ".gz", Body: compressed}} {
			generated["dists/"+folder.distribution+"/"+artifact.Path] = artifact.Body
			byDistribution[folder.distribution] = append(
				byDistribution[folder.distribution],
				artifact,
			)
			sum := sha256.Sum256(artifact.Body)
			generated["dists/"+folder.distribution+"/"+folder.component+
				"/source/by-hash/SHA256/"+fmt.Sprintf("%x", sum)] = artifact.Body
		}
		if components[folder.distribution] == nil {
			components[folder.distribution] = map[string]bool{}
			distArches[folder.distribution] = map[string]bool{}
		}
		components[folder.distribution][folder.component] = true
		distArches[folder.distribution]["source"] = true
	}
	stamp := time.Now().UTC()
	if env.Now != nil {
		stamp = env.Now().UTC()
	}
	for distribution, entries := range byDistribution {
		body, err := Release(
			distribution,
			keys(components[distribution]),
			keys(distArches[distribution]),
			entries,
			stamp,
		)
		if err != nil {
			return err
		}
		base := "dists/" + distribution + "/"
		generated[base+"Release"] = body
		if c.PrimaryKeyPairRef != "" {
			pair, err := env.Store.Keys().Keypair(ctx, c.PrimaryKeyPairRef)
			if err != nil {
				return fmt.Errorf("debian: signing key: %w", err)
			}
			if pair.Type != types.KeypairGPG {
				return fmt.Errorf(
					"%w: repository %s needs a gpg signing pair",
					errInvalidBinary,
					c.Key,
				)
			}
			signed, err := SignRelease(body, pair.PrivateKey, pair.Passphrase)
			if err != nil {
				return err
			}
			generated[base+"InRelease"] = signed.InRelease
			generated[base+"Release.gpg"] = signed.ReleaseGPG
		}
	}
	paths := keys(generated)
	for _, p := range paths {
		if _, err := env.Derive(
			ctx,
			format.Derived{Repo: c, Path: p, Body: bytes.NewReader(generated[p]), By: "_system_"},
		); err != nil {
			return fmt.Errorf("debian: derive %s: %w", p, err)
		}
		delete(old, p)
	}
	for _, p := range keys(old) {
		if err := env.Underive(ctx, c, false, p); err != nil {
			return fmt.Errorf("debian: remove stale %s: %w", p, err)
		}
	}
	return nil
}

func collectSourceGroups(
	ctx context.Context, env format.Env, items []types.Item,
) (map[aptCoordinates][]IndexedSource, error) {
	byPath := make(map[string]types.Item, len(items))
	for _, it := range items {
		byPath[it.FullPath()] = it
	}
	groups := map[aptCoordinates][]IndexedSource{}
	for _, it := range items {
		if !isSourceDescriptor(it.FullPath()) {
			continue
		}
		source, err := sourceFor(ctx, env, it)
		if err != nil {
			var reject *format.RejectionError
			if errors.As(err, &reject) {
				continue // invalid legacy content cannot enter a Sources index
			}
			return nil, err
		}
		props, err := env.Store.Items().Properties(ctx, it.ID)
		if err != nil {
			return nil, fmt.Errorf("debian: source coordinates: %w", err)
		}
		if sourceCoordinates(props, source, false) != nil {
			continue // an unannotated source is stored but not published
		}
		entry := IndexedSource{
			Source: source, Path: it.FullPath(), Size: it.Size,
			Checksums: it.Actual,
		}
		complete := true
		for _, part := range source.Parts {
			archive, ok := byPath[path.Join(path.Dir(it.FullPath()), part.Name)]
			if !ok || archive.Folder || archive.Size != part.Size ||
				!strings.EqualFold(archive.Actual.MD5, part.MD5) ||
				!strings.EqualFold(archive.Actual.SHA256, part.SHA256) {
				complete = false
				break
			}
			entry.Archives = append(entry.Archives, SourceArchive{
				Name: part.Name, Size: archive.Size, Checksums: archive.Actual,
			})
		}
		if !complete {
			continue // incomplete or corrupt source packages are never indexed
		}
		for _, distribution := range props["dsc.distribution"] {
			for _, component := range props["dsc.component"] {
				key := aptCoordinates{distribution, component}
				groups[key] = append(groups[key], entry)
			}
		}
	}
	return groups, nil
}

func collectAptGroups(
	ctx context.Context, env format.Env, items []types.Item,
) (map[string]bool, map[aptFolder][]IndexedBinary, error) {
	old := map[string]bool{}
	groups := map[aptFolder][]IndexedBinary{}
	all := map[aptCoordinates][]IndexedBinary{}
	arches := map[aptCoordinates]map[string]bool{}
	for _, it := range items {
		p := it.FullPath()
		if isGenerated(p) {
			old[p] = true
			continue
		}
		if !isBinary(p) {
			continue
		}
		b, err := packageFor(ctx, env, it)
		if err != nil {
			return nil, nil, err
		}
		props, err := env.Store.Items().Properties(ctx, it.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("debian: package coordinates: %w", err)
		}
		if err := coordinates(props, b); err != nil {
			return nil, nil, fmt.Errorf("debian: %s: %w", p, err)
		}
		if len(props[PropDistribution]) == 0 {
			continue
		}
		entry := IndexedBinary{Binary: b, Path: p, Size: it.Size, Checksums: it.Actual}
		for _, distribution := range props[PropDistribution] {
			for _, component := range props[PropComponent] {
				key := aptCoordinates{distribution, component}
				if arches[key] == nil {
					arches[key] = map[string]bool{}
				}
				for _, architecture := range props[PropArchitecture] {
					if architecture == "all" {
						all[key] = append(all[key], entry)
					} else {
						arches[key][architecture] = true
						folder := aptFolder{distribution, component, architecture}
						groups[folder] = append(groups[folder], entry)
					}
				}
			}
		}
	}
	for key, entries := range all {
		arches[key]["all"] = true
		// A new repository indexes amd64 by default; an Architecture: all
		// package must be visible to a fresh apt client of that architecture.
		arches[key]["amd64"] = true
		for architecture := range arches[key] {
			folder := aptFolder{key.distribution, key.component, architecture}
			groups[folder] = append(groups[folder], entries...)
		}
	}
	return old, groups, nil
}

func keys[V any](items map[string]V) []string {
	out := make([]string, 0, len(items))
	for key := range items {
		out = append(out, key)
	}
	slices.Sort(out)
	return out
}

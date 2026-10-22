package remote

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// MappedFolderListing exposes only authorized canonical files. Folders are
// private upstream traversal cursors, never public paths or permission grants.
// Documents contains non-artifact control-document cursors. Consumers must
// validate their identities and read them under a derived canonical scope.
type MappedFolderListing struct {
	Files     []ListingEntry
	Folders   []string `json:"-"`
	Documents []string `json:"-"`
	checks    []func(context.Context) error
}

func (l MappedFolderListing) Revalidate(ctx context.Context) error {
	for _, check := range l.checks {
		if err := check(ctx); err != nil {
			return err
		}
	}
	return nil
}

type mappedListing struct {
	discovery *Discovery
	scope     DiscoveryScope
}

func (m mappedListing) validate(page listingPage) error {
	for _, entry := range page.entries {
		if entry.Folder {
			continue
		}
		if _, _, err := m.discovery.MapFile(entry.Path); err != nil {
			return err
		}
	}
	return nil
}

// ListMappedFolder uses one fixed canonical query scope for each bounded
// upstream page. It must never authorize an upstream lexical path, including
// subdirectories that the caller later traverses under that same query scope.
func ListMappedFolder(ctx context.Context, service *source.Service, q source.Request,
	discovery *Discovery, scope DiscoveryScope, upstreamFolder string, budget *ListingBudget,
) (MappedFolderListing, error) {
	var out MappedFolderListing
	if service == nil || discovery == nil || budget == nil {
		return out, source.ErrUnsupported
	}
	if !scope.Allows(q) {
		return out, source.ErrDenied
	}
	target, err := listingTarget(q, upstreamFolder)
	if err != nil {
		return out, err
	}
	mapping := &mappedListing{discovery: discovery, scope: scope}
	visited := map[string]bool{}
	seen := map[ListingEntry]bool{}
	for target != "" {
		if visited[target] || budget.pages >= 100 {
			return MappedFolderListing{}, listingError()
		}
		visited[target] = true
		budget.pages++
		page, result, err := readListing(ctx, service, q, upstreamFolder, target, mapping)
		if err != nil {
			return MappedFolderListing{}, err
		}
		budget.entries += page.count
		if budget.entries > 10000 {
			return MappedFolderListing{}, listingError()
		}
		out.checks = append(out.checks, result.Revalidate)
		for _, entry := range page.entries {
			if seen[entry] {
				continue
			}
			seen[entry] = true
			if entry.Folder {
				out.Folders = append(out.Folders, entry.Path)
				continue
			}
			canonical, ok, err := discovery.MapFile(entry.Path)
			if err != nil {
				return MappedFolderListing{}, err
			}
			if !ok {
				out.Documents = append(out.Documents, entry.Path)
				continue
			}
			if !scope.contains(canonical) || !mappedFileAllowed(q, canonical) {
				continue
			}
			if err = result.CheckRead(ctx, canonical); errors.Is(err, source.ErrDenied) {
				continue
			} else if err != nil {
				return MappedFolderListing{}, fmt.Errorf("mapped file authority: %w", err)
			}
			out.Files = append(out.Files, ListingEntry{Path: canonical})
			out.checks = append(
				out.checks,
				func(ctx context.Context) error { return result.CheckRead(ctx, canonical) },
			)
		}
		target = page.next
	}
	slices.SortFunc(
		out.Files,
		func(a, b ListingEntry) int { return strings.Compare(a.Path, b.Path) },
	)
	slices.Sort(out.Folders)
	slices.Sort(out.Documents)
	if err = out.Revalidate(ctx); err != nil {
		return MappedFolderListing{}, err
	}
	return out, nil
}

func mappedFileAllowed(q source.Request, canonical string) bool {
	return authz.PatternsOf(q.Repo).Accepts(canonical, false) &&
		q.Access.Allows(types.ResourceArtifact,
			authz.Repo{Key: q.Repo.Key, Class: string(q.Repo.Class)}, canonical, types.ActionRead)
}

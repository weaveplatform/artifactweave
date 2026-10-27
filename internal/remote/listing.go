package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const listingBytes = 64 << 20

// ListingEntry describes an upstream child, not a cached item. Listing never
// creates an item row or fetches the child's payload.
type ListingEntry struct {
	Path   string
	Folder bool
}

// ListingBudget bounds an entire browsing/search operation, including multiple
// folders. A zero value is ready to use; the caller must not share it concurrently.
type ListingBudget struct{ pages, entries int }

// FolderListing retains current authority for every page and visible child.
// Call Revalidate immediately before disclosing the result, including HEAD.
type FolderListing struct {
	Entries []ListingEntry
	checks  []func(context.Context) error
}

func (l FolderListing) Revalidate(ctx context.Context) error {
	for _, check := range l.checks {
		if err := check(ctx); err != nil {
			return err
		}
	}
	return nil
}

// ListFolder performs an explicit, nonrecursive source lookup. The entry point
// owns the documented listRemoteFolderItems policy; this source primitive does
// not infer an entry point from a caller-controlled parameter. CacheOnly is
// carried unchanged to every page.
func ListFolder(ctx context.Context, service *source.Service, q source.Request,
	folder string, budget *ListingBudget,
) (FolderListing, error) {
	var out FolderListing
	if service == nil || budget == nil {
		return out, source.ErrUnsupported
	}
	target, err := listingTarget(q, folder)
	if err != nil {
		return out, err
	}
	visited := map[string]bool{}
	seen := map[ListingEntry]bool{}
	for target != "" {
		if visited[target] || budget.pages >= 100 {
			return FolderListing{}, listingError()
		}
		visited[target] = true
		budget.pages++
		page, result, err := readListingPage(ctx, service, q, folder, target)
		if err != nil {
			return FolderListing{}, err
		}
		budget.entries += page.count
		if budget.entries > 10000 {
			return FolderListing{}, listingError()
		}
		out.checks = append(out.checks, result.Revalidate)
		for _, entry := range page.entries {
			if seen[entry] {
				continue
			}
			seen[entry] = true
			allows := q.Access.Allows
			if entry.Folder {
				allows = q.Access.AllowsFolder
			}
			if !authz.PatternsOf(q.Repo).Accepts(entry.Path, entry.Folder) ||
				!allows(
					types.ResourceArtifact,
					authz.Repo{Key: q.Repo.Key, Class: string(q.Repo.Class)},
					entry.Path,
					types.ActionRead,
				) {
				continue
			}
			check := result.CheckRead
			if entry.Folder {
				check = result.CheckFolder
			}
			if err = check(ctx, entry.Path); errors.Is(err, source.ErrDenied) {
				continue
			} else if err != nil {
				return FolderListing{}, err
			}
			out.Entries = append(out.Entries, entry)
			out.checks = append(
				out.checks,
				func(ctx context.Context) error { return check(ctx, entry.Path) },
			)
		}
		target = page.next
	}
	slices.SortFunc(
		out.Entries,
		func(a, b ListingEntry) int { return strings.Compare(a.Path, b.Path) },
	)
	if err = out.Revalidate(ctx); err != nil {
		return FolderListing{}, err
	}
	return out, nil
}

type listingPage struct {
	entries []ListingEntry
	next    string
	count   int
}

func listingTarget(q source.Request, folder string) (string, error) {
	if folder != "" {
		if _, _, _, err := types.SplitItemPath(
			folder,
		); err != nil ||
			folder != strings.Trim(folder, "/") {
			return "", source.ErrInvalidRequest
		}
	}
	if listingSmart(q) {
		peer, err := ParseSmartPeer(q.Repo)
		if err != nil {
			return "", err
		}
		return peer.StorageURL(folder, nil)
	}
	u, err := checkedURL(q.Repo.URL)
	if err != nil {
		return "", source.ErrInvalidRequest
	}
	u.Path, u.RawPath = strings.TrimRight(u.Path, "/")+"/"+folder, ""
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	return u.String(), nil
}

func readListingPage(ctx context.Context, service *source.Service, q source.Request,
	folder, target string,
) (listingPage, source.Result, error) {
	return readListing(ctx, service, q, folder, target, nil)
}

func readListing(ctx context.Context, service *source.Service, q source.Request,
	folder, target string, mapping *mappedListing,
) (listingPage, source.Result, error) {
	parse := parseHTMLListing
	if listingSmart(q) {
		peer, err := ParseSmartPeer(q.Repo)
		if err != nil {
			return listingPage{}, source.Result{}, err
		}
		parse = func(ctx context.Context, r io.Reader, _ string, folder string) (listingPage, error) {
			return parseSmartListing(ctx, r, peer.Repository, folder)
		}
	}
	q.Resource = source.Resource{
		Kind:                 source.ResourceKindMetadata,
		Method:               http.MethodGet,
		Path:                 path.Join(folder, ".remote-listing"),
		PermissionFolder:     true,
		PermissionPath:       folder,
		ExternalURL:          target,
		Variant:              "folder-listing-html-v1",
		ExpectedContentTypes: []string{"text/html", "application/xhtml+xml"},
		ValidateMetadata: func(ctx context.Context, body io.ReadSeeker, _ int64, response source.MetadataResponse) error {
			page, err := parse(ctx, body, response.BaseURL, folder)
			if errors.Is(err, source.ErrUnsupported) {
				return &source.HTTPError{Status: 501, Kind: source.FailureKindProtocol}
			}
			if err == nil && mapping != nil {
				err = mapping.validate(page)
			}
			return err
		},
	}
	if listingSmart(q) {
		q.Resource.RequireSmart = true
		q.Resource.Variant = "folder-listing-smart-v1"
		q.Resource.ExpectedContentTypes = []string{
			"application/json",
			"application/vnd.org.jfrog.artifactory.storage.folderinfo+json",
		}
	}
	read := service.Read
	if mapping != nil {
		resource, err := mapping.discovery.MetadataResource(
			mapping.scope,
			path.Join(folder, ".remote-listing"),
			q.Resource.Variant,
		)
		if err != nil {
			return listingPage{}, source.Result{}, err
		}
		q.Resource.Path, q.Resource.Variant = resource.Path, resource.Variant
		q.Resource.PermissionFolder, q.Resource.PermissionPath, q.Resource.PermissionRoot = resource.PermissionFolder, resource.PermissionPath, resource.PermissionRoot
		read = func(ctx context.Context, q source.Request) (source.Result, error) {
			return mapping.discovery.Read(ctx, service, q)
		}
	}
	result, err := read(ctx, q)
	if err != nil {
		return listingPage{}, result, fmt.Errorf("read upstream listing: %w", err)
	}
	if result.Stream == nil {
		return listingPage{}, result, source.ErrInvalidResult
	}
	defer result.Stream.Body.Close()
	page, err := parse(ctx, result.Stream.Body, result.Metadata.BaseURL, folder)
	return page, result, err
}

func listingSmart(q source.Request) bool {
	return q.Repo.ContentSynchronisation != nil && q.Repo.ContentSynchronisation.Enabled
}

func listingError() error { return &source.HTTPError{Status: 502, Kind: source.FailureKindIntegrity} }

func listingBase(raw string) (*url.URL, error) {
	u, err := checkedURL(raw)
	if err != nil {
		return nil, listingError()
	}
	return u, nil
}

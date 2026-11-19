package itemkit

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

func (s *Kit) getRemoteFolder(w http.ResponseWriter, r *http.Request,
	st Store, c repo.Config, cached bool, req ItemRequest, p kernel.Principal, acc Access,
) bool {
	if c.Class != repo.Remote || cached || !c.ListRemoteFolderItems.Bool() ||
		(req.Path != "" && !req.IsFolder) {
		return false
	}
	q := source.Request{
		Repo: c, Subject: accessapi.SubjectOf(p), Access: acc.Decider, Project: c.ProjectKey,
	}
	authority, err := s.remoteFolderAuthority(r.Context(), q, req.Path)
	if err != nil {
		httpkit.WriteErr(w, r, pkgroute.SourceError(err, p.Anonymous))
		return true
	}
	held := false
	entries := indexEntries{ownership: firstIndexKind}
	checks := []func(context.Context) error{authority.Revalidate}
	var stored []model.Item
	if c.StoreArtifactsLocally == nil || *c.StoreArtifactsLocally {
		stored, held, err = storedFolderChildren(r.Context(), st, c.Key, true, req.Path)
		if err != nil {
			s.remoteFolderCompositionError(w, r, err, p)
			return true
		}
	}
	listing, err := remote.ListFolder(r.Context(), s.RemoteSources, q, req.Path, &remote.ListingBudget{})
	if err != nil && !remoteListingUnavailable(r.Context(), c, err) {
		httpkit.WriteErr(w, r, pkgroute.SourceError(err, p.Anonymous))
		return true
	}
	if err == nil {
		if err = listing.Revalidate(r.Context()); err != nil {
			httpkit.WriteErr(w, r, pkgroute.SourceError(err, p.Anonymous))
			return true
		}
		held = held || listing.HasChildren
		checks = append(checks, listing.Revalidate)
	}
	patterns := repo.PatternsOf(c)
	for _, child := range stored {
		fullPath := child.FullPath()
		if !acc.Item(c, fullPath, child.Folder, accessapi.ActionRead) || !patterns.AcceptsRequest(fullPath, child.Folder) {
			continue
		}
		check := authority.CheckRead
		if child.Folder {
			check = authority.CheckFolder
		}
		if err = check(r.Context(), fullPath); err != nil {
			httpkit.WriteErr(w, r, pkgroute.SourceError(err, p.Anonymous))
			return true
		}
		checks = append(checks, func(ctx context.Context) error { return check(ctx, fullPath) })
		entries.add(fullPath, child.Folder)
	}
	for _, child := range listing.Entries {
		entries.add(child.Path, child.Folder)
	}
	// A parent can remain readable after one child loses Read. Repeat each
	// disclosed child's checks, not just the folder's authority. Failed origin
	// acquisition has no result fence; the independent authority still applies.
	for _, check := range checks {
		if err = check(r.Context()); err != nil {
			httpkit.WriteErr(w, r, pkgroute.SourceError(err, p.Anonymous))
			return true
		}
	}
	if !held {
		httpkit.WriteErr(w, r, apierror.New(http.StatusNotFound, "Could not find resource"))
		return true
	}
	writeIndex(w, r, path.Join(c.Key, req.Path), entries.entries())
	return true
}

func (s *Kit) remoteFolderAuthority(ctx context.Context, q source.Request, folder string) (*source.Authority, error) {
	if s.RemoteSources == nil {
		return nil, source.ErrUnsupported
	}
	q.Resource = source.Resource{
		Kind: source.ResourceKindMetadata, Method: http.MethodGet,
		Path: path.Join(folder, ".remote-listing"), NoCacheFile: true,
		PermissionFolder: true, PermissionPath: folder,
	}
	authority, err := s.RemoteSources.Authorize(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("authorize folder: %w", err)
	}
	return authority, nil
}

// remoteListingUnavailable admits only source acquisition misses and ordinary
// transport/status failures. Security, current authority, protocol/integrity,
// own-store and ended-context errors keep their existing source answers.
func remoteListingUnavailable(ctx context.Context, c repo.Config, err error) bool {
	if ctx.Err() != nil || errors.Is(err, kernel.ErrStore) || errors.Is(err, format.ErrStorageFault) {
		return false
	}
	var content *source.ContentOpenError
	if errors.As(err, &content) {
		return false
	}
	var failure *source.HTTPError
	if errors.As(err, &failure) {
		switch failure.Kind {
		case 0, source.FailureKindHTTP, source.FailureKindConnectivity, source.FailureKindTimeout:
			return failure.Status >= 400
		default:
			return false
		}
	}
	return errors.Is(err, kernel.ErrNotFound) || errors.Is(err, source.ErrAssumedOffline) ||
		c.Offline != nil && *c.Offline && errors.Is(err, source.ErrUnavailable)
}

func (s *Kit) remoteFolderCompositionError(w http.ResponseWriter, r *http.Request, err error, p kernel.Principal) {
	if ended := r.Context().Err(); ended != nil {
		httpkit.WriteErr(w, r, pkgroute.SourceError(ended, p.Anonymous))
		return
	}
	// BrowseNativeHtmlService.execute catches failures of its cache composition
	// as not-found. Keep the original store cause in one log, outside that body;
	// earlier Source/current-authority failures use the distinct mapping above.
	s.Log().ErrorContext(r.Context(), "list cached folder", "request_id", httpkit.RequestIDFrom(r.Context()), "error", err)
	httpkit.WriteErr(w, r, apierror.New(http.StatusNotFound, "Could not find resource"))
}

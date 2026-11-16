package itemkit

import (
	"net/http"
	"path"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

func (s *Kit) getRemoteFolder(w http.ResponseWriter, r *http.Request,
	c repo.Config, cached bool, req ItemRequest, p kernel.Principal, acc Access,
) bool {
	if c.Class != repo.Remote || cached || !c.ListRemoteFolderItems.Bool() ||
		(req.Path != "" && !req.IsFolder) {
		return false
	}
	listing, err := remote.ListFolder(r.Context(), s.RemoteSources, source.Request{
		Repo: c, Subject: accessapi.SubjectOf(p), Access: acc.Decider, Project: c.ProjectKey,
	}, req.Path, &remote.ListingBudget{})
	if err != nil {
		httpkit.WriteErr(w, r, pkgroute.SourceError(err, p.Anonymous))
		return true
	}
	entries := make([]indexEntry, 0, len(listing.Entries))
	for _, entry := range listing.Entries {
		entries = append(entries, indexEntry{name: path.Base(entry.Path), folder: entry.Folder})
	}
	if err = listing.Revalidate(r.Context()); err != nil {
		httpkit.WriteErr(w, r, pkgroute.SourceError(err, p.Anonymous))
		return true
	}
	writeIndex(w, r, path.Join(c.Key, req.Path), entries)
	return true
}

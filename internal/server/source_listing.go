package server

import (
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit"
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
	title := html.EscapeString(path.Join(c.Key, req.Path))
	var body strings.Builder
	fmt.Fprintf(
		&body,
		"<!DOCTYPE html>\n<html><head><title>Index of %s</title></head><body>\n<h1>Index of %s</h1>\n<pre>",
		title,
		title,
	)
	for _, entry := range listing.Entries {
		name := path.Base(entry.Path)
		href := url.PathEscape(name)
		if strings.Contains(href, ":") {
			href = "./" + href
		}
		if entry.Folder {
			href, name = href+"/", name+"/"
		}
		fmt.Fprintf(
			&body,
			"<a href=\"%s\">%s</a>\n",
			html.EscapeString(href),
			html.EscapeString(name),
		)
	}
	body.WriteString("</pre></body></html>\n")
	if err = listing.Revalidate(r.Context()); err != nil {
		httpkit.WriteErr(w, r, pkgroute.SourceError(err, p.Anonymous))
		return true
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, body.String())
	}
	return true
}

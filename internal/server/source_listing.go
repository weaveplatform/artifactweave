package server

import (
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/remote"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func (s artifactoryService) getRemoteFolder(w http.ResponseWriter, r *http.Request,
	c repo.Config, cached bool, req itemRequest, p auth.Principal, acc access,
) bool {
	if c.Class != repo.Remote || cached || !c.ListRemoteFolderItems.Bool() ||
		(req.path != "" && !req.isFolder) {
		return false
	}
	listing, err := remote.ListFolder(r.Context(), s.cfg.RemoteSources, source.Request{
		Repo: c, Subject: p.Subject(), Access: acc.d, Project: c.ProjectKey,
	}, req.path, &remote.ListingBudget{})
	if err != nil {
		writeErr(w, r, pkgroute.SourceError(err, p.Anonymous))
		return true
	}
	title := html.EscapeString(path.Join(c.Key, req.path))
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
		writeErr(w, r, pkgroute.SourceError(err, p.Anonymous))
		return true
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, body.String())
	}
	return true
}

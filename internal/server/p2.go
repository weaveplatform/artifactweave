package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/internal/format/maven"
	"github.com/weaveplatform/artifactweave/internal/format/p2"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var errP2VirtualCycle = errors.New("P2 virtual repository cycle")

// tryP2Item reserves the update-site wire paths before generic item routing.
// Remote reads still cross the common source policy and cache boundary.
func (s artifactoryService) tryP2Item(w http.ResponseWriter, r *http.Request,
	st store.Store, c repo.Config, cached bool, req itemRequest,
	p auth.Principal, acc access,
) bool {
	if c.PackageType != repo.P2 {
		return false
	}
	if req.isFolder || len(req.props) != 0 || r.URL.RawQuery != "" {
		writeErr(w, r, itemNotFound())
		return true
	}
	if c.Class == repo.Virtual {
		s.serveP2Virtual(w, r, st, c, req, p, acc)
		return true
	}
	if c.Class != repo.Remote || p2.Classify(req.path) == p2.Invalid {
		writeErr(w, r, itemNotFound())
		return true
	}
	if strings.HasPrefix(req.path, "__p2_ext/") || p2CompositeName(req.path) {
		s.serveP2CompositeChild(w, r, c, cached, req, p, acc)
		return true
	}
	if archiveURL, ok := p2ArchiveURL(c.URL); ok {
		s.serveP2Archive(w, r, c, cached, req, p, acc, archiveURL)
		return true
	}
	return false
}

func p2ArchiveURL(raw string) (string, bool) {
	archive, suffix, ok := strings.Cut(raw, "!/")
	if !ok || suffix != "" {
		return "", false
	}
	u, err := url.Parse(archive)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") ||
		u.Host == "" || u.User != nil || u.Fragment != "" ||
		!strings.HasSuffix(strings.ToLower(u.Path), ".zip") {
		return "", false
	}
	return archive, true
}

func (s artifactoryService) serveP2Archive(w http.ResponseWriter, r *http.Request,
	c repo.Config, cached bool, req itemRequest, p auth.Principal, acc access,
	archiveURL string,
) {
	if !acc.file(c, req.path, types.ActionRead) ||
		generic.CheckDownload(c, req.path, false) != nil {
		writeErr(w, r, denied(p))
		return
	}
	if s.cfg.RemoteSources == nil {
		writeErr(w, r, pkgroute.SourceError(source.ErrUnavailable, p.Anonymous))
		return
	}
	q := source.Request{
		Repo: c, Subject: p.Subject(), Access: acc.d,
		Project: c.ProjectKey, CacheOnly: cached,
		Resource: source.Resource{
			Kind: source.Artifact, Path: req.path, Method: http.MethodGet,
			ExternalURL: archiveURL, Variant: "p2-site-archive-v1",
			Validate: func(_ context.Context, src io.ReadSeeker, size int64) error {
				if size <= 0 || size > p2.MaxArtifact {
					return &source.HTTPError{
						Status: http.StatusBadGateway,
						Kind:   source.IntegrityFailure,
					}
				}
				body, err := io.ReadAll(io.LimitReader(src, p2.MaxArtifact+1))
				if err != nil || int64(len(body)) != size {
					return &source.HTTPError{
						Status: http.StatusBadGateway,
						Kind:   source.IntegrityFailure,
					}
				}
				if _, err := p2.ExtractArchive(body, req.path); err != nil {
					return &source.HTTPError{
						Status: http.StatusBadGateway,
						Kind:   source.IntegrityFailure,
					}
				}
				return nil
			},
		},
	}
	result, err := s.cfg.RemoteSources.Read(r.Context(), q)
	if err != nil {
		writeErr(w, r, pkgroute.SourceError(err, p.Anonymous))
		return
	}
	if !s.remoteDeliveryAllowed(w, r, result, p.Anonymous) {
		return
	}
	var site []byte
	switch {
	case result.Stream != nil:
		defer func() { _ = result.Stream.Body.Close() }()
		site, err = io.ReadAll(io.LimitReader(result.Stream.Body, p2.MaxArtifact+1))
	case result.Item != nil:
		var reader io.ReadCloser
		reader, _, err = s.cfg.Blobs.Open(r.Context(), result.Item.Actual.SHA256)
		if err == nil {
			defer func() { _ = reader.Close() }()
			site, err = io.ReadAll(io.LimitReader(reader, p2.MaxArtifact+1))
		}
	default:
		err = source.ErrInvalidResult
	}
	if err != nil || len(site) > p2.MaxArtifact {
		writeErr(w, r, pkgroute.SourceError(source.ErrInvalidResult, p.Anonymous))
		return
	}
	body, err := p2.ExtractArchive(site, req.path)
	if err != nil {
		writeErr(w, r, pkgroute.SourceError(source.ErrInvalidResult, p.Anonymous))
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "private, no-store")
	sw := &statusWriter{ResponseWriter: w}
	http.ServeContent(sw, r, path.Base(req.path), time.Time{}, bytes.NewReader(body))
	if sw.complete(r) && result.Used != nil {
		_ = result.Used(r.Context())
	}
}

func (s artifactoryService) serveP2Virtual(w http.ResponseWriter, r *http.Request,
	st store.Store, c repo.Config, req itemRequest, p auth.Principal, acc access,
) {
	if !acc.folder(c, req.path, types.ActionRead) ||
		!acc.file(c, req.path, types.ActionRead) ||
		generic.CheckDownload(c, req.path, false) != nil {
		writeErr(w, r, denied(p))
		return
	}
	members, err := p2Members(r.Context(), st, c, acc, map[string]bool{})
	if err != nil {
		writeErr(w, r, s.itemError(r.Context(), "P2 virtual members", err))
		return
	}
	if strings.HasPrefix(req.path, "__p2/") {
		parts := strings.SplitN(strings.TrimPrefix(req.path, "__p2/"), "/", 2)
		if len(parts) != 2 || p2.Classify(parts[1]) == p2.Invalid {
			writeErr(w, r, itemNotFound())
			return
		}
		i, parseErr := strconv.Atoi(parts[0])
		if parseErr != nil || i < 0 || i >= len(members) || strconv.Itoa(i) != parts[0] {
			writeErr(w, r, itemNotFound())
			return
		}
		member := members[i]
		if !acc.file(member, parts[1], types.ActionRead) ||
			generic.CheckDownload(member, parts[1], false) != nil {
			writeErr(w, r, denied(p))
			return
		}
		// The cloned request retains the authenticated principal. The ordinary
		// item route performs the member's source/cache and final permission
		// checks again; it never grants a virtual-only caller member bytes.
		clone := r.Clone(r.Context())
		uri := *r.URL
		uri.Path = "/artifactory/" + member.Key + "/" + parts[1]
		uri.RawPath = ""
		clone.URL = &uri
		s.getItem(w, clone)
		return
	}
	var body []byte
	switch req.path {
	case "p2.index":
		body = []byte(
			"version=1\nmetadata.repository.factory.order=compositeContent.xml,\\!\nartifact.repository.factory.order=compositeArtifacts.xml,\\!\n",
		)
	case "compositeContent.xml":
		body = p2Composite("metadata", members)
	case "compositeArtifacts.xml":
		body = p2Composite("artifact", members)
	default:
		writeErr(w, r, itemNotFound())
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	if req.path == "p2.index" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	} else {
		w.Header().Set("Content-Type", "application/xml")
	}
	http.ServeContent(w, r, req.path, time.Time{}, bytes.NewReader(body))
}

// p2Members keeps the virtual order and removes members a caller cannot
// traverse. Only a P2 remote or a local Maven/Generic update site can supply
// native metadata. A nested P2 virtual is permitted if its graph is acyclic.
func p2Members(ctx context.Context, st store.Store, c repo.Config, acc access,
	visited map[string]bool,
) ([]repo.Config, error) {
	if visited[c.Key] {
		return nil, errP2VirtualCycle
	}
	visited[c.Key] = true
	defer delete(visited, c.Key)
	out := make([]repo.Config, 0, len(c.Repositories))
	for _, key := range c.Repositories {
		m, err := st.Repos().Get(ctx, key)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("P2 member %s: %w", key, err)
		}
		if !acc.folder(m, "", types.ActionRead) {
			continue
		}
		switch {
		case m.Class == repo.Remote && m.PackageType == repo.P2:
		case m.Class == repo.Local && (m.PackageType == repo.Generic || maven.Serves(m.PackageType)):
		case m.Class == repo.Virtual && m.PackageType == repo.P2:
			if _, err := p2Members(ctx, st, m, acc, visited); err != nil {
				return nil, err
			}
		default:
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

func p2Composite(kind string, members []repo.Config) []byte {
	typ := "org.eclipse.equinox.internal.p2.metadata.repository.CompositeMetadataRepository"
	if kind == "artifact" {
		typ = "org.eclipse.equinox.internal.p2.artifact.repository.CompositeArtifactRepository"
	}
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	if kind == "artifact" {
		b.WriteString("<?compositeArtifactRepository version=\"1.0.0\"?>\n")
	} else {
		b.WriteString("<?compositeMetadataRepository version=\"1.0.0\"?>\n")
	}
	fmt.Fprintf(
		&b,
		"<repository name=\"artifactweave P2 virtual\" type=\"%s\" version=\"1.0.0\">\n",
		typ,
	)
	fmt.Fprintf(&b, "<children size=\"%d\">\n", len(members))
	for i := range members {
		fmt.Fprintf(&b, "<child location=\"__p2/%d\"/>\n", i)
	}
	b.WriteString("</children>\n</repository>\n")
	return []byte(b.String())
}

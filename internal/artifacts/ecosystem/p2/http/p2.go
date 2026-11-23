package p2http

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/maven"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/p2"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

// p2Items serves P2's virtual and composite repositories over the item
// kit.
type p2Items struct{ *itemkit.Kit }

// TryP2Item is the overlay (itemkit.Overlay) that reserves a P2
// repository's update-site wire paths before generic item routing: a
// virtual repository's p2.index and composite metadata over the members the
// caller may traverse, and each member's files under <member key>/; a
// remote's composite metadata and external children; and a remote site.zip!/
// archive's entries. A virtual repository's forceP2Authentication is not
// checked here: the item kit's ForceAuthentication answers a request without
// credentials to such a repository before it is routed. A folder, a path P2
// does not class or a repository that is neither remote nor virtual answers
// 404; a query and matrix parameters are taken as any file's GET takes them
// (#1350). Remote reads still cross the common source policy and cache
// boundary. A remote's composite children and archive entries miss, are
// refused and fail a matrix parameter as any file the download answers
// does (itemkit.Missed, #1599), and a path P2 does not class answers as a
// file the repository does not hold (itemkit.NotServed). It reports
// whether it answered; a repository of another type, and a remote's own
// update-site files, it leaves to the generic lookup.
func TryP2Item(
	k *itemkit.Kit, w http.ResponseWriter, r *http.Request, st itemkit.Store, c repo.Config,
	cached bool, req itemkit.ItemRequest, p kernel.Principal, acc itemkit.Access,
) bool {
	s := p2Items{k}
	if c.PackageType != repo.P2 {
		return false
	}
	if req.IsFolder {
		httpkit.WriteErr(w, r, itemkit.ItemNotFound())
		return true
	}
	if c.Class == repo.Virtual {
		s.serveP2Virtual(w, r, st, c, req, p, acc)
		return true
	}
	if c.Class == repo.Remote && p2RemoteChecksum(c, req.Path) {
		return false
	}
	if c.Class != repo.Remote || p2.Classify(req.Path) == p2.KindInvalid {
		httpkit.WriteErr(w, r, itemkit.NotServed(c, cached, req, p, acc))
		return true
	}
	if strings.HasPrefix(req.Path, "__p2_ext/") || p2CompositeName(req.Path) {
		s.serveP2CompositeChild(w, r, c, cached, req, p, acc)
		return true
	}
	if archiveURL, ok := p2ArchiveURL(c.URL); ok {
		s.serveP2Archive(w, r, c, cached, req, p, acc, archiveURL)
		return true
	}
	return false
}

// p2RemoteChecksum reports whether name, asked for on the P2 remote c, is
// the checksum request of an update-site file P2 classes, which the generic
// lookup answers from that file, as 7.161.26's
// DownloadServiceImpl.respondForChecksumRequest answers a checksum request
// on a remote of any type (#1495). A composite child's or a site archive's
// file is read through this overlay alone, so its checksum is not.
func p2RemoteChecksum(c repo.Config, name string) bool {
	file, _, ok := item.Sidecar(name)
	if !ok || p2.Classify(name) != p2.KindInvalid || p2.Classify(file) == p2.KindInvalid ||
		strings.HasPrefix(file, "__p2_ext/") || p2CompositeName(file) {
		return false
	}
	_, archive := p2ArchiveURL(c.URL)
	return !archive
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

func (s p2Items) serveP2Archive(w http.ResponseWriter, r *http.Request,
	c repo.Config, cached bool, req itemkit.ItemRequest, p kernel.Principal, acc itemkit.Access,
	archiveURL string,
) {
	if !acc.File(c, req.Path, accessapi.ActionRead) {
		httpkit.WriteErr(w, r, itemkit.Refused(c, req, p))
		return
	}
	// The remote's blackout and patterns refuse an archive entry as they
	// refuse any of its files: 404 with the policy's message, after the
	// permission check (DV-057), and before the archive is fetched.
	if err := generic.CheckDownload(c, req.Path, false); err != nil {
		httpkit.WriteErr(w, r,
			s.ItemError(r.Context(), "P2 archive download check", itemkit.Rejected(c, req, err)))
		return
	}
	if !s.p2CopyMatches(w, r, c, req) {
		return
	}
	// The missing service is this server's fault, not the upstream's, so it
	// answers 500 rather than SourceError's 502, as Alpine's and opkg's
	// remote reads do.
	if s.RemoteSources == nil {
		httpkit.WriteErr(
			w,
			r,
			s.InternalError(r.Context(), "P2 archive source", source.ErrUnavailable),
		)
		return
	}
	q := source.Request{
		Repo: c, Subject: accessapi.SubjectOf(p), Access: acc.Decider,
		Project: c.ProjectKey, CacheOnly: cached,
		Resource: source.Resource{
			Kind: source.ResourceKindArtifact, Path: req.Path, Method: http.MethodGet,
			ExternalURL: archiveURL, Variant: "p2-site-archive-v1",
			Validate: func(ctx context.Context, src io.ReadSeeker, size int64) error {
				if size <= 0 || size > p2.MaxArtifact {
					return &source.HTTPError{
						Status: http.StatusBadGateway,
						Kind:   source.FailureKindIntegrity,
					}
				}
				body, err := io.ReadAll(io.LimitReader(src, p2.MaxArtifact+1))
				if err != nil || int64(len(body)) != size {
					return &source.HTTPError{
						Status: http.StatusBadGateway,
						Kind:   source.FailureKindIntegrity,
					}
				}
				if _, err := p2.ExtractArchive(ctx, body, req.Path); err != nil {
					return &source.HTTPError{
						Status: http.StatusBadGateway,
						Kind:   source.FailureKindIntegrity,
					}
				}
				return nil
			},
		},
	}
	result, err := s.RemoteSources.Read(r.Context(), q)
	if err != nil {
		httpkit.WriteErr(w, r, pkgroute.SourceError(
			itemkit.ReadAnswer(c, cached, req, p, err), p.Anonymous))
		return
	}
	if !s.RemoteDeliveryAllowed(w, r, result, p.Anonymous) {
		return
	}
	// Either copy is the server's, the cached archive or the spool file of
	// a repository that keeps none, so a failed read of it is a storage
	// fault, 500; an archive that reads but is over its limit or not one
	// keeps the 502 (#1488).
	var site []byte
	switch {
	case result.Stream != nil:
		defer func() { _ = result.Stream.Body.Close() }()
		site, err = pkgroute.ReadServed(r.Context(), result.Stream.Body, p2.MaxArtifact,
			source.ErrInvalidResult)
	case result.Item != nil:
		var reader io.ReadCloser
		reader, _, err = s.Blobs.Open(r.Context(), result.Item.Actual.SHA256)
		if err == nil {
			defer func() { _ = reader.Close() }()
			site, err = pkgroute.ReadServed(r.Context(), reader, p2.MaxArtifact,
				source.ErrInvalidResult)
		}
	default:
		err = source.ErrInvalidResult
	}
	if errors.Is(err, format.ErrStorageFault) {
		httpkit.WriteErr(w, r, s.InternalError(r.Context(), "P2 archive", err))
		return
	}
	if err != nil || len(site) > p2.MaxArtifact {
		httpkit.WriteErr(w, r, pkgroute.SourceError(source.ErrInvalidResult, p.Anonymous))
		return
	}
	body, err := p2.ExtractArchive(r.Context(), site, req.Path)
	if err != nil {
		httpkit.WriteErr(w, r, pkgroute.SourceError(source.ErrInvalidResult, p.Anonymous))
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "private, no-store")
	sw := &itemkit.StatusWriter{ResponseWriter: w}
	itemkit.ServeContent(sw, r, path.Base(req.Path), time.Time{}, bytes.NewReader(body))
	if sw.Complete(r) && result.Used != nil {
		_ = result.Used(r.Context())
	}
}

func (s p2Items) serveP2Virtual(
	w http.ResponseWriter,
	r *http.Request,
	st itemkit.Store,
	c repo.Config,
	req itemkit.ItemRequest,
	p kernel.Principal,
	acc itemkit.Access,
) {
	members, err := p2Members(r.Context(), st, c, acc)
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(r.Context(), "P2 virtual members", err))
		return
	}
	// The virtual's own files are single names; any other path is a
	// member's, <member key>/<path>. 7.x serves an aggregated repository's
	// files under its key and the update site's sub-path, as in
	// p2-virtual/download.eclipse.org/releases/luna/p2.index (RTFACT-7631,
	// RTFACT-29198), so a member's path names the member, never its place
	// among the members the caller may read, and the patterns below see
	// the path a 7.x client asks for. A member the caller may not traverse,
	// or a blacked-out one, is not among members, so its key answers 404.
	key, name, memberPath := strings.Cut(req.Path, "/")
	i := slices.IndexFunc(members, func(m repo.Config) bool { return m.Key == key })
	listed := memberPath && i >= 0 && p2.Classify(name) != p2.KindInvalid
	// The read permission is asked before the download policy (DV-057). A
	// listed member's file is decided by that member, as the member that
	// serves a file decides it (decision 0008, R3), and a nested virtual
	// member decides its own in the clone below, by its own members; any
	// other path by the virtual repository's read gate, which asks its
	// members, since no permission target can name a virtual repository
	// (decision 0006).
	switch {
	case listed && members[i].Class != repo.Virtual:
		if !acc.File(members[i], name, accessapi.ActionRead) {
			httpkit.WriteErr(w, r, itemkit.VirtualReadRefused(c, req.Path, p))
			return
		}
	case !listed:
		if !s.VirtualGate(w, r, st, c, req.Path, p, acc, "P2 virtual members") {
			return
		}
	}
	// The virtual repository's own patterns refuse a path with "No
	// repository found to serve the request", as on the generic route
	// (VirtualRepoDownloadStrategy.getInfoFromSearchableRepositories in
	// 6.20). p2.index ends in .index, which the patterns never refuse.
	if err := generic.CheckDownload(c, req.Path, false); err != nil {
		httpkit.WriteErr(w, r, s.ItemError(r.Context(), "P2 virtual download check", err))
		return
	}
	if memberPath {
		if !listed {
			httpkit.WriteErr(w, r, itemkit.ItemNotFound())
			return
		}
		member := members[i]
		// A member whose patterns refuse the path is passed over, as a
		// virtual repository's search passes over it (processStandard in
		// 6.20), and no other member serves its paths, so the answer is the
		// virtual's not-found.
		if generic.CheckDownload(member, name, false) != nil {
			httpkit.WriteErr(w, r, itemkit.ItemNotFound())
			return
		}
		// The cloned request retains the authenticated principal. The ordinary
		// item route performs the member's source/cache and final permission
		// checks again; it never grants a virtual-only caller member bytes.
		// It keeps the query and the matrix parameters, after the path's
		// last segment, so the member matches and forwards them as its own
		// GET would (#1350), as 7.x's virtual download passes the request's
		// properties to each member it asks (processStandard, 7.161.26).
		// The clone goes to GetItem, past the router's
		// itemkit.ForceAuthentication, so a nested P2 virtual member's own
		// forceP2Authentication does not apply: 7.x's filter checks only the
		// repository the request's path names [I]. Its miss and refusal stay
		// the virtual's (itemkit.MemberRead).
		// A nested virtual member walks on with this one's visited set
		// (p2Outer), so a cycle back to it is skipped there too.
		ctx := itemkit.MemberRead(r.Context())
		if member.Class == repo.Virtual {
			ctx = context.WithValue(ctx, p2OuterKey{}, append(slices.Clip(p2Outer(ctx)), c.Key))
		}
		clone := r.Clone(ctx)
		uri := *r.URL
		uri.RawPath = "/artifactory/" + url.PathEscape(member.Key) + "/" +
			p2EscapePath(name) + pkgroute.UpstreamMatrix(r.URL.EscapedPath())
		uri.Path, _ = url.PathUnescape(uri.RawPath)
		clone.URL = &uri
		s.GetItem(w, clone)
		return
	}
	// The virtual repository's own files take a query and matrix
	// parameters as any other file's GET does (#1350): a query, such as
	// skipUpdateStats (RTFACT-25636, 7.21.3), changes nothing, and matrix
	// parameters "make resolution conditional on properties" (JFrog's
	// guide, Using Properties in Deployment and Resolution). These composed
	// files carry none [I], so only a mandatory key refuses them.
	if !req.Matrix().Matches(nil) {
		httpkit.WriteErr(w, r, itemkit.ItemNotFound())
		return
	}
	var body []byte
	switch req.Path {
	case "p2.index":
		body = []byte(
			"version=1\nmetadata.repository.factory.order=compositeContent.xml,\\!\nartifact.repository.factory.order=compositeArtifacts.xml,\\!\n",
		)
	case "compositeContent.xml", "compositeArtifacts.xml":
		children, err := p2Children(r.Context(), st, c, members)
		if err != nil {
			httpkit.WriteErr(w, r, s.ItemError(r.Context(), "P2 virtual children", err))
			return
		}
		kind := "metadata"
		if req.Path == "compositeArtifacts.xml" {
			kind = "artifact"
		}
		body = p2Composite(kind, children)
	default:
		httpkit.WriteErr(w, r, itemkit.ItemNotFound())
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	if req.Path == "p2.index" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	} else {
		w.Header().Set("Content-Type", "application/xml")
	}
	itemkit.ServeContent(w, r, req.Path, time.Time{}, bytes.NewReader(body))
}

// p2EscapePath escapes each segment of an item path for a URL path, so a
// segment's ";" or "%" stays in its name.
func p2EscapePath(p string) string {
	segments := strings.Split(p, "/")
	for i, seg := range segments {
		segments[i] = url.PathEscape(seg)
	}
	return strings.Join(segments, "/")
}

// p2CopyMatches reports whether the copy of req's path that the remote c's
// cache holds matches the request's matrix parameters, or that it holds
// none (itemkit.RemoteCopyMatches); otherwise it answers the download's
// mismatch, or 500 when the properties cannot be read, and reports false. An external composite
// child and an archive entry are fetched from a URL other than the
// remote's own, so their parameters are matched here and never passed on
// [I]; a composite at the remote's own URL passes them on as any remote
// file does (#1371).
func (s p2Items) p2CopyMatches(
	w http.ResponseWriter, r *http.Request, c repo.Config, req itemkit.ItemRequest,
) bool {
	matches, err := itemkit.RemoteCopyMatches(r.Context(), s.Store, c, req.Path, req)
	if err != nil {
		httpkit.WriteErr(w, r, s.InternalError(r.Context(), "P2 properties", err))
		return false
	}
	if !matches {
		httpkit.WriteErr(w, r, itemkit.Mismatched(c, req))
		return false
	}
	return true
}

// p2OuterKey is the request context key of the virtual repositories a P2
// virtual repository's member path passed through (p2Outer).
type p2OuterKey struct{}

// p2Outer is the keys of the virtual repositories whose member path led to
// this request, outermost first: a nested P2 virtual member's own
// composite leaves them out as the outer walk's visited set does, so a
// cycle back to one is skipped rather than followed (decision 0008, R2).
func p2Outer(ctx context.Context) []string {
	keys, _ := ctx.Value(p2OuterKey{}).([]string)
	return keys
}

// p2Repos is the store's repositories with the keys of skip missing, so a
// walk that meets one passes over it as a member no repository has.
type p2Repos struct {
	virtual.Repos
	skip []string
}

// Get returns the configuration of key, or kernel.ErrNotFound for a key
// of skip.
func (r p2Repos) Get(ctx context.Context, key string) (repo.Config, error) {
	if slices.Contains(r.skip, key) {
		return repo.Config{}, kernel.ErrNotFound
	}
	return r.Repos.Get(ctx, key) //nolint:wrapcheck // the store's own answer
}

// p2Members returns the composite children of the virtual repository c the
// caller may use, as configurations: each direct member, a nested P2
// virtual one as a single child that a P2 client reads through its own
// composite. 7.x deferred nested P2 virtual repositories (RTFACT-9481,
// Deferred) but its member list offers them, so the nested one stays one
// child [I]. The order and the visited set are the package handlers' walk
// (virtual.Members in package order): a direct member is listed at the
// place of the first real member it reaches, a real member belongs to the
// direct member the walk first reaches it through, and a virtual reached
// again, through a cycle or as its own member, adds nothing, where a cycle
// answered 500 (decision 0008, R1 and R2). A direct member that reaches no
// real member the caller may use is left out.
//
// A real member is usable when it can supply native metadata, a P2 remote
// or a local Maven or Generic update site, the caller may read it, and it
// is not blacked out: 7.x builds the composite from the members a caller
// can use (RTFACT-29198) and ignores a blacked-out repository "when trying
// to resolve artifacts" (local-repositories), so listing it would only
// send the client to files that all refuse. A member's patterns refuse its
// files, not its place in the composite. A nested virtual member of
// another type is never a child.
func p2Members(ctx context.Context, st itemkit.Store, c repo.Config, acc itemkit.Access,
) ([]repo.Config, error) {
	rs := p2Repos{Repos: st.Repos(), skip: p2Outer(ctx)}
	steps, err := virtual.Members(ctx, rs, c, virtual.Options{Order: virtual.Package})
	if err != nil {
		return nil, fmt.Errorf("p2 members of %s: %w", c.Key, err)
	}
	owner := map[string]repo.Config{}
	if err := virtual.Walk(ctx, rs, c, func(chain []repo.Config, m repo.Config) error {
		if _, ok := owner[m.Key]; ok || m.Class == repo.Virtual {
			return nil
		}
		owner[m.Key] = m
		if len(chain) > 1 {
			owner[m.Key] = chain[1]
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("p2 members of %s: %w", c.Key, err)
	}
	listed := map[string]bool{}
	var out []repo.Config
	for _, step := range steps {
		m, o := step.Repo, owner[step.Repo.Key]
		if listed[o.Key] || !p2Usable(m, acc) ||
			o.Class == repo.Virtual && o.PackageType != repo.P2 {
			continue
		}
		listed[o.Key] = true
		out = append(out, o)
	}
	return out, nil
}

// p2Usable reports whether the real member m can supply a P2 composite
// child the caller may use (p2Members).
func p2Usable(m repo.Config, acc itemkit.Access) bool {
	switch {
	case m.Class == repo.Remote && m.PackageType == repo.P2:
	case m.Class == repo.Local && (m.PackageType == repo.Generic || maven.Serves(m.PackageType)):
	default:
		return false
	}
	return acc.Folder(m, "", accessapi.ActionRead) && generic.CheckDownload(m, "", true) == nil
}

// p2Children returns the composite children of the virtual repository c
// for a caller to whom members, the members it may traverse, are listed, as
// locations relative to c. Without p2Urls each listed member is a child at
// its key. With them each entry is a child at the path 7.x serves its site
// under (p2.SiteChild), as long as the member that serves it is listed for
// the caller: that member is chosen among all configured members, so a
// site's path is the same for every caller. A member no entry names is
// left out [I], though its files stay readable under its key, and so is an
// entry no member serves, which a YAML-applied configuration may hold [I].
func p2Children(ctx context.Context, st itemkit.Store, c repo.Config, members []repo.Config,
) ([]string, error) {
	if len(c.P2URLs) == 0 {
		out := make([]string, 0, len(members))
		for _, m := range members {
			out = append(out, url.PathEscape(m.Key))
		}
		return out, nil
	}
	configured := make([]p2.Member, 0, len(c.Repositories))
	for _, key := range c.Repositories {
		m, err := st.Repos().Get(ctx, key)
		if errors.Is(err, kernel.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("p2 member %s: %w", key, err)
		}
		pm := p2.Member{Key: m.Key}
		if m.Class == repo.Remote && m.PackageType == repo.P2 {
			pm.URL = m.URL
		}
		configured = append(configured, pm)
	}
	var out []string
	for _, raw := range c.P2URLs {
		site, err := p2.ParseSiteURL(raw)
		if err != nil {
			continue
		}
		key, child, ok := p2.SiteChild(site, configured)
		if ok && !slices.Contains(out, child) &&
			slices.ContainsFunc(members, func(m repo.Config) bool { return m.Key == key }) {
			out = append(out, child)
		}
	}
	return out, nil
}

func p2Composite(kind string, children []string) []byte {
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
	fmt.Fprintf(&b, "<children size=\"%d\">\n", len(children))
	for _, child := range children {
		// The location is relative, so a client resolves it under the
		// virtual repository. A key is path-escaped and a site path holds
		// only URL path characters (p2.ParseSiteURL), but either may hold
		// & or ', which the attribute escapes.
		b.WriteString("<child location=\"")
		_ = xml.EscapeText(&b, []byte(child))
		b.WriteString("\"/>\n")
	}
	b.WriteString("</children>\n</repository>\n")
	return []byte(b.String())
}

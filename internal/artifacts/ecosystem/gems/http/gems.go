package gemshttp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	gemfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/gems"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

const maxPackages = 10000

type (
	// Options configures the RubyGems protocol. RemoteSources serves remote
	// repositories and fetches through them; nil leaves the remote class
	// without a source, so a remote repository answers 501.
	Options       struct{ RemoteSources *source.Service }
	virtualSource struct{}
)

func (virtualSource) Item(
	context.Context,
	pkgroute.Store,
	repo.Config,
	string,
) (pkgroute.Found, error) {
	return pkgroute.Found{}, source.ErrUnsupported
}

// Protocol returns the RubyGems and Bundler protocol for the router kit,
// under /artifactory/api/gems/{repoKey}: POST of a gem to api/v1/gems to
// push it and DELETE of api/v1/yank or api/v1/gems/yank to yank one, on
// local and federated repositories; and GET and HEAD of a gem
// (gems/<file>.gem), the compact index (names, versions and info/<name>),
// the Marshal indexes (specs.4.8.gz, latest_specs.4.8.gz,
// prerelease_specs.4.8.gz and quick/Marshal.4.8/<file>.gemspec.rz) and the
// api/v1 gem, versions and search JSON. Local and federated repositories
// are served from their stored gems, virtual ones from their members in
// the package handlers' walk without priorityResolution (NoPriority), and
// remote ones through o.RemoteSources when it is set; a class
// without a source answers 501. The virtual class's source supports no
// single-item lookup, because the protocol reads a virtual repository's
// members itself. {repoKey}/reindex and {repoKey}/updateIndex are
// reserved, so neither is read as a package path; the server serves
// JFrog's reindex operation beside the protocol.
func Protocol(o Options) pkgroute.Protocol {
	sources := map[repo.Class]pkgroute.Source{
		repo.Local:     pkgroute.Local{},
		repo.Federated: pkgroute.Local{},
		repo.Virtual:   virtualSource{},
	}
	if o.RemoteSources != nil {
		sources[repo.Remote] = pkgroute.Remote{Service: o.RemoteSources}
	}
	return pkgroute.Protocol{
		Name:         "gems",
		Types:        []repo.PackageType{repo.Gems},
		ErrorProfile: apierror.ProfileArtifactory,
		Sources:      sources,
		// JFrog's RubyGems page: "Priority resolution is currently not
		// supported for RubyGems virtual repositories".
		NoPriority: true,
		Reserved:   []string{"{repoKey}/reindex", "{repoKey}/updateIndex"},
		Routes: func(r chi.Router, k *pkgroute.Kit) {
			h := handler{k}
			r.Get("/*", h.read)
			r.Head("/*", h.read)
			r.Post("/api/v1/gems", h.push)
			r.Post("/api/v1/gems/", h.push)
			r.Delete("/api/v1/yank", h.yank)
			r.Delete("/api/v1/gems/yank", h.yank)
		},
	}
}

type handler struct{ k *pkgroute.Kit }

func (h handler) fail(w http.ResponseWriter, r *http.Request, err error) { h.k.Error(w, r, err) }

func bad(
	msg string,
) error {
	return apierror.New(http.StatusBadRequest, msg)
}

func missing() error { return apierror.New(http.StatusNotFound, "Could not find resource") }

func (h handler) push(w http.ResponseWriter, r *http.Request) {
	switch pkgroute.RequestFrom(r).Repo.Class {
	case repo.Remote:
		h.fail(
			w,
			r,
			apierror.New(
				http.StatusMethodNotAllowed,
				"RubyGems push requires a local or federated repository",
			),
		)
		return
	case repo.Virtual:
		// gem push through a virtual repository lands in its default
		// deployment repository and needs Write there alone: 6.12.0 fixed
		// a push that asked Deploy of every member (RTFACT-17787).
		target, err := h.deployTarget(r)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		r = target
	}
	if !h.k.RequireFolder(w, r, "gems", api.ActionWrite) {
		return
	}
	info, err := h.k.Upload(r, io.LimitReader(r.Body, gemfmt.MaxGemBytes+1))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if info.Size > gemfmt.MaxGemBytes {
		h.fail(w, r, bad("gem upload exceeds size limit"))
		return
	}
	rc, _, err := h.k.Blobs().Open(r.Context(), info.Digests.SHA256)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	p, err := gemfmt.Read(r.Context(), rc, info.Size, "")
	_ = rc.Close()
	if err != nil {
		h.fail(w, r, bad("invalid RubyGem: "+err.Error()))
		return
	}
	path := "gems/" + gemfmt.Filename(p)
	_, err = h.k.StoreNew(
		r,
		path,
		info,
		nil,
		kernel.Checksums{},
		func() error { return apierror.New(http.StatusConflict, "gem version already exists") },
	)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = fmt.Fprintf(w, "Successfully registered gem: %s (%s)\n", p.Name, p.Version)
}

func (h handler) yank(w http.ResponseWriter, r *http.Request) {
	if pkgroute.RequestFrom(r).Repo.Class != repo.Local &&
		pkgroute.RequestFrom(r).Repo.Class != repo.Federated {
		h.fail(
			w,
			r,
			apierror.New(
				http.StatusMethodNotAllowed,
				"yank requires a local or federated repository",
			),
		)
		return
	}
	if !h.k.RequireFolder(w, r, "gems", api.ActionDelete) {
		return
	}
	input, err := io.ReadAll(io.LimitReader(r.Body, 4097))
	if err != nil || len(input) > 4096 {
		h.fail(w, r, bad("invalid yank form"))
		return
	}
	form, err := url.ParseQuery(string(input))
	if err != nil {
		h.fail(w, r, bad("invalid yank form"))
		return
	}
	name, version, platform := form.Get("gem_name"), form.Get("version"), form.Get("platform")
	if name == "" {
		name = r.URL.Query().Get("gem_name")
	}
	if version == "" {
		version = r.URL.Query().Get("version")
	}
	if platform == "" {
		platform = r.URL.Query().Get("platform")
	}
	if platform == "" {
		platform = "ruby"
	}
	p := gemfmt.Package{Name: name, Version: version, Platform: platform}
	path := "gems/" + gemfmt.Filename(p)
	if !gemfmt.ValidPath(path) {
		h.fail(w, r, bad("invalid gem identity"))
		return
	}
	if err := h.k.Delete(r, path); err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	//nolint:gosec // Identity is validated and this is a text/plain response.
	_, _ = fmt.Fprintf(
		w,
		"Successfully deleted gem: %s (%s)\n",
		name,
		version,
	) //nolint:gosec // Name and version are validated, and the response is text/plain.
}

func (h handler) read(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(pkgroute.RequestFrom(r).Rest, "/")
	if path == "" {
		h.fail(w, r, missing())
		return
	}
	if r.URL.RawQuery != "" && !strings.HasPrefix(path, "api/v1/search") {
		h.fail(w, r, bad("unsupported RubyGems query"))
		return
	}
	if strings.HasPrefix(path, "gems/") {
		if !gemfmt.ValidPath(path) {
			h.fail(w, r, missing())
			return
		}
		h.artifact(w, r, path)
		return
	}
	if !metadataPath(path) {
		h.fail(w, r, missing())
		return
	}
	if !h.k.RequireRoot(w, r) {
		return
	}
	entries, err := h.entries(r, path)
	if err != nil {
		h.fail(w, r, pkgroute.SourceError(err, false))
		return
	}
	if path == "api/v1/search.json" {
		q := strings.TrimSpace(r.URL.Query().Get("query"))
		if q == "" || len(q) > 128 {
			h.fail(w, r, bad("invalid gem search query"))
			return
		}
		var found []map[string]any
		for _, row := range entries {
			if strings.Contains(strings.ToLower(row.Name), strings.ToLower(q)) {
				found = append(
					found,
					map[string]any{
						"name":     row.Name,
						"version":  row.Version,
						"platform": row.Platform,
						"sha":      row.SHA256,
						"info":     row.Description,
						"authors":  strings.Join(row.Authors, ", "),
					},
				)
			}
		}
		if found == nil {
			found = []map[string]any{}
		}
		body, _ := json.Marshal(found)
		serveMetadata(w, r, path, "application/json", body)
		return
	}
	body, media, err := render(path, entries)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if storedDocument(path) {
		serveDocument(w, r, path, media, body)
		return
	}
	serveMetadata(w, r, path, media, body)
}

func metadataPath(path string) bool {
	return specIndex(path) || compactIndex(path) ||
		strings.HasPrefix(path, "api/v1/gems/") && strings.HasSuffix(path, ".json") ||
		strings.HasPrefix(path, "api/v1/versions/") ||
		path == "api/v1/search.json"
}

// specIndex reports the spec index files and quick gemspecs, which 7.x
// stores in the repository and reindex re-creates (JFrog's RubyGems docs).
func specIndex(path string) bool {
	return path == "specs.4.8.gz" || path == "latest_specs.4.8.gz" ||
		path == "prerelease_specs.4.8.gz" ||
		strings.HasPrefix(path, "quick/Marshal.4.8/") && strings.Count(path, "/") == 2 &&
			strings.HasSuffix(path, ".gemspec.rz")
}

// compactIndex reports Bundler's compact index.
func compactIndex(path string) bool {
	return path == "names" || path == "versions" ||
		strings.HasPrefix(path, "info/") && strings.Count(path, "/") == 1
}

// entries returns the gems the index at path lists for the request's
// repository, after the download policy refuses what it refuses before any
// gem is read:
//   - A spec index file or quick gemspec is a file 7.x reads through its
//     download path (RTFACT-27283), so the blackout and a pattern on path,
//     a virtual repository's own included, refuse it, and a virtual
//     repository leaves out a member that would.
//   - The compact index is resolution metadata whose 7.x storage path is
//     not documented, so the blackout alone refuses it, naming the
//     request's path [I] (#216).
//   - The JSON API and search keep a blacked-out repository's answers, as
//     6.20's searchers do not check the blackout (decision 0032) [I].
//
// Every virtual merge leaves out a blacked-out member, local or remote,
// which 7.x ignores when it resolves. A pattern on one gem's file leaves
// the gem listed (RTFACT-25571). A remote's own index reads check only its
// blackout, in its source.
func (h handler) entries(r *http.Request, path string) ([]gemfmt.Release, error) {
	class := pkgroute.RequestFrom(r).Repo.Class
	if class == repo.Remote {
		return h.remoteEntries(r)
	}
	document := ""
	switch {
	case specIndex(path):
		document = path
	case compactIndex(path):
		if err := h.k.CheckBlackedOut(r, path); err != nil {
			return nil, err //nolint:wrapcheck // the kit's own answer
		}
	case class != repo.Virtual:
		return h.local(r)
	}
	if class != repo.Virtual {
		if err := h.k.CheckDocument(r, document); err != nil {
			return nil, err //nolint:wrapcheck // the kit's own answer
		}
		return h.local(r)
	}
	parts, err := pkgroute.Collect(h.k, r, document, h.member)
	if err != nil {
		return nil, err //nolint:wrapcheck // the kit names the member and its fault
	}
	return gemfmt.Merger{}.Merge(r.Context(), parts) //nolint:wrapcheck // a merge never fails
}

// member reads the gems one member of a virtual repository lists, through
// its member request: a remote member's from its upstream's compact index,
// a local or federated member's from its stored gems, each filtered by the
// caller's Read in that member. The virtual repository's own Read is never
// asked: no permission target can name one, and 7.x decides a read through
// it by the member that serves it (decision 0008, R3).
//
// A member that fails is passed over, with a warning (WarnPassedOver), when
// its fault is one pkgroute.PassedOver passes over: an upstream's 5xx or
// one it cannot reach [I].
func (h handler) member(mr *http.Request) ([]gemfmt.Release, error) {
	read := h.local
	if pkgroute.RequestFrom(mr).Repo.Class == repo.Remote {
		read = h.remoteEntries
	}
	out, err := read(mr)
	h.k.WarnPassedOver(mr, err)
	return out, err
}

// deployTarget is the request a push through the virtual repository of r
// writes with: a member request for its default deployment repository, a
// local or federated member (virtual.DeployTarget), on whose path the push
// then checks Write. Without one 7.x answers 405 with Allow: GET before it
// checks any permission (decision 0008, R6; UploadServiceUtils.
// getTargetRepository 248-261, 7.161.26). One of a type the virtual
// repository does not take, which only data saved before the save checks
// can name (R11), is refused as a member request is.
func (h handler) deployTarget(r *http.Request) (*http.Request, error) {
	v := pkgroute.RequestFrom(r).Repo
	target, err := virtual.DeployTarget(r.Context(), h.k.DB().Repos(), v)
	switch {
	case errors.Is(err, virtual.ErrNoDeployTarget):
		return nil, itemkit.NoDeployTarget(v.Key) //nolint:wrapcheck // the API answer itself
	case err != nil:
		return nil, fmt.Errorf("gems: %w", err)
	}
	return h.k.MemberRequest(r, target) //nolint:wrapcheck // the kit's own answer
}

// serveMetadata answers a JSON API document, which 7.x computes per
// request rather than storing (storedDocument).
func serveMetadata(w http.ResponseWriter, r *http.Request, path, media string, b []byte) {
	sum := sha256.Sum256(b)
	etag := gemfmt.InfoMD5(b)
	w.Header().Set("Content-Type", media)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("ETag", "\""+etag+"\"")
	w.Header().Set("Repr-Digest", "sha-256=\""+base64.StdEncoding.EncodeToString(sum[:])+"\"")
	itemkit.ServeContent(w, r, path, time.Time{}, bytes.NewReader(b))
}

func render(path string, rows []gemfmt.Release) ([]byte, string, error) {
	packages := make([]gemfmt.Package, 0, len(rows))
	for _, row := range rows {
		packages = append(packages, row.Package)
	}
	switch path {
	case "names":
		return gemfmt.CompactNames(packages), documentType(path), nil
	case "versions":
		return gemfmt.CompactVersions(packages), documentType(path), nil
	case "specs.4.8.gz", "latest_specs.4.8.gz", "prerelease_specs.4.8.gz":
		b, err := gemfmt.Specs(
			packages,
			path == "latest_specs.4.8.gz",
			path == "prerelease_specs.4.8.gz",
		)
		if err != nil {
			return nil, "", fmt.Errorf("gems: render specs: %w", err)
		}
		return b, documentType(path), nil
	}
	if name, ok := strings.CutPrefix(path, "info/"); ok {
		if !gemfmt.ValidName(name) {
			return nil, "", missing()
		}
		return gemfmt.CompactInfo(name, packages), documentType(path), nil
	}
	if file, ok := strings.CutPrefix(path, "quick/Marshal.4.8/"); ok {
		file = strings.TrimSuffix(file, ".gemspec.rz")
		for _, row := range rows {
			if strings.TrimSuffix(gemfmt.Filename(row.Package), ".gem") == file {
				b, err := gemfmt.QuickSpec(row.Package, row.Created)
				if err != nil {
					return nil, "", fmt.Errorf("gems: render gemspec: %w", err)
				}
				return b, documentType(path), nil
			}
		}
		return nil, "", missing()
	}
	if name, ok := strings.CutPrefix(path, "api/v1/gems/"); ok {
		name = strings.TrimSuffix(name, ".json")
		if !gemfmt.ValidName(name) {
			return nil, "", missing()
		}
		var latest *gemfmt.Package
		for i := range packages {
			if packages[i].Name == name &&
				(latest == nil || gemfmt.CompareVersions(packages[i].Version, latest.Version) > 0) {
				latest = &packages[i]
			}
		}
		if latest == nil {
			return nil, "", missing()
		}
		b, _ := json.Marshal(
			map[string]any{
				"name":     latest.Name,
				"version":  latest.Version,
				"platform": latest.Platform,
				"sha":      latest.SHA256,
				"info":     latest.Description,
				"authors":  strings.Join(latest.Authors, ", "),
			},
		)
		return b, "application/json", nil
	}
	if name, ok := strings.CutPrefix(path, "api/v1/versions/"); ok {
		name = strings.TrimSuffix(name, ".json")
		if !gemfmt.ValidName(name) {
			return nil, "", missing()
		}
		var list []map[string]any
		for _, p := range packages {
			if p.Name == name {
				list = append(
					list,
					map[string]any{
						"number":     p.Version,
						"platform":   p.Platform,
						"sha":        p.SHA256,
						"prerelease": false,
					},
				)
			}
		}
		if len(list) == 0 {
			return nil, "", missing()
		}
		b, _ := json.Marshal(list)
		return b, "application/json", nil
	}
	return nil, "", missing()
}

func (h handler) local(r *http.Request) ([]gemfmt.Release, error) {
	c := pkgroute.RequestFrom(r).Repo
	items, err := gemfmt.Files(r.Context(), h.k.DB(), c)
	if err != nil {
		return nil, fmt.Errorf("gems: list local items: %w", err)
	}
	if len(items) > maxPackages {
		return nil, source.ErrInvalidResult
	}
	var out []gemfmt.Release
	for _, it := range items {
		if !gemfmt.ValidPath(it.FullPath()) || !h.k.Can(r, it.FullPath(), api.ActionRead) {
			continue
		}
		rc, info, err := h.k.Blobs().Open(r.Context(), it.Actual.SHA256)
		if err != nil {
			return nil, fmt.Errorf("gems: open local archive: %w", err)
		}
		p, err := gemfmt.Read(r.Context(), rc, info.Size, it.FullPath())
		_ = rc.Close()
		if err != nil {
			return nil, fmt.Errorf("stored gem %s: %w", it.FullPath(), err)
		}
		out = append(out, gemfmt.Release{Package: p, Created: it.Created})
	}
	return out, nil
}

func (h handler) artifact(w http.ResponseWriter, r *http.Request, path string) {
	switch pkgroute.RequestFrom(r).Repo.Class {
	case repo.Virtual:
		h.virtualArtifact(w, r, path)
	case repo.Remote:
		if h.k.Require(w, r, path, api.ActionRead) {
			h.remoteArtifact(w, r, path)
		}
	default:
		if h.k.Require(w, r, path, api.ActionRead) {
			h.k.ServeItem(w, r, path)
		}
	}
}

// virtualArtifact serves path from the first member, in the package
// handlers' walk (MergeMembers, without priorityResolution: Protocol's
// NoPriority), that holds the gem and lets the caller read it. The walk
// flattens nested virtual repositories, skips a cycle and passes over a
// member that is blacked out, whose patterns reject the gem or that the
// caller may read nothing in; the virtual repository's own patterns refuse
// the gem before any member is tried. A member without the gem, one whose
// Read refuses it and one whose upstream fails are passed over as well
// (pkgroute.PassedOver), the last with a warning [I], so the answer is the
// virtual repository's own 404, which names no member's setting (decision
// 0008, R3).
func (h handler) virtualArtifact(w http.ResponseWriter, r *http.Request, path string) {
	members, err := h.k.MergeMembers(r, path)
	if err != nil {
		h.fail(w, r, pkgroute.SourceError(err, false))
		return
	}
	for _, mr := range members {
		if !h.k.Can(mr, path, api.ActionRead) {
			continue
		}
		if pkgroute.RequestFrom(mr).Repo.Class == repo.Remote {
			result, err := h.remoteRead(mr, path)
			if pkgroute.PassedOver(err) {
				h.k.WarnPassedOver(mr, err)
				continue
			}
			if err != nil {
				h.fail(w, r, pkgroute.SourceError(err, false))
				return
			}
			h.k.ServeResult(w, mr, result)
			return
		}
		f, err := h.k.DownloadItem(mr, path)
		if pkgroute.PassedOver(err) {
			h.k.WarnPassedOver(mr, err)
			continue
		}
		if err != nil {
			h.fail(w, r, err)
			return
		}
		h.k.ServeFound(w, mr, f)
		return
	}
	h.fail(w, r, missing())
}

func (h handler) remoteEntries(r *http.Request) ([]gemfmt.Release, error) {
	if !h.k.Can(r, "", api.ActionRead) {
		return nil, source.ErrDenied
	}
	versions, err := h.remoteBytes(r, "versions")
	if err != nil {
		return nil, err
	}
	names, err := gemfmt.ParseCompactVersions(versions)
	if err != nil {
		return nil, source.ErrInvalidResult
	}
	if len(names) > maxPackages {
		return nil, source.ErrInvalidResult
	}
	keys := make([]string, 0, len(names))
	for name := range names {
		keys = append(keys, name)
	}
	slices.Sort(keys)
	var out []gemfmt.Release
	for _, name := range keys {
		body, err := h.remoteBytes(r, "info/"+name)
		if err != nil {
			return nil, err
		}
		if names[name].MD5 != gemfmt.InfoMD5(body) {
			return nil, source.ErrInvalidResult
		}
		rows, err := gemfmt.ParseCompactInfo(name, body)
		if err != nil {
			return nil, source.ErrInvalidResult
		}
		for _, p := range rows {
			if names[name].Versions[gemfmt.VersionToken(p)] &&
				h.k.Can(r, p.Path, api.ActionRead) {
				out = append(out, gemfmt.Release{Package: p})
			}
		}
	}
	return out, nil
}

func (h handler) remotePackage(r *http.Request, path string) (gemfmt.Package, error) {
	if !h.k.Can(r, "", api.ActionRead) {
		return gemfmt.Package{}, source.ErrDenied
	}
	versions, err := h.remoteBytes(r, "versions")
	if err != nil {
		return gemfmt.Package{}, err
	}
	names, err := gemfmt.ParseCompactVersions(versions)
	if err != nil {
		return gemfmt.Package{}, source.ErrInvalidResult
	}
	name := ""
	for candidate := range names {
		if strings.HasPrefix(path, "gems/"+candidate+"-") && len(candidate) > len(name) {
			name = candidate
		}
	}
	if name == "" {
		return gemfmt.Package{}, kernel.ErrNotFound
	}
	body, err := h.remoteBytes(r, "info/"+name)
	if err != nil {
		return gemfmt.Package{}, err
	}
	if names[name].MD5 != gemfmt.InfoMD5(body) {
		return gemfmt.Package{}, source.ErrInvalidResult
	}
	packages, err := gemfmt.ParseCompactInfo(name, body)
	if err != nil {
		return gemfmt.Package{}, source.ErrInvalidResult
	}
	for _, p := range packages {
		if p.Path == path && names[name].Versions[gemfmt.VersionToken(p)] &&
			h.k.Can(r, path, api.ActionRead) {
			return p, nil
		}
	}
	return gemfmt.Package{}, kernel.ErrNotFound
}

func (h handler) remoteBytes(r *http.Request, path string) ([]byte, error) {
	result, err := h.remoteResult(r, path)
	if err != nil {
		return nil, err
	}
	if result.Revalidate != nil {
		if err := result.Revalidate(r.Context()); err != nil {
			return nil, fmt.Errorf("gems: revalidate remote metadata: %w", err)
		}
	}
	var body io.ReadCloser
	switch {
	case result.Stream != nil:
		body = result.Stream.Body
	case result.Item != nil:
		body, _, err = h.k.Blobs().Open(r.Context(), result.Item.Actual.SHA256)
		if err != nil {
			return nil, fmt.Errorf("gems: open cached metadata: %w", err)
		}
	default:
		return nil, source.ErrInvalidResult
	}
	defer body.Close()
	b, err := io.ReadAll(io.LimitReader(body, gemfmt.MaxIndexBytes+1))
	if err != nil || len(b) > gemfmt.MaxIndexBytes {
		return nil, source.ErrInvalidResult
	}
	if result.Used != nil {
		_ = result.Used(r.Context())
	}
	return b, nil
}

func (h handler) remoteResult(r *http.Request, path string) (source.Result, error) {
	resource := source.Resource{
		Kind:             source.ResourceKindMetadata,
		Path:             path,
		Method:           http.MethodGet,
		PermissionFolder: true,
		Variant:          "gems-metadata-v1",
		ValidateMetadata: func(ctx context.Context, body io.ReadSeeker, size int64, _ source.MetadataResponse) error {
			if size <= 0 || size > gemfmt.MaxIndexBytes {
				return source.ErrInvalidResult
			}
			b, err := io.ReadAll(io.LimitReader(body, gemfmt.MaxIndexBytes+1))
			if err != nil || int64(len(b)) != size {
				return source.ErrInvalidResult
			}
			switch {
			case path == "versions":
				_, err = gemfmt.ParseCompactVersions(b)
			case strings.HasPrefix(path, "info/"):
				_, err = gemfmt.ParseCompactInfo(strings.TrimPrefix(path, "info/"), b)
			default:
				err = source.ErrInvalidResult
			}
			if err != nil {
				return source.ErrInvalidResult
			}
			return nil
		},
	}
	result, err := h.k.Read(r, resource)
	if err != nil {
		return source.Result{}, fmt.Errorf("gems: read remote metadata: %w", err)
	}
	return result, nil
}

func (h handler) remoteArtifact(w http.ResponseWriter, r *http.Request, path string) {
	result, err := h.remoteRead(r, path)
	if err != nil {
		h.fail(w, r, pkgroute.SourceError(err, false))
		return
	}
	h.k.ServeResult(w, r, result)
}

func (h handler) remoteRead(r *http.Request, path string) (source.Result, error) {
	// The compact index checksum binds the remote gem body to the metadata
	// used for resolution. A missing version never authorizes a cold fetch.
	p, err := h.remotePackage(r, path)
	if err != nil {
		return source.Result{}, err
	}
	expected := p.SHA256
	resource := pkgroute.Resource(r, path, "")
	resource.Variant = "gems-package-v1"
	resource.Validate = func(ctx context.Context, body io.ReadSeeker, size int64) error {
		p, err := gemfmt.Read(ctx, body, size, path)
		if err != nil || p.SHA256 != expected {
			return source.ErrInvalidResult
		}
		return nil
	}
	result, err := h.k.Read(r, resource)
	if err != nil {
		return source.Result{}, fmt.Errorf("gems: read remote gem: %w", err)
	}
	return result, nil
}

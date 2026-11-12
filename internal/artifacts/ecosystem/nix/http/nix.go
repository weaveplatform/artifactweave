package nixhttp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	nixfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/nix"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/keymaterial"
)

// Options configures the Nix binary-cache protocol. Its access contracts
// read through the request's context, so a server binds them over the store
// the router kit serves.
type Options struct {
	// RemoteSources serves remote repositories; nil answers them 501.
	RemoteSources *source.Service
	// Authorizer re-evaluates the caller's permissions against current
	// state just before a hosted or virtual repository's narinfo or archive
	// is served. Serving those requires it.
	Authorizer accessapi.Authorizer
	// KeyPairs reads the Ed25519 key pairs a repository names: a hosted
	// repository signs the narinfo it serves with its primary pair, and a
	// remote repository verifies upstream signatures against its pairs. A
	// repository that names a key pair requires it.
	KeyPairs accessapi.Keys
	// Channels is the NixOS Channel Registry a remote repository resolves
	// an official channel name against, and Releases the base the registry
	// redirects a channel to, whose tarball the remote caches. Nil means
	// https://channels.nixos.org and https://releases.nixos.org; tests
	// point both at a fixture.
	Channels, Releases *url.URL
}

// Protocol mounts the Nix binary-cache and channel routes for local,
// federated and virtual repositories, and for remote ones when
// o.RemoteSources is set.
func Protocol(o Options) pkgroute.Protocol {
	sources := map[repo.Class]pkgroute.Source{
		repo.Local:     pkgroute.Local{},
		repo.Federated: pkgroute.Local{},
		repo.Virtual:   pkgroute.Local{},
	}
	if o.RemoteSources != nil {
		sources[repo.Remote] = pkgroute.Remote{Service: o.RemoteSources}
	}
	if o.Channels == nil {
		o.Channels = &url.URL{Scheme: "https", Host: "channels.nixos.org"}
	}
	if o.Releases == nil {
		o.Releases = &url.URL{Scheme: "https", Host: "releases.nixos.org"}
	}
	return pkgroute.Protocol{
		Name:         "nix",
		Types:        []repo.PackageType{repo.Nix},
		ErrorProfile: apierror.ProfileArtifactory,
		Sources:      sources,
		Routes: func(r chi.Router, k *pkgroute.Kit) {
			h := handler{k: k, o: o}
			r.Get("/*", h.read)
			r.Head("/*", h.read)
			r.Put("/*", h.put)
			r.Delete("/*", h.remove)
			r.Post("/reindex", h.reindex)
		},
	}
}

type (
	handler struct {
		k *pkgroute.Kit
		o Options
	}
	located struct {
		info    nixfmt.Info
		archive *model.Item
		r       *http.Request
		check   func(context.Context) error
	}
)

func (h handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	p, _ := kernel.FromContext(r.Context())
	h.k.Error(w, r, pkgroute.SourceError(err, p.Anonymous || p.Name == ""))
}

func (h handler) env() format.Env {
	db := h.k.DB()
	env := format.Env{Store: db, Blobs: h.k.Blobs(), Now: time.Now}
	// A router built without a store leaves both nil rather than panicking.
	if db != nil {
		env.Leases, env.WorkQueue = db.Leases(), db.WorkQueue()
	}
	return env
}

func requestPath(r *http.Request) (string, error) {
	p, err := pkgroute.ParsePath(pkgroute.RequestFrom(r).Rest)
	if err != nil {
		return "", fmt.Errorf("nix: request path: %w", err)
	}
	if r.URL.RawQuery != "" {
		return "", apierror.New(400, "Nix requests do not accept query parameters")
	}
	return p.String(), nil
}

func (h handler) read(w http.ResponseWriter, r *http.Request) {
	path, err := requestPath(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if path == "channels" || strings.HasPrefix(path, "channels/") {
		h.channels(w, r, path)
		return
	}
	if path == "nix-cache-info" {
		// Read permission comes before the download policy (DV-057), so a
		// caller who may not read gets 403 or 401, not the policy's 404.
		if !h.k.Require(w, r, path, accessapi.ActionRead) {
			return
		}
		if err := generic.CheckDownload(pkgroute.RequestFrom(r).Repo, path, false); err != nil {
			h.fail(w, r, err)
			return
		}
		write(w, r, []byte(nixfmt.CacheInfo), "text/x-nix-cache-info")
		return
	}
	if nixfmt.IsInfoPath(path) {
		found, err := h.lookup(r, path[:32], "")
		if err != nil {
			h.fail(w, r, err)
			return
		}
		if err = h.sign(found.r, &found.info); err != nil {
			h.fail(w, r, err)
			return
		}
		if err = found.check(r.Context()); err != nil {
			h.fail(w, r, err)
			return
		}
		write(
			w,
			r,
			found.info.Encode("nar/"+path[:32]+"/"+strings.TrimPrefix(found.info.URL, "nar/")),
			"text/x-nix-narinfo",
		)
		return
	}
	parts := strings.Split(path, "/")
	if len(parts) != 3 || parts[0] != "nar" || !nixfmt.IsInfoPath(parts[1]+".narinfo") {
		h.fail(w, r, kernel.ErrNotFound)
		return
	}
	found, err := h.lookup(r, parts[1], "nar/"+parts[2])
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if found.info.URL != "nar/"+parts[2] {
		h.fail(w, r, kernel.ErrNotFound)
		return
	}
	c := pkgroute.RequestFrom(found.r).Repo
	if c.Class == repo.Remote {
		h.remoteArchive(w, r, found)
		return
	}
	if err = found.check(r.Context()); err != nil {
		h.fail(w, r, err)
		return
	}
	h.k.ServeFound(w, found.r, pkgroute.Found{Repo: c, Item: *found.archive})
}

func write(w http.ResponseWriter, r *http.Request, data []byte, mediaType string) {
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Cache-Control", "private, no-cache")
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
}

// missing reports a member that a virtual repository passes over: one
// without the path, and one whose download policy refuses it, a remote
// member's included, since 7.x ignores a blacked-out member when it
// resolves and resolves nothing from a member whose patterns reject the
// path (R5). An upstream fault is neither, so the virtual
// repository fails closed on it.
func missing(err error) bool {
	var e *apierror.Error
	return errors.Is(err, kernel.ErrNotFound) || errors.As(err, &e) && e.Status == 404 ||
		pkgroute.IsRefusal(err)
}

// lookup finds the narinfo of the store path whose hash is hash, and the
// NAR it names, for a caller who may read both. archive is empty for a
// narinfo request, and the NAR's URL, nar/<file>, for a NAR request. Both
// are checked where the repository stores them, a hosted repository's
// binary-cache folder (nixfmt.InfoItem, nixfmt.ArchiveItem), so that
// permissions, patterns and refusals name the paths 7.x stores and jf nix
// queries; a remote's cache keeps the client's paths. The download policy
// is checked at the file the client asked for alone, before the narinfo
// is read: a narinfo request at the narinfo, so that patterns rejecting
// only its NAR leave the stored narinfo served, as 7.x serves a stored
// document whose release's file is refused (R1, R3); a NAR request at the
// NAR, so that the narinfo read on the way is a lookup the patterns do not
// refuse and a refusal names the NAR (R4, R5, R10). A virtual repository
// checks its own patterns once, at that file in a hosted repository's
// layout [I], and never drops a member's narinfo for them.
func (h handler) lookup(r *http.Request, hash, archive string) (located, error) {
	c := pkgroute.RequestFrom(r).Repo
	cached := c.Class == repo.Remote
	path := nixfmt.InfoItem(hash, cached)
	checked, nar := path, ""
	if archive != "" {
		nar = nixfmt.ArchiveItem(hash, archive, cached)
		checked = nar
	}
	if !h.k.Can(r, path, accessapi.ActionRead) ||
		nar != "" && !h.k.Can(r, nar, accessapi.ActionRead) {
		return located{}, source.ErrDenied
	}
	if err := generic.CheckDownload(c, checked, false); err != nil {
		return located{}, fmt.Errorf("nix: lookup: %w", err)
	}
	if c.Class == repo.Virtual {
		for _, key := range c.Repositories {
			member, err := h.k.DB().Repos().Get(r.Context(), key)
			if errors.Is(err, kernel.ErrNotFound) {
				continue
			}
			if err != nil {
				return located{}, fmt.Errorf("nix: member: %w", err)
			}
			mr, err := h.k.MemberRequest(r, member)
			if err != nil {
				return located{}, fmt.Errorf("nix: lookup: %w", err)
			}
			memberInfo := nixfmt.InfoItem(hash, member.Class == repo.Remote)
			if !h.k.Can(mr, memberInfo, accessapi.ActionRead) {
				continue
			}
			found, err := h.lookup(mr, hash, archive)
			if missing(err) {
				continue
			}
			if err != nil {
				return located{}, fmt.Errorf("nix: lookup: %w", err)
			}
			stored := nixfmt.ArchiveItem(hash, found.info.URL, false)
			if !h.k.Can(r, stored, accessapi.ActionRead) {
				continue
			}
			memberCheck := found.check
			found.check = func(ctx context.Context) error {
				if err := h.current(r, path, stored); err != nil {
					return err
				}
				return memberCheck(ctx)
			}
			return found, nil
		}
		return located{}, kernel.ErrNotFound
	}
	if cached {
		return h.remoteInfo(r, path, archive != "")
	}
	it, err := h.k.DB().Items().Get(r.Context(), c.Key, false, path)
	if err != nil {
		return located{}, fmt.Errorf("nix: metadata item: %w", err)
	}
	info, err := nixfmt.ReadInfo(r.Context(), h.env(), it)
	if err != nil {
		return located{}, source.ErrInvalidResult
	}
	stored := nixfmt.ArchiveItem(hash, info.URL, false)
	if !h.k.Can(r, stored, accessapi.ActionRead) {
		return located{}, source.ErrDenied
	}
	archived, err := nixfmt.ReferencedArchive(r.Context(), h.env(), c, info, false)
	if err != nil {
		return located{}, fmt.Errorf("nix: lookup: %w", err)
	}
	return located{
		info:    info,
		archive: &archived,
		r:       r,
		check:   func(context.Context) error { return h.current(r, path, stored) },
	}, nil
}

func (h handler) current(r *http.Request, paths ...string) error {
	c := pkgroute.RequestFrom(r).Repo
	fresh, err := h.k.DB().Repos().Get(r.Context(), c.Key)
	if err != nil {
		return fmt.Errorf("nix: repository authority: %w", err)
	}
	if !reflect.DeepEqual(c, fresh) {
		return source.ErrSuperseded
	}
	p, _ := kernel.FromContext(r.Context())
	p, d, err := h.o.Authorizer.Authorize(r.Context(), p, true)
	if err != nil {
		return fmt.Errorf("nix: current permissions: %w", err)
	}
	r = r.WithContext(accessapi.WithCaller(r.Context(), p, d))
	for _, path := range paths {
		if !h.k.Can(r, path, accessapi.ActionRead) {
			return source.ErrDenied
		}
	}
	return nil
}

func (h handler) sign(r *http.Request, info *nixfmt.Info) error {
	c := pkgroute.RequestFrom(r).Repo
	if c.Class != repo.Local && c.Class != repo.Federated {
		return nil
	}
	if c.PrimaryKeyPairRef == "" {
		return nil
	}
	pair, err := h.o.KeyPairs.Keypair(r.Context(), c.PrimaryKeyPairRef)
	if err != nil || pair.Type != accessapi.KeypairEd25519 {
		return apierror.New(503, "Nix signing key is unavailable")
	}
	key, err := keymaterial.Ed25519PrivateKey(pair.PrivateKey, pair.Passphrase)
	if err != nil {
		return apierror.New(503, "Nix signing key is invalid")
	}
	if err = info.Sign(pair.Alias, key); err != nil {
		return apierror.New(503, "Nix signing key alias is invalid")
	}
	return nil
}

func (h handler) put(w http.ResponseWriter, r *http.Request) {
	path, err := requestPath(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	c := pkgroute.RequestFrom(r).Repo
	if c.Class != repo.Local && c.Class != repo.Federated {
		h.fail(w, r, apierror.New(405, "Nix publication requires a hosted repository"))
		return
	}
	// The JFrog documentation uploads a channel snapshot to its folder,
	// channels/<name>/<flavor>/<snapshot>, and says its tarball's name,
	// nixexprs.tar.xz, is enforced: the tarball is stored under that name
	// in the folder [I], where nix-channel asks for it. A PUT that names the
	// file itself, as curl -T sends to a URL ending in a slash, lands at the
	// same path.
	if s, ok := nixfmt.ParseSnapshotDir(path); ok {
		path = s.Path()
	}
	limit := nixfmt.MaxArchiveBytes
	if nixfmt.IsInfoPath(path) || path == "nix-cache-info" {
		limit = nixfmt.MaxMetadataBytes
	}
	body := io.LimitReader(r.Body, limit+1)
	if nixfmt.IsInfoPath(path) {
		body, path, err = h.publish(r, path, body)
		if err != nil {
			h.fail(w, r, err)
			return
		}
	}
	it, err := h.k.Store(r, path, body, nil, kernel.Checksums{})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.k.Created(w, r, it)
}

// publish prepares the narinfo a client uploads to its root path,
// <hash>.narinfo, for storing at its InfoItem, and returns its body and
// that path. Nix uploads the NAR first, to nar/<file>, which names no
// store path, so the repository holds it there (nixfmt.Handler); the
// narinfo names both, and the held NAR's content is stored at the
// narinfo's ArchiveItem before the narinfo, which the format admits only
// once its archive is there. A narinfo that does not parse is stored as
// it is, for the format to refuse.
func (h handler) publish(r *http.Request, path string, body io.Reader) (io.Reader, string, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, "", fmt.Errorf("nix: read narinfo: %w", err)
	}
	stored := nixfmt.InfoItem(path[:32], false)
	info, err := nixfmt.Parse(data, stored)
	if err == nil {
		err = h.place(r, info)
	}
	if err != nil && !errors.Is(err, nixfmt.ErrInvalid) {
		return nil, "", err
	}
	return bytes.NewReader(data), stored, nil
}

// place stores the archive info names at its ArchiveItem unless it is
// there: from the copy held at info.URL, or else from the same file
// stored for another store path of the repository, which the client
// uploaded and the repository released while this narinfo was on its way.
// Either must be a file of the repository whose SHA-256 and size are the
// narinfo's. The caller needs Write at the ArchiveItem, as for any upload,
// but no Read on the source, which publishing never needed: the format
// admits the narinfo only when the NAR's content hashes to its NarHash,
// which only someone who has the content can give. Without a source, the
// format refuses the narinfo with 409.
func (h handler) place(r *http.Request, info nixfmt.Info) error {
	ctx := r.Context()
	c := pkgroute.RequestFrom(r).Repo
	target := nixfmt.ArchiveItem(info.Hash(), info.URL, false)
	_, err := h.k.DB().Items().Get(ctx, c.Key, false, target)
	if !errors.Is(err, kernel.ErrNotFound) {
		return wrap("stored archive", err)
	}
	from, ok, err := h.source(r, info)
	if !ok || err != nil {
		return err
	}
	bi, err := h.k.Blobs().Stat(ctx, from.Actual.SHA256)
	if err != nil {
		return fmt.Errorf("nix: held archive content: %w", err)
	}
	bi.Digests = blob.Digests{
		SHA256: from.Actual.SHA256,
		SHA1:   from.Actual.SHA1,
		SHA512: from.Actual.SHA512,
		MD5:    from.Actual.MD5,
	}
	// A concurrent publication of the same store path stored it first.
	_, err = h.k.StoreNew(r, target, bi, nil, kernel.Checksums{}, func() error { return nil })
	return wrap("store archive", err)
}

// source finds the file place copies, and reports whether there is one:
// the held upload at info.URL, then a stored archive of the same file name
// and content in a store path's folder.
func (h handler) source(r *http.Request, info nixfmt.Info) (model.Item, bool, error) {
	ctx := r.Context()
	c := pkgroute.RequestFrom(r).Repo
	usable := func(it model.Item) bool {
		return !it.Folder && !it.Cached && it.RepoKey == c.Key && it.Size == info.FileSize &&
			it.Actual.SHA256 == info.FileHash
	}
	held, err := h.k.DB().Items().Get(ctx, c.Key, false, info.URL)
	switch {
	case err == nil && usable(held):
		return held, true, nil
	case err != nil && !errors.Is(err, kernel.ErrNotFound):
		return model.Item{}, false, fmt.Errorf("nix: held archive: %w", err)
	}
	name := "/" + strings.TrimPrefix(info.URL, "nar/")
	for page := (kernel.Page{Limit: kernel.MaxLimit}); ; {
		items, next, err := h.k.DB().
			Items().
			FindByChecksum(ctx, kernel.Checksums{SHA256: info.FileHash}, page)
		if err != nil {
			return model.Item{}, false, fmt.Errorf("nix: stored archives: %w", err)
		}
		for _, it := range items {
			if _, ok := nixfmt.IsArchiveItem(it.FullPath(), false); ok &&
				strings.HasSuffix(it.FullPath(), name) && usable(it) {
				return it, true, nil
			}
		}
		if next == "" {
			return model.Item{}, false, nil
		}
		page.After = next
	}
}

func wrap(what string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("nix: %s: %w", what, err)
}

// remove deletes what a client's URL names where the repository stores it:
// a narinfo, <hash>.narinfo, and a published NAR, nar/<hash>/<file> as the
// narinfo it serves names it, in a hosted repository's binary-cache
// folder. nar/<file> names only the upload a hosted repository holds until
// its narinfo is published [I].
func (h handler) remove(w http.ResponseWriter, r *http.Request) {
	path, err := requestPath(r)
	if err == nil {
		err = h.k.Delete(r, stored(pkgroute.RequestFrom(r).Repo, path))
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// stored is where a hosted repository c stores what the client's path
// names, or path itself.
func stored(c repo.Config, path string) string {
	if c.Class != repo.Local && c.Class != repo.Federated {
		return path
	}
	if nixfmt.IsInfoPath(path) {
		return nixfmt.InfoItem(path[:32], false)
	}
	parts := strings.Split(path, "/")
	if len(parts) == 3 && parts[0] == "nar" && nixfmt.IsInfoPath(parts[1]+".narinfo") {
		if _, ok := nixfmt.ArchivePath("nar/" + parts[2]); ok {
			return nixfmt.ArchiveItem(parts[1], "nar/"+parts[2], false)
		}
	}
	return path
}

func (h handler) reindex(w http.ResponseWriter, r *http.Request) {
	c := pkgroute.RequestFrom(r).Repo
	if r.URL.RawQuery != "" || c.Class == repo.Virtual {
		h.fail(w, r, apierror.New(400, "invalid Nix reindex request"))
		return
	}
	if !h.k.RequireFolder(w, r, "", accessapi.ActionManage) {
		return
	}
	err := h.k.DB().
		Tx(r.Context(), func(ctx context.Context) error { return (nixfmt.Handler{}).Reindex(ctx, h.env(), c) })
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

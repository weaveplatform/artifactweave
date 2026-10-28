// Package nix serves the native Nix HTTP binary-cache protocol.
package nix

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	nixfmt "github.com/weaveplatform/artifactweave/internal/format/nix"
	"github.com/weaveplatform/artifactweave/internal/keymaterial"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func Protocol(remote *source.Service) pkgroute.Protocol {
	sources := map[repo.Class]pkgroute.Source{
		repo.Local:     pkgroute.Local{},
		repo.Federated: pkgroute.Local{},
		repo.Virtual:   pkgroute.Local{},
	}
	if remote != nil {
		sources[repo.Remote] = pkgroute.Remote{Service: remote}
	}
	return pkgroute.Protocol{
		Name:         "nix",
		Types:        []repo.PackageType{repo.Nix},
		ErrorProfile: apierror.ProfileArtifactory,
		Sources:      sources,
		Routes: func(r chi.Router, k *pkgroute.Kit) {
			h := handler{k}
			r.Get("/*", h.read)
			r.Head("/*", h.read)
			r.Put("/*", h.put)
			r.Delete("/*", h.remove)
			r.Post("/reindex", h.reindex)
		},
	}
}

type (
	handler struct{ k *pkgroute.Kit }
	located struct {
		info    nixfmt.Info
		archive *types.Item
		r       *http.Request
		check   func(context.Context) error
	}
)

func (h handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	p, _ := auth.FromContext(r.Context())
	h.k.Error(w, r, pkgroute.SourceError(err, p.Anonymous || p.Name == ""))
}

func (h handler) env() format.Env {
	return format.Env{Store: h.k.DB(), Blobs: h.k.Blobs(), Now: time.Now}
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
	if path == "nix-cache-info" {
		if err := generic.CheckDownload(pkgroute.RequestFrom(r).Repo, path, false); err != nil {
			h.fail(w, r, err)
			return
		}
		if h.k.Require(w, r, path, types.ActionRead) {
			write(w, r, []byte(nixfmt.CacheInfo), "text/x-nix-cache-info")
		}
		return
	}
	if nixfmt.IsInfoPath(path) {
		found, err := h.lookup(r, path)
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
		h.fail(w, r, store.ErrNotFound)
		return
	}
	found, err := h.lookup(r, parts[1]+".narinfo")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if found.info.URL != "nar/"+parts[2] {
		h.fail(w, r, store.ErrNotFound)
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

func missing(err error) bool {
	var e *apierror.Error
	return errors.Is(err, store.ErrNotFound) || errors.As(err, &e) && e.Status == 404
}

func (h handler) lookup(r *http.Request, path string) (located, error) {
	c := pkgroute.RequestFrom(r).Repo
	if !h.k.Can(r, path, types.ActionRead) {
		return located{}, source.ErrDenied
	}
	if err := generic.CheckDownload(c, path, false); err != nil {
		return located{}, fmt.Errorf("nix: lookup: %w", err)
	}
	if c.Class == repo.Virtual {
		for _, key := range c.Repositories {
			member, err := h.k.DB().Repos().Get(r.Context(), key)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				return located{}, fmt.Errorf("nix: member: %w", err)
			}
			mr, err := h.k.MemberRequest(r, member)
			if err != nil {
				return located{}, fmt.Errorf("nix: lookup: %w", err)
			}
			if !h.k.Can(mr, path, types.ActionRead) {
				continue
			}
			found, err := h.lookup(mr, path)
			if missing(err) {
				continue
			}
			if err != nil {
				return located{}, fmt.Errorf("nix: lookup: %w", err)
			}
			if !h.k.Can(r, found.info.URL, types.ActionRead) ||
				generic.CheckDownload(c, found.info.URL, false) != nil {
				continue
			}
			memberCheck := found.check
			found.check = func(ctx context.Context) error {
				if err := h.current(r, path, found.info.URL); err != nil {
					return err
				}
				return memberCheck(ctx)
			}
			return found, nil
		}
		return located{}, store.ErrNotFound
	}
	if c.Class == repo.Remote {
		return h.remoteInfo(r, path)
	}
	it, err := h.k.DB().Items().Get(r.Context(), c.Key, false, path)
	if err != nil {
		return located{}, fmt.Errorf("nix: metadata item: %w", err)
	}
	info, err := nixfmt.ReadInfo(r.Context(), h.env(), it)
	if err != nil {
		return located{}, source.ErrInvalidResult
	}
	if !h.k.Can(r, info.URL, types.ActionRead) {
		return located{}, source.ErrDenied
	}
	if err := generic.CheckDownload(c, info.URL, false); err != nil {
		return located{}, fmt.Errorf("nix: lookup: %w", err)
	}
	archive, err := nixfmt.ReferencedArchive(r.Context(), h.env(), c, info, false)
	if err != nil {
		return located{}, fmt.Errorf("nix: lookup: %w", err)
	}
	return located{
		info:    info,
		archive: &archive,
		r:       r,
		check:   func(context.Context) error { return h.current(r, path, info.URL) },
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
	p, _ := auth.FromContext(r.Context())
	p, err = auth.Authorize(
		r.Context(),
		authz.Sources{
			Permissions:      h.k.DB().Permissions(),
			Groups:           h.k.DB().Groups(),
			AnonymousEnabled: true,
		},
		p,
	)
	if err != nil {
		return fmt.Errorf("nix: current permissions: %w", err)
	}
	r = r.WithContext(auth.WithPrincipal(r.Context(), p))
	for _, path := range paths {
		if !h.k.Can(r, path, types.ActionRead) {
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
	pair, err := h.k.DB().Keys().Keypair(r.Context(), c.PrimaryKeyPairRef)
	if err != nil || pair.Type != types.KeypairEd25519 {
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
	limit := nixfmt.MaxArchiveBytes
	if nixfmt.IsInfoPath(path) || path == "nix-cache-info" {
		limit = nixfmt.MaxMetadataBytes
	}
	it, err := h.k.Store(r, path, io.LimitReader(r.Body, limit+1), nil, types.Checksums{})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.k.Created(w, r, it)
}

func (h handler) remove(w http.ResponseWriter, r *http.Request) {
	path, err := requestPath(r)
	if err == nil {
		err = h.k.Delete(r, path)
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h handler) reindex(w http.ResponseWriter, r *http.Request) {
	c := pkgroute.RequestFrom(r).Repo
	if r.URL.RawQuery != "" || c.Class == repo.Virtual {
		h.fail(w, r, apierror.New(400, "invalid Nix reindex request"))
		return
	}
	if !h.k.RequireFolder(w, r, "", types.ActionManage) {
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

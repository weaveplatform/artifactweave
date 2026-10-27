// Package huggingface serves the native Hugging Face Hub client protocol for
// model and dataset repositories using Artifactory's current ML layout.
package huggingface

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/crypt"
	format "github.com/weaveplatform/artifactweave/internal/format/huggingface"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

const signingPurpose = "huggingfaceml-lfs-upload-v1"

type Options struct {
	RemoteSources *source.Service
	Keys          *crypt.Keyring
	Now           func() time.Time
}

type virtualSource struct{}

func (virtualSource) Item(
	_ context.Context,
	_ store.Store,
	_ repo.Config,
	_ string,
) (pkgroute.Found, error) {
	return pkgroute.Found{}, source.ErrUnsupported
}

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
		Name: "huggingfaceml", Types: []repo.PackageType{repo.HuggingFaceML},
		Surface: apierror.SurfaceArtifactory, Sources: sources,
		Signed: func(r *http.Request) bool {
			return r.Method == http.MethodPut &&
				strings.Contains(r.URL.EscapedPath(), "/lfs/upload/") &&
				r.URL.Query().Has("sig")
		},
		Routes: func(r chi.Router, k *pkgroute.Kit) {
			h := handler{k: k, o: o}
			r.Post("/api/repos/create", h.createRepo)
			for _, kind := range []string{"models", "datasets"} {
				prefix := "/api/" + kind + "/{owner}/{name}"
				r.Get(prefix, h.info)
				r.Get(prefix+"/revision/{revision}", h.info)
				r.Post(prefix+"/branch/{revision}", h.createBranch)
				r.Get(prefix+"/tree/{revision}", h.tree)
				r.Get(prefix+"/tree/{revision}/*", h.tree)
				r.Post(prefix+"/preupload/{revision}", h.preupload)
				r.Post(prefix+"/commit/{revision}", h.commit)
				r.Get(prefix+"/xet-read-token/{revision}", h.xetUnavailable)
				r.Get(prefix+"/xet-write-token/{revision}", h.xetUnavailable)
			}
			r.Get("/{owner}/{name}/resolve/{revision}/*", h.resolve)
			r.Head("/{owner}/{name}/resolve/{revision}/*", h.resolve)
			r.Get("/datasets/{owner}/{name}/resolve/{revision}/*", h.resolve)
			r.Head("/datasets/{owner}/{name}/resolve/{revision}/*", h.resolve)
			r.Post("/{owner}/{name}.git/info/lfs/objects/batch", h.lfsBatch)
			r.Post("/datasets/{owner}/{name}.git/info/lfs/objects/batch", h.lfsBatch)
			r.Put("/lfs/upload/{oid}", h.lfsUpload)
		},
	}
}

type handler struct {
	k *pkgroute.Kit
	o Options
}

func (h handler) now() time.Time {
	if h.o.Now != nil {
		return h.o.Now()
	}
	return time.Now()
}

func identity(r *http.Request) (format.Identity, bool) {
	kind := "models"
	if strings.HasPrefix(pkgroute.RequestFrom(r).Rest, "api/datasets/") ||
		strings.HasPrefix(pkgroute.RequestFrom(r).Rest, "datasets/") {
		kind = "datasets"
	}
	return format.ParseIdentity(kind, chi.URLParam(r, "owner"), chi.URLParam(r, "name"))
}

func (h handler) xetUnavailable(w http.ResponseWriter, r *http.Request) {
	h.k.Error(
		w,
		r,
		apierror.New(http.StatusNotImplemented, "Xet CAS is not enabled for this repository"),
	)
}

func (h handler) signedUpload(repoKey, oid string, size, expiry int64) string {
	if h.o.Keys == nil {
		return ""
	}
	mac := hmac.New(sha256.New, h.o.Keys.Derive(signingPurpose))
	_, _ = fmt.Fprintf(mac, "%s\n%s\n%d\n%d", repoKey, oid, size, expiry)
	return hex.EncodeToString(mac.Sum(nil))
}

func (h handler) verifyUpload(r *http.Request, oid string) (int64, bool) {
	q := r.URL.Query()
	if h.o.Keys == nil || !formatSHA256(oid) || len(q) != 3 ||
		len(q["size"]) != 1 || len(q["exp"]) != 1 || len(q["sig"]) != 1 ||
		q.Get("sig") == "" {
		return 0, false
	}
	size, err1 := strconv.ParseInt(r.URL.Query().Get("size"), 10, 64)
	exp, err2 := strconv.ParseInt(r.URL.Query().Get("exp"), 10, 64)
	if err1 != nil || err2 != nil || size <= 0 || size > 10<<30 ||
		exp <= h.now().Unix() || exp > h.now().Add(10*time.Minute).Unix() {
		return 0, false
	}
	want, err := hex.DecodeString(h.signedUpload(pkgroute.RequestFrom(r).Repo.Key, oid, size, exp))
	if err != nil {
		return 0, false
	}
	got, err := hex.DecodeString(r.URL.Query().Get("sig"))
	return size, err == nil && hmac.Equal(got, want)
}

func formatSHA256(s string) bool {
	if len(s) != 64 || strings.ToLower(s) != s {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

package terraformhttp

import (
	"net/http"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	tf "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/terraform"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

func (h handler) virtualLocalProviderURL(r, mr *http.Request, q providerOp, it model.Item, kind int) (string, error) {
	p, _ := kernel.FromContext(r.Context())
	now := h.o.Now()
	c := claims{
		Subject:  p.Name,
		Repo:     pkgroute.RequestFrom(r).Repo.Key,
		Member:   pkgroute.RequestFrom(mr).Repo.Key,
		Path:     it.FullPath(),
		SHA256:   it.Actual.SHA256,
		Issued:   now.Unix(),
		Expires:  now.Add(90 * time.Second).Unix(),
		Provider: &providerClaim{Name: q.name, Version: q.version, OS: q.os, Arch: q.arch, Kind: kind},
	}
	return h.signDownloadURL(r, c)
}

func (h handler) virtualRemoteProviderURL(r, mr *http.Request, b providerBundle, index int) (string, error) {
	c, err := h.providerDownloadClaim(mr, b, index)
	if err != nil {
		return "", err
	}
	c.Member = pkgroute.RequestFrom(mr).Repo.Key
	c.Repo = pkgroute.RequestFrom(r).Repo.Key
	return h.signDownloadURL(r, c)
}

func (h handler) virtualSignedProvider(w http.ResponseWriter, r *http.Request) {
	c, valid := h.verify(r.URL.Query().Get("sig"))
	q := pkgroute.RequestFrom(r)
	rest, ok := strings.CutPrefix(q.Rest, "providers/")
	path, err := pkgroute.ParsePath(rest)
	if !valid || !ok || err != nil || path.Folder || len(path.Props) > 0 || path.String() != c.Path || c.Repo != q.Repo.Key || c.Member == "" || c.Provider == nil ||
		c.Module != "" {
		h.k.Error(w, r, forbidden)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		h.k.Error(w, r, apierror.New(405, "method not allowed"))
		return
	}
	p, d, err := h.principal(r, c.Subject)
	if c.Credential != nil {
		if h.o.Authenticator == nil {
			h.k.Error(w, r, forbidden)
			return
		}
		p, err = h.o.Authenticator.RestoreDownload(r.Context(), c.Subject, *c.Credential)
		if err == nil {
			p, d, err = h.o.Authorizer.Authorize(r.Context(), p, p.Anonymous)
		}
	}
	if err != nil {
		h.k.Error(w, r, signedAuthorityError(err))
		return
	}
	r = r.WithContext(api.WithCaller(r.Context(), p, d))
	members, err := h.k.DownloadMembers(r, c.Path)
	if err != nil {
		h.virtualSignedError(w, r, err)
		return
	}
	for _, mr := range members {
		member := pkgroute.RequestFrom(mr).Repo
		if member.Key != c.Member || !providerMember(member) {
			continue
		}
		if !h.k.Can(mr, c.Path, api.ActionRead) {
			h.k.Error(w, r, forbidden)
			return
		}
		if member.Class == repo.Remote {
			h.remoteSignedProvider(w, mr, c)
			return
		}
		if !validHostedProviderClaim(c) {
			h.k.Error(w, r, forbidden)
			return
		}
		found, err := h.k.ResolveDownloadItem(mr, c.Path)
		if err != nil {
			h.virtualSignedError(w, r, err)
			return
		}
		if found.Item.Folder || found.Item.Actual.SHA256 != c.SHA256 {
			h.k.Error(w, r, forbidden)
			return
		}
		if err := h.virtualClaimFence(r, c); err != nil {
			h.k.Error(w, r, err)
			return
		}
		h.k.ServeFound(w, mr, found)
		return
	}
	h.k.Error(w, r, forbidden)
}

func validHostedProviderClaim(c claims) bool {
	p := c.Provider
	if p == nil || c.Source != nil || c.Credential != nil || p.Metadata != "" || p.Mirror || p.Kind < 0 || p.Kind > 2 {
		return false
	}
	file, ok := tf.ParseProviderPackageFile(c.Path)
	if !ok || file.Package() != p.Name || file.Version != p.Version {
		return false
	}
	switch p.Kind {
	case 0:
		return file.Kind == tf.KindArchive && file.OS == p.OS && file.Arch == p.Arch
	case 1:
		return file.Kind == tf.KindSums
	default:
		return file.Kind == tf.KindSignature
	}
}

package terraformhttp

import (
	"context"
	"fmt"
	"net/http"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

type providerClaim struct {
	Host     string `json:"host,omitempty"`
	Mirror   bool   `json:"mirror,omitempty"`
	Name     string `json:"name"`
	Version  string `json:"version"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Metadata string `json:"metadata"`
	Kind     int    `json:"kind"`
}

func (h handler) remoteProviderURL(r *http.Request, b providerBundle, index int) (string, error) {
	c, err := h.providerDownloadClaim(r, b, index)
	if err != nil {
		return "", err
	}
	return h.signDownloadURL(r, c)
}

func (h handler) providerDownloadClaim(r *http.Request, b providerBundle, index int) (claims, error) {
	p, _ := kernel.FromContext(r.Context())
	reference, err := p.DownloadReference()
	if err != nil {
		return claims{}, fmt.Errorf("terraform provider identity: %w", err)
	}
	result := b.results[b.resultIndex(index)]
	now := h.o.Now()
	return claims{
		Subject:    p.Name,
		Repo:       pkgroute.RequestFrom(r).Repo.Key,
		Path:       b.paths[index],
		SHA256:     moduleDigest(result),
		Issued:     now.Unix(),
		Expires:    now.Add(90 * time.Second).Unix(),
		Source:     &result.Identity,
		Credential: &reference,
		Provider: &providerClaim{
			Name:     b.q.name,
			Version:  b.q.version,
			OS:       b.q.os,
			Arch:     b.q.arch,
			Metadata: b.digest,
			Kind:     index,
			Host:     b.q.host,
			Mirror:   b.q.isUpstreamMirror,
		},
	}, nil
}

func (b providerBundle) resultIndex(kind int) int {
	if b.q.isUpstreamMirror {
		return 1
	}
	if kind == 0 {
		return 3
	}
	return kind
}

func (h handler) remoteSignedProvider(w http.ResponseWriter, r *http.Request, c claims) {
	if h.o.Authenticator == nil || c.Credential == nil || c.Source == nil || c.Provider == nil {
		h.k.Error(w, r, forbidden)
		return
	}
	p := c.Provider
	q := providerOp{
		name:             p.Name,
		version:          p.Version,
		os:               p.OS,
		arch:             p.Arch,
		host:             p.Host,
		isUpstreamMirror: p.Mirror,
	}
	if !validRemoteProvider(q) || p.Kind < 0 || p.Kind > 2 || p.Metadata == "" ||
		p.Mirror && (!validHostname(p.Host) || p.Kind != 0) {
		h.k.Error(w, r, forbidden)
		return
	}
	principal, err := h.o.Authenticator.RestoreDownload(r.Context(), c.Subject, *c.Credential)
	var d accessapi.Decider
	if err == nil {
		principal, d, err = h.o.Authorizer.Authorize(r.Context(), principal, principal.Anonymous)
	}
	if err != nil {
		h.k.Error(w, r, signedAuthorityError(err))
		return
	}
	ctx := accessapi.WithCaller(r.Context(), principal, d)
	r = r.WithContext(ctx)
	if c.Member != "" {
		if !h.k.Can(r, c.Path, accessapi.ActionRead) {
			h.k.Error(w, r, forbidden)
			return
		}
	} else if !h.k.Require(w, r, c.Path, accessapi.ActionRead) {
		return
	}
	if err := h.currentSignedSource(r, c); err != nil {
		h.k.Error(w, r, err)
		return
	}
	// The file's cache path follows the URL the metadata names, so the
	// metadata read now must name the URL's path for this file before any
	// file is read.
	admit := func(paths []string) error {
		if paths[p.Kind] != c.Path {
			return forbidden
		}
		return nil
	}
	var bundle providerBundle
	if q.isUpstreamMirror {
		bundle, err = h.providerMirrorBundle(r, q, admit)
	} else {
		bundle, err = h.providerBundle(r, q, admit)
	}
	if err != nil {
		bundle.close()
		h.k.Error(w, r, signedMemberError(c, err))
		return
	}
	selected := bundle.resultIndex(p.Kind)
	result := bundle.results[selected]
	if bundle.digest != p.Metadata || moduleDigest(result) != c.SHA256 ||
		result.Identity != *c.Source {
		bundle.close()
		h.k.Error(w, r, forbidden)
		return
	}
	// The selected result's body belongs to ServeResult; all companions remain
	// alive until its delivery fence has checked this complete verified bundle.
	defer func() {
		for index, entry := range bundle.results {
			if index != selected {
				closeModuleResult(entry)
			}
		}
	}()
	result.Revalidate = combineModuleFences(bundle.results, readCheck, bundle.disclosed())
	if c.Member != "" {
		check := result.Revalidate
		result.Revalidate = func(ctx context.Context) error {
			if err := check(ctx); err != nil {
				if signedDeliveryRefusal(err) {
					return forbidden
				}
				return err
			}
			return h.virtualClaimFence(r.WithContext(ctx), c)
		}
	}
	result.Used = combineModuleUsage(bundle.results)
	h.k.ServeResult(w, r, result)
}

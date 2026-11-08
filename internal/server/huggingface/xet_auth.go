package huggingface

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	format "github.com/weaveplatform/artifactweave/internal/format/huggingface"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/baseurl"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const (
	xetPurpose = "huggingfaceml-xet-cas-v1"
	xetTTL     = 5 * time.Minute
	// A valid 1,024-byte file path may expand sixfold in JSON before base64.
	maxXetTokenSize = 16 << 10
)

type xetClaim struct {
	Repo       string                    `json:"repo"`
	Member     string                    `json:"member"`
	Kind       string                    `json:"kind"`
	Owner      string                    `json:"owner"`
	Name       string                    `json:"name"`
	Reference  string                    `json:"reference,omitempty"`
	File       string                    `json:"file,omitempty"`
	Revision   string                    `json:"revision"`
	Write      bool                      `json:"write"`
	Subject    string                    `json:"subject"`
	Credential kernel.DownloadCredential `json:"credential"`
	Issued     int64                     `json:"issued"`
	Expires    int64                     `json:"expires"`
}

func (h handler) xetReadToken(w http.ResponseWriter, r *http.Request)  { h.xetToken(w, r, false) }
func (h handler) xetWriteToken(w http.ResponseWriter, r *http.Request) { h.xetToken(w, r, true) }

func (h handler) xetToken(w http.ResponseWriter, r *http.Request, write bool) {
	if !h.o.XetEnabled || h.o.Keys == nil || h.o.Authenticator == nil {
		h.xetUnavailable(w, r)
		return
	}
	id, ok := identity(r)
	revision := chi.URLParam(r, "revision")
	reference := revision
	q := r.URL.Query()
	file := q.Get("file")
	if !ok || !format.RefName(revision) ||
		len(q) > 0 && (write || len(q) != 1 || len(q["file"]) != 1 || !format.FilePath(file)) {
		h.k.Error(w, r, badIdentity())
		return
	}
	var selectedRequest *http.Request
	var err error
	if write {
		selectedRequest, err = h.targetRequest(r, id)
	} else {
		var selected selected
		selected, err = h.selectRepo(r, id, revision, map[string]bool{})
		selectedRequest = selected.request
		revision = selected.info.SHA
	}
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if !write && pkgroute.RequestFrom(selectedRequest).Repo.Class == repo.Remote && file == "" {
		h.k.Error(w, r, badIdentity())
		return
	}
	p, _ := kernel.FromContext(r.Context())
	credential, err := p.DownloadReference()
	if err != nil {
		h.k.Error(
			w,
			r,
			apierror.New(http.StatusForbidden, "Xet requires a current model credential"),
		)
		return
	}
	now := h.now().Unix()
	c := xetClaim{
		Repo:       pkgroute.RequestFrom(r).Repo.Key,
		Member:     pkgroute.RequestFrom(selectedRequest).Repo.Key,
		Kind:       id.Kind,
		Owner:      id.Owner,
		Name:       id.Name,
		Reference:  reference,
		File:       file,
		Revision:   revision,
		Write:      write,
		Subject:    p.Name,
		Credential: credential,
		Issued:     now,
		Expires:    now + int64(xetTTL/time.Second),
	}
	token := h.signXet(c)
	if token == "" {
		h.k.Error(
			w,
			r,
			apierror.New(
				http.StatusBadRequest,
				"model capability metadata exceeds the supported size",
			),
		)
		return
	}
	endpoint := h.xetEndpoint(r, c)
	w.Header().Set("X-Xet-Cas-Url", endpoint)
	w.Header().Set("X-Xet-Access-Token", token)
	w.Header().Set("X-Xet-Token-Expiration", fmt.Sprint(c.Expires))
	w.Header().Set("Cache-Control", "no-store")
	h.writeJSON(w, map[string]any{"casUrl": endpoint, "exp": c.Expires, "accessToken": token})
}

func (h handler) xetEndpoint(r *http.Request, c xetClaim) string {
	return baseurl.From(
		r.Context(),
	) + "/artifactory/api/huggingfaceml/" + url.PathEscape(
		c.Repo,
	) + "/xet/" + c.Kind + "/" + c.Owner + "/" + c.Name + "/" + c.Revision
}

func (h handler) signXet(c xetClaim) string {
	raw, _ := json.Marshal(c)
	encodedSize := base64.RawURLEncoding.EncodedLen(
		len(raw),
	) + 1 + base64.RawURLEncoding.EncodedLen(
		sha256.Size,
	)
	if h.o.Keys == nil || encodedSize > maxXetTokenSize {
		return ""
	}
	payload := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, h.o.Keys.Derive(xetPurpose))
	_, _ = mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (h handler) verifyXet(raw string) (xetClaim, bool) {
	var c xetClaim
	if h.o.Keys == nil || len(raw) > maxXetTokenSize {
		return c, false
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 2 {
		return c, false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return c, false
	}
	mac := hmac.New(sha256.New, h.o.Keys.Derive(xetPurpose))
	_, _ = mac.Write([]byte(parts[0]))
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return c, false
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(data, &c) != nil {
		return c, false
	}
	_, ok := format.ParseIdentity(c.Kind, c.Owner, c.Name)
	now := h.now().Unix()
	return c, ok && c.Repo != "" && c.Member != "" && c.Subject != "" &&
		format.RefName(c.Revision) &&
		format.RefName(c.Reference) &&
		(c.File == "" || format.FilePath(c.File)) &&
		c.Issued <= now &&
		c.Expires > now &&
		c.Expires-c.Issued == int64(xetTTL/time.Second)
}

func (h handler) xetAuthority(r *http.Request, write bool) (*http.Request, xetClaim, error) {
	forbidden := apierror.New(http.StatusForbidden, "invalid or expired Xet authority")
	var c xetClaim
	if !h.o.XetEnabled || h.o.Authenticator == nil {
		return nil, c, forbidden
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	q := r.URL.Query()
	if q.Has("token") {
		if len(q) != 1 || len(q["token"]) != 1 ||
			!strings.Contains(pkgroute.RequestFrom(r).Rest, "/data/") {
			return nil, c, forbidden
		}
		token = q.Get("token")
	} else if len(q) != 0 {
		return nil, c, forbidden
	}
	c, ok := h.verifyXet(token)
	if !ok || c.Repo != pkgroute.RequestFrom(r).Repo.Key || c.Kind != chi.URLParam(r, "kind") ||
		c.Owner != chi.URLParam(r, "owner") ||
		c.Name != chi.URLParam(r, "name") ||
		c.Revision != chi.URLParam(r, "revision") ||
		write && !c.Write {
		return nil, c, forbidden
	}
	authorized, err := h.restoreXetClaim(r, c, write)
	return authorized, c, err
}

func (h handler) restoreXetClaim(r *http.Request, c xetClaim, write bool) (*http.Request, error) {
	forbidden := apierror.New(http.StatusForbidden, "invalid or expired Xet authority")
	if h.o.Authenticator == nil || c.Write != write || c.Repo != pkgroute.RequestFrom(r).Repo.Key {
		return nil, forbidden
	}
	p, err := h.o.Authenticator.RestoreDownload(r.Context(), c.Subject, c.Credential)
	var d accessapi.Decider
	if err == nil {
		p, d, err = h.o.Authorizer.Authorize(r.Context(), p, p.Anonymous)
	}
	if err != nil {
		return nil, forbidden
	}
	r = r.WithContext(accessapi.WithCaller(r.Context(), p, d))
	id, _ := format.ParseIdentity(c.Kind, c.Owner, c.Name)
	action := types.ActionRead
	if write {
		action = types.ActionWrite
	}
	if !h.k.CanFolder(r, id.Prefix(), action) {
		return nil, forbidden
	}
	if write {
		target, err := h.targetRequest(r, id)
		if err != nil || pkgroute.RequestFrom(target).Repo.Key != c.Member {
			return nil, forbidden
		}
		return target, nil
	}
	if c.Write {
		return nil, forbidden
	}
	return r, nil
}

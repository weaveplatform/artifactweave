package terraform

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const signingPurpose = "terraform-registry-download"

var (
	encoding  = base64.RawURLEncoding
	forbidden = apierror.New(http.StatusForbidden, "Invalid or expired Terraform download URL")
)

type claims struct {
	Subject string `json:"sub"`
	Repo    string `json:"repo"`
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Issued  int64  `json:"iat"`
	Expires int64  `json:"exp"`
}

func (h handler) downloadURL(r *http.Request, it types.Item) (string, error) {
	if h.o.Keys == nil {
		return "", apierror.ErrNotImplemented
	}
	p, _ := auth.FromContext(r.Context())
	now := h.o.Now()
	c := claims{
		p.Name,
		pkgroute.RequestFrom(r).Repo.Key,
		it.FullPath(),
		it.Actual.SHA256,
		now.Unix(),
		now.Add(90 * time.Second).Unix(),
	}
	raw, _ := json.Marshal(c)
	input := encoding.EncodeToString(
		[]byte(`{"alg":"HS256","typ":"JWT"}`),
	) + "." + encoding.EncodeToString(
		raw,
	)
	mac := hmac.New(sha256.New, h.o.Keys.Derive(signingPurpose))
	_, _ = mac.Write([]byte(input))
	token := input + "." + encoding.EncodeToString(mac.Sum(nil))
	segments := strings.Split(c.Path, "/")
	for i := range segments {
		segments[i] = url.PathEscape(segments[i])
	}
	return baseurl.From(
		r.Context(),
	) + prefix + url.PathEscape(
		c.Repo,
	) + "/v1/modules/" + strings.Join(
		segments,
		"/",
	) + "?sig=" + token, nil
}

func (h handler) verify(raw string) (claims, bool) {
	var c claims
	if h.o.Keys == nil {
		return c, false
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return c, false
	}
	header, err := encoding.DecodeString(parts[0])
	var algorithm struct {
		Alg string `json:"alg"`
	}
	if err != nil || json.Unmarshal(header, &algorithm) != nil || algorithm.Alg != "HS256" {
		return c, false
	}
	sig, err := encoding.DecodeString(parts[2])
	if err != nil {
		return c, false
	}
	mac := hmac.New(sha256.New, h.o.Keys.Derive(signingPurpose))
	_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return c, false
	}
	body, err := encoding.DecodeString(parts[1])
	if err != nil || json.Unmarshal(body, &c) != nil {
		return c, false
	}
	now := h.o.Now().Unix()
	return c, c.Subject != "" && c.Expires > now && c.Issued <= now && c.Expires-c.Issued == 90
}

func (h handler) signed(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		h.k.Error(w, r, apierror.New(http.StatusMethodNotAllowed, "method not allowed"))
		return
	}
	q := pkgroute.RequestFrom(r)
	rest, ok := strings.CutPrefix(q.Rest, "v1/modules/")
	path, err := pkgroute.ParsePath(rest)
	c, valid := h.verify(r.URL.Query().Get("sig"))
	if !ok || err != nil || path.Folder || len(path.Props) > 0 || !valid || c.Repo != q.Repo.Key ||
		c.Path != path.String() ||
		q.Repo.TerraformType != repo.TerraformModule {
		h.k.Error(w, r, forbidden)
		return
	}
	f, err := h.k.Item(r, c.Path)
	if errors.Is(err, store.ErrNotFound) || err == nil && f.Item.Folder {
		h.k.Error(w, r, missing())
		return
	}
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if f.Item.Actual.SHA256 != c.SHA256 {
		h.k.Error(w, r, forbidden)
		return
	}
	p, err := h.principal(r, c.Subject)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	r = r.WithContext(auth.WithPrincipal(r.Context(), p))
	if !h.k.Can(r, c.Path, types.ActionRead) {
		h.k.Error(w, r, forbidden)
		return
	}
	h.k.ServeFound(w, r, f)
}

// Re-evaluate the user and permission targets at download time: a URL is
// not a grant that outlives an account or its Read permission.
func (h handler) principal(r *http.Request, name string) (auth.Principal, error) {
	db, ctx := h.k.DB(), r.Context()
	p := auth.Anonymous
	on := false
	if name == auth.AnonymousUser {
		err := db.Settings().Get(ctx, types.SettingAnonymousAccess, &on)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return p, fmt.Errorf("terraform: anonymous setting: %w", err)
		}
	} else {
		u, err := db.Users().Get(ctx, name)
		if errors.Is(err, store.ErrNotFound) ||
			err == nil && u.Status != types.UserEnabled && u.Status != "" {
			return p, forbidden
		}
		if err != nil {
			return p, fmt.Errorf("terraform: user: %w", err)
		}
		p = auth.Principal{Name: u.Name, Admin: u.Admin, Groups: u.Groups}
	}
	p, err := auth.Authorize(
		ctx,
		authz.Sources{Permissions: db.Permissions(), Groups: db.Groups(), AnonymousEnabled: on},
		p,
	)
	if err != nil {
		return p, fmt.Errorf("terraform: authorize download: %w", err)
	}
	return p, nil
}

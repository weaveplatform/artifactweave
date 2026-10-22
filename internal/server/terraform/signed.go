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
	Provider   *providerClaim           `json:"provider,omitempty"`
	Credential *auth.DownloadCredential `json:"credential,omitempty"`
	Source     *types.SourceIdentity    `json:"source,omitempty"`
	Subject    string                   `json:"sub"`
	Repo       string                   `json:"repo"`
	Path       string                   `json:"path"`
	SHA256     string                   `json:"sha256"`
	Issued     int64                    `json:"iat"`
	Expires    int64                    `json:"exp"`
}

func (h handler) downloadURL(r *http.Request, it types.Item) (string, error) {
	if h.o.Keys == nil {
		return "", apierror.ErrNotImplemented
	}
	p, _ := auth.FromContext(r.Context())
	now := h.o.Now()
	c := claims{
		Subject: p.Name,
		Repo:    pkgroute.RequestFrom(r).Repo.Key,
		Path:    it.FullPath(),
		SHA256:  it.Actual.SHA256,
		Issued:  now.Unix(),
		Expires: now.Add(90 * time.Second).Unix(),
	}
	return h.signDownloadURL(r, c)
}

func (h handler) signDownloadURL(r *http.Request, c claims) (string, error) {
	if h.o.Keys == nil {
		return "", apierror.ErrNotImplemented
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
	pathPrefix := downloadPrefix(pkgroute.RequestFrom(r).Repo)
	if c.Provider != nil {
		pathPrefix = "providers/"
	}
	return baseurl.From(
		r.Context(),
	) + prefix + url.PathEscape(
		c.Repo,
	) + "/" + pathPrefix + strings.Join(
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
	c, valid := h.verify(r.URL.Query().Get("sig"))
	pathPrefix := downloadPrefix(q.Repo)
	if q.Repo.Class == repo.Remote && c.Provider != nil {
		pathPrefix = "providers/"
	}
	rest, ok := strings.CutPrefix(q.Rest, pathPrefix)
	path, err := pkgroute.ParsePath(rest)
	if !ok || err != nil || path.Folder || len(path.Props) > 0 || !valid || c.Repo != q.Repo.Key ||
		c.Path != path.String() ||
		(q.Repo.Class != repo.Remote && q.Repo.TerraformType != repo.TerraformModule && q.Repo.TerraformType != repo.TerraformProvider) {
		h.k.Error(w, r, forbidden)
		return
	}
	if q.Repo.Class == repo.Remote {
		if c.Provider != nil {
			h.remoteSignedProvider(w, r, c)
			return
		}
		if !strings.HasPrefix(q.Rest, "v1/modules/") {
			h.k.Error(w, r, apierror.ErrNotImplemented)
			return
		}
		h.remoteSignedModule(w, r, c)
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

func downloadPrefix(c repo.Config) string {
	if c.TerraformType == repo.TerraformProvider {
		return "providers/"
	}
	return "v1/modules/"
}

func (h handler) remoteDownloadURL(
	r *http.Request,
	path, digest string,
	identity types.SourceIdentity,
	subdir, archive string,
) (string, error) {
	p, _ := auth.FromContext(r.Context())
	reference, err := p.DownloadReference()
	if err != nil {
		return "", fmt.Errorf("terraform download identity: %w", err)
	}
	now := h.o.Now()
	c := claims{
		Subject:    p.Name,
		Repo:       pkgroute.RequestFrom(r).Repo.Key,
		Path:       path,
		SHA256:     digest,
		Issued:     now.Unix(),
		Expires:    now.Add(90 * time.Second).Unix(),
		Source:     &identity,
		Credential: &reference,
	}
	raw, err := h.signDownloadURL(r, c)
	if err != nil {
		return "", err
	}
	u, _ := url.Parse(raw)
	q := u.Query()
	q.Set("archive", archive)
	u.RawQuery = q.Encode()
	// Getter selectors are a source-language suffix, not a URL path: percent
	// escaping '*' would turn a glob into a literal directory name in Terraform.
	if subdir != "" {
		head, query, _ := strings.Cut(u.String(), "?")
		return head + "//" + subdir + "?" + query, nil
	}
	return u.String(), nil
}

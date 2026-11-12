package terraformhttp

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

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	tf "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/terraform"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/baseurl"
)

const signingPurpose = "terraform-registry-download"

var (
	encoding  = base64.RawURLEncoding
	forbidden = apierror.New(http.StatusForbidden, "Invalid or expired Terraform download URL")
)

type claims struct {
	Provider *providerClaim `json:"provider,omitempty"`
	// Module is the remote module version a remote module URL serves,
	// <ns>/<name>/<system>/<version>: its archive's cache path follows the
	// upstream location, so Path does not name it.
	Module     string                     `json:"module,omitempty"`
	Credential *kernel.DownloadCredential `json:"credential,omitempty"`
	Source     *model.SourceIdentity      `json:"source,omitempty"`
	Subject    string                     `json:"sub"`
	Repo       string                     `json:"repo"`
	Path       string                     `json:"path"`
	SHA256     string                     `json:"sha256"`
	Issued     int64                      `json:"iat"`
	Expires    int64                      `json:"exp"`
}

func (h handler) downloadURL(r *http.Request, it model.Item) (string, error) {
	if h.o.Keys == nil {
		return "", apierror.ErrNotImplemented
	}
	p, _ := kernel.FromContext(r.Context())
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
	// The lookup leaves the download policy to ServeFound, which applies it
	// after the URL's principal is rechecked: a principal that lost Read
	// learns nothing of how the repository is set (DV-057), and a
	// blacked-out repository or a path its patterns reject answers the
	// policy's own 404 and message (R6, R9).
	f, err := h.k.StoredItem(r, c.Path)
	if errors.Is(err, kernel.ErrNotFound) || err == nil && f.Item.Folder {
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
	p, d, err := h.principal(r, c.Subject)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	r = r.WithContext(accessapi.WithCaller(r.Context(), p, d))
	if !h.k.Can(r, c.Path, accessapi.ActionRead) {
		h.k.Error(w, r, forbidden)
		return
	}
	h.k.ServeFound(w, r, f)
}

// Re-evaluate the user and permission targets at download time: a URL is
// not a grant that outlives an account or its Read permission.
func (h handler) principal(
	r *http.Request,
	name string,
) (kernel.Principal, accessapi.Decider, error) {
	ctx := r.Context()
	p := kernel.Anonymous
	on := false
	if name == kernel.AnonymousUser {
		var err error
		on, err = h.o.AnonymousAccess.Enabled(ctx)
		if err != nil && !errors.Is(err, kernel.ErrNotFound) {
			return p, nil, fmt.Errorf("terraform: anonymous setting: %w", err)
		}
	} else {
		if err := h.o.Restrictions.Check(ctx, name, time.Now()); err != nil {
			return p, nil, fmt.Errorf("terraform: restricted download: %w", err)
		}
		u, err := h.o.Identity.User(ctx, name)
		if errors.Is(err, kernel.ErrNotFound) || err == nil && !u.Enabled() {
			return p, nil, forbidden
		}
		if err != nil {
			return p, nil, fmt.Errorf("terraform: user: %w", err)
		}
		p = kernel.Principal{Name: u.Name, Admin: u.Admin, Groups: u.Groups}
	}
	p, d, err := h.o.Authorizer.Authorize(ctx, p, on)
	if err != nil {
		return p, nil, fmt.Errorf("terraform: authorize download: %w", err)
	}
	return p, d, nil
}

func downloadPrefix(c repo.Config) string {
	if c.TerraformType == repo.TerraformProvider {
		return "providers/"
	}
	return "v1/modules/"
}

func (h handler) remoteDownloadURL(
	r *http.Request,
	m tf.Module,
	path, digest string,
	identity model.SourceIdentity,
	subdir, archive string,
) (string, error) {
	p, _ := kernel.FromContext(r.Context())
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
		Module:     m.Package() + "/" + m.Version,
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

package terraformbackend

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
	tfbe "github.com/weaveplatform/artifactweave/internal/format/terraformbackend"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/service/terraformstate"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const (
	uploadPurpose  = "terraform-backend-upload"
	uploadLifetime = 10 * time.Minute
)

var (
	forbidden = apierror.New(403, "Invalid or expired Terraform state upload URL")
	encoding  = base64.RawURLEncoding
)

type claims struct {
	ID      string `json:"id"`
	Subject string `json:"sub"`
	MD5     string `json:"md5"`
	Serial  int64  `json:"serial"`
	Record  int64  `json:"record"`
	Created int64  `json:"created"`
	Issued  int64  `json:"iat"`
	Expires int64  `json:"exp"`
	// Keep a token's original ceiling: looking up only its user's current
	// permissions would silently turn a scoped capability into a full grant.
	Scope    *string             `json:"scope,omitempty"`
	Identity types.TokenIdentity `json:"identity,omitempty"`
}

func (h handler) uploadURL(
	r *http.Request,
	st terraformstate.State,
	ws terraformstate.Workspace,
	p auth.Principal,
	md5 string,
) (string, error) {
	identity, err := h.capabilityIdentity(r, p)
	if err != nil {
		return "", err
	}
	now := h.o.Now()
	expiry := now.Add(uploadLifetime)
	if !p.TokenExpires.IsZero() && p.TokenExpires.Before(expiry) {
		expiry = p.TokenExpires
	}
	c := claims{
		ID:       st.ID(),
		Subject:  p.Name,
		MD5:      md5,
		Serial:   st.Meta.Serial,
		Record:   ws.RecordID,
		Created:  ws.Created.UnixMilli(),
		Issued:   now.Unix(),
		Expires:  expiry.Unix(),
		Scope:    scopeText(p),
		Identity: identity,
	}
	raw, _ := json.Marshal(c)
	body := encoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, h.o.Keys.Derive(uploadPurpose))
	_, _ = mac.Write([]byte(body))
	token := body + "." + encoding.EncodeToString(mac.Sum(nil))
	return baseurl.From(
		r.Context(),
	) + Prefix + "/state-versions/" + url.PathEscape(
		st.ID(),
	) + "/content?token=" + token, nil
}

func (h handler) verify(raw, id string) (claims, bool) {
	var c claims
	if h.o.Keys == nil {
		return c, false
	}
	body, sig, ok := strings.Cut(raw, ".")
	if !ok {
		return c, false
	}
	signature, err := encoding.DecodeString(sig)
	if err != nil {
		return c, false
	}
	mac := hmac.New(sha256.New, h.o.Keys.Derive(uploadPurpose))
	_, _ = mac.Write([]byte(body))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return c, false
	}
	data, err := encoding.DecodeString(body)
	if err != nil || json.Unmarshal(data, &c) != nil {
		return c, false
	}
	now := h.o.Now().Unix()
	return c, c.ID == id && c.Subject != "" && c.Issued <= now && c.Expires > now &&
		c.Expires-c.Issued <= int64(uploadLifetime/time.Second)
}

func (h handler) upload(_ http.ResponseWriter, r *http.Request) (result, error) {
	cl, ok := h.verify(r.URL.Query().Get("token"), parameter(r, "id"))
	if !ok {
		return result{}, forbidden
	}
	id, ms, ok := tfbe.SplitStateID(cl.ID)
	if !ok {
		return result{}, forbidden
	}
	c, ws, err := h.states.ResolveWorkspace(r.Context(), id)
	if err != nil {
		return result{}, err //nolint:wrapcheck // storage errors retain their typed API status
	}
	if ws.Created.UnixMilli() != cl.Created || ws.RecordID != cl.Record {
		return result{}, forbidden
	}
	if err := h.authorizeUpload(r, c, ws.Name, cl); err != nil {
		return result{}, err //nolint:wrapcheck // storage errors retain their typed API status
	}
	_, err = h.states.WriteState(r.Context(), c, ws.Name, terraformstate.StateWrite{
		Ms: ms, Body: r.Body, MD5: cl.MD5, Serial: cl.Serial, By: cl.Subject,
	})
	return result{
		status: 200,
	}, err //nolint:wrapcheck // storage errors retain their typed API status
}

func (h handler) authorizeUpload(r *http.Request, c repo.Config, name string, cl claims) error {
	db, ctx := h.o.Items.Store, r.Context()
	u, err := h.uploadUser(r, cl)
	if err != nil {
		return err
	}

	var scope *types.Scope
	if cl.Scope != nil {
		parsed, err := types.ParseScope(*cl.Scope)
		if err != nil {
			return forbidden
		}
		scope = &parsed
	}
	subject := authz.Subject{Name: u.Name, Admin: u.Admin, Groups: u.Groups, Scope: scope}
	// Named-group tokens act through their named groups even when the user
	// is not a member, as auth.Principal.Subject does for bearer requests.
	if scope != nil {
		for _, entry := range scope.Entries {
			if entry.Kind == types.ScopeGroups {
				subject.Groups = append(subject.Groups, entry.Names...)
			}
		}
	}
	access, err := authz.New(
		ctx,
		authz.Sources{Permissions: db.Permissions(), Groups: db.Groups()},
		subject,
	)
	if err != nil {
		return fmt.Errorf("terraformbackend: upload permissions: %w", err)
	}
	if !access.Allows(
		types.ResourceArtifact,
		authz.Repo{Key: c.Key, Class: string(c.Class)},
		tfbe.LatestPath(name),
		types.ActionWrite,
	) {
		return forbidden
	}
	return nil
}

func scopeText(p auth.Principal) *string {
	if scope := p.Subject().Scope; scope != nil {
		text := scope.String()
		return &text
	}
	return nil
}

// Capability identities follow the same resolution as access tokens: a
// synthetic identity never becomes a same-named stored account; a transient
// one uses the current auto-join groups only while no stored account exists.
func (h handler) uploadUser(r *http.Request, cl claims) (types.User, error) {
	if cl.Identity == types.IdentitySynthetic {
		return types.User{Name: cl.Subject}, nil
	}
	db, ctx := h.o.Items.Store, r.Context()
	u, err := db.Users().Get(ctx, cl.Subject)
	if errors.Is(err, store.ErrNotFound) && cl.Identity == types.IdentityTransient {
		groups, err := db.Groups().AutoJoin(ctx)
		if err != nil {
			return u, fmt.Errorf("terraformbackend: upload auto-join groups: %w", err)
		}
		return types.User{Name: cl.Subject, Groups: groups}, nil
	}
	if errors.Is(err, store.ErrNotFound) ||
		err == nil && u.Status != "" && u.Status != types.UserEnabled {
		return u, forbidden
	}
	if err != nil {
		return u, fmt.Errorf("terraformbackend: upload user: %w", err)
	}
	return u, nil
}

func (h handler) capabilityIdentity(
	r *http.Request,
	p auth.Principal,
) (types.TokenIdentity, error) {
	if p.Identity != types.IdentityTransient {
		return p.Identity, nil
	}
	_, err := h.o.Items.Store.Users().Get(r.Context(), p.Name)
	if errors.Is(err, store.ErrNotFound) {
		return types.IdentityTransient, nil
	}
	if err != nil {
		return "", fmt.Errorf("terraformbackend: capability identity: %w", err)
	}
	// A capability issued for a stored account stops when that account is
	// deleted. It must not silently become a transient-user capability.
	return types.IdentityStored, nil
}

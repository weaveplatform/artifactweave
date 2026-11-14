package access_test

// This file holds the tests of the legacy identity lists that stream from
// store pages (#1409, decision 0043 "Streaming complete lists"): that each
// answers, on every backend and over several store pages, the bytes the
// collected handler wrote, and how a store failure before and after the
// first byte ends the answer.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/access/core/token"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/sql/postgres"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/sql/sqlite"
	appstoretest "github.com/weaveplatform/artifactweave/internal/app/storetest"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	accessgen "github.com/weaveplatform/artifactweave/internal/platform/gen/access"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	pgdrv "github.com/weaveplatform/artifactweave/internal/platform/sql/postgres"
	platsqltest "github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

// streamedBase is the public base URL the streamed lists' detail URLs use.
const streamedBase = "https://reg.example"

// The seeded sizes cross the store pages each list reads: 100 users or
// groups, 500 permissions or token records.
const (
	streamedUsers       = 230
	streamedGroups      = 120
	streamedPermissions = 510
	streamedTokens      = 520
)

// streamedWorld is a router over one backend, seeded so every streamed
// identity list spans several store pages, with the store it reads.
type streamedWorld struct {
	h      http.Handler
	db     userStore
	tokens *token.Service
}

func newStreamedWorld(t *testing.T, backend string) streamedWorld {
	t.Helper()
	ctx := t.Context()
	var db userStore
	switch backend {
	case "inmem":
		db = inmem.New()
	case "sqlite":
		s, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "lists.db"), sqlite.Options{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		db = s
	default:
		s, err := postgres.Open(ctx, platsqltest.PostgresDSN(t), postgres.Options{MaxOpenConns: 8})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		appstoretest.Reset(t, s.DB(), pgdrv.Dialect)
		db = s
	}
	support.Seed(t, db)
	if err := db.Instance().Init(ctx, instance.InstanceIdentity{
		InstanceID: "jfrt@01234567-89ab-4cde-8f01-23456789abcd", ProtocolVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	tokens, err := token.New(ctx, db, token.Options{KeyBits: 1024})
	if err != nil {
		t.Fatal(err)
	}
	seedStreamedLists(t, db, tokens)
	a, err := authn.NewAuthenticator(db.Users(), db.Settings(), authn.Options{
		Hash: testHash, Tokens: tokens,
	})
	if err != nil {
		t.Fatal(err)
	}
	return streamedWorld{
		h: New(Config{
			BaseURL: streamedBase, CompatVersion: "7.155.0", Store: db, Auth: a,
			PasswordHash: testHash, Tokens: tokens,
		}),
		db:     db,
		tokens: tokens,
	}
}

// seedStreamedLists adds users (every seventh locked, one with characters
// json.Encoder escapes), groups, permissions (every fiftieth managed by
// s001) and token records (some expired, some not revocable, ten s001's).
func seedStreamedLists(t *testing.T, db userStore, tokens *token.Service) {
	t.Helper()
	ctx := context.Background()
	names := []string{"x<&>y"}
	for i := range streamedUsers {
		names = append(names, fmt.Sprintf("s%03d", i))
	}
	for i, n := range names {
		if err := db.Users().
			Create(ctx, accesstest.User{Name: n, PasswordHash: cheapHash}); err != nil {
			t.Fatal(err)
		}
		if i%7 == 0 {
			if _, err := db.Users().RecordFailedLogin(ctx, n, 1); err != nil {
				t.Fatal(err)
			}
		}
	}
	for i := range streamedGroups {
		if err := db.Groups().
			Create(ctx, accesstest.Group{Name: fmt.Sprintf("g%03d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	for i := range streamedPermissions {
		grants := map[string]api.Actions{"s002": api.ActionRead}
		if i%50 == 0 {
			grants["s001"] = api.ActionRead | api.ActionManage
		}
		if err := db.Permissions().Create(ctx, accesstest.Permission{
			Name: fmt.Sprintf("p%03d", i),
			Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
				Actions: accesstest.Grants{Users: grants},
				Targets: map[string]accesstest.Target{"ANY LOCAL": {}},
			}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	issued := time.Now().Add(-time.Hour).Truncate(time.Second)
	for i := range streamedTokens {
		subject, scope := "admin", "applied-permissions/admin"
		if i%52 == 0 {
			subject, scope = "s001", "applied-permissions/user"
		}
		sc, err := kernel.ParseScope(scope)
		if err != nil {
			t.Fatal(err)
		}
		rec := accesstest.Token{
			ID: fmt.Sprintf("t%04d", i), Subject: tokens.Issuer() + "/users/" + subject,
			Scope: sc, IssuedAt: issued.Add(time.Duration(i) * time.Second),
			Revocable: i%11 != 0, Refreshable: i%3 == 0,
			AccessDigest:    []byte(fmt.Sprintf("access-%d", i)),
			RefreshDigest:   []byte(fmt.Sprintf("refresh-%d", i)),
			ReferenceDigest: []byte(fmt.Sprintf("reference-%d", i)),
		}
		if i%13 == 0 {
			rec.ExpiresAt = issued.Add(-time.Minute)
		} else if i%2 == 0 {
			rec.ExpiresAt = issued.Add(24 * time.Hour)
		}
		if err := db.Tokens().Create(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}
}

// collect reads every page of a store list, as the collected handlers did.
func collect[T any](
	t *testing.T,
	list func(context.Context, kernel.Page) ([]T, string, error),
	size int,
) []T {
	t.Helper()
	var out []T
	p := kernel.Page{Limit: size}
	for {
		rows, next, err := list(context.Background(), p)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, rows...)
		if next == "" {
			return out
		}
		p.After = next
	}
}

// collectedBody is the collected handlers' body: json.Encoder over the whole
// document.
func collectedBody(t *testing.T, v any) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(v); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func segment(name string) string {
	return strings.ReplaceAll(url.PathEscape(name), "+", "%2B")
}

func sp(s string) *string { return &s }

// TestAccStreamedIdentityListsAcrossBackends: each streamed legacy list
// answers, on every backend and over several store pages, the body the
// collected handler built from the same store, for the administrator and
// for a caller who sees part of the list.
func TestAccStreamedIdentityListsAcrossBackends(t *testing.T) {
	for _, backend := range []string{"inmem", "sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			w := newStreamedWorld(t, backend)
			for _, c := range streamedCases(t, w) {
				rec := do(t, w.h, http.MethodGet, c.path, c.as, "")
				if rec.Code != http.StatusOK || rec.Body.String() != c.want {
					t.Fatalf("%s: %d\n got %.300s\nwant %.300s", c.path, rec.Code, rec.Body, c.want)
				}
				if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
					t.Fatalf("%s: Content-Type %q", c.path, ct)
				}
			}
		})
	}
}

type streamedCase struct {
	path string
	as   map[string]string
	want string
}

// streamedCases are the lists and the bodies the collected handlers built.
func streamedCases(t *testing.T, w streamedWorld) []streamedCase {
	t.Helper()
	admin, s001 := basic("admin", "pw"), basic("s001", "pw")
	users := []artgen.UserListItem{}
	for _, u := range collect(t, w.db.Users().List, 0) {
		users = append(users, artgen.UserListItem{
			Name: sp(u.Name), Realm: sp(u.Realm),
			Uri: sp(streamedBase + secUsers + "/" + segment(u.Name)),
		})
	}
	groups := []artgen.GroupListItem{}
	for _, g := range collect(t, w.db.Groups().List, 0) {
		groups = append(groups, artgen.GroupListItem{
			Name: sp(g.Name), Uri: sp(streamedBase + secGroups + "/" + segment(g.Name)),
		})
	}
	locked, err := w.db.Users().Locked(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	perms := collect(t, w.db.Permissions().List, 500)
	v1 := []artgen.PermissionTargetListItem{}
	var managed []artgen.PermissionTargetListItem
	for _, p := range perms {
		v1 = append(v1, artgen.PermissionTargetListItem{
			Name: sp(p.Name), Uri: sp(streamedBase + permV1 + "/" + segment(p.Name)),
		})
		var n int
		if _, err := fmt.Sscanf(p.Name, "p%03d", &n); err == nil && n%50 == 0 {
			managed = append(managed, artgen.PermissionTargetListItem{
				Name: sp(p.Name), Uri: sp(streamedBase + permV2 + "/" + segment(p.Name)),
			})
		}
	}
	v2 := make([]artgen.PermissionTargetListItem, len(v1))
	for i, p := range perms {
		v2[i] = artgen.PermissionTargetListItem{
			Name: sp(p.Name), Uri: sp(streamedBase + permV2 + "/" + segment(p.Name)),
		}
	}
	now := time.Now()
	recs := collect(
		t,
		func(ctx context.Context, p kernel.Page) ([]accesstest.Token, string, error) {
			return w.db.Tokens().List(ctx, accesstest.TokenFilter{}, p)
		},
		500,
	)
	all, own, legacy := []accessgen.TokenDetails{}, []accessgen.TokenDetails{}, []artgen.LegacyTokenDetails{}
	scoped := []accessgen.TokenDetails{}
	for _, r := range recs {
		if !r.Revocable || r.Expired(now) {
			continue
		}
		d := accessgen.TokenDetails{
			TokenId: r.ID, Subject: r.Subject, Issuer: w.tokens.Issuer(), Scope: r.Scope.String(),
			IssuedAt: int(r.IssuedAt.Unix()), Refreshable: r.Refreshable,
		}
		if !r.ExpiresAt.IsZero() {
			expiry := int(r.ExpiresAt.Unix())
			d.Expiry = &expiry
		}
		all = append(all, d)
		if strings.HasSuffix(r.Subject, "/users/s001") {
			own = append(own, d)
			scoped = append(scoped, d)
		}
		legacy = append(legacy, artgen.LegacyTokenDetails{
			TokenId: &d.TokenId, Subject: &d.Subject, Issuer: &d.Issuer,
			IssuedAt: &d.IssuedAt, Expiry: d.Expiry, Refreshable: &d.Refreshable,
		})
	}
	return []streamedCase{
		{secUsers, admin, collectedBody(t, users)},
		{secGroups, admin, collectedBody(t, groups)},
		{"/artifactory/api/security/lockedUsers", admin, collectedBody(t, locked)},
		{permV1, admin, collectedBody(t, v1)},
		{permV2, admin, collectedBody(t, v2)},
		{permV2, s001, collectedBody(t, managed)},
		{
			"/access/api/v1/tokens",
			admin,
			collectedBody(t, accessgen.TokenListResponse{Tokens: all}),
		},
		{"/access/api/v1/tokens", s001, collectedBody(t, accessgen.TokenListResponse{Tokens: own})},
		{
			"/access/api/v1/tokens?scope=" + url.QueryEscape("applied-permissions/user"), admin,
			collectedBody(t, accessgen.TokenListResponse{Tokens: scoped}),
		},
		{legacyTokenPath, admin, collectedBody(t, artgen.LegacyTokenList{Tokens: &legacy})},
	}
}

// userFaults fails UserStore.List after letting skip calls through.
type userFaults struct {
	*inmem.Store
	skip *int
}

func (s userFaults) Users() accesstest.UserStore {
	return faultyUserList{s.Store.Users(), s.skip}
}

type faultyUserList struct {
	accesstest.UserStore
	skip *int
}

func (u faultyUserList) List(
	ctx context.Context,
	p kernel.Page,
) ([]accesstest.User, string, error) {
	if *u.skip == 0 {
		return nil, "", errBoom
	}
	*u.skip--
	return u.UserStore.List(ctx, p) //nolint:wrapcheck // test double
}

// TestAccStreamedListStoreFaults: a store failure on the first page answers
// main's 500 in the list's envelope; one after the first page has gone out
// aborts the answer, so the client reads a body that is cut short and that
// no JSON parser accepts, and the access log has the cause.
func TestAccStreamedListStoreFaults(t *testing.T) {
	db := inmem.New()
	cheapSeed(t, db)
	for i := range streamedUsers {
		if err := db.Users().Create(context.Background(),
			accesstest.User{Name: fmt.Sprintf("s%03d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	a, err := authn.NewAuthenticator(db.Users(), db.Settings(), authn.Options{Hash: testHash})
	if err != nil {
		t.Fatal(err)
	}
	skip := 0
	var logs bytes.Buffer
	h := New(Config{
		CompatVersion: "7.155.0", Store: userFaults{Store: db, skip: &skip}, Auth: a,
		Logger: slog.New(slog.NewJSONHandler(&logs, nil)),
	})
	// The authenticator reads the admin through Get, never List, so the
	// first List is the handler's first page.
	rec := do(t, h, http.MethodGet, secUsers, basic("admin", "pw"), "")
	if rec.Code != http.StatusInternalServerError ||
		strings.TrimSpace(
			rec.Body.String(),
		) != `{"errors":[{"message":"internal error","status":500}]}` {
		t.Fatalf("failure before the first byte: %d %s", rec.Code, rec.Body)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	skip = 1
	logs.Reset()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+secUsers, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth("admin", "pw")
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || err == nil ||
		!bytes.HasPrefix(body, []byte(`[{"name":`)) {
		t.Fatalf(
			"failure after the first byte: %d, %d bytes, read error %v",
			res.StatusCode,
			len(body),
			err,
		)
	}
	var v any
	if json.Unmarshal(body, &v) == nil {
		t.Fatal("the cut body parses as JSON")
	}
	if line := logs.String(); !strings.Contains(line, `"aborted":true`) ||
		!strings.Contains(line, "boom") {
		t.Fatalf("access log: %s", line)
	}
}

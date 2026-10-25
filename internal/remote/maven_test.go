package remote

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/service/work"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/layout"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func mavenFixture(t *testing.T) (*policyWorld, *MavenBackend) {
	t.Helper()
	w := policyFixture(t)
	w.c = *repo.Defaults(&repo.Config{Key: w.c.Key, Class: repo.Remote, PackageType: repo.Maven, URL: w.up.URL})
	if err := w.db.Repos().Update(t.Context(), w.c); err != nil {
		t.Fatal(err)
	}
	w.permit.Identity, _ = w.db.RemoteCache().Source(t.Context(), w.c.Key)
	m := &MavenBackend{Policy: w.p}
	w.p.PrepareUpload = m.PrepareUpload
	return w, m
}

func TestAccMavenSourceAdmissionFaults(t *testing.T) {
	for _, tc := range []struct {
		name     string
		resource fakeupstream.Resource
	}{
		{"malformed", fakeupstream.Resource{Body: []byte("invalid")}},
		{"empty", fakeupstream.Resource{}},
		{"oversize", fakeupstream.Resource{Body: []byte(strings.Repeat("0", 1025))}},
		{"upstream failure", fakeupstream.Resource{Status: 503}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, m := mavenFixture(t)
			p := "org/acme/app/1.0/app-1.0.jar"
			w.up.Set("/"+p, fakeupstream.Resource{Body: []byte("artifact")})
			w.up.Set("/"+p+".sha1", tc.resource)
			if _, err := m.Read(t.Context(), w.request(p, source.Artifact), w.permit); err == nil {
				t.Fatal("bad checksum admitted")
			}
			if _, err := w.db.Items().
				Get(t.Context(), w.c.Key, true, p); !errors.Is(
				err,
				store.ErrNotFound,
			) {
				t.Fatal("failed admission published", err)
			}
		})
	}
}

func TestAccMavenMetadataIdentityAndEquivalentLayouts(t *testing.T) {
	w, m := mavenFixture(t)
	p := "org/acme/app/maven-metadata.xml"
	q := w.request(p, source.Artifact)
	q.Resource.Accept = "application/xml, */*"
	q.Resource.EscapedPath = "org/acme/app/maven%2Dmetadata.xml"
	first, err := m.prepare(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	q.Resource.Accept = ""
	q.Resource.EscapedPath = p
	second, err := m.prepare(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	if first.Resource.Accept != second.Resource.Accept ||
		first.Resource.EscapedPath != second.Resource.EscapedPath {
		t.Fatal("client-specific metadata cache identity")
	}
	l, err := layout.Lookup(repo.LayoutMaven2)
	if err != nil {
		t.Fatal(err)
	}
	d := l.Definition()
	d.Name = "copy"
	copyLayout, err := layout.Compile(d)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := mavenUpstreamPath(l, copyLayout, p); err != nil || got != p {
		t.Fatal(got, err)
	}
	other, err := layout.Lookup("ivy-default")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = mavenUpstreamPath(l, other, p); err == nil {
		t.Fatal("unrepresentable metadata translated")
	}
}

func TestAccMavenDurableCompanionAuthorityAndRetries(t *testing.T) {
	for _, change := range []string{"restart", "permission revoked", "password changed", "token revoked", "token expired", "source changed", "credential changed", "credential raced", "offline", "failure"} {
		t.Run(change, func(t *testing.T) {
			w, m := mavenFixture(t)
			w.c.FetchJarsEagerly = new(true)
			if err := w.db.Repos().Update(t.Context(), w.c); err != nil {
				t.Fatal(err)
			}
			w.permit.Identity, _ = w.db.RemoteCache().Source(t.Context(), w.c.Key)
			hash, err := auth.HashPasswordWith(
				auth.HashParams{Time: 1, MemoryKiB: 8, Threads: 1},
				"test-password",
			)
			if err != nil {
				t.Fatal(err)
			}
			if err = w.db.Users().
				Create(t.Context(), types.User{Name: "reader", PasswordHash: hash}); err != nil {
				t.Fatal(err)
			}
			if err = w.db.Permissions().
				Create(t.Context(), types.Permission{Name: "read", Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {Targets: map[string]types.Target{w.c.Key: {}}, Actions: types.Grants{Users: map[string]types.Actions{"reader": types.ActionRead}}}}}); err != nil {
				t.Fatal(err)
			}
			a, err := auth.NewAuthenticator(
				w.db.Users(),
				w.db.Settings(),
				auth.Options{
					Hash: auth.HashParams{Time: 1, MemoryKiB: 8, Threads: 1},
					Now:  w.clock.Now,
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			p, err := a.Basic(t.Context(), "reader", "test-password")
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(change, "token") {
				scope, parseErr := types.ParseScope("artifact:remote:r")
				if parseErr != nil {
					t.Fatal(parseErr)
				}
				if err = w.db.Tokens().
					Create(t.Context(), types.Token{ID: "token", Subject: "jfac@test/users/reader", Scope: scope, ExpiresAt: w.clock.Now().Add(time.Hour)}); err != nil {
					t.Fatal(err)
				}
				p, err = a.Current(
					t.Context(),
					auth.Principal{Name: "reader", TokenID: "token"},
					w.db.Tokens(),
				)
				if err != nil {
					t.Fatal(err)
				}
			}
			service := &source.Service{
				HTTP:  m,
				Cache: m,
				Resolve: func(ctx context.Context, c repo.Config) (types.SourceIdentity, error) {
					return w.db.RemoteCache().Source(ctx, c.Key)
				},
			}
			q, err := work.New(
				w.db,
				work.Options{
					MaxAttempts: 3,
					Now:         w.clock.Now,
					Backoff:     func(int) time.Duration { return 0 },
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			e := &MavenEager{policy: w.p, service: service, auth: a, queue: q}
			if err = q.Handle(MavenCompanionKind, e.run); err != nil {
				t.Fatal(err)
			}
			request := w.request("org/acme/app/1.0/app-1.0.pom", source.Artifact)
			ctx := auth.WithPrincipal(t.Context(), p)
			e.Queue(ctx, request, w.permit)
			e.Queue(ctx, request, w.permit)
			// The serialized row alone is sufficient for a fresh executor; the original
			// request context and all cached grants are deliberately discarded.
			stats, err := w.db.WorkQueue().Stats(t.Context())
			if err != nil || len(stats) != 1 || stats[0].Pending != 1 {
				t.Fatal("not deduplicated", stats, err)
			}
			rows, err := w.db.WorkQueue().
				Lease(t.Context(), store.WorkLease{Kinds: []string{MavenCompanionKind}, Holder: "inspect", Limit: 1, Now: time.Now().Add(time.Minute), Until: time.Now().Add(2 * time.Minute)})
			if err != nil || len(rows) != 1 {
				t.Fatal(rows, err)
			}
			saved := rows[0]
			if strings.Contains(string(saved.Payload), "test-password") ||
				strings.Contains(string(saved.Payload), "Admin") {
				t.Fatal("persisted credential or grants")
			}
			if err = w.db.WorkQueue().
				Release(t.Context(), saved.Kind, saved.Key, saved.Generation, w.clock.Now()); err != nil {
				t.Fatal(err)
			}
			p = auth.Principal{}
			ctx = t.Context()
			target := "/org/acme/app/1.0/app-1.0.jar"
			w.up.Set(target, fakeupstream.Resource{Body: []byte("jar")})
			switch change {
			case "permission revoked":
				err = w.db.Permissions().Delete(ctx, "read")
			case "password changed":
				u, _ := w.db.Users().Get(ctx, "reader")
				u.PasswordHash = []byte("different")
				err = w.db.Users().Update(ctx, u)
			case "token revoked":
				err = w.db.Tokens().Delete(ctx, "token")
			case "token expired":
				w.clock.Advance(2 * time.Hour)
			case "source changed":
				w.c.URL += "/changed"
				err = w.db.Repos().Update(ctx, w.c)
			case "credential changed":
				w.p.Options = func(context.Context, repo.Config) (Options, error) { return Options{Revision: "changed"}, nil }
			case "credential raced":
				calls := 0
				w.p.Options = func(context.Context, repo.Config) (Options, error) {
					calls++
					revision := "credential-1"
					if calls > 1 {
						revision = "changed"
					}
					return Options{Revision: revision}, nil
				}
			case "offline":
				w.c.Offline = new(true)
				err = w.db.Repos().Update(ctx, w.c)
			case "failure":
				w.up.Set(target, fakeupstream.Resource{Status: http.StatusServiceUnavailable})
			}
			if err != nil {
				t.Fatal(err)
			}
			resumed, err := work.New(
				w.db,
				work.Options{
					MaxAttempts: 3,
					Now:         w.clock.Now,
					Backoff:     func(int) time.Duration { return 0 },
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			restored := &MavenEager{policy: w.p, service: service, auth: a, queue: resumed}
			if err = resumed.Handle(MavenCompanionKind, restored.run); err != nil {
				t.Fatal(err)
			}
			for range 4 {
				if _, err = resumed.RunDue(ctx, MavenCompanionKind); err != nil {
					t.Fatal(err)
				}
			}
			record, err := w.db.WorkQueue().Get(ctx, saved.Kind, saved.Key)
			if change == "restart" {
				if !errors.Is(err, store.ErrNotFound) || w.up.Count("GET", target) != 1 {
					t.Fatal("durable work did not resume", err, w.up.Requests())
				}
			} else {
				if err != nil || record.State != types.WorkParked {
					t.Fatal("failed job not visible", record.State, err)
				}
				want := 1
				if change == "failure" {
					want = 3
				}
				if record.Attempts != want {
					t.Fatal("attempt bound", record.Attempts, want)
				}
				if change != "failure" && w.up.Count("", "") != 0 {
					t.Fatal("revoked job reached upstream", w.up.Requests())
				}
			}
		})
	}
}

func TestUnitMavenCompanionReferenceDecoding(t *testing.T) {
	w, m := mavenFixture(t)
	e := &MavenEager{policy: w.p, service: &source.Service{HTTP: m}}
	if err := e.run(
		t.Context(),
		types.WorkItem{Payload: []byte("bad JSON")},
	); !errors.Is(
		err,
		work.ErrPermanent,
	) {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(mavenJob{Repo: "deleted"})
	if err := e.run(
		t.Context(),
		types.WorkItem{Payload: payload},
	); !errors.Is(
		err,
		source.ErrSuperseded,
	) {
		t.Fatal(err)
	}
}

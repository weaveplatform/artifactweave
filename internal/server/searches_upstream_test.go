package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/internal/remote"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type upstreamSearchWorld struct {
	*itemWorld
	up    *fakeupstream.Server
	clock *fakeupstream.Clock
	svc   *source.Service
	late  func(source.Request)
}

func searchUpstreamWorld(t *testing.T) *upstreamSearchWorld {
	t.Helper()
	u := &upstreamSearchWorld{
		clock: fakeupstream.NewClock(time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)),
	}
	u.up = fakeupstream.New(
		t,
		fakeupstream.Options{
			Now:  u.clock.Now,
			Auth: fakeupstream.Auth{Username: "upstream", Password: "upstream-secret"},
		},
	)
	u.itemWorld = newItemWorld(t, false, func(c *Config) {
		policy := &remote.Policy{
			Store: c.Store, Blobs: c.Blobs, Items: &item.Service{Store: c.Store, Blobs: c.Blobs},
			Pool: new(remote.Pool), Key: []byte("search-test-key"), Now: u.clock.Now,
			Options: func(context.Context, repo.Config) (remote.Options, error) {
				return remote.Options{
					Revision: "test-credentials",
					CheckURL: func(_ context.Context, target *url.URL) error {
						if target.Scheme+"://"+target.Host != u.up.URL {
							return remote.ErrRedirect
						}
						return nil
					},
					Prepare: func(r *http.Request) error {
						r.SetBasicAuth("upstream", "upstream-secret")
						return nil
					},
				}, nil
			},
		}
		t.Cleanup(policy.Pool.Close)
		backend := &remote.MavenBackend{Policy: policy}
		u.svc = &source.Service{
			Resolve: func(ctx context.Context, r repo.Config) (types.SourceIdentity, error) {
				return c.Store.RemoteCache().Source(ctx, r.Key)
			},
			Revalidate: func(ctx context.Context, q source.Request, permit source.Permit) error {
				current, err := c.Store.RemoteCache().Source(ctx, q.Repo.Key)
				if err != nil || current != permit.Identity {
					return source.ErrSuperseded
				}
				p, _ := auth.FromContext(ctx)
				p, err = c.Auth.Current(ctx, p, c.Store.Tokens())
				if err != nil {
					return source.ErrDenied
				}
				p, err = auth.Authorize(
					ctx,
					authz.Sources{Permissions: c.Store.Permissions(), Groups: c.Store.Groups()},
					p,
				)
				if err != nil {
					return source.ErrUnavailable
				}
				full, allows := q.Resource.Path, p.Access().Allows
				if q.Resource.PermissionRoot {
					full = ""
				} else if q.Resource.PermissionFolder {
					full, allows = q.Resource.PermissionPath, p.Access().AllowsFolder
				}
				if !allows(types.ResourceArtifact, target(q.Repo), full, types.ActionRead) {
					return source.ErrDenied
				}
				return generic.CheckDownload(
					q.Repo,
					full,
					q.Resource.PermissionFolder || q.Resource.PermissionRoot,
				)
			},
			HTTP: sourceBackend(
				func(ctx context.Context, q source.Request, p source.Permit) (source.Result, error) {
					result, err := backend.Read(ctx, q, p)
					if err == nil && u.late != nil {
						u.late(q)
					}
					return result, err
				},
			),
			Cache: backend,
		}
		c.RemoteSources = u.svc
	})
	u.config(func(c *repo.Config) {
		c.URL, c.PackageType = u.up.URL, repo.Maven
		c.RetrievalCachePeriodSecs = ptr(1)
		c.MetadataRetrievalTimeoutSecs = ptr(2)
		c.BypassHeadRequests = ptr(true)
	})
	return u
}

func (u *upstreamSearchWorld) config(edit func(*repo.Config)) {
	u.t.Helper()
	c, err := u.db.Repos().Get(u.t.Context(), "up")
	if err != nil {
		u.t.Fatal(err)
	}
	edit(&c)
	if err = u.db.Repos().Update(u.t.Context(), c); err != nil {
		u.t.Fatal(err)
	}
}

func (u *upstreamSearchWorld) metadata(folder, body string) {
	u.up.Set(
		"/"+folder+"/maven-metadata.xml",
		fakeupstream.Resource{
			Body:   []byte(body),
			Header: http.Header{"Content-Type": {"application/xml"}},
		},
	)
}

func (u *upstreamSearchWorld) listing(folder string, entries ...string) {
	var body strings.Builder
	body.WriteString("<!DOCTYPE html><h1>Index</h1>")
	for _, entry := range entries {
		fmt.Fprintf(&body, `<a href="%s">%s</a>`, entry, entry)
	}
	requestPath := "/" + strings.Trim(folder, "/")
	if !strings.HasSuffix(requestPath, "/") {
		requestPath += "/"
	}
	u.up.Set(
		requestPath,
		fakeupstream.Resource{
			Body:   []byte(body.String()),
			Header: http.Header{"Content-Type": {"text/html"}},
		},
	)
}

const searchMetadata = `<metadata><groupId>org.acme</groupId><artifactId>lib</artifactId><versioning><versions>` +
	`<version>2.0</version><version>1.0-rc1</version><version>1.0-SNAPSHOT</version><version>1.0</version><version>2.0</version>` +
	`</versions></versioning></metadata>`

func TestAccSearchUpstreamMavenColdWarmAndAliases(t *testing.T) {
	u := searchUpstreamWorld(t)
	u.metadata("org/acme/lib", searchMetadata)
	u.put("/artifactory/libs-local/org/acme/lib/1.0/lib-1.0.jar", "local")
	query := "/artifactory/api/search/versions?g=org.acme&a=lib&repos=virt"
	for _, suffix := range []string{"", "&remote=0"} {
		res := u.do("GET", query+suffix, "reader", "")
		if strings.Contains(res.Body.String(), `"2.0"`) {
			t.Fatal("cache-only search disclosed upstream", res.Body)
		}
	}
	wantStatus(
		t,
		u.do(
			"GET",
			"/artifactory/api/search/versions?g=org.acme&a=lib&repos=up-cache&remote=1",
			"reader",
			"",
		),
		404,
	)
	if len(u.up.Requests()) != 0 {
		t.Fatal("cache-only query fetched metadata")
	}
	res := u.do("GET", query+"&remote=1", "reader", "")
	wantStatus(t, res, 200)
	if res.Body.String() != "{\"results\":[{\"integration\":false,\"version\":\"2.0\"},{\"integration\":false,\"version\":\"1.0\"},{\"integration\":false,\"version\":\"1.0-rc1\"},{\"integration\":true,\"version\":\"1.0-SNAPSHOT\"}]}\n" {
		t.Fatal(res.Body)
	}
	for _, req := range u.up.Requests() {
		if !strings.HasSuffix(req.Path, "maven-metadata.xml") || !req.Authorized {
			t.Fatalf("unexpected upstream request %+v", req)
		}
	}
	if _, err := u.db.Items().
		Get(t.Context(), "up", true, "org/acme/lib/2.0/lib-2.0.jar"); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatal("version discovery fetched a binary", err)
	}
	u.up.Close()
	for _, endpoint := range []string{"versions", "latestVersion"} {
		res = u.do(
			"GET",
			"/artifactory/api/search/"+endpoint+"?g=org.acme&a=lib&repos=up&remote=1",
			"reader",
			"",
		)
		wantStatus(t, res, 200)
		if !strings.Contains(res.Body.String(), "2.0") {
			t.Fatal("warm source metadata missing", res.Body)
		}
	}
}

func TestAccSearchUpstreamSnapshotsAndListing(t *testing.T) {
	u := searchUpstreamWorld(t)
	u.metadata("org/acme/lib", searchMetadata)
	u.metadata(
		"org/acme/lib/1.0-SNAPSHOT",
		`<metadata><groupId>org.acme</groupId><artifactId>lib</artifactId><version>1.0-SNAPSHOT</version><versioning><snapshotVersions>`+
			`<snapshotVersion><extension>jar</extension><value>1.0-20261022.120000-2</value></snapshotVersion>`+
			`<snapshotVersion><extension>jar</extension><classifier>sources</classifier><value>1.0-20261022.120000-10</value></snapshotVersion>`+
			`</snapshotVersions></versioning></metadata>`,
	)
	query := "?g=org.acme&a=lib&repos=up&remote=1&v=1.0-SNAPSHOT"
	res := u.do("GET", "/artifactory/api/search/latestVersion"+query, "reader", "")
	wantStatus(t, res, 200)
	if res.Body.String() != "1.0-20261022.120000-10" {
		t.Fatal(res.Body)
	}
	u.config(func(c *repo.Config) { c.RepoLayoutRef = "ivy-default" })
	u.listing("org.acme/lib", "3.0/")
	u.listing("org.acme/lib/3.0", "jars/", "ivys/")
	u.listing("org.acme/lib/3.0/jars", "lib-3.0.jar")
	u.listing("org.acme/lib/3.0/ivys", "ivy-3.0.xml")
	query = "/artifactory/api/search/versions?g=org.acme&a=lib&repos=up&remote=1"
	wantStatus(t, u.do("GET", query, "reader", ""), 404)
	u.config(func(c *repo.Config) { c.ListRemoteFolderItems = repo.Flex(true) })
	res = u.do("GET", query, "reader", "")
	wantStatus(t, res, 200)
	if strings.Count(res.Body.String(), `"3.0"`) != 1 {
		t.Fatal(res.Body)
	}
	res = u.do(
		"GET",
		"/artifactory/api/search/gavc?g=org.acme&a=lib&repos=virt&specific=true&remote=1",
		"reader",
		"",
	)
	wantStatus(t, res, 200)
	if !strings.Contains(res.Body.String(), "/virt/org.acme/lib/3.0/jars/lib-3.0.jar") ||
		strings.Contains(res.Body.String(), "/api/storage") ||
		strings.Contains(res.Body.String(), u.up.URL) {
		t.Fatal("invalid GAVC projection", res.Body)
	}
}

func TestAccSearchUpstreamFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		hard   bool
		status int
	}{
		{"missing", store.ErrNotFound, true, 200},
		{"offline miss", store.ErrNotFound, false, 200},
		{"connect soft", &source.HTTPError{Status: 502, Kind: source.FailureKindConnectivity}, false, 200},
		{"connect hard", &source.HTTPError{Status: 502, Kind: source.FailureKindConnectivity}, true, 502},
		{"timeout soft", &source.HTTPError{Status: 504, Kind: source.FailureKindTimeout}, false, 200},
		{"http soft", &source.HTTPError{Status: 503, Kind: source.FailureKindHTTP}, false, 200},
		{"throttle hard", &source.HTTPError{Status: 429, Kind: source.FailureKindHTTP}, true, 429},
		{"credentials", &source.HTTPError{Status: 401, Kind: source.FailureKindHTTP}, false, 401},
		{"ssrf", &source.HTTPError{Status: 502, Kind: source.FailureKindSecurity}, false, 502},
		{"malformed", &source.HTTPError{Status: 502, Kind: source.FailureKindIntegrity}, false, 502},
		{"unsupported", source.ErrUnsupported, false, 501},
		{"changed source", source.ErrSuperseded, false, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := searchUpstreamWorld(t)
			u.put("/artifactory/libs-local/org/acme/lib/1.0/lib-1.0.jar", "local")
			u.config(func(c *repo.Config) { c.HardFail = &tc.hard })
			u.svc.HTTP = sourceBackend(
				func(context.Context, source.Request, source.Permit) (source.Result, error) {
					return source.Result{}, tc.err
				},
			)
			res := u.do(
				"GET",
				"/artifactory/api/search/versions?g=org.acme&a=lib&repos=virt&remote=1",
				"reader",
				"",
			)
			wantStatus(t, res, tc.status)
			if strings.Contains(res.Body.String(), "upstream-secret") ||
				res.Header().Get("WWW-Authenticate") != "" {
				t.Fatal("source error leaked a challenge or secret", res)
			}
		})
	}
}

func TestAccSearchUpstreamVisibilityAndRevocation(t *testing.T) {
	u := searchUpstreamWorld(t)
	u.metadata("org/acme/lib", searchMetadata)
	grant := types.Permission{
		Name: "search-user",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{"user": types.ActionRead}},
			Targets: map[string]types.Target{"up": {Include: []string{"org/acme/lib/2.0/*.jar"}}},
		}},
	}
	if err := u.db.Permissions().Create(t.Context(), grant); err != nil {
		t.Fatal(err)
	}
	const query = "/artifactory/api/search/versions?g=org.acme&a=lib&repos=up&remote=1"
	res := u.do("GET", query, "user", "")
	wantStatus(t, res, 200)
	if res.Body.String() != "{\"results\":[{\"integration\":false,\"version\":\"2.0\"}]}\n" {
		t.Fatal("folder-level version projection leaked siblings or required POM", res.Body)
	}
	u.late = func(source.Request) {
		u.late = nil
		if err := u.db.Permissions().Delete(t.Context(), grant.Name); err != nil {
			t.Fatal(err)
		}
	}
	res = u.do("GET", query, "user", "")
	wantStatus(t, res, 403)
	if strings.Contains(res.Body.String(), "2.0") {
		t.Fatal("revoked version delivered", res.Body)
	}
	for _, noStore := range []bool{false, true} {
		t.Run(fmt.Sprint("source rotation no-store=", noStore), func(t *testing.T) {
			u := searchUpstreamWorld(t)
			u.config(func(c *repo.Config) { c.StoreArtifactsLocally = ptr(!noStore) })
			u.metadata("org/acme/lib", searchMetadata)
			u.late = func(source.Request) {
				u.late = nil
				u.config(func(c *repo.Config) { c.URL += "/changed" })
			}
			wantStatus(t, u.do("GET", query, "reader", ""), 503)
		})
	}
}

func TestAccSearchUpstreamWildcardDiscoveryAndGavcValidation(t *testing.T) {
	u := searchUpstreamWorld(t)
	u.listing("org", "acme/", "other/")
	u.listing("org/acme", "lib/")
	u.listing("org/acme/lib", "maven-metadata.xml", "2.0/")
	u.metadata("org/acme/lib", searchMetadata)
	res := u.do(
		"GET",
		"/artifactory/api/search/versions?g=org.*&a=lib&repos=up&remote=1&v=2.*",
		"reader",
		"",
	)
	wantStatus(t, res, 200)
	if !strings.Contains(res.Body.String(), `"2.0"`) ||
		strings.Contains(res.Body.String(), `"1.0"`) {
		t.Fatal(res.Body)
	}
	for _, request := range u.up.Requests() {
		if strings.Contains(request.Path, "/2.0/") {
			t.Fatal("module discovery descended into versions", request.Path)
		}
	}
	before := len(u.up.Requests())
	for _, query := range []string{
		"g=org.acme&a=lib&remote=1", "g=org.acme&a=lib&remote=2", "g=org.*&a=lib&specific=true&remote=1",
		"a=lib&specific=true&remote=1",
	} {
		wantStatus(t, u.do("GET", "/artifactory/api/search/gavc?"+query, "reader", ""), 400)
	}
	for _, query := range []string{"g=org.acme&a=lib&specific=true&remote=0", "a=lib&specific=true"} {
		wantStatus(t, u.do("GET", "/artifactory/api/search/gavc?"+query, "reader", ""), 200)
	}
	if len(u.up.Requests()) != before {
		t.Fatal("invalid/disabled GAVC used network")
	}
	u.config(func(c *repo.Config) { c.RemoteRepoLayoutRef = "ivy-default" })
	wantStatus(
		t,
		u.do(
			"GET",
			"/artifactory/api/search/versions?g=org.acme&a=lib&repos=up&remote=1",
			"reader",
			"",
		),
		404,
	)
	if len(u.up.Requests()) != before {
		t.Fatal("non-Maven upstream bypassed disabled listing flag")
	}
	u.listing("org.acme/lib", "1.0/")
	u.listing("org.acme/lib/1.0", "jars/")
	u.listing("org.acme/lib/1.0/jars", "lib-1.0.jar")
	wantStatus(
		t,
		u.do(
			"GET",
			"/artifactory/api/search/gavc?g=org.acme&a=lib&repos=up&specific=true",
			"reader",
			"",
		),
		501,
	)
	if u.up.Count("GET", "/org.acme/lib/1.0/jars/lib-1.0.jar") != 0 {
		t.Fatal("lossy Ivy type fetched artifact bytes")
	}
}

func TestAccSearchUpstreamMalformedMetadataNeverAdmitted(t *testing.T) {
	for _, body := range []string{
		`<metadata><groupId>foreign</groupId></metadata>`,
		`<metadata><artifactId>other</artifactId></metadata>`,
		`<metadata><version>1.0</version></metadata>`,
		`<metadata><versioning><versions><version>../secret</version></versions></versioning></metadata>`,
		`<metadata><versioning><versions><version>%2fsecret</version></versions></versioning></metadata>`,
		`<metadata>broken`,
	} {
		for _, noStore := range []bool{false, true} {
			t.Run(fmt.Sprint(body, noStore), func(t *testing.T) {
				u := searchUpstreamWorld(t)
				u.config(func(c *repo.Config) { c.StoreArtifactsLocally = ptr(!noStore) })
				u.metadata("org/acme/lib", body)
				res := u.do(
					"GET",
					"/artifactory/api/search/versions?g=org.acme&a=lib&repos=up&remote=1",
					"reader",
					"",
				)
				wantStatus(t, res, 502)
				entries, _, err := u.db.RemoteCache().
					List(t.Context(), "up", "org/acme/lib", store.Page{Limit: 100})
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if entry.Blob.Checksums.SHA256 != "" || !entry.NegativeUntil.IsZero() {
						t.Fatal("invalid metadata was published or negatively cached")
					}
				}
			})
		}
	}
}

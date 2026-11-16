package swifthttp_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	swifthttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/swift/http"
)

type remoteFixture struct {
	*fixture
	up *fakeupstream.Server
}

func newRemoteFixture(t *testing.T) *remoteFixture {
	t.Helper()
	up := fakeupstream.New(t, fakeupstream.Options{})
	clock := fakeupstream.NewClock(time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC))
	f := newFixture(t, repo.Remote, func(items *item.Service, db *inmem.Store) swifthttp.Options {
		c, err := db.Repos().Get(t.Context(), "packages")
		if err != nil {
			t.Fatal(err)
		}
		c.URL = up.URL + "/swift/"
		ttl := 10
		c.RetrievalCachePeriodSecs = &ttl
		if err := db.Repos().Update(t.Context(), c); err != nil {
			t.Fatal(err)
		}
		if err := db.Permissions().
			Create(t.Context(), accesstest.Permission{Name: "swift-reader", Resources: map[api.ResourceType]accesstest.Resource{
				api.ResourceArtifact: {
					Actions: accesstest.Grants{
						Users: map[string]api.Actions{"reader": api.ActionRead},
					},
					Targets: map[string]accesstest.Target{
						"packages": {Include: []string{"acme/Widget/Widget-1.0.0.zip"}},
					},
				},
			}}); err != nil {
			t.Fatal(err)
		}
		key := []byte(strings.Repeat("k", 32))
		resolver, err := upstream.NewResolver(db, nil, upstream.ResolverOptions{RevisionKey: key})
		if err != nil {
			t.Fatal(err)
		}
		pool := new(upstream.Pool)
		policy := &remote.Policy{
			Store: db, Blobs: items.Blobs, Items: items, Pool: pool,
			Options: resolver.Options, Key: key, Now: clock.Now,
		}
		t.Cleanup(pool.Close)
		svc := &source.Service{
			HTTP: policy, Cache: policy,
			Resolve: func(ctx context.Context, c repo.Config) (artmodel.SourceIdentity, error) {
				return db.RemoteCache().Source(ctx, c.Key)
			},
		}
		return swifthttp.Options{RemoteSources: svc}
	})
	return &remoteFixture{fixture: f, up: up}
}

func upstreamResource(contentType string, body []byte) fakeupstream.Resource {
	return fakeupstream.Resource{
		Body:   body,
		Header: http.Header{"Content-Type": []string{contentType}},
	}
}

func (f *remoteFixture) seed(t *testing.T, checksum string) []byte {
	t.Helper()
	zipBytes := archive(t, "Widget/")
	if checksum == "correct" {
		checksum = fmt.Sprintf("%x", sha256.Sum256(zipBytes))
	}
	f.up.Set(
		"/swift/acme/Widget",
		upstreamResource("application/json", []byte(`{"releases":{"1.0.0":{},"2.0.0":{}}}`)),
	)
	f.up.Set("/swift/acme/Widget/1.0.0", upstreamResource("application/json", []byte(fmt.Sprintf(
		`{"id":"acme.Widget","version":"1.0.0","resources":[{"name":"source-archive","type":"application/zip","checksum":"%s"}]}`,
		checksum,
	))))
	f.up.Set(
		"/swift/acme/Widget/1.0.0/Package.swift",
		upstreamResource("text/x-swift", []byte(manifest)),
	)
	f.up.Set("/swift/acme/Widget/1.0.0.zip", upstreamResource("application/zip", zipBytes))
	return zipBytes
}

func TestUnitSwiftRemoteCacheOfflineAndPermissions(t *testing.T) {
	f := newRemoteFixture(t)
	zipBytes := f.seed(t, "correct")
	for _, path := range []string{
		base + "acme/Widget", base + "acme/Widget/1.0.0",
		base + "acme/Widget/1.0.0/Package.swift", base + "acme/Widget/1.0.0.zip",
	} {
		rec := f.do(http.MethodGet, path, nil, "", "admin")
		if rec.Code != http.StatusOK {
			t.Fatalf(
				"cold %s: %d %s; upstream zip GET %d",
				path,
				rec.Code,
				rec.Body,
				f.up.Count(http.MethodGet, "/swift/acme/Widget/1.0.0.zip"),
			)
		}
	}
	rec := f.do(http.MethodGet, base+"acme/Widget", nil, "", "reader")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"1.0.0"`) ||
		strings.Contains(rec.Body.String(), `"2.0.0"`) {
		t.Fatalf("permission-filtered releases: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(
		http.MethodGet,
		base+"acme/Widget/2.0.0.zip",
		nil,
		"",
		"reader",
	); rec.Code != http.StatusForbidden {
		t.Fatalf("unreadable release: %d %s", rec.Code, rec.Body)
	}
	if got := f.up.Count(http.MethodGet, "/swift/acme/Widget/2.0.0.zip"); got != 0 {
		t.Fatalf("unreadable release reached upstream: %d", got)
	}
	c, err := f.db.Repos().Get(t.Context(), "packages")
	if err != nil {
		t.Fatal(err)
	}
	offline := true
	c.Offline = &offline
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	f.up.Delete("/swift/acme/Widget")
	f.up.Delete("/swift/acme/Widget/1.0.0")
	f.up.Delete("/swift/acme/Widget/1.0.0/Package.swift")
	f.up.Delete("/swift/acme/Widget/1.0.0.zip")
	for _, path := range []string{
		base + "acme/Widget", base + "acme/Widget/1.0.0",
		base + "acme/Widget/1.0.0/Package.swift", base + "acme/Widget/1.0.0.zip",
	} {
		rec := f.do(http.MethodGet, path, nil, "", "admin")
		if rec.Code != http.StatusOK {
			t.Fatalf("offline %s: %d %s", path, rec.Code, rec.Body)
		}
		if strings.HasSuffix(path, ".zip") &&
			!strings.Contains(rec.Body.String(), string(zipBytes[:2])) {
			t.Fatalf("offline archive bytes changed: %d", rec.Code)
		}
	}
}

func TestUnitSwiftRemoteRejectsAbsentOrInvalidChecksum(t *testing.T) {
	for _, checksum := range []string{"", "not-a-sha256", strings.Repeat("0", 64)} {
		t.Run(checksum, func(t *testing.T) {
			f := newRemoteFixture(t)
			f.seed(t, checksum)
			rec := f.do(http.MethodGet, base+"acme/Widget/1.0.0.zip", nil, "", "admin")
			if rec.Code != http.StatusBadGateway {
				t.Fatalf("bad checksum: %d %s", rec.Code, rec.Body)
			}
			if got := f.up.Count(http.MethodGet, "/swift/acme/Widget/1.0.0.zip"); got > 1 {
				t.Fatalf("invalid metadata retried archive %d times", got)
			}
		})
	}
}

// A virtual repository takes its members from the shared walk (decision
// 0008, R1 to R3): a local member reached through a nested virtual
// repository resolves, a cycle and a repeated member are skipped, a remote
// member whose upstream fails is passed over rather than answering 502,
// and a caller who may read only the local member resolves through the
// virtual repository, with the unreadable remote member passed over.
func TestUnitSwiftVirtualResolvesThroughTheSharedWalk(t *testing.T) {
	f := newRemoteFixture(t)
	f.seed(t, "correct")
	f.up.Set("/swift/acme/Widget", fakeupstream.Resource{Status: http.StatusServiceUnavailable})
	for _, c := range []repo.Config{
		{Key: "local", Class: repo.Local},
		{Key: "nested", Class: repo.Virtual, Repositories: []string{"local", "virtual"}},
		{
			Key: "virtual", Class: repo.Virtual,
			Repositories: []string{"packages", "nested", "virtual", "packages"},
		},
	} {
		c.PackageType = repo.Swift
		if err := f.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	localZIP := archive(t, "LocalWidget/")
	f.publish("local", "3.0.0", localZIP)
	if err := f.db.Permissions().Create(t.Context(), accesstest.Permission{
		Name: "local-only",
		Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
			Actions: accesstest.Grants{Users: map[string]api.Actions{"member": api.ActionRead}},
			Targets: map[string]accesstest.Target{"local": {}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	const v = "/artifactory/api/swift/virtual/acme/Widget"
	for _, user := range []string{"admin", "member"} {
		wantReleases(t, f.do(http.MethodGet, v, nil, "", user), "3.0.0")
		wantOK(t, f.do(http.MethodGet, v+"/3.0.0", nil, "", user), `"checksum":"`)
		wantOK(t, f.do(http.MethodGet, v+"/3.0.0/Package.swift", nil, "", user),
			"PackageDescription")
		rec := f.do(http.MethodGet, v+"/3.0.0.zip", nil, "", user)
		if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), localZIP) {
			t.Fatalf("%s: nested local archive: %d %s", user, rec.Code, rec.Body)
		}
	}
	// The remote member still serves the release the failing list named.
	wantOK(t, f.do(http.MethodGet, v+"/1.0.0", nil, "", "admin"), `"checksum":"`)
	wantProblem(t, f.do(http.MethodGet, v+"/1.0.0", nil, "", "member"),
		http.StatusNotFound, "Swift release not found")
	wantProblem(t, f.do(http.MethodGet, v+"/9.0.0", nil, "", "admin"),
		http.StatusNotFound, "Swift release not found")
	wantProblem(t, f.do(http.MethodGet,
		"/artifactory/api/swift/virtual/identifiers?url=https://example.org/acme/Widget.git",
		nil, "", "member"),
		http.StatusNotFound, "Swift package identifier mapping not found")
}

// swift package-registry publish to a virtual repository lands in its
// default deployment repository; without one the answer is 7.x's 405 with
// Allow: GET (decision 0008, R6).
func TestUnitSwiftVirtualPublishesToItsDeploymentRepository(t *testing.T) {
	f := newRemoteFixture(t)
	for _, c := range []repo.Config{
		{Key: "local", Class: repo.Local},
		{Key: "virtual", Class: repo.Virtual, Repositories: []string{"local", "packages"}},
	} {
		c.PackageType = repo.Swift
		if err := f.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	zip := archive(t, "Widget/")
	put := func(user string) *httptest.ResponseRecorder {
		body, contentType := form(t, zip)
		return f.do(http.MethodPut, "/artifactory/api/swift/virtual/acme/Widget/4.0.0", body,
			contentType, user)
	}
	rec := put("admin")
	wantProblem(t, rec, http.StatusMethodNotAllowed, "No local repository was configured as "+
		"local deployment repository for the (virtual) virtual repository.")
	if got := rec.Header().Get("Allow"); got != http.MethodGet {
		t.Fatalf("Allow = %q", got)
	}
	f.patch("virtual", func(c *repo.Config) { c.DefaultDeploymentRepo = "local" })
	if rec := put("reader"); rec.Code != http.StatusForbidden {
		t.Fatalf("publish without Write on the deployment repository: %d %s", rec.Code, rec.Body)
	}
	rec = put("admin")
	if rec.Code != http.StatusCreated || !strings.HasSuffix(rec.Header().Get("Location"),
		"/artifactory/api/swift/virtual/acme/Widget/4.0.0") {
		t.Fatalf("publish through the virtual repository: %d %q %s", rec.Code,
			rec.Header().Get("Location"), rec.Body)
	}
	rec = f.do(http.MethodGet, "/artifactory/api/swift/local/acme/Widget/4.0.0.zip", nil, "", "admin")
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), zip) {
		t.Fatalf("release in the deployment repository: %d %s", rec.Code, rec.Body)
	}
	f.patch("virtual", func(c *repo.Config) { c.DefaultDeploymentRepo = "packages" })
	wantProblem(t, put("admin"), http.StatusMethodNotAllowed, "No local repository was "+
		"configured as local deployment repository for the (virtual) virtual repository.")
}

func TestUnitSwiftVirtualOrderedLocalThenRemote(t *testing.T) {
	f := newRemoteFixture(t)
	remoteZIP := f.seed(t, "correct")
	local := *repo.Defaults(&repo.Config{Key: "local", Class: repo.Local, PackageType: repo.Swift})
	if err := f.db.Repos().Create(t.Context(), local); err != nil {
		t.Fatal(err)
	}
	virtual := *repo.Defaults(&repo.Config{
		Key: "virtual", Class: repo.Virtual, PackageType: repo.Swift,
		Repositories: []string{"local", "packages"},
	})
	if err := f.db.Repos().Create(t.Context(), virtual); err != nil {
		t.Fatal(err)
	}
	localZIP := archive(t, "LocalWidget/")
	if bytes.Equal(localZIP, remoteZIP) {
		t.Fatal("test packages must have distinct archives")
	}
	body, contentType := form(t, localZIP)
	rec := f.do(
		http.MethodPut,
		"/artifactory/api/swift/local/acme/Widget/1.0.0",
		body,
		contentType,
		"admin",
	)
	if rec.Code != http.StatusCreated {
		t.Fatalf("local publish: %d %s", rec.Code, rec.Body)
	}
	rec = f.do(http.MethodGet, "/artifactory/api/swift/virtual/acme/Widget", nil, "", "admin")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"1.0.0"`) ||
		!strings.Contains(rec.Body.String(), `"2.0.0"`) {
		t.Fatalf("virtual merged releases: %d %s", rec.Code, rec.Body)
	}
	rec = f.do(
		http.MethodGet,
		"/artifactory/api/swift/virtual/acme/Widget/1.0.0.zip",
		nil,
		"",
		"admin",
	)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), localZIP) {
		t.Fatalf("first member archive: %d %d bytes", rec.Code, rec.Body.Len())
	}
	if got := f.up.Count(http.MethodGet, "/swift/acme/Widget/1.0.0.zip"); got != 0 {
		t.Fatalf("virtual fetched lower-priority remote archive: %d", got)
	}
	rec = f.do(http.MethodGet,
		"/artifactory/api/swift/virtual/acme/Widget/1.0.0/Package.swift?swift-version=6.9",
		nil, "", "admin")
	if rec.Code != http.StatusSeeOther ||
		!strings.Contains(
			rec.Header().Get("Location"),
			"/api/swift/virtual/acme/Widget/1.0.0/Package.swift",
		) ||
		strings.Contains(rec.Header().Get("Location"), "/api/swift/local/") {
		t.Fatalf(
			"virtual manifest redirect escaped member: %d %q",
			rec.Code,
			rec.Header().Get("Location"),
		)
	}
	if _, err := f.items.Delete(
		t.Context(),
		item.Target{Repo: local, Path: "acme/Widget/Widget-1.0.0.zip"},
		"admin",
	); err != nil {
		t.Fatal(err)
	}
	rec = f.do(
		http.MethodGet,
		"/artifactory/api/swift/virtual/acme/Widget/1.0.0.zip",
		nil,
		"",
		"admin",
	)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), remoteZIP) {
		t.Fatalf("remote member fallback: %d %d bytes", rec.Code, rec.Body.Len())
	}
}

func (f *remoteFixture) seedClientVersion(t *testing.T, version string) {
	t.Helper()
	zipBytes := archive(t, "Widget/")
	checksum := fmt.Sprintf("%x", sha256.Sum256(zipBytes))
	f.up.Set("/swift/acme/Widget", upstreamResource("application/json", []byte(fmt.Sprintf(
		`{"releases":{"%s":{}}}`, version))))
	f.up.Set("/swift/acme/Widget/"+version, upstreamResource("application/json", []byte(fmt.Sprintf(
		`{"id":"acme.Widget","version":"%s","resources":[{"name":"source-archive","type":"application/zip","checksum":"%s"}]}`,
		version,
		checksum,
	))))
	f.up.Set(
		"/swift/acme/Widget/"+version+"/Package.swift",
		upstreamResource("text/x-swift", []byte(manifest)),
	)
	f.up.Set(
		"/swift/acme/Widget/"+version+".zip",
		fakeupstream.Resource{Redirect: "/swift/blobs/Widget.zip"},
	)
	f.up.Set("/swift/blobs/Widget.zip", upstreamResource("application/zip", zipBytes))
}

func swiftConsumer(t *testing.T, root, name, version string) string {
	t.Helper()
	consumer := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(consumer, "Sources", "Consumer"), 0o700); err != nil {
		t.Fatal(err)
	}
	text := fmt.Sprintf(`// swift-tools-version:6.0
import PackageDescription
let package = Package(name: "Consumer", dependencies: [.package(id: "acme.Widget", from: "%s")], targets: [.executableTarget(name: "Consumer", dependencies: [.product(name: "Widget", package: "acme.Widget")])])
`, version)
	if err := os.WriteFile(
		filepath.Join(consumer, "Package.swift"),
		[]byte(text),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(consumer, "Sources", "Consumer", "main.swift"),
		[]byte("import Widget\nlet _ = Widget()\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	return consumer
}

func TestAccSwiftPMRemoteVirtualAndOffline(t *testing.T) {
	if !swiftClientEnabled(t) {
		return
	}
	f := newRemoteFixture(t)
	version := fmt.Sprintf("1.0.%d", time.Now().UnixMilli()%1_000_000_000_000)
	f.seedClientVersion(t, version)
	local := *repo.Defaults(&repo.Config{Key: "empty", Class: repo.Local, PackageType: repo.Swift})
	if err := f.db.Repos().Create(t.Context(), local); err != nil {
		t.Fatal(err)
	}
	virtual := *repo.Defaults(&repo.Config{
		Key: "virtual", Class: repo.Virtual, PackageType: repo.Swift,
		Repositories: []string{"empty", "packages"},
	})
	if err := f.db.Repos().Create(t.Context(), virtual); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Permissions().
		Create(t.Context(), accesstest.Permission{Name: "swiftpm-authenticated-reader", Resources: map[api.ResourceType]accesstest.Resource{
			api.ResourceArtifact: {
				Actions: accesstest.Grants{
					Users: map[string]api.Actions{"swift-client": api.ActionRead},
				},
				Targets: map[string]accesstest.Target{
					"packages": {Include: []string{"**"}},
					"virtual":  {Include: []string{"**"}},
					"empty":    {Include: []string{"**"}},
				},
			},
		}}); err != nil {
		t.Fatal(err)
	}
	// Deploy/Cache on the remote alone: the client's cold reads fill its
	// cache, which 7.x allows only a caller with Deploy/Cache on it (#1587).
	if err := f.db.Permissions().
		Create(t.Context(), accesstest.Permission{Name: "swiftpm-remote-fill", Resources: map[api.ResourceType]accesstest.Resource{
			api.ResourceArtifact: {
				Actions: accesstest.Grants{
					Users: map[string]api.Actions{"swift-client": api.ActionWrite},
				},
				Targets: map[string]accesstest.Target{"packages": {Include: []string{"**"}}},
			},
		}}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(swiftCredentialHandler(t, f.fixture, "swift-client"))
	defer srv.Close()
	root := t.TempDir()
	credentials := swiftNetrc(t, root, "swift-client")
	for _, tc := range []struct{ key, name string }{{"packages", "Remote"}, {"virtual", "Virtual"}} {
		consumer := swiftConsumer(t, root, tc.name, version)
		url := srv.URL + "/artifactory/api/swift/" + tc.key
		runSwift(
			t,
			root,
			"package-registry",
			"--package-path",
			swiftPath(root, consumer),
			"set",
			"--allow-insecure-http",
			url,
		)
		resolve := []string{
			"package", "--package-path", swiftPath(root, consumer),
			"--cache-path", swiftPath(root, filepath.Join(root, tc.name+"-cache")),
		}
		resolve = append(resolve, credentials...)
		runSwift(t, root, append(resolve, "resolve")...)
		if f.count(
			http.MethodGet,
			"/artifactory/api/swift/"+tc.key+"/acme/Widget/"+version+".zip",
		) == 0 {
			t.Fatalf("SwiftPM did not download through %s", tc.key)
		}
	}
	before := f.up.Count(http.MethodGet, "/swift/acme/Widget/"+version+".zip")
	c, err := f.db.Repos().Get(t.Context(), "packages")
	if err != nil {
		t.Fatal(err)
	}
	offline := true
	c.Offline = &offline
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	f.up.Delete("/swift/acme/Widget")
	f.up.Delete("/swift/acme/Widget/" + version)
	f.up.Delete("/swift/acme/Widget/" + version + "/Package.swift")
	f.up.Delete("/swift/acme/Widget/" + version + ".zip")
	consumer := swiftConsumer(t, root, "Offline", version)
	url := srv.URL + "/artifactory/api/swift/packages"
	runSwift(
		t,
		root,
		"package-registry",
		"--package-path",
		swiftPath(root, consumer),
		"set",
		"--allow-insecure-http",
		url,
	)
	resolve := []string{
		"package", "--package-path", swiftPath(root, consumer),
		"--cache-path", swiftPath(root, filepath.Join(root, "Offline-cache")),
	}
	resolve = append(resolve, credentials...)
	runSwift(t, root, append(resolve, "resolve")...)
	if got := f.up.Count(http.MethodGet, "/swift/acme/Widget/"+version+".zip"); got != before {
		t.Fatalf("offline SwiftPM contacted upstream: %d -> %d", before, got)
	}
}

// SwiftPM publishes a release through a virtual repository into its
// default deployment repository (decision 0008, R6), then resolves through
// the same virtual repository both that release, from the local member,
// and one only the remote member's upstream holds, as a caller who may
// read the members but holds no permission on the virtual repository
// (R10).
func TestAccSwiftPMVirtualLocalRemoteAndPublish(t *testing.T) {
	if !swiftClientEnabled(t) {
		return
	}
	f := newRemoteFixture(t)
	stamp := time.Now().UnixMilli() % 1_000_000_000_000
	remoteVersion, localVersion := fmt.Sprintf("1.0.%d", stamp), fmt.Sprintf("2.0.%d", stamp)
	f.seedClientVersion(t, remoteVersion)
	for _, c := range []repo.Config{
		{Key: "local", Class: repo.Local},
		{
			Key: "virtual", Class: repo.Virtual, Repositories: []string{"local", "packages"},
			DefaultDeploymentRepo: "local",
		},
	} {
		c.PackageType = repo.Swift
		if err := f.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	// Read on both members and Write on the deployment repository, with
	// Deploy/Cache on the remote so the client's cold reads fill its cache
	// (#1587); nothing on the virtual repository itself.
	if err := f.db.Permissions().Create(t.Context(), accesstest.Permission{
		Name: "swiftpm-members",
		Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
			Actions: accesstest.Grants{Users: map[string]api.Actions{
				"swift-client": api.ActionRead | api.ActionWrite,
			}},
			Targets: map[string]accesstest.Target{
				"local":    {Include: []string{"**"}},
				"packages": {Include: []string{"**"}},
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(swiftCredentialHandler(t, f.fixture, "swift-client"))
	defer srv.Close()
	root := t.TempDir()
	credentials := swiftNetrc(t, root, "swift-client")
	url := srv.URL + "/artifactory/api/swift/virtual"
	pkg := filepath.Join(root, "Widget")
	if err := os.MkdirAll(filepath.Join(pkg, "Sources", "Widget"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "Package.swift"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(pkg, "Sources", "Widget", "Widget.swift"),
		[]byte("public struct Widget { public init() {} }\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	publish := append(
		[]string{"package-registry", "--package-path", swiftPath(root, pkg)},
		credentials...)
	runSwift(t, root, append(publish,
		"publish", "acme.Widget", localVersion, "--url", url, "--allow-insecure-http")...)
	if rec := f.do(http.MethodGet, "/artifactory/api/swift/local/acme/Widget/"+localVersion,
		nil, "", "admin"); rec.Code != http.StatusOK {
		t.Fatalf("release published through the virtual repository: %d %s", rec.Code, rec.Body)
	}
	for _, tc := range []struct{ name, version string }{
		{"FromRemote", remoteVersion}, {"FromLocal", localVersion},
	} {
		consumer := swiftConsumer(t, root, tc.name, tc.version)
		runSwift(t, root, "package-registry", "--package-path", swiftPath(root, consumer),
			"set", "--allow-insecure-http", url)
		resolve := []string{
			"package", "--package-path", swiftPath(root, consumer),
			"--cache-path", swiftPath(root, filepath.Join(root, tc.name+"-cache")),
			"--replace-scm-with-registry",
		}
		resolve = append(resolve, credentials...)
		runSwift(t, root, append(resolve, "resolve")...)
		if f.count(
			http.MethodGet,
			"/artifactory/api/swift/virtual/acme/Widget/"+tc.version+".zip",
		) == 0 {
			t.Fatalf("SwiftPM did not download %s through the virtual repository", tc.version)
		}
	}
}

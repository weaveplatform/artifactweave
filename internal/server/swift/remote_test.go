package swift_test

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

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/remote"
	"github.com/weaveplatform/artifactweave/internal/server/swift"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type remoteFixture struct {
	*fixture
	up *fakeupstream.Server
}

func newRemoteFixture(t *testing.T) *remoteFixture {
	t.Helper()
	up := fakeupstream.New(t, fakeupstream.Options{})
	clock := fakeupstream.NewClock(time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC))
	f := newFixture(t, repo.Remote, func(items *item.Service, db *inmem.Store) swift.Options {
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
			Create(t.Context(), types.Permission{Name: "swift-reader", Resources: map[types.ResourceType]types.Resource{
				types.ResourceArtifact: {
					Actions: types.Grants{
						Users: map[string]types.Actions{"reader": types.ActionRead},
					},
					Targets: map[string]types.Target{
						"packages": {Include: []string{"acme/Widget/Widget-1.0.0.zip"}},
					},
				},
			}}); err != nil {
			t.Fatal(err)
		}
		key := []byte(strings.Repeat("k", 32))
		resolver, err := remote.NewResolver(db, nil, remote.ResolverOptions{RevisionKey: key})
		if err != nil {
			t.Fatal(err)
		}
		policy := &remote.Policy{
			Store: db, Blobs: items.Blobs, Items: items, Pool: new(remote.Pool),
			Options: resolver.Options, Key: key, Now: clock.Now,
		}
		t.Cleanup(policy.Pool.Close)
		svc := &source.Service{
			HTTP: policy, Cache: policy,
			Resolve: func(ctx context.Context, c repo.Config) (types.SourceIdentity, error) {
				return db.RemoteCache().Source(ctx, c.Key)
			},
		}
		return swift.Options{RemoteSources: svc}
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
		Create(t.Context(), types.Permission{Name: "swiftpm-anonymous-reader", Resources: map[types.ResourceType]types.Resource{
			types.ResourceArtifact: {
				Actions: types.Grants{
					Users: map[string]types.Actions{"anonymous": types.ActionRead},
				},
				Targets: map[string]types.Target{
					"packages": {Include: []string{"**"}},
					"virtual":  {Include: []string{"**"}},
					"empty":    {Include: []string{"**"}},
				},
			},
		}}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(f.r)
	defer srv.Close()
	root := t.TempDir()
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
		runSwift(t, root, "package", "--package-path", swiftPath(root, consumer),
			"--cache-path", swiftPath(root, filepath.Join(root, tc.name+"-cache")), "resolve")
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
	runSwift(t, root, "package", "--package-path", swiftPath(root, consumer),
		"--cache-path", swiftPath(root, filepath.Join(root, "Offline-cache")), "resolve")
	if got := f.up.Count(http.MethodGet, "/swift/acme/Widget/"+version+".zip"); got != before {
		t.Fatalf("offline SwiftPM contacted upstream: %d -> %d", before, got)
	}
}

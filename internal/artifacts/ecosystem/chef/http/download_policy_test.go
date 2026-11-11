package chefhttp_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	upstreamhttp "github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
)

const cookbookPath = "/api/v1/cookbooks/widget"

// A blacked-out local repository refuses the cookbook and version records
// a client resolves a cookbook from, naming the request's path, and every
// download, naming the archive's path whether the version exists or not,
// after read permission; its catalog and search keep their empty answers.
// Patterns that reject an archive refuse only its download, with the
// policy's message: the version stays listed and latest still names it.
func TestAccChefDownloadPolicyLocal(t *testing.T) {
	f := newFixture(t, repo.Local)
	one := f.deploy(f.c, "widget", "1.0.0", archive(t, "widget", "1.0.0"))
	f.deploy(f.c, "widget", "2.0.0", archive(t, "widget", "2.0.0"))
	a := policyAnswers{f: f}
	versions := cookbookPath + "/versions/"

	f.patch(f.c.Key, func(c *repo.Config) { c.BlackedOut = new(true) })
	a.answered("/api/v1/cookbooks", `"total":0`)
	a.answered("/api/v1/search?q=wid", `"total":0`)
	for _, path := range []string{
		cookbookPath, versions + "1_0_0", versions + "latest", versions + "latest/download",
	} {
		a.refused(path, blackedOut(f.c.Key, strings.TrimPrefix(path, "/")))
	}
	a.refused(versions+"1_0_0/download", blackedOut(f.c.Key, archivePath("1.0.0")))
	a.refused(versions+"9_9_9/download", blackedOut(f.c.Key, archivePath("9.9.9")))
	if got := f.do(http.MethodGet, base+cookbookPath, "nobody"); got.Code != http.StatusForbidden {
		t.Fatalf("a caller without read permission: %d %s, want 403", got.Code, got.Body)
	}

	f.patch(f.c.Key, func(c *repo.Config) {
		c.BlackedOut, c.ExcludesPattern = new(false), new("**/*-2.0.0.tar.gz")
	})
	a.answered("/api/v1/cookbooks", `"cookbook_name":"widget"`)
	a.answered("/api/v1/search?q=wid", `"cookbook_name":"widget"`)
	a.answered(cookbookPath, `"latest_version":"http://example.com`+base+versions+`2_0_0"`)
	a.answered(versions+"2_0_0", `"version":"2.0.0"`)
	a.answered(versions+"latest", `"version":"2.0.0"`)
	a.refused(versions+"2_0_0/download", rejected(f.c.Key, archivePath("2.0.0")))
	a.refused(versions+"latest/download", rejected(f.c.Key, archivePath("2.0.0")))
	a.served(versions+"1_0_0/download", one.Actual.SHA256)

	f.patch(f.c.Key, func(c *repo.Config) { c.ExcludesPattern = new("widget/**") })
	a.answered("/api/v1/search?q=wid", `"cookbook_name":"widget"`)
	a.answered(cookbookPath, `versions/1_0_0"`, `versions/2_0_0"`)
	a.refused(versions+"9_9_9/download", rejected(f.c.Key, archivePath("9.9.9")))
}

// A virtual repository leaves a blacked-out member out, keeps listed a
// version whose archive a member's patterns or its own reject, passes over
// a member whose patterns refuse the archive for the next one that holds
// it, and refuses a download its own patterns reject with "No repository
// found to serve the request", whether the version exists or not.
func TestAccChefDownloadPolicyVirtual(t *testing.T) {
	f := newFixture(t, repo.Virtual)
	first := f.member("first", repo.Local, "")
	second := f.member("second", repo.Local, "")
	firstOne := f.deploy(first, "widget", "1.0.0", memberArchive(t, "widget", "1.0.0", "first"))
	f.deploy(first, "widget", "2.0.0", memberArchive(t, "widget", "2.0.0", "first"))
	f.deploy(first, "solo", "1.0.0", memberArchive(t, "solo", "1.0.0", "first"))
	secondOne := f.deploy(second, "widget", "1.0.0", memberArchive(t, "widget", "1.0.0", "second"))
	secondTwo := f.deploy(second, "widget", "2.0.0", memberArchive(t, "widget", "2.0.0", "second"))
	f.patch(f.c.Key, func(c *repo.Config) { c.Repositories = []string{"first", "second"} })
	a := policyAnswers{f: f}
	versions := cookbookPath + "/versions/"

	f.patch("first", func(c *repo.Config) { c.BlackedOut = new(true) })
	a.answered("/api/v1/cookbooks", `"total":1`)
	a.missing("/api/v1/cookbooks/solo")
	a.served(versions+"1_0_0/download", secondOne.Actual.SHA256)

	f.patch("first", func(c *repo.Config) {
		c.BlackedOut, c.ExcludesPattern = new(false), new("**/*-2.0.0.tar.gz")
	})
	a.answered(cookbookPath, `versions/2_0_0"`)
	a.served(versions+"2_0_0/download", secondTwo.Actual.SHA256)
	a.served(versions+"1_0_0/download", firstOne.Actual.SHA256)
	f.patch("second", func(c *repo.Config) { c.ExcludesPattern = new("**/*-2.0.0.tar.gz") })
	a.answered(versions+"latest", `"version":"2.0.0"`)
	a.missing(versions + "latest/download")

	for _, key := range []string{"first", "second"} {
		f.patch(key, func(c *repo.Config) { c.ExcludesPattern = new("") })
	}
	f.patch(f.c.Key, func(c *repo.Config) { c.ExcludesPattern = new("**/*-2.0.0.tar.gz") })
	a.answered("/api/v1/search?q=wid", `"cookbook_name":"widget"`)
	a.answered(cookbookPath, `versions/2_0_0"`)
	a.answered(versions+"2_0_0", `"version":"2.0.0"`)
	a.refused(versions+"2_0_0/download", noMember(f.c.Key, archivePath("2.0.0")))
	a.refused(versions+"latest/download", noMember(f.c.Key, archivePath("2.0.0")))
	f.patch(f.c.Key, func(c *repo.Config) { c.ExcludesPattern = new("**/*-9.9.9.tar.gz") })
	a.refused(versions+"9_9_9/download", noMember(f.c.Key, archivePath("9.9.9")))
}

// A blacked-out remote refuses its cookbook and version records, naming
// the request's path, and its downloads, naming the archive's path,
// without asking its upstream; its catalog and search keep the blackout
// answer they gave. Its metadata reads check its blackout alone, so a
// version whose archive or folder the patterns reject stays listed and
// only its download is refused, at the archive's path.
func TestAccChefDownloadPolicyRemote(t *testing.T) {
	f, up, data := newSourceFixture(t, repo.Remote)
	f.patch(f.c.Key, func(c *repo.Config) { c.URL = up.URL + "/chef/" })
	a := policyAnswers{f: f}
	version := cookbookPath + "/versions/1_0_0"
	download := version + "/download"

	f.patch(f.c.Key, func(c *repo.Config) { c.BlackedOut = new(true) })
	a.refused("/api/v1/cookbooks", blackedOut(f.c.Key, ""))
	a.refused("/api/v1/search?q=wid", blackedOut(f.c.Key, ""))
	a.refused(cookbookPath, blackedOut(f.c.Key, "api/v1/cookbooks/widget"))
	a.refused(version, blackedOut(f.c.Key, "api/v1/cookbooks/widget/versions/1_0_0"))
	a.refused(download, blackedOut(f.c.Key, archivePath("1.0.0")))
	if n := len(up.Requests()); n != 0 {
		t.Fatalf("a blacked-out remote asked its upstream %d times", n)
	}

	for _, pattern := range []string{"**/*.tar.gz", "widget/**"} {
		f.patch(f.c.Key, func(c *repo.Config) {
			c.BlackedOut, c.ExcludesPattern = new(false), new(pattern)
		})
		a.answered("/api/v1/search?q=wid", `"cookbook_name":"widget"`)
		a.answered(cookbookPath, `versions/1_0_0"`)
		a.answered(version, `"version":"1.0.0"`)
		a.refused(download, rejected(f.c.Key, archivePath("1.0.0")))
	}
	if n := up.Count(http.MethodGet, "/chef/blobs/widget-1.0.0.tgz"); n != 0 {
		t.Fatalf("a rejected archive was fetched %d times", n)
	}
	f.patch(f.c.Key, func(c *repo.Config) { c.ExcludesPattern = new("") })
	a.served(download, digest(data))
}

// A virtual repository passes over a remote member whose own patterns
// reject a version's archive, and the next member serves it, while the
// remote's version stays listed; a blacked-out remote member is left out
// of the catalog, which the other members answer, and is not asked.
func TestAccChefDownloadPolicyVirtualRemoteMember(t *testing.T) {
	f, up, _ := newSourceFixture(t, repo.Virtual)
	f.member("chef-remote", repo.Remote, up.URL+"/chef/")
	hosted := f.member("chef-hosted", repo.Local, "")
	one := f.deploy(hosted, "widget", "1.0.0", memberArchive(t, "widget", "1.0.0", "chef-hosted"))
	f.patch(f.c.Key, func(c *repo.Config) { c.Repositories = []string{"chef-remote", "chef-hosted"} })
	a := policyAnswers{f: f}
	download := cookbookPath + "/versions/1_0_0/download"

	f.patch("chef-remote", func(c *repo.Config) { c.ExcludesPattern = new("**/*.tar.gz") })
	a.answered(cookbookPath, `versions/1_0_0"`)
	a.served(download, one.Actual.SHA256)
	if n := up.Count(http.MethodGet, "/chef/blobs/widget-1.0.0.tgz"); n != 0 {
		t.Fatalf("a member's rejected archive was fetched %d times", n)
	}

	f.patch("chef-remote", func(c *repo.Config) {
		c.BlackedOut, c.ExcludesPattern = new(true), new("")
	})
	asked := len(up.Requests())
	a.answered("/api/v1/cookbooks", `"cookbook_name":"widget"`)
	a.served(download, one.Actual.SHA256)
	if n := len(up.Requests()) - asked; n != 0 {
		t.Fatalf("a blacked-out member's upstream was asked %d times", n)
	}
}

// policyAnswers checks the answers of the fixture repository's Chef
// routes, asked by an administrator, whom the download policy refuses too.
type policyAnswers struct{ f *fixture }

func (a policyAnswers) refused(path, message string) {
	a.f.t.Helper()
	got := a.f.do(http.MethodGet, base+path, "admin")
	if got.Code != http.StatusNotFound || errorMessage(a.f.t, got.Body.Bytes()) != message {
		a.f.t.Fatalf("GET %s: %d %s, want 404 %q", path, got.Code, got.Body, message)
	}
}

// missing checks Chef's own not-found answer.
func (a policyAnswers) missing(path string) {
	a.f.t.Helper()
	a.refused(path, "Chef cookbook or version not found")
}

// answered checks a 200 whose body holds each of want.
func (a policyAnswers) answered(path string, want ...string) {
	a.f.t.Helper()
	got := a.f.do(http.MethodGet, base+path, "admin")
	if got.Code != http.StatusOK {
		a.f.t.Fatalf("GET %s: %d %s, want 200", path, got.Code, got.Body)
	}
	for _, w := range want {
		if !strings.Contains(got.Body.String(), w) {
			a.f.t.Fatalf("GET %s: %s, want %s in it", path, got.Body, w)
		}
	}
}

// served checks a 200 whose body is the archive whose SHA-256 is sha, so a
// virtual repository's answer shows which member served it.
func (a policyAnswers) served(path, sha string) {
	a.f.t.Helper()
	got := a.f.do(http.MethodGet, base+path, "admin")
	if got.Code != http.StatusOK || digest(got.Body.Bytes()) != sha {
		a.f.t.Fatalf("GET %s: %d, want 200 with the archive %s", path, got.Code, sha)
	}
}

func (f *fixture) patch(key string, change func(*repo.Config)) {
	f.t.Helper()
	c, err := f.db.Repos().Get(f.t.Context(), key)
	if err != nil {
		f.t.Fatal(err)
	}
	change(&c)
	if err := f.db.Repos().Update(f.t.Context(), c); err != nil {
		f.t.Fatal(err)
	}
}

// member creates a Chef repository for a virtual fixture to list.
func (f *fixture) member(key string, class repo.Class, url string) repo.Config {
	f.t.Helper()
	c := *repo.Defaults(&repo.Config{Key: key, Class: class, PackageType: repo.Chef, URL: url})
	if err := f.db.Repos().Create(f.t.Context(), c); err != nil {
		f.t.Fatal(err)
	}
	return c
}

// newSourceFixture is newFixture whose remote repositories read through
// the source boundary, which applies a remote's own download policy, and
// data, widget 1.0.0's archive, which up, a fake upstream Supermarket
// under /chef/, publishes with its catalog, cookbook and version
// documents.
func newSourceFixture(t *testing.T, class repo.Class) (*fixture, *fakeupstream.Server, []byte) {
	t.Helper()
	sources := new(source.Service)
	f := newFixtureWith(t, class, func(p *pkgroute.Protocol) {
		p.Sources[repo.Remote] = pkgroute.Remote{Service: sources}
	})
	up := fakeupstream.New(t, fakeupstream.Options{})
	key := []byte(strings.Repeat("k", 32))
	resolver, err := upstreamhttp.NewResolver(f.db, nil, upstreamhttp.ResolverOptions{RevisionKey: key})
	if err != nil {
		t.Fatal(err)
	}
	pool := new(upstreamhttp.Pool)
	t.Cleanup(pool.Close)
	policy := &remote.Policy{
		Store: f.db, Blobs: f.items.Blobs, Items: f.items, Pool: pool,
		Options: resolver.Options, Key: key,
		Now: fakeupstream.NewClock(time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)).Now,
	}
	*sources = source.Service{
		HTTP: policy, Cache: policy,
		Resolve: func(ctx context.Context, c repo.Config) (artmodel.SourceIdentity, error) {
			return f.db.RemoteCache().Source(ctx, c.Key)
		},
	}
	data := archive(t, "widget", "1.0.0")
	root := up.URL + "/chef/api/v1/cookbooks/widget"
	documents := map[string]string{
		"/chef/api/v1/cookbooks": `{"total":1,"start":0,"items":[{"cookbook_name":"widget"}]}`,
		"/chef/api/v1/cookbooks/widget": fmt.Sprintf(
			`{"name":"widget","versions":[%q],"latest_version":%q}`,
			root+"/versions/1_0_0", root+"/versions/1_0_0",
		),
		"/chef/api/v1/cookbooks/widget/versions/1_0_0": fmt.Sprintf(
			`{"version":"1.0.0","file":%q,"cookbook":%q,"tarball_file_size":%d,"sha256":%q,`+
				`"dependencies":{"helper":">= 1.0.0"},"platforms":{"ubuntu":">= 20.04"}}`,
			up.URL+"/chef/blobs/widget-1.0.0.tgz", root, len(data), digest(data),
		),
	}
	for path, body := range documents {
		up.Set(path, fakeupstream.Resource{
			Body:   []byte(body),
			Header: http.Header{"Content-Type": {"application/json"}},
		})
	}
	up.Set("/chef/blobs/widget-1.0.0.tgz", fakeupstream.Resource{Body: data})
	return f, up, data
}

// memberArchive is a cookbook archive whose recipe names from, so that
// the archive a virtual repository serves shows which member it came from;
// archive's files are written in map order, so two of its archives of one
// version may or may not be the same bytes.
func memberArchive(t *testing.T, name, version, from string) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	for _, file := range [][2]string{
		{"metadata.json", fmt.Sprintf(`{"name":%q,"version":%q}`, name, version)},
		{"recipes/default.rb", "# from " + from},
	} {
		header := &tar.Header{Name: name + "/" + file[0], Mode: 0o644, Size: int64(len(file[1]))}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(file[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func archivePath(version string) string { return "widget/widget-" + version + ".tar.gz" }

func blackedOut(key, path string) string {
	return "The repository '" + key + "' is blacked out and cannot serve artifact '" +
		key + ":" + path + "'."
}

func rejected(key, path string) string {
	return "The repository '" + key + "' rejected the artifact '" + key + ":" + path +
		"' due to its include/exclude pattern settings."
}

func noMember(key, path string) string {
	return "No repository found to serve the request for " + key + ":" + path
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// errorMessage is the message of the Artifactory error envelope body holds.
func errorMessage(t *testing.T, body []byte) string {
	t.Helper()
	var envelope struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || len(envelope.Errors) != 1 {
		t.Fatalf("not one error: %v: %s", err, body)
	}
	return envelope.Errors[0].Message
}

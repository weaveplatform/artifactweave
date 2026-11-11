package composerhttp

import (
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // Composer's dist.shasum is SHA-1 on the wire.
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
)

func (f *fixture) patch(key string, change func(*repo.Config)) {
	f.t.Helper()
	c, err := f.db.Repos().Get(context.Background(), key)
	if err != nil {
		f.t.Fatal(err)
	}
	change(&c)
	if err := f.db.Repos().Update(context.Background(), c); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) requestUserAgent(path, agent string) *httptest.ResponseRecorder {
	f.t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Header.Set("X-User", "admin")
	r.Header.Set("User-Agent", agent)
	f.router.ServeHTTP(w, r)
	return w
}

// wantRefusal fails unless got is the Artifactory error document with 404
// and message.
func wantRefusal(t *testing.T, label string, got *httptest.ResponseRecorder, message string) {
	t.Helper()
	want := `{"errors":[{"message":"` + message + `","status":404}]}` + "\n"
	if got.Code != http.StatusNotFound || got.Body.String() != want || got.Header().Get("ETag") != "" {
		t.Errorf("%s: %d %q (ETag %q), want 404 %q", label, got.Code, got.Body,
			got.Header().Get("ETag"), want)
	}
}

func blackedOut(key, path string) string {
	return "The repository '" + key + "' is blacked out and cannot serve artifact '" +
		key + ":" + path + "'."
}

func rejected(key, path string) string {
	return "The repository '" + key + "' rejected the artifact '" + key + ":" + path +
		"' due to its include/exclude pattern settings."
}

func sha1Hex(data []byte) string {
	return fmt.Sprintf("%x", sha1.Sum(data)) //nolint:gosec // Composer's wire checksum.
}

// TestAccDownloadPolicyOfAHostedRepository: a blacked-out local or federated
// repository refuses packages.json, to Composer 1 and 2 alike, and a
// package's p2 metadata with the blackout's 404 naming the request path,
// after read permission, as its archives are refused; a pattern on an
// archive leaves the release listed and refuses only the archive; and a
// pattern on the metadata's path refuses nothing, since 7.x does not
// document where it keeps it (#1237).
func TestAccDownloadPolicyOfAHostedRepository(t *testing.T) {
	for _, class := range []repo.Class{repo.Local, repo.Federated} {
		f := newFixture(t)
		c := f.create("php", class)
		first := f.deploy(c, "1.0.0", "first")
		f.deploy(c, "dev-main", "development")
		base := "/artifactory/api/composer/php"
		on, off := true, false
		f.patch("php", func(c *repo.Config) { c.BlackedOut = &on })
		for _, agent := range []string{"Composer/2.7.0", "Composer/1.10.0"} {
			wantRefusal(t, string(class)+" root "+agent,
				f.requestUserAgent(base+"/packages.json", agent), blackedOut("php", "packages.json"))
		}
		wantRefusal(t, string(class)+" p2", f.request("GET", base+"/p2/acme/widget.json"),
			blackedOut("php", "p2/acme/widget.json"))
		wantRefusal(t, string(class)+" p2 dev", f.request("GET", base+"/p2/acme/widget~dev.json"),
			blackedOut("php", "p2/acme/widget~dev.json"))
		wantRefusal(t, string(class)+" dist", f.request("GET", base+"/dist/acme/widget/1.0.0.zip"),
			blackedOut("php", "acme/widget/1.0.0.zip"))
		if got := f.request("HEAD", base+"/p2/acme/widget.json"); got.Code != http.StatusNotFound {
			t.Errorf("%s p2 HEAD: %d", class, got.Code)
		}
		if got := f.requestAs("GET", base+"/p2/acme/widget.json", "ungranted"); got.Code != 403 {
			t.Errorf("%s blacked out, unreadable: %d %s", class, got.Code, got.Body)
		}
		excluded := "acme/widget/1.0.0.zip"
		f.patch("php", func(c *repo.Config) { c.BlackedOut, c.ExcludesPattern = &off, &excluded })
		if got := f.request("GET", base+"/p2/acme/widget.json"); got.Code != 200 ||
			!strings.Contains(got.Body.String(), sha1Hex(first)) {
			t.Errorf("%s p2 with an excluded archive: %d %s", class, got.Code, got.Body)
		}
		if got := f.requestUserAgent(base+"/packages.json", "Composer/1.10.0"); got.Code != 200 ||
			!strings.Contains(got.Body.String(), `"1.0.0"`) {
			t.Errorf("%s Composer 1 catalog with an excluded archive: %d %s", class, got.Code, got.Body)
		}
		wantRefusal(t, string(class)+" excluded dist",
			f.request("GET", base+"/dist/acme/widget/1.0.0.zip"), rejected("php", excluded))
		excluded = "p2/**,packages.json"
		f.patch("php", func(c *repo.Config) { c.ExcludesPattern = &excluded })
		for _, path := range []string{"/packages.json", "/p2/acme/widget.json"} {
			if got := f.request("GET", base+path); got.Code != 200 {
				t.Errorf("%s pattern on %s: %d %s", class, path, got.Code, got.Body)
			}
		}
	}
}

// TestAccDownloadPolicyThroughAVirtualRepository: a virtual repository
// resolves a package past a blacked-out member to the next member that
// holds it; an archive the resolved member refuses is passed over to the
// next member that holds the version, while one the resolved member lacks
// stays not found; and the virtual repository's own patterns refuse an
// archive with "No repository found to serve the request" but leave its
// p2 metadata alone (#1237).
func TestAccDownloadPolicyThroughAVirtualRepository(t *testing.T) {
	f := newFixture(t)
	la, lb := f.create("la", repo.Local), f.create("lb", repo.Local)
	fromA := f.deploy(la, "1.0.0", "from la")
	f.deploy(la, "1.1.0", "from la")
	fromB := f.deploy(lb, "1.0.0", "from lb")
	f.deploy(lb, "1.1.0", "from lb")
	onlyB := f.deploy(lb, "2.0.0", "from lb")
	f.create("v", repo.Virtual, "la", "lb")
	base := "/artifactory/api/composer/v"
	dist := func(label, version string, want []byte) {
		t.Helper()
		got := f.request("GET", base+"/dist/acme/widget/"+version+".zip")
		if got.Code != 200 || !bytes.Equal(got.Body.Bytes(), want) {
			t.Errorf("%s: dist %s %d %s", label, version, got.Code, got.Body)
		}
	}
	p2 := func(label string, want []byte) {
		t.Helper()
		got := f.request("GET", base+"/p2/acme/widget.json")
		if got.Code != 200 || !strings.Contains(got.Body.String(), sha1Hex(want)) {
			t.Errorf("%s: p2 %d %s", label, got.Code, got.Body)
		}
	}
	p2("both members", fromA)
	dist("both members", "1.0.0", fromA)
	if got := f.request("GET", base+"/dist/acme/widget/2.0.0.zip"); got.Code != 404 ||
		!strings.Contains(got.Body.String(), "Composer package not found") {
		t.Errorf("version the resolved member lacks: %d %s", got.Code, got.Body)
	}
	on, off := true, false
	f.patch("la", func(c *repo.Config) { c.BlackedOut = &on })
	p2("first member blacked out", fromB)
	dist("first member blacked out", "1.0.0", fromB)
	dist("first member blacked out", "2.0.0", onlyB)
	excluded := "acme/widget/1.0.0.zip"
	f.patch("la", func(c *repo.Config) { c.BlackedOut, c.ExcludesPattern = &off, &excluded })
	p2("first member excludes an archive", fromA)
	dist("first member excludes an archive", "1.0.0", fromB)
	f.patch("lb", func(c *repo.Config) { c.ExcludesPattern = &excluded })
	if got := f.request("GET", base+"/dist/acme/widget/1.0.0.zip"); got.Code != 404 ||
		!strings.Contains(got.Body.String(), "Composer package not found") {
		t.Errorf("every member excludes the archive: %d %s", got.Code, got.Body)
	}
	f.patch("la", func(c *repo.Config) { c.ExcludesPattern = new(string) })
	f.patch("lb", func(c *repo.Config) { c.ExcludesPattern = new(string) })
	f.patch("v", func(c *repo.Config) { c.ExcludesPattern = &excluded })
	wantRefusal(t, "virtual excludes the archive", f.request("GET", base+"/dist/acme/widget/1.0.0.zip"),
		"No repository found to serve the request for v:acme/widget/1.0.0.zip")
	excluded = "p2/**"
	f.patch("v", func(c *repo.Config) { c.ExcludesPattern = &excluded })
	p2("virtual pattern on p2", fromA)
	f.patch("v", func(c *repo.Config) { c.ExcludesPattern = new(string) })
	// A member passed over hands the archive to the next member that holds
	// the version, past one that holds only other versions.
	f.deploy(f.create("lc", repo.Local), "3.0.0", "from lc")
	f.patch("v", func(c *repo.Config) { c.Repositories = []string{"la", "lc", "lb"} })
	excluded = "acme/widget/1.0.0.zip"
	f.patch("la", func(c *repo.Config) { c.ExcludesPattern = &excluded })
	dist("past a member without the version", "1.0.0", fromB)
	f.patch("v", func(c *repo.Config) { c.Repositories = []string{"la", "la"} })
	if got := f.request("GET", base+"/dist/acme/widget/1.0.0.zip"); got.Code != 400 {
		t.Errorf("repeated member: %d %s", got.Code, got.Body)
	}
}

// TestAccDownloadPolicyOfARemoteRepository: a blacked-out remote refuses
// packages.json, p2 metadata and an archive with the blackout's 404 naming
// the request path before anything is read; a release whose archive the
// remote's patterns reject stays listed and only its archive is refused;
// and a virtual repository resolves past a blacked-out remote member and
// passes an archive a remote member refuses on to the next member (#1237).
func TestAccDownloadPolicyOfARemoteRepository(t *testing.T) {
	first, second := makeZIP(t, "1.0.0", "remote"), makeZIP(t, "1.1.0", "remote")
	b := &sourceBackend{content: map[string][]byte{
		"packages.json": []byte(`{"packages":{},"metadata-url":"/p2/%package%.json"}`),
		"p2/acme/widget.json": []byte(fmt.Sprintf(`{"packages":{"acme/widget":[`+
			`{"name":"acme/widget","version":"1.0.0","dist":{"type":"zip","url":"https://up.example/a.zip","shasum":"%s"}},`+
			`{"name":"acme/widget","version":"1.1.0","dist":{"type":"zip","url":"https://up.example/b.zip","shasum":"%s"}}]}}`,
			sha1Hex(first), sha1Hex(second))),
		"acme/widget/1.0.0.zip": first,
		"acme/widget/1.1.0.zip": second,
	}}
	f := newFixtureWithSource(t, &source.Service{
		HTTP: b, Cache: b,
		Resolve: func(context.Context, repo.Config) (model.SourceIdentity, error) {
			return model.SourceIdentity{
				Kind: model.ExternalHTTP, Repository: "remote", ID: "source", Generation: "1",
			}, nil
		},
	})
	rc := f.create("remote", repo.Remote)
	rc.URL, rc.ComposerRegistryURL = "https://up.example", "https://up.example"
	if err := f.db.Repos().Update(context.Background(), rc); err != nil {
		t.Fatal(err)
	}
	fromLocal := f.deploy(f.create("local", repo.Local), "1.0.0", "local")
	f.create("all", repo.Virtual, "remote", "local")
	base := "/artifactory/api/composer/remote"
	on, off := true, false
	f.patch("remote", func(c *repo.Config) { c.BlackedOut = &on })
	for path, checked := range map[string]string{
		"/packages.json":              "packages.json",
		"/p2/acme/widget.json":        "p2/acme/widget.json",
		"/dist/acme/widget/1.0.0.zip": "acme/widget/1.0.0.zip",
	} {
		wantRefusal(t, "blacked out "+path, f.request("GET", base+path), blackedOut("remote", checked))
	}
	if len(b.calls) != 0 {
		t.Errorf("a blacked-out remote was read: %v", b.calls)
	}
	all := "/artifactory/api/composer/all"
	if got := f.request("GET", all+"/p2/acme/widget.json"); got.Code != 200 ||
		!strings.Contains(got.Body.String(), sha1Hex(fromLocal)) {
		t.Errorf("blacked-out remote member: p2 %d %s", got.Code, got.Body)
	}
	if got := f.request("GET", all+"/dist/acme/widget/1.0.0.zip"); got.Code != 200 ||
		!bytes.Equal(got.Body.Bytes(), fromLocal) {
		t.Errorf("blacked-out remote member: dist %d %s", got.Code, got.Body)
	}
	excluded := "acme/widget/1.0.0.zip"
	f.patch("remote", func(c *repo.Config) { c.BlackedOut, c.ExcludesPattern = &off, &excluded })
	if got := f.request("GET", base+"/p2/acme/widget.json"); got.Code != 200 ||
		!strings.Contains(got.Body.String(), sha1Hex(first)) {
		t.Errorf("release whose archive the remote refuses: %d %s", got.Code, got.Body)
	}
	wantRefusal(t, "excluded archive", f.request("GET", base+"/dist/acme/widget/1.0.0.zip"),
		rejected("remote", excluded))
	if got := f.request("GET", base+"/dist/acme/widget/1.1.0.zip"); got.Code != 200 ||
		!bytes.Equal(got.Body.Bytes(), second) {
		t.Errorf("archive beside an excluded one: %d %s", got.Code, got.Body)
	}
	if got := f.request("GET", all+"/dist/acme/widget/1.0.0.zip"); got.Code != 200 ||
		!bytes.Equal(got.Body.Bytes(), fromLocal) {
		t.Errorf("remote member excludes the archive: dist %d %s", got.Code, got.Body)
	}
}

// TestAccDownloadPolicyRefusalOfARemoteRevalidation: a remote's policy
// refusal that the revalidation of its metadata returns answers its own
// 404 and message, not a fault (R4); other failures keep their answers,
// through a virtual repository too (#1237).
func TestAccDownloadPolicyRefusalOfARemoteRevalidation(t *testing.T) {
	archive := makeZIP(t, "1.0.0", "remote")
	b := &sourceBackend{content: map[string][]byte{
		"packages.json": []byte(`{"packages":{},"metadata-url":"/p2/%package%.json"}`),
		"p2/acme/widget.json": []byte(fmt.Sprintf(`{"packages":{"acme/widget":[`+
			`{"name":"acme/widget","version":"1.0.0","dist":{"type":"zip","url":"https://up.example/a.zip","shasum":"%s"}}]}}`,
			sha1Hex(archive))),
		"acme/widget/1.0.0.zip": archive,
	}}
	f := newFixtureWithSource(t, &source.Service{
		HTTP: b, Cache: b,
		Resolve: func(context.Context, repo.Config) (model.SourceIdentity, error) {
			return model.SourceIdentity{
				Kind: model.ExternalHTTP, Repository: "remote", ID: "source", Generation: "1",
			}, nil
		},
	})
	rc := f.create("remote", repo.Remote)
	rc.URL, rc.ComposerRegistryURL = "https://up.example", "https://up.example"
	if err := f.db.Repos().Update(context.Background(), rc); err != nil {
		t.Fatal(err)
	}
	f.create("all", repo.Virtual, "remote")
	base := "/artifactory/api/composer/remote"
	message := blackedOut("remote", "")
	b.revalidateErr = format.Rejectf(http.StatusNotFound, "%s", message)
	wantRefusal(t, "root", f.request("GET", base+"/packages.json"), message)
	wantRefusal(t, "p2", f.request("GET", base+"/p2/acme/widget.json"), message)
	wantRefusal(t, "dist", f.request("GET", base+"/dist/acme/widget/1.0.0.zip"), message)
	b.revalidateErr = nil
	b.readError = map[string]error{"p2/acme/widget.json": source.ErrUnavailable}
	for _, path := range []string{
		base + "/dist/acme/widget/1.0.0.zip",
		"/artifactory/api/composer/all/p2/acme/widget.json",
		"/artifactory/api/composer/all/dist/acme/widget/1.0.0.zip",
	} {
		if got := f.request("GET", path); got.Code != http.StatusBadGateway {
			t.Errorf("failing source, %s: %d %s", path, got.Code, got.Body)
		}
	}
}

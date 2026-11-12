package p2_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// TestAccP2RepositoryWritesCreateTheRemotesP2UrlsNeed: a P2 virtual
// repository created or updated through the repositories API with p2Urls
// gets the members they need, and a P2 remote repository is created, under
// its host's key, for each site no remote covers, as 7.x's UI does
// (RTFACT-30256). The composite then lists each site at the path 7.x
// serves it under, and a client reads it there.
func TestAccP2RepositoryWritesCreateTheRemotesP2UrlsNeed(t *testing.T) {
	index := p2Fixture(t, "p2.index")
	backend := sourceBackend(
		func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
			switch q.Repo.Key + ":" + q.Resource.Path {
			case "download.eclipse.org:releases/luna/p2.index",
				"download.eclipse.org:releases/juno/p2.index":
			default:
				return source.Result{}, kernel.ErrNotFound
			}
			return source.Result{Stream: &source.Stream{
				Body: io.NopCloser(bytes.NewReader(index)), Status: http.StatusOK,
				Representation: source.Representation{Size: int64(len(index))},
			}}, nil
		},
	)
	w := newItemWorld(t, false, func(c *Config) {
		c.RemoteSources = &source.Service{Resolve: sourceIdentity, HTTP: backend}
	})
	for _, c := range []repo.Config{
		{Key: "p2-local", Class: repo.Local, PackageType: repo.Generic},
		{
			// p2Urls stored as a YAML apply stores them: nothing is created
			// for a site no member serves.
			Key: "p2-yaml", Class: repo.Virtual, PackageType: repo.P2,
			P2URLs: []string{"https://uncovered.example/site"},
		},
	} {
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	write := func(method, path, body string, code int, answer string) {
		t.Helper()
		res := w.do(method, path, "admin", body, "Content-Type", "application/json")
		got := res.Body.String()
		if code >= http.StatusBadRequest {
			var e struct{ Errors []struct{ Message string } }
			if json.Unmarshal(res.Body.Bytes(), &e) == nil && len(e.Errors) == 1 {
				got = e.Errors[0].Message
			}
		}
		if res.Code != code || !strings.Contains(got, answer) {
			t.Fatalf("%s %s: %d %s, want %d %q", method, path, res.Code, res.Body, code, answer)
		}
	}
	type config struct {
		Rclass, PackageType, URL string
		Repositories             []string
		ListRemoteFolderItems    bool
	}
	get := func(key string) (config, bool) {
		t.Helper()
		res := w.do(http.MethodGet, "/artifactory/api/repositories/"+key, "admin", "")
		var c config
		if res.Code != http.StatusOK {
			return c, false
		}
		if err := json.Unmarshal(res.Body.Bytes(), &c); err != nil {
			t.Fatalf("%s: %v %s", key, err, res.Body)
		}
		return c, true
	}
	members := func(key string, want ...string) {
		t.Helper()
		if c, _ := get(key); !slices.Equal(c.Repositories, want) {
			t.Errorf("%s members %q, want %q", key, c.Repositories, want)
		}
	}
	remote := func(key, url string) {
		t.Helper()
		c, ok := get(key)
		if !ok || c.Rclass != "remote" || c.PackageType != "p2" || c.URL != url ||
			!c.ListRemoteFolderItems {
			t.Errorf("%s: %+v %v, want a P2 remote of %s", key, c, ok, url)
		}
	}
	absent := func(keys ...string) {
		t.Helper()
		for _, key := range keys {
			if _, ok := get(key); ok {
				t.Errorf("%s exists", key)
			}
		}
	}

	// One remote serves both sites of its host, after the members named.
	write(http.MethodPut, "/artifactory/api/repositories/p2-virtual",
		`{"rclass":"virtual","packageType":"p2","repositories":["p2-local"],"p2Urls":[`+
			`"https://download.eclipse.org/releases/luna",`+
			`"https://download.eclipse.org/releases/juno","local://p2-local"]}`,
		http.StatusOK, "Successfully created repository 'p2-virtual'")
	remote("download.eclipse.org", "https://download.eclipse.org/")
	members("p2-virtual", "p2-local", "download.eclipse.org")
	res := w.do(http.MethodGet, "/artifactory/p2-virtual/compositeContent.xml", "admin", "")
	if want := "<children size=\"3\">\n" +
		"<child location=\"download.eclipse.org/releases/luna\"/>\n" +
		"<child location=\"download.eclipse.org/releases/juno\"/>\n" +
		"<child location=\"p2-local\"/>\n</children>"; res.Code != http.StatusOK ||
		!strings.Contains(res.Body.String(), want) {
		t.Errorf("composite: %d %s, want %q", res.Code, res.Body, want)
	}
	for _, site := range []string{"luna", "juno"} {
		path := "/artifactory/p2-virtual/download.eclipse.org/releases/" + site + "/p2.index"
		if res := w.do(http.MethodGet, path, "admin", ""); res.Code != http.StatusOK ||
			!bytes.Equal(res.Body.Bytes(), index) {
			t.Errorf("%s: %d %.200s", path, res.Code, res.Body)
		}
	}

	// An update that sends p2Urls creates what a new site needs; a port
	// joins the key.
	write(http.MethodPost, "/artifactory/api/repositories/p2-virtual",
		`{"p2Urls":["https://download.eclipse.org/releases/luna",`+
			`"http://mirror.example:8081/eclipse/updates"]}`,
		http.StatusOK, "Successfully updated repository 'p2-virtual'")
	remote("mirror.example-8081", "http://mirror.example:8081/")
	members("p2-virtual", "p2-local", "download.eclipse.org", "mirror.example-8081")

	// An update that leaves p2Urls out creates nothing, whatever they name.
	write(http.MethodPost, "/artifactory/api/repositories/p2-yaml", `{"description":"x"}`,
		http.StatusOK, "Successfully updated repository 'p2-yaml'")
	absent("uncovered.example")

	// A URL the plan cannot use refuses the whole write: neither the
	// virtual repository nor a remote for an earlier URL is stored.
	write(
		http.MethodPut,
		"/artifactory/api/repositories/p2-bad",
		`{"rclass":"virtual","packageType":"p2","p2Urls":["https://fresh.example/site","ftp://x.example/"]}`,
		http.StatusBadRequest,
		"Invalid P2 URL 'ftp://x.example/': expected local://<key>[/<suffix>] or an http or https URL",
	)
	write(
		http.MethodPost,
		"/artifactory/api/repositories/p2-yaml",
		`{"p2Urls":["https://fresh.example/site","local://missing"]}`,
		http.StatusBadRequest,
		"P2 URL 'local://missing' names no local Generic or Maven repository",
	)
	write(
		http.MethodPut,
		"/artifactory/api/v2/repositories/batch",
		`[{"key":"p2-bad","rclass":"virtual","packageType":"p2","p2Urls":["https://fresh.example/site"]},`+
			`{"key":"p2-bad-2","rclass":"virtual","packageType":"p2","p2Urls":["local://p2-yaml"]}]`,
		http.StatusBadRequest,
		"P2 URL 'local://p2-yaml' names no local Generic or Maven repository",
	)
	absent("p2-bad", "p2-bad-2", "fresh.example")
	members("p2-yaml")

	// A batch creates a site's remote once, for every virtual repository
	// that needs it, and answers for the repositories it was given.
	res = w.do(
		http.MethodPut,
		"/artifactory/api/v2/repositories/batch",
		"admin",
		`[{"key":"p2-a","rclass":"virtual","packageType":"p2","p2Urls":["https://batch.example/a"]},`+
			`{"key":"p2-b","rclass":"virtual","packageType":"p2","p2Urls":["https://batch.example/b"]}]`,
		"Content-Type",
		"application/json",
	)
	if want := "Successfully created repository 'p2-a'\nSuccessfully created repository 'p2-b'"; res.Code != http.StatusCreated ||
		strings.TrimSpace(res.Body.String()) != want {
		t.Errorf("batch create: %d %s, want %q", res.Code, res.Body, want)
	}
	remote("batch.example", "https://batch.example/")
	members("p2-a", "batch.example")
	members("p2-b", "batch.example")
	write(
		http.MethodPost,
		"/artifactory/api/v2/repositories/batch",
		`[{"key":"p2-a","p2Urls":["https://shared.example/a"]},{"key":"p2-yaml","p2Urls":["https://shared.example/b"]}]`,
		http.StatusOK,
		"Successfully updated 2 repositories",
	)
	remote("shared.example", "https://shared.example/")
	members("p2-a", "batch.example", "shared.example")
	members("p2-yaml", "shared.example")
	absent("batch.example-1", "shared.example-1")
}

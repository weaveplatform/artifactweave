package bowerhttp

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// The binary archive reads its files' blobs itself, so it applies the
// download policy that the kit's Item applies (decision 0032): a
// blacked-out repository refuses the version by its manifest, an excluded
// file refuses the archive that would hold it, administrators included,
// and a virtual repository passes over a member that refuses the version.
func TestUnitBowerBinaryDownloadPolicy(t *testing.T) {
	f := newFixture(t)
	l := f.repo("bower-local", repo.Local)
	fed := f.repo("bower-fed", repo.Federated)
	_ = f.repo("bower-virtual", repo.Virtual, l.Key, fed.Key)
	for _, c := range []repo.Config{l, fed} {
		f.deploy(c, "hello", "1.0.0", "bower.json", `{"name":"hello","version":"1.0.0"}`)
		f.deploy(c, "hello", "1.0.0", "lib/index.js", "from-"+c.Key)
	}
	binary := func(key string) string {
		return "/artifactory/api/bower/" + key + "/binaries/" + key + "/hello.git/v1.0.0"
	}
	patch := func(key string, change func(*repo.Config)) {
		t.Helper()
		c, err := f.db.Repos().Get(t.Context(), key)
		if err != nil {
			t.Fatal(err)
		}
		change(&c)
		if err := f.db.Repos().Update(t.Context(), c); err != nil {
			t.Fatal(err)
		}
	}
	served := func(key, from string) {
		t.Helper()
		rec := f.get(binary(key), "admin")
		if rec.Code != http.StatusOK || archivedIndex(t, rec) != from {
			t.Fatalf("GET %s: %d, want the archive from %s", binary(key), rec.Code, from)
		}
	}
	refused := func(key, message string) {
		t.Helper()
		rec := f.get(binary(key), "admin")
		if rec.Code != http.StatusNotFound || bowerMessage(t, rec) != message {
			t.Fatalf("GET %s: %d %s, want 404 %q", binary(key), rec.Code, rec.Body, message)
		}
	}
	served(l.Key, "from-bower-local")
	served("bower-virtual", "from-bower-local")

	patch(l.Key, func(c *repo.Config) { c.BlackedOut = ptr(true) })
	refused(l.Key, "The repository 'bower-local' is blacked out and cannot serve artifact "+
		"'bower-local:hello/1.0.0/bower.json'.")
	if rec := f.get(binary(l.Key), "guest"); rec.Code != http.StatusForbidden {
		t.Fatalf("a caller who may not read is told how the repository is set: %d %s",
			rec.Code, rec.Body)
	}
	served("bower-virtual", "from-bower-fed")

	patch(
		l.Key,
		func(c *repo.Config) { c.BlackedOut, c.ExcludesPattern = ptr(false), ptr("**/*.js") },
	)
	refused(l.Key, "The repository 'bower-local' rejected the artifact "+
		"'bower-local:hello/1.0.0/lib/index.js' due to its include/exclude pattern settings.")
	patch(l.Key, func(c *repo.Config) { c.ExcludesPattern = ptr("hello/**") })
	served("bower-virtual", "from-bower-fed")

	patch("bower-virtual", func(c *repo.Config) { c.ExcludesPattern = ptr("hello/**") })
	refused("bower-virtual",
		"No repository found to serve the request for bower-virtual:hello/1.0.0/bower.json")
	patch("bower-virtual", func(c *repo.Config) { c.ExcludesPattern = ptr("") })
	patch(l.Key, func(c *repo.Config) { c.ExcludesPattern = ptr("") })
	served(l.Key, "from-bower-local")
}

func ptr[T any](v T) *T { return &v }

// archivedIndex is lib/index.js in the archive rec answered.
func archivedIndex(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("archive: %v: %s", err, rec.Body)
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return ""
		}
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(h.Name, "/lib/index.js") {
			b, err := io.ReadAll(tr)
			if err != nil {
				t.Fatal(err)
			}
			return string(b)
		}
	}
}

// bowerMessage is the message of the Artifactory error envelope rec holds.
func bowerMessage(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.Errors) != 1 {
		t.Fatalf("not one error: %v: %s", err, rec.Body)
	}
	return body.Errors[0].Message
}

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/multipart"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// skipsExisting reports whether Poetry's publish --skip-existing takes rec
// for a file that is already there, by the rule it keeps for Artifactory:
// status == 403 and "overwrite artifact" in the reason phrase and the body,
// lower-cased and joined (Poetry 2.5.1, src/poetry/publishing/uploader.py,
// _is_file_exists_error; twine had the same rule in skip_upload until
// 6.2.0). Any other wording fails the publish, so a refused overwrite must
// keep Artifactory's.
func skipsExisting(rec *httptest.ResponseRecorder) bool {
	reasonAndText := strings.ToLower(http.StatusText(rec.Code) + rec.Body.String())
	return rec.Code == http.StatusForbidden && strings.Contains(reasonAndText, "overwrite artifact")
}

// refusedOverwrite is 7.x's text for an overwrite of repoKey:path refused to
// user (RTFACT-26398, a 7.26.0 REST deploy).
func refusedOverwrite(repoKey, path, user string) string {
	return fmt.Sprintf(
		"Not enough permissions to delete/overwrite artifact '%s:%s' (user: '%s' needs DELETE permission).",
		repoKey,
		path,
		user,
	)
}

// artifactoryMessage is the message of rec's Artifactory error envelope,
// which must hold one error whose status is rec's.
func artifactoryMessage(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Errors []struct {
			Status  int    `json:"status"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.Errors) != 1 ||
		body.Errors[0].Status != rec.Code {
		t.Fatalf("not one Artifactory error: %d %s", rec.Code, rec.Body)
	}
	return body.Errors[0].Message
}

// Every deploy over a file the caller may not delete is refused with
// Artifactory's text, which names the file where it is stored and the
// caller (#565): an upload, one that declares its content, a checksum
// deploy, the checksum deploy that ends a multipart upload, a deploy
// through a virtual repository, which names the repository it lands in,
// and an archive, which names the entry in the way. The file stays as it
// was.
func TestAccOverwriteRefusalText(t *testing.T) {
	w := newItemWorld(t, false, func(c *Config) {
		c.Multipart = &multipart.Service{Store: c.Store, Blobs: c.Blobs, Dir: t.TempDir()}
	})
	for p, body := range map[string]string{
		"taken.txt": "taken", "other.txt": "other", "ex/inner.txt": "inner", "big/data.bin": "old",
	} {
		w.put("/artifactory/libs-local/"+p, body)
	}
	data := strings.Repeat("uploaded in parts ", 1000)
	token := uploadInParts(t, w, "deployer", "big/data.bin", data)
	byChecksum := func(content string, more ...string) []string {
		return append(
			[]string{"X-Checksum-Deploy", "true", "X-Checksum-Sha1", sha1Hex(content)},
			more...)
	}
	for _, c := range []struct {
		name, path, body string
		headers          []string
		// file is the path in libs-local the refusal names.
		file string
	}{
		{"upload", "libs-local/taken.txt", "changed", nil, "taken.txt"},
		{
			"upload declaring its content", "libs-local/taken.txt", "changed",
			[]string{"X-Checksum-Sha1", sha1Hex("changed")},
			"taken.txt",
		},
		{"checksum deploy", "libs-local/taken.txt", "", byChecksum("other"), "taken.txt"},
		{
			"multipart completion", "libs-local/big/data.bin", "",
			byChecksum(data, "X-Checksum-Deploy-Token", token), "big/data.bin",
		},
		{"through a virtual repository", "virt/taken.txt", "changed", nil, "taken.txt"},
		{
			"archive", "libs-local/ex/bundle.zip",
			string(zipBytes(t, map[string]string{"inner.txt": "changed"})),
			[]string{"X-Explode-Archive", "true"},
			"ex/inner.txt",
		},
	} {
		rec := w.do(http.MethodPut, "/artifactory/"+c.path, "deployer", c.body, c.headers...)
		want := refusedOverwrite("libs-local", c.file, "deployer")
		if got := artifactoryMessage(t, rec); rec.Code != http.StatusForbidden || got != want {
			t.Errorf("%s: %d %q, want 403 %q", c.name, rec.Code, got, want)
		}
		if !skipsExisting(rec) {
			t.Errorf(
				"%s: Poetry would not take %d %s for an existing file",
				c.name,
				rec.Code,
				rec.Body,
			)
		}
	}
	for p, body := range map[string]string{
		"taken.txt": "taken", "ex/inner.txt": "inner", "big/data.bin": "old",
	} {
		if got := w.do(
			http.MethodGet,
			"/artifactory/libs-local/"+p,
			"admin",
			"",
		); got.Body.String() != body {
			t.Errorf("a refused overwrite replaced %s: %q", p, got.Body)
		}
	}

	// A checksum deploy the server checks before it looks at the content
	// (deploy.enforce_permission_check_on_identical_checksum_deploy) is
	// refused with the same text.
	strict := newItemWorld(t, false, func(c *Config) { c.EnforceIdenticalChecksumDeploy = true })
	strict.put("/artifactory/libs-local/taken.txt", "taken")
	rec := strict.do(http.MethodPut, "/artifactory/libs-local/taken.txt", "deployer", "",
		byChecksum("taken")...)
	if got := artifactoryMessage(t, rec); rec.Code != http.StatusForbidden ||
		got != refusedOverwrite("libs-local", "taken.txt", "deployer") {
		t.Errorf("checked before the content: %d %q", rec.Code, got)
	}
}

// An anonymous caller that may deploy but not delete gets the same text,
// naming the anonymous user, with 401 and a challenge, so a client that
// sends credentials only when asked sends them.
func TestAccOverwriteRefusalAnonymous(t *testing.T) {
	w := newItemWorld(t, true, func(c *Config) {
		c.Protocols = append(c.Protocols, pkgtestProtocol())
	})
	grant(t, w, "anonymous-deploys", auth.AnonymousUser, "libs-local",
		types.ActionRead|types.ActionWrite)
	w.put("/artifactory/libs-local/taken.txt", "taken")
	want := refusedOverwrite("libs-local", "taken.txt", auth.AnonymousUser)
	for _, path := range []string{
		"/artifactory/libs-local/taken.txt",
		"/artifactory/api/pkgtest/libs-local/files/taken.txt",
	} {
		rec := w.do(http.MethodPut, path, "", "changed")
		if got := artifactoryMessage(t, rec); rec.Code != http.StatusUnauthorized || got != want {
			t.Errorf("%s: %d %q, want 401 %q", path, rec.Code, got, want)
		}
		if rec.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("%s: no challenge", path)
		}
	}
	// A new file needs no Delete.
	wantStatus(t, w.do(http.MethodPut, "/artifactory/libs-local/fresh.txt", "", "fresh"),
		http.StatusCreated)
}

// The protocols that store through the router kit refuse an overwrite with
// the same text, each in its own envelope: Artifactory's for the test-only
// protocol, and one plain-text line for Go, whose .info a caller without
// Delete may not replace (decision 0035).
func TestAccOverwriteRefusalProtocols(t *testing.T) {
	w := newItemWorld(t, false, func(c *Config) {
		c.Protocols = append(c.Protocols, pkgtestProtocol())
	})
	ctx := context.Background()
	if err := w.db.Repos().Create(ctx, *repo.Defaults(&repo.Config{
		Key: "go-local", Class: repo.Local, PackageType: repo.Go,
	})); err != nil {
		t.Fatal(err)
	}
	grant(t, w, "deploy-go", "deployer", "go-local", types.ActionRead|types.ActionWrite)
	w.put("/artifactory/libs-local/taken.txt", "taken")
	const info = "/artifactory/api/go/go-local/example.com/m/@v/v1.0.0.info"
	wantStatus(t, w.do(http.MethodPut, info, "admin", `{"Version":"v1.0.0"}`), http.StatusCreated)

	rec := w.do(http.MethodPut, "/artifactory/api/pkgtest/libs-local/files/taken.txt", "deployer",
		"changed")
	if got := artifactoryMessage(t, rec); rec.Code != http.StatusForbidden ||
		got != refusedOverwrite("libs-local", "taken.txt", "deployer") || !skipsExisting(rec) {
		t.Errorf("router kit: %d %q", rec.Code, got)
	}

	rec = w.do(
		http.MethodPut,
		info,
		"deployer",
		`{"Version":"v1.0.0","Time":"2026-10-20T00:00:00Z"}`,
	)
	want := refusedOverwrite("go-local", "example.com/m/@v/v1.0.0.info", "deployer") + "\n"
	if rec.Code != http.StatusForbidden || rec.Body.String() != want ||
		!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Errorf("go: %d %q %s", rec.Code, rec.Body, rec.Header().Get("Content-Type"))
	}
}

// A copy or a move onto a file the caller may not delete fails in its
// report, as any item of it may, with the mover's own wording, which names
// the target and no user.
func TestAccOverwriteRefusalTransfer(t *testing.T) {
	w := newItemWorld(t, false)
	// deployer may move out of other-local, which takes Delete there, and
	// deploy to libs-local, but not delete there.
	grant(t, w, "move-other", "deployer", "other-local",
		types.ActionRead|types.ActionWrite|types.ActionDelete)
	w.put("/artifactory/libs-local/taken.txt", "taken")
	w.put("/artifactory/other-local/m.txt", "moved")
	for _, path := range []string{
		"/artifactory/api/copy/other-local/m.txt?to=/libs-local/taken.txt",
		"/artifactory/api/move/other-local/m.txt?to=/libs-local/taken.txt",
	} {
		rec := w.do(http.MethodPost, path, "deployer", "")
		wantStatus(t, rec, http.StatusOK)
		m := report(t, rec.Body.Bytes())
		const want = "User doesn't have permissions to override 'libs-local:taken.txt'. " +
			"Needs delete permissions."
		if len(m) != 2 || m[0]["level"] != item.LevelError || m[0]["message"] != want {
			t.Errorf("%s: %v", path, m)
		}
	}
	got := w.do(http.MethodGet, "/artifactory/libs-local/taken.txt", "admin", "")
	if got.Body.String() != "taken" {
		t.Fatalf("a refused copy or move replaced the file: %q", got.Body)
	}
	wantStatus(
		t,
		w.do(http.MethodGet, "/artifactory/other-local/m.txt", "admin", ""),
		http.StatusOK,
	)
}

// overwriter is the status fixture's user who may deploy but not delete:
// in libs-local under overwrite/, where seedOverwriteStatus puts the files
// its rows would replace, and in the formats' repositories their parts
// grant it.
const overwriter = "overwriter:pw"

// seedOverwriteStatus adds overwriter, its grant in libs-local, and the
// files there its rows act on: taken.txt, which they would replace, and
// other.txt, whose content a checksum deploy would put there.
func seedOverwriteStatus(t *testing.T, db store.Store, items *item.Service) {
	t.Helper()
	ctx := context.Background()
	name, _, _ := strings.Cut(overwriter, ":")
	if err := db.Users().Create(ctx, types.User{Name: name, PasswordHash: cheapHash}); err != nil {
		t.Fatal(err)
	}
	grantOverwriter(t, db, "libs-local", "overwrite/")
	libs, err := db.Repos().Get(ctx, "libs-local")
	if err != nil {
		t.Fatal(err)
	}
	for p, body := range map[string]string{
		"overwrite/taken.txt": "taken", "overwrite/other.txt": "other content",
	} {
		if _, err := items.Deploy(ctx, item.Deploy{
			Target: item.Target{Repo: libs, Path: p}, Body: strings.NewReader(body),
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// grantOverwriter lets overwriter read and deploy, but not delete, under
// the prefix in repository key.
func grantOverwriter(t *testing.T, db store.Store, key, under string) {
	t.Helper()
	name, _, _ := strings.Cut(overwriter, ":")
	if err := db.Permissions().Create(context.Background(), types.Permission{
		Name: "st-overwrite-" + key,
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{
				name: types.ActionRead | types.ActionWrite,
			}},
			Targets: map[string]types.Target{key: {Include: []string{under + "**"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
}

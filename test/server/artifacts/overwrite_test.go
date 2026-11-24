package artifacts_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/multipart"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// skipsExisting reports whether Poetry's publish --skip-existing takes rec
// for a file that is already there, by the rule it keeps for Artifactory:
// status == 403 and "overwrite artifact" in the reason phrase and the body,
// lower-cased and joined (Poetry 2.5.1, src/poetry/publishing/uploader.py,
// _is_file_exists_error; twine 5.1.1's skip_upload, until 6.2.0, asks the
// same of the body). 7.x's text since 7.111, "delete/overwrite all artifacts
// under", no longer matches, so neither client skips an existing file on 7.x
// (pypa/twine#1251), and the refusals below must not match either (#1916).
func skipsExisting(rec *httptest.ResponseRecorder) bool {
	reasonAndText := strings.ToLower(http.StatusText(rec.Code) + rec.Body.String())
	return rec.Code == http.StatusForbidden && strings.Contains(reasonAndText, "overwrite artifact")
}

// refusedOverwrite is 7.161.26's REQUIRES_DELETE_PERMISSION text for an
// overwrite of repoKey/path refused to user.
func refusedOverwrite(repoKey, path, user string) string {
	return fmt.Sprintf(
		"Artifact deletion error: Not enough permissions to delete/overwrite all artifacts under "+
			"'%s/%s' (user: '%s' needs DELETE permission).",
		repoKey,
		path,
		user,
	)
}

// hiddenOverwrite is 7.161.26's ARTIFACT_NOT_FOUND text, which the item
// deploy route answers in place of refusedOverwrite while the global
// hideUnauthorizedResources is on.
func hiddenOverwrite(repoKey, path string) string {
	return fmt.Sprintf("Artifact deletion error: Could not locate artifact. Path: '%s/%s'", repoKey, path)
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
// 7.x's text, which names the file where it is stored and the caller
// (#565, #1916): an upload, one that declares its content, a checksum
// deploy, the checksum deploy that ends a multipart upload, a deploy
// through a virtual repository, which names the repository it lands in,
// and an archive, which names the entry in the way. The file stays as it
// was.
func TestAccOverwriteRefusalText(t *testing.T) {
	w := newItemWorld(t, false, func(c *Config) {
		c.Multipart = &multipart.Service{
			Store: c.Store, Blobs: c.Blobs, Dir: t.TempDir(),
			Restrictions: restrictionsOver(t, c.Store),
		}
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
		if skipsExisting(rec) {
			t.Errorf("%s: %d %s matches the pre-7.111 text clients skip on", c.name, rec.Code, rec.Body)
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
	grant(t, w, "anonymous-deploys", kernel.AnonymousUser, "libs-local",
		api.ActionRead|api.ActionWrite)
	w.put("/artifactory/libs-local/taken.txt", "taken")
	want := refusedOverwrite("libs-local", "taken.txt", kernel.AnonymousUser)
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
	grant(t, w, "deploy-go", "deployer", "go-local", api.ActionRead|api.ActionWrite)
	w.put("/artifactory/libs-local/taken.txt", "taken")
	const info = "/artifactory/api/go/go-local/example.com/m/@v/v1.0.0.info"
	wantStatus(t, w.do(http.MethodPut, info, "admin", `{"Version":"v1.0.0"}`), http.StatusCreated)

	rec := w.do(http.MethodPut, "/artifactory/api/pkgtest/libs-local/files/taken.txt", "deployer",
		"changed")
	if got := artifactoryMessage(t, rec); rec.Code != http.StatusForbidden ||
		got != refusedOverwrite("libs-local", "taken.txt", "deployer") || skipsExisting(rec) {
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

// TestAccOverwriteWithoutDelete (#1916): a checksum file, Maven metadata
// and a Maven non-unique snapshot are replaced by a caller that may deploy
// but not delete, on the item route and through the router kit, as 7.161.26's
// shouldProtectPathDeletion leaves them unprotected (DbStoringRepoMixin
// 932-966); a unique snapshot and any other file are still refused.
func TestAccOverwriteWithoutDelete(t *testing.T) {
	w := newItemWorld(t, false, func(c *Config) {
		c.Protocols = append(c.Protocols, pkgtestProtocol())
	})
	for _, c := range []struct {
		path     string
		replaced bool
	}{
		// No file a.bin, so the .sha1 is a file of its own.
		{"g/a.bin.sha1", true},
		{"g/a/maven-metadata.xml", true},
		{"g/a/1.0-SNAPSHOT/a-1.0-SNAPSHOT.jar", true},
		{"g/a/1.0-SNAPSHOT/a-1.0-20261124.040638-2.jar", false},
		{"g/a/1.0/a-1.0.jar", false},
	} {
		for _, prefix := range []string{"/artifactory/libs-local/", "/artifactory/api/pkgtest/libs-local/files/"} {
			w.put("/artifactory/libs-local/"+c.path, "one")
			rec := w.do(http.MethodPut, prefix+c.path, "deployer", "two")
			want, body := http.StatusForbidden, "one"
			if c.replaced {
				want, body = http.StatusCreated, "two"
			}
			if rec.Code != want {
				t.Errorf("%s%s: %d %s, want %d", prefix, c.path, rec.Code, rec.Body, want)
			}
			if got := w.do(http.MethodGet, "/artifactory/libs-local/"+c.path, "admin", ""); got.Body.String() != body {
				t.Errorf("%s%s: holds %q, want %q", prefix, c.path, got.Body, body)
			}
		}
	}
}

// TestAccOverwriteRefusalHidden (#1916): while the global
// hideUnauthorizedResources is on, switched by PATCH on a running server,
// the item deploy route answers an overwrite without Delete with
// ARTIFACT_NOT_FOUND's 404, as 7.161.26's assertNonRootPathDelete records it
// (3428-3444) and UploadServiceImpl.handleInvalidDeployPathError sends it:
// to an anonymous caller too, with no challenge, since only a 403 earns one.
// It names the file as the refusal does. A package endpoint keeps its
// refusal, and switching the setting off brings the refusals back.
func TestAccOverwriteRefusalHidden(t *testing.T) {
	w := newItemWorld(t, true, func(c *Config) {
		c.Protocols = append(c.Protocols, pkgtestProtocol())
	})
	grant(t, w, "anonymous-deploys", kernel.AnonymousUser, "libs-local",
		api.ActionRead|api.ActionWrite)
	for p, body := range map[string]string{"taken.txt": "taken", "other.txt": "other", "ex/inner.txt": "inner"} {
		w.put("/artifactory/libs-local/"+p, body)
	}
	patch := func(on string) {
		t.Helper()
		wantStatus(t, w.do(http.MethodPatch, "/artifactory/api/system/configuration", "admin",
			"security:\n  hideUnauthorizedResources: "+on+"\n", "Content-Type", "application/yaml"), http.StatusOK)
	}
	type deploy struct {
		name, as, path, body string
		headers              []string
		// file is the path in libs-local the answer names.
		file string
	}
	archive := string(zipBytes(t, map[string]string{"inner.txt": "changed"}))
	routes := []deploy{
		{"upload", "deployer", "libs-local/taken.txt", "changed", nil, "taken.txt"},
		{"upload by anonymous", "", "libs-local/taken.txt", "changed", nil, "taken.txt"},
		{
			"checksum deploy", "deployer", "libs-local/taken.txt", "",
			[]string{"X-Checksum-Deploy", "true", "X-Checksum-Sha1", sha1Hex("other")},
			"taken.txt",
		},
		{"through a virtual repository", "deployer", "virt/taken.txt", "changed", nil, "taken.txt"},
		{"archive", "deployer", "libs-local/ex/bundle.zip", archive, []string{"X-Explode-Archive", "true"}, "ex/inner.txt"},
	}
	refused := func(d deploy) (int, string) {
		if d.as == "" {
			return http.StatusUnauthorized, refusedOverwrite("libs-local", d.file, kernel.AnonymousUser)
		}
		return http.StatusForbidden, refusedOverwrite("libs-local", d.file, d.as)
	}
	check := func(state string, d deploy, prefix string, status int, message string) {
		t.Helper()
		rec := w.do(http.MethodPut, prefix+d.path, d.as, d.body, d.headers...)
		if got := artifactoryMessage(t, rec); rec.Code != status || got != message {
			t.Errorf("%s, %s: %d %q, want %d %q", state, d.name, rec.Code, got, status, message)
		}
		if challenged := rec.Header().Get("WWW-Authenticate") != ""; challenged != (status == http.StatusUnauthorized) {
			t.Errorf("%s, %s: challenge %q", state, d.name, rec.Header().Get("WWW-Authenticate"))
		}
	}
	// The test-only package endpoint deploys through the router kit.
	pkg := deploy{"package endpoint", "deployer", "libs-local/files/taken.txt", "changed", nil, "taken.txt"}
	for _, state := range []string{"false", "true", "false"} {
		patch(state)
		for _, d := range routes {
			status, message := refused(d)
			if state == "true" {
				status, message = http.StatusNotFound, hiddenOverwrite("libs-local", d.file)
			}
			check("hiding "+state, d, "/artifactory/", status, message)
		}
		status, message := refused(pkg)
		check("hiding "+state, pkg, "/artifactory/api/pkgtest/", status, message)
	}
	for p, body := range map[string]string{"taken.txt": "taken", "ex/inner.txt": "inner"} {
		if got := w.do(http.MethodGet, "/artifactory/libs-local/"+p, "admin", ""); got.Body.String() != body {
			t.Errorf("a refused overwrite replaced %s: %q", p, got.Body)
		}
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
		api.ActionRead|api.ActionWrite|api.ActionDelete)
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

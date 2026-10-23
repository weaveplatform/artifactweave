package pypi_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/server"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestPypiUploadBasicToken(t *testing.T) {
	h := statusFixture(t, false, allowBasicTokens)
	created := mintAdmin(
		t,
		h,
		`{"username":"admin","scope":"applied-permissions/user","expires_in":3600}`,
	)
	token, ok := created["access_token"].(string)
	if !ok {
		t.Fatalf("token response: %v", created)
	}
	typ, body := contentTypeOf(pypiUploadBody("token-upload"))
	headers := basic("admin", token)
	headers["Content-Type"] = typ
	wantStatus(t, do(t, h, http.MethodPost, pypiBase+"pypi-local", headers, body), http.StatusOK)
	wantStatus(
		t,
		do(t, h, http.MethodGet, pypiBase+"pypi-local/simple/token-upload/", headers, ""),
		http.StatusOK,
	)
}

// A PUT through the package API uses every item-route branch, including
// checksum reuse authorization and strict identical-checksum policy.
func TestPypiPutUsesItemDeploy(t *testing.T) {
	for _, strict := range []bool{false, true} {
		w := newItemWorld(
			t,
			false,
			func(c *server.Config) { c.EnforceIdenticalChecksumDeploy = strict },
		)
		if err := w.db.Repos().
			Create(context.Background(), *repo.Defaults(&repo.Config{Key: "pypi-local", Class: repo.Local, PackageType: repo.PyPI})); err != nil {
			t.Fatal(err)
		}
		grant(t, w, "pypi-write", "deployer", "pypi-local", types.ActionRead|types.ActionWrite)
		const base = "/artifactory/api/pypi/pypi-local/"
		wantStatus(t, w.do("PUT", base+"data.txt;k=value", "admin", "content"), 201)
		item := w.item("pypi-local", false, "data.txt")
		props, err := w.db.Items().Properties(t.Context(), item.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got := props["k"]; len(got) != 1 || got[0] != "value" {
			t.Fatalf("matrix properties lost: %v", props)
		}
		expected := 201
		if strict {
			expected = 403
		}
		wantStatus(
			t,
			w.do(
				"PUT",
				base+"data.txt",
				"deployer",
				"",
				"X-Checksum-Deploy",
				"true",
				"X-Checksum-Sha256",
				sha256Hex("content"),
			),
			expected,
		)
		wantStatus(t, w.do("PUT", "/artifactory/other-local/secret", "admin", "secret"), 201)
		wantStatus(
			t,
			w.do(
				"PUT",
				base+"guessed.txt",
				"deployer",
				"",
				"X-Checksum-Deploy",
				"true",
				"X-Checksum-Sha256",
				sha256Hex("secret"),
			),
			404,
		)
		wantStatus(t, w.do("PUT", base+"folder/", "deployer", ""), 201)
		if !w.item("pypi-local", false, "folder").Folder {
			t.Fatal("folder PUT became a file")
		}
		wantStatus(t, w.do("PUT", base+"data.txt.sha256", "admin", sha256Hex("content")), 201)
		wantStatus(t, w.do("GET", "/artifactory/pypi-local/data.txt.sha256", "admin", ""), 200)
		wantStatus(t, w.do("PUT", base+"data.txt.sha256", "admin", sha256Hex("wrong")), 409)
		wantStatus(t, w.do("PUT", base+"bad;=value", "admin", "x"), 400)
	}
}

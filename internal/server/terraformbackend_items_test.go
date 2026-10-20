package server

import (
	"bytes"
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/crypt"
	"github.com/weaveplatform/artifactweave/internal/format/formats"
	tfbe "github.com/weaveplatform/artifactweave/internal/format/terraformbackend"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/terraformstate"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// tfbeKeys is the master key the Terraform backend fixtures seal under.
func tfbeKeys(t *testing.T) *crypt.Keyring {
	t.Helper()
	k, err := crypt.New([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// tfbeState is a Terraform state of serial.
func tfbeState(serial int) string {
	return `{"version":4,"serial":` + string(rune('0'+serial)) + `,"lineage":"status"}`
}

// seedTfbe creates workspace app in the Terraform backend repository key
// of db with two states written through the backend's storage, sealed
// under k while the repository's encryptStates says so.
func seedTfbe(t *testing.T, db store.Store, blobs blob.Store, k *crypt.Keyring, key string) {
	t.Helper()
	ctx := context.Background()
	c, err := db.Repos().Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	svc := &terraformstate.Service{
		Items: &item.Service{
			Store: db, Blobs: blobs,
			Formats: formats.Settings{TerraformBackend: tfbe.Settings{Keys: k}}.For,
		},
		Keys: k,
	}
	if _, err := svc.CreateWorkspace(ctx, c, "app", "", "admin"); err != nil {
		t.Fatal(err)
	}
	for serial := 1; serial <= 2; serial++ {
		body := tfbeState(serial)
		if _, err := svc.WriteState(ctx, c, "app", terraformstate.StateWrite{
			Body: strings.NewReader(body), MD5: tfbe.MD5([]byte(body)), Serial: int64(serial),
			By: "admin",
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// The item routes read a Terraform backend repository as stored, so a
// sealed state downloads as its envelope and a plain one as it is; they
// write nothing into it but deletes (decision 0041, "The item routes").
func TestTerraformBackendThroughTheItemRoutes(t *testing.T) {
	k := tfbeKeys(t)
	w := newItemWorld(t, false, func(c *Config) {
		for _, r := range []repo.Config{
			{Key: "tfbe-local", Class: repo.Local, PackageType: repo.TerraformBackend},
			{
				Key: "tfbe-plain", Class: repo.Local, PackageType: repo.TerraformBackend,
				EncryptStates: new(bool),
			},
		} {
			if err := c.Store.Repos().Create(context.Background(), *repo.Defaults(&r)); err != nil {
				t.Fatal(err)
			}
		}
		c.FormatSettings = formats.Settings{TerraformBackend: tfbe.Settings{Keys: k}}
	})
	seedTfbe(t, w.db, w.blobs, k, "tfbe-local")
	seedTfbe(t, w.db, w.blobs, k, "tfbe-plain")

	sealed := w.do(
		http.MethodGet,
		"/artifactory/tfbe-local/workspaces/app/state.latest.json",
		"admin",
		"",
	)
	wantStatus(t, sealed, http.StatusOK)
	if !crypt.IsSealed(sealed.Body.Bytes()) ||
		bytes.Contains(sealed.Body.Bytes(), []byte("lineage")) {
		t.Fatalf("the sealed state downloads as %q", sealed.Body)
	}
	// The storage API describes what is stored: the envelope's size and
	// checksums.
	info := w.do(http.MethodGet,
		"/artifactory/api/storage/tfbe-local/workspaces/app/state.latest.json", "admin", "")
	if !strings.Contains(info.Body.String(), `"size":"`+strconv.Itoa(sealed.Body.Len())+`"`) ||
		!strings.Contains(info.Body.String(), sha256Hex(sealed.Body.String())) {
		t.Fatalf("the storage API describes the sealed state as %s", info.Body)
	}
	const plainPath = "/artifactory/tfbe-plain/workspaces/app/state.latest.json"
	plain := w.do(http.MethodGet, plainPath, "admin", "")
	wantStatus(t, plain, http.StatusOK)
	if plain.Body.String() != tfbeState(2) {
		t.Fatalf("the plain state downloads as %q", plain.Body)
	}
	record := w.do(
		http.MethodGet,
		"/artifactory/tfbe-local/workspaces/app/workspace.json",
		"admin",
		"",
	)
	if record.Code != http.StatusOK || !strings.Contains(record.Body.String(), `"name":"app"`) {
		t.Fatalf("workspace.json downloads as %d %q", record.Code, record.Body)
	}

	const refusal = "Repository 'tfbe-local' is a Terraform backend repository; " +
		"its content is written only through the Terraform backend API"
	for name, rec := range map[string]interface{ Result() *http.Response }{
		"a deploy": w.do(http.MethodPut, "/artifactory/tfbe-local/workspaces/app/state.latest.json",
			"admin", tfbeState(3)),
		"a new file": w.do(http.MethodPut, "/artifactory/tfbe-local/workspaces/new/workspace.json",
			"admin", "{}"),
		"a folder": w.do(http.MethodPut, "/artifactory/tfbe-local/workspaces/new/", "admin", ""),
		"the lock property": w.do(http.MethodPut,
			"/artifactory/api/storage/tfbe-local/workspaces/app?properties=locked.by=mallory&recursive=0",
			"admin", ""),
	} {
		res := rec.Result()
		var body bytes.Buffer
		_, _ = body.ReadFrom(res.Body)
		if res.StatusCode != http.StatusBadRequest ||
			!strings.Contains(body.String(), "Terraform") {
			t.Errorf("%s: %d %s", name, res.StatusCode, body.String())
		}
		if name == "a deploy" && !strings.Contains(body.String(), refusal) {
			t.Errorf("a deploy's refusal: %s", body.String())
		}
	}
	other := w.do(
		http.MethodPut,
		"/artifactory/api/storage/tfbe-local/workspaces/app?properties=team=infra&recursive=0",
		"admin",
		"",
	)
	wantStatus(t, other, http.StatusNoContent)

	// A copy reports the refused file, as it reports any it could not land.
	w.do(http.MethodPut, "/artifactory/libs-local/a.json", "admin", tfbeState(3))
	cp := w.do(
		http.MethodPost,
		"/artifactory/api/copy/libs-local/a.json?to=/tfbe-local/workspaces/app/state.latest.json",
		"admin",
		"",
	)
	if !strings.Contains(
		cp.Body.String(),
		`"level":"ERROR","message":"Cannot transfer libs-local:a.json`+
			` to tfbe-local:workspaces/app/state.latest.json: `+refusal,
	) {
		t.Fatalf("a copy into the repository: %d %s", cp.Code, cp.Body)
	}
	if got := w.do(
		http.MethodGet,
		"/artifactory/tfbe-local/workspaces/app/state.latest.json",
		"admin",
		"",
	); !bytes.Equal(
		got.Body.Bytes(),
		sealed.Body.Bytes(),
	) {
		t.Fatal("a refused copy changed the current state")
	}

	// A delete is allowed, into the trash can, as for any item.
	wantStatus(
		t,
		w.do(http.MethodDelete, "/artifactory/tfbe-local/workspaces/app/state.latest.json",
			"admin", ""),
		http.StatusNoContent,
	)
}

// The v1 read of a local repository of any package type sends
// encryptStates, as 7.x's read of a Maven repository does (RTFACT-31100);
// the v2 read keeps it for the Terraform types alone.
func TestEncryptStatesInTheV1Read(t *testing.T) {
	w := newItemWorld(t, false)
	v1 := w.do(http.MethodGet, "/artifactory/api/repositories/libs-local", "admin", "")
	wantStatus(t, v1, http.StatusOK)
	if !strings.Contains(v1.Body.String(), `"encryptStates":true`) {
		t.Fatalf("the v1 read of a generic local repository: %s", v1.Body)
	}
	v2 := w.do(http.MethodGet, "/artifactory/api/v2/repositories/libs-local", "admin", "")
	wantStatus(t, v2, http.StatusOK)
	if strings.Contains(v2.Body.String(), "encryptStates") {
		t.Fatalf("the v2 read of a generic local repository: %s", v2.Body)
	}
	remote := w.do(http.MethodGet, "/artifactory/api/repositories/up", "admin", "")
	if strings.Contains(remote.Body.String(), "encryptStates") {
		t.Fatalf("the v1 read of a remote repository: %s", remote.Body)
	}
}

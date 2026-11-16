package artifacts_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/multipart"
)

// Writes addressed to a virtual repository on the item, multipart and
// transfer routes land in, or are refused, as 7.x does it (decision 0008,
// R6; #1534). The item world's virt deploys to libs-local and
// virt-nodeploy names no deployment repository.

// noTarget is 7.x's message for a deploy to the virtual repository key
// without a local deployment repository.
func noTarget(key string) string {
	return "No local repository was configured as local deployment repository for the (" +
		key + ") virtual repository."
}

// wantError checks the status, the Allow header and the Artifactory error
// envelope of rec.
func wantError(
	t *testing.T,
	what string,
	rec *httptest.ResponseRecorder,
	status int,
	msg, allow string,
) {
	t.Helper()
	var got struct {
		Errors []struct {
			Status  int    `json:"status"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if rec.Code != status || rec.Header().Get("Allow") != allow ||
		json.Unmarshal(rec.Body.Bytes(), &got) != nil || len(got.Errors) != 1 ||
		got.Errors[0].Status != status || got.Errors[0].Message != msg {
		t.Errorf("%s: %d, Allow %q: %s; want %d %q, Allow %q", what, rec.Code,
			rec.Header().Get("Allow"), rec.Body, status, msg, allow)
	}
}

// virtualDeploying adds the virtual repository key over libs-local and up,
// naming ddr as its default deployment repository without the save's
// checks, as a configuration stored without them would.
func (w *itemWorld) virtualDeploying(key, ddr string) {
	w.t.Helper()
	if err := w.db.Repos().Create(w.t.Context(), *repo.Defaults(&repo.Config{
		Key: key, Class: repo.Virtual, Repositories: []string{"libs-local", "up"},
		DefaultDeploymentRepo: ddr,
	})); err != nil {
		w.t.Fatal(err)
	}
}

// A deploy through a virtual repository lands in its local or federated
// default deployment repository, and one through a virtual repository
// without one is refused before any permission is checked.
func TestAccDeployThroughVirtual(t *testing.T) {
	w := newItemWorld(t, false)
	// deployer may write libs-local and nothing else, the virtual
	// repository included: Write is checked on the deployment
	// repository's path.
	rec := w.do(http.MethodPut, "/artifactory/virt/d/v.txt;team=core", "deployer", "v")
	wantStatus(t, rec, http.StatusCreated)
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	const uri = "https://reg.example/artifactory/libs-local/d/v.txt"
	if body["repo"] != "libs-local" || body["path"] != "/d/v.txt" || body["uri"] != uri ||
		body["downloadUri"] != uri || rec.Header().Get("Location") != uri {
		t.Fatalf("deploy through virt answered %v, Location %q", body, rec.Header().Get("Location"))
	}
	it := w.item("libs-local", false, "d/v.txt")
	props, err := w.db.Items().Properties(t.Context(), it.ID)
	if err != nil || it.CreatedBy != "deployer" || !props.Has("team", "core") {
		t.Fatalf("stored %+v with %v, %v", it, props, err)
	}
	// Replacing the file needs Delete on the deployment repository's
	// path, which deployer lacks, and the refusal names that repository.
	wantError(t, "overwrite through virt",
		w.do(http.MethodPut, "/artifactory/virt/d/v.txt", "deployer", "changed"),
		http.StatusForbidden,
		"Not enough permissions to delete/overwrite artifact 'libs-local:d/v.txt' "+
			"(user: 'deployer' needs DELETE permission).", "")
	// user may write nowhere.
	wantStatus(
		t,
		w.do(http.MethodPut, "/artifactory/virt/u.txt", "user", "u"),
		http.StatusForbidden,
	)

	// A checksum deploy and a folder land there too.
	w.put("/artifactory/other-local/known.bin", "known")
	rec = w.do(http.MethodPut, "/artifactory/virt/sum/known.bin", "admin", "",
		"X-Checksum-Deploy", "true", "X-Checksum-Sha1", sha1Hex("known"))
	wantStatus(t, rec, http.StatusCreated)
	if w.item("libs-local", false, "sum/known.bin").Size != int64(len("known")) {
		t.Fatal("the checksum deploy through virt stored nothing in libs-local")
	}
	wantStatus(
		t,
		w.do(http.MethodPut, "/artifactory/virt/folder/", "admin", ""),
		http.StatusCreated,
	)
	if !w.item("libs-local", false, "folder").Folder {
		t.Fatal("the folder through virt is not in libs-local")
	}
	// So does an archive explode, under the archive's folder.
	rec = w.do(http.MethodPut, "/artifactory/virt/x/a.zip", "admin",
		string(zipBytes(t, map[string]string{"in/f.txt": "f"})), "X-Explode-Archive", "true")
	wantStatus(t, rec, http.StatusCreated)
	if w.item("libs-local", false, "x/in/f.txt").Size != 1 {
		t.Fatal("the explode through virt stored nothing in libs-local")
	}

	w.virtualDeploying("virt-gone", "gone")
	w.virtualDeploying("virt-remote", "up")
	w.virtualDeploying("virt-virtual", "virt")
	for _, key := range []string{"virt-nodeploy", "virt-gone", "virt-remote", "virt-virtual"} {
		// The target is resolved before any permission is checked, so a
		// caller with no rights gets the same answer.
		for _, as := range []string{"admin", "user"} {
			wantError(t, "PUT "+key+" as "+as,
				w.do(http.MethodPut, "/artifactory/"+key+"/a.txt", as, "a"),
				http.StatusMethodNotAllowed, noTarget(key), http.MethodGet)
		}
		wantError(t, "checksum deploy to "+key,
			w.do(http.MethodPut, "/artifactory/"+key+"/a.txt", "admin", "",
				"X-Checksum-Deploy", "true", "X-Checksum-Sha1", sha1Hex("a")),
			http.StatusMethodNotAllowed, noTarget(key), http.MethodGet)
		wantError(t, "properties to "+key,
			w.do(http.MethodPut, "/artifactory/"+key+"/a.txt;k=v", "admin", "a"),
			http.StatusMethodNotAllowed, noTarget(key), http.MethodGet)
	}
	if _, err := w.db.Items().Get(t.Context(), "up", true, "a.txt"); err == nil {
		t.Fatal("a deploy through a virtual to a remote repository stored a file")
	}
}

func TestAccDeployToRemoteKeyFindsNoLocalRepository(t *testing.T) {
	w := newItemWorld(t, false)
	for _, key := range []string{"up", "up-cache"} {
		for _, as := range []string{"admin", "user"} {
			wantError(
				t,
				"PUT "+key+" as "+as,
				w.do(http.MethodPut, "/artifactory/"+key+"/a.txt", as, "a"),
				http.StatusNotFound,
				"Could not find a local repository named "+key+" to deploy to.",
				"",
			)
		}
	}
}

func TestAccVirtualMultipartCreate(t *testing.T) {
	w := newItemWorld(t, false, func(c *Config) {
		c.Multipart = &multipart.Service{
			Store: c.Store, Blobs: c.Blobs, Dir: t.TempDir(),
			Restrictions: restrictionsOver(t, c.Store),
		}
	})
	// The CLI's upload through virt, with deployer's Write on libs-local
	// alone, ends in the checksum deploy through virt that stores the file
	// in libs-local.
	data := "uploaded through a virtual repository"
	token := uploadInPartsTo(t, w, "deployer", "virt", "big/data.bin", data)
	rec := w.do(http.MethodPut, "/artifactory/virt/big/data.bin", "deployer", "",
		"X-Checksum-Deploy", "true", "X-Checksum-Sha1", sha1Hex(data),
		"X-Checksum-Deploy-Token", token)
	wantStatus(t, rec, http.StatusCreated)
	if w.item("libs-local", false, "big/data.bin").Size != int64(len(data)) {
		t.Fatal("the multipart upload through virt stored nothing in libs-local")
	}
	wantStatus(t, w.do(http.MethodPost,
		uploadsCreate+"?repoKey=virt&repoPath=big%2Fother.bin&partSizeMB=1", "user", ""),
		http.StatusForbidden)
	wantError(t, "create through virt-nodeploy",
		w.do(
			http.MethodPost,
			uploadsCreate+"?repoKey=virt-nodeploy&repoPath=big%2Fdata.bin&partSizeMB=1",
			"admin",
			"",
		),
		http.StatusMethodNotAllowed, noTarget("virt-nodeploy"), http.MethodGet)
}

func TestAccDeleteThroughVirtual(t *testing.T) {
	w := newItemWorld(t, false)
	w.put("/artifactory/libs-local/d/f.txt", "f")
	w.cacheFile("up", "cached.txt", "c")
	// 7.x deletes only in local, federated and cache repositories, and
	// answers any other key 404 with no body before any permission check.
	for _, p := range []string{"/artifactory/virt/d/f.txt", "/artifactory/virt/absent"} {
		for _, as := range []string{"admin", "user"} {
			rec := w.do(http.MethodDelete, p, as, "")
			if rec.Code != http.StatusNotFound || rec.Body.Len() != 0 {
				t.Errorf("DELETE %s as %s: %d %q", p, as, rec.Code, rec.Body)
			}
		}
	}
	w.item("libs-local", false, "d/f.txt")
	wantStatus(t, w.do(http.MethodDelete, "/artifactory/up-cache/cached.txt", "admin", ""),
		http.StatusNoContent)
}

func TestAccCopyAndMoveIntoRepositoriesThatHoldNoDeploys(t *testing.T) {
	w := newItemWorld(t, false)
	w.put("/artifactory/libs-local/d/a.txt", "a")
	off := false
	if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&repo.Config{
		Key: "nostore", Class: repo.Remote, URL: "https://nostore.example",
		StoreArtifactsLocally: &off,
	})); err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"copy", "move"} {
		for target, msg := range map[string]string{
			"virt":          "Target repository virt is a virtual repository. " + verb + " to virtual repositories is not allowed.",
			"virt-nodeploy": "Target repository virt-nodeploy is a virtual repository. " + verb + " to virtual repositories is not allowed.",
			"up":            "Target repository up is a remote repository. " + verb + " to remote repositories is not allowed.",
			"up-cache":      "Target repository up-cache is a cache repository. " + verb + " to cache repositories is not allowed.",
			// Only a remote repository that stores artifacts has a cache.
			"nostore-cache":    "The target repository 'nostore-cache' does not exist",
			"libs-local-cache": "The target repository 'libs-local-cache' does not exist",
			"missing":          "The target repository 'missing' does not exist",
		} {
			rec := w.do(http.MethodPost,
				"/artifactory/api/"+verb+"/libs-local/d/a.txt?to=/"+target+"/x.txt", "admin", "")
			msgs := report(t, rec.Body.Bytes())
			if rec.Code != http.StatusBadRequest ||
				rec.Header().Get("Content-Type") != copyOrMoveType ||
				len(msgs) != 1 || msgs[0]["level"] != "ERROR" || msgs[0]["message"] != msg {
				t.Errorf("%s to %s: %d %s", verb, target, rec.Code, rec.Body)
			}
		}
		// A copy or move out of a virtual repository keeps its refusal.
		rec := w.do(http.MethodPost,
			"/artifactory/api/"+verb+"/virt/d/a.txt?to=/other-local/x.txt", "admin", "")
		if msgs := report(t, rec.Body.Bytes()); rec.Code != http.StatusBadRequest ||
			len(
				msgs,
			) != 1 || msgs[0]["message"] != "Cannot "+verb+" from the virtual repository 'virt'" {
			t.Errorf("%s from virt: %d %s", verb, rec.Code, rec.Body)
		}
	}
	w.item("libs-local", false, "d/a.txt")
}

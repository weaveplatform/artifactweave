package gitlfshttp_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
)

// lfsAnswer is one answer under /artifactory/api/lfs/ with its headers,
// for the answers that carry a challenge or Allow.
type lfsAnswer struct {
	status int
	header http.Header
	body   string
}

func lfsAnswerOf(t *testing.T, server *httptest.Server, method, path, user, body string) lfsAnswer {
	t.Helper()
	req, err := http.NewRequest(method, server.URL+"/artifactory/api/lfs/"+path,
		strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasSuffix(path, "/objects/batch") || strings.Contains(path, "/locks") {
		req.Header.Set("Content-Type", "application/vnd.git-lfs+json")
		req.Header.Set("Accept", "application/vnd.git-lfs+json")
	}
	if user != "" {
		req.Header.Set("X-User", user)
	}
	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return lfsAnswer{status: res.StatusCode, header: res.Header, body: string(data)}
}

func batchBody(operation, oid string, size int) string {
	return `{"operation":"` + operation + `","objects":[{"oid":"` + oid + `","size":` +
		strconv.Itoa(size) + `}]}`
}

func createLFS(t *testing.T, db *inmem.Store, c repo.Config) {
	t.Helper()
	c.PackageType = repo.GitLFS
	if err := db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
		t.Fatal(err)
	}
}

// A virtual repository resolves through a nested virtual member and skips a
// cycle back to itself, a self-membership and a repeated member without an
// error (decision 0008, R2), where each answered 400 "unsupported Git LFS
// member" or "invalid Git LFS virtual member". A member that misses the
// object passes over (R3), a member whose object has another size ends the
// walk with its 422 rather than passing over, and a nested virtual
// repository whose own patterns reject the object's path is dropped from
// the walk.
func TestUnitVirtualFlattensNestedMembersSkippingCyclesAndDuplicates(t *testing.T) {
	f := newFixture(t, repo.Virtual)
	body := []byte("nested virtual Git LFS bytes\n")
	oid, _ := seedLFS(t, f.server, "lfs-local", body)
	createLFS(t, f.db, repo.Config{Key: "lfs-miss", Class: repo.Local})
	createLFS(t, f.db, repo.Config{
		Key: "lfs-inner", Class: repo.Virtual,
		Repositories: []string{"lfs", "lfs-inner", "lfs-miss", "lfs-local", "lfs-local"},
	})
	patchLFS(t, f.db, "lfs", func(c *repo.Config) {
		c.Repositories = []string{"lfs-inner", "lfs", "lfs-inner", "lfs-local"}
	})
	transfer := "lfs/objects/" + oid + "?size=" + strconv.Itoa(len(body))
	for _, user := range []string{"", "reader"} {
		reply := policyBatch(t, f.server, "lfs", user, "download", oid, len(body))
		if got := reply.Objects[0]; got.Error != nil || got.Actions["download"].Href == "" {
			t.Fatalf("%q: nested batch: %+v", user, got)
		}
		status, answer := lfsCall(t, f.server, http.MethodGet, transfer, user, "")
		wantAnswer(t, status, answer, http.StatusOK, string(body))
	}
	wantObjectError(t, policyBatch(t, f.server, "lfs", "reader", "download", oid, len(body)+1),
		http.StatusUnprocessableEntity)
	excluded := "objects/**"
	patchLFS(t, f.db, "lfs-inner", func(c *repo.Config) { c.ExcludesPattern = &excluded })
	patchLFS(t, f.db, "lfs", func(c *repo.Config) { c.Repositories = []string{"lfs-inner"} })
	status, answer := lfsCall(t, f.server, http.MethodGet, transfer, "", "")
	wantAnswer(t, status, answer, http.StatusNotFound, "Git LFS object unavailable")
}

// A member whose upstream fails passes over and is logged (decision 0008,
// R3) [I], where the fault ended the walk with the object's 502: a later
// member serves the object. A virtual repository whose only holder is a
// remote member serves it from that member (RTFACT-14494), and a member
// that merely misses is not logged.
func TestUnitVirtualPassesOverAFaultyMemberAndLogsIt(t *testing.T) {
	f := newRemoteFixture(t)
	f.available.Store(false)
	createLFS(t, f.db, repo.Config{Key: "lfs-local", Class: repo.Local})
	if oid, _ := seedLFS(t, f.server, "lfs-local", f.object); oid != f.oid {
		t.Fatalf("seeded %s, want %s", oid, f.oid)
	}
	// Remote members come after local ones (R1), so the holder behind the
	// faulty remote is a second remote, whose upstream is this server's
	// lfs-local.
	createLFS(t, f.db, repo.Config{
		Key: "lfs-mirror", Class: repo.Remote, URL: f.server.URL + "/artifactory/api/lfs/lfs-local",
	})
	createLFS(t, f.db, repo.Config{
		Key: "lfs-virtual", Class: repo.Virtual, Repositories: []string{"lfs", "lfs-mirror"},
	})
	reply := policyBatch(t, f.server, "lfs-virtual", "", "download", f.oid, len(f.object))
	if got := reply.Objects[0]; got.Error != nil || got.Actions["download"].Href == "" {
		t.Fatalf("batch past a faulty member: %+v", got)
	}
	transfer := "/objects/" + f.oid + "?size=" + strconv.Itoa(len(f.object))
	status, answer := lfsCall(t, f.server, http.MethodGet, "lfs-virtual"+transfer, "", "")
	wantAnswer(t, status, answer, http.StatusOK, string(f.object))
	logs := f.logs.String()
	if !strings.Contains(logs, "level=WARN") ||
		!strings.Contains(logs, "gitlfs: virtual repository member passed over") ||
		!strings.Contains(logs, "virtual=lfs-virtual member=lfs ") {
		t.Fatalf("fault not logged: %s", logs)
	}

	f.available.Store(true)
	createLFS(t, f.db, repo.Config{Key: "lfs-hollow", Class: repo.Local})
	createLFS(t, f.db, repo.Config{
		Key: "lfs-pull", Class: repo.Virtual, Repositories: []string{"lfs-hollow", "lfs"},
	})
	status, answer = lfsCall(t, f.server, http.MethodGet, "lfs-pull"+transfer, "", "")
	wantAnswer(t, status, answer, http.StatusOK, string(f.object))
	if f.objects.Load() == 0 {
		t.Fatal("remote member's upstream never asked")
	}
	if strings.Contains(f.logs.String(), "member=lfs-hollow") {
		t.Fatalf("a miss was logged as a fault: %s", f.logs.String())
	}
}

// An authenticated caller who may read the object in no member gets its 404,
// as for a member without it (decision 0008, R3), where it got 403 "Git LFS
// read denied". An anonymous caller gets the 401 challenge for the whole
// batch and for the transfer (R4) [I], unless the virtual repository hides
// unauthorized resources or no member's policy admits the path, when it gets
// the 404 too. An anonymous caller who may read a member gets the 404 of an
// object no member holds.
func TestUnitVirtualUnservedAnswersPerCaller(t *testing.T) {
	f := newFixture(t, repo.Virtual)
	body := []byte("unserved virtual Git LFS bytes\n")
	oid, _ := seedLFS(t, f.server, "lfs-local", body)
	transfer := "lfs/objects/" + oid + "?size=" + strconv.Itoa(len(body))
	batch := batchBody("download", oid, len(body))
	wantNotFound := func(user string) {
		t.Helper()
		wantObjectError(t, policyBatch(t, f.server, "lfs", user, "download", oid, len(body)),
			http.StatusNotFound)
		status, answer := lfsCall(t, f.server, http.MethodGet, transfer, user, "")
		wantAnswer(t, status, answer, http.StatusNotFound, "Git LFS object unavailable")
	}
	wantChallenge := func() {
		t.Helper()
		for _, got := range []lfsAnswer{
			lfsAnswerOf(t, f.server, http.MethodPost, "lfs/objects/batch", kernel.AnonymousUser, batch),
			lfsAnswerOf(t, f.server, http.MethodGet, transfer, kernel.AnonymousUser, ""),
		} {
			if got.status != http.StatusUnauthorized ||
				got.header.Get("WWW-Authenticate") != apierror.BasicChallenge ||
				strings.Contains(got.body, oid) {
				t.Fatalf("anonymous: status=%d header=%v body=%s", got.status, got.header, got.body)
			}
		}
	}
	wantNotFound("intruder")
	// narrow may read lfs-local, but not the object's path.
	if err := f.db.Permissions().Create(t.Context(), accesstest.Permission{
		Name: "lfs-narrow", Resources: map[api.ResourceType]accesstest.Resource{
			api.ResourceArtifact: {
				Actions: accesstest.Grants{Users: map[string]api.Actions{"narrow": api.ActionRead}},
				Targets: map[string]accesstest.Target{"lfs-local": {Include: []string{"other/**"}}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	wantNotFound("narrow")
	wantChallenge()
	hide := true
	patchLFS(t, f.db, "lfs", func(c *repo.Config) { c.HideUnauthorizedResources = &hide })
	wantNotFound(kernel.AnonymousUser)
	hide = false
	patchLFS(t, f.db, "lfs", func(c *repo.Config) {
		c.HideUnauthorizedResources, c.Repositories = &hide, []string{"lfs-empty"}
	})
	wantNotFound(kernel.AnonymousUser)
	patchLFS(t, f.db, "lfs", func(c *repo.Config) {
		c.Repositories = []string{"lfs-empty", "lfs-local"}
	})
	wantChallenge()
	if err := f.db.Permissions().Create(t.Context(), accesstest.Permission{
		Name: "lfs-anonymous", Resources: map[api.ResourceType]accesstest.Resource{
			api.ResourceArtifact: {
				Actions: accesstest.Grants{Users: map[string]api.Actions{
					kernel.AnonymousUser: api.ActionRead,
				}},
				Targets: map[string]accesstest.Target{"lfs-local": {Include: []string{"objects/**"}}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	status, answer := lfsCall(t, f.server, http.MethodGet, transfer, kernel.AnonymousUser, "")
	wantAnswer(t, status, answer, http.StatusOK, string(body))
	absent := strings.Repeat("b", 64)
	wantObjectError(t, policyBatch(t, f.server, "lfs", kernel.AnonymousUser, "download", absent, 1),
		http.StatusNotFound)
}

// An upload through a virtual repository lands in its default deployment
// repository with Write asked there alone, though it is no member (7.x's
// UploadServiceUtils.getTargetRepository looks it up among local and
// federated repositories). Without one, a remote or a virtual one, the batch
// and the transfer answer 7.x's 405 with Allow: GET and its message before
// any permission is asked (decision 0008, R6), where they answered a batch
// object's 405, or 400 "invalid Git LFS deployment target".
func TestUnitVirtualDeploysToTheDefaultDeploymentRepository(t *testing.T) {
	body := []byte("deployed through a virtual Git LFS repository\n")
	createRemote := func(t *testing.T, f *fixture) {
		t.Helper()
		createLFS(t, f.db, repo.Config{
			Key: "lfs-remote", Class: repo.Remote, URL: "https://lfs.example.invalid/",
		})
	}
	createInner := func(t *testing.T, f *fixture) {
		t.Helper()
		createLFS(t, f.db, repo.Config{
			Key: "lfs-inner", Class: repo.Virtual, Repositories: []string{"lfs-local"},
		})
	}
	for _, tc := range []struct {
		name   string
		target string
		setup  func(*testing.T, *fixture)
	}{
		{name: "unset"},
		{name: "missing", target: "missing"},
		{name: "remote", target: "lfs-remote", setup: createRemote},
		{name: "virtual", target: "lfs-inner", setup: createInner},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, repo.Virtual)
			if tc.setup != nil {
				tc.setup(t, f)
			}
			patchLFS(t, f.db, "lfs", func(c *repo.Config) { c.DefaultDeploymentRepo = tc.target })
			for _, user := range []string{"", "writer", "intruder"} {
				for _, got := range []lfsAnswer{
					lfsAnswerOf(t, f.server, http.MethodPost, "lfs/objects/batch", user,
						batchBody("upload", strings.Repeat("c", 64), len(body))),
					lfsAnswerOf(t, f.server, http.MethodPut,
						"lfs/objects/"+strings.Repeat("c", 64)+"?size="+strconv.Itoa(len(body)),
						user, string(body)),
				} {
					wantNoDeployTarget(t, got)
				}
			}
		})
	}

	f := newFixture(t, repo.Virtual)
	createLFS(t, f.db, repo.Config{Key: "lfs-solo", Class: repo.Local})
	patchLFS(t, f.db, "lfs", func(c *repo.Config) { c.DefaultDeploymentRepo = "lfs-solo" })
	_, object := seedLFS(t, f.server, "lfs", body)
	if _, err := f.db.Items().Get(t.Context(), "lfs-solo", false, object); err != nil {
		t.Fatalf("upload did not land in the deployment repository: %v", err)
	}
	upload := policyBatch(t, f.server, "lfs", "writer", "upload", strings.Repeat("d", 64), 1)
	if got := upload.Objects[0]; got.Error == nil || got.Error.Code != http.StatusForbidden {
		t.Fatalf("Write on a member granted a deploy to another repository: %+v", got)
	}

	// A target of another type is refused as before: only a configuration
	// saved before the save checks names one.
	generic := repo.Config{Key: "generic-solo", Class: repo.Local, PackageType: repo.Generic}
	if err := f.db.Repos().Create(t.Context(), *repo.Defaults(&generic)); err != nil {
		t.Fatal(err)
	}
	patchLFS(t, f.db, "lfs", func(c *repo.Config) { c.DefaultDeploymentRepo = "generic-solo" })
	status, answer := lfsCall(t, f.server, http.MethodPut,
		"lfs/objects/"+strings.Repeat("d", 64)+"?size=1", "", "d")
	wantAnswer(t, status, answer, http.StatusBadRequest, "invalid Git LFS deployment target")
}

func wantNoDeployTarget(t *testing.T, got lfsAnswer) {
	t.Helper()
	message := "No local repository was configured as local deployment repository " +
		"for the (lfs) virtual repository."
	if got.status != http.StatusMethodNotAllowed || got.header.Get("Allow") != http.MethodGet ||
		!strings.Contains(got.body, message) {
		t.Fatalf("no deployment target: status=%d allow=%q body=%s",
			got.status, got.header.Get("Allow"), got.body)
	}
}

// TestUnitVirtualUnservedHidesUnderTheGlobalSetting (#1903): the security
// configuration's hideUnauthorizedResources gives an anonymous caller the
// 404 in place of the challenge, as the virtual's own setting does.
func TestUnitVirtualUnservedHidesUnderTheGlobalSetting(t *testing.T) {
	f := newFixture(t, repo.Virtual)
	body := []byte("globally hidden Git LFS bytes\n")
	oid, _ := seedLFS(t, f.server, "lfs-local", body)
	transfer := "lfs/objects/" + oid + "?size=" + strconv.Itoa(len(body))
	if got := lfsAnswerOf(t, f.server, http.MethodGet, transfer, kernel.AnonymousUser, ""); got.status !=
		http.StatusUnauthorized {
		t.Fatalf("before: %d %s", got.status, got.body)
	}
	if err := f.db.Settings().Put(t.Context(), settings.SettingHideUnauthorizedResources, true); err != nil {
		t.Fatal(err)
	}
	status, answer := lfsCall(t, f.server, http.MethodGet, transfer, kernel.AnonymousUser, "")
	wantAnswer(t, status, answer, http.StatusNotFound, "Git LFS object unavailable")
	wantObjectError(t, policyBatch(t, f.server, "lfs", kernel.AnonymousUser, "download", oid, len(body)),
		http.StatusNotFound)
}

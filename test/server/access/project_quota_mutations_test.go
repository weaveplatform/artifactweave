package access_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/gc"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	artport "github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/fs"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/fault"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/internal/platform/tx"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

func quotaMutationRouter(t *testing.T, db harness.Store, blobs blob.Store) http.Handler {
	t.Helper()
	auth, err := authn.NewAuthenticator(db.Users(), db.Settings(), authn.Options{Hash: testHash})
	storekit.Must(t, err)
	return harness.Router(Config{Store: db, Blobs: blobs, Auth: auth, BaseURL: "https://quota.invalid"})
}

func quotaMutationCall(t *testing.T, h http.Handler, method, path, body string, want int) *httptest.ResponseRecorder {
	t.Helper()
	headers := support.Basic("admin", "pw")
	headers["Content-Type"] = "application/json"
	rec := support.Do(t, h, method, path, headers, body)
	wantStatus(t, rec, want)
	return rec
}

func quotaMutationBytes(t *testing.T, db harness.Store, key string, want int64) {
	t.Helper()
	state, initialized, err := db.Projects().Storage(t.Context(), key)
	if err != nil || !initialized || state.Bytes != want {
		t.Fatalf("project %s committed bytes = %d, initialized=%v, want %d: %v", key, state.Bytes, initialized, want, err)
	}
	actual, err := db.Repos().(artifactsapi.StorageUsage).ProjectStorageBytes(t.Context(), key)
	if err != nil || actual != want {
		t.Fatalf("project %s retained bytes = %d, want %d: %v", key, actual, want, err)
	}
}

func TestAccProjectQuotaTransfersAndTrash(t *testing.T) {
	projectQuotaTransfersAndTrash(t, inmem.New())
}

func TestAccProjectQuotaSQLTransfersAndTrash(t *testing.T) {
	projectRepositorySQL(t, projectQuotaTransfersAndTrash)
}

func projectQuotaTransfersAndTrash(t *testing.T, db harness.Store) {
	t.Helper()
	ctx := t.Context()
	support.Seed(t, db)
	blobs, err := fs.New(t.TempDir())
	storekit.Must(t, err)
	h := quotaMutationRouter(t, db, blobs)
	call := func(method, path, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		return quotaMutationCall(t, h, method, path, body, want)
	}
	for _, key := range []string{"alpha", "beta"} {
		call(http.MethodPost, "/access/api/v1/projects", `{"project_key":"`+key+`","display_name":"`+key+`","storage_quota_bytes":100}`, http.StatusCreated)
	}
	for key, owner := range map[string]string{"alpha-first": "alpha", "alpha-second": "alpha", "beta-first": "beta"} {
		call(http.MethodPut, "/artifactory/api/repositories/"+key, `{"rclass":"local","packageType":"generic","projectKey":"`+owner+`"}`, http.StatusOK)
	}
	shared, other := strings.Repeat("s", 40), strings.Repeat("o", 60)
	call(http.MethodPut, "/artifactory/alpha-first/shared.bin", shared, http.StatusCreated)
	call(http.MethodPut, "/artifactory/alpha-first/other.bin", other, http.StatusCreated)
	quotaMutationBytes(t, db, "alpha", 100)
	quotaMutationBytes(t, db, "beta", 0)
	call(http.MethodPut, "/access/api/v1/projects/alpha", `{"storage_quota_bytes":50}`, http.StatusOK)
	call(http.MethodPut, "/access/api/v1/projects/beta", `{"storage_quota_bytes":50}`, http.StatusOK)
	call(http.MethodPost, "/artifactory/api/copy/alpha-first/shared.bin?to=/beta-first/shared.bin&dry=1", "", http.StatusOK)
	quotaMutationBytes(t, db, "beta", 0)
	call(http.MethodPost, "/artifactory/api/move/alpha-first/shared.bin?to=/alpha-second/shared.bin", "", http.StatusOK)
	quotaMutationBytes(t, db, "alpha", 100)
	call(http.MethodGet, "/artifactory/alpha-first/shared.bin", "", http.StatusNotFound)
	call(http.MethodPost, "/artifactory/api/copy/alpha-first/other.bin?to=/alpha-second/refused.bin", "", http.StatusRequestEntityTooLarge)
	call(http.MethodGet, "/artifactory/alpha-second/refused.bin", "", http.StatusNotFound)
	quotaMutationBytes(t, db, "alpha", 100)
	call(http.MethodPost, "/artifactory/api/copy/alpha-second/shared.bin?to=/beta-first/shared.bin", "", http.StatusOK)
	quotaMutationBytes(t, db, "alpha", 100)
	quotaMutationBytes(t, db, "beta", 40)
	call(http.MethodPost, "/artifactory/api/move/alpha-first/other.bin?to=/beta-first/other.bin", "", http.StatusRequestEntityTooLarge)
	if rec := call(http.MethodGet, "/artifactory/alpha-first/other.bin", "", http.StatusOK); rec.Body.String() != other {
		t.Fatal("refused cross-project move changed source content")
	}
	call(http.MethodGet, "/artifactory/beta-first/other.bin", "", http.StatusNotFound)
	quotaMutationBytes(t, db, "alpha", 100)
	quotaMutationBytes(t, db, "beta", 40)
	call(http.MethodPut, "/access/api/v1/projects/beta", `{"storage_quota_bytes":200}`, http.StatusOK)
	call(http.MethodPost, "/artifactory/api/move/alpha-first/other.bin?to=/beta-first/other.bin", "", http.StatusOK)
	quotaMutationBytes(t, db, "alpha", 40)
	quotaMutationBytes(t, db, "beta", 100)
	retained, err := db.Items().Get(ctx, "alpha-second", false, "shared.bin")
	storekit.Must(t, err)
	refs := func(want int64) {
		t.Helper()
		value, err := db.Blobs().Get(ctx, retained.Actual.SHA256)
		if err != nil || value.Refs != want {
			t.Fatalf("shared CAS references = %d, want %d: %v", value.Refs, want, err)
		}
	}
	refs(2)
	call(http.MethodDelete, "/artifactory/beta-first/shared.bin", "", http.StatusNoContent)
	quotaMutationBytes(t, db, "beta", 60)
	refs(2) // the live alpha file and beta's trash entry both retain the blob
	now := time.Now().Add(2 * time.Hour)
	collector, err := gc.New(db, blobs, gc.Options{Grace: time.Second, Now: func() time.Time { return now }})
	storekit.Must(t, err)
	_, err = collector.Run(ctx)
	storekit.Must(t, err)
	refs(2)
	call(http.MethodPut, "/access/api/v1/projects/beta", `{"storage_quota_bytes":80}`, http.StatusOK)
	call(http.MethodPost, "/artifactory/api/trash/restore/beta-first/shared.bin?to=beta-first", "", http.StatusRequestEntityTooLarge)
	quotaMutationBytes(t, db, "beta", 60)
	refs(2)
	call(http.MethodGet, "/artifactory/beta-first/shared.bin", "", http.StatusNotFound)
	call(http.MethodPut, "/access/api/v1/projects/beta", `{"storage_quota_bytes":200}`, http.StatusOK)
	call(http.MethodPost, "/artifactory/api/trash/restore/beta-first/shared.bin?to=beta-first", "", http.StatusAccepted)
	quotaMutationBytes(t, db, "beta", 100)
	refs(2)
	call(http.MethodDelete, "/artifactory/beta-first/shared.bin", "", http.StatusNoContent)
	call(http.MethodPost, "/artifactory/api/trash/empty", "", http.StatusAccepted)
	refs(1)
	_, err = collector.Run(ctx)
	storekit.Must(t, err)
	if rec := call(http.MethodGet, "/artifactory/alpha-second/shared.bin", "", http.StatusOK); rec.Body.String() != shared {
		t.Fatal("quota/delete/GC lost another project's retained shared content")
	}
	call(http.MethodDelete, "/artifactory/alpha-second/shared.bin", "", http.StatusNoContent)
	quotaMutationBytes(t, db, "alpha", 0)
	call(http.MethodPost, "/artifactory/api/trash/empty", "", http.StatusAccepted)
	refs(0)
	now = now.Add(time.Hour)
	_, err = collector.Run(ctx)
	storekit.Must(t, err)
	if _, err := blobs.Stat(ctx, retained.Actual.SHA256); !errors.Is(err, blob.ErrNotFound) {
		t.Fatal("unreferenced shared content was not collected", err)
	}
	if rec := call(http.MethodGet, "/artifactory/beta-first/other.bin", "", http.StatusOK); rec.Body.String() != other {
		t.Fatal("GC deleted live content in the receiving project")
	}
	quotaMutationBytes(t, db, "beta", 60)
}

var errQuotaPublication = fmt.Errorf("injected quota publication failure: %w", kernel.ErrStore)

type quotaPublicationStore struct {
	harness.Store
	fail bool
}

func (s *quotaPublicationStore) Items() artport.ItemStore {
	return quotaPublicationItems{ItemStore: s.Store.Items(), owner: s}
}

type quotaPublicationItems struct {
	artport.ItemStore
	owner *quotaPublicationStore
}

func (i quotaPublicationItems) Put(ctx context.Context, value artmodel.Item) (artmodel.Item, error) {
	stored, err := i.ItemStore.Put(ctx, value)
	if err == nil && i.owner.fail && !value.Folder {
		err = tx.BeforeCommit(ctx, func(context.Context) error { return errQuotaPublication })
	}
	return stored, err
}

func TestAccProjectQuotaPublicationFailures(t *testing.T) {
	projectQuotaPublicationFailures(t, inmem.New())
}

func TestAccProjectQuotaSQLPublicationFailures(t *testing.T) {
	projectRepositorySQL(t, projectQuotaPublicationFailures)
}

func projectQuotaPublicationFailures(t *testing.T, db harness.Store) {
	t.Helper()
	ctx := t.Context()
	support.Seed(t, db)
	blobs, err := fs.New(t.TempDir())
	storekit.Must(t, err)
	h := quotaMutationRouter(t, db, blobs)
	quotaMutationCall(t, h, http.MethodPost, "/access/api/v1/projects", `{"project_key":"alpha","display_name":"Alpha","storage_quota_bytes":100}`, http.StatusCreated)
	quotaMutationCall(t, h, http.MethodPut, "/artifactory/api/repositories/alpha-local", `{"rclass":"local","packageType":"generic","projectKey":"alpha"}`, http.StatusOK)
	const path = "/artifactory/alpha-local/retained.bin"
	original := strings.Repeat("o", 40)
	quotaMutationCall(t, h, http.MethodPut, path, original, http.StatusCreated)
	for _, stage := range []string{"begin", "append", "commit", "transaction", "client disconnect"} {
		t.Run(stage, func(t *testing.T) {
			wrapped := &quotaPublicationStore{Store: db, fail: stage == "transaction"}
			broken := fault.Blobs{Store: blobs}
			switch stage {
			case "begin":
				broken.Begin = errQuotaPublication
			case "append":
				broken.Append = errQuotaPublication
			case "commit":
				broken.Commit = errQuotaPublication
			}
			h := quotaMutationRouter(t, wrapped, broken)
			if stage == "client disconnect" {
				req := httptest.NewRequest(http.MethodPut, path, fault.Disconnect(strings.NewReader("unfinished body"), 2))
				req.SetBasicAuth("admin", "pw")
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				if rec.Code >= 200 && rec.Code < 300 {
					t.Fatal("disconnected upload reported success")
				}
			} else {
				quotaMutationCall(t, h, http.MethodPut, path, "smaller overwrite", http.StatusInternalServerError)
			}
			quotaMutationBytes(t, db, "alpha", 40)
			if rec := quotaMutationCall(t, h, http.MethodGet, path, "", http.StatusOK); rec.Body.String() != original {
				t.Fatal("failed overwrite replaced downloadable content")
			}
			stored, err := db.Items().Get(ctx, "alpha-local", false, "retained.bin")
			storekit.Must(t, err)
			refs, err := db.Blobs().Get(ctx, stored.Actual.SHA256)
			if err != nil || refs.Refs != 1 {
				t.Fatal("failed overwrite changed the retained CAS reference", refs.Refs, err)
			}
		})
	}
	quotaMutationCall(t, h, http.MethodPut, path, "shortened!", http.StatusCreated)
	quotaMutationBytes(t, db, "alpha", 10)
}

func TestAccProjectQuotaCompetingMutations(t *testing.T) {
	projectQuotaCompetingMutations(t, inmem.New())
}

func TestAccProjectQuotaSQLCompetingMutations(t *testing.T) {
	projectRepositorySQL(t, projectQuotaCompetingMutations)
}

func projectQuotaCompetingMutations(t *testing.T, db harness.Store) {
	t.Helper()
	support.Seed(t, db)
	blobs, err := fs.New(t.TempDir())
	storekit.Must(t, err)
	h := quotaMutationRouter(t, db, blobs)
	for _, key := range []string{"alpha", "beta"} {
		quotaMutationCall(t, h, http.MethodPost, "/access/api/v1/projects", `{"project_key":"`+key+`","display_name":"`+key+`","storage_quota_bytes":100}`, http.StatusCreated)
	}
	for _, key := range []string{"alpha-local", "unowned-a", "unowned-b"} {
		body := `{"rclass":"local","packageType":"generic"}`
		if key == "alpha-local" {
			body = `{"rclass":"local","packageType":"generic","projectKey":"alpha"}`
		}
		quotaMutationCall(t, h, http.MethodPut, "/artifactory/api/repositories/"+key, body, http.StatusOK)
	}
	for _, name := range []string{"one.bin", "two.bin"} {
		quotaMutationCall(t, h, http.MethodPut, "/artifactory/alpha-local/"+name, strings.Repeat(name[:1], 35), http.StatusCreated)
	}
	type result struct {
		name     string
		response *httptest.ResponseRecorder
	}
	race := func(names []string, path func(string) string, body func(string) string) []result {
		t.Helper()
		start, results := make(chan struct{}), make(chan result, len(names))
		var workers sync.WaitGroup
		for _, name := range names {
			workers.Go(func() {
				<-start
				results <- result{name: name, response: support.Do(t, h, http.MethodPut, path(name), support.Basic("admin", "pw"), body(name))}
			})
		}
		close(start)
		workers.Wait()
		close(results)
		var out []result
		for value := range results {
			out = append(out, value)
		}
		return out
	}
	overwrites := race(
		[]string{"one.bin", "two.bin"},
		func(name string) string { return "/artifactory/alpha-local/" + name },
		func(name string) string { return strings.Repeat(name[:1], 55) },
	)
	accepted, refused := 0, 0
	for _, value := range overwrites {
		want := strings.Repeat(value.name[:1], 35)
		switch value.response.Code {
		case http.StatusCreated:
			accepted++
			want = strings.Repeat(value.name[:1], 55)
		case http.StatusRequestEntityTooLarge:
			refused++
		default:
			t.Fatalf("competing overwrite failed outside quota admission: %d %s", value.response.Code, value.response.Body)
		}
		if rec := quotaMutationCall(t, h, http.MethodGet, "/artifactory/alpha-local/"+value.name, "", http.StatusOK); rec.Body.String() != want {
			t.Fatal("refused concurrent overwrite changed content", value.name)
		}
	}
	if accepted != 1 || refused != 1 {
		t.Fatal("competing overwrites exceeded remaining quota", accepted, refused)
	}
	quotaMutationBytes(t, db, "alpha", 90)
	for _, key := range []string{"unowned-a", "unowned-b"} {
		quotaMutationCall(t, h, http.MethodPut, "/artifactory/"+key+"/item.bin", strings.Repeat(key[len(key)-1:], 60), http.StatusCreated)
	}
	attachments := race(
		[]string{"unowned-a", "unowned-b"},
		func(name string) string { return "/access/api/v1/projects/_/attach/repositories/" + name + "/beta" },
		func(string) string { return "" },
	)
	accepted, refused = 0, 0
	for _, value := range attachments {
		wantOwner := ""
		switch value.response.Code {
		case http.StatusNoContent:
			accepted++
			wantOwner = "beta"
		case http.StatusForbidden:
			refused++
		default:
			t.Fatalf("competing attachment failed outside quota admission: %d %s", value.response.Code, value.response.Body)
		}
		configuration, err := db.Repos().Get(t.Context(), value.name)
		if err != nil || configuration.ProjectKey != wantOwner {
			t.Fatal("refused concurrent attachment changed canonical ownership", value.name, configuration.ProjectKey, err)
		}
		if rec := quotaMutationCall(
			t,
			h,
			http.MethodGet,
			"/artifactory/"+value.name+"/item.bin",
			"",
			http.StatusOK,
		); rec.Body.String() != strings.Repeat(
			value.name[len(value.name)-1:],
			60,
		) {
			t.Fatal("competing attachment changed repository content")
		}
	}
	if accepted != 1 || refused != 1 {
		t.Fatal("competing attachments exceeded remaining quota", accepted, refused)
	}
	quotaMutationBytes(t, db, "beta", 60)
}

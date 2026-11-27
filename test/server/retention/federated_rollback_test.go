package retention_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	fedmodule "github.com/weaveplatform/artifactweave/internal/federation"
	fedapi "github.com/weaveplatform/artifactweave/internal/federation/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/federated"
	fedmodel "github.com/weaveplatform/artifactweave/internal/federation/core/model"
	streamsync "github.com/weaveplatform/artifactweave/internal/federation/core/sync"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

// The delivery worker is deliberately not started. Unexpected remote I/O is a
// fixture failure; the real module's stream and item hooks still stage effects.
type cleanupDormantRemote struct{ streamsync.Remote }

func TestAccFederatedCleanupRollbackSQL(t *testing.T) {
	keys := storekit.Get[*crypt.Keyring](t)(crypt.New(bytes.Repeat([]byte{7}, 32)))
	sqltest.RunModule(
		t,
		appsql.Modules(),
		func(s *dialect.Store) *appsql.Store { return appsql.New(s.DB(), s.Dialect(), keys) },
		func(t *testing.T, newStore func(*testing.T) *appsql.Store) {
			db := newStore(t)
			f := newCleanupExecutorFixture(t, db)
			f.now = time.Now()
			_, _, err := f.artifacts.Repositories.Remove(f.caller, "execution", "admin")
			storekit.Must(t, err)
			storekit.Must(t, f.artifacts.Repositories.Create(f.caller, *repo.Defaults(&repo.Config{Key: "execution", Class: repo.Local, PackageType: repo.Maven})))
			f.ids = nil
			for _, version := range []string{"1.0", "2.0"} {
				base := "com/example/lib/" + version + "/lib-" + version
				pom := fmt.Sprintf(`<project><modelVersion>4.0.0</modelVersion><groupId>com.example</groupId><artifactId>lib</artifactId><version>%s</version></project>`, version)
				for _, file := range []struct{ path, body string }{{base + ".pom", pom}, {base + ".jar", "jar-" + version}} {
					response := support.Do(t, f.handler, http.MethodPut, "/artifactory/execution/"+file.path, support.Basic("admin", "pw"), file.body)
					if response.Code != http.StatusCreated {
						t.Fatal("native Maven rollback fixture upload", response.Code, response.Body.String())
					}
					if version == "1.0" {
						f.ids = append(f.ids, storekit.Get[artmodel.Item](t)(db.Items().Get(f.ctx, "execution", false, file.path)).ID)
					}
				}
			}
			policy := storekit.Get[model.CleanupPolicy](t)(f.policies.Get(f.caller, "execution-policy", false))
			policy.Criteria.PackageTypes = []string{"maven"}
			_, err = f.policies.Update(f.caller, policy.Key, policy)
			storekit.Must(t, err)
			enableFederatedExecutor(t, f)
			module := storekit.Get[*fedmodule.Module](
				t,
			)(
				fedmodule.New(
					fedmodule.Deps{
						Store:        db,
						Access:       accesstest.Over(t, db).API(),
						Repositories: f.artifacts.Repositories,
						Lifetimes:    f.artifacts.Lifetimes,
						BaseURL:      "https://self.example",
					},
				),
			)
			t.Cleanup(func() { module.Close(); db.SetItemFeed(nil) })
			_, err = module.OpenStreams(fedmodule.StreamOptions{Blobs: f.blobs, Remote: cleanupDormantRemote{}, Queue: f.queue, Items: f.artifacts.FederatedItems, Feed: db})
			storekit.Must(t, err)
			metadataPath := "/artifactory/execution/com/example/lib/maven-metadata.xml"
			nativeMetadata := func() string {
				t.Helper()
				response := support.Do(t, f.handler, http.MethodGet, metadataPath, support.Basic("admin", "pw"), "")
				if response.Code != http.StatusOK {
					t.Fatal("native metadata", response.Code)
				}
				return response.Body.String()
			}
			// A public metadata GET is itself a hidden admission. Capture that
			// baseline before selection, so the fixture does not invalidate its
			// own selected download evidence before reaching the injected fault.
			scope := storekit.Get[fedapi.CleanupScope](t)(f.policies.Federation.Scope(f.ctx, "execution"))
			versions := []fedapi.CleanupVersion{{Package: "com.example:lib", Version: "1.0"}}
			beforeRead := storekit.Get[fedapi.CleanupEvidence](t)(f.policies.Federation.Collect(f.ctx, scope, versions))
			beforeMetadata := nativeMetadata()
			afterRead := storekit.Get[fedapi.CleanupEvidence](t)(f.policies.Federation.Collect(f.ctx, scope, versions))
			if beforeRead.SameDownloads(afterRead) {
				t.Fatal("metadata baseline did not exercise the private admission revision")
			}
			t.Log("public metadata baseline changed private evidence before selection")
			storedMetadata := func() string {
				t.Helper()
				it := storekit.Get[artmodel.Item](t)(db.Items().Get(f.ctx, "execution", false, "com/example/lib/maven-metadata.xml"))
				body, _, err := f.blobs.Open(f.ctx, it.Actual.SHA256)
				storekit.Must(t, err)
				defer func() { storekit.Must(t, body.Close()) }()
				content := storekit.Get[[]byte](t)(io.ReadAll(body))
				return string(content)
			}
			storekit.Same(t, "public and stored metadata baseline", beforeMetadata, storedMetadata())
			run, item := f.selected(t)
			progress := storekit.Get[model.CleanupProgress](t)(db.Cleanup().Progress(f.ctx, run.ID))
			beforeFiles := storekit.Get[[]int64](t)(db.Packages().Files(f.ctx, progress.Version.ID))
			self := storekit.Get[string](t)(db.Instance().ID(f.ctx))
			initialSeq := storekit.Get[int64](t)(db.Outbox().LastSeq(f.ctx, self, "repo:execution"))
			initialStamps := storekit.Get[[]fedmodel.ItemStamp](t)(db.Stamps().List(f.ctx, "execution", "", 100))
			armed := true
			faulty := f.executor
			faulty.Store = cleanupFaultStore{f.store, &armed}
			_, err = faulty.Step(f.ctx, run.ID, func(ctx context.Context) error {
				_, err := db.WorkQueue().GuardLease(ctx, item.Kind, item.Key, item.Generation)
				return err
			})
			storekit.WantErr(t, err, errCleanupEffectAck)
			if armed {
				t.Fatal("late acknowledgement fault was not reached")
			}
			for _, id := range f.ids {
				_, err = db.Items().GetByID(f.ctx, id)
				storekit.Must(t, err)
			}
			for _, path := range []string{"com/example/lib/1.0/lib-1.0.pom", "com/example/lib/1.0/lib-1.0.jar"} {
				trash := storekit.Get[[]artmodel.TrashEntry](t)(db.Trash().Find(f.ctx, "execution", path))
				if len(trash) != 0 {
					t.Fatal("rollback leaked native trash", path)
				}
			}
			storekit.Same(t, "rollback package links", beforeFiles, storekit.Get[[]int64](t)(db.Packages().Files(f.ctx, progress.Version.ID)))
			// Inspect persisted companion bytes without opening a new response under
			// the prepared gate. The member route must remain closed until recovery.
			storekit.Same(t, "rollback native companion metadata", beforeMetadata, storedMetadata())
			blocked := support.Do(t, f.handler, http.MethodGet, metadataPath, support.Basic("admin", "pw"), "")
			if blocked.Code != http.StatusServiceUnavailable {
				t.Fatal("prepared rollback admitted a member metadata download", blocked.Code)
			}
			storekit.Same(t, "rollback sequence", initialSeq, storekit.Get[int64](t)(db.Outbox().LastSeq(f.ctx, self, "repo:execution")))
			storekit.Same(t, "rollback tombstones", initialStamps, storekit.Get[[]fedmodel.ItemStamp](t)(db.Stamps().List(f.ctx, "execution", "", 100)))
			federatedRollbackContinuation(t, progress, storekit.Get[model.CleanupProgress](t)(db.Cleanup().Progress(f.ctx, run.ID)))
			storekit.Must(t, f.executor.Execute(f.ctx, item))
			after := storekit.Get[model.CleanupRun](t)(db.Cleanup().Run(f.ctx, run.ID))
			if after.Status != "COMPLETED" || after.CleanedItems != 2 {
				t.Fatal("retry did not remove exactly the native old version", after)
			}
			entries := storekit.Get[[]outbox.OutboxEntry](t)(db.Outbox().Read(f.ctx, self, "repo:execution", initialSeq, 100))
			deletes := map[string]int{}
			for n, entry := range entries {
				if entry.Seq != initialSeq+int64(n)+1 {
					t.Fatal("retry left a sequence gap", entry.Seq)
				}
				if entry.EventType == federated.EventDelete {
					var payload struct {
						Path string `json:"path"`
					}
					storekit.Must(t, json.Unmarshal(entry.Payload, &payload))
					deletes[payload.Path]++
				}
			}
			if len(deletes) != 2 || deletes["com/example/lib/1.0/lib-1.0.pom"] != 1 || deletes["com/example/lib/1.0/lib-1.0.jar"] != 1 {
				t.Fatal("retry emitted duplicate or missing native tombstones", deletes)
			}
			metadata := nativeMetadata()
			if strings.Contains(metadata, "<version>1.0</version>") || !strings.Contains(metadata, "<version>2.0</version>") {
				t.Fatal("native companion metadata retained removed version", metadata)
			}
		},
		sqltest.WithPostgres(),
		sqltest.WithKeyring(keys),
	)
}

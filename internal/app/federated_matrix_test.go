package app

import (
	"context"
	"io"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	streamsync "github.com/weaveplatform/artifactweave/internal/federation/core/sync"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// federatedMatrix covers the federated repository operations (FED-01,
// FED-04): administrators only, as 7.161.26's FederatedRepoResource and
// FederatedReplaceUrlResource allow.
func federatedMatrix() []matrixRow {
	const base = "/artifactory/api/federation"
	const urls = `{"oldBaseUrl":"https://old.example","newBaseUrl":"https://new.example"}`
	create := func(prefix string, class repo.Class) seeder {
		return func(t *testing.T, f *matrixFixture, p string) {
			t.Helper()
			c := repo.Config{Key: prefix + p, Class: class, PackageType: repo.Generic}
			if err := f.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
				t.Fatal(err)
			}
		}
	}
	return []matrixRow{
		row("GET "+base+"/binaries/failures/list", base+"/binaries/failures/list?repoKey=fbin-{p}", "", administrators, 200).seeding(create("fbin-", repo.Federated)),
		row("PUT "+base+"/binaries/failures/replay", base+"/binaries/failures/replay?repoKey=fbinr-{p}", "", administrators, 200).seeding(create("fbinr-", repo.Federated)),
		row("DELETE "+base+"/binaries/failures/delete", base+"/binaries/failures/delete?repoKey=fbind-{p}", "", administrators, 200).seeding(create("fbind-", repo.Federated)),
		row("GET "+base+"/binaries/tasks/{repoKey}", base+"/binaries/tasks/fbinc-{p}", "", administrators, 200).seeding(create("fbinc-", repo.Federated)),
		row(
			"GET "+base+"/status/repoState/{repoKey}/binaries/",
			base+"/status/repoState/libs-local/binaries/",
			"",
			holds("libs-local", "", accessapi.ActionRead),
			200,
		).seeding(func(t *testing.T, f *matrixFixture, _ string) {
			t.Helper()
			c, err := f.db.Repos().Get(t.Context(), "libs-local")
			if err != nil {
				t.Fatal(err)
			}
			c.Class = repo.Federated
			if err := f.db.Repos().Update(t.Context(), c); err != nil {
				t.Fatal(err)
			}
		}).
			alone(),

		row("GET "+base+"/status/connectionStates/{repoKey}", base+"/status/connectionStates/fconn-{p}", "", administrators, 200).seeding(create("fconn-", repo.Federated)),
		row("POST "+base+"/configSync/{repoKey}", base+"/configSync/fsync-{p}", "", administrators, 200).
			seeding(create("fsync-", repo.Federated)),
		row("POST "+base+"/migrate/{repoKey}", base+"/migrate/fmig-{p}", "", administrators, 200).
			seeding(create("fmig-", repo.Local)),
		at("POST "+base+"/convertToLocal", `["fcvt-{p}"]`, administrators, 200).
			seeding(create("fcvt-", repo.Federated)),
		at("POST "+base+"/replaceUrl", urls, administrators, 200),
		at("POST "+base+"/updateMemberUrl", urls, administrators, 200),
		row("POST "+base+"/fullSync/{repoKey}", base+"/fullSync/ffull-{p}", "", administrators, 200).
			seeding(create("ffull-", repo.Federated)),
		row("POST "+base+"/forceFullSync/{repoKey}", base+"/forceFullSync/fforce-{p}", "", administrators, 200).
			seeding(create("fforce-", repo.Federated)),
		at("POST "+base+"/recovery", "", administrators, 200),
		at("POST "+base+"/reconnect", "", administrators, 200),
		// No member matches, so an administrator reaches the operation's
		// 400; everyone else is refused before the body is read.
		at("POST "+base+"/removeMember", "https://nobody.example", administrators, 400),
		row("PUT "+base+"/prioritization/{repoKey}", base+"/prioritization/fprio-{p}?priorityLevel=high", "",
			administrators, 200).
			seeding(create("fprio-", repo.Federated)),
		row("PUT "+base+"/propagateCreatedTime", base+"/propagateCreatedTime?enabled=true", "", administrators, 200),
	}
}

// withStreams opens the federation module's stream delivery engine over
// the fixture's store, as OpenRuntime does, so the operations that need
// it (FED-04) answer as on a running server. Its queue runs no workers:
// the tables check answers, not delivery.
func withStreams(t *testing.T, cfg *RouterConfig, blobs blob.Store) {
	t.Helper()
	*cfg = withFederation(withAccess(*cfg))
	st, ok := cfg.Store.(work.Store)
	if !ok {
		t.Fatalf("%T has no work queue", cfg.Store)
	}
	q, err := work.New(st, work.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.Federation.OpenStreams(federation.StreamOptions{
		Blobs: blobs, Remote: unreachablePeers{}, Queue: q,
	}); err != nil {
		t.Fatal(err)
	}
}

// unreachablePeers is a stream transport no peer answers; the tables pair
// with no one, so nothing calls it.
type unreachablePeers struct{}

func (unreachablePeers) Push(context.Context, model.Peer, streamsync.Batch) (int64, error) {
	return 0, io.ErrUnexpectedEOF
}

func (unreachablePeers) Pull(context.Context, model.Peer, string, int64, int) (streamsync.Page, error) {
	return streamsync.Page{}, io.ErrUnexpectedEOF
}

func (unreachablePeers) Snapshot(context.Context, model.Peer, string, string, int) (streamsync.SnapshotPage, error) {
	return streamsync.SnapshotPage{}, io.ErrUnexpectedEOF
}

func (unreachablePeers) FetchBlob(context.Context, model.Peer, string, func(io.Reader) error) error {
	return io.ErrUnexpectedEOF
}

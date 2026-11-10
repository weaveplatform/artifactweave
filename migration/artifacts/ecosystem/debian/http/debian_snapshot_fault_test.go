package debianhttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/migration/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	artgen "github.com/weaveplatform/artifactweave/migration/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/migration/platform/testkit/fault"
	"github.com/weaveplatform/artifactweave/migration/platform/work"
)

// snapshotJob is the snapshot the tests here take: tag first of the stable
// distribution, from deb-source into deb-target.
var snapshotJob = debianSnapshotRequest{
	Source: "deb-source", Target: "deb-target", Tag: "first", Distribution: "stable",
}

// snapshotMarker is the folder that claims snapshotJob's tag in its target.
const snapshotMarker = "snapshots/first"

// itemFaults is a store whose item calls fail with errFault where fails
// says: call names the method, and key and path the item it touches.
type itemFaults struct {
	*inmem.Store
	fails func(call, key, path string) bool
}

func (s itemFaults) Items() port.ItemStore {
	return faultingItems{ItemStore: s.Store.Items(), fails: s.fails}
}

type faultingItems struct {
	port.ItemStore
	fails func(call, key, path string) bool
}

func (i faultingItems) Get(
	ctx context.Context, key string, cached bool, path string,
) (model.Item, error) {
	if i.fails("Get", key, path) {
		return model.Item{}, errFault
	}
	return i.ItemStore.Get(ctx, key, cached, path)
}

func (i faultingItems) Put(ctx context.Context, it model.Item) (model.Item, error) {
	if i.fails("Put", it.RepoKey, it.FullPath()) {
		return model.Item{}, errFault
	}
	return i.ItemStore.Put(ctx, it)
}

// failsByID reports whether call fails on the item with id, which the
// property calls name by id alone.
func (i faultingItems) failsByID(ctx context.Context, call string, id int64) bool {
	it, err := i.ItemStore.GetByID(ctx, id)
	return err == nil && i.fails(call, it.RepoKey, it.FullPath())
}

func (i faultingItems) Properties(ctx context.Context, id int64) (model.Properties, error) {
	if i.failsByID(ctx, "Properties", id) {
		return nil, errFault
	}
	return i.ItemStore.Properties(ctx, id)
}

func (i faultingItems) SetProperties(
	ctx context.Context, id int64, props model.Properties, at time.Time,
) error {
	if i.failsByID(ctx, "SetProperties", id) {
		return errFault
	}
	return i.ItemStore.SetProperties(ctx, id, props, at)
}

// onMarker fails call on snapshotJob's marker alone, so the metadata the
// snapshot copies is still written around it.
func onMarker(call string) func(string, string, string) bool {
	return func(c, key, path string) bool {
		return c == call && key == snapshotJob.Target && path == snapshotMarker
	}
}

// snapshotWorld is an item world with snapshotJob's local source and
// target, the source publishing a stable distribution that lists alpha.
func snapshotWorld(t *testing.T) *itemWorld {
	t.Helper()
	w := newItemWorld(t)
	w.debRepos(
		repo.Config{Key: "deb-source", Class: repo.Local},
		repo.Config{Key: "deb-target", Class: repo.Local},
	)
	w.publish("deb-source", packagesIndex("alpha 1.0 "+debAlpha))
	return w
}

// snapshotBody is snapshotJob as the operation's request body.
func snapshotBody() *artgen.CreateDebianSnapshotJSONRequestBody {
	return &artgen.CreateDebianSnapshotJSONRequestBody{
		Tag: snapshotJob.Tag, TargetRepo: snapshotJob.Target,
		Distribution: snapshotJob.Distribution,
	}
}

// refusingAuthorizer is an authorizer whose every evaluation fails, as
// one whose permission store cannot be read does.
func refusingAuthorizer() *accessapitest.Authorizer {
	return &accessapitest.Authorizer{DecideFunc: func(
		context.Context, accessapi.Subject, bool,
	) (accessapi.Decider, error) {
		return nil, errFault
	}}
}

// claim claims snapshotJob's tag in st as kittest's administrator.
func claim(t *testing.T, st *inmem.Store) {
	t.Helper()
	if err := claimDebianSnapshot(t.Context(), st, snapshotJob, kittest.Admin); err != nil {
		t.Fatal(err)
	}
}

// TestUnitDebianSnapshotRequestNeedsAStoreAndACaller: the snapshot
// operation answers 501 on a server without a store, 401 without a
// caller, and 500 when the caller's permissions cannot be read, before it
// claims anything.
func TestUnitDebianSnapshotRequestNeedsAStoreAndACaller(t *testing.T) {
	w := snapshotWorld(t)
	req := artgen.CreateDebianSnapshotRequestObject{
		RepoKey: snapshotJob.Source, Body: snapshotBody(),
	}
	for _, tc := range []struct {
		name    string
		svc     DebianAPI
		ctx     context.Context
		status  int
		message string
	}{
		{
			"no store",
			DebianAPI{itemkit.New(itemkit.Deps{})},
			adminContext(t.Context()),
			http.StatusNotImplemented, "not implemented by artifactweave yet",
		},
		{
			"no caller",
			DebianAPI{newKit(withAccess(t, Config{Store: w.db, Blobs: w.blobs}))},
			t.Context(), http.StatusUnauthorized, "Authentication is required",
		},
		{
			// With no decider beside the caller, the operation asks the
			// authorizer for one.
			"caller's permissions cannot be read",
			DebianAPI{newKit(Config{
				Store: w.db, Blobs: w.blobs,
				Access: accessapi.API{Authorizer: refusingAuthorizer()},
			})},
			accessapi.WithCaller(t.Context(), kernel.Principal{Name: "dev"}, nil),
			http.StatusInternalServerError, "internal error",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.svc.CreateDebianSnapshot(tc.ctx, req)
			wantAPIError(t, err, tc.status, tc.message)
		})
	}
	_, err := w.db.Items().Get(t.Context(), snapshotJob.Target, false, snapshotMarker)
	if !errors.Is(err, kernel.ErrNotFound) {
		t.Errorf("a refused snapshot claimed its tag: %v", err)
	}
}

// TestUnitDebianSnapshotThroughTheQueue: with a work queue, a snapshot
// whose work key would exceed the queue's bound is a bad request, and one
// the queue cannot take, or whose queued run is refused, is a server fault
// rather than a snapshot reported as created.
func TestUnitDebianSnapshotThroughTheQueue(t *testing.T) {
	long := strings.Repeat("t", work.MaxWorkKey)
	for _, tc := range []struct {
		name    string
		target  string
		queue   func(*inmem.Store) work.Store
		status  int
		message string
	}{
		{
			name: "work key too long", target: long,
			status: http.StatusBadRequest, message: "Debian snapshot key is too long",
		},
		{
			name: "queue refuses the work", target: snapshotJob.Target,
			queue:  func(s *inmem.Store) work.Store { return queueFault{s} },
			status: http.StatusInternalServerError, message: "internal error",
		},
		{
			// No authenticator can recheck the queued caller, so the run is
			// refused for good and the synchronous request fails with it.
			name: "queued run refused", target: snapshotJob.Target,
			status: http.StatusInternalServerError, message: "internal error",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := snapshotWorld(t)
			if tc.target != snapshotJob.Target {
				w.debRepos(repo.Config{Key: tc.target, Class: repo.Local})
			}
			var queued work.Store = w.db
			if tc.queue != nil {
				queued = tc.queue(w.db)
			}
			q, err := work.New(queued, work.Options{})
			if err != nil {
				t.Fatal(err)
			}
			k := itemkit.New(itemkit.Deps{
				Store: w.db, Blobs: w.blobs, Work: q,
				Access: withAccess(t, Config{Store: w.db}).Access,
			})
			if err := RegisterDebianSnapshotWork(q, k); err != nil {
				t.Fatal(err)
			}
			body := snapshotBody()
			body.TargetRepo = tc.target
			_, err = DebianAPI{k}.CreateDebianSnapshot(adminContext(t.Context()),
				artgen.CreateDebianSnapshotRequestObject{RepoKey: snapshotJob.Source, Body: body})
			wantAPIError(t, err, tc.status, tc.message)
		})
	}
}

// TestUnitDebianSnapshotWorkRegistersOnce: a second registration of the
// snapshot handler on one queue is refused, so two handlers can never race
// for the same queued snapshot.
func TestUnitDebianSnapshotWorkRegistersOnce(t *testing.T) {
	st := inmem.New()
	q, err := work.New(st, work.Options{})
	if err != nil {
		t.Fatal(err)
	}
	k := newKit(withAccess(t, Config{Store: st}))
	if err := RegisterDebianSnapshotWork(q, k); err != nil {
		t.Fatal(err)
	}
	if err := RegisterDebianSnapshotWork(q, k); !errors.Is(err, work.ErrDuplicateHandler) {
		t.Errorf("second registration returned %v, want a duplicate handler", err)
	}
}

// TestUnitDebianSnapshotWorkRefusesJobsItCannotRun: a queued snapshot that
// is not a well-formed job, or whose caller is no longer current, is
// refused for good; one whose caller's permissions cannot be read fails
// for now, so that the queue retries it.
func TestUnitDebianSnapshotWorkRefusesJobsItCannotRun(t *testing.T) {
	job := snapshotJob
	job.Credential = kernel.Principal{Name: "dev", Password: true}.CredentialReference()
	valid, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	expired := &accessapitest.Authenticator{CurrentFunc: func(
		context.Context, kernel.Principal,
	) (kernel.Principal, error) {
		return kernel.Principal{}, errFault
	}}
	for _, tc := range []struct {
		name      string
		payload   []byte
		access    accessapi.API
		permanent bool
	}{
		{
			name: "not a job", payload: []byte(`{"source":"deb-source"}`),
			access: accessapi.API{Authenticator: &accessapitest.Authenticator{}}, permanent: true,
		},
		{
			name: "caller no longer current", payload: valid,
			access: accessapi.API{Authenticator: expired}, permanent: true,
		},
		{
			name: "caller's permissions cannot be read", payload: valid,
			access: accessapi.API{
				Authenticator: &accessapitest.Authenticator{}, Authorizer: refusingAuthorizer(),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := inmem.New()
			q, err := work.New(st, work.Options{})
			if err != nil {
				t.Fatal(err)
			}
			k := newKit(Config{Store: st, Access: tc.access})
			if err := RegisterDebianSnapshotWork(q, k); err != nil {
				t.Fatal(err)
			}
			const key = "deb-target/first"
			if err := q.Enqueue(t.Context(), debianSnapshotKind, key, tc.payload); err != nil {
				t.Fatal(err)
			}
			err = q.EnsureFresh(t.Context(), debianSnapshotKind, key)
			if err == nil || errors.Is(err, work.ErrPermanent) != tc.permanent ||
				!tc.permanent && !errors.Is(err, errFault) {
				t.Errorf("run returned %v, want a failure that is permanent: %t", err, tc.permanent)
			}
		})
	}
}

// TestUnitDebianSnapshotClaimFailsOnStoreFaults: a claim the store cannot
// record fails, whichever of the marker's lookup, properties, folder or
// claim properties the store fails on, rather than leaving a tag claimed
// for no one.
func TestUnitDebianSnapshotClaimFailsOnStoreFaults(t *testing.T) {
	for _, tc := range []struct {
		call    string
		claimed bool
		want    string
	}{
		{"Get", false, "snapshot claim lookup"},
		{"Properties", true, "snapshot claim properties"},
		{"Put", false, "snapshot claim folder"},
		{"SetProperties", false, "snapshot claim source"},
	} {
		t.Run(tc.call, func(t *testing.T) {
			st := inmem.New()
			if tc.claimed {
				claim(t, st)
			}
			err := claimDebianSnapshot(t.Context(), itemFaults{Store: st, fails: onMarker(tc.call)},
				snapshotJob, kittest.Admin)
			if !errors.Is(err, errFault) || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("claim returned %v, want %q", err, tc.want)
			}
		})
	}
}

// TestUnitDebianSnapshotRepositoriesFailOnStoreFaults: a snapshot whose
// source or target repository cannot be looked up fails with the store's
// fault rather than as a missing repository.
func TestUnitDebianSnapshotRepositoriesFailOnStoreFaults(t *testing.T) {
	w := snapshotWorld(t)
	for _, key := range []string{snapshotJob.Source, snapshotJob.Target} {
		_, _, err := snapshotRepos(t.Context(), faulty{serverStore: w.db, repo: key}, snapshotJob,
			kittest.Admin, kittest.AdminAccess())
		if !errors.Is(err, errFault) {
			t.Errorf("%s lookup returned %v, want the store's fault", key, err)
		}
	}
}

// TestUnitDebianSnapshotCopiesTheSourceMetadata: a snapshot copies the
// source's Release and the Packages index it declares, with its by-hash
// copy, under the tag, and records the source on the tag's marker; with
// no InRelease or Release.gpg in the source, there are none to copy.
func TestUnitDebianSnapshotCopiesTheSourceMetadata(t *testing.T) {
	w := snapshotWorld(t)
	svc := DebianAPI{newKit(withAccess(t, Config{Store: w.db, Blobs: w.blobs}))}
	if err := svc.createDebianSnapshot(t.Context(), w.db, snapshotJob,
		kittest.Admin, kittest.AdminAccess()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{debRelease, debPackages} {
		w.item(snapshotJob.Target, false, snapshotMarker+"/"+name)
	}
	for _, name := range []string{"dists/stable/InRelease", "dists/stable/Release.gpg"} {
		if _, err := w.db.Items().Get(t.Context(), snapshotJob.Target, false,
			snapshotMarker+"/"+name); !errors.Is(err, kernel.ErrNotFound) {
			t.Errorf("snapshot holds %s the source never published: %v", name, err)
		}
	}
	props, err := w.db.Items().Properties(t.Context(),
		w.item(snapshotJob.Target, false, snapshotMarker).ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := props["deb.snapshot.source"]; len(got) != 1 || got[0] != snapshotJob.Source {
		t.Errorf("snapshot marker records source %v, want %s", got, snapshotJob.Source)
	}
}

// TestUnitDebianSnapshotCreationFailsClosed: a snapshot is recorded only
// when every step succeeds. A marker or metadata the store cannot read or
// write, a target the caller may not write below the tag, a source Release
// whose declarations do not parse, a listed package the source excludes,
// and an InRelease that cannot be read each fail the snapshot.
func TestUnitDebianSnapshotCreationFailsClosed(t *testing.T) {
	// The caller may do anything but act below the tag in the target, which
	// the snapshot checks file by file after it has checked the tag itself.
	noWritesBelowTag := itemkit.Access{Decider: &accessapitest.Decider{
		Admin: true,
		AllowsFunc: func(
			_ accessapi.ResourceType, r accessapi.Repo, path string, _ accessapi.Actions,
		) bool {
			return r.Key != snapshotJob.Target || !strings.HasPrefix(path, snapshotMarker+"/")
		},
	}}
	for _, tc := range []struct {
		name    string
		claimed bool
		fails   func(call, key, path string) bool
		blobs   func(*itemWorld)
		acc     *itemkit.Access
		seed    func(*itemWorld)
		want    error
		// step is part of the failure's message, for a failure that wraps no
		// sentinel the test can match.
		step string
	}{
		{name: "marker lookup fails", fails: onMarker("Get"), want: errFault},
		{
			name: "claimed marker's properties unreadable", claimed: true,
			fails: onMarker("Properties"), want: errFault,
		},
		{name: "marker cannot be stored", claimed: true, fails: onMarker("Put"), want: errFault},
		{
			name: "marker cannot record its source", claimed: true,
			fails: onMarker("SetProperties"), want: errFault,
		},
		{
			name: "metadata cannot be stored", want: errFault,
			blobs: func(w *itemWorld) { w.blobs = fault.Blobs{Store: w.blobs, Begin: errFault} },
		},
		{
			name: "target refuses writes below the tag", acc: &noWritesBelowTag,
			want: kernel.ErrForbidden,
		},
		{
			name: "source Release declarations do not parse",
			seed: func(w *itemWorld) {
				w.put("deb-source", debRelease, false, []byte("Suite: stable\n"))
			},
			step: "snapshot Release declarations",
		},
		{
			name: "listed package excluded by the source",
			seed: func(w *itemWorld) {
				c := w.debRepo("deb-source")
				c.ExcludesPattern = new("pool/**")
				if err := w.db.Repos().Update(w.t.Context(), c); err != nil {
					w.t.Fatal(err)
				}
			},
			want: kernel.ErrForbidden,
		},
		{
			name: "InRelease unreadable",
			seed: func(w *itemWorld) { w.lostAt("deb-source", "dists/stable/InRelease") },
			step: "snapshot InRelease",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := snapshotWorld(t)
			if tc.seed != nil {
				tc.seed(w)
			}
			if tc.claimed {
				claim(t, w.db)
			}
			if tc.blobs != nil {
				tc.blobs(w)
			}
			var st serverStore = w.db
			if tc.fails != nil {
				st = itemFaults{Store: w.db, fails: tc.fails}
			}
			acc := kittest.AdminAccess()
			if tc.acc != nil {
				acc = *tc.acc
			}
			svc := DebianAPI{newKit(withAccess(t, Config{Store: st, Blobs: w.blobs}))}
			err := svc.createDebianSnapshot(t.Context(), st, snapshotJob, kittest.Admin, acc)
			if err == nil || tc.want != nil && !errors.Is(err, tc.want) ||
				!strings.Contains(err.Error(), tc.step) {
				t.Fatalf("snapshot returned %v, want a failure (%v) at %q", err, tc.want, tc.step)
			}
			// An unclaimed snapshot that fails leaves no marker; a claimed one
			// leaves the claim for a retry to resume, but never the source
			// property that serves the tag.
			marker, err := w.db.Items().Get(t.Context(), snapshotJob.Target, false, snapshotMarker)
			if errors.Is(err, kernel.ErrNotFound) {
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			props, err := w.db.Items().Properties(t.Context(), marker.ID)
			if err != nil || len(props["deb.snapshot.source"]) != 0 {
				t.Errorf("failed snapshot recorded its source: %v (%v)", props, err)
			}
		})
	}
}

// TestUnitDebianSnapshotRechecksARemoteSourcesPackages: a snapshot of a
// remote repository copies its metadata only once the source has rechecked
// each package the index lists, so a package the upstream now refuses
// fails the snapshot rather than leaving a tag that names it.
func TestUnitDebianSnapshotRechecksARemoteSourcesPackages(t *testing.T) {
	index := packagesIndex("alpha 1.0 " + debAlpha)
	// The source service makes the first check itself when it answers, so
	// the second is the snapshot's check of alpha.
	refused := func() func(context.Context) error { return failsOnCall(2, source.ErrDenied) }
	w := newOverlayWorld(t, withSources(upstream(map[string]func() (source.Result, error){
		debRelease:  streamed(releaseFor(index), nil),
		debPackages: streamed(index, refused),
	})))
	w.debRepos(
		repo.Config{Key: snapshotJob.Source, Class: repo.Remote},
		repo.Config{Key: snapshotJob.Target, Class: repo.Local},
	)
	err := DebianAPI{w.kit}.createDebianSnapshot(t.Context(), w.db, snapshotJob,
		kittest.Admin, kittest.AdminAccess())
	if !errors.Is(err, source.ErrDenied) {
		t.Errorf("snapshot returned %v, want the source's refusal", err)
	}
	if _, err := w.db.Items().Get(t.Context(), snapshotJob.Target, false,
		snapshotMarker+"/"+debRelease); !errors.Is(err, kernel.ErrNotFound) {
		t.Errorf("refused snapshot copied its Release: %v", err)
	}
}

// TestAccDebianSnapshotReadsNeedAStoreAndACaller: the snapshot read route
// answers 501 on a server without a store and 401 without a caller,
// before it looks for the snapshot.
func TestAccDebianSnapshotReadsNeedAStoreAndACaller(t *testing.T) {
	w := snapshotWorld(t)
	for _, tc := range []struct {
		name    string
		svc     DebianAPI
		ctx     context.Context
		status  int
		message string
	}{
		{
			"no store",
			DebianAPI{itemkit.New(itemkit.Deps{})},
			adminContext(t.Context()),
			http.StatusNotImplemented, "not implemented by artifactweave yet",
		},
		{
			"no caller",
			DebianAPI{newKit(withAccess(t, Config{Store: w.db, Blobs: w.blobs}))},
			t.Context(), http.StatusUnauthorized, "Authentication is required",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := chi.NewRouter()
			tc.svc.RegisterDebianSnapshotRoutes(r)
			req := httptest.NewRequestWithContext(tc.ctx, http.MethodGet,
				"/artifactory/api/deb/deb-target/snapshots/first/"+debRelease, nil)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			wantError(t, rec, tc.status, tc.message)
		})
	}
}

package storetest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// runEnterprise covers release bundles, Distribution, the archive tier and
// Workers.
func runEnterprise(t *testing.T, newStore Factory) {
	t.Run("Bundles", func(t *testing.T) { testBundles(t, newStore(t)) })
	t.Run("BundleRetention", func(t *testing.T) { testBundleRetention(t, newStore(t)) })
	t.Run("BundleExports", func(t *testing.T) { testBundleExports(t, newStore(t)) })
	t.Run("Distribution", func(t *testing.T) { testDistribution(t, newStore(t)) })
	t.Run("ArchivePolicies", func(t *testing.T) { testArchivePolicies(t, newStore(t)) })
	t.Run("ArchiveExecutions", func(t *testing.T) { testArchiveExecutions(t, newStore(t)) })
	t.Run("ArchivedItems", func(t *testing.T) { testArchivedItems(t, newStore(t)) })
	t.Run("Workers", func(t *testing.T) { testWorkers(t, newStore(t)) })
	t.Run("WorkerState", func(t *testing.T) { testWorkerState(t, newStore(t)) })
	t.Run("WorkerSecrets", func(t *testing.T) { testWorkerSecrets(t, newStore(t)) })
	t.Run("WorkerExecutions", func(t *testing.T) { testWorkerExecutions(t, newStore(t)) })
}

// fxBundle builds an open release bundle version.
func fxBundle(name, version string) types.ReleaseBundle {
	return types.ReleaseBundle{
		Name:              name,
		Version:           version,
		Description:       "release " + version,
		Notes:             types.ReleaseNotes{Syntax: "markdown", Content: "# " + version},
		StoringRepository: "release-bundles",
		State:             types.BundleOpen,
		Spec: types.BundleSpec{Queries: []types.BundleQuery{{
			Name:     "q1",
			AQL:      `items.find({"repo":"libs-release"})`,
			Mappings: []types.PathMapping{{Input: "libs-release/(.*)", Output: "prod/$1"}},
			AddedProps: []types.PropertyValues{
				{Key: "release", Values: []string{version}},
			},
			ExcludePropsPatterns: []string{"internal.*"},
		}}},
		ProjectKey: "",
		Origin:     types.BundleSource,
		Created:    at(1),
		CreatedBy:  "admin",
	}
}

// fxArtifact builds a bundle artifact at target; the bundle fields are left
// empty because the store sets them.
func fxArtifact(target string, i int) types.BundleArtifact {
	return types.BundleArtifact{
		SourcePath: "libs-release/" + target,
		TargetPath: target,
		Checksums:  blob(i).Checksums,
		Size:       int64(10 * i),
		Props:      []types.PropertyValues{{Key: "build", Values: []string{fmt.Sprint(i)}}},
	}
}

// owned is a with its bundle fields set to name and version.
func owned(name, version string, a ...types.BundleArtifact) []types.BundleArtifact {
	out := make([]types.BundleArtifact, len(a))
	for i, v := range a {
		v.BundleName, v.BundleVersion = name, version
		out[i] = v
	}
	return out
}

func testBundles(t *testing.T, s store.Store) {
	ctx := context.Background()
	bs := s.Bundles()
	b := fxBundle("app", "1.0")
	jar, pom := fxArtifact("com/acme/app.jar", 1), fxArtifact("com/acme/app.pom", 2)
	must(t, bs.Create(ctx, b, []types.BundleArtifact{pom, jar}))
	same(t, "Get", b, get[types.ReleaseBundle](t)(bs.Get(ctx, "app", "1.0")))
	same(t, "Artifacts", owned("app", "1.0", jar, pom),
		get[[]types.BundleArtifact](t)(bs.Artifacts(ctx, "app", "1.0")))

	wantErr(t, bs.Create(ctx, b, nil), store.ErrConflict)
	// Two artifacts on one target path fail the whole create.
	wantErr(
		t,
		bs.Create(ctx, fxBundle("app", "dup"), []types.BundleArtifact{jar, jar}),
		store.ErrConflict,
	)
	_, err := bs.Get(ctx, "app", "dup")
	wantErr(t, err, store.ErrNotFound)
	_, err = bs.Get(ctx, "app", "9.9")
	wantErr(t, err, store.ErrNotFound)
	_, err = bs.Artifacts(ctx, "app", "9.9")
	wantErr(t, err, store.ErrNotFound)

	// Returned values are copies.
	got := get[types.ReleaseBundle](t)(bs.Get(ctx, "app", "1.0"))
	got.Spec.Queries[0].AQL = "changed"
	arts := get[[]types.BundleArtifact](t)(bs.Artifacts(ctx, "app", "1.0"))
	arts[0].Props[0].Values[0] = "changed"
	same(t, "Get after mutating a copy", b, get[types.ReleaseBundle](t)(bs.Get(ctx, "app", "1.0")))
	same(t, "Artifacts after mutating a copy", owned("app", "1.0", jar, pom),
		get[[]types.BundleArtifact](t)(bs.Artifacts(ctx, "app", "1.0")))

	// While open the artifact list can be replaced; signing freezes it.
	exe := fxArtifact("bin/app.exe", 3)
	must(t, bs.ReplaceArtifacts(ctx, "app", "1.0", []types.BundleArtifact{exe, jar}))
	same(t, "Artifacts after replace", owned("app", "1.0", exe, jar),
		get[[]types.BundleArtifact](t)(bs.Artifacts(ctx, "app", "1.0")))
	wantErr(
		t,
		bs.ReplaceArtifacts(ctx, "app", "1.0", []types.BundleArtifact{exe, exe}),
		store.ErrConflict,
	)
	same(t, "Artifacts after a failed replace", owned("app", "1.0", exe, jar),
		get[[]types.BundleArtifact](t)(bs.Artifacts(ctx, "app", "1.0")))
	wantErr(t, bs.ReplaceArtifacts(ctx, "app", "9.9", nil), store.ErrNotFound)

	signed := b
	signed.State = types.BundleSigned
	signed.SignedAt = at(5)
	signed.SigningKeyAlias = "release-key"
	signed.Signature = []byte{0x00, 0x01, 0xfe, 0xff, '\n'}
	signed.Description = "signed"
	must(t, bs.Update(ctx, signed))
	same(t, "Get after Update", signed, get[types.ReleaseBundle](t)(bs.Get(ctx, "app", "1.0")))
	wantErr(t, bs.ReplaceArtifacts(ctx, "app", "1.0", nil), store.ErrConflict)
	wantErr(t, bs.Update(ctx, fxBundle("app", "9.9")), store.ErrNotFound)

	// Listing pages in byte order of (name, version) with filters.
	received := fxBundle("lib", "0.1")
	received.Origin = types.BundleReceived
	received.ProjectKey = "p1"
	for _, v := range []types.ReleaseBundle{
		fxBundle("app", "2.0"), fxBundle("app", "1.1"), received, fxBundle("Zed", "1"),
	} {
		must(t, bs.Create(ctx, v, nil))
	}
	keys := func(f store.BundleFilter) []string {
		var out []string
		for _, v := range pages(t, 2, func(p store.Page) ([]types.ReleaseBundle, string, error) {
			return bs.List(ctx, f, p)
		}) {
			out = append(out, v.Name+"/"+v.Version)
		}
		return out
	}
	same(
		t,
		"List",
		[]string{"Zed/1", "app/1.0", "app/1.1", "app/2.0", "lib/0.1"},
		keys(store.BundleFilter{}),
	)
	same(
		t,
		"List by name",
		[]string{"app/1.0", "app/1.1", "app/2.0"},
		keys(store.BundleFilter{Name: "app"}),
	)
	same(
		t,
		"List by origin",
		[]string{"lib/0.1"},
		keys(store.BundleFilter{Origin: types.BundleReceived}),
	)
	same(t, "List by project", []string{"lib/0.1"}, keys(store.BundleFilter{ProjectKey: "p1"}))
	_, _, err = bs.List(ctx, store.BundleFilter{}, store.Page{After: store.Cursor("only-one-part")})
	wantErr(t, err, store.ErrInvalidCursor)
	_, _, err = bs.List(ctx, store.BundleFilter{}, store.Page{After: "%%%"})
	wantErr(t, err, store.ErrInvalidCursor)

	must(t, bs.Delete(ctx, "app", "1.0"))
	_, err = bs.Get(ctx, "app", "1.0")
	wantErr(t, err, store.ErrNotFound)
	_, err = bs.Artifacts(ctx, "app", "1.0")
	wantErr(t, err, store.ErrNotFound)
	wantErr(t, bs.Delete(ctx, "app", "1.0"), store.ErrNotFound)
	// The artifacts went with the version: recreating it starts empty.
	must(t, bs.Create(ctx, b, nil))
	same(t, "Artifacts of a recreated bundle", []types.BundleArtifact(nil),
		get[[]types.BundleArtifact](t)(bs.Artifacts(ctx, "app", "1.0")))
}

func testBundleRetention(t *testing.T, s store.Store) {
	ctx := context.Background()
	bs := s.Bundles()
	must(t, bs.Create(ctx, fxBundle("app", "1.0"), nil))
	kept := func() bool { return get[bool](t)(bs.Kept(ctx, "app", "1.0")) }
	if kept() {
		t.Fatal("a new bundle is not kept")
	}
	must(t, bs.SetKeep(ctx, "app", "1.0", true))
	must(t, bs.SetKeep(ctx, "app", "1.0", true))
	if !kept() {
		t.Fatal("SetKeep(true) did not mark the bundle")
	}
	must(t, bs.SetKeep(ctx, "app", "1.0", false))
	must(t, bs.SetKeep(ctx, "app", "1.0", false))
	if kept() {
		t.Fatal("SetKeep(false) did not clear the mark")
	}
	wantErr(t, bs.SetKeep(ctx, "app", "9.9", true), store.ErrNotFound)
	_, err := bs.Kept(ctx, "app", "9.9")
	wantErr(t, err, store.ErrNotFound)

	// The keep mark goes with the version.
	must(t, bs.SetKeep(ctx, "app", "1.0", true))
	must(t, bs.Delete(ctx, "app", "1.0"))
	must(t, bs.Create(ctx, fxBundle("app", "1.0"), nil))
	if kept() {
		t.Fatal("a recreated bundle inherited the keep mark")
	}

	audits := []types.BundleRetentionAudit{
		{
			BundleName:    "app",
			BundleVersion: "1.0",
			Operation:     types.BundleKeep,
			Created:       at(1),
			CreatedBy:     "a",
		},
		{
			BundleName:    "app",
			BundleVersion: "0.9",
			Operation:     types.BundleDelete,
			Created:       at(3),
			CreatedBy:     "retention",
		},
		{
			BundleName:    "lib",
			BundleVersion: "2",
			Operation:     types.BundleUnkeep,
			Created:       at(2),
			CreatedBy:     "b",
		},
		// Same instant as the first: the later record lists first.
		{
			BundleName:    "app",
			BundleVersion: "1.0",
			Operation:     types.BundleUnkeep,
			Created:       at(1),
			CreatedBy:     "c",
		},
	}
	for _, a := range audits {
		must(t, bs.AddAudit(ctx, a))
	}
	same(t, "Audits(app)", []types.BundleRetentionAudit{audits[1], audits[3], audits[0]},
		get[[]types.BundleRetentionAudit](t)(bs.Audits(ctx, "app")))
	same(t, "Audits()", []types.BundleRetentionAudit{audits[1], audits[2], audits[3], audits[0]},
		get[[]types.BundleRetentionAudit](t)(bs.Audits(ctx, "")))
	// The trail outlives the versions it names.
	must(t, bs.Delete(ctx, "app", "1.0"))
	if n := len(get[[]types.BundleRetentionAudit](t)(bs.Audits(ctx, "app"))); n != 3 {
		t.Fatalf("%d audits after deleting the bundle, want 3", n)
	}
}

func testBundleExports(t *testing.T, s store.Store) {
	ctx := context.Background()
	bs := s.Bundles()
	e := types.BundleExport{
		BundleName:    "app",
		BundleVersion: "1.0",
		Status:        types.JobInProgress,
		Created:       at(2),
	}
	wantErr(t, bs.PutExport(ctx, e), store.ErrNotFound)
	must(t, bs.Create(ctx, fxBundle("app", "1.0"), nil))
	must(t, bs.PutExport(ctx, e))
	same(t, "Export", e, get[types.BundleExport](t)(bs.Export(ctx, "app", "1.0")))
	e.Status, e.ArchiveSHA256 = types.JobCompleted, blob(4).Checksums.SHA256
	must(t, bs.PutExport(ctx, e))
	same(t, "Export after replace", e, get[types.BundleExport](t)(bs.Export(ctx, "app", "1.0")))
	must(t, bs.DeleteExport(ctx, "app", "1.0"))
	_, err := bs.Export(ctx, "app", "1.0")
	wantErr(t, err, store.ErrNotFound)
	wantErr(t, bs.DeleteExport(ctx, "app", "1.0"), store.ErrNotFound)

	// The export goes with the version.
	must(t, bs.PutExport(ctx, e))
	must(t, bs.Delete(ctx, "app", "1.0"))
	must(t, bs.Create(ctx, fxBundle("app", "1.0"), nil))
	_, err = bs.Export(ctx, "app", "1.0")
	wantErr(t, err, store.ErrNotFound)
}

// fxTracker builds a distribution tracker for a bundle version.
func fxTracker(name, version string, i int) types.DistributionTracker {
	return types.DistributionTracker{
		BundleName:                    name,
		BundleVersion:                 version,
		Kind:                          types.TrackerDistribute,
		DryRun:                        i%2 == 0,
		AutoCreateMissingRepositories: true,
		Rules: []types.DistributionRule{
			{SiteName: "edge-*", CountryCodes: []string{"GB", "IE"}, Priority: i},
		},
		Status:    types.JobInQueue,
		Created:   at(i),
		CreatedBy: "admin",
	}
}

func testDistribution(t *testing.T, s store.Store) {
	ctx := context.Background()
	ds := s.Distribution()
	t1 := fxTracker("app", "1.0", 1)
	jobB := types.DistributionJob{PeerInstanceID: "edge-b", Status: types.JobInQueue, Priority: 2}
	jobA := types.DistributionJob{
		PeerInstanceID: "edge-a",
		Status:         types.JobInProgress,
		Started:        at(2),
	}
	got := get[types.DistributionTracker](
		t,
	)(
		ds.CreateTracker(ctx, t1, []types.DistributionJob{jobB, jobA}),
	)
	if got.ID <= 0 {
		t.Fatalf("tracker ID = %d", got.ID)
	}
	t1.ID = got.ID
	same(t, "CreateTracker", t1, got)
	same(t, "Tracker", t1, get[types.DistributionTracker](t)(ds.Tracker(ctx, t1.ID)))
	jobA.TrackerID, jobB.TrackerID = t1.ID, t1.ID
	same(
		t,
		"Jobs",
		[]types.DistributionJob{jobA, jobB},
		get[[]types.DistributionJob](t)(ds.Jobs(ctx, t1.ID)),
	)

	_, err := ds.CreateTracker(ctx, fxTracker("app", "1.0", 9), []types.DistributionJob{jobA, jobA})
	wantErr(t, err, store.ErrConflict)
	if n := len(get[[]types.DistributionTracker](t)(ds.Trackers(ctx, "", ""))); n != 1 {
		t.Fatalf("%d trackers after a failed create, want 1", n)
	}
	_, err = ds.Tracker(ctx, t1.ID+100)
	wantErr(t, err, store.ErrNotFound)

	t2 := get[types.DistributionTracker](t)(ds.CreateTracker(ctx, fxTracker("app", "1.0", 2), nil))
	t3 := get[types.DistributionTracker](t)(ds.CreateTracker(
		ctx,
		fxTracker("app", "2.0", 3),
		[]types.DistributionJob{
			{PeerInstanceID: "edge-a", Status: types.JobFailed, Error: "unreachable"},
		},
	))
	t4 := get[types.DistributionTracker](t)(ds.CreateTracker(ctx, fxTracker("lib", "1", 4), nil))
	if !(t1.ID < t2.ID && t2.ID < t3.ID && t3.ID < t4.ID) {
		t.Fatalf("tracker IDs not increasing: %d %d %d %d", t1.ID, t2.ID, t3.ID, t4.ID)
	}
	ids := func(name, version string) []int64 {
		var out []int64
		for _, v := range get[[]types.DistributionTracker](t)(ds.Trackers(ctx, name, version)) {
			out = append(out, v.ID)
		}
		return out
	}
	same(t, "Trackers(app, 1.0)", []int64{t2.ID, t1.ID}, ids("app", "1.0"))
	same(t, "Trackers(app)", []int64{t3.ID, t2.ID, t1.ID}, ids("app", ""))
	same(t, "Trackers()", []int64{t4.ID, t3.ID, t2.ID, t1.ID}, ids("", ""))

	t1.Status = types.JobCompleted
	t1.Rules = nil
	must(t, ds.UpdateTracker(ctx, t1))
	same(t, "Tracker after update", t1, get[types.DistributionTracker](t)(ds.Tracker(ctx, t1.ID)))
	missing := t1
	missing.ID = t4.ID + 100
	wantErr(t, ds.UpdateTracker(ctx, missing), store.ErrNotFound)

	jobA.Status, jobA.Finished = types.JobCompleted, at(4)
	must(t, ds.PutJob(ctx, jobA))
	jobC := types.DistributionJob{
		TrackerID:      t1.ID,
		PeerInstanceID: "edge-c",
		Status:         types.JobNotDistributed,
	}
	must(t, ds.PutJob(ctx, jobC))
	same(t, "Jobs after PutJob", []types.DistributionJob{jobA, jobB, jobC},
		get[[]types.DistributionJob](t)(ds.Jobs(ctx, t1.ID)))
	wantErr(
		t,
		ds.PutJob(ctx, types.DistributionJob{TrackerID: missing.ID, PeerInstanceID: "x"}),
		store.ErrNotFound,
	)
	_, err = ds.Jobs(ctx, missing.ID)
	wantErr(t, err, store.ErrNotFound)
	same(t, "Jobs of a tracker without jobs", []types.DistributionJob(nil),
		get[[]types.DistributionJob](t)(ds.Jobs(ctx, t2.ID)))

	failed := types.DistributionJob{
		TrackerID: t3.ID, PeerInstanceID: "edge-a", Status: types.JobFailed, Error: "unreachable",
	}
	same(t, "JobsForPeer", []types.DistributionJob{jobA, failed},
		get[[]types.DistributionJob](t)(ds.JobsForPeer(ctx, "edge-a", nil)))
	same(
		t,
		"JobsForPeer by status",
		[]types.DistributionJob{failed},
		get[[]types.DistributionJob](
			t,
		)(
			ds.JobsForPeer(ctx, "edge-a", []types.JobStatus{types.JobFailed, types.JobAborted}),
		),
	)
	same(t, "JobsForPeer unknown", []types.DistributionJob(nil),
		get[[]types.DistributionJob](t)(ds.JobsForPeer(ctx, "edge-z", nil)))
}

// fxPolicy builds an archive policy.
func fxPolicy(key string, family types.ArchiveFamily) types.ArchivePolicy {
	return types.ArchivePolicy{
		Key:              key,
		Family:           family,
		Description:      "archive " + key,
		Enabled:          true,
		CronExp:          "0 0 2 * * ?",
		ProjectKey:       "p1",
		DurationMinutes:  60,
		ExpirationMonths: 24,
		SkipTrashcan:     true,
		Criteria:         []byte(`{"repos":["libs-release"],"createdBeforeInMonths":12}`),
		Created:          at(1),
		Updated:          at(2),
	}
}

func testArchivePolicies(t *testing.T, s store.Store) {
	ctx := context.Background()
	as := s.Archive()
	p := fxPolicy("old-jars", types.ArchiveRetentionV1)
	must(t, as.CreatePolicy(ctx, p))
	same(t, "Policy", p, get[types.ArchivePolicy](t)(as.Policy(ctx, "old-jars")))
	wantErr(t, as.CreatePolicy(ctx, p), store.ErrConflict)
	_, err := as.Policy(ctx, "nope")
	wantErr(t, err, store.ErrNotFound)

	p.Enabled, p.Criteria, p.Updated = false, nil, at(9)
	must(t, as.UpdatePolicy(ctx, p))
	same(t, "Policy after update", p, get[types.ArchivePolicy](t)(as.Policy(ctx, "old-jars")))
	wantErr(t, as.UpdatePolicy(ctx, fxPolicy("nope", types.ArchiveSmartV2)), store.ErrNotFound)

	must(t, as.CreatePolicy(ctx, fxPolicy("Smart", types.ArchiveSmartV2)))
	must(t, as.CreatePolicy(ctx, fxPolicy("docker", types.ArchiveSmartV2)))
	keys := func(f types.ArchiveFamily) []string {
		var out []string
		for _, v := range get[[]types.ArchivePolicy](t)(as.Policies(ctx, f)) {
			out = append(out, v.Key)
		}
		return out
	}
	same(t, "Policies", []string{"Smart", "docker", "old-jars"}, keys(""))
	same(t, "Policies(v2)", []string{"Smart", "docker"}, keys(types.ArchiveSmartV2))

	must(t, as.DeletePolicy(ctx, "docker"))
	_, err = as.Policy(ctx, "docker")
	wantErr(t, err, store.ErrNotFound)
	wantErr(t, as.DeletePolicy(ctx, "docker"), store.ErrNotFound)
}

func testArchiveExecutions(t *testing.T, s store.Store) {
	ctx := context.Background()
	as := s.Archive()
	var created []types.ArchiveExecution
	for i := 1; i <= 5; i++ {
		e := types.ArchiveExecution{
			PolicyKey:   []string{"a", "b"}[i%2],
			Kind:        []types.ExecutionKind{types.ExecutionArchive, types.ExecutionRestore}[i/4],
			State:       types.ExecutionInProgress,
			Started:     at(i),
			Total:       int64(10 * i),
			Completed:   int64(i),
			TotalBytes:  int64(1000 * i),
			TriggeredBy: "scheduler",
			Destination: []byte(fmt.Sprintf(`{"tier":"cold-%d"}`, i)),
		}
		got := get[types.ArchiveExecution](t)(as.CreateExecution(ctx, e))
		if got.ID <= 0 || (len(created) > 0 && got.ID <= created[len(created)-1].ID) {
			t.Fatalf("execution ID %d not increasing", got.ID)
		}
		e.ID = got.ID
		same(t, "CreateExecution", e, got)
		created = append(created, e)
	}
	same(
		t,
		"Execution",
		created[2],
		get[types.ArchiveExecution](t)(as.Execution(ctx, created[2].ID)),
	)
	_, err := as.Execution(ctx, created[4].ID+100)
	wantErr(t, err, store.ErrNotFound)

	done := created[2]
	done.State, done.Finished, done.Completed, done.Failed = types.ExecutionCompleteWithErrors, at(
		20,
	), 28, 2
	done.SuccessBytes, done.FailureBytes, done.Destination = 2900, 100, nil
	must(t, as.UpdateExecution(ctx, done))
	same(
		t,
		"Execution after update",
		done,
		get[types.ArchiveExecution](t)(as.Execution(ctx, done.ID)),
	)
	missing := done
	missing.ID = created[4].ID + 100
	wantErr(t, as.UpdateExecution(ctx, missing), store.ErrNotFound)

	ids := func(f store.ExecutionFilter) []int64 {
		var out []int64
		for _, v := range pages(t, 2, func(p store.Page) ([]types.ArchiveExecution, string, error) {
			return as.Executions(ctx, f, p)
		}) {
			out = append(out, v.ID)
		}
		return out
	}
	id := func(i int) int64 { return created[i-1].ID }
	same(t, "Executions", []int64{id(5), id(4), id(3), id(2), id(1)}, ids(store.ExecutionFilter{}))
	same(
		t,
		"Executions(policy b)",
		[]int64{id(5), id(3), id(1)},
		ids(store.ExecutionFilter{PolicyKey: "b"}),
	)
	same(
		t,
		"Executions(restore)",
		[]int64{id(5), id(4)},
		ids(store.ExecutionFilter{Kind: types.ExecutionRestore}),
	)
	_, _, err = as.Executions(ctx, store.ExecutionFilter{}, store.Page{After: store.Cursor("x")})
	wantErr(t, err, store.ErrInvalidCursor)
	_, _, err = as.Executions(ctx, store.ExecutionFilter{}, store.Page{After: "%%%"})
	wantErr(t, err, store.ErrInvalidCursor)
}

// fxArchived builds the archived copy of repoKey/full.
func fxArchived(repoKey, full string, i int) types.ArchivedItem {
	parent, name, depth, err := types.SplitItemPath(full)
	if err != nil {
		panic(err)
	}
	return types.ArchivedItem{
		Namespace: "ns-1",
		Item: types.Item{
			ID:      int64(100 + i),
			RepoKey: repoKey,
			Path:    parent,
			Name:    name,
			Depth:   depth,
			Size: int64(
				10 * i,
			),
			Actual:     blob(i).Checksums,
			Original:   types.Checksums{SHA1: blob(i).Checksums.SHA1},
			MimeType:   "application/java-archive",
			Created:    at(i),
			CreatedBy:  "ci",
			Modified:   at(i + 1),
			ModifiedBy: "ci",
			Updated:    at(i + 2),
		},
		Properties: types.Properties{"build.number": {fmt.Sprint(i)}, "env": {"dev", "prod"}},
		Stats: types.ItemStats{
			DownloadCount:    int64(i),
			LastDownloaded:   at(i + 3),
			LastDownloadedBy: "u",
		},
		PackageType:    "maven",
		PackageName:    "com.acme:app",
		PackageVersion: fmt.Sprintf("1.%d", i),
		PolicyKey:      []string{"p-a", "p-b"}[i%2],
		ExecutionID:    int64(i%2 + 1),
		ArchivedAt:     at(i + 10),
		ColdBlobSHA256: blob(i).Checksums.SHA256,
	}
}

func testArchivedItems(t *testing.T, s store.Store) {
	ctx := context.Background()
	as := s.Archive()
	var added []types.ArchivedItem
	add := func(a types.ArchivedItem) types.ArchivedItem {
		t.Helper()
		got := get[types.ArchivedItem](t)(as.AddItem(ctx, a))
		if got.ID <= 0 {
			t.Fatalf("archived item ID = %d", got.ID)
		}
		a.ID = got.ID
		same(t, "AddItem", a, got)
		added = append(added, a)
		return a
	}
	first := add(fxArchived("libs", "com/acme/app.jar", 1))
	same(t, "Item", first, get[types.ArchivedItem](t)(as.Item(ctx, first.ID)))
	_, err := as.Item(ctx, first.ID+100)
	wantErr(t, err, store.ErrNotFound)

	// A second archived copy of the same path lists first.
	second := fxArchived("libs", "com/acme/app.jar", 2)
	second.ExpiresAt = at(30)
	second = add(second)
	add(fxArchived("libs", "com/acme/other.jar", 3))
	root := fxArchived("other", "readme.txt", 4)
	root.ExpiresAt = at(25)
	root = add(root)
	late := fxArchived("libs", "z.jar", 5)
	late.ExpiresAt = at(40)
	late = add(late)

	same(t, "FindItems", []types.ArchivedItem{second, first},
		get[[]types.ArchivedItem](t)(as.FindItems(ctx, "libs", "/com/acme/app.jar")))
	same(t, "FindItems at the root", []types.ArchivedItem{root},
		get[[]types.ArchivedItem](t)(as.FindItems(ctx, "other", "readme.txt")))
	same(t, "FindItems none", []types.ArchivedItem(nil),
		get[[]types.ArchivedItem](t)(as.FindItems(ctx, "other", "com/acme/app.jar")))
	_, err = as.FindItems(ctx, "libs", "a/../b")
	wantErr(t, err, store.ErrInvalid)
	bad := fxArchived("libs", "x.jar", 6)
	bad.Item.Path, bad.Item.Name = ".", ""
	_, err = as.AddItem(ctx, bad)
	wantErr(t, err, store.ErrInvalid)

	ids := func(f store.ArchivedFilter) []int64 {
		var out []int64
		for _, v := range pages(t, 2, func(p store.Page) ([]types.ArchivedItem, string, error) {
			return as.Items(ctx, f, p)
		}) {
			out = append(out, v.ID)
		}
		return out
	}
	id := func(i int) int64 { return added[i-1].ID }
	same(t, "Items", []int64{id(1), id(2), id(3), id(4), id(5)}, ids(store.ArchivedFilter{}))
	same(
		t,
		"Items(libs)",
		[]int64{id(1), id(2), id(3), id(5)},
		ids(store.ArchivedFilter{RepoKey: "libs"}),
	)
	same(
		t,
		"Items(policy p-b)",
		[]int64{id(1), id(3), id(5)},
		ids(store.ArchivedFilter{PolicyKey: "p-b"}),
	)
	same(t, "Items(execution 1)", []int64{id(2), id(4)}, ids(store.ArchivedFilter{ExecutionID: 1}))
	_, _, err = as.Items(
		ctx,
		store.ArchivedFilter{},
		store.Page{After: store.Cursor("not-a-number")},
	)
	wantErr(t, err, store.ErrInvalidCursor)
	_, _, err = as.Items(ctx, store.ArchivedFilter{}, store.Page{After: "%%%"})
	wantErr(t, err, store.ErrInvalidCursor)

	same(t, "ExpiredItems", []types.ArchivedItem{root, second},
		get[[]types.ArchivedItem](t)(as.ExpiredItems(ctx, at(35), 0)))
	same(t, "ExpiredItems limited", []types.ArchivedItem{root},
		get[[]types.ArchivedItem](t)(as.ExpiredItems(ctx, at(35), 1)))
	same(t, "ExpiredItems at the boundary", []types.ArchivedItem{root},
		get[[]types.ArchivedItem](t)(as.ExpiredItems(ctx, at(30), 0)))

	// Returned values are copies.
	got := get[types.ArchivedItem](t)(as.Item(ctx, first.ID))
	got.Properties["env"][0] = "changed"
	same(t, "Item after mutating a copy", first, get[types.ArchivedItem](t)(as.Item(ctx, first.ID)))

	must(t, as.DeleteItem(ctx, first.ID))
	_, err = as.Item(ctx, first.ID)
	wantErr(t, err, store.ErrNotFound)
	wantErr(t, as.DeleteItem(ctx, first.ID), store.ErrNotFound)
}

// fxWorker builds a worker.
func fxWorker(key string, action types.WorkerAction) types.Worker {
	return types.Worker{
		Key:         key,
		Action:      action,
		SourceCode:  "export default async (ctx, data) => ({ status: 'DOWNLOAD_PROCEED' });",
		Enabled:     true,
		Description: "worker " + key,
		Filter: types.WorkerFilter{
			RepoKeys: []string{"libs"}, IncludePatterns: []string{"**/*.jar"}, AnyLocal: true,
		},
		ProgramHash: "sha256:abc",
		Created:     at(1),
		Updated:     at(2),
	}
}

func testWorkers(t *testing.T, s store.Store) {
	ctx := context.Background()
	ws := s.Workers()
	w := fxWorker("block-snapshots", "BEFORE_DOWNLOAD")
	must(t, ws.Create(ctx, w))
	same(t, "Get", w, get[types.Worker](t)(ws.Get(ctx, w.Key)))
	wantErr(t, ws.Create(ctx, w), store.ErrConflict)
	_, err := ws.Get(ctx, "nope")
	wantErr(t, err, store.ErrNotFound)

	w.Enabled, w.Failures, w.BlockedUntil, w.Debug, w.Shared = false, 3, at(10), true, true
	w.Filter = types.WorkerFilter{}
	must(t, ws.Update(ctx, w))
	same(t, "Get after update", w, get[types.Worker](t)(ws.Get(ctx, w.Key)))
	wantErr(t, ws.Update(ctx, fxWorker("nope", "AFTER_CREATE")), store.ErrNotFound)

	tag := fxWorker("Tagger", "AFTER_CREATE")
	tag.ProjectKey = "p1"
	must(t, ws.Create(ctx, tag))
	must(t, ws.Create(ctx, fxWorker("audit", "AFTER_CREATE")))
	keys := func(f store.WorkerFilter) []string {
		var out []string
		for _, v := range get[[]types.Worker](t)(ws.List(ctx, f)) {
			out = append(out, v.Key)
		}
		return out
	}
	same(t, "List", []string{"Tagger", "audit", "block-snapshots"}, keys(store.WorkerFilter{}))
	same(
		t,
		"List(action)",
		[]string{"Tagger", "audit"},
		keys(store.WorkerFilter{Action: "AFTER_CREATE"}),
	)
	same(t, "List(project)", []string{"Tagger"}, keys(store.WorkerFilter{ProjectKey: "p1"}))
	same(
		t,
		"List(enabled)",
		[]string{"Tagger", "audit"},
		keys(store.WorkerFilter{EnabledOnly: true}),
	)

	must(t, ws.Delete(ctx, "audit"))
	_, err = ws.Get(ctx, "audit")
	wantErr(t, err, store.ErrNotFound)
	wantErr(t, ws.Delete(ctx, "audit"), store.ErrNotFound)
}

func testWorkerState(t *testing.T, s store.Store) {
	ctx := context.Background()
	ws := s.Workers()
	must(t, ws.Create(ctx, fxWorker("w", "AFTER_CREATE")))
	same(
		t,
		"State of a new worker",
		map[string]string{},
		get[map[string]string](t)(ws.State(ctx, "w")),
	)
	must(t, ws.SetState(ctx, "w", "count", "1"))
	must(t, ws.SetState(ctx, "w", "count", "2"))
	must(t, ws.SetState(ctx, "w", "last", "lib.jar"))
	same(
		t,
		"State",
		map[string]string{"count": "2", "last": "lib.jar"},
		get[map[string]string](t)(ws.State(ctx, "w")),
	)
	must(t, ws.DeleteState(ctx, "w", "last"))
	must(t, ws.DeleteState(ctx, "w", "last"))
	same(
		t,
		"State after delete",
		map[string]string{"count": "2"},
		get[map[string]string](t)(ws.State(ctx, "w")),
	)
	wantErr(t, ws.SetState(ctx, "nope", "k", "v"), store.ErrNotFound)
	_, err := ws.State(ctx, "nope")
	wantErr(t, err, store.ErrNotFound)

	// State goes with the worker.
	must(t, ws.Delete(ctx, "w"))
	must(t, ws.Create(ctx, fxWorker("w", "AFTER_CREATE")))
	same(
		t,
		"State of a recreated worker",
		map[string]string{},
		get[map[string]string](t)(ws.State(ctx, "w")),
	)
}

func testWorkerSecrets(t *testing.T, s store.Store) {
	ctx := context.Background()
	ws := s.Workers()
	must(t, ws.Create(ctx, fxWorker("w", "AFTER_CREATE")))
	must(t, ws.Create(ctx, fxWorker("x", "AFTER_CREATE")))
	same(
		t,
		"Secrets of a new worker",
		map[string]string{},
		get[map[string]string](t)(ws.Secrets(ctx, "w")),
	)
	must(t, ws.PutSecret(ctx, "w", "token", "s3cret"))
	must(t, ws.PutSecret(ctx, "w", "token", "rotated"))
	must(t, ws.PutSecret(ctx, "w", "slack", "xoxb-1"))
	must(t, ws.PutSecret(ctx, "x", "token", "other"))
	same(t, "Secrets", map[string]string{"token": "rotated", "slack": "xoxb-1"},
		get[map[string]string](t)(ws.Secrets(ctx, "w")))
	same(t, "Secrets of another worker", map[string]string{"token": "other"},
		get[map[string]string](t)(ws.Secrets(ctx, "x")))
	must(t, ws.DeleteSecret(ctx, "w", "slack"))
	wantErr(t, ws.DeleteSecret(ctx, "w", "slack"), store.ErrNotFound)
	same(t, "Secrets after delete", map[string]string{"token": "rotated"},
		get[map[string]string](t)(ws.Secrets(ctx, "w")))
	wantErr(t, ws.PutSecret(ctx, "nope", "k", "v"), store.ErrNotFound)
	_, err := ws.Secrets(ctx, "nope")
	wantErr(t, err, store.ErrNotFound)

	// Secrets go with the worker.
	must(t, ws.Delete(ctx, "w"))
	must(t, ws.Create(ctx, fxWorker("w", "AFTER_CREATE")))
	same(
		t,
		"Secrets of a recreated worker",
		map[string]string{},
		get[map[string]string](t)(ws.Secrets(ctx, "w")),
	)
}

func testWorkerExecutions(t *testing.T, s store.Store) {
	ctx := context.Background()
	ws := s.Workers()
	must(t, ws.Create(ctx, fxWorker("a", "BEFORE_DOWNLOAD")))
	must(t, ws.Create(ctx, fxWorker("b", "AFTER_CREATE")))
	exec := func(id, worker string, started time.Time) types.WorkerExecution {
		return types.WorkerExecution{
			ID: id, WorkerKey: worker, Action: "BEFORE_DOWNLOAD", Trigger: types.TriggerEvent,
			Status: "STATUS_SUCCESS", Started: started,
			Duration: 1234567890 * time.Nanosecond, // 1234.56789 ms: stored as 1234 ms
			Payload:  []byte(`{"repoKey":"libs"}`), Result: []byte(`{"status":"DOWNLOAD_PROCEED"}`),
			Logs: []byte("line 1\nline 2\n"),
		}
	}
	e1, e2, e3 := exec("e1", "a", at(1)), exec("e2", "a", at(3)), exec("e3", "b", at(2))
	// e2 and e4 start in the same microsecond; the higher ID lists first.
	e4, e5 := exec("e4", "a", at(3)), exec("e5", "b", at(4))
	e5.Status, e5.Error, e5.Payload, e5.Result, e5.Logs = "STATUS_FAIL", "WORKER_TIMEOUT", nil, nil, nil
	for _, e := range []types.WorkerExecution{e3, e1, e4, e2, e5} {
		must(t, ws.AddExecution(ctx, e))
	}
	same(t, "Execution", e1, get[types.WorkerExecution](t)(ws.Execution(ctx, "e1")))
	if got := get[types.WorkerExecution](
		t,
	)(
		ws.Execution(ctx, "e1"),
	); got.Duration != 1234*time.Millisecond {
		t.Fatalf("Duration = %v, want 1.234s", got.Duration)
	}
	wantErr(t, ws.AddExecution(ctx, e1), store.ErrConflict)
	wantErr(t, ws.AddExecution(ctx, exec("e9", "nope", at(1))), store.ErrNotFound)
	_, err := ws.Execution(ctx, "nope")
	wantErr(t, err, store.ErrNotFound)

	ids := func(worker string) []string {
		var out []string
		for _, v := range pages(t, 2, func(p store.Page) ([]types.WorkerExecution, string, error) {
			return ws.Executions(ctx, worker, p)
		}) {
			out = append(out, v.ID)
		}
		return out
	}
	same(t, "Executions", []string{"e5", "e4", "e2", "e3", "e1"}, ids(""))
	same(t, "Executions(a)", []string{"e4", "e2", "e1"}, ids("a"))
	same(
		t,
		"Executions(b) full records",
		[]types.WorkerExecution{e5, e3},
		pages(
			t,
			1,
			func(p store.Page) ([]types.WorkerExecution, string, error) { return ws.Executions(ctx, "b", p) },
		),
	)
	_, _, err = ws.Executions(ctx, "", store.Page{After: store.Cursor("x", "e1")})
	wantErr(t, err, store.ErrInvalidCursor)
	_, _, err = ws.Executions(ctx, "", store.Page{After: "%%%"})
	wantErr(t, err, store.ErrInvalidCursor)

	n, err := ws.PruneExecutions(ctx, at(3))
	must(t, err)
	if n != 2 {
		t.Fatalf("PruneExecutions removed %d, want 2", n)
	}
	same(t, "Executions after prune", []string{"e5", "e4", "e2"}, ids(""))

	// Executions go with the worker.
	must(t, ws.Delete(ctx, "a"))
	same(t, "Executions after deleting a worker", []string{"e5"}, ids(""))
	_, err = ws.Execution(ctx, "e4")
	wantErr(t, err, store.ErrNotFound)
}

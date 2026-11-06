package storetest

import (
	"context"
	"fmt"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// runBundles covers release bundles and Distribution (decision 0023).
func runBundles(t *testing.T, newStore Factory) {
	t.Run("Bundles", func(t *testing.T) { testBundles(t, newStore(t)) })
	t.Run("BundleRetention", func(t *testing.T) { testBundleRetention(t, newStore(t)) })
	t.Run("BundleExports", func(t *testing.T) { testBundleExports(t, newStore(t)) })
	t.Run("Distribution", func(t *testing.T) { testDistribution(t, newStore(t)) })
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

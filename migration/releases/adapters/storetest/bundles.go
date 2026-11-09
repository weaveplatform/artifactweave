package storetest

import (
	"context"
	"fmt"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/platform/testkit/storekit"
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
		Created:    storekit.At(1),
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
	storekit.Must(t, bs.Create(ctx, b, []types.BundleArtifact{pom, jar}))
	storekit.Same(t, "Get", b, storekit.Get[types.ReleaseBundle](t)(bs.Get(ctx, "app", "1.0")))
	storekit.Same(t, "Artifacts", owned("app", "1.0", jar, pom),
		storekit.Get[[]types.BundleArtifact](t)(bs.Artifacts(ctx, "app", "1.0")))

	storekit.WantErr(t, bs.Create(ctx, b, nil), store.ErrConflict)
	// Two artifacts on one target path fail the whole create.
	storekit.WantErr(
		t,
		bs.Create(ctx, fxBundle("app", "dup"), []types.BundleArtifact{jar, jar}),
		store.ErrConflict,
	)
	_, err := bs.Get(ctx, "app", "dup")
	storekit.WantErr(t, err, store.ErrNotFound)
	_, err = bs.Get(ctx, "app", "9.9")
	storekit.WantErr(t, err, store.ErrNotFound)
	_, err = bs.Artifacts(ctx, "app", "9.9")
	storekit.WantErr(t, err, store.ErrNotFound)

	// Returned values are copies.
	got := storekit.Get[types.ReleaseBundle](t)(bs.Get(ctx, "app", "1.0"))
	got.Spec.Queries[0].AQL = "changed"
	arts := storekit.Get[[]types.BundleArtifact](t)(bs.Artifacts(ctx, "app", "1.0"))
	arts[0].Props[0].Values[0] = "changed"
	storekit.Same(
		t,
		"Get after mutating a copy",
		b,
		storekit.Get[types.ReleaseBundle](t)(bs.Get(ctx, "app", "1.0")),
	)
	storekit.Same(t, "Artifacts after mutating a copy", owned("app", "1.0", jar, pom),
		storekit.Get[[]types.BundleArtifact](t)(bs.Artifacts(ctx, "app", "1.0")))

	// While open the artifact list can be replaced; signing freezes it.
	exe := fxArtifact("bin/app.exe", 3)
	storekit.Must(t, bs.ReplaceArtifacts(ctx, "app", "1.0", []types.BundleArtifact{exe, jar}))
	storekit.Same(t, "Artifacts after replace", owned("app", "1.0", exe, jar),
		storekit.Get[[]types.BundleArtifact](t)(bs.Artifacts(ctx, "app", "1.0")))
	storekit.WantErr(
		t,
		bs.ReplaceArtifacts(ctx, "app", "1.0", []types.BundleArtifact{exe, exe}),
		store.ErrConflict,
	)
	storekit.Same(t, "Artifacts after a failed replace", owned("app", "1.0", exe, jar),
		storekit.Get[[]types.BundleArtifact](t)(bs.Artifacts(ctx, "app", "1.0")))
	storekit.WantErr(t, bs.ReplaceArtifacts(ctx, "app", "9.9", nil), store.ErrNotFound)

	signed := b
	signed.State = types.BundleSigned
	signed.SignedAt = storekit.At(5)
	signed.SigningKeyAlias = "release-key"
	signed.Signature = []byte{0x00, 0x01, 0xfe, 0xff, '\n'}
	signed.Description = "signed"
	storekit.Must(t, bs.Update(ctx, signed))
	storekit.Same(
		t,
		"Get after Update",
		signed,
		storekit.Get[types.ReleaseBundle](t)(bs.Get(ctx, "app", "1.0")),
	)
	storekit.WantErr(t, bs.ReplaceArtifacts(ctx, "app", "1.0", nil), store.ErrConflict)
	storekit.WantErr(t, bs.Update(ctx, fxBundle("app", "9.9")), store.ErrNotFound)

	// Listing pages in byte order of (name, version) with filters.
	received := fxBundle("lib", "0.1")
	received.Origin = types.BundleReceived
	received.ProjectKey = "p1"
	for _, v := range []types.ReleaseBundle{
		fxBundle("app", "2.0"), fxBundle("app", "1.1"), received, fxBundle("Zed", "1"),
	} {
		storekit.Must(t, bs.Create(ctx, v, nil))
	}
	keys := func(f store.BundleFilter) []string {
		var out []string
		for _, v := range storekit.Pages(t, 2, func(p store.Page) ([]types.ReleaseBundle, string, error) {
			return bs.List(ctx, f, p)
		}) {
			out = append(out, v.Name+"/"+v.Version)
		}
		return out
	}
	storekit.Same(
		t,
		"List",
		[]string{"Zed/1", "app/1.0", "app/1.1", "app/2.0", "lib/0.1"},
		keys(store.BundleFilter{}),
	)
	storekit.Same(
		t,
		"List by name",
		[]string{"app/1.0", "app/1.1", "app/2.0"},
		keys(store.BundleFilter{Name: "app"}),
	)
	storekit.Same(
		t,
		"List by origin",
		[]string{"lib/0.1"},
		keys(store.BundleFilter{Origin: types.BundleReceived}),
	)
	storekit.Same(
		t,
		"List by project",
		[]string{"lib/0.1"},
		keys(store.BundleFilter{ProjectKey: "p1"}),
	)
	_, _, err = bs.List(ctx, store.BundleFilter{}, store.Page{After: store.Cursor("only-one-part")})
	storekit.WantErr(t, err, store.ErrInvalidCursor)
	_, _, err = bs.List(ctx, store.BundleFilter{}, store.Page{After: "%%%"})
	storekit.WantErr(t, err, store.ErrInvalidCursor)

	storekit.Must(t, bs.Delete(ctx, "app", "1.0"))
	_, err = bs.Get(ctx, "app", "1.0")
	storekit.WantErr(t, err, store.ErrNotFound)
	_, err = bs.Artifacts(ctx, "app", "1.0")
	storekit.WantErr(t, err, store.ErrNotFound)
	storekit.WantErr(t, bs.Delete(ctx, "app", "1.0"), store.ErrNotFound)
	// The artifacts went with the version: recreating it starts empty.
	storekit.Must(t, bs.Create(ctx, b, nil))
	storekit.Same(t, "Artifacts of a recreated bundle", []types.BundleArtifact(nil),
		storekit.Get[[]types.BundleArtifact](t)(bs.Artifacts(ctx, "app", "1.0")))
}

func testBundleRetention(t *testing.T, s store.Store) {
	ctx := context.Background()
	bs := s.Bundles()
	storekit.Must(t, bs.Create(ctx, fxBundle("app", "1.0"), nil))
	kept := func() bool { return storekit.Get[bool](t)(bs.Kept(ctx, "app", "1.0")) }
	if kept() {
		t.Fatal("a new bundle is not kept")
	}
	storekit.Must(t, bs.SetKeep(ctx, "app", "1.0", true))
	storekit.Must(t, bs.SetKeep(ctx, "app", "1.0", true))
	if !kept() {
		t.Fatal("SetKeep(true) did not mark the bundle")
	}
	storekit.Must(t, bs.SetKeep(ctx, "app", "1.0", false))
	storekit.Must(t, bs.SetKeep(ctx, "app", "1.0", false))
	if kept() {
		t.Fatal("SetKeep(false) did not clear the mark")
	}
	storekit.WantErr(t, bs.SetKeep(ctx, "app", "9.9", true), store.ErrNotFound)
	_, err := bs.Kept(ctx, "app", "9.9")
	storekit.WantErr(t, err, store.ErrNotFound)

	// The keep mark goes with the version.
	storekit.Must(t, bs.SetKeep(ctx, "app", "1.0", true))
	storekit.Must(t, bs.Delete(ctx, "app", "1.0"))
	storekit.Must(t, bs.Create(ctx, fxBundle("app", "1.0"), nil))
	if kept() {
		t.Fatal("a recreated bundle inherited the keep mark")
	}

	audits := []types.BundleRetentionAudit{
		{
			BundleName:    "app",
			BundleVersion: "1.0",
			Operation:     types.BundleKeep,
			Created:       storekit.At(1),
			CreatedBy:     "a",
		},
		{
			BundleName:    "app",
			BundleVersion: "0.9",
			Operation:     types.BundleDelete,
			Created:       storekit.At(3),
			CreatedBy:     "retention",
		},
		{
			BundleName:    "lib",
			BundleVersion: "2",
			Operation:     types.BundleUnkeep,
			Created:       storekit.At(2),
			CreatedBy:     "b",
		},
		// Same instant as the first: the later record lists first.
		{
			BundleName:    "app",
			BundleVersion: "1.0",
			Operation:     types.BundleUnkeep,
			Created:       storekit.At(1),
			CreatedBy:     "c",
		},
	}
	for _, a := range audits {
		storekit.Must(t, bs.AddAudit(ctx, a))
	}
	storekit.Same(t, "Audits(app)", []types.BundleRetentionAudit{audits[1], audits[3], audits[0]},
		storekit.Get[[]types.BundleRetentionAudit](t)(bs.Audits(ctx, "app")))
	storekit.Same(
		t,
		"Audits()",
		[]types.BundleRetentionAudit{audits[1], audits[2], audits[3], audits[0]},
		storekit.Get[[]types.BundleRetentionAudit](t)(bs.Audits(ctx, "")),
	)
	// The trail outlives the versions it names.
	storekit.Must(t, bs.Delete(ctx, "app", "1.0"))
	if n := len(storekit.Get[[]types.BundleRetentionAudit](t)(bs.Audits(ctx, "app"))); n != 3 {
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
		Created:       storekit.At(2),
	}
	storekit.WantErr(t, bs.PutExport(ctx, e), store.ErrNotFound)
	storekit.Must(t, bs.Create(ctx, fxBundle("app", "1.0"), nil))
	storekit.Must(t, bs.PutExport(ctx, e))
	storekit.Same(t, "Export", e, storekit.Get[types.BundleExport](t)(bs.Export(ctx, "app", "1.0")))
	e.Status, e.ArchiveSHA256 = types.JobCompleted, blob(4).Checksums.SHA256
	storekit.Must(t, bs.PutExport(ctx, e))
	storekit.Same(
		t,
		"Export after replace",
		e,
		storekit.Get[types.BundleExport](t)(bs.Export(ctx, "app", "1.0")),
	)
	storekit.Must(t, bs.DeleteExport(ctx, "app", "1.0"))
	_, err := bs.Export(ctx, "app", "1.0")
	storekit.WantErr(t, err, store.ErrNotFound)
	storekit.WantErr(t, bs.DeleteExport(ctx, "app", "1.0"), store.ErrNotFound)

	// The export goes with the version.
	storekit.Must(t, bs.PutExport(ctx, e))
	storekit.Must(t, bs.Delete(ctx, "app", "1.0"))
	storekit.Must(t, bs.Create(ctx, fxBundle("app", "1.0"), nil))
	_, err = bs.Export(ctx, "app", "1.0")
	storekit.WantErr(t, err, store.ErrNotFound)
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
		Created:   storekit.At(i),
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
		Started:        storekit.At(2),
	}
	got := storekit.Get[types.DistributionTracker](
		t,
	)(
		ds.CreateTracker(ctx, t1, []types.DistributionJob{jobB, jobA}),
	)
	if got.ID <= 0 {
		t.Fatalf("tracker ID = %d", got.ID)
	}
	t1.ID = got.ID
	storekit.Same(t, "CreateTracker", t1, got)
	storekit.Same(
		t,
		"Tracker",
		t1,
		storekit.Get[types.DistributionTracker](t)(ds.Tracker(ctx, t1.ID)),
	)
	jobA.TrackerID, jobB.TrackerID = t1.ID, t1.ID
	storekit.Same(
		t,
		"Jobs",
		[]types.DistributionJob{jobA, jobB},
		storekit.Get[[]types.DistributionJob](t)(ds.Jobs(ctx, t1.ID)),
	)

	_, err := ds.CreateTracker(ctx, fxTracker("app", "1.0", 9), []types.DistributionJob{jobA, jobA})
	storekit.WantErr(t, err, store.ErrConflict)
	if n := len(storekit.Get[[]types.DistributionTracker](t)(ds.Trackers(ctx, "", ""))); n != 1 {
		t.Fatalf("%d trackers after a failed create, want 1", n)
	}
	_, err = ds.Tracker(ctx, t1.ID+100)
	storekit.WantErr(t, err, store.ErrNotFound)

	t2 := storekit.Get[types.DistributionTracker](
		t,
	)(
		ds.CreateTracker(ctx, fxTracker("app", "1.0", 2), nil),
	)
	t3 := storekit.Get[types.DistributionTracker](t)(ds.CreateTracker(
		ctx,
		fxTracker("app", "2.0", 3),
		[]types.DistributionJob{
			{PeerInstanceID: "edge-a", Status: types.JobFailed, Error: "unreachable"},
		},
	))
	t4 := storekit.Get[types.DistributionTracker](
		t,
	)(
		ds.CreateTracker(ctx, fxTracker("lib", "1", 4), nil),
	)
	if !(t1.ID < t2.ID && t2.ID < t3.ID && t3.ID < t4.ID) {
		t.Fatalf("tracker IDs not increasing: %d %d %d %d", t1.ID, t2.ID, t3.ID, t4.ID)
	}
	ids := func(name, version string) []int64 {
		var out []int64
		for _, v := range storekit.Get[[]types.DistributionTracker](t)(ds.Trackers(ctx, name, version)) {
			out = append(out, v.ID)
		}
		return out
	}
	storekit.Same(t, "Trackers(app, 1.0)", []int64{t2.ID, t1.ID}, ids("app", "1.0"))
	storekit.Same(t, "Trackers(app)", []int64{t3.ID, t2.ID, t1.ID}, ids("app", ""))
	storekit.Same(t, "Trackers()", []int64{t4.ID, t3.ID, t2.ID, t1.ID}, ids("", ""))

	t1.Status = types.JobCompleted
	t1.Rules = nil
	storekit.Must(t, ds.UpdateTracker(ctx, t1))
	storekit.Same(
		t,
		"Tracker after update",
		t1,
		storekit.Get[types.DistributionTracker](t)(ds.Tracker(ctx, t1.ID)),
	)
	missing := t1
	missing.ID = t4.ID + 100
	storekit.WantErr(t, ds.UpdateTracker(ctx, missing), store.ErrNotFound)

	jobA.Status, jobA.Finished = types.JobCompleted, storekit.At(4)
	storekit.Must(t, ds.PutJob(ctx, jobA))
	jobC := types.DistributionJob{
		TrackerID:      t1.ID,
		PeerInstanceID: "edge-c",
		Status:         types.JobNotDistributed,
	}
	storekit.Must(t, ds.PutJob(ctx, jobC))
	storekit.Same(t, "Jobs after PutJob", []types.DistributionJob{jobA, jobB, jobC},
		storekit.Get[[]types.DistributionJob](t)(ds.Jobs(ctx, t1.ID)))
	storekit.WantErr(
		t,
		ds.PutJob(ctx, types.DistributionJob{TrackerID: missing.ID, PeerInstanceID: "x"}),
		store.ErrNotFound,
	)
	_, err = ds.Jobs(ctx, missing.ID)
	storekit.WantErr(t, err, store.ErrNotFound)
	storekit.Same(t, "Jobs of a tracker without jobs", []types.DistributionJob(nil),
		storekit.Get[[]types.DistributionJob](t)(ds.Jobs(ctx, t2.ID)))

	failed := types.DistributionJob{
		TrackerID: t3.ID, PeerInstanceID: "edge-a", Status: types.JobFailed, Error: "unreachable",
	}
	storekit.Same(t, "JobsForPeer", []types.DistributionJob{jobA, failed},
		storekit.Get[[]types.DistributionJob](t)(ds.JobsForPeer(ctx, "edge-a", nil)))
	storekit.Same(
		t,
		"JobsForPeer by status",
		[]types.DistributionJob{failed},
		storekit.Get[[]types.DistributionJob](
			t,
		)(
			ds.JobsForPeer(ctx, "edge-a", []types.JobStatus{types.JobFailed, types.JobAborted}),
		),
	)
	storekit.Same(t, "JobsForPeer unknown", []types.DistributionJob(nil),
		storekit.Get[[]types.DistributionJob](t)(ds.JobsForPeer(ctx, "edge-z", nil)))
}

package item

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"slices"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// recorder is a format with every optional interface. It records each call
// and whether it ran inside a transaction, and lets a test decide what
// Inspect, Admit, Deployed, Annotated and Resolve do.
type recorder struct {
	generic.Handler
	calls map[string]bool // call → ran inside a transaction
	order []string

	inspection any
	inspectErr error
	content    []string // what Inspect read through Env.Blobs

	admit  func(format.Write) (format.Write, error)
	writes []format.Write

	deployed    func(ctx context.Context, env format.Env, it types.Item) error
	inspections []any
	props       []types.Properties // the item's properties when Deployed ran

	annotateErr error
	annotations []annotation

	resolve func(format.Download) (string, error)
}

type annotation struct {
	path string
	a    format.Annotation
}

func recording(t *testing.T) (*world, *recorder) {
	t.Helper()
	w := newWorld(t)
	r := &recorder{calls: map[string]bool{}}
	w.svc.Formats = func(repo.PackageType) format.Handler { return r }
	return w, r
}

// note records call. AfterCommit runs its function at once only outside a
// transaction, which tells the two apart.
func (r *recorder) note(ctx context.Context, env format.Env, call string) {
	inTx := true
	env.Store.AfterCommit(ctx, func() { inTx = false })
	r.calls[call] = inTx
	r.order = append(r.order, call)
}

func (r *recorder) reset() {
	r.calls, r.order = map[string]bool{}, nil
	r.writes, r.inspections, r.props, r.annotations = nil, nil, nil, nil
}

func (r *recorder) Inspect(
	ctx context.Context,
	env format.Env,
	_ repo.Config,
	w format.Write,
) (any, error) {
	r.note(ctx, env, "inspect "+w.Path)
	if rc, _, err := env.Blobs.Open(ctx, w.Checksums.SHA256); err == nil {
		b, _ := io.ReadAll(rc)
		_ = rc.Close()
		r.content = append(r.content, string(b))
	}
	return r.inspection, r.inspectErr
}

func (r *recorder) Admit(
	ctx context.Context,
	env format.Env,
	_ repo.Config,
	w format.Write,
) (format.Write, error) {
	r.note(ctx, env, fmt.Sprintf("admit %d %s", w.Op, w.Path))
	r.writes = append(r.writes, w)
	if r.admit != nil {
		return r.admit(w)
	}
	return w, nil
}

func (r *recorder) Deployed(
	ctx context.Context,
	env format.Env,
	_ repo.Config,
	it types.Item,
) error {
	r.note(ctx, env, "deployed "+it.FullPath())
	r.inspections = append(r.inspections, env.Inspection)
	props, err := env.Store.Items().Properties(ctx, it.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err //nolint:wrapcheck // test double
	}
	r.props = append(r.props, props)
	if r.deployed != nil {
		return r.deployed(ctx, env, it)
	}
	return nil
}

func (r *recorder) Deleting(
	ctx context.Context,
	env format.Env,
	_ repo.Config,
	it types.Item,
) error {
	r.note(ctx, env, "deleting "+it.FullPath())
	return nil
}

func (r *recorder) Deleted(
	ctx context.Context,
	env format.Env,
	_ repo.Config,
	gone []types.Item,
) error {
	r.note(ctx, env, fmt.Sprintf("deleted %d", len(gone)))
	return nil
}

func (r *recorder) Annotated(
	ctx context.Context,
	env format.Env,
	_ repo.Config,
	it types.Item,
	a format.Annotation,
) error {
	r.note(ctx, env, "annotated "+it.FullPath())
	r.annotations = append(r.annotations, annotation{it.FullPath(), a})
	return r.annotateErr
}

func (r *recorder) Resolve(
	ctx context.Context,
	env format.Env,
	_ repo.Config,
	d format.Download,
) (string, error) {
	r.note(ctx, env, "resolve "+d.Path)
	return r.resolve(d)
}

func (r *recorder) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	r.note(ctx, env, "reindex "+c.Key)
	return nil
}

func (w *world) props(p string) types.Properties {
	w.t.Helper()
	it, ok := w.get(w.loc, p)
	if !ok {
		w.t.Fatalf("%s is missing", p)
	}
	props, err := w.db.Items().Properties(context.Background(), it.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		w.t.Fatal(err)
	}
	return props
}

// A deploy inspects the stored content outside the transaction, then
// admits, stores and indexes the file inside it, with its properties set
// before Deployed and announced to Annotated.
func TestUnitDeployRunsTheHooks(t *testing.T) {
	ctx := context.Background()
	w, r := recording(t)
	r.inspection = "parsed"
	_, err := w.svc.Deploy(ctx, Deploy{
		Target: Target{Repo: w.loc, Path: "/a/b.txt"}, Body: strings.NewReader("body"),
		Properties: types.Properties{"k": {"v"}}, By: "alice",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"inspect a/b.txt", "admit 1 a/b.txt", "deployed a/b.txt", "annotated a/b.txt"}
	if !slices.Equal(r.order, want) {
		t.Fatalf("calls %v, want %v", r.order, want)
	}
	if r.calls["inspect a/b.txt"] || !r.calls["admit 1 a/b.txt"] ||
		!r.calls["deployed a/b.txt"] || !r.calls["annotated a/b.txt"] {
		t.Errorf("transactions %v", r.calls)
	}
	if got := r.writes[0]; got.Op != format.OpDeploy || got.Size != 4 ||
		got.Checksums != sums("body") || !got.Properties.Has("k", "v") {
		t.Errorf("admitted %+v", got)
	}
	if !slices.Equal(r.content, []string{"body"}) || r.inspections[0] != "parsed" {
		t.Errorf("inspection %v, %v", r.content, r.inspections)
	}
	if !r.props[0].Has("k", "v") || !r.annotations[0].a.Set.Has("k", "v") {
		t.Errorf("properties %v, annotation %+v", r.props, r.annotations)
	}

	// Without properties nothing is announced.
	r.reset()
	if _, err := w.deploy("c.txt", "c", types.Checksums{}); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(r.order, "annotated c.txt") {
		t.Errorf("calls %v", r.order)
	}
}

func TestUnitAdmitShapesTheDeploy(t *testing.T) {
	ctx := context.Background()
	w, r := recording(t)
	r.admit = func(wr format.Write) (format.Write, error) {
		wr.Path = "moved/" + path.Base(wr.Path)
		wr.Properties = types.Properties{"added": {"1"}}
		return wr, nil
	}
	it, err := w.deploy("a/b.txt", "body", types.Checksums{})
	if err != nil || it.FullPath() != "moved/b.txt" {
		t.Fatalf("deploy %+v, %v", it, err)
	}
	if _, ok := w.get(w.loc, "a/b.txt"); ok {
		t.Error("the requested path was written")
	}
	if !w.props("moved/b.txt").Has("added", "1") {
		t.Error("the admitted properties were not set")
	}

	r.admit = func(wr format.Write) (format.Write, error) {
		return wr, format.Rejectf(http.StatusConflict, "not here")
	}
	_, err = w.deploy("x.txt", "x", types.Checksums{})
	var refused *format.RejectionError
	if !errors.As(err, &refused) || refused.Status != http.StatusConflict ||
		refused.Error() != "not here" {
		t.Fatalf("refused deploy: %v", err)
	}
	if _, ok := w.get(w.loc, "x.txt"); ok {
		t.Error("a refused deploy was stored")
	}

	r.admit = func(wr format.Write) (format.Write, error) { wr.Path = "/"; return wr, nil }
	if _, err := w.svc.Deploy(ctx, Deploy{
		Target: Target{Repo: w.loc, Path: "y.txt"}, Body: strings.NewReader("y"),
	}); !errors.Is(err, types.ErrInvalidItemPath) {
		t.Fatalf("admitted to the root: %v", err)
	}
}

func TestUnitInspectFailureStopsTheDeploy(t *testing.T) {
	w, r := recording(t)
	r.inspectErr = errBoom
	if _, err := w.deploy("a.txt", "a", types.Checksums{}); !errors.Is(err, errBoom) {
		t.Fatalf("deploy: %v", err)
	}
	if _, ok := w.get(w.loc, "a.txt"); ok || len(r.order) != 1 {
		t.Errorf("stored %v after %v", ok, r.order)
	}
}

// Deploys by checksum, of committed uploads and of archive entries are
// inspected and admitted as uploads are; an atomic explode inspects every
// entry before its transaction.
func TestUnitOtherDeploysRunTheHooks(t *testing.T) {
	ctx := context.Background()
	w, r := recording(t)
	w.must("seed", "x")
	r.reset()
	if _, err := w.svc.DeployByChecksum(ctx, Deploy{
		Target: Target{Repo: w.loc, Path: "by-sum"}, Declared: sums("x"),
	}); err != nil {
		t.Fatal(err)
	}
	u, err := w.svc.Blobs.BeginUpload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.Append(ctx, strings.NewReader("committed")); err != nil {
		t.Fatal(err)
	}
	info, err := u.Commit(ctx, blob.Digests{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.DeployCommitted(ctx, Deploy{Target: Target{Repo: w.loc, Path: "late"}},
		info); err != nil {
		t.Fatal(err)
	}
	for _, call := range []string{"inspect by-sum", "deployed by-sum", "inspect late", "deployed late"} {
		if _, ok := r.calls[call]; !ok {
			t.Errorf("no %q in %v", call, r.order)
		}
	}

	for _, atomic := range []bool{true, false} {
		r.reset()
		zip := zipOf(t, entry{name: "one.txt", body: "1"}, entry{name: "two.txt", body: "2"})
		if _, err := w.explode(fmt.Sprintf("x%v/a.zip", atomic), zip, atomic); err != nil {
			t.Fatal(err)
		}
		inspected := slices.IndexFunc(r.order, func(c string) bool {
			return strings.HasPrefix(c, "inspect") && strings.HasSuffix(c, "two.txt")
		})
		admitted := slices.IndexFunc(r.order, func(c string) bool {
			return strings.HasPrefix(c, "admit")
		})
		if inspected < 0 || admitted < 0 || inspected > admitted ||
			len(r.annotations) != 2 {
			t.Errorf("atomic %v: calls %v", atomic, r.order)
		}
	}

	r.reset()
	r.inspectErr = errBoom
	zip := zipOf(t, entry{name: "one.txt", body: "1"})
	if _, err := w.explode("fails/a.zip", zip, true); !errors.Is(err, errBoom) {
		t.Fatalf("explode: %v", err)
	}
}

func TestUnitRecordChecksumIsAdmitted(t *testing.T) {
	ctx := context.Background()
	w, r := recording(t)
	w.must("f.txt", "content")
	w.must("g.txt", "other")
	r.reset()
	if _, err := w.svc.RecordChecksum(ctx, Target{Repo: w.loc, Path: "f.txt"}, SHA1,
		sums("content").SHA1); err != nil {
		t.Fatal(err)
	}
	if got := r.writes[0]; got.Op != format.OpChecksum || got.Path != "f.txt" ||
		!r.calls["admit 2 f.txt"] {
		t.Errorf("admitted %+v in %v", got, r.calls)
	}

	r.admit = func(wr format.Write) (format.Write, error) { wr.Path = "g.txt"; return wr, nil }
	if _, err := w.svc.RecordChecksum(ctx, Target{Repo: w.loc, Path: "f.txt"}, SHA1,
		sums("other").SHA1); err != nil {
		t.Fatal(err)
	}
	if g, _ := w.get(w.loc, "g.txt"); g.Original.SHA1 != sums("other").SHA1 {
		t.Errorf("the rewritten file's checksum: %+v", g.Original)
	}

	r.admit = func(wr format.Write) (format.Write, error) {
		return wr, format.Rejectf(http.StatusForbidden, "no")
	}
	var refused *format.RejectionError
	if _, err := w.svc.RecordChecksum(ctx, Target{Repo: w.loc, Path: "f.txt"}, SHA1,
		sums("content").SHA1); !errors.As(err, &refused) {
		t.Fatalf("refused checksum: %v", err)
	}
}

// A sidecar's declared length reaches Admit, a format may take the upload
// without recording it, and a refusal for the file carries Artifactory's
// message beside its sentinel.
func TestUnitRecordSidecarAnswers(t *testing.T) {
	ctx := context.Background()
	w, r := recording(t)
	w.must("f.txt", "content")
	if _, err := w.svc.MakeFolder(ctx, Target{Repo: w.loc, Path: "dir"}, nil, "a"); err != nil {
		t.Fatal(err)
	}
	if w.svc.OwnsSidecars(w.loc) {
		t.Fatal("a format without SidecarOwner owns its sidecars")
	}
	r.reset()
	if _, err := w.svc.RecordSidecar(ctx, Target{Repo: w.loc, Path: "f.txt"}, SHA1,
		sums("content").SHA1, 41); err != nil || r.writes[0].Size != 41 {
		t.Fatalf("size %+v %v", r.writes, err)
	}
	var side *SidecarError
	for _, tc := range []struct {
		path, value string
		sentinel    error
		msg         string
	}{
		{
			"none", sums("x").SHA1, store.ErrNotFound,
			"Target file to set checksum on doesn't exist: libs:none",
		},
		{
			"dir", sums("x").SHA1, ErrConflict,
			"Checksum only supported for files (but found folder): libs:dir",
		},
		{
			"f.txt", strings.ToUpper(sums("x").SHA1), ErrChecksumMismatch,
			"Checksum error for 'f.txt.sha1': received '" + strings.ToUpper(sums("x").SHA1) +
				"' but actual is '" + sums("content").SHA1 + "'",
		},
	} {
		_, err := w.svc.RecordSidecar(ctx, Target{Repo: w.loc, Path: tc.path}, SHA1, tc.value, 40)
		if !errors.As(err, &side) || !errors.Is(err, tc.sentinel) || err.Error() != tc.msg {
			t.Errorf("%s: %v", tc.path, err)
		}
	}
	r.admit = func(wr format.Write) (format.Write, error) {
		return wr, &format.AcceptedError{Status: http.StatusOK}
	}
	var accepted *format.AcceptedError
	if _, err := w.svc.RecordSidecar(ctx, Target{Repo: w.loc, Path: "none"}, SHA1, "junk",
		4); !errors.As(err, &accepted) || accepted.Status != http.StatusOK ||
		err.Error() != "format: accepted with status 200 and not stored" {
		t.Fatalf("accepted: %v", err)
	}
}

// A copy or move asks the target's format about each file while planning,
// lands it, sets its properties, and only then runs Deployed and announces
// them; a move also tells the source's format what left.
func TestUnitTransferRunsTheFormats(t *testing.T) {
	ctx := context.Background()
	w, r := recording(t)
	r.inspection = "parsed"
	if _, err := w.svc.Deploy(ctx, Deploy{
		Target: Target{Repo: w.loc, Path: "src/a.txt"}, Body: strings.NewReader("a"),
		Properties: types.Properties{"k": {"v"}},
	}); err != nil {
		t.Fatal(err)
	}
	src, _ := w.get(w.loc, "src/a.txt")

	r.reset()
	w.transfer(
		Transfer{From: Target{Repo: w.loc, Path: "src"}, To: Target{Repo: w.loc, Path: "dst"}},
	)
	if len(r.writes) != 1 {
		t.Fatalf("admitted %+v", r.writes)
	}
	if got := r.writes[0]; got.Op != format.OpCopy || got.Path != "dst/a.txt" ||
		got.From.ID != src.ID || !got.Properties.Has("k", "v") || got.Checksums != sums("a") {
		t.Errorf("copy admitted %+v", got)
	}
	if r.calls["inspect dst/a.txt"] || r.calls["admit 3 dst/a.txt"] ||
		!r.calls["deployed dst/a.txt"] || !r.calls["annotated dst/a.txt"] {
		t.Errorf("transactions %v", r.calls)
	}
	if !r.props[0].Has("k", "v") || r.inspections[0] != "parsed" ||
		!r.annotations[0].a.Set.Has("k", "v") {
		t.Errorf("deployed with %v, %v; annotated %+v", r.props, r.inspections, r.annotations)
	}

	r.reset()
	w.transfer(Transfer{
		From: Target{Repo: w.loc, Path: "dst/a.txt"}, To: Target{Repo: w.loc, Path: "moved.txt"},
		Move: true,
	})
	for _, call := range []string{"admit 4 moved.txt", "deployed moved.txt", "deleting dst/a.txt", "deleted 1"} {
		if _, ok := r.calls[call]; !ok {
			t.Errorf("no %q in %v", call, r.order)
		}
	}

	r.reset()
	r.admit = func(wr format.Write) (format.Write, error) {
		return wr, format.Rejectf(http.StatusConflict, "not here")
	}
	res := w.transfer(Transfer{
		From: Target{Repo: w.loc, Path: "src"}, To: Target{Repo: w.loc, Path: "refused"},
	})
	if res.Failed != 1 || !strings.Contains(fmt.Sprint(res.Messages), "not here") {
		t.Errorf("refused copy %+v", res)
	}
	if _, ok := w.get(w.loc, "refused/a.txt"); ok {
		t.Error("a refused file landed")
	}

	r.reset()
	r.admit = func(wr format.Write) (format.Write, error) {
		wr.Path = "elsewhere/" + path.Base(wr.Path)
		return wr, nil
	}
	w.transfer(
		Transfer{
			From: Target{Repo: w.loc, Path: "src/a.txt"},
			To:   Target{Repo: w.loc, Path: "c.txt"},
		},
	)
	if _, ok := w.get(w.loc, "elsewhere/c.txt"); !ok {
		t.Error("the admitted path was not used")
	}

	r.admit, r.inspectErr = nil, errBoom
	if _, err := w.svc.Transfer(ctx, Transfer{
		From: Target{Repo: w.loc, Path: "src/a.txt"}, To: Target{Repo: w.loc, Path: "d.txt"},
	}); !errors.Is(err, errBoom) {
		t.Fatalf("inspect failure: %v", err)
	}

	r.inspectErr = nil
	r.deployed = func(context.Context, format.Env, types.Item) error { return errBoom }
	if _, err := w.svc.Transfer(ctx, Transfer{
		From: Target{Repo: w.loc, Path: "src/a.txt"}, To: Target{Repo: w.loc, Path: "e.txt"},
	}); !errors.Is(err, errBoom) {
		t.Fatalf("deployed failure: %v", err)
	}
	if _, ok := w.get(w.loc, "e.txt"); ok {
		t.Error("a copy whose hook failed was kept")
	}
}

func TestUnitRestoreAndFoldersAnnotate(t *testing.T) {
	ctx := context.Background()
	w, r := recording(t)
	f := w.must("a/f.txt", "content")
	r.reset()
	if _, err := w.svc.Restore(ctx, Target{Repo: w.loc, Path: "b/r.txt"},
		types.TrashRecord{Item: f, Properties: types.Properties{"k": {"v"}}}, "bob"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.order, []string{"deployed b/r.txt", "annotated b/r.txt"}) ||
		r.inspections[0] != nil {
		t.Errorf("restore calls %v, inspection %v", r.order, r.inspections)
	}
	folder := types.Item{Folder: true, Created: at}
	r.reset()
	if _, err := w.svc.Restore(
		ctx,
		Target{Repo: w.loc, Path: "a"},
		types.TrashRecord{
			Item:       folder,
			Properties: types.Properties{"f": {"x"}},
		},
		"bob",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Restore(
		ctx,
		Target{Repo: w.loc, Path: "new"},
		types.TrashRecord{
			Item:       folder,
			Properties: types.Properties{"g": {"y"}},
		},
		"bob",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.MakeFolder(ctx, Target{Repo: w.loc, Path: "made"},
		types.Properties{"h": {"z"}}, "bob"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.MakeFolder(ctx, Target{Repo: w.loc, Path: "bare"}, nil, "bob"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.order, []string{"annotated a", "annotated new", "annotated made"}) {
		t.Errorf("folder calls %v", r.order)
	}

	r.reset()
	r.annotateErr = errBoom
	if _, err := w.svc.Restore(
		ctx,
		Target{Repo: w.loc, Path: "c/r.txt"},
		types.TrashRecord{
			Item:       f,
			Properties: types.Properties{"k": {"v"}},
		},
		"bob",
	); !errors.Is(err, errBoom) {
		t.Fatalf("restore: %v", err)
	}
	if _, err := w.svc.Restore(
		ctx,
		Target{Repo: w.loc, Path: "a"},
		types.TrashRecord{
			Item:       folder,
			Properties: types.Properties{"f": {"x"}},
		},
		"bob",
	); !errors.Is(err, errBoom) {
		t.Fatalf("folder restore: %v", err)
	}
	r.annotateErr = nil
	r.deployed = func(context.Context, format.Env, types.Item) error { return errBoom }
	if _, err := w.svc.Restore(ctx, Target{Repo: w.loc, Path: "d/r.txt"},
		types.TrashRecord{Item: f}, "bob"); !errors.Is(err, errBoom) {
		t.Fatalf("restore: %v", err)
	}
}

func TestUnitAnnotate(t *testing.T) {
	ctx := context.Background()
	w, r := recording(t)
	if _, err := w.svc.Deploy(ctx, Deploy{
		Target: Target{Repo: w.loc, Path: "p.txt"}, Body: strings.NewReader("p"),
		Properties: types.Properties{"old": {"1"}},
	}); err != nil {
		t.Fatal(err)
	}
	it, _ := w.get(w.loc, "p.txt")
	r.reset()
	if err := w.svc.Annotate(
		ctx,
		w.loc,
		it,
		types.Properties{"k": {"v"}},
		[]string{"old"},
	); err != nil {
		t.Fatal(err)
	}
	if got := w.props("p.txt"); !got.Has("k", "v") || got.Has("old", "1") {
		t.Errorf("properties %v", got)
	}
	if got := r.annotations[0].a; !got.Set.Has("k", "v") ||
		!slices.Equal(got.Removed, []string{"old"}) ||
		!r.calls["annotated p.txt"] {
		t.Errorf("annotation %+v", got)
	}

	// The hook's failure undoes the change it was told about.
	r.annotateErr = errBoom
	if err := w.svc.Annotate(
		ctx,
		w.loc,
		it,
		types.Properties{"k": {"w"}},
		nil,
	); !errors.Is(
		err,
		errBoom,
	) {
		t.Fatalf("annotate: %v", err)
	}
	if got := w.props("p.txt"); !got.Has("k", "v") || got.Has("k", "w") {
		t.Errorf("properties after a failed hook %v", got)
	}

	// A format that follows no properties still gets them changed.
	w.svc.Formats = nil
	if err := w.svc.Annotate(ctx, w.loc, it, types.Properties{"k": {"x"}}, nil); err != nil {
		t.Fatal(err)
	}
	if !w.props("p.txt").Has("k", "x") {
		t.Error("the generic format's item kept its properties")
	}
	w.svc.Store = faulty{w.db, "patch"}
	if err := w.svc.Annotate(
		ctx,
		w.loc,
		it,
		types.Properties{"k": {"y"}},
		nil,
	); !errors.Is(
		err,
		errBoom,
	) {
		t.Fatalf("patch failure: %v", err)
	}
}

func TestUnitResolveAndReindex(t *testing.T) {
	ctx := context.Background()
	w, r := recording(t)
	r.resolve = func(d format.Download) (string, error) {
		if d.Path == "bad" {
			return "", format.Rejectf(http.StatusConflict, "unresolvable")
		}
		return fmt.Sprintf("/real/%s/%v", d.Path, d.Cached), nil
	}
	if got, err := w.svc.Resolve(
		ctx,
		w.loc,
		format.Download{Path: "x", Cached: true},
	); err != nil ||
		got != "real/x/true" {
		t.Fatalf("resolve %q, %v", got, err)
	}
	if _, err := w.svc.Resolve(ctx, w.loc, format.Download{Path: "bad"}); err == nil {
		t.Fatal("an unresolvable path resolved")
	}
	// A reindex manages its own transactions.
	if err := w.svc.Reindex(ctx, w.loc); err != nil || r.calls["reindex libs"] ||
		!slices.Contains(r.order, "reindex libs") {
		t.Fatalf("reindex: %v, %v", err, r.calls)
	}

	w.svc.Formats = nil
	if got, err := w.svc.Resolve(
		ctx,
		w.loc,
		format.Download{Path: "as/is"},
	); err != nil ||
		got != "as/is" {
		t.Fatalf("generic resolve %q, %v", got, err)
	}
	if err := w.svc.Reindex(ctx, w.loc); !errors.Is(err, ErrNoReindex) {
		t.Fatalf("generic reindex: %v", err)
	}
}

// A format's derived item lands in the write's transaction without
// re-entering the hooks, and goes with the write when it rolls back.
func TestUnitDeriveWritesWithoutHooks(t *testing.T) {
	ctx := context.Background()
	w, r := recording(t)
	fail := false
	r.deployed = func(ctx context.Context, env format.Env, it types.Item) error {
		if _, err := env.Derive(ctx, format.Derived{
			Repo: w.loc, Path: "a/meta.xml", Body: strings.NewReader("meta " + it.Name),
			Properties: types.Properties{"derived": {"yes"}}, By: "format",
		}); err != nil {
			return err //nolint:wrapcheck // test double
		}
		if fail {
			return errBoom
		}
		return nil
	}
	if _, err := w.deploy("a/x.jar", "x", types.Checksums{}); err != nil {
		t.Fatal(err)
	}
	meta, ok := w.get(w.loc, "a/meta.xml")
	if !ok || meta.Actual != sums("meta x.jar") || meta.CreatedBy != "format" ||
		!w.props("a/meta.xml").Has("derived", "yes") {
		t.Fatalf("derived %+v", meta)
	}
	if !slices.Equal(r.order, []string{"inspect a/x.jar", "admit 1 a/x.jar", "deployed a/x.jar"}) {
		t.Errorf("hooks ran for the derived item: %v", r.order)
	}

	fail = true
	if _, err := w.deploy("a/y.jar", "y", types.Checksums{}); !errors.Is(err, errBoom) {
		t.Fatalf("deploy: %v", err)
	}
	if meta, _ := w.get(w.loc, "a/meta.xml"); meta.Actual != sums("meta x.jar") {
		t.Error("the rolled-back deploy's derived item stayed")
	}

	// A folder in the way of a derived item, and content that cannot be
	// stored, fail the derive.
	derive := func(svc *Service, p string, body io.Reader) error {
		_, err := svc.derive(ctx, format.Derived{Repo: w.loc, Path: p, Body: body})
		return err
	}
	if err := derive(w.svc, "a", strings.NewReader("m")); !errors.Is(err, ErrConflict) {
		t.Errorf("a folder in the way: %v", err)
	}
	if err := derive(w.svc, "z", iotest.ErrReader(errBoom)); !errors.Is(err, errBoom) {
		t.Errorf("unreadable content: %v", err)
	}
	for _, fail := range []string{"begin", "commit"} {
		svc := *w.svc
		svc.Blobs = faultyBlobStore{w.svc.Blobs, fail}
		if err := derive(&svc, "z", strings.NewReader("m")); !errors.Is(err, errBoom) {
			t.Errorf("%s: %v", fail, err)
		}
	}
}

// failingQueue fails the work queue's enqueue.
type failingQueue struct{ *inmem.Store }

func (f failingQueue) WorkQueue() store.WorkQueue { return failingWork{f.Store.WorkQueue()} }

type failingWork struct{ store.WorkQueue }

func (failingWork) Enqueue(context.Context, string, string, []byte, time.Time) error {
	return errBoom
}

// queued records what was enqueued through it.
type queued struct{ keys []string }

func (q *queued) Enqueue(_ context.Context, kind, key string, _ []byte) error {
	q.keys = append(q.keys, kind+"/"+key)
	return nil
}

// Work a hook enqueues is there exactly when the write commits.
func TestUnitEnqueueJoinsTheWrite(t *testing.T) {
	ctx := context.Background()
	w, r := recording(t)
	fail := false
	r.deployed = func(ctx context.Context, env format.Env, it types.Item) error {
		if err := env.Enqueue(ctx, "test.kind", it.Name, []byte(`{"a":1}`)); err != nil {
			return err //nolint:wrapcheck // test double
		}
		if fail {
			return errBoom
		}
		return nil
	}
	if _, err := w.deploy("kept", "k", types.Checksums{}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.db.WorkQueue().Get(ctx, "test.kind", "kept"); err != nil {
		t.Fatalf("committed work: %v", err)
	}
	fail = true
	if _, err := w.deploy("dropped", "d", types.Checksums{}); !errors.Is(err, errBoom) {
		t.Fatalf("deploy: %v", err)
	}
	if _, err := w.db.WorkQueue().
		Get(ctx, "test.kind", "dropped"); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatalf("rolled-back work: %v", err)
	}

	fail = false
	q := &queued{}
	w.svc.Work = q
	if _, err := w.deploy("woken", "w", types.Checksums{}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(q.keys, []string{"test.kind/woken"}) {
		t.Errorf("queue %v", q.keys)
	}

	w.svc.Work, w.svc.Store = nil, failingQueue{w.db}
	if _, err := w.deploy("refused", "r", types.Checksums{}); !errors.Is(err, errBoom) {
		t.Fatalf("enqueue failure: %v", err)
	}
}

func TestUnitEnvironment(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	doc, _ := json.Marshal(map[string]string{"artifactPathPattern": "[module]-[baseRev].[ext]"})
	if err := w.db.Configs().Put(ctx, types.ConfigDocument{
		Kind: types.ConfigLayout, Key: "flat", Document: doc,
	}); err != nil {
		t.Fatal(err)
	}
	env := w.svc.env()
	first, err := env.Layouts.Layout(ctx, "flat")
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := env.Layouts.Layout(ctx, "flat"); again != first {
		t.Error("an operation compiled one layout twice")
	}
	if fresh, _ := w.svc.env().Layouts.Layout(ctx, "flat"); fresh == first {
		t.Error("a layout outlived its operation")
	}
	if _, err := env.Layouts.Layout(ctx, "absent"); err == nil {
		t.Error("an unknown layout resolved")
	}
	if env.Now() != at || env.Blobs == nil {
		t.Errorf("env %+v", env)
	}
	if (&Service{Store: w.db}).env().Blobs != nil {
		t.Error("a service without blobs gave its hooks a blob reader")
	}
}

// bare is a format with the required hooks alone.
type bare struct{}

func (bare) Deployed(context.Context, format.Env, repo.Config, types.Item) error  { return nil }
func (bare) Deleting(context.Context, format.Env, repo.Config, types.Item) error  { return nil }
func (bare) Deleted(context.Context, format.Env, repo.Config, []types.Item) error { return nil }

func TestUnitOptionalInterfaces(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.svc.Formats = func(repo.PackageType) format.Handler { return bare{} }
	if _, err := w.svc.Deploy(ctx, Deploy{
		Target: Target{Repo: w.loc, Path: "a.txt"}, Body: strings.NewReader("a"),
		Properties: types.Properties{"k": {"v"}},
	}); err != nil {
		t.Fatalf("a format without optional interfaces: %v", err)
	}

	// An Annotated failure rolls the deploy that set the properties back.
	w, r := recording(t)
	r.annotateErr = errBoom
	if _, err := w.svc.Deploy(ctx, Deploy{
		Target: Target{Repo: w.loc, Path: "b.txt"}, Body: strings.NewReader("b"),
		Properties: types.Properties{"k": {"v"}},
	}); !errors.Is(err, errBoom) {
		t.Fatalf("deploy: %v", err)
	}
	if _, ok := w.get(w.loc, "b.txt"); ok {
		t.Error("the deploy stayed")
	}
}

// A protocol can resolve the canonical path from inspection without reading
// the archive again; a refusal happens before admission or any store mutation.
func TestUnitResolvedDeploy(t *testing.T) {
	for _, refuse := range []bool{false, true} {
		t.Run(fmt.Sprint(refuse), func(t *testing.T) {
			w, r := recording(t)
			r.inspection = "metadata"
			d := Deploy{
				Target: Target{Repo: w.loc, Path: "archive.zip"},
				Body:   strings.NewReader("archive"),
				ResolvePath: func(got any) (string, error) {
					if got != "metadata" || len(r.order) != 1 ||
						r.order[0] != "inspect archive.zip" {
						t.Fatalf("resolve before single inspection: %v, %v", got, r.order)
					}
					if refuse {
						return "", errBoom
					}
					return "canonical/archive.zip", nil
				},
			}
			it, err := w.svc.Deploy(t.Context(), d)
			if refuse {
				if !errors.Is(err, errBoom) || len(r.order) != 1 {
					t.Fatalf("refused: %v, calls %v", err, r.order)
				}
				if _, ok := w.get(w.loc, "canonical/archive.zip"); ok {
					t.Fatal("refused item stored")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if it.FullPath() != "canonical/archive.zip" || len(r.inspections) != 1 ||
				r.inspections[0] != "metadata" {
				t.Fatalf("resolved write: %+v, inspections %v", it, r.inspections)
			}
		})
	}
}

package item

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// seeded deploys body at p with props and one download recorded.
func (w *world) seeded(p, body string, props types.Properties) types.Item {
	w.t.Helper()
	ctx := context.Background()
	it, err := w.svc.Deploy(ctx, Deploy{
		Target: Target{Repo: w.loc, Path: p}, Body: strings.NewReader(body), Properties: props,
	})
	if err != nil {
		w.t.Fatal(err)
	}
	if err := w.db.Items().RecordDownload(ctx, it.ID, store.Download{By: "x", At: at}); err != nil {
		w.t.Fatal(err)
	}
	return it
}

func (w *world) downloads(p string) int64 {
	w.t.Helper()
	it, _ := w.get(w.loc, p)
	st, err := w.db.Items().Stats(context.Background(), it.ID)
	if err != nil {
		w.t.Fatal(err)
	}
	return st.DownloadCount
}

// A deploy over a file of other content is a new file at the old path, as
// 6.20's clearOldFileData makes it: the old file's properties and download
// statistics go. The same content keeps both, and a deploy's own
// properties replace the file's either way, as 6.20's saveProperties sets
// them. A Maven non-unique snapshot keeps its metadata across builds.
func TestUnitOverwriteClearsTheFilesMetadata(t *testing.T) {
	ctx := context.Background()
	old := types.Properties{"build.name": {"b"}, "npm.deprecated": {"old"}}
	for _, c := range []struct {
		name, path, body string
		props            types.Properties
		expected         types.Properties
		downloads        int64
	}{
		{"other content", "a/b.txt", "two", nil, types.Properties{}, 0},
		{
			"other content with its own", "a/b.txt", "two",
			types.Properties{"k": {"v"}},
			types.Properties{"k": {"v"}},
			0,
		},
		{"same content", "a/b.txt", "one", nil, old, 1},
		{
			"same content with its own", "a/b.txt", "one",
			types.Properties{"k": {"v"}},
			types.Properties{"k": {"v"}},
			1,
		},
		{"non-unique snapshot", "g/a/1.0-SNAPSHOT/a-1.0-SNAPSHOT.jar", "two", nil, old, 1},
		{
			"unique snapshot", "g/a/1.0-SNAPSHOT/a-1.0-20261019.120000-1.jar", "two", nil,
			types.Properties{},
			0,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			before := w.seeded(c.path, "one", old)
			it, err := w.svc.Deploy(ctx, Deploy{
				Target: Target{Repo: w.loc, Path: c.path}, Body: strings.NewReader(c.body),
				Properties: c.props,
			})
			if err != nil {
				t.Fatal(err)
			}
			if it.ID != before.ID || !it.Created.Equal(before.Created) {
				t.Errorf("the overwrite made a new item: %+v", it)
			}
			if diff := cmp.Diff(c.expected, w.props(c.path), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("properties (-want +got):\n%s", diff)
			}
			if got := w.downloads(c.path); got != c.downloads {
				t.Errorf("downloads %d, want %d", got, c.downloads)
			}
		})
	}
}

// Properties a format adds in Admit join the file's when the client sent
// none, as 6.20's interceptors add theirs after the deploy's own are set;
// over other content there is nothing left for them to join.
func TestUnitOverwriteKeepsWhatTheFormatAdds(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		body     string
		expected types.Properties
	}{
		{"one", types.Properties{"k": {"v"}, "plugin": {"true"}}},
		{"two", types.Properties{"plugin": {"true"}}},
	} {
		w, r := recording(t)
		w.seeded("a.pom", "one", types.Properties{"k": {"v"}})
		r.admit = func(fw format.Write) (format.Write, error) {
			fw.Properties = types.Properties{"plugin": {"true"}}
			return fw, nil
		}
		if _, err := w.svc.Deploy(ctx, Deploy{
			Target: Target{Repo: w.loc, Path: "a.pom"}, Body: strings.NewReader(c.body),
		}); err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(c.expected, w.props("a.pom"), cmpopts.EquateEmpty()); diff != "" {
			t.Errorf("%s: properties (-want +got):\n%s", c.body, diff)
		}
	}
}

// A file a copy or move lands on is replaced whole, as 6.20's mover deletes
// it first: the target has the source's properties and none of its own,
// and the download statistics of neither a copy's target nor the file it
// replaced; a move brings the source's.
func TestUnitTransferOntoAFileReplacesItsMetadata(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name      string
		move      bool
		srcProps  types.Properties
		expected  types.Properties
		downloads int64
	}{
		{"copy", false, types.Properties{"k": {"v"}}, types.Properties{"k": {"v"}}, 0},
		{"copy of a bare file", false, nil, types.Properties{}, 0},
		{"move", true, types.Properties{"k": {"v"}}, types.Properties{"k": {"v"}}, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			w.seeded("src.txt", "new", c.srcProps)
			w.seeded("dst.txt", "old", types.Properties{"stale": {"yes"}})
			dst, _ := w.get(w.loc, "dst.txt")
			if err := w.db.Items().RecordDownload(ctx, dst.ID, store.Download{At: at}); err != nil {
				t.Fatal(err)
			}
			res := w.transfer(Transfer{
				From: Target{
					Repo: w.loc,
					Path: "src.txt",
				},
				To:           Target{Repo: w.loc, Path: "dst.txt"},
				Move:         c.move,
				CanOverwrite: true,
			})
			if res.Files != 1 || res.Failed != 0 {
				t.Fatalf("%+v", res)
			}
			if diff := cmp.Diff(c.expected, w.props("dst.txt"), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("properties (-want +got):\n%s", diff)
			}
			if got := w.downloads("dst.txt"); got != c.downloads {
				t.Errorf("downloads %d, want %d", got, c.downloads)
			}
		})
	}
}

// Replacing a file's properties or statistics on a copy fails the copy,
// which writes nothing.
func TestUnitTransferOntoAFileStoreFailures(t *testing.T) {
	ctx := context.Background()
	for _, fail := range []string{"set", "putStats"} {
		w := newWorld(t)
		w.must("src.txt", "new")
		w.must("dst.txt", "old")
		w.svc.Store = faulty{w.db, fail}
		_, err := w.svc.Transfer(ctx, Transfer{
			From: Target{Repo: w.loc, Path: "src.txt"}, To: Target{Repo: w.loc, Path: "dst.txt"},
			CanOverwrite: true,
		})
		if !errors.Is(err, errBoom) {
			t.Errorf("%s: %v", fail, err)
		}
		if got, _ := w.get(w.loc, "dst.txt"); got.Actual != sums("old") {
			t.Errorf("%s: the target changed", fail)
		}
	}
}

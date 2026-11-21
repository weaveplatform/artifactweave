package transfer

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/fs"
)

var errFixture = errors.New("source fixture failure")

type sourceFixture struct {
	db       *inmem.Store
	source   Source
	config   repo.Config
	identity api.TransferRepository
	decider  *accessapitest.Decider
	checks   int
	guarded  bool
	items    *item.Service
}

func newFixture(t *testing.T) *sourceFixture {
	t.Helper()
	db := inmem.New()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &sourceFixture{db: db, config: *repo.Defaults(&repo.Config{Key: "source", Class: repo.Local, PackageType: repo.Generic})}
	if err := db.Repos().Create(t.Context(), f.config); err != nil {
		t.Fatal(err)
	}
	f.decider = &accessapitest.Decider{
		AllowsFolderFunc: func(accessapi.ResourceType, accessapi.Repo, string, accessapi.Actions) bool { return true },
		AllowsFunc: func(_ accessapi.ResourceType, resource accessapi.Repo, path string, want accessapi.Actions) bool {
			return resource.Key == "source" && resource.Class == "local" && want == accessapi.ActionRead && path != "z/unreadable.pkg"
		},
	}
	f.source = Source{
		Store: db, Blobs: blobs,
		Guard: func(ctx context.Context) error {
			if !db.InTx(ctx) {
				t.Fatal("source authority guard escaped its transaction")
			}
			f.guarded = true
			return nil
		},
		Current: func(ctx context.Context) (accessapi.Decider, error) {
			if !db.InTx(ctx) || !f.guarded {
				t.Fatal("source authority was read before its transaction guard")
			}
			f.checks++
			f.guarded = false
			return f.decider, nil
		},
	}
	f.items = &item.Service{Store: db, Blobs: blobs}
	f.identity, err = f.source.Repository(t.Context(), f.config.Key)
	if err != nil || f.identity.Incarnation == "" || f.identity.Generation == "" {
		t.Fatal("missing durable repository identity", f.identity, err)
	}
	return f
}

func (f *sourceFixture) put(t *testing.T, path, content string) model.Item {
	t.Helper()
	it, err := f.items.Deploy(t.Context(), item.Deploy{Target: item.Target{Repo: f.config, Path: path}, Body: strings.NewReader(content), By: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	return it
}

func (f *sourceFixture) one(t *testing.T, path string) api.TransferItem {
	t.Helper()
	rows, next, err := f.source.Items(t.Context(), f.identity, api.TransferSelection{Root: path, Properties: true, Statistics: true}, kernel.Page{Limit: 1})
	if err != nil || len(rows) != 1 || next != "" {
		t.Fatal("exact source snapshot", rows, next, err)
	}
	return rows[0]
}

func TestUnitSourcePagesRespectExactSelectionAndCurrentRead(t *testing.T) {
	f := newFixture(t)
	for _, path := range []string{"a/one.pkg", "a/excluded.tmp", "a/nested/two.pkg", "a2/other.pkg", "z/unreadable.pkg"} {
		f.put(t, path, path)
	}
	query := api.TransferSelection{Root: "a", Include: "**/*.pkg", Exclude: "**/*.tmp"}
	page := kernel.Page{Limit: 1}
	var got []string
	var emptyContinuation bool
	for steps := 0; ; steps++ {
		if steps > 10 {
			t.Fatal("source continuation did not terminate")
		}
		rows, next, err := f.source.Items(t.Context(), f.identity, query, page)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) > 1 {
			t.Fatal("source exceeded its page bound")
		}
		for _, row := range rows {
			got = append(got, row.Path)
			if row.PropertiesIncluded || row.Properties != nil || row.Statistics != nil {
				t.Fatal("unrequested metadata was disclosed", row)
			}
		}
		emptyContinuation = emptyContinuation || len(rows) == 0 && next != ""
		if next == "" {
			break
		}
		page.After = next
	}
	if !slices.Equal(got, []string{"a/nested/two.pkg", "a/one.pkg"}) || !emptyContinuation {
		t.Fatal("source scope or empty-page continuation lost", got, emptyContinuation)
	}
	rows, next, err := f.source.Items(t.Context(), f.identity, api.TransferSelection{}, kernel.Page{Limit: 100})
	if !errors.Is(err, kernel.ErrForbidden) || rows != nil || next != "" {
		t.Fatal("denied selected path leaked a partial page", rows, next, err)
	}
	if f.checks < 4 {
		t.Fatal("source reused authority across pages", f.checks)
	}
}

func TestUnitSourceCurrentSnapshotFencesEveryRepresentedChange(t *testing.T) {
	for _, change := range []string{"overwrite", "properties", "statistics", "replacement", "repository lifetime", "credential"} {
		t.Run(change, func(t *testing.T) {
			f := newFixture(t)
			it := f.put(t, "a/one.pkg", "original")
			at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
			if err := f.db.Items().SetProperties(t.Context(), it.ID, model.Properties{"quality": {"original"}}, at); err != nil {
				t.Fatal(err)
			}
			expected := f.one(t, it.FullPath())
			switch change {
			case "overwrite":
				f.put(t, it.FullPath(), "changed")
			case "properties":
				// Keep the same timestamp: the actual property snapshot must
				// fence the change even when timestamp precision cannot.
				if err := f.db.Items().SetProperties(t.Context(), it.ID, model.Properties{"quality": {"changed"}}, at); err != nil {
					t.Fatal(err)
				}
			case "statistics":
				if err := f.db.Items().PutStats(t.Context(), it.ID, model.ItemStats{DownloadCount: 4, LastDownloaded: at, LastDownloadedBy: "reader"}); err != nil {
					t.Fatal(err)
				}
			case "replacement":
				if _, err := f.db.Items().Delete(t.Context(), f.config.Key, false, it.FullPath()); err != nil {
					t.Fatal(err)
				}
				f.put(t, it.FullPath(), "original")
			case "repository lifetime":
				if err := f.db.Repos().Delete(t.Context(), f.config.Key); err != nil {
					t.Fatal(err)
				}
				if err := f.db.Repos().Create(t.Context(), f.config); err != nil {
					t.Fatal(err)
				}
			case "credential":
				f.source.Current = func(context.Context) (accessapi.Decider, error) { return nil, kernel.ErrBadCredentials }
			}
			called := false
			err := f.source.WithCurrent(t.Context(), expected, func(context.Context) error { called = true; return nil })
			want := kernel.ErrConflict
			if change == "credential" {
				want = kernel.ErrBadCredentials
			}
			if !errors.Is(err, want) || called {
				t.Fatal("obsolete source received a success checkpoint", change, called, err)
			}
		})
	}
}

func TestUnitSourceCheckpointJoinsAndRollsBackWithCurrentSnapshot(t *testing.T) {
	f := newFixture(t)
	it := f.put(t, "one.pkg", "content")
	expected := f.one(t, it.FullPath())
	data, err := json.Marshal(expected)
	if err != nil {
		t.Fatal(err)
	}
	var restored api.TransferItem
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	err = f.source.WithCurrent(t.Context(), restored, func(ctx context.Context) error {
		if !f.db.InTx(ctx) {
			t.Fatal("checkpoint escaped source transaction")
		}
		if err := f.db.Settings().Put(ctx, "test.source.checkpoint", true); err != nil {
			return errors.Join(errFixture, err)
		}
		return errFixture
	})
	if !errors.Is(err, errFixture) {
		t.Fatal(err)
	}
	var value bool
	if err := f.db.Settings().Get(t.Context(), "test.source.checkpoint", &value); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("failed source settlement committed checkpoint", value, err)
	}
	if err := f.source.WithCurrent(t.Context(), restored, nil); err != nil {
		t.Fatal("persisted source snapshot no longer compares canonically", err)
	}
	changed := restored
	changed.Size++
	if err := f.source.WithCurrent(t.Context(), changed, nil); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatal("mutated descriptive snapshot was admitted", err)
	}
}

type observedBlobs struct {
	blob.Store
	t      *testing.T
	db     *inmem.Store
	opened int
}

func (b *observedBlobs) Open(ctx context.Context, digest string) (io.ReadSeekCloser, blob.Info, error) {
	if b.db.InTx(ctx) {
		b.t.Fatal("blob I/O inherited a metadata transaction")
	}
	b.opened++
	return b.Store.Open(ctx, digest)
}

func TestUnitSourceOpenOwnsImmutableBodyWithoutMetadataTransaction(t *testing.T) {
	f := newFixture(t)
	it := f.put(t, "one.pkg", "original")
	expected := f.one(t, it.FullPath())
	blobs := &observedBlobs{Store: f.source.Blobs, t: t, db: f.db}
	f.source.Blobs = blobs
	if err := f.db.Tx(t.Context(), func(ctx context.Context) error {
		body, err := f.source.Open(ctx, expected)
		if !errors.Is(err, kernel.ErrInvalid) || body != nil {
			t.Fatal("transactional content open was admitted", body, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if blobs.opened != 0 {
		t.Fatal("transactional refusal touched blob backend")
	}
	body, err := f.source.Open(t.Context(), expected)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	f.put(t, it.FullPath(), "replacement")
	content, err := io.ReadAll(body)
	if err != nil || string(content) != "original" {
		t.Fatal("opened immutable version changed", string(content), err)
	}
	if err := f.source.WithCurrent(t.Context(), expected, nil); !errors.Is(err, kernel.ErrConflict) {
		t.Fatal("overwritten transfer could be settled as current", err)
	}
}

func TestUnitSourceAbsenceRequiresCurrentExactReadAndLifetime(t *testing.T) {
	f := newFixture(t)
	f.put(t, "exists.pkg", "exists")
	for _, tc := range []struct {
		name, path string
		want       error
	}{
		{name: "missing selected file", path: "missing.pkg"},
		{name: "existing file", path: "exists.pkg", want: kernel.ErrConflict},
		{name: "unreadable file is not absence", path: "z/unreadable.pkg", want: kernel.ErrForbidden},
		{name: "repository root", path: "", want: kernel.ErrInvalid},
		{name: "noncanonical file", path: "a/../outside.pkg", want: kernel.ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			err := f.source.WithAbsent(t.Context(), f.identity, tc.path, func(ctx context.Context) error {
				called = true
				if !f.db.InTx(ctx) {
					t.Fatal("absence settlement escaped transaction")
				}
				return nil
			})
			if !errors.Is(err, tc.want) || called != (tc.want == nil) {
				t.Fatal("invalid absence admission", called, err)
			}
		})
	}
	f.decider.AllowsFolderFunc = func(accessapi.ResourceType, accessapi.Repo, string, accessapi.Actions) bool { return false }
	if identity, err := f.source.Repository(t.Context(), f.config.Key); !errors.Is(err, kernel.ErrForbidden) || identity.Key != "" {
		t.Fatal("management visibility manufactured content read", identity, err)
	}
}

func TestUnitSourcePermissionUsesTheActualRepositoryClass(t *testing.T) {
	f := newFixture(t)
	f.config.Class = repo.Remote
	f.config.URL = "https://upstream.invalid/repository"
	if err := f.db.Repos().Update(t.Context(), f.config); err != nil {
		t.Fatal(err)
	}
	f.decider.AllowsFolderFunc = func(_ accessapi.ResourceType, resource accessapi.Repo, _ string, _ accessapi.Actions) bool {
		return resource.Class == string(repo.Local)
	}
	if value, err := f.source.Repository(t.Context(), f.config.Key); !errors.Is(err, kernel.ErrForbidden) || value.Key != "" {
		t.Fatal("local-only permission admitted a remote source", value, err)
	}
}

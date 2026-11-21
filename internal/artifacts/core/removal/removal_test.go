package removal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/pkgindex"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/fs"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

var removalAt = time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)

type removalFixture struct {
	db           *inmem.Store
	items        *item.Service
	repo         repo.Config
	request      api.RemovalRequest
	decider      *accessapitest.Decider
	origin       string
	service      api.ItemRemoval
	currentError error
}

func newRemovalFixture(t *testing.T) *removalFixture {
	t.Helper()
	ctx, db := t.Context(), inmem.New()
	blobs := storekit.Get[*fs.Store](t)(fs.New(t.TempDir()))
	c := *repo.Defaults(&repo.Config{Key: "packages", Class: repo.Local, PackageType: repo.Generic})
	storekit.Must(t, db.Repos().Create(ctx, c))
	items := &item.Service{Store: db, Blobs: blobs, Now: func() time.Time { return removalAt }}
	it := storekit.Get[model.Item](t)(items.Deploy(ctx, item.Deploy{
		Target: item.Target{Repo: c, Path: "one.bin"}, Body: strings.NewReader("content"),
		Properties: model.Properties{"quality": {"eligible"}}, By: "uploader",
	}))
	storekit.Must(t, pkgindex.Link(ctx, db, pkgindex.Version{RepoKey: c.Key, PackageType: c.PackageType, Package: "package", Version: "1"}, it.ID, removalAt))
	r := storekit.Get[api.IndexRepository](t)(db.Repos().(port.RepositoryIndexPages).IndexRepository(ctx, c.Key))
	p := storekit.Get[model.Package](t)(db.Packages().Find(ctx, c.Key, "package"))
	v := storekit.Get[model.PackageVersion](t)(db.Packages().Version(ctx, p.ID, "1"))
	ref := api.IndexVersion{Package: api.IndexPackage{Repository: r, ID: p.ID, Name: p.Name}, ID: v.ID, Version: v.Version}
	rows, _, err := pkgindex.NewReader(db, nil).Files(ctx, ref, map[string][]string{"quality": {"eligible"}, "absent": {"value"}}, kernel.Page{Limit: 1})
	storekit.Must(t, err)
	if len(rows) != 1 {
		t.Fatal("selected file fixture missing")
	}
	w := &removalFixture{
		db:    db,
		items: items,
		repo:  c,
		request: api.RemovalRequest{
			Credential: kernel.CredentialReference{Name: "caller", TokenID: "original-token"},
			Item:       rows[0],
			Properties: map[string][]string{"quality": {"eligible"}, "absent": {"value"}},
		},
		decider: &accessapitest.Decider{AllowsFunc: func(rt accessapi.ResourceType, r accessapi.Repo, _ string, want accessapi.Actions) bool {
			return rt == accessapi.ResourceArtifact && r.Key == c.Key && r.Class == string(c.Class) && want == accessapi.ActionDelete
		}},
	}
	w.service = New(db, items, func(ctx context.Context, ref kernel.CredentialReference) (kernel.Principal, accessapi.Decider, error) {
		if w.currentError != nil {
			return kernel.Principal{}, nil, w.currentError
		}
		if !db.InTx(ctx) || ref != w.request.Credential {
			return kernel.Principal{}, nil, kernel.ErrBadCredentials
		}
		return ref.Principal(), w.decider, nil
	})
	w.origin = storekit.Get[string](t)(instance.EnsureID(ctx, db))
	_, err = db.Events().RegisterConsumer(ctx, w.origin, "removal-proof")
	storekit.Must(t, err)
	return w
}

func (w *removalFixture) revalidate(ctx context.Context) error {
	if !w.db.InTx(ctx) {
		return kernel.ErrStore
	}
	p, err := kernel.RequireAuthenticated(ctx)
	if err != nil || p.Name != w.request.Credential.Name {
		return kernel.ErrBadCredentials
	}
	return nil
}

func (w *removalFixture) record(ctx context.Context, result api.RemovalResult) error {
	if !w.db.InTx(ctx) {
		return kernel.ErrStore
	}
	if _, err := w.db.Items().Get(ctx, w.repo.Key, false, w.request.Item.Path); !errors.Is(err, kernel.ErrNotFound) {
		return fmt.Errorf("record ran before deletion: %w", kernel.ErrStore)
	}
	if err := w.db.Settings().Put(ctx, "removal-test-ack", result); err != nil {
		return fmt.Errorf("write removal acknowledgement: %w", err)
	}
	return nil
}

func (w *removalFixture) events(t *testing.T) []outbox.Event {
	t.Helper()
	rows, _, err := w.db.Events().Next(t.Context(), w.origin, "removal-proof", 100)
	storekit.Must(t, err)
	return rows
}

func TestUnitGuardedRemovalEffectsAndRestore(t *testing.T) {
	for _, skip := range []bool{false, true} {
		t.Run(fmt.Sprintf("skip trash %t", skip), func(t *testing.T) {
			w, ctx := newRemovalFixture(t), t.Context()
			w.request.SkipTrashcan = skip
			beforeEvents := len(w.events(t))
			result := storekit.Get[api.RemovalResult](t)(w.service.Remove(ctx, w.request, w.revalidate, w.record))
			if result.Files != 1 || result.Bytes != 7 || len(result.Items) != 1 || result.Items[0].ID != w.request.Item.ID {
				t.Fatal("inaccurate actual removal", result)
			}
			var acknowledged api.RemovalResult
			storekit.Must(t, w.db.Settings().Get(ctx, "removal-test-ack", &acknowledged))
			storekit.Same(t, "atomic acknowledgement", result, acknowledged)
			_, err := w.db.Packages().Version(ctx, w.request.Item.Version.Package.ID, "1")
			storekit.WantErr(t, err, kernel.ErrNotFound)
			refs := storekit.Get[blob.Blob](t)(w.db.Blobs().Get(ctx, w.request.Item.Actual.SHA256))
			trash := storekit.Get[[]model.TrashEntry](t)(w.db.Trash().Find(ctx, w.repo.Key, w.request.Item.Path))
			if skip && (len(trash) != 0 || refs.Refs != 0) || !skip && (len(trash) != 1 || refs.Refs != 1) {
				t.Fatal("trash/reference lifecycle changed", skip, len(trash), refs.Refs)
			}
			events := w.events(t)
			if len(events) != beforeEvents+1 || events[len(events)-1].Topic != api.ArtifactDeleted {
				t.Fatal("normal delete event missing", len(events))
			}
			var fact api.ArtifactEvent
			storekit.Must(t, json.Unmarshal(events[len(events)-1].Payload, &fact))
			if fact.Actor.Name != "caller" || fact.Item.ID != w.request.Item.ID || fact.Size != 7 {
				t.Fatal("deletion actor/effect changed", fact)
			}
			_, err = w.service.Remove(ctx, w.request, w.revalidate, w.record)
			storekit.WantErr(t, err, kernel.ErrNotFound)
			if len(w.events(t)) != len(events) {
				t.Fatal("retry of an absent item duplicated the deletion event")
			}
			if !skip {
				storekit.Must(t, w.db.Tx(ctx, func(ctx context.Context) error {
					if _, err := w.items.Restore(ctx, item.Target{Repo: w.repo, Path: w.request.Item.Path}, trash[0].Record, "restorer"); err != nil {
						return err
					}
					return w.db.Trash().Delete(ctx, trash[0].ID)
				}))
				restored := storekit.Get[model.Item](t)(w.db.Items().Get(ctx, w.repo.Key, false, w.request.Item.Path))
				if restored.Actual != w.request.Item.Actual || restored.Size != 7 {
					t.Fatal("cleanup trash could not restore original content")
				}
			}
		})
	}
}

var errRemovalInjected = errors.New("injected removal refusal")

func TestUnitGuardedRemovalRefreshesCredentialAfterSelection(t *testing.T) {
	w, ctx := newRemovalFixture(t), t.Context()
	_, err := w.service.Remove(ctx, w.request, func(context.Context) error {
		// A token may expire while local selection waits even though the
		// publication guard prevents its record from changing concurrently.
		w.currentError = kernel.ErrBadCredentials
		return nil
	}, w.record)
	storekit.WantErr(t, err, kernel.ErrBadCredentials)
	_, err = w.db.Items().GetByID(ctx, w.request.Item.ID)
	storekit.Must(t, err)
}

func TestUnitGuardedRemovalUsesTheSelectedPropertyProjection(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stored model.Properties
		query  map[string][]string
	}{
		{"included value with unrelated sibling", model.Properties{"quality": {"eligible", "other"}}, map[string][]string{"quality": {"eligible"}}},
		{"excluded value absent from an existing key", model.Properties{"quality": {"good"}}, map[string][]string{"quality": {"blocked"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, ctx := newRemovalFixture(t), t.Context()
			before := w.request.Item
			storekit.Must(t, w.db.Items().SetProperties(ctx, before.ID, tc.stored, before.PropertiesModified))
			rows, _, err := pkgindex.NewReader(w.db, nil).Files(ctx, before.Version, tc.query, kernel.Page{Limit: 1})
			storekit.Must(t, err)
			if len(rows) != 1 {
				t.Fatal("selected item missing")
			}
			w.request.Item, w.request.Properties = rows[0], tc.query
			result, err := w.service.Remove(ctx, w.request, w.revalidate, w.record)
			storekit.Must(t, err)
			if result.Files != 1 || result.Items[0].ID != before.ID {
				t.Fatal("unchanged eligible property projection was refused")
			}
		})
	}
}

func TestUnitGuardedRemovalRollback(t *testing.T) {
	for _, stage := range []string{"selection", "acknowledgement"} {
		t.Run(stage, func(t *testing.T) {
			w, ctx := newRemovalFixture(t), t.Context()
			beforeEvents := len(w.events(t))
			revalidate, record := w.revalidate, w.record
			if stage == "selection" {
				revalidate = func(context.Context) error { return errRemovalInjected }
			} else {
				record = func(ctx context.Context, result api.RemovalResult) error {
					if err := w.record(ctx, result); err != nil {
						return err
					}
					return errRemovalInjected
				}
			}
			result, err := w.service.Remove(ctx, w.request, revalidate, record)
			storekit.WantErr(t, err, errRemovalInjected)
			if result.Files != 0 || result.Bytes != 0 || len(result.Items) != 0 {
				t.Fatal("failed removal returned a partial success", result)
			}
			current := storekit.Get[model.Item](t)(w.db.Items().Get(ctx, w.repo.Key, false, w.request.Item.Path))
			if current.ID != w.request.Item.ID {
				t.Fatal("failed acknowledgement lost the selected item")
			}
			trash := storekit.Get[[]model.TrashEntry](t)(w.db.Trash().Find(ctx, w.repo.Key, w.request.Item.Path))
			if len(trash) != 0 || len(w.events(t)) != beforeEvents {
				t.Fatal("failed removal leaked trash or events")
			}
			var ack api.RemovalResult
			storekit.WantErr(t, w.db.Settings().Get(ctx, "removal-test-ack", &ack), kernel.ErrNotFound)
		})
	}
}

func TestUnitGuardedRemovalRefusesChangedFacts(t *testing.T) {
	for _, change := range []string{"overwrite", "download", "property", "absent property", "owner", "folder", "permission"} {
		t.Run(change, func(t *testing.T) {
			w, ctx := newRemovalFixture(t), t.Context()
			before := w.request.Item
			switch change {
			case "overwrite":
				it := storekit.Get[model.Item](t)(w.items.Deploy(ctx, item.Deploy{Target: item.Target{Repo: w.repo, Path: before.Path}, Body: strings.NewReader("replacement")}))
				if it.ID != before.ID || !it.Created.Equal(before.Created) {
					t.Fatal("overwrite fixture did not preserve identity")
				}
			case "download":
				storekit.Must(t, w.db.Items().RecordDownload(ctx, before.ID, port.Download{At: removalAt.Add(time.Hour), By: "reader"}))
			case "property", "absent property":
				properties := model.Properties{"quality": {"changed"}}
				if change == "absent property" {
					properties = model.Properties{"quality": {"eligible"}, "absent": {"value"}}
				}
				// Reuse the exact property timestamp: comparison must include
				// requested values, including a previously absent property.
				storekit.Must(t, w.db.Items().SetProperties(ctx, before.ID, properties, before.PropertiesModified))
			case "owner":
				c := w.repo
				c.ProjectKey = "new-owner"
				storekit.Must(t, w.db.Repos().Update(ctx, c))
			case "folder":
				it := storekit.Get[model.Item](t)(w.db.Items().GetByID(ctx, before.ID))
				it.Folder, it.Size = true, 0
				_, err := w.db.Items().Put(ctx, it)
				storekit.Must(t, err)
			case "permission":
				w.decider.AllowsFunc = nil
			}
			called := false
			_, err := w.service.Remove(ctx, w.request, w.revalidate, func(context.Context, api.RemovalResult) error { called = true; return nil })
			want := kernel.ErrConflict
			if change == "permission" {
				want = kernel.ErrForbidden
			}
			storekit.WantErr(t, err, want)
			if called {
				t.Fatal("refused selection recorded success")
			}
			_, err = w.db.Items().GetByID(ctx, before.ID)
			storekit.Must(t, err)
		})
	}
}

type removalSidecars struct{ generic.Handler }

func (removalSidecars) Sidecars(_ repo.Config, path string) []string {
	if strings.HasSuffix(path, ".bin") {
		return []string{path + ".sig"}
	}
	return nil
}

func TestUnitGuardedRemovalCompanionsRequireDeleteAndCountActualEffects(t *testing.T) {
	w, ctx := newRemovalFixture(t), t.Context()
	w.items.Formats = func(repo.PackageType) format.Handler { return removalSidecars{} }
	sidecar := storekit.Get[model.Item](t)(w.items.Deploy(ctx, item.Deploy{
		Target: item.Target{Repo: w.repo, Path: "one.bin.sig"}, Body: strings.NewReader("signature"),
	}))
	grant := w.decider.AllowsFunc
	w.decider.AllowsFunc = func(rt accessapi.ResourceType, r accessapi.Repo, path string, want accessapi.Actions) bool {
		return path != sidecar.FullPath() && grant(rt, r, path, want)
	}
	_, err := w.service.Remove(ctx, w.request, w.revalidate, w.record)
	storekit.WantErr(t, err, kernel.ErrForbidden)
	_, err = w.db.Items().GetByID(ctx, w.request.Item.ID)
	storekit.Must(t, err)
	_, err = w.db.Items().GetByID(ctx, sidecar.ID)
	storekit.Must(t, err)
	w.decider.AllowsFunc = grant
	result := storekit.Get[api.RemovalResult](t)(w.service.Remove(ctx, w.request, w.revalidate, w.record))
	if result.Files != 2 || result.Bytes != 16 || len(result.Items) != 2 || result.Items[1].ID != sidecar.ID {
		t.Fatal("companion was omitted from actual result", result)
	}
}

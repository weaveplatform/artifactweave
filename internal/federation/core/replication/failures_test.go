package replication_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
)

type failedSettings struct {
	settings.SettingsStore
	read, write bool
}

func (s failedSettings) Get(ctx context.Context, key string, out any) error {
	if s.read {
		return errInjected
	}
	return s.SettingsStore.Get(ctx, key, out)
}

func (s failedSettings) Put(ctx context.Context, key string, value any) error {
	if err := s.SettingsStore.Put(ctx, key, value); err != nil {
		return err
	}
	if s.write {
		return errInjected
	}
	return nil
}

func TestUnitReplicationGlobalFailurePreservesPauseState(t *testing.T) {
	w, ctx := newWorld(), t.Context()
	must(t, w.service.SetBlocked(ctx, true, true, false))
	settingsStore := w.service.Settings
	w.service.Settings = failedSettings{SettingsStore: settingsStore, read: true}
	if _, err := w.service.Global(ctx); !errors.Is(err, errInjected) {
		t.Fatal("failed settings read claimed a global state", err)
	}
	if err := w.service.SetBlocked(ctx, false, true, true); !errors.Is(err, errInjected) {
		t.Fatal("failed read admitted global update", err)
	}
	w.service.Settings = failedSettings{SettingsStore: settingsStore, write: true}
	if err := w.service.SetBlocked(ctx, false, true, true); !errors.Is(err, errInjected) {
		t.Fatal("failed settings write claimed success", err)
	}
	w.service.Settings = settingsStore
	state, err := w.service.Global(ctx)
	must(t, err)
	if !state.Push || state.Pull {
		t.Fatal("failed write changed pause state")
	}
	w.service.Settings = nil
	if _, err := w.service.Global(ctx); !errors.Is(err, kernel.ErrStore) {
		t.Fatal("unwired settings claimed a global state", err)
	}
}

type failedListStore struct{ port.ConfigurationStore }

func (s failedListStore) Replications() port.ReplicationStore {
	return failedListTargets{s.ConfigurationStore.Replications()}
}

type failedListTargets struct{ port.ReplicationStore }

func (s failedListTargets) List(context.Context) ([]model.Replication, error) {
	return nil, errInjected
}

func TestUnitReplicationBulkFailurePreservesEveryTarget(t *testing.T) {
	w, ctx := newWorld(), t.Context()
	must(t, w.service.Replace(ctx, "local", replication.Input{URL: ptr("https://target.example/source")}))
	must(t, w.service.Replace(ctx, "remote", replication.Input{}))
	before, err := w.db.Replications().List(ctx)
	must(t, err)
	w.service.Store = failedListStore{w.db}
	if err := w.service.SetEnabled(ctx, false, nil, nil); !errors.Is(err, errInjected) {
		t.Fatal("failed enumeration claimed bulk success", err)
	}
	w.service.Store = failedStore{w.db}
	if err := w.service.SetEnabled(ctx, false, nil, nil); !errors.Is(err, errInjected) {
		t.Fatal("failed target write claimed bulk success", err)
	}
	after, err := w.db.Replications().List(ctx)
	must(t, err)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed bulk update left a changed target/secret/revision")
	}
}

func TestUnitReplicationBulkEnableRejectsUnsupportedLegacyState(t *testing.T) {
	for _, legacy := range []string{"multiple", "virtual", "invalid URL"} {
		t.Run(legacy, func(t *testing.T) {
			w, ctx := newWorld(), t.Context()
			must(t, w.service.Replace(ctx, "local", replication.Input{URL: ptr("https://target.example/source"), Enabled: ptr(false)}))
			target := stored(t, w, "local")
			want := kernel.ErrInvalid
			switch legacy {
			case "multiple":
				target.Key = "legacy-second"
				want = replication.ErrMultipleTargets
			case "virtual":
				target.RepoKey = "virtual"
			case "invalid URL":
				target.URL = "relative"
			}
			must(t, w.db.Replications().Put(ctx, target))
			before, err := w.db.Replications().List(ctx)
			must(t, err)
			if err := w.service.SetEnabled(ctx, true, nil, nil); !errors.Is(err, want) {
				t.Fatal("unsupported legacy configuration was enabled", err)
			}
			after, err := w.db.Replications().List(ctx)
			must(t, err)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("refused legacy enablement left changed targets")
			}
		})
	}
}

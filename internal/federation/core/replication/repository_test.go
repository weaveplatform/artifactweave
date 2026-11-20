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
)

func TestUnitReplicationRepositoryRefreshFailurePreservesTarget(t *testing.T) {
	for _, fault := range []string{"guard", "ownership", "targets", "source", "invalid URL", "write"} {
		t.Run(fault, func(t *testing.T) {
			w, ctx := newWorld(), t.Context()
			must(t, w.service.Replace(ctx, "remote", replication.Input{Password: ptr("original-fake-password")}))
			before := stored(t, w, "remote")
			changed := w.repos.values["remote"]
			changed.URL = "https://replacement.example/source"
			w.repos.values["remote"] = changed
			want := errInjected
			switch fault {
			case "guard":
				w.service.Guard = func(context.Context) error { return errInjected }
			case "ownership":
				w.repos.metadataError = errInjected
			case "targets":
				w.service.Store = failedRefreshReadStore{w.db}
			case "source":
				w.repos.secretError = errInjected
			case "invalid URL":
				changed.URL = "https://embedded-private-password@invalid.example"
				w.repos.values["remote"] = changed
				want = kernel.ErrInvalid
			case "write":
				w.service.Store = failedStore{w.db}
			}
			if err := w.service.RefreshRepository(ctx, "remote"); !errors.Is(err, want) {
				t.Fatalf("refresh failure = %v, want %v", err, want)
			}
			if stored(t, w, "remote") != before {
				t.Fatal("failed refresh changed target identity, credentials or revision")
			}
		})
	}
}

type failedRefreshReadStore struct{ port.ConfigurationStore }

func (s failedRefreshReadStore) Replications() port.ReplicationStore {
	return failedRefreshReadTargets{s.ConfigurationStore.Replications()}
}

type failedRefreshReadTargets struct{ port.ReplicationStore }

func (s failedRefreshReadTargets) ForRepo(context.Context, string) ([]model.Replication, error) {
	return nil, errInjected
}

func TestUnitReplicationRepositoryRefreshKeepsOtherOwners(t *testing.T) {
	for _, owner := range []string{"local", "absent", "event", "checksum", "multiple"} {
		t.Run(owner, func(t *testing.T) {
			w, ctx := newWorld(), t.Context()
			if owner != "local" && owner != "absent" {
				must(t, w.service.Replace(ctx, "remote", replication.Input{}))
				target := stored(t, w, "remote")
				switch owner {
				case "event":
					target.EventReplication = true
				case "checksum":
					target.CheckBinaryExistence = true
				case "multiple":
					target.Key = "second-legacy-target"
				}
				must(t, w.db.Replications().Put(ctx, target))
			}
			before, err := w.db.Replications().List(ctx)
			must(t, err)
			secretReads := w.repos.secretReads
			key := "remote"
			if owner == "local" {
				key = "local"
			}
			must(t, w.service.RefreshRepository(ctx, key))
			after, err := w.db.Replications().List(ctx)
			must(t, err)
			if !reflect.DeepEqual(before, after) || w.repos.secretReads != secretReads {
				t.Fatal("refresh changed another owner or unnecessarily opened source credentials")
			}
		})
	}
	if err := (replication.Service{}).RefreshRepository(t.Context(), "remote"); !errors.Is(err, kernel.ErrStore) {
		t.Fatal("unwired repository refresh did not fail explicitly", err)
	}
}

package events_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	artapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	artifactevents "github.com/weaveplatform/artifactweave/internal/artifacts/core/events"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
)

func TestUnitProducerCaptureFailuresRollBackBusinessState(t *testing.T) {
	marker := errors.New("origin unavailable")
	for _, check := range []struct {
		name string
		want error
		emit func(context.Context, *inmem.Store) error
	}{
		{name: "repository missing", want: kernel.ErrNotFound, emit: func(ctx context.Context, db *inmem.Store) error {
			return artifactevents.EmitArtifact(ctx, db, artapi.ArtifactDeleted, model.Item{RepoKey: "missing", ID: 1, Name: "file.bin"}, nil, "actor", time.Now())
		}},
		{name: "origin unavailable", want: marker, emit: func(ctx context.Context, db *inmem.Store) error {
			st := originStore{Store: db, err: marker}
			return artifactevents.EmitArtifact(ctx, st, artapi.ArtifactDeployed, model.Item{RepoKey: "business", ID: 1, Name: "file.bin"}, nil, "actor", time.Now())
		}},
		{name: "invalid origin", want: kernel.ErrInvalid, emit: func(ctx context.Context, db *inmem.Store) error {
			return artifactevents.Emit(ctx, originStore{Store: db}, artapi.ArtifactDeployed, "item", time.Now(), artapi.ArtifactEvent{})
		}},
		{name: "unencodable build time", emit: func(ctx context.Context, db *inmem.Store) error {
			return artifactevents.Emit(ctx, db, artapi.BuildUploaded, "build", time.Now(), artapi.BuildEvent{Build: artapi.EventBuild{Started: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}})
		}},
	} {
		t.Run(check.name, func(t *testing.T) {
			db := inmem.New()
			err := db.Tx(t.Context(), func(ctx context.Context) error {
				if err := db.Repos().Create(ctx, *repo.Defaults(&repo.Config{Key: "business", Class: repo.Local})); err != nil {
					return err
				}
				return check.emit(ctx, db)
			})
			if err == nil || (check.want != nil && !errors.Is(err, check.want)) {
				t.Fatalf("capture failure=%v", err)
			}
			if _, err := db.Repos().Get(t.Context(), "business"); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatalf("failed capture retained business state: %v", err)
			}
		})
	}
}

type originStore struct {
	artifactevents.Store
	err error
}

func (s originStore) Instance() instance.InstanceStore {
	return originReader{InstanceStore: s.Store.Instance(), err: s.err}
}

type originReader struct {
	instance.InstanceStore
	err error
}

func (s originReader) ID(context.Context) (string, error) { return "", s.err }

package build

import (
	"context"
	"fmt"
	"strconv"

	artapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	artifactevents "github.com/weaveplatform/artifactweave/internal/artifacts/core/events"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
)

func (s *Service) emitBuild(ctx context.Context, topic string, run model.BuildRun, target, by string, copied bool) error {
	facts := artapi.BuildEvent{
		EventMetadata: artifactevents.Metadata(ctx, by),
		Build:         artapi.EventBuild{ID: run.ID, ProjectKey: run.ProjectKey, Name: run.Name, Number: run.Number, Started: run.Started},
		Copy:          copied,
	}
	if target != "" {
		repository, err := artifactevents.Repository(ctx, s.Store, target)
		if err != nil {
			return fmt.Errorf("build: capture event target: %w", err)
		}
		facts.Target = &repository
	}
	if err := artifactevents.Emit(ctx, s.Store, topic, artifactevents.Key("build", strconv.FormatInt(run.ID, 10)), s.now(), facts); err != nil {
		return fmt.Errorf("build: stage event: %w", err)
	}
	return nil
}

package registry

import (
	"context"
	"fmt"
	"strings"

	artapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	artifactevents "github.com/weaveplatform/artifactweave/internal/artifacts/core/events"
	layout "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/docker"
)

func (s *Service) imageFacts(ctx context.Context, o Owner, ref, digest string) (artapi.EventImage, error) {
	repository, err := artifactevents.Repository(ctx, s.Store, o.Repo.Key)
	if err != nil {
		return artapi.EventImage{}, fmt.Errorf("registry: capture event repository: %w", err)
	}
	image := artapi.EventImage{Repository: repository, Name: o.Name, Digest: digest}
	if !layout.IsDigestFolder(ref) && !strings.HasPrefix(ref, "sha256:") {
		image.Tag = ref
	}
	return image, nil
}

func (s *Service) emitDocker(ctx context.Context, topic string, o Owner, ref, digest string, source *artapi.EventImage, copyImage bool) error {
	image, err := s.imageFacts(ctx, o, ref, digest)
	if err != nil {
		return err
	}
	facts := artapi.DockerEvent{EventMetadata: artifactevents.Metadata(ctx, o.By), Image: image, Source: source, Copy: copyImage}
	if err := artifactevents.Emit(ctx, s.Store, topic, artifactevents.Key("docker", o.Repo.Key, o.Name, ref), s.now(), facts); err != nil {
		return fmt.Errorf("registry: stage event: %w", err)
	}
	return nil
}

// One push can create a digest twin and descriptive platform folders. Those
// are physical layout, not additional Docker pushes.
func pushReference(p Push) string {
	for _, folder := range p.Folders {
		if !layout.IsDigestFolder(folder) {
			return folder
		}
	}
	return "sha256:" + strings.TrimPrefix(p.Manifest.Digest, "sha256:")
}

package api

import (
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
)

// Durable module topics are independent of vendor webhook envelopes.
const (
	ArtifactDeployed = "artifact.deployed"
	ArtifactDeleted  = "artifact.deleted"
	ArtifactMoved    = "artifact.moved"
	ArtifactCopied   = "artifact.copied"
	ArtifactCached   = "artifact.cached"
	PropertyAdded    = "property.added"
	PropertyDeleted  = "property.deleted"
	DockerPushed     = "docker.pushed"
	DockerDeleted    = "docker.deleted"
	DockerPromoted   = "docker.promoted"
	BuildUploaded    = "build.uploaded"
	BuildDeleted     = "build.deleted"
	BuildPromoted    = "build.promoted"
)

// EventMetadata is a committed outcome and sanitized actor. It intentionally
// contains neither a kernel.Principal nor user-provided configuration bodies.
type EventMetadata struct {
	Version int          `json:"version"`
	Outcome string       `json:"outcome"`
	Actor   outbox.Actor `json:"actor"`
}

// EventRepository is a descriptive snapshot, not an authorization grant or a
// repository-lifetime fence. No URL, password or sensitive header is present.
type EventRepository struct {
	Key         string           `json:"key"`
	ProjectKey  string           `json:"project_key,omitempty"`
	PackageType repo.PackageType `json:"package_type"`
}

// EventArtifact preserves the item identity and location even after deletion.
type EventArtifact struct {
	Repository EventRepository `json:"repository"`
	ID         int64           `json:"id"`
	Path       string          `json:"path"`
	Cached     bool            `json:"cached,omitempty"`
	Folder     bool            `json:"folder,omitempty"`
	// Variant identifies raw cache representations without copying source URLs.
	Variant string `json:"variant,omitempty"`
}

// ArtifactEvent records one affected item; Source is present for transfer.
type ArtifactEvent struct {
	EventMetadata
	Item   EventArtifact  `json:"item"`
	Source *EventArtifact `json:"source,omitempty"`
	Size   int64          `json:"size"`
	SHA256 string         `json:"sha256,omitempty"`
}

// PropertyEvent identifies one normalized key transition without copying
// arbitrary property values into durable events.
type PropertyEvent struct {
	EventMetadata
	Item     EventArtifact `json:"item"`
	Property string        `json:"property"`
}

// EventImage is the logical image/tag affected by a registry operation.
type EventImage struct {
	Repository EventRepository `json:"repository"`
	Name       string          `json:"name"`
	Tag        string          `json:"tag,omitempty"`
	Digest     string          `json:"digest,omitempty"`
}

// DockerEvent is emitted once by the registry business operation, independently
// of its physical manifest/layer/history item mutations.
type DockerEvent struct {
	EventMetadata
	Image  EventImage  `json:"image"`
	Source *EventImage `json:"source,omitempty"`
	Copy   bool        `json:"copy,omitempty"`
}

// EventBuild preserves a run's immutable identity after its document is gone.
type EventBuild struct {
	ID         int64     `json:"id"`
	ProjectKey string    `json:"project_key,omitempty"`
	Name       string    `json:"name"`
	Number     string    `json:"number"`
	Started    time.Time `json:"started"`
}

// BuildEvent records a build publication, deletion or successful promotion.
type BuildEvent struct {
	EventMetadata
	Build  EventBuild       `json:"build"`
	Target *EventRepository `json:"target,omitempty"`
	Copy   bool             `json:"copy,omitempty"`
}

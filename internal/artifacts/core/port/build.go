package port

import (
	"context"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/aql"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// BuildStore persists build-info runs (decision 0014): one record per run,
// keyed by (project, name, number, started), with the document as published
// and its modules, properties, artifacts and dependencies as rows for the
// AQL build domains and the checksum searches. A project key of "" is the
// default project.
//
// The store keeps the document itself and serves reads from it. Writing the
// copy of the document as an item (model.Build.DocumentItem) belongs to the
// build service, which holds the blob store: it puts the run and the item in
// one Tx, so a publish or a delete changes both or neither.
type BuildStore interface {
	// Put stores a new run with its modules and returns it with its
	// assigned ID. kernel.ErrConflict when (project, name, number, started)
	// exists; kernel.ErrInvalid when the name or number is empty or started is
	// zero.
	Put(ctx context.Context, b model.Build) (model.Build, error)
	// Replace replaces the content of run b.ID: its URL, modification stamp,
	// properties, modules and document. The identity (project, name, number,
	// started) and Created stay as stored. kernel.ErrNotFound when the run does
	// not exist.
	Replace(ctx context.Context, b model.Build) error
	// Get returns a run with its modules. A zero started selects the
	// latest-started run of that number. kernel.ErrNotFound when there is none.
	Get(
		ctx context.Context,
		projectKey, name, number string,
		started time.Time,
	) (model.Build, error)
	// GetForUpdate is Get with a row lock held until the enclosing Tx ends.
	// It requires a transaction context; use it before read-modify-write.
	GetForUpdate(
		ctx context.Context,
		projectKey, name, number string,
		started time.Time,
	) (model.Build, error)
	// ByID returns the run with id, with its modules.
	ByID(ctx context.Context, id int64) (model.Build, error)
	// ByIDForUpdate locks the identified run before loading its content.
	// It requires an enclosing Tx, like GetForUpdate.
	ByIDForUpdate(ctx context.Context, id int64) (model.Build, error)
	// Latest returns the latest-started run of a name, with its modules.
	// kernel.ErrNotFound when the name has no runs.
	Latest(ctx context.Context, projectKey, name string) (model.Build, error)
	// Runs lists a name's runs, newest first by (started, ID). A name with
	// no runs lists nothing.
	Runs(
		ctx context.Context,
		projectKey, name string,
		p kernel.Page,
	) ([]model.BuildRun, string, error)
	// Names lists a project's build names ordered by name, each with the
	// start of its latest run.
	Names(ctx context.Context, projectKey string, p kernel.Page) ([]model.BuildName, string, error)
	// DeleteNumbers deletes one run for each occurrence of a number in
	// numbers, the earliest-started of that number's remaining runs first,
	// and returns the deleted runs in deletion order. An occurrence with no
	// run left is skipped. This is what `jf rt bp --overwrite` expects: it
	// sends a number once per existing run.
	DeleteNumbers(
		ctx context.Context,
		projectKey, name string,
		numbers []string,
	) ([]model.BuildRun, error)
	// DeleteAll deletes every run of a name and its retention, and returns
	// the deleted runs oldest first.
	DeleteAll(ctx context.Context, projectKey, name string) ([]model.BuildRun, error)
	// Rename moves every run of a name, and its retention, to another name. A
	// retention left on to, which has no runs, is replaced by from's or removed
	// when from has none. kernel.ErrNotFound when from has no runs;
	// kernel.ErrConflict when to has runs. Renaming a name to itself does
	// nothing.
	Rename(ctx context.Context, projectKey, from, to string) error

	// AddPromotion records a promotion of run p.BuildID and returns it
	// with its assigned ID. kernel.ErrNotFound when the run does not exist.
	AddPromotion(ctx context.Context, p model.BuildPromotion) (model.BuildPromotion, error)
	// Promotions returns a run's promotions oldest first by (created, ID).
	// kernel.ErrNotFound when the run does not exist.
	Promotions(ctx context.Context, buildID int64) ([]model.BuildPromotion, error)

	// FindArtifacts returns the build artifacts whose checksums match every
	// non-empty field of c, across all projects, ordered by run ID, then
	// module and file position. kernel.ErrInvalid when c has no checksum.
	FindArtifacts(ctx context.Context, c kernel.Checksums) ([]model.BuildFileRef, error)
	// FindDependencies is FindArtifacts for dependencies.
	FindDependencies(ctx context.Context, c kernel.Checksums) ([]model.BuildFileRef, error)

	// Search runs an AQL builds or build.promotions query (decision
	// 0012): the matching runs, or promotions with their runs, in the
	// order aql.SelectBuilds returns them. A hit's run carries its own
	// columns only, not its properties, modules or document. Relation
	// paths reach items by checksum. kernel.ErrInvalid for an items query.
	Search(ctx context.Context, q *aql.Query, o aql.Options) ([]aql.BuildHit, error)

	// PutRetention creates or replaces a build name's retention.
	PutRetention(ctx context.Context, r model.BuildRetention) error
	// Retention returns a build name's retention. kernel.ErrNotFound when none
	// is set.
	Retention(ctx context.Context, projectKey, name string) (model.BuildRetention, error)
}

// NormaliseBuild is what every backend stores and returns for b: Normalise,
// with build and module properties cleaned (CleanProperties) to
// model.BuildPropertyValueMax.
func NormaliseBuild(b model.Build) model.Build {
	b = kernel.Normalise(b)
	b.Properties = cleanBuildProperties(b.Properties)
	for i := range b.Modules {
		b.Modules[i].Properties = cleanBuildProperties(b.Modules[i].Properties)
	}
	return b
}

// cleanBuildProperties is CleanProperties, nil when no key has a value.
func cleanBuildProperties(props model.Properties) model.Properties {
	out := CleanProperties(props, model.BuildPropertyValueMax)
	if len(out) == 0 {
		return nil
	}
	return out
}

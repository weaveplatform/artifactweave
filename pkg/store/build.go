package store

import (
	"context"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// BuildStore persists build-info runs (decision 0014): one record per run,
// keyed by (project, name, number, started), with the document as published
// and its modules, properties, artifacts and dependencies as rows for the
// AQL build domains and the checksum searches. A project key of "" is the
// default project.
//
// The store keeps the document itself and serves reads from it. Writing the
// copy of the document as an item (types.Build.DocumentItem) belongs to the
// build service, which holds the blob store: it puts the run and the item in
// one Tx, so a publish or a delete changes both or neither.
type BuildStore interface {
	// Put stores a new run with its modules and returns it with its
	// assigned ID. ErrConflict when (project, name, number, started)
	// exists; ErrInvalid when the name or number is empty or started is
	// zero.
	Put(ctx context.Context, b types.Build) (types.Build, error)
	// Replace replaces the content of run b.ID: its URL, modification
	// stamp, properties, modules and document. The identity (project,
	// name, number, started) and Created stay as stored. ErrNotFound when
	// the run does not exist.
	Replace(ctx context.Context, b types.Build) error
	// Get returns a run with its modules. A zero started selects the
	// latest-started run of that number. ErrNotFound when there is none.
	Get(
		ctx context.Context,
		projectKey, name, number string,
		started time.Time,
	) (types.Build, error)
	// ByID returns the run with id, with its modules.
	ByID(ctx context.Context, id int64) (types.Build, error)
	// Latest returns the latest-started run of a name, with its modules.
	// ErrNotFound when the name has no runs.
	Latest(ctx context.Context, projectKey, name string) (types.Build, error)
	// Runs lists a name's runs, newest first by (started, ID). A name with
	// no runs lists nothing.
	Runs(ctx context.Context, projectKey, name string, p Page) ([]types.BuildRun, string, error)
	// Names lists a project's build names ordered by name, each with the
	// start of its latest run.
	Names(ctx context.Context, projectKey string, p Page) ([]types.BuildName, string, error)
	// DeleteNumbers deletes one run for each occurrence of a number in
	// numbers, the earliest-started of that number's remaining runs first,
	// and returns the deleted runs in deletion order. An occurrence with no
	// run left is skipped. This is what `jf rt bp --overwrite` expects: it
	// sends a number once per existing run.
	DeleteNumbers(
		ctx context.Context,
		projectKey, name string,
		numbers []string,
	) ([]types.BuildRun, error)
	// DeleteAll deletes every run of a name and its retention, and returns
	// the deleted runs oldest first.
	DeleteAll(ctx context.Context, projectKey, name string) ([]types.BuildRun, error)
	// Rename moves every run of a name, and its retention, to another name.
	// A retention left on to, which has no runs, is replaced by from's or
	// removed when from has none. ErrNotFound when from has no runs;
	// ErrConflict when to has runs. Renaming a name to itself does nothing.
	Rename(ctx context.Context, projectKey, from, to string) error

	// AddPromotion records a promotion of run p.BuildID and returns it
	// with its assigned ID. ErrNotFound when the run does not exist.
	AddPromotion(ctx context.Context, p types.BuildPromotion) (types.BuildPromotion, error)
	// Promotions returns a run's promotions oldest first by (created, ID).
	// ErrNotFound when the run does not exist.
	Promotions(ctx context.Context, buildID int64) ([]types.BuildPromotion, error)

	// FindArtifacts returns the build artifacts whose checksums match every
	// non-empty field of c, across all projects, ordered by run ID, then
	// module and file position. ErrInvalid when c has no checksum.
	FindArtifacts(ctx context.Context, c types.Checksums) ([]types.BuildFileRef, error)
	// FindDependencies is FindArtifacts for dependencies.
	FindDependencies(ctx context.Context, c types.Checksums) ([]types.BuildFileRef, error)

	// Search runs an AQL builds or build.promotions query (decision
	// 0012): the matching runs, or promotions with their runs, in the
	// order aql.SelectBuilds returns them. A hit's run carries its own
	// columns only, not its properties, modules or document. Relation
	// paths reach items by checksum. ErrInvalid for an items query.
	Search(ctx context.Context, q *aql.Query, o aql.Options) ([]aql.BuildHit, error)

	// PutRetention creates or replaces a build name's retention.
	PutRetention(ctx context.Context, r types.BuildRetention) error
	// Retention returns a build name's retention. ErrNotFound when none is
	// set.
	Retention(ctx context.Context, projectKey, name string) (types.BuildRetention, error)
}

// NormaliseBuild is what every backend stores and returns for b: Normalise,
// with build and module properties cleaned (CleanProperties) to
// types.BuildPropertyValueMax.
func NormaliseBuild(b types.Build) types.Build {
	b = Normalise(b)
	b.Properties = cleanBuildProperties(b.Properties)
	for i := range b.Modules {
		b.Modules[i].Properties = cleanBuildProperties(b.Modules[i].Properties)
	}
	return b
}

// cleanBuildProperties is CleanProperties, nil when no key has a value.
func cleanBuildProperties(props types.Properties) types.Properties {
	out := CleanProperties(props, types.BuildPropertyValueMax)
	if len(out) == 0 {
		return nil
	}
	return out
}

// CleanProperties is how the stores keep properties, one row per value:
// keys without values dropped, each value cut to limit
// (types.TruncatePropertyValue), and the values sorted and unique, so two
// values that differ only past the limit become one.
func CleanProperties(props types.Properties, limit int) types.Properties {
	out := types.Properties{}
	for k, values := range props {
		if len(values) == 0 {
			continue
		}
		cut := make([]string, len(values))
		for i, v := range values {
			cut[i] = types.TruncatePropertyValue(v, limit)
		}
		out.Set(k, cut...)
	}
	return out
}

package sql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/weaveplatform/artifactweave/migration/artifacts/core/aql"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/dialect"
)

// buildRunCols are the run columns a search returns: buildCols but the
// document, which is last.
var buildRunCols = buildCols[:len(buildCols)-1]

// qualified lists cols on table.
func qualified(table string, cols []string) string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = table + "." + c
	}
	return strings.Join(out, ", ")
}

// Search implements port.BuildStore with the SQL aql.Compile writes.
func (b buildStore) Search(
	ctx context.Context,
	q *aql.Query,
	o aql.Options,
) ([]aql.BuildHit, error) {
	if q.Domain == aql.DomainItems {
		return nil, fmt.Errorf("search builds: an %s query: %w", q.Domain, kernel.ErrInvalid)
	}
	d := aql.SQLite
	if b.s.Dialect().Name == "postgres" {
		d = aql.PostgreSQL
	}
	c, err := q.Compile(d, o)
	if err != nil {
		return nil, fmt.Errorf("search builds: %w", err)
	}
	query := "SELECT " + qualified("builds", buildRunCols) + " FROM builds"
	if q.Domain == aql.DomainPromotions {
		// The run is aliased pb so the criteria's own aliases (builds
		// for the found row of a builds query, jN for paths) stay free.
		query = "SELECT " + qualified("build_promotions", promotionCols) + ", " +
			qualified("pb", buildRunCols) +
			" FROM build_promotions JOIN builds pb ON pb.id = build_promotions.build_id"
	}
	var hits []aql.BuildHit
	err = b.s.Rows(ctx, "search builds", query+" WHERE "+c.Where+c.Tail, c.Args,
		func(r *sql.Rows) error {
			h, err := scanBuildHit(r, q.Domain == aql.DomainPromotions)
			hits = append(hits, h)
			return err
		})
	return hits, err
}

// scanBuildHit reads a run, after its promotion when promoted is set.
func scanBuildHit(r *sql.Rows, promoted bool) (aql.BuildHit, error) {
	var (
		h                 aql.BuildHit
		p                 model.BuildPromotion
		started           int64
		created, modified sql.NullInt64
		promotedAt        sql.NullInt64
	)
	var dest []any
	if promoted {
		dest = append(dest, &p.ID, &p.BuildID, &promotedAt, &p.CreatedBy, &p.Status, &p.Comment,
			&p.CIUser, &p.SourceRepo, &p.Repo)
	}
	b := &h.Build
	dest = append(dest, &b.ID, &b.ProjectKey, &b.Name, &b.Number, &started, &b.URL,
		&created, &b.CreatedBy, &modified, &b.ModifiedBy)
	if err := r.Scan(dest...); err != nil {
		return h, err //nolint:wrapcheck // wrapped by Rows
	}
	b.Started = dialect.FromMicros(sql.NullInt64{Int64: started, Valid: true})
	b.Created, b.Modified = dialect.FromMicros(created), dialect.FromMicros(modified)
	if promoted {
		p.Created = dialect.FromMicros(promotedAt)
		h.Promotion = &p
	}
	return h, nil
}

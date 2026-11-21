package sql

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
	"github.com/weaveplatform/artifactweave/internal/retention/core/port"
)

func (c cleanupStore) RunPage(ctx context.Context, q port.RunListing) (port.RunPage, error) {
	if err := q.Validate(); err != nil {
		return port.RunPage{}, fmt.Errorf("cleanup run page: %w", err)
	}
	out := port.RunPage{Runs: []model.CleanupRunSnapshot{}}
	where, args := c.runListingWhere(q)
	from := " FROM cleanup_runs r JOIN cleanup_executions e ON e.run_id = r.id WHERE " + where
	err := c.s.Q(ctx).QueryRowContext(ctx, c.s.Dialect().Rebind("SELECT COUNT(*)"+from), args...).Scan(&out.Total)
	if err != nil {
		return port.RunPage{}, c.s.Wrap("count cleanup report runs", err)
	}
	if q.Offset >= out.Total {
		return out, nil
	}
	columns := "r.id, r." + strings.ReplaceAll(runCols, ", ", ", r.")
	policy := c.runSnapshotField("Policy")
	query := "SELECT " + columns + ", " + policy + from + " ORDER BY " + c.runOrdering(q) + " LIMIT ? OFFSET ?"
	err = c.s.Rows(ctx, "page cleanup report runs", query, append(slices.Clip(args), q.Limit, q.Offset), func(row *sql.Rows) error {
		var raw sql.NullString
		run, err := scanRun(row, &raw)
		if err != nil {
			return err
		}
		var policy model.CleanupPolicy
		if err := dialect.FromJSON(raw, &policy); err != nil {
			return err
		}
		out.Runs = append(out.Runs, model.CleanupSnapshot(run, policy))
		return nil
	})
	if err != nil {
		return port.RunPage{}, err
	}
	return out, nil
}

// Paths are fixed adapter-owned identifiers, never caller expressions. Missing
// JSON fields remain NULL: an unbound old row must not become a global policy.
func (c cleanupStore) runSnapshotField(path ...string) string {
	if c.s.Dialect().Dollar {
		operator := "#>>"
		if len(path) == 1 && path[0] == "Policy" {
			operator = "#>"
		}
		return "(e.snapshot " + operator + " '{" + strings.Join(path, ",") + "}')"
	}
	return "json_extract(e.snapshot, '$." + strings.Join(path, ".") + "')"
}

func (c cleanupStore) runListingWhere(q port.RunListing) (string, []any) {
	project := c.runSnapshotField("Policy", "ProjectKey")
	incarnation := c.runSnapshotField("ProjectIncarnation")
	where := []string{
		"r.trigger_id = r.id",
		"r.item_type = 'package'",
		c.runSnapshotField("Policy", "ItemType") + " = 'package'",
		c.runSnapshotField("Policy", "Key") + " = r.policy_key",
	}
	args := []any{}
	scopes := make([]string, 0, len(q.Scopes))
	for _, scope := range q.Scopes {
		condition := project + " = ?"
		args = append(args, scope.ProjectKey)
		if scope.Incarnation != "" {
			condition += " AND " + incarnation + " = ?"
			args = append(args, scope.Incarnation)
		}
		scopes = append(scopes, "("+condition+")")
	}
	if len(scopes) == 0 {
		scopes = []string{"1 = 0"}
	}
	where = append(where, "("+strings.Join(scopes, " OR ")+")")
	statuses := []api.RunStatus{api.RunInProgress, api.RunInProgressWithErrors, api.RunCompleted, api.RunCompletedWithErrors, api.RunStopping, api.RunStopped, api.RunFailed}
	where = append(where, "r.status IN ("+dialect.Placeholders(len(statuses))+")")
	for _, value := range statuses {
		args = append(args, value)
	}
	if q.PolicyKey != "" {
		where = append(where, "r.policy_key = ?")
		args = append(args, q.PolicyKey)
	}
	if len(q.ItemTypes) > 0 {
		where = append(where, "r.item_type IN ("+dialect.Placeholders(len(q.ItemTypes))+")")
		for _, value := range q.ItemTypes {
			args = append(args, value)
		}
	}
	if len(q.Statuses) > 0 {
		where = append(where, "r.status IN ("+dialect.Placeholders(len(q.Statuses))+")")
		for _, value := range q.Statuses {
			args = append(args, value)
		}
	}
	if q.From != nil {
		where = append(where, "r.started_at >= ?")
		args = append(args, *q.From)
	}
	if q.To != nil {
		where = append(where, "r.started_at < ?")
		args = append(args, *q.To)
	}
	if q.ExcludeBlank {
		where = append(where, "r.total_items <> 0")
	}
	return strings.Join(where, " AND "), args
}

func (c cleanupStore) runOrdering(q port.RunListing) string {
	type field struct {
		column         string
		text, nullable bool
	}
	fields := map[port.RunOrder]field{
		port.RunOrderID:           {column: "r.id"},
		port.RunOrderItemType:     {column: "r.item_type", text: true},
		port.RunOrderPolicy:       {column: "r.policy_key", text: true},
		port.RunOrderProject:      {column: c.runSnapshotField("Policy", "ProjectKey"), text: true},
		port.RunOrderStarted:      {column: "r.started_at", nullable: true},
		port.RunOrderEnded:        {column: "r.ended_at", nullable: true},
		port.RunOrderTotal:        {column: "r.total_items"},
		port.RunOrderCleanedSize:  {column: "r.cleaned_size"},
		port.RunOrderCleanedItems: {column: "r.cleaned_items"},
		port.RunOrderStatus:       {column: "r.status", text: true},
		port.RunOrderActor:        {column: "r.triggered_by", text: true},
		port.RunOrderManual:       {column: "r.manual"},
		port.RunOrderDry:          {column: "r.dry_run"},
	}
	selected, found := fields[q.Order]
	if !found {
		selected = fields[port.RunOrderStarted]
	}
	direction := " ASC"
	if q.Descending {
		direction = " DESC"
	}
	column := selected.column
	if selected.text {
		collation := "BINARY"
		if c.s.Dialect().Dollar {
			collation = `"C"`
		}
		column += " COLLATE " + collation
	}
	order := column + direction
	if selected.nullable {
		order = "CASE WHEN " + selected.column + " IS NULL THEN 0 ELSE 1 END" + direction + ", " + order
	}
	if selected.column != "r.id" {
		order += ", r.id" + direction
	}
	return order
}

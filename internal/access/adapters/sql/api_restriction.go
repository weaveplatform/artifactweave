package sql

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"

	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
)

// APIRestrictions returns the API restriction store: current restrictions
// and the append-only audit of their changes.
func (s *Store) APIRestrictions() port.APIRestrictionStore { return apiRestrictions{s} }

type apiRestrictions struct{ s *Store }

const restrictionColumns = "id, user_name, actor, reason, until_at, changed_at"

func scanAPIRestriction(row dialect.EntRow) (port.APIRestriction, error) {
	var r port.APIRestriction
	var until, changed sql.NullInt64
	err := row.Scan(&r.Sequence, &r.User, &r.Actor, &r.Reason, &until, &changed)
	r.Until, r.Changed = dialect.FromMicros(until), dialect.FromMicros(changed)
	return r, err //nolint:wrapcheck // the caller supplies operation context
}

func (a apiRestrictions) Get(ctx context.Context, user string) (port.APIRestriction, error) {
	r, err := scanAPIRestriction(a.s.Q(ctx).QueryRowContext(ctx,
		a.s.Dialect().
			Rebind("SELECT "+restrictionColumns+" FROM api_restrictions WHERE user_name = ?"),
		user),
	)
	return r, a.s.Wrap("get API restriction", err)
}

func (a apiRestrictions) Put(
	ctx context.Context,
	r port.APIRestriction,
) (port.APIRestriction, error) {
	if err := r.Validate(); err != nil {
		return r, fmt.Errorf("API restriction: %w", err)
	}
	r = kernel.Normalise(r)
	err := a.s.Tx(ctx, func(ctx context.Context) error {
		var name string
		if err := a.s.Q(ctx).
			QueryRowContext(ctx, a.s.Dialect().Rebind("SELECT name FROM users WHERE name = ? "+a.s.Dialect().ForUpdate), r.User).
			Scan(&name); err != nil {
			return a.s.Wrap("lock restricted user", err)
		}
		var err error
		r.Sequence, err = a.s.InsertID(
			ctx,
			"audit API restriction",
			dialect.Insert(
				"api_restriction_audit",
				[]string{"user_name", "actor", "reason", "until_at", "changed_at"},
			),
			r.User,
			r.Actor,
			r.Reason,
			dialect.Micros(r.Until),
			dialect.Micros(r.Changed),
		)
		if err != nil {
			return err
		}
		_, err = a.s.Exec(
			ctx,
			"set API restriction",
			dialect.Upsert(
				"api_restrictions",
				[]string{"id", "user_name", "actor", "reason", "until_at", "changed_at"},
				[]string{"user_name"},
			),
			r.Sequence,
			r.User,
			r.Actor,
			r.Reason,
			dialect.Micros(r.Until),
			dialect.Micros(r.Changed),
		)
		return err
	})
	return r, err
}

func (a apiRestrictions) History(
	ctx context.Context,
	user string,
	p kernel.Page,
) ([]port.APIRestriction, string, error) {
	p, err := p.Normalize()
	if err != nil {
		return nil, "", fmt.Errorf("restriction audit page: %w", err)
	}
	key, err := p.Key(1)
	if err != nil {
		return nil, "", fmt.Errorf("restriction audit cursor: %w", err)
	}
	where := "1 = 1"
	args := []any{}
	if user != "" {
		where += " AND user_name = ?"
		args = append(args, user)
	}
	if len(key) != 0 {
		after, err := strconv.ParseInt(key[0], 10, 64)
		if err != nil || after < 1 {
			return nil, "", fmt.Errorf("restriction audit cursor: %w", kernel.ErrInvalidCursor)
		}
		where += " AND id < ?"
		args = append(args, after)
	}
	args = append(args, p.Limit+1)
	var out []port.APIRestriction
	err = a.s.Rows(
		ctx,
		"page API restriction audit",
		"SELECT "+restrictionColumns+" FROM api_restriction_audit WHERE "+where+" ORDER BY id DESC LIMIT ?",
		args,
		func(row *sql.Rows) error {
			r, err := scanAPIRestriction(row)
			if err == nil {
				out = append(out, r)
			}
			return err
		},
	)
	out, next := kernel.Trim(
		out,
		p.Limit,
		func(r port.APIRestriction) []string { return []string{strconv.FormatInt(r.Sequence, 10)} },
	)
	return out, next, err
}

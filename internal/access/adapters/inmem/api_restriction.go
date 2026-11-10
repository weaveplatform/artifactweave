package inmem

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"

	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

// APIRestrictions returns the API restriction store: current restrictions
// and the append-only audit of their changes.
func (s *Store) APIRestrictions() port.APIRestrictionStore { return apiRestrictions{s} }

type apiRestrictions struct{ s *Store }

func (a apiRestrictions) current() *memtx.Table[string, port.APIRestriction] {
	return memtx.TableOf[string, port.APIRestriction](a.s.Store, "api_restrictions")
}

func (a apiRestrictions) audit() *memtx.Table[int64, port.APIRestriction] {
	return memtx.TableOf[int64, port.APIRestriction](a.s.Store, "api_restriction_audit")
}

func (a apiRestrictions) Get(ctx context.Context, user string) (port.APIRestriction, error) {
	var out port.APIRestriction
	err := a.s.Do(ctx, func() error {
		var ok bool
		out, ok = a.current().Get(user)
		if !ok {
			return memtx.NotFound("API restriction", user)
		}
		return nil
	})
	return out, err
}

func (a apiRestrictions) Put(
	ctx context.Context,
	r port.APIRestriction,
) (port.APIRestriction, error) {
	if err := r.Validate(); err != nil {
		return r, err //nolint:wrapcheck // Validate's error wraps kernel.ErrInvalid
	}
	err := a.s.Do(ctx, func() error {
		if !a.s.usersT().Has(r.User) {
			return memtx.NotFound("restricted user", r.User)
		}
		r.Sequence = a.s.NextID("api_restriction_audit")
		r = kernel.Normalise(r)
		a.current().Put(r.User, r)
		a.audit().Put(r.Sequence, r)
		return nil
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
		return nil, "", err //nolint:wrapcheck // already a store sentinel
	}
	key, err := p.Key(1)
	if err != nil {
		return nil, "", err //nolint:wrapcheck // already a store sentinel
	}
	var after int64
	if len(key) != 0 {
		after, err = strconv.ParseInt(key[0], 10, 64)
		if err != nil || after < 1 {
			return nil, "", fmt.Errorf("restriction audit cursor: %w", kernel.ErrInvalidCursor)
		}
	}
	var out []port.APIRestriction
	err = a.s.Do(ctx, func() error {
		for _, r := range a.audit().Rows {
			if user != "" && r.User != user || after != 0 && r.Sequence >= after {
				continue
			}
			i, _ := slices.BinarySearchFunc(
				out,
				r,
				func(a, b port.APIRestriction) int { return cmp.Compare(b.Sequence, a.Sequence) },
			)
			out = slices.Insert(out, i, r)
			if len(out) > p.Limit+1 {
				out = out[:p.Limit+1]
			}
		}
		return nil
	})
	out, next := kernel.Trim(
		out,
		p.Limit,
		func(r port.APIRestriction) []string { return []string{strconv.FormatInt(r.Sequence, 10)} },
	)
	return out, next, err
}

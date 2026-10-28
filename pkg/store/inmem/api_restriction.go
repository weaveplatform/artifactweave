package inmem

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"

	"github.com/weaveplatform/artifactweave/pkg/store"
)

func (s *Store) APIRestrictions() store.APIRestrictionStore { return apiRestrictions{s} }

type apiRestrictions struct{ s *Store }

func (a apiRestrictions) current() *table[string, store.APIRestriction] {
	return tableOf[string, store.APIRestriction](a.s, "api_restrictions")
}

func (a apiRestrictions) audit() *table[int64, store.APIRestriction] {
	return tableOf[int64, store.APIRestriction](a.s, "api_restriction_audit")
}

func (a apiRestrictions) Get(ctx context.Context, user string) (store.APIRestriction, error) {
	var out store.APIRestriction
	err := a.s.do(ctx, func() error {
		var ok bool
		out, ok = a.current().get(user)
		if !ok {
			return notFound("API restriction", user)
		}
		return nil
	})
	return out, err
}

func (a apiRestrictions) Put(ctx context.Context, r store.APIRestriction) (store.APIRestriction, error) {
	if err := r.Validate(); err != nil {
		return r, err
	}
	err := a.s.do(ctx, func() error {
		if !a.s.usersT().has(r.User) {
			return notFound("restricted user", r.User)
		}
		r.Sequence = a.s.nextID("api_restriction_audit")
		r = store.Normalise(r)
		a.current().put(r.User, r)
		a.audit().put(r.Sequence, r)
		return nil
	})
	return r, err
}

func (a apiRestrictions) History(ctx context.Context, user string, p store.Page) ([]store.APIRestriction, string, error) {
	p, err := p.Normalize()
	if err != nil {
		return nil, "", err
	}
	key, err := p.Key(1)
	if err != nil {
		return nil, "", err
	}
	var after int64
	if len(key) != 0 {
		after, err = strconv.ParseInt(key[0], 10, 64)
		if err != nil || after < 1 {
			return nil, "", fmt.Errorf("restriction audit cursor: %w", store.ErrInvalidCursor)
		}
	}
	var out []store.APIRestriction
	err = a.s.do(ctx, func() error {
		for _, r := range a.audit().rows {
			if user != "" && r.User != user || after != 0 && r.Sequence >= after {
				continue
			}
			i, _ := slices.BinarySearchFunc(out, r, func(a, b store.APIRestriction) int { return cmp.Compare(b.Sequence, a.Sequence) })
			out = slices.Insert(out, i, r)
			if len(out) > p.Limit+1 {
				out = out[:p.Limit+1]
			}
		}
		return nil
	})
	out, next := store.Trim(out, p.Limit, func(r store.APIRestriction) []string { return []string{strconv.FormatInt(r.Sequence, 10)} })
	return out, next, err
}

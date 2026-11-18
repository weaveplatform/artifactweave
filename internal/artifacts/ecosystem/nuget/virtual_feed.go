package nuget

import (
	"container/heap"
	"context"
	"fmt"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

// FeedCursor supplies one complete readable ID at a time, sorted by ID. A
// complete group is needed to compute latest flags even when its first page
// contains none of its newest stable/prerelease versions.
type FeedCursor[T any] func(context.Context) (Package[T], bool, error)

// FeedEntry retains the whole version owner and flags over the visible merged
// ID, before OData predicates and client paging.
type FeedEntry[T any] struct {
	ID string
	Contribution[T]
	Latest, AbsoluteLatest bool
	// A page can disclose latest flags whose authoritative version is on
	// another page, so its delivery fence retains those owners too.
	LatestOwner, AbsoluteOwner T
}

// FeedPage retains a page only. Total is exact for Count requests; otherwise it
// is a lower bound sufficient for More, without scanning the remaining catalog.
type FeedPage[T any] struct {
	Entries []FeedEntry[T]
	Total   int
	More    bool
}

// FeedMerger performs a k-way merge over ordered member cursors. Metadata for
// the active ID in each member is complete, while returned entries are bounded
// by Take plus one lookahead. Request filtering follows ownership/shadowing.
type FeedMerger[T any] struct {
	// Observe retains count provenance without retaining a whole result page.
	Observe    func(FeedEntry[T])
	Skip, Take int
	Count      bool
	Allowed    func(repo.Config, Contribution[T]) (bool, error)
	Shows      func(T) bool
	Keep       func(FeedEntry[T]) bool
}

// Merge stops after its page and lookahead, or consumes every cursor for Count.
func (m FeedMerger[T]) Merge(ctx context.Context, parts []virtual.Part[FeedCursor[T]]) (FeedPage[T], error) {
	out := FeedPage[T]{Entries: []FeedEntry[T]{}}
	heads := &feedHeads[T]{}
	advance := func(index int) error {
		p, ok, err := parts[index].Doc(ctx)
		if err != nil {
			return fmt.Errorf("nuget feed member: %w", err)
		}
		if ok {
			heap.Push(heads, feedHead[T]{index: index, p: p})
		}
		return nil
	}
	if !m.Count && m.Take == 0 {
		return out, nil
	}
	for i := range parts {
		if err := advance(i); err != nil {
			return out, err
		}
	}
	for heads.Len() > 0 {
		if err := ctx.Err(); err != nil {
			return out, fmt.Errorf("nuget feed canceled: %w", err)
		}
		id := PackageName(heads.items[0].p.ID)
		group := []feedHead[T]{}
		for heads.Len() > 0 && PackageName(heads.items[0].p.ID) == id {
			group = append(group, heap.Pop(heads).(feedHead[T]))
		}
		merged, err := m.group(ctx, parts, group)
		if err != nil {
			return out, err
		}
		latest, absolute := "", ""
		var latestOwner, absoluteOwner T
		for _, v := range merged.Versions {
			absolute = v.Version
			absoluteOwner = v.Value
			parsed, _ := ParseVersion(v.Version)
			if len(parsed.Labels) == 0 {
				latest = v.Version
				latestOwner = v.Value
			}
		}
		for _, v := range merged.Versions {
			entry := FeedEntry[T]{
				ID:             id,
				Contribution:   v,
				Latest:         v.Version == latest,
				AbsoluteLatest: v.Version == absolute,
				LatestOwner:    latestOwner,
				AbsoluteOwner:  absoluteOwner,
			}
			if m.Keep != nil && !m.Keep(entry) {
				continue
			}
			if m.Observe != nil {
				m.Observe(entry)
			}
			out.Total++
			if m.Count || out.Total <= m.Skip {
				continue
			}
			if len(out.Entries) == m.Take {
				out.More = true
				return out, nil
			}
			out.Entries = append(out.Entries, entry)
		}
		for _, head := range group {
			if err := advance(head.index); err != nil {
				return out, err
			}
		}
	}
	return out, nil
}

func (m FeedMerger[T]) group(ctx context.Context, parts []virtual.Part[FeedCursor[T]], group []feedHead[T]) (Package[T], error) {
	priority := false
	for _, head := range group {
		if parts[head.index].Member.PriorityResolution.Bool() && len(head.p.Versions) > 0 {
			priority = true
		}
	}
	contributions := []virtual.Part[Package[T]]{}
	for _, head := range group {
		member := parts[head.index].Member
		if priority && !member.PriorityResolution.Bool() {
			continue
		}
		p := Package[T]{ID: head.p.ID}
		for _, v := range head.p.Versions {
			if m.Allowed != nil {
				ok, err := m.Allowed(member, v)
				if err != nil {
					return p, fmt.Errorf("nuget feed visibility: %w", err)
				}
				if !ok {
					continue
				}
			}
			p.Versions = append(p.Versions, v)
		}
		contributions = append(contributions, virtual.Part[Package[T]]{Member: member, Doc: p})
	}
	merged, err := (Merger[T]{}).Merge(ctx, contributions)
	if err != nil {
		return merged, err
	}
	// Ownership is settled before request filtering: an unlisted/mismatching
	// first copy must not reveal a later member's otherwise matching copy.
	visible := merged.Versions[:0]
	for _, v := range merged.Versions {
		if m.Shows == nil || m.Shows(v.Value) {
			visible = append(visible, v)
		}
	}
	merged.Versions = visible
	return merged, nil
}

type feedHead[T any] struct {
	index int
	p     Package[T]
}
type feedHeads[T any] struct{ items []feedHead[T] }

func (h feedHeads[T]) Len() int { return len(h.items) }
func (h feedHeads[T]) Less(i, j int) bool {
	a, b := h.items[i], h.items[j]
	cmp := strings.Compare(PackageName(a.p.ID), PackageName(b.p.ID))
	return cmp < 0 || cmp == 0 && a.index < b.index
}
func (h feedHeads[T]) Swap(i, j int) { h.items[i], h.items[j] = h.items[j], h.items[i] }
func (h *feedHeads[T]) Push(x any)   { h.items = append(h.items, x.(feedHead[T])) }
func (h *feedHeads[T]) Pop() any {
	last := len(h.items) - 1
	v := h.items[last]
	h.items[last] = feedHead[T]{}
	h.items = h.items[:last]
	return v
}

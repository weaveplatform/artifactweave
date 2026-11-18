package nuget_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/nuget"
)

type feedValue struct {
	tag   string
	shown bool
}

func feedPart(priority bool, packages ...nuget.Package[feedValue]) virtual.Part[nuget.FeedCursor[feedValue]] {
	at := 0
	return virtual.Part[nuget.FeedCursor[feedValue]]{
		Member: repo.Config{PriorityResolution: repo.Flex(priority)},
		Doc: func(context.Context) (nuget.Package[feedValue], bool, error) {
			if at == len(packages) {
				return nuget.Package[feedValue]{}, false, nil
			}
			p := packages[at]
			at++
			return p, true, nil
		},
	}
}

func feedPackage(id, tag string, versions ...string) nuget.Package[feedValue] {
	p := nuget.Package[feedValue]{ID: id}
	for _, v := range versions {
		p.Versions = append(p.Versions, nuget.Contribution[feedValue]{Version: v, Path: v, Value: feedValue{tag: tag, shown: true}})
	}
	return p
}

func TestUnitVirtualV2KWayOwnershipPriorityAndLatest(t *testing.T) {
	for _, mode := range []string{"duplicate", "priority-query-miss", "priority-virtual-miss", "unreadable-priority"} {
		t.Run(mode, func(t *testing.T) {
			a, b := feedPackage("Acme", "first", "1.0", "2.0.0-beta"), feedPackage("ACME", "second", "1.0.0+build", "3.0.0")
			parts := []virtual.Part[nuget.FeedCursor[feedValue]]{feedPart(false, a, feedPackage("Zeta", "last", "1.0.0")), feedPart(false, b)}
			merger := nuget.FeedMerger[feedValue]{Take: 100, Shows: func(v feedValue) bool { return v.shown }}
			if mode == "duplicate" {
				a.Versions[0].Value.shown = false
				parts[0] = feedPart(false, a, feedPackage("Zeta", "last", "1.0.0"))
			}
			if mode == "priority-query-miss" {
				a.Versions[0].Value.shown = false
				a.Versions[1].Value.shown = false
				parts[0] = feedPart(true, a)
			}
			if mode == "priority-virtual-miss" {
				parts[0] = feedPart(true, a)
				merger.Allowed = func(_ repo.Config, v nuget.Contribution[feedValue]) (bool, error) { return v.Value.tag != "first", nil }
			}
			if mode == "unreadable-priority" {
				parts[0] = feedPart(true, feedPackage("Acme", "unreadable"))
			}
			got, err := merger.Merge(t.Context(), parts)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "duplicate":
				if len(got.Entries) != 3 || got.Entries[0].Version != "2.0.0-beta" || got.Entries[0].Value.tag != "first" || !got.Entries[1].Latest ||
					!got.Entries[1].AbsoluteLatest ||
					got.Entries[0].AbsoluteOwner.tag != "second" {
					t.Fatalf("%+v", got)
				}
			case "priority-query-miss", "priority-virtual-miss":
				if len(got.Entries) != 0 {
					t.Fatal(got)
				}
			case "unreadable-priority":
				if len(got.Entries) != 2 || got.Entries[1].Version != "3.0.0" {
					t.Fatal(got)
				}
			}
		})
	}
}

func TestUnitVirtualV2PageLookaheadUsesCompleteActiveID(t *testing.T) {
	versions := []string{}
	for i := range 150 {
		versions = append(versions, fmt.Sprintf("1.0.%d", i))
	}
	versions = append(versions, "2.0.0-beta.1")
	makeParts := func() []virtual.Part[nuget.FeedCursor[feedValue]] {
		return []virtual.Part[nuget.FeedCursor[feedValue]]{feedPart(false, feedPackage("Acme", "owner", versions...)), feedPart(false, feedPackage("Other", "other", "1.0.0"))}
	}
	page, err := (nuget.FeedMerger[feedValue]{Take: 100}).Merge(t.Context(), makeParts())
	if err != nil || len(page.Entries) != 100 || !page.More || page.Total != 101 {
		t.Fatal(page, err)
	}
	for _, e := range page.Entries {
		if e.Latest || e.AbsoluteLatest || e.LatestOwner.tag != "owner" {
			t.Fatal(e)
		}
	}
	page, err = (nuget.FeedMerger[feedValue]{Skip: 100, Take: 100}).Merge(t.Context(), makeParts())
	if err != nil || len(page.Entries) != 52 || page.More || !page.Entries[49].Latest || !page.Entries[50].AbsoluteLatest {
		t.Fatal(page, err)
	}
	page, err = (nuget.FeedMerger[feedValue]{Skip: 999, Take: 1, Count: true}).Merge(t.Context(), makeParts())
	if err != nil || page.Total != 152 || len(page.Entries) != 0 {
		t.Fatal(page, err)
	}
	page, err = (nuget.FeedMerger[feedValue]{Take: 100, Keep: func(e nuget.FeedEntry[feedValue]) bool { return e.Latest }}).Merge(t.Context(), makeParts())
	if err != nil || len(page.Entries) != 2 || page.Entries[0].Version != "1.0.149" {
		t.Fatal(page, err)
	}
	calls := 0
	part := virtual.Part[nuget.FeedCursor[feedValue]]{Doc: func(context.Context) (nuget.Package[feedValue], bool, error) {
		calls++
		return feedPackage(fmt.Sprint(calls), "bounded", "1.0.0"), true, nil
	}}
	page, err = (nuget.FeedMerger[feedValue]{Take: 1}).Merge(t.Context(), []virtual.Part[nuget.FeedCursor[feedValue]]{part})
	if err != nil || calls != 2 || !page.More {
		t.Fatal(page, calls, err)
	}
	calls = 0
	_, err = (nuget.FeedMerger[feedValue]{}).Merge(t.Context(), []virtual.Part[nuget.FeedCursor[feedValue]]{part})
	if err != nil || calls != 0 {
		t.Fatal(calls, err)
	}
}

func TestUnitVirtualV2FeedPropagatesAuthorityAndCursorFaults(t *testing.T) {
	fault := errors.New("storage fault")
	for _, mode := range []string{"cursor", "allowed", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			parts := []virtual.Part[nuget.FeedCursor[feedValue]]{feedPart(false, feedPackage("Acme", "first", "1.0.0"))}
			m := nuget.FeedMerger[feedValue]{Take: 100}
			ctx := t.Context()
			if mode == "cursor" {
				parts[0].Doc = func(context.Context) (nuget.Package[feedValue], bool, error) {
					return nuget.Package[feedValue]{}, false, fault
				}
			}
			if mode == "allowed" {
				m.Allowed = func(repo.Config, nuget.Contribution[feedValue]) (bool, error) { return false, fault }
			}
			if mode == "canceled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			_, err := m.Merge(ctx, parts)
			if err == nil {
				t.Fatal("lost fault")
			}
		})
	}
}

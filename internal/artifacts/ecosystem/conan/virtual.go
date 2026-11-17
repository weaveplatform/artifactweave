package conan

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

// Revision identifies a Conan recipe or package revision and its publication time.
type Revision struct {
	Revision string    `json:"revision"`
	Time     time.Time `json:"time"`
}

// Merger unions revision lists, retaining the first member's duplicate before
// sorting newest first, as Conan's latest route requires (decision 0008).
type Merger struct{}

// Merge returns each revision once, newest first.
func (Merger) Merge(_ context.Context, parts []virtual.Part[[]Revision]) ([]Revision, error) {
	docs := make([][]Revision, len(parts))
	for i, p := range parts {
		docs[i] = p.Doc
	}
	out := virtual.UnionBy(docs, func(r Revision) string { return r.Revision })
	slices.SortFunc(out, func(a, b Revision) int {
		if n := b.Time.Compare(a.Time); n != 0 {
			return n
		}
		return strings.Compare(b.Revision, a.Revision)
	})
	return out, nil
}

// SearchMerger unions authorized recipe references in the native search order.
type SearchMerger struct{}

// Merge deduplicates references and sorts them lexically.
func (SearchMerger) Merge(_ context.Context, parts []virtual.Part[[]string]) ([]string, error) {
	docs := make([][]string, len(parts))
	for i, p := range parts {
		docs[i] = p.Doc
	}
	out := virtual.UnionBy(docs, func(s string) string { return s })
	slices.Sort(out)
	return out, nil
}

// PackageMerger retains the first member's information for each package id.
type PackageMerger struct{}

// Merge unions package searches without mutating the member documents.
func (PackageMerger) Merge(_ context.Context, parts []virtual.Part[map[string]json.RawMessage]) (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	for _, p := range parts {
		for id, raw := range p.Doc {
			if _, ok := out[id]; !ok {
				out[id] = slices.Clone(raw)
			}
		}
	}
	return out, nil
}

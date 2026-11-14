package dockerhttp

import (
	recency "container/list"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	layout "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/docker"
)

// maxRemoteTags is the most of a remote tags list a request decodes, the
// bound the remote's cache holds the joined upstream list to
// (RegistryContent.prepare).
const maxRemoteTags = 16 << 20

// remoteTagsBudget is how many bytes of decoded remote tags lists the
// registry keeps (tagLists). A list packs into at most its document's
// size and four bytes a tag, so the budget holds two of the largest the
// remote caches, and thousands of usual ones; a list over the budget is
// decoded for each request, as every list was before #1423.
const remoteTagsBudget = 64 << 20

// sortedTags is a remote tags list's tags, distinct and in Compare's order,
// packed into one string: a tag a page reads costs no allocation, and a
// list of short tags far less than a slice of strings would.
type sortedTags struct {
	data string
	ends []uint32
}

// packTags packs tags, sorted and distinct.
func packTags(tags []string) *sortedTags {
	var b strings.Builder
	n := 0
	for _, t := range tags {
		n += len(t)
	}
	b.Grow(n)
	ends := make([]uint32, len(tags))
	for i, t := range tags {
		b.WriteString(t)
		// A document is at most maxRemoteTags bytes, so every offset fits.
		ends[i] = uint32(b.Len()) //nolint:gosec // bounded by maxRemoteTags
	}
	return &sortedTags{data: b.String(), ends: ends}
}

func (s *sortedTags) len() int { return len(s.ends) }

func (s *sortedTags) at(i int) string {
	start := uint32(0)
	if i > 0 {
		start = s.ends[i-1]
	}
	return s.data[start:s.ends[i]]
}

// size is what the list holds in memory, as the budget counts it.
func (s *sortedTags) size() int64 { return int64(len(s.data)) + 4*int64(len(s.ends)) }

// tagCache keeps decoded remote tags lists by the SHA-256 of the document
// they were decoded from, the least recently read evicted first once their
// sizes pass the budget. The document under a SHA-256 never changes, so a
// list read from here is the one decoding it again would give, whichever
// repository, image or caller reads it. A nil cache keeps nothing. It is
// safe for concurrent use.
type tagCache struct {
	mu      sync.Mutex
	budget  int64
	used    int64
	order   *recency.List // of *tagEntry, the most recently read first
	entries map[string]*recency.Element
}

type tagEntry struct {
	key  string
	tags *sortedTags
}

func newTagCache(budget int64) *tagCache {
	return &tagCache{budget: budget, order: recency.New(), entries: map[string]*recency.Element{}}
}

func (c *tagCache) get(key string) (*sortedTags, bool) {
	if c == nil || key == "" {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(e)
	return e.Value.(*tagEntry).tags, true //nolint:forcetypeassert // the list holds only these
}

func (c *tagCache) put(key string, tags *sortedTags) {
	if c == nil || key == "" || tags.size() > c.budget {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok {
		// Two requests decoded the same document at once.
		c.order.MoveToFront(e)
		return
	}
	c.entries[key] = c.order.PushFront(&tagEntry{key: key, tags: tags})
	c.used += tags.size()
	for c.used > c.budget {
		e := c.order.Back()
		old := e.Value.(*tagEntry) //nolint:forcetypeassert // the list holds only these
		c.order.Remove(e)
		delete(c.entries, old.key)
		c.used -= old.tags.size()
	}
}

// remoteTagList is the remote tags list stream holds, sorted and distinct.
// The first request for a document decodes and sorts it whole; a later
// one, a later page of the same list above all, finds it by the document's
// SHA-256 and reads none of stream (#1423). Every stream a remote read
// returns carries that SHA-256, a cached item's and an upstream's alike;
// one without it is decoded each time. false is a document that does not
// decode, which is never kept.
func (g *Registry) remoteTagList(stream *source.Stream) (*sortedTags, bool) {
	key := stream.Checksums.SHA256
	if tags, ok := g.tagLists.get(key); ok {
		return tags, true
	}
	var listing struct {
		Tags []string `json:"tags"`
	}
	if err := json.NewDecoder(io.LimitReader(stream.Body, maxRemoteTags)).
		Decode(&listing); err != nil {
		return nil, false
	}
	slices.SortFunc(listing.Tags, layout.Compare)
	tags := packTags(slices.Compact(listing.Tags))
	g.tagLists.put(key, tags)
	return tags, true
}

// remoteTagsPage is the page of tags that the request's n and last select,
// and the Link to the next one: the tags after last that readable accepts,
// at most n of them, with a Link while another readable tag follows. n of
// zero gives an empty page without a Link (end-8b); an n that is no count
// gives every readable tag after last. It asks readable about each tag in
// order from last, and stops at the readable tag after the page's n-th, so
// a page costs the tags it reads, not the whole list.
//
// The answer is byte for byte the one the registry gave when it filtered
// the whole list by readable before it sorted and cut it. tags is in
// Compare's order, which is total and equal only for equal tags, and holds
// each tag once, so the readable tags found after last here are the ones
// after last in the filtered list, whether or not the caller may read last
// itself.
func remoteTagsPage(
	r *http.Request,
	tags *sortedTags,
	readable func(context.Context, string) bool,
) ([]string, string) {
	values := r.URL.Query()
	i := 0
	if last := values.Get("last"); last != "" {
		notBefore := func(j int) bool { return layout.Compare(tags.at(j), last) >= 0 }
		i = sort.Search(tags.len(), notBefore)
		if i < tags.len() && tags.at(i) == last {
			i++
		}
	}
	// An empty list is [] in JSON, never null.
	page := []string{}
	n, err := strconv.Atoi(values.Get("n"))
	whole := err != nil || n < 0
	if !whole && n == 0 {
		return page, ""
	}
	for ; i < tags.len(); i++ {
		tag := tags.at(i)
		if !readable(r.Context(), tag) {
			continue
		}
		if !whole && len(page) == n {
			return page, nextLink(r, n, page[n-1])
		}
		page = append(page, tag)
	}
	return page, ""
}

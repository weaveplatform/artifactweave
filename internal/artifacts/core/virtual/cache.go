package virtual

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// The bounds NewCache takes when CacheOptions leaves them unset, which are
// virtual.metadata_cache's defaults in the application configuration. They
// hold a few hundred large npm packuments or Helm indexes, and many more of
// the small documents most merges build.
const (
	DefaultCacheEntries       = 10000
	DefaultCacheBytes   int64 = 256 << 20
)

// CacheKey names one merged document in a Cache. Two requests share an
// entry only when every field is equal.
type CacheKey struct {
	// Virtual is the key of the virtual repository the request addressed.
	Virtual string
	// Document names the document and every input of its merge that the
	// other fields leave out, such as an Accept variant. PartOf names one
	// member's part of a document.
	Document string
	// Members is the virtual repository's resolved member list (Members),
	// so a member added or removed misses the entries built before.
	Members string
	// Visibility is what of those members the caller's walk reads, so two
	// callers who may read different members never share an entry.
	Visibility string
	// Base is the base URL a merged document points its files at
	// (APIBase), so a merge read through one host never answers another.
	Base string
}

// Entry is one merged document as the merge that built it answered it.
// Body is shared by every caller the entry answers: read it, never write
// to it.
type Entry struct {
	Body []byte
	// Header is the merge's answer's headers, Content-Type, ETag and
	// Last-Modified among them, so a hit answers with the headers of the
	// fresh merge it came from. Get hands each caller a copy.
	Header http.Header
	// Built is when the merge that built the entry started.
	Built time.Time
}

// PartOf is the Document of member's part of document, for a format that
// caches each remote member's part and merges it with its local members'
// fresh ones: Conda's docs say a virtual's metadata reflects local members
// "in real-time" while remote members' parts are renewed by the retrieval
// cache period (conda-repositories.md). Evict and Zap of document reach its
// parts too.
func PartOf(document, member string) string { return document + partSep + member }

// partSep joins a document and a member key in PartOf. No path or key
// holds a NUL byte, so a part never collides with another document.
const partSep = "\x00"

// within reports whether doc is root or one of root's parts.
func within(doc, root string) bool {
	return doc == root || strings.HasPrefix(doc, root+partSep)
}

// Period is the virtual repository v's virtualRetrievalCachePeriodSecs, or
// the stored default when v has none.
func Period(v repo.Config) time.Duration {
	secs := repo.DefaultVirtualRetrievalCachePeriod
	if v.VirtualRetrievalCachePeriodSecs != nil {
		secs = *v.VirtualRetrievalCachePeriodSecs
	}
	return time.Duration(secs) * time.Second
}

// CacheOptions size a Cache. A bound of zero or less takes its default.
type CacheOptions struct {
	// MaxEntries is the most documents the cache holds.
	MaxEntries int
	// MaxBytes is the most bytes the cached documents' bodies, headers and
	// keys add up to. A document larger than that is served and not kept.
	MaxBytes int64
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// Log receives the debug logs of builds, hits, expiries, evictions and
	// zaps; nil discards them.
	Log *slog.Logger
}

// Cache holds a process's merged virtual metadata for the virtual
// repositories' virtualRetrievalCachePeriodSecs (decision 0008, R8). 7.x
// keeps a virtual's merged metadata in the virtual's own storage and builds
// it again once it is older than the period; artifactweave keeps it in
// memory, bounded, least recently used out first. Coordination across
// nodes is P35's (remote-cache-policy.md). A nil *Cache caches nothing:
// Get builds on every call and the other methods do nothing.
type Cache struct {
	maxEntries int
	maxBytes   int64
	now        func() time.Time
	log        *slog.Logger

	mu sync.Mutex
	// lru holds *cached values, the most recently used at the front.
	lru     *list.List
	entries map[CacheKey]*list.Element
	bytes   int64
	// calls are the builds running, one per key (single flight).
	calls map[CacheKey]*call
	seq   uint64
}

// cached is one entry with its accounting. expired is a zapped entry,
// whose bytes stay as the fallback of a rebuild that fails.
type cached struct {
	key     CacheKey
	entry   Entry
	size    int64
	seq     uint64
	expired bool
}

// call is one running build, which every request for its key waits on.
// seq orders builds, so a build that finishes late never replaces the entry
// of one that started after it; evicted and zapped are an Evict or Zap that
// reached the key while it ran.
type call struct {
	done    chan struct{}
	seq     uint64
	started time.Time
	evicted bool
	zapped  bool
	entry   Entry
	err     error
}

// errBuildPanicked is what the requests that waited on a build get when it
// panicked; the panic itself goes on up the building request's stack.
var errBuildPanicked = errors.New("virtual: merge panicked")

// NewCache returns an empty cache bounded by o.
func NewCache(o CacheOptions) *Cache {
	if o.MaxEntries <= 0 {
		o.MaxEntries = DefaultCacheEntries
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = DefaultCacheBytes
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	return &Cache{
		maxEntries: o.MaxEntries,
		maxBytes:   o.MaxBytes,
		now:        o.Now,
		log:        o.Log,
		lru:        list.New(),
		entries:    map[CacheKey]*list.Element{},
		calls:      map[CacheKey]*call{},
	}
}

// Get returns the entry k names while it is no older than period, and
// otherwise build's, which it keeps. 7.x's
// VirtualRepo.isCachedResourceExpired (658-662, 7.161.26) expires an entry
// older than the period, so a negative period always expires; JFrog's page
// says "A value of 0 indicates no caching", so 0 builds on every call too.
// 7.x still saves what it builds then, and so does Get, so a period raised
// later starts from the latest merge.
//
// Concurrent calls for one key share one build. It runs on the first
// caller's goroutine with that caller's context values and without its
// cancellation, so no caller's cancellation cancels it; a caller whose
// context ends while it waits returns the context's error, and the build
// goes on for the others.
//
// A build that fails with a not-found (kernel.ErrNotFound, ErrPassOver or
// a 404 answer) drops k's entry and returns the error, as 7.x drops a
// virtual's cached resource when no member serves it any more
// (ArtifactoryVirtualDownloadStrategy.getInfoInternal 90-136). Another
// failure returns k's expired entry when it still has one, zapped or aged,
// since 7.x's zap only ages an entry (zapFileFromVirtual) and keeps its
// bytes "as a stale fallback if a rebuild fails" (PackageRepoServiceImpl
// 446-473); else the error.
func (c *Cache) Get(
	ctx context.Context,
	k CacheKey,
	period time.Duration,
	build func(context.Context) (Entry, error),
) (Entry, error) {
	if c == nil {
		return build(ctx)
	}
	c.mu.Lock()
	expired := false
	if el, ok := c.entries[k]; ok {
		it := el.Value.(*cached)
		if !it.expired && period > 0 && c.now().Sub(it.entry.Built) <= period {
			c.lru.MoveToFront(el)
			e := it.entry
			c.mu.Unlock()
			c.debug(ctx, "virtual metadata cache hit", k)
			return share(e), nil
		}
		expired = true
	}
	cl, joined := c.calls[k]
	if !joined {
		c.seq++
		cl = &call{done: make(chan struct{}), seq: c.seq, started: c.now()}
		c.calls[k] = cl
	}
	c.mu.Unlock()
	if expired {
		c.debug(ctx, "virtual metadata cache entry expired", k)
	}
	if !joined {
		c.run(context.WithoutCancel(ctx), k, cl, build)
		return share(cl.entry), cl.err
	}
	select {
	case <-cl.done:
		return share(cl.entry), cl.err
	case <-ctx.Done():
		return Entry{}, fmt.Errorf("virtual: wait for the merge of %s: %w", k.Virtual, ctx.Err())
	}
}

// run builds k for cl and finishes cl, also when build panics, so no
// waiter is left waiting.
func (c *Cache) run(
	ctx context.Context,
	k CacheKey,
	cl *call,
	build func(context.Context) (Entry, error),
) {
	c.debug(ctx, "virtual metadata cache build", k)
	finished := false
	defer func() {
		if !finished {
			c.finish(ctx, k, cl, Entry{}, errBuildPanicked)
		}
	}()
	e, err := build(ctx)
	finished = true
	c.finish(ctx, k, cl, e, err)
}

// finish keeps or drops what cl's build answered, as Get describes, hands
// the answer to cl's waiters and ends cl.
func (c *Cache) finish(ctx context.Context, k CacheKey, cl *call, e Entry, err error) {
	c.mu.Lock()
	if c.calls[k] == cl {
		delete(c.calls, k)
	}
	msg := ""
	switch {
	case err == nil:
		e.Built = cl.started
		e.Header = e.Header.Clone()
		if !cl.evicted {
			msg = c.store(k, e, cl.seq, cl.zapped)
		}
	case gone(err):
		if el, ok := c.entries[k]; ok && el.Value.(*cached).seq <= cl.seq {
			c.drop(el)
			msg = "virtual metadata cache entry dropped: no member serves it"
		}
	default:
		if el, ok := c.entries[k]; ok {
			e, err = el.Value.(*cached).entry, nil
			msg = "virtual metadata cache served an expired entry: its rebuild failed"
		}
	}
	cl.entry, cl.err = e, err
	c.mu.Unlock()
	close(cl.done)
	if msg != "" {
		c.debug(ctx, msg, k)
	}
}

// store keeps e under k, unless a build that started later already did,
// and then drops the least recently used entries until the cache is within
// its bounds. It returns what to log.
func (c *Cache) store(k CacheKey, e Entry, seq uint64, expired bool) string {
	if el, ok := c.entries[k]; ok {
		if el.Value.(*cached).seq > seq {
			return ""
		}
		c.drop(el)
	}
	size := sizeOf(k, e)
	if size > c.maxBytes {
		return "virtual metadata cache entry not kept: larger than the cache"
	}
	c.entries[k] = c.lru.PushFront(&cached{key: k, entry: e, size: size, seq: seq, expired: expired})
	c.bytes += size
	msg := ""
	for c.lru.Len() > c.maxEntries || c.bytes > c.maxBytes {
		c.drop(c.lru.Back())
		msg = "virtual metadata cache evicted its least recently used entries"
	}
	return msg
}

// drop removes el's entry.
func (c *Cache) drop(el *list.Element) {
	it := el.Value.(*cached)
	c.lru.Remove(el)
	delete(c.entries, it.key)
	c.bytes -= it.size
}

// Evict removes every entry of document, and of its parts, in the virtual
// repository virtual, whatever its members, visibility and base: 7.x's
// evictVirtualCache undeploys the document's whole cache folder, which
// holds one file per permission set (PackageRepoServiceImpl 435-473). A
// build running for one of them answers its callers and keeps nothing.
func (c *Cache) Evict(virtual, document string) {
	c.invalidate(virtual, document, false)
}

// Zap expires every entry of document, and of its parts, in the virtual
// repository virtual, keeping their bytes as the fallback of a rebuild that
// fails, as 7.x's zapVirtualCache does (PackageRepoServiceImpl 446-473). A
// build running for one of them keeps its answer expired.
func (c *Cache) Zap(virtual, document string) {
	c.invalidate(virtual, document, true)
}

func (c *Cache) invalidate(virtual, document string, zap bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	n := 0
	for el := c.lru.Front(); el != nil; {
		next := el.Next()
		if it := el.Value.(*cached); it.key.Virtual == virtual && within(it.key.Document, document) {
			n++
			if zap {
				it.expired = true
			} else {
				c.drop(el)
			}
		}
		el = next
	}
	// A request after this must not join a build that read the members
	// before it.
	for k, cl := range c.calls {
		if k.Virtual == virtual && within(k.Document, document) {
			cl.zapped, cl.evicted = cl.zapped || zap, cl.evicted || !zap
			delete(c.calls, k)
		}
	}
	c.mu.Unlock()
	msg := "virtual metadata cache evicted"
	if zap {
		msg = "virtual metadata cache zapped"
	}
	c.log.Debug(msg, "virtual", virtual, "document", document, "entries", n)
}

// Containing is what MemberWritten reads: repository configurations, and
// the virtual repositories that list a key as a member. port.RepoStore
// satisfies it.
type Containing interface {
	Repos
	Including(ctx context.Context, key string) ([]string, error)
}

// MemberWritten evicts, or with zap expires, document in every virtual
// repository of member's package type that contains member, through
// nesting, after a write to member. 7.x invalidates merged metadata on a
// member write only for Helm (RTFACT-25909), and its Helm handler calls
// this walk (PackageRepoServiceImpl undeployVirtualMetadataCache and
// zapVirtualMetadataCache 353-375, over getVirtualReposContainingRepo
// 477-491, which keeps the virtual repositories of the member's type among
// all that resolve to it). The walk goes up through virtual repositories
// of any type and visits each once, so a cycle ends it.
func (c *Cache) MemberWritten(
	ctx context.Context,
	rs Containing,
	member, document string,
	zap bool,
) error {
	if c == nil {
		return nil
	}
	m, err := rs.Get(ctx, member)
	if err != nil {
		return fmt.Errorf("virtual: member %s: %w", member, err)
	}
	seen := map[string]bool{member: true}
	for queue := []string{member}; len(queue) > 0; queue = queue[1:] {
		parents, err := rs.Including(ctx, queue[0])
		if err != nil {
			return fmt.Errorf("virtual: repositories including %s: %w", queue[0], err)
		}
		for _, key := range parents {
			if seen[key] {
				continue
			}
			seen[key] = true
			queue = append(queue, key)
			v, err := rs.Get(ctx, key)
			switch {
			case errors.Is(err, kernel.ErrNotFound):
				continue
			case err != nil:
				return fmt.Errorf("virtual: repository %s: %w", key, err)
			case v.PackageType != m.PackageType:
				continue
			}
			c.invalidate(key, document, zap)
		}
	}
	return nil
}

// gone reports whether a build's error says no member serves the document.
func gone(err error) bool {
	var answer *apierror.Error
	return errors.Is(err, kernel.ErrNotFound) || errors.Is(err, ErrPassOver) ||
		errors.As(err, &answer) && answer.Status == http.StatusNotFound
}

// sizeOf is what an entry counts against the cache's byte bound: its body,
// its headers and its key.
func sizeOf(k CacheKey, e Entry) int64 {
	n := len(e.Body) + len(k.Virtual) + len(k.Document) + len(k.Members) + len(k.Visibility) +
		len(k.Base)
	for name, values := range e.Header {
		n += len(name)
		for _, v := range values {
			n += len(v)
		}
	}
	return int64(n)
}

// share is e for one caller, with a header map of its own.
func share(e Entry) Entry {
	e.Header = e.Header.Clone()
	return e
}

func (c *Cache) debug(ctx context.Context, msg string, k CacheKey) {
	c.log.DebugContext(ctx, msg, "virtual", k.Virtual, "document", k.Document)
}

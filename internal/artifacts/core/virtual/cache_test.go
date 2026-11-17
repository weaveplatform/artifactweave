package virtual_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// The store is where MemberWritten finds the virtual repositories that
// contain a member.
var _ virtual.Containing = port.RepoStore(nil)

// clock is a fake clock the tests move by hand.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *clock { return &clock{now: time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// builder is a merge that counts its builds. Each answers "<name> <n>",
// name "build" by default, with a type and an ETag naming n, or err when
// it is set.
type builder struct {
	mu   sync.Mutex
	name string
	n    int
	err  error
}

func (b *builder) build(context.Context) (virtual.Entry, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.n++
	if b.err != nil {
		return virtual.Entry{}, b.err
	}
	name := b.name
	if name == "" {
		name = "build"
	}
	return virtual.Entry{
		Body: fmt.Appendf(nil, "%s %d", name, b.n),
		Header: http.Header{
			"Content-Type": {"application/json"}, "Etag": {fmt.Sprintf(`"%d"`, b.n)},
		},
	}, nil
}

func (b *builder) builds() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.n
}

func (b *builder) fail(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.err = err
}

func key(document string) virtual.CacheKey {
	return virtual.CacheKey{
		Virtual: "outer", Document: document, Members: "local-a,remote-a",
		Visibility: "all", Base: "https://a.example",
	}
}

// get is Get with t's context, failing t on an error.
func get(t *testing.T, c *virtual.Cache, k virtual.CacheKey, period time.Duration, b *builder) string {
	t.Helper()
	e, err := c.Get(t.Context(), k, period, b.build)
	if err != nil {
		t.Fatalf("get %q: %v", k.Document, err)
	}
	return string(e.Body)
}

// TestUnitCacheExpiry: within the period a second request for the same
// key is the first build's answer, with its headers; past it the merge is
// built again. A period of 0 or below builds on every request (7.x's
// isCachedResourceExpired; "A value of 0 indicates no caching"), and keeps
// the latest build, which a later positive period serves.
func TestUnitCacheExpiry(t *testing.T) {
	clk := newClock()
	var logs bytes.Buffer
	c := virtual.NewCache(virtual.CacheOptions{
		Now: clk.Now, Log: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	b := &builder{}
	if got := get(t, c, key("idx"), time.Hour, b); got != "build 1" {
		t.Fatalf("first: %s", got)
	}
	clk.advance(time.Hour)
	e, err := c.Get(t.Context(), key("idx"), time.Hour, b.build)
	if err != nil || string(e.Body) != "build 1" || e.Header.Get("ETag") != `"1"` ||
		e.Header.Get("Content-Type") != "application/json" || !e.Built.Equal(clk.Now().Add(-time.Hour)) {
		t.Fatalf("within the period: %+v %v", e, err)
	}
	// Each caller's headers are its own.
	e.Header.Set("ETag", "changed")
	clk.advance(time.Second)
	if got := get(t, c, key("idx"), time.Hour, b); got != "build 2" {
		t.Fatalf("past the period: %s", got)
	}
	for _, period := range []time.Duration{0, -time.Second} {
		before := b.builds()
		get(t, c, key("idx"), period, b)
		get(t, c, key("idx"), period, b)
		if b.builds() != before+2 {
			t.Errorf("period %s: %d builds, want 2", period, b.builds()-before)
		}
	}
	if got := get(t, c, key("idx"), time.Hour, b); got != "build 6" {
		t.Fatalf("a positive period after 0: %s", got)
	}
	for _, msg := range []string{"build", "hit", "entry expired"} {
		if !strings.Contains(logs.String(), "virtual metadata cache "+msg) {
			t.Errorf("no %q log in %s", msg, logs.String())
		}
	}
}

// TestUnitCacheKeys: every field of the key separates entries: two
// callers who may read different members, another member list, another
// base URL, another document and another virtual repository each build
// their own.
func TestUnitCacheKeys(t *testing.T) {
	c := virtual.NewCache(virtual.CacheOptions{})
	b := &builder{}
	k := key("idx")
	get(t, c, k, time.Hour, b)
	others := []func(*virtual.CacheKey){
		func(k *virtual.CacheKey) { k.Visibility = "local-a" },
		func(k *virtual.CacheKey) { k.Members = "local-a" },
		func(k *virtual.CacheKey) { k.Base = "https://b.example" },
		func(k *virtual.CacheKey) { k.Document = "other" },
		func(k *virtual.CacheKey) { k.Virtual = "inner" },
	}
	for i, change := range others {
		o := k
		change(&o)
		if got, want := get(t, c, o, time.Hour, b), fmt.Sprintf("build %d", i+2); got != want {
			t.Errorf("%+v: %s, want %s", o, got, want)
		}
	}
	if got := get(t, c, k, time.Hour, b); got != "build 1" {
		t.Errorf("the first key: %s", got)
	}
}

// TestUnitCacheGone: a build that finds the document gone from every
// member, as a miss, a pass-over or a 404 answer, drops the entry and
// answers its error, so the next request builds again.
func TestUnitCacheGone(t *testing.T) {
	clk := newClock()
	c := virtual.NewCache(virtual.CacheOptions{Now: clk.Now})
	for _, missing := range []error{
		kernel.ErrNotFound,
		fmt.Errorf("member: %w", virtual.ErrPassOver),
		apierror.New(http.StatusNotFound, "Could not find resource"),
	} {
		b := &builder{}
		get(t, c, key("idx"), time.Hour, b)
		clk.advance(2 * time.Hour)
		b.fail(missing)
		if _, err := c.Get(t.Context(), key("idx"), time.Hour, b.build); err != missing {
			t.Fatalf("%v: %v", missing, err)
		}
		b.fail(nil)
		if got := get(t, c, key("idx"), time.Hour, b); got != "build 3" {
			t.Errorf("%v: the entry was kept: %s", missing, got)
		}
		c.Evict("outer", "idx")
	}
}

// TestUnitCacheStaleFallback: a rebuild that fails otherwise answers the
// expired entry's bytes, whether age or Zap expired it, as 7.x serves a
// zapped virtual cache file when its rebuild fails; with no entry left it
// answers the error. The entry stays expired, so the next request tries
// again.
func TestUnitCacheStaleFallback(t *testing.T) {
	clk := newClock()
	c := virtual.NewCache(virtual.CacheOptions{Now: clk.Now})
	b := &builder{}
	fault := errors.New("storage fault")
	get(t, c, key("idx"), time.Hour, b)
	clk.advance(2 * time.Hour)
	b.fail(fault)
	if got := get(t, c, key("idx"), time.Hour, b); got != "build 1" {
		t.Fatalf("aged: %s", got)
	}
	b.fail(nil)
	if got := get(t, c, key("idx"), time.Hour, b); got != "build 3" {
		t.Fatalf("the stale entry stayed fresh: %s", got)
	}

	c.Zap("outer", "idx")
	b.fail(fault)
	if got := get(t, c, key("idx"), time.Hour, b); got != "build 3" {
		t.Fatalf("zapped: %s", got)
	}
	b.fail(nil)
	if got := get(t, c, key("idx"), time.Hour, b); got != "build 5" {
		t.Fatalf("after the zap: %s", got)
	}

	b.fail(fault)
	if _, err := c.Get(t.Context(), key("new"), time.Hour, b.build); err != fault {
		t.Fatalf("nothing to fall back on: %v", err)
	}
}

// TestUnitCacheEvictZap: Evict removes a document's entries of every
// member list, visibility and base, and its members' parts, in one virtual
// repository and no other document or repository's; Evict of one part
// removes that part alone. Zap expires the same entries and keeps their
// bytes.
func TestUnitCacheEvictZap(t *testing.T) {
	c := virtual.NewCache(virtual.CacheOptions{})
	b := &builder{}
	all := []virtual.CacheKey{
		key("idx"), key("idx"), key(virtual.PartOf("idx", "remote-a")),
		key(virtual.PartOf("idx", "remote-b")), key("idx/sub"), key("other"), key("idx"),
	}
	all[1].Visibility = "local-a"
	all[6].Virtual = "inner"
	fill := func() map[int]string {
		bodies := map[int]string{}
		for i, k := range all {
			bodies[i] = get(t, c, k, time.Hour, b)
		}
		return bodies
	}
	kept := func(bodies map[int]string, want ...int) {
		t.Helper()
		for i, k := range all {
			hit := get(t, c, k, time.Hour, b) == bodies[i]
			if hit != containsInt(want, i) {
				t.Errorf("%q in %s: hit %v", k.Document, k.Virtual, hit)
			}
		}
	}
	bodies := fill()
	c.Evict("outer", "idx")
	kept(bodies, 4, 5, 6)

	bodies = fill()
	c.Evict("outer", virtual.PartOf("idx", "remote-a"))
	kept(bodies, 0, 1, 3, 4, 5, 6)

	bodies = fill()
	c.Zap("outer", "idx")
	kept(bodies, 4, 5, 6)

	// A nil cache keeps nothing and drops nothing.
	var none *virtual.Cache
	none.Evict("outer", "idx")
	none.Zap("outer", "idx")
	before := b.builds()
	get(t, none, key("idx"), time.Hour, b)
	get(t, none, key("idx"), time.Hour, b)
	if b.builds() != before+2 {
		t.Errorf("a nil cache: %d builds", b.builds()-before)
	}
}

func containsInt(list []int, n int) bool {
	for _, v := range list {
		if v == n {
			return true
		}
	}
	return false
}

// TestUnitCacheMergeForms: a test merger's whole merge, cached, reads no
// member within the period and every member past it; a merger that keeps
// only the remote member's part reads the local member on every request
// and the remote's once per period, as Conda's renews remote members'
// parts alone.
func TestUnitCacheMergeForms(t *testing.T) {
	clk := newClock()
	c := virtual.NewCache(virtual.CacheOptions{Now: clk.Now})
	members := []virtual.Member{
		{Repo: local("local-a", false)},
		{Repo: remote("remote-a", false), Upstream: true},
	}
	docs := map[string]index{
		"local-a":  {Releases: []release{{Version: "v1.0.0"}}},
		"remote-a": {Releases: []release{{Version: "v1.0.0"}, {Version: "v2.0.0"}}},
	}
	reads := map[string]int{}
	read := func(_ context.Context, m virtual.Member) (index, error) {
		reads[m.Repo.Key]++
		return docs[m.Repo.Key], nil
	}
	merge := func(ctx context.Context, read func(context.Context, virtual.Member) (index, error)) (virtual.Entry, error) {
		parts, err := virtual.Collect(ctx, members, read)
		if err != nil {
			return virtual.Entry{}, err
		}
		doc, err := testMerger{base: "https://a.example", api: "testfmt", key: "outer"}.Merge(ctx, parts)
		if err != nil {
			return virtual.Entry{}, err
		}
		body, err := json.Marshal(doc)
		return virtual.Entry{Body: body}, err
	}
	whole := func() string {
		e, err := c.Get(t.Context(), key("idx"), time.Hour,
			func(ctx context.Context) (virtual.Entry, error) { return merge(ctx, read) })
		if err != nil {
			t.Fatal(err)
		}
		return string(e.Body)
	}
	parted := func() string {
		e, err := merge(t.Context(), func(ctx context.Context, m virtual.Member) (index, error) {
			if m.Repo.Class != repo.Remote {
				return read(ctx, m)
			}
			part, err := c.Get(ctx, key(virtual.PartOf("idx", m.Repo.Key)), time.Hour,
				func(ctx context.Context) (virtual.Entry, error) {
					doc, err := read(ctx, m)
					if err != nil {
						return virtual.Entry{}, err
					}
					body, err := json.Marshal(doc)
					return virtual.Entry{Body: body}, err
				})
			var doc index
			if err == nil {
				err = json.Unmarshal(part.Body, &doc)
			}
			return doc, err
		})
		if err != nil {
			t.Fatal(err)
		}
		return string(e.Body)
	}
	for _, form := range []struct {
		name  string
		merge func() string
		local int
	}{{"whole", whole, 1}, {"parts", parted, 2}} {
		clear(reads)
		first := form.merge()
		docs["remote-a"].Releases[1].Version = "v2.5.0"
		if second := form.merge(); second != first ||
			reads["local-a"] != form.local || reads["remote-a"] != 1 {
			t.Errorf("%s within the period: %v %s", form.name, reads, second)
		}
		if !strings.Contains(first, `"Latest":"v2.0.0"`) {
			t.Errorf("%s: %s", form.name, first)
		}
		clk.advance(2 * time.Hour)
		docs["remote-a"].Releases[1].Version = "v3.0.0"
		if third := form.merge(); !strings.Contains(third, `"Latest":"v3.0.0"`) ||
			reads["remote-a"] != 2 {
			t.Errorf("%s past the period: %v %s", form.name, reads, third)
		}
		docs["remote-a"].Releases[1].Version = "v2.0.0"
		clk.advance(2 * time.Hour)
	}
}

// containing is a store of configurations that answers Including from
// them, and fails the reads named in fail.
type containing struct {
	virtual.Configs
	fail map[string]error
}

func (s containing) Get(ctx context.Context, key string) (repo.Config, error) {
	if err := s.fail["get "+key]; err != nil {
		return repo.Config{}, err
	}
	return s.Configs.Get(ctx, key)
}

func (s containing) Including(_ context.Context, key string) ([]string, error) {
	if err := s.fail["including "+key]; err != nil {
		return nil, err
	}
	var out []string
	for k, c := range s.Configs {
		if c.Class == repo.Virtual && containsString(c.Repositories, key) {
			out = append(out, k)
		}
	}
	return out, nil
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// TestUnitCacheMemberWritten: a write to a member of inner, which outer
// lists, evicts both virtual repositories' entries of the document, or
// with zap expires them, through a generic virtual repository between
// them and through a cycle; a virtual repository of another package type
// keeps its own, as 7.x keeps only the member's type
// (getVirtualReposContainingRepo).
func TestUnitCacheMemberWritten(t *testing.T) {
	helm := func(c repo.Config) repo.Config { c.PackageType = repo.Helm; return c }
	rs := containing{Configs: configs(
		helm(local("helm-local", false)),
		helm(virt("inner", "helm-local", "outer")),
		virt("generic", "inner"),
		helm(virt("outer", "generic", "inner")),
		helm(virt("unrelated", "helm-other")),
	)}
	c := virtual.NewCache(virtual.CacheOptions{})
	b := &builder{}
	entries := func() map[string]string {
		bodies := map[string]string{}
		for _, v := range []string{"inner", "generic", "outer", "unrelated"} {
			k := key("index.yaml")
			k.Virtual = v
			bodies[v] = get(t, c, k, time.Hour, b)
		}
		return bodies
	}
	for _, zap := range []bool{false, true} {
		before := entries()
		if err := c.MemberWritten(t.Context(), rs, "helm-local", "index.yaml", zap); err != nil {
			t.Fatal(err)
		}
		after := entries()
		for v, rebuilt := range map[string]bool{
			"inner": true, "outer": true, "generic": false, "unrelated": false,
		} {
			if (after[v] != before[v]) != rebuilt {
				t.Errorf("zap %v: %s rebuilt %v", zap, v, !rebuilt)
			}
		}
	}

	fault := errors.New("store down")
	for name, fail := range map[string]map[string]error{
		"member":    {"get helm-local": fault},
		"including": {"including inner": fault},
		"virtual":   {"get outer": fault},
	} {
		rs.fail = fail
		if err := c.MemberWritten(t.Context(), rs, "helm-local", "index.yaml", false); !errors.Is(err, fault) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A virtual repository deleted while the walk ran is passed over.
	rs.fail = map[string]error{"get outer": kernel.ErrNotFound}
	if err := c.MemberWritten(t.Context(), rs, "helm-local", "index.yaml", false); err != nil {
		t.Error(err)
	}
	var none *virtual.Cache
	if err := none.MemberWritten(t.Context(), rs, "missing", "index.yaml", false); err != nil {
		t.Error(err)
	}
}

// TestUnitCacheSingleFlight: 50 concurrent misses for one key run one
// build, and each caller gets its answer.
func TestUnitCacheSingleFlight(t *testing.T) {
	c := virtual.NewCache(virtual.CacheOptions{})
	release := make(chan struct{})
	b := &builder{}
	build := func(ctx context.Context) (virtual.Entry, error) {
		<-release
		return b.build(ctx)
	}
	var started, done sync.WaitGroup
	bodies := make([]string, 50)
	errs := make([]error, 50)
	for i := range bodies {
		started.Add(1)
		done.Add(1)
		go func() {
			defer done.Done()
			started.Done()
			e, err := c.Get(context.Background(), key("idx"), time.Hour, build)
			bodies[i], errs[i] = string(e.Body), err
		}()
	}
	started.Wait()
	time.Sleep(20 * time.Millisecond)
	close(release)
	done.Wait()
	if b.builds() != 1 {
		t.Fatalf("%d builds, want 1", b.builds())
	}
	for i := range bodies {
		if errs[i] != nil || bodies[i] != "build 1" {
			t.Errorf("caller %d: %q %v", i, bodies[i], errs[i])
		}
	}
}

// TestUnitCacheCancel: a waiter whose context ends returns at once, and
// the build goes on; the building caller's own cancellation does not reach
// the build, which keeps its answer for the next request.
func TestUnitCacheCancel(t *testing.T) {
	c := virtual.NewCache(virtual.CacheOptions{})
	started, release := make(chan struct{}), make(chan struct{})
	b := &builder{}
	var buildErr error
	build := func(ctx context.Context) (virtual.Entry, error) {
		close(started)
		<-release
		buildErr = ctx.Err()
		return b.build(ctx)
	}
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leader := make(chan string)
	go func() {
		e, _ := c.Get(leaderCtx, key("idx"), time.Hour, build)
		leader <- string(e.Body)
	}()
	<-started
	cancelLeader()
	waiterCtx, cancelWaiter := context.WithCancel(context.Background())
	cancelWaiter()
	if _, err := c.Get(waiterCtx, key("idx"), time.Hour, b.build); !errors.Is(err, context.Canceled) {
		t.Fatalf("waiter: %v", err)
	}
	close(release)
	if got := <-leader; got != "build 1" || buildErr != nil {
		t.Fatalf("leader: %q, build saw %v", got, buildErr)
	}
	if got := get(t, c, key("idx"), time.Hour, b); got != "build 1" {
		t.Fatalf("after: %s", got)
	}
}

// TestUnitCachePanic: a build that panics fails its waiters and panics on
// the building caller's stack, and keeps nothing.
func TestUnitCachePanic(t *testing.T) {
	c := virtual.NewCache(virtual.CacheOptions{})
	started, release := make(chan struct{}), make(chan struct{})
	recovered := make(chan any)
	go func() {
		defer func() { recovered <- recover() }()
		_, _ = c.Get(context.Background(), key("idx"), time.Hour,
			func(context.Context) (virtual.Entry, error) {
				close(started)
				<-release
				panic("merge bug")
			})
	}()
	<-started
	waiter := make(chan error)
	own := &builder{}
	go func() {
		_, err := c.Get(context.Background(), key("idx"), time.Hour, own.build)
		waiter <- err
	}()
	time.Sleep(20 * time.Millisecond)
	close(release)
	if p := <-recovered; p != "merge bug" {
		t.Fatalf("panic: %v", p)
	}
	// A waiter that joined fails; one the scheduler held back until after
	// the panic built for itself.
	if err := <-waiter; own.builds() == 0 && (err == nil || !strings.Contains(err.Error(), "panicked")) {
		t.Fatalf("waiter: %v", err)
	}
	if own.builds() == 0 {
		b := &builder{name: "after"}
		if got := get(t, c, key("idx"), time.Hour, b); got != "after 1" {
			t.Fatalf("the panicked build kept %s", got)
		}
	}
}

// TestUnitCacheLateBuild: a build that an Evict reaches answers its
// callers and keeps nothing; one a Zap reaches keeps its answer expired,
// and never replaces an entry a later build kept.
func TestUnitCacheLateBuild(t *testing.T) {
	for _, zap := range []bool{false, true} {
		c := virtual.NewCache(virtual.CacheOptions{})
		started, release := make(chan struct{}), make(chan struct{})
		late := &builder{name: "late"}
		first := make(chan string)
		go func() {
			e, _ := c.Get(context.Background(), key("idx"), time.Hour,
				func(ctx context.Context) (virtual.Entry, error) {
					close(started)
					<-release
					return late.build(ctx)
				})
			first <- string(e.Body)
		}()
		<-started
		if zap {
			c.Zap("outer", "idx")
		} else {
			c.Evict("outer", "idx")
		}
		// A request after the invalidation builds anew rather than joining.
		b := &builder{}
		if got := get(t, c, key("idx"), time.Hour, b); got != "build 1" {
			t.Fatalf("zap %v: the next request: %s", zap, got)
		}
		close(release)
		if got := <-first; got != "late 1" {
			t.Fatalf("zap %v: the late build answered %s", zap, got)
		}
		if got := get(t, c, key("idx"), time.Hour, b); got != "build 1" || b.builds() != 1 {
			t.Fatalf("zap %v: the late build replaced the later one: %s", zap, got)
		}
	}
	// With nothing kept since, the zapped late build's answer stays as an
	// expired fallback.
	c := virtual.NewCache(virtual.CacheOptions{})
	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = c.Get(context.Background(), key("idx"), time.Hour,
			func(ctx context.Context) (virtual.Entry, error) {
				close(started)
				<-release
				return (&builder{}).build(ctx)
			})
	}()
	<-started
	c.Zap("outer", "idx")
	close(release)
	<-done
	b := &builder{err: errors.New("fault")}
	if got := get(t, c, key("idx"), time.Hour, b); got != "build 1" || b.builds() != 1 {
		t.Fatalf("the zapped answer: %s after %d builds", got, b.builds())
	}
}

// TestUnitCacheBound: the cache never holds more entries or bytes than its
// bounds, drops the least recently used first, and does not keep a
// document larger than the whole cache.
func TestUnitCacheBound(t *testing.T) {
	c := virtual.NewCache(virtual.CacheOptions{MaxEntries: 3})
	b := &builder{}
	for i := range 5 {
		get(t, c, key(fmt.Sprint(i)), time.Hour, b)
		if i == 2 {
			get(t, c, key("0"), time.Hour, b) // 0 is used again, so 1 goes first
		}
		if n, _ := c.Usage(); n > 3 {
			t.Fatalf("%d entries", n)
		}
	}
	for doc, want := range map[string]string{"0": "build 1", "3": "build 4", "4": "build 5"} {
		if got := get(t, c, key(doc), time.Hour, b); got != want {
			t.Errorf("%s: %s, want %s", doc, got, want)
		}
	}
	if got := get(t, c, key("1"), time.Hour, b); got != "build 6" {
		t.Errorf("1 was kept: %s", got)
	}

	one := int64(len(key("0").Virtual+key("0").Members+key("0").Visibility+key("0").Base) +
		1 + len("build 1") + len("Content-Type") + len("application/json") + len("Etag") + 3)
	c = virtual.NewCache(virtual.CacheOptions{MaxBytes: 2*one + 1})
	b = &builder{}
	for i := range 5 {
		get(t, c, key(fmt.Sprint(i)), time.Hour, b)
		if n, size := c.Usage(); n > 2 || size > 2*one+1 {
			t.Fatalf("%d entries, %d bytes", n, size)
		}
	}
	c = virtual.NewCache(virtual.CacheOptions{MaxBytes: 4})
	b = &builder{}
	get(t, c, key("idx"), time.Hour, b)
	if got := get(t, c, key("idx"), time.Hour, b); got != "build 2" {
		t.Errorf("a document larger than the cache was kept: %s", got)
	}
	if n, size := c.Usage(); n != 0 || size != 0 {
		t.Errorf("%d entries, %d bytes", n, size)
	}
}

// TestUnitPeriod: a virtual repository's period is its
// virtualRetrievalCachePeriodSecs, or the stored default.
func TestUnitPeriod(t *testing.T) {
	if got := virtual.Period(virt("v")); got != 2*time.Hour {
		t.Errorf("default: %s", got)
	}
	secs := -1
	if got := virtual.Period(repo.Config{VirtualRetrievalCachePeriodSecs: &secs}); got != -time.Second {
		t.Errorf("-1: %s", got)
	}
}

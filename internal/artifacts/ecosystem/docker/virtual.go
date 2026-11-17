package docker

import (
	recency "container/list"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
)

// ImageWriteConfig admits an image write before the registry expands it into
// upload and digest files. Internal names must not be checked as image names
// again (RTFACT-17858). The returned copy is only for this admitted write;
// persisted configuration and every other repository policy stay unchanged.
func ImageWriteConfig(c repo.Config, image string) (repo.Config, error) {
	if err := generic.CheckBlackedOut(c, image); err != nil {
		return c, err //nolint:wrapcheck // the policy owns its refusal
	}
	if err := generic.CheckPatterns(c, image); err != nil {
		return c, err //nolint:wrapcheck // the policy owns its refusal
	}
	c.IncludesPattern, c.ExcludesPattern = nil, nil
	return c, nil
}

// VirtualImport is a reference a virtual push must also hold in its DDR.
// A subject is deliberately absent: OCI permits pushing it before its subject.
type VirtualImport struct {
	Descriptor
	Manifest bool
}

// VirtualImports plans children before blobs, once per digest and kind. The
// registry's existing presence/size validation remains authoritative afterwards.
func VirtualImports(m Manifest) []VirtualImport {
	var out []VirtualImport
	seen := map[string]bool{}
	for _, group := range []struct {
		descriptors []Descriptor
		manifest    bool
	}{{m.Manifests, true}, {m.BlobDescriptors(), false}} {
		for _, d := range group.descriptors {
			key := fmt.Sprintf("%t:%s", group.manifest, d.Digest)
			if !seen[key] {
				out = append(out, VirtualImport{Descriptor: d, Manifest: group.manifest})
				seen[key] = true
			}
		}
	}
	return out
}

// VirtualMembers retains the engine's download order, including separate remote
// cache/upstream steps. The revision includes nested configuration so changing
// membership or patterns invalidates every location hint for the virtual.
func VirtualMembers(ctx context.Context, rs virtual.Repos, c repo.Config, path string) ([]virtual.Member, string, error) {
	recording := &recordRepos{Repos: rs, seen: map[string]repo.Config{c.Key: c}}
	members, err := virtual.Members(ctx, recording, c, virtual.Options{Order: virtual.Resolve, Path: path, NoUpstream: remote.NoOriginUpstream(ctx, c)})
	if err != nil {
		return nil, "", fmt.Errorf("docker virtual members: %w", err)
	}
	body, err := json.Marshal(recording.seen)
	if err != nil {
		return nil, "", fmt.Errorf("docker virtual configuration: %w", err)
	}
	return members, fmt.Sprintf("%x", sha256.Sum256(body)), nil
}

type recordRepos struct {
	virtual.Repos
	seen map[string]repo.Config
}

func (r *recordRepos) Get(ctx context.Context, key string) (repo.Config, error) {
	c, err := r.Repos.Get(ctx, key)
	if err == nil {
		r.seen[key] = c
	}
	return c, err //nolint:wrapcheck // the engine wraps its repository lookup
}

// MemberLocation is a hint, never authority: every hit still checks the current
// member, caller, download policy and source before serving anything.
type MemberLocation struct {
	Key      string
	Upstream bool
}

type (
	locationKey   struct{ virtual, kind, digest string }
	locationEntry struct {
		key    locationKey
		member MemberLocation
		until  time.Time
	}
)

// MemberLocations bounds digest hints to 10,000 entries for 24 hours, matching
// 7.161.26's docker.virtual.{blob,manifest}.cache defaults. Tags never enter it.
// It is safe for concurrent use and holds no request or permission state.
type MemberLocations struct {
	mu        sync.Mutex
	limit     int
	ttl       time.Duration
	now       func() time.Time
	entries   map[locationKey]*recency.Element
	order     *recency.List
	revisions map[string]string
	counts    map[string]int
}

// NewMemberLocations uses the 7.x cache bounds and a wall clock.
func NewMemberLocations() *MemberLocations {
	return &MemberLocations{
		limit:     10000,
		ttl:       24 * time.Hour,
		now:       time.Now,
		entries:   map[locationKey]*recency.Element{},
		order:     recency.New(),
		revisions: map[string]string{},
		counts:    map[string]int{},
	}
}

// Get returns a current hint after checking the virtual configuration revision.
func (c *MemberLocations) Get(v, kind, digest, revision string) (MemberLocation, bool) {
	if c == nil {
		return MemberLocation{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.observe(v, revision)
	el, ok := c.entries[locationKey{v, kind, digest}]
	if !ok {
		return MemberLocation{}, false
	}
	e := el.Value.(*locationEntry) //nolint:forcetypeassert // only locationEntry is inserted
	if !c.now().Before(e.until) {
		c.remove(el)
		return MemberLocation{}, false
	}
	c.order.MoveToFront(el)
	return e.member, true
}

// Put records a digest holder, evicting the least recently used hint at capacity.
func (c *MemberLocations) Put(v, kind, digest, revision string, member MemberLocation) {
	if c == nil {
		return
	}
	if _, ok := SHA256Hex(digest); !ok {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.observe(v, revision)
	key := locationKey{v, kind, digest}
	if el, ok := c.entries[key]; ok {
		c.remove(el)
	}
	for len(c.entries) >= c.limit {
		c.remove(c.order.Back())
	}
	e := &locationEntry{key: key, member: member, until: c.now().Add(c.ttl)}
	c.entries[key] = c.order.PushFront(e)
	c.revisions[v] = revision
	c.counts[v]++
}

// Drop forgets a holder that no longer serves the requested digest.
func (c *MemberLocations) Drop(v, kind, digest string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[locationKey{v, kind, digest}]; ok {
		c.remove(el)
	}
}

func (c *MemberLocations) observe(v, revision string) {
	if before, ok := c.revisions[v]; !ok || before == revision {
		return
	}
	for _, el := range c.entries {
		if el.Value.(*locationEntry).key.virtual == v {
			c.remove(el)
		} //nolint:forcetypeassert // only locationEntry is inserted
	}
}

func (c *MemberLocations) remove(el *recency.Element) {
	e := el.Value.(*locationEntry) //nolint:forcetypeassert // only locationEntry is inserted
	delete(c.entries, e.key)
	c.order.Remove(el)
	c.counts[e.key.virtual]--
	if c.counts[e.key.virtual] == 0 {
		delete(c.counts, e.key.virtual)
		delete(c.revisions, e.key.virtual)
	}
}

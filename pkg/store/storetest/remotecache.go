package storetest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func runRemoteCache(t *testing.T, newStore Factory) {
	t.Run("RemoteCache", func(t *testing.T) { testRemoteCache(t, newStore(t)) })
	t.Run("RemoteCacheFences", func(t *testing.T) { testRemoteFences(t, newStore(t)) })
	t.Run("RemoteCacheAtomic", func(t *testing.T) { testRemoteAtomic(t, newStore(t)) })
	t.Run("RemoteCacheEvict", func(t *testing.T) { testRemoteEvict(t, newStore(t)) })
	t.Run("RemoteCacheConcurrent", func(t *testing.T) { testRemoteConcurrent(t, newStore(t)) })
	t.Run("RemoteCacheUsage", func(t *testing.T) { testRemoteUsage(t, newStore(t)) })
	t.Run("RemoteSmartState", func(t *testing.T) { testRemoteSmartState(t, newStore(t)) })
	t.Run("RemoteLayoutFences", func(t *testing.T) { testRemoteLayoutFences(t, newStore(t)) })
}

func testRemoteSmartState(t *testing.T, s store.Store) {
	ctx, c, key := remoteFixture(t, s)
	cache := s.RemoteCache()
	token := get[types.RemoteCacheToken](t)(cache.Begin(ctx, key))
	e := remoteEntry(token)
	e.Kind, e.MetadataBaseURL = types.RemoteUnique, ""
	it := get[types.Item](
		t,
	)(
		s.Items().
			Put(ctx, types.Item{RepoKey: c.Key, Cached: true, Name: key.Path, Actual: e.Blob.Checksums, Size: e.Blob.Size}),
	)
	e.ItemID = it.ID
	get[types.Blob](t)(s.Blobs().Ref(ctx, e.Blob, 1))
	e.Smart = &types.RemoteSmartState{
		OwnedProperties: types.Properties{"source": {"value"}},
		LocalProperties: map[string]bool{"deleted": true},
		PropertiesAt:    at(1),
		OriginAt:        at(1),
		OriginAbsent:    true,
		Downloads:       4,
		Claimed:         3,
		Uncertain:       1,
	}
	e = get[types.RemoteCacheEntry](t)(cache.Put(ctx, token, e))
	same(t, "persisted Smart state", e, get[types.RemoteCacheEntry](t)(cache.Read(ctx, key)))
	copy := get[types.RemoteCacheEntry](t)(cache.Read(ctx, key))
	copy.Smart.OwnedProperties["source"][0] = "mutated"
	same(t, "independent Smart snapshot", e, get[types.RemoteCacheEntry](t)(cache.Read(ctx, key)))
	for _, variant := range []types.RemoteCacheKey{{RepoKey: c.Key, Path: key.Path, Variant: "second"}, {RepoKey: c.Key, Path: key.Path + "/child"}, {RepoKey: c.Key, Path: key.Path + "x"}} {
		tok := get[types.RemoteCacheToken](t)(cache.Begin(ctx, variant))
		get[types.RemoteCacheEntry](t)(cache.Put(ctx, tok, remoteEntry(tok)))
	}
	page, next, err := cache.Exact(ctx, c.Key, key.Path, store.Page{Limit: 1})
	must(t, err)
	if len(page) != 1 || next == "" || page[0].Token.Key.Path != key.Path {
		t.Fatal("exact path first page", page, next)
	}
	page, next, err = cache.Exact(ctx, c.Key, key.Path, store.Page{Limit: 1, After: next})
	must(t, err)
	if len(page) != 1 || next != "" || page[0].Token.Key.Path != key.Path {
		t.Fatal("exact path included descendants", page, next)
	}
	_, _, err = cache.Exact(ctx, c.Key, "../escape", store.Page{})
	wantErr(t, err, store.ErrInvalid)
	for _, change := range []func(*types.RemoteSmartState){func(s *types.RemoteSmartState) { s.Claimed = 5 }, func(s *types.RemoteSmartState) { s.Uncertain = 4 }, func(s *types.RemoteSmartState) { s.Downloads = -1 }, func(s *types.RemoteSmartState) { s.OwnedProperties = types.Properties{"bad;key": {"value"}} }, func(s *types.RemoteSmartState) { s.LocalProperties = map[string]bool{"bad;key": true} }} {
		bad := store.Clone(e)
		change(bad.Smart)
		_, err = cache.Put(ctx, e.Token, bad)
		wantErr(t, err, store.ErrInvalid)
	}
	_, err = cache.Invalidate(ctx, c.Key, key.Path, true)
	must(t, err)
	after := get[types.RemoteCacheEntry](t)(cache.Read(ctx, key))
	if !after.Smart.PropertiesAt.IsZero() || !after.Smart.OriginAt.IsZero() ||
		after.Smart.Downloads != 4 ||
		after.Smart.Claimed != 3 ||
		after.Smart.Uncertain != 1 ||
		after.Blob != e.Blob {
		t.Fatal("zap discarded Smart counters or content", after)
	}
	wantErr(t, cache.Check(ctx, e.Token), store.ErrConflict)
}

func remoteFixture(
	t *testing.T,
	s store.Store,
) (context.Context, repo.Config, types.RemoteCacheKey) {
	t.Helper()
	ctx := context.Background()
	c := repo.Config{
		Key:         "upstream",
		Class:       repo.Remote,
		PackageType: repo.Generic,
		URL:         "https://source.example",
	}
	must(t, s.Repos().Create(ctx, c))
	return ctx, c, types.RemoteCacheKey{
		RepoKey:          c.Key,
		Path:             "pkg/index.json",
		Variant:          "json",
		SettingsRevision: "secret-revision-1",
	}
}

func remoteEntry(token types.RemoteCacheToken) types.RemoteCacheEntry {
	return types.RemoteCacheEntry{
		Token: token,
		Kind:  types.RemoteMetadata,
		Blob: types.Blob{
			Checksums: types.Checksums{SHA256: strings.Repeat("a", 64)},
			Size:      12,
			Created:   at(0),
		},
		MetadataBaseURL: "https://source.example/redirected/index/",
		ETag:            "\"upstream-1\"",
		LastModified:    "Wed, 21 Oct 2026 12:00:00 GMT",
		ContentType:     "application/json",
		CacheControl:    "must-revalidate",
		Vary:            "Accept",
		FetchedAt:       at(1),
		ValidatedAt:     at(1),
		LastUsed:        at(2),
	}
}

func testRemoteCache(t *testing.T, s store.Store) {
	ctx, c, k := remoteFixture(t, s)
	cache := s.RemoteCache()
	source := get[types.SourceIdentity](t)(cache.Source(ctx, c.Key))
	if source.Repository == "" || source.Generation == "" || source.ID != c.Key {
		t.Fatalf("source = %+v", source)
	}
	token := get[types.RemoteCacheToken](t)(cache.Begin(ctx, k))
	_, err := cache.Read(ctx, k)
	wantErr(t, err, store.ErrNotFound)
	wantErr(t, cache.Revalidate(ctx, token, at(1)), store.ErrInvalid)
	wantErr(t, cache.Touch(ctx, token, at(1)), store.ErrNotFound)
	e := remoteEntry(token)
	saved := get[types.RemoteCacheEntry](t)(cache.Put(ctx, token, e))
	e.Token.Revision++
	same(t, "committed metadata", e, saved)
	_, err = cache.Put(ctx, token, e)
	wantErr(t, err, store.ErrConflict)
	got := get[types.RemoteCacheEntry](t)(cache.Read(ctx, k))
	same(t, "read", saved, got)
	got.ETag = "mutated"
	same(t, "returned copy", saved, get[types.RemoteCacheEntry](t)(cache.Read(ctx, k)))
	b := get[types.Blob](t)(s.Blobs().Get(ctx, e.Blob.Checksums.SHA256))
	if b.Refs != 1 {
		t.Fatalf("raw refs=%d", b.Refs)
	}
	must(t, cache.Negative(ctx, saved.Token, at(8)))
	saved = get[types.RemoteCacheEntry](t)(cache.Read(ctx, k))
	if saved.Blob.Checksums != e.Blob.Checksums || saved.NegativeUntil.IsZero() {
		t.Fatal("negative destroyed good body")
	}
	must(t, cache.Revalidate(ctx, saved.Token, at(9)))
	saved = get[types.RemoteCacheEntry](t)(cache.Read(ctx, k))
	if !saved.NegativeUntil.IsZero() || !saved.ValidatedAt.Equal(store.Normalise(at(9))) {
		t.Fatal("304 validation state")
	}
	must(t, cache.Touch(ctx, saved.Token, at(10)))
	saved = get[types.RemoteCacheEntry](t)(cache.Read(ctx, k))
	must(t, cache.Touch(ctx, saved.Token, at(3)))
	saved = get[types.RemoteCacheEntry](t)(cache.Read(ctx, k))
	if !saved.LastUsed.Equal(store.Normalise(at(10))) {
		t.Fatal("activity clock went backwards")
	}
	for _, variant := range []types.RemoteCacheKey{{RepoKey: c.Key, Path: k.Path, Variant: "html", SettingsRevision: k.SettingsRevision}, {RepoKey: c.Key, Path: k.Path, Variant: k.Variant, SettingsRevision: "rotated"}} {
		_, err = cache.Read(ctx, variant)
		wantErr(t, err, store.ErrNotFound)
	}
	n, err := cache.Invalidate(ctx, c.Key, "pkg", true)
	must(t, err)
	if n != 1 {
		t.Fatalf("zap=%d", n)
	}
	got = get[types.RemoteCacheEntry](t)(cache.Read(ctx, k))
	if !got.ValidatedAt.IsZero() || got.Blob.Checksums != e.Blob.Checksums {
		t.Fatal("zap deleted metadata")
	}
	wantErr(t, cache.Check(ctx, saved.Token), store.ErrConflict)
	_, err = cache.Invalidate(ctx, c.Key, "pkg", false)
	must(t, err)
	_, err = cache.Read(ctx, k)
	wantErr(t, err, store.ErrNotFound)
	b = get[types.Blob](t)(s.Blobs().Get(ctx, e.Blob.Checksums.SHA256))
	if b.Refs != 0 {
		t.Fatal("raw metadata ref leaked")
	}
	token = get[types.RemoteCacheToken](t)(cache.Begin(ctx, k))
	must(t, cache.Negative(ctx, token, at(12)))
	_, err = cache.Invalidate(ctx, c.Key, k.Path, true)
	must(t, err)
	_, err = cache.Read(ctx, k)
	wantErr(t, err, store.ErrNotFound)
	_, err = cache.Begin(ctx, types.RemoteCacheKey{RepoKey: c.Key, Path: "../escape"})
	wantErr(t, err, store.ErrInvalid)
	_, err = cache.Read(ctx, types.RemoteCacheKey{RepoKey: c.Key, Path: "/path"})
	wantErr(t, err, store.ErrInvalid)
	_, err = cache.Invalidate(ctx, c.Key, "../escape", false)
	wantErr(t, err, store.ErrInvalid)
	token = get[types.RemoteCacheToken](t)(cache.Begin(ctx, k))
	e = remoteEntry(token)
	e.Kind = "invalid"
	_, err = cache.Put(ctx, token, e)
	wantErr(t, err, store.ErrInvalid)
	for _, bad := range []string{"https://user:password@example.com/index", "https://example.com/index?secret=value", "https://example.com/index?", "https://example.com/index#fragment", "file:///tmp/index", "https://%"} {
		e.Kind = types.RemoteMetadata
		e.MetadataBaseURL = bad
		_, err = cache.Put(ctx, token, e)
		wantErr(t, err, store.ErrInvalid)
	}
	e.MetadataBaseURL = ""
	e.Kind = types.RemoteUnique
	e.ItemID = -1
	_, err = cache.Put(ctx, token, e)
	wantErr(t, err, store.ErrInvalid)
}

func testRemoteFences(t *testing.T, s store.Store) {
	ctx, c, k := remoteFixture(t, s)
	cache := s.RemoteCache()
	token := get[types.RemoteCacheToken](t)(cache.Begin(ctx, k))
	c.Description = "cosmetic"
	must(t, s.Repos().Update(ctx, c))
	must(t, cache.Check(ctx, token))
	c.Offline = contentPtr(true)
	must(t, s.Repos().Update(ctx, c))
	source := get[types.SourceIdentity](t)(cache.Source(ctx, c.Key))
	if source != token.Source {
		t.Fatal("offline changed content identity")
	}
	wantErr(t, cache.Check(ctx, token), store.ErrConflict)
	token = get[types.RemoteCacheToken](t)(cache.Begin(ctx, k))
	c.Offline = contentPtr(false)
	must(t, s.Repos().Update(ctx, c))
	token = get[types.RemoteCacheToken](t)(cache.Begin(ctx, k))
	old := token
	c.URL = "https://other.example"
	must(t, s.Repos().Update(ctx, c))
	c.URL = "https://source.example"
	must(t, s.Repos().Update(ctx, c))
	source = get[types.SourceIdentity](t)(cache.Source(ctx, c.Key))
	if source.Generation == old.Source.Generation || source.Repository != old.Source.Repository {
		t.Fatal("source ABA not fenced")
	}
	wantErr(t, cache.Check(ctx, old), store.ErrConflict)
	token = get[types.RemoteCacheToken](t)(cache.Begin(ctx, k))
	e := get[types.RemoteCacheEntry](t)(cache.Put(ctx, token, remoteEntry(token)))
	c.Password = "rotated"
	must(t, s.Repos().Update(ctx, c))
	token = get[types.RemoteCacheToken](t)(cache.Begin(ctx, k))
	must(t, cache.Negative(ctx, token, at(5)))
	got := get[types.RemoteCacheEntry](t)(cache.Read(ctx, k))
	if got.Blob.Checksums.SHA256 != "" {
		t.Fatal("new source negative reused old source bytes")
	}
	b := get[types.Blob](t)(s.Blobs().Get(ctx, e.Blob.Checksums.SHA256))
	if b.Refs != 0 {
		t.Fatal("source change raw refs")
	}
	state := get[types.RemoteSourceState](t)(cache.State(ctx, c.Key))
	state.ConnectionFailures = 9
	state.OfflineUntil = at(20)
	state.RetryAfterUntil = at(21)
	must(t, cache.PutState(ctx, c.Key, state))
	wantErr(t, cache.PutState(ctx, c.Key, state), store.ErrConflict)
	state.Revision++
	same(t, "cooldown state", state, get[types.RemoteSourceState](t)(cache.State(ctx, c.Key)))
	state.ConnectionFailures = -1
	wantErr(t, cache.PutState(ctx, c.Key, state), store.ErrInvalid)
	token = get[types.RemoteCacheToken](t)(cache.Begin(ctx, k))
	must(t, s.Repos().Delete(ctx, c.Key))
	wantErr(t, cache.Check(ctx, token), store.ErrConflict)
	must(t, s.Repos().Create(ctx, c))
	source = get[types.SourceIdentity](t)(cache.Source(ctx, c.Key))
	if source.Repository == token.Source.Repository {
		t.Fatal("recreated lifetime reused")
	}
	_, err := cache.Put(ctx, token, remoteEntry(token))
	wantErr(t, err, store.ErrConflict)
	_, err = cache.Source(ctx, "missing")
	wantErr(t, err, store.ErrNotFound)
	_, err = cache.Begin(ctx, types.RemoteCacheKey{RepoKey: "missing", Path: "p"})
	wantErr(t, err, store.ErrNotFound)
}

func testRemoteAtomic(t *testing.T, s store.Store) {
	ctx, c, k := remoteFixture(t, s)
	cache := s.RemoteCache()
	token := get[types.RemoteCacheToken](t)(cache.Begin(ctx, k))
	boom := errors.New("rollback cache transaction")
	err := s.Tx(ctx, func(ctx context.Context) error {
		_, err := cache.Put(ctx, token, remoteEntry(token))
		if err != nil {
			return err
		}
		return boom
	})
	wantErr(t, err, boom)
	_, err = cache.Read(ctx, k)
	wantErr(t, err, store.ErrNotFound)
	_, err = s.Blobs().Get(ctx, strings.Repeat("a", 64))
	wantErr(t, err, store.ErrNotFound)
	// Item-backed data gets exactly the reference the caller's item transaction owns.
	e := remoteEntry(token)
	e.Kind = types.RemoteUnique
	e.MetadataBaseURL = ""
	it := get[types.Item](
		t,
	)(
		s.Items().
			Put(ctx, types.Item{RepoKey: c.Key, Cached: true, Name: k.Path, Actual: e.Blob.Checksums, Size: e.Blob.Size}),
	)
	e.ItemID = it.ID
	get[types.Blob](t)(s.Blobs().Ref(ctx, e.Blob, 1))
	e = get[types.RemoteCacheEntry](t)(cache.Put(ctx, token, e))
	b := get[types.Blob](t)(s.Blobs().Get(ctx, e.Blob.Checksums.SHA256))
	if b.Refs != 1 {
		t.Fatal("double reference for item")
	}
	n, err := cache.Invalidate(ctx, c.Key, "pkg", true)
	must(t, err)
	if n != 0 {
		t.Fatal("zap invalidated unique bytes")
	}
	must(t, cache.Check(ctx, e.Token))
	// Beginning an uncached path is enough for a delete to fence its future completion.
	other := k
	other.Path = "pkg/new.bin"
	pending := get[types.RemoteCacheToken](t)(cache.Begin(ctx, other))
	_, err = cache.Invalidate(ctx, c.Key, "pkg", false)
	must(t, err)
	wantErr(t, cache.Check(ctx, pending), store.ErrConflict)
	// Sibling prefix is isolated and a bad item reference cannot publish.
	sibling := k
	sibling.Path = "pkgs/a"
	tok := get[types.RemoteCacheToken](t)(cache.Begin(ctx, sibling))
	_, err = cache.Invalidate(ctx, c.Key, "pkg", false)
	must(t, err)
	must(t, cache.Check(ctx, tok))
	bad := remoteEntry(tok)
	bad.ItemID = it.ID
	bad.Blob.Checksums.SHA256 = strings.Repeat("b", 64)
	_, err = cache.Put(ctx, tok, bad)
	wantErr(t, err, store.ErrInvalid)
	bad.ItemID = 999999
	_, err = cache.Put(ctx, tok, bad)
	wantErr(t, err, store.ErrNotFound)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = cache.Begin(cancelled, k)
	wantErr(t, err, context.Canceled)
	// Pagination includes tombstones and escapes wildcard prefixes literally.
	for _, path := range []string{"percent%/a", "percent_/b", "percent%/c", "Percent%/upper", "é/child", "É/other"} {
		key := k
		key.Path = path
		get[types.RemoteCacheToken](t)(cache.Begin(ctx, key))
	}
	listed := pages(t, 1, func(p store.Page) ([]types.RemoteCacheEntry, string, error) {
		return cache.List(ctx, c.Key, "percent%", p)
	})
	if len(listed) != 2 {
		t.Fatalf("literal prefix list: %d", len(listed))
	}
	unicodeRows, _, err := cache.List(ctx, c.Key, "é", store.Page{})
	must(t, err)
	if len(unicodeRows) != 1 || unicodeRows[0].Token.Key.Path != "é/child" {
		t.Fatal("Unicode prefix mismatched", unicodeRows)
	}
	_, _, err = cache.List(ctx, c.Key, "", store.Page{After: "%%%"})
	wantErr(t, err, store.ErrInvalidCursor)
	// Repository deletion releases raw metadata independently of item storage.
	fresh := get[types.RemoteCacheToken](t)(cache.Begin(ctx, k))
	raw := remoteEntry(fresh)
	raw.Blob.Checksums.SHA256 = strings.Repeat("c", 64)
	get[types.RemoteCacheEntry](t)(cache.Put(ctx, fresh, raw))
	must(t, s.Repos().Delete(ctx, c.Key))
	b = get[types.Blob](t)(s.Blobs().Get(ctx, raw.Blob.Checksums.SHA256))
	if b.Refs != 0 {
		t.Fatal("repository deletion leaked raw metadata")
	}
}

func testRemoteConcurrent(t *testing.T, s store.Store) {
	ctx, c, k := remoteFixture(t, s)
	cache := s.RemoteCache()
	token := get[types.RemoteCacheToken](t)(cache.Begin(ctx, k))
	start := make(chan struct{})
	done := make(chan error, 2)
	for _, digest := range []string{strings.Repeat("a", 64), strings.Repeat("b", 64)} {
		go func() {
			<-start
			e := remoteEntry(token)
			e.Blob.Checksums.SHA256 = digest
			_, err := cache.Put(ctx, token, e)
			done <- err
		}()
	}
	close(start)
	successes, conflicts := 0, 0
	for range 2 {
		err := <-done
		switch {
		case err == nil:
			successes++
		case errors.Is(err, store.ErrConflict):
			conflicts++
		default:
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent publication successes=%d conflicts=%d", successes, conflicts)
	}
	saved := get[types.RemoteCacheEntry](t)(cache.Read(ctx, k))
	for _, digest := range []string{strings.Repeat("a", 64), strings.Repeat("b", 64)} {
		ref, err := s.Blobs().Get(ctx, digest)
		if digest == saved.Blob.Checksums.SHA256 {
			must(t, err)
			if ref.Refs != 1 {
				t.Fatal("winner reference", ref)
			}
		} else if !errors.Is(err, store.ErrNotFound) {
			t.Fatal("loser published reference", ref, err)
		}
	}
	// More than the internal invalidation page size must all be fenced. Keep
	// setup in one transaction; no external work runs inside it.
	var pending types.RemoteCacheToken
	must(t, s.Tx(ctx, func(ctx context.Context) error {
		for n := range 135 {
			key := k
			key.Path = fmt.Sprintf("batch/%03d", n)
			var err error
			pending, err = cache.Begin(ctx, key)
			if err != nil {
				return err
			}
		}
		return nil
	}))
	n, err := cache.Invalidate(ctx, c.Key, "batch", false)
	must(t, err)
	if n != 135 {
		t.Fatalf("paged invalidation changed %d", n)
	}
	wantErr(t, cache.Check(ctx, pending), store.ErrConflict)
}

func testRemoteUsage(t *testing.T, s store.Store) {
	ctx, c, key := remoteFixture(t, s)
	cache := s.RemoteCache()
	token := get[types.RemoteCacheToken](t)(cache.Begin(ctx, key))
	saved := get[types.RemoteCacheEntry](t)(cache.Put(ctx, token, remoteEntry(token)))
	pending := get[types.RemoteCacheToken](t)(cache.Begin(ctx, key))
	before := get[types.Blob](t)(s.Blobs().Get(ctx, saved.Blob.Checksums.SHA256))
	must(t, cache.Touch(ctx, saved.Token, at(10)))
	must(t, cache.Check(ctx, pending))
	touched := get[types.RemoteCacheEntry](t)(cache.Read(ctx, key))
	same(t, "usage preserves content token", saved.Token, touched.Token)
	same(
		t,
		"usage preserves raw reference",
		before,
		get[types.Blob](t)(s.Blobs().Get(ctx, saved.Blob.Checksums.SHA256)),
	)
	if !touched.LastUsed.Equal(store.Normalise(at(10))) {
		t.Fatal("usage not recorded")
	}
	// A stale snapshot carried by a refresh cannot erase newer successful usage.
	saved.ETag = "replacement"
	saved.LastUsed = at(3)
	refreshed := get[types.RemoteCacheEntry](t)(cache.Put(ctx, pending, saved))
	if refreshed.ETag != "replacement" || !refreshed.LastUsed.Equal(store.Normalise(at(10))) {
		t.Fatal("refresh lost usage", refreshed)
	}
	must(t, cache.Touch(ctx, refreshed.Token, at(4)))
	same(
		t,
		"out-of-order usage preserves content",
		refreshed,
		get[types.RemoteCacheEntry](t)(cache.Read(ctx, key)),
	)
	// Independently completed readers may report timestamps in either order.
	pending = get[types.RemoteCacheToken](t)(cache.Begin(ctx, key))
	start := make(chan struct{})
	done := make(chan error, 2)
	for _, n := range []int{30, 20} {
		go func() { <-start; done <- cache.Touch(ctx, refreshed.Token, at(n)) }()
	}
	close(start)
	for range 2 {
		must(t, <-done)
	}
	must(t, cache.Check(ctx, pending))
	got := get[types.RemoteCacheEntry](t)(cache.Read(ctx, key))
	same(t, "concurrent usage preserves token", refreshed.Token, got.Token)
	if !got.LastUsed.Equal(store.Normalise(at(30))) {
		t.Fatal("concurrent activity regressed")
	}
	// Real content publication and source rotation still fence obsolete usage.
	wantErr(t, cache.Touch(ctx, saved.Token, at(40)), store.ErrConflict)
	c.Password = "rotated"
	must(t, s.Repos().Update(ctx, c))
	wantErr(t, cache.Touch(ctx, got.Token, at(40)), store.ErrConflict)
	current := get[types.RemoteCacheToken](t)(cache.Begin(ctx, key))
	wantErr(t, cache.Touch(ctx, current, at(40)), store.ErrConflict)
	same(
		t,
		"stale source usage did not mutate entry",
		got,
		get[types.RemoteCacheEntry](t)(cache.Read(ctx, key)),
	)
}

func testRemoteEvict(t *testing.T, s store.Store) {
	ctx, _, k := remoteFixture(t, s)
	cache := s.RemoteCache()
	token := get[types.RemoteCacheToken](t)(cache.Begin(ctx, k))
	original := get[types.RemoteCacheEntry](t)(cache.Put(ctx, token, remoteEntry(token)))
	other := k
	other.Variant = "other"
	alternate := get[types.RemoteCacheToken](t)(cache.Begin(ctx, other))
	alternateEntry := get[types.RemoteCacheEntry](
		t,
	)(
		cache.Put(ctx, alternate, remoteEntry(alternate)),
	)
	must(t, cache.Evict(ctx, original.Token))
	wantErr(t, cache.Evict(ctx, original.Token), store.ErrConflict)
	_, err := cache.Read(ctx, k)
	wantErr(t, err, store.ErrNotFound)
	same(
		t,
		"other representation",
		alternateEntry,
		get[types.RemoteCacheEntry](t)(cache.Read(ctx, other)),
	)
	b := get[types.Blob](t)(s.Blobs().Get(ctx, original.Blob.Checksums.SHA256))
	if b.Refs != 1 {
		t.Fatal("eviction reference count", b.Refs)
	}
}

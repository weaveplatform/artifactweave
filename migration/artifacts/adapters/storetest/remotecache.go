package storetest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/blob"
	"github.com/weaveplatform/artifactweave/migration/platform/testkit/storekit"
)

func runRemoteCache(t *testing.T, newStore Factory) {
	t.Run("RemoteCache", func(t *testing.T) { testRemoteCache(t, newStore(t)) })
	t.Run("RemoteCacheFences", func(t *testing.T) { testRemoteFences(t, newStore(t)) })
	t.Run("RemoteCacheAtomic", func(t *testing.T) { testRemoteAtomic(t, newStore(t)) })
	t.Run("RemoteCacheEvict", func(t *testing.T) { testRemoteEvict(t, newStore(t)) })
	t.Run("RemoteCacheConcurrent", func(t *testing.T) { testRemoteConcurrent(t, newStore(t)) })
	t.Run("RemoteCacheUsage", func(t *testing.T) { testRemoteUsage(t, newStore(t)) })
	t.Run("RemoteSmartState", func(t *testing.T) { testRemoteSmartState(t, newStore(t)) })
}

func testRemoteSmartState(t *testing.T, s port.Store) {
	ctx, c, key := remoteFixture(t, s)
	cache := s.RemoteCache()
	token := storekit.Get[model.RemoteCacheToken](t)(cache.Begin(ctx, key))
	e := remoteEntry(token)
	e.Kind, e.MetadataBaseURL = model.RemoteUnique, ""
	it := storekit.Get[model.Item](
		t,
	)(
		s.Items().
			Put(ctx, model.Item{RepoKey: c.Key, Cached: true, Name: key.Path, Actual: e.Blob.Checksums, Size: e.Blob.Size}),
	)
	e.ItemID = it.ID
	storekit.Get[blob.Blob](t)(s.Blobs().Ref(ctx, e.Blob, 1))
	e.Smart = &model.RemoteSmartState{
		OwnedProperties: model.Properties{"source": {"value"}},
		LocalProperties: map[string]bool{"deleted": true},
		PropertiesAt:    storekit.At(1),
		OriginAt:        storekit.At(1),
		OriginAbsent:    true,
		Downloads:       4,
		Claimed:         3,
		Uncertain:       1,
	}
	e = storekit.Get[model.RemoteCacheEntry](t)(cache.Put(ctx, token, e))
	storekit.Same(
		t,
		"persisted Smart state",
		e,
		storekit.Get[model.RemoteCacheEntry](t)(cache.Read(ctx, key)),
	)
	copy := storekit.Get[model.RemoteCacheEntry](t)(cache.Read(ctx, key))
	copy.Smart.OwnedProperties["source"][0] = "mutated"
	storekit.Same(
		t,
		"independent Smart snapshot",
		e,
		storekit.Get[model.RemoteCacheEntry](t)(cache.Read(ctx, key)),
	)
	for _, variant := range []model.RemoteCacheKey{{RepoKey: c.Key, Path: key.Path, Variant: "second"}, {RepoKey: c.Key, Path: key.Path + "/child"}, {RepoKey: c.Key, Path: key.Path + "x"}} {
		tok := storekit.Get[model.RemoteCacheToken](t)(cache.Begin(ctx, variant))
		storekit.Get[model.RemoteCacheEntry](t)(cache.Put(ctx, tok, remoteEntry(tok)))
	}
	page, next, err := cache.Exact(ctx, c.Key, key.Path, kernel.Page{Limit: 1})
	storekit.Must(t, err)
	if len(page) != 1 || next == "" || page[0].Token.Key.Path != key.Path {
		t.Fatal("exact path first page", page, next)
	}
	page, next, err = cache.Exact(ctx, c.Key, key.Path, kernel.Page{Limit: 1, After: next})
	storekit.Must(t, err)
	if len(page) != 1 || next != "" || page[0].Token.Key.Path != key.Path {
		t.Fatal("exact path included descendants", page, next)
	}
	_, _, err = cache.Exact(ctx, c.Key, "../escape", kernel.Page{})
	storekit.WantErr(t, err, kernel.ErrInvalid)
	for _, change := range []func(*model.RemoteSmartState){func(s *model.RemoteSmartState) { s.Claimed = 5 }, func(s *model.RemoteSmartState) { s.Uncertain = 4 }, func(s *model.RemoteSmartState) { s.Downloads = -1 }, func(s *model.RemoteSmartState) { s.OwnedProperties = model.Properties{"bad;key": {"value"}} }, func(s *model.RemoteSmartState) { s.LocalProperties = map[string]bool{"bad;key": true} }} {
		bad := kernel.Clone(e)
		change(bad.Smart)
		_, err = cache.Put(ctx, e.Token, bad)
		storekit.WantErr(t, err, kernel.ErrInvalid)
	}
	_, err = cache.Invalidate(ctx, c.Key, key.Path, true)
	storekit.Must(t, err)
	after := storekit.Get[model.RemoteCacheEntry](t)(cache.Read(ctx, key))
	if !after.Smart.PropertiesAt.IsZero() || !after.Smart.OriginAt.IsZero() ||
		after.Smart.Downloads != 4 ||
		after.Smart.Claimed != 3 ||
		after.Smart.Uncertain != 1 ||
		after.Blob != e.Blob {
		t.Fatal("zap discarded Smart counters or content", after)
	}
	storekit.WantErr(t, cache.Check(ctx, e.Token), kernel.ErrConflict)
}

func remoteFixture(
	t *testing.T,
	s port.Store,
) (context.Context, repo.Config, model.RemoteCacheKey) {
	t.Helper()
	ctx := context.Background()
	c := repo.Config{
		Key:         "upstream",
		Class:       repo.Remote,
		PackageType: repo.Generic,
		URL:         "https://source.example",
	}
	storekit.Must(t, s.Repos().Create(ctx, c))
	return ctx, c, model.RemoteCacheKey{
		RepoKey:          c.Key,
		Path:             "pkg/index.json",
		Variant:          "json",
		SettingsRevision: "secret-revision-1",
	}
}

func remoteEntry(token model.RemoteCacheToken) model.RemoteCacheEntry {
	return model.RemoteCacheEntry{
		Token: token,
		Kind:  model.RemoteMetadata,
		Blob: blob.Blob{
			Checksums: kernel.Checksums{SHA256: strings.Repeat("a", 64)},
			Size:      12,
			Created:   storekit.At(0),
		},
		MetadataBaseURL: "https://source.example/redirected/index/",
		ETag:            "\"upstream-1\"",
		LastModified:    "Wed, 21 Oct 2026 12:00:00 GMT",
		ContentType:     "application/json",
		CacheControl:    "must-revalidate",
		Vary:            "Accept",
		FetchedAt:       storekit.At(1),
		ValidatedAt:     storekit.At(1),
		LastUsed:        storekit.At(2),
	}
}

func testRemoteCache(t *testing.T, s port.Store) {
	ctx, c, k := remoteFixture(t, s)
	cache := s.RemoteCache()
	source := storekit.Get[model.SourceIdentity](t)(cache.Source(ctx, c.Key))
	if source.Repository == "" || source.Generation == "" || source.ID != c.Key {
		t.Fatalf("source = %+v", source)
	}
	token := storekit.Get[model.RemoteCacheToken](t)(cache.Begin(ctx, k))
	_, err := cache.Read(ctx, k)
	storekit.WantErr(t, err, kernel.ErrNotFound)
	storekit.WantErr(t, cache.Revalidate(ctx, token, storekit.At(1)), kernel.ErrInvalid)
	storekit.WantErr(t, cache.Touch(ctx, token, storekit.At(1)), kernel.ErrNotFound)
	e := remoteEntry(token)
	saved := storekit.Get[model.RemoteCacheEntry](t)(cache.Put(ctx, token, e))
	e.Token.Revision++
	storekit.Same(t, "committed metadata", e, saved)
	_, err = cache.Put(ctx, token, e)
	storekit.WantErr(t, err, kernel.ErrConflict)
	got := storekit.Get[model.RemoteCacheEntry](t)(cache.Read(ctx, k))
	storekit.Same(t, "read", saved, got)
	got.ETag = "mutated"
	storekit.Same(
		t,
		"returned copy",
		saved,
		storekit.Get[model.RemoteCacheEntry](t)(cache.Read(ctx, k)),
	)
	b := storekit.Get[blob.Blob](t)(s.Blobs().Get(ctx, e.Blob.Checksums.SHA256))
	if b.Refs != 1 {
		t.Fatalf("raw refs=%d", b.Refs)
	}
	storekit.Must(t, cache.Negative(ctx, saved.Token, storekit.At(8)))
	saved = storekit.Get[model.RemoteCacheEntry](t)(cache.Read(ctx, k))
	if saved.Blob.Checksums != e.Blob.Checksums || saved.NegativeUntil.IsZero() {
		t.Fatal("negative destroyed good body")
	}
	storekit.Must(t, cache.Revalidate(ctx, saved.Token, storekit.At(9)))
	saved = storekit.Get[model.RemoteCacheEntry](t)(cache.Read(ctx, k))
	if !saved.NegativeUntil.IsZero() || !saved.ValidatedAt.Equal(kernel.Normalise(storekit.At(9))) {
		t.Fatal("304 validation state")
	}
	storekit.Must(t, cache.Touch(ctx, saved.Token, storekit.At(10)))
	saved = storekit.Get[model.RemoteCacheEntry](t)(cache.Read(ctx, k))
	storekit.Must(t, cache.Touch(ctx, saved.Token, storekit.At(3)))
	saved = storekit.Get[model.RemoteCacheEntry](t)(cache.Read(ctx, k))
	if !saved.LastUsed.Equal(kernel.Normalise(storekit.At(10))) {
		t.Fatal("activity clock went backwards")
	}
	for _, variant := range []model.RemoteCacheKey{{RepoKey: c.Key, Path: k.Path, Variant: "html", SettingsRevision: k.SettingsRevision}, {RepoKey: c.Key, Path: k.Path, Variant: k.Variant, SettingsRevision: "rotated"}} {
		_, err = cache.Read(ctx, variant)
		storekit.WantErr(t, err, kernel.ErrNotFound)
	}
	n, err := cache.Invalidate(ctx, c.Key, "pkg", true)
	storekit.Must(t, err)
	if n != 1 {
		t.Fatalf("zap=%d", n)
	}
	got = storekit.Get[model.RemoteCacheEntry](t)(cache.Read(ctx, k))
	if !got.ValidatedAt.IsZero() || got.Blob.Checksums != e.Blob.Checksums {
		t.Fatal("zap deleted metadata")
	}
	storekit.WantErr(t, cache.Check(ctx, saved.Token), kernel.ErrConflict)
	_, err = cache.Invalidate(ctx, c.Key, "pkg", false)
	storekit.Must(t, err)
	_, err = cache.Read(ctx, k)
	storekit.WantErr(t, err, kernel.ErrNotFound)
	b = storekit.Get[blob.Blob](t)(s.Blobs().Get(ctx, e.Blob.Checksums.SHA256))
	if b.Refs != 0 {
		t.Fatal("raw metadata ref leaked")
	}
	token = storekit.Get[model.RemoteCacheToken](t)(cache.Begin(ctx, k))
	storekit.Must(t, cache.Negative(ctx, token, storekit.At(12)))
	_, err = cache.Invalidate(ctx, c.Key, k.Path, true)
	storekit.Must(t, err)
	_, err = cache.Read(ctx, k)
	storekit.WantErr(t, err, kernel.ErrNotFound)
	_, err = cache.Begin(ctx, model.RemoteCacheKey{RepoKey: c.Key, Path: "../escape"})
	storekit.WantErr(t, err, kernel.ErrInvalid)
	_, err = cache.Read(ctx, model.RemoteCacheKey{RepoKey: c.Key, Path: "/path"})
	storekit.WantErr(t, err, kernel.ErrInvalid)
	_, err = cache.Invalidate(ctx, c.Key, "../escape", false)
	storekit.WantErr(t, err, kernel.ErrInvalid)
	token = storekit.Get[model.RemoteCacheToken](t)(cache.Begin(ctx, k))
	e = remoteEntry(token)
	e.Kind = "invalid"
	_, err = cache.Put(ctx, token, e)
	storekit.WantErr(t, err, kernel.ErrInvalid)
	for _, bad := range []string{"https://user:password@example.com/index", "https://example.com/index?secret=value", "https://example.com/index?", "https://example.com/index#fragment", "file:///tmp/index", "https://%"} {
		e.Kind = model.RemoteMetadata
		e.MetadataBaseURL = bad
		_, err = cache.Put(ctx, token, e)
		storekit.WantErr(t, err, kernel.ErrInvalid)
	}
	e.MetadataBaseURL = ""
	e.Kind = model.RemoteUnique
	e.ItemID = -1
	_, err = cache.Put(ctx, token, e)
	storekit.WantErr(t, err, kernel.ErrInvalid)
}

func testRemoteFences(t *testing.T, s port.Store) {
	ctx, c, k := remoteFixture(t, s)
	cache := s.RemoteCache()
	token := storekit.Get[model.RemoteCacheToken](t)(cache.Begin(ctx, k))
	c.Description = "cosmetic"
	storekit.Must(t, s.Repos().Update(ctx, c))
	storekit.Must(t, cache.Check(ctx, token))
	c.Offline = contentPtr(true)
	storekit.Must(t, s.Repos().Update(ctx, c))
	source := storekit.Get[model.SourceIdentity](t)(cache.Source(ctx, c.Key))
	if source != token.Source {
		t.Fatal("offline changed content identity")
	}
	storekit.WantErr(t, cache.Check(ctx, token), kernel.ErrConflict)
	token = storekit.Get[model.RemoteCacheToken](t)(cache.Begin(ctx, k))
	c.Offline = contentPtr(false)
	storekit.Must(t, s.Repos().Update(ctx, c))
	token = storekit.Get[model.RemoteCacheToken](t)(cache.Begin(ctx, k))
	old := token
	c.URL = "https://other.example"
	storekit.Must(t, s.Repos().Update(ctx, c))
	c.URL = "https://source.example"
	storekit.Must(t, s.Repos().Update(ctx, c))
	source = storekit.Get[model.SourceIdentity](t)(cache.Source(ctx, c.Key))
	if source.Generation == old.Source.Generation || source.Repository != old.Source.Repository {
		t.Fatal("source ABA not fenced")
	}
	storekit.WantErr(t, cache.Check(ctx, old), kernel.ErrConflict)
	token = storekit.Get[model.RemoteCacheToken](t)(cache.Begin(ctx, k))
	e := storekit.Get[model.RemoteCacheEntry](t)(cache.Put(ctx, token, remoteEntry(token)))
	c.Password = "rotated"
	storekit.Must(t, s.Repos().Update(ctx, c))
	token = storekit.Get[model.RemoteCacheToken](t)(cache.Begin(ctx, k))
	storekit.Must(t, cache.Negative(ctx, token, storekit.At(5)))
	got := storekit.Get[model.RemoteCacheEntry](t)(cache.Read(ctx, k))
	if got.Blob.Checksums.SHA256 != "" {
		t.Fatal("new source negative reused old source bytes")
	}
	b := storekit.Get[blob.Blob](t)(s.Blobs().Get(ctx, e.Blob.Checksums.SHA256))
	if b.Refs != 0 {
		t.Fatal("source change raw refs")
	}
	state := storekit.Get[model.RemoteSourceState](t)(cache.State(ctx, c.Key))
	state.ConnectionFailures = 9
	state.OfflineUntil = storekit.At(20)
	state.RetryAfterUntil = storekit.At(21)
	storekit.Must(t, cache.PutState(ctx, c.Key, state))
	storekit.WantErr(t, cache.PutState(ctx, c.Key, state), kernel.ErrConflict)
	state.Revision++
	storekit.Same(
		t,
		"cooldown state",
		state,
		storekit.Get[model.RemoteSourceState](t)(cache.State(ctx, c.Key)),
	)
	state.ConnectionFailures = -1
	storekit.WantErr(t, cache.PutState(ctx, c.Key, state), kernel.ErrInvalid)
	token = storekit.Get[model.RemoteCacheToken](t)(cache.Begin(ctx, k))
	storekit.Must(t, s.Repos().Delete(ctx, c.Key))
	storekit.WantErr(t, cache.Check(ctx, token), kernel.ErrConflict)
	storekit.Must(t, s.Repos().Create(ctx, c))
	source = storekit.Get[model.SourceIdentity](t)(cache.Source(ctx, c.Key))
	if source.Repository == token.Source.Repository {
		t.Fatal("recreated lifetime reused")
	}
	_, err := cache.Put(ctx, token, remoteEntry(token))
	storekit.WantErr(t, err, kernel.ErrConflict)
	_, err = cache.Source(ctx, "missing")
	storekit.WantErr(t, err, kernel.ErrNotFound)
	_, err = cache.Begin(ctx, model.RemoteCacheKey{RepoKey: "missing", Path: "p"})
	storekit.WantErr(t, err, kernel.ErrNotFound)
}

func testRemoteAtomic(t *testing.T, s port.Store) {
	ctx, c, k := remoteFixture(t, s)
	cache := s.RemoteCache()
	token := storekit.Get[model.RemoteCacheToken](t)(cache.Begin(ctx, k))
	//nolint:err113 // the test matches it by identity
	boom := errors.New("rollback cache transaction")
	err := s.Tx(ctx, func(ctx context.Context) error {
		_, err := cache.Put(ctx, token, remoteEntry(token))
		if err != nil {
			return err //nolint:wrapcheck // the transaction returns the store's own error
		}
		return boom
	})
	storekit.WantErr(t, err, boom)
	_, err = cache.Read(ctx, k)
	storekit.WantErr(t, err, kernel.ErrNotFound)
	_, err = s.Blobs().Get(ctx, strings.Repeat("a", 64))
	storekit.WantErr(t, err, kernel.ErrNotFound)
	// Item-backed data gets exactly the reference the caller's item transaction owns.
	e := remoteEntry(token)
	e.Kind = model.RemoteUnique
	e.MetadataBaseURL = ""
	it := storekit.Get[model.Item](
		t,
	)(
		s.Items().
			Put(ctx, model.Item{RepoKey: c.Key, Cached: true, Name: k.Path, Actual: e.Blob.Checksums, Size: e.Blob.Size}),
	)
	e.ItemID = it.ID
	storekit.Get[blob.Blob](t)(s.Blobs().Ref(ctx, e.Blob, 1))
	e = storekit.Get[model.RemoteCacheEntry](t)(cache.Put(ctx, token, e))
	b := storekit.Get[blob.Blob](t)(s.Blobs().Get(ctx, e.Blob.Checksums.SHA256))
	if b.Refs != 1 {
		t.Fatal("double reference for item")
	}
	n, err := cache.Invalidate(ctx, c.Key, "pkg", true)
	storekit.Must(t, err)
	if n != 0 {
		t.Fatal("zap invalidated unique bytes")
	}
	storekit.Must(t, cache.Check(ctx, e.Token))
	// Beginning an uncached path is enough for a delete to fence its future completion.
	other := k
	other.Path = "pkg/new.bin"
	pending := storekit.Get[model.RemoteCacheToken](t)(cache.Begin(ctx, other))
	_, err = cache.Invalidate(ctx, c.Key, "pkg", false)
	storekit.Must(t, err)
	storekit.WantErr(t, cache.Check(ctx, pending), kernel.ErrConflict)
	// Sibling prefix is isolated and a bad item reference cannot publish.
	sibling := k
	sibling.Path = "pkgs/a"
	tok := storekit.Get[model.RemoteCacheToken](t)(cache.Begin(ctx, sibling))
	_, err = cache.Invalidate(ctx, c.Key, "pkg", false)
	storekit.Must(t, err)
	storekit.Must(t, cache.Check(ctx, tok))
	bad := remoteEntry(tok)
	bad.ItemID = it.ID
	bad.Blob.Checksums.SHA256 = strings.Repeat("b", 64)
	_, err = cache.Put(ctx, tok, bad)
	storekit.WantErr(t, err, kernel.ErrInvalid)
	bad.ItemID = 999999
	_, err = cache.Put(ctx, tok, bad)
	storekit.WantErr(t, err, kernel.ErrNotFound)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = cache.Begin(cancelled, k)
	storekit.WantErr(t, err, context.Canceled)
	// Pagination includes tombstones and escapes wildcard prefixes literally.
	for _, path := range []string{"percent%/a", "percent_/b", "percent%/c", "Percent%/upper", "é/child", "É/other"} {
		key := k
		key.Path = path
		storekit.Get[model.RemoteCacheToken](t)(cache.Begin(ctx, key))
	}
	listed := storekit.Pages(t, 1, func(p kernel.Page) ([]model.RemoteCacheEntry, string, error) {
		return cache.List(ctx, c.Key, "percent%", p)
	})
	if len(listed) != 2 {
		t.Fatalf("literal prefix list: %d", len(listed))
	}
	unicodeRows, _, err := cache.List(ctx, c.Key, "é", kernel.Page{})
	storekit.Must(t, err)
	if len(unicodeRows) != 1 || unicodeRows[0].Token.Key.Path != "é/child" {
		t.Fatal("Unicode prefix mismatched", unicodeRows)
	}
	_, _, err = cache.List(ctx, c.Key, "", kernel.Page{After: "%%%"})
	storekit.WantErr(t, err, kernel.ErrInvalidCursor)
	// Repository deletion releases raw metadata independently of item storage.
	fresh := storekit.Get[model.RemoteCacheToken](t)(cache.Begin(ctx, k))
	raw := remoteEntry(fresh)
	raw.Blob.Checksums.SHA256 = strings.Repeat("c", 64)
	storekit.Get[model.RemoteCacheEntry](t)(cache.Put(ctx, fresh, raw))
	storekit.Must(t, s.Repos().Delete(ctx, c.Key))
	b = storekit.Get[blob.Blob](t)(s.Blobs().Get(ctx, raw.Blob.Checksums.SHA256))
	if b.Refs != 0 {
		t.Fatal("repository deletion leaked raw metadata")
	}
}

func testRemoteConcurrent(t *testing.T, s port.Store) {
	ctx, c, k := remoteFixture(t, s)
	cache := s.RemoteCache()
	token := storekit.Get[model.RemoteCacheToken](t)(cache.Begin(ctx, k))
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
		case errors.Is(err, kernel.ErrConflict):
			conflicts++
		default:
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent publication successes=%d conflicts=%d", successes, conflicts)
	}
	saved := storekit.Get[model.RemoteCacheEntry](t)(cache.Read(ctx, k))
	for _, digest := range []string{strings.Repeat("a", 64), strings.Repeat("b", 64)} {
		ref, err := s.Blobs().Get(ctx, digest)
		if digest == saved.Blob.Checksums.SHA256 {
			storekit.Must(t, err)
			if ref.Refs != 1 {
				t.Fatal("winner reference", ref)
			}
		} else if !errors.Is(err, kernel.ErrNotFound) {
			t.Fatal("loser published reference", ref, err)
		}
	}
	// More than the internal invalidation page size must all be fenced. Keep
	// setup in one transaction; no external work runs inside it.
	var pending model.RemoteCacheToken
	storekit.Must(t, s.Tx(ctx, func(ctx context.Context) error {
		for n := range 135 {
			key := k
			key.Path = fmt.Sprintf("batch/%03d", n)
			var err error
			pending, err = cache.Begin(ctx, key)
			if err != nil {
				return err //nolint:wrapcheck // the transaction returns the store's own error
			}
		}
		return nil
	}))
	n, err := cache.Invalidate(ctx, c.Key, "batch", false)
	storekit.Must(t, err)
	if n != 135 {
		t.Fatalf("paged invalidation changed %d", n)
	}
	storekit.WantErr(t, cache.Check(ctx, pending), kernel.ErrConflict)
}

func testRemoteUsage(t *testing.T, s port.Store) {
	ctx, c, key := remoteFixture(t, s)
	cache := s.RemoteCache()
	token := storekit.Get[model.RemoteCacheToken](t)(cache.Begin(ctx, key))
	saved := storekit.Get[model.RemoteCacheEntry](t)(cache.Put(ctx, token, remoteEntry(token)))
	pending := storekit.Get[model.RemoteCacheToken](t)(cache.Begin(ctx, key))
	before := storekit.Get[blob.Blob](t)(s.Blobs().Get(ctx, saved.Blob.Checksums.SHA256))
	storekit.Must(t, cache.Touch(ctx, saved.Token, storekit.At(10)))
	storekit.Must(t, cache.Check(ctx, pending))
	touched := storekit.Get[model.RemoteCacheEntry](t)(cache.Read(ctx, key))
	storekit.Same(t, "usage preserves content token", saved.Token, touched.Token)
	storekit.Same(
		t,
		"usage preserves raw reference",
		before,
		storekit.Get[blob.Blob](t)(s.Blobs().Get(ctx, saved.Blob.Checksums.SHA256)),
	)
	if !touched.LastUsed.Equal(kernel.Normalise(storekit.At(10))) {
		t.Fatal("usage not recorded")
	}
	// A stale snapshot carried by a refresh cannot erase newer successful usage.
	saved.ETag = "replacement"
	saved.LastUsed = storekit.At(3)
	refreshed := storekit.Get[model.RemoteCacheEntry](t)(cache.Put(ctx, pending, saved))
	if refreshed.ETag != "replacement" ||
		!refreshed.LastUsed.Equal(kernel.Normalise(storekit.At(10))) {
		t.Fatal("refresh lost usage", refreshed)
	}
	storekit.Must(t, cache.Touch(ctx, refreshed.Token, storekit.At(4)))
	storekit.Same(
		t,
		"out-of-order usage preserves content",
		refreshed,
		storekit.Get[model.RemoteCacheEntry](t)(cache.Read(ctx, key)),
	)
	// Independently completed readers may report timestamps in either order.
	pending = storekit.Get[model.RemoteCacheToken](t)(cache.Begin(ctx, key))
	start := make(chan struct{})
	done := make(chan error, 2)
	for _, n := range []int{30, 20} {
		go func() { <-start; done <- cache.Touch(ctx, refreshed.Token, storekit.At(n)) }()
	}
	close(start)
	for range 2 {
		storekit.Must(t, <-done)
	}
	storekit.Must(t, cache.Check(ctx, pending))
	got := storekit.Get[model.RemoteCacheEntry](t)(cache.Read(ctx, key))
	storekit.Same(t, "concurrent usage preserves token", refreshed.Token, got.Token)
	if !got.LastUsed.Equal(kernel.Normalise(storekit.At(30))) {
		t.Fatal("concurrent activity regressed")
	}
	// Real content publication and source rotation still fence obsolete usage.
	storekit.WantErr(t, cache.Touch(ctx, saved.Token, storekit.At(40)), kernel.ErrConflict)
	c.Password = "rotated"
	storekit.Must(t, s.Repos().Update(ctx, c))
	storekit.WantErr(t, cache.Touch(ctx, got.Token, storekit.At(40)), kernel.ErrConflict)
	current := storekit.Get[model.RemoteCacheToken](t)(cache.Begin(ctx, key))
	storekit.WantErr(t, cache.Touch(ctx, current, storekit.At(40)), kernel.ErrConflict)
	storekit.Same(
		t,
		"stale source usage did not mutate entry",
		got,
		storekit.Get[model.RemoteCacheEntry](t)(cache.Read(ctx, key)),
	)
}

func testRemoteEvict(t *testing.T, s port.Store) {
	ctx, _, k := remoteFixture(t, s)
	cache := s.RemoteCache()
	token := storekit.Get[model.RemoteCacheToken](t)(cache.Begin(ctx, k))
	original := storekit.Get[model.RemoteCacheEntry](t)(cache.Put(ctx, token, remoteEntry(token)))
	other := k
	other.Variant = "other"
	alternate := storekit.Get[model.RemoteCacheToken](t)(cache.Begin(ctx, other))
	alternateEntry := storekit.Get[model.RemoteCacheEntry](
		t,
	)(
		cache.Put(ctx, alternate, remoteEntry(alternate)),
	)
	storekit.Must(t, cache.Evict(ctx, original.Token))
	storekit.WantErr(t, cache.Evict(ctx, original.Token), kernel.ErrConflict)
	_, err := cache.Read(ctx, k)
	storekit.WantErr(t, err, kernel.ErrNotFound)
	storekit.Same(
		t,
		"other representation",
		alternateEntry,
		storekit.Get[model.RemoteCacheEntry](t)(cache.Read(ctx, other)),
	)
	b := storekit.Get[blob.Blob](t)(s.Blobs().Get(ctx, original.Blob.Checksums.SHA256))
	if b.Refs != 1 {
		t.Fatal("eviction reference count", b.Refs)
	}
}

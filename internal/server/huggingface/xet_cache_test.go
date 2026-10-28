package huggingface

import (
	"fmt"
	"sync"
	"testing"
)

func TestUnitXetMetadataCacheIsBoundedAndIsolated(t *testing.T) {
	var absent *xetMetadataCache
	absent.put(xetCacheKey{}, xetRecord{})
	if _, ok := absent.get(xetCacheKey{}); ok {
		t.Fatal("nil cache hit")
	}
	cache := new(xetMetadataCache)
	key := xetCacheKey{SHA256: "a", ItemID: 1}
	record := xetRecord{
		Hash:     "root",
		Segments: []xetSegment{{Hash: "xorb", Start: 0, End: 1, Size: 10}},
	}
	cache.put(key, record)
	record.Segments[0].Hash = "mutated"
	cached, ok := cache.get(key)
	if !ok || cached.Segments[0].Hash != "xorb" {
		t.Fatal("cache borrowed caller slice")
	}
	cached.Segments[0].Hash = "changed"
	cached, _ = cache.get(key)
	if cached.Segments[0].Hash != "xorb" {
		t.Fatal("cache exposed mutable slice")
	}
	cache.put(key, xetRecord{Hash: "different"})
	cached, _ = cache.get(key)
	if cached.Hash != "root" {
		t.Fatal("immutable cache entry changed")
	}
	var wg sync.WaitGroup
	for i := range maxXetMetadataEntries + 5 {
		wg.Go(func() {
			next := xetCacheKey{SHA256: fmt.Sprint(i), ItemID: int64(i + 2)}
			cache.put(next, record)
			_, _ = cache.get(next)
		})
	}
	wg.Wait()
	if len(cache.records) != maxXetMetadataEntries || len(cache.order) != maxXetMetadataEntries {
		t.Fatal("metadata cache exceeds bound")
	}
	if _, ok := cache.get(key); ok {
		t.Fatal("old metadata was never evicted")
	}
	if _, ok := cache.get(xetCacheKey{SHA256: "a", ItemID: 999}); ok {
		t.Fatal("deleted item incarnation reused cached authority")
	}
}

package gcs

import (
	"container/list"
	"sync"
)

// signedURLCacheSize bounds the URLs a store caches. A V4 URL with its key
// is about 1 KiB, so a full cache holds about 4 MiB; 4096 blobs cover the
// busiest window of downloads a single instance redirects, and a miss costs
// only one signBlob request. A variable so tests can exercise eviction.
var signedURLCacheSize = 4096

// urlKey identifies one signed URL: the blob, the response overrides it
// carries and the window it expires at the end of. The store signs one route
// (storage), so the route needs no field of its own.
type urlKey struct {
	sha         string
	filename    string
	contentType string
	expires     int64
}

type urlEntry struct {
	key urlKey
	url string
}

// urlCache is a bounded least-recently-used cache of signed URLs. URLs from
// past windows are never asked for again and age out of the back.
type urlCache struct {
	mu    sync.Mutex
	max   int
	order *list.List // front is the most recently used
	items map[urlKey]*list.Element
}

func newURLCache(maxEntries int) *urlCache {
	return &urlCache{max: maxEntries, order: list.New(), items: make(map[urlKey]*list.Element)}
}

func (c *urlCache) get(k urlKey) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[k]
	if !ok {
		return "", false
	}
	c.order.MoveToFront(e)
	return e.Value.(*urlEntry).url, true //nolint:forcetypeassert // only *urlEntry is stored
}

func (c *urlCache) put(k urlKey, url string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[k]; ok {
		e.Value.(*urlEntry).url = url //nolint:forcetypeassert // only *urlEntry is stored
		c.order.MoveToFront(e)
		return
	}
	c.items[k] = c.order.PushFront(&urlEntry{key: k, url: url})
	for c.order.Len() > c.max {
		last := c.order.Back()
		c.order.Remove(last)
		delete(
			c.items,
			last.Value.(*urlEntry).key,
		) //nolint:forcetypeassert // only *urlEntry is stored
	}
}

func (c *urlCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

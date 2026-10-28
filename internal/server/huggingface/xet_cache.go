package huggingface

import (
	"slices"
	"sync"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

const maxXetMetadataEntries = 128

type xetCacheKey struct {
	Source       types.SourceIdentity
	Path, SHA256 string
	Size, ItemID int64
}

// Only immutable derived metadata is retained. Every lookup follows a successful
// P9 source read and uses its live item ID and source generation. Eviction never
// deletes artifact bytes, and this cache cannot serve a deleted/no-store item.
type xetMetadataCache struct {
	mu      sync.Mutex
	records map[xetCacheKey]xetRecord
	order   []xetCacheKey
}

func (c *xetMetadataCache) get(key xetCacheKey) (xetRecord, bool) {
	if c == nil {
		return xetRecord{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	record, ok := c.records[key]
	record.Segments = slices.Clone(record.Segments)
	return record, ok
}

func (c *xetMetadataCache) put(key xetCacheKey, record xetRecord) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.records == nil {
		c.records = make(map[xetCacheKey]xetRecord)
	}
	if _, exists := c.records[key]; exists {
		return
	}
	if len(c.order) == maxXetMetadataEntries {
		delete(c.records, c.order[0])
		c.order = c.order[1:]
	}
	record.Segments = slices.Clone(record.Segments)
	c.records[key] = record
	c.order = append(c.order, key)
}

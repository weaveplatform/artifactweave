package virtual

// Usage is how many entries c holds and the bytes they count, for the
// bound tests.
func (c *Cache) Usage() (int, int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lru.Len(), c.bytes
}

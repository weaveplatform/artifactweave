package memtx

// Copied returns how many rows the tables have copied out so far.
func Copied() int64 { return copied.Load() }

//go:build !unix && !windows

package usage

// diskSpace has no implementation here; the capacity is reported unknown.
func diskSpace(string) (total, free int64, ok bool) { return 0, 0, false }

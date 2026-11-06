//go:build !windows

package fs

func publish(from, to string) error { return osRename(from, to) }

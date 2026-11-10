//go:build unix

package usage

import "golang.org/x/sys/unix"

// diskSpace reports the capacity of the volume holding dir.
func diskSpace(dir string) (total, free int64, ok bool) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, 0, false
	}
	// The field types differ by platform (Bsize is uint32 on Darwin, int64
	// on Linux); a volume's byte counts fit in an int64 on all of them.
	bsize := int64(st.Bsize)      //nolint:unconvert,nolintlint // uint32 on some platforms
	blocks := int64(st.Blocks)    //nolint:gosec // block counts fit
	available := int64(st.Bavail) //nolint:gosec // block counts fit
	return blocks * bsize, available * bsize, true
}

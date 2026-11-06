//go:build windows

package usage

import "golang.org/x/sys/windows"

// diskSpace reports the capacity of the volume holding dir.
func diskSpace(dir string) (total, free int64, ok bool) {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, 0, false
	}
	var avail, size, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &avail, &size, &totalFree); err != nil {
		return 0, 0, false
	}
	return int64(size), int64(avail), true //nolint:gosec // volume sizes fit
}

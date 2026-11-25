//go:build linux || darwin

package health

import "syscall"

// diskFree stats the filesystem that holds path.
//
// Bavail, not Bfree: the difference between them is the reserved block pool,
// which only root may write into, and this process runs as an unprivileged
// user. Reporting the root reserve as free space would report a disk that
// already refuses every write as having room.
//
// The field widths differ between Linux (Bsize int64) and Darwin (Bsize
// uint32), which is why every value is converted rather than used directly.
func diskFree(path string) (freeBytes, freeInodes, totalInodes uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, 0, err
	}
	return st.Bavail * uint64(st.Bsize), uint64(st.Ffree), uint64(st.Files), nil
}

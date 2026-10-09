//go:build !windows

package disklimit

import (
	"os"

	"golang.org/x/sys/unix"
)

func Free(path string) (uint64, error) {
	s, err := Stats(path)
	return s.AvailableBytes, err
}

func Stats(path string) (DiskStats, error) {
	var s unix.Statfs_t
	err := unix.Statfs(path, &s)
	total, free := uint64(s.Blocks)*uint64(s.Bsize), uint64(s.Bfree)*uint64(s.Bsize)
	return DiskStats{TotalBytes: total, UsedBytes: total - min(total, free), FreeBytes: free,
		AvailableBytes: uint64(s.Bavail) * uint64(s.Bsize)}, err
}

func Allocated(f *os.File) (int64, error) {
	var s unix.Stat_t
	err := unix.Fstat(int(f.Fd()), &s)
	return s.Blocks * 512, err
}

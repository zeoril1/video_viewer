package disklimit

import "os"

// DiskStats describes the filesystem containing a cache directory. FreeBytes
// includes space reserved for other users; AvailableBytes is usable by this
// server process and is the value used by the disk budget.
type DiskStats struct {
	TotalBytes     uint64 `json:"total_bytes"`
	UsedBytes      uint64 `json:"used_bytes"`
	FreeBytes      uint64 `json:"free_bytes"`
	AvailableBytes uint64 `json:"available_bytes"`
}

func AllocatedBytes(path string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return Allocated(f)
}

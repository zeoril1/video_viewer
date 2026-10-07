package disklimit

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func Free(path string) (uint64, error) {
	s, err := Stats(path)
	return s.AvailableBytes, err
}

func Stats(path string) (DiskStats, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return DiskStats{}, err
	}
	var available, total, free uint64
	err = windows.GetDiskFreeSpaceEx(p, &available, &total, &free)
	return DiskStats{TotalBytes: total, UsedBytes: total - min(total, free), FreeBytes: free, AvailableBytes: available}, err
}

// Allocated reports filesystem allocation, rather than the logical EOF of a
// partially written or sparse file.
func Allocated(f *os.File) (int64, error) {
	var standard struct {
		AllocationSize int64
		EndOfFile      int64
		NumberOfLinks  uint32
		DeletePending  uint8
		Directory      uint8
	}
	err := windows.GetFileInformationByHandleEx(windows.Handle(f.Fd()), windows.FileStandardInfo,
		(*byte)(unsafe.Pointer(&standard)), uint32(unsafe.Sizeof(standard)))
	return standard.AllocationSize, err
}

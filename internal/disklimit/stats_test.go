package disklimit

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStatsAndAllocatedFile(t *testing.T) {
	dir := t.TempDir()
	s, err := Stats(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.TotalBytes == 0 || s.UsedBytes+s.FreeBytes != s.TotalBytes || s.AvailableBytes > s.FreeBytes {
		t.Fatalf("invalid filesystem accounting: %+v", s)
	}
	free, err := Free(dir)
	if err != nil || free == 0 {
		t.Fatalf("Free: %d %v", free, err)
	}
	path := filepath.Join(dir, "allocation.bin")
	if err := os.WriteFile(path, make([]byte, 8192), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := AllocatedBytes(path)
	if err != nil || n <= 0 {
		t.Fatalf("AllocatedBytes: %d %v", n, err)
	}
	if _, err := AllocatedBytes(filepath.Join(dir, "absent")); err == nil {
		t.Fatal("missing file must fail")
	}
}

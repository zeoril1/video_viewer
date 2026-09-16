package torrents

import (
	"testing"
	"time"

	"github.com/zeoril1/video_viewer/internal/catalog"
)

// testMagnetHash — валидная магнет-ссылка с заданным infohash (пиры не нужны: проверяем кеш-логику).
func testMagnetHash(hex string) string {
	return "magnet:?xt=urn:btih:" + hex + "&dn=test"
}

// TestKeepDefersDropUntilTTL — после Keep торрент выгружается по TTL, а не через DropGrace.
func TestKeepDefersDropUntilTTL(t *testing.T) {
	silenceLogs(t)
	mgr := newTestManager(t)

	mag := testMagnetHash("1111111111111111111111111111111111111111")
	item := catalog.Item{ID: "t", Magnet: mag}
	if _, release, err := mgr.Acquire(item); err != nil {
		t.Fatalf("Acquire: %v", err)
	} else {
		release() // читателей 0 → вооружён дроп через DropGrace
	}

	mgr.Keep(mag, 200*time.Millisecond)

	hash, err := hashOf(mag)
	if err != nil {
		t.Fatal(err)
	}
	// Торрент должен выгрузиться вскоре после TTL (200 мс), а не раньше.
	deadline := time.Now().Add(2 * time.Second)
	for {
		mgr.mu.Lock()
		_, open := mgr.open[hash]
		mgr.mu.Unlock()
		if !open {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("торрент не выгружен после истечения TTL кеша")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestKeepCapEvictsOldest — при переполнении maxCached выгружается простаивающий торрент с самым ранним TTL.
func TestKeepCapEvictsOldest(t *testing.T) {
	silenceLogs(t)
	mgr, err := NewManager(Config{MaxCached: 2})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer mgr.Close()

	mags := []string{
		testMagnetHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), // старый (1 ч)
		testMagnetHash("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"), // средний (2 ч)
		testMagnetHash("cccccccccccccccccccccccccccccccccccccccc"), // новый (3 ч)
	}
	hashes := make([]string, len(mags))
	for i, mag := range mags {
		hashes[i], err = hashOf(mag)
		if err != nil {
			t.Fatal(err)
		}
		if _, release, err := mgr.Acquire(catalog.Item{ID: "t", Magnet: mag}); err != nil {
			t.Fatalf("Acquire %d: %v", i, err)
		} else {
			release()
		}
	}

	mgr.Keep(mags[0], time.Hour)   // open: A
	mgr.Keep(mags[1], 2*time.Hour) // open: A, B (в лимите)
	mgr.Keep(mags[2], 3*time.Hour) // open: A, B, C — лимит 2 → выгрузить A

	mgr.mu.Lock()
	_, aOpen := mgr.open[hashes[0]]
	_, bOpen := mgr.open[hashes[1]]
	_, cOpen := mgr.open[hashes[2]]
	_, aKept := mgr.keepUntil[hashes[0]]
	mgr.mu.Unlock()

	if aOpen {
		t.Fatal("самый старый торрент (A) должен быть выгружен по лимиту кеша")
	}
	if !bOpen || !cOpen {
		t.Fatal("торренты B и C должны остаться в кеше")
	}
	if aKept {
		t.Fatal("запись кеша A должна быть удалена при выгрузке")
	}
}

// TestWantFileNoInfoIsNoop — WantFile без метаданных (нет info) безопасен: applyFilePriorities пропускает.
func TestWantFileNoInfoIsNoop(t *testing.T) {
	silenceLogs(t)
	mgr := newTestManager(t)

	mag := testMagnetHash("2222222222222222222222222222222222222222")
	item := catalog.Item{ID: "t", Magnet: mag}
	if _, release, err := mgr.Acquire(item); err != nil {
		t.Fatalf("Acquire: %v", err)
	} else {
		defer release()
	}

	releaseWant := mgr.WantFile(item, 0)
	if releaseWant == nil {
		t.Fatal("WantFile вернул nil")
	}
	releaseWant() // не должно паниковать при отсутствии info
}

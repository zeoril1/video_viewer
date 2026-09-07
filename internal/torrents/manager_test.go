package torrents

import (
	"io"
	"log"
	"testing"

	"github.com/zeoril1/video_viewer/internal/catalog"
)

// silenceLogs глушит логгер на время теста (drop печатает про выгрузку).
func silenceLogs(t *testing.T) {
	t.Helper()
	old := log.Writer()
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(old) })
}

// testMagnet — валидная магнет-ссылка для тестов (пиры не нужны: нам
// важно поведение менеджера, а не реальное скачивание).
const testMagnet = "magnet:?xt=urn:btih:097fd9047fa928346e94ed5c1cb07b9531c15f90&dn=test"

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	mgr, err := NewManager(Config{}) // ListenPort 0 = случайный порт
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(mgr.Close)
	return mgr
}

// TestAcquireAfterDropCreatesNewTorrent — сценарий из задачи: поток
// (торрент) был выгружен из памяти, при возобновлении просмотра должен
// создаваться НОВЫЙ поток, а не возвращаться закрытый старый торрент.
func TestAcquireAfterDropCreatesNewTorrent(t *testing.T) {
	silenceLogs(t)
	mgr := newTestManager(t)
	item := catalog.Item{ID: "t", Magnet: testMagnet}

	hash, err := hashOf(item.Magnet)
	if err != nil {
		t.Fatal(err)
	}

	// Первый просмотр: открываем и закрываем (вооружает таймер выгрузки).
	t1, release1, err := mgr.Acquire(item)
	if err != nil {
		t.Fatalf("первый Acquire: %v", err)
	}
	release1()

	// Выгружаем торрент из памяти (как по истечении DropGrace).
	mgr.drop(hash)

	mgr.mu.Lock()
	_, stillOpen := mgr.open[hash]
	mgr.mu.Unlock()
	if stillOpen {
		t.Fatal("после drop торрент остался в open")
	}

	// Возобновление просмотра: должен создаться НОВЫЙ живой торрент.
	t2, release2, err := mgr.Acquire(item)
	if err != nil {
		t.Fatalf("Acquire после drop: %v", err)
	}
	defer release2()

	if t2 == t1 {
		t.Fatal("Acquire после выгрузки вернул СТАРЫЙ (закрытый) торрент — ожидался новый")
	}
	mgr.mu.Lock()
	live, ok := mgr.open[hash]
	mgr.mu.Unlock()
	if !ok || live != t2 {
		t.Fatalf("после повторного Acquire в open должен лежать новый торрент, got ok=%v live==t2=%v", ok, live == t2)
	}
}

// TestAcquireNeverReturnsDroppedTorrent — гонка: таймер выгрузки может
// сработать ровно в момент возобновления. Acquire обязан вернуть ЖИВОЙ
// торрент (присутствующий в open сразу после вызова), а не закрытый.
// Раньше поиск торрента и инкремент счётчика читателей были РАЗНЫМИ
// секциями блокировки — между ними могла произойти выгрузка.
func TestAcquireNeverReturnsDroppedTorrent(t *testing.T) {
	silenceLogs(t)
	mgr := newTestManager(t)
	item := catalog.Item{ID: "t", Magnet: testMagnet}

	hash, err := hashOf(item.Magnet)
	if err != nil {
		t.Fatal(err)
	}

	// Открываем и отпускаем: кэш заполнен, таймер выгрузки вооружён.
	_, release0, err := mgr.Acquire(item)
	if err != nil {
		t.Fatal(err)
	}
	release0()

	for i := 0; i < 50; i++ {
		// Параллельно: выгрузка (как по таймеру) и возобновление (Acquire).
		dropDone := make(chan struct{})
		go func() {
			defer close(dropDone)
			mgr.drop(hash)
		}()

		t2, release2, err := mgr.Acquire(item)
		if err != nil {
			t.Fatalf("iter %d: Acquire: %v", i, err)
		}

		// Инвариант: вернувшийся торрент обязан быть живым, т.е. лежать
		// в open сразу после Acquire (иначе это закрытый торрент).
		mgr.mu.Lock()
		live := mgr.open[hash]
		mgr.mu.Unlock()
		if live != t2 {
			t.Fatalf("iter %d: Acquire вернул торрент %p, но в open лежит %p — торрент был выгружен между поиском и регистрацией читателя", i, t2, live)
		}
		release2()
		<-dropDone
	}
}

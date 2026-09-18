package iptvapi

import (
	"log"
	"net/http"
	"sync/atomic"
	"time"
)

// Часы сервиса IPTV НЕ должны зависеть от системных: если они ушли вперёд (на
// этом хосте они убегали на трое суток), окно синхронизации телепрограммы
// сдвигается, все передачи выглядят прошедшими и «сейчас/далее» в интерфейсе
// пропадает — каналы есть, а программы будто нет.
//
// Время берём у внешних серверов, к которым сервис и так ходит за плейлистами
// и телепрограммой: каждый ответ несёт заголовок Date (UTC). Расхождение с
// локальными часами запоминаем как поправку и применяем её везде, где время
// сравнивается с временем телепрограммы.
const (
	// clockTolerance — расхождение, которое считаем сетевым джиттером:
	// заголовок Date имеет секундную точность, да и ответ идёт доли секунды.
	clockTolerance = 2 * time.Minute
	// clockWarnPeriod — как часто писать в лог про убежавшие часы.
	clockWarnPeriod = time.Hour
)

// clock — «правильное» время сервиса: поправка к локальным часам, выведенная
// из заголовков Date внешних ответов.
type clock struct {
	offset   atomic.Int64 // поправка к локальным часам, нс
	best     atomic.Int64 // самый свежий виденный Date, unix-секунды
	warnedAt atomic.Int64 // unix-время последнего предупреждения (0 — не было)
}

// serviceClock — часы процесса: и синхронизация, и справочник каналов получают
// ответы через одни и те же fetch-хелперы (см. observe в fetchStream).
var serviceClock clock

// now — текущее время с учётом поправки (часы в порядке — поправка нулевая).
func (c *clock) now() time.Time {
	return time.Now().Add(time.Duration(c.offset.Load()))
}

// observe учитывает заголовок Date ответа. Заголовку Age (Fastly и Cloudflare
// ставят его и на свежие ответы) не верим: он бывает и «завышенным», и
// нулевым на кэшированной копии. Вместо этого запоминаем САМЫЙ СВЕЖИЙ виденный
// Date — тогда копия из промежуточного кэша со старым Date ничего не сдвинет, а
// очередной свежий ответ поправку уточнит.
func (c *clock) observe(h http.Header) {
	if h == nil {
		return
	}
	date := h.Get("Date")
	if date == "" {
		return
	}
	t, err := http.ParseTime(date)
	if err != nil {
		return
	}
	// Время не свежее уже виденного — ответ из кэша, он не источник истины.
	if t.Unix() <= c.best.Load() {
		return
	}
	for {
		prev := c.best.Load()
		if t.Unix() <= prev {
			return
		}
		if !c.best.CompareAndSwap(prev, t.Unix()) {
			continue
		}
		off := time.Until(t)
		if off > -clockTolerance && off < clockTolerance {
			// Часы совпали — поправка не нужна (и старая, если была, снимается).
			c.offset.Store(0)
			return
		}
		c.offset.Store(int64(off))
		c.warn(off)
		return
	}
}

// warn пишет в лог про расхождение часов (не чаще раза в час).
func (c *clock) warn(off time.Duration) {
	now := time.Now().Unix()
	last := c.warnedAt.Load()
	if last != 0 && now-last < int64(clockWarnPeriod/time.Second) {
		return
	}
	if !c.warnedAt.CompareAndSwap(last, now) {
		return
	}
	log.Printf("iptv: системные часы расходятся с серверными на %s — для телепрограммы учтено серверное время, синхронизируйте часы системы",
		off.Round(time.Second))
}

// now — время сервиса (с поправкой на расхождение системных часов).
func (s *Server) now() time.Time {
	return serviceClock.now()
}

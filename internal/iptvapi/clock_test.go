package iptvapi

import (
	"net/http"
	"testing"
	"time"
)

// TestClockUsesServerDate — время сервиса берётся по заголовку Date внешнего
// ответа: убежавшие системные часы не должны сдвигать телепрограмму.
func TestClockUsesServerDate(t *testing.T) {
	var c clock
	server := time.Now().Add(72 * time.Hour).Truncate(time.Second)
	c.observe(http.Header{"Date": []string{server.UTC().Format(http.TimeFormat)}})
	if d := c.now().Sub(server); d > 2*time.Second || d < -2*time.Second {
		t.Errorf("время сервиса %s, ждали ≈%s (расхождение %s)", c.now(), server, d)
	}
}

// TestClockIgnoresJitter — расхождение в пределах сетевого джиттера поправкой
// не считается: заголовок Date точен лишь до секунды.
func TestClockIgnoresJitter(t *testing.T) {
	var c clock
	c.observe(http.Header{"Date": []string{time.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat)}})
	if d := time.Until(c.now()); d > time.Second || d < -time.Second {
		t.Errorf("часы сдвинулись из-за джиттера на %s", d)
	}

	// Мусор в заголовке — ничего не меняем.
	c.observe(http.Header{"Date": []string{"не дата"}})
	c.observe(nil)
	if d := time.Until(c.now()); d > time.Second || d < -time.Second {
		t.Errorf("часы сдвинулись из-за мусора в заголовке на %s", d)
	}
}

// TestClockKeepsOffsetOnCachedResponse — уже найденная поправка сохраняется,
// когда приходит копия из промежуточного кэша (её Date старее увиденного):
// иначе время «слетало» бы на каждой закэшированной отдаче плейлиста.
func TestClockKeepsOffsetOnCachedResponse(t *testing.T) {
	var c clock
	c.observe(http.Header{"Date": []string{time.Now().Add(72 * time.Hour).UTC().Format(http.TimeFormat)}})
	before := c.now()

	c.observe(http.Header{
		"Date": []string{time.Now().UTC().Format(http.TimeFormat)},
		"Age":  []string{"7200"},
	})
	if d := c.now().Sub(before); d > time.Second || d < -time.Second {
		t.Errorf("поправка потерялась на кэшированном ответе: %s", d)
	}
}

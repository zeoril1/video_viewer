package proxy

import (
	"errors"
	"net/http"
)

// RoundTripper возвращает http.RoundTripper, маршрутизирующий запросы
// через пул прокси с автоматическим failover: при ошибке подключения
// прокси помечается нерабочим, и запрос повторяется через следующий
// (до maxAttempts попыток). Если рабочих прокси нет — прямое соединение.
func (p *Pool) RoundTripper(maxAttempts int) http.RoundTripper {
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	return &failoverRT{pool: p, maxAttempts: maxAttempts}
}

type failoverRT struct {
	pool        *Pool
	maxAttempts int
}

func (rt *failoverRT) RoundTrip(req *http.Request) (*http.Response, error) {
	if rt.pool.Size() == 0 {
		return rt.pool.direct.RoundTrip(req)
	}
	// Тело без GetBody нельзя пересоздать для повторных попыток.
	attempts := rt.maxAttempts
	if req.Body != nil && req.GetBody == nil {
		attempts = 1
	}

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		addr := rt.pool.Next()
		if addr == "" {
			return rt.pool.direct.RoundTrip(req)
		}
		// Клонируем запрос: http.Transport не гарантирует неизменность
		// req при повторных вызовах RoundTrip.
		req2 := req.Clone(req.Context())
		resp, err := rt.pool.transportFor(addr).RoundTrip(req2)
		if err != nil {
			lastErr = err
			rt.pool.MarkBroken(addr)
			continue
		}
		if resp.StatusCode == http.StatusProxyAuthRequired {
			resp.Body.Close()
			rt.pool.MarkBroken(addr)
			lastErr = errors.New("proxy requires auth: " + redact(addr))
			continue
		}
		return resp, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("proxy: no working proxy")
}

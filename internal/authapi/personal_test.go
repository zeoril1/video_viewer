package authapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zeoril1/video_viewer/internal/db"
)

func TestPersonalAndDeviceIntegration(t *testing.T) {
	dsn := os.Getenv("FEATURE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("FEATURE_TEST_DATABASE_URL required")
	}
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)
	schema := "feature_test_" + randomHex(8)
	if _, err = conn.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
	if _, err = conn.Exec(`SET search_path TO ` + schema); err != nil {
		t.Fatal(err)
	}
	repo := db.NewRepo(conn)
	if err = repo.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = repo.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("schema not idempotent: %v", err)
	}
	handler := NewServer(Config{DB: repo})
	call := func(method, path, body string, cookie *http.Cookie, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://viewer.test"+path, strings.NewReader(body))
		req.RemoteAddr = "192.0.2.1:1234"
		if cookie != nil {
			req.AddCookie(cookie)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	reg := call("POST", "/api/auth/register", `{"username":"alice","password":"test-password"}`, nil, "")
	if reg.Code != 200 {
		t.Fatalf("register: %d %s", reg.Code, reg.Body)
	}
	cookie := reg.Result().Cookies()[0]
	reg2 := call("POST", "/api/auth/register", `{"username":"bob","password":"test-password"}`, nil, "")
	if reg2.Code != 200 {
		t.Fatal(reg2.Body)
	}
	bob := reg2.Result().Cookies()[0]
	if w := call("PUT", "/api/personal/watchlist/tt123", `{"title":"Film"}`, nil, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := call("PUT", "/api/personal/watchlist/tt123", `{"title":"Film"}`, cookie, "https://evil.test"); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := call("PUT", "/api/personal/watchlist/tt123", `{"title":"Film"}`, cookie, ""); w.Code != 204 {
		t.Fatal(w.Body)
	}
	if w := call("GET", "/api/personal", "", cookie, ""); !strings.Contains(w.Body.String(), "Film") {
		t.Fatal(w.Body)
	}
	if w := call("GET", "/api/personal", "", bob, ""); strings.Contains(w.Body.String(), "Film") {
		t.Fatal("account data leaked")
	}
	if w := call("PUT", "/api/personal/preferences/playback", `{"voice":"`+strings.Repeat("x", 40000)+`"}`, cookie, ""); w.Code != 400 {
		t.Fatal("oversized JSON accepted")
	}
	if w := call("PUT", "/api/personal/watchlist/tt123", `{} {}`, cookie, ""); w.Code != 400 {
		t.Fatal("trailing JSON accepted")
	}
	if w := call("DELETE", "/api/personal/watchlist/tt123", "", cookie, ""); w.Code != 204 {
		t.Fatal(w.Code)
	}
	start := call("POST", "/api/auth/device/start", "", nil, "")
	if start.Code != 200 {
		t.Fatal(start.Body)
	}
	var codes struct {
		Device string `json:"device_code"`
		User   string `json:"user_code"`
	}
	if err = json.Unmarshal(start.Body.Bytes(), &codes); err != nil {
		t.Fatal(err)
	}
	poll := fmt.Sprintf(`{"device_code":%q}`, codes.Device)
	if w := call("POST", "/api/auth/device/poll", poll, nil, ""); !strings.Contains(w.Body.String(), "pending") || len(w.Result().Cookies()) != 0 {
		t.Fatal(w.Body)
	}
	approval := fmt.Sprintf(`{"code":%q}`, codes.User)
	if w := call("POST", "/api/auth/device/approve", approval, nil, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := call("POST", "/api/auth/device/approve", approval, cookie, ""); w.Code != 204 {
		t.Fatal(w.Body)
	}
	if w := call("POST", "/api/auth/device/approve", approval, bob, ""); w.Code != 400 {
		t.Fatal("approved code reassigned")
	}
	var wg sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- call("POST", "/api/auth/device/poll", poll, nil, "") }()
	}
	wg.Wait()
	close(results)
	count := 0
	for w := range results {
		if strings.Contains(w.Body.String(), "approved") {
			count++
			cs := w.Result().Cookies()
			if len(cs) != 1 || !cs[0].HttpOnly {
				t.Fatal("missing session")
			}
			me := call("GET", "/api/auth/me", "", cs[0], "")
			if !strings.Contains(me.Body.String(), "alice") {
				t.Fatal("wrong user")
			}
		}
	}
	if count != 1 {
		t.Fatalf("claimed %d times", count)
	}
	if err = repo.CreateDeviceLink(context.Background(), deviceHash("expired"), "FFFFFFFF"); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(`UPDATE device_links SET expires_at=$1 WHERE user_code='FFFFFFFF'`, time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if ok, err := repo.ApproveDeviceLink(context.Background(), "FFFFFFFF", 1); err != nil || ok {
		t.Fatalf("expired code approved: %v", err)
	}
}

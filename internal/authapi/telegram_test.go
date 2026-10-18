package authapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/zeoril1/video_viewer/internal/db"
	"github.com/zeoril1/video_viewer/internal/releasewatch"
)

type notificationBot struct{ texts []string }

func (b *notificationBot) Username(context.Context) (string, error) { return "kinoteka_test_bot", nil }
func (b *notificationBot) Updates(context.Context, int64) ([]releasewatch.Update, error) {
	return nil, nil
}
func (b *notificationBot) Send(_ context.Context, _ int64, text string) error {
	b.texts = append(b.texts, text)
	return nil
}

type notificationDiscovery struct{ variants []releasewatch.Variant }

func (d *notificationDiscovery) Scan(context.Context, releasewatch.Subscription) ([]releasewatch.Variant, error) {
	return d.variants, nil
}

func telegramIntegrationRepo(t *testing.T) *db.Repo {
	t.Helper()
	dsn := os.Getenv("FEATURE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("FEATURE_TEST_DATABASE_URL required")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test DSN")
	}
	admin := stdlib.OpenDB(*config)
	t.Cleanup(func() { admin.Close() })
	schema := "telegram_api_test_" + randomHex(8)
	if _, err = admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); err != nil {
			t.Error(err)
		}
	})
	config.RuntimeParams["search_path"] = schema
	conn := stdlib.OpenDB(*config)
	t.Cleanup(func() { conn.Close() })
	repo := db.NewRepo(conn)
	if err = repo.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	return repo
}

func TestTelegramLinkAndReleaseNotificationIntegration(t *testing.T) {
	repo := telegramIntegrationRepo(t)
	ctx := context.Background()
	uid, err := repo.CreateUser(ctx, "alice", "test-hash")
	if err != nil {
		t.Fatal(err)
	}
	other, err := repo.CreateUser(ctx, "bob", "test-hash")
	if err != nil {
		t.Fatal(err)
	}
	token, err := repo.CreateSession(ctx, uid, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	otherToken, err := repo.CreateSession(ctx, other, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	bot := &notificationBot{}
	handler := NewServer(Config{DB: repo, TelegramBot: bot})
	call := func(method, path, body, session, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://viewer.test"+path, strings.NewReader(body))
		r.RemoteAddr = "192.0.2.1:1234"
		if session != "" {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := call("GET", "/api/notifications/telegram", "", "", ""); w.Code != 401 {
		t.Fatalf("guest: %d", w.Code)
	}
	if w := call("POST", "/api/notifications/telegram/link", "", token, "https://evil.test"); w.Code != 403 {
		t.Fatalf("CSRF: %d", w.Code)
	}
	w := call("POST", "/api/notifications/telegram/link", "", token, "")
	var link struct {
		URL string `json:"url"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &link) != nil {
		t.Fatalf("link: %d %s", w.Code, w.Body)
	}
	u, err := url.Parse(link.URL)
	if err != nil {
		t.Fatal(err)
	}
	payload := u.Query().Get("start")
	if len(payload) > 64 || !strings.HasPrefix(payload, "link_") {
		t.Fatal("invalid Telegram payload")
	}
	if linked, err := repo.ClaimTelegramLink(ctx, deviceHash(strings.TrimPrefix(payload, "link_")), 12345, "alice_tg"); err != nil || !linked {
		t.Fatal(linked, err)
	}
	if linked, err := repo.ClaimTelegramLink(ctx, deviceHash(strings.TrimPrefix(payload, "link_")), 12345, "alice_tg"); err != nil || linked {
		t.Fatal("link replay", err)
	}
	if w := call("GET", "/api/notifications/telegram", "", token, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"linked":true`) {
		t.Fatalf("status: %d %s", w.Code, w.Body)
	}
	if w := call("GET", "/api/notifications/telegram", "", otherToken, ""); strings.Contains(w.Body.String(), "alice_tg") || strings.Contains(w.Body.String(), `"linked":true`) {
		t.Fatal("binding leaked to another account")
	}
	for _, body := range []string{`{}`, `{"enabled":"yes"}`, `{"enabled":true} {}`} {
		if w := call("PUT", "/api/notifications/telegram", body, token, ""); w.Code != 400 {
			t.Fatal("invalid setting accepted", body, w.Code)
		}
	}
	if w := call("PUT", "/api/personal/follow/tt1", `{"kind":"movie","title_ru":"Премьера"}`, token, ""); w.Code != 204 {
		t.Fatal(w.Code, w.Body)
	}
	release, acquired, err := repo.AcquireNotificationLeader(ctx)
	if err != nil || !acquired {
		t.Fatal("leader", acquired, err)
	}
	defer release()
	d := &notificationDiscovery{variants: []releasewatch.Variant{{Format: "TS", Resolution: 1080, Voice: "LostFilm"}}}
	worker := &releasewatch.Worker{Store: repo, Discovery: d, Bot: bot, PublicURL: "https://kino.zeoril.ru"}
	for _, format := range []string{"TS", "WEB", "BD", "BD"} {
		d.variants[0].Format = format
		if err := worker.Scan(ctx); err != nil {
			t.Fatal(err)
		}
		if err := worker.Deliver(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if len(bot.texts) != 3 || !strings.Contains(bot.texts[1], "Обновилось качество") || !strings.Contains(bot.texts[2], "BD 1080p") {
		t.Fatalf("upgrade notifications: %+v", bot.texts)
	}
	d.variants = append(d.variants, releasewatch.Variant{Format: "WEB", Resolution: 1080, Voice: "Kravec"})
	if err := worker.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if w := call("PUT", "/api/notifications/telegram", `{"enabled":false}`, token, ""); w.Code != 204 {
		t.Fatal(w.Code)
	}
	if err := worker.Deliver(ctx); err != nil {
		t.Fatal(err)
	}
	if len(bot.texts) != 3 {
		t.Fatal("pause did not cancel queued notification")
	}
	if w := call("PUT", "/api/notifications/telegram", `{"enabled":true}`, token, ""); w.Code != 204 {
		t.Fatal(w.Code)
	}
	d.variants = append(d.variants, releasewatch.Variant{Format: "WEB", Resolution: 1080, Voice: "NewStudio"})
	if err := worker.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if err := worker.Deliver(ctx); err != nil {
		t.Fatal(err)
	}
	if len(bot.texts) != 4 || !strings.Contains(bot.texts[3], "NewStudio") {
		t.Fatal("new voice after resume missing")
	}
	if w := call("DELETE", "/api/notifications/telegram", "", token, ""); w.Code != 204 {
		t.Fatal(w.Code)
	}
	if w := call("GET", "/api/notifications/telegram", "", token, ""); strings.Contains(w.Body.String(), `"linked":true`) {
		t.Fatal("unlink failed")
	}
	if w := call("PUT", "/api/notifications/telegram", `{"enabled":true}`, token, ""); w.Code != 409 {
		t.Fatal("resumed without binding", w.Code)
	}
}

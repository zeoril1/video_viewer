package db

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// Explicit opt-in database only; every object is inside a unique test schema.
// Never use the application's DATABASE_URL or its production schema.
func notificationTestRepo(t *testing.T) (*Repo, context.Context) {
	t.Helper()
	dsn := os.Getenv("FEATURE_TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("FEATURE_TEST_DATABASE_URL or TEST_DATABASE_URL required")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test database configuration")
	}
	admin := stdlib.OpenDB(*config)
	t.Cleanup(func() { admin.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	schema := fmt.Sprintf("notification_test_%d", time.Now().UnixNano())
	if _, err = admin.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(cleanup, `DROP SCHEMA `+schema+` CASCADE`); err != nil {
			t.Error(err)
		}
	})
	config.RuntimeParams["search_path"] = schema
	conn := stdlib.OpenDB(*config)
	conn.SetMaxOpenConns(5)
	t.Cleanup(func() { conn.Close() })
	for _, ddl := range []string{authSchema, personalSchema} {
		if _, err = conn.ExecContext(ctx, ddl); err != nil {
			t.Fatal(err)
		}
	}
	repo := NewRepo(conn)
	if err = repo.ensureNotificationSchema(ctx); err != nil {
		t.Fatal(err)
	}
	return repo, ctx
}

func notificationTestUser(t *testing.T, repo *Repo, ctx context.Context, name string, chatID int64) NotificationSubscription {
	t.Helper()
	uid, err := repo.CreateUser(ctx, name, "test-hash")
	if err != nil {
		t.Fatal(err)
	}
	sub := NotificationSubscription{UserID: uid, FilmID: "tt1234567", ChatID: chatID, Data: json.RawMessage(`{"title":"Test"}`)}
	if err = repo.SavePersonal(ctx, uid, PersonalItem{Kind: "follow", Key: sub.FilmID, Data: sub.Data}); err != nil {
		t.Fatal(err)
	}
	if chatID != 0 {
		link := "link-" + name
		if err = repo.CreateTelegramLink(ctx, uid, link, time.Now().Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		if ok, err := repo.ClaimTelegramLink(ctx, link, chatID, name); err != nil || !ok {
			t.Fatalf("claim: %v %v", ok, err)
		}
	}
	return sub
}

func notificationPending(t *testing.T, repo *Repo, ctx context.Context, count int) []NotificationMessage {
	t.Helper()
	messages, err := repo.PendingNotifications(ctx, 100)
	if err != nil || len(messages) != count {
		t.Fatalf("pending=%+v expected=%d err=%v", messages, count, err)
	}
	return messages
}

func TestNotificationInputValidation(t *testing.T) {
	repo := &Repo{}
	ctx := context.Background()
	if err := repo.SaveNotificationScan(ctx, NotificationSubscription{}, json.RawMessage(`invalid`), nil); err == nil {
		t.Fatal("invalid JSON accepted")
	}
	for _, message := range []NotificationMessage{{Text: "missing key"}, {EventKey: "missing text"}} {
		if err := repo.SaveNotificationScan(ctx, NotificationSubscription{}, json.RawMessage(`{}`), []NotificationMessage{message}); err == nil {
			t.Fatal("invalid outbox event accepted")
		}
	}
	if err := repo.CreateTelegramLink(ctx, 1, "", time.Now().Add(time.Minute)); err == nil {
		t.Fatal("empty link accepted")
	}
	if err := repo.CreateTelegramLink(ctx, 1, "expired", time.Now().Add(-time.Minute)); err == nil {
		t.Fatal("already expired link accepted")
	}
	if ok, err := repo.ClaimTelegramLink(ctx, "token", -1, "group"); ok || err != nil {
		t.Fatal("non-private chat accepted", ok, err)
	}
	if alive, err := repo.NotificationLeaderAlive(ctx); alive || err != nil {
		t.Fatal("unclaimed leadership reported alive", alive, err)
	}
}

func TestTelegramLinkSingleUseAndOwnership(t *testing.T) {
	repo, ctx := notificationTestRepo(t)
	one := notificationTestUser(t, repo, ctx, "one", 0)
	two := notificationTestUser(t, repo, ctx, "two", 0)
	for _, hash := range []string{"old", "new"} {
		if err := repo.CreateTelegramLink(ctx, one.UserID, hash, time.Now().Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if ok, err := repo.ClaimTelegramLink(ctx, "old", 111, "old"); ok || err != nil {
		t.Fatal("replaced link accepted", ok, err)
	}
	if ok, err := repo.ClaimTelegramLink(ctx, "new", -111, "group"); ok || err != nil {
		t.Fatal("group chat accepted", ok, err)
	}
	if ok, err := repo.ClaimTelegramLink(ctx, "new", 111, "one"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, err := repo.ClaimTelegramLink(ctx, "new", 222, "reuse"); ok || err != nil {
		t.Fatal("used token accepted", ok, err)
	}
	if err := repo.CreateTelegramLink(ctx, two.UserID, "two", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if ok, err := repo.ClaimTelegramLink(ctx, "two", 111, "takeover"); ok || err != nil {
		t.Fatal("another user's chat reassigned", ok, err)
	}
	binding, found, err := repo.TelegramBinding(ctx, one.UserID)
	if err != nil || !found || binding.ChatID != 111 || !binding.Enabled || binding.Username != "one" {
		t.Fatal(binding, found, err)
	}
	if _, found, err := repo.TelegramBinding(ctx, two.UserID); err != nil || found {
		t.Fatal("rejected claim created binding", found, err)
	}
	if _, err = repo.conn.ExecContext(ctx, `UPDATE telegram_links SET expires_at=now()-interval '1 second' WHERE token_hash='two'`); err != nil {
		t.Fatal(err)
	}
	if ok, err := repo.ClaimTelegramLink(ctx, "two", 222, "expired"); ok || err != nil {
		t.Fatal("expired token accepted", ok, err)
	}
}

func TestNotificationStateOutboxDeduplicationAndRetry(t *testing.T) {
	repo, ctx := notificationTestRepo(t)
	sub := notificationTestUser(t, repo, ctx, "one", 111)
	state := json.RawMessage(`{"episodes":[1],"quality":"WEB"}`)
	messages := []NotificationMessage{{EventKey: "s1e1:web:voice", Text: "New episode"}}
	if value, err := repo.NotificationState(ctx, sub.UserID, sub.FilmID); err != nil || value != nil {
		t.Fatal(value, err)
	}
	for range 2 {
		if err := repo.SaveNotificationScan(ctx, sub, state, messages); err != nil {
			t.Fatal(err)
		}
	}
	// New Repo instance simulates restarting the worker without losing state.
	repo = NewRepo(repo.conn)
	stored, err := repo.NotificationState(ctx, sub.UserID, sub.FilmID)
	var decoded struct {
		Quality string `json:"quality"`
	}
	if err != nil || json.Unmarshal(stored, &decoded) != nil || decoded.Quality != "WEB" {
		t.Fatalf("stored state=%s err=%v", stored, err)
	}
	pending := notificationPending(t, repo, ctx, 1)
	if pending[0].ChatID != sub.ChatID || pending[0].UserID != sub.UserID || pending[0].FilmID != sub.FilmID {
		t.Fatal("message not bound to scan subscription", pending[0])
	}
	if err = repo.RetryNotification(ctx, pending[0].ID, time.Hour); err != nil {
		t.Fatal(err)
	}
	notificationPending(t, repo, ctx, 0)
	if _, err = repo.conn.ExecContext(ctx, `UPDATE notification_outbox SET next_attempt_at=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	pending = notificationPending(t, repo, ctx, 1)
	if pending[0].Attempts != 1 {
		t.Fatal("retry attempt not persisted", pending[0])
	}
	if err = repo.MarkNotificationSent(ctx, pending[0].ID); err != nil {
		t.Fatal(err)
	}
	if err = repo.SaveNotificationScan(ctx, sub, state, messages); err != nil {
		t.Fatal(err)
	}
	notificationPending(t, repo, ctx, 0)
}

func TestTelegramConcurrentChatClaims(t *testing.T) {
	repo, ctx := notificationTestRepo(t)
	for _, name := range []string{"one", "two"} {
		sub := notificationTestUser(t, repo, ctx, name, 0)
		if err := repo.CreateTelegramLink(ctx, sub.UserID, name, time.Now().Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	type result struct {
		ok  bool
		err error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for _, hash := range []string{"one", "two"} {
		wg.Go(func() {
			ok, err := repo.ClaimTelegramLink(ctx, hash, 111, hash)
			results <- result{ok, err}
		})
	}
	wg.Wait()
	close(results)
	claimed := 0
	for r := range results {
		if r.err != nil {
			t.Fatal("concurrent claim failed instead of rejecting ownership conflict", r.err)
		}
		if r.ok {
			claimed++
		}
	}
	if claimed != 1 {
		t.Fatalf("expected exactly one chat owner, got %d", claimed)
	}
}

func TestNotificationScanAtomicRollback(t *testing.T) {
	repo, ctx := notificationTestRepo(t)
	sub := notificationTestUser(t, repo, ctx, "one", 111)
	if _, err := repo.conn.ExecContext(ctx, `ALTER TABLE notification_outbox ADD CONSTRAINT test_reject CHECK(message_text<>'reject')`); err != nil {
		t.Fatal(err)
	}
	err := repo.SaveNotificationScan(ctx, sub, json.RawMessage(`{"changed":true}`), []NotificationMessage{
		{EventKey: "first", Text: "accepted"}, {EventKey: "second", Text: "reject"},
	})
	if err == nil {
		t.Fatal("expected failed outbox insertion")
	}
	if value, err := repo.NotificationState(ctx, sub.UserID, sub.FilmID); err != nil || value != nil {
		t.Fatal("failed transaction persisted scan state", string(value), err)
	}
	notificationPending(t, repo, ctx, 0)
}

func TestNotificationUnsubscribePauseAndRelink(t *testing.T) {
	repo, ctx := notificationTestRepo(t)
	sub := notificationTestUser(t, repo, ctx, "one", 111)
	_ = notificationTestUser(t, repo, ctx, "unbound", 0)
	state := json.RawMessage(`{"v":1}`)
	queue := func(event string) {
		t.Helper()
		if err := repo.SaveNotificationScan(ctx, sub, state, []NotificationMessage{{EventKey: event, Text: event}}); err != nil {
			t.Fatal(err)
		}
	}
	subs, err := repo.ListNotificationSubscriptions(ctx)
	if err != nil || len(subs) != 1 || subs[0].UserID != sub.UserID {
		t.Fatal(subs, err)
	}
	queue("first")
	if err = repo.SetTelegramEnabled(ctx, sub.UserID, false); err != nil {
		t.Fatal(err)
	}
	queue("stale-paused")
	subs, err = repo.ListNotificationSubscriptions(ctx)
	if err != nil || len(subs) != 0 {
		t.Fatal(subs, err)
	}
	if err = repo.SetTelegramEnabled(ctx, sub.UserID, true); err != nil {
		t.Fatal(err)
	}
	notificationPending(t, repo, ctx, 0)
	queue("second")
	if err = repo.CreateTelegramLink(ctx, sub.UserID, "relink", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if ok, err := repo.ClaimTelegramLink(ctx, "relink", 222, "new"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	queue("stale-chat")
	notificationPending(t, repo, ctx, 0)
	sub.ChatID = 222
	queue("third")
	notificationPending(t, repo, ctx, 1)
	if err = repo.StopTelegramChat(ctx, 222); err != nil {
		t.Fatal(err)
	}
	if err = repo.SetTelegramEnabled(ctx, sub.UserID, true); err != nil {
		t.Fatal(err)
	}
	notificationPending(t, repo, ctx, 0)
	queue("fourth")
	if err = repo.DeletePersonal(ctx, sub.UserID, "follow", sub.FilmID); err != nil {
		t.Fatal(err)
	}
	queue("stale-unfollow")
	notificationPending(t, repo, ctx, 0)
	if value, err := repo.NotificationState(ctx, sub.UserID, sub.FilmID); err != nil || value != nil {
		t.Fatal("unfollow retained state", string(value), err)
	}
	if err = repo.SavePersonal(ctx, sub.UserID, PersonalItem{Kind: "follow", Key: sub.FilmID, Data: sub.Data}); err != nil {
		t.Fatal(err)
	}
	queue("fresh")
	if err = repo.UnlinkTelegram(ctx, sub.UserID); err != nil {
		t.Fatal(err)
	}
	if _, found, err := repo.TelegramBinding(ctx, sub.UserID); err != nil || found {
		t.Fatal(found, err)
	}
	notificationPending(t, repo, ctx, 0)
}

func TestNotificationCursorLeadershipAndMigration(t *testing.T) {
	repo, ctx := notificationTestRepo(t)
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for range 3 {
		wg.Go(func() { errs <- repo.ensureNotificationSchema(ctx) })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal("concurrent migration failed", err)
		}
	}
	if value, err := repo.NotificationCursor(ctx, "last"); err != nil || value != "" {
		t.Fatal(value, err)
	}
	for _, value := range []string{"old", "new"} {
		if err := repo.SaveNotificationCursor(ctx, "last", value); err != nil {
			t.Fatal(err)
		}
	}
	if value, err := NewRepo(repo.conn).NotificationCursor(ctx, "last"); err != nil || value != "new" {
		t.Fatal(value, err)
	}
	release, acquired, err := repo.AcquireNotificationLeader(ctx)
	if err != nil || !acquired {
		t.Fatal(acquired, err)
	}
	t.Cleanup(release)
	if alive, err := repo.NotificationLeaderAlive(ctx); err != nil || !alive {
		t.Fatal(alive, err)
	}
	second := NewRepo(repo.conn)
	if otherRelease, acquired, err := second.AcquireNotificationLeader(ctx); err != nil || acquired || otherRelease != nil {
		t.Fatal("duplicate leader", acquired, err)
	}
	// Dropping precisely the dedicated test connection simulates a lost backend.
	if err = repo.notificationLeaderConn.Raw(func(any) error { return driver.ErrBadConn }); err != driver.ErrBadConn {
		t.Fatal(err)
	}
	if alive, err := repo.NotificationLeaderAlive(ctx); err == nil && alive {
		t.Fatal("lost lock connection reported healthy")
	}
	release()
	release() // Release must be safe after failures and repeated shutdown paths.
	if alive, err := repo.NotificationLeaderAlive(ctx); err != nil || alive {
		t.Fatal(alive, err)
	}
	otherRelease, acquired, err := second.AcquireNotificationLeader(ctx)
	if err != nil || !acquired {
		t.Fatal("leadership not released", acquired, err)
	}
	otherRelease()
}

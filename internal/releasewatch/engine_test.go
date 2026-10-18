package releasewatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/zeoril1/video_viewer/internal/db"
)

func variant(season, episode int, format string, resolution int, voice string) Variant {
	return Variant{Season: season, Episode: episode, Format: format, Resolution: resolution, Voice: voice}
}

func TestObservedMovieUpgradesAndVoiceChanges(t *testing.T) {
	ts := variant(0, 0, "TS", 1080, "LostFilm")
	state, changes := Observe(nil, []Variant{ts, ts})
	if len(changes) != 1 || changes[0].Kind != "release" {
		t.Fatalf("initial: %+v", changes)
	}
	web := variant(0, 0, "WEB-DL", 720, "Lost Film")
	state, changes = Observe(&state, []Variant{web})
	if len(changes) != 1 || changes[0].Kind != "quality" || changes[0].Format != "WEB" {
		t.Fatalf("TS→WEB: %+v", changes)
	}
	bd := variant(0, 0, "BRRip", 720, "LostFilm")
	state, changes = Observe(&state, []Variant{bd})
	if len(changes) != 1 || changes[0].Kind != "quality" || changes[0].Format != "BD" {
		t.Fatalf("WEB→BR: %+v", changes)
	}
	bd1080 := variant(0, 0, "BD", 1080, "LostFilm")
	state, changes = Observe(&state, []Variant{bd1080, variant(0, 0, "TS", 480, "Кравец")})
	if len(changes) != 2 || changes[0].Kind != "quality" || changes[1].Kind != "voice" {
		t.Fatalf("new resolution/voice: %+v", changes)
	}
	for _, observed := range [][]Variant{nil, {ts}, {web, bd, bd1080, variant(0, 0, "TS", 480, "Kravec")}} {
		state, changes = Observe(&state, observed)
		if len(changes) != 0 {
			t.Fatalf("outage, downgrade or repeat manufactured update: %+v", changes)
		}
	}
}

func TestSeriesBaselineOnlyAnnouncesLatestButTracksEveryEpisode(t *testing.T) {
	observed := []Variant{variant(1, 1, "WEB", 1080, "LostFilm"), variant(7, 15, "WEB", 1080, "LostFilm"), variant(7, 14, "WEB", 1080, "LostFilm")}
	state, changes := Observe(nil, observed)
	if len(state.Variants) != 3 || len(changes) != 1 || changes[0].Season != 7 || changes[0].Episode != 15 {
		t.Fatalf("baseline: %+v %+v", state, changes)
	}
	state, changes = Observe(&state, append(observed, variant(7, 16, "WEB", 1080, "LostFilm")))
	if len(changes) != 1 || changes[0].Kind != "release" || changes[0].Episode != 16 {
		t.Fatalf("next: %+v", changes)
	}
	_, changes = Observe(&state, []Variant{variant(1, 1, "BD", 1080, "LostFilm")})
	if len(changes) != 1 || changes[0].Kind != "quality" {
		t.Fatalf("old episode upgrade: %+v", changes)
	}
}

func TestMessagesGroupEpisodesAndSplitBelowTelegramLimit(t *testing.T) {
	sub := db.NotificationSubscription{UserID: 1, ChatID: 2, FilmID: "tt1", Data: json.RawMessage(`{"title_ru":"Тест"}`)}
	changes := []Change{}
	for season := 1; season <= 90; season++ {
		for ep := 1; ep <= 250; ep += 2 {
			changes = append(changes, Change{Kind: "voice", Variant: variant(season, ep, "WEB", 1080, "LostFilm")})
		}
	}
	messages := Messages(sub, changes, "https://kino.zeoril.ru")
	if len(messages) < 2 {
		t.Fatal("large updates must split")
	}
	keys := map[string]bool{}
	for _, m := range messages {
		if len(utf16.Encode([]rune(m.Text))) > 4096 {
			t.Fatalf("message too long: %d", len([]rune(m.Text)))
		}
		if keys[m.EventKey] || m.ChatID != 2 || !strings.Contains(m.Text, "WEB 1080p") || !strings.Contains(m.Text, "Озвучка: LostFilm") {
			t.Fatalf("bad message: %+v", m)
		}
		keys[m.EventKey] = true
	}
	for i, j := 0, len(changes)-1; i < j; i, j = i+1, j-1 {
		changes[i], changes[j] = changes[j], changes[i]
	}
	again := Messages(sub, changes, "https://kino.zeoril.ru")
	if len(again) != len(messages) {
		t.Fatal("ordering changes chunks")
	}
	for i := range again {
		if again[i].EventKey != messages[i].EventKey {
			t.Fatal("ordering changes event identity")
		}
	}
}

type memoryStore struct {
	mu            sync.Mutex
	subs          []db.NotificationSubscription
	states        map[string]json.RawMessage
	cursors       map[string]string
	queue         []db.NotificationMessage
	sent, retried []int64
	delay         time.Duration
	alive         bool
	claimHash     string
	stopped       []int64
}

func newMemoryStore() *memoryStore {
	return &memoryStore{states: map[string]json.RawMessage{}, cursors: map[string]string{}, alive: true}
}
func stateKey(uid int64, id string) string { return fmt.Sprintf("%d/%s", uid, id) }
func (s *memoryStore) ListNotificationSubscriptions(context.Context) ([]db.NotificationSubscription, error) {
	return s.subs, nil
}
func (s *memoryStore) NotificationState(_ context.Context, uid int64, id string) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.states[stateKey(uid, id)], nil
}
func (s *memoryStore) SaveNotificationScan(_ context.Context, sub db.NotificationSubscription, state json.RawMessage, m []db.NotificationMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states[stateKey(sub.UserID, sub.FilmID)] = state
	for _, message := range m {
		message.ID = int64(len(s.queue) + 1)
		s.queue = append(s.queue, message)
	}
	return nil
}
func (s *memoryStore) PendingNotifications(_ context.Context, limit int) ([]db.NotificationMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]db.NotificationMessage{}, s.queue[:min(limit, len(s.queue))]...), nil
}
func (s *memoryStore) MarkNotificationSent(_ context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, id)
	s.queue = s.queue[1:]
	return nil
}
func (s *memoryStore) RetryNotification(_ context.Context, id int64, delay time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retried = append(s.retried, id)
	s.delay = delay
	s.queue = s.queue[1:]
	return nil
}
func (s *memoryStore) ClaimTelegramLink(_ context.Context, hash string, _ int64, _ string) (bool, error) {
	s.claimHash = hash
	return true, nil
}
func (s *memoryStore) StopTelegramChat(_ context.Context, chat int64) error {
	s.stopped = append(s.stopped, chat)
	s.queue = nil
	return nil
}
func (s *memoryStore) NotificationCursor(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cursors[key], nil
}
func (s *memoryStore) SaveNotificationCursor(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cursors[key] = value
	return nil
}
func (s *memoryStore) AcquireNotificationLeader(context.Context) (func(), bool, error) {
	return func() {}, true, nil
}
func (s *memoryStore) NotificationLeaderAlive(context.Context) (bool, error) { return s.alive, nil }

type fakeDiscovery struct {
	mu       sync.Mutex
	calls    map[string]int
	variants []Variant
	fail     string
}

func (d *fakeDiscovery) Scan(_ context.Context, s Subscription) ([]Variant, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.calls == nil {
		d.calls = map[string]int{}
	}
	d.calls[s.FilmID]++
	if s.FilmID == d.fail {
		return nil, errors.New("tracker down")
	}
	return d.variants, nil
}

type fakeBot struct {
	texts  []string
	fail   error
	onSend func()
}

func (b *fakeBot) Username(context.Context) (string, error) { return "test_bot", nil }
func (b *fakeBot) Updates(ctx context.Context, _ int64) ([]Update, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (b *fakeBot) Send(_ context.Context, _ int64, text string) error {
	b.texts = append(b.texts, text)
	if b.onSend != nil {
		b.onSend()
	}
	return b.fail
}

func TestScanSharesDiscoveryButKeepsUserHistoriesSeparate(t *testing.T) {
	s := newMemoryStore()
	s.subs = []db.NotificationSubscription{{UserID: 1, ChatID: 11, FilmID: "tt1"}, {UserID: 2, ChatID: 22, FilmID: "tt1"}, {UserID: 3, ChatID: 33, FilmID: "tt-fail"}}
	d := &fakeDiscovery{variants: []Variant{variant(0, 0, "WEB", 1080, "LostFilm")}, fail: "tt-fail"}
	state, _ := json.Marshal(Snapshot{Version: 1, Variants: d.variants})
	s.states[stateKey(1, "tt1")] = state
	w := &Worker{Store: s, Discovery: d, PublicURL: "https://kino.zeoril.ru"}
	if err := w.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d.calls["tt1"] != 1 || d.calls["tt-fail"] != 1 || len(s.queue) != 1 || s.queue[0].UserID != 2 {
		t.Fatalf("scan/delivery isolation: %+v %+v", d.calls, s.queue)
	}
	if err := w.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(s.queue) != 1 {
		t.Fatal("unchanged recheck duplicated notification")
	}
}

func TestSixHourScheduleSurvivesWorkerRestart(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	s := newMemoryStore()
	s.subs = []db.NotificationSubscription{{UserID: 1, ChatID: 11, FilmID: "tt1"}}
	d := &fakeDiscovery{variants: []Variant{variant(0, 0, "WEB", 1080, "LostFilm")}}
	w := &Worker{Store: s, Discovery: d, Now: func() time.Time { return now }}
	s.cursors["release_last_scan"] = now.Add(-5 * time.Hour).Format(time.RFC3339Nano)
	run := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		w.schedule(ctx)
	}
	run()
	if d.calls["tt1"] != 0 {
		t.Fatal("early scan")
	}
	now = now.Add(time.Hour)
	run()
	if d.calls["tt1"] != 1 {
		t.Fatal("six hour scan missing")
	}
	run()
	if d.calls["tt1"] != 1 {
		t.Fatal("restart rescanned before six hours")
	}
}

func TestDeliveryRetriesRateLimitsAndRechecksUnsubscriptionBetweenMessages(t *testing.T) {
	s := newMemoryStore()
	s.queue = []db.NotificationMessage{{ID: 1, ChatID: 11, Text: "first"}}
	b := &fakeBot{fail: &APIError{Code: 429, RetryAfter: 5 * time.Minute}}
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	w := &Worker{Store: s, Bot: b, Now: func() time.Time { return now }}
	if err := w.Deliver(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(s.sent) != 0 || len(s.retried) != 1 || s.delay != 5*time.Minute {
		t.Fatalf("retry lost: %+v", s)
	}
	s.queue = []db.NotificationMessage{{ID: 2, ChatID: 11, Text: "second"}, {ID: 3, ChatID: 11, Text: "cancelled"}}
	b.fail = nil
	b.onSend = func() { s.queue = s.queue[:1] }
	if err := w.Deliver(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(b.texts) != 1 {
		t.Fatal("Telegram 429 cooldown must block other queued messages too")
	}
	now = now.Add(5 * time.Minute)
	if err := w.Deliver(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(b.texts) != 2 || len(s.sent) != 1 {
		t.Fatalf("sent cancelled later message: %+v", b.texts)
	}
	s.alive = false
	if err := w.Deliver(context.Background()); err == nil {
		t.Fatal("lost leader must not send")
	}
}

func TestTelegramCommandsOnlyBindTheirOwnPrivateChat(t *testing.T) {
	s := newMemoryStore()
	b := &fakeBot{}
	w := &Worker{Store: s, Bot: b}
	var update Update
	if err := json.Unmarshal([]byte(`{"update_id":1,"message":{"text":"/start link_`+strings.Repeat("a", 48)+`","chat":{"id":15,"type":"group"},"from":{"id":15}}}`), &update); err != nil {
		t.Fatal(err)
	}
	if err := w.handleUpdate(context.Background(), update); err != nil {
		t.Fatal(err)
	}
	if s.claimHash != "" || len(b.texts) != 0 {
		t.Fatal("group binding allowed")
	}
	update.Message.Chat.Type = "private"
	update.Message.From.ID = 16
	_ = w.handleUpdate(context.Background(), update)
	if s.claimHash != "" {
		t.Fatal("another sender bound privatechat")
	}
	update.Message.From.ID = 15
	if err := w.handleUpdate(context.Background(), update); err != nil {
		t.Fatal(err)
	}
	if len(s.claimHash) != 64 || s.claimHash == strings.Repeat("a", 48) || !strings.Contains(b.texts[0], "подключён") {
		t.Fatal("link not hashed/confirmed")
	}
	update.Message.Text = "/stop"
	_ = w.handleUpdate(context.Background(), update)
	if len(s.stopped) != 1 || s.stopped[0] != 15 {
		t.Fatal("stop missing")
	}
}

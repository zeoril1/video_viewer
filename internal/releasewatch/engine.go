package releasewatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/zeoril1/video_viewer/internal/db"
)

const CheckInterval = 6 * time.Hour

type Store interface {
	ListNotificationSubscriptions(context.Context) ([]db.NotificationSubscription, error)
	NotificationState(context.Context, int64, string) (json.RawMessage, error)
	SaveNotificationScan(context.Context, db.NotificationSubscription, json.RawMessage, []db.NotificationMessage) error
	PendingNotifications(context.Context, int) ([]db.NotificationMessage, error)
	MarkNotificationSent(context.Context, int64) error
	RetryNotification(context.Context, int64, time.Duration) error
	ClaimTelegramLink(context.Context, string, int64, string) (bool, error)
	StopTelegramChat(context.Context, int64) error
	NotificationCursor(context.Context, string) (string, error)
	SaveNotificationCursor(context.Context, string, string) error
	AcquireNotificationLeader(context.Context) (func(), bool, error)
	NotificationLeaderAlive(context.Context) (bool, error)
}

type Snapshot struct {
	Version  int       `json:"version"`
	Variants []Variant `json:"variants"`
}

type Change struct {
	Kind string
	Variant
}

func assetKey(v Variant) string { return fmt.Sprintf("%d/%d", v.Season, v.Episode) }
func voiceKey(v Variant) string { return assetKey(v) + "/" + strings.ToLower(v.Voice) }
func better(a, b Variant) bool {
	if FormatRank(a.Format) != FormatRank(b.Format) {
		return FormatRank(a.Format) > FormatRank(b.Format)
	}
	return a.Resolution > b.Resolution
}

// Keep every observed variant. A temporary tracker outage, disappearing source
// or lower quality release must never erase delivery history or manufacture news.
func Observe(previous *Snapshot, observed []Variant) (Snapshot, []Change) {
	observed = NormalizeVariants(observed)
	old := []Variant{}
	if previous != nil {
		old = previous.Variants
	}
	assets, best := map[string]bool{}, map[string]Variant{}
	for _, v := range NormalizeVariants(old) {
		assets[assetKey(v)] = true
		key := voiceKey(v)
		if prior, ok := best[key]; !ok || better(v, prior) {
			best[key] = v
		}
	}
	currentBest := map[string]Variant{}
	for _, v := range observed {
		key := voiceKey(v)
		if prior, ok := currentBest[key]; !ok || better(v, prior) {
			currentBest[key] = v
		}
	}
	latestSeason, latestEpisode := 0, 0
	for _, v := range observed {
		if v.Season > latestSeason || v.Season == latestSeason && v.Episode > latestEpisode {
			latestSeason, latestEpisode = v.Season, v.Episode
		}
	}
	changes := []Change{}
	for key, v := range currentBest {
		if previous == nil && (v.Season != latestSeason || v.Episode != latestEpisode) {
			continue
		}
		kind := ""
		if !assets[assetKey(v)] {
			kind = "release"
		} else if prior, ok := best[key]; !ok {
			kind = "voice"
		} else if better(v, prior) {
			kind = "quality"
		}
		if kind != "" {
			changes = append(changes, Change{Kind: kind, Variant: v})
		}
	}
	sort.Slice(changes, func(i, j int) bool {
		a, b := changes[i], changes[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Key() < b.Key()
	})
	all := append(append([]Variant{}, old...), observed...)
	return Snapshot{Version: 1, Variants: NormalizeVariants(all)}, changes
}

func qualityLabel(v Variant) string {
	quality := v.Format
	if quality == "UNKNOWN" || quality == "" {
		quality = "качество не указано"
	}
	if v.Resolution > 0 {
		quality += " " + strconv.Itoa(v.Resolution) + "p"
	}
	return quality
}

func episodeSummary(variants []Variant) string {
	seasons := map[int][]int{}
	for _, v := range variants {
		seasons[v.Season] = append(seasons[v.Season], v.Episode)
	}
	keys := make([]int, 0, len(seasons))
	for season := range seasons {
		keys = append(keys, season)
	}
	sort.Ints(keys)
	parts := []string{}
	for _, season := range keys {
		if season == 0 && seasons[season][0] == 0 {
			parts = append(parts, "Фильм")
			continue
		}
		numbers := seasons[season]
		sort.Ints(numbers)
		ranges := []string{}
		for i := 0; i < len(numbers); {
			start, end := numbers[i], numbers[i]
			i++
			for i < len(numbers) && numbers[i] <= end+1 {
				if numbers[i] > end {
					end = numbers[i]
				}
				i++
			}
			label := strconv.Itoa(start)
			if end > start {
				label += "–" + strconv.Itoa(end)
			}
			ranges = append(ranges, label)
		}
		parts = append(parts, fmt.Sprintf("Сезон %d · серии %s", season, strings.Join(ranges, ", ")))
	}
	return strings.Join(parts, "; ")
}

func filmTitle(sub db.NotificationSubscription) string {
	var data struct {
		Title   string `json:"title"`
		TitleRU string `json:"title_ru"`
	}
	_ = json.Unmarshal(sub.Data, &data)
	title := data.TitleRU
	if title == "" {
		title = data.Title
	}
	if title == "" {
		title = sub.FilmID
	}
	return string([]rune(title)[:min(len([]rune(title)), 180)])
}

// One compact line covers all episodes with the same voice/quality change.
// Chunk conservatively below Telegram's message limit; event IDs use semantic
// changes, not torrent URLs, seed counts or the order of tracker responses.
func Messages(sub db.NotificationSubscription, changes []Change, publicURL string) []db.NotificationMessage {
	if len(changes) == 0 {
		return nil
	}
	type group struct {
		kind    string
		variant Variant
		items   []Variant
		keys    []string
	}
	groups := map[string]*group{}
	ordered := append([]Change{}, changes...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Kind+ordered[i].Key() < ordered[j].Kind+ordered[j].Key() })
	counts := map[string]int{}
	for _, change := range ordered {
		base := change.Kind + "|" + change.Voice + "|" + qualityLabel(change.Variant) + "|" + strconv.Itoa(change.Season)
		key := base + "|" + strconv.Itoa(counts[base]/64)
		counts[base]++
		g := groups[key]
		if g == nil {
			g = &group{kind: change.Kind, variant: change.Variant}
			groups[key] = g
		}
		g.items = append(g.items, change.Variant)
		g.keys = append(g.keys, change.Kind+":"+change.Key())
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	link := strings.TrimRight(publicURL, "/") + "/film.html?id=" + url.QueryEscape(sub.FilmID)
	header := "Кинотека · " + filmTitle(sub) + "\n\n"
	foot := "\n\nКачество и озвучка — по описанию раздачи.\n" + link
	texts, events := []string{}, []string{}
	flush := func() db.NotificationMessage {
		sort.Strings(events)
		hash := sha256.Sum256([]byte(strings.Join(events, "\n")))
		return db.NotificationMessage{UserID: sub.UserID, FilmID: sub.FilmID, ChatID: sub.ChatID,
			EventKey: hex.EncodeToString(hash[:]), Text: header + strings.Join(texts, "\n\n") + foot}
	}
	out := []db.NotificationMessage{}
	for _, key := range keys {
		g := groups[key]
		label := map[string]string{"release": "Доступно для просмотра", "voice": "Появилась озвучка", "quality": "Обновилось качество"}[g.kind]
		line := label + ":\n" + episodeSummary(g.items) + "\nОзвучка: " + g.variant.Voice + "\nКачество: " + qualityLabel(g.variant)
		if len(utf16.Encode([]rune(header+strings.Join(texts, "\n\n")+"\n\n"+line+foot))) > 3700 && len(texts) > 0 {
			out = append(out, flush())
			texts, events = nil, nil
		}
		texts = append(texts, line)
		events = append(events, g.keys...)
	}
	if len(texts) > 0 {
		out = append(out, flush())
	}
	return out
}

type Worker struct {
	Store        Store
	Discovery    Discovery
	Bot          Bot
	PublicURL    string
	ScanInterval time.Duration
	ScanTimeout  time.Duration
	Now          func() time.Time
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func (w *Worker) Scan(ctx context.Context) error {
	subs, err := w.Store.ListNotificationSubscriptions(ctx)
	if err != nil {
		return err
	}
	byFilm := map[string][]db.NotificationSubscription{}
	for _, sub := range subs {
		byFilm[sub.FilmID] = append(byFilm[sub.FilmID], sub)
	}
	jobs := make(chan []db.NotificationSubscription)
	var wg sync.WaitGroup
	var failures int
	var mu sync.Mutex
	for i := 0; i < min(4, len(byFilm)); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for recipients := range jobs {
				first := recipients[0]
				limit := w.ScanTimeout
				if limit <= 0 {
					limit = 15 * time.Minute
				}
				filmCtx, cancel := context.WithTimeout(ctx, limit)
				variants, scanErr := w.Discovery.Scan(filmCtx, Subscription{FilmID: first.FilmID, Data: first.Data})
				cancel()
				if scanErr != nil {
					log.Printf("notifications: film %s scan failed: %v", first.FilmID, scanErr)
					mu.Lock()
					failures++
					mu.Unlock()
					continue
				}
				for _, sub := range recipients {
					if err := w.observe(ctx, sub, variants); err != nil {
						log.Printf("notifications: film %s state unavailable", sub.FilmID)
						mu.Lock()
						failures++
						mu.Unlock()
					}
				}
			}
		}()
	}
	for _, recipients := range byFilm {
		select {
		case jobs <- recipients:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return ctx.Err()
		}
	}
	close(jobs)
	wg.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	log.Printf("notifications: checked %d titles, %d subscriptions, %d failures", len(byFilm), len(subs), failures)
	// A failing title does not prevent the other subscriptions from being checked.
	return nil
}

func (w *Worker) observe(ctx context.Context, sub db.NotificationSubscription, variants []Variant) error {
	raw, err := w.Store.NotificationState(ctx, sub.UserID, sub.FilmID)
	if err != nil {
		return err
	}
	var previous *Snapshot
	if len(raw) > 0 {
		previous = new(Snapshot)
		if err := json.Unmarshal(raw, previous); err != nil {
			return err
		}
		if previous.Version != 1 {
			return fmt.Errorf("unsupported notification snapshot")
		}
	}
	state, changes := Observe(previous, variants)
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return w.Store.SaveNotificationScan(ctx, sub, encoded, Messages(sub, changes, w.PublicURL))
}

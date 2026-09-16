package magnet

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const torznabSample = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:atom="http://www.w3.org/2005/Atom" xmlns:torznab="http://torznab.com/schemas/2015/feed">
<channel>
<title>RuTracker.org</title>
<item>
<title>Тёмный рыцарь / The Dark Knight (2008) [1080p, MVO]</title>
<guid isPermaLink="true">https://rutracker.org/forum/viewtopic.php?t=123456</guid>
<dc:creator xmlns:dc="http://purl.org/dc/elements/1.1/">Uploader</dc:creator>
<comments>https://rutracker.org/forum/viewtopic.php?t=123456</comments>
<pubDate>Sat, 21 Aug 2026 10:00:00 +0000</pubDate>
<description>Раздача</description>
<category>2093</category>
<enclosure url="magnet:?xt=urn:btih:aaaabbbbccccddddeeeeffff0000111122223333&amp;dn=test" length="1234567890" type="application/x-bittorrent"/>
<size>1234567890</size>
<torznab:attr name="seeders" value="42"/>
<torznab:attr name="peers" value="55"/>
<torznab:attr name="size" value="1234567890"/>
</item>
<item>
<title>The Dark Knight 2008 1080p</title>
<link>magnet:?xt=urn:btih:ffffeeee00001111222233334444555566667777</link>
<torznab:attr name="seeders" value="7"/>
</item>
<item>
<title>Без магнита</title>
<enclosure url="http://127.0.0.1:9117/dl/abc123?jackett_apikey=x&amp;file=1" length="1024" type="application/x-bittorrent"/>
<torznab:attr name="seeders" value="1"/>
</item>
<item>
<title>С magneturl</title>
<enclosure url="http://127.0.0.1:9117/dl/xyz?jackett_apikey=x&amp;file=2" length="1024" type="application/x-bittorrent"/>
<torznab:attr name="seeders" value="3"/>
<torznab:attr name="magneturl" value="magnet:?xt=urn:btih:cccc111122223333444455556666777788889999&amp;tr=udp%3A%2F%2Ftracker.example%3A1337%2Fannounce"/>
</item>
</channel>
</rss>`

func TestParseJackettItems(t *testing.T) {
	items := parseJackettItems(torznabSample)
	if len(items) != 4 {
		t.Fatalf("expected 4 items, got %d", len(items))
	}
	// Первый: magnet из enclosure + seeders/size.
	it := items[0]
	if it.magnet == "" || !hasPrefix(it.magnet, "magnet:?xt=urn:btih:aaaabbbb") {
		t.Errorf("item0 magnet: %q", it.magnet)
	}
	if it.seeds != 42 {
		t.Errorf("item0 seeds: %d", it.seeds)
	}
	if it.sizeHuman == "" || it.sizeHuman != "1.1 GB" {
		t.Errorf("item0 size: %q", it.sizeHuman)
	}
	// Второй: magnet прямо в <link>.
	if !hasPrefix(items[1].magnet, "magnet:?xt=urn:btih:ffffeeee") {
		t.Errorf("item1 magnet: %q", items[1].magnet)
	}
	if items[1].seeds != 7 {
		t.Errorf("item1 seeds: %d", items[1].seeds)
	}
	// Третий: нет магнита — ссылка на .torrent через Jackett.
	if items[2].magnet != "" || !hasPrefix(items[2].downloadURL, "http://127.0.0.1:9117/dl/abc123") {
		t.Errorf("item2: magnet=%q url=%q", items[2].magnet, items[2].downloadURL)
	}
	// Четвёртый (item3): magnet пришёл атрибутом magneturl — .torrent не нужен.
	if !hasPrefix(items[3].magnet, "magnet:?xt=urn:btih:cccc1111") {
		t.Errorf("item3 magnet (magneturl): %q", items[3].magnet)
	}
	if strings.Contains(items[3].magnet, "&amp;") {
		t.Errorf("item3 magnet должен быть HTML-разэкранирован: %q", items[3].magnet)
	}
}

func hasPrefix(s, p string) bool {
	return len(s) >= len(p) && s[:len(p)] == p
}

func TestFormatBytes(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1234567890, "1.1 GB"},
		{2 << 30, "2.0 GB"},
	}
	for _, c := range cases {
		if got := formatBytes(c.n); got != c.want {
			t.Errorf("formatBytes(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

// TestJackettSearchIndexerError: Jackett при ненастроенном индексере отдаёт
// HTTP 200 с XML-ошибкой — Search должен вернуть ошибку, а не молча «пусто».
func TestJackettSearchIndexerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/api/v2.0/indexers/rutracker-ru/results/torznab/api") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?>
<error code="201" description="Indexer is not configured" />`)
	}))
	defer srv.Close()

	j := NewJackett(srv.URL, "testkey", "rutracker-ru", nil)
	res, err := j.Search(t.Context(), "test", 5)
	if err == nil {
		t.Fatalf("expected error, got results: %#v", res)
	}
	if !strings.Contains(err.Error(), "Indexer is not configured") {
		t.Errorf("error should mention the indexer issue, got: %v", err)
	}
}

// TestJackettSearchNoErrorOnResults — обычный успешный ответ (HTTP 200 + RSS)
// не считается ошибкой.
func TestJackettSearchNoErrorOnResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, torznabSample)
	}))
	defer srv.Close()

	j := NewJackett(srv.URL, "testkey", "rutracker-ru", nil)
	res, err := j.Search(t.Context(), "dark knight", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Из выборки: item0 (magnet в enclosure), item1 (magnet в link) и
	// item3 (magnet из magneturl); item2 — .torrent-ссылка, её search не качает.
	if len(res) != 3 {
		t.Fatalf("expected 3 results, got %d: %#v", len(res), res)
	}
}

const torznabItemA = `<item>
<title>История игрушек 5 / Toy Story 5 (2026) 2160p</title>
<enclosure url="magnet:?xt=urn:btih:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa&amp;dn=A" length="10" type="application/x-bittorrent"/>
<torznab:attr name="seeders" value="11"/>
</item>`

const torznabItemB = `<item>
<title>История игрушек 5 / Toy Story 5 (2026) 1080p</title>
<enclosure url="magnet:?xt=urn:btih:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb&amp;dn=B" length="20" type="application/x-bittorrent"/>
<torznab:attr name="seeders" value="22"/>
</item>`

// TestJackettSearchMultiIndexer: несколько индексереров через запятую —
// результаты объединяются, дубли (по info_hash) не повторяются, ошибка одного
// индексера не роняет весь поиск.
func TestJackettSearchMultiIndexer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusOK)
		var body string
		switch {
		case strings.Contains(r.URL.Path, "/rutracker-ru/"):
			// вторая раздача совпадает с A из rutor — должна дедуплицироваться
			body = "<?xml version=\"1.0\"?><rss version=\"2.0\"><channel>" + torznabItemA + torznabItemA + "</channel></rss>"
		case strings.Contains(r.URL.Path, "/rutor/"):
			body = "<?xml version=\"1.0\"?><rss version=\"2.0\"><channel>" + torznabItemB + "</channel></rss>"
		case strings.Contains(r.URL.Path, "/broken/"):
			body = "<?xml version=\"1.0\"?><error code=\"201\" description=\"Indexer is not configured\" />"
		default:
			body = "<?xml version=\"1.0\"?><rss version=\"2.0\"><channel></channel></rss>"
		}
		io.WriteString(w, body)
	}))
	defer srv.Close()

	j := NewJackett(srv.URL, "testkey", "rutracker-ru,rutor,broken", nil)
	res, err := j.Search(t.Context(), "toy story", 10)
	if err == nil {
		t.Fatal("partial indexer failure must be reported to prevent pruning cached sources")
	}

	if len(res) != 2 {
		t.Fatalf("expected 2 dedup results, got %d: %#v", len(res), res)
	}
	hashes := map[string]bool{}
	for _, r := range res {
		hashes[magnetInfoHash(r.Magnet)] = true
	}
	if !hashes["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"] || !hashes["bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"] {
		t.Fatalf("unexpected hashes: %#v", hashes)
	}
}

// TestJackettSearchTorrentFetchBudget — раздачи без готового магнета приходится
// «докачивать» (.torrent — лишний запрос к трекеру), поэтому число скачиваний
// на один поисковый запрос ограничено (maxTorrentFetches).
func TestJackettSearchTorrentFetchBudget(t *testing.T) {
	const items = 12
	var downloads int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/dl/") {
			atomic.AddInt32(&downloads, 1)
			http.NotFound(w, r) // тело неважно — считаем сами попытки
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		var b strings.Builder
		b.WriteString(`<?xml version="1.0"?><rss version="2.0"><channel>`)
		for i := 0; i < items; i++ {
			fmt.Fprintf(&b,
				`<item><title>Раздача %d</title><enclosure url="http://%s/dl/%d" length="1024" type="application/x-bittorrent"/><torznab:attr name="seeders" value="1"/></item>`,
				i, r.Host, i)
		}
		b.WriteString(`</channel></rss>`)
		io.WriteString(w, b.String())
	}))
	defer srv.Close()

	j := NewJackett(srv.URL, "testkey", "rutracker-ru", nil)
	if _, err := j.Search(t.Context(), "test", items); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := atomic.LoadInt32(&downloads); got != maxTorrentFetches {
		t.Fatalf("скачиваний .torrent: %d, ожидали %d", got, maxTorrentFetches)
	}
}

// TestJackettSearchAllIndexersFailed: если все индексера вернули ошибку —
// Search возвращает ошибку (а не молча пустоту).
func TestJackettSearchAllIndexersFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "<?xml version=\"1.0\"?><error code=\"201\" description=\"Indexer is not configured\" />")
	}))
	defer srv.Close()

	j := NewJackett(srv.URL, "testkey", "rutracker-ru,rutor", nil)
	res, err := j.Search(t.Context(), "toy story", 10)
	if err == nil {
		t.Fatalf("expected error, got %#v", res)
	}
	if !strings.Contains(err.Error(), "Indexer is not configured") {
		t.Errorf("unexpected error: %v", err)
	}
}

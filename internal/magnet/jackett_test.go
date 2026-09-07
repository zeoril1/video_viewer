package magnet

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
</channel>
</rss>`

func TestParseJackettItems(t *testing.T) {
	items := parseJackettItems(torznabSample)
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
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

// TestJackettSearchIndexerError: Jackett при несуществующем/не настроенном
// индексере отдаёт HTTP 200 с XML-ошибкой <error code="201" .../>. Search
// должен вернуть ошибку, а не молча «пусто» (иначе фронт показывает
// «ничего не найдено», а в логах ничего нет).
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

// TestJackettSearchNoErrorOnResults: обычный успешный ответ (HTTP 200 + RSS)
// не должен считаться ошибкой.
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
	// Из выборки: item0 (magnet в enclosure) и item1 (magnet в link);
	// item2 — только .torrent-ссылка, которую search не качает (нет сервера).
	if len(res) != 2 {
		t.Fatalf("expected 2 results, got %d: %#v", len(res), res)
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
// результаты объединяются, одинаковые раздачи (по info_hash) не дублируются,
// ошибка на одном индексере не роняет весь поиск.
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
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// A (из rutracker-ru, дубль убран) + B (из rutor) = 2. Сломанный
	// индексера пропускается, но т.к. есть результаты — ошибки нет.
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

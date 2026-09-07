// Провайдер поиска раздач через внешний Jackett (Torznab API). Удобен для
// трекеров с антиботом/приватных (RuTracker): доступы, капча и обход
// Cloudflare выполняются в Jackett, а наш сервис просто шлёт запросы.
package magnet

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anacrolix/torrent/metainfo"
)

// Jackett — поиск раздач через Jackett (Torznab). indexer — ID индексера
// в Jackett (например "rutracker-ru"), список через запятую
// ("rutracker-ru,rutor,anilibria") или "all" (все настроенные индексера).
type Jackett struct {
	baseURL string
	apiKey  string
	indexer string
	hc      *http.Client
}

// NewJackett создаёт провайдер Jackett. rt — необязательный http.RoundTripper
// (пул прокси) для запросов к самому Jackett.
func NewJackett(baseURL, apiKey, indexer string, rt http.RoundTripper) *Jackett {
	hc := &http.Client{Timeout: 30 * time.Second}
	if rt != nil {
		hc.Transport = rt
	}
	if indexer == "" {
		indexer = "rutracker-ru"
	}
	return &Jackett{baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, indexer: indexer, hc: hc}
}

// Name возвращает имя провайдера.
func (j *Jackett) Name() string { return "jackett" }

// Парсинг Torznab RSS.
var (
	jtItemRe      = regexp.MustCompile(`(?is)<item\b.*?</item>`)
	jtTitleRe     = regexp.MustCompile(`(?is)<title>(.*?)</title>`)
	jtLinkRe      = regexp.MustCompile(`(?is)<link>(.*?)</link>`)
	jtEnclosureRe = regexp.MustCompile(`(?is)<enclosure[^>]*url="([^"]+)"`)
	jtAttrRe      = regexp.MustCompile(`(?is)<torznab:attr name="([^"]+)" value="([^"]+)"`)
	jtSizeRe      = regexp.MustCompile(`(?is)<size>(.*?)</size>`)
	// Jackett при проблемах (несуществующий индексера, отсутствие настроек)
	// отдаёт HTTP 200 с XML-ошибкой вместо результатов.
	jtErrorRe = regexp.MustCompile(`(?is)<error\s+code="([^"]+)"\s+description="([^"]*)"`)
)

// jackettItem — распарсенная запись выдачи Torznab.
type jackettItem struct {
	Title       string
	seeds       int
	sizeHuman   string
	magnet      string
	downloadURL string // ссылка на .torrent через Jackett (если не magnet)
}

// indexerIDs возвращает список индексереров для опроса: один ID, несколько
// через запятую ("rutracker-ru,rutor,anilibria") или "all" (все настроенные
// в Jackett). Пустое значение трактуется как "all".
func (j *Jackett) indexerIDs() []string {
	raw := strings.TrimSpace(j.indexer)
	if raw == "" {
		return []string{"all"}
	}
	var ids []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			ids = append(ids, p)
		}
	}
	if len(ids) == 0 {
		return []string{"all"}
	}
	return ids
}

// Search ищет раздачи через Torznab по всем настроенным индексерам
// (список из j.indexer) и возвращает их с готовыми магнетами.
func (j *Jackett) Search(ctx context.Context, q string, limit int) ([]Result, error) {
	if limit <= 0 {
		limit = 8
	}
	var (
		all  []Result
		errs []string
	)
	for _, id := range j.indexerIDs() {
		if ctx.Err() != nil {
			break
		}
		res, err := j.searchIndexer(ctx, id, q, limit)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", id, err))
			continue
		}
		log.Printf("jackett: %s '%s' -> %d результатов", id, q, len(res))
		all = append(all, res...)
	}
	if len(all) == 0 && len(errs) > 0 {
		log.Printf("jackett: '%s' — все индексера ошиблись: %s", q, strings.Join(errs, "; "))
		return nil, fmt.Errorf("jackett: %s", strings.Join(errs, "; "))
	}
	all = dedupJackettResults(all)
	// НЕ обрезаем объединение до limit: каждый индексера уже ограничен своим
	// limit в searchIndexer, а обрезка ИТОГА до лимита приводила к тому, что
	// результаты первого индексера в списке заполняли весь лимит и вытесняли
	// реальные раздачи остальных. Например, при поиске обычного фильма
	// аниме-трекер anilibria (стоит первым) отдаёт мусорные аниме — и они
	// занимали все 8 слотов, а настоящие раздачи с переводами (rutracker,
	// rutor, megapeer) отбрасывались.
	log.Printf("jackett: '%s' -> всего %d результатов (после дедупа)", q, len(all))
	return all, nil
}

// searchIndexer опрашивает один индексера по Torznab.
func (j *Jackett) searchIndexer(ctx context.Context, id, q string, limit int) ([]Result, error) {
	params := url.Values{}
	params.Set("apikey", j.apiKey)
	params.Set("t", "search")
	params.Set("q", q)
	params.Set("limit", strconv.Itoa(limit))
	u := fmt.Sprintf("%s/api/v2.0/indexers/%s/results/torznab/api?%s",
		j.baseURL, url.PathEscape(id), params.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := j.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jackett: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jackett: %s (url=%s)", resp.Status, u)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	// Jackett отдаёт ошибки в теле XML даже при HTTP 200 — превращаем их
	// в настоящие ошибки, чтобы не получить молча «пусто» (и логировалось
	// в sources_bg, а не терялось).
	if m := jtErrorRe.FindStringSubmatch(string(body)); len(m) == 3 {
		return nil, fmt.Errorf("jackett: %s (code=%s)", strings.TrimSpace(m[2]), m[1])
	}
	items := parseJackettItems(string(body))
	if len(items) > limit {
		items = items[:limit]
	}
	if len(items) == 0 {
		return nil, nil
	}

	// Для каждой раздачи получаем магнет: из ссылки (если уже magnet),
	// иначе качаем .torrent через Jackett и строим magnet.
	var (
		wg  sync.WaitGroup
		sem = make(chan struct{}, 3)
		mu  sync.Mutex
		out = make([]Result, len(items))
	)
	for i := range items {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			it := items[i]
			m := it.magnet
			if m == "" && it.downloadURL != "" {
				m, _ = j.downloadMagnet(ctx, it.downloadURL)
			}
			if m == "" {
				return
			}
			mu.Lock()
			out[i] = Result{Title: it.Title, Size: it.sizeHuman, Seeds: it.seeds, Magnet: m}
			mu.Unlock()
		}(i)
	}
	wg.Wait()

	results := make([]Result, 0, len(out))
	for _, r := range out {
		if r.Magnet != "" {
			results = append(results, r)
		}
	}
	return results, nil
}

// magnetInfoHash извлекает xt=urn:btih:<hex> из magnet-ссылки (40 или 32
// символа) для дедупликации.
func magnetInfoHash(m string) string {
	const p = "xt=urn:btih:"
	i := strings.Index(strings.ToLower(m), p)
	if i < 0 {
		return ""
	}
	h := m[i+len(p):]
	if k := strings.IndexAny(h, "&?"); k >= 0 {
		h = h[:k]
	}
	if len(h) == 40 || len(h) == 32 {
		return strings.ToLower(h)
	}
	return ""
}

// dedupJackettResults оставляет первую запись по info_hash — один и тот же
// релиз может найтись на нескольких индексерах.
func dedupJackettResults(in []Result) []Result {
	seen := make(map[string]struct{}, len(in))
	out := make([]Result, 0, len(in))
	for _, r := range in {
		key := magnetInfoHash(r.Magnet)
		if key == "" {
			out = append(out, r)
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, r)
	}
	return out
}

// parseJackettItems разбирает Torznab-RSS в список записей.
func parseJackettItems(xml string) []jackettItem {
	var out []jackettItem
	for _, block := range jtItemRe.FindAllString(xml, -1) {
		var it jackettItem
		if m := jtTitleRe.FindStringSubmatch(block); len(m) == 2 {
			it.Title = html.UnescapeString(strings.TrimSpace(m[1]))
		}
		if m := jtEnclosureRe.FindStringSubmatch(block); len(m) == 2 {
			u := html.UnescapeString(strings.TrimSpace(m[1]))
			if strings.HasPrefix(u, "magnet:") {
				it.magnet = u
			} else if u != "" {
				it.downloadURL = u
			}
		}
		for _, m := range jtAttrRe.FindAllStringSubmatch(block, -1) {
			switch strings.ToLower(m[1]) {
			case "seeders":
				it.seeds, _ = strconv.Atoi(m[2])
			case "size":
				if n, err := strconv.ParseInt(m[2], 10, 64); err == nil {
					it.sizeHuman = formatBytes(n)
				}
			}
		}
		// Размер не всегда приходит как torznab:attr — некоторые индексера
		// отдают его прямым тегом <size> (если attr не разобрался).
		if it.sizeHuman == "" {
			if m := jtSizeRe.FindStringSubmatch(block); len(m) == 2 {
				if n, err := strconv.ParseInt(strings.TrimSpace(m[1]), 10, 64); err == nil {
					it.sizeHuman = formatBytes(n)
				}
			}
		}
		if it.magnet == "" {
			// Некоторые индексера отдают magnet прямо в <link>.
			if m := jtLinkRe.FindStringSubmatch(block); len(m) == 2 {
				l := html.UnescapeString(strings.TrimSpace(m[1]))
				if strings.HasPrefix(l, "magnet:") {
					it.magnet = l
				}
			}
		}
		if it.Title == "" {
			continue
		}
		out = append(out, it)
	}
	return out
}

// downloadMagnet скачивает .torrent через Jackett и строит magnet-ссылку.
func (j *Jackett) downloadMagnet(ctx context.Context, u string) (string, error) {
	// Jackett по умолчанию формирует ссылки на себя как http://localhost:9117,
	// а наш сервис может ходить к нему по другому адресу (например
	// http://jackett:9117 в docker-compose). Подменяем localhost/127.0.0.1
	// на реальный адрес Jackett из конфигурации.
	if parsed, err := url.Parse(u); err == nil {
		if h := parsed.Hostname(); h == "localhost" || h == "127.0.0.1" {
			if b, err2 := url.Parse(j.baseURL); err2 == nil && b.Host != "" {
				parsed.Scheme = b.Scheme
				parsed.Host = b.Host
				u = parsed.String()
			}
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := j.hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("jackett: download %s: %s", u, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", err
	}
	mi, err := metainfo.Load(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("jackett: parse torrent: %w", err)
	}
	h := mi.HashInfoBytes()
	var info *metainfo.Info
	if inf, err := mi.UnmarshalInfo(); err == nil {
		info = &inf
	}
	return mi.Magnet(&h, info).String(), nil
}

// formatBytes переводит размер в байтах в человекочитаемый вид ("1.46 GB").
func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	if exp >= len("KMGTPE") {
		exp = len("KMGTPE") - 1
		div = 1 << (10 * (exp + 1))
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

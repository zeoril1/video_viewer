package catalogapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/zeoril1/video_viewer/internal/tmdb"
)

type animeMedia struct {
	ID        int                                      `json:"id"`
	Type      string                                   `json:"type"`
	Format    string                                   `json:"format"`
	Title     struct{ Romaji, English, Native string } `json:"title"`
	Synonyms  []string                                 `json:"synonyms"`
	StartDate struct{ Year, Month, Day int }           `json:"startDate"`
	Relations struct {
		Edges []struct {
			Relation string      `json:"relationType"`
			Node     *animeMedia `json:"node"`
		} `json:"edges"`
	} `json:"relations"`
}

const animeFields = `id type format title { romaji english native } synonyms startDate { year month day }`

type animeProvider struct {
	client   *http.Client
	endpoint string
	mu       sync.Mutex
	next     time.Time
}

func (p *animeProvider) query(ctx context.Context, query string, variables any, dst any) error {
	// AniList's reduced limit can be 30 requests/minute; reserve one slot every 2.1s.
	p.mu.Lock()
	wait := time.Until(p.next)
	if wait < 0 {
		wait = 0
	}
	p.next = time.Now().Add(wait + 2100*time.Millisecond)
	p.mu.Unlock()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}
	body, _ := json.Marshal(map[string]any{"query": query, "variables": variables})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "video_viewer/1.0")
	res, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("anilist status %d", res.StatusCode)
	}
	var envelope struct {
		Data   json.RawMessage   `json:"data"`
		Errors []json.RawMessage `json:"errors"`
	}
	if err = json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&envelope); err != nil {
		return err
	}
	if len(envelope.Errors) > 0 || len(envelope.Data) == 0 {
		return fmt.Errorf("anilist query failed")
	}
	return json.Unmarshal(envelope.Data, dst)
}
func orderTitleKey(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, s)
}
func animeMatches(m animeMedia, f tmdb.Film) bool {
	if m.Type != "ANIME" || f.Year == 0 || m.StartDate.Year != f.Year {
		return false
	}
	// Remakes, recaps and live-action adaptations must never be merged by fuzzy title.
	if (f.Kind == "tvSeries") != (m.Format == "TV" || m.Format == "ONA" || m.Format == "TV_SHORT") {
		return false
	}
	names := append([]string{m.Title.English, m.Title.Native, m.Title.Romaji}, m.Synonyms...)
	for _, a := range names {
		for _, b := range []string{f.Title, f.TitleRU} {
			if a != "" && b != "" && orderTitleKey(a) == orderTitleKey(b) {
				return true
			}
		}
	}
	return false
}
func animeRelation(s string) bool {
	return s == "PREQUEL" || s == "SEQUEL" || s == "SIDE_STORY" || s == "PARENT" || s == "SUMMARY"
}

func (p *animeProvider) order(ctx context.Context, f tmdb.Film) (watchOrder, error) {
	result := watchOrder{Items: []orderItem{}, Source: "AniList", Mode: "release", Note: "Связанные аниме по дате выхода. Место спецвыпусков в сюжете может отличаться."}
	var search struct {
		Page struct {
			Media []animeMedia `json:"media"`
		} `json:"Page"`
	}
	err := p.query(ctx, `query($search:String){Page(perPage:10){media(search:$search,type:ANIME){`+animeFields+`}}}`, map[string]any{"search": f.Title}, &search)
	if err != nil {
		return result, err
	}
	var root *animeMedia
	for i := range search.Page.Media {
		if animeMatches(search.Page.Media[i], f) {
			if root != nil {
				return result, nil
			}
			root = &search.Page.Media[i]
		}
	}
	if root == nil {
		return result, nil
	}
	result.SourceURL = fmt.Sprintf("https://anilist.co/anime/%d", root.ID)
	result.Title = animeTitle(*root)
	queue := []int{root.ID}
	seen := map[int]bool{root.ID: true}
	nodes := map[int]animeMedia{root.ID: *root}
	extras := map[int]bool{}
	cursor := 0
	for batch := 0; cursor < len(queue) && batch < 12; batch++ {
		end := cursor + 8
		if end > len(queue) {
			end = len(queue)
		}
		var query strings.Builder
		query.WriteString("query {")
		for _, id := range queue[cursor:end] {
			fmt.Fprintf(&query, "m%d: Media(id:%d,type:ANIME){%s relations {edges {relationType node {%s}}}} ", id, id, animeFields, animeFields)
		}
		query.WriteString("}")
		var data map[string]animeMedia
		if err = p.query(ctx, query.String(), nil, &data); err != nil {
			return result, err
		}
		for _, id := range queue[cursor:end] {
			m := data[fmt.Sprintf("m%d", id)]
			if m.ID != id {
				return result, fmt.Errorf("anilist missing media")
			}
			nodes[m.ID] = m
			for _, edge := range m.Relations.Edges {
				node := edge.Node
				if node == nil || node.ID <= 0 || node.Type != "ANIME" || !animeRelation(edge.Relation) {
					continue
				}
				if !seen[node.ID] {
					if len(queue) >= 64 {
						result.Partial = true
						continue
					}
					seen[node.ID] = true
					queue = append(queue, node.ID)
					nodes[node.ID] = *node
					extras[node.ID] = edge.Relation == "SIDE_STORY" || edge.Relation == "SUMMARY"
				}
			}
		}
		cursor = end
	}
	result.Partial = result.Partial || cursor < len(queue)
	for _, m := range nodes {
		date := ""
		if m.StartDate.Year > 0 {
			date = fmt.Sprintf("%04d", m.StartDate.Year)
			if m.StartDate.Month > 0 {
				date += fmt.Sprintf("-%02d", m.StartDate.Month)
				if m.StartDate.Day > 0 {
					date += fmt.Sprintf("-%02d", m.StartDate.Day)
				}
			}
		}
		it := orderItem{Title: animeTitle(m), MatchTitles: []string{m.Title.English, m.Title.Romaji, m.Title.Native}, Date: date, ExternalURL: fmt.Sprintf("https://anilist.co/anime/%d", m.ID), Extra: extras[m.ID] || m.Format == "OVA" || m.Format == "SPECIAL", Current: m.ID == root.ID}
		if it.Current {
			it.ID = fmt.Sprintf("tmdb-%s-%d", filmMedia(f), f.TMDBID)
		}
		result.Items = append(result.Items, it)
	}
	sort.Slice(result.Items, func(i, j int) bool {
		a, b := result.Items[i], result.Items[j]
		if a.Date == b.Date {
			return a.ExternalURL < b.ExternalURL
		}
		if a.Date == "" {
			return false
		}
		if b.Date == "" {
			return true
		}
		return a.Date < b.Date
	})
	return result, nil
}
func animeTitle(m animeMedia) string {
	if m.Title.English != "" {
		return m.Title.English
	}
	if m.Title.Romaji != "" {
		return m.Title.Romaji
	}
	return m.Title.Native
}

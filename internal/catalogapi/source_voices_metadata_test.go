package catalogapi

import (
	"context"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/zeoril1/video_viewer/internal/db"
	"github.com/zeoril1/video_viewer/internal/magnet"
)

func TestSourceVoicesMergeByHashAcrossTrackerTokens(t *testing.T) {
	const hash = "0123456789abcdef0123456789abcdef01234567"
	bytes, _ := hex.DecodeString(hash)
	first := magnet.Result{Title: "Example (2026) WEB-DL", Magnet: "magnet:?xt=urn:btih:" + hash + "&tr=first", Seeds: 2,
		Voices: []string{"TVShows"}}
	second := first
	second.Magnet = "magnet:?xt=urn:btih:" + base32.StdEncoding.EncodeToString(bytes) + "&tr=second"
	second.Voices = []string{"MovieDalen", "TVShows"}
	items := runQueries(context.Background(), fakeSearch(map[string][]magnet.Result{"first": {first}, "second": {second}}, nil),
		"tt-voices", []sourceQuery{{q: "first"}, {q: "second"}}, "test", false)
	if len(items) != 1 || !reflect.DeepEqual(items[0].Voices, []string{"MovieDalen", "TVShows"}) {
		t.Fatalf("same hash metadata was lost: %+v", items)
	}
	if items[0].Title != first.Title || items[0].Magnet != first.Magnet {
		t.Fatal("voice enrichment changed the representative source")
	}
}

func TestSourceVoicesDifferentHashesWithEqualTitlesStaySeparate(t *testing.T) {
	first := magnet.Result{Title: "Example (2026) WEB-DL", Size: "10 GB", Magnet: "magnet:?xt=urn:btih:1111111111111111111111111111111111111111", Seeds: 2,
		Voices: []string{"MovieDalen"}}
	second := first
	second.Magnet = "magnet:?xt=urn:btih:2222222222222222222222222222222222222222"
	second.Voices = []string{"TVShows"}
	items := runQueries(context.Background(), fakeSearch(map[string][]magnet.Result{"query": {first, second}}, nil),
		"tt-voices", []sourceQuery{{q: "query"}}, "test", false)
	if len(items) != 2 || !reflect.DeepEqual(items[0].Voices, first.Voices) || !reflect.DeepEqual(items[1].Voices, second.Voices) {
		t.Fatalf("title/size dedup hid a distinct audio release: %+v", items)
	}
}

func TestSourceVoicesMergeAcrossSearchPassesWithoutMutatingInputs(t *testing.T) {
	const base = "magnet:?xt=urn:btih:1111111111111111111111111111111111111111"
	first := []sourceItem{{Title: "Example", Magnet: base + "&tr=first", Voices: []string{"TVShows"}}}
	second := []sourceItem{{Title: "Example alternate title", Magnet: base + "&tr=second", Voices: []string{"MovieDalen"}}}
	merged := mergeSourceItems(first, second)
	if len(merged) != 1 || !reflect.DeepEqual(merged[0].Voices, []string{"MovieDalen", "TVShows"}) {
		t.Fatalf("search passes lost studio metadata: %+v", merged)
	}
	if !reflect.DeepEqual(first[0].Voices, []string{"TVShows"}) {
		t.Fatal("merging changed the earlier search result in place")
	}
}

func TestSourceVoicesZeroSeedSeriesUsesOnlyNamedMetadata(t *testing.T) {
	first := magnet.Result{Title: "Example WEB-DL", Magnet: "magnet:?xt=urn:btih:1111111111111111111111111111111111111111",
		Voices: []string{"TVShows"}}
	second := first
	second.Magnet = "magnet:?xt=urn:btih:2222222222222222222222222222222222222222"
	second.Voices = []string{"", "  "}
	items := runQueries(context.Background(), fakeSearch(map[string][]magnet.Result{"query": {first, second}}, nil),
		"tt-voices", []sourceQuery{{q: "query"}}, "test", true)
	if len(items) != 1 || !reflect.DeepEqual(items[0].Voices, []string{"TVShows"}) {
		t.Fatalf("series must retain only the named zero-seed voice: %+v", items)
	}
}

type sourceVoicesRepo struct {
	*db.Repo
	film  db.Film
	saved []db.Source
}

func (r *sourceVoicesRepo) GetByIMDBID(context.Context, string) (db.Film, bool, error) {
	return r.film, true, nil
}
func (r *sourceVoicesRepo) SaveSources(_ context.Context, _ string, items []db.Source, _ ...bool) (int, int, int, error) {
	r.saved = items
	return len(items), 0, 0, nil
}

type sourceVoicesProvider struct{ items []magnet.Result }

func (sourceVoicesProvider) Name() string { return "test" }
func (p sourceVoicesProvider) Search(context.Context, string, int) ([]magnet.Result, error) {
	return p.items, nil
}

func TestSourceVoicesBackgroundSaveAndCachedAPIProjection(t *testing.T) {
	repo := &sourceVoicesRepo{film: db.Film{IMDBID: "tt-voices", Title: "Example", Kind: "feature", Year: 2026}}
	provider := sourceVoicesProvider{items: []magnet.Result{
		{Title: "Example (2026) WEB-DL", Seeds: 10, Magnet: "magnet:?xt=urn:btih:1111111111111111111111111111111111111111", Voices: []string{" TVShows ", "MovieDalen"}},
		{Title: "Example (2026) BDRip", Seeds: 5, Magnet: "magnet:?xt=urn:btih:2222222222222222222222222222222222222222"},
	}}
	manager := newSourcesManager(Config{Magnet: provider})
	manager.db = repo
	if err := manager.run(context.Background(), repo.film.IMDBID); err != nil {
		t.Fatal(err)
	}
	if len(repo.saved) != 2 || !reflect.DeepEqual(repo.saved[0].Voices, []string{"MovieDalen", "TVShows"}) {
		t.Fatalf("provider-to-DB conversion lost description voices: %+v", repo.saved)
	}
	items := sourceItemsFromDB(repo.saved, repo.film)
	encoded, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	var response []map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &response); err != nil {
		t.Fatal(err)
	}
	var names []string
	if len(response) != 2 || json.Unmarshal(response[0]["voices"], &names) != nil || !reflect.DeepEqual(names, []string{"MovieDalen", "TVShows"}) {
		t.Fatalf("cached source API lost explicit voice array: %s", encoded)
	}
	if _, exists := response[1]["voices"]; exists {
		t.Fatal("legacy source without studio metadata should omit the optional field")
	}
}

package catalogapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/zeoril1/video_viewer/internal/db"
	"github.com/zeoril1/video_viewer/internal/magnet"
)

func TestMovieSearchIncludesVoiceBeyondFirstTwelveTrackerResults(t *testing.T) {
	film := db.Film{
		IMDBID: "tt22084616", Kind: "feature", Year: 2026,
		Title: "Spider-Man: Brand New Day", TitleRU: "Человек-паук: Новый день",
	}
	// A tracker can list many quality/size variants of two voices before the
	// next studio. The mock honours the actual Torznab limit sent by Jackett.
	voices := []string{"HDRezka Studio", "LostFilm"}
	releases := make([]string, 13)
	for i := range releases {
		voice := voices[i%len(voices)]
		if i == len(releases)-1 {
			voice = "MovieDalen"
		}
		releases[i] = fmt.Sprintf("%s / %s (2026) WEB-DL 1080p | %s", film.TitleRU, film.Title, voice)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/api/v2.0/indexers/test/results/torznab/api" || r.URL.Query().Get("q") != "Человек-паук: Новый день 2026" {
			t.Error("unexpected tracker request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
		if err != nil || limit < 1 {
			t.Error("missing tracker result limit")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0"?><rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel>`)
		for i := 0; i < min(limit, len(releases)); i++ {
			fmt.Fprintf(w, `<item><title>%s</title><enclosure url="magnet:?xt=urn:btih:%040x" length="1000" type="application/x-bittorrent"/><torznab:attr name="seeders" value="10"/><torznab:attr name="size" value="%d"/></item>`, releases[i], i+1, int64(i+1)<<30)
		}
		fmt.Fprint(w, `</channel></rss>`)
	}))
	defer server.Close()

	tracker := magnet.NewJackett(server.URL, "test-key", "test", nil)
	items := runQueries(context.Background(), tracker.Search, film.IMDBID, sourceQueries(film), tracker.Name(), false)
	items = newFilmTitleMatcher(film).filter(film.IMDBID, items)
	if len(items) != len(releases) {
		t.Fatalf("the tracker result limit lost a later voice: got %d releases, want %d", len(items), len(releases))
	}
	found := false
	for _, item := range items {
		if strings.Contains(item.Title, "MovieDalen") {
			found = true
		}
	}
	if !found {
		t.Fatal("the thirteenth release's voice was lost before catalogue filtering")
	}
	if requests.Load() != 1 {
		t.Fatal("ready magnet results must not trigger torrent or video downloads")
	}
}

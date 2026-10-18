package catalogapi

import (
	"context"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/zeoril1/video_viewer/internal/db"
)

type checkSourceRepo struct {
	film    db.Film
	found   bool
	sources []db.Source
	getErr  error
	listErr error
}

func (r *checkSourceRepo) GetByIMDBID(context.Context, string) (db.Film, bool, error) {
	return r.film, r.found, r.getErr
}

func (r *checkSourceRepo) ListSources(context.Context, string) ([]db.Source, error) {
	return r.sources, r.listErr
}

func TestSourceCheckIdentity(t *testing.T) {
	film := db.Film{IMDBID: "tt35854736", TitleRU: "Аватар: Семь убежищ", Title: "Avatar: Seven Havens", Kind: "tvSeries"}
	const hash = "0123456789abcdef0123456789abcdef01234567"
	magnet := "magnet:?xt=urn:btih:" + hash
	const own = "Аватар: Семь убежищ / Avatar: Seven Havens [S01] WEB-DL 1080p"
	const foreign = "Аватар короля 2 / E01-E12 Quan Zhi Gao Shou 2 - AniLiberty.TOP [WEBRip 1080p]"
	hashBytes, _ := hex.DecodeString(hash)
	base32Magnet := "magnet:?xt=urn:btih:" + base32.StdEncoding.EncodeToString(hashBytes) + "&tr=https://new-tracker.invalid/announce"
	for _, tc := range []struct {
		name    string
		repo    checkSourceRepo
		magnet  string
		title   string
		torrent string
		status  string
		checked string
	}{
		{name: "cached wrong series overrides misleading caller title", repo: checkSourceRepo{film: film, found: true,
			sources: []db.Source{{InfoHash: hash, Magnet: magnet, Title: foreign}}}, title: own, status: "mismatch", checked: foreign},
		{name: "same hash with changed trackers and base32 spelling", repo: checkSourceRepo{film: film, found: true,
			sources: []db.Source{{InfoHash: strings.ToUpper(hash), Magnet: magnet + "&tr=https://old.invalid", Title: foreign}}},
			magnet: base32Magnet, status: "mismatch", checked: foreign},
		{name: "old cache without hash uses magnet identity", repo: checkSourceRepo{film: film, found: true,
			sources: []db.Source{{Magnet: magnet, Title: own}}}, magnet: base32Magnet, status: "matched", checked: own},
		{name: "valid manually selected source absent from cache", repo: checkSourceRepo{film: film, found: true},
			title: own, status: "matched", checked: own},
		{name: "wrong series absent from cache", repo: checkSourceRepo{film: film, found: true},
			title: foreign, status: "mismatch", checked: foreign},
		{name: "magnet display name fallback", repo: checkSourceRepo{film: film, found: true},
			magnet: magnet + "&dn=" + url.QueryEscape(own), status: "matched", checked: own},
		{name: "cached title takes precedence over generic torrent root", repo: checkSourceRepo{film: film, found: true,
			sources: []db.Source{{InfoHash: hash, Title: foreign}}}, torrent: "Season 1", status: "mismatch", checked: foreign},
		{name: "cached matching title overrides wrong torrent root", repo: checkSourceRepo{film: film, found: true,
			sources: []db.Source{{InfoHash: hash, Title: own}}}, torrent: foreign, status: "matched", checked: own},
		{name: "provided release title overrides wrong torrent root", repo: checkSourceRepo{film: film, found: true},
			title: own, torrent: foreign, status: "matched", checked: own},
		{name: "display name overrides generic torrent root", repo: checkSourceRepo{film: film, found: true},
			magnet: magnet + "&dn=" + url.QueryEscape(foreign), torrent: "Season 1", status: "mismatch", checked: foreign},
		{name: "generic display name is inconclusive", repo: checkSourceRepo{film: film, found: true},
			magnet: magnet + "&dn=Season+1", status: "unknown", checked: "Season 1"},
		{name: "release title overrides generic display name", repo: checkSourceRepo{film: film, found: true},
			magnet: magnet + "&dn=Season+1", title: foreign, torrent: own, status: "mismatch", checked: foreign},
		{name: "display name overrides wrong torrent root", repo: checkSourceRepo{film: film, found: true},
			magnet: magnet + "&dn=" + url.QueryEscape(own), torrent: foreign, status: "matched", checked: own},
		{name: "matching torrent root", repo: checkSourceRepo{film: film, found: true},
			torrent: own, status: "matched", checked: own},
		{name: "informative wrong Russian torrent root", repo: checkSourceRepo{film: film, found: true},
			torrent: "Аватар короля 2", status: "mismatch", checked: "Аватар короля 2"},
		{name: "informative wrong English torrent root", repo: checkSourceRepo{film: film, found: true},
			torrent: "The Kings Avatar", status: "mismatch", checked: "The Kings Avatar"},
		{name: "generic season torrent root", repo: checkSourceRepo{film: film, found: true},
			torrent: "Season 1", status: "unknown", checked: "Season 1"},
		{name: "season token torrent root", repo: checkSourceRepo{film: film, found: true},
			torrent: "S01", status: "unknown", checked: "S01"},
		{name: "uploader-only torrent root", repo: checkSourceRepo{film: film, found: true},
			torrent: "Releaser", status: "unknown", checked: "Releaser"},
		{name: "season encoding uploader torrent root", repo: checkSourceRepo{film: film, found: true},
			torrent: "S01.1080p.WEB-DL.Releaser", status: "unknown", checked: "S01.1080p.WEB-DL.Releaser"},
		{name: "season encoding unknown studio torrent root", repo: checkSourceRepo{film: film, found: true},
			torrent: "S01.1080p.WEB-DL.MysteryStudio", status: "unknown", checked: "S01.1080p.WEB-DL.MysteryStudio"},
		{name: "quality audio and known studios only", repo: checkSourceRepo{film: film, found: true},
			torrent: "1080p.WEB-DL.HDR10.DDP5.1.x265.LostFilm.NovaFilm", status: "unknown", checked: "1080p.WEB-DL.HDR10.DDP5.1.x265.LostFilm.NovaFilm"},
		{name: "multiword uploader-only torrent root", repo: checkSourceRepo{film: film, found: true},
			torrent: "New Studio [WEB-DL 1080p]", status: "unknown", checked: "New Studio [WEB-DL 1080p]"},
		{name: "generic Russian episode and format torrent root", repo: checkSourceRepo{film: film, found: true},
			torrent: "Сезон 1. Серии 01-12 [WEB-DL 1080p H.264 AAC 2.0]", status: "unknown", checked: "Сезон 1. Серии 01-12 [WEB-DL 1080p H.264 AAC 2.0]"},
		{name: "unknown single word torrent root", repo: checkSourceRepo{film: film, found: true},
			torrent: "Unrelated", status: "unknown", checked: "Unrelated"},
		{name: "matching single word torrent root", repo: checkSourceRepo{film: db.Film{Title: "Supernatural", Kind: "tvSeries"}, found: true},
			torrent: "Supernatural", status: "matched", checked: "Supernatural"},
		{name: "name missing", repo: checkSourceRepo{film: film, found: true}, status: "unknown"},
		{name: "film missing", repo: checkSourceRepo{}, title: own, status: "unknown"},
		{name: "film title missing", repo: checkSourceRepo{film: db.Film{IMDBID: film.IMDBID}, found: true}, title: own, status: "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := tc.magnet
			if candidate == "" {
				candidate = magnet
			}
			body, _ := json.Marshal(map[string]string{"magnet": candidate, "title": tc.title, "torrent_title": tc.torrent})
			request := httptest.NewRequest(http.MethodPost, "/api/films/"+film.IMDBID+"/source-check", strings.NewReader(string(body)))
			response := httptest.NewRecorder()
			handleFilmSourceCheck(&tc.repo, film.IMDBID)(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("HTTP %d: %s", response.Code, response.Body.String())
			}
			var result sourceCheckResult
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Status != tc.status || result.Title != tc.checked {
				t.Fatalf("verdict = %+v, want %s %q", result, tc.status, tc.checked)
			}
		})
	}
}

func TestSourceCheckErrorsDoNotBecomeMismatches(t *testing.T) {
	const body = `{"magnet":"magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567","title":"Example"}`
	for _, tc := range []struct {
		name string
		repo sourceCheckRepo
		body string
		code int
	}{
		{name: "no database", body: body, code: http.StatusServiceUnavailable},
		{name: "film database error", repo: &checkSourceRepo{getErr: errors.New("unavailable")}, body: body, code: http.StatusServiceUnavailable},
		{name: "source database error", repo: &checkSourceRepo{found: true, listErr: errors.New("unavailable")}, body: body, code: http.StatusServiceUnavailable},
		{name: "invalid magnet", body: `{"magnet":"https://invalid.example"}`, code: http.StatusBadRequest},
		{name: "invalid hash", body: `{"magnet":"magnet:?xt=urn:btih:invalid"}`, code: http.StatusBadRequest},
		{name: "oversized body", body: `{"title":"` + strings.Repeat("a", 64<<10) + `","magnet":"invalid"}`, code: http.StatusBadRequest},
		{name: "multiple JSON values", body: body + `{}`, code: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/source-check", strings.NewReader(tc.body))
			handleFilmSourceCheck(tc.repo, "tt35854736")(response, request)
			if response.Code != tc.code {
				t.Fatalf("HTTP %d, want %d: %s", response.Code, tc.code, response.Body.String())
			}
		})
	}
}

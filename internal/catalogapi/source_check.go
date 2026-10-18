package catalogapi

import (
	"context"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"github.com/zeoril1/video_viewer/internal/db"
)

type sourceCheckRepo interface {
	GetByIMDBID(context.Context, string) (db.Film, bool, error)
	ListSources(context.Context, string) ([]db.Source, error)
}

type sourceCheckResult struct {
	Status string `json:"status"`
	Title  string `json:"title,omitempty"`
}

var torrentContainerToken = regexp.MustCompile(`(?i)^(?:\d+|\d{3,4}p|\d{3,4}x\d{3,4}|\d+bit|[248]k|[sx]\d{1,3}(?:e\d{1,3})?|e\d{1,3}|[hx]\d{3}|hdr\d+|ddp?\d+)$`)

var torrentContainerWords = map[string]bool{
	"season": true, "seasons": true, "series": true, "episode": true, "episodes": true, "ep": true,
	"сезон": true, "сезоны": true, "сезонов": true, "сезона": true, "серия": true, "серии": true, "серий": true,
	"part": true, "часть": true, "collection": true, "complete": true, "full": true, "pack": true,
	"полный": true, "полная": true, "полностью": true, "сборник": true, "of": true,
	"web": true, "dl": true, "rip": true, "webrip": true, "webdl": true, "bdrip": true,
	"bdremux": true, "bd": true, "br": true, "bluray": true, "blu": true, "ray": true, "remux": true,
	"dvd": true, "dvdrip": true, "brrip": true, "hdtv": true, "hdrip": true, "uhd": true, "hdr": true, "sdr": true,
	"avc": true, "hevc": true, "h": true, "x": true, "aac": true, "ac3": true, "eac3": true, "dts": true,
	"dv": true, "dovi": true, "dolby": true, "vision": true, "truehd": true, "atmos": true, "dd": true, "ddp": true,
	"flac": true, "mp3": true, "opus": true,
	"mkv": true, "mp4": true, "avi": true, "rus": true, "eng": true, "multi": true,
	"group": true, "release": true, "releaser": true, "team": true, "rg": true, "рг": true, "tv": true, "top": true,
	"newstudio": true, "novafilm": true, "lostfilm": true, "tvshows": true, "aniliberty": true, "horriblesubs": true,
	"erairaws": true, "subsplease": true, "baibako": true, "kravec": true, "kravets": true,
}

// A torrent's root/display name is weaker evidence than a release title: it is
// often just a season folder, an encoding label, or an uploader's name. Require
// at least two remaining title words before treating a non-match as a wrong film.
// A matching title is accepted separately, including genuine one-word titles.
func informativeTorrentTitle(title string) bool {
	words := strings.FieldsFunc(normalizeMovieName(title), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	var names []string
	for _, word := range words {
		if !torrentContainerWords[word] && !torrentContainerToken.MatchString(word) {
			names = append(names, word)
		}
	}
	if len(names) < 2 {
		return false
	}
	// Common multi-word uploader labels do not identify the film either.
	switch strings.Join(names, "") {
	case "newstudio", "erairaws":
		return false
	}
	return true
}

// sourceCheckHash identifies a release independently of changing tracker URLs,
// display names, and the hexadecimal/base32 spelling of its BitTorrent hash.
func sourceCheckHash(magnet string) string {
	u, err := url.Parse(magnet)
	if err != nil || !strings.EqualFold(u.Scheme, "magnet") {
		return ""
	}
	for _, xt := range u.Query()["xt"] {
		if !strings.HasPrefix(strings.ToLower(xt), "urn:btih:") {
			continue
		}
		raw := xt[len("urn:btih:"):]
		var hash []byte
		switch len(raw) {
		case 40:
			hash, err = hex.DecodeString(raw)
		case 32:
			hash, err = base32.StdEncoding.DecodeString(strings.ToUpper(raw))
		default:
			continue
		}
		if err == nil && len(hash) == 20 {
			return hex.EncodeToString(hash)
		}
	}
	return ""
}

// handleFilmSourceCheck only checks identity against local metadata. In
// particular, an old manually selected release is not rejected merely because
// a tracker no longer returns it. Unknown names can be checked again after the
// already required torrent-file lookup returns its actual metadata title.
func handleFilmSourceCheck(repo sourceCheckRepo, filmID string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Magnet       string `json:"magnet"`
			Title        string `json:"title"`
			TorrentTitle string `json:"torrent_title"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			http.Error(w, "invalid source check", http.StatusBadRequest)
			return
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			http.Error(w, "invalid source check", http.StatusBadRequest)
			return
		}
		hash := sourceCheckHash(strings.TrimSpace(request.Magnet))
		if hash == "" || strings.TrimSpace(filmID) == "" {
			http.Error(w, "valid film and magnet required", http.StatusBadRequest)
			return
		}
		if repo == nil {
			http.Error(w, "source metadata unavailable", http.StatusServiceUnavailable)
			return
		}
		film, found, err := repo.GetByIMDBID(r.Context(), filmID)
		if err != nil {
			http.Error(w, "film metadata unavailable", http.StatusServiceUnavailable)
			return
		}
		result := sourceCheckResult{Status: "unknown"}
		if found {
			sources, err := repo.ListSources(r.Context(), filmID)
			if err != nil {
				http.Error(w, "source metadata unavailable", http.StatusServiceUnavailable)
				return
			}
			title := strings.TrimSpace(request.Title)
			weakTitle := false
			if title == "" {
				u, _ := url.Parse(request.Magnet)
				title = strings.TrimSpace(u.Query().Get("dn"))
				weakTitle = true
			}
			if title == "" {
				title = strings.TrimSpace(request.TorrentTitle)
				weakTitle = true
			}
			// Cached identity wins over a caller-supplied label for the same hash.
			for _, source := range sources {
				if strings.EqualFold(source.InfoHash, hash) || sourceCheckHash(source.Magnet) == hash {
					if cachedTitle := strings.TrimSpace(source.Title); cachedTitle != "" {
						title = cachedTitle
						weakTitle = false
						break
					}
				}
			}
			matcher := newFilmTitleMatcher(film)
			if title != "" && len(matcher.norm) > 0 {
				result.Title = title
				if matcher.matches(title) {
					result.Status = "matched"
				} else if !weakTitle || informativeTorrentTitle(title) {
					result.Status = "mismatch"
				}
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(result)
	}
}

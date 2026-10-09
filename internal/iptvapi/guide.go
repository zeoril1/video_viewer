package iptvapi

import (
	"fmt"
	"hash/fnv"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/zeoril1/video_viewer/internal/authn"
	"github.com/zeoril1/video_viewer/internal/db"
	"github.com/zeoril1/video_viewer/internal/iptv"
)

// Guide is paginated by channel and fetches programmes in one database query.
func (s *Server) handleGuide(w http.ResponseWriter, r *http.Request) {
	from, err := time.Parse(time.RFC3339, r.URL.Query().Get("from"))
	if err != nil {
		from = s.now().Truncate(time.Hour)
	}
	if from.Before(s.now().Add(-7*24*time.Hour)) || from.After(s.now().Add(3*24*time.Hour)) {
		http.Error(w, "date outside programme window", 400)
		return
	}
	offset := atoiOr(r.URL.Query().Get("offset"), 0)
	if offset < 0 {
		offset = 0
	}
	channels, err := s.cfg.DB.ListIPTVChannels(r.Context(), 0, r.URL.Query().Get("group"), r.URL.Query().Get("q"), 20, offset)
	if err != nil {
		http.Error(w, "database unavailable", 503)
		return
	}
	keys := []string{}
	for _, c := range channels {
		if c.EPGKey != "" {
			keys = append(keys, c.EPGKey)
		}
	}
	programs, err := s.cfg.DB.ListIPTVProgramsForKeys(r.Context(), keys, from, from.Add(6*time.Hour))
	if err != nil {
		http.Error(w, "database unavailable", 503)
		return
	}
	out := []map[string]any{}
	for _, ch := range channels {
		ps := []programme{}
		for _, p := range programs[ch.EPGKey] {
			ps = append(ps, programmeView(p))
		}
		out = append(out, map[string]any{"id": ch.ID, "name": ch.Name, "logo": ch.Logo, "catchup_days": ch.CatchupDays, "programs": ps})
	}
	authn.WriteJSON(w, map[string]any{"channels": out, "from": from.Format(time.RFC3339), "now": s.now().Format(time.RFC3339), "has_more": len(channels) == 20})
}

func archiveURL(ch db.IPTVChannel, pl db.IPTVPlaylist, start, stop time.Time) (string, error) {
	duration := int(stop.Sub(start).Seconds())
	if ch.CatchupMode == "xtream" && pl.Kind == "xtream" {
		u, err := url.Parse(pl.URL)
		if err != nil || u.Host == "" {
			return "", fmt.Errorf("invalid provider")
		}
		if _, err = strconv.ParseInt(ch.ExtID, 10, 64); err != nil {
			return "", fmt.Errorf("invalid stream")
		}
		// Xtream uses UTC start and a duration in minutes.
		if u.Scheme != "http" && u.Scheme != "https" {
			return "", fmt.Errorf("invalid provider scheme")
		}
		return u.Scheme + "://" + u.Host + fmt.Sprintf("/timeshift/%s/%s/%d/%s/%s.m3u8", url.PathEscape(pl.Username), url.PathEscape(pl.Password), (duration+59)/60, start.UTC().Format("2006-01-02:15-04"), ch.ExtID), nil
	}
	if ch.CatchupSource == "" || (ch.CatchupMode != "default" && ch.CatchupMode != "append" && ch.CatchupMode != "") {
		return "", fmt.Errorf("unsupported archive format")
	}
	raw := strings.NewReplacer("{utc}", strconv.FormatInt(start.Unix(), 10), "{utcend}", strconv.FormatInt(stop.Unix(), 10), "{start}", strconv.FormatInt(start.Unix(), 10), "{end}", strconv.FormatInt(stop.Unix(), 10), "{duration}", strconv.Itoa(duration)).Replace(ch.CatchupSource)
	if strings.ContainsAny(raw, "{}") {
		return "", fmt.Errorf("unsupported archive template")
	}
	if ch.CatchupMode == "append" {
		raw = ch.StreamURL + raw
	}
	base, err := url.Parse(ch.StreamURL)
	if err != nil {
		return "", err
	}
	ref, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	raw = base.ResolveReference(ref).String()
	if !iptv.IsStreamURL(raw) {
		return "", fmt.Errorf("invalid archive URL")
	}
	return raw, nil
}
func (s *Server) handleArchive(w http.ResponseWriter, r *http.Request) {
	id := atoi64(r.PathValue("id"))
	start, e1 := time.Parse(time.RFC3339, r.URL.Query().Get("start"))
	stop, e2 := time.Parse(time.RFC3339, r.URL.Query().Get("stop"))
	if id <= 0 || e1 != nil || e2 != nil || !stop.After(start) || stop.Sub(start) > 12*time.Hour {
		http.Error(w, "invalid programme", 400)
		return
	}
	ch, ok, err := s.cfg.DB.GetIPTVChannel(r.Context(), id)
	if err != nil {
		http.Error(w, "database unavailable", 503)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	if ch.CatchupDays <= 0 || start.Before(s.now().Add(-time.Duration(ch.CatchupDays)*24*time.Hour)) || stop.After(s.now()) {
		http.Error(w, "archive unavailable for this programme", 404)
		return
	}
	pl, ok, err := s.cfg.DB.GetIPTVPlaylist(r.Context(), ch.PlaylistID)
	if err != nil || !ok || !pl.Enabled {
		http.Error(w, "playlist unavailable", 404)
		return
	}
	raw, err := archiveURL(ch, pl, start, stop)
	if err != nil {
		http.Error(w, err.Error(), 422)
		return
	}
	// Archive sessions must not replace the live session of this channel or another programme.
	h := fnv.New64a()
	_, _ = h.Write([]byte(fmt.Sprintf("%d:%d:%d", id, start.Unix(), stop.Unix())))
	ch.ID = int64(h.Sum64()&((1<<61)-1)) | (1 << 61)
	ch.StreamURL = raw
	ch.IsHLS = true
	ch.IsArchive = true
	ua, ref := channelHeaders(ch.UA, ch.Referer, pl.UA, pl.Referer)
	var data []byte
	if r.URL.Query().Get("remux") == "1" {
		data, err = s.live.playlistFFmpeg(r.Context(), ch, ua, ref)
	} else {
		data, err = s.live.playlist(r.Context(), ch, ua, ref, pl)
	}
	if err != nil {
		http.Error(w, "provider archive unavailable", 502)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

package streamapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A seek used to be checked only for the existence of a playlist and codecs.
// Feed the real production HTTP mux enough B-frame media to cross fragment 44,
// then check decode times in the published bytes, including fragment boundaries.
func TestHLSFractionalSeekPreservesFragmentTimelineAndGeneration(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := filepath.Join(t.TempDir(), "source.mkv")
	// A low sample rate keeps this five-minute timeline quick to produce. PCM
	// input still exercises the AAC conversion used by the streaming service.
	create := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=64x64:rate=12",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=8000",
		"-t", "300", "-c:v", "libx264", "-preset", "ultrafast",
		"-g", "72", "-keyint_min", "72", "-sc_threshold", "0", "-bf", "3",
		"-c:a", "pcm_s16le", fixture)
	if output, err := create.CombinedOutput(); err != nil {
		t.Fatalf("create seek fixture: %v: %s", err, output)
	}
	m := newHLSManager("")
	m.dataDir = t.TempDir()
	m.probeCache[probeKey("tt-seek", "magnet", 0)] = probeResult{
		Duration: 300, VideoCodec: "h264", Tracks: []audioTrack{{Index: 1, Ordinal: 0, Codec: "pcm_s16le"}},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/stream/{id}", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, fixture)
	})
	mux.HandleFunc("PUT "+hlsOutputPath+"{token}/{name}", m.serveOutput)
	mux.HandleFunc("POST "+hlsOutputPath+"{token}/{name}", m.serveOutput)
	server := httptest.NewServer(mux)
	defer server.Close()
	defer m.stopAll()
	m.selfBase = server.URL
	var previous *hlsSession
	for _, seek := range []float64{0, 13.51} {
		t.Run(fmt.Sprintf("start-%.2f", seek), func(t *testing.T) {
			s, err := m.ensure(ctx, "tt-seek", "magnet", 0, 0, -1, seek, "source", "viewer")
			if err != nil {
				t.Fatal("start real HTTP remux", err)
			}
			select {
			case <-s.done:
			case <-ctx.Done():
				t.Fatal("remux did not finish", ctx.Err())
			}
			m.mu.Lock()
			exitErr := s.exitErr
			m.mu.Unlock()
			if exitErr != nil {
				t.Fatal("real HTTP remux failed", exitErr)
			}
			playlist, err := os.ReadFile(s.playlist)
			if err != nil || !bytes.Contains(playlist, []byte("#EXT-X-ENDLIST")) {
				t.Fatalf("missing completed output: %v: %s", err, playlist)
			}
			if !bytes.Contains(playlist, []byte("#EXT-X-MEDIA-SEQUENCE:0")) || bytes.Contains(playlist, []byte(s.outputToken)) {
				t.Fatal("new timeline or private output URLs are invalid", string(playlist))
			}
			init, err := os.ReadFile(filepath.Join(s.dir, "init.mp4"))
			if err != nil || len(init) == 0 {
				t.Fatal("missing initialization segment", err)
			}
			media := append([]byte(nil), init...)
			count := 0
			for _, line := range strings.Split(string(playlist), "\n") {
				name := strings.TrimSpace(line)
				if !strings.HasPrefix(name, "seg_") {
					continue
				}
				if !hlsSegmentName.MatchString(name) {
					t.Fatal("unexpected media name", name)
				}
				fragment, err := os.ReadFile(filepath.Join(s.dir, name))
				if err != nil || len(fragment) == 0 {
					t.Fatal("missing or empty published fragment", name, err)
				}
				media = append(media, fragment...)
				count++
			}
			if count < 45 {
				t.Fatalf("only %d fragments; regression must cross fragment 44", count)
			}
			joined := filepath.Join(t.TempDir(), "all-fragments.mp4")
			if err := os.WriteFile(joined, media, 0600); err != nil {
				t.Fatal(err)
			}
			probe := exec.CommandContext(ctx, ffprobe, "-v", "error", "-show_entries",
				"stream=index,codec_name:packet=stream_index,pts_time,dts_time,duration_time,flags", "-of", "json", joined)
			output, err := probe.CombinedOutput()
			if err != nil {
				t.Fatalf("probe actual fragment bytes: %v: %s", err, output)
			}
			assertHLSDecodeTimeline(t, output, 300-seek)
			if previous != nil {
				if previous.dir == s.dir || previous.outputToken == s.outputToken || previous.generation >= s.generation {
					t.Fatal("seek shared its predecessor's directory, token or generation")
				}
				if _, err := os.Stat(previous.dir); !os.IsNotExist(err) {
					t.Fatal("old output survived the restart", err)
				}
				for _, generation := range []uint64{previous.generation, s.generation} {
					q := url.Values{"magnet": {"magnet"}, "session": {"viewer"}, "file": {"0"}, "track": {"0"}, "generation": {strconv.FormatUint(generation, 10)}}
					r := httptest.NewRequest(http.MethodGet, "/?"+q.Encode(), nil)
					r.SetPathValue("id", "tt-seek")
					r.SetPathValue("name", "init.mp4")
					w := httptest.NewRecorder()
					m.serveSegment(w, r)
					if generation == previous.generation {
						if w.Code != http.StatusNotFound {
							t.Fatal("old generation received a successor's init", w.Code)
						}
					} else if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), init) || w.Header().Get("Cache-Control") != "no-store" {
						t.Fatal("new generation did not receive its complete private init", w.Code)
					}
				}
			}
			previous = s
		})
	}
}

func assertHLSDecodeTimeline(t *testing.T, data []byte, remaining float64) {
	t.Helper()
	var parsed struct {
		Streams []struct {
			Index int    `json:"index"`
			Codec string `json:"codec_name"`
		} `json:"streams"`
		Packets []struct {
			Stream   int    `json:"stream_index"`
			PTS      string `json:"pts_time"`
			DTS      string `json:"dts_time"`
			Duration string `json:"duration_time"`
			Flags    string `json:"flags"`
		} `json:"packets"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	codecs := make(map[int]string)
	for _, stream := range parsed.Streams {
		codecs[stream.Index] = stream.Codec
	}
	if len(codecs) != 2 || codecs[0] != "h264" || codecs[1] != "aac" {
		t.Fatal("unexpected remux codecs", codecs)
	}
	type timeline struct {
		first, last, duration float64
		count                 int
	}
	tracks := make(map[int]timeline)
	parse := func(value string) float64 {
		v, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			t.Fatalf("invalid timestamp %q: %v", value, err)
		}
		return v
	}
	for _, packet := range parsed.Packets {
		dts, pts, duration := parse(packet.DTS), parse(packet.PTS), parse(packet.Duration)
		track := tracks[packet.Stream]
		if track.count == 0 {
			track.first = dts
			if dts < -0.01 || dts > 1 || pts < -0.01 || pts > 1 {
				t.Fatalf("new %s timeline did not start near zero: DTS %.6f, PTS %.6f", codecs[packet.Stream], dts, pts)
			}
			if packet.Stream == 0 && !strings.Contains(packet.Flags, "K") {
				t.Fatal("copied video did not start with a keyframe")
			}
		} else {
			gap := dts - (track.last + track.duration)
			if dts <= track.last || math.Abs(gap) > 0.01 {
				t.Fatalf("%s discontinuity at packet %d, including fragment boundaries: previous %.6f+%.6f, next %.6f", codecs[packet.Stream], track.count, track.last, track.duration, dts)
			}
		}
		track.last, track.duration = dts, duration
		track.count++
		tracks[packet.Stream] = track
	}
	for index, track := range tracks {
		// Stream copy may preserve one preceding six-second GOP. This is a
		// bounded seek preroll, rather than an absolute timestamp after restart.
		if track.count < 100 || track.last < remaining-1 || track.last > remaining+7 {
			t.Fatalf("%s timeline has a missing/truncated tail or retained absolute times: %+v, remaining %.2f", codecs[index], track, remaining)
		}
	}
	if len(tracks) != 2 {
		t.Fatal("missing a media timeline", tracks)
	}
}

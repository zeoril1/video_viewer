package segmentdetect

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"math/bits"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/zeoril1/video_viewer/internal/segments"
)

// Changing multiband notes exercise the spectral pipeline rather than copying
// hash values. These are not a substitute for a labelled real-show corpus.
func signal(seed int64, seconds int) []byte {
	return musicalSignal(seed, seconds, frameSamples)
}

func musicalSignal(seed int64, seconds, phraseSamples int) []byte {
	random := rand.New(rand.NewSource(seed))
	pcm := make([]byte, seconds*sampleRate*2)
	for start := 0; start < seconds*sampleRate; start += phraseSamples {
		var frequencies, phases, amplitudes [8]float64
		for i := range frequencies {
			frequencies[i] = 100 + random.Float64()*1650
			phases[i] = random.Float64() * 2 * math.Pi
			amplitudes[i] = 0.025 + random.Float64()*0.06
		}
		for sample := 0; sample < phraseSamples && start+sample < seconds*sampleRate; sample++ {
			var value float64
			for i, frequency := range frequencies {
				value += amplitudes[i] * math.Sin(2*math.Pi*frequency*float64(sample)/sampleRate+phases[i])
			}
			binary.LittleEndian.PutUint16(pcm[(start+sample)*2:], uint16(int16(value*32767)))
		}
	}
	return pcm
}

func TestPCMRecognizesShiftedSignalWithDifferentGain(t *testing.T) {
	aPCM, bPCM := signal(40, 180), signal(41, 180)
	start, target, duration := 10, 30, 30
	for i := 0; i < duration*sampleRate; i++ {
		sample := int16(binary.LittleEndian.Uint16(aPCM[(start*sampleRate+i)*2:]))
		binary.LittleEndian.PutUint16(bPCM[(target*sampleRate+i)*2:], uint16(sample/2))
	}
	aHash, err := fingerprintPCM(context.Background(), bytes.NewReader(aPCM), 180)
	if err != nil {
		t.Fatal(err)
	}
	bHash, err := fingerprintPCM(context.Background(), bytes.NewReader(bPCM), 180)
	if err != nil {
		t.Fatal(err)
	}
	a, b := record(1, 1, 180), record(2, 2, 180)
	a.Fingerprint, b.Fingerprint = aHash.Frames, bHash.Frames
	x, y := Detect(a, b)
	if len(findKind(x, segments.Intro)) != 1 || len(findKind(y, segments.Intro)) != 1 {
		t.Fatalf("PCM opening not recognized: %+v %+v", x, y)
	}
	if x[0].Start < 10 || x[0].Start > 11 || x[0].End < 39 || y[0].Start < 30 || y[0].Start > 31 || y[0].End < 59 {
		t.Fatalf("wrong PCM range: %+v %+v", x, y)
	}
}

func TestExtractPreservesDelayedAudioTimeline(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed on this test host")
	}
	dir := t.TempDir()
	audio, video := filepath.Join(dir, "delayed.wav"), filepath.Join(dir, "delayed.mkv")
	pcm := signal(60, 4)
	writeWAV(t, audio, pcm)
	// The video starts at zero while the audio starts five seconds later. PCM
	// output has no timestamps of its own, so Extract must materialize that gap.
	cmd := exec.Command("ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "color=c=black:s=32x32:r=2:d=9",
		"-itsoffset", "5", "-i", audio, "-map", "0:v:0", "-map", "1:a:0",
		"-c:v", "ffv1", "-c:a", "pcm_s16le", "-threads", "1", video)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create delayed audio fixture: %v %s", err, out)
	}
	got, err := Extract(context.Background(), video, 9)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Frames) != 18 {
		t.Fatalf("timeline length: %d", len(got.Frames))
	}
	for i := 0; i < 10; i++ {
		if got.Frames[i] != 0 {
			t.Fatalf("delayed audio shifted to frame %d: %v", i, got.Frames)
		}
	}
	want, err := fingerprintPCM(context.Background(), bytes.NewReader(pcm), 4)
	if err != nil {
		t.Fatal(err)
	}
	for i := range want.Frames {
		if bits.OnesCount32(got.Frames[10+i]^want.Frames[i]) > 1 {
			t.Fatalf("delayed audio fingerprint misplaced at %d", i)
		}
	}
}

func TestPCMRecognizesSubFrameOpeningShift(t *testing.T) {
	// Stretch the changing notes to musical phrases. The episode offset is
	// deliberately not a multiple of the half-second fingerprint step.
	shared := musicalSignal(42, 120, 4*frameSamples)
	aPCM, bPCM := signal(43, 480), signal(44, 480)
	copy(aPCM[20*sampleRate*2:], shared)
	copy(bPCM[(60*sampleRate+sampleRate/4)*2:], shared)
	aHash, err := fingerprintPCM(context.Background(), bytes.NewReader(aPCM), 480)
	if err != nil {
		t.Fatal(err)
	}
	bHash, err := fingerprintPCM(context.Background(), bytes.NewReader(bPCM), 480)
	if err != nil {
		t.Fatal(err)
	}
	a, b := record(1, 1, 480), record(2, 2, 480)
	a.Fingerprint, b.Fingerprint = aHash.Frames, bHash.Frames
	x, y := Detect(a, b)
	if len(findKind(x, segments.Intro)) == 0 || len(findKind(y, segments.Intro)) == 0 {
		t.Fatalf("sub-frame PCM opening not recognized: %+v %+v", x, y)
	}
	if x[0].End-x[0].Start < 100 || y[0].End-y[0].Start < 100 {
		t.Fatalf("opening fragmented by fractional offset: %+v %+v", x, y)
	}
}

func TestPCMSilenceAndSteadyToneAreUninformative(t *testing.T) {
	for _, tone := range []bool{false, true} {
		pcm := make([]byte, sampleRate*20*2)
		if tone {
			for i := 0; i < len(pcm)/2; i++ {
				binary.LittleEndian.PutUint16(pcm[i*2:], uint16(int16(10000*math.Sin(2*math.Pi*440.133*float64(i)/sampleRate))))
			}
		}
		fingerprint, err := fingerprintPCM(context.Background(), bytes.NewReader(pcm), 20)
		if err != nil {
			t.Fatal(err)
		}
		if informative(fingerprint.Frames, 8) {
			t.Fatalf("silence/tone accepted: %v", fingerprint.Frames)
		}
	}
}

func TestPCMUnrelatedSignalsDoNotMatch(t *testing.T) {
	aHash, err := fingerprintPCM(context.Background(), bytes.NewReader(signal(54, 180)), 180)
	if err != nil {
		t.Fatal(err)
	}
	bHash, err := fingerprintPCM(context.Background(), bytes.NewReader(signal(55, 180)), 180)
	if err != nil {
		t.Fatal(err)
	}
	a, b := record(1, 1, 180), record(2, 2, 180)
	a.Fingerprint, b.Fingerprint = aHash.Frames, bHash.Frames
	if x, y := Detect(a, b); len(x) > 0 || len(y) > 0 {
		t.Fatalf("unrelated PCM detected as skips: %+v %+v", x, y)
	}
}

func TestFingerprintPCMRejectsTruncationAndLimits(t *testing.T) {
	if _, err := fingerprintPCM(context.Background(), bytes.NewReader([]byte{1}), 1); err == nil {
		t.Fatal("accepted an incomplete PCM sample")
	}
	if _, err := fingerprintPCM(context.Background(), bytes.NewReader(make([]byte, frameSamples*2*4)), 0.5); err == nil {
		t.Fatal("accepted excessive output")
	}
	for _, duration := range []float64{0, -1, 21601, math.NaN(), math.Inf(1)} {
		if _, err := Extract(context.Background(), "unused", duration); err == nil {
			t.Fatalf("accepted duration %v", duration)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fingerprintPCM(ctx, bytes.NewReader([]byte{0, 0}), 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestFingerprintPartialAudioKeepsMediaTimeline(t *testing.T) {
	pcm := signal(47, 1)
	fingerprint, err := fingerprintPCM(context.Background(), bytes.NewReader(pcm), 2.5)
	if err != nil {
		t.Fatal(err)
	}
	if fingerprint.Step != Step || len(fingerprint.Frames) != 5 || fingerprint.Frames[2] != 0 || fingerprint.Frames[4] != 0 {
		t.Fatalf("shorter audio timeline: %+v", fingerprint)
	}
}

func writeWAV(t *testing.T, path string, pcm []byte) {
	t.Helper()
	var header [44]byte
	copy(header[:], "RIFF")
	binary.LittleEndian.PutUint32(header[4:], uint32(len(pcm)+36))
	copy(header[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(header[16:], 16)
	binary.LittleEndian.PutUint16(header[20:], 1)
	binary.LittleEndian.PutUint16(header[22:], 1)
	binary.LittleEndian.PutUint32(header[24:], sampleRate)
	binary.LittleEndian.PutUint32(header[28:], sampleRate*2)
	binary.LittleEndian.PutUint16(header[32:], 2)
	binary.LittleEndian.PutUint16(header[34:], 16)
	copy(header[36:], "data")
	binary.LittleEndian.PutUint32(header[40:], uint32(len(pcm)))
	if err := os.WriteFile(path, append(header[:], pcm...), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestExtractThroughFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed on this test host")
	}
	pcm := signal(50, 4)
	path := filepath.Join(t.TempDir(), "audio.wav")
	writeWAV(t, path, pcm)
	got, err := Extract(context.Background(), path, 4)
	if err != nil {
		t.Fatal(err)
	}
	want, err := fingerprintPCM(context.Background(), bytes.NewReader(pcm), 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Frames) != len(want.Frames) {
		t.Fatalf("ffmpeg output length: %d != %d", len(got.Frames), len(want.Frames))
	}
	for i := range got.Frames {
		if bits.OnesCount32(got.Frames[i]^want.Frames[i]) > 1 {
			t.Fatalf("ffmpeg altered PCM fingerprint at %d", i)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Extract(ctx, path, 4); !errors.Is(err, context.Canceled) {
		t.Fatalf("ffmpeg cancellation lost: %v", err)
	}
}

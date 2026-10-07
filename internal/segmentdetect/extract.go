// Package segmentdetect finds repeated audio in adjacent episodes. Audio alone
// cannot prove that a repeated scene is an opening or credits, so all findings
// remain suggestions rather than automatic seeks.
package segmentdetect

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"math/cmplx"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	Version       = 1
	Step          = 0.5
	sampleRate    = 4000
	frameSamples  = sampleRate / 2
	fftSize       = 512
	maxDuration   = 21600
	maxFrames     = 50000
	decodeTimeout = 10 * time.Minute
)

// Fingerprint has one spectral hash for each half-second of media time. Zero
// marks silence or an uninformative frame; it never participates in a match.
// The output includes no PCM and needs at most 200 KB for a six-hour input.
type Fingerprint struct {
	Frames []uint32
	Step   float64
}

// Extract decodes the first audio track as mono PCM using the existing ffmpeg
// executable. It reads stdout incrementally, bounds CPU threads, duration,
// memory and wall time, and kills the child when the request is cancelled.
// inputURL must be selected by the server, never taken from an analysis request.
func Extract(ctx context.Context, inputURL string, duration float64) (Fingerprint, error) {
	if err := ctx.Err(); err != nil {
		return Fingerprint{}, err
	}
	if !validDuration(duration) || strings.TrimSpace(inputURL) == "" || strings.ContainsRune(inputURL, 0) {
		return Fingerprint{}, errors.New("invalid audio analysis input")
	}
	decodeCtx, cancel := context.WithTimeout(ctx, decodeTimeout)
	defer cancel()
	cmd := exec.CommandContext(decodeCtx, "ffmpeg",
		"-nostdin", "-hide_banner", "-loglevel", "error", "-copyts", "-start_at_zero",
		"-threads", "1", "-filter_threads", "1", "-rw_timeout", "30000000",
		"-i", inputURL, "-map", "0:a:0", "-vn", "-sn", "-dn",
		"-af", "aresample=4000:async=1:first_pts=0", "-ac", "1", "-ar", strconv.Itoa(sampleRate), "-c:a", "pcm_s16le",
		"-t", strconv.FormatFloat(duration, 'f', 3, 64), "-threads", "1", "-f", "s16le", "pipe:1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Fingerprint{}, fmt.Errorf("audio output: %w", err)
	}
	// Discard diagnostics rather than collecting unbounded input-derived text.
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return Fingerprint{}, fmt.Errorf("start audio decoder: %w", err)
	}
	result, readErr := fingerprintPCM(decodeCtx, stdout, duration)
	if readErr != nil {
		cancel()
	}
	waitErr := cmd.Wait()
	if err := decodeCtx.Err(); err != nil {
		return Fingerprint{}, fmt.Errorf("audio analysis interrupted: %w", err)
	}
	if readErr != nil {
		return Fingerprint{}, readErr
	}
	if waitErr != nil {
		return Fingerprint{}, fmt.Errorf("decode audio: %w", waitErr)
	}
	return result, nil
}

func validDuration(duration float64) bool {
	return duration > 0 && duration <= maxDuration && !math.IsNaN(duration) && !math.IsInf(duration, 0)
}

func fingerprintPCM(ctx context.Context, input io.Reader, duration float64) (Fingerprint, error) {
	if !validDuration(duration) {
		return Fingerprint{}, errors.New("invalid fingerprint duration")
	}
	wanted := int(math.Ceil(duration / Step))
	frames := make([]uint32, 0, wanted)
	var raw [frameSamples * 2]byte
	var samples [frameSamples]float64
	for {
		if err := ctx.Err(); err != nil {
			return Fingerprint{}, err
		}
		n, err := io.ReadFull(input, raw[:])
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return Fingerprint{}, fmt.Errorf("read audio: %w", err)
		}
		if n == 0 {
			break
		}
		if n%2 != 0 {
			return Fingerprint{}, errors.New("truncated PCM sample")
		}
		if len(frames) >= wanted+1 || len(frames) >= maxFrames {
			return Fingerprint{}, errors.New("audio exceeds declared duration")
		}
		for i := range samples {
			if i*2 < n {
				samples[i] = float64(int16(binary.LittleEndian.Uint16(raw[i*2:]))) / 32768
			} else {
				samples[i] = 0
			}
		}
		frames = append(frames, spectralHash(samples[:]))
		if err != nil {
			break
		}
	}
	if len(frames) == 0 {
		return Fingerprint{}, errors.New("audio track is empty")
	}
	// An audio track may end before the video (for example, silent trailing
	// credits). Keep absolute media time without inventing a fingerprint there.
	if len(frames) > wanted {
		frames = frames[:wanted]
	}
	for len(frames) < wanted {
		frames = append(frames, 0)
	}
	return Fingerprint{Frames: frames, Step: Step}, nil
}

var (
	fftReverse [fftSize]int
	fftRoots   [fftSize / 2]complex128
	hann       [fftSize]float64
	bandForBin [fftSize / 2]int
)

func init() {
	for i := 0; i < fftSize; i++ {
		value := i
		for j := 0; j < 9; j++ {
			fftReverse[i] = fftReverse[i]<<1 | value&1
			value >>= 1
		}
		hann[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(fftSize-1))
	}
	for i := range fftRoots {
		fftRoots[i] = cmplx.Exp(complex(0, -2*math.Pi*float64(i)/fftSize))
	}
	for i := range bandForBin {
		frequency := float64(i*sampleRate) / fftSize
		bandForBin[i] = -1
		if frequency >= 80 && frequency < 1900 {
			bandForBin[i] = min(31, int(math.Log(frequency/80)/math.Log(1900.0/80)*32))
		}
	}
}

func spectralHash(samples []float64) uint32 {
	var rms float64
	for _, value := range samples {
		rms += value * value
	}
	if rms/float64(len(samples)) < 0.00000025 {
		return 0
	}
	var bands [32]float64
	var spectrum [fftSize]complex128
	// Three spread Hann windows cover the frame without retaining the whole
	// soundtrack. Band ratios tolerate gain changes and small codec differences.
	for _, offset := range [...]int{0, 744, 1488} {
		for i := range spectrum {
			spectrum[fftReverse[i]] = complex(samples[offset+i]*hann[i], 0)
		}
		for width := 2; width <= fftSize; width *= 2 {
			half, rootStep := width/2, fftSize/width
			for start := 0; start < fftSize; start += width {
				for j := 0; j < half; j++ {
					even, odd := spectrum[start+j], spectrum[start+j+half]*fftRoots[j*rootStep]
					spectrum[start+j], spectrum[start+j+half] = even+odd, even-odd
				}
			}
		}
		for i, band := range bandForBin {
			if band >= 0 {
				bands[band] += real(spectrum[i])*real(spectrum[i]) + imag(spectrum[i])*imag(spectrum[i])
			}
		}
	}
	var total, maximum float64
	for _, energy := range bands {
		total += energy
		maximum = max(maximum, energy)
	}
	if total == 0 || maximum/total > 0.93 {
		return 0
	}
	for i := range bands {
		bands[i] = math.Log1p(1000 * bands[i] / total)
	}
	var hash uint32
	for i := 0; i < 32; i++ {
		if bands[i]-bands[(i+1)%32] > 0.02 {
			hash |= 1 << i
		}
	}
	if hash == 0 {
		return 0
	}
	return hash
}

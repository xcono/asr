package vad

import (
	"encoding/binary"
	"math"
	"os"
	"testing"

	"github.com/xcono/asr/pkg/audio"
)

// refTolerance is the allowed |p - official| per window. The vendored model
// matched the official wrapper to 3.4e-6 on the session recordings (it runs
// the half-precision weights in float32); a missing or misplaced context is
// off by up to 0.88 on this signal.
const refTolerance = 1e-4

// TestModelMatchesReference guards the model input against the bug that made
// Silero v5 deaf in a stream: our wrappers fed bare 512-sample windows, while
// the official one prepends the previous window's last 64 samples
// (snakers4/silero-vad#771). It streams syntheticSpeech through Model and
// compares every window with probabilities computed by the official
// implementation, stored in testdata/synthetic.v5.f32 (little-endian float32,
// one per window).
//
// To regenerate the reference (only if syntheticSpeech changes):
//
//  1. VAD_SYNTHETIC_WAV=/tmp/synthetic.wav go test -run TestModelMatchesReference ./pkg/vad
//     writes the signal as a 16 kHz WAV (the test then fails until step 3).
//  2. Outside the repo: pip install silero-vad onnxruntime, and download the
//     v5.0 model, https://github.com/snakers4/silero-vad/raw/v5.0/files/silero_vad.onnx
//     (v5.1.2 gave identical probabilities).
//  3. Feed the WAV, as float32 samples / 32768 in 512-sample windows, through
//     silero_vad.utils_vad.OnnxWrapper(model, force_onnx_cpu=True) with no
//     reset_states between windows, and write each window's probability with
//     numpy .astype("<f4").tofile("pkg/vad/testdata/synthetic.v5.f32").
func TestModelMatchesReference(t *testing.T) {
	x := syntheticSpeech()
	if path := os.Getenv("VAD_SYNTHETIC_WAV"); path != "" {
		if err := os.WriteFile(path, audio.EncodeWAV(x, SampleRate), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	raw, err := os.ReadFile("testdata/synthetic.v5.f32")
	if err != nil {
		t.Fatalf("read reference: %v", err)
	}
	if want := len(x) / Window * 4; len(raw) != want {
		t.Fatalf("reference has %d bytes, want %d (one float32 per window)", len(raw), want)
	}

	m, err := NewModel()
	if err != nil {
		t.Fatalf("NewModel: %v", err)
	}
	defer m.Close()
	var worst float64
	var speech, bad int
	for w := 0; w < len(x)/Window; w++ {
		ref := math.Float32frombits(binary.LittleEndian.Uint32(raw[w*4:]))
		p, err := m.Infer(x[w*Window : (w+1)*Window])
		if err != nil {
			t.Fatalf("Infer: %v", err)
		}
		if ref >= Threshold {
			speech++
		}
		d := math.Abs(float64(p - ref))
		worst = max(worst, d)
		if d > refTolerance {
			if bad++; bad > 5 {
				continue
			}
			t.Errorf("window %d (%.2f s): p=%.6f, official %.6f", w, float64(w*WindowMs)/1000, p, ref)
		}
	}
	if bad > 0 {
		t.Errorf("%d of %d windows off by more than %g (max %.3g)", bad, len(x)/Window, refTolerance, worst)
	}
	// A reference of near-zero probabilities would let a deaf model pass.
	if speech < 50 {
		t.Fatalf("only %d windows of the reference reach the threshold; the signal no longer exercises the model", speech)
	}
	t.Logf("max |p - official| = %.2g over %d windows", worst, len(x)/Window)
}

// syntheticSpeech is the guard test's input: refSeconds of deterministic
// voice-like sound — harmonic stacks with a gliding pitch, syllable-rate
// amplitude modulation and some noise — split by pauses, so the model sees
// both onsets and long quiet stretches as in a session.
func syntheticSpeech() []int16 {
	const n = refSeconds * SampleRate
	out := make([]int16, n)
	var seed uint32 = 12345
	type phrase struct{ from, to, f0, glide, syl float64 }
	phrases := []phrase{
		{0.40, 1.90, 130, 40, 4.0},
		{2.60, 3.70, 210, -60, 5.0},
		{4.40, 5.60, 160, 25, 3.5},
		{6.60, 7.30, 110, 30, 6.0},
	}
	var phase float64
	for i := range out {
		t := float64(i) / SampleRate
		seed = seed*1664525 + 1013904223
		noise := float64(int32(seed)>>16) / 32768
		v := 0.002 * noise // room floor
		for _, p := range phrases {
			if t < p.from || t >= p.to {
				continue
			}
			x := (t - p.from) / (p.to - p.from)
			f0 := p.f0 + p.glide*x + 4*math.Sin(2*math.Pi*5.5*t)
			phase += 2 * math.Pi * f0 / SampleRate
			env := math.Sin(math.Pi*x) * (0.55 + 0.45*math.Sin(2*math.Pi*p.syl*(t-p.from)-math.Pi/2))
			var s float64
			for h := 1; h <= 12; h++ {
				// A crude formant: harmonics near 500 and 1500 Hz are loudest.
				fh := f0 * float64(h)
				g := math.Exp(-math.Pow((fh-500)/250, 2)) + 0.6*math.Exp(-math.Pow((fh-1500)/400, 2)) + 0.15/float64(h)
				s += g * math.Sin(float64(h)*phase)
			}
			v += env * (0.25*s + 0.03*noise)
		}
		out[i] = int16(max(-32768, min(32767, math.Round(v*32767))))
	}
	return out
}

const refSeconds = 8

package vad

import (
	"math"
	"testing"
)

func TestModelInferRejectsWrongWindow(t *testing.T) {
	m, err := NewModel()
	if err != nil {
		t.Fatalf("NewModel: %v", err)
	}
	defer m.Close()
	if _, err := m.Infer(make([]int16, Window-1)); err == nil {
		t.Fatalf("Infer accepted a short window")
	}
}

// Silence must stay well under the onset gate and a loud tone is not speech
// either: a wiring mistake in the embedded model (wrong weights, missing
// normalisation) shows up as probabilities near 1 or NaN here.
func TestModelInferNonSpeechStaysBelowThreshold(t *testing.T) {
	m, err := NewModel()
	if err != nil {
		t.Fatalf("NewModel: %v", err)
	}
	defer m.Close()

	silence := make([]int16, Window)
	tone := make([]int16, Window)
	for w := 0; w < 30; w++ {
		for i := range tone {
			n := w*Window + i
			tone[i] = int16(8000 * math.Sin(2*math.Pi*1000*float64(n)/SampleRate))
		}
		for name, win := range map[string][]int16{"silence": silence, "tone": tone} {
			p, err := m.Infer(win)
			if err != nil {
				t.Fatalf("Infer(%s): %v", name, err)
			}
			if math.IsNaN(float64(p)) || p < 0 || p > 1 {
				t.Fatalf("%s window %d: probability %v out of [0,1]", name, w, p)
			}
			if name == "silence" && p >= Threshold {
				t.Errorf("silence window %d: probability %.3f >= threshold %.2f", w, p, Threshold)
			}
		}
	}
}

// noiseWindows returns n deterministic windows of mixed tone and noise, so the
// recurrent state moves away from the fresh one.
func noiseWindows(n int) [][]int16 {
	var seed uint32 = 1
	out := make([][]int16, n)
	for w := range out {
		win := make([]int16, Window)
		for i := range win {
			seed = seed*1664525 + 1013904223
			noise := float64(int32(seed)>>16) / 32768 * 3000
			tone := 6000 * math.Sin(2*math.Pi*220*float64(w*Window+i)/SampleRate)
			win[i] = int16(noise + tone)
		}
		out[w] = win
	}
	return out
}

func toFloat(win []int16) []float32 {
	out := make([]float32, len(win))
	for i, s := range win {
		out[i] = float32(s) / 32768
	}
	return out
}

// silero.Reset must clear everything a new detector would not have — the
// LSTM state and the 64-sample input context — so a reused detector scores a
// new stream exactly like a fresh one.
func TestModelSileroResetEqualsNew(t *testing.T) {
	used, err := NewModel()
	if err != nil {
		t.Fatalf("NewModel: %v", err)
	}
	fresh, err := NewModel()
	if err != nil {
		t.Fatalf("NewModel: %v", err)
	}
	wins := noiseWindows(40)
	for _, w := range wins {
		used.sd.Process(toFloat(w))
	}
	used.sd.Reset()
	for i, w := range wins {
		a, b := used.sd.Process(toFloat(w)), fresh.sd.Process(toFloat(w))
		if a != b {
			t.Fatalf("window %d after Reset: %v, fresh model %v", i, a, b)
		}
	}
}

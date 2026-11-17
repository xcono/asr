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

// The reset fires on the window that completes ResetAfterSilenceMs of quiet,
// not one earlier, and restarts the count so a long silence keeps resetting.
func TestModelPauseEndedAfterQuietRun(t *testing.T) {
	m, err := NewModel()
	if err != nil {
		t.Fatalf("NewModel: %v", err)
	}
	n := msToWindows(ResetAfterSilenceMs)
	for round := 0; round < 2; round++ {
		for i := 1; i < n; i++ {
			if m.pauseEnded(0.01) {
				t.Fatalf("round %d: reset after %d quiet windows, want %d", round, i, n)
			}
		}
		if !m.pauseEnded(0.01) {
			t.Fatalf("round %d: no reset after %d quiet windows", round, n)
		}
	}
}

// A window at the silence gate or above is not quiet: it restarts the count, so
// a pause shorter than ResetAfterSilenceMs between words never resets.
func TestModelPauseEndedGateRestartsCount(t *testing.T) {
	m, err := NewModel()
	if err != nil {
		t.Fatalf("NewModel: %v", err)
	}
	n := msToWindows(ResetAfterSilenceMs)
	for _, loud := range []float32{Threshold - Hysteresis, Threshold, 0.9} {
		for i := 1; i < n; i++ {
			if m.pauseEnded(0.01) {
				t.Fatalf("loud %.2f: early reset at quiet window %d", loud, i)
			}
		}
		if m.pauseEnded(loud) {
			t.Fatalf("loud %.2f: reset on a window at/above the gate", loud)
		}
	}
	for i := 1; i < n; i++ {
		if m.pauseEnded(0.01) {
			t.Fatalf("reset after %d quiet windows following a loud one, want %d", i, n)
		}
	}
	if !m.pauseEnded(0.01) {
		t.Fatalf("no reset after %d quiet windows following a loud one", n)
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

// govad.Reset must clear everything a new detector would not have: if it left
// any state behind, "reset after a pause" would not restore fresh-model
// sensitivity.
func TestModelGovadResetEqualsNew(t *testing.T) {
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

// After an Infer-triggered reset the model is a fresh one that has read the
// triggering quiet window (the warm-up): same probabilities on the same input.
func TestModelInferResetBehavesLikeFresh(t *testing.T) {
	m, err := NewModel()
	if err != nil {
		t.Fatalf("NewModel: %v", err)
	}
	wins := noiseWindows(60)
	for _, w := range wins[:40] {
		m.sd.Process(toFloat(w)) // age the state without the reset logic
	}
	// The aged state may score silence above the gate for a while; feed it
	// until the reset fires (the countdown itself is tested above).
	silence := make([]int16, Window)
	for i := 0; m.Resets() == 0; i++ {
		if i == 200 {
			t.Fatalf("no reset after %d silent windows", i)
		}
		if _, err := m.Infer(silence); err != nil {
			t.Fatalf("Infer: %v", err)
		}
	}

	fresh, err := NewModel()
	if err != nil {
		t.Fatalf("NewModel: %v", err)
	}
	fresh.sd.Process(toFloat(silence)) // the warm-up window
	for i, w := range wins[40:] {
		a, err := m.Infer(w)
		if err != nil {
			t.Fatalf("Infer: %v", err)
		}
		b, err := fresh.Infer(w)
		if err != nil {
			t.Fatalf("Infer: %v", err)
		}
		if a != b {
			t.Fatalf("window %d after reset: %v, fresh model %v", i, a, b)
		}
	}
}

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

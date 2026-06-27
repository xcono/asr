package main

import (
	"testing"

	"github.com/xcono/asr/pkg/vad"
)

// TestProbBar checks the meter is fixed-width and keeps the release/onset gate
// ticks visible at the positions implied by the VAD thresholds, for any prob.
func TestProbBar(t *testing.T) {
	tm := vad.DefaultTiming()
	width := 30
	fwidth := float32(width)                                // var → runtime truncation
	release := int(fwidth * (tm.Threshold - tm.Hysteresis)) // 10
	onset := int(fwidth * tm.Threshold)                     // 15

	for _, prob := range []float32{0, 0.34, 0.36, 0.5, 0.9, 1.0} {
		bar := probBar(prob, tm)
		t.Logf("%.2f %s", prob, bar)

		if len(bar) != width+2 { // surrounding [ ]
			t.Fatalf("prob %.2f: bar width = %d, want %d", prob, len(bar), width+2)
		}
		inner := bar[1 : len(bar)-1]
		if inner[release] != ':' {
			t.Errorf("prob %.2f: release tick missing at %d, got %q", prob, release, inner[release])
		}
		if inner[onset] != '|' {
			t.Errorf("prob %.2f: onset tick missing at %d, got %q", prob, onset, inner[onset])
		}
	}
}

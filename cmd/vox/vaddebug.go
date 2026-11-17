package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/xcono/asr/pkg/asr"
	"github.com/xcono/asr/pkg/vad"
)

// debugDetector wraps an asr.Detector and tees each window's Silero speech
// probability to w (stderr under -vad-debug). It changes no behaviour — it
// returns the inner result unchanged — so the loop runs exactly as in
// production. The point is to see whether prob actually crosses the onset
// threshold during a sentence that never printed "speaking started" (a missed
// onset) versus the audio never arriving.
type debugDetector struct {
	inner  asr.Detector
	w      io.Writer
	timing vad.Timing
}

func (d debugDetector) Infer(window []int16) (float32, error) {
	prob, err := d.inner.Infer(window)
	if err == nil {
		fmt.Fprintf(d.w, "vad %.2f %s\n", prob, probBar(prob, d.timing))
	}
	return prob, err
}

// probBar renders prob in [0,1] as a fixed-width meter with ticks at the release
// (Threshold-Hysteresis) and onset (Threshold) levels from t, so it's obvious at
// a glance whether a window counts as speech (':' = release gate, '|' = onset
// gate; '#' = filled). Ticks take precedence over fill so the gates stay visible,
// and they move when the thresholds change in config.json.
func probBar(prob float32, t vad.Timing) string {
	const width = 30
	filled := max(0, min(int(prob*width), width))
	fwidth := float32(width)                              // float var → runtime (not constant) truncation
	release := int(fwidth * (t.Threshold - t.Hysteresis)) // release gate
	onset := int(fwidth * t.Threshold)                    // onset gate

	var b strings.Builder
	b.WriteByte('[')
	for i := range width {
		switch {
		case i == onset:
			b.WriteByte('|')
		case i == release:
			b.WriteByte(':')
		case i < filled:
			b.WriteByte('#')
		default:
			b.WriteByte(' ')
		}
	}
	b.WriteByte(']')
	return b.String()
}

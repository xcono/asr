package vad

import (
	"fmt"

	"github.com/zserge/govad"
)

// Model wraps the Silero detector: it normalises an int16 window to float32
// [-1,1] and returns the per-window speech probability. The detector is
// stateful across calls (recurrent state) — never Reset between windows of a
// continuous stream.
//
// Inference is pure Go (zserge/govad, Silero v5 weights embedded in the
// binary): no cgo, no model file. The timing consts in this package are tuned
// for v5 probabilities — a different Silero generation (v6) scores the same
// audio differently and means re-tuning them.
type Model struct {
	sd  *govad.VAD
	buf []float32
}

// NewModel builds a detector from the embedded Silero v5 weights.
func NewModel() (*Model, error) {
	sd, err := govad.New()
	if err != nil {
		return nil, fmt.Errorf("vad: load embedded weights: %w", err)
	}
	return &Model{sd: sd, buf: make([]float32, Window)}, nil
}

// Infer returns the Silero speech probability for one window. window must be
// exactly Window samples: a shorter window would leave the previous window's
// tail in the buffer and skew the probability, so a length mismatch returns an
// error rather than running on stale data.
func (m *Model) Infer(window []int16) (float32, error) {
	if len(window) != Window {
		return 0, fmt.Errorf("vad: infer window len %d != %d", len(window), Window)
	}
	for i, s := range window {
		m.buf[i] = float32(s) / 32768
	}
	return m.sd.Process(m.buf), nil
}

// Close is a no-op: the pure-Go detector holds no native resources. It stays so
// owners can keep a uniform teardown list.
func (m *Model) Close() error { return nil }

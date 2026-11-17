package vad

import (
	"fmt"

	"github.com/xcono/asr/pkg/vad/internal/silero"
)

// Model wraps the Silero detector: it normalises an int16 window to float32
// [-1,1] and returns the per-window speech probability.
//
// The detector is stateful across calls: the recurrent LSTM state and the
// 64-sample input context (the tail of the previous window). Silero v5 needs
// that context — the official wrapper feeds 64 + 512 samples per window — and
// without it the model goes deaf in a continuous stream: on a 304 s session
// recording 27 of 124 loud bursts scored 0.01-0.2 and fell outside every
// segment (snakers4/silero-vad#771). Our earlier wrappers (silero-vad-onnx-go,
// then upstream govad) both fed bare 512-sample windows; it was masked by
// resetting the state after every 500 ms pause. With the context the
// probabilities match the official implementation (to 3.4e-6 on the session
// recordings, guarded by TestModelMatchesReference) and no reset is needed:
// 1 of 124 bursts missed, as with the official wrapper. Model never resets
// the state itself — one Model per stream.
//
// Inference is pure Go (pkg/vad/internal/silero, vendored zserge/govad with
// the context added; Silero v5 weights embedded in the binary): no cgo, no
// model file. The timing consts in this package are tuned for v5
// probabilities — a different Silero generation (v6) scores the same audio
// differently and means re-tuning them.
type Model struct {
	sd  *silero.VAD
	buf []float32
}

// NewModel builds a detector from the embedded Silero v5 weights.
func NewModel() (*Model, error) {
	sd, err := silero.New()
	if err != nil {
		return nil, fmt.Errorf("vad: load embedded weights: %w", err)
	}
	return &Model{sd: sd, buf: make([]float32, Window)}, nil
}

// Infer returns the Silero speech probability for one window. window must be
// exactly Window samples: a shorter window would leave the previous window's
// tail in the buffer and skew the probability, so a length mismatch returns an
// error rather than running on stale data. Windows must be consecutive audio:
// each one's last 64 samples are the next one's context.
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

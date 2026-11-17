package vad

import (
	"fmt"

	"github.com/zserge/govad"
)

// Model wraps the Silero detector: it normalises an int16 window to float32
// [-1,1] and returns the per-window speech probability.
//
// The detector is stateful across calls (recurrent LSTM state), and Model
// manages that state itself: inside speech it is never touched, and after
// ResetAfterSilenceMs of windows below the silence gate (Threshold -
// Hysteresis) it is reset. A long-running stream degrades the state until loud
// words score near zero (numbers on ResetAfterSilenceMs); a reset during a
// pause restores fresh-model sensitivity for the next word. The reset keys on
// probability alone, not on the FSM state, so it also fires in the release
// tail of an open segment: on the session recordings that cost nothing (no
// phrase was split, the same bursts were caught as with a reset only while the
// FSM was silent) and it keeps Model free of an FSM dependency. The window
// counts against the package consts, not a configured Timing: NewModel takes
// no Timing so asr.Detector stays a one-method interface.
//
// After the reset the model re-reads the quiet window that triggered it. A
// fresh state overshoots on its first window of faint sound (0.3-0.5 on a
// -55 dBFS rustle), and when that window happened to follow a reset the
// overshoot alone confirmed an onset: two 0.7 s segments with no loud sound on
// the 304 s recording. One window of warm-up removed both and missed nothing
// more.
//
// Inference is pure Go (zserge/govad, Silero v5 weights embedded in the
// binary): no cgo, no model file. govad's state is only the LSTM h/c (it
// reflect-pads each window instead of carrying the previous window's tail), so
// govad.Reset is equivalent to a new detector. The timing consts in this
// package are tuned for v5 probabilities — a different Silero generation (v6)
// scores the same audio differently and means re-tuning them.
type Model struct {
	sd  *govad.VAD
	buf []float32

	quiet      int // consecutive windows below the silence gate
	resetAfter int // quiet windows that trigger a state reset
	resets     int
}

// NewModel builds a detector from the embedded Silero v5 weights.
func NewModel() (*Model, error) {
	sd, err := govad.New()
	if err != nil {
		return nil, fmt.Errorf("vad: load embedded weights: %w", err)
	}
	return &Model{
		sd:         sd,
		buf:        make([]float32, Window),
		resetAfter: msToWindows(ResetAfterSilenceMs),
	}, nil
}

// Infer returns the Silero speech probability for one window. window must be
// exactly Window samples: a shorter window would leave the previous window's
// tail in the buffer and skew the probability, so a length mismatch returns an
// error rather than running on stale data.
//
// The returned probability is always the one from the state before any reset
// this call performs; the reset affects the next window on.
func (m *Model) Infer(window []int16) (float32, error) {
	if len(window) != Window {
		return 0, fmt.Errorf("vad: infer window len %d != %d", len(window), Window)
	}
	for i, s := range window {
		m.buf[i] = float32(s) / 32768
	}
	p := m.sd.Process(m.buf)
	if m.pauseEnded(p) {
		m.sd.Reset()
		m.sd.Process(m.buf) // warm-up on the quiet window; see the Model comment
		m.resets++
	}
	return p, nil
}

// pauseEnded counts consecutive windows below the silence gate and reports true
// on the window that completes resetAfter of them, restarting the count so a
// long silence resets every resetAfter windows (re-arming once per pause missed
// one more burst on the recordings). A window at or above the gate restarts it:
// that is speech or the dead zone, where the state must be left alone.
func (m *Model) pauseEnded(p float32) bool {
	if p >= Threshold-Hysteresis {
		m.quiet = 0
		return false
	}
	m.quiet++
	if m.quiet < m.resetAfter {
		return false
	}
	m.quiet = 0
	return true
}

// Resets reports how many times the recurrent state was reset after a pause
// (diagnostics for cmd/vadscan).
func (m *Model) Resets() int { return m.resets }

// Close is a no-op: the pure-Go detector holds no native resources. It stays so
// owners can keep a uniform teardown list.
func (m *Model) Close() error { return nil }

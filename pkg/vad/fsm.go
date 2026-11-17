package vad

// Transition is what FSM.Update reports for the window just processed.
type Transition int

const (
	None Transition = iota
	ToSpeaking
	ToSilence
)

// FSM is a hysteresis state machine over Silero's per-window probability. It
// holds a binary speaking state and flips once enough evidence for the opposite
// state accumulates. speechRun/silenceRun are LEAKY counters: a speech window
// builds speechRun and bleeds one off silenceRun (and vice-versa), so a brief
// dip between words decrements evidence by one instead of zeroing it — sustained
// but choppy speech still crosses the onset threshold, and a short pause inside
// an utterance is forgiven gradually rather than at once.
//
// The leak holds WITHIN a state only. A flip zeroes the counter that argues for
// the state just left, because that evidence was gathered before the flip and
// says nothing about how long the new state has lasted. Without the reset the
// clamp kept it: after a long idle silenceRun sat at releaseWindow, the onset
// bled off only onsetWindows of it, and that many silence windows topped it
// back up — segments closed 64–128 ms after opening instead of after ReleaseMs,
// chopping phrases into fragments. The full release applied only once speech
// had run for a whole release window. The mirror case (speechRun surviving a
// release and confirming the next onset early) needs a release window shorter
// than the onset budget, so the defaults never hit it; the reset covers it too.
type FSM struct {
	speaking      bool
	speechRun     int
	silenceRun    int
	onsetWindows  int
	releaseWindow int
	threshold     float32
	hysteresis    float32
}

// NewFSM builds the state machine from t. The window counts and the prob gates
// are snapshot here, so a swept Timing (run flags) takes effect per run. Derived
// window counts live on Timing (OnsetWindows/ReleaseWindows); the pre-roll is
// sized from Timing.PrerollSamples, deliberately larger than the onset budget so
// a confirmed onset recovers the sub-threshold lead-in.
func NewFSM(t Timing) *FSM {
	return &FSM{
		onsetWindows:  t.OnsetWindows(),
		releaseWindow: t.ReleaseWindows(),
		threshold:     t.Threshold,
		hysteresis:    t.Hysteresis,
	}
}

func msToWindows(ms int) int {
	n := ms / WindowMs
	if n < 1 {
		n = 1
	}
	return n
}

// Update feeds one window's probability and reports whether the binary state
// flipped on this window.
func (f *FSM) Update(prob float32) Transition {
	switch {
	case prob >= f.threshold:
		if f.speechRun < f.onsetWindows {
			f.speechRun++
		}
		if f.silenceRun > 0 {
			f.silenceRun--
		}
	case prob < f.threshold-f.hysteresis:
		if f.silenceRun < f.releaseWindow {
			f.silenceRun++
		}
		if f.speechRun > 0 {
			f.speechRun--
		}
	default:
		// Dead zone: hold both runs.
	}

	switch {
	case !f.speaking && f.speechRun >= f.onsetWindows:
		f.speaking = true
		f.silenceRun = 0 // evidence from before the onset; see the FSM comment
		return ToSpeaking
	case f.speaking && f.silenceRun >= f.releaseWindow:
		f.speaking = false
		f.speechRun = 0 // evidence from before the release; see the FSM comment
		return ToSilence
	default:
		return None
	}
}

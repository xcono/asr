// Package vad is the Silero VAD engine: feed it audio windows, it reports
// speech-state transitions. It owns the Silero model, the hysteresis state
// machine, the pre-roll ring, and the end-of-turn timer — the agent loop
// orchestrates these primitives but contains none of their logic.
//
// The 16 kHz / 512-sample window is Silero's native contract, not a tunable.
package vad

const (
	SampleRate = 16000
	Window     = 512                        // Silero's native 16 kHz window
	WindowMs   = Window * 1000 / SampleRate // 32 ms per window

	// Silero's onset threshold. Speech starts when prob >= Threshold and ends
	// when prob drops below (Threshold - Hysteresis). Tuned low (0.30) on the rig
	// to catch soft one-syllable words ("Да") — this pairs with elevated mic gain
	// (~150%). It is environment-specific: re-tune with -vad-debug (read the peak
	// of a soft word, set threshold a hair under it). Lower = more sensitive =
	// more false triggers, which matters most on the cmd/act barge-in path.
	Threshold  = float32(0.30)
	Hysteresis = float32(0.15)

	// Barge-in budget: above-threshold audio needed before confirming speech.
	// 64 ms (2 windows) — short enough to latch one-syllable words; the pre-roll
	// recovers any lead-in. Lower still risks noise/breath false-triggering onset.
	BargeInMs = 64
	// Sustained silence before declaring Silence. In the batch path (asr.Listen,
	// used by `make run` and cmd/act) this IS the turn-end: too short and natural
	// within-sentence pauses (clause breaks, breaths, hesitation — commonly
	// 200–600 ms) close the turn early and chop a sentence into fragments. 700 ms
	// rides over those at the cost of ~380 ms more end-of-turn latency.
	ReleaseMs = 700
	// Continuous silence that ends a streaming turn (triggers finish).
	EndOfTurnMs = 800

	// PrerollMs of audio is retained ahead of a confirmed onset. The FSM needs
	// BargeInMs of above-threshold evidence to flip to Speaking, but Silero's
	// probability ramps through the sub-threshold/dead-zone region first, so a
	// word's true onset precedes confirmation by more than BargeInMs. Sizing the
	// ring from BargeInMs alone clips that lead-in (soft consonants, short leading
	// words); PrerollMs adds margin to recover it.
	PrerollMs = 300
)

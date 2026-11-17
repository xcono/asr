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

	// ResetAfterSilenceMs of consecutive windows below the silence gate
	// (Threshold - Hysteresis) makes Model reset Silero's recurrent state. In a
	// continuous stream Silero v5 goes deaf: loud words (windows up to
	// -14 dBFS) scored 0.01-0.2 and never opened a segment, while the same
	// samples from a clean state scored 0.8-0.99 — the owner heard it as 3-4
	// words in a row lost. It is the model, not govad (govad matched ONNX
	// Runtime to 4e-6 on the same 304 s recording); why the state degrades is
	// not known. Measured on two session recordings (304 s / 79 s; a loud
	// burst = windows >= -35 dBFS, gaps <= 3 windows, >= 3 windows long):
	// without the reset 27 of 124 and 12 of 40 bursts fell outside every
	// segment, with it 1 and 0; no segment without loud sound was added, the
	// mean segment peak rose from 0.68 to 0.94, and no phrase longer than 2 s
	// was split. 250 ms scored the same at twice the resets; 1 s missed 5 and
	// 3 s missed 13 on the long recording (measured before the warm-up in
	// Model). Rounded down to whole windows: 15 windows = 480 ms.
	ResetAfterSilenceMs = 500
)

package vad

// Timing holds the tunable VAD timings. The zero value is not valid — start
// from DefaultTiming() (seeded from the package consts, the measured defaults)
// and override fields. The run command builds one from flags so the timings can
// be swept live with -vad-debug without editing consts and recompiling.
type Timing struct {
	Threshold   float32 // onset prob gate: a window is speech when prob >= Threshold
	Hysteresis  float32 // release gate = Threshold-Hysteresis; in between is the dead zone
	BargeInMs   int     // above-threshold evidence needed to confirm onset (→ Speaking)
	ReleaseMs   int     // silence evidence needed to confirm offset (batch turn-end)
	EndOfTurnMs int     // continuous silence that ends a streaming turn
	PrerollMs   int     // lead-in audio retained ahead of a confirmed onset
}

// DefaultTiming returns the measured defaults from the package consts.
func DefaultTiming() Timing {
	return Timing{
		Threshold:   Threshold,
		Hysteresis:  Hysteresis,
		BargeInMs:   BargeInMs,
		ReleaseMs:   ReleaseMs,
		EndOfTurnMs: EndOfTurnMs,
		PrerollMs:   PrerollMs,
	}
}

// OnsetWindows is the above-threshold window count needed to confirm speech.
func (t Timing) OnsetWindows() int { return msToWindows(t.BargeInMs) }

// ReleaseWindows is the silence window count needed to confirm offset.
func (t Timing) ReleaseWindows() int { return msToWindows(t.ReleaseMs) }

// EndOfTurnWindows is the continuous-silence window count that ends a stream turn.
func (t Timing) EndOfTurnWindows() int { return msToWindows(t.EndOfTurnMs) }

// PrerollSamples is the pre-roll ring capacity in samples: PrerollMs rounded to
// whole Silero windows. Sized independently of the onset budget so a confirmed
// onset recovers the full sub-threshold lead-in, not just the above-threshold
// portion (otherwise soft/short leading words clip).
func (t Timing) PrerollSamples() int { return msToWindows(t.PrerollMs) * Window }

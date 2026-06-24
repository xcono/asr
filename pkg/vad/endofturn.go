package vad

// EndOfTurn counts continuous silence and fires once it reaches EndOfTurnMs,
// marking the end of a streaming turn. Speech resets the countdown; the dead
// zone between onset and release thresholds holds it (consistent with FSM).
type EndOfTurn struct {
	run        int
	windows    int
	threshold  float32
	hysteresis float32
}

func NewEndOfTurn(t Timing) *EndOfTurn {
	return &EndOfTurn{
		windows:    t.EndOfTurnWindows(),
		threshold:  t.Threshold,
		hysteresis: t.Hysteresis,
	}
}

func (e *EndOfTurn) Update(prob float32) bool {
	switch {
	case prob < e.threshold-e.hysteresis:
		e.run++
	case prob >= e.threshold:
		e.run = 0
	}
	return e.run == e.windows
}

func (e *EndOfTurn) Reset() { e.run = 0 }

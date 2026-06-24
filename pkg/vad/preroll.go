package vad

// Preroll is a fixed-size ring of the most recent PCM samples. The FSM confirms
// speech only after ~BargeInMs of evidence, so by the time it flips to speaking
// the utterance's first phoneme is already past; seeding a new utterance with
// this lead-in avoids clipping word onsets.
type Preroll struct {
	buf []int16
	max int
}

func NewPreroll(maxSamples int) *Preroll {
	return &Preroll{max: maxSamples}
}

// Push appends one window and trims to the most recent max samples, copying down
// in place so the backing array stays bounded.
func (p *Preroll) Push(window []int16) {
	p.buf = append(p.buf, window...)
	if len(p.buf) > p.max {
		p.buf = append(p.buf[:0], p.buf[len(p.buf)-p.max:]...)
	}
}

// Snapshot returns an independent copy of the retained samples, safe to hand to
// a transcription goroutine while the capture loop keeps mutating the ring.
func (p *Preroll) Snapshot() []int16 {
	out := make([]int16, len(p.buf))
	copy(out, p.buf)
	return out
}

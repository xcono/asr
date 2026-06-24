package asr

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/xcono/vox/pkg/vad"
)

// fakeSource yields a fixed number of dummy windows, then io.EOF.
type fakeSource struct {
	frames, i int
	buf       []int16
}

func (s *fakeSource) Read() ([]int16, error) {
	if s.i >= s.frames {
		return nil, io.EOF
	}
	s.i++
	return s.buf, nil
}

// fakeDetector returns scripted probabilities, one per frame, so VAD transitions
// are deterministic without the ONNX model.
type fakeDetector struct {
	probs []float32
	i     int
}

func (d *fakeDetector) Infer([]int16) (float32, error) {
	v := d.probs[d.i]
	d.i++
	return v, nil
}

type fakeRecognizer struct{ text string }

func (r fakeRecognizer) Transcribe(context.Context, []int16, int) (string, error) {
	return r.text, nil
}

func collectEvents(ch <-chan Event, timeout time.Duration) []Event {
	var out []Event
	deadline := time.After(timeout)
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, e)
		case <-deadline:
			return out
		}
	}
}

// speechThenSilence scripts probabilities that make the FSM fire one onset and
// one release: onsetWindows highs (→ ToSpeaking), then releaseWindow lows
// (→ ToSilence).
func speechThenSilence() []float32 {
	var p []float32
	for range vad.DefaultTiming().OnsetWindows() {
		p = append(p, 0.9)
	}
	silence := vad.DefaultTiming().ReleaseWindows() + 6 // comfortably past the release window
	for range silence {
		p = append(p, 0.0)
	}
	return p
}

func TestListenEmitsStartEndText(t *testing.T) {
	probs := speechThenSilence()
	src := &fakeSource{frames: len(probs), buf: make([]int16, vad.Window)}
	det := &fakeDetector{probs: probs}
	rec := fakeRecognizer{text: "привет мир"}

	got := collectEvents(Listen(context.Background(), src, det, rec, vad.DefaultTiming()), 2*time.Second)

	if len(got) != 3 {
		t.Fatalf("got %d events %+v, want 3 (start,end,text)", len(got), got)
	}
	if got[0].Kind != SpeechStart {
		t.Errorf("event 0 = %v, want SpeechStart", got[0].Kind)
	}
	if got[1].Kind != SpeechEnd {
		t.Errorf("event 1 = %v, want SpeechEnd", got[1].Kind)
	}
	if got[2].Kind != SpeechText || got[2].Text != "привет мир" {
		t.Errorf("event 2 = %+v, want SpeechText 'привет мир'", got[2])
	}
	// Verify timestamps are set
	for i, e := range got {
		if e.Timestamp.IsZero() {
			t.Errorf("event %d has zero Timestamp", i)
		}
	}
}

func TestListenNoSpeechEmitsNothing(t *testing.T) {
	probs := make([]float32, 20) // all silence
	src := &fakeSource{frames: len(probs), buf: make([]int16, vad.Window)}
	det := &fakeDetector{probs: probs}

	got := collectEvents(Listen(context.Background(), src, det, fakeRecognizer{text: "x"}, vad.DefaultTiming()), time.Second)

	if len(got) != 0 {
		t.Fatalf("want no events on pure silence, got %+v", got)
	}
}

func TestListenSkipsEmptyTranscription(t *testing.T) {
	probs := speechThenSilence()
	src := &fakeSource{frames: len(probs), buf: make([]int16, vad.Window)}
	det := &fakeDetector{probs: probs}
	rec := fakeRecognizer{text: ""} // recogniser returns nothing

	got := collectEvents(Listen(context.Background(), src, det, rec, vad.DefaultTiming()), 2*time.Second)

	if len(got) != 2 {
		t.Fatalf("got %d events %+v, want 2 (start,end; no empty text)", len(got), got)
	}
	if got[0].Kind != SpeechStart || got[1].Kind != SpeechEnd {
		t.Errorf("got kinds %v,%v; want SpeechStart,SpeechEnd", got[0].Kind, got[1].Kind)
	}
}

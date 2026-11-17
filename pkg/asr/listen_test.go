package asr

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/xcono/asr/pkg/vad"
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
// are deterministic without the real model.
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

// errSource yields a non-EOF error on its first Read, simulating a real
// device / transport failure (as opposed to a finite source's io.EOF).
type errSource struct{ err error }

func (s *errSource) Read() ([]int16, error) { return nil, s.err }

// TestListenSurfacesSourceError guards the SpeechError contract: a non-EOF
// source error emits exactly one SpeechError event carrying the cause before
// the channel closes. io.EOF must NOT emit SpeechError (it's a clean shutdown
// for finite sources), which the existing TestListen* tests already assert
// via their exact event counts.
func TestListenSurfacesSourceError(t *testing.T) {
	src := &errSource{err: errors.New("device disconnected")}
	got := collectEvents(Listen(context.Background(), src,
		&fakeDetector{probs: []float32{0.0}}, fakeRecognizer{}, vad.DefaultTiming()), time.Second)

	if len(got) != 1 {
		t.Fatalf("got %d events %+v, want 1 SpeechError", len(got), got)
	}
	if got[0].Kind != SpeechError {
		t.Fatalf("event 0 = %v, want SpeechError", got[0].Kind)
	}
	if got[0].Err == nil {
		t.Fatal("SpeechError event must carry the cause in Err")
	}
	if got[0].Err.Error() != "device disconnected" {
		t.Fatalf("Err = %q, want %q", got[0].Err.Error(), "device disconnected")
	}
}

// scoredRecognizer implements ScoredRecognizer, as *Transcriber does.
type scoredRecognizer struct{ t Transcript }

func (r scoredRecognizer) Transcribe(context.Context, []int16, int) (string, error) {
	return r.t.Text, nil
}

func (r scoredRecognizer) TranscribeScored(context.Context, []int16, int) (Transcript, error) {
	return r.t, nil
}

// listenOnce runs one scripted turn through Listen and returns its events.
func listenOnce(rec Recognizer, opts ...ListenOption) []Event {
	probs := speechThenSilence()
	src := &fakeSource{frames: len(probs), buf: make([]int16, vad.Window)}
	det := &fakeDetector{probs: probs}
	return collectEvents(Listen(context.Background(), src, det, rec, vad.DefaultTiming(), opts...), 2*time.Second)
}

// credits is the live hallucination: far faster than speech over the ~1 s
// scripted clip.
const credits = "Редактор субтитров А.Семкин Корректор А.Егорова"

// TestListenDropsFastText: a transcription over the rate limit is not emitted,
// so the turn ends at SpeechEnd.
func TestListenDropsFastText(t *testing.T) {
	got := listenOnce(fakeRecognizer{text: credits + " " + credits}, WithMaxCharsPerSec(30))
	if len(got) != 2 || got[0].Kind != SpeechStart || got[1].Kind != SpeechEnd {
		t.Fatalf("got %+v, want only SpeechStart,SpeechEnd", got)
	}
}

// TestListenKeepsNormalText: speech-rate text passes the filter and carries
// the clip length and rate it was judged by.
func TestListenKeepsNormalText(t *testing.T) {
	got := listenOnce(fakeRecognizer{text: " Да. "}, WithMaxCharsPerSec(30))
	if len(got) != 3 || got[2].Kind != SpeechText {
		t.Fatalf("got %+v, want start,end,text", got)
	}
	ev := got[2]
	if ev.Audio <= 0 {
		t.Fatalf("Audio = %v, want > 0", ev.Audio)
	}
	if want := 3 / ev.Audio.Seconds(); ev.CharsPerSec != want {
		t.Errorf("CharsPerSec = %v, want %v (3 runes of trimmed text)", ev.CharsPerSec, want)
	}
	if ev.Segments != nil {
		t.Errorf("Segments = %v, want nil for a plain Recognizer", ev.Segments)
	}
}

// TestListenFilterOff: limit 0 and no option both emit text at any rate.
func TestListenFilterOff(t *testing.T) {
	text := credits + " " + credits
	for name, opts := range map[string][]ListenOption{
		"zero":      {WithMaxCharsPerSec(0)},
		"no option": nil,
	} {
		got := listenOnce(fakeRecognizer{text: text}, opts...)
		if len(got) != 3 || got[2].Kind != SpeechText || got[2].Text != text {
			t.Errorf("%s: got %+v, want the text emitted", name, got)
			continue
		}
		if got[2].CharsPerSec <= 30 {
			t.Errorf("%s: CharsPerSec = %v, want the fast rate it was let through at", name, got[2].CharsPerSec)
		}
	}
}

// TestListenPassesScores: a ScoredRecognizer's segments reach the event.
func TestListenPassesScores(t *testing.T) {
	segs := []Segment{{NoSpeechProb: 0.4, AvgLogprob: -0.9}}
	got := listenOnce(scoredRecognizer{Transcript{Text: "Да.", Segments: segs}}, WithMaxCharsPerSec(30))
	if len(got) != 3 || got[2].Kind != SpeechText {
		t.Fatalf("got %+v, want start,end,text", got)
	}
	if s := got[2].Segments; len(s) != 1 || s[0] != segs[0] {
		t.Fatalf("Segments = %v, want %v", s, segs)
	}
}

func TestFormatSegments(t *testing.T) {
	if got := FormatSegments(nil); got != "" {
		t.Errorf("FormatSegments(nil) = %q, want empty", got)
	}
	got := FormatSegments([]Segment{{0.0123, -0.314}, {0.0123, -0.449}})
	if want := " no_speech=0.012,0.012 logprob=-0.31,-0.45"; got != want {
		t.Errorf("FormatSegments = %q, want %q", got, want)
	}
}

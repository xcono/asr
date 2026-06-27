package stt

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/xcono/asr/pkg/vad"
)

// Local fakes (the asr package's are unexported). They satisfy asr.Source /
// asr.Detector / asr.Recognizer so the facade runs with no ONNX or PortAudio.

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

// onsetProbs / onsetThenSilence drive the FSM off the Timing helpers so they
// stay correct if the consts change.
func onsetProbs() []float32 {
	var p []float32
	for range vad.DefaultTiming().OnsetWindows() {
		p = append(p, 0.9)
	}
	return p
}

func onsetThenSilence() []float32 {
	p := onsetProbs()
	for range vad.DefaultTiming().ReleaseWindows() + 6 {
		p = append(p, 0.0)
	}
	return p
}

// drain collects events until the channel closes (or the test times out). The
// channel close happens-after every speaking-flag store in the forwarder, so a
// flag read after drain is race-free — unlike a read mid-stream, where the
// boundary may have already flipped.
func drain(t *testing.T, ch <-chan Event) []Event {
	t.Helper()
	var got []Event
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return got
			}
			got = append(got, ev)
		case <-deadline:
			t.Fatal("timed out waiting for events")
		}
	}
}

func TestServiceFullTurnStreamsAndClearsSpeaking(t *testing.T) {
	probs := onsetThenSilence()
	src := &fakeSource{frames: len(probs), buf: make([]int16, vad.Window)}
	svc := NewWith(src, &fakeDetector{probs: probs}, fakeRecognizer{text: "привет мир"}, vad.DefaultTiming())
	defer svc.Close()

	got := drain(t, svc.Events())

	if len(got) != 3 {
		t.Fatalf("got %d events %+v, want 3 (start,end,text)", len(got), got)
	}
	if got[0].Kind != SpeechStart || got[1].Kind != SpeechEnd || got[2].Kind != SpeechText {
		t.Fatalf("kinds = %v/%v/%v, want SpeechStart/SpeechEnd/SpeechText", got[0].Kind, got[1].Kind, got[2].Kind)
	}
	if got[2].Text != "привет мир" {
		t.Errorf("text = %q, want %q", got[2].Text, "привет мир")
	}
	if svc.IsSpeaking() {
		t.Error("IsSpeaking() = true after a completed turn, want false")
	}
}

func TestServiceIsSpeakingTrueWhileTurnOpen(t *testing.T) {
	// Onset with no release: the source EOFs mid-turn, so the flag stays set.
	probs := onsetProbs()
	src := &fakeSource{frames: len(probs), buf: make([]int16, vad.Window)}
	svc := NewWith(src, &fakeDetector{probs: probs}, fakeRecognizer{}, vad.DefaultTiming())
	defer svc.Close()

	got := drain(t, svc.Events())

	if len(got) != 1 || got[0].Kind != SpeechStart {
		t.Fatalf("got %+v, want a single SpeechStart", got)
	}
	if !svc.IsSpeaking() {
		t.Error("IsSpeaking() = false with a turn still open, want true")
	}
}

func TestServiceTranscribeBatch(t *testing.T) {
	// NewWith with an idle source: nothing to capture, just exercise Batch.
	svc := NewWith(&fakeSource{}, &fakeDetector{}, fakeRecognizer{text: "ok"}, vad.DefaultTiming())
	defer svc.Close()

	text, err := svc.Transcribe(context.Background(), make([]int16, vad.Window))
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if text != "ok" {
		t.Errorf("text = %q, want %q", text, "ok")
	}
}

func TestServiceCloseIsIdempotent(t *testing.T) {
	svc := NewWith(&fakeSource{}, &fakeDetector{}, fakeRecognizer{}, vad.DefaultTiming())
	if err := svc.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := svc.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

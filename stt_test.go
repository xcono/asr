package stt

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xcono/asr/pkg/asr"
	"github.com/xcono/asr/pkg/config"
	"github.com/xcono/asr/pkg/vad"
)

// Local fakes (the asr package's are unexported). They satisfy asr.Source /
// asr.Detector / asr.Recognizer so the facade runs with no model or mic.

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

// TestRecognizerWhisper checks the facade maps the whisper config section onto
// the batch client, including the whisper.cpp-only fields, and leaves
// Verbose off (plain json; see NewRecognizer).
func TestRecognizerWhisper(t *testing.T) {
	rec, err := NewRecognizer(config.ASRConfig{
		Provider: "whisper",
		Whisper:  config.WhisperConfig{BaseURL: "http://w:8008/v1", Model: "m", Language: "ru", Prompt: "p"},
	})
	if err != nil {
		t.Fatalf("NewRecognizer: %v", err)
	}
	tr, ok := rec.(*asr.Transcriber)
	if !ok {
		t.Fatalf("recognizer type = %T, want *asr.Transcriber", rec)
	}
	want := asr.Transcriber{BaseURL: "http://w:8008/v1", Model: "m", Language: "ru", Prompt: "p"}
	if *tr != want {
		t.Fatalf("transcriber = %+v, want %+v", *tr, want)
	}
}

// TestRecognizerGigaAMPlainJSON: GigaAM's server answers 400 to any format but
// json|text, so its client must never ask for verbose_json.
func TestRecognizerGigaAMPlainJSON(t *testing.T) {
	rec, err := NewRecognizer(config.ASRConfig{Provider: "gigaam", GigaAM: config.GigaAMConfig{BaseURL: "http://g"}})
	if err != nil {
		t.Fatalf("NewRecognizer: %v", err)
	}
	if tr, ok := rec.(*asr.Transcriber); !ok || tr.Verbose {
		t.Fatalf("recognizer = %+v, want a *asr.Transcriber without Verbose", rec)
	}
}

// TestRecognizerElevenLabs checks the elevenlabs section reaches the Scribe
// client; the two providers must not shadow each other in the one switch.
func TestRecognizerElevenLabs(t *testing.T) {
	rec, err := NewRecognizer(config.ASRConfig{
		Provider:   "elevenlabs",
		ElevenLabs: config.ElevenLabsConfig{APIKey: "k", Model: "m", LanguageCode: "ru", BaseURL: "http://e"},
	})
	if err != nil {
		t.Fatalf("NewRecognizer: %v", err)
	}
	el, ok := rec.(*asr.ElevenLabs)
	if !ok {
		t.Fatalf("recognizer type = %T, want *asr.ElevenLabs", rec)
	}
	want := asr.ElevenLabs{APIKey: "k", Model: "m", LanguageCode: "ru", BaseURL: "http://e"}
	if *el != want {
		t.Fatalf("elevenlabs = %+v, want %+v", *el, want)
	}
}

// TestRecognizerUnknownProvider keeps a typo in config a hard error rather than
// a silently missing transcriber.
func TestRecognizerUnknownProvider(t *testing.T) {
	if _, err := NewRecognizer(config.ASRConfig{Provider: "nope"}); err == nil {
		t.Fatal("expected an error for an unknown provider, got nil")
	}
}

// TestRecordOff: without record_dir the source is used as is and nothing is
// written (checked in an empty working directory).
func TestRecordOff(t *testing.T) {
	wd := t.TempDir()
	t.Chdir(wd)
	src := &fakeSource{}
	got, r, err := record(src, "")
	if err != nil || r != nil || got != asr.Source(src) {
		t.Fatalf("record(src, \"\") = %v, %v, %v; want src, nil, nil", got, r, err)
	}
	if files, _ := os.ReadDir(wd); len(files) != 0 {
		t.Fatalf("working dir has %v, want empty", files)
	}
}

// TestRecordOn: with record_dir the source is a Recorder writing there.
func TestRecordOn(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "recordings")
	got, r, err := record(&fakeSource{}, dir)
	if err != nil || r == nil || got != asr.Source(r) {
		t.Fatalf("record = %v, %v, %v; want the Recorder", got, r, err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(r.Path()) != dir {
		t.Fatalf("Path = %s, want under %s", r.Path(), dir)
	}
}

// TestNewWithPhrases: NewWith runs the phrase filter only when given it —
// without options a listed phrase is emitted, as for the letter budget.
func TestNewWithPhrases(t *testing.T) {
	for name, c := range map[string]struct {
		opts []ListenOption
		want int
	}{
		"no option":    {nil, 3},
		"default list": {[]ListenOption{WithHallucinationPhrases(asr.DefaultHallucinationPhrases())}, 2},
	} {
		probs := onsetThenSilence()
		src := &fakeSource{frames: len(probs), buf: make([]int16, vad.Window)}
		svc := NewWith(src, &fakeDetector{probs: probs}, fakeRecognizer{text: "Продолжение следует..."}, vad.DefaultTiming(), c.opts...)
		if got := drain(t, svc.Events()); len(got) != c.want {
			t.Errorf("%s: got %d events %+v, want %d", name, len(got), got, c.want)
		}
		svc.Close()
	}
}

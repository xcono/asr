package asr

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xcono/asr/pkg/audio"
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

// clipRecognizer records the length of the clip it was given.
type clipRecognizer struct {
	text string
	got  chan int
}

func (r clipRecognizer) Transcribe(_ context.Context, s []int16, _ int) (string, error) {
	r.got <- len(s)
	return r.text, nil
}

// TestListenVADStats: the stats cover exactly the clip's windows — preroll
// included, earlier windows not — and count only windows at or above
// Threshold. The probabilities are built from Timing so they survive const
// changes.
func TestListenVADStats(t *testing.T) {
	tm := vad.DefaultTiming()
	pre := tm.PrerollSamples() / vad.Window
	if tm.OnsetWindows() != 2 || pre < 4 {
		t.Fatalf("script assumes a 2-window onset and a preroll of >= 4 windows, got %d, %d", tm.OnsetWindows(), pre)
	}
	dead := tm.Threshold - tm.Hysteresis/2 // between the gates: neither speech nor silence

	probs := []float32{0.99} // speech, but pushed out of the preroll below
	for range pre + 2 {
		probs = append(probs, 0)
	}
	probs = append(probs, tm.Threshold, 0.95) // onset confirmed on 0.95
	probs = append(probs, dead, 0.6)
	for range tm.ReleaseWindows() + 3 {
		probs = append(probs, 0)
	}

	src := &fakeSource{frames: len(probs), buf: make([]int16, vad.Window)}
	rec := clipRecognizer{text: "да", got: make(chan int, 1)}
	got := collectEvents(Listen(context.Background(), src, &fakeDetector{probs: probs}, rec, tm), 2*time.Second)

	if len(got) != 3 || got[1].Kind != SpeechEnd || got[2].Kind != SpeechText {
		t.Fatalf("got %+v, want start,end,text", got)
	}
	clip := <-rec.got
	want := VADStats{
		Peak:    0.95,
		Speech:  3, // Threshold, 0.95, 0.6
		Windows: pre + 2 + tm.ReleaseWindows(),
	}
	if want.Windows*vad.Window != clip {
		t.Fatalf("clip = %d samples, want %d windows (test arithmetic)", clip, want.Windows)
	}
	for _, i := range []int{1, 2} {
		if got[i].VAD != want {
			t.Errorf("%v VAD = %+v, want %+v", got[i].Kind, got[i].VAD, want)
		}
	}
	if got[0].VAD != (VADStats{}) {
		t.Errorf("SpeechStart VAD = %+v, want zero", got[0].VAD)
	}
	if s, w := want.String(), " vad_peak=0.95 speech_win=3/"; len(s) <= len(w) || s[:len(w)] != w {
		t.Errorf("String() = %q, want prefix %q", s, w)
	}
	if s := (VADStats{}).String(); s != "" {
		t.Errorf("zero String() = %q, want empty", s)
	}
}

// listenDump runs one scripted turn with a fast (dropped) or slow (kept) text
// and the dump option, and returns its events and the clip length.
func listenDump(text string, opts ...ListenOption) ([]Event, int) {
	probs := speechThenSilence()
	buf := make([]int16, vad.Window)
	for i := range buf {
		buf[i] = int16(i - vad.Window/2)
	}
	src := &fakeSource{frames: len(probs), buf: buf}
	rec := clipRecognizer{text: text, got: make(chan int, 1)}
	opts = append([]ListenOption{WithMaxCharsPerSec(30)}, opts...)
	got := collectEvents(Listen(context.Background(), src, &fakeDetector{probs: probs}, rec, vad.DefaultTiming(), opts...), 2*time.Second)
	return got, <-rec.got
}

// TestListenDumpsDropped: a dropped turn leaves one valid 16 kHz WAV of the
// clip sent to the recogniser, in a directory created on demand.
func TestListenDumpsDropped(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dropped")
	got, clip := listenDump(credits+" "+credits, WithDumpDropped(dir))
	if len(got) != 2 || got[1].Kind != SpeechEnd {
		t.Fatalf("got %+v, want start,end", got)
	}

	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 1 {
		t.Fatalf("ReadDir = %v, %v; want one file", files, err)
	}
	name := files[0].Name()
	if !strings.HasPrefix(name, "dropped-") || !strings.HasSuffix(name, ".wav") {
		t.Errorf("file name %q, want dropped-<time>.wav", name)
	}
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	samples, rate, err := audio.DecodeWAV(b)
	if err != nil {
		t.Fatalf("DecodeWAV: %v", err)
	}
	if rate != vad.SampleRate || len(samples) != clip {
		t.Fatalf("WAV %d Hz, %d samples; want %d Hz, %d", rate, len(samples), vad.SampleRate, clip)
	}
	for i, s := range samples {
		if want := int16(i%vad.Window - vad.Window/2); s != want {
			t.Fatalf("sample %d = %d, want %d", i, s, want)
		}
	}
}

// TestListenDumpOnlyDropped: a kept turn writes nothing even with the option.
func TestListenDumpOnlyDropped(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dropped")
	got, _ := listenDump("Да.", WithDumpDropped(dir))
	if len(got) != 3 {
		t.Fatalf("got %+v, want start,end,text", got)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("Stat(%s) = %v, want not created", dir, err)
	}
}

// TestListenNoDumpByDefault: without the option a dropped turn writes nothing
// (checked in an empty working directory).
func TestListenNoDumpByDefault(t *testing.T) {
	wd := t.TempDir()
	t.Chdir(wd)
	if got, _ := listenDump(credits + " " + credits); len(got) != 2 {
		t.Fatalf("got %+v, want start,end", got)
	}
	if files, _ := os.ReadDir(wd); len(files) != 0 {
		t.Fatalf("working dir has %v, want empty", files)
	}
}

// TestListenDumpErrorKeepsStream: an unwritable dump dir (a regular file in
// its place) is logged; the events of this and the next turn still arrive.
func TestListenDumpErrorKeepsStream(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(dir, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	probs := append(speechThenSilence(), speechThenSilence()...)
	src := &fakeSource{frames: len(probs), buf: make([]int16, vad.Window)}
	rec := clipRecognizer{text: credits + " " + credits, got: make(chan int, 2)}
	got := collectEvents(Listen(context.Background(), src, &fakeDetector{probs: probs}, rec,
		vad.DefaultTiming(), WithMaxCharsPerSec(30), WithDumpDropped(dir)), 2*time.Second)

	want := []EventKind{SpeechStart, SpeechEnd, SpeechStart, SpeechEnd}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %v", got, want)
	}
	for i, k := range want {
		if got[i].Kind != k {
			t.Errorf("event %d = %v, want %v", i, got[i].Kind, k)
		}
	}
	if len(rec.got) != 2 {
		t.Errorf("recogniser called %d times, want 2", len(rec.got))
	}
}

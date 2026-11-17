package asr

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xcono/asr/pkg/audio"
	"github.com/xcono/asr/pkg/vad"
)

// reusingSource behaves like audio.Capture: every Read returns the same
// slice, overwritten with the next window (each sample = window index), so a
// recorder that kept the slice instead of copying it would record the last
// window over and over.
type reusingSource struct {
	frames, i int
	buf       []int16
}

func (s *reusingSource) Read() ([]int16, error) {
	if s.i >= s.frames {
		return nil, io.EOF
	}
	for j := range s.buf {
		s.buf[j] = int16(s.i)
	}
	s.i++
	return s.buf, nil
}

// readWAV decodes a recording and checks its format.
func readWAV(t *testing.T, path string) []int16 {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	samples, rate, err := audio.DecodeWAV(b)
	if err != nil || rate != vad.SampleRate {
		t.Fatalf("DecodeWAV = %d Hz, %v; want %d Hz", rate, err, vad.SampleRate)
	}
	if want := 44 + 2*len(samples); len(b) != want {
		t.Fatalf("file is %d bytes, want %d: header lengths not patched", len(b), want)
	}
	return samples
}

// TestRecorderWritesSession: every window read lands in one session-*.wav,
// copied before the source reuses its buffer, in a directory made on demand;
// Read passes the window through unchanged.
func TestRecorderWritesSession(t *testing.T) {
	const n = 40
	dir := filepath.Join(t.TempDir(), "recordings")
	src := &reusingSource{frames: n, buf: make([]int16, vad.Window)}
	r, err := NewRecorder(src, dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := range n {
		f, err := r.Read()
		if err != nil || len(f) != vad.Window || f[0] != int16(i) {
			t.Fatalf("Read %d = len %d [0]=%v, %v; want window %d", i, len(f), f, err, i)
		}
	}
	if _, err := r.Read(); err != io.EOF {
		t.Fatalf("Read past end = %v, want io.EOF", err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("second Close = %v, want nil", err)
	}

	files, _ := os.ReadDir(dir)
	if len(files) != 1 {
		t.Fatalf("dir has %v, want one file", files)
	}
	name := files[0].Name()
	if !strings.HasPrefix(name, "session-") || !strings.HasSuffix(name, ".wav") || filepath.Join(dir, name) != r.Path() {
		t.Fatalf("file %q, Path %q; want session-<time>.wav at Path", name, r.Path())
	}
	samples := readWAV(t, r.Path())
	if len(samples) != n*vad.Window {
		t.Fatalf("%d samples, want %d", len(samples), n*vad.Window)
	}
	for i, s := range samples {
		if want := int16(i / vad.Window); s != want {
			t.Fatalf("sample %d = %d, want %d (window %d)", i, s, want, want)
		}
	}
	if r.Lost() != 0 {
		t.Errorf("Lost = %v, want 0", r.Lost())
	}
}

// TestRecorderPadsLostWindows: a window the queue dropped becomes silence of
// its length, so later audio stays at its stream position. Driven through
// write directly — a full queue is a race to provoke via Read.
func TestRecorderPadsLostWindows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.wav")
	w, err := audio.CreateWAV(path, vad.SampleRate)
	if err != nil {
		t.Fatal(err)
	}
	one := func(v int16) []int16 {
		f := make([]int16, vad.Window)
		for i := range f {
			f[i] = v
		}
		return f
	}
	r := &Recorder{path: path, q: make(chan recFrame, 3), done: make(chan struct{})}
	r.q <- recFrame{pos: 0, samples: one(1)}
	r.q <- recFrame{pos: 3 * vad.Window, samples: one(4)} // windows 1 and 2 dropped
	r.q <- recFrame{pos: 4 * vad.Window, samples: one(5)}
	close(r.q)
	r.closed = true
	r.write(w)

	samples := readWAV(t, path)
	want := []int16{1, 0, 0, 4, 5}
	if len(samples) != len(want)*vad.Window {
		t.Fatalf("%d samples, want %d", len(samples), len(want)*vad.Window)
	}
	for i, v := range want {
		if s := samples[i*vad.Window]; s != v {
			t.Errorf("window %d = %d, want %d", i, s, v)
		}
	}
	if lost, want := r.Lost(), 2*32*time.Millisecond; lost != want {
		t.Errorf("Lost = %v, want %v", lost, want)
	}
	if err := r.Close(); err != nil {
		t.Errorf("Close = %v", err)
	}
}

// TestRecorderReadAfterClose: a Read that races Close (the mic is closed
// after the recorder in some teardowns) returns its window and records
// nothing, instead of sending on a closed queue.
func TestRecorderReadAfterClose(t *testing.T) {
	src := &reusingSource{frames: 3, buf: make([]int16, vad.Window)}
	r, err := NewRecorder(src, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Read(); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if f, err := r.Read(); err != nil || len(f) != vad.Window {
		t.Fatalf("Read after Close = %d samples, %v", len(f), err)
	}
	if n := len(readWAV(t, r.Path())); n != vad.Window {
		t.Fatalf("%d samples, want the one window read before Close", n)
	}
}

// TestRecorderOffsetsMatchFile: Listen over a Recorder — the events' Offset
// is a position in the recording, which holds every window Listen read.
func TestRecorderOffsetsMatchFile(t *testing.T) {
	tm := vad.DefaultTiming()
	lead := 7
	probs := append(make([]float32, lead), speechThenSilence()...)
	r, err := NewRecorder(&reusingSource{frames: len(probs), buf: make([]int16, vad.Window)}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	got := collectEvents(Listen(context.Background(), r, &fakeDetector{probs: probs}, fakeRecognizer{text: "да"}, tm), 2*time.Second)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %+v, want start,end,text", got)
	}
	samples := readWAV(t, r.Path())
	if len(samples) != len(probs)*vad.Window {
		t.Fatalf("%d samples, want all %d windows", len(samples), len(probs))
	}
	// The window ending at SpeechStart's Offset is the one that confirmed
	// the onset: its samples carry its index.
	at := int(got[0].Offset * vad.SampleRate / time.Second)
	if want := lead + tm.OnsetWindows(); at != want*vad.Window || samples[at-1] != int16(want-1) {
		t.Fatalf("SpeechStart at sample %d (window value %d), want end of window %d", at, samples[at-1], want-1)
	}
}

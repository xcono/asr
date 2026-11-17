package asr

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/xcono/asr/pkg/audio"
	"github.com/xcono/asr/pkg/vad"
)

// recordQueue is how many windows the writer may fall behind before Read
// starts dropping them: 256 × 32 ms ≈ 8 s, far more than a disk stall should
// take, and only ~256 KiB of copies.
const recordQueue = 256

// Recorder is a Source that passes another Source through unchanged and
// writes every window it reads into one WAV — a whole session of mic input,
// so missed words can be found by ear and the session replayed offline
// through the VAD. Wrap the mic in it and hand the Recorder to Listen.
//
// Its time base is Event.Offset's: both count samples read from the same
// Source, so "at=12.48s" in a log line is 12.48 s into the file. To keep that
// true when the writer falls behind, a window it never got is written as
// silence of the same length instead of being skipped.
//
// Writing never delays capture: Read copies the window (the mic's slice is
// overwritten by its next Read) and queues it without blocking; one goroutine
// owns the file. A full queue drops the window and counts it (Lost); the
// writer logs the gap when it pads it.
type Recorder struct {
	src  Source
	path string

	mu     sync.Mutex // guards closed and sends on q against Close
	closed bool
	q      chan recFrame
	pos    int64 // samples read so far; owned by the reading goroutine

	done chan struct{}
	lost int64 // samples padded with silence; written by the writer, read after done
	err  error // first write/close error; read after done
}

// recFrame is one window and its position in the stream, so the writer can
// tell how much was dropped before it.
type recFrame struct {
	pos     int64
	samples []int16
}

// NewRecorder creates dir if needed and starts a session-<time>.wav there
// (16 kHz mono 16-bit), named like the dropped-clip dumps so the two sort
// together. Read must then be called from one goroutine, as Listen does.
func NewRecorder(src Source, dir string) (*Recorder, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("asr: record: %w", err)
	}
	path := filepath.Join(dir, "session-"+time.Now().Format("20060102-150405.000")+".wav")
	w, err := audio.CreateWAV(path, vad.SampleRate)
	if err != nil {
		return nil, fmt.Errorf("asr: record: %w", err)
	}
	r := &Recorder{
		src:  src,
		path: path,
		q:    make(chan recFrame, recordQueue),
		done: make(chan struct{}),
	}
	go r.write(w)
	return r, nil
}

// Path is the WAV file being written.
func (r *Recorder) Path() string { return r.path }

// Read returns the next window of the wrapped Source unchanged and queues a
// copy for the file. Errors pass through and are not recorded.
func (r *Recorder) Read() ([]int16, error) {
	frame, err := r.src.Read()
	if err != nil {
		return frame, err
	}
	f := recFrame{pos: r.pos, samples: append([]int16(nil), frame...)}
	r.pos += int64(len(frame))

	r.mu.Lock()
	if !r.closed {
		select {
		case r.q <- f:
		default: // writer behind: it pads the gap when the next window arrives
		}
	}
	r.mu.Unlock()
	return frame, nil
}

// write owns the file: it pads every gap left by a dropped window with
// silence, so positions in the file stay those of the stream.
func (r *Recorder) write(w *audio.WAVWriter) {
	defer close(r.done)
	for f := range r.q {
		if r.err != nil {
			continue // drain so Read never blocks; the error is already logged
		}
		if gap := f.pos - w.Samples(); gap > 0 {
			r.lost += gap
			log.Printf("asr: record: writer fell behind, %.2fs of silence written at=%.2fs",
				seconds(gap), seconds(w.Samples()))
			r.err = w.Write(make([]int16, gap))
		}
		if r.err == nil {
			r.err = w.Write(f.samples)
		}
		if r.err != nil {
			log.Printf("asr: record %s: %v (recording stopped, capture goes on)", r.path, r.err)
		}
	}
	if err := w.Close(); r.err == nil {
		r.err = err
	}
}

// Lost is the audio padded with silence because the writer fell behind. Read
// it after Close.
func (r *Recorder) Lost() time.Duration {
	<-r.done
	return time.Duration(r.lost) * time.Second / vad.SampleRate
}

// Close stops recording, waits for the queue to drain and finalises the WAV
// header, so the file is valid after a Ctrl-C that reaches Close. It does not
// close the wrapped Source — its owner does. A Read racing Close is safe: it
// returns its window and records nothing. Idempotent.
func (r *Recorder) Close() error {
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		close(r.q)
	}
	r.mu.Unlock()
	<-r.done
	return r.err
}

// seconds converts a sample count at vad.SampleRate.
func seconds(samples int64) float64 { return float64(samples) / vad.SampleRate }

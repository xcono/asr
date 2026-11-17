package audio

import (
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jfreymuth/pulse"
)

// readTimeout is how long Read waits for audio before calling the mic gone.
// The server streams continuously — silence arrives as samples, not as a pause —
// so seconds without a packet mean the device or the server went away. The
// library does not report a removed device or a killed stream (it drops those
// messages), so this timeout is the only signal for the former.
const readTimeout = 2 * time.Second

// queueLen bounds the packets parked between the server and Read: at the
// one-window fragment we request (32 ms), 256 packets are about 8 s of audio.
const queueLen = 256

// Capture is an open PulseAudio record stream delivering fixed-size windows of
// 16-bit mono PCM. The client is pure Go (no cgo, no libpulse) and works with
// PipeWire's pulse layer too. The server resamples and downmixes the source to
// the requested format.
type Capture struct {
	client *pulse.Client
	stream *pulse.RecordStream
	frames *framer
}

// OpenCapture opens the default input source for sampleRate Hz mono, reading
// frameSize samples per Read.
func OpenCapture(sampleRate, frameSize int) (*Capture, error) {
	client, err := pulse.NewClient(pulse.ClientApplicationName("asr"))
	if err != nil {
		return nil, fmt.Errorf("open input: connect to pulse: %w", err)
	}
	src, err := client.DefaultSource()
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("open input: default source: %w", err)
	}
	f := newFramer(frameSize, queueLen, readTimeout)
	stream, err := client.NewRecord(pulse.Int16Writer(f.push),
		pulse.RecordSource(src),
		pulse.RecordMono,
		pulse.RecordSampleRate(sampleRate),
		// One packet per window keeps capture latency at a window, not the
		// server's default fragment.
		pulse.RecordBufferFragmentSize(uint32(frameSize*2)),
		pulse.RecordMediaName("asr capture"),
	)
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("open input: record stream: %w", err)
	}
	f.streamErr = stream.Error
	stream.Start()
	return &Capture{client: client, stream: stream, frames: f}, nil
}

// Read blocks for the next window and returns it. The returned slice aliases
// the internal buffer and is overwritten on the next Read — copy it (append /
// snapshot) if you need to retain it past the next call.
//
// Read returns io.EOF after Close, and an error when no audio arrived for
// readTimeout (device or server lost).
func (c *Capture) Read() ([]int16, error) { return c.frames.next() }

// Overflows is the number of packets dropped because the consumer fell behind
// the mic (the queue to Read was full). Nonzero means some samples were lost,
// though the stream stays usable: like the PortAudio input overflow it
// replaces, it is counted rather than fatal, so a transient hiccup doesn't end
// listening.
func (c *Capture) Overflows() int { return int(c.frames.dropped.Load()) }

// Close stops the stream, closes the server connection and unblocks a pending
// Read.
func (c *Capture) Close() error {
	c.frames.close()
	c.stream.Stop()
	c.stream.Close()
	c.client.Close()
	return nil
}

// framer turns the server's packets into fixed windows. push runs on the pulse
// connection's reader goroutine, which also delivers the reply to every
// request: blocking it would stall the whole connection, Close included. So
// push copies (the library reuses the packet buffer) and hands off without
// waiting, dropping the packet when the queue is full.
type framer struct {
	ch        chan []int16
	done      chan struct{}
	closeOnce sync.Once
	pending   []int16
	frame     []int16
	timeout   time.Duration
	dropped   atomic.Int64
	streamErr func() error // the stream's last error; nil in tests
}

func newFramer(frameSize, queue int, timeout time.Duration) *framer {
	return &framer{
		ch:      make(chan []int16, queue),
		done:    make(chan struct{}),
		frame:   make([]int16, frameSize),
		timeout: timeout,
	}
}

// push is the pulse.Int16Writer callback. It never returns an error: the
// library stops the stream for good on one.
func (f *framer) push(p []int16) (int, error) {
	cp := make([]int16, len(p))
	copy(cp, p)
	select {
	case f.ch <- cp:
	default:
		f.dropped.Add(1)
	}
	return len(p), nil
}

// next assembles one window from the queued packets.
func (f *framer) next() ([]int16, error) {
	for len(f.pending) < len(f.frame) {
		select {
		case p := <-f.ch:
			f.pending = append(f.pending, p...)
		case <-f.done:
			return nil, io.EOF
		case <-time.After(f.timeout):
			if f.streamErr != nil {
				if err := f.streamErr(); err != nil {
					return nil, fmt.Errorf("read input: %w", err)
				}
			}
			return nil, fmt.Errorf("read input: no audio for %v (device or server lost)", f.timeout)
		}
	}
	n := copy(f.frame, f.pending)
	f.pending = append(f.pending[:0], f.pending[n:]...)
	return f.frame, nil
}

func (f *framer) close() { f.closeOnce.Do(func() { close(f.done) }) }

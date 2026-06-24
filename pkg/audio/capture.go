package audio

import (
	"errors"
	"fmt"

	"github.com/gordonklaus/portaudio"
)

// Capture is an open PortAudio input stream delivering fixed-size windows of
// 16-bit mono PCM. portaudio.Initialize must have been called first (and
// Terminate after Close) — Capture does not own the global PortAudio lifecycle.
type Capture struct {
	stream    *portaudio.Stream
	buf       []int16
	overflows int
}

// OpenCapture opens the default input device for sampleRate Hz mono, reading
// frameSize samples per Read.
func OpenCapture(sampleRate, frameSize int) (*Capture, error) {
	buf := make([]int16, frameSize)
	stream, err := portaudio.OpenDefaultStream(1, 0, float64(sampleRate), frameSize, buf)
	if err != nil {
		return nil, fmt.Errorf("open input: %w", err)
	}
	if err := stream.Start(); err != nil {
		stream.Close()
		return nil, fmt.Errorf("start input: %w", err)
	}
	return &Capture{stream: stream, buf: buf}, nil
}

// Read blocks for the next window and returns it. The returned slice aliases
// the internal buffer and is overwritten on the next Read — copy it (append /
// snapshot) if you need to retain it past the next call.
//
// An input overflow (we fell behind, so PortAudio dropped some earlier samples)
// is tolerated, not fatal: Pa_ReadStream still fills the buffer before reporting
// the overflow, so the window is valid — we return it and count the event.
// Propagating it would tear down the whole listen loop over a transient hiccup.
// Every other error stays fatal.
func (c *Capture) Read() ([]int16, error) {
	if err := c.stream.Read(); err != nil {
		if isOverflow(err) {
			c.overflows++
			return c.buf, nil
		}
		return nil, err
	}
	return c.buf, nil
}

// isOverflow reports whether err is a recoverable PortAudio input overflow.
func isOverflow(err error) bool { return errors.Is(err, portaudio.InputOverflowed) }

// Overflows is the number of tolerated input-overflow events so far. Nonzero
// means the consumer isn't keeping up with the mic (some samples were dropped),
// though the stream stays usable.
func (c *Capture) Overflows() int { return c.overflows }

// Close stops and closes the stream.
func (c *Capture) Close() error {
	c.stream.Stop()
	return c.stream.Close()
}

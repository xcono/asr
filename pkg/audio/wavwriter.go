package audio

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"math"
	"os"
)

// WAVWriter streams 16-bit LE mono PCM into a WAV file of unknown final
// length — a whole session's mic input, where EncodeWAV would need every
// sample in memory first. The header goes out first with placeholder lengths
// and Close patches the real ones.
//
// The placeholders are 0xFFFFFFFF, not 0: a file left behind by a crash (no
// Close) then still decodes to everything written before the crash —
// DecodeWAV clamps a data chunk that runs past the end of the file, and
// ffmpeg/sox read such a header as "until EOF" — whereas 0 would read as empty.
type WAVWriter struct {
	f       *os.File
	w       *bufio.Writer
	samples int64
	buf     []byte
}

// CreateWAV creates path (failing if it exists, so a session never overwrites
// another) and writes the header for rate Hz mono 16-bit.
func CreateWAV(path string, rate int) (*WAVWriter, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, err
	}
	hdr := EncodeWAV(nil, rate) // canonical 44 bytes: RIFF size at 4, data size at 40
	binary.LittleEndian.PutUint32(hdr[4:], math.MaxUint32)
	binary.LittleEndian.PutUint32(hdr[40:], math.MaxUint32)
	w := &WAVWriter{f: f, w: bufio.NewWriterSize(f, 64<<10)}
	if _, err := w.w.Write(hdr); err != nil {
		f.Close()
		return nil, fmt.Errorf("wav header %s: %w", path, err)
	}
	return w, nil
}

// Write appends samples. It buffers, so an error may surface on a later Write
// or on Close.
func (w *WAVWriter) Write(samples []int16) error {
	n := len(samples) * 2
	if cap(w.buf) < n {
		w.buf = make([]byte, n)
	}
	b := w.buf[:n]
	for i, s := range samples {
		binary.LittleEndian.PutUint16(b[i*2:], uint16(s))
	}
	if _, err := w.w.Write(b); err != nil {
		return err
	}
	w.samples += int64(len(samples))
	return nil
}

// Samples is the number of samples written so far.
func (w *WAVWriter) Samples() int64 { return w.samples }

// Close flushes, patches the RIFF and data lengths and closes the file. Past
// 4 GiB of data (~37 h at 16 kHz) the lengths no longer fit and are left as
// the 0xFFFFFFFF placeholders.
func (w *WAVWriter) Close() error {
	err := w.w.Flush()
	if data := w.samples * 2; err == nil && data <= math.MaxUint32-36 {
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], uint32(36+data))
		if _, err = w.f.WriteAt(b[:], 4); err == nil {
			binary.LittleEndian.PutUint32(b[:], uint32(data))
			_, err = w.f.WriteAt(b[:], 40)
		}
	}
	if cerr := w.f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("wav close %s: %w", w.f.Name(), err)
	}
	return nil
}

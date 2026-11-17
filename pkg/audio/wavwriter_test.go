package audio

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// TestWAVWriterClose: chunks written in pieces decode back whole, and Close
// leaves the same header EncodeWAV would write for those samples.
func TestWAVWriterClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.wav")
	w, err := CreateWAV(path, 16000)
	if err != nil {
		t.Fatal(err)
	}
	in := []int16{0, 1, -1, 32767, -32768, 1000, -1000}
	if err := w.Write(in[:3]); err != nil {
		t.Fatal(err)
	}
	if err := w.Write(in[3:]); err != nil {
		t.Fatal(err)
	}
	if w.Samples() != int64(len(in)) {
		t.Fatalf("Samples = %d, want %d", w.Samples(), len(in))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := EncodeWAV(in, 16000)
	if string(got) != string(want) {
		t.Fatalf("file differs from EncodeWAV:\n got % x\nwant % x", got, want)
	}
}

// TestWAVWriterUnclosed: before Close the lengths are the 0xFFFFFFFF
// placeholders, which still decode to everything flushed — what a crash
// leaves behind.
func TestWAVWriterUnclosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.wav")
	w, err := CreateWAV(path, 16000)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err := w.Write([]int16{5, 6, 7}); err != nil {
		t.Fatal(err)
	}
	if err := w.w.Flush(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if binary.LittleEndian.Uint32(b[40:]) != 0xFFFFFFFF {
		t.Fatalf("data size % x, want placeholder", b[40:44])
	}
	got, rate, err := DecodeWAV(b)
	if err != nil || rate != 16000 || len(got) != 3 || got[2] != 7 {
		t.Fatalf("DecodeWAV = %v, %d, %v; want [5 6 7], 16000", got, rate, err)
	}
}

// TestCreateWAVExclusive: an existing file is never overwritten.
func TestCreateWAVExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.wav")
	if err := os.WriteFile(path, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateWAV(path, 16000); err == nil {
		t.Fatal("CreateWAV over an existing file succeeded")
	}
	if b, _ := os.ReadFile(path); string(b) != "keep" {
		t.Fatalf("file changed to %q", b)
	}
}

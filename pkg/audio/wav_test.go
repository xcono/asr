package audio

import "testing"

// TestWAVRoundTrip encodes PCM with EncodeWAV and decodes it back with
// DecodeWAV, asserting samples and rate survive and the header is well-formed.
func TestWAVRoundTrip(t *testing.T) {
	in := []int16{0, 1, -1, 32767, -32768, 1000, -1000}
	const rate = 16000

	wav := EncodeWAV(in, rate)
	if len(wav) != 44+len(in)*2 {
		t.Fatalf("encoded length = %d, want %d (44-byte header + 2 bytes/sample)", len(wav), 44+len(in)*2)
	}
	if string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		t.Fatalf("missing RIFF/WAVE magic")
	}

	got, gotRate, err := DecodeWAV(wav)
	if err != nil {
		t.Fatalf("DecodeWAV: %v", err)
	}
	if gotRate != rate {
		t.Fatalf("decoded rate = %d, want %d", gotRate, rate)
	}
	if len(got) != len(in) {
		t.Fatalf("decoded sample count = %d, want %d", len(got), len(in))
	}
	for i := range in {
		if got[i] != in[i] {
			t.Fatalf("sample %d = %d, want %d", i, got[i], in[i])
		}
	}
}

// TestPCM16LE pins the little-endian byte layout.
func TestPCM16LE(t *testing.T) {
	got := PCM16LE([]int16{0, 1, -1, 0x1234})
	want := []byte{0, 0, 1, 0, 0xFF, 0xFF, 0x34, 0x12}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("PCM16LE = % x, want % x", got, want)
		}
	}
}

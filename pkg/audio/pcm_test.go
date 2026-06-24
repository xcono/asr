package audio

import (
	"reflect"
	"testing"
)

func TestDecodePCM16LERoundTrip(t *testing.T) {
	samples := []int16{0, 1, -1, 32767, -32768, 256, -257}
	got := DecodePCM16LE(PCM16LE(samples))
	if !reflect.DeepEqual(got, samples) {
		t.Fatalf("round-trip mismatch:\n got %v\nwant %v", got, samples)
	}
}

func TestDecodePCM16LEDropsOddTrailingByte(t *testing.T) {
	// A streamed chunk may split a sample across reads, leaving a dangling byte.
	// Decoding must drop the partial sample, not panic.
	got := DecodePCM16LE([]byte{0x00, 0x01, 0xff})
	want := []int16{0x0100}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

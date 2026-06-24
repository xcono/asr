// Package audio is the low-level audio layer: PortAudio capture, the PCM/WAV
// wire-format codec, and the ALSA-probe-spew silencer. It knows nothing about
// VAD, ASR, or the agent loop — callers feed it samples and rates.
package audio

import "encoding/binary"

// PCM16LE serialises int16 samples to little-endian bytes — the raw wire format
// the ASR servers expect for binary frames and WAV data.
func PCM16LE(samples []int16) []byte {
	b := make([]byte, len(samples)*2)
	for i, s := range samples {
		binary.LittleEndian.PutUint16(b[i*2:], uint16(s))
	}
	return b
}

// DecodePCM16LE deserialises little-endian 16-bit PCM bytes back to int16
// samples — the inverse of PCM16LE, used to decode the raw chunks the TTS server
// streams. A dangling trailing byte (a sample split across stream reads) is
// dropped rather than panicked on.
func DecodePCM16LE(b []byte) []int16 {
	samples := make([]int16, len(b)/2)
	for i := range samples {
		samples[i] = int16(binary.LittleEndian.Uint16(b[i*2:]))
	}
	return samples
}

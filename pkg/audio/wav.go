package audio

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// EncodeWAV wraps 16-bit LE mono PCM samples in a canonical 44-byte WAV header.
// The Qwen3-ASR batch endpoint decodes uploads with libsndfile, which needs a
// real container — headerless PCM does not decode.
func EncodeWAV(samples []int16, rate int) []byte {
	const (
		numChannels   = 1
		bitsPerSample = 16
	)
	dataLen := len(samples) * 2
	byteRate := rate * numChannels * bitsPerSample / 8
	blockAlign := numChannels * bitsPerSample / 8

	w := bytes.NewBuffer(make([]byte, 0, 44+dataLen))
	w.WriteString("RIFF")
	binary.Write(w, binary.LittleEndian, uint32(36+dataLen)) // file size minus 8
	w.WriteString("WAVE")
	w.WriteString("fmt ")
	binary.Write(w, binary.LittleEndian, uint32(16)) // PCM fmt chunk size
	binary.Write(w, binary.LittleEndian, uint16(1))  // audio format = PCM
	binary.Write(w, binary.LittleEndian, uint16(numChannels))
	binary.Write(w, binary.LittleEndian, uint32(rate))
	binary.Write(w, binary.LittleEndian, uint32(byteRate))
	binary.Write(w, binary.LittleEndian, uint16(blockAlign))
	binary.Write(w, binary.LittleEndian, uint16(bitsPerSample))
	w.WriteString("data")
	binary.Write(w, binary.LittleEndian, uint32(dataLen))
	binary.Write(w, binary.LittleEndian, samples) // int16 slice -> LE bytes
	return w.Bytes()
}

// DecodeWAV parses a little-endian 16-bit mono PCM WAV, returning its samples
// and declared sample rate. It walks RIFF chunks to find "fmt " and "data", so
// it tolerates extra metadata chunks. Inverse of EncodeWAV.
func DecodeWAV(b []byte) (samples []int16, rate int, err error) {
	if len(b) < 12 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, 0, fmt.Errorf("not a RIFF/WAVE file")
	}
	var data []byte
	for off := 12; off+8 <= len(b); {
		id := string(b[off : off+4])
		size := int(binary.LittleEndian.Uint32(b[off+4 : off+8]))
		end := off + 8 + size
		if end > len(b) {
			end = len(b)
		}
		body := b[off+8 : end]
		switch id {
		case "fmt ":
			if len(body) >= 8 {
				rate = int(binary.LittleEndian.Uint32(body[4:8]))
			}
		case "data":
			data = body
		}
		off += 8 + size + (size & 1) // chunks are word-aligned
	}
	samples = make([]int16, len(data)/2)
	for i := range samples {
		samples[i] = int16(binary.LittleEndian.Uint16(data[i*2:]))
	}
	return samples, rate, nil
}

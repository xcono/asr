package audio

// ResampleLinear converts int16 PCM at inRate to normalised float32 [-1,1] at
// outRate by linear interpolation — good enough to feed a VAD, not hi-fi.
func ResampleLinear(in []int16, inRate, outRate int) []float32 {
	if inRate == outRate {
		out := make([]float32, len(in))
		for i, s := range in {
			out[i] = float32(s) / 32768
		}
		return out
	}
	outLen := len(in) * outRate / inRate
	out := make([]float32, outLen)
	ratio := float64(inRate) / float64(outRate)
	for j := range out {
		pos := float64(j) * ratio
		i := int(pos)
		frac := float32(pos - float64(i))
		a := float32(in[i]) / 32768
		b := a
		if i+1 < len(in) {
			b = float32(in[i+1]) / 32768
		}
		out[j] = a + (b-a)*frac
	}
	return out
}

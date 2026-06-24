package vad

// The fork keeps the upstream module path; go.mod has a `replace` pointing it
// at hylarucoder/silero-vad-onnx-go.
import "github.com/streamer45/silero-vad-go/speech"

// Model wraps the Silero detector: it normalises an int16 window to float32
// [-1,1] and returns the per-window speech probability. The detector is
// stateful across calls (recurrent state) — never Reset between windows of a
// continuous stream.
type Model struct {
	sd  *speech.Detector
	buf []float32
}

// NewModel loads the Silero v5 ONNX model at modelPath. Needs ONNX Runtime at
// build/run time (see the Makefile ORT env).
func NewModel(modelPath string) (*Model, error) {
	sd, err := speech.NewDetector(speech.DetectorConfig{
		ModelPath:  modelPath,
		SampleRate: SampleRate,
		Threshold:  Threshold, // config validation requires (0,1)
	})
	if err != nil {
		return nil, err
	}
	return &Model{sd: sd, buf: make([]float32, Window)}, nil
}

// Infer returns the Silero speech probability for one window. window should be
// Window samples; a shorter window leaves the tail of the previous buffer.
func (m *Model) Infer(window []int16) (float32, error) {
	for i, s := range window {
		m.buf[i] = float32(s) / 32768
	}
	return m.sd.Infer(m.buf)
}

// Close releases the ONNX session.
func (m *Model) Close() error { return m.sd.Destroy() }

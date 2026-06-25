package asr

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/xcono/vox/pkg/vad"
)

// Source yields fixed-size windows of 16 kHz mono PCM. *audio.Capture is the mic
// implementation; a clip/file reader (e.g. TTS-generated audio resampled to
// 16 kHz) substitutes it for replay and tests. Read returns a non-nil error
// (io.EOF for a finite source) to end listening.
type Source interface {
	Read() ([]int16, error)
}

// Detector returns the per-window speech probability. *vad.Model is the Silero
// implementation; tests inject scripted probabilities to avoid the ONNX model.
type Detector interface {
	Infer(window []int16) (float32, error)
}

// Recognizer transcribes a finished utterance. *Transcriber satisfies it.
type Recognizer interface {
	Transcribe(ctx context.Context, samples []int16, rate int) (string, error)
}

// EventKind tags what happened on the channel. SpeechError is terminal: the
// pipeline hit an unexpected source/detector error and will close the channel
// immediately after emitting it. io.EOF on a finite source and context
// cancellation are clean shutdowns and do NOT emit SpeechError.
type EventKind int

const (
	SpeechStart EventKind = iota // VAD confirmed onset — a turn began
	SpeechEnd                    // VAD confirmed offset — the turn closed
	SpeechText                   // transcription of the closed turn is ready
	SpeechError                  // terminal pipeline error; Err carries the cause
)

func (k EventKind) String() string {
	switch k {
	case SpeechStart:
		return "SpeechStart"
	case SpeechEnd:
		return "SpeechEnd"
	case SpeechText:
		return "SpeechText"
	case SpeechError:
		return "SpeechError"
	default:
		return "Event(?)"
	}
}

// Event is one signal from the listen loop. Text is set only for SpeechText;
// Err is set only for SpeechError. Timestamp records when the event was
// created, and VoiceFileID associates the transcription with an audio
// recording file (if any).
type Event struct {
	Kind        EventKind
	Text        string
	Timestamp   time.Time
	VoiceFileID string
	Err         error // populated for SpeechError
}

// Listen runs the capture → VAD → segment → transcribe turn pipeline and returns
// a channel of events: SpeechStart and SpeechEnd are emitted synchronously as the
// VAD confirms turn boundaries (so act can react to barge-in immediately), and
// SpeechText follows once the closed turn is transcribed off the loop — so a slow
// recogniser never stalls capture. The channel closes when src is exhausted (its
// Read errors) or ctx is cancelled. This is the ASR domain's "controller": act
// consumes it; it knows nothing about act.
func Listen(ctx context.Context, src Source, det Detector, rec Recognizer, t vad.Timing) <-chan Event {
	out := make(chan Event)

	go func() {
		defer close(out)
		var wg sync.WaitGroup
		defer wg.Wait() // let in-flight transcriptions finish before closing

		fsm := vad.NewFSM(t)
		pre := vad.NewPreroll(t.PrerollSamples())
		var utter []int16
		capturing := false

		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			frame, err := src.Read()
			if err != nil {
				if err != io.EOF {
					emit(ctx, out, Event{Kind: SpeechError, Timestamp: time.Now(), Err: err})
				}
				return // EOF (clean) or transport error ends listening
			}
			pre.Push(frame)

			prob, err := det.Infer(frame)
			if err != nil {
				emit(ctx, out, Event{Kind: SpeechError, Timestamp: time.Now(), Err: err})
				return
			}

			if capturing {
				utter = append(utter, frame...)
			}

			switch fsm.Update(prob) {
			case vad.ToSpeaking:
				emit(ctx, out, Event{Kind: SpeechStart, Timestamp: time.Now()})
				utter = pre.Snapshot() // seed with the pre-onset lead-in
				capturing = true
			case vad.ToSilence:
				if !capturing {
					continue
				}
				capturing = false
				emit(ctx, out, Event{Kind: SpeechEnd, Timestamp: time.Now()})
				clip := utter
				utter = nil
				wg.Go(func() {
					text, err := rec.Transcribe(ctx, clip, vad.SampleRate)
					if err != nil || text == "" {
						return // skip silent/failed turns
					}
					emit(ctx, out, Event{Kind: SpeechText, Text: text, Timestamp: time.Now()})
				})
			}
		}
	}()

	return out
}

// emit sends e unless ctx is cancelled, so a stalled consumer plus cancellation
// can't deadlock the loop or a transcription goroutine.
func emit(ctx context.Context, out chan<- Event, e Event) {
	select {
	case out <- e:
	case <-ctx.Done():
	}
}

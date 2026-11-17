package asr

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/xcono/asr/pkg/audio"
	"github.com/xcono/asr/pkg/vad"
)

// Source yields fixed-size windows of 16 kHz mono PCM. *audio.Capture is the mic
// implementation; a clip/file reader (e.g. TTS-generated audio resampled to
// 16 kHz) substitutes it for replay and tests. Read returns a non-nil error
// (io.EOF for a finite source) to end listening.
type Source interface {
	Read() ([]int16, error)
}

// Detector returns the per-window speech probability. *vad.Model is the Silero
// implementation; tests inject scripted probabilities to stay deterministic.
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
//
// Audio, CharsPerSec and Segments are diagnostics for SpeechText, kept so a
// log can show why a turn passed the rate filter; consumers may ignore them.
// VAD is set on SpeechEnd and SpeechText alike, so a turn whose transcription
// is empty, failed or dropped still shows what the VAD saw.
type Event struct {
	Kind        EventKind
	Text        string
	Timestamp   time.Time
	VoiceFileID string
	Err         error // populated for SpeechError

	Audio       time.Duration // length of the clip sent to the recogniser (preroll included)
	CharsPerSec float64       // runes of the trimmed Text per second of Audio
	Segments    []Segment     // recogniser scores; nil unless it implements ScoredRecognizer
	VAD         VADStats      // Silero over the clip's windows; zero on SpeechStart/SpeechError
}

// VADStats summarises Silero's per-window probabilities over exactly the
// windows of the clip sent to the recogniser — the preroll windows included,
// because the onset evidence (the first BargeInMs above Threshold) sits in the
// preroll, not after the confirmed onset. It exists to tell a real utterance
// from a short non-speech sound that opened a segment: such a burst shows as a
// few speech windows out of ~30 (onset + ReleaseMs of tail), where speech
// fills most of the clip.
type VADStats struct {
	Peak    float32 // highest probability in the clip
	Speech  int     // windows with probability >= Timing.Threshold
	Windows int     // all windows in the clip
}

// add accounts for one window's probability.
func (s *VADStats) add(prob, threshold float32) {
	s.Windows++
	if prob > s.Peak {
		s.Peak = prob
	}
	if prob >= threshold {
		s.Speech++
	}
}

// String renders the stats for a log line as " vad_peak=0.87 speech_win=5/34",
// or "" for the zero value, matching FormatSegments' leading-space convention.
func (s VADStats) String() string {
	if s.Windows == 0 {
		return ""
	}
	return fmt.Sprintf(" vad_peak=%.2f speech_win=%d/%d", s.Peak, s.Speech, s.Windows)
}

// ListenOption tunes Listen. The zero set (no options) is the pipeline as it
// was before options existed: every non-empty transcription is emitted.
type ListenOption func(*listenOptions)

type listenOptions struct {
	maxCharsPerSec float64
	dumpDir        string
}

// WithMaxCharsPerSec drops a transcription whose text rate — runes of the
// trimmed text per second of submitted audio — exceeds limit; 0 (or less)
// disables the check. It exists because Whisper, given a short unclear sound,
// hallucinates subtitle credits from its training data ("Редактор субтитров
// А.Семкин Корректор А.Егорова": 47 runes from 0.77 s of speech, ~44/s over
// the clip with preroll), while real speech runs at 15–18/s and a lone
// "Да." at 4–9/s. A count of speech windows was rejected as the gate: it
// cannot tell such a burst from a genuine "Да". The dropped turn is logged,
// never emitted, so its SpeechEnd is the last event of that turn.
func WithMaxCharsPerSec(limit float64) ListenOption {
	return func(o *listenOptions) { o.maxCharsPerSec = limit }
}

// WithDumpDropped writes the clip of every transcription WithMaxCharsPerSec
// drops to dir as a 16 kHz mono 16-bit WAV, named by the time of the drop
// (dropped-20261118-034512.345.wav), and puts the path in the "asr: dropped"
// line; "" disables it. It exists so a person can listen to what opened a
// false segment — the text alone is a hallucination and says nothing about
// the sound. dir is created on first use. The write runs in the transcription
// goroutine, off the capture loop; a failure is logged and the pipeline goes on.
func WithDumpDropped(dir string) ListenOption {
	return func(o *listenOptions) { o.dumpDir = dir }
}

// Listen runs the capture → VAD → segment → transcribe turn pipeline and returns
// a channel of events: SpeechStart and SpeechEnd are emitted synchronously as the
// VAD confirms turn boundaries (so act can react to barge-in immediately), and
// SpeechText follows once the closed turn is transcribed off the loop — so a slow
// recogniser never stalls capture. The channel closes when src is exhausted (its
// Read errors) or ctx is cancelled. This is the ASR domain's "controller": act
// consumes it; it knows nothing about act.
func Listen(ctx context.Context, src Source, det Detector, rec Recognizer, t vad.Timing, opts ...ListenOption) <-chan Event {
	var o listenOptions
	for _, opt := range opts {
		opt(&o)
	}
	out := make(chan Event)

	go func() {
		defer close(out)
		var wg sync.WaitGroup
		defer wg.Wait() // let in-flight transcriptions finish before closing

		fsm := vad.NewFSM(t)
		pre := vad.NewPreroll(t.PrerollSamples())
		// preProbs mirrors pre window for window (Source yields one window
		// per Read), so a confirmed onset can seed the stats with exactly
		// the windows the preroll snapshot puts into the clip.
		preProbs := make([]float32, 0, t.PrerollSamples()/vad.Window+1)
		var utter []int16
		var stats VADStats
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

			if len(preProbs) == cap(preProbs)-1 {
				preProbs = append(preProbs[:0], preProbs[1:]...)
			}
			preProbs = append(preProbs, prob)

			if capturing {
				utter = append(utter, frame...)
				stats.add(prob, t.Threshold)
			}

			switch fsm.Update(prob) {
			case vad.ToSpeaking:
				emit(ctx, out, Event{Kind: SpeechStart, Timestamp: time.Now()})
				utter = pre.Snapshot() // seed with the pre-onset lead-in
				stats = VADStats{}
				for _, p := range preProbs {
					stats.add(p, t.Threshold)
				}
				capturing = true
			case vad.ToSilence:
				if !capturing {
					continue
				}
				capturing = false
				vs := stats
				emit(ctx, out, Event{Kind: SpeechEnd, Timestamp: time.Now(), VAD: vs})
				clip := utter
				utter = nil
				wg.Go(func() {
					tr, err := transcribe(ctx, rec, clip)
					if err != nil || tr.Text == "" {
						return // skip silent/failed turns
					}
					ev := Event{
						Kind:     SpeechText,
						Text:     tr.Text,
						Audio:    time.Duration(len(clip)) * time.Second / vad.SampleRate,
						Segments: tr.Segments,
						VAD:      vs,
					}
					ev.CharsPerSec = charsPerSec(ev.Text, ev.Audio)
					if o.maxCharsPerSec > 0 && ev.CharsPerSec > o.maxCharsPerSec {
						wav := ""
						if o.dumpDir != "" {
							if path, err := dumpClip(o.dumpDir, clip, time.Now()); err != nil {
								log.Printf("asr: dump dropped clip: %v", err)
							} else {
								wav = " wav=" + path
							}
						}
						log.Printf("asr: dropped %q: %.2fs, %.1f chars/s > %g%s%s%s",
							ev.Text, ev.Audio.Seconds(), ev.CharsPerSec, o.maxCharsPerSec,
							ev.VAD, FormatSegments(ev.Segments), wav)
						return
					}
					ev.Timestamp = time.Now()
					emit(ctx, out, ev)
				})
			}
		}
	}()

	return out
}

// dumpClip writes clip as a WAV named by now into dir, creating dir if needed,
// and returns the file's path. O_EXCL keeps two drops in the same millisecond
// from overwriting each other; the second one fails and is logged instead.
func dumpClip(dir string, clip []int16, now time.Time) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "dropped-"+now.Format("20060102-150405.000")+".wav")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", err
	}
	if _, err := f.Write(audio.EncodeWAV(clip, vad.SampleRate)); err != nil {
		f.Close()
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("close %s: %w", path, err)
	}
	return path, nil
}

// transcribe asks rec for the text and, when it can give them, the scores.
func transcribe(ctx context.Context, rec Recognizer, clip []int16) (Transcript, error) {
	if sr, ok := rec.(ScoredRecognizer); ok {
		return sr.TranscribeScored(ctx, clip, vad.SampleRate)
	}
	text, err := rec.Transcribe(ctx, clip, vad.SampleRate)
	return Transcript{Text: text}, err
}

// charsPerSec is the text rate the hallucination filter compares: runes, not
// bytes, so Cyrillic counts the same as Latin.
func charsPerSec(text string, d time.Duration) float64 {
	if d <= 0 {
		return 0
	}
	return float64(utf8.RuneCountInString(strings.TrimSpace(text))) / d.Seconds()
}

// FormatSegments renders scores for a log line as
// " no_speech=0.012,0.012 logprob=-0.31,-0.45" (one value per segment), or ""
// when there are none, so a line for a scoreless provider stays as it was.
func FormatSegments(segs []Segment) string {
	if len(segs) == 0 {
		return ""
	}
	ns := make([]string, len(segs))
	lp := make([]string, len(segs))
	for i, s := range segs {
		ns[i] = fmt.Sprintf("%.3f", s.NoSpeechProb)
		lp[i] = fmt.Sprintf("%.2f", s.AvgLogprob)
	}
	return " no_speech=" + strings.Join(ns, ",") + " logprob=" + strings.Join(lp, ",")
}

// emit sends e unless ctx is cancelled, so a stalled consumer plus cancellation
// can't deadlock the loop or a transcription goroutine.
func emit(ctx context.Context, out chan<- Event, e Event) {
	select {
	case out <- e:
	case <-ctx.Done():
	}
}

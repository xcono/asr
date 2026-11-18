package asr

import (
	"context"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
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
// Offset places the event in the audio rather than on the clock: the samples
// Listen has read from its Source up to and including the window that
// confirmed the event, as a duration at vad.SampleRate. SpeechText repeats
// the Offset of its SpeechEnd (the clip's end). Counting samples, not wall
// time, keeps it exact against a Recorder wrapping the same Source — its WAV
// has the same time base — however late the event is consumed.
//
// Audio, CharsPerSec, Letters, Budget and Segments are diagnostics for
// SpeechText, kept so a log can show why a turn passed the hallucination
// filter; consumers may ignore them. VAD is set on SpeechEnd and SpeechText
// alike, so a turn whose transcription is empty, failed or dropped still
// shows what the VAD saw.
type Event struct {
	Kind        EventKind
	Text        string
	Timestamp   time.Time
	VoiceFileID string
	Err         error         // populated for SpeechError
	Offset      time.Duration // position in the Source stream (see above)

	Audio       time.Duration // length of the clip sent to the recogniser (preroll included)
	CharsPerSec float64       // runes of the trimmed Text per second of Audio
	Letters     int           // letters and digits in Text, what the budget counts
	Budget      float64       // letters allowed for Audio; +Inf when the filter is off
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
	charsSlack     float64
	dumpDir        string
	phrases        phraseFilter
}

// DefaultCharsSlack is the letters a clip may carry beyond its rate budget
// when WithCharsSlack is not given: a short real answer ("Я не знаю.", 7
// letters in a 1.15 s clip) fits in it whole, the shortest observed
// hallucination ("Продолжение следует", 18 letters in 1.06 s) does not.
const DefaultCharsSlack = 12

// WithMaxCharsPerSec turns on the hallucination filter: a transcription is
// dropped when its letters and digits exceed the budget
//
//	slack + limit × max(0, clip − (PrerollMs + ReleaseMs))
//
// with slack from WithCharsSlack (DefaultCharsSlack when absent) and the
// pause from the Timing Listen runs with. limit <= 0 disables the filter.
//
// Whisper, given a short unclear sound, hallucinates subtitle credits and
// sign-offs from its training data ("Редактор субтитров А.Семкин Корректор
// А.Егорова", "Продолжение следует"). A plain rate cap over the whole clip
// missed them: every clip carries ~1 s that is not speech — the preroll
// before the onset and the release tail of silence — so a short clip's rate
// is diluted. "Продолжение следует" in a 1.06 s clip is 20.8 runes/s, below
// any cap that fast connected speech (15–18/s) clears. The budget subtracts
// that pause and charges the rate only for the rest, so the 0.06 s of
// actual speech buys 1.5 letters on top of the slack: 18 > 13.5, dropped,
// while a long sentence still gets 25 letters for every second of it.
// Counting letters and digits, not runes, keeps punctuation and spaces —
// which Whisper adds freely ("Не знаю...") — out of the sum.
//
// A count of speech windows was rejected as the gate: it cannot tell such a
// burst from a genuine "Да". The dropped turn is logged, never emitted, so
// its SpeechEnd is the last event of that turn.
func WithMaxCharsPerSec(limit float64) ListenOption {
	return func(o *listenOptions) { o.maxCharsPerSec = limit }
}

// WithCharsSlack sets the letters allowed regardless of clip length in the
// WithMaxCharsPerSec budget (DefaultCharsSlack when absent). It has no effect
// while the filter is off.
func WithCharsSlack(n float64) ListenOption {
	return func(o *listenOptions) { o.charsSlack = n }
}

// WithHallucinationPhrases turns on the second line of the hallucination
// filter: a transcription the letter budget let through is still dropped when
// it contains one of phrases (see DefaultHallucinationPhrases for the entry
// syntax and phraseFilter.match for the rule). nil or empty disables it.
//
// It exists because the budget cannot catch a credit on a clip long enough to
// afford its letters: on a clear "доделай" (1.50 s, VAD peak 0.99) Whisper
// returned "Добавил субтитры DimaTorzok", 25 letters against a budget of 24.6
// — 30 ms more audio and it would have passed. Whisper's own scores
// (no_speech_prob, avg_logprob) did not tell it from speech either; the
// words themselves do. The budget is checked first, so a turn over it is
// logged with the budget as its reason whether or not a phrase also matches.
func WithHallucinationPhrases(phrases []string) ListenOption {
	return func(o *listenOptions) { o.phrases = newPhraseFilter(phrases) }
}

// WithDumpDropped writes the clip of every transcription WithMaxCharsPerSec
// or WithHallucinationPhrases drops to dir as a 16 kHz mono 16-bit WAV, named
// by the time of the drop (dropped-20261118-034512.345.wav), and puts the path
// in the "asr: dropped" line; "" disables it. It exists so a person can listen to what opened a
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
	o := listenOptions{charsSlack: DefaultCharsSlack}
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
		var read int64 // samples read from src: Event.Offset's clock
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
			read += int64(len(frame))
			at := time.Duration(read) * time.Second / vad.SampleRate
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
				emit(ctx, out, Event{Kind: SpeechStart, Timestamp: time.Now(), Offset: at})
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
				emit(ctx, out, Event{Kind: SpeechEnd, Timestamp: time.Now(), Offset: at, VAD: vs})
				clip := utter
				utter = nil
				wg.Go(func() {
					tr, err := transcribe(ctx, rec, clip)
					if err != nil {
						// Log it: a failed turn used to vanish, so a hung STT
						// server (every request timing out) looked like the
						// VAD hearing speech and nothing else happening. Not a
						// SpeechError — that kind is terminal and the next
						// turn may well succeed. Cancellation is a shutdown,
						// not a failure.
						if ctx.Err() == nil {
							log.Printf("asr: transcription failed: at=%.2fs %.2fs: %v",
								at.Seconds(), float64(len(clip))/vad.SampleRate, err)
						}
						return
					}
					if tr.Text == "" {
						return // skip silent turns
					}
					ev := Event{
						Kind:     SpeechText,
						Text:     tr.Text,
						Offset:   at,
						Audio:    time.Duration(len(clip)) * time.Second / vad.SampleRate,
						Letters:  countLetters(tr.Text),
						Budget:   math.Inf(1),
						Segments: tr.Segments,
						VAD:      vs,
					}
					ev.CharsPerSec = charsPerSec(ev.Text, ev.Audio)
					if o.maxCharsPerSec > 0 {
						ev.Budget = letterBudget(ev.Audio, t, o.charsSlack, o.maxCharsPerSec)
					}
					reason := dropReason(ev, o.phrases)
					if reason != "" {
						wav := ""
						if o.dumpDir != "" {
							if path, err := dumpClip(o.dumpDir, clip, time.Now()); err != nil {
								log.Printf("asr: dump dropped clip: %v", err)
							} else {
								wav = " wav=" + path
							}
						}
						log.Printf("asr: dropped %q: at=%.2fs %.2fs, %s, %.1f chars/s%s%s%s",
							ev.Text, ev.Offset.Seconds(), ev.Audio.Seconds(), reason, ev.CharsPerSec,
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

// dropReason is why ev must not be emitted, for the "asr: dropped" line, or
// "" to emit it. The budget goes first and, when it fires, is the only reason
// given — its wording is the line's from before phrases existed:
//
//	41 letters > budget 16.5
//	phrase "*DimaTorzok" 10/25 letters, budget 24.6
//
// A phrase reason lists the entries that decided it, as configured, and the
// letters they cover out of the text's.
func dropReason(ev Event, f phraseFilter) string {
	if float64(ev.Letters) > ev.Budget {
		return fmt.Sprintf("%d letters > budget %.1f", ev.Letters, ev.Budget)
	}
	hits, covered := f.match(ev.Text)
	if hits == nil {
		return ""
	}
	q := make([]string, len(hits))
	for i, h := range hits {
		q[i] = strconv.Quote(h)
	}
	return fmt.Sprintf("phrase %s %d/%d letters, budget %.1f", strings.Join(q, ","), covered, ev.Letters, ev.Budget)
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

// letterBudget is the letters WithMaxCharsPerSec allows in a clip of length d:
// slack plus rate for every second beyond the pause the clip always carries —
// the preroll lead-in and the release tail (1.0 s at the defaults). The pause
// is taken from the configured milliseconds, not rounded to windows, so the
// budget reads off the config directly.
func letterBudget(d time.Duration, t vad.Timing, slack, rate float64) float64 {
	pause := time.Duration(t.PrerollMs+t.ReleaseMs) * time.Millisecond
	return slack + rate*max(0, (d-pause).Seconds())
}

// countLetters counts the letters and digits of text — what a speaker
// actually says; punctuation and spaces are the recogniser's formatting.
func countLetters(text string) int {
	n := 0
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			n++
		}
	}
	return n
}

// charsPerSec is the plain text rate, runes per second of the whole clip,
// kept for the log alongside the letter budget that now decides: runes, not
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

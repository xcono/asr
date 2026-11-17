// Package stt is the importable, NATS-free facade for the speech-to-text
// pipeline. It wraps the capture → Silero VAD → transcribe controller
// (pkg/asr.Listen) behind a small surface the LLM loop in ../voices can wire
// in-process: a stream of events, an atomic "is the user speaking" flag, and a
// one-shot batch transcribe. NATS lives only behind cmd/vox (the standalone
// debug binary); nothing here starts an embedded server.
//
// Contract note: this package returns the concrete *Service. Consumers should
// declare their own narrow interface for the subset they need (Go's
// consumer-side-interface idiom — see pkg/asr.Source/Detector/Recognizer), so
// they can mock STT in their own tests without importing pkg/vad and the mic.
// Event types are re-exported as aliases below so a consumer can depend on this
// one package as the whole STT contract.
package stt

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/xcono/asr/pkg/asr"
	"github.com/xcono/asr/pkg/audio"
	"github.com/xcono/asr/pkg/config"
	"github.com/xcono/asr/pkg/vad"
)

// Event and its kinds are aliased from pkg/asr so this package is a complete
// STT contract on its own. They are type aliases (identity), so a value from
// asr.Listen and one observed here are the same type — a consumer may import
// either path interchangeably.
type (
	Event     = asr.Event
	EventKind = asr.EventKind
)

const (
	SpeechStart = asr.SpeechStart // VAD confirmed onset — a turn began
	SpeechEnd   = asr.SpeechEnd   // VAD confirmed offset — the turn closed
	SpeechText  = asr.SpeechText  // transcription of the closed turn is ready
	SpeechError = asr.SpeechError // terminal pipeline error; Event.Err carries it
)

// Service owns one running capture → VAD → transcribe pipeline and exposes it
// three ways: Events() for streaming, IsSpeaking() for the VAD TRUE/FALSE
// barge-in signal, and Transcribe() for one-shot batch use. Build it with New
// (owns the mic + Silero model) or NewWith (inject your own source/detector,
// for tests or a shared audio device).
type Service struct {
	out      chan Event
	speaking atomic.Bool
	rec      asr.Recognizer

	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{} // closed when the forwarder goroutine exits
	closeMu sync.Once
	closers []func() error // resources New owns (mic, model); nil for NewWith
}

// New builds the default pipeline from config: the Silero model (pure Go), the
// mic (a PulseAudio record stream, pure Go), and the configured STT provider —
// the same wiring as cmd/vox. It owns those resources and frees them on Close.
// The mic has its own server connection, so nothing here is process-global; a
// consumer that already owns the audio device should still use NewWith.
func New(cfg *config.Config) (*Service, error) {
	rec, err := NewRecognizer(cfg.ASR)
	if err != nil {
		return nil, err
	}

	model, err := vad.NewModel()
	if err != nil {
		return nil, fmt.Errorf("stt: vad model: %w", err)
	}

	mic, err := audio.OpenCapture(vad.SampleRate, vad.Window)
	if err != nil {
		model.Close()
		return nil, fmt.Errorf("stt: open capture: %w", err)
	}

	// Teardown order: stop the mic, then free the model.
	closers := []func() error{
		mic.Close,
		model.Close,
	}
	return start(rec, mic, model, cfg.VAD.ToTiming(), closers,
		asr.WithMaxCharsPerSec(cfg.ASR.CharsPerSecLimit()),
		asr.WithDumpDropped(cfg.ASR.DumpDroppedDir)), nil
}

// ListenOption tunes the pipeline NewWith starts; aliased from pkg/asr so a
// consumer needs only this package.
type ListenOption = asr.ListenOption

// WithMaxCharsPerSec drops live transcriptions faster than limit runes per
// second of audio (0 disables); see asr.WithMaxCharsPerSec. New applies
// config.ASRConfig.CharsPerSecLimit itself; NewWith filters only when given it.
func WithMaxCharsPerSec(limit float64) ListenOption { return asr.WithMaxCharsPerSec(limit) }

// WithDumpDropped saves each clip WithMaxCharsPerSec drops to dir as a WAV
// ("" disables); see asr.WithDumpDropped. New applies
// config.ASRConfig.DumpDroppedDir itself; NewWith dumps only when given it.
func WithDumpDropped(dir string) ListenOption { return asr.WithDumpDropped(dir) }

// NewWith starts a pipeline over caller-supplied components and owns none of
// them — the caller closes src/det. Use it in tests (scripted Source/Detector,
// no mic) or when ../voices already owns the audio device and
// feeds STT from a shared source. Timing comes from config.VADConfig.ToTiming
// or vad.DefaultTiming. Without opts every transcription is emitted; pass
// WithMaxCharsPerSec(cfg.ASR.CharsPerSecLimit()) to filter as New does, and
// WithDumpDropped(cfg.ASR.DumpDroppedDir) to save what it drops.
func NewWith(src asr.Source, det asr.Detector, rec asr.Recognizer, t vad.Timing, opts ...ListenOption) *Service {
	return start(rec, src, det, t, nil, opts...)
}

// start wires asr.Listen and the event forwarder. The forwarder is the single
// place the atomic speaking flag is updated, keeping IsSpeaking consistent with
// the event stream a consumer sees.
func start(rec asr.Recognizer, src asr.Source, det asr.Detector, t vad.Timing, closers []func() error, opts ...ListenOption) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{
		out:     make(chan Event),
		rec:     rec,
		ctx:     ctx,
		cancel:  cancel,
		done:    make(chan struct{}),
		closers: closers,
	}
	go s.forward(asr.Listen(ctx, src, det, rec, t, opts...))
	return s
}

// forward relays pipeline events to consumers and maintains the speaking flag.
// The select on ctx.Done mirrors asr.emit so a consumer that stops reading
// can't wedge Close: cancelling the context unblocks both this send and the
// underlying loop.
func (s *Service) forward(events <-chan Event) {
	defer close(s.done)
	defer close(s.out)
	for ev := range events {
		switch ev.Kind {
		case asr.SpeechStart:
			s.speaking.Store(true)
		case asr.SpeechEnd:
			s.speaking.Store(false)
		}
		select {
		case s.out <- ev:
		case <-s.ctx.Done():
			return
		}
	}
}

// Events returns the read-only stream of pipeline events (Stream). The channel
// closes when the source is exhausted or Close is called.
func (s *Service) Events() <-chan Event { return s.out }

// IsSpeaking reports whether the VAD currently considers a turn open — the
// TRUE/FALSE barge-in signal ../voices uses to interrupt TTS. It flips to true
// on SpeechStart and false on SpeechEnd; it is safe to poll from any goroutine.
func (s *Service) IsSpeaking() bool { return s.speaking.Load() }

// Transcribe runs a one-shot batch transcription of 16 kHz mono PCM (Batch),
// reusing the configured provider. Independent of the live capture stream — for
// transcribing a clip on demand. The chars-per-second filter does not apply:
// the caller chose the clip, so there is no VAD burst to guard against.
func (s *Service) Transcribe(ctx context.Context, pcm []int16) (string, error) {
	return s.rec.Transcribe(ctx, pcm, vad.SampleRate)
}

// Close stops the pipeline, waits for the forwarder to drain, then frees any
// resources New owns (NewWith owns none). Idempotent.
func (s *Service) Close() error {
	var err error
	s.closeMu.Do(func() {
		s.cancel()
		<-s.done
		for _, c := range s.closers {
			if e := c(); e != nil && err == nil {
				err = e
			}
		}
	})
	return err
}

// NewRecognizer builds the STT provider from config: local GigaAM ("gigaam"),
// local whisper.cpp ("whisper" — the same batch client plus language/prompt)
// or cloud ElevenLabs Scribe ("elevenlabs"). It is the only provider switch:
// cmd/vox and the facade both call it. Exported so consumers that inject
// components via NewWith (e.g. ../voices WAV replay) build the same provider
// the live pipeline uses.
func NewRecognizer(c config.ASRConfig) (asr.Recognizer, error) {
	switch c.Provider {
	case "gigaam":
		return &asr.Transcriber{BaseURL: c.GigaAM.BaseURL, Model: c.GigaAM.Model}, nil
	case "whisper":
		w := c.Whisper
		// Verbose: verbose_json carries the no_speech_prob / avg_logprob scores
		// cmd/vox logs. GigaAM stays on plain json — its server rejects others.
		return &asr.Transcriber{BaseURL: w.BaseURL, Model: w.Model, Language: w.Language, Prompt: w.Prompt, Verbose: true}, nil
	case "elevenlabs":
		return &asr.ElevenLabs{
			APIKey:       c.ElevenLabs.APIKey,
			Model:        c.ElevenLabs.Model,
			LanguageCode: c.ElevenLabs.LanguageCode,
			BaseURL:      c.ElevenLabs.BaseURL,
		}, nil
	default:
		return nil, fmt.Errorf("stt: unknown provider %q", c.Provider)
	}
}

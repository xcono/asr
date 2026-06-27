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
// they can mock STT in their own tests without importing the cgo-heavy pkg/vad.
// Event types are re-exported as aliases below so a consumer can depend on this
// one package as the whole STT contract.
package stt

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/gordonklaus/portaudio"

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
	closers []func() error // resources New owns (mic, model, portaudio); nil for NewWith
}

// New builds the default pipeline from config: the Silero model (cgo/ONNX), the
// PortAudio mic, and the configured STT provider — the same wiring as cmd/vox
// minus NATS. It owns those resources and frees them on Close.
//
// Caveat: New calls portaudio.Initialize and Close calls portaudio.Terminate,
// which are process-global. Run at most one New-built Service per process. A
// consumer that already owns the audio device should use NewWith instead.
func New(cfg *config.Config) (*Service, error) {
	rec, err := recognizer(cfg.STT)
	if err != nil {
		return nil, err
	}

	model, err := vad.NewModel(cfg.VAD.ModelPath)
	if err != nil {
		return nil, fmt.Errorf("stt: vad model: %w", err)
	}

	// PortAudio probes devices on init and spews ALSA/JACK warnings to the raw
	// C-level stderr; silence it for that window only. Best-effort — a failure
	// here is cosmetic, so proceed without it.
	restore, _ := audio.SilenceStderr()
	if err := portaudio.Initialize(); err != nil {
		if restore != nil {
			restore()
		}
		model.Close()
		return nil, fmt.Errorf("stt: portaudio init: %w", err)
	}
	mic, err := audio.OpenCapture(vad.SampleRate, vad.Window)
	if restore != nil {
		restore()
	}
	if err != nil {
		portaudio.Terminate()
		model.Close()
		return nil, fmt.Errorf("stt: open capture: %w", err)
	}

	// Teardown order: stop the mic, free the model, then terminate PortAudio.
	closers := []func() error{
		mic.Close,
		model.Close,
		portaudio.Terminate,
	}
	return start(rec, mic, model, cfg.VAD.ToTiming(), closers), nil
}

// NewWith starts a pipeline over caller-supplied components and owns none of
// them — the caller closes src/det. Use it in tests (scripted Source/Detector,
// no ONNX or PortAudio) or when ../voices already owns the audio device and
// feeds STT from a shared source. Timing comes from config.VADConfig.ToTiming
// or vad.DefaultTiming.
func NewWith(src asr.Source, det asr.Detector, rec asr.Recognizer, t vad.Timing) *Service {
	return start(rec, src, det, t, nil)
}

// start wires asr.Listen and the event forwarder. The forwarder is the single
// place the atomic speaking flag is updated, keeping IsSpeaking consistent with
// the event stream a consumer sees.
func start(rec asr.Recognizer, src asr.Source, det asr.Detector, t vad.Timing, closers []func() error) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{
		out:     make(chan Event),
		rec:     rec,
		ctx:     ctx,
		cancel:  cancel,
		done:    make(chan struct{}),
		closers: closers,
	}
	go s.forward(asr.Listen(ctx, src, det, rec, t))
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
// transcribing a clip on demand.
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

// recognizer builds the STT provider from config. Mirrors cmd/vox: only
// "gigaam" is wired today; "elevenlabs" is config-only until a Recognizer
// implementation exists.
func recognizer(c config.STTConfig) (asr.Recognizer, error) {
	switch c.Provider {
	case "gigaam":
		return &asr.Transcriber{BaseURL: c.GigaAM.BaseURL, Model: c.GigaAM.Model}, nil
	default:
		return nil, fmt.Errorf("stt: unknown provider %q", c.Provider)
	}
}

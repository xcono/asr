# Spec 09 — Importable, NATS-free STT facade (`package stt`)

## Context
Decoupling (see `goal.txt`) makes `stt` a Go library that `../voices` (the LLM
loop) imports in-process; integration is over Go channels, not NATS. Until now
the only entry point was `cmd/vox/main.go`, which hard-wires an embedded NATS
server into the pipeline. A consumer that just wants events + a barge-in signal
had to re-implement the whole capture → VAD → transcribe wiring.

## Goal
A root `stt` package exposing the pipeline as a small, NATS-free contract the
LLM loop binds to. NATS stays only behind `cmd/vox` (the standalone debug
binary).

## Contract (`stt.go`)
`*Service` — concrete, returned by the constructors. The authoritative surface:

```go
func New(cfg *config.Config) (*Service, error)                         // mic + Silero, owns resources
func NewWith(src asr.Source, det asr.Detector, rec asr.Recognizer,
             t vad.Timing) *Service                                    // inject; owns nothing

func (s *Service) Events() <-chan Event                                // Stream
func (s *Service) IsSpeaking() bool                                    // VAD TRUE/FALSE (atomic)
func (s *Service) Transcribe(ctx, pcm []int16) (string, error)         // Batch (16 kHz mono)
func (s *Service) Close() error                                        // idempotent
```

`Event` / `EventKind` / `SpeechStart…SpeechError` are **type aliases** of the
`pkg/asr` types, so this one package is the whole STT contract yet a value from
`asr.Listen` and one from `Events()` are the same type.

## Design decisions
- **Returns a concrete `*Service`, not an interface.** Accept interfaces, return
  structs. `../voices` declares its own narrow consumer interface (e.g.
  `type Ears interface { Events() <-chan stt.Event; IsSpeaking() bool }`) for
  mocking — the provider does not dictate it. Matches the consumer-side-interface
  convention already used by `asr.Source/Detector/Recognizer`.
- **`IsSpeaking()` is an `atomic.Bool`** flipped in the single event-forwarder
  goroutine: `true` on `SpeechStart`, `false` on `SpeechEnd`. It is a *poll* for
  barge-in ("VAD TRUE → interrupt TTS"), not a synchronous per-event check — the
  boundary may flip the instant after an event is delivered. Tests therefore
  read it only after the stream closes (race-free happens-before).
- **`New` owns PortAudio (process-global).** `Initialize`/`Terminate` are global,
  so at most one `New`-built `Service` per process. A consumer that already owns
  the audio device uses `NewWith` with its own `asr.Source`.
- **NATS-free.** No embedded server here; `pkg/nats` is reachable only via
  `cmd/vox`.

## Tests (`stt_test.go`)
Run through `NewWith` with scripted fakes — no ONNX, no PortAudio: full-turn
stream (start/end/text + flag cleared), flag stays set while a turn is open,
batch transcribe, idempotent `Close`. `go test -race` clean.

## Not in scope (deferred until a consumer needs it)
`VoiceFileID` plumbing, an `elevenlabs` provider case, streaming STT, and making
`cmd/vox` consume this facade. Keep the surface minimal (AGENTS.md).

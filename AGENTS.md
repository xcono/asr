# asr

Speech-to-text module (`github.com/xcono/asr`) for the realtime voice agent:
Silero VAD + transcription behind an importable facade
(`stt.New`/`stt.NewWith` → `Events()` / `IsSpeaking()` / `Transcribe()`),
consumed **in-process** by [`xcono/voices`](../voices) over Go channels.
`cmd/vox` is a standalone debug binary that runs the same pipeline and logs
events to stdout.

**No event transport lives here.** Events flow over Go channels only; if
cross-service propagation is ever needed, `xcono/voices` owns it.

Out of scope: LLM, TTS (sibling [`xcono/tts`](../tts)), playback, the
orchestrator turn loop.

## Build & run

```bash
make          # default goal: run cmd/vox (loads config.json from cwd)
make build    # compile to ./bin/vox
make test     # go test ./...
make help     # list documented targets
make up-whisper / down-whisper   # Whisper STT server (docker.whisper.yaml)
```

`vox` takes one flag: `-config <path>` (default `config.json`). VAD diagnostics
are a config field, not a flag: `"vad": {"debug": true}` tees each window's
speech probability to stderr as a meter.

### Native dependencies

- **None.** The module builds with `CGO_ENABLED=0`.
- **Mic capture is pure Go.** `pkg/audio.Capture` speaks the PulseAudio
  protocol over its socket via
  [`jfreymuth/pulse`](https://github.com/jfreymuth/pulse) — no libpulse, no
  PortAudio. Runtime needs a PulseAudio server or PipeWire with its pulse layer
  (`pipewire-pulse`); set `PULSE_SERVER` if it isn't auto-discovered.
- **Silero VAD is pure Go.** `pkg/vad.Model` wraps
  [`zserge/govad`](https://github.com/zserge/govad) with the **v5** weights
  embedded in the binary — no model file, no ONNX Runtime.

## Config

`config.json` (or `-config`). Schema is in `pkg/config/config.go`; `Load` calls
`setDefaults`, so a minimal `{}` file runs. Sections: `vad` and `asr`. Unknown
keys are ignored silently — a misspelt section falls back to defaults.

Provider defaults to `"gigaam"` in code; the checked-in `config.json` selects
`"whisper"`. `gigaam` and `whisper` share `asr.Transcriber` (`language`/`prompt`
are sent only when non-empty); `"elevenlabs"` is `pkg/asr/elevenlabs.go`. All
three are built by `stt.NewRecognizer` — the one provider switch, shared with
`xcono/voices`. `README.md` has the parameter tables.

## Architecture

```
Mic → audio.Capture → asr.Listen → SpeechStart / SpeechEnd / SpeechText
```

- `stt.go` — the facade. `New` owns the mic and the model;
  `NewWith` takes caller-owned components.
- `cmd/vox/main.go` — load config → build model, capture and recogniser → range
  over `asr.Listen` → log. `vaddebug.go` wraps the `Detector` to print the
  probability meter without changing results.
- `pkg/asr` — `Listen` (`listen.go`) is the pipeline controller: it owns the
  FSM, preroll ring and utterance buffer, and transcribes in a goroutine so a
  slow recogniser never stalls capture. The channel closes when the source
  errors or the context is cancelled. Also here: the consumer-side interfaces
  `Source`/`Detector`/`Recognizer`, `Transcriber` (batch, OpenAI-compatible
  multipart WAV upload), `ElevenLabs`, and `StreamSession` (WebSocket streaming).
- `pkg/vad` — `Model`, hysteresis `FSM`, `Preroll` ring, `EndOfTurn` countdown,
  `Timing` and the tuned consts.
- `pkg/audio` — PulseAudio `Capture`, PCM16LE / WAV codec, `ResampleLinear`.
- `pkg/config` — schema + `Load`. `VADConfig.ToTiming()` lives here so all
  config-schema concerns stay in one package; `config` imports `vad`, never the
  reverse.
- `llm/` + `docker.*.yaml` — the external STT servers (whisper.cpp, GigaAM,
  Qwen3-ASR). Whisper's HTTP contract is in `llm/whisper/README.md`.

## Audio format (hard constraint)

Input is **16 kHz / 16-bit / little-endian / mono**. `vad.SampleRate` (16000)
and `vad.Window` (512 samples = 32 ms) are Silero's native contract, not
tunables. Convert anything else before it reaches the pipeline
(`audio.ResampleLinear`, `audio.PCM16LE` / `audio.DecodePCM16LE`); resample
output is float32 [-1,1], not int16.

## Conventions

- Short package names, one concern per directory: `vad`, `asr`, `audio`,
  `config`.
- One noun per file (`listen.go`, `stream.go`, `batch.go`, `capture.go`).
- Errors: `fmt.Errorf("context: %w", err)`, return early, package-prefixed
  messages (`"vad model: %w"`, `"asr: status %d: %s"`).
- Doc comments explain *why*. Several record a bug they prevent (FSM leaky
  counters, preroll sizing, overflow tolerance) — keep that context when editing.
- Interfaces are defined at the consumer (`asr.Source`, `asr.Detector`,
  `asr.Recognizer`) so tests can inject fakes.

## Testing

- Tests sit next to the code, same package (white-box).
- `pkg/asr/batch` and `pkg/config` use `testify`; `pkg/vad`, `pkg/asr/listen`
  and `pkg/audio` use plain `t.Fatalf`/`t.Errorf`. Match the package.
- Pipeline tests avoid the real model and the network: `fakeDetector` returns
  scripted probabilities, `fakeSource` yields N windows then `io.EOF`, batch
  tests use `httptest.NewServer`, streaming tests a `websocket.Upgrader`.
- Drive timing-dependent tests from `Timing.OnsetWindows()` /
  `ReleaseWindows()` / `EndOfTurnWindows()` so they survive const changes.

## Gotchas

- **`Model.Infer` is stateful** (Silero's recurrent state). One `Model` per
  stream; it is not safe for concurrent use.
- **The timing consts are tuned for Silero v5.** A v6 model scores the same
  audio differently — switching generations means re-tuning
  `pkg/vad/consts.go`, not a drop-in swap. Read the comments there before
  changing anything.
- **`vad.Timing` zero value is invalid.** Use `vad.DefaultTiming()` or
  `config.VADConfig.ToTiming()`.
- **`govad` is a small single-author module pinned by pseudo-version**, with no
  releases. If it disappears, vendor it rather than reintroducing cgo.
- **`audio.Capture.Read()` aliases its internal buffer.** The slice is
  overwritten on the next `Read` — copy before retaining it. `Listen` already
  does.
- **The mic is push, `Read` is pull.** The pulse library calls our writer from
  the goroutine that reads the server connection — the same one that delivers
  every request's reply. The callback copies the packet (the library reuses the
  buffer) into a queue and never blocks; `Read` assembles `frameSize` windows
  from that queue.
- **Overflow is counted, not fatal.** When the queue is full the packet is
  dropped and `Capture.Overflows()` counts it; the stream keeps running. Don't
  propagate it. The server itself has no overflow signal for recording.
- **A removed mic is not reported.** The library drops the server's
  stream-killed / suspended / moved messages; only a lost connection surfaces.
  `Read` therefore errors after 2 s without a packet (`readTimeout`) — the
  server streams silence as samples, so a gap that long means the source is
  gone. `Read` returns `io.EOF` after `Close`.
- **A Bluetooth mic starts with about a second of exact zeros** (seen with
  EarFun earbuds; a USB Jabra starts with real signal). Frames arrive on time,
  they are just digital silence while the headset switches profile. No
  special handling — the VAD sees silence.
- **`jfreymuth/pulse` is pinned at v0.1.1**, the version `xcono/tts` (via oto)
  already uses, so `xcono/voices` gets one version for both.
- **All providers are batch.** `asr.StreamSession` / `DialStream` and
  `vad.EndOfTurn` are implemented and tested but not wired into `Listen`.
- **`Event.VoiceFileID` is never set** by the pipeline.
- **ElevenLabs needs `elevenlabs.api_key`**, and `stt.NewRecognizer` does not
  validate it — a missing key fails at request time (401), not at startup.

## Python

Keep Python out of the Go codebase. The STT servers under `llm/` are external
processes reached over HTTP: GigaAM and Qwen3-ASR are Python, Whisper is a
locally built whisper.cpp image (`llm/whisper/Dockerfile`, compiled for sm_120
because the upstream image has no RTX 50xx code).

## Development

Prefer small, reviewed increments. `README.md` is the user-facing reference,
this file the agent-facing one — keep both consistent when behaviour changes.

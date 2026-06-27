# asr

Decoupled speech-to-text module (`github.com/xcono/asr`) for the realtime voice
agent: Silero VAD + transcription behind an importable, NATS-free facade
(`stt.New`/`stt.NewWith` → `Events()` / `IsSpeaking()` / `Transcribe()`),
consumed **in-process** by [`xcono/voices`](../voices). A standalone debug
binary, `cmd/vox`, wraps the same pipeline and emits VAD + STT events over an
embedded NATS JetStream — **NATS lives only there**, never on the library path.

**Source PoC:** `xcono/voices` — the full voice assistant (VAD → ASR → LLM →
TTS). This module extracts the VAD + ASR layer; sibling [`xcono/tts`](../tts) is
the "mouth". Do not copy TTS, LLM, voiceq, act, or audio-playback code here.

Out of scope: LLM, TTS, playback, orchestrator turn loop, Bluetooth headset.

## Build & run

```bash
make deps     # one-shot: PortAudio headers + ONNX Runtime + Silero VAD model
make          # default goal: run  (build + run, loads config.json from cwd)
make build    # compile to ./bin/vox
make test     # go test ./...  (guarded — see ONNX Runtime below)
make env      # print resolved ORT path + model path
make help     # list documented targets
```

`make deps` is idempotent and splits into `deps-portaudio` (`apt install
portaudio19-dev`, needs sudo), `deps-ort` (downloads ONNX Runtime
`ORT_VERSION` — default `1.18.1` — to `~/.local/onnxruntime-linux-x64-<ver>`
and extracts it), and `deps-model` (fetches the Silero VAD v5 ONNX model to
`pkg/vad/silero_vad.onnx`). `deps-model` prefers copying from the go modcache
(the `hylarucoder/silero-vad-onnx-go` fork bundles a byte-identical copy in
its `testfiles/`), falling back to a `wget` from `snakers4/silero-vad`
`SILERO_VERSION` (default `v5.1.2`). Override `ORT_VERSION`, `SILERO_VERSION`,
`ORT`, `MODEL_PATH` on the make command line to retarget.

The binary takes one flag: `-config <path>` (default `config.json`). Config is
read from the working directory by default — run from the repo root so
`pkg/vad/silero_vad.onnx` (the default `model_path`) resolves.

### ONNX Runtime (required to build/run)

`pkg/vad` is cgo. The Makefile exports `C_INCLUDE_PATH`, `LIBRARY_PATH`, and
`LD_RUN_PATH` from `ORT` (default
`~/.local/onnxruntime-linux-x64-1.18.1`). Without these the linker fails with
`ld: cannot find -lonnxruntime`. The `check-ort` guard fires a clear error
before the build if `$(ORT)/lib/libonnxruntime.so` is missing. Override with
`make ORT=/path/to/your/onnxruntime`.

Because `vad` is cgo, `make test` (which runs `go test ./...`) also requires a
working ORT. To run non-cgo package tests in isolation without ORT, target the
specific packages: e.g. `go test ./pkg/asr/... ./pkg/audio/... ./pkg/config/...
./pkg/nats/...`.

### PortAudio (required for mic capture)

`apt install portaudio19-dev`. The `audio.Capture` stream wires PortAudio;
tests in `pkg/audio` use it (only `TestIsOverflow` exercises the real
PortAudio error type).

### Silero model file (not in repo)

`silero_vad.onnx` is gitignored (`*.onnx`). The default `config.json` points at
`pkg/vad/silero_vad.onnx`. Run `make deps-model` to fetch it (copies from the go
modcache — the fork's `testfiles/silero_vad.onnx` — or downloads from
`snakers4/silero-vad` `v5.1.2`). `vad.NewModel` fails without it.

> ⚠️ The fork's cgo bridge pins the Silero **v5** state layout (`stateLen =
> 2*1*128`, `contextLen = 64`). The v6 model changed this shape — do not bump
> `SILERO_VERSION` to a v6 tag without updating the bridge first.

## Config

`config.json` (or the path passed via `-config`). Schema lives in
`pkg/config/config.go`; `Load` calls `setDefaults`, so every zero value has a
sensible fallback and a minimal `{}` file still runs. Provider is `"gigaam"` by
default; `"elevenlabs"` is parsed but **not wired in code** (config placeholder
only). See `README.md` for the full table of VAD parameters and their meaning.

## Architecture / data flow

```
Mic → audio.Capture → asr.Listen ┬─ SpeechStart → NATS vad.speaking.start
                                  ├─ SpeechEnd   → NATS vad.speaking.stop
                                  └─ SpeechText   → NATS stt.message
```

- `cmd/vox/main.go` is the only entry point: load config → start NATS → load
  Silero → init PortAudio → build an `asr.Recognizer` (only `gigaam` switch
  case exists) → range over `asr.Listen`'s event channel → publish each event.
- `asr.Listen` (`pkg/asr/listen.go`) is the pipeline controller and the place
  the four domains meet. It owns the FSM, preroll ring, and utterance buffer,
  and dispatches transcription to a goroutine so a slow recogniser never
  stalls capture. The channel closes when the source errors (EOF/stream error)
  or the context is cancelled.
- `pkg/vad` — Silero model wrapper (`Model`, cgo), hysteresis FSM (`FSM`),
  preroll ring (`Preroll`), end-of-turn countdown (`EndOfTurn`, used by the
  streaming path), and `Timing`/consts. **`Model.Infer` is stateful** (Silero's
  recurrent state) — never call `Reset` between windows of one stream.
- `pkg/asr` — `Source`/`Detector`/`Recognizer` interfaces (so tests inject
  scripted implementations and avoid the ONNX model / network), `Transcriber`
  (batch, OpenAI-compatible multipart WAV upload), `StreamSession` (WebSocket
  streaming; **not wired into the pipeline**, exists for future streaming STT).
- `pkg/audio` — PortAudio `Capture` + PCM16LE / WAV codec + the
  Linux-only `SilenceStderr` trick (see Gotchas).
- `pkg/nats` — embedded NATS server with JetStream. On boot it creates two
  streams: `VAD` (`vad.>`) and `STT` (`stt.>`). Subjects:
  `vad.speaking.start`, `vad.speaking.stop`, `stt.message`.
- `pkg/config` — config schema + `Load`. `VADConfig.ToTiming()` lives here
  (not in `vad`) deliberately, to keep `config` from importing `vad`.

## Audio wire format (hard constraint)

STT/VAD input is **16 kHz / 16-bit / little-endian / mono**. `vad.SampleRate`
(16000) and `vad.Window` (512 samples = 32 ms) are Silero's native contract —
not tunable. Audio coming in at any other rate/width must be converted before
it reaches the pipeline. `audio.ResampleLinear` and `audio.PCM16LE` /
`audio.DecodePCM16LE` exist for this; resample output is float32 [-1,1] (for
VAD-style consumers), not int16.

## Conventions

- Short package names (3–5 letters), one concern per directory: `vad`, `asr`,
  `audio`, `config`, `nats`. (The original PoC used `bus` for the event layer;
  this repo renamed it `nats` — follow the real name here.)
- One noun per file: `listen.go`, `stream.go`, `batch.go`, `capture.go`. Where
  files form a pipeline, name them so **alphabetical order = process order**.
- Error wrapping: `fmt.Errorf("context: %w", err)`. Return early, no deep
  nesting. Package-prefix messages at call sites (e.g. `"nats: publish vad
  start: %v"`, `"asr: status %d: %s"`).
- Doc comments on exported identifiers explain *why*, not *what*. Several
  comments document prior bugs they prevent regressing (FSM leaky counters,
  preroll sizing, overflow tolerance) — preserve that context when editing.
- Interfaces are defined at the consumer side (`asr.Source`, `asr.Detector`,
  `asr.Recognizer`), not the implementation side, so tests can inject fakes.

## Testing patterns

- Tests live next to the code (`*_test.go` in the same package, white-box).
- `pkg/nats` and `pkg/asr/batch` use `testify` (`require`/`assert`).
  `pkg/vad`, `pkg/asr/listen`, `pkg/audio` use plain `t.Fatalf`/`t.Errorf`.
  Match the convention of the package you're editing.
- VAD/ASR tests avoid the ONNX model and the network via fakes:
  - `fakeDetector` (`pkg/asr/listen_test.go`) returns scripted per-window
    probabilities so FSM transitions are deterministic.
  - `fakeSource` yields N dummy windows then `io.EOF`.
  - Batch tests use `httptest.NewServer` to fake the OpenAI-compatible STT
    endpoint; streaming tests upgrade it to a `websocket.Upgrader`.
- `nats.NewServer(0, t.TempDir())` — port `0` for a random free port,
  `t.TempDir()` for an isolated JetStream store dir. Use this pattern, not a
  fixed port, to keep tests parallel-safe.
- When adding VAD timing-dependent tests, drive counts from the
  `Timing.OnsetWindows()`/`ReleaseWindows()`/`EndOfTurnWindows()` helpers so
  they stay correct if consts change.

## Gotchas

- **`audio.Capture.Read()` aliases its internal buffer.** The returned
  `[]int16` is overwritten on the next `Read`. Copy (append / `Preroll.Snapshot`
  / `Pre/append(utter, frame...)`) before the next call if you need to retain
  it. The listen loop already does this correctly.
- **PortAudio writes to raw fd 2, bypassing `os.Stderr`.** During init the
  ALSA/JACK backends spew device-probe warnings straight to the C-level
  stderr. `audio.SilenceStderr()` (in `altstderr_linux.go`, build-tagged
  Linux-only) dups fd 2 to `/dev/null` for that window and restores it after
  the stream is open. Don't try to silence PortAudio via `os.Stderr` redirects
  — they won't catch it.
- **Input overflow is tolerated, not fatal.** `Capture.Read` catches
  `portaudio.InputOverflowed`, counts it, and still returns the (valid)
  buffer. Every other error stays fatal so a real device failure isn't
  swallowed. `Capture.Overflows()` reports the count; `main` logs it at
  shutdown. Don't "fix" this by propagating overflow.
- **`asr.Listen` never sets `Event.VoiceFileID`.** The field exists and is
  plumbed end-to-end (NATS `MessageEvent.voice_file_id`), but the current
  pipeline leaves it empty. Wire it only if a recording/audio-file-id source
  is added.
- **`silero-vad-go` is a fork via `go.mod replace`.** The import path is
  `github.com/streamer45/silero-vad-go`, but `replace` redirects it to
  `github.com/hylarucoder/silero-vad-onnx-go`. Don't "tidy" the replace away.
- **ElevenLabs STT is config-only.** `STTConfig.ElevenLabs` parses, but
  `main.go`'s provider switch has no `"elevenlabs"` case — it will fail with
  `unknown stt provider`. Add the case + a `Recognizer` implementation before
  relying on it.
- **Streaming STT is unused.** `asr.StreamSession` / `DialStream` /
  `WSURLFromHTTP` are implemented and tested but not called by the pipeline.
  `vad.EndOfTurn` (the streaming turn-end signal) is also unused outside its
  tests. The live path is batch only.
- **`vad.Timing` zero value is invalid.** Use `vad.DefaultTiming()` and
  override fields, or build it from `config.VADConfig.ToTiming()` as `main`
  does. The consts in `pkg/vad/consts.go` are the measured defaults — the
  comments explain the tuning tradeoffs; re-read them before changing.

## Python constraint

Goal is to avoid Python in the codebase. GigaAM currently requires an external
Python server exposing an OpenAI-compatible `/v1/audio/transcriptions`
endpoint — that process is an external dependency, not embedded Go code, and is
pointed at via `stt.gigaam.base_url`. If a Go-native ASR alternative becomes
viable, replace the `Transcriber`.

## Development

Built iteratively by AI coding agents. Prefer small, reviewed increments over
large rewrites. `README.md` holds the more detailed, user-facing reference;
this file is the agent-facing compass — keep both consistent when you change
behavior.
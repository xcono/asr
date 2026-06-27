# asr

Decoupled **speech-to-text "ears"** for the realtime voice agent: Silero VAD +
transcription, exposed as an importable, NATS-free Go module
(`github.com/xcono/asr`) consumed in-process by [`xcono/voices`](../voices). A
standalone debug binary, `cmd/vox`, wraps the same pipeline and additionally
emits events over an embedded NATS JetStream.

## Facade (importable — the path voices uses)

```go
svc, err := stt.New(cfg)               // owns the mic + Silero model
svc := stt.NewWith(src, det, rec, t)   // or inject components (voices owns the device)

for ev := range svc.Events() { ... }   // Stream: SpeechStart / SpeechEnd / SpeechText
ok := svc.IsSpeaking()                 // VAD TRUE/FALSE (barge-in signal)
text, err := svc.Transcribe(ctx, pcm)  // Batch one-shot (16 kHz mono PCM)
```

The facade is NATS-free; nothing here starts a server. NATS lives only behind
`cmd/vox` (below).

## Pipeline (the cmd/vox debug path)

```
Mic → audio.Capture → asr.Listen → ┬─ SpeechStart → NATS "vad.speaking.start"
                                    ├─ SpeechEnd   → NATS "vad.speaking.stop"
                                    └─ SpeechText  → NATS "stt.message"
```

`asr.Listen` runs the full pipeline: audio capture → Silero VAD (FSM + preroll) → segment collection → batch transcription. The facade returns these as a channel of events; `cmd/vox` additionally publishes them to NATS JetStream for any subscriber to consume.

## Config

`config.json` in the working directory:

```json
{
  "vad": {
    "model_path": "pkg/vad/silero_vad.onnx",
    "threshold": 0.30,
    "hysteresis": 0.15,
    "barge_in_ms": 64,
    "release_ms": 700,
    "end_of_turn_ms": 800,
    "preroll_ms": 300
  },
  "stt": {
    "provider": "gigaam",
    "gigaam": { "base_url": "http://localhost:8008/v1", "model": "" },
    "elevenlabs": { "api_key": "", "model": "" }
  },
  "nats": {
    "port": 4222,
    "store_dir": "/tmp/nats"
  }
}
```

### VAD parameters

| Parameter | Default | Purpose |
|-----------|---------|---------|
| `threshold` | 0.30 | Speech probability above this → "maybe speaking" |
| `hysteresis` | 0.15 | Release threshold = `threshold - hysteresis` (0.15). Prevents flicker at the boundary |
| `barge_in_ms` | 64 | Consecutive speech windows needed to confirm onset (2 × 32ms windows). Filters out noise bursts |
| `release_ms` | 700 | Consecutive silence windows to confirm offset (batch path). Prevents cutting off mid-pause |
| `end_of_turn_ms` | 800 | Consecutive silence windows to finalize a streaming turn. Slightly longer than release — more conservative for turn commitment |
| `preroll_ms` | 300 | Audio retained before confirmed onset. Prevents clipping the first syllable |

### STT providers

- **gigaam** — local batch STT via an OpenAI-compatible HTTP API (`/v1/audio/transcriptions`). Requires a separate Python server running at `base_url`.
- **elevenlabs** — remote STT (not yet wired in code, config placeholder only).

### NATS settings

- `port` — embedded server listen port (default 4222)
- `store_dir` — JetStream persistence directory

## VAD

Silero VAD model (ONNX, cgo) wrapped in a leaky-counter FSM:

1. **Model** — `vad.Model` loads the ONNX file, `Infer(window)` returns a speech probability per 512-sample (32ms) window.
2. **FSM** — `vad.FSM` tracks `speechRun` / `silenceRun` counters. When `speechRun` reaches `onsetWindows` → `ToSpeaking`. When `silenceRun` reaches `releaseWindow` → `ToSilence`. Hysteresis prevents rapid toggling.
3. **Preroll** — `vad.Preroll` is a ring buffer that continuously records. On `ToSpeaking`, `Snapshot()` returns the last `preroll_ms` of audio so the first syllable isn't lost.
4. **EndOfTurn** — `vad.EndOfTurn` counts consecutive silence windows for the streaming path. Fires when the counter reaches `end_of_turn_ms`.

Audio wire format: **16 kHz / 16-bit / little-endian / mono** (hard constraint from the model).

## STT

Two paths exist in `pkg/asr/`:

- **Batch** — `Transcriber` encodes the full utterance as WAV, POSTs to `{base_url}/audio/transcriptions`. Used by `asr.Listen` after `SpeechEnd`.
- **Streaming** — `StreamSession` opens a WebSocket, sends PCM frames in real-time, receives partial/final transcripts. For future streaming STT providers.

The `asr.Listen` pipeline ties it together:

```go
func Listen(ctx context.Context, src Source, det Detector, rec Recognizer, t vad.Timing) <-chan Event
```

Returns a channel of `Event` (`SpeechStart`, `SpeechEnd`, `SpeechText` with timestamp, text, and voice file ID).

## Events

Three NATS subjects, two JetStream streams:

| Subject | Stream | Event | Fields |
|---------|--------|-------|--------|
| `vad.speaking.start` | VAD | `VADEvent` | `timestamp` |
| `vad.speaking.stop` | VAD | `VADEvent` | `timestamp` |
| `stt.message` | STT | `MessageEvent` | `timestamp`, `text`, `voice_file_id` |

Streams use the `vad.>` and `stt.>` wildcard subjects, so any future subjects under those prefixes are automatically captured.

## Infrastructure

All three native dep groups (PortAudio headers, ONNX Runtime, Silero VAD model) are installable with one command:

```bash
make deps                  # PortAudio + ONNX Runtime + Silero VAD model
# Or individually:
make deps-portaudio       # sudo apt install portaudio19-dev (Debian/Ubuntu)
make deps-ort              # download + extract ONNX Runtime to ~/.local/
make deps-model            # fetch the Silero VAD v5 ONNX model to pkg/vad/
```

`ORT_VERSION` (default `1.18.1`) and `SILERO_VERSION` (default `v5.1.2`) are overridable on the make command line.

### ONNX Runtime

The VAD package uses cgo. Set `ORT` to your ONNX Runtime install:

```bash
export ORT=~/.local/onnxruntime-linux-x64-1.18.1
# Or: make ORT=/path/to/it  (or  make ORT_VERSION=1.18.1 deps-ort)
```

The Makefile sets `C_INCLUDE_PATH`, `LIBRARY_PATH`, and `LD_RUN_PATH` from `ORT`. Without these the linker fails (`cannot find -lonnxruntime`).

### Silero VAD model

The ONNX model ships from `snakers4/silero-vad` ([v5.1.2 tag](https://github.com/snakers4/silero-vad/raw/v5.1.2/src/silero_vad/data/silero_vad.onnx)); it is gitignored (`*.onnx`) and not committed. `make deps-model` first tries to copy it from the go modcache (the `hylarucoder/silero-vad-onnx-go` fork bundles a byte-identical copy in its `testfiles/`), falling back to a direct download from upstream if the modcache is unavailable.

> ⚠️ The fork's cgo bridge pins the Silero **v5** state layout (`stateLen = 2*1*128`, `contextLen = 64`). The v6 model changed this shape — do not bump `SILERO_VERSION` to a v6 tag without updating the bridge first.

### PortAudio (required for mic capture)

```bash
make deps-portaudio      # or:  apt install portaudio19-dev
```

### GigaAM STT server (external dependency)

GigaAM requires a Python server exposing an OpenAI-compatible `/v1/audio/transcriptions` endpoint. This is an external process, not embedded Go code. Point `stt.gigaam.base_url` in config at it.

### NATS server

Embedded in-process — no separate binary needed. The server starts with JetStream enabled and creates the `VAD` and `STT` streams on boot. Data persists to `nats.store_dir`.

### NATS client example (Go)

```go
package main

import (
    "fmt"
    "log"
    "time"

    "github.com/nats-io/nats.go"
)

func main() {
    nc, err := nats.Connect("nats://localhost:4222")
    if err != nil {
        log.Fatal(err)
    }
    defer nc.Close()

    js, err := nc.JetStream()
    if err != nil {
        log.Fatal(err)
    }

    // Subscribe to transcription messages
    sub, err := js.Subscribe("stt.message", func(msg *nats.Msg) {
        fmt.Printf("[%s] %s\n", msg.Subject, string(msg.Data))
        msg.Ack()
    }, nats.DeliverAll())
    if err != nil {
        log.Fatal(err)
    }
    defer sub.Unsubscribe()

    // Or: subscribe to VAD events
    sub2, err := js.Subscribe("vad.>", func(msg *nats.Msg) {
        fmt.Printf("[%s] %s\n", msg.Subject, string(msg.Data))
        msg.Ack()
    })
    if err != nil {
        log.Fatal(err)
    }
    defer sub2.Unsubscribe()

    // Keep running
    select {}
}
```

## Build & Run

```bash
make          # build → ./bin/vox
make run      # build + run (loads config.json from cwd)
make test     # run tests
make env      # print resolved ORT path
```
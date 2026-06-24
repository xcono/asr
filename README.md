# vox

Standalone ASR/STT Go service for a realtime voice agent. Runs VAD + transcription and emits events over an embedded NATS JetStream.

## Architecture

```
Mic → audio.Capture → asr.Listen → ┬─ SpeechStart → NATS "vox.vad.speaking.start"
                                    ├─ SpeechEnd   → NATS "vox.vad.speaking.stop"
                                    └─ SpeechText → NATS "vox.stt.message"
```

`asr.Listen` runs the full pipeline: audio capture → Silero VAD (FSM + preroll) → segment collection → batch transcription. Events are published to NATS JetStream for any subscriber to consume.

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
| `vox.vad.speaking.start` | VAD | `VADEvent` | `timestamp` |
| `vox.vad.speaking.stop` | VAD | `VADEvent` | `timestamp` |
| `vox.stt.message` | STT | `MessageEvent` | `timestamp`, `text`, `voice_file_id` |

Streams use the `vox.vad.>` and `vox.stt.>` wildcard subjects, so any future subjects under those prefixes are automatically captured.

## Infrastructure

### ONNX Runtime (required)

The VAD package uses cgo. Set `ORT` to your ONNX Runtime install:

```bash
export ORT=~/.local/onnxruntime-linux-x64-1.18.1
# Or: make ORT=/path/to/it
```

The Makefile sets `C_INCLUDE_PATH`, `LIBRARY_PATH`, and `LD_RUN_PATH` from `ORT`. Without these the linker fails (`cannot find -lonnxruntime`).

### PortAudio (required for mic capture)

```bash
apt install portaudio19-dev
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
    sub, err := js.Subscribe("vox.stt.message", func(msg *nats.Msg) {
        fmt.Printf("[%s] %s\n", msg.Subject, string(msg.Data))
        msg.Ack()
    }, nats.DeliverAll())
    if err != nil {
        log.Fatal(err)
    }
    defer sub.Unsubscribe()

    // Or: subscribe to VAD events
    sub2, err := js.Subscribe("vox.vad.>", func(msg *nats.Msg) {
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
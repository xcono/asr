# asr

Speech-to-text "ears" for the realtime voice agent: Silero VAD + transcription
as an importable Go module (`github.com/xcono/asr`), consumed in-process by
[`xcono/voices`](../voices) over Go channels. `cmd/vox` is a standalone debug
binary that runs the same pipeline and logs events to stdout.

The module carries **no event transport**: events flow over Go channels only.

## Usage

```go
import "github.com/xcono/asr" // package stt

svc, err := stt.New(cfg)               // owns the mic + Silero model
svc := stt.NewWith(src, det, rec, t)   // or inject components (caller owns them)

for ev := range svc.Events() { ... }   // SpeechStart / SpeechEnd / SpeechText / SpeechError
ok := svc.IsSpeaking()                 // VAD state (barge-in signal)
text, err := svc.Transcribe(ctx, pcm)  // one-shot batch (16 kHz mono PCM)
```

| Event | Fields |
|-------|--------|
| `SpeechStart` | `Timestamp` |
| `SpeechEnd` | `Timestamp` |
| `SpeechText` | `Timestamp`, `Text` |
| `SpeechError` | `Err` |

## Pipeline

```
Mic → audio.Capture → asr.Listen → SpeechStart / SpeechEnd / SpeechText
```

`asr.Listen` runs capture → Silero VAD (FSM + preroll) → utterance collection →
batch transcription, and returns a channel of events.

Audio format is fixed: **16 kHz / 16-bit / little-endian / mono**, in
512-sample (32 ms) windows.

## Build & run

```bash
make deps     # PortAudio headers (sudo apt install portaudio19-dev)
make          # run cmd/vox (loads config.json from the working directory)
make build    # compile to ./bin/vox
make test     # go test ./...
make help     # list all targets
```

PortAudio (mic capture) is the only native dependency. The Silero VAD is pure
Go ([`zserge/govad`](https://github.com/zserge/govad)) with the v5 weights
embedded in the binary — no model file, no ONNX Runtime.

## Config

`config.json` in the working directory, or `vox -config <path>`. Every field
has a default, so `{}` is a valid config.

```json
{
  "vad": {
    "threshold": 0.30,
    "hysteresis": 0.15,
    "barge_in_ms": 64,
    "release_ms": 700,
    "end_of_turn_ms": 800,
    "preroll_ms": 300,
    "debug": false
  },
  "asr": {
    "provider": "whisper",
    "gigaam": { "base_url": "http://localhost:8008/v1", "model": "" },
    "whisper": { "base_url": "http://localhost:8008/v1", "model": "", "language": "ru", "prompt": "" },
    "elevenlabs": { "api_key": "", "model": "", "language_code": "", "base_url": "" }
  }
}
```

### VAD

| Parameter | Default | Purpose |
|-----------|---------|---------|
| `threshold` | 0.30 | Speech probability at or above this counts as speech |
| `hysteresis` | 0.15 | Release gate = `threshold - hysteresis`; prevents flicker at the boundary |
| `barge_in_ms` | 64 | Speech evidence needed to confirm onset (2 windows); filters noise bursts |
| `release_ms` | 700 | Silence needed to end the utterance; prevents cutting off mid-pause |
| `end_of_turn_ms` | 800 | Silence needed to finalize a streaming turn (streaming path, not wired in) |
| `preroll_ms` | 300 | Audio kept from before the confirmed onset, so the first syllable isn't clipped |
| `debug` | false | Print each window's speech probability to stderr as a meter |

The defaults are tuned for Silero **v5** probabilities; a different model
generation needs re-tuning.

### STT providers

All providers are batch: each VAD-segmented utterance is sent as one request.

- **gigaam** (code default) — OpenAI-compatible `/v1/audio/transcriptions` at `base_url`.
- **whisper** — the same client against the whisper.cpp server. `language`
  (default `ru`; `auto` to detect — with a fixed language whisper.cpp translates
  other languages instead of transcribing them) and `prompt` (decoder context
  that steers spelling of terms) are sent only when non-empty.
- **elevenlabs** — cloud ElevenLabs Scribe (`/v1/speech-to-text`, default model
  `scribe_v2`). Needs `api_key`; `language_code` is an optional hint.

## STT servers

The STT model runs as a separate server on `:8008`. Three interchangeable
backends ship as standalone compose files — run exactly one. They need the GPU
box and the NVIDIA Container Toolkit.

| Backend | Compose file | Dir | Mode | Command |
|---|---|---|---|---|
| Whisper large-v3-turbo (whisper.cpp) | `docker.whisper.yaml` | `llm/whisper` | batch, multilingual | `make up-whisper` / `make down-whisper` |
| GigaAM v3 | `docker.giga.yaml` | `llm/giga3` | batch, Russian-only | `make up` / `make down` |
| Qwen3-ASR-1.7B | `docker.qwen.yaml` | `llm/qwen3` | streaming (WS `/v1/asr/stream`), multilingual | `make up-qwen` / `make down` |

`xcono/voices` picks the backend through its own compose
(`../asr/docker.${ASR_BACKEND:-giga}.yaml`). The whisper.cpp HTTP contract is
described in [`llm/whisper/README.md`](llm/whisper/README.md).

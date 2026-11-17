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
make          # run cmd/vox (loads config.json from the working directory)
make build    # compile to ./bin/vox
make test     # go test ./...
make help     # list all targets
```

No native dependencies: the module builds with `CGO_ENABLED=0`. Mic capture
talks to PulseAudio (or PipeWire's pulse layer) in pure Go via
[`jfreymuth/pulse`](https://github.com/jfreymuth/pulse); it needs a running
PulseAudio/`pipewire-pulse` server, nothing to install at build time. The
Silero VAD is pure Go ([`zserge/govad`](https://github.com/zserge/govad)) with the v5 weights
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
    "max_chars_per_sec": 30,
    "dump_dropped_dir": "dropped",
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

| Parameter | Default | Purpose |
|-----------|---------|---------|
| `provider` | `gigaam` | `gigaam`, `whisper` or `elevenlabs` |
| `max_chars_per_sec` | 30 | Drop a live transcription faster than this many characters per second of submitted audio (preroll included); `0` disables |
| `dump_dropped_dir` | `""` | Save each dropped clip here as a 16 kHz mono 16-bit WAV (created if missing; relative to the working directory); `""` disables. The checked-in `config.json` uses `dropped/`, which git ignores |

The rate filter targets Whisper's hallucinated subtitle credits on short
unclear sounds (~44 chars/s observed) while real speech runs at 15–18 chars/s
and a lone "Да." at 4–9. A dropped turn is logged as

```
asr: dropped "<text>": 1.18s, 39.7 chars/s > 30 vad_peak=0.62 speech_win=6/37 no_speech=0.612 logprob=-1.20 wav=dropped/dropped-20261118-034512.345.wav
```

and produces `SpeechEnd` with no `SpeechText`. `wav=` appears only when
`dump_dropped_dir` is set and the write succeeded (a failure is logged as
`asr: dump dropped clip: <err>` and the pipeline carries on). The time in
the file name is taken just before the line is logged, so it matches the log
timestamp. Listen to these clips to learn what opened the segment. `stt.New` and `vox` apply the filter and the dump;
`stt.NewWith` does each only when passed `stt.WithMaxCharsPerSec(...)` /
`stt.WithDumpDropped(...)`; the one-shot `Service.Transcribe` never filters.

`vad_peak` is Silero's highest window probability in the clip and
`speech_win` the windows at or above `threshold` out of all its 32 ms windows
(preroll included). A short non-speech sound that opened a segment shows a few
speech windows out of ~30; speech fills most of the clip. The same two fields
end `vox`'s `vad: speaking stopped at ...` line, so turns with an empty or
failed transcription show them too.

For `whisper` the client requests `response_format=verbose_json` (with
`no_language_probabilities=true`) and `vox` logs each turn as

```
stt: "<text>" 1.23s 8.1 chars/s vad_peak=0.97 speech_win=28/39 no_speech=0.012 logprob=-0.31 (voice_file_id=)
```

with one comma-separated `no_speech` / `logprob` value per segment. The scores
are only logged — nothing is dropped by them yet. GigaAM stays on plain `json`
(its server rejects other formats) and ElevenLabs returns no scores; their
lines omit both fields.

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

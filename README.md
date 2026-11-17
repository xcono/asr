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
    "max_chars_per_sec": 25,
    "chars_slack": 12,
    "dump_dropped_dir": "dropped",
    "record_dir": "recordings",
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
| `max_chars_per_sec` | 25 | Rate of the letter budget: letters allowed per second of clip beyond the preroll + release pause; `0` turns the filter off |
| `chars_slack` | 12 | Letters the budget allows whatever the clip length |
| `dump_dropped_dir` | `""` | Save each dropped clip here as a 16 kHz mono 16-bit WAV (created if missing; relative to the working directory); `""` disables. The checked-in `config.json` uses `dropped/`, which git ignores |
| `record_dir` | `""` | Record the whole mic stream of each run here as one `session-<time>.wav` (16 kHz mono 16-bit); `""` disables. No size limit or rotation (~115 MB/hour). The checked-in `config.json` uses `recordings/`, which git ignores |

#### Letter budget

The filter targets Whisper's hallucinations on short unclear sounds —
subtitle credits and sign-offs from its training data. A transcription is
dropped when its letters and digits (punctuation and spaces don't count)
exceed

```
chars_slack + max_chars_per_sec × max(0, clip − (preroll_ms + release_ms))
```

Every clip carries about 1 s that is not speech — the preroll before the
onset and the release tail of silence — so a plain rate over the whole clip
is diluted on short clips: "Продолжение следует" in a 1.06 s clip is only
20.8 chars/s, below any cap that fast connected speech (15–18/s) clears. The
budget charges the rate only for the clip beyond that pause and gives a fixed
slack for short answers:

| Text | Clip | Letters | Budget | |
|------|------|---------|--------|---|
| Продолжение следует | 1.06 s | 18 | 13.5 | dropped |
| Редактор субтитров А.Семкин Корректор А.Егорова | 1.18 s | 41 | 16.5 | dropped |
| Я не знаю. | 1.15 s | 7 | 15.75 | kept |
| Не знаю... | 1.44 s | 6 | 23 | kept |
| Обычная речь 3-4 фразы … (a long sentence) | 6.72 s | 62 | 155 | kept |

A dropped turn is logged as

```
asr: dropped "<text>": at=12.48s 1.06s, 18 letters > budget 13.5, 20.8 chars/s vad_peak=0.62 speech_win=6/37 no_speech=0.612 logprob=-1.20 wav=dropped/dropped-20261118-034512.345.wav
```

and produces `SpeechEnd` with no `SpeechText`. `wav=` appears only when
`dump_dropped_dir` is set and the write succeeded (a failure is logged as
`asr: dump dropped clip: <err>` and the pipeline carries on). The time in
the file name is taken just before the line is logged, so it matches the log
timestamp. Listen to these clips to learn what opened the segment. `stt.New` and `vox` apply the filter and the dump;
`stt.NewWith` does each only when passed `stt.WithMaxCharsPerSec(...)` /
`stt.WithDumpDropped(...)` (its slack is 12 unless `stt.WithCharsSlack(...)`
is passed too); the one-shot `Service.Transcribe` never filters.

`vad_peak` is Silero's highest window probability in the clip and
`speech_win` the windows at or above `threshold` out of all its 32 ms windows
(preroll included). A short non-speech sound that opened a segment shows a few
speech windows out of ~30; speech fills most of the clip. The same two fields
end `vox`'s `vad: speaking stopped at ...` line, so turns with an empty or
failed transcription show them too.

#### Session recording

With `record_dir` set, `vox` and `stt.New` write everything the mic delivers
into one WAV per run and print its path at startup
(`recording session -> recordings/session-20261118-040412.123.wav`; `stt.New`
logs `stt: recording session to ...`). Every `vad:`, `stt:` and
`asr: dropped` line carries `at=` — the event's position in that file in
seconds, counted from the samples read rather than the clock:

```
vad: speaking started at 2026-11-18T04:04:25.101+07:00 at=12.48s
vad: speaking stopped at 2026-11-18T04:04:26.420+07:00 at=13.79s vad_peak=0.97 speech_win=28/39
```

`at=` on `speaking started` is the end of the window that confirmed the
onset (speech began up to `barge_in_ms` plus the preroll earlier); on
`speaking stopped`, `stt:` and `asr: dropped` it is the end of the clip.
Recording never delays capture: windows are copied and queued to a writer
goroutine; if it falls ~8 s behind, windows are dropped from the queue and
written as silence of the same length (logged as `asr: record: writer fell
behind ...`), so `at=` stays aligned. The WAV header is finalised when `vox`
exits through Ctrl-C/SIGTERM; a file from a crash keeps placeholder lengths
that ffmpeg, sox and `audio.DecodeWAV` read up to the end of the file.

For `whisper` the client requests `response_format=verbose_json` (with
`no_language_probabilities=true`) and `vox` logs each turn as

```
stt: "<text>" at=13.79s 1.23s 8 letters <= budget 17.8 6.5 chars/s vad_peak=0.97 speech_win=28/39 no_speech=0.012 logprob=-0.31 (voice_file_id=)
```

with one comma-separated `no_speech` / `logprob` value per segment (with
`max_chars_per_sec: 0` the budget reads `+Inf`). The scores
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

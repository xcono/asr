# vox

Standalone ASR/STT Go service for a realtime voice agent. Emits VAD and
transcription events over NATS Jetstream (embedded).

**Source PoC:** `/home/xcono/src/github.com/xcono/voices` — copy and adapt
packages from there. The PoC is a full voice assistant (VAD → ASR → LLM → TTS);
this project extracts just the VAD + ASR + event-bus layer and adds NATS
persistence. Do not copy TTS, LLM, voiceq, act, or audio-playback code.

## Scope

- **VAD** — Silero VAD (ONNX, cgo) → `StartSpeaking` / `StopSpeaking` events
- **ASR/STT** — GigaAM (local, batch) and ElevenLabs (remote) → `Message`
  events (timestamp, text, voice file ID)
- **NATS Jetstream** — embedded, persists events and allows subscribers

Out of scope: LLM, TTS, playback, orchestrator turn loop, Bluetooth headset.

## Build requirements

- **ONNX Runtime** — `pkg/vad` is cgo. Set `ORT` (default:
  `~/.local/onnxruntime-linux-x64-1.18.1`). The Makefile in `voices/` shows the
  env vars: `C_INCLUDE_PATH`, `LIBRARY_PATH`, `LD_RUN_PATH`. Without these the
  linker fails (`ld: cannot find -lonnxruntime`).
- **PortAudio** — `apt install portaudio19-dev` (needed if copying audio capture
  from the PoC).

## Config

`config.json` at working directory (differs from the PoC, which uses env +
flags). Fields:

- VAD barge-in / release delay durations
- STT provider config (JSON structure predefined per provider)
- NATS settings

## Conventions (from the PoC — carry these over)

- **Short package names** (3–4 letters): `vad`, `asr`, `bus`. One concern per
  directory.
- **One noun per file**: `listen.go`, `stream.go`, `batch.go`. Where files form
  a pipeline, name them so **alphabetical order = process order**.
- **Error wrapping**: `fmt.Errorf("context: %w", err)`. Return early, no deep
  nesting.
- **Audio wire format**: STT input is 16 kHz / 16-bit / LE / mono (hard
  constraint from the PoC).

## Python constraint

Goal is to avoid Python in the codebase. GigaAM currently requires a Python
server — that Python process is an external dependency, not embedded code. If a
Go-native ASR alternative becomes viable, replace it.

## Development

Built iteratively by AI coding agents. Prefer small, reviewed increments over
large rewrites.
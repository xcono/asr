# VAD → ASR integration (Qwen3-ASR)

> **V4 current.** The ASR server now lives in `llm/qwen3-asr/` (this repo). Pipecat is retired —
> ignore `pipecat/...` paths below; the protocol (batch `/v1/audio/transcriptions`, streaming
> `/v1/asr/stream`) is current.
>
> **As of 2026-06-21, Qwen3-ASR is no longer the default `stt`.** The default batch STT is now
> **GigaAM** (`make up`, `:8008`) — see [`asr-russian.md`](./asr-russian.md). This doc covers
> Qwen3-ASR, which is now the **streaming + multilingual** path (`make up-qwen`,
> `docker.qwen.yaml`); the protocol details here remain accurate for it.

Status: **Milestone 1 (batch) implemented** on 2026-06-16. Milestone 2
(streaming, session-per-turn) is now also implemented — see
[`asr-streaming.md`](./asr-streaming.md). This document is the source of truth
for how `run/loop.go` talks to the local Qwen3-ASR server, and why.

## TL;DR / decision

We do **not** need vLLM streaming for the goal scenario. Silero VAD already
solves the hard part of streaming ASR — endpointing (knowing when an utterance
starts/ends). Given that, the simplest correct design is **VAD-segmented
batch**: buffer PCM between the `Speaking`→`Silence` flips, POST the clip to the
already-running `/v1/audio/transcriptions`, print the line.

Decisive data point: a 5.0 s Russian clip (`vad/ref_ru.wav`) transcribes in
**~0.2 s** (RTFx ≈ 25×), accurate text, auto-resampled from 24 kHz. For
VAD-bounded turns the user finishes speaking and the line appears effectively
instantly. Streaming only buys *partials mid-utterance* — a "live dictation"
feel — at real complexity cost.

Two milestones:

1. **Batch (done):** stdlib `net/http` + `mime/multipart`, **zero new deps**.
   One transcript line per utterance.
2. **Streaming (future):** the server's custom `WS /v1/asr/stream`, using
   `gorilla/websocket`. Live partials overwritten in place.

## What's actually running (the important nuance)

`docker ps` shows `llms-qwen3-asr-1` on **:8008**, but it is **not** `vllm
serve`. It's a custom **FastAPI app** (`pipecat/llms/llm/qwen3-asr/asr_server.py`)
holding one in-process `qwen_asr.Qwen3ASRModel.LLM` (which uses `vllm.LLM` under
the hood). This matters because **none of vLLM's native HTTP endpoints are
exposed** — only what that file defines.

- Model: `Qwen/Qwen3-ASR-0.6B`, bf16, vLLM **v0.14.0**, `enforce_eager` (no CUDA
  graphs), `max_model_len=2048`, `gpu_mem_util≈0.30`.
- Hardware: RTX 5060 Ti (16 GB, Blackwell / sm_120), GPU 0 shared with qwen3-tts.
- Qwen3-ASR is **not** an in-tree vLLM model — it's registered via the
  `qwen-asr[vllm]` PyPI package, whose high-level API provides the
  chunked-streaming state machine the WS endpoint drives.
- One global model lock → single-user MVP; calls are serialized.

### Exposed surface (verified live)

| Endpoint | Type | Contract |
|---|---|---|
| `POST /v1/audio/transcriptions` | **batch, non-streaming** | multipart `file`/`model`/`language`/`response_format`; libsndfile decodes the upload (needs a real container, e.g. WAV — **not** headerless PCM); returns `{"text":…, "usage":{"type":"duration","seconds":N}}` |
| `WS /v1/asr/stream` | **custom streaming** | binary = raw 16-bit LE PCM @ 16 kHz mono → `{"type":"partial","text":<cumulative>,"language":…}` per frame; text `{"type":"finish"}` → `{"type":"final","text":…}` then `close(1000)` |
| `GET /health`, `GET /v1/models` | meta | health = `{"status":"ok","model_loaded":true}` |

Note: `openapi.json` lists only the three HTTP paths — WebSocket routes never
appear in OpenAPI; the WS endpoint is live regardless.

## Empirical latency

```
POST ref_ru.wav (24 kHz, 5.0 s) → /v1/audio/transcriptions
→ {"text":"Привет, этот тестовый запись для синтеза речи на русском языке.", …}
curl wall-clock: ~0.23 s   |   via the Go client (TestTranscribeLiveServer): ~0.18 s
```

Reference text is "Привет, это тестовая запись…" — content correct, two minor
inflection slips. The server auto-resamples any input rate to 16 kHz internally;
the VAD already captures 16 kHz mono int16, so no resampling on the Go side for
the live mic path.

## OpenAI SDK for Go (`github.com/openai/openai-go`)

- **Batch works** *if* you want the SDK: `client.Audio.Transcriptions.New(ctx,
  …)` with `option.WithBaseURL("http://localhost:8008/v1")`. The server's JSON
  shape (`text` + duration `usage`) matches what the SDK expects.
- **Streaming via the SDK does NOT work against this server.** The SDK's
  streaming is SSE-based (`ssestream.Stream`). Audio-transcription streaming,
  where the SDK supports it, expects the server to emit OpenAI SSE
  `transcription.text.delta` events. This server returns a single JSON body, not
  SSE — and the SDK cannot speak the custom WS protocol at all.
- **Decision:** don't pull in the SDK. For one multipart POST returning
  `{"text":…}`, the full `openai-go` dependency is overkill — stdlib
  `net/http` + `mime/multipart` is ~30 lines and zero deps (see `pkg/asr/batch.go`).

## vLLM streaming vs OpenAI streaming compatibility

What the ecosystem offers vs. what *this* server exposes:

| Streaming wire format | Where it lives | Exposed here? |
|---|---|---|
| OpenAI SSE transcription (`stream=true` → `transcription.text.delta`) | OpenAI API; vLLM's `vllm serve` `/v1/audio/transcriptions` | **No** |
| vLLM Realtime WS (`/v1/realtime`: `input_audio_buffer.append`/`commit` → `transcription.delta`/`done`) | recent `vllm serve` | **No** |
| **Custom Qwen3-ASR WS** (`/v1/asr/stream`: binary PCM → cumulative `partial` → `final`) | `asr_server.py` only | **Yes** |

So: this deployment is wire-compatible with OpenAI only on the **batch** path.
For streaming it speaks **neither** OpenAI's nor vLLM's native protocol — it has
its own minimal one. Pipecat made the same split: `pipecat/server/src/cobrain/
stt_stream.py` is a hand-written client for the custom WS (explicitly *"NOT
OpenAI Realtime"*); batch goes through an OpenAI-compatible shim. Strong
corroboration that the right move is batch-reuse + a bespoke WS, not a streaming
SDK.

## Streaming vs "chunk before the LLM" — the core comparison

| | **A. VAD-segmented batch** *(chunk before LLM)* | **B. Custom WS streaming** |
|---|---|---|
| Endpoint | `POST /v1/audio/transcriptions` | `WS /v1/asr/stream` |
| New Go deps | none (stdlib) | `gorilla/websocket` (direct dep) |
| UX | one line per finished utterance | live partials, overwritten as you speak |
| Latency | ~0.2–0.5 s **after** silence (whole-utterance reprocess, cheap on 0.6B) | first partial ~`STREAM_CHUNK_SEC` (1.0 s) after onset |
| Complexity | trivial: buffer → WAV-wrap → POST → print | WS connect per utterance, parse cumulative partials (tail is *revisable* — `unfixed` tokens), handle reconnect/`close(1000)` |
| Failure modes | one HTTP call | socket lifecycle, partial revisions, in-place redraw |

**Why batch is the right default:** the usual reason to stream ASR is to overlap
recognition with speech and to get endpointing for free from the server. We
already have *superior* endpointing locally (Silero + the hysteresis FSM in
`pkg/vad/fsm.go`), and the model is so small that reprocessing the whole utterance
at silence costs ~0.2 s. Streaming's benefit collapses to "partials during long
utterances" — worth it for dictation, unnecessary for turn-based interaction.
Note `STREAM_CHUNK_SEC=1.0` means the first partial lands ~1 s in, so for short
turns streaming isn't even faster end-to-end than batch.

## Milestone 1 — as built

Files (after the structural refactor):

- `pkg/asr/batch.go`
  - `Transcriber{BaseURL, Model, Client}` + `Transcribe(ctx, samples, rate)` —
    multipart POST to `{BaseURL}/audio/transcriptions`, returns the `text` field.
- `pkg/audio/wav.go`
  - `EncodeWAV(samples, rate) []byte` — wraps int16 PCM in a 44-byte WAV header.
    Required because the server decodes uploads with libsndfile and won't read
    headerless PCM.
- `pkg/vad/preroll.go`
  - `Preroll` ring — retains the last `onsetWindows*window` samples so seeding a
    new utterance recovers the lead-in the VAD swallows before it confirms speech
    (~`bargeInMs`), preventing clipped word onsets.
- `run/loop.go` — capture loop: pushes each window into `Preroll`; on `Speaking`
  seeds the utterance buffer from the pre-roll snapshot and starts accumulating;
  on `Silence` hands the buffered clip to `Transcribe` on a goroutine (so the mic
  never stalls during the HTTP call) and prints `» <text>`.
- Flag: `-asr` (default `http://localhost:8008/v1`); `-asr ""` disables ASR and
  restores the original pure-VAD `Speaking…/Silence…` output.

Tests live in `pkg/asr/` and `pkg/vad/`: WAV round-trip, `Transcriber`
HTTP client (against an `httptest` server), pre-roll semantics, and
`TestTranscribeLiveServer` (real `localhost:8008`, skipped when unreachable).

Run:

```sh
make run          # batch ASR (default)
make run-stream   # streaming ASR
make run-novad    # mic only, no STT
```

## Milestone 2 — streaming (implemented)

Built as session-per-turn streaming over `WS /v1/asr/stream`: keep one session
open for a whole turn so the model retains cross-pause context, finalising only
after a long end-of-turn silence. This fixes the fragmentation/context loss the
per-segment batch path suffers. Full design, mechanics, constraints, and the
1.7B + VRAM server tuning are in [`asr-streaming.md`](./asr-streaming.md).

# ASR streaming & context (Milestone 2)

> **V4 current.** Streaming ASR is a standalone backend compose (`docker.qwen.yaml`, in this
> repo — `make up-qwen`); GigaAM batch (`docker.giga.yaml`) is default. Server in `llm/qwen3/`;
> pipecat retired.
>
> **As of 2026-06-21** the default batch STT is **GigaAM** (ru-only, batch-only — see
> [`asr-russian.md`](./asr-russian.md)). This streaming overlay swaps in **Qwen3-ASR** (1.7B,
> `ASR_MODEL` in the compose), which is also the multilingual/English path; the "bump to 1.7B"
> section below is obsolete.

Status: **streaming implemented** in `vad/` on 2026-06-16 (session-per-turn over
the custom WebSocket). Context-keyword biasing is **deferred** (needs a server
change). Companion to [`asr-integration.md`](./asr-integration.md), which covers
the batch path and the server contract.

## The problem this solves

The batch path (Milestone 1) sends one clip per VAD segment. But the VAD's
release gap (`releaseMs=700`) declares silence on long *within-sentence* pauses, so a
single spoken sentence becomes several **independent** ASR calls — each with no
acoustic or text history. The model re-guesses sentence boundaries, casing, and
context-dependent words on every fragment and the errors compound. The fix is to
stop treating each pause as a hard boundary.

## What the model actually supports (verified in the running container)

Introspected from `qwen_asr` (the package the server loads):

**1. Context = a system prompt.** Both entry points take it:
`transcribe(audio, context='', language=None, …)` and
`init_streaming_state(context='', language=None, unfixed_chunk_num=2, unfixed_token_num=5, chunk_size_sec=2.0)`.
It's injected as `{"role":"system","content": context}`. Good for domain
keywords / names, or prior transcript. **Our server does not plumb it through**
yet (deferred) — `asr_server.py` calls both with no `context`.

**2. Streaming maintains context *internally* within a session.** Per
`streaming_transcribe`:
- each chunk **re-feeds all audio seen so far** (`audio_accum`, no padding) →
  full acoustic context;
- prompt = `prompt_raw + prefix`, where `prefix` = previously-decoded text minus
  the last `unfixed_token_num` tokens (the revisable tail); the first
  `unfixed_chunk_num` chunks use no prefix.

So a streaming **session** grows one coherent, self-consistent transcript
instead of N cold guesses. That is the cure for fragmentation — and it needs
**no server change**, because `asr_server.py` already keeps one streaming state
per WebSocket *connection*. Keeping the socket open across short pauses = one
context-rich session.

## The Qwen team's official tooling (also in-container)

- `qwen-asr-serve` (`cli/serve.py`) is literally **`vllm serve`** with the model
  registered — vanilla vLLM. Your team replaced it with the in-process custom
  server to get this streaming API at v0.14.0.
- `qwen-asr-demo-streaming` (`cli/demo_streaming.py`) is the reference streaming
  server: a session protocol **`/api/start` (init) → `/api/chunk` (feed PCM) →
  `/api/finish`**. Your `WS /v1/asr/stream` is a cleaner equivalent (one
  persistent socket vs. chunk-POSTs); same session model. Their demo defaults to
  **1.7B**.
- `Qwen3-ASR-Toolkit` is a different tool: **cloud DashScope**, offline long
  files, VAD-chunk at silences (default 120 s targets — i.e. *don't* fragment),
  `--context` = keyword guidance, plus post-processing to strip
  repetitions/hallucinations. Not for local realtime, but the lessons transfer:
  segment at natural silences with long targets, and dedup the tail.

## Constraints to design around

- **`max_model_len` caps a streaming turn's length.** Because every chunk
  re-feeds all accumulated audio, prompt+audio tokens grow with the turn.
  2048 ≈ tens of seconds; we raised it (see server section). This is a **VRAM
  config, not a model limit** — the text backbone is `max_position_embeddings=65536`.
- **Re-feed-all-audio cost grows over a turn** (≈ quadratic). Short turns:
  negligible. vLLM **prefix caching (on by default)** reuses the unchanged
  prompt+audio prefix across chunks, softening this.
- **Streaming is vLLM-backend only** (we are ✅) and has **no timestamps**.
- Effective chunk is **`chunk_size_sec=1.0`** (server default) → first partial
  ~1 s in; lower = snappier but more re-feed decodes.
- **Accuracy vs. batch on a *single clean* clip:** streaming can be slightly
  *worse* (incremental decode + token rollback commit early). Measured on
  `ref_ru.wav`: batch → "тестовый запись", streaming (0.6B) → "тестовый забыть".
  Streaming's win is multi-pause *turns*, not single clips; 1.7B + chunk tuning
  closes the single-clip gap.

## Milestone 2 — as built (client, `vad/`)

`stream.go`:
- `wsURLFromHTTP(base)` — http(s) `/v1` base → `ws(s)://host/v1/asr/stream`.
- `pcm16ToLE(samples)` — int16 → little-endian wire bytes.
- `streamSession` + `dialStream(ctx, url, onMsg)` / `sendPCM` / `finish` — one
  WebSocket session per turn; a reader goroutine pumps decoded
  `partial`/`final`/`error` messages to `onMsg`.
- `endOfTurn` — counts continuous silence; fires once at `endOfTurnMs`.
- `newStreamPrinter()` — renders partials in place (`\r`+clear-to-EOL), commits
  the final with a newline, dedupes repeated partials.

`main.go` turn loop (streaming branch): on a confirmed onset, dial the socket and
replay the pre-roll; stream **every** window (including short pauses) so the
model keeps full context; finalise (`{"finish"}`) only after **`endOfTurnMs=800`**
of continuous silence. One session = one turn = one coherent transcript. The VAD
shifts from "segment every utterance" to "detect turn start + long end-of-turn."

Flag: `-stream` (default **on**); `-stream=false` is the Milestone 1 batch path;
`-asr ""` is pure VAD. Tests in `stream_test.go` cover URL derivation, PCM
encoding, the end-of-turn timer, a fake-server protocol run, and
`TestStreamLiveServer` (real `localhost:8008`, skipped when unreachable —
verified: 157 frames → Russian final in ~0.4 s).

## Server-side: bump to 1.7B + bigger context (pipecat repo)

Edited `pipecat/llms/.env` (inert until restart):
- `ASR_MODEL=Qwen/Qwen3-ASR-1.7B` (0.6B was the latency-test floor; 1.7B already
  cached). Accuracy lever — directly attacks the wrong-word problem.
- `ASR_MAX_MODEL_LEN=8192` (longer streaming turns; model supports 65536).
- `ASR_GPU_MEM_UTIL=0.50` (~8 GB; vLLM sizes the KV pool to fill it).

VRAM budget on the one 16 GB GPU0 (verify at restart — these are unverified
knobs): move the **Ollama** router (qwen3:4b-instruct, ~3.75 GB) to the idle
**GPU1** (`CUDA_VISIBLE_DEVICES=1` on the ollama service; it currently defaults
to GPU0). Then GPU0 = ASR ~8 + TTS ~3.2 ≈ 11.2 GB, ~5 GB headroom. Fallback if
Ollama stays on GPU0: `ASR_GPU_MEM_UTIL=0.40` + `ASR_MAX_MODEL_LEN=4096`.

Apply + verify:
```sh
# 1. move Ollama off GPU0 (CUDA_VISIBLE_DEVICES=1 on its service), then:
cd pipecat/llms && docker compose up -d --force-recreate qwen3-asr
# 2. watch the load + KV-cache log + 300 s healthcheck:
docker logs -f llms-qwen3-asr-1   # look for "GPU KV cache size: N tokens"
```
If `asr_server.py`'s prefill activation reserve (it couples
`max_num_batched_tokens` to `max_model_len`) is too heavy at 8192, expose
`ASR_MAX_NUM_BATCHED_TOKENS` in `docker-compose.yml` and cap it (e.g. 2048);
chunked prefill (enabled) splits longer prompts.

## Deferred / next

- **Context keywords (server change).** Accept `context` at WS start (e.g. a
  first `{"type":"start","context":"…","language":"ru"}` control message) and
  pass it to `init_streaming_state`. Enables Russian domain/name biasing — and
  the **hybrid long-turn rollover**: near the `max_model_len` cap, finalise and
  reopen a session seeded with the transcript tail as `context`. This is the one
  path that uses *both* features (streaming + context).
- Tune `chunk_size_sec` / `unfixed_*` (server env) and `endOfTurnMs` (client) for
  the latency/accuracy/turn-length balance.

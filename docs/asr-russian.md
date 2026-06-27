# Russian ASR — GigaAM (default STT)

> **Current default.** GigaAM v3 is the default batch STT: the `stt` service on `:8008`
> (`llm/giga-asr/`). Qwen3-ASR is now the **streaming + multilingual** overlay — its protocol
> and the streaming design live in [`asr-integration.md`](./asr-integration.md) and
> [`asr-streaming.md`](./asr-streaming.md). The decision + rig measurements are in
> [`research/gigaam-russian-asr.md`](./research/gigaam-russian-asr.md).

Switched to GigaAM on 2026-06-21. The Go side did **not** change: `pkg/asr.Transcriber` POSTs a
WAV to `{BaseURL}/audio/transcriptions` and reads `{"text":…}`; only the image serving `:8008`
differs. This doc is the source of truth for *which* model runs, *why*, and *how to install/run* it.

## TL;DR

- **GigaAM v3 (`v3_rnnt`)** — open SOTA Russian ASR (SberDevices, MIT, Conformer ~240M).
- **Russian-only** and **batch-only.** No streaming, no English. For either, run the Qwen3-ASR
  backend instead (`docker.qwen.yaml`, `make up-qwen`).
- **No Hugging Face token** — weights stream from the Sber CDN on first request.
- **Default run:** `make up` (builds + starts `stt`), then `make run` (VAD→ASR) or `make act`
  (full turn loop). `GIGAAM_MODEL=v3_e2e_rnnt make up` for punctuation + normalization.

## Why GigaAM

| Fact | Detail |
|---|---|
| Provenance | SberDevices / Salute. arXiv [2506.01192](https://arxiv.org/abs/2506.01192) (InterSpeech 2025). |
| License | **MIT** (since Dec 2024) — clean for the Bangkok-deployed ru SaaS. |
| Architecture | Conformer foundational encoder (~220–240M params), SSL pre-train (HuBERT-CTC), CTC + RNNT decoder heads. |
| v3 training | 700k h pre-train / 4k h ASR; +30% on new domains (callcenter, voice messages) vs v2. |
| Size | ~8× smaller than Qwen3-ASR-1.7B → less VRAM, faster, frees GPU for the hives. |
| Weights | Sber CDN (`cdn.chatwm.opensmodel.sberdevices.ru/GigaAM`), **no HF token**. |
| Repo | <https://github.com/salute-developers/GigaAM> · HF: `ai-sage/GigaAM-v3`. |

## Model family / variants

`GIGAAM_MODEL` selects the checkpoint (short names like `rnnt` resolve to `v3_rnnt`):

| Model | Output | Use |
|---|---|---|
| **`v3_rnnt`** (default) | raw lowercase, no punctuation | lowest WER; best feed for the LLM answer tier |
| `v3_ctc` | raw lowercase | CTC variant (slightly higher WER, can be faster) |
| `v3_e2e_rnnt` | **punctuation + text normalization** | display-ready text (logs, UI); ~slightly higher WER |
| `v3_e2e_ctc` | punctuation + normalization | CTC end-to-end variant |
| `v3_ssl` | audio embeddings | not ASR — feature extraction |
| `emo` | emotion probabilities | not ASR — `GigaAM-Emo` |

Default is `v3_rnnt`: downstream is the LLM answer tier, which doesn't need casing/punctuation,
and `v3_rnnt` was the more faithful transcript on English-heavy clips in our test (see below).

## Accuracy (Word Error Rate, %, lower = better)

First-party `evaluation.md` (their test suite, no external LM):

| | v3 RNNT | v3 CTC | T-One+LM | Whisper-large-v3 |
|---|---:|---:|---:|---:|
| **Average** | **8.3** | 9.1 | 16.3 | 21.0 |
| Callcenter | **9.5** | 10.3 | 13.5 | 23.1 |
| OpenSTT Phone Calls | **17.4** | 18.6 | 19.8 | 27.4 |

Independent confirmation: the alphacephei open-ru-ASR benchmark (updated through Sept 2025) puts
**GigaAM2** at the top of open Russian ASR (~8.4 avg), ahead of Vosk, Whisper, and NeMo. v3 is a
newer release (not yet in that table) that matches v2 on public sets and improves telephony.

## Measured on the rig (this session — one RTX 5060 Ti, GPU0, fp16)

`docs/research/gigaam_stt.py` and the live `stt` server, on
`docs/recordings/google-gemini-instructed__dev1-backend.wav` (24.87 s, dense Russian/English
dev-jargon — a worst case for a ru-only model):

| model | warm RTF | cold (incl. CUDA init) | VRAM |
|---|---|---|---|
| `v3_rnnt` | ~0.007 (0.18 s) | ~0.68 s | ~0.5 GB |

Russian core was essentially perfect; errors clustered on **English tech jargon** (`Pool`→"Pull",
`worker`→"оркере"; `e2e` mangled "логи в Кибана"→"логифки Бана" where raw `v3_rnnt` was correct).
Stack-wide VRAM/throughput facts live in [`infrastructure.md`](./infrastructure.md).

## Trade-offs — what GigaAM is NOT

- **Russian-only.** English code-switching degrades. Use the Qwen3-ASR overlay for multilingual.
- **Batch-only.** No streaming state → `WS /v1/asr/stream` is gated off (returns one error frame,
  then closes). Streaming stays on the Qwen3-ASR vLLM image. (`t-tech/T-One` is the open ru
  *streaming* SOTA if the streaming path is ever revisited on a local model.)

## The service contract (`llm/giga-asr/asr_server.py`)

Identical wire format to `llm/qwen3-asr`, so the Go client and every `make` target are unchanged.

| Endpoint | Contract |
|---|---|
| `POST /v1/audio/transcriptions` | multipart `file` (WAV; libsndfile-decodable), optional `model`/`language`/`response_format`. Returns `{"text":…, "usage":{"type":"duration","seconds":N}}`. `language` accepted but **ignored** (ru-only); `model` echoed (one model per process). |
| `GET /health` | `{"status":"ok"\|"loading","model_loaded":bool}` |
| `GET /v1/models` | `{"object":"list","data":[{"id":<model>,"object":"model"}]}` |
| `WS /v1/asr/stream` | **gated off** — one error frame then `close(1003)`; use `-stream=false` or the Qwen image |

Internally: decodes the upload in-process (soundfile → 16 kHz mono float32) and runs GigaAM's
`forward`/`_decode` directly (no ffmpeg subprocess); all model calls serialise behind one lock
(single-user MVP).

### Environment variables

| Var | Default | Meaning |
|---|---|---|
| `GIGAAM_MODEL` | `v3_rnnt` | checkpoint name, or a path to a fine-tuned `.ckpt` |
| `GIGAAM_DEVICE` | `cuda:0` | torch device; `cpu`; or `auto` |
| `GIGAAM_FP16` | `1` | fp16 encoder on GPU (ignored on CPU) |
| `GIGAAM_DOWNLOAD_ROOT` | unset → `~/.cache/gigaam` | weight cache dir (compose sets it on the `hf-cache` volume) |
| `GIGAAM_PORT` | `8008` | listen port |
| `GIGAAM_SKIP_MODEL_LOAD` | `0` | tests only — skip the load, inject a fake |

## Installation & run

### A. Docker — the default / production path

Prerequisites: a GPU host with the **NVIDIA Container Toolkit** (do **not** build in a sandbox).

```bash
make up        # builds image `voices-stt-giga` from llm/giga-asr, starts stt on :8008
make run       # VAD → GigaAM batch loop (transcribe + print)
make act       # full turn loop: ASR → answer (Gemini) → TTS (Qwen3-TTS); use headphones
```

First start downloads `v3_rnnt` (~426 MB) from the Sber CDN into the `hf-cache` volume at
`/root/.cache/huggingface/gigaam` — once; restarts are instant. No HF token. Verify:

```bash
curl -fsS http://localhost:8008/health        # {"status":"ok","model_loaded":true}
curl -fsS -X POST http://localhost:8008/v1/audio/transcriptions \
  -F "file=@clip.wav;type=audio/wav" -F "response_format=json"
```

Punctuated/normalized variant: `GIGAAM_MODEL=v3_e2e_rnnt make up` (or set `GIGAAM_MODEL` in `.env`).

> Image name is `voices-stt-giga` (distinct from the old Qwen `voices-stt`) so `up` rebuilds
> instead of silently reusing a stale image.

### B. Local Python — dev / eval / standalone server

Prerequisites: Python ≥ 3.10, `ffmpeg` on PATH. Blackwell (sm_120) needs cu12.8+ torch wheels;
the latest torch satisfies this (we ran torch 2.12.1+cu130).

```bash
# isolated venv (uv or python -m venv)
uv venv ~/.cache/gigaam-venv --python 3.12
uv pip install --python ~/.cache/gigaam-venv/bin/python \
  "gigaam[torch] @ git+https://github.com/salute-developers/GigaAM.git"

# 1) one-off CLI transcription (the original spike)
~/.cache/gigaam-venv/bin/python docs/research/gigaam_stt.py clip.wav \
  --models v3_rnnt,v3_e2e_rnnt          # [--device cpu] to force CPU

# 2) run the server standalone (also needs the web deps)
uv pip install --python ~/.cache/gigaam-venv/bin/python fastapi "uvicorn[standard]" python-multipart soundfile
GIGAAM_PORT=8009 ~/.cache/gigaam-venv/bin/python llm/giga-asr/asr_server.py
```

### C. Contract tests (no GPU, no torch)

```bash
python3 -m venv llm/giga-asr/.venv-test
llm/giga-asr/.venv-test/bin/pip install -r llm/giga-asr/tests/requirements-dev.txt
cd llm/giga-asr && GIGAAM_SKIP_MODEL_LOAD=1 .venv-test/bin/pytest tests/ -v
```

The tests inject a fake model and monkeypatch the torch path, so they assert the HTTP contract
(batch JSON/text, 400 on bad format, WS gated off, health, models) without a model or GPU.

## Switching to Qwen3-ASR (streaming + English)

GigaAM has no streaming and no English. The Qwen3-ASR vLLM image provides both on the same port:

```bash
docker compose -f docker.qwen.yaml up -d   # or: make up-qwen
make run-stream          # streaming partials over WS /v1/asr/stream
```

Trade-off: the vLLM streaming server reserves more VRAM — re-check GPU0 headroom against `tts`.
See [`asr-integration.md`](./asr-integration.md) / [`asr-streaming.md`](./asr-streaming.md).

## Files

| Path | Role |
|---|---|
| `llm/giga-asr/asr_server.py` | FastAPI server (OpenAI batch contract; WS gated off) |
| `llm/giga-asr/Dockerfile` | cu128 torch → GigaAM from git; runtime weight download |
| `llm/giga-asr/tests/` | contract tests + dev requirements |
| `docker.giga.yaml` | `stt` builds GigaAM (`llm/giga3`, image `xcono-asr-giga`) — default backend |
| `docker.qwen.yaml` | `stt` builds Qwen3-ASR vLLM (`llm/qwen3`, image `xcono-asr-qwen3`) — streaming + multilingual |
| `docs/research/gigaam_stt.py` | standalone CLI runner (the spike) |
| `pkg/asr/` | Go client — unchanged; endpoint-agnostic |

## References

- GigaAM repo & README: <https://github.com/salute-developers/GigaAM>
- Paper: <https://arxiv.org/abs/2506.01192> (InterSpeech 2025)
- Open ru ASR benchmark: <https://alphacephei.com/nsh/2025/04/18/russian-models.html>
- Decision + measurements: [`research/gigaam-russian-asr.md`](./research/gigaam-russian-asr.md)

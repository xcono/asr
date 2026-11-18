# Whisper large-v3-turbo (vLLM) STT backend

Started by `docker.whisper.yaml` in the repo root: `vllm serve
openai/whisper-large-v3-turbo` in the pinned image `vllm/vllm-omni:v0.32.0` (vLLM 0.32.0,
PyTorch 2.13.0+cu130). There is no Dockerfile — the image is used as published and every
setting is a flag in the compose file.

```bash
make up-whisper      # docker compose -f docker.whisper.yaml up -d
make down-whisper
```

| | |
|---|---|
| Model | `openai/whisper-large-v3-turbo`, HF snapshot `41f01f3f`, `model.safetensors` 1 617 824 864 B (fp16) |
| Where | shared HF cache volume `llms_hf-cache` (first `up` on a clean machine: `HF_HUB_OFFLINE=0`) |
| GPU | `ASR_GPU` (default `1`); 2.6 GB VRAM, constant |
| Start | 21 s to `/health` 200 with weights cached, when it does not stall (see "Start-up stall") |
| Speed | 0.08 s for a 2.5 s clip, 0.11 s for 8.6 s; first request after start 0.55 s (greedy, RTX 5060 Ti) |

Docs used: vLLM's
[Speech to Text APIs](https://github.com/vllm-project/vllm/blob/v0.32.0/docs/serving/online_serving/speech_to_text.md)
and `vllm serve --help=all` in the container. vLLM-Omni's own docs cover TTS only
(`/v1/audio/speech`); it lists no STT model. Whisper is served by the plain vLLM inside the
image, without `--omni`. The image was chosen because `xcono/tts` already has it on disk;
`vllm/vllm-openai:v0.32.0` should behave the same and was not tried.

## Why vLLM and not whisper.cpp (RTX 50xx / sm_120)

Until 2026-11-18 this backend was whisper.cpp's `whisper-server`. whisper.cpp does not
target this card: the upstream image is built for CUDA archs 75–90 and JIT-compiled its
kernels on the RTX 5060 Ti (~20 s first request, 1–4 s per new prompt length); our own
build for `120a-real` ran clean for a day and then, after a host reboot, hung on requests
and crashed with `Xid 13` (details in `AGENTS.md`). vLLM's PyTorch lists `sm_120` among its
supported architectures (`torch.cuda.get_arch_list()` in the container). The whisper.cpp
Dockerfile, image and model volume are removed; they are in git history before this change.

## Flags that are not optional on this card

- `--attention-backend TRITON_ATTN --mm-encoder-attn-backend TORCH_SDPA`. Left to itself
  vLLM picks its bundled FlashAttention 2, starts normally, answers 200 in 0.54 s — and the
  text is `ewewew…` up to the token limit for any audio (8 of 8 clips). With these two
  backends the text is right. Which of the two matters, and why FA2 is wrong here, was not
  investigated.
- `--kv-cache-memory-bytes 536870912 --gpu-memory-utilization 0.5`. By default vLLM asks
  for 92% of the card and exits when the TTS server already holds part of it. With the KV
  cache fixed, the utilization value is only a start-up check.
- No `--max-num-seqs`: with `2` the batch (896 tokens) is smaller than one audio item
  (1500) and the server exits with `Chunked MM input disabled but max_tokens_per_mm_item…`.

## Start-up stall (open)

One start out of four (2026-11-18) never became ready: the engine process sat at 100% CPU
in `torch.cuda.synchronize()` → `cuCtxSynchronize` (native stack taken with gdb) with the
GPU idle and nothing in the kernel log, for 8 minutes until restarted. Of the others, one
spent 82 s in engine init (the same step, by the log timestamps), one 3.5 s, one was not
timed. A container stuck there
stays `unhealthy` — Docker does not restart it; `docker restart asr-stt-1` does. This is
the same picture whisper.cpp showed on the same GPU (a thread spinning, GPU idle), so it
may not be specific to either server. Not established: the cause, and whether it can
happen on a request after start (none of 30 requests stalled).

## HTTP contract (measured 2026-11-18)

`POST /v1/audio/transcriptions` (multipart), `GET /health` → 200 once the engine is up
(connection refused before that).

- `file`: a WAV file. 16 kHz / 16-bit / mono with a 44-byte header (what `audio.EncodeWAV`
  writes) is accepted as is.
- `response_format=json` → `{"text":"…","usage":{"type":"duration","seconds":N}}`. The text
  starts with a space; `asr.Transcriber` trims it. `verbose_json` is accepted by the API
  but was not exercised here.
- `model`: optional. Omitted works (the Go config keeps `"model": ""`); the served name is
  `whisper`.
- `language`: `ru` is what the client sends. Omitted, the server detected Russian on the
  one clip tried (0.24 s instead of 0.08 s). There is no server-side default language.
- `prompt`: **do not send one.** vLLM splices it after a literal `<|prev|>`, which is not a
  Whisper token, and decoding degrades (vllm-project/vllm#53206, open; the line is in
  0.32.0, `vllm/model_executor/models/whisper.py`). Keep `asr.whisper.prompt` empty. This
  loses the term-spelling glossary that worked on whisper.cpp.
- Decoding is greedy. whisper.cpp ran beam 5 because its greedy mode looped on 3–4 s
  clips; no loop was seen here in 30 requests, including the same clip three times with
  identical text. Beam search exists (`use_beam_search`) and vLLM's docs call it highly
  inefficient for Whisper; not tried.
- Hallucinations on noise are the model's, not the server's: clips from `dropped/` still
  come back as `Продолжение следует...` and `Добавил субтитры DimaTorzok`. The letter
  budget and the phrase list stay necessary.

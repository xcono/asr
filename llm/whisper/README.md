# Whisper large-v3-turbo (whisper.cpp) STT backend

Started by `docker.whisper.yaml` in the repo root, which builds `./Dockerfile`: upstream
whisper.cpp pinned to commit `5922450f` (CUDA 13.0, ships `whisper-server`,
`download-ggml-model.sh`, `curl`, `ffmpeg`), compiled for the RTX 50xx
(`CMAKE_CUDA_ARCHITECTURES=120a-real`).

```bash
make up-whisper      # docker compose -f docker.whisper.yaml up -d
make down-whisper    # keeps the model volume
```

The first `up` builds the image (about 7 minutes) and downloads the model (~1.6 GB).

| | |
|---|---|
| Model | `ggml-large-v3-turbo.bin`, 1 624 555 275 B, sha256 `1fc70f774d38eb169993ac391eea357ef47c88757ef72ee5943879b7e8e2bc69` (fp16, not quantized; same as the LFS hash on huggingface.co/ggerganov/whisper.cpp) |
| Where | compose volume `asr_whisper-models` → `/models` (filled by the one-shot `model` service) |
| GPU | `ASR_GPU` (default `1`); about 2 GB VRAM in use after a few requests |
| Env | `WHISPER_MODEL` (default `large-v3-turbo`), `WHISPER_LANGUAGE` (default `ru`, `auto` to detect) |
| Speed | 0.12 s for a 3 s clip, 0.17 s for 7 s, 0.5 s for 25 s (beam 5, RTX 5060 Ti) |

## Why a local build (RTX 50xx / sm_120)

The upstream image `ghcr.io/ggml-org/whisper.cpp:main-cuda` is built for
`CMAKE_CUDA_ARCHITECTURES='75;80;86;90'` and carries no sm_120 code. On the RTX 5060 Ti
(compute capability 12.0) it still runs, but the driver JIT-compiles each ggml kernel on
first use. Measured on this machine, same commit, same model:

| | upstream image | this build |
|---|---|---|
| server log | `CUDA : ARCHS = 750,800,860,900` | `CUDA : ARCHS = 1200` |
| first request on a fresh container | ~20.5 s | 0.17–0.22 s |
| first request with a new prompt length (1–13 tokens) | 1.1–4.0 s each | 0.11–0.12 s |
| driver JIT cache (`/root/.nv`) after use | ~104 MB | 8 KB (nothing compiled) |
| transcripts on the test clips, with and without a prompt | — | byte-identical |

The image contains Blackwell device code only and does not run on older cards
(`CUDA_ARCHITECTURES` build arg in the Dockerfile).

Known oddity, unexplained: with a degenerate prompt (the same word repeated 6+ times,
e.g. ` да да да да да да`) a 3 s clip takes 0.44 s on this build versus 0.12 s on the
upstream image, with identical text. Seven realistic prompts (2–40 tokens) on two clips
showed no difference in time or text.

## HTTP contract (measured 2026-11-17)

`POST /v1/audio/transcriptions` (multipart), `GET /health` → `200 {"status":"ok"}`
(returns `503 {"status":"loading model"}` while the model loads).

- `file`: a WAV file. 16 kHz / 16-bit / mono with a 44-byte header (what `audio.EncodeWAV`
  writes) is accepted as is.
- `response_format=json` → `{"text":"…"}`, `text` is the only field. The text starts with a
  space and ends with `\n`. Longer audio also gets `\n ` between segments; the Go client
  (`asr.Transcriber`) flattens this to one line.
  `verbose_json` adds `task`, `language`, `duration`, `segments[]` (with `words[]`) and
  `detected_language*`.
- `model`: ignored on this path (any value, even an unknown one, gives the same 200 result).
- `language`: if omitted, the server's `-l` is used (`ru` here). Russian works with
  `language` omitted, `ru` or `auto`; `en` translates Russian speech into English. With the
  `ru` default, English speech comes back translated into Russian, so English needs
  `language=auto` or `en`. An unknown code (`xx`) still returns 200, with no error in the log.
- `prompt`: accepted; it steers the spelling of terms. On a 25 s technical-Russian sample a
  glossary prompt changed spellings to the prompt's forms (`race condition`,
  `connection pool`, `Kibana`, `хотфиксом`); on a 3 s clip it did not fix a misheard word.
  - The server uses the **last 223 tokens** of the prompt; a longer one is cut from the
    beginning, so the glossary belongs at the end.
  - Length costs little: 14 → 223 tokens adds 10–25 ms per request.
  - Unrelated text dilutes a glossary: the same 14-token glossary after 209 tokens of filler
    no longer fixed `хотфиксом` (one clip, one run).
- Other per-request fields: `temperature`, `temperature_inc`, `beam_size`, `best_of`,
  `no_timestamps`, `max_len`, `vad*`, `carry_initial_prompt` and more (see `server.cpp`).

`--beam-size 5` is set on the server on purpose. With the server's greedy default, 3–4 s
clips repeated phrases and gave a different text on each run.

# Whisper large-v3-turbo (whisper.cpp) STT backend

Started by `docker.whisper.yaml` in the repo root. No Dockerfile: the upstream
`ghcr.io/ggml-org/whisper.cpp:main-cuda` image (CUDA 13, ships `whisper-server`,
`download-ggml-model.sh`, `curl`, `ffmpeg`) is used as is, pinned by digest.

```bash
docker compose -f docker.whisper.yaml up -d     # first run downloads the model (~1.6 GB)
docker compose -f docker.whisper.yaml down      # keeps the model volume
```

| | |
|---|---|
| Model | `ggml-large-v3-turbo.bin`, 1 624 555 275 B, sha256 `1fc70f774d38eb169993ac391eea357ef47c88757ef72ee5943879b7e8e2bc69` (fp16, not quantized; same as the LFS hash on huggingface.co/ggerganov/whisper.cpp) |
| Where | compose volume `asr_whisper-models` → `/models` (filled by the one-shot `model` service) |
| GPU | `ASR_GPU` (default `1`); about 2 GB VRAM in use after a few requests |
| Env | `WHISPER_MODEL` (default `large-v3-turbo`), `WHISPER_LANGUAGE` (default `ru`, `auto` to detect) |

## HTTP contract (measured 2026-11-17, image digest `sha256:ff5d5510…`)

`POST /v1/audio/transcriptions` (multipart), `GET /health` → `200 {"status":"ok"}`
(returns `503 {"status":"loading model"}` while the model loads).

- `file`: a WAV file. 16 kHz / 16-bit / mono with a 44-byte header (what `audio.EncodeWAV`
  writes) is accepted as is.
- `response_format=json` → `{"text":"…"}`, `text` is the only field. The text starts with a
  space and ends with `\n`. Longer audio also gets `\n ` between segments, so trim it before use.
  `verbose_json` adds `task`, `language`, `duration`, `segments[]` (with `words[]`) and
  `detected_language*`.
- `model`: ignored on this path (any value, even an unknown one, gives the same 200 result).
- `language`: if omitted, the server's `-l` is used (`ru` here). Russian works with
  `language` omitted, `ru` or `auto`; `en` translates Russian speech into English. With the
  `ru` default, English speech comes back translated into Russian, so English needs
  `language=auto` or `en`. An unknown code (`xx`) still returns 200, with no error in the log.
- `prompt`: accepted. On a 25 s technical-Russian sample, a glossary prompt changed spellings
  to the prompt's forms (`race condition`, `connection pool`, `Kibana`, `хотфиксом`). On a
  3 s clip it did not fix a misheard word.
- Other per-request fields: `temperature`, `temperature_inc`, `beam_size`, `best_of`,
  `no_timestamps`, `max_len`, `vad*`, `carry_initial_prompt` and more (see `server.cpp`).

`--beam-size 5` is set on the server on purpose. With the server's greedy default, 3–4 s
clips repeated phrases and gave a different text on each run.

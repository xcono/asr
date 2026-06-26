"""GigaAM-backed STT server — the DEFAULT batch ASR for the voices stack.

Drop-in replacement for llm/qwen3-asr's asr_server_hf.py: SAME OpenAI-compatible batch
contract on the SAME :8008 port, so the Go pkg/asr.Transcriber and every `make run/act/
loopback` target work unchanged — only the acoustic model differs.

WHY GIGAAM
  GigaAM v3 (SberDevices, MIT, Conformer ~240M) is the open SOTA for Russian ASR and owns
  the telephony/call-center domains (see docs/research/gigaam-russian-asr.md). It is ~8x
  smaller than Qwen3-ASR-1.7B, runs at RTF ~0.01-0.03 warm on one RTX 5060 Ti (~0.5 GB
  VRAM, fp16), and needs NO HF token — weights stream from the Sber CDN.
  TRADE-OFF: GigaAM is Russian-ONLY. English code-switching degrades (Pool→"Pull",
  Kibana→"Бана"); keep the Qwen3-ASR image for a multilingual path. It is also the
  streaming backend — GigaAM has no streaming (see below).

ENDPOINTS
  POST /v1/audio/transcriptions   OpenAI multipart (file, model, language, response_format).
                                  Returns {"text": ..., "usage": {"type":"duration","seconds":N}}.
                                  `language` is accepted for OpenAI-compat but IGNORED (ru-only).
                                  `model` is echoed; this process serves ONE model (MVP).
  WS   /v1/asr/stream             GATED OFF — GigaAM is utterance/batch only. Accepts then rejects
                                  with a reason so a misrouted `vad -stream=true` client gets told
                                  why instead of hanging. For streaming run the Qwen3-ASR vLLM
                                  image (docker-compose.stream.yaml).
  GET  /health                    -> {"status":"ok"|"loading","model_loaded":bool}
  GET  /v1/models                 -> {"object":"list","data":[{"id":<model>,"object":"model"}]}

One model => one lock: all model calls serialise behind _MODEL_LOCK (MVP, single user),
exactly as in the Qwen3-ASR server.

Env (optional, with defaults):
  GIGAAM_MODEL           (v3_rnnt)   GigaAM model name. v3_rnnt = lowest WER, raw lowercase text
                         (best feed for the LLM answer tier — casing/punctuation don't matter
                         downstream, and it was more faithful on English-heavy clips). v3_e2e_rnnt
                         adds punctuation + text normalization (display-ready, slightly higher WER).
                         Also: v3_ctc, v3_e2e_ctc, or a local path to a fine-tuned .ckpt.
  GIGAAM_DEVICE          (cuda:0)    torch device; "cpu"; or "auto" (cuda if available else cpu).
  GIGAAM_FP16            (1)         fp16 encoder on GPU (ignored on CPU).
  GIGAAM_DOWNLOAD_ROOT   (unset)     weight cache dir; defaults to ~/.cache/gigaam.
  GIGAAM_PORT            (8008)
  GIGAAM_SKIP_MODEL_LOAD (0)         skip the load for contract tests; the caller injects a fake
                         into _MODEL and/or monkeypatches _transcribe_f32.
"""
from __future__ import annotations

import io
import json
import os
import threading
from contextlib import asynccontextmanager

import numpy as np
import soundfile as sf
import uvicorn
from fastapi import FastAPI, File, Form, HTTPException, WebSocket
from fastapi.responses import PlainTextResponse


def _envflag(name: str, default: bool = False) -> bool:
    return os.environ.get(name, "1" if default else "0").strip().lower() in ("1", "true", "yes", "on")


MODEL_NAME = os.environ.get("GIGAAM_MODEL", "v3_rnnt").strip()
DEVICE = os.environ.get("GIGAAM_DEVICE", "cuda:0").strip()
FP16 = _envflag("GIGAAM_FP16", True)
DOWNLOAD_ROOT = os.environ.get("GIGAAM_DOWNLOAD_ROOT", "").strip() or None
PORT = int(os.environ.get("GIGAAM_PORT", "8008"))

# gigaam's .transcribe() hard-fails beyond this; we warn but still transcribe so a long
# turn degrades instead of dropping mid-conversation (VAD utterances rarely exceed it).
LONGFORM_THRESHOLD_S = 25.0

_MODEL = None                    # gigaam.GigaAMASR | None
_MODEL_LOCK = threading.Lock()   # serialise all model calls (one model, single user)


def _resolve_device(name: str) -> str:
    if name in ("", "auto"):
        import torch

        return "cuda:0" if torch.cuda.is_available() else "cpu"
    return name


def _decode_to_16k_mono_f32(raw: bytes) -> np.ndarray:
    """Decode an uploaded audio file (wav/flac/ogg/... via libsndfile) to mono float32 @ 16 kHz.

    The Go pkg/asr client always sends 16 kHz mono PCM WAV, so the resample branch is a
    defensive no-op for it; other clients (curl with a 24 kHz clip) are handled too.
    """
    data, sr = sf.read(io.BytesIO(raw), dtype="float32", always_2d=False)
    if data.ndim == 2:
        data = data.mean(axis=1)
    data = np.asarray(data, dtype=np.float32)
    if sr != 16000 and data.size:
        dur = data.shape[0] / float(sr)
        n = max(1, int(round(dur * 16000)))
        x_old = np.linspace(0.0, dur, num=data.shape[0], endpoint=False)
        x_new = np.linspace(0.0, dur, num=n, endpoint=False)
        data = np.interp(x_new, x_old, data).astype(np.float32)
    return np.ascontiguousarray(data, dtype=np.float32)


def _transcribe_f32(audio: np.ndarray) -> str:
    """Run GigaAM on a 16 kHz mono float32 waveform already in memory.

    Replicates GigaAMASR.transcribe()'s body (prepare_wav -> forward -> _decode) but feeds
    the in-memory array directly instead of re-decoding a file through ffmpeg, and drops the
    25 s LONGFORM guard so a long turn degrades rather than 500-ing mid-call. forward()
    autocasts to fp16 on GPU internally; we still match transcribe()'s input dtype.
    Patched out in contract tests (so they need neither torch nor a GPU).
    """
    import torch

    if audio.size == 0:
        return ""
    wav = torch.from_numpy(audio).to(_MODEL._device).to(_MODEL._dtype).unsqueeze(0)
    length = torch.full([1], wav.shape[-1], device=_MODEL._device)
    with torch.inference_mode():
        encoded, encoded_len = _MODEL.forward(wav, length)
        decoded = _MODEL._decode(encoded, encoded_len, length, word_timestamps=False)
    text = decoded[0][0] if decoded else ""
    return (text or "").strip()


@asynccontextmanager
async def lifespan(_app: FastAPI):
    global _MODEL
    if not _envflag("GIGAAM_SKIP_MODEL_LOAD"):
        import gigaam

        # load_model downloads the checkpoint (and tokenizer for e2e/rnnt) on first use to
        # download_root (default ~/.cache/gigaam) from the Sber CDN — no HF token required.
        _MODEL = gigaam.load_model(
            MODEL_NAME,
            fp16_encoder=FP16,
            device=_resolve_device(DEVICE),
            download_root=DOWNLOAD_ROOT,
        )
    yield
    # Process exit frees the model; no explicit shutdown hook needed.


app = FastAPI(lifespan=lifespan)


@app.get("/health")
def health():
    return {"status": "ok" if _MODEL is not None else "loading", "model_loaded": _MODEL is not None}


@app.get("/v1/models")
def models():
    return {"object": "list", "data": [{"id": MODEL_NAME, "object": "model", "owned_by": "salute"}]}


@app.post("/v1/audio/transcriptions")
def transcriptions(
    file: bytes = File(...),
    model: str = Form(default=MODEL_NAME),       # echoed; one model per process (MVP)
    language: str | None = Form(default=None),   # accepted for OpenAI-compat; ignored (ru-only)
    response_format: str = Form(default="json"),
):
    # plain `def` -> FastAPI runs this in its threadpool, so the blocking model call below
    # doesn't stall the event loop. Mirrors the Qwen3-ASR server's handler 1:1.
    if response_format not in ("json", "text"):
        raise HTTPException(status_code=400, detail="only response_format=json|text supported")
    if _MODEL is None:
        raise HTTPException(status_code=503, detail="model still loading")
    audio = _decode_to_16k_mono_f32(file)
    seconds = float(audio.shape[0]) / 16000.0 if audio.size else 0.0
    with _MODEL_LOCK:
        text = _transcribe_f32(audio)
    if response_format == "text":
        return PlainTextResponse(text)
    return {"text": text, "usage": {"type": "duration", "seconds": seconds}}


@app.websocket("/v1/asr/stream")
async def asr_stream_ws_disabled(websocket: WebSocket):
    """GATED OFF — GigaAM is utterance/batch only; it has no streaming state.

    Accept then immediately reject with a one-line reason so a client that connects by mistake
    (e.g. `vad -stream=true`) gets a clear message instead of a confusing hang. Use
    `-stream=false` against this image, or run the Qwen3-ASR vLLM image for /v1/asr/stream.
    """
    await websocket.accept()
    await websocket.send_text(json.dumps({
        "type": "error",
        "detail": "streaming not supported by GigaAM (batch-only); use -stream=false, "
                  "or run the Qwen3-ASR vLLM image for /v1/asr/stream",
    }))
    # 1003 = "unsupported data": this endpoint cannot serve the streaming protocol here.
    await websocket.close(code=1003)


if __name__ == "__main__":
    uvicorn.run(app, host="0.0.0.0", port=PORT)

"""Transformers-backend, BATCH-ONLY variant of asr_server.py — for A/B vs the vLLM image.

Same OpenAI-compatible batch transcription contract as asr_server.py, but the model is
loaded on the qwen_asr **transformers** backend (``Qwen3ASRModel.from_pretrained``) instead
of the vLLM backend (``Qwen3ASRModel.LLM``). No vLLM, no torch.compile, no CUDA-graph
startup, and no ``gpu_memory_utilization`` reservation to tune against the co-resident
TTS/Ollama — the model just allocates its weights (+activations).

WHY THIS EXISTS
  The chunked streaming API (``init_streaming_state`` / ``streaming_transcribe`` /
  ``finish_streaming_transcribe``) is implemented ONLY by qwen_asr's vLLM backend; the
  transformers backend has no streaming state. So this image can serve everything that uses
  the batch endpoint (e.g. the `vad -stream=false` demo, which POSTs one clip per utterance
  to /v1/audio/transcriptions) but CANNOT serve the WebSocket streaming endpoint. That route
  is therefore GATED OFF (see asr_stream_ws_disabled). For streaming, run the vLLM image.

ENDPOINTS
  POST /v1/audio/transcriptions   Identical contract / handler to asr_server.py: OpenAI
                                  multipart (file, model, language, response_format); returns
                                  {"text": ..., "usage": {"type":"duration","seconds":N}}.
  WS   /v1/asr/stream             GATED OFF — accepts then rejects with a one-line reason so a
                                  client that connects by mistake gets told why instead of
                                  hanging. Use -stream=false, or the vLLM image, for streaming.
  GET  /health                    -> {"status":"ok"|"loading","model_loaded":bool}
  GET  /v1/models                 -> {"object":"list","data":[{"id":..., "object":"model"}]}

One model => one lock: all calls into the model run serialised behind ``_MODEL_LOCK`` (MVP,
single user), exactly as in asr_server.py.

Env (optional, with defaults):
  ASR_MODEL                  (Qwen/Qwen3-ASR-1.7B)  HF repo id or local path. Defaults to 1.7B
                             since this variant exists to A/B "1.7B without vLLM"; override for 0.6B.
  ASR_DTYPE                  (bfloat16)  bfloat16|float16|float32 (aliases bf16/fp16/half/fp32).
  ASR_DEVICE_MAP             (cuda:0)    transformers device_map. Unlike vLLM, cuda:N selection
                             works directly here — no CUDA_VISIBLE_DEVICES quirk.
  ASR_ATTN_IMPL              ("")        Optional attn_implementation, e.g. flash_attention_2 or
                             sdpa. Empty => let transformers pick (sdpa). FlashAttention 2 needs
                             fp16/bf16 + compatible hardware and a built flash-attn wheel.
  ASR_MAX_INFERENCE_BATCH_SIZE (8)       Internal inference batch-size cap (-1 = unlimited).
                             Smaller avoids OOM on long clips. Replaces vLLM's batched-tokens knob.
  ASR_MAX_NEW_TOKENS         (256)       Cap on generated transcript tokens per request.
  ASR_LANGUAGE               (Russian)   Default forced language *name* (Russian/English), or
                             "auto"/"" for per-utterance auto-detection. Per-request `language`
                             (ISO codes ru/en or names) overrides it.
  ASR_PORT                   (8008)
  ASR_SKIP_MODEL_LOAD        (0)         Set 1 to skip qwen_asr import/load (tests only; caller
                             injects a fake model into ``asr_server_hf._MODEL`` before use).
"""
from __future__ import annotations

import io
import json
import os
import re
import threading
from contextlib import asynccontextmanager

import numpy as np
import soundfile as sf
import uvicorn
from fastapi import FastAPI, File, Form, HTTPException, WebSocket
from fastapi.responses import PlainTextResponse


def _envflag(name: str, default: bool = False) -> bool:
    return os.environ.get(name, "1" if default else "0").strip().lower() in ("1", "true", "yes", "on")


# --- transformers-backend config (vLLM knobs intentionally absent) ---
MODEL_NAME = os.environ.get("ASR_MODEL", "Qwen/Qwen3-ASR-1.7B")
DTYPE = os.environ.get("ASR_DTYPE", "bfloat16").strip()
DEVICE_MAP = os.environ.get("ASR_DEVICE_MAP", "cuda:0").strip()
ATTN_IMPL = os.environ.get("ASR_ATTN_IMPL", "").strip()  # "" -> transformers default (sdpa)
MAX_INFERENCE_BATCH_SIZE = int(os.environ.get("ASR_MAX_INFERENCE_BATCH_SIZE", "8"))
MAX_NEW_TOKENS = int(os.environ.get("ASR_MAX_NEW_TOKENS", "256"))
PORT = int(os.environ.get("ASR_PORT", "8008"))

# Qwen3-ASR takes full language *names* ("Russian"/"English"), not ISO codes; None means
# auto-detect. OpenAI-compatible clients send ISO-639-1 codes, so map the common ones to names.
_LANG_ALIASES = {
    "ru": "Russian", "rus": "Russian", "russian": "Russian",
    "en": "English", "eng": "English", "english": "English",
}


def _normalize_language(lang: str | None) -> str | None:
    """Map a user/ISO language hint to a Qwen3-ASR language name, or None for auto-detect.

    "", "auto", "none", "detect" (case-insensitive) -> None; known ISO codes/names -> the
    Qwen language name; anything else is passed through for the model to validate.
    """
    if lang is None:
        return None
    s = lang.strip()
    if not s or s.lower() in ("auto", "none", "detect"):
        return None
    return _LANG_ALIASES.get(s.lower(), s)


DEFAULT_LANGUAGE = _normalize_language(os.environ.get("ASR_LANGUAGE", "Russian"))

_MODEL = None                    # qwen_asr.Qwen3ASRModel | None
_MODEL_LOCK = threading.Lock()   # serialise all model calls
_PREAMBLE_RE = re.compile(r"^\s*language\s+\S+\s*<asr_text>", re.IGNORECASE)


def _strip_preamble(text: str) -> str:
    """Defensive: the high-level qwen_asr API already returns parsed text, but if a raw
    `language <Lang><asr_text>...` form ever leaks through, return the part after the tag."""
    if not text:
        return ""
    if _PREAMBLE_RE.search(text):
        return text.split("<asr_text>", 1)[1].strip()
    if "<asr_text>" in text:
        return text.split("<asr_text>", 1)[1].strip()
    return text.strip()


def _decode_to_16k_mono_f32(raw: bytes) -> np.ndarray:
    """Decode an uploaded audio file (wav/flac/ogg/... via libsndfile) to mono float32 @ 16 kHz."""
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
    return data


def _resolve_dtype(name: str):
    """Map an ASR_DTYPE string to a torch dtype. Imported lazily so the module stays
    importable (and testable) without torch when ASR_SKIP_MODEL_LOAD=1."""
    import torch

    table = {
        "bfloat16": torch.bfloat16, "bf16": torch.bfloat16,
        "float16": torch.float16, "fp16": torch.float16, "half": torch.float16,
        "float32": torch.float32, "fp32": torch.float32, "float": torch.float32,
    }
    return table.get(name.lower(), torch.bfloat16)


@asynccontextmanager
async def lifespan(_app: FastAPI):
    global _MODEL
    if not _envflag("ASR_SKIP_MODEL_LOAD"):
        from qwen_asr import Qwen3ASRModel

        # from_pretrained() == the transformers backend (no vLLM). max_inference_batch_size
        # caps the internal inference batch (OOM guard); there is no KV reservation to size.
        kwargs = dict(
            dtype=_resolve_dtype(DTYPE),
            device_map=DEVICE_MAP,
            max_inference_batch_size=MAX_INFERENCE_BATCH_SIZE,
            max_new_tokens=MAX_NEW_TOKENS,
        )
        if ATTN_IMPL:
            # Only pass when explicitly set — an unsupported value would raise at load.
            kwargs["attn_implementation"] = ATTN_IMPL
        _MODEL = Qwen3ASRModel.from_pretrained(MODEL_NAME, **kwargs)
    yield
    # No explicit shutdown hook needed; process exit frees the model.


app = FastAPI(lifespan=lifespan)


@app.get("/health")
def health():
    return {"status": "ok" if _MODEL is not None else "loading", "model_loaded": _MODEL is not None}


@app.get("/v1/models")
def models():
    return {"object": "list", "data": [{"id": MODEL_NAME, "object": "model", "owned_by": "qwen"}]}


@app.post("/v1/audio/transcriptions")
def transcriptions(
    file: bytes = File(...),
    model: str = Form(default=MODEL_NAME),
    language: str | None = Form(default=None),
    response_format: str = Form(default="json"),
):
    # plain `def` -> FastAPI runs this in its threadpool, so the blocking
    # `_MODEL.transcribe(...)` below doesn't stall the event loop. This handler is
    # byte-for-byte the same as asr_server.py's — the backend differs only in how _MODEL
    # was constructed; the high-level transcribe() API is unified across backends.
    if response_format not in ("json", "text"):
        raise HTTPException(status_code=400, detail="only response_format=json|text supported")
    audio = _decode_to_16k_mono_f32(file)
    seconds = float(audio.shape[0]) / 16000.0 if audio.size else 0.0
    lang = _normalize_language(language) if language is not None else DEFAULT_LANGUAGE
    with _MODEL_LOCK:
        # transcribe() takes a (waveform, sample_rate) tuple wrapped in a list; language=None
        # -> auto-detect, a language *name* forces that language; returned text is preamble-free.
        results = _MODEL.transcribe(audio=[(audio, 16000)], language=lang)
    text = _strip_preamble(results[0].text if results else "")
    if response_format == "text":
        return PlainTextResponse(text)
    return {"text": text, "usage": {"type": "duration", "seconds": seconds}}


@app.websocket("/v1/asr/stream")
async def asr_stream_ws_disabled(websocket: WebSocket):
    """GATED OFF on the transformers backend.

    qwen_asr's chunked streaming (init/streaming/finish_streaming_transcribe) is vLLM-only;
    from_pretrained() exposes no streaming state. We accept then immediately reject with a
    one-line reason so a client that connects by mistake (e.g. `vad -stream=true`) gets a
    clear message instead of a confusing hang. Use `vad -stream=false` (batch) against this
    image, or run the vLLM image for /v1/asr/stream.
    """
    await websocket.accept()
    await websocket.send_text(json.dumps({
        "type": "error",
        "detail": "streaming not supported on the transformers backend (batch-only image); "
                  "use -stream=false, or run the vLLM image for /v1/asr/stream",
    }))
    # 1003 = "unsupported data": this endpoint cannot serve the streaming protocol here.
    await websocket.close(code=1003)


if __name__ == "__main__":
    uvicorn.run(app, host="0.0.0.0", port=PORT)

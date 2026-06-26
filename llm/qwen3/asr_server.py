"""OpenAI-compatible batch transcription + a WebSocket streaming endpoint for Qwen3-ASR.

Single FastAPI app holding one in-process model (``qwen_asr.Qwen3ASRModel.LLM``),
serving on 0.0.0.0:8008. Replaces the previous two-process setup (the ``qwen-asr-serve``
vLLM subprocess + the ``openai_asr_proxy.py`` shim) so the same model load backs both:

  POST /v1/audio/transcriptions         OpenAI multipart (file, model, language,
                                        response_format); batch transcribe; returns
                                        {"text": ..., "usage": {"type":"duration","seconds":N}}.
                                        Same contract the openai.STT(base_url=...) shim expects.
  WS   /v1/asr/stream                   WebSocket streaming endpoint.
                                        On connect, a streaming state is initialised.
                                        Protocol:
                                          - binary message  = raw 16-bit LE PCM @ 16 kHz mono;
                                            server responds with JSON:
                                            {"type":"partial","text":<cumulative>,"language":<detected or "">}
                                          - text message {"type":"finish"} = end of utterance;
                                            server responds with JSON:
                                            {"type":"final","text":<final>}
                                            then closes the connection (code 1000).
                                          - any other text message type returns JSON:
                                            {"type":"error","detail":"<reason>"}
                                          - odd-length binary frame (not a multiple of 2) returns JSON:
                                            {"type":"error","detail":"PCM frame length must be a multiple of 2"}
                                        One WebSocket connection == one streaming session.
                                        On WebSocketDisconnect, state is finalized/cleaned up.
  GET  /health                          -> {"status":"ok","model_loaded":bool}
  GET  /v1/models                       -> {"object":"list","data":[{"id":..., "object":"model"}]}

One model => one lock: all calls into the model run serialised behind ``_MODEL_LOCK``
(MVP, single user). Idle streaming sessions are managed by the WebSocket connection
lifecycle — one connection == one streaming state; no external GC needed.

Env (optional, with defaults): ASR_MODEL (Qwen/Qwen3-ASR-0.6B), ASR_GPU_MEM_UTIL (0.40),
ASR_MAX_MODEL_LEN (4096), ASR_MAX_NUM_BATCHED_TOKENS (= ASR_MAX_MODEL_LEN; sizes the
chunked-prefill batch and the audio-encoder activation reserve), ASR_MAX_NEW_TOKENS (256;
cap on generated transcript tokens per request -- keep modest for streaming, raise for long
batch clips), ASR_ENFORCE_EAGER (0; set 1 to skip vLLM's torch.compile), ASR_STREAM_CHUNK_SEC
(1.0), ASR_STREAM_UNFIXED_CHUNKS (2), ASR_STREAM_UNFIXED_TOKENS (5), ASR_PORT (8008),
ASR_LANGUAGE (Russian; default forced transcription language as a Qwen3-ASR language *name*
like Russian/English, or "auto"/"" for per-utterance auto-detection. The batch endpoint's
per-request `language` field overrides it — and also accepts ISO codes like ru/en — and the
streaming endpoint accepts a `?language=` query-param override),
ASR_SKIP_MODEL_LOAD (0; set 1 to skip qwen_asr import/load — for testing only, requires
caller to inject a fake model into ``asr_server._MODEL`` before use).
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
from fastapi import Body, FastAPI, File, Form, HTTPException, WebSocket, WebSocketDisconnect
from fastapi.responses import PlainTextResponse


def _envflag(name: str, default: bool = False) -> bool:
    return os.environ.get(name, "1" if default else "0").strip().lower() in ("1", "true", "yes", "on")


MODEL_NAME = os.environ.get("ASR_MODEL", "Qwen/Qwen3-ASR-0.6B")
GPU_MEM_UTIL = float(os.environ.get("ASR_GPU_MEM_UTIL", "0.40"))
MAX_MODEL_LEN = int(os.environ.get("ASR_MAX_MODEL_LEN", "4096"))
MAX_NUM_BATCHED_TOKENS = int(os.environ.get("ASR_MAX_NUM_BATCHED_TOKENS", str(MAX_MODEL_LEN)))
MAX_NEW_TOKENS = int(os.environ.get("ASR_MAX_NEW_TOKENS", "256"))
ENFORCE_EAGER = _envflag("ASR_ENFORCE_EAGER", default=False)
STREAM_CHUNK_SEC = float(os.environ.get("ASR_STREAM_CHUNK_SEC", "1.0"))
STREAM_UNFIXED_CHUNKS = int(os.environ.get("ASR_STREAM_UNFIXED_CHUNKS", "2"))
STREAM_UNFIXED_TOKENS = int(os.environ.get("ASR_STREAM_UNFIXED_TOKENS", "5"))
PORT = int(os.environ.get("ASR_PORT", "8008"))

# Qwen3-ASR takes full language *names* ("Russian"/"English"), not ISO codes; None means
# auto-detect (the model picks the language per utterance). NOTE: transcribe()'s list form is
# one-language-*per-audio-sample* (the batch dim), NOT a primary/secondary candidate list —
# the model has no fallback-language feature. This server is OpenAI-compatible and such clients
# send ISO-639-1 codes ("ru"/"en"), so the common ones are mapped to names below.
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


# Default forced language for both batch and streaming. "Russian" is primary; set
# ASR_LANGUAGE=auto (or empty) for per-utterance auto-detection across all supported languages.
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


def _pcm16_bytes_to_f32(raw: bytes) -> np.ndarray:
    return np.frombuffer(raw, dtype="<i2").astype(np.float32) / 32768.0


@asynccontextmanager
async def lifespan(_app: FastAPI):
    global _MODEL
    if not _envflag("ASR_SKIP_MODEL_LOAD"):
        from qwen_asr import Qwen3ASRModel

        # extra kwargs flow through Qwen3ASRModel.LLM(...) -> vllm.LLM(...).
        # max_num_batched_tokens (== max_model_len by default) keeps the chunked-prefill batch
        # and the audio-encoder activation reserve small so KV cache fits in the VRAM slice.
        _MODEL = Qwen3ASRModel.LLM(
            model=MODEL_NAME,
            max_new_tokens=MAX_NEW_TOKENS,
            gpu_memory_utilization=GPU_MEM_UTIL,
            max_model_len=MAX_MODEL_LEN,
            max_num_batched_tokens=MAX_NUM_BATCHED_TOKENS,
            enforce_eager=ENFORCE_EAGER,
        )
    yield
    # vLLM has no explicit shutdown hook we need here; process exit is fine.


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
    # `_MODEL.transcribe(...)` below doesn't stall the event loop.
    if response_format not in ("json", "text"):
        raise HTTPException(status_code=400, detail="only response_format=json|text supported")
    audio = _decode_to_16k_mono_f32(file)
    seconds = float(audio.shape[0]) / 16000.0 if audio.size else 0.0
    # An explicitly-provided `language` field wins (empty/"auto" -> auto-detect; ISO codes and
    # names are normalized); when the field is omitted, fall back to the server default
    # (ASR_LANGUAGE, "Russian").
    lang = _normalize_language(language) if language is not None else DEFAULT_LANGUAGE
    with _MODEL_LOCK:
        # transcribe() takes a (waveform, sample_rate) tuple (a bare np.ndarray is NOT
        # accepted -- see qwen_asr.inference.utils.normalize_audio_input); wrap it in a list
        # so ensure_list() treats the tuple as one item. language=None -> auto-detect, a
        # language *name* forces that language; the returned text is already preamble-free.
        results = _MODEL.transcribe(audio=[(audio, 16000)], language=lang)
    text = _strip_preamble(results[0].text if results else "")
    if response_format == "text":
        return PlainTextResponse(text)
    return {"text": text, "usage": {"type": "duration", "seconds": seconds}}


@app.websocket("/v1/asr/stream")
async def asr_stream_ws(websocket: WebSocket):
    """WebSocket streaming endpoint.

    One connection == one streaming session. Binary messages are raw 16-bit LE PCM
    @ 16 kHz mono. A text message ``{"type":"finish"}`` finalises the session.
    All model calls are serialised behind ``_MODEL_LOCK``.
    """
    await websocket.accept()

    # Per-connection language override via query string (?language=English|en|auto); when
    # absent, use the server default (ASR_LANGUAGE, "Russian"). Streaming forces a single
    # language for the whole session — there is no per-frame language.
    req_lang = websocket.query_params.get("language")
    lang = _normalize_language(req_lang) if req_lang is not None else DEFAULT_LANGUAGE

    # Initialise a streaming state for this connection.
    with _MODEL_LOCK:
        state = _MODEL.init_streaming_state(
            language=lang,
            unfixed_chunk_num=STREAM_UNFIXED_CHUNKS,
            unfixed_token_num=STREAM_UNFIXED_TOKENS,
            chunk_size_sec=STREAM_CHUNK_SEC,
        )

    try:
        while True:
            message = await websocket.receive()

            if message["type"] == "websocket.disconnect":
                # Client disconnected; finalise state and exit cleanly.
                try:
                    with _MODEL_LOCK:
                        _MODEL.finish_streaming_transcribe(state)
                except Exception:
                    pass
                return

            if "bytes" in message and message["bytes"] is not None:
                # --- binary frame: raw 16-bit LE PCM ---
                raw: bytes = message["bytes"]
                if len(raw) % 2 != 0:
                    await websocket.send_text(json.dumps({
                        "type": "error",
                        "detail": "PCM frame length must be a multiple of 2 bytes (16-bit LE PCM)",
                    }))
                    continue
                if raw:
                    pcm = _pcm16_bytes_to_f32(raw)
                    with _MODEL_LOCK:
                        _MODEL.streaming_transcribe(pcm, state)
                # Return cumulative partial transcript (even for empty frame).
                text = _strip_preamble(getattr(state, "text", "") or "")
                lang = getattr(state, "language", "") or ""
                await websocket.send_text(json.dumps({
                    "type": "partial",
                    "text": text,
                    "language": lang,
                }))

            elif "text" in message and message["text"] is not None:
                # --- text control message ---
                try:
                    ctrl = json.loads(message["text"])
                except (json.JSONDecodeError, ValueError):
                    await websocket.send_text(json.dumps({
                        "type": "error",
                        "detail": "control message must be valid JSON",
                    }))
                    continue

                msg_type = ctrl.get("type")
                if msg_type == "finish":
                    with _MODEL_LOCK:
                        _MODEL.finish_streaming_transcribe(state)
                        text = _strip_preamble(getattr(state, "text", "") or "")
                    await websocket.send_text(json.dumps({
                        "type": "final",
                        "text": text,
                    }))
                    await websocket.close(code=1000)
                    return
                else:
                    await websocket.send_text(json.dumps({
                        "type": "error",
                        "detail": f"unknown control message type: {msg_type!r}",
                    }))

    except WebSocketDisconnect:
        # Unexpected disconnect: finalise state.
        try:
            with _MODEL_LOCK:
                _MODEL.finish_streaming_transcribe(state)
        except Exception:
            pass


if __name__ == "__main__":
    uvicorn.run(app, host="0.0.0.0", port=PORT)

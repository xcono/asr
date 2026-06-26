# Run with: GIGAAM_SKIP_MODEL_LOAD=1 .venv-test/bin/pytest tests/test_asr_server.py -v
# from llm/giga-asr/ (light test deps: tests/requirements-dev.txt — no torch, no gigaam, no GPU).
"""
Contract tests for asr_server.py — the GigaAM batch ASR (default `stt`).

The real GigaAM model needs a GPU and is not loaded here: GIGAAM_SKIP_MODEL_LOAD=1 stops the
lifespan hook from importing gigaam/torch, a sentinel is injected into asr_server._MODEL so the
handlers see a "loaded" model, and asr_server._transcribe_f32 is monkeypatched to a canned
string (the torch forward/_decode path is exercised by the live smoke test, not here).

These assert the batch endpoint matches the Qwen3-ASR server's wire contract AND that the
streaming WebSocket is gated off (GigaAM is batch-only).
"""
from __future__ import annotations

import io
import json
import os
import sys
import wave

import pytest

# Ensure the module directory is on the path so `import asr_server` works.
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

os.environ["GIGAAM_SKIP_MODEL_LOAD"] = "1"

import asr_server  # noqa: E402  (side-effect: defines `app`)

# A non-None sentinel makes /health report "ok" and the endpoint skip its 503 guard; the real
# torch path is replaced so no model/tensors are needed.
asr_server._MODEL = object()
asr_server._transcribe_f32 = lambda audio: "привет мир"

from fastapi.testclient import TestClient  # noqa: E402
from starlette.websockets import WebSocketDisconnect  # noqa: E402

client = TestClient(asr_server.app, raise_server_exceptions=True)


def _make_wav_bytes(duration_sec: float = 0.1, sample_rate: int = 16000) -> bytes:
    n_samples = int(sample_rate * duration_sec)
    buf = io.BytesIO()
    with wave.open(buf, "wb") as wf:
        wf.setnchannels(1)
        wf.setsampwidth(2)
        wf.setframerate(sample_rate)
        wf.writeframes(b"\x00\x00" * n_samples)
    return buf.getvalue()


# ---------------------------------------------------------------------------
# Batch transcription — same contract as the Qwen3-ASR server
# ---------------------------------------------------------------------------

class TestBatchTranscription:
    def test_batch_happy_path_json(self):
        wav = _make_wav_bytes(duration_sec=0.1)
        resp = client.post(
            "/v1/audio/transcriptions",
            files={"file": ("audio.wav", wav, "audio/wav")},
            data={"model": "v3_rnnt", "response_format": "json"},
        )
        assert resp.status_code == 200
        body = resp.json()
        assert body["text"] == "привет мир"
        assert body["usage"]["type"] == "duration"
        assert body["usage"]["seconds"] > 0

    def test_batch_text_format(self):
        wav = _make_wav_bytes(duration_sec=0.05)
        resp = client.post(
            "/v1/audio/transcriptions",
            files={"file": ("audio.wav", wav, "audio/wav")},
            data={"response_format": "text"},
        )
        assert resp.status_code == 200
        assert resp.headers["content-type"].startswith("text/plain")
        assert resp.text == "привет мир"

    def test_batch_invalid_format_returns_400(self):
        wav = _make_wav_bytes()
        resp = client.post(
            "/v1/audio/transcriptions",
            files={"file": ("audio.wav", wav, "audio/wav")},
            data={"response_format": "xml"},
        )
        assert resp.status_code == 400


# ---------------------------------------------------------------------------
# Streaming WebSocket — GATED OFF (GigaAM is batch-only)
# ---------------------------------------------------------------------------

class TestStreamingGatedOff:
    def test_ws_rejects_with_reason_then_closes(self):
        with client.websocket_connect("/v1/asr/stream") as ws:
            msg = json.loads(ws.receive_text())
            assert msg["type"] == "error"
            assert "stream" in msg["detail"].lower()
            with pytest.raises(WebSocketDisconnect):
                ws.receive_text()


# ---------------------------------------------------------------------------
# Health / models
# ---------------------------------------------------------------------------

class TestHealthAndModels:
    def test_health_returns_ok(self):
        resp = client.get("/health")
        assert resp.status_code == 200
        body = resp.json()
        assert body["status"] == "ok"
        assert body["model_loaded"] is True

    def test_models_endpoint(self):
        resp = client.get("/v1/models")
        assert resp.status_code == 200
        body = resp.json()
        assert body["object"] == "list"
        assert body["data"][0]["id"] == "v3_rnnt"

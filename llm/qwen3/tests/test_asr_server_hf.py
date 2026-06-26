# Run with: ASR_SKIP_MODEL_LOAD=1 .venv-test/bin/pytest tests/test_asr_server_hf.py -v
# from llms/llm/qwen3-asr/ (same light test deps as tests/requirements-dev.txt).
"""
Contract tests for asr_server_hf.py — the transformers-backend, BATCH-ONLY variant.

The real Qwen3-ASR model needs a GPU and is not loaded here. A FakeModel is injected into
asr_server_hf._MODEL; ASR_SKIP_MODEL_LOAD=1 prevents the lifespan hook from importing
qwen_asr/torch. These tests assert the batch endpoint behaves exactly like asr_server.py's
AND that the streaming WebSocket endpoint is gated off on this backend.
"""
from __future__ import annotations

import io
import json
import os
import sys
import wave

import pytest

# Ensure the module directory is on the path so `import asr_server_hf` works.
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

os.environ["ASR_SKIP_MODEL_LOAD"] = "1"


class FakeModel:
    """Minimal stand-in for qwen_asr.Qwen3ASRModel.from_pretrained — batch path only.

    The transformers backend has no streaming-state methods, so (unlike the vLLM FakeModel)
    this deliberately implements ONLY transcribe().
    """

    def transcribe(self, audio, language=None):
        class _R:
            text = "hello world"

        return [_R()]


import asr_server_hf  # noqa: E402  (side-effect: defines `app`)

asr_server_hf._MODEL = FakeModel()

from fastapi.testclient import TestClient  # noqa: E402
from starlette.websockets import WebSocketDisconnect  # noqa: E402

client = TestClient(asr_server_hf.app, raise_server_exceptions=True)


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
# Batch transcription — same contract as asr_server.py
# ---------------------------------------------------------------------------

class TestBatchTranscription:
    def test_batch_happy_path_json(self):
        wav = _make_wav_bytes(duration_sec=0.1)
        resp = client.post(
            "/v1/audio/transcriptions",
            files={"file": ("audio.wav", wav, "audio/wav")},
            data={"model": "Qwen/Qwen3-ASR-1.7B", "response_format": "json"},
        )
        assert resp.status_code == 200
        body = resp.json()
        assert body["text"] == "hello world"
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
        assert resp.text == "hello world"

    def test_batch_invalid_format_returns_400(self):
        wav = _make_wav_bytes()
        resp = client.post(
            "/v1/audio/transcriptions",
            files={"file": ("audio.wav", wav, "audio/wav")},
            data={"response_format": "xml"},
        )
        assert resp.status_code == 400


# ---------------------------------------------------------------------------
# Streaming WebSocket — GATED OFF on the transformers backend
# ---------------------------------------------------------------------------

class TestStreamingGatedOff:
    def test_ws_rejects_with_reason_then_closes(self):
        """Connecting to /v1/asr/stream returns a single error frame explaining streaming is
        unavailable, then the server closes the connection."""
        with client.websocket_connect("/v1/asr/stream") as ws:
            msg = json.loads(ws.receive_text())
            assert msg["type"] == "error"
            assert "stream" in msg["detail"].lower()
            # Server closes right after the error frame.
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
        assert body["data"][0]["id"] == "Qwen/Qwen3-ASR-1.7B"

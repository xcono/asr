# Run with: ASR_SKIP_MODEL_LOAD=1 .venv-test/bin/pytest tests/test_ws_protocol.py -v
# from llms/llm/qwen3-asr/ after: uv venv .venv-test && uv pip install --python .venv-test
#   fastapi "uvicorn[standard]" httpx numpy soundfile python-multipart pytest
"""
Protocol tests for asr_server.py WebSocket streaming endpoint.

The real Qwen3-ASR model requires a GPU + vLLM and cannot be loaded here.
A FakeModel is injected into asr_server._MODEL before the test app is created.
ASR_SKIP_MODEL_LOAD=1 prevents the lifespan hook from importing / loading qwen_asr.
"""
from __future__ import annotations

import io
import json
import os
import struct
import sys
import wave

import numpy as np
import pytest

# Ensure the module directory is on the path so `import asr_server` works.
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

os.environ["ASR_SKIP_MODEL_LOAD"] = "1"

# ---------------------------------------------------------------------------
# Fake model
# ---------------------------------------------------------------------------

class FakeStreamingState:
    def __init__(self):
        self.text = ""
        self.language = "en"


class FakeModel:
    """Minimal stand-in for qwen_asr.Qwen3ASRModel.LLM."""

    def init_streaming_state(self, **kwargs) -> FakeStreamingState:
        return FakeStreamingState()

    def streaming_transcribe(self, pcm: np.ndarray, state: FakeStreamingState) -> None:
        # Appends a word per call so cumulative text grows.
        words = ("hello", "world", "foo", "bar")
        idx = state.text.count(" ") if state.text else 0
        new_word = words[idx % len(words)]
        state.text = (state.text + " " + new_word).strip()

    def finish_streaming_transcribe(self, state: FakeStreamingState) -> None:
        # Optionally polish; here we just leave text as-is.
        pass

    def transcribe(self, audio, language=None):
        """Batch path: returns a list of result objects with a .text attribute."""

        class _R:
            text = "hello world"

        return [_R()]


# ---------------------------------------------------------------------------
# Test app fixture — inject fake model BEFORE importing the FastAPI app
# ---------------------------------------------------------------------------

import asr_server  # noqa: E402  (side-effect: defines `app`)

asr_server._MODEL = FakeModel()

from fastapi.testclient import TestClient  # noqa: E402

client = TestClient(asr_server.app, raise_server_exceptions=True)


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def _make_pcm_frame(n_samples: int = 160) -> bytes:
    """Return n_samples * 2 bytes of silent 16-bit LE PCM (valid even-length frame)."""
    return bytes(n_samples * 2)


def _make_wav_bytes(duration_sec: float = 0.1, sample_rate: int = 16000) -> bytes:
    """Generate a minimal mono 16-bit WAV file in memory."""
    n_samples = int(sample_rate * duration_sec)
    buf = io.BytesIO()
    with wave.open(buf, "wb") as wf:
        wf.setnchannels(1)
        wf.setsampwidth(2)
        wf.setframerate(sample_rate)
        wf.writeframes(b"\x00\x00" * n_samples)
    return buf.getvalue()


# ---------------------------------------------------------------------------
# WebSocket streaming tests
# ---------------------------------------------------------------------------

class TestWebSocketStreamingProtocol:
    """Tests for POST /v1/asr/stream WebSocket endpoint."""

    def test_connect_receive_partials_then_finish(self):
        """Connect, send two PCM binary frames, receive growing partial transcripts,
        send finish control message, receive final transcript, connection closes."""
        with client.websocket_connect("/v1/asr/stream") as ws:
            # --- frame 1 ---
            ws.send_bytes(_make_pcm_frame(160))
            msg1 = json.loads(ws.receive_text())
            assert msg1["type"] == "partial"
            assert isinstance(msg1["text"], str)
            assert isinstance(msg1["language"], str)
            text_after_frame1 = msg1["text"]
            assert len(text_after_frame1) > 0, "Expected non-empty partial after first frame"

            # --- frame 2 ---
            ws.send_bytes(_make_pcm_frame(160))
            msg2 = json.loads(ws.receive_text())
            assert msg2["type"] == "partial"
            text_after_frame2 = msg2["text"]
            # Cumulative text should grow (FakeModel appends a word each call)
            assert len(text_after_frame2) >= len(text_after_frame1), (
                f"Expected cumulative text to grow: {text_after_frame1!r} -> {text_after_frame2!r}"
            )

            # --- finish ---
            ws.send_text(json.dumps({"type": "finish"}))
            msg3 = json.loads(ws.receive_text())
            assert msg3["type"] == "final"
            assert isinstance(msg3["text"], str)
            # After finish the server should close; receive_text would raise or return nothing.
            # We don't assert on the close code here as TestClient behaviour varies.

    def test_empty_binary_frame_returns_partial(self):
        """An empty binary message (0 bytes) is a valid no-op frame and returns a partial."""
        with client.websocket_connect("/v1/asr/stream") as ws:
            ws.send_bytes(b"")
            msg = json.loads(ws.receive_text())
            assert msg["type"] == "partial"

    def test_odd_length_pcm_returns_error_not_crash(self):
        """Odd-length PCM frame (not a multiple of 2) returns an error JSON, not a crash."""
        with client.websocket_connect("/v1/asr/stream") as ws:
            ws.send_bytes(b"\x00" * 3)  # 3 bytes — odd
            msg = json.loads(ws.receive_text())
            assert msg["type"] == "error"
            assert "odd" in msg["detail"].lower() or "multiple" in msg["detail"].lower() or "2" in msg["detail"]

    def test_finish_without_chunks_returns_final(self):
        """Sending finish immediately (no prior chunks) still returns a valid final message."""
        with client.websocket_connect("/v1/asr/stream") as ws:
            ws.send_text(json.dumps({"type": "finish"}))
            msg = json.loads(ws.receive_text())
            assert msg["type"] == "final"
            assert "text" in msg

    def test_unknown_control_message_returns_error(self):
        """An unknown control message type returns an error JSON."""
        with client.websocket_connect("/v1/asr/stream") as ws:
            ws.send_text(json.dumps({"type": "bogus"}))
            msg = json.loads(ws.receive_text())
            assert msg["type"] == "error"


# ---------------------------------------------------------------------------
# Batch transcription tests
# ---------------------------------------------------------------------------

class TestBatchTranscription:
    """Tests for POST /v1/audio/transcriptions (unchanged endpoint)."""

    def test_batch_happy_path_json(self):
        """A WAV upload returns a JSON body with 'text' and 'usage' fields."""
        wav = _make_wav_bytes(duration_sec=0.1)
        resp = client.post(
            "/v1/audio/transcriptions",
            files={"file": ("audio.wav", wav, "audio/wav")},
            data={"model": "Qwen/Qwen3-ASR-0.6B", "response_format": "json"},
        )
        assert resp.status_code == 200
        body = resp.json()
        assert "text" in body
        assert "usage" in body
        assert body["usage"]["type"] == "duration"

    def test_batch_text_format(self):
        """response_format=text returns a plain-text response."""
        wav = _make_wav_bytes(duration_sec=0.05)
        resp = client.post(
            "/v1/audio/transcriptions",
            files={"file": ("audio.wav", wav, "audio/wav")},
            data={"response_format": "text"},
        )
        assert resp.status_code == 200
        assert resp.headers["content-type"].startswith("text/plain")

    def test_batch_invalid_format_returns_400(self):
        """response_format=xml (unsupported) returns 400."""
        wav = _make_wav_bytes()
        resp = client.post(
            "/v1/audio/transcriptions",
            files={"file": ("audio.wav", wav, "audio/wav")},
            data={"response_format": "xml"},
        )
        assert resp.status_code == 400


# ---------------------------------------------------------------------------
# Health / models endpoints
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
        assert len(body["data"]) >= 1
        assert "id" in body["data"][0]

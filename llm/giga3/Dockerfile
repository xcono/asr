# GigaAM OpenAI-compatible STT server — the DEFAULT batch ASR (replaces Qwen3-ASR as `stt`).
#
# Entrypoint is asr_server.py — one in-process gigaam.load_model() (SberDevices GigaAM v3,
# Conformer, MIT). It exposes the SAME contract as llm/qwen3-asr:
#   POST /v1/audio/transcriptions   (OpenAI-compatible batch — identical wire format)
#   WS   /v1/asr/stream             GATED OFF (GigaAM has no streaming state)
#   GET  /health   GET /v1/models
# so the Go pkg/asr client and `make up/run/act/loopback` need NO changes — only the model swaps.
#
# WHY A SEPARATE IMAGE (vs the multilingual Qwen3-ASR)
#   GigaAM v3 is the open SOTA for Russian and ~8x smaller than Qwen3-ASR-1.7B, but it is
#   Russian-ONLY and batch-only. For a multilingual or streaming path, build llm/qwen3-asr
#   instead (the streaming overlay docker-compose.stream.yaml already pins that context).
#
# BLACKWELL NOTE: the RTX 5060 Ti is sm_120 → needs cu128 torch wheels. We install them FIRST,
# then GigaAM from git WITHOUT its [torch] extra so it resolves against the torch we pinned
# (no downgrade) — same strategy as llm/qwen3-asr/Dockerfile.hf.
#
# Weights are NOT baked in: gigaam.load_model() streams the checkpoint from the Sber CDN on
# first request into GIGAAM_DOWNLOAD_ROOT (mounted on the hf-cache volume → persists across
# restarts). No Hugging Face token required.
#
# HEADLESS PREP: validated on the GPU host (NVIDIA Container Toolkit required).
# DO NOT build here in a sandbox — no GPU.

FROM nvidia/cuda:12.8.1-cudnn-runtime-ubuntu22.04

ENV DEBIAN_FRONTEND=noninteractive PYTHONUNBUFFERED=1
RUN apt-get update && apt-get install -y --no-install-recommends \
        python3 python3-pip python3-dev build-essential ffmpeg curl ca-certificates git \
    && rm -rf /var/lib/apt/lists/*

# cu128 torch wheels first (Blackwell / sm_120), then GigaAM from git WITHOUT [torch] so it
# resolves transformers/onnxruntime/etc. against the torch above (no downgrade).
RUN python3 -m pip install --no-cache-dir --upgrade pip \
    && python3 -m pip install --no-cache-dir \
        torch==2.9.1 torchaudio==2.9.1 \
        --index-url https://download.pytorch.org/whl/cu128 \
    && python3 -m pip install --no-cache-dir \
        "gigaam @ git+https://github.com/salute-developers/GigaAM.git"

RUN pip3 install --no-cache-dir fastapi "uvicorn[standard]" python-multipart soundfile numpy

# v3_rnnt (lowest WER, raw text) is the default feed for the LLM answer tier; override
# GIGAAM_MODEL=v3_e2e_rnnt for punctuation + normalization. DEVICE/DOWNLOAD_ROOT match compose.
ENV GIGAAM_MODEL=v3_rnnt \
    GIGAAM_DEVICE=cuda:0 \
    GIGAAM_DOWNLOAD_ROOT=/root/.cache/gigaam
COPY asr_server.py /app/
EXPOSE 8008

# Single-process FastAPI server as PID 1, holding one GigaAM model and serving batch
# transcriptions on 0.0.0.0:8008. Runtime knobs come from the environment: GIGAAM_MODEL,
# GIGAAM_DEVICE, GIGAAM_FP16, GIGAAM_DOWNLOAD_ROOT, GIGAAM_PORT. See asr_server.py for defaults.
CMD ["python3", "/app/asr_server.py"]

# Qwen3-ASR OpenAI-compatible STT server (batch + WebSocket streaming) via vLLM.
#
# Entrypoint is `asr_server.py` — a single FastAPI app holding one in-process
# qwen_asr.Qwen3ASRModel.LLM (vLLM backend). It exposes:
#   POST /v1/audio/transcriptions   (OpenAI-compatible batch; same contract as before)
#   WS   /v1/asr/stream             WebSocket streaming: binary = 16-bit LE PCM frames
#                                   -> JSON {"type":"partial","text":...,"language":...};
#                                   text {"type":"finish"} -> {"type":"final","text":...}
#                                   then close (1000).
#   GET  /health  GET /v1/models
# Replaces the old two-process setup (qwen-asr-serve subprocess on inner port 8009
# + openai_asr_proxy.py shim).
#
# vLLM is the first-party OpenAI adapter for Qwen3-ASR. Qwen3-ASR is NOT (yet)
# an in-tree vLLM model, so it is registered via the `qwen-asr` PyPI package
# (qwen_asr.Qwen3ASRModel.LLM uses vllm.LLM under the hood).
#   pip install -U "qwen-asr[vllm]"   ->   vllm==0.14.0  ->  torch==2.9.1
#
# CUDA 12.8 base -- required for Blackwell GPUs (RTX 50xx, incl. the 5060 Ti, sm_120).
# Ubuntu 22.04's default python3 is 3.10, which satisfies qwen-asr (>=3.9) and
# vllm 0.14.0 (>=3.10,<3.14), so we use the distro python.
#
# DEVIATION FROM THE PLAN'S STARTING-POINT DOCKERFILE:
#   - Base is `nvidia/cuda:12.8.1-cudnn-runtime-ubuntu22.04` (not `vllm/vllm-openai:latest`):
#     the prebuilt vllm-openai image pins a newer vLLM (v0.20.x) that does not contain
#     Qwen3-ASR, and `pip install qwen-asr[vllm]` would try to *downgrade* it to
#     vllm==0.14.0 -- fragile. Building on the CUDA base mirrors qwen3-tts/Dockerfile
#     and lets pip install vllm 0.14.0 + torch 2.9.1 (cu128) cleanly.
#   - torch/torchaudio/torchvision are installed first from the cu128 wheel index so we
#     get the Blackwell-capable build; `qwen-asr[vllm]`'s torch==2.9.1 pin is then a no-op.
#   - The `language <DetectedLanguage><asr_text>` preamble that the raw vLLM inference
#     emits (QwenLM/Qwen3-ASR#71, vllm-project/vllm#33768) is now stripped by
#     qwen_asr's high-level transcribe/streaming API; asr_server.py adds a defensive
#     _strip_preamble just in case.

FROM nvidia/cuda:12.8.1-cudnn-runtime-ubuntu22.04

ENV DEBIAN_FRONTEND=noninteractive PYTHONUNBUFFERED=1
RUN apt-get update && apt-get install -y --no-install-recommends \
        python3 python3-pip python3-dev build-essential ffmpeg libsox-dev sox curl ca-certificates git \
    && rm -rf /var/lib/apt/lists/*

# cu128 torch wheels (torch 2.9.1 ships cu128; required for sm_120 / Blackwell).
# qwen-asr[vllm] pins exactly these versions, so installing them up front from the
# cu128 index keeps the wheel index correct without a later resolver downgrade.
RUN python3 -m pip install --no-cache-dir --upgrade pip \
    && python3 -m pip install --no-cache-dir \
        torch==2.9.1 torchaudio==2.9.1 torchvision==0.24.1 \
        --index-url https://download.pytorch.org/whl/cu128 \
    && python3 -m pip install --no-cache-dir "qwen-asr[vllm]"

RUN pip3 install --no-cache-dir fastapi "uvicorn[standard]" python-multipart soundfile numpy

# ASR_LANGUAGE sets the default forced transcription language (a Qwen3-ASR language *name*).
# "Russian" is primary; set to "auto" (or override per request / per WS ?language=) for
# English or full auto-detection. Override in llms/docker-compose.yml as needed.
ENV HF_HOME=/root/.cache/huggingface \
    ASR_LANGUAGE=Russian
COPY asr_server.py /app/
EXPOSE 8008

# Run the single-process FastAPI server as PID 1. It holds one in-process
# qwen_asr.Qwen3ASRModel.LLM (vLLM backend) and serves batch transcriptions
# (/v1/audio/transcriptions) plus a WebSocket streaming endpoint (/v1/asr/stream)
# on 0.0.0.0:8008. Runtime VRAM knobs come from the environment (set in
# llms/docker-compose.yml): ASR_ENFORCE_EAGER=1 + ASR_GPU_MEM_UTIL=0.32 (~5 GB,
# eager/no CUDA graphs) so it coexists with qwen3-tts (~3.2 GB) and Ollama's
# qwen3:4b-instruct (~3-4 GB) on the one 16 GB RTX 5060 Ti. See llms/.env.example.
CMD ["python3", "/app/asr_server.py"]

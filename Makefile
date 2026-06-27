# Makefile for the vox service.
#
# Standalone ASR/STT Go service for a realtime voice agent.
# Emits VAD and transcription events over NATS Jetstream (embedded).

# --- Dependency versions (overridable) --------------------------------------
ORT_VERSION ?= 1.18.1
# Silero VAD v5 ONNX model. The fork's cgo bridge pins the v5 layout
# (stateLen = 2*1*128, contextLen = 64); v6 changed the state shape, so do
# not bump SILERO_VERSION without updating the bridge expectations first.
SILERO_VERSION ?= v5.1.2

# --- ONNX Runtime env for the cgo `vad` build ------------------------------
ORT ?= $(HOME)/.local/onnxruntime-linux-x64-$(ORT_VERSION)
export C_INCLUDE_PATH := $(ORT)/include
export LIBRARY_PATH   := $(ORT)/lib
export LD_RUN_PATH    := $(ORT)/lib

# --- VAD model -------------------------------------------------------------
# Default config.json points at pkg/vad/silero_vad.onnx. The file is
# gitignored (*.onnx); run `make deps-model` to fetch it.
MODEL_PATH ?= pkg/vad/silero_vad.onnx

.DEFAULT_GOAL := run

# ====== build / run =========================================================

## run: start the vox service (loads config.json from working directory)
.PHONY: run
run: check-ort check-model
	go run ./cmd/vox

## build: compile to ./bin/vox (rpath baked via LD_RUN_PATH)
.PHONY: build
build: check-ort
	go build -o bin/vox ./cmd/vox

## test: run the Go test suite
.PHONY: test
test: check-ort
	go test ./...

# ====== model server (docker compose) ======================================
# The Python STT server on :8008 — two interchangeable backends as standalone composes.
# `up` = GigaAM (batch, ru-only); `up-qwen` = Qwen3-ASR (streaming WS /v1/asr/stream,
# multilingual). DO NOT build/up in a sandbox — needs the GPU box + NVIDIA toolkit.

## up: start the default GigaAM batch STT server (:8008)
.PHONY: up
up:
	docker compose -f docker.giga.yaml up -d

## up-qwen: start the Qwen3-ASR streaming STT server (:8008, WS /v1/asr/stream)
.PHONY: up-qwen
up-qwen:
	docker compose -f docker.qwen.yaml up -d

## down: stop the STT server (either backend — same `asr` project)
.PHONY: down
down:
	docker compose -f docker.giga.yaml down

# ====== deps ===============================================================
# One-shot: `make deps` fetches PortAudio headers, ONNX Runtime, and the
# Silero VAD model. Override ORT_VERSION / SILERO_VERSION / ORT / MODEL_PATH
# to retarget.

## deps: install all build/runtime deps (PortAudio, ONNX Runtime, Silero VAD model)
.PHONY: deps
deps: deps-portaudio deps-ort deps-model

## deps-portaudio: install PortAudio dev headers (Debian/Ubuntu; needs sudo)
.PHONY: deps-portaudio
deps-portaudio:
	@command -v apt-get >/dev/null 2>&1 || { \
	  echo "ERROR: apt-get not found — install portaudio19-dev via your distro's package manager"; \
	  exit 1; }
	sudo apt-get update
	sudo apt-get install -y portaudio19-dev

## deps-ort: download + extract ONNX Runtime to $(ORT) (idempotent)
.PHONY: deps-ort
deps-ort:
	@if [ -e "$(ORT)/lib/libonnxruntime.so" ]; then \
	  echo "ORT $(ORT_VERSION) already at $(ORT)"; \
	else \
	  echo "Downloading ONNX Runtime $(ORT_VERSION) -> $(ORT) ..."; \
	  mkdir -p $(HOME)/.local; \
	  cd /tmp && wget -q https://github.com/microsoft/onnxruntime/releases/download/v$(ORT_VERSION)/onnxruntime-linux-x64-$(ORT_VERSION).tgz; \
	  cd /tmp && tar xf onnxruntime-linux-x64-$(ORT_VERSION).tgz; \
	  mv /tmp/onnxruntime-linux-x64-$(ORT_VERSION) $(ORT); \
	  rm -f /tmp/onnxruntime-linux-x64-$(ORT_VERSION).tgz; \
	  echo "Installed ONNX Runtime $(ORT_VERSION) to $(ORT)"; \
	fi

## deps-model: fetch the Silero VAD v5 ONNX model to $(MODEL_PATH) (idempotent)
##
## Primary path: copy from the go modcache — the fork ships a copy in its
## testfiles/ (byte-identical to the v5.1.2 model from snakers4/silero-vad).
## Fallback: wget from the official snakers4/silero-vad tag. `install` is used
## instead of `cp` so the model lands at mode 0644 (the modcache copy is
## read-only, which `cp` would otherwise preserve).
.PHONY: deps-model
deps-model:
	@if [ -e "$(MODEL_PATH)" ]; then \
	  echo "Silero VAD model already at $(MODEL_PATH)"; \
	else \
	  mkdir -p $(dir $(MODEL_PATH)); \
	  fork_dir=`go list -m -f '{{.Dir}}' github.com/streamer45/silero-vad-go 2>/dev/null`; \
	  if [ -n "$$fork_dir" ] && [ -f "$$fork_dir/testfiles/silero_vad.onnx" ]; then \
	    install -m 0644 "$$fork_dir/testfiles/silero_vad.onnx" "$(MODEL_PATH)"; \
	    echo "Copied Silero VAD model from go modcache ($$fork_dir) -> $(MODEL_PATH)"; \
	  else \
	    echo "Downloading Silero VAD $(SILERO_VERSION) model -> $(MODEL_PATH) ..."; \
	    wget -q -O "$(MODEL_PATH)" https://github.com/snakers4/silero-vad/raw/$(SILERO_VERSION)/src/silero_vad/data/silero_vad.onnx; \
	    chmod 0644 "$(MODEL_PATH)"; \
	    echo "Downloaded Silero VAD $(SILERO_VERSION) model -> $(MODEL_PATH)"; \
	  fi; \
	fi

# ====== guards / helpers ====================================================

# Fail early with a clear message if ORT is missing (vs a cryptic linker error).
.PHONY: check-ort
check-ort:
	@test -e "$(ORT)/lib/libonnxruntime.so" || { \
	  echo "ERROR: ONNX Runtime not found at $(ORT)/lib/libonnxruntime.so"; \
	  echo "  install with: make deps-ort"; \
	  echo "  (or pin a version: make ORT_VERSION=1.18.1 deps-ort)"; \
	  echo "  or point ORT at an existing install: make ORT=/path/to/it"; \
	  exit 1; }

# Fail early if the Silero VAD model is missing — avoids an opaque runtime
# error from go-onnxruntime session creation.
.PHONY: check-model
check-model:
	@test -e "$(MODEL_PATH)" || { \
	  echo "ERROR: Silero VAD model not found at $(MODEL_PATH)"; \
	  echo "  install with: make deps-model"; \
	  echo "  (or point MODEL_PATH at an existing model in config.json)"; \
	  echo "  note: the fork's cgo bridge expects the v5 model layout"; \
	  exit 1; }

## env: print the resolved build env
.PHONY: env
env:
	@echo "ORT_VERSION  = $(ORT_VERSION)"
	@echo "ORT         = $(ORT)"
	@echo "MODEL_PATH  = $(MODEL_PATH)  (Silero VAD $(SILERO_VERSION))"

## help: list documented targets
.PHONY: help
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /'
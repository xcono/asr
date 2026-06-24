# Makefile for the vox service.
#
# Standalone ASR/STT Go service for a realtime voice agent.
# Emits VAD and transcription events over NATS Jetstream (embedded).

# --- ONNX Runtime env for the cgo `vad` build ------------------------------
ORT ?= $(HOME)/.local/onnxruntime-linux-x64-1.18.1
export C_INCLUDE_PATH := $(ORT)/include
export LIBRARY_PATH   := $(ORT)/lib
export LD_RUN_PATH    := $(ORT)/lib

.DEFAULT_GOAL := run

# ====== build / run =========================================================

## run: start the vox service (loads config.json from working directory)
.PHONY: run
run: check-ort
	go run ./cmd/vox

## build: compile to ./bin/vox (rpath baked via LD_RUN_PATH)
.PHONY: build
build: check-ort
	go build -o bin/vox ./cmd/vox

## test: run the Go test suite
.PHONY: test
test: check-ort
	go test ./...

# ====== guards / helpers ====================================================

# Fail early with a clear message if ORT is missing (vs a cryptic linker error).
.PHONY: check-ort
check-ort:
	@test -e "$(ORT)/lib/libonnxruntime.so" || { \
	  echo "ERROR: ONNX Runtime not found at $(ORT)/lib/libonnxruntime.so"; \
	  echo "  install onnxruntime-linux-x64-1.18.1, or point ORT at it: make ORT=/path/to/it"; \
	  exit 1; }

## env: print the resolved build env
.PHONY: env
env:
	@echo "ORT = $(ORT)"

## help: list documented targets
.PHONY: help
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /'
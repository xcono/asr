# Makefile for the vox service.
#
# Builds cmd/vox, the standalone debug binary of the asr module (Silero VAD +
# STT for a realtime voice agent). Logs VAD and transcription events to stdout;
# the module is transport-free (events flow over Go channels only).

.DEFAULT_GOAL := run

# ====== build / run =========================================================

## run: start the vox service (loads config.json from working directory)
.PHONY: run
run:
	go run ./cmd/vox

## build: compile to ./bin/vox
.PHONY: build
build:
	go build -o bin/vox ./cmd/vox

## test: run the Go test suite
.PHONY: test
test:
	go test ./...

# ====== model server (docker compose) ======================================
# The STT server on :8008 — three interchangeable backends as standalone composes; run ONE.
# `up` = GigaAM (batch, ru-only); `up-qwen` = Qwen3-ASR (streaming WS /v1/asr/stream,
# multilingual); `up-whisper` = Whisper large-v3-turbo on whisper.cpp (batch, multilingual).
# DO NOT build/up in a sandbox — needs the GPU box + NVIDIA toolkit.

## up: start the default GigaAM batch STT server (:8008)
.PHONY: up
up:
	docker compose -f docker.giga.yaml up -d

## up-qwen: start the Qwen3-ASR streaming STT server (:8008, WS /v1/asr/stream)
.PHONY: up-qwen
up-qwen:
	docker compose -f docker.qwen.yaml up -d

## up-whisper: start the Whisper large-v3-turbo batch STT server (:8008, whisper.cpp)
.PHONY: up-whisper
up-whisper:
	docker compose -f docker.whisper.yaml up -d

## down: stop the GigaAM or Qwen STT server (same `asr` project)
.PHONY: down
down:
	docker compose -f docker.giga.yaml down

## down-whisper: stop the Whisper STT server (keeps the model volume)
.PHONY: down-whisper
down-whisper:
	docker compose -f docker.whisper.yaml down

# ====== deps ===============================================================
# The only native dependency is PortAudio (mic capture). The Silero VAD runs in
# pure Go with its weights embedded in the binary — no ONNX Runtime, no model
# file.

## deps: install build/runtime deps (PortAudio headers)
.PHONY: deps
deps: deps-portaudio

## deps-portaudio: install PortAudio dev headers (Debian/Ubuntu; needs sudo)
.PHONY: deps-portaudio
deps-portaudio:
	@command -v apt-get >/dev/null 2>&1 || { \
	  echo "ERROR: apt-get not found — install portaudio19-dev via your distro's package manager"; \
	  exit 1; }
	sudo apt-get update
	sudo apt-get install -y portaudio19-dev

## help: list documented targets
.PHONY: help
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /'
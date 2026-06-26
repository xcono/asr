# Spec 01 — Bump go.mod directive to go 1.25

## Context
`pkg/asr/listen.go:117` uses `(*sync.WaitGroup).Go`, added in Go 1.25.
`go.mod` declares `go 1.24.0`, so `go vet ./...` reports:
`sync.Go requires go1.25 or later (file is go1.24)`. It only builds because
the host toolchain is go1.26.4; a consumer on a 1.24 toolchain fails to build.

## Goal
Align the module's declared Go version with the language features it uses.

## Changes
1. Edit `go.mod`: change `go 1.24.0` → `go 1.25.0`.
   - Do NOT add a `toolchain` line — let the user's installed Go drive toolchain
     selection. The single `go` directive is the source of truth.

## Verification
- `go vet ./...` exits 0 on the `asr` package's `sync.Go` check
  (the only vet failure it currently raises).
- `go build ./...` still succeeds for non-cgo packages. The cgo `vad` package
  will not build without ORT; that is unrelated and expected.
- `go test ./pkg/asr/... ./pkg/config/... ./pkg/nats/... ./pkg/audio/...`
  still passes.

## Out of scope
- Upgrading any dependencies.
- Adding a `toolchain` directive.
- Touching `go.sum`.
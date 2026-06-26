# Spec 02 — Harden vad.Model.Infer against short windows

## Context
`pkg/vad/model.go:33-36` only overwrites `m.buf[:len(window)]`. A window
shorter than `vad.Window` (512) leaves the previous window's tail in the
remainder of `m.buf`, silently producing wrong speech probabilities.

The 16 kHz / 512-sample window is Silero's native contract and is enforced
upstream in the pipeline, so today this is unreachable. The doc comment
admits the hazard ("a shorter window leaves the tail of the previous
buffer"). Make a misuse fail loudly instead of producing wrong output.

## Goal
Any call to `Infer` with a window whose length != `vad.Window` returns an
explicit error instead of running on stale-tail data.

## Changes
File: `pkg/vad/model.go`

1. At the top of `(*Model).Infer`, add a length check that returns a wrapped
   error when `len(window) != Window`:

   ```go
   if len(window) != Window {
       return 0, fmt.Errorf("vad: infer window len %d != %d", len(window), Window)
   }
   ```

2. Add the `"fmt"` import if not already present.

## Constraints
- Do NOT reset / zero `m.buf`. The recurrent state contract documented in
  `model.go` (never Reset between windows of one stream) still holds.
- Do NOT change the int16 → float32 normalisation (`float32(s) / 32768`).
- Keep the existing doc comment; you may extend it with one line noting the
  length contract is now enforced.

## Verification
- `go vet ./pkg/vad/...` (or at minimum `go build ./pkg/vad/...`) compiles.
  Note: the `vad` package is cgo and needs ORT to build — if ORT is
  unavailable, at least confirm via `gofmt`/inspection that the file is
  syntactically valid and imports resolve.
- The existing pipeline always passes `vad.Window` samples, so no caller
  needs updating.

## Out of scope
- Changing `m.buf` allocation strategy.
- Adding a test (cgo/ORT-dependent; the `vad` package has no model-level
  tests today and adding one would require ORT).
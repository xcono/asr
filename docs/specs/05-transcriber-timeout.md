# Spec 05 — Add an HTTP timeout to Transcriber

## Context
`pkg/asr/batch.go:56-60` falls back to `http.DefaultClient` when
`tr.Client == nil`. `http.DefaultClient` has no `Timeout`, so a hung STT
server keeps the transcription goroutine alive until the listen `ctx`
cancels (which only happens at process shutdown). A hung dependency turns
into a leaked goroutine + an unbounded request.

## Goal
Give `Transcriber` a sensible default HTTP deadline that bounds any single
request independently of the process lifetime, while still allowing callers
to override via `tr.Client` for tests.

## Changes
File: `pkg/asr/batch.go`

1. Add a default request timeout constant near the struct:

   ```go
   // DefaultTimeout caps a single Transcribe HTTP request so a hung STT
   // server cannot hold a transcription goroutine indefinitely. Override via
   // Transcriber.Client.
   const DefaultTimeout = 30 * time.Second
   ```

   30s is generous for a batched utterance POST; big utterances are still
   well under this on a local GigaAM server, and a remote hang fails fast.

2. Choose the client at the top of `Transcribe`:

   ```go
   client := tr.Client
   if client == nil {
       client = &http.Client{Timeout: DefaultTimeout}
   }
   ```

   Do NOT allocate a new client per call when `tr.Client` is non-nil
   (tests pass a shared `*http.Client`; respect it).

## Constraints
- Do NOT add a `time` import if already present (it isn't — add it).
- Do NOT add a per-call `context.WithTimeout` wrapping the caller's `ctx`.
  The request already carries `ctx` via `http.NewRequestWithContext`; the
  `http.Client.Timeout` is a backstop for the case where `ctx` never
  cancels (the listen ctx is process-lifetime). Layering both is fine but
  the client-level timeout is the load-bearing fix.
- Do NOT change multipart assembly, the response decoder, or the error
  formatting (`asr: status %d: %s`).
- Tests pass a `*Transcriber` with only `BaseURL` set (`batch_test.go:42`,
  `batch_test.go:70`): they rely on the nil-client fallback. Confirm
  they still pass — `httptest.Server` responds immediately so the 30s
  timeout never fires.

## Verification
- `go test ./pkg/asr/...` passes (the two batch tests).
- `go vet ./pkg/asr/...` clean.
- A `Transcriber{Client: someClient}` still uses `someClient` unchanged —
  covered by inspection (the nil branch does not run for a non-nil client).

## Out of scope
- Configuring the timeout via `config.json` (the override path is
  `Transcriber.Client`, used by tests and advanced callers).
- Retries / backoff on transient STT failures.
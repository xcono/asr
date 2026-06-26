# vox Refactoring Plan

Outcome of an automated review (context7 NATS docs + `go vet`/`go build`/`go test`
static analysis). Items are ordered by priority: correctness first, then
production hardening, then simplification. Each item maps to a spec in
`docs/specs/` that a subagent can execute in isolation.

## A. Correctness

### A1. Bump `go.mod` directive to `go 1.25`
`pkg/asr/listen.go:117` uses `(*sync.WaitGroup).Go`, added in go 1.25.
`go.mod` declares `go 1.24.0`, so `go vet ./...` reports
`sync.Go requires go1.25 or later (file is go1.24)`. It only builds because
the host toolchain is go1.26.4; a consumer on a 1.24 toolchain fails.

- Spec: `docs/specs/01-go-version-bump.md`

### A2. Harden `vad.Model.Infer` against short windows
`pkg/vad/model.go:33-36` overwrites only `m.buf[:len(window)]`; a shorter
window leaves the previous window's tail in the remainder. Today unreachable
because the pipeline always feeds `vad.Window` samples, but the doc comment
admits the hazard. Add a length assertion so a misuse fails loudly instead of
silently producing wrong probabilities.

- Spec: `docs/specs/02-infer-length-guard.md`

## B. Production best practices (NATS / HTTP)

### B1. Publish through JetStream, not core NATS
`pkg/nats/events.go:53` uses `s.nc.Publish(subject, data)`. Core publish *is*
captured by a JetStream-monitored subject, but the caller loses the publish
acknowledgement, dedup (`Nats-Msg-Id`), and failure visibility that a persisting
events service should have. Migrate the helpers to `s.js.Publish` (sync ack).
Document the change for subscribers (they already consume via `js.Subscribe`,
per the README client example).

- Spec: `docs/specs/03-jetstream-publish.md`

### B2. Add retention limits to the `VAD` and `STT` streams
`pkg/nats/server.go:78-89` creates both streams with the default `LimitsPolicy`
and no `MaxMsgs` / `MaxBytes` / `MaxAge`. A live mic produces events
indefinitely, so the JetStream store in `store_dir` grows unbounded. Set a
sensible `MaxAge` (default 72h) on both streams; make it overridable via
`config.json` so ops can tune per deployment.

- Spec: `docs/specs/04-stream-retention.md`

### B3. Add an HTTP timeout to `Transcriber`
`pkg/asr/batch.go:56-60` falls back to `http.DefaultClient` (no deadline). A
hung STT server keeps the transcription goroutine alive until the listen `ctx`
cancels (= process shutdown). Give `Transcriber` a real client with a default
`Timeout` and wire it from `main.go`.

- Spec: `docs/specs/05-transcriber-timeout.md`

### B4. Surface source / detector errors when closing the listen channel
`pkg/asr/listen.go:90-98` returns on any `src.Read()` / `det.Infer()` error,
closing the event channel silently — no log, no event. For production a
transient detector failure that tears down the whole pipeline with zero
diagnostics is a reliability gap. Add an `Event` variant for a terminal error
(or at minimum log the cause before closing). Keep the contract: the channel
still closes on context cancel / EOF / transport error; the only change is
observability.

- Spec: `docs/specs/06-listen-err-surfacing.md`

### B5. Replace deprecated `nats.Conn.ConnectedUrl()` with `ClientURL()`
~~`cmd/vox/main.go:48` logs `ns.Conn().ConnectedUrl()`. The deprecated accessor
is being removed from nats.go; `ClientURL()` (already used in
`pkg/nats/server.go:38`) is the preferred replacement.~~

**DROPPED** during implementation: verification against
`nats.go@v1.39.1` source showed `ConnectedUrl()` carries no deprecation
comment, and `ClientURL()` is a method on `*server.Server` (not `*nats.Conn`).
The original review premise was wrong; no change needed.

## C. Simplification

### C1. Return `vad.Timing` directly from `config.VADConfig.ToTiming()`
`pkg/config/config.go:110` returns a 6-tuple of positional scalars that
`main.go:58-66` reassembles into `vad.Timing`. The comment justifies the
indirection as avoiding a circular import, but `pkg/vad` imports nothing from
the project — `config` importing `vad` creates no cycle. Returning
`vad.Timing` directly deletes the plumbing and removes a positional-arg
footgun. Update the one caller and the one test.

- Spec: `docs/specs/08-timing-struct-return.md`

## Non-goals (deferred)

The review surfaced these but they are out of scope for this increment:

- Migrate from deprecated `nats.JetStreamContext` to the new `jetstream`
  package. The legacy API still works; a full migration touches every
  publish/consume site and the README client example. Track separately.
- Gate the unused `pkg/asr/stream.go` WebSocket client behind a build tag or
  move to `internal/`. It is implemented + tested and explicitly
  future-use per AGENTS.md; keep until wired.
- `WaitForShutdown` after `ns.Close()` for fully-graceful embedded-server
  teardown. Low risk; revisit if/when JetStream durability guarantees matter
  under SIGTERM.
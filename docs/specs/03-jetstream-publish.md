# Spec 03 — Publish through JetStream, not core NATS

## Context
`pkg/nats/events.go:48-54` publishes via `s.nc.Publish(subject, data)` —
the core NATS connection. Although the server captures core publishes on
JetStream-monitored subjects, the caller loses:

- the JetStream publish acknowledgement (stream + sequence number),
- dedup via `Nats-Msg-Id`,
- a clear failure signal if JetStream rejects the message.

For "a persisting events service" the produce path should go through
JetStream. The README's Go client example already consumes via
`js.Subscribe`, so produce/consume become symmetric.

The legacy `nats.JetStreamContext` API in use (`pkg/nats/server.go:16`)
is deprecated but still fully functional; migrating to the new `jetstream`
package is tracked as a non-goal in `docs/refactoring.md` and is OUT OF
SCOPE for this task. This spec only changes *which* API on the existing
`JetStreamContext` is invoked to publish.

## Goal
Publish all three event kinds through the `JetStreamContext` (`s.js`) with a
sync publish ack, and use a deterministic `Nats-Msg-Id` so a retry of the
same logical event is deduplicated by JetStream instead of stored twice.

## Changes
File: `pkg/nats/events.go`

1. `publish` signature: keep `subject string` and `event interface{}`, add
   an `id string` parameter and call `s.js.Publish(subject, data,
   nats.MsgId(id))` instead of `s.nc.Publish(subject, data)`.

   Signature becomes:
   ```go
   func (s *Server) publish(subject, id string, event interface{}) error
   ```

   `s.js` already exists on `Server` (`pkg/nats/server.go:16`). `nats.MsgId`
   is the existing option helper from the legacy API.

2. Build a stable `Nats-Msg-Id` at each call site so retries dedup:
   - `PublishVADStart(ts)`: id = `vad-start-<ts.UnixNano()>`
   - `PublishVADStop(ts)`:  id = `vad-stop-<ts.UnixNano()>`
   - `PublishMessage(ts, text, voiceFileID)`: id = `stt-<ts.UnixNano()>`

   Rationale: timestamps are the natural unique key per event in this
   service, and `UnixNano()` is monotonically increasing within a process.
   If two events ever share a nanosecond timestamp they would dedup —
   acceptable given event granularity (turn boundaries are seconds apart).

3. Wrap the ack error: `s.js.Publish` returns `(*nats.PubAck, error)`. Return
   `fmt.Errorf("nats: publish %s: %w", subject, err)` on a non-nil error so
   the callers' existing `log.Printf("nats: publish ...: %v", err)` messages
   stay meaningful (the subject is included). Discard the `*PubAck` (not
   needed by callers yet).

## Constraints
- Do NOT change `pkg/nats/server.go` (the `Server` struct already exposes
  `js` through `JS()` and holds the `JetStreamContext`).
- Do NOT touch `cmd/vox/main.go` — its error handling already uses the
  returned `error` and logs it with a per-event prefix; keep that
  behaviour intact.
- Do NOT add a new `nats.Header` or change the JSON marshalling.
- Subscribers in tests (`server_test.go`) use `s.Conn().SubscribeSync(...)`
  (core subscription). Core subscribers still receive JetStream-published
  messages on a server with JetStream enabled, so those tests stay valid.
  Do NOT change the tests unless one breaks.

## Verification
- `go test ./pkg/nats/...` passes (the existing tests subscribe via
  `Conn().SubscribeSync` and expect to receive the published event —
  JetStream publishes are mirrored to core subscribers on the same server,
  so they still arrive).
- `go vet ./pkg/nats/...` clean.
- The publish path now returns a real error if JetStream rejects
  (e.g. no matching stream) — a regression here would surface as a test
  failure, since `setupStreams` creates both `VAD` and `STT` streams
  covering `vox.vad.>` / `vox.stt.>`.

## Out of scope
- Migrating to the new `github.com/nats-io/nats.go/jetstream` package.
- Adding publish retries / dead-letter handling.
- Changing subscribers in `cmd/vox/main.go` (none exist — it only publishes).
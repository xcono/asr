# Spec 04 — Add retention limits to the VAD and STT streams

## Context
`pkg/nats/server.go:78-89` creates the `VAD` and `STT` JetStream streams with
the default `LimitsPolicy` and no `MaxMsgs` / `MaxBytes` / `MaxAge`. A live
mic produces events indefinitely, so the JetStream store in `store_dir`
grows unbounded. A persisting events service must bound its disk usage.

## Goal
Give both streams a default `MaxAge` (messages expire after 72h) so the
store self-trims, and make it overridable per-stream from `config.json`.

## Changes

### 1. `pkg/config/config.go`

Add two fields to `NATSConfig`:

```go
type NATSConfig struct {
    Port     int    `json:"port"`
    StoreDir string `json:"store_dir"`
    // MaxAge truncates persisted events older than this duration.
    // 0 disables the age limit (unbounded). Default 72h.
    VADMaxAge string `json:"vad_max_age"`
    STTMaxAge string `json:"stt_max_age"`
}
```

`setDefaults` — when a MaxAge field is empty, set it to `"72h"`.
(Keep the field type a string so the JSON schema stays simple; parse with
`time.ParseDuration` at the NATS layer.)

### 2. `pkg/nats/server.go`

1. `NewServer` signature gains two `maxAge` durations. Simplest:
   change the signature to accept a `Config`-shaped struct OR two extra
   `time.Duration` params. To keep the call site in `cmd/vox/main.go`
   minimal and the nats package decoupled from `config`, pass two
   `time.Duration` params:

   ```go
   func NewServer(port int, storeDir string, vadMaxAge, sttMaxAge time.Duration) (*Server, error)
   ```

2. In `setupStreams`, pass the durations into `nats.StreamConfig.MaxAge`:

   ```go
   streams := []*nats.StreamConfig{
       {Name: "VAD", Subjects: []string{"vox.vad.>"}, Replicas: 1, MaxAge: vadMaxAge},
       {Name: "STT", Subjects: []string{"vox.stt.>"}, Replicas: 1, MaxAge: sttMaxAge},
   }
   ```

   `nats.StreamConfig.MaxAge` is a `time.Duration`. A zero value disables
   age-based retention — preserving the "explicit opt-out" path.

### 3. `cmd/vox/main.go`

Parse the two config strings and pass them through. In `run`, before
`nats.NewServer`:

```go
vadMaxAge, err := parseMaxAge(cfg.NATS.VADMaxAge, 72*time.Hour)
if err != nil {
    return fmt.Errorf("nats vad_max_age: %w", err)
}
sttMaxAge, err := parseMaxAge(cfg.NATS.STTMaxAge, 72*time.Hour)
if err != nil {
    return fmt.Errorf("nats stt_max_age: %w", err)
}
ns, err := nats.NewServer(cfg.NATS.Port, cfg.NATS.StoreDir, vadMaxAge, sttMaxAge)
```

Add a small helper in `main.go`:

```go
func parseMaxAge(s string, fallback time.Duration) (time.Duration, error) {
    if s == "" {
        return fallback, nil
    }
    d, err := time.ParseDuration(s)
    if err != nil {
        return 0, fmt.Errorf("parse %q: %w", s, err)
    }
    return d, nil
}
```

(`setDefaults` already fills empty with `"72h"`, so the fallback is belt-
and-braces; it lets the helper be unit-safe if defaults change later.)

### 4. Tests

`pkg/nats/server_test.go` — update existing `NewServer(0, t.TempDir())`
call sites to the new signature: `NewServer(0, t.TempDir(), 0, 0)` (zero
durations disable MaxAge — preserves current behaviour for tests that
don't care about retention).

`pkg/config/config_test.go` — extend `TestLoad_Defaults` and
`TestLoad_FullConfig` to assert the two new fields default to `"72h"`
and round-trip from a full config respectively. Add one case to
`TestLoad_FullConfig` setting `VADMaxAge: "24h"`, `STTMaxAge: "48h"` and
assert they survive `Load`.

## Constraints
- Do NOT change the JSON shape of existing fields. Only ADD two new
  optional fields.
- Do NOT set `MaxMsgs` / `MaxBytes` — `MaxAge` is the right default for a
  timestamped event log.
- Keep `Replicas: 1` (single-node embedded server).

## Verification
- `go test ./pkg/config/... ./pkg/nats/...` passes.
- `go vet ./...` clean for these packages.
- A `config.json` with no `vad_max_age` / `stt_max_age` still starts and
  resolves to 72h (covered by the config test).

## Out of scope
- `MaxMsgs` / `MaxBytes` limits.
- Configuring retention policy (`LimitsPolicy` stays the default).
- Migrating to the new `jetstream` package.
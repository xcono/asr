# Spec 08 — Return vad.Timing directly from config.VADConfig.ToTiming()

## Context
`pkg/config/config.go:107-112` defines `ToTiming()` to return a 6-tuple of
positional scalars, which `cmd/vox/main.go:58-66` reassembles into a
`vad.Timing`:

```go
th, hy, bi, rm, eot, pr := cfg.VAD.ToTiming()
timing := vad.Timing{
    Threshold:   th,
    Hysteresis:  hy,
    BargeInMs:   bi,
    ReleaseMs:   rm,
    EndOfTurnMs: eot,
    PrerollMs:   pr,
}
```

The comment on `ToTiming` justifies the indirection as avoiding a circular
import, but `pkg/vad` imports nothing from the project — `config` importing
`vad` creates no cycle. The positional tuple is a footgun (any swap of two
`int` fields silently breaks VAD tuning) for no benefit.

## Goal
Have `VADConfig.ToTiming()` return `vad.Timing` directly; collapse the
caller reassembly.

## Changes

### 1. `pkg/config/config.go`

1. Add the import: `"github.com/xcono/vox/pkg/vad"`.
2. Replace the method:

   ```go
   // ToTiming builds the vad.Timing for the configured VAD parameters. Lives
   // in config (not vad) so all config schema concerns stay in one package.
   func (v *VADConfig) ToTiming() vad.Timing {
       return vad.Timing{
           Threshold:   v.Threshold,
           Hysteresis:  v.Hysteresis,
           BargeInMs:   v.BargeInMs,
           ReleaseMs:   v.ReleaseMs,
           EndOfTurnMs: v.EndOfTurnMs,
           PrerollMs:   v.PrerollMs,
       }
   }
   ```

   The old comment ("This lives here rather than in vad to avoid a circular
   import") is no longer accurate in spirit but the *location* decision is
   still relevant — keep a shorter note explaining the placement is a
   convention (config schema), not a cycle requirement.

### 2. `cmd/vox/main.go`

Replace the reassembly block (`main.go:58-66`) with:

```go
timing := cfg.VAD.ToTiming()
```

Delete the six local vars.

### 3. `pkg/config/config_test.go`

`TestToTiming` (`config_test.go:148-164`) currently unpacks the tuple.
Rewrite it to assert on `vad.Timing` fields:

```go
func TestToTiming(t *testing.T) {
    v := VADConfig{
        Threshold:   0.40,
        Hysteresis:  0.10,
        BargeInMs:   96,
        ReleaseMs:   600,
        EndOfTurnMs: 900,
        PrerollMs:   250,
    }
    got := v.ToTiming()
    assert.Equal(t, float32(0.40), got.Threshold)
    assert.Equal(t, float32(0.10), got.Hysteresis)
    assert.Equal(t, 96, got.BargeInMs)
    assert.Equal(t, 600, got.ReleaseMs)
    assert.Equal(t, 900, got.EndOfTurnMs)
    assert.Equal(t, 250, got.PrerollMs)
}
```

Add the `"github.com/xcono/vox/pkg/vad"` import to the test file.

## Constraints
- Do NOT move the method out of `config` — keep `ToTiming` as a method on
  `*VADConfig` in `pkg/config`.
- Do NOT add any new field to `vad.Timing` — this is a pure plumbing change.
- Behaviour is identical: the struct already has the same six fields in the
  same order, so the constructed `vad.Timing` equals the prior tuple-built
  one for every input.

## Import-cycle check (must verify before declaring done)
- `pkg/vad` currently imports: nothing from the project (only `speech` from
  the external fork). So `pkg/config` → `pkg/vad` adds no cycle.
- `pkg/asr` already imports `pkg/vad` (`listen.go:8`), confirming the
  import graph tolerates this direction.

## Verification
- `go test ./pkg/config/...` passes (updated `TestToTiming`).
- `go build ./cmd/vox` unchanged (modulo cgo/ORT availability).
- `go vet ./pkg/config/... ./cmd/vox/...` clean.
- `go test ./pkg/asr/...` still passes (unaffected; `asr` already imports
  `vad`).

## Out of scope
- Splitting `vad.Timing` into a separate `vadtiming` sub-package.
- Changing the JSON tags on `VADConfig`.
- Merging `DefaultTiming()` concerns into config.
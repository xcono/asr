# Spec 06 — Surface source / detector errors when closing the listen channel

## Context
`pkg/asr/listen.go:89-98` ends the pipeline — and closes the event channel —
silently whenever `src.Read()` or `det.Infer()` returns a non-nil error. For
`io.EOF` on a finite source this is correct (it's the documented shutdown
contract). For any other error (real device failure, ONNX inference failure,
network blip on a streaming source) the pipeline dies with zero observability:
no log, no event, the consumer just sees a closed channel.

## Goal
Distinguish a clean shutdown (`io.EOF` / context cancel) from an unexpected
error, and surface the latter to the consumer before closing the channel.
Preserve the existing contract: the channel still closes on all of those
paths; the only addition is one terminal event.

## Changes
File: `pkg/asr/listen.go`

1. Add a new `EventKind`:

   ```go
   SpeechError // terminal pipeline error surfaced before the channel closes
   ```

   Add it to the `EventKind.String()` switch (return `"SpeechError"`).
   Extend the doc comment on `EventKind` to note it's terminal.

2. Extend `Event` with one optional field (defaulting to empty, so existing
   consumers ignore it):

   ```go
   type Event struct {
       Kind        EventKind
       Text        string
       Timestamp   time.Time
       VoiceFileID string
       Err         error // populated for SpeechError
   }
   ```

   Document that `Err` is only set for `SpeechError`.

3. In `Listen`, factor the early-return paths so a non-EOF, non-cancel error
   emits one `SpeechError` event. Concretely:

   - For `src.Read()` error:
     ```go
     frame, err := src.Read()
     if err != nil {
         if err != io.EOF {
             emit(ctx, out, Event{Kind: SpeechError, Timestamp: time.Now(), Err: err})
         }
         return
     }
     ```
     Add the `"io"` import.

   - For `det.Infer()` error: same shape — emit a `SpeechError` before `return`.

   Leave the `ctx.Done()` branch as-is (clean cancel, no error event).

## Constraints
- `io.EOF` must NOT emit `SpeechError` — it's the documented clean-shutdown
  signal for finite sources; the consumer should treat it identically to
  "no more events".
- Context cancellation must NOT emit `SpeechError`.
- The channel must still close in all cases (the `defer close(out)` already
  handles this). Do not move the close.
- `cmd/vox/main.go`'s event switch only handles `SpeechStart`, `SpeechEnd`,
  `SpeechText`. After this change it should also handle `SpeechError`:
  log it so the operator sees why the pipeline went down. Add a `case
  asr.SpeechError:` to the switch in `run` that logs `log.Printf("asr:
  pipeline error: %v", ev.Err)`. This is the only caller-side change
  required.

## Verification
- `go test ./pkg/asr/...` passes — the existing `fakeSource` returns
  `io.EOF`, which must NOT produce a `SpeechError` event (the three
  existing tests assert exact event counts/lists; a stray `SpeechError`
  would break them, confirming regressions surface).
- Add one new test `TestListenSurfacesSourceError` in `listen_test.go`:
  a `fakeSource` variant that returns a non-EOF error exhausts the event
  channel with exactly one `SpeechError` event carrying that error.

  Sketch:
  ```go
  type errSource struct{ err error }
  func (s *errSource) Read() ([]int16, error) { return nil, s.err }

  func TestListenSurfacesSourceError(t *testing.T) {
      src := &errSource{err: errors.New("device disconnected")}
      got := collectEvents(Listen(context.Background(), src,
          &fakeDetector{probs: []float32{0.0}}, fakeRecognizer{}, vad.DefaultTiming()), time.Second)
      if len(got) != 1 || got[0].Kind != SpeechError || got[0].Err == nil {
          t.Fatalf("got %+v, want 1 SpeechError with Err", got)
      }
  }
  ```
  (`errors` import already present via `io` is not — add `"errors"`.)

## Out of scope
- Retrying / reconnecting a failed source.
- Distinguishing detector vs source errors in the event (the `Err` carries
  that detail; consumers can type-switch or string-match if they care).
- Changing the cancellation path.
# asr

Speech-to-text module (`github.com/xcono/asr`) for the realtime voice agent:
Silero VAD + transcription behind an importable facade
(`stt.New`/`stt.NewWith` → `Events()` / `IsSpeaking()` / `Transcribe()`),
consumed **in-process** by [`xcono/voices`](../voices) over Go channels.
`cmd/vox` is a standalone debug binary that runs the same pipeline and logs
events to stdout.

**No event transport lives here.** Events flow over Go channels only; if
cross-service propagation is ever needed, `xcono/voices` owns it.

Out of scope: LLM, TTS (sibling [`xcono/tts`](../tts)), playback, the
orchestrator turn loop.

## Build & run

```bash
make          # default goal: run cmd/vox (loads config.json from cwd)
make build    # compile to ./bin/vox
make test     # go test ./...
make vadscan  # replay recordings/session-*.wav through vad.Model+FSM (WAV=<file>)
make help     # list documented targets
make up-whisper / down-whisper   # Whisper STT server (docker.whisper.yaml)
```

`vox` takes one flag: `-config <path>` (default `config.json`). VAD diagnostics
are a config field, not a flag: `"vad": {"debug": true}` tees each window's
speech probability to stderr as a meter.

### Native dependencies

- **None.** The module builds with `CGO_ENABLED=0`.
- **Mic capture is pure Go.** `pkg/audio.Capture` speaks the PulseAudio
  protocol over its socket via
  [`jfreymuth/pulse`](https://github.com/jfreymuth/pulse) — no libpulse, no
  PortAudio. Runtime needs a PulseAudio server or PipeWire with its pulse layer
  (`pipewire-pulse`); set `PULSE_SERVER` if it isn't auto-discovered.
- **Silero VAD is pure Go.** `pkg/vad.Model` wraps
  `pkg/vad/internal/silero` — [`zserge/govad`](https://github.com/zserge/govad)
  vendored (MIT, licence and source version next to the code) with the 64-sample
  input context added — and the **v5** weights embedded in the binary — no
  model file, no ONNX Runtime.

## Config

`config.json` (or `-config`). Schema is in `pkg/config/config.go`; `Load` calls
`setDefaults`, so a minimal `{}` file runs. Sections: `vad` and `asr`. Unknown
keys are ignored silently — a misspelt section falls back to defaults.

Provider defaults to `"gigaam"` in code; the checked-in `config.json` selects
`"whisper"`. `gigaam` and `whisper` share `asr.Transcriber` (`language`/`prompt`
are sent only when non-empty); `"elevenlabs"` is `pkg/asr/elevenlabs.go`. All
three are built by `stt.NewRecognizer` — the one provider switch, shared with
`xcono/voices`. The letter budget is `asr.chars_slack` (default 12) +
`asr.max_chars_per_sec` (default 25, `0` turns the filter off) per second of
clip beyond `preroll_ms + release_ms`. Both are pointer fields read through
`ASRConfig.CharsSlackOrDefault()` / `CharsPerSecLimit()`, so an explicit `0`
differs from absent and configs composed without `Load` (voices) still get
the defaults. `asr.dump_dropped_dir` (default `""`, off) saves each clip either
filter drops as a WAV; the checked-in `config.json` sets `"dropped"`
(git-ignored). It is independent of `vad.debug`. `asr.record_dir` (default
`""`, off) records the whole mic stream as one `session-<time>.wav` per run;
the checked-in `config.json` sets `"recordings"` (git-ignored).
`asr.hallucination_phrases` is the phrase list (second filter line); read it
through `ASRConfig.HallucinationPhrasesOrDefault()`: absent (nil) =
`asr.DefaultHallucinationPhrases()`, `[]` = off, a list replaces the default
(no merge). The checked-in `config.json` leaves it absent. `README.md` has
the parameter tables.

## Architecture

```
Mic → audio.Capture → asr.Listen → SpeechStart / SpeechEnd / SpeechText
```

- `stt.go` — the facade. `New` owns the mic and the model;
  `NewWith` takes caller-owned components.
- `cmd/vox/main.go` — load config → build model, capture and recogniser → range
  over `asr.Listen` → log. `vaddebug.go` wraps the `Detector` to print the
  probability meter without changing results; under `asr.record_dir` the mic
  is wrapped in an `asr.Recorder` the same way, and its `Close` is deferred
  so the Ctrl-C path (cancel → Listen returns → `run` returns) finalises the
  WAV header.
- `cmd/vadscan/main.go` — offline replay of a 16 kHz mono WAV (a session
  recording) through `vad.Model` + `vad.FSM` with `-config` timings: summary,
  segments (`at=`/`end=` aligned with `vox`'s lines, clip stats as in
  `VADStats`) and loud bursts (`-loud` dBFS, `-min-ms`) outside every clip.
  The tool for VAD tuning on real sessions; no network.
- `pkg/asr` — `Listen` (`listen.go`) is the pipeline controller: it owns the
  FSM, preroll ring and utterance buffer, and transcribes in a goroutine so a
  slow recogniser never stalls capture. The channel closes when the source
  errors or the context is cancelled. `WithMaxCharsPerSec` (a variadic
  `ListenOption`, so existing calls compile) turns on the letter budget:
  a transcription whose letters+digits exceed `slack + rate × max(0, clip −
  (PrerollMs + ReleaseMs))` is dropped — Whisper's subtitle-credit and
  "Продолжение следует" hallucinations — logging `asr: dropped ...`; that turn
  ends at `SpeechEnd`. A budget, not a plain rate, because the ~1 s pause in
  every clip dilutes a short clip's rate below that of fast real speech.
  `WithCharsSlack` sets the slack (`DefaultCharsSlack` = 12 when absent).
  `WithHallucinationPhrases` (`phrases.go`) is the second line, checked only
  when the budget passed: text and entries are normalised to whole words
  (lower case, ё→е, non-letters/digits are breaks); a `*`-prefixed marker
  drops the text on any occurrence, a plain phrase only when the matched
  phrases cover >= half its letters (so "продолжение следует" inside a long
  real sentence passes). `dropReason` builds the log reason: the budget's
  `N letters > budget X` (unchanged wording) or `phrase "<entries>" c/N
  letters, budget X` — one reason per line, budget first.
  `SpeechText` carries `Audio`, `CharsPerSec`, `Letters`, `Budget` (+Inf when
  off) and `Segments` for logs; every event carries `Offset` — samples read
  from the `Source` through the confirming window (SpeechText: its clip end),
  the time base of the session recording;
  `SpeechEnd` and `SpeechText` carry `VAD` (`VADStats`: peak probability and
  windows >= `Threshold` out of all windows of the clip, preroll included —
  a parallel probability ring mirrors the preroll, so it assumes one window
  per `Source.Read`). `WithDumpDropped(dir)` writes a dropped clip to
  `dir/dropped-<time>.wav` from the transcription goroutine, never the
  capture loop; a write error is logged and ignored. A
  recogniser implementing the optional `ScoredRecognizer` (`*Transcriber`)
  supplies `Segments` (verbose_json `no_speech_prob` / `avg_logprob`); scores
  are logged by `cmd/vox`, never acted on. `Recorder` (`record.go`) is a
  `Source` wrapper that copies each window into a queue drained by a writer
  goroutine (`audio.WAVWriter`); a full queue drops the window and the writer
  pads the gap with silence so `Offset` stays aligned with the file. Also here: the consumer-side interfaces
  `Source`/`Detector`/`Recognizer`, `Transcriber` (batch, OpenAI-compatible
  multipart WAV upload), `ElevenLabs`, and `StreamSession` (WebSocket streaming).
- `pkg/vad` — `Model`, hysteresis `FSM`, `Preroll` ring, `EndOfTurn` countdown,
  `Timing` and the tuned consts.
- `pkg/audio` — PulseAudio `Capture`, PCM16LE / WAV codec, streaming
  `WAVWriter` (placeholder lengths patched on `Close`), `ResampleLinear`.
- `pkg/config` — schema + `Load`. `VADConfig.ToTiming()` lives here so all
  config-schema concerns stay in one package; `config` imports `vad` and `asr`
  (for `asr.DefaultCharsSlack`), never the reverse.
- `llm/` + `docker.*.yaml` — the external STT servers (Whisper on vLLM, GigaAM,
  Qwen3-ASR). Whisper's HTTP contract is in `llm/whisper/README.md`.

## Audio format (hard constraint)

Input is **16 kHz / 16-bit / little-endian / mono**. `vad.SampleRate` (16000)
and `vad.Window` (512 samples = 32 ms) are Silero's native contract, not
tunables. Convert anything else before it reaches the pipeline
(`audio.ResampleLinear`, `audio.PCM16LE` / `audio.DecodePCM16LE`); resample
output is float32 [-1,1], not int16.

## Conventions

- Short package names, one concern per directory: `vad`, `asr`, `audio`,
  `config`.
- One noun per file (`listen.go`, `stream.go`, `batch.go`, `capture.go`).
- Errors: `fmt.Errorf("context: %w", err)`, return early, package-prefixed
  messages (`"vad model: %w"`, `"asr: status %d: %s"`).
- Doc comments explain *why*. Several record a bug they prevent (FSM leaky
  counters, preroll sizing, overflow tolerance) — keep that context when editing.
- Interfaces are defined at the consumer (`asr.Source`, `asr.Detector`,
  `asr.Recognizer`) so tests can inject fakes.

## Testing

- Tests sit next to the code, same package (white-box).
- `pkg/asr/batch` and `pkg/config` use `testify`; `pkg/vad`, `pkg/asr/listen`
  and `pkg/audio` use plain `t.Fatalf`/`t.Errorf`. Match the package.
- Pipeline tests avoid the real model and the network: `fakeDetector` returns
  scripted probabilities, `fakeSource` yields N windows then `io.EOF`, batch
  tests use `httptest.NewServer`, streaming tests a `websocket.Upgrader`.
- Drive timing-dependent tests from `Timing.OnsetWindows()` /
  `ReleaseWindows()` / `EndOfTurnWindows()` so they survive const changes.

## Gotchas

- **`Model.Infer` is stateful** (Silero's recurrent state and input context).
  One `Model` per stream; it is not safe for concurrent use.
- **Silero v5 needs 64 samples of context; without it the model goes deaf in
  a stream.** The official wrapper feeds the previous window's last 64 samples
  + the 512-sample window (576) per call; fed bare 512-sample windows, loud
  words score 0.01–0.2 after a while (27 of 124 loud bursts missed on a 304 s
  session; snakers4/silero-vad#771). Both earlier wrappers (silero-vad-onnx-go,
  upstream govad) made that mistake, and it was masked by resetting the state
  after every 500 ms pause — removed once the context was added. `Model` now
  never resets; windows must be consecutive audio.
  `TestModelMatchesReference` compares a synthetic signal with probabilities
  from the official implementation (`pkg/vad/testdata`, regeneration steps on
  the test) — keep it passing; check model changes with `cmd/vadscan` on
  session recordings. A VAD symptom that looks like sensitivity (missed soft
  words, a threshold that has to go lower) is a reason to compare against the
  official implementation first — the missing context hid behind a lowered
  threshold and raised mic gain from June to November 2026.
- **A cough still opens a segment; claps and mic rustle do not** (live test,
  2026-11-18). The text is caught downstream (letter budget, phrase list), but
  `IsSpeaking()` is true for that segment — whether a segment with no text
  interrupts playback is `xcono/voices`' decision, not solved here.
- **The timing consts are tuned for Silero v5.** A v6 model scores the same
  audio differently — switching generations means re-tuning
  `pkg/vad/consts.go`, not a drop-in swap. Read the comments there before
  changing anything.
- **`vad.Timing` zero value is invalid.** Use `vad.DefaultTiming()` or
  `config.VADConfig.ToTiming()`.
- **`pkg/vad/internal/silero` is vendored code** (zserge/govad
  v0.0.0-20260330155402-74750eabf3a4, a single-author module with no
  releases), not a dependency. Local changes are listed in its package comment
  — keep them minimal and listed; upstream has no context API to go back to.
- **`audio.Capture.Read()` aliases its internal buffer.** The slice is
  overwritten on the next `Read` — copy before retaining it. `Listen` already
  does.
- **The mic is push, `Read` is pull.** The pulse library calls our writer from
  the goroutine that reads the server connection — the same one that delivers
  every request's reply. The callback copies the packet (the library reuses the
  buffer) into a queue and never blocks; `Read` assembles `frameSize` windows
  from that queue.
- **Overflow is counted, not fatal.** When the queue is full the packet is
  dropped and `Capture.Overflows()` counts it; the stream keeps running. Don't
  propagate it. The server itself has no overflow signal for recording.
- **A removed mic is not reported.** The library drops the server's
  stream-killed / suspended / moved messages; only a lost connection surfaces.
  `Read` therefore errors after 2 s without a packet (`readTimeout`) — the
  server streams silence as samples, so a gap that long means the source is
  gone. `Read` returns `io.EOF` after `Close`.
- **A Bluetooth mic starts with about a second of exact zeros** (seen with
  EarFun earbuds; a USB Jabra starts with real signal). Frames arrive on time,
  they are just digital silence while the headset switches profile. No
  special handling — the VAD sees silence.
- **`jfreymuth/pulse` is pinned at v0.1.1**, the version `xcono/tts` (via oto)
  already uses, so `xcono/voices` gets one version for both.
- **All providers are batch.** `asr.StreamSession` / `DialStream` and
  `vad.EndOfTurn` are implemented and tested but not wired into `Listen`.
- **`Event.VoiceFileID` is never set** by the pipeline.
- **A failed transcription is logged, not emitted.** `Listen` prints
  `asr: transcription failed: at=… …: <cause>` and the turn ends at
  `SpeechEnd`; `SpeechError` stays terminal (source failure only).
- **Whisper runs on vLLM since 2026-11-18; whisper.cpp was dropped because
  it does not target the RTX 50xx.** History: our sm_120 build of
  whisper-server ran several hundred requests cleanly, then after a host
  reboot (2026-11-18 15:01) hung four times on a request (one thread at 100%
  CPU in user time, GPU idle, `/health` still 200) and once aborted with
  `CUDA error: unspecified launch failure` + kernel `Xid 13` on GPU 1 (PCI
  03:00). The response format was not the cause (it happened with both
  `verbose_json` and `json`). The whisper.cpp Dockerfile is in git history.
- **The vLLM Whisper server needs its attention backends set by hand.**
  With vLLM's default (FlashAttention 2) on sm_120 every request returns 200
  and the text `ewewew…` — a silent failure, so after any image or flag
  change send a known clip and read the text. Flags and reasons:
  `docker.whisper.yaml`, `llm/whisper/README.md`.
- **A GPU-side stall is still open, and it is not whisper.cpp-specific.** One
  of four vLLM starts hung in `torch.cuda.synchronize()` (100% CPU, GPU 1
  idle, no Xid) until restarted — the same picture as the whisper.cpp hangs.
  A container stuck at start is `unhealthy` and Docker does not restart it:
  `docker restart asr-stt-1`. In `vox` a dead server shows as VAD segments
  with no `stt:` line and `transcription failed` (connection refused at once,
  or a timeout after `DefaultTimeout` 30 s). Not checked: GPU 0 as a
  control, running without the TTS container on the same card, a cold power
  cycle.
- **Keep `asr.whisper.prompt` empty.** vLLM 0.32.0 corrupts Whisper decoding
  when a prompt is sent (vllm-project/vllm#53206).
- **`Transcriber.Verbose` is currently unused.** The GigaAM server answers
  400 to any `response_format` but `json|text`; `NewRecognizer` sets it for
  no provider since 2026-11-18 — the scores it carried on whisper.cpp
  (`no_speech_prob`, `avg_logprob`) had not separated hallucinations from
  speech, and `verbose_json` is untested against vLLM. `Segments` is nil and
  `vox` logs no `no_speech=`/`logprob=`.
- **`stt.NewWith` does not filter, dump or record by default.** Pass
  `stt.WithMaxCharsPerSec(cfg.CharsPerSecLimit())` (slack defaults to 12;
  add `stt.WithCharsSlack(cfg.CharsSlackOrDefault())` to honour a configured
  one), `stt.WithHallucinationPhrases(cfg.HallucinationPhrasesOrDefault())`
  and `stt.WithDumpDropped(cfg.DumpDroppedDir)`; to record, wrap the
  source in `asr.NewRecorder` and close it after the service. `stt.New` and
  `vox` do all of it from config. `Service.Transcribe` (one-shot) is never
  filtered.
- **`Recorder.Close` must run after `Listen` stops reading** for the file to
  hold every window; a `Read` racing `Close` is safe but unrecorded. A
  session file has no size limit or rotation (~115 MB/hour).
- **ElevenLabs needs `elevenlabs.api_key`**, and `stt.NewRecognizer` does not
  validate it — a missing key fails at request time (401), not at startup.

## Python

Keep Python out of the Go codebase. The STT servers under `llm/` are external
processes reached over HTTP: GigaAM and Qwen3-ASR are Python, Whisper is vLLM in a
published image (no Dockerfile; flags in `docker.whisper.yaml`).

## Development

Prefer small, reviewed increments. `README.md` is the user-facing reference,
this file the agent-facing one — keep both consistent when behaviour changes.

// Command vox is the standalone ASR/STT debug loop. It captures the mic, detects
// speech with Silero VAD, transcribes each turn via a configured STT provider, and
// logs the VAD/transcription events to stdout. It carries no event transport: the
// importable facade (stt.New / stt.NewWith) is how other processes consume ASR.
// If cross-service event propagation is ever needed, the central unit (xcono/voices)
// owns that — a NATS server in its docker stack — not this module.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	stt "github.com/xcono/asr"
	"github.com/xcono/asr/pkg/asr"
	"github.com/xcono/asr/pkg/audio"
	"github.com/xcono/asr/pkg/config"
	"github.com/xcono/asr/pkg/vad"
)

func main() {
	cfgPath := flag.String("config", "config.json", "path to config.json")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	if err := run(cfg); err != nil {
		log.Fatal(err)
	}
}

func run(cfg *config.Config) error {
	// --- VAD model ---
	model, err := vad.NewModel()
	if err != nil {
		return fmt.Errorf("vad model: %w", err)
	}
	defer model.Close()

	// Build Timing from config.
	timing := cfg.VAD.ToTiming()

	// --- Audio capture ---
	mic, err := audio.OpenCapture(vad.SampleRate, vad.Window)
	if err != nil {
		return fmt.Errorf("open capture: %w", err)
	}
	defer mic.Close()

	// --- Session recording (asr.record_dir) ---
	// The recorder wraps the mic, so it sees exactly the windows Listen reads
	// and "at=" in the lines below is a position in its file. Deferred after
	// mic.Close, so it runs first: Listen has returned by then (the event loop
	// below drains its channel), the queue drains and the WAV header is
	// patched — the Ctrl-C path ends here, through run's return.
	var src asr.Source = mic
	var recorder *asr.Recorder
	if dir := cfg.ASR.RecordDir; dir != "" {
		recorder, err = asr.NewRecorder(mic, dir)
		if err != nil {
			return err
		}
		defer func() {
			if err := recorder.Close(); err != nil {
				log.Printf("record: %v", err)
			}
			if lost := recorder.Lost(); lost > 0 {
				log.Printf("note: %.2fs of the recording is silence padded in: the writer fell behind", lost.Seconds())
			}
			fmt.Printf("session recorded -> %s\n", recorder.Path())
		}()
		src = recorder
	}

	// --- ASR recognizer ---
	rec, err := stt.NewRecognizer(cfg.ASR)
	if err != nil {
		return err
	}

	// asr.Listen takes the Detector interface; under "vad.debug" wrap the model to
	// tee each window's Silero probability to stderr (diagnoses missed onsets). The
	// meter draws its gate ticks from timing, so they track the configured thresholds.
	var det asr.Detector = model
	if cfg.VAD.Debug {
		det = debugDetector{inner: model, w: os.Stderr, timing: timing}
	}

	// --- Pipeline ---
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		fmt.Println("\nshutting down...")
		cancel()
	}()

	fmt.Println("Listening... (Ctrl-C to quit)")
	fmt.Printf("ASR: %s%s, %s, %s\n", cfg.ASR.Provider, endpoint(rec), budgetLine(cfg, timing), phraseLine(cfg))
	if d := cfg.ASR.DumpDroppedDir; d != "" {
		fmt.Printf("dropped clips -> %s\n", d)
	}
	if recorder != nil {
		fmt.Printf("recording session -> %s\n", recorder.Path())
	}

	events := asr.Listen(ctx, src, det, rec, timing,
		asr.WithMaxCharsPerSec(cfg.ASR.CharsPerSecLimit()),
		asr.WithCharsSlack(cfg.ASR.CharsSlackOrDefault()),
		asr.WithDumpDropped(cfg.ASR.DumpDroppedDir),
		asr.WithHallucinationPhrases(cfg.ASR.HallucinationPhrasesOrDefault()))
	for ev := range events {
		// at= is the event's position in the session recording (Event.Offset,
		// counted in samples read, so it matches the WAV even without one).
		switch ev.Kind {
		case asr.SpeechStart:
			log.Printf("vad: speaking started at %s at=%.2fs", ev.Timestamp.Format(time.RFC3339Nano), ev.Offset.Seconds())
		case asr.SpeechEnd:
			log.Printf("vad: speaking stopped at %s at=%.2fs%s", ev.Timestamp.Format(time.RFC3339Nano), ev.Offset.Seconds(), ev.VAD)
		case asr.SpeechText:
			// Scores are logged to choose a threshold from live speech; nothing
			// is dropped by them. Turns over the letter budget or holding a
			// hallucination phrase never get here — Listen logs them as
			// "asr: dropped ...".
			log.Printf("stt: %q at=%.2fs %.2fs %d letters <= budget %.1f %.1f chars/s%s%s (voice_file_id=%s)",
				ev.Text, ev.Offset.Seconds(), ev.Audio.Seconds(), ev.Letters, ev.Budget, ev.CharsPerSec,
				ev.VAD, asr.FormatSegments(ev.Segments), ev.VoiceFileID)
		case asr.SpeechError:
			log.Printf("asr: pipeline error: %v", ev.Err)
		}
	}

	if n := mic.Overflows(); n > 0 {
		log.Printf("note: dropped %d mic packet(s): the pipeline fell behind", n)
	}
	return nil
}

// budgetLine describes the hallucination filter for the startup line:
// "letter budget 12 + 25/s beyond a 1.00s pause", or "letter budget off".
func budgetLine(cfg *config.Config, t vad.Timing) string {
	rate := cfg.ASR.CharsPerSecLimit()
	if rate <= 0 {
		return "letter budget off"
	}
	pause := float64(t.PrerollMs+t.ReleaseMs) / 1000
	return fmt.Sprintf("letter budget %g + %g/s beyond a %.2fs pause", cfg.ASR.CharsSlackOrDefault(), rate, pause)
}

// phraseLine describes the phrase filter for the startup line:
// "hallucination phrases 6 (2 markers)", or "hallucination phrases off".
func phraseLine(cfg *config.Config) string {
	phrases := cfg.ASR.HallucinationPhrasesOrDefault()
	if len(phrases) == 0 {
		return "hallucination phrases off"
	}
	markers := 0
	for _, p := range phrases {
		if strings.HasPrefix(strings.TrimSpace(p), asr.MarkerPrefix) {
			markers++
		}
	}
	return fmt.Sprintf("hallucination phrases %d (%d markers)", len(phrases), markers)
}

// endpoint names the server a recognizer talks to, for the startup line. It reads
// the built recognizer rather than a config section so the line cannot name one
// provider's URL while another is selected (it used to print GigaAM's always).
func endpoint(rec asr.Recognizer) string {
	switch r := rec.(type) {
	case *asr.Transcriber:
		return " (" + r.BaseURL + ")"
	case *asr.ElevenLabs:
		if r.BaseURL != "" {
			return " (" + r.BaseURL + ")"
		}
		return " (api.elevenlabs.io)"
	default:
		return ""
	}
}

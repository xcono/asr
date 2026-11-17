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
	"syscall"
	"time"

	"github.com/gordonklaus/portaudio"

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
	model, err := vad.NewModel(cfg.VAD.ModelPath)
	if err != nil {
		return fmt.Errorf("vad model: %w", err)
	}
	defer model.Close()

	// Build Timing from config.
	timing := cfg.VAD.ToTiming()

	// --- Audio capture ---
	restore, err := audio.SilenceStderr()
	if err != nil {
		log.Printf("warning: could not suppress portaudio stderr: %v", err)
	}
	if restore != nil {
		defer restore()
	}

	if err := portaudio.Initialize(); err != nil {
		return fmt.Errorf("portaudio init: %w", err)
	}
	defer portaudio.Terminate()

	mic, err := audio.OpenCapture(vad.SampleRate, vad.Window)
	if err != nil {
		return fmt.Errorf("open capture: %w", err)
	}
	defer mic.Close()

	// Restore stderr after capture is live.
	if restore != nil {
		restore()
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
	fmt.Printf("ASR: %s%s\n", cfg.ASR.Provider, endpoint(rec))

	events := asr.Listen(ctx, mic, det, rec, timing)
	for ev := range events {
		switch ev.Kind {
		case asr.SpeechStart:
			log.Printf("vad: speaking started at %s", ev.Timestamp.Format(time.RFC3339Nano))
		case asr.SpeechEnd:
			log.Printf("vad: speaking stopped at %s", ev.Timestamp.Format(time.RFC3339Nano))
		case asr.SpeechText:
			log.Printf("stt: %q (voice_file_id=%s)", ev.Text, ev.VoiceFileID)
		case asr.SpeechError:
			log.Printf("asr: pipeline error: %v", ev.Err)
		}
	}

	if n := mic.Overflows(); n > 0 {
		log.Printf("note: tolerated %d mic input overflow(s)", n)
	}
	return nil
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

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

	// --- STT recognizer ---
	var rec asr.Recognizer
	switch cfg.STT.Provider {
	case "gigaam":
		rec = &asr.Transcriber{BaseURL: cfg.STT.GigaAM.BaseURL, Model: cfg.STT.GigaAM.Model}
	default:
		return fmt.Errorf("unknown stt provider: %s", cfg.STT.Provider)
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
	fmt.Printf("STT: %s (%s)\n", cfg.STT.Provider, cfg.STT.GigaAM.BaseURL)

	events := asr.Listen(ctx, mic, model, rec, timing)
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

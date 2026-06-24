// Command vox is the standalone ASR/STT service for a realtime voice agent.
// It captures the mic, detects speech with Silero VAD, transcribes each turn
// via a configured STT provider, and publishes events over an embedded NATS
// Jetstream server. Clients subscribe to NATS subjects to consume VAD and
// transcription events.
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

	"github.com/xcono/vox/pkg/asr"
	"github.com/xcono/vox/pkg/audio"
	"github.com/xcono/vox/pkg/config"
	"github.com/xcono/vox/pkg/nats"
	"github.com/xcono/vox/pkg/vad"
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
	// --- NATS ---
	ns, err := nats.NewServer(cfg.NATS.Port, cfg.NATS.StoreDir)
	if err != nil {
		return fmt.Errorf("nats: %w", err)
	}
	defer ns.Close()
	log.Printf("nats: listening on %s", ns.Conn().ConnectedUrl())

	// --- VAD model ---
	model, err := vad.NewModel(cfg.VAD.ModelPath)
	if err != nil {
		return fmt.Errorf("vad model: %w", err)
	}
	defer model.Close()

	// Build Timing from config.
	th, hy, bi, rm, eot, pr := cfg.VAD.ToTiming()
	timing := vad.Timing{
		Threshold:   th,
		Hysteresis:  hy,
		BargeInMs:   bi,
		ReleaseMs:   rm,
		EndOfTurnMs: eot,
		PrerollMs:   pr,
	}

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
			if err := ns.PublishVADStart(ev.Timestamp); err != nil {
				log.Printf("nats: publish vad start: %v", err)
			}
		case asr.SpeechEnd:
			log.Printf("vad: speaking stopped at %s", ev.Timestamp.Format(time.RFC3339Nano))
			if err := ns.PublishVADStop(ev.Timestamp); err != nil {
				log.Printf("nats: publish vad stop: %v", err)
			}
		case asr.SpeechText:
			log.Printf("stt: %q (voice_file_id=%s)", ev.Text, ev.VoiceFileID)
			if err := ns.PublishMessage(ev.Timestamp, ev.Text, ev.VoiceFileID); err != nil {
				log.Printf("nats: publish message: %v", err)
			}
		}
	}

	if n := mic.Overflows(); n > 0 {
		log.Printf("note: tolerated %d mic input overflow(s)", n)
	}
	return nil
}

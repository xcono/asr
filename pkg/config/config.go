package config

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/xcono/vox/pkg/vad"
)

// Config is the top-level configuration for the vox service.
type Config struct {
	VAD  VADConfig  `json:"vad"`
	STT  STTConfig  `json:"stt"`
	NATS NATSConfig `json:"nats"`
}

// VADConfig holds Silero VAD timing parameters.
type VADConfig struct {
	ModelPath   string  `json:"model_path"`     // Path to silero_vad.onnx
	Threshold   float32 `json:"threshold"`      // Onset probability gate (default 0.30)
	Hysteresis  float32 `json:"hysteresis"`     // Release gate = Threshold - Hysteresis (default 0.15)
	BargeInMs   int     `json:"barge_in_ms"`    // Above-threshold evidence for onset (default 64)
	ReleaseMs   int     `json:"release_ms"`     // Silence evidence for offset (default 700)
	EndOfTurnMs int     `json:"end_of_turn_ms"` // Continuous silence for streaming turn-end (default 800)
	PrerollMs   int     `json:"preroll_ms"`     // Lead-in audio before confirmed onset (default 300)
}

// STTConfig holds the speech-to-text provider configuration.
type STTConfig struct {
	Provider   string           `json:"provider"` // "gigaam" or "elevenlabs"
	GigaAM     GigaAMConfig     `json:"gigaam"`
	ElevenLabs ElevenLabsConfig `json:"elevenlabs"`
}

// GigaAMConfig holds configuration for the local GigaAM batch STT server.
type GigaAMConfig struct {
	BaseURL string `json:"base_url"` // e.g. "http://localhost:8008/v1"
	Model   string `json:"model"`    // Optional model name
}

// ElevenLabsConfig holds configuration for the remote ElevenLabs STT service.
type ElevenLabsConfig struct {
	APIKey string `json:"api_key"`
	Model  string `json:"model"`
}

// NATSConfig holds the embedded NATS server configuration.
type NATSConfig struct {
	Port      int    `json:"port"`        // Client port (default 4222)
	StoreDir  string `json:"store_dir"`   // Jetstream storage directory (default "/tmp/nats")
	VADMaxAge string `json:"vad_max_age"` // MaxAge for the VAD stream (default "72h"); 0 disables
	STTMaxAge string `json:"stt_max_age"` // MaxAge for the STT stream (default "72h"); 0 disables
}

// Load reads config.json from the given path and unmarshals it.
// If path is empty, it defaults to "config.json".
func Load(path string) (*Config, error) {
	if path == "" {
		path = "config.json"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	cfg.setDefaults()
	return &cfg, nil
}

// setDefaults fills in zero values with sensible defaults.
func (c *Config) setDefaults() {
	if c.VAD.ModelPath == "" {
		c.VAD.ModelPath = "pkg/vad/silero_vad.onnx"
	}
	if c.VAD.Threshold == 0 {
		c.VAD.Threshold = 0.30
	}
	if c.VAD.Hysteresis == 0 {
		c.VAD.Hysteresis = 0.15
	}
	if c.VAD.BargeInMs == 0 {
		c.VAD.BargeInMs = 64
	}
	if c.VAD.ReleaseMs == 0 {
		c.VAD.ReleaseMs = 700
	}
	if c.VAD.EndOfTurnMs == 0 {
		c.VAD.EndOfTurnMs = 800
	}
	if c.VAD.PrerollMs == 0 {
		c.VAD.PrerollMs = 300
	}
	if c.STT.Provider == "" {
		c.STT.Provider = "gigaam"
	}
	if c.STT.GigaAM.BaseURL == "" {
		c.STT.GigaAM.BaseURL = "http://localhost:8008/v1"
	}
	if c.NATS.Port == 0 {
		c.NATS.Port = 4222
	}
	if c.NATS.StoreDir == "" {
		c.NATS.StoreDir = "/tmp/nats"
	}
	if c.NATS.VADMaxAge == "" {
		c.NATS.VADMaxAge = "72h"
	}
	if c.NATS.STTMaxAge == "" {
		c.NATS.STTMaxAge = "72h"
	}
}

// ToTiming builds the vad.Timing for the configured VAD parameters. Lives
// here (rather than as a vad constructor) so all config schema concerns stay
// in one package; config importing vad adds no cycle since vad imports
// nothing from this project.
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

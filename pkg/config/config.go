package config

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/xcono/asr/pkg/vad"
)

// Config is the top-level configuration for the asr (vox) service. The two
// sections are namespaced by name ("vad", "asr") so they are byte-identical
// whether this module is configured standalone or merged into the xcono/voices
// config, which composes the same sections.
type Config struct {
	VAD VADConfig `json:"vad"`
	ASR ASRConfig `json:"asr"`
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
	Debug       bool    `json:"debug"`          // Tee per-window Silero probability to stderr (onset diagnostics)
}

// ASRConfig holds the speech-to-text provider configuration.
type ASRConfig struct {
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
	APIKey       string `json:"api_key"`
	Model        string `json:"model"`         // "" => "scribe_v2"
	LanguageCode string `json:"language_code"` // ISO-639-1/-3 hint, e.g. "ru"; "" => auto-detect
	BaseURL      string `json:"base_url"`      // "" => "https://api.elevenlabs.io"
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
	if c.ASR.Provider == "" {
		c.ASR.Provider = "gigaam"
	}
	if c.ASR.GigaAM.BaseURL == "" {
		c.ASR.GigaAM.BaseURL = "http://localhost:8008/v1"
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

package config

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/xcono/asr/pkg/asr"
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
	Provider string `json:"provider"` // "gigaam", "whisper" or "elevenlabs"
	// MaxCharsPerSec is the rate of the letter budget that drops Whisper's
	// subtitle-credit hallucinations on short unclear sounds: a live
	// transcription may carry CharsSlack letters plus this many per second
	// of clip beyond the preroll + release pause (see asr.WithMaxCharsPerSec
	// for why a budget and not a plain rate). 0 turns the filter off.
	// A pointer so an explicit 0 differs from "absent"; read it through
	// CharsPerSecLimit, which also covers configs built without Load.
	MaxCharsPerSec *float64 `json:"max_chars_per_sec"`
	// CharsSlack is the letters the budget allows whatever the clip length,
	// so a short real answer passes whole. A pointer for the same reason as
	// MaxCharsPerSec; read it through CharsSlackOrDefault.
	CharsSlack *float64 `json:"chars_slack"`
	// DumpDroppedDir, when set, saves the clip of each transcription the
	// letter budget drops there as a WAV, so the sound that opened a false segment
	// can be listened to (see asr.WithDumpDropped). "" (default) disables it.
	// Independent of vad.debug: the meter floods stderr, the dump should not
	// need it. A relative path resolves against the working directory.
	DumpDroppedDir string `json:"dump_dropped_dir"`
	// HallucinationPhrases is the second line of the filter: a live
	// transcription within the letter budget is still dropped when it
	// contains one of these (see asr.WithHallucinationPhrases; a leading
	// "*" marks an entry that drops the text wherever it occurs, the others
	// must cover at least half its letters). The list replaces the default
	// rather than extending it, so it is the whole truth in one place: absent
	// (nil) is asr.DefaultHallucinationPhrases, an explicit [] turns the
	// filter off, and to extend the default copy it and add. A plain slice,
	// not a pointer: JSON already tells absent (nil) from [] (empty, non-nil).
	// Read it through HallucinationPhrasesOrDefault.
	HallucinationPhrases []string `json:"hallucination_phrases"`
	// RecordDir, when set, records the whole mic stream of each run there as
	// one session-<time>.wav (see asr.Recorder), so words the VAD missed can
	// be found by ear and the session replayed offline. "" (default)
	// disables it. No size limit or rotation: a file grows ~115 MB an hour.
	RecordDir  string           `json:"record_dir"`
	GigaAM     GigaAMConfig     `json:"gigaam"`
	Whisper    WhisperConfig    `json:"whisper"`
	ElevenLabs ElevenLabsConfig `json:"elevenlabs"`
}

// DefaultMaxCharsPerSec is the budget's rate: above connected speech
// (15–18 letters per second of speech) once the pause is subtracted, which
// is what lets it sit below the old plain-rate cap of 30.
const DefaultMaxCharsPerSec = 25

// CharsPerSecLimit is the effective rate limit: MaxCharsPerSec when set
// (0 disables the filter), DefaultMaxCharsPerSec when absent.
func (c *ASRConfig) CharsPerSecLimit() float64 {
	if c.MaxCharsPerSec == nil {
		return DefaultMaxCharsPerSec
	}
	return *c.MaxCharsPerSec
}

// CharsSlackOrDefault is the effective budget slack: CharsSlack when set,
// asr.DefaultCharsSlack when absent.
func (c *ASRConfig) CharsSlackOrDefault() float64 {
	if c.CharsSlack == nil {
		return asr.DefaultCharsSlack
	}
	return *c.CharsSlack
}

// HallucinationPhrasesOrDefault is the effective phrase list:
// HallucinationPhrases when set (empty turns the filter off),
// asr.DefaultHallucinationPhrases when nil — also for a config built
// without Load.
func (c *ASRConfig) HallucinationPhrasesOrDefault() []string {
	if c.HallucinationPhrases == nil {
		return asr.DefaultHallucinationPhrases()
	}
	return c.HallucinationPhrases
}

// GigaAMConfig holds configuration for the local GigaAM batch STT server.
type GigaAMConfig struct {
	BaseURL string `json:"base_url"` // e.g. "http://localhost:8008/v1"
	Model   string `json:"model"`    // Optional model name
}

// WhisperConfig holds configuration for the local whisper.cpp batch STT server
// (docker.whisper.yaml). Language and Prompt map to the request fields of the
// same name; see llm/whisper/README.md for how the server treats them.
type WhisperConfig struct {
	BaseURL  string `json:"base_url"` // e.g. "http://localhost:8008/v1"
	Model    string `json:"model"`    // Optional; whisper.cpp ignores it
	Language string `json:"language"` // Default "ru"; "auto" to detect
	Prompt   string `json:"prompt"`   // Optional decoder context; empty by default
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
	if c.ASR.Whisper.BaseURL == "" {
		c.ASR.Whisper.BaseURL = "http://localhost:8008/v1"
	}
	if c.ASR.Whisper.Language == "" {
		c.ASR.Whisper.Language = "ru"
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

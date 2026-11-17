package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/xcono/asr/pkg/vad"
)

func TestLoad_Defaults(t *testing.T) {
	// A minimal config with zero values should produce defaults.
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	// Write an empty object so JSON parsing succeeds.
	requireWriteJSON(t, path, map[string]any{})

	cfg, err := Load(path)
	assert.NoError(t, err)
	assert.NotNil(t, cfg)

	// VAD defaults
	assert.Equal(t, float32(0.30), cfg.VAD.Threshold)
	assert.Equal(t, float32(0.15), cfg.VAD.Hysteresis)
	assert.Equal(t, 64, cfg.VAD.BargeInMs)
	assert.Equal(t, 700, cfg.VAD.ReleaseMs)
	assert.Equal(t, 800, cfg.VAD.EndOfTurnMs)
	assert.Equal(t, 300, cfg.VAD.PrerollMs)

	// STT defaults
	assert.Equal(t, "gigaam", cfg.ASR.Provider)
	assert.Equal(t, "http://localhost:8008/v1", cfg.ASR.GigaAM.BaseURL)
	assert.Empty(t, cfg.ASR.GigaAM.Model)
	assert.Equal(t, "http://localhost:8008/v1", cfg.ASR.Whisper.BaseURL)
	assert.Equal(t, "ru", cfg.ASR.Whisper.Language)
	assert.Empty(t, cfg.ASR.Whisper.Model)
	assert.Empty(t, cfg.ASR.Whisper.Prompt)
	assert.Empty(t, cfg.ASR.ElevenLabs.APIKey)
	assert.Empty(t, cfg.ASR.ElevenLabs.Model)
}

func TestLoad_FullConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	input := Config{
		VAD: VADConfig{
			Threshold:   0.50,
			Hysteresis:  0.20,
			BargeInMs:   128,
			ReleaseMs:   500,
			EndOfTurnMs: 1000,
			PrerollMs:   200,
		},
		ASR: ASRConfig{
			Provider: "elevenlabs",
			GigaAM: GigaAMConfig{
				BaseURL: "http://custom:8008/v1",
				Model:   "custom-model",
			},
			ElevenLabs: ElevenLabsConfig{
				APIKey: "sk-abc123",
				Model:  "eleven_multilingual_v2",
			},
		},
	}

	requireWriteJSON(t, path, input)

	cfg, err := Load(path)
	assert.NoError(t, err)
	assert.NotNil(t, cfg)

	// VAD
	assert.Equal(t, float32(0.50), cfg.VAD.Threshold)
	assert.Equal(t, float32(0.20), cfg.VAD.Hysteresis)
	assert.Equal(t, 128, cfg.VAD.BargeInMs)
	assert.Equal(t, 500, cfg.VAD.ReleaseMs)
	assert.Equal(t, 1000, cfg.VAD.EndOfTurnMs)
	assert.Equal(t, 200, cfg.VAD.PrerollMs)

	// STT
	assert.Equal(t, "elevenlabs", cfg.ASR.Provider)
	assert.Equal(t, "http://custom:8008/v1", cfg.ASR.GigaAM.BaseURL)
	assert.Equal(t, "custom-model", cfg.ASR.GigaAM.Model)
	assert.Equal(t, "sk-abc123", cfg.ASR.ElevenLabs.APIKey)
	assert.Equal(t, "eleven_multilingual_v2", cfg.ASR.ElevenLabs.Model)
}

func TestLoad_WhisperSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	requireWriteString(t, path, `{"asr": {"provider": "whisper", "whisper": {
		"base_url": "http://gpu:9000/v1", "model": "large-v3-turbo",
		"language": "auto", "prompt": "Kibana, хотфикс"}}}`)

	cfg, err := Load(path)
	assert.NoError(t, err)
	assert.Equal(t, "whisper", cfg.ASR.Provider)
	assert.Equal(t, WhisperConfig{
		BaseURL:  "http://gpu:9000/v1",
		Model:    "large-v3-turbo",
		Language: "auto",
		Prompt:   "Kibana, хотфикс",
	}, cfg.ASR.Whisper)
}

func TestLoad_MissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent.json")

	cfg, err := Load(path)
	assert.Error(t, err)
	assert.Nil(t, cfg)
	assert.ErrorContains(t, err, "read config")
}

func TestLoad_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	requireWriteString(t, path, "{invalid json}")

	cfg, err := Load(path)
	assert.Error(t, err)
	assert.Nil(t, cfg)
	assert.ErrorContains(t, err, "parse config")
}

func TestLoad_EmptyPathDefaults(t *testing.T) {
	// When path is empty, Load defaults to "config.json" in the current dir.
	// We'll change into a temp dir so the default path resolves there.
	dir := t.TempDir()
	origDir, err := os.Getwd()
	assert.NoError(t, err)
	defer func() { _ = os.Chdir(origDir) }()

	err = os.Chdir(dir)
	assert.NoError(t, err)

	requireWriteJSON(t, "config.json", map[string]any{
		"asr": map[string]any{
			"provider": "elevenlabs",
		},
	})

	cfg, err := Load("")
	assert.NoError(t, err)
	assert.NotNil(t, cfg)
	assert.Equal(t, "elevenlabs", cfg.ASR.Provider)
}

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
	// Ensure the returned value is a proper vad.Timing, not just a config-shaped copy.
	assert.IsType(t, vad.Timing{}, got)
}

// --- helpers ---

func requireWriteJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
}

func requireWriteString(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// TestCharsPerSecLimit: absent means the default, an explicit 0 turns the
// filter off, any other value is taken as is — also for a config built
// without Load (xcono/voices composes ASRConfig literally).
func TestCharsPerSecLimit(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct {
		name string
		asr  map[string]any
		want float64
	}{
		{"absent", map[string]any{}, DefaultMaxCharsPerSec},
		{"zero disables", map[string]any{"max_chars_per_sec": 0}, 0},
		{"explicit", map[string]any{"max_chars_per_sec": 25.5}, 25.5},
	} {
		path := filepath.Join(dir, "config.json")
		requireWriteJSON(t, path, map[string]any{"asr": c.asr})
		cfg, err := Load(path)
		assert.NoError(t, err, c.name)
		assert.Equal(t, c.want, cfg.ASR.CharsPerSecLimit(), c.name)
	}
	var lit ASRConfig
	assert.Equal(t, float64(DefaultMaxCharsPerSec), lit.CharsPerSecLimit())
}

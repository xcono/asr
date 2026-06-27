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
	assert.Equal(t, "pkg/vad/silero_vad.onnx", cfg.VAD.ModelPath)
	assert.Equal(t, float32(0.30), cfg.VAD.Threshold)
	assert.Equal(t, float32(0.15), cfg.VAD.Hysteresis)
	assert.Equal(t, 64, cfg.VAD.BargeInMs)
	assert.Equal(t, 700, cfg.VAD.ReleaseMs)
	assert.Equal(t, 800, cfg.VAD.EndOfTurnMs)
	assert.Equal(t, 300, cfg.VAD.PrerollMs)

	// STT defaults
	assert.Equal(t, "gigaam", cfg.STT.Provider)
	assert.Equal(t, "http://localhost:8008/v1", cfg.STT.GigaAM.BaseURL)
	assert.Empty(t, cfg.STT.GigaAM.Model)
	assert.Empty(t, cfg.STT.ElevenLabs.APIKey)
	assert.Empty(t, cfg.STT.ElevenLabs.Model)
}

func TestLoad_FullConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	input := Config{
		VAD: VADConfig{
			ModelPath:   "/models/silero_vad.onnx",
			Threshold:   0.50,
			Hysteresis:  0.20,
			BargeInMs:   128,
			ReleaseMs:   500,
			EndOfTurnMs: 1000,
			PrerollMs:   200,
		},
		STT: STTConfig{
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
	assert.Equal(t, "/models/silero_vad.onnx", cfg.VAD.ModelPath)
	assert.Equal(t, float32(0.50), cfg.VAD.Threshold)
	assert.Equal(t, float32(0.20), cfg.VAD.Hysteresis)
	assert.Equal(t, 128, cfg.VAD.BargeInMs)
	assert.Equal(t, 500, cfg.VAD.ReleaseMs)
	assert.Equal(t, 1000, cfg.VAD.EndOfTurnMs)
	assert.Equal(t, 200, cfg.VAD.PrerollMs)

	// STT
	assert.Equal(t, "elevenlabs", cfg.STT.Provider)
	assert.Equal(t, "http://custom:8008/v1", cfg.STT.GigaAM.BaseURL)
	assert.Equal(t, "custom-model", cfg.STT.GigaAM.Model)
	assert.Equal(t, "sk-abc123", cfg.STT.ElevenLabs.APIKey)
	assert.Equal(t, "eleven_multilingual_v2", cfg.STT.ElevenLabs.Model)
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
		"stt": map[string]any{
			"provider": "elevenlabs",
		},
	})

	cfg, err := Load("")
	assert.NoError(t, err)
	assert.NotNil(t, cfg)
	assert.Equal(t, "elevenlabs", cfg.STT.Provider)
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

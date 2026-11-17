package asr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/xcono/asr/pkg/audio"
)

// ElevenLabs posts whole utterances to the ElevenLabs speech-to-text API
// (Scribe). Unlike Transcriber it is not OpenAI-compatible: the path is
// /v1/speech-to-text, auth is the xi-api-key header, and the model travels
// as the model_id form field.
type ElevenLabs struct {
	APIKey       string
	Model        string       // "" => "scribe_v2" (scribe_v1 is deprecated upstream)
	LanguageCode string       // ISO-639-1/-3 hint, e.g. "ru"; "" => server auto-detect
	BaseURL      string       // "" => "https://api.elevenlabs.io"
	Client       *http.Client // nil => a client with DefaultTimeout
}

const (
	defaultElevenBaseURL = "https://api.elevenlabs.io"
	defaultElevenModel   = "scribe_v2"
)

// Transcribe encodes the samples as a WAV clip and POSTs them as a multipart
// upload to {BaseURL}/v1/speech-to-text, returning the recognised text.
func (e *ElevenLabs) Transcribe(ctx context.Context, samples []int16, rate int) (string, error) {
	wav := audio.EncodeWAV(samples, rate)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", "audio.wav")
	if err != nil {
		return "", err
	}
	if _, err := fw.Write(wav); err != nil {
		return "", err
	}
	model := e.Model
	if model == "" {
		model = defaultElevenModel
	}
	_ = mw.WriteField("model_id", model)
	if e.LanguageCode != "" {
		_ = mw.WriteField("language_code", e.LanguageCode)
	}
	// Audio-event tags like "(смех)" would leak into the answer prompt.
	_ = mw.WriteField("tag_audio_events", "false")
	if err := mw.Close(); err != nil {
		return "", err
	}

	base := e.BaseURL
	if base == "" {
		base = defaultElevenBaseURL
	}
	url := strings.TrimRight(base, "/") + "/v1/speech-to-text"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("xi-api-key", e.APIKey)

	client := e.Client
	if client == nil {
		client = &http.Client{Timeout: DefaultTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("asr: elevenlabs status %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	var out struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("asr: decode elevenlabs response: %w", err)
	}
	return strings.TrimSpace(out.Text), nil
}

// Package asr is the speech-to-text domain: the batch (OpenAI-compatible STT
// endpoint) and streaming (WebSocket) transport clients, plus Listen — the
// capture → VAD → transcribe turn pipeline that turns a stream of audio windows
// into SpeechStart/SpeechEnd/SpeechText events for the orchestrator to consume.
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

	"github.com/xcono/vox/pkg/audio"
)

// Transcriber posts whole utterances to an OpenAI-compatible STT endpoint.
type Transcriber struct {
	BaseURL string       // e.g. "http://localhost:8008/v1"
	Model   string       // optional; sent as the "model" form field
	Client  *http.Client // nil => http.DefaultClient
}

// Transcribe encodes the samples as a WAV clip and POSTs them as a multipart
// upload to {BaseURL}/audio/transcriptions, returning the recognised text.
func (tr *Transcriber) Transcribe(ctx context.Context, samples []int16, rate int) (string, error) {
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
	_ = mw.WriteField("response_format", "json")
	if tr.Model != "" {
		_ = mw.WriteField("model", tr.Model)
	}
	if err := mw.Close(); err != nil {
		return "", err
	}

	url := strings.TrimRight(tr.BaseURL, "/") + "/audio/transcriptions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	client := tr.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("asr: status %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	var out struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("asr: decode response: %w", err)
	}
	return strings.TrimSpace(out.Text), nil
}

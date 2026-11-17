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
	"time"

	"github.com/xcono/asr/pkg/audio"
)

// Transcriber posts whole utterances to an OpenAI-compatible STT endpoint.
//
// Optional fields are sent only when non-empty, so a server that never asked
// for them (GigaAM, Qwen) sees exactly the same form as before they existed.
type Transcriber struct {
	BaseURL string // e.g. "http://localhost:8008/v1"
	Model   string // optional; sent as the "model" form field
	// Language is the optional "language" form field. whisper.cpp falls back to
	// its server-side -l when it is absent, and with a fixed language it
	// translates speech in any other language instead of transcribing it — so
	// set "auto" here when the speaker's language is not known in advance.
	Language string
	// Prompt is the optional "prompt" form field. whisper.cpp uses it as
	// decoder context, which steers the spelling of terms toward the prompt's
	// forms; it does not reliably fix misheard words on short clips.
	Prompt string
	// Verbose asks for response_format=verbose_json (plus
	// no_language_probabilities=true, which spares whisper.cpp an extra
	// language-detection pass it would otherwise run for that format), so the
	// reply carries per-segment no_speech_prob / avg_logprob for the log. Only
	// whisper.cpp sets it: the GigaAM server rejects any format but json|text
	// with HTTP 400.
	Verbose bool
	Client  *http.Client // nil => a client with DefaultTimeout
}

// Segment holds the confidence scores whisper.cpp reports for one decoded
// segment of a verbose_json reply. They are logged, not acted on: no threshold
// has been chosen yet. NoSpeechProb is computed once per decoding window, so
// every segment of a short utterance carries the same value.
type Segment struct {
	NoSpeechProb float64 `json:"no_speech_prob"`
	AvgLogprob   float64 `json:"avg_logprob"`
}

// Transcript is a recognised utterance plus whatever scores the server sent.
// Segments is nil for a plain json reply or a server without verbose_json.
type Transcript struct {
	Text     string
	Segments []Segment
}

// ScoredRecognizer is the optional extension of Recognizer that also returns
// the server's scores. Listen uses it when the recogniser implements it, so
// Recognizer itself — and every implementation outside this package — stays
// unchanged.
type ScoredRecognizer interface {
	TranscribeScored(ctx context.Context, samples []int16, rate int) (Transcript, error)
}

// DefaultTimeout caps a single Transcribe HTTP request so a hung STT server
// cannot hold a transcription goroutine indefinitely (the listen ctx is
// process-lifetime and won't cancel it). Override via Transcriber.Client.
const DefaultTimeout = 30 * time.Second

// Transcribe encodes the samples as a WAV clip and POSTs them as a multipart
// upload to {BaseURL}/audio/transcriptions, returning the recognised text.
func (tr *Transcriber) Transcribe(ctx context.Context, samples []int16, rate int) (string, error) {
	t, err := tr.TranscribeScored(ctx, samples, rate)
	return t.Text, err
}

// TranscribeScored is Transcribe plus the per-segment scores of a verbose_json
// reply (see Verbose). A reply without "segments" decodes to nil Segments.
func (tr *Transcriber) TranscribeScored(ctx context.Context, samples []int16, rate int) (Transcript, error) {
	wav := audio.EncodeWAV(samples, rate)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", "audio.wav")
	if err != nil {
		return Transcript{}, err
	}
	if _, err := fw.Write(wav); err != nil {
		return Transcript{}, err
	}
	if tr.Verbose {
		_ = mw.WriteField("response_format", "verbose_json")
		_ = mw.WriteField("no_language_probabilities", "true")
	} else {
		_ = mw.WriteField("response_format", "json")
	}
	if tr.Model != "" {
		_ = mw.WriteField("model", tr.Model)
	}
	if tr.Language != "" {
		_ = mw.WriteField("language", tr.Language)
	}
	if tr.Prompt != "" {
		_ = mw.WriteField("prompt", tr.Prompt)
	}
	if err := mw.Close(); err != nil {
		return Transcript{}, err
	}

	url := strings.TrimRight(tr.BaseURL, "/") + "/audio/transcriptions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &body)
	if err != nil {
		return Transcript{}, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	client := tr.Client
	if client == nil {
		client = &http.Client{Timeout: DefaultTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return Transcript{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return Transcript{}, fmt.Errorf("asr: status %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	var out struct {
		Text     string    `json:"text"`
		Segments []Segment `json:"segments"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Transcript{}, fmt.Errorf("asr: decode response: %w", err)
	}
	return Transcript{Text: oneLine(out.Text), Segments: out.Segments}, nil
}

// oneLine flattens a transcript to a single line. whisper.cpp ends every
// segment with "\n" and starts the next with a space, so a long utterance comes
// back as " seg1.\n seg2.\n"; consumers expect one line of text per turn. Each
// line is trimmed and the non-empty ones are joined with a single space, which
// leaves an ordinary one-line response exactly as strings.TrimSpace would.
func oneLine(text string) string {
	parts := strings.Split(text, "\n")
	kept := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, " ")
}

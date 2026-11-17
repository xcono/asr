package asr

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestElevenLabsTranscribeReturnsText drives Transcribe against a fake
// ElevenLabs server and asserts it posts a WAV multipart to /v1/speech-to-text
// with the xi-api-key header and Scribe form fields, returning "text".
func TestElevenLabsTranscribeReturnsText(t *testing.T) {
	const want = "привет мир, deploy прошёл"
	var gotPath, gotKey, gotModel, gotLang, gotTags, fileMagic string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotKey = r.URL.Path, r.Header.Get("xi-api-key")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		gotModel = r.FormValue("model_id")
		gotLang = r.FormValue("language_code")
		gotTags = r.FormValue("tag_audio_events")
		f, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "no file field", http.StatusBadRequest)
			return
		}
		defer f.Close()
		hdr := make([]byte, 4)
		_, _ = io.ReadFull(f, hdr)
		fileMagic = string(hdr)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"language_code": "ru",
			"text":          want,
		})
	}))
	defer srv.Close()

	e := &ElevenLabs{APIKey: "k", LanguageCode: "ru", BaseURL: srv.URL}
	got, err := e.Transcribe(context.Background(), []int16{1, 2, 3, 4}, 16000)
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}

	if got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
	if gotPath != "/v1/speech-to-text" {
		t.Fatalf("path = %s, want /v1/speech-to-text", gotPath)
	}
	if gotKey != "k" {
		t.Fatalf("xi-api-key = %q, want %q", gotKey, "k")
	}
	if gotModel != defaultElevenModel {
		t.Fatalf("model_id = %q, want default %q", gotModel, defaultElevenModel)
	}
	if gotLang != "ru" {
		t.Fatalf("language_code = %q, want %q", gotLang, "ru")
	}
	if gotTags != "false" {
		t.Fatalf("tag_audio_events = %q, want %q", gotTags, "false")
	}
	if fileMagic != "RIFF" {
		t.Fatalf("uploaded file magic = %q, want RIFF (a WAV container)", fileMagic)
	}
}

// TestElevenLabsTranscribeServerError asserts a non-2xx response becomes an
// error naming the status, rather than empty text pretending to be success.
func TestElevenLabsTranscribeServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"detail":"invalid api key"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	e := &ElevenLabs{APIKey: "bad", BaseURL: srv.URL}
	if _, err := e.Transcribe(context.Background(), []int16{1, 2}, 16000); err == nil {
		t.Fatal("expected an error on HTTP 401, got nil")
	} else if !strings.Contains(err.Error(), "401") {
		t.Fatalf("error %q should mention status 401", err)
	}
}

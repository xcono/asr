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

// TestTranscribeReturnsText drives Transcribe against a fake OpenAI-compatible
// server and asserts it posts a WAV multipart to /audio/transcriptions and
// returns the server's "text" field.
func TestTranscribeReturnsText(t *testing.T) {
	const want = "привет мир"
	var gotPath, gotMethod, fileMagic string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
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
			"text":  want,
			"usage": map[string]any{"type": "duration", "seconds": 1},
		})
	}))
	defer srv.Close()

	tr := &Transcriber{BaseURL: srv.URL + "/v1"}
	got, err := tr.Transcribe(context.Background(), []int16{1, 2, 3, 4}, 16000)
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}

	if got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("method = %s, want POST", gotMethod)
	}
	if gotPath != "/v1/audio/transcriptions" {
		t.Fatalf("path = %s, want /v1/audio/transcriptions", gotPath)
	}
	if fileMagic != "RIFF" {
		t.Fatalf("uploaded file magic = %q, want RIFF (a WAV container)", fileMagic)
	}
}

// TestTranscribeServerError asserts a non-2xx response becomes an error that
// names the status, rather than returning empty text as if it succeeded.
func TestTranscribeServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	tr := &Transcriber{BaseURL: srv.URL + "/v1"}
	if _, err := tr.Transcribe(context.Background(), []int16{1, 2}, 16000); err == nil {
		t.Fatal("expected an error on HTTP 500, got nil")
	} else if !strings.Contains(err.Error(), "500") {
		t.Fatalf("error %q should mention status 500", err)
	}
}

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

// formFields captures the multipart form a fake STT server received, so the
// tests below can assert which optional fields were (not) sent.
func formFields(t *testing.T, reply string, tr *Transcriber) (map[string][]string, string) {
	t.Helper()
	var got map[string][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		got = r.MultipartForm.Value
		_ = json.NewEncoder(w).Encode(map[string]any{"text": reply})
	}))
	defer srv.Close()

	tr.BaseURL = srv.URL + "/v1"
	text, err := tr.Transcribe(context.Background(), []int16{1, 2, 3, 4}, 16000)
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	return got, text
}

// TestTranscribeSendsLanguageAndPrompt asserts the whisper.cpp fields reach the
// form verbatim when set.
func TestTranscribeSendsLanguageAndPrompt(t *testing.T) {
	got, _ := formFields(t, "ok", &Transcriber{Language: "ru", Prompt: "Kibana, race condition"})
	if v := got["language"]; len(v) != 1 || v[0] != "ru" {
		t.Fatalf("language = %v, want [ru]", v)
	}
	if v := got["prompt"]; len(v) != 1 || v[0] != "Kibana, race condition" {
		t.Fatalf("prompt = %v, want [Kibana, race condition]", v)
	}
}

// TestTranscribeOmitsEmptyOptionalFields guards the GigaAM/Qwen request shape:
// with Model/Language/Prompt unset the form carries only response_format.
func TestTranscribeOmitsEmptyOptionalFields(t *testing.T) {
	got, _ := formFields(t, "ok", &Transcriber{})
	if len(got) != 1 || got["response_format"] == nil {
		t.Fatalf("form fields = %v, want only response_format", got)
	}
}

// TestTranscribeFlattensSegments covers whisper.cpp's multi-segment reply
// (" seg.\n seg.\n") and checks a plain one-line reply is only trimmed.
func TestTranscribeFlattensSegments(t *testing.T) {
	cases := []struct{ reply, want string }{
		{" Так, коллеги.\n Я задеплоил релиз.\n Сначала грешил на кэш.\n", "Так, коллеги. Я задеплоил релиз. Сначала грешил на кэш."},
		{" привет  мир\n", "привет  мир"},
		{"привет мир", "привет мир"},
		{"\n \n", ""},
	}
	for _, c := range cases {
		if _, got := formFields(t, c.reply, &Transcriber{}); got != c.want {
			t.Errorf("reply %q: text = %q, want %q", c.reply, got, c.want)
		}
	}
}

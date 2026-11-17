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

// TestTranscribeVerboseFields: Verbose asks for verbose_json and skips the
// language-probability pass; without it the form says json (GigaAM's only
// accepted structured format).
func TestTranscribeVerboseFields(t *testing.T) {
	got, _ := formFields(t, "ok", &Transcriber{Verbose: true})
	if v := got["response_format"]; len(v) != 1 || v[0] != "verbose_json" {
		t.Fatalf("response_format = %v, want [verbose_json]", v)
	}
	if v := got["no_language_probabilities"]; len(v) != 1 || v[0] != "true" {
		t.Fatalf("no_language_probabilities = %v, want [true]", v)
	}

	got, _ = formFields(t, "ok", &Transcriber{})
	if v := got["response_format"]; len(v) != 1 || v[0] != "json" {
		t.Fatalf("response_format = %v, want [json]", v)
	}
	if _, ok := got["no_language_probabilities"]; ok {
		t.Fatal("no_language_probabilities sent without Verbose")
	}
}

// scoredReply serves body verbatim and returns what TranscribeScored decoded.
func scoredReply(t *testing.T, body string) Transcript {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	tr := &Transcriber{BaseURL: srv.URL + "/v1", Verbose: true}
	got, err := tr.TranscribeScored(context.Background(), []int16{1, 2, 3, 4}, 16000)
	if err != nil {
		t.Fatalf("TranscribeScored: %v", err)
	}
	return got
}

// TestTranscribeScoredParsesSegments: a whisper.cpp verbose_json reply keeps
// the full top-level text and every segment's scores.
func TestTranscribeScoredParsesSegments(t *testing.T) {
	got := scoredReply(t, `{
		"task": "transcribe", "language": "russian", "duration": 1.1,
		"text": " Редактор субтитров А.Семкин\n Корректор А.Егорова\n",
		"segments": [
			{"id": 0, "text": " Редактор субтитров А.Семкин", "avg_logprob": -0.61, "no_speech_prob": 0.42,
			 "words": [{"word": " Редактор", "probability": 0.3}]},
			{"id": 1, "text": " Корректор А.Егорова", "avg_logprob": -0.48, "no_speech_prob": 0.42}
		]
	}`)
	if want := "Редактор субтитров А.Семкин Корректор А.Егорова"; got.Text != want {
		t.Fatalf("text = %q, want %q", got.Text, want)
	}
	want := []Segment{{NoSpeechProb: 0.42, AvgLogprob: -0.61}, {NoSpeechProb: 0.42, AvgLogprob: -0.48}}
	if len(got.Segments) != len(want) || got.Segments[0] != want[0] || got.Segments[1] != want[1] {
		t.Fatalf("segments = %+v, want %+v", got.Segments, want)
	}
}

// TestTranscribeScoredWithoutSegments: a plain json reply (GigaAM, or a server
// ignoring verbose_json) decodes without error and without scores.
func TestTranscribeScoredWithoutSegments(t *testing.T) {
	got := scoredReply(t, `{"text": "привет мир"}`)
	if got.Text != "привет мир" || got.Segments != nil {
		t.Fatalf("got %+v, want text only", got)
	}
}

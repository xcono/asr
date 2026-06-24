package asr

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestWSURLFromHTTP(t *testing.T) {
	cases := []struct{ in, want string }{
		{"http://localhost:8008/v1", "ws://localhost:8008/v1/asr/stream"},
		{"https://asr.example.com/v1", "wss://asr.example.com/v1/asr/stream"},
		{"http://127.0.0.1:9000", "ws://127.0.0.1:9000/v1/asr/stream"},
		{"https://asr.example.com:8443/v1/", "wss://asr.example.com:8443/v1/asr/stream"},
	}
	for _, c := range cases {
		if got := WSURLFromHTTP(c.in); got != c.want {
			t.Errorf("WSURLFromHTTP(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// fakeASRStream mirrors asr_server.py's /v1/asr/stream contract: each binary
// frame yields a growing cumulative partial; {"type":"finish"} yields a final
// then a normal close.
func fakeASRStream(t *testing.T) (wsURL string, closeFn func()) {
	t.Helper()
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		words := []string{"привет", "мир", "это", "тест"}
		cumulative, n := "", 0
		for {
			mt, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			switch mt {
			case websocket.BinaryMessage:
				if n < len(words) {
					cumulative = strings.TrimSpace(cumulative + " " + words[n])
					n++
				}
				_ = c.WriteJSON(map[string]string{"type": "partial", "text": cumulative, "language": "ru"})
			case websocket.TextMessage:
				var ctrl struct {
					Type string `json:"type"`
				}
				_ = json.Unmarshal(data, &ctrl)
				if ctrl.Type == "finish" {
					_ = c.WriteJSON(map[string]string{"type": "final", "text": cumulative})
					_ = c.WriteMessage(websocket.CloseMessage,
						websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
					return
				}
			}
		}
	}))
	return "ws://" + strings.TrimPrefix(srv.URL, "http://") + "/v1/asr/stream", srv.Close
}

// TestStreamSessionPartialsThenFinal drives the whole streaming client against
// a fake server: binary frames produce growing partials, finish produces a
// final, and the session ends (socket closed) afterward.
func TestStreamSessionPartialsThenFinal(t *testing.T) {
	wsURL, done := fakeASRStream(t)
	defer done()

	msgs := make(chan StreamMsg, 16)
	sess, err := DialStream(context.Background(), wsURL, func(m StreamMsg) { msgs <- m })
	if err != nil {
		t.Fatalf("DialStream: %v", err)
	}

	if err := sess.SendPCM([]int16{1, 2, 3, 4}); err != nil {
		t.Fatalf("SendPCM: %v", err)
	}
	m := <-msgs
	if m.Kind != MsgPartial || m.Text == "" {
		t.Fatalf("want non-empty partial, got %+v", m)
	}
	first := m.Text

	if err := sess.SendPCM([]int16{5, 6, 7, 8}); err != nil {
		t.Fatalf("SendPCM: %v", err)
	}
	m = <-msgs
	if m.Kind != MsgPartial || len([]rune(m.Text)) < len([]rune(first)) {
		t.Fatalf("partial should grow: %q -> %q", first, m.Text)
	}

	if err := sess.Finish(); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	m = <-msgs
	if m.Kind != MsgFinal || m.Text == "" {
		t.Fatalf("want non-empty final, got %+v", m)
	}

	select {
	case <-sess.done:
	case <-time.After(2 * time.Second):
		t.Fatal("session did not end after final + server close")
	}
}

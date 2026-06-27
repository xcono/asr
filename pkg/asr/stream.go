package asr

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/xcono/asr/pkg/audio"
)

// Streaming message kinds, mirroring the server's JSON "type" field.
const (
	MsgPartial = "partial"
	MsgFinal   = "final"
	MsgError   = "error"
)

// StreamMsg is one decoded server message handed to the session's callback.
type StreamMsg struct {
	Kind   string // MsgPartial | MsgFinal | MsgError
	Text   string
	Detail string // populated for MsgError
}

// WSURLFromHTTP derives the ws(s) streaming URL from the http(s) /v1 base URL.
// The endpoint is mounted at /v1/asr/stream regardless of the base's path.
func WSURLFromHTTP(base string) string {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil {
		return base
	}
	scheme := "ws"
	if u.Scheme == "https" {
		scheme = "wss"
	}
	return scheme + "://" + u.Host + "/v1/asr/stream"
}

// StreamSession is one open WebSocket streaming session (== one turn). A reader
// goroutine pumps decoded messages to onMsg until the socket closes.
type StreamSession struct {
	conn *websocket.Conn
	done chan struct{} // closed when the reader goroutine exits
	mu   sync.Mutex
}

// DialStream opens a streaming session and starts its reader. onMsg is invoked
// from the reader goroutine for each server message (partial/final/error).
func DialStream(ctx context.Context, wsURL string, onMsg func(StreamMsg)) (*StreamSession, error) {
	d := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, _, err := d.DialContext(ctx, wsURL, nil)
	if err != nil {
		return nil, err
	}
	s := &StreamSession{conn: conn, done: make(chan struct{})}
	go s.read(onMsg)
	return s, nil
}

func (s *StreamSession) read(onMsg func(StreamMsg)) {
	defer close(s.done)
	defer s.conn.Close()
	for {
		_, data, err := s.conn.ReadMessage()
		if err != nil {
			return // server closed (after final) or transport error
		}
		var m struct {
			Type   string `json:"type"`
			Text   string `json:"text"`
			Detail string `json:"detail"`
		}
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		onMsg(StreamMsg{Kind: m.Type, Text: m.Text, Detail: m.Detail})
	}
}

// SendPCM streams one window of samples as a binary frame.
func (s *StreamSession) SendPCM(samples []int16) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn.WriteMessage(websocket.BinaryMessage, audio.PCM16LE(samples))
}

// Finish signals end-of-utterance. The server replies with a final message then
// closes; the reader goroutine surfaces that final and exits.
func (s *StreamSession) Finish() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"finish"}`))
}

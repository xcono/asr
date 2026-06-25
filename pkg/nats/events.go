package nats

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/nats-io/nats.go"
)

// NATS subject constants.
const (
	SubjectVADStart = "vox.vad.speaking.start"
	SubjectVADStop  = "vox.vad.speaking.stop"
	SubjectSTTMsg   = "vox.stt.message"
)

// VADEvent is published when VAD detects speech start or stop.
type VADEvent struct {
	Timestamp time.Time `json:"timestamp"`
}

// MessageEvent is published when STT produces a transcription.
type MessageEvent struct {
	Timestamp   time.Time `json:"timestamp"`
	Text        string    `json:"text"`
	VoiceFileID string    `json:"voice_file_id,omitempty"`
}

// PublishVADStart publishes a speech-start event.
func (s *Server) PublishVADStart(ts time.Time) error {
	return s.publish(SubjectVADStart, "vad-start-"+strconv.FormatInt(ts.UnixNano(), 10), &VADEvent{Timestamp: ts})
}

// PublishVADStop publishes a speech-stop event.
func (s *Server) PublishVADStop(ts time.Time) error {
	return s.publish(SubjectVADStop, "vad-stop-"+strconv.FormatInt(ts.UnixNano(), 10), &VADEvent{Timestamp: ts})
}

// PublishMessage publishes an STT transcription event.
func (s *Server) PublishMessage(ts time.Time, text, voiceFileID string) error {
	return s.publish(SubjectSTTMsg, "stt-"+strconv.FormatInt(ts.UnixNano(), 10), &MessageEvent{
		Timestamp:   ts,
		Text:        text,
		VoiceFileID: voiceFileID,
	})
}

// publish marshals and publishes an event to the given JetStream-monitored
// subject, deduplicating by id (Nats-Msg-Id) so a retry of the same logical
// event is not stored twice. The JetStream sync publish ack confirms that the
// message has been persisted — a missing core publish acknowledgement was the
// reason this moved off nc.Publish.
func (s *Server) publish(subject, id string, event interface{}) error {
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	if _, err := s.js.Publish(subject, data, nats.MsgId(id)); err != nil {
		return fmt.Errorf("nats: publish %s: %w", subject, err)
	}
	return nil
}

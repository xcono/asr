package nats

import (
	"encoding/json"
	"fmt"
	"time"
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
	return s.publish(SubjectVADStart, &VADEvent{Timestamp: ts})
}

// PublishVADStop publishes a speech-stop event.
func (s *Server) PublishVADStop(ts time.Time) error {
	return s.publish(SubjectVADStop, &VADEvent{Timestamp: ts})
}

// PublishMessage publishes an STT transcription event.
func (s *Server) PublishMessage(ts time.Time, text, voiceFileID string) error {
	return s.publish(SubjectSTTMsg, &MessageEvent{
		Timestamp:   ts,
		Text:        text,
		VoiceFileID: voiceFileID,
	})
}

// publish marshals and publishes an event to the given subject.
func (s *Server) publish(subject string, event interface{}) error {
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	return s.nc.Publish(subject, data)
}

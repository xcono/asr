package nats

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewServerStartAndClose(t *testing.T) {
	s, err := NewServer(0, t.TempDir(), 0, 0)
	require.NoError(t, err)
	require.NotNil(t, s)
	require.NotNil(t, s.Conn())
	require.NotNil(t, s.JS())
	s.Close()
}

func TestPublishVADEvents(t *testing.T) {
	s, err := NewServer(0, t.TempDir(), 0, 0)
	require.NoError(t, err)
	defer s.Close()

	ts := time.Now()
	err = s.PublishVADStart(ts)
	assert.NoError(t, err)

	err = s.PublishVADStop(ts)
	assert.NoError(t, err)
}

func TestPublishMessageEvent(t *testing.T) {
	s, err := NewServer(0, t.TempDir(), 0, 0)
	require.NoError(t, err)
	defer s.Close()

	ts := time.Now()
	err = s.PublishMessage(ts, "hello world", "file123.wav")
	assert.NoError(t, err)
}

func TestSubscribeAndReceiveVADStart(t *testing.T) {
	s, err := NewServer(0, t.TempDir(), 0, 0)
	require.NoError(t, err)
	defer s.Close()

	sub, err := s.Conn().SubscribeSync(SubjectVADStart)
	require.NoError(t, err)
	defer sub.Unsubscribe()

	ts := time.Now().Truncate(time.Millisecond)
	err = s.PublishVADStart(ts)
	require.NoError(t, err)

	msg, err := sub.NextMsg(2 * time.Second)
	require.NoError(t, err)
	require.NotNil(t, msg)

	var got VADEvent
	err = json.Unmarshal(msg.Data, &got)
	require.NoError(t, err)
	assert.Equal(t, ts.UnixMilli(), got.Timestamp.UnixMilli())
}

func TestSubscribeAndReceiveVADStop(t *testing.T) {
	s, err := NewServer(0, t.TempDir(), 0, 0)
	require.NoError(t, err)
	defer s.Close()

	sub, err := s.Conn().SubscribeSync(SubjectVADStop)
	require.NoError(t, err)
	defer sub.Unsubscribe()

	ts := time.Now().Truncate(time.Millisecond)
	err = s.PublishVADStop(ts)
	require.NoError(t, err)

	msg, err := sub.NextMsg(2 * time.Second)
	require.NoError(t, err)

	var got VADEvent
	err = json.Unmarshal(msg.Data, &got)
	require.NoError(t, err)
	assert.Equal(t, ts.UnixMilli(), got.Timestamp.UnixMilli())
}

func TestSubscribeAndReceiveMessage(t *testing.T) {
	s, err := NewServer(0, t.TempDir(), 0, 0)
	require.NoError(t, err)
	defer s.Close()

	sub, err := s.Conn().SubscribeSync(SubjectSTTMsg)
	require.NoError(t, err)
	defer sub.Unsubscribe()

	ts := time.Now().Truncate(time.Millisecond)
	err = s.PublishMessage(ts, "hello world", "file123.wav")
	require.NoError(t, err)

	msg, err := sub.NextMsg(2 * time.Second)
	require.NoError(t, err)

	var got MessageEvent
	err = json.Unmarshal(msg.Data, &got)
	require.NoError(t, err)
	assert.Equal(t, ts.UnixMilli(), got.Timestamp.UnixMilli())
	assert.Equal(t, "hello world", got.Text)
	assert.Equal(t, "file123.wav", got.VoiceFileID)
}

func TestSubscribeAndReceiveMessageEmptyVoiceFileID(t *testing.T) {
	s, err := NewServer(0, t.TempDir(), 0, 0)
	require.NoError(t, err)
	defer s.Close()

	sub, err := s.Conn().SubscribeSync(SubjectSTTMsg)
	require.NoError(t, err)
	defer sub.Unsubscribe()

	ts := time.Now().Truncate(time.Millisecond)
	err = s.PublishMessage(ts, "no file", "")
	require.NoError(t, err)

	msg, err := sub.NextMsg(2 * time.Second)
	require.NoError(t, err)

	var got MessageEvent
	err = json.Unmarshal(msg.Data, &got)
	require.NoError(t, err)
	assert.Equal(t, "no file", got.Text)
	assert.Empty(t, got.VoiceFileID)
}

func TestStreamsExist(t *testing.T) {
	s, err := NewServer(0, t.TempDir(), 0, 0)
	require.NoError(t, err)
	defer s.Close()

	for _, name := range []string{"VAD", "STT"} {
		stream, err := s.JS().StreamInfo(name)
		require.NoError(t, err, "stream %s should exist", name)
		require.NotNil(t, stream)
	}
}

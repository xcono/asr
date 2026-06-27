module github.com/xcono/asr

go 1.25.0

require (
	github.com/gordonklaus/portaudio v0.0.0-20260203164431-765aa7dfa631
	github.com/gorilla/websocket v1.5.3
	github.com/streamer45/silero-vad-go v0.0.0-00010101000000-000000000000
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/stretchr/testify v1.10.0
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/streamer45/silero-vad-go => github.com/hylarucoder/silero-vad-onnx-go v0.0.0-20250225204146-bf1977c1c97c

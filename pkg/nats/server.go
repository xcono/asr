package nats

import (
	"fmt"
	"log"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// Server wraps an embedded NATS server and a client connection.
type Server struct {
	ns *server.Server
	nc *nats.Conn
	js nats.JetStreamContext
}

// NewServer starts an embedded NATS server with Jetstream enabled.
// port is the client port (e.g. 4222, or 0 for random), storeDir is the
// Jetstream storage directory.
func NewServer(port int, storeDir string) (*Server, error) {
	opts := &server.Options{
		Port:      port,
		StoreDir:  storeDir,
		JetStream: true,
		NoLog:     true,
	}
	ns, err := server.NewServer(opts)
	if err != nil {
		return nil, fmt.Errorf("create nats server: %w", err)
	}
	go ns.Start()
	if !ns.ReadyForConnections(5 * time.Second) {
		return nil, fmt.Errorf("nats server did not start")
	}

	nc, err := nats.Connect(ns.ClientURL())
	if err != nil {
		ns.Shutdown()
		return nil, fmt.Errorf("connect to nats: %w", err)
	}

	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		ns.Shutdown()
		return nil, fmt.Errorf("jetstream: %w", err)
	}

	s := &Server{ns: ns, nc: nc, js: js}
	if err := s.setupStreams(); err != nil {
		s.Close()
		return nil, err
	}
	log.Printf("nats server started on %s", ns.ClientURL())
	return s, nil
}

// Conn returns the NATS client connection.
func (s *Server) Conn() *nats.Conn { return s.nc }

// JS returns the JetStream context.
func (s *Server) JS() nats.JetStreamContext { return s.js }

// Close shuts down the client connection and server.
func (s *Server) Close() {
	if s.nc != nil {
		s.nc.Close()
	}
	if s.ns != nil {
		s.ns.Shutdown()
	}
}

// setupStreams creates the Jetstream streams for VAD and STT events.
func (s *Server) setupStreams() error {
	streams := []*nats.StreamConfig{
		{
			Name:     "VAD",
			Subjects: []string{"vox.vad.>"},
			Replicas: 1,
		},
		{
			Name:     "STT",
			Subjects: []string{"vox.stt.>"},
			Replicas: 1,
		},
	}
	for _, cfg := range streams {
		if _, err := s.js.AddStream(cfg); err != nil {
			return fmt.Errorf("add stream %s: %w", cfg.Name, err)
		}
	}
	return nil
}

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sync"
)

// IPCServer manages Unix socket communication with PicoClaw.
type IPCServer struct {
	listener net.Listener
	conn     net.Conn
	reader   *bufio.Reader
	mu       sync.Mutex
	sockPath string
}

// NewIPCServer creates a Unix socket server at the given path.
func NewIPCServer(sockPath string) (*IPCServer, error) {
	// Remove stale socket
	os.Remove(sockPath)

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on %s: %w", sockPath, err)
	}

	s := &IPCServer{
		listener: listener,
		sockPath: sockPath,
	}

	// Accept connections in background
	go s.acceptLoop()

	return s, nil
}

// acceptLoop accepts incoming connections from PicoClaw.
func (s *IPCServer) acceptLoop() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return // listener closed
		}

		s.mu.Lock()
		// Close old connection if exists
		if s.conn != nil {
			s.conn.Close()
		}
		s.conn = conn
		s.reader = bufio.NewReader(conn)
		s.mu.Unlock()
	}
}

// Send writes a JSON message to the connected PicoClaw client.
func (s *IPCServer) Send(msg interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.conn == nil {
		return fmt.Errorf("no PicoClaw client connected")
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}
	data = append(data, '\n')

	if _, err := s.conn.Write(data); err != nil {
		s.conn.Close()
		s.conn = nil
		s.reader = nil
		return fmt.Errorf("failed to write to PicoClaw: %w", err)
	}

	return nil
}

// Receive reads a newline-delimited JSON message from PicoClaw.
func (s *IPCServer) Receive() ([]byte, error) {
	s.mu.Lock()
	reader := s.reader
	s.mu.Unlock()

	if reader == nil {
		return nil, fmt.Errorf("no PicoClaw client connected")
	}

	line, err := reader.ReadBytes('\n')
	if err != nil {
		s.mu.Lock()
		if s.conn != nil {
			s.conn.Close()
			s.conn = nil
			s.reader = nil
		}
		s.mu.Unlock()
		return nil, fmt.Errorf("read error: %w", err)
	}

	return line, nil
}

// Close shuts down the IPC server.
func (s *IPCServer) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.conn != nil {
		s.conn.Close()
	}
	s.listener.Close()
	os.Remove(s.sockPath)
}

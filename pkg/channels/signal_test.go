package channels

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
)

func TestParseBridgeURL(t *testing.T) {
	tests := []struct {
		input   string
		network string
		address string
	}{
		{"unix:///tmp/signal.sock", "unix", "/tmp/signal.sock"},
		{"tcp://localhost:9090", "tcp", "localhost:9090"},
		{"localhost:9090", "tcp", "localhost:9090"},
		{"127.0.0.1:8080", "tcp", "127.0.0.1:8080"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			network, address := parseBridgeURL(tt.input)
			assert.Equal(t, tt.network, network)
			assert.Equal(t, tt.address, address)
		})
	}
}

func TestSignalIPCProtocol(t *testing.T) {
	t.Run("marshal inbound message", func(t *testing.T) {
		msg := SignalIPCInbound{
			Type:    "message",
			From:    "+84123456789",
			ChatID:  "+84123456789",
			Content: "Hello from Signal",
			Media:   []string{"/tmp/photo.jpg"},
			Metadata: map[string]string{
				"peer_kind": "direct",
			},
		}

		data, err := json.Marshal(msg)
		require.NoError(t, err)

		var decoded SignalIPCInbound
		err = json.Unmarshal(data, &decoded)
		require.NoError(t, err)
		assert.Equal(t, msg.Type, decoded.Type)
		assert.Equal(t, msg.From, decoded.From)
		assert.Equal(t, msg.Content, decoded.Content)
		assert.Equal(t, msg.Media, decoded.Media)
	})

	t.Run("marshal outbound message", func(t *testing.T) {
		msg := SignalIPCOutbound{
			Type:    "send",
			To:      "+84987654321",
			Content: "Hello back",
		}

		data, err := json.Marshal(msg)
		require.NoError(t, err)

		var decoded SignalIPCOutbound
		err = json.Unmarshal(data, &decoded)
		require.NoError(t, err)
		assert.Equal(t, "send", decoded.Type)
		assert.Equal(t, msg.To, decoded.To)
		assert.Equal(t, msg.Content, decoded.Content)
	})
}

func TestNewSignalChannel(t *testing.T) {
	messageBus := bus.NewMessageBus()
	cfg := config.SignalConfig{
		Enabled:   true,
		BridgeURL: "unix:///tmp/test-signal.sock",
		AllowFrom: config.FlexibleStringSlice{"+84123456789"},
	}

	ch, err := NewSignalChannel(cfg, messageBus)
	require.NoError(t, err)
	assert.Equal(t, "signal", ch.Name())
	assert.False(t, ch.IsRunning())
	assert.Equal(t, "unix:///tmp/test-signal.sock", ch.bridgeURL)
}

func TestSignalChannelSendReceive(t *testing.T) {
	// Create a temp Unix socket for testing
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	// Start mock bridge server
	listener, err := net.Listen("unix", sockPath)
	require.NoError(t, err)
	defer listener.Close()

	var receivedData []byte
	serverReady := make(chan struct{})
	messageReceived := make(chan struct{})

	go func() {
		close(serverReady)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		// Send a test message to the channel
		inbound := SignalIPCInbound{
			Type:    "message",
			From:    "+84111222333",
			ChatID:  "+84111222333",
			Content: "Test message from Signal",
		}
		data, _ := json.Marshal(inbound)
		data = append(data, '\n')
		conn.Write(data)

		// Read the response from Send()
		buf := make([]byte, 4096)
		n, _ := conn.Read(buf)
		receivedData = buf[:n]
		close(messageReceived)
	}()

	<-serverReady
	// Give server a moment to start accepting
	time.Sleep(50 * time.Millisecond)

	messageBus := bus.NewMessageBus()
	cfg := config.SignalConfig{
		Enabled:   true,
		BridgeURL: "unix://" + sockPath,
		AllowFrom: config.FlexibleStringSlice{},
	}

	ch, err := NewSignalChannel(cfg, messageBus)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = ch.Start(ctx)
	require.NoError(t, err)
	assert.True(t, ch.IsRunning())

	// Wait for bridge message to be processed
	time.Sleep(200 * time.Millisecond)

	// Send a reply
	err = ch.Send(ctx, bus.OutboundMessage{
		Channel: "signal",
		ChatID:  "+84111222333",
		Content: "Reply from PicoClaw",
	})
	require.NoError(t, err)

	// Wait for the mock server to receive the message
	select {
	case <-messageReceived:
		// Verify the sent data
		var outbound SignalIPCOutbound
		err = json.Unmarshal(receivedData[:len(receivedData)-1], &outbound) // trim newline
		require.NoError(t, err)
		assert.Equal(t, "send", outbound.Type)
		assert.Equal(t, "+84111222333", outbound.To)
		assert.Equal(t, "Reply from PicoClaw", outbound.Content)
	case <-time.After(3 * time.Second):
		t.Fatal("Timed out waiting for message")
	}

	// Verify inbound message was published to bus
	inMsg, ok := messageBus.ConsumeInbound(ctx)
	assert.True(t, ok)
	assert.Equal(t, "signal", inMsg.Channel)
	assert.Equal(t, "+84111222333", inMsg.SenderID)
	assert.Equal(t, "Test message from Signal", inMsg.Content)

	// Stop
	err = ch.Stop(ctx)
	require.NoError(t, err)
	assert.False(t, ch.IsRunning())
}

func TestSignalChannelReconnect(t *testing.T) {
	messageBus := bus.NewMessageBus()
	cfg := config.SignalConfig{
		Enabled:   true,
		BridgeURL: "unix:///tmp/nonexistent-signal-test.sock",
		AllowFrom: config.FlexibleStringSlice{},
	}

	ch, err := NewSignalChannel(cfg, messageBus)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Start should succeed even if bridge is not available (reconnect loop)
	err = ch.Start(ctx)
	require.NoError(t, err)
	assert.True(t, ch.IsRunning())

	// Send should fail when not connected
	err = ch.Send(ctx, bus.OutboundMessage{
		Channel: "signal",
		ChatID:  "+84111222333",
		Content: "This should fail",
	})
	assert.Error(t, err)

	_ = os.Remove("/tmp/nonexistent-signal-test.sock")

	err = ch.Stop(ctx)
	require.NoError(t, err)
}

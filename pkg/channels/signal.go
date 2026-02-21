package channels

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/utils"
)

const (
	signalReconnectBaseDelay = 1 * time.Second
	signalReconnectMaxDelay  = 30 * time.Second
	signalReadBufferSize     = 64 * 1024 // 64KB read buffer
)

// SignalChannel communicates with picoclaw-signal-bridge via Unix socket or TCP.
// PicoClaw side is pure Go (MIT). All AGPL code lives in the separate bridge process.
type SignalChannel struct {
	*BaseChannel
	config    config.SignalConfig
	bridgeURL string
	conn      net.Conn
	mu        sync.Mutex
	connected bool
}

// NewSignalChannel creates a new Signal channel that connects to the bridge process.
func NewSignalChannel(cfg config.SignalConfig, messageBus *bus.MessageBus) (*SignalChannel, error) {
	base := NewBaseChannel("signal", cfg, messageBus, cfg.AllowFrom)

	return &SignalChannel{
		BaseChannel: base,
		config:      cfg,
		bridgeURL:   cfg.BridgeURL,
		connected:   false,
	}, nil
}

// Start connects to the Signal bridge and begins listening for messages.
func (c *SignalChannel) Start(ctx context.Context) error {
	logger.InfoCF("signal", "Starting Signal channel, connecting to bridge", map[string]interface{}{
		"bridge_url": c.bridgeURL,
	})

	if err := c.connect(); err != nil {
		// Don't fail startup — reconnect loop will retry
		logger.WarnCF("signal", "Initial bridge connection failed, will retry", map[string]interface{}{
			"error": err.Error(),
		})
	}

	c.setRunning(true)
	go c.listen(ctx)

	return nil
}

// Stop disconnects from the Signal bridge.
func (c *SignalChannel) Stop(ctx context.Context) error {
	logger.InfoC("signal", "Stopping Signal channel...")

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		if err := c.conn.Close(); err != nil {
			logger.DebugCF("signal", "Error closing bridge connection", map[string]interface{}{
				"error": err.Error(),
			})
		}
		c.conn = nil
	}

	c.connected = false
	c.setRunning(false)

	return nil
}

// Send sends a message to the Signal bridge for delivery.
func (c *SignalChannel) Send(ctx context.Context, msg bus.OutboundMessage) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return fmt.Errorf("signal bridge not connected")
	}

	payload := SignalIPCOutbound{
		Type:    "send",
		To:      msg.ChatID,
		Content: msg.Content,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal signal message: %w", err)
	}

	// Append newline delimiter for the bridge to parse messages
	data = append(data, '\n')

	if _, err := c.conn.Write(data); err != nil {
		return fmt.Errorf("failed to send to signal bridge: %w", err)
	}

	return nil
}

// connect establishes a connection to the bridge process.
func (c *SignalChannel) connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}

	network, address := parseBridgeURL(c.bridgeURL)

	conn, err := net.DialTimeout(network, address, 10*time.Second)
	if err != nil {
		c.connected = false
		return fmt.Errorf("failed to connect to signal bridge at %s: %w", c.bridgeURL, err)
	}

	c.conn = conn
	c.connected = true
	logger.InfoC("signal", "Connected to Signal bridge")

	return nil
}

// listen reads messages from the bridge in a loop with automatic reconnection.
func (c *SignalChannel) listen(ctx context.Context) {
	delay := signalReconnectBaseDelay

	for {
		select {
		case <-ctx.Done():
			return
		default:
			c.mu.Lock()
			conn := c.conn
			c.mu.Unlock()

			if conn == nil {
				// Try to reconnect
				if err := c.connect(); err != nil {
					logger.DebugCF("signal", "Bridge reconnect failed", map[string]interface{}{
						"error":      err.Error(),
						"retry_in_s": delay.Seconds(),
					})

					select {
					case <-ctx.Done():
						return
					case <-time.After(delay):
					}

					// Exponential backoff
					delay *= 2
					if delay > signalReconnectMaxDelay {
						delay = signalReconnectMaxDelay
					}
					continue
				}
				// Reset delay on successful connect
				delay = signalReconnectBaseDelay
			}

			buf := make([]byte, signalReadBufferSize)
			n, err := conn.Read(buf)
			if err != nil {
				logger.WarnCF("signal", "Bridge read error, will reconnect", map[string]interface{}{
					"error": err.Error(),
				})

				c.mu.Lock()
				if c.conn != nil {
					c.conn.Close()
					c.conn = nil
				}
				c.connected = false
				c.mu.Unlock()

				select {
				case <-ctx.Done():
					return
				case <-time.After(delay):
				}
				continue
			}

			// Process potentially multiple newline-delimited JSON messages
			messages := strings.Split(strings.TrimSpace(string(buf[:n])), "\n")
			for _, raw := range messages {
				raw = strings.TrimSpace(raw)
				if raw == "" {
					continue
				}
				c.handleBridgeMessage([]byte(raw))
			}
		}
	}
}

// handleBridgeMessage processes a single JSON message from the bridge.
func (c *SignalChannel) handleBridgeMessage(data []byte) {
	var msg SignalIPCInbound
	if err := json.Unmarshal(data, &msg); err != nil {
		logger.ErrorCF("signal", "Failed to unmarshal bridge message", map[string]interface{}{
			"error": err.Error(),
		})
		return
	}

	switch msg.Type {
	case "message":
		c.handleIncomingMessage(msg)
	case "status":
		logger.InfoCF("signal", "Bridge status update", map[string]interface{}{
			"content": msg.Content,
		})
	case "error":
		logger.ErrorCF("signal", "Bridge error", map[string]interface{}{
			"content": msg.Content,
		})
	default:
		logger.DebugCF("signal", "Unknown bridge message type", map[string]interface{}{
			"type": msg.Type,
		})
	}
}

// handleIncomingMessage routes an incoming Signal message to the message bus.
func (c *SignalChannel) handleIncomingMessage(msg SignalIPCInbound) {
	senderID := msg.From
	chatID := msg.ChatID
	if chatID == "" {
		chatID = senderID
	}

	content := msg.Content
	if content == "" {
		content = "[empty message]"
	}

	metadata := msg.Metadata
	if metadata == nil {
		metadata = make(map[string]string)
	}

	// Set peer info if not provided by bridge
	if _, ok := metadata["peer_kind"]; !ok {
		if chatID == senderID {
			metadata["peer_kind"] = "direct"
			metadata["peer_id"] = senderID
		} else {
			metadata["peer_kind"] = "group"
			metadata["peer_id"] = chatID
		}
	}

	if !c.IsAllowed(senderID) {
		logger.WarnCF("signal", "🛑 Unauthorized user blocked. To allow, copy the phone number or UUID below into allow_from", map[string]interface{}{
			"sender_id": senderID,
		})
		return
	}

	logger.InfoCF("signal", "Signal message received", map[string]interface{}{
		"sender_id": senderID,
		"chat_id":   chatID,
		"preview":   utils.Truncate(content, 50),
	})

	c.HandleMessage(senderID, chatID, content, msg.Media, metadata)
}

// parseBridgeURL extracts network type and address from a bridge URL.
// Supported formats:
//   - "unix:///path/to/socket" → ("unix", "/path/to/socket")
//   - "tcp://host:port"       → ("tcp", "host:port")
//   - "host:port"             → ("tcp", "host:port") (default)
func parseBridgeURL(url string) (network, address string) {
	if strings.HasPrefix(url, "unix://") {
		return "unix", strings.TrimPrefix(url, "unix://")
	}
	if strings.HasPrefix(url, "tcp://") {
		return "tcp", strings.TrimPrefix(url, "tcp://")
	}
	// Default to TCP
	return "tcp", url
}

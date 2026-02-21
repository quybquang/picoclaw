package channels

// Signal IPC protocol messages for communication between PicoClaw and
// picoclaw-signal-bridge. This file is MIT-licensed and contains NO AGPL code.

// SignalIPCInbound represents an incoming message from the Signal bridge.
type SignalIPCInbound struct {
	Type     string            `json:"type"`              // "message", "status", "error"
	From     string            `json:"from,omitempty"`    // sender phone or UUID
	ChatID   string            `json:"chat_id,omitempty"` // conversation ID
	Content  string            `json:"content,omitempty"` // text content
	Media    []string          `json:"media,omitempty"`   // attachment file paths
	Metadata map[string]string `json:"metadata,omitempty"`
}

// SignalIPCOutbound represents an outgoing message sent to the Signal bridge.
type SignalIPCOutbound struct {
	Type        string   `json:"type"`                  // "send"
	To          string   `json:"to"`                    // recipient phone or UUID
	Content     string   `json:"content"`               // text content
	Attachments []string `json:"attachments,omitempty"` // attachment file paths
}

// SignalIPCStatus represents a status update from the bridge.
type SignalIPCStatus struct {
	Type      string `json:"type"`      // "status"
	Connected bool   `json:"connected"` // bridge connection state
	Message   string `json:"message,omitempty"`
}

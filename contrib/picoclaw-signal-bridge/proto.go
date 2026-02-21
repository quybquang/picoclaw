package main

// IPC protocol types — must match PicoClaw's pkg/channels/signal_proto.go

// SignalIPCInbound represents a message sent from bridge to PicoClaw.
type SignalIPCInbound struct {
	Type     string            `json:"type"`
	From     string            `json:"from,omitempty"`
	ChatID   string            `json:"chat_id,omitempty"`
	Content  string            `json:"content,omitempty"`
	Media    []string          `json:"media,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// SignalIPCOutbound represents a message sent from PicoClaw to bridge.
type SignalIPCOutbound struct {
	Type        string   `json:"type"`
	To          string   `json:"to"`
	Content     string   `json:"content"`
	Attachments []string `json:"attachments,omitempty"`
}

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/mdp/qrterminal/v3"
	"github.com/rs/zerolog"
	"go.mau.fi/mautrix-signal/pkg/libsignalgo"
	"go.mau.fi/mautrix-signal/pkg/signalmeow"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/events"
	signalpb "go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/store"
	"go.mau.fi/util/dbutil"
	"google.golang.org/protobuf/proto"

	_ "github.com/mattn/go-sqlite3"
)

// Bridge manages the Signal client and routes messages to/from PicoClaw via IPC.
type Bridge struct {
	logger    zerolog.Logger
	dataDir   string
	container *store.Container
	device    *store.Device
	client    *signalmeow.Client
	ipc       *IPCServer
	mu        sync.Mutex
}

// NewBridge initializes the bridge with a data directory for session storage.
func NewBridge(logger zerolog.Logger, dataDir string) (*Bridge, error) {
	dbPath := filepath.Join(dataDir, "signal.db")

	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	rawDB, err := dbutil.NewWithDB(db, "sqlite3")
	if err != nil {
		return nil, fmt.Errorf("failed to create dbutil: %w", err)
	}

	container := store.NewStore(rawDB, nil)
	if err := container.Upgrade(context.Background()); err != nil {
		return nil, fmt.Errorf("failed to upgrade database: %w", err)
	}

	bridge := &Bridge{
		logger:    logger,
		dataDir:   dataDir,
		container: container,
	}

	devices, err := container.GetAllDevices(context.Background())
	if err == nil && len(devices) > 0 {
		bridge.device = devices[0]
		logger.Info().Str("number", bridge.device.Number).Msg("Loaded existing device session")
	}

	return bridge, nil
}

// IsLinked returns true if a device session exists.
func (b *Bridge) IsLinked() bool {
	return b.device != nil && b.device.IsDeviceLoggedIn()
}

// LinkDevice performs the QR code device linking flow.
func (b *Bridge) LinkDevice(ctx context.Context) error {
	b.logger.Info().Msg("Starting device provisioning...")
	b.logger.Info().Msg("Open Signal app → Settings → Linked Devices → Link New Device")
	fmt.Println()

	provChan := signalmeow.PerformProvisioning(ctx, b.container, "PicoClaw Bridge", false)

	for resp := range provChan {
		switch resp.State {
		case signalmeow.StateProvisioningURLReceived:
			fmt.Println("📱 Scan this QR code with Signal app:")
			fmt.Println()
			qrterminal.GenerateHalfBlock(resp.ProvisioningURL, qrterminal.L, os.Stdout)
			fmt.Println()
			fmt.Printf("   Or open manually: %s\n", resp.ProvisioningURL)
			fmt.Println()

		case signalmeow.StateProvisioningDataReceived:
			b.logger.Info().Msg("Provisioning data received, finalizing...")

		case signalmeow.StateProvisioningError:
			return fmt.Errorf("provisioning failed: %w", resp.Err)
		}

		if resp.ProvisioningData != nil {
			b.logger.Info().
				Str("aci", resp.ProvisioningData.ACI.String()).
				Msg("✅ Device linked successfully!")
			// Device data is already stored by PerformProvisioning via DeviceStore
			return nil
		}
	}

	return fmt.Errorf("provisioning channel closed unexpectedly")
}

// Start begins the Signal WebSocket connection and message routing.
func (b *Bridge) Start(ctx context.Context, ipc *IPCServer) error {
	b.mu.Lock()
	b.ipc = ipc
	b.mu.Unlock()

	if b.device == nil {
		return fmt.Errorf("no device session, run --link first")
	}

	b.client = signalmeow.NewClient(b.device, b.logger, b.handleSignalEvent)

	// Start listening for IPC messages from PicoClaw
	go b.listenIPC(ctx)

	// Connect to Signal and start receive loops
	statusChan, err := b.client.StartReceiveLoops(ctx)
	if err != nil {
		return fmt.Errorf("failed to start receive loops: %w", err)
	}

	// Monitor connection status
	go func() {
		for status := range statusChan {
			b.logger.Info().
				Str("status", fmt.Sprintf("%v", status)).
				Msg("Signal connection status changed")
		}
	}()

	b.sendStatus(true, "Bridge connected to Signal")
	return nil
}

// Stop gracefully shuts down the bridge.
func (b *Bridge) Stop() {
	b.sendStatus(false, "Bridge shutting down")

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.client != nil {
		_ = b.client.StopReceiveLoops()
	}
	if b.ipc != nil {
		b.ipc.Close()
	}
}

// handleSignalEvent processes events from the Signal client.
func (b *Bridge) handleSignalEvent(evt events.SignalEvent) bool {
	switch e := evt.(type) {
	case *events.ChatEvent:
		b.handleChatEvent(e)
	case *events.Receipt:
		b.logger.Debug().Msg("Received receipt")
	case *events.LoggedOut:
		b.logger.Warn().Err(e.Error).Msg("Logged out from Signal")
		b.sendStatus(false, "Device logged out")
	default:
		b.logger.Debug().Str("type", fmt.Sprintf("%T", evt)).Msg("Unhandled event")
	}
	return true
}

// handleChatEvent converts a Signal chat event to IPC format.
func (b *Bridge) handleChatEvent(evt *events.ChatEvent) {
	senderUUID := evt.Info.Sender
	sender := evt.Info.Sender.String()
	chatID := evt.Info.ChatID
	if chatID == "" {
		chatID = sender
	}

	// 1. BLOCK GROUP CHATS FOR SPAM PROTECTION
	if chatID != sender {
		b.logger.Warn().
			Str("from", sender).
			Str("chat", chatID).
			Msg("🛑 Blocked message from Group (Security Policy)")
		return
	}

	// 2. GET PHONE NUMBER FOR BETTER ALLOWLIST UX
	recipient, err := b.device.RecipientStore.LoadAndUpdateRecipient(context.Background(), senderUUID, uuid.Nil, nil)
	if err == nil && recipient != nil && recipient.E164 != "" {
		// Combine UUID and Phone: UUID|+1234xxxxxx
		sender = sender + "|" + recipient.E164
	}

	// Extract text body from the protobuf event
	body := extractBody(evt.Event)

	b.logger.Info().
		Str("from", sender).
		Str("chat", chatID).
		Str("preview", truncate(body, 50)).
		Msg("Incoming Signal message")

	ipcMsg := SignalIPCInbound{
		Type:    "message",
		From:    sender,
		ChatID:  chatID,
		Content: body,
		Metadata: map[string]string{
			"timestamp": fmt.Sprintf("%d", evt.Info.ServerTimestamp),
		},
	}

	b.mu.Lock()
	ipc := b.ipc
	b.mu.Unlock()

	if ipc != nil {
		if err := ipc.Send(ipcMsg); err != nil {
			b.logger.Error().Err(err).Msg("Failed to send to PicoClaw")
		}
	}
}

// extractBody extracts the text body from a Signal chat event content.
func extractBody(evt signalpb.ChatEventContent) string {
	switch e := evt.(type) {
	case *signalpb.DataMessage:
		return e.GetBody()
	case *signalpb.EditMessage:
		if dm := e.GetDataMessage(); dm != nil {
			return dm.GetBody()
		}
	}
	return ""
}

// listenIPC reads outbound messages from PicoClaw and sends them via Signal.
func (b *Bridge) listenIPC(ctx context.Context) {
	b.mu.Lock()
	ipc := b.ipc
	b.mu.Unlock()

	if ipc == nil {
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
			data, err := ipc.Receive()
			if err != nil {
				// Avoid log spam when PicoClaw isn't running by sleeping 1s
				time.Sleep(1 * time.Second)
				continue
			}

			var outMsg SignalIPCOutbound
			if err := json.Unmarshal(data, &outMsg); err != nil {
				b.logger.Error().Err(err).Msg("Failed to parse IPC message")
				continue
			}

			if outMsg.Type == "send" {
				b.sendSignalMessage(ctx, outMsg)
			}
		}
	}
}

// sendSignalMessage sends a message to a Signal recipient.
func (b *Bridge) sendSignalMessage(ctx context.Context, msg SignalIPCOutbound) {
	recipientUUID, err := uuid.Parse(msg.To)
	if err != nil {
		b.logger.Error().Err(err).Str("to", msg.To).Msg("Invalid recipient UUID")
		return
	}

	recipientServiceID := libsignalgo.NewACIServiceID(recipientUUID)

	body := msg.Content
	timestamp := uint64(time.Now().UnixMilli())
	content := &signalpb.Content{
		DataMessage: &signalpb.DataMessage{
			Body:      proto.String(body),
			Timestamp: proto.Uint64(timestamp),
		},
	}

	result := b.client.SendMessage(ctx, recipientServiceID, content)
	if result.WasSuccessful {
		b.logger.Info().Str("to", msg.To).Msg("Signal message sent")
	} else {
		errMsg := "unknown"
		if result.FailedSendResult.Error != nil {
			errMsg = result.FailedSendResult.Error.Error()
		}
		b.logger.Error().
			Str("to", msg.To).
			Str("error", errMsg).
			Msg("Failed to send Signal message")
	}
}

// sendStatus sends a status update to PicoClaw.
func (b *Bridge) sendStatus(connected bool, message string) {
	b.mu.Lock()
	ipc := b.ipc
	b.mu.Unlock()

	if ipc != nil {
		_ = ipc.Send(SignalIPCInbound{
			Type:    "status",
			Content: message,
			Metadata: map[string]string{
				"connected": fmt.Sprintf("%t", connected),
			},
		})
	}
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

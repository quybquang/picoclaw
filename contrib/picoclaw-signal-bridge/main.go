// picoclaw-signal-bridge — Signal bridge for PicoClaw
//
// Licensed under AGPL-3.0 (links libsignal_ffi.a + imports mautrix-signal)
// PicoClaw itself remains MIT-licensed.
//
// Usage:
//   picoclaw-signal-bridge --link --data-dir ~/.picoclaw/signal
//   picoclaw-signal-bridge --data-dir ~/.picoclaw/signal --socket /tmp/picoclaw-signal.sock

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/rs/zerolog"
)

var (
	socketPath = flag.String("socket", "/tmp/picoclaw-signal.sock", "Unix socket path for PicoClaw IPC")
	dataDir    = flag.String("data-dir", "", "Data directory for Signal session (required)")
	linkMode   = flag.Bool("link", false, "Link as secondary device (one-time setup)")
	logLevel   = flag.String("log-level", "info", "Log level (debug, info, warn, error)")
)

func main() {
	flag.Parse()

	if *dataDir == "" {
		home, _ := os.UserHomeDir()
		*dataDir = filepath.Join(home, ".picoclaw", "signal")
	}
	if err := os.MkdirAll(*dataDir, 0700); err != nil {
		log.Fatalf("Failed to create data dir: %v", err)
	}

	// Setup logger
	level, err := zerolog.ParseLevel(*logLevel)
	if err != nil {
		level = zerolog.InfoLevel
	}
	logger := zerolog.New(zerolog.NewConsoleWriter()).
		Level(level).
		With().Timestamp().
		Str("component", "signal-bridge").
		Logger()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bridge, err := NewBridge(logger, *dataDir)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to initialize bridge")
	}

	if *linkMode {
		logger.Info().Msg("Starting device linking flow...")
		if err := bridge.LinkDevice(ctx); err != nil {
			logger.Fatal().Err(err).Msg("Device linking failed")
		}
		logger.Info().Msg("Device linked successfully!")
		return
	}

	// Check if device is linked
	if !bridge.IsLinked() {
		fmt.Println("⚠️  No linked device found. Run with --link first:")
		fmt.Printf("   %s --link --data-dir %s\n", os.Args[0], *dataDir)
		os.Exit(1)
	}

	// Start IPC server
	ipcServer, err := NewIPCServer(*socketPath)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to start IPC server")
	}
	defer ipcServer.Close()

	// Start bridge with IPC
	logger.Info().
		Str("socket", *socketPath).
		Str("data_dir", *dataDir).
		Msg("Starting Signal bridge")

	if err := bridge.Start(ctx, ipcServer); err != nil {
		logger.Fatal().Err(err).Msg("Failed to start bridge")
	}

	// Wait for shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	logger.Info().Msg("Shutting down...")
	bridge.Stop()
}

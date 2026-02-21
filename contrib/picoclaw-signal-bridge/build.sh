#!/bin/bash
# Build script for picoclaw-signal-bridge
# Requires: Rust toolchain, Go 1.25+, cmake, protobuf
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
LIB_DIR="$SCRIPT_DIR/lib"
mkdir -p "$LIB_DIR"

# Step 1: Build or download libsignal_ffi.a
if [ ! -f "$LIB_DIR/libsignal_ffi.a" ]; then
    echo "📦 Building libsignal_ffi.a from source..."
    TMPDIR=$(mktemp -d)
    git clone --depth 1 https://github.com/signalapp/libsignal.git "$TMPDIR/libsignal"
    cd "$TMPDIR/libsignal"
    cargo build --release -p libsignal-ffi
    cp target/release/libsignal_ffi.a "$LIB_DIR/"
    cp swift/Sources/SignalFfi/signal_ffi.h "$LIB_DIR/"
    cd "$SCRIPT_DIR"
    rm -rf "$TMPDIR"
    echo "✅ libsignal_ffi.a built"
else
    echo "✅ libsignal_ffi.a already exists"
fi

# Step 2: Build Go binary
echo "🔨 Building picoclaw-signal-bridge..."
cd "$SCRIPT_DIR"
LIBRARY_PATH="$LIB_DIR:$LIBRARY_PATH" \
CGO_ENABLED=1 \
CGO_LDFLAGS="-L$LIB_DIR -lsignal_ffi -lc++ -framework Security -framework Foundation" \
go build -o picoclaw-signal-bridge .

echo "✅ Build complete: $SCRIPT_DIR/picoclaw-signal-bridge"
echo ""
echo "Usage:"
echo "  # Link device (one-time)"
echo "  ./picoclaw-signal-bridge --link --data-dir ~/.picoclaw/signal"
echo ""
echo "  # Run bridge"
echo "  ./picoclaw-signal-bridge --data-dir ~/.picoclaw/signal --socket /tmp/picoclaw-signal.sock"

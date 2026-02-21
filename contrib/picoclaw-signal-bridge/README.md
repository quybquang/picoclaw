# picoclaw-signal-bridge

> Native Signal messenger integration bridge for PicoClaw using `libsignal` FFI.

## Quick Start

```bash
# 1. Download dependencies and build
./build.sh

# 2. Link your Signal device (one-time setup)
./picoclaw-signal-bridge --link --data-dir ~/.picoclaw/signal

# 3. Start the bridge daemon
./picoclaw-signal-bridge --data-dir ~/.picoclaw/signal --socket /tmp/picoclaw-signal.sock
```

## Features

- **Direct Signal Integration:** Sends and receives messages securely via the official Signal infrastructure.
- **Unix Socket IPC:** Fast, secure communication with the PicoClaw gateway.
- **E164 Phone Number Allowlist:** Automatically extracts sender phone numbers for easy configuration in PicoClaw (e.g., `UUID|+1234567890`).
- **Group Chat Spam Protection:** Automatically blocks and logs messages from group chats to maintain security and focus.

## Configuration

The bridge is configured via command-line flags.

| Flag | Description | Default |
|----------|-------------|---------|
| `--data-dir` | Path to store the SQLite session database | (current dir) |
| `--socket` | Unix socket path to listen on for PicoClaw IPC | `/tmp/picoclaw-signal.sock` |
| `--link` | Run in interactive QR-code device linking mode | false |

## Architecture

```
Signal Server ←→ picoclaw-signal-bridge (AGPL) ←→ PicoClaw (MIT)
                    Unix socket IPC
```

## Build Prerequisites

- Go 1.25+
- Rust toolchain (if compiling `libsignal_ffi.a` from source)

## License

AGPL-3.0 (due to `libsignal` and `mautrix-signal` dependencies).
PicoClaw's main binary remains MIT-licensed by executing this bridge as an independent process.

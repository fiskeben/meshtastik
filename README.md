# meshtastik

meshtastik is a single-binary Go CLI for administering a local Meshtastic radio node over serial, TCP or BLE. It replaces the Python `meshtastic` CLI for node configuration and inspection.

Requires node firmware 2.5 or newer.

## Install

```
go build ./cmd/meshtastik
```

### Platforms

All three platforms build, including BLE support (tinygo bluetooth is compiled on every target):

| Target | Build |
| --- | --- |
| macOS | `go build ./cmd/meshtastik` (needs cgo, which the BLE stack and serial-port detection use) |
| Linux amd64 | `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./cmd/meshtastik` |
| Linux arm64 | `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./cmd/meshtastik` |
| Windows amd64 | `CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./cmd/meshtastik` |

All four builds were verified to succeed. None has been run against real hardware yet; see [docs/smoke-test.md](docs/smoke-test.md) for the hardware checklist.

## Connecting

Pick one transport with a global flag. With none, meshtastik looks for a serial port.

- `--port <path>` serial port
- `--host <host[:port]>` TCP (default port 4403)
- `--ble <name or address>` Bluetooth LE

Other global flags: `--json`, `--yes` (skip confirmations), `--debug`, `--timeout <duration>` (0 = per-transport default).

## Commands

| Command | What it does |
| --- | --- |
| `info` | Show the connected node's identity and basics |
| `nodes` | List nodes known to the connected node |
| `config get [path]` | Show the whole configuration, a section, or one field (e.g. `lora.region`) |
| `config set <path> <value> [...]` | Set one or more fields in a single verified edit |
| `config export [-o file]` | Write a Python-compatible YAML backup |
| `config import <file> [--keys]` | Apply a YAML backup in a single verified edit |
| `channel list` | List the node's channels |
| `channel add <name> [--psk ...]` | Add a secondary channel in the first free slot |
| `channel set <index> <field> <value>` | Set a channel settings field |
| `channel del <index>` | Delete a secondary channel; later channels shift down |
| `channel url [--no-qr] [--set URL]` | Show the channel URL and QR code, or replace all channels from a URL |
| `owner set <long-name> [--short X]` | Set the owner's long name, and optionally the short name |
| `reboot` | Reboot the node in 5 seconds |
| `shutdown` | Power the node off in 5 seconds |
| `factory-reset [--full]` | Erase the node's configuration (`--full` also erases device state) |
| `send <text> [--to node] [--channel n] [--ack-timeout d]` | Send a text message; `--to` takes a `!hex` id, number or name (default: broadcast) |
| `ble scan [--duration d]` | Scan for nearby Meshtastic devices over BLE |
| `version` | Print the version |

Run `meshtastik <command> --help` for details.

### Notes

- Negative values need `--` so they are not read as flags: `meshtastik config set -- lora.tx_power -5`.
- `config export` writes the node's private key. The file is created with mode 0600; keep it safe.
- `config import` leaves the node's own private and public keys alone unless you pass `--keys`.

## Bluetooth

- On macOS the terminal app is prompted for Bluetooth permission on first BLE use. Allow it, then run the command again if needed.
- On macOS, addresses shown by `ble scan` are CoreBluetooth UUIDs, not MAC addresses. Pass the name or that UUID to `--ble`.

## Licence

GPL-3.0. See [LICENSE](LICENSE).

# Meshtastic admin CLI in Go — design

Date: 2026-11-19
Status: draft for approval

## Intent

A single, self-contained binary for administering the user's own Meshtastic
nodes, replacing the Python `meshtastic` CLI. The user wants zero Python and a
tool they can trust: when it says a setting was changed, it was. They have not
used the Python CLI, so feature parity with it is not a goal; covering what a
node owner needs to configure and look after their nodes is.

## Goals

- One static-ish binary per platform; no runtime dependencies (no Python, no
  shared libraries beyond what the OS provides).
- Connect to a **local** node over **serial (USB)**, **Bluetooth LE**, or
  **WiFi/Ethernet (TCP)**.
- Read device state: device info, node list, full config, channels.
- Change device state: any config / module-config field, channels, owner name,
  reboot / shutdown / factory reset.
- Back up a node's full config to a file and restore or clone it onto a node.
- Send a text message as a quick "is the mesh working" check.
- Reliable: every write is confirmed; failures are loud and specific.
- `--json` output for every read command.

## Non-goals

- Remote admin over the mesh (admin messages routed to other nodes, PKI admin
  keys used for remote control). Local nodes only.
- Feature parity with the Python CLI or its flag names beyond `--port`,
  `--host`, `--ble`.
- Long-running features: tunnel, power monitoring, telemetry/packet logging,
  MQTT bridging, map/GUI.
- A public Go library API. Internal packages are fine; no stability promises.
- Firmware flashing.

## Anti-goals (would count as failure even if it "works")

- Reporting success for a write the device didn't apply.
- Leaving a node half-configured after an interrupted multi-setting change
  without telling the user.
- Bricking or locking the user out of a node (e.g. disabling the only
  transport they're connected over without warning).
- Silently cloning one node's identity (private key) onto another.

## Constraints

- Language: Go (toolchain on the user's machine: Go 1.27, macOS arm64).
- Protocol types come from the official Buf-generated module
  `buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic`
  (pin a version; it tracks firmware releases). No hand-maintained `.proto`
  copies.
- Bluetooth on macOS requires CoreBluetooth via cgo, so macOS binaries are
  built on macOS. Linux and Windows BLE stacks can be reached without cgo.
- Licence: the official protobufs are GPL-3.0; this project should be GPL-3.0
  compatible (it will be GPL-3.0 if it links them).

## Approaches considered

1. **Use lmatte7/meshtastic-go as-is.** Zero effort, MIT, serial + TCP. Lacks
   BLE, config backup/restore, most admin operations; reported slow and behind
   the Python CLI. Rejected: doesn't cover node admin.
2. **Fork lmatte7/meshtastic-go + goMesh.** Working base, but inherits its
   structure, CLI framework and protobuf copy; catching up to current firmware
   is about as much work as starting clean. Rejected.
3. **Fresh CLI on the official Go protobufs with our own transport layer.**
   The transport is small; the work is in the commands, and we choose them.
   **Chosen by the user.** goMesh may be read for ideas, not depended on.

## Protocol facts the builder needs (verified against meshtastic/python)

- **Stream framing (serial and TCP):** each `ToRadio`/`FromRadio` protobuf is
  prefixed with `0x94 0xC3` + 16-bit big-endian length. Max payload 512 bytes.
  On serial, bytes outside frames are the device's debug log — skip them (and
  optionally surface with `--debug`). Before the first frame on serial, write
  32× `0xC3` to wake/resync the device, then wait ~100 ms.
- **TCP:** port 4403, same framing.
- **BLE:** service `6ba1b218-15a8-461f-9fa8-5dcae273eafd`;
  write `ToRadio` to `f75c76d2-129e-4dad-a1dd-7866124401e7`;
  read `FromRadio` from `2c55e69e-4993-11ed-b878-0242ac120002` repeatedly until
  it returns empty; subscribe to `FromNum`
  `ed9da18c-a800-4f66-a670-aa7547e34453` (notify) as the "more data" signal.
  Log characteristic `5a3d6e49-06e6-4423-9944-e9de8cdf9547` is optional.
  No length framing on BLE — one protobuf per characteristic value.
- **Handshake:** send `ToRadio{want_config_id: <random nonce>}`; the device
  streams `my_info`, `metadata`, `node_info`×N, `config`×N, `moduleConfig`×N,
  `channel`×N, …, then `config_complete_id == nonce`. Only then is the
  in-memory view complete.
- **Admin:** a `MeshPacket` addressed to the local node number, port
  `ADMIN_APP`, payload `AdminMessage`, `want_response` set. Multi-field
  changes are wrapped in `begin_edit_settings` … `commit_edit_settings`.
  Echo the `session_passkey` from the latest admin response in subsequent
  admin messages, as the Python client does.
- **Heartbeat:** send `ToRadio{heartbeat}` periodically on long sessions so
  serial links don't idle out.

## Design

### Command surface

Binary name: `meshtastik` (chosen by the user). "Meshtastic" is a trademark; the name is fine for personal use but should be revisited before any public release.

Connection flags (global, mutually exclusive):
`--port <serial device>`, `--host <ip[:port]>`, `--ble <name|address>`.
With none given: if exactly one likely Meshtastic serial device is present,
use it; otherwise list candidates and exit non-zero.

```
meshtastik info                          # owner, node num/id, hw model, firmware, region, role, battery
meshtastik nodes                         # node db: id, name, last heard, SNR, hops, battery
meshtastik config get [path]             # e.g. lora, lora.region; no path = everything
meshtastik config set <path> <value>...  # multiple pairs applied in one edit transaction
meshtastik config export [-o file]       # YAML backup
meshtastik config import <file> [--keys] # restore/clone
meshtastik channel list
meshtastik channel add <name> [--psk random|none|base64:...]
meshtastik channel set <index> <field> <value>
meshtastik channel del <index>
meshtastik channel url [--set <url>]     # print or apply a channel-set URL (+ QR in terminal)
meshtastik owner set <long> [--short X]
meshtastik reboot | shutdown | factory-reset
meshtastik send <text> [--to <node>] [--channel <i>]
meshtastik ble scan                      # list nearby Meshtastic BLE devices
```

`--json` on every read command. `--yes` skips confirmation prompts.

### Config paths

Paths are `section.field` using the protobuf field names (snake_case), e.g.
`lora.region`, `device.role`, `mqtt.enabled` (module config sections live in
the same namespace; the tool knows which message each section belongs to).
Values are parsed by field type via protobuf reflection: enums by name
(case-insensitive), bools, numbers, `base64:` for bytes, comma lists for
repeated fields. Unknown paths/values fail with the list of valid options.

### Reliability rules

- **Every write is confirmed.** After a set/commit, re-read the affected
  config/channel from the device and compare; report any field that didn't
  take. Exit non-zero on mismatch.
- **Edit transactions:** all `config set` pairs and all of `config import` go
  inside one begin/commit. If the connection drops mid-transaction, say so
  explicitly and tell the user the node may need a reboot to discard the
  partial edit.
- **Reboot awareness:** some changes make the device reboot. Detect the
  disconnect, reconnect (same transport, with backoff, bounded wait), redo the
  handshake, then verify.
- **Lock-out guard:** refuse without `--yes` any change that disables the
  transport currently in use (e.g. `bluetooth.enabled false` over BLE,
  `security.serial_enabled false` over serial, disabling WiFi over TCP), and
  confirm destructive commands (`factory-reset`, `shutdown`, `channel del 0`).
- **Timeouts everywhere** with specific messages ("no response to handshake
  after 10s on /dev/cu.usbserial-0001 — is another program holding the port?").
- **Firmware floor:** support firmware 2.5 and newer (my call — the PKI /
  session-passkey era). Older firmware → clear "unsupported firmware" error.

### Backup format

YAML, structurally compatible with the Python CLI's `--export-config`
(top-level `owner`, `owner_short`, `channel_url`, `location`, `config`,
`module_config`; `base64:`-prefixed byte fields) so community configs and
Python backups can be imported. Export writes the full config **including**
security keys (it's a real backup).

**Import does not apply the node's own private/public key unless `--keys` is
given** (my call): restoring onto the same node uses `--keys`; cloning settings
across several nodes is safe by default and won't duplicate an identity.
`admin_key` entries are applied normally.

### Structure (suggested, builder may adjust)

- `cmd/meshtastik` — CLI wiring.
- `internal/transport` — `serial`, `tcp`, `ble`; each yields a stream of
  `FromRadio` and accepts `ToRadio`. Framing lives in one shared codec used by
  serial and TCP.
- `internal/device` — handshake, in-memory device state, admin request/response
  matching, edit transactions, verify, reconnect.
- `internal/configpath` — path parsing, reflection-based get/set, YAML
  import/export.

### Testing

- Unit: framing codec (including garbage/log bytes between frames, truncated
  frames, oversize lengths) with fuzz tests; config path parsing; YAML
  round-trip against a real Python export fixture.
- Integration: an in-process fake device speaking the stream protocol, covering
  handshake, admin set + verify, mismatch detection, mid-transaction drop,
  reboot/reconnect.
- Hardware: a short manual smoke checklist per transport, run against the
  user's own node(s) before calling a milestone done.

### Platforms and delivery

- macOS arm64 (primary, user's machine), Linux amd64/arm64 (Raspberry Pi is
  common), Windows amd64.
- macOS binaries built on macOS (cgo for BLE); Linux/Windows can be
  cross-compiled. Release automation is a later concern; `go build` first.

### Suggested order

1. Serial + TCP transport, handshake, `info`, `nodes`, `config get`.
2. `config set` with verify, owner, reboot/shutdown/factory-reset, lock-out guard.
3. Channels and channel URLs.
4. Export / import.
5. BLE transport + `ble scan`.
6. `send`.

## Left to the builder

- CLI framework, YAML library, serial and BLE libraries (must meet the
  no-runtime-deps and platform constraints above).
- Exact table layouts for human output.
- Reconnect backoff values and default timeouts.
- Whether to use firmware's config-only / nodes-only handshake shortcuts.
- QR rendering library for `channel url`.

# meshtastik

meshtastik is a single-binary Go CLI for administering a local Meshtastic radio node over serial, TCP or BLE. It replaces the Python `meshtastic` CLI for node configuration and inspection.

Requires node firmware 2.5 or newer.

## Install

With Homebrew (macOS and Linux):

```
brew install --cask fiskeben/tap/meshtastik
```

Or download an archive for your platform from [Releases](https://github.com/fiskeben/meshtastik/releases).

### From source

```
make build      # ./meshtastik for this machine, version stamped from git
```

Other targets: `make test`, `make race`, `make vet`, `make check` (vet + race tests), `make build-all` (binaries for every platform in `dist/`; the macOS one only when run on a Mac), `make release-check` and `make snapshot` (see [docs/releasing.md](docs/releasing.md)), `make clean`. Plain `go build ./cmd/meshtastik` also works.

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

Other global flags: `--json`, `--yes` (skip confirmations), `--debug`, `--timeout <duration>` (bounds the handshake and each request, not the whole operation; 0 = per-transport default), `--no-daemon` (see below), `--dest <node>` (see [Remote admin](#remote-admin)).

## The daemon

Commands connect to the node through a background daemon. The first command starts it, and it keeps the node connection open between commands, so later commands start at once instead of waiting for the node's config dump. Several commands can use a node at the same time (`listen -f` in one terminal, `send` in another), and messages that arrive between `listen` runs are kept for the next one (up to 256).

- One daemon per user holds a connection to each node you've used (one per `--port`/`--host`/`--ble` value). A connection with no commands using it is closed after 30 minutes, and the daemon exits once it has none left.
- It listens on a socket only you can open: `$MESHTASTIK_SOCKET`, else `$XDG_RUNTIME_DIR/meshtastik.sock`, else `meshtastik-<uid>.sock` in the temporary directory. Its log sits next to the socket (`.log`); `daemon status` shows both paths.
- `--no-daemon`, or `MESHTASTIK_NO_DAEMON=1`, connects directly as before. A serial port the daemon holds can't be opened directly at the same time; run `meshtastik daemon stop` first.
- After upgrading meshtastik, commands note that the daemon runs the old version until you run `meshtastik daemon restart`.

## Remote admin

With `--dest <node>`, admin commands act on another node over the mesh instead of the one you are connected to: a solar repeater on a roof, a node at a cabin. `<node>` is a `!hex` id, a number or a name from the connected node's node list.

Setup, once per remote node:

1. On the node you connect to, `meshtastik info` shows its public key (`base64:…`).
2. Connect to the remote node locally (USB, WiFi or Bluetooth) and allow that key: `meshtastik admin-key add <the key, or the name of your node>`.
3. From then on, through your own node:

```
$ meshtastik --dest HIL1 config set lora.hop_limit 5
reaching !00000099 (Hill) over the mesh — this can take a while
Applied 1 change(s); verified.
```

- Supported by `info`, `config get/set/export/import`, `owner set`, `channel list/add/set/del/url`, `reboot`, `shutdown`, `factory-reset`, `nodes remove/favorite/unfavorite/ignore/unignore/reset` and `admin-key …`. Other commands refuse `--dest`.
- The remote node must run firmware 2.5 or newer and list your node's public key in its `security.admin_key`; otherwise the error says so and how to fix it. Your node must have heard the remote node's node info (it needs its public key to encrypt).
- Every answer is waited for 30 seconds (`--timeout` changes that) and each message is tried up to 3 times. Commands read only what they need (`config get lora.region` reads the LoRa section); a full `config get` or `config export` reads every section and channel one by one, which takes a while on LoRa.
- Writes are verified by reading them back, waiting for the node if it reboots. Two things cannot be read back from a remote node, and the output says so: the coordinates of a fixed position, and its node list (`nodes …` reports `sent`).
- A private key never goes over the air: a remote `config export` has none, and `config import --keys --dest` is refused. Writing a remote node's security section (`admin-key`, `config set security.…`) needs firmware 2.8 or newer on it; older firmware would replace or clear its keys.
- Changes that could cut the remote node off ask first: its LoRa link settings (region, preset, frequency, channel number, `tx_enabled`), its primary channel, and an admin key list without your node's key. `shutdown` and `factory-reset` say the node cannot be reached remotely afterwards.

## Commands

| Command | What it does |
| --- | --- |
| `info` | Show the connected node's identity and basics, including its public key (what other nodes need in their admin keys) |
| `nodes` | List nodes known to the connected node (FLAGS shows `fav` and `ignored`) |
| `nodes remove/favorite/unfavorite/ignore/unignore <node>...` | Change entries in the node list; each change is verified by re-reading it (`remove` of several nodes asks first) |
| `nodes reset [--keep-favorites]` | Clear the node list; the node reboots (nodes in a router or base role always keep favourites) |
| `config get [path]` | Show the whole configuration, a section, or one field (e.g. `lora.region`) |
| `config set <path> <value> [...]` | Set one or more fields in a single verified edit |
| `config export [-o file]` | Write a Python-compatible YAML backup |
| `config import <file> [--keys]` | Apply a YAML backup in a single verified edit |
| `channel list` | List the node's channels |
| `channel add <name> [--psk ...]` | Add a secondary channel in the first free slot |
| `channel set <index> <field> <value>` | Set a channel settings field |
| `channel del <index>` | Delete a secondary channel; later channels shift down |
| `channel url [--no-qr] [--set URL]` | Show the channel URL and QR code, or replace all channels from a URL (add-style `?add=true` URLs are refused; use `channel add`) |
| `owner set <long-name> [--short X]` | Set the owner's long name, and optionally the short name |
| `reboot` | Ask the node to reboot (it restarts in ~5 seconds) |
| `shutdown` | Ask the node to power off (it shuts down in ~5 seconds) |
| `factory-reset [--full]` | Erase the node's configuration (`--full` also erases device state) |
| `send <text> [--to node] [--channel n] [--ack-timeout d]` | Send a text message; `--to` takes a `!hex` id, number or name (default: broadcast) |
| `listen [-f] [--channel n] [--json [--raw]] [--all] [--count n]` | Print the next received message and exit; `-f`/`--follow` keeps printing (the node's queued backlog, then new ones) until Ctrl-C; `--all` adds other packet types, `--count` exits after n messages, `--raw` adds the whole FromRadio message to each JSON line under `raw` |
| `admin-key list` | List the public keys allowed to administer the node remotely, with the node each belongs to |
| `admin-key add <key\|node>` | Allow a key (`base64:…`, or a node whose public key the node list knows); at most 3 |
| `admin-key remove <key\|node\|index>` | Remove an admin key |
| `traceroute <node> [--hop-limit n]` | Trace the route to a node and back with the SNR of each hop (default hop limit: the node's `lora.hop_limit`) |
| `request position <node>` | Ask a node for its position |
| `request telemetry <node> [--type device\|environment\|power\|local-stats]` | Ask a node for its telemetry |
| `ble scan [--duration d]` | Scan for nearby Meshtastic devices over BLE (directly, never through the daemon) |
| `daemon status` | Show whether the daemon runs, its socket and log, and the node connections it holds |
| `daemon start [--idle-timeout d]` | Start the daemon in the background unless it is running (it also starts on demand) |
| `daemon stop` | Stop the daemon, closing its node connections |
| `daemon restart [--idle-timeout d]` | Stop the daemon, then start it (e.g. after an upgrade) |
| `daemon run [--idle-timeout d]` | Run the daemon in the foreground, logging to the terminal |
| `version` | Print the version |

Run `meshtastik <command> --help` for details.

### Notes

- Negative values need `--` so they are not read as flags: `meshtastik config set -- lora.tx_power -5`.
- `config get` shows zero values (e.g. `enabled: false`) and prints `security.private_key` as `<redacted; use config export>`. Ask for the field itself (`config get security.private_key`) to see it.
- `config export` writes the node's private key. The file is created with mode 0600; keep it safe.
- `config import` leaves the node's own private and public keys alone unless you pass `--keys`. Even with `--keys`, neither key is applied if either is empty or not 32 bytes in the file, because the node would then generate a new identity.
- `config import` restores `location` only when the backup's merged config has `position.fixed_position: true`; Python backups of GPS nodes carry a location that is otherwise ignored (with a note).
- A node with `security.is_managed` set ignores local configuration, so meshtastik refuses changes to it, except turning `security.is_managed` off.
- Changes that could cut the link in use (Bluetooth off or WiFi on over BLE, WiFi/network changes over TCP, the serial console off or taken over by the serial module over serial) ask for confirmation first.
- `listen` has no history beyond what the daemon has kept: it shows the messages that arrived while no `listen` was running (or, with `--no-daemon`, the few the node queued while no client was connected), then live traffic. The node hands each message to one connected client only, so if Home Assistant or the phone app is connected at the same time they split the traffic. If the connection keeps dropping soon after each reconnect (another client taking the node's one TCP slot), `listen` gives up with an error.
- `traceroute` and `request` make one attempt and wait 60 seconds for the answer (twice `--timeout` when it is set). The firmware limits them: one traceroute per 30 seconds and one position or telemetry request per 10 seconds from a connection (meshtastik reports the node's refusal at once), and a node answers position requests at most every 3 minutes. A node without the asked-for sensor, or without a position to share, says it received the request but did not answer it.
- Ctrl-C during a change: before the commit, meshtastik reports that the node may hold a partial edit (firmware 2.8 and newer saves the partial edit after 60 seconds; older firmware discards it on reboot); after the commit, it reports that the changes were committed but could not be verified. A second Ctrl-C exits at once.

## Bluetooth

- On macOS the terminal app is prompted for Bluetooth permission on first BLE use. Allow it, then run the command again if needed.
- On macOS, addresses shown by `ble scan` are CoreBluetooth UUIDs, not MAC addresses. Pass the name or that UUID to `--ble`.

## Licence

GPL-3.0. See [LICENSE](LICENSE).

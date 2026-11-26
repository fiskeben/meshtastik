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
- `--device <name>` a named device (below)

### Named and current devices

To avoid passing a flag on every command, name the nodes you use and pick one as the current device:

```
$ meshtastik device add home --host 192.168.1.40
$ meshtastik device add hill --ble Hill_1234
$ meshtastik use home
Commands now connect to home (tcp:192.168.1.40).
$ meshtastik info                  # connects to home
$ meshtastik --device hill info    # just this once
```

- `use --host 192.168.1.40` (or `--port`, `--ble`) saves a current device without naming it. `use` alone shows which node commands connect to and why; `use --clear` forgets it.
- Commands pick the node from, in order: `--port`/`--host`/`--ble`/`--device`; then `MESHTASTIK_DEVICE`, `MESHTASTIK_PORT`, `MESHTASTIK_HOST` or `MESHTASTIK_BLE` (one of them, handy per terminal or in scripts); then the current device; then a single serial port.
- When the node came from the environment or the current device, commands that change it (or `send`) say which one on stderr first: `using home (tcp:192.168.1.40) — the current device`. Confirmation prompts start with the node they act on.
- If the current device can't be reached, the command fails and says so; it never falls back to another node.
- Devices are kept in `devices.yaml` in your config directory (`~/Library/Application Support/meshtastik` on macOS, `~/.config/meshtastik` on Linux), or at `$MESHTASTIK_DEVICES`.

Other global flags: `--json`, `--yes` (skip confirmations), `--debug`, `--timeout <duration>` (bounds the handshake and each request, not the whole operation; 0 = per-transport default), `--no-daemon` (see below), `--dest <node>` (see [Remote admin](#remote-admin)).

## The daemon

Commands connect to the node through a background daemon. The first command starts it, and it keeps the node connection open between commands, so later commands start at once instead of waiting for the node's config dump. Several commands can use a node at the same time (`listen -f` in one terminal, `send` in another), and messages that arrive between `listen` runs are kept for the next one (up to 256). `listen` also shows text that other commands send through the daemon, as sent by your node.

- One daemon per user holds a connection to each node you've used (one per `--port`/`--host`/`--ble` value). A connection with no commands using it is closed after 30 minutes, and the daemon exits once it has none left.
- It listens on a socket only you can open: `$MESHTASTIK_SOCKET`, else `$XDG_RUNTIME_DIR/meshtastik.sock`, else `meshtastik-<uid>.sock` in the temporary directory. Its log sits next to the socket (`.log`); `daemon status` shows both paths.
- `--no-daemon`, or `MESHTASTIK_NO_DAEMON=1`, connects directly as before. A serial port the daemon holds can't be opened directly at the same time; run `meshtastik daemon stop` first.
- After upgrading meshtastik, commands note that the daemon runs the old version until you run `meshtastik daemon restart`.

## IRC (ircmesh)

`ircmesh` is a second program in the same install. It serves your node as an IRC server, so you can chat on the mesh from any IRC client (irssi, WeeChat, HexChat, …). Like a bouncer, it stays connected while no client is, and you can attach several clients at once.

```
ircmesh --device home
```

Then connect your IRC client to `127.0.0.1` port `6667` (`/connect 127.0.0.1 6667` in irssi). It picks the node the same way meshtastik does (`--device`, `--host`, `--port`, `--ble`, the `MESHTASTIK_*` variables, or the current device), and goes through the daemon, starting one itself if none is running.

- **You are the node.** Your nick is the node's short name, or its long name when the short name is an emoji, and can't be changed from IRC.
- **Channels are the node's channels.** The primary channel is named after its preset when it has no name (`#LongFast`). You join every channel on connect. `/part` hides a channel for that client only.
- **Other nodes are users.** A node shows up in a channel once it has been heard there, and leaves after 24 hours without being heard there (`--active`). `/whois <nick>` shows when it was last heard, and you can `/msg` any node the node knows, even an offline one.
- **Messages:** `/msg <nick>` sends a direct message and tells you if it isn't acknowledged. Long messages are split into up to three packets; anything longer is refused. `/me` is sent as `* <nick> …`. Reactions and replies from the apps show up quoted.
- **While you're away**, up to 100 messages per channel and per person are kept, and the next client to connect gets them (with timestamps if it supports `server-time`).
- **Off this machine:** with `--listen 0.0.0.0:6667` or any address that isn't loopback, set a password with `MESHTASTIK_IRC_PASSWORD` or `--password-file`, and consider `--tls-cert`/`--tls-key`.

Known gaps: messages you send from the phone app over Bluetooth don't show up in IRC (the node doesn't pass them on), but ones sent with `meshtastik send` do. While ircmesh runs the daemon, `meshtastik daemon stop` stops it only until ircmesh starts it again.

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
- Writes are verified by reading them back, waiting for the node if it reboots. If a message needed retries, the edit is reopened and sent again, so a slow mesh cannot split it (firmware 2.8 closes an edit idle for 60 seconds). A commit that was sent but never acknowledged is reported as such: check with `config get --dest`. Two things cannot be read back from a remote node, and the output says so: the coordinates of a fixed position, and its node list (`nodes …` reports `sent`).
- A private key never goes over the air: a remote `config export` has none (nor a `location`, which cannot be read either), and `config import --keys --dest` and `config set --dest security.private_key|public_key` are refused. Writing a remote node's security section (`admin-key`, `config set security.…`) needs firmware 2.8 or newer on it; older firmware would replace or clear its keys.
- Changes that could cut the remote node off ask first: its LoRa link settings (region, preset, frequency, channel number, `tx_enabled`), its primary channel, and an admin key list without your node's key. `shutdown` and `factory-reset` say the node cannot be reached remotely afterwards, and `nodes reset --dest` without `--keep-favorites` warns that the node forgets your node's key. Ignoring or removing your own node on a remote node is refused.

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
| `device add <name> --port/--host/--ble X` | Name a node connection, for `--device` and `use` (an existing name is replaced) |
| `device list` | List the named devices; `*` marks the current one |
| `device remove <name>` | Forget a named device |
| `use [name \| --port/--host/--ble X \| --clear]` | Choose the node commands connect to when none is given; without arguments, show it |
| `daemon status` | Show whether the daemon runs, its socket and log, and the node connections it holds (with their device names) |
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

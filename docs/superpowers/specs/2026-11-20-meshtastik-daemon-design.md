# meshtastik daemon — design

Date: 2026-11-20
Status: approved to build (the user asked to go ahead without further questions)

## Intent

Today every command connects to the node, waits for its full config dump
(up to 15 s, 30 s over BLE), does its work and disconnects. A background
daemon holds the node connection instead, and commands become its clients,
the way `gopls -remote=auto` forwards to a shared `gopls serve`. If no daemon
is running, the first command starts one. A `daemon` command manages it.

What this buys:

- Commands start in milliseconds instead of waiting for a handshake.
- Several commands can use the node at once (`listen -f` in one terminal,
  `send` in another), which a node's single serial/TCP connection otherwise
  rules out.
- Messages that arrive between commands are kept for the next `listen`.

## Goals

- Commands talk to the daemon by default and start it when it isn't running.
- `--no-daemon` (or `MESHTASTIK_NO_DAEMON=1`) connects directly, as today.
- One daemon per user holds any number of node connections, one per target
  (`--host`, `--port`, `--ble` value).
- `meshtastik daemon start|stop|restart|status|run`.
- Every existing guarantee holds through the daemon: writes are verified,
  reboots are waited out, lock-out guards see the real transport kind.

## Non-goals

- Remote access: the socket is local to the user's machine.
- A message history beyond what arrives while the daemon runs.
- Supervising the daemon (launchd/systemd units).

## Approaches

1. **Proxy the node's message stream (chosen).** The socket carries the same
   framed `ToRadio`/`FromRadio` messages as serial and TCP. A command's
   `device.Client` works unchanged over it, so Apply, verification, reboot
   handling and listen keep their logic. The daemon answers each client's
   want_config from its own cached state (instant), forwards packets both
   ways, and fans received packets out to every client.
2. **An RPC API per client operation.** Every `device.Client` method
   (Apply, AdminRequest, SendText, Listen, …) becomes a request to the
   daemon. Duplicates the device logic behind a second interface and
   changes every command. Rejected.
3. **Forward raw bytes to one client at a time.** Trivial, but no
   concurrency and no handshake savings. Rejected.

## How it works

### Socket and lifetime

- Unix socket at `$MESHTASTIK_SOCKET`, else `$XDG_RUNTIME_DIR/meshtastik.sock`,
  else `<os.TempDir>/meshtastik-<uid>.sock` (macOS's temp dir is already
  per user). Mode 0600. The daemon logs to the same path with `.log`.
- Autostart: if dialling the socket fails, the CLI runs its own executable
  as `meshtastik daemon run` in a new session, detached, output to the log,
  and retries for up to 5 s. If two commands race to start it, the loser
  finds the socket taken by a live daemon and exits.
- A node connection the daemon holds is closed after the idle timeout with
  no clients (default 30 min, `--idle-timeout`, 0 = never). The daemon exits
  when it has held no connection and served no client for that long.

### Wire protocol

1. The client sends one JSON line, the hello:
   `{"proto":1,"version":"…","op":"connect","target":{"kind":"tcp","addr":"…"},"timeout":"15s","backlog":true}`.
   Other ops: `status`, `stop`.
2. The daemon answers one JSON line: `{"ok":true,"version":"…","kind":"tcp"}`
   or `{"error":"…"}`. For `connect` the answer waits until the node
   connection is up, so connection errors (and their hints) reach the CLI.
3. After an ok `connect`, both sides switch to the stream framing
   (`0x94 0xC3` + length): `ToRadio` from the client, `FromRadio` from the
   daemon.

A different `proto` is refused with a hint to run `meshtastik daemon
restart`; a different `version` only prints that hint on stderr.

### The daemon's node connection ("hub")

- One `device.Client` per target, opened on the first `connect` for it with
  the client's handshake timeout.
- `Listen` runs for the hub's lifetime; each received packet goes to every
  attached client, and to the backlog (below).
- Client `want_config_id`: answered from the hub's `State` in firmware order
  (my_info, metadata, node infos, config sections, module sections,
  channels, config_complete_id with the client's nonce). The hub first
  re-reads the node's config (a want_config of its own) when the cache is
  stale: after any admin packet from a client, after the node reports a
  reboot, or 10 minutes after the last read.
- Client heartbeats are dropped (the hub sends its own); other `ToRadio`
  messages go to the node.
- Non-packet `FromRadio` messages after the handshake (`rebooted`, queue
  status, client notifications) are forwarded to clients.
- When the node connection drops, the hub closes every client connection,
  so each client's own reconnect logic (Apply's reboot wait, listen's
  reconnect) runs as it does over a direct link. Hellos that arrive while
  the hub is reconnecting wait until it is back. If the hub gives up
  (reconnect timeout, another client fighting for the connection), waiting
  hellos get the error and the hub is removed.

### Backlog

A node hands the messages it queued while nobody was connected to the next
client that connects, whatever it is. The hub keeps up to 256 packets that
arrived while no `listen` client was attached, and hands them to the next
client whose hello asks for the backlog (`listen` does) right after its
config_complete_id. Other commands don't consume it.

### CLI

- `--no-daemon` global flag; `MESHTASTIK_NO_DAEMON=1` does the same.
- Serial auto-detection still runs in the CLI, so the daemon always gets an
  explicit target.
- `daemon run [--idle-timeout]`: run in the foreground (what autostart runs).
- `daemon start [--idle-timeout]`: start in the background unless running.
- `daemon stop`: ask it to exit and wait.
- `daemon restart`: stop, then start.
- `daemon status [--json]`: pid, version, uptime, socket, log file, and per
  node connection the target, transport, state, node name and client count.
- `ble scan` keeps talking to Bluetooth directly. A scan won't find a node
  the daemon is connected to; that's expected.

## Calls made by the builder

- Default idle timeout 30 min, cache max age 10 min, backlog 256 packets.
- Version mismatch warns instead of restarting a daemon other terminals may
  be using.
- No flock: two simultaneous autostarts are settled by whichever binds the
  socket first.

# ircmesh — design

Date: 2026-11-26
Status: agreed in a grilling session; ready to plan

## Intent

`ircmesh` turns a Meshtastic node into an IRC server, the way a bouncer
turns an IRC network into one. Any ordinary IRC client (irssi, WeeChat,
HexChat, …) connects to it and sees the node's channels as IRC channels,
other mesh nodes as IRC users and direct messages as private messages.
Meshtastic's own notions of channels and nodes are reused, not reinvented.

It is a separate tool from `meshtastik`, but shares its daemon: one
background process per user holds the node connection, and the CLI and
`ircmesh` are both its clients.

## Goals

- A regular IRC client with default settings connects and works.
- Bouncer behaviour: `ircmesh` stays connected to the mesh whether or not an
  IRC client is attached, several IRC clients can be attached at once, and
  messages that arrive while none is attached are replayed to the next one.
- Mesh channels map to IRC channels, and node channel membership follows
  what the node has actually heard (as the Android app shows it).
- Uses the meshtastik daemon without needing the `meshtastik` binary.

## Non-goals

- Multi-user: everyone on the IRC side speaks as the one local node.
- Sending positions, telemetry, traceroutes, admin, reactions or replies.
- Persistent message history (replay buffers live in memory).
- A config file, daemonising or supervision (tmux, launchd, systemd are the
  user's business).
- IRC services, channel creation, operator features.

## Approaches

1. **Second binary in this module (chosen).** `cmd/ircmesh` imports the
   `internal` packages directly and ships in the same release and Homebrew
   formula. It can move out later if it grows its own life.
2. **Separate repository.** Requires promoting `daemon`, `device` and
   `transport` to public packages with a versioned API. Rejected for now.

For reaching the daemon:

1. **Connect, else host it in-process (chosen).** `daemon.Server` is a
   library type; if no daemon answers on the socket, `ircmesh` serves one on
   the same socket, and CLI commands use it as they would any daemon.
2. **Exec `meshtastik daemon run`.** Needs the CLI installed. Rejected.
3. **Require a running daemon.** Needless friction. Rejected.

## How it works

### Process and node selection

- `ircmesh` runs in the foreground, logs to stderr and exits on SIGINT or
  SIGTERM.
- One node per process. It is chosen as the CLI chooses it: the current
  named device from `devices.yaml`, or `--device <name>`, `--host`,
  `--port` or `--ble`. Several nodes means several `ircmesh` processes on
  different listen addresses.
- Target resolution (named devices, serial auto-detection) and the
  connect-or-start-daemon logic currently live in `internal/cli`; they move
  to a shared internal package so `ircmesh` doesn't import the CLI.
- If no daemon answers on `daemon.SocketPath()`, `ircmesh` listens on it
  and runs a `daemon.Server` in-process, then connects to it as a client.
  A daemon it hosts lives as long as `ircmesh` does.
- `ircmesh` holds its daemon connection for its whole lifetime (which keeps
  the daemon's node connection from idling out) and asks for the daemon's
  backlog in its hello.

### Flags and environment

- `--listen addr` (default `127.0.0.1:6667`).
- `--tls-cert`, `--tls-key`: serve TLS instead of plaintext.
- `--password-file path`, or `MESHTASTIK_IRC_PASSWORD`: the `PASS` clients
  must send. Never a plain flag, so it stays out of `ps` and shell history.
  On a non-loopback listen address `ircmesh` refuses to start without one.
- `--active dur` (default `24h`): how long a node stays in a channel without
  being heard there.
- Device selection: `--device`, `--host`, `--port`, `--ble`.

### IRC protocol baseline

RFC 1459/2812 as real clients use it:

- Registration with `PASS`/`NICK`/`USER`, then 001–004, 005 (`ISUPPORT`:
  `CHANTYPES=#`, `NICKLEN=16`, `CHANNELLEN`, `CASEMAPPING=ascii`,
  `NETWORK=mesh`) and the MOTD.
- `PING`/`PONG` in both directions; `QUIT`.
- `JOIN`, `PART`, `NAMES`, `WHO`, `WHOIS`, `LIST`, `TOPIC` (read only),
  `MODE` (enough to answer client probes), `USERHOST`, `PRIVMSG`, `NOTICE`.
- IRCv3 `CAP LS 302` offering only `server-time`. Clients that never send
  `CAP` work unchanged.
- Anything else: `421 ERR_UNKNOWNCOMMAND`.

### Identity

- The IRC user *is* the local node. Whatever `NICK` a client registers with,
  001 is addressed to the node's nick and followed by a forced `NICK`
  change. `/nick` afterwards is refused with an explanatory numeric; IRC
  never renames the node.
- Several clients may be attached at once, each seeing the same channels
  and traffic.

### Nicks

A node's nick is the first non-empty of:

1. its short name with characters IRC nicks don't allow removed;
2. its long name, spaces turned into `_`, disallowed characters removed,
   cut to 16 characters;
3. `n` + the last 4 hex digits of its node number.

On a collision the later node gets `-<last 4 hex>` appended. Emoji are
never used in nicks (UTF-8 nicks break some clients). The hostmask is
`<nick>!<node id hex>@mesh` and the realname is the long name, so `/whois`
shows the full identity. A node whose names change gets a `NICK` change.

### Channels

- Each enabled channel slot on the node is `#<sanitised channel name>`. A
  primary channel with an empty name is `#<modem preset name>` (e.g.
  `#LongFast`), as the official apps display it. Names that sanitise alike
  become `#<name>-<index>`. Disabled slots don't exist.
- `TOPIC` shows channel info: index, role, preset, whether the key is the
  default or custom.
- After registration every client is JOINed to every channel. `/join` of a
  channel that doesn't exist on the node gets `403 ERR_NOSUCHCHANNEL`.
  `/part` only stops delivery of that channel to that client connection;
  `/join` resumes it.
- When the node's channels change while running (seen when the daemon's
  cached state refreshes), attached clients get `PART :channel removed on
  node` for removed or renamed channels and are JOINed to new or renamed
  ones. Replay buffers and memberships are keyed by channel index, so a
  rename keeps both.

### Membership

A node may listen on several channels, but the firmware only records the
one it was last heard on (`NodeInfo.channel`). Membership is therefore
"channels we have heard this node on":

- `ircmesh` keeps a last-heard time per (node, channel index). Any packet
  from a node on a channel — not just text — makes it a member there
  (`JOIN`).
- Each membership ages out on its own after `--active` (24 h) without
  hearing the node on that channel: `PART :not heard on this channel for
  24h`. When a node's last membership ends it gets `QUIT :timed out`.
- At startup each node is seeded with one membership: its
  `NodeInfo.channel` (missing or 0: primary) at its `last_heard`.
- The pairs are saved to `<os.UserConfigDir>/ircmesh/members-<node id>.json`
  so a restart doesn't drop everyone back to one channel. This is the only
  state `ircmesh` persists.
- A node that sends text on a channel it isn't a member of is JOINed there
  before the message is delivered; clients never see a message from a
  non-member.
- `PRIVMSG <nick>` and `WHOIS <nick>` resolve against the whole node DB, so
  nodes that are in no channel (offline) can still be messaged and looked
  up; `WHOIS` shows their last-heard time.
- Member lists are kept while the mesh link is down; nobody is QUIT because
  `ircmesh` went deaf.

### Messages from IRC to the mesh

- `PRIVMSG #chan` sends channel text on that index. `PRIVMSG <nick>` sends a
  direct message, PKI-encrypted when the firmware supports it. An unknown
  nick gets `401 ERR_NOSUCHNICK`.
- Text is cleaned first: IRC formatting codes (`\x02`, `\x03` colours,
  `\x1d`, `\x1f`, `\x16`, `\x0f`, …) are stripped. `/me text` (CTCP ACTION)
  is sent as `* <nick> text`. Other CTCP (VERSION, PING, TIME, …) is never
  transmitted: `ircmesh` answers VERSION itself and drops the rest.
- Text over one packet's limit (about 200 bytes) is split on word
  boundaries, never inside a UTF-8 character, into at most 3 packets, paced
  to respect airtime. Anything that would need more is refused with a
  `NOTICE` and nothing is sent.
- Channel sends are fire-and-forget; only a routing error produces a
  `NOTICE` in that channel. DMs wait for the ACK; a NAK or timeout produces
  a `NOTICE` naming the recipient and the start of the text. Success is
  silent.
- A message sent from one attached IRC client is echoed to the others as
  coming from the node's nick.
- While the mesh link is down, sends are refused with a `NOTICE`, not
  queued.

### Messages from the mesh to IRC

- Channel text becomes `PRIVMSG #chan` from the sender's nick; a DM becomes
  a private `PRIVMSG` from the sender's nick.
- Incoming text matching `* <name> …` is delivered literally, not turned
  back into an ACTION.
- A reaction (text packet with the `emoji` flag and a `reply_id`) becomes an
  ACTION from the sender: `reacted 👍 to "<first 30 chars of original>…"`,
  or `reacted 👍 to a message` if the original isn't in the replay buffers
  or a small index of recent messages.
- A reply (`reply_id` without the emoji flag) is normal text prefixed with
  `↪ "<first 30 chars of original>…" `.
- Text sent by other daemon clients (e.g. `meshtastik send`) appears as
  coming from the node's nick. This needs a daemon change (below).
- Text sent by the Android app over BLE can't appear: the firmware doesn't
  echo it to other connections. This is a documented gap.

### Replay

ZNC's default semantics:

- Messages are buffered only while no IRC client is attached, up to 100 per
  channel and per DM peer.
- The next client to register gets the buffer replayed after its
  auto-JOINs, with `server-time` tags if it negotiated them, and the buffer
  is cleared.
- While any client is attached nothing is buffered.
- Buffers are in memory only and lost on restart.

### Mesh link loss

When the daemon connection drops (node reboot, network gone, daemon
restart):

- IRC clients stay connected and get a `NOTICE`: mesh link down,
  reconnecting. Another when it's back.
- `ircmesh` reconnects with backoff. If the daemon itself is gone, it hosts
  one in-process as at startup.

### Daemon change: echo outgoing text

The hub fans a client's outgoing text packets (`ToRadio` packets on the
text message port) out to the hub's other clients as `FromRadio` packets,
so `ircmesh` sees what the CLI sends. Clients must not receive their own
packets back.

## Testing

- A raw-protocol test drives `ircmesh` over a socket against
  `internal/fakedevice`: registration with and without `CAP`, forced nick,
  auto-JOIN, `NAMES`/`WHO`/`WHOIS`, channel and DM traffic both ways,
  splitting, refusal of over-long text, CTCP handling, replay and clearing,
  membership aging, link-loss notices.
- Unit tests for nick and channel-name sanitising and collisions, the
  formatting stripper and the splitter.
- Acceptance: irssi, WeeChat and HexChat connect with default settings
  against the user's node (the user tests on hardware).

## Calls made in the grilling session

- Binary name `ircmesh` (not `meshtastik-irc`, which would fight shell
  completion with `meshtastik`).
- Default `--active` 24 h.
- Replay cap 100 messages per channel or DM peer; split cap 3 packets;
  reaction/reply quotes 30 characters.
- Membership state file is per node, since one process serves one node.

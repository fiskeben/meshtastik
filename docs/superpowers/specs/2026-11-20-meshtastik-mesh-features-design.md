# meshtastik mesh features — design

Date: 2026-11-20
Status: draft for approval

## Intent

Until now meshtastik only talks to the node it is connected to. The user
wants four additions, all of them:

1. **Node list housekeeping** on the connected node: remove, favourite,
   ignore, reset.
2. **Mesh requests** to other nodes: traceroute, position, telemetry.
3. **Admin key management**: list, add and remove the public keys allowed to
   administer a node remotely.
4. **Remote admin**: run the admin commands (config, owner, channels, reboot,
   …) against another node over the mesh with `--dest`.

The original design listed remote admin as a non-goal; that is now reversed.
Everything else in it stands, notably: every write is verified, and failures
are loud and specific.

## Goals

- Each part ships on its own, in the order above (1 and 2 are independent;
  3 is useful alone and is the setup step for 4).
- Remote writes keep the local guarantee: read back and compared, or a
  specific error saying what is unknown.
- Mesh failures name the cause the firmware gives (unauthorised key, unknown
  public key, no route, timeout) and the fix where there is one.
- Everything works through the daemon as well as with `--no-daemon`.

## Non-goals

- Legacy remote admin over an "admin" channel (deprecated in firmware; PKI
  only). The firmware floor stays 2.5.
- Reading a remote node's node list (there is no admin request for it).
- Remote `send`/`listen`, remote hardware GPIO, OTA, file transfer.
- Broadcast requests (telemetry/position from every node).

## Anti-goals

- Reporting a remote write as applied without reading it back.
- Sending a private key over the air (remote `config import --keys`).
- Cutting off a remote node without a warning: changing its LoRa link
  settings or primary channel, removing the admin key that lets us in, or
  shutting it down / factory resetting it (which clears its admin keys).

## Protocol facts (verified against firmware `AdminModule.cpp` on master and the Python client)

- **Authorisation:** the target accepts a remote admin message when it
  arrives PKI-encrypted and the sender's public key is in its
  `security.admin_key` list (max 3 keys of 32 bytes). Otherwise it answers
  with a routing error: `ADMIN_PUBLIC_KEY_UNAUTHORIZED` (37) or
  `NOT_AUTHORIZED` (33). Responses come back PKI-encrypted.
- **Sending:** a `MeshPacket` to the target's node number, port `ADMIN_APP`,
  `pki_encrypted: true`, `want_ack: true`, and `want_response` for requests
  (the Python client does exactly this). The local node encrypts with the
  target's public key from its node list; without it the local node answers
  `PKI_UNKNOWN_PUBKEY` (35) or `PKI_SEND_FAIL_PUBLIC_KEY` (39).
- **Session passkey:** node-wide on the target; returned in every admin
  response; required on every non-request, non-response message from a
  remote (`ADMIN_BAD_SESSION_KEY`, 36, otherwise); valid 300 s from issue,
  re-issued when older than 150 s. Kept per target node.
- **Reads:** a remote node does not send its config on connect; each part is
  requested: `get_owner_request`, `get_device_metadata_request`,
  `get_config_request` (enum `<SECTION>_CONFIG`), `get_module_config_request`
  (enum, e.g. `MQTT_CONFIG`, `EXTNOTIF_CONFIG`), `get_channel_request`
  (index + 1).
- **Private key:** never returned to a remote requester (blanked).
- **Edit transactions:** firmware 2.8.0+ (released 2026-08-30) commits an
  open transaction after 60 s without activity ("committing what it
  applied"), without rebooting. This also applies to local edits: today's
  message "reboot it to discard" is wrong on 2.8+.
- **Node list:** `remove_by_nodenum`, `set_favorite_node`,
  `remove_favorite_node`, `set_ignored_node`, `remove_ignored_node` take a
  node number. `nodedb_reset: <keep favourites>` resets and **reboots**; nodes
  in role ROUTER, ROUTER_LATE or CLIENT_BASE always keep favourites.
  `NodeInfo.is_favorite` / `is_ignored` report the flags in the handshake.
- **Traceroute:** port `TRACEROUTE_APP`, empty `RouteDiscovery`,
  `want_response`, packet hop limit = the requested limit. The response (from
  the target) carries `route` (intermediate nodes towards it), `snr_towards`
  (one more entry than `route`; the last is measured at the target),
  `route_back` and `snr_back` (valid only when the response packet has
  `hop_start` set and `snr_back` has one more entry than `route_back`). SNR
  is dB × 4; −128 means unknown. A routing error ends it (firmware rate-limits
  traceroutes: `RATE_LIMIT_EXCEEDED`, 38).
- **Position / telemetry requests:** port `POSITION_APP` with a `Position`,
  or `TELEMETRY_APP` with a `Telemetry` whose variant is set to an empty
  message of the wanted kind (`device_metrics`, `environment_metrics`,
  `power_metrics`, `local_stats`), `want_response`. The answer is a packet of
  the same port with `request_id` set.

## Approaches (remote admin, the part with real choices)

1. **A `Remote` node type beside the connected client (chosen).** The device
   package gets a `Node` interface implemented by the connected node and by
   `Remote`; Apply's transaction is shared, and only "read current state",
   "send an admin message" and "re-read after commit" differ. Commands call
   `a.node(...)` and work on either.
2. **Make every command fetch through one generic remote/local switch in the
   CLI.** Keeps the device package unchanged but duplicates the transaction,
   verification and reboot logic in the CLI. Rejected.
3. **Proxy remote admin through the daemon.** No benefit; remote admin is
   per command. Rejected.

## How it works

### 1. Node list housekeeping

`nodes` still lists; it gains subcommands `remove`, `favorite`,
`unfavorite`, `ignore`, `unignore` (each takes one or more nodes as `send
--to` does) and `reset [--keep-favorites]`. Each change is verified by
re-reading the node list (a handshake on the same connection). The list
shows a FLAGS column (`fav`, `ignored`) and JSON gains `is_favorite`,
`is_ignored`. `reset` asks for confirmation and says the node reboots;
`remove` of several nodes asks too. The connected node itself cannot be
removed or ignored.

### 2. Mesh requests

- `traceroute <node> [--hop-limit n]` (default: the node's `lora.hop_limit`)
  prints the route both ways with SNR per hop, e.g.

  ```
  towards:  me → Hill (HIL1) 6.25 dB → Car (CAR1) 4.5 dB
  back:     Car (CAR1) → Hill (HIL1) 5.0 dB → me 7.75 dB
  ```

- `request position <node>` and
  `request telemetry <node> [--type device|environment|power|local-stats]`
  print the answer (and `--json`).
- One attempt each (they cost airtime); `--timeout` bounds the wait, default
  60 s over the mesh. stderr says "asking !abcd1234 (this can take a while)".

### 3. Admin keys

- `info` shows the node's public key (`base64:…`), the value to give to
  other nodes.
- `admin-key list` shows the node's `security.admin_key` entries with the
  name of the node each belongs to when the node list knows it.
- `admin-key add <key|node>`: a `base64:` key, or a node (id, number or name)
  whose public key the connected node knows. Refuses a 4th key and keys that
  are not 32 bytes. `admin-key remove <key|node|index>`.
- Both are ordinary verified security-section changes. With part 4 they take
  `--dest` like every admin command.

### 4. Remote admin

- Global `--dest <node>`: id, number or name resolved against the connected
  node's node list. Supported by `info`, `config get/set/export/import`,
  `owner set`, `channel list/add/set/del/url`, `reboot`, `shutdown`,
  `factory-reset`, `nodes remove/favorite/…/reset`, `admin-key …`. Every
  other command refuses it.
- Before anything, the remote node is asked for its metadata: proves it is
  reachable and authorises us, gets the session passkey, checks firmware ≥
  2.5.
- Reads fetch only what the command needs (`config get lora.region` fetches
  the lora section); a full `config get`/`export` fetches every section with
  progress on stderr.
- Each attempt waits `--timeout` (default 30 s over the mesh); requests and
  writes are tried up to 3 times. `ADMIN_BAD_SESSION_KEY` refreshes the
  passkey and retries once.
- Writes: begin, each change acknowledged by the target, commit; then wait
  `RebootGrace`, re-fetch what changed (retrying while the node reboots, up
  to `ReconnectTimeout`), compare. A remote fixed position is verified by
  `position.fixed_position` only (its coordinates cannot be read remotely);
  the result says so.
- Interrupted remote edit: the error says the firmware commits what it
  received after 60 s idle (2.8+), so the user should check with `config get`.
- Remote warnings (confirm, or `--yes`): any LoRa link setting (region,
  preset, bandwidth, spreading factor, coding rate, frequency, channel
  number, `tx_enabled` false), primary channel PSK/name/role, an
  `admin_key` list without this node's public key. `shutdown` and
  `factory-reset` prompts say the node cannot be reached remotely afterwards.
  The `is_managed` refusal applies only to local changes.
- `config export --dest` writes no private key and says so;
  `config import --keys --dest` is refused.
- The daemon re-reads its cached state only for admin packets addressed to
  the connected node, not for remote admin traffic.

## Calls made by the builder

- Mesh timeouts: 30 s per attempt and 3 attempts for admin; 60 s and one
  attempt for traceroute/position/telemetry.
- `--dest` is a global flag (as in the Python client), refused where it
  does not apply.
- Traceroute default hop limit is the connected node's `lora.hop_limit`
  (3 when unset).
- Order of work: 1, 2, 3, 4; each part is releasable.

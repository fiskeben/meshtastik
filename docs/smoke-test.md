# Hardware smoke test

Run this checklist against a real node before a release. Repeat the core section once per transport:

- Serial: `--port <path>`
- TCP: `--host <host>`
- BLE: `--ble <name or address>` (find it with `meshtastik ble scan`)

Use a node you can reset. Firmware 2.5 or newer is required. Note firmware version, board and OS in your results.

## Preconditions

- [ ] `meshtastik version` prints a version.
- [ ] Back up first: `meshtastik config export -o pre-test.yaml` (the file holds the private key and has mode 0600; check with `ls -l`).
- [ ] macOS only: on first BLE use the terminal app gets a Bluetooth permission prompt. Allow it. Note whether you had to re-run the command afterwards.
- [ ] BLE: note the pairing PIN behaviour (prompt shown by the OS, fixed or random PIN, and whether a wrong or cancelled PIN gives a clear error).
- [ ] macOS: `meshtastik ble scan` lists the node, and the address is a CoreBluetooth UUID (not a MAC). Using that UUID with `--ble` connects.

## Core checks (per transport: serial / TCP / BLE)

- [ ] `info` shows the expected node identity, firmware and region.
- [ ] `nodes` lists the nodes the node knows about.
- [ ] `config get lora` prints the LoRa section.
- [ ] `config set lora.hop_limit <n>` (pick a different value) reports verified, and `config get lora.hop_limit` shows `<n>`. Restore the original value.
- [ ] After any `config set`, `config get` on the same section shows exactly what was set. Record every spurious "not applied by the node" mismatch (a field the firmware normalises), with the field name: ______
- [ ] Reboot-requiring change: `config set device.role <other role>`, then restore. Confirm the tool reconnects and reports verified (timing per transport below).
- [ ] `owner set "Smoke Test" --short SMK` works and `info` shows the new names. Restore the original names.
- [ ] `channel add test` succeeds and `channel list` shows it. `channel del 1` removes it (check the index with `channel list` first).
- [ ] `channel url` prints a URL and a QR code. Scan the QR with the phone app; it offers the same channels.
- [ ] `config export -o backup.yaml`, then `config import backup.yaml --keys` on the same node reports `No changes needed.`
- [ ] `config import backup.yaml` (without `--keys`) also reports `No changes needed.` and leaves the node's keys alone.
- [ ] Negative value: `config set -- lora.tx_power -5` is accepted (restore afterwards).
- [ ] `send "hello"` (broadcast) completes, and a second device or the phone app sees the message.

## Reboot behaviour by transport

- [ ] Serial: change a reboot-requiring setting (e.g. `device.role`). Observe whether the node sends a reboot notice and how long verify takes. It should finish and say verified. If it takes about 15 s, the node did not announce its reboot; note the time: ______
- [ ] BLE: make the same reboot-triggering change. Expect up to about 30 s before the reconnect (known limitation: no BLE disconnect callback yet). It should then reconnect and say verified. Time: ______
- [ ] TCP: same change; note the time and whether it reconnects: ______

## Position and import idempotence

- [ ] Set a fixed position by importing a backup that contains `location` (edit `backup.yaml` to add it). Check with `config get position`.
- [ ] Import the same file again: expect `No changes needed.`

## Direct messages (needs a second node)

- [ ] `send --to <node> "ping"` to a reachable node: the output says `Acknowledged by ...`.
- [ ] `send --to <node> "ping"` when the mesh relays the packet but no acknowledgement comes back: the output says it was relayed by the mesh, but delivery is unconfirmed. Confirm the two outcomes read clearly differently.
- [ ] `send --to <unreachable node> --ack-timeout 10s "ping"` ends with a clear message after the timeout.

## Node list

- [ ] `nodes favorite <node>`, then `nodes`: the FLAGS column shows `fav`. `nodes unfavorite <node>` clears it.
- [ ] `nodes ignore <node>` / `nodes unignore <node>` report done (verified by re-reading the list).
- [ ] `nodes remove <node>` reports done and `nodes` no longer lists it.
- [ ] `nodes ignore !<an unheard id>` on firmware 2.8+ adds an ignored entry; on older firmware it says older firmware can only ignore listed nodes.
- [ ] `nodes reset --keep-favorites` over TCP: it waits for the reboot (~7 s after the request; over TCP up to 15 s) and reconnects, and `nodes` shows only the node and its favourites. Time: ______

## Mesh requests (needs a second node, ideally two hops away)

- [ ] `traceroute <node>` prints both directions with an SNR per hop. Record whether unknown hops appear: ______
- [ ] A second `traceroute` within 30 s fails at once with the node's rate-limit message.
- [ ] `request position <node>` prints coordinates for a node that shares its position; a node without one says it received the request but did not answer it.
- [ ] `request telemetry <node>` prints device metrics; `--type environment` on a node without sensors says it did not answer.
- [ ] A two-hop target answers (requests carry the node's hop limit).

## Remote admin (needs a second node you can reach locally once)

- [ ] Locally on the remote node: `admin-key add <your node's public key from info>`; `admin-key list` shows it.
- [ ] From your node: `--dest <remote> info` shows the remote's identity, firmware and public key.
- [ ] `--dest <remote> config get lora` prints its LoRa section.
- [ ] `--dest <remote> config set lora.hop_limit <n>` reports verified; restore it.
- [ ] A reboot-requiring remote change (`device.role`) waits for the node and reports verified. Time: ______
- [ ] `--dest <remote> config set lora.region <other>` asks for confirmation first (answer `n`).
- [ ] With your key removed from the remote (`admin-key remove` locally on it), `--dest <remote> info` fails with the "does not list this node's public key" message.
- [ ] `--dest <remote> config export` writes a backup without a private key and says so.

## Lock-out prompt (BLE)

- [ ] `config set bluetooth.enabled false` over BLE asks for confirmation because it would lock you out. Answer `n`: nothing changes and the node stays connected. Do not answer `y` unless you can still reach the node over serial or TCP.

## Python export compatibility

- [ ] If you have a real Python `meshtastic --export-config` output, replace `internal/backup/testdata/python-export.yaml` with it (strip secrets you do not want committed) and run `go test ./internal/backup/`. Result: ______
- [ ] Import that Python file with `config import` on a node, confirm it applies, then re-import and see `No changes needed.`

## IRC (ircmesh)

Run `ircmesh --device <name>` (or `--host`/`--port`/`--ble`) and repeat with each of irssi, WeeChat and HexChat, using default client settings.

- [ ] The client registers and is renamed to the node's nick. With an emoji short name, the long name is used.
- [ ] Every channel is joined (`#LongFast` or the primary's name, plus private channels), and each topic shows its index, preset and key type.
- [ ] Channel member lists match which nodes the Android app shows on each channel.
- [ ] A message in a channel from the phone app or another node shows up from the right nick. A message sent from IRC arrives on the other device.
- [ ] `/msg <nick> hi` arrives as a direct message. A DM to a node that is off gives a "not delivered" notice within about 30 s.
- [ ] Optional, with a client that shows reactions (gamja, Goguma): a DM to a node that is on gets ✅ on the message, and one to a node that is off gets ❌ along with the notice. What you send shows up once, not twice (`echo-message`).
- [ ] `/me waves` arrives as `* <nick> waves`. A reaction and a reply from the app show up quoted.
- [ ] A message of about 450 characters arrives as 3 messages. One of about 700 is refused with a notice.
- [ ] Messages from before the client connected, including ones you sent from IRC, are replayed on connect (with their original timestamps in WeeChat/HexChat), and again on every later connect.
- [ ] Optional, with a `draft/chathistory` client (gamja, Goguma, senpai): nothing is replayed on connect, the client loads recent history itself, and scrolling back loads older messages.
- [ ] Two clients at once: what one sends shows up in the other.
- [ ] `meshtastik send "from cli"` shows up in IRC from your nick.
- [ ] Rebooting the node gives "mesh link down" and then "mesh link back up" notices; messages sent in between are refused, not sent late.
- [ ] Renaming a channel with `meshtastik channel` makes clients leave the old name and join the new one within 10 minutes (or right after a reboot).
- [ ] `--listen 0.0.0.0:6667` without a password is refused. With `MESHTASTIK_IRC_PASSWORD` set, a client without the password is rejected.

## Wrap-up

- [ ] Restore the node from `pre-test.yaml`: `config import pre-test.yaml --keys`.
- [ ] Record all failures, timings and mismatches above as issues.

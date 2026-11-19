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

## Lock-out prompt (BLE)

- [ ] `config set bluetooth.enabled false` over BLE asks for confirmation because it would lock you out. Answer `n`: nothing changes and the node stays connected. Do not answer `y` unless you can still reach the node over serial or TCP.

## Python export compatibility

- [ ] If you have a real Python `meshtastic --export-config` output, replace `internal/backup/testdata/python-export.yaml` with it (strip secrets you do not want committed) and run `go test ./internal/backup/`. Result: ______
- [ ] Import that Python file with `config import` on a node, confirm it applies, then re-import and see `No changes needed.`

## Wrap-up

- [ ] Restore the node from `pre-test.yaml`: `config import pre-test.yaml --keys`.
- [ ] Record all failures, timings and mismatches above as issues.

# meshtastik `listen` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** A `listen` command that prints received mesh messages: the node's queued backlog right after connecting, then live messages until Ctrl-C.

**Architecture:** The device client buffers every MeshPacket the node delivers (from the moment the handshake starts) in a bounded queue, and exposes `Listen`, which drains that queue and then streams live packets, reconnecting if the link drops. The CLI formats packets as human lines or JSON lines.

**Tech Stack:** as the main plan (`docs/superpowers/plans/2026-11-19-meshtastik.md`); no new dependencies.

**Spec:** approved in conversation on 2026-11-19 (extends `docs/superpowers/specs/2026-11-19-meshtastic-go-cli-design.md`, which had listed packet logging as a non-goal; the user asked for it):

```
meshtastik listen [--channel N] [--json] [--all] [--count N]

15:42:07  ch0  Base Station (BASE) → all   : anyone on the hill tonight?
15:42:31  ch0  Car (CAR1)          → me    : yes, 20 min out
```

- Connects, prints the backlog, then keeps printing new text messages until Ctrl-C (exit 0 on Ctrl-C).
- Sender names from the node DB; "→ all" for broadcast, "→ me" for the local node, otherwise the target's name or `!id`.
- `--json`: one JSON object per line. `--all`: other packet types too, shown by port name. `--count N`: exit after N printed messages (for scripts and tests).
- Reconnects if the node reboots or the link drops; keeps going.

## Global Constraints

- Everything in the main plan's Global Constraints still applies.
- Never print key material. Encrypted packets the node couldn't decode are shown only with `--all`, as `<encrypted>`.
- stdout carries only messages; status/notes go to stderr.

## Review Focus

1. Backlog packets arrive immediately after `config_complete_id`, before the CLI calls `Listen` → they must not be lost (buffer from handshake start).
2. A node with heavy traffic while nobody is listening → bounded buffer (256), oldest dropped, drop count reported to stderr once `Listen` starts; never unbounded memory, never blocking the reader goroutine.
3. Home Assistant (or another TCP client) kicks the connection repeatedly → `Listen` reconnects with the existing backoff and gives up with a clear error after `ReconnectTimeout`, rather than spinning.
4. Non-UTF-8 or control characters in text → printed safely (invalid UTF-8 replaced, control chars except tab escaped), never corrupting the terminal.
5. Packets that are responses to our own requests (admin replies, routing acks) are not printed as messages unless `--all`.

---

### Task 1: Packet buffer + Listen in the device client, `listen` command

**Files:**
- Modify: `internal/device/client.go` (buffer packets in dispatch), Create: `internal/device/listen.go`
- Modify: `internal/fakedevice/fake.go` (knobs below)
- Create: `internal/cli/listen.go`; Modify: `internal/cli/root.go` (register)
- Test: `internal/device/listen_test.go`, `internal/cli/listen_test.go`

**Interfaces:**
- Consumes: `Client` link/reader/dispatch/reconnect machinery (client.go), `State`, waiter map (packets with a matching waiter are still also buffered — filtering is the CLI's job via `--all`).
- Produces:
  ```go
  // device
  const packetBuffer = 256
  // Listen calls fn for each buffered packet (oldest first), then for each new
  // packet, until ctx ends (returns nil) or fn returns an error (returned as is).
  // If the link drops, it reconnects (redial + handshake, same backoff and
  // ReconnectTimeout as Apply) and continues; reconnect failure is returned.
  // dropped reports packets discarded because the buffer was full before Listen ran.
  func (c *Client) Listen(ctx context.Context, fn func(*pb.MeshPacket) error) error
  func (c *Client) DroppedPackets() int
  // fakedevice
  Backlog []*pb.MeshPacket            // emitted right after config_complete_id on every handshake (first handshake only? → first dial only)
  func (d *Device) Inject(p *pb.MeshPacket) // emit FromRadio{Packet} on the current link (no-op if none)
  func (d *Device) DropLink()               // close the current link from the device side (simulates a kick/drop)
  ```
  - CLI JSON line keys: `time` (RFC 3339, from `rx_time` if non-zero else receive time), `from` (`!%08x`), `from_name` (long name or ""), `to` (`!%08x` or `"broadcast"`), `channel`, `port` (PortNum enum name), `text` (only for TEXT_MESSAGE_APP), `snr`, `rssi`, `encrypted` (bool, only when true).
  - Human line: `HH:MM:SS  ch<N>  <Long> (<SHORT)  → <all|me|name>  : <text>`; for `--all` non-text: `: [<PORT_NAME>]`; names fall back to `!id`.

- [ ] **Step 1: Write the failing tests**

```go
// internal/device/listen_test.go
func text(from uint32, to uint32, s string) *pb.MeshPacket {
	return &pb.MeshPacket{From: from, To: to, Id: rand.Uint32() | 1,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_TEXT_MESSAGE_APP, Payload: []byte(s)}}}
}
func TestListenDeliversBacklogThenLive(t *testing.T) {
	d := fakedevice.New()
	d.Backlog = []*pb.MeshPacket{text(42, device.Broadcast, "old")}
	c, _ := open(t, d, device.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var got []string
	go func() { time.Sleep(50 * time.Millisecond); d.Inject(text(42, device.Broadcast, "new")) }()
	err := c.Listen(ctx, func(p *pb.MeshPacket) error {
		got = append(got, string(p.GetDecoded().GetPayload()))
		if len(got) == 2 { cancel() }
		return nil
	})
	if err != nil || !slices.Equal(got, []string{"old", "new"}) { t.Fatal(got, err) }
}
func TestListenBufferBounded(t *testing.T) {
	d := fakedevice.New()
	for i := range 300 { d.Backlog = append(d.Backlog, text(42, device.Broadcast, fmt.Sprint(i))) }
	c, _ := open(t, d, device.Options{})
	time.Sleep(100 * time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	var first string; n := 0
	c.Listen(ctx, func(p *pb.MeshPacket) error {
		if n == 0 { first = string(p.GetDecoded().GetPayload()) }
		n++
		if n == 256 { cancel() }
		return nil
	})
	if first != "44" || c.DroppedPackets() != 44 { t.Fatal(first, c.DroppedPackets()) }
}
func TestListenReconnectsAfterDrop(t *testing.T) {
	d := fakedevice.New()
	c, _ := open(t, d, device.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() {
		time.Sleep(50 * time.Millisecond); d.DropLink()
		for d.Dials() < 2 { time.Sleep(10 * time.Millisecond) }
		time.Sleep(100 * time.Millisecond); d.Inject(text(42, device.Broadcast, "after"))
	}()
	var got string
	err := c.Listen(ctx, func(p *pb.MeshPacket) error { got = string(p.GetDecoded().GetPayload()); cancel(); return nil })
	if err != nil || got != "after" { t.Fatal(got, err) }
}
func TestListenGivesUpWhenNodeGone(t *testing.T) { /* DropLink + RefuseDialsAfterReboot-style knob (reuse existing refuse knob or add RefuseDials) with ReconnectTimeout 300ms → error contains "did not come back" */ }

// internal/cli/listen_test.go  (uses runCLI; --count makes it terminate)
func TestListenHuman(t *testing.T) {
	d := fakedevice.New()
	d.ExtraNodes = []*pb.NodeInfo{{Num: 42, User: &pb.User{LongName: "Car", ShortName: "CAR1"}}}
	d.Backlog = []*pb.MeshPacket{text(42, 0xffffffff, "yes, 20 min out"), text(42, d.NodeNum, "for you")}
	out, _, err := runCLI(t, d, "", "listen", "--count", "2")
	if err != nil || !strings.Contains(out, "Car (CAR1)") || !strings.Contains(out, "→ all") || !strings.Contains(out, "→ me") || !strings.Contains(out, ": yes, 20 min out") { t.Fatal(out, err) }
}
func TestListenJSON(t *testing.T) { /* --json --count 1 → one line, json.Unmarshal ok, from "!0000002a", to "broadcast", port "TEXT_MESSAGE_APP", text set */ }
func TestListenChannelFilter(t *testing.T) { /* backlog ch0 "a", ch1 "b"; --channel 1 --count 1 → only "b" */ }
func TestListenAllShowsOtherPorts(t *testing.T) { /* backlog POSITION_APP + text; default --count 1 shows the text only; --all --count 2 shows "[POSITION_APP]" first */ }
func TestListenHidesOwnResponsesByDefault(t *testing.T) { /* a ROUTING_APP packet with RequestId set is not printed without --all */ }
func TestListenSanitisesText(t *testing.T) { /* payload "hi\x1b[31mred\xff" → output has no ESC byte, contains "\\x1b" or similar escape and U+FFFD */ }
```

- [ ] **Step 2: Run** `go test ./internal/device/ ./internal/cli/` → FAIL (undefined Listen / Backlog / listen command).
- [ ] **Step 3: Implement.**
  - **Buffer:** in `dispatch`, every `FromRadio_Packet` (after existing state/waiter handling) is appended to a client-level ring (mutex-guarded slice, cap `packetBuffer`, drop oldest, count drops) and a non-blocking signal wakes any active `Listen`. The ring lives on `Client`, not `link`, so it survives reconnects. Buffer from the first handshake (packets arriving before `config_complete_id` count too).
  - **Listen:** loop: drain ring → call fn; wait on (signal | current link's readerEnd | ctx.Done). On readerEnd: call the existing reconnect path (redial + handshake with backoff, `ReconnectTimeout`), print nothing itself, continue. If `fn` returns an error, return it. ctx done → return nil.
  - **CLI:** `listen` connects via `connect`, prints `Listening on <node name> — Ctrl-C to stop` to stderr, and a stderr note `note: N older messages were dropped (buffer full)` if `DroppedPackets() > 0` on the first drain. Filters: default = TEXT_MESSAGE_APP only, excluding packets with `Decoded.RequestId != 0`; `--all` = everything (encrypted → `<encrypted>`); `--channel` validated 0–7. Names resolved from `c.State()` at print time (refreshes as NodeInfo updates arrive). Text sanitising: `strings.ToValidUTF8(s, "�")`, then escape runes < 0x20 except `\t` and 0x7f as `\xNN`. Ctrl-C (context canceled from the signal context) exits 0 without "error:". `--count N` (N ≥ 1) cancels after N printed lines.
  - **Fake:** `Backlog` emitted after `config_complete_id` on the first handshake only; `Inject`, `DropLink` as above (state read under `d.mu`).
- [ ] **Step 4: Run** `go test -race ./... && go vet ./...` → PASS.
- [ ] **Step 5:** README: add `listen` to the command table with a one-line note on the backlog/no-history and HA traffic split. Commit `feat: listen for received messages`.

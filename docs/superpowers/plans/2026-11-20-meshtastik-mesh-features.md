# meshtastik mesh features Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add node list housekeeping, mesh requests (traceroute, position, telemetry), admin key management, and remote admin over the mesh (`--dest`) to meshtastik.

**Architecture:** Part A adds node-list admin messages to `device.Client` and `nodes` subcommands. Part B adds a generic "request and wait for the answer" primitive on `device.Client`, plus a simulated remote node in `fakedevice`, then traceroute/position/telemetry on top. Part C is CLI-only on top of the verified `Apply`. Part D introduces a `device.Node` interface implemented by the connected node (`*device.Local`) and by `*device.Remote`. Apply's transaction is shared through an internal `adminTarget` seam, and each admin command acts on `a.node(cmd, c)`.

**Tech Stack:** Go 1.27, cobra, official Buf-generated Meshtastic protobufs, the in-memory `internal/fakedevice` for tests.

**Spec:** `docs/superpowers/specs/2026-11-20-meshtastik-mesh-features-design.md`

Four parts, each releasable on its own: A (Tasks 1–3), B (4–7), C (8–9), D (10–16). Run `make check` at the end of every task; it must pass before the commit.

## Global Constraints

- Firmware floor stays 2.5; remote admin is PKI only (no "admin" channel).
- Admin keys: at most 3, each exactly 32 bytes; written and shown as `base64:<data>`.
- Mesh timeouts: admin 30 s per attempt, up to 3 attempts; traceroute/position/telemetry 60 s, one attempt. `--timeout`, when set, replaces the per-attempt value.
- SNR in traceroutes is dB × 4 on the wire; −128 means unknown.
- Every write is verified by reading back; a remote fixed position is verified by `position.fixed_position` only, and the output says so.
- No private key over the air: remote `config import --keys` is refused; remote `config export` omits it with a note on stderr.
- Errors name the routing error and the fix: 37 `ADMIN_PUBLIC_KEY_UNAUTHORIZED` → "!x does not list this node's public key in security.admin_key; on !x run: meshtastik admin-key add <this node's key>"; 35/39 → "this node has no public key for !x yet; wait until it has heard !x's node info"; 36 → refresh passkey and retry once; 1/5/8/3 (`NO_ROUTE`, `MAX_RETRANSMIT`, `NO_RESPONSE`, `TIMEOUT`) → "no route to !x".
- Commands keep working through the daemon and with `--no-daemon`; tests use the existing `runCLI` (direct) and `testDaemon` (daemon) harnesses.
- Match the existing code: comment density, error wording style ("lowercase, specific"), table output via `tabwriter`, JSON via `printJSON`.

## Review Focus

1. **A lost ack for a write that the remote did apply** (common on LoRa). The retry must not double-apply anything harmful, and verification must still pass. Test: `TestRemoteApplyRetriesLostAck` (Task 13).
2. **The remote node reboots after commit and answers nothing for a while.** Re-fetch must keep retrying until `ReconnectTimeout`, then say "committed but could not be verified". Test: `TestRemoteApplyWaitsOutReboot` (Task 13).
3. **The session passkey expires mid-command** (more than 300 s, or the node rebooted). This gives `ADMIN_BAD_SESSION_KEY`, which must refresh once and succeed. Test: `TestRemoteBadSessionKeyRefreshes` (Task 11).
4. **`--dest` naming the connected node itself** (by name or id). It should act locally, as if `--dest` were absent. Test: `TestDestSelfIsLocal` (Task 15).
5. **A response arriving from the wrong node or for another request id**, e.g. another client's traffic through the daemon. It must be ignored, not taken as the answer. Test: `TestRequestIgnoresOtherAnswers` (Task 5).

---

## Part A — Node list housekeeping

### Task 1: Correct the interrupted-edit message

**Files:**
- Modify: `internal/device/admin.go` (the `ErrPartialEdit` declaration)
- Modify: `README.md` (the "Ctrl-C during a change" note)
- Test: `internal/device/admin_test.go`

**Interfaces:**
- Produces: `device.ErrPartialEdit` keeps its identity (tests use `errors.Is`); only its text changes.

- [ ] **Step 1: Write the failing test** `TestPartialEditMessageExplainsAutoCommit`: `ErrPartialEdit.Error()` contains `"60 s"` and `"2.8"` and does not contain `"reboot it to discard"`.
- [ ] **Step 2: Run** `go test ./internal/device -run TestPartialEditMessage` and expect FAIL.
- [ ] **Step 3: Change the text** to exactly: `edit interrupted before commit; firmware 2.8 and newer saves what it received after 60 s, older firmware holds it until reboot — check with config get`. In the README note, replace "(reboot the node to discard it)" with "(firmware 2.8 and newer saves the partial edit after 60 seconds; older firmware discards it on reboot)".
- [ ] **Step 4: Run** `make check` and expect PASS.
- [ ] **Step 5: Commit** `fix: interrupted-edit message matches firmware 2.8's auto-commit`.

### Task 2: Node list operations in the device client and fake

**Files:**
- Create: `internal/device/nodedb.go`
- Modify: `internal/fakedevice/admin.go` (handle the six messages), `internal/fakedevice/fake.go` (handshake emits `IsFavorite`/`IsIgnored`; Extra nodes mutable under lock)
- Test: `internal/device/nodedb_test.go`

**Interfaces:**
- Produces:
  ```go
  type NodeOp int
  const (RemoveNode NodeOp = iota; Favorite; Unfavorite; Ignore; Unignore)
  func (op NodeOp) String() string // "remove", "favorite", …
  // EditNodeDB applies op to node num on the connected node and verifies it by re-reading the node list.
  func (c *Client) EditNodeDB(ctx context.Context, op NodeOp, num uint32) error
  // ResetNodeDB clears the node list (keeping favourites if asked); the node reboots.
  func (c *Client) ResetNodeDB(ctx context.Context, keepFavorites bool) error
  ```
- Errors: `num == own node` → `errors.New("cannot <op> the connected node itself")`; not applied → `*VerifyError` with `Mismatch{Path: "node !0000002a favorite", Want: "true", Got: "false"}` (`remove`: `Want: "absent", Got: "present"`).

- [ ] **Step 1: Write failing tests** in `nodedb_test.go`:
  - `TestFavoriteAndIgnoreAreVerified`: fake with `ExtraNodes` 42. After `EditNodeDB(ctx, Favorite, 42)`, `c.State().Nodes[42].GetIsFavorite()` is true and the fake received `SetFavoriteNode: 42`. The same for `Ignore`/`Unignore`/`Unfavorite`.
  - `TestRemoveNodeIsVerified`: after `RemoveNode`, `Nodes[42]` is absent.
  - `TestEditNodeDBRefusesSelf`: `EditNodeDB(ctx, Ignore, d.NodeNum)` errors and nothing is sent.
  - `TestEditNodeDBNotApplied`: fake option `IgnoreNodeOps bool` set. The call returns `*VerifyError`.
  - `TestResetNodeDB`: `ResetNodeDB(ctx, true)` sends `NodedbReset: true`. The fake reboots (closes the link after `rebooted`), the client reconnects, and the node list then holds only self and favourites.
- [ ] **Step 2: Run** `go test ./internal/device -run 'NodeDB|Favorite|RemoveNode'` and expect FAIL (undefined).
- [ ] **Step 3: Implement the fake.**
  - `admin()` handles `SetFavoriteNode`, `RemoveFavoriteNode`, `SetIgnoredNode`, `RemoveIgnoredNode` and `RemoveByNodenum` by editing `d.ExtraNodes` under `d.mu`. These are no-ops when `IgnoreNodeOps` is set.
  - `NodedbReset` keeps only favourites when the value is true, then reboots like `RebootOnCommit`.
- [ ] **Step 4: Implement `nodedb.go`.**
  - Send via `c.action` (passkey and post).
  - Verify with `c.Refresh(ctx)`, then inspect `c.State()`.
  - `ResetNodeDB` posts, then calls `c.awaitNode(ctx)` (it reboots) under `applyMu`.
- [ ] **Step 5: Run** `make check` and expect PASS.
- [ ] **Step 6: Commit** `feat(device): node list remove/favorite/ignore/reset with verification`.

### Task 3: `nodes` subcommands and flags column

**Files:**
- Modify: `internal/cli/info.go` (`newNodesCmd`: keep the list as the parent's `RunE`, add the subcommands, add FLAGS, JSON fields)
- Create: `internal/cli/nodes.go` (the subcommands)
- Modify: `README.md` (Commands table)
- Test: `internal/cli/nodes_test.go`

**Interfaces:**
- Consumes: `device.EditNodeDB`, `device.ResetNodeDB`, `device.NodeOp`, `resolveNode(st, q)` from `send.go`.
- Produces: `nodeRow` gains `IsFavorite bool \`json:"is_favorite"\`` and `IsIgnored bool \`json:"is_ignored"\``. The table header becomes `ID NAME SHORT LAST HEARD SNR HOPS BATTERY FLAGS` (FLAGS: `fav`, `ignored`, `fav,ignored` or `-`).
- Commands:
  - `nodes remove <node>...`: asks "Remove N nodes from the node list?" when N > 1.
  - `nodes favorite|unfavorite|ignore|unignore <node>...`.
  - `nodes reset [--keep-favorites]`: always asks "Reset the node list? The node reboots."
  - Each prints `"<op> !0000002a (Car): done"` per node.

- [ ] **Step 1: Write failing tests**:
  - `TestNodesFavoriteShowsInList`: `nodes favorite CAR1`, then `nodes` has `fav` in Car's row, and `--json nodes` has `"is_favorite": true`.
  - `TestNodesRemoveSeveralAsks`: `nodes remove CAR1 BUS2` with stdin `n` → error "aborted"; with `y`, both are gone.
  - `TestNodesResetAsksAndReboots`.
  - `TestNodesThroughDaemon`: the same favourite flow via `testDaemon.run`.
- [ ] **Step 2: Run** `go test ./internal/cli -run Nodes` and expect FAIL.
- [ ] **Step 3: Implement** the subcommands and the column. Resolve every node before sending anything. Reuse `confirmOrAbort`.
- [ ] **Step 4: Run** `make check` and expect PASS.
- [ ] **Step 5: Commit** `feat: nodes remove/favorite/ignore/reset`.

---

## Part B — Mesh requests

### Task 4: Simulated remote nodes in the fake

**Files:**
- Create: `internal/fakedevice/remote.go`
- Modify: `internal/fakedevice/fake.go` (`serve`: route packets whose `To` is a remote; the handshake emits a `NodeInfo` per remote including `User.PublicKey`)
- Test: `internal/fakedevice/remote_test.go`

**Interfaces:**
- Produces:
  ```go
  // Remote is a node reached through the fake's radio.
  type Remote struct {
      Num             uint32
      Owner           *pb.User          // PublicKey set by New... helpers
      FirmwareVersion string
      Config          *pb.LocalConfig
      ModuleConfig    *pb.LocalModuleConfig
      Channels        [8]*pb.Channel
      AdminKeys       [][]byte          // public keys allowed to administer it
      Route, RouteBack []uint32         // traceroute hops (intermediate nodes)
      Position        *pb.Position
      Telemetry       *pb.Telemetry     // answered for device metrics requests
      Unreachable     bool              // drop everything addressed to it
      FailWith        pb.Routing_Error  // with Unreachable: the local node answers this routing error (e.g. MAX_RETRANSMIT)
      DropFirst       int               // drop this many packets to it, then deliver
      DropAcks        int               // apply, but drop this many want_ack acks
      RotatePasskeyOnce bool            // after the first response, silently invalidate the issued passkey once
      Decoys          bool              // before each answer, emit one with a wrong RequestId and one from another node
      IgnoreFields    []string          // config paths whose new value is dropped, as Device.IgnoreFields
      RebootOnCommit  bool              // after commit: unreachable for RebootFor
      RebootFor       time.Duration
      Latency         time.Duration     // before each answer
  }
  func NewRemote(num uint32, name, short string) *Remote // defaults like New(): filled sections, EU_868, a random 32-byte public key
  // Device gains:
  //   Remotes   map[uint32]*Remote
  //   PublicKey []byte  // the fake's own key: Config.Security.PublicKey, set in New()
  func (d *Device) RemoteAdmin(num uint32) []*pb.AdminMessage // admin messages that reached remote num
  func (d *Device) RemoteRequests(num uint32) []*pb.MeshPacket // every packet that reached remote num
  func (d *Device) WantConfigs() int                          // want_config requests answered (Task 10)
  ```
- Behaviour (one goroutine per answer; emits on the current link):
  - Requires `pki_encrypted` for `ADMIN_APP`. The `PublicKey` is missing from `Remote.Owner` → routing `PKI_UNKNOWN_PUBKEY` from the local node. The fake's `PublicKey` is not in `AdminKeys` → `ADMIN_PUBLIC_KEY_UNAUTHORIZED` from the remote.
  - Session passkey per remote: an 8-byte value, re-issued on every response. A non-get message without it → `ADMIN_BAD_SESSION_KEY`.
  - `want_ack` → routing `NONE` from the remote (after `Latency`).
  - Answers carry `From: remote`, `To: local`, `RequestId: req.Id`, and `PkiEncrypted` copied from the request.
  - Traceroute → `RouteDiscovery{Route, SnrTowards: one per hop + 1 (value 4·(10−i)), RouteBack, SnrBack}` on a packet with `HopStart: 3`.
  - Position → `Position`. Telemetry → `Telemetry` with the requested variant (device: `Telemetry`; others: an empty message of that kind).
  - Security get responses blank `PrivateKey`. Begin/commit pending sets are applied as in local `admin()`.

- [ ] **Step 1: Write failing tests** driving raw `transport.Conn` from `Dialer()`:
  - `TestRemoteAnswersTraceroute`
  - `TestRemoteRefusesUnknownAdminKey`
  - `TestRemoteNeedsPasskeyForSets`
  - `TestRemoteAcksWantAck`
  - `TestRemoteUnreachableSaysNothing`
  - `TestRemoteFailWith`
  - `TestRemoteDropAcksStillApplies`
  - `TestRemoteRotatePasskeyOnce`
  - `TestRemoteBlanksPrivateKey`
- [ ] **Step 2: Run** `go test ./internal/fakedevice -run Remote` and expect FAIL.
- [ ] **Step 3: Implement `remote.go`** and the routing hook in `serve`. Reuse `configSection`/`applySet` logic by making them operate on a `*nodeConfig` shared by `Device` and `Remote`. Extract `struct{ Config; ModuleConfig; Channels; Owner }`, keeping exported fields unchanged.
- [ ] **Step 4: Run** `make check` and expect PASS (existing tests unchanged).
- [ ] **Step 5: Commit** `test(fakedevice): simulated remote nodes`.

### Task 5: Request/response primitive and routing errors

**Files:**
- Create: `internal/device/request.go`
- Modify: `internal/device/client.go` (`Options.MeshTimeout`, default 30 s), `internal/device/text.go` (`SendText` uses `routingErr`)
- Test: `internal/device/request_test.go`

**Interfaces:**
- Produces:
  ```go
  // RoutingError is a routing reply other than NONE.
  type RoutingError struct { From uint32; Reason pb.Routing_Error }
  func (e *RoutingError) Error() string // spec wording per reason (Global Constraints)
  // ErrNoAnswer (wrapped) when the wait ends without an answer.
  var ErrNoAnswer = errors.New("no answer")
  type RequestOptions struct {
      Channel  uint32
      HopLimit uint32        // 0: node default
      PKI      bool          // pki_encrypted
      WantAck  bool
      Timeout  time.Duration // 0: Options.MeshTimeout
  }
  // Request sends payload to node to on port with want_response and returns the first packet
  // from to with request_id equal to the sent id and the same port. It fails on a
  // routing error from anyone, ignores routing NONE (implicit acks), and fails with
  // ErrNoAnswer after the timeout ("no answer from !0000002a after 30s").
  func (c *Client) Request(ctx context.Context, to uint32, port pb.PortNum, payload proto.Message, o RequestOptions) (*pb.MeshPacket, error)
  ```

- [ ] **Step 1: Write failing tests** (against `fakedevice` remotes):
  - `TestRequestReturnsTheAnswer` (position)
  - `TestRequestRoutingError` (the remote is Unreachable but the fake local answers `MAX_RETRANSMIT` → `*RoutingError` with that reason; error text contains "no route to !…")
  - `TestRequestTimesOut` (Unreachable, `Timeout: 100ms` → `errors.Is(err, ErrNoAnswer)`)
  - `TestRequestIgnoresOtherAnswers` (fake option on the remote: first emit an answer with a wrong `RequestId` and one from another node, then the right one → the right one is returned)
  - `TestRequestRefusesSelf` (`to == own node` → error)
- [ ] **Step 2: Run** `go test ./internal/device -run Request` and expect FAIL.
- [ ] **Step 3: Implement.** Register the waiter before sending (as `AdminRequest` does). Watch `l.readerEnd` and return `fmt.Errorf("request: %w", l.readErr)`. Map routing errors through one `routingErr(p *pb.MeshPacket) error` helper, which `SendText` also adopts (its messages stay the same).
- [ ] **Step 4: Run** `make check` and expect PASS.
- [ ] **Step 5: Commit** `feat(device): request/response over the mesh with routing errors`.

### Task 6: Traceroute, position and telemetry in the device client

**Files:**
- Create: `internal/device/mesh.go`
- Test: `internal/device/mesh_test.go`

**Interfaces:**
- Consumes: `Client.Request`, `RequestOptions`.
- Produces:
  ```go
  type Hop struct { Num uint32; SNR *float64 } // SNR nil when unknown (-128) or not reported
  type Route struct { Towards []Hop; Back []Hop } // Back nil when not reported; first hop is the sender (SNR nil)
  func (c *Client) Traceroute(ctx context.Context, to uint32, hopLimit uint32) (*Route, error)
  type TelemetryKind int
  const (DeviceMetrics TelemetryKind = iota; EnvironmentMetrics; PowerMetrics; LocalStats)
  func ParseTelemetryKind(s string) (TelemetryKind, error) // "device", "environment", "power", "local-stats"
  func (c *Client) RequestPosition(ctx context.Context, to uint32) (*pb.Position, error)
  func (c *Client) RequestTelemetry(ctx context.Context, to uint32, kind TelemetryKind) (*pb.Telemetry, error)
  ```
- Route assembly (from the spec's traceroute facts):
  - `Towards = [me] + route[i] (snr_towards[i]) + [to] (snr_towards[last])`, used only when `len(snr_towards) == len(route)+1`; otherwise every SNR is nil.
  - `Back = [to] + route_back[i] (snr_back[i]) + [me] (snr_back[last])`, only when the answer packet has `HopStart != 0` and `len(snr_back) == len(route_back)+1`.
  - SNR = value / 4.

- [ ] **Step 1: Write failing tests**:
  - `TestTracerouteBothWays` (remote with Route `[7]` and RouteBack `[7]` → Towards nums `[me, 7, remote]`, SNRs `[nil, 10, 9]`, Back nums `[remote, 7, me]`)
  - `TestTracerouteUnknownSNR` (−128 → nil)
  - `TestTracerouteNoBack` (`HopStart` 0 → `Back == nil`)
  - `TestRequestTelemetryKinds` (each kind sets the matching variant in the request the fake saw)
  - `TestRequestPosition`
- [ ] **Step 2: Run** and expect FAIL.
- [ ] **Step 3: Implement.** Traceroute timeout: `RequestOptions.Timeout` = `Options.MeshTimeout` × 2 (60 s by default).
- [ ] **Step 4: Run** `make check` and expect PASS.
- [ ] **Step 5: Commit** `feat(device): traceroute, position and telemetry requests`.

### Task 7: `traceroute` and `request` commands

**Files:**
- Create: `internal/cli/mesh.go`
- Modify: `internal/cli/root.go` (register; set `Options.MeshTimeout` from `--timeout` when it is set), `README.md`
- Test: `internal/cli/mesh_test.go`

**Interfaces:**
- Consumes: Task 6.
- Produces:
  - `traceroute <node> [--hop-limit n]`: the default is `st.Config.GetLora().GetHopLimit()`, or 3 when 0. Output lines `towards:  me → Hill (HIL1) 6.25 dB → Car (CAR1) 4.5 dB` and `back:     …` (`?` for unknown SNR). Without a back route it prints `back:     not reported`. JSON: `{"towards":[{"id","name","snr"}],"back":[...]|null}`.
  - `request position <node>` prints lat/lon (degrees, 7 decimals), altitude, time, precision bits. JSON is protojson with proto names.
  - `request telemetry <node> [--type device|environment|power|local-stats]` prints the set fields of the variant as `name: value` lines. JSON is protojson.
  - stderr: `asking !0000002a (Car) — this can take a while`.

- [ ] **Step 1: Write failing tests** (direct and via `testDaemon`):
  - `TestTracerouteOutput` (exact two lines)
  - `TestTracerouteJSON`
  - `TestRequestPositionOutput`
  - `TestRequestTelemetryEnvironment`
  - `TestTracerouteNoRoute` (Unreachable + local `MAX_RETRANSMIT` → error "no route to !…")
- [ ] **Step 2: Run** and expect FAIL.
- [ ] **Step 3: Implement.** Use `nodeName` from `listen.go` for hop names, and `resolveNode` for the argument.
- [ ] **Step 4: Run** `make check` and expect PASS.
- [ ] **Step 5: Commit** `feat: traceroute and request position/telemetry`.

---

## Part C — Admin keys

### Task 8: Show the public key in `info`

**Files:**
- Modify: `internal/cli/info.go`, `README.md`
- Test: `internal/cli/info_test.go`

- [ ] **Step 1: Write the failing test** `TestInfoShowsPublicKey`: give the fake a `Config.Security.PublicKey` of 32 bytes. `info` has the row `Public key:  base64:<std base64>`; `--json info` has `"public_key": "base64:…"`; with no key, the row shows `-`.
- [ ] **Step 2: Run** and expect FAIL.
- [ ] **Step 3: Implement** using `st.Config.GetSecurity().GetPublicKey()`.
- [ ] **Step 4: Run** `make check` and expect PASS.
- [ ] **Step 5: Commit** `feat: info shows the node's public key`.

### Task 9: `admin-key list|add|remove`

**Files:**
- Create: `internal/cli/adminkey.go`
- Modify: `internal/cli/root.go` (register), `README.md`
- Test: `internal/cli/adminkey_test.go`

**Interfaces:**
- Consumes: `applyChanges(cmd, a, c, changes)`, `resolveNode`.
- Produces:
  ```go
  // parseKeyRef turns "base64:<32 bytes>", plain base64, or a node reference into a public key.
  func parseKeyRef(st *device.State, ref string) ([]byte, error)
  ```
  - Errors: `"key must be 32 bytes, got N"`; `"!0000002a has no known public key"`.
  - `admin-key list`: a table `# KEY NODE`. NODE is the name of the node in `st.Nodes` whose `User.PublicKey` equals the key, or `-`. With no keys it prints `no admin keys`. JSON: `[{"index":0,"key":"base64:…","node":"!…"|null}]`.
  - `admin-key add <ref>`: if the key is already listed, it prints `already listed` and exits 0; a 4th key gives `"the node holds at most 3 admin keys; remove one first"`.
  - `admin-key remove <ref|index>`: an index is 0–2.
  - add and remove go through `applyChanges` with one `Config{Security}` change.

- [ ] **Step 1: Write failing tests**:
  - `TestAdminKeyAddByNode` (ExtraNodes 42 with a public key → `security.admin_key` on the fake contains it)
  - `TestAdminKeyAddBase64`
  - `TestAdminKeyRejectsShortKey`
  - `TestAdminKeyFourthRefused`
  - `TestAdminKeyRemoveByIndex`
  - `TestAdminKeyListNamesNodes`
  - `TestAdminKeyAddTwiceIsNoop`
- [ ] **Step 2: Run** and expect FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make check` and expect PASS.
- [ ] **Step 5: Commit** `feat: admin-key list/add/remove`.

---

## Part D — Remote admin

### Task 10: Daemon ignores remote admin traffic for cache staleness

**Files:**
- Modify: `internal/daemon/hub.go` (`session.handle`: mark stale only when `To == own node num || To == 0`)
- Test: `internal/daemon/daemon_test.go`

- [ ] **Step 1: Write the failing test** `TestRemoteAdminDoesNotStaleTheCache`. Send an ADMIN_APP packet with `To: 99` through a session, then open a second client. The fake saw exactly 1 `want_config` (count via a new fake accessor `WantConfigs() int`).
- [ ] **Step 2: Run** and expect FAIL.
- [ ] **Step 3: Implement.** The hub reads its own node number from `h.c.State().MyInfo`.
- [ ] **Step 4: Run** `make check` and expect PASS.
- [ ] **Step 5: Commit** `fix(daemon): only local admin traffic invalidates the cached state`.

### Task 11: Per-destination admin plumbing

**Files:**
- Modify: `internal/device/admin.go` (`passkey []byte` → `passkeys map[uint32][]byte`; `sendAdmin`, `AdminRequest`, `post`, `ensurePasskey` take `to uint32`; the exported `AdminRequest` keeps its signature and targets the local node)
- Create: `internal/device/remote_admin.go`
- Test: `internal/device/remote_admin_test.go`

**Interfaces:**
- Consumes: `Client.Request` (Task 5), `RoutingError`.
- Produces (unexported, used by Tasks 12–13):
  ```go
  func (c *Client) adminRequestTo(ctx context.Context, to uint32, m *pb.AdminMessage) (*pb.AdminMessage, error)
  // remote: PKI + want_ack + want_response, up to 3 attempts of MeshTimeout; on ADMIN_BAD_SESSION_KEY
  // drops the passkey for to, refetches it and retries once. Local (to == own num): today's behaviour.
  func (c *Client) postTo(ctx context.Context, to uint32, m *pb.AdminMessage) error
  // remote: PKI + want_ack, waits for routing NONE from to; up to 3 attempts. Local: today's post.
  func (c *Client) ensurePasskeyFor(ctx context.Context, to uint32) error
  ```
- Precheck for a remote: when the connected node's node list has no `User.PublicKey` for `to`, return `PKI_UNKNOWN_PUBKEY`'s message without sending.

- [ ] **Step 1: Write failing tests**:
  - `TestRemoteAdminRequestGetsOwner`
  - `TestRemoteUnauthorizedNamesTheFix` (the error contains "admin-key add")
  - `TestRemoteUnknownPublicKeyPrecheck` (nothing is sent)
  - `TestRemoteBadSessionKeyRefreshes` (the fake rotates the passkey after the first get → the set gets 36, then succeeds after the refresh)
  - `TestRemoteRequestRetries` (`DropFirst: 2` → succeeds on attempt 3)
  - `TestRemoteRequestGivesUp` (Unreachable, MeshTimeout 50ms → `ErrNoAnswer` after 3 attempts)
  - all existing `admin_test.go` tests unchanged
- [ ] **Step 2: Run** and expect FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make check` and expect PASS.
- [ ] **Step 5: Commit** `feat(device): admin messages to remote nodes with passkeys and retries`.

### Task 12: `device.Node`, `Local` and `Remote` with partial fetch

**Files:**
- Create: `internal/device/node.go`
- Modify: `internal/device/client.go` (add `remote bool` to `State`, unexported, copied by `clone`)
- Test: `internal/device/node_test.go`

**Interfaces:**
- Produces:
  ```go
  // Want selects what Fetch reads. Config/Modules hold section names as in
  // configpath.Sections(); AllSections reads every one.
  type Want struct { Owner, Metadata, Channels bool; Config, Modules []string }
  var AllSections = []string{"*"}
  type Node interface {
      Num() uint32
      IsRemote() bool
      Fetch(ctx context.Context, w Want) (*State, error) // Local ignores w and returns Client.State()
      Apply(ctx context.Context, changes []Change) (int, error)
      Reboot(ctx context.Context, seconds int32) error
      Shutdown(ctx context.Context, seconds int32) error
      FactoryReset(ctx context.Context, full bool) error
      EditNodeDB(ctx context.Context, op NodeOp, num uint32) error
      ResetNodeDB(ctx context.Context, keepFavorites bool) error
  }
  type Local struct{ *Client }   // Num, IsRemote=false, Fetch; the rest are Client methods
  type Remote struct { /* c *Client; num uint32; meta *pb.DeviceMetadata */ }
  // Remote asks num for its metadata (reachability, authorisation, passkey) and checks firmware >= 2.5.
  func (c *Client) Remote(ctx context.Context, num uint32) (*Remote, error)
  // Options.OnProgress, when set, is called with "lora", "mqtt", "channel 3", … as Fetch reads each part.
  ```
- `Remote.Fetch` builds a `State` with `MyInfo{MyNodeNum: num}`, `Nodes[num] = {Num, User: owner}`, `Metadata`, the fetched sections merged into fresh `LocalConfig`/`LocalModuleConfig`, `Channels[i]`, and `remote: true`.
- Section name → enum: `AdminMessage_ConfigType_value[strings.ToUpper(name)+"_CONFIG"]`. For modules, use an explicit map where the proto enum is not `<NAME>_CONFIG`: `external_notification→EXTNOTIF_CONFIG`, `store_forward→STOREFORWARD_CONFIG`, `range_test→RANGETEST_CONFIG`, `canned_message→CANNEDMSG_CONFIG`, `remote_hardware→REMOTEHARDWARE_CONFIG`, `neighbor_info→NEIGHBORINFO_CONFIG`, `ambient_lighting→AMBIENTLIGHTING_CONFIG`, `detection_sensor→DETECTIONSENSOR_CONFIG`, `traffic_management→TRAFFICMANAGEMENT_CONFIG`, `mesh_beacon→MESHBEACON_CONFIG`, `status_message→STATUSMESSAGE_CONFIG`. A test asserts every `configpath.Sections()` module maps to an existing enum value.

- [ ] **Step 1: Write failing tests**:
  - `TestRemoteFetchOnlyWhatIsWanted` (`Want{Config: {"lora"}}` → the remote saw exactly one `get_config_request: LORA_CONFIG`)
  - `TestRemoteFetchAll` (AllSections and channels → equal to the remote's config; progress is called per part)
  - `TestRemoteFirmwareTooOld` (`"2.4.3"` → `ErrUnsupportedFirmware`)
  - `TestEveryModuleSectionHasAnEnum`
  - `TestLocalIsANode` (compile-time `var _ Node = Local{}` and `var _ Node = (*Remote)(nil)`)
- [ ] **Step 2: Run** and expect FAIL.
- [ ] **Step 3: Implement.** `Remote`'s Apply/Reboot/… may return `errors.New("not yet")` until Task 13; the tests above don't call them.
- [ ] **Step 4: Run** `make check` and expect PASS.
- [ ] **Step 5: Commit** `feat(device): Node interface with local and remote nodes`.

### Task 13: Shared Apply transaction and remote writes

**Files:**
- Modify: `internal/device/admin.go` (extract the transaction), `internal/device/nodedb.go`, `internal/device/node.go`
- Test: `internal/device/remote_apply_test.go`

**Interfaces:**
- Produces (internal seam):
  ```go
  type adminTarget interface {
      current(ctx context.Context, changes []Change) (*State, error) // local: c.State(); remote: Fetch of the changed parts
      ensurePasskey(ctx context.Context) error
      post(ctx context.Context, m *pb.AdminMessage) error            // local: post; remote: postTo (acked)
      settle(ctx context.Context, changes []Change) (*State, error)  // local: awaitNode then c.State(); remote: below
  }
  func (c *Client) apply(ctx context.Context, t adminTarget, changes []Change) (int, error) // today's Apply body
  // Notes, not failures: the CLI prints them after "Applied N change(s)" / "sent".
  var ErrCoordinatesUnverified = errors.New("fixed position set; its coordinates cannot be read back from a remote node")
  var ErrNotVerifiable = errors.New("sent; a remote node list cannot be read back")
  ```
- `Client.Apply` = `c.apply(ctx, localTarget{c}, changes)`, so its behaviour and tests are unchanged.
- Remote `settle`:
  - Wait `RebootGrace`, then re-Fetch the changed parts.
  - On `ErrNoAnswer` or a routing error, drop the passkey and keep retrying (backoff 2 s doubling to 10 s) until `ReconnectTimeout`.
  - After that it fails with `"changes were committed but could not be verified: …"`.
- Remote `diff` for `FixedPosition` (when `s.remote`) checks only `position.fixed_position`. `Remote.Apply` then returns `ErrCoordinatesUnverified` wrapped together with the count; the CLI prints it as a note.
- An interrupted remote edit wraps `ErrPartialEdit`.
- `Remote.Reboot`/`Shutdown`/`FactoryReset` use `postTo`. `Remote.EditNodeDB` uses `postTo`; it can't verify (no remote node list), so it returns `ErrNotVerifiable` and the CLI notes "sent; a remote node list cannot be read back". `Remote.ResetNodeDB` is the same.

- [ ] **Step 1: Write failing tests**:
  - `TestRemoteApplyVerified` (lora.hop_limit 5 on a remote)
  - `TestRemoteApplyRetriesLostAck` (the remote applies but drops the first ack → succeeds and is verified)
  - `TestRemoteApplyWaitsOutReboot` (`RebootOnCommit`, `RebootFor` 300ms → verified)
  - `TestRemoteApplyRebootNeverReturns` (`RebootFor` > `ReconnectTimeout` → "committed but could not be verified")
  - `TestRemoteApplyMismatch` (remote option `IgnoreFields` → `*VerifyError`)
  - `TestRemoteFixedPositionCoordinatesUnverified`
  - `TestRemoteRebootSendsAcked`
  - all existing Apply tests unchanged
- [ ] **Step 2: Run** and expect FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make check` and expect PASS.
- [ ] **Step 5: Commit** `feat(device): verified writes to remote nodes`.

### Task 14: Remote lock-out warnings

**Files:**
- Modify: `internal/device/guard.go`
- Test: `internal/device/guard_test.go`

**Interfaces:**
- Produces:
  ```go
  // RemoteRisks lists changes that could cut this node off from remote node `before`.
  func RemoteRisks(before *State, myPublicKey []byte, changes []Change) []string
  ```
- Risks (one string each, naming the field):
  - Any change to `lora.region`, `lora.use_preset`, `lora.modem_preset`, `lora.bandwidth`, `lora.spread_factor`, `lora.coding_rate`, `lora.frequency_offset`, `lora.override_frequency` or `lora.channel_num`.
  - `lora.tx_enabled` false.
  - Channel index 0 `settings.psk`, `settings.name` or `role`.
  - A `security.admin_key` list without `myPublicKey`.
- `ManagedRefusal` is not applied to remote nodes (Task 15 skips it).

- [ ] **Step 1: Write failing tests**, one per risk group, plus `TestRemoteRisksQuietForSafeChange` (`lora.hop_limit` → none).
- [ ] **Step 2: Run** and expect FAIL.
- [ ] **Step 3: Implement** using `configpath.FieldDiffs` on the before/after sections.
- [ ] **Step 4: Run** `make check` and expect PASS.
- [ ] **Step 5: Commit** `feat(device): lock-out warnings for remote changes`.

### Task 15: `--dest` across the admin commands

**Files:**
- Modify: `internal/cli/root.go` (persistent `--dest`; `a.node`), `internal/cli/config.go` (`applyChanges` takes a `device.Node`; get/set/export/import), `internal/cli/info.go` (`info`), `internal/cli/admin.go` (owner, reboot, shutdown, factory-reset), `internal/cli/channel.go`, `internal/cli/nodes.go`, `internal/cli/adminkey.go`; every other command calls `a.refuseDest(cmd)`
- Test: `internal/cli/remote_test.go`

**Interfaces:**
- Consumes: Tasks 12–14.
- Produces:
  ```go
  // node returns what admin commands act on: the connected node, or the --dest node (asked for its
  // metadata first). --dest naming the connected node returns Local.
  func (a *app) node(cmd *cobra.Command, c *device.Client) (device.Node, error)
  func (a *app) refuseDest(cmd *cobra.Command) error // "--dest is not supported by <command path>"
  func applyChanges(cmd *cobra.Command, a *app, c *device.Client, n device.Node, before *device.State, changes []device.Change) error
  ```
- Each command fetches only its `Want`:
  - `config get <path>` → that section
  - `config get`/`export` → `AllSections` + owner + channels
  - `channel …` → channels
  - `owner set` → owner
  - `info` → owner, metadata, `device` and `lora` sections, and channels; JSON gains `"remote": true`
- `config export --dest` writes no private key and prints `note: a remote node never sends its private key; the backup has none` to stderr.
- `config import --keys --dest` → `"--keys cannot be used with --dest: a private key must not be sent over the air"`.
- The `shutdown --dest` prompt adds ` It cannot be powered on remotely.`; `factory-reset --dest` adds ` Its admin keys are erased, so it cannot be administered remotely afterwards.`
- Remote risks go through the same Warning/confirm path as local risks.
- `OnProgress` prints `reading lora…` to stderr only when stderr is a terminal (tests: never).

- [ ] **Step 1: Write failing tests**:
  - `TestDestConfigSetAndGet`
  - `TestDestInfo`
  - `TestDestOwnerSet`
  - `TestDestChannelList`
  - `TestDestExportHasNoPrivateKey`
  - `TestDestImportKeysRefused`
  - `TestDestUnauthorizedError`
  - `TestDestRiskAsks` (lora.region with stdin `n` → aborted)
  - `TestDestSelfIsLocal` (`--dest TEST` behaves like no flag, with no `get_device_metadata_request` sent)
  - `TestDestRefusedBySend`
  - `TestDestThroughDaemon`
- [ ] **Step 2: Run** and expect FAIL.
- [ ] **Step 3: Implement**, command by command. Keep each existing local test passing unchanged.
- [ ] **Step 4: Run** `make check` and expect PASS.
- [ ] **Step 5: Commit** `feat: --dest runs admin commands on a remote node over the mesh`.

### Task 16: Documentation and hardware checklist

**Files:**
- Modify: `README.md` (a "Remote admin" section covering setup with `admin-key add`, `--dest`, timeouts, what can't be verified, the private key rule), `docs/smoke-test.md` (hardware checks: remote `config get lora`, remote set + reboot verify, unauthorised key error, traceroute both ways, telemetry request, `nodes favorite` then `nodes`), and a link to the spec in the original design doc's Non-goals ("superseded by 2026-11-20 mesh features")

- [ ] **Step 1: Write the docs.** The README examples must match the actual output from the tests in Tasks 7 and 15.
- [ ] **Step 2: Run** `make check` and expect PASS.
- [ ] **Step 3: Commit** `docs: remote admin, mesh requests and node list housekeeping`.

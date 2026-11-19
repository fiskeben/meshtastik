# meshtastik Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A single Go binary, `meshtastik`, that administers a local Meshtastic node over serial, TCP or BLE, and confirms every change by reading it back.

**Architecture:** `internal/transport` turns serial/TCP/BLE into one `Conn` of `ToRadio`/`FromRadio` messages. `internal/device` does the config handshake, keeps an in-memory `State`, and applies admin changes as an edit transaction, then verifies them via a fresh handshake (reconnecting if the node reboots). `internal/configpath`, `internal/channelurl` and `internal/backup` are pure helpers over the protobufs. `internal/cli` wires cobra commands to these. `internal/fakedevice` is an in-memory node used by the device and CLI tests.

**Tech Stack:** Go 1.27; `buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go v1.36.12-20261117212720-9364d7a3b4da.2` (import as `pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"`); `google.golang.org/protobuf v1.36.12`; `github.com/spf13/cobra v1.10.2`; `go.bug.st/serial v1.8.0`; `tinygo.org/x/bluetooth v0.17.0`; `sigs.k8s.io/yaml v1.6.0`; `github.com/mdp/qrterminal/v3 v3.2.1`. Tests use only the standard library plus `proto.Equal`.

**Spec:** `docs/superpowers/specs/2026-11-19-meshtastic-go-cli-design.md`

## Global Constraints

- Binary name `meshtastik`; module path `meshtastik` (rename when a GitHub home exists).
- Local nodes only: no admin packets addressed to any node other than the connected one.
- Connection flags `--port`, `--host`, `--ble`, mutually exclusive. TCP default port 4403.
- Stream framing: `0x94 0xC3` + big-endian uint16 length; max payload 512 bytes. Serial wake: 32 × `0xC3`, then wait 100 ms.
- BLE UUIDs: service `6ba1b218-15a8-461f-9fa8-5dcae273eafd`, ToRadio `f75c76d2-129e-4dad-a1dd-7866124401e7`, FromRadio `2c55e69e-4993-11ed-b878-0242ac120002`, FromNum `ed9da18c-a800-4f66-a670-aa7547e34453`.
- Firmware floor 2.5. Older or unparseable firmware → `ErrUnsupportedFirmware`.
- Every write is verified by reading it back. A mismatch exits non-zero and names each field that didn't take.
- `--json` on every read command; `--yes` skips confirmations; confirmations are refused (not assumed) when stdin isn't a terminal.
- Licence GPL-3.0 (the protobufs are GPL-3.0).
- No runtime dependencies. Serial/TCP must cross-compile with `CGO_ENABLED=0`. macOS builds need cgo only for BLE.
- Never write the private key anywhere except an explicit `config export`, which writes the file with mode `0600`.

## Review Focus

1. **The serial port is held by another program, or missing.** Expect a message that names the port and says it's busy/missing/permission denied, not a raw errno. → Task 3, `TestSerialOpenErrorMessages`.
2. **Ctrl-C (context cancel) between begin and commit.** Expect `ErrPartialEdit`, telling the user the node may need a reboot to discard the partial edit. → Task 7, `TestApplyPartialEditOnCancel`.
3. **Emoji and non-ASCII names.** Short names are limited to 4 **bytes**, long names to 39 bytes and channel names to 11 bytes. Expect `🦊` accepted as a short name, and `🦊🦊` rejected with the byte limit in the message. → Task 8, `TestOwnerSetByteLimits`; Task 9, `TestChannelAddNameByteLimit`.
4. **Large node database (hundreds of nodes), especially over BLE.** Expect the handshake to complete. BLE gets a longer default timeout (30 s vs 15 s). → Task 5, `TestOpenLargeNodeDB`; Task 6, `TestDefaultHandshakeTimeoutPerKind`.
5. **Odd firmware version strings** (`2.7.15.567b8ea`, `v2.6.11`, empty). Expect correct parsing or a clear "unknown firmware version" error, never a panic. → Task 5, `TestParseFirmware`.

---

## File Structure

```
cmd/meshtastik/main.go            entry point: cli.NewRoot(cli.DefaultDeps()).Execute()
internal/transport/conn.go        Conn, Kind, Dialer, ErrClosed
internal/transport/framing.go     WriteFrame, FrameReader
internal/transport/stream.go      NewStreamConn (serial + TCP share it)
internal/transport/tcp.go         DialTCP
internal/transport/serial.go      OpenSerial, DetectSerial, serial error messages
internal/transport/ble.go         DialBLE, ScanBLE, bleConn
internal/configpath/sections.go   section <-> Config/ModuleConfig oneof mapping
internal/configpath/path.go       Parse, Path
internal/configpath/value.go      SetField, FormatField, value parsing
internal/configpath/changes.go    Changes, FieldDiffs
internal/fakedevice/fake.go       in-memory node for tests
internal/device/client.go         Open, Client, State, handshake, heartbeat
internal/device/firmware.go       parseFirmware, ErrUnsupportedFirmware
internal/device/admin.go          Apply, Change, VerifyError, ErrPartialEdit, Reboot/Shutdown/FactoryReset
internal/device/guard.go          LockoutRisks
internal/device/text.go           SendText
internal/channelurl/url.go        Encode, Decode, ToChannels
internal/backup/backup.go         Export, Read, Doc.MergeConfig/MergeModuleConfig
internal/cli/root.go              NewRoot, Deps, ConnFlags, connection choice, output helpers
internal/cli/info.go              info, nodes
internal/cli/config.go            config get|set|export|import
internal/cli/channel.go           channel list|add|set|del|url
internal/cli/admin.go             owner set, reboot, shutdown, factory-reset
internal/cli/send.go              send, node resolution
internal/cli/ble.go               ble scan
docs/smoke-test.md                manual hardware checklist
```

---

### Task 1: Project scaffold and root command

**Files:**
- Create: `go.mod`, `LICENSE` (GPL-3.0 text), `README.md` (one-paragraph intro + install with `go build ./cmd/meshtastik`), `cmd/meshtastik/main.go`, `internal/cli/root.go`
- Test: `internal/cli/root_test.go`

**Interfaces:**
- Produces:
  - `type ConnFlags struct { Port, Host, BLE string }`
  - `type Deps struct { Dial func(ConnFlags) (transport.Dialer, error); Stdout, Stderr io.Writer; Confirm func(prompt string) bool }`. `transport.Dialer` arrives in Task 3. Until then, declare `Dial` as `func(ConnFlags) (any, error)` and change the type in Task 3.
  - `func NewRoot(d Deps) *cobra.Command` with persistent flags `--port`, `--host`, `--ble`, `--json`, `--yes`, `--debug`, `--timeout duration` (0 = per-transport default), and a `version` subcommand printing `meshtastik <version>` (version var set via `-ldflags`, default `dev`).
  - `func DefaultDeps() Deps`

- [ ] **Step 1: `go mod init meshtastik`, add the pinned dependencies from Tech Stack with `go get <module>@<version>`, add LICENSE and README.**
- [ ] **Step 2: Write the failing tests**

```go
func TestConnFlagsMutuallyExclusive(t *testing.T) {
	var out, errOut bytes.Buffer
	root := NewRoot(Deps{Stdout: &out, Stderr: &errOut})
	root.SetArgs([]string{"--port", "/dev/x", "--host", "1.2.3.4", "version"})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "only one of --port, --host, --ble") {
		t.Fatalf("want exclusivity error, got %v", err)
	}
}

func TestVersion(t *testing.T) {
	var out bytes.Buffer
	root := NewRoot(Deps{Stdout: &out, Stderr: io.Discard})
	root.SetArgs([]string{"version"})
	if err := root.Execute(); err != nil { t.Fatal(err) }
	if !strings.HasPrefix(out.String(), "meshtastik ") { t.Fatalf("got %q", out.String()) }
}
```

- [ ] **Step 3: Run** `go test ./internal/cli/` → FAIL (undefined `NewRoot`).
- [ ] **Step 4: Implement `NewRoot`, `DefaultDeps`, `main.go`.** Check exclusivity in `PersistentPreRunE`. Set `SilenceUsage: true` and print errors to `Stderr` once in `main`, then `os.Exit(1)`.
- [ ] **Step 5: Run** `go test ./... && go vet ./...` → PASS.
- [ ] **Step 6: Commit** `git commit -m "feat: scaffold meshtastik CLI"`

---

### Task 2: Stream framing codec

**Files:**
- Create: `internal/transport/framing.go`
- Test: `internal/transport/framing_test.go`

**Interfaces:**
- Produces: `const MaxPayload = 512`; `var ErrFrameTooLarge`; `func WriteFrame(w io.Writer, payload []byte) error`; `type FrameReader`; `func NewFrameReader(r io.Reader, log io.Writer) *FrameReader`; `func (f *FrameReader) Next() ([]byte, error)`.

- [ ] **Step 1: Write the failing tests**

```go
func frame(p string) []byte { return append([]byte{0x94, 0xC3, 0, byte(len(p))}, p...) }

func TestWriteFrame(t *testing.T) {
	var b bytes.Buffer
	if err := WriteFrame(&b, []byte("hello")); err != nil { t.Fatal(err) }
	if !bytes.Equal(b.Bytes(), frame("hello")) { t.Fatalf("got % x", b.Bytes()) }
}
func TestWriteFrameRejectsOversize(t *testing.T) {
	if err := WriteFrame(io.Discard, make([]byte, 513)); !errors.Is(err, ErrFrameTooLarge) { t.Fatal(err) }
}
func TestFrameReaderSkipsLogBytes(t *testing.T) {
	in := append([]byte("boot log\r\n"), frame("a")...)
	in = append(in, "more\n"...)
	in = append(in, frame("b")...)
	var log bytes.Buffer
	fr := NewFrameReader(bytes.NewReader(in), &log)
	for _, want := range []string{"a", "b"} {
		p, err := fr.Next()
		if err != nil || string(p) != want { t.Fatalf("got %q %v, want %q", p, err, want) }
	}
	if _, err := fr.Next(); err != io.EOF { t.Fatalf("want EOF, got %v", err) }
	if log.String() != "boot log\r\nmore\n" { t.Fatalf("log %q", log.String()) }
}
func TestFrameReaderResyncsOnOversizeLength(t *testing.T) {
	in := append([]byte{0x94, 0xC3, 0x02, 0x01}, frame("ok")...)
	p, err := NewFrameReader(bytes.NewReader(in), io.Discard).Next()
	if err != nil || string(p) != "ok" { t.Fatalf("got %q %v", p, err) }
}
func TestFrameReaderFalseStart(t *testing.T) {
	in := append([]byte{0x94, 'x'}, frame("ok")...)
	p, err := NewFrameReader(bytes.NewReader(in), io.Discard).Next()
	if err != nil || string(p) != "ok" { t.Fatalf("got %q %v", p, err) }
}
func TestFrameReaderTruncated(t *testing.T) {
	in := []byte{0x94, 0xC3, 0, 5, 'a', 'b'}
	if _, err := NewFrameReader(bytes.NewReader(in), io.Discard).Next(); err != io.ErrUnexpectedEOF { t.Fatal(err) }
}
func FuzzFrameReader(f *testing.F) {
	f.Add(frame("seed"))
	f.Fuzz(func(t *testing.T, in []byte) {
		fr := NewFrameReader(bytes.NewReader(in), io.Discard)
		for {
			p, err := fr.Next()
			if err != nil { return }
			if len(p) > MaxPayload { t.Fatalf("payload %d > max", len(p)) }
		}
	})
}
```

- [ ] **Step 2: Run** `go test ./internal/transport/` → FAIL.
- [ ] **Step 3: Implement.** This is a byte-at-a-time state machine, matching Python's `stream_interface.py`. Non-frame bytes go to `log`. When the length is > 512, drop the header bytes and resume looking for `0x94`. On a false start (byte after `0x94` isn't `0xC3`), send `0x94` to the log and re-examine the current byte as a possible `0x94`. Use a `bufio.Reader` internally.
- [ ] **Step 4: Run** `go test ./internal/transport/ && go test -fuzz=FuzzFrameReader -fuzztime=20s ./internal/transport/` → PASS.
- [ ] **Step 5: Commit** `git commit -m "feat: stream framing codec"`

---

### Task 3: Conn, stream, TCP and serial transports

**Files:**
- Create: `internal/transport/conn.go`, `stream.go`, `tcp.go`, `serial.go`
- Modify: `internal/cli/root.go` (change `Deps.Dial` to return `transport.Dialer`)
- Test: `internal/transport/stream_test.go`, `tcp_test.go`, `serial_test.go`

**Interfaces:**
- Consumes: `WriteFrame`, `NewFrameReader` (Task 2).
- Produces:
  ```go
  type Kind int
  const ( KindSerial Kind = iota; KindTCP; KindBLE )
  func (k Kind) String() string // "serial", "tcp", "ble"
  var ErrClosed = errors.New("connection closed")
  type Conn interface {
      Send(ctx context.Context, m *pb.ToRadio) error
      Recv(ctx context.Context) (*pb.FromRadio, error) // ErrClosed (wrapped) once the link is gone
      Kind() Kind
      Close() error
  }
  type Dialer func(ctx context.Context) (Conn, error)
  func NewStreamConn(rwc io.ReadWriteCloser, kind Kind, log io.Writer) Conn
  func DialTCP(ctx context.Context, addr string, log io.Writer) (Conn, error)
  func OpenSerial(ctx context.Context, path string, log io.Writer) (Conn, error)
  type SerialCandidate struct { Path, VID, PID, Product string }
  func DetectSerial() ([]SerialCandidate, error)
  ```

- [ ] **Step 1: Write the failing tests**

```go
// stream_test.go
func TestStreamConnRoundTrip(t *testing.T) {
	a, b := net.Pipe()
	c := NewStreamConn(a, KindTCP, io.Discard)
	defer c.Close()
	go func() {
		p, _ := proto.Marshal(&pb.FromRadio{PayloadVariant: &pb.FromRadio_ConfigCompleteId{ConfigCompleteId: 42}})
		b.Write([]byte("\x00\xffINFO | boot\n")) // garbage before first frame
		WriteFrame(b, p)
	}()
	m, err := c.Recv(ctxT(t))
	if err != nil || m.GetConfigCompleteId() != 42 { t.Fatalf("%v %v", m, err) }

	go c.Send(ctxT(t), &pb.ToRadio{PayloadVariant: &pb.ToRadio_WantConfigId{WantConfigId: 7}})
	p, err := NewFrameReader(b, io.Discard).Next()
	var tr pb.ToRadio
	if err != nil || proto.Unmarshal(p, &tr) != nil || tr.GetWantConfigId() != 7 { t.Fatal(err) }
}
func TestStreamConnRecvAfterPeerClose(t *testing.T) {
	a, b := net.Pipe()
	c := NewStreamConn(a, KindTCP, io.Discard)
	b.Close()
	if _, err := c.Recv(ctxT(t)); !errors.Is(err, ErrClosed) { t.Fatal(err) }
}
func TestStreamConnRecvHonoursContext(t *testing.T) {
	a, _ := net.Pipe()
	c := NewStreamConn(a, KindTCP, io.Discard)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.Recv(ctx); !errors.Is(err, context.DeadlineExceeded) { t.Fatal(err) }
}
// ctxT returns a context with a 2s timeout, cancelled at test cleanup.

// tcp_test.go
func TestTCPAddr(t *testing.T) {
	for in, want := range map[string]string{
		"192.168.1.5": "192.168.1.5:4403", "node.local:1234": "node.local:1234",
		"fe80::1": "[fe80::1]:4403", "[fe80::1]": "[fe80::1]:4403", "[fe80::1]:99": "[fe80::1]:99",
	} {
		if got := tcpAddr(in); got != want { t.Errorf("tcpAddr(%q)=%q want %q", in, got, want) }
	}
}
func TestDialTCPConnects(t *testing.T) { /* net.Listen("tcp","127.0.0.1:0"); DialTCP(ctx, ln.Addr().String(), io.Discard) succeeds; Kind()==KindTCP */ }
func TestDialTCPErrorNamesAddress(t *testing.T) { /* listen then close to get a dead port; err contains the address */ }

// serial_test.go
func TestFilterSerialCandidates(t *testing.T) {
	in := []*enumerator.PortDetails{
		{Name: "/dev/cu.usbserial-0001", IsUSB: true, VID: "10C4"},
		{Name: "/dev/tty.usbserial-0001", IsUSB: true, VID: "10C4"},
		{Name: "/dev/cu.Bluetooth-Incoming-Port"},
		{Name: "/dev/cu.usbmodem101", IsUSB: true, VID: "303a"},
		{Name: "/dev/cu.usbmodemX", IsUSB: true, VID: "05AC"},
	}
	got := filterCandidates(in)
	if len(got) != 2 || got[0].Path != "/dev/cu.usbserial-0001" || got[1].Path != "/dev/cu.usbmodem101" { t.Fatalf("%+v", got) }
}
func TestSerialOpenErrorMessages(t *testing.T) {
	cases := map[serial.PortErrorCode]string{
		serial.PortBusy: "is another program using it", serial.PortNotFound: "not found",
		serial.PermissionDenied: "permission denied",
	}
	for code, want := range cases {
		err := serialOpenError("/dev/cu.x", code, errors.New("raw"))
		if !strings.Contains(err.Error(), "/dev/cu.x") || !strings.Contains(err.Error(), want) { t.Errorf("%v", err) }
	}
}
```

- [ ] **Step 2: Run** `go test ./internal/transport/` → FAIL.
- [ ] **Step 3: Implement.**
  - `NewStreamConn`: one reader goroutine runs `FrameReader.Next` → unmarshal → buffered channel. A read error closes the channel, and `Recv` then returns `fmt.Errorf("%w: %v", ErrClosed, cause)`. `Send` marshals and calls `WriteFrame` under a mutex. Undecodable frames are logged and skipped.
  - `DialTCP`: `net.Dialer.DialContext` to `tcpAddr(addr)`.
  - `OpenSerial`: open at 115200 8N1 with `InitialStatusBits{DTR: true, RTS: true}` (matches pyserial, so ESP32 auto-reset doesn't trigger). Write 32 × `0xC3`, sleep 100 ms, wrap in `NewStreamConn`. Map `*serial.PortError` codes through `serialOpenError`.
  - `filterCandidates`: USB ports whose VID (case-insensitive) is in {`10C4`, `1A86`, `0403`, `303A`, `239A`, `2886`, `2E8A`}. On duplicates differing only by `/dev/tty.` vs `/dev/cu.`, keep `cu`. Keep enumerator order.
- [ ] **Step 4: Run** `go test ./internal/transport/` → PASS.
- [ ] **Step 5: Commit** `git commit -m "feat: serial and TCP transports"`

---

### Task 4: Config paths and section mapping

**Files:**
- Create: `internal/configpath/sections.go`, `path.go`, `value.go`, `changes.go`
- Test: `internal/configpath/configpath_test.go`

**Interfaces:**
- Produces:
  ```go
  // Sections are the message-typed fields of LocalConfig (Module=false) and
  // LocalModuleConfig (Module=true); each matches a same-named oneof field in
  // Config / ModuleConfig. The scalar `version` field is not a section.
  func Sections() []string                                   // sorted, both kinds
  func MergeConfig(dst *pb.LocalConfig, c *pb.Config)        // set the section c carries
  func MergeModuleConfig(dst *pb.LocalModuleConfig, c *pb.ModuleConfig)
  func ConfigSections(l *pb.LocalConfig) []*pb.Config        // one per populated section
  func ModuleSections(l *pb.LocalModuleConfig) []*pb.ModuleConfig

  type Path struct { Module bool; Section, Field string }   // Field may be dotted ("ipv4_config.ip") or ""
  func (p Path) String() string
  func Parse(s string) (Path, error)                         // accepts snake_case or camelCase field names
  func Section(cfg *pb.LocalConfig, mod *pb.LocalModuleConfig, p Path) protoreflect.Message // allocates if nil
  func Set(cfg *pb.LocalConfig, mod *pb.LocalModuleConfig, p Path, raw string) error         // mutates
  func SetField(m protoreflect.Message, field, raw string) error   // generic; also used for ChannelSettings
  func FormatField(m protoreflect.Message, field string) (string, error)
  func Changes(beforeCfg, afterCfg *pb.LocalConfig, beforeMod, afterMod *pb.LocalModuleConfig) ([]*pb.Config, []*pb.ModuleConfig)
  type FieldDiff struct { Path, Want, Got string }
  func FieldDiffs(prefix string, want, got protoreflect.Message) []FieldDiff  // leaf-level, recursive, formatted via FormatField rules
  ```

- [ ] **Step 1: Write the failing tests**

```go
func TestSectionsMatchOneofs(t *testing.T) {
	check := func(local, wrapper protoreflect.MessageDescriptor) {
		for i := 0; i < local.Fields().Len(); i++ {
			f := local.Fields().Get(i)
			if f.Kind() != protoreflect.MessageKind { continue }
			w := wrapper.Fields().ByName(f.Name())
			if w == nil || w.ContainingOneof() == nil || w.Message().FullName() != f.Message().FullName() {
				t.Errorf("%s.%s has no matching oneof in %s", local.Name(), f.Name(), wrapper.Name())
			}
		}
	}
	check((&pb.LocalConfig{}).ProtoReflect().Descriptor(), (&pb.Config{}).ProtoReflect().Descriptor())
	check((&pb.LocalModuleConfig{}).ProtoReflect().Descriptor(), (&pb.ModuleConfig{}).ProtoReflect().Descriptor())
	s := Sections()
	for _, want := range []string{"device", "lora", "bluetooth", "security", "mqtt", "serial", "telemetry", "neighbor_info"} {
		if !slices.Contains(s, want) { t.Errorf("missing section %s", want) }
	}
	if len(s) != len(slices.Compact(slices.Clone(s))) { t.Error("config and module section names collide") }
}
func TestParse(t *testing.T) {
	ok := map[string]Path{
		"lora.region": {Section: "lora", Field: "region"}, "lora.hopLimit": {Section: "lora", Field: "hop_limit"},
		"mqtt.enabled": {Module: true, Section: "mqtt", Field: "enabled"}, "lora": {Section: "lora"},
		"network.ipv4_config.ip": {Section: "network", Field: "ipv4_config.ip"},
	}
	for in, want := range ok { if got, err := Parse(in); err != nil || got != want { t.Errorf("%s: %+v %v", in, got, err) } }
	for in, hint := range map[string]string{"lroa.region": "lora", "lora.regoin": "region", "lora.region.x": "region"} {
		if _, err := Parse(in); err == nil || !strings.Contains(err.Error(), hint) { t.Errorf("%s: %v", in, err) }
	}
}
func TestSetValues(t *testing.T) {
	cfg, mod := &pb.LocalConfig{}, &pb.LocalModuleConfig{}
	set := func(path, raw string) error { p, err := Parse(path); if err != nil { return err }; return Set(cfg, mod, p, raw) }
	must := func(err error) { t.Helper(); if err != nil { t.Fatal(err) } }
	must(set("lora.region", "eu_868"))
	if cfg.Lora.Region != pb.Config_LoRaConfig_EU_868 { t.Fatal("enum") }
	for _, v := range []string{"false", "no", "off", "0"} { must(set("bluetooth.enabled", "true")); must(set("bluetooth.enabled", v)); if cfg.Bluetooth.Enabled { t.Fatal(v) } }
	must(set("security.admin_key", "base64:AAEC,base64:AwQF"))
	if len(cfg.Security.AdminKey) != 2 || !bytes.Equal(cfg.Security.AdminKey[1], []byte{3, 4, 5}) { t.Fatal("bytes list") }
	must(set("mqtt.enabled", "yes"))
	if !mod.Mqtt.Enabled { t.Fatal("module") }
	for path, raw := range map[string]string{"lora.region": "mars", "bluetooth.enabled": "maybe", "lora.hop_limit": "-1", "lora": "x"} {
		if err := set(path, raw); err == nil { t.Errorf("%s=%s accepted", path, raw) }
	}
	if err := set("lora.hop_limit", "4294967296"); err == nil { t.Error("uint32 overflow accepted") }
	if err := set("lora.region", "mars"); !strings.Contains(err.Error(), "EU_868") { t.Errorf("no options listed: %v", err) }
}
func TestFormatField(t *testing.T) {
	cfg := &pb.LocalConfig{Lora: &pb.Config_LoRaConfig{Region: pb.Config_LoRaConfig_EU_868, TxEnabled: true}}
	m := cfg.Lora.ProtoReflect()
	for f, want := range map[string]string{"region": "EU_868", "tx_enabled": "true", "hop_limit": "0"} {
		if got, _ := FormatField(m, f); got != want { t.Errorf("%s=%q", f, got) }
	}
}
func TestSectionRoundTrip(t *testing.T) {
	l := &pb.LocalConfig{Lora: &pb.Config_LoRaConfig{HopLimit: 3}, Bluetooth: &pb.Config_BluetoothConfig{Enabled: true}}
	got := &pb.LocalConfig{}
	for _, c := range ConfigSections(l) { MergeConfig(got, c) }
	if !proto.Equal(l, got) { t.Fatal(got) }
}
func TestChangesOnlyChangedSections(t *testing.T) {
	before := &pb.LocalConfig{Lora: &pb.Config_LoRaConfig{HopLimit: 3}, Device: &pb.Config_DeviceConfig{}}
	after := proto.Clone(before).(*pb.LocalConfig)
	after.Lora.HopLimit = 5
	cfgs, mods := Changes(before, after, &pb.LocalModuleConfig{}, &pb.LocalModuleConfig{})
	if len(cfgs) != 1 || cfgs[0].GetLora().GetHopLimit() != 5 || len(mods) != 0 { t.Fatal(cfgs, mods) }
}
func TestFieldDiffs(t *testing.T) {
	want := (&pb.Config_LoRaConfig{HopLimit: 5}).ProtoReflect()
	got := (&pb.Config_LoRaConfig{HopLimit: 3}).ProtoReflect()
	d := FieldDiffs("lora", want, got)
	if len(d) != 1 || d[0] != (FieldDiff{"lora.hop_limit", "5", "3"}) { t.Fatal(d) }
}
```

- [ ] **Step 2: Run** `go test ./internal/configpath/` → FAIL.
- [ ] **Step 3: Implement** using `protoreflect` only (no per-section switch statements).
  - Value syntax:
    - enums: name, case-insensitive
    - bools: `true|false|yes|no|on|off|1|0`
    - integers: `strconv.ParseInt`/`ParseUint` with the field's bit size
    - floats
    - strings
    - bytes: `base64:<std base64>`
    - repeated: comma-separated, replacing the whole list
  - Errors for an unknown section, field or enum value list the valid options.
  - Format output mirrors the input syntax: repeated fields comma-joined, bytes as `base64:`.
- [ ] **Step 4: Run** `go test ./internal/configpath/` → PASS.
- [ ] **Step 5: Commit** `git commit -m "feat: config path parsing and reflection-based get/set"`

---

### Task 5: Fake device and handshake

**Files:**
- Create: `internal/fakedevice/fake.go`, `internal/device/client.go`, `internal/device/firmware.go`
- Test: `internal/device/client_test.go`, `internal/device/firmware_test.go`

**Interfaces:**
- Consumes: `transport.Conn`, `transport.Dialer`, `transport.Kind` (Task 3); `configpath.ConfigSections`, `ModuleSections`, `MergeConfig`, `MergeModuleConfig` (Task 4).
- Produces:
  ```go
  // fakedevice
  type Device struct {
      NodeNum         uint32           // default 0x1234abcd
      FirmwareVersion string           // default "2.7.15.567b8ea"
      Kind            transport.Kind   // default KindSerial
      Owner           *pb.User         // default LongName "Test Node", ShortName "TEST", HwModel HELTEC_V3
      Config          *pb.LocalConfig  // default: every section non-nil; Lora{Region: EU_868, HopLimit: 3, TxEnabled: true, UsePreset: true}, Bluetooth{Enabled: true}, Security{SerialEnabled: true}, Network{}
      ModuleConfig    *pb.LocalModuleConfig // every section non-nil
      Channels        [8]*pb.Channel   // [0] PRIMARY, Settings{Psk: []byte{1}}; rest DISABLED; Index set on all
      ExtraNodes      []*pb.NodeInfo
      Silent          bool             // never answer want_config
      StaleCompleteID bool             // send one config_complete_id with a wrong nonce first
      // knobs added in Task 7 and 12 live here too
  }
  func New() *Device
  func (d *Device) Dialer() transport.Dialer   // each call returns a fresh in-memory Conn; counts dials
  func (d *Device) Dials() int
  func (d *Device) Heartbeats() int

  // device
  type State struct {
      MyInfo       *pb.MyNodeInfo
      Metadata     *pb.DeviceMetadata
      Nodes        map[uint32]*pb.NodeInfo
      Config       *pb.LocalConfig
      ModuleConfig *pb.LocalModuleConfig
      Channels     [8]*pb.Channel
  }
  func (s *State) Self() *pb.NodeInfo
  type Options struct {
      HandshakeTimeout  time.Duration // default 15s
      ReconnectTimeout  time.Duration // default 90s
      HeartbeatInterval time.Duration // default 5m
      Log               io.Writer     // default io.Discard
  }
  func Open(ctx context.Context, dial transport.Dialer, opts Options) (*Client, error)
  func (c *Client) State() *State
  func (c *Client) Kind() transport.Kind
  func (c *Client) Close() error
  var ErrUnsupportedFirmware = errors.New("unsupported firmware")
  func parseFirmware(v string) (major, minor int, ok bool)
  ```

- [ ] **Step 1: Write the failing tests**

```go
func open(t *testing.T, d *fakedevice.Device, o device.Options) (*device.Client, error) {
	t.Helper()
	c, err := device.Open(ctxT(t), d.Dialer(), o)
	if err == nil { t.Cleanup(func() { c.Close() }) }
	return c, err
}
func TestOpenHandshake(t *testing.T) {
	d := fakedevice.New()
	c, err := open(t, d, device.Options{})
	if err != nil { t.Fatal(err) }
	s := c.State()
	if s.MyInfo.MyNodeNum != d.NodeNum || !proto.Equal(s.Config, d.Config) || !proto.Equal(s.ModuleConfig, d.ModuleConfig) { t.Fatal("state mismatch") }
	if s.Channels[0].Role != pb.Channel_PRIMARY || s.Self().User.LongName != "Test Node" { t.Fatal(s.Self()) }
}
func TestOpenIgnoresStaleConfigComplete(t *testing.T) {
	d := fakedevice.New(); d.StaleCompleteID = true
	c, err := open(t, d, device.Options{})
	if err != nil || c.State().Config.Lora == nil { t.Fatal(err) }
}
func TestOpenUnsupportedFirmware(t *testing.T) {
	d := fakedevice.New(); d.FirmwareVersion = "2.3.2.abc1234"
	_, err := open(t, d, device.Options{})
	if !errors.Is(err, device.ErrUnsupportedFirmware) || !strings.Contains(err.Error(), "2.3.2") { t.Fatal(err) }
}
func TestOpenHandshakeTimeout(t *testing.T) {
	d := fakedevice.New(); d.Silent = true
	_, err := open(t, d, device.Options{HandshakeTimeout: 100 * time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "no response") { t.Fatal(err) }
}
func TestOpenLargeNodeDB(t *testing.T) {
	d := fakedevice.New()
	for i := range 300 { d.ExtraNodes = append(d.ExtraNodes, &pb.NodeInfo{Num: uint32(i + 1), User: &pb.User{LongName: fmt.Sprint("n", i)}}) }
	c, err := open(t, d, device.Options{})
	if err != nil || len(c.State().Nodes) != 301 { t.Fatal(err) }
}
func TestHeartbeat(t *testing.T) {
	d := fakedevice.New()
	if _, err := open(t, d, device.Options{HeartbeatInterval: 20 * time.Millisecond}); err != nil { t.Fatal(err) }
	time.Sleep(80 * time.Millisecond)
	if d.Heartbeats() < 2 { t.Fatal(d.Heartbeats()) }
}

// firmware_test.go (package device)
func TestParseFirmware(t *testing.T) {
	cases := []struct{ in string; maj, min int; ok bool }{
		{"2.7.15.567b8ea", 2, 7, true}, {"2.5.0", 2, 5, true}, {"v2.6.11", 2, 6, true},
		{"", 0, 0, false}, {"garbage", 0, 0, false}, {"2", 0, 0, false},
	}
	for _, c := range cases {
		maj, min, ok := parseFirmware(c.in)
		if maj != c.maj || min != c.min || ok != c.ok { t.Errorf("%q -> %d %d %v", c.in, maj, min, ok) }
	}
}
```

Every test package that needs it defines its own `ctxT(t)` (2s timeout, cancelled at cleanup), as in Task 3.

- [ ] **Step 2: Run** `go test ./internal/device/` → FAIL.
- [ ] **Step 3: Implement the fake.**
  - Each `Dialer()` call creates a pipe-like in-memory `Conn` (two channels).
  - The fake goroutine answers `want_config_id` in firmware order: `my_info`, `metadata` (FirmwareVersion), `node_info` for self (Owner) then ExtraNodes, one `config` per `ConfigSections`, one `moduleConfig` per `ModuleSections`, eight `channel`, then `config_complete_id`.
  - It counts `heartbeat` messages. Protect fields with a mutex, since tests read them while the fake runs.
- [ ] **Step 4: Implement `Open`.**
  - Exactly one goroutine reads `conn.Recv` and dispatches the messages. Task 7 adds response waiters and reboot signals to this same goroutine.
  - Use a random non-zero nonce. Ignore a `config_complete_id` that doesn't match it.
  - On timeout, return `fmt.Errorf("no response to handshake after %s on %s — is another program connected to the node?", timeout, kind)`.
  - After the handshake, check the firmware: below 2.5 or unparseable → `fmt.Errorf("%w: %s (need 2.5 or newer)", ErrUnsupportedFirmware, v)`, using "unknown firmware version" when empty.
  - Later `node_info` and `packet` messages for known nodes update `State.Nodes`, under a mutex.
  - `State()` returns a deep copy.
- [ ] **Step 5: Run** `go test -race ./internal/device/ ./internal/fakedevice/` → PASS.
- [ ] **Step 6: Commit** `git commit -m "feat: device handshake with fake device for tests"`

---

### Task 6: Connection choice, `info` and `nodes`

**Files:**
- Modify: `internal/cli/root.go` (connection choice, `connect` helper, output helpers)
- Create: `internal/cli/info.go`
- Test: `internal/cli/cli_test.go` (shared helper), `internal/cli/info_test.go`, `internal/cli/root_test.go`

**Interfaces:**
- Consumes: `device.Open`, `device.Options`, `State` (Task 5); `transport.DialTCP`, `OpenSerial`, `DetectSerial` (Task 3).
- Produces:
  - `func chooseConn(f ConnFlags, detect func() ([]transport.SerialCandidate, error)) (transport.Kind, string, error)`
  - `func defaultHandshakeTimeout(k transport.Kind) time.Duration`: 15s serial/TCP, 30s BLE
  - `func (a *app) connect(cmd *cobra.Command) (*device.Client, error)`: the one place commands get a client. It honours `--timeout` and `--debug` (device log to Stderr).
  - `func printProto(w io.Writer, m proto.Message, asJSON bool) error`: YAML (protojson → `yaml.JSONToYAML`) or indented JSON
  - `func printJSON(w io.Writer, v any) error`
  - Test helper `runCLI(t *testing.T, d *fakedevice.Device, stdin string, args ...string) (stdout, stderr string, err error)`. `Deps.Confirm` returns `strings.HasPrefix(stdin, "y")`.
  - `info` JSON object keys: `id` (`!%08x`), `num`, `long_name`, `short_name`, `hw_model`, `firmware`, `region`, `role`, `modem_preset`, `battery_level`, `channels` (count of non-disabled)
  - `nodes` JSON: array of `{id, num, long_name, short_name, last_heard (RFC 3339 or null), snr, hops_away, battery_level}`

- [ ] **Step 1: Write the failing tests**

```go
func TestChooseConn(t *testing.T) {
	one := func() ([]transport.SerialCandidate, error) { return []transport.SerialCandidate{{Path: "/dev/cu.a"}}, nil }
	two := func() ([]transport.SerialCandidate, error) { return []transport.SerialCandidate{{Path: "/dev/cu.a"}, {Path: "/dev/cu.b"}}, nil }
	none := func() ([]transport.SerialCandidate, error) { return nil, nil }
	if k, p, err := chooseConn(ConnFlags{}, one); err != nil || k != transport.KindSerial || p != "/dev/cu.a" { t.Fatal(k, p, err) }
	if k, p, _ := chooseConn(ConnFlags{Host: "10.0.0.2"}, none); k != transport.KindTCP || p != "10.0.0.2" { t.Fatal(k, p) }
	if _, _, err := chooseConn(ConnFlags{}, none); err == nil || !strings.Contains(err.Error(), "no Meshtastic device found") { t.Fatal(err) }
	if _, _, err := chooseConn(ConnFlags{}, two); err == nil || !strings.Contains(err.Error(), "/dev/cu.b") { t.Fatal(err) }
}
func TestDefaultHandshakeTimeoutPerKind(t *testing.T) {
	if defaultHandshakeTimeout(transport.KindBLE) != 30*time.Second || defaultHandshakeTimeout(transport.KindSerial) != 15*time.Second { t.Fatal() }
}
func TestInfoHuman(t *testing.T) {
	out, _, err := runCLI(t, fakedevice.New(), "", "info")
	if err != nil { t.Fatal(err) }
	for _, want := range []string{"Test Node", "TEST", "!1234abcd", "2.7.15", "EU_868"} {
		if !strings.Contains(out, want) { t.Errorf("missing %q in\n%s", want, out) }
	}
}
func TestInfoJSON(t *testing.T) {
	out, _, _ := runCLI(t, fakedevice.New(), "", "info", "--json")
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil || m["id"] != "!1234abcd" || m["region"] != "EU_868" { t.Fatal(out, err) }
}
func TestNodesSortedByLastHeard(t *testing.T) {
	d := fakedevice.New()
	d.ExtraNodes = []*pb.NodeInfo{
		{Num: 1, LastHeard: 100, User: &pb.User{LongName: "old"}},
		{Num: 2, LastHeard: 200, User: &pb.User{LongName: "new"}},
	}
	out, _, _ := runCLI(t, d, "", "nodes")
	if strings.Index(out, "new") > strings.Index(out, "old") { t.Fatal(out) }
	out, _, _ = runCLI(t, d, "", "nodes", "--json")
	var arr []map[string]any
	if json.Unmarshal([]byte(out), &arr) != nil || len(arr) != 3 { t.Fatal(out) }
}
```

- [ ] **Step 2: Run** `go test ./internal/cli/` → FAIL.
- [ ] **Step 3: Implement.** `DefaultDeps().Dial` uses `chooseConn` with `transport.DetectSerial` and returns a `transport.Dialer` closure that re-opens the same target, so reconnects work. `--ble` returns "BLE not available yet" until Task 11. Human output uses `text/tabwriter`.
- [ ] **Step 4: Run** `go test ./internal/cli/` → PASS.
- [ ] **Step 5: Commit** `git commit -m "feat: info and nodes commands"`

---

### Task 7: Apply changes with verification

**Files:**
- Create: `internal/device/admin.go`
- Modify: `internal/device/client.go` (response waiters, reboot signal), `internal/fakedevice/fake.go` (admin handling + knobs)
- Test: `internal/device/admin_test.go`

**Interfaces:**
- Consumes: State, Open (Task 5); `configpath.FieldDiffs`, `MergeConfig`, `MergeModuleConfig` (Task 4).
- Produces:
  ```go
  type Change struct {           // exactly one field set
      Config        *pb.Config
      ModuleConfig  *pb.ModuleConfig
      Channel       *pb.Channel
      Owner         *pb.User
      FixedPosition *pb.Position
  }
  func (c *Client) Apply(ctx context.Context, changes []Change) (applied int, err error)
  type Mismatch = configpath.FieldDiff
  type VerifyError struct{ Mismatches []Mismatch }   // Error(): "not applied by the node:\n  lora.hop_limit: want 5, got 3"
  var ErrPartialEdit = errors.New("edit interrupted before commit; the node may hold a partial edit — reboot it to discard")
  func (c *Client) AdminRequest(ctx context.Context, m *pb.AdminMessage) (*pb.AdminMessage, error) // waits for response
  func (c *Client) Reboot(ctx context.Context, seconds int32) error
  func (c *Client) Shutdown(ctx context.Context, seconds int32) error
  func (c *Client) FactoryReset(ctx context.Context, full bool) error   // full → factory_reset_device, else factory_reset_config
  ```
  Fake knobs: `RequirePasskey bool`; `IgnoreFields []string` (config paths whose new value is silently dropped on commit); `RebootOnCommit bool`; `RefuseDialsAfterReboot bool`; `DropAfter func(*pb.AdminMessage) bool`; `OnAdmin func(*pb.AdminMessage)`; `Received() []*pb.AdminMessage`.
  Test helpers on the fake, used by device and CLI tests: `fakedevice.CountBegin(ms []*pb.AdminMessage) int`, `fakedevice.HasCommit(ms) bool`, and `(d *Device) LastAdmin() *pb.AdminMessage`. In the tests below, `hasBegin(r)` means `fakedevice.CountBegin(r) > 0`, `countBegin` means `fakedevice.CountBegin`, `hasCommit` means `fakedevice.HasCommit`, and `lastAdmin(d)` means `d.LastAdmin()`.

- [ ] **Step 1: Write the failing tests**

```go
func lora(hop uint32) device.Change {
	return device.Change{Config: &pb.Config{PayloadVariant: &pb.Config_Lora{Lora: &pb.Config_LoRaConfig{Region: pb.Config_LoRaConfig_EU_868, HopLimit: hop, TxEnabled: true, UsePreset: true}}}}
}
func TestApplySetsAndVerifies(t *testing.T) {
	d := fakedevice.New(); c, _ := open(t, d, device.Options{})
	n, err := c.Apply(ctxT(t), []device.Change{lora(5)})
	if err != nil || n != 1 || c.State().Config.Lora.HopLimit != 5 { t.Fatal(n, err) }
	r := d.Received()
	if !hasBegin(r) || !hasCommit(r) { t.Fatal("no edit transaction") }
}
func TestApplyUsesSessionPasskey(t *testing.T) {
	d := fakedevice.New(); d.RequirePasskey = true; c, _ := open(t, d, device.Options{})
	if _, err := c.Apply(ctxT(t), []device.Change{lora(5)}); err != nil { t.Fatal(err) }
	for _, m := range d.Received() { if m.GetSetConfig() != nil && len(m.SessionPasskey) == 0 { t.Fatal("set without passkey") } }
}
func TestApplyReportsMismatch(t *testing.T) {
	d := fakedevice.New(); d.IgnoreFields = []string{"lora.hop_limit"}; c, _ := open(t, d, device.Options{})
	_, err := c.Apply(ctxT(t), []device.Change{lora(5)})
	var ve *device.VerifyError
	if !errors.As(err, &ve) || len(ve.Mismatches) != 1 || ve.Mismatches[0] != (device.Mismatch{Path: "lora.hop_limit", Want: "5", Got: "3"}) { t.Fatal(err) }
}
func TestApplyNoChangesSkips(t *testing.T) {
	d := fakedevice.New(); c, _ := open(t, d, device.Options{})
	n, err := c.Apply(ctxT(t), []device.Change{lora(3)})
	if err != nil || n != 0 || hasBegin(d.Received()) { t.Fatal(n, err) }
}
func TestApplyReconnectsAfterReboot(t *testing.T) {
	d := fakedevice.New(); d.RebootOnCommit = true; c, _ := open(t, d, device.Options{})
	if _, err := c.Apply(ctxT(t), []device.Change{lora(5)}); err != nil || d.Dials() != 2 { t.Fatal(err, d.Dials()) }
}
func TestApplyReconnectTimeout(t *testing.T) {
	d := fakedevice.New(); d.RebootOnCommit = true; d.RefuseDialsAfterReboot = true
	c, _ := open(t, d, device.Options{ReconnectTimeout: 300 * time.Millisecond})
	if _, err := c.Apply(ctxT(t), []device.Change{lora(5)}); err == nil || !strings.Contains(err.Error(), "did not come back") { t.Fatal(err) }
}
func TestApplyPartialEditOnDrop(t *testing.T) {
	d := fakedevice.New(); d.DropAfter = func(m *pb.AdminMessage) bool { return m.GetSetConfig() != nil }
	c, _ := open(t, d, device.Options{})
	bt := device.Change{Config: &pb.Config{PayloadVariant: &pb.Config_Bluetooth{Bluetooth: &pb.Config_BluetoothConfig{Enabled: false}}}}
	_, err := c.Apply(ctxT(t), []device.Change{lora(5), bt})
	if !errors.Is(err, device.ErrPartialEdit) { t.Fatal(err) }
}
func TestApplyPartialEditOnCancel(t *testing.T) {
	d := fakedevice.New(); c, _ := open(t, d, device.Options{})
	ctx, cancel := context.WithCancel(ctxT(t))
	d.OnAdmin = func(m *pb.AdminMessage) { if m.GetBeginEditSettings() { cancel(); time.Sleep(20 * time.Millisecond) } }
	if _, err := c.Apply(ctx, []device.Change{lora(5)}); !errors.Is(err, device.ErrPartialEdit) { t.Fatal(err) }
}
func TestApplyOwnerAndChannel(t *testing.T) {
	d := fakedevice.New(); c, _ := open(t, d, device.Options{})
	ch := &pb.Channel{Index: 1, Role: pb.Channel_SECONDARY, Settings: &pb.ChannelSettings{Name: "admin", Psk: bytes.Repeat([]byte{7}, 32)}}
	_, err := c.Apply(ctxT(t), []device.Change{{Owner: &pb.User{LongName: "Base", ShortName: "BASE"}}, {Channel: ch}})
	if err != nil || c.State().Self().User.ShortName != "BASE" || c.State().Channels[1].Settings.Name != "admin" { t.Fatal(err) }
}
func TestFactoryResetSendsRightField(t *testing.T) { /* FactoryReset(ctx,false) → Received has FactoryResetConfig==1; true → FactoryResetDevice==1 */ }
```

- [ ] **Step 2: Run** `go test ./internal/device/` → FAIL.
- [ ] **Step 3: Extend the fake.**
  - It handles `get_config_request` (including `SESSIONKEY_CONFIG`) and `get_channel_request` (index+1) with responses carrying `request_id` and `session_passkey`.
  - Sets are buffered between begin and commit, and applied on commit, minus any `IgnoreFields`.
  - Outside a transaction, sets apply immediately.
  - `RebootOnCommit` sends `FromRadio{rebooted: true}`, then closes the conn.
  - `set_fixed_position` sets `Config.Position.FixedPosition = true`.
  - Reboot, shutdown and factory reset are recorded only.
- [ ] **Step 4: Implement `Apply`.**
  1. Drop any change equal to the current state (`proto.Equal` on the section, channel, or owner long/short name). Return `0, nil` if nothing is left.
  2. If no passkey is held yet, `AdminRequest(get_config_request: SESSIONKEY_CONFIG)` and store the `session_passkey`. Refresh it from every admin response, and set it on every outgoing admin message.
  3. Send `begin_edit_settings`, one `set_*` per change, then `commit_edit_settings`. Each admin message is a `MeshPacket{To: myNodeNum, Id: random, Decoded: Data{Portnum: ADMIN_APP, Payload, WantResponse: <get only>}}`. Any error or ctx cancel after begin and before commit → `fmt.Errorf("%w: %v", ErrPartialEdit, cause)`.
  4. Wait 3s (`rebootGrace`) for `rebooted` or a closed connection. If either happens, redial with backoff from 500ms doubling to a 5s cap, until `ReconnectTimeout`. Otherwise "node did not come back after reboot within %s". Then re-run the handshake on the new conn. If there's no reboot, re-run the handshake on the same conn.
  5. Compare each change against the fresh State using `configpath.FieldDiffs` (sections and channel settings), long/short name (owner), or `position.fixed_position == true` (fixed position). Return `*VerifyError` on any diff.
  - `Reboot`, `Shutdown` and `FactoryReset` send a single admin message with the passkey, and don't verify.
- [ ] **Step 5: Run** `go test -race ./internal/device/` → PASS.
- [ ] **Step 6: Commit** `git commit -m "feat: apply admin changes in an edit transaction and verify by read-back"`

---

### Task 8: Lock-out guard, `config get|set`, `owner set`, power commands

**Files:**
- Create: `internal/device/guard.go`, `internal/cli/config.go`, `internal/cli/admin.go`
- Test: `internal/device/guard_test.go`, `internal/cli/config_test.go`, `internal/cli/admin_test.go`

**Interfaces:**
- Consumes: Apply, Change, Reboot/Shutdown/FactoryReset (Task 7); configpath (Task 4); `connect`, `printProto` (Task 6).
- Produces: `func LockoutRisks(kind transport.Kind, before *State, changes []Change) []string` (human sentences); `func confirmOrAbort(a *app, prompt string) error`. With `--yes` it returns nil; otherwise it returns nil only if `Deps.Confirm(prompt)` is true. The default `Confirm` returns false with "not a terminal; pass --yes" when stdin isn't a TTY.
- Commands:
  - `config get [path]`: no path → whole config + module config. A section prints YAML (JSON with `--json`). A single field prints just the value plus a newline (`{"path":..,"value":..}` with `--json`).
  - `config set <path> <value> [<path> <value>...]`
  - `owner set <long> [--short X]`: keeps the current short name if `--short` is omitted.
  - `reboot`, `shutdown`: 5 seconds delay.
  - `factory-reset [--full]`
  - Prompts are needed for: lock-out risks, `shutdown`, `factory-reset`.

- [ ] **Step 1: Write the failing tests**

```go
// guard_test.go
func TestLockoutRisks(t *testing.T) {
	st := &device.State{Config: &pb.LocalConfig{
		Bluetooth: &pb.Config_BluetoothConfig{Enabled: true}, Security: &pb.Config_SecurityConfig{SerialEnabled: true},
		Network: &pb.Config_NetworkConfig{WifiEnabled: true}}}
	cfg := func(c *pb.Config) []device.Change { return []device.Change{{Config: c}} }
	btOff := cfg(&pb.Config{PayloadVariant: &pb.Config_Bluetooth{Bluetooth: &pb.Config_BluetoothConfig{}}})
	serialOff := cfg(&pb.Config{PayloadVariant: &pb.Config_Security{Security: &pb.Config_SecurityConfig{}}})
	wifiOff := cfg(&pb.Config{PayloadVariant: &pb.Config_Network{Network: &pb.Config_NetworkConfig{}}})
	wifiOffEthOn := cfg(&pb.Config{PayloadVariant: &pb.Config_Network{Network: &pb.Config_NetworkConfig{EthEnabled: true}}})
	managed := cfg(&pb.Config{PayloadVariant: &pb.Config_Security{Security: &pb.Config_SecurityConfig{SerialEnabled: true, IsManaged: true}}})
	cases := []struct { k transport.Kind; ch []device.Change; risky bool }{
		{transport.KindBLE, btOff, true}, {transport.KindSerial, btOff, false},
		{transport.KindSerial, serialOff, true}, {transport.KindTCP, wifiOff, true},
		{transport.KindTCP, wifiOffEthOn, false}, {transport.KindSerial, managed, true},
	}
	for i, c := range cases { if got := len(device.LockoutRisks(c.k, st, c.ch)) > 0; got != c.risky { t.Errorf("case %d: risky=%v", i, got) } }
}

// config_test.go
func TestConfigGet(t *testing.T) {
	d := fakedevice.New()
	if out, _, _ := runCLI(t, d, "", "config", "get", "lora.region"); out != "EU_868\n" { t.Fatalf("%q", out) }
	if out, _, _ := runCLI(t, d, "", "config", "get", "lora"); !strings.Contains(out, "region: EU_868") { t.Fatal(out) }
	out, _, _ := runCLI(t, d, "", "config", "get", "--json")
	var m map[string]any
	if json.Unmarshal([]byte(out), &m) != nil || m["config"] == nil || m["module_config"] == nil { t.Fatal(out) }
}
func TestConfigSetMultiplePairs(t *testing.T) {
	d := fakedevice.New()
	if _, _, err := runCLI(t, d, "", "config", "set", "lora.hop_limit", "5", "lora.tx_power", "20"); err != nil { t.Fatal(err) }
	if d.Config.Lora.HopLimit != 5 || d.Config.Lora.TxPower != 20 || countBegin(d.Received()) != 1 { t.Fatal() }
}
func TestConfigSetOddArgs(t *testing.T) {
	if _, _, err := runCLI(t, fakedevice.New(), "", "config", "set", "lora.hop_limit"); err == nil { t.Fatal() }
}
func TestConfigSetLockoutNeedsConfirm(t *testing.T) {
	d := fakedevice.New(); d.Kind = transport.KindBLE
	if _, _, err := runCLI(t, d, "n\n", "config", "set", "bluetooth.enabled", "false"); err == nil || !d.Config.Bluetooth.Enabled { t.Fatal("applied without confirm") }
	if _, _, err := runCLI(t, d, "", "--yes", "config", "set", "bluetooth.enabled", "false"); err != nil || d.Config.Bluetooth.Enabled { t.Fatal(err) }
}
func TestConfigSetMismatchFails(t *testing.T) {
	d := fakedevice.New(); d.IgnoreFields = []string{"lora.hop_limit"}
	_, _, err := runCLI(t, d, "", "config", "set", "lora.hop_limit", "5")
	if err == nil || !strings.Contains(err.Error(), "lora.hop_limit") { t.Fatal(err) }
}

// admin_test.go
func TestOwnerSet(t *testing.T) {
	d := fakedevice.New()
	if _, _, err := runCLI(t, d, "", "owner", "set", "Base Station", "--short", "BASE"); err != nil || d.Owner.ShortName != "BASE" { t.Fatal(err) }
}
func TestOwnerSetByteLimits(t *testing.T) {
	d := fakedevice.New()
	if _, _, err := runCLI(t, d, "", "owner", "set", "Fox", "--short", "🦊"); err != nil { t.Fatal(err) }
	if _, _, err := runCLI(t, d, "", "owner", "set", "Fox", "--short", "🦊🦊"); err == nil || !strings.Contains(err.Error(), "4 bytes") { t.Fatal(err) }
	if _, _, err := runCLI(t, d, "", "owner", "set", strings.Repeat("x", 40)); err == nil || !strings.Contains(err.Error(), "39 bytes") { t.Fatal(err) }
}
func TestReboot(t *testing.T) {
	d := fakedevice.New()
	out, _, err := runCLI(t, d, "", "reboot")
	if err != nil || !strings.Contains(out, "Rebooting") || lastAdmin(d).GetRebootSeconds() != 5 { t.Fatal(out, err) }
}
func TestFactoryResetConfirm(t *testing.T) {
	d := fakedevice.New()
	runCLI(t, d, "n\n", "factory-reset")
	if lastAdmin(d).GetFactoryResetConfig() != 0 { t.Fatal("reset without confirm") }
	runCLI(t, d, "y\n", "factory-reset", "--full")
	if lastAdmin(d).GetFactoryResetDevice() != 1 { t.Fatal("full reset not sent") }
}
```

- [ ] **Step 2: Run** `go test ./internal/device/ ./internal/cli/` → FAIL.
- [ ] **Step 3: Implement.**
  - `config set`: clone `State().Config`/`ModuleConfig`, `configpath.Set` each pair, then `configpath.Changes` → `[]device.Change`, then `LockoutRisks`. Print the risks to stderr and call `confirmOrAbort`, then `Apply`.
  - On success print `Applied N change(s); verified.`, or `No changes needed.` when nothing changed.
  - Lock-out rules:
    - BLE + `bluetooth.enabled` → false
    - serial + `security.serial_enabled` → false
    - TCP + resulting network config has neither `wifi_enabled` nor `eth_enabled`
    - any transport + `security.is_managed` → true ("local config will be blocked")
- [ ] **Step 4: Run** `go test ./internal/device/ ./internal/cli/` → PASS.
- [ ] **Step 5: Commit** `git commit -m "feat: config get/set, owner, reboot/shutdown/factory-reset with lock-out guard"`

---

### Task 9: Channels and channel URLs

**Files:**
- Create: `internal/channelurl/url.go`, `internal/cli/channel.go`
- Test: `internal/channelurl/url_test.go`, `internal/cli/channel_test.go`

**Interfaces:**
- Consumes: Apply, Change (Task 7); `configpath.SetField`, `FormatField` (Task 4); `confirmOrAbort` (Task 8).
- Produces:
  ```go
  const Prefix = "https://meshtastic.org/e/#"
  func Encode(channels [8]*pb.Channel, lora *pb.Config_LoRaConfig) (string, error) // PRIMARY then SECONDARY in index order; base64.RawURLEncoding
  func Decode(url string) (*pb.ChannelSet, error)   // takes text after '#'; tolerates '=' padding and std alphabet, and "?add=true"
  func ToChannels(set *pb.ChannelSet) [8]*pb.Channel // [0] PRIMARY, then SECONDARY, rest DISABLED; Index set on all
  ```
- Commands:
  - `channel list`: index, role, name (`(default)` if empty), PSK shown as `default` / `none` / `random (N bytes)`, uplink/downlink. Never print PSK bytes in human output; `--json` includes `psk` as `base64:`.
  - `channel add <name> [--psk random|none|default|base64:...]`: default `random` = 32 bytes from `crypto/rand`; uses the first DISABLED index.
  - `channel set <index> <field> <value>`: field is any `ChannelSettings` field (dotted for `module_settings.position_precision`). `psk` accepts the same keywords.
  - `channel del <index>`: shifts later channels down, last becomes DISABLED, all in one `Apply`. Refuses index 0.
  - `channel url [--set <url>] [--no-qr]`: print the URL and a half-block QR (`qrterminal.GenerateHalfBlock`). `--set` replaces all channels and the LoRa config, after confirmation.

- [ ] **Step 1: Write the failing tests**

```go
func chans() [8]*pb.Channel {
	var c [8]*pb.Channel
	for i := range c { c[i] = &pb.Channel{Index: int32(i), Role: pb.Channel_DISABLED} }
	c[0] = &pb.Channel{Index: 0, Role: pb.Channel_PRIMARY, Settings: &pb.ChannelSettings{Psk: []byte{1}}}
	c[1] = &pb.Channel{Index: 1, Role: pb.Channel_SECONDARY, Settings: &pb.ChannelSettings{Name: "admin", Psk: bytes.Repeat([]byte{9}, 32)}}
	return c
}
func TestURLRoundTrip(t *testing.T) {
	lora := &pb.Config_LoRaConfig{Region: pb.Config_LoRaConfig_EU_868, UsePreset: true}
	u, err := Encode(chans(), lora)
	if err != nil || !strings.HasPrefix(u, Prefix) || strings.Contains(u, "=") { t.Fatal(u, err) }
	set, err := Decode(u)
	if err != nil || len(set.Settings) != 2 || set.Settings[1].Name != "admin" || set.LoraConfig.Region != pb.Config_LoRaConfig_EU_868 { t.Fatal(set, err) }
}
func TestDecodeVariants(t *testing.T) {
	raw, _ := proto.Marshal(&pb.ChannelSet{Settings: []*pb.ChannelSettings{{Psk: []byte{1}}}})
	for _, u := range []string{
		Prefix + base64.RawURLEncoding.EncodeToString(raw),
		"https://meshtastic.org/e/?add=true#" + base64.RawURLEncoding.EncodeToString(raw),
		Prefix + base64.StdEncoding.EncodeToString(raw),
	} { if _, err := Decode(u); err != nil { t.Errorf("%s: %v", u, err) } }
	for _, bad := range []string{"https://meshtastic.org/e/", Prefix + "!!!", Prefix} {
		if _, err := Decode(bad); err == nil { t.Errorf("%q accepted", bad) }
	}
}
func TestToChannels(t *testing.T) {
	c := ToChannels(&pb.ChannelSet{Settings: []*pb.ChannelSettings{{}, {Name: "b"}}})
	if c[0].Role != pb.Channel_PRIMARY || c[1].Role != pb.Channel_SECONDARY || c[7].Role != pb.Channel_DISABLED || c[7].Index != 7 { t.Fatal(c) }
}

// channel_test.go
func TestChannelAdd(t *testing.T) {
	d := fakedevice.New()
	if _, _, err := runCLI(t, d, "", "channel", "add", "admin"); err != nil { t.Fatal(err) }
	if c := d.Channels[1]; c.Role != pb.Channel_SECONDARY || c.Settings.Name != "admin" || len(c.Settings.Psk) != 32 { t.Fatal(c) }
}
func TestChannelAddNameByteLimit(t *testing.T) {
	if _, _, err := runCLI(t, fakedevice.New(), "", "channel", "add", "🦊🦊🦊"); err == nil || !strings.Contains(err.Error(), "11 bytes") { t.Fatal(err) }
}
func TestChannelAddFull(t *testing.T) { /* all 8 non-disabled → error contains "no free channel slot" */ }
func TestChannelSetPSKKeywords(t *testing.T) {
	d := fakedevice.New(); d.Channels = chans()
	for kw, n := range map[string]int{"none": 0, "default": 1, "random": 32} {
		if _, _, err := runCLI(t, d, "", "channel", "set", "1", "psk", kw); err != nil || len(d.Channels[1].Settings.Psk) != n { t.Fatal(kw, err) }
	}
}
func TestChannelDelShiftsDown(t *testing.T) {
	d := fakedevice.New(); d.Channels = chans()
	d.Channels[2] = &pb.Channel{Index: 2, Role: pb.Channel_SECONDARY, Settings: &pb.ChannelSettings{Name: "two"}}
	if _, _, err := runCLI(t, d, "", "channel", "del", "1"); err != nil { t.Fatal(err) }
	if d.Channels[1].Settings.Name != "two" || d.Channels[2].Role != pb.Channel_DISABLED || countBegin(d.Received()) != 1 { t.Fatal(d.Channels) }
}
func TestChannelDelPrimaryRefused(t *testing.T) {
	if _, _, err := runCLI(t, fakedevice.New(), "", "channel", "del", "0"); err == nil || !strings.Contains(err.Error(), "primary") { t.Fatal(err) }
}
func TestChannelListHidesPSK(t *testing.T) {
	d := fakedevice.New(); d.Channels = chans()
	out, _, _ := runCLI(t, d, "", "channel", "list")
	if !strings.Contains(out, "random (32 bytes)") || strings.Contains(out, base64.StdEncoding.EncodeToString(d.Channels[1].Settings.Psk)) { t.Fatal(out) }
}
func TestChannelURLSet(t *testing.T) {
	src := chans(); src[1].Settings.Name = "club"
	u, _ := channelurl.Encode(src, &pb.Config_LoRaConfig{Region: pb.Config_LoRaConfig_US, UsePreset: true, TxEnabled: true})
	d := fakedevice.New()
	if _, _, err := runCLI(t, d, "y\n", "channel", "url", "--set", u); err != nil { t.Fatal(err) }
	if d.Channels[1].Settings.Name != "club" || d.Config.Lora.Region != pb.Config_LoRaConfig_US { t.Fatal() }
}
```

- [ ] **Step 2: Run** `go test ./internal/channelurl/ ./internal/cli/` → FAIL.
- [ ] **Step 3: Implement.** Channel name limit: 11 bytes. Changes from `--set` are one `Change` per channel whose content differs, plus one lora `Config` change, all in one `Apply`.
- [ ] **Step 4: Run** `go test ./internal/channelurl/ ./internal/cli/` → PASS.
- [ ] **Step 5: Commit** `git commit -m "feat: channel commands and channel URLs"`

---

### Task 10: Config export and import

**Files:**
- Create: `internal/backup/backup.go`, `internal/backup/testdata/python-export.yaml`
- Modify: `internal/cli/config.go`
- Test: `internal/backup/backup_test.go`, `internal/cli/config_test.go`

**Interfaces:**
- Consumes: State (Task 5); `channelurl.Encode/Decode/ToChannels` (Task 9); `configpath.Sections`, `Changes` (Task 4); Apply, LockoutRisks (Tasks 7–8).
- Produces:
  ```go
  func Export(s *device.State) ([]byte, error)
  type Location struct{ Lat, Lon float64; Alt int32 }
  type Doc struct {
      Owner, OwnerShort, ChannelURL string
      Location    *Location
      Unsupported []string     // top-level keys we recognise but don't apply: canned_messages, ringtone
  }
  func Read(data []byte) (*Doc, error)
  func (d *Doc) MergeConfig(base *pb.LocalConfig, withKeys bool) (merged *pb.LocalConfig, skipped []string, err error)
  func (d *Doc) MergeModuleConfig(base *pb.LocalModuleConfig) (*pb.LocalModuleConfig, error)
  ```
  - Export format, matching Python `--export-config`:
    - first line `# start of Meshtastic configure yaml`
    - top-level keys `owner`, `owner_short`, `channel_url`, `location` (only if `position.fixed_position`), `config`, `module_config`
    - section keys snake_case; field keys camelCase (protojson default)
    - every bytes field prefixed `base64:`
    - `EmitUnpopulated: true`, so the backup is complete
  - Import semantics: per field, only keys present in the file overwrite `base` (explicit `false`/`0` included); absent keys keep `base`. Section and field keys are accepted in snake or camel case. `base64:` is stripped from bytes. An unknown section or field is an error naming it.
  - Without `withKeys`, `security.private_key` and `security.public_key` are not applied, and both are listed in `skipped`.
- Commands:
  - `config export [-o file]`: stdout by default; the file is written `0600`.
  - `config import <file> [--keys]`. The CLI builds base config as current State. If `channel_url` is set: base `Lora` = URL's LoRa config, and channel changes come from `ToChannels`. Then `MergeConfig` → `Changes`. Also owner change if present, and `FixedPosition` (`LatitudeI = lat*1e7`, `LongitudeI = lon*1e7`, `Altitude`) if present. Run the lock-out guard, then one `Apply`. Print skipped and unsupported keys to stderr.

- [ ] **Step 1: Write the fixture** `testdata/python-export.yaml`, modelled on Python's `export_config` output. The `channel_url` value is the literal placeholder `{{URL}}`, which tests replace via `channelurl.Encode`.

```yaml
# start of Meshtastic configure yaml
owner: Base Station
owner_short: BASE
channel_url: '{{URL}}'
location:
  lat: 59.9139
  lon: 10.7522
  alt: 23
canned_messages: Hi|Bye
config:
  bluetooth:
    enabled: false
    fixedPin: 123456
  device:
    role: ROUTER
  lora:
    hopLimit: 5
    region: EU_868
    txEnabled: true
    usePreset: true
  security:
    adminKey:
    - base64:AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA=
    privateKey: base64:ICEiIyQlJicoKSorLC0uLzAxMjM0NTY3ODk6Ozw9Pj8=
    publicKey: base64:QEFCQ0RFRkdISUpLTE1OT1BRUlNUVVZXWFlaW1xdXl8=
    serialEnabled: true
module_config:
  mqtt:
    enabled: false
    encryptionEnabled: true
  telemetry:
    deviceUpdateInterval: 900
```

- [ ] **Step 2: Write the failing tests**

```go
func fixture(t *testing.T) []byte { /* read testdata, replace {{URL}} with channelurl.Encode of a primary-only set */ }
func TestReadPythonFixture(t *testing.T) {
	d, err := Read(fixture(t))
	if err != nil || d.Owner != "Base Station" || d.OwnerShort != "BASE" || d.Location.Lat != 59.9139 || !slices.Contains(d.Unsupported, "canned_messages") { t.Fatal(d, err) }
}
func TestMergeConfigPresenceSemantics(t *testing.T) {
	d, _ := Read(fixture(t))
	base := &pb.LocalConfig{Bluetooth: &pb.Config_BluetoothConfig{Enabled: true}, Lora: &pb.Config_LoRaConfig{TxPower: 27}, Security: &pb.Config_SecurityConfig{PrivateKey: []byte{1}}}
	got, skipped, err := d.MergeConfig(base, false)
	if err != nil { t.Fatal(err) }
	if got.Bluetooth.Enabled { t.Error("explicit false not applied") }
	if got.Lora.TxPower != 27 || got.Lora.HopLimit != 5 { t.Error("absent key overwrote base, or present key missed") }
	if !bytes.Equal(got.Security.PrivateKey, []byte{1}) || len(got.Security.AdminKey) != 1 { t.Error("key handling") }
	if !slices.Contains(skipped, "security.private_key") { t.Error(skipped) }
	got, _, _ = d.MergeConfig(base, true)
	if len(got.Security.PrivateKey) != 32 { t.Error("--keys not honoured") }
}
func TestMergeAcceptsSnakeAndCamel(t *testing.T) {
	for _, y := range []string{"config: {lora: {hop_limit: 6}}", "config: {lora: {hopLimit: 6}}"} {
		d, _ := Read([]byte(y)); got, _, err := d.MergeConfig(&pb.LocalConfig{}, false)
		if err != nil || got.Lora.HopLimit != 6 { t.Fatal(y, err) }
	}
}
func TestMergeUnknownNamesError(t *testing.T) {
	for y, name := range map[string]string{"config: {lroa: {region: EU_868}}": "lroa", "config: {lora: {regoin: EU_868}}": "regoin"} {
		d, _ := Read([]byte(y))
		if _, _, err := d.MergeConfig(&pb.LocalConfig{}, false); err == nil || !strings.Contains(err.Error(), name) { t.Fatal(y, err) }
	}
}
func TestExportRoundTrip(t *testing.T) {
	c, _ := device.Open(ctxT(t), fakedevice.New().Dialer(), device.Options{}); st := c.State()
	out, err := Export(st)
	if err != nil { t.Fatal(err) }
	for _, want := range []string{"# start of Meshtastic configure yaml", "owner_short: TEST", "module_config:", "external_notification:", "hopLimit:"} {
		if !strings.Contains(string(out), want) { t.Errorf("missing %q", want) }
	}
	d, _ := Read(out)
	cfg, _, _ := d.MergeConfig(&pb.LocalConfig{}, true)
	mod, _ := d.MergeModuleConfig(&pb.LocalModuleConfig{})
	if !proto.Equal(cfg, st.Config) || !proto.Equal(mod, st.ModuleConfig) { t.Fatal("round trip lost data") }
}

// cli config_test.go
func TestExportFileMode(t *testing.T) {
	p := filepath.Join(t.TempDir(), "node.yaml")
	if _, _, err := runCLI(t, fakedevice.New(), "", "config", "export", "-o", p); err != nil { t.Fatal(err) }
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 { t.Fatal(fi.Mode()) }
}
func TestImportClonesWithoutIdentity(t *testing.T) {
	a := fakedevice.New(); a.Config.Lora.HopLimit = 6; a.Config.Security.PrivateKey = bytes.Repeat([]byte{1}, 32)
	p := filepath.Join(t.TempDir(), "a.yaml")
	runCLI(t, a, "", "config", "export", "-o", p)
	b := fakedevice.New(); b.Config.Security.PrivateKey = bytes.Repeat([]byte{2}, 32)
	_, stderr, err := runCLI(t, b, "", "config", "import", p)
	if err != nil || b.Config.Lora.HopLimit != 6 || b.Config.Security.PrivateKey[0] != 2 || !strings.Contains(stderr, "security.private_key") { t.Fatal(err, stderr) }
}
```

- [ ] **Step 3: Run** `go test ./internal/backup/ ./internal/cli/` → FAIL.
- [ ] **Step 4: Implement.**
  - Export: per section, `protojson.MarshalOptions{EmitUnpopulated: true}` → map → prefix bytes fields with `base64:` (walk the descriptor) → nest under the section's snake name → `yaml.Marshal` of an ordered top-level structure.
  - Import: `yaml.YAMLToJSON` → map. For each section and key, resolve the field by `TextName` or `JSONName`. Strip `base64:`. Unmarshal that single key into a fresh section message with `protojson`, then copy only that field onto the base clone.
- [ ] **Step 5: Run** `go test ./internal/backup/ ./internal/cli/` → PASS.
- [ ] **Step 6: Commit** `git commit -m "feat: config export/import compatible with Python backups"`

---

### Task 11: Bluetooth LE transport and `ble scan`

**Files:**
- Create: `internal/transport/ble.go`, `internal/cli/ble.go`
- Modify: `internal/cli/root.go` (wire `--ble` in `DefaultDeps().Dial`)
- Test: `internal/transport/ble_test.go`

**Interfaces:**
- Consumes: Conn, Kind (Task 3).
- Produces:
  ```go
  type BLEDevice struct{ Name, Address string; RSSI int16 }
  func ScanBLE(ctx context.Context, d time.Duration) ([]BLEDevice, error)   // filter on service UUID
  func DialBLE(ctx context.Context, nameOrAddr string, log io.Writer) (Conn, error)
  // testable core:
  type bleLink interface { WriteToRadio(p []byte) error; ReadFromRadio(buf []byte) (int, error) }
  func newBLEConn(l bleLink, log io.Writer) *bleConn      // bleConn implements Conn; Kind() == KindBLE
  func (c *bleConn) notify()                               // called on FromNum notifications
  func matchDevice(d BLEDevice, want string) bool          // case-insensitive exact on Name or Address
  ```
- Command: `ble scan [--duration 10s]` prints name, address and RSSI (`--json` array). This command doesn't connect to a node.

- [ ] **Step 1: Write the failing tests**

```go
type fakeLink struct{ mu sync.Mutex; queue [][]byte; written [][]byte }
func (f *fakeLink) WriteToRadio(p []byte) error { f.mu.Lock(); defer f.mu.Unlock(); f.written = append(f.written, p); return nil }
func (f *fakeLink) ReadFromRadio(b []byte) (int, error) {
	f.mu.Lock(); defer f.mu.Unlock()
	if len(f.queue) == 0 { return 0, nil }
	n := copy(b, f.queue[0]); f.queue = f.queue[1:]; return n, nil
}
func fr(id uint32) []byte { p, _ := proto.Marshal(&pb.FromRadio{Id: id}); return p }

func TestBLEDrainReadsUntilEmpty(t *testing.T) {
	l := &fakeLink{queue: [][]byte{fr(1), fr(2), fr(3)}}
	c := newBLEConn(l, io.Discard); c.notify()
	for want := uint32(1); want <= 3; want++ {
		m, err := c.Recv(ctxT(t)); if err != nil || m.Id != want { t.Fatal(m, err) }
	}
}
func TestBLESendWritesRawProtoAndDrains(t *testing.T) {
	l := &fakeLink{}
	c := newBLEConn(l, io.Discard)
	l.queue = [][]byte{fr(9)}
	if err := c.Send(ctxT(t), &pb.ToRadio{PayloadVariant: &pb.ToRadio_WantConfigId{WantConfigId: 1}}); err != nil { t.Fatal(err) }
	if len(l.written) != 1 || l.written[0][0] == 0x94 { t.Fatal("must write unframed protobuf") }
	if m, _ := c.Recv(ctxT(t)); m.Id != 9 { t.Fatal("no drain after write") }
}
func TestMatchDevice(t *testing.T) {
	d := BLEDevice{Name: "Meshtastic_ab12", Address: "AA:BB:CC:DD:EE:FF"}
	if !matchDevice(d, "meshtastic_ab12") || !matchDevice(d, "aa:bb:cc:dd:ee:ff") || matchDevice(d, "Meshtastic") { t.Fatal() }
}
```

- [ ] **Step 2: Run** `go test ./internal/transport/` → FAIL.
- [ ] **Step 3: Implement** with `tinygo.org/x/bluetooth`.
  - `DialBLE`: scan up to 10s for a match, connect, discover the service and the three characteristics, and enable FromNum notifications → `notify()`. Do an initial drain.
  - `bleConn` serialises drains with a mutex. Drain reads into a 512-byte buffer until it reads 0 bytes. Each read is one `FromRadio`, with no framing.
  - Read errors close the conn (`ErrClosed`).
  - The macOS address is the CoreBluetooth UUID. Say so in `ble scan` help.
- [ ] **Step 4: Run** `go test ./internal/transport/ && go build ./cmd/meshtastik` → PASS.
- [ ] **Step 5: Commit** `git commit -m "feat: BLE transport and ble scan"`

---

### Task 12: `send`

**Files:**
- Create: `internal/device/text.go`, `internal/cli/send.go`
- Modify: `internal/fakedevice/fake.go` (knob `AckTexts bool`, `Texts() []*pb.MeshPacket`)
- Test: `internal/device/text_test.go`, `internal/cli/send_test.go`

**Interfaces:**
- Consumes: Client and its reader goroutine (Tasks 5, 7).
- Produces:
  - `func (c *Client) SendText(ctx context.Context, text string, to uint32, channel uint32, waitAck bool) error`. Returns `ErrNoAck` on timeout (ctx deadline) when `waitAck`.
  - `var ErrNoAck = errors.New("no acknowledgement")`
  - `const Broadcast = 0xffffffff`
  - `func resolveNode(s *device.State, q string) (uint32, error)`: in the CLI.
- Command: `send <text> [--to <node>] [--channel <i>] [--ack-timeout 30s]`.
  - `--to` accepts `!hex`, a decimal number, or a short/long name (case-insensitive, must be unique).
  - Direct messages set `want_ack` and wait for the ROUTING_APP reply whose `request_id` matches.
  - Broadcasts print `Sent.` without waiting.
  - The text limit is `pb.Constants_DATA_PAYLOAD_LEN` (233) bytes.

- [ ] **Step 1: Write the failing tests**

```go
func TestResolveNode(t *testing.T) {
	s := &device.State{Nodes: map[uint32]*pb.NodeInfo{
		0x1234abcd: {Num: 0x1234abcd, User: &pb.User{LongName: "Base Station", ShortName: "BASE"}},
		7: {Num: 7, User: &pb.User{LongName: "Car", ShortName: "CAR1"}}, 8: {Num: 8, User: &pb.User{LongName: "Car", ShortName: "CAR2"}},
	}}
	for q, want := range map[string]uint32{"!1234abcd": 0x1234abcd, "305441741": 0x1234abcd, "base": 0x1234abcd, "base station": 0x1234abcd, "car2": 8} {
		if got, err := resolveNode(s, q); err != nil || got != want { t.Errorf("%s: %x %v", q, got, err) }
	}
	if _, err := resolveNode(s, "Car"); err == nil || !strings.Contains(err.Error(), "CAR1") { t.Error("ambiguous not reported") }
	if _, err := resolveNode(s, "nobody"); err == nil { t.Error("unknown accepted") }
}
func TestSendBroadcast(t *testing.T) {
	d := fakedevice.New()
	out, _, err := runCLI(t, d, "", "send", "hello")
	p := d.Texts()[0]
	if err != nil || p.To != device.Broadcast || string(p.GetDecoded().Payload) != "hello" || !strings.Contains(out, "Sent") { t.Fatal(out, err) }
}
func TestSendDirectAck(t *testing.T) {
	d := fakedevice.New(); d.AckTexts = true
	d.ExtraNodes = []*pb.NodeInfo{{Num: 42, User: &pb.User{LongName: "Peer", ShortName: "PEER"}}}
	out, _, err := runCLI(t, d, "", "send", "hi", "--to", "PEER")
	if err != nil || !strings.Contains(out, "Acknowledged") || !d.Texts()[0].WantAck { t.Fatal(out, err) }
}
func TestSendDirectNoAck(t *testing.T) {
	d := fakedevice.New(); d.ExtraNodes = []*pb.NodeInfo{{Num: 42, User: &pb.User{ShortName: "PEER"}}}
	_, _, err := runCLI(t, d, "", "send", "hi", "--to", "PEER", "--ack-timeout", "100ms")
	if err == nil || !strings.Contains(err.Error(), "no acknowledgement") { t.Fatal(err) }
}
func TestSendTooLong(t *testing.T) {
	if _, _, err := runCLI(t, fakedevice.New(), "", "send", strings.Repeat("x", 234)); err == nil || !strings.Contains(err.Error(), "233") { t.Fatal(err) }
}
```

- [ ] **Step 2: Run** `go test ./internal/device/ ./internal/cli/` → FAIL.
- [ ] **Step 3: Implement.** The packet is `MeshPacket{To, Channel, Id: random, WantAck: to != Broadcast, Decoded: Data{Portnum: TEXT_MESSAGE_APP, Payload: text}}`. Ack wait uses the Task 7 waiter map, keyed by packet id, matching `ROUTING_APP` packets with `Decoded.RequestId == id`. A `Routing.error_reason` other than `NONE` → error naming the reason.
- [ ] **Step 4: Run** `go test -race ./...` → PASS.
- [ ] **Step 5: Commit** `git commit -m "feat: send text messages"`

---

### Task 13: Cross-platform builds and hardware smoke test

**Files:**
- Create: `docs/smoke-test.md`
- Modify: `README.md` (command overview from the spec; the macOS Bluetooth-permission note)

- [ ] **Step 1: Verify the builds.**

Run:
```bash
go build -o /dev/null ./cmd/meshtastik
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /dev/null ./cmd/meshtastik
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o /dev/null ./cmd/meshtastik
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o /dev/null ./cmd/meshtastik
```
Expected: all succeed. If a target fails only because of BLE, move `ble.go` behind build constraints. Add a `ble_unsupported.go` whose `DialBLE`/`ScanBLE` return "BLE is not supported in this build". Re-run, and record the outcome in README.

- [ ] **Step 2: Write `docs/smoke-test.md`.** It's a checklist to run against a real node for each transport (serial, TCP, BLE):
  - `info`, `nodes`, `config get lora`
  - `config set lora.hop_limit <n>`, then restore it
  - a reboot-triggering change (`device.role`), confirming reconnect and verify
  - `owner set`
  - `channel add test` / `channel del 1`
  - `channel url`, then scan the QR with the phone app
  - `config export -o backup.yaml`, then `config import backup.yaml --keys` on the same node, which should report `No changes needed.`
  - `send` to a second node, if available
  - the lock-out prompt over BLE (`bluetooth.enabled false`, answer `n`)

  Also replace `internal/backup/testdata/python-export.yaml` with a real Python export if one is available. Note the macOS Bluetooth permission prompt for the terminal app, and pairing PIN behaviour.

- [ ] **Step 3: Run** `go test -race ./... && go vet ./...` → PASS.
- [ ] **Step 4: Commit** `git commit -m "docs: smoke test checklist and cross-platform build notes"`

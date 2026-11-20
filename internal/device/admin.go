package device

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"meshtastik/internal/configpath"
)

// Change is one write to the node; exactly one field is set. Config and
// ModuleConfig carry a whole section, which replaces the node's section.
type Change struct {
	Config        *pb.Config
	ModuleConfig  *pb.ModuleConfig
	Channel       *pb.Channel
	Owner         *pb.User
	FixedPosition *pb.Position
}

// Mismatch is a field the node did not take.
type Mismatch = configpath.FieldDiff

// VerifyError lists the fields whose read-back value differs from the write.
type VerifyError struct{ Mismatches []Mismatch }

// privateKeyPath is the one field whose value is never shown in messages.
const privateKeyPath = "security.private_key"

func (e *VerifyError) Error() string {
	var b strings.Builder
	b.WriteString("not applied by the node:")
	for _, m := range e.Mismatches {
		want, got := m.Want, m.Got
		if m.Path == privateKeyPath {
			want, got = "<redacted>", "<redacted>"
		}
		fmt.Fprintf(&b, "\n  %s: want %s, got %s", m.Path, want, got)
	}
	return b.String()
}

// ErrPartialEdit means an edit transaction was begun but not committed.
var ErrPartialEdit = errors.New("edit interrupted before commit; firmware 2.8 and newer saves what it received after 60 s, older firmware holds it until reboot — check with config get")

// section returns the section carried by a Config/ModuleConfig wrapper and
// the matching section of the Local* message.
func section(wrapper, local protoreflect.Message) (name string, want, got protoreflect.Message, err error) {
	var f protoreflect.FieldDescriptor
	wrapper.Range(func(fd protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
		if fd.ContainingOneof() != nil && fd.Message() != nil {
			f = fd
			return false
		}
		return true
	})
	if f == nil {
		return "", nil, nil, errors.New("no config section set")
	}
	lf := local.Descriptor().Fields().ByName(f.Name())
	if lf == nil {
		return "", nil, nil, fmt.Errorf("config section %q cannot be written", f.Name())
	}
	return string(f.Name()), wrapper.Get(f).Message(), local.Get(lf).Message(), nil
}

func (ch Change) validate() error {
	n := 0
	for _, set := range []bool{ch.Config != nil, ch.ModuleConfig != nil, ch.Channel != nil, ch.Owner != nil, ch.FixedPosition != nil} {
		if set {
			n++
		}
	}
	if n != 1 {
		return fmt.Errorf("must set exactly one of config, module config, channel, owner or fixed position (has %d)", n)
	}
	var err error
	switch {
	case ch.Config != nil:
		_, _, _, err = section(ch.Config.ProtoReflect(), (&pb.LocalConfig{}).ProtoReflect())
	case ch.ModuleConfig != nil:
		_, _, _, err = section(ch.ModuleConfig.ProtoReflect(), (&pb.LocalModuleConfig{}).ProtoReflect())
	case ch.Channel != nil:
		if i := ch.Channel.GetIndex(); i < 0 || int(i) >= len(State{}.Channels) {
			err = fmt.Errorf("channel index %d out of range 0-%d", i, len(State{}.Channels)-1)
		}
	}
	return err
}

// diff compares the change with s. The same comparison decides whether a
// change is a no-op and whether the node took it.
func (ch Change) diff(s *State) []Mismatch {
	switch {
	case ch.Config != nil:
		name, want, got, _ := section(ch.Config.ProtoReflect(), s.Config.ProtoReflect())
		return configpath.FieldDiffs(name, want, got)
	case ch.ModuleConfig != nil:
		name, want, got, _ := section(ch.ModuleConfig.ProtoReflect(), s.ModuleConfig.ProtoReflect())
		return configpath.FieldDiffs(name, want, got)
	case ch.Channel != nil:
		i := ch.Channel.GetIndex()
		got := s.Channels[i]
		prefix := fmt.Sprintf("channel[%d]", i)
		settings := func(c *pb.Channel) protoreflect.Message {
			if c.GetSettings() == nil {
				return (&pb.ChannelSettings{}).ProtoReflect()
			}
			return c.GetSettings().ProtoReflect()
		}
		out := configpath.FieldDiffs(prefix, settings(ch.Channel), settings(got))
		if w, g := ch.Channel.GetRole(), got.GetRole(); w != g {
			out = append(out, Mismatch{Path: prefix + ".role", Want: w.String(), Got: g.String()})
		}
		return out
	case ch.Owner != nil:
		u := s.Self().GetUser()
		var out []Mismatch
		if w, g := ch.Owner.GetLongName(), u.GetLongName(); w != g {
			out = append(out, Mismatch{Path: "owner.long_name", Want: w, Got: g})
		}
		if w, g := ch.Owner.GetShortName(), u.GetShortName(); w != g {
			out = append(out, Mismatch{Path: "owner.short_name", Want: w, Got: g})
		}
		return out
	case ch.FixedPosition != nil:
		var out []Mismatch
		if !s.Config.GetPosition().GetFixedPosition() {
			out = append(out, Mismatch{Path: "position.fixed_position", Want: "true", Got: "false"})
		}
		if s.remote { // a remote node's coordinates cannot be read back
			return out
		}
		cur := s.Self().GetPosition()
		for _, f := range []struct {
			path      string
			want, got int32
		}{
			{"position.latitude_i", ch.FixedPosition.GetLatitudeI(), cur.GetLatitudeI()},
			{"position.longitude_i", ch.FixedPosition.GetLongitudeI(), cur.GetLongitudeI()},
			{"position.altitude", ch.FixedPosition.GetAltitude(), cur.GetAltitude()},
		} {
			if f.want != f.got {
				out = append(out, Mismatch{Path: f.path, Want: strconv.Itoa(int(f.want)), Got: strconv.Itoa(int(f.got))})
			}
		}
		return out
	}
	return nil
}

func (ch Change) admin() *pb.AdminMessage {
	m := &pb.AdminMessage{}
	switch {
	case ch.Config != nil:
		m.PayloadVariant = &pb.AdminMessage_SetConfig{SetConfig: ch.Config}
	case ch.ModuleConfig != nil:
		m.PayloadVariant = &pb.AdminMessage_SetModuleConfig{SetModuleConfig: ch.ModuleConfig}
	case ch.Channel != nil:
		m.PayloadVariant = &pb.AdminMessage_SetChannel{SetChannel: ch.Channel}
	case ch.Owner != nil:
		m.PayloadVariant = &pb.AdminMessage_SetOwner{SetOwner: ch.Owner}
	case ch.FixedPosition != nil:
		m.PayloadVariant = &pb.AdminMessage_SetFixedPosition{SetFixedPosition: ch.FixedPosition}
	}
	return m
}

// Apply writes the changes in one edit transaction, waits for the node to
// settle (reconnecting if it reboots), and verifies every change by reading
// the config back. Changes equal to the current state are skipped. applied
// counts the changes written; it is 0 whenever err is non-nil.
//
// ErrPartialEdit boundary: from the moment begin_edit_settings is handed to
// the connection (including when that send fails, as the frame may have
// reached the node) until commit_edit_settings has been sent, every failure
// wraps ErrPartialEdit. Failures before that (validation, fetching the
// session passkey) and after it (reconnecting, verifying) do not; a failure
// to reach the node after the commit says the changes were committed but
// could not be verified.
func (c *Client) Apply(ctx context.Context, changes []Change) (applied int, err error) {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	return c.apply(ctx, localTarget{c}, changes)
}

// adminTarget is what differs between writing to the connected node and to
// a remote one; the edit transaction around it is shared (apply).
type adminTarget interface {
	// current returns the state the changes are compared against.
	current(ctx context.Context, changes []Change) (*State, error)
	ensurePasskey(ctx context.Context) error
	post(ctx context.Context, m *pb.AdminMessage) error
	// settle waits for the node after the commit and returns its state.
	settle(ctx context.Context, changes []Change) (*State, error)
}

type localTarget struct{ c *Client }

func (t localTarget) current(context.Context, []Change) (*State, error)  { return t.c.State(), nil }
func (t localTarget) ensurePasskey(ctx context.Context) error            { return t.c.ensurePasskey(ctx) }
func (t localTarget) post(ctx context.Context, m *pb.AdminMessage) error { return t.c.post(ctx, m) }
func (t localTarget) settle(ctx context.Context, _ []Change) (*State, error) {
	if err := t.c.awaitNode(ctx); err != nil {
		return nil, err
	}
	return t.c.State(), nil
}

// apply is Apply's edit transaction against target t.
func (c *Client) apply(ctx context.Context, t adminTarget, changes []Change) (applied int, err error) {
	for i, ch := range changes {
		if err := ch.validate(); err != nil {
			return 0, fmt.Errorf("change %d: %w", i+1, err)
		}
	}
	st, err := t.current(ctx, changes)
	if err != nil {
		return 0, err
	}
	var todo []Change
	for _, ch := range changes {
		if len(ch.diff(st)) > 0 {
			todo = append(todo, ch)
		}
	}
	if len(todo) == 0 {
		return 0, nil
	}
	if err := t.ensurePasskey(ctx); err != nil {
		return 0, err
	}
	begin := &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_BeginEditSettings{BeginEditSettings: true}}
	partial := func(cause error) (int, error) { return 0, fmt.Errorf("%w: %v", ErrPartialEdit, cause) }
	if err := t.post(ctx, begin); err != nil {
		return partial(fmt.Errorf("begin edit: %w", err))
	}
	for _, ch := range todo {
		if err := ctx.Err(); err != nil {
			return partial(err)
		}
		if err := t.post(ctx, ch.admin()); err != nil {
			return partial(err)
		}
	}
	if err := ctx.Err(); err != nil {
		return partial(err)
	}
	commit := &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_CommitEditSettings{CommitEditSettings: true}}
	if err := t.post(ctx, commit); err != nil {
		return partial(err)
	}
	if st, err = t.settle(ctx, todo); err != nil {
		return 0, fmt.Errorf("changes were committed but could not be verified: %w", err)
	}
	var mismatches []Mismatch
	for _, ch := range todo {
		mismatches = append(mismatches, ch.diff(st)...)
	}
	if len(mismatches) > 0 {
		return 0, &VerifyError{Mismatches: mismatches}
	}
	return len(todo), nil
}

// awaitNode gives the node RebootGrace to reboot after a commit, then
// refreshes State by re-running the handshake on the same connection. If the
// node rebooted, or that re-read fails for any reason (a reboot mid-handshake,
// a dead or silent link), the commit has already landed, so State is
// refreshed over a new connection instead.
func (c *Client) awaitNode(ctx context.Context) error {
	l := c.current()
	t := time.NewTimer(c.opts.RebootGrace)
	defer t.Stop()
	select {
	case <-l.rebooted:
		return c.reconnect(ctx, afterReboot)
	case <-l.readerEnd:
		return c.reconnect(ctx, afterReboot)
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
	}
	err := c.handshake(ctx, l, c.opts.HandshakeTimeout, false)
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	fmt.Fprintf(c.opts.Log, "re-read after commit failed (%v); reconnecting\n", err)
	return c.reconnect(ctx, afterReboot)
}

// localNum is the connected node's number.
func (c *Client) localNum() uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state.MyInfo.GetMyNodeNum()
}

func (c *Client) sessionPasskey(node uint32) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.passkeys[node]
}

func (c *Client) setPasskey(node uint32, b []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.passkeys[node] = b
}

// ensurePasskey fetches the connected node's admin session passkey unless
// one is held.
func (c *Client) ensurePasskey(ctx context.Context) error {
	return c.ensurePasskeyFor(ctx, c.localNum())
}

// ensurePasskeyFor fetches node's admin session passkey unless one is held.
func (c *Client) ensurePasskeyFor(ctx context.Context, node uint32) error {
	if len(c.sessionPasskey(node)) > 0 {
		return nil
	}
	req := &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_GetConfigRequest{GetConfigRequest: pb.AdminMessage_SESSIONKEY_CONFIG}}
	if _, err := c.adminRequestTo(ctx, node, req); err != nil {
		return fmt.Errorf("get session passkey: %w", err)
	}
	return nil
}

// sendAdmin sends m, stamped with the session passkey, to the local node.
func (c *Client) sendAdmin(ctx context.Context, id uint32, m *pb.AdminMessage, wantResponse bool) error {
	to := c.localNum()
	m = proto.Clone(m).(*pb.AdminMessage)
	m.SessionPasskey = c.sessionPasskey(to)
	payload, err := proto.Marshal(m)
	if err != nil {
		return fmt.Errorf("encode admin message: %w", err)
	}
	pkt := &pb.MeshPacket{
		To:      to,
		Id:      id,
		Channel: 0,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{
			Portnum:      pb.PortNum_ADMIN_APP,
			Payload:      payload,
			WantResponse: wantResponse,
		}},
	}
	return c.send(ctx, &pb.ToRadio{PayloadVariant: &pb.ToRadio_Packet{Packet: pkt}})
}

// post sends an admin message that expects no response to the local node.
func (c *Client) post(ctx context.Context, m *pb.AdminMessage) error {
	return c.sendAdmin(ctx, randomID(), m, false)
}

// AdminRequest sends m to the local node and waits for its admin response.
func (c *Client) AdminRequest(ctx context.Context, m *pb.AdminMessage) (*pb.AdminMessage, error) {
	return c.adminRequestTo(ctx, c.localNum(), m)
}

// adminRequestTo sends m to node to and waits for its admin response: the
// connected node directly, another node over the mesh (see remoteAdminRequest).
func (c *Client) adminRequestTo(ctx context.Context, to uint32, m *pb.AdminMessage) (*pb.AdminMessage, error) {
	if to != c.localNum() {
		return c.remoteAdminRequest(ctx, to, m)
	}
	id := randomID()
	responses := c.addWaiter(id)
	defer c.removeWaiter(id)
	l := c.current()
	if err := c.sendAdmin(ctx, id, m, true); err != nil {
		return nil, err
	}
	t := time.NewTimer(c.opts.HandshakeTimeout)
	defer t.Stop()
	for {
		select {
		case p := <-responses:
			data := p.GetDecoded()
			switch data.GetPortnum() {
			case pb.PortNum_ADMIN_APP:
				resp := &pb.AdminMessage{}
				if err := proto.Unmarshal(data.GetPayload(), resp); err != nil {
					return nil, fmt.Errorf("decode admin response: %w", err)
				}
				if len(resp.SessionPasskey) > 0 {
					c.setPasskey(to, resp.SessionPasskey)
				}
				return resp, nil
			case pb.PortNum_ROUTING_APP:
				r := &pb.Routing{}
				if proto.Unmarshal(data.GetPayload(), r) == nil && r.GetErrorReason() != pb.Routing_NONE {
					return nil, fmt.Errorf("node rejected admin request: %s", r.GetErrorReason())
				}
			}
		case <-l.readerEnd:
			return nil, fmt.Errorf("admin request: %w", l.readErr)
		case <-t.C:
			return nil, fmt.Errorf("no response to admin request after %s", c.opts.HandshakeTimeout)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// postTo sends an admin message that expects no response to node to: the
// connected node directly, another node over the mesh (see remotePost).
func (c *Client) postTo(ctx context.Context, to uint32, m *pb.AdminMessage) error {
	if to != c.localNum() {
		return c.remotePost(ctx, to, m)
	}
	return c.post(ctx, m)
}

// action sends a single admin command, with the session passkey, unverified.
func (c *Client) action(ctx context.Context, m *pb.AdminMessage) error {
	if err := c.ensurePasskey(ctx); err != nil {
		return err
	}
	return c.post(ctx, m)
}

// Reboot asks the node to reboot after seconds.
func (c *Client) Reboot(ctx context.Context, seconds int32) error {
	return c.action(ctx, &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_RebootSeconds{RebootSeconds: seconds}})
}

// Shutdown asks the node to power off after seconds.
func (c *Client) Shutdown(ctx context.Context, seconds int32) error {
	return c.action(ctx, &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_ShutdownSeconds{ShutdownSeconds: seconds}})
}

// FactoryReset erases the config; full also erases the device state
// (factory_reset_device rather than factory_reset_config).
func (c *Client) FactoryReset(ctx context.Context, full bool) error {
	m := &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_FactoryResetConfig{FactoryResetConfig: 1}}
	if full {
		m.PayloadVariant = &pb.AdminMessage_FactoryResetDevice{FactoryResetDevice: 1}
	}
	return c.action(ctx, m)
}

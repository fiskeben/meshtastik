package device

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"

	"meshtastik/internal/configpath"
)

// Want selects what Remote.Fetch reads. Config and Modules hold section
// names as in configpath ("lora", "mqtt"); AllSections reads every one.
type Want struct {
	Owner, Metadata, Channels bool
	Config, Modules           []string
}

// AllSections, as Want.Config or Want.Modules, reads every section.
var AllSections = []string{"*"}

// Node is what admin commands act on: the connected node (Local) or another
// node reached over the mesh (Remote).
type Node interface {
	Num() uint32
	IsRemote() bool
	// Fetch returns the node's state; a remote node reads only what w asks for.
	Fetch(ctx context.Context, w Want) (*State, error)
	Apply(ctx context.Context, changes []Change) (int, error)
	Reboot(ctx context.Context, seconds int32) error
	Shutdown(ctx context.Context, seconds int32) error
	FactoryReset(ctx context.Context, full bool) error
	EditNodeDB(ctx context.Context, op NodeOp, num uint32) error
	ResetNodeDB(ctx context.Context, keepFavorites bool) error
}

// Local is the connected node as a Node.
type Local struct{ *Client }

func (l Local) Num() uint32    { return l.localNum() }
func (l Local) IsRemote() bool { return false }

// Fetch returns the state read during the handshake; w is ignored.
func (l Local) Fetch(context.Context, Want) (*State, error) { return l.State(), nil }

// Remote is another node administered over the mesh.
type Remote struct {
	c    *Client
	num  uint32
	meta *pb.DeviceMetadata
}

// Remote asks node num for its metadata, which proves it is reachable and
// accepts this node as an admin, fetches its session passkey, and checks it
// runs firmware 2.5 or newer.
func (c *Client) Remote(ctx context.Context, num uint32) (*Remote, error) {
	if num == c.localNum() {
		return nil, errors.New("that is the connected node, not a remote one")
	}
	resp, err := c.adminRequestTo(ctx, num, &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_GetDeviceMetadataRequest{GetDeviceMetadataRequest: true}})
	if err != nil {
		return nil, err
	}
	meta := resp.GetGetDeviceMetadataResponse()
	if v := meta.GetFirmwareVersion(); !supported(v) {
		if v == "" {
			v = "unknown firmware version"
		}
		return nil, fmt.Errorf("!%08x: %w: %s (need 2.5 or newer)", num, ErrUnsupportedFirmware, v)
	}
	return &Remote{c: c, num: num, meta: meta}, nil
}

func (r *Remote) Num() uint32    { return r.num }
func (r *Remote) IsRemote() bool { return true }

// Metadata is what the node reported when Remote reached it.
func (r *Remote) Metadata() *pb.DeviceMetadata { return proto.Clone(r.meta).(*pb.DeviceMetadata) }

// configType is the get_config_request enum for config section name.
func configType(name string) (pb.AdminMessage_ConfigType, bool) {
	v, ok := pb.AdminMessage_ConfigType_value[strings.ToUpper(name)+"_CONFIG"]
	return pb.AdminMessage_ConfigType(v), ok
}

// moduleTypes maps module sections whose get_module_config_request enum is
// not <NAME>_CONFIG.
var moduleTypes = map[string]pb.AdminMessage_ModuleConfigType{
	"external_notification": pb.AdminMessage_EXTNOTIF_CONFIG,
	"store_forward":         pb.AdminMessage_STOREFORWARD_CONFIG,
	"range_test":            pb.AdminMessage_RANGETEST_CONFIG,
	"canned_message":        pb.AdminMessage_CANNEDMSG_CONFIG,
	"remote_hardware":       pb.AdminMessage_REMOTEHARDWARE_CONFIG,
	"neighbor_info":         pb.AdminMessage_NEIGHBORINFO_CONFIG,
	"ambient_lighting":      pb.AdminMessage_AMBIENTLIGHTING_CONFIG,
	"detection_sensor":      pb.AdminMessage_DETECTIONSENSOR_CONFIG,
	"traffic_management":    pb.AdminMessage_TRAFFICMANAGEMENT_CONFIG,
	"mesh_beacon":           pb.AdminMessage_MESHBEACON_CONFIG,
}

// moduleType is the get_module_config_request enum for module section name.
func moduleType(name string) (pb.AdminMessage_ModuleConfigType, bool) {
	if t, ok := moduleTypes[name]; ok {
		return t, true
	}
	v, ok := pb.AdminMessage_ModuleConfigType_value[strings.ToUpper(name)+"_CONFIG"]
	return pb.AdminMessage_ModuleConfigType(v), ok
}

// sectionsWithoutEnum lists sections Fetch could not ask for (for a test).
func sectionsWithoutEnum() []string {
	var out []string
	for _, s := range configpath.ConfigSectionNames() {
		if _, ok := configType(s); !ok {
			out = append(out, s)
		}
	}
	for _, s := range configpath.ModuleSectionNames() {
		if _, ok := moduleType(s); !ok {
			out = append(out, "module "+s)
		}
	}
	return out
}

func expand(names, all []string) []string {
	if slices.Equal(names, AllSections) {
		return all
	}
	return names
}

func (r *Remote) progress(part string) {
	if f := r.c.opts.OnProgress; f != nil {
		f(part)
	}
}

// Fetch reads what w asks for from the node, one admin request per part.
// The result has the node as MyInfo and Self (with its owner when asked),
// the metadata from Remote, and only the fetched sections and channels set.
func (r *Remote) Fetch(ctx context.Context, w Want) (*State, error) {
	st := newState()
	st.remote = true
	st.MyInfo = &pb.MyNodeInfo{MyNodeNum: r.num}
	st.Metadata = r.Metadata()
	st.Nodes[r.num] = &pb.NodeInfo{Num: r.num}
	ask := func(part string, m *pb.AdminMessage) (*pb.AdminMessage, error) {
		r.progress(part)
		resp, err := r.c.adminRequestTo(ctx, r.num, m)
		if err != nil {
			return nil, fmt.Errorf("read %s from !%08x: %w", part, r.num, err)
		}
		return resp, nil
	}
	if w.Owner {
		resp, err := ask("owner", &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_GetOwnerRequest{GetOwnerRequest: true}})
		if err != nil {
			return nil, err
		}
		st.Nodes[r.num].User = resp.GetGetOwnerResponse()
	}
	for _, name := range expand(w.Config, configpath.ConfigSectionNames()) {
		t, ok := configType(name)
		if !ok {
			return nil, fmt.Errorf("unknown config section %q", name)
		}
		resp, err := ask(name, &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_GetConfigRequest{GetConfigRequest: t}})
		if err != nil {
			return nil, err
		}
		configpath.MergeConfig(st.Config, resp.GetGetConfigResponse())
	}
	for _, name := range expand(w.Modules, configpath.ModuleSectionNames()) {
		t, ok := moduleType(name)
		if !ok {
			return nil, fmt.Errorf("unknown module section %q", name)
		}
		resp, err := ask(name, &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_GetModuleConfigRequest{GetModuleConfigRequest: t}})
		if err != nil {
			return nil, err
		}
		configpath.MergeModuleConfig(st.ModuleConfig, resp.GetGetModuleConfigResponse())
	}
	if w.Channels {
		for i := range st.Channels {
			resp, err := ask(fmt.Sprintf("channel %d", i), &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_GetChannelRequest{GetChannelRequest: uint32(i + 1)}})
			if err != nil {
				return nil, err
			}
			ch := resp.GetGetChannelResponse()
			ch.Index = int32(i)
			st.Channels[i] = ch
		}
	}
	return st, nil
}

// Notes rather than failures: the CLI prints them after the result.
var (
	ErrCoordinatesUnverified = errors.New("fixed position set; its coordinates cannot be read back from a remote node")
	ErrNotVerifiable         = errors.New("sent; a remote node list cannot be read back")
)

// remoteTarget writes to a remote node over the mesh.
type remoteTarget struct{ r *Remote }

// want is what must be read to compare changes.
func want(changes []Change) Want {
	var w Want
	for _, ch := range changes {
		switch {
		case ch.Config != nil:
			name, _, _, _ := section(ch.Config.ProtoReflect(), (&pb.LocalConfig{}).ProtoReflect())
			w.Config = append(w.Config, name)
		case ch.ModuleConfig != nil:
			name, _, _, _ := section(ch.ModuleConfig.ProtoReflect(), (&pb.LocalModuleConfig{}).ProtoReflect())
			w.Modules = append(w.Modules, name)
		case ch.Channel != nil:
			w.Channels = true
		case ch.Owner != nil:
			w.Owner = true
		case ch.FixedPosition != nil:
			w.Config = append(w.Config, "position")
		}
	}
	slices.Sort(w.Config)
	slices.Sort(w.Modules)
	w.Config, w.Modules = slices.Compact(w.Config), slices.Compact(w.Modules)
	return w
}

func (t remoteTarget) current(ctx context.Context, changes []Change) (*State, error) {
	return t.r.Fetch(ctx, want(changes))
}

func (t remoteTarget) ensurePasskey(ctx context.Context) error {
	return t.r.c.ensurePasskeyFor(ctx, t.r.num)
}

func (t remoteTarget) post(ctx context.Context, m *pb.AdminMessage) error {
	return t.r.c.remotePost(ctx, t.r.num, m)
}

// settle gives the node RebootGrace, then reads back what changed, retrying
// while the node does not answer (it may be rebooting) until
// ReconnectTimeout.
func (t remoteTarget) settle(ctx context.Context, changes []Change) (*State, error) {
	c := t.r.c
	if err := sleep(ctx, c.opts.RebootGrace); err != nil {
		return nil, err
	}
	rctx, cancel := context.WithTimeout(ctx, c.opts.ReconnectTimeout)
	defer cancel()
	delay := c.opts.ReconnectDelay
	for {
		st, err := t.r.Fetch(rctx, want(changes))
		if err == nil {
			return st, nil
		}
		var re *RoutingError
		if !errors.Is(err, ErrNoAnswer) && !errors.As(err, &re) && rctx.Err() == nil {
			return nil, err
		}
		c.setPasskey(t.r.num, nil) // a rebooted node has a new session key
		if err := sleep(rctx, delay); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("!%08x did not answer within %s: %w", t.r.num, c.opts.ReconnectTimeout, err)
		}
		delay = min(delay*2, maxReconnectBackoff)
	}
}

// sleep waits d or until ctx ends.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// keysSafe reports whether the node keeps its keypair when its security
// section is written without the private key (which a remote node never
// sends): firmware 2.8 and newer. Older firmware makes a new keypair (2.7)
// or stores the empty key (2.5, 2.6).
func (r *Remote) keysSafe() bool {
	maj, min, ok := parseFirmware(r.meta.GetFirmwareVersion())
	return ok && (maj > 2 || maj == 2 && min >= 8)
}

// Apply writes the changes in one edit transaction over the mesh and
// verifies them by reading them back. A fixed position is verified by
// position.fixed_position only: it then returns the count with
// ErrCoordinatesUnverified.
func (r *Remote) Apply(ctx context.Context, changes []Change) (int, error) {
	for _, ch := range changes {
		if ch.Config.GetSecurity() != nil && !r.keysSafe() {
			return 0, fmt.Errorf("!%08x runs firmware %s; firmware older than 2.8 would replace or clear its keys when "+
				"the security section is written remotely — change it over a local connection", r.num, r.meta.GetFirmwareVersion())
		}
	}
	n, err := r.c.apply(ctx, remoteTarget{r}, changes)
	if err != nil {
		return n, err
	}
	for _, ch := range changes {
		if ch.FixedPosition != nil && n > 0 {
			return n, ErrCoordinatesUnverified
		}
	}
	return n, nil
}

func (r *Remote) post(ctx context.Context, m *pb.AdminMessage) error {
	return r.c.remotePost(ctx, r.num, m)
}

// Reboot asks the node to reboot after seconds.
func (r *Remote) Reboot(ctx context.Context, seconds int32) error {
	return r.post(ctx, &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_RebootSeconds{RebootSeconds: seconds}})
}

// Shutdown asks the node to power off after seconds.
func (r *Remote) Shutdown(ctx context.Context, seconds int32) error {
	return r.post(ctx, &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_ShutdownSeconds{ShutdownSeconds: seconds}})
}

// FactoryReset erases the node's config (full: also its device state).
func (r *Remote) FactoryReset(ctx context.Context, full bool) error {
	m := &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_FactoryResetConfig{FactoryResetConfig: 1}}
	if full {
		m.PayloadVariant = &pb.AdminMessage_FactoryResetDevice{FactoryResetDevice: 1}
	}
	return r.post(ctx, m)
}

// EditNodeDB applies op to node num in the remote node's node list. A
// remote node list cannot be read, so success returns ErrNotVerifiable.
func (r *Remote) EditNodeDB(ctx context.Context, op NodeOp, num uint32) error {
	if num == r.num {
		return fmt.Errorf("cannot %s a node in its own node list", op)
	}
	if err := r.post(ctx, op.admin(num)); err != nil {
		return err
	}
	return ErrNotVerifiable
}

// ResetNodeDB clears the remote node's node list (it reboots). Success
// returns ErrNotVerifiable.
func (r *Remote) ResetNodeDB(ctx context.Context, keepFavorites bool) error {
	if err := r.post(ctx, &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_NodedbReset{NodedbReset: keepFavorites}}); err != nil {
		return err
	}
	return ErrNotVerifiable
}

// Package backup writes and reads node configuration backups in YAML that is
// structurally compatible with the Python CLI's --export-config / --configure.
package backup

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"sigs.k8s.io/yaml"

	"meshtastik/internal/channelurl"
	"meshtastik/internal/device"
)

const (
	header       = "# start of Meshtastic configure yaml\n"
	base64Prefix = "base64:"
)

// Location is a fixed position as written by the Python CLI.
type Location struct {
	Lat, Lon float64
	Alt      int32
}

// Doc is a parsed backup file.
type Doc struct {
	Owner, OwnerShort, ChannelURL string
	Location                      *Location
	// Unsupported lists top-level keys we recognise but do not apply
	// (canned_messages, ringtone).
	Unsupported []string

	config, moduleConfig map[string]any
}

// Export renders the node state as a backup. Fields are emitted even when
// unset, so the backup is complete; every bytes field is prefixed "base64:".
func Export(s *device.State) ([]byte, error) {
	out := []byte(header)
	add := func(key string, v any) error {
		b, err := yaml.Marshal(map[string]any{key: v})
		out = append(out, b...)
		return err
	}
	if self := s.Self(); self != nil {
		if err := add("owner", self.GetUser().GetLongName()); err != nil {
			return nil, err
		}
		if err := add("owner_short", self.GetUser().GetShortName()); err != nil {
			return nil, err
		}
	}
	if hasChannels(s.Channels) {
		u, err := channelurl.Encode(s.Channels, s.Config.GetLora())
		if err != nil {
			return nil, err
		}
		if err := add("channel_url", u); err != nil {
			return nil, err
		}
	}
	if s.Config.GetPosition().GetFixedPosition() {
		p := s.Self().GetPosition()
		loc := map[string]any{"lat": float64(p.GetLatitudeI()) / 1e7, "lon": float64(p.GetLongitudeI()) / 1e7, "alt": p.GetAltitude()}
		if err := add("location", loc); err != nil {
			return nil, err
		}
	}
	cfg, err := marshalSections(s.Config)
	if err != nil {
		return nil, err
	}
	mod, err := marshalSections(s.ModuleConfig)
	if err != nil {
		return nil, err
	}
	if err := add("config", cfg); err != nil {
		return nil, err
	}
	if err := add("module_config", mod); err != nil {
		return nil, err
	}
	return out, nil
}

// hasChannels reports whether any channel is in use (Encode refuses none).
func hasChannels(chs [8]*pb.Channel) bool {
	for _, c := range chs {
		if r := c.GetRole(); r == pb.Channel_PRIMARY || r == pb.Channel_SECONDARY {
			return true
		}
	}
	return false
}

// marshalSections converts a Local* message to {snake_section: {camelField: v}},
// skipping unset sections.
func marshalSections(m proto.Message) (map[string]any, error) {
	raw, err := protojson.MarshalOptions{EmitUnpopulated: true}.Marshal(m)
	if err != nil {
		return nil, err
	}
	var byJSON map[string]any
	if err := json.Unmarshal(raw, &byJSON); err != nil {
		return nil, err
	}
	out := map[string]any{}
	fields := m.ProtoReflect().Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		f := fields.Get(i)
		if f.Kind() != protoreflect.MessageKind {
			continue
		}
		obj, ok := byJSON[f.JSONName()].(map[string]any)
		if !ok {
			continue
		}
		prefixBytes(obj, f.Message())
		out[f.TextName()] = obj
	}
	return out, nil
}

// prefixBytes adds "base64:" to every bytes value under obj, at any depth.
func prefixBytes(obj map[string]any, d protoreflect.MessageDescriptor) {
	for i := 0; i < d.Fields().Len(); i++ {
		f := d.Fields().Get(i)
		v, ok := obj[f.JSONName()]
		if !ok {
			continue
		}
		switch {
		case f.Kind() == protoreflect.BytesKind:
			if l, ok := v.([]any); ok {
				for j, e := range l {
					if s, ok := e.(string); ok {
						l[j] = base64Prefix + s
					}
				}
			} else if s, ok := v.(string); ok {
				obj[f.JSONName()] = base64Prefix + s
			}
		case f.Message() != nil && !f.IsMap():
			switch t := v.(type) {
			case map[string]any:
				prefixBytes(t, f.Message())
			case []any:
				for _, e := range t {
					if m, ok := e.(map[string]any); ok {
						prefixBytes(m, f.Message())
					}
				}
			}
		}
	}
}

// Read parses a backup. Unknown top-level keys are an error, except the
// recognised-but-unsupported ones, which are listed in Doc.Unsupported.
func Read(data []byte) (*Doc, error) {
	j, err := yaml.YAMLToJSON(data)
	if err != nil {
		return nil, fmt.Errorf("backup: %w", err)
	}
	var top map[string]any
	if err := json.Unmarshal(j, &top); err != nil {
		return nil, fmt.Errorf("backup: %w", err)
	}
	d := &Doc{}
	keys := make([]string, 0, len(top))
	for k := range top {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		v := top[k]
		var err error
		switch k {
		case "owner":
			d.Owner, err = asString(k, v)
		case "owner_short":
			d.OwnerShort, err = asString(k, v)
		case "channel_url":
			d.ChannelURL, err = asString(k, v)
		case "location":
			d.Location, err = asLocation(v)
		case "config":
			d.config, err = asMap(k, v)
		case "module_config":
			d.moduleConfig, err = asMap(k, v)
		case "canned_messages", "ringtone":
			d.Unsupported = append(d.Unsupported, k)
		default:
			err = fmt.Errorf("backup: unknown top-level key %q", k)
		}
		if err != nil {
			return nil, err
		}
	}
	slices.Sort(d.Unsupported)
	return d, nil
}

func asString(key string, v any) (string, error) {
	if v == nil {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("backup: %s must be a string", key)
	}
	return s, nil
}

func asMap(key string, v any) (map[string]any, error) {
	if v == nil {
		return nil, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("backup: %s must be a mapping", key)
	}
	return m, nil
}

func asLocation(v any) (*Location, error) {
	m, err := asMap("location", v)
	if err != nil || m == nil {
		return nil, err
	}
	var loc Location
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		n, ok := m[k].(float64)
		if !ok {
			return nil, fmt.Errorf("backup: location.%s must be a number", k)
		}
		switch k {
		case "lat":
			if !(n >= -90 && n <= 90) {
				return nil, fmt.Errorf("backup: location.lat %v out of range (-90..90)", n)
			}
			loc.Lat = n
		case "lon":
			if !(n >= -180 && n <= 180) {
				return nil, fmt.Errorf("backup: location.lon %v out of range (-180..180)", n)
			}
			loc.Lon = n
		case "alt":
			loc.Alt = int32(n)
		default:
			return nil, fmt.Errorf("backup: unknown location key %q", k)
		}
	}
	return &loc, nil
}

// Skip is a field present in the file that was not applied, and why.
type Skip struct{ Path, Reason string }

// MergeConfig overlays the file's config sections onto a clone of base. Only
// keys present in the file overwrite base; absent keys keep it. A nested
// message inside a section (e.g. network.ipv4_config) is replaced as a whole
// when present. security.private_key and security.public_key are not applied
// unless withKeys; they are listed in skipped when present with a value. Even
// with withKeys, neither is applied when either is present but not 32 bytes,
// since the firmware would then generate a new key pair (a new identity).
func (d *Doc) MergeConfig(base *pb.LocalConfig, withKeys bool) (*pb.LocalConfig, []Skip, error) {
	out := proto.Clone(base).(*pb.LocalConfig)
	skipped, err := merge(out.ProtoReflect(), d.config, withKeys)
	return out, skipped, err
}

// MergeModuleConfig overlays the file's module_config sections onto a clone of
// base, with the same presence semantics as MergeConfig.
func (d *Doc) MergeModuleConfig(base *pb.LocalModuleConfig) (*pb.LocalModuleConfig, error) {
	out := proto.Clone(base).(*pb.LocalModuleConfig)
	_, err := merge(out.ProtoReflect(), d.moduleConfig, true)
	return out, err
}

func findField(d protoreflect.MessageDescriptor, name string) protoreflect.FieldDescriptor {
	fields := d.Fields()
	for i := 0; i < fields.Len(); i++ {
		if f := fields.Get(i); f.TextName() == name || f.JSONName() == name {
			return f
		}
	}
	return nil
}

// isKey reports whether f is security.private_key or security.public_key.
func isKey(section string, f protoreflect.FieldDescriptor) bool {
	return section == "security" && (f.TextName() == "private_key" || f.TextName() == "public_key")
}

// keyLen returns the decoded length of a base64 bytes value from the file;
// ok is false when v is not a decodable string (protojson reports that).
func keyLen(v any) (n int, ok bool) {
	s, isStr := v.(string)
	if !isStr {
		return 0, false
	}
	s = strings.TrimRight(strings.TrimPrefix(s, base64Prefix), "=")
	for _, enc := range []*base64.Encoding{base64.RawStdEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return len(b), true
		}
	}
	return 0, false
}

// keySkips decides which key fields of the security section fieldsIn are
// not applied: the reason, or "" to skip silently.
func keySkips(sec protoreflect.MessageDescriptor, fieldsIn map[string]any, withKeys bool) map[string]string {
	present := map[string]any{}
	for k, v := range fieldsIn {
		if f := findField(sec, k); f != nil && isKey("security", f) {
			present[f.TextName()] = v
		}
	}
	out := map[string]string{}
	if !withKeys {
		for name, v := range present {
			out[name] = ""
			if s, _ := v.(string); strings.TrimPrefix(s, base64Prefix) != "" {
				out[name] = "pass --keys to apply"
			}
		}
		return out
	}
	const newIdentity = "would make the node generate a new identity"
	for name, v := range present {
		switch n, ok := keyLen(v); {
		case !ok || n == 32:
		case n == 0:
			out[name] = "empty in file; " + newIdentity
		default:
			out[name] = fmt.Sprintf("%d bytes in file, not 32; %s", n, newIdentity)
		}
	}
	if len(out) == 0 {
		return out
	}
	for name := range present {
		if _, bad := out[name]; !bad {
			other := "public_key"
			if name == "public_key" {
				other = "private_key"
			}
			out[name] = "not applied without a valid security." + other
		}
	}
	return out
}

func merge(dst protoreflect.Message, sections map[string]any, withKeys bool) (skipped []Skip, err error) {
	names := make([]string, 0, len(sections))
	for k := range sections {
		names = append(names, k)
	}
	slices.Sort(names)
	for _, name := range names {
		sf := findField(dst.Descriptor(), name)
		if sf == nil || sf.Kind() != protoreflect.MessageKind {
			return nil, fmt.Errorf("unknown section %q", name)
		}
		if sections[name] == nil {
			continue
		}
		fieldsIn, ok := sections[name].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("section %q must be a mapping", name)
		}
		sec := dst.Mutable(sf).Message()
		var skipKey map[string]string
		if sf.TextName() == "security" {
			skipKey = keySkips(sec.Descriptor(), fieldsIn, withKeys)
		}
		keys := make([]string, 0, len(fieldsIn))
		for k := range fieldsIn {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, key := range keys {
			ff := findField(sec.Descriptor(), key)
			if ff == nil {
				return nil, fmt.Errorf("unknown field %q in section %q", key, name)
			}
			val := fieldsIn[key]
			path := sf.TextName() + "." + ff.TextName()
			if reason, skip := skipKey[ff.TextName()]; skip && isKey(sf.TextName(), ff) {
				if reason != "" {
					skipped = append(skipped, Skip{Path: path, Reason: reason})
				}
				continue
			}
			if val == nil {
				// null is what EmitUnpopulated writes for an unset nested message.
				if ff.Message() == nil || ff.IsList() || ff.IsMap() {
					return nil, fmt.Errorf("%s: null is only valid for a nested message", path)
				}
				sec.Clear(ff)
				continue
			}
			stripBytes(&val, ff)
			raw, err := json.Marshal(map[string]any{ff.JSONName(): val})
			if err != nil {
				return nil, err
			}
			fresh := sec.New()
			if err := protojson.Unmarshal(raw, fresh.Interface()); err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			if ff.Message() != nil && !ff.IsList() && !ff.IsMap() && !fresh.Has(ff) {
				continue
			}
			setField(sec, fresh, ff)
		}
	}
	return skipped, nil
}

// stripBytes removes the "base64:" prefix from every bytes value under *v,
// which holds the value of field f.
func stripBytes(v *any, f protoreflect.FieldDescriptor) {
	switch t := (*v).(type) {
	case string:
		if f.Kind() == protoreflect.BytesKind {
			*v = strings.TrimPrefix(t, base64Prefix)
		}
	case []any:
		for i := range t {
			stripBytes(&t[i], f)
		}
	case map[string]any:
		if f.Message() == nil || f.IsMap() {
			return
		}
		for k, e := range t {
			if nf := findField(f.Message(), k); nf != nil {
				stripBytes(&e, nf)
				t[k] = e
			}
		}
	}
}

// setField copies field f from src onto dst, replacing any existing value.
func setField(dst, src protoreflect.Message, f protoreflect.FieldDescriptor) {
	switch {
	case f.IsList():
		dst.Clear(f)
		l, from := dst.Mutable(f).List(), src.Get(f).List()
		for i := 0; i < from.Len(); i++ {
			l.Append(from.Get(i))
		}
	case f.IsMap():
		dst.Clear(f)
		m := dst.Mutable(f).Map()
		src.Get(f).Map().Range(func(k protoreflect.MapKey, v protoreflect.Value) bool {
			m.Set(k, v)
			return true
		})
	default:
		dst.Set(f, src.Get(f))
	}
}

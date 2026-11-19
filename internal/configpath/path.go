package configpath

import (
	"fmt"
	"slices"
	"strings"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// Path addresses a config section, or a field inside it.
type Path struct {
	Module         bool
	Section, Field string // Field may be dotted ("ipv4_config.ip") or empty
}

func (p Path) String() string {
	if p.Field == "" {
		return p.Section
	}
	return p.Section + "." + p.Field
}

// fieldByName finds a field by snake_case name or camelCase JSON name.
func fieldByName(d protoreflect.MessageDescriptor, name string) protoreflect.FieldDescriptor {
	if f := d.Fields().ByName(protoreflect.Name(name)); f != nil {
		return f
	}
	if f := d.Fields().ByJSONName(name); f != nil {
		return f
	}
	return nil
}

func fieldNames(d protoreflect.MessageDescriptor) []string {
	var out []string
	for i := 0; i < d.Fields().Len(); i++ {
		out = append(out, string(d.Fields().Get(i).Name()))
	}
	return out
}

// resolve walks a dotted field name from d and returns the canonical
// (snake_case) names and descriptors along the way.
func resolve(d protoreflect.MessageDescriptor, dotted string) ([]protoreflect.FieldDescriptor, error) {
	var out []protoreflect.FieldDescriptor
	parts := strings.Split(dotted, ".")
	for i, part := range parts {
		f := fieldByName(d, part)
		if f == nil {
			return nil, fmt.Errorf("unknown field %q in %s (valid: %s)", part, d.Name(), strings.Join(fieldNames(d), ", "))
		}
		out = append(out, f)
		if i < len(parts)-1 {
			if f.Kind() != protoreflect.MessageKind || f.IsList() || f.IsMap() {
				return nil, fmt.Errorf("field %q is not a message; cannot look up %q inside it", f.Name(), strings.Join(parts[i+1:], "."))
			}
			d = f.Message()
		}
	}
	return out, nil
}

// Parse reads "section" or "section.field[.sub...]" with snake_case or camelCase names.
func Parse(s string) (Path, error) {
	section, field, _ := strings.Cut(s, ".")
	var p Path
	var d protoreflect.MessageDescriptor
	switch {
	case slices.Contains(sectionNames(configDesc()), section):
		d = configDesc().Fields().ByName(protoreflect.Name(section)).Message()
	case slices.Contains(sectionNames(moduleDesc()), section):
		p.Module = true
		d = moduleDesc().Fields().ByName(protoreflect.Name(section)).Message()
	default:
		return Path{}, fmt.Errorf("unknown section %q (valid: %s)", section, strings.Join(Sections(), ", "))
	}
	p.Section = section
	if field == "" {
		return p, nil
	}
	fds, err := resolve(d, field)
	if err != nil {
		return Path{}, err
	}
	names := make([]string, len(fds))
	for i, f := range fds {
		names[i] = string(f.Name())
	}
	p.Field = strings.Join(names, ".")
	return p, nil
}

// Section returns the section message addressed by p, allocating it if unset.
func Section(cfg *pb.LocalConfig, mod *pb.LocalModuleConfig, p Path) protoreflect.Message {
	var local protoreflect.Message
	if p.Module {
		local = mod.ProtoReflect()
	} else {
		local = cfg.ProtoReflect()
	}
	return local.Mutable(local.Descriptor().Fields().ByName(protoreflect.Name(p.Section))).Message()
}

// Set parses raw and assigns it to the field addressed by p.
func Set(cfg *pb.LocalConfig, mod *pb.LocalModuleConfig, p Path, raw string) error {
	if p.Field == "" {
		return fmt.Errorf("%s is a section; set one of its fields", p.Section)
	}
	// Validate against a scratch message so a bad value leaves a nil section nil.
	local := configDesc()
	if p.Module {
		local = moduleDesc()
	}
	scratch := dynamicpb.NewMessage(local.Fields().ByName(protoreflect.Name(p.Section)).Message())
	if err := SetField(scratch, p.Field, raw); err != nil {
		return err
	}
	return SetField(Section(cfg, mod, p), p.Field, raw)
}

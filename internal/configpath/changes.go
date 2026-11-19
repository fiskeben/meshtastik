package configpath

import (
	"fmt"
	"slices"
	"strings"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// changedSections returns a Local* message holding only the populated
// sections of after that differ from before.
func changedSections(before, after protoreflect.Message) protoreflect.Message {
	out := after.New()
	fields := after.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		f := fields.Get(i)
		if f.Kind() != protoreflect.MessageKind || !after.Has(f) {
			continue
		}
		if before.Has(f) && proto.Equal(before.Get(f).Message().Interface(), after.Get(f).Message().Interface()) {
			continue
		}
		out.Set(f, after.Get(f))
	}
	return out
}

// Changes returns one wrapper per section that is populated in the after
// configs and differs from the before configs.
func Changes(beforeCfg, afterCfg *pb.LocalConfig, beforeMod, afterMod *pb.LocalModuleConfig) ([]*pb.Config, []*pb.ModuleConfig) {
	c := changedSections(beforeCfg.ProtoReflect(), afterCfg.ProtoReflect()).Interface().(*pb.LocalConfig)
	m := changedSections(beforeMod.ProtoReflect(), afterMod.ProtoReflect()).Interface().(*pb.LocalModuleConfig)
	return ConfigSections(c), ModuleSections(m)
}

// FieldDiff is one leaf field whose wanted and actual values differ.
type FieldDiff struct{ Path, Want, Got string }

// FieldDiffs compares want against got and reports differing leaf fields,
// recursing into nested messages. Repeated fields are compared whole.
func FieldDiffs(prefix string, want, got protoreflect.Message) []FieldDiff {
	var out []FieldDiff
	fields := want.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		f := fields.Get(i)
		path := string(f.Name())
		if prefix != "" {
			path = prefix + "." + path
		}
		if f.Kind() == protoreflect.MessageKind && !f.IsList() && !f.IsMap() {
			out = append(out, FieldDiffs(path, want.Get(f).Message(), got.Get(f).Message())...)
			continue
		}
		w, g := want.Get(f), got.Get(f)
		if f.IsMap() || f.Kind() == protoreflect.MessageKind {
			if ws, gs := formatOpaque(f, w), formatOpaque(f, g); ws != gs {
				out = append(out, FieldDiff{path, ws, gs})
			}
			continue
		}
		if ws, gs := formatValue(f, w), formatValue(f, g); ws != gs {
			out = append(out, FieldDiff{path, ws, gs})
		}
	}
	return out
}

// formatOpaque renders repeated-message and map values deterministically:
// elements via compact prototext joined by ",", map entries sorted by key.
func formatOpaque(f protoreflect.FieldDescriptor, v protoreflect.Value) string {
	opts := prototext.MarshalOptions{Multiline: false}
	msg := func(m protoreflect.Message) string {
		// Normalise prototext's deliberately unstable spacing.
		return strings.Join(strings.Fields(opts.Format(m.Interface())), " ")
	}
	var parts []string
	switch {
	case f.IsList():
		l := v.List()
		for i := 0; i < l.Len(); i++ {
			parts = append(parts, msg(l.Get(i).Message()))
		}
	case f.IsMap():
		v.Map().Range(func(k protoreflect.MapKey, mv protoreflect.Value) bool {
			val := fmt.Sprint(mv.Interface())
			if f.MapValue().Message() != nil {
				val = msg(mv.Message())
			}
			parts = append(parts, fmt.Sprint(k.Interface())+"="+val)
			return true
		})
		slices.Sort(parts)
	}
	return strings.Join(parts, ",")
}

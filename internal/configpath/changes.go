package configpath

import (
	"fmt"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
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
			if !want.Has(f) && !got.Has(f) {
				continue
			}
			ws, gs := formatOpaque(w), formatOpaque(g)
			if ws != gs {
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

// formatOpaque renders map and repeated-message values for diff output.
func formatOpaque(v protoreflect.Value) string {
	return fmt.Sprint(v.Interface())
}

// Package configpath maps dotted config paths ("lora.hop_limit") onto the
// Meshtastic protobufs using protoreflect. It performs no I/O.
package configpath

import (
	"slices"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func configDesc() protoreflect.MessageDescriptor {
	return (&pb.LocalConfig{}).ProtoReflect().Descriptor()
}

func moduleDesc() protoreflect.MessageDescriptor {
	return (&pb.LocalModuleConfig{}).ProtoReflect().Descriptor()
}

// sectionNames lists the message-typed fields of a Local* descriptor.
func sectionNames(d protoreflect.MessageDescriptor) []string {
	var out []string
	for i := 0; i < d.Fields().Len(); i++ {
		if f := d.Fields().Get(i); f.Kind() == protoreflect.MessageKind {
			out = append(out, string(f.Name()))
		}
	}
	return out
}

// ConfigSectionNames returns the section names of LocalConfig ("device", "lora", …).
func ConfigSectionNames() []string { return sectionNames(configDesc()) }

// ModuleSectionNames returns the section names of LocalModuleConfig ("mqtt", …).
func ModuleSectionNames() []string { return sectionNames(moduleDesc()) }

// Sections returns every section name of LocalConfig and LocalModuleConfig, sorted.
func Sections() []string {
	s := append(sectionNames(configDesc()), sectionNames(moduleDesc())...)
	slices.Sort(s)
	return s
}

// mergeSection copies the oneof section carried by wrapper into dst.
func mergeSection(dst, wrapper protoreflect.Message) {
	wrapper.Range(func(f protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if f.ContainingOneof() == nil || f.Message() == nil {
			return true
		}
		if df := dst.Descriptor().Fields().ByName(f.Name()); df != nil {
			dst.Set(df, protoreflect.ValueOfMessage(proto.Clone(v.Message().Interface()).ProtoReflect()))
		}
		return true
	})
}

// wrapSections builds one wrapper message per populated section of local.
func wrapSections[W proto.Message](local protoreflect.Message, newWrapper func() W) []W {
	var out []W
	fields := local.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		f := fields.Get(i)
		if f.Kind() != protoreflect.MessageKind || !local.Has(f) {
			continue
		}
		w := newWrapper()
		wm := w.ProtoReflect()
		wf := wm.Descriptor().Fields().ByName(f.Name())
		if wf == nil {
			continue
		}
		wm.Set(wf, protoreflect.ValueOfMessage(proto.Clone(local.Get(f).Message().Interface()).ProtoReflect()))
		out = append(out, w)
	}
	return out
}

// MergeConfig sets the section carried by c on dst.
func MergeConfig(dst *pb.LocalConfig, c *pb.Config) {
	mergeSection(dst.ProtoReflect(), c.ProtoReflect())
}

// MergeModuleConfig sets the section carried by c on dst.
func MergeModuleConfig(dst *pb.LocalModuleConfig, c *pb.ModuleConfig) {
	mergeSection(dst.ProtoReflect(), c.ProtoReflect())
}

// ConfigSections returns one Config per populated section of l.
func ConfigSections(l *pb.LocalConfig) []*pb.Config {
	return wrapSections(l.ProtoReflect(), func() *pb.Config { return &pb.Config{} })
}

// ModuleSections returns one ModuleConfig per populated section of l.
func ModuleSections(l *pb.LocalModuleConfig) []*pb.ModuleConfig {
	return wrapSections(l.ProtoReflect(), func() *pb.ModuleConfig { return &pb.ModuleConfig{} })
}

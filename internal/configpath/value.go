package configpath

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// walk follows a dotted field name from m, allocating intermediate messages
// when mutable is true. It returns the owning message and the leaf field.
func walk(m protoreflect.Message, dotted string, mutable bool) (protoreflect.Message, protoreflect.FieldDescriptor, error) {
	fds, err := resolve(m.Descriptor(), dotted)
	if err != nil {
		return nil, nil, err
	}
	for _, f := range fds[:len(fds)-1] {
		if mutable {
			m = m.Mutable(f).Message()
		} else {
			m = m.Get(f).Message()
		}
	}
	return m, fds[len(fds)-1], nil
}

// SetField parses raw and assigns it to the (possibly dotted) field of m.
func SetField(m protoreflect.Message, field, raw string) error {
	owner, f, err := walk(m, field, true)
	if err != nil {
		return err
	}
	if f.IsMap() || f.Kind() == protoreflect.MessageKind || f.Kind() == protoreflect.GroupKind {
		return fmt.Errorf("field %q is a message; set one of its fields", f.Name())
	}
	if f.IsList() {
		list := owner.Mutable(f).List()
		list.Truncate(0)
		if strings.TrimSpace(raw) == "" {
			return nil
		}
		items := strings.Split(raw, ",")
		vals := make([]protoreflect.Value, len(items))
		for i, it := range items {
			if vals[i], err = parseScalar(f, strings.TrimSpace(it)); err != nil {
				return fmt.Errorf("%s: %w", f.Name(), err)
			}
		}
		for _, v := range vals {
			list.Append(v)
		}
		return nil
	}
	v, err := parseScalar(f, raw)
	if err != nil {
		return fmt.Errorf("%s: %w", f.Name(), err)
	}
	owner.Set(f, v)
	return nil
}

func parseScalar(f protoreflect.FieldDescriptor, raw string) (protoreflect.Value, error) {
	switch f.Kind() {
	case protoreflect.BoolKind:
		switch strings.ToLower(raw) {
		case "true", "yes", "on", "1":
			return protoreflect.ValueOfBool(true), nil
		case "false", "no", "off", "0":
			return protoreflect.ValueOfBool(false), nil
		}
		return protoreflect.Value{}, fmt.Errorf("invalid bool %q (use true|false|yes|no|on|off|1|0)", raw)
	case protoreflect.EnumKind:
		vals := f.Enum().Values()
		var names []string
		for i := 0; i < vals.Len(); i++ {
			ev := vals.Get(i)
			if strings.EqualFold(string(ev.Name()), raw) {
				return protoreflect.ValueOfEnum(ev.Number()), nil
			}
			names = append(names, string(ev.Name()))
		}
		return protoreflect.Value{}, fmt.Errorf("invalid value %q (options: %s)", raw, strings.Join(names, ", "))
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		n, err := strconv.ParseInt(raw, 10, 32)
		return protoreflect.ValueOfInt32(int32(n)), err
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		n, err := strconv.ParseInt(raw, 10, 64)
		return protoreflect.ValueOfInt64(n), err
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		n, err := strconv.ParseUint(raw, 10, 32)
		return protoreflect.ValueOfUint32(uint32(n)), err
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		n, err := strconv.ParseUint(raw, 10, 64)
		return protoreflect.ValueOfUint64(n), err
	case protoreflect.FloatKind:
		n, err := strconv.ParseFloat(raw, 32)
		return protoreflect.ValueOfFloat32(float32(n)), err
	case protoreflect.DoubleKind:
		n, err := strconv.ParseFloat(raw, 64)
		return protoreflect.ValueOfFloat64(n), err
	case protoreflect.StringKind:
		return protoreflect.ValueOfString(raw), nil
	case protoreflect.BytesKind:
		enc, ok := strings.CutPrefix(raw, "base64:")
		if !ok {
			return protoreflect.Value{}, fmt.Errorf("bytes must be written as base64:<data>, got %q", raw)
		}
		b, err := base64.StdEncoding.DecodeString(enc)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("invalid base64: %w", err)
		}
		return protoreflect.ValueOfBytes(b), nil
	}
	return protoreflect.Value{}, fmt.Errorf("unsupported field kind %s", f.Kind())
}

func formatScalar(f protoreflect.FieldDescriptor, v protoreflect.Value) string {
	switch f.Kind() {
	case protoreflect.EnumKind:
		if ev := f.Enum().Values().ByNumber(v.Enum()); ev != nil {
			return string(ev.Name())
		}
		return strconv.Itoa(int(v.Enum()))
	case protoreflect.BytesKind:
		return "base64:" + base64.StdEncoding.EncodeToString(v.Bytes())
	}
	return fmt.Sprint(v.Interface())
}

func formatValue(f protoreflect.FieldDescriptor, v protoreflect.Value) string {
	if f.IsList() {
		l := v.List()
		parts := make([]string, l.Len())
		for i := range parts {
			parts[i] = formatScalar(f, l.Get(i))
		}
		return strings.Join(parts, ",")
	}
	return formatScalar(f, v)
}

// FormatField renders the (possibly dotted) field of m in the syntax SetField accepts.
func FormatField(m protoreflect.Message, field string) (string, error) {
	owner, f, err := walk(m, field, false)
	if err != nil {
		return "", err
	}
	if f.IsMap() || f.Kind() == protoreflect.MessageKind || f.Kind() == protoreflect.GroupKind {
		return "", fmt.Errorf("field %q is a message; format one of its fields", f.Name())
	}
	return formatValue(f, owner.Get(f)), nil
}

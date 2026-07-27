// Reads settings field metadata straight from proto descriptors
package protometa

import (
	"strconv"
	"sync"

	optionsv1 "github.com/discohaus/discopanel/pkg/proto/discopanel/options/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// One settings column with its ui metadata
type Prop struct {
	Field protoreflect.FieldDescriptor
	Meta  *optionsv1.PropField
}

var (
	propMu    sync.RWMutex
	propCache = map[protoreflect.FullName][]Prop{}
)

// Prop annotated fields of one message in declaration order
func Props(md protoreflect.MessageDescriptor) []Prop {
	propMu.RLock()
	props, ok := propCache[md.FullName()]
	propMu.RUnlock()
	if ok {
		return props
	}
	fields := md.Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if ext := proto.GetExtension(fd.Options(), optionsv1.E_Prop).(*optionsv1.PropField); ext != nil {
			props = append(props, Prop{Field: fd, Meta: ext})
		}
	}
	propMu.Lock()
	propCache[md.FullName()] = props
	propMu.Unlock()
	return props
}

// String form of one scalar field, false when unset
func ScalarString(m protoreflect.Message, fd protoreflect.FieldDescriptor) (string, bool) {
	if fd.HasPresence() && !m.Has(fd) {
		return "", false
	}
	v := m.Get(fd)
	switch fd.Kind() {
	case protoreflect.StringKind:
		return v.String(), true
	case protoreflect.BoolKind:
		return strconv.FormatBool(v.Bool()), true
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return strconv.FormatInt(v.Int(), 10), true
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind,
		protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return strconv.FormatUint(v.Uint(), 10), true
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return strconv.FormatFloat(v.Float(), 'g', -1, 64), true
	case protoreflect.EnumKind:
		if vd := fd.Enum().Values().ByNumber(v.Enum()); vd != nil {
			return string(vd.Name()), true
		}
		return strconv.FormatInt(int64(v.Enum()), 10), true
	}
	return "", false
}

// Parses a string into one scalar field, empty clears it
func SetScalarString(m protoreflect.Message, fd protoreflect.FieldDescriptor, s string) error {
	if s == "" {
		m.Clear(fd)
		return nil
	}
	switch fd.Kind() {
	case protoreflect.StringKind:
		m.Set(fd, protoreflect.ValueOfString(s))
	case protoreflect.BoolKind:
		b, err := strconv.ParseBool(s)
		if err != nil {
			return err
		}
		m.Set(fd, protoreflect.ValueOfBool(b))
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		i, err := strconv.ParseInt(s, 10, 32)
		if err != nil {
			return err
		}
		m.Set(fd, protoreflect.ValueOfInt32(int32(i)))
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		i, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return err
		}
		m.Set(fd, protoreflect.ValueOfInt64(i))
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		u, err := strconv.ParseUint(s, 10, 32)
		if err != nil {
			return err
		}
		m.Set(fd, protoreflect.ValueOfUint32(uint32(u)))
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		u, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return err
		}
		m.Set(fd, protoreflect.ValueOfUint64(u))
	case protoreflect.FloatKind:
		f, err := strconv.ParseFloat(s, 32)
		if err != nil {
			return err
		}
		m.Set(fd, protoreflect.ValueOfFloat32(float32(f)))
	case protoreflect.DoubleKind:
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return err
		}
		m.Set(fd, protoreflect.ValueOfFloat64(f))
	}
	return nil
}

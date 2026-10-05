// Reads enum display metadata straight from proto descriptors
package protometa

import (
	"strings"
	"sync"

	optionsv1 "github.com/discohaus/discopanel/pkg/proto/discopanel/options/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Resolved strings for one enum value
type valueMeta struct {
	name  string
	label string
	desc  string
}

// Resolved strings for one enum type
type enumMeta struct {
	label  string
	values map[protoreflect.EnumNumber]valueMeta
	byName map[string]protoreflect.EnumNumber
}

var (
	mu    sync.RWMutex
	cache = map[protoreflect.FullName]*enumMeta{}
)

// Cached metadata table for one enum descriptor
func metaFor(ed protoreflect.EnumDescriptor) *enumMeta {
	mu.RLock()
	m, ok := cache[ed.FullName()]
	mu.RUnlock()
	if ok {
		return m
	}
	m = buildMeta(ed)
	mu.Lock()
	cache[ed.FullName()] = m
	mu.Unlock()
	return m
}

// Builds the table from descriptor annotations
func buildMeta(ed protoreflect.EnumDescriptor) *enumMeta {
	m := &enumMeta{
		values: map[protoreflect.EnumNumber]valueMeta{},
		byName: map[string]protoreflect.EnumNumber{},
	}
	if et := proto.GetExtension(ed.Options(), optionsv1.E_EnumType).(*optionsv1.EnumType); et != nil {
		m.label = et.Label
	}
	prefix := screamingPrefix(string(ed.Name()))
	for i := 0; i < ed.Values().Len(); i++ {
		vd := ed.Values().Get(i)
		v := valueMeta{name: strings.ToLower(strings.TrimPrefix(string(vd.Name()), prefix))}
		v.label = v.name
		if ev := proto.GetExtension(vd.Options(), optionsv1.E_Value).(*optionsv1.EnumValue); ev != nil {
			if ev.Name != "" {
				v.name = ev.Name
				v.label = ev.Name
			}
			if ev.Label != "" {
				v.label = ev.Label
			}
			v.desc = ev.Desc
		}
		m.values[vd.Number()] = v
		if _, dup := m.byName[v.name]; !dup {
			m.byName[v.name] = vd.Number()
		}
	}
	return m
}

// Canonical string for an enum value
func Name(e protoreflect.Enum) string {
	return metaFor(e.Descriptor()).values[e.Number()].name
}

// Display label for an enum value
func Label(e protoreflect.Enum) string {
	return metaFor(e.Descriptor()).values[e.Number()].label
}

// Longer help text for an enum value
func Desc(e protoreflect.Enum) string {
	return metaFor(e.Descriptor()).values[e.Number()].desc
}

// Display name for the enum type itself
func TypeLabel(e protoreflect.Enum) string {
	return metaFor(e.Descriptor()).label
}

// Enum value matching a canonical name, false when unknown
func FromName[E interface {
	protoreflect.Enum
	~int32
}](s string) (E, bool) {
	var zero E
	n, ok := metaFor(zero.Descriptor()).byName[s]
	return E(n), ok
}

// Enum value prefix like MOD_LOADER_ from ModLoader
func screamingPrefix(name string) string {
	var b strings.Builder
	for i, r := range name {
		if r >= 'A' && r <= 'Z' && i > 0 {
			b.WriteByte('_')
		}
		b.WriteRune(r)
	}
	return strings.ToUpper(b.String()) + "_"
}

// Reads typed option extensions straight from proto descriptors
package protometa

import (
	"strings"
	"sync"

	optionsv1 "github.com/discohaus/discopanel/pkg/proto/discopanel/options/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// Value descriptor for one enum value, nil when unknown
func valueDesc(e protoreflect.Enum) protoreflect.EnumValueDescriptor {
	return e.Descriptor().Values().ByNumber(e.Number())
}

// Loader facts annotated on one ModLoader value, never nil
func Loader(e protoreflect.Enum) *optionsv1.LoaderMeta {
	if vd := valueDesc(e); vd != nil {
		if m, ok := proto.GetExtension(vd.Options(), optionsv1.E_Loader).(*optionsv1.LoaderMeta); ok && m != nil {
			return m
		}
	}
	return &optionsv1.LoaderMeta{}
}

// Resource providing scope objects, unspecified when not scopeable
func ScopeSource(r optionsv1.ResourceType) optionsv1.ResourceType {
	if vd := valueDesc(r); vd != nil {
		if s, ok := proto.GetExtension(vd.Options(), optionsv1.E_ScopeSource).(optionsv1.ResourceType); ok {
			return s
		}
	}
	return optionsv1.ResourceType_RESOURCE_TYPE_UNSPECIFIED
}

// Declared values of an enum in declaration order, zero skipped
func Values[E interface {
	protoreflect.Enum
	~int32
}]() []E {
	var zero E
	vals := zero.Descriptor().Values()
	out := make([]E, 0, vals.Len())
	for i := range vals.Len() {
		if n := vals.Get(i).Number(); n != 0 {
			out = append(out, E(n))
		}
	}
	return out
}

// Settings categories annotated on a message, declaration order
func Categories(md protoreflect.MessageDescriptor) []*optionsv1.PropCategory {
	pc, ok := proto.GetExtension(md.Options(), optionsv1.E_PropCategories).(*optionsv1.PropCategories)
	if !ok || pc == nil {
		return nil
	}
	return pc.Categories
}

var (
	permMu    sync.RWMutex
	permCache = map[string]*optionsv1.RpcPerm{}
)

// Permission gate for one connect procedure, nil when unannotated
func Perm(procedure string) *optionsv1.RpcPerm {
	permMu.RLock()
	p, ok := permCache[procedure]
	permMu.RUnlock()
	if ok {
		return p
	}
	p = lookupPerm(procedure)
	permMu.Lock()
	permCache[procedure] = p
	permMu.Unlock()
	return p
}

// Resolves a procedure to its method descriptor annotation
func lookupPerm(procedure string) *optionsv1.RpcPerm {
	svc, method, ok := strings.Cut(strings.TrimPrefix(procedure, "/"), "/")
	if !ok {
		return nil
	}
	desc, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(svc))
	if err != nil {
		return nil
	}
	sd, ok := desc.(protoreflect.ServiceDescriptor)
	if !ok {
		return nil
	}
	md := sd.Methods().ByName(protoreflect.Name(method))
	if md == nil {
		return nil
	}
	if p, ok := proto.GetExtension(md.Options(), optionsv1.E_Perm).(*optionsv1.RpcPerm); ok && p != nil {
		return p
	}
	return nil
}

// Walks every annotated rpc perm across registered descriptors
func RangePerms(fn func(*optionsv1.RpcPerm)) {
	protoregistry.GlobalFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		svcs := fd.Services()
		for i := range svcs.Len() {
			methods := svcs.Get(i).Methods()
			for j := range methods.Len() {
				if p, ok := proto.GetExtension(methods.Get(j).Options(), optionsv1.E_Perm).(*optionsv1.RpcPerm); ok && p != nil {
					fn(p)
				}
			}
		}
		return true
	})
}

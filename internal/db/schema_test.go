package db

import (
	"reflect"
	"strings"
	"sync"
	"testing"

	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	protogormv1 "github.com/nickheyer/protogorm/gen/protogorm/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"gorm.io/gorm/schema"
)

// Locks injected tags and redact behavior for every model
func TestSchemaIntegrity(t *testing.T) {
	models := v1.AllModels()
	if len(models) == 0 {
		t.Fatal("no models")
	}
	modelNames := map[protoreflect.FullName]bool{}
	for _, m := range models {
		modelNames[m.(proto.Message).ProtoReflect().Descriptor().FullName()] = true
	}

	cache := &sync.Map{}
	for _, mdl := range models {
		msg := mdl.(proto.Message)
		desc := msg.ProtoReflect().Descriptor()
		rt := reflect.TypeOf(mdl).Elem()
		name := string(desc.Name())

		sch, err := schema.Parse(mdl, cache, schema.NamingStrategy{})
		if err != nil {
			t.Errorf("%s: parse: %v", name, err)
			continue
		}
		if len(sch.PrimaryFields) == 0 {
			t.Errorf("%s has no primary key", name)
		}

		fields := desc.Fields()
		for i := 0; i < fields.Len(); i++ {
			fd := fields.Get(i)
			ext, _ := proto.GetExtension(fd.Options(), protogormv1.E_Db).(*protogormv1.Field)
			sf, ok := structField(rt, string(fd.Name()))
			if !ok {
				t.Errorf("%s: no struct field for %s", name, fd.Name())
				continue
			}
			gormTag := sf.Tag.Get("gorm")
			if gormTag == "" {
				t.Errorf("%s.%s missing gorm tag", name, fd.Name())
				continue
			}
			if ext.GetSkip() {
				if gormTag != "-" {
					t.Errorf("%s.%s skip lost, tag %q", name, fd.Name(), gormTag)
				}
				continue
			}
			relation := fd.Kind() == protoreflect.MessageKind && !fd.IsMap() && !fd.IsList() && modelNames[fd.Message().FullName()]
			if !relation && !strings.Contains(gormTag, "column:"+string(fd.Name())) && !strings.Contains(ext.GetTag(), "column:") {
				t.Errorf("%s.%s missing column tag, got %q", name, fd.Name(), gormTag)
			}
			for _, frag := range strings.Split(ext.GetTag(), ";") {
				if frag != "" && !strings.Contains(gormTag, frag) {
					t.Errorf("%s.%s lost tag fragment %q, got %q", name, fd.Name(), frag, gormTag)
				}
			}
			if !relation {
				switch {
				case fd.IsMap() || fd.IsList():
					if !strings.Contains(gormTag, "serializer:json") {
						t.Errorf("%s.%s missing json serializer, got %q", name, fd.Name(), gormTag)
					}
				case fd.Kind() == protoreflect.MessageKind && fd.Message().FullName() == "google.protobuf.Timestamp":
					if !strings.Contains(gormTag, "serializer:tspb") {
						t.Errorf("%s.%s missing tspb serializer, got %q", name, fd.Name(), gormTag)
					}
				case fd.Kind() == protoreflect.MessageKind:
					if !strings.Contains(gormTag, "serializer:json") {
						t.Errorf("%s.%s missing json serializer, got %q", name, fd.Name(), gormTag)
					}
				}
			}
			if ext.GetRedact() {
				if sf.Tag.Get("json") != "-" {
					t.Errorf("%s.%s redact lost json dash", name, fd.Name())
				}
				checkRedact(t, msg, fd)
			}
		}
	}
}

// Finds the struct field generated for one proto field
func structField(rt reflect.Type, protoName string) (reflect.StructField, bool) {
	for i := 0; i < rt.NumField(); i++ {
		sf := rt.Field(i)
		for _, part := range strings.Split(sf.Tag.Get("protobuf"), ",") {
			if part == "name="+protoName {
				return sf, true
			}
		}
	}
	return reflect.StructField{}, false
}

// Proves Redact clears the field on a clone only
func checkRedact(t *testing.T, msg proto.Message, fd protoreflect.FieldDescriptor) {
	t.Helper()
	if fd.Kind() != protoreflect.StringKind {
		t.Errorf("%s redact on non string field", fd.FullName())
		return
	}
	fresh := msg.ProtoReflect().New()
	fresh.Set(fd, protoreflect.ValueOfString("sekrit"))
	src := fresh.Interface()
	method := reflect.ValueOf(src).MethodByName("Redact")
	if !method.IsValid() {
		t.Errorf("%s model missing Redact", fd.FullName())
		return
	}
	out, ok := method.Call(nil)[0].Interface().(proto.Message)
	if !ok {
		t.Errorf("%s Redact returned non message", fd.FullName())
		return
	}
	if got := out.ProtoReflect().Get(fd).String(); got != "" {
		t.Errorf("%s survived Redact, got %q", fd.FullName(), got)
	}
	if got := src.ProtoReflect().Get(fd).String(); got != "sekrit" {
		t.Errorf("%s Redact mutated source, got %q", fd.FullName(), got)
	}
}

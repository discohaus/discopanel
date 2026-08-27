// Generic api surface model every discoverer feeds the seeder
package seed

import "strings"

// Value classes a request field can hold
type Kind int

const (
	KindString Kind = iota
	KindInt
	KindFloat
	KindBool
	KindBytes
	KindTime
	KindEnum
	KindMessage
	KindList
	KindMap
	KindAny
)

// Shape one value takes on the wire
type Shape struct {
	Kind Kind
	// Message or enum type name
	Name string
	// Members in declaration order for messages
	Fields []*Field
	// Accepted literals with the zero value removed
	Enum []string
	// Element for lists, value for maps
	Elem *Shape
	// Key for maps
	Key *Shape
}

// One named member of a message
type Field struct {
	// Name sent over json
	Name     string
	Shape    *Shape
	Optional bool
}

// One callable procedure on the panel
type Operation struct {
	// Method or handler name
	Name string
	// Http verb
	Method string
	// Url path under the panel base
	Path string
	// Body shape, nil when the call takes none
	Input *Shape
	// Reply shape when the discoverer knows it
	Output *Shape
	// Entity the reply carries when the path says so
	Entity string
}

// Everything one running panel exposes
type Surface struct {
	// Either rest or connect
	Era string
	Ops []*Operation
}

// Finds one field by wire name
func (s *Shape) Field(name string) *Field {
	if s == nil {
		return nil
	}
	for _, f := range s.Fields {
		if f.Name == name {
			return f
		}
	}
	return nil
}

// Lowercase letters and digits only
func Norm(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Drops a trailing plural marker
func Singular(s string) string {
	switch {
	case strings.HasSuffix(s, "ies"):
		return strings.TrimSuffix(s, "ies") + "y"
	case strings.HasSuffix(s, "ses"), strings.HasSuffix(s, "xes"):
		return strings.TrimSuffix(s, "es")
	case strings.HasSuffix(s, "s") && !strings.HasSuffix(s, "ss") && !strings.HasSuffix(s, "us"):
		return strings.TrimSuffix(s, "s")
	}
	return s
}

// Entity a reference field points at, empty for plain ids
func RefEntity(field string) string {
	n := Norm(field)
	if n == "id" || !strings.HasSuffix(n, "id") {
		return ""
	}
	return strings.TrimSuffix(n, "id")
}

// Whether a field name is a reference to another row
func IsRef(field string) bool {
	return RefEntity(field) != ""
}

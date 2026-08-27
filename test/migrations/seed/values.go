// Synthesizes request values from field names and shapes
package seed

import (
	"fmt"
	"strings"
	"time"
)

// Credentials the first registered account uses
type Principal struct {
	Username string
	Email    string
	Password string
}

// Default identity every fixture panel is seeded under
var DefaultPrincipal = Principal{
	Username: "fixture-admin",
	Email:    "fixture-admin@example.com",
	Password: "Fixture-Passw0rd!",
}

// Answers reference lookups against harvested rows
type Resolver interface {
	// Id of one known row, false when none
	ResolveID(entity string) (string, bool)
	// Display name of one known row, false when none
	ResolveName(entity string) (string, bool)
}

// Deterministic value factory with per field counters
type Values struct {
	Principal Principal
	seq       map[string]int
	port      int
	now       time.Time
}

// Value factory starting its counters fresh
func NewValues(p Principal) *Values {
	return &Values{
		Principal: p,
		seq:       map[string]int{},
		port:      25600,
		now:       time.Now().UTC().Truncate(time.Second),
	}
}

// Next counter for one key
func (v *Values) next(key string) int {
	v.seq[key]++
	return v.seq[key]
}

// Next unused port number
func (v *Values) nextPort() int {
	v.port++
	return v.port
}

// Nesting cap for message values
const maxDepth = 3

// Builds one json value for a shape and field name
func (v *Values) Build(op *Operation, name string, s *Shape, res Resolver, depth int, auth bool) any {
	if s == nil {
		return nil
	}
	switch s.Kind {
	case KindString:
		return v.stringFor(op, name, res, auth)
	case KindInt:
		return v.intFor(name)
	case KindFloat:
		return v.floatFor(name)
	case KindBool:
		return boolFor(name)
	case KindBytes:
		return nil
	case KindTime:
		return v.timeFor(name)
	case KindEnum:
		if len(s.Enum) == 0 {
			return nil
		}
		return s.Enum[0]
	case KindList:
		return v.listFor(op, name, s, res, depth, auth)
	case KindMap:
		if depth >= maxDepth || s.Elem == nil {
			return nil
		}
		return map[string]any{"fixture": v.Build(op, name, s.Elem, res, depth+1, auth)}
	case KindMessage:
		if depth >= maxDepth {
			return nil
		}
		return v.messageFor(op, s, res, depth+1, auth)
	}
	return nil
}

// Fills every member of one message shape
func (v *Values) messageFor(op *Operation, s *Shape, res Resolver, depth int, auth bool) map[string]any {
	out := map[string]any{}
	for _, f := range s.Fields {
		if f.Shape == nil || f.Shape.Kind == KindAny || f.Shape.Kind == KindBytes {
			continue
		}
		// Rows never choose their own primary key
		if Norm(f.Name) == "id" {
			continue
		}
		// Credential calls carry nothing beyond the credentials
		if auth && depth <= 1 && !isCredential(f.Name) {
			continue
		}
		val := v.Build(op, f.Name, f.Shape, res, depth, auth)
		if val == nil {
			continue
		}
		if str, ok := val.(string); ok && str == "" {
			continue
		}
		out[f.Name] = val
	}
	return out
}

// One element lists, names borrowed from pools when known
func (v *Values) listFor(op *Operation, name string, s *Shape, res Resolver, depth int, auth bool) any {
	if s.Elem == nil {
		return nil
	}
	if s.Elem.Kind == KindString && res != nil {
		if n, ok := res.ResolveName(Singular(Norm(name))); ok {
			return []any{n}
		}
	}
	if s.Elem.Kind == KindMessage && depth >= maxDepth {
		return nil
	}
	val := v.Build(op, Singular(name), s.Elem, res, depth+1, auth)
	if val == nil {
		return nil
	}
	return []any{val}
}

// Whether a field names part of a login identity
func isCredential(name string) bool {
	n := Norm(name)
	return strings.Contains(n, "username") || strings.Contains(n, "email") || strings.Contains(n, "password")
}

// Picks a string from the field vocabulary
func (v *Values) stringFor(op *Operation, name string, res Resolver, auth bool) string {
	n := Norm(name)
	if entity := RefEntity(name); entity != "" {
		if res != nil {
			if id, ok := res.ResolveID(entity); ok {
				return id
			}
		}
		return ""
	}
	switch {
	case strings.Contains(n, "password") || strings.Contains(n, "secret") || n == "pin" || strings.HasSuffix(n, "pin"):
		return v.Principal.Password
	case strings.Contains(n, "username"):
		if auth {
			return v.Principal.Username
		}
		return fmt.Sprintf("fixture-user-%d", v.next(n))
	case strings.Contains(n, "email"):
		if auth {
			return v.Principal.Email
		}
		return fmt.Sprintf("fixture-user-%d@example.com", v.next(n))
	case strings.Contains(n, "mcversion") || strings.Contains(n, "gameversion") || strings.Contains(n, "minecraftversion"):
		return "1.20.1"
	case strings.Contains(n, "protocol"):
		return "tcp"
	case n == "config" || strings.HasSuffix(n, "config") || strings.Contains(n, "json") || strings.Contains(n, "payload"):
		return "{}"
	case strings.Contains(n, "hostname"):
		return fmt.Sprintf("fixture-%d.example.com", v.next(n))
	case strings.Contains(n, "url"):
		return fmt.Sprintf("https://example.com/fixture/%d", v.next(n))
	case strings.Contains(n, "cron"):
		return "0 3 * * *"
	case strings.Contains(n, "timezone") || n == "tz":
		return "UTC"
	case strings.Contains(n, "command") || strings.Contains(n, "cmd"):
		return fmt.Sprintf("say fixture %d", v.next(n))
	case strings.Contains(n, "path") || strings.Contains(n, "dir"):
		return fmt.Sprintf("fixture/%d", v.next(n))
	case strings.Contains(n, "version"):
		return "1.0.0"
	case strings.Contains(n, "description") || strings.Contains(n, "summary") || strings.Contains(n, "documentation") || strings.Contains(n, "message"):
		return fmt.Sprintf("Fixture %s number %d", op.Name, v.next(n))
	case strings.HasSuffix(n, "name") || n == "title" || n == "label":
		return fmt.Sprintf("Fixture %s %d", op.Name, v.next(n))
	case n == "category":
		return "fixture"
	case n == "icon":
		return "box"
	case strings.Contains(n, "code"):
		return fmt.Sprintf("FIXTURE%d", v.next(n))
	}
	return fmt.Sprintf("fixture-%s-%d", n, v.next(n))
}

// Picks an integer from the field vocabulary
func (v *Values) intFor(name string) int {
	n := Norm(name)
	switch {
	case strings.Contains(n, "port"):
		return v.nextPort()
	case strings.Contains(n, "memory"):
		return 2048
	case strings.Contains(n, "player"):
		return 20
	case strings.Contains(n, "interval"):
		return 3600
	case strings.Contains(n, "timeout"):
		return 300
	case strings.Contains(n, "delay"):
		return 5
	case strings.Contains(n, "retr"):
		return 1
	case strings.Contains(n, "uses"):
		return 5
	case strings.Contains(n, "expire"):
		return 24
	case strings.Contains(n, "size") || strings.Contains(n, "length") || strings.Contains(n, "bytes") || strings.Contains(n, "chunk"):
		return 1024
	case strings.Contains(n, "max"):
		return 10
	case strings.Contains(n, "tail"):
		return 10
	}
	return 1
}

// Picks a float from the field vocabulary
func (v *Values) floatFor(name string) float64 {
	if strings.Contains(Norm(name), "cpu") {
		return 0.5
	}
	return 1.5
}

// Only switches that read as enabling flip on
func boolFor(name string) bool {
	n := Norm(name)
	return n == "enabled" || strings.HasPrefix(n, "enable") || n == "isactive"
}

// Future stamps for expiries, now for everything else
func (v *Values) timeFor(name string) string {
	n := Norm(name)
	if strings.Contains(n, "expire") || strings.Contains(n, "runat") {
		return v.now.Add(24 * time.Hour).Format(time.RFC3339)
	}
	return v.now.Format(time.RFC3339)
}

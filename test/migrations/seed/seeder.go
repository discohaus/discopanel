// Plans and runs creates against any discovered surface
package seed

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Rows harvested for one entity
type Pool struct {
	IDs   []string
	Names []string
}

// What one procedure did across its attempts
type OpReport struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Attempts  int    `json:"attempts"`
	Succeeded int    `json:"succeeded"`
	LastError string `json:"last_error,omitempty"`
}

// Everything one seeding run achieved
type Report struct {
	Era           string         `json:"era"`
	Authenticated bool           `json:"authenticated"`
	Ops           []OpReport     `json:"ops"`
	Pools         map[string]int `json:"pools"`
}

// Names of procedures never worth calling while seeding
var DefaultSkip = regexp.MustCompile(`(?i)sync|upload|download|bundle|recovery|reset|logout|password`)

// Drives one panel through auth, harvest, and creates
type Seeder struct {
	Surface *Surface
	Client  *Client
	Values  *Values
	// Attempts per create procedure
	Repeat int
	// Procedures matching this never run
	Skip *regexp.Regexp
	Log  func(format string, args ...any)

	pools   map[string]*Pool
	reports map[*Operation]*OpReport
	order   []*Operation
}

// Seeder with sane defaults over one surface
func New(surface *Surface, client *Client) *Seeder {
	return &Seeder{
		Surface: surface,
		Client:  client,
		Values:  NewValues(DefaultPrincipal),
		Repeat:  2,
		Skip:    DefaultSkip,
		Log:     func(string, ...any) {},
		pools:   map[string]*Pool{},
		reports: map[*Operation]*OpReport{},
	}
}

// Id of one harvested row, longest entity name match wins
func (s *Seeder) ResolveID(entity string) (string, bool) {
	if p := s.pool(entity); p != nil && len(p.IDs) > 0 {
		return p.IDs[0], true
	}
	return "", false
}

// Name of one harvested row, longest entity name match wins
func (s *Seeder) ResolveName(entity string) (string, bool) {
	if p := s.pool(entity); p != nil && len(p.Names) > 0 {
		return p.Names[0], true
	}
	return "", false
}

// Pool whose name equals or suffixes the entity
func (s *Seeder) pool(entity string) *Pool {
	if entity == "" {
		return nil
	}
	if p, ok := s.pools[entity]; ok && (len(p.IDs) > 0 || len(p.Names) > 0) {
		return p
	}
	best := ""
	for name, p := range s.pools {
		if len(p.IDs) == 0 && len(p.Names) == 0 {
			continue
		}
		if strings.HasSuffix(entity, name) || strings.HasSuffix(name, entity) {
			if len(name) > len(best) {
				best = name
			}
		}
	}
	if best == "" {
		return nil
	}
	return s.pools[best]
}

// Runs the whole seeding sequence
func (s *Seeder) Run(ctx context.Context) (*Report, error) {
	if s.Surface == nil || len(s.Surface.Ops) == 0 {
		return nil, fmt.Errorf("surface has no procedures")
	}
	s.authenticate(ctx)
	s.harvestLists(ctx)
	s.createAll(ctx)
	s.harvestLists(ctx)
	return s.report(), nil
}

// Registers the principal then logs in for a token
func (s *Seeder) authenticate(ctx context.Context) {
	register := s.find(func(n string) bool { return strings.Contains(n, "register") || strings.Contains(n, "signup") })
	login := s.find(func(n string) bool { return strings.Contains(n, "login") && !strings.Contains(n, "url") })
	if register != nil {
		res := s.call(ctx, register, true)
		s.Log("auth register %s %s", register.Path, outcome(res))
	}
	if login == nil {
		return
	}
	res := s.call(ctx, login, true)
	s.Log("auth login %s %s", login.Path, outcome(res))
	if token := findToken(res.Body); token != "" {
		s.Client.Token = token
	}
}

// First procedure whose normalized name satisfies the test
func (s *Seeder) find(match func(string) bool) *Operation {
	for _, op := range s.Surface.Ops {
		if match(Norm(op.Name)) {
			return op
		}
	}
	return nil
}

// Whether a procedure reads rows without needing references
func isList(op *Operation) bool {
	n := Norm(op.Name)
	if op.Method == http.MethodGet {
		return true
	}
	if !strings.HasPrefix(n, "list") {
		return false
	}
	for _, f := range shapeFields(op.Input) {
		if IsRef(f.Name) {
			return false
		}
	}
	return true
}

// Whether a procedure inserts rows
func (s *Seeder) isCreate(op *Operation) bool {
	n := Norm(op.Name)
	if s.Skip != nil && s.Skip.MatchString(op.Name) {
		return false
	}
	if strings.Contains(n, "register") || strings.Contains(n, "login") || strings.Contains(n, "signup") {
		return false
	}
	if s.Surface.Era == "rest" {
		return op.Method == http.MethodPost && op.Input != nil && !strings.Contains(op.Path, "{")
	}
	return strings.HasPrefix(n, "create")
}

// Calls every list procedure and harvests what comes back
func (s *Seeder) harvestLists(ctx context.Context) {
	for _, op := range s.Surface.Ops {
		if !isList(op) || (s.Skip != nil && s.Skip.MatchString(op.Name)) {
			continue
		}
		res := s.Client.Call(ctx, op, map[string]any{})
		if res.OK() {
			s.harvest(res.Body, op.Entity)
		}
	}
}

// Runs creates in dependency rounds then best effort
func (s *Seeder) createAll(ctx context.Context) {
	var pending []*Operation
	for _, op := range s.Surface.Ops {
		if s.isCreate(op) {
			pending = append(pending, op)
		}
	}
	producible := s.producible()
	for len(pending) > 0 {
		var ready, blocked []*Operation
		for _, op := range pending {
			if len(s.unmet(op, producible)) == 0 {
				ready = append(ready, op)
			} else {
				blocked = append(blocked, op)
			}
		}
		if len(ready) == 0 {
			sort.SliceStable(blocked, func(i, j int) bool {
				ci, cj := s.consumers(blocked[i], blocked), s.consumers(blocked[j], blocked)
				if ci != cj {
					return ci > cj
				}
				return len(s.unmet(blocked[i], producible)) < len(s.unmet(blocked[j], producible))
			})
			ready = blocked[:1]
			blocked = blocked[1:]
			s.Log("create %s runs without %v", ready[0].Name, s.unmet(ready[0], producible))
		}
		for _, op := range ready {
			s.runCreate(ctx, op)
		}
		pending = blocked
	}
}

// Calls one create the configured number of times
func (s *Seeder) runCreate(ctx context.Context, op *Operation) {
	for i := 0; i < s.Repeat; i++ {
		res := s.call(ctx, op, false)
		s.Log("create %s %s", op.Name, outcome(res))
		if res.OK() {
			s.harvest(res.Body, op.Entity)
		}
	}
}

// Builds a body, sends it, and records the outcome
func (s *Seeder) call(ctx context.Context, op *Operation, auth bool) Result {
	var body any
	if op.Input != nil {
		body = s.Values.Build(op, "", op.Input, s, 0, auth)
	}
	res := s.Client.Call(ctx, op, body)
	rep := s.reports[op]
	if rep == nil {
		rep = &OpReport{Name: op.Name, Path: op.Path}
		s.reports[op] = rep
		s.order = append(s.order, op)
	}
	rep.Attempts++
	if res.OK() {
		rep.Succeeded++
	} else {
		rep.LastError = res.Error()
	}
	return res
}

// Entities some procedure on the surface can hand back
func (s *Seeder) producible() map[string]bool {
	out := map[string]bool{}
	for _, op := range s.Surface.Ops {
		for _, entity := range produces(op) {
			out[entity] = true
		}
	}
	return out
}

// Entities one procedure's reply carries
func produces(op *Operation) []string {
	var out []string
	if op.Entity != "" {
		out = append(out, op.Entity)
	}
	for _, f := range shapeFields(op.Output) {
		elem := f.Shape
		if elem != nil && elem.Kind == KindList {
			elem = elem.Elem
		}
		if elem != nil && elem.Kind == KindMessage && elem.Field("id") != nil {
			out = append(out, Singular(Norm(f.Name)))
		}
	}
	return out
}

// Pending procedures waiting on something this one produces
func (s *Seeder) consumers(op *Operation, pending []*Operation) int {
	n := 0
	for _, other := range pending {
		if other == op {
			continue
		}
		for _, f := range shapeFields(other.Input) {
			entity := RefEntity(f.Name)
			if entity == "" {
				continue
			}
			for _, made := range produces(op) {
				if strings.HasSuffix(entity, made) || strings.HasSuffix(made, entity) {
					n++
				}
			}
		}
	}
	return n
}

// References the surface could satisfy but pools cannot yet
func (s *Seeder) unmet(op *Operation, producible map[string]bool) []string {
	var out []string
	for _, f := range shapeFields(op.Input) {
		entity := RefEntity(f.Name)
		if entity == "" {
			continue
		}
		if _, ok := s.ResolveID(entity); ok {
			continue
		}
		if s.canProduce(entity, producible) {
			out = append(out, f.Name)
		}
	}
	return out
}

// Whether any producible entity matches by suffix
func (s *Seeder) canProduce(entity string, producible map[string]bool) bool {
	for name := range producible {
		if strings.HasSuffix(entity, name) || strings.HasSuffix(name, entity) {
			return true
		}
	}
	return false
}

// Members of a message shape, nil safe
func shapeFields(s *Shape) []*Field {
	if s == nil || s.Kind != KindMessage {
		return nil
	}
	return s.Fields
}

// Walks a reply collecting ids and names by entity
func (s *Seeder) harvest(body any, hint string) {
	switch v := body.(type) {
	case map[string]any:
		if id := stringValue(v["id"]); id != "" && hint != "" {
			p := s.pools[hint]
			if p == nil {
				p = &Pool{}
				s.pools[hint] = p
			}
			if !slices.Contains(p.IDs, id) {
				p.IDs = append(p.IDs, id)
			}
			if name := stringValue(v["name"]); name != "" && !slices.Contains(p.Names, name) {
				p.Names = append(p.Names, name)
			}
		}
		for key, child := range v {
			switch child.(type) {
			case map[string]any, []any:
				s.harvest(child, Singular(Norm(key)))
			}
		}
	case []any:
		for _, item := range v {
			s.harvest(item, hint)
		}
	}
}

// Session token from a login reply
func findToken(body any) string {
	m, ok := body.(map[string]any)
	if !ok {
		return ""
	}
	for key, val := range m {
		n := Norm(key)
		if n == "token" || n == "accesstoken" || n == "sessiontoken" {
			if str := stringValue(val); str != "" {
				return str
			}
		}
	}
	return ""
}

// String form of scalar json values
func stringValue(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		if x == float64(int64(x)) {
			return fmt.Sprintf("%d", int64(x))
		}
		return fmt.Sprintf("%v", x)
	}
	return ""
}

// One line summary of a call
func outcome(res Result) string {
	if res.OK() {
		return "ok"
	}
	return "failed " + res.Error()
}

// Snapshot of procedures and pools
func (s *Seeder) report() *Report {
	rep := &Report{
		Era:           s.Surface.Era,
		Authenticated: s.Client.Token != "",
		Pools:         map[string]int{},
	}
	for _, op := range s.order {
		rep.Ops = append(rep.Ops, *s.reports[op])
	}
	for name, p := range s.pools {
		if len(p.IDs) > 0 {
			rep.Pools[name] = len(p.IDs)
		}
	}
	return rep
}

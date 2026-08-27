// Discovers rest routes and bodies from a release's own source
package seed

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"os/exec"
	"path"
	"reflect"
	"strconv"
	"strings"
)

// One parsed go package from the archived tree
type srcPackage struct {
	dir   string
	files []*ast.File
	// Import alias to path per file
	imports map[*ast.File]map[string]string
}

// Source tree of one tag held in memory
type srcTree struct {
	fset *token.FileSet
	pkgs map[string]*srcPackage
}

// Reads every internal go file at one git tag
func loadTree(ctx context.Context, repoDir, tag string) (*srcTree, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", repoDir, "archive", "--format=tar", tag, "internal")
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git archive %s: %v %s", tag, err, stderr.String())
	}
	tree := &srcTree{fset: token.NewFileSet(), pkgs: map[string]*srcPackage{}}
	tr := tar.NewReader(&out)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if hdr.Typeflag != tar.TypeReg || !strings.HasSuffix(hdr.Name, ".go") || strings.HasSuffix(hdr.Name, "_test.go") {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, err
		}
		file, err := parser.ParseFile(tree.fset, hdr.Name, data, parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", hdr.Name, err)
		}
		dir := path.Dir(hdr.Name)
		pkg := tree.pkgs[dir]
		if pkg == nil {
			pkg = &srcPackage{dir: dir, imports: map[*ast.File]map[string]string{}}
			tree.pkgs[dir] = pkg
		}
		pkg.files = append(pkg.files, file)
		aliases := map[string]string{}
		for _, imp := range file.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			name := path.Base(p)
			if imp.Name != nil {
				name = imp.Name.Name
			}
			aliases[name] = p
		}
		pkg.imports[file] = aliases
	}
	if len(tree.pkgs) == 0 {
		return nil, fmt.Errorf("tag %s has no internal go sources", tag)
	}
	return tree, nil
}

// Package whose directory ends the import path
func (t *srcTree) byImport(importPath string) *srcPackage {
	for dir, pkg := range t.pkgs {
		if strings.HasSuffix(importPath, "/"+dir) {
			return pkg
		}
	}
	return nil
}

// Function declaration by name, methods included
func (p *srcPackage) funcDecl(name string) (*ast.FuncDecl, *ast.File) {
	for _, file := range p.files {
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == name {
				return fn, file
			}
		}
	}
	return nil, nil
}

// Type declaration by name
func (p *srcPackage) typeSpec(name string) (*ast.TypeSpec, *ast.File) {
	for _, file := range p.files {
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				if ts, ok := spec.(*ast.TypeSpec); ok && ts.Name.Name == name {
					return ts, file
				}
			}
		}
	}
	return nil, nil
}

// String constants declared with the named type
func (p *srcPackage) constValues(typeName string) []string {
	var out []string
	for _, file := range p.files {
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				ident, ok := vs.Type.(*ast.Ident)
				if !ok || ident.Name != typeName {
					continue
				}
				for _, val := range vs.Values {
					if lit, ok := val.(*ast.BasicLit); ok && lit.Kind == token.STRING {
						s, _ := strconv.Unquote(lit.Value)
						out = append(out, s)
					}
				}
			}
		}
	}
	return out
}

// One registered mux route
type route struct {
	method  string
	path    string
	handler string
}

// Walks the router setup collecting every route
func (t *srcTree) routes(api *srcPackage) ([]route, error) {
	setup, _ := api.funcDecl("setupRoutes")
	if setup == nil {
		return nil, fmt.Errorf("api package has no setupRoutes")
	}
	var out []route
	t.walkRoutes(api, setup, map[string]string{}, &out, 0)
	if len(out) == 0 {
		return nil, fmt.Errorf("setupRoutes registered nothing")
	}
	return out, nil
}

// Interprets one function body binding routers to prefixes
func (t *srcTree) walkRoutes(api *srcPackage, fn *ast.FuncDecl, prefixes map[string]string, out *[]route, depth int) {
	if fn.Body == nil || depth > 4 {
		return
	}
	for _, stmt := range fn.Body.List {
		switch st := stmt.(type) {
		case *ast.AssignStmt:
			if len(st.Lhs) != 1 || len(st.Rhs) != 1 {
				continue
			}
			ident, ok := st.Lhs[0].(*ast.Ident)
			if !ok {
				continue
			}
			if prefix, ok := routerPrefix(st.Rhs[0], prefixes); ok {
				prefixes[ident.Name] = prefix
			}
		case *ast.ExprStmt:
			call, ok := st.X.(*ast.CallExpr)
			if !ok {
				continue
			}
			if r, ok := registration(call, prefixes); ok {
				*out = append(*out, r...)
				continue
			}
			// Route groups delegate to sibling setup methods
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && strings.HasPrefix(sel.Sel.Name, "setup") {
				callee, _ := api.funcDecl(sel.Sel.Name)
				if callee == nil || callee.Type.Params == nil {
					continue
				}
				bound := map[string]string{}
				argIdx := 0
				for _, param := range callee.Type.Params.List {
					for _, name := range param.Names {
						if argIdx < len(call.Args) {
							if id, ok := call.Args[argIdx].(*ast.Ident); ok {
								if p, ok := prefixes[id.Name]; ok {
									bound[name.Name] = p
								}
							}
						}
						argIdx++
					}
				}
				t.walkRoutes(api, callee, bound, out, depth+1)
			}
		}
	}
}

// Prefix a subrouter expression resolves to
func routerPrefix(expr ast.Expr, prefixes map[string]string) (string, bool) {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return "", false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	switch sel.Sel.Name {
	case "NewRouter":
		return "", true
	case "Subrouter":
		inner, ok := sel.X.(*ast.CallExpr)
		if !ok {
			return "", false
		}
		innerSel, ok := inner.Fun.(*ast.SelectorExpr)
		if !ok {
			return "", false
		}
		root, ok := innerSel.X.(*ast.Ident)
		if !ok {
			return "", false
		}
		base, ok := prefixes[root.Name]
		if !ok {
			return "", false
		}
		switch innerSel.Sel.Name {
		case "PathPrefix":
			if len(inner.Args) == 1 {
				if lit, ok := inner.Args[0].(*ast.BasicLit); ok {
					s, _ := strconv.Unquote(lit.Value)
					return base + s, true
				}
			}
		case "NewRoute":
			return base, true
		}
	}
	return "", false
}

// Routes one HandleFunc or Handle call chain registers
func registration(call *ast.CallExpr, prefixes map[string]string) ([]route, bool) {
	var methods []string
	cur := call
	for {
		sel, ok := cur.Fun.(*ast.SelectorExpr)
		if !ok {
			return nil, false
		}
		if sel.Sel.Name == "Methods" {
			for _, arg := range cur.Args {
				if lit, ok := arg.(*ast.BasicLit); ok {
					m, _ := strconv.Unquote(lit.Value)
					methods = append(methods, m)
				}
			}
			next, ok := sel.X.(*ast.CallExpr)
			if !ok {
				return nil, false
			}
			cur = next
			continue
		}
		if sel.Sel.Name != "HandleFunc" && sel.Sel.Name != "Handle" {
			return nil, false
		}
		root, ok := sel.X.(*ast.Ident)
		if !ok || len(cur.Args) < 2 {
			return nil, false
		}
		base, ok := prefixes[root.Name]
		if !ok {
			return nil, false
		}
		lit, ok := cur.Args[0].(*ast.BasicLit)
		if !ok {
			return nil, false
		}
		p, _ := strconv.Unquote(lit.Value)
		handler := handlerName(cur.Args[1])
		if handler == "" {
			return nil, false
		}
		if len(methods) == 0 {
			methods = []string{http.MethodGet}
		}
		var out []route
		for _, m := range methods {
			out = append(out, route{method: m, path: base + p, handler: handler})
		}
		return out, true
	}
}

// Innermost handle method referenced by an expression
func handlerName(expr ast.Expr) string {
	name := ""
	ast.Inspect(expr, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && id.Name == "s" && strings.HasPrefix(sel.Sel.Name, "handle") {
			name = sel.Sel.Name
		}
		return true
	})
	return name
}

// Type expression a handler decodes its json body into
func decodeTarget(fn *ast.FuncDecl) ast.Expr {
	if fn.Body == nil {
		return nil
	}
	var target string
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if target != "" {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Decode" || len(call.Args) != 1 {
			return true
		}
		if !mentionsBody(sel.X) {
			return true
		}
		if un, ok := call.Args[0].(*ast.UnaryExpr); ok && un.Op == token.AND {
			if id, ok := un.X.(*ast.Ident); ok {
				target = id.Name
			}
		}
		return true
	})
	if target == "" {
		return nil
	}
	var typ ast.Expr
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if typ != nil {
			return false
		}
		switch st := n.(type) {
		case *ast.DeclStmt:
			gd, ok := st.Decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				return true
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, name := range vs.Names {
					if name.Name == target && vs.Type != nil {
						typ = vs.Type
					}
				}
			}
		case *ast.AssignStmt:
			for i, lhs := range st.Lhs {
				id, ok := lhs.(*ast.Ident)
				if !ok || id.Name != target || i >= len(st.Rhs) {
					continue
				}
				if cl, ok := st.Rhs[i].(*ast.CompositeLit); ok {
					typ = cl.Type
				}
			}
		}
		return true
	})
	return typ
}

// Whether a decoder expression reads the request body
func mentionsBody(expr ast.Expr) bool {
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "Body" {
			found = true
		}
		return !found
	})
	return found
}

// Resolves go type expressions into wire shapes
type restShapes struct {
	tree *srcTree
	// Guards against self referential structs
	active map[string]bool
}

// Shape of a type expression seen from one file
func (r *restShapes) shape(expr ast.Expr, pkg *srcPackage, file *ast.File, depth int) *Shape {
	if depth > 6 {
		return &Shape{Kind: KindAny}
	}
	switch t := expr.(type) {
	case *ast.StarExpr:
		return r.shape(t.X, pkg, file, depth)
	case *ast.ParenExpr:
		return r.shape(t.X, pkg, file, depth)
	case *ast.ArrayType:
		if id, ok := t.Elt.(*ast.Ident); ok && id.Name == "byte" {
			return &Shape{Kind: KindBytes}
		}
		return &Shape{Kind: KindList, Elem: r.shape(t.Elt, pkg, file, depth+1)}
	case *ast.MapType:
		return &Shape{Kind: KindMap, Key: r.shape(t.Key, pkg, file, depth+1), Elem: r.shape(t.Value, pkg, file, depth+1)}
	case *ast.InterfaceType:
		return &Shape{Kind: KindAny}
	case *ast.StructType:
		return r.structShape(t, pkg, file, depth)
	case *ast.Ident:
		if s := builtinShape(t.Name); s != nil {
			return s
		}
		return r.named(pkg, t.Name, depth)
	case *ast.SelectorExpr:
		alias, ok := t.X.(*ast.Ident)
		if !ok {
			return &Shape{Kind: KindAny}
		}
		importPath := pkg.imports[file][alias.Name]
		switch {
		case importPath == "time" && t.Sel.Name == "Time":
			return &Shape{Kind: KindTime}
		case importPath == "encoding/json":
			return &Shape{Kind: KindAny}
		}
		target := r.tree.byImport(importPath)
		if target == nil {
			return &Shape{Kind: KindAny}
		}
		return r.named(target, t.Sel.Name, depth)
	}
	return &Shape{Kind: KindAny}
}

// Shape of a named type declared in one package
func (r *restShapes) named(pkg *srcPackage, name string, depth int) *Shape {
	key := pkg.dir + "." + name
	if r.active[key] {
		return &Shape{Kind: KindMessage, Name: name}
	}
	ts, file := pkg.typeSpec(name)
	if ts == nil {
		return &Shape{Kind: KindAny}
	}
	r.active[key] = true
	defer delete(r.active, key)
	if id, ok := ts.Type.(*ast.Ident); ok && id.Name == "string" {
		if values := pkg.constValues(name); len(values) > 0 {
			return &Shape{Kind: KindEnum, Name: name, Enum: values}
		}
	}
	s := r.shape(ts.Type, pkg, file, depth+1)
	if s.Kind == KindMessage && s.Name == "" {
		s.Name = name
	}
	return s
}

// Shape of a struct literal type honoring json tags
func (r *restShapes) structShape(st *ast.StructType, pkg *srcPackage, file *ast.File, depth int) *Shape {
	s := &Shape{Kind: KindMessage}
	for _, field := range st.Fields.List {
		tag := ""
		if field.Tag != nil {
			raw, _ := strconv.Unquote(field.Tag.Value)
			tag = reflect.StructTag(raw).Get("json")
		}
		name, opts, _ := strings.Cut(tag, ",")
		if name == "-" {
			continue
		}
		// Embedded structs flatten into the parent
		if len(field.Names) == 0 {
			if inner := r.shape(field.Type, pkg, file, depth+1); inner.Kind == KindMessage {
				s.Fields = append(s.Fields, inner.Fields...)
			}
			continue
		}
		for _, ident := range field.Names {
			if !ident.IsExported() {
				continue
			}
			wire := name
			if wire == "" {
				wire = ident.Name
			}
			_, star := field.Type.(*ast.StarExpr)
			s.Fields = append(s.Fields, &Field{
				Name:     wire,
				Shape:    r.shape(field.Type, pkg, file, depth+1),
				Optional: star || strings.Contains(opts, "omitempty"),
			})
		}
	}
	return s
}

// Shape of a go builtin type name
func builtinShape(name string) *Shape {
	switch name {
	case "string":
		return &Shape{Kind: KindString}
	case "bool":
		return &Shape{Kind: KindBool}
	case "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64", "byte":
		return &Shape{Kind: KindInt}
	case "float32", "float64":
		return &Shape{Kind: KindFloat}
	case "any", "error":
		return &Shape{Kind: KindAny}
	}
	return nil
}

// Entity a rest path speaks about, empty for auth
func restEntity(p string) string {
	segments := strings.Split(strings.Trim(p, "/"), "/")
	last := ""
	for _, seg := range segments {
		if strings.HasPrefix(seg, "{") {
			break
		}
		last = seg
	}
	if last == "" || strings.HasPrefix(p, "/api/v1/auth") {
		return ""
	}
	return Singular(Norm(last))
}

// Builds the rest surface for one tag of the repository
func DiscoverREST(ctx context.Context, repoDir, tag string) (*Surface, error) {
	tree, err := loadTree(ctx, repoDir, tag)
	if err != nil {
		return nil, err
	}
	api := tree.pkgs["internal/api"]
	if api == nil {
		return nil, fmt.Errorf("tag %s has no internal/api package", tag)
	}
	routes, err := tree.routes(api)
	if err != nil {
		return nil, err
	}
	shapes := &restShapes{tree: tree, active: map[string]bool{}}
	surface := &Surface{Era: "rest"}
	for _, rt := range routes {
		op := &Operation{
			Name:   strings.TrimPrefix(rt.handler, "handle"),
			Method: rt.method,
			Path:   rt.path,
			Entity: restEntity(rt.path),
		}
		if rt.method != http.MethodGet {
			fn, file := api.funcDecl(rt.handler)
			if fn == nil {
				continue
			}
			if typ := decodeTarget(fn); typ != nil {
				if s := shapes.shape(typ, api, file, 0); s.Kind == KindMessage {
					op.Input = s
				}
			}
		}
		if rt.method == http.MethodGet && strings.Contains(rt.path, "{") {
			continue
		}
		surface.Ops = append(surface.Ops, op)
	}
	if len(surface.Ops) == 0 {
		return nil, fmt.Errorf("tag %s exposes no rest routes", tag)
	}
	return surface, nil
}

package module

import (
	"strings"
	"testing"

	"github.com/discohaus/discopanel/internal/alias"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"google.golang.org/protobuf/proto"
)

func field(env string, mut func(*v1.ModuleConfigField)) *v1.ModuleConfigField {
	f := &v1.ModuleConfigField{
		Env:      env,
		Type:     v1.ModuleConfigFieldType_MODULE_CONFIG_FIELD_TYPE_STRING,
		Severity: v1.ModuleConfigSeverity_MODULE_CONFIG_SEVERITY_DENY,
	}
	if mut != nil {
		mut(f)
	}
	return f
}

func TestValidateConfigFields(t *testing.T) {
	tests := []struct {
		name     string
		fields   []*v1.ModuleConfigField
		env      map[string]string
		wantMsgs []string
	}{
		{
			name:     "required empty fails",
			fields:   []*v1.ModuleConfigField{field("KEY", func(f *v1.ModuleConfigField) { f.Required = true })},
			env:      map[string]string{"KEY": "  "},
			wantMsgs: []string{"KEY is required"},
		},
		{
			name:     "required filled passes",
			fields:   []*v1.ModuleConfigField{field("KEY", func(f *v1.ModuleConfigField) { f.Required = true })},
			env:      map[string]string{"KEY": "value"},
			wantMsgs: nil,
		},
		{
			name: "required unless truthy waives",
			fields: []*v1.ModuleConfigField{field("KEY", func(f *v1.ModuleConfigField) {
				f.Required = true
				f.RequiredUnless = "SKIP"
			})},
			env:      map[string]string{"KEY": "", "SKIP": "true"},
			wantMsgs: nil,
		},
		{
			name: "required unless false still requires",
			fields: []*v1.ModuleConfigField{field("KEY", func(f *v1.ModuleConfigField) {
				f.Required = true
				f.RequiredUnless = "SKIP"
			})},
			env:      map[string]string{"KEY": "", "SKIP": "false"},
			wantMsgs: []string{"KEY is required"},
		},
		{
			name: "required unless zero still requires",
			fields: []*v1.ModuleConfigField{field("KEY", func(f *v1.ModuleConfigField) {
				f.Required = true
				f.RequiredUnless = "SKIP"
			})},
			env:      map[string]string{"KEY": "", "SKIP": "0"},
			wantMsgs: []string{"KEY is required"},
		},
		{
			name: "int not numeric fails",
			fields: []*v1.ModuleConfigField{field("PORT", func(f *v1.ModuleConfigField) {
				f.Type = v1.ModuleConfigFieldType_MODULE_CONFIG_FIELD_TYPE_INT
			})},
			env:      map[string]string{"PORT": "abc"},
			wantMsgs: []string{"PORT must be a number"},
		},
		{
			name: "int below min fails",
			fields: []*v1.ModuleConfigField{field("PORT", func(f *v1.ModuleConfigField) {
				f.Type = v1.ModuleConfigFieldType_MODULE_CONFIG_FIELD_TYPE_INT
				f.Min = proto.Int32(1)
				f.Max = proto.Int32(65535)
			})},
			env:      map[string]string{"PORT": "0"},
			wantMsgs: []string{"PORT must be at least 1"},
		},
		{
			name: "int above max fails",
			fields: []*v1.ModuleConfigField{field("PORT", func(f *v1.ModuleConfigField) {
				f.Type = v1.ModuleConfigFieldType_MODULE_CONFIG_FIELD_TYPE_INT
				f.Max = proto.Int32(65535)
			})},
			env:      map[string]string{"PORT": "70000"},
			wantMsgs: []string{"PORT must be at most 65535"},
		},
		{
			name: "int in range passes",
			fields: []*v1.ModuleConfigField{field("PORT", func(f *v1.ModuleConfigField) {
				f.Type = v1.ModuleConfigFieldType_MODULE_CONFIG_FIELD_TYPE_INT
				f.Min = proto.Int32(1)
				f.Max = proto.Int32(65535)
			})},
			env:      map[string]string{"PORT": "25565"},
			wantMsgs: nil,
		},
		{
			name: "int empty optional passes",
			fields: []*v1.ModuleConfigField{field("PORT", func(f *v1.ModuleConfigField) {
				f.Type = v1.ModuleConfigFieldType_MODULE_CONFIG_FIELD_TYPE_INT
				f.Min = proto.Int32(1)
			})},
			env:      map[string]string{},
			wantMsgs: nil,
		},
		{
			name: "bool invalid fails",
			fields: []*v1.ModuleConfigField{field("FLAG", func(f *v1.ModuleConfigField) {
				f.Type = v1.ModuleConfigFieldType_MODULE_CONFIG_FIELD_TYPE_BOOL
			})},
			env:      map[string]string{"FLAG": "yes"},
			wantMsgs: []string{"FLAG must be true or false"},
		},
		{
			name: "select unknown option fails",
			fields: []*v1.ModuleConfigField{field("MODE", func(f *v1.ModuleConfigField) {
				f.Type = v1.ModuleConfigFieldType_MODULE_CONFIG_FIELD_TYPE_SELECT
				f.Options = []*v1.ModuleConfigOption{{Value: "auto"}, {Value: "p2p"}}
			})},
			env:      map[string]string{"MODE": "warp"},
			wantMsgs: []string{"MODE must be one of the listed options"},
		},
		{
			name: "select known option passes",
			fields: []*v1.ModuleConfigField{field("MODE", func(f *v1.ModuleConfigField) {
				f.Type = v1.ModuleConfigFieldType_MODULE_CONFIG_FIELD_TYPE_SELECT
				f.Options = []*v1.ModuleConfigOption{{Value: "auto"}, {Value: "p2p"}}
			})},
			env:      map[string]string{"MODE": "p2p"},
			wantMsgs: nil,
		},
		{
			name: "regex mismatch uses custom message",
			fields: []*v1.ModuleConfigField{field("IDS", func(f *v1.ModuleConfigField) {
				f.Regex = `^[0-9,\s]*$`
				f.RegexMessage = "IDS must be numeric"
			})},
			env:      map[string]string{"IDS": "abc"},
			wantMsgs: []string{"IDS must be numeric"},
		},
		{
			name: "regex match passes",
			fields: []*v1.ModuleConfigField{field("IDS", func(f *v1.ModuleConfigField) {
				f.Regex = `^[0-9,\s]*$`
			})},
			env:      map[string]string{"IDS": "123, 456"},
			wantMsgs: nil,
		},
		{
			name:     "nil and keyless fields skipped",
			fields:   []*v1.ModuleConfigField{nil, field("", nil)},
			env:      map[string]string{},
			wantMsgs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ValidateConfigFields(tt.fields, tt.env, nil)
			if len(got) != len(tt.wantMsgs) {
				t.Fatalf("got %d violations %v, want %d", len(got), got, len(tt.wantMsgs))
			}
			for i, v := range got {
				if v.Message != tt.wantMsgs[i] {
					t.Errorf("violation %d = %q, want %q", i, v.Message, tt.wantMsgs[i])
				}
			}
		})
	}
}

func TestValidateConfigFieldsResolvesAliases(t *testing.T) {
	f := field("HOST", func(f *v1.ModuleConfigField) { f.Required = true })
	ctx := alias.NewContext()
	ctx.Server = &v1.Server{ProxyHostnames: []string{"mc.example.com"}}
	env := map[string]string{"HOST": "{{server.proxy_hostnames}}"}
	if got := ValidateConfigFields([]*v1.ModuleConfigField{f}, env, ctx); len(got) != 0 {
		t.Fatalf("resolved alias should pass, got %v", got)
	}
	ctx.Server = &v1.Server{}
	if got := ValidateConfigFields([]*v1.ModuleConfigField{f}, env, ctx); len(got) != 1 {
		t.Fatalf("empty alias resolution should fail required, got %v", got)
	}
}

func TestFieldSeverityDefaultsToWarn(t *testing.T) {
	f := field("KEY", func(f *v1.ModuleConfigField) {
		f.Required = true
		f.Severity = v1.ModuleConfigSeverity_MODULE_CONFIG_SEVERITY_UNSPECIFIED
	})
	got := ValidateConfigFields([]*v1.ModuleConfigField{f}, map[string]string{}, nil)
	if len(got) != 1 || got[0].Severity != v1.ModuleConfigSeverity_MODULE_CONFIG_SEVERITY_WARN {
		t.Fatalf("unspecified severity should warn, got %v", got)
	}
	if err := DenyError(got); err != nil {
		t.Fatalf("warn violations must not deny, got %v", err)
	}
}

func TestDenyError(t *testing.T) {
	if err := DenyError(nil); err != nil {
		t.Fatalf("no violations should pass, got %v", err)
	}
	violations := []ConfigViolation{
		{Env: "A", Message: "A is required", Severity: v1.ModuleConfigSeverity_MODULE_CONFIG_SEVERITY_DENY},
		{Env: "B", Message: "B must be a number", Severity: v1.ModuleConfigSeverity_MODULE_CONFIG_SEVERITY_WARN},
		{Env: "C", Message: "C is required", Severity: v1.ModuleConfigSeverity_MODULE_CONFIG_SEVERITY_DENY},
	}
	err := DenyError(violations)
	if err == nil {
		t.Fatal("deny violations should error")
	}
	want := "module config invalid: A is required; C is required"
	if err.Error() != want {
		t.Fatalf("got %q, want %q", err.Error(), want)
	}
}

func TestValidateConfigFieldDefs(t *testing.T) {
	intField := field("N", func(f *v1.ModuleConfigField) {
		f.Type = v1.ModuleConfigFieldType_MODULE_CONFIG_FIELD_TYPE_INT
		f.Min = proto.Int32(10)
		f.Max = proto.Int32(1)
	})
	selectField := field("S", func(f *v1.ModuleConfigField) {
		f.Type = v1.ModuleConfigFieldType_MODULE_CONFIG_FIELD_TYPE_SELECT
	})
	tests := []struct {
		name    string
		fields  []*v1.ModuleConfigField
		wantErr string
	}{
		{"empty list passes", nil, ""},
		{"missing env", []*v1.ModuleConfigField{field(" ", nil)}, "env is required"},
		{"duplicate env", []*v1.ModuleConfigField{field("A", nil), field("A", nil)}, "duplicate config field A"},
		{"bad regex", []*v1.ModuleConfigField{field("A", func(f *v1.ModuleConfigField) { f.Regex = "[" })}, "regex invalid"},
		{"min exceeds max", []*v1.ModuleConfigField{intField}, "min exceeds max"},
		{"select without options", []*v1.ModuleConfigField{selectField}, "needs options"},
		{"valid fields pass", []*v1.ModuleConfigField{field("A", func(f *v1.ModuleConfigField) { f.Regex = "^x$" })}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateConfigFieldDefs(tt.fields)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("want nil, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("want error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

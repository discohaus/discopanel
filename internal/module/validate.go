package module

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/discohaus/discopanel/internal/alias"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// ConfigViolation is one failed config field check
type ConfigViolation struct {
	Env      string
	Message  string
	Severity v1.ModuleConfigSeverity
}

// Treats non empty and not false as truthy
func envTruthy(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	return v != "" && v != "false" && v != "0"
}

// Falls back to warn when severity is unspecified
func fieldSeverity(f *v1.ModuleConfigField) v1.ModuleConfigSeverity {
	if f.Severity == v1.ModuleConfigSeverity_MODULE_CONFIG_SEVERITY_UNSPECIFIED {
		return v1.ModuleConfigSeverity_MODULE_CONFIG_SEVERITY_WARN
	}
	return f.Severity
}

// Checks resolved env values against template config fields
func ValidateConfigFields(fields []*v1.ModuleConfigField, env map[string]string, aliasCtx *alias.Context) []ConfigViolation {
	var out []ConfigViolation
	add := func(f *v1.ModuleConfigField, msg string) {
		out = append(out, ConfigViolation{Env: f.Env, Message: msg, Severity: fieldSeverity(f)})
	}
	for _, f := range fields {
		if f == nil || f.Env == "" {
			continue
		}
		val := strings.TrimSpace(alias.Substitute(env[f.Env], aliasCtx))
		required := f.Required
		if required && f.RequiredUnless != "" && envTruthy(alias.Substitute(env[f.RequiredUnless], aliasCtx)) {
			required = false
		}
		if val == "" {
			if required {
				add(f, fmt.Sprintf("%s is required", f.Env))
			}
			continue
		}
		switch f.Type {
		case v1.ModuleConfigFieldType_MODULE_CONFIG_FIELD_TYPE_INT:
			n, err := strconv.ParseInt(val, 10, 64)
			if err != nil {
				add(f, fmt.Sprintf("%s must be a number", f.Env))
				continue
			}
			if f.Min != nil && n < int64(*f.Min) {
				add(f, fmt.Sprintf("%s must be at least %d", f.Env, *f.Min))
			}
			if f.Max != nil && n > int64(*f.Max) {
				add(f, fmt.Sprintf("%s must be at most %d", f.Env, *f.Max))
			}
		case v1.ModuleConfigFieldType_MODULE_CONFIG_FIELD_TYPE_BOOL:
			if val != "true" && val != "false" {
				add(f, fmt.Sprintf("%s must be true or false", f.Env))
			}
		case v1.ModuleConfigFieldType_MODULE_CONFIG_FIELD_TYPE_SELECT:
			ok := slices.ContainsFunc(f.Options, func(o *v1.ModuleConfigOption) bool {
				return o != nil && o.Value == val
			})
			if !ok {
				add(f, fmt.Sprintf("%s must be one of the listed options", f.Env))
			}
		}
		if f.Regex != "" {
			// Bad patterns are rejected at template save
			if re, err := regexp.Compile(f.Regex); err == nil && !re.MatchString(val) {
				msg := f.RegexMessage
				if msg == "" {
					msg = fmt.Sprintf("%s does not match the required format", f.Env)
				}
				add(f, msg)
			}
		}
	}
	return out
}

// Joins deny violations into one gate error
func DenyError(violations []ConfigViolation) error {
	var msgs []string
	for _, v := range violations {
		if v.Severity == v1.ModuleConfigSeverity_MODULE_CONFIG_SEVERITY_DENY {
			msgs = append(msgs, v.Message)
		}
	}
	if len(msgs) == 0 {
		return nil
	}
	return fmt.Errorf("module config invalid: %s", strings.Join(msgs, "; "))
}

// Checks field definitions are well formed at template save
func ValidateConfigFieldDefs(fields []*v1.ModuleConfigField) error {
	seen := make(map[string]bool)
	for _, f := range fields {
		if f == nil || strings.TrimSpace(f.Env) == "" {
			return errors.New("config field env is required")
		}
		if seen[f.Env] {
			return fmt.Errorf("duplicate config field %s", f.Env)
		}
		seen[f.Env] = true
		if f.Regex != "" {
			if _, err := regexp.Compile(f.Regex); err != nil {
				return fmt.Errorf("config field %s regex invalid: %w", f.Env, err)
			}
		}
		if f.Min != nil && f.Max != nil && *f.Min > *f.Max {
			return fmt.Errorf("config field %s min exceeds max", f.Env)
		}
		if f.Type == v1.ModuleConfigFieldType_MODULE_CONFIG_FIELD_TYPE_SELECT && len(f.Options) == 0 {
			return fmt.Errorf("config field %s needs options", f.Env)
		}
	}
	return nil
}

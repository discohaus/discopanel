package diagnostics

import (
	"fmt"
	"strings"

	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// Collects one check's verdict, evidence, and remedy
type check struct {
	runner *Runner
	state  *runState

	severity v1.DiagnosticSeverity
	summary  string
	remedy   string
	docs     string
	detail   []string
	facts    map[string]string
}

// Severity rank, higher wins when a check reports twice
func rank(s v1.DiagnosticSeverity) int {
	switch s {
	case v1.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_PASS:
		return 1
	case v1.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_INFO:
		return 2
	case v1.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_WARN:
		return 3
	case v1.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_FAIL:
		return 4
	case v1.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_SKIP:
		return 0
	}
	return -1
}

// Records a verdict, weaker verdicts never overwrite stronger
func (c *check) verdict(s v1.DiagnosticSeverity, format string, args ...any) {
	if rank(s) < rank(c.severity) {
		return
	}
	c.severity = s
	c.summary = fmt.Sprintf(format, args...)
}

func (c *check) pass(format string, args ...any) {
	c.verdict(v1.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_PASS, format, args...)
}

func (c *check) info(format string, args ...any) {
	c.verdict(v1.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_INFO, format, args...)
}

func (c *check) warn(format string, args ...any) {
	c.verdict(v1.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_WARN, format, args...)
}

func (c *check) fail(format string, args ...any) {
	c.verdict(v1.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_FAIL, format, args...)
}

// Skip only lands when nothing else was decided
func (c *check) skip(format string, args ...any) {
	if c.severity != v1.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_UNSPECIFIED {
		return
	}
	c.severity = v1.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_SKIP
	c.summary = fmt.Sprintf(format, args...)
}

// Sets the remedy and docs link
func (c *check) fix(remedy string, docs string) {
	c.remedy = remedy
	if docs != "" {
		c.docs = docs
	}
}

// Adds one evidence line
func (c *check) note(format string, args ...any) {
	c.detail = append(c.detail, fmt.Sprintf(format, args...))
}

// Records one key fact
func (c *check) fact(key string, value any) {
	switch v := value.(type) {
	case string:
		if v == "" {
			return
		}
		c.facts[key] = v
	default:
		c.facts[key] = fmt.Sprint(v)
	}
}

// Forgets everything, used after a panic
func (c *check) reset() {
	c.severity = v1.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_UNSPECIFIED
	c.summary = ""
	c.remedy = ""
	c.docs = ""
	c.detail = nil
	c.facts = map[string]string{}
}

// Wire form of the collected verdict
func (c *check) proto() *v1.DiagnosticCheck {
	return &v1.DiagnosticCheck{
		Severity: c.severity,
		Summary:  c.summary,
		Detail:   strings.Join(c.detail, "\n"),
		Remedy:   c.remedy,
		DocsUrl:  c.docs,
		Facts:    c.facts,
	}
}

// Trims a list, counting the rest
func abbreviate(items []string, limit int) string {
	if len(items) <= limit {
		return strings.Join(items, ", ")
	}
	return strings.Join(items[:limit], ", ") + fmt.Sprintf(" and %d more", len(items)-limit)
}

// Plural helper for summaries
func plural(n int, singular, pluralForm string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singular)
	}
	return fmt.Sprintf("%d %s", n, pluralForm)
}

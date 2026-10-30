package diagnostics

import (
	"fmt"
	"sort"
	"strings"

	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/protometa"
)

// Plain text rendering for bundles and terminals
func RenderText(report *v1.DiagnosticReport) string {
	if report == nil {
		return "no diagnostics report\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "DiscoPanel diagnostics (%s) version %s\n", report.Trigger, report.Version)
	if report.StartedAt != nil && report.FinishedAt != nil {
		fmt.Fprintf(&b, "started %s, took %s\n", report.StartedAt.AsTime().Format("2006-01-02 15:04:05 MST"),
			report.FinishedAt.AsTime().Sub(report.StartedAt.AsTime()).Round(1e8))
	}
	fmt.Fprintf(&b, "%d passed, %d info, %d warnings, %d failed, %d skipped\n\n",
		report.PassCount, report.InfoCount, report.WarnCount, report.FailCount, report.SkipCount)

	var category v1.DiagnosticCategory = -1
	for _, c := range report.Checks {
		if c.Category != category {
			category = c.Category
			fmt.Fprintf(&b, "== %s ==\n", protometa.Label(category))
		}
		fmt.Fprintf(&b, "[%s] %s (%s)\n", strings.ToUpper(protometa.Name(c.Severity)), c.Title, c.Id)
		fmt.Fprintf(&b, "  %s\n", c.Summary)
		if c.Remedy != "" {
			fmt.Fprintf(&b, "  fix: %s\n", c.Remedy)
		}
		if c.DocsUrl != "" {
			fmt.Fprintf(&b, "  docs: %s\n", c.DocsUrl)
		}
		keys := make([]string, 0, len(c.Facts))
		for k := range c.Facts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "  %s: %s\n", k, c.Facts[k])
		}
		if c.Detail != "" {
			for _, line := range strings.Split(c.Detail, "\n") {
				fmt.Fprintf(&b, "  | %s\n", line)
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

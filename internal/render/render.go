// Package render turns an analyzer's types.Report into command output. It
// is the only package that knows about colors, table widths, or JSON
// marshaling — analyzers stay renderer-agnostic.
package render

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/charmbracelet/lipgloss"

	"github.com/tylermac92/kscope/pkg/types"
)

// Severity-to-color mapping: the one place in kscope that owns it.
var (
	criticalStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
	warningStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	okStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	headerStyle   = lipgloss.NewStyle().Bold(true)
)

func severityStyle(s types.Severity) lipgloss.Style {
	switch s {
	case types.SeverityCritical:
		return criticalStyle
	case types.SeverityWarning:
		return warningStyle
	default:
		return okStyle
	}
}

// Render writes report to w in the given format ("table" or "json"; "table"
// is used if format is empty).
func Render(w io.Writer, report types.Report, format string) error {
	switch format {
	case "", "table":
		return renderTable(w, report)
	case "json":
		return renderJSON(w, report)
	default:
		return fmt.Errorf("unknown output format %q (want table or json)", format)
	}
}

func renderJSON(w io.Writer, report types.Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

func renderTable(w io.Writer, report types.Report) error {
	if len(report.Findings) == 0 {
		fmt.Fprintln(w, okStyle.Render("OK")+"  no issues found")
		return nil
	}

	fmt.Fprintln(w, headerStyle.Render(fmt.Sprintf("%-8s  %-12s  %-24s  %-24s  %s",
		"SEVERITY", "NAMESPACE", "RESOURCE", "NAME", "MESSAGE")))

	for _, f := range report.Findings {
		sev := severityStyle(f.Severity).Render(fmt.Sprintf("%-8s", f.Severity))
		fmt.Fprintf(w, "%s  %-12s  %-24s  %-24s  %s\n", sev, f.Namespace, f.Resource, f.Name, f.Message)
	}

	return nil
}

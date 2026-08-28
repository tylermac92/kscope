// Package types defines the exported, stable types shared across kscope's
// analyzers and safe for external consumers to import.
package types

// Severity classifies how urgently a Finding warrants attention.
type Severity string

const (
	// SeverityOK indicates the subject was checked and found healthy.
	SeverityOK Severity = "OK"
	// SeverityWarning indicates a condition worth investigating but not
	// immediately harmful.
	SeverityWarning Severity = "Warning"
	// SeverityCritical indicates a condition that is actively broken or
	// poses a significant risk.
	SeverityCritical Severity = "Critical"
)

// Finding is a single observation produced by an analyzer, scoped to one
// subject (a namespace/resource/name tuple) with a human-readable message.
type Finding struct {
	Severity  Severity `json:"severity"`
	Namespace string   `json:"namespace,omitempty"`
	Resource  string   `json:"resource,omitempty"`
	Name      string   `json:"name,omitempty"`
	Message   string   `json:"message"`
}

// Report is the flat, renderer-agnostic result of running an analyzer. It is
// the only structure internal/render is allowed to depend on for command
// output.
type Report struct {
	Findings []Finding `json:"findings"`
}

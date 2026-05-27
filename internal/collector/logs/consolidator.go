// SPDX-License-Identifier: Apache-2.0

package logs

import (
	"regexp"

	wirelogs "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// Consolidator merges language-specific or structural noise fragments into
// coherent templates. Each consolidator targets a specific pattern family
// (e.g., Go stack dumps, Python tracebacks).
type Consolidator interface {
	Name() string
	Consolidate(templates []*wirelogs.LogTemplate) []*wirelogs.LogTemplate
}

// DefaultConsolidators returns the standard set of consolidators in
// recommended execution order.
func DefaultConsolidators() []Consolidator {
	return []Consolidator{
		&goStackConsolidator{},
		&pythonTracebackConsolidator{},
	}
}

// RunConsolidators applies each consolidator in order to the template list.
func RunConsolidators(consolidators []Consolidator, templates []*wirelogs.LogTemplate) []*wirelogs.LogTemplate {
	for _, c := range consolidators {
		templates = c.Consolidate(templates)
	}
	return templates
}

// Shared stack trace detection patterns used by multiple consolidators.
var (
	reGoStack         = regexp.MustCompile(`^goroutine\s+\d+`)
	reExceptionHeader = regexp.MustCompile(
		`(?i)^(exception|error|panic|traceback|caused by|fatal)`)
)

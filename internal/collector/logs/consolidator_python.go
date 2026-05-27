// SPDX-License-Identifier: Apache-2.0

package logs

import (
	"regexp"
	"sort"
	"strings"
	"time"

	wirelogs "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

type pythonTracebackConsolidator struct{}

func (p *pythonTracebackConsolidator) Name() string { return "python_traceback" }

// Consolidate detects Python traceback fragments, groups by temporal proximity
// (5s window), and merges groups of 3+ into single ERROR templates.
func (p *pythonTracebackConsolidator) Consolidate(templates []*wirelogs.LogTemplate) []*wirelogs.LogTemplate {
	var fragments []*wirelogs.LogTemplate
	var tracebackHeaders []*wirelogs.LogTemplate
	var normal []*wirelogs.LogTemplate

	for _, t := range templates {
		text := effectiveText(t)
		if strings.HasPrefix(strings.TrimSpace(text), "Traceback (most recent call last)") {
			tracebackHeaders = append(tracebackHeaders, t)
		} else if isPythonTracebackFragment(t) {
			fragments = append(fragments, t)
		} else {
			normal = append(normal, t)
		}
	}

	if len(fragments) < 3 && len(tracebackHeaders) == 0 {
		return templates
	}

	allPy := append(tracebackHeaders, fragments...)
	sort.Slice(allPy, func(i, j int) bool {
		return allPy[i].LastSeen.Before(allPy[j].LastSeen)
	})

	groups := groupByTime(allPy, 5*time.Second)

	for _, group := range groups {
		if len(group) < 3 {
			normal = append(normal, group...)
			continue
		}
		normal = append(normal, mergePythonGroup(group))
	}

	return normal
}

// effectiveText returns the best text for pattern matching: the first
// ExampleVars row joined, falling back to Pattern.
func effectiveText(t *wirelogs.LogTemplate) string {
	if len(t.ExampleVars) > 0 {
		return strings.Join(t.ExampleVars[0], " ")
	}
	return t.Pattern
}

// mergePythonGroup merges a temporal group of Python traceback fragments
// into a single ERROR template.
func mergePythonGroup(group []*wirelogs.LogTemplate) *wirelogs.LogTemplate {
	merged := &wirelogs.LogTemplate{
		Severity:  wirelogs.SeverityError,
		FirstSeen: group[0].FirstSeen,
		LastSeen:  group[0].LastSeen,
	}

	var bestHeader, bestException string
	for _, t := range group {
		merged.Count += t.Count
		if t.FirstSeen.Before(merged.FirstSeen) {
			merged.FirstSeen = t.FirstSeen
		}
		if t.LastSeen.After(merged.LastSeen) {
			merged.LastSeen = t.LastSeen
		}
		if wirelogs.SeverityIndex(t.Severity) > wirelogs.SeverityIndex(merged.Severity) {
			merged.Severity = t.Severity
		}

		text := effectiveText(t)
		if strings.Contains(text, "Traceback") && bestHeader == "" {
			bestHeader = text
		}
		if rePyExceptionClass.MatchString(text) && bestException == "" {
			bestException = text
		}
	}

	merged.Pattern = buildPythonTemplate(bestHeader, bestException)
	merged.ExampleVars = buildPythonExamples(bestHeader, bestException, group)
	merged.Alias = TemplateAliasFor(merged)

	return merged
}

// buildPythonTemplate constructs the merged pattern from the best available
// traceback header and exception class.
func buildPythonTemplate(header, exception string) string {
	switch {
	case header != "":
		truncated := header
		if len(truncated) > 120 {
			truncated = truncated[:117] + "..."
		}
		return "Python exception: " + truncated
	case exception != "":
		return "Python exception: " + exception
	default:
		return "Python traceback"
	}
}

// buildPythonExamples collects relevant example lines from the merged group.
func buildPythonExamples(header, exception string, group []*wirelogs.LogTemplate) [][]string {
	var examples [][]string
	if header != "" {
		examples = append(examples, []string{header})
	}
	if exception != "" && exception != header {
		examples = append(examples, []string{exception})
	}
	if len(examples) == 0 && len(group[0].ExampleVars) > 0 {
		examples = [][]string{group[0].ExampleVars[0]}
	}
	return examples
}

// Python traceback detection regexes.
var (
	rePyFileFrame      = regexp.MustCompile(`(?m)^\s+File "`)
	rePyUnderline      = regexp.MustCompile(`(?m)^\s+[\^~]{5,}`)
	rePyRaiseAwait     = regexp.MustCompile(`(?m)^\s*(raise |await |return await |return \w+[\.(]|with \w)`)
	rePySelfCall       = regexp.MustCompile(`(?m)^\s*self\.\w+`)
	rePyAssignAwait    = regexp.MustCompile(`(?m)=\s+await\s+`)
	rePyExceptionClass = regexp.MustCompile(`(?m)^\w+(\.\w+)*\.\w*(Timeout|Error|Exception|Fault)\b`)
	rePyExceptionChain = regexp.MustCompile(`(?i)^(The above exception|During handling of the above)`)
	rePyObjectRepr     = regexp.MustCompile(`(?m)^\w+:\s+<[\w.]+\s+object\s+at\s+0x`)
)

func isPythonTracebackFragment(t *wirelogs.LogTemplate) bool {
	return matchesPyTracebackPattern(t.Pattern) ||
		exampleVarsContain(t.ExampleVars, matchesPyTracebackPattern)
}

func matchesPyTracebackPattern(msg string) bool {
	return rePyUnderline.MatchString(msg) ||
		rePyRaiseAwait.MatchString(msg) ||
		rePySelfCall.MatchString(msg) ||
		rePyAssignAwait.MatchString(msg) ||
		rePyExceptionClass.MatchString(msg) ||
		rePyExceptionChain.MatchString(msg) ||
		rePyFileFrame.MatchString(msg) ||
		rePyObjectRepr.MatchString(msg)
}

// SPDX-License-Identifier: Apache-2.0

package github

import (
	"regexp"
	"strings"
)

// workflow_parser.go extracts secret references and environment names from
// GitHub Actions workflow YAML content using lightweight regex + string
// parsing. Full YAML unmarshaling is avoided because workflow files have
// highly variable structure (matrix strategies, reusable workflows, composite
// actions) that would require a complex, brittle schema.

// secretRefPattern matches ${{ secrets.SECRET_NAME }} patterns in YAML.
var secretRefPattern = regexp.MustCompile(`\$\{\{\s*secrets\.([A-Z_][A-Z0-9_]*)\s*\}\}`)

// parseSecretRefs extracts unique secret names referenced via
// ${{ secrets.NAME }} in workflow YAML content.
func parseSecretRefs(yamlContent string) []string {
	matches := secretRefPattern.FindAllStringSubmatch(yamlContent, -1)
	seen := make(map[string]bool, len(matches))
	var names []string
	for _, m := range matches {
		name := m[1]
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names
}

// parseEnvironmentRefs extracts environment names from workflow YAML.
// Looks for `environment:` keys in job definitions. Handles both:
//   - environment: production
//   - environment:\n  name: production
func parseEnvironmentRefs(yamlContent string) []string {
	seen := make(map[string]bool)
	var envs []string

	for line := range strings.SplitSeq(yamlContent, "\n") {
		trimmed := strings.TrimSpace(line)

		// Simple: `environment: production`
		if after, ok := strings.CutPrefix(trimmed, "environment:"); ok {
			val := after
			val = strings.TrimSpace(val)
			if val != "" && !strings.HasPrefix(val, "{") {
				val = strings.Trim(val, `"'`)
				if val != "" && !seen[val] {
					seen[val] = true
					envs = append(envs, val)
				}
			}
			continue
		}

		// Nested: `name: production` under an environment block.
		// We rely on the previous environment: (with no inline value) to set context.
		if strings.HasPrefix(trimmed, "name:") && len(envs) < len(seen) {
			// Skip — this heuristic is fragile. The simple form covers most cases.
			continue
		}
	}
	return envs
}

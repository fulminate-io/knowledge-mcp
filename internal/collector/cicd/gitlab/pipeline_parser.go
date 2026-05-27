// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"regexp"

	"gopkg.in/yaml.v3"
)

// pipelineConfig is the parsed representation of a .gitlab-ci.yml file.
type pipelineConfig struct {
	Stages   []string
	Jobs     []jobDef
	Includes []includeEntry
}

// jobDef holds the parsed fields of a single CI job definition.
type jobDef struct {
	Name        string
	Stage       string
	Image       string
	Tags        []string
	Environment string
	Services    []string
	Script      []string
	VarRefs     []string // variable names referenced via ${VAR} or $VAR in scripts
}

// includeEntry represents a single include: directive.
type includeEntry struct {
	Local    string
	Remote   string
	Template string
	Project  string
	File     string
}

// varRefPattern matches ${VAR_NAME} and $VAR_NAME in script lines.
var varRefPattern = regexp.MustCompile(`\$\{?([A-Z_][A-Z0-9_]*)\}?`)

// knownCIVars are GitLab CI predefined variables that should not be treated
// as user-defined secret references.
var knownCIVars = map[string]bool{
	"CI":                                  true,
	"CI_COMMIT_SHA":                       true,
	"CI_COMMIT_REF_NAME":                  true,
	"CI_COMMIT_REF_SLUG":                  true,
	"CI_COMMIT_BRANCH":                    true,
	"CI_COMMIT_TAG":                       true,
	"CI_COMMIT_MESSAGE":                   true,
	"CI_PIPELINE_ID":                      true,
	"CI_PIPELINE_SOURCE":                  true,
	"CI_PROJECT_ID":                       true,
	"CI_PROJECT_NAME":                     true,
	"CI_PROJECT_PATH":                     true,
	"CI_PROJECT_DIR":                      true,
	"CI_PROJECT_URL":                      true,
	"CI_PROJECT_NAMESPACE":                true,
	"CI_JOB_ID":                           true,
	"CI_JOB_NAME":                         true,
	"CI_JOB_STAGE":                        true,
	"CI_JOB_TOKEN":                        true,
	"CI_JOB_URL":                          true,
	"CI_REGISTRY":                         true,
	"CI_REGISTRY_IMAGE":                   true,
	"CI_SERVER_URL":                       true,
	"CI_API_V4_URL":                       true,
	"CI_ENVIRONMENT_NAME":                 true,
	"CI_ENVIRONMENT_SLUG":                 true,
	"CI_ENVIRONMENT_URL":                  true,
	"CI_DEFAULT_BRANCH":                   true,
	"CI_MERGE_REQUEST_IID":                true,
	"CI_MERGE_REQUEST_SOURCE_BRANCH_NAME": true,
	"CI_RUNNER_ID":                        true,
	"CI_RUNNER_TAGS":                      true,
	"GITLAB_USER_LOGIN":                   true,
	"GITLAB_USER_EMAIL":                   true,
	"HOME":                                true,
	"PATH":                                true,
	"PWD":                                 true,
	"SHELL":                               true,
}

// parseGitLabCI parses raw .gitlab-ci.yml bytes into a pipelineConfig.
// Unknown top-level keys are treated as job definitions (GitLab convention).
func parseGitLabCI(raw []byte) (*pipelineConfig, error) {
	var doc map[string]yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}

	cfg := &pipelineConfig{}
	parseStages(doc, cfg)
	parseIncludes(doc, cfg)
	parseJobs(doc, cfg)
	return cfg, nil
}

// reservedKeys are top-level keys that are not job definitions.
var reservedKeys = map[string]bool{
	"stages": true, "include": true, "variables": true,
	"default": true, "workflow": true, "image": true,
	"services": true, "before_script": true, "after_script": true,
	"cache": true, "pages": true,
}

// parseStages extracts the stages list from the YAML doc.
func parseStages(doc map[string]yaml.Node, cfg *pipelineConfig) {
	node, ok := doc["stages"]
	if !ok {
		return
	}
	var stages []string
	if err := node.Decode(&stages); err == nil {
		cfg.Stages = stages
	}
}

// parseIncludes extracts include directives from the YAML doc.
func parseIncludes(doc map[string]yaml.Node, cfg *pipelineConfig) {
	node, ok := doc["include"]
	if !ok {
		return
	}
	cfg.Includes = decodeIncludes(&node)
}

// decodeIncludes handles the various include formats (string, map, list).
func decodeIncludes(node *yaml.Node) []includeEntry {
	// Single string: include: "path.yml"
	if node.Kind == yaml.ScalarNode {
		return []includeEntry{{Local: node.Value}}
	}

	// List of includes
	if node.Kind == yaml.SequenceNode {
		var entries []includeEntry
		for _, child := range node.Content {
			entries = append(entries, decodeIncludeItem(child)...)
		}
		return entries
	}

	// Single mapping
	if node.Kind == yaml.MappingNode {
		return decodeIncludeItem(node)
	}

	return nil
}

// decodeIncludeItem decodes a single include item (string or mapping).
func decodeIncludeItem(node *yaml.Node) []includeEntry {
	if node.Kind == yaml.ScalarNode {
		return []includeEntry{{Local: node.Value}}
	}

	var m map[string]string
	if err := node.Decode(&m); err != nil {
		return nil
	}

	return []includeEntry{{
		Local:    m["local"],
		Remote:   m["remote"],
		Template: m["template"],
		Project:  m["project"],
		File:     m["file"],
	}}
}

// parseJobs extracts job definitions from non-reserved top-level keys.
func parseJobs(doc map[string]yaml.Node, cfg *pipelineConfig) {
	for key, node := range doc {
		if key == "" || reservedKeys[key] || key[0] == '.' {
			continue // skip empty, reserved, and hidden (template) keys
		}
		job := decodeJob(key, &node)
		if job != nil {
			cfg.Jobs = append(cfg.Jobs, *job)
		}
	}
}

// decodeJob parses a single job definition from a YAML mapping node.
func decodeJob(name string, node *yaml.Node) *jobDef {
	if node.Kind != yaml.MappingNode {
		return nil
	}

	var raw struct {
		Stage       string   `yaml:"stage"`
		Image       string   `yaml:"image"`
		Tags        []string `yaml:"tags"`
		Environment any      `yaml:"environment"`
		Services    []any    `yaml:"services"`
		Script      any      `yaml:"script"`
	}
	if err := node.Decode(&raw); err != nil {
		return nil
	}

	job := &jobDef{
		Name:  name,
		Stage: raw.Stage,
		Image: raw.Image,
		Tags:  raw.Tags,
	}

	job.Environment = decodeEnvironment(raw.Environment)
	job.Services = decodeServices(raw.Services)
	job.Script = decodeScript(raw.Script)
	job.VarRefs = extractVarRefs(job.Script)
	return job
}

// decodeEnvironment extracts the environment name from either a string or map.
func decodeEnvironment(v any) string {
	switch e := v.(type) {
	case string:
		return e
	case map[string]any:
		if name, ok := e["name"].(string); ok {
			return name
		}
	}
	return ""
}

// decodeServices extracts service names from the services list.
func decodeServices(items []any) []string {
	var svc []string
	for _, item := range items {
		switch v := item.(type) {
		case string:
			svc = append(svc, v)
		case map[string]any:
			if name, ok := v["name"].(string); ok {
				svc = append(svc, name)
			}
		}
	}
	return svc
}

// decodeScript normalizes script to a string slice.
func decodeScript(v any) []string {
	switch s := v.(type) {
	case string:
		return []string{s}
	case []any:
		var lines []string
		for _, item := range s {
			if str, ok := item.(string); ok {
				lines = append(lines, str)
			}
		}
		return lines
	}
	return nil
}

// extractVarRefs finds user-defined variable references in script lines.
func extractVarRefs(scripts []string) []string {
	seen := make(map[string]bool)
	var refs []string
	for _, line := range scripts {
		for _, m := range varRefPattern.FindAllStringSubmatch(line, -1) {
			name := m[1]
			if !knownCIVars[name] && !seen[name] {
				seen[name] = true
				refs = append(refs, name)
			}
		}
	}
	return refs
}

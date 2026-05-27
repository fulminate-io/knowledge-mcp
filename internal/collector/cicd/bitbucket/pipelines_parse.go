// SPDX-License-Identifier: Apache-2.0

package bitbucket

import (
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// pipelinesFile represents the top-level bitbucket-pipelines.yml structure.
type pipelinesFile struct {
	Pipelines pipelinesDef `yaml:"pipelines"`
}

// pipelinesDef holds all pipeline trigger definitions.
type pipelinesDef struct {
	Default      []pipelineStep            `yaml:"default"`
	Branches     map[string][]pipelineStep `yaml:"branches"`
	PullRequests map[string][]pipelineStep `yaml:"pull-requests"`
	Custom       map[string][]pipelineStep `yaml:"custom"`
	Tags         map[string][]pipelineStep `yaml:"tags"`
}

// pipelineStep is one step within a pipeline definition.
type pipelineStep struct {
	Step *stepBody `yaml:"step"`
	// Parallel and Stage are ignored for now.
}

// stepBody holds the actual step configuration.
type stepBody struct {
	Name       string   `yaml:"name"`
	Script     []string `yaml:"script"`
	Deployment string   `yaml:"deployment"`
	RunsOn     []string `yaml:"runs-on"`
	Services   []string `yaml:"services"`
	Caches     []string `yaml:"caches"`
}

// parsedPipeline is a flattened pipeline definition extracted from YAML.
type parsedPipeline struct {
	Name       string // e.g. "default", "branches/main", "custom/deploy"
	Steps      []parsedStep
	TriggerKey string // the map key (branch pattern, tag pattern, etc.)
}

// parsedStep holds extracted step info.
type parsedStep struct {
	Name       string
	Deployment string
	RunsOn     []string
	Services   []string
	Caches     []string
	VarRefs    []string // variable references found in scripts
}

// parsePipelinesYAML parses the raw bitbucket-pipelines.yml content.
func parsePipelinesYAML(data []byte) (*pipelinesFile, error) {
	var pf pipelinesFile
	if err := yaml.Unmarshal(data, &pf); err != nil {
		return nil, err
	}
	return &pf, nil
}

// extractPipelines flattens pipelinesDef into a slice of parsedPipeline.
func extractPipelines(def pipelinesDef) []parsedPipeline {
	var out []parsedPipeline

	if len(def.Default) > 0 {
		out = append(out, buildParsedPipeline("default", "", def.Default))
	}

	out = append(out, extractMapPipelines("branches", def.Branches)...)
	out = append(out, extractMapPipelines("pull-requests", def.PullRequests)...)
	out = append(out, extractMapPipelines("custom", def.Custom)...)
	out = append(out, extractMapPipelines("tags", def.Tags)...)

	return out
}

// extractMapPipelines converts a map of pipeline steps into parsedPipeline entries.
func extractMapPipelines(prefix string, m map[string][]pipelineStep) []parsedPipeline {
	var out []parsedPipeline
	for key, steps := range m {
		name := prefix + "/" + key
		out = append(out, buildParsedPipeline(name, key, steps))
	}
	return out
}

// buildParsedPipeline converts a slice of pipelineStep into a parsedPipeline.
func buildParsedPipeline(name, triggerKey string, steps []pipelineStep) parsedPipeline {
	pp := parsedPipeline{Name: name, TriggerKey: triggerKey}
	for _, s := range steps {
		if s.Step == nil {
			continue
		}
		ps := parsedStep{
			Name:       s.Step.Name,
			Deployment: s.Step.Deployment,
			RunsOn:     s.Step.RunsOn,
			Services:   s.Step.Services,
			Caches:     s.Step.Caches,
			VarRefs:    extractVarRefs(s.Step.Script),
		}
		pp.Steps = append(pp.Steps, ps)
	}
	return pp
}

// varRefPattern matches $VAR or ${VAR} references in script commands.
var varRefPattern = regexp.MustCompile(`\$\{?([A-Z_][A-Z0-9_]*)\}?`)

// extractVarRefs scans script lines for variable references.
func extractVarRefs(lines []string) []string {
	seen := make(map[string]struct{})
	var refs []string
	for _, line := range lines {
		for _, match := range varRefPattern.FindAllStringSubmatch(line, -1) {
			varName := match[1]
			// Skip common shell builtins.
			if isShellBuiltin(varName) {
				continue
			}
			if _, ok := seen[varName]; !ok {
				seen[varName] = struct{}{}
				refs = append(refs, varName)
			}
		}
	}
	return refs
}

// isShellBuiltin returns true for common shell variables that are not CI/CD secrets.
func isShellBuiltin(name string) bool {
	switch strings.ToUpper(name) {
	case "HOME", "PATH", "PWD", "SHELL", "USER", "HOSTNAME",
		"LANG", "TERM", "TMPDIR", "OLDPWD", "SHLVL", "IFS",
		"PIPESTATUS", "BITBUCKET_CLONE_DIR", "BITBUCKET_WORKSPACE",
		"BITBUCKET_REPO_SLUG", "BITBUCKET_COMMIT", "BITBUCKET_BRANCH",
		"BITBUCKET_TAG", "BITBUCKET_PR_ID", "BITBUCKET_BUILD_NUMBER",
		"BITBUCKET_PIPELINE_UUID", "BITBUCKET_STEP_UUID",
		"BITBUCKET_REPO_FULL_NAME", "BITBUCKET_REPO_UUID",
		"BITBUCKET_DEPLOYMENT_ENVIRONMENT",
		"BITBUCKET_REPO_IS_PRIVATE", "BITBUCKET_GIT_HTTP_ORIGIN",
		"BITBUCKET_PROJECT_KEY", "BITBUCKET_EXIT_CODE",
		"BITBUCKET_STEP_TRIGGERER_UUID", "BITBUCKET_PARALLEL_STEP",
		"BITBUCKET_PARALLEL_STEP_COUNT", "CI":
		return true
	}
	return false
}

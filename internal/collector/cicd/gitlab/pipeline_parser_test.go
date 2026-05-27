// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"sort"
	"testing"
)

func TestParseGitLabCI_SimpleJobsAndStages(t *testing.T) {
	yaml := []byte(`
stages:
  - build
  - test
  - deploy

build_job:
  stage: build
  image: golang:1.22
  script:
    - go build ./...

test_job:
  stage: test
  script:
    - go test ./...

deploy_job:
  stage: deploy
  environment: production
  tags:
    - docker
    - linux
  script:
    - echo "deploying to $DEPLOY_HOST"
    - 'curl -H "Authorization: Bearer $API_TOKEN" https://api.example.com'
`)

	cfg, err := parseGitLabCI(yaml)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cfg.Stages) != 3 {
		t.Errorf("expected 3 stages, got %d", len(cfg.Stages))
	}
	if cfg.Stages[0] != "build" || cfg.Stages[1] != "test" || cfg.Stages[2] != "deploy" {
		t.Errorf("unexpected stages: %v", cfg.Stages)
	}

	if len(cfg.Jobs) != 3 {
		t.Fatalf("expected 3 jobs, got %d", len(cfg.Jobs))
	}

	// Find the deploy_job to check its fields.
	var deployJob *jobDef
	for i := range cfg.Jobs {
		if cfg.Jobs[i].Name == "deploy_job" {
			deployJob = &cfg.Jobs[i]
			break
		}
	}
	if deployJob == nil {
		t.Fatal("deploy_job not found")
	}

	if deployJob.Stage != "deploy" {
		t.Errorf("expected stage 'deploy', got %q", deployJob.Stage)
	}
	if deployJob.Environment != "production" {
		t.Errorf("expected environment 'production', got %q", deployJob.Environment)
	}
	if len(deployJob.Tags) != 2 {
		t.Errorf("expected 2 tags, got %d", len(deployJob.Tags))
	}
	if len(deployJob.VarRefs) < 2 {
		t.Errorf("expected at least 2 var refs (DEPLOY_HOST, API_TOKEN), got %v", deployJob.VarRefs)
	}
}

func TestParseGitLabCI_WithIncludes(t *testing.T) {
	yaml := []byte(`
include:
  - local: ".ci/build.yml"
  - template: "Auto-DevOps.gitlab-ci.yml"
  - remote: "https://example.com/ci.yml"
  - project: "group/shared-ci"
    file: "/templates/deploy.yml"

stages:
  - build

build:
  script: echo hello
`)

	cfg, err := parseGitLabCI(yaml)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cfg.Includes) != 4 {
		t.Fatalf("expected 4 includes, got %d", len(cfg.Includes))
	}

	if cfg.Includes[0].Local != ".ci/build.yml" {
		t.Errorf("expected local include, got %+v", cfg.Includes[0])
	}
	if cfg.Includes[1].Template != "Auto-DevOps.gitlab-ci.yml" {
		t.Errorf("expected template include, got %+v", cfg.Includes[1])
	}
	if cfg.Includes[2].Remote != "https://example.com/ci.yml" {
		t.Errorf("expected remote include, got %+v", cfg.Includes[2])
	}
	if cfg.Includes[3].Project != "group/shared-ci" || cfg.Includes[3].File != "/templates/deploy.yml" {
		t.Errorf("expected project include, got %+v", cfg.Includes[3])
	}
}

func TestParseGitLabCI_EnvironmentAsMap(t *testing.T) {
	yaml := []byte(`
deploy:
  script: deploy.sh
  environment:
    name: staging
    url: https://staging.example.com
`)

	cfg, err := parseGitLabCI(yaml)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cfg.Jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(cfg.Jobs))
	}
	if cfg.Jobs[0].Environment != "staging" {
		t.Errorf("expected environment 'staging', got %q", cfg.Jobs[0].Environment)
	}
}

func TestParseGitLabCI_SkipsHiddenJobs(t *testing.T) {
	yaml := []byte(`
.template:
  image: ruby:3.0

build:
  script: make build
`)

	cfg, err := parseGitLabCI(yaml)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cfg.Jobs) != 1 {
		t.Fatalf("expected 1 job (hidden should be skipped), got %d", len(cfg.Jobs))
	}
	if cfg.Jobs[0].Name != "build" {
		t.Errorf("expected job 'build', got %q", cfg.Jobs[0].Name)
	}
}

func TestParseGitLabCI_SkipsReservedKeys(t *testing.T) {
	yaml := []byte(`
variables:
  GLOBAL_VAR: "value"
default:
  image: alpine
workflow:
  rules:
    - if: $CI_COMMIT_BRANCH

build:
  script: make
`)

	cfg, err := parseGitLabCI(yaml)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cfg.Jobs) != 1 {
		t.Fatalf("expected 1 job (reserved keys skipped), got %d", len(cfg.Jobs))
	}
}

func TestParseGitLabCI_Services(t *testing.T) {
	yaml := []byte(`
test:
  services:
    - postgres:14
    - name: redis:7
  script: pytest
`)

	cfg, err := parseGitLabCI(yaml)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cfg.Jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(cfg.Jobs))
	}
	sort.Strings(cfg.Jobs[0].Services)
	if len(cfg.Jobs[0].Services) != 2 {
		t.Errorf("expected 2 services, got %d", len(cfg.Jobs[0].Services))
	}
}

func TestExtractVarRefs_FiltersKnownVars(t *testing.T) {
	scripts := []string{
		"echo $CI_COMMIT_SHA",
		"echo $MY_SECRET",
		"curl -H $API_KEY https://api.example.com",
		"echo ${HOME} and ${DEPLOY_TOKEN}",
	}

	refs := extractVarRefs(scripts)
	expected := map[string]bool{"MY_SECRET": true, "API_KEY": true, "DEPLOY_TOKEN": true}

	if len(refs) != len(expected) {
		t.Fatalf("expected %d refs, got %d: %v", len(expected), len(refs), refs)
	}
	for _, ref := range refs {
		if !expected[ref] {
			t.Errorf("unexpected var ref: %s", ref)
		}
	}
}

func TestParseGitLabCI_SingleStringInclude(t *testing.T) {
	yaml := []byte(`
include: "common.yml"

build:
  script: make
`)

	cfg, err := parseGitLabCI(yaml)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cfg.Includes) != 1 {
		t.Fatalf("expected 1 include, got %d", len(cfg.Includes))
	}
	if cfg.Includes[0].Local != "common.yml" {
		t.Errorf("expected local include 'common.yml', got %+v", cfg.Includes[0])
	}
}

func TestParseGitLabCI_InvalidYAML(t *testing.T) {
	yaml := []byte(`
invalid: yaml: [broken
`)

	_, err := parseGitLabCI(yaml)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

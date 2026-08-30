// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestSGAttachmentEdgeCoverage is a defensive regression test that scans
// the cloud/aws/ source files for collectors that should emit
// EdgeUsesSecurityGroup and fails if any expected collector is
// missing the edge emission. It protects against refactors that
// accidentally drop the attachment edge and leave the SG reachability
// analyzer with a silent blind spot for that resource type.
//
// The regex accepts either PACKAGE QUALIFIER — kgtypes. (the client leaf
// package that owns the EdgeType vocabulary, which every collector file here
// uses today) or store. (the historical re-export alias). Those are Go
// qualifiers, not directory paths.
//
// The test is deliberately string-based (grep on disk) rather than
// runtime-based because several of these collectors need AWS API
// credentials to exercise and we want the guarantee to hold without
// having to stand up fake SDK clients for every one.
func TestSGAttachmentEdgeCoverage(t *testing.T) {
	// The set below MUST include every AWS resource type with an
	// attachment-to-SG relationship that the reachability analyzer
	// depends on. Adding a new attached resource type? Add its
	// collector file here.
	expected := map[string]string{
		"ec2.go":         "ec2-instance",
		"rds.go":         "rds-instance",
		"lambda.go":      "lambda-function",
		"elbv2.go":       "elbv2-loadbalancer",
		"ecs_edges.go":   "ecs-task/service",
		"eks.go":         "eks-cluster",
		"elasticache.go": "elasticache-cluster",
		"opensearch.go":  "opensearch-domain",
		"efs.go":         "efs-filesystem",
	}

	// Resolve cloud/aws source directory. The test runs with cwd set to
	// the package directory by `go test`.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	edgePattern := regexp.MustCompile(`(store|kgtypes)\.EdgeUsesSecurityGroup`)

	for file, label := range expected {
		path := filepath.Join(cwd, file)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("expected collector file missing: %s (%s): %v", file, label, err)
			continue
		}
		src := string(data)
		if !strings.Contains(src, "package aws") {
			t.Errorf("%s: not a package aws source file", file)
			continue
		}
		if !edgePattern.MatchString(src) {
			t.Errorf("%s (%s): missing EdgeUsesSecurityGroup emission", file, label)
		}
	}
}

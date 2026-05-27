// SPDX-License-Identifier: Apache-2.0

package cloud

// SummarizeAllowlist enumerates ResourceType strings that are intentionally
// shipped without a registered summarizer. The Phase 8 audit treats these as
// waived (generic fallback is acceptable forever for these types).
//
// Add an entry only with a one-line comment explaining why fallback is OK.
var SummarizeAllowlist = map[string]bool{
	// K8s postpopulate_external_patterns.go synthesizes cross-cloud proxy nodes
	// for AWS services referenced from K8s workloads. These are placeholder IDs,
	// not real cloud resources -- fallback "<rt> <name>" is sufficient.
	"aws:s3:bucket":           true,
	"aws:rds:instance":        true,
	"aws:elasticache:cluster": true,
	"aws:sqs:queue":           true,
	"aws:dynamodb:service":    true,
	"aws:ebs:volume":          true,

	// K8s postpopulate_pv_disk.go synthesizes Azure-side disk proxies.
	"azure:compute:disk": true,

	// K8s postpopulate_workload_identity.go synthesizes cross-cloud IAM proxies.
	"iam:role":              true,
	"azure:managedidentity": true,
}

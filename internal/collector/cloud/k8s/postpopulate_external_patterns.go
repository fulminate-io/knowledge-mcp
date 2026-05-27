// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"regexp"
	"strings"
)

// externalTarget is the output of a pattern matcher: the canonical
// resource identifier the workload connects to, plus enough provenance
// for edge evidence. Account may be empty (dangling proxy) when the
// identifier alone doesn't carry the account/subscription.
type externalTarget struct {
	Account      string // proxy graph name (empty for dangling)
	ID           string // canonical cloud resource ID (ARN, selfLink, Azure resource ID, ...)
	ResourceType string // e.g. "aws:s3:bucket", "gcp:sql:instance"
	Provider     string // "aws" | "gcp" | "azure"
	PatternName  string // "aws:s3", "gcp:cloudsql", "aws:rds", ...
	Matched      string // the substring of the original value that matched — NOT the full env value
}

// externalPattern is a single pattern matcher. MatchAll runs every
// pattern against every env value; the set of matches (possibly
// multiple per env value) becomes the edge set.
type externalPattern struct {
	Name string
	Scan func(value string) []externalTarget
}

// externalPatterns is the registry of URI → cloud resource patterns.
// Order matters only for documentation readability — each scanner is
// independent and any pattern that finds a substring emits its own
// targets. Results across patterns are deduplicated by the caller.
//
// Secret safety: each Scan function returns the matched URI substring
// only (via externalTarget.Matched). Callers MUST NOT pass the full
// env value into edge evidence or resource metadata — the URI itself
// is a public endpoint, but the surrounding value may embed credentials
// (userinfo in a DB URL, query-string secrets, etc).
var externalPatterns = []externalPattern{
	{Name: "aws:s3", Scan: scanAWSS3},
	{Name: "aws:rds", Scan: scanAWSRDS},
	{Name: "aws:elasticache", Scan: scanAWSElastiCache},
	{Name: "aws:sqs", Scan: scanAWSSQS},
	{Name: "aws:dynamodb", Scan: scanAWSDynamoDB},
	{Name: "aws:arn", Scan: scanAWSARN},
	{Name: "gcp:gcs", Scan: scanGCPGCS},
	{Name: "gcp:cloudsql", Scan: scanGCPCloudSQL},
	{Name: "gcp:pubsub", Scan: scanGCPPubSub},
	{Name: "azure:blob", Scan: scanAzureBlob},
	{Name: "azure:sql", Scan: scanAzureSQL},
	{Name: "azure:redis", Scan: scanAzureRedis},
	{Name: "azure:servicebus", Scan: scanAzureServiceBus},
}

// scanAllPatterns runs every pattern against value and returns the
// merged (deduplicated-by-ID) result set.
func scanAllPatterns(value string) []externalTarget {
	if value == "" {
		return nil
	}
	var out []externalTarget
	seen := make(map[string]struct{})
	for _, p := range externalPatterns {
		for _, t := range p.Scan(value) {
			if _, dup := seen[t.ID]; dup {
				continue
			}
			seen[t.ID] = struct{}{}
			out = append(out, t)
		}
	}
	return out
}

// --- AWS S3 ---------------------------------------------------------

var (
	reAWSS3Scheme  = regexp.MustCompile(`s3://([a-z0-9.\-]+)(?:/|$)`)
	reAWSS3Virtual = regexp.MustCompile(`([a-z0-9.\-]+)\.s3(?:\.[a-z0-9-]+)?\.amazonaws\.com`)
	reAWSS3ARN     = regexp.MustCompile(`arn:aws:s3:::([a-z0-9.\-]+)`)
)

func scanAWSS3(value string) []externalTarget {
	var out []externalTarget
	if m := reAWSS3Scheme.FindStringSubmatch(value); m != nil {
		out = append(out, s3Target(m[1], m[0]))
	}
	for _, m := range reAWSS3Virtual.FindAllStringSubmatch(value, -1) {
		// Exclude meta-hostnames like "s3.amazonaws.com" (empty bucket).
		if m[1] == "" || m[1] == "s3" {
			continue
		}
		out = append(out, s3Target(m[1], m[0]))
	}
	if m := reAWSS3ARN.FindStringSubmatch(value); m != nil {
		out = append(out, s3Target(m[1], m[0]))
	}
	return out
}

func s3Target(bucket, matched string) externalTarget {
	return externalTarget{
		ID:           "arn:aws:s3:::" + bucket,
		ResourceType: "aws:s3:bucket",
		Provider:     "aws",
		PatternName:  "aws:s3",
		Matched:      matched,
	}
}

// --- AWS RDS --------------------------------------------------------

// RDS instance: {db-id}.{random-suffix}.{region}.rds.amazonaws.com
// Aurora cluster: {cluster}.cluster-{random}.{region}.rds.amazonaws.com
// Aurora reader: {cluster}.cluster-ro-{random}.{region}.rds.amazonaws.com
// Aurora custom: {cluster}.cluster-custom-{random}.{region}.rds.amazonaws.com
// The middle segment MUST allow hyphens — "cluster-", "cluster-ro-", and
// "cluster-custom-" prefixes on Aurora endpoints carry them. We extract
// the DB-id (outer) and region; account is not encoded → dangling.
var reAWSRDS = regexp.MustCompile(`([a-z0-9-]+)\.([a-z0-9-]+)\.([a-z0-9-]+)\.rds\.amazonaws\.com`)

func scanAWSRDS(value string) []externalTarget {
	var out []externalTarget
	for _, m := range reAWSRDS.FindAllStringSubmatch(value, -1) {
		dbID, region := m[1], m[3]
		out = append(out, externalTarget{
			// Account is empty → dangling; use a synthetic ARN that preserves the shape.
			ID:           "arn:aws:rds:" + region + "::db/" + dbID,
			ResourceType: "aws:rds:instance",
			Provider:     "aws",
			PatternName:  "aws:rds",
			Matched:      m[0],
		})
	}
	return out
}

// --- AWS ElastiCache ------------------------------------------------

// ElastiCache endpoint: {cluster}.{id}.{region}.cache.amazonaws.com.
// Middle segment allows hyphens to cover replication-group, cluster-mode
// ("clustercfg"), and other prefixed suffixes that can carry hyphens.
// Same reasoning as reAWSRDS — don't silently drop real endpoints.
var reAWSElastiCache = regexp.MustCompile(`([a-z0-9-]+)\.([a-z0-9-]+)\.([a-z0-9-]+)\.cache\.amazonaws\.com`)

func scanAWSElastiCache(value string) []externalTarget {
	var out []externalTarget
	for _, m := range reAWSElastiCache.FindAllStringSubmatch(value, -1) {
		cluster, region := m[1], m[3]
		out = append(out, externalTarget{
			ID:           "arn:aws:elasticache:" + region + "::cluster/" + cluster,
			ResourceType: "aws:elasticache:cluster",
			Provider:     "aws",
			PatternName:  "aws:elasticache",
			Matched:      m[0],
		})
	}
	return out
}

// --- AWS SQS --------------------------------------------------------

// SQS URL carries the account: sqs.{region}.amazonaws.com/{account}/{queue}
var reAWSSQS = regexp.MustCompile(`sqs\.([a-z0-9-]+)\.amazonaws\.com/([0-9]{12})/([a-zA-Z0-9_.\-]+)`)

func scanAWSSQS(value string) []externalTarget {
	var out []externalTarget
	for _, m := range reAWSSQS.FindAllStringSubmatch(value, -1) {
		region, account, queue := m[1], m[2], m[3]
		out = append(out, externalTarget{
			Account:      account,
			ID:           "arn:aws:sqs:" + region + ":" + account + ":" + queue,
			ResourceType: "aws:sqs:queue",
			Provider:     "aws",
			PatternName:  "aws:sqs",
			Matched:      m[0],
		})
	}
	return out
}

// --- AWS DynamoDB ---------------------------------------------------

// DynamoDB endpoint only encodes the region — table name + account are elsewhere.
// We emit a service-level dangling proxy so the workload → DDB connection shows up.
var reAWSDynamoDB = regexp.MustCompile(`dynamodb\.([a-z0-9-]+)\.amazonaws\.com`)

func scanAWSDynamoDB(value string) []externalTarget {
	m := reAWSDynamoDB.FindStringSubmatch(value)
	if m == nil {
		return nil
	}
	region := m[1]
	return []externalTarget{{
		ID:           "arn:aws:dynamodb:" + region + "::service",
		ResourceType: "aws:dynamodb:service",
		Provider:     "aws",
		PatternName:  "aws:dynamodb",
		Matched:      m[0],
	}}
}

// --- AWS generic ARN ------------------------------------------------

// Captures any arn:aws:{service}:{region}:{account}:{resource}.
// Account and region are both optional (IAM ARNs have no region; some
// service ARNs have no account). The pattern is anchored to the "arn:"
// prefix so random strings don't match.
var reAWSARN = regexp.MustCompile(`arn:aws:([a-z0-9-]+):([a-z0-9-]*):([0-9]*):([a-zA-Z0-9_/:.\-]+)`)

func scanAWSARN(value string) []externalTarget {
	var out []externalTarget
	for _, m := range reAWSARN.FindAllStringSubmatch(value, -1) {
		service, account := m[1], m[3]
		// Skip overlap with specialized patterns to avoid double-edges
		// (s3, rds, sqs, elasticache, dynamodb are handled above with
		// richer canonical IDs; a generic arn:aws:s3:::bucket pattern
		// would redo the same edge).
		if service == "s3" || service == "sqs" || service == "rds" || service == "elasticache" || service == "dynamodb" {
			continue
		}
		out = append(out, externalTarget{
			Account:      account,
			ID:           m[0],
			ResourceType: "aws:" + service,
			Provider:     "aws",
			PatternName:  "aws:arn",
			Matched:      m[0],
		})
	}
	return out
}

// --- GCP GCS --------------------------------------------------------

// gs://{bucket}[/...]
var reGCPGCS = regexp.MustCompile(`gs://([a-z0-9.\-_]+)(?:/|$)`)

func scanGCPGCS(value string) []externalTarget {
	var out []externalTarget
	for _, m := range reGCPGCS.FindAllStringSubmatch(value, -1) {
		bucket := m[1]
		out = append(out, externalTarget{
			// gs://{bucket} is the canonical GCS URI; keep it as the ID
			// (no leading // collision with proxy:cloud:: scheme) and
			// the bucket name is preserved for readable traversal.
			ID:           "gs://" + bucket,
			ResourceType: "gcp:storage:bucket",
			Provider:     "gcp",
			PatternName:  "gcp:gcs",
			Matched:      m[0],
		})
	}
	return out
}

// --- GCP Cloud SQL --------------------------------------------------

// Cloud SQL socket connection string: /cloudsql/{project}:{region}:{instance}
var reGCPCloudSQL = regexp.MustCompile(`/cloudsql/([a-z0-9\-]+):([a-z0-9\-]+):([a-z0-9\-]+)`)

func scanGCPCloudSQL(value string) []externalTarget {
	var out []externalTarget
	for _, m := range reGCPCloudSQL.FindAllStringSubmatch(value, -1) {
		project, instance := m[1], m[3]
		selfLink := "https://sqladmin.googleapis.com/sql/v1beta4/projects/" + project +
			"/instances/" + instance
		out = append(out, externalTarget{
			Account:      project,
			ID:           selfLink,
			ResourceType: "gcp:sql:instance",
			Provider:     "gcp",
			PatternName:  "gcp:cloudsql",
			Matched:      m[0],
		})
	}
	return out
}

// --- GCP Pub/Sub ----------------------------------------------------

// projects/{project}/topics/{topic} or projects/{project}/subscriptions/{sub}
var reGCPPubSub = regexp.MustCompile(`projects/([a-z0-9\-]+)/(topics|subscriptions)/([a-zA-Z0-9_.\-]+)`)

func scanGCPPubSub(value string) []externalTarget {
	var out []externalTarget
	for _, m := range reGCPPubSub.FindAllStringSubmatch(value, -1) {
		project, kind := m[1], m[2]
		resourceType := "gcp:pubsub:topic"
		if kind == "subscriptions" {
			resourceType = "gcp:pubsub:subscription"
		}
		out = append(out, externalTarget{
			Account:      project,
			ID:           m[0],
			ResourceType: resourceType,
			Provider:     "gcp",
			PatternName:  "gcp:pubsub",
			Matched:      m[0],
		})
	}
	return out
}

// --- Azure patterns -------------------------------------------------

// All Azure patterns produce dangling proxies: the storage account /
// server / namespace name in the hostname is NOT the Azure subscription,
// and we intentionally don't cross-scan Azure graphs (plan OQ decision).

var (
	reAzureBlob       = regexp.MustCompile(`([a-z0-9]+)\.blob\.core\.windows\.net`)
	reAzureSQL        = regexp.MustCompile(`([a-z0-9-]+)\.database\.windows\.net`)
	reAzureRedis      = regexp.MustCompile(`([a-z0-9-]+)\.redis\.cache\.windows\.net`)
	reAzureServiceBus = regexp.MustCompile(`([a-z0-9-]+)\.servicebus\.windows\.net`)
)

func scanAzureBlob(value string) []externalTarget {
	return matchAzure(value, reAzureBlob, "azure:storage:account", "azure:blob", "storageAccounts")
}

func scanAzureSQL(value string) []externalTarget {
	return matchAzure(value, reAzureSQL, "azure:sql:server", "azure:sql", "servers")
}

func scanAzureRedis(value string) []externalTarget {
	return matchAzure(value, reAzureRedis, "azure:cache:redis", "azure:redis", "Redis")
}

func scanAzureServiceBus(value string) []externalTarget {
	return matchAzure(value, reAzureServiceBus, "azure:servicebus:namespace", "azure:servicebus", "namespaces")
}

func matchAzure(value string, pattern *regexp.Regexp, resourceType, patternName, kind string) []externalTarget {
	var out []externalTarget
	for _, m := range pattern.FindAllStringSubmatch(value, -1) {
		// Synthetic dangling resource ID that preserves the hostname → resource mapping.
		id := "azure:" + strings.ToLower(kind) + "/" + m[1]
		out = append(out, externalTarget{
			ID:           id,
			ResourceType: resourceType,
			Provider:     "azure",
			PatternName:  patternName,
			Matched:      m[0],
		})
	}
	return out
}

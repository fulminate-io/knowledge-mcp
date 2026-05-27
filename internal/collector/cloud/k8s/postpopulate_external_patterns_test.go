// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestScanAllPatterns is a table-driven test covering every pattern
// with at least one positive match and one representative negative.
// Each case also asserts on the canonical resource ID so regressions
// in ID shape show up immediately.
func TestScanAllPatterns(t *testing.T) {
	cases := []struct {
		name        string
		value       string
		wantPattern string // empty means "no match expected"
		wantID      string
		wantAccount string
	}{
		// --- AWS S3 ---
		{
			name:        "aws s3 scheme",
			value:       "s3://my-bucket/path/to/file",
			wantPattern: "aws:s3",
			wantID:      "arn:aws:s3:::my-bucket",
		},
		{
			name:        "aws s3 virtual-host global",
			value:       "https://my-bucket.s3.amazonaws.com/key",
			wantPattern: "aws:s3",
			wantID:      "arn:aws:s3:::my-bucket",
		},
		{
			name:        "aws s3 virtual-host region",
			value:       "https://my-bucket.s3.us-west-2.amazonaws.com/key",
			wantPattern: "aws:s3",
			wantID:      "arn:aws:s3:::my-bucket",
		},
		// --- AWS RDS ---
		{
			name:        "aws rds endpoint",
			value:       "mydb.abc123.us-east-1.rds.amazonaws.com",
			wantPattern: "aws:rds",
			wantID:      "arn:aws:rds:us-east-1::db/mydb",
		},
		{
			// Aurora cluster endpoint — middle segment "cluster-<random>"
			// has a hyphen. Previously the regex dropped these silently.
			name:        "aws aurora cluster endpoint",
			value:       "prod-orders.cluster-abcd1234efgh.us-east-1.rds.amazonaws.com",
			wantPattern: "aws:rds",
			wantID:      "arn:aws:rds:us-east-1::db/prod-orders",
		},
		{
			// Aurora reader endpoint — middle segment "cluster-ro-<random>".
			name:        "aws aurora reader endpoint",
			value:       "prod-orders.cluster-ro-abcd1234efgh.us-east-1.rds.amazonaws.com",
			wantPattern: "aws:rds",
			wantID:      "arn:aws:rds:us-east-1::db/prod-orders",
		},
		{
			// Aurora custom endpoint — middle "cluster-custom-<random>".
			name:        "aws aurora custom endpoint",
			value:       "prod-orders.cluster-custom-abcd1234.us-east-1.rds.amazonaws.com",
			wantPattern: "aws:rds",
			wantID:      "arn:aws:rds:us-east-1::db/prod-orders",
		},
		// --- AWS ElastiCache ---
		{
			name:        "aws elasticache endpoint",
			value:       "mycache.abc123.us-east-1.cache.amazonaws.com",
			wantPattern: "aws:elasticache",
			wantID:      "arn:aws:elasticache:us-east-1::cluster/mycache",
		},
		{
			// Replication-group-style: the unanchored regex matches the
			// rightmost three segments before .cache.amazonaws.com, so the
			// captured "cluster" is the hyphenated replication-group id.
			// Under the old regex (no hyphens in middle segment) this
			// input didn't match at all — now it does.
			name:        "aws elasticache replication group endpoint",
			value:       "master.my-rg.abc123.use1.cache.amazonaws.com",
			wantPattern: "aws:elasticache",
			wantID:      "arn:aws:elasticache:use1::cluster/my-rg",
		},
		// --- AWS SQS (has account in URL) ---
		{
			name:        "aws sqs url",
			value:       "https://sqs.us-east-1.amazonaws.com/123456789012/my-queue",
			wantPattern: "aws:sqs",
			wantID:      "arn:aws:sqs:us-east-1:123456789012:my-queue",
			wantAccount: "123456789012",
		},
		// --- AWS DynamoDB ---
		{
			name:        "aws dynamodb endpoint",
			value:       "https://dynamodb.us-east-1.amazonaws.com",
			wantPattern: "aws:dynamodb",
			wantID:      "arn:aws:dynamodb:us-east-1::service",
		},
		// --- AWS generic ARN ---
		{
			name:        "aws generic arn kms",
			value:       "arn:aws:kms:us-west-2:123456789012:key/abc-123",
			wantPattern: "aws:arn",
			wantID:      "arn:aws:kms:us-west-2:123456789012:key/abc-123",
			wantAccount: "123456789012",
		},
		// --- GCP GCS ---
		{
			name:        "gcp gcs scheme",
			value:       "gs://my-gcp-bucket/dir",
			wantPattern: "gcp:gcs",
			wantID:      "gs://my-gcp-bucket",
		},
		// --- GCP Cloud SQL ---
		{
			name:        "gcp cloudsql socket",
			value:       "host=/cloudsql/my-project:us-central1:my-instance",
			wantPattern: "gcp:cloudsql",
			wantID:      "https://sqladmin.googleapis.com/sql/v1beta4/projects/my-project/instances/my-instance",
			wantAccount: "my-project",
		},
		// --- GCP Pub/Sub ---
		{
			name:        "gcp pubsub topic",
			value:       "projects/my-project/topics/events",
			wantPattern: "gcp:pubsub",
			wantID:      "projects/my-project/topics/events",
			wantAccount: "my-project",
		},
		{
			name:        "gcp pubsub subscription",
			value:       "projects/my-project/subscriptions/workers-1",
			wantPattern: "gcp:pubsub",
			wantID:      "projects/my-project/subscriptions/workers-1",
			wantAccount: "my-project",
		},
		// --- Azure Blob ---
		{
			name:        "azure blob endpoint",
			value:       "https://mydata.blob.core.windows.net/container",
			wantPattern: "azure:blob",
			wantID:      "azure:storageaccounts/mydata",
		},
		// --- Azure SQL ---
		{
			name:        "azure sql server",
			value:       "myserver.database.windows.net",
			wantPattern: "azure:sql",
			wantID:      "azure:servers/myserver",
		},
		// --- Azure Redis ---
		{
			name:        "azure redis",
			value:       "mycache.redis.cache.windows.net",
			wantPattern: "azure:redis",
			wantID:      "azure:redis/mycache",
		},
		// --- Azure ServiceBus ---
		{
			name:        "azure servicebus",
			value:       "mynamespace.servicebus.windows.net",
			wantPattern: "azure:servicebus",
			wantID:      "azure:namespaces/mynamespace",
		},

		// --- Negatives ---
		{
			name:  "plain string no match",
			value: "hello world",
		},
		{
			name:  "private hostname no match",
			value: "my-internal-db.cluster.local",
		},
		{
			name:  "random http url no match",
			value: "https://example.com/api",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := scanAllPatterns(tc.value)
			if tc.wantPattern == "" {
				assert.Empty(t, got, "expected no matches for %q", tc.value)
				return
			}
			require := assert.New(t)
			require.NotEmpty(got, "expected pattern %q to match", tc.wantPattern)
			// Find the specific pattern match we expect.
			var found *externalTarget
			for i := range got {
				if got[i].PatternName == tc.wantPattern && got[i].ID == tc.wantID {
					found = &got[i]
					break
				}
			}
			require.NotNil(found, "expected %s target %q, got %#v", tc.wantPattern, tc.wantID, got)
			require.Equal(tc.wantAccount, found.Account)
			require.NotEmpty(found.Matched, "matched substring must be populated")
			// The matched substring must be a substring of the input (NOT the full input).
			require.Contains(tc.value, found.Matched)
		})
	}
}

// TestScanAllPatterns_Dedup: a value that matches both a specialized
// pattern (e.g. aws:s3 via the scheme) and the generic ARN pattern
// (via arn:aws:...) must only produce one edge thanks to pattern
// precedence inside scanAWSARN (skip s3/sqs/rds/elasticache/dynamodb).
func TestScanAllPatterns_Dedup(t *testing.T) {
	value := "arn:aws:s3:::my-bucket"
	got := scanAllPatterns(value)
	assert.Len(t, got, 1, "expected single match")
	assert.Equal(t, "aws:s3", got[0].PatternName)
}

// TestScanAllPatterns_MultipleURIs: a value containing two distinct
// cloud URIs produces two separate matches.
func TestScanAllPatterns_MultipleURIs(t *testing.T) {
	value := "s3://bucket-a/key and gs://bucket-b/obj"
	got := scanAllPatterns(value)
	require := assert.New(t)
	require.Len(got, 2)
	patterns := map[string]bool{}
	for _, t := range got {
		patterns[t.PatternName] = true
	}
	require.True(patterns["aws:s3"])
	require.True(patterns["gcp:gcs"])
}

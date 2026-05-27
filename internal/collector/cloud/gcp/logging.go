// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	logging "cloud.google.com/go/logging/apiv2"
	loggingpb "cloud.google.com/go/logging/apiv2/loggingpb"
	"google.golang.org/api/iterator"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type loggingSubCollector struct {
	client    *logging.ConfigClient
	projectID string
}

func newLoggingSubCollector(client *logging.ConfigClient, projectID string) *loggingSubCollector {
	return &loggingSubCollector{client: client, projectID: projectID}
}

func (c *loggingSubCollector) Name() string { return "gcp-logging-sinks" }

func (c *loggingSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var result cloud.SubCollectorResult

	it := c.client.ListSinks(ctx, &loggingpb.ListSinksRequest{
		Parent: fmt.Sprintf("projects/%s", c.projectID),
	})
	for {
		sink, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return result, fmt.Errorf("logging sinks: list: %w", err)
		}

		sinkName := sink.GetName()
		if sinkName == "" {
			continue
		}

		content, err := json.Marshal(sink)
		if err != nil {
			continue
		}

		result.Resources = append(result.Resources, cloud.ResourceSpec{
			ID:           sinkName,
			Name:         lastSegment(sinkName),
			ResourceType: "gcp:logging:sink",
			Content:      content,
		})

		destID := resolveLoggingSinkDest(sink.GetDestination())
		if destID != "" {
			result.Edges = append(result.Edges, cloud.EdgeSpec{
				SourceID:     sinkName,
				TargetID:     destID,
				Relationship: kgtypes.EdgeSinksTo,
			})
		}
	}

	return result, nil
}

// resolveLoggingSinkDest maps a logging sink destination string to a GCP
// resource self-link or identifier usable as a graph node ID.
//
// Destination formats:
//
//	storage.googleapis.com/BUCKET
//	bigquery.googleapis.com/projects/P/datasets/D
//	pubsub.googleapis.com/projects/P/topics/T
//	logging.googleapis.com/projects/P/locations/L/buckets/B
func resolveLoggingSinkDest(dest string) string {
	if dest == "" {
		return ""
	}
	switch {
	case strings.HasPrefix(dest, "storage.googleapis.com/"):
		// Match the canonical GCS bucket node ID emitted by storage.go
		// ("gs://<name>", per decision 1390ea2b). Returning a REST URL
		// here would dangle every Logging-sink → bucket edge.
		bucket := strings.TrimPrefix(dest, "storage.googleapis.com/")
		return "gs://" + bucket
	case strings.HasPrefix(dest, "bigquery.googleapis.com/"):
		return strings.TrimPrefix(dest, "bigquery.googleapis.com/")
	case strings.HasPrefix(dest, "pubsub.googleapis.com/"):
		return strings.TrimPrefix(dest, "pubsub.googleapis.com/")
	case strings.HasPrefix(dest, "logging.googleapis.com/"):
		return dest
	default:
		return dest
	}
}

// lastSegment returns the final path segment of a resource name.
func lastSegment(name string) string {
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[i+1:]
	}
	return name
}

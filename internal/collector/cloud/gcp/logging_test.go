// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLoggingSubCollector_Name(t *testing.T) {
	c := &loggingSubCollector{}
	assert.Equal(t, "gcp-logging-sinks", c.Name())
}

func TestResolveLoggingSinkDest(t *testing.T) {
	tests := []struct {
		name string
		dest string
		want string
	}{
		{
			name: "storage maps to canonical gs:// bucket ID",
			dest: "storage.googleapis.com/my-bucket",
			want: "gs://my-bucket",
		},
		{
			name: "bigquery maps to projects/P/datasets/D",
			dest: "bigquery.googleapis.com/projects/p/datasets/d",
			want: "projects/p/datasets/d",
		},
		{
			name: "pubsub maps to projects/P/topics/T",
			dest: "pubsub.googleapis.com/projects/p/topics/t",
			want: "projects/p/topics/t",
		},
		{
			name: "logging bucket destination passes through",
			dest: "logging.googleapis.com/projects/p/locations/global/buckets/b",
			want: "logging.googleapis.com/projects/p/locations/global/buckets/b",
		},
		{
			name: "empty destination returns empty",
			dest: "",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, resolveLoggingSinkDest(tt.dest))
		})
	}
}

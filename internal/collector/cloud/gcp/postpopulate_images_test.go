// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestExtractCloudRunImages(t *testing.T) {
	content := `{
		"name": "projects/my-project/locations/us-central1/services/my-svc",
		"template": {
			"containers": [
				{"image": "us-central1-docker.pkg.dev/my-project/my-repo/my-image:v1"},
				{"image": "gcr.io/my-project/sidecar:latest"}
			]
		}
	}`
	images := extractCloudRunImages(content)
	require.Len(t, images, 2)
	assert.Equal(t, "us-central1-docker.pkg.dev/my-project/my-repo/my-image:v1", images[0])
	assert.Equal(t, "gcr.io/my-project/sidecar:latest", images[1])
}

func TestExtractCloudRunImages_NoContainers(t *testing.T) {
	content := `{"name": "projects/p/locations/l/services/s", "template": {}}`
	images := extractCloudRunImages(content)
	assert.Empty(t, images)
}

func TestExtractCloudRunImages_EmptyContent(t *testing.T) {
	images := extractCloudRunImages("")
	assert.Nil(t, images)
}

func TestExtractCloudRunImages_InvalidJSON(t *testing.T) {
	images := extractCloudRunImages("{invalid")
	assert.Nil(t, images)
}

func TestMatchARImage_ArtifactRegistry(t *testing.T) {
	index := map[string]string{
		"my-project/my-repo": "projects/my-project/locations/us-central1/repositories/my-repo",
	}
	ref := cloud.ParseImageRef("us-central1-docker.pkg.dev/my-project/my-repo/my-image:v1")
	got := matchARImage(ref, index)
	assert.Equal(t, "projects/my-project/locations/us-central1/repositories/my-repo", got)
}

func TestMatchARImage_GCR_ToAR(t *testing.T) {
	index := map[string]string{
		"my-project/my-image": "projects/my-project/locations/us/repositories/my-image",
	}
	ref := cloud.ParseImageRef("gcr.io/my-project/my-image:latest")
	got := matchARImage(ref, index)
	assert.Equal(t, "projects/my-project/locations/us/repositories/my-image", got)
}

func TestMatchARImage_GCR_NoMatch(t *testing.T) {
	index := map[string]string{
		"other-project/other": "projects/other-project/locations/us/repositories/other",
	}
	ref := cloud.ParseImageRef("gcr.io/my-project/my-image:latest")
	got := matchARImage(ref, index)
	assert.Empty(t, got)
}

func TestMatchARImage_Unknown(t *testing.T) {
	index := map[string]string{}
	ref := cloud.ParseImageRef("docker.io/library/nginx:latest")
	got := matchARImage(ref, index)
	assert.Empty(t, got)
}

func TestParseARResourceID(t *testing.T) {
	project, repo := parseARResourceID("projects/my-project/locations/us-central1/repositories/my-repo")
	assert.Equal(t, "my-project", project)
	assert.Equal(t, "my-repo", repo)
}

func TestParseARResourceID_Invalid(t *testing.T) {
	project, _ := parseARResourceID("random/string")
	assert.Empty(t, project)
}

func TestResolveCloudRunImageLineage_EndToEnd(t *testing.T) {
	svcContent := `{
		"name": "projects/my-project/locations/us-central1/services/my-svc",
		"template": {
			"containers": [
				{"image": "us-central1-docker.pkg.dev/my-project/my-repo/app:v1"},
				{"image": "nginx:latest"}
			]
		}
	}`

	images := extractCloudRunImages(svcContent)
	require.Len(t, images, 2)

	arIndex := map[string]string{
		"my-project/my-repo": "projects/my-project/locations/us-central1/repositories/my-repo",
	}

	var edges []knowledgev1.Edge
	seen := make(map[string]struct{})
	svcID := "projects/my-project/locations/us-central1/services/my-svc"

	for _, img := range images {
		ref := cloud.ParseImageRef(img)
		targetID := matchARImage(ref, arIndex)
		if targetID == "" {
			continue
		}
		key := svcID + "|" + targetID
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		edges = append(edges, knowledgev1.Edge{
			FromId:   svcID,
			ToId:     targetID,
			Type:     string(kgtypes.EdgeUsesImage),
			Method:   "postpopulate:image-lineage",
			Evidence: ref.Full,
		})
	}

	require.Len(t, edges, 1)
	assert.Equal(t, svcID, edges[0].FromId)
	assert.Equal(t, "projects/my-project/locations/us-central1/repositories/my-repo", edges[0].ToId)
	assert.Equal(t, string(kgtypes.EdgeUsesImage), edges[0].Type)
	assert.Contains(t, edges[0].Evidence, "us-central1-docker.pkg.dev/my-project/my-repo/app:v1")
}

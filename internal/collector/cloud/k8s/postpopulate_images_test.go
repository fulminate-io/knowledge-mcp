// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestExtractContainerImages_Deployment(t *testing.T) {
	content := makeWorkloadJSON(t, "Deployment",
		[]string{"123456789.dkr.ecr.us-east-1.amazonaws.com/my-app:latest", "nginx:1.25"},
		[]string{"init-ecr.dkr.ecr.us-east-1.amazonaws.com/init:v1"},
	)
	images := extractContainerImages(content, "Deployment")
	require.Len(t, images, 3)
	assert.Equal(t, "123456789.dkr.ecr.us-east-1.amazonaws.com/my-app:latest", images[0])
	assert.Equal(t, "nginx:1.25", images[1])
	assert.Equal(t, "init-ecr.dkr.ecr.us-east-1.amazonaws.com/init:v1", images[2])
}

func TestExtractContainerImages_CronJob(t *testing.T) {
	content := makeCronJobJSON(t,
		[]string{"us-central1-docker.pkg.dev/proj/repo/worker:v2"},
		nil,
	)
	images := extractContainerImages(content, "CronJob")
	require.Len(t, images, 1)
	assert.Equal(t, "us-central1-docker.pkg.dev/proj/repo/worker:v2", images[0])
}

func TestExtractContainerImages_EmptyContent(t *testing.T) {
	images := extractContainerImages("", "Deployment")
	assert.Nil(t, images)
}

func TestExtractContainerImages_InvalidJSON(t *testing.T) {
	images := extractContainerImages("{invalid", "Deployment")
	assert.Nil(t, images)
}

func TestMatchImages_ECR(t *testing.T) {
	index := &registryIndex{
		ecrRepos: map[string]string{
			"123456789:us-east-1:my-app": "arn:aws:ecr:us-east-1:123456789:repository/my-app",
		},
		acrHosts: map[string]string{},
		arRepos:  map[string]string{},
	}
	seen := make(map[string]struct{})
	images := []string{
		"123456789.dkr.ecr.us-east-1.amazonaws.com/my-app:latest",
		"nginx:1.25", // unknown, skipped
	}
	edges := matchImages("deploy-1", images, index, seen)
	require.Len(t, edges, 1)
	assert.Equal(t, "deploy-1", edges[0].FromId)
	assert.Equal(t, "arn:aws:ecr:us-east-1:123456789:repository/my-app", edges[0].ToId)
	assert.Equal(t, string(kgtypes.EdgeUsesImage), edges[0].Type)
	assert.Equal(t, "postpopulate:image-lineage", edges[0].Method)
	assert.Equal(t, "123456789.dkr.ecr.us-east-1.amazonaws.com/my-app:latest", edges[0].Evidence)
}

func TestMatchImages_ACR(t *testing.T) {
	index := &registryIndex{
		ecrRepos: map[string]string{},
		acrHosts: map[string]string{
			"myregistry.azurecr.io": "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.ContainerRegistry/registries/myregistry",
		},
		arRepos: map[string]string{},
	}
	seen := make(map[string]struct{})
	edges := matchImages("deploy-1",
		[]string{"myregistry.azurecr.io/myapp:v1"},
		index, seen)
	require.Len(t, edges, 1)
	assert.Equal(t, string(kgtypes.EdgeUsesImage), edges[0].Type)
}

func TestMatchImages_ArtifactRegistry(t *testing.T) {
	index := &registryIndex{
		ecrRepos: map[string]string{},
		acrHosts: map[string]string{},
		arRepos: map[string]string{
			"my-project/my-repo": "projects/my-project/locations/us-central1/repositories/my-repo",
		},
	}
	seen := make(map[string]struct{})
	edges := matchImages("deploy-1",
		[]string{"us-central1-docker.pkg.dev/my-project/my-repo/my-image:v2"},
		index, seen)
	require.Len(t, edges, 1)
	assert.Equal(t, "projects/my-project/locations/us-central1/repositories/my-repo", edges[0].ToId)
}

func TestMatchImages_GCR_ToAR(t *testing.T) {
	index := &registryIndex{
		ecrRepos: map[string]string{},
		acrHosts: map[string]string{},
		arRepos: map[string]string{
			"my-project/my-image": "projects/my-project/locations/us/repositories/my-image",
		},
	}
	seen := make(map[string]struct{})
	edges := matchImages("deploy-1",
		[]string{"gcr.io/my-project/my-image:latest"},
		index, seen)
	require.Len(t, edges, 1)
	assert.Equal(t, "projects/my-project/locations/us/repositories/my-image", edges[0].ToId)
}

func TestMatchImages_Dedup(t *testing.T) {
	index := &registryIndex{
		ecrRepos: map[string]string{
			"111:us-east-1:app": "arn:aws:ecr:us-east-1:111:repository/app",
		},
		acrHosts: map[string]string{},
		arRepos:  map[string]string{},
	}
	seen := make(map[string]struct{})
	images := []string{
		"111.dkr.ecr.us-east-1.amazonaws.com/app:v1",
		"111.dkr.ecr.us-east-1.amazonaws.com/app:v2", // same repo, deduped
	}
	edges := matchImages("deploy-1", images, index, seen)
	require.Len(t, edges, 1)
}

func TestParseECRArn(t *testing.T) {
	account, region, repo := parseECRArn("arn:aws:ecr:us-east-1:123456789:repository/my-app")
	assert.Equal(t, "123456789", account)
	assert.Equal(t, "us-east-1", region)
	assert.Equal(t, "my-app", repo)
}

func TestParseECRArn_NestedRepo(t *testing.T) {
	account, region, repo := parseECRArn("arn:aws:ecr:eu-west-1:999:repository/team/service")
	assert.Equal(t, "999", account)
	assert.Equal(t, "eu-west-1", region)
	assert.Equal(t, "team/service", repo)
}

func TestParseECRArn_Invalid(t *testing.T) {
	account, _, _ := parseECRArn("not-an-arn")
	assert.Empty(t, account)
}

func TestParseARID(t *testing.T) {
	project, repo := parseARID("projects/my-project/locations/us-central1/repositories/my-repo")
	assert.Equal(t, "my-project", project)
	assert.Equal(t, "my-repo", repo)
}

func TestParseARID_Invalid(t *testing.T) {
	project, _ := parseARID("random/string")
	assert.Empty(t, project)
}

func TestDrillJSON(t *testing.T) {
	raw := `{"spec":{"template":{"spec":{"containers":[{"image":"nginx"}]}}}}`
	var obj map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(raw), &obj))

	result := drillJSON(obj, "spec", "template", "spec")
	require.NotNil(t, result)
	assert.Contains(t, string(result["containers"]), "nginx")
}

func TestDrillJSON_MissingKey(t *testing.T) {
	raw := `{"spec":{"template":{}}}`
	var obj map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(raw), &obj))

	result := drillJSON(obj, "spec", "template", "spec")
	assert.Nil(t, result)
}

func TestRegistryIndex_Match_Unknown(t *testing.T) {
	index := &registryIndex{
		ecrRepos: map[string]string{},
		acrHosts: map[string]string{},
		arRepos:  map[string]string{},
	}
	ref := cloud.ParseImageRef("docker.io/library/nginx:latest")
	assert.Empty(t, index.match(ref))
}

// --- test helpers ---

// makeWorkloadJSON creates a minimal K8s workload JSON with the standard
// spec.template.spec.containers path (Deployment/StatefulSet/DaemonSet/Job).
func makeWorkloadJSON(t *testing.T, kind string, images, initImages []string) string {
	t.Helper()
	type ctr struct {
		Name  string `json:"name"`
		Image string `json:"image"`
	}
	d := map[string]any{
		"apiVersion": "apps/v1",
		"kind":       kind,
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{},
			},
		},
	}
	spec := d["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	var containers []ctr
	for i, img := range images {
		containers = append(containers, ctr{Name: "c" + string(rune('0'+i)), Image: img})
	}
	spec["containers"] = containers
	if len(initImages) > 0 {
		var initContainers []ctr
		for i, img := range initImages {
			initContainers = append(initContainers, ctr{Name: "init" + string(rune('0'+i)), Image: img})
		}
		spec["initContainers"] = initContainers
	}
	b, err := json.Marshal(d)
	require.NoError(t, err)
	return string(b)
}

// makeCronJobJSON creates a minimal CronJob JSON with the nested jobTemplate path.
func makeCronJobJSON(t *testing.T, images, initImages []string) string {
	t.Helper()
	type ctr struct {
		Name  string `json:"name"`
		Image string `json:"image"`
	}
	spec := map[string]any{}
	var containers []ctr
	for i, img := range images {
		containers = append(containers, ctr{Name: "c" + string(rune('0'+i)), Image: img})
	}
	spec["containers"] = containers
	if len(initImages) > 0 {
		var initContainers []ctr
		for i, img := range initImages {
			initContainers = append(initContainers, ctr{Name: "init" + string(rune('0'+i)), Image: img})
		}
		spec["initContainers"] = initContainers
	}
	d := map[string]any{
		"apiVersion": "batch/v1",
		"kind":       "CronJob",
		"spec": map[string]any{
			"jobTemplate": map[string]any{
				"spec": map[string]any{
					"template": map[string]any{
						"spec": spec,
					},
				},
			},
		},
	}
	b, err := json.Marshal(d)
	require.NoError(t, err)
	return string(b)
}

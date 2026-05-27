// SPDX-License-Identifier: Apache-2.0

package cloud

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseImageRef(t *testing.T) {
	tests := []struct {
		name     string
		image    string
		wantRef  ImageRef
		wantKind RegistryType
	}{
		{
			name:  "ECR image with tag",
			image: "123456789.dkr.ecr.us-east-1.amazonaws.com/my-app:latest",
			wantRef: ImageRef{
				Registry:   "123456789.dkr.ecr.us-east-1.amazonaws.com",
				Repository: "my-app",
				Tag:        "latest",
				Full:       "123456789.dkr.ecr.us-east-1.amazonaws.com/my-app:latest",
			},
			wantKind: RegistryECR,
		},
		{
			name:  "ECR image with digest",
			image: "123456789.dkr.ecr.us-east-1.amazonaws.com/my-app@sha256:abcdef1234567890",
			wantRef: ImageRef{
				Registry:   "123456789.dkr.ecr.us-east-1.amazonaws.com",
				Repository: "my-app",
				Digest:     "sha256:abcdef1234567890",
				Full:       "123456789.dkr.ecr.us-east-1.amazonaws.com/my-app@sha256:abcdef1234567890",
			},
			wantKind: RegistryECR,
		},
		{
			name:  "ECR nested repo",
			image: "123456789.dkr.ecr.eu-west-1.amazonaws.com/team/service:v1.2.3",
			wantRef: ImageRef{
				Registry:   "123456789.dkr.ecr.eu-west-1.amazonaws.com",
				Repository: "team/service",
				Tag:        "v1.2.3",
				Full:       "123456789.dkr.ecr.eu-west-1.amazonaws.com/team/service:v1.2.3",
			},
			wantKind: RegistryECR,
		},
		{
			name:  "GCR image",
			image: "gcr.io/my-project/my-image:v1",
			wantRef: ImageRef{
				Registry:   "gcr.io",
				Repository: "my-project/my-image",
				Tag:        "v1",
				Full:       "gcr.io/my-project/my-image:v1",
			},
			wantKind: RegistryGCR,
		},
		{
			name:  "GCR regional",
			image: "us.gcr.io/my-project/my-image:latest",
			wantRef: ImageRef{
				Registry:   "us.gcr.io",
				Repository: "my-project/my-image",
				Tag:        "latest",
				Full:       "us.gcr.io/my-project/my-image:latest",
			},
			wantKind: RegistryGCR,
		},
		{
			name:  "Artifact Registry",
			image: "us-central1-docker.pkg.dev/my-project/my-repo/my-image:v2",
			wantRef: ImageRef{
				Registry:   "us-central1-docker.pkg.dev",
				Repository: "my-project/my-repo/my-image",
				Tag:        "v2",
				Full:       "us-central1-docker.pkg.dev/my-project/my-repo/my-image:v2",
			},
			wantKind: RegistryArtifactRegistry,
		},
		{
			name:  "ACR image",
			image: "myregistry.azurecr.io/myapp:latest",
			wantRef: ImageRef{
				Registry:   "myregistry.azurecr.io",
				Repository: "myapp",
				Tag:        "latest",
				Full:       "myregistry.azurecr.io/myapp:latest",
			},
			wantKind: RegistryACR,
		},
		{
			name:  "Docker Hub short form",
			image: "nginx:latest",
			wantRef: ImageRef{
				Repository: "nginx",
				Tag:        "latest",
				Full:       "nginx:latest",
			},
			wantKind: RegistryUnknown,
		},
		{
			name:  "Docker Hub library path",
			image: "library/nginx:1.25",
			wantRef: ImageRef{
				Repository: "library/nginx",
				Tag:        "1.25",
				Full:       "library/nginx:1.25",
			},
			wantKind: RegistryUnknown,
		},
		{
			name:  "Docker Hub official with docker.io",
			image: "docker.io/library/nginx:latest",
			wantRef: ImageRef{
				Registry:   "docker.io",
				Repository: "library/nginx",
				Tag:        "latest",
				Full:       "docker.io/library/nginx:latest",
			},
			wantKind: RegistryUnknown,
		},
		{
			name:    "empty string",
			image:   "",
			wantRef: ImageRef{Full: ""},
		},
		{
			name:  "no tag or digest",
			image: "gcr.io/project/image",
			wantRef: ImageRef{
				Registry:   "gcr.io",
				Repository: "project/image",
				Full:       "gcr.io/project/image",
			},
			wantKind: RegistryGCR,
		},
		{
			name:  "registry with port",
			image: "localhost:5000/myrepo:v1",
			wantRef: ImageRef{
				Registry:   "localhost:5000",
				Repository: "myrepo",
				Tag:        "v1",
				Full:       "localhost:5000/myrepo:v1",
			},
			wantKind: RegistryUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseImageRef(tt.image)
			assert.Equal(t, tt.wantRef.Registry, got.Registry, "Registry")
			assert.Equal(t, tt.wantRef.Repository, got.Repository, "Repository")
			assert.Equal(t, tt.wantRef.Tag, got.Tag, "Tag")
			assert.Equal(t, tt.wantRef.Digest, got.Digest, "Digest")
			assert.Equal(t, tt.wantRef.Full, got.Full, "Full")
			assert.Equal(t, tt.wantKind, got.RegistryKind(), "RegistryKind")
		})
	}
}

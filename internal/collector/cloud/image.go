// SPDX-License-Identifier: Apache-2.0

package cloud

import "strings"

// RegistryType classifies a container image registry.
type RegistryType int

const (
	RegistryUnknown          RegistryType = iota
	RegistryECR                           // AWS Elastic Container Registry
	RegistryGCR                           // Google Container Registry (legacy)
	RegistryArtifactRegistry              // Google Artifact Registry
	RegistryACR                           // Azure Container Registry
)

// ImageRef holds the parsed components of a container image reference.
type ImageRef struct {
	Registry   string // hostname (e.g. "123456789.dkr.ecr.us-east-1.amazonaws.com")
	Repository string // repo path without tag/digest (e.g. "my-app")
	Tag        string // tag if present (e.g. "latest"), empty when digest is used
	Digest     string // sha256:... if present, empty when tag is used
	Full       string // original unmodified string
}

// RegistryKind classifies the registry hostname.
func (ref ImageRef) RegistryKind() RegistryType {
	r := ref.Registry
	switch {
	case strings.Contains(r, ".dkr.ecr.") && strings.HasSuffix(r, ".amazonaws.com"):
		return RegistryECR
	case r == "gcr.io" || strings.HasSuffix(r, ".gcr.io"):
		return RegistryGCR
	case strings.HasSuffix(r, "-docker.pkg.dev"):
		return RegistryArtifactRegistry
	case strings.HasSuffix(r, ".azurecr.io"):
		return RegistryACR
	default:
		return RegistryUnknown
	}
}

// ParseImageRef parses a container image string into its components.
// It handles ECR, GCR, Artifact Registry, ACR, and Docker Hub formats.
// Docker Hub short-form images (no hostname slash) are returned with an
// empty Registry, resulting in RegistryUnknown.
func ParseImageRef(image string) ImageRef {
	ref := ImageRef{Full: image}
	if image == "" {
		return ref
	}

	// Split off digest first: repo@sha256:abc...
	remaining := image
	if idx := strings.Index(remaining, "@"); idx >= 0 {
		ref.Digest = remaining[idx+1:]
		remaining = remaining[:idx]
	} else {
		// Split off tag: repo:tag  (but not port like registry:5000/repo)
		remaining, ref.Tag = splitTag(remaining)
	}

	// Split registry from repository. The first component is a registry
	// hostname if it contains a dot or colon (port), per OCI distribution spec.
	parts := strings.SplitN(remaining, "/", 2)
	if len(parts) == 2 && isRegistryHost(parts[0]) {
		ref.Registry = parts[0]
		ref.Repository = parts[1]
	} else {
		// Docker Hub short form (e.g. "nginx" or "library/nginx") — no registry.
		ref.Repository = remaining
	}

	return ref
}

// splitTag splits "repo:tag" returning (repo, tag). If the last colon sits
// inside the first path component it is treated as a port, not a tag.
func splitTag(s string) (string, string) {
	idx := strings.LastIndex(s, ":")
	if idx < 0 {
		return s, ""
	}
	// If there is no slash after the colon, the part after colon is a tag.
	// If the colon comes before the first slash it is a port (e.g. localhost:5000/repo).
	firstSlash := strings.Index(s, "/")
	if firstSlash >= 0 && idx < firstSlash {
		return s, "" // colon is part of host:port
	}
	return s[:idx], s[idx+1:]
}

// isRegistryHost returns true when the first path component looks like a
// registry hostname (contains a dot or colon). Plain names like "library"
// are treated as Docker Hub repository prefixes.
func isRegistryHost(s string) bool {
	return strings.ContainsAny(s, ".:")
}

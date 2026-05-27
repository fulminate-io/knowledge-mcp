// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"strings"

	"cloud.google.com/go/artifactregistry/apiv1/artifactregistrypb"
)

// remoteRepoUpstream resolves an Artifact Registry RemoteRepositoryConfig
// to a stable upstream identifier for the EdgeProxiesFrom target. Returns
// (targetID, displayName, remoteType). The targetID is keyed on the typed
// PublicRepository enum or CustomRepository.Uri host — never the
// user-supplied Description field, which would fragment / collide proxy
// nodes across organizations or sibling repos.
//
// remoteType is "public" for canonical registries (Docker Hub, Maven
// Central, etc.) and "custom" for non-public mirrors. Empty target means
// the config does not encode a recognizable upstream — caller should
// emit no edge.
func remoteRepoUpstream(rcfg *artifactregistrypb.RemoteRepositoryConfig) (targetID, displayName, remoteType string) {
	switch src := rcfg.GetRemoteSource().(type) {
	case *artifactregistrypb.RemoteRepositoryConfig_DockerRepository_:
		return dockerRemoteUpstream(src.DockerRepository)
	case *artifactregistrypb.RemoteRepositoryConfig_MavenRepository_:
		return mavenRemoteUpstream(src.MavenRepository)
	case *artifactregistrypb.RemoteRepositoryConfig_NpmRepository_:
		return npmRemoteUpstream(src.NpmRepository)
	case *artifactregistrypb.RemoteRepositoryConfig_PythonRepository_:
		return pythonRemoteUpstream(src.PythonRepository)
	case *artifactregistrypb.RemoteRepositoryConfig_AptRepository_:
		return aptRemoteUpstream(src.AptRepository)
	case *artifactregistrypb.RemoteRepositoryConfig_YumRepository_:
		return yumRemoteUpstream(src.YumRepository)
	case *artifactregistrypb.RemoteRepositoryConfig_CommonRepository:
		if uri := src.CommonRepository.GetUri(); uri != "" {
			return customRemoteIdentity("common", uri)
		}
	}
	return "", "", ""
}

func dockerRemoteUpstream(r *artifactregistrypb.RemoteRepositoryConfig_DockerRepository) (string, string, string) {
	if c := r.GetCustomRepository(); c != nil && c.GetUri() != "" {
		return customRemoteIdentity("docker", c.GetUri())
	}
	switch r.GetPublicRepository() {
	case artifactregistrypb.RemoteRepositoryConfig_DockerRepository_DOCKER_HUB:
		return "remote://docker.io", "Docker Hub", "public"
	}
	return "", "", ""
}

func mavenRemoteUpstream(r *artifactregistrypb.RemoteRepositoryConfig_MavenRepository) (string, string, string) {
	if c := r.GetCustomRepository(); c != nil && c.GetUri() != "" {
		return customRemoteIdentity("maven", c.GetUri())
	}
	switch r.GetPublicRepository() {
	case artifactregistrypb.RemoteRepositoryConfig_MavenRepository_MAVEN_CENTRAL:
		return "remote://repo.maven.apache.org", "Maven Central", "public"
	}
	return "", "", ""
}

func npmRemoteUpstream(r *artifactregistrypb.RemoteRepositoryConfig_NpmRepository) (string, string, string) {
	if c := r.GetCustomRepository(); c != nil && c.GetUri() != "" {
		return customRemoteIdentity("npm", c.GetUri())
	}
	switch r.GetPublicRepository() {
	case artifactregistrypb.RemoteRepositoryConfig_NpmRepository_NPMJS:
		return "remote://registry.npmjs.org", "npmjs", "public"
	}
	return "", "", ""
}

func pythonRemoteUpstream(r *artifactregistrypb.RemoteRepositoryConfig_PythonRepository) (string, string, string) {
	if c := r.GetCustomRepository(); c != nil && c.GetUri() != "" {
		return customRemoteIdentity("python", c.GetUri())
	}
	switch r.GetPublicRepository() {
	case artifactregistrypb.RemoteRepositoryConfig_PythonRepository_PYPI:
		return "remote://pypi.org", "PyPI", "public"
	}
	return "", "", ""
}

func aptRemoteUpstream(r *artifactregistrypb.RemoteRepositoryConfig_AptRepository) (string, string, string) {
	if c := r.GetCustomRepository(); c != nil && c.GetUri() != "" {
		return customRemoteIdentity("apt", c.GetUri())
	}
	if pub := r.GetPublicRepository(); pub != nil {
		// PublicRepository here is itself a struct with a RepositoryBase enum
		// (DEBIAN, UBUNTU) and a RepositoryPath. The pair is enough to identify
		// the upstream without fragmenting on user description.
		base := pub.GetRepositoryBase().String()
		path := pub.GetRepositoryPath()
		if base != "" {
			return "remote://apt/" + base + "/" + path, base, "public"
		}
	}
	return "", "", ""
}

func yumRemoteUpstream(r *artifactregistrypb.RemoteRepositoryConfig_YumRepository) (string, string, string) {
	if c := r.GetCustomRepository(); c != nil && c.GetUri() != "" {
		return customRemoteIdentity("yum", c.GetUri())
	}
	if pub := r.GetPublicRepository(); pub != nil {
		base := pub.GetRepositoryBase().String()
		path := pub.GetRepositoryPath()
		if base != "" {
			return "remote://yum/" + base + "/" + path, base, "public"
		}
	}
	return "", "", ""
}

// customRemoteIdentity normalizes a custom upstream URI into a stable
// (targetID, displayName, "custom") tuple. The host portion of the URI is
// the natural identity; URI path differences for the same host shouldn't
// fragment proxy nodes.
func customRemoteIdentity(format, uri string) (string, string, string) {
	host := uri
	if idx := strings.Index(host, "://"); idx >= 0 {
		host = host[idx+3:]
	}
	if idx := strings.Index(host, "/"); idx >= 0 {
		host = host[:idx]
	}
	host = strings.ToLower(host)
	if host == "" {
		return "", "", ""
	}
	return "remote://" + format + "/" + host, host, "custom"
}

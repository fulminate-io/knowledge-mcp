// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"regexp"
	"strings"
)

// gkeGraphPrefix is the leading token every GKE cloud graph name uses.
// Matches the format emitted by cloud/gcp/gke.go:159's gkeKubecontext:
// "gke_{project}_{region}_{cluster}". Surfaced as a constant so the
// parser and the selfLink builder share a single source of truth.
const gkeGraphPrefix = "gke_"

// gcpRegionPattern matches a GCP region or zone token embedded between
// underscores — "_us-central1_", "_europe-west4_", "_asia-southeast1_",
// or their zone variants "_us-central1-a_". The anchor underscores let
// us locate the region segment inside the graph name even when project
// ids contain hyphens and cluster names contain underscores.
//
// Shape rationale:
//   - {continent prefix: us, europe, asia, australia, northamerica, ...}
//     → [a-z]+
//   - separator hyphen
//   - {region word: central, west, east, south, north, southeast, ...}
//     → [a-z]+
//   - {digit suffix identifying the region instance} → [0-9]+
//   - optional zone suffix "-a" / "-b" / "-c" for multi-zone clusters
//     (regional clusters omit it; we capture both via the optional
//     group).
//
// The outer underscores ensure we match at token boundaries so project
// ids like "my-gke-project" or cluster names like "main_cluster" cannot
// accidentally steal characters from the region window.
var gcpRegionPattern = regexp.MustCompile(`_([a-z]+-[a-z]+[0-9]+(?:-[a-z])?)_`)

// parseGKEGraphName splits a cloud-graph name of the form
// "gke_{project}_{region}_{cluster}" into its components. It accepts
// project ids with hyphens (GCP's spec) and cluster names with
// underscores by locating the region segment via gcpRegionPattern
// rather than naive token splitting — a plain Split on "_" would
// mis-parse any cluster whose name contains an underscore.
//
// Returns ok=false for:
//   - Non-GKE prefixes (bare EKS/AKS kubecontexts, test names)
//   - Names without a recognizable region token
//   - Empty project, region, or cluster segments
//
// The matched region is the FIRST region-shaped token inside the name
// because project ids never contain region-shaped substrings (GCP
// projects are lowercase letters / digits / hyphens but do not embed
// "{word}-{word}{digit}+" patterns in practice).
func parseGKEGraphName(name string) (project, region, cluster string, ok bool) {
	if !strings.HasPrefix(name, gkeGraphPrefix) {
		return "", "", "", false
	}
	// Region regex works on the full name; the "_us-central1_" match
	// carries its anchor underscores so FindStringSubmatchIndex points
	// us directly at the project/cluster boundaries.
	idx := gcpRegionPattern.FindStringSubmatchIndex(name)
	if idx == nil {
		return "", "", "", false
	}
	// idx layout: [matchStart, matchEnd, groupStart, groupEnd]
	matchStart, matchEnd := idx[0], idx[1]
	groupStart, groupEnd := idx[2], idx[3]

	// project is everything after "gke_" up to the leading anchor
	// underscore of the match.
	project = name[len(gkeGraphPrefix):matchStart]
	region = name[groupStart:groupEnd]
	// cluster is everything after the trailing anchor underscore.
	cluster = name[matchEnd:]

	if project == "" || region == "" || cluster == "" {
		return "", "", "", false
	}
	return project, region, cluster, true
}

// gkeClusterSelfLink returns the canonical GKE Cluster resource URL
// exactly as the GCP collector stores it. The GCP container API
// populates cluster.SelfLink with
// "https://container.googleapis.com/v1/projects/{project}/locations/{location}/clusters/{cluster}",
// which is the node ID the GCP cloud graph uses for the Cluster node.
//
// Keeping this formatter alongside parseGKEGraphName makes the
// round-trip explicit: parser extracts the triple from the graph name;
// builder reconstructs the GCP Cluster node ID to target with a
// cross-graph proxy.
func gkeClusterSelfLink(project, location, cluster string) string {
	return "https://container.googleapis.com/v1/projects/" + project +
		"/locations/" + location + "/clusters/" + cluster
}

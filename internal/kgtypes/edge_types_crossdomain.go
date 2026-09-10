// SPDX-License-Identifier: Apache-2.0

package kgtypes

// Cross-domain edge types.
//
// THEY LIVE BESIDE edge_types.go RATHER THAN IN IT, and the split is by
// VOCABULARY rather than by size alone: that file holds the CODE and KNOWLEDGE
// edge types, which the collector and the reasoning graph produce, while every
// constant here is produced by the cross-graph linker.
//
// THE CLOUD, LOG AND CI/CD VOCABULARIES THAT USED TO SHARE THIS FILE ARE GONE,
// with the built-in collectors that emitted them. What a contrib collector emits
// into its own registered graph is that collector's vocabulary and is declared in
// its registration, not here: the edge type is an OPEN string on the wire, so a
// family with no built-in producer needs no constant in this module. That is the
// rule this file's own header stated before the CI/CD block was subject to it,
// and applying it to that block is what emptied the file's original subject.
//
// THE CROSS-DOMAIN BLOCK STAYS BECAUSE IT STILL HAS A PRODUCER: linker/
// dockerfile.go emits BUILDS between a repo's Dockerfile and the files and
// packages it copies, and that pass is the one surviving member of the
// cross-graph linker. Two of its siblings were emitted by collectors that are
// gone; they are kept as the vocabulary a contrib collector writes against,
// which is the same reason the server's twin keeps its rows.
const (
	// Cross-domain edge types (uppercase, for linkage graph relationships).
	EdgeBuilds     EdgeType = "BUILDS"     // Dockerfile/CI pipeline → container image → Deployment (code artifact produces cloud resource)
	EdgeDeploys    EdgeType = "DEPLOYS"    // Helm chart/deploy script → K8s resources (deployment tool creates cloud resources)
	EdgeManages    EdgeType = "MANAGES"    // IaC/SDK wrapper code → cloud resources (code manages cloud lifecycle)
	EdgeConfigures EdgeType = "CONFIGURES" // Config files in code → ConfigMaps/Secrets in cloud (code configures cloud resources)
	EdgeServes     EdgeType = "SERVES"     // Service/Ingress cloud resource → code endpoint (cloud routes traffic to code)
)

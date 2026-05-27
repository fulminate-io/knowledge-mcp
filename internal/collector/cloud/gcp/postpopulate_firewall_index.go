// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"encoding/json"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// instanceSpec is a partial JSON schema for a GCP compute instance, covering
// tags, service accounts, and network interfaces.
type instanceSpec struct {
	Tags              *instanceTags `json:"tags,omitempty"`
	ServiceAccounts   []instanceSA  `json:"serviceAccounts,omitempty"`
	NetworkInterfaces []instanceNIC `json:"networkInterfaces,omitempty"`
}

type instanceTags struct {
	Items []string `json:"items,omitempty"`
}

type instanceSA struct {
	Email string `json:"email,omitempty"`
}

type instanceNIC struct {
	Network string `json:"network,omitempty"`
}

// instanceRef holds a parsed instance for index lookups.
type instanceRef struct {
	id       string
	networks []string // VPC network URLs this instance is connected to
}

// instanceIndex provides O(1) lookups for firewall target/source matching.
type instanceIndex struct {
	byTag     map[string][]instanceRef // network tag -> instances
	bySA      map[string][]instanceRef // service account email -> instances
	byNetwork map[string][]instanceRef // VPC network URL -> instances
}

// buildInstanceIndex parses instance Content JSON and indexes by tag, service
// account, and network for O(1) lookup during firewall matching.
func buildInstanceIndex(nodes []*knowledgev1.Node) instanceIndex {
	idx := instanceIndex{
		byTag:     make(map[string][]instanceRef),
		bySA:      make(map[string][]instanceRef),
		byNetwork: make(map[string][]instanceRef),
	}
	for _, node := range nodes {
		ref := parseInstanceRef(node)
		if ref == nil {
			continue
		}
		var spec instanceSpec
		if err := json.Unmarshal([]byte(node.Content), &spec); err != nil {
			continue
		}
		if spec.Tags != nil {
			for _, tag := range spec.Tags.Items {
				idx.byTag[tag] = append(idx.byTag[tag], *ref)
			}
		}
		for _, sa := range spec.ServiceAccounts {
			if sa.Email != "" {
				idx.bySA[sa.Email] = append(idx.bySA[sa.Email], *ref)
			}
		}
		for _, net := range ref.networks {
			idx.byNetwork[net] = append(idx.byNetwork[net], *ref)
		}
	}
	return idx
}

func parseInstanceRef(node *knowledgev1.Node) *instanceRef {
	if len(node.Content) == 0 {
		return nil
	}
	var spec instanceSpec
	if err := json.Unmarshal([]byte(node.Content), &spec); err != nil {
		return nil
	}
	ref := &instanceRef{id: node.Id}
	for _, nic := range spec.NetworkInterfaces {
		if nic.Network != "" {
			ref.networks = append(ref.networks, nic.Network)
		}
	}
	return ref
}

// resolveTargets finds instances that a firewall applies to. If no targetTags
// and no targetServiceAccounts are set, the firewall applies to ALL instances
// in the VPC (per GCP documentation).
func resolveTargets(spec firewallContent, idx instanceIndex) []instanceRef {
	var targets []instanceRef
	seen := make(map[string]bool)
	addUnique := func(refs []instanceRef) {
		for _, inst := range refs {
			if !seen[inst.id] {
				seen[inst.id] = true
				targets = append(targets, inst)
			}
		}
	}
	for _, tag := range spec.TargetTags {
		addUnique(idx.byTag[tag])
	}
	for _, sa := range spec.TargetServiceAccounts {
		addUnique(idx.bySA[sa])
	}
	// No target constraints: match all instances in the VPC.
	if len(spec.TargetTags) == 0 && len(spec.TargetServiceAccounts) == 0 {
		addUnique(idx.byNetwork[derefStr(spec.Network)])
	}
	return targets
}

func resolveSources(spec firewallContent, idx instanceIndex) []instanceRef {
	var sources []instanceRef
	seen := make(map[string]bool)
	for _, tag := range spec.SourceTags {
		for _, inst := range idx.byTag[tag] {
			if !seen[inst.id] {
				seen[inst.id] = true
				sources = append(sources, inst)
			}
		}
	}
	for _, sa := range spec.SourceServiceAccounts {
		for _, inst := range idx.bySA[sa] {
			if !seen[inst.id] {
				seen[inst.id] = true
				sources = append(sources, inst)
			}
		}
	}
	return sources
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

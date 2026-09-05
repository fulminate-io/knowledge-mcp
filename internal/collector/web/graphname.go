// SPDX-License-Identifier: Apache-2.0

package web

import (
	"fmt"
	"net/url"
	"strings"
)

// SeedHost returns the crawl's SOURCE IDENTITY: the normalized host of the
// first seed URL. It is the value a later collect is compared against to
// decide whether an incoming crawl is the same site as the one a graph
// already holds, so the normalization is the whole point — an https URL at
// WWW.Example.com and one at example.com are ONE site, and a comparison that
// said otherwise would refuse a legitimate re-collect of the same crawl.
//
// The normalization is: take u.Hostname(), which drops any port; lowercase
// it; trim a leading "www." prefix.
//
// IT REFUSES RATHER THAN DEFAULTING. An empty seed list, an empty first
// entry, a URL that will not parse, and a URL carrying no host are each an
// error naming the condition. Inventing a host here would name a graph after
// a site nobody asked for, and the caller could not tell.
func SeedHost(seedURLs []string) (string, error) {
	if len(seedURLs) == 0 || seedURLs[0] == "" {
		return "", fmt.Errorf(
			"web collector: cannot derive a graph name: no seed URL to take a host from; " +
				"supply an explicit id to name the graph, or a seed_urls entry to name it after")
	}
	u, err := url.Parse(seedURLs[0])
	if err != nil {
		return "", fmt.Errorf("web collector: parse seed URL %q: %w", seedURLs[0], err)
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", fmt.Errorf("web collector: seed URL %q has no host component", seedURLs[0])
	}
	return strings.TrimPrefix(host, "www."), nil
}

// GraphName is the web family's rule for the name a collect lands under.
//
// An EXPLICIT source wins VERBATIM. It is returned exactly as given, never
// sanitized and never derived, because a web graph has always been named by
// the string its collect supplied — rewriting one here would rename every
// existing web graph on its next collect.
//
// With no source, the name is derived from the crawl's seed host: SeedHost's
// answer with every dot mapped to a hyphen. www.Go101.org yields go101-org;
// an httptest server at 127.0.0.1 with a port yields 127-0-0-1.
//
// A DERIVED CHARACTER OUTSIDE [a-z0-9-_] IS AN ERROR, not a silent sanitize.
// A DNS host is already constrained to that shape once dots are mapped, so
// anything else means the input was not the host it claimed to be, and the
// honest answer is to name the host and the offending character rather than
// to quietly produce a name the caller never asked for.
func GraphName(source string, seedURLs []string) (string, error) {
	if source != "" {
		return source, nil
	}
	host, err := SeedHost(seedURLs)
	if err != nil {
		return "", err
	}
	name := strings.ReplaceAll(host, ".", "-")
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return "", fmt.Errorf(
				"web collector: seed host %q derives graph name %q carrying %q, which is outside [a-z0-9-_]; "+
					"supply an explicit id to name the graph", host, name, string(r))
		}
	}
	return name, nil
}

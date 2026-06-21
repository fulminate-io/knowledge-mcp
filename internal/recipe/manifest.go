// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"fmt"
	"strings"
)

// Manifest keys — the collect layer stuffs these into
// Options.SourceManifest so RunRecipe can recover both the source slug
// (which web graph to read) and the recipe name (which recipe body to
// fetch from GraphTransformers).
const (
	manifestKeySource = "source"
	manifestKeyRecipe = "recipe"
)

// FormatSourceManifest renders (source, recipe) as a semicolon-
// delimited key=value string. Example output:
//
//	source=hohpe-eip;recipe=eip-to-design-patterns
//
// Chosen over JSON because this string lands in audit-thought
// metadata where it will be eyeballed by operators — the k=v form
// reads cleanly and survives copy-paste without quoting surprises.
// Neither arg may contain '=' or ';' — that would corrupt the
// round-trip. Callers are expected to pass URL-safe slugs; the
// formatter errors via panic (via Sprintf-then-validate) only when
// sanity-check fails, which never happens on well-formed inputs.
func FormatSourceManifest(source, recipe string) string {
	return fmt.Sprintf("%s=%s;%s=%s",
		manifestKeySource, source,
		manifestKeyRecipe, recipe,
	)
}

// ParseSourceManifest recovers (source, recipe) from a manifest
// string produced by FormatSourceManifest. Unknown keys are silently
// ignored for forward compatibility (a future version could add
// `format=` or `version=` segments without breaking older readers).
// Missing source or recipe is surfaced as an error.
func ParseSourceManifest(manifest string) (source, recipe string, err error) {
	if manifest == "" {
		return "", "", fmt.Errorf("empty SourceManifest")
	}
	for segment := range strings.SplitSeq(manifest, ";") {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			continue
		}
		eq := strings.IndexByte(segment, '=')
		if eq <= 0 {
			return "", "", fmt.Errorf("malformed SourceManifest segment %q (expected key=value)", segment)
		}
		key := strings.TrimSpace(segment[:eq])
		value := strings.TrimSpace(segment[eq+1:])
		switch key {
		case manifestKeySource:
			source = value
		case manifestKeyRecipe:
			recipe = value
		}
	}
	if source == "" {
		return "", "", fmt.Errorf("SourceManifest %q missing required %q key", manifest, manifestKeySource)
	}
	if recipe == "" {
		return "", "", fmt.Errorf("SourceManifest %q missing required %q key", manifest, manifestKeyRecipe)
	}
	return source, recipe, nil
}

// SPDX-License-Identifier: Apache-2.0

package config

import (
	"maps"
	"slices"
	"strings"
	"testing"
)

// providerOptionFields returns every settings field the form renders as a
// provider menu, keyed by its dotted path.
//
// It reads MainSettingsMetadata rather than a hand-written list of paths so
// the pin cannot pass vacuously: a field path that moves takes its row with
// it and the assertions below fail, instead of a lookup quietly finding
// nothing.
func providerOptionFields(t *testing.T) map[string][]any {
	t.Helper()
	out := map[string][]any{}
	for _, field := range MainSettingsMetadata().Fields {
		if len(field.Options) == 0 || field.Path[len(field.Path)-1] != "provider" {
			continue
		}
		out[strings.Join(field.Path, ".")] = field.Options
	}
	return out
}

// TestSettingsMetadataNeverOffersFakeProvider pins the requirement that no
// test-only provider is OFFERED in any user-facing list. The fake stays
// REPRESENTABLE — EmbedProvider.IsValid still accepts it, and a stored
// configuration naming it still displays that value as the current one — so
// this asserts the offered Options only.
func TestSettingsMetadataNeverOffersFakeProvider(t *testing.T) {
	fields := providerOptionFields(t)
	if len(fields) == 0 {
		t.Fatalf("providerOptionFields(MainSettingsMetadata()) = 0 provider menus, want at least the embedder, reranker and LLM ones")
	}
	for _, path := range slices.Sorted(maps.Keys(fields)) {
		if slices.Contains(fields[path], any(EmbedProviderFake.String())) {
			t.Errorf("MainSettingsMetadata() field %q offers options %v, want no %q option", path, fields[path], EmbedProviderFake)
		}
	}
	// The two embed-axis menus are the ones the requirement names, so the
	// pin states that both were present and inspected. Without this, a
	// schema change that dropped the reranker's provider field entirely
	// would satisfy the loop above by having nothing to check.
	for _, path := range []string{"embedder.provider", "reranker.provider"} {
		options, ok := fields[path]
		if !ok {
			t.Errorf("MainSettingsMetadata() has no provider menu at %q; the paths present are %v", path, slices.Sorted(maps.Keys(fields)))
			continue
		}
		for _, want := range []EmbedProvider{EmbedProviderVoyage, EmbedProviderCohere, EmbedProviderGemini, EmbedProviderOpenAICompatible} {
			if !slices.Contains(options, any(want.String())) {
				t.Errorf("MainSettingsMetadata() field %q offers %v, want it to still offer %q", path, options, want)
			}
		}
	}
}

// TestFakeEmbedProviderStaysRepresentable is the other half of the
// requirement, and the control for the pin above: removing the fake from
// the offered menus must not make a stored fake configuration invalid,
// because the wording is "never offered", not "never displayed".
func TestFakeEmbedProviderStaysRepresentable(t *testing.T) {
	if !EmbedProviderFake.IsValid() {
		t.Errorf("EmbedProviderFake.IsValid() = false, want true so a stored fake value still displays as the current one")
	}
}

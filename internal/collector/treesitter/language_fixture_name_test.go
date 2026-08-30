// SPDX-License-Identifier: Apache-2.0

package treesitter

import "testing"

// TestFixtureFileName_RoundTrips states the helper's whole correctness claim as
// a ROUND TRIP rather than as a table of expected names: whatever name the
// helper picks, detection must route it back to the language it was picked for.
// A table would restate the answer the helper computes and could not catch the
// two failures that matter — an extension that stops mapping to its language,
// and a fallback spelling the filename switch no longer recognizes.
func TestFixtureFileName_RoundTrips(t *testing.T) {
	resolved := 0
	for _, lang := range RegisteredLanguages() {
		name, ok := FixtureFileName(lang)
		if !ok {
			// A registered language with no route is a real answer, not a
			// failure: the caller refuses it loudly. It just must not be a
			// silent one, so the name has to be empty too.
			if name != "" {
				t.Errorf("FixtureFileName(%q) returned ok=false with a non-empty name %q", lang, name)
			}
			continue
		}
		resolved++
		if got := DetectLanguage(name); got != lang {
			t.Errorf("FixtureFileName(%q) = %q, which DetectLanguage routes to %q", lang, name, got)
		}
	}
	// Known-positive floor. Without it a helper that returned ("", false) for
	// every language would satisfy every assertion above by never entering the
	// round-trip branch at all. The four named languages are the ones the corpus
	// fixture executor actually needs, and dockerfile is the one that can only
	// resolve through the extensionless fallback.
	if resolved == 0 {
		t.Fatal("FixtureFileName resolved no registered language at all; the round trip above never ran")
	}
	for _, lang := range []Language{LangGo, LangPython, LangTypeScript, LangDockerfile} {
		if _, ok := FixtureFileName(lang); !ok {
			t.Errorf("FixtureFileName(%q) must resolve", lang)
		}
	}

	t.Run("unknown is refused", func(t *testing.T) {
		if name, ok := FixtureFileName(LangUnknown); ok || name != "" {
			t.Errorf("FixtureFileName(LangUnknown) = (%q, %v), want (\"\", false)", name, ok)
		}
	})

	// Every extensionless spelling is exercised directly, because the languages
	// that also have extensions never reach the fallback through the loop above
	// — an entry there would otherwise be able to rot unobserved.
	t.Run("extensionless fallbacks route back", func(t *testing.T) {
		for lang, name := range extensionlessNames {
			if got := DetectLanguage(name); got != lang {
				t.Errorf("extensionlessNames[%q] = %q, which DetectLanguage routes to %q", lang, name, got)
			}
		}
	})
}

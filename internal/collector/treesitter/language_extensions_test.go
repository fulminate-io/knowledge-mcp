// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestAddedExtensions asserts membership per extension, never a total. An
// unmapped extension is TOTAL ABSENCE rather than degraded chunking — the
// discovery gate declines the file before any chunker sees it — so each routing
// is asserted individually and a count is deliberately not, since a count is a
// number another change can move without breaking anything.
func TestAddedExtensions(t *testing.T) {
	for path, want := range map[string]Language{
		"a.cjs":     LangJavaScript,
		"a.mts":     LangTypeScript,
		"a.cts":     LangTypeScript,
		"a.hxx":     LangCPP,
		"a.c++":     LangCPP,
		"a.h++":     LangCPP,
		"a.ipp":     LangCPP,
		"a.tpp":     LangCPP,
		"a.inl":     LangCPP,
		"a.csx":     LangCSharp,
		"a.rake":    LangRuby,
		"a.gemspec": LangRuby,
		"a.ru":      LangRuby,
		"a.sbt":     LangScala,
		"a.tfvars":  LangHCL,
		"a.phtml":   LangPHP,
		"a.pyw":     LangPython,
		"a.ksh":     LangBash,
		"a.pgsql":   LangSQL,
		"a.mysql":   LangSQL,
		"a.gvy":     LangGroovy,
		"a.gy":      LangGroovy,
		"a.mdx":     LangMarkdown,
	} {
		assert.Equal(t, want, DetectLanguage(path), "%s must route to %s", path, want)
	}

	// The four measured as haserror=true under the grammar they would have
	// routed to. Their exclusion is a decision, so it is asserted rather than
	// left to the absence of a table entry.
	for _, path := range []string{"x.less", "x.sass", "x.heex", "x.xhtml"} {
		assert.Equal(t, LangUnknown, DetectLanguage(path),
			"%s parses with errors under the grammar it would route to", path)
	}

	// Known-positive control: an extension that was already mapped still is, so
	// a build where extMap failed to initialize could not pass the block above
	// by returning LangUnknown for everything.
	assert.Equal(t, LangGo, DetectLanguage("a.go"))
}

// TestExtensionlessFilenames covers the files that carry no extension at all,
// plus the Dockerfile.<suffix> form, which routed nowhere through either path:
// filepath.Ext returns the suffix, which is absent from extMap, and
// filepath.Base returns the whole name, which the filename switch does not
// match.
func TestExtensionlessFilenames(t *testing.T) {
	assert.Equal(t, LangRuby, DetectLanguage("Rakefile"))
	assert.Equal(t, LangRuby, DetectLanguage("proj/Gemfile"))
	assert.Equal(t, LangGroovy, DetectLanguage("ci/Jenkinsfile"))
	assert.Equal(t, LangDockerfile, DetectLanguage("Dockerfile.dev"))
	assert.Equal(t, LangDockerfile, DetectLanguage("build/dockerfile.prod"))

	// THE PREFIX RULE MUST NOT SWALLOW A REAL EXTENSION. extMap is consulted
	// first, so this ordering is what keeps a file genuinely named
	// Dockerfile.go routed to Go — asserted directly rather than trusted.
	assert.Equal(t, LangGo, DetectLanguage("Dockerfile.go"))

	// Controls: the pre-existing filename rules are unchanged, and a lockfile
	// is still unrouted here — discovery declines it under its own rule, and a
	// rule in two places is a rule one place overrides.
	assert.Equal(t, LangDockerfile, DetectLanguage("Dockerfile"))
	assert.Equal(t, LangBash, DetectLanguage("Makefile"))
	assert.Equal(t, LangUnknown, DetectLanguage("Gemfile.lock"))
	assert.Equal(t, LangUnknown, DetectLanguage("CMakeLists.txt"))
}

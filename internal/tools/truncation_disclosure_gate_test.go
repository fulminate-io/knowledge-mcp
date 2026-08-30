// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"sort"
	"strings"
	"testing"
)

// truncation_disclosure_gate_test.go holds the census GATE — the sub-tests that
// read truncation_disclosure_census_test.go's declaration table against the scan
// in truncation_disclosure_scan_test.go. The four-way split is a file-length
// consequence, not a design one: the pre-commit hook hard-errors above 500 lines
// on staged *.go and test files are not exempt, and the table plus its written
// reasons is most of a file on its own. The table stays in the census file
// because that is where a criterion extracts it from.

// TestTruncationDisclosureCensus is the live gate: every scanned site must be
// declared on both axes with written reasons, every disclosed-by-caller row must
// terminate on a real "handles" row, and every json_carrier "yes" row must
// actually emit the key.
func TestTruncationDisclosureCensus(t *testing.T) {
	all := scanCensusTree(t)
	var sites []fnFacts
	for _, f := range all {
		if f.isSite {
			sites = append(sites, f)
		}
	}

	t.Run("population", func(t *testing.T) {
		if len(sites) < minCensusPopulation {
			t.Fatalf("the member rule matched %d sites, want at least %d — a rule that stops matching\n"+
				"  reports a clean scan over a surface it can no longer see. Re-derive the rule against\n"+
				"  current source before touching this floor.", len(sites), minCensusPopulation)
		}
	})

	t.Run("declared", func(t *testing.T) {
		for _, s := range sites {
			if _, ok := truncationDisclosureSites[s.key()]; !ok {
				t.Errorf("undeclared truncation-disclosure site %s (clause %s) at %s\n"+
					"  Declare it in truncationDisclosureSites on BOTH axes with a written reason:\n"+
					"  disclosure is one of %q/%q/%q, json_carrier one of %q/%q/%q.",
					s.key(), s.clause, s.pos,
					disclosureHandles, disclosureByCaller, disclosureCannot,
					carrierYes, carrierNo, carrierNA)
			}
		}
	})

	t.Run("no_stale_rows", func(t *testing.T) {
		live := map[string]bool{}
		for _, s := range sites {
			live[s.key()] = true
		}
		for key := range truncationDisclosureSites {
			if !live[key] {
				t.Errorf("stale declaration %q: no scanned site matches it. A row outlasting its site\n"+
					"  turns the next real violation into a silent pass — delete it or fix the key.", key)
			}
		}
	})

	t.Run("vocabulary", func(t *testing.T) {
		for key, row := range truncationDisclosureSites {
			switch row.disclosure {
			case disclosureHandles, disclosureByCaller, disclosureCannot:
			default:
				t.Errorf("%s: disclosure %q is not one of the three declared values", key, row.disclosure)
			}
			switch row.carrier {
			case carrierYes, carrierNo, carrierNA:
			default:
				t.Errorf("%s: json_carrier %q is not one of the three declared values", key, row.carrier)
			}
			if strings.TrimSpace(row.reason) == "" {
				t.Errorf("%s: the disclosure reason is empty — a classification with no reason records a\n"+
					"  verdict without recording what a later reader must re-check", key)
			}
			if strings.TrimSpace(row.carrierWhy) == "" {
				t.Errorf("%s: the json_carrier reason is empty", key)
			}
		}
	})

	t.Run("chain_termination", func(t *testing.T) {
		for key, row := range truncationDisclosureSites {
			if row.disclosure != disclosureByCaller {
				continue
			}
			if !namesHandlingWrapper(row.reason) {
				t.Errorf("%s is %q but its reason names no wrapper this table classifies %q.\n"+
					"  Every disclosed-by-caller row must resolve, in one hop, to a real disclosure\n"+
					"  call this table declares. reason = %q",
					key, disclosureByCaller, disclosureHandles, row.reason)
			}
		}
	})

	t.Run("handles_actually_disclose", func(t *testing.T) {
		// THE GATE THAT WAS MISSING, and its absence was measured rather than
		// guessed: seven single-site mutations across the wired arms — removing a
		// WithTruncationNotice wrapper outright, or pinning a threaded verdict to
		// false — ALL left this package and the engine package fully green. A
		// "handles" row asserted a classification that nothing checked.
		//
		// fnFacts.callNames carries what is needed: the bare names of every
		// function the site's body calls. A handles row must call one of the
		// declared disclosure helpers, or name a refusal it issues instead.
		byKey := map[string]fnFacts{}
		for _, f := range all {
			byKey[f.key()] = f
		}
		for key, row := range truncationDisclosureSites {
			if row.disclosure != disclosureHandles {
				continue
			}
			f, ok := byKey[key]
			if !ok {
				continue // reported by no_stale_rows
			}
			if callsDisclosureHelper(f) || refusesOnTruncation(f, row) {
				continue
			}
			t.Errorf("%s is %q but its body calls no declared disclosure helper.\n"+
				"  A handles row must call one of %v, or REFUSE the read on the verdict and say so in\n"+
				"  its reason. Removing a wrapper must turn this row red — that is the whole point of\n"+
				"  the classification. calls = %v", key, disclosureHandles, disclosureHelpers, sortedCalls(f))
		}
	})

	t.Run("json_carriers", func(t *testing.T) {
		emitters := transitiveEmitters(all)
		byKey := map[string]fnFacts{}
		for _, f := range all {
			byKey[f.key()] = f
		}
		for key, row := range truncationDisclosureSites {
			if row.carrier != carrierYes {
				continue
			}
			f, ok := byKey[key]
			if !ok {
				continue // already reported by no_stale_rows
			}
			// A yes-row that reads no verdict is emitting a CONSTANT, and a constant
			// is admissible only when the row SAYS SO and says why. That is the same
			// reason-bearing shape censusSurvivors uses; a bare non-reading row is
			// the gate being talked out of its job.
			if !f.readsVerd && !strings.Contains(row.carrierWhy, constantByConstruction) {
				t.Errorf("%s is json_carrier %q but its body never reads a truncation verdict, so any\n"+
					"  key it emits is a CONSTANT. A constant is admissible only when the json_carrier\n"+
					"  reason contains %q AND names the structural fact that makes `false` true.",
					key, carrierYes, constantByConstruction)
				continue
			}
			if !emitters[f.name] {
				t.Errorf("%s is json_carrier %q but neither it nor anything it calls writes the\n"+
					"  `truncated` key into a payload. Reading the verdict is not emitting it.", key, carrierYes)
			}
		}
	})
}

// disclosureHelpers are the declared truncation-disclosure helpers. There are
// exactly three and the list is closed: the two engine wrappers, and the
// tree-rendering copy, which now lives in
// cmd/knowledge/internal/projects/render and is shared by the plan_tree arm and
// the assemble arms, permitted because its product copy deliberately differs. A
// fourth name here would be a second sentence in the tree, which the uniqueness
// gate refuses.
var disclosureHelpers = []string{
	"WithTruncationNotice",
	"WithTruncationNoticeFor",
	"AppendTruncationNotice",
}

// callsDisclosureHelper reports whether the site's own body calls one of them.
func callsDisclosureHelper(f fnFacts) bool {
	for _, h := range disclosureHelpers {
		if f.callNames[h] {
			return true
		}
	}
	return false
}

// refusesOnTruncation is the second legitimate way to disclose: REFUSE the read
// rather than render a partial one. It is admitted only when the row SAYS SO —
// the reason must contain "REFUSES" — and when the body actually calls the
// failure path it names, so a row cannot claim a refusal it does not issue.
func refusesOnTruncation(f fnFacts, row disclosureRow) bool {
	if !strings.Contains(row.reason, "REFUSES") {
		return false
	}
	for name := range f.callNames {
		if strings.Contains(strings.ToLower(name), "failure") || strings.Contains(strings.ToLower(name), "refus") {
			return true
		}
	}
	return false
}

// sortedCalls renders a site's call set for a failure message.
func sortedCalls(f fnFacts) []string {
	out := make([]string, 0, len(f.callNames))
	for name := range f.callNames {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// namesHandlingWrapper reports whether reason names, as a whole word, a function
// this table declares as a "handles" row.
func namesHandlingWrapper(reason string) bool {
	for key, row := range truncationDisclosureSites {
		if row.disclosure != disclosureHandles {
			continue
		}
		_, fn, ok := strings.Cut(key, ":")
		if !ok {
			continue
		}
		if containsWord(reason, fn) {
			return true
		}
	}
	return false
}

// containsWord reports whether word appears in s bounded by non-identifier
// bytes, so "Render" does not match inside "RenderBrowse".
func containsWord(s, word string) bool {
	for i := 0; i+len(word) <= len(s); i++ {
		if s[i:i+len(word)] != word {
			continue
		}
		if i > 0 && isIdentByte(s[i-1]) {
			continue
		}
		if j := i + len(word); j < len(s) && isIdentByte(s[j]) {
			continue
		}
		return true
	}
	return false
}

func isIdentByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

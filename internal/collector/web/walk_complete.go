// SPDX-License-Identifier: Apache-2.0

package web

// THE WALK-ASSERTION PARTITION. Every class in the degrade vocabulary declared
// in crawl.go (see "THE DEGRADE CLASS VOCABULARY") belongs to exactly one of the
// two lists below, and the RULE that decides which is this:
//
//	A class is a READ FAILURE when a unit this crawl SET OUT TO READ is missing
//	because reading it FAILED. Every other class is a scoping or policy decision
//	this collector made DELIBERATELY, under which the emission is still the
//	authoritative set FOR THE CONFIGURATION THAT PRODUCED IT.
//
// THIS IS THE CODE COLLECTOR'S RULE APPLIED, NOT A NEW POLICY. codesync clears
// its walk assertion on ChunkReport.Dropped() — a file it set out to read and
// could not — and never on a file that discovery scoped OUT.
//
// THE PARTITION IS TOTAL AND DISJOINT, and the totality is what makes it safe.
// walkCompleteFrom consults the read-failure list only, so a class in NEITHER
// list would read as "not a read failure" — the walk-complete, deletion-ENABLING
// answer — which is the opposite of the wire field's documented default, where
// the zero value is the safe one. TestWebDegradeVocabulary_EveryClassIsClassified
// ForTheWalkAssertion parses the const block and fails, naming the offending
// class, until a newly declared class is classified here.
var readFailureDegradeClasses = []string{
	degradeFetchFailed,
	degradeCleanFailed,
	degradeParseFailed,
	degradeRawLinkParseFailed,
	degradeGithubUnpackFailed,
	degradeGithubTarReadFailed,
}

// policyDegradeClasses names the classes that are this collector's own scoping
// and policy decisions. It exists to make the partition TOTAL and is never
// consulted at runtime — nothing reads it but the parity test.
//
// degradeNotAPage sits here rather than beside the read failures because it is a
// DECLINE-TO-PARSE: the fetch SUCCEEDED and the collector decided the response
// was not a page worth emitting, so nothing this crawl set out to read went
// unread. Classifying it as a read failure would clear the walk assertion on a
// healthy crawl and disable deletion for every site serving one non-page response.
var policyDegradeClasses = []string{
	degradeContentAlias,
	degradeHostCap,
	degradePathSegmentCap,
	degradeBudgetDeclined,
	degradeLinkDowngraded,
	degradeHiddenPruned,
	degradeNotAPage,
	degradeGithubUnsafePath,
	degradeGithubNonregular,
}

// walkCompleteFrom reports whether this crawl read every unit it set out to
// read, which is what entitles the server to treat the emission as the
// authoritative set and retire whatever the crawl did not re-emit. Any
// read-failure class with a positive count clears it.
func walkCompleteFrom(degraded map[string]int) bool {
	for _, c := range readFailureDegradeClasses {
		if degraded[c] > 0 {
			return false
		}
	}
	return true
}

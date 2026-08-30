// SPDX-License-Identifier: Apache-2.0

package veckernel

import (
	"fmt"
	"testing"
)

// pintable_test.go holds the STRUCTURAL checks on the performance contract: that
// every row is well formed, that every machine class pins the tiers it must, and
// that no class pins a tier it cannot execute.
//
// They live apart from perf_test.go because they are a different kind of check.
// Nothing here takes a timing measurement, so nothing here is behind the
// VECKERNEL_PERF gate — these run on every ordinary `go test`, which is what
// makes a malformed table fail immediately rather than the next time somebody
// opts into the timing suite.

// TestPinTableIsWellFormed is a cheap, always-on structural check on the
// performance contract. It needs no timing and therefore no env gate.
func TestPinTableIsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range pinTable {
		key := fmt.Sprintf("%s/%s/%d", p.Class, p.Tier, p.Dim)
		if seen[key] {
			t.Errorf("duplicate pin row for %s", key)
		}
		seen[key] = true

		if p.Unmeasured {
			if p.TraverseNsPerDistance != 0 || p.MicroNsPerDistance != 0 || p.Machine != "" {
				t.Errorf("%s is marked Unmeasured but carries numbers or a machine name — one of "+
					"the two is a lie", key)
			}
			continue
		}
		if p.Machine == "" {
			t.Errorf("%s carries numbers with no Machine — a pin without the silicon it came "+
				"from is not a measurement", key)
		}
		if p.TraverseNsPerDistance <= 0 {
			t.Errorf("%s has no traverse floor; that is the number the gate asserts on", key)
		}
	}

	// Every row must name a class the resolver can actually produce. A row keyed
	// to a class no host ever resolves to is a measurement that will never be
	// read — silently unguarded, and indistinguishable from a guarded tier from
	// inside the table.
	known := map[string]bool{ClassARM64: true, ClassAMD64AVX512: true, ClassAMD64NoAVX512: true}
	for _, p := range pinTable {
		if !known[p.Class] {
			t.Errorf("pin row %s/%s/%d names class %q, which machineClass() never returns — "+
				"that row can never be read", p.Class, p.Tier, p.Dim, p.Class)
		}
	}

	// Every tier this build can actually run needs a row at every pinned dim for
	// THIS host's class, or it is an unguarded tier here. Skipped when the class
	// has no rows at all: that is a different, louder failure the floor gate
	// reports, and duplicating it here as twelve identical errors buries it.
	class := machineClass()
	if !classHasPins(class) {
		t.Logf("machine class %q has no pins at all — per-tier completeness is not checked here; "+
			"TestPerfFloorsTraverse reports the unmeasured class", class)
		return
	}
	for _, a := range testArms() {
		for _, dim := range pinnedDims {
			if _, ok := pinFor(class, a.name, dim); !ok {
				t.Errorf("supported tier %s has no pin row at class %s/dim=%d", a.name, class, dim)
			}
		}
	}
}

// TestEveryClassesPinsAreCompleteAndMeasured checks the WHOLE performance
// contract from WHATEVER MACHINE runs it, including the classes this host will
// never be.
//
// The floor gate only ever queries rows for the class it is running on, so on an
// arm64 laptop the two amd64 classes are never read — and a typo, a missing dim,
// or a row carrying invented numbers would sit there undetected until somebody
// finally ran the suite on that hardware. It cuts every way now that three
// classes have rows, so this test reads every row explicitly.
//
// IT ASSERTS THE ROWS ARE MEASURED, which is the opposite of what its ancestor
// asserted before the amd64 kernels existed: while they did not exist, the
// requirement was that their slots be EMPTY, because plausible numbers for a
// kernel that has never executed would make the floor gate pass against a tier
// nobody had measured. Every class below has now been benchmarked on its own
// hardware, so the requirement inverts: an Unmeasured row means the table lost a
// measurement that was taken, and a row without a Machine means it lost the
// silicon.
//
// IT ALSO ASSERTS AN ABSENCE, which is the "never inherited" half and the one a
// per-slot completeness check would miss entirely. ClassAMD64NoAVX512 must carry
// NO amd64-avx512 row: that tier cannot execute on a part without AVX-512, so a
// row there could only have come from another class's numbers being copied
// across — exactly the borrowing that keying by class exists to prevent.
func TestEveryClassesPinsAreCompleteAndMeasured(t *testing.T) {
	// The tiers each class is REQUIRED to pin. Fixture-derived, not read back
	// out of pinTable, so a table that lost rows fails instead of agreeing with
	// itself about what it should contain.
	required := map[string][]string{
		ClassARM64:         {TierNEON, TierReference},
		ClassAMD64AVX512:   {TierAVX512, TierAVX2, TierReference},
		ClassAMD64NoAVX512: {TierAVX2, TierReference},
	}
	// The tiers each class must NOT pin, because they cannot execute there.
	forbidden := map[string][]string{
		ClassAMD64NoAVX512: {TierAVX512},
		ClassARM64:         {TierAVX512, TierAVX2},
		ClassAMD64AVX512:   {TierNEON},
	}

	checked := 0
	for class, tierList := range required {
		for _, tier := range tierList {
			for _, dim := range pinnedDims {
				p, ok := pinFor(class, tier, dim)
				if !ok {
					t.Errorf("no pin slot for %s/%s/dim=%d — that tier is unguarded on that "+
						"machine class, and the gate there would report a clean run having "+
						"checked nothing", class, tier, dim)
					continue
				}
				if p.Unmeasured {
					t.Errorf("%s/%s/dim=%d is marked Unmeasured. Every class in this table has "+
						"been benchmarked on its own hardware; an empty slot here means the "+
						"table lost a measurement that was taken.", class, tier, dim)
					continue
				}
				if p.TraverseNsPerDistance <= 0 {
					t.Errorf("%s/%s/dim=%d has no traverse floor, which is the number the gate "+
						"asserts on", class, tier, dim)
				}
				if p.Machine == "" {
					t.Errorf("%s/%s/dim=%d carries numbers with no Machine — a pin without the "+
						"silicon it came from is not a measurement", class, tier, dim)
				}
				checked++
			}
		}
	}

	// THE ABSENCE ASSERTION. A row here would be a borrowed number wearing this
	// class's name, and it would make the floor gate on that hardware guard a
	// tier the hardware cannot run.
	forbiddenChecked := 0
	for class, tierList := range forbidden {
		for _, tier := range tierList {
			for _, dim := range pinnedDims {
				if p, ok := pinFor(class, tier, dim); ok {
					t.Errorf("class %s carries a pin for tier %s at dim %d (Machine %q). That "+
						"tier cannot execute on that class, so the row can only have been copied "+
						"from another class — which is the inheritance keying by class exists to "+
						"prevent.", class, tier, dim, p.Machine)
				}
				forbiddenChecked++
			}
		}
	}

	// KNOWN POSITIVES FOR BOTH LOOPS, against fixture-derived constants rather
	// than counts these same loops produced. An emptiness assertion especially
	// needs one: a forbidden-tier sweep that iterated zero times would report a
	// clean absence having looked at nothing.
	wantChecked := (2 + 3 + 2) * len(pinnedDims)
	if checked != wantChecked {
		t.Fatalf("checked %d pin rows, expected %d — this gate examined a different table than "+
			"the one it claims to cover", checked, wantChecked)
	}
	wantForbidden := (1 + 2 + 1) * len(pinnedDims)
	if forbiddenChecked != wantForbidden {
		t.Fatalf("probed %d forbidden slots, expected %d — the absence assertion above proved "+
			"nothing about the slots it did not visit", forbiddenChecked, wantForbidden)
	}
	t.Logf("%d pin rows verified present, measured and attributed across %d machine classes; "+
		"%d forbidden slots verified absent", checked, len(required), forbiddenChecked)
}

// TestEveryClassesIDRemainderPinsAreComplete does for idRemainderPins what the
// test above does for pinTable.
//
// A SEPARATE TABLE NEEDS A SEPARATE COMPLETENESS CHECK, or it silently acquires
// the property the main table was gated against: a class whose tail is unpinned
// looks identical, from the gate, to a class whose tail was measured and found
// fine. Both would report "no id-remainder pin" only on hardware of that class.
func TestEveryClassesIDRemainderPinsAreComplete(t *testing.T) {
	required := map[string][]string{
		ClassARM64:         {TierNEON, TierReference},
		ClassAMD64AVX512:   {TierAVX512, TierAVX2, TierReference},
		ClassAMD64NoAVX512: {TierAVX2, TierReference},
	}

	checked := 0
	for class, tiers := range required {
		for _, tier := range tiers {
			p, ok := idRemainderPinFor(class, tier)
			if !ok {
				t.Errorf("no id-remainder pin for %s/%s — the per-row tail is unguarded on that "+
					"machine class", class, tier)
				continue
			}
			if p.Dim != idRemainderDim {
				t.Errorf("id-remainder pin %s/%s is at dim %d, but the harness measures at %d; "+
					"the pin and the measurement are describing different work", class, tier, p.Dim, idRemainderDim)
			}
			if p.TraverseNsPerDistance <= 0 || p.Tolerance <= 0 || p.Machine == "" {
				t.Errorf("id-remainder pin %s/%s is incomplete: %+v", class, tier, p)
			}
			checked++
		}
	}

	if want := 2 + 3 + 2; checked != want {
		t.Fatalf("checked %d id-remainder rows, expected %d", checked, want)
	}
	if len(idRemainderPins) != checked {
		t.Errorf("idRemainderPins holds %d rows but only %d are required by any class — an "+
			"unreachable row is a measurement nothing will ever read",
			len(idRemainderPins), checked)
	}
	t.Logf("%d id-remainder rows verified complete across %d machine classes", checked, len(required))
}

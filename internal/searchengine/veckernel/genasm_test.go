// SPDX-License-Identifier: Apache-2.0

package veckernel

import (
	_ "embed"
	"errors"
	"regexp"
	"strings"
	"testing"
)

// genasm_test.go reads the GENERATED ASSEMBLY AS TEXT and asserts properties the
// Go toolchain will not catch and avo does not check.
//
// It is a source-reading test, which is unusual and deliberate. The defect it
// exists for is invisible everywhere else: on an arm64 workstation the amd64
// assembly compiles and vets clean, `go test` never executes it, and avo's own
// "Requires:" annotation is computed from instruction MNEMONICS rather than from
// the registers its allocator chose. A kernel can therefore claim it needs only
// AVX2 while referencing a register that exists only with AVX-512, and nothing
// says so until it faults on a customer's machine.

// genAsmSrc is the committed generated assembly, EMBEDDED rather than read from
// disk.
//
// Embedding is what lets these gates run wherever the test binary runs. They are
// cross-compiled and shipped to a benchmark box that has no checkout, and a
// file-reading version failed there with "no such file or directory" — a gate
// that cannot run on the one machine whose architecture it describes. Embedded,
// the bytes travel with the binary and the check is identical everywhere.
//
//go:embed dot_avx_amd64.s
var genAsmSrc string

// generatedAsm returns the embedded assembly, refusing loudly if it is empty.
func generatedAsm() (string, error) {
	if strings.TrimSpace(genAsmSrc) == "" {
		return "", errEmptyGeneratedAsm
	}
	return genAsmSrc, nil
}

// errEmptyGeneratedAsm means the embed produced nothing, which would make every
// gate in this file pass having examined no instructions.
var errEmptyGeneratedAsm = errors.New(
	"veckernel: the embedded generated assembly is empty; every check in genasm_test.go " +
		"would pass having examined nothing")

// avx2ExtendedRegisters is the whole point: X16-X31, Y16-Y31 and every Z
// register are AVX-512 encodings. An AVX2 tier that names one is not an AVX2
// tier.
var (
	textDirective = regexp.MustCompile(`(?m)^TEXT\s+·(\w+)`)
	vecRegister   = regexp.MustCompile(`\b([XYZ])(\d+)\b`)
)

// TestAVX2KernelsUseNoExtendedRegisters is the standing gate on the accumulator
// counts in avogen/dot.go.
//
// THIS DEFECT HAS ALREADY HAPPENED ONCE. Raising the AVX2 dot to eight
// accumulators put eight accumulators plus eight load temps into the sixteen
// legacy vector registers, leaving the scalar-remainder accumulator to be
// allocated at X16 — an AVX-512-only register — inside a function whose emitted
// Requires line still read "AVX, FMA3, SSE". It would have raised an illegal
// instruction on exactly the AVX2-only parts the tier serves, and it would have
// done so in production rather than in any suite, because no arm64 developer
// machine executes this code and no AVX-512-capable benchmark box would fault on
// it either.
func TestAVX2KernelsUseNoExtendedRegisters(t *testing.T) {
	src, err := generatedAsm()
	if err != nil {
		t.Fatalf("%v", err)
	}

	blocks := splitTextBlocks(src)
	if len(blocks) == 0 {
		t.Fatal("found no TEXT blocks in the generated assembly — this gate parsed something " +
			"it did not understand and would have passed having checked nothing")
	}

	checked := 0
	for name, body := range blocks {
		if !strings.Contains(name, "AVX2") {
			continue
		}
		checked++
		for _, m := range vecRegister.FindAllStringSubmatch(body, -1) {
			idx := 0
			for _, c := range m[2] {
				idx = idx*10 + int(c-'0')
			}
			if m[1] == "Z" || idx > 15 {
				t.Errorf("AVX2 kernel %s references %s, which only exists with AVX-512. "+
					"The tier gates on AVX+AVX2+FMA3 and will raise an illegal instruction on "+
					"any part without AVX-512. Lower the accumulator count in avogen/dot.go "+
					"until the allocator stays inside Y0-Y15.", name, m[0])
			}
		}
	}

	// KNOWN POSITIVE. An AVX2 function this gate never visited is an AVX2
	// function it never checked, and a name-matching typo would make that
	// silent. Both amd64 AVX2 kernels must be seen: the scalar dot and the
	// fused four-row gather.
	if checked != 2 {
		t.Fatalf("scanned %d AVX2 blocks, expected 2 (dotF32AVX2 and dotF32x4AVX2). "+
			"This gate examined a different file than the one it claims to cover; blocks "+
			"found: %v", checked, blockNames(blocks))
	}
	t.Logf("%d AVX2 kernel(s) verified free of AVX-512-only registers", checked)
}

// TestAccumulatorsAreZeroedBeforeAnyLabel encodes the invariant a real bug
// violated, in a form that is checkable WITHOUT amd64 hardware.
//
// THE BUG. Every loop in these kernels exits by jumping FORWARD to the next
// loop's label. Instructions emitted between a loop's backward jump and the
// label it exits to are therefore unreachable on the exit path — a dead zone
// that reads, in the generator, exactly like ordinary sequential code. The
// scalar accumulator's zeroing was moved into that dead zone to free a register;
// the narrow loop's `JL` jumped straight over it, and the scalar tail then
// accumulated into a dirty register. At dim 23 the AVX2 kernel returned -28.125
// where the truth is -21.5625: thirty terms summed instead of twenty-three.
//
// WHY A TEXT CHECK RATHER THAN A TEST. Nothing on an arm64 workstation executes
// this code. The defect was caught by the committed fuzz corpus — but only once
// the binary reached an AMD box, which is late, expensive, and only happens
// because the benchmark procedure insists on running correctness there first. A
// register zeroed after the first label is a mechanical property of the emitted
// text, so it can be caught on any machine, at `go test` speed, before anyone
// boots anything.
//
// THE INVARIANT: in every generated function, every accumulator-zeroing
// instruction appears BEFORE the first label. That is stricter than strictly
// necessary and deliberately so — it is the property that is easy to state, easy
// to check, and true of every correct version of these kernels.
func TestAccumulatorsAreZeroedBeforeAnyLabel(t *testing.T) {
	src, err := generatedAsm()
	if err != nil {
		t.Fatalf("%v", err)
	}

	checked := 0
	for name, body := range splitTextBlocks(src) {
		checked++
		lines := strings.Split(body, "\n")
		seenLabel := ""
		for _, ln := range lines {
			trimmed := strings.TrimSpace(ln)
			if isAsmLabel(trimmed) {
				if seenLabel == "" {
					seenLabel = strings.TrimSuffix(trimmed, ":")
				}
				continue
			}
			if seenLabel != "" && isZeroingInstruction(trimmed) {
				t.Errorf("%s zeroes a register AFTER label %q:\n    %s\n"+
					"Every loop here exits by jumping to the next label, so anything between a "+
					"backward jump and that label is unreachable on the exit path. Zero every "+
					"accumulator in the prologue, before the first label.",
					name, seenLabel, trimmed)
			}
		}
	}

	// KNOWN POSITIVE: all four kernels must have been walked. A block-splitting
	// regex that stopped matching would leave this loop empty and serene.
	if checked != 4 {
		t.Fatalf("walked %d generated function(s), expected 4 (two dots, two gathers)", checked)
	}
	t.Logf("%d generated function(s) verified to zero every accumulator before the first label", checked)
}

// isAsmLabel reports whether a line is a Go-assembly label definition.
func isAsmLabel(s string) bool {
	if !strings.HasSuffix(s, ":") || strings.ContainsAny(s, " \t") {
		return false
	}
	return s != ":" && !strings.HasPrefix(s, "//")
}

// isZeroingInstruction reports whether a line is an xor-with-self, which is how
// both tiers clear an accumulator.
func isZeroingInstruction(s string) bool {
	for _, op := range []string{"VXORPS", "VPXORD"} {
		if !strings.HasPrefix(s, op) {
			continue
		}
		args := strings.Split(strings.TrimSpace(strings.TrimPrefix(s, op)), ",")
		if len(args) != 3 {
			continue
		}
		a, b, c := strings.TrimSpace(args[0]), strings.TrimSpace(args[1]), strings.TrimSpace(args[2])
		if a == b && b == c {
			return true
		}
	}
	return false
}

// TestGeneratedAssemblyDeclaresItsBuildConstraint pins the two tags the
// generated file must carry. Losing either would compile the amd64 assembly into
// an arm64 build, or defeat the veckernel_noasm opt-out for amd64 callers.
func TestGeneratedAssemblyDeclaresItsBuildConstraint(t *testing.T) {
	src, err := generatedAsm()
	if err != nil {
		t.Fatalf("%v", err)
	}
	want := "//go:build amd64 && !veckernel_noasm"
	if !strings.Contains(src, want) {
		t.Errorf("the generated assembly must carry %q; regenerate with the avogen "+
			"ConstraintExpr intact", want)
	}
}

// splitTextBlocks maps each assembly function name to its body text.
func splitTextBlocks(src string) map[string]string {
	locs := textDirective.FindAllStringSubmatchIndex(src, -1)
	out := make(map[string]string, len(locs))
	for i, loc := range locs {
		name := src[loc[2]:loc[3]]
		end := len(src)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		out[name] = src[loc[0]:end]
	}
	return out
}

func blockNames(blocks map[string]string) []string {
	out := make([]string, 0, len(blocks))
	for k := range blocks {
		out = append(out, k)
	}
	return out
}

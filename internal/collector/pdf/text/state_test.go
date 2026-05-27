package text

import (
	"errors"
	"testing"

	internalpdf "github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/internal/pdfcpu"
)

func TestNewState_Defaults(t *testing.T) {
	t.Parallel()
	s := newState()
	if s.horizScale != 1.0 {
		t.Errorf("horizScale: got %v, want 1.0", s.horizScale)
	}
	if !matricesEqual(s.tm, identityMatrix) {
		t.Errorf("tm not identity: %+v", s.tm)
	}
	if !matricesEqual(s.tlm, identityMatrix) {
		t.Errorf("tlm not identity: %+v", s.tlm)
	}
	if !matricesEqual(s.ctm, identityMatrix) {
		t.Errorf("ctm not identity: %+v", s.ctm)
	}
	if s.font != nil || s.fontKey != "" || s.fontSize != 0 {
		t.Errorf("font defaults wrong: %+v / %q / %v", s.font, s.fontKey, s.fontSize)
	}
	if s.charSpacing != 0 || s.wordSpacing != 0 || s.leading != 0 || s.rise != 0 {
		t.Errorf("scalar defaults wrong: c=%v w=%v l=%v r=%v",
			s.charSpacing, s.wordSpacing, s.leading, s.rise)
	}
	if s.renderMode != 0 {
		t.Errorf("renderMode: got %d, want 0", s.renderMode)
	}
}

func TestApplyTf_ResolverHit(t *testing.T) {
	t.Parallel()
	s := newState()
	want := &internalpdf.ResolvedFont{FontResource: &internalpdf.FontResource{Key: "F1", BaseFont: "Helvetica", Subtype: "Type1"}}
	resolver := func(k string) (*internalpdf.ResolvedFont, error) {
		if k != "F1" {
			t.Errorf("resolver called with %q, want F1", k)
		}
		return want, nil
	}
	if err := s.applyTf("F1", 12.0, resolver); err != nil {
		t.Fatalf("applyTf: %v", err)
	}
	if s.fontKey != "F1" || s.fontSize != 12.0 || s.font != want {
		t.Errorf("after applyTf: key=%q size=%v font=%v", s.fontKey, s.fontSize, s.font)
	}
}

func TestApplyTf_ResolverMiss_ClearsFont(t *testing.T) {
	t.Parallel()
	s := newState()
	s.font = &internalpdf.ResolvedFont{FontResource: &internalpdf.FontResource{Key: "F0"}}
	resolver := func(k string) (*internalpdf.ResolvedFont, error) {
		return nil, nil
	}
	if err := s.applyTf("F1", 14.0, resolver); err != nil {
		t.Fatalf("applyTf: %v", err)
	}
	if s.font != nil {
		t.Errorf("font: got %+v, want nil after resolver miss", s.font)
	}
	if s.fontKey != "F1" || s.fontSize != 14.0 {
		t.Errorf("key/size not recorded: %q / %v", s.fontKey, s.fontSize)
	}
}

func TestApplyTf_ResolverError_ClearsFontReturnsErr(t *testing.T) {
	t.Parallel()
	s := newState()
	want := errors.New("dereference failure")
	resolver := func(k string) (*internalpdf.ResolvedFont, error) { return nil, want }
	err := s.applyTf("F1", 10.0, resolver)
	if !errors.Is(err, want) {
		t.Errorf("applyTf err: got %v, want %v", err, want)
	}
	if s.font != nil {
		t.Errorf("font: got %+v, want nil on resolver error", s.font)
	}
}

func TestApplyTf_NilResolver_LeavesFontNil(t *testing.T) {
	t.Parallel()
	s := newState()
	if err := s.applyTf("F1", 12.0, nil); err != nil {
		t.Fatalf("applyTf nil resolver: %v", err)
	}
	if s.font != nil {
		t.Errorf("font: got %+v, want nil", s.font)
	}
}

func TestApplyTc_Tw_Tz_TL_Tr_Ts(t *testing.T) {
	t.Parallel()
	s := newState()
	s.applyTc(0.5)
	s.applyTw(1.5)
	s.applyTz(150) // percent → factor 1.5
	s.applyTL(14)
	s.applyTr(3)
	s.applyTs(2.5)
	if s.charSpacing != 0.5 {
		t.Errorf("charSpacing: %v", s.charSpacing)
	}
	if s.wordSpacing != 1.5 {
		t.Errorf("wordSpacing: %v", s.wordSpacing)
	}
	if s.horizScale != 1.5 {
		t.Errorf("horizScale: %v", s.horizScale)
	}
	if s.leading != 14 {
		t.Errorf("leading: %v", s.leading)
	}
	if s.renderMode != 3 {
		t.Errorf("renderMode: %d", s.renderMode)
	}
	if s.rise != 2.5 {
		t.Errorf("rise: %v", s.rise)
	}
}

func TestApplyTd_TranslatesTLM_AndTM(t *testing.T) {
	t.Parallel()
	s := newState()
	s.applyTd(10, 20)
	want := matrix{a: 1, d: 1, e: 10, f: 20}
	if !matricesEqual(s.tlm, want) || !matricesEqual(s.tm, want) {
		t.Errorf("after Td(10,20): tlm=%+v tm=%+v want %+v", s.tlm, s.tm, want)
	}
	// Td accumulates onto tlm.
	s.applyTd(5, -5)
	want2 := matrix{a: 1, d: 1, e: 15, f: 15}
	if !matricesEqual(s.tlm, want2) || !matricesEqual(s.tm, want2) {
		t.Errorf("after Td(10,20) Td(5,-5): tlm=%+v tm=%+v want %+v", s.tlm, s.tm, want2)
	}
}

func TestApplyTD_SetsLeadingAndTranslates(t *testing.T) {
	t.Parallel()
	s := newState()
	s.applyTD(3, -14)
	if s.leading != 14 {
		t.Errorf("leading: %v, want 14 (negation of ty)", s.leading)
	}
	want := matrix{a: 1, d: 1, e: 3, f: -14}
	if !matricesEqual(s.tm, want) {
		t.Errorf("tm: %+v, want %+v", s.tm, want)
	}
}

func TestApplyTm_AbsoluteSet(t *testing.T) {
	t.Parallel()
	s := newState()
	s.applyTd(99, 99) // pre-existing offset
	s.applyTm(2, 0, 0, 3, 100, 200)
	want := matrix{a: 2, d: 3, e: 100, f: 200}
	if !matricesEqual(s.tm, want) {
		t.Errorf("tm: %+v, want %+v", s.tm, want)
	}
	if !matricesEqual(s.tlm, want) {
		t.Errorf("tlm: %+v, want %+v", s.tlm, want)
	}
}

func TestApplyTStar_DropsByLeading(t *testing.T) {
	t.Parallel()
	s := newState()
	s.applyTL(20)
	s.applyTd(50, 700)
	s.applyTStar()
	want := matrix{a: 1, d: 1, e: 50, f: 680}
	if !matricesEqual(s.tm, want) {
		t.Errorf("after T*: tm=%+v want %+v", s.tm, want)
	}
}

func TestApplyQ_AndBigQ_Roundtrip(t *testing.T) {
	t.Parallel()
	s := newState()
	s.applyCm(2, 0, 0, 2, 100, 200)
	saved := s.ctm
	s.applyQ()
	s.applyCm(0.5, 0, 0, 0.5, -50, -50) // mutate within q/Q
	if matricesEqual(s.ctm, saved) {
		t.Fatal("CTM should differ after cm inside q/Q")
	}
	s.applyBigQ()
	if !matricesEqual(s.ctm, saved) {
		t.Errorf("CTM after Q: %+v, want %+v", s.ctm, saved)
	}
	if len(s.gsStack) != 0 {
		t.Errorf("gsStack length: %d, want 0", len(s.gsStack))
	}
}

func TestApplyBigQ_OnEmptyStack_NoOp_NoPanic(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("applyBigQ on empty stack panicked: %v", r)
		}
	}()
	s := newState()
	prev := s.ctm
	s.applyBigQ() // empty stack — must NOT panic
	if !matricesEqual(s.ctm, prev) {
		t.Errorf("CTM mutated on Q-underflow: %+v vs %+v", s.ctm, prev)
	}
}

func TestApplyCm_Compose(t *testing.T) {
	t.Parallel()
	s := newState()
	s.applyCm(2, 0, 0, 2, 0, 0)     // CTM = scale(2,2)
	s.applyCm(1, 0, 0, 1, 100, 200) // CTM = translate(100,200) * scale(2,2)
	// Pose-multiplication semantics: cm is applied to "the ctm", with
	// operand on the LEFT (operand * oldCTM). After two cms:
	// step1: CTM = S(2,2) * I = S(2,2) = {a:2, d:2}
	// step2: CTM = T(100,200) * S(2,2)
	//        a = 1*2 + 0*0 = 2
	//        b = 1*0 + 0*2 = 0
	//        c = 0*2 + 1*0 = 0
	//        d = 0*0 + 1*2 = 2
	//        e = 100*2 + 200*0 + 0 = 200
	//        f = 100*0 + 200*2 + 0 = 400
	want := matrix{a: 2, d: 2, e: 200, f: 400}
	if !matricesEqual(s.ctm, want) {
		t.Errorf("CTM after two cm: %+v, want %+v", s.ctm, want)
	}
}

func TestResetForBT_PreservesTextStateButResetsCursor(t *testing.T) {
	t.Parallel()
	s := newState()
	s.fontKey = "F1"
	s.fontSize = 12
	s.charSpacing = 1.5
	s.applyTm(2, 0, 0, 2, 100, 200) // non-identity tm
	s.resetForBT()
	if !matricesEqual(s.tm, identityMatrix) || !matricesEqual(s.tlm, identityMatrix) {
		t.Errorf("BT did not reset tm/tlm: tm=%+v tlm=%+v", s.tm, s.tlm)
	}
	if s.fontKey != "F1" || s.fontSize != 12 || s.charSpacing != 1.5 {
		t.Errorf("BT clobbered text state: key=%q size=%v c=%v", s.fontKey, s.fontSize, s.charSpacing)
	}
}

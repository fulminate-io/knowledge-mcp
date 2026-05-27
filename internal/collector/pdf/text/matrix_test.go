package text

import (
	"math"
	"testing"
)

const matrixEpsilon = 1e-9

func matricesEqual(a, b matrix) bool {
	return math.Abs(a.a-b.a) < matrixEpsilon &&
		math.Abs(a.b-b.b) < matrixEpsilon &&
		math.Abs(a.c-b.c) < matrixEpsilon &&
		math.Abs(a.d-b.d) < matrixEpsilon &&
		math.Abs(a.e-b.e) < matrixEpsilon &&
		math.Abs(a.f-b.f) < matrixEpsilon
}

func pointsEqual(x1, y1, x2, y2 float64) bool {
	return math.Abs(x1-x2) < matrixEpsilon && math.Abs(y1-y2) < matrixEpsilon
}

// TestIdentityMatrix verifies the package-level identity is the
// canonical I and behaves as a left/right multiplicative identity.
func TestIdentityMatrix(t *testing.T) {
	t.Parallel()
	want := matrix{a: 1, b: 0, c: 0, d: 1, e: 0, f: 0}
	if !matricesEqual(identityMatrix, want) {
		t.Fatalf("identityMatrix = %+v, want %+v", identityMatrix, want)
	}

	// Identity preserves arbitrary points.
	for _, p := range []struct{ x, y float64 }{
		{0, 0}, {1, 1}, {-3.5, 9.25}, {612, 792}, // page-corner
	} {
		gx, gy := identityMatrix.transformPoint(p.x, p.y)
		if !pointsEqual(gx, gy, p.x, p.y) {
			t.Errorf("identity.transformPoint(%v, %v) = (%v, %v), want (%v, %v)",
				p.x, p.y, gx, gy, p.x, p.y)
		}
	}
}

// TestMul_Identity confirms I * M = M and M * I = M.
func TestMul_Identity(t *testing.T) {
	t.Parallel()
	m := matrix{a: 2, b: 3, c: 4, d: 5, e: 6, f: 7}
	if got := identityMatrix.mul(m); !matricesEqual(got, m) {
		t.Errorf("I * m = %+v, want %+v", got, m)
	}
	if got := m.mul(identityMatrix); !matricesEqual(got, m) {
		t.Errorf("m * I = %+v, want %+v", got, m)
	}
}

// TestMul_TranslationOnly verifies translate-only composition.
// T1 * T2 = T(t1+t2) — translations add.
func TestMul_TranslationOnly(t *testing.T) {
	t.Parallel()
	t1 := identityMatrix.translate(3, 4)
	t2 := identityMatrix.translate(7, 11)
	got := t1.mul(t2)
	want := matrix{a: 1, d: 1, e: 10, f: 15}
	if !matricesEqual(got, want) {
		t.Errorf("T(3,4) * T(7,11) = %+v, want %+v", got, want)
	}
}

// TestMul_ScaleOnly verifies scale-only composition: scales multiply.
func TestMul_ScaleOnly(t *testing.T) {
	t.Parallel()
	s1 := identityMatrix.scale(2, 3)
	s2 := identityMatrix.scale(5, 7)
	got := s1.mul(s2)
	want := matrix{a: 10, d: 21}
	if !matricesEqual(got, want) {
		t.Errorf("S(2,3) * S(5,7) = %+v, want %+v", got, want)
	}
}

// TestMul_TranslateThenScale verifies the spec'd ORDER:
// (T * S) means translate-first-then-scale in PDF post-multiplication.
// Translation contributes only via the rhs e/f terms of the formula.
func TestMul_TranslateThenScale(t *testing.T) {
	t.Parallel()
	tr := identityMatrix.translate(10, 20)
	sc := identityMatrix.scale(2, 3)
	got := tr.mul(sc)
	// Expected: a=2, b=0, c=0, d=3, e=10*2+20*0+0=20, f=10*0+20*3+0=60
	want := matrix{a: 2, d: 3, e: 20, f: 60}
	if !matricesEqual(got, want) {
		t.Errorf("T(10,20) * S(2,3) = %+v, want %+v", got, want)
	}
}

// TestTransformPoint_Translation pins translation alone behavior.
func TestTransformPoint_Translation(t *testing.T) {
	t.Parallel()
	m := identityMatrix.translate(50, 70)
	gx, gy := m.transformPoint(0, 0)
	if !pointsEqual(gx, gy, 50, 70) {
		t.Errorf("T(50,70).transformPoint(0,0) = (%v,%v), want (50,70)", gx, gy)
	}
	gx, gy = m.transformPoint(100, 200)
	if !pointsEqual(gx, gy, 150, 270) {
		t.Errorf("T(50,70).transformPoint(100,200) = (%v,%v), want (150,270)", gx, gy)
	}
}

// TestTransformPoint_Scale pins scale alone behavior.
func TestTransformPoint_Scale(t *testing.T) {
	t.Parallel()
	m := identityMatrix.scale(2, 3)
	gx, gy := m.transformPoint(10, 20)
	if !pointsEqual(gx, gy, 20, 60) {
		t.Errorf("S(2,3).transformPoint(10,20) = (%v,%v), want (20,60)", gx, gy)
	}
}

// TestTransformVec_IgnoresTranslation: directions don't translate.
// A vector (5, 10) under translate(50, 70) stays (5, 10), not (55, 80).
func TestTransformVec_IgnoresTranslation(t *testing.T) {
	t.Parallel()
	m := identityMatrix.translate(50, 70)
	gx, gy := m.transformVec(5, 10)
	if !pointsEqual(gx, gy, 5, 10) {
		t.Errorf("T(50,70).transformVec(5,10) = (%v,%v), want (5,10)", gx, gy)
	}
}

// TestTransformVec_AppliesScale: directions DO scale.
func TestTransformVec_AppliesScale(t *testing.T) {
	t.Parallel()
	m := identityMatrix.scale(2, 3)
	gx, gy := m.transformVec(4, 5)
	if !pointsEqual(gx, gy, 8, 15) {
		t.Errorf("S(2,3).transformVec(4,5) = (%v,%v), want (8,15)", gx, gy)
	}
}

// TestMul_Associativity property test: (A*B)*C == A*(B*C) for arbitrary
// non-degenerate transforms.
func TestMul_Associativity(t *testing.T) {
	t.Parallel()
	a := matrix{a: 2, b: 0.5, c: -0.25, d: 3, e: 5, f: 7}
	b := matrix{a: 1, b: 0, c: 0, d: 1, e: 11, f: 13}
	c := matrix{a: 0.5, b: 0, c: 0, d: 0.5, e: 0, f: 0}

	left := a.mul(b).mul(c)
	right := a.mul(b.mul(c))
	if !matricesEqual(left, right) {
		t.Errorf("associativity: (A*B)*C = %+v, A*(B*C) = %+v", left, right)
	}
}

// TestTranslate_OnNonIdentity exercises translate composition on a
// non-identity matrix (the realistic case during cm operator
// processing).
func TestTranslate_OnNonIdentity(t *testing.T) {
	t.Parallel()
	m := matrix{a: 2, d: 2}.translate(10, 20)
	// Expected: a=2, d=2, e=10*2+0=20, f=20*2+0=40 (no, wait —
	// translate uses m.mul(T): result.e = 0*1+0*0+10 = 10. Let me
	// recompute: starting m = {a:2, b:0, c:0, d:2, e:0, f:0};
	// T = {a:1, b:0, c:0, d:1, e:10, f:20}.
	// result.a = 2*1 + 0*0 = 2; .b = 2*0+0*1 = 0; .c = 0*1+2*0 = 0;
	// .d = 0*0+2*1 = 2; .e = 0*1+0*0+10 = 10; .f = 0*0+0*1+20 = 20.
	want := matrix{a: 2, d: 2, e: 10, f: 20}
	if !matricesEqual(m, want) {
		t.Errorf("S(2,2).translate(10,20) = %+v, want %+v", m, want)
	}
}

// TestScale_OnNonIdentity exercises scale composition on a
// non-identity matrix (operator chaining).
func TestScale_OnNonIdentity(t *testing.T) {
	t.Parallel()
	m := identityMatrix.translate(10, 20).scale(2, 3)
	// translate(10,20) = {a:1, d:1, e:10, f:20}; scale composes
	// post-multiplication: result = T(10,20) * S(2,3) =
	// {a:1*2, b:0, c:0, d:1*3, e:10*2+20*0+0=20, f:10*0+20*3+0=60}.
	want := matrix{a: 2, d: 3, e: 20, f: 60}
	if !matricesEqual(m, want) {
		t.Errorf("T(10,20).scale(2,3) = %+v, want %+v", m, want)
	}
}

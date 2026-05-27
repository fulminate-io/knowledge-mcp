package text

// matrix is the PDF-spec affine transform [a b c d e f], representing
// the 3x3 matrix
//
//	| a  b  0 |
//	| c  d  0 |
//	| e  f  1 |
//
// PDF 32000-1:2008 §8.3.4 specifies that PDF transforms compose
// post-multiplication: a content-stream `cm` operator produces a new
// CTM = currentCTM * cmMatrix (the operator's matrix is multiplied on
// the RIGHT). Coordinates transform as (x, y, 1) * M; the formula
// expands to:
//
//	x' = x*a + y*c + e
//	y' = x*b + y*d + f
//
// Only matrix and (later) state.go in package text consume this type;
// the package keeps it unexported so the public surface stays at
// ExtractRuns / ExtractRunsWithOptions / ExtractOptions.
type matrix struct {
	a, b, c, d, e, f float64
}

// identityMatrix is the no-op transform: x' = x, y' = y. Equivalent
// to the spec's identity matrix `1 0 0 1 0 0`.
var identityMatrix = matrix{a: 1, d: 1}

// mul returns m * other per PDF 32000-1:2008 §8.3.4 Eqn 8.3. Order
// matters: matrices compose post-multiplication, so the operator's
// matrix is the RIGHT-hand operand when updating the CTM.
//
//	M = self * other
//	M.a = self.a*other.a + self.b*other.c
//	M.b = self.a*other.b + self.b*other.d
//	M.c = self.c*other.a + self.d*other.c
//	M.d = self.c*other.b + self.d*other.d
//	M.e = self.e*other.a + self.f*other.c + other.e
//	M.f = self.e*other.b + self.f*other.d + other.f
func (m matrix) mul(other matrix) matrix {
	return matrix{
		a: m.a*other.a + m.b*other.c,
		b: m.a*other.b + m.b*other.d,
		c: m.c*other.a + m.d*other.c,
		d: m.c*other.b + m.d*other.d,
		e: m.e*other.a + m.f*other.c + other.e,
		f: m.e*other.b + m.f*other.d + other.f,
	}
}

// transformPoint maps the position (x, y) through m, returning the
// user-space coordinates. Translation contributes via the e/f terms.
func (m matrix) transformPoint(x, y float64) (float64, float64) {
	return x*m.a + y*m.c + m.e, x*m.b + y*m.d + m.f
}

// transformVec maps the direction (x, y) through m, ignoring the
// translation components (e, f). Used for advance widths and other
// vector quantities that should not be translated by cm/Td deltas.
func (m matrix) transformVec(x, y float64) (float64, float64) {
	return x*m.a + y*m.c, x*m.b + y*m.d
}

// translate returns m post-multiplied by a translation matrix
// (1 0 0 1 tx ty).
func (m matrix) translate(tx, ty float64) matrix {
	return m.mul(matrix{a: 1, d: 1, e: tx, f: ty})
}

// scale returns m post-multiplied by a non-uniform scale matrix
// (sx 0 0 sy 0 0).
func (m matrix) scale(sx, sy float64) matrix {
	return m.mul(matrix{a: sx, d: sy})
}

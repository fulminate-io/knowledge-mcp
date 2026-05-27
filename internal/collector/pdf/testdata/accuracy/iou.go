package accuracy

// Box is an axis-aligned rectangle in any coordinate frame. The
// helpers here are frame-agnostic — callers normalize coordinates
// (top-down vs +y-up) before passing in.
type Box struct {
	X0, Y0, X1, Y1 float64
}

// MeanCoverage returns the mean per-golden-box coverage by the
// best-matching actual box. Asymmetric on purpose (per locked
// decision #8): for each golden box, find the actual box with the
// largest fraction of golden's area covered by the intersection.
// Sum and divide by len(golden).
//
// Returns 0 when len(golden) == 0 (no boxes to score against).
//
// Mirror shape: collector/pdf/layout/pdfminer_xval_test.go:184
// computeMeanIoU. Rationale documented there: pdfminer.six emits
// line-level boxes while we emit paragraph-level Blocks; symmetric
// IoU under-reports agreement when the granularities differ. The
// asymmetric form is the right metric for "how much of golden is
// covered by some actual box" — exactly what the corpus harness
// wants.
func MeanCoverage(actual, golden []Box) float64 {
	if len(golden) == 0 {
		return 0
	}
	var sum float64
	for _, g := range golden {
		gArea := (g.X1 - g.X0) * (g.Y1 - g.Y0)
		if gArea <= 0 {
			continue
		}
		var bestCov float64
		for _, a := range actual {
			ix0 := maxFloat(g.X0, a.X0)
			iy0 := maxFloat(g.Y0, a.Y0)
			ix1 := minFloat(g.X1, a.X1)
			iy1 := minFloat(g.Y1, a.Y1)
			if ix1 <= ix0 || iy1 <= iy0 {
				continue
			}
			cov := (ix1 - ix0) * (iy1 - iy0) / gArea
			if cov > bestCov {
				bestCov = cov
			}
		}
		sum += bestCov
	}
	return sum / float64(len(golden))
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

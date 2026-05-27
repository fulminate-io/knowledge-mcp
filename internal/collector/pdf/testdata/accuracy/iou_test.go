package accuracy

import "testing"

func TestMeanCoverage_EmptyGolden(t *testing.T) {
	t.Parallel()
	got := MeanCoverage([]Box{{0, 0, 10, 10}}, nil)
	if got != 0 {
		t.Errorf("empty golden: got %v want 0", got)
	}
}

func TestMeanCoverage_IdenticalBoxes(t *testing.T) {
	t.Parallel()
	a := []Box{{0, 0, 10, 10}, {20, 20, 30, 30}}
	g := []Box{{0, 0, 10, 10}, {20, 20, 30, 30}}
	got := MeanCoverage(a, g)
	if got != 1.0 {
		t.Errorf("identical: got %v want 1.0", got)
	}
}

func TestMeanCoverage_NonOverlapping(t *testing.T) {
	t.Parallel()
	a := []Box{{0, 0, 10, 10}}
	g := []Box{{100, 100, 110, 110}}
	got := MeanCoverage(a, g)
	if got != 0 {
		t.Errorf("non-overlapping: got %v want 0", got)
	}
}

func TestMeanCoverage_HalfCovered(t *testing.T) {
	t.Parallel()
	// Golden box is 10x10 = 100 area; actual covers left half (5x10
	// = 50 area). Coverage = 50/100 = 0.5.
	a := []Box{{0, 0, 5, 10}}
	g := []Box{{0, 0, 10, 10}}
	got := MeanCoverage(a, g)
	if got != 0.5 {
		t.Errorf("half-covered: got %v want 0.5", got)
	}
}

func TestMeanCoverage_BestActualWins(t *testing.T) {
	t.Parallel()
	// Multiple actuals; the best per-golden coverage should be picked.
	// First actual covers 25% of golden, second covers 75%.
	a := []Box{
		{0, 0, 5, 5},  // 25% of golden's 10x10
		{0, 0, 10, 7}, // 70% of golden's 10x10 (height 7 out of 10)
	}
	g := []Box{{0, 0, 10, 10}}
	got := MeanCoverage(a, g)
	if got != 0.7 {
		t.Errorf("best-actual-wins: got %v want 0.7", got)
	}
}

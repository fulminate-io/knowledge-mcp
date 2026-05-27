package accuracy

import "testing"

func TestWordLevenshtein_EmptyEmpty(t *testing.T) {
	t.Parallel()
	if got := WordLevenshtein(nil, nil); got != 0 {
		t.Errorf("WordLevenshtein(nil,nil): got %d want 0", got)
	}
}

func TestWordLevenshtein_EmptyNonEmpty(t *testing.T) {
	t.Parallel()
	got := WordLevenshtein(nil, []string{"a", "b", "c"})
	if got != 3 {
		t.Errorf("WordLevenshtein(nil, [a,b,c]): got %d want 3", got)
	}
	got = WordLevenshtein([]string{"a", "b", "c"}, nil)
	if got != 3 {
		t.Errorf("WordLevenshtein([a,b,c], nil): got %d want 3", got)
	}
}

func TestWordLevenshtein_SingleSwap(t *testing.T) {
	t.Parallel()
	a := []string{"the", "quick", "brown", "fox"}
	b := []string{"the", "fast", "brown", "fox"}
	got := WordLevenshtein(a, b)
	if got != 1 {
		t.Errorf("single-substitution: got %d want 1", got)
	}
}

func TestWordLevenshtein_Identity(t *testing.T) {
	t.Parallel()
	a := []string{"hello", "world"}
	if got := WordLevenshtein(a, a); got != 0 {
		t.Errorf("identity: got %d want 0", got)
	}
}

func TestWordEditDistanceRatio_EmptyEmpty(t *testing.T) {
	t.Parallel()
	if got := WordEditDistanceRatio(nil, nil); got != 0 {
		t.Errorf("ratio(nil,nil): got %v want 0", got)
	}
}

func TestWordEditDistanceRatio_HalfDiverged(t *testing.T) {
	t.Parallel()
	a := []string{"a", "b", "c", "d"}
	b := []string{"a", "x", "c", "y"}
	got := WordEditDistanceRatio(a, b)
	if got != 0.5 {
		t.Errorf("ratio: got %v want 0.5", got)
	}
}

func TestWordEditDistanceRatio_FullyDiverged(t *testing.T) {
	t.Parallel()
	a := []string{"a", "b"}
	b := []string{"x", "y"}
	got := WordEditDistanceRatio(a, b)
	if got != 1.0 {
		t.Errorf("ratio fully diverged: got %v want 1.0", got)
	}
}

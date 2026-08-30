// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"strings"
	"testing"
)

// TestEmbedShapeAcceptedSet covers the widened admission gate: the accepted
// (dimension, dtype) SET parses, everything outside it still errors with the
// vocabulary, and the default an absent section resolves to has not moved.
//
// THE LAST SUBTEST IS THE ONE THAT PROTECTS EXISTING DEPLOYMENTS. Widening what
// a build ACCEPTS is not the same as changing what an operator RUNS, and a
// graph's embed identity is sticky precisely so no config change triggers a
// corpus-scale re-embed spend. If the default moved with the set, every
// deployment that never touched its config would silently change width.
func TestEmbedShapeAcceptedSet(t *testing.T) {
	const head = "[default]\nprovider = \"anthropic\"\nmodel = \"claude-haiku-5\"\n"

	t.Run("every accepted pair parses", func(t *testing.T) {
		for _, dim := range AcceptedEmbedDimensions {
			for _, dtype := range AcceptedEmbedDtypes {
				body := fmt.Sprintf("%s[embedder]\nprovider = \"voyage\"\ndimension = %d\ndtype = %q\n",
					head, dim, dtype)
				cfg, err := Parse([]byte(body))
				if err != nil {
					t.Fatalf("accepted pair (%d, %s) was refused: %v", dim, dtype, err)
				}
				if cfg.Embedder == nil || cfg.Embedder.Dimension != dim || cfg.Embedder.Dtype != dtype {
					t.Fatalf("accepted pair (%d, %s) parsed to %+v", dim, dtype, cfg.Embedder)
				}
			}
		}
		// The set is not empty and not a singleton — a gate over one value would
		// satisfy the loop above while accepting nothing new.
		if len(AcceptedEmbedDimensions) < 2 || len(AcceptedEmbedDtypes) < 2 {
			t.Fatalf("the accepted set must genuinely be a set: dims=%v dtypes=%v",
				AcceptedEmbedDimensions, AcceptedEmbedDtypes)
		}
	})

	t.Run("off set dimension is refused", func(t *testing.T) {
		// 3072 is a real width other embedders offer, which is what makes it the
		// honest negative: a plausible value this build does not serve.
		body := head + "[embedder]\ndimension = 3072\n"
		if _, err := Parse([]byte(body)); err == nil {
			t.Fatal("dimension 3072 was accepted; the gate is not armed")
		}
	})

	t.Run("off set dtype is refused", func(t *testing.T) {
		body := head + "[embedder]\ndtype = \"int8\"\n"
		if _, err := Parse([]byte(body)); err == nil {
			t.Fatal("dtype int8 was accepted; the gate is not armed")
		}
	})

	t.Run("error names value and vocabulary", func(t *testing.T) {
		_, err := Parse([]byte(head + "[embedder]\ndimension = 3072\n"))
		if err == nil {
			t.Fatal("want a refusal")
		}
		msg := err.Error()
		if !strings.Contains(msg, "3072") {
			t.Errorf("refusal %q does not name the offending value", msg)
		}
		for _, dim := range AcceptedEmbedDimensions {
			if !strings.Contains(msg, fmt.Sprint(dim)) {
				t.Errorf("refusal %q does not list accepted dimension %d", msg, dim)
			}
		}

		_, err = Parse([]byte(head + "[embedder]\ndtype = \"int8\"\n"))
		if err == nil {
			t.Fatal("want a refusal")
		}
		msg = err.Error()
		if !strings.Contains(msg, "int8") {
			t.Errorf("refusal %q does not name the offending value", msg)
		}
		for _, dt := range AcceptedEmbedDtypes {
			if !strings.Contains(msg, dt) {
				t.Errorf("refusal %q does not list accepted dtype %s", msg, dt)
			}
		}
	})

	t.Run("absent section default is unchanged", func(t *testing.T) {
		cfg, err := Parse([]byte(head))
		if err != nil {
			t.Fatalf("a config with no [embedder] section must parse: %v", err)
		}
		got, err := cfg.ResolveEmbedder()
		if err != nil {
			t.Fatalf("ResolveEmbedder: %v", err)
		}
		if got.Dimension != AcceptedEmbedDimension || got.Dtype != AcceptedEmbedDtype {
			t.Fatalf("absent-section default resolved to (%d, %s); want the unchanged (%d, %s) — "+
				"widening the accepted set must not move what an untouched config runs",
				got.Dimension, got.Dtype, AcceptedEmbedDimension, AcceptedEmbedDtype)
		}
	})
}

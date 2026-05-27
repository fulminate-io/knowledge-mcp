// SPDX-License-Identifier: Apache-2.0

package foundation

import "strconv"

// extra.go holds the typed-extra accessor helpers analyzers use to read their
// per-analyzer tuning knobs out of Request.Extra. Centralizing the lookups
// gives every analyzer identical default-fallback semantics and a single place
// to harden validation. The accessors are exported because the analyzers that
// call them now live in sibling family packages that import foundation.

// ExtraFloat reads a float-valued knob from req.Extra. Returns def when the
// key is missing, the value fails to parse as a float64, or valid is non-nil
// and rejects the parsed value. Centralizing the lookup means every analyzer
// gets identical default-fallback semantics and a single place to harden
// validation in the future.
func ExtraFloat(req Request, key string, def float64, valid func(float64) bool) float64 {
	if req.Extra == nil {
		return def
	}
	raw, ok := req.Extra[key]
	if !ok {
		return def
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return def
	}
	if valid != nil && !valid(v) {
		return def
	}
	return v
}

// ExtraString reads a string-valued knob from req.Extra. Returns def when
// the key is missing or the value is empty. This is the sibling of
// ExtraFloat used by analyzers whose knobs are regexes, mode selectors
// (heading/attribute/title/content), or comma-separated lists — values
// that don't parse to a float. Centralizing the lookup gives every
// analyzer the same "empty → default" semantics without each Run()
// re-implementing the nil map check.
func ExtraString(req Request, key, def string) string {
	if req.Extra == nil {
		return def
	}
	if v, ok := req.Extra[key]; ok && v != "" {
		return v
	}
	return def
}

// ExtraInt reads an integer-valued knob from req.Extra. Returns def when
// the key is missing, the value fails to parse as an int, or valid is
// non-nil and rejects the parsed value. Identical fallback semantics to
// ExtraFloat so analyzers with int-valued tuning knobs (max_depth,
// min_members, bucket_count) get the same hardening surface.
func ExtraInt(req Request, key string, def int, valid func(int) bool) int {
	if req.Extra == nil {
		return def
	}
	raw, ok := req.Extra[key]
	if !ok {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	if valid != nil && !valid(v) {
		return def
	}
	return v
}

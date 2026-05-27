// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// LLM clients frequently send numeric / boolean / array params encoded
// differently from the JSON schema's declared type — quoted ints, comma-
// joined arrays, etc. The flex* types defer the rigid type check until
// after the obvious coercions succeed, so the wire shape stays honest
// without forcing every caller through a string workaround.
//
// These mirror the server-side flexInt / flexBool / flexStringSlice at
// cmd/knowledge-server/tools/helpers.go, duplicated rather than shared
// because (a) cmd/knowledge-server cannot import cmd/knowledge/internal
// (wrong direction for the client/server split), (b) extracting to a
// shared subpkg under domains/ widens API surface for stable helpers
// that never change, and (c) the duplication cost is two ~40-line
// copies vs. the cost of a third package that exists only for two
// callers.

// flexInt unmarshals both JSON numbers (5) and JSON strings ("5") into
// an int.
type flexInt int

func (f *flexInt) UnmarshalJSON(data []byte) error {
	var n int
	if err := json.Unmarshal(data, &n); err == nil {
		*f = flexInt(n)
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		n, err := strconv.Atoi(s)
		if err != nil {
			return fmt.Errorf("flexInt: cannot parse %q as int", s)
		}
		*f = flexInt(n)
		return nil
	}
	return fmt.Errorf("flexInt: cannot unmarshal %s", string(data))
}

// flexBool unmarshals both JSON booleans (true) and JSON strings
// ("true", "1", "yes") into a bool.
type flexBool bool

func (f *flexBool) UnmarshalJSON(data []byte) error {
	var b bool
	if err := json.Unmarshal(data, &b); err == nil {
		*f = flexBool(b)
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "true", "1", "yes", "y", "t":
			*f = true
		case "false", "0", "no", "n", "f", "":
			*f = false
		default:
			return fmt.Errorf("flexBool: cannot parse %q as bool", s)
		}
		return nil
	}
	return fmt.Errorf("flexBool: cannot unmarshal %s", string(data))
}

// flexStringSlice unmarshals into a []string and accepts any of:
//   - a JSON array of strings: ["a","b"]
//   - a JSON-encoded array string: "[\"a\",\"b\"]" (LLMs sometimes
//     double-encode)
//   - a comma-separated string: "a,b,c"
//   - a single string: "abc"
type flexStringSlice []string

func (f *flexStringSlice) UnmarshalJSON(data []byte) error {
	var arr []string
	if err := json.Unmarshal(data, &arr); err == nil {
		*f = arr
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("flexStringSlice: cannot unmarshal %s", string(data))
	}
	*f = parseFlexStringSliceString(s)
	return nil
}

// parseFlexStringSliceString extracts a []string from s, handling empty
// strings, JSON-encoded arrays, and comma-separated / single values.
func parseFlexStringSliceString(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if strings.HasPrefix(s, "[") {
		var inner []string
		if err := json.Unmarshal([]byte(s), &inner); err == nil {
			return inner
		}
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// LLM clients frequently send numeric / boolean params encoded
// differently from the JSON schema's declared type — quoted ints, quoted
// bools, etc. The flex* types defer the rigid type check until
// after the obvious coercions succeed, so the wire shape stays honest
// without forcing every caller through a string workaround.
//
// This is the only copy in the tree: the server binary carries no flexInt /
// flexBool of its own, and could not import this package across the
// client/server split in any case.

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

// decodeArgsError renders a caller-facing message for a JSON decode failure on a
// tool's argument payload. It is the single translator every decode site routes
// through, so no site leaks a raw Go decode error.
//
// THE DEFECT IT REPLACES was the raw error verbatim:
//
//	json: cannot unmarshal string into Go struct field thinkArgs.links of type []string
//
// which leaks an internal Go struct name (thinkArgs), never quotes the value the
// caller sent, and never says what to send instead. All three are fixed here: the
// message names the WIRE param (the json tag, which is what the caller typed),
// quotes the offending value read back out of the caller's own payload, and
// states the accepted form.
//
// IT DOES NOT COERCE. A bare string where an array is declared is BAD INPUT and
// errors, naming the value and the vocabulary. Three in-tree UnmarshalJSON
// implementations do wrap a bare string into a one-element slice
// (ast/where_json.go's jsonStringOrArr and the two cloud collectors'
// stringOrSlice); that shape is deliberately NOT copied here, because a param
// silently promoted is a param the caller never learns to send correctly.
//
// A non-type decode failure (malformed JSON, a truncated body) falls through to
// the underlying message, which is already caller-facing.
func decodeArgsError(raw json.RawMessage, err error) string {
	var typeErr *json.UnmarshalTypeError
	if !errors.As(err, &typeErr) || typeErr.Field == "" {
		return err.Error()
	}
	msg := fmt.Sprintf("%s was sent as %s", typeErr.Field, describeJSONKind(typeErr.Value))
	if got := rawFieldValue(raw, typeErr.Field); got != "" {
		msg = fmt.Sprintf("%s was sent as %s (%s)", typeErr.Field, describeJSONKind(typeErr.Value), got)
	}
	return msg + ", but it takes " + describeGoTarget(typeErr.Type.String()) + "."
}

// describeJSONKind renders the encoding/json name of the sent value's kind in
// caller words. The zero case keeps the raw token rather than guessing.
func describeJSONKind(kind string) string {
	switch kind {
	case "string":
		return "a string"
	case "number":
		return "a number"
	case "bool":
		return "a boolean"
	case "array":
		return "an array"
	case "object":
		return "an object"
	}
	return kind
}

// describeGoTarget renders the DECLARED shape a param takes, from the Go type the
// decoder was aiming at. Only the shapes the tool schemas actually declare are
// named; anything else is reported verbatim rather than paraphrased into a claim
// the schema does not make.
func describeGoTarget(goType string) string {
	switch goType {
	case "[]string":
		return `an array of strings — wrap the value in brackets, e.g. ["a", "b"]`
	case "[]int", "[]int64", "[]float64":
		return "an array of numbers — wrap the value in brackets, e.g. [1, 2]"
	case "string":
		return "a string"
	case "int", "int32", "int64", "float64":
		return "a number"
	case "bool":
		return "a boolean"
	case "map[string]string", "map[string]any", "map[string]interface {}":
		return "an object of key/value pairs"
	}
	return goType
}

// rawFieldValue reads one TOP-LEVEL field back out of the caller's payload so the
// refusal can quote what they actually sent. A nested field path (a.b) and an
// unreadable payload both return "" — the message then names the kind without the
// literal, which is still actionable.
func rawFieldValue(raw json.RawMessage, field string) string {
	if strings.Contains(field, ".") {
		return ""
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return ""
	}
	v, ok := top[field]
	if !ok {
		return ""
	}
	const maxQuoted = 80
	s := strings.TrimSpace(string(v))
	if len(s) > maxQuoted {
		s = s[:maxQuoted] + "…"
	}
	return s
}

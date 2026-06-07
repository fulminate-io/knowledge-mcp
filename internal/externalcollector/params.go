// SPDX-License-Identifier: Apache-2.0

package externalcollector

import (
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// ValidateParams checks the incoming collect params against a registered
// collector's declared param_schema, at COLLECT time. It is distinct from
// graphtypecrud.Validate, which validates the SCHEMA shape at REGISTRATION time
// (and takes a *GraphTypeDef, not a params map). This is the client-side
// collect-time gate: it rejects an unknown param key and a missing required
// param so a malformed collect call fails loud before the binary is execed.
//
// The type check is intentionally LOOSE: it confirms a provided value is broadly
// compatible with the declared string/int/bool type but does not reject every
// edge case (e.g. a JSON number decodes to float64, which satisfies "int"). The
// schema is a small {type, required} contract by design — no DSL — so the check
// stays correspondingly minimal.
func ValidateParams(schema map[string]*knowledgev1.ParamSpec, params map[string]any) error {
	// Unknown params: every provided key must be declared in the schema.
	for key := range params {
		if _, ok := schema[key]; !ok {
			return fmt.Errorf("externalcollector: unknown param %q (not in collector param_schema)", key)
		}
	}

	// Missing required params + loose type check on provided values.
	for name, spec := range schema {
		v, present := params[name]
		if !present {
			if spec.GetRequired() {
				return fmt.Errorf("externalcollector: required param %q is missing", name)
			}
			continue
		}
		if err := checkParamType(name, spec.GetType(), v); err != nil {
			return err
		}
	}
	return nil
}

// checkParamType loosely validates that value v is compatible with the declared
// type t. JSON decoding (encoding/json into map[string]any) yields float64 for
// every number, string for strings, and bool for booleans, so "int" accepts a
// float64 and "string"/"bool" accept their native kinds. An empty declared type
// is treated as "no constraint" (the registration validator already rejects an
// empty type, so this is a defensive pass-through).
func checkParamType(name, t string, v any) error {
	switch t {
	case "", "string":
		if t == "" {
			return nil
		}
		if _, ok := v.(string); !ok {
			return fmt.Errorf("externalcollector: param %q must be a string", name)
		}
	case "int":
		// JSON numbers decode to float64; accept that and native ints.
		switch v.(type) {
		case float64, int, int32, int64:
		default:
			return fmt.Errorf("externalcollector: param %q must be an int", name)
		}
	case "bool":
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("externalcollector: param %q must be a bool", name)
		}
	default:
		// An unrecognized declared type slips through registration validation
		// only if the schema was hand-built; do not block the collect on it.
	}
	return nil
}

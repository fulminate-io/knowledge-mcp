// SPDX-License-Identifier: Apache-2.0

package externalcollector

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	_ "embed"

	"github.com/google/jsonschema-go/jsonschema"
)

// contract.go — the COLLECTOR CONTRACT: the input and output schemas a custom
// collector's MCP tool must advertise, and the validation of the result it
// returns.
//
// THE SCHEMAS ARE CHECKED-IN JSON, not Go literals. A collector author writing a
// provider in another language reads contract/*.schema.json and copies it into
// their tool definition; a Go literal would make the contract unreadable to
// exactly the audience it exists for. The in-tree precedent for a checked-in
// JSON artifact embedded into the binary is assets/claude_hooks.json.
//
// THE COMPARATOR READS THE SAME FILES, so the requirement has ONE source. A
// hand-written table of "the fields we require" beside a checked-in schema is
// two sources that drift; here the contract schema IS the requirement, and
// schemaSatisfies walks it.

//go:embed contract/collector_input.schema.json
var inputContractJSON []byte

//go:embed contract/collector_output.schema.json
var outputContractJSON []byte

// InputContractJSON and OutputContractJSON expose the checked-in contract
// schemas verbatim so the docs generator and the tests read the same bytes the
// checks run against rather than a transcription of them.
func InputContractJSON() []byte  { return slices.Clone(inputContractJSON) }
func OutputContractJSON() []byte { return slices.Clone(outputContractJSON) }

// CheckToolSchemas is R2's HARD GATE, run at registration and again at collect:
// the target tool must advertise BOTH an input schema and an output schema, and
// both must satisfy the contract. A missing schema or a mismatch is an error
// naming the tool and the mismatch; there is no admit-and-validate-later path.
//
// advertisedInput and advertisedOutput are the tool's raw schema values as the
// MCP tool listing carries them (an untyped any — the MCP SDK does not validate
// a peer's schemas client-side, so this is ours to do).
func CheckToolSchemas(tool string, advertisedInput, advertisedOutput any) error {
	if advertisedInput == nil {
		return fmt.Errorf(
			"custom collector: tool %q advertises NO input schema; the collector contract requires one (see contract/collector_input.schema.json)", tool)
	}
	if advertisedOutput == nil {
		return fmt.Errorf(
			"custom collector: tool %q advertises NO output schema; the collector contract requires one, including the walk_complete completeness assertion (see contract/collector_output.schema.json)", tool)
	}
	if err := checkAgainstContract(tool, "input", inputContractJSON, advertisedInput); err != nil {
		return err
	}
	return checkAgainstContract(tool, "output", outputContractJSON, advertisedOutput)
}

// checkAgainstContract parses one advertised schema and compares it to the
// contract schema of the same side.
func checkAgainstContract(tool, side string, contractJSON []byte, advertised any) error {
	contract, err := parseSchema(contractJSON)
	if err != nil {
		return fmt.Errorf("custom collector: the checked-in %s contract schema is unreadable: %w", side, err)
	}
	raw, err := json.Marshal(advertised)
	if err != nil {
		return fmt.Errorf("custom collector: tool %q %s schema is not JSON: %w", tool, side, err)
	}
	adv, err := parseSchema(raw)
	if err != nil {
		return fmt.Errorf("custom collector: tool %q %s schema is not a readable JSON Schema: %w", tool, side, err)
	}
	if err := schemaSatisfies(contract, adv, side+"Schema"); err != nil {
		return fmt.Errorf("custom collector: tool %q %s schema does not satisfy the collector contract: %w", tool, side, err)
	}
	return nil
}

// parseSchema decodes JSON Schema bytes into the SDK's schema type. Unknown
// keywords are tolerated (a provider's schema legitimately carries keywords the
// contract does not name); a malformed document is an error.
func parseSchema(b []byte) (*jsonschema.Schema, error) {
	var s jsonschema.Schema
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// schemaSatisfies reports whether an advertised schema declares AT LEAST what
// the contract declares, at every path the contract names: the same type, every
// required key the contract requires, every property the contract declares, and
// the item schema of every array. It says nothing about the keywords the
// contract does not name — a provider is free to be STRICTER (extra properties,
// extra required keys, formats, enums), never looser.
//
// The error names the PATH, the expectation and what was found, so a collector
// author reading the refusal knows which line of their tool definition to fix.
func schemaSatisfies(contract, advertised *jsonschema.Schema, path string) error {
	if contract == nil {
		return nil
	}
	if advertised == nil {
		return fmt.Errorf("%s: the contract declares this and the tool does not", path)
	}
	if contract.Type != "" && advertised.Type != contract.Type {
		got := advertised.Type
		if got == "" {
			got = "no declared type"
		}
		return fmt.Errorf("%s: the contract requires type %q, the tool declares %s", path, contract.Type, got)
	}
	for _, req := range contract.Required {
		if !slices.Contains(advertised.Required, req) {
			return fmt.Errorf("%s: the contract requires %q to be a required property, the tool's required list is %v", path, req, advertised.Required)
		}
	}
	for name, sub := range contract.Properties {
		advSub, ok := advertised.Properties[name]
		if !ok {
			// AN OPTIONAL PROPERTY THE TOOL DOES NOT DECLARE IS ADMITTED, and the
			// discriminator is the CONTRACT'S OWN required list rather than the
			// advertised one. A property the contract requires is still refused when
			// its declaration is missing, even where the tool lists the key as
			// required — listing a key and declaring nothing for it is exactly the
			// shape this loop exists to catch.
			//
			// THE REASON IS COMPATIBILITY WITH PROVIDERS THAT PREDATE A PROPERTY.
			// The contract grows optional properties over time (the declared
			// foreign-graph context is the first), and under the old rule every such
			// addition refused every existing third-party provider at once, naming a
			// property its author had never heard of. A provider lacking an optional
			// property is admitted and simply receives nothing for it.
			//
			// IT SKIPS AN ABSENT PROPERTY, NEVER A PRESENT ONE: a tool that DOES
			// declare an optional property still has it compared below, so declaring
			// `context` as a string is refused rather than waved through.
			if !slices.Contains(contract.Required, name) {
				continue
			}
			return fmt.Errorf("%s: the contract declares the property %q and the tool does not", path, name)
		}
		if err := schemaSatisfies(sub, advSub, path+"."+name); err != nil {
			return err
		}
	}
	if contract.Items != nil {
		if advertised.Items == nil {
			return fmt.Errorf("%s: the contract declares an item schema and the tool does not", path)
		}
		if err := schemaSatisfies(contract.Items, advertised.Items, path+"[]"); err != nil {
			return err
		}
	}
	return nil
}

// DecodeResult validates a tool call's structuredContent against the CONTRACT
// output schema and decodes it into the envelope.
//
// THERE IS NO SIZE GATE HERE. This function used to refuse a result over 64
// MiB, and that bound is gone with the rest of the collector-traffic caps
// (mcphost.go states why). What remains are two gates, and they answer different
// questions. The schema validation asks
// whether the payload is a conforming collector result at all; the strict decode
// asks whether every field it carries is one this client knows how to ship. A
// key the envelope does not name is an ERROR rather than a silent drop, because
// a typo'd field name silently dropped is a collector author debugging an empty
// graph with no message to go on.
func DecodeResult(tool string, structured any) (*Result, error) {
	if structured == nil {
		return nil, fmt.Errorf(
			"custom collector: tool %q returned no structuredContent; the contract requires a structured result matching its advertised output schema", tool)
	}
	raw, err := json.Marshal(structured)
	if err != nil {
		return nil, fmt.Errorf("custom collector: tool %q result is not JSON: %w", tool, err)
	}
	if err := ValidateResultPayload(tool, raw); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var r Result
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf(
			"custom collector: tool %q result carries a field this collector contract does not define: %w", tool, err)
	}
	return &r, nil
}

// ValidateResultPayload validates raw result JSON against the checked-in
// contract output schema. Split from DecodeResult so a caller holding bytes (the
// contract test, a future replay path) validates through the same instrument.
func ValidateResultPayload(tool string, raw []byte) error {
	contract, err := parseSchema(outputContractJSON)
	if err != nil {
		return fmt.Errorf("custom collector: the checked-in output contract schema is unreadable: %w", err)
	}
	resolved, err := contract.Resolve(nil)
	if err != nil {
		return fmt.Errorf("custom collector: the checked-in output contract schema does not resolve: %w", err)
	}
	var instance any
	if err := json.Unmarshal(raw, &instance); err != nil {
		return fmt.Errorf("custom collector: tool %q result is not JSON: %w", tool, err)
	}
	if err := resolved.Validate(instance); err != nil {
		return fmt.Errorf(
			"custom collector: tool %q result does not satisfy the collector contract's output schema: %w", tool, err)
	}
	return nil
}

// ValidateAgainstAdvertised validates the collect arguments (or a result)
// against a schema the PROVIDER advertised, rather than against the contract.
// The two are different assertions: the contract says what a collector result
// must always look like, and this says whether the provider kept its own word.
//
// label names the side in the error so a refusal is readable without the caller
// wrapping it again.
func ValidateAgainstAdvertised(tool, label string, advertised any, instance any) error {
	raw, err := json.Marshal(advertised)
	if err != nil {
		return fmt.Errorf("custom collector: tool %q %s schema is not JSON: %w", tool, label, err)
	}
	schema, err := parseSchema(raw)
	if err != nil {
		return fmt.Errorf("custom collector: tool %q %s schema is not a readable JSON Schema: %w", tool, label, err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return fmt.Errorf("custom collector: tool %q %s schema does not resolve: %w", tool, label, err)
	}
	// Round-trip the instance through JSON so the validator sees the same plain
	// any-shaped document the wire carries, never a Go struct it cannot walk.
	buf, err := json.Marshal(instance)
	if err != nil {
		return fmt.Errorf("custom collector: tool %q %s value is not JSON: %w", tool, label, err)
	}
	var doc any
	if err := json.Unmarshal(buf, &doc); err != nil {
		return fmt.Errorf("custom collector: tool %q %s value is not JSON: %w", tool, label, err)
	}
	if err := resolved.Validate(doc); err != nil {
		return fmt.Errorf("custom collector: tool %q %s does not satisfy the schema the provider itself advertised: %w",
			tool, label, err)
	}
	return nil
}

// ContractSummary renders the contract's required keys as a one-line summary for
// the registration tool's description and its success text, so an operator sees
// what their provider owes without opening the schema file.
func ContractSummary() string {
	return strings.Join([]string{
		"input: id (string, required) + params (object) + context (object, declared foreign-graph slice)",
		"output: nodes[] + edges[] + walk_complete (all required)",
	}, "; ")
}

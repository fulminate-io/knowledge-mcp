// SPDX-License-Identifier: Apache-2.0

package tools

// query_param_accounting.go is the query surface's gate wrapper: the one
// function every query claim point calls before it serves a call. It makes it
// structurally impossible for a query arm to accept a caller-supplied param and
// return success while dropping it.
//
// TWO CHECKS, IN ORDER, and the order is the behavior rather than an
// implementation detail — the same arrangement accountMutateParams documents.
//
//  1. The per-arm CLASSIFICATION check (accountParams over queryArmRegistry): an
//     error when the caller supplied any param THIS arm classifies as rejected,
//     naming the field and the handler that does not route it. It runs FIRST so a
//     DECLARED-but-unrouted param keeps its specific, actionable message instead
//     of falling to the generic unknown-key form.
//
//  2. The UNKNOWN-TOP-LEVEL-KEY sweep: a supplied key QueryToolDef does not
//     declare AT ALL — a typo, or a param that belongs to another tool. No arm's
//     rejected set can catch one, because every arm's sets are authored from
//     schema keys only, so a key with no cell can never be classified rejected.
//     The sweep keys on the SCHEMA rather than on the arm, which is exactly what
//     makes it one check here instead of a rejection cell on each of the 47 arms:
//     a key the schema does not declare is unknown for every one of them.
//
// WHY THIS FILE EXISTS AT ALL — a disclosed deviation from the plan. The plan's
// wiring step specifies the bare accountParams(queryArmRegistry, "query", …)
// call at each claim point, which routes only DECLARED params per arm and leaves
// query with no equivalent of the unknown-key sweep mutate's wrapper carries. No
// step assigned that sweep to anyone. Rather than fork a second accounting
// mechanism, query gets the same wrapper shape mutate has, calling the same two
// shared primitives; the claim points call accountQueryParams instead of
// accountParams directly, and the bijection between armIDs and gate call sites
// is unchanged.
//
// ONE SWEEP SERVES BOTH TOOLS THAT REACH THE FILE_SYMBOLS ARM. InterceptFileSymbols
// claims the standalone `file_symbols` tool as well as query(mode:file_symbols),
// so its gate call sweeps a file_symbols payload against the QUERY schema. That
// is safe rather than lucky: FileSymbolsToolDef declares file_path, file_paths,
// include_source, include_tombstones, format and repo, all six of which
// QueryToolDef also declares, so no file_symbols param can be reported unknown.
// A param added to the file_symbols schema and NOT to query's would break that
// containment — hence TestQuerySweepCoversFileSymbolsSchema, which asserts it.
//
// SCOPE IS TOP-LEVEL ONLY. The `extra` and `meta` map CONTENTS stay flex-open by
// design — the sweep reads suppliedMutateParams' key set, which never descends
// into nested objects, so nested keys are structurally out of reach here rather
// than filtered out.

import (
	"encoding/json"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// queryProperties is the live query schema's declared param set — the single
// source both halves of the unknown-key check read.
func queryProperties() map[string]kgtools.Property {
	return QueryToolDef().InputSchema.Properties
}

// accountQueryParams is the pre-read gate every query claim point runs before
// serving. A non-nil error means the call must terminate with that message and
// issue NO read.
//
// Fail-closed on a missing carrier: a nil raw payload means accounting never
// ran, and passing everything through in that state would be the exact silent
// hole this gate exists to close.
func accountQueryParams(arm armID, raw json.RawMessage) error {
	if raw == nil {
		return fmt.Errorf("query: param accounting not initialized for arm %s", arm)
	}
	if err := accountParams(queryArmRegistry, "query", arm, raw); err != nil {
		return err
	}
	schema := queryProperties()
	if unknown := unknownTopLevelParams(suppliedMutateParams(raw), schema); len(unknown) > 0 {
		return fmt.Errorf(
			"query: unknown parameter %q. Valid top-level parameters: %s",
			unknown[0], validTopLevelParams(schema),
		)
	}
	return nil
}

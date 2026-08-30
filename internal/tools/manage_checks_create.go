// SPDX-License-Identifier: Apache-2.0

// manage_checks_create.go — authoring a check and both of its fixtures in ONE
// validated call.
//
// ORDER IS THE WHOLE DESIGN: VALIDATE BEFORE ANY WRITE. The pre-existing write
// gate resolves fixtures by FETCHING them from the graph, which forces an author
// to create two example nodes, copy their ids into the check's metadata, and
// create the check — with the admission gate running only on that third call, so
// a fixture problem is discovered after two nodes are already in the graph. This
// operation does not need that sequence: corpus.ValidateFixtures takes fixture
// VALUES, an id and a content string, and its own doc sanctions being handed a
// Check that never passed through ParseCheck. So the whole admission decision is
// made in memory, and nothing is written until it has passed.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/corpus"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/validate"
)

// The placeholder fixture ids the in-memory validation runs under.
//
// THEY ARE READABLE ON PURPOSE. corpus.ValidateFixtures quotes the fixture id in
// every refusal it produces ("the check is SILENT on its bad example %q"), and at
// validation time the real nodes do not exist yet — so the token has to name the
// PARAM the author supplied rather than an id they could not act on. They differ
// from each other because the contract requires the two bindings to differ.
const (
	pendingFixtureBad  = "the supplied fixture_bad"
	pendingFixtureGood = "the supplied fixture_good"
)

// manageChecksCreate authors a check and both fixtures.
//
// CRASH WINDOW, stated rather than papered over. Nothing is deleted or
// superseded, so there is no destroy-before-persist hazard. The one exposure is
// the inverse: the fixtures land and the check write then fails, leaving two
// orphaned example nodes. By that point validation has already passed, so the
// only remaining failure is a transport or store error — and the answer is to
// FAIL LOUDLY naming both fixture ids so a human can bind or delete them. There
// is deliberately no rollback lane and no best-effort cleanup: a silent
// compensator here is a fallback, and the list operation already surfaces
// fixtures bound by no check, which is the recovery path.
func manageChecksCreate(ctx context.Context, gc GraphCaller, a manageChecksArgs) kgtools.ToolResult {
	if err := validateCreateBodies(a); err != nil {
		return errorResult(err.Error())
	}
	candidate, err := parseCandidateCheck(a)
	if err != nil {
		return errorResult(err.Error())
	}
	// THE ADMISSION GATE, run in memory over the fixture bodies the caller
	// supplied. Its error is relayed VERBATIM: the message is payload — it
	// carries the per-fixture match counts a reader needs — and its three
	// sentinels are classified with errors.Is by consumers, never by wording.
	if err := corpus.ValidateFixtures(ctx, candidate,
		corpus.Fixture{ID: pendingFixtureBad, Content: a.FixtureBad.Content},
		corpus.Fixture{ID: pendingFixtureGood, Content: a.FixtureGood.Content},
	); err != nil {
		return errorResult("manage_checks create: the check was not admitted, so nothing was written: " + err.Error())
	}

	badID, goodID, err := writeCheckFixtures(ctx, gc, a)
	if err != nil {
		return errorResult("manage_checks create: " + err.Error())
	}
	checkID, err := writeCheckNode(ctx, gc, a, badID, goodID)
	if err != nil {
		return errorResult(fmt.Sprintf(
			"manage_checks create: the fixtures were written but the check was not: %v. "+
				"Fixture nodes %s and %s are now ORPHANED in the %s graph — bind them to a check or delete them. "+
				"They are listed under %q by manage_checks(operation:%q)",
			err, badID, goodID, kgtypes.GraphChecks, laneUnboundFixt, OpChecksList))
	}
	return textResult(fmt.Sprintf(
		"check %s created in the %s graph, admitted by its own fixtures\n  %s=%s\n  %s=%s",
		checkID, kgtypes.GraphChecks, corpus.MetaFixtureBad, badID, corpus.MetaFixtureGood, goodID))
}

// validateCreateBodies runs the SHARED client-side validators over all three node
// bodies before the first write.
//
// IT IS NOT A SECOND GATE. The summary rule is the engine's, keyed on the node
// type's own eligibility, and the server enforces it as the last line of defense;
// this calls the same client-side validator its eleven other callers use, for the
// one property a per-node server refusal cannot give this operation — a refusal
// BEFORE the first of three sequenced writes. Without it a summaryless CHECK
// would be refused only after both fixtures had already landed.
func validateCreateBodies(a manageChecksArgs) error {
	const tool = "manage_checks create"
	if err := validate.Name(tool, a.Name); err != nil {
		return err
	}
	if err := validate.Summary(tool, "summary", a.Summary); err != nil {
		return err
	}
	for _, f := range []struct {
		field string
		body  *manageChecksFixtureArgs
	}{
		{corpus.MetaFixtureBad, a.FixtureBad},
		{corpus.MetaFixtureGood, a.FixtureGood},
	} {
		if f.body == nil {
			return fmt.Errorf("%s: %s is required — there is no fixture-exempt check type", tool, f.field)
		}
		if err := validate.Name(tool, f.body.Name); err != nil {
			return fmt.Errorf("%s.%s: %w", tool, f.field, err)
		}
		if err := validate.Summary(tool, f.field+".summary", f.body.Summary); err != nil {
			return err
		}
		if strings.TrimSpace(f.body.Content) == "" {
			return fmt.Errorf("%s: %s.content is required — the fixture body is what the check is run against", tool, f.field)
		}
	}
	return nil
}

// parseCandidateCheck builds the metadata map the contract reads and runs it
// through corpus.ParseCheck.
//
// IT REUSES THE ONE ADMISSION PARSER rather than re-spelling the vocabulary: the
// check family's rule is that every consumer decodes through ParseCheck, so the
// check_type / severity / language vocabularies and the ast body's parse-and-
// compile proof all come from the contract and none of them is restated here.
func parseCandidateCheck(a manageChecksArgs) (corpus.Check, error) {
	md := map[string]string{
		corpus.MetaCheckType:   a.CheckType,
		corpus.MetaSeverity:    a.Severity,
		corpus.MetaLanguage:    a.Language,
		corpus.MetaDSLPattern:  a.DSLPattern,
		corpus.MetaFixtureBad:  pendingFixtureBad,
		corpus.MetaFixtureGood: pendingFixtureGood,
	}
	if a.CheckWhere != "" {
		md[corpus.MetaCheckWhere] = a.CheckWhere
	}
	c, isCheck, err := corpus.ParseCheck(&knowledgev1.Node{
		Id:         "the check being created",
		Type:       string(kgtypes.NodeFinding),
		SymbolName: a.Name,
		Metadata:   md,
	})
	if err != nil {
		return corpus.Check{}, fmt.Errorf("manage_checks create: %w", err)
	}
	if !isCheck {
		// Reachable only with an absent check_type, which the contract treats as
		// "not a check" rather than as an error — for this operation it is one.
		return corpus.Check{}, fmt.Errorf("manage_checks create: %s is required — a node without it is not a check",
			corpus.MetaCheckType)
	}
	return c, nil
}

// checksCreateArgs is the wire envelope for a create_batch into the checks graph.
// It is this operation's own struct rather than PersistBatch's, because
// PersistBatch carries no graph selector and every node here belongs to the
// checks graph rather than to knowledge.
type checksCreateArgs struct {
	Operation string             `json:"operation"`
	Graph     string             `json:"graph"`
	Nodes     []persistBatchNode `json:"nodes"`
	Edges     []persistBatchEdge `json:"edges"`
}

// writeCheckFixtures writes both example nodes in ONE atomic create_batch, so a
// half-written fixture pair is not a state this operation can produce.
func writeCheckFixtures(ctx context.Context, gc GraphCaller, a manageChecksArgs) (badID, goodID string, err error) {
	ids, err := executeChecksCreate(ctx, gc, checksCreateArgs{
		Operation: "create_batch",
		Graph:     string(kgtypes.GraphChecks),
		Nodes: []persistBatchNode{
			fixtureNodeBody(a.FixtureBad, a.Language),
			fixtureNodeBody(a.FixtureGood, a.Language),
		},
	})
	if err != nil {
		return "", "", fmt.Errorf("write the fixtures: %w", err)
	}
	if len(ids) != 2 {
		return "", "", fmt.Errorf("write the fixtures: the batch returned %d id(s) for 2 fixture nodes", len(ids))
	}
	return ids[0], ids[1], nil
}

// fixtureNodeBody projects one caller-supplied fixture onto a node body.
//
// THE LANGUAGE LABEL IS NOT DECORATION. The corpus read narrows BOTH node types
// by a language metadata predicate, so an unlabeled fixture is invisible to
// every scan and its own check fails to resolve it.
func fixtureNodeBody(f *manageChecksFixtureArgs, language string) persistBatchNode {
	return persistBatchNode{
		Type:        string(kgtypes.NodeExample),
		Name:        f.Name,
		Description: f.Description,
		Summary:     f.Summary,
		Content:     f.Content,
		Metadata:    map[string]string{corpus.MetaLanguage: language},
	}
}

// writeCheckNode writes the check and BOTH display-only fixture edges in one
// atomic create_batch, so a check can never exist carrying only one of them.
//
// EDGE DIRECTION, and it is the locked one:
//
//	check --avoid-when--> check_fixture_bad   (the shape the check fires on is the one to avoid)
//	check --applies-when--> check_fixture_good (the conforming near-miss)
//
// The edges stay DISPLAY-ONLY. The fixture binding is metadata and only
// metadata; no executor consults these edges and none may start to.
func writeCheckNode(ctx context.Context, gc GraphCaller, a manageChecksArgs, badID, goodID string) (string, error) {
	md := map[string]string{
		corpus.MetaCheckType:   a.CheckType,
		corpus.MetaSeverity:    a.Severity,
		corpus.MetaLanguage:    a.Language,
		corpus.MetaDSLPattern:  a.DSLPattern,
		corpus.MetaFixtureBad:  badID,
		corpus.MetaFixtureGood: goodID,
	}
	if a.CheckWhere != "" {
		md[corpus.MetaCheckWhere] = a.CheckWhere
	}
	ids, err := executeChecksCreate(ctx, gc, checksCreateArgs{
		Operation: "create_batch",
		Graph:     string(kgtypes.GraphChecks),
		Nodes: []persistBatchNode{{
			Type:        string(kgtypes.NodeFinding),
			Name:        a.Name,
			Description: a.Description,
			Summary:     a.Summary,
			Content:     a.Content,
			Metadata:    md,
		}},
		Edges: []persistBatchEdge{
			{FromIdx: 0, ToIdx: -1, ToID: badID, Type: string(kgtypes.EdgeAvoidWhen)},
			{FromIdx: 0, ToIdx: -1, ToID: goodID, Type: string(kgtypes.EdgeAppliesWhen)},
		},
	})
	if err != nil {
		return "", err
	}
	if len(ids) != 1 {
		return "", fmt.Errorf("the batch returned %d id(s) for 1 check node", len(ids))
	}
	return ids[0], nil
}

// executeChecksCreate marshals one checks-graph create_batch and runs it through
// the same engine-lowering seam every other client write uses.
func executeChecksCreate(ctx context.Context, gc GraphCaller, args checksCreateArgs) ([]string, error) {
	body, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	resp, err := executeMutate(ctx, gc, body)
	if err != nil {
		return nil, err
	}
	return resp.GetIds(), nil
}

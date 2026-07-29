// SPDX-License-Identifier: Apache-2.0

package transcripts

import (
	"bytes"
	"testing"

	"github.com/parquet-go/parquet-go"
)

// reducedRow is a DELIBERATELY-narrower mirror of parquetRow that OMITS the
// enrichment tail — most importantly the is_meta column. It stands in for a
// pre-enrichment parquet file written before the is_meta column existed: the
// physical schema on disk simply has no is_meta leaf.
type reducedRow struct {
	Source       string `parquet:"source"`
	SessionID    string `parquet:"session_id"`
	Model        string `parquet:"model"`
	RecordTS     string `parquet:"record_ts"`
	InputTokens  int64  `parquet:"input_tokens"`
	OutputTokens int64  `parquet:"output_tokens"`
	ToolName     string `parquet:"tool_name"`
}

// TestParquetDrift_MissingIsMetaZeroFills is the Phase-0 SPIKE. It resolves
// the one open architectural risk before any reimplementation: does parquet-go
// zero-fill a struct field that is ABSENT from a file's physical schema, or does it
// error?
//
// The CEO is_meta directive (locked): a row from a pre-enrichment parquet file that
// LACKS the is_meta column must read is_meta=false and be KEPT (missing→false→keep),
// the OPPOSITE of the old duckdb `NOT NULL` = NULL exclusion. That fix is reproducible
// in pure-Go ONLY IF parquet-go zero-fills the absent field rather than erroring.
//
// This test writes a parquet from the reduced struct (no is_meta leaf) and reads it
// back into the FULL parquetRow via parquet.Read[parquetRow] (the exact call shape
// proven at parquet_test.go:114). It asserts (a) NO error and (b) the absent is_meta
// decodes to the Go zero-value false.
//
// CONTINGENCY: if parquet.Read ERRORS on the missing column instead of zero-filling,
// Phase 0.2's ReadSessionParquet must add drift tolerance (a schema-union / per-column
// presence read, as the old duckdb read_parquet(..., union_by_name=true) did) and the
// outcome must be surfaced to the orchestrator before Phase 3. As of this spike, the
// zero-fill path holds and no drift-tolerance contingency is needed.
func TestParquetDrift_MissingIsMetaZeroFills(t *testing.T) {
	var buf bytes.Buffer
	w := parquet.NewGenericWriter[reducedRow](&buf, parquet.Compression(&parquet.Zstd))
	if _, err := w.Write([]reducedRow{
		{Source: "claude", SessionID: "s1", Model: "m", RecordTS: "2026-06-01T10:00:00Z", InputTokens: 10, ToolName: "Bash"},
		{Source: "claude", SessionID: "s1", Model: "m", RecordTS: "2026-06-01T10:00:05Z", OutputTokens: 5, ToolName: "Read"},
	}); err != nil {
		t.Fatalf("write reduced parquet: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close reduced parquet writer: %v", err)
	}

	// Read the is_meta-less file into the FULL parquetRow schema.
	got, err := parquet.Read[parquetRow](bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("parquet.Read into full parquetRow errored on a file MISSING is_meta: %v\n"+
			"CONTINGENCY TRIGGERED: parquet-go does NOT zero-fill an absent column — "+
			"ReadSessionParquet (Phase 0.2) must add drift tolerance; surface to orchestrator before Phase 3", err)
	}
	if len(got) != 2 {
		t.Fatalf("read %d rows, want 2", len(got))
	}
	for i, r := range got {
		if r.IsMeta {
			t.Errorf("row %d: is_meta = true, want false (absent column must zero-fill to false → row KEPT)", i)
		}
	}
	// The present columns still decode correctly.
	if got[0].Model != "m" || got[0].InputTokens != 10 || got[0].ToolName != "Bash" {
		t.Errorf("row 0 present columns mis-decoded: %+v", got[0])
	}
}

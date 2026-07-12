// SPDX-License-Identifier: Apache-2.0

package transcriptanalytics

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// detectorFixtureColumns is the varied column tuple the fixture VALUES body fills; the
// remaining schema columns are appended as typed defaults by the projection below. This
// fixture is the SHARED cross-repo contract: the cloud QueryInsights parity test writes
// a byte-identical fixture so the two engines' numbers must match.
const detectorFixtureColumns = "(model, session_id, record_ts, input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens, tool_name, duration_ms, is_sidechain, agent_id, subagent_type, tool_input_hash, tool_input_preview, cache_creation_1h_tokens, cache_creation_5m_tokens, stop_reason, is_api_error, is_meta, interrupted)"

// detectorFixtureProjection casts the numeric columns to BIGINT (matching the client
// parquet writer) and appends the string/bool schema columns not varied by the fixture
// as empty/zero defaults, so read_parquet resolves the full column set by name.
const detectorFixtureProjection = `SELECT
	model, session_id, record_ts,
	CAST(input_tokens AS BIGINT) AS input_tokens,
	CAST(output_tokens AS BIGINT) AS output_tokens,
	CAST(cache_read_tokens AS BIGINT) AS cache_read_tokens,
	CAST(cache_creation_tokens AS BIGINT) AS cache_creation_tokens,
	tool_name,
	CAST(duration_ms AS BIGINT) AS duration_ms,
	is_sidechain, agent_id, subagent_type, tool_input_hash, tool_input_preview,
	CAST(cache_creation_1h_tokens AS BIGINT) AS cache_creation_1h_tokens,
	CAST(cache_creation_5m_tokens AS BIGINT) AS cache_creation_5m_tokens,
	stop_reason, is_api_error, is_meta, interrupted,
	'' AS source, '' AS project, '' AS git_branch, '' AS record_type, false AS is_error,
	'' AS cli_version, '' AS uuid, '' AS parent_uuid, '' AS tool_use_id, '' AS service_tier,
	CAST(0 AS BIGINT) AS web_search_count, CAST(0 AS BIGINT) AS web_fetch_count,
	'' AS mcp_server, '' AS mcp_tool, '' AS skill`

// detectorFixtureRows exercises EVERY detector family in one session (S1):
//   - dup Bash/h1 x2 (rows 1,2)          → duplicate command, wasted=2000ms
//   - Read/h2 (row 3, 3000ms)            → tool latency + time-total
//   - token row (row 4)                  → cache-efficiency (read 800/input, 1h=40 5m=60)
//   - max_tokens truncation (row 5)      → waste (out=4000, dur=5000)
//   - api-error row (row 6)              → waste
//   - interrupted Bash/h3 (row 7)        → idle-EXCLUDED from time, counted in waste
//   - is_meta row (row 8)                → MUST be excluded from ALL totals
//   - <synthetic> row (row 9)            → MUST be excluded from ALL totals
//   - agent-1 researcher span (rows 10,11) wall=120000ms in=500 out=250
//   - agent-2 planner span (rows 12,13)  wall=60000ms  in=200 out=100
const detectorFixtureRows = `
	('claude-sonnet-4','S1','2026-06-01T10:00:00Z',0,0,0,0,'Bash',1000,false,'','','h1','ls',0,0,'',false,false,false),
	('claude-sonnet-4','S1','2026-06-01T10:00:05Z',0,0,0,0,'Bash',1000,false,'','','h1','ls',0,0,'',false,false,false),
	('claude-sonnet-4','S1','2026-06-01T10:00:10Z',0,0,0,0,'Read',3000,false,'','','h2','read a',0,0,'',false,false,false),
	('claude-sonnet-4','S1','2026-06-01T10:00:20Z',1000,500,800,100,'',0,false,'','','','',40,60,'end_turn',false,false,false),
	('claude-sonnet-4','S1','2026-06-01T10:00:30Z',200,4000,0,0,'',5000,false,'','','','',0,0,'max_tokens',false,false,false),
	('claude-sonnet-4','S1','2026-06-01T10:00:40Z',100,0,0,0,'',0,false,'','','','',0,0,'',true,false,false),
	('claude-sonnet-4','S1','2026-06-01T10:00:50Z',0,0,0,0,'Bash',10000,false,'','','h3','build',0,0,'',false,false,true),
	('claude-sonnet-4','S1','2026-06-01T10:01:00Z',99999,99999,99999,99999,'Bash',99999,false,'','','hmeta','x',99999,99999,'max_tokens',true,true,true),
	('<synthetic>','S1','2026-06-01T10:01:10Z',88888,88888,0,0,'Read',7777,false,'','','h2','y',0,0,'',false,false,false),
	('claude-sonnet-4','S1','2026-06-01T10:02:00Z',300,150,0,0,'',0,true,'agent-1','researcher','','',0,0,'',false,false,false),
	('claude-sonnet-4','S1','2026-06-01T10:04:00Z',200,100,0,0,'',0,true,'agent-1','researcher','','',0,0,'',false,false,false),
	('claude-sonnet-4','S1','2026-06-01T10:05:00Z',100,50,0,0,'',0,true,'agent-2','planner','','',0,0,'',false,false,false),
	('claude-sonnet-4','S1','2026-06-01T10:06:00Z',100,50,0,0,'',0,true,'agent-2','planner','','',0,0,'',false,false,false)`

// newDetectorFixture writes the shared fixture parquet into the fixed
// {source}/{session}.parquet cache layout and returns the analyzer over that cache
// root plus the fixture's direct path (the *From builder tests read the path directly;
// the RunDetectors test relies on the cache glob).
func newDetectorFixture(t *testing.T) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	srcDir := filepath.Join(root, "claude")
	fixturePath := filepath.Join(srcDir, "S1.parquet")

	require.NoError(t, os.MkdirAll(srcDir, 0o750))

	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	//nolint:gosec // fixturePath is a t.TempDir() location, not user input.
	copySQL := "COPY (" + detectorFixtureProjection + " FROM (VALUES " + detectorFixtureRows +
		") AS t" + detectorFixtureColumns + ") TO '" + fixturePath + "' (FORMAT parquet)"
	_, err = db.ExecContext(context.Background(), copySQL)
	require.NoError(t, err)

	svc, err := NewService(root)
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })
	return svc, fixturePath
}

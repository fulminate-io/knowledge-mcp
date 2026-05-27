// SPDX-License-Identifier: Apache-2.0

package tools

// topology_register.go blank-imports the four topology analyzer family
// packages so their init() self-registration (foundation.Register) fires when
// this package is loaded. Without these imports foundation.All() / foundation.Get
// would see an empty registry and the manage(topology) sweep + the single-analyzer
// query intercept would dispatch nothing.
//
// This mirrors the database/sql + net/http driver-registration convention (and
// the client collector/registry init-registration precedent): a package that
// drives a registry blank-imports every provider so the registry is fully
// populated at dispatch time. The dead_code analyzer is NOT among these — it runs
// through the dedicated clienttopo.RunDeadCode RTA path (filesystem packages.Load
// + SSA), not the foundation registry, so it is imported as clienttopo directly
// in intercept_topology.go rather than blank-imported here.
import (
	_ "github.com/fulminate-io/knowledge-mcp/internal/topology/cloud"
	_ "github.com/fulminate-io/knowledge-mcp/internal/topology/content"
	_ "github.com/fulminate-io/knowledge-mcp/internal/topology/exposure"
	_ "github.com/fulminate-io/knowledge-mcp/internal/topology/graph"
)

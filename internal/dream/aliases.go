// SPDX-License-Identifier: Apache-2.0

package dream

import "github.com/fulminate-io/knowledge-mcp/internal/workers"

// Worker types and event constants are defined in domains/workers (so
// the server side can read/write the worker JSON blob without importing
// the client-side dream runner). Aliases here keep the dream package's
// public surface consistent with pre-extraction call sites.
type (
	Worker  = workers.Worker
	Trigger = workers.Trigger
)

const (
	EventToolStarted     = workers.EventToolStarted
	EventToolCompleted   = workers.EventToolCompleted
	EventWorkerStarted   = workers.EventWorkerStarted
	EventWorkerCompleted = workers.EventWorkerCompleted
	EventCron            = workers.EventCron
	EventManual          = workers.EventManual
)

var (
	DefaultMaxIterations       = workers.DefaultMaxIterations
	DefaultMaxWallclockSeconds = workers.DefaultMaxWallclockSeconds
)

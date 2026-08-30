// SPDX-License-Identifier: Apache-2.0

package collector

import (
	"context"
	"fmt"
)

// DefaultSinkFactory, when non-nil, constructs the sink used when
// CollectOptions.Sink is nil. The collector root package does not import
// collector/local (that would re-introduce the store write-path transitive
// dep this refactor eliminates). Instead, the binary-main / test init
// registers a factory by blank-importing collector/local (which registers
// local.NewStoreSink) or collector/remote (RemoteUploadSink).
//
// If both opts.Sink is nil AND DefaultSinkFactory is nil, Collect returns an
// error.
var DefaultSinkFactory func() Sink

// Collect is the top-level entry point for the collector pipeline. It looks
// up a registered collector by typ, invokes it to obtain a CollectResult,
// then hands the result to the configured Sink (DefaultSinkFactory by
// default). The server the sink streams into applies its own force/replace
// policy at ingest time.
//
// It also returns the node-type composition of the run. The census is taken
// here because this is where the whole CollectResult is in memory client-side,
// before it is handed to the sink; node Type is set at emission and nothing
// downstream retypes a node.
//
// Collect performs NO composition ASSERTION — that is CheckComposition's job,
// called once at the single top-level collect site. The placement is
// load-bearing: the four cloud collectors call Collect RECURSIVELY for cascade
// targets, so an assertion here would fire once per cascade sub-collect and let
// a sub-collect's composition fail the parent.
//
// Every error path returns the zero CollectComposition.
func Collect(ctx context.Context, typ, id string, opts CollectOptions) (CollectComposition, error) {
	c, err := Lookup(typ)
	if err != nil {
		return CollectComposition{}, fmt.Errorf("collect %s: %w", typ, err)
	}

	result, err := c.Collect(ctx, id, opts)
	if err != nil {
		return CollectComposition{}, fmt.Errorf("collect %s: %w", typ, err)
	}

	comp := NewCollectComposition(result)

	sink, err := resolveSink(opts)
	if err != nil {
		return CollectComposition{}, fmt.Errorf("collect %s: %w", typ, err)
	}
	if err := sink.WriteResult(ctx, typ, result); err != nil {
		return CollectComposition{}, err
	}
	return comp, nil
}

// resolveSink picks the Sink for this call: opts.Sink wins when set,
// otherwise DefaultSinkFactory (registered by the binary-main via blank
// import). Returns an error when neither is available.
func resolveSink(opts CollectOptions) (Sink, error) {
	if opts.Sink != nil {
		return opts.Sink, nil
	}
	if DefaultSinkFactory != nil {
		return DefaultSinkFactory(), nil
	}
	return nil, fmt.Errorf("collector: no default Sink registered (blank-import collector/local or collector/remote, or set CollectOptions.Sink)")
}

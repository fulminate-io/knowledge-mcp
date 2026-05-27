// SPDX-License-Identifier: Apache-2.0

package cloud

import (
	"context"
	"testing"
)

// newTestCtx returns a plain background context. The cloud-parent runner /
// cascade / resolution-map code only reads cancellation and context-values
// from ctx — it never touches a store.Txn — so the wire-only test surface
// installs no store engine state here. Retained as a named helper so the
// cascade / runner / subcollector tests keep a single ctx-construction
// callsite (the legacy txn-installing initTestStore was removed alongside
// the deleted ReindexCloud-specific tests).
func newTestCtx(_ testing.TB) context.Context {
	return context.Background()
}

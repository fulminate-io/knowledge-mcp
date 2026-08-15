// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"fmt"
	"strings"
)

// declKey identifies a declaration by its exact declared position in a scope.
// Parent and Name are BASE names — the "#<astPathHash>" suffix a colliding
// declaration takes belongs to its IDENTITY, never to this key, because a
// reference writes Thing and never Thing#a1b2c3d4.
type declKey struct {
	Scope  string
	Parent string
	Name   string
}

// scopeNameKey is the any-parent view of a scope: every declaration of a name
// within one scope unit regardless of which parent declares it. This is the
// candidate set of a runtime dispatch, which cannot know the parent statically.
type scopeNameKey struct {
	Scope string
	Name  string
}

// declRec is one declaration, as resolution sees it. Every field has a named
// reader — a carrier arrives with its consumer or not at all.
type declRec struct {
	// NodeID is the emitted edge's target, and the resolved source of a Go
	// receiver containment edge. It is the SUFFIXED identity.
	NodeID string
	// File backs file-order assertions on a lookup's candidate slice.
	File string
	// Scope is the resolution unit this declaration lives in.
	Scope string
	// Parent is the BASE name of the declaring container, or "".
	Parent string
	// Name is the declaration's BASE name.
	Name string
}

// declIndex is the set-valued, identity-keyed replacement for the scalar
// symbol map whose bare last-write-wins assignment destroyed one declaration
// per collision.
//
// THREE VIEWS, and deliberately no by-file view: nothing reads one. byID serves
// identity; byKey serves the qualified, sibling and own-scope rules plus the Go
// receiver containment source; byScopeName serves the dynamic rule. A field
// with no consumer is the same dead carrier this work removes elsewhere.
//
// The two keyed views answer DIFFERENT questions and are populated in one pass
// so they cannot disagree. byKey is parent-qualified by construction and so
// cannot answer "what could a dispatch on this name reach here regardless of
// parent"; iterating it per lookup to find out would be O(keys) per reference.
type declIndex struct {
	byID        map[string]*declRec
	byKey       map[declKey][]*declRec
	byScopeName map[scopeNameKey][]*declRec

	// scopes is the set of scope IDs that contribute at least one declaration.
	// The external-qualifier rule reads it to tell "this bind's target is
	// indexed" from "this bind's target contributes nothing", which is what
	// lets a reference through an unindexed target terminate instead of
	// manufacturing an edge to a same-named local.
	//
	// Written in add(), which is already the single write path, so the set
	// cannot drift from the index it summarizes.
	scopes map[string]bool
}

// newDeclIndex pre-sizes all three maps from the total declaration count.
// Growing a map from zero across tens of thousands of declarations costs
// several rehashes for no reason — the count is known cheaply before the build.
func newDeclIndex(capacity int) *declIndex {
	return &declIndex{
		byID:        make(map[string]*declRec, capacity),
		byKey:       make(map[declKey][]*declRec, capacity),
		byScopeName: make(map[scopeNameKey][]*declRec, capacity),
		scopes:      make(map[string]bool),
	}
}

// add records one declaration in every view.
//
// It RETURNS AN ERROR on a duplicate NodeID, and that error is the whole
// enforcement of "a collision is unrepresentable": the property is only real
// because something checks it. A duplicate ID means ChunkNodeID or
// DeduplicateChunks has regressed — a defect to alarm on, not a case to serve —
// so the caller logs it and keeps the FIRST record.
func (ix *declIndex) add(rec *declRec) error {
	if prior, ok := ix.byID[rec.NodeID]; ok {
		return fmt.Errorf("duplicate declaration node ID %q (already held by %s:%s.%s)",
			rec.NodeID, prior.File, prior.Parent, prior.Name)
	}
	ix.byID[rec.NodeID] = rec

	k := declKey{Scope: rec.Scope, Parent: rec.Parent, Name: rec.Name}
	ix.byKey[k] = append(ix.byKey[k], rec)

	sk := scopeNameKey{Scope: rec.Scope, Name: rec.Name}
	ix.byScopeName[sk] = append(ix.byScopeName[sk], rec)

	ix.scopes[rec.Scope] = true
	return nil
}

// hasScope reports whether a scope contributes any declaration to the index.
func (ix *declIndex) hasScope(scope string) bool {
	return ix.scopes[scope]
}

// lookup returns every declaration under an exact parent in a scope. The
// returned slice is in build order, which is file order then in-file byte
// order — deterministic by construction, never a map range.
func (ix *declIndex) lookup(k declKey) []*declRec {
	return ix.byKey[k]
}

// lookupScopeName returns every declaration of a name within one scope,
// REGARDLESS of parent — the candidate set of a runtime dispatch. Its sets are
// legitimately larger than lookup's: a package with many same-named methods
// genuinely offers that many dispatch targets, whereas the same count under one
// exact key would mean the scope unit is too coarse.
func (ix *declIndex) lookupScopeName(k scopeNameKey) []*declRec {
	return ix.byScopeName[k]
}

// baseDeclName strips the "#<astPathHash>" suffix that resolveCollisionNames
// appends to a declaration whose (parent, name) collides inside its file.
//
// The suffix is part of the declaration's IDENTITY and flows into its node ID;
// it is never part of a KEY, because a reference carries the base name only.
// Keying on the base name is what lets a reference to a collided declaration
// find the whole surviving set rather than nothing at all.
func baseDeclName(name string) string {
	base, _, _ := strings.Cut(name, "#")
	return base
}

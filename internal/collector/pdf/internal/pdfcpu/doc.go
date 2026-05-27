// Package pdfcpu is the internal wrapper over
// github.com/pdfcpu/pdfcpu. It is the ONLY package in collector/pdf/ that
// imports pdfcpu directly; every other sub-package consumes pdfcpu through
// the types and functions exported here. A confinement-audit script in T1
// enforces the boundary by failing CI if any other file imports pdfcpu.
//
// Filled by T1 (Load, LoadFile, Context, PageObject, IsTagged, Info-dict
// getters); extended in later tickets as more pdfcpu surface is needed.
package pdfcpu

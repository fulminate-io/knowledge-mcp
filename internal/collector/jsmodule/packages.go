// SPDX-License-Identifier: Apache-2.0

package jsmodule

import (
	"encoding/json"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// exportConditions is the preference order for an `exports` conditions object.
// import / module / default are preferred in that order; require, node and
// types are accepted rather than preferred, because they name a build the
// collector is not reading but still point at a real file.
var exportConditions = []string{"import", "module", "default", "require", "node", "types"}

// pkgEntry is one in-repo workspace package: a package.json that declares a
// name, and therefore a bare specifier some other file in this repo can import.
type pkgEntry struct {
	// dir is the package.json's repo-relative directory ("" at the root).
	dir  string
	name string

	// exports is held raw because the field is polymorphic — a string, a
	// subpath map, or a bare conditions map — and each shape is read on demand.
	exports json.RawMessage
	module  string
	main    string
}

// pkgIndex answers "does a bare specifier name a package inside this repo".
//
// A bare specifier that matches NO participating package is out of repo: it
// lives in node_modules, which the collector never discovered.
//
// HONEST SCOPE NOTE: the acceptance corpus has exactly one package.json and no
// workspaces, so zero bare specifiers resolve in-repo THERE and this table is
// exercised by fixtures rather than by that corpus. That is a property of the
// corpus, not evidence the branch is unnecessary — the ticket names
// package.json exports as a required shape.
type pkgIndex struct {
	byName map[string]*pkgEntry
}

// newPkgIndex reads every package.json in the discovered set once. A package
// participates only when it declares a name; an unparseable manifest is skipped
// with a warning rather than failing the collect.
func newPkgIndex(root string, files []string) *pkgIndex {
	pi := &pkgIndex{byName: map[string]*pkgEntry{}}
	for _, f := range files {
		if path.Base(f) != "package.json" {
			continue
		}
		// Sanitized for the same reason as the config reader: a caller-supplied
		// root joined with a discovered path is cleaned, not trusted.
		data, err := os.ReadFile(filepath.Clean(filepath.Join(root, f)))
		if err != nil {
			slog.Warn("jsmodule: package.json unreadable", "path", f, "err", err)
			continue
		}
		var raw struct {
			Name    string          `json:"name"`
			Exports json.RawMessage `json:"exports"`
			Module  string          `json:"module"`
			Main    string          `json:"main"`
		}
		if err := json.Unmarshal(data, &raw); err != nil {
			slog.Warn("jsmodule: package.json unparseable", "path", f, "err", err)
			continue
		}
		if raw.Name == "" {
			continue
		}
		dir := path.Dir(f)
		if dir == "." {
			dir = ""
		}
		pi.byName[raw.Name] = &pkgEntry{
			dir: dir, name: raw.Name, exports: raw.Exports, module: raw.Module, main: raw.Main,
		}
	}
	return pi
}

// resolve maps a bare specifier onto a repo-relative candidate path, or reports
// that no in-repo package claims it.
//
// The returned candidate still goes through the F1-F4 file-resolution ladder:
// an entry point may name an extensionless path or a directory holding an index
// file exactly as a relative specifier may.
func (pi *pkgIndex) resolve(specifier string) (string, bool) {
	entry, subpath, ok := pi.match(specifier)
	if !ok {
		return "", false
	}
	if target := entry.entryFor(subpath); target != "" {
		return path.Clean(path.Join(entry.dir, target)), true
	}
	if subpath != "." {
		// A subpath with no exports map addresses the package directory.
		return path.Clean(path.Join(entry.dir, strings.TrimPrefix(subpath, "./"))), true
	}
	switch {
	case entry.module != "":
		return path.Clean(path.Join(entry.dir, entry.module)), true
	case entry.main != "":
		return path.Clean(path.Join(entry.dir, entry.main)), true
	default:
		return path.Clean(path.Join(entry.dir, "index")), true
	}
}

// match finds the package a bare specifier names, and the subpath within it.
// The longest matching package name wins, so a scoped package @a/b never loses
// to a shorter @a.
func (pi *pkgIndex) match(specifier string) (*pkgEntry, string, bool) {
	var best *pkgEntry
	var bestSub string
	for name, entry := range pi.byName {
		var sub string
		switch {
		case specifier == name:
			sub = "."
		case strings.HasPrefix(specifier, name+"/"):
			sub = "./" + strings.TrimPrefix(specifier, name+"/")
		default:
			continue
		}
		if best == nil || len(name) > len(best.name) {
			best, bestSub = entry, sub
		}
	}
	if best == nil {
		return nil, "", false
	}
	return best, bestSub, true
}

// entryFor reads one subpath out of the package's `exports` field, or returns
// "" when the field is absent or names nothing for this subpath.
func (e *pkgEntry) entryFor(subpath string) string {
	if len(e.exports) == 0 {
		return ""
	}
	// STRING FORM: `"exports": "./index.js"` names the package root only.
	var asString string
	if err := json.Unmarshal(e.exports, &asString); err == nil {
		if subpath == "." {
			return asString
		}
		return ""
	}
	var asObject map[string]json.RawMessage
	if err := json.Unmarshal(e.exports, &asObject); err != nil {
		return ""
	}
	// A KEY STARTING WITH '.' MAKES IT A SUBPATH MAP; otherwise the whole
	// object is a conditions map addressing the package root.
	subpathMap := false
	for k := range asObject {
		if strings.HasPrefix(k, ".") {
			subpathMap = true
			break
		}
	}
	if !subpathMap {
		if subpath != "." {
			return ""
		}
		return pickCondition(e.exports)
	}
	if raw, ok := asObject[subpath]; ok {
		return pickCondition(raw)
	}
	return matchExportWildcard(asObject, subpath)
}

// matchExportWildcard applies the '*' subpath form — `"./util/*": "./src/util/*.js"`
// — substituting the matched remainder into the target, longest literal prefix
// winning exactly as a tsconfig path key does.
func matchExportWildcard(obj map[string]json.RawMessage, subpath string) string {
	best, bestStar, bestPrefix := "", "", -1
	for k := range obj {
		prefix, suffix, isWildcard := strings.Cut(k, "*")
		if !isWildcard {
			continue
		}
		if !strings.HasPrefix(subpath, prefix) || !strings.HasSuffix(subpath, suffix) {
			continue
		}
		if len(subpath) < len(prefix)+len(suffix) {
			continue
		}
		if len(prefix) > bestPrefix {
			best, bestPrefix = k, len(prefix)
			bestStar = subpath[len(prefix) : len(subpath)-len(suffix)]
		}
	}
	if best == "" {
		return ""
	}
	target := pickCondition(obj[best])
	return strings.ReplaceAll(target, "*", bestStar)
}

// pickCondition reads a conditional export value: a plain string, or a
// conditions object from which the first supported condition wins.
func pickCondition(raw json.RawMessage) string {
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return asString
	}
	var conds map[string]json.RawMessage
	if err := json.Unmarshal(raw, &conds); err != nil {
		return ""
	}
	for _, cond := range exportConditions {
		if v, ok := conds[cond]; ok {
			// Conditions nest — `{"import": {"default": "./x.js"}}` is legal.
			if got := pickCondition(v); got != "" {
				return got
			}
		}
	}
	return ""
}

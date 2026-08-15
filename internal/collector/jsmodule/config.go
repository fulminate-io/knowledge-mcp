// SPDX-License-Identifier: Apache-2.0

// Package jsmodule resolves an ECMAScript module specifier to the repo-relative
// file it names, with the TypeScript and Node rules a real repository needs:
// relative paths, extension inference, index files, tsconfig/jsconfig path
// aliases through extends, and in-repo package.json entry points.
//
// It resolves against the DISCOVERED FILE SET, never against the filesystem.
// The scopes a bind can usefully point at are exactly the scopes the collector's
// declaration index holds, so naming a file that was never discovered would
// claim an indexed target that is not there.
package jsmodule

import (
	"encoding/json"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// maxExtendsHops bounds the `extends` chain. A cycle is caught by the visited
// set; this bounds a merely absurd chain.
const maxExtendsHops = 8

// tsConfig is one tsconfig/jsconfig as it governs files, after its `extends`
// chain has been folded in.
type tsConfig struct {
	// path is the config's own repo-relative path, used for the deterministic
	// tie-break between two configs governing at the same depth.
	path string

	// dir is the config's repo-relative directory ("" at the repo root). A
	// config can only govern files beneath it.
	dir string

	// paths is compilerOptions.paths as declared, keys and substitutions
	// verbatim.
	paths map[string][]string

	// pathsBase is the repo-relative directory that path substitutions resolve
	// AGAINST: baseUrl joined onto the directory of the config that DECLARED
	// baseUrl — never the directory of a config that merely inherits it
	// through extends. When no config in the chain declares a baseUrl, it is
	// the directory of the config that declared the paths.
	pathsBase string

	// include and exclude are the declared patterns; governsNothing is set for
	// the solution-file shape, a config with a `files` key and no include,
	// which covers no file at all.
	include        []string
	exclude        []string
	governsNothing bool
}

// rawConfig is the on-disk shape of one config file, before extends folding.
type rawConfig struct {
	Extends         string `json:"extends"`
	CompilerOptions struct {
		BaseURL string              `json:"baseUrl"`
		Paths   map[string][]string `json:"paths"`
	} `json:"compilerOptions"`
	Include []string  `json:"include"`
	Exclude []string  `json:"exclude"`
	Files   *[]string `json:"files"`
}

// configIndex answers "which config governs this file", which is NOT the
// nearest-ancestor tsconfig.json.
//
// THE OBVIOUS RULE IS WRONG AND THE COST IS MEASURABLE. A solution-style
// tsconfig.json — `"files": []` plus `references` — carries no paths at all,
// and TypeScript does not merge a referenced project's paths into the
// referencing one, so a nearest-ancestor walk stops at the solution file and
// finds nothing. On the acceptance corpus that loses every one of the 246
// alias-resolved imports.
type configIndex struct {
	configs []*tsConfig

	// governedBy memoizes the answer per FILE. Files in one directory can take
	// different answers — an include pattern discriminates by name — so the
	// memo cannot be keyed on the directory.
	governedBy map[string]*tsConfig
}

// newConfigIndex reads every tsconfig*.json / jsconfig*.json in the discovered
// set once, folding each one's extends chain.
//
// A config that cannot be parsed is skipped with a warning rather than failing:
// a malformed config in a user's repository must never fail a collect.
func newConfigIndex(root string, files []string) *configIndex {
	ci := &configIndex{governedBy: map[string]*tsConfig{}}
	for _, f := range files {
		if !isTSConfigName(path.Base(f)) {
			continue
		}
		if cfg := loadTSConfig(root, f); cfg != nil {
			ci.configs = append(ci.configs, cfg)
		}
	}
	return ci
}

// isTSConfigName reports whether a base filename is a TypeScript or JavaScript
// project config: tsconfig.json, jsconfig.json and their tsconfig.<name>.json
// variants, which is where a solution file's real options usually live.
func isTSConfigName(base string) bool {
	if !strings.HasSuffix(base, ".json") {
		return false
	}
	return strings.HasPrefix(base, "tsconfig") || strings.HasPrefix(base, "jsconfig")
}

// loadTSConfig folds one config's extends chain into the effective options.
//
// Each option is inherited INDEPENDENTLY and the nearest declaration wins, which
// is why the walk records a value only when the slot is still empty.
func loadTSConfig(root, rel string) *tsConfig {
	cfg := &tsConfig{path: rel, dir: path.Dir(rel)}
	if cfg.dir == "." {
		cfg.dir = ""
	}

	var baseURLDir, baseURL, pathsDir string
	sawFiles, sawInclude, sawBaseURL := false, false, false
	seen := map[string]bool{}
	cur := rel

	for hop := 0; hop < maxExtendsHops && cur != ""; hop++ {
		if seen[cur] {
			break
		}
		seen[cur] = true

		raw := readRawConfig(root, cur)
		if raw == nil {
			break
		}
		curDir := path.Dir(cur)
		if curDir == "." {
			curDir = ""
		}

		if len(raw.CompilerOptions.Paths) > 0 && cfg.paths == nil {
			cfg.paths = raw.CompilerOptions.Paths
			pathsDir = curDir
		}
		if raw.CompilerOptions.BaseURL != "" && !sawBaseURL {
			sawBaseURL = true
			baseURL, baseURLDir = raw.CompilerOptions.BaseURL, curDir
		}
		if len(raw.Include) > 0 && !sawInclude {
			sawInclude = true
			cfg.include = raw.Include
		}
		if len(raw.Exclude) > 0 && cfg.exclude == nil {
			cfg.exclude = raw.Exclude
		}
		if raw.Files != nil && !sawFiles {
			sawFiles = true
		}

		if raw.Extends == "" {
			break
		}
		cur = resolveExtendsTarget(curDir, raw.Extends)
	}

	switch {
	case sawBaseURL:
		cfg.pathsBase = path.Clean(path.Join(baseURLDir, baseURL))
	default:
		cfg.pathsBase = pathsDir
	}
	if cfg.pathsBase == "." {
		cfg.pathsBase = ""
	}

	// THE SOLUTION-FILE SHAPE: a `files` key with no include covers nothing,
	// which is exactly what drops a solution config out of the governing set
	// and lets the project it references govern instead.
	cfg.governsNothing = sawFiles && !sawInclude
	return cfg
}

// resolveExtendsTarget turns an `extends` value into a repo-relative config
// path. A bare (package) extends target lives in node_modules, which is not in
// the discovered set, so it resolves to nothing.
func resolveExtendsTarget(dir, extends string) string {
	if !strings.HasPrefix(extends, "./") && !strings.HasPrefix(extends, "../") {
		return ""
	}
	target := path.Clean(path.Join(dir, extends))
	if path.Ext(target) == "" {
		target += ".json"
	}
	return target
}

// readRawConfig reads and JSONC-parses one config file.
func readRawConfig(root, rel string) *rawConfig {
	// Sanitized before the read: root arrives from a caller and is joined
	// with a discovered path, so the join is cleaned rather than trusted.
	data, err := os.ReadFile(filepath.Clean(filepath.Join(root, rel)))
	if err != nil {
		slog.Warn("jsmodule: config unreadable", "path", rel, "err", err)
		return nil
	}
	var raw rawConfig
	if err := json.Unmarshal(stripJSONC(data), &raw); err != nil {
		slog.Warn("jsmodule: config unparseable", "path", rel, "err", err)
		return nil
	}
	return &raw
}

// stripJSONC removes line comments, block comments and trailing commas so that
// encoding/json can read a real-world tsconfig.
//
// IT IS NOT OPTIONAL. encoding/json rejects a comment outright, and tsconfigs
// generated by the standard tooling carry them — the acceptance corpus's own
// web/tsconfig.app.json opens with `/* Path aliases */`. A resolver that called
// json.Unmarshal directly would read ZERO paths, take the relative branch for
// every alias import, and lose all of them silently.
//
// STRING LITERALS ARE HONORED, because a path value legitimately contains '/'
// and may contain '//' — stripping inside a string would corrupt the value it
// is trying to preserve.
func stripJSONC(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inString, escaped := false, false

	for i := 0; i < len(data); i++ {
		c := data[i]
		if inString {
			out = append(out, c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch {
		case c == '"':
			inString = true
			out = append(out, c)
		case c == '/' && i+1 < len(data) && data[i+1] == '/':
			for i < len(data) && data[i] != '\n' {
				i++
			}
			out = append(out, '\n')
		case c == '/' && i+1 < len(data) && data[i+1] == '*':
			i += 2
			for i+1 < len(data) && (data[i] != '*' || data[i+1] != '/') {
				i++
			}
			i++
			out = append(out, ' ')
		default:
			out = append(out, c)
		}
	}
	return dropTrailingCommas(out)
}

// dropTrailingCommas removes a comma that is followed only by whitespace and a
// closing brace or bracket, which JSONC permits and encoding/json does not.
func dropTrailingCommas(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inString, escaped := false, false

	for i := range len(data) {
		c := data[i]
		if inString {
			out = append(out, c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			out = append(out, c)
			continue
		}
		if c == ',' {
			j := i + 1
			for j < len(data) && isJSONSpace(data[j]) {
				j++
			}
			if j < len(data) && (data[j] == '}' || data[j] == ']') {
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

func isJSONSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// governing returns the config that governs one repo-relative file, or nil.
//
// A config governs a file when its directory is an ancestor of the file, the
// file matches an include pattern, and no exclude pattern matches it. Among the
// governing configs the LONGEST directory wins, and a tie breaks
// lexicographically by config path so the answer is stable across runs.
func (ci *configIndex) governing(file string) *tsConfig {
	if cfg, ok := ci.governedBy[file]; ok {
		return cfg
	}
	var best *tsConfig
	for _, cfg := range ci.configs {
		if !cfg.governs(file) {
			continue
		}
		switch {
		case best == nil,
			len(cfg.dir) > len(best.dir),
			len(cfg.dir) == len(best.dir) && cfg.path < best.path:
			best = cfg
		}
	}
	ci.governedBy[file] = best
	return best
}

// governs applies one config's own rules to one file.
func (c *tsConfig) governs(file string) bool {
	if c.governsNothing {
		return false
	}
	if c.dir != "" && !strings.HasPrefix(file, c.dir+"/") {
		return false
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(file, c.dir), "/")
	for _, pat := range c.exclude {
		if matchTSGlob(pat, rel) {
			return false
		}
	}
	if len(c.include) == 0 {
		// TypeScript's own default: everything beneath the config's directory.
		return true
	}
	for _, pat := range c.include {
		if matchTSGlob(pat, rel) {
			return true
		}
	}
	return false
}

// matchTSGlob matches a tsconfig include/exclude pattern against a path
// relative to the config's directory.
//
// A pattern with no wildcard and no extension names a DIRECTORY in TypeScript's
// rules — `"include": ["src"]` covers everything under src — so it is expanded
// before matching.
func matchTSGlob(pattern, name string) bool {
	pattern = strings.TrimPrefix(pattern, "./")
	if !strings.ContainsAny(pattern, "*?") && path.Ext(pattern) == "" {
		pattern = path.Join(pattern, "**/*")
	}
	return globSegments(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

// globSegments matches path segments, where "**" spans any number of segments,
// "*" any characters within one segment, and "?" one character.
func globSegments(pat, name []string) bool {
	switch {
	case len(pat) == 0:
		return len(name) == 0
	case pat[0] == "**":
		for i := 0; i <= len(name); i++ {
			if globSegments(pat[1:], name[i:]) {
				return true
			}
		}
		return false
	case len(name) == 0:
		return false
	case !globSegment(pat[0], name[0]):
		return false
	default:
		return globSegments(pat[1:], name[1:])
	}
}

// globSegment matches one segment with '*' and '?' wildcards.
func globSegment(pat, name string) bool {
	if pat == "*" {
		return true
	}
	pi, ni, star, mark := 0, 0, -1, 0
	for ni < len(name) {
		switch {
		case pi < len(pat) && (pat[pi] == '?' || pat[pi] == name[ni]):
			pi++
			ni++
		case pi < len(pat) && pat[pi] == '*':
			star, mark = pi, ni
			pi++
		case star >= 0:
			mark++
			pi, ni = star+1, mark
		default:
			return false
		}
	}
	for pi < len(pat) && pat[pi] == '*' {
		pi++
	}
	return pi == len(pat)
}

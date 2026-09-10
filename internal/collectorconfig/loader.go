// SPDX-License-Identifier: Apache-2.0

package collectorconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// loader.go — the two scopes, their precedence, and where each file lives.
//
// THE LOADER RESOLVES NO HOME DIRECTORY AND HAS NO $HOME FALLBACK. Both paths
// are PARAMETERS the call site supplies, which is what lets every test build its
// scopes under t.TempDir() and is why no suite here can reach the operator's
// real ~/.knowledge. A client test suite has already written an operator's real
// config directory on this project once; storage locations are caller
// parameters, not package knowledge.

// Loader reads the two scoped files. A zero-value path means that scope is
// absent: an empty UserPath contributes no user entries, and an empty
// ProjectPath means the session resolved to no project file at all.
type Loader struct {
	// UserPath is ~/.knowledge/collectors.json, resolved by the caller.
	UserPath string
	// ProjectPath is <repo root>/.knowledge/collectors.json for THIS session's
	// working directory, or "" when no ancestor of it holds one.
	ProjectPath string
	// Env resolves ${VAR} references. nil means os.LookupEnv.
	Env varLookup
}

// UserPathIn returns the user-scope path under a given home directory. The
// expression matches every other ~/.knowledge consumer in this binary; the home
// directory itself is the caller's to resolve.
func UserPathIn(home string) string {
	return filepath.Join(home, ConfigDirName, FileName)
}

// ProjectPathIn returns the project-scope path for a given repository root.
func ProjectPathIn(root string) string {
	return filepath.Join(root, ConfigDirName, FileName)
}

// FindProjectPath walks UP from cwd to the nearest ancestor directory holding a
// project-scope file, returning "" when none does.
//
// IT WALKS UP RATHER THAN READING ONE FIXED DIRECTORY because the lookup runs
// inside the shared daemon on the session's own working directory, which is
// usually a subdirectory of the repository rather than its root. The walk stops
// at the filesystem root, and a session whose cwd has no such ancestor sees
// user-scope entries only.
//
// A cwd is NOT a git root: two concurrent sessions standing in two different
// repositories legitimately resolve two different project files, and a session
// carrying no workspace cwd resolves from the daemon's --root instead.
func FindProjectPath(cwd string) string {
	if cwd == "" {
		return ""
	}
	dir := cwd
	for {
		candidate := ProjectPathIn(dir)
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// Entries returns every entry from BOTH scopes, project first then user, each
// sorted by name. Both files are loaded whatever the caller is looking for: a
// refusal in one scope is a refusal, never a reason to answer from the other.
func (l Loader) Entries() ([]ScopedEntry, error) {
	project, err := l.scope(ScopeProject, l.ProjectPath)
	if err != nil {
		return nil, err
	}
	user, err := l.scope(ScopeUser, l.UserPath)
	if err != nil {
		return nil, err
	}
	return append(project, user...), nil
}

// scope loads one file into ScopedEntry rows, sorted by name.
func (l Loader) scope(scope Scope, path string) ([]ScopedEntry, error) {
	if path == "" {
		return nil, nil
	}
	entries, err := loadFile(l.Env, path)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]ScopedEntry, 0, len(names))
	for _, name := range names {
		out = append(out, ScopedEntry{
			Name:  name,
			Scope: scope,
			Path:  path,
			Entry: entries[name].Entry,
			// THE ENTRY'S OWN REFUSAL TRAVELS WITH IT rather than failing this
			// scope: one entry whose reference is unset leaves every sibling in
			// the same file collectable, which is what it means for bad input to
			// error at the thing that is bad.
			Unresolved: entries[name].Unresolved,
		})
	}
	return out, nil
}

// Resolve returns the entry registered under name, and whether one exists.
//
// A RESOLVED ENTRY MAY STILL CARRY ITS OWN REFUSAL. When the winner's `${VAR}`
// references do not resolve in this process, the returned row carries the
// unexpanded entry and a non-nil Unresolved, and the error return stays nil: the
// entry IS registered, and whether that matters depends on what the caller does
// with it. Every caller that spawns from the entry reads Unresolved first.
//
// PRECEDENCE IS PROJECT THEN USER, AND THE WINNER IS TAKEN WHOLE. No field is
// merged across scopes: a project entry with no env block resolves to an empty
// environment, never to the user entry's env. That is Claude's own documented
// rule for its scopes, and merging would give an operator an entry that exists
// in neither file they can read.
func (l Loader) Resolve(name string) (ScopedEntry, bool, error) {
	all, err := l.Entries()
	if err != nil {
		return ScopedEntry{}, false, err
	}
	for _, se := range all {
		if se.Name == name {
			return se, true, nil
		}
	}
	return ScopedEntry{}, false, nil
}

// Winners returns one row per family name, the winning scope's entry, sorted by
// name — what the collect dispatch and the coverage table both see.
func (l Loader) Winners() ([]ScopedEntry, error) {
	all, err := l.Entries()
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(all))
	out := make([]ScopedEntry, 0, len(all))
	for _, se := range all {
		if _, dup := seen[se.Name]; dup {
			continue // the earlier row is the project scope, which wins.
		}
		seen[se.Name] = struct{}{}
		out = append(out, se)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// PathForScope returns the file this loader reads for one scope, and an error
// naming the scope when this loader has none for it.
func (l Loader) PathForScope(scope Scope) (string, error) {
	switch scope {
	case ScopeProject:
		if l.ProjectPath == "" {
			return "", fmt.Errorf("collector config: no project scope here — a project-scope file is %s in a repository root", ProjectPathIn("<repo root>"))
		}
		return l.ProjectPath, nil
	case ScopeUser:
		if l.UserPath == "" {
			return "", fmt.Errorf("collector config: no user scope — the user-scope file is %s and this machine's home directory could not be resolved", UserPathIn("<home>"))
		}
		return l.UserPath, nil
	default:
		return "", fmt.Errorf("collector config: unknown scope %q — it is %q or %q", scope, ScopeUser, ScopeProject)
	}
}

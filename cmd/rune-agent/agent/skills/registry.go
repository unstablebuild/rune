// Copyright (C) 2017-2026 The Rune Authors
// SPDX-License-Identifier: GPL-3.0-or-later
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or (at
// your option) any later version.
//
// This program is distributed in the hope that it will be useful, but
// WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU
// General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program. If not, see <https://www.gnu.org/licenses/>.

package skills

import (
	"fmt"
	"maps"
	"os/user"
	"slices"
	"strings"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"

	builtins "unstable.build/rune/cmd/rune-agent/skills"
)

// SkillError captures a parsing or validation error encountered while
// reading a SKILL.md file.
type SkillError struct {
	Path string
	Err  error
}

func (e SkillError) Error() string {
	return fmt.Sprintf("%s: %v", e.Path, e.Err)
}

// ReloadResult summarizes the outcome of a skill registry re-scan.
type ReloadResult struct {
	Dirs    []string     // Tracked directories that were scanned
	Loaded  []Skill      // All currently loaded skills (sorted by name)
	Added   []Skill      // Skills newly discovered during reload
	Updated []Skill      // Skills whose definitions changed during reload
	Dropped []Skill      // Skills removed since the previous scan
	Errors  []SkillError // Parsing/validation errors encountered
}

// SkillRegistry is a thread-safe, mutable registry of skills.
// Created once at startup and shared by the skill tool and agentshell.
type SkillRegistry struct {
	mu     sync.RWMutex
	fs     workspaceapi.FileSystem
	byName map[string]Skill
	dirs   []string // tracked directories (absolute paths)
	cwd    workspaceapi.URI
	n      browserapi.Notifications
}

// NewRegistry creates a registry pre-populated from the given directories.
// fs is used to read skill directories and SKILL.md files.
// cwd is used to resolve relative dirs. n may be nil (warnings
// are silently dropped).
func NewRegistry(fs workspaceapi.FileSystem, cwd workspaceapi.URI, dirs []string, n browserapi.Notifications) *SkillRegistry {
	r := &SkillRegistry{
		fs:     fs,
		byName: make(map[string]Skill),
		cwd:    cwd,
		n:      n,
	}
	for _, dir := range dirs {
		abs := r.resolve(dir)
		if slices.Contains(r.dirs, abs) {
			continue
		}
		r.dirs = append(r.dirs, abs)
		loaded, _ := r.loadDir(abs)
		for _, s := range loaded {
			r.warnDescription(s)
			if existing, exists := r.byName[s.Name]; !exists {
				r.byName[s.Name] = s
			} else {
				r.notify("skill %q shadowed: keeping %s, ignoring %s",
					s.Name, existing.Dir, s.Dir)
			}
		}
	}
	r.registerBuiltins()
	return r
}

// Get returns the skill with the given name, or false.
func (r *SkillRegistry) Get(name string) (Skill, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.byName[name]
	return s, ok
}

// GetFold returns the skill whose name matches case-insensitively,
// or false. Exact match is tried first; on miss it scans all names.
func (r *SkillRegistry) GetFold(name string) (Skill, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if s, ok := r.byName[name]; ok {
		return s, true
	}
	for k, s := range r.byName {
		if strings.EqualFold(k, name) {
			return s, true
		}
	}
	return Skill{}, false
}

// List returns a snapshot of all registered skills, sorted by name.
func (r *SkillRegistry) List() []Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.listLocked()
}

func (r *SkillRegistry) listLocked() []Skill {
	result := make([]Skill, 0, len(r.byName))
	for _, s := range r.byName {
		result = append(result, s)
	}
	slices.SortFunc(result, func(a, b Skill) int {
		return strings.Compare(a.Name, b.Name)
	})
	return result
}

// Dirs returns a snapshot of tracked directories.
func (r *SkillRegistry) Dirs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return slices.Clone(r.dirs)
}

// AddDir adds a skill directory. Resolves relative paths against
// cwd (stored at construction). Scans the directory and
// registers all found skills. Returns the list of newly added skills.
// Returns error if directory is already tracked.
func (r *SkillRegistry) AddDir(dir string) ([]Skill, error) {
	abs := r.resolve(dir)

	r.mu.Lock()
	defer r.mu.Unlock()

	if slices.Contains(r.dirs, abs) {
		return nil, fmt.Errorf("directory already tracked: %s", abs)
	}

	loaded, _ := r.loadDir(abs)
	var added []Skill
	for _, s := range loaded {
		r.warnDescription(s)
		if existing, exists := r.byName[s.Name]; !exists {
			r.byName[s.Name] = s
			added = append(added, s)
		} else {
			r.notify("skill %q shadowed: keeping %s, ignoring %s",
				s.Name, existing.Dir, s.Dir)
		}
	}
	r.dirs = append(r.dirs, abs)
	return added, nil
}

// RemoveDir removes a skill directory and unregisters all skills
// whose Dir is under that directory. Returns error if dir not tracked.
func (r *SkillRegistry) RemoveDir(dir string) error {
	abs := r.resolve(dir)

	r.mu.Lock()
	defer r.mu.Unlock()

	idx := slices.Index(r.dirs, abs)
	if idx < 0 {
		return fmt.Errorf("directory not tracked: %s", abs)
	}

	r.dirs = slices.Delete(r.dirs, idx, idx+1)
	for name, s := range r.byName {
		if strings.HasPrefix(s.Dir, abs) {
			delete(r.byName, name)
		}
	}
	return nil
}

// Reload re-scans all tracked directories and re-registers builtins.
// New or updated skills become visible; skills whose SKILL.md was
// removed are dropped. This is safe to call from the agent loop on
// every turn so that out-of-band skill installations are picked up.
func (r *SkillRegistry) Reload() ReloadResult {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Snapshot existing state for diffing.
	prevByName := make(map[string]Skill, len(r.byName))
	for k, v := range r.byName {
		prevByName[k] = v
	}

	r.byName = make(map[string]Skill, len(r.byName))
	var allErrors []SkillError

	for _, abs := range r.dirs {
		loaded, errs := r.loadDir(abs)
		allErrors = append(allErrors, errs...)
		for _, s := range loaded {
			r.warnDescription(s)
			if existing, exists := r.byName[s.Name]; !exists {
				r.byName[s.Name] = s
			} else {
				r.notify("skill %q shadowed: keeping %s, ignoring %s",
					s.Name, existing.Dir, s.Dir)
			}
		}
	}
	r.registerBuiltins()

	res := ReloadResult{
		Dirs:   slices.Clone(r.dirs),
		Errors: allErrors,
	}

	// Compute Added and Updated
	for name, curr := range r.byName {
		prev, existed := prevByName[name]
		if !existed {
			// Builtins are always present and not considered newly added.
			if !strings.HasPrefix(curr.Dir, "<builtin>/") {
				res.Added = append(res.Added, curr)
			}
		} else if !skillEqual(prev, curr) {
			res.Updated = append(res.Updated, curr)
		}
	}

	// Compute Dropped
	for name, prev := range prevByName {
		if _, exists := r.byName[name]; !exists {
			if !strings.HasPrefix(prev.Dir, "<builtin>/") {
				res.Dropped = append(res.Dropped, prev)
			}
		}
	}

	sortByName := func(a, b Skill) int { return strings.Compare(a.Name, b.Name) }
	slices.SortFunc(res.Added, sortByName)
	slices.SortFunc(res.Updated, sortByName)
	slices.SortFunc(res.Dropped, sortByName)

	res.Loaded = r.listLocked()
	return res
}

func skillEqual(a, b Skill) bool {
	return a.Name == b.Name &&
		a.Description == b.Description &&
		a.Body == b.Body &&
		a.Dir == b.Dir &&
		a.License == b.License &&
		a.Compatibility == b.Compatibility &&
		a.AllowedTools == b.AllowedTools &&
		a.Type == b.Type &&
		a.Model == b.Model &&
		a.ParentContext == b.ParentContext &&
		maps.Equal(a.Metadata, b.Metadata)
}

func (r *SkillRegistry) resolve(dir string) string {
	expanded, err := workspaceapi.ExpandPath(dir, user.Current, func() (string, error) {
		return r.cwd.Path(), nil
	})
	if err != nil {
		return dir
	}
	return expanded
}

func (r *SkillRegistry) warnDescription(s Skill) {
	if len(s.Description) > 1024 {
		r.notify("skill %q: description exceeds 1024 characters (%d)",
			s.Name, len(s.Description))
	}
}

func (r *SkillRegistry) registerBuiltins() {
	for _, entry := range []struct {
		data []byte
		name string
	}{
		{builtins.Explore, "explore"},
		{builtins.Plan, "plan"},
	} {
		s, err := Parse(entry.data, "<builtin>/"+entry.name)
		if err != nil {
			panic("builtin skill " + entry.name + ": " + err.Error())
		}
		r.byName[s.Name] = s
	}
}

func (r *SkillRegistry) notify(format string, args ...any) {
	if r.n == nil {
		return
	}
	_, _ = r.n.Notify(browserapi.LevelWarn, format, args...)
}

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
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

func dirURI(dir string) workspaceapi.URI {
	u, _ := workspaceapi.ParseURI("file://" + dir)
	return u
}

type osFileSystem struct{}

type recordedNotification struct {
	level browserapi.NotificationLevel
	msg   string
}

type recordingNotifications struct {
	mu       sync.Mutex
	messages []recordedNotification
}

func (n *recordingNotifications) Notify(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.messages = append(n.messages, recordedNotification{
		level: level,
		msg:   fmt.Sprintf(msg, args...),
	})
	return "", nil
}

func (n *recordingNotifications) NotifyOnce(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	return n.Notify(level, msg, args...)
}

func (*recordingNotifications) UpdateNotificationProgress(
	id, message string, progress, total int64,
) error {
	return nil
}

func (n *recordingNotifications) notifications() []recordedNotification {
	n.mu.Lock()
	defer n.mu.Unlock()

	return append([]recordedNotification(nil), n.messages...)
}

func (osFileSystem) URI(path string) (workspaceapi.URI, error) {
	return workspaceapi.CurrentUserHostURI(path)
}
func (osFileSystem) OpenFile(path string, flag int, mode os.FileMode) (workspaceapi.File, error) {
	return os.OpenFile(path, flag, mode)
}
func (osFileSystem) Remove(path string) error                     { return os.Remove(path) }
func (osFileSystem) Stat(path string) (os.FileInfo, error)        { return os.Stat(path) }
func (osFileSystem) ReadDir(name string) ([]os.DirEntry, error)   { return os.ReadDir(name) }
func (osFileSystem) MkdirAll(path string, perm os.FileMode) error { return os.MkdirAll(path, perm) }

func TestNewRegistry(t *testing.T) {
	t.Run("populates from dirs", func(t *testing.T) {
		dir := t.TempDir()
		writeSkill(t, dir, "alpha", `---
name: alpha
description: Alpha skill
---
alpha body`)

		r := NewRegistry(osFileSystem{}, dirURI(""), []string{dir}, nil)
		s, ok := r.Get("alpha")
		assert.True(t, ok)
		assert.Equal(t, "Alpha skill", s.Description)
		assert.Equal(t, []string{dir}, r.Dirs())
	})

	t.Run("empty dirs still has builtins", func(t *testing.T) {
		r := NewRegistry(osFileSystem{}, dirURI(""), nil, nil)
		assert.Empty(t, r.Dirs())
		for _, name := range []string{
			"explore", "plan",
		} {
			_, ok := r.Get(name)
			assert.True(t, ok, "builtin %s must be present", name)
		}
	})

	t.Run("explore cannot recursively invoke skills", func(t *testing.T) {
		r := NewRegistry(osFileSystem{}, dirURI(""), nil, nil)
		explore, ok := r.Get("explore")
		require.True(t, ok)

		assert.NotContains(t, strings.Fields(explore.AllowedTools), "skill")
		assert.Contains(t, explore.Body, "invoke the explore skill")
	})

	t.Run("deduplicates skills across dirs", func(t *testing.T) {
		dir1 := t.TempDir()
		dir2 := t.TempDir()
		writeSkill(t, dir1, "shared", `---
name: shared
description: From dir1
---
dir1 body`)
		writeSkill(t, dir2, "shared", `---
name: shared
description: From dir2
---
dir2 body`)

		r := NewRegistry(osFileSystem{}, dirURI(""), []string{dir1, dir2}, nil)
		s, ok := r.Get("shared")
		require.True(t, ok)
		assert.Equal(t, "From dir1", s.Description, "first dir wins")
	})

	t.Run("skips duplicate resolved directories", func(t *testing.T) {
		workspace := t.TempDir()
		dir := filepath.Join(workspace, "skills")
		writeSkill(t, dir, "shared", `---
name: shared
description: Shared skill
---
body`)

		notifications := &recordingNotifications{}
		r := NewRegistry(osFileSystem{}, dirURI(workspace), []string{dir, "./skills"}, notifications)

		s, ok := r.Get("shared")
		require.True(t, ok)
		assert.Equal(t, "Shared skill", s.Description)
		assert.Equal(t, []string{dir}, r.Dirs(), "resolved dirs should be tracked once")
		assert.Empty(t, notifications.notifications(), "duplicate resolved dirs should not warn")
	})

	t.Run("notifies on skill load error", func(t *testing.T) {
		dir := t.TempDir()
		writeSkill(t, dir, "broken", "---\nname: broken\n---\nno description")
		notifications := &recordingNotifications{}
		_ = NewRegistry(osFileSystem{}, dirURI(""), []string{dir}, notifications)
		notifs := notifications.notifications()
		require.NotEmpty(t, notifs)
		assert.Contains(t, notifs[0].msg, "skill load error in")
		assert.Contains(t, notifs[0].msg, "broken")
	})
}

func TestRegistryGet(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "debug", `---
name: debug
description: Debug issues
---
debug body`)

	r := NewRegistry(osFileSystem{}, dirURI(""), []string{dir}, nil)

	t.Run("returns skill", func(t *testing.T) {
		s, ok := r.Get("debug")
		assert.True(t, ok)
		assert.Equal(t, "Debug issues", s.Description)
	})

	t.Run("returns false for unknown", func(t *testing.T) {
		_, ok := r.Get("nonexistent")
		assert.False(t, ok)
	})
}

func TestRegistryGetFold(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "debug", `---
name: debug
description: Debug issues
---
debug body`)

	r := NewRegistry(osFileSystem{}, dirURI(""), []string{dir}, nil)

	t.Run("exact match", func(t *testing.T) {
		s, ok := r.GetFold("debug")
		assert.True(t, ok)
		assert.Equal(t, "debug", s.Name)
	})

	t.Run("case-insensitive match", func(t *testing.T) {
		s, ok := r.GetFold("Debug")
		assert.True(t, ok)
		assert.Equal(t, "debug", s.Name)
	})

	t.Run("all uppercase match", func(t *testing.T) {
		s, ok := r.GetFold("DEBUG")
		assert.True(t, ok)
		assert.Equal(t, "debug", s.Name)
	})

	t.Run("builtin plan case-insensitive", func(t *testing.T) {
		s, ok := r.GetFold("Plan")
		assert.True(t, ok)
		assert.Equal(t, "plan", s.Name)
	})

	t.Run("builtin explore case-insensitive", func(t *testing.T) {
		s, ok := r.GetFold("Explore")
		assert.True(t, ok)
		assert.Equal(t, "explore", s.Name)
	})

	t.Run("no match returns false", func(t *testing.T) {
		_, ok := r.GetFold("nonexistent")
		assert.False(t, ok)
	})
}

func TestRegistryList(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "beta", `---
name: beta
description: Beta
---
b`)
	writeSkill(t, dir, "alpha", `---
name: alpha
description: Alpha
---
a`)

	r := NewRegistry(osFileSystem{}, dirURI(""), []string{dir}, nil)
	list := r.List()
	names := make([]string, len(list))
	for i, s := range list {
		names[i] = s.Name
	}
	assert.Contains(t, names, "alpha")
	assert.Contains(t, names, "beta")
	assert.Contains(t, names, "explore")
	assert.Contains(t, names, "plan")
	assert.True(t, slices.IsSorted(names), "list must be sorted")
}

func TestRegistryAddDir(t *testing.T) {
	t.Run("scans and registers", func(t *testing.T) {
		r := NewRegistry(osFileSystem{}, dirURI(""), nil, nil)
		dir := t.TempDir()
		writeSkill(t, dir, "new-skill", `---
name: new-skill
description: New
---
body`)

		added, errs, err := r.AddDir(dir)
		require.NoError(t, err)
		assert.Empty(t, errs)
		assert.Len(t, added, 1)
		assert.Equal(t, "new-skill", added[0].Name)

		s, ok := r.Get("new-skill")
		assert.True(t, ok)
		assert.Equal(t, "New", s.Description)
		assert.Contains(t, r.Dirs(), dir)
	})

	t.Run("duplicate dir returns error", func(t *testing.T) {
		dir := t.TempDir()
		r := NewRegistry(osFileSystem{}, dirURI(""), []string{dir}, nil)

		_, _, err := r.AddDir(dir)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "already tracked")
	})

	t.Run("resolves relative path", func(t *testing.T) {
		workspace := t.TempDir()
		relDir := "my-skills"
		writeSkill(t, workspace+"/"+relDir, "rel", `---
name: rel
description: Relative
---
body`)

		r := NewRegistry(osFileSystem{}, dirURI(workspace), nil, nil)
		_, _, err := r.AddDir(relDir)
		require.NoError(t, err)

		_, ok := r.Get("rel")
		assert.True(t, ok)
	})

	t.Run("expands tilde to home directory", func(t *testing.T) {
		home, err := os.UserHomeDir()
		require.NoError(t, err)

		r := NewRegistry(osFileSystem{}, dirURI(""), nil, nil)
		resolved := r.resolve("~/some/path")
		assert.Equal(t, filepath.Join(home, "some/path"), resolved)
		assert.True(t, filepath.IsAbs(resolved))
	})

	t.Run("tilde alone expands to home", func(t *testing.T) {
		home, err := os.UserHomeDir()
		require.NoError(t, err)

		r := NewRegistry(osFileSystem{}, dirURI(""), nil, nil)
		resolved := r.resolve("~")
		assert.Equal(t, home, resolved)
	})

	t.Run("expands environment variables", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("TEST_SKILL_DIR", dir)

		r := NewRegistry(osFileSystem{}, dirURI(""), nil, nil)
		resolved := r.resolve("$TEST_SKILL_DIR/skills")
		assert.Equal(t, filepath.Join(dir, "skills"), resolved)
		assert.True(t, filepath.IsAbs(resolved))
	})

	t.Run("skips existing skill names", func(t *testing.T) {
		dir1 := t.TempDir()
		dir2 := t.TempDir()
		writeSkill(t, dir1, "shared", `---
name: shared
description: Original
---
original`)
		writeSkill(t, dir2, "shared", `---
name: shared
description: Duplicate
---
dup`)

		r := NewRegistry(osFileSystem{}, dirURI(""), []string{dir1}, nil)
		added, _, err := r.AddDir(dir2)
		require.NoError(t, err)
		assert.Empty(t, added, "existing name should be skipped")

		s, _ := r.Get("shared")
		assert.Equal(t, "Original", s.Description)
	})

	t.Run("surfaces parse errors for broken skills", func(t *testing.T) {
		dir := t.TempDir()
		writeSkill(t, dir, "valid", "---\nname: valid\ndescription: Valid\n---\nbody")
		writeSkill(t, dir, "broken", "---\nname: broken\n---\nno description")

		r := NewRegistry(osFileSystem{}, dirURI(""), nil, nil)
		added, errs, err := r.AddDir(dir)
		require.NoError(t, err)
		assert.Len(t, added, 1)
		assert.Equal(t, "valid", added[0].Name)
		require.Len(t, errs, 1)
		assert.Contains(t, errs[0].Path, "broken")
		assert.Error(t, errs[0].Err)
	})
}

func TestRegistryRemoveDir(t *testing.T) {
	t.Run("unregisters skills from dir but keeps builtins", func(t *testing.T) {
		dir := t.TempDir()
		writeSkill(t, dir, "remove-me", `---
name: remove-me
description: Will be removed
---
body`)

		r := NewRegistry(osFileSystem{}, dirURI(""), []string{dir}, nil)
		_, ok := r.Get("remove-me")
		require.True(t, ok)

		err := r.RemoveDir(dir)
		require.NoError(t, err)
		_, ok = r.Get("remove-me")
		assert.False(t, ok, "filesystem skill must be removed")
		_, ok = r.Get("explore")
		assert.True(t, ok, "builtin explore must survive dir removal")
		_, ok = r.Get("plan")
		assert.True(t, ok, "builtin plan must survive dir removal")
		assert.Empty(t, r.Dirs())
	})

	t.Run("unknown dir returns error", func(t *testing.T) {
		r := NewRegistry(osFileSystem{}, dirURI(""), nil, nil)
		err := r.RemoveDir("/nonexistent")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not tracked")
	})
}

func TestRegistryBuiltinsOverrideFilesystem(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "explore", `---
name: explore
description: Custom explore from filesystem
---
custom body`)

	r := NewRegistry(osFileSystem{}, dirURI(""), []string{dir}, nil)
	s, ok := r.Get("explore")
	require.True(t, ok)
	assert.Equal(t, "<builtin>/explore", s.Dir, "builtin must override filesystem skill")
}

func TestRegistryReload(t *testing.T) {
	t.Run("picks up new skills", func(t *testing.T) {
		dir := t.TempDir()
		writeSkill(t, dir, "initial", `---
name: initial
description: Initial
---
body`)

		r := NewRegistry(osFileSystem{}, dirURI(""), []string{dir}, nil)
		_, ok := r.Get("initial")
		require.True(t, ok)
		_, ok = r.Get("added-later")
		require.False(t, ok)

		// Add a new skill on disk, then reload.
		writeSkill(t, dir, "added-later", `---
name: added-later
description: Added after initial load
---
new body`)

		r.Reload()
		_, ok = r.Get("added-later")
		assert.True(t, ok, "new skill must appear after Reload")
		_, ok = r.Get("initial")
		assert.True(t, ok, "existing skill must survive Reload")
	})

	t.Run("drops removed skills", func(t *testing.T) {
		dir := t.TempDir()
		writeSkill(t, dir, "ephemeral", `---
name: ephemeral
description: Will be removed
---
body`)

		r := NewRegistry(osFileSystem{}, dirURI(""), []string{dir}, nil)
		_, ok := r.Get("ephemeral")
		require.True(t, ok)

		// Remove the skill from disk, then reload.
		require.NoError(t, os.RemoveAll(filepath.Join(dir, "ephemeral")))

		r.Reload()
		_, ok = r.Get("ephemeral")
		assert.False(t, ok, "removed skill must disappear after Reload")
	})

	t.Run("preserves builtins", func(t *testing.T) {
		r := NewRegistry(osFileSystem{}, dirURI(""), nil, nil)
		r.Reload()
		for _, name := range []string{"explore", "plan"} {
			_, ok := r.Get(name)
			assert.True(t, ok, "builtin %s must survive Reload", name)
		}
	})

	t.Run("returns differential reload result and surfaces errors", func(t *testing.T) {
		dir := t.TempDir()
		writeSkill(t, dir, "initial", `---
name: initial
description: Initial
---
body`)

		r := NewRegistry(osFileSystem{}, dirURI(""), []string{dir}, nil)

		// Unchanged reload
		res := r.Reload()
		assert.Equal(t, []string{dir}, res.Dirs)
		assert.Empty(t, res.Added)
		assert.Empty(t, res.Updated)
		assert.Empty(t, res.Dropped)
		assert.Empty(t, res.Errors)
		assert.Len(t, res.Loaded, 3)

		// Added
		writeSkill(t, dir, "added", `---
name: added
description: Added skill
---
body`)
		res = r.Reload()
		require.Len(t, res.Added, 1)
		assert.Equal(t, "added", res.Added[0].Name)
		assert.Empty(t, res.Updated)
		assert.Empty(t, res.Dropped)
		assert.Empty(t, res.Errors)
		for _, s := range res.Added {
			assert.NotEqual(t, "explore", s.Name)
			assert.NotEqual(t, "plan", s.Name)
		}

		// Updated
		writeSkill(t, dir, "initial", `---
name: initial
description: Updated initial
---
body`)
		res = r.Reload()
		assert.Empty(t, res.Added)
		require.Len(t, res.Updated, 1)
		assert.Equal(t, "initial", res.Updated[0].Name)
		assert.Equal(t, "Updated initial", res.Updated[0].Description)
		assert.Empty(t, res.Dropped)
		assert.Empty(t, res.Errors)

		// Dropped
		require.NoError(t, os.RemoveAll(filepath.Join(dir, "added")))
		res = r.Reload()
		assert.Empty(t, res.Added)
		assert.Empty(t, res.Updated)
		require.Len(t, res.Dropped, 1)
		assert.Equal(t, "added", res.Dropped[0].Name)
		for _, s := range res.Dropped {
			assert.NotEqual(t, "explore", s.Name)
			assert.NotEqual(t, "plan", s.Name)
		}

		// Errors
		writeSkill(t, dir, "broken", `---
name: broken
---
missing description`)
		res = r.Reload()
		require.Len(t, res.Errors, 1)
		assert.Contains(t, res.Errors[0].Path, "broken")
		assert.Error(t, res.Errors[0].Err)
		assert.Contains(t, res.Errors[0].Error(), "description")

		// Unreadable / nonexistent tracked directory surfaces as SkillError
		missingDir := filepath.Join(t.TempDir(), "nonexistent")
		rMissing := NewRegistry(osFileSystem{}, dirURI(""), []string{missingDir}, nil)
		resMissing := rMissing.Reload()
		require.Len(t, resMissing.Errors, 1)
		assert.Equal(t, missingDir, resMissing.Errors[0].Path)
		assert.Error(t, resMissing.Errors[0].Err)
	})
}

func TestRegistryReloadSince(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "skill-1", "---\nname: skill-1\ndescription: Skill 1\n---\nbody")

	r := NewRegistry(osFileSystem{}, dirURI(""), []string{dir}, nil)
	baseline := r.Snapshot()
	assert.Contains(t, baseline, "skill-1")
	assert.Contains(t, baseline, "explore")

	// Add a new skill
	writeSkill(t, dir, "skill-2", "---\nname: skill-2\ndescription: Skill 2\n---\nbody")

	// Intermediate standard reloads update registry state
	for range 3 {
		r.Reload()
	}

	// ReloadSince against the older baseline must still identify skill-2 as Added
	res := r.ReloadSince(baseline)
	require.Len(t, res.Added, 1)
	assert.Equal(t, "skill-2", res.Added[0].Name)
	assert.Empty(t, res.Updated)
	assert.Empty(t, res.Dropped)
}

func TestSkillEqual(t *testing.T) {
	base := Skill{
		Name:          "skill",
		Description:   "desc",
		Body:          "body",
		Dir:           "/path/to/skill",
		License:       "MIT",
		Compatibility: ">=1.0",
		Metadata:      map[string]string{"author": "alice"},
		AllowedTools:  "read_file",
		Type:          "agent",
		Model:         "claude",
		ParentContext: true,
	}

	t.Run("identical skills are equal", func(t *testing.T) {
		other := base
		other.Metadata = map[string]string{"author": "alice"}
		assert.True(t, skillEqual(base, other))
	})

	t.Run("nil and empty metadata handling", func(t *testing.T) {
		s1 := base
		s1.Metadata = nil
		s2 := base
		s2.Metadata = map[string]string{}
		assert.True(t, skillEqual(s1, s2))
	})

	t.Run("different metadata is not equal", func(t *testing.T) {
		other := base
		other.Metadata = map[string]string{"author": "bob"}
		assert.False(t, skillEqual(base, other))
	})

	t.Run("different field is not equal", func(t *testing.T) {
		fields := []func(*Skill){
			func(s *Skill) { s.Name = "diff" },
			func(s *Skill) { s.Description = "diff" },
			func(s *Skill) { s.Body = "diff" },
			func(s *Skill) { s.Dir = "diff" },
			func(s *Skill) { s.License = "diff" },
			func(s *Skill) { s.Compatibility = "diff" },
			func(s *Skill) { s.AllowedTools = "diff" },
			func(s *Skill) { s.Type = "diff" },
			func(s *Skill) { s.Model = "diff" },
			func(s *Skill) { s.ParentContext = false },
		}
		for i, mod := range fields {
			other := base
			other.Metadata = map[string]string{"author": "alice"}
			mod(&other)
			assert.False(t, skillEqual(base, other), "field %d should not be equal", i)
		}
	})
}

func TestRegistryConcurrentAccess(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	writeSkill(t, dir1, "skill-a", `---
name: skill-a
description: A
---
a`)
	writeSkill(t, dir2, "skill-b", `---
name: skill-b
description: B
---
b`)

	r := NewRegistry(osFileSystem{}, dirURI(""), []string{dir1}, nil)

	var wg sync.WaitGroup
	wg.Add(4)

	go func() {
		defer wg.Done()
		for range 100 {
			r.List()
		}
	}()
	go func() {
		defer wg.Done()
		for range 100 {
			r.Get("skill-a")
		}
	}()
	go func() {
		defer wg.Done()
		_, _, _ = r.AddDir(dir2)
	}()
	go func() {
		defer wg.Done()
		for range 100 {
			r.Dirs()
		}
	}()

	wg.Wait()
}

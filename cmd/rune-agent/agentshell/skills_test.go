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

package agentshell

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"

	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/agent/skills"
	"unstable.build/rune/cmd/rune-agent/configedit"
)

func writeTestSkill(t *testing.T, baseDir, name, content string) {
	t.Helper()
	dir := filepath.Join(baseDir, name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644))
}

func newSkillsTestShell(t *testing.T, skillDirs []string) (*shell, string) {
	t.Helper()
	dataPath := newMemoryWorkspaceDir(t)
	cwdDir := t.TempDir()
	cwd := dirURI(cwdDir)
	fs := osFS{}
	sh := New(
		stubWindowManager{},
		&stopOnlyLLMService{},
		"test-model",
		&emptyDialogueStore{},
		agent.NewRegistry(),
		&agent.Cfg{},
		configedit.NewConfig(fs, cwd, nil),
		skills.NewRegistry(fs, cwd, skillDirs, nil),
		cwd,
		fs,
		storagestub.NewInMemoryService(),
		mockExec{},
		noopLSP{},
		nil,
		noopNotifications{},
		dataPath,
	)
	return sh.(*shell), cwdDir
}

func renderShellCommand(t *testing.T, sh *shell, cmd repl.Command) string {
	t.Helper()
	ctx := context.Background()
	it, err := sh.HandleCommand(ctx, cmd, noopProgressWriter{})
	require.NoError(t, err)
	require.NotNil(t, it)

	w := term.NewStringWriter(120, 60)
	for {
		comp, ok := it.Next(ctx)
		if !ok {
			break
		}
		comp.Resize(120, comp.Height(120))
		comp.Draw(w)
	}
	require.NoError(t, w.Flush())
	require.NoError(t, it.Err())
	return w.String()
}

func TestAgentShell_SkillsReload_Command(t *testing.T) {
	skillsDir := t.TempDir()
	sh, _ := newSkillsTestShell(t, []string{skillsDir})

	// Initial reload with empty directory: shows tracked dir and builtins
	out := renderShellCommand(t, sh, repl.Command{Name: CommandName, Args: []string{"skills", "reload"}})
	assert.Contains(t, out, "Skills Reloaded")
	assert.Contains(t, out, skillsDir)
	assert.Contains(t, out, "No skill changes detected.")
	assert.Contains(t, out, "explore")
	assert.Contains(t, out, "plan")

	// Add a new skill on disk
	writeTestSkill(t, skillsDir, "diagnose", "---\nname: diagnose\ndescription: Diagnose runtime issues\n---\nRun diagnostics.")

	// Execute via "agent skills reload" and verify Added is reported (not swallowed by pre-dispatch reload)
	out = renderShellCommand(t, sh, repl.Command{Name: CommandName, Args: []string{"skills", "reload"}})
	assert.Contains(t, out, "Added (1)")
	assert.Contains(t, out, "diagnose")
	assert.Contains(t, out, "Diagnose runtime issues")

	// Update the skill
	writeTestSkill(t, skillsDir, "diagnose", "---\nname: diagnose\ndescription: Updated diagnostics\n---\nRun updated diagnostics.")
	out = renderShellCommand(t, sh, repl.Command{Name: CommandName, Args: []string{"skills", "reload"}})
	assert.Contains(t, out, "Updated (1)")
	assert.Contains(t, out, "diagnose")
	assert.Contains(t, out, "Updated diagnostics")

	// Add a malformed skill
	writeTestSkill(t, skillsDir, "broken", "---\nname: broken\n---\nmissing description")
	out = renderShellCommand(t, sh, repl.Command{Name: CommandName, Args: []string{"skills", "reload"}})
	assert.Contains(t, out, "Errors (1)")
	assert.Contains(t, out, "broken")
	assert.Contains(t, out, "description")

	// Drop the valid skill
	require.NoError(t, os.RemoveAll(filepath.Join(skillsDir, "diagnose")))
	out = renderShellCommand(t, sh, repl.Command{Name: CommandName, Args: []string{"skills", "reload"}})
	assert.Contains(t, out, "Dropped (1)")
	assert.Contains(t, out, "diagnose")
}

func TestAgentShell_SkillsReload_SurvivesIntermediateReloads(t *testing.T) {
	skillsDir := t.TempDir()
	sh, _ := newSkillsTestShell(t, []string{skillsDir})

	// Initial reload
	out := renderShellCommand(t, sh, repl.Command{Name: CommandName, Args: []string{"skills", "reload"}})
	assert.Contains(t, out, "No skill changes detected.")

	// Drop a new skill on disk
	writeTestSkill(t, skillsDir, "telemetry", "---\nname: telemetry\ndescription: Telemetry agent\n---\nCollect metrics.")

	// Simulate intermediate background agent loop reloads
	for range 5 {
		sh.skillRegistry.Reload()
	}

	// Execute an unrelated command which runs HandleCommand's pre-dispatch Reload()
	_ = renderShellCommand(t, sh, repl.Command{Name: CommandName, Args: []string{"chats", "list"}})

	// Now run skills reload: Added (1) MUST be reported despite intermediate reloads
	out = renderShellCommand(t, sh, repl.Command{Name: CommandName, Args: []string{"skills", "reload"}})
	assert.Contains(t, out, "Added (1)")
	assert.Contains(t, out, "telemetry")
	assert.Contains(t, out, "Telemetry agent")

	// Immediate subsequent reload should detect no changes
	out = renderShellCommand(t, sh, repl.Command{Name: CommandName, Args: []string{"skills", "reload"}})
	assert.Contains(t, out, "No skill changes detected.")
}

func TestAgentShell_SkillsAddDir_Command(t *testing.T) {
	sh, _ := newSkillsTestShell(t, nil)
	newDir := t.TempDir()

	writeTestSkill(t, newDir, "valid-skill", "---\nname: valid-skill\ndescription: A valid skill\n---\nValid body.")
	writeTestSkill(t, newDir, "broken-skill", "---\nname: broken-skill\n---\nMissing description.")

	out := renderShellCommand(t, sh, repl.Command{Name: CommandName, Args: []string{"skills", "add-dir", newDir}})
	assert.Contains(t, out, "Added directory")
	assert.Contains(t, out, newDir)
	assert.Contains(t, out, "Discovered 1 skill(s):")
	assert.Contains(t, out, "valid-skill")
	assert.Contains(t, out, "Errors (1)")
	assert.Contains(t, out, "broken-skill")
	assert.Contains(t, out, "description")
}

func TestAgentShell_Skills_TabCompletion(t *testing.T) {
	skillsDir := t.TempDir()
	sh, _ := newSkillsTestShell(t, []string{skillsDir})
	ctx := context.Background()

	// Subcommands under skills
	it, err := sh.Complete(ctx, "skills", []string{""})
	require.NoError(t, err)
	got, err := iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	assert.Equal(t, []string{"add-dir", "list", "list-dirs", "reload", "remove-dir", "show"}, got)

	// Prefix "re" completes to reload and remove-dir
	it, err = sh.Complete(ctx, "skills", []string{"re"})
	require.NoError(t, err)
	got, err = iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	assert.Equal(t, []string{"reload", "remove-dir"}, got)

	// Nested via "agent skills re"
	it, err = sh.Complete(ctx, CommandName, []string{"skills", "re"})
	require.NoError(t, err)
	got, err = iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	assert.Equal(t, []string{"reload", "remove-dir"}, got)
}

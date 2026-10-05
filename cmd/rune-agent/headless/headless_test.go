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

package headless

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunRequiresInstructionsFile(t *testing.T) {
	_, err := Run(context.Background(), Options{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "instructions file is required")
}

func TestRunRejectsUnreadableInstructions(t *testing.T) {
	_, err := Run(context.Background(), Options{
		InstructionsFile: filepath.Join(t.TempDir(), "missing.md"),
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "read instructions")
}

func TestRunRequiresPluginEnvironment(t *testing.T) {
	t.Setenv("RUNE_SOCKET", "")
	t.Setenv("RUNE_DATADIR", "")
	dir := t.TempDir()
	path := filepath.Join(dir, "instructions.md")
	require.NoError(t, os.WriteFile(path, []byte("do it"), 0o600))

	_, err := Run(context.Background(), Options{InstructionsFile: path})

	assert.ErrorIs(t, err, ErrNotInRune)
}

func TestRunLeavesWorkspaceUnmodified(t *testing.T) {
	t.Setenv("RUNE_SOCKET", "")
	t.Setenv("RUNE_DATADIR", "")
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(workspace, "main.go"), []byte("package main\n"), 0o600))
	instructions := filepath.Join(t.TempDir(), "instructions.md")
	require.NoError(t, os.WriteFile(instructions, []byte("do it"), 0o600))

	before := treeDigest(t, workspace)
	chdir(t, workspace)

	var out strings.Builder
	_, err := Run(context.Background(), Options{
		InstructionsFile: instructions, Stdout: &out,
	})

	require.Error(t, err)
	assert.Equal(t, before, treeDigest(t, workspace))
	assert.Empty(t, out.String())
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	prev, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { require.NoError(t, os.Chdir(prev)) })
}

// treeDigest fingerprints every path and file body under root.
func treeDigest(t *testing.T, root string) string {
	t.Helper()
	var entries []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if d.IsDir() {
			entries = append(entries, "d "+rel)
			return nil
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		entries = append(entries,
			fmt.Sprintf("f %s %x", rel, sha256.Sum256(body)))
		return nil
	})
	require.NoError(t, err)
	sort.Strings(entries)
	sum := sha256.Sum256([]byte(strings.Join(entries, "\n")))
	return hex.EncodeToString(sum[:])
}

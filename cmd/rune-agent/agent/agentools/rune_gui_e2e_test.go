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

//go:build e2e && (darwin || linux)

package agentools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
)

func TestRuneGUIDrivesRune(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "rune")
	out, err := exec.Command("go", "build", "-o", bin, "../../../rune").CombinedOutput()
	require.NoError(t, err, "go build rune: %s", out)
	base, host, ws := filepath.Join(dir, "gui"), filepath.Join(dir, "host"), filepath.Join(dir, "ws")
	require.NoError(t, os.MkdirAll(ws, 0o755))
	// Plain text has no language package, whose install prompt would take
	// the keys.
	notes := filepath.Join(ws, "notes.txt")
	require.NoError(t, os.WriteFile(notes, []byte("package main\n"), 0o644))
	tool := NewRuneGUI(nil, bin, base, host, "", "e2e")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	run := func(command ...string) string {
		t.Helper()
		result := executeRuneGUI(t, tool, ctx, command...)
		require.False(t, result.IsError, result.Content)
		return result.Content
	}
	run("start", "--workspace", ws, "--size", "400x300")
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("log:\n%s", executeRuneGUI(t, tool, ctx, "log", "200").Content)
		}
	})
	assert.Equal(t, "ok", run("exec", "workspaceready"))
	assert.Equal(t, "ok", run("exec", "edit notes.txt"))
	assert.Equal(t, "ok", run("keys", "ix<esc>"))
	assert.Equal(t, "ok", run("exec", "write"))
	got, err := os.ReadFile(notes)
	require.NoError(t, err)
	assert.Equal(t, "xpackage main\n", string(got))

	shot := executeRuneGUI(t, tool, ctx, "screenshot")
	require.False(t, shot.IsError, shot.Content)
	assert.Contains(t, shot.Content, "800x600")
	require.Len(t, shot.MultiContent, 2)
	assert.Equal(t, llmapi.ContentPartTypeImageURL, shot.MultiContent[1].Type)
	entries, err := os.ReadDir(filepath.Join(base, "rune-e2e", "screenshots"))
	require.NoError(t, err)
	assert.Empty(t, entries, "screenshots are removed once returned")

	failed := executeRuneGUI(t, tool, ctx, "exec", "nosuchcommand")
	assert.True(t, failed.IsError)
	assert.Contains(t, failed.Content, "nosuchcommand")
	assert.NotEqual(t, "(no output)", run("log"))
	assert.Equal(t, "Stopped Rune.", run("stop"))

	tool.Close()
	require.Eventually(t, func() bool {
		_, err := os.Stat(filepath.Join(base, "rune-e2e"))
		return os.IsNotExist(err)
	}, 15*time.Second, 100*time.Millisecond, "closing the chat removes the instance's data")
}

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

package texttest_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/ide/syntax"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/texttest"
	"unstable.build/rune/internal/workspace"
)

func TestSyntaxSizeGuardSkipsTreeForLargeBuffers(t *testing.T) {
	dir := t.TempDir()
	wsURI, err := workspaceapi.ParseURI("file://" + dir)
	require.NoError(t, err)

	scheme, err := workspace.NewFileScheme(
		context.Background(), config.NopConfig(), wsURI)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })

	ws := workspace.NewSchemeWorkspace(wsURI, scheme, inlineSchedule)

	const threshold = 1024 // 1 KiB
	cfg := text.DefaultConfig()
	cfg.ScheduleNextTick = inlineSchedule
	cfg.MaxSyntaxParseSize = threshold
	c, err := text.NewComponent(texttest.NopEditor(), ws, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	// Small file (well below threshold) — tree should be installed.
	smallPath := filepath.Join(dir, "small.txt")
	require.NoError(t, os.WriteFile(smallPath, []byte("hello\n"), 0o644))
	smallURI, err := workspaceapi.ParseURI("file://" + smallPath)
	require.NoError(t, err)

	_, err = c.OpenFileTab(smallURI, false)
	require.NoError(t, err)
	smallEd, err := c.Editor(smallURI)
	require.NoError(t, err)
	_, smallHasTree := smallEd.CellView().(*syntax.Tree)
	assert.True(t, smallHasTree,
		"buffers below MaxSyntaxParseSize must have a syntax tree installed")

	// Large file (well above threshold) — tree must be skipped.
	largePath := filepath.Join(dir, "large.txt")
	require.NoError(t, os.WriteFile(
		largePath, []byte(strings.Repeat("x", threshold*4)+"\n"), 0o644))
	largeURI, err := workspaceapi.ParseURI("file://" + largePath)
	require.NoError(t, err)

	_, err = c.OpenFileTab(largeURI, false)
	require.NoError(t, err)
	largeEd, err := c.Editor(largeURI)
	require.NoError(t, err)
	_, largeHasTree := largeEd.CellView().(*syntax.Tree)
	assert.False(t, largeHasTree,
		"buffers above MaxSyntaxParseSize must NOT have a syntax tree installed")
}

func TestSyntaxSizeGuardZeroDisablesGuard(t *testing.T) {
	dir := t.TempDir()
	wsURI, err := workspaceapi.ParseURI("file://" + dir)
	require.NoError(t, err)

	scheme, err := workspace.NewFileScheme(
		context.Background(), config.NopConfig(), wsURI)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })

	ws := workspace.NewSchemeWorkspace(wsURI, scheme, inlineSchedule)

	cfg := text.DefaultConfig()
	cfg.ScheduleNextTick = inlineSchedule
	cfg.MaxSyntaxParseSize = 0
	c, err := text.NewComponent(texttest.NopEditor(), ws, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	fpath := filepath.Join(dir, "huge.txt")
	require.NoError(t, os.WriteFile(
		fpath, []byte(strings.Repeat("y", 64*1024)+"\n"), 0o644))
	fileURI, err := workspaceapi.ParseURI("file://" + fpath)
	require.NoError(t, err)

	_, err = c.OpenFileTab(fileURI, false)
	require.NoError(t, err)
	ed, err := c.Editor(fileURI)
	require.NoError(t, err)
	_, hasTree := ed.CellView().(*syntax.Tree)
	assert.True(t, hasTree,
		"MaxSyntaxParseSize=0 must behave as no limit")
}

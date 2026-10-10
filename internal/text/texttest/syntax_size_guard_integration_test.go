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
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/ide/syntax"
	"unstable.build/rune/internal/ide/syntax/treesitter"
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
	cfg.SyntaxTree = treesitter.New
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
	_, smallHasTree := smallEd.CellView().(*treesitter.Tree)
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
	_, largeHasTree := largeEd.CellView().(*treesitter.Tree)
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
	cfg.SyntaxTree = treesitter.New
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
	_, hasTree := ed.CellView().(*treesitter.Tree)
	assert.True(t, hasTree,
		"MaxSyntaxParseSize=0 must behave as no limit")
}

const fakeSyntaxTreeCommand = "faketreecmd"

type fakeSyntaxTree struct {
	workspace.FlusherCloser
	uri        workspaceapi.URI
	config     syntax.Config
	dispatched []workspaceapi.URI
	closed     bool
}

func (f *fakeSyntaxTree) Commands(syntax.Handler) ([]textapi.CommandManual, syntax.CommandHandler) {
	return []textapi.CommandManual{{Name: fakeSyntaxTreeCommand}}, f
}

func (f *fakeSyntaxTree) HandleCommand(_ context.Context, cmd textapi.Command) error {
	f.dispatched = append(f.dispatched, cmd.URI)
	return nil
}

func (f *fakeSyntaxTree) Complete(context.Context, textapi.Command) (
	iterator.Iterator[string], string, error,
) {
	return iterator.Empty[string](), "", nil
}

func (f *fakeSyntaxTree) Close() error {
	f.closed = true
	return f.FlusherCloser.Close()
}

func TestSyntaxTreeHook(t *testing.T) {
	const maxParseSize = 1024
	small := "hello\n"
	large := strings.Repeat("x", maxParseSize*4) + "\n"

	tests := []struct {
		name     string
		hook     bool
		content  string
		wantTree bool
	}{
		{name: "nil hook installs no tree", content: small},
		{name: "hook installs the tree of every tab", hook: true, content: small, wantTree: true},
		{name: "hook is skipped above MaxSyntaxParseSize", hook: true, content: large},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
			cfg.MaxSyntaxParseSize = maxParseSize
			cfg.Syntax.StrictErrors = true
			cfg.Syntax.CaptureNamesAttributes = map[string]term.Attributes{
				"keyword": {Fg: term.ColorGreen},
			}
			var trees []*fakeSyntaxTree
			if tt.hook {
				cfg.SyntaxTree = func(
					_ context.Context, _ browserapi.Notifications, _ term.Interrupter,
					_ syntax.PkgManager, _ syntax.LocationSetter,
					uri workspaceapi.URI, _ *cell.Buffer, fc workspace.FlusherCloser,
					_ syntax.Opener, config syntax.Config,
				) syntax.Tree {
					tree := &fakeSyntaxTree{FlusherCloser: fc, uri: uri, config: config}
					trees = append(trees, tree)
					return tree
				}
			}
			c, err := text.NewComponent(texttest.NopEditor(), ws, cfg)
			require.NoError(t, err)

			var uris []workspaceapi.URI
			for _, name := range []string{"a.go", "b.go"} {
				path := filepath.Join(dir, name)
				require.NoError(t, os.WriteFile(path, []byte(tt.content), 0o644))
				uri, err := workspaceapi.ParseURI("file://" + path)
				require.NoError(t, err)
				_, err = c.OpenFileTab(uri, false)
				require.NoError(t, err)
				uris = append(uris, uri)
			}

			for _, uri := range uris {
				handled, err := c.DispatchCommand(context.Background(), textapi.Command{
					Name:   fakeSyntaxTreeCommand,
					Window: c.Browser().Focus(),
					URI:    uri,
				})
				require.NoError(t, err)
				assert.Equal(t, tt.wantTree, handled)
			}

			require.NoError(t, c.Close())

			if !tt.wantTree {
				assert.Empty(t, trees)
				return
			}
			require.Len(t, trees, len(uris))
			for i, tree := range trees {
				assert.Equal(t, uris[i], tree.uri)
				assert.True(t, tree.config.StrictErrors)
				assert.Equal(t, cfg.Syntax.CaptureNamesAttributes, tree.config.CaptureNamesAttributes)
				assert.Equal(t, []workspaceapi.URI{uris[i]}, tree.dispatched,
					"a tree's commands must serve only its own tab")
				assert.True(t, tree.closed, "the tree must replace the tab's FlusherCloser")
			}
		})
	}
}

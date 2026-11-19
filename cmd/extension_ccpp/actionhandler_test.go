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

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/ide/idelsp/lspcmd"
)

// actionFixture is a `c` or `cpp` command-prompt handler over fakes.
type actionFixture struct {
	lsp     *captureLSP
	editor  *fakeEditor
	wm      *fakeWM
	notify  *fakeNotifications
	opener  *fakeOpener
	manual  textapi.CommandManual
	handler textapi.CommandHandler
	uri     workspaceapi.URI
	res     *stubResource
}

func newActionFixture(t *testing.T, name string) *actionFixture {
	t.Helper()
	uri, err := workspaceapi.ParseURI("file:///ws/src/main.cpp")
	require.NoError(t, err)
	f := &actionFixture{
		lsp:    &captureLSP{requests: map[string]json.RawMessage{}},
		editor: &fakeEditor{},
		wm:     &fakeWM{},
		notify: newFakeNotifications(),
		opener: &fakeOpener{},
		uri:    uri,
		res:    &stubResource{uri: uri},
	}
	f.editor.register(f.res)
	f.manual, f.handler = newActionHandler(name, actionDeps{
		lsp: f.lsp, editor: f.editor, wm: f.wm, notify: f.notify, opener: f.opener,
		sel: lspcmd.NewSelectionTracker(),
	})
	return f
}

// cmd builds the command the prompt dispatches for `<name> <sub>` with
// the cursor at line y, column x of the fixture's file.
func (f *actionFixture) cmd(sub string, y, x int, args ...string) textapi.Command {
	c := textapi.Command{
		Name:     f.manual.Name,
		Args:     append([]string{sub}, args...),
		URI:      f.uri,
		Resource: f.res,
	}
	c.Cursor.Content = term.Coordinates{Y: y, X: x}
	return c
}

func TestActionRouterRejectsForeignAndUnknown(t *testing.T) {
	f := newActionFixture(t, cActionCmdName)
	err := f.handler.HandleCommand(context.Background(), textapi.Command{Name: "rust"})
	require.Error(t, err)

	err = f.handler.HandleCommand(context.Background(), textapi.Command{Name: cActionCmdName})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing subcommand")

	err = f.handler.HandleCommand(context.Background(), f.cmd("frobnicate", 0, 0))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown c subcommand")

	err = f.handler.HandleCommand(context.Background(), f.cmd("expand-auto", 0, 0))
	require.Error(t, err, "C++-only tweaks stay off the c command")
}

func TestActionRouterCompleteDocumentsEveryRoute(t *testing.T) {
	for _, name := range []string{cActionCmdName, cppActionCmdName} {
		t.Run(name, func(t *testing.T) {
			f := newActionFixture(t, name)
			ctx := context.Background()
			it, err := f.handler.Complete(ctx, name, []string{""})
			require.NoError(t, err)
			got, err := iterator.ToSlice(ctx, it)
			require.NoError(t, err)

			documented := make([]string, 0, len(f.manual.Commands))
			for _, c := range f.manual.Commands {
				documented = append(documented, c.Name)
			}
			sort.Strings(documented)
			assert.Equal(t, documented, got, "the manual must document every route")
			assert.Contains(t, got, "switch")
			assert.Contains(t, got, "extract-function")
			assert.Equal(t, name == cppActionCmdName, slices.Contains(got, "scopify-enum"),
				"C++-only tweaks are routed by cpp alone")

			_, err = f.handler.Complete(ctx, "rust", []string{""})
			require.Error(t, err)
			it, err = f.handler.Complete(ctx, name, nil)
			require.NoError(t, err)
			got, err = iterator.ToSlice(ctx, it)
			require.NoError(t, err)
			assert.Empty(t, got)
		})
	}
}

func TestActionRouterRefreshesZeroCursor(t *testing.T) {
	f := newActionFixture(t, cppActionCmdName)
	require.NoError(t, f.editor.SetCursor(f.res, term.Coordinates{Y: 7, X: 3}))
	err := f.handler.HandleCommand(context.Background(), f.cmd("symbol-info", 0, 0))
	require.NoError(t, err)
	f.lsp.mu.Lock()
	defer f.lsp.mu.Unlock()
	require.Len(t, f.lsp.requested, 1)
	var params semanticapi.TextDocumentPositionParams
	require.NoError(t, json.Unmarshal(f.lsp.requested[0].Params, &params))
	assert.Equal(t, semanticapi.Position{Line: 7, Character: 3}, params.Position,
		"a zero cursor snapshot is replaced by the live cursor")
	assert.Equal(t, ccppLanguageID, f.lsp.requested[0].ServerID)
}

func TestTweakSubcommandsMatchByTitle(t *testing.T) {
	tweakCmd := semanticapi.Command{Command: "clangd.applyTweak"}
	offered := []semanticapi.CodeAction{
		{Title: "Extract to function", Kind: "refactor", Command: &tweakCmd},
		{Title: "Expand macro 'MAX'", Kind: "refactor", Command: &tweakCmd},
		{Title: "Populate switch", Kind: "quickfix", Command: &tweakCmd},
		{Title: "Replace with deduced type", Kind: "refactor", Command: &tweakCmd},
	}
	cases := []struct {
		name     string
		sub      string
		wantRun  bool
		wantHint string
	}{
		{name: "exact title", sub: "extract-function", wantRun: true},
		{name: "title with the symbol embedded", sub: "expand-macro", wantRun: true},
		{name: "quickfix-kinded tweak", sub: "populate-switch", wantRun: true},
		{name: "deduced type", sub: "expand-auto", wantRun: true},
		{name: "not offered here", sub: "define-outline",
			wantHint: "Place the cursor on the name of a function defined inside its declaration"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newActionFixture(t, cppActionCmdName)
			f.lsp.codeActions = offered
			err := f.handler.HandleCommand(context.Background(), f.cmd(tc.sub, 2, 4))
			require.NoError(t, err)
			if tc.wantRun {
				assert.Equal(t, []string{"clangd.applyTweak"}, f.lsp.executedCommands(),
					"a lone matching tweak runs without a picker")
				assert.Nil(t, f.wm.floated())
				return
			}
			assert.Empty(t, f.lsp.executedCommands())
			assert.True(t, f.notify.hasMessage(tc.wantHint), "got %v", f.notify.notifMessages())
		})
	}
}

func TestListShowsPickerForSeveralActions(t *testing.T) {
	tweakCmd := semanticapi.Command{Command: "clangd.applyTweak"}
	f := newActionFixture(t, cActionCmdName)
	f.lsp.codeActions = []semanticapi.CodeAction{
		{Title: "Extract to function", Kind: "refactor", Command: &tweakCmd},
		{Title: "Swap operands to !=", Kind: "refactor", Command: &tweakCmd},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.handler.HandleCommand(ctx, f.cmd("list", 2, 4)) }()
	require.Eventually(t, func() bool { return f.wm.floated() != nil }, timeout, tick,
		"two actions must go through the picker")
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	assert.Empty(t, f.lsp.executedCommands())
}

func TestExpandMacroInsideDefineExplainsWithoutAskingClangd(t *testing.T) {
	const src = "#include \"util.h\"\n" +
		"#define SQUARE(x) ((x) * (x))\n" +
		"# define CUBE(x)   \\\n" +
		"  (SQUARE(x) *     \\\n" +
		"   (x))\n" +
		"const char *s = \"a\\\n" +
		"b\";\n" +
		"int f(int x) { return CUBE(x); }\n"
	const (
		cubeDefined = "`CUBE` is defined here, not used; clangd expands a macro where it is " +
			"used. Place the cursor on a use of `CUBE`."
		insideCube = "`%s` sits in the definition of `CUBE`, where nothing is expanded; clangd " +
			"expands a macro where it is used. Place the cursor on a use of `CUBE`, or of `%s` " +
			"outside a `#define`."
	)
	tests := []struct {
		name string
		y, x int
		hint string
	}{
		{"the name on its define line", 1, 8,
			"`SQUARE` is defined here, not used; clangd expands a macro where it is used. " +
				"Place the cursor on a use of `SQUARE`."},
		{"the directive keyword, spaced from the hash", 2, 3, cubeDefined},
		{"a parameter on the define line", 2, 14, fmt.Sprintf(insideCube, "x", "x")},
		{"a macro in a continuation line", 3, 3, fmt.Sprintf(insideCube, "SQUARE", "SQUARE")},
		{"just past a macro in a continuation line", 3, 9, fmt.Sprintf(insideCube, "SQUARE", "SQUARE")},
		{"whitespace on the last continuation line", 4, 1, cubeDefined},
		{"a use in code", 7, 22, ""},
		{"a backslash-continued string literal", 6, 0, ""},
		{"past the end of the file", 42, 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newActionFixture(t, cActionCmdName)
			f.editor.seed(f.res, src)
			require.NoError(t, f.handler.HandleCommand(context.Background(), f.cmd("expand-macro", tt.y, tt.x)))
			if tt.hint == "" {
				assert.Equal(t, 1, f.lsp.codeActionCalls(), "outside a directive clangd decides")
				assert.Equal(t, []string{"Place the cursor on a macro name. " + clangdNoActionHint},
					f.notify.notifMessages())
				return
			}
			assert.Zero(t, f.lsp.codeActionCalls(), "a directive holds no expansion clangd could offer")
			assert.Equal(t, []string{tt.hint}, f.notify.notifMessages())
		})
	}
}

func TestActionsWithNoFileOpen(t *testing.T) {
	f := newActionFixture(t, cActionCmdName)
	for _, sub := range []string{"switch", "ast", "symbol-info", "list"} {
		err := f.handler.HandleCommand(context.Background(),
			textapi.Command{Name: cActionCmdName, Args: []string{sub}})
		require.Error(t, err, sub)
		assert.Contains(t, err.Error(), "no file open", sub)
	}
}

const (
	timeout = 5 * time.Second
	tick    = 10 * time.Millisecond
)

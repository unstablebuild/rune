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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/ide/idelsp/lspcmd"
)

// actionEnv is the `ts` command-prompt handler wired to fakes.
type actionEnv struct {
	lsp    *requestLSP
	editor *fakeEditor
	wm     *fakeWM
	opener *recordingOpener
	notify *fakeNotifications
	router textapi.CommandHandler
	manual textapi.CommandManual
}

func newActionEnv(t *testing.T, lsp *requestLSP, developer bool) *actionEnv {
	t.Helper()
	if lsp.answers == nil {
		lsp.answers = map[string][]answer{}
	}
	env := &actionEnv{
		lsp: lsp, editor: &fakeEditor{}, wm: &fakeWM{},
		opener: &recordingOpener{}, notify: &fakeNotifications{},
	}
	env.manual, env.router = newTSActionHandler(lsp, env.editor, env.wm, env.notify, env.opener,
		lspcmd.NewSelectionTracker(), "/ws", developer)
	return env
}

func testURI(t *testing.T, path string) workspaceapi.URI {
	t.Helper()
	uri, err := workspaceapi.ParseURI("file://" + path)
	require.NoError(t, err)
	return uri
}

// tsCmd is the `ts <sub> <args...>` command issued with the cursor at
// (line, char) of the file at path.
func tsCmd(t *testing.T, path string, line, char int, sub string, args ...string) textapi.Command {
	t.Helper()
	uri := testURI(t, path)
	cmd := textapi.Command{
		Name:     tsActionCmdName,
		Args:     append([]string{sub}, args...),
		URI:      uri,
		Resource: &stubResource{uri: uri},
	}
	cmd.Cursor.Content = term.Coordinates{X: char, Y: line}
	return cmd
}

func TestTSActionRouterDispatch(t *testing.T) {
	env := newActionEnv(t, &requestLSP{}, false)
	ctx := context.Background()

	err := env.router.HandleCommand(ctx, textapi.Command{Name: "lsp", Args: []string{"list"}})
	require.EqualError(t, err, "unknown command: lsp")
	err = env.router.HandleCommand(ctx, textapi.Command{Name: tsActionCmdName})
	require.EqualError(t, err, "missing subcommand")
	err = env.router.HandleCommand(ctx, tsCmd(t, "/ws/a.ts", 0, 0, "frobnicate"))
	require.EqualError(t, err, "unknown ts subcommand: frobnicate")
	err = env.router.HandleCommand(ctx, tsCmd(t, "/ws/a.ts", 0, 0, "gc"))
	require.EqualError(t, err, "unknown ts subcommand: gc", "profiling needs developer")
}

func TestTSActionRouterRefreshesLostCursor(t *testing.T) {
	lsp := &requestLSP{answers: map[string][]answer{
		"custom/textDocument/sourceDefinition": {{raw: "null"}, {raw: "null"}},
	}}
	env := newActionEnv(t, lsp, false)
	env.editor.live = term.Coordinates{X: 7, Y: 3}

	require.NoError(t, env.router.HandleCommand(context.Background(),
		tsCmd(t, "/ws/a.ts", 0, 0, "source-definition")))
	require.NoError(t, env.router.HandleCommand(context.Background(),
		tsCmd(t, "/ws/a.ts", 1, 2, "source-definition")))

	reqs := lsp.recorded()
	require.Len(t, reqs, 2)
	assert.JSONEq(t, `{"textDocument":{"uri":"file:///ws/a.ts"},"position":{"line":3,"character":7}}`,
		string(reqs[0].Params), "a zero cursor snapshot uses the live cursor")
	assert.JSONEq(t, `{"textDocument":{"uri":"file:///ws/a.ts"},"position":{"line":1,"character":2}}`,
		string(reqs[1].Params))
}

func TestTSActionRouterComplete(t *testing.T) {
	ctx := context.Background()
	complete := func(r textapi.CommandHandler, cmd string, args ...string) ([]string, error) {
		it, err := r.Complete(ctx, cmd, args)
		if err != nil {
			return nil, err
		}
		return iterator.ToSlice(ctx, it)
	}

	base := []string{
		"fix-all", "list", "open-tsconfig", "organize-imports", "quickfix",
		"remove-unused-imports", "sort-imports", "source-definition",
	}
	got, err := complete(newActionEnv(t, &requestLSP{}, false).router, tsActionCmdName, "")
	require.NoError(t, err)
	assert.Equal(t, base, got)

	dev := newActionEnv(t, &requestLSP{}, true)
	got, err = complete(dev.router, tsActionCmdName, "")
	require.NoError(t, err)
	assert.Equal(t, []string{
		"alloc-profile", "cpu-profile", "fix-all", "gc", "heap-profile", "list", "open-tsconfig",
		"organize-imports", "quickfix", "remove-unused-imports", "sort-imports", "source-definition",
	}, got)
	require.Len(t, dev.manual.Commands, len(got), "every subcommand has a manual entry")

	got, err = complete(dev.router, tsActionCmdName, "cpu-profile", "st")
	require.NoError(t, err)
	assert.Equal(t, []string{"start", "stop"}, got)

	_, err = complete(dev.router, "lsp", "")
	require.EqualError(t, err, "unknown command: lsp")
}

func TestCodeActionLSP(t *testing.T) {
	diag := func(line, from, to uint32) semanticapi.Diagnostic {
		return semanticapi.Diagnostic{
			Range: semanticapi.Range{
				Start: semanticapi.Position{Line: line, Character: from},
				End:   semanticapi.Position{Line: line, Character: to},
			},
			CodeInt: 2304, CodeIsInt: true,
		}
	}
	cursor := func(line, char uint32) semanticapi.Range {
		p := semanticapi.Position{Line: line, Character: char}
		return semanticapi.Range{Start: p, End: p}
	}
	pulled := []semanticapi.Diagnostic{diag(0, 4, 9), diag(2, 0, 3), diag(5, 1, 2)}
	tests := []struct {
		name      string
		only      []semanticapi.CodeActionKind
		rng       semanticapi.Range
		sent      []semanticapi.Diagnostic
		pullErr   error
		wantPull  bool
		wantDiags []semanticapi.Diagnostic
	}{
		{
			name: "quick fixes get the diagnostics under the cursor",
			only: []semanticapi.CodeActionKind{"quickfix"}, rng: cursor(0, 6),
			wantPull: true, wantDiags: []semanticapi.Diagnostic{diag(0, 4, 9)},
		},
		{
			name: "a cursor at the end of a diagnostic touches it",
			only: []semanticapi.CodeActionKind{"quickfix"}, rng: cursor(2, 3),
			wantPull: true, wantDiags: []semanticapi.Diagnostic{diag(2, 0, 3)},
		},
		{
			name: "a selection collects every diagnostic it overlaps",
			rng: semanticapi.Range{
				Start: semanticapi.Position{Line: 0, Character: 8},
				End:   semanticapi.Position{Line: 2, Character: 0},
			},
			wantPull: true, wantDiags: []semanticapi.Diagnostic{diag(0, 4, 9), diag(2, 0, 3)},
		},
		{
			name: "no diagnostic under the cursor",
			only: []semanticapi.CodeActionKind{"quickfix"}, rng: cursor(1, 0),
			wantPull: true, wantDiags: []semanticapi.Diagnostic{},
		},
		{
			name: "a failed pull still asks for actions",
			only: []semanticapi.CodeActionKind{"quickfix"}, rng: cursor(0, 6),
			pullErr: errors.New("no server"), wantPull: true, wantDiags: []semanticapi.Diagnostic{},
		},
		{
			name: "source actions need no diagnostics",
			only: []semanticapi.CodeActionKind{"source.organizeImports"}, rng: cursor(0, 6),
		},
		{
			name: "diagnostics the caller sent are kept",
			only: []semanticapi.CodeActionKind{"quickfix"}, rng: cursor(0, 6),
			sent:      []semanticapi.Diagnostic{diag(9, 0, 1)},
			wantDiags: []semanticapi.Diagnostic{diag(9, 0, 1)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inner := &requestLSP{diagnostics: pulled, diagErr: tt.pullErr}
			_, err := codeActionLSP{LSP: inner}.CodeAction(context.Background(), semanticapi.CodeActionParams{
				TextDocument: semanticapi.TextDocumentIdentifier{URI: "file:///ws/a.ts"},
				Range:        tt.rng,
				Context:      semanticapi.CodeActionContext{Only: tt.only, Diagnostics: tt.sent},
			})
			require.NoError(t, err)
			assert.Equal(t, tt.wantPull, inner.pulls == 1)
			require.Len(t, inner.actionParams, 1)
			assert.Equal(t, tt.wantDiags, inner.actionParams[0].Context.Diagnostics)
		})
	}
}

func TestTSActionCodeActions(t *testing.T) {
	const uri = "file:///ws/a.ts"
	organize := func(e *semanticapi.WorkspaceEdit) []semanticapi.CodeActionResult {
		kind := semanticapi.CodeActionKind("source.organizeImports")
		return []semanticapi.CodeActionResult{{CodeAction: &semanticapi.CodeAction{
			Title: "Organize Imports", Kind: kind, Edit: e,
		}}}
	}
	tests := []struct {
		name       string
		sub        string
		actions    []semanticapi.CodeActionResult
		wantKind   semanticapi.CodeActionKind
		wantNotify []string
	}{
		{
			name:       "already organized",
			sub:        "organize-imports",
			actions:    organize(&semanticapi.WorkspaceEdit{Changes: map[string][]semanticapi.TextEdit{}}),
			wantKind:   "source.organizeImports",
			wantNotify: []string{"Imports are already organized"},
		},
		{
			name:       "nothing unused",
			sub:        "remove-unused-imports",
			wantKind:   "source.removeUnusedImports",
			wantNotify: []string{"No unused imports"},
		},
		{
			name:       "already sorted",
			sub:        "sort-imports",
			wantKind:   "source.sortImports",
			wantNotify: []string{"Imports are already sorted"},
		},
		{
			name:       "nothing to fix",
			sub:        "fix-all",
			wantKind:   "source.fixAll",
			wantNotify: []string{"No automatic fixes available"},
		},
		{
			name:       "no quick fix",
			sub:        "quickfix",
			wantKind:   "quickfix",
			wantNotify: []string{"No quick fix available. Place the cursor on a diagnostic to see its fixes"},
		},
		{
			name:       "nothing at the cursor",
			sub:        "list",
			wantNotify: []string{"No code actions available at the cursor"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lsp := &requestLSP{actions: tt.actions}
			env := newActionEnv(t, lsp, false)
			require.NoError(t, env.router.HandleCommand(context.Background(), tsCmd(t, "/ws/a.ts", 0, 0, tt.sub)))
			require.Len(t, lsp.actionParams, 1)
			assert.Equal(t, uri, lsp.actionParams[0].TextDocument.URI)
			if tt.wantKind == "" {
				assert.Empty(t, lsp.actionParams[0].Context.Only)
			} else {
				assert.Equal(t, []semanticapi.CodeActionKind{tt.wantKind}, lsp.actionParams[0].Context.Only)
			}
			assert.Equal(t, tt.wantNotify, env.notify.messages())
		})
	}
}

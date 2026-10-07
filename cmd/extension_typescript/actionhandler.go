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
	"fmt"
	"log/slog"
	"slices"
	"sort"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/ide/idelsp/lspcmd"
)

// tsActionCmdName is the command-prompt command. It shares the `ts`
// word with the console command, which lives in a separate registry.
const tsActionCmdName = "ts"

// newTSActionHandler builds the `ts` command-prompt handler: tsgo's code
// actions and its custom/* requests. developer adds the profiling
// commands, which only someone working on tsgo itself needs. wsDir
// anchors a relative profile directory.
func newTSActionHandler(
	lsp semanticapi.LSP, editor textapi.Editor,
	wm browserapi.WindowManager, notify browserapi.Notifications,
	opener browserapi.ResourceOpener, sel *lspcmd.SelectionTracker,
	wsDir string, developer bool,
) (textapi.CommandManual, textapi.CommandHandler) {
	actions := codeActionLSP{LSP: lsp}
	handlers := map[string]textapi.CommandHandler{
		"organize-imports": lspcmd.CodeActionHandler(actions, editor, notify, wm, sel,
			"source.organizeImports", true, "Imports are already organized"),
		"remove-unused-imports": lspcmd.CodeActionHandler(actions, editor, notify, wm, sel,
			"source.removeUnusedImports", true, "No unused imports"),
		"sort-imports": lspcmd.CodeActionHandler(actions, editor, notify, wm, sel,
			"source.sortImports", true, "Imports are already sorted"),
		"fix-all": lspcmd.CodeActionHandler(actions, editor, notify, wm, sel,
			"source.fixAll", true, "No automatic fixes available"),
		"quickfix": lspcmd.CodeActionHandler(actions, editor, notify, wm, sel,
			"quickfix", false, "No quick fix available. Place the cursor on a diagnostic to see its fixes"),
		"list": lspcmd.CodeActionHandler(actions, editor, notify, wm, sel,
			"", false, "No code actions available at the cursor"),
		"source-definition": &sourceDefinitionCmd{
			lsp: lsp, editor: editor, wm: wm, opener: opener, notify: notify,
		},
		"open-tsconfig": &projectConfigCmd{
			lsp: lsp, editor: editor, wm: wm, opener: opener, notify: notify,
		},
	}
	manual := textapi.CommandManual{
		Name:     tsActionCmdName,
		Summary:  "TypeScript and JavaScript commands powered by tsgo",
		Synopsis: "<subcommand> [<args>...]",
		Commands: []textapi.CommandManual{
			{Name: "organize-imports", Summary: "Remove unused imports, then sort and merge the rest"},
			{Name: "remove-unused-imports", Summary: "Remove unused imports, keeping the order of the rest"},
			{Name: "sort-imports", Summary: "Sort and merge imports without removing any"},
			{Name: "fix-all", Summary: "Apply every fix tsgo can make on its own across the file"},
			{Name: "quickfix", Summary: "Apply a fix for the diagnostic at the cursor"},
			{Name: "list", Summary: "List every code action at the cursor or selection and apply the chosen one"},
			{Name: "source-definition", Summary: "Go to the JavaScript implementation of the symbol at the cursor rather than its declaration file"},
			{Name: "open-tsconfig", Summary: "Open the tsconfig.json or jsconfig.json that owns the current file"},
		},
	}
	if developer {
		handlers["cpu-profile"] = &cpuProfileCmd{lsp: lsp, notify: notify, wsDir: wsDir}
		handlers["heap-profile"] = &saveProfileCmd{lsp: lsp, notify: notify, wsDir: wsDir,
			method: "custom/saveHeapProfile", what: "Heap profile"}
		handlers["alloc-profile"] = &saveProfileCmd{lsp: lsp, notify: notify, wsDir: wsDir,
			method: "custom/saveAllocProfile", what: "Allocation profile"}
		handlers["gc"] = &gcCmd{lsp: lsp, notify: notify}
		manual.Commands = append(manual.Commands,
			textapi.CommandManual{Name: "cpu-profile", Summary: "Start or stop profiling the CPU of the TypeScript language servers", Synopsis: "start [<dir>] | stop"},
			textapi.CommandManual{Name: "heap-profile", Summary: "Save a heap profile of a TypeScript language server", Synopsis: "[<dir>]"},
			textapi.CommandManual{Name: "alloc-profile", Summary: "Save an allocation profile of a TypeScript language server", Synopsis: "[<dir>]"},
			textapi.CommandManual{Name: "gc", Summary: "Run the garbage collector of the TypeScript language servers"},
		)
	}
	return manual, &tsActionRouter{handlers: handlers, editor: editor}
}

var _ textapi.CommandHandler = (*tsActionRouter)(nil)

type tsActionRouter struct {
	handlers map[string]textapi.CommandHandler
	editor   textapi.Editor
}

func (r *tsActionRouter) HandleCommand(ctx context.Context, cmd textapi.Command) error {
	if cmd.Name != tsActionCmdName {
		return fmt.Errorf("unknown command: %s", cmd.Name)
	}
	if len(cmd.Args) == 0 {
		return fmt.Errorf("missing subcommand")
	}
	cmd.Name, cmd.Args = cmd.Args[0], cmd.Args[1:]
	h, ok := r.handlers[cmd.Name]
	if !ok {
		return fmt.Errorf("unknown ts subcommand: %s", cmd.Name)
	}
	r.refreshCursor(&cmd)
	return h.HandleCommand(ctx, cmd)
}

// refreshCursor replaces a zero cursor snapshot with the live editor
// cursor. Dispatch paths that cannot capture a cursor (a non-text window
// in focus, a stale tab handler) deliver {0,0} alongside a valid
// resource, which would run the cursor-scoped subcommands against the
// top of the file.
func (r *tsActionRouter) refreshCursor(cmd *textapi.Command) {
	if cmd.Resource == nil || cmd.Cursor.Content != (term.Coordinates{}) {
		return
	}
	if live, err := r.editor.Cursor(cmd.Resource); err == nil {
		cmd.Cursor.Content = live
	}
}

func (r *tsActionRouter) Complete(
	ctx context.Context, cmd string, args []string,
) (iterator.Iterator[string], error) {
	if cmd != tsActionCmdName {
		return nil, fmt.Errorf("unknown command: %s", cmd)
	}
	if len(args) == 0 {
		return iterator.Empty[string](), nil
	}
	if h, ok := r.handlers[args[0]]; ok {
		return h.Complete(ctx, args[0], args[1:])
	}
	names := make([]string, 0, len(r.handlers))
	for name := range r.handlers {
		names = append(names, name)
	}
	sort.Strings(names)
	return iterator.FromSlice(names), nil
}

// codeActionLSP gives a code-action request that wants quick fixes the
// pulled diagnostics overlapping its range. tsgo builds quick fixes only
// from the diagnostics a request carries, and Rune's code-action
// commands send none.
type codeActionLSP struct {
	semanticapi.LSP
}

func (l codeActionLSP) CodeAction(
	ctx context.Context, params semanticapi.CodeActionParams,
) ([]semanticapi.CodeActionResult, error) {
	if len(params.Context.Diagnostics) == 0 && wantsQuickFixes(params.Context.Only) {
		report, err := l.Diagnostic(ctx, semanticapi.DocumentDiagnosticParams{
			TextDocument: params.TextDocument,
		})
		if err != nil {
			slog.Warn("pull diagnostics for quick fixes", "uri", params.TextDocument.URI, "error", err)
			report.Items = nil
		}
		params.Context.Diagnostics = overlapping(report.Items, params.Range)
	}
	return l.LSP.CodeAction(ctx, params)
}

func wantsQuickFixes(only []semanticapi.CodeActionKind) bool {
	return len(only) == 0 || slices.Contains(only, "quickfix")
}

// overlapping returns the diagnostics whose range touches rng; a
// diagnostic ending exactly at a collapsed cursor counts.
func overlapping(diags []semanticapi.Diagnostic, rng semanticapi.Range) []semanticapi.Diagnostic {
	out := []semanticapi.Diagnostic{}
	for _, d := range diags {
		if !posLess(rng.End, d.Range.Start) && !posLess(d.Range.End, rng.Start) {
			out = append(out, d)
		}
	}
	return out
}

func posLess(a, b semanticapi.Position) bool {
	return a.Line < b.Line || (a.Line == b.Line && a.Character < b.Character)
}

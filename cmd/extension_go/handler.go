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
	"sort"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/ide/idelsp/lspcmd"
)

const cmdName = "go"

func newGoHandler(
	lsp semanticapi.LSP, editor textapi.Editor,
	wm browserapi.WindowManager, notify browserapi.Notifications,
	parser syntaxapi.Parser, executor workspaceapi.Executor,
	interrupter term.Interrupter, fs workspaceapi.FileSystem,
	cfg config.Config, storage storageapi.Service,
) (textapi.CommandManual, textapi.CommandHandler, error) {
	sel := lspcmd.NewSelectionTracker()
	evs := []textapi.EventType{textapi.EventTypeSelection, textapi.EventTypeCursor}
	if err := editor.SubscribeEvents(evs, sel); err != nil {
		return textapi.CommandManual{}, nil, fmt.Errorf("subscribe selection events: %w", err)
	}
	addImport, err := addImportHandler(lsp, editor, notify)
	if err != nil {
		return textapi.CommandManual{}, nil, err
	}

	handlers := map[string]textapi.CommandHandler{
		// Source actions.
		"organize-imports": lspcmd.CodeActionHandler(lsp, editor, notify, wm, sel, "source.organizeImports", true,
			"Imports are already organized"),
		"fix-all": lspcmd.CodeActionHandler(lsp, editor, notify, wm, sel, "source.fixAll", true,
			"No automatic fixes available"),
		"add-test": lspcmd.CodeActionHandler(lsp, editor, notify, wm, sel, "source.addTest", true,
			"Place cursor inside a function to generate a test"),
		"assembly": lspcmd.CodeActionHandler(lsp, editor, notify, wm, sel, "source.assembly", true,
			"Place cursor inside a function to view assembly. Generic functions and init() are not supported"),
		"doc": lspcmd.CodeActionHandler(lsp, editor, notify, wm, sel, "source.doc", true,
			"No documentation available for this package"),
		"free-symbols": lspcmd.CodeActionHandler(lsp, editor, notify, wm, sel, "source.freesymbols", true,
			"Select a block of code to analyze its free symbols"),
		"toggle-compiler-opt": lspcmd.CodeActionHandler(lsp, editor, notify, wm, sel, "source.toggleCompilerOptDetails", true,
			"Place cursor in a Go file to toggle compiler optimization diagnostics"),

		// Refactoring actions.
		"fill-struct": lspcmd.CodeActionHandler(lsp, editor, notify, wm, sel, "refactor.rewrite.fillStruct", true,
			"Place cursor on a struct literal (e.g. Type{}) to fill fields"),
		"fill-switch": lspcmd.CodeActionHandler(lsp, editor, notify, wm, sel, "refactor.rewrite.fillSwitch", true,
			"Place cursor on a switch statement to add missing cases"),
		"add-tags": lspcmd.CodeActionHandler(lsp, editor, notify, wm, sel, "refactor.rewrite.addTags", true,
			"Place cursor on a struct field to add tags"),
		"remove-tags": lspcmd.CodeActionHandler(lsp, editor, notify, wm, sel, "refactor.rewrite.removeTags", true,
			"Place cursor on a struct field with existing tags"),
		"extract-function": lspcmd.CodeActionHandler(lsp, editor, notify, wm, sel, "refactor.extract.function", true,
			"Select one or more complete statements to extract"),
		"extract-variable": lspcmd.CodeActionHandler(lsp, editor, notify, wm, sel, "refactor.extract.variable", true,
			"Select an expression to extract as a variable"),
		"extract-method": lspcmd.CodeActionHandler(lsp, editor, notify, wm, sel, "refactor.extract.method", true,
			"Select statements inside a method to extract"),
		"extract-variable-all": lspcmd.CodeActionHandler(lsp, editor, notify, wm, sel, "refactor.extract.variable-all", true,
			"Select an expression to extract all occurrences as a variable"),
		"extract-constant": lspcmd.CodeActionHandler(lsp, editor, notify, wm, sel, "refactor.extract.constant", true,
			"Select a constant expression to extract"),
		"extract-constant-all": lspcmd.CodeActionHandler(lsp, editor, notify, wm, sel, "refactor.extract.constant-all", true,
			"Select a constant expression to extract all occurrences"),
		"extract-to-new-file": lspcmd.CodeActionHandler(lsp, editor, notify, wm, sel, "refactor.extract.toNewFile", true,
			"Select one or more top-level declarations to move"),
		"inline-call": lspcmd.CodeActionHandler(lsp, editor, notify, wm, sel, "refactor.inline.call", true,
			"Place cursor on a function call to inline it"),
		"inline-variable": lspcmd.CodeActionHandler(lsp, editor, notify, wm, sel, "refactor.inline.variable", true,
			"Place cursor on a reference to a local variable to inline it (not the declaration)"),
		"invert-if": lspcmd.CodeActionHandler(lsp, editor, notify, wm, sel, "refactor.rewrite.invertIf", true,
			"Place cursor on an if-else statement to invert its condition"),
		"remove-unused-param": lspcmd.CodeActionHandler(lsp, editor, notify, wm, sel, "refactor.rewrite.removeUnusedParam", true,
			"Place cursor on an unused function parameter to remove it"),
		"move-param-left": lspcmd.CodeActionHandler(lsp, editor, notify, wm, sel, "refactor.rewrite.moveParamLeft", true,
			"Place cursor on a function parameter to move it left"),
		"move-param-right": lspcmd.CodeActionHandler(lsp, editor, notify, wm, sel, "refactor.rewrite.moveParamRight", true,
			"Place cursor on a function parameter to move it right"),
		"change-quote": lspcmd.CodeActionHandler(lsp, editor, notify, wm, sel, "refactor.rewrite.changeQuote", true,
			"Place cursor on a string literal to toggle between raw and interpreted form"),
		"split-lines": lspcmd.CodeActionHandler(lsp, editor, notify, wm, sel, "refactor.rewrite.splitLines", true,
			"Place cursor inside a bracketed list to split items onto separate lines"),
		"join-lines": lspcmd.CodeActionHandler(lsp, editor, notify, wm, sel, "refactor.rewrite.joinLines", true,
			"Place cursor inside a multi-line bracketed list to join items onto one line"),
		"eliminate-dot-import": lspcmd.CodeActionHandler(lsp, editor, notify, wm, sel, "refactor.rewrite.eliminateDotImport", true,
			"Place cursor on a dot import to qualify all references with the package name"),

		// Code lens commands.
		"test":           testHandler(lsp, notify, parser, executor),
		"generate":       codeLensHandler(lsp, notify, "gopls.generate"),
		"regenerate-cgo": codeLensHandler(lsp, notify, "gopls.regenerate_cgo"),

		// Module management.
		"tidy":               modCommandHandler(lsp, notify, "gopls.tidy", tidyArgs),
		"vendor":             modCommandHandler(lsp, notify, "gopls.vendor", vendorArgs),
		"upgrade-dependency": modCommandHandler(lsp, notify, "gopls.check_upgrades", checkUpgradesArgs(fs)),
		"vulncheck":          vulncheckHandler(lsp, notify),

		// Imports.
		"add-import": addImport,

		// Interactive REPL.
		"repl": newREPLSubcommand(wm, interrupter, fs, executor, lsp, cfg, storage),
	}

	manual := textapi.CommandManual{
		Name:     cmdName,
		Summary:  "Go language commands powered by gopls",
		Synopsis: "<subcommand> [<args>...]",
		Commands: []textapi.CommandManual{
			{Name: "organize-imports", Summary: "Organize imports by removing unused, adding missing, and sorting into conventional order"},
			{Name: "fix-all", Summary: "Apply all unambiguously safe fixes to code issues"},
			{Name: "add-test", Summary: "Generate a table-driven test for the function or method at the cursor"},
			{Name: "assembly", Summary: "Show the assembly produced by the compiler for the function at the cursor"},
			{Name: "doc", Summary: "Browse documentation for the current Go package"},
			{Name: "free-symbols", Summary: "Analyze the selected code and report symbols referenced within it but defined outside it"},
			{Name: "toggle-compiler-opt", Summary: "Toggle compiler optimization details (inlining, escape analysis) in diagnostics"},
			{Name: "fill-struct", Summary: "Fill each missing field in a struct literal with a zero value or matching variable"},
			{Name: "fill-switch", Summary: "Add missing cases to a type switch or enum switch statement"},
			{Name: "add-tags", Summary: "Add json struct tags to the fields of the struct enclosing the cursor"},
			{Name: "remove-tags", Summary: "Clear struct tags on the fields of the struct enclosing the cursor"},
			{Name: "extract-function", Summary: "Replace the selected statements with a call to a new function"},
			{Name: "extract-variable", Summary: "Replace the selected expression with a reference to a new local variable"},
			{Name: "extract-method", Summary: "Replace the selected statements inside a method with a call to a new method on the same receiver"},
			{Name: "extract-variable-all", Summary: "Replace all occurrences of the selected expression with a reference to a new local variable"},
			{Name: "extract-constant", Summary: "Replace the selected constant expression with a reference to a new named constant"},
			{Name: "extract-constant-all", Summary: "Replace all occurrences of the selected constant expression with a reference to a new named constant"},
			{Name: "extract-to-new-file", Summary: "Move selected top-level declarations to a new file in the same package"},
			{Name: "inline-call", Summary: "Replace a call to a function or method with the contents of its body"},
			{Name: "inline-variable", Summary: "Replace references to a local variable with its initializer expression"},
			{Name: "invert-if", Summary: "Invert an if-else statement, negating its condition and swapping its blocks"},
			{Name: "remove-unused-param", Summary: "Remove an unused function parameter and update all callers"},
			{Name: "move-param-left", Summary: "Move the function parameter at the cursor one position left, updating all callers"},
			{Name: "move-param-right", Summary: "Move the function parameter at the cursor one position right, updating all callers"},
			{Name: "change-quote", Summary: "Toggle a string literal between raw backtick and interpreted double-quote form"},
			{Name: "split-lines", Summary: "Split function arguments or composite literal fields onto separate lines"},
			{Name: "join-lines", Summary: "Join multi-line function arguments or composite literal fields onto a single line"},
			{Name: "eliminate-dot-import", Summary: "Remove a dot import and qualify all references with the package name"},
			{Name: "test", Summary: "Run the Test or Benchmark function nearest to the cursor"},
			{Name: "generate", Summary: "Run go generate for the //go:generate directive nearest to the cursor"},
			{Name: "regenerate-cgo", Summary: "Re-run cgo to regenerate Go declarations after editing C code"},
			{Name: "tidy", Summary: "Run go mod tidy to ensure the go.mod file matches the source code in the module"},
			{Name: "vendor", Summary: "Run go mod vendor to create or update the vendor directory with all necessary dependencies"},
			{Name: "upgrade-dependency", Summary: "Check for available upgrades of direct dependencies in go.mod"},
			{Name: "vulncheck", Summary: "Run govulncheck to find known vulnerabilities in functions reachable by the application"},
			{Name: "add-import", Summary: "Add a package import to the current file"},
			{Name: "repl", Summary: "Open an interactive Go REPL that runs via go run in the project's module"},
		},
	}

	return manual, &goRouter{handlers: handlers}, nil
}

var _ textapi.CommandHandler = (*goRouter)(nil)

type goRouter struct {
	handlers map[string]textapi.CommandHandler
}

func (r *goRouter) HandleCommand(
	ctx context.Context, cmd textapi.Command,
) error {
	if cmd.Name != cmdName {
		return fmt.Errorf("unknown command: %s", cmd.Name)
	}
	if len(cmd.Args) == 0 {
		return fmt.Errorf("missing subcommand")
	}
	cmd.Name = cmd.Args[0]
	cmd.Args = cmd.Args[1:]
	h, ok := r.handlers[cmd.Name]
	if !ok {
		return fmt.Errorf("unknown go subcommand: %s", cmd.Name)
	}
	return h.HandleCommand(ctx, cmd)
}

func (r *goRouter) Complete(
	ctx context.Context, cmd string, args []string,
) (iterator.Iterator[string], error) {
	if cmd != cmdName {
		return nil, fmt.Errorf("unknown command: %s", cmd)
	}
	if len(args) == 0 {
		return iterator.Empty[string](), nil
	}
	cmd = args[0]
	args = args[1:]
	if h, ok := r.handlers[cmd]; ok {
		return h.Complete(ctx, cmd, args)
	}
	names := make([]string, 0, len(r.handlers))
	for name := range r.handlers {
		names = append(names, name)
	}
	sort.Strings(names)
	return iterator.FromSlice(names), nil
}

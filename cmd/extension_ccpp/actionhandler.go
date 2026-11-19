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
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/ide/idelsp/lspcmd"
)

// cActionCmdName and cppActionCmdName are the command-prompt commands.
// They route to the same clangd; `cpp` adds the tweaks clangd only
// offers to C++ code.
const (
	cActionCmdName   = "c"
	cppActionCmdName = "cpp"
)

// clangdNoActionHint explains the usual reason clangd offers nothing:
// its tweaks are gated on what the cursor or selection covers.
const clangdNoActionHint = "clangd offers its refactorings for the exact selection or cursor " +
	"position they apply to, and none while the file has a parse error"

// actionDeps groups what every `c`/`cpp` subcommand needs.
type actionDeps struct {
	lsp    semanticapi.LSP
	editor textapi.Editor
	wm     browserapi.WindowManager
	notify browserapi.Notifications
	opener browserapi.ResourceOpener
	sel    *lspcmd.SelectionTracker
}

// tweak names a clangd tweak the command prompt exposes as a
// subcommand. clangd files every tweak under the broad refactor or
// quickfix kinds, so a subcommand recognizes its tweak by title.
type tweak struct {
	name    string
	summary string
	// title accepts the titles clangd gives the tweak; a prefix match
	// covers titles that embed the symbol they act on.
	title string
	// hint is notified when clangd does not offer the tweak here.
	hint string
	// cppOnly keeps the tweak off the `c` command, since clangd gates it
	// on C++.
	cppOnly bool
}

// clangdTweaks are the tweaks of clangd 21, by title prefix.
var clangdTweaks = []tweak{
	{name: "extract-function", summary: "Extract the selected statements into a new function",
		title: "Extract to function",
		hint:  "Select whole statements inside a function body to extract them"},
	{name: "extract-variable", summary: "Extract the selected expression into a new variable",
		title: "Extract subexpression to variable",
		hint:  "Select an expression, or place the cursor on one, to extract it"},
	{name: "define-inline", summary: "Move the out-of-line function body at the cursor to its declaration",
		title: "Move function body to declaration",
		hint:  "Place the cursor on the name of an out-of-line function definition"},
	{name: "define-outline", summary: "Move the function body at the cursor out of line",
		title: "Move function body to out-of-line",
		hint:  "Place the cursor on the name of a function defined inside its declaration"},
	{name: expandMacroName, summary: "Replace the macro invocation at the cursor with its expansion",
		title: "Expand macro",
		hint:  "Place the cursor on a macro name"},
	{name: "populate-switch", summary: "Add the missing enum cases to the switch at the cursor",
		title: "Populate switch",
		hint:  "Place the cursor on a switch over an enum that lacks cases"},
	{name: "swap-operands", summary: "Swap the operands of the binary expression at the cursor",
		title: "Swap operands",
		hint:  "Place the cursor on a binary operator"},
	{name: "add-using", summary: "Add a using-declaration for the qualified name at the cursor and drop the qualifier",
		title: "Add using-declaration", cppOnly: true,
		hint: "Place the cursor on a namespace-qualified name"},
	{name: "expand-auto", summary: "Replace the auto at the cursor with the type it deduces to",
		title: "Replace with deduced type", cppOnly: true,
		hint: "Place the cursor on an auto, decltype(auto) or deduced class template"},
	{name: "raw-string", summary: "Convert the string literal at the cursor to a raw string literal",
		title: "Convert to raw string", cppOnly: true,
		hint: "Place the cursor on a string literal with escape sequences"},
	{name: "remove-using-namespace", summary: "Remove the using-directive at the cursor and qualify the names it covered",
		title: "Remove using namespace", cppOnly: true,
		hint: "Place the cursor on a using namespace directive"},
	{name: "scopify-enum", summary: "Convert the enum at the cursor to a scoped enum and qualify its uses",
		title: "Convert to scoped enum", cppOnly: true,
		hint: "Place the cursor on the name of an unscoped enum"},
	{name: "special-members", summary: "Declare the implicit copy and move members of the class at the cursor",
		title: "Declare implicit", cppOnly: true,
		hint: "Place the cursor on a class name whose copy or move members are implicit"},
	{name: "memberwise-constructor", summary: "Define a constructor initializing every member of the class at the cursor",
		title: "Define constructor initializing all members", cppOnly: true,
		hint: "Place the cursor on a class name"},
	{name: "override-pure-virtuals", summary: "Override the pure virtual methods the class at the cursor inherits",
		title: "Override pure virtual", cppOnly: true,
		hint: "Place the cursor on a class name that leaves pure virtual methods unimplemented"},
}

// newActionHandler builds the `c` or `cpp` command-prompt handler. Both
// run the same code-action and custom-request subcommands against the
// shared clangd; `cpp` adds the C++-only tweaks.
func newActionHandler(name string, d actionDeps) (textapi.CommandManual, textapi.CommandHandler) {
	cpp := name == cppActionCmdName
	handlers := map[string]textapi.CommandHandler{
		"switch": &switchSourceHeaderCmd{d: d},
		"list": lspcmd.CodeActionHandler(d.lsp, d.editor, d.notify, d.wm, d.sel, "", false,
			"No code actions available. "+clangdNoActionHint),
		"quickfix": lspcmd.CodeActionHandler(d.lsp, d.editor, d.notify, d.wm, d.sel, "quickfix", false,
			"No quick fixes available. Place the cursor on a diagnostic that clangd offers a fix for"),
		"refactor": lspcmd.CodeActionHandler(d.lsp, d.editor, d.notify, d.wm, d.sel, "refactor", false,
			"No refactoring at the cursor. "+clangdNoActionHint),
		"ast":          &astCmd{d: d},
		"symbol-info":  &symbolInfoCmd{d: d},
		"memory-usage": &memoryUsageCmd{d: d},
	}
	manual := textapi.CommandManual{
		Name:     name,
		Summary:  "C code actions and clangd requests",
		Synopsis: "<subcommand> [<args>...]",
		Commands: []textapi.CommandManual{
			{Name: "switch", Summary: "Open the header of the current source file, or the source of the current header"},
			{Name: "list", Summary: "List every code action clangd offers at the cursor or selection and apply the chosen one"},
			{Name: "quickfix", Summary: "Apply a fix for the diagnostic at the cursor"},
			{Name: "refactor", Summary: "Apply one of the refactorings clangd offers at the cursor or selection"},
		},
	}
	if cpp {
		manual.Summary = "C++ code actions and clangd requests"
	}
	for _, t := range clangdTweaks {
		if t.cppOnly && !cpp {
			continue
		}
		handlers[t.name] = lspcmd.FilteredCodeActionHandler(d.lsp, d.editor, d.notify, d.wm, d.sel,
			lspcmd.CodeActionFilter{Title: titleHasPrefix(t.title)}, true,
			t.hint+". "+clangdNoActionHint)
		manual.Commands = append(manual.Commands, textapi.CommandManual{Name: t.name, Summary: t.summary})
	}
	handlers[expandMacroName] = &expandMacroCmd{d: d, tweak: handlers[expandMacroName]}
	manual.Commands = append(manual.Commands,
		textapi.CommandManual{Name: "ast", Summary: "Show the clang AST of the selection, or of the node at the cursor"},
		textapi.CommandManual{Name: "symbol-info", Summary: "Show the name, container and USR of the symbol at the cursor"},
		textapi.CommandManual{Name: "memory-usage", Summary: "Show what clangd holds in memory, largest first"},
	)
	return manual, &actionRouter{name: name, handlers: handlers, editor: d.editor}
}

// titleHasPrefix accepts the code-action titles starting with prefix.
func titleHasPrefix(prefix string) func(string) bool {
	return func(title string) bool { return strings.HasPrefix(title, prefix) }
}

const expandMacroName = "expand-macro"

// defineDirective matches the first line of a #define and captures the
// macro it defines.
var defineDirective = regexp.MustCompile(`^\s*#\s*define\s+([A-Za-z_]\w*)`)

// expandMacroCmd fronts clangd's expand-macro tweak for a cursor inside a
// #define. The preprocessor expands nothing in a directive, so there is
// no expansion for clangd to offer there, and the tweak's miss would
// send the user to a macro name they are already on. The buffer tells
// that miss apart and names the macro whose use to go to instead.
type expandMacroCmd struct {
	d     actionDeps
	tweak textapi.CommandHandler
}

func (c *expandMacroCmd) HandleCommand(ctx context.Context, cmd textapi.Command) error {
	if cmd.Resource != nil {
		if hint, ok := defineHint(c.d.editor, cmd.Resource, cmd.Cursor.Content); ok {
			_, _ = c.d.notify.Notify(browserapi.LevelInfo, hint)
			return nil
		}
	}
	return c.tweak.HandleCommand(ctx, cmd)
}

func (c *expandMacroCmd) Complete(
	ctx context.Context, name string, args []string,
) (iterator.Iterator[string], error) {
	return c.tweak.Complete(ctx, name, args)
}

// defineHint explains an expand-macro miss at cur when cur lies in a
// #define, which runs on through every line ending in a backslash. On
// the macro's own name, or off any identifier, the macro is reported as
// defined rather than used; on another identifier, that one is reported
// as unexpanded in the definition.
func defineHint(editor textapi.Editor, res textapi.Handler, cur term.Coordinates) (string, bool) {
	cv := editor.CellView(res)
	if cv == nil {
		return "", false
	}
	cells, err := cv.RawCells()
	if err != nil || cur.Y < 0 || cur.Y >= len(cells) {
		return "", false
	}
	start := cur.Y
	for start > 0 && strings.HasSuffix(strings.TrimRight(cellText(cells[start-1]), " \t"), `\`) {
		start--
	}
	first := cellText(cells[start])
	m := defineDirective.FindStringSubmatchIndex(first)
	if m == nil {
		return "", false
	}
	name := first[m[2]:m[3]]
	nameEnd := utf8.RuneCountInString(first[:m[3]])
	under := identifierAt(cells[cur.Y], cur.X)
	if under == "" || under == name || (cur.Y == start && cur.X < nameEnd) {
		return fmt.Sprintf("`%s` is defined here, not used; clangd expands a macro where it is used. "+
			"Place the cursor on a use of `%s`.", name, name), true
	}
	return fmt.Sprintf("`%s` sits in the definition of `%s`, where nothing is expanded; clangd expands "+
		"a macro where it is used. Place the cursor on a use of `%s`, or of `%s` outside a `#define`.",
		under, name, name, under), true
}

// identifierAt returns the identifier covering column x of row, or the
// one ending right before it, as a cursor rests after a word as often as
// on it.
func identifierAt(row []term.Cell, x int) string {
	if x < 0 || x > len(row) {
		return ""
	}
	if x == len(row) || !isIdentRune(row[x].Ch) {
		if x == 0 || !isIdentRune(row[x-1].Ch) {
			return ""
		}
		x--
	}
	lo, hi := x, x+1
	for lo > 0 && isIdentRune(row[lo-1].Ch) {
		lo--
	}
	for hi < len(row) && isIdentRune(row[hi].Ch) {
		hi++
	}
	return cellText(row[lo:hi])
}

func isIdentRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

func cellText(row []term.Cell) string {
	var b strings.Builder
	for _, c := range row {
		b.WriteRune(c.Ch)
		b.WriteString(string(c.CombiningRunes()))
	}
	return b.String()
}

var _ textapi.CommandHandler = (*actionRouter)(nil)

type actionRouter struct {
	name     string
	handlers map[string]textapi.CommandHandler
	editor   textapi.Editor
}

func (r *actionRouter) HandleCommand(ctx context.Context, cmd textapi.Command) error {
	if cmd.Name != r.name {
		return fmt.Errorf("unknown command: %s", cmd.Name)
	}
	if len(cmd.Args) == 0 {
		return fmt.Errorf("missing subcommand")
	}
	cmd.Name, cmd.Args = cmd.Args[0], cmd.Args[1:]
	h, ok := r.handlers[cmd.Name]
	if !ok {
		return fmt.Errorf("unknown %s subcommand: %s", r.name, cmd.Name)
	}
	r.refreshCursor(&cmd)
	return h.HandleCommand(ctx, cmd)
}

// refreshCursor replaces a zero cursor snapshot with the live editor
// cursor. Dispatch paths that cannot capture a cursor (a non-text window
// in focus, a stale tab handler) deliver {0,0} alongside a valid
// resource, which would run the cursor-scoped subcommands against the
// top of the file.
func (r *actionRouter) refreshCursor(cmd *textapi.Command) {
	if cmd.Resource == nil || cmd.Cursor.Content != (term.Coordinates{}) {
		return
	}
	if live, err := r.editor.Cursor(cmd.Resource); err == nil {
		cmd.Cursor.Content = live
	}
}

func (r *actionRouter) Complete(
	ctx context.Context, cmd string, args []string,
) (iterator.Iterator[string], error) {
	if cmd != r.name {
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

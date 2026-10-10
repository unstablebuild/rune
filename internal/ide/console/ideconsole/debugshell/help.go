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

package debugshell

import (
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	tuimarkdown "unstable.build/rune/internal/component/markdown"
)

// Subcommand names. Exported so callers (typically language
// extensions owning the top-level REPL command) can reuse them
// when composing completions or help text.
const (
	subHelp            = "help"
	subInitialize      = "initialize"
	subLaunch          = "launch"
	subAttach          = "attach"
	subConfigured      = "configured"
	subTerminate       = "terminate"
	subRestart         = "restart"
	subContinue        = "continue"
	subNext            = "next"
	subStepIn          = "step-in"
	subStepOut         = "step-out"
	subStepBack        = "step-back"
	subReverseContinue = "reverse-continue"
	subPause           = "pause"
	subGoto            = "goto"
	subVariables       = "variables"
	subStackTrace      = "stack-trace"
	subThreads         = "threads"
	subScopes          = "scopes"
	subModules         = "modules"
	subLoadedSources   = "loaded-sources"
	subSetBreakpoint   = "set-breakpoint"
	subSetVariable     = "set-variable"
	subSetExpression   = "set-expression"
	subEvaluate        = "evaluate"
	subDisassemble     = "disassemble"
	subJump            = "jump"
)

// Direction arguments for the `jump` prompt subcommand. They
// move the cursor through the current stack-frame trace without
// issuing any DAP step-in/step-out request. Direction follows
// program execution: `backward` walks up the stack toward the
// caller (shallower frames), `forward` walks back down toward
// the deepest (most-recently-entered) frame.
const (
	jumpForward  = "forward"
	jumpBackward = "backward"
)

// subcommands returns the supported subcommands in help order.
func subcommands() []string {
	return []string{
		subHelp,
		subInitialize,
		subLaunch,
		subAttach,
		subConfigured,
		subTerminate,
		subRestart,
		subContinue,
		subNext,
		subStepIn,
		subStepOut,
		subStepBack,
		subReverseContinue,
		subPause,
		subGoto,
		subThreads,
		subStackTrace,
		subScopes,
		subVariables,
		subModules,
		subLoadedSources,
		subSetBreakpoint,
		subSetVariable,
		subSetExpression,
		subEvaluate,
		subDisassemble,
		subJump,
	}
}

// completeSubcommands returns completions for the subcommand
// position. args are the arguments after the top-level command
// name.
func completeSubcommands(args []string) []string {
	if len(args) == 0 {
		return subcommands()
	}
	if len(args) > 1 {
		return nil
	}
	prefix := args[0]
	var out []string
	for _, s := range subcommands() {
		if strings.HasPrefix(s, prefix) {
			out = append(out, s)
		}
	}
	return out
}

// promptSubcommands returns the debugger subcommands that are
// available from the editor command prompt. This is a strict
// subset of subcommands(): only those that depend on live editor
// state (cursor, resource) belong here. Keep in sync with the
// switch in PromptHandler.HandleCommand.
func promptSubcommands() []string {
	return []string{subSetBreakpoint, subJump}
}

// completePromptSubcommands mirrors completeSubcommands but
// filters to prompt-available subcommands.
func completePromptSubcommands(args []string) []string {
	if len(args) == 0 {
		return promptSubcommands()
	}
	if len(args) > 1 {
		return nil
	}
	prefix := args[0]
	var out []string
	for _, s := range promptSubcommands() {
		if strings.HasPrefix(s, prefix) {
			out = append(out, s)
		}
	}
	return out
}

// Manual returns the CommandManual to register for the debugger
// command (in both the REPL and the editor command prompt).
func Manual() textapi.CommandManual {
	return textapi.CommandManual{
		Name: CommandName,
		Summary: "Control a debug session via the Debug Adapter Protocol. " +
			"Start with `debugger initialize <langID>`, then `launch` or `attach`, then `configured`.",
		Synopsis: "<subcommand> [...]",
	}
}

// helpLines returns the manual/help text for the debugger
// command.
func helpLines() iterator.Iterator[component.Responsive] {
	var b strings.Builder
	b.WriteString("## Quick start\n")
	b.WriteString("- `debugger initialize go`\n")
	b.WriteString("- `debugger launch ./cmd/myprog` or `debugger attach 1234`\n")
	b.WriteString("- Navigate to a location with your cursor and invoke ")
	b.WriteString("`debugger set-breakpoint` command prompt command to set a breakpoint.\n")
	b.WriteString("- `debugger configured`\n")
	b.WriteString("## Session lifecycle\n")
	b.WriteString("- **`initialize`** `<langID>` — Create a debug session for the language\n")
	b.WriteString("- **`launch`** `[-e KEY=VAL]... [--] <program> [args...]` — Send Launch. " +
		"Repeat `-e` to set debuggee env vars; use `--` to allow a program path " +
		"or args that start with `-`.\n")
	b.WriteString("- **`attach`** `<pid|program>` — Send Attach\n")
	b.WriteString("- **`attach`** `<langID> connect://host:port [program]` — " +
		"Create a session against an adapter that is already listening " +
		"(e.g. `python -m debugpy --listen 127.0.0.1:5678 app.py`) and " +
		"send Attach. No prior `initialize` is needed.\n")
	b.WriteString("- **`configured`** — Send all in-memory breakpoints, then ConfigurationDone\n")
	b.WriteString("- **`terminate`** — End the current debug session\n")
	b.WriteString("- **`restart`** — Restart the current debug session\n")
	b.WriteString("## Execution\n")
	b.WriteString("- **`continue`** `[thread-id]` — Resume execution\n")
	b.WriteString("- **`next`** `[thread-id]` — Step over\n")
	b.WriteString("- **`step-in`** `[thread-id]` — Step into\n")
	b.WriteString("- **`step-out`** `[thread-id]` — Step out\n")
	b.WriteString("- **`step-back`** `[thread-id]` — Step backward\n")
	b.WriteString("- **`reverse-continue`** `[thread-id]` — Reverse-continue\n")
	b.WriteString("- **`pause`** `[thread-id]` — Pause execution\n")
	b.WriteString("- **`goto`** `<target>` — Jump to a goto target\n")
	b.WriteString("## Introspection\n")
	b.WriteString("- **`threads`** — List threads\n")
	b.WriteString("- **`stack-trace`** `[thread-id]` — Show the call stack\n")
	b.WriteString("- **`scopes`** `[frame-id]` — Show scopes for a frame\n")
	b.WriteString("- **`variables`** `[ref]` — Show variables for a scope\n")
	b.WriteString("- **`modules`** — Show loaded modules\n")
	b.WriteString("- **`loaded-sources`** — Show loaded sources\n")
	b.WriteString("- **`set-breakpoint`** — Toggle a breakpoint at the cursor ")
	b.WriteString("(use the command prompt: `:debugger set-breakpoint`); breakpoints are kept ")
	b.WriteString("in memory and sent on `configured`\n")
	b.WriteString("- **`set-variable`** `<name>` `<expr>` — Set a variable in the current scope\n")
	b.WriteString("- **`set-expression`** `<expr>` `<value>` — Assign to an expression\n")
	b.WriteString("- **`evaluate`** `<expr...>` — Evaluate an expression in the top frame\n")
	b.WriteString("- **`disassemble`** `<memref>` `[count]` — Disassemble `count` (default 16) ")
	b.WriteString("instructions starting at `memref`. `memref` is a hex address (e.g. `0x10b3a40`) ")
	b.WriteString("provided by the debug adapter. The most common source is the instruction ")
	b.WriteString("pointer of a stack frame: run `debugger stack-trace` after a stop and copy the ")
	b.WriteString("`ip:` field of the frame you want to inspect. Example: ")
	b.WriteString("`debugger disassemble 0x10b3a40 32`.\n")
	return helpMarkdown(b.String())
}

// helpMarkdown renders the debugger help text with no extra
// paragraph spacing between sections so the manual stays compact.
// Falls back to the default markdown renderer on parse error.
func helpMarkdown(content string) iterator.Iterator[component.Responsive] {
	cfg := tuimarkdown.DefaultConfig()
	cfg.HeaderPrefix = false
	cfg.ParagraphSpacing = 0
	md, err := tuimarkdown.NewWithConfig(content, cfg)
	if err != nil {
		return markdown(content)
	}
	return iterator.FromSlice([]component.Responsive{md})
}

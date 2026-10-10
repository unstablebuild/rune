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

package ideconsole

import (
	"context"

	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
)

// Config configures optional dependencies of the IDE shell handler.
type Config struct {
	// Storage is used by both the inner repl history persistence and
	// by the reverse-history search overlay. May be nil in tests.
	Storage storageapi.Service
	// HistoryDocumentID is the storage key under which the shell
	// history is persisted. Required if Storage is set.
	HistoryDocumentID string
	// MaxHistory caps the number of persisted history entries.
	MaxHistory int
	// Prompt is the inputbox prompt prefix shown both in the
	// regular shell view and on the search overlay's input bar.
	// Defaults to "> ".
	Prompt string
	// Workspace seeds the shell interpreter's working directory.
	// Without it the interpreter inherits the process working
	// directory, which under a macOS .app launch is the bundle.
	Workspace workspaceapi.URI
	// Executor dispatches external (non-builtin) commands through the
	// workspace so remote workspaces (ssh, in-memory) run them on the
	// right host. When nil, external commands run via mvdan/sh's
	// default local exec handler.
	Executor schemeapi.Executor
	// Modal reports whether the editor backing the input line is
	// modal (vi). Only when true does ModalStartInsert take effect; a
	// modeless editor has no normal mode to switch out of.
	Modal bool
	// ModalStartInsert opens the input line in insert mode at
	// construction when Modal is true, sparing the user from pressing
	// `i` before typing into a fresh shell prompt.
	ModalStartInsert bool
	// DisableShellInterpreter, when non-nil, runs the shell as a pure
	// command registry with no mvdan/sh parsing and no PATH executable
	// fallback. Lines whose first token does not match a registered
	// command are dispatched to this handler instead of surfacing
	// repl.ErrNotFound. Use it for REPL surfaces (e.g. a language REPL)
	// where every input line is a fragment of the hosted language
	// rather than a discrete command — the handler receives the whole
	// line reconstructed from cmd.Name and cmd.Args.
	DisableShellInterpreter repl.CommandHandler

	// ClearHook, when set, runs after the shell clears its screen
	// (<c-l>). A host that keeps state mirrored on the screen (e.g. a
	// REPL accumulating a program) uses it to reset that state so it
	// stays in sync with the now-blank view.
	ClearHook func()
}

const defaultPrompt = "> "

func registerBaseCommands(r *CommandRegistry) {
	r.Register("help", "Show available commands", &helpHandler{r: r})
}

type helpHandler struct {
	r *CommandRegistry
}

func (h *helpHandler) HandleCommand(
	ctx context.Context, cmd repl.Command, _ repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	return h.r.Help(ctx, cmd.Args)
}

func (h *helpHandler) Complete(
	ctx context.Context, _ string, args []string,
) (iterator.Iterator[string], error) {
	if len(args) == 0 {
		return iterator.Empty[string](), nil
	}
	return h.r.Complete(ctx, args[len(args)-1], nil)
}

func (h *helpHandler) Help(
	_ context.Context, _ []string,
) (iterator.Iterator[component.Responsive], error) {
	return toLines(
		"Show available commands or help for a specific command",
	), nil
}

func toLines(ss ...string) iterator.Iterator[component.Responsive] {
	out := make([]component.Responsive, len(ss))
	for i, s := range ss {
		out[i] = toResponsive(s)
	}
	return iterator.FromSlice(out)
}

func toResponsive(s string) component.Responsive {
	return component.NewResponsiveString(
		s, component.StringResponsiveConfig{},
	)
}

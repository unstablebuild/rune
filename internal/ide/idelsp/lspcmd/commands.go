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

package lspcmd

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
)

const cmdName = "lsp"

// Manual returns the textapi.CommandManual for the "lsp" command.
func Manual() textapi.CommandManual {
	return textapi.CommandManual{
		Name:     cmdName,
		Summary:  "Language Server Protocol commands",
		Synopsis: "<subcommand> [<args>...]",
		Commands: []textapi.CommandManual{
			{
				Name:    "format",
				Summary: "Formats the file",
			},
			{
				Name:     "hover",
				Synopsis: "[<symbol>]",
				Summary:  "Displays documentation, types, and signatures for a symbol; if no symbol argument is passed, then the symbol at cursor is used",
			},
			{
				Name:    "complete",
				Summary: "Show completions at cursor",
			},
			{
				Name:     "definition",
				Synopsis: "[<symbol>]",
				Summary:  "Go to definition of a symbol; if no symbol argument is passed, then the symbol at cursor is used",
			},
			{
				Name:     "declaration",
				Synopsis: "[<symbol>]",
				Summary:  "Go to declaration of a symbol; if no symbol argument is passed, then the symbol at cursor is used",
			},
			{
				Name:     "type-definition",
				Synopsis: "[<symbol>]",
				Summary:  "Go to type definition of a symbol; if no symbol argument is passed, then the symbol at cursor is used",
			},
			{
				Name:     "implementation",
				Synopsis: "[<symbol>]",
				Summary:  "Find implementations of a symbol; if no symbol argument is passed, then the symbol at cursor is used",
			},
			{
				Name:     "references",
				Synopsis: "[<symbol>]",
				Summary:  "Find references to a symbol; if no symbol argument is passed, then the symbol at cursor is used",
			},
			{
				Name:    "diagnostics",
				Summary: "Show all LSP diagnostics in a location picker",
			},
			{
				Name:    "signature-help",
				Summary: "Show signature help at cursor",
			},
			{
				Name:    "rename",
				Summary: "Rename symbol at cursor",
			},
		},
	}
}

// Config groups configuration for every subcommand registered by
// AllHandler.
type Config struct {
	RootURI        workspaceapi.URI
	Parser         syntaxapi.Parser // nil = no highlighting
	Hover          HoverConfig
	Complete       CompleteConfig
	Definition     DefinitionConfig
	Declaration    DeclarationConfig
	TypeDefinition TypeDefinitionConfig
	Implementation ImplementationConfig
	References     ReferencesConfig
	Highlight      HighlightConfig
	SignatureHelp  SignatureHelpConfig
	Diagnostics    DiagnosticsConfig

	// DiagnosticsSource provides the workspace-wide diagnostics
	// snapshot consumed by the "diagnostics" subcommand. When nil,
	// the subcommand reports that no diagnostics are available.
	DiagnosticsSource DiagnosticsSource

	// ScheduleNextTick defers a function to the next event-loop tick.
	ScheduleNextTick func(func()) bool

	// Interrupter signals the event loop to re-render after
	// asynchronous updates (e.g. completion results arriving).
	Interrupter term.Interrupter
}

// DefaultConfig returns a Config where every subcommand uses its
// own default configuration.
func DefaultConfig() Config {
	return Config{
		Hover:          DefaultHoverConfig(),
		Complete:       DefaultCompleteConfig(),
		Definition:     DefaultDefinitionConfig(),
		Declaration:    DefaultDeclarationConfig(),
		TypeDefinition: DefaultTypeDefinitionConfig(),
		Implementation: DefaultImplementationConfig(),
		References:     DefaultReferencesConfig(),
		Highlight:      DefaultHighlightConfig(),
		SignatureHelp:  DefaultSignatureHelpConfig(),
		Diagnostics:    DefaultDiagnosticsConfig(),
		Interrupter:    term.NopInterrupter(),
	}
}

// AllHandler creates the textapi.CommandHandler for the "lsp" command
// by composing all individual subcommand handlers.
func AllHandler(
	lsp semanticapi.LSP, editor textapi.Editor,
	wm browserapi.WindowManager, opener browserapi.ResourceOpener,
	notify browserapi.Notifications, fs workspaceapi.FileSystem,
	parser syntaxapi.Parser, cfg Config,
) (textapi.CommandHandler, error) {
	if cfg.RootURI.String() == "" {
		panic("lspcmd: Config.RootURI must be set")
	}
	wsLog := slog.With("workspace", cfg.RootURI.String())
	formatH, err := FormatHandler(lsp, editor)
	if err != nil {
		return nil, err
	}
	err = SubscribeHighlight(lsp, editor, cfg.ScheduleNextTick, cfg.Highlight, wsLog)
	if err != nil {
		return nil, err
	}
	r := &routerHandler{
		lsp: lsp,
	}
	if err := editor.SubscribeEvents(
		[]textapi.EventType{textapi.EventTypeFocus}, r,
	); err != nil {
		return nil, err
	}
	r.handlers = map[string]textapi.CommandHandler{
		"format":   formatH,
		"hover":    HoverHandler(lsp, wm, notify, fs, cfg.RootURI, cfg.ScheduleNextTick, cfg.Parser, cfg.Hover),
		"complete": CompleteHandler(lsp, editor, wm, cfg.Complete, cfg.Interrupter, wsLog),
		"definition": DefinitionHandler(
			lsp, editor, wm, opener, notify, fs,
			cfg.RootURI, cfg.ScheduleNextTick, cfg.Parser, cfg.Definition, wsLog,
		),
		"declaration": DeclarationHandler(
			lsp, editor, wm, opener, notify, fs,
			cfg.RootURI, cfg.ScheduleNextTick, cfg.Parser, cfg.Declaration, wsLog,
		),
		"type-definition": TypeDefinitionHandler(
			lsp, editor, wm, opener, notify, fs,
			cfg.RootURI, cfg.ScheduleNextTick, cfg.Parser, cfg.TypeDefinition, wsLog,
		),
		"implementation": ImplementationHandler(
			lsp, editor, wm, opener, notify, fs,
			cfg.RootURI, cfg.ScheduleNextTick, cfg.Parser, cfg.Implementation, wsLog,
		),
		"references": ReferencesHandler(
			lsp, editor, wm, opener, notify, fs,
			cfg.RootURI, cfg.ScheduleNextTick, cfg.Parser, cfg.References, wsLog,
		),
		"diagnostics": DiagnosticsHandler(
			editor, wm, opener, notify, fs,
			cfg.RootURI, cfg.ScheduleNextTick, cfg.Parser,
			cfg.DiagnosticsSource, cfg.Diagnostics, wsLog,
		),
		"signature-help": SignatureHelpHandler(lsp, editor, wm,
			cfg.ScheduleNextTick, cfg.SignatureHelp, wsLog),
		"rename": RenameHandler(lsp, editor, wm, opener, notify, wsLog),
	}
	return r, nil
}

var (
	_ textapi.CommandHandler = (*routerHandler)(nil)
	_ textapi.EventHandler   = (*routerHandler)(nil)
)

type routerHandler struct {
	handlers map[string]textapi.CommandHandler
	lsp      semanticapi.LSP
	focusURI workspaceapi.URI
}

func (r *routerHandler) Handle(_ context.Context, ev textapi.Event) bool {
	if ev.Type == textapi.EventTypeFocus && ev.URI.String() != "" {
		r.focusURI = ev.URI
	}
	return false
}

func (r *routerHandler) HandleCommand(
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
		return fmt.Errorf("unknown lsp subcommand: %s", cmd.Name)
	}
	return h.HandleCommand(ctx, cmd)
}

func (r *routerHandler) Complete(
	ctx context.Context,
	cmd string, args []string,
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

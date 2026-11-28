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
	"log/slog"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/handler/locationpicker"
)

// ReferencesConfig configures the "references" subcommand.
type ReferencesConfig struct {
	ListConfig locationpicker.Config
}

// DefaultReferencesConfig returns a ReferencesConfig with sensible defaults.
func DefaultReferencesConfig() ReferencesConfig {
	return ReferencesConfig{
		ListConfig: locationpicker.DefaultConfig(),
	}
}

// ReferencesHandler creates a textapi.CommandHandler that finds all references
// to the symbol at the cursor position and displays them in a floating window
// with a file preview.
func ReferencesHandler(
	lsp commandLSP, editor textapi.Editor,
	wm browserapi.WindowManager, opener browserapi.ResourceOpener,
	notify browserapi.Notifications, fs workspaceapi.FileSystem,
	rootURI workspaceapi.URI, scheduleNextTick func(func()) bool,
	parser syntaxapi.Parser, cfg ReferencesConfig,
	log *slog.Logger,
) textapi.CommandHandler {
	return &referencesHandler{
		lsp: lsp, editor: editor, wm: wm, opener: opener,
		notify: notify, fs: fs, rootURI: rootURI,
		scheduleNextTick: scheduleNextTick, parser: parser, cfg: cfg,
		log: log,
	}
}

type referencesHandler struct {
	lsp              commandLSP
	editor           textapi.Editor
	wm               browserapi.WindowManager
	opener           browserapi.ResourceOpener
	notify           browserapi.Notifications
	fs               workspaceapi.FileSystem
	rootURI          workspaceapi.URI
	scheduleNextTick func(func()) bool
	parser           syntaxapi.Parser
	cfg              ReferencesConfig
	log              *slog.Logger
}

func (h *referencesHandler) HandleCommand(ctx context.Context, cmd textapi.Command) error {
	proceed, err := resolveCommandSymbol(ctx, &cmd, h.rootURI, h.wm, h.fs, h.notify, h.scheduleNextTick, h.parser,
		executeResolved("references", h.rootURI, h.notify, h.scheduleNextTick, h.execute))
	if !proceed || err != nil {
		return err
	}
	executeAtCursor("references", h.notify, h.scheduleNextTick, cmd, h.execute)
	return nil
}

// execute performs the blocking LSP round trip. It is called off the event
// loop and schedules UI work onto it.
func (h *referencesHandler) execute(
	ctx context.Context, uri workspaceapi.URI, pos semanticapi.Position,
) error {
	params := semanticapi.ReferenceParams{
		TextDocument: TextDocID(uri),
		Position:     pos,
		Context:      semanticapi.ReferenceContext{IncludeDeclaration: true},
	}
	locs, fromDisk, err := h.lsp.ReferenceLocations(ctx, params)
	if err != nil {
		return err
	}
	if len(locs) == 0 {
		return nil
	}
	entries := make([]locationEntry, len(locs))
	for i, loc := range locs {
		entries[i] = locationFromLoc(loc)
	}
	entries = enrichEntries(entries, h.rootURI)
	cfg := h.cfg.ListConfig
	cfg.Editor = h.editor
	if fromDisk {
		cfg.Editor = nil
	}
	presentLocations(
		"references", h.rootURI, entries, h.opener, h.wm, h.editor, h.notify,
		h.fs, h.scheduleNextTick, h.parser, cfg, h.log,
	)
	return nil
}

func (h *referencesHandler) Complete(
	ctx context.Context, _ string, args []string,
) (iterator.Iterator[string], error) {
	return completeReferencedSymbol(ctx, h.parser)
}

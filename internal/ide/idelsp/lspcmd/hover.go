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

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	mdcomp "unstable.build/rune/internal/component/markdown"
	mdhandler "unstable.build/rune/internal/handler/markdown"
)

// HoverConfig configures the "hover" subcommand.
type HoverConfig struct {
	// MarkdownConfig configures the markdown component used to render
	// hover results with MarkupKindMarkdown content.
	MarkdownConfig mdcomp.Config
	// MarkdownHandlerOptions configures the markdown handler used to
	// render hover results with MarkupKindMarkdown content.
	MarkdownHandlerOptions []mdhandler.Option
}

// DefaultHoverConfig returns a HoverConfig with sensible defaults.
func DefaultHoverConfig() HoverConfig {
	return HoverConfig{
		MarkdownConfig: mdcomp.DefaultConfig(),
	}
}

// HoverHandler creates a textapi.CommandHandler for the "hover" subcommand.
// It calls the LSP hover request and displays the result in a floating window.
func HoverHandler(
	lsp semanticapi.LSP, wm browserapi.WindowManager,
	notify browserapi.Notifications, fs workspaceapi.FileSystem,
	rootURI workspaceapi.URI, scheduleNextTick func(func()) bool,
	parser syntaxapi.Parser, cfg HoverConfig,
) textapi.CommandHandler {
	return &hoverHandler{
		lsp: lsp, wm: wm, notify: notify, fs: fs, rootURI: rootURI,
		scheduleNextTick: scheduleNextTick, parser: parser, cfg: cfg,
	}
}

var (
	_ textapi.CommandHandler = (*hoverHandler)(nil)
	_ browserapi.Floating    = (*hoverFloating)(nil)
)

type hoverHandler struct {
	lsp              semanticapi.LSP
	wm               browserapi.WindowManager
	notify           browserapi.Notifications
	fs               workspaceapi.FileSystem
	rootURI          workspaceapi.URI
	scheduleNextTick func(func()) bool
	parser           syntaxapi.Parser
	cfg              HoverConfig
}

func (h *hoverHandler) HandleCommand(ctx context.Context, cmd textapi.Command) error {
	proceed, err := resolveCommandSymbol(ctx, &cmd, h.rootURI, h.wm, h.fs, h.notify, h.scheduleNextTick, h.parser,
		executeResolved("hover", h.rootURI, h.notify, h.scheduleNextTick, h.execute))
	if !proceed || err != nil {
		return err
	}
	executeAtCursor("hover", h.notify, h.scheduleNextTick, cmd, h.execute)
	return nil
}

// execute performs the blocking LSP round trip. It is called off the event
// loop; floating-window creation is scheduled onto it.
func (h *hoverHandler) execute(
	ctx context.Context, uri workspaceapi.URI, pos semanticapi.Position,
) error {
	params := semanticapi.HoverParams{
		TextDocument: TextDocID(uri),
		Position:     pos,
	}
	result, err := h.lsp.Hover(ctx, params)
	if err != nil {
		return err
	}
	if result == nil || result.Contents.Value == "" {
		return nil
	}
	if result.Contents.Kind == semanticapi.MarkupKindMarkdown {
		comp, mdErr := mdcomp.NewWithConfig(result.Contents.Value, h.cfg.MarkdownConfig)
		if mdErr != nil {
			return mdErr
		}
		mdh := mdhandler.New(comp, h.cfg.MarkdownHandlerOptions...)
		span := handler.NewSpan(mdh, component.SpanConfig{
			PadHorizontal:    2,
			ContentAlignment: component.AlignmentCentered,
		})
		h.scheduleNextTick(func() {
			floating := browserapi.FuncFloatingHandler(span, mdh.Close)
			_, err := h.wm.Floating(floating, browserapi.FloatingConfig{
				Alignment: component.AlignmentCentered,
			})
			if err != nil {
				_, _ = h.notify.Notify(browserapi.LevelError, "hover: %s", err)
			}
		})
		return nil
	}
	f := newHoverFloating(component.NewString(result.Contents.Value))
	h.scheduleNextTick(func() {
		_, err := h.wm.Floating(f, browserapi.FloatingConfig{
			Alignment: component.AlignmentCentered,
		})
		if err != nil {
			_, _ = h.notify.Notify(browserapi.LevelError, "hover: %s", err)
		}
	})
	return nil
}

func (h *hoverHandler) Complete(ctx context.Context, _ string, _ []string) (
	iterator.Iterator[string], error,
) {
	return completeReferencedSymbol(ctx, h.parser)
}

func newHoverFloating(f component.Floating) *hoverFloating {
	w, h := f.Dimensions()
	f.Resize(w, h)
	return &hoverFloating{Floating: f}
}

type hoverFloating struct {
	component.Floating
}

// Handle dismisses the hover on any key event, including modifier-only
// keys. This is intentional: the hover tooltip is transient and should
// disappear on any keyboard interaction.
func (h *hoverFloating) Handle(ev term.Event) (bool, bool) {
	if ev.Type == term.EventKey {
		return true, true
	}
	return false, false
}

func (h *hoverFloating) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return term.Coordinates{}, term.CursorStyleDefault, false
}

func (h *hoverFloating) Selection() (string, bool) {
	return "", false
}

func (h *hoverFloating) Close() error {
	return nil
}

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
	"strings"
	"unicode/utf8"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// CodeActionHandler creates a textapi.CommandHandler that requests
// textDocument/codeAction of the given kind at the cursor or selection
// and applies the chosen action. An empty kind requests every kind and
// keeps every returned action, which is how a caller exposes a "list
// everything" subcommand. noActionHint is notified when the server
// offers nothing.
//
// applyLoneAction applies a sole result without confirmation, which
// suits a subcommand whose name already states the one thing it does.
// A caller that maps to a broad kind leaves it false, since there the
// command name says nothing about which of the family will run. A sole
// result that would change nothing also notifies noActionHint, since
// servers answer an already organized file with such an action.
func CodeActionHandler(
	lsp semanticapi.LSP, editor textapi.Editor,
	notify browserapi.Notifications,
	wm browserapi.WindowManager,
	sel *SelectionTracker,
	kind semanticapi.CodeActionKind,
	applyLoneAction bool,
	noActionHint string,
) textapi.CommandHandler {
	return &codeActionCmd{lsp: lsp, editor: editor, notify: notify,
		wm: wm, sel: sel, kind: kind, applyLoneAction: applyLoneAction,
		noActionHint: noActionHint}
}

var _ textapi.CommandHandler = (*codeActionCmd)(nil)

type codeActionCmd struct {
	lsp             semanticapi.LSP
	editor          textapi.Editor
	notify          browserapi.Notifications
	wm              browserapi.WindowManager
	sel             *SelectionTracker
	kind            semanticapi.CodeActionKind
	applyLoneAction bool
	noActionHint    string
}

func (h *codeActionCmd) HandleCommand(
	ctx context.Context, cmd textapi.Command,
) error {
	if cmd.Resource == nil {
		return fmt.Errorf("no file open; open a file first")
	}

	rng, ok := h.sel.Get(cmd.URI)
	slog.Debug("handle code action command", "uri", cmd.URI, "selection-range", rng, "selection", ok)
	if !ok {
		cursorPos := CoordToPos(cmd.Cursor.Content)
		rng = semanticapi.Range{Start: cursorPos, End: cursorPos}
	} else {
		rng = ClampRange(rng, h.editor, cmd.Resource)
		slog.Debug("clamped selection range", "uri", cmd.URI, "selection-range", rng)
	}

	// An empty kind lists every action applicable at the cursor. Servers
	// group many actions under a handful of broad kinds (refactor.rewrite,
	// refactor.extract, ...), so a nil Only filter is how the user browses
	// the full menu rather than pre-selecting a single kind.
	var only []semanticapi.CodeActionKind
	if h.kind != "" {
		only = []semanticapi.CodeActionKind{h.kind}
	}
	params := semanticapi.CodeActionParams{
		TextDocument: TextDocID(cmd.URI),
		Range:        rng,
		Context: semanticapi.CodeActionContext{
			// rust-analyzer rejects a request whose context.diagnostics is
			// null, so always send an explicit (possibly empty) slice.
			Diagnostics: []semanticapi.Diagnostic{},
			Only:        only,
			TriggerKind: semanticapi.CodeActionTriggerKindInvoked,
		},
	}

	results, err := h.lsp.CodeAction(ctx, params)
	if err != nil {
		return fmt.Errorf("code action: %w", err)
	}

	var actions []semanticapi.CodeAction
	for _, r := range results {
		if r.CodeAction == nil {
			continue
		}
		if h.kind == "" || strings.HasPrefix(string(r.CodeAction.Kind), string(h.kind)) {
			actions = append(actions, *r.CodeAction)
		}
	}

	if len(actions) == 0 {
		_, _ = h.notify.Notify(browserapi.LevelInfo, h.noActionHint)
		return nil
	}

	if len(actions) == 1 && h.applyLoneAction {
		if a := actions[0]; a.Command == nil && (a.Edit == nil || !h.editChangesText(cmd.URI, a.Edit)) {
			_, _ = h.notify.Notify(browserapi.LevelInfo, h.noActionHint)
			return nil
		}
		return h.applyAction(ctx, cmd.URI, actions[0])
	}

	// A code action rewrites the buffer, so anything the command name
	// does not already pin down goes through the picker for the user to
	// see and confirm rather than being applied out from under them.
	ch := make(chan int, 1)
	picker := newCodeActionPicker(actions, ch)
	_, err = h.wm.Floating(picker, browserapi.FloatingConfig{
		Alignment: component.AlignmentCentered,
	})
	if err != nil {
		return fmt.Errorf("show picker: %w", err)
	}

	select {
	case idx := <-ch:
		if idx < 0 {
			return nil
		}
		return h.applyAction(ctx, cmd.URI, actions[idx])
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *codeActionCmd) applyAction(
	ctx context.Context, base workspaceapi.URI, action semanticapi.CodeAction,
) error {
	if action.Edit != nil && h.editChangesText(base, action.Edit) {
		err := ApplyWorkspaceEdit(ctx, h.editor, nil, base, action.Edit, nil)
		if err != nil {
			return err
		}
	}
	if action.Command != nil {
		_, err := h.lsp.ExecuteCommand(ctx, semanticapi.ExecuteCommandParams{
			Command:   action.Command.Command,
			Arguments: action.Command.Arguments,
		})
		if err != nil {
			return fmt.Errorf("execute command %s: %w", action.Command.Command, err)
		}
		_, _ = h.notify.Notify(browserapi.LevelInfo, "Executed: %s", action.Title)
	}
	return nil
}

// editChangesText reports whether edit rewrites any of the documents it
// touches. Servers answer refactorings such as source.organizeImports
// with a full rewrite of the affected region every time, even when the
// region is already in the shape they want; applying that dirties the
// buffer, pushes an undo entry and forces a re-parse for no change.
func (h *codeActionCmd) editChangesText(
	base workspaceapi.URI, edit *semanticapi.WorkspaceEdit,
) bool {
	if len(edit.DocumentChanges) > 0 {
		for _, dc := range edit.DocumentChanges {
			if dc.TextDocumentEdit == nil {
				return true
			}
			if h.uriEditsChangeText(base,
				dc.TextDocumentEdit.TextDocument.URI,
				dc.TextDocumentEdit.Edits) {
				return true
			}
		}
		return false
	}
	for uriStr, edits := range edit.Changes {
		if h.uriEditsChangeText(base, uriStr, edits) {
			return true
		}
	}
	return false
}

func (h *codeActionCmd) uriEditsChangeText(
	base workspaceapi.URI, uriStr string, edits []semanticapi.TextEdit,
) bool {
	uri, err := LspToURI(base, uriStr)
	if err != nil {
		return true
	}
	handler, err := h.editor.Editor(uri)
	if err != nil {
		return true
	}
	cv := h.editor.CellView(handler)
	if cv == nil {
		return true
	}
	cells, err := cv.RawCells()
	if err != nil {
		return true
	}
	return EditsChangeText(cells, edits)
}

func (h *codeActionCmd) Complete(_ context.Context, _ string, _ []string) (
	iterator.Iterator[string], error,
) {
	return iterator.Empty[string](), nil
}

// CodeActionPicker is the floating handler CodeActionHandler shows when
// several actions apply. A caller that observes floated windows can
// type-assert to it to inspect and drive the offered actions.
type CodeActionPicker interface {
	browserapi.Floating

	// Actions returns the code actions on offer, in the order they are
	// listed.
	Actions() []semanticapi.CodeAction
}

// codeActionPicker is a floating handler that presents a list of code
// actions for the user to choose from using keyboard navigation.
type codeActionPicker struct {
	actions        []semanticapi.CodeAction
	list           *component.FocusList
	ch             chan<- int
	idealW, idealH int
}

const (
	maxPickerHeight = 15
	minPickerWidth  = 40
	minPickerHeight = 3
)

var _ CodeActionPicker = (*codeActionPicker)(nil)

func newCodeActionPicker(
	actions []semanticapi.CodeAction, ch chan<- int,
) *codeActionPicker {
	list := component.NewFocusList()
	maxW := 0
	for _, a := range actions {
		list.PushBack(component.NewResponsiveString(
			a.Title, component.StringResponsiveConfig{},
		))
		if w := utf8.RuneCountInString(a.Title); w > maxW {
			maxW = w
		}
	}
	return &codeActionPicker{
		actions: actions,
		list:    list,
		ch:      ch,
		idealW:  max(maxW, minPickerWidth),
		idealH:  min(max(len(actions), minPickerHeight), maxPickerHeight),
	}
}

func (p *codeActionPicker) Actions() []semanticapi.CodeAction {
	return p.actions
}

func (p *codeActionPicker) Resize(width, height int) {
	p.list.Resize(width, height)
}

func (p *codeActionPicker) Draw(w term.Writer) {
	p.list.Draw(w)
}

func (p *codeActionPicker) Handle(ev term.Event) (exit, handled bool) {
	if ev.Type != term.EventKey {
		return false, false
	}
	switch ev.Key {
	case term.KeyEsc:
		p.ch <- -1
		return true, true
	case term.KeyEnter:
		idx := p.list.FocusOffset()
		if idx >= 0 && idx < len(p.actions) {
			p.ch <- idx
		} else {
			p.ch <- -1
		}
		return true, true
	case term.KeyArrowUp:
		p.list.FocusUp()
		return false, true
	case term.KeyArrowDown:
		p.list.FocusDown()
		return false, true
	}
	if ev.Mod == term.ModCtrl {
		switch ev.Ch {
		case 'j', 'n':
			p.list.FocusDown()
			return false, true
		case 'k', 'p':
			p.list.FocusUp()
			return false, true
		case 'c':
			p.ch <- -1
			return true, true
		}
	}
	return false, false
}

func (p *codeActionPicker) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return term.Coordinates{}, term.CursorStyleDefault, false
}

func (p *codeActionPicker) Selection() (string, bool) {
	return "", false
}

func (p *codeActionPicker) Close() error {
	select {
	case p.ch <- -1:
	default:
	}
	return nil
}

func (p *codeActionPicker) Dimensions() (int, int) {
	return p.idealW, p.idealH
}

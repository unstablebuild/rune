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
	"unicode/utf8"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/inputbox"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// RenameHandler creates a textapi.CommandHandler for the
// "rename" subcommand. It shows an inputbox under the cursor,
// pre-filled with the current symbol name, and applies the
// workspace edit returned by the language server.
func RenameHandler(
	lsp semanticapi.LSP, editor textapi.Editor, wm browserapi.WindowManager,
	opener browserapi.ResourceOpener, notify browserapi.Notifications,
	log *slog.Logger,
) textapi.CommandHandler {
	if notify == nil {
		panic("lspcmd: RenameHandler: notify must not be nil")
	}
	if log == nil {
		log = slog.Default()
	}
	return &renameHandler{
		lsp:    lsp,
		editor: editor,
		wm:     wm,
		opener: opener,
		notify: notify,
		log:    log,
	}
}

var _ textapi.CommandHandler = (*renameHandler)(nil)

type renameHandler struct {
	lsp    semanticapi.LSP
	editor textapi.Editor
	wm     browserapi.WindowManager
	opener browserapi.ResourceOpener
	notify browserapi.Notifications
	log    *slog.Logger
}

func (h *renameHandler) HandleCommand(ctx context.Context, cmd textapi.Command) error {
	if cmd.Resource == nil {
		return fmt.Errorf("no file open; open a file first")
	}
	pos := CoordToPos(cmd.Cursor.Content)

	prep, err := h.lsp.PrepareRename(ctx, semanticapi.PrepareRenameParams{
		TextDocument: TextDocID(cmd.URI),
		Position:     pos,
	})
	if err != nil {
		return fmt.Errorf("prepare rename: %w", err)
	}
	if prep == nil {
		return fmt.Errorf("rename not available at cursor position")
	}

	ib := inputbox.New(
		inputbox.WithPrompt(""),
		inputbox.WithText(prep.Placeholder),
	)

	ctx, cancel := context.WithCancel(context.Background())
	floating := &renameFloatingHandler{
		ib:       ib,
		lsp:      h.lsp,
		editor:   h.editor,
		opener:   h.opener,
		wm:       h.wm,
		notify:   h.notify,
		uri:      cmd.URI,
		position: pos,
		ctx:      ctx,
		cancel:   cancel,
		log:      h.log,
	}

	win, err := h.wm.Floating(floating, browserapi.FloatingConfig{
		Alignment: component.AlignmentLeft | component.AlignmentTop,
		Offset: term.Coordinates{
			X: cmd.Cursor.Window.X + 1,
			Y: cmd.Cursor.Window.Y + 2,
		},
	})
	if err != nil {
		return err
	}
	floating.win = win
	return nil
}

func (h *renameHandler) Complete(_ context.Context, _ string, _ []string) (
	iterator.Iterator[string], error,
) {
	return iterator.Empty[string](), nil
}

// renameFloatingHandler wraps an inputbox.Handler to
// implement browserapi.Floating. On confirmation it calls
// LSP rename and applies the returned workspace edit.
var _ browserapi.Floating = (*renameFloatingHandler)(nil)

type renameFloatingHandler struct {
	ib       *inputbox.Handler
	lsp      semanticapi.LSP
	editor   textapi.Editor
	opener   browserapi.ResourceOpener
	wm       browserapi.WindowManager
	win      browserapi.Window
	notify   browserapi.Notifications
	uri      workspaceapi.URI
	position semanticapi.Position
	ctx      context.Context
	cancel   context.CancelFunc
	log      *slog.Logger
}

func (r *renameFloatingHandler) Handle(ev term.Event) (bool, bool) {
	isEsc := ev.Type == term.EventKey && ev.Key == term.KeyEsc
	exit, handled := r.ib.Handle(ev)
	if !exit {
		return false, handled
	}
	if isEsc {
		return true, true
	}

	newName := r.ib.Text()
	if newName == "" {
		return true, true
	}

	edit, err := r.lsp.Rename(r.ctx, semanticapi.RenameParams{
		TextDocument: TextDocID(r.uri),
		Position:     r.position,
		NewName:      newName,
	})
	if err != nil {
		r.log.Warn("rename", "err", err)
		_, _ = r.notify.Notify(browserapi.LevelError, "rename: %s", err)
		return true, true
	}
	if edit == nil || renameEditEmpty(edit) {
		_, _ = r.notify.Notify(browserapi.LevelError, "rename: no edits returned")
		return true, true
	}

	err = ApplyWorkspaceEdit(r.ctx, r.editor, r.opener, r.uri, edit, r.log)
	if err != nil {
		r.log.Warn("rename apply", "err", err)
		_, _ = r.notify.Notify(browserapi.LevelError, "rename: %s", err)
		return true, true
	}
	return true, true
}

func renameEditEmpty(edit *semanticapi.WorkspaceEdit) bool {
	for _, dc := range edit.DocumentChanges {
		if dc.TextDocumentEdit != nil && len(dc.TextDocumentEdit.Edits) > 0 {
			return false
		}
		if dc.CreateFile != nil || dc.RenameFile != nil || dc.DeleteFile != nil {
			return false
		}
	}
	for _, edits := range edit.Changes {
		if len(edits) > 0 {
			return false
		}
	}
	return true
}

func (r *renameFloatingHandler) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return r.ib.Cursor()
}

func (r *renameFloatingHandler) Selection() (string, bool) {
	return r.ib.Selection()
}

func (r *renameFloatingHandler) Resize(width, height int) {
	r.ib.Resize(width, height)
}

func (r *renameFloatingHandler) Draw(w term.Writer) {
	r.ib.Draw(w)
}

func (r *renameFloatingHandler) Dimensions() (int, int) {
	textW := utf8.RuneCountInString(r.ib.Text())
	return max(textW+2, 30), 1
}

func (r *renameFloatingHandler) Close() error {
	r.cancel()
	if r.win != nil {
		return r.wm.CloseWindow(r.win)
	}
	return nil
}

// ApplyWorkspaceEdit applies a WorkspaceEdit by routing
// each file's text edits through the editor API.
// Per the LSP spec, DocumentChanges is preferred over
// Changes when both are present.
// base is any URI of the workspace the edit belongs to; it is used to
// rebase the server's file:// document URIs onto the workspace scheme.
func ApplyWorkspaceEdit(
	ctx context.Context, editor textapi.Editor,
	opener browserapi.ResourceOpener,
	base workspaceapi.URI,
	edit *semanticapi.WorkspaceEdit,
	log *slog.Logger,
) error {
	if log == nil {
		log = slog.Default()
	}
	if len(edit.DocumentChanges) > 0 {
		for _, dc := range edit.DocumentChanges {
			switch {
			case dc.TextDocumentEdit != nil:
				err := ApplyEditsForURI(ctx, editor, opener, base,
					dc.TextDocumentEdit.TextDocument.URI, dc.TextDocumentEdit.Edits,
				)
				if err != nil {
					return err
				}
			case dc.CreateFile != nil:
				log.Warn("rename: create file not supported", "uri", dc.CreateFile.URI)
			case dc.RenameFile != nil:
				log.Warn("rename: file rename not supported",
					"old", dc.RenameFile.OldURI,
					"new", dc.RenameFile.NewURI)
			case dc.DeleteFile != nil:
				log.Warn("rename: delete file not supported", "uri", dc.DeleteFile.URI)
			}
		}
		return nil
	}
	for uriStr, edits := range edit.Changes {
		err := ApplyEditsForURI(ctx, editor, opener, base, uriStr, edits)
		if err != nil {
			return err
		}
	}
	return nil
}

// ApplyEditsForURI applies the given text edits to the document identified by uriStr.
func ApplyEditsForURI(
	ctx context.Context, editor textapi.Editor,
	opener browserapi.ResourceOpener,
	base workspaceapi.URI,
	uriStr string, edits []semanticapi.TextEdit,
) error {
	uri, err := LspToURI(base, uriStr)
	if err != nil {
		return fmt.Errorf("parse URI %s: %w", uriStr, err)
	}
	handler, err := editor.Editor(uri)
	if err != nil && opener != nil {
		if _, openErr := opener.Open(uri); openErr == nil {
			handler, err = editor.Editor(uri)
		}
	}
	if err != nil {
		return fmt.Errorf("editor for %s: %w", uri.Name(), err)
	}
	ce := editor.CellEditor(handler)
	if err := ApplyEdits(ctx, ce, edits); err != nil {
		return fmt.Errorf("apply edits for %s: %w", uri.Name(), err)
	}
	return nil
}

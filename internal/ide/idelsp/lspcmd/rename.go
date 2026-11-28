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

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/iterator"
)

// RenameHandler creates a textapi.CommandHandler for the "rename"
// subcommand. It asks for the new name in a prompt, starting from the
// current symbol name, and applies the workspace edit returned by the
// language server.
func RenameHandler(
	lsp semanticapi.LSP, editor textapi.Editor, prompter browserapi.Prompter,
	opener browserapi.ResourceOpener,
	log *slog.Logger,
) textapi.CommandHandler {
	if log == nil {
		log = slog.Default()
	}
	return &renameHandler{
		lsp:      lsp,
		editor:   editor,
		prompter: prompter,
		opener:   opener,
		log:      log,
	}
}

var _ textapi.CommandHandler = (*renameHandler)(nil)

type renameHandler struct {
	lsp      semanticapi.LSP
	editor   textapi.Editor
	prompter browserapi.Prompter
	opener   browserapi.ResourceOpener
	log      *slog.Logger
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

	uri := cmd.URI
	// The message asks the question, which leaves the field unlabeled.
	message := "Rename the symbol to:"
	if prep.Placeholder != "" {
		message = fmt.Sprintf("Rename `%s` to:", prep.Placeholder)
	}
	_, err = h.prompter.Prompt(browserapi.PromptConfig{
		Message: message,
		Options: []string{"Rename"},
		Fields: []browserapi.Field{
			{Answer: prep.Placeholder, Placeholder: "New name", Mandatory: true},
		},
	}, handler.FuncAnswerHandler(
		func(_ int, _ string, answers []string) {
			h.rename(uri, pos, strings.TrimSpace(answers[0]))
		},
		func() error { return nil },
	))
	return err
}

// rename renames the symbol at position in uri to newName and applies
// the edit. It runs on the event loop, where the prompt answers.
func (h *renameHandler) rename(
	uri workspaceapi.URI, position semanticapi.Position, newName string,
) {
	ctx := context.Background()
	edit, err := h.lsp.Rename(ctx, semanticapi.RenameParams{
		TextDocument: TextDocID(uri),
		Position:     position,
		NewName:      newName,
	})
	if err != nil {
		h.log.Warn("rename", "err", err)
		return
	}
	if edit == nil {
		return
	}
	if err := ApplyWorkspaceEdit(ctx, h.editor, h.opener, uri, edit, h.log); err != nil {
		h.log.Warn("rename apply", "err", err)
	}
}

func (h *renameHandler) Complete(_ context.Context, _ string, _ []string) (
	iterator.Iterator[string], error,
) {
	return iterator.Empty[string](), nil
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

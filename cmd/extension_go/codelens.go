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
	"math"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/debug"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/ide/idelsp/lspcmd"
)

func codeLensHandler(
	lsp semanticapi.LSP,
	notify browserapi.Notifications,
	commandName string,
) textapi.CommandHandler {
	return &codeLensCmd{lsp: lsp, notify: notify, commandName: commandName}
}

var _ textapi.CommandHandler = (*codeLensCmd)(nil)

type codeLensCmd struct {
	lsp         semanticapi.LSP
	notify      browserapi.Notifications
	commandName string
}

func (h *codeLensCmd) HandleCommand(
	ctx context.Context, cmd textapi.Command,
) error {
	if cmd.Resource == nil {
		return fmt.Errorf("no file open; open a file first")
	}

	params := semanticapi.CodeLensParams{
		TextDocument: lspcmd.TextDocID(cmd.URI),
	}
	lenses, err := h.lsp.CodeLens(ctx, params)
	if err != nil {
		return fmt.Errorf("code lens: %w", err)
	}

	cursorLine := uint32(cmd.Cursor.Content.Y)
	var nearest *semanticapi.CodeLens
	minDist := uint32(math.MaxUint32)
	for i := range lenses {
		lens := &lenses[i]
		if lens.Command == nil || lens.Command.Command != h.commandName {
			continue
		}
		dist := absDiff(lens.Range.Start.Line, cursorLine)
		if dist < minDist {
			minDist = dist
			nearest = lens
		}
	}

	if nearest == nil || nearest.Command == nil {
		_, _ = h.notify.Notify(browserapi.LevelInfo, "No %s lens found", h.commandName)
		return nil
	}

	command := *nearest.Command
	// The request ctx is canceled when HandleCommand returns, before the
	// command completes.
	ctx = context.WithoutCancel(ctx)
	go debug.CapturePanicReport(func() {
		result, err := h.lsp.ExecuteCommand(ctx, semanticapi.ExecuteCommandParams{
			Command:   command.Command,
			Arguments: command.Arguments,
		})
		if err != nil {
			_, _ = h.notify.Notify(browserapi.LevelError, "execute %s: %s", h.commandName, err)
			return
		}

		if result != "" {
			_, _ = h.notify.Notify(browserapi.LevelInfo, "%s", result)
		} else {
			_, _ = h.notify.Notify(browserapi.LevelInfo, "Executed: %s", command.Title)
		}
	})
	return nil
}

func (h *codeLensCmd) Complete(
	_ context.Context, _ string, _ []string,
) (iterator.Iterator[string], error) {
	return iterator.Empty[string](), nil
}

func absDiff(a, b uint32) uint32 {
	if a > b {
		return a - b
	}
	return b - a
}

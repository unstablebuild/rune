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

package extension

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/cmd/rune-agent/agent/agentools"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguetui"
	"unstable.build/rune/internal/debug"
)

// cursorLocation is the last known caret position in an editable
// resource. chataddsymbol runs from the command prompt, by which point
// the prompt owns the caret, so the position has to be mirrored from
// cursor events as they happen.
type cursorLocation struct {
	uri workspaceapi.URI
	pos term.Coordinates
}

type cursorEditor interface {
	Editor(workspaceapi.URI) (textapi.Handler, error)
	CellView(textapi.Handler) textapi.CellView
	SetCursor(textapi.Handler, term.Coordinates) error
}

// recordCursor mirrors a cursor event. Chat tabs are not text
// resources, so every event here comes from an editable buffer.
func (h *aiEditorHandler) recordCursor(ev textapi.Event) {
	h.lastCursor.Store(&cursorLocation{uri: ev.URI, pos: ev.From})
}

// recordFocusedChat remembers which chat should receive workspace
// commands that are invoked from outside a chat tab.
func (h *aiEditorHandler) recordFocusedChat(id string) {
	if id == "" {
		return
	}
	h.lastChatID.Store(&id)
}

func (h *aiEditorHandler) focusedChatTx() (chan<- dialoguetui.MessageEvent, error) {
	id, _ := h.lastChatID.Load().(*string)
	if id == nil || *id == "" {
		return nil, fmt.Errorf("no agent chat is open")
	}
	v, ok := h.openChatTx.Load(*id)
	if !ok {
		return nil, fmt.Errorf("agent chat %s is no longer open", *id)
	}
	return v.(chan<- dialoguetui.MessageEvent), nil
}

// handleChatAddSymbol attaches the symbol under the caret to the chat
// that was last in focus, so the next message carries its definition,
// references and documentation.
func (h *aiEditorHandler) handleChatAddSymbol(cmd textapi.Command) error {
	if len(cmd.Args) > 1 {
		return fmt.Errorf("%s takes at most one symbol", cmd.Name)
	}
	if len(cmd.Args) == 1 {
		tx, err := h.focusedChatTx()
		if err != nil {
			return err
		}
		a := dialoguetui.NewSymbolAttachment(cmd.Args[0])
		go debug.CapturePanicReport(func() {
			h.sendSymbolAttachment(tx, a)
		})
		return nil
	}
	loc, _ := h.lastCursor.Load().(*cursorLocation)
	if loc == nil {
		return fmt.Errorf("no cursor position recorded yet")
	}
	symbol, pos, err := h.symbolAtCursor(*loc)
	if err != nil {
		return err
	}
	tx, err := h.focusedChatTx()
	if err != nil {
		return err
	}
	go debug.CapturePanicReport(func() {
		a, err := h.resolveSymbolAttachment(h.ctx, symbol, loc.uri, pos)
		if err != nil {
			_, _ = h.n.Notify(browserapi.LevelError, "%s: %v", cmd.Name, err)
			return
		}
		h.sendSymbolAttachment(tx, a)
	})
	return nil
}

func (h *aiEditorHandler) sendSymbolAttachment(
	tx chan<- dialoguetui.MessageEvent, a dialoguetui.Attachment,
) {
	select {
	case tx <- dialoguetui.MessageEvent{
		Type:       dialoguetui.MessageEventAttachment,
		Attachment: a,
	}:
	case <-h.ctx.Done():
	}
}

func (h *aiEditorHandler) resolveSymbolAttachment(
	ctx context.Context, symbol string, uri workspaceapi.URI, pos term.Coordinates,
) (dialoguetui.Attachment, error) {
	resolved, err := agentools.SymbolContextAtPosition(
		ctx, h.lsp, h.fs, h.cwd, uri, semanticapi.Position{
			Line: uint32(pos.Y), Character: uint32(pos.X),
		},
	)
	if err != nil {
		return dialoguetui.Attachment{}, err
	}
	content := strings.Join([]string{
		"## Definition\n" + locationList(resolved.Definition),
		"## References\n" + locationList(resolved.References),
		"## Documentation\n" + resolved.Documentation,
	}, "\n\n")
	return dialoguetui.NewResolvedSymbolAttachment(symbol, content), nil
}

func (h *aiEditorHandler) symbolAtCursor(
	loc cursorLocation,
) (string, term.Coordinates, error) {
	eh, err := h.ed.Editor(loc.uri)
	if err != nil {
		return "", term.Coordinates{}, fmt.Errorf("editor for %s: %v", loc.uri, err)
	}
	rows, err := h.ed.CellView(eh).RawCells()
	if err != nil {
		return "", term.Coordinates{}, fmt.Errorf("read %s: %v", loc.uri, err)
	}
	word := wordAt(rows, loc.pos)
	if word == "" {
		return "", term.Coordinates{}, fmt.Errorf("no symbol under the cursor")
	}
	return word, loc.pos, nil
}

// wordAt returns the qualified name at pos: the identifier under the
// caret, prefixed by the dot-separated container path to its left. The
// symbol tools resolve any trailing part of a container path, so
// keeping the prefix is what tells Order.total apart from every other
// total. Segments to the right are dropped because they name something
// the caret is not pointing at.
func wordAt(rows [][]term.Cell, pos term.Coordinates) string {
	if pos.Y < 0 || pos.Y >= len(rows) {
		return ""
	}
	row := rows[pos.Y]
	if pos.X < 0 || pos.X >= len(row) {
		return ""
	}
	x := pos.X
	if row[x].Ch == '.' {
		// A caret on the separator names the segment it introduces,
		// falling back to the one it terminates while a path is still
		// being typed.
		switch {
		case x+1 < len(row) && isSymbolRune(row[x+1].Ch):
			x++
		case x > 0 && isSymbolRune(row[x-1].Ch):
			x--
		}
	}
	if !isSymbolRune(row[x].Ch) {
		return ""
	}
	start, end := x, x
	for start > 0 && isSymbolRune(row[start-1].Ch) {
		start--
	}
	for end < len(row) && isSymbolRune(row[end].Ch) {
		end++
	}
	for start > 1 && row[start-1].Ch == '.' && isSymbolRune(row[start-2].Ch) {
		start -= 2
		for start > 0 && isSymbolRune(row[start-1].Ch) {
			start--
		}
	}
	var b strings.Builder
	for i := start; i < end; i++ {
		b.WriteRune(row[i].Ch)
	}
	return b.String()
}

func isSymbolRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

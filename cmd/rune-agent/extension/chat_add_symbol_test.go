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
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"

	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguetui"
	"unstable.build/rune/internal/browser/browsertest"
)

func cellRows(lines ...string) [][]term.Cell {
	rows := make([][]term.Cell, len(lines))
	for i, line := range lines {
		for _, r := range line {
			rows[i] = append(rows[i], term.Cell{Ch: r})
		}
	}
	return rows
}

func TestWordAt(t *testing.T) {
	//                     0    5    10   15   20   25   30   35   40
	//                     |....|....|....|....|....|....|....|....|
	const decl = "func handleChatAddSymbol(cmd textapi.Command) error {"
	const chain = "  pkg.Symbol.Method(x)"
	const method = "  x().Method()"
	const partial = "  pkg."
	const blanks = "a\tb c\x00d"
	rows := cellRows(decl, "", "  x := foo_bar1 + 2", chain, method, partial, blanks)

	tests := []struct {
		name string
		pos  term.Coordinates
		want string
	}{
		{"start of word", term.Coordinates{X: 5, Y: 0}, "handleChatAddSymbol"},
		{"middle of word", term.Coordinates{X: 12, Y: 0}, "handleChatAddSymbol"},
		{"last rune of word", term.Coordinates{X: 23, Y: 0}, "handleChatAddSymbol"},
		{"on punctuation", term.Coordinates{X: 24, Y: 0}, ""},
		{"underscores and digits", term.Coordinates{X: 9, Y: 2}, "foo_bar1"},
		{"empty line", term.Coordinates{X: 0, Y: 1}, ""},
		{"row out of range", term.Coordinates{X: 0, Y: 9}, ""},
		{"column out of range", term.Coordinates{X: 99, Y: 0}, ""},
		{"negative", term.Coordinates{X: -1, Y: -1}, ""},

		// A caret on a container segment names that container, not the
		// members hanging off it.
		{"container segment", term.Coordinates{X: 3, Y: 3}, "pkg"},
		{"member keeps its container", term.Coordinates{X: 8, Y: 3}, "pkg.Symbol"},
		{"method keeps the whole path", term.Coordinates{X: 15, Y: 3}, "pkg.Symbol.Method"},
		{"last rune of method", term.Coordinates{X: 18, Y: 3}, "pkg.Symbol.Method"},
		{"separator names the segment it introduces",
			term.Coordinates{X: 5, Y: 3}, "pkg.Symbol"},
		{"second separator", term.Coordinates{X: 12, Y: 3}, "pkg.Symbol.Method"},
		{"qualifier of a call result is dropped",
			term.Coordinates{X: 7, Y: 4}, "Method"},
		{"separator after a call result",
			term.Coordinates{X: 5, Y: 4}, "Method"},
		{"trailing separator falls back to the left",
			term.Coordinates{X: 5, Y: 5}, "pkg"},

		{"on a tab", term.Coordinates{X: 1, Y: 6}, ""},
		{"on a space", term.Coordinates{X: 3, Y: 6}, ""},
		{"on a NUL", term.Coordinates{X: 5, Y: 6}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, wordAt(rows, tc.pos))
		})
	}

	t.Run("qualified type in a signature", func(t *testing.T) {
		x := strings.Index(decl, "Command") + 2
		assert.Equal(t, "textapi.Command", wordAt(rows, term.Coordinates{X: x}))
	})
}

type cursorTestHandler struct {
	*browsertest.TestHandler
	uri workspaceapi.URI
}

func (h cursorTestHandler) Resource() workspaceapi.URI { return h.uri }

type cursorTestView struct{ rows [][]term.Cell }

func (v cursorTestView) RawCells() ([][]term.Cell, error) { return v.rows, nil }

type cursorTestEditor struct {
	h    textapi.Handler
	view textapi.CellView
}

func (e cursorTestEditor) Editor(workspaceapi.URI) (textapi.Handler, error) { return e.h, nil }
func (e cursorTestEditor) CellView(textapi.Handler) textapi.CellView        { return e.view }
func (e cursorTestEditor) SetCursor(textapi.Handler, term.Coordinates) error {
	return errors.New("unexpected SetCursor")
}

func TestHandleChatAddSymbolDeliversExactCursorContext(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "scope.go")
	source := "package scope\nfunc f() {\n\tvalue := 1\n\t_ = value\n}"
	require.NoError(t, os.WriteFile(path, []byte(source), 0o600))
	uri, err := workspaceapi.ParseURI("file://" + path)
	require.NoError(t, err)
	rootURI, err := workspaceapi.ParseURI("file://" + root)
	require.NoError(t, err)
	windowPos := term.Coordinates{X: 6, Y: 0}
	contentPos := term.Coordinates{X: 6, Y: 3}
	lspPos := semanticapi.Position{Line: 3, Character: 6}
	assertParams := func(doc semanticapi.TextDocumentIdentifier, got semanticapi.Position) {
		assert.Equal(t, "file://"+path, doc.URI)
		assert.Equal(t, lspPos, got)
	}
	lsp := stubLSP{
		workspaceSymbolFn: func(semanticapi.WorkspaceSymbolParams) ([]semanticapi.SymbolInformation, error) {
			t.Fatal("chataddsymbol must not call WorkspaceSymbol")
			return nil, nil
		},
		definitionFn: func(p semanticapi.DefinitionParams) (semanticapi.LocationResult, error) {
			assertParams(p.TextDocument, p.Position)
			loc := semanticapi.Location{URI: "file://" + path,
				Range: semanticapi.Range{Start: semanticapi.Position{Line: 2, Character: 1}}}
			return semanticapi.LocationResult{Location: &loc}, nil
		},
		referencesFn: func(p semanticapi.ReferenceParams) ([]semanticapi.Location, error) {
			assertParams(p.TextDocument, p.Position)
			return []semanticapi.Location{{URI: "file://" + path,
				Range: semanticapi.Range{Start: semanticapi.Position{Line: 3, Character: 6}}}}, nil
		},
		hoverFn: func(p semanticapi.HoverParams) (*semanticapi.Hover, error) {
			assertParams(p.TextDocument, p.Position)
			return &semanticapi.Hover{Contents: semanticapi.MarkupContent{
				Value: "```go\nvar value int\n```",
			}}, nil
		},
	}
	th := cursorTestHandler{TestHandler: browsertest.NewTestHandler(), uri: uri}
	ch := make(chan dialoguetui.MessageEvent, 1)
	h := &aiEditorHandler{
		ctx: t.Context(), fs: testLocalFS{root: root}, cwd: rootURI,
		lsp: lsp, n: stubNotifications{},
		ed: cursorTestEditor{h: th, view: cursorTestView{
			rows: cellRows(strings.Split(source, "\n")...),
		}},
	}
	h.openChatTx.Store("rolling-fox", (chan<- dialoguetui.MessageEvent)(ch))
	h.recordFocusedChat("rolling-fox")
	h.recordCursor(textapi.Event{URI: uri, Start: windowPos, From: contentPos})

	require.NoError(t, h.handleChatAddSymbol(textapi.Command{Name: commandAddSymbol}))

	select {
	case ev := <-ch:
		assert.Equal(t, dialoguetui.MessageEventAttachment, ev.Type)
		assert.Equal(t, dialoguetui.AttachmentSymbol, ev.Attachment.Kind)
		assert.Equal(t, "value", ev.Attachment.Symbol)
		assert.Equal(t, strings.Join([]string{
			"## Definition",
			"- `scope.go:3`",
			"",
			"## References",
			"- `scope.go:4:\t_ = value`",
			"",
			"## Documentation",
			"```go",
			"var value int",
			"```",
		}, "\n"), ev.Attachment.Content)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the symbol attachment event")
	}
}

func TestHandleChatAddSymbolWithNameUsesNameBasedAttachment(t *testing.T) {
	ch := make(chan dialoguetui.MessageEvent, 1)
	h := &aiEditorHandler{ctx: t.Context()}
	h.openChatTx.Store("rolling-fox", (chan<- dialoguetui.MessageEvent)(ch))
	h.recordFocusedChat("rolling-fox")

	require.NoError(t, h.handleChatAddSymbol(textapi.Command{
		Name: commandAddSymbol, Args: []string{"pkg.Symbol"},
	}))

	select {
	case ev := <-ch:
		assert.Equal(t, dialoguetui.MessageEventAttachment, ev.Type)
		assert.Equal(t, dialoguetui.NewSymbolAttachment("pkg.Symbol"), ev.Attachment)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the symbol attachment event")
	}
}

func TestHandleChatAddSymbolErrors(t *testing.T) {
	ctx := context.Background()
	h := &aiEditorHandler{ctx: ctx}

	// At most one name is accepted.
	require.Error(t, h.handleChatAddSymbol(textapi.Command{
		Name: commandAddSymbol, Args: []string{"pkg.Symbol", "other.Symbol"},
	}))

	// A cursor is required before chat lookup or semantic resolution.
	h.openChatTx.Store("rolling-fox",
		(chan<- dialoguetui.MessageEvent)(make(chan dialoguetui.MessageEvent, 1)))
	require.Error(t, h.handleChatAddSymbol(textapi.Command{Name: commandAddSymbol}))
}

func TestHandleChatAddSymbolRejectsWhitespaceAtCursor(t *testing.T) {
	uri, err := workspaceapi.ParseURI("file:///workspace/scope.go")
	require.NoError(t, err)
	pos := term.Coordinates{X: 5}
	th := cursorTestHandler{TestHandler: browsertest.NewTestHandler(), uri: uri}
	h := &aiEditorHandler{
		ctx: t.Context(),
		ed: cursorTestEditor{h: th, view: cursorTestView{
			rows: cellRows("value other"),
		}},
	}
	h.recordCursor(textapi.Event{URI: uri, From: pos})

	err = h.handleChatAddSymbol(textapi.Command{Name: commandAddSymbol})
	require.ErrorContains(t, err, "no symbol under the cursor")
}

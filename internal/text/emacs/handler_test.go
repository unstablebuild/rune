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

package emacs

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/handler/handlertest"
	"unstable.build/rune/internal/ide/syntax/treesitter"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/registerhistory"
	"unstable.build/rune/internal/text/registerset"
)

var _ foldsService = (*treesitter.Tree)(nil)

type testSelectionService struct {
	view   cell.View
	expand map[term.Range]term.Range
	shrink map[term.Range]term.Range
}

type testCommentService struct {
	view  cell.View
	line  []string
	block []string
}

type testMacroRecorder struct {
	started   []string
	stopped   int
	recording bool
}

func (r *testMacroRecorder) Start(registerID string) {
	r.started = append(r.started, registerID)
	r.recording = true
}

func (r *testMacroRecorder) Stop() {
	r.stopped++
	r.recording = false
}

func (r *testMacroRecorder) IsRecording() bool {
	return r.recording
}

type testMacroPlayer struct {
	plays   []testMacroPlay
	err     error
	playing bool
}

type testStatusBar struct {
	status string
	attrs  term.Attributes
}

func (b *testStatusBar) SetStatus(status string, attrs term.Attributes) {
	b.status = status
	b.attrs = attrs
}

type testMacroPlay struct {
	registerID string
	count      int
}

func (p *testMacroPlayer) Play(registerID string, count int) error {
	p.plays = append(p.plays, testMacroPlay{registerID: registerID, count: count})
	return p.err
}

func (p *testMacroPlayer) IsPlaying() bool {
	return p.playing
}

func (s testSelectionService) Rows() int { return s.view.Rows() }

func (s testSelectionService) Columns(row int) int { return s.view.Columns(row) }

func (s testSelectionService) Cell(at term.Coordinates) (term.Cell, bool) {
	return s.view.Cell(at)
}

func (s testSelectionService) RawCells() [][]term.Cell { return s.view.RawCells() }

func (s testSelectionService) String() string { return s.view.String() }

func (s testSelectionService) SelectionExpand(rng term.Range) (term.Range, bool) {
	next, ok := s.expand[rng]
	return next, ok
}

func (s testSelectionService) SelectionShrink(rng term.Range, caret term.Coordinates) (term.Range, bool) {
	next, ok := s.shrink[rng]
	return next, ok
}

func (s testCommentService) Rows() int { return s.view.Rows() }

func (s testCommentService) Columns(row int) int { return s.view.Columns(row) }

func (s testCommentService) Cell(at term.Coordinates) (term.Cell, bool) { return s.view.Cell(at) }

func (s testCommentService) RawCells() [][]term.Cell { return s.view.RawCells() }

func (s testCommentService) String() string { return s.view.String() }

// SelectionExpand lets the emacs handler tests exercise wiring that
// targets a selection service. The default behavior expands an empty
// caret range by one column so the tests do not need a full syntactic
// selection implementation.
func (s testCommentService) SelectionExpand(rng term.Range) (term.Range, bool) {
	if rng.Start == rng.End {
		end := rng.End
		end.X++
		return term.Range{Start: rng.Start, End: end}, true
	}
	return term.Range{}, false
}

func (s testCommentService) SelectionShrink(
	rng term.Range, caret term.Coordinates,
) (term.Range, bool) {
	return term.Range{Start: caret, End: caret}, true
}

func (s testCommentService) CommentCoverage(rng term.Range) ([]term.Range, bool) {
	start, end := term.CoordinatesSort(rng.Start, rng.End)
	var ranges []term.Range
	for y := start.Y; y <= end.Y; y++ {
		line := term.CellsToString([][]term.Cell{s.view.RawCells()[y]})
		trimmed := strings.TrimLeft(line, " \t")
		indent := len(line) - len(trimmed)
		for _, prefix := range s.line {
			if strings.HasPrefix(trimmed, prefix) {
				ranges = append(ranges, term.Range{
					Start: term.Coordinates{Y: y, X: indent},
					End:   term.Coordinates{Y: y, X: len(line)},
				})
				goto nextLine
			}
			if strings.HasPrefix(trimmed, prefix+" ") {
				ranges = append(ranges, term.Range{
					Start: term.Coordinates{Y: y, X: indent},
					End:   term.Coordinates{Y: y, X: len(line)},
				})
				goto nextLine
			}
		}
		for i := 0; i+1 < len(s.block); i += 2 {
			open, close := s.block[i], s.block[i+1]
			openIdx := strings.Index(line, open)
			closeIdx := strings.LastIndex(line, close)
			if openIdx >= 0 && closeIdx >= openIdx+len(open) {
				ranges = append(ranges, term.Range{
					Start: term.Coordinates{Y: y, X: openIdx},
					End:   term.Coordinates{Y: y, X: closeIdx + len(close)},
				})
				goto nextLine
			}
		}
		return nil, false
	nextLine:
	}
	if len(ranges) == 0 {
		return nil, false
	}
	return ranges, true
}

func TestCursorExternalEdit(t *testing.T) {
	uri, err := workspaceapi.ParseURI("test:///")
	require.NoError(t, err)

	t.Run("if external insert above, moves cursor to keep cursor in current logical line", func(t *testing.T) {
		width, height := 20, 10

		buf := cell.NewBuffer()
		buf.ReadFrom(strings.NewReader(snippet))
		vi := NewHandler(buf, uri, text.IndentRuneTab, 0)
		vi.Resize(width, height)

		_, handled := vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowDown})
		require.True(t, handled)
		_, handled = vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowDown})
		require.True(t, handled)

		assert.Equal(t, term.Coordinates{Y: 2}, vi.CursorAtScroll())
		at := term.Coordinates{}
		vi.CellEditor().Edit(context.Background(), at, at, "a\nb")
		assert.Equal(t, term.Coordinates{Y: 3}, vi.CursorAtScroll())
	})

	t.Run("if external insert below, it does nothing", func(t *testing.T) {
		width, height := 20, 10

		buf := cell.NewBuffer()
		buf.ReadFrom(strings.NewReader(snippet))
		vi := NewHandler(buf, uri, text.IndentRuneTab, 0)
		vi.Resize(width, height)

		_, handled := vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowDown})
		require.True(t, handled)
		_, handled = vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowDown})
		require.True(t, handled)

		assert.Equal(t, term.Coordinates{Y: 2}, vi.CursorAtScroll())
		at := term.Coordinates{Y: 3}
		vi.CellEditor().Edit(context.Background(), at, at, "a\nb")
		assert.Equal(t, term.Coordinates{Y: 2}, vi.CursorAtScroll())
	})

	t.Run("if external delete below, it does nothing", func(t *testing.T) {
		width, height := 20, 10

		buf := cell.NewBuffer()
		buf.ReadFrom(strings.NewReader(snippet))
		vi := NewHandler(buf, uri, text.IndentRuneTab, 0)
		vi.Resize(width, height)

		_, handled := vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowDown})
		require.True(t, handled)
		_, handled = vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowDown})
		require.True(t, handled)

		assert.Equal(t, term.Coordinates{Y: 2}, vi.CursorAtScroll())
		from := term.Coordinates{Y: 3}
		to := term.Coordinates{Y: 4}
		vi.CellEditor().Edit(context.Background(), from, to, "")
		assert.Equal(t, term.Coordinates{Y: 2}, vi.CursorAtScroll())
	})

	t.Run("if external delete above, it keeps cursor at logical line", func(t *testing.T) {
		width, height := 20, 10

		buf := cell.NewBuffer()
		buf.ReadFrom(strings.NewReader(snippet))
		vi := NewHandler(buf, uri, text.IndentRuneTab, 0)
		vi.Resize(width, height)

		_, handled := vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowDown})
		require.True(t, handled)
		_, handled = vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowDown})
		require.True(t, handled)

		assert.Equal(t, term.Coordinates{Y: 2}, vi.CursorAtScroll())
		from := term.Coordinates{Y: 0}
		to := term.Coordinates{Y: 1}
		vi.CellEditor().Edit(context.Background(), from, to, "")
		assert.Equal(t, term.Coordinates{Y: 1}, vi.CursorAtScroll())
	})

	t.Run("if external delete to current line, it keeps cursor at logical line", func(t *testing.T) {
		width, height := 20, 10

		buf := cell.NewBuffer()
		buf.ReadFrom(strings.NewReader(snippet))
		vi := NewHandler(buf, uri, text.IndentRuneTab, 0)
		vi.Resize(width, height)

		_, handled := vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowDown})
		require.True(t, handled)
		_, handled = vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowDown})
		require.True(t, handled)
		_, handled = vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowRight})
		require.True(t, handled)

		assert.Equal(t, term.Coordinates{Y: 2, X: 1}, vi.CursorAtScroll())
		from := term.Coordinates{Y: 0}
		to := term.Coordinates{Y: 2, X: 5}
		vi.CellEditor().Edit(context.Background(), from, to, "")
		assert.Equal(t, term.Coordinates{Y: 0, X: 0}, vi.CursorAtScroll())
	})

	t.Run("if external insert to current line, it keeps cursor at logical line", func(t *testing.T) {
		width, height := 20, 10

		buf := cell.NewBuffer()
		buf.ReadFrom(strings.NewReader(snippet))
		vi := NewHandler(buf, uri, text.IndentRuneTab, 0)
		vi.Resize(width, height)

		_, handled := vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowDown})
		require.True(t, handled)
		_, handled = vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowDown})
		require.True(t, handled)
		_, handled = vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowRight})
		require.True(t, handled)

		assert.Equal(t, term.Coordinates{Y: 2, X: 1}, vi.CursorAtScroll())
		at := term.Coordinates{Y: 2, X: 0}
		vi.CellEditor().Edit(context.Background(), at, at, "a\nbbb")
		assert.Equal(t, term.Coordinates{Y: 3, X: 3}, vi.CursorAtScroll())
	})

	t.Run("if external replace to current line, it keeps cursor at logical line", func(t *testing.T) {
		width, height := 20, 10

		buf := cell.NewBuffer()
		buf.ReadFrom(strings.NewReader(snippet))
		vi := NewHandler(buf, uri, text.IndentRuneTab, 0)
		vi.Resize(width, height)

		_, handled := vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowDown})
		require.True(t, handled)
		_, handled = vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowDown})
		require.True(t, handled)
		_, handled = vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowRight})
		require.True(t, handled)

		assert.Equal(t, term.Coordinates{Y: 2, X: 1}, vi.CursorAtScroll())
		start := term.Coordinates{Y: 1, X: 0}
		end := term.Coordinates{Y: 2, X: 1}
		vi.CellEditor().Edit(context.Background(), start, end, "a\nbbb")
		assert.Equal(t, term.Coordinates{Y: 2, X: 3}, vi.CursorAtScroll())
	})

	t.Run("if external replace only cols to current line, it keeps cursor at logical position", func(t *testing.T) {
		width, height := 20, 10

		buf := cell.NewBuffer()
		buf.ReadFrom(strings.NewReader(snippet))
		vi := NewHandler(buf, uri, text.IndentRuneTab, 0)
		vi.Resize(width, height)

		_, handled := vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowDown})
		require.True(t, handled)
		_, handled = vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowDown})
		require.True(t, handled)
		for range 3 {
			_, handled = vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowRight})
			require.True(t, handled)
		}

		assert.Equal(t, term.Coordinates{Y: 2, X: 3}, vi.CursorAtScroll())
		start := term.Coordinates{Y: 2, X: 0}
		end := term.Coordinates{Y: 2, X: 4}
		vi.CellEditor().Edit(context.Background(), start, end, "bbb")
		assert.Equal(t, term.Coordinates{Y: 2, X: 3}, vi.CursorAtScroll())
	})
}

func TestEmacsCursorCorrections(t *testing.T) {
	tests := []struct {
		name string
		keys string
		want term.Coordinates
	}{
		{
			name: "short line with arrows",
			keys: "<end><down>",
			want: term.Coordinates{X: 1, Y: 1},
		},
		{
			name: "sticky desired column with arrows",
			keys: "<end><down><down>",
			want: term.Coordinates{X: 8, Y: 2},
		},
		{
			name: "sticky desired column with control motions",
			keys: "<end><ctrl-n><ctrl-n>",
			want: term.Coordinates{X: 8, Y: 2},
		},
		{
			name: "counted vertical motion",
			keys: "<end><ctrl-u>2<ctrl-n>",
			want: term.Coordinates{X: 8, Y: 2},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _ := newEmacsHandler(t, "longline\nx\nlongline")

			feedKeys(t, h, tt.keys)

			assert.Equal(t, tt.want, h.CursorAtScroll())
			pos, _, show := h.Cursor()
			assert.True(t, show)
			assert.Equal(t, tt.want, pos)
		})
	}
}

func TestEmacsCursorCorrectionsAfterExternalEdit(t *testing.T) {
	h, _ := newEmacsHandler(t, "")
	h.CellEditor().Edit(
		context.Background(), term.Coordinates{}, term.Coordinates{},
		"abcdefghi\n1234\nXXXX\nX\nX\nX\nX\nX\nX",
	)
	require.Equal(t, term.Coordinates{X: 1, Y: 8}, h.CursorAtScroll())

	_, handled := h.Handle(alt('<'))

	require.True(t, handled)
	assert.Equal(t, term.Coordinates{X: 1}, h.CursorAtScroll())
}

func TestEmacsCursorCorrectionsDisabled(t *testing.T) {
	h, _ := newEmacsHandler(
		t, "longline\nx\nlongline", WithCursorCorrections(false),
	)

	feedKeys(t, h, "<end><down>")

	want := term.Coordinates{X: 8, Y: 1}
	assert.Equal(t, want, h.CursorAtScroll())
	pos, _, show := h.Cursor()
	assert.True(t, show)
	assert.Equal(t, want, pos)
}

func TestEmacsMoveToBoundsReportsCorrection(t *testing.T) {
	buf := cell.NewBuffer()
	buf.ReadFrom(strings.NewReader("longline\nx"))
	h := NewHandler(
		buf, emacsTestURI(t), text.IndentRuneTab, 0, WithCursorCorrections(false),
	).(*emacsHandler)
	h.Resize(20, 10)
	feedKeys(t, h, "<end><down>")
	require.Equal(t, term.Coordinates{X: 8, Y: 1}, h.CursorAtScroll())

	assert.False(t, h.doMoveToBounds())
	assert.Equal(t, term.Coordinates{X: 8, Y: 1}, h.CursorAtScroll())

	h.cfg.cursorCorrections = true
	assert.True(t, h.doMoveToBounds())
	assert.Equal(t, term.Coordinates{X: 1, Y: 1}, h.CursorAtScroll())
	assert.False(t, h.doMoveToBounds())
}

func TestEmacsMoveToBoundsBoundaryMatrix(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		at        term.Coordinates
		wantStart term.Coordinates
		want      term.Coordinates
		moved     bool
	}{
		{name: "empty buffer origin", content: "", at: term.Coordinates{}, wantStart: term.Coordinates{}, want: term.Coordinates{}},
		{name: "empty buffer oversized", content: "", at: term.Coordinates{X: 20, Y: 20}, wantStart: term.Coordinates{X: 20, Y: 20}, want: term.Coordinates{}, moved: true},
		{name: "negative coordinates normalize before correction", content: "abc", at: term.Coordinates{X: -5, Y: -3}, wantStart: term.Coordinates{}, want: term.Coordinates{}},
		{name: "valid origin", content: "abc", at: term.Coordinates{}, wantStart: term.Coordinates{}, want: term.Coordinates{}},
		{name: "valid interior", content: "abc", at: term.Coordinates{X: 2}, wantStart: term.Coordinates{X: 2}, want: term.Coordinates{X: 2}},
		{name: "valid end caret", content: "abc", at: term.Coordinates{X: 3}, wantStart: term.Coordinates{X: 3}, want: term.Coordinates{X: 3}},
		{name: "one past end caret", content: "abc", at: term.Coordinates{X: 4}, wantStart: term.Coordinates{X: 4}, want: term.Coordinates{X: 3}, moved: true},
		{name: "far past end caret", content: "abc", at: term.Coordinates{X: 40}, wantStart: term.Coordinates{X: 40}, want: term.Coordinates{X: 3}, moved: true},
		{name: "empty line origin", content: "abc\n\ndef", at: term.Coordinates{Y: 1}, wantStart: term.Coordinates{Y: 1}, want: term.Coordinates{Y: 1}},
		{name: "empty line virtual column", content: "abc\n\ndef", at: term.Coordinates{X: 9, Y: 1}, wantStart: term.Coordinates{X: 9, Y: 1}, want: term.Coordinates{Y: 1}, moved: true},
		{name: "after last line", content: "abc\ndef", at: term.Coordinates{X: 2, Y: 2}, wantStart: term.Coordinates{X: 2, Y: 2}, want: term.Coordinates{X: 2, Y: 1}, moved: true},
		{name: "many rows after last line", content: "abc\ndef", at: term.Coordinates{X: 50, Y: 20}, wantStart: term.Coordinates{X: 50, Y: 20}, want: term.Coordinates{X: 3, Y: 1}, moved: true},
		{name: "longest line column on short row", content: "longest-line\nx", at: term.Coordinates{X: 12, Y: 1}, wantStart: term.Coordinates{X: 12, Y: 1}, want: term.Coordinates{X: 1, Y: 1}, moved: true},
		{name: "longest line end caret", content: "longest-line\nx", at: term.Coordinates{X: 12}, wantStart: term.Coordinates{X: 12}, want: term.Coordinates{X: 12}},
		{name: "leading null remains content", content: "\x00ab", at: term.Coordinates{}, wantStart: term.Coordinates{}, want: term.Coordinates{}},
		{name: "interior null remains content", content: "a\x00b", at: term.Coordinates{X: 1}, wantStart: term.Coordinates{X: 1}, want: term.Coordinates{X: 1}},
		{name: "right of interior null remains content", content: "a\x00b", at: term.Coordinates{X: 2}, wantStart: term.Coordinates{X: 2}, want: term.Coordinates{X: 2}},
		{name: "trailing null remains content", content: "ab\x00\x00", at: term.Coordinates{X: 4}, wantStart: term.Coordinates{X: 4}, want: term.Coordinates{X: 4}},
		{name: "past trailing nulls", content: "ab\x00\x00", at: term.Coordinates{X: 9}, wantStart: term.Coordinates{X: 9}, want: term.Coordinates{X: 4}, moved: true},
		{name: "only null cells", content: "\x00\x00\x00", at: term.Coordinates{X: 3}, wantStart: term.Coordinates{X: 3}, want: term.Coordinates{X: 3}},
		{name: "wide rune cell", content: "a界b", at: term.Coordinates{X: 1}, wantStart: term.Coordinates{X: 1}, want: term.Coordinates{X: 1}},
		{name: "after wide rune", content: "a界b", at: term.Coordinates{X: 2}, wantStart: term.Coordinates{X: 2}, want: term.Coordinates{X: 2}},
		{name: "wide line end caret", content: "a界b", at: term.Coordinates{X: 3}, wantStart: term.Coordinates{X: 3}, want: term.Coordinates{X: 3}},
		{name: "past wide line", content: "a界b", at: term.Coordinates{X: 8}, wantStart: term.Coordinates{X: 8}, want: term.Coordinates{X: 3}, moved: true},
		{name: "trailing newline virtual row", content: "abc\n", at: term.Coordinates{X: 7, Y: 2}, wantStart: term.Coordinates{X: 7, Y: 2}, want: term.Coordinates{Y: 1}, moved: true},
		{name: "trailing newline empty row", content: "abc\n", at: term.Coordinates{Y: 1}, wantStart: term.Coordinates{Y: 1}, want: term.Coordinates{Y: 1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := cell.NewBuffer()
			_, err := buf.ReadFrom(strings.NewReader(tt.content))
			require.NoError(t, err)
			h := NewHandler(buf, emacsTestURI(t), text.IndentRuneTab, 0).(*emacsHandler)
			h.Resize(100, 100)
			h.cursor.MoveToScroll(tt.at)
			require.Equal(t, tt.wantStart, h.CursorAtScroll(), "constructed start")

			var moved bool
			require.NotPanics(t, func() { moved = h.doMoveToBounds() })
			assert.Equal(t, tt.moved, moved, "movement result")
			assert.Equal(t, tt.want, h.CursorAtScroll(), "corrected cursor")
			assert.False(t, h.doMoveToBounds(), "correction must be idempotent")
			assert.Equal(t, tt.want, h.CursorAtScroll(), "idempotent cursor")

			pos := h.CursorAtScroll()
			assert.GreaterOrEqual(t, pos.X, 0)
			assert.GreaterOrEqual(t, pos.Y, 0)
			if pos.Y < buf.Rows() {
				assert.LessOrEqual(t, pos.X, buf.Columns(pos.Y), "insert-like end caret")
			}
		})
	}
}

func TestEmacsMoveToBoundsCoordinateContentSweep(t *testing.T) {
	contents := []struct {
		name string
		text string
	}{
		{name: "empty"},
		{name: "single cell", text: "x"},
		{name: "single line", text: "abcdef"},
		{name: "mixed lengths", text: "longest-line\nx\nmedium"},
		{name: "blank rows", text: "\n\n\n"},
		{name: "blank middle row", text: "abc\n\ndef"},
		{name: "trailing newline", text: "abc\n"},
		{name: "leading nulls", text: "\x00\x00abc"},
		{name: "interior nulls", text: "a\x00b\x00c"},
		{name: "trailing nulls", text: "abc\x00\x00\x00"},
		{name: "only nulls", text: "\x00\x00\x00"},
		{name: "null rows", text: "\x00\x00\nx\n\x00\x00\x00\x00"},
		{name: "wide runes", text: "界\n界界界\na界b"},
		{name: "emoji", text: "🙂🙂\nx🙂y"},
		{name: "combining runes", text: "e\u0301\ne\u0301e\u0301"},
		{name: "tabs and spaces", text: "\t\n  x\n\t\t"},
		{name: "long row", text: strings.Repeat("x", 128)},
		{name: "longest middle row", text: "x\n" + strings.Repeat("y", 128) + "\nz"},
	}

	for _, content := range contents {
		t.Run(content.name, func(t *testing.T) {
			buf := cell.NewBuffer()
			_, err := buf.ReadFrom(strings.NewReader(content.text))
			require.NoError(t, err)
			h := NewHandler(buf, emacsTestURI(t), text.IndentRuneTab, 0).(*emacsHandler)
			h.Resize(512, 512)

			xs := uniqueInts(-9, -1, 0, 1, 2, 3, buf.MaxColumns()-1,
				buf.MaxColumns(), buf.MaxColumns()+1, buf.MaxColumns()+17, 256)
			ys := uniqueInts(-9, -1, 0, 1, 2, buf.Rows()-1,
				buf.Rows(), buf.Rows()+1, buf.Rows()+17, 64)
			for _, y := range ys {
				for _, x := range xs {
					input := term.Coordinates{X: x, Y: y}
					h.cursor.MoveToScroll(input)
					start := h.CursorAtScroll()
					want := moveToBoundsOracle(buf, start)

					var moved bool
					require.NotPanics(t, func() { moved = h.doMoveToBounds() }, "input=%v start=%v", input, start)
					assert.Equal(t, start != want, moved, "movement input=%v start=%v", input, start)
					assert.Equal(t, want, h.CursorAtScroll(), "cursor input=%v start=%v", input, start)
					assert.False(t, h.doMoveToBounds(), "idempotence input=%v", input)
					assert.Equal(t, want, h.CursorAtScroll(), "idempotent cursor input=%v", input)
				}
			}
		})
	}
}

func moveToBoundsOracle(buf *cell.Buffer, pos term.Coordinates) term.Coordinates {
	if buf.Rows() == 0 {
		return term.Coordinates{}
	}
	pos.Y = max(0, min(pos.Y, buf.Rows()-1))
	pos.X = max(0, min(pos.X, buf.Columns(pos.Y)))
	return pos
}

func uniqueInts(values ...int) []int {
	seen := make(map[int]struct{}, len(values))
	unique := make([]int, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	return unique
}

func TestLocationMessage(t *testing.T) {
	cases := []handlertest.SequenceTestCase{
		{"<down>",
			`a                   
▐                   
c                   
                    
                    
                    
                    
                    
                    
            durrdurr`},
		{"<down><down>",
			`a                   
b                   
▐                   
                    
                    
                    
                    
                    
                    
                    `},
		{"<down><down><up>",
			`a                   
▐                   
c                   
                    
                    
                    
                    
                    
                    
            durrdurr`},
	}

	newVi := func(t *testing.T) tui.Handler {
		uri, err := workspaceapi.ParseURI("memory:///myfile")
		require.NoError(t, err)
		buf := cell.NewBuffer()
		_, _ = buf.ReadFrom(strings.NewReader("a\nb\nc"))
		handler := NewHandler(buf, uri, text.IndentRuneTab, 0)
		handler.Resize(20, 10)
		handler.SetLocationList(textapi.LocationPriorityInfo, "id",
			textapi.LocationSlice([]textapi.Location{
				{
					Message: "durrdurr",
					From:    term.Coordinates{Y: 1},
					To:      term.Coordinates{Y: 1, X: 5},
				},
			}))
		return handler
	}
	handlertest.RunHandlerIsolated(t, newVi, 20, 10, cases)
}

func TestSyntacticSelectionKeyBindings(t *testing.T) {
	uri, err := workspaceapi.ParseURI("memory:///selection.go")
	require.NoError(t, err)

	buf := cell.NewBuffer()
	_, _ = buf.ReadFrom(strings.NewReader("alpha beta gamma"))

	view := testSelectionService{
		view: buf.View(),
		expand: map[term.Range]term.Range{
			{Start: term.Coordinates{X: 6}, End: term.Coordinates{X: 6}}:  {Start: term.Coordinates{X: 6}, End: term.Coordinates{X: 10}},
			{Start: term.Coordinates{X: 6}, End: term.Coordinates{X: 10}}: {Start: term.Coordinates{}, End: term.Coordinates{X: 10}},
		},
		shrink: map[term.Range]term.Range{
			{Start: term.Coordinates{}, End: term.Coordinates{X: 10}}:     {Start: term.Coordinates{X: 6}, End: term.Coordinates{X: 10}},
			{Start: term.Coordinates{X: 6}, End: term.Coordinates{X: 10}}: {Start: term.Coordinates{X: 6}, End: term.Coordinates{X: 6}},
		},
	}
	buf.WithView(view)

	h := NewHandler(buf, uri, text.IndentRuneTab, 0)
	h.Resize(80, 10)
	require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 6}))

	_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: '='})
	require.True(t, handled)
	selection, ok := h.Selection()
	require.True(t, ok)
	assert.Equal(t, "beta", selection)

	_, handled = h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: '='})
	require.True(t, handled)
	selection, ok = h.Selection()
	require.True(t, ok)
	assert.Equal(t, "alpha beta", selection)

	_, handled = h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl | term.ModShift, Ch: 'W'})
	require.True(t, handled)
	selection, ok = h.Selection()
	require.True(t, ok)
	assert.Equal(t, "beta", selection)

	_, handled = h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl | term.ModShift, Ch: 'W'})
	require.True(t, handled)
	selection, ok = h.Selection()
	assert.False(t, ok)
	assert.Equal(t, "", selection)
	assert.Equal(t, term.Coordinates{X: 6}, h.CursorAtScroll())

	_, handled = h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl | term.ModShift, Ch: 'W'})
	assert.False(t, handled)
}

func TestEmacsKeyBindingsMacOS(t *testing.T) {
	const snippet = "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"

	suite := []struct {
		description string
		keycomb     string
		result      *string
		coordinates term.Coordinates
		clipboard   *string
	}{
		// General editing
		// C-y yanks the most recently copied region (C-c copies).
		{"Copy+Paste", "<shift-right><alt-w><ctrl-y>", new("aa\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 2}, nil},
		{"Undo", "<ctrl-shift-backspace><ctrl-/>", new("a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 0}, nil},
		{"Insert completion/snippet or indent", "<tab>", new("\ta\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 1}, nil},
		{"Previous snippet field or unindent", "<tab><shift-tab>", new("a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 0}, nil},

		// Line manipulation
		{"Insert line after current line", "<ctrl-enter>", new("a\n\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 1, X: 0}, nil},
		{"Insert line before current line", "<ctrl-shift-enter>", new("\na\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 0}, nil},
		{"Move line/selection up", "<down><alt-up>", new("b\na\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 0}, nil},
		{"Move line/selection down", "<alt-down>", new("b\na\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 1, X: 0}, nil},
		{"Duplicate line(s)", "<alt-shift-down>", new("a\na\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 1, X: 0}, nil},
		{"Delete entire line", "<ctrl-shift-backspace>", new("b\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 0}, nil},
		{"Kill to end of line", "<ctrl-k>", new("\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 0}, nil},
		{"Set mark at cursor position", "<ctrl-space>", nil, term.Coordinates{}, nil},
		{"Copy region from mark to point (M-w)", "<ctrl-space><down><down><alt-w>", nil, term.Coordinates{Y: 2, X: 0}, new("a\nb\n")},
		{"Kill region from mark to point (C-w)", "<ctrl-space><down><down><ctrl-w>", new("c\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 0}, nil},

		// Comments - depends on language/syntax (assuming C-style). M-; is
		// comment-dwim.
		{"Toggle line comment (M-;)", "<alt-;>", new("// a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 3}, nil},

		// Text transformation. M-q is fill-paragraph.
		{"Wrap paragraph at ruler (M-q)", "<alt-q>", new("alpha beta\ngamma delta\nepsilon zeta\neta theta\n\ni\nj\nk"), term.Coordinates{Y: 0, X: 0}, nil},

		// Expand selection
		{"Expand selection to brackets", "{abc}<left><left><shift-left><ctrl-shift-m>", nil, term.Coordinates{X: 4}, nil},

		// Navigation and movement
		{"Move cursor to beginning of line", "<right><ctrl-a>", nil, term.Coordinates{Y: 0, X: 0}, nil},
		{"Move cursor to end of line", "<ctrl-e>", nil, term.Coordinates{Y: 0, X: 1}, nil},
		{"Move up one line", "<down><ctrl-p>", nil, term.Coordinates{Y: 0, X: 0}, nil},
		{"Move down one line", "<ctrl-n>", nil, term.Coordinates{Y: 1, X: 0}, nil},
		{"Move right one character", "<ctrl-f>", nil, term.Coordinates{Y: 0, X: 1}, nil},
		{"Move left one character", "<right><ctrl-b>", nil, term.Coordinates{Y: 0, X: 0}, nil},
		{"Move to start of buffer (M-<)", "<down><down><alt-shift-,>", nil, term.Coordinates{Y: 0, X: 0}, nil},
		{"Move to end of buffer (M->)", "<alt-shift-.>", nil, term.Coordinates{Y: 10, X: 0}, nil},

		// Scrolling
		{"Center current line in view", "<alt-shift-.>z<enter>z<enter>z<enter>z<enter>z<enter>z<enter><up><up><up><up><ctrl-l>",
			nil, term.Coordinates{Y: 12}, nil},
		{"Scroll down one page", "<ctrl-v>", nil, term.Coordinates{Y: 3, X: 0}, nil},
		{"Scroll view up one line", "<alt-shift-.><up><ctrl-alt-up>", nil, term.Coordinates{Y: 9}, nil},
		{"Scroll view down one line", "<ctrl-alt-down>", nil, term.Coordinates{Y: 1}, nil},

		// Search and replace
		//{"Find", "<meta-f>", nil, term.Coordinates{}},
		//{"Find next", "<meta-f><meta-g>", nil, term.Coordinates{}},
		//{"Find previous", "<meta-f><shift-meta-g>", nil, term.Coordinates{}},
		//{"Incremental find", "<meta-i>", nil, term.Coordinates{}},
		//{"Find and replace", "<alt-meta-f>", nil, term.Coordinates{}},
		//{"Replace next", "<alt-meta-f><alt-meta-e>", nil, term.Coordinates{}},
		//{"Replace all", "<alt-meta-f><ctrl-meta-e>", nil, term.Coordinates{}},
		//{"Find in files", "<shift-meta-f>", nil, term.Coordinates{}},
		//{"Next result in file search", "<f4>", nil, term.Coordinates{}},
		//{"Previous result in file search", "<shift-f4>", nil, term.Coordinates{}},
		//{"Use selection for find", "<shift-right><meta-e>", nil, term.Coordinates{Y: 0, X: 1}},
		//{"Use selection for replace", "<shift-right><shift-meta-e>", nil, term.Coordinates{Y: 0, X: 1}},
		//{"Quick find (select word under cursor)", "<alt-meta-g>", nil, term.Coordinates{}},
		//{"Quick find all (select all occurrences of word)", "<ctrl-meta-g>", nil, term.Coordinates{}},

		// Bookmarks (done via command.key_bindings)
		//{"Toggle bookmark on current line", "<meta-f2>", nil, term.Coordinates{}},
		//{"Jump to next bookmark", "<meta-f2><f2>", nil, term.Coordinates{Y: 0, X: 0}},
		//{"Jump to previous bookmark", "<meta-f2><shift-f2>", nil, term.Coordinates{Y: 0, X: 0}},
		//{"Select all bookmarks", "<meta-f2><alt-f2>", nil, term.Coordinates{}},
		//{"Clear all bookmarks", "<meta-f2><shift-meta-f2>", nil, term.Coordinates{}},

		// Macros
		{"Start/stop recording macro", "<f3>", nil, term.Coordinates{}, nil},
		{"Playback recorded macro", "<f3><right><f4><f4>", nil, term.Coordinates{Y: 0, X: 1}, nil},

		// Build system
		// {"Build (run default build system)", "<meta-b>", nil, term.Coordinates{}},
		// {"Build with... (select build system)", "<shift-meta-b>", nil, term.Coordinates{}},
		// {"Cancel current build", "<ctrl-c>", nil, term.Coordinates{}},

		// Spell check
		//{"Toggle spell check", "<f6>", nil, term.Coordinates{}},
		//{"Jump to next misspelling", "<ctrl-f6>", nil, term.Coordinates{}},
		//{"Jump to previous misspelling", "<ctrl-shift-f6>", nil, term.Coordinates{}},

		// Auto-pairing (context-dependent) - these insert characters
		{"Auto-pair double quotes", "\"", new("\"\"a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 1}, nil},
		{"Auto-pair single quotes", "'", new("''a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 1}, nil},
		{"Auto-pair parentheses", "(", new("()a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 1}, nil},
		{"Auto-pair square brackets", "[", new("[]a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 1}, nil},
		{"Auto-pair curly braces", "{", new("{}a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 1}, nil},
		{"Delete matching pair (when between paired characters)", "(<backspace>", new("a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 0}, nil},
		{"Add line between paired braces", "{<enter>", new("{\n\n}\na\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 1, X: 0}, nil},
	}

	uri, err := workspaceapi.ParseURI("memory:///myfile.go")
	require.NoError(t, err)

	for _, test := range suite {
		t.Run(test.description, func(t *testing.T) {
			seq, err := term.ParseKeys(test.keycomb)
			require.NoError(t, err)

			clip := clipboard.NewInMemory()
			reg := registerhistory.NewClipboard(registerset.New(clip))
			content := snippet
			if test.description == "Wrap paragraph at ruler (M-q)" {
				content = "alpha beta gamma delta epsilon zeta eta theta\n\ni\nj\nk"
			}
			buf := cell.NewBuffer()
			buf.ReadFrom(strings.NewReader(content))
			buf.WithView(testCommentService{
				view:  buf.View(),
				line:  []string{"//"},
				block: []string{"/*", "*/"},
			})
			recorder := new(testMacroRecorder)
			player := new(testMacroPlayer)
			opts := []Option{
				WithClipboard(reg),
				WithMacroRecorder(recorder),
				WithMacroPlayer(player),
				WithRuler(12),
				WithComments(text.CommentConfig{
					"go": {
						Line:  []string{"//"},
						Block: []text.CommentBlock{{Start: "/*", End: "*/"}},
					},
				}),
			}
			if strings.HasPrefix(test.description, "Auto-pair") ||
				test.description == "Delete matching pair (when between paired characters)" ||
				test.description == "Add line between paired braces" {
				opts = append(opts, WithAutoPair(true))
			}
			handler := NewHandler(buf, uri, text.IndentRuneTab, 0, opts...)
			handler.Resize(10, 3)
			for _, key := range seq {
				ev := term.Event{Type: term.EventKey, Key: key.Key, Mod: key.Mod, Ch: key.Ch}
				_, ok := handler.Handle(ev)
				require.True(t, ok)
			}
			if test.result != nil {
				assert.Equal(t, *test.result, buf.String())
			}
			if test.clipboard != nil {
				paste, err := clip.Paste(clipboard.DefaultRegisterID)
				require.NoError(t, err)
				assert.Equal(t, *test.clipboard, paste.Text)
			}
			switch test.description {
			case "Start/stop recording macro":
				assert.Equal(t, []string{registerset.UnnamedRegisterID}, recorder.started)
				assert.True(t, recorder.recording)
				assert.Zero(t, recorder.stopped)
				assert.Empty(t, player.plays)
			case "Playback recorded macro":
				assert.Equal(t, []string{registerset.UnnamedRegisterID}, recorder.started)
				assert.False(t, recorder.recording)
				assert.Equal(t, 1, recorder.stopped)
				assert.Equal(t, []testMacroPlay{{
					registerID: registerset.UnnamedRegisterID,
					count:      1,
				}}, player.plays)
			}
			assert.Equal(t, test.coordinates, handler.CursorAtScroll())
		})
	}
}

func TestAutoPairOption(t *testing.T) {
	uri, err := workspaceapi.ParseURI("memory:///myfile.go")
	require.NoError(t, err)

	run := func(t *testing.T, keycomb string, opts ...Option) (*cell.Buffer, *emacsHandler) {
		t.Helper()
		seq, err := term.ParseKeys(keycomb)
		require.NoError(t, err)

		buf := cell.NewBuffer()
		buf.ReadFrom(strings.NewReader("a"))
		h := NewHandler(buf, uri, text.IndentRuneTab, 0, opts...)
		h.Resize(10, 3)
		handler := h.(*emacsHandler)
		for _, key := range seq {
			_, handled := handler.Handle(term.Event{
				Type: term.EventKey,
				Key:  key.Key,
				Mod:  key.Mod,
				Ch:   key.Ch,
			})
			require.True(t, handled)
		}
		return buf, handler
	}

	t.Run("disabled by default", func(t *testing.T) {
		buf, h := run(t, "(")

		assert.Equal(t, "(a", buf.String())
		assert.Equal(t, term.Coordinates{X: 1}, h.CursorAtScroll())
	})

	t.Run("opening delimiters insert matching pair when enabled", func(t *testing.T) {
		buf, h := run(t, "(", WithAutoPair(true))

		assert.Equal(t, "()a", buf.String())
		assert.Equal(t, term.Coordinates{X: 1}, h.CursorAtScroll())
	})

	t.Run("backspace removes matching pair when enabled", func(t *testing.T) {
		buf, h := run(t, "(<backspace>", WithAutoPair(true))

		assert.Equal(t, "a", buf.String())
		assert.Equal(t, term.Coordinates{}, h.CursorAtScroll())
	})

	t.Run("enter between braces creates blank line when enabled", func(t *testing.T) {
		buf, h := run(t, "{<enter>", WithAutoPair(true))

		assert.Equal(t, "{\n\n}\na", buf.String())
		assert.Equal(t, term.Coordinates{Y: 1}, h.CursorAtScroll())
	})
}

func TestSetMarkUsesSharedLocationList(t *testing.T) {
	uri, err := workspaceapi.ParseURI("memory:///myfile.go")
	require.NoError(t, err)

	buf := cell.NewBuffer()
	buf.ReadFrom(strings.NewReader("a\nb\nc"))
	h := NewHandler(buf, uri, text.IndentRuneTab, 0)
	h.Resize(10, 3)

	run := func(keys string) {
		seq, err := term.ParseKeys(keys)
		require.NoError(t, err)
		for _, key := range seq {
			_, handled := h.Handle(term.Event{
				Type: term.EventKey,
				Key:  key.Key,
				Mod:  key.Mod,
				Ch:   key.Ch,
			})
			require.True(t, handled)
		}
	}
	markLocations := func() []textapi.Location {
		for _, list := range h.LocationLists() {
			if list.ID == emacsMarkLocationListID {
				return list.Locations
			}
		}
		return nil
	}

	// C-SPC is set-mark-command.
	run("<ctrl-space>")
	require.Len(t, markLocations(), 1)
	assert.Equal(t, term.Coordinates{}, markLocations()[0].From)

	run("<down><down><ctrl-space>")
	require.Len(t, markLocations(), 2)
	assert.Equal(t, term.Coordinates{}, markLocations()[0].From)
	assert.Equal(t, term.Coordinates{Y: 2}, markLocations()[1].From)
}

func TestEmacsMetaWordEditing(t *testing.T) {
	uri, err := workspaceapi.ParseURI("memory:///words.go")
	require.NoError(t, err)

	cases := []struct {
		name    string
		content string
		keys    string
		want    *string
		at      term.Coordinates
	}{
		{"M-f forward-word", "alpha beta", "<alt-f>", nil, term.Coordinates{X: 5}},
		{"M-f twice crosses space", "alpha beta gamma", "<alt-f><alt-f>", nil, term.Coordinates{X: 10}},
		{"M-b backward-word", "alpha beta", "<end><alt-b>", nil, term.Coordinates{X: 6}},
		{"M-d kill-word forward", "alpha beta", "<alt-d>", new(" beta"), term.Coordinates{}},
		{"M-DEL backward-kill-word", "alpha beta", "<end><alt-backspace>", new("alpha "), term.Coordinates{X: 6}},
		{"M-u upcase-word", "alpha beta", "<alt-u>", new("ALPHA beta"), term.Coordinates{X: 5}},
		{"M-l downcase-word", "ALPHA beta", "<alt-l>", new("alpha beta"), term.Coordinates{X: 5}},
		{"M-c capitalize-word", "alpha beta", "<alt-c>", new("Alpha beta"), term.Coordinates{X: 5}},
		{"M-c capitalize lowercases tail", "aLPHA beta", "<alt-c>", new("Alpha beta"), term.Coordinates{X: 5}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := cell.NewBuffer()
			buf.ReadFrom(strings.NewReader(tc.content))
			h := NewHandler(buf, uri, text.IndentRuneTab, 0)
			h.Resize(80, 3)

			seq, err := term.ParseKeys(tc.keys)
			require.NoError(t, err)
			for _, key := range seq {
				_, handled := h.Handle(term.Event{
					Type: term.EventKey, Key: key.Key, Mod: key.Mod, Ch: key.Ch,
				})
				require.True(t, handled, "key %v", key)
			}

			if tc.want != nil {
				assert.Equal(t, *tc.want, buf.String())
			}
			assert.Equal(t, tc.at, h.CursorAtScroll())
		})
	}
}

func TestEmacsKeymapAdditions(t *testing.T) {
	uri, err := workspaceapi.ParseURI("memory:///keymap.go")
	require.NoError(t, err)

	cases := []struct {
		name    string
		content string
		start   term.Coordinates
		events  []term.Event
		want    *string
		at      term.Coordinates
	}{
		{
			name:    "M-m back-to-indentation",
			content: "  ab",
			start:   term.Coordinates{X: 4},
			events:  []term.Event{{Type: term.EventKey, Mod: term.ModAlt, Ch: 'm'}},
			at:      term.Coordinates{X: 2},
		},
		{
			name:    "M-^ delete-indentation joins onto previous line",
			content: "a\nb",
			start:   term.Coordinates{Y: 1},
			events:  []term.Event{{Type: term.EventKey, Mod: term.ModAlt, Ch: '^'}},
			want:    new("a b"),
			at:      term.Coordinates{X: 1},
		},
		{
			name:    "M-backslash delete-horizontal-space",
			content: "a   b",
			start:   term.Coordinates{X: 2},
			events:  []term.Event{{Type: term.EventKey, Mod: term.ModAlt, Ch: '\\'}},
			want:    new("ab"),
			at:      term.Coordinates{X: 1},
		},
		{
			name:    "C-o open-line keeps point before newline",
			content: "ab",
			start:   term.Coordinates{X: 1},
			events:  []term.Event{{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'o'}},
			want:    new("a\nb"),
			at:      term.Coordinates{X: 1},
		},
		{
			name:    "C-j newline-and-indent",
			content: "ab",
			start:   term.Coordinates{X: 1},
			events:  []term.Event{{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'j'}},
			want:    new("a\nb"),
			at:      term.Coordinates{Y: 1, X: 0},
		},
		{
			name:    "C-M-f forward-sexp over brackets",
			content: "(x)y",
			events:  []term.Event{{Type: term.EventKey, Mod: term.ModCtrlAlt, Ch: 'f'}},
			at:      term.Coordinates{X: 3},
		},
		{
			name:    "C-M-f forward-sexp scans to next opener",
			content: "a(x)y",
			events:  []term.Event{{Type: term.EventKey, Mod: term.ModCtrlAlt, Ch: 'f'}},
			at:      term.Coordinates{X: 4},
		},
		{
			name:    "C-M-b backward-sexp over brackets",
			content: "(x)y",
			start:   term.Coordinates{X: 4},
			events:  []term.Event{{Type: term.EventKey, Mod: term.ModCtrlAlt, Ch: 'b'}},
			at:      term.Coordinates{X: 0},
		},
		{
			name:    "C-t transpose-chars",
			content: "abc",
			start:   term.Coordinates{X: 1},
			events:  []term.Event{{Type: term.EventKey, Mod: term.ModCtrl, Ch: 't'}},
			want:    new("bac"),
			at:      term.Coordinates{X: 2},
		},
		{
			name:    "C-t transpose-chars at end of line",
			content: "ab",
			start:   term.Coordinates{X: 2},
			events:  []term.Event{{Type: term.EventKey, Mod: term.ModCtrl, Ch: 't'}},
			want:    new("ba"),
			at:      term.Coordinates{X: 2},
		},
		{
			name:    "M-t transpose-words",
			content: "alpha beta",
			start:   term.Coordinates{X: 5},
			events:  []term.Event{{Type: term.EventKey, Mod: term.ModAlt, Ch: 't'}},
			want:    new("beta alpha"),
			at:      term.Coordinates{X: 10},
		},
		{
			name:    "M-t transpose-words from inside second word",
			content: "alpha beta",
			start:   term.Coordinates{X: 8},
			events:  []term.Event{{Type: term.EventKey, Mod: term.ModAlt, Ch: 't'}},
			want:    new("beta alpha"),
			at:      term.Coordinates{X: 10},
		},
		{
			name:    "M-< beginning-of-buffer",
			content: "a\nb\nc",
			start:   term.Coordinates{Y: 2, X: 1},
			events:  []term.Event{{Type: term.EventKey, Mod: term.ModAlt, Ch: '<'}},
			at:      term.Coordinates{Y: 0, X: 1},
		},
		{
			name:    "M-> end-of-buffer",
			content: "a\nb\nc",
			events:  []term.Event{{Type: term.EventKey, Mod: term.ModAlt, Ch: '>'}},
			at:      term.Coordinates{Y: 2, X: 0},
		},
		{
			name:    "M-e forward-sentence",
			content: "One.  Two.",
			events:  []term.Event{{Type: term.EventKey, Mod: term.ModAlt, Ch: 'e'}},
			at:      term.Coordinates{X: 4},
		},
		{
			name:    "M-a backward-sentence",
			content: "One.  Two.",
			start:   term.Coordinates{X: 8},
			events:  []term.Event{{Type: term.EventKey, Mod: term.ModAlt, Ch: 'a'}},
			at:      term.Coordinates{X: 6},
		},
		{
			name:    "M-k kill-sentence",
			content: "One.  Two.",
			events:  []term.Event{{Type: term.EventKey, Mod: term.ModAlt, Ch: 'k'}},
			want:    new("  Two."),
			at:      term.Coordinates{},
		},
		{
			name:    "M-z zap-to-char reads the target",
			content: "hello world",
			events: []term.Event{
				{Type: term.EventKey, Mod: term.ModAlt, Ch: 'z'},
				{Type: term.EventKey, Ch: 'o'},
			},
			want: new(" world"),
			at:   term.Coordinates{},
		},
		{
			name:    "C-u C-f universal argument",
			content: "abcdefgh",
			events: []term.Event{
				{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'u'},
				{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'f'},
			},
			at: term.Coordinates{X: 4},
		},
		{
			name:    "C-/ undo",
			content: "ab",
			start:   term.Coordinates{X: 2},
			events: []term.Event{
				{Type: term.EventKey, Ch: 'X'},
				{Type: term.EventKey, Mod: term.ModCtrl, Ch: '/'},
			},
			want: new("ab"),
			at:   term.Coordinates{X: 2},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := cell.NewBuffer()
			buf.ReadFrom(strings.NewReader(tc.content))
			h := NewHandler(buf, uri, text.IndentRuneTab, 0)
			h.Resize(80, 10)
			if tc.start != (term.Coordinates{}) {
				require.True(t, h.SetCursorAtScroll(tc.start))
			}

			for _, ev := range tc.events {
				_, handled := h.Handle(ev)
				require.True(t, handled, "event %v", ev)
			}

			if tc.want != nil {
				assert.Equal(t, *tc.want, buf.String())
			}
			assert.Equal(t, tc.at, h.CursorAtScroll())
		})
	}
}

func TestEmacsMetaPunctuationSemantics(t *testing.T) {
	uri, err := workspaceapi.ParseURI("memory:///meta.go")
	require.NoError(t, err)

	t.Run("M-comma is unbound", func(t *testing.T) {
		buf := cell.NewBuffer()
		buf.ReadFrom(strings.NewReader("a\nb\nc"))
		h := NewHandler(buf, uri, text.IndentRuneTab, 0)
		h.Resize(80, 10)
		require.True(t, h.SetCursorAtScroll(term.Coordinates{Y: 2, X: 1}))
		_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModAlt, Ch: ','})
		assert.False(t, handled, "M-, must not be handled by the editor")
		assert.Equal(t, term.Coordinates{Y: 2, X: 1}, h.CursorAtScroll())
	})

	t.Run("M-period is unbound", func(t *testing.T) {
		buf := cell.NewBuffer()
		buf.ReadFrom(strings.NewReader("a\nb\nc"))
		h := NewHandler(buf, uri, text.IndentRuneTab, 0)
		h.Resize(80, 10)
		_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModAlt, Ch: '.'})
		assert.False(t, handled, "M-. must not be handled by the editor")
		assert.Equal(t, term.Coordinates{}, h.CursorAtScroll())
	})
}

func TestEmacsLeavesCtrlAltSuperToRune(t *testing.T) {
	uri, err := workspaceapi.ParseURI("memory:///resize.go")
	require.NoError(t, err)
	for _, ch := range []rune{'p', 'b', 'n', 'f'} {
		t.Run(string(ch), func(t *testing.T) {
			buf := cell.NewBuffer()
			buf.ReadFrom(strings.NewReader("(a)\n(b)\n(c)"))
			h := NewHandler(buf, uri, text.IndentRuneTab, 0)
			h.Resize(80, 10)
			at := term.Coordinates{Y: 1, X: 1}
			require.True(t, h.SetCursorAtScroll(at))
			_, handled := h.Handle(term.Event{Type: term.EventKey,
				Mod: term.ModCtrl | term.ModAlt | term.ModMeta, Ch: ch})
			assert.False(t, handled, "Ctrl+Alt+Super+%c belongs to Rune", ch)
			assert.Equal(t, at, h.CursorAtScroll())
		})
	}
}

func TestEmacsGotoLine(t *testing.T) {
	content := "l1\nl2\nl3\nl4\nl5"

	feed := func(t *testing.T, h text.Handler, keys string) {
		t.Helper()
		seq, err := term.ParseKeys(keys)
		require.NoError(t, err)
		for _, k := range seq {
			h.Handle(term.Event{Type: term.EventKey, Key: k.Key, Mod: k.Mod, Ch: k.Ch})
		}
	}

	t.Run("jumps to the requested line", func(t *testing.T) {
		h, _ := newEmacsHandler(t, content)
		feed(t, h, "<alt-g>g3<enter>")
		assert.Equal(t, term.Coordinates{Y: 2, X: 0}, h.CursorAtScroll())
		assert.False(t, h.IsSearchMode(), "prompt must close on submit")
	})

	t.Run("clamps a too-large line to the last line", func(t *testing.T) {
		h, _ := newEmacsHandler(t, content)
		feed(t, h, "<alt-g>g99<enter>")
		assert.Equal(t, 4, h.CursorAtScroll().Y)
	})

	t.Run("is search mode while the prompt is open", func(t *testing.T) {
		h, _ := newEmacsHandler(t, content)
		feed(t, h, "<alt-g>g2")
		assert.True(t, h.IsSearchMode(), "editor owns keys while prompting")
		// Enter commits and closes the prompt.
		feed(t, h, "<enter>")
		assert.Equal(t, term.Coordinates{Y: 1, X: 0}, h.CursorAtScroll())
	})

	t.Run("C-g aborts the prompt without moving", func(t *testing.T) {
		h, _ := newEmacsHandler(t, content)
		require.True(t, h.SetCursorAtScroll(term.Coordinates{Y: 1, X: 1}))
		feed(t, h, "<alt-g>g4<ctrl-g>")
		assert.False(t, h.IsSearchMode())
		assert.Equal(t, term.Coordinates{Y: 1, X: 1}, h.CursorAtScroll())
	})

	t.Run("M-g then a non-g key does not open the prompt", func(t *testing.T) {
		h, _ := newEmacsHandler(t, content)
		feed(t, h, "<alt-g>x")
		assert.False(t, h.IsSearchMode())
	})

	t.Run("blank entry is ignored", func(t *testing.T) {
		h, _ := newEmacsHandler(t, content)
		require.True(t, h.SetCursorAtScroll(term.Coordinates{Y: 3, X: 0}))
		feed(t, h, "<alt-g>g<enter>")
		assert.Equal(t, term.Coordinates{Y: 3, X: 0}, h.CursorAtScroll())
	})
}

func TestEmacsIncrementalSearch(t *testing.T) {
	feed := func(t *testing.T, h text.Handler, keys string) {
		t.Helper()
		seq, err := term.ParseKeys(keys)
		require.NoError(t, err)
		for _, k := range seq {
			h.Handle(term.Event{Type: term.EventKey, Key: k.Key, Mod: k.Mod, Ch: k.Ch})
		}
	}

	t.Run("C-s lands at the end of the first forward match", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "foo bar foo")
		feed(t, h, "<ctrl-s>bar")
		assert.True(t, h.IsSearchMode())
		// GNU isearch-forward leaves point after the matched text.
		assert.Equal(t, term.Coordinates{X: 7}, h.CursorAtScroll())
	})

	t.Run("second C-s advances to the next match", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "foo x foo x foo")
		feed(t, h, "<ctrl-s>foo")
		// First match starts at the origin (X:0); point lands at its end.
		assert.Equal(t, term.Coordinates{X: 3}, h.CursorAtScroll())
		feed(t, h, "<ctrl-s>")
		assert.Equal(t, term.Coordinates{X: 9}, h.CursorAtScroll())
		feed(t, h, "<ctrl-s>")
		assert.Equal(t, term.Coordinates{X: 15}, h.CursorAtScroll())
	})

	t.Run("Enter accepts and leaves point at the match", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "foo bar")
		feed(t, h, "<ctrl-s>bar<enter>")
		assert.False(t, h.IsSearchMode())
		assert.Equal(t, term.Coordinates{X: 7}, h.CursorAtScroll())
	})

	t.Run("C-g aborts and restores the origin", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "foo bar foo")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 2}))
		feed(t, h, "<ctrl-s>bar")
		assert.Equal(t, term.Coordinates{X: 7}, h.CursorAtScroll())
		feed(t, h, "<ctrl-g>")
		assert.False(t, h.IsSearchMode())
		assert.Equal(t, term.Coordinates{X: 2}, h.CursorAtScroll())
	})

	t.Run("Backspace widens the query again", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "aXb aY")
		feed(t, h, "<ctrl-s>aX")
		assert.Equal(t, term.Coordinates{X: 2}, h.CursorAtScroll())
		// Deleting the X leaves "a", whose first match still starts at the
		// origin; point sits after it.
		feed(t, h, "<backspace>")
		assert.True(t, h.IsSearchMode())
		assert.Equal(t, term.Coordinates{X: 1}, h.CursorAtScroll())
	})

	t.Run("C-r searches backward", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "foo bar foo")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 10}))
		feed(t, h, "<ctrl-r>foo")
		assert.Equal(t, term.Coordinates{X: 8}, h.CursorAtScroll())
	})

	t.Run("a motion key ends the search and still moves", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "foo bar baz")
		feed(t, h, "<ctrl-s>bar")
		assert.Equal(t, term.Coordinates{X: 7}, h.CursorAtScroll())
		feed(t, h, "<right>")
		assert.False(t, h.IsSearchMode(), "arrow ends isearch")
		assert.Equal(t, term.Coordinates{X: 8}, h.CursorAtScroll())
	})

	t.Run("no match leaves point at origin", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "foo bar")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 1}))
		feed(t, h, "<ctrl-s>zzz")
		assert.True(t, h.IsSearchMode())
		assert.Equal(t, term.Coordinates{X: 1}, h.CursorAtScroll())
	})
}

func TestEmacsQueryReplace(t *testing.T) {
	feed := func(t *testing.T, h text.Handler, keys string) {
		t.Helper()
		seq, err := term.ParseKeys(keys)
		require.NoError(t, err)
		for _, k := range seq {
			h.Handle(term.Event{Type: term.EventKey, Key: k.Key, Mod: k.Mod, Ch: k.Ch})
		}
	}
	// M-% is Alt+Shift+5, which reaches the handler as ModAlt with '%'.
	startReplace := "<alt-shift-5>"

	t.Run("replace all with bang", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "foo foo foo")
		feed(t, h, startReplace+"foo<enter>bar<enter>")
		assert.True(t, h.IsSearchMode(), "loop is active after the two prompts")
		feed(t, h, "!")
		assert.False(t, h.IsSearchMode())
		assert.Equal(t, "bar bar bar", buf.String())
	})

	t.Run("y replaces and n skips per match", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "x x x")
		feed(t, h, startReplace+"x<enter>Y<enter>")
		// Replace first, skip second, replace third.
		feed(t, h, "y")
		feed(t, h, "n")
		feed(t, h, "y")
		assert.Equal(t, "Y x Y", buf.String())
	})

	t.Run("dot replaces current then quits", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "a a a")
		feed(t, h, startReplace+"a<enter>b<enter>")
		feed(t, h, ".")
		assert.False(t, h.IsSearchMode())
		assert.Equal(t, "b a a", buf.String())
	})

	t.Run("q quits without replacing the current match", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "a a")
		feed(t, h, startReplace+"a<enter>b<enter>")
		feed(t, h, "q")
		assert.Equal(t, "a a", buf.String())
	})

	t.Run("C-g quits the loop", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "a a")
		feed(t, h, startReplace+"a<enter>b<enter>")
		feed(t, h, "<ctrl-g>")
		assert.False(t, h.IsSearchMode())
		assert.Equal(t, "a a", buf.String())
	})

	t.Run("replacement containing the search is not rematched", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "a a")
		feed(t, h, startReplace+"a<enter>aa<enter>")
		feed(t, h, "!")
		assert.Equal(t, "aa aa", buf.String())
	})

	t.Run("empty search cancels", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "abc")
		feed(t, h, startReplace+"<enter>")
		assert.False(t, h.IsSearchMode())
		assert.Equal(t, "abc", buf.String())
	})

	t.Run("C-g during the search prompt cancels", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "abc")
		feed(t, h, startReplace+"ab<ctrl-g>")
		assert.False(t, h.IsSearchMode())
		assert.Equal(t, "abc", buf.String())
	})

	t.Run("replace only from point forward", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "a a a")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 2}))
		feed(t, h, startReplace+"a<enter>b<enter>")
		feed(t, h, "!")
		// The first "a" (before point) is left untouched.
		assert.Equal(t, "a b b", buf.String())
	})
}

func TestEmacsKeyboardQuit(t *testing.T) {
	uri, err := workspaceapi.ParseURI("memory:///quit.go")
	require.NoError(t, err)

	buf := cell.NewBuffer()
	buf.ReadFrom(strings.NewReader("alpha beta"))
	h := NewHandler(buf, uri, text.IndentRuneTab, 0)
	h.Resize(80, 10)

	run := func(keys string) {
		seq, err := term.ParseKeys(keys)
		require.NoError(t, err)
		for _, key := range seq {
			h.Handle(term.Event{
				Type: term.EventKey, Key: key.Key, Mod: key.Mod, Ch: key.Ch,
			})
		}
	}
	markLocations := func() []textapi.Location {
		for _, list := range h.LocationLists() {
			if list.ID == emacsMarkLocationListID {
				return list.Locations
			}
		}
		return nil
	}

	// Set a mark and grow a selection towards it.
	run("<ctrl-space><shift-right><shift-right>")
	selection, ok := h.Selection()
	require.True(t, ok)
	require.Equal(t, "al", selection)
	require.Len(t, markLocations(), 1)

	// C-g cancels: selection gone, mark cleared.
	run("<ctrl-g>")
	_, ok = h.Selection()
	assert.False(t, ok)
	assert.Empty(t, markLocations())
}

func TestEmacsTransientModesUpdateStatusBar(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		start    []term.Event
		wantMode string
		finish   []term.Event
	}{
		{
			name:     "incremental search",
			content:  "alpha beta",
			start:    []term.Event{ctrl('s')},
			wantMode: "ISEARCH",
			finish:   []term.Event{key(term.KeyEnter)},
		},
		{
			name:     "query replace",
			content:  "alpha alpha",
			start:    []term.Event{alt('%')},
			wantMode: "QUERY",
			finish:   []term.Event{ctrl('g')},
		},
		{
			name:     "goto prefix",
			content:  "one\ntwo",
			start:    []term.Event{alt('g')},
			wantMode: "GOTO",
			finish:   []term.Event{char('x')},
		},
		{
			name:     "goto prompt",
			content:  "one\ntwo",
			start:    []term.Event{alt('g'), char('g')},
			wantMode: "GOTO",
			finish:   []term.Event{ctrl('g')},
		},
		{
			name:     "zap",
			content:  "alpha beta",
			start:    []term.Event{alt('z')},
			wantMode: "ZAP",
			finish:   []term.Event{ctrl('g')},
		},
		{
			name:     "prefix argument",
			content:  "alpha beta",
			start:    []term.Event{ctrl('u')},
			wantMode: "ARG",
			finish:   []term.Event{ctrl('g')},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _ := newEmacsHandler(t, tt.content)
			root := h.(*emacsHandler)
			bar := new(testStatusBar)
			root.setStatusBar(bar)

			for _, ev := range tt.start {
				require.True(t, runEvent(h, ev))
			}
			require.Equal(t, tt.wantMode, bar.status)

			for _, ev := range tt.finish {
				require.True(t, runEvent(h, ev))
			}
			require.Empty(t, bar.status)
		})
	}
}

func TestEmacsTransientModeStatusKeepsLayoutAttributes(t *testing.T) {
	h, _ := newEmacsHandler(t, "alpha beta",
		WithBarAttr(term.Attributes{Fg: term.ColorBlack, Bg: term.ColorWhite}),
		WithAttr(term.Attributes{Bg: term.ColorGray}),
	)
	root := h.(*emacsHandler)
	bar := new(testStatusBar)
	root.setStatusBar(bar)

	require.True(t, runEvent(h, ctrl('s')))
	require.Equal(t, "ISEARCH", bar.status)
	require.Equal(t, term.Attributes{}, bar.attrs)

	require.True(t, runEvent(h, key(term.KeyEnter)))
	require.Empty(t, bar.status)
	require.Equal(t, term.Attributes{}, bar.attrs)
}

func TestPasteFromClipboardHistory(t *testing.T) {
	uri, err := workspaceapi.ParseURI("memory:///myfile")
	require.NoError(t, err)

	type pasteHistoryStep struct {
		input       string
		wantHandled bool
		wantContent string
		wantCursor  *term.Coordinates
	}

	type pasteHistoryTest struct {
		name        string
		content     string
		history     []string
		wrapHistory bool
		steps       []pasteHistoryStep
	}

	coords := func(pos term.Coordinates) *term.Coordinates { return &pos }

	runKeys := func(t *testing.T, h text.Handler, keys string, wantHandled bool) {
		t.Helper()
		seq, err := term.ParseKeys(keys)
		require.NoError(t, err)
		for _, key := range seq {
			ev := term.Event{Type: term.EventKey, Key: key.Key, Mod: key.Mod, Ch: key.Ch}
			_, ok := h.Handle(ev)
			require.Equal(t, wantHandled, ok, "input %q", keys)
		}
	}

	tests := []pasteHistoryTest{
		{
			name:        "cycles through multiple history entries",
			content:     "z",
			history:     []string{"a", "b", "c"},
			wrapHistory: true,
			steps: []pasteHistoryStep{
				{input: "<end><ctrl-y>", wantHandled: true, wantContent: "zc", wantCursor: coords(term.Coordinates{X: 2})},
				{input: "<alt-y>", wantHandled: true, wantContent: "zb", wantCursor: coords(term.Coordinates{X: 2})},
				{input: "<alt-y>", wantHandled: true, wantContent: "za", wantCursor: coords(term.Coordinates{X: 2})},
				// The kill ring is a ring: after the oldest entry M-y wraps
				// back to the newest, as in GNU yank-pop.
				{input: "<alt-y>", wantHandled: true, wantContent: "zc", wantCursor: coords(term.Coordinates{X: 2})},
			},
		},
		{
			name:        "regular paste restarts history cycle",
			content:     "z",
			history:     []string{"a", "b", "c"},
			wrapHistory: true,
			steps: []pasteHistoryStep{
				{input: "<end><ctrl-y>", wantHandled: true, wantContent: "zc"},
				{input: "<alt-y>", wantHandled: true, wantContent: "zb"},
				{input: "<ctrl-y>", wantHandled: true, wantContent: "zbc"},
				{input: "<alt-y>", wantHandled: true, wantContent: "zbb"},
			},
		},
		{
			name:        "typing after paste blocks yank-pop",
			content:     "z",
			history:     []string{"a", "b"},
			wrapHistory: true,
			steps: []pasteHistoryStep{
				{input: "<end><ctrl-y>", wantHandled: true, wantContent: "zb", wantCursor: coords(term.Coordinates{X: 2})},
				{input: "x", wantHandled: true, wantContent: "zbx", wantCursor: coords(term.Coordinates{X: 3})},
				// GNU yank-pop refuses when the previous command was not a
				// yank; nothing may be inserted.
				{input: "<alt-y>", wantHandled: false, wantContent: "zbx", wantCursor: coords(term.Coordinates{X: 3})},
			},
		},
		{
			name:        "cursor movement after paste blocks yank-pop",
			content:     "z",
			history:     []string{"a", "b"},
			wrapHistory: true,
			steps: []pasteHistoryStep{
				{input: "<end><ctrl-y>", wantHandled: true, wantContent: "zb", wantCursor: coords(term.Coordinates{X: 2})},
				{input: "<left>", wantHandled: true, wantContent: "zb", wantCursor: coords(term.Coordinates{X: 1})},
				{input: "<alt-y>", wantHandled: false, wantContent: "zb", wantCursor: coords(term.Coordinates{X: 1})},
			},
		},
		{
			name:        "clipboard without history support consumes in paste context",
			content:     "z",
			history:     []string{"a", "b"},
			wrapHistory: false,
			steps: []pasteHistoryStep{
				{input: "<end><ctrl-y>", wantHandled: true, wantContent: "zb", wantCursor: coords(term.Coordinates{X: 2})},
				{input: "<alt-y>", wantHandled: true, wantContent: "zb", wantCursor: coords(term.Coordinates{X: 2})},
			},
		},
		{
			name:        "M-y without a prior yank is refused",
			content:     "z",
			history:     []string{"a", "b", "c"},
			wrapHistory: true,
			steps: []pasteHistoryStep{
				{input: "<alt-y>", wantHandled: false, wantContent: "z", wantCursor: coords(term.Coordinates{})},
			},
		},
		{
			name:        "M-y without history support before paste is not handled",
			content:     "z",
			history:     []string{"a", "b"},
			wrapHistory: false,
			steps: []pasteHistoryStep{
				{input: "<alt-y>", wantHandled: false, wantContent: "z", wantCursor: coords(term.Coordinates{})},
			},
		},
		{
			name:        "emacs copy operations populate shared history",
			content:     "ab\ncd",
			wrapHistory: true,
			steps: []pasteHistoryStep{
				// M-w leaves point where it was, so the second region starts
				// there and the yank lands at the end of the line.
				{input: "<shift-right><alt-w><shift-right><alt-w><ctrl-y>", wantHandled: true, wantContent: "abb\ncd"},
				{input: "<alt-y>", wantHandled: true, wantContent: "aba\ncd"},
				// Wraps back to the newest copy.
				{input: "<alt-y>", wantHandled: true, wantContent: "abb\ncd"},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clip := clipboard.NewInMemory()
			var reg clipboard.Register = registerset.New(clip)
			if test.wrapHistory {
				reg = registerhistory.NewClipboard(reg)
			}
			for _, entry := range test.history {
				require.NoError(t, reg.Copy(clipboard.DefaultRegisterID, clipboard.Data{Text: entry, Metadata: text.StandardSelection}))
			}

			buf := cell.NewBuffer()
			buf.ReadFrom(strings.NewReader(test.content))
			h := NewHandler(buf, uri, text.IndentRuneTab, 0, WithClipboard(reg))
			h.Resize(10, 3)

			for _, step := range test.steps {
				runKeys(t, h, step.input, step.wantHandled)
				assert.Equal(t, step.wantContent, buf.String(), "input %q", step.input)
				if step.wantCursor != nil {
					assert.Equal(t, *step.wantCursor, h.CursorAtScroll(), "input %q", step.input)
				}
			}
		})
	}
}

// testIndentView wraps a cell.View and provides an IndentationAt method
// so that ReindentSelection actually adjusts indentation in tests.
type testIndentView struct {
	cell.View
	indents map[int]int // line -> target indentation level
}

func (v testIndentView) IndentationAt(line int) (int, bool) {
	target, ok := v.indents[line]
	return target, ok
}

func TestEmacsTabAtTargetInsertsFullIndentLevel(t *testing.T) {
	uri, err := workspaceapi.ParseURI("memory:///indent.yaml")
	require.NoError(t, err)

	buf := cell.NewBuffer()
	buf.Init()
	_, err = buf.ReadFrom(strings.NewReader("  "))
	require.NoError(t, err)
	buf.WithView(testIndentView{View: buf.View(), indents: map[int]int{0: 1}})

	h := NewHandler(buf, uri, text.IndentRuneSpace, 2, WithTabspaces(2))
	h.Resize(20, 10)
	require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 2, Y: 0}))

	_, handled := h.Handle(term.Event{Type: term.EventKey, Key: term.KeyTab})
	require.True(t, handled)
	assert.Equal(t, "    ", buf.String())
	assert.Equal(t, term.Coordinates{X: 4, Y: 0}, h.CursorAtScroll())

	_, handled = h.Handle(term.Event{Type: term.EventKey, Key: term.KeyTab})
	require.True(t, handled)
	assert.Equal(t, "      ", buf.String())
	assert.Equal(t, term.Coordinates{X: 6, Y: 0}, h.CursorAtScroll())
}

func TestEmacsShiftTabFallsThroughWhenNoDedent(t *testing.T) {
	uri, err := workspaceapi.ParseURI("memory:///dedent.txt")
	require.NoError(t, err)

	buf := cell.NewBuffer()
	buf.Init()
	_, err = buf.ReadFrom(strings.NewReader("hello"))
	require.NoError(t, err)

	h := NewHandler(buf, uri, text.IndentRuneSpace, 2, WithTabspaces(2))
	h.Resize(20, 10)

	_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModShift, Key: term.KeyTab})
	assert.False(t, handled, "shift-tab with nothing to dedent must fall through")
	assert.Equal(t, "hello", buf.String())
}

func TestEmacsShiftTabDedentsWhenIndented(t *testing.T) {
	uri, err := workspaceapi.ParseURI("memory:///dedent.txt")
	require.NoError(t, err)

	buf := cell.NewBuffer()
	buf.Init()
	_, err = buf.ReadFrom(strings.NewReader("  hello"))
	require.NoError(t, err)

	h := NewHandler(buf, uri, text.IndentRuneSpace, 2, WithTabspaces(2))
	h.Resize(20, 10)
	require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 2, Y: 0}))

	_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModShift, Key: term.KeyTab})
	assert.True(t, handled, "shift-tab with indentation must be handled")
	assert.Equal(t, "hello", buf.String())
}

// The tests below form the emacs edge-case battery. They mirror the
// robustness bar set by the vi (modal) handler tests: every functional
// area is exercised at the buffer boundaries (empty buffer, empty lines,
// first/last line and column), with null cells, wide runes, stale
// cursors after external edits, and no-op safety for keys that cannot
// act. They intentionally assert observed handler behavior rather than
// idealized Emacs semantics.

// emacsTestURI is the shared resource URI used by the edge-case tests.
// A .go suffix keeps comment/language wiring consistent with the other
// suites in this file.
func emacsTestURI(t *testing.T) workspaceapi.URI {
	t.Helper()
	uri, err := workspaceapi.ParseURI("memory:///edgecase.go")
	require.NoError(t, err)
	return uri
}

// newEmacsHandler builds an emacs handler over content with a generous
// window so scrolling never interferes with boundary assertions.
func newEmacsHandler(t *testing.T, content string, opts ...Option) (text.Handler, *cell.Buffer) {
	t.Helper()
	buf := cell.NewBuffer()
	_, err := buf.ReadFrom(strings.NewReader(content))
	require.NoError(t, err)
	h := NewHandler(buf, emacsTestURI(t), text.IndentRuneTab, 0, opts...)
	h.Resize(80, 20)
	return h, buf
}

// key builds a plain key event.
func key(k term.Key) term.Event { return term.Event{Type: term.EventKey, Key: k} }

// key2 builds a modified special-key event (e.g. M-DEL is
// key2(term.KeyBackspace, term.ModAlt)).
func key2(k term.Key, mod term.Modifier) term.Event {
	return term.Event{Type: term.EventKey, Key: k, Mod: mod}
}

// ctrl builds a Control-modified character event (e.g. C-a).
func ctrl(ch rune) term.Event {
	return term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: ch}
}

// char builds a plain self-inserting character event.
func char(ch rune) term.Event {
	return term.Event{Type: term.EventKey, Ch: ch}
}

// alt builds a Meta-modified character event (e.g. M-f).
func alt(ch rune) term.Event {
	return term.Event{Type: term.EventKey, Mod: term.ModAlt, Ch: ch}
}

// ctrlAlt builds a Control-Meta-modified character event (e.g. C-M-f).
func ctrlAlt(ch rune) term.Event {
	return term.Event{Type: term.EventKey, Mod: term.ModCtrlAlt, Ch: ch}
}

// runEvent feeds one event and returns whether it was handled.
func runEvent(h text.Handler, ev term.Event) bool {
	_, handled := h.Handle(ev)
	return handled
}

// emacsMarks returns the current emacs mark locations.
func emacsMarks(h text.Handler) []textapi.Location {
	for _, list := range h.LocationLists() {
		if list.ID == emacsMarkLocationListID {
			return list.Locations
		}
	}
	return nil
}

func TestEmacsMotionEdgeCases(t *testing.T) {
	cases := []struct {
		name    string
		content string
		start   term.Coordinates
		event   term.Event
		at      term.Coordinates
	}{
		// Arrow keys at the edges.
		{"left at buffer start", "abc", term.Coordinates{}, key(term.KeyArrowLeft), term.Coordinates{}},
		{"right at buffer end", "abc", term.Coordinates{X: 3}, key(term.KeyArrowRight), term.Coordinates{X: 3}},
		{"up at first line", "a\nb", term.Coordinates{}, key(term.KeyArrowUp), term.Coordinates{}},
		{"down at last line", "a\nb", term.Coordinates{Y: 1}, key(term.KeyArrowDown), term.Coordinates{Y: 1}},
		{"left at column zero stays on line", "ab\ncd", term.Coordinates{Y: 1}, key(term.KeyArrowLeft), term.Coordinates{Y: 1}},
		{"right at line end stays on line", "ab\ncd", term.Coordinates{Y: 0, X: 2}, key(term.KeyArrowRight), term.Coordinates{Y: 0, X: 2}},

		// Ctrl motion aliases (C-f/C-b/C-n/C-p/C-a/C-e).
		{"C-f at end is no-op", "abc", term.Coordinates{X: 3}, ctrl('f'), term.Coordinates{X: 3}},
		{"C-b at start is no-op", "abc", term.Coordinates{}, ctrl('b'), term.Coordinates{}},
		{"C-p at first line is no-op", "a\nb", term.Coordinates{}, ctrl('p'), term.Coordinates{}},
		{"C-n at last line is no-op", "a\nb", term.Coordinates{Y: 1}, ctrl('n'), term.Coordinates{Y: 1}},
		{"C-a already at line start", "abc", term.Coordinates{}, ctrl('a'), term.Coordinates{}},
		{"C-e already at line end", "abc", term.Coordinates{X: 3}, ctrl('e'), term.Coordinates{X: 3}},
		{"C-a from mid line", "abc", term.Coordinates{X: 2}, ctrl('a'), term.Coordinates{}},
		{"C-e from mid line", "abc", term.Coordinates{X: 1}, ctrl('e'), term.Coordinates{X: 3}},

		// Home/End.
		{"Home at start", "abc", term.Coordinates{}, key(term.KeyHome), term.Coordinates{}},
		{"End at end", "abc", term.Coordinates{X: 3}, key(term.KeyEnd), term.Coordinates{X: 3}},

		// Buffer ends (M-< / M->).
		{"M-< at buffer start", "a\nb\nc", term.Coordinates{}, alt('<'), term.Coordinates{}},
		{"M-> from top reaches last line", "a\nb\nc", term.Coordinates{}, alt('>'), term.Coordinates{Y: 2}},
		{"M-< from bottom reaches first line", "a\nb\nc", term.Coordinates{Y: 2}, alt('<'), term.Coordinates{}},

		// Word motion (M-f / M-b) at the ends.
		{"M-f at buffer end", "ab", term.Coordinates{X: 2}, alt('f'), term.Coordinates{X: 2}},
		{"M-b at buffer start", "ab", term.Coordinates{}, alt('b'), term.Coordinates{}},
		{"M-f over single word", "word", term.Coordinates{}, alt('f'), term.Coordinates{X: 4}},
		{"M-b from end of single word", "word", term.Coordinates{X: 4}, alt('b'), term.Coordinates{}},

		// back-to-indentation (M-m).
		{"M-m from end of indented line", "\t\tab", term.Coordinates{X: 4}, alt('m'), term.Coordinates{X: 2}},
		{"M-m already at indentation is no-op", "\t\tab", term.Coordinates{X: 2}, alt('m'), term.Coordinates{X: 2}},
		{"M-m on unindented line", "abc", term.Coordinates{X: 2}, alt('m'), term.Coordinates{}},
		// On an entirely blank line GNU back-to-indentation lands at the
		// end of the line.
		{"M-m on blank line moves to line end", "   ", term.Coordinates{X: 1}, alt('m'), term.Coordinates{X: 3}},

		// Empty buffer: no motion can move.
		{"left on empty buffer", "", term.Coordinates{}, key(term.KeyArrowLeft), term.Coordinates{}},
		{"right on empty buffer", "", term.Coordinates{}, key(term.KeyArrowRight), term.Coordinates{}},
		{"up on empty buffer", "", term.Coordinates{}, key(term.KeyArrowUp), term.Coordinates{}},
		{"down on empty buffer", "", term.Coordinates{}, key(term.KeyArrowDown), term.Coordinates{}},
		{"C-e on empty buffer", "", term.Coordinates{}, ctrl('e'), term.Coordinates{}},
		{"M-f on empty buffer", "", term.Coordinates{}, alt('f'), term.Coordinates{}},
		{"M-> on empty buffer", "", term.Coordinates{}, alt('>'), term.Coordinates{}},

		// Single character buffer.
		{"right past only char", "a", term.Coordinates{X: 1}, key(term.KeyArrowRight), term.Coordinates{X: 1}},
		{"C-a on single char", "a", term.Coordinates{X: 1}, ctrl('a'), term.Coordinates{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := newEmacsHandler(t, tc.content)
			if tc.start != (term.Coordinates{}) {
				require.True(t, h.SetCursorAtScroll(tc.start))
			}
			require.NotPanics(t, func() { h.Handle(tc.event) })
			assert.Equal(t, tc.at, h.CursorAtScroll())
		})
	}
}

func TestEmacsEditingEdgeCases(t *testing.T) {
	cases := []struct {
		name    string
		content string
		start   term.Coordinates
		event   term.Event
		want    string
		at      term.Coordinates
	}{
		// Character insertion.
		{"insert into empty buffer", "", term.Coordinates{}, term.Event{Type: term.EventKey, Ch: 'x'}, "x", term.Coordinates{X: 1}},
		{"insert at end of line", "ab", term.Coordinates{X: 2}, term.Event{Type: term.EventKey, Ch: 'c'}, "abc", term.Coordinates{X: 3}},
		{"insert at start of line", "ab", term.Coordinates{}, term.Event{Type: term.EventKey, Ch: 'z'}, "zab", term.Coordinates{X: 1}},

		// Backspace (also C-h).
		{"backspace at buffer start is no-op", "ab", term.Coordinates{}, key(term.KeyBackspace), "ab", term.Coordinates{}},
		{"backspace on empty buffer is no-op", "", term.Coordinates{}, key(term.KeyBackspace), "", term.Coordinates{}},
		{"backspace mid line", "abc", term.Coordinates{X: 2}, key(term.KeyBackspace), "ac", term.Coordinates{X: 1}},
		{"backspace joins with previous line", "a\nb", term.Coordinates{Y: 1}, key(term.KeyBackspace), "ab", term.Coordinates{X: 1}},
		{"C-h at buffer start is no-op", "ab", term.Coordinates{}, ctrl('h'), "ab", term.Coordinates{}},
		{"C-h mid line", "abc", term.Coordinates{X: 2}, ctrl('h'), "ac", term.Coordinates{X: 1}},

		// Forward delete (Delete / C-d).
		{"delete at buffer end is no-op", "ab", term.Coordinates{X: 2}, key(term.KeyDelete), "ab", term.Coordinates{X: 2}},
		{"delete on empty buffer is no-op", "", term.Coordinates{}, key(term.KeyDelete), "", term.Coordinates{}},
		{"delete mid line", "abc", term.Coordinates{X: 1}, key(term.KeyDelete), "ac", term.Coordinates{X: 1}},
		// Forward delete does not join the following line (asymmetric with
		// backspace, which does join the previous line).
		{"delete at line end is no-op", "a\nb", term.Coordinates{X: 1}, key(term.KeyDelete), "a\nb", term.Coordinates{X: 1}},
		{"C-d at buffer end is no-op", "ab", term.Coordinates{X: 2}, ctrl('d'), "ab", term.Coordinates{X: 2}},
		{"C-d mid line", "abc", term.Coordinates{X: 1}, ctrl('d'), "ac", term.Coordinates{X: 1}},

		// Kill to end of line (C-k).
		{"C-k from start of line", "abc", term.Coordinates{}, ctrl('k'), "", term.Coordinates{}},
		{"C-k from mid line", "abc", term.Coordinates{X: 1}, ctrl('k'), "a", term.Coordinates{X: 1}},
		// GNU kill-line at the very end of the buffer has nothing to kill.
		{"C-k at buffer end is no-op", "abc", term.Coordinates{X: 3}, ctrl('k'), "abc", term.Coordinates{X: 3}},
		// At the end of a non-final line C-k kills the line break instead.
		{"C-k at end of line kills the newline", "ab\ncd", term.Coordinates{X: 2}, ctrl('k'), "abcd", term.Coordinates{X: 2}},
		{"C-k on empty buffer is no-op", "", term.Coordinates{}, ctrl('k'), "", term.Coordinates{}},
		{"C-k on empty line kills the line", "\nb", term.Coordinates{}, ctrl('k'), "b", term.Coordinates{}},

		// Word kill forward (M-d).
		{"M-d kill first word", "alpha beta", term.Coordinates{}, alt('d'), " beta", term.Coordinates{}},
		{"M-d at buffer end is no-op", "alpha", term.Coordinates{X: 5}, alt('d'), "alpha", term.Coordinates{X: 5}},
		{"M-d on empty buffer is no-op", "", term.Coordinates{}, alt('d'), "", term.Coordinates{}},

		// Backward word kill (M-DEL).
		{"M-DEL kill previous word", "alpha beta", term.Coordinates{X: 10}, key2(term.KeyBackspace, term.ModAlt), "alpha ", term.Coordinates{X: 6}},
		{"M-DEL at buffer start is no-op", "alpha", term.Coordinates{}, key2(term.KeyBackspace, term.ModAlt), "alpha", term.Coordinates{}},

		// Forward word kill via Delete+Alt (M-Delete).
		{"M-Delete kill next word", "alpha beta", term.Coordinates{}, key2(term.KeyDelete, term.ModAlt), " beta", term.Coordinates{}},

		// delete-indentation / join (M-^).
		{"M-^ joins onto the previous line", "a\nb", term.Coordinates{Y: 1}, alt('^'), "a b", term.Coordinates{X: 1}},
		{"M-^ on first line is no-op", "a\nb", term.Coordinates{}, alt('^'), "a\nb", term.Coordinates{}},
		{"M-^ on single line is no-op", "abc", term.Coordinates{}, alt('^'), "abc", term.Coordinates{}},

		// delete-horizontal-space (M-\).
		{"M-backslash collapses interior spaces", "a   b", term.Coordinates{X: 2}, alt('\\'), "ab", term.Coordinates{X: 1}},
		{"M-backslash with no spaces is no-op", "ab", term.Coordinates{X: 1}, alt('\\'), "ab", term.Coordinates{X: 1}},
		{"M-backslash trailing spaces", "ab   ", term.Coordinates{X: 5}, alt('\\'), "ab", term.Coordinates{X: 2}},
		{"M-backslash leading spaces", "   ab", term.Coordinates{}, alt('\\'), "ab", term.Coordinates{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, buf := newEmacsHandler(t, tc.content)
			if tc.start != (term.Coordinates{}) {
				require.True(t, h.SetCursorAtScroll(tc.start))
			}
			require.NotPanics(t, func() { h.Handle(tc.event) })
			assert.Equal(t, tc.want, buf.String())
			assert.Equal(t, tc.at, h.CursorAtScroll())
		})
	}
}

func TestEmacsCursorRobustnessAfterExternalEdit(t *testing.T) {
	edit := func(h text.Handler, from, to term.Coordinates, str string) {
		h.CellEditor().Edit(context.Background(), from, to, str)
	}

	t.Run("external delete of whole buffer snaps cursor to origin", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "a\nb\nc\nd\ne")
		for range 4 {
			require.True(t, runEvent(h, key(term.KeyArrowDown)))
		}
		require.Equal(t, term.Coordinates{Y: 4}, h.CursorAtScroll())

		edit(h, term.Coordinates{Y: 0}, term.Coordinates{Y: 5}, "")
		assert.Equal(t, "", buf.String())
		assert.Equal(t, term.Coordinates{}, h.CursorAtScroll())

		// Further motion is a safe no-op; insertion still works.
		assert.False(t, runEvent(h, key(term.KeyArrowRight)))
		assert.Equal(t, term.Coordinates{}, h.CursorAtScroll())
		require.True(t, runEvent(h, term.Event{Type: term.EventKey, Ch: 'z'}))
		assert.Equal(t, "z", buf.String())
		assert.Equal(t, term.Coordinates{X: 1}, h.CursorAtScroll())
	})

	t.Run("external delete of lines below leaves cursor in place", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "a\nb\nc\nd\ne")
		require.True(t, runEvent(h, key(term.KeyArrowDown)))
		require.Equal(t, term.Coordinates{Y: 1}, h.CursorAtScroll())

		edit(h, term.Coordinates{Y: 3}, term.Coordinates{Y: 5}, "")
		assert.Equal(t, "a\nb\nc\n", buf.String())
		assert.Equal(t, term.Coordinates{Y: 1}, h.CursorAtScroll())
	})

	t.Run("external delete of lines above shifts cursor up", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "a\nb\nc\nd\ne")
		for range 3 {
			require.True(t, runEvent(h, key(term.KeyArrowDown)))
		}
		require.Equal(t, term.Coordinates{Y: 3}, h.CursorAtScroll())

		edit(h, term.Coordinates{Y: 0}, term.Coordinates{Y: 2}, "")
		assert.Equal(t, term.Coordinates{Y: 1}, h.CursorAtScroll())
	})

	t.Run("external truncation of current line clamps column", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "abcdef\ngh")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 5}))

		edit(h, term.Coordinates{X: 2}, term.Coordinates{X: 6}, "")
		assert.Equal(t, "ab\ngh", buf.String())
		// Column clamps back to the new (shorter) line end.
		assert.Equal(t, term.Coordinates{X: 2}, h.CursorAtScroll())
		// Editing continues without panic.
		require.True(t, runEvent(h, term.Event{Type: term.EventKey, Ch: 'X'}))
		assert.Equal(t, "abX\ngh", buf.String())
	})

	t.Run("external edit then kill line stays valid", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "one\ntwo\nthree")
		require.True(t, runEvent(h, key(term.KeyArrowDown)))
		edit(h, term.Coordinates{Y: 0}, term.Coordinates{Y: 1}, "")
		assert.Equal(t, "two\nthree", buf.String())
		require.NotPanics(t, func() { h.Handle(ctrl('k')) })
	})

	t.Run("external replace growing the buffer keeps cursor logical", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "a\nb\nc")
		for range 2 {
			require.True(t, runEvent(h, key(term.KeyArrowDown)))
		}
		require.Equal(t, term.Coordinates{Y: 2}, h.CursorAtScroll())

		edit(h, term.Coordinates{Y: 0}, term.Coordinates{Y: 0}, "x\ny\n")
		assert.Equal(t, "x\ny\na\nb\nc", buf.String())
		assert.Equal(t, term.Coordinates{Y: 4}, h.CursorAtScroll())
	})

	t.Run("external shrink to empty leaves navigation panic-free", func(t *testing.T) {
		// An external edit that empties the buffer moves the cursor row to
		// the only remaining line, but the column recorded before the edit
		// can outlive the content it referred to (external edits do not run
		// through SetCursorAtScroll's clamp). The handler must stay
		// panic-free; End collapses the stale column back to zero.
		h, buf := newEmacsHandler(t, "line1\nline2\nline3")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{Y: 2, X: 4}))
		edit(h, term.Coordinates{Y: 0}, term.Coordinates{Y: 3}, "")
		require.Equal(t, "", buf.String())
		assert.Equal(t, 0, h.CursorAtScroll().Y)

		for _, k := range []term.Key{term.KeyArrowUp, term.KeyArrowDown, term.KeyArrowLeft, term.KeyArrowRight, term.KeyEnd, term.KeyHome} {
			require.NotPanics(t, func() { h.Handle(key(k)) })
			assert.Equal(t, 0, h.CursorAtScroll().Y, "row must stay on the only line for %v", k)
		}
		// End on the empty line resolves the column to the true line end.
		assert.Equal(t, term.Coordinates{}, h.CursorAtScroll())

		// Insertion after all of that lands at the origin and works.
		require.True(t, runEvent(h, term.Event{Type: term.EventKey, Ch: 'q'}))
		assert.Equal(t, "q", buf.String())
	})
}

func TestEmacsNullCellHandling(t *testing.T) {
	t.Run("arrow-right traverses null cells", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "a\x00b")
		for _, want := range []int{1, 2, 3, 3} {
			require.NotPanics(t, func() { h.Handle(key(term.KeyArrowRight)) })
			assert.Equal(t, want, h.CursorAtScroll().X)
		}
	})

	t.Run("C-e moves to end past null cells", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "a\x00b")
		require.True(t, runEvent(h, ctrl('e')))
		assert.Equal(t, term.Coordinates{X: 3}, h.CursorAtScroll())
	})

	t.Run("kill to end of line removes null cells", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "a\x00b\nc")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 1}))
		require.True(t, runEvent(h, ctrl('k')))
		assert.Equal(t, "a\nc", buf.String())
	})

	t.Run("join onto previous line collapses null cells at the join", func(t *testing.T) {
		// The NUL filler is treated as horizontal space by the M-^ whitespace
		// fixup, so the join is normalized to a single real space.
		h, buf := newEmacsHandler(t, "a\x00\nb")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{Y: 1}))
		require.True(t, runEvent(h, alt('^')))
		assert.Equal(t, "a b", buf.String())
		assert.Equal(t, term.Coordinates{X: 1}, h.CursorAtScroll())
	})

	t.Run("delete-horizontal-space removes null cells around point", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "a\x00\x00b")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 1}))
		require.NotPanics(t, func() { h.Handle(alt('\\')) })
		assert.Equal(t, "ab", buf.String())
	})

	t.Run("backspace over null cell", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "a\x00b")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 2}))
		require.True(t, runEvent(h, key(term.KeyBackspace)))
		assert.Equal(t, "ab", buf.String())
	})

	t.Run("null-only line navigation and edits are panic-free", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "\x00\x00\x00")
		for _, ev := range []term.Event{
			key(term.KeyArrowRight), ctrl('e'), ctrl('a'), alt('f'), alt('b'),
			ctrl('k'), alt('\\'), key(term.KeyBackspace), key(term.KeyDelete),
		} {
			require.NotPanics(t, func() { h.Handle(ev) })
		}
	})
}

func TestEmacsWideRuneHandling(t *testing.T) {
	t.Run("arrow-right advances one cell per wide rune", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "世界")
		require.True(t, runEvent(h, key(term.KeyArrowRight)))
		assert.Equal(t, term.Coordinates{X: 1}, h.CursorAtScroll())
		require.True(t, runEvent(h, key(term.KeyArrowRight)))
		assert.Equal(t, term.Coordinates{X: 2}, h.CursorAtScroll())
		assert.False(t, runEvent(h, key(term.KeyArrowRight)))
	})

	t.Run("C-e reaches end of wide-rune line", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "世界")
		require.True(t, runEvent(h, ctrl('e')))
		assert.Equal(t, term.Coordinates{X: 2}, h.CursorAtScroll())
	})

	t.Run("M-f crosses a wide word", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "世界 foo")
		require.True(t, runEvent(h, alt('f')))
		assert.Equal(t, term.Coordinates{X: 2}, h.CursorAtScroll())
	})

	t.Run("insert a wide rune into an empty buffer", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "")
		require.True(t, runEvent(h, term.Event{Type: term.EventKey, Ch: '世'}))
		assert.Equal(t, "世", buf.String())
		assert.Equal(t, term.Coordinates{X: 1}, h.CursorAtScroll())
	})

	t.Run("backspace removes a whole wide rune", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "a世b")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 2}))
		require.True(t, runEvent(h, key(term.KeyBackspace)))
		assert.Equal(t, "ab", buf.String())
		assert.Equal(t, term.Coordinates{X: 1}, h.CursorAtScroll())
	})

	t.Run("forward delete removes a whole wide rune", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "a世b")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 1}))
		require.True(t, runEvent(h, key(term.KeyDelete)))
		assert.Equal(t, "ab", buf.String())
	})

	t.Run("emoji rune insertion and deletion", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "")
		require.True(t, runEvent(h, term.Event{Type: term.EventKey, Ch: '🚀'}))
		assert.Equal(t, "🚀", buf.String())
		require.True(t, runEvent(h, key(term.KeyBackspace)))
		assert.Equal(t, "", buf.String())
	})

	t.Run("kill to end of line over wide runes", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "世界世\nx")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 1}))
		require.True(t, runEvent(h, ctrl('k')))
		assert.Equal(t, "世\nx", buf.String())
	})
}

func TestEmacsSelectionEdgeCases(t *testing.T) {
	shiftRight := key2(term.KeyArrowRight, term.ModShift)
	shiftLeft := key2(term.KeyArrowLeft, term.ModShift)

	t.Run("shift-right grows then plain motion clears", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "abcde")
		require.True(t, runEvent(h, shiftRight))
		sel, ok := h.Selection()
		require.True(t, ok)
		assert.Equal(t, "a", sel)

		require.True(t, runEvent(h, key(term.KeyArrowRight)))
		_, ok = h.Selection()
		assert.False(t, ok)
		assert.Equal(t, term.Coordinates{X: 2}, h.CursorAtScroll())
	})

	t.Run("shift-left shrinks a growing selection", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "abcde")
		require.True(t, runEvent(h, shiftRight))
		require.True(t, runEvent(h, shiftRight))
		sel, _ := h.Selection()
		require.Equal(t, "ab", sel)

		require.True(t, runEvent(h, shiftLeft))
		sel, ok := h.Selection()
		require.True(t, ok)
		assert.Equal(t, "a", sel)
	})

	t.Run("Esc clears the selection", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "abcde")
		require.True(t, runEvent(h, shiftRight))
		require.True(t, runEvent(h, shiftRight))
		require.True(t, runEvent(h, key(term.KeyEsc)))
		_, ok := h.Selection()
		assert.False(t, ok)
		assert.Equal(t, term.Coordinates{X: 2}, h.CursorAtScroll())
	})

	t.Run("Delete removes the selection", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "abcde")
		require.True(t, runEvent(h, shiftRight))
		require.True(t, runEvent(h, shiftRight))
		require.True(t, runEvent(h, key(term.KeyDelete)))
		assert.Equal(t, "cde", buf.String())
		_, ok := h.Selection()
		assert.False(t, ok)
	})

	t.Run("Backspace removes the selection", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "abcde")
		require.True(t, runEvent(h, shiftRight))
		require.True(t, runEvent(h, shiftRight))
		require.True(t, runEvent(h, key(term.KeyBackspace)))
		assert.Equal(t, "cde", buf.String())
	})

	t.Run("Space over a selection replaces it with a space", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "abcde")
		require.True(t, runEvent(h, shiftRight))
		require.True(t, runEvent(h, shiftRight))
		require.True(t, runEvent(h, key(term.KeySpace)))
		assert.Equal(t, " cde", buf.String())
		assert.Equal(t, term.Coordinates{X: 1}, h.CursorAtScroll())
	})

	t.Run("shift-right at buffer end cannot select", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "ab")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 2}))
		require.NotPanics(t, func() { h.Handle(shiftRight) })
		_, ok := h.Selection()
		assert.False(t, ok)
		assert.Equal(t, term.Coordinates{X: 2}, h.CursorAtScroll())
	})

	t.Run("shift-select on empty buffer is a safe no-op", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "")
		require.NotPanics(t, func() { h.Handle(shiftRight) })
		_, ok := h.Selection()
		assert.False(t, ok)
	})
}

// allEmacsBindings returns one event for every key binding the handler
// recognizes across all modifier layers. It is used to prove that no
// binding panics regardless of the buffer or cursor state.
func allEmacsBindings() []term.Event {
	var evs []term.Event
	// Plain keys.
	for _, k := range []term.Key{
		term.KeyEsc, term.KeyEnd, term.KeyHome, term.KeyPgup, term.KeyPgdn,
		term.KeyArrowLeft, term.KeyArrowRight, term.KeyArrowUp, term.KeyArrowDown,
		term.KeyEnter, term.KeySpace, term.KeyTab, term.KeyBackspace, term.KeyDelete,
	} {
		evs = append(evs, key(k))
	}
	evs = append(evs, term.Event{Type: term.EventKey, Ch: 'a'}) // plain insert
	// GNU kmacro keys.
	evs = append(evs, key(term.KeyF3), key(term.KeyF4))

	// Meta (M-) layer.
	for _, ch := range []rune{'f', 'b', 'd', 'w', '<', '>', 'v', 't', 'm', '^', '\\', 'u', 'l', 'c', ';', 'y', 'q', 'g', '%', '{', '}'} {
		evs = append(evs, alt(ch))
	}
	evs = append(evs,
		key2(term.KeyArrowDown, term.ModAlt), key2(term.KeyArrowUp, term.ModAlt),
		key2(term.KeyArrowLeft, term.ModAlt), key2(term.KeyArrowRight, term.ModAlt),
		key2(term.KeyBackspace, term.ModAlt), key2(term.KeyDelete, term.ModAlt),
		key2(term.KeySpace, term.ModAlt),
	)

	// Meta-Shift line duplication.
	evs = append(evs,
		key2(term.KeyArrowDown, term.ModAltShift), key2(term.KeyArrowUp, term.ModAltShift),
	)

	// Control (C-) layer.
	for _, ch := range []rune{
		'y', 'l', 'v', 'g', 'o', 'j', 'm', 'i', 'd', 'h', 'a', 'p', 'n',
		'q', 'e', 'f', 'b', 'k', '-', '_', 't', 'w', '=', 's', 'r', '?', '/',
	} {
		evs = append(evs, ctrl(ch))
	}
	evs = append(evs, key2(term.KeyEnter, term.ModCtrl), key2(term.KeySpace, term.ModCtrl))

	// Control-Shift layer.
	evs = append(evs,
		key2(term.KeyEnter, term.ModCtrlShift),
		key2(term.KeyBackspace, term.ModCtrlShift),
		term.Event{Type: term.EventKey, Mod: term.ModCtrlShift, Ch: 'M'},
		term.Event{Type: term.EventKey, Mod: term.ModCtrlShift, Ch: 'W'},
		term.Event{Type: term.EventKey, Mod: term.ModCtrlShift, Ch: 'Z'},
		term.Event{Type: term.EventKey, Mod: term.ModCtrlShift, Ch: 'A'},
		term.Event{Type: term.EventKey, Mod: term.ModCtrlShift, Ch: 'H'},
		term.Event{Type: term.EventKey, Mod: term.ModCtrlShift, Ch: 'V'},
	)

	// Control-Meta layer.
	for _, ch := range []rune{'h', 'f', 'b', 'k', 'a', 'e', 'u', 'd', '/', '_'} {
		evs = append(evs, ctrlAlt(ch))
	}
	evs = append(evs,
		key2(term.KeyArrowUp, term.ModCtrlAlt), key2(term.KeyArrowDown, term.ModCtrlAlt),
	)
	return evs
}

func TestEmacsNoOpSafety(t *testing.T) {
	fixtures := []struct {
		name    string
		content string
		arrange func(t *testing.T, h text.Handler)
	}{
		{name: "empty buffer", content: ""},
		{name: "single character", content: "a"},
		{name: "single blank line", content: " "},
		{name: "trailing newline", content: "a\n"},
		{name: "null cells only", content: "\x00\x00"},
		{
			name:    "cursor past last column",
			content: "abc",
			arrange: func(t *testing.T, h text.Handler) {
				h.SetCursorAtScroll(term.Coordinates{X: 99})
			},
		},
		{
			name:    "cursor past last line",
			content: "a\nb",
			arrange: func(t *testing.T, h text.Handler) {
				h.SetCursorAtScroll(term.Coordinates{Y: 99})
			},
		},
		{
			name:    "cursor stranded after external shrink",
			content: "a\nb\nc\nd",
			arrange: func(t *testing.T, h text.Handler) {
				require.True(t, h.SetCursorAtScroll(term.Coordinates{Y: 3, X: 1}))
				h.CellEditor().Edit(context.Background(), term.Coordinates{Y: 0}, term.Coordinates{Y: 4}, "")
			},
		},
	}

	for _, fx := range fixtures {
		t.Run(fx.name, func(t *testing.T) {
			for _, ev := range allEmacsBindings() {
				h, _ := newEmacsHandler(t, fx.content)
				if fx.arrange != nil {
					fx.arrange(t, h)
				}
				require.NotPanicsf(t, func() { h.Handle(ev) },
					"binding %+v panicked on %q", ev, fx.content)
				// The handler must still accept input afterwards.
				require.NotPanics(t, func() {
					h.Handle(term.Event{Type: term.EventKey, Ch: 'z'})
				})
			}
		})
	}
}

func TestEmacsResizeRobustness(t *testing.T) {
	t.Run("cursor survives shrink and grow", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "a\nb\nc\nd\ne")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{Y: 3}))
		require.Equal(t, term.Coordinates{Y: 3}, h.CursorAtScroll())

		for _, dim := range []struct{ w, h int }{{1, 1}, {80, 20}, {2, 2}, {40, 3}} {
			h.Resize(dim.w, dim.h)
			assert.Equal(t, term.Coordinates{Y: 3}, h.CursorAtScroll(),
				"cursor must survive resize to %dx%d", dim.w, dim.h)
		}
	})

	t.Run("set through zero window applies after resize", func(t *testing.T) {
		buf := cell.NewBuffer()
		_, err := buf.ReadFrom(strings.NewReader("a\nb\nc\nd\ne"))
		require.NoError(t, err)
		h := NewHandler(buf, emacsTestURI(t), text.IndentRuneTab, 0)
		h.Resize(0, 0)

		// A zero window defers the set; the pending position is applied
		// once the window has real dimensions.
		assert.False(t, h.SetCursorAtScroll(term.Coordinates{Y: 3}))
		h.Resize(80, 20)
		assert.Equal(t, term.Coordinates{Y: 3}, h.CursorAtScroll())
	})

	t.Run("out-of-range set through zero window is clamped", func(t *testing.T) {
		buf := cell.NewBuffer()
		_, err := buf.ReadFrom(strings.NewReader("a\nb"))
		require.NoError(t, err)
		h := NewHandler(buf, emacsTestURI(t), text.IndentRuneTab, 0)
		h.Resize(0, 0)

		assert.False(t, h.SetCursorAtScroll(term.Coordinates{Y: 99}))
		h.Resize(80, 20)
		assert.Equal(t, term.Coordinates{Y: 1}, h.CursorAtScroll())
	})

	t.Run("editing after collapse and grow is panic-free", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "hello")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 5}))
		h.Resize(1, 1)
		h.Resize(80, 20)
		require.NotPanics(t, func() {
			h.Handle(term.Event{Type: term.EventKey, Ch: '!'})
		})
		assert.Equal(t, "hello!", buf.String())
	})
}

func TestEmacsMarkRobustness(t *testing.T) {
	t.Run("C-g clears a pending mark", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "abcde")
		require.True(t, runEvent(h, key2(term.KeySpace, term.ModCtrl)))
		require.Len(t, emacsMarks(h), 1)

		require.NotPanics(t, func() { h.Handle(ctrl('g')) })
		assert.Empty(t, emacsMarks(h))
	})

	t.Run("multiple set-mark commands accumulate", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "a\nb\nc")
		require.True(t, runEvent(h, key2(term.KeySpace, term.ModCtrl)))
		require.True(t, runEvent(h, key(term.KeyArrowDown)))
		require.True(t, runEvent(h, key2(term.KeySpace, term.ModCtrl)))
		marks := emacsMarks(h)
		require.Len(t, marks, 2)
		assert.Equal(t, term.Coordinates{}, marks[0].From)
		assert.Equal(t, term.Coordinates{Y: 1}, marks[1].From)
	})

	t.Run("external delete of the whole buffer keeps mark until C-g", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "a\nb\nc")
		require.True(t, runEvent(h, key(term.KeyArrowDown)))
		require.True(t, runEvent(h, key2(term.KeySpace, term.ModCtrl)))
		require.Len(t, emacsMarks(h), 1)

		h.CellEditor().Edit(context.Background(), term.Coordinates{Y: 0}, term.Coordinates{Y: 3}, "")
		require.Equal(t, "", buf.String())
		// The mark location survives the external delete (its stored
		// coordinates are remapped by the edit, not dropped).
		require.Len(t, emacsMarks(h), 1)
		// C-g then clears it, and is safe even though the mark is stale.
		require.NotPanics(t, func() { h.Handle(ctrl('g')) })
		assert.Empty(t, emacsMarks(h))
	})

	t.Run("kill-region after external shrink is panic-free", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "alpha\nbeta\ngamma")
		require.True(t, runEvent(h, key(term.KeyArrowDown)))
		require.True(t, runEvent(h, key2(term.KeySpace, term.ModCtrl)))
		// Shrink the buffer out from under the mark and point.
		h.CellEditor().Edit(context.Background(), term.Coordinates{Y: 0}, term.Coordinates{Y: 2}, "")
		require.NotPanics(t, func() { h.Handle(ctrl('w')) })
		_ = buf
	})

	t.Run("set-mark on empty buffer then C-g", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "")
		require.NotPanics(t, func() { h.Handle(key2(term.KeySpace, term.ModCtrl)) })
		require.NotPanics(t, func() { h.Handle(ctrl('g')) })
		assert.Empty(t, emacsMarks(h))
	})
}

// newEmacsHandlerWithHistory builds a handler backed by a history-aware
// clipboard so kill-ring and yank-pop behavior can be exercised.
func newEmacsHandlerWithHistory(t *testing.T, content string) (text.Handler, *cell.Buffer, clipboard.Register) {
	t.Helper()
	clip := clipboard.NewInMemory()
	reg := registerhistory.NewClipboard(registerset.New(clip))
	buf := cell.NewBuffer()
	_, err := buf.ReadFrom(strings.NewReader(content))
	require.NoError(t, err)
	h := NewHandler(buf, emacsTestURI(t), text.IndentRuneTab, 0, WithClipboard(reg))
	h.Resize(80, 20)
	return h, buf, reg
}

func TestEmacsKillRingEdgeCases(t *testing.T) {
	t.Run("C-y with empty clipboard is a safe no-op", func(t *testing.T) {
		h, buf, _ := newEmacsHandlerWithHistory(t, "abc")
		require.NotPanics(t, func() { h.Handle(ctrl('y')) })
		assert.Equal(t, "abc", buf.String())
		assert.Equal(t, term.Coordinates{}, h.CursorAtScroll())
	})

	t.Run("M-y without a prior yank is not handled", func(t *testing.T) {
		h, buf, _ := newEmacsHandlerWithHistory(t, "abc")
		assert.False(t, runEvent(h, alt('y')))
		assert.Equal(t, "abc", buf.String())
	})

	t.Run("C-w deletes the region", func(t *testing.T) {
		h, buf, _ := newEmacsHandlerWithHistory(t, "alpha beta")
		// Mark at start, select "alpha", kill it.
		require.True(t, runEvent(h, key2(term.KeySpace, term.ModCtrl)))
		for range 5 {
			require.True(t, runEvent(h, key2(term.KeyArrowRight, term.ModShift)))
		}
		require.True(t, runEvent(h, ctrl('w')))
		assert.Equal(t, " beta", buf.String())
		assert.Equal(t, term.Coordinates{}, h.CursorAtScroll())
	})

	t.Run("M-w copy then C-y yank duplicates the region", func(t *testing.T) {
		h, buf, _ := newEmacsHandlerWithHistory(t, "alpha beta")
		require.True(t, runEvent(h, key2(term.KeySpace, term.ModCtrl)))
		for range 5 {
			require.True(t, runEvent(h, key2(term.KeyArrowRight, term.ModShift)))
		}
		require.True(t, runEvent(h, alt('w')))
		require.True(t, runEvent(h, key(term.KeyEnd)))
		require.True(t, runEvent(h, ctrl('y')))
		assert.Equal(t, "alpha betaalpha", buf.String())
	})

	t.Run("yank on empty buffer with empty clipboard is safe", func(t *testing.T) {
		h, buf, _ := newEmacsHandlerWithHistory(t, "")
		require.NotPanics(t, func() { h.Handle(ctrl('y')) })
		require.NotPanics(t, func() { h.Handle(alt('y')) })
		assert.Equal(t, "", buf.String())
	})

	t.Run("C-y yank-pop cycle through kill ring", func(t *testing.T) {
		h, buf, reg := newEmacsHandlerWithHistory(t, "z")
		for _, entry := range []string{"a", "b", "c"} {
			require.NoError(t, reg.Copy(clipboard.DefaultRegisterID,
				clipboard.Data{Text: entry, Metadata: text.StandardSelection}))
		}
		require.True(t, runEvent(h, key(term.KeyEnd)))
		require.True(t, runEvent(h, ctrl('y')))
		assert.Equal(t, "zc", buf.String())
		require.True(t, runEvent(h, alt('y')))
		assert.Equal(t, "zb", buf.String())
		require.True(t, runEvent(h, alt('y')))
		assert.Equal(t, "za", buf.String())
	})

	t.Run("yank-pop of a block entry undoes in one step", func(t *testing.T) {
		h, buf, reg := newEmacsHandlerWithHistory(t, "z\nz")
		require.NoError(t, reg.Copy(clipboard.DefaultRegisterID,
			clipboard.Data{Text: "1\n2", Metadata: text.BlockSelection}))
		require.NoError(t, reg.Copy(clipboard.DefaultRegisterID,
			clipboard.Data{Text: "c", Metadata: text.StandardSelection}))
		require.True(t, runEvent(h, ctrl('y')))
		require.Equal(t, "cz\nz", buf.String())
		require.True(t, runEvent(h, alt('y')))
		require.Equal(t, "1z\n2z", buf.String())
		require.True(t, runEvent(h, ctrl('/')))
		assert.Equal(t, "z\nz", buf.String(), "the block yank-pop reverts as one group")
	})
}

func TestEmacsSexpMotionEdgeCases(t *testing.T) {
	cases := []struct {
		name    string
		content string
		start   term.Coordinates
		event   term.Event
		handled bool
		at      term.Coordinates
	}{
		{"forward over nested from outer opener", "((a))b", term.Coordinates{}, ctrlAlt('f'), true, term.Coordinates{X: 5}},
		{"forward over nested from inner opener", "((a))b", term.Coordinates{X: 1}, ctrlAlt('f'), true, term.Coordinates{X: 4}},
		{"forward scans to next opener", "a(x)y", term.Coordinates{}, ctrlAlt('f'), true, term.Coordinates{X: 4}},
		{"forward on closer is a no-op", "(a)", term.Coordinates{X: 2}, ctrlAlt('f'), false, term.Coordinates{X: 2}},
		{"forward with mismatched brackets is a no-op", "(a]b", term.Coordinates{}, ctrlAlt('f'), false, term.Coordinates{}},
		{"forward with unclosed bracket is a no-op", "(ab", term.Coordinates{}, ctrlAlt('f'), false, term.Coordinates{}},
		{"forward across a line boundary", "(a\nb)", term.Coordinates{}, ctrlAlt('f'), true, term.Coordinates{Y: 1, X: 2}},
		{"forward on empty buffer is a no-op", "", term.Coordinates{}, ctrlAlt('f'), false, term.Coordinates{}},
		{"backward from after a closer", "(a)", term.Coordinates{X: 3}, ctrlAlt('b'), true, term.Coordinates{}},
		{"backward over nested closers", "((a))", term.Coordinates{X: 5}, ctrlAlt('b'), true, term.Coordinates{}},
		{"backward with unopened bracket is a no-op", "ab)", term.Coordinates{X: 3}, ctrlAlt('b'), false, term.Coordinates{X: 3}},
		{"backward on empty buffer is a no-op", "", term.Coordinates{}, ctrlAlt('b'), false, term.Coordinates{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := newEmacsHandler(t, tc.content)
			if tc.start != (term.Coordinates{}) {
				require.True(t, h.SetCursorAtScroll(tc.start))
			}
			var handled bool
			require.NotPanics(t, func() { _, handled = h.Handle(tc.event) })
			assert.Equal(t, tc.handled, handled)
			assert.Equal(t, tc.at, h.CursorAtScroll())
		})
	}
}

func TestEmacsStructuralMotion(t *testing.T) {
	cases := []struct {
		name    string
		content string
		start   term.Coordinates
		event   term.Event
		handled bool
		at      term.Coordinates
	}{
		{"C-M-a to enclosing block start", "{ab}", term.Coordinates{X: 2}, ctrlAlt('a'), true, term.Coordinates{}},
		{"C-M-a innermost of nested", "{[a]}", term.Coordinates{X: 2}, ctrlAlt('a'), true, term.Coordinates{X: 1}},
		{"C-M-a outside any block is a no-op", "abc", term.Coordinates{X: 1}, ctrlAlt('a'), false, term.Coordinates{X: 1}},
		{"C-M-e to enclosing block end", "{ab}", term.Coordinates{X: 2}, ctrlAlt('e'), true, term.Coordinates{X: 4}},
		{"C-M-e innermost of nested", "{[a]}", term.Coordinates{X: 2}, ctrlAlt('e'), true, term.Coordinates{X: 4}},
		{"C-M-u to enclosing opener", "(ab)", term.Coordinates{X: 2}, ctrlAlt('u'), true, term.Coordinates{}},
		{"C-M-u outside any block is a no-op", "ab", term.Coordinates{X: 1}, ctrlAlt('u'), false, term.Coordinates{X: 1}},
		{"C-M-d into the next opener", "a(b)", term.Coordinates{}, ctrlAlt('d'), true, term.Coordinates{X: 2}},
		{"C-M-d already before opener", "(b)", term.Coordinates{}, ctrlAlt('d'), true, term.Coordinates{X: 1}},
		{"C-M-d with no opener is a no-op", "abc", term.Coordinates{}, ctrlAlt('d'), false, term.Coordinates{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := newEmacsHandler(t, tc.content)
			if tc.start != (term.Coordinates{}) {
				require.True(t, h.SetCursorAtScroll(tc.start))
			}
			var handled bool
			require.NotPanics(t, func() { _, handled = h.Handle(tc.event) })
			assert.Equal(t, tc.handled, handled)
			assert.Equal(t, tc.at, h.CursorAtScroll())
		})
	}
}

func TestEmacsKillSexp(t *testing.T) {
	cases := []struct {
		name    string
		content string
		start   term.Coordinates
		handled bool
		want    string
		at      term.Coordinates
	}{
		{"kill from opener", "(ab)c", term.Coordinates{}, true, "c", term.Coordinates{}},
		{"kill scans forward to opener", "x(ab)c", term.Coordinates{}, true, "xc", term.Coordinates{X: 1}},
		{"kill nested", "([a])b", term.Coordinates{}, true, "b", term.Coordinates{}},
		{"no bracket is a no-op", "abc", term.Coordinates{}, false, "abc", term.Coordinates{}},
		{"unclosed bracket is a no-op", "(ab", term.Coordinates{}, false, "(ab", term.Coordinates{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, buf := newEmacsHandler(t, tc.content)
			if tc.start != (term.Coordinates{}) {
				require.True(t, h.SetCursorAtScroll(tc.start))
			}
			var handled bool
			require.NotPanics(t, func() { _, handled = h.Handle(ctrlAlt('k')) })
			assert.Equal(t, tc.handled, handled)
			assert.Equal(t, tc.want, buf.String())
			assert.Equal(t, tc.at, h.CursorAtScroll())
		})
	}
}

func TestEmacsLineManipulationEdgeCases(t *testing.T) {
	t.Run("move line up at the top is a no-op", func(t *testing.T) {
		h, buf, _ := newEmacsHandlerWithHistory(t, "a\nb\nc")
		assert.False(t, runEvent(h, key2(term.KeyArrowUp, term.ModAlt)))
		assert.Equal(t, "a\nb\nc", buf.String())
		assert.Equal(t, term.Coordinates{}, h.CursorAtScroll())
	})

	t.Run("move line down at the bottom is a no-op", func(t *testing.T) {
		h, buf, _ := newEmacsHandlerWithHistory(t, "a\nb\nc")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{Y: 2}))
		assert.False(t, runEvent(h, key2(term.KeyArrowDown, term.ModAlt)))
		assert.Equal(t, "a\nb\nc", buf.String())
		assert.Equal(t, term.Coordinates{Y: 2}, h.CursorAtScroll())
	})

	t.Run("move middle line up lands with the line", func(t *testing.T) {
		h, buf, _ := newEmacsHandlerWithHistory(t, "a\nb\nc")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{Y: 1}))
		require.True(t, runEvent(h, key2(term.KeyArrowUp, term.ModAlt)))
		assert.Equal(t, "b\na\nc", buf.String())
		assert.Equal(t, term.Coordinates{}, h.CursorAtScroll())
	})

	t.Run("duplicate line down leaves point on the copy", func(t *testing.T) {
		h, buf, _ := newEmacsHandlerWithHistory(t, "a\nb\nc")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{Y: 1}))
		require.True(t, runEvent(h, key2(term.KeyArrowDown, term.ModAltShift)))
		assert.Equal(t, "a\nb\nb\nc", buf.String())
		assert.Equal(t, term.Coordinates{Y: 2}, h.CursorAtScroll())
	})

	t.Run("duplicate line up leaves point on the original row", func(t *testing.T) {
		h, buf, _ := newEmacsHandlerWithHistory(t, "a\nb\nc")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{Y: 1}))
		require.True(t, runEvent(h, key2(term.KeyArrowUp, term.ModAltShift)))
		assert.Equal(t, "a\nb\nb\nc", buf.String())
		assert.Equal(t, term.Coordinates{Y: 1}, h.CursorAtScroll())
	})

	t.Run("move first line down swaps with the next", func(t *testing.T) {
		h, buf, _ := newEmacsHandlerWithHistory(t, "a\nb\nc")
		require.True(t, runEvent(h, key2(term.KeyArrowDown, term.ModAlt)))
		assert.Equal(t, "b\na\nc", buf.String())
	})

	t.Run("move line up swaps with the previous", func(t *testing.T) {
		h, buf, _ := newEmacsHandlerWithHistory(t, "a\nb\nc")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{Y: 1}))
		require.True(t, runEvent(h, key2(term.KeyArrowUp, term.ModAlt)))
		assert.Equal(t, "b\na\nc", buf.String())
	})

	t.Run("move on empty buffer is panic-free", func(t *testing.T) {
		h, _, _ := newEmacsHandlerWithHistory(t, "")
		require.NotPanics(t, func() { h.Handle(key2(term.KeyArrowUp, term.ModAlt)) })
		require.NotPanics(t, func() { h.Handle(key2(term.KeyArrowDown, term.ModAlt)) })
	})

	t.Run("duplicate only line", func(t *testing.T) {
		h, buf, _ := newEmacsHandlerWithHistory(t, "solo")
		require.True(t, runEvent(h, key2(term.KeyArrowDown, term.ModAltShift)))
		assert.Equal(t, "solo\nsolo", buf.String())
	})

	t.Run("duplicate empty buffer is a no-op", func(t *testing.T) {
		h, buf, _ := newEmacsHandlerWithHistory(t, "")
		require.NotPanics(t, func() { h.Handle(key2(term.KeyArrowDown, term.ModAltShift)) })
		assert.Equal(t, "", buf.String())
	})

}

// newEmacsHandlerWithClipboard builds a handler over an inspectable
// clipboard so tests can assert the exact kill-ring contents.
func newEmacsHandlerWithClipboard(t *testing.T, content string) (text.Handler, *cell.Buffer, clipboard.Register) {
	t.Helper()
	clip := clipboard.NewInMemory()
	buf := cell.NewBuffer()
	_, err := buf.ReadFrom(strings.NewReader(content))
	require.NoError(t, err)
	h := NewHandler(buf, emacsTestURI(t), text.IndentRuneTab, 0, WithClipboard(clip))
	h.Resize(80, 20)
	return h, buf, clip
}

// killRingText returns the current head of the kill ring, or "" when the
// clipboard is empty.
func killRingText(clip clipboard.Register) string {
	data, err := clip.Paste(clipboard.DefaultRegisterID)
	if err != nil {
		return ""
	}
	return data.Text
}

func TestEmacsWordMotion(t *testing.T) {
	cases := []struct {
		name    string
		content string
		start   term.Coordinates
		event   term.Event
		handled bool
		at      term.Coordinates
	}{
		// forward-word (M-f).
		{"M-f from word start", "alpha beta", term.Coordinates{}, alt('f'), true, term.Coordinates{X: 5}},
		{"M-f from inside word", "alpha beta", term.Coordinates{X: 2}, alt('f'), true, term.Coordinates{X: 5}},
		{"M-f from gap between words", "alpha beta", term.Coordinates{X: 5}, alt('f'), true, term.Coordinates{X: 10}},
		{"M-f skips punctuation runs", "foo.bar", term.Coordinates{X: 3}, alt('f'), true, term.Coordinates{X: 7}},
		{"M-f stops before punctuation", "foo.bar", term.Coordinates{}, alt('f'), true, term.Coordinates{X: 3}},
		{"M-f treats underscore as separator", "foo_bar", term.Coordinates{}, alt('f'), true, term.Coordinates{X: 3}},
		{"M-f crosses the line break", "alpha\nbeta x", term.Coordinates{X: 5}, alt('f'), true, term.Coordinates{Y: 1, X: 4}},
		{"M-f from blank line reaches next word end", "a\n\nbeta", term.Coordinates{Y: 1}, alt('f'), true, term.Coordinates{Y: 2, X: 4}},
		{"M-f with only spaces ahead moves to buffer end", "ab   ", term.Coordinates{X: 2}, alt('f'), true, term.Coordinates{X: 5}},
		{"M-f at buffer end is a no-op", "ab", term.Coordinates{X: 2}, alt('f'), false, term.Coordinates{X: 2}},
		{"M-f on empty buffer is a no-op", "", term.Coordinates{}, alt('f'), false, term.Coordinates{}},
		{"M-f over digits", "123 abc", term.Coordinates{}, alt('f'), true, term.Coordinates{X: 3}},
		{"M-f over wide runes", "世界 foo", term.Coordinates{}, alt('f'), true, term.Coordinates{X: 2}},
		{"M-f stops at null cell", "a\x00b", term.Coordinates{}, alt('f'), true, term.Coordinates{X: 1}},
		{"M-right matches M-f", "alpha beta", term.Coordinates{}, key2(term.KeyArrowRight, term.ModAlt), true, term.Coordinates{X: 5}},

		// backward-word (M-b).
		{"M-b from word end", "alpha beta", term.Coordinates{X: 10}, alt('b'), true, term.Coordinates{X: 6}},
		{"M-b from inside word", "alpha beta", term.Coordinates{X: 8}, alt('b'), true, term.Coordinates{X: 6}},
		{"M-b from gap between words", "alpha beta", term.Coordinates{X: 5}, alt('b'), true, term.Coordinates{}},
		{"M-b skips punctuation runs", "foo.bar", term.Coordinates{X: 4}, alt('b'), true, term.Coordinates{}},
		{"M-b crosses the line break", "alpha\nbeta", term.Coordinates{Y: 1}, alt('b'), true, term.Coordinates{}},
		{"M-b with only spaces behind moves to buffer start", "  ab", term.Coordinates{X: 2}, alt('b'), true, term.Coordinates{}},
		{"M-b at buffer start is a no-op", "ab", term.Coordinates{}, alt('b'), false, term.Coordinates{}},
		{"M-b on empty buffer is a no-op", "", term.Coordinates{}, alt('b'), false, term.Coordinates{}},
		{"M-left matches M-b", "alpha beta", term.Coordinates{X: 10}, key2(term.KeyArrowLeft, term.ModAlt), true, term.Coordinates{X: 6}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := newEmacsHandler(t, tc.content)
			if tc.start != (term.Coordinates{}) {
				require.True(t, h.SetCursorAtScroll(tc.start))
			}
			var handled bool
			require.NotPanics(t, func() { _, handled = h.Handle(tc.event) })
			assert.Equal(t, tc.handled, handled)
			assert.Equal(t, tc.at, h.CursorAtScroll())
		})
	}
}

func TestEmacsTransposeWords(t *testing.T) {
	cases := []struct {
		name    string
		content string
		start   term.Coordinates
		handled bool
		want    string
		at      term.Coordinates
	}{
		{"between two words", "alpha beta", term.Coordinates{X: 5}, true, "beta alpha", term.Coordinates{X: 10}},
		{"inside the second word", "alpha beta", term.Coordinates{X: 8}, true, "beta alpha", term.Coordinates{X: 10}},
		{"at start of the second word", "alpha beta", term.Coordinates{X: 6}, true, "beta alpha", term.Coordinates{X: 10}},
		{"at end of buffer swaps the last two words", "alpha beta", term.Coordinates{X: 10}, true, "beta alpha", term.Coordinates{X: 10}},
		{"inside the first word is a no-op", "alpha beta", term.Coordinates{X: 2}, false, "alpha beta", term.Coordinates{X: 2}},
		{"before the first word is a no-op", "  alpha beta", term.Coordinates{}, false, "  alpha beta", term.Coordinates{}},
		{"single word is a no-op", "word", term.Coordinates{X: 4}, false, "word", term.Coordinates{X: 4}},
		{"empty buffer is a no-op", "", term.Coordinates{}, false, "", term.Coordinates{}},
		{"punctuation separator is preserved", "foo, bar", term.Coordinates{X: 4}, true, "bar, foo", term.Coordinates{X: 8}},
		{"underscore separator is preserved", "foo_bar", term.Coordinates{X: 4}, true, "bar_foo", term.Coordinates{X: 7}},
		{"multiple spaces are preserved", "a   b", term.Coordinates{X: 2}, true, "b   a", term.Coordinates{X: 5}},
		{"across a line break from below", "alpha\nbeta", term.Coordinates{Y: 1}, true, "beta\nalpha", term.Coordinates{Y: 1, X: 5}},
		{"across a line break from the first line end", "alpha\nbeta", term.Coordinates{Y: 0, X: 5}, true, "beta\nalpha", term.Coordinates{Y: 1, X: 5}},
		{"digit words", "1 2", term.Coordinates{X: 1}, true, "2 1", term.Coordinates{X: 3}},
		{"wide-rune words", "世界 foo", term.Coordinates{X: 2}, true, "foo 世界", term.Coordinates{X: 6}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, buf := newEmacsHandler(t, tc.content)
			if tc.start != (term.Coordinates{}) {
				require.True(t, h.SetCursorAtScroll(tc.start))
			}
			var handled bool
			require.NotPanics(t, func() { _, handled = h.Handle(alt('t')) })
			assert.Equal(t, tc.handled, handled)
			assert.Equal(t, tc.want, buf.String())
			assert.Equal(t, tc.at, h.CursorAtScroll())
		})
	}

	t.Run("repeated M-t drags a word forward", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "a b c")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 1}))
		require.True(t, runEvent(h, alt('t')))
		assert.Equal(t, "b a c", buf.String())
		assert.Equal(t, term.Coordinates{X: 3}, h.CursorAtScroll())
		require.True(t, runEvent(h, alt('t')))
		assert.Equal(t, "b c a", buf.String())
		assert.Equal(t, term.Coordinates{X: 5}, h.CursorAtScroll())
	})
}

func TestEmacsWordKills(t *testing.T) {
	cases := []struct {
		name    string
		content string
		start   term.Coordinates
		event   term.Event
		handled bool
		want    string
		at      term.Coordinates
		killed  string
	}{
		{"M-d kills the first word", "alpha beta", term.Coordinates{}, alt('d'), true, " beta", term.Coordinates{}, "alpha"},
		{"M-d from inside a word kills its tail", "alpha beta", term.Coordinates{X: 2}, alt('d'), true, "al beta", term.Coordinates{X: 2}, "pha"},
		{"M-d from the gap kills separator and next word", "alpha beta", term.Coordinates{X: 5}, alt('d'), true, "alpha", term.Coordinates{X: 5}, " beta"},
		{"M-d at end of line kills across the break", "alpha\nbeta x", term.Coordinates{X: 5}, alt('d'), true, "alpha x", term.Coordinates{X: 5}, "\nbeta"},
		{"M-d before punctuation kills only the word", "foo.bar", term.Coordinates{}, alt('d'), true, ".bar", term.Coordinates{}, "foo"},
		{"M-d on punctuation kills through the next word", "foo.bar", term.Coordinates{X: 3}, alt('d'), true, "foo", term.Coordinates{X: 3}, ".bar"},
		{"M-d with only spaces ahead kills them", "ab  ", term.Coordinates{X: 2}, alt('d'), true, "ab", term.Coordinates{X: 2}, "  "},
		{"M-d at buffer end is a no-op", "ab", term.Coordinates{X: 2}, alt('d'), false, "ab", term.Coordinates{X: 2}, ""},
		{"M-d on empty buffer is a no-op", "", term.Coordinates{}, alt('d'), false, "", term.Coordinates{}, ""},
		{"M-Delete matches M-d", "alpha beta", term.Coordinates{}, key2(term.KeyDelete, term.ModAlt), true, " beta", term.Coordinates{}, "alpha"},

		{"M-DEL kills the previous word", "alpha beta", term.Coordinates{X: 10}, key2(term.KeyBackspace, term.ModAlt), true, "alpha ", term.Coordinates{X: 6}, "beta"},
		{"M-DEL from inside a word kills its head", "alpha beta", term.Coordinates{X: 8}, key2(term.KeyBackspace, term.ModAlt), true, "alpha ta", term.Coordinates{X: 6}, "be"},
		{"M-DEL from the word start kills the previous word and gap", "alpha beta", term.Coordinates{X: 6}, key2(term.KeyBackspace, term.ModAlt), true, "beta", term.Coordinates{}, "alpha "},
		{"M-DEL at start of line kills across the break", "alpha x\nbeta", term.Coordinates{Y: 1}, key2(term.KeyBackspace, term.ModAlt), true, "alpha beta", term.Coordinates{X: 6}, "x\n"},
		{"M-DEL with only spaces behind kills to buffer start", "ab  ", term.Coordinates{X: 4}, key2(term.KeyBackspace, term.ModAlt), true, "", term.Coordinates{}, "ab  "},
		{"M-DEL at buffer start is a no-op", "alpha", term.Coordinates{}, key2(term.KeyBackspace, term.ModAlt), false, "alpha", term.Coordinates{}, ""},
		{"M-DEL on empty buffer is a no-op", "", term.Coordinates{}, key2(term.KeyBackspace, term.ModAlt), false, "", term.Coordinates{}, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, buf, clip := newEmacsHandlerWithClipboard(t, tc.content)
			if tc.start != (term.Coordinates{}) {
				require.True(t, h.SetCursorAtScroll(tc.start))
			}
			var handled bool
			require.NotPanics(t, func() { _, handled = h.Handle(tc.event) })
			assert.Equal(t, tc.handled, handled)
			assert.Equal(t, tc.want, buf.String())
			assert.Equal(t, tc.at, h.CursorAtScroll())
			assert.Equal(t, tc.killed, killRingText(clip), "kill ring")
		})
	}
}

func TestEmacsCaseCommands(t *testing.T) {
	cases := []struct {
		name    string
		content string
		start   term.Coordinates
		event   term.Event
		handled bool
		want    string
		at      term.Coordinates
	}{
		{"M-u upcases the word at point", "alpha beta", term.Coordinates{}, alt('u'), true, "ALPHA beta", term.Coordinates{X: 5}},
		{"M-u from inside a word upcases the tail", "alpha beta", term.Coordinates{X: 2}, alt('u'), true, "alPHA beta", term.Coordinates{X: 5}},
		{"M-u from the gap upcases the next word", "alpha beta", term.Coordinates{X: 5}, alt('u'), true, "alpha BETA", term.Coordinates{X: 10}},
		{"M-u across a line break", "alpha\nbeta", term.Coordinates{X: 5}, alt('u'), true, "alpha\nBETA", term.Coordinates{Y: 1, X: 4}},
		{"M-u at buffer end is a no-op", "alpha", term.Coordinates{X: 5}, alt('u'), false, "alpha", term.Coordinates{X: 5}},
		{"M-u on empty buffer is a no-op", "", term.Coordinates{}, alt('u'), false, "", term.Coordinates{}},
		{"M-u on unicode word", "ñoño x", term.Coordinates{}, alt('u'), true, "ÑOÑO x", term.Coordinates{X: 4}},

		{"M-l downcases the word at point", "ALPHA BETA", term.Coordinates{}, alt('l'), true, "alpha BETA", term.Coordinates{X: 5}},
		{"M-l from inside a word downcases the tail", "ALPHA BETA", term.Coordinates{X: 2}, alt('l'), true, "ALpha BETA", term.Coordinates{X: 5}},
		{"M-l from the gap downcases the next word", "ALPHA BETA", term.Coordinates{X: 5}, alt('l'), true, "ALPHA beta", term.Coordinates{X: 10}},

		{"M-c capitalizes the word at point", "alpha beta", term.Coordinates{}, alt('c'), true, "Alpha beta", term.Coordinates{X: 5}},
		{"M-c lowercases the tail", "aLPHA beta", term.Coordinates{}, alt('c'), true, "Alpha beta", term.Coordinates{X: 5}},
		{"M-c from inside a word capitalizes at point", "alpha beta", term.Coordinates{X: 2}, alt('c'), true, "alPha beta", term.Coordinates{X: 5}},
		{"M-c from the gap capitalizes the next word", "alpha BETA", term.Coordinates{X: 5}, alt('c'), true, "alpha Beta", term.Coordinates{X: 10}},
		{"M-c on a digit-led word only lowercases the tail", "123ABC x", term.Coordinates{}, alt('c'), true, "123abc x", term.Coordinates{X: 6}},
		{"M-c at buffer end is a no-op", "alpha", term.Coordinates{X: 5}, alt('c'), false, "alpha", term.Coordinates{X: 5}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, buf := newEmacsHandler(t, tc.content)
			if tc.start != (term.Coordinates{}) {
				require.True(t, h.SetCursorAtScroll(tc.start))
			}
			var handled bool
			require.NotPanics(t, func() { _, handled = h.Handle(tc.event) })
			assert.Equal(t, tc.handled, handled)
			assert.Equal(t, tc.want, buf.String())
			assert.Equal(t, tc.at, h.CursorAtScroll())
		})
	}

	t.Run("repeated M-u upcases successive words", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "ab cd")
		require.True(t, runEvent(h, alt('u')))
		assert.Equal(t, "AB cd", buf.String())
		assert.Equal(t, term.Coordinates{X: 2}, h.CursorAtScroll())
		require.True(t, runEvent(h, alt('u')))
		assert.Equal(t, "AB CD", buf.String())
		assert.Equal(t, term.Coordinates{X: 5}, h.CursorAtScroll())
	})
}

func TestEmacsKillLine(t *testing.T) {
	cases := []struct {
		name    string
		content string
		start   term.Coordinates
		handled bool
		want    string
		at      term.Coordinates
		killed  string
	}{
		{"kill from line start", "abc\ndef", term.Coordinates{}, true, "\ndef", term.Coordinates{}, "abc"},
		{"kill from mid line", "hello world", term.Coordinates{X: 5}, true, "hello", term.Coordinates{X: 5}, " world"},
		{"kill at end of line takes the newline", "ab\ncd", term.Coordinates{X: 2}, true, "abcd", term.Coordinates{X: 2}, "\n"},
		{"kill on empty line removes it", "\nb", term.Coordinates{}, true, "b", term.Coordinates{}, "\n"},
		{"kill on empty interior line", "a\n\nb", term.Coordinates{Y: 1}, true, "a\nb", term.Coordinates{Y: 1}, "\n"},
		{"kill whitespace-only tail", "ab  \ncd", term.Coordinates{X: 2}, true, "ab\ncd", term.Coordinates{X: 2}, "  "},
		{"kill at buffer end is a no-op", "abc", term.Coordinates{X: 3}, false, "abc", term.Coordinates{X: 3}, ""},
		{"kill on last empty line is a no-op", "a\n", term.Coordinates{Y: 1}, false, "a\n", term.Coordinates{Y: 1}, ""},
		{"kill on empty buffer is a no-op", "", term.Coordinates{}, false, "", term.Coordinates{}, ""},
		{"kill over wide runes", "世界世\nx", term.Coordinates{X: 1}, true, "世\nx", term.Coordinates{X: 1}, "界世"},
		{"kill over null cells", "a\x00b\nc", term.Coordinates{X: 1}, true, "a\nc", term.Coordinates{X: 1}, "\x00b"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, buf, clip := newEmacsHandlerWithClipboard(t, tc.content)
			if tc.start != (term.Coordinates{}) {
				require.True(t, h.SetCursorAtScroll(tc.start))
			}
			var handled bool
			require.NotPanics(t, func() { _, handled = h.Handle(ctrl('k')) })
			assert.Equal(t, tc.handled, handled)
			assert.Equal(t, tc.want, buf.String())
			assert.Equal(t, tc.at, h.CursorAtScroll())
			assert.Equal(t, tc.killed, killRingText(clip), "kill ring")
		})
	}
}

func TestEmacsKillRingAccumulation(t *testing.T) {
	t.Run("C-k C-k kills text then newline", func(t *testing.T) {
		h, buf, clip := newEmacsHandlerWithClipboard(t, "abc\ndef")
		require.True(t, runEvent(h, ctrl('k')))
		require.True(t, runEvent(h, ctrl('k')))
		assert.Equal(t, "def", buf.String())
		assert.Equal(t, "abc\n", killRingText(clip))

		// C-y restores the killed line whole, leaving point after it.
		require.True(t, runEvent(h, ctrl('y')))
		assert.Equal(t, "abc\ndef", buf.String())
		assert.Equal(t, term.Coordinates{Y: 1}, h.CursorAtScroll())
	})

	t.Run("C-k across three lines accumulates them all", func(t *testing.T) {
		h, buf, clip := newEmacsHandlerWithClipboard(t, "a\nb\nc")
		for range 4 {
			require.True(t, runEvent(h, ctrl('k')))
		}
		assert.Equal(t, "c", buf.String())
		assert.Equal(t, "a\nb\n", killRingText(clip))
		require.True(t, runEvent(h, ctrl('y')))
		assert.Equal(t, "a\nb\nc", buf.String())
		assert.Equal(t, term.Coordinates{Y: 2}, h.CursorAtScroll())
	})

	t.Run("M-d M-d appends the second word", func(t *testing.T) {
		h, buf, clip := newEmacsHandlerWithClipboard(t, "one two three")
		require.True(t, runEvent(h, alt('d')))
		require.True(t, runEvent(h, alt('d')))
		assert.Equal(t, " three", buf.String())
		assert.Equal(t, "one two", killRingText(clip))
	})

	t.Run("M-DEL M-DEL prepends the earlier word", func(t *testing.T) {
		h, buf, clip := newEmacsHandlerWithClipboard(t, "one two")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 7}))
		require.True(t, runEvent(h, key2(term.KeyBackspace, term.ModAlt)))
		require.True(t, runEvent(h, key2(term.KeyBackspace, term.ModAlt)))
		assert.Equal(t, "", buf.String())
		assert.Equal(t, "one two", killRingText(clip))
	})

	t.Run("forward kill then backward kill prepends", func(t *testing.T) {
		h, buf, clip := newEmacsHandlerWithClipboard(t, "xy ab cd")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 3}))
		require.True(t, runEvent(h, alt('d')))
		assert.Equal(t, "xy  cd", buf.String())
		require.True(t, runEvent(h, key2(term.KeyBackspace, term.ModAlt)))
		assert.Equal(t, " cd", buf.String())
		assert.Equal(t, "xy ab", killRingText(clip))
	})

	t.Run("a motion between kills breaks the chain", func(t *testing.T) {
		h, buf, clip := newEmacsHandlerWithClipboard(t, "ab cd\nef")
		require.True(t, runEvent(h, alt('d')))
		assert.Equal(t, "ab", killRingText(clip))
		require.True(t, runEvent(h, key(term.KeyArrowRight)))
		require.True(t, runEvent(h, alt('d')))
		assert.Equal(t, "cd", killRingText(clip), "chain must restart after motion")
		assert.Equal(t, " \nef", buf.String())
	})

	t.Run("an insertion between kills breaks the chain", func(t *testing.T) {
		h, _, clip := newEmacsHandlerWithClipboard(t, "ab cd ef")
		require.True(t, runEvent(h, alt('d')))
		require.True(t, runEvent(h, term.Event{Type: term.EventKey, Ch: 'z'}))
		require.True(t, runEvent(h, alt('d')))
		assert.Equal(t, " cd", killRingText(clip))
	})

	t.Run("C-w region kill chains with C-k", func(t *testing.T) {
		h, buf, clip := newEmacsHandlerWithClipboard(t, "ab cd\nef")
		// Mark at start, move past "ab", kill region, then kill the rest.
		require.True(t, runEvent(h, key2(term.KeySpace, term.ModCtrl)))
		require.True(t, runEvent(h, alt('f')))
		require.True(t, runEvent(h, ctrl('w')))
		assert.Equal(t, " cd\nef", buf.String())
		require.True(t, runEvent(h, ctrl('k')))
		assert.Equal(t, "ab cd", killRingText(clip))
		assert.Equal(t, "\nef", buf.String())
	})

	t.Run("C-M-k kill-sexp lands on the kill ring", func(t *testing.T) {
		h, buf, clip := newEmacsHandlerWithClipboard(t, "x(a b)y")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 1}))
		require.True(t, runEvent(h, ctrlAlt('k')))
		assert.Equal(t, "xy", buf.String())
		assert.Equal(t, "(a b)", killRingText(clip))
		require.True(t, runEvent(h, ctrl('y')))
		assert.Equal(t, "x(a b)y", buf.String())
		assert.Equal(t, term.Coordinates{X: 6}, h.CursorAtScroll())
	})

	t.Run("C-S-DEL kills the whole line to the ring", func(t *testing.T) {
		h, buf, clip := newEmacsHandlerWithClipboard(t, "ab\ncd\nef")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{Y: 1, X: 1}))
		require.True(t, runEvent(h, key2(term.KeyBackspace, term.ModCtrlShift)))
		assert.Equal(t, "ab\nef", buf.String())
		assert.Equal(t, "cd\n", killRingText(clip))
		// The kill leaves point at the start of the following line; yank
		// re-inserts the line text literally there.
		require.Equal(t, term.Coordinates{Y: 1}, h.CursorAtScroll())
		require.True(t, runEvent(h, ctrl('y')))
		assert.Equal(t, "ab\ncd\nef", buf.String())
		assert.Equal(t, term.Coordinates{Y: 2}, h.CursorAtScroll())
	})

	t.Run("kill ring works without a mark or prior copy", func(t *testing.T) {
		h, buf, clip := newEmacsHandlerWithClipboard(t, "hello")
		require.True(t, runEvent(h, ctrl('k')))
		assert.Equal(t, "", buf.String())
		assert.Equal(t, "hello", killRingText(clip))
		require.True(t, runEvent(h, ctrl('y')))
		assert.Equal(t, "hello", buf.String())
		assert.Equal(t, term.Coordinates{X: 5}, h.CursorAtScroll())
	})
}

func TestEmacsTransposeChars(t *testing.T) {
	cases := []struct {
		name    string
		content string
		start   term.Coordinates
		handled bool
		want    string
		at      term.Coordinates
	}{
		{"swap around point", "abc", term.Coordinates{X: 1}, true, "bac", term.Coordinates{X: 2}},
		{"at end of line swaps the trailing pair", "ab", term.Coordinates{X: 2}, true, "ba", term.Coordinates{X: 2}},
		{"at buffer start is a no-op", "ab", term.Coordinates{}, false, "ab", term.Coordinates{}},
		{"at start of first line with more lines is a no-op", "ab\ncd", term.Coordinates{}, false, "ab\ncd", term.Coordinates{}},
		{"at start of a later line drags the first char up", "ab\ncd", term.Coordinates{Y: 1}, true, "abc\nd", term.Coordinates{Y: 1}},
		{"on an empty line pulls the previous char down", "ab\n\ncd", term.Coordinates{Y: 1}, true, "a\nb\ncd", term.Coordinates{Y: 1, X: 1}},
		{"on an empty line below an empty line is a no-op", "\n\nx", term.Coordinates{Y: 1}, false, "\n\nx", term.Coordinates{Y: 1}},
		{"after a lone character pushes it up", "ab\nc", term.Coordinates{Y: 1, X: 1}, true, "abc\n", term.Coordinates{Y: 1}},
		{"single character buffer is a no-op", "a", term.Coordinates{X: 1}, false, "a", term.Coordinates{X: 1}},
		{"empty buffer is a no-op", "", term.Coordinates{}, false, "", term.Coordinates{}},
		{"wide runes swap as single cells", "世界", term.Coordinates{X: 1}, true, "界世", term.Coordinates{X: 2}},
		{"null cell participates in the swap", "a\x00b", term.Coordinates{X: 1}, true, "\x00ab", term.Coordinates{X: 2}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, buf := newEmacsHandler(t, tc.content)
			if tc.start != (term.Coordinates{}) {
				require.True(t, h.SetCursorAtScroll(tc.start))
			}
			var handled bool
			require.NotPanics(t, func() { _, handled = h.Handle(ctrl('t')) })
			assert.Equal(t, tc.handled, handled)
			assert.Equal(t, tc.want, buf.String())
			assert.Equal(t, tc.at, h.CursorAtScroll())
		})
	}

	t.Run("repeated C-t drags a character forward", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "abc")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 1}))
		require.True(t, runEvent(h, ctrl('t')))
		assert.Equal(t, "bac", buf.String())
		assert.Equal(t, term.Coordinates{X: 2}, h.CursorAtScroll())
		require.True(t, runEvent(h, ctrl('t')))
		assert.Equal(t, "bca", buf.String())
		assert.Equal(t, term.Coordinates{X: 3}, h.CursorAtScroll())
	})
}

func TestEmacsDeleteIndentation(t *testing.T) {
	cases := []struct {
		name    string
		content string
		start   term.Coordinates
		handled bool
		want    string
		at      term.Coordinates
	}{
		{"joins onto the previous line with one space", "foo\nbar", term.Coordinates{Y: 1}, true, "foo bar", term.Coordinates{X: 3}},
		{"column does not matter", "foo\nbar", term.Coordinates{Y: 1, X: 2}, true, "foo bar", term.Coordinates{X: 3}},
		{"deletes the indentation of the joined line", "foo\n    bar", term.Coordinates{Y: 1, X: 4}, true, "foo bar", term.Coordinates{X: 3}},
		{"deletes tab indentation", "foo\n\tbar", term.Coordinates{Y: 1}, true, "foo bar", term.Coordinates{X: 3}},
		{"collapses trailing spaces of the previous line", "foo  \nbar", term.Coordinates{Y: 1}, true, "foo bar", term.Coordinates{X: 3}},
		{"collapses whitespace on both sides", "foo  \n  bar", term.Coordinates{Y: 1}, true, "foo bar", term.Coordinates{X: 3}},
		{"no space when the previous line is empty", "x\n\nbar", term.Coordinates{Y: 2}, true, "x\nbar", term.Coordinates{Y: 1}},
		{"no space when the joined line is empty", "foo\n", term.Coordinates{Y: 1}, true, "foo", term.Coordinates{X: 3}},
		{"no space when the joined line is blank", "foo\n   ", term.Coordinates{Y: 1}, true, "foo", term.Coordinates{X: 3}},
		{"no space before a closing bracket", "f(x\n) y", term.Coordinates{Y: 1}, true, "f(x) y", term.Coordinates{X: 3}},
		{"no space after an opening bracket", "f(\nx)", term.Coordinates{Y: 1}, true, "f(x)", term.Coordinates{X: 2}},
		{"on the first line is a no-op", "a\nb", term.Coordinates{}, false, "a\nb", term.Coordinates{}},
		{"on a single line is a no-op", "abc", term.Coordinates{X: 1}, false, "abc", term.Coordinates{X: 1}},
		{"on an empty buffer is a no-op", "", term.Coordinates{}, false, "", term.Coordinates{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, buf := newEmacsHandler(t, tc.content)
			if tc.start != (term.Coordinates{}) {
				require.True(t, h.SetCursorAtScroll(tc.start))
			}
			var handled bool
			require.NotPanics(t, func() { _, handled = h.Handle(alt('^')) })
			assert.Equal(t, tc.handled, handled)
			assert.Equal(t, tc.want, buf.String())
			assert.Equal(t, tc.at, h.CursorAtScroll())
		})
	}

	t.Run("repeated M-^ folds a paragraph upward", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "a\nb\nc")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{Y: 2}))
		require.True(t, runEvent(h, alt('^')))
		assert.Equal(t, "a\nb c", buf.String())
		assert.Equal(t, term.Coordinates{Y: 1, X: 1}, h.CursorAtScroll())
		require.True(t, runEvent(h, alt('^')))
		assert.Equal(t, "a b c", buf.String())
		assert.Equal(t, term.Coordinates{X: 1}, h.CursorAtScroll())
	})
}

func TestEmacsJustOneSpace(t *testing.T) {
	cases := []struct {
		name    string
		content string
		start   term.Coordinates
		want    string
		at      term.Coordinates
	}{
		{"collapses interior spaces", "a   b", term.Coordinates{X: 2}, "a b", term.Coordinates{X: 2}},
		{"collapses tabs", "a\t\tb", term.Coordinates{X: 1}, "a b", term.Coordinates{X: 2}},
		{"collapses null cells", "a\x00\x00b", term.Coordinates{X: 1}, "a b", term.Coordinates{X: 2}},
		{"inserts a space when there is none", "ab", term.Coordinates{X: 1}, "a b", term.Coordinates{X: 2}},
		{"collapses leading spaces at line start", "  ab", term.Coordinates{X: 1}, " ab", term.Coordinates{X: 1}},
		{"collapses trailing spaces at line end", "ab  ", term.Coordinates{X: 3}, "ab ", term.Coordinates{X: 3}},
		{"inserts into an empty buffer", "", term.Coordinates{}, " ", term.Coordinates{X: 1}},
		{"only affects the current line", "a  \n  b", term.Coordinates{X: 2}, "a \n  b", term.Coordinates{X: 2}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, buf := newEmacsHandler(t, tc.content)
			if tc.start != (term.Coordinates{}) {
				require.True(t, h.SetCursorAtScroll(tc.start))
			}
			require.True(t, runEvent(h, key2(term.KeySpace, term.ModAlt)))
			assert.Equal(t, tc.want, buf.String())
			assert.Equal(t, tc.at, h.CursorAtScroll())
		})
	}
}

func TestEmacsParagraphMotion(t *testing.T) {
	const content = "p1a\np1b\n\np2a\np2b\n\np3"

	cases := []struct {
		name    string
		content string
		start   term.Coordinates
		event   term.Event
		at      term.Coordinates
	}{
		{"M-} lands on the blank line after the paragraph", content, term.Coordinates{}, alt('}'), term.Coordinates{Y: 2}},
		{"M-} from mid paragraph", content, term.Coordinates{Y: 1, X: 2}, alt('}'), term.Coordinates{Y: 2}},
		{"M-} from a blank line skips to the next boundary", content, term.Coordinates{Y: 2}, alt('}'), term.Coordinates{Y: 5}},
		{"M-} at the last paragraph stops on the last line", content, term.Coordinates{Y: 5}, alt('}'), term.Coordinates{Y: 6}},
		{"M-} resets the column", content, term.Coordinates{Y: 0, X: 2}, alt('}'), term.Coordinates{Y: 2}},
		{"M-{ lands on the blank line before the paragraph", content, term.Coordinates{Y: 6}, alt('{'), term.Coordinates{Y: 5}},
		{"M-{ from mid paragraph", content, term.Coordinates{Y: 4, X: 1}, alt('{'), term.Coordinates{Y: 2}},
		{"M-{ from the first paragraph stops on the first line", content, term.Coordinates{Y: 1}, alt('{'), term.Coordinates{}},
		{"M-} without blank lines reaches the last line", "a\nb\nc", term.Coordinates{}, alt('}'), term.Coordinates{Y: 2}},
		{"M-{ without blank lines reaches the first line", "a\nb\nc", term.Coordinates{Y: 2}, alt('{'), term.Coordinates{}},
		{"M-} collapses a blank run", "a\n\n\n\nb", term.Coordinates{}, alt('}'), term.Coordinates{Y: 1}},
		{"M-} from inside a blank run", "a\n\n\n\nb", term.Coordinates{Y: 2}, alt('}'), term.Coordinates{Y: 4}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := newEmacsHandler(t, tc.content)
			if tc.start != (term.Coordinates{}) {
				require.True(t, h.SetCursorAtScroll(tc.start))
			}
			require.NotPanics(t, func() { h.Handle(tc.event) })
			assert.Equal(t, tc.at, h.CursorAtScroll())
		})
	}

	t.Run("empty buffer is a safe no-op", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "")
		require.NotPanics(t, func() { h.Handle(alt('}')) })
		assert.Equal(t, term.Coordinates{}, h.CursorAtScroll())
		require.NotPanics(t, func() { h.Handle(alt('{')) })
		assert.Equal(t, term.Coordinates{}, h.CursorAtScroll())
	})
}

// pageTestContent returns numbered lines ("l0".."l39") so page-motion
// assertions can pin exact rows.
func pageTestContent(lines int) string {
	var sb strings.Builder
	for i := range lines {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString("l")
		sb.WriteString(strconv.Itoa(i))
	}
	return sb.String()
}

func TestEmacsPageMotion(t *testing.T) {
	const height = 10
	newPaged := func(t *testing.T) text.Handler {
		t.Helper()
		buf := cell.NewBuffer()
		_, err := buf.ReadFrom(strings.NewReader(pageTestContent(40)))
		require.NoError(t, err)
		h := NewHandler(buf, emacsTestURI(t), text.IndentRuneTab, 0)
		h.Resize(80, height)
		return h
	}

	t.Run("C-v pages point forward a full window", func(t *testing.T) {
		h := newPaged(t)
		require.True(t, runEvent(h, ctrl('v')))
		assert.Equal(t, term.Coordinates{Y: height}, h.CursorAtScroll())
		assert.Equal(t, height, h.SeekOffset())
		require.True(t, runEvent(h, ctrl('v')))
		assert.Equal(t, term.Coordinates{Y: 2 * height}, h.CursorAtScroll())
	})

	t.Run("C-v keeps point on the same window row", func(t *testing.T) {
		// GNU C-v scrolls the window a full page and point keeps its
		// position within the window, so a point on window row 5 lands a
		// page further down, column preserved.
		h := newPaged(t)
		require.True(t, h.SetCursorAtScroll(term.Coordinates{Y: 5, X: 1}))
		require.True(t, runEvent(h, ctrl('v')))
		assert.Equal(t, term.Coordinates{Y: 5 + height, X: 1}, h.CursorAtScroll())
	})

	t.Run("C-v at the bottom of the buffer is a no-op", func(t *testing.T) {
		h := newPaged(t)
		// Placing point on the last line scrolls the view to the bottom,
		// so there is no further page to scroll into.
		require.True(t, h.SetCursorAtScroll(term.Coordinates{Y: 39}))
		require.False(t, runEvent(h, ctrl('v')))
		assert.Equal(t, term.Coordinates{Y: 39}, h.CursorAtScroll())
	})

	t.Run("M-v pages the view back up", func(t *testing.T) {
		h := newPaged(t)
		require.True(t, runEvent(h, ctrl('v')))
		require.Equal(t, height, h.SeekOffset())
		require.True(t, runEvent(h, alt('v')))
		assert.Equal(t, 0, h.SeekOffset())
	})

	t.Run("M-v at the top of the buffer is a no-op", func(t *testing.T) {
		h := newPaged(t)
		require.False(t, runEvent(h, alt('v')))
		assert.Equal(t, term.Coordinates{}, h.CursorAtScroll())
		assert.Equal(t, 0, h.SeekOffset())
	})

	t.Run("PgDn moves point a window of lines and repositions", func(t *testing.T) {
		h := newPaged(t)
		require.True(t, runEvent(h, key(term.KeyPgdn)))
		assert.Equal(t, term.Coordinates{Y: height}, h.CursorAtScroll())
	})

	t.Run("PgUp moves point back a window of lines", func(t *testing.T) {
		h := newPaged(t)
		require.True(t, h.SetCursorAtScroll(term.Coordinates{Y: 2 * height}))
		require.True(t, runEvent(h, key(term.KeyPgup)))
		assert.Equal(t, term.Coordinates{Y: height}, h.CursorAtScroll())
	})

	t.Run("PgDn clamps at the last line", func(t *testing.T) {
		h := newPaged(t)
		require.True(t, h.SetCursorAtScroll(term.Coordinates{Y: 35}))
		require.True(t, runEvent(h, key(term.KeyPgdn)))
		assert.Equal(t, term.Coordinates{Y: 39}, h.CursorAtScroll())
	})

	t.Run("C-l recenters the view around point", func(t *testing.T) {
		h := newPaged(t)
		require.True(t, h.SetCursorAtScroll(term.Coordinates{Y: 20}))
		before := h.SeekOffset()
		require.True(t, runEvent(h, ctrl('l')))
		assert.Equal(t, term.Coordinates{Y: 20}, h.CursorAtScroll(), "point must not move")
		assert.NotEqual(t, before, h.SeekOffset())
		// Point sits on the center row of the window.
		center := h.SeekOffset() + height/2
		assert.InDelta(t, 20, center, 1)
	})

	t.Run("C-M-Down scrolls one line keeping a visible point", func(t *testing.T) {
		h := newPaged(t)
		require.True(t, h.SetCursorAtScroll(term.Coordinates{Y: 5, X: 1}))
		require.True(t, runEvent(h, key2(term.KeyArrowDown, term.ModCtrlAlt)))
		assert.Equal(t, 1, h.SeekOffset())
		assert.Equal(t, term.Coordinates{Y: 5, X: 1}, h.CursorAtScroll())
	})

	t.Run("C-M-Up scrolls back keeping a visible point", func(t *testing.T) {
		h := newPaged(t)
		require.True(t, h.SetCursorAtScroll(term.Coordinates{Y: 5, X: 1}))
		require.True(t, runEvent(h, key2(term.KeyArrowDown, term.ModCtrlAlt)))
		require.True(t, runEvent(h, key2(term.KeyArrowUp, term.ModCtrlAlt)))
		assert.Equal(t, 0, h.SeekOffset())
		assert.Equal(t, term.Coordinates{Y: 5, X: 1}, h.CursorAtScroll())
	})

	t.Run("C-M-Down drags point when it would leave the view", func(t *testing.T) {
		h := newPaged(t)
		require.True(t, runEvent(h, key2(term.KeyArrowDown, term.ModCtrlAlt)))
		assert.Equal(t, 1, h.SeekOffset())
		assert.Equal(t, term.Coordinates{Y: 1}, h.CursorAtScroll())
	})

	t.Run("paging on an empty buffer is a safe no-op", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "")
		for _, ev := range []term.Event{ctrl('v'), alt('v'), key(term.KeyPgdn), key(term.KeyPgup), ctrl('l')} {
			require.NotPanics(t, func() { h.Handle(ev) })
			assert.Equal(t, term.Coordinates{}, h.CursorAtScroll())
		}
	})
}

// feedKeys parses a key-sequence string and drives it through the handler.
func feedKeys(t *testing.T, h text.Handler, keys string) {
	t.Helper()
	seq, err := term.ParseKeys(keys)
	require.NoError(t, err)
	for _, k := range seq {
		h.Handle(term.Event{Type: term.EventKey, Key: k.Key, Mod: k.Mod, Ch: k.Ch})
	}
}

func TestEmacsIsearchEdgeCases(t *testing.T) {
	t.Run("repeat C-s wraps past the last match", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "foo x foo")
		feedKeys(t, h, "<ctrl-s>foo")
		assert.Equal(t, term.Coordinates{X: 3}, h.CursorAtScroll())
		feedKeys(t, h, "<ctrl-s>")
		assert.Equal(t, term.Coordinates{X: 9}, h.CursorAtScroll())
		feedKeys(t, h, "<ctrl-s>")
		assert.Equal(t, term.Coordinates{X: 3}, h.CursorAtScroll(), "search wraps to the first match")
	})

	t.Run("repeat C-r wraps past the first match", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "foo x foo")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 9}))
		feedKeys(t, h, "<ctrl-r>foo")
		assert.Equal(t, term.Coordinates{X: 6}, h.CursorAtScroll())
		feedKeys(t, h, "<ctrl-r>")
		assert.Equal(t, term.Coordinates{X: 0}, h.CursorAtScroll())
		feedKeys(t, h, "<ctrl-r>")
		assert.Equal(t, term.Coordinates{X: 6}, h.CursorAtScroll(), "search wraps to the last match")
	})

	t.Run("C-r during a forward search steps back a match", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "aa bb aa")
		feedKeys(t, h, "<ctrl-s>aa<ctrl-s>")
		assert.Equal(t, term.Coordinates{X: 8}, h.CursorAtScroll())
		feedKeys(t, h, "<ctrl-r>")
		// The direction flips, so point lands at the previous match start.
		assert.Equal(t, term.Coordinates{X: 0}, h.CursorAtScroll())
		assert.True(t, h.IsSearchMode())
	})

	t.Run("backspacing the whole query returns to the origin", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "foo bar")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 1}))
		feedKeys(t, h, "<ctrl-s>bar")
		assert.Equal(t, term.Coordinates{X: 7}, h.CursorAtScroll())
		feedKeys(t, h, "<backspace><backspace><backspace>")
		assert.True(t, h.IsSearchMode(), "empty query keeps the search active")
		assert.Equal(t, term.Coordinates{X: 1}, h.CursorAtScroll())
		// A further backspace on the empty query is a safe no-op.
		feedKeys(t, h, "<backspace>")
		assert.True(t, h.IsSearchMode())
		assert.Equal(t, term.Coordinates{X: 1}, h.CursorAtScroll())
	})

	t.Run("C-s with an empty query keeps point at the origin", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "foo bar")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 2}))
		feedKeys(t, h, "<ctrl-s><ctrl-s>")
		assert.True(t, h.IsSearchMode())
		assert.Equal(t, term.Coordinates{X: 2}, h.CursorAtScroll())
	})

	t.Run("the search is case sensitive", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "Foo foo")
		feedKeys(t, h, "<ctrl-s>foo")
		assert.Equal(t, term.Coordinates{X: 7}, h.CursorAtScroll(), "only the lowercase occurrence matches")
	})

	t.Run("a query may contain spaces", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "ab a b")
		feedKeys(t, h, "<ctrl-s>a<space>b")
		assert.Equal(t, term.Coordinates{X: 6}, h.CursorAtScroll())
	})

	t.Run("Esc accepts the search at the current match", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "foo bar")
		feedKeys(t, h, "<ctrl-s>bar<esc>")
		assert.False(t, h.IsSearchMode())
		assert.Equal(t, term.Coordinates{X: 7}, h.CursorAtScroll())
	})

	t.Run("a Meta chord ends the search and is re-handled", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "foo bar baz")
		feedKeys(t, h, "<ctrl-s>foo")
		assert.Equal(t, term.Coordinates{X: 3}, h.CursorAtScroll())
		feedKeys(t, h, "<alt-f>")
		assert.False(t, h.IsSearchMode())
		// M-f runs after the search exits: point crosses to the next word end.
		assert.Equal(t, term.Coordinates{X: 7}, h.CursorAtScroll())
	})

	t.Run("typing while failing keeps point at the origin", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "abc")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 1}))
		feedKeys(t, h, "<ctrl-s>zz")
		assert.Equal(t, term.Coordinates{X: 1}, h.CursorAtScroll())
		feedKeys(t, h, "z")
		assert.True(t, h.IsSearchMode())
		assert.Equal(t, term.Coordinates{X: 1}, h.CursorAtScroll())
	})

	t.Run("C-s C-s resumes the last accepted search", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "foo x foo y foo")
		feedKeys(t, h, "<ctrl-s>foo<enter>")
		require.False(t, h.IsSearchMode())
		require.Equal(t, term.Coordinates{X: 3}, h.CursorAtScroll())
		feedKeys(t, h, "<ctrl-s><ctrl-s>")
		assert.True(t, h.IsSearchMode())
		assert.Equal(t, term.Coordinates{X: 9}, h.CursorAtScroll())
		// A further C-s keeps stepping through the matches.
		feedKeys(t, h, "<ctrl-s>")
		assert.Equal(t, term.Coordinates{X: 15}, h.CursorAtScroll())
	})

	t.Run("C-r C-r resumes the last search backward", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "foo x foo")
		feedKeys(t, h, "<ctrl-s>foo<enter>")
		require.Equal(t, term.Coordinates{X: 3}, h.CursorAtScroll())
		feedKeys(t, h, "<ctrl-r><ctrl-r>")
		assert.True(t, h.IsSearchMode())
		assert.Equal(t, term.Coordinates{}, h.CursorAtScroll())
	})

	t.Run("resume with no history keeps the empty prompt", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "abc")
		feedKeys(t, h, "<ctrl-s><ctrl-s>")
		assert.True(t, h.IsSearchMode())
		assert.Equal(t, term.Coordinates{}, h.CursorAtScroll())
	})

	t.Run("an aborted search does not enter the resume history", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "foo bar")
		feedKeys(t, h, "<ctrl-s>foo<ctrl-g>")
		require.Equal(t, term.Coordinates{}, h.CursorAtScroll())
		feedKeys(t, h, "<ctrl-s><ctrl-s>")
		assert.True(t, h.IsSearchMode())
		assert.Equal(t, term.Coordinates{}, h.CursorAtScroll(),
			"nothing to resume after C-g")
	})

	t.Run("resuming a query with no matches left fails in place", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "foo")
		feedKeys(t, h, "<ctrl-s>foo<enter>")
		require.Equal(t, term.Coordinates{X: 3}, h.CursorAtScroll())
		for range 3 {
			require.True(t, runEvent(h, key(term.KeyBackspace)))
		}
		require.Equal(t, "", buf.String())
		feedKeys(t, h, "<ctrl-s><ctrl-s>")
		assert.True(t, h.IsSearchMode())
		assert.Equal(t, term.Coordinates{}, h.CursorAtScroll())
		assert.Equal(t, "", buf.String())
	})

	t.Run("isearch on an empty buffer stays put", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "")
		feedKeys(t, h, "<ctrl-s>x")
		assert.True(t, h.IsSearchMode())
		assert.Equal(t, term.Coordinates{}, h.CursorAtScroll())
		feedKeys(t, h, "<ctrl-g>")
		assert.False(t, h.IsSearchMode())
	})
}

func TestEmacsQueryReplaceEdgeCases(t *testing.T) {
	// M-% is Alt+Shift+5, which reaches the handler as ModAlt with '%'.
	const start = "<alt-shift-5>"

	t.Run("no match anywhere ends the loop immediately", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "abc")
		feedKeys(t, h, start+"zz<enter>y<enter>")
		assert.False(t, h.IsSearchMode())
		assert.Equal(t, "abc", buf.String())
		assert.Equal(t, term.Coordinates{}, h.CursorAtScroll())
	})

	t.Run("Space replaces like y", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "a a")
		feedKeys(t, h, start+"a<enter>b<enter>")
		feedKeys(t, h, "<space>")
		assert.Equal(t, "b a", buf.String())
		feedKeys(t, h, "<space>")
		assert.Equal(t, "b b", buf.String())
		assert.False(t, h.IsSearchMode())
	})

	t.Run("Backspace skips like n", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "a a")
		feedKeys(t, h, start+"a<enter>b<enter>")
		feedKeys(t, h, "<backspace>")
		assert.Equal(t, "a a", buf.String())
		assert.True(t, h.IsSearchMode())
		feedKeys(t, h, "y")
		assert.Equal(t, "a b", buf.String())
	})

	t.Run("n on the last match ends the loop", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "a a")
		feedKeys(t, h, start+"a<enter>b<enter>")
		feedKeys(t, h, "n")
		require.True(t, h.IsSearchMode())
		feedKeys(t, h, "n")
		assert.False(t, h.IsSearchMode())
		assert.Equal(t, "a a", buf.String())
	})

	t.Run("Enter ends the loop without replacing", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "a a")
		feedKeys(t, h, start+"a<enter>b<enter>")
		feedKeys(t, h, "<enter>")
		assert.False(t, h.IsSearchMode())
		assert.Equal(t, "a a", buf.String())
	})

	t.Run("Esc ends the loop without replacing", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "a a")
		feedKeys(t, h, start+"a<enter>b<enter>")
		feedKeys(t, h, "<esc>")
		assert.False(t, h.IsSearchMode())
		assert.Equal(t, "a a", buf.String())
	})

	t.Run("empty replacement deletes matches", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "a b a")
		feedKeys(t, h, start+"a<enter><enter>")
		feedKeys(t, h, "!")
		assert.Equal(t, " b ", buf.String())
	})

	t.Run("longer replacement shifts later matches correctly", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "x x x")
		feedKeys(t, h, start+"x<enter>long<enter>")
		feedKeys(t, h, "!")
		assert.Equal(t, "long long long", buf.String())
	})

	t.Run("adjacent matches are all replaced", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "aaaa")
		feedKeys(t, h, start+"aa<enter>b<enter>")
		feedKeys(t, h, "!")
		assert.Equal(t, "bb", buf.String())
	})

	t.Run("matches across lines are replaced", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "x\ny x")
		feedKeys(t, h, start+"x<enter>z<enter>")
		feedKeys(t, h, "!")
		assert.Equal(t, "z\ny z", buf.String())
	})

	t.Run("point rests after the last replacement", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "foo foo")
		feedKeys(t, h, start+"foo<enter>bar<enter>")
		feedKeys(t, h, "!")
		assert.Equal(t, "bar bar", buf.String())
		assert.Equal(t, term.Coordinates{X: 7}, h.CursorAtScroll())
	})

	t.Run("dot replaces the current match and stops there", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "a a a")
		feedKeys(t, h, start+"a<enter>b<enter>")
		feedKeys(t, h, "y")
		require.Equal(t, "b a a", buf.String())
		feedKeys(t, h, ".")
		assert.False(t, h.IsSearchMode())
		assert.Equal(t, "b b a", buf.String())
		assert.Equal(t, term.Coordinates{X: 3}, h.CursorAtScroll())
	})

	t.Run("the search prompt supports backspace editing", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "ac ab")
		feedKeys(t, h, start+"ab<backspace>c<enter>x<enter>")
		feedKeys(t, h, "!")
		assert.Equal(t, "x ab", buf.String())
	})

	t.Run("the search string may contain spaces", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "a b c")
		feedKeys(t, h, start+"a<space>b<enter>z<enter>")
		feedKeys(t, h, "!")
		assert.Equal(t, "z c", buf.String())
	})

	t.Run("C-g at the replacement prompt cancels", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "a a")
		feedKeys(t, h, start+"a<enter>b<ctrl-g>")
		assert.False(t, h.IsSearchMode())
		assert.Equal(t, "a a", buf.String())
	})

	t.Run("keys during the loop do not leak into the buffer", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "a a")
		feedKeys(t, h, start+"a<enter>b<enter>")
		// An unbound decision key is swallowed by the loop.
		feedKeys(t, h, "zq")
		assert.False(t, h.IsSearchMode())
		assert.Equal(t, "a a", buf.String())
	})

	t.Run("query-replace on an empty buffer ends cleanly", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "")
		feedKeys(t, h, start+"a<enter>b<enter>")
		assert.False(t, h.IsSearchMode())
		assert.Equal(t, "", buf.String())
	})
}

func TestEmacsGotoLineEdgeCases(t *testing.T) {
	const content = "l1\nl2\nl3\nl4\nl5"

	cases := []struct {
		name string
		keys string
		at   term.Coordinates
	}{
		{"M-g M-g jumps like M-g g", "<alt-g><alt-g>4<enter>", term.Coordinates{Y: 3}},
		{"line zero is ignored", "<alt-g>g0<enter>", term.Coordinates{Y: 1, X: 1}},
		{"negative line is ignored", "<alt-g>g-2<enter>", term.Coordinates{Y: 1, X: 1}},
		{"garbage input is ignored", "<alt-g>gxyz<enter>", term.Coordinates{Y: 1, X: 1}},
		{"mixed digits and letters are ignored", "<alt-g>g2x<enter>", term.Coordinates{Y: 1, X: 1}},
		{"surrounding spaces are trimmed", "<alt-g>g<space>3<space><enter>", term.Coordinates{Y: 2}},
		{"backspace edits the line number", "<alt-g>g25<backspace><enter>", term.Coordinates{Y: 1}},
		{"backspace on empty input is safe", "<alt-g>g<backspace><backspace>4<enter>", term.Coordinates{Y: 3}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, buf := newEmacsHandler(t, content)
			require.True(t, h.SetCursorAtScroll(term.Coordinates{Y: 1, X: 1}))
			feedKeys(t, h, tc.keys)
			assert.False(t, h.IsSearchMode(), "prompt must be closed")
			assert.Equal(t, tc.at, h.CursorAtScroll())
			assert.Equal(t, content, buf.String(), "goto-line must not edit the buffer")
		})
	}
}

func TestEmacsMinibufferKeys(t *testing.T) {
	const content = "l1\nl2\nl3"

	t.Run("typed keys do not reach the buffer", func(t *testing.T) {
		h, buf := newEmacsHandler(t, content)
		feedKeys(t, h, "<alt-g>g123")
		assert.True(t, h.IsSearchMode())
		assert.Equal(t, content, buf.String())
		assert.Equal(t, term.Coordinates{}, h.CursorAtScroll())
	})

	t.Run("control chords are ignored while prompting", func(t *testing.T) {
		h, buf := newEmacsHandler(t, content)
		feedKeys(t, h, "<alt-g>g2<ctrl-a><ctrl-e><ctrl-k><enter>")
		// C-a / C-e / C-k neither edit the buffer nor abort the prompt;
		// the pending "2" still commits.
		assert.Equal(t, content, buf.String())
		assert.Equal(t, term.Coordinates{Y: 1}, h.CursorAtScroll())
	})

	t.Run("Esc cancels the prompt like C-g", func(t *testing.T) {
		h, _ := newEmacsHandler(t, content)
		require.True(t, h.SetCursorAtScroll(term.Coordinates{Y: 2, X: 1}))
		feedKeys(t, h, "<alt-g>g3<esc>")
		assert.False(t, h.IsSearchMode())
		assert.Equal(t, term.Coordinates{Y: 2, X: 1}, h.CursorAtScroll())
	})
}

func TestEmacsPrefixAbort(t *testing.T) {
	cases := []struct {
		name string
		keys string
	}{
		{"unbound letter aborts", "<alt-g>x"},
		{"C-g aborts", "<alt-g><ctrl-g>"},
		{"motion key is swallowed", "<alt-g><right>"},
		{"another prefix key is swallowed", "<alt-g><alt-g>x<esc>"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, buf := newEmacsHandler(t, "abc")
			require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 1}))
			feedKeys(t, h, tc.keys)
			assert.False(t, h.IsSearchMode())
			assert.Equal(t, "abc", buf.String(), "aborted prefix must not edit")
			assert.Equal(t, term.Coordinates{X: 1}, h.CursorAtScroll(), "aborted prefix must not move point")
		})
	}
}

func TestEmacsRegionCommands(t *testing.T) {
	setMark := key2(term.KeySpace, term.ModCtrl)

	t.Run("Selection reports the mark-to-point region", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "alpha beta")
		require.True(t, runEvent(h, setMark))
		require.True(t, runEvent(h, alt('f')))
		sel, ok := h.Selection()
		require.True(t, ok)
		assert.Equal(t, "alpha", sel)
		// Reading the region must not edit the buffer, move point or
		// leave a selection behind.
		assert.Equal(t, "alpha beta", buf.String())
		assert.Equal(t, term.Coordinates{X: 5}, h.CursorAtScroll())
		_, _, active := h.(*emacsHandler).cursor.SelectionBounds()
		assert.False(t, active)
	})

	t.Run("Selection reports a backward region", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "alpha beta")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 5}))
		require.True(t, runEvent(h, setMark))
		require.True(t, runEvent(h, ctrl('a')))
		sel, ok := h.Selection()
		require.True(t, ok)
		assert.Equal(t, "alpha", sel)
	})

	t.Run("Selection reports a multiline region", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "ab\ncd")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 1}))
		require.True(t, runEvent(h, setMark))
		require.True(t, h.SetCursorAtScroll(term.Coordinates{Y: 1, X: 1}))
		sel, ok := h.Selection()
		require.True(t, ok)
		assert.Equal(t, "b\nc", sel)
	})

	t.Run("Selection is empty with point at the mark", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "abc")
		require.True(t, runEvent(h, setMark))
		_, ok := h.Selection()
		assert.False(t, ok)
	})

	t.Run("Selection is empty after the mark is popped", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "abcdef")
		require.True(t, runEvent(h, setMark))
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 2}))
		require.True(t, runEvent(h, alt('w')))
		// C-g reports unhandled when there is no selection to clear, but
		// it still discards the pending mark.
		require.NotPanics(t, func() { h.Handle(ctrl('g')) })
		_, ok := h.Selection()
		assert.False(t, ok)
	})

	t.Run("C-w without a mark is a no-op", func(t *testing.T) {
		h, buf, clip := newEmacsHandlerWithClipboard(t, "abc")
		require.False(t, runEvent(h, ctrl('w')))
		assert.Equal(t, "abc", buf.String())
		assert.Equal(t, "", killRingText(clip))
	})

	t.Run("C-w kills mark-to-point onto the kill ring", func(t *testing.T) {
		h, buf, clip := newEmacsHandlerWithClipboard(t, "alpha beta")
		require.True(t, runEvent(h, setMark))
		require.True(t, runEvent(h, alt('f')))
		require.True(t, runEvent(h, ctrl('w')))
		assert.Equal(t, " beta", buf.String())
		assert.Equal(t, term.Coordinates{}, h.CursorAtScroll())
		assert.Equal(t, "alpha", killRingText(clip))
	})

	t.Run("C-w with point before the mark kills backward", func(t *testing.T) {
		h, buf, clip := newEmacsHandlerWithClipboard(t, "alpha beta")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 5}))
		require.True(t, runEvent(h, setMark))
		require.True(t, runEvent(h, ctrl('a')))
		require.True(t, runEvent(h, ctrl('w')))
		assert.Equal(t, " beta", buf.String())
		assert.Equal(t, term.Coordinates{}, h.CursorAtScroll())
		assert.Equal(t, "alpha", killRingText(clip))
	})

	t.Run("C-w kills a multiline region exactly", func(t *testing.T) {
		h, buf, clip := newEmacsHandlerWithClipboard(t, "ab\ncd")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 1}))
		require.True(t, runEvent(h, setMark))
		require.True(t, h.SetCursorAtScroll(term.Coordinates{Y: 1, X: 1}))
		require.True(t, runEvent(h, ctrl('w')))
		assert.Equal(t, "ad", buf.String())
		assert.Equal(t, term.Coordinates{X: 1}, h.CursorAtScroll())
		assert.Equal(t, "b\nc", killRingText(clip))
	})

	t.Run("C-w with point at the mark is a no-op", func(t *testing.T) {
		h, buf, _ := newEmacsHandlerWithClipboard(t, "abc")
		require.True(t, runEvent(h, setMark))
		require.False(t, runEvent(h, ctrl('w')))
		assert.Equal(t, "abc", buf.String())
	})

	t.Run("C-w pops the mark it used", func(t *testing.T) {
		h, buf, _ := newEmacsHandlerWithClipboard(t, "abcdef")
		require.True(t, runEvent(h, setMark))
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 2}))
		require.True(t, runEvent(h, ctrl('w')))
		require.Equal(t, "cdef", buf.String())
		require.Empty(t, emacsMarks(h))
		// Without a fresh mark a second C-w has nothing to kill.
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 2}))
		assert.False(t, runEvent(h, ctrl('w')))
		assert.Equal(t, "cdef", buf.String())
	})

	t.Run("C-g clears the mark so C-w cannot kill", func(t *testing.T) {
		h, buf, _ := newEmacsHandlerWithClipboard(t, "abc")
		require.True(t, runEvent(h, setMark))
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 2}))
		// C-g reports unhandled when there is no selection to clear, but
		// it still discards the pending mark.
		require.NotPanics(t, func() { h.Handle(ctrl('g')) })
		require.Empty(t, emacsMarks(h))
		assert.False(t, runEvent(h, ctrl('w')))
		assert.Equal(t, "abc", buf.String())
	})

	t.Run("M-w copies without editing or moving point", func(t *testing.T) {
		h, buf, clip := newEmacsHandlerWithClipboard(t, "alpha beta")
		require.True(t, runEvent(h, setMark))
		require.True(t, runEvent(h, alt('f')))
		require.True(t, runEvent(h, alt('w')))
		assert.Equal(t, "alpha beta", buf.String())
		assert.Equal(t, term.Coordinates{X: 5}, h.CursorAtScroll())
		assert.Equal(t, "alpha", killRingText(clip))
	})

	t.Run("M-w without a mark is a no-op", func(t *testing.T) {
		h, _, clip := newEmacsHandlerWithClipboard(t, "abc")
		assert.False(t, runEvent(h, alt('w')))
		assert.Equal(t, "", killRingText(clip))
	})

	t.Run("M-w then C-y duplicates the region", func(t *testing.T) {
		h, buf, _ := newEmacsHandlerWithClipboard(t, "ab\ncd")
		require.True(t, runEvent(h, setMark))
		require.True(t, h.SetCursorAtScroll(term.Coordinates{Y: 1}))
		require.True(t, runEvent(h, alt('w')))
		require.True(t, h.SetCursorAtScroll(term.Coordinates{Y: 1, X: 2}))
		require.True(t, runEvent(h, ctrl('y')))
		assert.Equal(t, "ab\ncdab\n", buf.String())
		assert.Equal(t, term.Coordinates{Y: 2}, h.CursorAtScroll())
	})
}

func TestEmacsYankCommands(t *testing.T) {
	copyData := func(clip clipboard.Register, text_ string, meta any) {
		_ = clip.Copy(clipboard.DefaultRegisterID, clipboard.Data{Text: text_, Metadata: meta})
	}

	t.Run("C-y inserts at point with point after the text", func(t *testing.T) {
		h, buf, clip := newEmacsHandlerWithClipboard(t, "xy")
		copyData(clip, "AB", text.StandardSelection)
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 1}))
		require.True(t, runEvent(h, ctrl('y')))
		assert.Equal(t, "xABy", buf.String())
		assert.Equal(t, term.Coordinates{X: 3}, h.CursorAtScroll())
	})

	t.Run("C-y inserts multiline text literally", func(t *testing.T) {
		h, buf, clip := newEmacsHandlerWithClipboard(t, "xy")
		copyData(clip, "a\nb", text.StandardSelection)
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 1}))
		require.True(t, runEvent(h, ctrl('y')))
		assert.Equal(t, "xa\nby", buf.String())
		assert.Equal(t, term.Coordinates{Y: 1, X: 1}, h.CursorAtScroll())
	})

	t.Run("C-y of a killed line inserts before the current line", func(t *testing.T) {
		h, buf, clip := newEmacsHandlerWithClipboard(t, "xy")
		copyData(clip, "abc\n", text.StandardSelection)
		require.True(t, runEvent(h, ctrl('y')))
		assert.Equal(t, "abc\nxy", buf.String())
		assert.Equal(t, term.Coordinates{Y: 1}, h.CursorAtScroll())
	})

	t.Run("C-y replaces an active selection", func(t *testing.T) {
		h, buf, clip := newEmacsHandlerWithClipboard(t, "abcde")
		copyData(clip, "Z", text.StandardSelection)
		require.True(t, runEvent(h, key2(term.KeyArrowRight, term.ModShift)))
		require.True(t, runEvent(h, key2(term.KeyArrowRight, term.ModShift)))
		require.True(t, runEvent(h, ctrl('y')))
		assert.Equal(t, "Zcde", buf.String())
		assert.Equal(t, term.Coordinates{X: 1}, h.CursorAtScroll())
	})

	t.Run("C-y with an empty clipboard is a no-op", func(t *testing.T) {
		h, buf, _ := newEmacsHandlerWithClipboard(t, "abc")
		require.NotPanics(t, func() { h.Handle(ctrl('y')) })
		assert.Equal(t, "abc", buf.String())
		assert.Equal(t, term.Coordinates{}, h.CursorAtScroll())
	})

	t.Run("M-w copies a shift-selection for a later yank", func(t *testing.T) {
		h, buf, clip := newEmacsHandlerWithClipboard(t, "abc")
		require.True(t, runEvent(h, key2(term.KeyArrowRight, term.ModShift)))
		require.True(t, runEvent(h, key2(term.KeyArrowRight, term.ModShift)))
		require.True(t, runEvent(h, alt('w')))
		assert.Equal(t, "ab", killRingText(clip))
		require.True(t, runEvent(h, key(term.KeyEnd)))
		require.True(t, runEvent(h, ctrl('y')))
		assert.Equal(t, "abcab", buf.String())
		assert.Equal(t, term.Coordinates{X: 5}, h.CursorAtScroll())
	})

	t.Run("M-w without a selection or mark leaves the clipboard alone", func(t *testing.T) {
		h, buf, clip := newEmacsHandlerWithClipboard(t, "abc")
		require.NotPanics(t, func() { h.Handle(alt('w')) })
		assert.Equal(t, "abc", buf.String())
		assert.Equal(t, "", killRingText(clip))
	})

	t.Run("C-c is unbound: GNU reserves it as the mode prefix", func(t *testing.T) {
		h, buf, clip := newEmacsHandlerWithClipboard(t, "abc")
		require.True(t, runEvent(h, key2(term.KeyArrowRight, term.ModShift)))
		assert.False(t, runEvent(h, ctrl('c')))
		assert.Equal(t, "abc", buf.String())
		assert.Equal(t, "", killRingText(clip))
	})

	t.Run("M-y after a motion is not handled", func(t *testing.T) {
		h, buf, reg := newEmacsHandlerWithHistory(t, "z")
		for _, entry := range []string{"a", "b"} {
			require.NoError(t, reg.Copy(clipboard.DefaultRegisterID,
				clipboard.Data{Text: entry, Metadata: text.StandardSelection}))
		}
		require.True(t, runEvent(h, key(term.KeyEnd)))
		require.True(t, runEvent(h, ctrl('y')))
		require.Equal(t, "zb", buf.String())
		// The motion breaks the yank chain, so M-y must refuse.
		require.True(t, runEvent(h, key(term.KeyArrowLeft)))
		assert.False(t, runEvent(h, alt('y')))
		assert.Equal(t, "zb", buf.String())
	})
}

func TestEmacsLineInsertCommands(t *testing.T) {
	cases := []struct {
		name    string
		content string
		start   term.Coordinates
		event   term.Event
		want    string
		at      term.Coordinates
	}{
		{"C-o splits the line keeping point", "ab", term.Coordinates{X: 1}, ctrl('o'), "a\nb", term.Coordinates{X: 1}},
		{"C-o keeps indentation with point", "  ab", term.Coordinates{X: 2}, ctrl('o'), "  \nab", term.Coordinates{X: 2}},
		{"C-o at end of line opens below", "ab", term.Coordinates{X: 2}, ctrl('o'), "ab\n", term.Coordinates{X: 2}},
		{"C-o on an empty buffer", "", term.Coordinates{}, ctrl('o'), "\n", term.Coordinates{}},
		{"C-j inserts a plain newline", "ab", term.Coordinates{X: 1}, ctrl('j'), "a\nb", term.Coordinates{Y: 1}},
		{"C-j does not inherit indentation", "\tab", term.Coordinates{X: 3}, ctrl('j'), "\tab\n", term.Coordinates{Y: 1}},
		{"RET inserts a plain newline", "ab", term.Coordinates{X: 1}, key(term.KeyEnter), "a\nb", term.Coordinates{Y: 1}},
		{"RET at buffer end", "ab", term.Coordinates{X: 2}, key(term.KeyEnter), "ab\n", term.Coordinates{Y: 1}},
		{"RET on empty buffer", "", term.Coordinates{}, key(term.KeyEnter), "\n", term.Coordinates{Y: 1}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, buf := newEmacsHandler(t, tc.content)
			if tc.start != (term.Coordinates{}) {
				require.True(t, h.SetCursorAtScroll(tc.start))
			}
			require.True(t, runEvent(h, tc.event))
			assert.Equal(t, tc.want, buf.String())
			assert.Equal(t, tc.at, h.CursorAtScroll())
		})
	}

	t.Run("RET replaces an active selection", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "abcde")
		require.True(t, runEvent(h, key2(term.KeyArrowRight, term.ModShift)))
		require.True(t, runEvent(h, key2(term.KeyArrowRight, term.ModShift)))
		require.True(t, runEvent(h, key(term.KeyEnter)))
		assert.Equal(t, "\ncde", buf.String())
		assert.Equal(t, term.Coordinates{Y: 1}, h.CursorAtScroll())
	})

	t.Run("C-Enter opens a line below from mid line", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "ab\ncd")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 1}))
		require.True(t, runEvent(h, key2(term.KeyEnter, term.ModCtrl)))
		assert.Equal(t, "ab\n\ncd", buf.String())
		assert.Equal(t, term.Coordinates{Y: 1}, h.CursorAtScroll())
	})

	t.Run("C-S-Enter opens a line above from mid line", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "ab\ncd")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{Y: 1, X: 1}))
		require.True(t, runEvent(h, key2(term.KeyEnter, term.ModCtrlShift)))
		assert.Equal(t, "ab\n\ncd", buf.String())
		assert.Equal(t, term.Coordinates{Y: 1}, h.CursorAtScroll())
	})
}

func TestEmacsCommentToggle(t *testing.T) {
	// Uncommenting requires the comment-coverage view on top of the
	// configured comment spec, mirroring how the editor wires syntax
	// services in production.
	newCommentHandler := func(t *testing.T, content string) (text.Handler, *cell.Buffer) {
		t.Helper()
		buf := cell.NewBuffer()
		_, err := buf.ReadFrom(strings.NewReader(content))
		require.NoError(t, err)
		buf.WithView(testCommentService{
			view: buf.View(),
			line: []string{"//"},
		})
		h := NewHandler(buf, emacsTestURI(t), text.IndentRuneTab, 0,
			WithComments(text.CommentConfig{
				"go": {Line: []string{"//"}, Block: []text.CommentBlock{{Start: "/*", End: "*/"}}},
			}))
		h.Resize(80, 20)
		return h, buf
	}

	cases := []struct {
		name    string
		content string
		start   term.Coordinates
		want    string
	}{
		{"comments the current line", "x := 1\ny := 2", term.Coordinates{}, "// x := 1\ny := 2"},
		{"uncomments a commented line", "// x := 1\ny := 2", term.Coordinates{}, "x := 1\ny := 2"},
		{"keeps indentation", "\tx := 1", term.Coordinates{X: 1}, "\t// x := 1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, buf := newCommentHandler(t, tc.content)
			if tc.start != (term.Coordinates{}) {
				require.True(t, h.SetCursorAtScroll(tc.start))
			}
			require.True(t, runEvent(h, alt(';')))
			assert.Equal(t, tc.want, buf.String())
		})
	}

	t.Run("round trip preserves the original line", func(t *testing.T) {
		h, buf := newCommentHandler(t, "x := 1")
		require.True(t, runEvent(h, alt(';')))
		require.Equal(t, "// x := 1", buf.String())
		require.True(t, runEvent(h, alt(';')))
		assert.Equal(t, "x := 1", buf.String())
	})

	t.Run("without comment configuration M-; is a no-op", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "x := 1")
		assert.False(t, runEvent(h, alt(';')))
		assert.Equal(t, "x := 1", buf.String())
	})
}

func TestEmacsFillParagraph(t *testing.T) {
	cases := []struct {
		name    string
		content string
		start   term.Coordinates
		handled bool
		want    string
	}{
		{"wraps a long line at the ruler", "aaa bbb ccc ddd", term.Coordinates{}, true, "aaa bbb\nccc ddd"},
		{"rejoins short lines", "aaa\nbbb", term.Coordinates{}, true, "aaa bbb"},
		{"only fills the paragraph around point", "aaa bbb ccc\n\nzz", term.Coordinates{}, true, "aaa bbb\nccc\n\nzz"},
		{"second paragraph is filled independently", "zz\n\naaa bbb ccc", term.Coordinates{Y: 2}, true, "zz\n\naaa bbb\nccc"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, buf := newEmacsHandler(t, tc.content, WithRuler(10))
			if tc.start != (term.Coordinates{}) {
				require.True(t, h.SetCursorAtScroll(tc.start))
			}
			var handled bool
			require.NotPanics(t, func() { _, handled = h.Handle(alt('q')) })
			assert.Equal(t, tc.handled, handled)
			assert.Equal(t, tc.want, buf.String())
		})
	}

	t.Run("empty buffer is a safe no-op", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "", WithRuler(10))
		require.NotPanics(t, func() { h.Handle(alt('q')) })
		assert.Equal(t, "", buf.String())
	})
}

func TestEmacsUndoInputSurface(t *testing.T) {
	tests := []struct {
		name    string
		content string
		at      term.Coordinates
		arrange []term.Event
		edit    []term.Event
		after   string
	}{
		{name: "ASCII insertion", content: "ab", at: term.Coordinates{X: 1}, edit: []term.Event{char('X')}, after: "aXb"},
		{name: "wide-rune insertion", content: "界界", at: term.Coordinates{X: 1}, edit: []term.Event{char('世')}, after: "界世界"},
		{name: "emoji insertion", content: "ab", at: term.Coordinates{X: 1}, edit: []term.Event{char('🚀')}, after: "a🚀b"},
		{name: "combining-mark insertion", content: "ab", at: term.Coordinates{X: 1}, edit: []term.Event{char('\u0301')}, after: "a\u0301b"},
		{name: "normal TAB", content: "ab", at: term.Coordinates{X: 2}, edit: []term.Event{key(term.KeyTab)}, after: "ab\t"},
		{name: "quoted TAB", content: "ab", at: term.Coordinates{X: 1}, edit: []term.Event{ctrl('q'), key(term.KeyTab)}, after: "a\tb"},
		{name: "quoted NUL", content: "a\x00b", at: term.Coordinates{X: 1}, edit: []term.Event{ctrl('q'), ctrl('@')}, after: "a\x00\x00b"},
		{name: "newline", content: "ab", at: term.Coordinates{X: 1}, edit: []term.Event{key(term.KeyEnter)}, after: "a\nb"},
		{name: "backspace over TAB", content: "a\tb", at: term.Coordinates{X: 2}, edit: []term.Event{key(term.KeyBackspace)}, after: "ab"},
		{name: "backspace over wide rune", content: "a界b", at: term.Coordinates{X: 2}, edit: []term.Event{ctrl('h')}, after: "ab"},
		{name: "delete NUL", content: "a\x00b", at: term.Coordinates{X: 1}, edit: []term.Event{key(term.KeyDelete)}, after: "ab"},
		{name: "delete emoji", content: "a🚀b", at: term.Coordinates{X: 1}, edit: []term.Event{ctrl('d')}, after: "ab"},
		{name: "kill mixed-width line", content: "a\t\x00界\nd", at: term.Coordinates{X: 1}, edit: []term.Event{ctrl('k')}, after: "a\nd"},
		{name: "transpose wide runes", content: "世界", at: term.Coordinates{X: 1}, edit: []term.Event{ctrl('t')}, after: "界世"},
		{
			name: "replace mixed selection", content: "a\t\x00界z",
			arrange: []term.Event{
				key2(term.KeyArrowRight, term.ModShift),
				key2(term.KeyArrowRight, term.ModShift),
				key2(term.KeyArrowRight, term.ModShift),
				key2(term.KeyArrowRight, term.ModShift),
			},
			edit: []term.Event{char('世')}, after: "世z",
		},
		{
			name: "paste TAB and wide rune", content: "ab", at: term.Coordinates{X: 1},
			edit: []term.Event{
				{Type: term.EventPasteStart},
				char('\t'),
				char('界'),
				{Type: term.EventPasteEnd},
			},
			after: "a\t界b",
		},
	}
	undoBindings := []struct {
		name string
		ev   term.Event
	}{
		{name: "C-slash", ev: ctrl('/')},
		{name: "C-underscore", ev: ctrl('_')},
	}
	breakers := []term.Event{key(term.KeyF5), ctrl('z'), ctrl('l')}

	for _, tc := range tests {
		for i, undo := range undoBindings {
			t.Run(tc.name+"/"+undo.name, func(t *testing.T) {
				h, buf, _ := newEmacsHandlerWithClipboard(t, tc.content)
				if tc.at != (term.Coordinates{}) {
					require.True(t, h.SetCursorAtScroll(tc.at))
				}
				for _, ev := range tc.arrange {
					require.True(t, runEvent(h, ev), "arrange event %#v", ev)
				}
				for _, ev := range tc.edit {
					require.True(t, runEvent(h, ev), "edit event %#v", ev)
				}
				require.Equal(t, tc.after, buf.String(), "after edit")

				require.True(t, runEvent(h, undo.ev), "undo")
				require.Equal(t, tc.content, buf.String(), "after undo")
				for _, ev := range breakers {
					h.Handle(ev)
				}
				redo := undoBindings[(i+1)%len(undoBindings)].ev
				require.True(t, runEvent(h, redo), "ordinary undo after breakers")
				assert.Equal(t, tc.after, buf.String(), "after GNU-style redo")
			})
		}
	}
}

func TestEmacsUndoTimelines(t *testing.T) {
	t.Run("ordinary undo and redo timelines", func(t *testing.T) {
		tests := []struct {
			name        string
			undoBefore  int
			beforeBreak string
			breakers    []term.Event
			want        []string
		}{
			{
				name:        "repeated undo stays backward",
				undoBefore:  1,
				beforeBreak: "a",
				want:        []string{""},
			},
			{
				name:        "motions switch repeated undo to redo",
				undoBefore:  2,
				beforeBreak: "",
				breakers: []term.Event{
					ctrl('f'), ctrl('b'), key(term.KeyHome), key(term.KeyEnd),
				},
				want: []string{"a", "ba"},
			},
			{
				name:        "boundary no-ops still switch to redo",
				undoBefore:  2,
				beforeBreak: "",
				breakers: []term.Event{
					ctrl('b'), key(term.KeyArrowLeft), ctrl('p'), key(term.KeyF5), ctrl('z'),
				},
				want: []string{"a", "ba"},
			},
			{
				name:        "mouse input switches to redo",
				undoBefore:  2,
				beforeBreak: "",
				breakers: []term.Event{
					{Type: term.EventMouse, Key: term.MouseWheelDown, MouseX: 0, MouseY: 0},
				},
				want: []string{"a", "ba"},
			},
			{
				name:        "empty paste switches to redo",
				undoBefore:  2,
				beforeBreak: "",
				breakers: []term.Event{
					{Type: term.EventPasteStart},
					{Type: term.EventPasteEnd},
				},
				want: []string{"a", "ba"},
			},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				h, buf := newEmacsHandler(t, "")
				require.True(t, runEvent(h, char('a')))
				require.True(t, runEvent(h, ctrl('b')))
				require.True(t, runEvent(h, char('b')))
				require.Equal(t, "ba", buf.String())
				for range tc.undoBefore {
					require.True(t, runEvent(h, ctrl('/')))
				}
				require.Equal(t, tc.beforeBreak, buf.String())
				for _, ev := range tc.breakers {
					h.Handle(ev)
				}
				for i, want := range tc.want {
					require.True(t, runEvent(h, []term.Event{ctrl('_'), ctrl('/')}[i]))
					assert.Equal(t, want, buf.String(), "step %d", i)
				}
			})
		}
	})

	t.Run("an unbroken sequence rolls past the redos into undoing again", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "")
		require.True(t, runEvent(h, char('a')))
		require.True(t, runEvent(h, ctrl('b')))
		require.True(t, runEvent(h, char('b')))
		require.True(t, runEvent(h, ctrl('/')))
		require.True(t, runEvent(h, ctrl('_')))
		require.Equal(t, "", buf.String())
		h.Handle(ctrl('f'))
		for i, want := range []string{"a", "ba", "a", ""} {
			require.True(t, runEvent(h, []term.Event{ctrl('/'), ctrl('_')}[i%2]), "step %d", i)
			assert.Equal(t, want, buf.String(), "step %d", i)
		}
	})

	t.Run("runtime events are not editing commands", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			ev   term.Event
		}{
			{name: "resize", ev: term.Event{Type: term.EventResize, Width: 80, Height: 20}},
			{name: "focus", ev: term.Event{Type: term.EventFocus}},
			{name: "unfocus", ev: term.Event{Type: term.EventUnfocus}},
			{name: "interrupt", ev: term.Event{Type: term.EventInterrupt}},
			{name: "stray paste end", ev: term.Event{Type: term.EventPasteEnd}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				h, buf := newEmacsHandler(t, "")
				require.True(t, runEvent(h, char('a')))
				require.True(t, runEvent(h, ctrl('b')))
				require.True(t, runEvent(h, char('b')))
				require.True(t, runEvent(h, ctrl('/')))
				require.Equal(t, "a", buf.String())
				_, handled := h.Handle(tc.ev)
				assert.False(t, handled)
				require.True(t, runEvent(h, ctrl('_')))
				assert.Equal(t, "", buf.String())
			})
		}
	})

	t.Run("empty paste over an empty selection creates no undo entry", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "")
		require.True(t, runEvent(h, char('a')))
		require.True(t, runEvent(h, ctrl('b')))
		require.True(t, runEvent(h, char('b')))
		require.True(t, runEvent(h, ctrl('/')))
		require.True(t, runEvent(h, ctrl('_')))
		require.Equal(t, "", buf.String())
		assert.False(t, runEvent(h, key2(term.KeyArrowRight, term.ModShift)))
		require.True(t, runEvent(h, term.Event{Type: term.EventPasteStart}))
		require.True(t, runEvent(h, term.Event{Type: term.EventPasteEnd}))
		require.True(t, runEvent(h, ctrl('/')))
		assert.Equal(t, "a", buf.String())
	})

	t.Run("explicit redo bindings start a fresh undo sequence", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			ev   term.Event
		}{
			{name: "C-question", ev: ctrl('?')},
			{name: "C-M-slash", ev: ctrlAlt('/')},
			{name: "C-M-underscore", ev: ctrlAlt('_')},
		} {
			t.Run(tc.name, func(t *testing.T) {
				h, buf := newEmacsHandler(t, "ab")
				require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 2}))
				require.True(t, runEvent(h, char('X')))
				require.True(t, runEvent(h, ctrl('/')))
				require.True(t, runEvent(h, tc.ev))
				require.Equal(t, "abX", buf.String())
				require.True(t, runEvent(h, ctrl('_')))
				assert.Equal(t, "ab", buf.String())
			})
		}
	})

	t.Run("empty timelines are no-ops", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			ev   term.Event
		}{
			{name: "ordinary undo", ev: ctrl('/')},
			{name: "alternate undo", ev: ctrl('_')},
			{name: "explicit redo", ev: ctrl('?')},
		} {
			t.Run(tc.name, func(t *testing.T) {
				h, buf := newEmacsHandler(t, "abc")
				assert.False(t, runEvent(h, tc.ev))
				assert.Equal(t, "abc", buf.String())
			})
		}
	})
}

func TestEmacsUndoStateMachineBreakers(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		at       term.Coordinates
		breakers []term.Event
	}{
		{name: "incremental search accepted", content: "foo", at: term.Coordinates{X: 3}, breakers: []term.Event{ctrl('s'), char('f'), char('o'), char('o'), key(term.KeyEnter)}},
		{name: "incremental search aborted", content: "foo", at: term.Coordinates{X: 3}, breakers: []term.Event{ctrl('s'), char('f'), ctrl('g')}},
		{name: "goto minibuffer submitted", content: "a\nb", at: term.Coordinates{Y: 1, X: 1}, breakers: []term.Event{alt('g'), char('g'), char('1'), key(term.KeyEnter)}},
		{name: "goto minibuffer cancelled", content: "ab", at: term.Coordinates{X: 2}, breakers: []term.Event{alt('g'), char('g'), ctrl('g')}},
		{name: "query prompt cancelled", content: "foo", at: term.Coordinates{X: 3}, breakers: []term.Event{alt('%'), char('f'), char('o'), ctrl('g')}},
		{name: "query decision quit", content: "foo", breakers: []term.Event{alt('%'), char('f'), char('o'), char('o'), key(term.KeyEnter), char('x'), key(term.KeyEnter), char('q')}},
		{name: "goto prefix invalid key", content: "ab", at: term.Coordinates{X: 2}, breakers: []term.Event{alt('g'), char('x')}},
		{name: "zap target not found", content: "ab", at: term.Coordinates{X: 2}, breakers: []term.Event{alt('z'), char('x')}},
		{name: "quoted key without literal form", content: "ab", at: term.Coordinates{X: 2}, breakers: []term.Event{ctrl('q'), key(term.KeyArrowRight)}},
		{name: "counted motion", content: "abcd", at: term.Coordinates{X: 4}, breakers: []term.Event{ctrl('u'), char('2'), ctrl('b')}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, buf := newEmacsHandler(t, tc.content)
			if tc.at != (term.Coordinates{}) {
				require.True(t, h.SetCursorAtScroll(tc.at))
			}
			require.True(t, runEvent(h, char('X')))
			edited := buf.String()
			require.True(t, runEvent(h, ctrl('/')))
			require.Equal(t, tc.content, buf.String())
			for _, ev := range tc.breakers {
				require.True(t, runEvent(h, ev), "breaker %#v", ev)
			}
			require.True(t, runEvent(h, ctrl('_')))
			assert.Equal(t, edited, buf.String())
		})
	}
}

func TestEmacsUndoKeysInTransientStates(t *testing.T) {
	tests := []struct {
		name       string
		content    string
		at         term.Coordinates
		enter      []term.Event
		undoInMode term.Event
		exit       []term.Event
		executes   bool
	}{
		{name: "isearch rehandles undo", content: "foo", at: term.Coordinates{X: 3}, enter: []term.Event{ctrl('s'), char('f')}, undoInMode: ctrl('/'), executes: true},
		{name: "goto minibuffer consumes undo", content: "a\nb", at: term.Coordinates{Y: 1, X: 1}, enter: []term.Event{alt('g'), char('g'), char('1')}, undoInMode: ctrl('_'), exit: []term.Event{ctrl('g')}},
		{name: "query search minibuffer consumes undo", content: "foo", at: term.Coordinates{X: 3}, enter: []term.Event{alt('%'), char('f')}, undoInMode: ctrl('/'), exit: []term.Event{ctrl('g')}},
		{name: "query decision consumes undo", content: "foo", enter: []term.Event{alt('%'), char('f'), char('o'), char('o'), key(term.KeyEnter), char('x'), key(term.KeyEnter)}, undoInMode: ctrl('_'), exit: []term.Event{char('q')}},
		{name: "goto prefix consumes undo", content: "ab", at: term.Coordinates{X: 2}, enter: []term.Event{alt('g')}, undoInMode: ctrl('/')},
		{name: "zap prefix consumes undo", content: "ab", at: term.Coordinates{X: 2}, enter: []term.Event{alt('z')}, undoInMode: ctrl('_')},
		{name: "quoted insert consumes nonliteral undo chord", content: "ab", at: term.Coordinates{X: 2}, enter: []term.Event{ctrl('q')}, undoInMode: ctrl('/')},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, buf := newEmacsHandler(t, tc.content)
			if tc.at != (term.Coordinates{}) {
				require.True(t, h.SetCursorAtScroll(tc.at))
			}
			require.True(t, runEvent(h, char('X')))
			edited := buf.String()
			require.True(t, runEvent(h, ctrl('/')))
			require.Equal(t, tc.content, buf.String())
			for _, ev := range tc.enter {
				require.True(t, runEvent(h, ev), "enter event %#v", ev)
			}
			require.True(t, runEvent(h, tc.undoInMode), "undo key must be consumed")
			if tc.executes {
				require.Equal(t, edited, buf.String(), "active state must re-dispatch undo")
			} else {
				require.Equal(t, tc.content, buf.String(), "active state must own undo")
			}
			for _, ev := range tc.exit {
				require.True(t, runEvent(h, ev), "exit event %#v", ev)
			}
			if !tc.executes {
				require.True(t, runEvent(h, ctrl('/')), "next ordinary undo must redo")
			}
			assert.Equal(t, edited, buf.String())
		})
	}
}

func TestEmacsCountedUndo(t *testing.T) {
	tests := []struct {
		name          string
		before        []term.Event
		count         []term.Event
		afterEachUndo []string
	}{
		{
			name:          "counted undo walks backward",
			count:         []term.Event{ctrl('u'), char('2'), ctrl('/')},
			afterEachUndo: []string{""},
		},
		{
			name:          "prefix collection does not break an undo run",
			before:        []term.Event{ctrl('/')},
			count:         []term.Event{ctrl('u'), char('1'), ctrl('_')},
			afterEachUndo: []string{""},
		},
		{
			name:          "counted undo becomes counted redo after a breaker",
			before:        []term.Event{ctrl('/'), ctrl('_'), key(term.KeyF5)},
			count:         []term.Event{ctrl('u'), char('2'), ctrl('/')},
			afterEachUndo: []string{"ba"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, buf := newEmacsHandler(t, "")
			require.True(t, runEvent(h, char('a')))
			require.True(t, runEvent(h, ctrl('b')))
			require.True(t, runEvent(h, char('b')))
			require.Equal(t, "ba", buf.String())
			for _, ev := range tc.before {
				h.Handle(ev)
			}
			for _, ev := range tc.count {
				require.True(t, runEvent(h, ev), "counted event %#v", ev)
			}
			assert.Equal(t, tc.afterEachUndo[0], buf.String())
		})
	}

	t.Run("counted redo keeps the redone changes separate", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "")
		require.True(t, runEvent(h, char('a')))
		require.True(t, runEvent(h, ctrl('b')))
		require.True(t, runEvent(h, char('b')))
		require.True(t, runEvent(h, ctrl('/')))
		require.True(t, runEvent(h, ctrl('_')))
		require.Equal(t, "", buf.String())
		h.Handle(key(term.KeyF5))
		for _, ev := range []term.Event{ctrl('u'), char('2'), ctrl('/')} {
			require.True(t, runEvent(h, ev), "counted event %#v", ev)
		}
		require.Equal(t, "ba", buf.String(), "counted redo replays both edits")
		h.Handle(key(term.KeyF5))
		require.True(t, runEvent(h, ctrl('/')))
		assert.Equal(t, "a", buf.String(), "undo after a counted redo reverses one change")
	})
}

func TestEmacsUndoAfterNewEdit(t *testing.T) {
	tests := []struct {
		name  string
		edit  []term.Event
		after string
	}{
		{name: "ASCII insertion", edit: []term.Event{char('b')}, after: "b"},
		{name: "normal TAB", edit: []term.Event{key(term.KeyTab)}, after: "\t"},
		{name: "quoted NUL", edit: []term.Event{ctrl('q'), ctrl('@')}, after: "\x00"},
		{name: "wide paste", edit: []term.Event{{Type: term.EventPasteStart}, char('界'), {Type: term.EventPasteEnd}}, after: "界"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, buf := newEmacsHandler(t, "")
			require.True(t, runEvent(h, char('a')))
			require.True(t, runEvent(h, ctrl('/')))
			for _, ev := range tc.edit {
				require.True(t, runEvent(h, ev), "edit event %#v", ev)
			}
			require.Equal(t, tc.after, buf.String())
			require.True(t, runEvent(h, ctrl('_')))
			assert.Equal(t, "", buf.String(), "undo must target the new edit")
			require.True(t, runEvent(h, ctrl('?')), "the fresh edit can be redone")
			assert.Equal(t, tc.after, buf.String())
			assert.False(t, runEvent(h, ctrl('?')), "the abandoned edit must not remain on the redo timeline")
		})
	}
}

func TestEmacsUndoGrouping(t *testing.T) {
	tests := []struct {
		name          string
		content       string
		at            term.Coordinates
		arrange       []term.Event
		events        []term.Event
		keys          string
		after         string
		afterEachUndo []string
		finalNoOp     bool
	}{
		{name: "delete-indentation", content: "a\n\t\x00界", at: term.Coordinates{Y: 1, X: 2}, events: []term.Event{alt('^')}, after: "a 界", afterEachUndo: []string{"a\n\t\x00界"}},
		{name: "transpose words", content: "alpha 世界", at: term.Coordinates{X: 5}, events: []term.Event{alt('t')}, after: "世界 alpha", afterEachUndo: []string{"alpha 世界"}},
		{name: "transpose across lines", content: "ab\n\n界d", at: term.Coordinates{Y: 1}, events: []term.Event{ctrl('t')}, after: "a\nb\n界d", afterEachUndo: []string{"ab\n\n界d"}},
		{name: "collapse mixed whitespace", content: "a \t\x00 b", at: term.Coordinates{X: 3}, events: []term.Event{key2(term.KeySpace, term.ModAlt)}, after: "a b", afterEachUndo: []string{"a \t\x00 b"}},
		{
			name: "paste over mixed selection", content: "a\t\x00界z",
			arrange: []term.Event{
				key2(term.KeyArrowRight, term.ModShift),
				key2(term.KeyArrowRight, term.ModShift),
				key2(term.KeyArrowRight, term.ModShift),
				key2(term.KeyArrowRight, term.ModShift),
			},
			events: []term.Event{
				{Type: term.EventPasteStart}, char('\t'), char('🚀'), {Type: term.EventPasteEnd},
			},
			after: "\t🚀z", afterEachUndo: []string{"a\t\x00界z"},
		},
		{
			name: "empty paste deletes mixed selection", content: "a\t\x00界z",
			arrange: []term.Event{
				key2(term.KeyArrowRight, term.ModShift),
				key2(term.KeyArrowRight, term.ModShift),
				key2(term.KeyArrowRight, term.ModShift),
				key2(term.KeyArrowRight, term.ModShift),
			},
			events: []term.Event{
				{Type: term.EventPasteStart}, {Type: term.EventPasteEnd},
			},
			after: "z", afterEachUndo: []string{"a\t\x00界z"},
		},
		{name: "interactive query replace", content: "界 a 界 b 界", keys: "<alt-shift-5>界<enter>世<enter>yyy", after: "世 a 世 b 世", afterEachUndo: []string{"界 a 界 b 界"}},
		{name: "replace all", content: "foo a foo b foo", keys: "<alt-shift-5>foo<enter>zap<enter>!", after: "zap a zap b zap", afterEachUndo: []string{"foo a foo b foo"}},
		{name: "quit query replace", content: "foo a foo b foo", keys: "<alt-shift-5>foo<enter>zap<enter>yq", after: "zap a foo b foo", afterEachUndo: []string{"foo a foo b foo"}},
		{name: "abort query replace", content: "foo a foo", keys: "<alt-shift-5>foo<enter>zap<enter>y<ctrl-g>", after: "zap a foo", afterEachUndo: []string{"foo a foo"}},
		{name: "counted quoted TAB", content: "ab", at: term.Coordinates{X: 1}, keys: "<ctrl-u>3<ctrl-q><tab>", after: "a\t\t\tb", afterEachUndo: []string{"ab"}},
		{name: "counted wide insertion", content: "ab", at: term.Coordinates{X: 1}, keys: "<ctrl-u>3界", after: "a界界界b", afterEachUndo: []string{"ab"}},
		{
			name: "typing over selection", content: "a\t\x00界z",
			arrange: []term.Event{
				key2(term.KeyArrowRight, term.ModShift),
				key2(term.KeyArrowRight, term.ModShift),
				key2(term.KeyArrowRight, term.ModShift),
				key2(term.KeyArrowRight, term.ModShift),
			},
			events: []term.Event{char('X'), char('世')}, after: "X世z",
			afterEachUndo: []string{"a\t\x00界z"},
		},
		{name: "consecutive kills stay separate", content: "ab\ncd", events: []term.Event{ctrl('k'), ctrl('k')}, after: "cd", afterEachUndo: []string{"\ncd", "ab\ncd"}},
		{name: "query replace without matches", content: "abc", keys: "<alt-shift-5>zz<enter>y<enter>", after: "abc", finalNoOp: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, buf, _ := newEmacsHandlerWithClipboard(t, tc.content)
			if tc.at != (term.Coordinates{}) {
				require.True(t, h.SetCursorAtScroll(tc.at))
			}
			for _, ev := range tc.arrange {
				require.True(t, runEvent(h, ev), "arrange event %#v", ev)
			}
			for _, ev := range tc.events {
				require.True(t, runEvent(h, ev), "command event %#v", ev)
			}
			if tc.keys != "" {
				feedKeys(t, h, tc.keys)
			}
			require.Equal(t, tc.after, buf.String())
			for i, want := range tc.afterEachUndo {
				require.True(t, runEvent(h, []term.Event{ctrl('/'), ctrl('_')}[i%2]), "undo %d", i)
				assert.Equal(t, want, buf.String(), "after undo %d", i)
			}
			if tc.finalNoOp {
				assert.False(t, runEvent(h, ctrl('/')))
				assert.Equal(t, tc.after, buf.String())
			}
		})
	}
}

func TestEmacsSentenceMotion(t *testing.T) {
	const three = "One.  Two three.  Four."
	const abbrev = "Mr. Smith stayed.  He left."
	const wrapped = "One two\nthree.  Four\nfive."
	const pars = "One end\n\nTwo."

	cases := []struct {
		name    string
		content string
		start   term.Coordinates
		event   term.Event
		handled bool
		at      term.Coordinates
	}{
		{"M-e to the first sentence end", three, term.Coordinates{}, alt('e'), true, term.Coordinates{X: 4}},
		{"M-e from a sentence end to the next", three, term.Coordinates{X: 4}, alt('e'), true, term.Coordinates{X: 16}},
		{"M-e from inside the second sentence", three, term.Coordinates{X: 6}, alt('e'), true, term.Coordinates{X: 16}},
		{"M-e to the buffer end", three, term.Coordinates{X: 16}, alt('e'), true, term.Coordinates{X: 23}},
		{"M-e at the buffer end is a no-op", three, term.Coordinates{X: 23}, alt('e'), false, term.Coordinates{X: 23}},
		{"a single space does not end a sentence", abbrev, term.Coordinates{}, alt('e'), true, term.Coordinates{X: 17}},
		{"a tab ends the sentence", "One.\tTwo", term.Coordinates{}, alt('e'), true, term.Coordinates{X: 4}},
		{"closing characters at the buffer end", `He said "Go."`, term.Coordinates{}, alt('e'), true, term.Coordinates{X: 13}},
		{"wide-rune sentences", "宇宙.  次", term.Coordinates{}, alt('e'), true, term.Coordinates{X: 3}},
		{"null cells inside a sentence", "a\x00b.  c", term.Coordinates{}, alt('e'), true, term.Coordinates{X: 4}},
		{"M-e from inter-sentence whitespace", three, term.Coordinates{X: 5}, alt('e'), true, term.Coordinates{X: 16}},
		{"M-e crosses line breaks inside a sentence", wrapped, term.Coordinates{}, alt('e'), true, term.Coordinates{Y: 1, X: 6}},
		{"M-e stops at a paragraph end without punctuation", pars, term.Coordinates{}, alt('e'), true, term.Coordinates{X: 7}},
		{"M-e from a paragraph end enters the next paragraph", pars, term.Coordinates{X: 7}, alt('e'), true, term.Coordinates{Y: 2, X: 4}},
		{"a closing quote belongs to the sentence", `He said "Stop."  Go.`, term.Coordinates{}, alt('e'), true, term.Coordinates{X: 15}},
		{"?! ends after the exclamation", "Really?!  Yes.", term.Coordinates{}, alt('e'), true, term.Coordinates{X: 8}},
		{"a space before end-of-line ends the sentence", "One. \nTwo.", term.Coordinates{}, alt('e'), true, term.Coordinates{X: 4}},
		{"no punctuation moves to the end of the paragraph", "abc def", term.Coordinates{}, alt('e'), true, term.Coordinates{X: 7}},
		{"M-e on an empty buffer is a no-op", "", term.Coordinates{}, alt('e'), false, term.Coordinates{}},

		{"M-a to the sentence start", three, term.Coordinates{X: 23}, alt('a'), true, term.Coordinates{X: 18}},
		{"M-a from inside a sentence", three, term.Coordinates{X: 8}, alt('a'), true, term.Coordinates{X: 6}},
		{"M-a at a sentence start goes to the previous start", three, term.Coordinates{X: 6}, alt('a'), true, term.Coordinates{}},
		{"M-a at the buffer start is a no-op", three, term.Coordinates{}, alt('a'), false, term.Coordinates{}},
		{"M-a from inter-sentence whitespace", three, term.Coordinates{X: 5}, alt('a'), true, term.Coordinates{}},
		{"M-a does not stop after an abbreviation", abbrev, term.Coordinates{X: 21}, alt('a'), true, term.Coordinates{X: 19}},
		{"M-a lands after a tab separator", "One.\tTwo", term.Coordinates{X: 8}, alt('a'), true, term.Coordinates{X: 5}},
		{"M-a from a wide-rune sentence start", "宇宙.  次", term.Coordinates{X: 5}, alt('a'), true, term.Coordinates{}},
		{"M-a crosses a blank line to the previous paragraph", pars, term.Coordinates{Y: 2}, alt('a'), true, term.Coordinates{}},
		{"M-a inside the second paragraph stops at its start", pars, term.Coordinates{Y: 2, X: 4}, alt('a'), true, term.Coordinates{Y: 2}},
		{"M-a lands after indentation", "  Indented one.  Two.", term.Coordinates{X: 17}, alt('a'), true, term.Coordinates{X: 2}},
		{"M-a to a sentence starting mid-paragraph line", wrapped, term.Coordinates{Y: 2, X: 5}, alt('a'), true, term.Coordinates{Y: 1, X: 8}},
		{"M-a to a sentence start after a line break", "One.\nTwo.", term.Coordinates{Y: 1, X: 2}, alt('a'), true, term.Coordinates{Y: 1}},
		{"M-a on an empty buffer is a no-op", "", term.Coordinates{}, alt('a'), false, term.Coordinates{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, buf := newEmacsHandler(t, tc.content)
			if tc.start != (term.Coordinates{}) {
				require.True(t, h.SetCursorAtScroll(tc.start))
			}
			var handled bool
			require.NotPanics(t, func() { _, handled = h.Handle(tc.event) })
			assert.Equal(t, tc.handled, handled)
			assert.Equal(t, tc.content, buf.String(), "sentence motion must not edit")
			assert.Equal(t, tc.at, h.CursorAtScroll())
		})
	}
}

func TestEmacsKillSentence(t *testing.T) {
	cases := []struct {
		name    string
		content string
		start   term.Coordinates
		handled bool
		want    string
		at      term.Coordinates
		killed  string
	}{
		{"kill to the sentence end", "One.  Two.", term.Coordinates{}, true, "  Two.", term.Coordinates{}, "One."},
		{"kill from mid-sentence", "One two.  X", term.Coordinates{X: 4}, true, "One   X", term.Coordinates{X: 4}, "two."},
		{"kill without punctuation takes the paragraph tail", "abc def\n\nx", term.Coordinates{}, true, "\n\nx", term.Coordinates{}, "abc def"},
		{"kill from a paragraph end crosses into the next", "ab\n\nx", term.Coordinates{X: 2}, true, "ab", term.Coordinates{X: 2}, "\n\nx"},
		{"kill at the buffer end is a no-op", "abc.", term.Coordinates{X: 4}, false, "abc.", term.Coordinates{X: 4}, ""},
		{"kill on an empty buffer is a no-op", "", term.Coordinates{}, false, "", term.Coordinates{}, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, buf, clip := newEmacsHandlerWithClipboard(t, tc.content)
			if tc.start != (term.Coordinates{}) {
				require.True(t, h.SetCursorAtScroll(tc.start))
			}
			var handled bool
			require.NotPanics(t, func() { _, handled = h.Handle(alt('k')) })
			assert.Equal(t, tc.handled, handled)
			assert.Equal(t, tc.want, buf.String())
			assert.Equal(t, tc.at, h.CursorAtScroll())
			assert.Equal(t, tc.killed, killRingText(clip), "kill ring")
		})
	}

	t.Run("consecutive M-k accumulate in the kill ring", func(t *testing.T) {
		h, buf, clip := newEmacsHandlerWithClipboard(t, "One.  Two.")
		require.True(t, runEvent(h, alt('k')))
		require.True(t, runEvent(h, alt('k')))
		assert.Equal(t, "", buf.String())
		assert.Equal(t, "One.  Two.", killRingText(clip))
	})

	t.Run("C-u 2 M-k kills two sentences as one entry", func(t *testing.T) {
		h, buf, clip := newEmacsHandlerWithClipboard(t, "One.  Two.  X")
		feedKeys(t, h, "<ctrl-u>2")
		require.True(t, runEvent(h, alt('k')))
		assert.Equal(t, "  X", buf.String())
		assert.Equal(t, "One.  Two.", killRingText(clip))
	})

	t.Run("C-- M-k kills back to the sentence start", func(t *testing.T) {
		h, buf, clip := newEmacsHandlerWithClipboard(t, "One.  Two.")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 10}))
		require.True(t, runEvent(h, ctrl('-')))
		require.True(t, runEvent(h, alt('k')))
		assert.Equal(t, "One.  ", buf.String())
		assert.Equal(t, term.Coordinates{X: 6}, h.CursorAtScroll())
		assert.Equal(t, "Two.", killRingText(clip))
	})

	t.Run("C-u -2 M-k kills two sentences backward", func(t *testing.T) {
		h, buf, clip := newEmacsHandlerWithClipboard(t, "One.  Two.  Three.")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 18}))
		feedKeys(t, h, "<ctrl-u>-2")
		require.True(t, runEvent(h, alt('k')))
		assert.Equal(t, "One.  ", buf.String())
		assert.Equal(t, term.Coordinates{X: 6}, h.CursorAtScroll())
		assert.Equal(t, "Two.  Three.", killRingText(clip))
	})

	t.Run("C-- M-k at the buffer start is refused", func(t *testing.T) {
		h, buf, clip := newEmacsHandlerWithClipboard(t, "One.")
		require.True(t, runEvent(h, ctrl('-')))
		assert.False(t, runEvent(h, alt('k')))
		assert.Equal(t, "One.", buf.String())
		assert.Equal(t, "", killRingText(clip))
	})
}

func TestEmacsZapToChar(t *testing.T) {
	cases := []struct {
		name    string
		content string
		start   term.Coordinates
		zap     term.Event
		handled bool
		want    string
		at      term.Coordinates
		killed  string
	}{
		{"zap through the next occurrence", "hello world", term.Coordinates{}, char('o'), true, " world", term.Coordinates{}, "hello"},
		{"zap when point is on the char kills just it", "xyz", term.Coordinates{}, char('x'), true, "yz", term.Coordinates{}, "x"},
		{"zap crosses lines", "ab\ncd", term.Coordinates{}, char('c'), true, "d", term.Coordinates{}, "ab\nc"},
		{"zap to RET kills through the newline", "ab\ncd", term.Coordinates{}, key(term.KeyEnter), true, "cd", term.Coordinates{}, "ab\n"},
		{"zap to SPC kills through the space", "ab cd", term.Coordinates{}, key(term.KeySpace), true, "cd", term.Coordinates{}, "ab "},
		{"zap not found leaves the buffer intact", "abc", term.Coordinates{}, char('q'), true, "abc", term.Coordinates{}, ""},
		{"zap behind point is not found", "ab", term.Coordinates{X: 2}, char('a'), true, "ab", term.Coordinates{X: 2}, ""},
		{"zap to a wide rune", "a界b界c", term.Coordinates{}, char('界'), true, "b界c", term.Coordinates{}, "a界"},
		{"zap across null cells", "a\x00x b", term.Coordinates{}, char('x'), true, " b", term.Coordinates{}, "a\x00x"},
		{"zap on an empty buffer reports failure", "", term.Coordinates{}, char('q'), true, "", term.Coordinates{}, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, buf, clip := newEmacsHandlerWithClipboard(t, tc.content)
			if tc.start != (term.Coordinates{}) {
				require.True(t, h.SetCursorAtScroll(tc.start))
			}
			require.True(t, runEvent(h, alt('z')), "M-z must be handled")
			var handled bool
			require.NotPanics(t, func() { _, handled = h.Handle(tc.zap) })
			assert.Equal(t, tc.handled, handled)
			assert.Equal(t, tc.want, buf.String())
			assert.Equal(t, tc.at, h.CursorAtScroll())
			assert.Equal(t, tc.killed, killRingText(clip), "kill ring")
		})
	}

	t.Run("C-g aborts the pending zap", func(t *testing.T) {
		h, buf, clip := newEmacsHandlerWithClipboard(t, "abc")
		require.True(t, runEvent(h, alt('z')))
		require.True(t, runEvent(h, ctrl('g')))
		// The next plain character self-inserts instead of zapping.
		require.True(t, runEvent(h, char('b')))
		assert.Equal(t, "babc", buf.String())
		assert.Equal(t, "", killRingText(clip))
	})

	t.Run("a named key aborts the pending zap", func(t *testing.T) {
		h, buf, clip := newEmacsHandlerWithClipboard(t, "abc")
		require.True(t, runEvent(h, alt('z')))
		require.True(t, runEvent(h, key(term.KeyArrowUp)))
		require.True(t, runEvent(h, char('b')))
		assert.Equal(t, "babc", buf.String())
		assert.Equal(t, "", killRingText(clip))
	})

	t.Run("a count beyond the last occurrence fails without editing", func(t *testing.T) {
		h, buf, clip := newEmacsHandlerWithClipboard(t, "axb")
		feedKeys(t, h, "<ctrl-u>5<alt-z>x")
		assert.Equal(t, "axb", buf.String())
		assert.Equal(t, term.Coordinates{}, h.CursorAtScroll())
		assert.Equal(t, "", killRingText(clip))
	})

	t.Run("a backward zap without a match fails without editing", func(t *testing.T) {
		h, buf, clip := newEmacsHandlerWithClipboard(t, "abc")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 3}))
		require.True(t, runEvent(h, ctrl('-')))
		require.True(t, runEvent(h, alt('z')))
		require.True(t, runEvent(h, char('q')))
		assert.Equal(t, "abc", buf.String())
		assert.Equal(t, term.Coordinates{X: 3}, h.CursorAtScroll())
		assert.Equal(t, "", killRingText(clip))
	})

	t.Run("consecutive zaps accumulate in the kill ring", func(t *testing.T) {
		h, buf, clip := newEmacsHandlerWithClipboard(t, "axbxc")
		require.True(t, runEvent(h, alt('z')))
		require.True(t, runEvent(h, char('x')))
		require.True(t, runEvent(h, alt('z')))
		require.True(t, runEvent(h, char('x')))
		assert.Equal(t, "c", buf.String())
		assert.Equal(t, "axbx", killRingText(clip))
	})

	t.Run("zap kill undoes in one step", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "ab\ncd")
		require.True(t, runEvent(h, alt('z')))
		require.True(t, runEvent(h, char('c')))
		require.Equal(t, "d", buf.String())
		require.True(t, runEvent(h, ctrl('/')))
		assert.Equal(t, "ab\ncd", buf.String())
	})
}

func TestEmacsUniversalArgument(t *testing.T) {
	const abc20 = "abcdefghijklmnopqrst"
	a70 := strings.Repeat("a", 70)

	motion := []struct {
		name    string
		content string
		start   term.Coordinates
		keys    []term.Event
		at      term.Coordinates
	}{
		{"C-u C-f moves four", abc20, term.Coordinates{},
			[]term.Event{ctrl('u'), ctrl('f')}, term.Coordinates{X: 4}},
		{"C-u C-u C-f moves sixteen", abc20, term.Coordinates{},
			[]term.Event{ctrl('u'), ctrl('u'), ctrl('f')}, term.Coordinates{X: 16}},
		{"C-u C-u C-u C-f moves sixty-four", a70, term.Coordinates{},
			[]term.Event{ctrl('u'), ctrl('u'), ctrl('u'), ctrl('f')}, term.Coordinates{X: 64}},
		{"C-u 3 C-f moves three", abc20, term.Coordinates{},
			[]term.Event{ctrl('u'), char('3'), ctrl('f')}, term.Coordinates{X: 3}},
		{"C-u 1 2 C-b moves twelve back", abc20, term.Coordinates{X: 15},
			[]term.Event{ctrl('u'), char('1'), char('2'), ctrl('b')}, term.Coordinates{X: 3}},
		{"M-5 C-f moves five", abc20, term.Coordinates{},
			[]term.Event{alt('5'), ctrl('f')}, term.Coordinates{X: 5}},
		{"M-1 M-0 C-f chains meta digits", abc20, term.Coordinates{},
			[]term.Event{alt('1'), alt('0'), ctrl('f')}, term.Coordinates{X: 10}},
		{"C-5 digit argument", abc20, term.Coordinates{},
			[]term.Event{ctrl('5'), ctrl('f')}, term.Coordinates{X: 5}},
		{"C-M-3 digit argument", abc20, term.Coordinates{},
			[]term.Event{ctrlAlt('3'), ctrl('f')}, term.Coordinates{X: 3}},
		{"C-u C-n moves four lines down", "a\nb\nc\nd\ne\nf", term.Coordinates{},
			[]term.Event{ctrl('u'), ctrl('n')}, term.Coordinates{Y: 4}},
		{"C-u 3 right-arrow moves three", abc20, term.Coordinates{},
			[]term.Event{ctrl('u'), char('3'), key(term.KeyArrowRight)}, term.Coordinates{X: 3}},
		{"C-- C-f moves one back", abc20, term.Coordinates{X: 2},
			[]term.Event{ctrl('-'), ctrl('f')}, term.Coordinates{X: 1}},
		{"bare M-- C-f moves one back", abc20, term.Coordinates{X: 2},
			[]term.Event{alt('-'), ctrl('f')}, term.Coordinates{X: 1}},
		{"bare C-u - C-f moves one back", abc20, term.Coordinates{X: 2},
			[]term.Event{ctrl('u'), char('-'), ctrl('f')}, term.Coordinates{X: 1}},
		{"C-M-- negative argument", abc20, term.Coordinates{X: 2},
			[]term.Event{ctrlAlt('-'), ctrl('f')}, term.Coordinates{X: 1}},
		{"M-- 3 C-f moves three back", abc20, term.Coordinates{X: 5},
			[]term.Event{alt('-'), char('3'), ctrl('f')}, term.Coordinates{X: 2}},
		{"C-u - 5 C-f moves five back", abc20, term.Coordinates{X: 6},
			[]term.Event{ctrl('u'), char('-'), char('5'), ctrl('f')}, term.Coordinates{X: 1}},
		{"C-- 2 down-arrow moves two up", "a\nb\nc\nd", term.Coordinates{Y: 3},
			[]term.Event{ctrl('-'), char('2'), key(term.KeyArrowDown)}, term.Coordinates{Y: 1}},
		{"C-u 2 M-f crosses two words", "one two three", term.Coordinates{},
			[]term.Event{ctrl('u'), char('2'), alt('f')}, term.Coordinates{X: 7}},
		{"C-- M-e moves back a sentence", "One.  Two.", term.Coordinates{X: 6},
			[]term.Event{ctrl('-'), alt('e')}, term.Coordinates{}},
		{"C-u 2 M-e crosses two sentence ends", "One.  Two three.  Four.", term.Coordinates{},
			[]term.Event{ctrl('u'), char('2'), alt('e')}, term.Coordinates{X: 16}},
		{"C-u 100 C-f clamps at the buffer end", "abc", term.Coordinates{},
			[]term.Event{ctrl('u'), char('1'), char('0'), char('0'), ctrl('f')}, term.Coordinates{X: 3}},
	}
	for _, tc := range motion {
		t.Run(tc.name, func(t *testing.T) {
			h, buf := newEmacsHandler(t, tc.content)
			if tc.start != (term.Coordinates{}) {
				require.True(t, h.SetCursorAtScroll(tc.start))
			}
			for _, ev := range tc.keys {
				require.True(t, runEvent(h, ev), "key %+v", ev)
			}
			assert.Equal(t, tc.content, buf.String(), "motion must not edit")
			assert.Equal(t, tc.at, h.CursorAtScroll())
		})
	}

	t.Run("C-u 8 x inserts eight characters as one undo", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "")
		feedKeys(t, h, "<ctrl-u>8x")
		assert.Equal(t, "xxxxxxxx", buf.String())
		require.True(t, runEvent(h, ctrl('/')))
		assert.Equal(t, "", buf.String())
	})

	t.Run("C-u 5 C-u 7 self-inserts the digit", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "")
		feedKeys(t, h, "<ctrl-u>5<ctrl-u>7")
		assert.Equal(t, "77777", buf.String())
	})

	t.Run("a negative argument refuses self-insert", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "")
		require.True(t, runEvent(h, ctrl('-')))
		assert.False(t, runEvent(h, char('x')))
		assert.Equal(t, "", buf.String())
	})

	t.Run("C-g aborts the pending argument", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "")
		feedKeys(t, h, "<ctrl-u>5<ctrl-g>x")
		assert.Equal(t, "x", buf.String())
	})

	t.Run("C-u 2 RET inserts two newlines", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "")
		feedKeys(t, h, "<ctrl-u>2<enter>")
		assert.Equal(t, "\n\n", buf.String())
	})

	t.Run("C-u 0 C-f is a handled no-op", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "abc")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 1}))
		feedKeys(t, h, "<ctrl-u>0")
		assert.True(t, runEvent(h, ctrl('f')))
		assert.Equal(t, "abc", buf.String())
		assert.Equal(t, term.Coordinates{X: 1}, h.CursorAtScroll())
	})

	t.Run("digit accumulation saturates instead of overflowing", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "ab")
		feedKeys(t, h, "<ctrl-u>9999999999999999999")
		assert.True(t, runEvent(h, ctrl('f')), "the count must stay positive")
		assert.Equal(t, "ab", buf.String())
		assert.Equal(t, term.Coordinates{X: 2}, h.CursorAtScroll())
	})

	t.Run("C-u 2 C-t drags the character forward twice", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "abc")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 1}))
		feedKeys(t, h, "<ctrl-u>2")
		require.True(t, runEvent(h, ctrl('t')))
		assert.Equal(t, "bca", buf.String())
		assert.Equal(t, term.Coordinates{X: 3}, h.CursorAtScroll())
	})

	t.Run("C-- C-t is refused", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "abc")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 1}))
		require.True(t, runEvent(h, ctrl('-')))
		assert.False(t, runEvent(h, ctrl('t')))
		assert.Equal(t, "abc", buf.String())
		assert.Equal(t, term.Coordinates{X: 1}, h.CursorAtScroll())
	})

	t.Run("C-- M-t is refused", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "alpha beta")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 5}))
		require.True(t, runEvent(h, ctrl('-')))
		assert.False(t, runEvent(h, alt('t')))
		assert.Equal(t, "alpha beta", buf.String())
	})
}

func TestEmacsUniversalArgumentKills(t *testing.T) {
	t.Run("C-u 2 M-d kills two words as one entry", func(t *testing.T) {
		h, buf, clip := newEmacsHandlerWithClipboard(t, "one two three")
		feedKeys(t, h, "<ctrl-u>2<alt-d>")
		assert.Equal(t, " three", buf.String())
		assert.Equal(t, "one two", killRingText(clip))
		require.True(t, runEvent(h, ctrl('/')))
		assert.Equal(t, "one two three", buf.String(), "counted kill is one undo")
	})

	t.Run("C-- M-d kills the previous word", func(t *testing.T) {
		h, buf, clip := newEmacsHandlerWithClipboard(t, "one two three")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 7}))
		require.True(t, runEvent(h, ctrl('-')))
		require.True(t, runEvent(h, alt('d')))
		assert.Equal(t, "one  three", buf.String())
		assert.Equal(t, "two", killRingText(clip))
	})

	t.Run("C-u 2 M-DEL kills two words backward", func(t *testing.T) {
		h, buf, clip := newEmacsHandlerWithClipboard(t, "one two three")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 13}))
		require.True(t, runEvent(h, ctrl('u')))
		require.True(t, runEvent(h, char('2')))
		require.True(t, runEvent(h, key2(term.KeyBackspace, term.ModAlt)))
		assert.Equal(t, "one ", buf.String())
		assert.Equal(t, "two three", killRingText(clip))
	})

	t.Run("the prefix keystrokes preserve the kill chain", func(t *testing.T) {
		h, buf, clip := newEmacsHandlerWithClipboard(t, "one two three")
		require.True(t, runEvent(h, alt('d')))
		feedKeys(t, h, "<ctrl-u>1")
		require.True(t, runEvent(h, alt('d')))
		assert.Equal(t, " three", buf.String())
		assert.Equal(t, "one two", killRingText(clip), "chain must survive the prefix")
	})

	t.Run("C-u 2 C-k kills two whole lines", func(t *testing.T) {
		h, buf, clip := newEmacsHandlerWithClipboard(t, "ab\ncd\nef")
		feedKeys(t, h, "<ctrl-u>2<ctrl-k>")
		assert.Equal(t, "ef", buf.String())
		assert.Equal(t, "ab\ncd\n", killRingText(clip))
		require.True(t, runEvent(h, ctrl('/')))
		assert.Equal(t, "ab\ncd\nef", buf.String())
	})

	t.Run("C-u 2 C-k from mid-line", func(t *testing.T) {
		h, buf, clip := newEmacsHandlerWithClipboard(t, "abcd\nef\ngh")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 2}))
		feedKeys(t, h, "<ctrl-u>2<ctrl-k>")
		assert.Equal(t, "abgh", buf.String())
		assert.Equal(t, "cd\nef\n", killRingText(clip))
	})

	t.Run("C-u 0 C-k kills to the beginning of the line", func(t *testing.T) {
		h, buf, clip := newEmacsHandlerWithClipboard(t, "abcd")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 2}))
		feedKeys(t, h, "<ctrl-u>0<ctrl-k>")
		assert.Equal(t, "cd", buf.String())
		assert.Equal(t, term.Coordinates{}, h.CursorAtScroll())
		assert.Equal(t, "ab", killRingText(clip))
	})

	t.Run("C-- C-k kills back to the end of the previous line", func(t *testing.T) {
		h, buf, clip := newEmacsHandlerWithClipboard(t, "ab\ncd")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{Y: 1, X: 1}))
		require.True(t, runEvent(h, ctrl('-')))
		require.True(t, runEvent(h, ctrl('k')))
		assert.Equal(t, "abd", buf.String())
		assert.Equal(t, "\nc", killRingText(clip))
	})

	t.Run("C-u 10 C-k clamps at the buffer end", func(t *testing.T) {
		h, buf, clip := newEmacsHandlerWithClipboard(t, "ab\ncd")
		feedKeys(t, h, "<ctrl-u>10<ctrl-k>")
		assert.Equal(t, "", buf.String())
		assert.Equal(t, "ab\ncd", killRingText(clip))
	})

	t.Run("C-- C-k at the buffer start is refused", func(t *testing.T) {
		h, buf, clip := newEmacsHandlerWithClipboard(t, "ab")
		require.True(t, runEvent(h, ctrl('-')))
		assert.False(t, runEvent(h, ctrl('k')))
		assert.Equal(t, "ab", buf.String())
		assert.Equal(t, "", killRingText(clip))
	})

	t.Run("C-- C-k clamps at the buffer start", func(t *testing.T) {
		h, buf, clip := newEmacsHandlerWithClipboard(t, "ab")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 1}))
		require.True(t, runEvent(h, ctrl('-')))
		require.True(t, runEvent(h, ctrl('k')))
		assert.Equal(t, "b", buf.String())
		assert.Equal(t, term.Coordinates{}, h.CursorAtScroll())
		assert.Equal(t, "a", killRingText(clip))
	})

	t.Run("C-u 2 M-z zaps through the second occurrence", func(t *testing.T) {
		h, buf, clip := newEmacsHandlerWithClipboard(t, "axbxc")
		feedKeys(t, h, "<ctrl-u>2<alt-z>x")
		assert.Equal(t, "c", buf.String())
		assert.Equal(t, "axbx", killRingText(clip))
	})

	t.Run("C-- M-z zaps backward through the char", func(t *testing.T) {
		h, buf, clip := newEmacsHandlerWithClipboard(t, "axbc")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 4}))
		require.True(t, runEvent(h, ctrl('-')))
		require.True(t, runEvent(h, alt('z')))
		require.True(t, runEvent(h, char('x')))
		assert.Equal(t, "a", buf.String())
		assert.Equal(t, "xbc", killRingText(clip))
		assert.Equal(t, term.Coordinates{X: 1}, h.CursorAtScroll())
	})
}

func TestEmacsUniversalArgumentSpecials(t *testing.T) {
	t.Run("C-u C-SPC pops the mark and jumps", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "abcdefgh")
		require.True(t, runEvent(h, key2(term.KeySpace, term.ModCtrl)))
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 5}))
		feedKeys(t, h, "<ctrl-u>")
		require.True(t, runEvent(h, key2(term.KeySpace, term.ModCtrl)))
		assert.Equal(t, term.Coordinates{}, h.CursorAtScroll())
		assert.Empty(t, emacsMarks(h))
	})

	t.Run("repeated C-u C-SPC pops marks newest first", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "abcdefgh")
		require.True(t, runEvent(h, key2(term.KeySpace, term.ModCtrl)))
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 3}))
		require.True(t, runEvent(h, key2(term.KeySpace, term.ModCtrl)))
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 6}))
		feedKeys(t, h, "<ctrl-u>")
		require.True(t, runEvent(h, key2(term.KeySpace, term.ModCtrl)))
		assert.Equal(t, term.Coordinates{X: 3}, h.CursorAtScroll())
		feedKeys(t, h, "<ctrl-u>")
		require.True(t, runEvent(h, key2(term.KeySpace, term.ModCtrl)))
		assert.Equal(t, term.Coordinates{}, h.CursorAtScroll())
		assert.Empty(t, emacsMarks(h))
	})

	t.Run("C-u C-y yanks leaving point before the text", func(t *testing.T) {
		h, buf, _ := newEmacsHandlerWithClipboard(t, "ab")
		require.True(t, runEvent(h, ctrl('k')))
		require.Equal(t, "", buf.String())
		feedKeys(t, h, "<ctrl-u>")
		require.True(t, runEvent(h, ctrl('y')))
		assert.Equal(t, "ab", buf.String())
		assert.Equal(t, term.Coordinates{}, h.CursorAtScroll())
	})

	t.Run("C-u 2 C-y yanks the second most recent kill", func(t *testing.T) {
		h, buf, _ := newEmacsHandlerWithHistory(t, "one two")
		require.True(t, runEvent(h, alt('d'))) // kills "one"
		require.True(t, runEvent(h, key(term.KeyArrowRight)))
		require.True(t, runEvent(h, alt('d'))) // kills "two"
		require.Equal(t, " ", buf.String())
		require.True(t, h.SetCursorAtScroll(term.Coordinates{}))
		feedKeys(t, h, "<ctrl-u>2")
		require.True(t, runEvent(h, ctrl('y')))
		assert.Equal(t, "one ", buf.String())
		assert.Equal(t, term.Coordinates{X: 3}, h.CursorAtScroll())
	})

	t.Run("C-- M-u upcases the previous word in place", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "foo bar")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 7}))
		require.True(t, runEvent(h, ctrl('-')))
		require.True(t, runEvent(h, alt('u')))
		assert.Equal(t, "foo BAR", buf.String())
		assert.Equal(t, term.Coordinates{X: 7}, h.CursorAtScroll())
	})

	t.Run("C-u - 2 M-c capitalizes the previous two words in place", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "foo bar baz")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 11}))
		feedKeys(t, h, "<ctrl-u>-2")
		require.True(t, runEvent(h, alt('c')))
		assert.Equal(t, "foo Bar Baz", buf.String())
		assert.Equal(t, term.Coordinates{X: 11}, h.CursorAtScroll())
	})

	t.Run("C-u 3 C-v scrolls three lines", func(t *testing.T) {
		h, _ := newEmacsHandler(t, pageTestContent(60))
		feedKeys(t, h, "<ctrl-u>3")
		require.True(t, runEvent(h, ctrl('v')))
		assert.Equal(t, 3, h.SeekOffset())
		feedKeys(t, h, "<ctrl-u>2")
		require.True(t, runEvent(h, alt('v')))
		assert.Equal(t, 1, h.SeekOffset())
	})

	t.Run("a counted scroll clamps at the buffer edges", func(t *testing.T) {
		h, _ := newEmacsHandler(t, pageTestContent(60))
		feedKeys(t, h, "<ctrl-u>999")
		require.True(t, runEvent(h, ctrl('v')))
		assert.Equal(t, h.MaxSeekOffset(), h.SeekOffset())
		feedKeys(t, h, "<ctrl-u>999")
		require.True(t, runEvent(h, alt('v')))
		assert.Equal(t, 0, h.SeekOffset())
		feedKeys(t, h, "<ctrl-u>2")
		assert.False(t, runEvent(h, alt('v')), "scrolling up at the top is refused")
	})

	t.Run("C-u C-SPC with an empty mark ring is refused", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "abc")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 2}))
		feedKeys(t, h, "<ctrl-u>")
		assert.False(t, runEvent(h, key2(term.KeySpace, term.ModCtrl)))
		assert.Equal(t, term.Coordinates{X: 2}, h.CursorAtScroll())
	})

	t.Run("C-u N C-y beyond the kill ring is refused", func(t *testing.T) {
		h, buf, _ := newEmacsHandlerWithHistory(t, "one two")
		require.True(t, runEvent(h, alt('d')))
		require.Equal(t, " two", buf.String())
		// The kill leaves point at the line start already.
		feedKeys(t, h, "<ctrl-u>5")
		assert.False(t, runEvent(h, ctrl('y')))
		assert.Equal(t, " two", buf.String())
		assert.Equal(t, term.Coordinates{}, h.CursorAtScroll())
	})

	t.Run("C-u C-y with an empty clipboard stays put", func(t *testing.T) {
		h, buf, _ := newEmacsHandlerWithClipboard(t, "abc")
		feedKeys(t, h, "<ctrl-u>")
		assert.True(t, runEvent(h, ctrl('y')))
		assert.Equal(t, "abc", buf.String())
		assert.Equal(t, term.Coordinates{}, h.CursorAtScroll())
	})

	t.Run("C-u -5 M-u clamps at the buffer start", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "ab cd")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 5}))
		feedKeys(t, h, "<ctrl-u>-5")
		require.True(t, runEvent(h, alt('u')))
		assert.Equal(t, "AB CD", buf.String())
		assert.Equal(t, term.Coordinates{X: 5}, h.CursorAtScroll())
	})

	t.Run("C-- M-u with no previous word is refused", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "ab")
		require.True(t, runEvent(h, ctrl('-')))
		assert.False(t, runEvent(h, alt('u')))
		assert.Equal(t, "ab", buf.String())
	})

	t.Run("C-u 2 M-y rotates the kill ring twice", func(t *testing.T) {
		h, buf, _ := newEmacsHandlerWithHistory(t, "one two three")
		// Build three kill-ring entries: "one", "two", "three".
		require.True(t, runEvent(h, alt('d')))
		require.True(t, runEvent(h, key(term.KeyArrowRight)))
		require.True(t, runEvent(h, alt('d')))
		require.True(t, runEvent(h, key(term.KeyArrowRight)))
		require.True(t, runEvent(h, alt('d')))
		require.Equal(t, "  ", buf.String())
		require.True(t, h.SetCursorAtScroll(term.Coordinates{}))
		require.True(t, runEvent(h, ctrl('y')))
		require.Equal(t, "three  ", buf.String())
		feedKeys(t, h, "<ctrl-u>2")
		require.True(t, runEvent(h, alt('y')))
		assert.Equal(t, "one  ", buf.String())
	})
}

func TestEmacsUndoAmalgamation(t *testing.T) {
	wideRun := make([]term.Event, 25)
	wideDeletes := make([]term.Event, 25)
	for i := range wideRun {
		wideRun[i] = char('界')
		wideDeletes[i] = key(term.KeyBackspace)
	}
	tests := []struct {
		name          string
		content       string
		at            term.Coordinates
		events        []term.Event
		after         string
		afterEachUndo []string
	}{
		{name: "ASCII and space form one insert run", events: []term.Event{char('a'), char('b'), key(term.KeySpace), char('c'), char('d')}, after: "ab cd", afterEachUndo: []string{""}},
		{name: "wide emoji and combining marks form one insert run", events: []term.Event{char('界'), char('🚀'), char('\u0301'), char('世')}, after: "界🚀\u0301世", afterEachUndo: []string{""}},
		{name: "wide run caps at twenty logical characters", events: wideRun, after: strings.Repeat("界", 25), afterEachUndo: []string{strings.Repeat("界", 20), ""}},
		{name: "motion splits insert runs", events: []term.Event{char('a'), char('b'), ctrl('b'), char('c'), char('d')}, after: "acdb", afterEachUndo: []string{"ab", ""}},
		{name: "unbound key splits insert runs", events: []term.Event{char('a'), char('b'), key(term.KeyF5), char('c'), char('d')}, after: "abcd", afterEachUndo: []string{"ab", ""}},
		{name: "normal TAB has its own group", events: []term.Event{char('a'), char('b'), key(term.KeyTab), char('c'), char('d')}, after: "ab\tcd", afterEachUndo: []string{"ab\t", "ab", ""}},
		{name: "quoted TAB has its own group", events: []term.Event{char('a'), char('b'), ctrl('q'), key(term.KeyTab), char('c'), char('d')}, after: "ab\tcd", afterEachUndo: []string{"ab\t", "ab", ""}},
		{name: "quoted NUL has its own group", events: []term.Event{char('a'), char('b'), ctrl('q'), ctrl('@'), char('c'), char('d')}, after: "ab\x00cd", afterEachUndo: []string{"ab\x00", "ab", ""}},
		{name: "newline has its own group", events: []term.Event{char('a'), char('b'), key(term.KeyEnter), char('c'), char('d')}, after: "ab\ncd", afterEachUndo: []string{"ab\n", "ab", ""}},
		{name: "empty paste splits insert runs", events: []term.Event{char('a'), char('b'), {Type: term.EventPasteStart}, {Type: term.EventPasteEnd}, char('c'), char('d')}, after: "abcd", afterEachUndo: []string{"ab", ""}},
		{name: "nonempty paste has its own group", events: []term.Event{char('a'), char('b'), {Type: term.EventPasteStart}, char('\t'), char('界'), {Type: term.EventPasteEnd}, char('c'), char('d')}, after: "ab\t界cd", afterEachUndo: []string{"ab\t界", "ab", ""}},
		{name: "backspace aliases share a mixed-width run", content: "a\t\x00界🚀", at: term.Coordinates{X: 5}, events: []term.Event{key(term.KeyBackspace), ctrl('h'), key(term.KeyBackspace)}, after: "a\t", afterEachUndo: []string{"a\t\x00界🚀"}},
		{name: "forward-delete aliases share a mixed-width run", content: "\t\x00界🚀z", events: []term.Event{key(term.KeyDelete), ctrl('d'), key(term.KeyDelete), ctrl('d')}, after: "z", afterEachUndo: []string{"\t\x00界🚀z"}},
		{name: "wide delete run caps at twenty logical characters", content: strings.Repeat("界", 25), at: term.Coordinates{X: 25}, events: wideDeletes, after: "", afterEachUndo: []string{strings.Repeat("界", 5), strings.Repeat("界", 25)}},
		{name: "switching delete direction splits runs", content: "abcdef", at: term.Coordinates{X: 3}, events: []term.Event{key(term.KeyBackspace), ctrl('d')}, after: "abef", afterEachUndo: []string{"abdef", "abcdef"}},
		{name: "typing then deleting splits runs", events: []term.Event{char('a'), char('b'), key(term.KeyBackspace)}, after: "a", afterEachUndo: []string{"ab", ""}},
		{name: "boundary no-op splits delete runs", content: "abc", at: term.Coordinates{X: 3}, events: []term.Event{key(term.KeyBackspace), ctrl('f'), ctrl('h')}, after: "a", afterEachUndo: []string{"ab", "abc"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, buf := newEmacsHandler(t, tc.content)
			if tc.at != (term.Coordinates{}) {
				require.True(t, h.SetCursorAtScroll(tc.at))
			}
			for _, ev := range tc.events {
				h.Handle(ev)
			}
			require.Equal(t, tc.after, buf.String())
			for i, want := range tc.afterEachUndo {
				require.True(t, runEvent(h, []term.Event{ctrl('/'), ctrl('_')}[i%2]), "undo %d", i)
				assert.Equal(t, want, buf.String(), "after undo %d", i)
			}
		})
	}
}

func TestEmacsMacroKeys(t *testing.T) {
	t.Run("F3 without a recorder is a no-op", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "ab")
		assert.False(t, runEvent(h, key(term.KeyF3)))
		assert.Equal(t, "ab", buf.String())
	})

	t.Run("F4 without a player is a no-op", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "ab")
		assert.False(t, runEvent(h, key(term.KeyF4)))
	})

	t.Run("F3 records, F4 ends the recording and then replays", func(t *testing.T) {
		recorder := new(testMacroRecorder)
		player := new(testMacroPlayer)
		h, _ := newEmacsHandler(t, "ab",
			WithMacroRecorder(recorder), WithMacroPlayer(player))

		require.True(t, runEvent(h, key(term.KeyF3)))
		assert.True(t, recorder.recording)
		assert.Equal(t, []string{registerset.UnnamedRegisterID}, recorder.started)

		require.True(t, runEvent(h, key(term.KeyF4)))
		assert.False(t, recorder.recording)
		assert.Equal(t, 1, recorder.stopped)
		assert.Empty(t, player.plays)

		require.True(t, runEvent(h, key(term.KeyF4)))
		assert.Equal(t, []testMacroPlay{{registerID: registerset.UnnamedRegisterID, count: 1}}, player.plays)
	})

	t.Run("C-q is quoted-insert, not a macro key", func(t *testing.T) {
		recorder := new(testMacroRecorder)
		h, buf := newEmacsHandler(t, "ab", WithMacroRecorder(recorder))
		require.True(t, runEvent(h, ctrl('q')))
		assert.False(t, recorder.recording)
		require.True(t, runEvent(h, ctrl('i')))
		assert.Equal(t, "\tab", buf.String())
	})

	// The kmacro keys are a small state machine, so drive the transitions
	// as a table rather than one happy path.
	t.Run("transitions", func(t *testing.T) {
		tests := []struct {
			name          string
			keys          []term.Event
			wantHandled   []bool
			wantStarts    int
			wantStops     int
			wantPlays     int
			wantRecording bool
		}{
			{"idle", nil, nil, 0, 0, 0, false},
			{"F3 starts", []term.Event{key(term.KeyF3)}, []bool{true}, 1, 0, 0, true},
			// GNU's F3 inserts the kmacro counter while defining; Rune does
			// not implement counters, so a second F3 is simply unhandled.
			{"F3 twice does not restart", []term.Event{key(term.KeyF3), key(term.KeyF3)}, []bool{true, false}, 1, 0, 0, true},
			{"F4 alone plays", []term.Event{key(term.KeyF4)}, []bool{true}, 0, 0, 1, false},
			{"F4 twice plays twice", []term.Event{key(term.KeyF4), key(term.KeyF4)}, []bool{true, true}, 0, 0, 2, false},
			{"F3 F4 stops without playing", []term.Event{key(term.KeyF3), key(term.KeyF4)}, []bool{true, true}, 1, 1, 0, false},
			{"F3 F4 F4 stops then plays", []term.Event{key(term.KeyF3), key(term.KeyF4), key(term.KeyF4)}, []bool{true, true, true}, 1, 1, 1, false},
			{"F3 F4 F3 records again", []term.Event{key(term.KeyF3), key(term.KeyF4), key(term.KeyF3)}, []bool{true, true, true}, 2, 1, 0, true},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				recorder := new(testMacroRecorder)
				player := new(testMacroPlayer)
				h, _ := newEmacsHandler(t, "ab",
					WithMacroRecorder(recorder), WithMacroPlayer(player))
				for i, ev := range tc.keys {
					assert.Equalf(t, tc.wantHandled[i], runEvent(h, ev), "key %d", i)
				}
				assert.Len(t, recorder.started, tc.wantStarts)
				assert.Equal(t, tc.wantStops, recorder.stopped)
				assert.Len(t, player.plays, tc.wantPlays)
				assert.Equal(t, tc.wantRecording, recorder.recording)
			})
		}
	})

	t.Run("recording never edits the buffer", func(t *testing.T) {
		for _, content := range []string{"", "ab", "\x00\x00", "界界", "\t"} {
			recorder := new(testMacroRecorder)
			player := new(testMacroPlayer)
			h, buf := newEmacsHandler(t, content,
				WithMacroRecorder(recorder), WithMacroPlayer(player))
			h.SetCursorAtScroll(term.Coordinates{Y: 99, X: 99})
			require.NotPanicsf(t, func() {
				h.Handle(key(term.KeyF3))
				h.Handle(key(term.KeyF4))
				h.Handle(key(term.KeyF4))
			}, "content %q", content)
			assert.Equal(t, content, buf.String())
		}
	})
}

func TestEmacsPasteEvents(t *testing.T) {
	paste := func(h text.Handler, s string) {
		h.Handle(term.Event{Type: term.EventPasteStart})
		for _, r := range s {
			h.Handle(term.Event{Type: term.EventKey, Ch: r})
		}
		h.Handle(term.Event{Type: term.EventPasteEnd})
	}

	t.Run("paste inserts the buffered text at point", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "xy")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 1}))
		paste(h, "abc")
		assert.Equal(t, "xabcy", buf.String())
		assert.Equal(t, term.Coordinates{X: 4}, h.CursorAtScroll())
	})

	t.Run("paste with newlines inserts them literally", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "xy")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 1}))
		paste(h, "a\nb")
		assert.Equal(t, "xa\nby", buf.String())
		assert.Equal(t, term.Coordinates{Y: 1, X: 1}, h.CursorAtScroll())
	})

	t.Run("no text is inserted before paste end", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "xy")
		h.Handle(term.Event{Type: term.EventPasteStart})
		h.Handle(term.Event{Type: term.EventKey, Ch: 'a'})
		assert.Equal(t, "xy", buf.String(), "characters must buffer until paste end")
		h.Handle(term.Event{Type: term.EventPasteEnd})
		assert.Equal(t, "axy", buf.String())
	})

	t.Run("paste replaces an active selection", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "abcde")
		require.True(t, runEvent(h, key2(term.KeyArrowRight, term.ModShift)))
		require.True(t, runEvent(h, key2(term.KeyArrowRight, term.ModShift)))
		paste(h, "Z")
		assert.Equal(t, "Zcde", buf.String())
		assert.Equal(t, term.Coordinates{X: 1}, h.CursorAtScroll())
	})

	t.Run("stray paste end is safe", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "ab")
		require.NotPanics(t, func() { h.Handle(term.Event{Type: term.EventPasteEnd}) })
		assert.Equal(t, "ab", buf.String())
		assert.Equal(t, term.Coordinates{}, h.CursorAtScroll())
	})

	t.Run("paste into an empty buffer", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "")
		paste(h, "hi")
		assert.Equal(t, "hi", buf.String())
		assert.Equal(t, term.Coordinates{X: 2}, h.CursorAtScroll())
	})
}

func TestEmacsBracketCommands(t *testing.T) {
	blocks := []struct {
		name    string
		content string
		start   term.Coordinates
		handled bool
		sel     string
		at      term.Coordinates
	}{
		{"C-S-M selects the enclosing parens", "(ab)", term.Coordinates{X: 2}, true, "(ab)", term.Coordinates{X: 3}},
		{"C-S-M selects braces around point", "x{y}z", term.Coordinates{X: 2}, true, "{y}", term.Coordinates{X: 3}},
		{"C-S-M selects brackets around point", "a[b]c", term.Coordinates{X: 2}, true, "[b]", term.Coordinates{X: 3}},
		{"C-S-M prefers the brace pair over inner brackets", "{[ab]}", term.Coordinates{X: 2}, true, "{[ab]}", term.Coordinates{X: 5}},
		{"C-S-M outside any block is a no-op", "abc", term.Coordinates{X: 1}, false, "", term.Coordinates{X: 1}},
	}

	for _, tc := range blocks {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := newEmacsHandler(t, tc.content)
			if tc.start != (term.Coordinates{}) {
				require.True(t, h.SetCursorAtScroll(tc.start))
			}
			var handled bool
			require.NotPanics(t, func() { _, handled = h.Handle(ctrl('M')) })
			assert.Equal(t, tc.handled, handled)
			sel, ok := h.Selection()
			assert.Equal(t, tc.sel != "", ok)
			assert.Equal(t, tc.sel, sel)
			assert.Equal(t, tc.at, h.CursorAtScroll())
		})
	}

	t.Run("ModCtrlShift M matches the upper-case ModCtrl path", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "(ab)")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 2}))
		require.True(t, runEvent(h, term.Event{Type: term.EventKey, Mod: term.ModCtrlShift, Ch: 'M'}))
		sel, ok := h.Selection()
		require.True(t, ok)
		assert.Equal(t, "(ab)", sel)
	})

	t.Run("C-m inserts a newline like RET", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "ab")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 1}))
		require.True(t, runEvent(h, ctrl('m')))
		assert.Equal(t, "a\nb", buf.String())
	})

	t.Run("C-i indents like TAB", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "ab")
		require.True(t, runEvent(h, ctrl('i')))
		assert.Equal(t, "\tab", buf.String())
	})
}

func TestEmacsTabSelection(t *testing.T) {
	t.Run("Tab indents every selected line", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "ab\ncd")
		require.True(t, runEvent(h, key2(term.KeyArrowDown, term.ModShift)))
		require.True(t, runEvent(h, key(term.KeyTab)))
		assert.Equal(t, "\tab\n\tcd", buf.String())
		_, ok := h.Selection()
		assert.False(t, ok, "selection is consumed by the shift")
	})

	t.Run("Shift-Tab dedents every selected line", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "\tab\n\tcd")
		require.True(t, runEvent(h, key2(term.KeyArrowDown, term.ModShift)))
		require.True(t, runEvent(h, key2(term.KeyTab, term.ModShift)))
		assert.Equal(t, "ab\ncd", buf.String())
	})

	t.Run("Shift-Tab on unindented selection is a no-op", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "ab\ncd")
		require.True(t, runEvent(h, key2(term.KeyArrowDown, term.ModShift)))
		require.NotPanics(t, func() { h.Handle(key2(term.KeyTab, term.ModShift)) })
		assert.Equal(t, "ab\ncd", buf.String())
	})
}

// asciiControlChords enumerates every Control chord that ASCII maps onto a
// control code, paired with the code quoted-insert must produce for it.
func asciiControlChords() []struct {
	name string
	ev   term.Event
	want rune
} {
	var out []struct {
		name string
		ev   term.Event
		want rune
	}
	for ch := '@'; ch <= '_'; ch++ {
		out = append(out, struct {
			name string
			ev   term.Event
			want rune
		}{fmt.Sprintf("C-%c", ch), ctrl(ch), ch - '@'})
	}
	for ch := 'a'; ch <= 'z'; ch++ {
		out = append(out, struct {
			name string
			ev   term.Event
			want rune
		}{fmt.Sprintf("C-%c", ch), ctrl(ch), ch - 'a' + 1})
	}
	return out
}

func TestEmacsQuotedInsertControlCodes(t *testing.T) {
	for _, tc := range asciiControlChords() {
		t.Run(tc.name, func(t *testing.T) {
			h, buf := newEmacsHandler(t, "")
			require.True(t, runEvent(h, ctrl('q')))
			require.True(t, runEvent(h, tc.ev))
			assert.Equal(t, string(tc.want), buf.String(),
				"C-q %s must insert %#x literally", tc.name, tc.want)
		})
	}
}

func TestEmacsQuotedInsertKeySpace(t *testing.T) {
	tests := []struct {
		name    string
		ev      term.Event
		want    string
		inserts bool
	}{
		{"printable ASCII", char('x'), "x", true},
		{"digit", char('7'), "7", true},
		{"space character", char(' '), " ", true},
		{"wide rune", char('界'), "界", true},
		{"emoji", char('😀'), "😀", true},
		{"combining mark", char('\u0301'), "\u0301", true},
		{"NUL via C-@", ctrl('@'), "\x00", true},
		{"backslash", char('\\'), "\\", true},
		{"angle bracket", char('<'), "<", true},

		{"TAB key", key(term.KeyTab), "\t", true},
		{"RET key", key(term.KeyEnter), "\n", true},
		{"SPACE key", key(term.KeySpace), " ", true},
		{"ESC key", key(term.KeyEsc), "\x1b", true},

		{"Meta chord has no literal form", alt('f'), "", false},
		{"Control-Meta chord has no literal form", ctrlAlt('f'), "", false},
		{"arrow key", key(term.KeyArrowRight), "", false},
		{"function key", key(term.KeyF5), "", false},
		{"home key", key(term.KeyHome), "", false},
		{"delete key", key(term.KeyDelete), "", false},
		{"backspace key", key(term.KeyBackspace), "", false},
		{"pgdn key", key(term.KeyPgdn), "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, buf := newEmacsHandler(t, "")
			require.True(t, runEvent(h, ctrl('q')))
			require.True(t, runEvent(h, tc.ev),
				"the quoted key must always be consumed")
			assert.Equal(t, tc.want, buf.String())
			// Either way the quoting state is closed, so the next key is a
			// normal command again.
			require.True(t, runEvent(h, char('Z')))
			assert.Equal(t, tc.want+"Z", buf.String())
		})
	}
}

func TestEmacsQuotedInsertContexts(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		at       term.Coordinates
		arrange  func(t *testing.T, h text.Handler)
		quoted   term.Event
		want     string
		wantCurs *term.Coordinates
	}{
		{name: "into an empty buffer", content: "", quoted: key(term.KeyTab), want: "\t"},
		{name: "at start of line", content: "ab", quoted: char('x'), want: "xab"},
		{name: "at end of line", content: "ab", at: term.Coordinates{X: 2}, quoted: char('x'), want: "abx"},
		{name: "mid line", content: "ab", at: term.Coordinates{X: 1}, quoted: char('x'), want: "axb"},

		{name: "before a null cell", content: "a\x00b", at: term.Coordinates{X: 1}, quoted: char('x'), want: "ax\x00b"},
		{name: "after a null cell", content: "a\x00b", at: term.Coordinates{X: 2}, quoted: char('x'), want: "a\x00xb"},
		{name: "into an all-null line", content: "\x00\x00", at: term.Coordinates{X: 1}, quoted: char('x'), want: "\x00x\x00"},
		{name: "quote a NUL next to nulls", content: "\x00\x00", at: term.Coordinates{X: 1}, quoted: ctrl('@'), want: "\x00\x00\x00"},

		{name: "before a wide rune", content: "界界", quoted: char('x'), want: "x界界"},
		{name: "between wide runes", content: "界界", at: term.Coordinates{X: 1}, quoted: char('x'), want: "界x界"},
		{name: "after wide runes", content: "界界", at: term.Coordinates{X: 2}, quoted: char('x'), want: "界界x"},
		{name: "wide rune between wide runes", content: "界界", at: term.Coordinates{X: 1}, quoted: char('世'), want: "界世界"},

		{name: "before a tab", content: "\tab", quoted: char('x'), want: "x\tab"},
		{name: "after a tab", content: "\tab", at: term.Coordinates{X: 1}, quoted: char('x'), want: "\txab"},
		{name: "quote a tab beside a tab", content: "\t", at: term.Coordinates{X: 1}, quoted: key(term.KeyTab), want: "\t\t"},

		{name: "newline splits the line", content: "ab", at: term.Coordinates{X: 1}, quoted: key(term.KeyEnter), want: "a\nb"},
		{name: "newline at end of buffer", content: "ab", at: term.Coordinates{X: 2}, quoted: key(term.KeyEnter), want: "ab\n"},
		{name: "on the last line of a multiline buffer", content: "a\nb", at: term.Coordinates{Y: 1, X: 1}, quoted: char('x'), want: "a\nbx"},

		{
			name: "replaces an active shift-selection", content: "abcd",
			arrange: func(t *testing.T, h text.Handler) {
				require.True(t, runEvent(h, key2(term.KeyArrowRight, term.ModShift)))
				require.True(t, runEvent(h, key2(term.KeyArrowRight, term.ModShift)))
			},
			quoted: char('x'), want: "xcd",
		},
		{
			name: "replaces a wide-rune selection", content: "界界x",
			arrange: func(t *testing.T, h text.Handler) {
				require.True(t, runEvent(h, key2(term.KeyArrowRight, term.ModShift)))
				require.True(t, runEvent(h, key2(term.KeyArrowRight, term.ModShift)))
			},
			quoted: key(term.KeyTab), want: "\tx",
		},
		{
			name: "caret past the last column", content: "abc",
			arrange: func(t *testing.T, h text.Handler) {
				h.SetCursorAtScroll(term.Coordinates{X: 99})
			},
			quoted: char('x'), want: "abcx",
		},
		{
			name: "caret past the last line", content: "a\nb",
			arrange: func(t *testing.T, h text.Handler) {
				h.SetCursorAtScroll(term.Coordinates{Y: 99})
			},
			quoted: char('x'), want: "a\nxb",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, buf := newEmacsHandler(t, tc.content)
			if tc.at != (term.Coordinates{}) {
				require.True(t, h.SetCursorAtScroll(tc.at))
			}
			if tc.arrange != nil {
				tc.arrange(t, h)
			}
			require.True(t, runEvent(h, ctrl('q')))
			require.True(t, runEvent(h, tc.quoted))
			assert.Equal(t, tc.want, buf.String())
			if tc.wantCurs != nil {
				assert.Equal(t, *tc.wantCurs, h.CursorAtScroll())
			}
		})
	}
}

func TestEmacsQuotedInsertCount(t *testing.T) {
	tests := []struct {
		name   string
		prefix []term.Event
		quoted term.Event
		want   string
	}{
		{"no argument inserts once", nil, char('x'), "x"},
		{"C-u alone inserts four", []term.Event{ctrl('u')}, char('x'), "xxxx"},
		{"C-u C-u inserts sixteen", []term.Event{ctrl('u'), ctrl('u')}, char('x'), strings.Repeat("x", 16)},
		{"explicit count", []term.Event{ctrl('u'), char('3')}, char('x'), "xxx"},
		{"explicit count of a control code", []term.Event{ctrl('u'), char('3')}, ctrl('i'), "\t\t\t"},
		{"explicit count of a newline", []term.Event{ctrl('u'), char('2')}, key(term.KeyEnter), "\n\n"},
		{"explicit count of a wide rune", []term.Event{ctrl('u'), char('3')}, char('界'), "界界界"},
		{"multi-digit count", []term.Event{ctrl('u'), char('1'), char('2')}, char('x'), strings.Repeat("x", 12)},
		{"meta digit argument", []term.Event{alt('3')}, char('x'), "xxx"},
		{"zero inserts nothing", []term.Event{ctrl('u'), char('0')}, char('x'), ""},
		{"negative inserts nothing", []term.Event{alt('-'), char('3')}, char('x'), ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, buf := newEmacsHandler(t, "")
			for _, ev := range tc.prefix {
				require.True(t, runEvent(h, ev))
			}
			require.True(t, runEvent(h, ctrl('q')))
			require.True(t, runEvent(h, tc.quoted))
			assert.Equal(t, tc.want, buf.String())
		})
	}
}

func TestEmacsQuotedInsertState(t *testing.T) {
	t.Run("consumes exactly one key", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "")
		require.True(t, runEvent(h, ctrl('q')))
		require.True(t, runEvent(h, key(term.KeyTab)))
		// A second TAB is an ordinary indent command again.
		require.True(t, runEvent(h, key(term.KeyTab)))
		assert.Equal(t, "\t\t", buf.String())
	})

	t.Run("the quoted key never runs its own command", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "ab")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 1}))
		require.True(t, runEvent(h, ctrl('q')))
		require.True(t, runEvent(h, ctrl('k')))
		assert.Equal(t, "a\x0bb", buf.String(), "C-k must not kill the line")
	})

	t.Run("quoting C-q inserts its own control code", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "")
		require.True(t, runEvent(h, ctrl('q')))
		require.True(t, runEvent(h, ctrl('q')))
		assert.Equal(t, "\x11", buf.String())
	})

	t.Run("a counted insertion reverts in one undo", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "")
		require.True(t, runEvent(h, ctrl('u')))
		require.True(t, runEvent(h, char('3')))
		require.True(t, runEvent(h, ctrl('q')))
		require.True(t, runEvent(h, char('x')))
		require.Equal(t, "xxx", buf.String())
		require.True(t, runEvent(h, ctrl('/')))
		assert.Equal(t, "", buf.String())
	})

	t.Run("a non-key event closes the quote without inserting", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "ab")
		require.True(t, runEvent(h, ctrl('q')))
		h.Handle(term.Event{Type: term.EventMouse, Key: term.MouseLeft})
		require.Equal(t, "ab", buf.String())
		// Quoting is closed, so the next key is a normal command again.
		require.True(t, runEvent(h, char('Z')))
		assert.Equal(t, "Zab", buf.String())
	})

	t.Run("survives pathological buffers without panicking", func(t *testing.T) {
		for _, content := range []string{"", " ", "\x00\x00", "界界", "\t\t", "a\n"} {
			h, _ := newEmacsHandler(t, content)
			h.SetCursorAtScroll(term.Coordinates{Y: 99, X: 99})
			require.NotPanicsf(t, func() {
				h.Handle(ctrl('q'))
				h.Handle(char('x'))
			}, "content %q", content)
		}
	})
}

func TestEmacsMarkDefun(t *testing.T) {
	tests := []struct {
		name    string
		content string
		at      term.Coordinates
		handled bool
		sel     string
		want    term.Coordinates
	}{
		{"parens", "(ab)", term.Coordinates{X: 2}, true, "(ab)", term.Coordinates{X: 4}},
		{"braces", "{ab}", term.Coordinates{X: 2}, true, "{ab}", term.Coordinates{X: 4}},
		{"brackets", "[ab]", term.Coordinates{X: 2}, true, "[ab]", term.Coordinates{X: 4}},
		{"innermost of nested same kind", "((a))", term.Coordinates{X: 2}, true, "(a)", term.Coordinates{X: 4}},
		{"innermost of nested mixed kinds", "{[a]}", term.Coordinates{X: 2}, true, "[a]", term.Coordinates{X: 4}},
		{"embedded in surrounding text", "x{ab}y", term.Coordinates{X: 3}, true, "{ab}", term.Coordinates{X: 5}},
		{"just inside the opener", "(ab)", term.Coordinates{X: 1}, true, "(ab)", term.Coordinates{X: 4}},
		{"on the closer", "(ab)", term.Coordinates{X: 3}, true, "(ab)", term.Coordinates{X: 4}},
		{"across a line boundary", "(a\nb)", term.Coordinates{Y: 1}, true, "(a\nb)", term.Coordinates{Y: 1, X: 2}},
		{"spanning several lines", "{\na\nb\n}", term.Coordinates{Y: 2}, true, "{\na\nb\n}", term.Coordinates{Y: 3, X: 1}},
		{"wide runes inside", "(界界)", term.Coordinates{X: 2}, true, "(界界)", term.Coordinates{X: 4}},
		{"null cells inside", "(a\x00b)", term.Coordinates{X: 2}, true, "(a\x00b)", term.Coordinates{X: 5}},
		{"tabs inside", "(\ta\t)", term.Coordinates{X: 2}, true, "(\ta\t)", term.Coordinates{X: 5}},

		// Rune's defun boundary is the enclosing bracket pair, so anything
		// that is not inside one is a no-op.
		{"outside any block", "abc", term.Coordinates{X: 1}, false, "", term.Coordinates{X: 1}},
		{"on the opener itself", "(ab)", term.Coordinates{}, false, "", term.Coordinates{}},
		{"unclosed bracket", "(ab", term.Coordinates{X: 2}, false, "", term.Coordinates{X: 2}},
		{"unopened bracket", "ab)", term.Coordinates{X: 1}, false, "", term.Coordinates{X: 1}},
		{"mismatched brackets", "(a]", term.Coordinates{X: 1}, false, "", term.Coordinates{X: 1}},
		{"empty buffer", "", term.Coordinates{}, false, "", term.Coordinates{}},
		{"only null cells", "\x00\x00", term.Coordinates{X: 1}, false, "", term.Coordinates{X: 1}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, buf := newEmacsHandler(t, tc.content)
			if tc.at != (term.Coordinates{}) {
				require.True(t, h.SetCursorAtScroll(tc.at))
			}
			var handled bool
			require.NotPanics(t, func() { _, handled = h.Handle(ctrlAlt('h')) })
			assert.Equal(t, tc.handled, handled)
			assert.Equal(t, tc.content, buf.String(), "mark-defun must not edit")
			sel, ok := h.Selection()
			assert.Equal(t, tc.sel != "", ok)
			assert.Equal(t, tc.sel, sel)
			assert.Equal(t, tc.want, h.CursorAtScroll())
		})
	}

	t.Run("the marked region is copyable with M-w", func(t *testing.T) {
		h, _, clip := newEmacsHandlerWithClipboard(t, "x{界a}y")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 3}))
		require.True(t, runEvent(h, ctrlAlt('h')))
		require.True(t, runEvent(h, alt('w')))
		assert.Equal(t, "{界a}", killRingText(clip))
	})

	t.Run("the marked region is killable with C-w", func(t *testing.T) {
		h, buf, clip := newEmacsHandlerWithClipboard(t, "x{ab}y")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 3}))
		require.True(t, runEvent(h, ctrlAlt('h')))
		require.True(t, runEvent(h, ctrl('w')))
		assert.Equal(t, "xy", buf.String())
		assert.Equal(t, "{ab}", killRingText(clip))
	})

	t.Run("pushes a mark point can return to", func(t *testing.T) {
		h, _ := newEmacsHandler(t, "x{ab}y")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 3}))
		before := emacsMarks(h)
		require.True(t, runEvent(h, ctrlAlt('h')))
		assert.Len(t, emacsMarks(h), len(before)+1)
	})

	t.Run("survives pathological carets without panicking", func(t *testing.T) {
		for _, content := range []string{"", "(", ")", "()", "\x00\x00", "界界", "(\n)"} {
			h, _ := newEmacsHandler(t, content)
			h.SetCursorAtScroll(term.Coordinates{Y: 99, X: 99})
			require.NotPanicsf(t, func() { h.Handle(ctrlAlt('h')) }, "content %q", content)
		}
	})
}

func TestEmacsAsciiControlFolding(t *testing.T) {
	tests := []struct {
		name    string
		content string
		at      term.Coordinates
		arrange func(t *testing.T, h text.Handler)
		folded  term.Event
		bare    term.Event
	}{
		{name: "newline in an empty buffer", content: "", folded: ctrl('m'), bare: key(term.KeyEnter)},
		{name: "newline splits a line", content: "ab", at: term.Coordinates{X: 1}, folded: ctrl('m'), bare: key(term.KeyEnter)},
		{name: "newline at end of line", content: "ab", at: term.Coordinates{X: 2}, folded: ctrl('m'), bare: key(term.KeyEnter)},
		{name: "newline between wide runes", content: "界界", at: term.Coordinates{X: 1}, folded: ctrl('m'), bare: key(term.KeyEnter)},
		{name: "newline beside null cells", content: "a\x00b", at: term.Coordinates{X: 2}, folded: ctrl('m'), bare: key(term.KeyEnter)},
		{name: "newline after a tab", content: "\tab", at: term.Coordinates{X: 1}, folded: ctrl('m'), bare: key(term.KeyEnter)},
		{name: "newline on an indented line", content: "\t\tab", at: term.Coordinates{X: 3}, folded: ctrl('m'), bare: key(term.KeyEnter)},

		{name: "indent an empty buffer", content: "", folded: ctrl('i'), bare: key(term.KeyTab)},
		{name: "indent at start of line", content: "ab", folded: ctrl('i'), bare: key(term.KeyTab)},
		{name: "indent mid line", content: "ab", at: term.Coordinates{X: 1}, folded: ctrl('i'), bare: key(term.KeyTab)},
		{name: "indent a wide-rune line", content: "界界", at: term.Coordinates{X: 1}, folded: ctrl('i'), bare: key(term.KeyTab)},
		{name: "indent a null-cell line", content: "\x00\x00", at: term.Coordinates{X: 1}, folded: ctrl('i'), bare: key(term.KeyTab)},
		{
			name: "indent an active selection", content: "ab\ncd",
			arrange: func(t *testing.T, h text.Handler) {
				require.True(t, runEvent(h, key2(term.KeyArrowDown, term.ModShift)))
			},
			folded: ctrl('i'), bare: key(term.KeyTab),
		},
		{
			name: "newline replaces an active selection", content: "abcd",
			arrange: func(t *testing.T, h text.Handler) {
				require.True(t, runEvent(h, key2(term.KeyArrowRight, term.ModShift)))
				require.True(t, runEvent(h, key2(term.KeyArrowRight, term.ModShift)))
			},
			folded: ctrl('m'), bare: key(term.KeyEnter),
		},
	}

	run := func(t *testing.T, tc struct {
		name    string
		content string
		at      term.Coordinates
		arrange func(t *testing.T, h text.Handler)
		folded  term.Event
		bare    term.Event
	}, ev term.Event) (string, term.Coordinates, bool) {
		t.Helper()
		h, buf := newEmacsHandler(t, tc.content)
		if tc.at != (term.Coordinates{}) {
			require.True(t, h.SetCursorAtScroll(tc.at))
		}
		if tc.arrange != nil {
			tc.arrange(t, h)
		}
		handled := runEvent(h, ev)
		return buf.String(), h.CursorAtScroll(), handled
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotBuf, gotCur, gotOK := run(t, tc, tc.folded)
			wantBuf, wantCur, wantOK := run(t, tc, tc.bare)
			assert.Equal(t, wantBuf, gotBuf, "buffer must match the bare key")
			assert.Equal(t, wantCur, gotCur, "cursor must match the bare key")
			assert.Equal(t, wantOK, gotOK, "handled must match the bare key")
		})
	}

	t.Run("only C-m and C-i fold", func(t *testing.T) {
		// C-j stays newline-and-indent and C-h stays backspace: neither is
		// rewritten into a bare key, so they keep their own semantics.
		h, buf := newEmacsHandler(t, "ab")
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 1}))
		require.True(t, runEvent(h, ctrl('h')))
		assert.Equal(t, "b", buf.String())

		h2, buf2 := newEmacsHandler(t, "ab")
		require.True(t, h2.SetCursorAtScroll(term.Coordinates{X: 1}))
		require.True(t, runEvent(h2, ctrl('j')))
		assert.Equal(t, "a\nb", buf2.String())
	})

	t.Run("folding respects auto-pair like RET", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "{}", WithAutoPair(true))
		require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 1}))
		require.True(t, runEvent(h, ctrl('m')))

		h2, buf2 := newEmacsHandler(t, "{}", WithAutoPair(true))
		require.True(t, h2.SetCursorAtScroll(term.Coordinates{X: 1}))
		require.True(t, runEvent(h2, key(term.KeyEnter)))

		assert.Equal(t, buf2.String(), buf.String())
	})
}

func TestEmacsCtrlShiftNormalization(t *testing.T) {
	type outcome struct {
		handled bool
		buf     string
		cursor  term.Coordinates
		sel     string
		hasSel  bool
	}

	apply := func(t *testing.T, content string, at term.Coordinates,
		arrange func(t *testing.T, h text.Handler), ev term.Event) outcome {
		t.Helper()
		h, buf := newEmacsHandler(t, content)
		if at != (term.Coordinates{}) {
			require.True(t, h.SetCursorAtScroll(at))
		}
		if arrange != nil {
			arrange(t, h)
		}
		var handled bool
		require.NotPanicsf(t, func() { _, handled = h.Handle(ev) }, "event %+v", ev)
		sel, hasSel := h.Selection()
		return outcome{handled, buf.String(), h.CursorAtScroll(), sel, hasSel}
	}

	selectChars := func(t *testing.T, h text.Handler) {
		require.True(t, runEvent(h, key2(term.KeyArrowRight, term.ModShift)))
		require.True(t, runEvent(h, key2(term.KeyArrowRight, term.ModShift)))
	}
	selectLine := func(t *testing.T, h text.Handler) {
		require.True(t, runEvent(h, key2(term.KeyArrowDown, term.ModShift)))
	}
	hideLine := func(t *testing.T, h text.Handler) {
		selectLine(t, h)
		require.True(t, runEvent(h,
			term.Event{Type: term.EventKey, Mod: term.ModCtrlShift, Ch: 'H'}))
	}

	tests := []struct {
		name    string
		letter  rune
		content string
		at      term.Coordinates
		arrange func(t *testing.T, h text.Handler)
	}{
		{name: "C-S-M select enclosing block", letter: 'M', content: "(ab)", at: term.Coordinates{X: 2}},
		{name: "C-S-M outside a block", letter: 'M', content: "abc", at: term.Coordinates{X: 1}},
		{name: "C-S-M over wide runes", letter: 'M', content: "(界界)", at: term.Coordinates{X: 2}},
		{name: "C-S-W shrink selection", letter: 'W', content: "alpha beta", at: term.Coordinates{X: 6}, arrange: selectChars},
		{name: "C-S-H hide a selected line", letter: 'H', content: "ab\ncd\nef", arrange: selectLine},
		{name: "C-S-H over wide runes", letter: 'H', content: "界界\ncd", arrange: selectLine},
		{name: "C-S-H over null cells", letter: 'H', content: "\x00\x00\ncd", arrange: selectLine},
		{name: "C-S-H with no selection", letter: 'H', content: "ab"},
		{name: "C-S-V reveals what C-S-H hid", letter: 'V', content: "ab\ncd\nef", arrange: hideLine},
		{name: "C-S-V with nothing hidden", letter: 'V', content: "ab\ncd"},
		// C-S-Z and C-S-A drive syntax folds, which need fold information
		// this harness does not provide; their dispatch is covered by the
		// allEmacsBindings no-op sweep instead.
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			viaCtrl := apply(t, tc.content, tc.at, tc.arrange,
				term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: tc.letter})
			viaCtrlShift := apply(t, tc.content, tc.at, tc.arrange,
				term.Event{Type: term.EventKey, Mod: term.ModCtrlShift, Ch: tc.letter})
			assert.Equal(t, viaCtrlShift, viaCtrl,
				"ModCtrl with a shifted glyph must reach the same binding as ModCtrlShift")
		})
	}

	t.Run("C-S-DEL kills the whole line", func(t *testing.T) {
		h, buf := newEmacsHandler(t, "ab\ncd")
		require.True(t, runEvent(h, key2(term.KeyBackspace, term.ModCtrlShift)))
		assert.Equal(t, "cd", buf.String())
	})

	t.Run("lower-case Control chords keep their own bindings", func(t *testing.T) {
		// C-w is kill-region, not the C-S-W shrink-selection binding, and
		// C-a is start-of-line, not the C-S-A fold toggle.
		h, buf, clip := newEmacsHandlerWithClipboard(t, "abcd")
		require.True(t, runEvent(h, key2(term.KeyArrowRight, term.ModShift)))
		require.True(t, runEvent(h, key2(term.KeyArrowRight, term.ModShift)))
		require.True(t, runEvent(h, ctrl('w')))
		assert.Equal(t, "cd", buf.String())
		assert.Equal(t, "ab", killRingText(clip))

		h2, _ := newEmacsHandler(t, "abcd")
		require.True(t, h2.SetCursorAtScroll(term.Coordinates{X: 3}))
		require.True(t, runEvent(h2, ctrl('a')))
		assert.Equal(t, term.Coordinates{}, h2.CursorAtScroll())
	})

	t.Run("non-letter Control chords are not folded into ctrl-shift", func(t *testing.T) {
		// C-/ and C-_ must stay undo even though they sit outside the A-Z
		// range the normalization inspects.
		for _, ev := range []term.Event{ctrl('/'), ctrl('_')} {
			h, buf := newEmacsHandler(t, "ab")
			require.True(t, h.SetCursorAtScroll(term.Coordinates{X: 2}))
			require.True(t, runEvent(h, char('X')))
			require.Equal(t, "abX", buf.String())
			require.True(t, runEvent(h, ev))
			assert.Equal(t, "ab", buf.String())
		}
	})
}

func TestEmacsRegionSources(t *testing.T) {
	// markThen sets a mark at the caret then applies the motions.
	markThen := func(motions ...term.Event) func(t *testing.T, h text.Handler) {
		return func(t *testing.T, h text.Handler) {
			require.True(t, runEvent(h, key2(term.KeySpace, term.ModCtrl)))
			for _, m := range motions {
				require.True(t, runEvent(h, m))
			}
		}
	}
	shiftThen := func(motions ...term.Event) func(t *testing.T, h text.Handler) {
		return func(t *testing.T, h text.Handler) {
			for _, m := range motions {
				require.True(t, runEvent(h, m))
			}
		}
	}

	right := key(term.KeyArrowRight)
	left := key(term.KeyArrowLeft)
	sRight := key2(term.KeyArrowRight, term.ModShift)
	sLeft := key2(term.KeyArrowLeft, term.ModShift)

	tests := []struct {
		name     string
		content  string
		at       term.Coordinates
		arrange  func(t *testing.T, h text.Handler)
		wantCopy string
		wantKill string
		wantBuf  string
	}{
		{
			name: "mark then forward motion", content: "abcd",
			arrange:  markThen(right, right),
			wantCopy: "ab", wantKill: "ab", wantBuf: "cd",
		},
		{
			name: "mark then backward motion", content: "abcd", at: term.Coordinates{X: 3},
			arrange:  markThen(left, left),
			wantCopy: "bc", wantKill: "bc", wantBuf: "ad",
		},
		{
			name: "forward shift-selection", content: "abcd",
			arrange:  shiftThen(sRight, sRight),
			wantCopy: "ab", wantKill: "ab", wantBuf: "cd",
		},
		{
			name: "backward shift-selection", content: "abcd", at: term.Coordinates{X: 3},
			arrange:  shiftThen(sLeft, sLeft),
			wantCopy: "bc", wantKill: "bc", wantBuf: "ad",
		},
		{
			name: "shift-selection over wide runes", content: "界界x",
			arrange:  shiftThen(sRight, sRight),
			wantCopy: "界界", wantKill: "界界", wantBuf: "x",
		},
		{
			name: "shift-selection over null cells", content: "a\x00b",
			arrange:  shiftThen(sRight, sRight),
			wantCopy: "a\x00", wantKill: "a\x00", wantBuf: "b",
		},
		{
			name: "shift-selection over a tab", content: "\tab",
			arrange:  shiftThen(sRight, sRight),
			wantCopy: "\ta", wantKill: "\ta", wantBuf: "b",
		},
		{
			name: "multiline shift-selection", content: "ab\ncd",
			arrange:  shiftThen(key2(term.KeyArrowDown, term.ModShift)),
			wantCopy: "ab\n", wantKill: "ab\n", wantBuf: "cd",
		},
		{
			name: "mark spanning lines", content: "ab\ncd",
			arrange:  markThen(key(term.KeyArrowDown)),
			wantCopy: "ab\n", wantKill: "ab\n", wantBuf: "cd",
		},
		{
			name: "no mark and no selection", content: "abcd",
			wantCopy: "", wantKill: "", wantBuf: "abcd",
		},
		{
			name: "mark at point is an empty region", content: "abcd",
			arrange:  markThen(),
			wantCopy: "", wantKill: "", wantBuf: "abcd",
		},
		{
			name: "empty buffer", content: "",
			arrange:  markThen(),
			wantCopy: "", wantKill: "", wantBuf: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name+"/M-w", func(t *testing.T) {
			h, buf, clip := newEmacsHandlerWithClipboard(t, tc.content)
			if tc.at != (term.Coordinates{}) {
				require.True(t, h.SetCursorAtScroll(tc.at))
			}
			if tc.arrange != nil {
				tc.arrange(t, h)
			}
			at := h.CursorAtScroll()
			require.NotPanics(t, func() { h.Handle(alt('w')) })
			assert.Equal(t, tc.wantCopy, killRingText(clip))
			assert.Equal(t, tc.content, buf.String(), "M-w must not edit")
			assert.Equal(t, at, h.CursorAtScroll(), "M-w must leave point alone")
		})

		t.Run(tc.name+"/C-w", func(t *testing.T) {
			h, buf, clip := newEmacsHandlerWithClipboard(t, tc.content)
			if tc.at != (term.Coordinates{}) {
				require.True(t, h.SetCursorAtScroll(tc.at))
			}
			if tc.arrange != nil {
				tc.arrange(t, h)
			}
			require.NotPanics(t, func() { h.Handle(ctrl('w')) })
			assert.Equal(t, tc.wantKill, killRingText(clip))
			assert.Equal(t, tc.wantBuf, buf.String())
		})
	}

	t.Run("M-w deactivates the region", func(t *testing.T) {
		h, _, _ := newEmacsHandlerWithClipboard(t, "abcd")
		require.True(t, runEvent(h, key2(term.KeyArrowRight, term.ModShift)))
		require.True(t, runEvent(h, alt('w')))
		_, ok := h.Selection()
		assert.False(t, ok, "GNU M-w deactivates the mark")
	})

	t.Run("a shift-selection wins over a stale mark", func(t *testing.T) {
		h, _, clip := newEmacsHandlerWithClipboard(t, "abcdef")
		// Mark at the start, then move away and shift-select elsewhere.
		require.True(t, runEvent(h, key2(term.KeySpace, term.ModCtrl)))
		for range 4 {
			require.True(t, runEvent(h, key(term.KeyArrowRight)))
		}
		require.True(t, runEvent(h, key2(term.KeyArrowRight, term.ModShift)))
		require.True(t, runEvent(h, alt('w')))
		assert.Equal(t, "e", killRingText(clip))
	})

	t.Run("survives pathological carets without panicking", func(t *testing.T) {
		for _, content := range []string{"", " ", "\x00\x00", "界界", "\t\t", "a\n"} {
			for _, ev := range []term.Event{alt('w'), ctrl('w')} {
				h, _ := newEmacsHandler(t, content)
				h.Handle(key2(term.KeySpace, term.ModCtrl))
				h.SetCursorAtScroll(term.Coordinates{Y: 99, X: 99})
				require.NotPanicsf(t, func() { h.Handle(ev) },
					"content %q event %+v", content, ev)
			}
		}
	})
}

func TestHandleMouseWindowCoordinates(t *testing.T) {
	uri, err := workspaceapi.ParseURI("test:///")
	require.NoError(t, err)
	newEditor := func() text.Handler {
		buf := cell.NewBuffer()
		buf.ReadFrom(strings.NewReader("alpha bravo charlie"))
		h := NewHandler(buf, uri, text.IndentRuneTab, 0,
			WithCommandBar(false), WithWrap(false))
		h.Resize(8, 1)
		return h
	}

	t.Run("drag past the viewport edge selects the mapped columns", func(t *testing.T) {
		h := newEditor()
		h.Handle(mouseEventAt(term.MouseLeft, 6, 0))
		h.Handle(mouseEventAt(term.MouseLeft, 11, 0))
		h.Handle(mouseEventAt(term.MouseRelease, 11, 0))

		assert.Equal(t, term.Coordinates{X: 11}, h.CursorAtScroll())
		sel, ok := h.Selection()
		require.True(t, ok)
		assert.Equal(t, "bravo", sel)
	})

	t.Run("leftward drag reports sorted half-open bounds", func(t *testing.T) {
		h := newEditor()
		h.Handle(mouseEventAt(term.MouseLeft, 7, 0))
		h.Handle(mouseEventAt(term.MouseLeft, 2, 0))
		h.Handle(mouseEventAt(term.MouseRelease, 2, 0))

		sel, ok := h.Selection()
		require.True(t, ok)
		assert.Equal(t, "pha b", sel)
		sb, sbok := h.(interface {
			SelectionBounds() (term.Coordinates, term.Coordinates, bool)
		})
		require.True(t, sbok)
		from, to, bok := sb.SelectionBounds()
		require.True(t, bok)
		assert.Equal(t, term.Coordinates{X: 2}, from)
		assert.Equal(t, term.Coordinates{X: 7}, to,
			"the anchor cell sits one past the exclusive selection")
	})

	t.Run("click on a horizontally scrolled viewport follows the offset", func(t *testing.T) {
		h := newEditor()
		h.Handle(term.Event{Type: term.EventKey, Ch: 'e', Mod: term.ModCtrl})
		win, _, ok := h.Cursor()
		require.True(t, ok)
		offset := h.CursorAtScroll().X - win.X
		require.Positive(t, offset, "the viewport must be scrolled right")

		h.Handle(mouseEventAt(term.MouseLeft, 2, 0))
		h.Handle(mouseEventAt(term.MouseRelease, 2, 0))

		assert.Equal(t, term.Coordinates{X: offset + 2}, h.CursorAtScroll())
	})
}

func mouseEventAt(key term.Key, x, y int) term.Event {
	return term.Event{Type: term.EventMouse, Key: key, MouseX: x, MouseY: y}
}

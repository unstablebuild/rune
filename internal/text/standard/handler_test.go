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

package standard

import (
	"context"
	"errors"
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
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/registerhistory"
	"unstable.build/rune/internal/text/registerset"
	"unstable.build/rune/internal/text/texttest"
)

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

// SelectionExpand lets the standard handler tests exercise wiring that
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

func TestStandardCursorCorrections(t *testing.T) {
	tests := []struct {
		name string
		text string
		keys string
		want term.Coordinates
	}{
		{
			name: "trailing null cells",
			text: "abc\x00\x00\x00",
			keys: "<end>",
			want: term.Coordinates{X: 6},
		},
		{
			name: "short line",
			text: "longline\nx\nlongline",
			keys: "<end><down>",
			want: term.Coordinates{X: 1, Y: 1},
		},
		{
			name: "sticky desired column",
			text: "longline\nx\nlongline",
			keys: "<end><down><down>",
			want: term.Coordinates{X: 8, Y: 2},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			uri, err := workspaceapi.ParseURI("test:///cursor-corrections")
			require.NoError(t, err)
			buf := cell.NewBuffer()
			buf.ReadFrom(strings.NewReader(tt.text))
			h := NewHandler(buf, uri, text.IndentRuneTab, 0)
			h.Resize(20, 10)

			feedKeys(t, h, tt.keys)

			assert.Equal(t, tt.want, h.CursorAtScroll())
			pos, _, show := h.Cursor()
			assert.True(t, show)
			assert.Equal(t, tt.want, pos)
		})
	}
}

func TestStandardCursorCorrectionsDisabled(t *testing.T) {
	uri, err := workspaceapi.ParseURI("test:///cursor-corrections-disabled")
	require.NoError(t, err)
	buf := cell.NewBuffer()
	buf.ReadFrom(strings.NewReader("longline\nx\nlongline"))
	h := NewHandler(buf, uri, text.IndentRuneTab, 0, WithCursorCorrections(false))
	h.Resize(20, 10)

	feedKeys(t, h, "<end><down>")

	want := term.Coordinates{X: 8, Y: 1}
	assert.Equal(t, want, h.CursorAtScroll())
	pos, _, show := h.Cursor()
	assert.True(t, show)
	assert.Equal(t, want, pos)
}

func TestStandardMoveToBoundsReportsCorrection(t *testing.T) {
	uri, err := workspaceapi.ParseURI("test:///move-to-bounds-result")
	require.NoError(t, err)
	buf := cell.NewBuffer()
	buf.ReadFrom(strings.NewReader("longline\nx"))
	h := NewHandler(
		buf, uri, text.IndentRuneTab, 0, WithCursorCorrections(false),
	).(*standardHandler)
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

func TestStandardMoveToBoundsBoundaryMatrix(t *testing.T) {
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
			uri, err := workspaceapi.ParseURI("test:///move-to-bounds-matrix")
			require.NoError(t, err)
			buf := cell.NewBuffer()
			_, err = buf.ReadFrom(strings.NewReader(tt.content))
			require.NoError(t, err)
			h := NewHandler(buf, uri, text.IndentRuneTab, 0).(*standardHandler)
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

func TestStandardMoveToBoundsCoordinateContentSweep(t *testing.T) {
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
			uri, err := workspaceapi.ParseURI("test:///move-to-bounds-sweep")
			require.NoError(t, err)
			buf := cell.NewBuffer()
			_, err = buf.ReadFrom(strings.NewReader(content.text))
			require.NoError(t, err)
			h := NewHandler(buf, uri, text.IndentRuneTab, 0).(*standardHandler)
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

func TestStandardCursorCorrectionsAfterExternalEdit(t *testing.T) {
	uri, err := workspaceapi.ParseURI("test:///cursor-corrections-external-edit")
	require.NoError(t, err)
	buf := cell.NewBuffer()
	h := NewHandler(buf, uri, text.IndentRuneTab, 0, WithKeymap(KeymapMacOS))
	h.Resize(20, 10)
	h.CellEditor().Edit(
		context.Background(), term.Coordinates{}, term.Coordinates{},
		"abcdefghi\n1234\nXXXX\nX\nX\nX\nX\nX\nX",
	)
	require.Equal(t, term.Coordinates{X: 1, Y: 8}, h.CursorAtScroll())

	_, handled := h.Handle(term.Event{
		Type: term.EventKey,
		Mod:  term.ModMeta,
		Key:  term.KeyArrowUp,
	})

	require.True(t, handled)
	assert.Equal(t, term.Coordinates{X: 1}, h.CursorAtScroll())
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

	_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'w'})
	require.True(t, handled)
	selection, ok := h.Selection()
	require.True(t, ok)
	assert.Equal(t, "beta", selection)

	_, handled = h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'w'})
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

// sublimeCase is one Sublime Text shortcut replayed against the standard
// editor over sublimeSnippet.
type sublimeCase struct {
	description string
	keycomb     string
	result      *string
	coordinates term.Coordinates
	clipboard   *string
}

const sublimeSnippet = "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"

func sublimeSnippetPlus(add string) *string {
	return new(sublimeSnippet + add)
}

func TestSublimeKeyBindingsMacOS(t *testing.T) {
	spAll := sublimeSnippetPlus
	suite := []sublimeCase{
		// General editing
		{"Cut (cuts entire line when nothing selected)", "<meta-x>", new("b\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 0}, nil},
		{"Copy+Paste", "<shift-right><meta-c><meta-v>", new("aa\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 1}, nil},
		{"Copy+Paste and indent correctly", "<shift-right><meta-c><down><shift-meta-v>", new("a\nab\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 1, X: 1}, nil},
		{"Paste from clipboard history", "<shift-right><meta-c><meta-v><meta-v><alt-meta-v>", new("aaa\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 2}, nil},
		{"Undo", "<meta-x><meta-z>", new("a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 0}, nil},
		{"Redo", "<meta-x><meta-z><shift-meta-z>", new("b\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 0}, nil},
		{"Redo or repeat last command", "<meta-x><meta-z><meta-y>", new("b\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 0}, nil},
		// {"Soft undo (undo cursor movement without undoing edit)", "<right><right><meta-u>", nil, term.Coordinates{Y: 0, X: 0}},
		// {"Soft redo", "<right><right><meta-u><shift-meta-u>", nil, term.Coordinates{Y: 0, X: 1}},
		// {"Trigger auto-complete", "<ctrl-space>", nil, term.Coordinates{}},
		{"Insert completion/snippet or indent", "<tab>", new("\ta\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 1}, nil},
		{"Previous snippet field or unindent", "<tab><shift-tab>", new("a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 0}, nil},

		// Line manipulation
		{"Insert line after current line", "<ctrl-enter>", new("a\n\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 1, X: 0}, nil},
		{"Insert line before current line", "<ctrl-shift-enter>", new("\na\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 0}, nil},
		{"Move line/selection up", "<down><alt-up>", new("b\na\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 0}, nil},
		{"Move line/selection down", "<alt-down>", new("b\na\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 1, X: 0}, nil},
		{"Duplicate line(s)", "<shift-meta-d>", new("a\na\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 1, X: 0}, nil},
		{"Delete entire line", "<ctrl-shift-k>", new("b\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 0}, nil},
		{"Join line below to end of current line", "<meta-j>", new("ab\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 1}, nil},
		{"Indent current line(s)", "<meta-]>", new("\ta\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 1}, nil},
		{"Unindent current line(s)", "<meta-]><meta-[>", new("a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 0}, nil},
		{"Indent current line(s) with Ctrl", "<ctrl-]>", new("\ta\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 1}, nil},
		{"Unindent current line(s) with Ctrl", "<ctrl-]><ctrl-[>", new("a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 0}, nil},
		{"Delete from cursor to end of line", "<meta-k><meta-k>", new("\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 0}, nil},
		{"Delete to beginning of line", "<right><meta-k><meta-backspace>", new("\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 0}, nil},
		{"Set mark at cursor position", "<meta-k><meta-space>", nil, term.Coordinates{}, nil},
		{"Select from cursor to mark", "<meta-k><meta-space><down><down><meta-k><meta-a><m-c>", nil, term.Coordinates{Y: 0, X: 0}, new("a\nb\n")},
		{"Delete from cursor to mark", "<meta-k><meta-space><down><down><meta-k><meta-w>", new("c\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 0}, nil},
		{"Swap cursor position with mark", "<meta-k><meta-space><down><down><meta-k><meta-x>", nil, term.Coordinates{Y: 0, X: 0}, nil},
		{"Clear mark", "<meta-k><meta-space><meta-k><meta-g>", nil, term.Coordinates{}, nil},
		{"Delete to end of line", "<meta-delete>", new("\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 0}, nil},
		//{"Sort lines alphabetically", "<f5>", nil, term.Coordinates{}}, // Already sorted a-k
		//{"Sort lines (case sensitive)", "<ctrl-f5>", nil, term.Coordinates{}},

		// Comments - depends on language/syntax (assuming C-style)
		{"Toggle line comment", "<meta-/>", new("// a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 3}, nil},
		{"Toggle line comment with Ctrl", "<ctrl-/>", new("// a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 3}, nil},
		{"Toggle block comment", "<shift-right><alt-meta-/>", new("/*a*/\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 2}, nil},

		// Text transformation - require selection
		{"Transform selection to UPPERCASE", "<shift-right><meta-k><meta-u>", new("A\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 1}, nil},
		{"Transform selection to lowercase", "<shift-right><meta-k><meta-u><home><shift-right><meta-k><meta-l>", new("a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 1}, nil},
		{"Wrap paragraph at ruler", "<alt-meta-q>", new("alpha beta\ngamma delta\nepsilon zeta\neta theta\n\ni\nj\nk"), term.Coordinates{Y: 0, X: 0}, nil},
		//{"Wrap selection in HTML tag", "<shift-right><ctrl-shift-w>", sp("<p>a</p>\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 3}},
		//{"Close current HTML/XML tag", "<alt-meta-.>", nil, term.Coordinates{}}, // No open tag

		// Selection
		{"Select all", "<meta-a><m-c>", nil, term.Coordinates{}, spAll("\n")},
		{"Select entire line (repeat to select additional lines)", "<down><meta-l><meta-l><m-c>", nil, term.Coordinates{Y: 1, X: 0}, new("b\nc\n")},
		{"Select word at cursor (repeat to select next occurrence)", "<meta-down>a<enter><meta-up><meta-d><meta-d><meta-d><meta-c>", nil, term.Coordinates{}, new("a")},
		//{"Select word at cursor (repeat to select next occurrence, multi cursor edits all)", "<meta-down>a<meta-up><meta-d><meta-c>", nil, term.Coordinates{Y: 10, X: 0}, sp("a")},
		//{"Skip current selection, find and select next occurrence", "<meta-d><meta-k><meta-d>", nil, term.Coordinates{X: 0}, sp("a")},
		//{"Select all occurrences of current selection", "<meta-d><ctrl-meta-g>", nil, term.Coordinates{}},
		//{"Split selection into multiple cursors (one per line)", "<meta-a><shift-meta-l>", nil, term.Coordinates{}},
		//{"Add cursor on previous line (column selection up)", "<down><ctrl-shift-up>", nil, term.Coordinates{Y: 0, X: 0}},
		//{"Add cursor on next line (column selection down)", "<ctrl-shift-down>", nil, term.Coordinates{Y: 1, X: 0}},
		//{"Add cursor at click location", "<meta-click>", nil, term.Coordinates{}},
		//{"Exit multiple selections (single selection mode)", "<ctrl-shift-down><escape>", nil, term.Coordinates{Y: 0, X: 0}},

		// Expand selection
		{"Expand selection to brackets", "{abc}<left><left><shift-left><ctrl-shift-m>", nil, term.Coordinates{X: 4}, nil},
		//{"Expand selection to HTML/XML tag", "<shift-meta-a>", nil, term.Coordinates{}}, // No tags
		{"Expand selection to scope", "<shift-meta-space>", nil, term.Coordinates{}, nil},
		{"Expand selection to indentation level", "<shift-meta-j><m-c>", nil, term.Coordinates{}, spAll("\n")},

		// Navigation and movement
		{"Move to start of line", "<space><right><meta-left>", nil, term.Coordinates{Y: 0, X: 0}, nil},
		{"Move to end of line", "<meta-right>", nil, term.Coordinates{Y: 0, X: 1}, nil},
		{"Jump to matching bracket", "{}<left><left><ctrl-m>", nil, term.Coordinates{X: 1}, nil},
		{"Move to start of file", "<down><down><meta-up>", nil, term.Coordinates{Y: 0, X: 0}, nil},
		{"Move to end of file", "<meta-down>", nil, term.Coordinates{Y: 10, X: 0}, nil},
		//{"Jump back (previous location)", "<meta-down><ctrl-->", nil, term.Coordinates{Y: 0, X: 0}, nil},
		//{"Jump forward (next location)", "<meta-down><ctrl--><ctrl-shift-->", nil, term.Coordinates{Y: 10, X: 1}, nil},

		// Scrolling
		{"Center current line in view", "<meta-down>z<enter>z<enter>z<enter>z<enter>z<enter>z<enter><up><up><up><up><ctrl-l>",
			nil, term.Coordinates{Y: 12}, nil},
		{"Scroll view up one line", "<meta-down><up><ctrl-alt-up>", nil, term.Coordinates{Y: 9}, nil},
		{"Scroll view down one line", "<ctrl-alt-down>", nil, term.Coordinates{Y: 1}, nil},

		// Search and replace
		{"Find", "<meta-f>", nil, term.Coordinates{}, nil},
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

		// Marks (advanced bookmarking)
		//{"Set mark at cursor position", "<meta-k><meta-space>", nil, term.Coordinates{}},
		//{"Select from cursor to mark", "<meta-k><meta-space><down><down><meta-k><meta-a>", nil, term.Coordinates{Y: 0, X: 0}},
		//{"Delete from cursor to mark", "<meta-k><meta-space><down><down><meta-k><meta-w>", sp("c\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 0}},
		//{"Swap cursor position with mark", "<meta-k><meta-space><down><down><meta-k><meta-x>", nil, term.Coordinates{Y: 0, X: 0}},
		//{"Clear mark", "<meta-k><meta-space><meta-k><meta-g>", nil, term.Coordinates{}},

		// Macros
		{"Start/stop recording macro", "<ctrl-q>", nil, term.Coordinates{}, nil},
		{"Playback recorded macro", "<ctrl-q><right><ctrl-q><ctrl-shift-q>", nil, term.Coordinates{Y: 0, X: 1}, nil},

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

	runSublimeSuite(t, KeymapMacOS, suite)
}

// runSublimeSuite replays each case on a fresh handler using keymap and
// requires every key of the case to be handled.
func runSublimeSuite(t *testing.T, keymap Keymap, suite []sublimeCase) {
	t.Helper()
	uri, err := workspaceapi.ParseURI("memory:///myfile.go")
	require.NoError(t, err)

	for _, test := range suite {
		t.Run(test.description, func(t *testing.T) {
			seq, err := term.ParseKeys(test.keycomb)
			require.NoError(t, err)

			clip := clipboard.NewInMemory()
			reg := registerhistory.NewClipboard(registerset.New(clip))
			content := sublimeSnippet
			if test.description == "Wrap paragraph at ruler" {
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
				WithKeymap(keymap),
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

// TestSublimeKeyBindingsLinux replays the Sublime Text for Linux shortcuts:
// editing lives on <ctrl>, with <ctrl-k> as the chord prefix, because every
// <meta> chord belongs to Rune's command layer.
func TestSublimeKeyBindingsLinux(t *testing.T) {
	spAll := sublimeSnippetPlus
	runSublimeSuite(t, KeymapLinux, []sublimeCase{
		// General editing
		{"Cut (cuts entire line when nothing selected)", "<ctrl-x>", new("b\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 0}, nil},
		{"Copy+Paste", "<shift-right><ctrl-c><ctrl-v>", new("aa\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 1}, nil},
		{"Copy+Paste and indent correctly", "<shift-right><ctrl-c><down><ctrl-shift-v>", new("a\nab\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 1, X: 1}, nil},
		{"Paste from clipboard history", "<shift-right><ctrl-c><ctrl-v><ctrl-v><ctrl-k><ctrl-v>", new("aaa\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 2}, nil},
		{"Undo", "<ctrl-x><ctrl-z>", new("a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 0}, nil},
		{"Redo", "<ctrl-x><ctrl-z><ctrl-shift-z>", new("b\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 0}, nil},
		{"Redo or repeat last command", "<ctrl-x><ctrl-z><ctrl-y>", new("b\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 0}, nil},

		// Line manipulation
		{"Insert line after current line", "<ctrl-enter>", new("a\n\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 1, X: 0}, nil},
		{"Insert line before current line", "<ctrl-shift-enter>", new("\na\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 0}, nil},
		{"Duplicate line(s)", "<ctrl-shift-d>", new("a\na\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 1, X: 0}, nil},
		{"Delete entire line", "<ctrl-shift-k>", new("b\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 0}, nil},
		{"Join line below to end of current line", "<ctrl-j>", new("ab\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 1}, nil},
		{"Indent current line(s)", "<ctrl-]>", new("\ta\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 1}, nil},
		{"Unindent current line(s)", "<ctrl-]><ctrl-[>", new("a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 0}, nil},
		{"Delete from cursor to end of line", "<ctrl-k><ctrl-k>", new("\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 0}, nil},
		{"Delete to beginning of line", "<right><ctrl-k><ctrl-backspace>", new("\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 0}, nil},
		{"Set mark at cursor position", "<ctrl-k><ctrl-space>", nil, term.Coordinates{}, nil},
		{"Select from cursor to mark", "<ctrl-k><ctrl-space><down><down><ctrl-k><ctrl-a><ctrl-c>", nil, term.Coordinates{Y: 0, X: 0}, new("a\nb\n")},
		{"Delete from cursor to mark", "<ctrl-k><ctrl-space><down><down><ctrl-k><ctrl-w>", new("c\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 0}, nil},
		{"Swap cursor position with mark", "<ctrl-k><ctrl-space><down><down><ctrl-k><ctrl-x>", nil, term.Coordinates{Y: 0, X: 0}, nil},
		{"Clear mark", "<ctrl-k><ctrl-space><ctrl-k><ctrl-g>", nil, term.Coordinates{}, nil},
		{"Delete to end of line", "<ctrl-shift-delete>", new("\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 0}, nil},
		{"Delete to start of line", "<right><ctrl-shift-backspace>", new("\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 0}, nil},

		// Comments
		{"Toggle line comment", "<ctrl-/>", new("// a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 3}, nil},
		{"Toggle block comment", "<shift-right><ctrl-shift-/>", new("/*a*/\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 2}, nil},

		// Text transformation
		{"Transform selection to UPPERCASE", "<shift-right><ctrl-k><ctrl-u>", new("A\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 1}, nil},
		{"Transform selection to lowercase", "<shift-right><ctrl-k><ctrl-u><home><shift-right><ctrl-k><ctrl-l>", new("a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"), term.Coordinates{Y: 0, X: 1}, nil},
		{"Wrap paragraph at ruler", "<ctrl-k><ctrl-q>", new("alpha beta\ngamma delta\nepsilon zeta\neta theta\n\ni\nj\nk"), term.Coordinates{Y: 0, X: 0}, nil},

		// Selection
		{"Select all", "<ctrl-a><ctrl-c>", nil, term.Coordinates{}, spAll("\n")},
		{"Select entire line (repeat to select additional lines)", "<down><ctrl-l><ctrl-l><ctrl-c>", nil, term.Coordinates{Y: 1, X: 0}, new("b\nc\n")},
		{"Select word at cursor (repeat to select next occurrence)", "<ctrl-end>a<enter><ctrl-home><ctrl-d><ctrl-d><ctrl-d><ctrl-c>", nil, term.Coordinates{}, new("a")},
		{"Expand selection to brackets", "{abc}<left><left><shift-left><ctrl-shift-m>", nil, term.Coordinates{X: 4}, nil},
		{"Expand selection to scope", "<ctrl-shift-space>", nil, term.Coordinates{}, nil},
		{"Expand selection to indentation level", "<ctrl-shift-j><ctrl-c>", nil, term.Coordinates{}, spAll("\n")},

		// Navigation and movement
		{"Move to start of line", "<space><right><home>", nil, term.Coordinates{Y: 0, X: 0}, nil},
		{"Move to end of line", "<end>", nil, term.Coordinates{Y: 0, X: 1}, nil},
		{"Jump to matching bracket", "{}<left><left><ctrl-m>", nil, term.Coordinates{X: 1}, nil},
		{"Move to start of file", "<down><down><ctrl-home>", nil, term.Coordinates{Y: 0, X: 0}, nil},
		{"Move to end of file", "<ctrl-end>", nil, term.Coordinates{Y: 10, X: 0}, nil},

		// Scrolling
		{"Center current line in view", "<ctrl-end>z<enter>z<enter>z<enter>z<enter>z<enter>z<enter><up><up><up><up><ctrl-k><ctrl-c>",
			nil, term.Coordinates{Y: 12}, nil},
		{"Scroll view up one line", "<ctrl-end><up><ctrl-alt-up>", nil, term.Coordinates{Y: 9}, nil},
		{"Scroll view down one line", "<ctrl-alt-down>", nil, term.Coordinates{Y: 1}, nil},

		// Search
		{"Find", "<ctrl-f>", nil, term.Coordinates{}, nil},

		// Macros
		{"Start/stop recording macro", "<ctrl-q>", nil, term.Coordinates{}, nil},
		{"Playback recorded macro", "<ctrl-q><right><ctrl-q><ctrl-shift-q>", nil, term.Coordinates{Y: 0, X: 1}, nil},
	})
}

func TestAutoPairOption(t *testing.T) {
	uri, err := workspaceapi.ParseURI("memory:///myfile.go")
	require.NoError(t, err)

	run := func(t *testing.T, keycomb string, opts ...Option) (*cell.Buffer, *standardHandler) {
		t.Helper()
		seq, err := term.ParseKeys(keycomb)
		require.NoError(t, err)

		buf := cell.NewBuffer()
		buf.ReadFrom(strings.NewReader("a"))
		h := NewHandler(buf, uri, text.IndentRuneTab, 0, opts...)
		h.Resize(10, 3)
		handler := h.(*standardHandler)
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

func TestMetaKMarkUsesSharedLocationList(t *testing.T) {
	uri, err := workspaceapi.ParseURI("memory:///myfile.go")
	require.NoError(t, err)

	buf := cell.NewBuffer()
	buf.ReadFrom(strings.NewReader("a\nb\nc"))
	h := NewHandler(buf, uri, text.IndentRuneTab, 0, WithKeymap(KeymapMacOS))
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
			if list.ID == standardMarkLocationListID {
				return list.Locations
			}
		}
		return nil
	}

	run("<meta-k><meta-space>")
	require.Len(t, markLocations(), 1)
	assert.Equal(t, term.Coordinates{}, markLocations()[0].From)

	run("<down><down><meta-k><meta-space>")
	require.Len(t, markLocations(), 2)
	assert.Equal(t, term.Coordinates{}, markLocations()[0].From)
	assert.Equal(t, term.Coordinates{Y: 2}, markLocations()[1].From)

	run("<meta-k><meta-g>")
	assert.Empty(t, markLocations())
}

func TestMetaKSwapUpdatesSharedMarkLocation(t *testing.T) {
	uri, err := workspaceapi.ParseURI("memory:///myfile.go")
	require.NoError(t, err)

	buf := cell.NewBuffer()
	buf.ReadFrom(strings.NewReader("a\nb\nc"))
	h := NewHandler(buf, uri, text.IndentRuneTab, 0, WithKeymap(KeymapMacOS))
	h.Resize(10, 3)

	seq, err := term.ParseKeys("<meta-k><meta-space><down><meta-k><meta-space><down><meta-k><meta-x>")
	require.NoError(t, err)
	for _, key := range seq {
		_, handled := h.Handle(term.Event{Type: term.EventKey, Key: key.Key, Mod: key.Mod, Ch: key.Ch})
		require.True(t, handled)
	}

	assert.Equal(t, term.Coordinates{Y: 1}, h.CursorAtScroll())
	for _, list := range h.LocationLists() {
		if list.ID != standardMarkLocationListID {
			continue
		}
		require.Len(t, list.Locations, 2)
		assert.Equal(t, term.Coordinates{}, list.Locations[0].From)
		assert.Equal(t, term.Coordinates{Y: 2}, list.Locations[1].From)
		return
	}
	t.Fatal("mark location list not found")
}

func TestSublimeSelectIndentationLevelKeyBinding(t *testing.T) {
	uri, err := workspaceapi.ParseURI("memory:///myfile.go")
	require.NoError(t, err)

	tests := []struct {
		name        string
		content     string
		keys        string
		tabspaces   int
		wantHandled []bool
		wantClip    string
		wantSel     string
		wantAt      term.Coordinates
		wantMode    text.SelectMode
		wantModeOK  bool
	}{
		{
			name:     "copies current indented block",
			content:  "root\n\tb\n\tc\nroot2",
			keys:     "<down><shift-meta-j><m-c>",
			wantClip: "\tb\n\tc\n",
			wantAt:   term.Coordinates{Y: 1},
		},
		{
			name:       "leaves line selection active after keybinding",
			content:    "root\n\tb\n\tc\nroot2",
			keys:       "<down><shift-meta-j>",
			wantSel:    "\tb\n\tc\n",
			wantAt:     term.Coordinates{Y: 1},
			wantMode:   text.LineSelection,
			wantModeOK: true,
		},
		{
			name:       "keeps deeper nested lines in selected block",
			content:    "root\n  if\n    child\n  sibling\nroot2",
			keys:       "<down><shift-meta-j>",
			wantSel:    "  if\n    child\n  sibling\n",
			wantAt:     term.Coordinates{Y: 1},
			wantMode:   text.LineSelection,
			wantModeOK: true,
		},
		{
			name:       "uses configured tabspaces for visual indentation",
			content:    "root\n\ttabbed\n  two spaces\n shallow",
			keys:       "<down><shift-meta-j>",
			tabspaces:  2,
			wantSel:    "\ttabbed\n  two spaces\n",
			wantAt:     term.Coordinates{Y: 1},
			wantMode:   text.LineSelection,
			wantModeOK: true,
		},
		{
			name:        "does not leave selection on blank-only buffer",
			content:     "\n\t\n  ",
			keys:        "<down><shift-meta-j>",
			wantHandled: []bool{true, false},
			wantAt:      term.Coordinates{Y: 1},
			wantModeOK:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			seq, err := term.ParseKeys(tc.keys)
			require.NoError(t, err)

			clip := clipboard.NewInMemory()
			reg := registerhistory.NewClipboard(registerset.New(clip))
			buf := cell.NewBuffer()
			buf.ReadFrom(strings.NewReader(tc.content))

			tabspaces := tc.tabspaces
			if tabspaces <= 0 {
				tabspaces = 4
			}
			handler := NewHandler(buf, uri, text.IndentRuneTab, tabspaces,
				WithKeymap(KeymapMacOS), WithClipboard(reg)).(*standardHandler)
			handler.Resize(80, 10)
			for i, key := range seq {
				ev := term.Event{Type: term.EventKey, Key: key.Key, Mod: key.Mod, Ch: key.Ch}
				_, ok := handler.Handle(ev)
				wantHandled := true
				if len(tc.wantHandled) > 0 {
					wantHandled = tc.wantHandled[i]
				}
				require.Equal(t, wantHandled, ok, "key %d", i)
			}

			if tc.wantClip != "" {
				paste, err := clip.Paste(clipboard.DefaultRegisterID)
				require.NoError(t, err)
				assert.Equal(t, tc.wantClip, paste.Text)
			}
			if tc.wantSel != "" {
				assert.Equal(t, tc.wantSel, handler.cursor.Selection())
			}
			mode, ok := handler.cursor.SelectionMode()
			assert.Equal(t, tc.wantModeOK, ok)
			if tc.wantModeOK {
				assert.Equal(t, tc.wantMode, mode)
			}
			assert.Equal(t, tc.wantAt, handler.CursorAtScroll())
		})
	}
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
				{input: "<end><meta-v>", wantHandled: true, wantContent: "zc", wantCursor: coords(term.Coordinates{X: 2})},
				{input: "<alt-meta-v>", wantHandled: true, wantContent: "zb", wantCursor: coords(term.Coordinates{X: 2})},
				{input: "<alt-meta-v>", wantHandled: true, wantContent: "za", wantCursor: coords(term.Coordinates{X: 2})},
				{input: "<alt-meta-v>", wantHandled: true, wantContent: "za", wantCursor: coords(term.Coordinates{X: 2})},
			},
		},
		{
			name:        "regular paste restarts history cycle",
			content:     "z",
			history:     []string{"a", "b", "c"},
			wrapHistory: true,
			steps: []pasteHistoryStep{
				{input: "<end><meta-v>", wantHandled: true, wantContent: "zc"},
				{input: "<alt-meta-v>", wantHandled: true, wantContent: "zb"},
				{input: "<meta-v>", wantHandled: true, wantContent: "zbc"},
				{input: "<alt-meta-v>", wantHandled: true, wantContent: "zbb"},
			},
		},
		{
			name:        "typing after paste starts a new history paste",
			content:     "z",
			history:     []string{"a", "b"},
			wrapHistory: true,
			steps: []pasteHistoryStep{
				{input: "<end><meta-v>", wantHandled: true, wantContent: "zb", wantCursor: coords(term.Coordinates{X: 2})},
				{input: "x", wantHandled: true, wantContent: "zbx", wantCursor: coords(term.Coordinates{X: 3})},
				{input: "<alt-meta-v>", wantHandled: true, wantContent: "zbxb", wantCursor: coords(term.Coordinates{X: 4})},
				{input: "<alt-meta-v>", wantHandled: true, wantContent: "zbxa", wantCursor: coords(term.Coordinates{X: 4})},
			},
		},
		{
			name:        "cursor movement after paste starts a new history paste",
			content:     "z",
			history:     []string{"a", "b"},
			wrapHistory: true,
			steps: []pasteHistoryStep{
				{input: "<end><meta-v>", wantHandled: true, wantContent: "zb", wantCursor: coords(term.Coordinates{X: 2})},
				{input: "<left>", wantHandled: true, wantContent: "zb", wantCursor: coords(term.Coordinates{X: 1})},
				{input: "<alt-meta-v>", wantHandled: true, wantContent: "zbb", wantCursor: coords(term.Coordinates{X: 2})},
				{input: "<alt-meta-v>", wantHandled: true, wantContent: "zab", wantCursor: coords(term.Coordinates{X: 2})},
			},
		},
		{
			name:        "clipboard without history support consumes in paste context",
			content:     "z",
			history:     []string{"a", "b"},
			wrapHistory: false,
			steps: []pasteHistoryStep{
				{input: "<end><meta-v>", wantHandled: true, wantContent: "zb", wantCursor: coords(term.Coordinates{X: 2})},
				{input: "<alt-meta-v>", wantHandled: true, wantContent: "zb", wantCursor: coords(term.Coordinates{X: 2})},
			},
		},
		{
			name:        "alt-meta-v before paste initiates history paste",
			content:     "z",
			history:     []string{"a", "b", "c"},
			wrapHistory: true,
			steps: []pasteHistoryStep{
				{input: "<end><alt-meta-v>", wantHandled: true, wantContent: "zc", wantCursor: coords(term.Coordinates{X: 2})},
				{input: "<alt-meta-v>", wantHandled: true, wantContent: "zb", wantCursor: coords(term.Coordinates{X: 2})},
				{input: "<alt-meta-v>", wantHandled: true, wantContent: "za", wantCursor: coords(term.Coordinates{X: 2})},
			},
		},
		{
			name:        "alt-meta-v without history support before paste is not handled",
			content:     "z",
			history:     []string{"a", "b"},
			wrapHistory: false,
			steps: []pasteHistoryStep{
				{input: "<alt-meta-v>", wantHandled: false, wantContent: "z", wantCursor: coords(term.Coordinates{})},
			},
		},
		{
			name:        "standard copy operations populate shared history",
			content:     "ab\ncd",
			wrapHistory: true,
			steps: []pasteHistoryStep{
				{input: "<shift-right><meta-c><right><shift-right><meta-c><end><meta-v>", wantHandled: true, wantContent: "abb\ncd"},
				{input: "<alt-meta-v>", wantHandled: true, wantContent: "aba\ncd"},
				{input: "<alt-meta-v>", wantHandled: true, wantContent: "aba\ncd"},
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
			h := NewHandler(buf, uri, text.IndentRuneTab, 0,
				WithKeymap(KeymapMacOS), WithClipboard(reg))
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

// TestStandardTabAtTargetInsertsFullIndentLevel reproduces RUNE-121 for the
// standard handler: when the line is already at the syntax target indent, a
// <tab> keypress must add a full indent level rather than a single space, and
// a second <tab> must add another level rather than dedenting.
func TestStandardTabAtTargetInsertsFullIndentLevel(t *testing.T) {
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

// TestStandardShiftTabFallsThroughWhenNoDedent verifies that <shift-tab>
// is reported as unhandled when there is no indentation to remove, so the
// event can fall through to outer command keybindings (e.g. the file
// explorer toggle) instead of being silently swallowed.
func TestStandardShiftTabFallsThroughWhenNoDedent(t *testing.T) {
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

// TestStandardShiftTabDedentsWhenIndented verifies that <shift-tab> still
// dedents an indented line and reports the event as handled.
func TestStandardShiftTabDedentsWhenIndented(t *testing.T) {
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

// errorClipboard is a clipboard.Register that always returns an error on Paste.
type errorClipboard struct{}

func (errorClipboard) Paste(string) (clipboard.Data, error) {
	return clipboard.Data{}, errors.New("clipboard error")
}

func (errorClipboard) Copy(string, clipboard.Data) error {
	return errors.New("clipboard error")
}

func TestPasteAndReindent(t *testing.T) {
	uri, err := workspaceapi.ParseURI("memory:///reindent.go")
	require.NoError(t, err)

	tests := []struct {
		name         string
		content      string           // initial buffer content
		clipText     string           // text to place in clipboard before paste
		clipMeta     any              // metadata for clipboard data
		indents      map[int]int      // per-line target indentation (nil = no indent view)
		cursorAt     term.Coordinates // cursor position before paste
		wantHandled  bool
		wantContent  string
		wantCursor   term.Coordinates
		useErrorClip bool // use errorClipboard instead of normal one
	}{
		{
			name:        "under-indented adds tab",
			content:     "hello",
			clipText:    "x",
			clipMeta:    text.StandardSelection,
			indents:     map[int]int{0: 1},
			cursorAt:    term.Coordinates{},
			wantHandled: true,
			wantContent: "\txhello",
			wantCursor:  term.Coordinates{X: 1},
		},
		{
			name:        "over-indented removes tab",
			content:     "\t\thello",
			clipText:    "x",
			clipMeta:    text.StandardSelection,
			indents:     map[int]int{0: 1},
			cursorAt:    term.Coordinates{X: 2},
			wantHandled: true,
			wantContent: "\txhello",
			wantCursor:  term.Coordinates{X: 3},
		},
		{
			name:        "already indented is unchanged",
			content:     "\thello",
			clipText:    "x",
			clipMeta:    text.StandardSelection,
			indents:     map[int]int{0: 1},
			cursorAt:    term.Coordinates{X: 1},
			wantHandled: true,
			wantContent: "\txhello",
			wantCursor:  term.Coordinates{X: 2},
		},
		{
			name:        "metadata is not SelectMode defaults to StandardSelection",
			content:     "hello\nworld",
			clipText:    "x",
			clipMeta:    "not a SelectMode",
			indents:     nil,
			cursorAt:    term.Coordinates{},
			wantHandled: true,
			wantContent: "xhello\nworld",
			wantCursor:  term.Coordinates{X: 1},
		},
		{
			name:        "startY at last line",
			content:     "a\nb\nc",
			clipText:    "x",
			clipMeta:    text.StandardSelection,
			indents:     map[int]int{2: 1},
			cursorAt:    term.Coordinates{Y: 2},
			wantHandled: true,
			wantContent: "a\nb\n\txc",
			wantCursor:  term.Coordinates{Y: 2, X: 1},
		},
		{
			name:         "clipboard error returns false",
			content:      "hello\nworld",
			useErrorClip: true,
			wantHandled:  false,
			wantContent:  "hello\nworld",
			wantCursor:   term.Coordinates{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := cell.NewBuffer()
			buf.ReadFrom(strings.NewReader(tt.content))

			var clip clipboard.Register
			if tt.useErrorClip {
				clip = errorClipboard{}
			} else {
				clip = clipboard.NewInMemory()
				require.NoError(t, clip.Copy(
					clipboard.DefaultRegisterID,
					clipboard.Data{Text: tt.clipText, Metadata: tt.clipMeta},
				))
			}

			handler := NewHandler(buf, uri,
				text.IndentRuneTab, 0,
				WithKeymap(KeymapMacOS),
				WithClipboard(clip),
				WithTabspaces(1),
			)
			handler.Resize(80, 10)

			if tt.indents != nil {
				buf.WithView(testIndentView{
					View:    buf.View(),
					indents: tt.indents,
				})
			}

			if tt.cursorAt.X != 0 || tt.cursorAt.Y != 0 {
				require.True(t, handler.SetCursorAtScroll(tt.cursorAt))
			}

			ev := term.Event{
				Type: term.EventKey,
				Mod:  term.ModMeta,
				Ch:   'V',
			}
			_, handled := handler.Handle(ev)

			assert.Equal(t, tt.wantHandled, handled, "handled")
			assert.Equal(t, tt.wantContent, buf.String(), "buffer content")
			assert.Equal(t, tt.wantCursor, handler.CursorAtScroll(), "cursor position")
		})
	}
}

type findTestRange struct {
	from term.Coordinates
	to   term.Coordinates
}

type findTestStep struct {
	name          string
	keys          string
	wantMode      bool
	wantCursor    term.Coordinates
	wantSelection *findTestRange
	wantMatches   []findTestRange
	wantPrompt    string
}

func TestStandardFind(t *testing.T) {
	tests := []struct {
		name          string
		content       string
		initialCursor term.Coordinates
		wantInitial   term.Coordinates
		steps         []findTestStep
	}{
		{
			name:    "Meta-F owns input and refines live",
			content: "foo bar foo",
			steps: []findTestStep{
				{
					name:          "partial query",
					keys:          "<meta-f>ba",
					wantMode:      true,
					wantCursor:    term.Coordinates{X: 6},
					wantSelection: findRange(4, 0, 6, 0),
					wantMatches:   findRanges(4, 0, 6, 0),
					wantPrompt:    "Find: ba  1/1",
				},
				{
					name:          "complete query",
					keys:          "r",
					wantMode:      true,
					wantCursor:    term.Coordinates{X: 7},
					wantSelection: findRange(4, 0, 7, 0),
					wantMatches:   findRanges(4, 0, 7, 0),
					wantPrompt:    "Find: bar  1/1",
				},
			},
		},
		{
			name:    "Ctrl-F and space search",
			content: "foo bar x foo bar",
			steps: []findTestStep{
				{
					keys:          "<ctrl-f>foo<space>bar",
					wantMode:      true,
					wantCursor:    term.Coordinates{X: 7},
					wantSelection: findRange(0, 0, 7, 0),
					wantMatches: findRanges(
						0, 0, 7, 0,
						10, 0, 17, 0,
					),
					wantPrompt: "Find: foo bar  1/2",
				},
			},
		},
		{
			name:    "Enter advances and wraps",
			content: "foo x foo",
			steps: []findTestStep{
				findStep("<meta-f>foo", 0, 0, 3, 0, "Find: foo  1/2", 0, 0, 3, 0, 6, 0, 9, 0),
				findStep("<enter>", 6, 0, 9, 0, "Find: foo  2/2", 0, 0, 3, 0, 6, 0, 9, 0),
				findStep("<enter>", 0, 0, 3, 0, "Find: foo  1/2", 0, 0, 3, 0, 6, 0, 9, 0),
			},
		},
		{
			name:    "Shift-Enter moves backward and wraps",
			content: "foo x foo",
			steps: []findTestStep{
				findStep("<meta-f>foo<shift-enter>", 6, 0, 9, 0, "Find: foo  2/2", 0, 0, 3, 0, 6, 0, 9, 0),
				findStep("<shift-enter>", 0, 0, 3, 0, "Find: foo  1/2", 0, 0, 3, 0, 6, 0, 9, 0),
			},
		},
		{
			name:    "Meta-F advances while active",
			content: "foo x foo",
			steps: []findTestStep{
				findStep("<meta-f>foo<meta-f>", 6, 0, 9, 0, "Find: foo  2/2", 0, 0, 3, 0, 6, 0, 9, 0),
			},
		},
		{
			name:    "Ctrl-F advances while active",
			content: "foo x foo",
			steps: []findTestStep{
				findStep("<meta-f>foo<ctrl-f>", 6, 0, 9, 0, "Find: foo  2/2", 0, 0, 3, 0, 6, 0, 9, 0),
			},
		},
		{
			name:    "Backspace widens then empties the query",
			content: "aX aY",
			steps: []findTestStep{
				findStep("<meta-f>aX", 0, 0, 2, 0, "Find: aX  1/1", 0, 0, 2, 0),
				findStep("<backspace>", 0, 0, 1, 0, "Find: a  1/2", 0, 0, 1, 0, 3, 0, 4, 0),
				{
					name:       "empty query",
					keys:       "<backspace>",
					wantMode:   true,
					wantCursor: term.Coordinates{},
					wantPrompt: "Find: ",
				},
				{
					name:       "backspace remains safe",
					keys:       "<backspace>",
					wantMode:   true,
					wantCursor: term.Coordinates{},
					wantPrompt: "Find: ",
				},
			},
		},
		{
			name:    "Search is case sensitive",
			content: "Foo foo FOO",
			steps: []findTestStep{
				findStep("<meta-f>foo", 4, 0, 7, 0, "Find: foo  1/1", 4, 0, 7, 0),
			},
		},
		{
			name:    "Esc accepts then clears on second Esc",
			content: "foo bar",
			steps: []findTestStep{
				{
					name:          "accept",
					keys:          "<meta-f>bar<esc>",
					wantCursor:    term.Coordinates{X: 7},
					wantSelection: findRange(4, 0, 7, 0),
					wantMatches:   findRanges(4, 0, 7, 0),
				},
				{
					name:       "clear",
					keys:       "<esc>",
					wantCursor: term.Coordinates{X: 7},
				},
			},
		},
		{
			name:    "Arrow accepts and is rehandled",
			content: "foo bar baz",
			steps: []findTestStep{
				{
					keys:       "<meta-f>bar<right>",
					wantCursor: term.Coordinates{X: 8},
				},
			},
		},
		{
			name:    "Empty prompt resumes last accepted query",
			content: "foo x foo y foo",
			steps: []findTestStep{
				{
					name:          "accept first result",
					keys:          "<meta-f>foo<esc>",
					wantCursor:    term.Coordinates{X: 3},
					wantSelection: findRange(0, 0, 3, 0),
					wantMatches: findRanges(
						0, 0, 3, 0,
						6, 0, 9, 0,
						12, 0, 15, 0,
					),
				},
				{
					name:          "open empty prompt",
					keys:          "<meta-f>",
					wantMode:      true,
					wantCursor:    term.Coordinates{X: 3},
					wantSelection: findRange(0, 0, 3, 0),
					wantMatches: findRanges(
						0, 0, 3, 0,
						6, 0, 9, 0,
						12, 0, 15, 0,
					),
					wantPrompt: "Find: ",
				},
				findStep("<meta-f>", 6, 0, 9, 0, "Find: foo  2/3", 0, 0, 3, 0, 6, 0, 9, 0, 12, 0, 15, 0),
			},
		},
		{
			name:          "No match parks at origin",
			content:       "foo bar",
			initialCursor: term.Coordinates{X: 2},
			wantInitial:   term.Coordinates{X: 2},
			steps: []findTestStep{
				{
					keys:       "<meta-f>zzz",
					wantMode:   true,
					wantCursor: term.Coordinates{X: 2},
					wantPrompt: "Find: zzz  no matches",
				},
			},
		},
		{
			name:    "Empty buffer is safe",
			content: "",
			steps: []findTestStep{
				{
					keys:       "<ctrl-f>x",
					wantMode:   true,
					wantCursor: term.Coordinates{},
					wantPrompt: "Find: x  no matches",
				},
			},
		},
		{
			name:          "Origin chooses the next multiline match",
			content:       "foo\nx foo\nfoo",
			initialCursor: term.Coordinates{X: 2},
			wantInitial:   term.Coordinates{X: 2},
			steps: []findTestStep{
				findStep("<meta-f>foo", 2, 1, 5, 1, "Find: foo  2/3", 0, 0, 3, 0, 2, 1, 5, 1, 0, 2, 3, 2),
				findStep("<enter>", 0, 2, 3, 2, "Find: foo  3/3", 0, 0, 3, 0, 2, 1, 5, 1, 0, 2, 3, 2),
				findStep("<enter>", 0, 0, 3, 0, "Find: foo  1/3", 0, 0, 3, 0, 2, 1, 5, 1, 0, 2, 3, 2),
			},
		},
		{
			name:    "Search does not cross lines",
			content: "ab\ncd",
			steps: []findTestStep{
				{
					keys:       "<meta-f>abcd",
					wantMode:   true,
					wantCursor: term.Coordinates{},
					wantPrompt: "Find: abcd  no matches",
				},
			},
		},
		{
			name:    "Wide character matches use cell coordinates",
			content: "界x界",
			steps: []findTestStep{
				findStep("<meta-f>界", 0, 0, 1, 0, "Find: 界  1/2", 0, 0, 1, 0, 2, 0, 3, 0),
				findStep("<enter>", 2, 0, 3, 0, "Find: 界  2/2", 0, 0, 1, 0, 2, 0, 3, 0),
			},
		},
		{
			name:    "Multi-wide query selects every rune",
			content: "你好 x 你好",
			steps: []findTestStep{
				findStep("<meta-f>你好", 0, 0, 2, 0, "Find: 你好  1/2", 0, 0, 2, 0, 5, 0, 7, 0),
				findStep("<shift-enter>", 5, 0, 7, 0, "Find: 你好  2/2", 0, 0, 2, 0, 5, 0, 7, 0),
			},
		},
		{
			name:    "NUL cells preserve separated matches",
			content: "foo\x00foo\x00",
			steps: []findTestStep{
				findStep("<meta-f>foo", 0, 0, 3, 0, "Find: foo  1/2", 0, 0, 3, 0, 4, 0, 7, 0),
				findStep("<enter>", 4, 0, 7, 0, "Find: foo  2/2", 0, 0, 3, 0, 4, 0, 7, 0),
			},
		},
		{
			name:    "Search does not cross a NUL cell",
			content: "a\x00b",
			steps: []findTestStep{
				{
					keys:       "<meta-f>ab",
					wantMode:   true,
					wantCursor: term.Coordinates{},
					wantPrompt: "Find: ab  no matches",
				},
			},
		},
		{
			name:    "Only NUL cells are safe",
			content: "\x00\x00\x00",
			steps: []findTestStep{
				{
					keys:       "<meta-f>x",
					wantMode:   true,
					wantCursor: term.Coordinates{},
					wantPrompt: "Find: x  no matches",
				},
			},
		},
		{
			name:          "Positive X overflow clamps before searching",
			content:       "foo bar",
			initialCursor: term.Coordinates{X: 99},
			wantInitial:   term.Coordinates{X: 7},
			steps: []findTestStep{
				findStep("<meta-f>foo", 0, 0, 3, 0, "Find: foo  1/1", 0, 0, 3, 0),
			},
		},
		{
			name:          "Positive Y overflow clamps to the last row",
			content:       "foo\nbar foo",
			initialCursor: term.Coordinates{Y: 99},
			wantInitial:   term.Coordinates{Y: 1},
			steps: []findTestStep{
				findStep("<meta-f>foo", 4, 1, 7, 1, "Find: foo  2/2", 0, 0, 3, 0, 4, 1, 7, 1),
			},
		},
		{
			name:          "Negative X clamps to the row start",
			content:       "x foo",
			initialCursor: term.Coordinates{X: -99},
			steps: []findTestStep{
				findStep("<meta-f>foo", 2, 0, 5, 0, "Find: foo  1/1", 2, 0, 5, 0),
			},
		},
		{
			name:          "Negative Y clamps to the first row",
			content:       "x foo\nfoo",
			initialCursor: term.Coordinates{X: 2, Y: -99},
			wantInitial:   term.Coordinates{X: 2},
			steps: []findTestStep{
				findStep("<meta-f>foo", 2, 0, 5, 0, "Find: foo  1/2", 2, 0, 5, 0, 0, 1, 3, 1),
			},
		},
		{
			name:          "Combined overflow clamps to the last row end",
			content:       "foo\nbar foo",
			initialCursor: term.Coordinates{X: 99, Y: 99},
			wantInitial:   term.Coordinates{X: 7, Y: 1},
			steps: []findTestStep{
				findStep("<meta-f>foo", 0, 0, 3, 0, "Find: foo  1/2", 0, 0, 3, 0, 4, 1, 7, 1),
			},
		},
		{
			name:          "Combined negative coordinates clamp to origin",
			content:       "foo",
			initialCursor: term.Coordinates{X: -99, Y: -99},
			steps: []findTestStep{
				findStep("<meta-f>foo", 0, 0, 3, 0, "Find: foo  1/1", 0, 0, 3, 0),
			},
		},
		{
			name:          "Empty buffer clamps overflow to origin",
			content:       "",
			initialCursor: term.Coordinates{X: 99, Y: 99},
			steps: []findTestStep{
				{
					keys:       "<meta-f>x",
					wantMode:   true,
					wantCursor: term.Coordinates{},
					wantPrompt: "Find: x  no matches",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, buf := newStandardFindHandler(t, tt.content)
			h.SetCursorAtScroll(tt.initialCursor)
			assert.Equal(t, tt.wantInitial, h.CursorAtScroll(), "initial cursor")

			for _, step := range tt.steps {
				name := step.name
				if name == "" {
					name = step.keys
				}
				t.Run(name, func(t *testing.T) {
					feedKeys(t, h, step.keys)
					assert.Equal(t, tt.content, buf.String(), "buffer content")
					assert.Equal(t, step.wantMode, h.IsSearchMode(), "search mode")
					assert.Equal(t, step.wantCursor, h.CursorAtScroll(), "cursor")
					assertFindState(t, h, step)
				})
			}
		})
	}
}

func TestStandardFindCurrentMatchInvertsDefaultAttributes(t *testing.T) {
	tests := []struct {
		name          string
		keys          string
		currentStart  int
		inactiveStart int
	}{
		{"first result", "<meta-f>foo", 0, 6},
		{"advanced result", "<meta-f>foo<enter>", 6, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _ := newStandardFindHandler(
				t,
				"foo x foo",
				WithResAttr(term.Attributes{Bg: term.ColorYellow}),
			)
			feedKeys(t, h, tt.keys)

			w := cell.NewBufferWriter(context.Background(), 80, 10)
			h.Draw(w)
			cells := w.RawCells()[0]
			for x := range 3 {
				assert.Equal(t, term.Attributes{Attrs: term.AttrReverse}, cells[tt.currentStart+x].Attributes())
				assert.Equal(t, term.Attributes{Bg: term.ColorYellow}, cells[tt.inactiveStart+x].Attributes())
			}
		})
	}
}

// statusLayoutAttr is the status slot styling supplied by the status bar
// layout; it must survive find prompts that do not configure status_attr.
var statusLayoutAttr = term.Attributes{Fg: term.ColorWhite, Bg: term.ColorPurple}

func TestStandardFindStatusAttributes(t *testing.T) {
	tests := []struct {
		name string
		opts []Option
		want term.Attributes
	}{
		{
			name: "configured foreground and background",
			opts: []Option{WithBarAttr(term.Attributes{
				Fg: term.ColorBlack,
				Bg: term.ColorWhite,
			})},
			want: term.Attributes{Fg: term.ColorBlack, Bg: term.ColorWhite},
		},
		{
			name: "unconfigured status keeps layout attributes",
			opts: []Option{WithAttr(term.Attributes{Bg: term.ColorRed})},
			want: statusLayoutAttr,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _ := newStandardFindHandler(t, "foo", tt.opts...)
			feedKeys(t, h, "<meta-f>foo")

			w := cell.NewBufferWriter(context.Background(), 80, 10)
			h.Draw(w)
			row := w.RawCells()[9]
			for x := range len("Find: foo  1/1") {
				require.Equal(t, tt.want.Fg, row[x].Fg, "status fg cell %d", x)
				require.Equal(t, tt.want.Bg, row[x].Bg, "status bg cell %d", x)
			}
			assert.NotEqual(t, tt.want.Bg, row[len("Find: foo  1/1")].Bg)
		})
	}
}

type standardFindTestHandler struct {
	text.Handler
	root *standardHandler
}

func (h standardFindTestHandler) SelectionBounds() (term.Coordinates, term.Coordinates, bool) {
	return h.root.SelectionBounds()
}

func newStandardFindHandler(t *testing.T, content string, opts ...Option) (text.Handler, *cell.Buffer) {
	t.Helper()
	buf := cell.NewBuffer()
	buf.ReadFrom(strings.NewReader(content))
	uri, err := workspaceapi.ParseURI("memory:///find.txt")
	require.NoError(t, err)
	opts = append([]Option{WithKeymap(KeymapMacOS)}, append(opts, WithCommandBar(true))...)
	root := NewHandler(buf, uri, text.IndentRuneTab, 0, opts...).(*standardHandler)
	bar := text.WithStatusBar(root, buf, root.less.Scroll(), false, false, text.StatusBarConfig{
		Publisher:        &texttest.TestEditor{},
		ScheduleNextTick: func(fn func()) bool { fn(); return true },
		Layout: []text.StatusBarComponent{{
			Type:       text.StatusBarStatus,
			Template:   "%s",
			Attributes: statusLayoutAttr,
		}},
	})
	root.setStatusBar(bar)
	bar.Resize(80, 10)
	return standardFindTestHandler{Handler: bar, root: root}, buf
}

func findStep(
	keys string,
	fromX, fromY, toX, toY int,
	prompt string,
	matchCoordinates ...int,
) findTestStep {
	return findTestStep{
		keys:          keys,
		wantMode:      true,
		wantCursor:    term.Coordinates{X: toX, Y: toY},
		wantSelection: findRange(fromX, fromY, toX, toY),
		wantMatches:   findRanges(matchCoordinates...),
		wantPrompt:    prompt,
	}
}

func findRange(fromX, fromY, toX, toY int) *findTestRange {
	return &findTestRange{
		from: term.Coordinates{X: fromX, Y: fromY},
		to:   term.Coordinates{X: toX, Y: toY},
	}
}

func findRanges(coordinates ...int) []findTestRange {
	if len(coordinates)%4 != 0 {
		panic("find range coordinates must be provided in groups of four")
	}
	ranges := make([]findTestRange, 0, len(coordinates)/4)
	for i := 0; i < len(coordinates); i += 4 {
		ranges = append(ranges, *findRange(
			coordinates[i],
			coordinates[i+1],
			coordinates[i+2],
			coordinates[i+3],
		))
	}
	return ranges
}

func assertFindState(t *testing.T, h text.Handler, step findTestStep) {
	t.Helper()
	gotFrom, gotTo, ok := selectionBounds(t, h)
	if step.wantSelection == nil {
		assert.False(t, ok, "selection")
	} else if assert.True(t, ok, "selection") {
		assert.Equal(t, step.wantSelection.from, gotFrom, "selection start")
		assert.Equal(t, step.wantSelection.to, gotTo, "selection end")
	}

	var gotMatches []findTestRange
	for _, location := range findLocations(h) {
		gotMatches = append(gotMatches, findTestRange{
			from: location.From,
			to:   location.To,
		})
	}
	assert.Equal(t, step.wantMatches, gotMatches, "search matches")
	if step.wantPrompt != "" {
		drawn := strings.ReplaceAll(drawStandardHandler(t, h), "\x00", "")
		assert.Contains(
			t,
			strings.Join(strings.Fields(drawn), ""),
			strings.Join(strings.Fields(step.wantPrompt), ""),
			"prompt",
		)
	}
}

func selectionBounds(t *testing.T, h text.Handler) (term.Coordinates, term.Coordinates, bool) {
	t.Helper()
	withBounds, ok := h.(interface {
		SelectionBounds() (term.Coordinates, term.Coordinates, bool)
	})
	require.True(t, ok)
	return withBounds.SelectionBounds()
}

func findLocations(h text.Handler) []textapi.Location {
	for _, list := range h.LocationLists() {
		if list.ID == searchListID {
			return list.Locations
		}
	}
	return nil
}

func drawStandardHandler(t *testing.T, h text.Handler) string {
	t.Helper()
	w := cell.NewBufferWriter(context.Background(), 80, 10)
	h.Draw(w)
	var drawn strings.Builder
	for _, row := range w.RawCells() {
		for _, c := range row {
			drawn.WriteRune(c.Ch)
		}
		drawn.WriteByte('\n')
	}
	return drawn.String()
}

// robustnessSequences is the catalog of input token strings exercised by every
// robustness scenario. It aims to touch each arm of standardHandler.Handle:
// motion, selection, editing, clipboard, folds, comments, undo/redo, macros,
// paragraph/word/line ops, and the new Zed-parity chords.
var robustnessSequences = []struct {
	name string
	keys string
}{
	{"insert text", "abc"},
	{"newline", "<enter>"},
	{"tab", "<tab>"},
	{"space", "<space>"},
	{"backspace", "<backspace>"},
	{"delete", "<delete>"},
	{"arrow left", "<left>"},
	{"arrow right", "<right>"},
	{"arrow up", "<up>"},
	{"arrow down", "<down>"},
	{"home", "<home>"},
	{"end", "<end>"},
	{"pgup", "<pgup>"},
	{"pgdn", "<pgdn>"},
	{"ctrl word left", "<ctrl-left>"},
	{"ctrl word right", "<ctrl-right>"},
	{"ctrl paragraph up", "<ctrl-up>"},
	{"ctrl paragraph down", "<ctrl-down>"},
	{"ctrl home", "<ctrl-home>"},
	{"ctrl end", "<ctrl-end>"},
	{"ctrl backspace word", "<ctrl-backspace>"},
	{"ctrl delete word", "<ctrl-delete>"},
	{"ctrl enter line below", "<ctrl-enter>"},
	{"ctrl-shift enter line above", "<ctrl-shift-enter>"},
	{"cmd left bol", "<meta-left>"},
	{"cmd right eol", "<meta-right>"},
	{"cmd up bof", "<meta-up>"},
	{"cmd down eof", "<meta-down>"},
	{"cmd backspace to bol", "<meta-backspace>"},
	{"cmd delete to eol", "<meta-delete>"},
	{"shift right select", "<shift-right>"},
	{"shift left select", "<shift-left>"},
	{"shift home select", "<shift-home>"},
	{"shift end select", "<shift-end>"},
	{"cmd-shift left select", "<shift-meta-left>"},
	{"cmd-shift right select", "<shift-meta-right>"},
	{"cmd-shift up select", "<shift-meta-up>"},
	{"cmd-shift down select", "<shift-meta-down>"},
	{"alt-shift word select right", "<alt-shift-right>"},
	{"alt-shift word select left", "<alt-shift-left>"},
	{"ctrl-shift expand", "<ctrl-shift-right>"},
	{"ctrl-shift shrink", "<ctrl-shift-left>"},
	{"select all", "<meta-a>"},
	{"select line", "<meta-l>"},
	{"copy", "<meta-c>"},
	{"cut", "<meta-x>"},
	{"paste", "<meta-v>"},
	{"undo", "<meta-z>"},
	{"redo", "<meta-Z>"},
	{"selection undo", "<meta-u>"},
	{"selection redo", "<meta-U>"},
	{"recenter", "<ctrl-l>"},
	{"transpose", "<ctrl-t>"},
	{"cut to eol", "<ctrl-k>"},
	{"toggle soft wrap", "<alt-z>"},
	{"toggle line comment", "<meta-/>"},
	{"indent line", "<meta-]>"},
	{"outdent line", "<meta-[>"},
	{"move line up", "<alt-up>"},
	{"move line down", "<alt-down>"},
	{"duplicate line up", "<alt-shift-up>"},
	{"duplicate line down", "<alt-shift-down>"},
	{"select next occurrence", "<meta-d>"},
	{"select prev occurrence", "<ctrl-meta-d>"},
	{"matching bracket", "<ctrl-m>"},
	{"expand fold", "<meta-rbrace>"},
	{"collapse fold", "<meta-lbrace>"},
	{"select word then delete", "<meta-d><delete>"},
	{"select line then cut", "<meta-l><meta-x>"},
	{"select all then type", "<meta-a>z"},
}

// feedKeys dispatches an input token string to the handler. It first expands
// the pseudo-tokens <meta-lbrace>/<meta-rbrace> into raw cmd+brace events, since
// the term token grammar cannot express modifier+brace chords used by the fold
// bindings.
func feedKeys(t *testing.T, h text.Handler, keys string) {
	t.Helper()
	switch keys {
	case "<meta-lbrace>":
		h.Handle(term.Event{Type: term.EventKey, Mod: term.ModMeta, Ch: '{'})
		return
	case "<meta-rbrace>":
		h.Handle(term.Event{Type: term.EventKey, Mod: term.ModMeta, Ch: '}'})
		return
	}
	seq, err := term.ParseKeys(keys)
	require.NoError(t, err)
	for _, key := range seq {
		h.Handle(term.Event{Type: term.EventKey, Key: key.Key, Mod: key.Mod, Ch: key.Ch})
	}
}

// assertCursorInBounds verifies the cursor never lands on a non-existent row and
// never has a negative coordinate — the invariants whose violation causes
// out-of-range panics in row-indexed buffer accessors. The row may sit on the
// virtual line just past the last one (Y == Rows), which represents an empty
// buffer and the end-of-file caret. The column upper bound is intentionally not
// asserted: the standard editor keeps a sticky "desired column" across vertical
// motion that can exceed a shorter target line, and the buffer pads sparsely on
// edit — that is defined behavior, not an out-of-bounds error.
func assertCursorInBounds(t *testing.T, h text.Handler, ctx string) {
	t.Helper()
	rows := h.CellView().Rows()
	pos := h.CursorAtScroll()
	require.GreaterOrEqual(t, pos.Y, 0, "%s: cursor row negative", ctx)
	require.GreaterOrEqual(t, pos.X, 0, "%s: cursor col negative", ctx)
	if rows == 0 {
		require.Equal(t, 0, pos.Y, "%s: cursor row must be 0 on empty buffer", ctx)
		return
	}
	require.LessOrEqual(t, pos.Y, rows, "%s: cursor row past buffer", ctx)
}

// newRobustnessHandler builds a standard handler over content with a clipboard
// and a single seeded clipboard entry (so paste has something to paste), then
// resizes it. width/height of 0 are passed through so callers can test the
// zero-dimension path.
func newRobustnessHandler(
	t *testing.T, content string, width, height int,
) (text.Handler, *cell.Buffer, clipboard.Register) {
	t.Helper()
	uri, err := workspaceapi.ParseURI("test:///robust.go")
	require.NoError(t, err)
	buf := cell.NewBuffer()
	buf.ReadFrom(strings.NewReader(content))
	clip := clipboard.NewInMemory()
	require.NoError(t, clip.Copy(clipboard.DefaultRegisterID,
		clipboard.Data{Text: "X", Metadata: text.StandardSelection}))
	h := NewHandler(buf, uri, text.IndentRuneTab, 0,
		WithClipboard(clip),
		WithTabspaces(4),
		WithComments(text.CommentConfig{
			"go": {
				Line:  []string{"//"},
				Block: []text.CommentBlock{{Start: "/*", End: "*/"}},
			},
		}),
	)
	h.Resize(width, height)
	return h, buf, clip
}

// TestStandardRobustnessDegenerateBuffers drives every input sequence against
// degenerate buffers (empty, blank-only, single char, whitespace, no trailing
// newline) with the cursor at the origin. Nothing may panic and the cursor must
// stay in bounds.
func TestStandardRobustnessDegenerateBuffers(t *testing.T) {
	contents := []struct {
		name    string
		content string
	}{
		{"empty", ""},
		{"single newline", "\n"},
		{"blank lines", "\n\n\n"},
		{"single char", "x"},
		{"single char no newline", "a"},
		{"whitespace only", "   \n\t\n"},
		{"one word", "word"},
		{"trailing spaces", "abc   "},
		{"leading tab", "\tindented"},
	}
	for _, c := range contents {
		for _, seq := range robustnessSequences {
			name := c.name + "/" + seq.name
			t.Run(name, func(t *testing.T) {
				h, _, _ := newRobustnessHandler(t, c.content, 20, 10)
				feedKeys(t, h, seq.keys)
				assertCursorInBounds(t, h, name)
			})
		}
	}
}

// TestStandardRobustnessBoundaryPositions moves the cursor to a boundary
// (start of file, end of file, end of a line) and then drives every sequence.
func TestStandardRobustnessBoundaryPositions(t *testing.T) {
	const content = "first line\n\nthird line has more\nx\n"
	boundaries := []struct {
		name string
		move string
	}{
		{"start of file", "<meta-up>"},
		{"end of file", "<meta-down>"},
		{"end of line", "<meta-right>"},
		{"start of line", "<meta-left>"},
		{"last col of long line", "<down><down><meta-right>"},
		{"empty middle line", "<down>"},
	}
	for _, b := range boundaries {
		for _, seq := range robustnessSequences {
			name := b.name + "/" + seq.name
			t.Run(name, func(t *testing.T) {
				h, _, _ := newRobustnessHandler(t, content, 20, 10)
				feedKeys(t, h, b.move)
				feedKeys(t, h, seq.keys)
				assertCursorInBounds(t, h, name)
			})
		}
	}
}

// TestStandardRobustnessNullCells exercises the handler over buffers containing
// sparse/null cells (\x00), which arise from performance/VTE buffers. The
// cursor must report no cell on a null and every op must stay in bounds.
func TestStandardRobustnessNullCells(t *testing.T) {
	contents := []struct {
		name    string
		content string
	}{
		{"trailing nulls", "abc\x00\x00\x00"},
		{"only nulls", "\x00\x00\x00"},
		{"nulls between", "a\x00b\x00c"},
		{"null line", "one\n\x00\x00\x00\nthree"},
	}
	for _, c := range contents {
		for _, seq := range robustnessSequences {
			name := c.name + "/" + seq.name
			t.Run(name, func(t *testing.T) {
				h, _, _ := newRobustnessHandler(t, c.content, 20, 10)
				feedKeys(t, h, seq.keys)
				assertCursorInBounds(t, h, name)
			})
		}
	}
}

// TestStandardRobustnessExternalEdit drives every sequence after an out-of-band
// edit through CellEditor().Edit mutates the buffer under the cursor: deleting
// the cursor's line, replacing a range, or inserting above.
func TestStandardRobustnessExternalEdit(t *testing.T) {
	edits := []struct {
		name     string
		from, to term.Coordinates
		str      string
	}{
		{"delete cursor line and beyond", term.Coordinates{Y: 1}, term.Coordinates{Y: 6}, ""},
		{"delete whole buffer", term.Coordinates{}, term.Coordinates{Y: 6}, ""},
		{"replace range with short text", term.Coordinates{Y: 1}, term.Coordinates{Y: 5}, "z"},
		{"insert above", term.Coordinates{}, term.Coordinates{}, "new\nlines\n"},
		{"collapse to single line", term.Coordinates{}, term.Coordinates{Y: 6}, "single"},
	}
	for _, e := range edits {
		for _, seq := range robustnessSequences {
			name := e.name + "/" + seq.name
			t.Run(name, func(t *testing.T) {
				h, _, _ := newRobustnessHandler(t, "l0\nl1\nl2\nl3\nl4\nl5\nl6", 20, 10)
				feedKeys(t, h, "<meta-down>")
				h.CellEditor().Edit(context.Background(), e.from, e.to, e.str)
				assertCursorInBounds(t, h, name+" (after edit)")
				feedKeys(t, h, seq.keys)
				assertCursorInBounds(t, h, name)
			})
		}
	}
}

// TestStandardRobustnessZeroDimensions drives every sequence before Resize has
// given the handler a viewport, and after a Resize to a zero dimension. These
// paths must not panic.
func TestStandardRobustnessZeroDimensions(t *testing.T) {
	dims := []struct {
		name          string
		width, height int
	}{
		{"unsized", 0, 0},
		{"zero width", 0, 10},
		{"zero height", 20, 0},
		{"one by one", 1, 1},
	}
	for _, d := range dims {
		for _, seq := range robustnessSequences {
			name := d.name + "/" + seq.name
			t.Run(name, func(t *testing.T) {
				h, _, _ := newRobustnessHandler(t, "alpha\nbeta\ngamma", d.width, d.height)
				require.NotPanics(t, func() {
					feedKeys(t, h, seq.keys)
				}, name)
			})
		}
	}
}

// TestStandardRobustnessRepeatedOps repeats each single-key sequence many times
// to drive the cursor and buffer well past content bounds; the handler must not
// panic or leave the cursor out of bounds regardless of repetition.
func TestStandardRobustnessRepeatedOps(t *testing.T) {
	for _, seq := range robustnessSequences {
		t.Run(seq.name, func(t *testing.T) {
			h, _, _ := newRobustnessHandler(t, "aa\nbb\ncc", 20, 10)
			for range 50 {
				feedKeys(t, h, seq.keys)
			}
			assertCursorInBounds(t, h, seq.name)
		})
	}
}

// TestStandardSetCursorAtScrollClampsOutOfBounds pins that the public
// SetCursorAtScroll clamps a request past the buffer into valid bounds rather
// than leaving the cursor stranded.
func TestStandardSetCursorAtScrollClampsOutOfBounds(t *testing.T) {
	cases := []struct {
		name    string
		content string
		request term.Coordinates
		wantMax term.Coordinates
	}{
		{"row past end", "a\nb\nc\nd", term.Coordinates{Y: 999}, term.Coordinates{Y: 3}},
		{"row and col past end", "a\nb\nc\nd", term.Coordinates{Y: 999, X: 999}, term.Coordinates{Y: 3}},
		{"col past line", "hello", term.Coordinates{X: 999}, term.Coordinates{X: 5}},
		{"single char buffer", "x", term.Coordinates{Y: 50, X: 50}, term.Coordinates{X: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := newRobustnessHandler(t, tc.content, 20, 10)
			h.SetCursorAtScroll(tc.request)
			pos := h.CursorAtScroll()
			rows := h.CellView().Rows()
			require.LessOrEqual(t, pos.Y, rows-1, "row must be within buffer")
			require.GreaterOrEqual(t, pos.Y, 0)
			require.LessOrEqual(t, pos.X, h.CellView().Columns(pos.Y),
				"column must clamp to the resolved line length")
		})
	}
}

// TestStandardNullCellEditing pins that placing the cursor on a sparse/null
// cell and editing there is safe and well-defined: the cell reads back as the
// null rune and a delete removes it without panicking.
func TestStandardNullCellEditing(t *testing.T) {
	h, buf, _ := newRobustnessHandler(t, "ab\x00\x00", 20, 10)
	sh := h.(*standardHandler)

	// Column 0 holds a real rune (cursor starts at origin).
	c, ok := sh.cursor.Cell()
	require.True(t, ok)
	assert.Equal(t, 'a', c.Ch)

	// The cursor can rest on a null cell and read it back as the null rune.
	require.True(t, sh.SetCursorAtScroll(term.Coordinates{X: 2}))
	c, ok = sh.cursor.Cell()
	require.True(t, ok)
	assert.Equal(t, '\x00', c.Ch)

	// Deleting the null cell must not panic and must shrink the line.
	_, handled := h.Handle(term.Event{Type: term.EventKey, Key: term.KeyDelete})
	require.True(t, handled)
	assertCursorInBounds(t, h, "after deleting null cell")
	assert.Equal(t, "ab\x00", buf.String())
}

// TestHandleMouseWindowCoordinates verifies mouse events are
// interpreted in window coordinates: positions follow the horizontal
// scroll offset and resolve past the viewport edge with wrap off.
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
		h.Handle(mouseEvent(term.MouseLeft, 6, 0))
		h.Handle(mouseEvent(term.MouseLeft, 11, 0))
		h.Handle(mouseEvent(term.MouseRelease, 11, 0))

		assert.Equal(t, term.Coordinates{X: 11}, h.CursorAtScroll())
		sel, ok := h.Selection()
		require.True(t, ok)
		assert.Equal(t, "bravo", sel)
	})

	t.Run("leftward drag reports sorted half-open bounds", func(t *testing.T) {
		h := newEditor()
		h.Handle(mouseEvent(term.MouseLeft, 7, 0))
		h.Handle(mouseEvent(term.MouseLeft, 2, 0))
		h.Handle(mouseEvent(term.MouseRelease, 2, 0))

		sel, ok := h.Selection()
		require.True(t, ok)
		assert.Equal(t, "pha b", sel)
		from, to, bok := selectionBounds(t, h)
		require.True(t, bok)
		assert.Equal(t, term.Coordinates{X: 2}, from)
		assert.Equal(t, term.Coordinates{X: 7}, to,
			"the anchor cell sits one past the exclusive selection")
	})

	t.Run("click on a horizontally scrolled viewport follows the offset", func(t *testing.T) {
		h := newEditor()
		h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnd})
		win, _, ok := h.Cursor()
		require.True(t, ok)
		offset := h.CursorAtScroll().X - win.X
		require.Positive(t, offset, "the viewport must be scrolled right")

		h.Handle(mouseEvent(term.MouseLeft, 2, 0))
		h.Handle(mouseEvent(term.MouseRelease, 2, 0))

		assert.Equal(t, term.Coordinates{X: offset + 2}, h.CursorAtScroll())
	})
}

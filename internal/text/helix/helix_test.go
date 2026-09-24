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

package helix

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/registerset"
)

// newHelix builds a real Helix handler over content with an in-memory
// clipboard, positions the caret, and returns the handler plus the
// backing buffer and clipboard for assertions.
func newHelix(
	t *testing.T, content string, at term.Coordinates, opts ...Option,
) (*Helix, *cell.Buffer, clipboard.Register) {
	t.Helper()
	return newHelixURI(t, "test:///", content, at, opts...)
}

func newHelixURI(
	t *testing.T, uri, content string, at term.Coordinates, opts ...Option,
) (*Helix, *cell.Buffer, clipboard.Register) {
	t.Helper()
	resource, err := workspaceapi.ParseURI(uri)
	require.NoError(t, err)
	buf := cell.NewBuffer()
	buf.ReadFrom(strings.NewReader(content))
	// clipboard.NewInMemory has a single slot; registerset adds the
	// per-register addressing the " prefix needs.
	clip := registerset.New(clipboard.NewInMemory())
	hx := NewWithIndent(buf, resource, text.IndentRuneSpace, 2,
		append([]Option{WithClipboard(clip), WithTabspaces(2)}, opts...)...)
	hx.Resize(80, 20)
	if at != (term.Coordinates{}) {
		hx.SetCursorAtScroll(at)
	}
	return hx, buf, clip
}

func key(ch rune) term.Event {
	return term.Event{Type: term.EventKey, Ch: ch}
}

func modKey(mod term.Modifier, ch rune) term.Event {
	return term.Event{Type: term.EventKey, Mod: mod, Ch: ch}
}

func namedKey(k term.Key) term.Event {
	return term.Event{Type: term.EventKey, Key: k}
}

func modNamedKey(mod term.Modifier, k term.Key) term.Event {
	return term.Event{Type: term.EventKey, Mod: mod, Key: k}
}

// keys turns a literal chord string into plain key events. Modified and
// named keys are built with modKey/namedKey and appended explicitly.
func keys(s string) []term.Event {
	evs := make([]term.Event, 0, len(s))
	for _, ch := range s {
		evs = append(evs, key(ch))
	}
	return evs
}

func send(t *testing.T, hx *Helix, evs ...term.Event) {
	t.Helper()
	for _, ev := range evs {
		hx.Handle(ev)
	}
}

// sel returns the current selection text. Selection reports ok=false for
// a caret on an empty line, where the one-cell range covers no text.
func sel(t *testing.T, hx *Helix) string {
	t.Helper()
	s, _ := hx.Selection()
	return s
}

// seedClipboard puts text in the default register with the given
// selection metadata so paste tests are deterministic.
func seedClipboard(
	t *testing.T, clip clipboard.Register, str string, mode text.SelectMode,
) {
	t.Helper()
	require.NoError(t, clip.Copy(clipboard.DefaultRegisterID,
		clipboard.Data{Text: str, Metadata: mode}))
}

// testIndentView supplies the syntax indent targets ReindentSelection
// needs, which a plain cell.View does not provide.
type testIndentView struct {
	cell.View
	indents map[int]int
}

func (v testIndentView) IndentationAt(line int) (int, bool) {
	target, ok := v.indents[line]
	return target, ok
}

// testCommentView reports which lines are already commented, which is
// the syntax service Cursor.ToggleLineComment consults before it will
// uncomment anything.
type testCommentView struct {
	cell.View
	line []string
}

func (v testCommentView) CommentCoverage(rng term.Range) ([]term.Range, bool) {
	start, end := term.CoordinatesSort(rng.Start, rng.End)
	var ranges []term.Range
	for y := start.Y; y <= end.Y && y < v.Rows(); y++ {
		line := term.CellsToString([][]term.Cell{v.RawCells()[y]})
		trimmed := strings.TrimLeft(line, " \t")
		indent := len(line) - len(trimmed)
		for _, prefix := range v.line {
			if !strings.HasPrefix(trimmed, prefix) {
				continue
			}
			ranges = append(ranges, term.Range{
				Start: term.Coordinates{Y: y, X: indent},
				End:   term.Coordinates{Y: y, X: len(line)},
			})
			break
		}
	}
	if len(ranges) == 0 {
		return nil, false
	}
	return ranges, true
}

// testSelectionView supplies the syntax-tree expansion A-o / A-i walk.
type testSelectionView struct {
	cell.View
	expand map[term.Range]term.Range
	shrink map[term.Range]term.Range
}

func (v testSelectionView) SelectionExpand(rng term.Range) (term.Range, bool) {
	next, ok := v.expand[rng]
	return next, ok
}

func (v testSelectionView) SelectionShrink(
	rng term.Range, caret term.Coordinates,
) (term.Range, bool) {
	next, ok := v.shrink[rng]
	return next, ok
}

// TestHandlerSatisfiesTextHandler pins the interface the IDE depends on.
func TestHandlerSatisfiesTextHandler(t *testing.T) {
	var _ text.Handler = (*Helix)(nil)
	hx, _, _ := newHelix(t, "abc", term.Coordinates{})
	assert.Equal(t, "test:///", hx.Resource().String())
	assert.NotNil(t, hx.CellView())
	assert.NotNil(t, hx.CellEditor())
}

// TestSetCursorAtScroll pins clamping and the pending-position path.
func TestSetCursorAtScroll(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		at      term.Coordinates
		want    term.Coordinates
	}{
		{name: "in bounds", content: "abc\ndef",
			at: term.Coordinates{X: 1, Y: 1}, want: term.Coordinates{X: 1, Y: 1}},
		{name: "clamps a negative column", content: "abc",
			at: term.Coordinates{X: -5}, want: term.Coordinates{}},
		{name: "clamps a negative row", content: "abc",
			at: term.Coordinates{Y: -5}, want: term.Coordinates{}},
		{name: "clamps a row past the end", content: "abc\ndef",
			at: term.Coordinates{Y: 99}, want: term.Coordinates{Y: 1}},
		{name: "clamps a column past the end", content: "abc",
			at: term.Coordinates{X: 99}, want: term.Coordinates{X: 3}},
		{name: "clamps both", content: "ab\ncd",
			at: term.Coordinates{X: 99, Y: 99}, want: term.Coordinates{X: 2, Y: 1}},
		{name: "an empty line pins column zero", content: "a\n\nb",
			at: term.Coordinates{X: 4, Y: 1}, want: term.Coordinates{Y: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hx, _, _ := newHelix(t, tc.content, term.Coordinates{})
			hx.SetCursorAtScroll(tc.at)
			assert.Equal(t, tc.want, hx.CursorAtScroll())
		})
	}

	t.Run("a position set before the first resize is applied later", func(t *testing.T) {
		resource, err := workspaceapi.ParseURI("test:///")
		require.NoError(t, err)
		buf := cell.NewBuffer()
		buf.ReadFrom(strings.NewReader("one\ntwo\nthree"))
		hx := NewWithIndent(buf, resource, text.IndentRuneSpace, 2,
			WithClipboard(registerset.New(clipboard.NewInMemory())))

		assert.False(t, hx.SetCursorAtScroll(term.Coordinates{X: 1, Y: 2}),
			"the scroll has no size yet")
		hx.Resize(80, 20)
		assert.Equal(t, term.Coordinates{X: 1, Y: 2}, hx.CursorAtScroll())
		_, ok := hx.cursor.SelectionMode()
		assert.True(t, ok, "resize restores the selection invariant")
	})

	t.Run("a user event cancels a pending position", func(t *testing.T) {
		resource, err := workspaceapi.ParseURI("test:///")
		require.NoError(t, err)
		buf := cell.NewBuffer()
		buf.ReadFrom(strings.NewReader("one\ntwo\nthree"))
		hx := NewWithIndent(buf, resource, text.IndentRuneSpace, 2,
			WithClipboard(registerset.New(clipboard.NewInMemory())))

		hx.SetCursorAtScroll(term.Coordinates{X: 1, Y: 2})
		hx.Handle(key('l'))
		hx.Resize(80, 20)
		assert.Equal(t, 0, hx.CursorAtScroll().Y,
			"the pending position was dropped by the keystroke")
	})
}

// TestLocationLists pins the location plumbing the IDE drives.
func TestLocationLists(t *testing.T) {
	locs := textapi.LocationSlice([]textapi.Location{
		{From: term.Coordinates{Y: 0}, To: term.Coordinates{X: 1, Y: 0}},
		{From: term.Coordinates{Y: 2}, To: term.Coordinates{X: 1, Y: 2}},
	})

	t.Run("next and previous walk the list", func(t *testing.T) {
		hx, _, _ := newHelix(t, "a\nb\nc", term.Coordinates{})
		hx.SetLocationList(textapi.LocationPriorityError, "diag", locs)
		require.True(t, hx.MoveToNextLocation("diag"))
		assert.Equal(t, 2, hx.CursorAtScroll().Y)
		require.True(t, hx.MoveToPrevLocation("diag"))
		assert.Equal(t, 0, hx.CursorAtScroll().Y)
	})

	t.Run("an unknown list is inert", func(t *testing.T) {
		hx, _, _ := newHelix(t, "a\nb\nc", term.Coordinates{})
		assert.False(t, hx.MoveToNextLocation("nope"))
	})

	t.Run("the lists are reported back", func(t *testing.T) {
		hx, _, _ := newHelix(t, "a\nb\nc", term.Coordinates{})
		hx.SetLocationList(textapi.LocationPriorityError, "diag", locs)
		var ids []string
		for _, set := range hx.LocationLists() {
			ids = append(ids, set.ID)
		}
		assert.Contains(t, ids, "diag")
	})

	t.Run("g. jumps to the last change", func(t *testing.T) {
		hx, _, _ := newHelix(t, "a\nb\nc", term.Coordinates{Y: 2})
		send(t, hx, key('d'))
		send(t, hx, keys("gg")...)
		require.Equal(t, 0, hx.CursorAtScroll().Y)
		send(t, hx, keys("g.")...)
		assert.Equal(t, 2, hx.CursorAtScroll().Y)
	})
}

// TestDrawAndDimensions exercises the component surface.
func TestDrawAndDimensions(t *testing.T) {
	t.Run("draws the buffer", func(t *testing.T) {
		hx, _, _ := newHelix(t, "one\ntwo", term.Coordinates{})
		hx.Resize(10, 4)
		w := term.NewStringWriter(10, 4)
		hx.Draw(w)
		require.NoError(t, w.Flush())
		assert.Contains(t, w.String(), "one")
		assert.Contains(t, w.String(), "two")
	})

	t.Run("dimensions follow the content", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abcdef\nxy", term.Coordinates{})
		width, height := hx.Dimensions()
		assert.Equal(t, 6, width)
		assert.Equal(t, 2, height)
	})

	t.Run("the cursor style tracks the mode", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abc", term.Coordinates{})
		_, style, visible := hx.Cursor()
		require.True(t, visible)
		assert.Equal(t, term.CursorStyleDefault, style)

		send(t, hx, key('i'))
		_, style, _ = hx.Cursor()
		assert.Equal(t, term.CursorStyleSteadyBar, style)

		send(t, hx, namedKey(term.KeyEsc))
		send(t, hx, key('g'))
		_, style, _ = hx.Cursor()
		assert.Equal(t, term.CursorStyleSteadyUnderline, style)
	})

	t.Run("close is idempotent", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abc", term.Coordinates{})
		hx.Close()
		hx.Close()
	})

	t.Run("a zero sized resize is survivable", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abc", term.Coordinates{})
		hx.Resize(0, 0)
		hx.Resize(80, 20)
		send(t, hx, key('l'))
		assert.Equal(t, term.Coordinates{X: 1}, hx.CursorAtScroll())
	})

	t.Run("wrap can be toggled", func(t *testing.T) {
		hx, _, _ := newHelix(t, strings.Repeat("x", 200), term.Coordinates{})
		hx.Resize(20, 10)
		hx.SetWrap(true)
		hx.Draw(term.NewStringWriter(20, 10))
		hx.SetWrap(false)
		hx.Draw(term.NewStringWriter(20, 10))
	})
}

// TestSeekSurface pins the scrollable API the IDE reads.
func TestSeekSurface(t *testing.T) {
	hx, _, _ := newHelix(t, strings.Repeat("line\n", 200), term.Coordinates{})
	assert.Equal(t, 0, hx.SeekOffset())
	assert.Greater(t, hx.MaxSeekOffset(), 0)

	hx.SeekDown()
	assert.Equal(t, 1, hx.SeekOffset())
	hx.SeekUp()
	assert.Equal(t, 0, hx.SeekOffset())
}

// TestConstructors pins the entry points the IDE uses to build a Helix
// editor, including the Scroll-backed variant used by embedded views.
func TestConstructors(t *testing.T) {
	resource, err := workspaceapi.ParseURI("test:///main.go")
	require.NoError(t, err)

	t.Run("New defaults to tab indentation", func(t *testing.T) {
		buf := cell.NewBuffer()
		buf.ReadFrom(strings.NewReader("abc"))
		hx := New(buf, resource)
		hx.Resize(80, 20)
		assert.Equal(t, text.IndentRuneTab, hx.config.indentRune)
		assert.True(t, hx.IsNormalMode())
		send(t, hx, key('l'))
		assert.Equal(t, term.Coordinates{X: 1}, hx.CursorAtScroll())
	})

	t.Run("InitWithScroll shares an existing scroll", func(t *testing.T) {
		buf := cell.NewBuffer()
		buf.ReadFrom(strings.NewReader("one\ntwo"))
		scroll := component.NewScroll(buf)

		hx := new(Helix)
		hx.InitWithScroll(scroll, resource, text.IndentRuneSpace, 2)
		hx.Resize(80, 20)
		send(t, hx, key('j'))
		assert.Equal(t, term.Coordinates{Y: 1}, hx.CursorAtScroll())
	})

	t.Run("initial folds are requested when enabled", func(t *testing.T) {
		buf := cell.NewBuffer()
		buf.ReadFrom(strings.NewReader("one\ntwo"))
		// The plain view exposes no folds service, so this only has to
		// stay inert.
		hx := NewWithIndent(buf, resource, text.IndentRuneSpace, 2,
			WithHideInitialFolds(true))
		hx.Resize(80, 20)
		assert.True(t, hx.IsNormalMode())
	})
}

// TestUndoRedo pins u/U and the Alt variants, which the wrapper claims
// before the state machine sees them.
func TestUndoRedo(t *testing.T) {
	t.Run("u undoes the last edit", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "abc", term.Coordinates{})
		send(t, hx, key('d'))
		require.Equal(t, "bc", buf.String())
		send(t, hx, key('u'))
		assert.Equal(t, "abc", buf.String())
	})

	t.Run("U redoes", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "abc", term.Coordinates{})
		send(t, hx, keys("du")...)
		require.Equal(t, "abc", buf.String())
		send(t, hx, key('U'))
		assert.Equal(t, "bc", buf.String())
	})

	t.Run("a counted undo walks back several edits", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "abcd", term.Coordinates{})
		send(t, hx, keys("ddd")...)
		require.Equal(t, "d", buf.String())
		send(t, hx, keys("3u")...)
		assert.Equal(t, "abcd", buf.String())
	})

	t.Run("alt-u is undo with a count", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "abcd", term.Coordinates{})
		send(t, hx, keys("ddd")...)
		send(t, hx, key('2'))
		send(t, hx, modKey(term.ModAlt, 'u'))
		assert.Equal(t, "bcd", buf.String())
	})

	t.Run("undo on a pristine buffer is inert", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "abc", term.Coordinates{})
		send(t, hx, key('u'))
		assert.Equal(t, "abc", buf.String())
	})

	t.Run("an insert session undoes as one edit", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "", term.Coordinates{})
		send(t, hx, keys("ihello")...)
		send(t, hx, namedKey(term.KeyEsc))
		require.Equal(t, "hello", buf.String())
		send(t, hx, key('u'))
		assert.Equal(t, "", buf.String())
	})

	t.Run("ctrl-s splits an insert session into two undo steps", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "", term.Coordinates{})
		send(t, hx, keys("iab")...)
		send(t, hx, modKey(term.ModCtrl, 's'))
		send(t, hx, keys("cd")...)
		send(t, hx, namedKey(term.KeyEsc))
		require.Equal(t, "abcd", buf.String())
		send(t, hx, key('u'))
		assert.Equal(t, "ab", buf.String())
	})

	t.Run("undo does not write to the clipboard", func(t *testing.T) {
		hx, _, clip := newHelix(t, "abc", term.Coordinates{})
		send(t, hx, key('d'))
		seedClipboard(t, clip, "KEEP", text.StandardSelection)
		send(t, hx, key('u'))
		data, err := clip.Paste(clipboard.DefaultRegisterID)
		require.NoError(t, err)
		assert.Equal(t, "KEEP", data.Text)
	})
}

// TestDotRepeat pins `.`, which replays the last insert session.
func TestDotRepeat(t *testing.T) {
	t.Run("repeats the last insert", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "", term.Coordinates{})
		send(t, hx, keys("ihi")...)
		send(t, hx, namedKey(term.KeyEsc))
		require.Equal(t, "hi", buf.String())
		send(t, hx, key('.'))
		assert.Equal(t, "hhii", buf.String())
	})

	t.Run("with nothing recorded it is unhandled", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abc", term.Coordinates{})
		_, handled := hx.Handle(key('.'))
		assert.False(t, handled)
	})

	t.Run("a repeat stays in normal mode", func(t *testing.T) {
		hx, _, _ := newHelix(t, "", term.Coordinates{})
		send(t, hx, keys("ix")...)
		send(t, hx, namedKey(term.KeyEsc))
		send(t, hx, key('.'))
		assert.True(t, hx.IsNormalMode())
	})
}

// TestSelectionBoundsAndPaste pins the two thin wrapper methods the IDE
// calls directly.
func TestSelectionBoundsAndPaste(t *testing.T) {
	t.Run("SelectionBounds reports the raw anchor and head", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar", term.Coordinates{})
		send(t, hx, key('w'))
		anchor, head, ok := hx.SelectionBounds()
		require.True(t, ok)
		assert.Equal(t, term.Coordinates{}, anchor)
		assert.Equal(t, term.Coordinates{X: 4}, head,
			"the reported head is the exclusive edge")
	})

	t.Run("Paste reads through to the register", func(t *testing.T) {
		hx, _, clip := newHelix(t, "abc", term.Coordinates{})
		seedClipboard(t, clip, "X", text.StandardSelection)
		data, err := hx.Paste(clipboard.DefaultRegisterID)
		require.NoError(t, err)
		assert.Equal(t, "X", data.Text)
	})

	t.Run("SetDefaultAttributes is accepted", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abc", term.Coordinates{})
		hx.SetDefaultAttributes(term.Attributes{Fg: term.ColorRed})
		hx.ShowCommandBar(true)
		hx.SetMessage("hello")
	})
}

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
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/text"
)

// TestSelectionInvariantHolds is the single rule the whole grammar rests
// on: normal mode always owns at least the cell under the caret.
func TestSelectionInvariantHolds(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		at      term.Coordinates
		evs     []term.Event
	}{
		{name: "fresh handler", content: "abc"},
		{name: "after a motion", content: "foo bar", evs: keys("w")},
		{name: "after a delete", content: "foo bar", evs: keys("wd")},
		{name: "after a paste", content: "foo", evs: keys("yp")},
		{name: "after undo", content: "abc", evs: keys("du")},
		{name: "after leaving insert", content: "abc",
			evs: []term.Event{key('i'), key('X'), namedKey(term.KeyEsc)}},
		{name: "after esc in select mode", content: "abc",
			evs: []term.Event{key('v'), key('l'), namedKey(term.KeyEsc)}},
		{name: "after a failed motion at the buffer start", content: "abc",
			evs: keys("hhhh")},
		{name: "after a failed motion at the buffer end", content: "abc",
			evs: keys("llllll")},
		{name: "on an empty line", content: "a\n\nb", evs: keys("j")},
		{name: "on a wide glyph", content: "世界", evs: keys("l")},
		{name: "after select all", content: "a\nb", evs: keys("%")},
		{name: "after a text object", content: "foo bar", evs: keys("miw")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hx, _, _ := newHelix(t, tc.content, tc.at)
			send(t, hx, tc.evs...)
			_, ok := hx.cursor.SelectionMode()
			assert.True(t, ok || hx.buf.Rows() == 0,
				"normal mode must always own a selection")
		})
	}
}

// TestEmptyAndDegenerateBuffers walks every command over buffers that
// have nothing, or almost nothing, to act on.
func TestEmptyAndDegenerateBuffers(t *testing.T) {
	contents := []string{"", "\n", "\n\n\n", "a", "\t", " "}
	// One representative key from every dispatch arm.
	events := []term.Event{
		key('h'), key('j'), key('k'), key('l'),
		key('w'), key('W'), key('e'), key('E'), key('b'), key('B'),
		key('x'), key('X'), key('%'), key('v'), key(';'),
		key('d'), key('c'), key('y'), key('p'), key('P'), key('R'),
		key('~'), key('`'), key('J'), key('>'), key('<'), key('='),
		key('n'), key('N'), key('G'), key('u'), key('U'), key('.'),
		namedKey(term.KeyHome), namedKey(term.KeyEnd),
		namedKey(term.KeyPgup), namedKey(term.KeyPgdn),
		namedKey(term.KeyArrowUp), namedKey(term.KeyArrowDown),
		namedKey(term.KeyArrowLeft), namedKey(term.KeyArrowRight),
		namedKey(term.KeyTab), namedKey(term.KeyEsc),
		modKey(term.ModCtrl, 'a'), modKey(term.ModCtrl, 'x'),
		modKey(term.ModCtrl, 'b'), modKey(term.ModCtrl, 'f'),
		modKey(term.ModCtrl, 'u'), modKey(term.ModCtrl, 'd'),
		modKey(term.ModCtrl, 'e'), modKey(term.ModCtrl, 'y'),
		modKey(term.ModCtrl, 's'), modKey(term.ModCtrl, 'o'),
		modKey(term.ModCtrl, 'i'), modKey(term.ModCtrl, 'c'),
		modKey(term.ModAlt, 'd'), modKey(term.ModAlt, 'c'),
		modKey(term.ModAlt, ';'), modKey(term.ModAlt, ':'),
		modKey(term.ModAlt, 'x'), modKey(term.ModAlt, 'o'),
		modKey(term.ModAlt, 'i'), modKey(term.ModAlt, '`'),
		modKey(term.ModAlt, '.'), modKey(term.ModAlt, 'J'),
		modKey(term.ModAlt, '*'),
	}
	// Minor modes reached through a prefix key.
	prefixes := [][]term.Event{
		{key('g'), key('g')}, {key('g'), key('e')}, {key('g'), key('h')},
		{key('g'), key('l')}, {key('g'), key('s')}, {key('g'), key('|')},
		{key('g'), key('t')}, {key('g'), key('c')}, {key('g'), key('b')},
		{key('g'), key('j')}, {key('g'), key('k')}, {key('g'), key('.')},
		{key('m'), key('m')}, {key('m'), key('i'), key('w')},
		{key('m'), key('a'), key('w')}, {key('m'), key('s'), key('(')},
		{key('m'), key('d'), key('(')}, {key('m'), key('r'), key('('), key('[')},
		{key('z'), key('z')}, {key('z'), key('t')}, {key('z'), key('b')},
		{key('['), namedKey(term.KeySpace)}, {key(']'), namedKey(term.KeySpace)},
		{key('['), key('p')}, {key(']'), key('p')},
		{key('r'), key('z')}, {key('f'), key('z')}, {key('t'), key('z')},
		{key('F'), key('z')}, {key('T'), key('z')},
		{key('"'), key('a'), key('y')},
	}

	for _, content := range contents {
		for _, ev := range events {
			hx, _, _ := newHelix(t, content, term.Coordinates{})
			require.NotPanics(t, func() { hx.Handle(ev) },
				"content %q event %v", content, ev)
		}
		for _, seq := range prefixes {
			hx, _, _ := newHelix(t, content, term.Coordinates{})
			require.NotPanics(t, func() { send(t, hx, seq...) },
				"content %q sequence %v", content, seq)
		}
	}
}

// TestOutOfBoundsCaret drives commands from positions the IDE can hand
// over after an out-of-band edit.
func TestOutOfBoundsCaret(t *testing.T) {
	positions := []term.Coordinates{
		{X: -1, Y: -1}, {X: 1000, Y: 0}, {X: 0, Y: 1000},
		{X: 1000, Y: 1000}, {X: -5, Y: 2},
	}
	for _, at := range positions {
		for _, ev := range []term.Event{
			key('w'), key('b'), key('e'), key('d'), key('x'), key('J'),
			key('%'), key('j'), key('k'), modKey(term.ModCtrl, 'a'),
		} {
			hx, _, _ := newHelix(t, "one\n\nthree", term.Coordinates{})
			hx.SetCursorAtScroll(at)
			require.NotPanics(t, func() { hx.Handle(ev) },
				"at %v event %v", at, ev)
			pos := hx.CursorAtScroll()
			assert.GreaterOrEqual(t, pos.X, 0)
			assert.GreaterOrEqual(t, pos.Y, 0)
		}
	}
}

// TestWideAndControlCharacters pins the motions over content the cell
// grid stores in more than one column, or not at all.
func TestWideAndControlCharacters(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		evs     []term.Event
		wantSel string
	}{
		{name: "l steps one glyph at a time", content: "世界x",
			evs: keys("l"), wantSel: "界"},
		{name: "w over CJK", content: "世界 x", evs: keys("w"), wantSel: "世界 "},
		{name: "d removes a whole glyph", content: "世界", evs: keys("d")},
		{name: "a tab is one cell", content: "a\tb", evs: keys("l"),
			wantSel: "\t"},
		{name: "w over a tab", content: "a\tb", evs: keys("w"), wantSel: "a\t"},
		{name: "e over mixed scripts", content: "héllo wörld", evs: keys("e"),
			wantSel: "héllo"},
		{name: "w over an emoji", content: "a 🙂 b", evs: keys("w"), wantSel: "a "},
		{name: "% selects everything including wide glyphs",
			content: "世界\nxy", evs: keys("%"), wantSel: "世界\nxy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hx, _, _ := newHelix(t, tc.content, term.Coordinates{})
			send(t, hx, tc.evs...)
			if tc.wantSel != "" {
				assert.Equal(t, tc.wantSel, sel(t, hx))
			}
		})
	}

	t.Run("a NUL byte does not derail a motion", func(t *testing.T) {
		hx, _, _ := newHelix(t, "a\x00b c", term.Coordinates{})
		require.NotPanics(t, func() { send(t, hx, keys("wwbb")...) })
	})

	t.Run("very long lines are navigable", func(t *testing.T) {
		hx, _, _ := newHelix(t, strings.Repeat("word ", 500), term.Coordinates{})
		send(t, hx, keys("100w")...)
		assert.Greater(t, hx.CursorAtScroll().X, 0)
	})
}

// TestLargeCounts pins counts that overrun the buffer.
func TestLargeCounts(t *testing.T) {
	for _, evs := range [][]term.Event{
		keys("999j"), keys("999k"), keys("999l"), keys("999h"),
		keys("999w"), keys("999b"), keys("999e"), keys("999x"),
		keys("999>"), keys("999<"), keys("999G"),
	} {
		hx, _, _ := newHelix(t, "one\ntwo\nthree", term.Coordinates{Y: 1})
		require.NotPanics(t, func() { send(t, hx, evs...) }, "%v", evs)
		pos := hx.CursorAtScroll()
		assert.Less(t, pos.Y, 3)
		assert.GreaterOrEqual(t, pos.Y, 0)
	}

	t.Run("a multi digit count accumulates", func(t *testing.T) {
		hx, _, _ := newHelix(t, strings.Repeat("x\n", 300), term.Coordinates{})
		send(t, hx, keys("123j")...)
		assert.Equal(t, 123, hx.CursorAtScroll().Y)
	})

	t.Run("a leading zero is not a count", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abc", term.Coordinates{X: 2})
		_, handled := hx.Handle(key('0'))
		assert.False(t, handled, "0 is goto-line-start in Helix's gh, not a count")
	})

	t.Run("the count resets after the command", func(t *testing.T) {
		hx, _, _ := newHelix(t, strings.Repeat("x\n", 20), term.Coordinates{})
		send(t, hx, keys("3j")...)
		require.Equal(t, 3, hx.CursorAtScroll().Y)
		send(t, hx, key('j'))
		assert.Equal(t, 4, hx.CursorAtScroll().Y)
	})

	t.Run("an abandoned count does not leak", func(t *testing.T) {
		hx, _, _ := newHelix(t, strings.Repeat("x\n", 20), term.Coordinates{})
		send(t, hx, key('3'))
		send(t, hx, namedKey(term.KeyEsc))
		send(t, hx, key('j'))
		assert.Equal(t, 1, hx.CursorAtScroll().Y)
	})
}

// TestNonKeyEvents pins the events the runtime delivers that are not
// keystrokes.
func TestNonKeyEvents(t *testing.T) {
	for _, ev := range []term.Event{
		{Type: term.EventResize},
		{Type: term.EventError},
		{Type: term.EventInterrupt},
	} {
		hx, buf, _ := newHelix(t, "abc", term.Coordinates{})
		require.NotPanics(t, func() { hx.Handle(ev) })
		assert.Equal(t, "abc", buf.String())
	}

	t.Run("a zero key event in normal mode is unhandled", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abc", term.Coordinates{})
		_, handled := hx.Handle(term.Event{Type: term.EventKey})
		assert.False(t, handled)
	})

	t.Run("a zero key event does not resolve a pending prefix", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "abc", term.Coordinates{})
		send(t, hx, key('r'), term.Event{Type: term.EventKey})
		assert.Equal(t, "abc", buf.String())
		assert.True(t, hx.IsNormalMode())
	})
}

// TestScrollSubscriber pins the callbacks component.Scroll delivers when
// folds open and close under the caret.
func TestScrollSubscriber(t *testing.T) {
	hx, _, _ := newHelix(t, strings.Repeat("line\n", 40), term.Coordinates{Y: 5})
	impl := hx.handler.(*helixHandlerImpl)

	require.NotPanics(t, func() {
		impl.OnWillSeek(term.Coordinates{})
		impl.OnDidSeek(term.Coordinates{}, term.Coordinates{Y: 1})
		impl.OnWillHide(1, 3)
		impl.OnDidHide(1, 3)
		impl.OnWillVisible(1)
		impl.OnDidVisible(1)
	})
	assert.Equal(t, hx.CursorAtScroll(), impl.anchor,
		"the desired column is resynced after a fold change")
}

// TestModeTransitions pins the normal / select / insert cycle and the
// status each mode reports through the public predicates.
func TestModeTransitions(t *testing.T) {
	t.Run("a fresh handler starts in normal mode", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abc", term.Coordinates{})
		assert.True(t, hx.IsNormalMode())
		assert.False(t, hx.IsSelectMode())
		assert.False(t, hx.IsEditMode())
		assert.False(t, hx.IsSearchMode())
	})

	t.Run("v toggles select mode", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abc", term.Coordinates{})
		send(t, hx, key('v'))
		assert.True(t, hx.IsSelectMode())
		assert.True(t, hx.IsNormalMode(), "select mode is normal mode with extend")
		send(t, hx, key('v'))
		assert.False(t, hx.IsSelectMode())
	})

	t.Run("esc leaves select mode", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abc", term.Coordinates{})
		send(t, hx, key('v'))
		send(t, hx, namedKey(term.KeyEsc))
		assert.False(t, hx.IsSelectMode())
	})

	t.Run("esc leaves insert mode", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abc", term.Coordinates{})
		send(t, hx, key('i'))
		require.True(t, hx.IsEditMode())
		send(t, hx, namedKey(term.KeyEsc))
		assert.True(t, hx.IsNormalMode())
		assert.False(t, hx.IsEditMode())
	})

	t.Run("an operator drops select mode", func(t *testing.T) {
		for _, ev := range []term.Event{
			key('d'), key('y'), key('~'), key('`'), key('>'),
			key('<'), key('p'), key('P'), key('R'),
			modKey(term.ModCtrl, 'c'),
		} {
			hx, _, _ := newHelix(t, "foo bar\nbaz", term.Coordinates{})
			send(t, hx, key('v'), key('w'))
			require.True(t, hx.IsSelectMode())
			send(t, hx, ev)
			assert.False(t, hx.IsSelectMode(), "%v must exit select mode", ev)
		}
	})

	// join_selections and format_selections are the operators Helix
	// deliberately leaves select mode alone for.
	t.Run("J and = keep select mode", func(t *testing.T) {
		for _, ev := range []term.Event{key('J'), key('=')} {
			hx, _, _ := newHelix(t, "foo bar\nbaz", term.Coordinates{})
			send(t, hx, key('v'), key('w'))
			require.True(t, hx.IsSelectMode())
			send(t, hx, ev)
			assert.True(t, hx.IsSelectMode(), "%v must stay in select mode", ev)
		}
	})

	t.Run("an increment that changes nothing keeps select mode", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar", term.Coordinates{})
		send(t, hx, key('v'), key('w'))
		require.True(t, hx.IsSelectMode())
		send(t, hx, modKey(term.ModCtrl, 'a'))
		assert.True(t, hx.IsSelectMode())

		hx2, _, _ := newHelix(t, "41 n", term.Coordinates{})
		send(t, hx2, key('v'), key('e'))
		require.True(t, hx2.IsSelectMode())
		send(t, hx2, modKey(term.ModCtrl, 'a'))
		assert.False(t, hx2.IsSelectMode())
	})

	t.Run("SetNormalMode resets every pending state", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abc", term.Coordinates{})
		send(t, hx, key('v'), key('"'))
		hx.SetNormalMode()
		assert.True(t, hx.IsNormalMode())
		assert.False(t, hx.IsSelectMode())
		// The abandoned register prefix must not swallow the next key.
		send(t, hx, key('l'))
		assert.Equal(t, term.Coordinates{X: 1}, hx.CursorAtScroll())
	})

	// Helix's minor modes are one shot unless the sticky Z variant is used.
	for _, tc := range []struct {
		name  string
		enter term.Event
		key   term.Event
	}{
		{name: "goto", enter: key('g'), key: key('h')},
		{name: "match", enter: key('m'), key: key('m')},
		{name: "view", enter: key('z'), key: key('z')},
		{name: "replace", enter: key('r'), key: key('x')},
		{name: "bracket forward", enter: key(']'), key: key('p')},
		{name: "bracket backward", enter: key('['), key: key('p')},
	} {
		t.Run(tc.name+" mode is one shot", func(t *testing.T) {
			hx, _, _ := newHelix(t, "one two\n\nthree", term.Coordinates{})
			send(t, hx, tc.enter)
			assert.False(t, hx.IsNormalMode(), "still in the minor mode")
			send(t, hx, tc.key)
			assert.True(t, hx.IsNormalMode())
		})

		t.Run(tc.name+" mode exits on esc", func(t *testing.T) {
			hx, _, _ := newHelix(t, "one two\n\nthree", term.Coordinates{})
			send(t, hx, tc.enter)
			send(t, hx, namedKey(term.KeyEsc))
			assert.True(t, hx.IsNormalMode())
		})

		t.Run(tc.name+" mode exits on an unbound key", func(t *testing.T) {
			hx, _, _ := newHelix(t, "one two\n\nthree", term.Coordinates{})
			send(t, hx, tc.enter)
			send(t, hx, key('Ω'))
			assert.True(t, hx.IsNormalMode())
		})
	}
}

// TestInsertModeKeys walks the insert-mode key table.
func TestInsertModeKeys(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		at      term.Coordinates
		evs     []term.Event
		want    string
		wantAt  *term.Coordinates
	}{
		{name: "printable characters insert", content: "", evs: keys("iabc"),
			want: "abc"},
		{name: "space inserts a space", content: "ab",
			evs: []term.Event{key('i'), namedKey(term.KeySpace)}, want: " ab"},
		{name: "enter splits the line", content: "ab",
			at:  term.Coordinates{X: 1},
			evs: []term.Event{key('i'), namedKey(term.KeyEnter)}, want: "a\nb"},
		{name: "ctrl-j also splits the line", content: "ab",
			at:  term.Coordinates{X: 1},
			evs: []term.Event{key('i'), modKey(term.ModCtrl, 'j')}, want: "a\nb"},
		// Without an indent service TryIndent declines and the handler
		// falls back to a literal indent rune.
		{name: "tab inserts the indent rune", content: "ab",
			evs: []term.Event{key('i'), namedKey(term.KeyTab)}, want: " ab"},
		{name: "shift-tab inserts the indent rune", content: "ab",
			evs:  []term.Event{key('i'), modNamedKey(term.ModShift, term.KeyTab)},
			want: " ab"},
		{name: "backspace deletes the previous cell", content: "abc",
			at:  term.Coordinates{X: 2},
			evs: []term.Event{key('i'), namedKey(term.KeyBackspace)}, want: "ac"},
		{name: "shift-backspace deletes too", content: "abc",
			at:   term.Coordinates{X: 2},
			evs:  []term.Event{key('i'), modNamedKey(term.ModShift, term.KeyBackspace)},
			want: "ac"},
		{name: "ctrl-h deletes the previous cell", content: "abc",
			at:  term.Coordinates{X: 2},
			evs: []term.Event{key('i'), modKey(term.ModCtrl, 'h')}, want: "ac"},
		{name: "delete removes the cell under the caret", content: "abc",
			evs: []term.Event{key('i'), namedKey(term.KeyDelete)}, want: "bc"},
		{name: "ctrl-d removes the cell under the caret", content: "abc",
			evs: []term.Event{key('i'), modKey(term.ModCtrl, 'd')}, want: "bc"},
		{name: "backspace at the buffer start is inert", content: "abc",
			evs: []term.Event{key('i'), namedKey(term.KeyBackspace)}, want: "abc"},
		{name: "backspace joins lines", content: "ab\ncd",
			at:  term.Coordinates{Y: 1},
			evs: []term.Event{key('i'), namedKey(term.KeyBackspace)}, want: "abcd"},
		{name: "ctrl-w deletes the previous word", content: "foo bar",
			at:  term.Coordinates{X: 7},
			evs: []term.Event{key('a'), modKey(term.ModCtrl, 'w')}, want: "foo "},
		{name: "alt-backspace deletes the previous word", content: "foo bar",
			at:   term.Coordinates{X: 7},
			evs:  []term.Event{key('a'), modNamedKey(term.ModAlt, term.KeyBackspace)},
			want: "foo "},
		{name: "alt-d deletes the next word", content: "foo bar",
			evs:  []term.Event{key('i'), modKey(term.ModAlt, 'd')},
			want: " bar"},
		{name: "alt-delete deletes the next word", content: "foo bar",
			evs:  []term.Event{key('i'), modNamedKey(term.ModAlt, term.KeyDelete)},
			want: " bar"},
		{name: "ctrl-u kills back to the first non blank", content: "  foo\nx",
			at:  term.Coordinates{X: 4},
			evs: []term.Event{key('a'), modKey(term.ModCtrl, 'u')}, want: "  \nx"},
		{name: "ctrl-u at the first non blank kills the indent", content: "  foo",
			at:  term.Coordinates{X: 2},
			evs: []term.Event{key('i'), modKey(term.ModCtrl, 'u')}, want: "foo"},
		{name: "ctrl-u at column zero joins the previous line", content: "ab\ncd",
			at:  term.Coordinates{Y: 1},
			evs: []term.Event{key('i'), modKey(term.ModCtrl, 'u')}, want: "abcd"},
		{name: "ctrl-u at the buffer start is inert", content: "ab",
			evs: []term.Event{key('i'), modKey(term.ModCtrl, 'u')}, want: "ab"},
		{name: "ctrl-k kills to the line end", content: "abcd",
			at:  term.Coordinates{X: 2},
			evs: []term.Event{key('i'), modKey(term.ModCtrl, 'k')}, want: "ab"},
		{name: "ctrl-k at the line end joins", content: "ab\ncd",
			at:  term.Coordinates{X: 1},
			evs: []term.Event{key('a'), modKey(term.ModCtrl, 'k')}, want: "abcd"},
		{name: "insert accepts wide glyphs", content: "",
			evs: []term.Event{key('i'), key('世'), key('界')}, want: "世界"},
		{name: "backspace removes a whole wide glyph", content: "世界",
			at:  term.Coordinates{X: 1},
			evs: []term.Event{key('i'), namedKey(term.KeyBackspace)}, want: "界"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hx, buf, _ := newHelix(t, tc.content, tc.at)
			send(t, hx, tc.evs...)
			assert.Equal(t, tc.want, buf.String())
			if tc.wantAt != nil {
				assert.Equal(t, *tc.wantAt, hx.CursorAtScroll())
			}
		})
	}

	t.Run("tab snaps to the syntax indent level", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "ab", term.Coordinates{})
		buf.WithView(testIndentView{View: buf.View(), indents: map[int]int{0: 0}})
		send(t, hx, key('i'), namedKey(term.KeyTab))
		assert.Equal(t, "  ab", buf.String())
	})

	t.Run("ctrl-c leaves insert mode", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "ab", term.Coordinates{})
		send(t, hx, keys("iX")...)
		send(t, hx, modKey(term.ModCtrl, 'c'))
		assert.True(t, hx.IsNormalMode())
		assert.Equal(t, "Xab", buf.String())
	})

	t.Run("arrows move without leaving insert mode", func(t *testing.T) {
		for _, k := range []term.Key{
			term.KeyArrowLeft, term.KeyArrowRight,
			term.KeyArrowUp, term.KeyArrowDown,
			term.KeyHome, term.KeyEnd, term.KeyPgup, term.KeyPgdn,
		} {
			hx, _, _ := newHelix(t, "abc\ndef", term.Coordinates{X: 1, Y: 0})
			send(t, hx, key('i'))
			send(t, hx, namedKey(k))
			assert.True(t, hx.IsEditMode(), "%v must stay in insert mode", k)
		}
	})

	t.Run("ctrl-r inserts a register", func(t *testing.T) {
		hx, buf, clip := newHelix(t, "ab", term.Coordinates{})
		require.NoError(t, clip.Copy(registerNameToID('a'),
			clipboard.Data{Text: "REG", Metadata: text.StandardSelection}))
		send(t, hx, key('i'))
		send(t, hx, modKey(term.ModCtrl, 'r'), key('a'))
		assert.Equal(t, "REGab", buf.String())
	})

	t.Run("ctrl-r with an unknown register inserts nothing", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "ab", term.Coordinates{})
		send(t, hx, key('i'))
		send(t, hx, modKey(term.ModCtrl, 'r'), modKey(term.ModCtrl, 'g'))
		assert.Equal(t, "ab", buf.String())
		assert.True(t, hx.IsEditMode())
	})

	t.Run("ctrl-r from an empty register inserts nothing", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "ab", term.Coordinates{})
		send(t, hx, key('i'))
		send(t, hx, modKey(term.ModCtrl, 'r'), key('z'))
		assert.Equal(t, "ab", buf.String())
	})

	t.Run("ctrl-r swallows a zero key", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "ab", term.Coordinates{})
		send(t, hx, key('i'))
		send(t, hx, modKey(term.ModCtrl, 'r'), term.Event{Type: term.EventKey})
		assert.Equal(t, "ab", buf.String())
		assert.True(t, hx.IsEditMode())
	})

	t.Run("ctrl-u on a blank line kills the whole indent", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "   \nx", term.Coordinates{X: 2})
		send(t, hx, key('a'), modKey(term.ModCtrl, 'u'))
		assert.Equal(t, "\nx", buf.String())
	})

	// delete_char_backward dedents by a whole indent level when only
	// indentation precedes the caret.
	t.Run("backspace dedents", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			content string
			at      term.Coordinates
			want    string
		}{
			{name: "at an indent boundary", content: "    ab",
				at: term.Coordinates{X: 4}, want: "  ab"},
			{name: "off an indent boundary", content: "   ab",
				at: term.Coordinates{X: 3}, want: "  ab"},
			{name: "a single leading space", content: " ab",
				at: term.Coordinates{X: 1}, want: "ab"},
			{name: "after real text it deletes one cell", content: "  abc",
				at: term.Coordinates{X: 4}, want: "  ac"},
			{name: "a tab takes the plain path", content: "\t\tab",
				at: term.Coordinates{X: 2}, want: "\tab"},
		} {
			hx, buf, _ := newHelix(t, tc.content, tc.at)
			send(t, hx, key('i'), namedKey(term.KeyBackspace))
			assert.Equal(t, tc.want, buf.String(), tc.name)
		}
	})

	t.Run("ctrl-h dedents too", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "    ab", term.Coordinates{X: 4})
		send(t, hx, key('i'), modKey(term.ModCtrl, 'h'))
		assert.Equal(t, "  ab", buf.String())
	})

	t.Run("the insert register records the session", func(t *testing.T) {
		hx, _, clip := newHelix(t, "", term.Coordinates{})
		send(t, hx, keys("iabc")...)
		send(t, hx, namedKey(term.KeyEsc))
		data, err := clip.Paste(registerNameToID('.'))
		require.NoError(t, err)
		assert.Equal(t, "abc", data.Text)
	})

	// i is the one entry that leaves a non-empty range behind with its
	// head at the start, so escaping keeps the caret where it was
	// rather than dropping it onto the text just typed.
	t.Run("esc after i keeps the caret on the original cell", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "ab", term.Coordinates{})
		send(t, hx, keys("iX")...)
		send(t, hx, namedKey(term.KeyEsc))
		assert.Equal(t, "Xab", buf.String())
		assert.Equal(t, term.Coordinates{X: 1}, hx.CursorAtScroll())
		assert.Equal(t, "a", sel(t, hx), "normal mode always owns a selection")
	})

	t.Run("esc after a plain i does not move the caret", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abcdef", term.Coordinates{X: 3})
		send(t, hx, key('i'), namedKey(term.KeyEsc))
		assert.Equal(t, term.Coordinates{X: 3}, hx.CursorAtScroll())
	})

	// Every other entry starts from an empty range, which the
	// min-width-1 invariant widens backwards on the way out.
	for _, tc := range []struct {
		name string
		evs  []term.Event
		want term.Coordinates
	}{
		{name: "a", evs: keys("aX"), want: term.Coordinates{X: 1}},
		{name: "A", evs: keys("AX"), want: term.Coordinates{X: 2}},
		{name: "I", evs: keys("IX"), want: term.Coordinates{}},
		{name: "o", evs: keys("oX"), want: term.Coordinates{Y: 1}},
		{name: "O", evs: keys("OX"), want: term.Coordinates{}},
		{name: "c", evs: keys("cX"), want: term.Coordinates{}},
	} {
		t.Run("esc after "+tc.name+" lands on the last insert", func(t *testing.T) {
			hx, _, _ := newHelix(t, "ab", term.Coordinates{})
			send(t, hx, tc.evs...)
			send(t, hx, namedKey(term.KeyEsc))
			assert.Equal(t, tc.want, hx.CursorAtScroll())
			assert.Equal(t, "X", sel(t, hx))
		})
	}
}

// TestBracketedPaste pins the paste burst protocol.
func TestBracketedPaste(t *testing.T) {
	burst := func(str string) []term.Event {
		evs := []term.Event{{Type: term.EventPasteStart}}
		evs = append(evs, keys(str)...)
		return append(evs, term.Event{Type: term.EventPasteEnd})
	}

	t.Run("a burst in insert mode is inserted verbatim", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "", term.Coordinates{})
		send(t, hx, key('i'))
		send(t, hx, burst("hello")...)
		assert.Equal(t, "hello", buf.String())
	})

	t.Run("a burst in normal mode is swallowed", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "ab", term.Coordinates{})
		send(t, hx, burst("hello")...)
		assert.Equal(t, "ab", buf.String())
		assert.True(t, hx.IsNormalMode())
	})

	t.Run("an empty burst is a no-op", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "ab", term.Coordinates{})
		send(t, hx, key('i'))
		send(t, hx, term.Event{Type: term.EventPasteStart},
			term.Event{Type: term.EventPasteEnd})
		assert.Equal(t, "ab", buf.String())
	})

	t.Run("burst keys never reach the keymap", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "abc", term.Coordinates{})
		send(t, hx, term.Event{Type: term.EventPasteStart})
		send(t, hx, keys("dd")...)
		send(t, hx, term.Event{Type: term.EventPasteEnd})
		assert.Equal(t, "abc", buf.String())
	})
}

// TestUnboundShellKeys pins the shell and command-line entries, which
// belong to the IDE rather than the buffer, and the syntax-tree
// selection commands that need a tree this handler does not own.
func TestUnboundShellKeys(t *testing.T) {
	for _, ev := range []term.Event{
		key('|'), key('!'), key('$'), key(':'),
		modKey(term.ModAlt, 'I'), modKey(term.ModAlt, 'a'),
		modKey(term.ModAlt, '|'),
		modKey(term.ModAlt, '!'), modKey(term.ModCtrl, 'z'),
	} {
		hx, buf, _ := newHelix(t, "foo bar", term.Coordinates{})
		_, handled := hx.Handle(ev)
		assert.False(t, handled, "%v must stay unbound", ev)
		assert.Equal(t, "foo bar", buf.String())
		assert.True(t, hx.IsNormalMode())
	}
}

// TestUnboundSyntaxKeys covers the sibling and parent-node motions,
// which need a syntax tree this handler does not own. They stay free
// for the keymap layer instead of being approximated.
func TestUnboundSyntaxKeys(t *testing.T) {
	for _, ev := range []term.Event{
		modKey(term.ModAlt, 'n'), modKey(term.ModAlt, 'p'),
		modKey(term.ModAlt, 'b'), modKey(term.ModAlt, 'e'),
		modNamedKey(term.ModAlt, term.KeyArrowLeft),
		modNamedKey(term.ModAlt, term.KeyArrowRight),
	} {
		hx, buf, _ := newHelix(t, "f(a b)", term.Coordinates{})
		_, handled := hx.Handle(ev)
		assert.False(t, handled, "%v must stay unbound", ev)
		assert.Equal(t, "f(a b)", buf.String())
		assert.True(t, hx.IsNormalMode())
	}
}

// TestUnboundLayerKeys covers the prefixes Helix reserves for the
// window manager, the pickers and the shell, which belong to the IDE
// rather than the buffer.
func TestUnboundLayerKeys(t *testing.T) {
	for _, ev := range []term.Event{
		modKey(term.ModCtrl, 'w'), namedKey(term.KeySpace),
	} {
		hx, buf, _ := newHelix(t, "foo bar", term.Coordinates{})
		_, handled := hx.Handle(ev)
		assert.False(t, handled, "%v must stay unbound", ev)
		assert.Equal(t, "foo bar", buf.String())
		assert.True(t, hx.IsNormalMode())
	}
}

// TestUnboundBracketKeys pins the [ and ] entries that need
// diagnostics, VCS or a syntax tree. Only paragraphs and add_newline
// are served here.
func TestUnboundBracketKeys(t *testing.T) {
	for _, prefix := range []rune{'[', ']'} {
		for _, ch := range []rune{'d', 'D', 'g', 'G', 'f', 't', 'a', 'c', 'e', 'T', 'x'} {
			hx, _, _ := newHelix(t, "foo bar", term.Coordinates{})
			send(t, hx, key(prefix))
			_, handled := hx.Handle(key(ch))
			assert.False(t, handled, "%c%c must stay unbound", prefix, ch)
			assert.True(t, hx.IsNormalMode(), "%c%c still leaves bracket mode", prefix, ch)
		}
	}
}

// TestUnboundNavigationKeys pins the goto-mode entries Helix reserves
// for the language server and buffer list.
func TestUnboundNavigationKeys(t *testing.T) {
	for _, ch := range []rune{'d', 'D', 'y', 'r', 'i', 'a', 'm', 'n', 'p', 'f'} {
		hx, _, _ := newHelix(t, "foo bar", term.Coordinates{})
		send(t, hx, key('g'))
		_, handled := hx.Handle(key(ch))
		assert.False(t, handled, "g%c must stay unbound", ch)
		assert.True(t, hx.IsNormalMode(), "g%c still leaves goto mode", ch)
	}
}

// TestMacroBindings pins Q and q against a recorder/player double.
func TestMacroBindings(t *testing.T) {
	t.Run("Q toggles recording", func(t *testing.T) {
		rec := new(fakeMacroRecorder)
		hx, _, _ := newHelix(t, "abc", term.Coordinates{}, WithMacroRecorder(rec))
		send(t, hx, key('Q'))
		assert.Equal(t, 1, rec.starts)
		rec.recording = true
		send(t, hx, key('Q'))
		assert.Equal(t, 1, rec.stops)
	})

	t.Run("Q records into the selected register", func(t *testing.T) {
		rec := new(fakeMacroRecorder)
		hx, _, _ := newHelix(t, "abc", term.Coordinates{}, WithMacroRecorder(rec))
		send(t, hx, keys("\"aQ")...)
		assert.Equal(t, registerNameToID('a'), rec.lastID)
	})

	t.Run("q replays a register", func(t *testing.T) {
		player := new(fakeMacroPlayer)
		hx, _, _ := newHelix(t, "abc", term.Coordinates{}, WithMacroPlayer(player))
		send(t, hx, keys("3q")...)
		assert.Equal(t, 3, player.count)
	})

	t.Run("without a recorder Q is unhandled", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abc", term.Coordinates{})
		_, handled := hx.Handle(key('Q'))
		assert.False(t, handled)
	})
}

type fakeMacroRecorder struct {
	recording bool
	starts    int
	stops     int
	lastID    string
}

func (r *fakeMacroRecorder) IsRecording() bool { return r.recording }

func (r *fakeMacroRecorder) Start(id string) {
	r.starts++
	r.lastID = id
}

func (r *fakeMacroRecorder) Stop() { r.stops++ }

type fakeMacroPlayer struct {
	playing bool
	id      string
	count   int
}

func (p *fakeMacroPlayer) IsPlaying() bool { return p.playing }

func (p *fakeMacroPlayer) Play(id string, count int) error {
	p.id = id
	p.count = count
	return nil
}

// TestSearchMode pins / and ? plus the n/N repeats.
func TestSearchMode(t *testing.T) {
	t.Run("/ opens the search prompt", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abc", term.Coordinates{})
		send(t, hx, key('/'))
		assert.True(t, hx.IsSearchMode())
		assert.False(t, hx.IsNormalMode())
	})

	t.Run("esc closes the prompt", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abc", term.Coordinates{})
		send(t, hx, key('/'))
		send(t, hx, namedKey(term.KeyEsc))
		assert.True(t, hx.IsNormalMode())
		assert.False(t, hx.IsSearchMode())
	})

	t.Run("a search selects the match", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar foo", term.Coordinates{})
		send(t, hx, key('/'))
		send(t, hx, keys("bar")...)
		send(t, hx, namedKey(term.KeyEnter))
		assert.Equal(t, "bar", sel(t, hx))
		assert.True(t, hx.IsNormalMode())
	})

	t.Run("n walks forward through matches", func(t *testing.T) {
		hx, _, _ := newHelix(t, "ab ab ab", term.Coordinates{})
		send(t, hx, key('/'))
		send(t, hx, keys("ab")...)
		send(t, hx, namedKey(term.KeyEnter))
		first := hx.CursorAtScroll()
		send(t, hx, key('n'))
		second := hx.CursorAtScroll()
		assert.NotEqual(t, first, second)
		assert.Equal(t, "ab", sel(t, hx))
		send(t, hx, key('N'))
		assert.Equal(t, first, hx.CursorAtScroll())
	})

	t.Run("Search only arms the pattern", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar foo", term.Coordinates{})
		hx.Search("bar")
		assert.Equal(t, "f", sel(t, hx), "the caret does not move")
		send(t, hx, key('n'))
		assert.Equal(t, "bar", sel(t, hx))
	})

	t.Run("search can be disabled", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abc", term.Coordinates{}, WithSearch(false))
		_, handled := hx.Handle(key('/'))
		assert.False(t, handled)
		assert.True(t, hx.IsNormalMode())
	})

	t.Run("* searches for the selection", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar foo", term.Coordinates{})
		send(t, hx, keys("w*")...)
		assert.Contains(t, strings.TrimSpace(sel(t, hx)), "foo")
	})
}

// motionCase drives one keystroke sequence against a buffer and checks
// where the caret and the selection ended up.
type motionCase struct {
	name    string
	content string
	at      term.Coordinates
	evs     []term.Event
	wantAt  term.Coordinates
	wantSel string
	// skipSel leaves the selection unchecked for cases where only the
	// caret position is meaningful.
	skipSel bool
}

func runMotionCases(t *testing.T, cases []motionCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hx, _, _ := newHelix(t, tc.content, tc.at)
			send(t, hx, tc.evs...)
			assert.Equal(t, tc.wantAt, hx.CursorAtScroll(), "caret position")
			if !tc.skipSel {
				assert.Equal(t, tc.wantSel, sel(t, hx), "selection")
			}
		})
	}
}

// TestSelectionInvariant pins Helix's core rule: a range is never empty.
// Selection::ensure_invariants widens a collapsed range to one grapheme,
// so every operator always has something to act on.
func TestSelectionInvariant(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		at      term.Coordinates
		want    string
	}{
		{name: "start of buffer", content: "hello", want: "h"},
		{name: "mid line", content: "hello", at: term.Coordinates{X: 2}, want: "l"},
		{name: "last cell", content: "hello", at: term.Coordinates{X: 4}, want: "o"},
		{name: "second line", content: "ab\ncd", at: term.Coordinates{Y: 1}, want: "c"},
		{name: "wide glyph", content: "世界", want: "世"},
		{name: "tab cell", content: "\tx", want: "\t"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hx, _, _ := newHelix(t, tc.content, tc.at)
			assert.Equal(t, tc.want, sel(t, hx))
		})
	}
}

// TestEmptyBufferIsSafe pins that a zero-length buffer does not panic
// and leaves every command a no-op.
func TestEmptyBufferIsSafe(t *testing.T) {
	for _, ev := range []term.Event{
		key('w'), key('b'), key('e'), key('h'), key('j'), key('k'), key('l'),
		key('d'), key('y'), key('p'), key('x'), key('X'), key('%'), key('~'),
		key('J'), key(';'),
		modKey(term.ModAlt, 'x'), modKey(term.ModAlt, ';'),
		modKey(term.ModCtrl, 'a'), modKey(term.ModCtrl, 'x'),
	} {
		hx, buf, _ := newHelix(t, "", term.Coordinates{})
		require.NotPanics(t, func() { hx.Handle(ev) }, "event %v", ev)
		assert.Equal(t, "", buf.String())
	}

	// The indent operators do write, so they only get the crash check.
	for _, ev := range []term.Event{key('>'), key('<'), key('=')} {
		hx, _, _ := newHelix(t, "", term.Coordinates{})
		require.NotPanics(t, func() { hx.Handle(ev) }, "event %v", ev)
	}
}

// TestCaretMotions pins h/j/k/l and the arrow keys: put_cursor with
// Movement::Move collapses the range onto the landing cell.
func TestCaretMotions(t *testing.T) {
	runMotionCases(t, []motionCase{
		{name: "l moves right", content: "hello", evs: keys("l"),
			wantAt: term.Coordinates{X: 1}, wantSel: "e"},
		{name: "h moves left", content: "hello", at: term.Coordinates{X: 2},
			evs: keys("h"), wantAt: term.Coordinates{X: 1}, wantSel: "e"},
		{name: "j moves down", content: "ab\ncd", evs: keys("j"),
			wantAt: term.Coordinates{Y: 1}, wantSel: "c"},
		{name: "k moves up", content: "ab\ncd", at: term.Coordinates{Y: 1},
			evs: keys("k"), wantAt: term.Coordinates{}, wantSel: "a"},
		{name: "arrow right", content: "hello", evs: []term.Event{namedKey(term.KeyArrowRight)},
			wantAt: term.Coordinates{X: 1}, wantSel: "e"},
		{name: "arrow left", content: "hello", at: term.Coordinates{X: 1},
			evs: []term.Event{namedKey(term.KeyArrowLeft)}, wantAt: term.Coordinates{}, wantSel: "h"},
		{name: "arrow down", content: "ab\ncd", evs: []term.Event{namedKey(term.KeyArrowDown)},
			wantAt: term.Coordinates{Y: 1}, wantSel: "c"},
		{name: "arrow up", content: "ab\ncd", at: term.Coordinates{Y: 1},
			evs: []term.Event{namedKey(term.KeyArrowUp)}, wantAt: term.Coordinates{}, wantSel: "a"},
		{name: "h clamps at line start", content: "hello", evs: keys("hhh"),
			wantAt: term.Coordinates{}, wantSel: "h"},
		{name: "l clamps at line end", content: "ab", evs: keys("lllll"),
			wantAt: term.Coordinates{X: 1}, wantSel: "b"},
		{name: "k clamps at first line", content: "ab\ncd", evs: keys("kkk"),
			wantAt: term.Coordinates{}, wantSel: "a"},
		{name: "j clamps at last line", content: "ab\ncd", evs: keys("jjj"),
			wantAt: term.Coordinates{Y: 1}, wantSel: "c"},
		{name: "count repeats l", content: "abcdef", evs: keys("3l"),
			wantAt: term.Coordinates{X: 3}, wantSel: "d"},
		{name: "count repeats j", content: "a\nb\nc\nd", evs: keys("3j"),
			wantAt: term.Coordinates{Y: 3}, wantSel: "d"},
		{name: "multi digit count", content: strings.Repeat("x", 30), evs: keys("12l"),
			wantAt: term.Coordinates{X: 12}, wantSel: "x"},
		{name: "count larger than buffer clamps", content: "ab\ncd", evs: keys("99j"),
			wantAt: term.Coordinates{Y: 1}, wantSel: "c"},
		{name: "j onto a shorter line clamps the column", content: "abcd\nx\nabcd",
			at: term.Coordinates{X: 3}, evs: keys("j"), wantAt: term.Coordinates{Y: 1}, wantSel: "x"},
		{name: "j past a shorter line restores the column", content: "abcd\nx\nabcd",
			at: term.Coordinates{X: 3}, evs: keys("jj"),
			wantAt: term.Coordinates{X: 3, Y: 2}, wantSel: "d"},
		{name: "j across an empty line", content: "ab\n\ncd", evs: keys("j"),
			wantAt: term.Coordinates{Y: 1}, wantSel: ""},
	})
}

// TestWordMotions pins the word grammar from helix-core word_move: the
// motion selects what it travels over, w stops before the word it
// found, and repeats advance rather than standing still.
func TestWordMotions(t *testing.T) {
	runMotionCases(t, []motionCase{
		{name: "w selects up to the next word", content: "foo bar baz", evs: keys("w"),
			wantAt: term.Coordinates{X: 3}, wantSel: "foo "},
		{name: "w advances on repeat", content: "foo bar baz", evs: keys("ww"),
			wantAt: term.Coordinates{X: 7}, wantSel: "bar "},
		{name: "w from mid word", content: "foo bar", at: term.Coordinates{X: 1},
			evs: keys("w"), wantAt: term.Coordinates{X: 3}, wantSel: "oo "},
		{name: "w from the last cell of a word", content: "foo bar",
			at: term.Coordinates{X: 2}, evs: keys("w"),
			wantAt: term.Coordinates{X: 3}, wantSel: "o "},
		{name: "w stops at a punctuation boundary", content: "a.b c", evs: keys("w"),
			wantAt: term.Coordinates{X: 1}, wantSel: "."},
		{name: "W treats punctuation as word material", content: "a.b c", evs: keys("W"),
			wantAt: term.Coordinates{X: 3}, wantSel: "a.b "},
		{name: "e selects through the word end", content: "foo bar", evs: keys("e"),
			wantAt: term.Coordinates{X: 2}, wantSel: "foo"},
		{name: "e advances on repeat", content: "foo bar", evs: keys("ee"),
			wantAt: term.Coordinates{X: 6}, wantSel: " bar"},
		{name: "E spans punctuation", content: "a.b c", evs: keys("E"),
			wantAt: term.Coordinates{X: 2}, wantSel: "a.b"},
		{name: "b selects backwards", content: "foo bar", at: term.Coordinates{X: 6},
			evs: keys("b"), wantAt: term.Coordinates{X: 4}, wantSel: "bar"},
		{name: "b advances backwards on repeat", content: "foo bar",
			at: term.Coordinates{X: 6}, evs: keys("bb"),
			wantAt: term.Coordinates{}, wantSel: "foo "},
		{name: "b after w reverses over the same text", content: "foo bar",
			evs: keys("wb"), wantAt: term.Coordinates{}, wantSel: "foo "},
		{name: "B spans punctuation", content: "a.b c", at: term.Coordinates{X: 4},
			evs: keys("B"), wantAt: term.Coordinates{}, wantSel: "a.b "},
		{name: "w at the buffer end stays put", content: "ab", at: term.Coordinates{X: 1},
			evs: keys("w"), wantAt: term.Coordinates{X: 1}, wantSel: "b"},
		{name: "b at the buffer start stays put", content: "ab", evs: keys("b"),
			wantAt: term.Coordinates{}, wantSel: "a"},
		{name: "w wraps to the next line", content: "ab\ncd", at: term.Coordinates{X: 1},
			evs: keys("w"), wantAt: term.Coordinates{X: 1, Y: 1}, wantSel: "cd"},
		{name: "2w covers only the last leg", content: "foo bar baz", evs: keys("2w"),
			wantAt: term.Coordinates{X: 7}, wantSel: "bar "},
		{name: "2e covers only the last leg", content: "foo bar baz", evs: keys("2e"),
			wantAt: term.Coordinates{X: 6}, wantSel: " bar"},
		{name: "w over leading whitespace", content: "  foo", evs: keys("w"),
			wantAt: term.Coordinates{X: 1}, wantSel: "  "},
		{name: "e over wide glyphs", content: "世界 x", evs: keys("e"),
			wantAt: term.Coordinates{X: 1}, wantSel: "世界"},
		{name: "w stops before a tab run", content: "a\t b", evs: keys("w"),
			wantAt: term.Coordinates{X: 2}, wantSel: "a\t "},
		{name: "e re-anchors past a word it already ends on", content: "a\t b",
			evs: keys("e"), wantAt: term.Coordinates{X: 3}, wantSel: "\t b"},
		{name: "w skips blank lines and re-anchors on the word",
			content: "foo\n\nbar", at: term.Coordinates{X: 2}, evs: keys("w"),
			wantAt: term.Coordinates{X: 2, Y: 2}, wantSel: "bar"},
		{name: "b skips blank lines backwards", content: "foo\n\nbar",
			at: term.Coordinates{X: 2, Y: 2}, evs: keys("b"),
			wantAt: term.Coordinates{Y: 2}, wantSel: "bar"},
		{name: "e on the last cell of the buffer stays put", content: "foo bar",
			at: term.Coordinates{X: 6}, evs: keys("e"),
			wantAt: term.Coordinates{X: 6}, wantSel: "r"},
		{name: "b from a word start crosses to the previous word",
			content: "foo bar", at: term.Coordinates{X: 4}, evs: keys("b"),
			wantAt: term.Coordinates{}, wantSel: "foo "},
		{name: "w runs into trailing whitespace", content: "foo   ",
			evs: keys("w"), wantAt: term.Coordinates{X: 5}, wantSel: "foo   "},
		{name: "w stops on a punctuation run", content: "a,,,b", evs: keys("w"),
			wantAt: term.Coordinates{X: 3}, wantSel: ",,,"},
		{name: "e ends on a punctuation run", content: "a,,,b", evs: keys("e"),
			wantAt: term.Coordinates{X: 3}, wantSel: ",,,"},
		{name: "b stops on a punctuation run", content: "a,,,b",
			at: term.Coordinates{X: 4}, evs: keys("b"),
			wantAt: term.Coordinates{X: 1}, wantSel: ",,,"},
		{name: "B swallows a punctuation run", content: "a,,,b",
			at: term.Coordinates{X: 4}, evs: keys("B"),
			wantAt: term.Coordinates{}, wantSel: "a,,,b"},
		{name: "3w covers only the last leg", content: "foo bar baz",
			evs: keys("3w"), wantAt: term.Coordinates{X: 10}, wantSel: "baz"},
		{name: "an overshooting count clamps at the buffer start",
			content: "foo bar baz", at: term.Coordinates{X: 10}, evs: keys("5b"),
			wantAt: term.Coordinates{}, wantSel: "foo "},
		{name: "w over wide glyphs", content: "x 世界 y", evs: keys("ww"),
			wantAt: term.Coordinates{X: 4}, wantSel: "世界 "},
		{name: "b over wide glyphs", content: "x 世界 y",
			at: term.Coordinates{X: 5}, evs: keys("b"),
			wantAt: term.Coordinates{X: 2}, wantSel: "世界 "},
		{name: "w on a one cell buffer stays put", content: "a", evs: keys("w"),
			wantAt: term.Coordinates{}, wantSel: "a"},
		{name: "w stops at the line ending of a blank line", content: "  \n  x",
			evs: keys("w"), wantAt: term.Coordinates{X: 1}, wantSel: "  "},
		{name: "e crosses a line ending", content: "foo\nbar",
			at: term.Coordinates{X: 2}, evs: keys("e"),
			wantAt: term.Coordinates{X: 2, Y: 1}, wantSel: "bar"},
		{name: "b crosses a line ending", content: "foo\nbar",
			at: term.Coordinates{Y: 1}, evs: keys("b"),
			wantAt: term.Coordinates{}, wantSel: "foo"},
		// A blank line has no cells, so the caret sits on the line
		// ending itself. word_move still has to walk off it.
		{name: "e from a blank line reaches the next word end",
			content: "## Examples\n\n        <.flash />",
			at:      term.Coordinates{Y: 1}, evs: keys("e"),
			wantAt: term.Coordinates{X: 9, Y: 2}, wantSel: "        <."},
		{name: "w from a blank line reaches the next word start",
			content: "## Examples\n\n        <.flash />",
			at:      term.Coordinates{Y: 1}, evs: keys("w"),
			wantAt: term.Coordinates{X: 7, Y: 2}, wantSel: "        "},
		{name: "e from a blank line between words", content: "a\n\nbb cc",
			at: term.Coordinates{Y: 1}, evs: keys("e"),
			wantAt: term.Coordinates{X: 1, Y: 2}, wantSel: "bb"},
		{name: "b from a blank line reaches the previous word start",
			content: "aa bb\n\ncc", at: term.Coordinates{Y: 1}, evs: keys("b"),
			wantAt: term.Coordinates{X: 3, Y: 0}, wantSel: "bb"},
	})
}

// TestFindCharMotions pins f/t/F/T. Helix offsets the search start for
// till motions so a repeat makes progress.
func TestFindCharMotions(t *testing.T) {
	runMotionCases(t, []motionCase{
		{name: "f selects through the match", content: "foo,bar",
			evs: append(keys("f"), key(',')), wantAt: term.Coordinates{X: 3}, wantSel: "foo,"},
		{name: "t stops before the match", content: "foo,bar",
			evs: append(keys("t"), key(',')), wantAt: term.Coordinates{X: 2}, wantSel: "foo"},
		{name: "F selects backwards through the match", content: "foo,bar",
			at: term.Coordinates{X: 6}, evs: append(keys("F"), key(',')),
			wantAt: term.Coordinates{X: 3}, wantSel: ",bar"},
		{name: "T stops after the match", content: "foo,bar",
			at: term.Coordinates{X: 6}, evs: append(keys("T"), key(',')),
			wantAt: term.Coordinates{X: 4}, wantSel: "bar"},
		{name: "t skips the adjacent match", content: "a,b,c",
			evs: append(keys("t"), key(',')), wantAt: term.Coordinates{X: 2}, wantSel: "a,b"},
		{name: "t with no further match keeps the range", content: "a,b,c",
			evs:    append(append(keys("t"), key(',')), append(keys("t"), key(','))...),
			wantAt: term.Coordinates{X: 2}, wantSel: "a,b"},
		{name: "f with a count finds the nth match", content: "a,b,c,d",
			evs: append(keys("2f"), key(',')), wantAt: term.Coordinates{X: 3}, wantSel: "a,b,"},
		{name: "f with no match leaves the selection alone", content: "abc",
			evs: append(keys("f"), key('z')), wantAt: term.Coordinates{}, wantSel: "a"},
		{name: "f on <space> uses the space key", content: "ab cd",
			evs:    []term.Event{key('f'), namedKey(term.KeySpace)},
			wantAt: term.Coordinates{X: 2}, wantSel: "ab "},
		{name: "f on <tab> uses a literal tab", content: "ab\tcd",
			evs:    []term.Event{key('f'), namedKey(term.KeyTab)},
			wantAt: term.Coordinates{X: 2}, wantSel: "ab\t"},
		{name: "esc cancels a pending find", content: "foo,bar",
			evs:    []term.Event{key('f'), namedKey(term.KeyEsc)},
			wantAt: term.Coordinates{}, wantSel: "f"},
	})
}

// TestRepeatLastMotion pins A-. replaying the last f/t/F/T.
func TestRepeatLastMotion(t *testing.T) {
	hx, _, _ := newHelix(t, "a,b,c,d", term.Coordinates{})
	send(t, hx, key('f'), key(','))
	require.Equal(t, term.Coordinates{X: 1}, hx.CursorAtScroll())

	send(t, hx, modKey(term.ModAlt, '.'))
	assert.Equal(t, term.Coordinates{X: 3}, hx.CursorAtScroll())
	assert.Equal(t, ",b,", sel(t, hx))

	t.Run("without a prior motion it is unhandled", func(t *testing.T) {
		fresh, _, _ := newHelix(t, "abc", term.Coordinates{})
		_, handled := fresh.Handle(modKey(term.ModAlt, '.'))
		assert.False(t, handled)
	})
}

// TestSelectModeExtends pins v as Helix's sticky select mode: every
// motion keeps the anchor until the mode is left.
func TestSelectModeExtends(t *testing.T) {
	t.Run("word motions accumulate", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar baz", term.Coordinates{})
		send(t, hx, key('v'))
		assert.True(t, hx.IsSelectMode())
		send(t, hx, key('w'))
		assert.Equal(t, "foo ", sel(t, hx))
		send(t, hx, key('w'))
		assert.Equal(t, "foo bar ", sel(t, hx))
		send(t, hx, key('e'))
		assert.Equal(t, "foo bar baz", sel(t, hx))
	})

	t.Run("caret motions accumulate", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abcdef", term.Coordinates{})
		send(t, hx, key('v'))
		send(t, hx, keys("lll")...)
		assert.Equal(t, "abcd", sel(t, hx))
	})

	t.Run("counted motions keep one anchor", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar baz", term.Coordinates{})
		send(t, hx, key('v'))
		send(t, hx, keys("2w")...)
		assert.Equal(t, "foo bar ", sel(t, hx))
	})

	t.Run("vertical motions accumulate", func(t *testing.T) {
		hx, _, _ := newHelix(t, "ab\ncd\nef", term.Coordinates{})
		send(t, hx, key('v'))
		send(t, hx, keys("jj")...)
		assert.Equal(t, "ab\ncd\ne", sel(t, hx))
	})

	t.Run("v toggles back to normal mode", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abcdef", term.Coordinates{})
		send(t, hx, keys("vll")...)
		require.Equal(t, "abc", sel(t, hx))
		send(t, hx, key('v'))
		assert.False(t, hx.IsSelectMode())
		send(t, hx, key('l'))
		assert.Equal(t, "d", sel(t, hx), "normal mode collapses again")
	})

	t.Run("esc leaves select mode but keeps the selection", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abcdef", term.Coordinates{})
		send(t, hx, keys("vll")...)
		send(t, hx, namedKey(term.KeyEsc))
		assert.False(t, hx.IsSelectMode())
		assert.Equal(t, "abc", sel(t, hx))
	})

	t.Run("backwards extension keeps the anchor cell covered", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abcdef", term.Coordinates{X: 3})
		send(t, hx, key('v'))
		send(t, hx, keys("hh")...)
		assert.Equal(t, "bcd", sel(t, hx))
	})

	t.Run("find char extends", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo,bar", term.Coordinates{})
		send(t, hx, key('v'), key('l'))
		require.Equal(t, "fo", sel(t, hx))
		send(t, hx, key('f'), key('r'))
		assert.Equal(t, "foo,bar", sel(t, hx))
	})

	// put_cursor shifts the anchor by one grapheme when the head
	// crosses it, so the cell the selection started on stays covered.
	t.Run("a word motion flips the selection backwards", func(t *testing.T) {
		hx, _, _ := newHelix(t, "one two three", term.Coordinates{X: 8})
		send(t, hx, key('v'), key('b'))
		assert.Equal(t, "two t", sel(t, hx))
		assert.Equal(t, term.Coordinates{X: 4}, hx.CursorAtScroll())
	})

	t.Run("a backward selection flips forward again", func(t *testing.T) {
		hx, _, _ := newHelix(t, "one two three", term.Coordinates{X: 8})
		send(t, hx, key('v'), key('b'))
		require.Equal(t, "two t", sel(t, hx))
		send(t, hx, key('e'))
		assert.Equal(t, "o t", sel(t, hx), "the head walks back over the anchor")
		send(t, hx, key('e'))
		assert.Equal(t, "three", sel(t, hx))
	})

	t.Run("a collapsing motion keeps a single cell", func(t *testing.T) {
		hx, _, _ := newHelix(t, "one two three", term.Coordinates{X: 8})
		send(t, hx, key('v'), key('l'))
		require.Equal(t, "th", sel(t, hx))
		send(t, hx, key('b'))
		assert.Equal(t, "t", sel(t, hx))
	})

	t.Run("A-; swaps the ends without changing the span", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar", term.Coordinates{})
		send(t, hx, key('v'), key('w'))
		require.Equal(t, "foo ", sel(t, hx))
		send(t, hx, modKey(term.ModAlt, ';'))
		assert.Equal(t, "foo ", sel(t, hx))
		assert.Equal(t, term.Coordinates{}, hx.CursorAtScroll())
	})

	t.Run("A-: forces the head to trail the anchor", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar", term.Coordinates{X: 4})
		send(t, hx, key('v'), key('b'))
		require.Equal(t, "foo b", sel(t, hx))
		send(t, hx, modKey(term.ModAlt, ':'))
		assert.Equal(t, "foo b", sel(t, hx))
		assert.Equal(t, term.Coordinates{X: 4}, hx.CursorAtScroll())
	})

	t.Run("A-: on a forward selection is unhandled", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar", term.Coordinates{})
		send(t, hx, key('v'), key('w'))
		_, handled := hx.Handle(modKey(term.ModAlt, ':'))
		assert.False(t, handled)
	})

	t.Run("an explicit selection is rebound before extending", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar", term.Coordinates{})
		send(t, hx, keys("miw")...)
		require.Equal(t, "foo", sel(t, hx))
		send(t, hx, key('v'))
		send(t, hx, key('l'))
		assert.Equal(t, "foo ", sel(t, hx))
	})
}

// TestCollapseAndFlip pins ; A-; and A-:.
func TestCollapseAndFlip(t *testing.T) {
	t.Run("; collapses onto the caret", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar", term.Coordinates{})
		send(t, hx, keys("w;")...)
		assert.Equal(t, " ", sel(t, hx))
		assert.Equal(t, term.Coordinates{X: 3}, hx.CursorAtScroll())
	})

	t.Run("A-; flips the ends", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar", term.Coordinates{})
		send(t, hx, key('w'))
		require.Equal(t, term.Coordinates{X: 3}, hx.CursorAtScroll())
		send(t, hx, modKey(term.ModAlt, ';'))
		assert.Equal(t, term.Coordinates{}, hx.CursorAtScroll())
		assert.Equal(t, "foo ", sel(t, hx), "flipping must not change the covered text")
	})

	t.Run("A-: normalises a backwards selection", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar", term.Coordinates{X: 6})
		send(t, hx, key('b'))
		require.Equal(t, term.Coordinates{X: 4}, hx.CursorAtScroll())
		send(t, hx, modKey(term.ModAlt, ':'))
		assert.Equal(t, term.Coordinates{X: 6}, hx.CursorAtScroll())
		assert.Equal(t, "bar", sel(t, hx))
	})

	t.Run("A-: on a forward selection is a no-op", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar", term.Coordinates{})
		send(t, hx, key('w'))
		_, handled := hx.Handle(modKey(term.ModAlt, ':'))
		assert.False(t, handled)
		assert.Equal(t, term.Coordinates{X: 3}, hx.CursorAtScroll())
	})
}

// TestLineSelection pins x, X and A-x.
func TestLineSelection(t *testing.T) {
	t.Run("x selects the line", func(t *testing.T) {
		hx, _, _ := newHelix(t, "one\ntwo\nthree", term.Coordinates{})
		send(t, hx, key('x'))
		assert.Equal(t, "one\n", sel(t, hx))
	})

	t.Run("x extends downwards on repeat", func(t *testing.T) {
		hx, _, _ := newHelix(t, "one\ntwo\nthree", term.Coordinates{})
		send(t, hx, keys("xx")...)
		assert.Equal(t, "one\ntwo\n", sel(t, hx))
	})

	t.Run("counted x selects that many lines", func(t *testing.T) {
		hx, _, _ := newHelix(t, "a\nb\nc\nd", term.Coordinates{})
		send(t, hx, keys("3x")...)
		assert.Equal(t, "a\nb\nc\n", sel(t, hx))
	})

	t.Run("x at the last line stays put", func(t *testing.T) {
		hx, _, _ := newHelix(t, "one\ntwo", term.Coordinates{Y: 1})
		send(t, hx, key('x'))
		first := sel(t, hx)
		send(t, hx, key('x'))
		assert.Equal(t, first, sel(t, hx), "no line below to extend onto")
	})

	t.Run("X snaps a partial selection out to whole lines", func(t *testing.T) {
		hx, _, _ := newHelix(t, "one two\nthree", term.Coordinates{})
		send(t, hx, key('w'))
		require.Equal(t, "one ", sel(t, hx))
		send(t, hx, key('X'))
		assert.Equal(t, "one two\n", sel(t, hx))
	})

	t.Run("A-x leaves a single-line selection alone", func(t *testing.T) {
		hx, _, _ := newHelix(t, "one two\nthree", term.Coordinates{})
		send(t, hx, key('w'))
		_, handled := hx.Handle(modKey(term.ModAlt, 'x'))
		assert.False(t, handled, "shrink_to_line_bounds is a no-op within one line")
		assert.Equal(t, "one ", sel(t, hx))
	})

	t.Run("A-x drops partially covered lines", func(t *testing.T) {
		hx, _, _ := newHelix(t, "one\ntwo\nthree", term.Coordinates{X: 1})
		send(t, hx, key('v'))
		send(t, hx, keys("jj")...)
		require.Equal(t, "ne\ntwo\nth", sel(t, hx))
		send(t, hx, modKey(term.ModAlt, 'x'))
		assert.Equal(t, "two\n", sel(t, hx))
	})
}

// TestSelectAll pins %.
func TestSelectAll(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		want    string
	}{
		{name: "multi line", content: "one\ntwo", want: "one\ntwo"},
		{name: "single line", content: "only", want: "only"},
		{name: "trailing newline", content: "one\n", want: "one\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hx, _, _ := newHelix(t, tc.content, term.Coordinates{})
			send(t, hx, key('%'))
			assert.Equal(t, tc.want, sel(t, hx))
		})
	}
}

// TestGotoMode pins the g minor mode against Helix's goto commands.
func TestGotoMode(t *testing.T) {
	runMotionCases(t, []motionCase{
		{name: "gg goes to the first line", content: "a\nb\nc", at: term.Coordinates{Y: 2},
			evs: keys("gg"), wantAt: term.Coordinates{}, wantSel: "a"},
		{name: "ge goes to the last line", content: "a\nb\nc", evs: keys("ge"),
			wantAt: term.Coordinates{Y: 2}, wantSel: "c"},
		{name: "ge skips a blank last line", content: "a\nb\n", evs: keys("ge"),
			wantAt: term.Coordinates{Y: 1}, wantSel: "b"},
		{name: "gh goes to the line start", content: "hello", at: term.Coordinates{X: 3},
			evs: keys("gh"), wantAt: term.Coordinates{}, wantSel: "h"},
		{name: "gl goes to the last cell", content: "hello", evs: keys("gl"),
			wantAt: term.Coordinates{X: 4}, wantSel: "o"},
		{name: "gl on an empty line stays at column zero", content: "\nx",
			evs: keys("gl"), wantAt: term.Coordinates{}, wantSel: ""},
		{name: "gs goes to the first non blank", content: "   hi", evs: keys("gs"),
			wantAt: term.Coordinates{X: 3}, wantSel: "h"},
		{name: "counted gg goes to that line", content: "a\nb\nc\nd", evs: keys("3gg"),
			wantAt: term.Coordinates{Y: 2}, wantSel: "c"},
		{name: "counted gg clamps", content: "a\nb", evs: keys("99gg"),
			wantAt: term.Coordinates{Y: 1}, wantSel: "b"},
		{name: "g| goes to the count-th column", content: "abcdef", evs: keys("4g|"),
			wantAt: term.Coordinates{X: 3}, wantSel: "d"},
		{name: "g| clamps to the line end", content: "ab", evs: keys("9g|"),
			wantAt: term.Coordinates{X: 1}, wantSel: "b"},
		{name: "gj moves one line down", content: "ab\ncd", evs: keys("gj"),
			wantAt: term.Coordinates{Y: 1}, wantSel: "c"},
		{name: "gk moves one line up", content: "ab\ncd", at: term.Coordinates{Y: 1},
			evs: keys("gk"), wantAt: term.Coordinates{}, wantSel: "a"},
	})

	t.Run("goto mode is one shot", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abc\ndef", term.Coordinates{})
		send(t, hx, keys("gh")...)
		assert.True(t, hx.IsNormalMode())
	})

	t.Run("esc cancels goto mode", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abc", term.Coordinates{X: 2})
		send(t, hx, key('g'), namedKey(term.KeyEsc))
		assert.True(t, hx.IsNormalMode())
		assert.Equal(t, term.Coordinates{X: 2}, hx.CursorAtScroll())
	})

	t.Run("unbound goto keys stay unhandled", func(t *testing.T) {
		for _, ch := range []rune{'d', 'y', 'r', 'i', 'f', 'a', 'm', 'n', 'p', 'w'} {
			hx, _, _ := newHelix(t, "abc", term.Coordinates{})
			send(t, hx, key('g'))
			_, handled := hx.Handle(key(ch))
			assert.False(t, handled, "g%c belongs to the command layer", ch)
			assert.True(t, hx.IsNormalMode())
		}
	})

	t.Run("goto extends in select mode", func(t *testing.T) {
		hx, _, _ := newHelix(t, "hello", term.Coordinates{})
		send(t, hx, key('v'))
		send(t, hx, keys("gl")...)
		assert.Equal(t, "hello", sel(t, hx))
	})
}

// TestGotoLine pins G, which Helix makes a no-op without a count.
func TestGotoLine(t *testing.T) {
	t.Run("bare G does nothing", func(t *testing.T) {
		hx, _, _ := newHelix(t, "a\nb\nc", term.Coordinates{})
		_, handled := hx.Handle(key('G'))
		assert.False(t, handled, "goto_line only acts on a count")
		assert.Equal(t, term.Coordinates{}, hx.CursorAtScroll())
	})

	t.Run("counted G jumps to the line", func(t *testing.T) {
		hx, _, _ := newHelix(t, "a\nb\nc", term.Coordinates{})
		send(t, hx, keys("2G")...)
		assert.Equal(t, term.Coordinates{Y: 1}, hx.CursorAtScroll())
		assert.Equal(t, "b", sel(t, hx))
	})

	t.Run("counted G skips a blank last line", func(t *testing.T) {
		hx, _, _ := newHelix(t, "a\nb\n", term.Coordinates{})
		send(t, hx, keys("9G")...)
		assert.Equal(t, term.Coordinates{Y: 1}, hx.CursorAtScroll())
	})
}

// TestHomeEndPageKeys pins the named keys Helix binds in normal mode.
func TestHomeEndPageKeys(t *testing.T) {
	runMotionCases(t, []motionCase{
		{name: "home goes to the line start", content: "hello", at: term.Coordinates{X: 3},
			evs: []term.Event{namedKey(term.KeyHome)}, wantAt: term.Coordinates{}, wantSel: "h"},
		{name: "end goes to the last cell", content: "hello",
			evs: []term.Event{namedKey(term.KeyEnd)}, wantAt: term.Coordinates{X: 4}, wantSel: "o"},
	})

	t.Run("pagedown moves down a screen", func(t *testing.T) {
		hx, _, _ := newHelix(t, strings.Repeat("line\n", 200), term.Coordinates{})
		_, handled := hx.Handle(namedKey(term.KeyPgdn))
		require.True(t, handled)
		assert.Greater(t, hx.CursorAtScroll().Y, 0)
	})

	t.Run("pageup moves up a screen", func(t *testing.T) {
		hx, _, _ := newHelix(t, strings.Repeat("line\n", 200), term.Coordinates{Y: 100})
		_, handled := hx.Handle(namedKey(term.KeyPgup))
		require.True(t, handled)
		assert.Less(t, hx.CursorAtScroll().Y, 100)
	})
}

// TestScrollCommands pins ctrl-b/f/u/d/e/y.
func TestScrollCommands(t *testing.T) {
	content := strings.Repeat("line\n", 200)
	for _, tc := range []struct {
		name string
		ch   rune
		at   term.Coordinates
		down bool
	}{
		{name: "ctrl-f pages down", ch: 'f', down: true},
		{name: "ctrl-d half-pages down", ch: 'd', down: true},
		{name: "ctrl-b pages up", ch: 'b', at: term.Coordinates{Y: 100}},
		{name: "ctrl-u half-pages up", ch: 'u', at: term.Coordinates{Y: 100}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hx, _, _ := newHelix(t, content, tc.at)
			before := hx.CursorAtScroll().Y
			_, handled := hx.Handle(modKey(term.ModCtrl, tc.ch))
			require.True(t, handled)
			if tc.down {
				assert.Greater(t, hx.CursorAtScroll().Y, before)
			} else {
				assert.Less(t, hx.CursorAtScroll().Y, before)
			}
		})
	}

	// C-e/C-y move the viewport; the caret only follows when it would
	// otherwise leave the window, and then by at most one line.
	for _, ch := range []rune{'e', 'y'} {
		t.Run("ctrl-"+string(ch)+" scrolls the viewport", func(t *testing.T) {
			hx, _, _ := newHelix(t, content, term.Coordinates{Y: 50})
			before := hx.CursorAtScroll()
			offset := hx.SeekOffset()
			_, handled := hx.Handle(modKey(term.ModCtrl, ch))
			require.True(t, handled)
			assert.NotEqual(t, offset, hx.SeekOffset(), "viewport must move")
			assert.LessOrEqual(t, abs(hx.CursorAtScroll().Y-before.Y), 1,
				"caret follows the viewport by at most one line")
		})
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// TestViewMode pins z and the sticky Z variant.
func TestViewMode(t *testing.T) {
	content := strings.Repeat("line\n", 200)

	t.Run("zz centers without moving", func(t *testing.T) {
		hx, _, _ := newHelix(t, content, term.Coordinates{Y: 100})
		before := hx.CursorAtScroll()
		send(t, hx, keys("zz")...)
		assert.Equal(t, before, hx.CursorAtScroll())
		assert.True(t, hx.IsNormalMode(), "z is one shot")
	})

	for _, ch := range []rune{'z', 'c', 't', 'b', 'm'} {
		t.Run("z"+string(ch)+" realigns the view", func(t *testing.T) {
			hx, _, _ := newHelix(t, content, term.Coordinates{Y: 100})
			before := hx.CursorAtScroll()
			send(t, hx, key('z'))
			send(t, hx, key(ch))
			assert.Equal(t, before, hx.CursorAtScroll())
			assert.True(t, hx.IsNormalMode(), "z is one shot")
		})
	}

	t.Run("zj and zk scroll", func(t *testing.T) {
		for _, ch := range []rune{'j', 'k'} {
			hx, _, _ := newHelix(t, content, term.Coordinates{Y: 100})
			send(t, hx, key('z'))
			_, handled := hx.Handle(key(ch))
			assert.True(t, handled, "z%c must scroll", ch)
		}
	})

	t.Run("z page keys", func(t *testing.T) {
		for _, ev := range []term.Event{
			modKey(term.ModCtrl, 'f'), modKey(term.ModCtrl, 'b'),
			modKey(term.ModCtrl, 'd'), modKey(term.ModCtrl, 'u'),
			namedKey(term.KeyPgdn), namedKey(term.KeyPgup),
			namedKey(term.KeySpace), namedKey(term.KeyBackspace),
		} {
			hx, _, _ := newHelix(t, content, term.Coordinates{Y: 100})
			send(t, hx, key('z'))
			_, handled := hx.Handle(ev)
			assert.True(t, handled, "z + %v must page", ev)
		}
	})

	t.Run("Z stays in view mode until esc", func(t *testing.T) {
		hx, _, _ := newHelix(t, content, term.Coordinates{Y: 100})
		send(t, hx, key('Z'))
		assert.False(t, hx.IsNormalMode())
		send(t, hx, key('t'))
		assert.False(t, hx.IsNormalMode(), "Z is sticky")
		send(t, hx, key('b'))
		assert.False(t, hx.IsNormalMode())
		send(t, hx, namedKey(term.KeyEsc))
		assert.True(t, hx.IsNormalMode())
	})

	t.Run("zn and zN repeat the search", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo\nbar\nfoo", term.Coordinates{})
		send(t, hx, key('/'))
		send(t, hx, keys("foo")...)
		send(t, hx, namedKey(term.KeyEnter))
		require.Equal(t, 2, hx.CursorAtScroll().Y)

		send(t, hx, keys("zn")...)
		assert.Equal(t, 0, hx.CursorAtScroll().Y)
		send(t, hx, keys("zN")...)
		assert.Equal(t, 2, hx.CursorAtScroll().Y)
	})

	t.Run("z/ opens the search prompt and leaves view mode", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar", term.Coordinates{})
		send(t, hx, keys("z/")...)
		assert.True(t, hx.IsSearchMode())
	})

	t.Run("z? opens the reverse search prompt", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar", term.Coordinates{})
		send(t, hx, keys("z?")...)
		assert.True(t, hx.IsSearchMode())
	})

	t.Run("an unbound view key leaves view mode", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar", term.Coordinates{})
		send(t, hx, key('z'))
		_, handled := hx.Handle(modKey(term.ModCtrl, 'g'))
		assert.False(t, handled)
		assert.True(t, hx.IsNormalMode())
	})
}

// TestJumplist pins ctrl-s / ctrl-o / ctrl-i.
func TestJumplist(t *testing.T) {
	t.Run("ctrl-o returns to a saved selection", func(t *testing.T) {
		hx, _, _ := newHelix(t, "a\nb\nc\nd\ne", term.Coordinates{})
		send(t, hx, modKey(term.ModCtrl, 's'))
		send(t, hx, keys("3j")...)
		send(t, hx, modKey(term.ModCtrl, 's'))
		require.Equal(t, term.Coordinates{Y: 3}, hx.CursorAtScroll())

		send(t, hx, modKey(term.ModCtrl, 'o'))
		assert.Equal(t, term.Coordinates{}, hx.CursorAtScroll())
	})

	t.Run("ctrl-i walks forward again", func(t *testing.T) {
		hx, _, _ := newHelix(t, "a\nb\nc\nd\ne", term.Coordinates{})
		send(t, hx, modKey(term.ModCtrl, 's'))
		send(t, hx, keys("3j")...)
		send(t, hx, modKey(term.ModCtrl, 's'))
		send(t, hx, modKey(term.ModCtrl, 'o'))
		send(t, hx, modKey(term.ModCtrl, 'i'))
		assert.Equal(t, term.Coordinates{Y: 3}, hx.CursorAtScroll())
	})

	t.Run("tab is an alias for ctrl-i", func(t *testing.T) {
		hx, _, _ := newHelix(t, "a\nb\nc", term.Coordinates{})
		send(t, hx, modKey(term.ModCtrl, 's'))
		send(t, hx, keys("2j")...)
		send(t, hx, modKey(term.ModCtrl, 's'))
		send(t, hx, modKey(term.ModCtrl, 'o'))
		send(t, hx, namedKey(term.KeyTab))
		assert.Equal(t, term.Coordinates{Y: 2}, hx.CursorAtScroll())
	})

	t.Run("an empty jumplist is unhandled", func(t *testing.T) {
		hx, _, _ := newHelix(t, "a\nb", term.Coordinates{})
		_, handled := hx.Handle(modKey(term.ModCtrl, 'o'))
		assert.False(t, handled)
		_, handled = hx.Handle(modKey(term.ModCtrl, 'i'))
		assert.False(t, handled)
	})

	t.Run("gg and ge push a jump", func(t *testing.T) {
		hx, _, _ := newHelix(t, "a\nb\nc\nd", term.Coordinates{Y: 2})
		send(t, hx, keys("gg")...)
		require.Equal(t, term.Coordinates{}, hx.CursorAtScroll())
		send(t, hx, modKey(term.ModCtrl, 'o'))
		assert.Equal(t, term.Coordinates{Y: 2}, hx.CursorAtScroll())
	})

	// push_jump also fires for the explicit jumps G, g|, g. and search.
	for _, tc := range []struct {
		name string
		evs  []term.Event
		want term.Coordinates
	}{
		{name: "counted G", evs: keys("4G"), want: term.Coordinates{Y: 3}},
		{name: "counted gg", evs: keys("4gg"), want: term.Coordinates{Y: 3}},
		{name: "g|", evs: keys("3g|"), want: term.Coordinates{X: 2, Y: 2}},
	} {
		t.Run(tc.name+" pushes a jump", func(t *testing.T) {
			hx, _, _ := newHelix(t, "aaa\nbbb\nccc\nddd", term.Coordinates{Y: 2})
			send(t, hx, tc.evs...)
			require.Equal(t, tc.want, hx.CursorAtScroll())
			send(t, hx, modKey(term.ModCtrl, 'o'))
			assert.Equal(t, term.Coordinates{Y: 2}, hx.CursorAtScroll())
		})
	}

	t.Run("a search pushes a jump", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo\nbar\nbaz", term.Coordinates{})
		send(t, hx, key('/'))
		send(t, hx, keys("baz")...)
		send(t, hx, namedKey(term.KeyEnter))
		require.Equal(t, 2, hx.CursorAtScroll().Y)
		send(t, hx, modKey(term.ModCtrl, 'o'))
		assert.Equal(t, term.Coordinates{}, hx.CursorAtScroll())
	})

	t.Run("n pushes a jump", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo\nbar\nfoo", term.Coordinates{})
		hx.Search("foo")
		send(t, hx, key('n'))
		require.Equal(t, 2, hx.CursorAtScroll().Y)
		send(t, hx, modKey(term.ModCtrl, 'o'))
		assert.Equal(t, term.Coordinates{}, hx.CursorAtScroll())
	})

	t.Run("g. pushes a jump", func(t *testing.T) {
		hx, _, _ := newHelix(t, "a\nb\nc\nd", term.Coordinates{Y: 3})
		send(t, hx, key('d'))
		send(t, hx, keys("gg")...)
		require.Equal(t, term.Coordinates{}, hx.CursorAtScroll())
		send(t, hx, keys("g.")...)
		require.Equal(t, 3, hx.CursorAtScroll().Y)
		send(t, hx, modKey(term.ModCtrl, 'o'))
		assert.Equal(t, term.Coordinates{}, hx.CursorAtScroll())
	})
}

// TestBracketMode pins the [ and ] minor modes.
func TestBracketMode(t *testing.T) {
	t.Run("]p moves to the next paragraph", func(t *testing.T) {
		hx, _, _ := newHelix(t, "a\n\nb\n\nc", term.Coordinates{})
		send(t, hx, keys("]p")...)
		assert.Greater(t, hx.CursorAtScroll().Y, 0)
		assert.True(t, hx.IsNormalMode())
	})

	t.Run("[p moves to the previous paragraph", func(t *testing.T) {
		hx, _, _ := newHelix(t, "a\n\nb\n\nc", term.Coordinates{Y: 4})
		send(t, hx, keys("[p")...)
		assert.Less(t, hx.CursorAtScroll().Y, 4)
	})

	t.Run("]<space> adds a line below", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "a\nb", term.Coordinates{})
		send(t, hx, key(']'), namedKey(term.KeySpace))
		assert.Equal(t, "a\n\nb", buf.String())
		assert.Equal(t, term.Coordinates{}, hx.CursorAtScroll())
		assert.True(t, hx.IsNormalMode())
	})

	t.Run("[<space> adds a line above", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "a\nb", term.Coordinates{})
		send(t, hx, key('['), namedKey(term.KeySpace))
		assert.Equal(t, "\na\nb", buf.String())
		assert.Equal(t, term.Coordinates{Y: 1}, hx.CursorAtScroll())
	})

	t.Run("unbound bracket keys stay unhandled", func(t *testing.T) {
		for _, ch := range []rune{'d', 'g', 'f', 't', 'a', 'c', 'e', 'x'} {
			hx, _, _ := newHelix(t, "abc", term.Coordinates{})
			send(t, hx, key(']'))
			_, handled := hx.Handle(key(ch))
			assert.False(t, handled, "]%c belongs to the command layer", ch)
			assert.True(t, hx.IsNormalMode())
		}
	})
}

// TestMatchMode pins mm and the mi/ma text objects.
func TestMatchMode(t *testing.T) {
	t.Run("mm jumps to the matching bracket", func(t *testing.T) {
		hx, _, _ := newHelix(t, "(abc)", term.Coordinates{})
		send(t, hx, keys("mm")...)
		assert.Equal(t, term.Coordinates{X: 4}, hx.CursorAtScroll())
		assert.Equal(t, ")", sel(t, hx))
	})

	t.Run("mm jumps back", func(t *testing.T) {
		hx, _, _ := newHelix(t, "(abc)", term.Coordinates{X: 4})
		send(t, hx, keys("mm")...)
		assert.Equal(t, term.Coordinates{}, hx.CursorAtScroll())
	})

	for _, tc := range []struct {
		name    string
		content string
		at      term.Coordinates
		obj     rune
		around  bool
		want    string
	}{
		{name: "inner paren", content: "f(abc)", at: term.Coordinates{X: 3}, obj: '(', want: "abc"},
		{name: "around paren", content: "f(abc)", at: term.Coordinates{X: 3}, obj: '(', around: true, want: "(abc)"},
		{name: "inner brace", content: "x{ab}", at: term.Coordinates{X: 2}, obj: '{', want: "ab"},
		{name: "inner bracket", content: "x[ab]", at: term.Coordinates{X: 2}, obj: '[', want: "ab"},
		{name: "inner quote", content: `x = "abc"`, at: term.Coordinates{X: 6}, obj: '"', want: "abc"},
		{name: "around quote", content: `x = "abc"`, at: term.Coordinates{X: 6}, obj: '"', around: true, want: `"abc"`},
		{name: "inner word", content: "foo bar", at: term.Coordinates{X: 5}, obj: 'w', want: "bar"},
		{name: "b alias for paren", content: "f(abc)", at: term.Coordinates{X: 3}, obj: 'b', want: "abc"},
		{name: "B alias for brace", content: "x{ab}", at: term.Coordinates{X: 2}, obj: 'B', want: "ab"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hx, _, _ := newHelix(t, tc.content, tc.at)
			inner := 'i'
			if tc.around {
				inner = 'a'
			}
			send(t, hx, key('m'), key(inner), key(tc.obj))
			assert.Equal(t, tc.want, sel(t, hx))
			assert.True(t, hx.IsNormalMode())
		})
	}

	t.Run("an unknown text object leaves the selection alone", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abc", term.Coordinates{})
		send(t, hx, key('m'), key('i'), key('Z'))
		assert.Equal(t, "a", sel(t, hx))
		assert.True(t, hx.IsNormalMode())
	})

	t.Run("esc cancels match mode", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abc", term.Coordinates{})
		send(t, hx, key('m'), namedKey(term.KeyEsc))
		assert.True(t, hx.IsNormalMode())
	})

	t.Run("a text object selection is operable", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "f(abc)", term.Coordinates{X: 3})
		send(t, hx, keys("mi(d")...)
		assert.Equal(t, "f()", buf.String())
	})

	t.Run("a text object selection can be extended", func(t *testing.T) {
		hx, _, _ := newHelix(t, "(ab) cd", term.Coordinates{X: 1})
		send(t, hx, keys("mi(")...)
		require.Equal(t, "ab", sel(t, hx))
		send(t, hx, key('v'), key('l'))
		assert.Equal(t, "ab)", sel(t, hx))
	})
}

// TestSearch pins / ? n N and the search-selection commands.
func TestSearch(t *testing.T) {
	t.Run("forward search selects the match", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo\nbar\nfoo", term.Coordinates{})
		send(t, hx, key('/'))
		require.True(t, hx.IsSearchMode())
		send(t, hx, keys("foo")...)
		send(t, hx, namedKey(term.KeyEnter))
		require.False(t, hx.IsSearchMode())
		assert.Equal(t, term.Coordinates{X: 2, Y: 2}, hx.CursorAtScroll(),
			"confirming the prompt jumps to the next match")
		assert.Equal(t, "foo", sel(t, hx))

		send(t, hx, key('n'))
		assert.Equal(t, term.Coordinates{X: 2}, hx.CursorAtScroll(), "n wraps")
		assert.Equal(t, "foo", sel(t, hx))
	})

	t.Run("N walks backwards", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo\nbar\nfoo", term.Coordinates{})
		send(t, hx, key('/'))
		send(t, hx, keys("foo")...)
		send(t, hx, namedKey(term.KeyEnter))
		send(t, hx, key('n'))
		require.Equal(t, term.Coordinates{X: 2}, hx.CursorAtScroll())
		send(t, hx, key('N'))
		assert.Equal(t, term.Coordinates{X: 2, Y: 2}, hx.CursorAtScroll())
		assert.Equal(t, "foo", sel(t, hx))
	})

	t.Run("? reverses the meaning of n", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo\nbar\nfoo", term.Coordinates{Y: 2})
		send(t, hx, key('?'))
		require.True(t, hx.IsSearchMode())
		send(t, hx, keys("foo")...)
		send(t, hx, namedKey(term.KeyEnter))
		require.Equal(t, 0, hx.CursorAtScroll().Y)
		send(t, hx, key('n'))
		assert.Equal(t, 2, hx.CursorAtScroll().Y, "n keeps searching backwards")
	})

	t.Run("* searches the selected word", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar\nfoo", term.Coordinates{})
		send(t, hx, keys("mi")...)
		send(t, hx, key('w'))
		require.Equal(t, "foo", sel(t, hx))
		_, handled := hx.Handle(key('*'))
		require.True(t, handled)
		send(t, hx, key('n'))
		assert.Equal(t, 1, hx.CursorAtScroll().Y)
	})

	t.Run("A-* searches the raw selection", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foobar\nxfoo", term.Coordinates{})
		send(t, hx, keys("2l")...)
		send(t, hx, key('v'))
		send(t, hx, key('h'))
		_, handled := hx.Handle(modKey(term.ModAlt, '*'))
		assert.True(t, handled)
	})

	t.Run("search is disabled by option", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo", term.Coordinates{}, WithSearch(false))
		_, handled := hx.Handle(key('/'))
		assert.False(t, handled)
		assert.False(t, hx.IsSearchMode())
	})

	t.Run("n without a search is unhandled", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo", term.Coordinates{})
		_, handled := hx.Handle(key('n'))
		assert.False(t, handled)
	})
}

// TestExpandShrinkSelection pins A-o / A-i and their arrow aliases,
// which need a syntax service and therefore stay unhandled here.
func TestExpandShrinkSelection(t *testing.T) {
	for _, ev := range []term.Event{
		modKey(term.ModAlt, 'o'), modKey(term.ModAlt, 'i'),
		modNamedKey(term.ModAlt, term.KeyArrowUp),
		modNamedKey(term.ModAlt, term.KeyArrowDown),
	} {
		hx, _, _ := newHelix(t, "f(abc)", term.Coordinates{X: 3})
		_, handled := hx.Handle(ev)
		assert.False(t, handled, "%v needs a syntax service", ev)
	}

	// With a syntax tree the caret's one-cell range grows to the node
	// around it and shrinks back.
	// An unexpanded caret is reported as a zero-width range.
	caret := term.Range{
		Start: term.Coordinates{X: 3}, End: term.Coordinates{X: 3}}
	single := term.Range{
		Start: term.Coordinates{X: 3}, End: term.Coordinates{X: 4}}
	inner := term.Range{
		Start: term.Coordinates{X: 2}, End: term.Coordinates{X: 5}}
	outer := term.Range{
		Start: term.Coordinates{X: 1}, End: term.Coordinates{X: 6}}

	newSyntaxHelix := func(t *testing.T) *Helix {
		t.Helper()
		hx, buf, _ := newHelix(t, "f(abc)", term.Coordinates{X: 3})
		buf.WithView(testSelectionView{
			View:   buf.View(),
			expand: map[term.Range]term.Range{caret: inner, inner: outer},
			shrink: map[term.Range]term.Range{outer: inner, inner: single},
		})
		return hx
	}

	for _, tc := range []struct {
		name   string
		expand term.Event
		shrink term.Event
	}{
		{name: "alt letters", expand: modKey(term.ModAlt, 'o'),
			shrink: modKey(term.ModAlt, 'i')},
		{name: "alt arrows",
			expand: modNamedKey(term.ModAlt, term.KeyArrowUp),
			shrink: modNamedKey(term.ModAlt, term.KeyArrowDown)},
	} {
		t.Run(tc.name+" expand and shrink", func(t *testing.T) {
			hx := newSyntaxHelix(t)
			send(t, hx, tc.expand)
			assert.Equal(t, "abc", sel(t, hx))
			send(t, hx, tc.expand)
			assert.Equal(t, "(abc)", sel(t, hx))
			send(t, hx, tc.shrink)
			assert.Equal(t, "abc", sel(t, hx))
		})
	}

	t.Run("an expanded selection is pinned until a motion rebinds it", func(t *testing.T) {
		hx := newSyntaxHelix(t)
		send(t, hx, modKey(term.ModAlt, 'o'))
		require.Equal(t, "abc", sel(t, hx))
		send(t, hx, key('d'))
		assert.Equal(t, "f()", hx.buf.String())
	})
}

// opCase drives an operator through the real handler and checks the
// resulting buffer, selection and caret.
type opCase struct {
	name    string
	content string
	at      term.Coordinates
	evs     []term.Event
	want    string
	wantSel string
	wantAt  *term.Coordinates
}

func runOpCases(t *testing.T, cases []opCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hx, buf, _ := newHelix(t, tc.content, tc.at)
			send(t, hx, tc.evs...)
			assert.Equal(t, tc.want, buf.String(), "buffer")
			if tc.wantSel != "" {
				assert.Equal(t, tc.wantSel, sel(t, hx), "selection")
			}
			if tc.wantAt != nil {
				assert.Equal(t, *tc.wantAt, hx.CursorAtScroll(), "caret")
			}
		})
	}
}

func at(x, y int) *term.Coordinates { return &term.Coordinates{X: x, Y: y} }

// TestDelete pins d and A-d.
func TestDelete(t *testing.T) {
	runOpCases(t, []opCase{
		{name: "d removes the one-cell selection", content: "abc",
			evs: keys("d"), want: "bc", wantAt: at(0, 0)},
		{name: "d removes a word selection", content: "foo bar",
			evs: keys("wd"), want: "bar"},
		{name: "d removes a line selection", content: "one\ntwo\nthree",
			evs: keys("xd"), want: "two\nthree"},
		{name: "d over several lines", content: "one\ntwo\nthree",
			evs: keys("xxd"), want: "three"},
		{name: "d at the buffer end", content: "ab", at: term.Coordinates{X: 1},
			evs: keys("d"), want: "a"},
		{name: "d on an empty buffer is inert", content: "", evs: keys("d"), want: ""},
		{name: "d on a wide glyph removes the whole rune", content: "世界",
			evs: keys("d"), want: "界"},
		{name: "alt-d deletes without yanking", content: "foo bar",
			evs: append(keys("w"), modKey(term.ModAlt, 'd')), want: "bar"},
		{name: "d drops select mode", content: "foo bar",
			evs: keys("vwd"), want: "bar"},
		{name: "counted d still deletes the selection once", content: "abcd",
			evs: keys("3d"), want: "bcd"},
	})

	t.Run("d yanks into the unnamed register", func(t *testing.T) {
		hx, _, clip := newHelix(t, "foo bar", term.Coordinates{})
		send(t, hx, keys("wd")...)
		data, err := clip.Paste(clipboard.DefaultRegisterID)
		require.NoError(t, err)
		assert.Equal(t, "foo ", data.Text)
	})

	t.Run("alt-d leaves the register alone", func(t *testing.T) {
		hx, _, clip := newHelix(t, "foo bar", term.Coordinates{})
		seedClipboard(t, clip, "KEEP", text.StandardSelection)
		send(t, hx, key('w'))
		send(t, hx, modKey(term.ModAlt, 'd'))
		data, err := clip.Paste(clipboard.DefaultRegisterID)
		require.NoError(t, err)
		assert.Equal(t, "KEEP", data.Text)
	})

	t.Run("the black hole register discards the text", func(t *testing.T) {
		hx, _, clip := newHelix(t, "foo bar", term.Coordinates{})
		seedClipboard(t, clip, "KEEP", text.StandardSelection)
		send(t, hx, keys("w\"_d")...)
		data, err := clip.Paste(clipboard.DefaultRegisterID)
		require.NoError(t, err)
		assert.Equal(t, "KEEP", data.Text)
	})
}

// TestChange pins c and A-c, which delete and then enter insert mode.
func TestChange(t *testing.T) {
	for _, tc := range []struct {
		name string
		ev   term.Event
	}{
		{name: "c", ev: key('c')},
		{name: "alt-c", ev: modKey(term.ModAlt, 'c')},
	} {
		t.Run(tc.name+" deletes and enters insert", func(t *testing.T) {
			hx, buf, _ := newHelix(t, "foo bar", term.Coordinates{})
			send(t, hx, key('w'), tc.ev)
			require.True(t, hx.IsEditMode())
			send(t, hx, keys("X")...)
			assert.Equal(t, "Xbar", buf.String())
		})
	}

	t.Run("c on an empty buffer still enters insert", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "", term.Coordinates{})
		send(t, hx, key('c'))
		assert.True(t, hx.IsEditMode())
		send(t, hx, keys("hi")...)
		assert.Equal(t, "hi", buf.String())
	})

	// delete_selection_impl opens a line instead of plainly entering
	// insert mode when the selection covers whole lines, so xc leaves a
	// blank line to type on rather than pulling the next one up.
	t.Run("xc leaves a blank line behind", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "foo\nbar", term.Coordinates{})
		send(t, hx, keys("xc")...)
		require.True(t, hx.IsEditMode())
		assert.Equal(t, "\nbar", buf.String())
		assert.Equal(t, term.Coordinates{}, hx.CursorAtScroll())
		send(t, hx, keys("hi")...)
		assert.Equal(t, "hi\nbar", buf.String())
	})

	t.Run("xxc replaces both lines with one blank line", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "foo\nbar\nbaz", term.Coordinates{})
		send(t, hx, keys("xxc")...)
		assert.Equal(t, "\nbaz", buf.String())
	})

	t.Run("alt-c is linewise too", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "foo\nbar", term.Coordinates{})
		send(t, hx, key('x'), modKey(term.ModAlt, 'c'))
		assert.Equal(t, "\nbar", buf.String())
	})
}

// TestYankAndPaste pins y, p, P and R.
func TestYankAndPaste(t *testing.T) {
	t.Run("y copies without deleting", func(t *testing.T) {
		hx, buf, clip := newHelix(t, "foo bar", term.Coordinates{})
		send(t, hx, keys("wy")...)
		assert.Equal(t, "foo bar", buf.String())
		data, err := clip.Paste(clipboard.DefaultRegisterID)
		require.NoError(t, err)
		assert.Equal(t, "foo ", data.Text)
		assert.Equal(t, "foo ", sel(t, hx), "the selection survives a yank")
	})

	t.Run("y also fills register 0", func(t *testing.T) {
		hx, _, clip := newHelix(t, "foo bar", term.Coordinates{})
		send(t, hx, keys("wy")...)
		data, err := clip.Paste(registerNameToID('0'))
		require.NoError(t, err)
		assert.Equal(t, "foo ", data.Text)
	})

	t.Run("a named register keeps its own copy", func(t *testing.T) {
		hx, _, clip := newHelix(t, "foo bar", term.Coordinates{})
		send(t, hx, keys("w\"ay")...)
		data, err := clip.Paste(registerNameToID('a'))
		require.NoError(t, err)
		assert.Equal(t, "foo ", data.Text)
	})

	// Helix never has less than one range, so dropping the cursor's
	// selection leaves the cell under the caret selected.
	t.Run("y after Unselect yanks the cell under the caret", func(t *testing.T) {
		hx, _, clip := newHelix(t, "abc", term.Coordinates{})
		require.True(t, hx.Unselect())
		_, handled := hx.Handle(key('y'))
		assert.True(t, handled)
		data, err := clip.Paste(clipboard.DefaultRegisterID)
		require.NoError(t, err)
		assert.Equal(t, "a", data.Text)
	})

	// Helix's p/P never replace the selection; only R does.
	for _, tc := range []struct {
		name string
		evs  []term.Event
		want string
	}{
		{name: "p pastes after the selection", evs: keys("wp"), want: "foo Xbar"},
		{name: "P pastes before the selection", evs: keys("wP"), want: "Xfoo bar"},
		{name: "R replaces the selection", evs: keys("wR"), want: "Xbar"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hx, buf, clip := newHelix(t, "foo bar", term.Coordinates{})
			seedClipboard(t, clip, "X", text.StandardSelection)
			send(t, hx, tc.evs...)
			assert.Equal(t, tc.want, buf.String())
		})
	}

	t.Run("counted p repeats the paste", func(t *testing.T) {
		hx, buf, clip := newHelix(t, "ab", term.Coordinates{})
		seedClipboard(t, clip, "X", text.StandardSelection)
		send(t, hx, keys("3p")...)
		assert.Equal(t, "aXXXb", buf.String())
	})

	t.Run("linewise paste lands on its own line", func(t *testing.T) {
		hx, buf, clip := newHelix(t, "one\ntwo", term.Coordinates{})
		seedClipboard(t, clip, "mid\n", text.LineSelection)
		send(t, hx, key('p'))
		assert.Equal(t, "one\nmid\ntwo", buf.String())
	})

	t.Run("p from an empty register is unhandled", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abc", term.Coordinates{})
		_, handled := hx.Handle(key('p'))
		assert.False(t, handled)
	})

	t.Run("yank then paste round trips", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "foo bar", term.Coordinates{})
		send(t, hx, keys("wy")...)
		send(t, hx, key('p'))
		assert.Equal(t, "foo foo bar", buf.String())
	})
}

// TestReplaceChar pins r<char>, which rewrites every selected cell.
func TestReplaceChar(t *testing.T) {
	runOpCases(t, []opCase{
		{name: "r replaces the cell under the caret", content: "abc",
			evs: keys("rz"), want: "zbc", wantSel: "z"},
		{name: "r replaces the whole selection", content: "foo bar",
			evs: keys("wrz"), want: "zzzzbar", wantSel: "zzzz"},
		{name: "r over a line selection", content: "ab\ncd",
			evs: keys("xr-"), want: "--\ncd"},
		{name: "r with a wide glyph", content: "世界",
			evs: keys("rx"), want: "x界"},
		{name: "r on a wide replacement", content: "ab",
			evs: append(keys("r"), key('界')), want: "界b"},
	})

	t.Run("r<space> uses a literal space", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "abc", term.Coordinates{})
		send(t, hx, key('r'), namedKey(term.KeySpace))
		assert.Equal(t, " bc", buf.String())
	})

	t.Run("r<tab> uses a literal tab", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "abc", term.Coordinates{})
		send(t, hx, key('r'), namedKey(term.KeyTab))
		assert.Equal(t, "\tbc", buf.String())
	})

	t.Run("r<enter> splits the line", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "abc", term.Coordinates{})
		send(t, hx, key('r'), namedKey(term.KeyEnter))
		assert.Equal(t, "\nbc", buf.String())
	})

	t.Run("esc cancels replace mode", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "abc", term.Coordinates{})
		send(t, hx, key('r'), namedKey(term.KeyEsc))
		assert.Equal(t, "abc", buf.String())
		assert.True(t, hx.IsNormalMode())
	})

	t.Run("r is a one-shot mode", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "abc", term.Coordinates{})
		send(t, hx, keys("rzz")...)
		assert.Equal(t, "zbc", buf.String(), "the second z is a normal-mode key")
	})

	t.Run("r on an empty buffer is inert", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "", term.Coordinates{})
		send(t, hx, keys("rz")...)
		assert.Equal(t, "", buf.String())
	})
}

// TestCaseOperators pins ~, ` and A-`.
func TestCaseOperators(t *testing.T) {
	runOpCases(t, []opCase{
		{name: "~ toggles a mixed selection", content: "aBc",
			evs: keys("w~"), want: "AbC"},
		{name: "~ toggles one cell", content: "abc", evs: keys("~"),
			want: "Abc", wantSel: "A"},
		{name: "backtick lowercases", content: "ABC",
			evs: keys("w`"), want: "abc"},
		{name: "alt-backtick uppercases", content: "abc",
			evs: append(keys("w"), modKey(term.ModAlt, '`')), want: "ABC"},
		{name: "case operators keep the selection", content: "abc",
			evs: keys("w~"), want: "ABC", wantSel: "ABC"},
		{name: "~ over wide glyphs is a no-op", content: "世界",
			evs: keys("w~"), want: "世界"},
	})
}

// TestJoin pins J and A-J.
func TestJoin(t *testing.T) {
	runOpCases(t, []opCase{
		{name: "J joins the next line", content: "one\ntwo",
			evs: keys("J"), want: "one two"},
		{name: "J across a line selection", content: "a\nb\nc",
			evs: keys("xxJ"), want: "a b\nc"},
		{name: "J at the last line is inert", content: "one",
			evs: keys("J"), want: "one"},
		{name: "counted J joins several lines", content: "a\nb\nc\nd",
			evs: keys("3xJ"), want: "a b c\nd"},
	})

	t.Run("alt-J selects the inserted space", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "one\ntwo", term.Coordinates{})
		send(t, hx, modKey(term.ModAlt, 'J'))
		assert.Equal(t, "one two", buf.String())
		assert.Equal(t, " ", sel(t, hx))
	})

	t.Run("J on an empty buffer is unhandled", func(t *testing.T) {
		hx, _, _ := newHelix(t, "", term.Coordinates{})
		_, handled := hx.Handle(key('J'))
		assert.False(t, handled)
	})
}

// TestIndentOperators pins >, < and =.
func TestIndentOperators(t *testing.T) {
	runOpCases(t, []opCase{
		{name: "> indents the line", content: "a\nb", evs: keys(">"), want: "  a\nb"},
		{name: "counted > indents repeatedly", content: "a", evs: keys("3>"),
			want: "      a"},
		{name: "< unindents", content: "    a", evs: keys("<"), want: "  a"},
		{name: "< on a flush line is inert", content: "a", evs: keys("<"), want: "a"},
		{name: "> across a multi line selection", content: "a\nb\nc",
			evs: keys("xx>"), want: "  a\n  b\nc"},
	})

	t.Run("= reindents to the syntax target", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "      a", term.Coordinates{})
		buf.WithView(testIndentView{View: buf.View(), indents: map[int]int{0: 0}})
		send(t, hx, key('='))
		assert.Equal(t, "a", buf.String())
	})

	// Helix maps the selection through the indent it inserts rather
	// than snapping it back to whole lines, so the first line's new
	// indent falls outside the range while the line ending it ended on
	// stays inside. The cursor has no cell for that line ending and
	// shows the range ending on b.
	t.Run("indent carries the selection through the inserted indent", func(t *testing.T) {
		hx, _, _ := newHelix(t, "a\nb\nc", term.Coordinates{})
		send(t, hx, keys("xx>")...)
		assert.Equal(t, "a\n  b", sel(t, hx))
		assert.Equal(t, []string{"a\n  b\n"}, sels(hx))
	})
}

// TestComments pins C-c.

// TestTrimSelection pins _, which shrinks the selection past the
// whitespace at either end.
func TestTrimSelection(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		at      term.Coordinates
		evs     []term.Event
		wantSel string
	}{
		{name: "drops a trailing space", content: "foo bar",
			evs: keys("w_"), wantSel: "foo"},
		{name: "drops leading whitespace", content: "  foo",
			evs: keys("vlll_"), wantSel: "fo"},
		{name: "drops both ends", content: " ab ",
			evs: keys("%_"), wantSel: "ab"},
		{name: "spans lines", content: "a\n  b  \nc",
			evs: keys("x_"), wantSel: "a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hx, _, _ := newHelix(t, tc.content, tc.at)
			send(t, hx, tc.evs...)
			assert.Equal(t, tc.wantSel, sel(t, hx))
		})
	}

	t.Run("a selection with no whitespace is unhandled", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar", term.Coordinates{})
		send(t, hx, key('e'))
		require.Equal(t, "foo", sel(t, hx))
		_, handled := hx.Handle(key('_'))
		assert.False(t, handled)
		assert.Equal(t, "foo", sel(t, hx))
	})

	t.Run("an all whitespace selection collapses", func(t *testing.T) {
		hx, _, _ := newHelix(t, "  a", term.Coordinates{})
		send(t, hx, key('w'))
		require.Equal(t, "  ", sel(t, hx))
		send(t, hx, key('_'))
		assert.Equal(t, " ", sel(t, hx))
	})

	t.Run("a backward selection keeps its direction", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar", term.Coordinates{X: 4})
		send(t, hx, key('v'), key('b'))
		require.Equal(t, "foo b", sel(t, hx))
		send(t, hx, key('_'))
		assert.Equal(t, "foo b", sel(t, hx), "neither end is blank")
	})
}
func TestComments(t *testing.T) {
	t.Run("ctrl-c toggles a line comment", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "a\nb", term.Coordinates{})
		hx.cursor.SetCommentSpec(text.CommentSpec{Line: []string{"//"}})
		buf.WithView(testCommentView{View: buf.View(), line: []string{"//"}})
		send(t, hx, modKey(term.ModCtrl, 'c'))
		assert.Equal(t, "// a\nb", buf.String())
		send(t, hx, modKey(term.ModCtrl, 'c'))
		assert.Equal(t, "a\nb", buf.String())
	})

	t.Run("ctrl-c across a selection", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "a\nb\nc", term.Coordinates{})
		hx.cursor.SetCommentSpec(text.CommentSpec{Line: []string{"//"}})
		send(t, hx, keys("xx")...)
		send(t, hx, modKey(term.ModCtrl, 'c'))
		assert.Equal(t, "// a\n// b\nc", buf.String())
	})
}

// TestInsertEntry pins i, a, I, A, o and O.
func TestInsertEntry(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		at      term.Coordinates
		evs     []term.Event
		want    string
	}{
		{name: "i inserts before the selection", content: "foo bar",
			evs: append(keys("wi"), keys("X")...), want: "Xfoo bar"},
		{name: "a inserts after the selection", content: "foo bar",
			evs: append(keys("wa"), keys("X")...), want: "foo Xbar"},
		{name: "i at the caret", content: "abc",
			evs: append(keys("i"), keys("X")...), want: "Xabc"},
		{name: "a at the caret", content: "abc",
			evs: append(keys("a"), keys("X")...), want: "aXbc"},
		{name: "I goes to the first non blank", content: "  abc",
			at: term.Coordinates{X: 4}, evs: append(keys("I"), keys("X")...),
			want: "  Xabc"},
		{name: "A goes to the line end", content: "abc",
			evs: append(keys("A"), keys("X")...), want: "abcX"},
		{name: "o opens below", content: "a\nb",
			evs: append(keys("o"), keys("X")...), want: "a\nX\nb"},
		{name: "O opens above", content: "a\nb",
			evs: append(keys("O"), keys("X")...), want: "X\na\nb"},
		{name: "counted o opens several lines", content: "a",
			evs: keys("3o"), want: "a\n\n\n"},
		{name: "a at the buffer end appends", content: "ab",
			at: term.Coordinates{X: 1}, evs: append(keys("a"), keys("X")...),
			want: "abX"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hx, buf, _ := newHelix(t, tc.content, tc.at)
			send(t, hx, tc.evs...)
			assert.Equal(t, tc.want, buf.String())
		})
	}
}

// TestAddNewline pins [<space> and ]<space>, which do not move the caret.
func TestAddNewline(t *testing.T) {
	t.Run("]<space> adds a line below", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "a\nb", term.Coordinates{})
		send(t, hx, key(']'), namedKey(term.KeySpace))
		assert.Equal(t, "a\n\nb", buf.String())
		assert.Equal(t, term.Coordinates{}, hx.CursorAtScroll())
		assert.True(t, hx.IsNormalMode())
	})

	t.Run("[<space> adds a line above", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "a\nb", term.Coordinates{})
		send(t, hx, key('['), namedKey(term.KeySpace))
		assert.Equal(t, "\na\nb", buf.String())
		assert.Equal(t, term.Coordinates{Y: 1}, hx.CursorAtScroll())
	})

	t.Run("counted ]<space>", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "a", term.Coordinates{})
		send(t, hx, keys("2]")...)
		send(t, hx, namedKey(term.KeySpace))
		assert.Equal(t, "a\n\n", buf.String())
	})
}

// TestSurround pins ms, mr and md.
func TestSurround(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		at      term.Coordinates
		evs     []term.Event
		want    string
		wantSel string
	}{
		{name: "ms wraps the selection in parens", content: "foo bar",
			evs: keys("wms("), want: "(foo )bar", wantSel: "(foo )"},
		{name: "ms with the closing key uses the same pair", content: "ab",
			evs: keys("ms)"), want: "(a)b"},
		{name: "ms with braces", content: "ab", evs: keys("ms{"), want: "{a}b"},
		{name: "ms with brackets", content: "ab", evs: keys("ms["), want: "[a]b"},
		{name: "ms with angle brackets", content: "ab", evs: keys("ms<"), want: "<a>b"},
		{name: "ms with a quote surrounds with itself", content: "ab",
			evs: keys("ms\""), want: "\"a\"b"},
		{name: "md removes the pair", content: "(foo)", at: term.Coordinates{X: 2},
			evs: keys("md("), want: "foo"},
		{name: "md with quotes", content: "\"foo\"", at: term.Coordinates{X: 2},
			evs: keys("md\""), want: "foo"},
		{name: "mr swaps the pair", content: "(foo)", at: term.Coordinates{X: 2},
			evs: keys("mr(["), want: "[foo]"},
		{name: "mr from braces to parens", content: "{foo}",
			at: term.Coordinates{X: 2}, evs: keys("mr{("), want: "(foo)"},
		{name: "md with no surrounding pair is inert", content: "foo",
			evs: keys("md("), want: "foo"},
		{name: "mr with no surrounding pair is inert", content: "foo",
			evs: keys("mr(["), want: "foo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hx, buf, _ := newHelix(t, tc.content, tc.at)
			send(t, hx, tc.evs...)
			assert.Equal(t, tc.want, buf.String(), "buffer")
			assert.True(t, hx.IsNormalMode(), "surround is a one shot mode")
			if tc.wantSel != "" {
				assert.Equal(t, tc.wantSel, sel(t, hx), "selection")
			}
		})
	}

	t.Run("esc cancels a pending surround", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "ab", term.Coordinates{})
		send(t, hx, keys("ms")...)
		send(t, hx, namedKey(term.KeyEsc))
		assert.Equal(t, "ab", buf.String())
		assert.True(t, hx.IsNormalMode())
	})

	t.Run("mr waits for two delimiters", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "(foo)", term.Coordinates{X: 2})
		send(t, hx, keys("mr(")...)
		assert.False(t, hx.IsNormalMode(), "still waiting for the target pair")
		send(t, hx, key('{'))
		assert.Equal(t, "{foo}", buf.String())
	})

	t.Run("ms across lines wraps the whole span", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "one\ntwo", term.Coordinates{})
		send(t, hx, keys("xxms(")...)
		assert.Equal(t, "(one\ntwo)", buf.String())
	})

	t.Run("md on an empty pair leaves the caret in place", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "f()", term.Coordinates{X: 1})
		send(t, hx, keys("md(")...)
		assert.Equal(t, "f", buf.String())
		assert.True(t, hx.IsNormalMode())
	})

	t.Run("a surround delimiter cannot be a modified key", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "ab", term.Coordinates{})
		send(t, hx, keys("ms")...)
		_, handled := hx.Handle(modKey(term.ModCtrl, 'g'))
		assert.False(t, handled)
		assert.Equal(t, "ab", buf.String())
	})
}

// TestIncrement pins C-a and C-x, which hand the text under the
// selection to the incrementors as it is: a cursor on one digit of a
// number changes that digit alone, so the whole number has to be
// selected for it to be counted as one.
func TestIncrement(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		at      term.Coordinates
		evs     []term.Event
		want    string
	}{
		{name: "ctrl-a increments", content: "1",
			evs: []term.Event{modKey(term.ModCtrl, 'a')}, want: "2"},
		{name: "ctrl-x decrements", content: "2",
			evs: []term.Event{modKey(term.ModCtrl, 'x')}, want: "1"},
		{name: "counted increment", content: "1",
			evs: append(keys("5"), modKey(term.ModCtrl, 'a')), want: "6"},
		{name: "rolls over digits", content: "9",
			evs: []term.Event{modKey(term.ModCtrl, 'a')}, want: "10"},
		{name: "borrows across the selected digits", content: "10",
			evs: append(keys("miw"), modKey(term.ModCtrl, 'x')), want: "9"},
		{name: "keeps zero padding", content: "007",
			evs: append(keys("miw"), modKey(term.ModCtrl, 'a')), want: "008"},
		{name: "padding survives a carry", content: "099",
			evs: append(keys("miw"), modKey(term.ModCtrl, 'a')), want: "100"},
		{name: "crosses zero into negative", content: "0",
			evs: []term.Event{modKey(term.ModCtrl, 'x')}, want: "-1"},
		{name: "a selected negative number increments", content: "-2",
			evs: append(keys("vl"), modKey(term.ModCtrl, 'a')), want: "-1"},
		{name: "a cursor off the number does nothing", content: "ab 12",
			evs: []term.Event{modKey(term.ModCtrl, 'a')}, want: "ab 12"},
		{name: "a cursor on one digit changes that digit alone", content: "1234",
			at: term.Coordinates{X: 2}, evs: []term.Event{modKey(term.ModCtrl, 'a')},
			want: "1244"},
		{name: "a selected word with letters is left alone", content: "a1",
			evs: append(keys("miw"), modKey(term.ModCtrl, 'a')), want: "a1"},
		{name: "hexadecimal", content: "0x0f",
			evs: append(keys("miw"), modKey(term.ModCtrl, 'a')), want: "0x10"},
		{name: "a date moves by a day", content: "2021-12-31",
			evs: append(keys("vlllllllll"), modKey(term.ModCtrl, 'a')), want: "2022-01-01"},
		{name: "no number leaves the line alone", content: "abc",
			evs: []term.Event{modKey(term.ModCtrl, 'a')}, want: "abc"},
		{name: "empty buffer is inert", content: "",
			evs: []term.Event{modKey(term.ModCtrl, 'a')}, want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hx, buf, _ := newHelix(t, tc.content, tc.at)
			send(t, hx, tc.evs...)
			assert.Equal(t, tc.want, buf.String())
		})
	}

	t.Run("the new digits stay selected", func(t *testing.T) {
		hx, _, _ := newHelix(t, "9", term.Coordinates{})
		send(t, hx, modKey(term.ModCtrl, 'a'))
		assert.Equal(t, "10", sel(t, hx))
	})

	t.Run("a multi line selection is rejected", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "1\n2", term.Coordinates{})
		send(t, hx, keys("xx")...)
		_, handled := hx.Handle(modKey(term.ModCtrl, 'a'))
		assert.False(t, handled)
		assert.Equal(t, "1\n2", buf.String())
	})
}

// TestTextObjects pins the mi/ma pairs.
func TestTextObjects(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		at      term.Coordinates
		evs     []term.Event
		wantSel string
	}{
		{name: "miw selects the inner word", content: "foo bar",
			at: term.Coordinates{X: 5}, evs: keys("miw"), wantSel: "bar"},
		{name: "maw includes the trailing space", content: "foo bar",
			at: term.Coordinates{X: 1}, evs: keys("maw"), wantSel: "foo "},
		{name: "miW spans punctuation", content: "a.b c",
			evs: keys("miW"), wantSel: "a.b"},
		{name: "mi\" selects inside quotes", content: "x \"abc\" y",
			at: term.Coordinates{X: 4}, evs: keys("mi\""), wantSel: "abc"},
		{name: "ma\" includes the quotes", content: "x \"abc\" y",
			at: term.Coordinates{X: 4}, evs: keys("ma\""), wantSel: "\"abc\""},
		{name: "mi( selects inside parens", content: "f(ab)",
			at: term.Coordinates{X: 3}, evs: keys("mi("), wantSel: "ab"},
		{name: "ma( includes the parens", content: "f(ab)",
			at: term.Coordinates{X: 3}, evs: keys("ma("), wantSel: "(ab)"},
		{name: "mi{ selects inside braces", content: "f{ab}",
			at: term.Coordinates{X: 3}, evs: keys("mi{"), wantSel: "ab"},
		{name: "mi[ selects inside brackets", content: "f[ab]",
			at: term.Coordinates{X: 3}, evs: keys("mi["), wantSel: "ab"},
		{name: "mip selects the paragraph", content: "a\nb\n\nc",
			evs: keys("mip"), wantSel: "a\nb\n"},
		{name: "mim finds the closest pair", content: "f(ab)",
			at: term.Coordinates{X: 3}, evs: keys("mim"), wantSel: "ab"},
		{name: "mam includes the closest pair", content: "f(ab)",
			at: term.Coordinates{X: 3}, evs: keys("mam"), wantSel: "(ab)"},
		{name: "mim steps over a nested pair", content: "{a (b) c}",
			at: term.Coordinates{X: 1}, evs: keys("mim"), wantSel: "a (b) c"},
		{name: "mim picks the innermost enclosing pair", content: "{a [b] c}",
			at: term.Coordinates{X: 4}, evs: keys("mim"), wantSel: "b"},
		{name: "mim ignores brackets that shut before the caret",
			content: "(a) [b]", at: term.Coordinates{X: 5},
			evs: keys("mim"), wantSel: "b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hx, _, _ := newHelix(t, tc.content, tc.at)
			send(t, hx, tc.evs...)
			assert.Equal(t, tc.wantSel, sel(t, hx))
			assert.True(t, hx.IsNormalMode(), "mi/ma are one shot")
		})
	}

	t.Run("an unknown object leaves the selection alone", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar", term.Coordinates{})
		send(t, hx, keys("wmiZ")...)
		assert.Equal(t, "foo ", sel(t, hx))
		assert.True(t, hx.IsNormalMode())
	})

	t.Run("mim outside any pair leaves the selection alone", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar", term.Coordinates{})
		send(t, hx, keys("wmim")...)
		assert.Equal(t, "foo ", sel(t, hx))
		assert.True(t, hx.IsNormalMode())
	})

	t.Run("a text object is a delete target", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "f(ab)", term.Coordinates{X: 3})
		send(t, hx, keys("mi(d")...)
		assert.Equal(t, "f()", buf.String())
	})

	t.Run("esc cancels match mode", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar", term.Coordinates{})
		send(t, hx, key('w'), key('m'))
		send(t, hx, namedKey(term.KeyEsc))
		assert.True(t, hx.IsNormalMode())
		assert.Equal(t, "foo ", sel(t, hx))
	})

	t.Run("ma matches mi for every object", func(t *testing.T) {
		for _, tc := range []struct {
			content string
			at      term.Coordinates
			object  rune
			want    string
		}{
			{content: "x 'abc' y", at: term.Coordinates{X: 4}, object: '\'',
				want: "'abc'"},
			{content: "x `abc` y", at: term.Coordinates{X: 4}, object: '`',
				want: "`abc`"},
			{content: "f<ab>", at: term.Coordinates{X: 3}, object: '<',
				want: "<ab>"},
			{content: "One. Two.", at: term.Coordinates{X: 1}, object: 's',
				want: "One. "},
		} {
			hx, _, _ := newHelix(t, tc.content, tc.at)
			send(t, hx, keys("ma")...)
			send(t, hx, key(tc.object))
			assert.Equal(t, tc.want, sel(t, hx), "ma%c", tc.object)
		}
	})

	t.Run("mis selects the sentence", func(t *testing.T) {
		hx, _, _ := newHelix(t, "One. Two.", term.Coordinates{X: 1})
		send(t, hx, keys("mis")...)
		assert.Equal(t, "One.", sel(t, hx))
	})

	t.Run("a modified key cancels match mode", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar", term.Coordinates{})
		send(t, hx, key('m'))
		_, handled := hx.Handle(modKey(term.ModCtrl, 'g'))
		assert.False(t, handled)
		assert.True(t, hx.IsNormalMode())
	})
}

// TestRegisters pins the " prefix, including the black hole register.
func TestRegisters(t *testing.T) {
	t.Run("a named register round trips", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "foo bar", term.Coordinates{})
		send(t, hx, keys("\"awy")...)
		send(t, hx, keys("gl")...)
		send(t, hx, keys("\"ap")...)
		assert.Equal(t, "foo barfoo ", buf.String())
	})

	t.Run("the register only applies to the next operator", func(t *testing.T) {
		hx, _, clip := newHelix(t, "foo bar", term.Coordinates{})
		send(t, hx, keys("\"awy")...)
		send(t, hx, keys("wy")...)
		data, err := clip.Paste(registerNameToID('a'))
		require.NoError(t, err)
		assert.Equal(t, "foo ", data.Text, "the second yank used the default register")
	})

	t.Run("an invalid register name is swallowed", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abc", term.Coordinates{})
		send(t, hx, key('"'))
		_, handled := hx.Handle(modKey(term.ModCtrl, 'g'))
		assert.True(t, handled)
		assert.True(t, hx.IsNormalMode())
	})

	t.Run("a count survives the register prefix", func(t *testing.T) {
		hx, buf, clip := newHelix(t, "ab", term.Coordinates{})
		require.NoError(t, clip.Copy(registerNameToID('a'),
			clipboard.Data{Text: "X", Metadata: text.StandardSelection}))
		send(t, hx, keys("3\"ap")...)
		assert.Equal(t, "aXXXb", buf.String())
	})

	t.Run("an aborted register prefix does not eat a zero key", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abc", term.Coordinates{})
		send(t, hx, key('"'))
		_, handled := hx.Handle(term.Event{Type: term.EventKey})
		assert.True(t, handled, "the prefix consumes the key")
		assert.True(t, hx.IsNormalMode())
	})
}

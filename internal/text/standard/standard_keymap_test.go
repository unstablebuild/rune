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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/text"
)

// newStandardKeymapHandler builds a real standard handler over content with an
// in-memory clipboard, positions the cursor, and returns the handler
// plus the backing buffer and clipboard for assertions.
func newStandardKeymapHandler(
	t *testing.T, content string, at term.Coordinates, opts ...Option,
) (text.Handler, *cell.Buffer, clipboard.Register) {
	t.Helper()
	uri, err := workspaceapi.ParseURI("test:///")
	require.NoError(t, err)
	buf := cell.NewBuffer()
	buf.ReadFrom(strings.NewReader(content))
	clip := clipboard.NewInMemory()
	h := NewHandler(buf, uri, text.IndentRuneTab, 0,
		append([]Option{WithKeymap(KeymapMacOS), WithClipboard(clip), WithTabspaces(1)},
			opts...)...)
	h.Resize(80, 20)
	if at.X != 0 || at.Y != 0 {
		require.True(t, h.SetCursorAtScroll(at))
	}
	return h, buf, clip
}

func TestStandardKeymapMotion(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		at      term.Coordinates
		ev      term.Event
		wantAt  term.Coordinates
	}{
		{
			name:    "ctrl-right moves to end of word",
			content: "foo bar",
			ev:      term.Event{Type: term.EventKey, Mod: term.ModCtrl, Key: term.KeyArrowRight},
			wantAt:  term.Coordinates{X: 3},
		},
		{
			name:    "ctrl-left moves to start of word",
			content: "foo bar",
			at:      term.Coordinates{X: 7},
			ev:      term.Event{Type: term.EventKey, Mod: term.ModCtrl, Key: term.KeyArrowLeft},
			wantAt:  term.Coordinates{X: 4},
		},
		{
			name:    "cmd-left moves to start of indented line",
			content: "    hello",
			at:      term.Coordinates{X: 9},
			ev:      term.Event{Type: term.EventKey, Mod: term.ModMeta, Key: term.KeyArrowLeft},
			wantAt:  term.Coordinates{},
		},
		{
			name:    "ctrl-home moves to first line",
			content: "a\nb\nc",
			at:      term.Coordinates{Y: 2},
			ev:      term.Event{Type: term.EventKey, Mod: term.ModCtrl, Key: term.KeyHome},
			wantAt:  term.Coordinates{Y: 0},
		},
		{
			name:    "ctrl-end moves to last line",
			content: "a\nb\nc",
			ev:      term.Event{Type: term.EventKey, Mod: term.ModCtrl, Key: term.KeyEnd},
			wantAt:  term.Coordinates{Y: 2},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := newStandardKeymapHandler(t, tc.content, tc.at)
			_, handled := h.Handle(tc.ev)
			assert.True(t, handled, "event must be handled")
			assert.Equal(t, tc.wantAt, h.CursorAtScroll())
		})
	}
}

func TestStandardKeymapSelectAll(t *testing.T) {
	for _, tc := range []struct {
		name string
		mod  term.Modifier
	}{
		{name: "ctrl-a", mod: term.ModCtrl},
		{name: "meta-a", mod: term.ModMeta},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := newStandardKeymapHandler(t, "hello\nworld", term.Coordinates{})
			_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: tc.mod, Ch: 'a'})
			require.True(t, handled)
			sel, ok := h.Selection()
			require.True(t, ok, "%s must create a selection", tc.name)
			assert.Equal(t, "hello\nworld", sel)
		})
	}
}

func TestStandardKeymapSelectLine(t *testing.T) {
	h, _, _ := newStandardKeymapHandler(t, "hello\nworld", term.Coordinates{})
	_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModMeta, Ch: 'l'})
	require.True(t, handled)
	_, ok := h.Selection()
	assert.True(t, ok, "cmd-l must select the line")
}

func TestStandardKeymapRecenter(t *testing.T) {
	content := strings.Repeat("line\n", 60)
	h, _, _ := newStandardKeymapHandler(t, content, term.Coordinates{Y: 40})
	_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'l'})
	require.True(t, handled, "ctrl-l must recenter")
	_, ok := h.Selection()
	assert.False(t, ok, "ctrl-l must not create a selection")
}

func TestStandardKeymapClipboard(t *testing.T) {
	for _, tc := range []struct {
		name string
		mod  term.Modifier
	}{
		{name: "ctrl", mod: term.ModCtrl},
		{name: "meta", mod: term.ModMeta},
	} {
		t.Run(tc.name+"-x cuts current line when no selection", func(t *testing.T) {
			h, buf, clip := newStandardKeymapHandler(t, "one\ntwo\nthree", term.Coordinates{Y: 1})
			_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: tc.mod, Ch: 'x'})
			require.True(t, handled)
			assert.Equal(t, "one\nthree", buf.String())
			data, err := clip.Paste(clipboard.DefaultRegisterID)
			require.NoError(t, err)
			assert.Contains(t, data.Text, "two")
		})

		t.Run(tc.name+"-v pastes clipboard", func(t *testing.T) {
			h, buf, clip := newStandardKeymapHandler(t, "ab", term.Coordinates{})
			require.NoError(t, clip.Copy(clipboard.DefaultRegisterID,
				clipboard.Data{Text: "X", Metadata: text.StandardSelection}))
			_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: tc.mod, Ch: 'v'})
			require.True(t, handled)
			assert.Equal(t, "Xab", buf.String())
		})
	}

	t.Run("ctrl-c copies selection without deleting", func(t *testing.T) {
		h, buf, clip := newStandardKeymapHandler(t, "one\ntwo\nthree", term.Coordinates{Y: 1})
		_, _ = h.Handle(term.Event{Type: term.EventKey, Mod: term.ModMeta, Ch: 'l'})
		_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'c'})
		require.True(t, handled)
		assert.Equal(t, "one\ntwo\nthree", buf.String())
		data, err := clip.Paste(clipboard.DefaultRegisterID)
		require.NoError(t, err)
		assert.Contains(t, data.Text, "two")
	})

	t.Run("ctrl-c copies current line when no selection", func(t *testing.T) {
		h, buf, clip := newStandardKeymapHandler(t, "one\ntwo\nthree", term.Coordinates{Y: 1})
		_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'c'})
		require.True(t, handled)
		assert.Equal(t, "one\ntwo\nthree", buf.String())
		data, err := clip.Paste(clipboard.DefaultRegisterID)
		require.NoError(t, err)
		assert.Equal(t, "two\n", data.Text)
		assert.Equal(t, text.LineSelection, data.Metadata)
	})

	t.Run("cmd-c copies current line when no selection", func(t *testing.T) {
		h, buf, clip := newStandardKeymapHandler(t, "one\ntwo\nthree", term.Coordinates{Y: 1})
		_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModMeta, Ch: 'c'})
		require.True(t, handled)
		assert.Equal(t, "one\ntwo\nthree", buf.String())
		data, err := clip.Paste(clipboard.DefaultRegisterID)
		require.NoError(t, err)
		assert.Equal(t, "two\n", data.Text)
		assert.Equal(t, text.LineSelection, data.Metadata)
	})

	t.Run("empty-selection copy pastes line-wise above the cursor line", func(t *testing.T) {
		h, buf, _ := newStandardKeymapHandler(t, "one\ntwo\nthree", term.Coordinates{Y: 1})
		_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'c'})
		require.True(t, handled)
		_, handled = h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'v'})
		require.True(t, handled)
		assert.Equal(t, "one\ntwo\ntwo\nthree", buf.String())
	})
}

func TestStandardKeymapUndoRedo(t *testing.T) {
	for _, tc := range []struct {
		name string
		mod  term.Modifier
	}{
		{name: "ctrl", mod: term.ModCtrl},
		{name: "meta", mod: term.ModMeta},
	} {
		t.Run(tc.name+"-z undoes", func(t *testing.T) {
			h, buf, _ := newStandardKeymapHandler(t, "", term.Coordinates{})
			_, _ = h.Handle(term.Event{Type: term.EventKey, Ch: 'a'})
			require.Equal(t, "a", buf.String())
			_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: tc.mod, Ch: 'z'})
			require.True(t, handled)
			assert.Equal(t, "", buf.String())
		})

		t.Run(tc.name+"-shift-z redoes", func(t *testing.T) {
			h, buf, _ := newStandardKeymapHandler(t, "", term.Coordinates{})
			_, _ = h.Handle(term.Event{Type: term.EventKey, Ch: 'a'})
			_, _ = h.Handle(term.Event{Type: term.EventKey, Mod: tc.mod, Ch: 'z'})
			_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: tc.mod, Ch: 'Z'})
			require.True(t, handled)
			assert.Equal(t, "a", buf.String())
		})
	}

	t.Run("ctrl-y redoes", func(t *testing.T) {
		h, buf, _ := newStandardKeymapHandler(t, "", term.Coordinates{})
		_, _ = h.Handle(term.Event{Type: term.EventKey, Ch: 'a'})
		_, _ = h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'z'})
		require.Equal(t, "", buf.String())
		_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'y'})
		require.True(t, handled)
		assert.Equal(t, "a", buf.String())
	})

}

func TestStandardKeymapSelectNextOccurrence(t *testing.T) {
	for _, tc := range []struct {
		name string
		mod  term.Modifier
	}{
		{name: "ctrl-d", mod: term.ModCtrl},
		{name: "meta-d", mod: term.ModMeta},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := newStandardKeymapHandler(t, "foo bar foo", term.Coordinates{})
			_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: tc.mod, Ch: 'd'})
			require.True(t, handled)
			sel, ok := h.Selection()
			require.True(t, ok)
			assert.Equal(t, "foo", sel)
		})
	}
}

func TestStandardKeymapDeletion(t *testing.T) {
	t.Run("ctrl-backspace deletes word to the left", func(t *testing.T) {
		h, buf, _ := newStandardKeymapHandler(t, "foo bar", term.Coordinates{X: 7})
		_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Key: term.KeyBackspace})
		require.True(t, handled)
		assert.Equal(t, "foo ", buf.String())
	})

	t.Run("ctrl-delete deletes to the end of the word to the right", func(t *testing.T) {
		h, buf, _ := newStandardKeymapHandler(t, "foo bar", term.Coordinates{})
		_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Key: term.KeyDelete})
		require.True(t, handled)
		assert.Equal(t, " bar", buf.String())
	})

	t.Run("ctrl-delete stops at the end of the line", func(t *testing.T) {
		h, buf, _ := newStandardKeymapHandler(t, "one\n\ntwo", term.Coordinates{X: 2})
		_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Key: term.KeyDelete})
		require.True(t, handled)
		assert.Equal(t, "on\n\ntwo", buf.String())
	})

	t.Run("ctrl-delete on an empty line joins the next line", func(t *testing.T) {
		h, buf, _ := newStandardKeymapHandler(t, "one\n\ntwo", term.Coordinates{Y: 1})
		_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Key: term.KeyDelete})
		require.True(t, handled)
		assert.Equal(t, "one\ntwo", buf.String())
	})

	t.Run("alt-backspace deletes word to the left", func(t *testing.T) {
		h, buf, _ := newStandardKeymapHandler(t, "left middle right", term.Coordinates{X: 11})
		_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModAlt, Key: term.KeyBackspace})
		require.True(t, handled)
		assert.Equal(t, "left  right", buf.String())
	})

	t.Run("alt-delete deletes to the end of the word to the right", func(t *testing.T) {
		h, buf, _ := newStandardKeymapHandler(t, "left middle right", term.Coordinates{X: 5})
		_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModAlt, Key: term.KeyDelete})
		require.True(t, handled)
		assert.Equal(t, "left  right", buf.String())
	})

	t.Run("alt-backspace at buffer start leaves no selection", func(t *testing.T) {
		h, buf, _ := newStandardKeymapHandler(t, "foo", term.Coordinates{})
		_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModAlt, Key: term.KeyBackspace})
		assert.False(t, handled)
		assert.Equal(t, "foo", buf.String())
		_, selected := h.Selection()
		assert.False(t, selected)
	})

	t.Run("alt-delete at buffer end leaves no selection", func(t *testing.T) {
		h, buf, _ := newStandardKeymapHandler(t, "foo", term.Coordinates{X: 3})
		_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModAlt, Key: term.KeyDelete})
		assert.False(t, handled)
		assert.Equal(t, "foo", buf.String())
		_, selected := h.Selection()
		assert.False(t, selected)
	})

	t.Run("ctrl-shift-k deletes the line", func(t *testing.T) {
		h, buf, _ := newStandardKeymapHandler(t, "one\ntwo\nthree", term.Coordinates{Y: 1})
		_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'K'})
		require.True(t, handled)
		assert.Equal(t, "one\nthree", buf.String())
	})
}

func TestStandardKeymapTypingReplacesSelection(t *testing.T) {
	const word = "<s-right><s-right><s-right>"
	paste := func(s string) *string { return &s }
	for _, tc := range []struct {
		name    string
		content string
		at      term.Coordinates
		opts    []Option
		clip    string  // seeded into the clipboard default register
		setup   string  // input tokens that establish the selection
		wantSel string  // selection after setup; empty expects none
		input   string  // input tokens dispatched after setup
		paste   *string // bracketed paste payload dispatched after setup
		want    string
		undos   []string // buffer after each successive undo
	}{
		{
			name:    "typed char replaces word selection",
			content: "one two",
			setup:   word,
			wantSel: "one",
			input:   "X",
			want:    "X two",
			undos:   []string{" two", "one two"},
		},
		{
			name:    "typed char replaces reverse selection",
			content: "one two",
			at:      term.Coordinates{X: 3},
			setup:   "<s-left><s-left><s-left>",
			wantSel: "one",
			input:   "X",
			want:    "X two",
		},
		{
			name:    "typed char replaces multi-line selection",
			content: "one\ntwo\nthree",
			setup:   "<s-down><s-down>",
			wantSel: "one\ntwo\n",
			input:   "X",
			want:    "Xthree",
		},
		{
			name:    "typed char replaces line selection",
			content: "one\ntwo",
			setup:   "<m-l>",
			wantSel: "one\n",
			input:   "X",
			want:    "Xtwo",
		},
		{
			name:    "typed char replaces select-all",
			content: "one\ntwo",
			setup:   "<m-a>",
			wantSel: "one\ntwo",
			input:   "X",
			want:    "X",
		},
		{
			name:    "typed wide char replaces wide selection",
			content: "界界 tail",
			setup:   "<s-right><s-right>",
			wantSel: "界界",
			input:   "界",
			want:    "界 tail",
		},
		{
			name:    "typed char replaces null-cell selection",
			content: "ab\x00\x00cd",
			setup:   "<s-right><s-right><s-right><s-right>",
			wantSel: "ab\x00\x00",
			input:   "X",
			want:    "Xcd",
		},
		{
			name:    "typed char with collapsed selection inserts",
			content: "one two",
			setup:   "<s-right><s-left>",
			input:   "X",
			want:    "Xone two",
		},
		{
			name:    "auto-pair opener replaces selection with the pair",
			content: "one two",
			opts:    []Option{WithAutoPair(true)},
			setup:   word,
			wantSel: "one",
			input:   "(",
			want:    "() two",
		},
		{
			name:    "enter replaces selection with newline",
			content: "one two",
			setup:   word,
			wantSel: "one",
			input:   "<enter>",
			want:    "\n two",
		},
		{
			name:    "space replaces selection with space",
			content: "one two",
			setup:   word,
			wantSel: "one",
			input:   "<space>",
			want:    "  two",
		},
		{
			name:    "backspace deletes selection without inserting",
			content: "one two",
			setup:   word,
			wantSel: "one",
			input:   "<backspace>",
			want:    " two",
		},
		{
			name:    "delete deletes selection without inserting",
			content: "one two",
			setup:   word,
			wantSel: "one",
			input:   "<delete>",
			want:    " two",
		},
		{
			name:    "tab indents selection instead of replacing",
			content: "one two",
			setup:   word,
			wantSel: "one",
			input:   "<tab>",
			want:    "\tone two",
		},
		{
			name:    "bracketed paste replaces selection",
			content: "one two",
			setup:   word,
			wantSel: "one",
			paste:   paste("AB"),
			want:    "AB two",
			undos:   []string{"one two"},
		},
		{
			name:    "bracketed multi-line paste replaces mid-buffer selection",
			content: "one two three",
			at:      term.Coordinates{X: 4},
			setup:   word,
			wantSel: "two",
			paste:   paste("mid\nline"),
			want:    "one mid\nline three",
			undos:   []string{"one two three"},
		},
		{
			name:    "bracketed wide paste replaces wide selection",
			content: "界界 tail",
			setup:   "<s-right><s-right>",
			wantSel: "界界",
			paste:   paste("宽"),
			want:    "宽 tail",
			undos:   []string{"界界 tail"},
		},
		{
			name:    "empty bracketed paste deletes selection",
			content: "one two",
			setup:   word,
			wantSel: "one",
			paste:   paste(""),
			want:    " two",
			undos:   []string{"one two"},
		},
		{
			name:    "empty bracketed paste without selection is a no-op",
			content: "one two",
			paste:   paste(""),
			want:    "one two",
		},
		{
			name:    "bracketed paste without selection inserts",
			content: "one two",
			paste:   paste("new "),
			want:    "new one two",
		},
		{
			name:    "clipboard paste replaces selection",
			content: "one two",
			clip:    "PASTED",
			setup:   word,
			wantSel: "one",
			input:   "<m-v>",
			want:    "PASTED two",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, buf, clip := newStandardKeymapHandler(t, tc.content, tc.at, tc.opts...)
			if tc.clip != "" {
				require.NoError(t, clip.Copy(clipboard.DefaultRegisterID,
					clipboard.Data{Text: tc.clip, Metadata: text.StandardSelection}))
			}
			if tc.setup != "" {
				feedKeys(t, h, tc.setup)
			}
			sel, selected := h.Selection()
			if tc.wantSel != "" {
				require.True(t, selected, "setup must select")
				require.Equal(t, tc.wantSel, sel)
			} else {
				require.False(t, selected, "setup must not report a selection")
			}

			if tc.paste != nil {
				h.Handle(term.Event{Type: term.EventPasteStart})
				for _, r := range *tc.paste {
					h.Handle(term.Event{Type: term.EventKey, Ch: r})
				}
				_, handled := h.Handle(term.Event{Type: term.EventPasteEnd})
				require.True(t, handled)
			} else {
				feedKeys(t, h, tc.input)
			}

			assert.Equal(t, tc.want, buf.String())
			_, selected = h.Selection()
			assert.False(t, selected, "no selection must survive the input")
			for i, undo := range tc.undos {
				feedKeys(t, h, "<m-z>")
				assert.Equal(t, undo, buf.String(), "undo step %d", i+1)
			}
		})
	}
}

func TestStandardKeymapLineInsert(t *testing.T) {
	t.Run("ctrl-enter inserts line below", func(t *testing.T) {
		h, buf, _ := newStandardKeymapHandler(t, "a\nb", term.Coordinates{})
		_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Key: term.KeyEnter})
		require.True(t, handled)
		assert.Equal(t, "a\n\nb", buf.String())
	})

	t.Run("ctrl-shift-enter inserts line above", func(t *testing.T) {
		h, buf, _ := newStandardKeymapHandler(t, "a\nb", term.Coordinates{})
		_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl | term.ModShift, Key: term.KeyEnter})
		require.True(t, handled)
		assert.Equal(t, "\na\nb", buf.String())
	})
}

func TestStandardKeymapConflate(t *testing.T) {
	h, buf, _ := newStandardKeymapHandler(t, "one\ntwo", term.Coordinates{})
	_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'J'})
	require.True(t, handled)
	assert.Equal(t, "onetwo", buf.String())
}

func TestStandardKeymapDropsEmacsBindings(t *testing.T) {
	t.Run("ctrl-e no longer moves to end of line", func(t *testing.T) {
		h, _, _ := newStandardKeymapHandler(t, "hello", term.Coordinates{})
		_, _ = h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'e'})
		assert.NotEqual(t, term.Coordinates{X: 5}, h.CursorAtScroll(),
			"ctrl-e must not behave as emacs move-end-line")
	})

	t.Run("ctrl-d no longer deletes a single char", func(t *testing.T) {
		h, buf, _ := newStandardKeymapHandler(t, "hello", term.Coordinates{})
		_, _ = h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'd'})
		assert.NotEqual(t, "ello", buf.String(),
			"ctrl-d must not behave as emacs delete-char")
	})

	t.Run("ctrl-b no longer moves left", func(t *testing.T) {
		h, _, _ := newStandardKeymapHandler(t, "hello", term.Coordinates{X: 3})
		_, _ = h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'b'})
		assert.Equal(t, term.Coordinates{X: 3}, h.CursorAtScroll(),
			"ctrl-b must not behave as emacs move-left")
	})
}

func TestStandardKeymapParagraphMotion(t *testing.T) {
	const content = "a\nb\n\nc\nd\n\ne"
	t.Run("ctrl-down moves to next paragraph", func(t *testing.T) {
		h, _, _ := newStandardKeymapHandler(t, content, term.Coordinates{})
		_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Key: term.KeyArrowDown})
		require.True(t, handled)
		assert.Equal(t, term.Coordinates{Y: 2}, h.CursorAtScroll())
	})

	t.Run("ctrl-up moves to previous paragraph", func(t *testing.T) {
		h, _, _ := newStandardKeymapHandler(t, content, term.Coordinates{Y: 6})
		_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Key: term.KeyArrowUp})
		require.True(t, handled)
		assert.Equal(t, term.Coordinates{Y: 5}, h.CursorAtScroll())
	})
}

func TestStandardKeymapParagraphSelection(t *testing.T) {
	const content = "a\nb\n\nc\nd\n\ne"
	t.Run("ctrl-shift-down selects to next paragraph", func(t *testing.T) {
		h, _, _ := newStandardKeymapHandler(t, content, term.Coordinates{})
		_, handled := h.Handle(term.Event{
			Type: term.EventKey, Mod: term.ModCtrl | term.ModShift, Key: term.KeyArrowDown,
		})
		require.True(t, handled)
		_, ok := h.Selection()
		assert.True(t, ok, "ctrl-shift-down must create a selection")
		assert.Equal(t, term.Coordinates{Y: 2}, h.CursorAtScroll())
	})

	t.Run("ctrl-shift-up selects to previous paragraph", func(t *testing.T) {
		h, _, _ := newStandardKeymapHandler(t, content, term.Coordinates{Y: 6})
		_, handled := h.Handle(term.Event{
			Type: term.EventKey, Mod: term.ModCtrl | term.ModShift, Key: term.KeyArrowUp,
		})
		require.True(t, handled)
		_, ok := h.Selection()
		assert.True(t, ok, "ctrl-shift-up must create a selection")
		assert.Equal(t, term.Coordinates{Y: 5}, h.CursorAtScroll())
	})
}

func TestStandardKeymapTranspose(t *testing.T) {
	h, buf, _ := newStandardKeymapHandler(t, "abcd", term.Coordinates{X: 2})
	_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 't'})
	require.True(t, handled)
	assert.Equal(t, "acbd", buf.String())
	assert.Equal(t, term.Coordinates{X: 3}, h.CursorAtScroll())
}

func TestStandardKeymapSelectionUndoRedo(t *testing.T) {
	h, _, _ := newStandardKeymapHandler(t, "hello world\nsecond line", term.Coordinates{})

	_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModMeta, Ch: 'l'})
	require.True(t, handled)
	_, ok := h.Selection()
	require.True(t, ok, "cmd-l must create a selection")

	_, handled = h.Handle(term.Event{Type: term.EventKey, Mod: term.ModMeta, Ch: 'u'})
	require.True(t, handled, "cmd-u must be handled")
	_, ok = h.Selection()
	assert.False(t, ok, "cmd-u must undo the selection")

	_, handled = h.Handle(term.Event{Type: term.EventKey, Mod: term.ModMeta, Ch: 'U'})
	require.True(t, handled, "cmd-shift-u must be handled")
	_, ok = h.Selection()
	assert.True(t, ok, "cmd-shift-u must redo the selection")
}

func TestStandardKeymapSelectPrevious(t *testing.T) {
	h, _, _ := newStandardKeymapHandler(t, "foo bar foo baz", term.Coordinates{X: 9})
	_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl | term.ModMeta, Ch: 'd'})
	require.True(t, handled)
	sel, ok := h.Selection()
	require.True(t, ok, "ctrl-cmd-d must create a selection")
	assert.Equal(t, "foo", sel)
	assert.Equal(t, term.Coordinates{X: 3}, h.CursorAtScroll())
}

func TestStandardKeymapToggleSoftWrap(t *testing.T) {
	uri, err := workspaceapi.ParseURI("test:///")
	require.NoError(t, err)
	buf := cell.NewBuffer()
	buf.ReadFrom(strings.NewReader("hello"))
	h := NewHandler(buf, uri, text.IndentRuneTab, 0, WithWrap(false))
	h.Resize(80, 20)

	sh, ok := h.(*standardHandler)
	require.True(t, ok)
	require.False(t, sh.less.Scroll().Wrap)

	_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModAlt, Ch: 'z'})
	require.True(t, handled)
	assert.True(t, sh.less.Scroll().Wrap, "alt-z must enable soft wrap")

	_, handled = h.Handle(term.Event{Type: term.EventKey, Mod: term.ModAlt, Ch: 'z'})
	require.True(t, handled)
	assert.False(t, sh.less.Scroll().Wrap, "alt-z must toggle soft wrap off")
}

func TestStandardKeymapDeleteToBeginningOfLine(t *testing.T) {
	h, buf, _ := newStandardKeymapHandler(t, "hello world", term.Coordinates{X: 6})
	_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModMeta, Key: term.KeyBackspace})
	require.True(t, handled)
	assert.Equal(t, "world", buf.String())
	assert.Equal(t, term.Coordinates{}, h.CursorAtScroll())
}

func TestStandardKeymapSyntaxNodeSelect(t *testing.T) {
	t.Run("ctrl-shift-right does not word-select", func(t *testing.T) {
		h, _, _ := newStandardKeymapHandler(t, "foo bar", term.Coordinates{})
		_, _ = h.Handle(term.Event{
			Type: term.EventKey, Mod: term.ModCtrl | term.ModShift, Key: term.KeyArrowRight,
		})
		assert.Equal(t, term.Coordinates{}, h.CursorAtScroll(),
			"ctrl-shift-right must not move by word")
		_, ok := h.Selection()
		assert.False(t, ok, "ctrl-shift-right must not create a word selection")
	})

	t.Run("ctrl-shift-left does not word-select", func(t *testing.T) {
		h, _, _ := newStandardKeymapHandler(t, "foo bar", term.Coordinates{X: 7})
		_, _ = h.Handle(term.Event{
			Type: term.EventKey, Mod: term.ModCtrl | term.ModShift, Key: term.KeyArrowLeft,
		})
		assert.Equal(t, term.Coordinates{X: 7}, h.CursorAtScroll(),
			"ctrl-shift-left must not move by word")
	})
}

func TestStandardKeymapWordSelect(t *testing.T) {
	t.Run("alt-shift-right selects to next word end", func(t *testing.T) {
		h, _, _ := newStandardKeymapHandler(t, "foo bar", term.Coordinates{})
		_, handled := h.Handle(term.Event{
			Type: term.EventKey, Mod: term.ModAlt | term.ModShift, Key: term.KeyArrowRight,
		})
		require.True(t, handled)
		sel, ok := h.Selection()
		require.True(t, ok, "alt-shift-right must create a selection")
		assert.Equal(t, "foo", sel)
		assert.Equal(t, term.Coordinates{X: 3}, h.CursorAtScroll())
	})

	t.Run("alt-shift-left selects to previous word start", func(t *testing.T) {
		h, _, _ := newStandardKeymapHandler(t, "foo bar", term.Coordinates{X: 7})
		_, handled := h.Handle(term.Event{
			Type: term.EventKey, Mod: term.ModAlt | term.ModShift, Key: term.KeyArrowLeft,
		})
		require.True(t, handled)
		sel, ok := h.Selection()
		require.True(t, ok, "alt-shift-left must create a selection")
		assert.Equal(t, "bar", sel)
		assert.Equal(t, term.Coordinates{X: 4}, h.CursorAtScroll())
	})

	t.Run("alt-shift-f selects to next word end", func(t *testing.T) {
		h, _, _ := newStandardKeymapHandler(t, "foo bar", term.Coordinates{})
		_, handled := h.Handle(term.Event{
			Type: term.EventKey, Mod: term.ModAlt | term.ModShift, Ch: 'F',
		})
		require.True(t, handled)
		sel, ok := h.Selection()
		require.True(t, ok, "alt-shift-f must create a selection")
		assert.Equal(t, "foo", sel)
	})

	t.Run("alt-shift-b selects to previous word start", func(t *testing.T) {
		h, _, _ := newStandardKeymapHandler(t, "foo bar", term.Coordinates{X: 7})
		_, handled := h.Handle(term.Event{
			Type: term.EventKey, Mod: term.ModAlt | term.ModShift, Ch: 'B',
		})
		require.True(t, handled)
		sel, ok := h.Selection()
		require.True(t, ok, "alt-shift-b must create a selection")
		assert.Equal(t, "bar", sel)
	})
}

func TestStandardKeymapLineDocSelect(t *testing.T) {
	t.Run("cmd-shift-right selects to end of line", func(t *testing.T) {
		h, _, _ := newStandardKeymapHandler(t, "hello world\nsecond", term.Coordinates{})
		_, handled := h.Handle(term.Event{
			Type: term.EventKey, Mod: term.ModShift | term.ModMeta, Key: term.KeyArrowRight,
		})
		require.True(t, handled)
		sel, ok := h.Selection()
		require.True(t, ok, "cmd-shift-right must create a selection")
		assert.Equal(t, "hello world", sel)
	})

	t.Run("cmd-shift-left selects to start of indented line", func(t *testing.T) {
		h, _, _ := newStandardKeymapHandler(t, "    hello world", term.Coordinates{X: 15})
		_, handled := h.Handle(term.Event{
			Type: term.EventKey, Mod: term.ModShift | term.ModMeta, Key: term.KeyArrowLeft,
		})
		require.True(t, handled)
		sel, ok := h.Selection()
		require.True(t, ok, "cmd-shift-left must create a selection")
		assert.Equal(t, "    hello world", sel)
		assert.Equal(t, term.Coordinates{}, h.CursorAtScroll())
	})

	t.Run("cmd-shift-down selects to end of document", func(t *testing.T) {
		h, _, _ := newStandardKeymapHandler(t, "a\nb\nc", term.Coordinates{})
		_, handled := h.Handle(term.Event{
			Type: term.EventKey, Mod: term.ModShift | term.ModMeta, Key: term.KeyArrowDown,
		})
		require.True(t, handled)
		_, ok := h.Selection()
		require.True(t, ok, "cmd-shift-down must create a selection")
		assert.Equal(t, term.Coordinates{Y: 2}, h.CursorAtScroll())
	})

	t.Run("cmd-shift-up selects to start of document", func(t *testing.T) {
		h, _, _ := newStandardKeymapHandler(t, "a\nb\nc", term.Coordinates{Y: 2})
		_, handled := h.Handle(term.Event{
			Type: term.EventKey, Mod: term.ModShift | term.ModMeta, Key: term.KeyArrowUp,
		})
		require.True(t, handled)
		_, ok := h.Selection()
		require.True(t, ok, "cmd-shift-up must create a selection")
		assert.Equal(t, term.Coordinates{}, h.CursorAtScroll())
	})

	t.Run("shift-up selects first line to start", func(t *testing.T) {
		h, _, _ := newStandardKeymapHandler(t, "first\nmiddle\nlast", term.Coordinates{X: 5})
		_, handled := h.Handle(term.Event{
			Type: term.EventKey, Mod: term.ModShift, Key: term.KeyArrowUp,
		})
		require.True(t, handled)
		sel, ok := h.Selection()
		require.True(t, ok, "shift-up must create a selection")
		assert.Equal(t, "first", sel)
		assert.Equal(t, term.Coordinates{}, h.CursorAtScroll())
	})

	t.Run("shift-down selects last line to end", func(t *testing.T) {
		h, _, _ := newStandardKeymapHandler(t, "first\nmiddle\nlast", term.Coordinates{Y: 2})
		_, handled := h.Handle(term.Event{
			Type: term.EventKey, Mod: term.ModShift, Key: term.KeyArrowDown,
		})
		require.True(t, handled)
		sel, ok := h.Selection()
		require.True(t, ok, "shift-down must create a selection")
		assert.Equal(t, "last", sel)
		assert.Equal(t, term.Coordinates{X: 4, Y: 2}, h.CursorAtScroll())
	})

	t.Run("shift-end selects to end of line", func(t *testing.T) {
		h, _, _ := newStandardKeymapHandler(t, "hello world", term.Coordinates{})
		_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModShift, Key: term.KeyEnd})
		require.True(t, handled)
		sel, ok := h.Selection()
		require.True(t, ok, "shift-end must create a selection")
		assert.Equal(t, "hello world", sel)
	})

	t.Run("shift-home selects to start of line", func(t *testing.T) {
		h, _, _ := newStandardKeymapHandler(t, "hello world", term.Coordinates{X: 11})
		_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModShift, Key: term.KeyHome})
		require.True(t, handled)
		_, ok := h.Selection()
		assert.True(t, ok, "shift-home must create a selection")
		assert.Equal(t, term.Coordinates{}, h.CursorAtScroll())
	})
}

func TestStandardKeymapCutToEndOfLine(t *testing.T) {
	t.Run("ctrl-k cuts to end of line", func(t *testing.T) {
		h, buf, clip := newStandardKeymapHandler(t, "hello world", term.Coordinates{X: 6})
		_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'k'})
		require.True(t, handled)
		assert.Equal(t, "hello ", buf.String())
		data, err := clip.Paste(clipboard.DefaultRegisterID)
		require.NoError(t, err)
		assert.Contains(t, data.Text, "world")
	})

	t.Run("ctrl-k at end of line joins next line", func(t *testing.T) {
		h, buf, _ := newStandardKeymapHandler(t, "hello\nworld", term.Coordinates{X: 5})
		_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'k'})
		require.True(t, handled)
		assert.Equal(t, "helloworld", buf.String())
	})
}

func TestStandardKeymapDeleteToEndOfLine(t *testing.T) {
	h, buf, _ := newStandardKeymapHandler(t, "hello world", term.Coordinates{X: 5})
	_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModMeta, Key: term.KeyDelete})
	require.True(t, handled)
	assert.Equal(t, "hello", buf.String())
}

func TestStandardKeymapLinuxLeavesMetaToRune(t *testing.T) {
	for _, keys := range []string{
		"<meta-a>", "<meta-c>", "<meta-v>", "<meta-x>", "<meta-z>",
		"<meta-l>", "<meta-d>", "<meta-j>", "<meta-k>", "<meta-/>",
		"<meta-]>", "<meta-u>", "<shift-meta-d>", "<shift-meta-z>",
		"<meta-left>", "<meta-right>", "<meta-up>", "<meta-down>",
		"<meta-backspace>", "<meta-delete>", "<shift-meta-left>",
		"<shift-meta-space>", "<ctrl-meta-d>", "<alt-meta-/>", "<alt-meta-v>",
	} {
		t.Run(keys, func(t *testing.T) {
			h, buf, _ := newStandardKeymapHandler(t, "one two\nthree", term.Coordinates{X: 4},
				WithKeymap(KeymapLinux))
			seq, err := term.ParseKeys(keys)
			require.NoError(t, err)
			require.Len(t, seq, 1)
			_, handled := h.Handle(term.Event{
				Type: term.EventKey, Key: seq[0].Key, Mod: seq[0].Mod, Ch: seq[0].Ch,
			})
			assert.False(t, handled, "%s belongs to Rune on Linux", keys)
			assert.Equal(t, "one two\nthree", buf.String())
			assert.Equal(t, term.Coordinates{X: 4}, h.CursorAtScroll())
			_, selected := h.Selection()
			assert.False(t, selected)
		})
	}
}

func TestStandardKeymapLinuxCtrlLayer(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		at      term.Coordinates
		input   string
		want    string
		wantSel string
		wantAt  *term.Coordinates
	}{
		{
			name:    "ctrl-l selects the line instead of recentering",
			content: "one\ntwo",
			input:   "<ctrl-l>",
			want:    "one\ntwo",
			wantSel: "one\n",
		},
		{
			name:    "ctrl-k waits for a chord instead of cutting",
			content: "hello world",
			at:      term.Coordinates{X: 6},
			input:   "<ctrl-k>",
			want:    "hello world",
		},
		{
			name:    "ctrl-alt-d selects the previous occurrence",
			content: "foo bar foo baz",
			at:      term.Coordinates{X: 9},
			input:   "<ctrl-alt-d>",
			want:    "foo bar foo baz",
			wantSel: "foo",
			wantAt:  &term.Coordinates{X: 3},
		},
		{
			name:    "ctrl-u undoes the selection",
			content: "one\ntwo",
			input:   "<ctrl-l><ctrl-u>",
			want:    "one\ntwo",
		},
		{
			name:    "ctrl-shift-u redoes the selection",
			content: "one\ntwo",
			input:   "<ctrl-l><ctrl-u><ctrl-shift-u>",
			want:    "one\ntwo",
			wantSel: "one\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, buf, _ := newStandardKeymapHandler(t, tc.content, tc.at, WithKeymap(KeymapLinux))
			feedKeys(t, h, tc.input)
			assert.Equal(t, tc.want, buf.String())
			sel, selected := h.Selection()
			if tc.wantSel == "" {
				assert.False(t, selected, "no selection expected, got %q", sel)
			} else {
				require.True(t, selected)
				assert.Equal(t, tc.wantSel, sel)
			}
			if tc.wantAt != nil {
				assert.Equal(t, *tc.wantAt, h.CursorAtScroll())
			}
		})
	}
}

func TestStandardKeymapLineAndWordEditsMatchVSCode(t *testing.T) {
	// The expectations come from VS Code, whose line and word commands the
	// standard keymap follows: monaco's word operations for the word motions,
	// and its cut, copy, paste, deleteLines, moveLines, copyLines and
	// expandLineSelection commands for the lines.
	deleteLine := term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'K'}
	cases := []struct {
		name     string
		content  string
		at       term.Coordinates
		clip     string
		keys     string
		then     []term.Event
		want     string
		wantAt   term.Coordinates
		wantClip string
		wantSel  string
	}{
		{name: "cut a middle line", content: "ab\ncd\nef", at: term.Coordinates{X: 1, Y: 1}, keys: "<meta-x>", want: "ab\nef", wantAt: term.Coordinates{Y: 1}, wantClip: "cd\n"},
		{name: "cut the first line", content: "ab\ncd\nef", keys: "<meta-x>", want: "cd\nef", wantAt: term.Coordinates{}, wantClip: "ab\n"},
		{name: "cut the last line", content: "ab\ncd\nef", at: term.Coordinates{X: 1, Y: 2}, keys: "<meta-x>", want: "ab\ncd", wantAt: term.Coordinates{X: 2, Y: 1}, wantClip: "ef\n"},
		{name: "cut the trailing empty line", content: "ab\ncd\n", at: term.Coordinates{Y: 2}, keys: "<meta-x>", want: "ab\ncd", wantAt: term.Coordinates{X: 2, Y: 1}, wantClip: "\n"},
		{name: "cut the line before the trailing empty line", content: "ab\ncd\n", at: term.Coordinates{Y: 1}, keys: "<meta-x>", want: "ab\n", wantAt: term.Coordinates{Y: 1}, wantClip: "cd\n"},
		{name: "cut the only line", content: "ab", at: term.Coordinates{X: 1}, keys: "<meta-x>", want: "", wantAt: term.Coordinates{}, wantClip: "ab\n"},
		{name: "cut on an empty buffer", content: "", keys: "<meta-x>", want: "", wantAt: term.Coordinates{}, wantClip: "\n"},
		{name: "cut a double-width last line", content: "ab\n日本", at: term.Coordinates{X: 1, Y: 1}, keys: "<meta-x>", want: "ab", wantAt: term.Coordinates{X: 2}, wantClip: "日本\n"},
		{name: "cut an emoji line", content: "\U0001F600\ncd", keys: "<meta-x>", want: "cd", wantAt: term.Coordinates{}, wantClip: "\U0001F600\n"},
		{name: "cut a tab-indented last line", content: "ab\n\tcd", at: term.Coordinates{X: 2, Y: 1}, keys: "<meta-x>", want: "ab", wantAt: term.Coordinates{X: 2}, wantClip: "\tcd\n"},
		{name: "cut past the line end", content: "ab\ncd", at: term.Coordinates{X: 9, Y: 1}, keys: "<meta-x>", want: "ab", wantAt: term.Coordinates{X: 2}, wantClip: "cd\n"},
		{name: "cut below the last line", content: "ab\ncd", at: term.Coordinates{Y: 9}, keys: "<meta-x>", want: "ab", wantAt: term.Coordinates{X: 2}, wantClip: "cd\n"},
		{name: "delete line a middle line", content: "ab\ncd\nef", at: term.Coordinates{X: 1, Y: 1}, then: []term.Event{deleteLine}, want: "ab\nef", wantAt: term.Coordinates{X: 1, Y: 1}},
		{name: "delete line the first line", content: "ab\ncd\nef", then: []term.Event{deleteLine}, want: "cd\nef", wantAt: term.Coordinates{}},
		{name: "delete line the last line", content: "ab\ncd\nef", at: term.Coordinates{X: 1, Y: 2}, then: []term.Event{deleteLine}, want: "ab\ncd", wantAt: term.Coordinates{X: 1, Y: 1}},
		{name: "delete line the trailing empty line", content: "ab\ncd\n", at: term.Coordinates{Y: 2}, then: []term.Event{deleteLine}, want: "ab\ncd", wantAt: term.Coordinates{Y: 1}},
		{name: "delete line the line before the trailing empty line", content: "ab\ncd\n", at: term.Coordinates{Y: 1}, then: []term.Event{deleteLine}, want: "ab\n", wantAt: term.Coordinates{Y: 1}},
		{name: "delete line the only line", content: "ab", at: term.Coordinates{X: 1}, then: []term.Event{deleteLine}, want: "", wantAt: term.Coordinates{}},
		{name: "delete line on an empty buffer", content: "", then: []term.Event{deleteLine}, want: "", wantAt: term.Coordinates{}},
		{name: "delete line a double-width last line", content: "ab\n日本", at: term.Coordinates{X: 1, Y: 1}, then: []term.Event{deleteLine}, want: "ab", wantAt: term.Coordinates{X: 1}},
		{name: "delete line an emoji line", content: "\U0001F600\ncd", then: []term.Event{deleteLine}, want: "cd", wantAt: term.Coordinates{}},
		{name: "delete line a tab-indented last line", content: "ab\n\tcd", at: term.Coordinates{X: 2, Y: 1}, then: []term.Event{deleteLine}, want: "ab", wantAt: term.Coordinates{X: 2}},
		{name: "delete line past the line end", content: "ab\ncd", at: term.Coordinates{X: 9, Y: 1}, then: []term.Event{deleteLine}, want: "ab", wantAt: term.Coordinates{X: 2}},
		{name: "delete line below the last line", content: "ab\ncd", at: term.Coordinates{Y: 9}, then: []term.Event{deleteLine}, want: "ab", wantAt: term.Coordinates{}},
		{name: "select the last two lines then backspace", content: "ab\ncd\nef", at: term.Coordinates{Y: 1}, keys: "<meta-l><meta-l><backspace>", want: "ab\n", wantAt: term.Coordinates{Y: 1}},
		{name: "select the last line then backspace", content: "ab\ncd\nef", at: term.Coordinates{Y: 2}, keys: "<meta-l><backspace>", want: "ab\ncd\n", wantAt: term.Coordinates{Y: 2}},
		{name: "select two middle lines then backspace", content: "ab\ncd\nef\ngh", at: term.Coordinates{Y: 1}, keys: "<meta-l><meta-l><backspace>", want: "ab\ngh", wantAt: term.Coordinates{Y: 1}},
		{name: "select the trailing empty line then backspace", content: "ab\ncd\n", at: term.Coordinates{Y: 2}, keys: "<meta-l><backspace>", want: "ab\ncd", wantAt: term.Coordinates{X: 2, Y: 1}},
		{name: "select two lines over the trailing empty line then backspace", content: "ab\ncd\n", at: term.Coordinates{Y: 1}, keys: "<meta-l><meta-l><backspace>", want: "ab\n", wantAt: term.Coordinates{Y: 1}},
		{name: "select all then backspace", content: "ab\ncd\nef", at: term.Coordinates{Y: 1}, keys: "<meta-a><backspace>", want: "", wantAt: term.Coordinates{}},
		{name: "select the last two lines then delete", content: "ab\ncd\nef", at: term.Coordinates{Y: 1}, keys: "<meta-l><meta-l><delete>", want: "ab\n", wantAt: term.Coordinates{Y: 1}},
		{name: "select the last line then delete", content: "ab\ncd\nef", at: term.Coordinates{Y: 2}, keys: "<meta-l><delete>", want: "ab\ncd\n", wantAt: term.Coordinates{Y: 2}},
		{name: "select two middle lines then delete", content: "ab\ncd\nef\ngh", at: term.Coordinates{Y: 1}, keys: "<meta-l><meta-l><delete>", want: "ab\ngh", wantAt: term.Coordinates{Y: 1}},
		{name: "select the trailing empty line then delete", content: "ab\ncd\n", at: term.Coordinates{Y: 2}, keys: "<meta-l><delete>", want: "ab\ncd\n", wantAt: term.Coordinates{Y: 2}},
		{name: "select two lines over the trailing empty line then delete", content: "ab\ncd\n", at: term.Coordinates{Y: 1}, keys: "<meta-l><meta-l><delete>", want: "ab\n", wantAt: term.Coordinates{Y: 1}},
		{name: "select all then delete", content: "ab\ncd\nef", at: term.Coordinates{Y: 1}, keys: "<meta-a><delete>", want: "", wantAt: term.Coordinates{}},
		{name: "select the last two lines then cut", content: "ab\ncd\nef", at: term.Coordinates{Y: 1}, keys: "<meta-l><meta-l><meta-x>", want: "ab\n", wantAt: term.Coordinates{Y: 1}, wantClip: "cd\nef"},
		{name: "select the last line then cut", content: "ab\ncd\nef", at: term.Coordinates{Y: 2}, keys: "<meta-l><meta-x>", want: "ab\ncd\n", wantAt: term.Coordinates{Y: 2}, wantClip: "ef"},
		{name: "select two middle lines then cut", content: "ab\ncd\nef\ngh", at: term.Coordinates{Y: 1}, keys: "<meta-l><meta-l><meta-x>", want: "ab\ngh", wantAt: term.Coordinates{Y: 1}, wantClip: "cd\nef\n"},
		{name: "select the trailing empty line then cut", content: "ab\ncd\n", at: term.Coordinates{Y: 2}, keys: "<meta-l><meta-x>", want: "ab\ncd", wantAt: term.Coordinates{X: 2, Y: 1}, wantClip: "\n"},
		{name: "select two lines over the trailing empty line then cut", content: "ab\ncd\n", at: term.Coordinates{Y: 1}, keys: "<meta-l><meta-l><meta-x>", want: "ab\n", wantAt: term.Coordinates{Y: 1}, wantClip: "cd\n"},
		{name: "select all then cut", content: "ab\ncd\nef", at: term.Coordinates{Y: 1}, keys: "<meta-a><meta-x>", want: "", wantAt: term.Coordinates{}, wantClip: "ab\ncd\nef"},
		{name: "select the last two lines then typing", content: "ab\ncd\nef", at: term.Coordinates{Y: 1}, keys: "<meta-l><meta-l>z", want: "ab\nz", wantAt: term.Coordinates{X: 1, Y: 1}},
		{name: "select the last line then typing", content: "ab\ncd\nef", at: term.Coordinates{Y: 2}, keys: "<meta-l>z", want: "ab\ncd\nz", wantAt: term.Coordinates{X: 1, Y: 2}},
		{name: "select two middle lines then typing", content: "ab\ncd\nef\ngh", at: term.Coordinates{Y: 1}, keys: "<meta-l><meta-l>z", want: "ab\nzgh", wantAt: term.Coordinates{X: 1, Y: 1}},
		{name: "select the trailing empty line then typing", content: "ab\ncd\n", at: term.Coordinates{Y: 2}, keys: "<meta-l>z", want: "ab\ncd\nz", wantAt: term.Coordinates{X: 1, Y: 2}},
		{name: "select two lines over the trailing empty line then typing", content: "ab\ncd\n", at: term.Coordinates{Y: 1}, keys: "<meta-l><meta-l>z", want: "ab\nz", wantAt: term.Coordinates{X: 1, Y: 1}},
		{name: "select all then typing", content: "ab\ncd\nef", at: term.Coordinates{Y: 1}, keys: "<meta-a>z", want: "z", wantAt: term.Coordinates{X: 1}},
		{name: "select the last two lines then enter", content: "ab\ncd\nef", at: term.Coordinates{Y: 1}, keys: "<meta-l><meta-l><enter>", want: "ab\n\n", wantAt: term.Coordinates{Y: 2}},
		{name: "select the last line then enter", content: "ab\ncd\nef", at: term.Coordinates{Y: 2}, keys: "<meta-l><enter>", want: "ab\ncd\n\n", wantAt: term.Coordinates{Y: 3}},
		{name: "select two middle lines then enter", content: "ab\ncd\nef\ngh", at: term.Coordinates{Y: 1}, keys: "<meta-l><meta-l><enter>", want: "ab\n\ngh", wantAt: term.Coordinates{Y: 2}},
		{name: "select the trailing empty line then enter", content: "ab\ncd\n", at: term.Coordinates{Y: 2}, keys: "<meta-l><enter>", want: "ab\ncd\n\n", wantAt: term.Coordinates{Y: 3}},
		{name: "select two lines over the trailing empty line then enter", content: "ab\ncd\n", at: term.Coordinates{Y: 1}, keys: "<meta-l><meta-l><enter>", want: "ab\n\n", wantAt: term.Coordinates{Y: 2}},
		{name: "select all then enter", content: "ab\ncd\nef", at: term.Coordinates{Y: 1}, keys: "<meta-a><enter>", want: "\n", wantAt: term.Coordinates{Y: 1}},
		{name: "select the last two lines then paste a line", content: "ab\ncd\nef", at: term.Coordinates{Y: 1}, clip: "zz\n", keys: "<meta-l><meta-l><meta-v>", want: "ab\nzz\n", wantAt: term.Coordinates{Y: 2}, wantClip: "zz\n"},
		{name: "select the last line then paste a line", content: "ab\ncd\nef", at: term.Coordinates{Y: 2}, clip: "zz\n", keys: "<meta-l><meta-v>", want: "ab\ncd\nzz\n", wantAt: term.Coordinates{Y: 3}, wantClip: "zz\n"},
		{name: "select a double-width last line pair then backspace", content: "ab\n日本\n\U0001F600", at: term.Coordinates{Y: 1}, keys: "<meta-l><meta-l><backspace>", want: "ab\n", wantAt: term.Coordinates{Y: 1}},
		{name: "select tab lines to the end then backspace", content: "ab\n\tcd\n\tef", at: term.Coordinates{Y: 1}, keys: "<meta-l><meta-l><backspace>", want: "ab\n", wantAt: term.Coordinates{Y: 1}},
		{name: "move the last line up", content: "ab\ncd\nef", at: term.Coordinates{X: 1, Y: 2}, keys: "<alt-up>", want: "ab\nef\ncd", wantAt: term.Coordinates{X: 1, Y: 1}},
		{name: "move the second to last line down", content: "ab\ncd\nef", at: term.Coordinates{X: 1, Y: 1}, keys: "<alt-down>", want: "ab\nef\ncd", wantAt: term.Coordinates{X: 1, Y: 2}},
		{name: "move the last line down", content: "ab\ncd\nef", at: term.Coordinates{Y: 2}, keys: "<alt-down>", want: "ab\ncd\nef", wantAt: term.Coordinates{Y: 2}},
		{name: "move the first line up", content: "ab\ncd\nef", keys: "<alt-up>", want: "ab\ncd\nef", wantAt: term.Coordinates{}},
		{name: "move the last two lines up", content: "ab\ncd\nef", at: term.Coordinates{Y: 1}, keys: "<meta-l><meta-l><alt-up>", want: "cd\nef\nab", wantAt: term.Coordinates{X: 2, Y: 1}, wantSel: "cd\nef"},
		{name: "move the trailing empty line up", content: "ab\ncd\n", at: term.Coordinates{Y: 2}, keys: "<alt-up>", want: "ab\n\ncd", wantAt: term.Coordinates{Y: 1}},
		{name: "move the line before the trailing empty line down", content: "ab\ncd\n", at: term.Coordinates{Y: 1}, keys: "<alt-down>", want: "ab\n\ncd", wantAt: term.Coordinates{Y: 2}},
		{name: "move a double-width last line up", content: "ab\n日本", at: term.Coordinates{X: 1, Y: 1}, keys: "<alt-up>", want: "日本\nab", wantAt: term.Coordinates{X: 1}},
		{name: "move a tab-indented last line up", content: "ab\n\tcd", at: term.Coordinates{X: 1, Y: 1}, keys: "<alt-up>", want: "\tcd\nab", wantAt: term.Coordinates{X: 1}},
		{name: "move a line up past the line end", content: "ab\ncd", at: term.Coordinates{X: 9, Y: 1}, keys: "<alt-up>", want: "cd\nab", wantAt: term.Coordinates{X: 2}},
		{name: "move a line up on an empty buffer", content: "", keys: "<alt-up>", want: "", wantAt: term.Coordinates{}},
		{name: "duplicate the last line down", content: "ab\ncd\nef", at: term.Coordinates{Y: 2}, keys: "<alt-shift-down>", want: "ab\ncd\nef\nef", wantAt: term.Coordinates{Y: 3}},
		{name: "duplicate the last line up", content: "ab\ncd\nef", at: term.Coordinates{Y: 2}, keys: "<alt-shift-up>", want: "ab\ncd\nef\nef", wantAt: term.Coordinates{Y: 2}},
		{name: "alt-right from ascii to an emoji", content: "ab\U0001F600cd", keys: "<alt-right>", want: "ab\U0001F600cd", wantAt: term.Coordinates{X: 5}},
		{name: "alt-right from an emoji", content: "\U0001F600ab cd", keys: "<alt-right>", want: "\U0001F600ab cd", wantAt: term.Coordinates{X: 3}},
		{name: "alt-right from cjk", content: "日本ab cd", keys: "<alt-right>", want: "日本ab cd", wantAt: term.Coordinates{X: 4}},
		{name: "alt-right from ascii to cjk", content: "ab日本 cd", keys: "<alt-right>", want: "ab日本 cd", wantAt: term.Coordinates{X: 4}},
		{name: "alt-right over a tab", content: "ab\tcd", at: term.Coordinates{X: 2}, keys: "<alt-right>", want: "ab\tcd", wantAt: term.Coordinates{X: 5}},
		{name: "alt-right at the line end", content: "foo\nbar", at: term.Coordinates{X: 3}, keys: "<alt-right>", want: "foo\nbar", wantAt: term.Coordinates{X: 3, Y: 1}},
		{name: "alt-right over an empty line", content: "ab\n\ncd", at: term.Coordinates{X: 2}, keys: "<alt-right>", want: "ab\n\ncd", wantAt: term.Coordinates{Y: 1}},
		{name: "alt-right past the line end", content: "ab cd\nef", at: term.Coordinates{X: 9}, keys: "<alt-right>", want: "ab cd\nef", wantAt: term.Coordinates{X: 2, Y: 1}},
		{name: "alt-right below the last line", content: "ab\ncd", at: term.Coordinates{Y: 9}, keys: "<alt-right>", want: "ab\ncd", wantAt: term.Coordinates{X: 2, Y: 1}},
		{name: "alt-right on an empty buffer", content: "", keys: "<alt-right>", want: "", wantAt: term.Coordinates{}},
		{name: "alt-right on the trailing empty line", content: "ab\n", at: term.Coordinates{Y: 1}, keys: "<alt-right>", want: "ab\n", wantAt: term.Coordinates{Y: 1}},
		{name: "alt-left from ascii to an emoji", content: "ab\U0001F600cd", at: term.Coordinates{X: 5}, keys: "<alt-left>", want: "ab\U0001F600cd", wantAt: term.Coordinates{}},
		{name: "alt-left after an emoji", content: "ab \U0001F600", at: term.Coordinates{X: 4}, keys: "<alt-left>", want: "ab \U0001F600", wantAt: term.Coordinates{X: 3}},
		{name: "alt-left after cjk", content: "ab 日本", at: term.Coordinates{X: 5}, keys: "<alt-left>", want: "ab 日本", wantAt: term.Coordinates{X: 3}},
		{name: "alt-left from ascii after cjk", content: "日本ab", at: term.Coordinates{X: 4}, keys: "<alt-left>", want: "日本ab", wantAt: term.Coordinates{}},
		{name: "alt-left over a tab", content: "ab\tcd", at: term.Coordinates{X: 3}, keys: "<alt-left>", want: "ab\tcd", wantAt: term.Coordinates{}},
		{name: "alt-left at the line start", content: "foo bar\nbaz", at: term.Coordinates{Y: 1}, keys: "<alt-left>", want: "foo bar\nbaz", wantAt: term.Coordinates{X: 4}},
		{name: "alt-left over an empty line", content: "ab\n\ncd", at: term.Coordinates{Y: 2}, keys: "<alt-left>", want: "ab\n\ncd", wantAt: term.Coordinates{Y: 1}},
		{name: "alt-left over two empty lines", content: "ab\n\n\ncd", at: term.Coordinates{Y: 3}, keys: "<alt-left>", want: "ab\n\n\ncd", wantAt: term.Coordinates{Y: 2}},
		{name: "alt-left past the line end", content: "ab cd", at: term.Coordinates{X: 9}, keys: "<alt-left>", want: "ab cd", wantAt: term.Coordinates{X: 3}},
		{name: "alt-left below the last line", content: "ab\ncd", at: term.Coordinates{Y: 9}, keys: "<alt-left>", want: "ab\ncd", wantAt: term.Coordinates{}},
		{name: "alt-left on an empty buffer", content: "", keys: "<alt-left>", want: "", wantAt: term.Coordinates{}},
		{name: "alt-left on the trailing empty line", content: "ab cd\n", at: term.Coordinates{Y: 1}, keys: "<alt-left>", want: "ab cd\n", wantAt: term.Coordinates{X: 3}},
		{name: "alt-backspace after an emoji word", content: "ab\U0001F600cd", at: term.Coordinates{X: 5}, keys: "<alt-backspace>", want: "", wantAt: term.Coordinates{}},
		{name: "alt-backspace after cjk", content: "ab 日本", at: term.Coordinates{X: 5}, keys: "<alt-backspace>", want: "ab ", wantAt: term.Coordinates{X: 3}},
		{name: "alt-backspace at the line start", content: "ab cd\nef", at: term.Coordinates{Y: 1}, keys: "<alt-backspace>", want: "ab cdef", wantAt: term.Coordinates{X: 5}},
		{name: "alt-backspace over an empty line", content: "ab\n\ncd", at: term.Coordinates{Y: 2}, keys: "<alt-backspace>", want: "ab\ncd", wantAt: term.Coordinates{Y: 1}},
		{name: "alt-backspace over a tab", content: "ab\tcd", at: term.Coordinates{X: 3}, keys: "<alt-backspace>", want: "cd", wantAt: term.Coordinates{}},
		{name: "alt-delete before an emoji", content: "ab\U0001F600cd", keys: "<alt-delete>", want: "", wantAt: term.Coordinates{}},
		{name: "alt-delete on cjk", content: "日本 ab", keys: "<alt-delete>", want: " ab", wantAt: term.Coordinates{}},
		{name: "alt-delete at the line end", content: "ab\ncd", at: term.Coordinates{X: 2}, keys: "<alt-delete>", want: "abcd", wantAt: term.Coordinates{X: 2}},
		{name: "alt-delete over an empty line", content: "ab\n\ncd", at: term.Coordinates{X: 2}, keys: "<alt-delete>", want: "ab\ncd", wantAt: term.Coordinates{X: 2}},
		{name: "alt-delete over a tab", content: "ab\tcd", keys: "<alt-delete>", want: "\tcd", wantAt: term.Coordinates{}},
		{name: "alt-shift-right over an emoji", content: "ab\U0001F600cd", keys: "<alt-shift-right>", want: "ab\U0001F600cd", wantAt: term.Coordinates{X: 5}, wantSel: "ab\U0001F600cd"},
		{name: "alt-shift-left over cjk", content: "ab 日本", at: term.Coordinates{X: 5}, keys: "<alt-shift-left>", want: "ab 日本", wantAt: term.Coordinates{X: 3}, wantSel: "日本"},
		{name: "cmd-d selects a word next to an emoji", content: "foo\U0001F600bar", at: term.Coordinates{X: 1}, keys: "<meta-d>", want: "foo\U0001F600bar", wantAt: term.Coordinates{X: 7}, wantSel: "foo\U0001F600bar"},
		{name: "cmd-d selects cjk", content: "ab 日本 cd", at: term.Coordinates{X: 3}, keys: "<meta-d>", want: "ab 日本 cd", wantAt: term.Coordinates{X: 5}, wantSel: "日本"},
		{name: "cut a tab-indented middle line", content: "\tab\ncd", at: term.Coordinates{X: 1}, keys: "<meta-x>", want: "cd", wantAt: term.Coordinates{}, wantClip: "\tab\n"},
		{name: "cut a double-width middle line", content: "日本\ncd", at: term.Coordinates{X: 1}, keys: "<meta-x>", want: "cd", wantAt: term.Coordinates{}, wantClip: "日本\n"},
		{name: "cut past the end of a middle line", content: "ab\ncd", at: term.Coordinates{X: 9}, keys: "<meta-x>", want: "cd", wantAt: term.Coordinates{}, wantClip: "ab\n"},
		{name: "cut an empty middle line", content: "ab\n\ncd", at: term.Coordinates{Y: 1}, keys: "<meta-x>", want: "ab\ncd", wantAt: term.Coordinates{Y: 1}, wantClip: "\n"},
		{name: "delete line a tab-indented middle line", content: "\tab\ncd", at: term.Coordinates{X: 1}, then: []term.Event{deleteLine}, want: "cd", wantAt: term.Coordinates{X: 1}},
		{name: "delete line a double-width middle line", content: "日本\ncd", at: term.Coordinates{X: 1}, then: []term.Event{deleteLine}, want: "cd", wantAt: term.Coordinates{X: 1}},
		{name: "delete line past the end of a middle line", content: "ab\ncd", at: term.Coordinates{X: 9}, then: []term.Event{deleteLine}, want: "cd", wantAt: term.Coordinates{X: 2}},
		{name: "delete line an empty middle line", content: "ab\n\ncd", at: term.Coordinates{Y: 1}, then: []term.Event{deleteLine}, want: "ab\ncd", wantAt: term.Coordinates{Y: 1}},
		{name: "select a double-width middle line then backspace", content: "日本\ncd", at: term.Coordinates{X: 1}, keys: "<meta-l><backspace>", want: "cd", wantAt: term.Coordinates{}},
		{name: "select a tab-indented middle line then backspace", content: "\tab\ncd", at: term.Coordinates{X: 2}, keys: "<meta-l><backspace>", want: "cd", wantAt: term.Coordinates{}},
		{name: "select a line past its end then backspace", content: "ab\ncd\nef", at: term.Coordinates{X: 9}, keys: "<meta-l><backspace>", want: "cd\nef", wantAt: term.Coordinates{}},
		{name: "alt-right over a double-width word", content: "ab 日本 cd", at: term.Coordinates{X: 2}, keys: "<alt-right>", want: "ab 日本 cd", wantAt: term.Coordinates{X: 5}},
		{name: "alt-left over a tab-indented word", content: "\tab", at: term.Coordinates{X: 3}, keys: "<alt-left>", want: "\tab", wantAt: term.Coordinates{X: 1}},
		{name: "copy a middle line then paste it on the next line", content: "ab\ncd\nef", at: term.Coordinates{X: 1, Y: 1}, keys: "<meta-c><down><meta-v>", want: "ab\ncd\ncd\nef", wantAt: term.Coordinates{X: 1, Y: 3}, wantClip: "cd\n"},
		{name: "copy an empty line", content: "ab\n\ncd", at: term.Coordinates{Y: 1}, keys: "<meta-c>", want: "ab\n\ncd", wantAt: term.Coordinates{Y: 1}, wantClip: "\n"},
		{name: "copy the last line", content: "ab\ncd", at: term.Coordinates{X: 1, Y: 1}, keys: "<meta-c>", want: "ab\ncd", wantAt: term.Coordinates{X: 1, Y: 1}, wantClip: "cd\n"},
		{name: "copy a shift selection", content: "ab\ncd", keys: "<shift-right><meta-c>", want: "ab\ncd", wantAt: term.Coordinates{X: 1}, wantClip: "a", wantSel: "a"},
		{name: "copy the last line selected by line", content: "ab\ncd", at: term.Coordinates{Y: 1}, keys: "<meta-l><meta-c>", want: "ab\ncd", wantAt: term.Coordinates{X: 2, Y: 1}, wantClip: "cd", wantSel: "cd"},
		{name: "copy a middle line selected by line", content: "ab\ncd", keys: "<meta-l><meta-c>", want: "ab\ncd", wantAt: term.Coordinates{Y: 1}, wantClip: "ab\n", wantSel: "ab\n"},
		{name: "paste a line with the caret mid-line", content: "ab\ncd", at: term.Coordinates{X: 1, Y: 1}, clip: "zz\n", keys: "<meta-v>", want: "ab\nzz\ncd", wantAt: term.Coordinates{X: 1, Y: 2}, wantClip: "zz\n"},
		{name: "paste a line on the last line", content: "ab\ncd", at: term.Coordinates{X: 2, Y: 1}, clip: "zz\n", keys: "<meta-v>", want: "ab\nzz\ncd", wantAt: term.Coordinates{X: 2, Y: 2}, wantClip: "zz\n"},
		{name: "paste a line on an empty buffer", content: "", clip: "zz\n", keys: "<meta-v>", want: "zz\n", wantAt: term.Coordinates{Y: 1}, wantClip: "zz\n"},
		{name: "paste a line over a shift selection", content: "ab\ncd", clip: "zz\n", keys: "<shift-right><meta-v>", want: "zz\nb\ncd", wantAt: term.Coordinates{Y: 1}, wantClip: "zz\n"},
		{name: "paste a line with a double-width caret line", content: "ab\n日本", at: term.Coordinates{X: 1, Y: 1}, clip: "zz\n", keys: "<meta-v>", want: "ab\nzz\n日本", wantAt: term.Coordinates{X: 1, Y: 2}, wantClip: "zz\n"},
		{name: "move a shift selection down", content: "ab\ncd\nef\ngh", at: term.Coordinates{X: 1, Y: 1}, keys: "<shift-down><alt-down>", want: "ab\ngh\ncd\nef", wantAt: term.Coordinates{X: 1, Y: 3}, wantSel: "d\ne"},
		{name: "move a selection that ends at a line start up", content: "ab\ncd\nef\ngh", at: term.Coordinates{Y: 1}, keys: "<shift-down><alt-up>", want: "cd\nab\nef\ngh", wantAt: term.Coordinates{Y: 1}, wantSel: "cd\n"},
		{name: "move a line down past the line end", content: "ab\ncd", at: term.Coordinates{X: 9}, keys: "<alt-down>", want: "cd\nab", wantAt: term.Coordinates{X: 2, Y: 1}},
		{name: "move the only line down", content: "ab", at: term.Coordinates{X: 1}, keys: "<alt-down>", want: "ab", wantAt: term.Coordinates{X: 1}},
		{name: "move a line down on an empty buffer", content: "", keys: "<alt-down>", want: "", wantAt: term.Coordinates{}},
		{name: "move a tab-indented line down", content: "\tab\ncd", at: term.Coordinates{X: 2}, keys: "<alt-down>", want: "cd\n\tab", wantAt: term.Coordinates{X: 2, Y: 1}},
		{name: "move a double-width line down", content: "日本\ncd", at: term.Coordinates{X: 1}, keys: "<alt-down>", want: "cd\n日本", wantAt: term.Coordinates{X: 1, Y: 1}},
		{name: "move a line over a double-width line", content: "ab\n日本", at: term.Coordinates{X: 2}, keys: "<alt-down>", want: "日本\nab", wantAt: term.Coordinates{X: 2, Y: 1}},
		{name: "duplicate a shift selection down", content: "ab\ncd\nef", keys: "<shift-down><alt-shift-down>", want: "ab\nab\ncd\nef", wantAt: term.Coordinates{Y: 2}, wantSel: "ab\n"},
		{name: "duplicate a shift selection up", content: "ab\ncd\nef", at: term.Coordinates{X: 1}, keys: "<shift-down><alt-shift-up>", want: "ab\ncd\nab\ncd\nef", wantAt: term.Coordinates{X: 1, Y: 1}, wantSel: "b\nc"},
		{name: "duplicate a middle line down", content: "ab\ncd\nef", at: term.Coordinates{X: 1, Y: 1}, keys: "<alt-shift-down>", want: "ab\ncd\ncd\nef", wantAt: term.Coordinates{X: 1, Y: 2}},
		{name: "duplicate the only line down", content: "ab", at: term.Coordinates{X: 2}, keys: "<alt-shift-down>", want: "ab\nab", wantAt: term.Coordinates{X: 2, Y: 1}},
		{name: "duplicate on an empty buffer", content: "", keys: "<alt-shift-down>", want: "\n", wantAt: term.Coordinates{Y: 1}},
		{name: "duplicate the trailing empty line down", content: "ab\n", at: term.Coordinates{Y: 1}, keys: "<alt-shift-down>", want: "ab\n\n", wantAt: term.Coordinates{Y: 2}},
		{name: "duplicate a line past its end", content: "ab\ncd", at: term.Coordinates{X: 9}, keys: "<alt-shift-down>", want: "ab\nab\ncd", wantAt: term.Coordinates{X: 2, Y: 1}},
		{name: "delete line with a selection over two lines", content: "ab\ncd\nef\ngh", at: term.Coordinates{X: 1}, keys: "<shift-down>", then: []term.Event{deleteLine}, want: "ef\ngh", wantAt: term.Coordinates{X: 1}},
		{name: "delete line with a selection ending at a line start", content: "ab\ncd\nef", keys: "<shift-down>", then: []term.Event{deleteLine}, want: "cd\nef", wantAt: term.Coordinates{}},
		{name: "delete line on a line selection to the end", content: "ab\ncd\nef", at: term.Coordinates{Y: 1}, keys: "<meta-l><meta-l>", then: []term.Event{deleteLine}, want: "ab", wantAt: term.Coordinates{X: 2}},
		{name: "cut a shift selection over a line break", content: "ab\ncd", at: term.Coordinates{X: 1}, keys: "<shift-down><meta-x>", want: "ad", wantAt: term.Coordinates{X: 1}, wantClip: "b\nc"},
		{name: "cut a line selection in the middle", content: "ab\ncd\nef\ngh", at: term.Coordinates{X: 1, Y: 1}, keys: "<meta-l><meta-x>", want: "ab\nef\ngh", wantAt: term.Coordinates{Y: 1}, wantClip: "cd\n"},
		{name: "alt-right over a separator glued to a word", content: "a.b c", at: term.Coordinates{X: 1}, keys: "<alt-right>", want: "a.b c", wantAt: term.Coordinates{X: 3}},
		{name: "alt-right over a separator run", content: "foo::bar", at: term.Coordinates{X: 3}, keys: "<alt-right>", want: "foo::bar", wantAt: term.Coordinates{X: 5}},
		{name: "alt-right from mid-word", content: "foo bar", at: term.Coordinates{X: 1}, keys: "<alt-right>", want: "foo bar", wantAt: term.Coordinates{X: 3}},
		{name: "alt-right on a separator-only line", content: "...", keys: "<alt-right>", want: "...", wantAt: term.Coordinates{X: 3}},
		{name: "alt-right over blanks before a word", content: "ab   cd", at: term.Coordinates{X: 2}, keys: "<alt-right>", want: "ab   cd", wantAt: term.Coordinates{X: 7}},
		{name: "alt-right at the end of the buffer", content: "ab", at: term.Coordinates{X: 2}, keys: "<alt-right>", want: "ab", wantAt: term.Coordinates{X: 2}},
		{name: "alt-right over a tab-indented line", content: "ab\n\tcd", at: term.Coordinates{X: 2}, keys: "<alt-right>", want: "ab\n\tcd", wantAt: term.Coordinates{X: 3, Y: 1}},
		{name: "alt-left over a separator run", content: "foo::bar", at: term.Coordinates{X: 5}, keys: "<alt-left>", want: "foo::bar", wantAt: term.Coordinates{X: 3}},
		{name: "alt-left from mid-word", content: "foo bar", at: term.Coordinates{X: 5}, keys: "<alt-left>", want: "foo bar", wantAt: term.Coordinates{X: 4}},
		{name: "alt-left at the start of the buffer", content: "ab", keys: "<alt-left>", want: "ab", wantAt: term.Coordinates{}},
		{name: "alt-left over blanks", content: "ab   cd", at: term.Coordinates{X: 5}, keys: "<alt-left>", want: "ab   cd", wantAt: term.Coordinates{}},
		{name: "alt-left onto a double-width line", content: "日本\nab", at: term.Coordinates{Y: 1}, keys: "<alt-left>", want: "日本\nab", wantAt: term.Coordinates{}},
		{name: "alt-backspace over two blanks", content: "ab  cd", at: term.Coordinates{X: 4}, keys: "<alt-backspace>", want: "abcd", wantAt: term.Coordinates{X: 2}},
		{name: "alt-backspace over one blank", content: "ab cd", at: term.Coordinates{X: 3}, keys: "<alt-backspace>", want: "cd", wantAt: term.Coordinates{}},
		{name: "alt-backspace mid-word", content: "foo bar", at: term.Coordinates{X: 6}, keys: "<alt-backspace>", want: "foo r", wantAt: term.Coordinates{X: 4}},
		{name: "alt-backspace at the start of the buffer", content: "ab", keys: "<alt-backspace>", want: "ab", wantAt: term.Coordinates{}},
		{name: "alt-backspace past the line end", content: "ab cd\nef", at: term.Coordinates{X: 9}, keys: "<alt-backspace>", want: "ab \nef", wantAt: term.Coordinates{X: 3}},
		{name: "alt-backspace below the last line", content: "ab\ncd", at: term.Coordinates{Y: 9}, keys: "<alt-backspace>", want: "abcd", wantAt: term.Coordinates{X: 2}},
		{name: "alt-backspace after a separator", content: "ab.", at: term.Coordinates{X: 3}, keys: "<alt-backspace>", want: "ab", wantAt: term.Coordinates{X: 2}},
		{name: "alt-backspace after an emoji", content: "ab \U0001F600", at: term.Coordinates{X: 4}, keys: "<alt-backspace>", want: "ab ", wantAt: term.Coordinates{X: 3}},
		{name: "alt-backspace on an empty buffer", content: "", keys: "<alt-backspace>", want: "", wantAt: term.Coordinates{}},
		{name: "alt-delete over blanks", content: "ab  cd", at: term.Coordinates{X: 2}, keys: "<alt-delete>", want: "abcd", wantAt: term.Coordinates{X: 2}},
		{name: "alt-delete mid-word", content: "foo bar", at: term.Coordinates{X: 1}, keys: "<alt-delete>", want: "f bar", wantAt: term.Coordinates{X: 1}},
		{name: "alt-delete before a separator", content: "a.b", at: term.Coordinates{X: 1}, keys: "<alt-delete>", want: "ab", wantAt: term.Coordinates{X: 1}},
		{name: "alt-delete at the end of the buffer", content: "ab", at: term.Coordinates{X: 2}, keys: "<alt-delete>", want: "ab", wantAt: term.Coordinates{X: 2}},
		{name: "alt-delete at a line end before blanks", content: "ab\n  cd", at: term.Coordinates{X: 2}, keys: "<alt-delete>", want: "abcd", wantAt: term.Coordinates{X: 2}},
		{name: "alt-delete past the line end", content: "ab\ncd", at: term.Coordinates{X: 9}, keys: "<alt-delete>", want: "abcd", wantAt: term.Coordinates{X: 2}},
		{name: "alt-delete below the last line", content: "ab\ncd", at: term.Coordinates{Y: 9}, keys: "<alt-delete>", want: "ab\n", wantAt: term.Coordinates{Y: 1}},
		{name: "alt-delete on an empty buffer", content: "", keys: "<alt-delete>", want: "", wantAt: term.Coordinates{}},
		{name: "alt-delete over an empty last line", content: "ab\n", at: term.Coordinates{X: 2}, keys: "<alt-delete>", want: "ab", wantAt: term.Coordinates{X: 2}},
		{name: "ctrl-right", content: "ab cd", keys: "<ctrl-right>", want: "ab cd", wantAt: term.Coordinates{X: 2}},
		{name: "ctrl-left", content: "ab cd", at: term.Coordinates{X: 5}, keys: "<ctrl-left>", want: "ab cd", wantAt: term.Coordinates{X: 3}},
		{name: "ctrl-backspace", content: "ab cd", at: term.Coordinates{X: 5}, keys: "<ctrl-backspace>", want: "ab ", wantAt: term.Coordinates{X: 3}},
		{name: "ctrl-delete", content: "ab cd", keys: "<ctrl-delete>", want: " cd", wantAt: term.Coordinates{}},
		{name: "alt-shift-right twice", content: "ab cd ef", keys: "<alt-shift-right><alt-shift-right>", want: "ab cd ef", wantAt: term.Coordinates{X: 5}, wantSel: "ab cd"},
		{name: "alt-shift-left across a line", content: "ab\ncd", at: term.Coordinates{X: 1, Y: 1}, keys: "<alt-shift-left><alt-shift-left>", want: "ab\ncd", wantAt: term.Coordinates{}, wantSel: "ab\nc"},
		{name: "alt-backspace with a selection", content: "ab cd", at: term.Coordinates{X: 1}, keys: "<shift-right><alt-backspace>", want: "a cd", wantAt: term.Coordinates{X: 1}},
		{name: "alt-delete with a selection", content: "ab cd", at: term.Coordinates{X: 1}, keys: "<shift-right><alt-delete>", want: "a cd", wantAt: term.Coordinates{X: 1}},
		{name: "alt-delete with a line selection on the last line", content: "ab\ncd", at: term.Coordinates{Y: 1}, keys: "<meta-l><alt-delete>", want: "ab\n", wantAt: term.Coordinates{Y: 1}},
		{name: "select a line then extend it right", content: "ab\ncd\nef", keys: "<meta-l><shift-right>", want: "ab\ncd\nef", wantAt: term.Coordinates{X: 1, Y: 1}, wantSel: "ab\nc"},
		{name: "select the last line then shrink it left", content: "ab\ncd", at: term.Coordinates{Y: 1}, keys: "<meta-l><shift-left>", want: "ab\ncd", wantAt: term.Coordinates{X: 1, Y: 1}, wantSel: "c"},
		{name: "select a line then shrink it up", content: "ab\ncd\nef", at: term.Coordinates{Y: 1}, keys: "<meta-l><shift-up>", want: "ab\ncd\nef", wantAt: term.Coordinates{Y: 1}},
		{name: "select a line then extend it down", content: "ab\ncd\nef", keys: "<meta-l><shift-down>", want: "ab\ncd\nef", wantAt: term.Coordinates{Y: 2}, wantSel: "ab\ncd\n"},
		{name: "select all then shrink it left", content: "ab\ncd", keys: "<meta-a><shift-left>", want: "ab\ncd", wantAt: term.Coordinates{X: 1, Y: 1}, wantSel: "ab\nc"},
		{name: "copy a line selection then paste it over itself", content: "ab\ncd", keys: "<meta-l><meta-c><meta-v>", want: "ab\ncd", wantAt: term.Coordinates{Y: 1}, wantClip: "ab\n"},
		{name: "copy a shift selection then type over it", content: "ab", keys: "<shift-right><meta-c>X", want: "Xb", wantAt: term.Coordinates{X: 1}, wantClip: "a"},
		{name: "select the empty last line", content: "ab\n", at: term.Coordinates{Y: 1}, keys: "<meta-l>", want: "ab\n", wantAt: term.Coordinates{Y: 1}},
		{name: "select a single line twice", content: "ab", at: term.Coordinates{X: 1}, keys: "<meta-l><meta-l>", want: "ab", wantAt: term.Coordinates{X: 2}, wantSel: "ab"},
		{name: "select a line from past its end", content: "ab\ncd", at: term.Coordinates{X: 9}, keys: "<meta-l>", want: "ab\ncd", wantAt: term.Coordinates{Y: 1}, wantSel: "ab\n"},
		{name: "select a line from below the buffer", content: "ab\ncd", at: term.Coordinates{Y: 9}, keys: "<meta-l>", want: "ab\ncd", wantAt: term.Coordinates{X: 2, Y: 1}, wantSel: "cd"},
		{name: "select a wide line", content: "界界\nb", at: term.Coordinates{X: 1}, keys: "<meta-l>", want: "界界\nb", wantAt: term.Coordinates{Y: 1}, wantSel: "界界\n"},
		{name: "select a tab line", content: "\tx\ny", at: term.Coordinates{X: 1}, keys: "<meta-l>", want: "\tx\ny", wantAt: term.Coordinates{Y: 1}, wantSel: "\tx\n"},
		{name: "select a line with a null", content: "a\u0000b\nc", keys: "<meta-l>", want: "a\u0000b\nc", wantAt: term.Coordinates{Y: 1}, wantSel: "a\u0000b\n"},
		{name: "select an emoji line", content: "\U0001F600x\ny", at: term.Coordinates{X: 1}, keys: "<meta-l>", want: "\U0001F600x\ny", wantAt: term.Coordinates{Y: 1}, wantSel: "\U0001F600x\n"},
		{name: "select an empty buffer line", content: "", keys: "<meta-l>", want: "", wantAt: term.Coordinates{}},
		{name: "delete a middle line selected by line", content: "ab\ncd\nef", at: term.Coordinates{X: 1, Y: 1}, keys: "<meta-l>", then: []term.Event{deleteLine}, want: "ab\nef", wantAt: term.Coordinates{Y: 1}},
		{name: "select a shift selection's lines", content: "ab\ncd\nef", at: term.Coordinates{Y: 1}, keys: "<shift-down><meta-l>", want: "ab\ncd\nef", wantAt: term.Coordinates{X: 2, Y: 2}, wantSel: "cd\nef"},
		{name: "copy two lines selected by line", content: "ab\ncd\nef", keys: "<meta-l><meta-l><meta-c>", want: "ab\ncd\nef", wantAt: term.Coordinates{Y: 2}, wantClip: "ab\ncd\n", wantSel: "ab\ncd\n"},
		{name: "move a line selection down", content: "ab\ncd\nef", keys: "<meta-l><alt-down>", want: "cd\nab\nef", wantAt: term.Coordinates{Y: 2}, wantSel: "ab\n"},
		{name: "copy a line selection down", content: "ab\ncd", keys: "<meta-l><alt-shift-down>", want: "ab\nab\ncd", wantAt: term.Coordinates{Y: 2}, wantSel: "ab\n"},
		{name: "backspace a line selection", content: "ab\ncd", keys: "<meta-l><backspace>", want: "cd", wantAt: term.Coordinates{}},
		{name: "copy all", content: "ab\ncd", keys: "<meta-a><meta-c>", want: "ab\ncd", wantAt: term.Coordinates{X: 2, Y: 1}, wantClip: "ab\ncd", wantSel: "ab\ncd"},
		{name: "copy a wide shift selection", content: "界界", keys: "<shift-right><meta-c>", want: "界界", wantAt: term.Coordinates{X: 1}, wantClip: "界", wantSel: "界"},
		{name: "copy a line selection at the last line then type", content: "ab\ncd", at: term.Coordinates{Y: 1}, keys: "<meta-l><meta-c>X", want: "ab\nX", wantAt: term.Coordinates{X: 1, Y: 1}, wantClip: "cd"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, buf, clip := newStandardKeymapHandler(t, tc.content, term.Coordinates{})
			h.SetCursorAtScroll(tc.at)
			if h.CursorAtScroll() != tc.at {
				// SetCursorAtScroll clamps; only a buffer shrinking under
				// the cursor leaves it out of bounds.
				h.(*standardHandler).cursor.MoveToScroll(tc.at)
			}
			require.Equal(t, tc.at, h.CursorAtScroll())
			if tc.clip != "" {
				require.NoError(t, clip.Copy(clipboard.DefaultRegisterID,
					clipboard.Data{Text: tc.clip, Metadata: text.LineSelection}))
			}

			if tc.keys != "" {
				feedKeys(t, h, tc.keys)
			}
			for _, ev := range tc.then {
				h.Handle(ev)
			}

			assert.Equal(t, tc.want, buf.String(), "content")
			assert.Equal(t, tc.wantAt, h.CursorAtScroll(), "cursor")
			data, _ := clip.Paste(clipboard.DefaultRegisterID)
			assert.Equal(t, tc.wantClip, data.Text, "clipboard")
			sel, _ := h.Selection()
			assert.Equal(t, tc.wantSel, sel, "selection")
		})
	}
}

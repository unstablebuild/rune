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

// TestStandardKeymapMotion pins the standard editor's word/line motions
// on ctrl-arrows and ctrl-home/end, replacing the old emacs single-key
// motions.
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
			assert.Equal(t, "hello\nworld\n", sel)
		})
	}
}

// TestStandardKeymapSelectLine pins cmd-l selecting the current line,
// matching Zed's editor::SelectLine.
func TestStandardKeymapSelectLine(t *testing.T) {
	h, _, _ := newStandardKeymapHandler(t, "hello\nworld", term.Coordinates{})
	_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModMeta, Ch: 'l'})
	require.True(t, handled)
	_, ok := h.Selection()
	assert.True(t, ok, "cmd-l must select the line")
}

// TestStandardKeymapRecenter pins ctrl-l recentering the view on the
// cursor (Zed's editor::ScrollCursorCenter) instead of selecting a line.
func TestStandardKeymapRecenter(t *testing.T) {
	content := strings.Repeat("line\n", 60)
	h, _, _ := newStandardKeymapHandler(t, content, term.Coordinates{Y: 40})
	_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'l'})
	require.True(t, handled, "ctrl-l must recenter")
	_, ok := h.Selection()
	assert.False(t, ok, "ctrl-l must not create a selection")
}

// TestStandardKeymapClipboard pins ctrl-c copy, ctrl-x cut, ctrl-v paste.
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

// TestStandardKeymapDeletion pins ctrl/alt-backspace and ctrl/alt-delete word
// delete, and ctrl-shift-k line delete.
func TestStandardKeymapDeletion(t *testing.T) {
	t.Run("ctrl-backspace deletes word to the left", func(t *testing.T) {
		h, buf, _ := newStandardKeymapHandler(t, "foo bar", term.Coordinates{X: 7})
		_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Key: term.KeyBackspace})
		require.True(t, handled)
		assert.Equal(t, "foo ", buf.String())
	})

	t.Run("ctrl-delete deletes word to the right", func(t *testing.T) {
		h, buf, _ := newStandardKeymapHandler(t, "foo bar", term.Coordinates{})
		_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Key: term.KeyDelete})
		require.True(t, handled)
		assert.Equal(t, "bar", buf.String())
	})

	t.Run("ctrl-delete stops at a following empty line", func(t *testing.T) {
		h, buf, _ := newStandardKeymapHandler(t, "one\n\ntwo", term.Coordinates{X: 2})
		_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Key: term.KeyDelete})
		require.True(t, handled)
		assert.Equal(t, "on\ntwo", buf.String())
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

	t.Run("alt-delete deletes word to the right", func(t *testing.T) {
		h, buf, _ := newStandardKeymapHandler(t, "left middle right", term.Coordinates{X: 5})
		_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModAlt, Key: term.KeyDelete})
		require.True(t, handled)
		assert.Equal(t, "left right", buf.String())
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

// TestStandardKeymapTypingReplacesSelection pins the TextEdit/Sublime
// convention: typing or pasting with an active selection replaces the
// selected text instead of only deleting it and swallowing the input.
// Deletion keys remove the selection without inserting, and tab keeps
// its indent semantics instead of replacing.
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
			wantSel: "one\ntwo\n",
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

// TestStandardKeymapLineInsert pins ctrl-enter (below) and
// ctrl-shift-enter (above) line insertion.
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

// TestStandardKeymapConflate pins ctrl-shift-j joining lines.
func TestStandardKeymapConflate(t *testing.T) {
	h, buf, _ := newStandardKeymapHandler(t, "one\ntwo", term.Coordinates{})
	_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'J'})
	require.True(t, handled)
	assert.Equal(t, "onetwo", buf.String())
}

// TestStandardKeymapDropsEmacsBindings asserts the dropped emacs
// single-key motions no longer perform their emacs actions in the
// standard editor.
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

// TestStandardKeymapParagraphMotion pins ctrl-up/down paragraph motion,
// matching Zed's editor::MoveToStartOfParagraph / MoveToEndOfParagraph.
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

// TestStandardKeymapParagraphSelection pins ctrl-shift-up/down extending a
// selection by paragraph, matching Zed's SelectToStart/EndOfParagraph.
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

// TestStandardKeymapTranspose pins ctrl-t transposing the two characters
// around the caret, matching Zed's editor::Transpose.
func TestStandardKeymapTranspose(t *testing.T) {
	h, buf, _ := newStandardKeymapHandler(t, "abcd", term.Coordinates{X: 2})
	_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 't'})
	require.True(t, handled)
	assert.Equal(t, "acbd", buf.String())
	assert.Equal(t, term.Coordinates{X: 3}, h.CursorAtScroll())
}

// TestStandardKeymapSelectionUndoRedo pins cmd-u / cmd-shift-u undoing and
// redoing selection changes, matching Zed's editor::UndoSelection /
// RedoSelection.
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

// TestStandardKeymapSelectPrevious pins ctrl-cmd-d selecting the previous
// occurrence of the word at the cursor, matching Zed's editor::SelectPrevious.
func TestStandardKeymapSelectPrevious(t *testing.T) {
	h, _, _ := newStandardKeymapHandler(t, "foo bar foo baz", term.Coordinates{X: 9})
	_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl | term.ModMeta, Ch: 'd'})
	require.True(t, handled)
	sel, ok := h.Selection()
	require.True(t, ok, "ctrl-cmd-d must create a selection")
	assert.Equal(t, "foo", sel)
	assert.Equal(t, term.Coordinates{X: 3}, h.CursorAtScroll())
}

// TestStandardKeymapToggleSoftWrap pins alt-z toggling soft wrap, matching
// Zed's editor::ToggleSoftWrap.
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

// TestStandardKeymapDeleteToBeginningOfLine pins cmd-backspace deleting from
// the caret to the start of the line, matching Zed's
// editor::DeleteToBeginningOfLine.
func TestStandardKeymapDeleteToBeginningOfLine(t *testing.T) {
	h, buf, _ := newStandardKeymapHandler(t, "hello world", term.Coordinates{X: 6})
	_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModMeta, Key: term.KeyBackspace})
	require.True(t, handled)
	assert.Equal(t, "world", buf.String())
	assert.Equal(t, term.Coordinates{}, h.CursorAtScroll())
}

// TestStandardKeymapSyntaxNodeSelect pins ctrl-shift-right/left to
// expand/shrink the syntactic selection (Zed's SelectLarger/SmallerSyntaxNode)
// rather than word-select. Without a syntax service in the harness the ops are
// no-ops, so we assert the cursor does not move by word.
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

// TestStandardKeymapWordSelect pins alt-shift-left/right (and alt-shift-b/f)
// extending a selection by word, matching Zed's SelectToPreviousWordStart /
// SelectToNextWordEnd.
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

// TestStandardKeymapLineDocSelect pins cmd-shift-left/right/up/down and
// shift-home/shift-end extending a selection to line and document bounds.
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

// TestStandardKeymapCutToEndOfLine pins ctrl-k cutting from the caret to the
// end of the line into the clipboard, matching Zed's editor::CutToEndOfLine.
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

// TestStandardKeymapDeleteToEndOfLine pins cmd-delete deleting from the caret
// to the end of the line, matching Zed's editor::DeleteToEndOfLine.
func TestStandardKeymapDeleteToEndOfLine(t *testing.T) {
	h, buf, _ := newStandardKeymapHandler(t, "hello world", term.Coordinates{X: 5})
	_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModMeta, Key: term.KeyDelete})
	require.True(t, handled)
	assert.Equal(t, "hello", buf.String())
}

// TestStandardKeymapLinuxLeavesMetaToRune pins that the Linux keymap handles
// no <meta> chord, so every one reaches Rune's command layer whichever
// physical key the user picked as <meta>.
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

// TestStandardKeymapLinuxCtrlLayer pins the Linux chords that differ from
// their macOS <ctrl> twins.
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

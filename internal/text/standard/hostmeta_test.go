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
	"unstable.build/rune/internal/text/registerhistory"
	"unstable.build/rune/internal/text/registerset"
)

// The package's keymap tests spell the Command layout, so they run the
// same on every host; the Linux layout is pinned below.
func init() { defaultHostMetaChords = true }

func hostKey(mod term.Modifier, ch rune) term.Event {
	return term.Event{Type: term.EventKey, Mod: mod, Ch: ch}
}

func hostNamedKey(mod term.Modifier, key term.Key) term.Event {
	return term.Event{Type: term.EventKey, Mod: mod, Key: key}
}

// TestLinuxLayoutDeclinesHostChords pins that without host Meta chords the
// editor claims none of its Command, Ctrl+Alt or Ctrl+Alt+Shift chords, so
// they reach the desktop or Rune's command layer, and edits nothing.
func TestLinuxLayoutDeclinesHostChords(t *testing.T) {
	var evs []term.Event
	for _, ch := range "kuUj/][lDJdxfacvVzyZK" {
		evs = append(evs, hostKey(term.ModMeta, ch))
	}
	for _, k := range []term.Key{
		term.KeyArrowLeft, term.KeyArrowRight, term.KeyArrowUp, term.KeyArrowDown,
		term.KeyBackspace, term.KeyDelete,
	} {
		evs = append(evs, hostNamedKey(term.ModMeta, k), hostNamedKey(term.ModShiftMeta, k))
	}
	for _, ch := range "[]/qv" {
		evs = append(evs, hostKey(term.ModAltMeta, ch))
	}
	evs = append(evs,
		hostKey(term.ModCtrlMeta, 'd'),
		hostNamedKey(term.ModShiftMeta, term.KeySpace),
		hostNamedKey(term.ModCtrlAlt, term.KeyArrowUp),
		hostNamedKey(term.ModCtrlAlt, term.KeyArrowDown),
		hostKey(term.ModCtrlAlt, 'h'),
		hostKey(term.ModCtrlAlt, 'v'),
		hostKey(term.ModCtrlAlt, 'i'),
		hostKey(term.ModCtrlAlt, 'H'),
		hostKey(term.ModCtrlAlt, '1'),
	)
	const content = "foo bar foo\n\tbaz\nqux\n"
	at := term.Coordinates{X: 4, Y: 0}
	for _, ev := range evs {
		t.Run(ev.KeyComb().String(), func(t *testing.T) {
			h, _, _ := newStandardKeymapHandler(t, content, at, WithHostMetaChords(false))
			_, handled := h.Handle(ev)
			assert.False(t, handled, "the Linux layout must leave %s alone", ev.KeyComb())
			assert.Equal(t, content, h.CellView().String())
			assert.Equal(t, at, h.CursorAtScroll())
			_, selected := h.Selection()
			assert.False(t, selected)
		})
	}
}

// TestLinuxLayoutMirrorsHostChords pins that each Linux home does exactly
// what the Command chord it replaces does on macOS.
func TestLinuxLayoutMirrorsHostChords(t *testing.T) {
	ctrlRight := hostNamedKey(term.ModCtrl, term.KeyArrowRight)
	shiftDown := hostNamedKey(term.ModShift, term.KeyArrowDown)
	copyLine := hostKey(term.ModCtrl, 'c')
	for _, tc := range []struct {
		name    string
		content string
		at      term.Coordinates
		opts    []Option
		linux   []term.Event
		darwin  []term.Event
	}{
		{
			name: "delete to line start", content: "foo bar", at: term.Coordinates{X: 4},
			linux:  []term.Event{hostNamedKey(term.ModCtrlShift, term.KeyBackspace)},
			darwin: []term.Event{hostNamedKey(term.ModMeta, term.KeyBackspace)},
		},
		{
			name: "delete to line end", content: "foo bar", at: term.Coordinates{X: 3},
			linux:  []term.Event{hostNamedKey(term.ModCtrlShift, term.KeyDelete)},
			darwin: []term.Event{hostNamedKey(term.ModMeta, term.KeyDelete)},
		},
		{
			name: "undo cursor move", content: "foo bar",
			linux:  []term.Event{ctrlRight, hostKey(term.ModCtrl, 'L'), hostKey(term.ModCtrl, 'u')},
			darwin: []term.Event{ctrlRight, hostKey(term.ModMeta, 'l'), hostKey(term.ModMeta, 'u')},
		},
		{
			name: "redo cursor move", content: "foo bar",
			linux: []term.Event{
				hostKey(term.ModCtrl, 'L'), hostKey(term.ModCtrl, 'u'), hostKey(term.ModCtrl, 'U'),
			},
			darwin: []term.Event{
				hostKey(term.ModMeta, 'l'), hostKey(term.ModMeta, 'u'), hostKey(term.ModMeta, 'U'),
			},
		},
		{
			name: "select line", content: "hello\nworld",
			linux:  []term.Event{hostKey(term.ModCtrl, 'L')},
			darwin: []term.Event{hostKey(term.ModMeta, 'l')},
		},
		{
			name: "select indentation level", content: "a\n\tb\n\tc\nd", at: term.Coordinates{Y: 1},
			linux:  []term.Event{hostKey(term.ModCtrl, 'I')},
			darwin: []term.Event{hostKey(term.ModMeta, 'J')},
		},
		{
			name: "paste and reindent", content: "\tfoo\nbar",
			linux:  []term.Event{copyLine, hostKey(term.ModCtrl, 'V')},
			darwin: []term.Event{copyLine, hostKey(term.ModMeta, 'V')},
		},
		{
			name: "prefix delete to line start", content: "foo bar", at: term.Coordinates{X: 4},
			linux: []term.Event{
				hostKey(term.ModCtrl, 'k'), hostNamedKey(term.ModCtrl, term.KeyBackspace),
			},
			darwin: []term.Event{
				hostKey(term.ModMeta, 'k'), hostNamedKey(term.ModMeta, term.KeyBackspace),
			},
		},
		{
			name: "prefix uppercase selection", content: "foo bar",
			linux: []term.Event{
				hostKey(term.ModCtrl, 'L'), hostKey(term.ModCtrl, 'k'), hostKey(term.ModCtrl, 'u'),
			},
			darwin: []term.Event{
				hostKey(term.ModMeta, 'l'), hostKey(term.ModMeta, 'k'), hostKey(term.ModMeta, 'u'),
			},
		},
		{
			name: "wrap paragraph", content: "aaa bbb ccc ddd eee", opts: []Option{WithRuler(8)},
			linux:  []term.Event{hostKey(term.ModCtrl, 'G')},
			darwin: []term.Event{hostKey(term.ModAltMeta, 'q')},
		},
		{
			name: "select previous occurrence", content: "foo x foo y foo", at: term.Coordinates{X: 12},
			linux:  []term.Event{hostKey(term.ModCtrl, 'D')},
			darwin: []term.Event{hostKey(term.ModCtrlMeta, 'd')},
		},
		{
			name: "scroll down", content: strings.Repeat("line\n", 60),
			linux:  []term.Event{hostNamedKey(term.ModAlt, term.KeyPgdn)},
			darwin: []term.Event{hostNamedKey(term.ModCtrlAlt, term.KeyArrowDown)},
		},
		{
			name: "scroll up", content: strings.Repeat("line\n", 60), at: term.Coordinates{Y: 40},
			linux:  []term.Event{hostNamedKey(term.ModAlt, term.KeyPgup)},
			darwin: []term.Event{hostNamedKey(term.ModCtrlAlt, term.KeyArrowUp)},
		},
		{
			name: "hide selection", content: "a\nb\nc\nd",
			linux:  []term.Event{shiftDown, hostKey(term.ModCtrl, 'H')},
			darwin: []term.Event{shiftDown, hostKey(term.ModCtrlAlt, 'h')},
		},
		{
			name: "reveal hidden lines", content: "a\nb\nc\nd",
			linux:  []term.Event{shiftDown, hostKey(term.ModCtrl, 'H'), hostKey(term.ModCtrl, 'R')},
			darwin: []term.Event{shiftDown, hostKey(term.ModCtrlAlt, 'h'), hostKey(term.ModCtrlAlt, 'v')},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := func(hostMeta bool, evs []term.Event) (handled []bool, content string,
				at term.Coordinates, sel string, clip string,
			) {
				opts := append([]Option{WithHostMetaChords(hostMeta)}, tc.opts...)
				h, _, reg := newStandardKeymapHandler(t, tc.content, tc.at, opts...)
				for _, ev := range evs {
					_, ok := h.Handle(ev)
					handled = append(handled, ok)
				}
				sel, _ = h.Selection()
				if paste, err := reg.Paste(clipboard.DefaultRegisterID); err == nil {
					clip = paste.Text
				}
				return handled, h.CellView().String(), h.CursorAtScroll(), sel, clip
			}
			lh, lc, lat, lsel, lclip := run(false, tc.linux)
			dh, dc, dat, dsel, dclip := run(true, tc.darwin)
			require.True(t, dh[len(dh)-1], "the Command chord must act on this content")
			assert.Equal(t, dh, lh, "handled")
			assert.Equal(t, dc, lc, "content")
			assert.Equal(t, dat, lat, "cursor")
			assert.Equal(t, dsel, lsel, "selection")
			assert.Equal(t, dclip, lclip, "clipboard")
		})
	}
}

// TestLinuxLayoutCtrlKCutsToLineEnd pins that <ctrl-k>, now the prefix,
// still cuts to the line end when doubled.
func TestLinuxLayoutCtrlKCutsToLineEnd(t *testing.T) {
	h, _, reg := newStandardKeymapHandler(t, "foo bar", term.Coordinates{X: 3},
		WithHostMetaChords(false))
	_, handled := h.Handle(hostKey(term.ModCtrl, 'k'))
	require.True(t, handled, "<ctrl-k> must start the prefix")
	assert.Equal(t, "foo bar", h.CellView().String())
	_, handled = h.Handle(hostKey(term.ModCtrl, 'k'))
	require.True(t, handled)
	assert.Equal(t, "foo", h.CellView().String())
	paste, err := reg.Paste(clipboard.DefaultRegisterID)
	require.NoError(t, err)
	assert.Equal(t, " bar", paste.Text)
}

// TestPrefixPastesFromHistory pins <ctrl-k><ctrl-v>, Sublime's paste from
// history, as the Linux home of <alt-meta-v>: repeating it walks back
// through older clipboard entries, and the prefix also works on macOS.
func TestPrefixPastesFromHistory(t *testing.T) {
	for _, tc := range []struct {
		hostMeta    bool
		paste, hist string
	}{
		{hostMeta: false, paste: "<ctrl-v>", hist: "<ctrl-k><ctrl-v>"},
		{hostMeta: true, paste: "<meta-v>", hist: "<meta-k><meta-v>"},
		{hostMeta: true, paste: "<meta-v>", hist: "<alt-meta-v>"},
	} {
		t.Run(tc.hist, func(t *testing.T) {
			reg := registerhistory.NewClipboard(registerset.New(clipboard.NewInMemory()))
			for _, entry := range []string{"a", "b", "c"} {
				require.NoError(t, reg.Copy(clipboard.DefaultRegisterID,
					clipboard.Data{Text: entry, Metadata: text.StandardSelection}))
			}
			h, buf, _ := newStandardKeymapHandler(t, "z", term.Coordinates{X: 1},
				WithHostMetaChords(tc.hostMeta), WithClipboard(reg))
			for _, step := range []struct{ keys, want string }{
				{tc.paste, "zc"},
				{tc.hist, "zb"},
				{tc.hist, "za"},
			} {
				keys, err := term.ParseKeys(step.keys)
				require.NoError(t, err)
				for _, k := range keys {
					_, handled := h.Handle(term.Event{
						Type: term.EventKey, Key: k.Key, Mod: k.Mod, Ch: k.Ch,
					})
					require.True(t, handled, "%s", step.keys)
				}
				assert.Equal(t, step.want, buf.String(), "after %s", step.keys)
			}
		})
	}
}

// TestLinuxLayoutTogglesBlockComment pins <ctrl-shift-/> as the Linux home
// of <alt-meta-/>.
func TestLinuxLayoutTogglesBlockComment(t *testing.T) {
	for _, tc := range []struct {
		hostMeta bool
		sel      term.Event
		ev       term.Event
	}{
		{hostMeta: true, sel: hostKey(term.ModMeta, 'a'), ev: hostKey(term.ModAltMeta, '/')},
		{hostMeta: false, sel: hostKey(term.ModCtrl, 'a'), ev: hostKey(term.ModCtrl, '?')},
	} {
		t.Run(tc.ev.KeyComb().String(), func(t *testing.T) {
			uri, err := workspaceapi.ParseURI("file:///main.go")
			require.NoError(t, err)
			buf := cell.NewBuffer()
			buf.ReadFrom(strings.NewReader("foo"))
			h := NewHandler(buf, uri, text.IndentRuneTab, 0,
				WithHostMetaChords(tc.hostMeta),
				WithComments(text.CommentConfig{"go": {
					Block: []text.CommentBlock{{Start: "/*", End: "*/"}},
				}}))
			h.Resize(80, 20)
			_, handled := h.Handle(tc.sel)
			require.True(t, handled)
			_, handled = h.Handle(tc.ev)
			require.True(t, handled)
			assert.Contains(t, h.CellView().String(), "/*")
		})
	}
}
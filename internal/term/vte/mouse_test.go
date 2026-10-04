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

package vte

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/mouse"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/term/vte/vteparser"
)

func newMouseHarness(t *testing.T, modes ...vteparser.PrivateMode) *mouseDriver {
	t.Helper()
	comp, err := NewComponent(&testExecutor{}, &testExecutor{}, &mockTabManager{}, DefaultConfig())
	require.NoError(t, err)
	comp.Resize(300, 300)
	for _, mode := range modes {
		comp.parserHandler.SetPrivateMode(mode)
	}
	return &mouseDriver{t: comp}
}

func mouseEv(key term.Key, x, y int) term.Event {
	return term.Event{Type: term.EventMouse, Key: key, MouseX: x, MouseY: y}
}

type mouseStep struct {
	ev       term.Event
	want     string
	tracking bool
}

// TestMouseDriverReport pins the encodings to kitty's
// encode_mouse_event_impl and the per-mode filter to its
// send_mouse_event (kitty/mouse.c:73-121, :1672-1673).
func TestMouseDriverReport(t *testing.T) {
	t.Parallel()
	const (
		anyEvent = vteparser.PrivateModeReportAllMouseMotion
		button   = vteparser.PrivateModeReportCellMouseMotion
		click    = vteparser.PrivateModeReportMouseClicks
		sgr      = vteparser.PrivateModeSgrMouse
		utf8     = vteparser.PrivateModeUtf8Mouse
	)
	cases := []struct {
		name  string
		modes []vteparser.PrivateMode
		steps []mouseStep
	}{
		{
			// the modes terminal-browser enables: 1003 on its own,
			// without 1000 or 1002
			name:  "any-event sgr",
			modes: []vteparser.PrivateMode{anyEvent, sgr},
			steps: []mouseStep{
				{mouseEv(0, 4, 2), "\x1b[<35;5;3M", true},
				{mouseEv(0, 4, 2), "", true},
				{mouseEv(term.MouseLeft, 4, 2), "\x1b[<0;5;3M", true},
				{mouseEv(term.MouseLeft, 4, 2), "", true},
				{mouseEv(term.MouseLeft, 6, 2), "\x1b[<32;7;3M", true},
				{mouseEv(term.MouseRelease, 6, 2), "\x1b[<0;7;3m", true},
				{mouseEv(term.MouseRight, 6, 2), "\x1b[<2;7;3M", true},
				{mouseEv(term.MouseRelease, 6, 2), "\x1b[<2;7;3m", true},
				{mouseEv(term.MouseMiddle, 6, 2), "\x1b[<1;7;3M", true},
				{mouseEv(term.MouseRelease, 6, 2), "\x1b[<1;7;3m", true},
				{mouseEv(term.MouseWheelUp, 1, 1), "\x1b[<64;2;2M", true},
				{mouseEv(term.MouseWheelUp, 1, 1), "\x1b[<64;2;2M", true},
				{mouseEv(term.MouseWheelDown, 1, 1), "\x1b[<65;2;2M", true},
			},
		},
		{
			name:  "button-event sgr drops hover",
			modes: []vteparser.PrivateMode{button, sgr},
			steps: []mouseStep{
				{mouseEv(0, 4, 2), "", true},
				{mouseEv(term.MouseLeft, 4, 2), "\x1b[<0;5;3M", true},
				{mouseEv(term.MouseLeft, 5, 3), "\x1b[<32;6;4M", true},
				{mouseEv(term.MouseRelease, 5, 3), "\x1b[<0;6;4m", true},
				{mouseEv(term.MouseWheelDown, 5, 3), "\x1b[<65;6;4M", true},
			},
		},
		{
			name:  "click sgr drops motion",
			modes: []vteparser.PrivateMode{click, sgr},
			steps: []mouseStep{
				{mouseEv(0, 4, 2), "", true},
				{mouseEv(term.MouseLeft, 4, 2), "\x1b[<0;5;3M", true},
				{mouseEv(term.MouseLeft, 5, 3), "", true},
				{mouseEv(term.MouseRelease, 5, 3), "\x1b[<0;6;4m", true},
				{mouseEv(term.MouseWheelUp, 5, 3), "\x1b[<64;6;4M", true},
			},
		},
		{
			name:  "legacy encoding",
			modes: []vteparser.PrivateMode{anyEvent},
			steps: []mouseStep{
				{mouseEv(term.MouseLeft, 4, 2), "\x1b[M %#", true},
				{mouseEv(term.MouseLeft, 5, 2), "\x1b[M@&#", true},
				{mouseEv(term.MouseRelease, 5, 2), "\x1b[M#&#", true},
				{mouseEv(term.MouseWheelUp, 5, 2), "\x1b[M`&#", true},
				{mouseEv(0, 6, 2), "\x1b[MC'#", true},
				// kitty drops what the byte encoding cannot address
				{mouseEv(term.MouseLeft, 223, 2), "", true},
			},
		},
		{
			name:  "utf8 encoding",
			modes: []vteparser.PrivateMode{click, utf8},
			steps: []mouseStep{
				{mouseEv(term.MouseLeft, 250, 2), "\x1b[M " + string(rune(283)) + "#", true},
				{mouseEv(term.MouseRelease, 250, 2), "\x1b[M#" + string(rune(283)) + "#", true},
			},
		},
		{
			name:  "modifiers",
			modes: []vteparser.PrivateMode{click, sgr},
			steps: []mouseStep{
				{term.Event{
					Type: term.EventMouse, Key: term.MouseLeft,
					Mod: term.ModCtrl | term.ModShift, MouseX: 0, MouseY: 0,
				}, "\x1b[<20;1;1M", true},
				{term.Event{
					Type: term.EventMouse, Key: term.MouseWheelUp,
					Mod: term.ModAlt, MouseX: 0, MouseY: 0,
				}, "\x1b[<72;1;1M", true},
			},
		},
		{
			name:  "no tracking",
			modes: []vteparser.PrivateMode{sgr},
			steps: []mouseStep{
				{mouseEv(term.MouseLeft, 4, 2), "", false},
				{mouseEv(term.MouseWheelUp, 4, 2), "", false},
				{mouseEv(0, 4, 2), "", false},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newMouseHarness(t, tc.modes...)
			for i, step := range tc.steps {
				raw, tracking := d.report(step.ev)
				assert.Equal(t, step.want, string(raw), "step %d", i)
				assert.Equal(t, step.tracking, tracking, "step %d", i)
			}
		})
	}
}

// TestMouseDriverReportIgnoresPixelMode keeps SGR cell coordinates when
// a program also asks for SGR-pixels (1016): mouse events carry no
// pixel position, and DECRQM reports 1016 as unrecognized so programs
// such as terminal-browser fall back to cells.
func TestMouseDriverReportIgnoresPixelMode(t *testing.T) {
	t.Parallel()
	d := newMouseHarness(t,
		vteparser.PrivateModeReportAllMouseMotion,
		vteparser.PrivateModeSgrMouse,
		vteparser.PrivateMode(1016),
	)
	raw, tracking := d.report(mouseEv(term.MouseLeft, 9, 9))
	assert.True(t, tracking)
	assert.Equal(t, "\x1b[<0;10;10M", string(raw))
}

// TestMouseDriverAlternateScroll pins DECSET 1007: without mouse
// tracking the wheel over the alternate screen, which has no
// scrollback, becomes cursor keys (kitty keys.c:363 fake_scroll).
func TestMouseDriverAlternateScroll(t *testing.T) {
	t.Parallel()
	alt := vteparser.PrivateModeSwapScreenAndSetRestoreCursor
	cases := []struct {
		name  string
		set   []vteparser.PrivateMode
		unset []vteparser.PrivateMode
		ev    term.Event
		want  string
	}{
		{"wheel up", []vteparser.PrivateMode{alt}, nil,
			mouseEv(term.MouseWheelUp, 0, 0), "\x1b[A"},
		{"wheel down", []vteparser.PrivateMode{alt}, nil,
			mouseEv(term.MouseWheelDown, 0, 0), "\x1b[B"},
		{"application cursor keys",
			[]vteparser.PrivateMode{alt, vteparser.PrivateModeCursorKeys}, nil,
			mouseEv(term.MouseWheelUp, 0, 0), "\x1bOA"},
		{"primary screen scrolls the scrollback instead", nil, nil,
			mouseEv(term.MouseWheelUp, 0, 0), ""},
		{"disabled", []vteparser.PrivateMode{alt},
			[]vteparser.PrivateMode{vteparser.PrivateModeAlternateScroll},
			mouseEv(term.MouseWheelUp, 0, 0), ""},
		{"not a wheel", []vteparser.PrivateMode{alt}, nil,
			mouseEv(term.MouseLeft, 0, 0), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newMouseHarness(t, tc.set...)
			for _, mode := range tc.unset {
				d.t.parserHandler.UnsetPrivateMode(mode)
			}
			assert.Equal(t, tc.want, string(d.alternateScroll(tc.ev)))
		})
	}
}

// recordingClipboard counts Copy calls on top of an in-memory register.
type recordingClipboard struct {
	clipboard.Register
	copies []string
}

func (c *recordingClipboard) Copy(registerID string, data clipboard.Data) error {
	c.copies = append(c.copies, data.Text)
	return c.Register.Copy(registerID, data)
}

// TestMouseDriverSelectionCopy pins when a terminal highlight replaces the
// clipboard. A click, or pointer jitter that stays inside the pressed cell,
// must not highlight or copy anything; a drag that reaches another cell
// highlights and copies as before.
func TestMouseDriverSelectionCopy(t *testing.T) {
	t.Parallel()

	const sentinel = "previously-copied"
	press := term.Coordinates{X: 1}

	cases := []struct {
		desc string
		drag []term.Coordinates
		want string // expected highlight, empty for none
	}{
		{
			desc: "click without movement",
		},
		{
			desc: "jitter inside the pressed cell",
			drag: []term.Coordinates{press, press, press},
		},
		{
			desc: "drag right",
			drag: []term.Coordinates{press, {X: 4}},
			want: "ello",
		},
		{
			// Every tick after the first extends a highlight that already
			// exists, which a single jump to the final cell never does.
			desc: "drag right one cell at a time",
			drag: []term.Coordinates{{X: 2}, {X: 3}, {X: 4}, {X: 5}},
			want: "ello ",
		},
		{
			desc: "drag left",
			drag: []term.Coordinates{{X: 0}},
			want: "he",
		},
		{
			desc: "drag to the next row",
			drag: []term.Coordinates{{X: 1, Y: 1}},
			want: "ello world     \nfo",
		},
		{
			desc: "drag away and back keeps the pressed cell",
			drag: []term.Coordinates{{X: 3}, press},
			want: "e",
		},
	}

	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()
			tm := mockTabManager{}
			comp, err := NewComponent(&testExecutor{}, &testExecutor{}, &tm, DefaultConfig())
			require.NoError(t, err)
			p := comp.parserHandler
			p.sync.primBuf.SetDefaultChar(' ')
			comp.Resize(16, 3)
			writeToBuffer(p, "hello world\nfoo_bar")

			clip := &recordingClipboard{Register: clipboard.NewInMemory()}
			require.NoError(t, clip.Register.Copy(
				clipboard.DefaultRegisterID, clipboard.Data{Text: sentinel}))
			driver := &mouseDriver{t: comp, clipboard: clip}

			// Mirror mouse.Mouse.handleLeftClickSelect: the press clears and
			// anchors, then every held-button event calls SetSelectionEnd.
			driver.ClearSelection()
			driver.SetSelectionStart(press)
			for _, pos := range tc.drag {
				driver.SetSelectionEnd(pos)
			}
			if tc.want != "" {
				highlighted, highlightedOK := comp.Selection()
				require.True(t, highlightedOK)
				assert.Equal(t, tc.want, highlighted, "highlight before release")
				held, err := clip.Paste(clipboard.DefaultRegisterID)
				require.NoError(t, err)
				assert.Equal(t, sentinel, held.Text, "clipboard waits for release")
			}
			driver.OnAction(term.Event{}, term.Coordinates{}, mouse.Release)

			got, ok := comp.Selection()
			pasted, err := clip.Paste(clipboard.DefaultRegisterID)
			require.NoError(t, err)
			if tc.want == "" {
				assert.False(t, ok, "unexpected highlight %q", got)
				assert.Empty(t, clip.copies)
				assert.Equal(t, sentinel, pasted.Text)
				return
			}
			require.True(t, ok)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.want, pasted.Text)
		})
	}
}

// TestMouseDriverWordAndLineSelectionCopy pins that double- and
// triple-click selection still copy.
func TestMouseDriverWordAndLineSelectionCopy(t *testing.T) {
	t.Parallel()

	cases := []struct {
		desc  string
		steps func(*mouseDriver)
		want  string
	}{
		{
			desc:  "double-click selects a word",
			steps: func(d *mouseDriver) { d.SelectWordAt(term.Coordinates{X: 7}) },
			want:  "world",
		},
		{
			desc:  "triple-click selects a line",
			steps: func(d *mouseDriver) { d.SelectLine(1) },
			want:  "foo_bar\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()
			tm := mockTabManager{}
			comp, err := NewComponent(&testExecutor{}, &testExecutor{}, &tm, DefaultConfig())
			require.NoError(t, err)
			p := comp.parserHandler
			p.sync.primBuf.SetDefaultChar(' ')
			comp.Resize(16, 3)
			writeToBuffer(p, "hello world\nfoo_bar")

			clip := clipboard.NewInMemory()
			driver := &mouseDriver{t: comp, clipboard: clip}
			tc.steps(driver)

			pasted, err := clip.Paste(clipboard.DefaultRegisterID)
			require.NoError(t, err)
			assert.Equal(t, tc.want, pasted.Text)
		})
	}
}

// TestMouseDriverGestures drives the driver through mouse.Mouse, as
// Handler does, so the SDK's edge auto-scroll and the actions that
// separate one gesture from the next are exercised with it. The buffer
// has scrollback so that auto-scroll in the top rows has an effect.
func TestMouseDriverGestures(t *testing.T) {
	t.Parallel()

	const (
		sentinel  = "previously-copied"
		elsewhere = "copied-elsewhere"
		wordRow   = 4 // shows "hello world"
		otherRow  = 5
	)

	type env struct {
		comp  *Component
		clip  clipboard.Register
		mouse *mouse.Mouse
	}
	type step func(*testing.T, env)
	ev := func(key term.Key, x, y int) step {
		return func(_ *testing.T, e env) { e.mouse.Handle(mouseEv(key, x, y)) }
	}
	left := func(x, y int) step { return ev(term.MouseLeft, x, y) }
	release := func(x, y int) step { return ev(term.MouseRelease, x, y) }
	// esc mirrors Handler.handleInput, which clears the highlight on Esc
	// without going through the driver.
	esc := func(_ *testing.T, e env) { e.comp.Unselect() }
	copyElsewhere := func(t *testing.T, e env) {
		require.NoError(t, e.clip.Copy(clipboard.DefaultRegisterID, clipboard.Data{Text: elsewhere}))
	}
	drag := []step{left(1, wordRow), left(4, wordRow), release(4, wordRow)}

	cases := []struct {
		desc       string
		steps      []step
		want       string // clipboard after the steps
		wantScroll int
	}{
		{
			desc:  "drag copies on release",
			steps: drag,
			want:  "ello",
		},
		{
			desc:  "click with jitter in the top rows neither scrolls nor copies",
			steps: []step{left(1, 1), left(1, 1), left(1, 1), release(1, 1)},
			want:  sentinel,
		},
		{
			desc:       "drag in the top rows auto-scrolls",
			steps:      []step{left(1, 2), left(1, 1)},
			want:       sentinel,
			wantScroll: 1,
		},
		{
			desc:       "wheel after an unreleased click scrolls",
			steps:      []step{left(1, wordRow), ev(term.MouseWheelUp, 1, wordRow)},
			want:       sentinel,
			wantScroll: 1,
		},
		{
			desc: "right click after a finished drag keeps a later copy",
			steps: append(drag[:len(drag):len(drag)], esc, copyElsewhere,
				ev(term.MouseRight, 8, otherRow), release(8, otherRow)),
			want: elsewhere,
		},
		{
			desc: "middle click after a finished drag keeps a later copy",
			steps: append(drag[:len(drag):len(drag)], copyElsewhere,
				ev(term.MouseMiddle, 8, otherRow), release(8, otherRow)),
			want: elsewhere,
		},
		{
			desc:  "Esc during a drag leaves nothing to copy",
			steps: []step{left(1, wordRow), left(4, wordRow), esc, release(4, wordRow)},
			want:  sentinel,
		},
	}

	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()
			tm := mockTabManager{}
			comp, err := NewComponent(&testExecutor{}, &testExecutor{}, &tm, DefaultConfig())
			require.NoError(t, err)
			p := comp.parserHandler
			p.sync.primBuf.SetDefaultChar(' ')
			comp.Resize(16, 6)
			writeToBuffer(p, "s0\ns1\ns2\ns3\ns4\ns5\nhello world\nfoo_bar")

			clip := clipboard.NewInMemory()
			require.NoError(t, clip.Copy(clipboard.DefaultRegisterID, clipboard.Data{Text: sentinel}))
			e := env{comp: comp, clip: clip, mouse: mouse.New(&mouseDriver{t: comp, clipboard: clip})}
			for _, s := range tc.steps {
				s(t, e)
			}

			pasted, err := clip.Paste(clipboard.DefaultRegisterID)
			require.NoError(t, err)
			assert.Equal(t, tc.want, pasted.Text)
			assert.Equal(t, tc.wantScroll, comp.scrollY())
		})
	}
}

// TestHandlerMouseTrackingKeepsScrollback asserts that a drag through the
// top rows belongs to a program tracking the mouse on the primary screen
// (fzf --height): it neither scrolls the scrollback nor highlights.
func TestHandlerMouseTrackingKeepsScrollback(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		modes []vteparser.PrivateMode
	}{
		{"click tracking", []vteparser.PrivateMode{
			vteparser.PrivateModeReportMouseClicks, vteparser.PrivateModeSgrMouse}},
		{"button-event tracking", []vteparser.PrivateMode{
			vteparser.PrivateModeReportCellMouseMotion, vteparser.PrivateModeSgrMouse}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			comp, err := NewComponent(&testExecutor{}, &testExecutor{}, &mockTabManager{}, DefaultConfig())
			require.NoError(t, err)
			comp.Resize(16, 6)
			writeToBuffer(comp.parserHandler, "s0\ns1\ns2\ns3\ns4\ns5\nhello world\nfoo_bar")
			for _, mode := range tc.modes {
				comp.parserHandler.SetPrivateMode(mode)
			}
			d := &mouseDriver{t: comp, clipboard: clipboard.NewInMemory()}
			h := &Handler{comp: comp, mouseDriver: d, mouse: mouse.New(d)}

			for _, ev := range []term.Event{
				mouseEv(term.MouseLeft, 1, 1),
				mouseEv(term.MouseLeft, 1, 1),
				mouseEv(term.MouseLeft, 3, 0),
				mouseEv(term.MouseLeft, 5, 0),
				mouseEv(term.MouseRelease, 5, 0),
			} {
				h.handleInput(ev, keyEncoding{})
			}

			assert.Equal(t, 0, comp.scrollY())
			sel, ok := comp.Selection()
			assert.False(t, ok, "unexpected highlight %q", sel)
		})
	}
}

// TestMouseDriverDragScrollsDown drags through a scrolled-back terminal
// with more history than fits on screen. The SDK's bottom auto-scroll zone
// is the last rows of the pane, so a drag into it must reveal the next
// lines and extend the highlight over them.
func TestMouseDriverDragScrollsDown(t *testing.T) {
	t.Parallel()

	const scrollBack = 10
	// At scrollBack, window row 4 shows r16.
	press := mouseEv(term.MouseLeft, 1, 4)

	cases := []struct {
		desc       string
		drag       []term.Event
		want       string // clipboard after release
		wantScroll int
	}{
		{
			desc: "drag into the bottom rows",
			drag: []term.Event{
				mouseEv(term.MouseLeft, 1, 5),
				mouseEv(term.MouseLeft, 1, 6),
				mouseEv(term.MouseLeft, 1, 7),
			},
			want:       "16       \nr17       \nr18       \nr19       \nr20       \nr2",
			wantScroll: scrollBack - 3,
		},
		{
			desc:       "drag above the bottom rows",
			drag:       []term.Event{mouseEv(term.MouseLeft, 3, 4)},
			want:       "16 ",
			wantScroll: scrollBack,
		},
	}

	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()
			comp, err := NewComponent(&testExecutor{}, &testExecutor{}, &mockTabManager{}, DefaultConfig())
			require.NoError(t, err)
			comp.parserHandler.sync.primBuf.SetDefaultChar(' ')
			comp.Resize(10, 8)
			lines := make([]string, 30)
			for i := range lines {
				lines[i] = fmt.Sprintf("r%02d", i)
			}
			writeToBuffer(comp.parserHandler, strings.Join(lines, "\n"))
			require.True(t, comp.ScrollUp(scrollBack))
			require.Equal(t, scrollBack, comp.scrollY())

			clip := clipboard.NewInMemory()
			m := mouse.New(&mouseDriver{t: comp, clipboard: clip})
			m.Handle(press)
			for _, ev := range tc.drag {
				m.Handle(ev)
			}
			m.Handle(mouseEv(term.MouseRelease, 1, 7))

			assert.Equal(t, tc.wantScroll, comp.scrollY())
			pasted, err := clip.Paste(clipboard.DefaultRegisterID)
			require.NoError(t, err)
			assert.Equal(t, tc.want, pasted.Text)
		})
	}
}

// TestMouseDriverDragScrollsTowardSelection pins that a drag through a
// scrolled-back terminal only auto-scrolls toward the edge the selection
// grows to, which relies on ScrollUp and ScrollDown reporting whether the
// view moved.
func TestMouseDriverDragScrollsTowardSelection(t *testing.T) {
	t.Parallel()

	const (
		height     = 12
		scrollBack = 10
	)
	// At scrollBack, window row 0 shows r18 and row 11 shows r29.
	rowsDown := func(from, to int) []int {
		var ys []int
		for y := from; y <= to; y++ {
			ys = append(ys, y)
		}
		return ys
	}
	lines := func(first, last int, lastCols int) string {
		var b strings.Builder
		for i := first; i <= last; i++ {
			line := fmt.Sprintf("r%02d       ", i)
			if i == first {
				line = line[1:]
			}
			if i == last {
				line = line[:lastCols]
			} else {
				line += "\n"
			}
			b.WriteString(line)
		}
		return b.String()
	}

	cases := []struct {
		desc       string
		press      int
		drag       []int
		wantScroll int
		wantCopy   string // clipboard after release, unchecked when empty
	}{
		{
			// Only the bottom rows scroll: 9, 10 and 11 reveal r30-r32.
			// Each scroll follows the selection update, so the last
			// revealed line is selected only by the next move.
			desc:       "press in the top rows and drag down",
			press:      0,
			drag:       rowsDown(0, height-1),
			wantScroll: scrollBack - 3,
			wantCopy:   lines(18, 31, 3),
		},
		{
			// The first scroll moves the pressed line to row 4, so the
			// pointer back on row 3 is still above it and keeps scrolling.
			desc:       "anchor follows the view while scrolling up",
			press:      3,
			drag:       []int{2, 3},
			wantScroll: scrollBack + 2,
		},
	}

	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()
			comp, err := NewComponent(&testExecutor{}, &testExecutor{}, &mockTabManager{}, DefaultConfig())
			require.NoError(t, err)
			comp.parserHandler.sync.primBuf.SetDefaultChar(' ')
			comp.Resize(10, height)
			buf := make([]string, 40)
			for i := range buf {
				buf[i] = fmt.Sprintf("r%02d", i)
			}
			writeToBuffer(comp.parserHandler, strings.Join(buf, "\n"))
			require.True(t, comp.ScrollUp(scrollBack))

			clip := clipboard.NewInMemory()
			m := mouse.New(&mouseDriver{t: comp, clipboard: clip})
			m.Handle(mouseEv(term.MouseLeft, 1, tc.press))
			for _, y := range tc.drag {
				m.Handle(mouseEv(term.MouseLeft, 2, y))
			}

			assert.Equal(t, tc.wantScroll, comp.scrollY())
			if tc.wantCopy == "" {
				return
			}
			m.Handle(mouseEv(term.MouseRelease, 2, tc.drag[len(tc.drag)-1]))
			pasted, err := clip.Paste(clipboard.DefaultRegisterID)
			require.NoError(t, err)
			assert.Equal(t, tc.wantCopy, pasted.Text)
		})
	}
}

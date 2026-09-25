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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/mouse"
	"github.com/unstablebuild/rune-go-sdk/term"
)

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
// highlights and copies as before. While the running program tracks the
// mouse, drags belong to it and the terminal must not highlight at all.
func TestMouseDriverSelectionCopy(t *testing.T) {
	t.Parallel()

	const sentinel = "previously-copied"
	press := term.Coordinates{X: 1}

	cases := []struct {
		desc       string
		trackMouse bool
		drag       []term.Coordinates
		want       string // expected highlight, empty for none
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
			// A direct jump to the final cell hides a drag that sticks
			// after the first cell boundary. Walk one cell at a time.
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
		{
			desc:       "program tracking the mouse owns the drag",
			trackMouse: true,
			drag:       []term.Coordinates{press, {X: 4}},
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
			p.modeReportMouseClicks = tc.trackMouse

			clip := &recordingClipboard{Register: clipboard.NewInMemory()}
			require.NoError(t, clip.Register.Copy(
				clipboard.DefaultRegisterID, clipboard.Data{Text: sentinel}))
			driver := &mouseDriver{t: comp, clipboard: clip}

			// Mirror mouse.Mouse.handleLeftClickSelect: the press clears and
			// anchors, then every held-button event calls SetSelectionEnd.
			if !tc.trackMouse {
				driver.ClearSelection()
				driver.SetSelectionStart(press)
			}
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
			if !tc.trackMouse {
				driver.OnAction(term.Event{}, term.Coordinates{}, mouse.Release)
			}

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

// TestMouseDriverDragMotionReported pins that a program tracking button
// motion receives the drag, and the terminal does not paint its own
// highlight over that drag.
func TestMouseDriverDragMotionReported(t *testing.T) {
	t.Parallel()

	tm := mockTabManager{}
	comp, err := NewComponent(&testExecutor{}, &testExecutor{}, &tm, DefaultConfig())
	require.NoError(t, err)
	p := comp.parserHandler
	p.sync.primBuf.SetDefaultChar(' ')
	comp.Resize(16, 3)
	writeToBuffer(p, "hello world\nfoo_bar")
	p.modeReportCellMouseMotion = true
	p.modeSgrMouse = true

	driver := &mouseDriver{t: comp, clipboard: clipboard.NewInMemory(), lastButton: 0}
	driver.SetSelectionEnd(term.Coordinates{X: 4, Y: 1})

	assert.Equal(t, "\x1b[<32;5;2M", string(driver.hookRawBytes))
	_, ok := comp.Selection()
	assert.False(t, ok)
}

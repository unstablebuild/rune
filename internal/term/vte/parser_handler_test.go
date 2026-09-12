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
	"bytes"
	"encoding/base64"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component"
	"unstable.build/rune/internal/term/vte/vteparser"
	"unstable.build/rune/internal/term/vte/vtescreen"
	"unstable.build/rune/internal/workspace/workspacetest"
)

func TestIntegrationParserHandler(t *testing.T) {
	t.Parallel()
	testURI, err := workspaceapi.ParseURI("memory:///radical")
	require.NoError(t, err)

	suite := []struct {
		desc      string
		altBuffer bool
		sut       func(*testing.T, *parserHandler, *mockTabManager, *workspacetest.File)
	}{
		{
			desc:      "primary input after carriage return and line feed",
			altBuffer: false,
			sut: func(t *testing.T, p *parserHandler, tm *mockTabManager, pty *workspacetest.File) {
				p.Resize(5, 5)
				p.Input('a')
				p.CarriageReturn()
				p.Linefeed()
				p.Input('b')
				assertEqualBuf(t, p, "a    \nb    \n     \n     \n     ")
			},
		},
		{
			desc:      "alt input after carriage return and line feed",
			altBuffer: true,
			sut: func(t *testing.T, p *parserHandler, tm *mockTabManager, pty *workspacetest.File) {
				p.Resize(5, 5)
				p.Input('a')
				p.CarriageReturn()
				p.Linefeed()
				p.Input('b')
				assertEqualBuf(t, p, "a    \nb    \n     \n     \n     ")
			},
		},
		{
			desc:      "primary input after clear right of line and goto",
			altBuffer: false,
			sut: func(t *testing.T, p *parserHandler, tm *mockTabManager, pty *workspacetest.File) {
				p.Resize(5, 5)
				p.Input('a')
				p.Goto(0, 1)
				p.ClearLine(vteparser.LineClearModeRight)
				p.Goto(1, 0)
				p.Input('b')
				assertEqualBuf(t, p, "a    \nb    \n     \n     \n     ")
			},
		},
		{
			desc:      "alt input after clear right of line and goto",
			altBuffer: true,
			sut: func(t *testing.T, p *parserHandler, tm *mockTabManager, pty *workspacetest.File) {
				p.Resize(5, 5)
				p.Input('a')
				p.Goto(0, 1)
				p.ClearLine(vteparser.LineClearModeRight)
				p.Goto(1, 0)
				p.Input('b')
				assertEqualBuf(t, p, "a    \nb    \n     \n     \n     ")
			},
		},
		{
			desc:      "scrolling region change + linefeed scrolls up on margin",
			altBuffer: true,
			sut: func(t *testing.T, p *parserHandler, tm *mockTabManager, pty *workspacetest.File) {
				p.Resize(5, 5)
				resetBuffer(t, p, "a    \nb    \n     \n     \n     ")

				p.SetScrollingRegion(2, 4, false)
				p.Goto(3, 0)
				p.CarriageReturn()
				p.Linefeed()
				p.SetScrollingRegion(1, 5, false)
				assertEqualBuf(t, p, "a    \n     \n     \n     \n     ")
			},
		},
		{
			desc:      "vi delete a line 'dd'",
			altBuffer: true,
			sut: func(t *testing.T, p *parserHandler, tm *mockTabManager, pty *workspacetest.File) {
				p.Resize(5, 5)
				resetBuffer(t, p, "a    \nb    \n     \n     \n     ")

				p.SetScrollingRegion(2, 4, false)
				p.Goto(3, 0)
				p.CarriageReturn()
				p.Linefeed()
				p.SetScrollingRegion(1, 5, false)
				p.Goto(3, 0)
				p.Input(' ')
				p.Input(' ')
				p.Input(' ')
				p.Input(' ')
				p.Input(' ')
				p.Goto(4, 0)
				p.ClearLine(vteparser.LineClearModeRight)
				p.Goto(1, 0)
				assertEqualBuf(t, p, "a    \n     \n     \n     \n     ")
			},
		},
		{
			desc:      "vi visual delete multiple lines'",
			altBuffer: true,
			sut: func(t *testing.T, p *parserHandler, tm *mockTabManager, pty *workspacetest.File) {
				p.Resize(5, 6)
				resetBuffer(t, p, "a    \nb    \nc    \nd    \ne    \n     ")

				p.UnsetPrivateMode(25)
				// do not scroll command bar
				p.SetScrollingRegion(1, 5, false)
				p.Goto(0, 0)
				p.DeleteLines(2)
				p.SetScrollingRegion(1, 6, false)
				p.Goto(3, 0)
				p.Input('X')
				p.CarriageReturn()
				p.Linefeed()
				p.Input('Y')
				p.Goto(0, 0)
				p.SetPrivateMode(25)

				assertEqualBuf(t, p, "c    \nd    \ne    \nX    \nY    \n     ")
			},
		},
		{
			desc:      "primary scroll down non-capped",
			altBuffer: false,
			sut: func(t *testing.T, p *parserHandler, tm *mockTabManager, pty *workspacetest.File) {
				p.Resize(5, 5)
				resetBuffer(t, p, "a    \nb    \nc    \nd    \ne    \nf    ")
				assertEqualBuf(t, p, "b    \nc    \nd    \ne    \nf    ")
				p.ScrollDown(100)
				assertEqualBuf(t, p, "     \n     \n     \n     \n     ")
			},
		},
		{
			desc:      "primary delete lines (ctrl-r on plain zsh)",
			altBuffer: false,
			sut: func(t *testing.T, p *parserHandler, tm *mockTabManager, pty *workspacetest.File) {
				p.Resize(5, 5)
				resetBuffer(t, p, "a    \nb    \nc    \nd    \ne    \nf    ")
				assertEqualBuf(t, p, "b    \nc    \nd    \ne    \nf    ")
				p.Backspace()
				p.Input('X')
				p.MoveDown(1)
				p.CarriageReturn()
				p.ClearLine(0)
				p.MoveDown(1)
				p.Input('Y')
				p.ClearLine(0)
				p.MoveUp(1)
				p.MoveUp(1)
				p.MoveForward(32)
				p.MoveForward(3)
				p.Input('Z')
				p.MoveDown(2)
				p.MoveBackward(38)
				p.Input('1')
				p.MoveUp(1)
				p.MoveUp(1)
				p.MoveForward(30)
				// sut
				p.MoveBackward(19)
				p.Input('2')
				p.MoveDown(1)
				p.CarriageReturn()
				p.DeleteLines(1)

				assertEqualBuf(t, p, "b    \nc    \n2   Z\ne    \n1    ")
			},
		},
		{
			desc:      "primary scroll up non-capped",
			altBuffer: false,
			sut: func(t *testing.T, p *parserHandler, tm *mockTabManager, pty *workspacetest.File) {
				p.Resize(5, 5)
				resetBuffer(t, p, "a    \nb    \nc    \nd    \ne    \nf    ")
				assertEqualBuf(t, p, "b    \nc    \nd    \ne    \nf    ")
				p.ScrollDown(100)
				assertEqualBuf(t, p, "     \n     \n     \n     \n     ")
				p.ScrollUp(2)
				assertEqualBuf(t, p, "     \n     \n     \n     \n     ")
				p.ScrollUp(100)
				assertEqualBuf(t, p, "     \n     \n     \n     \n     ")
				p.ScrollDown(1)
				assertEqualBuf(t, p, "     \n     \n     \n     \n     ")
			},
		},
		{
			desc:      "alternate scroll down 0 rows does nothing",
			altBuffer: true,
			sut: func(t *testing.T, p *parserHandler, tm *mockTabManager, pty *workspacetest.File) {
				p.Resize(5, 5)
				resetBuffer(t, p, "a    \nb    \nc    \nd    \ne    ")
				p.ScrollDown(0)
				assertEqualBuf(t, p, "a    \nb    \nc    \nd    \ne    ")
			},
		},
		{
			desc:      "alternate scroll up 0 rows does nothing",
			altBuffer: true,
			sut: func(t *testing.T, p *parserHandler, tm *mockTabManager, pty *workspacetest.File) {
				p.Resize(5, 5)
				resetBuffer(t, p, "a    \nb    \nc    \nd    \ne    ")
				p.ScrollUp(0)
				assertEqualBuf(t, p, "a    \nb    \nc    \nd    \ne    ")
			},
		},
		{
			desc:      "shell scroll back, then write next command",
			altBuffer: false,
			sut: func(t *testing.T, p *parserHandler, tm *mockTabManager, pty *workspacetest.File) {
				p.Resize(5, 5)
				resetBuffer(t, p, "a    \nb    \nc    \nd    \ne    \n$ .  ")
				assertEqualBuf(t, p, "b    \nc    \nd    \ne    \n$ .  ")
				p.CarriageReturn()
				p.CarriageReturn()
				p.Linefeed()
				p.Input('o')
				p.Input('u')
				p.Input('t')
				p.CarriageReturn()
				p.Linefeed()
				p.CarriageReturn()
				p.Linefeed()
				p.ClearScreen(vteparser.ClearModeBelow)
				p.Input('$')
				p.Input(' ')
				p.ClearLine(vteparser.LineClearModeRight)
				assertEqualBuf(t, p, "e    \n$ .  \nout  \n     \n$    ")
			},
		},
		{
			desc:      "shell resize + move up does not oob",
			altBuffer: false,
			sut: func(t *testing.T, p *parserHandler, tm *mockTabManager, pty *workspacetest.File) {
				p.Resize(5, 5)
				resetBuffer(t, p, "a    \nb    \nc    \nd    \ne    \n$ .  ")
				assertEqualBuf(t, p, "b    \nc    \nd    \ne    \n$ .  ")
				p.Resize(4, 4)
				p.ClearScreen(vteparser.ClearModeAll)
				p.Goto(0, 0)
				p.Input('$')
				assertEqualBuf(t, p, "$   \n    \n    \n    ")
			},
		},
		{
			desc:      "shell cltr-l exactly all screen",
			altBuffer: false,
			sut: func(t *testing.T, p *parserHandler, tm *mockTabManager, pty *workspacetest.File) {
				p.Resize(5, 5)
				resetBuffer(t, p, "a    \nb    \nc    \nd    \ne    \n$ .  ")
				p.Goto(0, 0)
				p.ClearScreen(vteparser.ClearModeAll)
				p.ClearScreen(vteparser.ClearModeBelow)
				p.Input('$')
				p.ClearLine(vteparser.LineClearModeRight)
				assertEqualBuf(t, p, "$    \n     \n     \n     \n     ")
			},
		},
		{
			desc:      "shell input wrap around and scroll down",
			altBuffer: false,
			sut: func(t *testing.T, p *parserHandler, tm *mockTabManager, pty *workspacetest.File) {
				p.Resize(5, 5)
				resetBuffer(t, p, "a    \nb    \nc    \nd    \ne    \n$ .  \nout  \n$    ")
				assertEqualBuf(t, p, "d    \ne    \n$ .  \nout  \n$    ")
				p.Linefeed()
				p.CarriageReturn()
				for range 7 {
					p.Input('a')
				}
				assertEqualBuf(t, p, "$ .  \nout  \n$    \naaaaa\naa   ")
			},
		},
		{
			desc:      "shell input un-wrap after resize",
			altBuffer: false,
			sut: func(t *testing.T, p *parserHandler, tm *mockTabManager, pty *workspacetest.File) {
				p.Resize(5, 5)
				resetBuffer(t, p, "a    \nb    \nc    \nd    \ne    \n$ .  \nout  \n$    ")
				p.Linefeed()
				p.CarriageReturn()
				for range 7 {
					p.Input('a')
				}
				assertEqualBuf(t, p, "$ .  \nout  \n$    \naaaaa\naa   ")
				p.Resize(6, 5)
				assertEqualBuf(t, p, "$ .   \nout   \n$     \naaaaaa\na     ")
				p.Resize(4, 5)
				assertEqualBuf(t, p, "$ . \nout \n$   \naaaa\naaa ")
				p.Resize(9, 5)
				assertEqualBuf(t, p, "e        \n$ .      \nout      \n$        \naaaaaaa  ")
			},
		},
		{
			desc:      "shell input un-wrap of multiple lines after resize",
			altBuffer: false,
			sut: func(t *testing.T, p *parserHandler, tm *mockTabManager, pty *workspacetest.File) {
				p.Resize(5, 5)
				resetBuffer(t, p, "a    \nb    \nc    \nd    \ne    \n$ .  \nout  \n$    ")
				p.Linefeed()
				p.CarriageReturn()
				for range 7 {
					p.Input('a')
				}
				p.Linefeed()
				p.CarriageReturn()
				for range 7 {
					p.Input('b')
				}
				assertEqualBuf(t, p, "$    \naaaaa\naa   \nbbbbb\nbb   ")
				p.Resize(6, 5)
				assertEqualBuf(t, p, "$     \naaaaaa\na     \nbbbbbb\nb     ")
				p.Resize(3, 5)
				assertEqualBuf(t, p, "aaa\na  \nbbb\nbbb\nb  ")
				p.Resize(9, 5)
				assertEqualBuf(t, p, "$ .      \nout      \n$        \naaaaaaa  \nbbbbbbb  ")
			},
		},
		{
			desc:      "shell input un-wrap of multiple lines after resize, after hit max scroll length",
			altBuffer: false,
			sut: func(t *testing.T, p *parserHandler, tm *mockTabManager, pty *workspacetest.File) {
				p.maxScrollLength = 6
				p.ResetState()
				p.Resize(5, 5)
				resetBuffer(t, p, "c    \nd    \ne    \n$ .  \nout  \n$    ")

				for range 6 {
					p.Linefeed()
					p.CarriageReturn()
					for range 7 {
						p.Input('a')
					}
					p.Linefeed()
					p.CarriageReturn()
					for range 7 {
						p.Input('b')
					}
				}
				assertEqualBuf(t, p, "bb   \naaaaa\naa   \nbbbbb\nbb   ")
				p.Resize(6, 5)
				assertEqualBuf(t, p, "b     \naaaaaa\na     \nbbbbbb\nb     ")
				p.Resize(3, 5)
				assertEqualBuf(t, p, "aaa\na  \nbbb\nbbb\nb  ")
				p.Resize(9, 5)
				assertEqualBuf(t, p, "bbbbbbb  \naaaaaaa  \nbbbbbbb  \n         \n         ")
			},
		},
		{
			desc:      "primary resize maintains cursor position at content",
			altBuffer: false,
			sut: func(t *testing.T, p *parserHandler, tm *mockTabManager, pty *workspacetest.File) {
				p.Resize(5, 5)
				resetBuffer(t, p, "a    \nb    \nc    \nd    \ne    \n$ .  \nout  \n$    ")
				assert.Equal(t, term.Coordinates{Y: 7, X: 4}, p.sync.buf.CursorAtScroll())
				assert.Equal(t, term.Coordinates{Y: 4, X: 4}, p.sync.buf.CursorAtScreen())

				p.Resize(1, 1)
				// content was wrapped, CursorAtScroll doesn't take that into consideration
				assert.Equal(t, term.Coordinates{Y: 11, X: 4}, p.sync.buf.CursorAtScroll())
				assert.Equal(t, term.Coordinates{Y: 0, X: 4}, p.sync.buf.CursorAtScreen())

				p.Resize(5, 5)
				assert.Equal(t, term.Coordinates{Y: 7, X: 4}, p.sync.buf.CursorAtScroll())
				assert.Equal(t, term.Coordinates{Y: 4, X: 4}, p.sync.buf.CursorAtScreen())

				p.Resize(0, 0)
				assert.Equal(t, term.Coordinates{Y: 7, X: 4}, p.sync.buf.CursorAtScroll())
				assert.Equal(t, term.Coordinates{Y: -1, X: 4}, p.sync.buf.CursorAtScreen())

				p.Resize(5, 5)
				assert.Equal(t, term.Coordinates{Y: 7, X: 4}, p.sync.buf.CursorAtScroll())
				assert.Equal(t, term.Coordinates{Y: 4, X: 4}, p.sync.buf.CursorAtScreen())
			},
		},
		{
			desc:      "alternate resize maintains cursor position at content",
			altBuffer: true,
			sut: func(t *testing.T, p *parserHandler, tm *mockTabManager, pty *workspacetest.File) {
				p.Resize(5, 5)
				resetBuffer(t, p, "a    \nb    \nc    \nd    \ne    ")
				assert.Equal(t, term.Coordinates{Y: 4, X: 4}, p.sync.buf.CursorAtScroll())
				assert.Equal(t, term.Coordinates{Y: 4, X: 4}, p.sync.buf.CursorAtScreen())

				p.Resize(1, 1)
				assert.Equal(t, term.Coordinates{Y: 4, X: 4}, p.sync.buf.CursorAtScroll())
				assert.Equal(t, term.Coordinates{Y: 4, X: 4}, p.sync.buf.CursorAtScreen())

				p.Resize(5, 5)
				assert.Equal(t, term.Coordinates{Y: 4, X: 4}, p.sync.buf.CursorAtScroll())
				assert.Equal(t, term.Coordinates{Y: 4, X: 4}, p.sync.buf.CursorAtScreen())

				p.Resize(0, 0)
				assert.Equal(t, term.Coordinates{Y: 4, X: 4}, p.sync.buf.CursorAtScroll())
				assert.Equal(t, term.Coordinates{Y: 4, X: 4}, p.sync.buf.CursorAtScreen())

				p.Resize(5, 5)
				assert.Equal(t, term.Coordinates{Y: 4, X: 4}, p.sync.buf.CursorAtScroll())
				assert.Equal(t, term.Coordinates{Y: 4, X: 4}, p.sync.buf.CursorAtScreen())
			},
		},
		{
			desc:      "primary resize negative does not panic",
			altBuffer: false,
			sut: func(t *testing.T, p *parserHandler, tm *mockTabManager, pty *workspacetest.File) {
				p.Resize(-1, -1)
			},
		},
		{
			desc:      "alternate resize negative does not panic",
			altBuffer: true,
			sut: func(t *testing.T, p *parserHandler, tm *mockTabManager, pty *workspacetest.File) {
				p.Resize(-1, -1)
			},
		},
		{
			desc:      "primary clear mode above",
			altBuffer: false,
			sut: func(t *testing.T, p *parserHandler, tm *mockTabManager, pty *workspacetest.File) {
				p.Resize(5, 5)
				resetBuffer(t, p, "a    \nb    \nc    \nd    \ne    \n$ .  \nout  \n$    ")
				assertEqualBuf(t, p, "d    \ne    \n$ .  \nout  \n$    ")
				assert.Equal(t, term.Coordinates{Y: 4, X: 4}, p.sync.buf.CursorAtScreen())
				p.ClearScreen(vteparser.ClearModeAbove)
				assertEqualBuf(t, p, "     \n     \n     \n     \n     ")
			},
		},
		{
			desc:      "primary clear mode saved with no history does nothing",
			altBuffer: false,
			sut: func(t *testing.T, p *parserHandler, tm *mockTabManager, pty *workspacetest.File) {
				p.Resize(5, 5)
				resetBuffer(t, p, "a    \nb    \nc    \nd    \ne    ")

				p.ClearScreen(vteparser.ClearModeSaved)
				assertEqualBuf(t, p, "a    \nb    \nc    \nd    \ne    ")
			},
		},
		{
			desc:      "secondary clear mode saved, does nothing, because there's no history",
			altBuffer: true,
			sut: func(t *testing.T, p *parserHandler, tm *mockTabManager, pty *workspacetest.File) {
				p.Resize(5, 5)
				resetBuffer(t, p, "a    \nb    \nc    \nd    \ne    ")
				assertEqualBuf(t, p, "a    \nb    \nc    \nd    \ne    ")

				p.ScrollDown(1)
				assertEqualBuf(t, p, "     \na    \nb    \nc    \nd    ")

				p.ScrollUp(1)
				assertEqualBuf(t, p, "a    \nb    \nc    \nd    \n     ")

				p.ClearScreen(vteparser.ClearModeSaved)
				assertEqualBuf(t, p, "a    \nb    \nc    \nd    \n     ")

				p.ScrollDown(1)
				assertEqualBuf(t, p, "     \na    \nb    \nc    \nd    ")
			},
		},
		{
			desc:      "unknown clipboard does nothing",
			altBuffer: false,
			sut: func(t *testing.T, p *parserHandler, tm *mockTabManager, pty *workspacetest.File) {
				p.Resize(5, 5)

				data := base64Data(t, "1234\n5678@hello")

				p.ClipboardStore(1, data)
				p.ClipboardLoad(1, "TERM")
				require.Len(t, pty.Writes, 0)
			},
		},
		{
			desc:      "clipboard load/store",
			altBuffer: false,
			sut: func(t *testing.T, p *parserHandler, tm *mockTabManager, pty *workspacetest.File) {
				p.Resize(5, 5)

				data := base64Data(t, "1234\n5678@hello")

				p.ClipboardStore(int('c'), data)

				p.ClipboardLoad(int('p'), "TERM")
				require.Len(t, pty.Writes, 0)

				p.ClipboardLoad(int('c'), "TERM")
				assertWriteToPty(t, pty, fmt.Sprintf("\x1b]52;c;%sTERM", data))
			},
		},
		{
			desc:      "sh ls usage of put tab",
			altBuffer: false,
			sut: func(t *testing.T, p *parserHandler, tm *mockTabManager, pty *workspacetest.File) {
				p.Resize(48, 2)
				resetBuffer(t, p, "sh-3.2$ ls                                      \n                                                ")

				p.CarriageReturn()

				for _, ch := range "LICENSE" {
					p.Input(ch)
				}
				p.PutTab()
				p.PutTab()
				for _, ch := range "cpu.out" {
					p.Input(ch)
				}
				p.PutTab()
				p.PutTab()
				for _, ch := range "plugin" {
					p.Input(ch)
				}
				assertEqualBuf(t, p, "sh-3.2$ ls                                      \n"+
					"LICENSE         cpu.out         plugin          ")

			},
		},
		{
			desc:      "reverse index usage of git log on primary buffer",
			altBuffer: false,
			sut: func(t *testing.T, p *parserHandler, tm *mockTabManager, pty *workspacetest.File) {
				p.Resize(5, 5)
				resetBuffer(t, p, "a    \nb    \nc    \nd    \n:    ")
				p.CarriageReturn()
				p.ClearLine(0)
				p.Goto(0, 0)
				p.ReverseIndex()
				p.Input('X')
				p.CarriageReturn()
				p.Linefeed()
				p.Goto(4, 0)
				p.CarriageReturn()
				p.ClearLine(0)
				p.Input(':')
				p.ClearLine(0)
				assertEqualBuf(t, p, "X    \na    \nb    \nc    \n:    ")
			},
		},
		{
			// Regression: when a program sets a non-default cursor (e.g.
			// an input box bar) and then resets it via DECSCUSR 0 on exit,
			// the parser must restore CursorStyleDefault (rendered as a bar
			// on the primary buffer) instead of leaving a block.
			desc:      "DECSCUSR 0 resets steady bar to terminal default",
			altBuffer: false,
			sut: func(t *testing.T, p *parserHandler, tm *mockTabManager, pty *workspacetest.File) {
				p.SetCursorStyle(vteparser.CursorStyle{
					Shape: vteparser.CursorShapeBeam, Blinking: false,
				})
				require.Equal(t, term.CursorStyleSteadyBar, p.cursorStyle)

				p.SetCursorStyle(vteparser.CursorStyle{
					Shape: vteparser.CursorShapeDefault, Blinking: false,
				})
				require.Equal(t, term.CursorStyleDefault, p.cursorStyle)
			},
		},
		{
			desc:      "DECSCUSR 2 sets steady block",
			altBuffer: false,
			sut: func(t *testing.T, p *parserHandler, tm *mockTabManager, pty *workspacetest.File) {
				p.SetCursorStyle(vteparser.CursorStyle{
					Shape: vteparser.CursorShapeBlock, Blinking: false,
				})
				require.Equal(t, term.CursorStyleSteadyBlock, p.cursorStyle)
			},
		},
		{
			desc:      "reverse index usage of git log on primary buffer with history",
			altBuffer: false,
			sut: func(t *testing.T, p *parserHandler, tm *mockTabManager, pty *workspacetest.File) {
				p.Resize(5, 5)
				resetBuffer(t, p, "com  \nlog  \na    \nb    \nc    \nd    \n:    ")
				assertEqualBuf(t, p, "a    \nb    \nc    \nd    \n:    ")
				p.CarriageReturn()
				p.ClearLine(0)
				p.Goto(0, 0)
				p.ReverseIndex()
				p.Input('X')
				p.CarriageReturn()
				p.Linefeed()
				p.Goto(4, 0)
				p.CarriageReturn()
				p.ClearLine(0)
				p.Input(':')
				p.ClearLine(0)
				assertEqualBuf(t, p, "X    \na    \nb    \nc    \n:    ")
			},
		},
		{
			desc:      "multiple reverse index after sefveral 'scroll down' on primary buffer with history",
			altBuffer: false,
			sut: func(t *testing.T, p *parserHandler, tm *mockTabManager, pty *workspacetest.File) {
				p.Resize(5, 5)
				resetBuffer(t, p, "com  \nlog  \na    \nb    \nc    \nd    \n:    ")
				assertEqualBuf(t, p, "a    \nb    \nc    \nd    \n:    ")

				// scroll down with git log
				p.Goto(4, 0)
				for i := range 3 {
					p.CarriageReturn()
					p.ClearLine(0)
					p.Input([]rune(strconv.Itoa(i))[0])
					p.CarriageReturn()
					p.Linefeed()
					p.Input(':')
					p.ClearLine(0)
				}
				assertEqualBuf(t, p, "d    \n0    \n1    \n2    \n:    ")

				for i := range 3 {
					p.CarriageReturn()
					p.ClearLine(0)
					p.Goto(0, 0)
					p.ReverseIndex()
					switch i {
					case 0:
						p.Input('c')
					case 1:
						p.Input('b')
					case 2:
						p.Input('a')
					}
					p.CarriageReturn()
					p.Linefeed()
					p.Goto(4, 0)
					p.CarriageReturn()
					p.ClearLine(0)
					p.Input(':')
					p.ClearLine(0)
				}
				assertEqualBuf(t, p, "a    \nb    \nc    \nd    \n:    ")
			},
		},
		{
			desc:      "zsh delete a character in vi mode",
			altBuffer: false,
			sut: func(t *testing.T, p *parserHandler, tm *mockTabManager, pty *workspacetest.File) {
				p.Resize(5, 5)
				resetBuffer(t, p, "log  \na    \nb    \nc    \nd    \n$ xaa")
				assertEqualBuf(t, p, "a    \nb    \nc    \nd    \n$ xaa")
				p.Backspace()
				p.Backspace()
				p.DeleteChars(1)
				p.MoveForward(2)
				p.Input(' ')
				p.MoveBackward(2)
				assertEqualBuf(t, p, "a    \nb    \nc    \nd    \n$ aa ")
				p.Input('X')
				assertEqualBuf(t, p, "a    \nb    \nc    \nd    \n$ Xa ")
			},
		},
		{
			desc:      "zsh delete a character in vi mode with wrap around line",
			altBuffer: false,
			sut: func(t *testing.T, p *parserHandler, tm *mockTabManager, pty *workspacetest.File) {
				p.Resize(5, 5)
				resetBuffer(t, p, "log  \na    \nb    \nc    \nd    \n$ xaa")
				assertEqualBuf(t, p, "a    \nb    \nc    \nd    \n$ xaa")
				p.Backspace()
				p.Backspace()
				p.DeleteChars(1)
				p.MoveForward(2)
				p.ClearLine(0)
				assertEqualBuf(t, p, "a    \nb    \nc    \nd    \n$ aa ")
				p.Input('X')
				assertEqualBuf(t, p, "a    \nb    \nc    \nd    \n$ aaX")
			},
		},
		{
			desc:      "bell is called if idle (first created)",
			altBuffer: false,
			sut: func(t *testing.T, p *parserHandler, tm *mockTabManager, pty *workspacetest.File) {
				assert.False(t, tm.belled)
				p.Bell()
				assert.True(t, tm.belled)
				assert.Zero(t, tm.setName)
				assert.Zero(t, tm.toUri)
				assert.Zero(t, tm.setAttr)
			},
		},
		{
			desc:      "bell is called if in focus",
			altBuffer: false,
			sut: func(t *testing.T, p *parserHandler, tm *mockTabManager, pty *workspacetest.File) {
				assert.False(t, tm.belled)
				p.onFocusChange(false)
				p.onFocusChange(true)
				p.Bell()
				assert.True(t, tm.belled)
				assert.Zero(t, tm.setName)
				assert.Zero(t, tm.toUri)
				assert.Zero(t, tm.setAttr)
			},
		},
		{
			desc:      "bell is called if not in focus, but needs attention attrs are set if mode urgency hints is set",
			altBuffer: false,
			sut: func(t *testing.T, p *parserHandler, tm *mockTabManager, pty *workspacetest.File) {
				assert.False(t, tm.belled)
				p.onFocusChange(false)
				p.Bell()
				assert.True(t, tm.belled)
				assert.Equal(t, testURI, tm.toUri)
				assert.Equal(t, "radical", tm.setName)
				assert.Equal(t, term.Attributes{Attrs: term.AttrBlink}, tm.setAttr)
			},
		},
		{
			desc:      "bell is called even if not in focus, and needs attention attrs are not set if mode urgency hints is disabled",
			altBuffer: false,
			sut: func(t *testing.T, p *parserHandler, tm *mockTabManager, pty *workspacetest.File) {
				assert.False(t, tm.belled)
				p.onFocusChange(false)
				p.UnsetPrivateMode(vteparser.PrivateModeUrgencyHints)
				p.Bell()
				assert.True(t, tm.belled)
				assert.Zero(t, tm.setName)
				assert.Zero(t, tm.toUri)
				assert.Zero(t, tm.setAttr)
			},
		},
		{
			desc:      "bell is called whether mode urgency hints is disabled or not",
			altBuffer: false,
			sut: func(t *testing.T, p *parserHandler, tm *mockTabManager, pty *workspacetest.File) {
				assert.False(t, tm.belled)
				p.onFocusChange(true)
				p.UnsetPrivateMode(vteparser.PrivateModeUrgencyHints)
				p.Bell()
				assert.True(t, tm.belled)
				assert.Zero(t, tm.setName)
				assert.Zero(t, tm.toUri)
				assert.Zero(t, tm.setAttr)
			},
		},
		{
			desc:      "needs attention attrs are cleared if on focus true is triggered",
			altBuffer: false,
			sut: func(t *testing.T, p *parserHandler, tm *mockTabManager, pty *workspacetest.File) {
				assert.False(t, tm.belled)
				p.onFocusChange(false)
				p.Bell()
				p.onFocusChange(true)
				assert.Equal(t, testURI, tm.toUri)
				assert.Equal(t, "radical", tm.setName)
				assert.Equal(t, term.Attributes{}, tm.setAttr)
			},
		},
	}

	for _, test := range suite {
		t.Run(test.desc, func(t *testing.T) {
			t.Parallel()
			mockPtyFile := workspacetest.File{}
			tm := mockTabManager{}
			attrs := DefaultConfig().NeedsAttentionAttributes
			pty := workspaceapi.Pty{Master: &mockPtyFile, Slave: &mockPtyFile}
			ph := newParserHandler(new(sync.Mutex), pty, &tm,
				clipboard.NewInMemory(), tm.bell, testURI, attrs, false, 10000, 0)
			ph.sync.primBuf.SetDefaultChar(' ')
			ph.sync.altBuf.SetDefaultChar(' ')

			if test.altBuffer {
				ph.SetPrivateMode(vteparser.PrivateModeSwapScreenAndSetRestoreCursor)
			} else {
				ph.UnsetPrivateMode(vteparser.PrivateModeSwapScreenAndSetRestoreCursor)
			}

			test.sut(t, ph, &tm, &mockPtyFile)
		})
	}
}

func assertEqualBuf(t *testing.T, p *parserHandler, expected string) {
	t.Helper()
	assertEqualScreenBuf(t, p.sync.buf, expected)
}

func assertEqualScreenBuf(t *testing.T, p screenBuffer, expected string) {
	t.Helper()
	if prim, ok := p.(*vtescreen.PrimaryBuffer); ok {
		width, height := prim.Dimensions()
		writer := term.NewStringWriter(width, height)
		prim.Draw(writer)
		writer.Flush()
		assert.Equal(t, expected, writer.String())
	} else {
		assert.Equal(t, expected, term.CellsToString(p.(*vtescreen.AltBuffer).Cells.RawCells()))
	}
}

func resetBuffer(t *testing.T, p *parserHandler, to string) {
	t.Helper()
	writeToBuffer(p, to)
	if prim, ok := p.sync.buf.(*vtescreen.PrimaryBuffer); ok {
		require.Equal(t, to, term.CellsToString(prim.Cells.RawCells()))
	} else {
		require.Equal(t, to, term.CellsToString(p.sync.buf.(*vtescreen.AltBuffer).Cells.RawCells()))
	}
}

func writeToBuffer(p *parserHandler, str string) {
	for _, ch := range str {
		if ch == '\n' {
			p.CarriageReturn()
			p.Linefeed()
		} else {
			p.Input(ch)
		}
	}
}

func assertWriteToPty(t *testing.T, pty *workspacetest.File, data string) {
	t.Helper()
	require.Len(t, pty.Writes, 1)
	assert.Equal(t, string(pty.Writes[0]), data)
}

func base64Data(t *testing.T, data string) []byte {
	var buf bytes.Buffer
	e := base64.NewEncoder(base64.StdEncoding, &buf)
	_, err := e.Write([]byte(data))
	require.NoError(t, err)
	require.NoError(t, e.Close())
	return buf.Bytes()
}

type mockTabManager struct {
	belled  bool
	toUri   workspaceapi.URI
	setName string
	setAttr term.Attributes
}

func (tm *mockTabManager) Tab(
	uri workspaceapi.URI, icon rune, name string, h browserapi.Handler,
) (
	browserapi.Handler, error,
) {
	panic("not in use")
}

func (tm *mockTabManager) SetTabName(uri workspaceapi.URI, name string, attr term.Attributes) error {
	tm.toUri = uri
	tm.setName = name
	tm.setAttr = attr
	return nil
}

func (tm *mockTabManager) OnTabExit(workspaceapi.URI) bool {
	return false
}

func (tm *mockTabManager) bell() {
	tm.belled = true
}

func newInputParserHandler(t *testing.T, alt bool) *parserHandler {
	t.Helper()
	testURI, err := workspaceapi.ParseURI("memory:///radical")
	require.NoError(t, err)
	mockPtyFile := workspacetest.File{}
	tm := mockTabManager{}
	attrs := DefaultConfig().NeedsAttentionAttributes
	pty := workspaceapi.Pty{Master: &mockPtyFile, Slave: &mockPtyFile}
	ph := newParserHandler(new(sync.Mutex), pty, &tm,
		clipboard.NewInMemory(), tm.bell, testURI, attrs, false, 10000, 0)
	ph.sync.primBuf.SetDefaultChar(' ')
	ph.sync.altBuf.SetDefaultChar(' ')
	if alt {
		ph.SetPrivateMode(vteparser.PrivateModeSwapScreenAndSetRestoreCursor)
	} else {
		ph.UnsetPrivateMode(vteparser.PrivateModeSwapScreenAndSetRestoreCursor)
	}
	return ph
}

// TestBellFocusChangeRace pins that Bell synchronizes with focus
// changes: Bell runs on the parse goroutine while onFocusChange runs
// under the component lock on the event-loop goroutine, so an unlocked
// Bell races on inFocus/needsAttention (caught by -race).
func TestBellFocusChangeRace(t *testing.T) {
	ph := newInputParserHandler(t, false)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 1000 {
			ph.Bell()
		}
	}()
	for i := range 1000 {
		ph.sync.mu.Lock()
		ph.onFocusChange(i%2 == 0)
		ph.sync.mu.Unlock()
	}
	<-done
}

// TestBellReleasesLockBeforeInvokingCallback pins that Bell releases
// ph.sync.mu before invoking the configured bell callback (in
// production, Config.scheduleBell -> ScheduleNextTick). Callers often
// run ScheduleNextTick synchronously under their own lock to model a
// single-threaded event loop (see exo_go_test.go's `schedule`). If
// Bell still held ph.sync.mu while calling into that foreign lock,
// any other parserHandler method invoked while the caller's lock is
// held (e.g. Draw/SetTitle) would deadlock against it — exactly the
// hang reproduced by TestExoSyntaxHighlightsOverlayScrolling.
func TestBellReleasesLockBeforeInvokingCallback(t *testing.T) {
	var extMu sync.Mutex
	extMu.Lock()

	bellEntered := make(chan struct{})
	bellFn := func() {
		close(bellEntered)
		extMu.Lock()
		defer extMu.Unlock()
	}

	testURI, err := workspaceapi.ParseURI("memory:///radical")
	require.NoError(t, err)
	mockPtyFile := workspacetest.File{}
	tm := mockTabManager{}
	attrs := DefaultConfig().NeedsAttentionAttributes
	pty := workspaceapi.Pty{Master: &mockPtyFile, Slave: &mockPtyFile}
	ph := newParserHandler(new(sync.Mutex), pty, &tm,
		clipboard.NewInMemory(), bellFn, testURI, attrs, false, 10000, 0)

	go ph.Bell()
	<-bellEntered

	lockAcquired := make(chan struct{})
	go func() {
		ph.SetTitle("probe")
		close(lockAcquired)
	}()

	select {
	case <-lockAcquired:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("ph.sync.mu is still held while Bell's callback is blocked on an " +
			"external lock: Bell must release its lock before invoking the bell " +
			"callback")
	}

	extMu.Unlock()
}

func firstRowCells(p *parserHandler) []term.Cell {
	if prim, ok := p.sync.buf.(*vtescreen.PrimaryBuffer); ok {
		return prim.Cells.RawCells()[0]
	}
	return p.sync.buf.(*vtescreen.AltBuffer).Cells.RawCells()[0]
}

// TestInputWideRuneSurvivesNextInput reproduces a wide (width-2) glyph
// being clobbered when the following glyph was printed: the cursor
// advanced by one column, landing on the wide glyph's second half, so the
// next write overwrote it. Both screen buffers must keep the wide glyph
// and place the next glyph after it.
func TestInputWideRuneSurvivesNextInput(t *testing.T) {
	for _, alt := range []bool{false, true} {
		name := "primary"
		if alt {
			name = "alt"
		}
		t.Run(name+"/wide then narrow", func(t *testing.T) {
			p := newInputParserHandler(t, alt)
			p.Resize(8, 2)
			p.Input('世')
			p.Input('a')
			cells := firstRowCells(p)
			require.GreaterOrEqual(t, len(cells), 2)
			assert.Equal(t, '世', cells[0].Ch)
			assert.Equal(t, uint8(2), cells[0].Width)
			assert.Equal(t, 'a', cells[1].Ch)
			assert.Equal(t, uint8(1), cells[1].Width)
		})
		t.Run(name+"/two wide", func(t *testing.T) {
			p := newInputParserHandler(t, alt)
			p.Resize(8, 2)
			p.Input('世')
			p.Input('界')
			cells := firstRowCells(p)
			require.GreaterOrEqual(t, len(cells), 2)
			assert.Equal(t, '世', cells[0].Ch)
			assert.Equal(t, uint8(2), cells[0].Width)
			assert.Equal(t, '界', cells[1].Ch)
			assert.Equal(t, uint8(2), cells[1].Width)
		})
		t.Run(name+"/narrow then wide then narrow", func(t *testing.T) {
			p := newInputParserHandler(t, alt)
			p.Resize(8, 2)
			p.Input('x')
			p.Input('世')
			p.Input('y')
			cells := firstRowCells(p)
			require.GreaterOrEqual(t, len(cells), 3)
			assert.Equal(t, 'x', cells[0].Ch)
			assert.Equal(t, '世', cells[1].Ch)
			assert.Equal(t, uint8(2), cells[1].Width)
			assert.Equal(t, 'y', cells[2].Ch)
		})
	}
}

// TestInputWideRuneWrapsAtRightMargin asserts a double-width glyph that
// cannot fit in the last column wraps to the next row whole rather than
// straddling the margin.
func TestInputWideRuneWrapsAtRightMargin(t *testing.T) {
	p := newInputParserHandler(t, false)
	p.Resize(5, 3)
	p.Input('世')
	p.Input('界')
	p.Input('中')
	assertEqualBuf(t, p, "世 界  \n中    \n     ")
}

// TestInputWideRuneKeepsRowWithinWidth reproduces the primary buffer
// growing one visual column past the terminal width: erase operations
// (EL/ED) pad a row with width-1 cells, and a wide glyph printed over
// one of them used to be overwritten in place, leaving the row summing
// to width+1 columns. Scroll.MaxOffset then allowed a spurious one-cell
// horizontal scroll of the vte view. A wide glyph must instead consume
// the cell(s) whose columns it covers.
func TestInputWideRuneKeepsRowWithinWidth(t *testing.T) {
	// mirrors how Component.Init/Resize wire the live view scroll.
	newViewScroll := func(p *parserHandler, width, height int) *component.Scroll {
		s := new(component.Scroll)
		s.InitPerformance(&p.sync.primBuf.Cells)
		s.InvertOffset = true
		s.SetTabspaces(1)
		s.Resize(width, height)
		return s
	}

	t.Run("wide glyph over erased cells", func(t *testing.T) {
		p := newInputParserHandler(t, false)
		p.Resize(10, 3)
		p.ClearLine(vteparser.LineClearModeAll)
		p.Input('世')

		scroll := newViewScroll(p, 10, 3)
		assert.Equal(t, 0, scroll.MaxOffset().X,
			"row overwritten by a wide glyph must not exceed the terminal width")
		assert.False(t, scroll.CanSeekRight())
	})

	t.Run("wide glyph over narrow glyphs consumes covered cell", func(t *testing.T) {
		p := newInputParserHandler(t, false)
		p.Resize(4, 3)
		for _, r := range "abcd" {
			p.Input(r)
		}
		p.CarriageReturn()
		p.Input('世')

		// 世 now covers the columns previously held by 'a' and 'b'.
		cells := firstRowCells(p)
		require.GreaterOrEqual(t, len(cells), 2)
		assert.Equal(t, '世', cells[0].Ch)
		assert.Equal(t, 'c', cells[1].Ch)
		scroll := newViewScroll(p, 4, 3)
		assert.Equal(t, 0, scroll.MaxOffset().X)
		assert.False(t, scroll.CanSeekRight())
	})

	t.Run("wide glyph redrawn in place is stable", func(t *testing.T) {
		p := newInputParserHandler(t, false)
		p.Resize(10, 3)
		p.ClearLine(vteparser.LineClearModeAll)
		p.Input('世')
		p.CarriageReturn()
		p.Input('世')

		cells := firstRowCells(p)
		require.NotEmpty(t, cells)
		assert.Equal(t, '世', cells[0].Ch)
		scroll := newViewScroll(p, 10, 3)
		assert.Equal(t, 0, scroll.MaxOffset().X)
	})

	t.Run("wide glyph over another wide glyph's first column", func(t *testing.T) {
		p := newInputParserHandler(t, false)
		p.Resize(6, 3)
		p.Input('a')
		p.Input('世')
		p.Input('x')
		p.CarriageReturn()
		p.Input('中')

		// 中 covers 'a' and the first column of 世; the leftover 世
		// column becomes blank and 'x' keeps its column.
		assertEqualBuf(t, p, "中  x  \n      \n      ")
		scroll := newViewScroll(p, 6, 3)
		assert.Equal(t, 0, scroll.MaxOffset().X)
	})
}

// TestResizeShrinkKeepsRowsWithinWidth reproduces the vte view allowing
// a spurious one-cell horizontal scroll after the terminal shrinks:
// shrinkColumns/growColumns iterated rows with `y > 0` and only handed
// the cursor row to wrapTopLines when the loop reached it, so with the
// cursor sitting on row 0 (a fresh shell), that row was never re-wrapped
// nor trimmed and kept its old width. Erase ops pad rows with width-1
// cells to the full terminal width, so a plain ASCII prompt row was
// enough to exceed the new width.
func TestResizeShrinkKeepsRowsWithinWidth(t *testing.T) {
	newViewScroll := func(p *parserHandler, width, height int) *component.Scroll {
		s := new(component.Scroll)
		s.InitPerformance(&p.sync.primBuf.Cells)
		s.InvertOffset = true
		s.SetTabspaces(1)
		s.Resize(width, height)
		return s
	}

	t.Run("erased row with cursor on first row", func(t *testing.T) {
		p := newInputParserHandler(t, false)
		p.Resize(10, 3)
		p.ClearLine(vteparser.LineClearModeAll)
		p.Input('a')
		p.Resize(9, 3)

		scroll := newViewScroll(p, 9, 3)
		assert.Equal(t, 0, scroll.MaxOffset().X,
			"shrinking must trim the cursor row to the new width")
		assert.False(t, scroll.CanSeekRight())
	})

	t.Run("grow rewraps line wrapped at the old width", func(t *testing.T) {
		p := newInputParserHandler(t, false)
		p.Resize(4, 3)
		for _, r := range "abcde" {
			p.Input(r)
		}
		p.Resize(6, 3)

		assertEqualBuf(t, p, "abcde \n      \n      ")
		scroll := newViewScroll(p, 6, 3)
		assert.Equal(t, 0, scroll.MaxOffset().X)
	})
}

// TestInputClustersCombiningSequences asserts codepoints that continue a
// grapheme cluster (ZWJ-joined emoji, skin-tone modifiers, variation
// selectors, combining marks) merge into the preceding cell instead of
// each consuming their own cell, while sequences that are genuinely
// separate graphemes stay in separate cells.
func TestInputClustersCombiningSequences(t *testing.T) {
	cases := []struct {
		name     string
		runes    []rune
		wantCh   rune
		wantComb []rune
		wantSep  bool // true: expect distinct cells, not a merge
	}{
		{
			name:     "zwj family",
			runes:    []rune{'\U0001F468', '\u200D', '\U0001F469', '\u200D', '\U0001F467'},
			wantCh:   '\U0001F468',
			wantComb: []rune{'\u200D', '\U0001F469', '\u200D', '\U0001F467'},
		},
		{
			name:     "skin tone modifier",
			runes:    []rune{'\U0001F91F', '\U0001F3FC'},
			wantCh:   '\U0001F91F',
			wantComb: []rune{'\U0001F3FC'},
		},
		{
			name:     "variation selector",
			runes:    []rune{'\u2764', '\uFE0F'},
			wantCh:   '\u2764',
			wantComb: []rune{'\uFE0F'},
		},
		{
			name:     "combining mark",
			runes:    []rune{'e', '\u0301'},
			wantCh:   'e',
			wantComb: []rune{'\u0301'},
		},
		{name: "ascii pair stays separate", runes: []rune{'a', 'b'}, wantSep: true},
		{name: "wide pair stays separate", runes: []rune{'世', '界'}, wantSep: true},
	}
	for _, tc := range cases {
		for _, alt := range []bool{false, true} {
			bufName := "primary"
			if alt {
				bufName = "alt"
			}
			t.Run(tc.name+"/"+bufName, func(t *testing.T) {
				p := newInputParserHandler(t, alt)
				p.Resize(12, 2)
				for _, r := range tc.runes {
					p.Input(r)
				}
				cells := firstRowCells(p)
				require.NotEmpty(t, cells)
				if tc.wantSep {
					require.GreaterOrEqual(t, len(cells), 2)
					assert.Equal(t, tc.runes[0], cells[0].Ch)
					assert.Equal(t, tc.runes[1], cells[1].Ch)
					return
				}
				assert.Equal(t, tc.wantCh, cells[0].Ch)
				assert.Equal(t, tc.wantComb, cells[0].CombiningRunes())
			})
		}
	}
}

// TestInputClustersCombiningSequencesInsertMode asserts the clustering
// merge also holds when the terminal is in insert mode.
func TestInputClustersCombiningSequencesInsertMode(t *testing.T) {
	p := newInputParserHandler(t, false)
	p.Resize(12, 2)
	p.SetMode(vteparser.ModeInsert)
	for _, r := range []rune{'\U0001F468', '\u200D', '\U0001F469'} {
		p.Input(r)
	}
	cells := firstRowCells(p)
	require.NotEmpty(t, cells)
	assert.Equal(t, '\U0001F468', cells[0].Ch)
	assert.Equal(t, []rune{'\u200D', '\U0001F469'}, cells[0].CombiningRunes())
}

// TestInputClustersZWJFamilyThroughFullParser drives the byte-level parser
// stack (utf8parser -> scanner -> driver -> Input) with the raw UTF-8 a
// shell echoes for a ZWJ family emoji, guarding the live PTY decode path
// rather than direct Input calls. Each codepoint arrives on its own
// Input, so the cluster must still collapse into a single cell.
func TestInputClustersZWJFamilyThroughFullParser(t *testing.T) {
	p := newInputParserHandler(t, false)
	p.Resize(20, 3)

	// echo <space> 👨 ZWJ 👩 ZWJ 👧
	data := []byte("echo ")
	data = append(data, 0xf0, 0x9f, 0x91, 0xa8) // 👨 U+1F468
	data = append(data, 0xe2, 0x80, 0x8d)       // ZWJ U+200D
	data = append(data, 0xf0, 0x9f, 0x91, 0xa9) // 👩 U+1F469
	data = append(data, 0xe2, 0x80, 0x8d)       // ZWJ U+200D
	data = append(data, 0xf0, 0x9f, 0x91, 0xa7) // 👧 U+1F467

	parser := vteparser.NewParser(p, new(vteparser.StdTimeout))
	for _, b := range data {
		parser.Advance(b)
	}

	cells := firstRowCells(p)
	require.GreaterOrEqual(t, len(cells), 6)
	assert.Equal(t, '\U0001F468', cells[5].Ch)
	assert.Equal(t, uint8(2), cells[5].Width)
	assert.Equal(t,
		[]rune{'\u200D', '\U0001F469', '\u200D', '\U0001F467'},
		cells[5].CombiningRunes())
}

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
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"strconv"
	"strings"
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
	"unstable.build/rune/internal/term/programstatus"
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
			// The recorded sequence ends with DL on the row holding `e`,
			// which zsh expects to vanish and the rows below to move up.
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

				assertEqualBuf(t, p, "b    \nc    \n2   Z\n1    \n     ")
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
				assert.Equal(t, term.Coordinates{Y: 15, X: 0}, p.sync.buf.CursorAtScroll())
				assert.Equal(t, term.Coordinates{Y: 0, X: 0}, p.sync.buf.CursorAtScreen())

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
			ph := newParserHandler(new(sync.Mutex), &mockPtyFile, testParserHost{}, &tm,
				clipboard.NewInMemory(), tm.bell, testURI, attrs, false, 10000, 0, nil, nil, "")
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

type mockTabManager struct {
	belled   bool
	toUri    workspaceapi.URI
	setName  string
	setAttr  term.Attributes
	statuses []statusCall
}

// statusCall is a SetTabStatus call, or an EndTabStatus one when end is
// set.
type statusCall struct {
	report programstatus.Report
	end    bool
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

func (tm *mockTabManager) SetTabStatus(uri workspaceapi.URI, report programstatus.Report) error {
	tm.toUri = uri
	tm.statuses = append(tm.statuses, statusCall{report: report})
	return nil
}

func (tm *mockTabManager) EndTabStatus(uri workspaceapi.URI) error {
	tm.toUri = uri
	tm.statuses = append(tm.statuses, statusCall{end: true})
	return nil
}

func (tm *mockTabManager) bell() {
	tm.belled = true
}

// testParserHost grants the clipboard access it is given and keeps no
// frames.
type testParserHost struct {
	access ClipboardAccess
}

func (h testParserHost) clipboardAccess() ClipboardAccess { return h.access }

func (testParserHost) syncUpdateLocked(bool) {}

func newInputParserHandler(t *testing.T, alt bool) *parserHandler {
	t.Helper()
	ph, _ := newInputParserHandlerPty(t, alt)
	return ph
}

// newInputParserHandlerPty is newInputParserHandler exposing the pty
// master, for tests that assert on what the program is sent.
func newInputParserHandlerPty(t *testing.T, alt bool) (*parserHandler, *workspacetest.File) {
	t.Helper()
	testURI, err := workspaceapi.ParseURI("memory:///radical")
	require.NoError(t, err)
	mockPtyFile := &workspacetest.File{}
	tm := mockTabManager{}
	attrs := DefaultConfig().NeedsAttentionAttributes
	ph := newParserHandler(new(sync.Mutex), mockPtyFile, testParserHost{}, &tm,
		clipboard.NewInMemory(), tm.bell, testURI, attrs, false, 10000, 0, nil, nil, "")
	ph.sync.primBuf.SetDefaultChar(' ')
	ph.sync.altBuf.SetDefaultChar(' ')
	if alt {
		ph.SetPrivateMode(vteparser.PrivateModeSwapScreenAndSetRestoreCursor)
	} else {
		ph.UnsetPrivateMode(vteparser.PrivateModeSwapScreenAndSetRestoreCursor)
	}
	return ph, mockPtyFile
}

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

func TestProgramStatus(t *testing.T) {
	t.Parallel()
	const st = "\x1b\\"
	working := programstatus.Report{State: programstatus.Working, Progress: -1, App: "brew", Msg: "Installing"}
	done := programstatus.Report{State: programstatus.Done, Progress: -1, App: "brew", Msg: "Upgraded"}
	blocked := programstatus.Report{ID: "eu", State: programstatus.Blocked, Kind: programstatus.KindAuth, Progress: -1}
	clearAll := programstatus.Report{State: programstatus.Clear}
	end := statusCall{end: true}
	cases := []struct {
		name    string
		run     func(ph *parserHandler)
		want    []statusCall
		wantPty string
	}{
		{
			name: "every report is forwarded",
			run: func(ph *parserHandler) {
				ph.ProgramStatus(working)
				ph.ProgramStatus(working)
				ph.ProgramStatus(blocked)
				ph.ProgramStatus(done)
			},
			want: []statusCall{{report: working}, {report: working}, {report: blocked}, {report: done}},
		},
		{
			name:    "query is echoed with ST",
			run:     func(ph *parserHandler) { ph.ProgramStatusQuery(st) },
			wantPty: "\x1b]7501;?" + st,
		},
		{
			name:    "query is echoed with BEL",
			run:     func(ph *parserHandler) { ph.ProgramStatusQuery("\x07") },
			wantPty: "\x1b]7501;?\x07",
		},
		{
			name: "prompt ends what was reported",
			run: func(ph *parserHandler) {
				ph.ProgramStatus(done)
				ph.ProgramStatus(blocked)
				ph.PromptStart()
			},
			want: []statusCall{{report: done}, {report: blocked}, end},
		},
		{
			name: "prompt without a report tells nothing",
			run:  func(ph *parserHandler) { ph.PromptStart() },
		},
		{
			name: "prompt after clearing everything tells nothing",
			run: func(ph *parserHandler) {
				ph.ProgramStatus(working)
				ph.ProgramStatus(clearAll)
				ph.PromptStart()
			},
			want: []statusCall{{report: working}, {report: clearAll}},
		},
		{
			name: "prompt after clearing a child ends the rest",
			run: func(ph *parserHandler) {
				ph.ProgramStatus(working)
				ph.ProgramStatus(programstatus.Report{ID: "eu", State: programstatus.Clear})
				ph.PromptStart()
			},
			want: []statusCall{
				{report: working},
				{report: programstatus.Report{ID: "eu", State: programstatus.Clear}},
				end,
			},
		},
		{
			name: "process exit ends what was reported",
			run: func(ph *parserHandler) {
				ph.ProgramStatus(working)
				ph.sync.mu.Lock()
				ph.endStatus()
				ph.sync.mu.Unlock()
			},
			want: []statusCall{{report: working}, end},
		},
		{
			name: "reset clears what was reported",
			run: func(ph *parserHandler) {
				ph.ProgramStatus(done)
				ph.ResetState()
				ph.ResetState()
			},
			want: []statusCall{{report: done}, {report: clearAll}},
		},
		{
			name: "reports after a reset are forwarded",
			run: func(ph *parserHandler) {
				ph.ResetState()
				ph.ProgramStatus(done)
			},
			want: []statusCall{{report: done}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ph, pty := newInputParserHandlerPty(t, false)
			tm := ph.tm.(*mockTabManager)
			tc.run(ph)
			assert.Equal(t, tc.want, tm.statuses)
			if tc.want != nil {
				assert.Equal(t, ph.uri, tm.toUri)
			}
			if tc.wantPty != "" {
				assertWriteToPty(t, pty, tc.wantPty)
			} else {
				assert.Empty(t, pty.Writes)
			}
		})
	}
}

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
	ph := newParserHandler(new(sync.Mutex), &mockPtyFile, testParserHost{}, &tm,
		clipboard.NewInMemory(), bellFn, testURI, attrs, false, 10000, 0, nil, nil, "")

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

func TestUnderlineColorAttribute(t *testing.T) {
	p := newInputParserHandler(t, false)
	p.Resize(8, 2)
	p.TerminalAttribute(vteparser.Attr{
		Type: vteparser.UnderlineColorAttr, Color: term.NewRGBColor(10, 20, 30),
	})
	p.Input('a')
	p.TerminalAttribute(vteparser.Attr{
		Type: vteparser.UnderlineColorAttr, Color: term.ColorDefault,
	})
	p.Input('b')
	p.TerminalAttribute(vteparser.Attr{
		Type: vteparser.UnderlineColorAttr, Color: term.NewRGBColor(10, 20, 30),
	})
	p.TerminalAttribute(vteparser.Attr{Type: vteparser.ResetAttr})
	p.Input('c')

	cells := firstRowCells(p)
	require.GreaterOrEqual(t, len(cells), 3)
	assert.Equal(t, term.NewRGBColor(10, 20, 30), cells[0].UnderlineColor())
	assert.Equal(t, term.ColorDefault, cells[1].UnderlineColor(), "SGR 59 resets it")
	assert.Equal(t, term.ColorDefault, cells[2].UnderlineColor(), "SGR 0 resets it")
}

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

func TestInputWideRuneWrapsAtRightMargin(t *testing.T) {
	p := newInputParserHandler(t, false)
	p.Resize(5, 3)
	p.Input('世')
	p.Input('界')
	p.Input('中')
	assertEqualBuf(t, p, "世 界  \n中    \n     ")
}

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

func TestResizeKeepsCursorAfterItsCharacter(t *testing.T) {
	suite := []struct {
		name                string
		width, height       int
		input               string
		up                  int
		newWidth, newHeight int
		expected            string
	}{
		{
			name:  "widening joins the cursor row",
			width: 10, height: 3,
			input:    "abcdefghijklm",
			newWidth: 20, newHeight: 3,
			expected: "abcdefghijklm_      \n                    \n                    ",
		},
		{
			name:  "narrowing splits the cursor row",
			width: 20, height: 3,
			input:    "abcdefghijklm",
			newWidth: 10, newHeight: 3,
			expected: "abcdefghij\nklm_      \n          ",
		},
		{
			name:  "prompt drawn before the first resize",
			width: 1, height: 1,
			input:    "sh ",
			newWidth: 16, newHeight: 3,
			expected: "sh _            \n                \n                ",
		},
		{
			name:  "wrap pending at the new width",
			width: 30, height: 3,
			input:    "abcdefghijklmnopqrst",
			newWidth: 10, newHeight: 3,
			expected: "abcdefghij\nklmnopqrst\n_         ",
		},
		{
			name:  "wrap pending at both widths",
			width: 10, height: 3,
			input:    "abcdefghij",
			newWidth: 5, newHeight: 3,
			expected: "abcde\nfghij\n_    ",
		},
		{
			name:  "narrowing a line that continues below the cursor",
			width: 10, height: 4,
			input: "ab\nABCDEFGHIJKLMNOPQRST", up: 1,
			newWidth: 4, newHeight: 4,
			expected: "EFGH\nI_KL\nMNOP\nQRST",
		},
		{
			name:  "widening a line that continues below the cursor",
			width: 5, height: 6,
			input: "abcdefg\nABCDEFGHIJKL", up: 1,
			newWidth: 10, newHeight: 6,
			expected: "abcdefg   \nABCDEFG_IJ\nKL        \n          \n          \n          ",
		},
		{
			name:  "shortening while widening a line that continues below the cursor",
			width: 5, height: 3,
			input: "1\n2\n3\n4\nabcdefghijk", up: 2,
			newWidth: 15, newHeight: 2,
			expected: "4              \na_cdefghijk    ",
		},
	}

	for _, test := range suite {
		t.Run(test.name, func(t *testing.T) {
			p := newInputParserHandler(t, false)
			p.Resize(test.width, test.height)
			writeToBuffer(p, test.input)
			if test.up > 0 {
				p.MoveUp(test.up)
			}
			p.Resize(test.newWidth, test.newHeight)
			p.Input('_')
			assertEqualBuf(t, p, test.expected)
		})
	}

	t.Run("saved cursor", func(t *testing.T) {
		p := newInputParserHandler(t, false)
		p.Resize(10, 3)
		writeToBuffer(p, "abcdefghijklm")
		p.SaveCursorPosition()
		writeToBuffer(p, "\nx")
		p.Resize(20, 3)
		p.RestoreCursorPosition()
		p.Input('_')
		assertEqualBuf(t, p, "abcdefghijklm_      \nx                   \n                    ")
	})

	t.Run("saved cursor after trailing blanks", func(t *testing.T) {
		p := newInputParserHandler(t, false)
		p.Resize(10, 4)
		writeToBuffer(p, "sh ")
		p.SaveCursorPosition()
		writeToBuffer(p, "\nx")
		p.Resize(2, 4)
		p.RestoreCursorPosition()
		p.Input('_')
		assertEqualBuf(t, p, "sh\n _\nx \n  ")
	})

	t.Run("cursor whose cell scrolls into history stays on screen", func(t *testing.T) {
		p := newInputParserHandler(t, false)
		p.Resize(20, 2)
		writeToBuffer(p, "abcdefghijklmnopqrst")
		p.CarriageReturn()
		p.Resize(5, 2)
		assert.Equal(t, term.Coordinates{}, p.sync.buf.CursorAtScreen())
	})
}

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

	parser := vteparser.NewParser(p)
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

func TestPrimaryScrollingRegion(t *testing.T) {
	newScreen := func(t *testing.T) *parserHandler {
		t.Helper()
		p := newInputParserHandler(t, false)
		p.Resize(4, 5)
		writeToBuffer(p, "aaaa\nbbbb\ncccc\ndddd\neeee")
		return p
	}

	t.Run("a region below the top discards the scrolled out line", func(t *testing.T) {
		p := newScreen(t)
		rowsBefore := p.sync.primBuf.Rows()
		p.SetScrollingRegion(2, 4, false)
		assert.Equal(t, term.Coordinates{}, p.sync.buf.CursorAtScreen(),
			"DECSTBM homes the cursor to the screen unless DECOM is set")
		p.setCursorAtScreen(term.Coordinates{Y: 3})
		p.Linefeed()
		assertEqualBuf(t, p, "aaaa\ncccc\ndddd\n    \neeee")
		assert.Equal(t, rowsBefore, p.sync.primBuf.Rows(), "nothing was saved")
		assert.Equal(t, term.Coordinates{Y: 3}, p.sync.buf.CursorAtScreen())
	})

	t.Run("a region at the top saves the scrolled out line", func(t *testing.T) {
		p := newScreen(t)
		rowsBefore := p.sync.primBuf.Rows()
		p.SetScrollingRegion(1, 4, false)
		p.setCursorAtScreen(term.Coordinates{Y: 3})
		p.Linefeed()
		assertEqualBuf(t, p, "bbbb\ncccc\ndddd\n    \neeee")
		assert.Equal(t, rowsBefore+1, p.sync.primBuf.Rows(), "the top row went to history")
		assert.Equal(t, "aaaa", term.CellsToString(p.sync.primBuf.Cells.RawCells()[:1]))
	})

	t.Run("reverse index scrolls the region down", func(t *testing.T) {
		p := newScreen(t)
		p.SetScrollingRegion(2, 4, false)
		p.setCursorAtScreen(term.Coordinates{Y: 1})
		p.ReverseIndex()
		assertEqualBuf(t, p, "aaaa\n    \nbbbb\ncccc\neeee")
	})

	t.Run("insert and delete lines stay inside the region", func(t *testing.T) {
		p := newScreen(t)
		p.SetScrollingRegion(2, 4, false)
		p.setCursorAtScreen(term.Coordinates{Y: 1})
		p.InsertBlankLines(1)
		assertEqualBuf(t, p, "aaaa\n    \nbbbb\ncccc\neeee")
		p.DeleteLines(1)
		assertEqualBuf(t, p, "aaaa\nbbbb\ncccc\n    \neeee")
	})

	t.Run("a resize resets the region", func(t *testing.T) {
		p := newScreen(t)
		p.SetScrollingRegion(2, 4, false)
		p.Resize(4, 5)
		assert.Equal(t, 0, p.sync.primBuf.TopScrollableRegion())
		assert.Equal(t, 5, p.sync.primBuf.BottomScrollableRegion())
	})
}

func TestIndexOutsideScrollingRegion(t *testing.T) {
	const screen = "aaaa\nbbbb\ncccc\ndddd\neeee"
	tests := []struct {
		name string
		// top and bottom are the DECSTBM parameters.
		top, bottom int
		cursor      term.Coordinates
		input       func(p *parserHandler)
		want        string
		wantCursor  term.Coordinates
	}{
		{
			name: "linefeed below the bottom margin moves down",
			top:  2, bottom: 3,
			cursor:     term.Coordinates{Y: 3},
			input:      (*parserHandler).Linefeed,
			want:       screen,
			wantCursor: term.Coordinates{Y: 4},
		},
		{
			name: "linefeed on the last line below the region stays",
			top:  2, bottom: 4,
			cursor:     term.Coordinates{Y: 4},
			input:      (*parserHandler).Linefeed,
			want:       screen,
			wantCursor: term.Coordinates{Y: 4},
		},
		{
			name: "linefeed above the region moves into it",
			top:  3, bottom: 4,
			cursor:     term.Coordinates{Y: 1},
			input:      (*parserHandler).Linefeed,
			want:       screen,
			wantCursor: term.Coordinates{Y: 2},
		},
		{
			name: "linefeed on the bottom margin scrolls the region",
			top:  2, bottom: 4,
			cursor:     term.Coordinates{X: 1, Y: 3},
			input:      (*parserHandler).Linefeed,
			want:       "aaaa\ncccc\ndddd\n    \neeee",
			wantCursor: term.Coordinates{X: 1, Y: 3},
		},
		{
			name: "autowrap on the last line below the region stays",
			top:  2, bottom: 4,
			cursor: term.Coordinates{X: 3, Y: 4},
			input: func(p *parserHandler) {
				p.Input('x')
				p.Input('y')
			},
			want:       "aaaa\nbbbb\ncccc\ndddd\nyeex",
			wantCursor: term.Coordinates{X: 1, Y: 4},
		},
		{
			name: "linefeed ends a pending wrap",
			top:  1, bottom: 5,
			cursor: term.Coordinates{Y: 1},
			input: func(p *parserHandler) {
				writeToBuffer(p, "wxyz")
				p.Linefeed()
				p.Input('Q')
			},
			want:       "aaaa\nwxyz\ncccQ\ndddd\neeee",
			wantCursor: term.Coordinates{X: 3, Y: 2},
		},
		{
			name: "reverse index above the top margin moves up",
			top:  3, bottom: 5,
			cursor:     term.Coordinates{Y: 1},
			input:      (*parserHandler).ReverseIndex,
			want:       screen,
			wantCursor: term.Coordinates{Y: 0},
		},
		{
			name: "reverse index on the first line above the region stays",
			top:  2, bottom: 5,
			cursor:     term.Coordinates{Y: 0},
			input:      (*parserHandler).ReverseIndex,
			want:       screen,
			wantCursor: term.Coordinates{Y: 0},
		},
		{
			name: "reverse index on the top margin scrolls the region",
			top:  2, bottom: 4,
			cursor:     term.Coordinates{Y: 1},
			input:      (*parserHandler).ReverseIndex,
			want:       "aaaa\n    \nbbbb\ncccc\neeee",
			wantCursor: term.Coordinates{Y: 1},
		},
	}
	for _, tt := range tests {
		for _, alt := range []bool{false, true} {
			name := tt.name + "/primary"
			if alt {
				name = tt.name + "/alt"
			}
			t.Run(name, func(t *testing.T) {
				p := newInputParserHandler(t, alt)
				p.Resize(4, 5)
				writeToBuffer(p, screen)
				p.SetScrollingRegion(tt.top, tt.bottom, false)
				p.setCursorAtScreen(tt.cursor)
				tt.input(p)
				assertEqualBuf(t, p, tt.want)
				assert.Equal(t, tt.wantCursor, p.sync.buf.CursorAtScreen())
			})
		}
	}
}

// forEachScreen runs fn against a fresh handler on the primary and on
// the alternate screen.
func forEachScreen(t *testing.T, name string, fn func(t *testing.T, p *parserHandler, pty *workspacetest.File)) {
	t.Helper()
	for _, alt := range []bool{false, true} {
		screen := "primary"
		if alt {
			screen = "alt"
		}
		t.Run(name+"/"+screen, func(t *testing.T) {
			p, pty := newInputParserHandlerPty(t, alt)
			fn(t, p, pty)
		})
	}
}

func TestSetScrollingRegion(t *testing.T) {
	tests := []struct {
		name        string
		top, bottom int
		end         bool
		origin      bool
		wantTop     int
		wantBottom  int
		wantCursor  term.Coordinates
		ignored     bool
	}{
		{
			name: "inverted region is ignored",
			top:  4, bottom: 2,
			wantTop: 0, wantBottom: 6, ignored: true,
		},
		{
			name: "single row region is ignored",
			top:  3, bottom: 3,
			wantTop: 0, wantBottom: 6, ignored: true,
		},
		{
			name: "two row region is accepted",
			top:  3, bottom: 4,
			wantTop: 2, wantBottom: 4,
		},
		{
			name: "bottom beyond the screen is clamped",
			top:  2, bottom: 99,
			wantTop: 1, wantBottom: 6,
		},
		{
			name: "top beyond the screen is ignored",
			top:  7, end: true,
			wantTop: 0, wantBottom: 6, ignored: true,
		},
		{
			name: "defaults select the whole screen",
			top:  1, end: true,
			wantTop: 0, wantBottom: 6,
		},
		{
			name: "the cursor is homed to the screen",
			top:  3, bottom: 5,
			wantTop: 2, wantBottom: 5,
		},
		{
			name: "under DECOM the cursor is homed to the top margin",
			top:  3, bottom: 5, origin: true,
			wantTop: 2, wantBottom: 5, wantCursor: term.Coordinates{Y: 2},
		},
	}
	for _, tt := range tests {
		forEachScreen(t, tt.name, func(t *testing.T, p *parserHandler, _ *workspacetest.File) {
			p.Resize(8, 6)
			if tt.origin {
				p.SetPrivateMode(vteparser.PrivateModeOrigin)
			}
			before := term.Coordinates{X: 2, Y: 1}
			p.setCursorAtScreen(before)
			p.SetScrollingRegion(tt.top, tt.bottom, tt.end)
			assert.Equal(t, tt.wantTop, p.sync.buf.TopScrollableRegion())
			assert.Equal(t, tt.wantBottom, p.sync.buf.BottomScrollableRegion())
			if tt.ignored {
				assert.Equal(t, before, p.sync.buf.CursorAtScreen(), "an ignored DECSTBM leaves the cursor")
			} else {
				assert.Equal(t, tt.wantCursor, p.sync.buf.CursorAtScreen())
			}
		})
	}
}

func TestOriginMode(t *testing.T) {
	// The region is rows 3 to 5 of a 6-row screen.
	const top, bottom = 2, 4
	tests := []struct {
		name       string
		input      func(p *parserHandler)
		want       term.Coordinates
		wantScreen string
		wantReport string
	}{
		{
			name:  "CUP is relative to the top margin",
			input: func(p *parserHandler) { p.Goto(1, 3) },
			want:  term.Coordinates{X: 3, Y: top + 1},
		},
		{
			name:  "CUP stops at the bottom margin",
			input: func(p *parserHandler) { p.Goto(10, 0) },
			want:  term.Coordinates{Y: bottom},
		},
		{
			name:  "VPA is relative to the top margin",
			input: func(p *parserHandler) { p.GotoLine(1) },
			want:  term.Coordinates{Y: top + 1},
		},
		{
			name:  "CHA keeps the row",
			input: func(p *parserHandler) { p.Goto(1, 0); p.GotoCol(5) },
			want:  term.Coordinates{X: 5, Y: top + 1},
		},
		{
			name:  "CUF and CUB keep the row",
			input: func(p *parserHandler) { p.Goto(1, 0); p.MoveForward(2); p.MoveBackward(1) },
			want:  term.Coordinates{X: 1, Y: top + 1},
		},
		{
			name:  "CUU stops at the top margin",
			input: func(p *parserHandler) { p.Goto(1, 0); p.MoveUp(5) },
			want:  term.Coordinates{Y: top},
		},
		{
			name:  "CUD stops at the bottom margin",
			input: func(p *parserHandler) { p.Goto(0, 0); p.MoveDown(10) },
			want:  term.Coordinates{Y: bottom},
		},
		{
			name:  "CNL moves down one row",
			input: func(p *parserHandler) { p.Goto(0, 3); p.MoveDownAndCR(1) },
			want:  term.Coordinates{Y: top + 1},
		},
		{
			name:  "CPL moves up one row",
			input: func(p *parserHandler) { p.Goto(2, 3); p.MoveUpAndCR(1) },
			want:  term.Coordinates{Y: top + 1},
		},
		{
			name:       "printing advances along the row",
			input:      func(p *parserHandler) { p.Goto(0, 0); writeToBuffer(p, "abc") },
			want:       term.Coordinates{X: 3, Y: top},
			wantScreen: "        \n        \nabc     \n        \n        \n        ",
		},
		{
			name:  "linefeed moves down one row",
			input: func(p *parserHandler) { p.Goto(0, 0); p.Linefeed() },
			want:  term.Coordinates{Y: top + 1},
		},
		{
			name:       "CPR reports relative to the origin",
			input:      func(p *parserHandler) { p.Goto(1, 2); p.DeviceStatus(6) },
			want:       term.Coordinates{X: 2, Y: top + 1},
			wantReport: "\x1b[2;3R",
		},
		{
			name: "setting DECOM homes the cursor to the origin",
			input: func(p *parserHandler) {
				p.UnsetPrivateMode(vteparser.PrivateModeOrigin)
				p.Goto(5, 5)
				p.SetPrivateMode(vteparser.PrivateModeOrigin)
			},
			want: term.Coordinates{Y: top},
		},
		{
			name: "resetting DECOM homes the cursor to the screen",
			input: func(p *parserHandler) {
				p.Goto(1, 5)
				p.UnsetPrivateMode(vteparser.PrivateModeOrigin)
			},
			want: term.Coordinates{},
		},
	}
	for _, tt := range tests {
		forEachScreen(t, tt.name, func(t *testing.T, p *parserHandler, pty *workspacetest.File) {
			p.Resize(8, 6)
			p.SetScrollingRegion(top+1, bottom+1, false)
			p.SetPrivateMode(vteparser.PrivateModeOrigin)
			pty.Writes = nil
			tt.input(p)
			assert.Equal(t, tt.want, p.sync.buf.CursorAtScreen())
			if tt.wantScreen != "" {
				assertEqualBuf(t, p, tt.wantScreen)
			}
			if tt.wantReport != "" {
				assertWriteToPty(t, pty, tt.wantReport)
			}
		})
	}
}

func TestCursorMotionWithinMargins(t *testing.T) {
	// The region is rows 3 to 5 of a 6-row screen.
	const top, bottom = 2, 4
	tests := []struct {
		name  string
		start term.Coordinates
		input func(p *parserHandler)
		want  term.Coordinates
	}{
		{
			name:  "CUD inside the region stops at the bottom margin",
			start: term.Coordinates{Y: top},
			input: func(p *parserHandler) { p.MoveDown(10) },
			want:  term.Coordinates{Y: bottom},
		},
		{
			name:  "CUD above the region stops at the bottom margin",
			start: term.Coordinates{Y: 0},
			input: func(p *parserHandler) { p.MoveDown(10) },
			want:  term.Coordinates{Y: bottom},
		},
		{
			name:  "CUD below the region stops at the last line",
			start: term.Coordinates{Y: bottom + 1},
			input: func(p *parserHandler) { p.MoveDown(10) },
			want:  term.Coordinates{Y: 5},
		},
		{
			name:  "CUU inside the region stops at the top margin",
			start: term.Coordinates{Y: bottom},
			input: func(p *parserHandler) { p.MoveUp(10) },
			want:  term.Coordinates{Y: top},
		},
		{
			name:  "CUU below the region stops at the top margin",
			start: term.Coordinates{Y: 5},
			input: func(p *parserHandler) { p.MoveUp(10) },
			want:  term.Coordinates{Y: top},
		},
		{
			name:  "CUU above the region stops at the first line",
			start: term.Coordinates{Y: 1},
			input: func(p *parserHandler) { p.MoveUp(10) },
			want:  term.Coordinates{},
		},
		{
			name:  "CUP is not confined to the region",
			start: term.Coordinates{Y: top},
			input: func(p *parserHandler) { p.Goto(5, 0) },
			want:  term.Coordinates{Y: 5},
		},
	}
	for _, tt := range tests {
		forEachScreen(t, tt.name, func(t *testing.T, p *parserHandler, _ *workspacetest.File) {
			p.Resize(8, 6)
			p.SetScrollingRegion(top+1, bottom+1, false)
			p.setCursorAtScreen(tt.start)
			tt.input(p)
			assert.Equal(t, tt.want, p.sync.buf.CursorAtScreen())
		})
	}
}

func TestResetsClearScrollingRegion(t *testing.T) {
	tests := []struct {
		name  string
		input func(p *parserHandler)
	}{
		{"DECCOLM set", func(p *parserHandler) { p.SetPrivateMode(vteparser.PrivateModeColumnMode) }},
		{"DECCOLM reset", func(p *parserHandler) { p.UnsetPrivateMode(vteparser.PrivateModeColumnMode) }},
		{"DECALN", (*parserHandler).Decaln},
	}
	for _, tt := range tests {
		forEachScreen(t, tt.name, func(t *testing.T, p *parserHandler, _ *workspacetest.File) {
			p.Resize(4, 5)
			p.SetScrollingRegion(2, 4, false)
			p.setCursorAtScreen(term.Coordinates{X: 1, Y: 3})
			tt.input(p)
			assert.Equal(t, 0, p.sync.buf.TopScrollableRegion())
			assert.Equal(t, 5, p.sync.buf.BottomScrollableRegion())
			assert.Equal(t, term.Coordinates{}, p.sync.buf.CursorAtScreen())
		})
	}
}

func TestDecalnFillsTheScreen(t *testing.T) {
	p := newInputParserHandler(t, false)
	p.Resize(4, 5)
	writeToBuffer(p, "h1\nh2\naaaa\nbbbb\ncccc\ndddd\neeee")
	require.Equal(t, 7, p.sync.primBuf.Rows())
	history := term.CellsToString(p.sync.primBuf.Cells.RawCells()[:2])

	p.Decaln()
	assertEqualBuf(t, p, "EEEE\nEEEE\nEEEE\nEEEE\nEEEE")
	assert.Equal(t, history, term.CellsToString(p.sync.primBuf.Cells.RawCells()[:2]),
		"the scrollback is untouched")
}

func TestEraseAboveKeepsScrollback(t *testing.T) {
	tests := []struct {
		name    string
		history string
		cursor  term.Coordinates
		want    string
	}{
		{
			name:   "from the second row without scrollback",
			cursor: term.Coordinates{Y: 1},
			want:   "    \n bbb\ncccc\ndddd\neeee",
		},
		{
			name:    "from the third row with scrollback",
			history: "h1\nh2\n",
			cursor:  term.Coordinates{X: 1, Y: 2},
			want:    "    \n    \n  cc\ndddd\neeee",
		},
		{
			name:    "from the first row with scrollback",
			history: "h1\nh2\n",
			cursor:  term.Coordinates{X: 2},
			want:    "   a\nbbbb\ncccc\ndddd\neeee",
		},
	}
	for _, tt := range tests {
		forEachScreen(t, tt.name, func(t *testing.T, p *parserHandler, _ *workspacetest.File) {
			if p.useAlt && tt.history != "" {
				t.Skip("the alternate screen has no scrollback")
			}
			p.Resize(4, 5)
			writeToBuffer(p, tt.history+"aaaa\nbbbb\ncccc\ndddd\neeee")
			var history string
			if tt.history != "" {
				history = term.CellsToString(p.sync.primBuf.Cells.RawCells()[:2])
			}
			p.setCursorAtScreen(tt.cursor)
			p.ClearScreen(vteparser.ClearModeAbove)
			assertEqualBuf(t, p, tt.want)
			if tt.history != "" {
				assert.Equal(t, history, term.CellsToString(p.sync.primBuf.Cells.RawCells()[:2]),
					"the scrollback is untouched")
			}
			assert.Equal(t, tt.cursor, p.sync.buf.CursorAtScreen())
		})
	}
}

func TestInsertDeleteLinesOnPrimaryScreen(t *testing.T) {
	tests := []struct {
		name   string
		cursor term.Coordinates
		input  func(p *parserHandler)
		want   string
	}{
		{
			name:   "delete one line",
			cursor: term.Coordinates{X: 2, Y: 1},
			input:  func(p *parserHandler) { p.DeleteLines(1) },
			want:   "aaaa\ncccc\ndddd\neeee\n    ",
		},
		{
			name:   "delete two lines",
			cursor: term.Coordinates{Y: 1},
			input:  func(p *parserHandler) { p.DeleteLines(2) },
			want:   "aaaa\ndddd\neeee\n    \n    ",
		},
		{
			name:   "delete past the bottom clears to the bottom",
			cursor: term.Coordinates{Y: 3},
			input:  func(p *parserHandler) { p.DeleteLines(10) },
			want:   "aaaa\nbbbb\ncccc\n    \n    ",
		},
		{
			name:   "insert one line",
			cursor: term.Coordinates{X: 2, Y: 1},
			input:  func(p *parserHandler) { p.InsertBlankLines(1) },
			want:   "aaaa\n    \nbbbb\ncccc\ndddd",
		},
		{
			name:   "insert past the bottom clears to the bottom",
			cursor: term.Coordinates{Y: 1},
			input:  func(p *parserHandler) { p.InsertBlankLines(10) },
			want:   "aaaa\n    \n    \n    \n    ",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newInputParserHandler(t, false)
			p.Resize(4, 5)
			writeToBuffer(p, "h1\nh2\naaaa\nbbbb\ncccc\ndddd\neeee")
			rows := p.sync.primBuf.Rows()
			history := term.CellsToString(p.sync.primBuf.Cells.RawCells()[:2])
			p.setCursorAtScreen(tt.cursor)
			tt.input(p)
			assertEqualBuf(t, p, tt.want)
			assert.Equal(t, rows, p.sync.primBuf.Rows(), "the scrollback neither grows nor shrinks")
			assert.Equal(t, history, term.CellsToString(p.sync.primBuf.Cells.RawCells()[:2]),
				"the scrollback is untouched")
			assert.Equal(t, tt.cursor, p.sync.buf.CursorAtScreen(), "the cursor stays put")
		})
	}
}

func TestClipboardOSC52(t *testing.T) {
	t.Parallel()
	const (
		sel = selectionRegisterID
		bel = "\x07"
		st  = "\x1b\\"
	)
	clip := clipboard.DefaultRegisterID
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	errUnavailable := errors.New("clipboard unavailable")
	cases := []struct {
		name      string
		registers map[string]string // held before the output
		err       error             // returned by every clipboard operation
		output    string
		// want holds the registers after the output; nil means the output
		// must leave them unchanged.
		want  map[string]string
		reply string
	}{
		// store
		{
			name:   "store c",
			output: "\x1b]52;c;" + b64("hello") + bel,
			want:   map[string]string{clip: "hello"},
		},
		{
			name:   "store p",
			output: "\x1b]52;p;" + b64("hello") + bel,
			want:   map[string]string{sel: "hello"},
		},
		{
			name:   "store s",
			output: "\x1b]52;s;" + b64("hello") + bel,
			want:   map[string]string{sel: "hello"},
		},
		{
			name:   "store without a register selects c",
			output: "\x1b]52;;" + b64("hello") + bel,
			want:   map[string]string{clip: "hello"},
		},
		{
			name:   "store terminated by ST",
			output: "\x1b]52;c;" + b64("hello") + st,
			want:   map[string]string{clip: "hello"},
		},
		{
			name:   "store multiline text",
			output: "\x1b]52;c;" + b64("one\ntwo\n") + bel,
			want:   map[string]string{clip: "one\ntwo\n"},
		},
		{
			name:   "store unicode text",
			output: "\x1b]52;c;" + b64("héllo 🚀") + bel,
			want:   map[string]string{clip: "héllo 🚀"},
		},
		{
			name:      "store replaces the register",
			registers: map[string]string{clip: "old"},
			output:    "\x1b]52;c;" + b64("hello") + bel,
			want:      map[string]string{clip: "hello"},
		},
		{
			name:      "store c keeps the primary selection",
			registers: map[string]string{sel: "kept"},
			output:    "\x1b]52;c;" + b64("hello") + bel,
			want:      map[string]string{clip: "hello", sel: "kept"},
		},
		{
			name:      "store p keeps the clipboard",
			registers: map[string]string{clip: "kept"},
			output:    "\x1b]52;p;" + b64("hello") + bel,
			want:      map[string]string{clip: "kept", sel: "hello"},
		},
		{
			name:      "store empty data clears the register",
			registers: map[string]string{clip: "old"},
			output:    "\x1b]52;c;" + bel,
			want:      map[string]string{clip: ""},
		},
		{
			name:      "store invalid base64 is dropped",
			registers: map[string]string{clip: "old"},
			output:    "\x1b]52;c;not base64!" + bel,
		},
		{
			name:   "store unknown register is dropped",
			output: "\x1b]52;q;" + b64("hello") + bel,
		},
		{
			name:   "store cut buffer is dropped",
			output: "\x1b]52;0;" + b64("hello") + bel,
		},
		{
			name:      "store without data is dropped",
			registers: map[string]string{clip: "old"},
			output:    "\x1b]52;c" + bel,
		},
		{
			name:      "clipboard store error doesn't freeze the terminal",
			registers: map[string]string{clip: "old"},
			err:       errUnavailable,
			output:    "\x1b]52;c;" + b64("hello") + bel,
		},

		// load
		{
			name:      "load c",
			registers: map[string]string{clip: "hello"},
			output:    "\x1b]52;c;?" + bel,
			reply:     "\x1b]52;c;" + b64("hello") + bel,
		},
		{
			name:      "load p",
			registers: map[string]string{sel: "hello"},
			output:    "\x1b]52;p;?" + bel,
			reply:     "\x1b]52;p;" + b64("hello") + bel,
		},
		{
			name:      "load s",
			registers: map[string]string{sel: "hello"},
			output:    "\x1b]52;s;?" + bel,
			reply:     "\x1b]52;s;" + b64("hello") + bel,
		},
		{
			name:      "load without a register selects c",
			registers: map[string]string{clip: "hello"},
			output:    "\x1b]52;;?" + bel,
			reply:     "\x1b]52;c;" + b64("hello") + bel,
		},
		{
			name:      "load replies with the ST terminator",
			registers: map[string]string{clip: "hello"},
			output:    "\x1b]52;c;?" + st,
			reply:     "\x1b]52;c;" + b64("hello") + st,
		},
		{
			name:      "load c ignores the primary selection",
			registers: map[string]string{sel: "hello"},
			output:    "\x1b]52;c;?" + bel,
			reply:     "\x1b]52;c;" + bel,
		},
		{
			name:      "load p ignores the clipboard",
			registers: map[string]string{clip: "hello"},
			output:    "\x1b]52;p;?" + bel,
			reply:     "\x1b]52;p;" + bel,
		},
		{
			name:   "load empty register",
			output: "\x1b]52;c;?" + bel,
			reply:  "\x1b]52;c;" + bel,
		},
		{
			name:      "load multiline text",
			registers: map[string]string{clip: "one\ntwo\n"},
			output:    "\x1b]52;c;?" + bel,
			reply:     "\x1b]52;c;" + b64("one\ntwo\n") + bel,
		},
		{
			name:      "load unicode text",
			registers: map[string]string{clip: "héllo 🚀"},
			output:    "\x1b]52;c;?" + bel,
			reply:     "\x1b]52;c;" + b64("héllo 🚀") + bel,
		},
		{
			name:      "load unknown register is dropped",
			registers: map[string]string{clip: "hello"},
			output:    "\x1b]52;q;?" + bel,
		},
		{
			name:      "clipboard load error doesn't freeze the terminal",
			registers: map[string]string{clip: "hello"},
			err:       errUnavailable,
			output:    "\x1b]52;c;?" + bel,
		},

		// round trips
		{
			name:   "store c then load c",
			output: "\x1b]52;c;" + b64("hello") + bel + "\x1b]52;c;?" + bel,
			want:   map[string]string{clip: "hello"},
			reply:  "\x1b]52;c;" + b64("hello") + bel,
		},
		{
			name:   "store p then load s",
			output: "\x1b]52;p;" + b64("hello") + bel + "\x1b]52;s;?" + bel,
			want:   map[string]string{sel: "hello"},
			reply:  "\x1b]52;s;" + b64("hello") + bel,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			uri, err := workspaceapi.ParseURI("memory:///osc52")
			require.NoError(t, err)
			registers := map[string]string{}
			maps.Copy(registers, tc.registers)
			want := tc.want
			if want == nil {
				want = maps.Clone(registers)
			}
			pty := &workspacetest.File{}
			lock := new(lockCounter)
			ph := newParserHandler(lock, pty,
				testParserHost{access: ClipboardAccess{Write: true, Read: true}},
				&mockTabManager{}, &registerClipboard{registers: registers, err: tc.err},
				func() {}, uri, term.Attributes{}, false, 100, 0, nil, nil, "")

			vteparser.NewParser(ph).AdvanceBytes([]byte(tc.output))

			assert.Equal(t, want, registers)
			assert.Equal(t, tc.reply, string(bytes.Join(pty.Writes, nil)))
			assert.Zero(t, lock.held, "terminal lock left held")
			assert.Zero(t, lock.stray, "terminal lock released without being held")
		})
	}
}

func TestClipboardOSC52Access(t *testing.T) {
	t.Parallel()
	const bel = "\x07"
	clip := clipboard.DefaultRegisterID
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	store := "\x1b]52;c;" + b64("evil") + bel
	load := "\x1b]52;c;?" + bel
	cases := []struct {
		name   string
		access ClipboardAccess
		output string
		want   map[string]string
		reply  string
	}{
		{
			name:   "store is refused by default",
			output: store,
			want:   map[string]string{clip: "secret"},
		},
		{
			name:   "load is refused by default",
			output: load,
			want:   map[string]string{clip: "secret"},
		},
		{
			name:   "store is refused when only read is allowed",
			access: ClipboardAccess{Read: true},
			output: store,
			want:   map[string]string{clip: "secret"},
		},
		{
			name:   "load is refused when only write is allowed",
			access: ClipboardAccess{Write: true},
			output: load,
			want:   map[string]string{clip: "secret"},
		},
		{
			name:   "store is allowed by write",
			access: ClipboardAccess{Write: true},
			output: store,
			want:   map[string]string{clip: "evil"},
		},
		{
			name:   "load is allowed by read",
			access: ClipboardAccess{Read: true},
			output: load,
			want:   map[string]string{clip: "secret"},
			reply:  "\x1b]52;c;" + b64("secret") + bel,
		},
		{
			name:   "a refused store does not reply",
			output: store + load,
			want:   map[string]string{clip: "secret"},
		},
		{
			name:   "a reset keeps the access",
			access: ClipboardAccess{Write: true},
			output: "\x1bc" + store,
			want:   map[string]string{clip: "evil"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			uri, err := workspaceapi.ParseURI("memory:///osc52")
			require.NoError(t, err)
			registers := map[string]string{clip: "secret"}
			pty := &workspacetest.File{}
			ph := newParserHandler(new(lockCounter), pty,
				testParserHost{access: tc.access}, &mockTabManager{},
				&registerClipboard{registers: registers},
				func() {}, uri, term.Attributes{}, false, 100, 0, nil, nil, "")

			vteparser.NewParser(ph).AdvanceBytes([]byte(tc.output))

			assert.Equal(t, tc.want, registers)
			assert.Equal(t, tc.reply, string(bytes.Join(pty.Writes, nil)))
		})
	}
}

func TestRequestTermcapAdvertisesOSC52(t *testing.T) {
	t.Parallel()
	const st = "\x1b\\"
	hex := func(s string) string { return fmt.Sprintf("%x", s) }
	msValue := `\E]52;%p1%s;%p2%s\E\\`
	cases := []struct {
		name   string
		access ClipboardAccess
		output string
		reply  string
	}{
		{
			name:   "Ms when programs may write the clipboard",
			access: ClipboardAccess{Write: true},
			output: "\x1bP+q" + hex("Ms") + st,
			reply:  "\x1bP1+r" + hex("Ms") + "=" + hex(msValue) + st,
		},
		{
			name:   "Ms when programs may not write the clipboard",
			access: ClipboardAccess{Read: true},
			output: "\x1bP+q" + hex("Ms") + st,
			reply:  "\x1bP0+r" + hex("Ms") + st,
		},
		{
			name:   "an unknown capability",
			access: ClipboardAccess{Write: true},
			output: "\x1bP+q" + hex("Tc") + st,
			reply:  "\x1bP0+r" + hex("Tc") + st,
		},
		{
			name:   "every name of a request is answered on its own",
			access: ClipboardAccess{Write: true},
			output: "\x1bP+q" + hex("Tc") + ";" + hex("Ms") + ";" + hex("RGB") + st,
			reply: "\x1bP0+r" + hex("Tc") + st +
				"\x1bP1+r" + hex("Ms") + "=" + hex(msValue) + st +
				"\x1bP0+r" + hex("RGB") + st,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			uri, err := workspaceapi.ParseURI("memory:///xtgettcap")
			require.NoError(t, err)
			pty := &workspacetest.File{}
			ph := newParserHandler(new(lockCounter), pty,
				testParserHost{access: tc.access}, &mockTabManager{}, clipboard.NewInMemory(),
				func() {}, uri, term.Attributes{}, false, 100, 0, nil, nil, "")

			vteparser.NewParser(ph).AdvanceBytes([]byte(tc.output))

			assert.Equal(t, tc.reply, string(bytes.Join(pty.Writes, nil)))
		})
	}
}

func TestRequestTermcapMsReadsAsOSC52ToNeovim(t *testing.T) {
	t.Parallel()
	uri, err := workspaceapi.ParseURI("memory:///xtgettcap")
	require.NoError(t, err)
	pty := &workspacetest.File{}
	ph := newParserHandler(new(lockCounter), pty,
		testParserHost{access: ClipboardAccess{Write: true}}, &mockTabManager{},
		clipboard.NewInMemory(),
		func() {}, uri, term.Attributes{}, false, 100, 0, nil, nil, "")

	ph.RequestTermcap([]string{"Ms"})

	// vim.termcap.query: ^\027P1+r(%x+)(.*)$, then rest must be =%x+ and
	// the value, with \E read as ESC and %pN removed, must start the OSC
	// 52 sequence for runtime/plugin/osc52.lua to enable the provider.
	reply := string(bytes.Join(pty.Writes, nil))
	require.True(t, strings.HasPrefix(reply, "\x1bP1+r"), reply)
	require.True(t, strings.HasSuffix(reply, "\x1b\\"), reply)
	name, rest, ok := strings.Cut(strings.TrimSuffix(reply[len("\x1bP1+r"):], "\x1b\\"), "=")
	require.True(t, ok, reply)
	decodedName, err := hex.DecodeString(name)
	require.NoError(t, err)
	assert.Equal(t, "Ms", string(decodedName))
	value, err := hex.DecodeString(rest)
	require.NoError(t, err)
	seq := strings.ReplaceAll(string(value), `\E`, "\x1b")
	seq = regexp.MustCompile(`%p\d`).ReplaceAllString(seq, "")
	assert.True(t, strings.HasPrefix(seq, "\x1b]52"), "%q", seq)
}

// registerClipboard keeps each register apart, as the clipboard Rune
// configures does, so that a test tells which one a request reached.
type registerClipboard struct {
	registers map[string]string
	err       error
}

func (c *registerClipboard) Copy(registerID string, data clipboard.Data) error {
	if c.err != nil {
		return c.err
	}
	c.registers[registerID] = data.Text
	return nil
}

func (c *registerClipboard) Paste(registerID string) (clipboard.Data, error) {
	if c.err != nil {
		return clipboard.Data{}, c.err
	}
	return clipboard.Data{Text: c.registers[registerID]}, nil
}

// lockCounter stands in for the terminal lock so that releasing it
// while nobody holds it fails the test: on a sync.Mutex that is a fatal
// error, which would take down the test binary instead.
type lockCounter struct {
	held, stray int
}

func (l *lockCounter) Lock() { l.held++ }

func (l *lockCounter) Unlock() {
	if l.held == 0 {
		l.stray++
		return
	}
	l.held--
}

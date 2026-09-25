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
	"context"
	"errors"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/term/vte/vteparser"
	"unstable.build/rune/internal/workspace/workspacetest"
)

func TestIntegrationComponent(t *testing.T) {
	t.Parallel()

	suite := []struct {
		desc      string
		altBuffer bool
		sut       func(*testing.T, *Component, *mockTabManager)
	}{
		{
			desc:      "primary scroll down 0 rows does nothing",
			altBuffer: false,
			sut: func(t *testing.T, comp *Component, tm *mockTabManager) {
				p := comp.parserHandler
				comp.Resize(5, 5)
				resetBuffer(t, p, "a    \nb    \nc    \nd    \ne    ")
				p.ScrollUp(0)
				assertDraw(t, comp, "a    \nb    \nc    \nd    \ne    ")
				assert.False(t, comp.ScrollUp(0))
				assertDraw(t, comp, "a    \nb    \nc    \nd    \ne    ")
			},
		},
		{
			desc:      "primary scroll ScrollOffset/MaxOffset rows <= height",
			altBuffer: false,
			sut: func(t *testing.T, comp *Component, tm *mockTabManager) {
				p := comp.parserHandler
				comp.Resize(5, 5)
				resetBuffer(t, p, "a    \nb    \nc    \nd    \ne    ")
				assert.Equal(t, 0, comp.ScrollOffset())
				assert.Equal(t, 0, comp.MaxScrollOffset())
			},
		},
		{
			desc:      "primary scroll ScrollOffset/MaxOffset rows > height",
			altBuffer: false,
			sut: func(t *testing.T, comp *Component, tm *mockTabManager) {
				p := comp.parserHandler
				comp.Resize(5, 5)
				resetBuffer(t, p, "a    \nb    \nc    \nd    \ne    \nf    \ng    ")
				assert.Equal(t, 2, comp.ScrollOffset())
				assert.Equal(t, 2, comp.MaxScrollOffset())
			},
		},
		{
			desc:      "primary scroll up 0 rows does nothing",
			altBuffer: false,
			sut: func(t *testing.T, comp *Component, tm *mockTabManager) {
				p := comp.parserHandler
				comp.Resize(5, 5)
				resetBuffer(t, p, "a    \nb    \nc    \nd    \ne    ")
				p.ScrollDown(0)
				assertDraw(t, comp, "a    \nb    \nc    \nd    \ne    ")
				assert.False(t, comp.ScrollDown(0))
				assertDraw(t, comp, "a    \nb    \nc    \nd    \ne    ")
			},
		},
		{
			desc:      "primary input mixed with user scrolls",
			altBuffer: false,
			sut: func(t *testing.T, comp *Component, tm *mockTabManager) {
				p := comp.parserHandler
				comp.Resize(5, 5)
				for range 6 {
					p.Input('a')
					p.CarriageReturn()
					p.Linefeed()
				}
				require.True(t, comp.ScrollUp(1))
				p.Input('b')
				require.True(t, comp.ScrollUp(1))
				p.CarriageReturn()
				p.Linefeed()
				comp.ScrollUp(1)
				comp.ScrollDown(3)
				assertDraw(t, comp, "a    \na    \na    \nb    \n     ")
			},
		},
		{
			desc:      "primary scroll ScrollOffset/MaxOffset after scroll",
			altBuffer: false,
			sut: func(t *testing.T, comp *Component, tm *mockTabManager) {
				p := comp.parserHandler
				comp.Resize(5, 5)
				resetBuffer(t, p, "a    \nb    \nc    \nd    \ne    \nf    \ng    ")
				require.False(t, comp.ScrollDown(1))
				assert.Equal(t, 2, comp.ScrollOffset())
				assert.Equal(t, 2, comp.MaxScrollOffset())
				require.True(t, comp.ScrollUp(1))
				assert.Equal(t, 1, comp.ScrollOffset())
				assert.Equal(t, 2, comp.MaxScrollOffset())
				require.True(t, comp.ScrollUp(1))
				assert.Equal(t, 0, comp.ScrollOffset())
				assert.Equal(t, 2, comp.MaxScrollOffset())
				require.False(t, comp.ScrollUp(1))
				assert.Equal(t, 0, comp.ScrollOffset())
				assert.Equal(t, 2, comp.MaxScrollOffset())
				require.True(t, comp.ScrollDown(1))
				assert.Equal(t, 1, comp.ScrollOffset())
				assert.Equal(t, 2, comp.MaxScrollOffset())
				require.True(t, comp.ScrollDown(1))
				assert.Equal(t, 2, comp.ScrollOffset())
				assert.Equal(t, 2, comp.MaxScrollOffset())
				require.False(t, comp.ScrollDown(1))
				assert.Equal(t, 2, comp.ScrollOffset())
				assert.Equal(t, 2, comp.MaxScrollOffset())
			},
		},
		{
			desc:      "primary scroll ScrollOffset/MaxOffset after ScrollBottom",
			altBuffer: false,
			sut: func(t *testing.T, comp *Component, tm *mockTabManager) {
				p := comp.parserHandler
				comp.Resize(5, 5)
				resetBuffer(t, p, "a    \nb    \nc    \nd    \ne    \nf    \ng    ")
				assertDraw(t, comp, "c    \nd    \ne    \nf    \ng    ")
				require.True(t, comp.ScrollUp(100))
				require.True(t, comp.ScrollBottom())
				assertDraw(t, comp, "c    \nd    \ne    \nf    \ng    ")
				assert.Equal(t, 2, comp.ScrollOffset())
				assert.Equal(t, 2, comp.MaxScrollOffset())
			},
		},
		{
			desc:      "selection after scroll",
			altBuffer: false,
			sut: func(t *testing.T, comp *Component, tm *mockTabManager) {
				p := comp.parserHandler
				comp.Resize(5, 5)
				resetBuffer(t, p, "a    \nb    \nc    \nd    \ne    \nf    \ng    ")
				require.True(t, comp.ScrollUp(1))
				assertDraw(t, comp, "b    \nc    \nd    \ne    \nf    ")

				comp.SelectWordAt(term.Coordinates{})
				data, ok := comp.Selection()
				assert.True(t, ok)
				assert.Equal(t, "b", data)

				comp.Unselect()

				comp.Select(term.Coordinates{})
				comp.SelectEnd(term.Coordinates{})
				data, ok = comp.Selection()
				assert.True(t, ok)
				assert.Equal(t, "b", data)

				comp.Unselect()

				comp.SelectLine(term.Coordinates{})
				data, ok = comp.Selection()
				assert.True(t, ok)
				assert.Equal(t, "b    \n", data)
			},
		},
		{
			desc:      "selection strips null characters",
			altBuffer: false,
			sut: func(t *testing.T, comp *Component, tm *mockTabManager) {
				p := comp.parserHandler
				comp.Resize(5, 5)
				p.Input('a')
				p.Input('b')
				p.Input('c')
				p.Input('\x00')

				comp.Select(term.Coordinates{})
				comp.SelectEnd(term.Coordinates{X: 3})
				data, ok := comp.Selection()
				assert.True(t, ok)
				assert.Equal(t, "abc", data)
			},
		},
		{
			desc:      "primary scroll up/down with cap",
			altBuffer: false,
			sut: func(t *testing.T, comp *Component, tm *mockTabManager) {
				p := comp.parserHandler
				comp.Resize(5, 5)
				resetBuffer(t, p, "a    \nb    \nc    \nd    \ne    \nf    \ng    ")
				assertDraw(t, comp, "c    \nd    \ne    \nf    \ng    ")

				assert.False(t, comp.ScrollDown(1))
				assertDraw(t, comp, "c    \nd    \ne    \nf    \ng    ")

				assert.True(t, comp.ScrollUp(1))
				assertDraw(t, comp, "b    \nc    \nd    \ne    \nf    ")

				assert.True(t, comp.ScrollUp(1))
				assertDraw(t, comp, "a    \nb    \nc    \nd    \ne    ")

				assert.False(t, comp.ScrollUp(1))
				assertDraw(t, comp, "a    \nb    \nc    \nd    \ne    ")

				assert.True(t, comp.ScrollDown(1))
				assertDraw(t, comp, "b    \nc    \nd    \ne    \nf    ")

				assert.True(t, comp.ScrollDown(1))
				assertDraw(t, comp, "c    \nd    \ne    \nf    \ng    ")

				assert.False(t, comp.ScrollDown(1))
				assertDraw(t, comp, "c    \nd    \ne    \nf    \ng    ")
			},
		},
		{
			desc:      "primary clear mode saved",
			altBuffer: false,
			sut: func(t *testing.T, comp *Component, tm *mockTabManager) {
				comp.Resize(5, 5)
				p := comp.parserHandler
				resetBuffer(t, p, "a    \nb    \nc    \nd    \ne    \n$ .  \nout  \n$    ")
				assertDraw(t, comp, "d    \ne    \n$ .  \nout  \n$    ")

				p.ClearScreen(vteparser.ClearModeSaved)
				assertDraw(t, comp, "d    \ne    \n$ .  \nout  \n$    ")

				assert.False(t, comp.ScrollUp(1))
				assertDraw(t, comp, "d    \ne    \n$ .  \nout  \n$    ")

				assert.False(t, comp.ScrollDown(1))
				assertDraw(t, comp, "d    \ne    \n$ .  \nout  \n$    ")
			},
		},
		{
			desc:      "shell cltr-l with scrollback history",
			altBuffer: false,
			sut: func(t *testing.T, comp *Component, tm *mockTabManager) {
				comp.Resize(5, 5)
				p := comp.parserHandler

				resetBuffer(t, p, "a    \nb    \nc    \nd    \ne    \n$ .  \nout  \n$    ")
				assertDraw(t, comp, "d    \ne    \n$ .  \nout  \n$    ")
				assert.Equal(t, 3, comp.ScrollOffset())
				assert.Equal(t, 3, comp.MaxScrollOffset())

				p.Goto(0, 0)
				p.ClearScreen(vteparser.ClearModeAll)
				p.ClearScreen(vteparser.ClearModeBelow)
				p.Input('$')
				p.ClearLine(vteparser.LineClearModeRight)
				assertDraw(t, comp, "$    \n     \n     \n     \n     ")
				assert.Equal(t, 8, comp.ScrollOffset())
				assert.Equal(t, 8, comp.MaxScrollOffset())

				// simulate user scrolling
				assert.True(t, comp.ScrollUp(1))
				assertDraw(t, comp, "$    \n$    \n     \n     \n     ")
				assert.Equal(t, 7, comp.ScrollOffset())
				assert.Equal(t, 8, comp.MaxScrollOffset())
				comp.Select(term.Coordinates{})
				comp.SelectEnd(term.Coordinates{})
				data, ok := comp.Selection()
				assert.True(t, ok)
				assert.Equal(t, "$", data)

				assert.True(t, comp.ScrollUp(1))
				assertDraw(t, comp, "out  \n$    \n$    \n     \n     ")
				assert.Equal(t, 6, comp.ScrollOffset())
				assert.Equal(t, 8, comp.MaxScrollOffset())

				assert.True(t, comp.ScrollUp(2))
				assertDraw(t, comp, "e    \n$ .  \nout  \n$    \n$    ")
				assert.Equal(t, 4, comp.ScrollOffset())
				assert.Equal(t, 8, comp.MaxScrollOffset())

				assert.True(t, comp.ScrollUp(100))
				assertDraw(t, comp, "a    \nb    \nc    \nd    \ne    ")
				assert.Equal(t, 0, comp.ScrollOffset())
				assert.Equal(t, 8, comp.MaxScrollOffset())

				assert.True(t, comp.ScrollDown(100))
				assertDraw(t, comp, "e    \n$ .  \nout  \n$    \n$    ")
				assert.Equal(t, 4, comp.ScrollOffset())
				assert.Equal(t, 8, comp.MaxScrollOffset())

				comp.SelectWordAt(term.Coordinates{})
				data, ok = comp.Selection()
				assert.True(t, ok)
				assert.Equal(t, "e", data)

				comp.Unselect()

				comp.Select(term.Coordinates{})
				comp.SelectEnd(term.Coordinates{})
				data, ok = comp.Selection()
				assert.True(t, ok)
				assert.Equal(t, "e", data)

				comp.Unselect()

				comp.SelectLine(term.Coordinates{})
				data, ok = comp.Selection()
				assert.True(t, ok)
				assert.Equal(t, "e    \n", data)

				require.True(t, comp.ScrollBottom())
				assertDraw(t, comp, "$    \n     \n     \n     \n     ")
			},
		},
		{
			desc:      "Input + Linefeed + CarriageReturn hit max scrollback history",
			altBuffer: false,
			sut: func(t *testing.T, comp *Component, tm *mockTabManager) {
				comp.Resize(5, 5)
				p := comp.parserHandler
				p.maxScrollLength = 6
				p.ResetState()
				p.Input('a')
				p.Linefeed()
				p.CarriageReturn()
				p.Input('b')
				p.Linefeed()
				p.CarriageReturn()
				p.Input('c')
				p.Linefeed()
				p.CarriageReturn()
				p.Input('d')
				p.Linefeed()
				p.CarriageReturn()
				p.Input('e')
				assertDraw(t, comp, "a    \nb    \nc    \nd    \ne    ")

				p.Linefeed()
				p.CarriageReturn()
				p.Input('f')
				assertDraw(t, comp, "b    \nc    \nd    \ne    \nf    ")

				p.CarriageReturn()
				p.Linefeed()
				p.Input('g')
				assertDraw(t, comp, "c    \nd    \ne    \nf    \ng    ")
				assert.Equal(t, p.maxScrollLength, p.sync.buf.Rows())

				p.CarriageReturn()
				p.Linefeed()
				p.Input('h')
				assertDraw(t, comp, "d    \ne    \nf    \ng    \nh    ")
				assert.Equal(t, p.maxScrollLength, p.sync.buf.Rows())
			},
		},
	}

	for _, test := range suite {
		t.Run(test.desc, func(t *testing.T) {
			t.Parallel()
			tm := mockTabManager{}

			cfg := DefaultConfig()
			comp, err := NewComponent(&testExecutor{}, &testExecutor{}, &tm, cfg)
			require.NoError(t, err)

			ph := comp.parserHandler
			ph.sync.primBuf.SetDefaultChar(' ')
			ph.sync.altBuf.SetDefaultChar(' ')

			if test.altBuffer {
				ph.SetPrivateMode(vteparser.PrivateModeSwapScreenAndSetRestoreCursor)
			} else {
				ph.UnsetPrivateMode(vteparser.PrivateModeSwapScreenAndSetRestoreCursor)
			}

			test.sut(t, comp, &tm)
		})
	}
}

// TestMouseDriverSelectionStartPinnedAcrossAutoScroll reproduces an upward
// drag selection that auto-scrolls the buffer. The SDK keeps re-applying the
// stored selection start on every drag tick via SetSelectionEnd; because
// Select translates window coordinates by the current scroll offset, the start
// anchor used to drift up with the content and drop the originally pressed
// cell from the selection.
func TestMouseDriverSelectionStartPinnedAcrossAutoScroll(t *testing.T) {
	t.Parallel()
	tm := mockTabManager{}

	cfg := DefaultConfig()
	comp, err := NewComponent(&testExecutor{}, &testExecutor{}, &tm, cfg)
	require.NoError(t, err)

	p := comp.parserHandler
	p.sync.primBuf.SetDefaultChar(' ')
	p.sync.altBuf.SetDefaultChar(' ')
	comp.Resize(5, 5)

	resetBuffer(t, p, "a    \nb    \nc    \nd    \ne    \nf    \ng    ")
	assertDraw(t, comp, "c    \nd    \ne    \nf    \ng    ")

	// Establish scrollback headroom so the upward drag has somewhere to go.
	// scrollY() is the raw offset and grows as the view scrolls up.
	require.True(t, comp.ScrollUp(1))
	assertDraw(t, comp, "b    \nc    \nd    \ne    \nf    ")
	require.Equal(t, 1, comp.scrollY())

	// Anchor the press coordinate empirically: window row 2 currently shows
	// "d". Selecting it directly proves the press target before the drag.
	const pressRow = 2
	comp.Select(term.Coordinates{Y: pressRow})
	comp.SelectEnd(term.Coordinates{Y: pressRow})
	pressed, ok := comp.Selection()
	require.True(t, ok)
	require.Equal(t, "d", pressed)
	comp.Unselect()

	driver := &mouseDriver{t: comp, clipboard: cfg.Clipboard}

	// Mirror mouse.Mouse.handleLeftClickSelect: the press anchors the start,
	// then each upward drag tick calls SetSelectionEnd before the top zone
	// triggers ScrollUp(1).
	driver.ClearSelection()
	driver.SetSelectionStart(term.Coordinates{Y: pressRow})

	driver.SetSelectionEnd(term.Coordinates{Y: 0})
	require.True(t, comp.ScrollUp(1))
	require.Equal(t, 2, comp.scrollY())

	// Already at the top of the scrollback, so further ScrollUp is a no-op,
	// but the SDK still issues the drag tick.
	driver.SetSelectionEnd(term.Coordinates{Y: 0})
	require.False(t, comp.ScrollUp(1))

	driver.SetSelectionEnd(term.Coordinates{Y: 0})

	data, ok := comp.Selection()
	require.True(t, ok)
	assert.Contains(t, data, "d",
		"selection must keep the originally pressed cell after auto-scroll")
}

type testExecutor struct {
}

func (e *testExecutor) StartCommand(ctx context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
	return 0, nil
}

func (e *testExecutor) Signal(pid workspaceapi.Pid, signal syscall.Signal) error {
	return nil
}

func (e *testExecutor) Close() error {
	return nil
}

func (e *testExecutor) NewPty(context.Context) (workspaceapi.Pty, error) {
	mockPtyFile := workspacetest.File{}
	return workspaceapi.Pty{Master: &mockPtyFile, Slave: &mockPtyFile}, nil
}

func (e *testExecutor) SetPtySize(p workspaceapi.Pty, size workspaceapi.PtySize) error {
	return nil
}

// stalledTerminal simulates a remote workspace whose transport has
// wedged: NewPty never returns until the RPC context is cancelled.
type stalledTerminal struct {
	testExecutor
}

func (s *stalledTerminal) NewPty(ctx context.Context) (workspaceapi.Pty, error) {
	<-ctx.Done()
	return workspaceapi.Pty{}, ctx.Err()
}

// stalledExecutor simulates a wedged spawn stream: the pty is created
// fine but StartCommand never returns until cancelled.
type stalledExecutor struct {
	testExecutor
}

func (s *stalledExecutor) StartCommand(
	ctx context.Context, cmd workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	<-ctx.Done()
	return 0, ctx.Err()
}

// TestComponentSpawnTimeout pins the regression where a stalled
// remote workspace transport blocked NewPty/StartCommand forever
// during Component.Init, wedging the caller (the vtereservoir
// warm-up and, transitively, the host event loop blocked in
// Facility.Get). With SpawnTimeout set, Init must fail after the
// bound instead of blocking indefinitely.
func TestComponentSpawnTimeout(t *testing.T) {
	t.Parallel()

	suite := []struct {
		desc     string
		terminal schemeapi.Terminal
		executor schemeapi.Executor
	}{
		{
			desc:     "stalled NewPty",
			terminal: &stalledTerminal{},
			executor: &testExecutor{},
		},
		{
			desc:     "stalled StartCommand",
			terminal: &testExecutor{},
			executor: &stalledExecutor{},
		},
	}

	for _, tc := range suite {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()
			cfg := DefaultConfig()
			cfg.SpawnTimeout = 25 * time.Millisecond

			errCh := make(chan error, 1)
			go func() {
				_, err := NewComponent(tc.terminal, tc.executor,
					&mockTabManager{}, cfg)
				errCh <- err
			}()

			select {
			case err := <-errCh:
				require.Error(t, err)
				assert.Contains(t, err.Error(), "spawn timed out after",
					"the error must point at the spawn watchdog, "+
						"not read like an ordinary shutdown "+
						"cancellation")
			case <-time.After(5 * time.Second):
				t.Fatal("NewComponent wedged on a stalled spawn RPC; " +
					"SpawnTimeout must bound it")
			}
		})
	}
}

// recordingExecutor captures the workspaceapi.Cmd passed to StartCommand
// so tests can assert on env, path, args, etc.
type recordingExecutor struct {
	testExecutor
	mu         sync.Mutex
	cmd        workspaceapi.Cmd
	setPtySize []ptySize
}

func (e *recordingExecutor) StartCommand(
	ctx context.Context, cmd workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cmd = cmd
	return 0, nil
}

func (e *recordingExecutor) snapshotCmd() workspaceapi.Cmd {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cmd
}

// TestComponentCreatePtySetsTerminalEnv pins the contract that the vte
// component injects a sane TERM/COLORTERM into every command it spawns.
// Without it, an SSH-workspace runesvc inherits TERM="" or "dumb" from
// its non-PTY SSH session; vim then falls back to its built-in "ansi"
// terminfo, which has no Ss/Se entries, never emits DECSCUSR, and the
// host renders CursorStyleDefault as a bar instead of the block vim
// would otherwise request.
func TestComponentCreatePtySetsTerminalEnv(t *testing.T) {
	t.Parallel()

	exec := &recordingExecutor{}
	cfg := DefaultConfig()
	_, err := NewComponent(exec, exec, &mockTabManager{}, cfg)
	require.NoError(t, err)

	got := exec.snapshotCmd()
	assert.Contains(t, got.Env, "TERM=xterm",
		"vte.Component must pin TERM=xterm (not xterm-256color) so "+
			"the spawned program (e.g. vim) emits SGR 30-37/90-97 "+
			"ANSI colors and lets the editor theme govern the "+
			"palette, while still finding Ss/Se in terminfo for "+
			"DECSCUSR. Got Env=%v", got.Env)
	for _, e := range got.Env {
		assert.NotEqual(t, "TERM=xterm-256color", e,
			"vte.Component must NOT advertise xterm-256color; "+
				"that would let programs use SGR 38;5;N which "+
				"bypasses the editor theme. Got Env=%v", got.Env)
	}
	assert.Contains(t, got.Env, "COLORTERM=",
		"vte.Component must clear COLORTERM so spawned programs "+
			"render with the 256-color ANSI palette and the "+
			"editor's theme remains the single source of truth "+
			"for colors. Got Env=%v", got.Env)
	for _, e := range got.Env {
		assert.NotEqual(t, "COLORTERM=truecolor", e,
			"vte.Component must NOT advertise truecolor; that "+
				"would let programs bypass the editor theme. "+
				"Got Env=%v", got.Env)
	}
}

// TestComponentAlternateScroll verifies AlternateScroll mirrors
// PrimaryScroll for the alternate buffer: callers can reach the rendered
// cells via Scroll.Buffer().RawCells() and the cursor via
// CursorAtScreen, without any cloning. The inference layer
// (term/vte/vteprobe) and exo's editorHandler rely on these two
// accessors plus IsAltBuffer to read the rendered grid of whichever
// screen the embedded program is drawing into.
func TestComponentAlternateScroll(t *testing.T) {
	t.Parallel()

	t.Run("primary", func(t *testing.T) {
		t.Parallel()
		comp, err := NewComponent(&testExecutor{}, &testExecutor{},
			&mockTabManager{}, DefaultConfig())
		require.NoError(t, err)
		ph := comp.parserHandler
		ph.sync.primBuf.SetDefaultChar(' ')
		ph.sync.altBuf.SetDefaultChar(' ')
		require.NoError(t, comp.Resize(5, 3))

		resetBuffer(t, ph, "abcde\nfg   \n     ")
		ph.setCursorAtScreen(term.Coordinates{X: 2, Y: 1})

		assert.False(t, comp.IsAltBuffer(),
			"primary must be the active buffer by default")
		scroll, mu := comp.PrimaryScroll()
		require.NotNil(t, scroll)
		require.NotNil(t, mu)
		assert.Equal(t, term.Coordinates{X: 2, Y: 1},
			comp.CursorAtScreen(),
			"primary cursor must match what was set")
		assert.Equal(t, "abcde\nfg   \n     ",
			term.CellsToString(scroll.Buffer().RawCells()),
			"primary cells must match the active buffer contents")
	})

	t.Run("alternate", func(t *testing.T) {
		t.Parallel()
		comp, err := NewComponent(&testExecutor{}, &testExecutor{},
			&mockTabManager{}, DefaultConfig())
		require.NoError(t, err)
		ph := comp.parserHandler
		ph.sync.primBuf.SetDefaultChar(' ')
		ph.sync.altBuf.SetDefaultChar(' ')
		require.NoError(t, comp.Resize(5, 3))

		// Swap to the alternate screen first, then populate it.
		ph.SetPrivateMode(vteparser.PrivateModeSwapScreenAndSetRestoreCursor)
		require.True(t, comp.IsAltBuffer(),
			"alt buffer must be active after DECSET 1049")
		resetBuffer(t, ph, "ALT  \nBUF  \n     ")
		ph.setCursorAtScreen(term.Coordinates{X: 3, Y: 0})

		scroll, mu := comp.AlternateScroll()
		require.NotNil(t, scroll)
		require.NotNil(t, mu)
		assert.Equal(t, term.Coordinates{X: 3, Y: 0},
			comp.CursorAtScreen(),
			"alt cursor must match what was set")
		assert.Equal(t, "ALT  \nBUF  \n     ",
			term.CellsToString(scroll.Buffer().RawCells()),
			"alt cells must match the active buffer contents")
	})
}

// TestComponentSnapshotIsConsistentUnderParserPressure regresses a
// exo bug where the editorHandler refreshed its vteprobe from two
// separate accessors (RawCells then CursorAtScreen). Each call locked
// Component.mu independently, so the parser goroutine could advance
// the grid between them and the resulting (cells, cursor) pair was
// from two different parser-callback boundaries — vteprobe.Cursor.Infer
// then confidently reported the wrong file line because its cursor
// argument referred to a state the cells did not match. The race
// detector did not see it: every read was properly synchronised, the
// inconsistency was semantic, not concurrent unsynchronised access.
//
// The invariant exercised below holds at every parser-callback
// boundary: immediately after Input, the row containing the cursor
// has at least cursor.X populated cells (Input both writes a cell
// and advances the cursor under one Lock). With a split snapshot
// the cursor can be sampled after a later Input while the cells were
// sampled before the row was rewritten, leaving cells[cursor.Y]
// shorter than the cursor demands.
func TestComponentSnapshotIsConsistentUnderParserPressure(t *testing.T) {
	t.Parallel()

	comp, err := NewComponent(&testExecutor{}, &testExecutor{},
		&mockTabManager{}, DefaultConfig())
	require.NoError(t, err)
	ph := comp.parserHandler
	ph.sync.primBuf.SetDefaultChar(' ')
	ph.sync.altBuf.SetDefaultChar(' ')
	require.NoError(t, comp.Resize(40, 4))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var writerWG sync.WaitGroup
	writerWG.Go(func() {
		row := 0
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			// Goto + Input together produce a state where row Y
			// has at least cursor.X populated cells immediately
			// after Input returns. Cycling rows keeps the cells
			// "short" so a stale cells snapshot is observably
			// shorter than the post-Input cursor demands.
			ph.Goto(row, 0)
			ph.ClearLine(vteparser.LineClearModeAll)
			ph.Input('x')
			row = (row + 1) % 4
		}
	})

	const iterations = 5000
	for range iterations {
		snap, err := comp.Snapshot()
		require.NoError(t, err)
		active := snap.Active()
		cells, cursor := active.Cells, active.Cursor
		if cursor.X == 0 {
			continue
		}
		require.Less(t, cursor.Y, len(cells),
			"cursor.Y must be within the snapshotted cells")
		row := cells[cursor.Y]
		assert.GreaterOrEqual(t, len(row), cursor.X,
			"the row holding the cursor must have at least cursor.X "+
				"populated cells — Input writes a cell and advances "+
				"the cursor under one lock, so a snapshot that "+
				"observes a cursor at column N must observe a row "+
				"with at least N cells. Seeing fewer means the "+
				"cursor was sampled from a later parser callback "+
				"than the cells.")
	}
	cancel()
	writerWG.Wait()
}

// TestComponentDrawSnapshotIsConsistentUnderParserPressure regresses the
// stale-overlay race at its source: exo paints the grid and probes a
// snapshot, then overlays highlights using that probe. When the paint
// and the snapshot came from two separate Component.mu acquisitions the
// parser goroutine could advance the grid in between, so the overlay was
// computed for a grid the paint had already scrolled past. DrawSnapshot
// paints and snapshots under one acquire; the snapshot it returns must
// therefore satisfy the same cells/cursor consistency invariant the
// split path could violate: the row holding the cursor has at least
// cursor.X populated cells.
func TestComponentDrawSnapshotIsConsistentUnderParserPressure(t *testing.T) {
	t.Parallel()

	comp, err := NewComponent(&testExecutor{}, &testExecutor{},
		&mockTabManager{}, DefaultConfig())
	require.NoError(t, err)
	ph := comp.parserHandler
	ph.sync.primBuf.SetDefaultChar(' ')
	ph.sync.altBuf.SetDefaultChar(' ')
	require.NoError(t, comp.Resize(40, 4))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var writerWG sync.WaitGroup
	writerWG.Go(func() {
		row := 0
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			ph.Goto(row, 0)
			ph.ClearLine(vteparser.LineClearModeAll)
			ph.Input('x')
			row = (row + 1) % 4
		}
	})

	writer := term.NewStringWriter(comp.width, comp.height)
	const iterations = 5000
	for range iterations {
		writer.Reset()
		snap, err := comp.DrawSnapshot(writer, nil)
		require.NoError(t, err)
		active := snap.Active()
		cells, cursor := active.Cells, active.Cursor
		if cursor.X == 0 {
			continue
		}
		require.Less(t, cursor.Y, len(cells),
			"cursor.Y must be within the snapshotted cells")
		assert.GreaterOrEqual(t, len(cells[cursor.Y]), cursor.X,
			"DrawSnapshot must return the very grid it painted: the row "+
				"holding the cursor must have at least cursor.X cells. "+
				"Fewer means the snapshot was taken from a different "+
				"parser state than the paint.")
	}
	cancel()
	writerWG.Wait()
}

func TestComponentPrimaryRingWrap(t *testing.T) {
	newComponent := func(t *testing.T) *Component {
		t.Helper()
		cfg := DefaultConfig()
		cfg.MaxLines = 6
		comp, err := NewComponent(&testExecutor{}, &testExecutor{},
			&mockTabManager{}, cfg)
		require.NoError(t, err)
		comp.parserHandler.sync.primBuf.SetDefaultChar(' ')
		comp.parserHandler.sync.altBuf.SetDefaultChar(' ')
		require.NoError(t, comp.Resize(4, 3))
		return comp
	}

	t.Run("draw snapshot and selection preserve logical order", func(t *testing.T) {
		comp := newComponent(t)
		comp.parser.AdvanceBytes([]byte(
			"0\r\n1\r\n2\r\n3\r\n4\r\n5\r\n6\r\n7\r\n8"))

		assertDraw(t, comp, "6   \n7   \n8   ")
		snap, err := comp.Snapshot()
		require.NoError(t, err)
		assert.Equal(t, "3   \n4   \n5   \n6   \n7   \n8   ",
			term.CellsToString(snap.Primary.Cells))

		selected, _, ok := comp.parserHandler.sync.primBuf.Cells.SelectLine(
			term.Coordinates{}, term.Coordinates{Y: 5})
		require.True(t, ok)
		assert.Equal(t, "3   \n4   \n5   \n6   \n7   \n8   ",
			term.CellsToString(selected))
	})

	t.Run("resize and wide writes after wrap", func(t *testing.T) {
		comp := newComponent(t)
		comp.parser.AdvanceBytes([]byte(
			"0\r\n1\r\n2\r\n3\r\n4\r\n5\r\n6\r\n7\r\n8"))
		require.NoError(t, comp.Resize(6, 3))
		comp.parser.AdvanceBytes([]byte("\r\n漢字"))

		assertDraw(t, comp, "7     \n8     \n漢 字   ")
		snap, err := comp.Snapshot()
		require.NoError(t, err)
		assert.Equal(t, "4     \n5     \n6     \n7     \n8     \n漢字  ",
			term.CellsToString(snap.Primary.Cells))
	})
}

func assertDraw(t *testing.T, comp *Component, expected string) {
	t.Helper()
	writer := term.NewStringWriter(comp.width, comp.height)
	comp.Draw(writer)
	writer.Flush()
	assert.Equal(t, expected, writer.String())
}

// TestComponentSelectionUnwrapsSoftWrappedLines pins that copying a
// logical line the emulator broke across rows to fit the width yields
// the original single line rather than one line per screen row, while
// rows separated by a real newline keep theirs.
func TestComponentSelectionUnwrapsSoftWrappedLines(t *testing.T) {
	t.Parallel()

	cases := []struct {
		desc     string
		from, to term.Coordinates
		want     string
	}{
		{
			desc: "soft wrapped line is joined back",
			from: term.Coordinates{},
			to:   term.Coordinates{Y: 1, X: 4},
			want: "abcdefghij",
		},
		{
			desc: "soft wrap joined, hard newline kept",
			from: term.Coordinates{},
			to:   term.Coordinates{Y: 2, X: 1},
			want: "abcdefghij\nxy",
		},
		{
			desc: "wrap continuation row starts a plain selection",
			from: term.Coordinates{Y: 1},
			to:   term.Coordinates{Y: 2, X: 1},
			want: "fghij\nxy",
		},
	}

	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()

			cfg := DefaultConfig()
			comp, err := NewComponent(&testExecutor{}, &testExecutor{},
				&mockTabManager{}, cfg)
			require.NoError(t, err)
			comp.parserHandler.sync.primBuf.SetDefaultChar(' ')
			comp.parserHandler.sync.altBuf.SetDefaultChar(' ')
			require.NoError(t, comp.Resize(5, 5))

			comp.parser.AdvanceBytes([]byte("abcdefghij\r\nxy"))
			assertDraw(t, comp, "abcde\nfghij\nxy   \n     \n     ")

			comp.Select(tc.from)
			comp.SelectEnd(tc.to)
			got, ok := comp.Selection()
			require.True(t, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestComponentWidenWhileScrolledUpKeepsContentVisible reproduces the
// black, frozen screen from catting a large file, narrowing the terminal
// (vertical splits), scrolling to the top, then widening it again.
// Widening unwraps the history into far fewer rows, but the view scroll
// kept its old, now out-of-bounds offset, so it converted to a negative
// start row and drew nothing — and could not be scrolled up out of. The
// offset must snap back to the top of history so the content stays
// visible and scrolling still works.
func TestComponentWidenWhileScrolledUpKeepsContentVisible(t *testing.T) {
	t.Parallel()

	comp, err := NewComponent(&testExecutor{}, &testExecutor{},
		&mockTabManager{}, DefaultConfig())
	require.NoError(t, err)
	comp.parserHandler.sync.primBuf.SetDefaultChar(' ')

	const narrow, wide, height, lines = 10, 40, 6, 100
	require.NoError(t, comp.Resize(narrow, height))

	var payload strings.Builder
	for i := range lines {
		payload.WriteString(strings.Repeat(string(rune('0'+i%10)), 24))
		payload.WriteString("\r\n")
	}
	comp.parser.AdvanceBytes([]byte(payload.String()))

	// Scroll all the way to the top of history.
	require.True(t, comp.ScrollUp(lines*4))
	require.False(t, comp.ScrollUp(1), "precondition: already at the top")
	require.Positive(t, comp.scroll.Offset().Y, "precondition: scrolled up into history")
	rowsNarrow := comp.parserHandler.sync.primBuf.Cells.Rows()

	// Widen ~2x, as a vertical split being closed would.
	require.NoError(t, comp.Resize(wide, height))
	require.Less(t, comp.parserHandler.sync.primBuf.Cells.Rows(), rowsNarrow,
		"precondition: widening unwraps history into fewer rows")

	writer := term.NewStringWriter(wide, height)
	comp.Draw(writer)
	writer.Flush()
	rows := strings.Split(writer.String(), "\n")

	nonBlank := 0
	for _, row := range rows {
		if strings.TrimSpace(row) != "" {
			nonBlank++
		}
	}
	assert.Positive(t, nonBlank, "widening while scrolled up must not black out the screen")
	assert.Equal(t, strings.Repeat("0", 24), strings.TrimRight(rows[0], " "),
		"the top of history must be visible after the widen")

	// The view is still live: the user can scroll back down toward the
	// bottom rather than being stuck.
	assert.True(t, comp.ScrollDown(1), "must be able to scroll back down after widening")
}

// TestComponentReverseScreen pins DECSCNM (CSI ? 5 h): like kitty and
// xterm, the whole screen is drawn in reverse video, including the cells
// no program has written, and cells already in SGR 7 flip back to normal
// video. vttest's "light background" pages rely on it.
func TestComponentReverseScreen(t *testing.T) {
	t.Parallel()

	// reverseMap renders each cell as 'r' (reverse video) or 'n' (normal
	// video).
	reverseMap := func(w *term.StringWriter, width int) string {
		var sb strings.Builder
		for i, c := range w.Cells() {
			if i != 0 && i%width == 0 {
				sb.WriteByte('\n')
			}
			if c.Attrs&term.AttrReverse != 0 {
				sb.WriteByte('r')
			} else {
				sb.WriteByte('n')
			}
		}
		return sb.String()
	}

	cases := []struct {
		desc   string
		input  string
		want   string
		report string
	}{
		{
			desc:   "normal video",
			input:  "ab\x1b[7mc\x1b[m",
			want:   "nnrn\nnnnn\nnnnn",
			report: "\x1b[?5;2$y",
		},
		{
			desc:   "reverse video fills the screen and cancels SGR 7",
			input:  "ab\x1b[7mc\x1b[m\x1b[?5h",
			want:   "rrnr\nrrrr\nrrrr",
			report: "\x1b[?5;1$y",
		},
		{
			desc:   "reverse video applies to the alternate screen",
			input:  "\x1b[?1049h\x1b[?5hab",
			want:   "rrrr\nrrrr\nrrrr",
			report: "\x1b[?5;1$y",
		},
		{
			desc:   "reset restores normal video",
			input:  "ab\x1b[7mc\x1b[m\x1b[?5h\x1b[?5l",
			want:   "nnrn\nnnnn\nnnnn",
			report: "\x1b[?5;2$y",
		},
		{
			desc:   "RIS restores normal video",
			input:  "\x1b[?5h\x1bcab",
			want:   "nnnn\nnnnn\nnnnn",
			report: "\x1b[?5;2$y",
		},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()

			// Without default attributes, as the default theme leaves
			// them, a row in the middle of the screen is only drawn once
			// written to, which a two-row screen would not show.
			const width, height = 4, 3
			comp, err := NewComponent(&testExecutor{}, &testExecutor{},
				&mockTabManager{}, DefaultConfig())
			require.NoError(t, err)
			require.NoError(t, comp.Resize(width, height))
			comp.parser.AdvanceBytes([]byte(tc.input))

			writer := term.NewStringWriter(width, height)
			comp.Draw(writer)
			assert.Equal(t, tc.want, reverseMap(writer, width))

			pty := comp.pty.Master.(*workspacetest.File)
			pty.Writes = nil
			comp.parser.AdvanceBytes([]byte("\x1b[?5$p"))
			var report strings.Builder
			for _, w := range pty.Writes {
				report.Write(w)
			}
			assert.Equal(t, tc.report, report.String(), "DECRQM")
		})
	}
}

// newPopulatedComponentForBench builds a component with a fully written
// grid so Snapshot/SnapshotInto copy a realistic amount of cells.
func newPopulatedComponentForBench(b *testing.B, width, height int) *Component {
	b.Helper()
	comp, err := NewComponent(&testExecutor{}, &testExecutor{},
		&mockTabManager{}, DefaultConfig())
	require.NoError(b, err)
	ph := comp.parserHandler
	ph.sync.primBuf.SetDefaultChar(' ')
	ph.sync.altBuf.SetDefaultChar(' ')
	require.NoError(b, comp.Resize(width, height))
	for row := range height {
		ph.Goto(row, 0)
		for col := range width {
			ph.Input(rune('a' + (col+row)%26))
		}
	}
	return comp
}

// BenchmarkSnapshot and BenchmarkSnapshotInto document the allocation
// win of reusing a destination grid: Snapshot deep-copies the whole grid
// each call, while SnapshotInto copies into caller-owned scratch and only
// allocates when a row must grow (never, in steady state).
func BenchmarkSnapshot(b *testing.B) {
	comp := newPopulatedComponentForBench(b, 80, 48)

	b.ReportAllocs()
	for b.Loop() {
		if _, err := comp.Snapshot(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSnapshotInto(b *testing.B) {
	comp := newPopulatedComponentForBench(b, 80, 48)

	var dst [][]term.Cell
	snap, err := comp.SnapshotInto(dst)
	require.NoError(b, err)
	dst = snap.Active().Cells

	b.ReportAllocs()
	for b.Loop() {
		snap, err := comp.SnapshotInto(dst)
		if err != nil {
			b.Fatal(err)
		}
		dst = snap.Active().Cells
	}
}

// ptySize captures a single SetPtySize call so tests can assert the
// component drives the pty winsize via the schemeapi.Terminal contract.
type ptySize struct {
	width, height           int
	pixelWidth, pixelHeight int
}

// expanderFunc adapts a plain function into the CommandExpander
// interface for tests.
type expanderFunc func(ctx context.Context, line string) (string, error)

func (f expanderFunc) ExpandCommand(ctx context.Context, line string) (string, error) {
	return f(ctx, line)
}

// TestComponentInitAsyncExpander pins the contract that, when a
// CommandExpander is configured, NewComponent returns immediately
// (without blocking the caller / event-loop goroutine) and the
// expander runs in a background goroutine before the foreign
// command is started. This is what lets `! echo $(sleep 10)` open
// the floating window instantly while the $(...) resolution
// continues in the background.
func TestComponentInitAsyncExpander(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	expanderEntered := make(chan struct{})
	cfg := DefaultConfig()
	cfg.CommandAndArgs = []string{"echo", "$(slow)"}
	cfg.CommandExpander = expanderFunc(func(ctx context.Context, line string) (string, error) {
		close(expanderEntered)
		<-release
		return "echo expanded", nil
	})

	exec := &recordingExecutor{}
	comp, err := NewComponent(exec, exec, &mockTabManager{}, cfg)
	require.NoError(t, err,
		"NewComponent must return immediately when CommandExpander "+
			"is set, before the expander completes")

	// The expander must have been entered in the background goroutine
	// (so the caller did not block on it).
	select {
	case <-expanderEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("expander was never invoked")
	}

	// The executor must not have received StartCommand yet, because
	// the expander has not returned.
	got := exec.snapshotCmd()
	assert.Empty(t, got.Path,
		"StartCommand must not run before the expander returns; "+
			"got Path=%q", got.Path)

	// Release the expander; the resolved command must reach the
	// executor.
	close(release)
	require.Eventually(t, func() bool {
		return exec.snapshotCmd().Path != ""
	}, 2*time.Second, 5*time.Millisecond,
		"StartCommand must run with the resolved line after the "+
			"expander returns")

	got = exec.snapshotCmd()
	assert.Equal(t, "echo", got.Path,
		"expanded line must be field-split into Path/Args; got %q", got.Path)
	assert.Equal(t, []string{"expanded"}, got.Args,
		"expanded line must be field-split into Path/Args; got %v", got.Args)

	_ = comp
}

// TestComponentInitAsyncExpanderErrorReachesWatcher pins that when
// the expander returns an error, no foreign command is started and
// the configured watcher fires with that error so plugin.Handler
// can surface the failure in the floating window.
func TestComponentInitAsyncExpanderErrorReachesWatcher(t *testing.T) {
	t.Parallel()

	expectedErr := errors.New("expander boom")
	watchCh := make(chan error, 1)
	cfg := DefaultConfig()
	cfg.CommandAndArgs = []string{"echo", "$(broken)"}
	cfg.CommandExpander = expanderFunc(func(ctx context.Context, line string) (string, error) {
		return "", expectedErr
	})
	cfg.Watcher = workspaceapi.ChanProcessWatcher(watchCh)

	exec := &recordingExecutor{}
	_, err := NewComponent(exec, exec, &mockTabManager{}, cfg)
	require.NoError(t, err)

	select {
	case got := <-watchCh:
		require.Error(t, got)
		assert.ErrorIs(t, got, expectedErr,
			"the expander error must reach the watcher so "+
				"plugin.Handler can transition into the done state")
	case <-time.After(2 * time.Second):
		t.Fatal("watcher never received the expander error")
	}

	assert.Empty(t, exec.snapshotCmd().Path,
		"StartCommand must not run when the expander errors")
}

func (e *recordingExecutor) SetPtySize(p workspaceapi.Pty, size workspaceapi.PtySize) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.setPtySize = append(e.setPtySize, ptySize{
		width: size.Columns, height: size.Rows,
		pixelWidth: size.PixelWidth, pixelHeight: size.PixelHeight,
	})
	return nil
}

// TestComponentResizeAfterCloseSkipsSetPtySize reproduces the bug where a
// closed Component still ioctl'd its master descriptor. Close releases the
// master, but the window manager keeps resizing the handler until the tab
// is removed (a paused idetask keeps its closed handler installed for the
// whole pause), so every resize reached SetPtySize on a dead fd and the
// workspace reported EBADF — or, once the number was recycled, ENOTTY on
// an unrelated file. Since resizes moved off the event loop the failure
// surfaced as an error toast per resize step.
func TestComponentResizeAfterCloseSkipsSetPtySize(t *testing.T) {
	t.Parallel()

	tm := mockTabManager{}
	exe := &recordingExecutor{}
	comp, err := NewComponent(exe, exe, &tm, DefaultConfig())
	require.NoError(t, err)

	require.NoError(t, comp.Resize(80, 24))
	require.NoError(t, comp.Close())

	exe.mu.Lock()
	exe.setPtySize = nil
	exe.mu.Unlock()

	require.NoError(t, comp.Resize(80, 23),
		"resizing a closed terminal is a no-op, not a failure")

	exe.mu.Lock()
	defer exe.mu.Unlock()
	assert.Empty(t, exe.setPtySize,
		"a closed Component must not ioctl its released master descriptor")
}

// parkedStartExecutor parks inside StartCommand, standing in for the
// window where the executor is building the child process from the
// pty descriptors it was handed.
type parkedStartExecutor struct {
	testExecutor
	entered chan struct{}
	release chan struct{}
}

func (e *parkedStartExecutor) StartCommand(
	context.Context, workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	close(e.entered)
	<-e.release
	return 1, nil
}

// TestComponentCloseWaitsForInflightStartCommand reproduces the data
// race between Close and the async spawn goroutine: the executor reads
// the slave descriptor while building the child (os/exec calls
// File.Fd), and Close closed that same *os.File without serializing
// against the hand-off. The race detector flagged it on CI as a
// concurrent File.Fd / File.Close on the pty slave.
func TestComponentCloseWaitsForInflightStartCommand(t *testing.T) {
	t.Parallel()

	exe := &parkedStartExecutor{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	cfg := DefaultConfig()
	cfg.CommandAndArgs = []string{"sh"}
	cfg.CommandExpander = expanderFunc(
		func(_ context.Context, line string) (string, error) {
			return line, nil
		})
	comp, err := NewComponent(exe, exe, &mockTabManager{}, cfg)
	require.NoError(t, err)

	select {
	case <-exe.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the spawn goroutine never reached StartCommand")
	}

	closed := make(chan error, 1)
	go debug.CapturePanicReport(func() { closed <- comp.Close() })

	select {
	case <-closed:
		t.Fatal("Close released the pty while the executor was still " +
			"building the child from its descriptors")
	case <-time.After(100 * time.Millisecond):
	}

	close(exe.release)
	select {
	case err := <-closed:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Close never completed after the spawn settled")
	}
}

func TestComponentCreatePtyDefersAsyncSpawnUntilInitialized(t *testing.T) {
	t.Parallel()

	invoked := make(chan struct{}, 1)
	cfg := DefaultConfig()
	cfg.CommandExpander = expanderFunc(func(_ context.Context, line string) (string, error) {
		invoked <- struct{}{}
		return line, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	comp := &Component{
		terminal: &testExecutor{}, executor: &testExecutor{},
		cfg: cfg, ctx: ctx, cancelCtx: cancel,
	}
	t.Cleanup(cancel)
	require.NoError(t, comp.createPty([]string{"sh"}))
	t.Cleanup(func() { require.NoError(t, comp.Close()) })

	// Drain any launched spawn so the assertion does not depend on scheduling.
	comp.WaitSpawn()
	select {
	case <-invoked:
		t.Fatal("PTY preparation launched the expander before synchronous initialization")
	default:
	}
}

// TestComponentRestoreFromSnapshotDrivesSetPtySize reproduces a bug where
// a freshly-restored Component would keep its pre-restore width/height (e.g.
// the warm-reservoir WidthHint or a stale workspace size propagated via
// Facility.Resize) without driving that value into the pty via SetPtySize.
// When the window manager subsequently called Resize with dimensions that
// happened to match those stale values (a common case: snapshot was taken
// at the same workspace size), Component.Resize early-returned and never
// reached SetPtySize. The pty winsize stayed at the kernel default, the
// shell wrote 1-column output into primBuf, and the user saw a vertical
// "main\n?\n)\nblue\n…" cascade until they manually resized the tile.
//
// The fix unifies restore through the regular Resize path: after restoring
// the cells, RestoreFromSnapshot drives the snapshot dimensions through
// Component.Resize so SetPtySize is invoked exactly once via the same
// well-tested code path used by the window manager.
func TestComponentRestoreFromSnapshotDrivesSetPtySize(t *testing.T) {
	t.Parallel()

	tm := mockTabManager{}
	exe := &recordingExecutor{}
	cfg := DefaultConfig()
	comp, err := NewComponent(exe, exe, &tm, cfg)
	require.NoError(t, err)

	const w, h = 80, 24

	// simulate the warm reservoir / Facility.Resize path: live dimensions
	// are already (w, h) before RestoreFromSnapshot is called.
	require.NoError(t, comp.Resize(w, h))
	require.Equal(t, w, comp.width)
	require.Equal(t, h, comp.height)

	exe.setPtySize = nil

	snap := Snapshot{
		Schema: terminalSnapshotVersion,
		Width:  w,
		Height: h,
		Primary: ScreenSnapshot{
			Cells:  [][]term.Cell{{{Ch: 'h'}, {Ch: 'i'}}},
			Cursor: term.Coordinates{X: 2, Y: 0},
		},
	}

	_, err = comp.RestoreFromSnapshot(snap)
	require.NoError(t, err)

	// RestoreFromSnapshot must drive the snapshot's dimensions through
	// the regular Resize path so SetPtySize is called even when the
	// live width/height already match the snapshot dimensions. Without
	// this, the pty stays at its kernel-default winsize and the shell
	// renders into a 0/1-column buffer.
	require.NotEmpty(t, exe.setPtySize,
		"RestoreFromSnapshot must call SetPtySize via the regular Resize path")
	assert.Equal(t, ptySize{width: w, height: h},
		exe.setPtySize[len(exe.setPtySize)-1])
	assert.Equal(t, w, comp.width)
	assert.Equal(t, h, comp.height)

	// The window manager's follow-up Resize at the tile's dimensions
	// (matching the snapshot dimensions) is now a legitimate no-op.
	exe.setPtySize = nil
	require.NoError(t, comp.Resize(w, h))
	assert.Empty(t, exe.setPtySize,
		"follow-up Resize at the same size is a legitimate no-op")
}

// TestComponentResizeDrivesPixelSize pins that the pty learns the pixel
// size graphics clients read from TIOCGWINSZ, and that a font change
// re-issues the ioctl for the new pixel size without resizing buffers.
func TestComponentResizeDrivesPixelSize(t *testing.T) {
	t.Parallel()

	tm := mockTabManager{}
	exe := &recordingExecutor{}
	cfg := DefaultConfig()
	cellW, cellH := 10, 20
	cfg.CellPixelSize = func() (int, int) { return cellW, cellH }
	comp, err := NewComponent(exe, exe, &tm, cfg)
	require.NoError(t, err)

	require.NoError(t, comp.Resize(80, 24))
	require.Len(t, exe.setPtySize, 1)
	assert.Equal(t, ptySize{width: 80, height: 24, pixelWidth: 800, pixelHeight: 480},
		exe.setPtySize[0])

	exe.setPtySize = nil
	require.NoError(t, comp.Resize(80, 24))
	assert.Empty(t, exe.setPtySize, "same cells and pixels is a no-op")

	cellW, cellH = 12, 24
	writeToBuffer(comp.parserHandler, "keep")
	require.NoError(t, comp.Resize(80, 24))
	require.Len(t, exe.setPtySize, 1, "a font change re-issues the ioctl")
	assert.Equal(t, ptySize{width: 80, height: 24, pixelWidth: 960, pixelHeight: 576},
		exe.setPtySize[0])
	assert.Equal(t, "keep", term.CellsToString([][]term.Cell{firstRowCells(comp.parserHandler)[:4]}),
		"the buffers are not resized when only the pixel size changed")
}

// TestComponentRestoreFromSnapshotCursorWithScrollback reproduces a bug
// where restoring a snapshot whose buffer has scrollback (rows > height)
// placed the cursor too high. The snapshot stores the cursor in screen
// coordinates (the contract vteprobe/exoeditor depend on), but Restore
// re-projected it through ScrollToWindowCoordinates, subtracting
// rows-height. CursorAtScreen is what DeviceStatus reports to zsh, so the
// shell cleared/redrew from the wrong row.
func TestComponentRestoreFromSnapshotCursorWithScrollback(t *testing.T) {
	t.Parallel()

	tm := mockTabManager{}
	exe := &recordingExecutor{}
	cfg := DefaultConfig()
	comp, err := NewComponent(exe, exe, &tm, cfg)
	require.NoError(t, err)

	const w, h = 80, 24
	require.NoError(t, comp.Resize(w, h))

	cells := make([][]term.Cell, 30)
	for i := range cells {
		cells[i] = []term.Cell{{Ch: 'x'}}
	}

	snap := Snapshot{
		Schema: terminalSnapshotVersion,
		Width:  w,
		Height: h,
		Primary: ScreenSnapshot{
			Cells:  cells,
			Cursor: term.Coordinates{X: 5, Y: 23},
		},
	}

	_, err = comp.RestoreFromSnapshot(snap)
	require.NoError(t, err)

	assert.Equal(t, term.Coordinates{X: 5, Y: 23}, comp.CursorAtScreen())
}

type pidExecutor struct {
	testExecutor
	startedPid workspaceapi.Pid
}

func (e *pidExecutor) StartCommand(
	context.Context, workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	return e.startedPid, nil
}

// TestComponentPidExposesStartedProcess asserts that Pid returns the pid
// of the started process, and 0 before any process is started.
func TestComponentPidExposesStartedProcess(t *testing.T) {
	t.Parallel()

	withProc := &pidExecutor{startedPid: 4242}
	comp, err := NewComponent(withProc, withProc, &mockTabManager{}, DefaultConfig())
	require.NoError(t, err)
	assert.Equal(t, workspaceapi.Pid(4242), comp.Pid())

	noProc := &pidExecutor{startedPid: 0}
	comp, err = NewComponent(noProc, noProc, &mockTabManager{}, DefaultConfig())
	require.NoError(t, err)
	assert.Equal(t, workspaceapi.Pid(0), comp.Pid())
}

// TestComponentResizeIsSerialized pins that Component.Resize takes
// Component.mu when mutating t.width/t.height. Other Component
// accessors (CursorVisible, ScrollDown, drawSelection via Selection,
// Snapshot) read t.width/t.height under t.mu, so writing them off-lock
// from Resize races the IDE event-loop reads. The race detector trips
// when Resize touches shared state without locking.
func TestComponentResizeIsSerialized(t *testing.T) {
	t.Parallel()

	comp, err := NewComponent(&testExecutor{}, &testExecutor{},
		&mockTabManager{}, DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, comp.Resize(20, 10))

	const iterations = 500
	stop := make(chan struct{})
	done := make(chan struct{})

	// Reader: takes Component.mu and reads t.height (CursorVisible at
	// component.go:305 references t.height under t.mu).
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = comp.CursorVisible()
		}
	}()

	// Writer: Component.Resize writes t.width/t.height. Must take
	// t.mu to serialize with the reader.
	for i := range iterations {
		w, h := 20, 10
		if i%2 == 0 {
			w, h = 30, 12
		}
		require.NoError(t, comp.Resize(w, h))
	}
	close(stop)
	<-done
}

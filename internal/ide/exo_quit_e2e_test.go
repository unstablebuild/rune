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

//go:build e2e

package ide

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/handler/handlertest"
	"unstable.build/rune/internal/ide/pkgtrust"
	"unstable.build/rune/internal/term/vte/vtereservoir"
	"unstable.build/rune/internal/text"
)

// TestE2EExoUserQuitAutoClosesTab boots a real IDE through ide.New
// with a working exo section, opens a file, types `:q<enter>` into
// the embedded editor and asserts that the tab is removed
// automatically once the editor process exits.
//
// The auto-close chain in production is: vim exits -> vte's
// comp.Run returns -> Handler publishes a term.EventNone via the
// host EventPublisher -> the host event loop calls root.Handle with
// that event -> WindowManager routes it to the focused Tab ->
// vte.Handler.Handle returns exit=true (because e.exit is set) ->
// Tab.Handle calls Component.RemoveTab(self), which drops the tab
// from c.buffers and tears the window down. This test stands in for
// the host event loop by intercepting the publish via
// WithPublishEvent and re-dispatching the event through root.Handle
// under the IDE locker.
func TestE2EExoUserQuitAutoClosesTab(t *testing.T) {
	bin, err := exec.LookPath("nvim")
	if err != nil {
		bin, err = exec.LookPath("vim")
		if err != nil {
			t.Skip("neither nvim nor vim available")
		}
	}

	dir := t.TempDir()
	canonical, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	dir = canonical
	dataDir := t.TempDir()

	configPath := filepath.Join(dataDir, "rune.yaml")
	require.NoError(t, os.WriteFile(configPath, fmt.Appendf(nil, `
editor:
  mode: exo
  exo:
    command: %s --cmd "set shortmess+=F" "+call cursor({line}, {col})" {file}
    goto: "<esc>:{line}<enter>{col}|"
    quit: "<esc>:q!<enter>"
command:
  key: "<c-\\\\>"
`, bin), 0o666))

	relFile := "exo.txt"
	filePath := filepath.Join(dir, relFile)
	require.NoError(t, os.WriteFile(filePath, []byte("hello\n"), 0o644))

	mu := new(sync.Mutex)
	scheduleNextTick, drainSchedule := newTestScheduler(t, mu)

	var rootRef atomic.Pointer[tui.Handler]
	publish := func(ev term.Event) bool {
		if ev.Type != term.EventNone {
			return true
		}
		rp := rootRef.Load()
		if rp == nil {
			return true
		}
		r := *rp
		scheduleNextTick(func() {
			r.Handle(ev)
		})
		return true
	}

	i, err := New(dir, configPath, dataDir, pkgtrust.NewStore(dataDir, nil), newTestStorage(t, dataDir),
		WithLocker(mu),
		WithScheduleNextTick(scheduleNextTick),
		WithBell(func() {}),
		WithStreamingOpen(true),
		WithPublishEvent(publish),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = i.Close() })

	root := i.Ready()
	rootRef.Store(&root)
	// A realistic terminal size: on anything tiny the editor pages
	// its long file-info message through `-- More --` and waits for
	// a keypress before ever showing the file.
	mu.Lock()
	root.Resize(80, 24)
	mu.Unlock()
	drainSchedule()
	i.WaitWorkspaces()

	sendKeys := func(seq string) {
		t.Helper()
		keys, err := term.ParseKeys(seq)
		require.NoError(t, err)
		for _, k := range keys {
			ev := term.Event{
				Type: term.EventKey, Ch: k.Ch, Mod: k.Mod, Key: k.Key,
			}
			switch {
			case k.Key == term.KeyEsc:
				ev.Raw = []byte{0x1b}
			case k.Key == term.KeyEnter:
				ev.Raw = []byte{0x0d}
			case k.Key == term.KeySpace:
				ev.Raw = []byte{' '}
			case k.Mod == term.ModCtrl && k.Ch == '\\':
				ev.Raw = []byte{0x1c}
			case k.Mod == term.ModCtrl && k.Ch >= 'a' && k.Ch <= 'z':
				ev.Raw = []byte{byte(k.Ch - 'a' + 1)}
			case k.Ch != 0:
				ev.Raw = []byte(string(k.Ch))
			}
			mu.Lock()
			root.Handle(ev)
			mu.Unlock()
			i.WaitInflight()
		}
	}

	tabCount := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(i.workspaceHandler.focusEx().comp.Tabs())
	}

	sendKeys(`<c-\\>edit<space>` + relFile + `<enter>`)

	require.Eventually(t, func() bool {
		return tabCount() > 0
	}, 10*time.Second, 100*time.Millisecond,
		"exo file tab must be open before quitting the editor")

	// Wait for the streaming-open swap to land so the tab's
	// handler is the exo editor rather than the deferHandler /
	// streamload pair. Without this gate the publish dispatch
	// below can race text.(*Component).openFileTabStreaming's
	// deferred sh.Close() with streamload.Handle (RUNE-205).
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		tabs := i.workspaceHandler.focusEx().comp.Tabs()
		if len(tabs) == 0 {
			return false
		}
		_, streaming := tabs[0].Handler().(interface {
			Swap(text.Handler)
		})
		return !streaming
	}, 10*time.Second, 50*time.Millisecond,
		"streaming-open swap must complete before quitting the editor")

	// Wait for the editor to draw the opened file before quitting
	// it. Vim's startup blocks for seconds waiting for terminal
	// query responses, and any input arriving during that window is
	// consumed as response data — a fixed sleep either wastes time
	// or lands inside the window and loses the quit sequence.
	drawFrame := func() string {
		w := term.NewStringWriter(80, 24)
		require.NoError(t, w.Clear(term.Attributes{}))
		mu.Lock()
		root.Draw(w)
		mu.Unlock()
		require.NoError(t, w.Flush())
		return w.String()
	}
	require.Eventually(t, func() bool {
		return strings.Contains(drawFrame(), "hello")
	}, 30*time.Second, 200*time.Millisecond,
		"editor must draw the opened file before we quit it")

	// Drawing the file is still not proof the editor accepts input:
	// vim keeps waiting several seconds for terminal query responses
	// after its first draw and consumes anything typed in that
	// window as response data. Probe with <c-g>, which is harmless
	// and echoes the quoted file name only once vim processes input
	// normally.
	require.Eventually(t, func() bool {
		sendKeys(`<c-g>`)
		return strings.Contains(drawFrame(), `"`+relFile+`"`)
	}, 30*time.Second, 300*time.Millisecond,
		"editor must respond to input before we quit it")

	sendKeys(`<esc>:q<enter>`)

	require.Eventually(t, func() bool {
		return tabCount() == 0
	}, 10*time.Second, 100*time.Millisecond,
		"exo tab must auto-close after :q<enter> exits %s; if "+
			"this assertion fails the vte exit publish -> "+
			"root.Handle -> Tab.Handle -> RemoveTab chain is "+
			"broken", bin)

	handlertest.RunHandlerSequence(t, &lockedHandler{Handler: root, mu: mu},
		20, 8, []handlertest.SequenceTestCase{{
			InputSequence: "",
			Expected: `┌──────────────────┐
│                  │
├──────────────────┤
│                  │
│                  │
│                  │
│                  │
└──────────────────┘`,
		}})
}

// TestE2ETerminalExitDropsUnfocusedTerminal is the counterpart to the
// test above for a terminal that is not in focus. The chain documented
// there — publish term.EventNone, dispatch through root.Handle, let the
// WindowManager route it to the focused handler — can only ever reach
// whatever holds focus, so an unfocused terminal whose child exited
// stayed on screen until the user happened to focus it. The push
// through browser.TabManager.OnTabExit has to reach the browser with no
// event routing at all.
//
// The terminal is opened as plain window content rather than as a tab
// because that is what :terminalnew installs, and the assertion spans
// every hop the notification takes (vte.Component.Run -> tabNameAliaser
// resolving the pty URI to the tab key -> workspaceTabManager ->
// browser.Component); each hop is otherwise only covered against a stub.
func TestE2ETerminalExitDropsUnfocusedTerminal(t *testing.T) {
	dir := t.TempDir()
	dataDir := t.TempDir()

	configPath := filepath.Join(dataDir, "rune.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(`
editor:
  mode: modal
command:
  key: "<c-\\\\>"
`), 0o666))

	// The child prints a marker so the test can wait for a live shell,
	// then blocks until the test releases it. Releasing it from Go
	// rather than by typing keeps the exit independent of focus, which
	// is the whole point of the test.
	release := filepath.Join(dir, "release")
	script := filepath.Join(dir, "wait.sh")
	require.NoError(t, os.WriteFile(script, []byte(
		"#!/bin/sh\n"+
			"echo READY\n"+
			"while [ ! -f "+release+" ]; do sleep 1; done\n",
	), 0o755))

	mu := new(sync.Mutex)
	scheduleNextTick, drainSchedule := newTestScheduler(t, mu)

	i, err := New(dir, configPath, dataDir, pkgtrust.NewStore(dataDir, nil),
		newTestStorage(t, dataDir),
		WithLocker(mu),
		WithScheduleNextTick(scheduleNextTick),
		WithPublishEvent(func(term.Event) bool { return true }),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = i.Close() })

	root := i.Ready()
	mu.Lock()
	root.Resize(80, 24)
	mu.Unlock()
	drainSchedule()
	i.WaitWorkspaces()

	sendKeys := func(seq string) {
		t.Helper()
		keys, err := term.ParseKeys(seq)
		require.NoError(t, err)
		for _, k := range keys {
			mu.Lock()
			root.Handle(term.Event{
				Type: term.EventKey, Ch: k.Ch, Mod: k.Mod, Key: k.Key,
			})
			mu.Unlock()
			i.WaitInflight()
		}
	}

	terminalWindow := func() (id uint64, cells string, found bool) {
		mu.Lock()
		defer mu.Unlock()
		i.workspaceHandler.focusEx().comp.Browser().IterateWindows(
			func(win browser.Window) {
				if found {
					return
				}
				content, cerr := win.Content()
				if cerr != nil {
					return
				}
				v, isVTE := content.(vtereservoir.VTE)
				if !isVTE {
					return
				}
				snap, sErr := v.Snapshot()
				if sErr != nil {
					return
				}
				id, cells, found = win.WindowID(),
					term.CellsToString(snap.ActiveCells()), true
			})
		return id, cells, found
	}

	countWindows := func() (n int) {
		mu.Lock()
		defer mu.Unlock()
		i.workspaceHandler.focusEx().comp.Browser().
			IterateWindows(func(browser.Window) { n++ })
		return n
	}

	focusedWindow := func() uint64 {
		mu.Lock()
		defer mu.Unlock()
		return i.workspaceHandler.focusEx().comp.Browser().Focus().WindowID()
	}

	sendKeys(`<c-\\>terminalnew<space>` + script + `<enter>`)

	var termWin uint64
	require.Eventually(t, func() bool {
		id, cells, ok := terminalWindow()
		if !ok || !strings.Contains(cells, "READY") {
			return false
		}
		termWin = id
		return true
	}, 30*time.Second, 50*time.Millisecond,
		"terminal did not start the child process")

	sendKeys(`<c-\\>windownew<space>right<enter>`)

	require.Equal(t, 2, countWindows(),
		"expected the terminal window plus a new one")
	require.NotEqual(t, termWin, focusedWindow(),
		"the terminal must be unfocused for this test to mean anything")

	// Nothing is typed from here on, so the terminal's Handle is never
	// called: only the push can drop it.
	require.NoError(t, os.WriteFile(release, nil, 0o644))

	require.Eventually(t, func() bool {
		drainSchedule()
		_, _, stillThere := terminalWindow()
		return !stillThere
	}, 30*time.Second, 50*time.Millisecond,
		"terminal whose child exited was not dropped while unfocused")

	require.Equal(t, 2, countWindows(),
		"dropping the terminal must swap the window's content, not close the window")
}

// lockedHandler serializes Handle/Draw/Resize/Cursor on the shared
// IDE locker so handlertest.RunHandlerSequence does not race
// scheduled callbacks that the test scheduler runs under the same
// mutex.
type lockedHandler struct {
	tui.Handler
	mu sync.Locker
}

func (h *lockedHandler) Handle(ev term.Event) (bool, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.Handler.Handle(ev)
}

func (h *lockedHandler) Draw(w term.Writer) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.Handler.Draw(w)
}

func (h *lockedHandler) Resize(width, height int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.Handler.Resize(width, height)
}

func (h *lockedHandler) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.Handler.Cursor()
}

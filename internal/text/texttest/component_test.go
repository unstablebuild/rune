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

package texttest

import (
	"context"
	"errors"
	"os"
	"os/user"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/component/comptest"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	sdkiterator "github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	gomock "go.uber.org/mock/gomock"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/browser/browsertest"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/extension/extutil"
	"unstable.build/rune/internal/handler/command"
	"unstable.build/rune/internal/handler/handlertest"
	hmarkdown "unstable.build/rune/internal/handler/markdown"
	"unstable.build/rune/internal/ide/idecmd"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/standard"
	"unstable.build/rune/internal/workspace"
	"unstable.build/rune/internal/workspace/workspacetest"
)

var (
	keya = term.KeyComb{Ch: 'a'}
)

// awaitErr blocks on a (chan error, error) pair and collapses it into
// a single error so existing sync-style assertions keep working
// against the async FlusherCloser API.
func awaitErr(ch <-chan error, err error) error {
	if err != nil {
		return err
	}
	return <-ch
}

type testFlusherCloser struct {
	buf      *cell.Buffer
	closeFn  func() error
	flushFn  func() error
	reloadFn func() error
	content  string
	// reloadContent, when set, is the buffer content installed by Reload.
	// It lets a test open a file with one content and reload to a different
	// (e.g. shorter) content, exercising stale-cursor handling.
	reloadContent string
	lastFlush     time.Time
}

func (t *testFlusherCloser) Close() error {
	if t.closeFn != nil {
		return t.closeFn()
	}
	return nil
}
func (t *testFlusherCloser) Flush(context.Context) (<-chan error, error) {
	var err error
	if t.flushFn != nil {
		err = t.flushFn()
	}
	return doneChanErr(err), nil
}

func (t *testFlusherCloser) ForceFlush(context.Context) (<-chan error, error) {
	var err error
	if t.flushFn != nil {
		err = t.flushFn()
	}
	return doneChanErr(err), nil
}

func (t *testFlusherCloser) LastFlush() time.Time {
	return t.lastFlush
}

func (t *testFlusherCloser) Reload(context.Context) (<-chan error, error) {
	var err error
	if t.reloadFn != nil {
		err = t.reloadFn()
	} else if t.reloadContent != "" {
		t.buf.Replace(t.reloadContent)
	} else if t.content != "" {
		t.buf.Replace(t.content)
	}
	return doneChanErr(err), nil
}

func doneChanErr(err error) <-chan error {
	ch := make(chan error, 1)
	ch <- err
	close(ch)
	return ch
}

type testLoader struct {
	openFile      workspaceapi.File
	content       string
	flusherCloser *testFlusherCloser
	expectError   error
	// reloadContent, when set, is propagated to the testFlusherCloser that
	// Load creates, so Reload installs it instead of re-installing content.
	reloadContent string
}

type markdownReloadFile struct {
	workspaceapi.File
	beforeRead time.Time
	afterRead  time.Time
	read       bool
}

func (f *markdownReloadFile) Read(data []byte) (int, error) {
	f.read = true
	return f.File.Read(data)
}

func (f *markdownReloadFile) Stat() (os.FileInfo, error) {
	info, err := f.File.Stat()
	if err != nil {
		return nil, err
	}
	modTime := f.beforeRead
	if f.read {
		modTime = f.afterRead
	}
	return markdownReloadFileInfo{FileInfo: info, modTime: modTime}, nil
}

type markdownReloadFileInfo struct {
	os.FileInfo
	modTime time.Time
}

func (f markdownReloadFileInfo) ModTime() time.Time {
	return f.modTime
}

type testOpenRouter struct {
	h        browserapi.Handler
	handled  bool
	err      error
	called   int
	uri      workspaceapi.URI
	readOnly bool
}

func (t *testOpenRouter) RouteOpen(
	uri workspaceapi.URI, readOnly bool,
) (browserapi.Handler, bool, error) {
	t.called++
	t.uri = uri
	t.readOnly = readOnly
	return t.h, t.handled, t.err
}

func (t *testLoader) Remove(string) error {
	return nil
}

func (t *testLoader) StartCommand(context.Context, workspaceapi.Cmd) (workspaceapi.Pid, error) {
	panic("unimplemented")
}

func (t *testLoader) Signal(workspaceapi.Pid, syscall.Signal) error {
	panic("unimplemented")
}

func (t *testLoader) Close() error {
	panic("unimplemented")
}

func (t *testLoader) Load(
	file workspaceapi.URI, buf *cell.Buffer, swapDir workspaceapi.URI, readOnly bool,
) (workspace.FlusherCloser, error) {
	if t.expectError != nil {
		return nil, t.expectError
	}
	if t.flusherCloser != nil {
		return t.flusherCloser, nil
	}
	if t.content != "" {
		buf.WriteString(t.content)
	}
	return &testFlusherCloser{
		buf:           buf,
		content:       t.content,
		reloadContent: t.reloadContent,
	}, nil
}

func (t *testLoader) Recover(
	file, swapFilePath workspaceapi.URI, buf *cell.Buffer, force bool,
) (workspace.FlusherCloser, error) {
	return t.Load(file, buf, workspaceapi.URI{}, false)
}

func (t *testLoader) URI(path string) (workspaceapi.URI, error) {
	panic("unused")
}

func (t *testLoader) OpenFile(path string, flag int, perm os.FileMode) (
	workspaceapi.File, error,
) {
	if t.openFile == nil {
		return nil, errors.New("not found")
	}
	return t.openFile, nil
}

func (t *testLoader) Open(path string) (workspaceapi.File, error) {
	return t.OpenFile(path, os.O_RDONLY, 0)
}

func (t *testLoader) Stat(path string) (os.FileInfo, error) {
	if t.openFile == nil {
		return nil, os.ErrNotExist
	}
	return t.openFile.Stat()
}

func (t *testLoader) ReadDir(name string) ([]os.DirEntry, error) {
	panic("unused")
}

func newTestComponentErr(ed text.Editor, cfg text.Config) (*text.Component, *testLoader, error) {
	loader := &testLoader{}
	if cfg.ScheduleNextTick == nil {
		cfg.ScheduleNextTick = func(fn func()) bool { fn(); return true }
	}
	c, err := text.NewComponent(ed, loader, cfg)
	if err != nil {
		return nil, nil, err
	}
	return c, loader, nil
}

func newTestComponent(t *testing.T, ed text.Editor) (*text.Component, *testLoader) {
	return newTestComponentConfig(t, ed, text.DefaultConfig())
}

func newTestComponentConfig(t *testing.T, ed text.Editor, cfg text.Config) (
	*text.Component, *testLoader,
) {
	cfg.NoMaxSize = false
	if cfg.ScheduleNextTick == nil {
		// text.Component requires a non-nil scheduler at construction.
		// Tests that don't care about event-loop ordering get the
		// trivial inline runner; tests that drive concurrent UI
		// mutations must pass their own.
		cfg.ScheduleNextTick = func(fn func()) bool { fn(); return true }
	}
	c, loader, err := newTestComponentErr(ed, cfg)
	require.NoError(t, err)
	return c, loader
}

func TestComponentInterfaces(t *testing.T) {
	// this test is just a compile-time test
	cfgc := text.DefaultConfig()
	cfgc.ScheduleNextTick = func(fn func()) bool { fn(); return true }
	c, err := text.NewComponent(NopEditor(), &testLoader{}, cfgc)
	require.NoError(t, err)

	var ed text.Editor
	ed = c

	var b browser.Browser
	b = c

	var comp tui.Component
	comp = c

	// use so compiler does not complain
	ed.Edit(context.Background(), workspaceapi.URI{}, cell.NewBuffer(), false, false)
	_, _ = b.Focus()
	comp.Resize(0, 0)
}

func TestComponentCommandKeyBinding(t *testing.T) {
	t.Run("on a non-mapped event returns false", func(t *testing.T) {
		c, _ := newTestComponent(t, NopEditor())
		_, ok := c.CommandKeyBinding(keya)
		assert.False(t, ok)
	})

	t.Run("returns mapped command", func(t *testing.T) {
		config := text.DefaultConfig()
		config.ScheduleNextTick = func(fn func()) bool { fn(); return true }
		config.CommandKeyBindings[keya] = [][]string{{"myCmd"}}
		c, _ := newTestComponentConfig(t, NopEditor(), config)
		cmd, ok := c.CommandKeyBinding(keya)
		assert.True(t, ok)
		assert.Equal(t, [][]string{{"myCmd"}}, cmd)
	})

	t.Run("returns mapped command and args", func(t *testing.T) {
		config := text.DefaultConfig()
		config.ScheduleNextTick = func(fn func()) bool { fn(); return true }
		config.CommandKeyBindings[keya] = [][]string{{"myCmd", "1"}}
		c, _ := newTestComponentConfig(t, NopEditor(), config)
		cmd, ok := c.CommandKeyBinding(keya)
		assert.True(t, ok)
		assert.Equal(t, [][]string{{"myCmd", "1"}}, cmd)
	})

	t.Run("returns multiple mapped commands and args", func(t *testing.T) {
		config := text.DefaultConfig()
		config.ScheduleNextTick = func(fn func()) bool { fn(); return true }
		config.CommandKeyBindings[keya] = [][]string{
			{"myCmd", "1"},
			{"GZA", "Duel Of The Iron Mic", "Masta Killa", "Dreddy Kruger"},
		}
		c, _ := newTestComponentConfig(t, NopEditor(), config)
		cmd, ok := c.CommandKeyBinding(keya)
		assert.True(t, ok)
		assert.Equal(t, [][]string{
			{"myCmd", "1"},
			{"GZA", "Duel Of The Iron Mic", "Masta Killa", "Dreddy Kruger"},
		}, cmd)
	})
}

func newTestComponentWithFile(
	t *testing.T, filename string,
) (*text.Component, *testLoader, browserapi.Handler, workspaceapi.URI) {
	uri, err := workspaceapi.ParseURI(filename)
	require.NoError(t, err)
	c, loader := newTestComponent(t, NopEditor())
	h, err := c.Open(uri)
	require.NoError(t, err)
	return c, loader, h, uri
}

func TestComponentOpen(t *testing.T) {
	t.Run("opens a new tab", func(t *testing.T) {
		myName := "file:///tmp/Its_1am_and_Im_very_tired.go"
		c, _, h, uri := newTestComponentWithFile(t, myName)

		h2, ok := c.Browser().Tab(uri)
		assert.True(t, ok)
		assert.Equal(t, h, h2)
	})

	t.Run("remote file tab uses basename like a local file", func(t *testing.T) {
		c, _, _, uri := newTestComponentWithFile(
			t, "ssh://user@host:22/home/user/project/main.go",
		)

		_, defName, ok := c.Browser().TabName(uri)
		require.True(t, ok)
		assert.Equal(t, "main.go", defName)
	})

	t.Run("it's idempotent", func(t *testing.T) {
		myName := "file:///var/music/La_Rosalia.mp3"

		c, loader, h, uri := newTestComponentWithFile(t, myName)
		_, ok := c.Browser().Tab(uri)
		assert.True(t, ok)

		loader.expectError = workspaceapi.ErrFileAlreadyOpen

		h2, err := c.Open(uri)
		require.NoError(t, err)
		assert.Equal(t, h, h2)
	})

	t.Run("it's idempotent 2", func(t *testing.T) {
		myName := "file:///tmp/Its_1am_and_Im_very_tired.go"
		c, _, _, uri := newTestComponentWithFile(t, myName)

		t2, ok := c.Browser().Tab(uri)
		require.True(t, ok)

		b3, err := c.Open(uri)
		require.NoError(t, err)
		assert.Equal(t, t2, b3)
	})

	t.Run("bubbles up open file error", func(t *testing.T) {
		c, loader, _, _ := newTestComponentWithFile(t, "file:///tmp/lmao")
		myErr := errors.New("oopsie daisy")
		loader.expectError = myErr

		uri, err := workspaceapi.ParseURI("file:///Holmes.xd")
		require.NoError(t, err)
		_, err = c.Open(uri)
		require.Error(t, err)
	})

	t.Run("if file is already open it returns its handler", func(t *testing.T) {
		c, loader, h1, uri := newTestComponentWithFile(t, "file:///tmp/wasup")
		loader.expectError = errors.New("should not be called")

		h2, err := c.Open(uri)
		require.NoError(t, err)
		assert.Equal(t, h1, h2)
	})

	t.Run("opens recovery prompt if err == workspaceapi.ErrFileAlreadyOpen", func(t *testing.T) {
		c, loader, _, _ := newTestComponentWithFile(t, "file:///tmp/wasup")
		c.Resize(30, 20)

		tests := []comptest.TestCase{
			{nil, `
┌────────────────────────────┐
│o wasup                     │
├────────────────────────────┤
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
└────────────────────────────┘`,
			},
			{func() {
				uri, err := workspaceapi.ParseURI("file:///tmp/busy")
				require.NoError(t, err)

				loader.expectError = workspaceapi.ErrFileAlreadyOpen
				_, err = c.Open(uri)
				require.Equal(t, workspaceapi.ErrFileAlreadyOpen, err)
			}, `
┌────────────────────────────┐
│o wasup                     │
├────────────────────────────┤
│                            │
│                            │
│                            │
█●████████████████████████████
│                            │
│  File file:///tmp/busy is  │
│  already open by another   │
│  process or an edit        │
│  session for this file     │
│  crashed.                  │
│                            │
│Open rdonly      Force Edit │
└────────────────────────────┘
│                            │
│                            │
│                            │
└────────────────────────────┘`,
			},
			{func() {
				loader.expectError = nil
				_, handled := c.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
				assert.True(t, handled)
			}, `
┌─────────━━━━━━─────────────┐
│o wasup  o busy             │
├────────────────────────────┤
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
└────────────────────────────┘`,
			},
			{func() { // test double prompt, switches focuses correctly
				loader.expectError = workspaceapi.ErrFileAlreadyOpen

				uri1, err := workspaceapi.ParseURI("file:///tmp/m")
				require.NoError(t, err)

				_, err = c.Open(uri1)
				require.Equal(t, workspaceapi.ErrFileAlreadyOpen, err)

				uri2, err := workspaceapi.ParseURI("file:///tmp/more")
				require.NoError(t, err)

				_, err = c.Open(uri2)
				require.Equal(t, workspaceapi.ErrFileAlreadyOpen, err)
			}, `
┌────────────────────────────┐
│o wasup  o busy             │
├────────────────────────────┤
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
█●████████████████████████████
│                            │
│  File file:///tmp/more is  │
│  already open by another   │
│  process or an edit        │
│  session for this file     │
│  crashed.                  │
│                            │
│Open rdonly      Force Edit │
└────────────────────────────┘
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
└────────────────────────────┘`,
			},
			{func() {
				loader.expectError = nil
				_, handled := c.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
				assert.True(t, handled)
			}, `
┌────────────────────────────┐
│o wasup  o busy  o more     │
├────────────────────────────┤
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
█●████████████████████████████
│                            │
│  File file:///tmp/m is     │
│  already open by another   │
│  process or an edit        │
│  session for this file     │
│  crashed.                  │
│                            │
│Open rdonly      Force Edit │
└────────────────────────────┘
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
└────────────────────────────┘`,
			},
			{func() {
				loader.expectError = nil
				_, handled := c.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
				assert.True(t, handled)
			}, `
┌───────────────────────━━━──┐
│o wasu  o busy  o mor  o m  │
├────────────────────────────┤
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
└────────────────────────────┘`,
			},
		}

		w := term.NewStringWriter(30, 20)
		comptest.TestComponent(t, c, w, tests)

	})

	t.Run("recovery prompt installs recovered file in tile above finder split", func(t *testing.T) {
		// Reproduces RUNE-139: when a finder/search-style split is the
		// focused window and Open returns ErrFileAlreadyOpen, the recovery
		// prompt must NOT install the recovered file into the finder's
		// split window. Instead, the file must end up in a sibling
		// (tab-bearing) tile.
		c, loader, _, originalURI := newTestComponentWithFile(t, "file:///tmp/wasup")
		c.Resize(30, 20)

		// Simulate the finder splitting the original window: create a
		// non-tab tiled handler in a new bottom split that takes focus,
		// like cmd/extension_fuzzy_search.splitCommandHandler does.
		focus, err := c.Focus()
		require.NoError(t, err)
		finderHandler := browser.NopHandler(handler.Nop())
		finderWin, err := c.Split(browserapi.OrientationBottom, focus, finderHandler)
		require.NoError(t, err)

		// finder split is now in focus. Confirm focus is the finder
		// window, not the original tab window.
		curFocus, err := c.Focus()
		require.NoError(t, err)
		assert.Equal(t, finderWin.WindowID(), curFocus.WindowID(),
			"finder window should be focused after Split takes focus")

		// Now Open a busy file from inside the finder context. The
		// expectation is that the recovery prompt is shown and that on
		// answering it the recovered file is installed into the
		// original tile, NOT the finder's tile.
		busyURI, err := workspaceapi.ParseURI("file:///tmp/busy")
		require.NoError(t, err)
		loader.expectError = workspaceapi.ErrFileAlreadyOpen
		_, err = c.Open(busyURI)
		require.Equal(t, workspaceapi.ErrFileAlreadyOpen, err)

		// Answer the recovery prompt with "Open rdonly" (second option,
		// safest choice that does not touch the swap file).
		loader.expectError = nil
		_, handled := c.Handle(term.Event{Type: term.EventKey, Ch: 'O'})
		assert.True(t, handled)

		// The original tile (which held the wasup tab) should now show
		// the busy tab, NOT the finder window.
		busyTab, ok := c.Browser().Tab(busyURI)
		require.True(t, ok, "busy tab should have been created")

		// Find which window owns the busy tab now: it must be the
		// original tile, not the finder's tile.
		var busyWin browser.Window
		c.Browser().IterateWindows(func(w browser.Window) {
			h, _ := w.Content()
			if h == busyTab {
				busyWin = w
			}
		})
		require.NotNil(t, busyWin, "no window holds busy tab")
		assert.NotEqual(t, finderWin.WindowID(), busyWin.WindowID(),
			"recovered file must not land in the finder window")

		// And the original tile (which had wasup) must be the one now
		// showing busy, since the prompt's intended target is "the
		// window above the finder".
		_ = originalURI
	})

	t.Run("routes before opening recovery prompt if err == workspaceapi.ErrFileAlreadyOpen", func(t *testing.T) {
		cfg := text.DefaultConfig()
		cfg.ScheduleNextTick = func(fn func()) bool { fn(); return true }
		router := new(testOpenRouter)
		cfg.OpenRouter = router
		c, loader := newTestComponentConfig(t, NopEditor(), cfg)
		c.Resize(30, 20)

		busy, err := workspaceapi.ParseURI("file:///tmp/busy")
		require.NoError(t, err)
		routed := browser.NopHandler(handler.Nop())
		router.h = routed
		router.handled = true

		loader.expectError = workspaceapi.ErrFileAlreadyOpen
		h, err := c.Open(busy)
		require.NoError(t, err)
		assert.Equal(t, routed, h)
		assert.Equal(t, 1, router.called)
		assert.True(t, router.uri.Equal(busy))
		assert.False(t, router.readOnly)
		w := term.NewStringWriter(30, 20)
		c.Draw(w)
		require.NoError(t, w.Flush())
		assert.NotContains(t, w.String(), "already open by another")
	})

	t.Run("falls back to recovery prompt when router does not handle open", func(t *testing.T) {
		cfg := text.DefaultConfig()
		cfg.ScheduleNextTick = func(fn func()) bool { fn(); return true }
		router := new(testOpenRouter)
		cfg.OpenRouter = router
		c, loader := newTestComponentConfig(t, NopEditor(), cfg)
		c.Resize(30, 20)

		busy, err := workspaceapi.ParseURI("file:///tmp/busy")
		require.NoError(t, err)
		loader.expectError = workspaceapi.ErrFileAlreadyOpen
		_, err = c.Open(busy)
		require.Equal(t, workspaceapi.ErrFileAlreadyOpen, err)
		assert.Equal(t, 1, router.called)

		tests := []comptest.TestCase{
			{nil, `
┌────────────────────────────┐
│                            │
├────────────────────────────┤
│                            │
│                            │
│                            │
█●████████████████████████████
│                            │
│  File file:///tmp/busy is  │
│  already open by another   │
│  process or an edit        │
│  session for this file     │
│  crashed.                  │
│                            │
│Open rdonly      Force Edit │
└────────────────────────────┘
│                            │
│                            │
│                            │
└────────────────────────────┘`},
		}

		w := term.NewStringWriter(30, 20)
		comptest.TestComponent(t, c, w, tests)
	})

	t.Run("routes before opening recovery prompt if err == workspace.OpenInOtherWorkspaceError", func(t *testing.T) {
		cfg := text.DefaultConfig()
		cfg.ScheduleNextTick = func(fn func()) bool { fn(); return true }
		router := new(testOpenRouter)
		cfg.OpenRouter = router
		c, loader := newTestComponentConfig(t, NopEditor(), cfg)
		busy, err := workspaceapi.ParseURI("file:///tmp/busy")
		require.NoError(t, err)
		router.h = browser.NopHandler(handler.Nop())
		router.handled = true
		loader.expectError = workspace.ErrOpenInOtherWorkspace

		h, err := c.Open(busy)
		require.NoError(t, err)
		assert.Equal(t, router.h, h)
		assert.Equal(t, 1, router.called)
		assert.True(t, router.uri.Equal(busy))
	})

	t.Run("sets a tab name", func(t *testing.T) {
		c, _, _, _ := newTestComponentWithFile(t, "file:///tmp/wasup")
		c.Resize(30, 20)

		tests := []comptest.TestCase{
			{nil, `
┌────────────────────────────┐
│o wasup                     │
├────────────────────────────┤
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
└────────────────────────────┘`,
			},
			{func() {
				uri, err := workspaceapi.ParseURI("file:///tmp/wasup")
				require.NoError(t, err)

				require.NoError(t, c.SetTabName(uri, "whatevs", term.Attributes{}))
			}, `
┌────────────────────────────┐
│o whatevs                   │
├────────────────────────────┤
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
└────────────────────────────┘`,
			},
		}

		w := term.NewStringWriter(30, 20)
		comptest.TestComponent(t, c, w, tests)

	})

	t.Run("loads markdown using markdown handler", func(t *testing.T) {
		markdown := `# Go-TUI
Do not edit this file. Instead edit its [corresponding wiki page](https://x.unstable.build/docs/repos/go-tui)
`
		cfg := text.DefaultConfig()
		cfg.ScheduleNextTick = func(fn func()) bool { fn(); return true }
		// give the focus tab icon attr an explicit fg so the
		// term.StringWriter's ForegroundCh substitution renders the
		// icon in the expected layout below.
		cfg.FocusTabIconAttr = term.Attributes{Fg: term.ColorWhite}
		c, tl := newTestComponentConfig(t, NopEditor(), cfg)
		c.Resize(30, 10)

		uri, err := workspaceapi.ParseURI("memory:///tmp/markdown.md")
		require.NoError(t, err)
		tl.openFile = workspace.NewMemoryFile("markdown.md", 1, 0,
			[]byte(markdown), new(sync.Mutex))

		h, err := c.OpenFileTab(uri, true /* read only */)
		require.NoError(t, err)
		require.NoError(t, c.Browser().Focus().SetContent(h))

		tests := []comptest.TestCase{
			{nil, `
┌$$$$$$$$$$$$$───────────────┐
│$ $$$$$$$$$$$               │
├────────────────────────────┤
│                            │
│#$$$$$$$$#                  │
│                            │
│Do not edit this file.      │
│Instead edit its            │
│$$$$$$$$$$$$$$$$$$$$$$$     │
└────────────────────────────┘`,
			},
		}

		w := term.NewStringWriter(30, 10)
		w.BackgroundCh = '#'
		w.ForegroundCh = '$'
		comptest.TestComponent(t, c, w, tests)

	})
}

func TestComponentEditorSubscriber(t *testing.T) {
	content := "Mr. Patoto"
	tsuite := []struct {
		name       string
		evType     textapi.EventType
		trigger    func(*testing.T, *text.Component, workspaceapi.URI)
		preTrigger func(*testing.T, *text.Component, workspaceapi.URI)
	}{
		{
			"Edit->EventTypeOpen",
			textapi.EventTypeOpen,
			func(t *testing.T, c *text.Component, resource workspaceapi.URI) {
				buf := cell.NewBuffer()
				buf.WriteString(content)
				_, err := c.Edit(context.Background(), resource, buf, false, false)
				assert.NoError(t, err)
			},
			nil,
		},
		{
			"OpenFileTab->EventTypeOpen",
			textapi.EventTypeOpen,
			func(t *testing.T, c *text.Component, resource workspaceapi.URI) {
				_, err := c.OpenFileTab(resource, false)
				assert.NoError(t, err)
			},
			nil,
		},
		{
			"Open->EventTypeOpen",
			textapi.EventTypeOpen,
			func(t *testing.T, c *text.Component, resource workspaceapi.URI) {
				_, err := c.Open(resource)
				assert.NoError(t, err)
			},
			nil,
		},
		{
			"Flush->EventTypeFlush",
			textapi.EventTypeFlush,
			func(t *testing.T, c *text.Component, resource workspaceapi.URI) {
				h, err := c.OpenFileTab(resource, false)
				require.NoError(t, err)

				win, err := c.Focus()
				require.NoError(t, err)

				require.NoError(t, win.SetContent(h))

				assert.NoError(t, awaitErr(c.Flush(context.Background(), win)))
			},
			nil,
		},
		{
			"Browser.RemoveWindowContent->EventTypeClose",
			textapi.EventTypeClose,
			func(t *testing.T, c *text.Component, resource workspaceapi.URI) {
				h, err := c.OpenFileTab(resource, false)
				require.NoError(t, err)

				win, err := c.Focus()
				require.NoError(t, err)

				require.NoError(t, win.SetContent(h))

				c.Browser().RemoveWindowContent(win)
			},
			nil,
		},
		{
			"buf.WriteString->EventTypeEdit",
			textapi.EventTypeEdit,
			func(t *testing.T, c *text.Component, resource workspaceapi.URI) {
				buf := cell.NewBuffer()
				_, err := c.Edit(context.Background(), resource, buf, false, false)
				assert.NoError(t, err)

				buf.WriteString("wasup")
			},
			nil,
		},
		{
			"buf.DeleteRow->EventTypeEdit",
			textapi.EventTypeEdit,
			func(t *testing.T, c *text.Component, resource workspaceapi.URI) {
				buf := cell.NewBuffer()
				buf.WriteString("wasup")
				_, err := c.Edit(context.Background(), resource, buf, false, false)
				assert.NoError(t, err)

				buf.DeleteRow(0)
			},
			nil,
		},
		{
			"Window.SetContent->EventTypeUnfocus",
			textapi.EventTypeUnfocus,
			func(t *testing.T, c *text.Component, resource workspaceapi.URI) {
				uri2, err := workspaceapi.ParseURI("file:///tmp/bleh")
				require.NoError(t, err)

				win, err := c.Focus()
				require.NoError(t, err)

				h, err := c.Open(uri2)
				require.NoError(t, err)
				require.NoError(t, win.SetContent(h))
			},
			func(t *testing.T, c *text.Component, resource workspaceapi.URI) {
				h, err := c.Open(resource)
				require.NoError(t, err)

				win, err := c.Focus()
				require.NoError(t, err)
				require.NoError(t, win.SetContent(h))
			},
		},
		{
			"Window.SetContent->EventTypeFocus",
			textapi.EventTypeFocus,
			func(t *testing.T, c *text.Component, resource workspaceapi.URI) {
				h, err := c.Open(resource)
				require.NoError(t, err)

				win, err := c.Focus()
				require.NoError(t, err)
				require.NoError(t, win.SetContent(h))
			},
			nil,
		},
		{
			"Split->EventTypeFocus",
			textapi.EventTypeFocus,
			func(t *testing.T, c *text.Component, resource workspaceapi.URI) {
				h, err := c.OpenFileTab(resource, false)
				require.NoError(t, err)

				focus, err := c.Focus()
				require.NoError(t, err)
				_, err = c.Split(browserapi.OrientationBottom, focus, h)
				require.NoError(t, err)
			},
			func(t *testing.T, c *text.Component, resource workspaceapi.URI) {
				uri2, err := workspaceapi.ParseURI("file:///tmp/blah")
				require.NoError(t, err)
				_, err = c.Open(uri2)
				require.NoError(t, err)
			},
		},
		{
			"SetContent->EventTypeFocus",
			textapi.EventTypeFocus,
			func(t *testing.T, c *text.Component, resource workspaceapi.URI) {
				// SubscribeEditor should trigger it
			},
			func(t *testing.T, c *text.Component, resource workspaceapi.URI) {
				h, err := c.Open(resource)
				require.NoError(t, err)

				win, err := c.Focus()
				require.NoError(t, err)

				require.NoError(t, win.SetContent(h))
			},
		},
		{
			"SubscribeOpen->EventTypeOpen",
			textapi.EventTypeOpen,
			func(t *testing.T, c *text.Component, resource workspaceapi.URI) {
				// SubscribeEditor should trigger it
			},
			func(t *testing.T, c *text.Component, resource workspaceapi.URI) {
				_, err := c.Open(resource)
				require.NoError(t, err)
			},
		},
		{
			"Handle>EventTypeCursor",
			textapi.EventTypeCursor,
			func(t *testing.T, c *text.Component, resource workspaceapi.URI) {
				buf := cell.NewBuffer()
				buf.WriteString(content)
				h, err := c.Edit(context.Background(), resource, buf, false, false)
				assert.NoError(t, err)
				h.Handle(term.Event{Ch: 'l'})
			},
			nil,
		},
		{
			"Handle>EventTypeSelection",
			textapi.EventTypeSelection,
			func(t *testing.T, c *text.Component, resource workspaceapi.URI) {
				buf := cell.NewBuffer()
				buf.WriteString(content)
				h, err := c.Edit(context.Background(), resource, buf, false, false)
				assert.NoError(t, err)
				h.Handle(term.Event{Ch: 'v'})
			},
			nil,
		},
		{
			"DispatchEvent>EventTypeRename",
			textapi.EventTypeRename,
			func(t *testing.T, c *text.Component, resource workspaceapi.URI) {
				assert.True(t, c.DispatchEvent(textapi.Event{
					Type: textapi.EventTypeRename,
					URI:  resource,
				}))
			},
			nil,
		},
		{
			"DispatchEvent>EventTypeRemove",
			textapi.EventTypeRemove,
			func(t *testing.T, c *text.Component, resource workspaceapi.URI) {
				assert.True(t, c.DispatchEvent(textapi.Event{
					Type: textapi.EventTypeRemove,
					URI:  resource,
				}))
			},
			nil,
		},
		{
			"DispatchEvent>EventTypeChange",
			textapi.EventTypeChange,
			func(t *testing.T, c *text.Component, resource workspaceapi.URI) {
				assert.True(t, c.DispatchEvent(textapi.Event{
					Type: textapi.EventTypeChange,
					URI:  resource,
				}))
			},
			nil,
		},
		{
			"DispatchEvent>EventTypeCreate",
			textapi.EventTypeCreate,
			func(t *testing.T, c *text.Component, resource workspaceapi.URI) {
				assert.True(t, c.DispatchEvent(textapi.Event{
					Type: textapi.EventTypeCreate,
					URI:  resource,
				}))
			},
			nil,
		},
	}

	for _, _tcase := range tsuite {
		tcase := _tcase
		t.Run(tcase.name+" SubscribeEditor subscribes an event handler", func(t *testing.T) {
			c, _ := newTestComponent(t, NopEditor())

			filename := "~/Joe_Biden.txt"
			uri, err := workspaceapi.CurrentUserHostURI(filename)
			require.NoError(t, err)
			if tcase.preTrigger != nil {
				tcase.preTrigger(t, c, uri)
			}

			var fired int
			usr, _ := user.Current()
			dir := usr.HomeDir
			evs := []textapi.EventType{tcase.evType}
			h := text.FuncEventHandler(func(ctx context.Context, ev textapi.Event) bool {
				// if preTrigger, then only assert relevant file event
				if tcase.preTrigger == nil {
					assert.True(t, strings.Contains(ev.URI.String(), "Joe_Biden.txt"))
					fired++
				} else if strings.Contains(ev.URI.String(), "Joe_Biden.txt") {
					fired++
				}
				// Edit skip Edit as it takes the resource name as is.
				if tcase.evType == textapi.EventTypeOpen && tcase.name != "Edit->EventTypeOpen" {
					assert.True(t, strings.Contains(ev.URI.String(), dir))
				}
				return false
			})
			c.SubscribeEvents(evs, h)

			tcase.trigger(t, c, uri)
			assert.Equal(t, 1, fired)
		})

		t.Run(tcase.name+" unsubscribes if handler returns exit=true", func(t *testing.T) {
			c, _ := newTestComponent(t, NopEditor())

			filename := "file:///Jill_Biden.txt"
			uri, err := workspaceapi.ParseURI(filename)
			require.NoError(t, err)
			if tcase.preTrigger != nil {
				tcase.preTrigger(t, c, uri)
			}

			var fired int
			evs := []textapi.EventType{tcase.evType}
			h := text.FuncEventHandler(func(ctx context.Context, ev textapi.Event) bool {
				if tcase.preTrigger == nil {
					assert.True(t, strings.Contains(ev.URI.String(), "Jill_Biden.txt"))
					fired++
					return true
				}
				if strings.Contains(ev.URI.String(), "Jill_Biden.txt") {
					fired++
					return true
				}
				return false
			})
			c.SubscribeEvents(evs, h)

			tcase.trigger(t, c, uri)
			assert.Equal(t, 1, fired)
		})
	}

	t.Run("no events are dispatched after Close is called", func(t *testing.T) {
		c, loader := newTestComponent(t, NopEditor())

		filename := "file:///Jill_Biden.txt"
		uri, err := workspaceapi.ParseURI(filename)
		require.NoError(t, err)
		fc := testFlusherCloser{closeFn: func() error {
			return nil
		}}

		loader.flusherCloser = &fc

		var fired int
		evs := []textapi.EventType{textapi.EventTypeClose}
		h := text.FuncEventHandler(func(ctx context.Context, ev textapi.Event) bool {
			fired++
			return false
		})
		c.SubscribeEvents(evs, h)

		_, err = c.Open(uri)
		require.NoError(t, err)

		assert.NoError(t, c.Close())
		assert.Equal(t, 0, fired)
	})

	t.Run("one open event is dispatched per open tab upon subscribe to open", func(t *testing.T) {
		c, loader := newTestComponent(t, NopEditor())
		uri1, err := workspaceapi.ParseURI("file:///Jill_Biden.txt")
		require.NoError(t, err)
		uri2, err := workspaceapi.ParseURI("file:///Joe_Biden.txt")
		require.NoError(t, err)

		content := "how bout that"
		loader.content = content

		_, err = c.Open(uri1)
		require.NoError(t, err)
		_, err = c.Open(uri2)
		require.NoError(t, err)

		var fired int
		ev := []textapi.EventType{textapi.EventTypeOpen}
		h := text.FuncEventHandler(func(ctx context.Context, ev textapi.Event) bool {
			fired++
			assert.Equal(t, content, ev.Content)
			return false
		})
		c.SubscribeEvents(ev, h)

		assert.Equal(t, 2, fired)
	})

	t.Run("EventTypeUnfocus is dispatched before EventTypeFocus on content update", func(t *testing.T) {
		c, _ := newTestComponent(t, NopEditor())
		uri1, err := workspaceapi.ParseURI("file:///Jill_Biden.txt")
		require.NoError(t, err)
		uri2, err := workspaceapi.ParseURI("file:///Joe_Biden.txt")
		require.NoError(t, err)

		a, err := c.Open(uri1)
		require.NoError(t, err)

		b, err := c.Open(uri2)
		require.NoError(t, err)

		win, err := c.Focus()
		require.NoError(t, err)

		require.NoError(t, win.SetContent(a))

		var i int
		evs := []textapi.EventType{textapi.EventTypeFocus, textapi.EventTypeUnfocus}
		h := text.FuncEventHandler(func(ctx context.Context, ev textapi.Event) bool {
			if i == 1 {
				assert.Equal(t, textapi.EventTypeUnfocus, ev.Type)
			} else {
				assert.Equal(t, textapi.EventTypeFocus, ev.Type)
			}
			i++
			return false
		})
		c.SubscribeEvents(evs, h)

		// upon SubscribeEvents, we dispatch first Focus
		assert.Equal(t, 1, i)
		require.NoError(t, win.SetContent(b))
		assert.Equal(t, 3, i)
	})
}

func TestEventTypeFocusIntegration(t *testing.T) {
	ctrl := gomock.NewController(t)
	c, _ := newTestComponent(t, &TestEditor{})
	c.Resize(100, 100)

	uri1, err := workspaceapi.ParseURI("file:///Bastardo.txt")
	require.NoError(t, err)

	uri2, err := workspaceapi.ParseURI("file:///Bigotudo.txt")
	require.NoError(t, err)

	h1, err := c.OpenFileTab(uri1, false)
	require.NoError(t, err)

	h2, err := c.OpenFileTab(uri2, false)
	require.NoError(t, err)

	w1, err := c.Focus()
	require.NoError(t, err)

	require.NoError(t, w1.SetContent(h2))

	mock := NewMockEventHandler(ctrl)
	evs := []textapi.EventType{textapi.EventTypeFocus, textapi.EventTypeUnfocus}

	const (
		frameWidth  = 2
		tabBarWidth = 2
	)

	expectedWidth := 100 - frameWidth
	expectedHeight := 100 - frameWidth - tabBarWidth

	t.Run("dispatch focus event upon subscribe", func(t *testing.T) {
		expectFocusEvent(t, mock, uri2, expectedWidth, expectedHeight)
		require.NoError(t, c.SubscribeEvents(evs, mock))
	})

	t.Run("dispatch focus/unfocus events on focus window content changes", func(t *testing.T) {
		expectFocusEvents(t, mock, uri1, uri2, expectedWidth, expectedHeight)
		require.NoError(t, w1.SetContent(h1))

		expectFocusEvents(t, mock, uri2, uri1, expectedWidth, expectedHeight)
		require.NoError(t, c.Browser().Focus().SetContent(h2))
	})
	var w2 browser.Window
	t.Run("dispatch focus/unfocus events upon creating a new window", func(t *testing.T) {
		expectFocusEvents(t, mock, uri1, uri2, 50-frameWidth, expectedHeight)
		w2, err = c.Split(browserapi.OrientationRight, w1, h1)
		require.NoError(t, err)
	})

	t.Run("dispatch focus/unfocus events on changing window in focus", func(t *testing.T) {
		expectFocusEvents(t, mock, uri2, uri1, 50-frameWidth, expectedHeight)
		_, err = c.SetFocus(w1)
		require.NoError(t, err)

		expectFocusEvents(t, mock, uri1, uri2, 50-frameWidth, expectedHeight)
		_, err = c.SetFocus(w2)
		require.NoError(t, err)
	})

	t.Run("do not dispatch focus/unfocus events on new tab", func(t *testing.T) {
		ok := c.Browser().NextTab(w2)
		require.False(t, ok)
	})

	t.Run("dispatch focus/unfocus events on window focus shift", func(t *testing.T) {
		expectFocusEvents(t, mock, uri2, uri1, 50-frameWidth, expectedHeight)
		ok := c.Browser().ShiftFocus()
		require.True(t, ok)

		expectFocusEvents(t, mock, uri1, uri2, 50-frameWidth, expectedHeight)
		ok = c.Browser().ShiftFocus()
		require.True(t, ok)
	})

	t.Run("dispatch focus/unfocus events on window in focus close", func(t *testing.T) {
		expectFocusEvents(t, mock, uri2, uri1, expectedWidth, expectedHeight)
		require.NoError(t, w2.Close())
	})

	t.Run("do not dispatch focus/unfocus events upon NextTab on window not in focus",
		func(t *testing.T) {
			ok := c.Browser().NextTab(w2)
			require.False(t, ok)
		})

	t.Run("dispatch focus/unfocus events upon NextTab on window in focus",
		func(t *testing.T) {
			expectFocusEvents(t, mock, uri1, uri2, expectedWidth, expectedHeight)
			ok := c.Browser().NextTab(w1)
			require.True(t, ok)
		})

	t.Run("dispatch focus event on resize", func(t *testing.T) {
		expectFocusEvent(t, mock, uri1, 8-frameWidth, 8-frameWidth-tabBarWidth)
		c.Resize(8, 8)
	})

	t.Run("dispatch focus event upon subscribe, non handler doesn't panic", func(t *testing.T) {
		win, err := c.Focus()
		require.NoError(t, err)
		mock.EXPECT().Handle(gomock.Any(), gomock.Any()).Return(false).Times(1)
		win.SetContent(browserapi.NopHandler(handler.Nop()))
		_, ok := c.Browser().NewTabFromContent('a', "bla", win)
		require.True(t, ok)
		require.NoError(t, c.SubscribeEvents(evs, mock))
	})
}

type fakeFallbackPrompter struct {
	calls   int
	command string
	args    []string
}

func (f *fakeFallbackPrompter) ShowFallbackPrompt(
	_ context.Context, command string, args ...string,
) {
	f.calls++
	f.command = command
	f.args = args
}

func TestDispatchCommand(t *testing.T) {
	uri, err := workspaceapi.ParseURI("file:///MacMecMic")
	require.NoError(t, err)

	t.Run("returns false if there's no registered handler", func(t *testing.T) {
		c, _ := newTestComponent(t, NopEditor())

		win, _ := c.Focus()
		cmd := textapi.Command{
			Resource: NewTestHandler(),
			URI:      uri,
			Name:     "SELL",
			Window:   win,
		}
		ok, err := dispatchWithAliases(context.Background(), c, cmd)
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("invokes fallback when no handler is registered", func(t *testing.T) {
		fb := &fakeFallbackPrompter{}
		config := text.DefaultConfig()
		config.ScheduleNextTick = func(fn func()) bool { fn(); return true }
		config.CommandFallbacks = map[string]text.FallbackPrompter{"agent": fb}
		c, _ := newTestComponentConfig(t, NopEditor(), config)

		win, _ := c.Focus()
		cmd := textapi.Command{
			Resource: NewTestHandler(),
			URI:      uri,
			Name:     "agent",
			Args:     []string{"hello"},
			Window:   win,
		}
		ok, err := c.DispatchCommand(context.Background(), cmd)
		require.NoError(t, err)
		assert.True(t, ok)
		assert.Equal(t, 1, fb.calls)
		assert.Equal(t, "agent", fb.command)
		assert.Equal(t, []string{"hello"}, fb.args)
	})

	t.Run("returns false with neither handler nor fallback", func(t *testing.T) {
		config := text.DefaultConfig()
		config.ScheduleNextTick = func(fn func()) bool { fn(); return true }
		config.CommandFallbacks = map[string]text.FallbackPrompter{
			"agent": &fakeFallbackPrompter{},
		}
		c, _ := newTestComponentConfig(t, NopEditor(), config)

		win, _ := c.Focus()
		cmd := textapi.Command{
			Resource: NewTestHandler(),
			URI:      uri,
			Name:     "SELL",
			Window:   win,
		}
		ok, err := c.DispatchCommand(context.Background(), cmd)
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("uses aliases from config to dispatch", func(t *testing.T) {
		config := text.DefaultConfig()
		config.ScheduleNextTick = func(fn func()) bool { fn(); return true }
		config.CommandAliases = map[string]text.CommandAlias{
			"workstation_layout": text.CommandAlias{
				Commands: []string{"newWindow", "edit /tmp/todo.md"},
			},
		}
		c, _ := newTestComponentConfig(t, NopEditor(), config)
		win, _ := c.Focus()

		var newWindowCalled, editCalled bool

		c.SubscribeCommand(testCommand("newWindow", "", ""),
			text.FuncCommandHandler(func(ctx context.Context, cmd textapi.Command) error {
				newWindowCalled = true
				assert.Equal(t, cmd.Name, "newWindow")
				assert.Equal(t, cmd.Args, []string{"newArgs"})
				return nil
			}, nil))

		c.SubscribeCommand(testCommand("edit", "", ""),
			text.FuncCommandHandler(func(ctx context.Context, cmd textapi.Command) error {
				editCalled = true
				assert.Equal(t, cmd.Name, "edit")
				assert.Equal(t, cmd.Args, []string{"/tmp/todo.md", "newArgs"})
				return nil
			}, nil))

		cmd := textapi.Command{
			Resource: NewTestHandler(),
			URI:      uri,
			Name:     "workstation_layout",
			Args:     []string{"newArgs"},
			Window:   win,
		}
		ok, err := dispatchWithAliases(context.Background(), c, cmd)
		assert.True(t, ok)
		require.NoError(t, err)
		assert.True(t, newWindowCalled)
		assert.True(t, editCalled)
	})

	t.Run("commands dispatched via alias have ctx set", func(t *testing.T) {
		config := text.DefaultConfig()
		config.ScheduleNextTick = func(fn func()) bool { fn(); return true }
		config.CommandAliases = map[string]text.CommandAlias{
			"workstation_layout": text.CommandAlias{
				Commands: []string{"newWindow"},
			},
		}
		c, _ := newTestComponentConfig(t, NopEditor(), config)
		win, _ := c.Focus()

		var newWindowCalled bool
		c.SubscribeCommand(testCommand("newWindow", "", ""),
			text.FuncCommandHandler(func(ctx context.Context, cmd textapi.Command) error {
				orig, ok := idecmd.IsContext(ctx)
				assert.True(t, ok)
				assert.Equal(t, orig, "workstation_layout")
				newWindowCalled = true
				return nil
			}, nil))

		cmd := textapi.Command{
			Resource: NewTestHandler(),
			URI:      uri,
			Name:     "workstation_layout",
			Args:     []string{"newArgs"},
			Window:   win,
		}
		ok, err := dispatchWithAliases(context.Background(), c, cmd)
		assert.True(t, ok)
		require.NoError(t, err)
		assert.True(t, newWindowCalled)
	})

	t.Run("passes echo-syntax alias body verbatim", func(t *testing.T) {
		config := text.DefaultConfig()
		config.ScheduleNextTick = func(fn func()) bool { fn(); return true }
		config.CommandAliases = map[string]text.CommandAlias{
			"searchfunc": {
				Commands: []string{
					`echo {prompt}searchast<space>locals.scm<enter>`,
				},
			},
		}
		c, _ := newTestComponentConfig(t, NopEditor(), config)
		win, _ := c.Focus()

		var gotArgs []string
		var called bool
		c.SubscribeCommand(testCommand("echo", "", ""),
			text.FuncCommandHandler(func(ctx context.Context, cmd textapi.Command) error {
				called = true
				gotArgs = cmd.Args
				return nil
			}, nil))

		cmd := textapi.Command{
			Resource: NewTestHandler(),
			URI:      uri,
			Name:     "searchfunc",
			Window:   win,
		}
		ok, err := dispatchWithAliases(context.Background(), c, cmd)
		assert.True(t, ok)
		require.NoError(t, err)
		require.True(t, called)
		assert.Equal(t,
			[]string{`{prompt}searchast<space>locals.scm<enter>`},
			gotArgs)
	})

	t.Run("replaces aliases positional commands with dispatched cmds", func(t *testing.T) {
		config := text.DefaultConfig()
		config.ScheduleNextTick = func(fn func()) bool { fn(); return true }
		config.CommandAliases = map[string]text.CommandAlias{
			"yeti": {
				Commands: []string{"newWindow wasup '$2' $name $$1", "edit $1 hellagood"},
			},
		}
		c, _ := newTestComponentConfig(t, NopEditor(), config)
		win, _ := c.Focus()

		var newWindowCalled, editCalled int

		// $name is not in the recognised set (positional digits,
		// $FILE, $WORKSPACE); shell.Expand falls
		// back to os.Getenv("name"). Pin it to the empty string so
		// the expected argv is deterministic regardless of host env.
		t.Setenv("name", "")
		// make sure that substitution doesn't replace original alias
		// so we can replace it dynamically every time
		const n = 100
		for range n {
			c.SubscribeCommand(testCommand("newWindow", "", ""),
				text.FuncCommandHandler(func(ctx context.Context, cmd textapi.Command) error {
					newWindowCalled++
					assert.Equal(t, "newWindow", cmd.Name)
					assert.Equal(t, []string{"wasup", "arg2", "", "$1"}, cmd.Args)
					return nil
				}, nil))

			c.SubscribeCommand(testCommand("edit", "", ""),
				text.FuncCommandHandler(func(ctx context.Context, cmd textapi.Command) error {
					editCalled++
					assert.Equal(t, "edit", cmd.Name)
					assert.Equal(t, []string{"arg1", "hellagood"}, cmd.Args)
					return nil
				}, nil))

			cmd := textapi.Command{
				Resource: NewTestHandler(),
				URI:      uri,
				Name:     "yeti",
				Args:     []string{"arg1", "arg2"},
				Window:   win,
			}
			ok, err := dispatchWithAliases(context.Background(), c, cmd)
			assert.True(t, ok)
			require.NoError(t, err)
		}

		assert.Equal(t, n, newWindowCalled)
		assert.Equal(t, n, editCalled)
	})

	t.Run("replaces aliases multiple positional commands with dispatched cmds", func(t *testing.T) {
		config := text.DefaultConfig()
		config.ScheduleNextTick = func(fn func()) bool { fn(); return true }
		config.CommandAliases = map[string]text.CommandAlias{
			"yeti": {
				Commands: []string{"newWindow wasup '$2' $name $1", "edit $1 hellagood"},
			},
		}
		c, _ := newTestComponentConfig(t, NopEditor(), config)
		win, _ := c.Focus()
		t.Setenv("name", "")

		var newWindowCalled, editCalled int

		c.SubscribeCommand(testCommand("newWindow", "", ""),
			text.FuncCommandHandler(func(ctx context.Context, cmd textapi.Command) error {
				newWindowCalled++
				assert.Equal(t, "newWindow", cmd.Name)
				assert.Equal(t, []string{"wasup", "arg2", "", "arg1"}, cmd.Args)
				return nil
			}, nil))

		c.SubscribeCommand(testCommand("edit", "", ""),
			text.FuncCommandHandler(func(ctx context.Context, cmd textapi.Command) error {
				editCalled++
				assert.Equal(t, "edit", cmd.Name)
				assert.Equal(t, []string{"arg1", "hellagood"}, cmd.Args)
				return nil
			}, nil))

		cmd := textapi.Command{
			Resource: NewTestHandler(),
			URI:      uri,
			Name:     "yeti",
			Args:     []string{"arg1", "arg2"},
			Window:   win,
		}
		ok, err := dispatchWithAliases(context.Background(), c, cmd)
		assert.True(t, ok)
		require.NoError(t, err)

		assert.Equal(t, 1, newWindowCalled)
		assert.Equal(t, 1, editCalled)
	})

	t.Run("returns error if alias expects positional arg and it's not passed", func(t *testing.T) {
		config := text.DefaultConfig()
		config.ScheduleNextTick = func(fn func()) bool { fn(); return true }
		config.CommandAliases = map[string]text.CommandAlias{
			"yeti": {
				Commands: []string{"newWindow wasup '$2' $name $$1", "edit $1 hellagood"},
			},
		}
		c, _ := newTestComponentConfig(t, NopEditor(), config)
		win, _ := c.Focus()
		t.Setenv("name", "")

		var newWindowCalled, editCalled int

		c.SubscribeCommand(testCommand("newWindow", "", ""),
			text.FuncCommandHandler(func(ctx context.Context, cmd textapi.Command) error {
				newWindowCalled++
				assert.Equal(t, "newWindow", cmd.Name)
				assert.Equal(t, []string{"wasup", "arg2", "", "$1"}, cmd.Args)
				return nil
			}, nil))

		c.SubscribeCommand(testCommand("edit", "", ""),
			text.FuncCommandHandler(func(ctx context.Context, cmd textapi.Command) error {
				editCalled++
				assert.Equal(t, "edit", cmd.Name)
				assert.Equal(t, []string{"arg1", "hellagood"}, cmd.Args)
				return nil
			}, nil))

		cmd := textapi.Command{
			Resource: NewTestHandler(),
			URI:      uri,
			Name:     "yeti",
			Args:     []string{"arg1"},
			Window:   win,
		}
		ok, err := dispatchWithAliases(context.Background(), c, cmd)
		assert.False(t, ok)
		require.EqualError(t, err, "alias expects an argument at position 2 ($2)")

		assert.Equal(t, 0, newWindowCalled)
		assert.Equal(t, 0, editCalled)
	})

	t.Run("replaces commands $FILE arg with current file", func(t *testing.T) {
		config := text.DefaultConfig()
		config.ScheduleNextTick = func(fn func()) bool { fn(); return true }
		c, _ := newTestComponentConfig(t, NopEditor(), config)
		win, _ := c.Focus()

		var editCalled int

		c.SubscribeCommand(testCommand("edit", "", ""),
			text.FuncCommandHandler(func(ctx context.Context, cmd textapi.Command) error {
				editCalled++
				assert.Equal(t, "edit", cmd.Name)
				assert.Equal(t, []string{"/a", "--all"}, cmd.Args)
				return nil
			}, nil))

		resource1, err := workspaceapi.ParseURI("file:///a")
		require.NoError(t, err)

		h, err := c.Open(resource1)
		require.NoError(t, err)

		c.Browser().Focus().SetContent(h)

		cmd := textapi.Command{
			Resource: NewTestHandler(),
			URI:      uri,
			Name:     "edit",
			Args:     []string{"$FILE", "--all"},
			Window:   win,
		}
		ok, err := dispatchWithAliases(context.Background(), c, cmd)
		assert.True(t, ok)
		require.NoError(t, err)
	})

	t.Run("expands file/cursor/workspace variables in alias body", func(t *testing.T) {
		// Wire a WORKSPACE_URI EnvSource so $FILE_REL can resolve;
		// covers the same delegation chain the workspace handler
		// uses in production (ide/workspace_handler.go envSource).
		config := text.DefaultConfig()
		config.ScheduleNextTick = func(fn func()) bool { fn(); return true }
		config.EnvSource = func(name string) (string, bool) {
			switch name {
			case "WORKSPACE_URI":
				return "file:///root", true
			case "WORKSPACE_PATH":
				return "/root", true
			}
			return "", false
		}
		config.CommandAliases = map[string]text.CommandAlias{
			"check": {Commands: []string{
				"sink $FILE $FILE_URI $FILE_DIR $FILE_BASENAME " +
					"$FILE_STEM $FILE_EXT $FILE_REL $LANG $LINE " +
					"$COLUMN $WORKSPACE_URI $WORKSPACE_PATH",
			}},
		}
		c, _ := newTestComponentConfig(t, NopEditor(), config)
		win, _ := c.Focus()

		var sinkCalled int
		c.SubscribeCommand(testCommand("sink", "", ""),
			text.FuncCommandHandler(func(ctx context.Context, cmd textapi.Command) error {
				sinkCalled++
				assert.Equal(t, []string{
					"/root/src/main.go",
					"file:///root/src/main.go",
					"/root/src",
					"main.go",
					"main",
					"go",
					"src/main.go",
					"go",
					// LINE/COLUMN are 1-based; on a freshly opened
					// tab they read as (1,1) — the cursor starts
					// at row 0, column 0 in the buffer.
					"1",
					"1",
					"file:///root",
					"/root",
				}, cmd.Args)
				return nil
			}, nil))

		resource, err := workspaceapi.ParseURI("file:///root/src/main.go")
		require.NoError(t, err)
		h, err := c.Open(resource)
		require.NoError(t, err)
		c.Browser().Focus().SetContent(h)

		cmd := textapi.Command{
			Resource: NewTestHandler(),
			URI:      uri,
			Name:     "check",
			Window:   win,
		}
		ok, err := dispatchWithAliases(context.Background(), c, cmd)
		assert.True(t, ok)
		require.NoError(t, err)
		assert.Equal(t, 1, sinkCalled)
	})

	t.Run("expands new variables in dispatched argv too", func(t *testing.T) {
		// The dispatched-arg expansion path is symmetric with the
		// alias-target path; both must see the same context and the
		// same names. Cover that explicitly so a future regression
		// can't silently shrink the dispatched-arg overlay.
		config := text.DefaultConfig()
		config.ScheduleNextTick = func(fn func()) bool { fn(); return true }
		config.EnvSource = func(name string) (string, bool) {
			if name == "WORKSPACE_URI" {
				return "file:///w", true
			}
			return "", false
		}
		c, _ := newTestComponentConfig(t, NopEditor(), config)
		win, _ := c.Focus()

		var gotArgs []string
		c.SubscribeCommand(testCommand("sink", "", ""),
			text.FuncCommandHandler(func(ctx context.Context, cmd textapi.Command) error {
				gotArgs = cmd.Args
				return nil
			}, nil))

		resource, err := workspaceapi.ParseURI("file:///w/pkg/foo.go")
		require.NoError(t, err)
		h, err := c.Open(resource)
		require.NoError(t, err)
		c.Browser().Focus().SetContent(h)

		cmd := textapi.Command{
			Resource: NewTestHandler(),
			URI:      uri,
			Name:     "sink",
			Args: []string{
				"$FILE_REL", "$FILE_STEM", "$LANG",
			},
			Window: win,
		}
		ok, err := dispatchWithAliases(context.Background(), c, cmd)
		assert.True(t, ok)
		require.NoError(t, err)
		assert.Equal(t, []string{"pkg/foo.go", "foo", "go"}, gotArgs)
	})

	t.Run("bubbles up HandleCommand errors", func(t *testing.T) {
		c, _ := newTestComponent(t, NopEditor())
		win, _ := c.Focus()
		c.SubscribeCommand(testCommand("bla", "", ""),
			text.FuncCommandHandler(func(ctx context.Context, cmd textapi.Command) error {
				return errors.New("boom")
			}, nil))

		cmd := textapi.Command{
			Resource: NewTestHandler(),
			URI:      uri,
			Name:     "bla",
			Window:   win,
		}
		ok, err := dispatchWithAliases(context.Background(), c, cmd)
		assert.True(t, ok)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "boom")
	})

	t.Run("handles bad aliases", func(t *testing.T) {
		config := text.DefaultConfig()
		config.ScheduleNextTick = func(fn func()) bool { fn(); return true }
		config.CommandAliases = map[string]text.CommandAlias{
			"bad1": text.CommandAlias{Commands: []string{""}},
			"bad2": text.CommandAlias{Commands: []string{}},
			"bad3": text.CommandAlias{},
		}
		c, _ := newTestComponentConfig(t, NopEditor(), config)
		win, _ := c.Focus()

		for _, cmd := range []string{"bad1", "bad2", "bad3"} {
			cmd := textapi.Command{
				Resource: NewTestHandler(),
				URI:      uri,
				Name:     cmd,
				Window:   win,
			}
			ok, err := dispatchWithAliases(context.Background(), c, cmd)
			assert.False(t, ok)
			require.NoError(t, err)
		}
	})

	t.Run("handles aliases missing from config", func(t *testing.T) {
		config := text.DefaultConfig()
		config.ScheduleNextTick = func(fn func()) bool { fn(); return true }
		config.CommandAliases = nil
		c, _ := newTestComponentConfig(t, NopEditor(), config)
		win, _ := c.Focus()

		cmd := textapi.Command{
			Resource: NewTestHandler(),
			URI:      uri,
			Name:     "kaboom",
			Window:   win,
		}
		ok, err := dispatchWithAliases(context.Background(), c, cmd)
		assert.False(t, ok)
		require.NoError(t, err)
	})

	t.Run("NewComponent returns error if aliases create an infinite loop of command calls", func(t *testing.T) {
		cfg := text.DefaultConfig()
		cfg.ScheduleNextTick = func(fn func()) bool { fn(); return true }
		cfg.CommandAliases = map[string]text.CommandAlias{
			"blah": text.CommandAlias{Commands: []string{"bleh"}},
			"bleh": text.CommandAlias{Commands: []string{"blah"}},
		}
		_, err := text.NewComponent(NopEditor(), &testLoader{}, cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cycle detected")
	})

	t.Run("NewComponent does not return error if aliases simply embeds another alias", func(t *testing.T) {
		cfg := text.DefaultConfig()
		cfg.ScheduleNextTick = func(fn func()) bool { fn(); return true }
		cfg.CommandAliases = map[string]text.CommandAlias{
			"blah": text.CommandAlias{Commands: []string{"bleh"}},
			"bleh": text.CommandAlias{Commands: []string{"bloh"}},
		}
		_, err := text.NewComponent(NopEditor(), &testLoader{}, cfg)
		require.NoError(t, err)
	})

	t.Run("NewComponent returns error if aliases create an infinite loop of nested command calls", func(t *testing.T) {
		cfg := text.DefaultConfig()
		cfg.ScheduleNextTick = func(fn func()) bool { fn(); return true }
		cfg.CommandAliases = map[string]text.CommandAlias{
			"blah": text.CommandAlias{Commands: []string{"bleh"}},
			"bleh": text.CommandAlias{Commands: []string{"bloh"}},
			"bloh": text.CommandAlias{Commands: []string{"bluh", "blah"}},
		}
		_, err := text.NewComponent(NopEditor(), &testLoader{}, cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cycle detected")
	})

	t.Run("NewComponent does not return error if aliases simply embeds another nested alias", func(t *testing.T) {
		cfg := text.DefaultConfig()
		cfg.ScheduleNextTick = func(fn func()) bool { fn(); return true }
		cfg.CommandAliases = map[string]text.CommandAlias{
			"blah": text.CommandAlias{Commands: []string{"bleh"}},
			"bleh": text.CommandAlias{Commands: []string{"bloh"}},
			"bloh": text.CommandAlias{Commands: []string{"bluh", "otherThing"}},
		}
		_, err := text.NewComponent(NopEditor(), &testLoader{}, cfg)
		require.NoError(t, err)
	})
}
func TestCompleteCommand(t *testing.T) {
	t.Run("uses alias completer, if defined", func(t *testing.T) {
		completer := func(c *text.Component) command.Completer {
			return command.FuncCompleter(func(ctx context.Context, args []string) (
				iterator.Iterator[string], string, error,
			) {
				return iterator.FromSlice[string]([]string{"a", "b", "c"}), "sus", nil
			})
		}
		cfg := text.DefaultConfig()
		cfg.ScheduleNextTick = func(fn func()) bool { fn(); return true }
		cfg.CommandAliases = map[string]text.CommandAlias{
			"blah": text.CommandAlias{
				Commands:   []string{"bleh"},
				Completers: []func(*text.Component) command.Completer{completer},
			},
		}
		c, err := text.NewComponent(NopEditor(), &testLoader{}, cfg)
		require.NoError(t, err)

		it, arg, err := c.CompleteCommand(context.Background(), textapi.Command{Name: "blah"})
		require.NoError(t, err)

		slice, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)

		assert.Equal(t, []string{"a", "b", "c"}, slice)
		assert.Equal(t, "sus", arg)
	})

	t.Run("bubbles up alias completer error", func(t *testing.T) {
		completer := func(c *text.Component) command.Completer {
			return command.FuncCompleter(func(ctx context.Context, args []string) (
				iterator.Iterator[string], string, error,
			) {
				return nil, "", errors.New("kaboom")
			})
		}
		cfg := text.DefaultConfig()
		cfg.ScheduleNextTick = func(fn func()) bool { fn(); return true }
		cfg.CommandAliases = map[string]text.CommandAlias{
			"blah": text.CommandAlias{
				Commands:   []string{"bleh"},
				Completers: []func(*text.Component) command.Completer{completer},
			},
		}
		c, err := text.NewComponent(NopEditor(), &testLoader{}, cfg)
		require.NoError(t, err)

		_, _, err = c.CompleteCommand(context.Background(), textapi.Command{Name: "blah"})
		require.EqualError(t, err, "kaboom")
	})

	t.Run("returns empty iterator if there's no registered handler", func(t *testing.T) {
		c, _ := newTestComponent(t, NopEditor())

		it, _, err := c.CompleteCommand(context.Background(), textapi.Command{Name: "blabla"})
		require.NoError(t, err)
		assertIteratorLen(t, 0, it)
	})

	t.Run("returns empty iterator if attempting to complete alias with no completer defined", func(t *testing.T) {
		cfg := text.DefaultConfig()
		cfg.ScheduleNextTick = func(fn func()) bool { fn(); return true }
		cfg.CommandAliases = map[string]text.CommandAlias{
			"workstation_layout": text.CommandAlias{
				Commands: []string{
					"newWindow",
					"edit /tmp/todo.md",
				},
			},
		}
		c, _ := newTestComponentConfig(t, NopEditor(), cfg)

		it, _, err := c.CompleteCommand(context.Background(), textapi.Command{Name: "workstation_layout"})
		require.NoError(t, err)
		assertIteratorLen(t, 0, it)
	})

	t.Run("calls command handler Complete", func(t *testing.T) {
		c, _ := newTestComponent(t, NopEditor())

		c.SubscribeCommand(testCommand("edit", "", ""),
			text.FuncCommandHandler(func(ctx context.Context, cmd textapi.Command) error {
				return nil
			}, func(ctx context.Context, cmd textapi.Command) (iterator.Iterator[string], string, error) {
				assert.Equal(t, []string{"letter", "number"}, cmd.Args)
				return iterator.FromSlice([]string{"one", "two"}), "2", nil
			}))

		it, newLastArg, err := c.CompleteCommand(context.Background(), textapi.Command{Name: "edit", Args: []string{"letter", "number"}})
		require.NoError(t, err)
		options := assertIteratorLen(t, 2, it)
		assert.Equal(t, []string{"one", "two"}, options)
		assert.Equal(t, "2", newLastArg)
	})

	t.Run("chains multiple alias completers in order", func(t *testing.T) {
		first := func(c *text.Component) command.Completer {
			return command.FuncCompleter(func(ctx context.Context, args []string) (
				iterator.Iterator[string], string, error,
			) {
				return iterator.FromSlice([]string{"alpha", "beta"}), "", nil
			})
		}
		second := func(c *text.Component) command.Completer {
			return command.FuncCompleter(func(ctx context.Context, args []string) (
				iterator.Iterator[string], string, error,
			) {
				return iterator.FromSlice([]string{"gamma", "delta"}), "", nil
			})
		}
		cfg := text.DefaultConfig()
		cfg.ScheduleNextTick = func(fn func()) bool { fn(); return true }
		cfg.CommandAliases = map[string]text.CommandAlias{
			"chain": text.CommandAlias{
				Commands: []string{"e"},
				Completers: []func(*text.Component) command.Completer{
					first, second,
				},
			},
		}
		c, err := text.NewComponent(NopEditor(), &testLoader{}, cfg)
		require.NoError(t, err)

		it, _, err := c.CompleteCommand(context.Background(), textapi.Command{Name: "chain"})
		require.NoError(t, err)

		got, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)
		assert.Equal(t, []string{"alpha", "beta", "gamma", "delta"}, got)
	})
}

func assertIteratorLen(t *testing.T, n int, it iterator.Iterator[string]) []string {
	var ret []string
	var i int
	for ; ; i++ {
		next, ok := it.Next(context.Background())
		if !ok {
			break
		}
		ret = append(ret, next)
	}
	assert.Equal(t, n, i)
	return ret
}

func TestComponentEditor(t *testing.T) {
	t.Run("returns tab with name as Handler", func(t *testing.T) {
		myName := "file:///tmp/Ennio_Morricone.go"
		c, _, h1, uri := newTestComponentWithFile(t, myName)

		h2, err := c.Editor(uri)
		assert.NoError(t, err)
		assert.Equal(t, h1.(*browser.Tab).Handler(), h2)
	})

	t.Run("returns editor returned in call to Edit", func(t *testing.T) {
		myName, err := workspaceapi.ParseURI("file:///tmp/Ennio_Morricone.go")
		require.NoError(t, err)
		c, _ := newTestComponent(t, NopEditor())

		h1, err := c.Edit(context.Background(), myName, cell.NewBuffer(), false, false)
		assert.NoError(t, err)

		actualH1, err := c.Editor(myName)
		assert.NoError(t, err)
		assert.Equal(t, h1, actualH1)
	})

	t.Run("returns error if Handler returned in call to Edit is closed", func(t *testing.T) {
		myName, err := workspaceapi.ParseURI("file:///tmp/Ennio_Morricone.go")
		require.NoError(t, err)
		c, _ := newTestComponent(t, NopEditor())

		h1, err := c.Edit(context.Background(), myName, cell.NewBuffer(), false, false)
		assert.NoError(t, err)

		require.NoError(t, h1.Close())

		actualH1, err := c.Editor(myName)
		assert.Error(t, err)
		assert.Nil(t, actualH1)
	})

	t.Run("returns error if no handler is found with name", func(t *testing.T) {
		c, _ := newTestComponent(t, NopEditor())

		h, err := c.Editor(workspaceapi.URI{})
		assert.Error(t, err)
		assert.Nil(t, h)
	})
}

func TestComponentCommands(t *testing.T) {
	t.Run("returns empty slice if no commands have been registered", func(t *testing.T) {
		c, _ := newTestComponent(t, NopEditor())
		assert.Len(t, c.Commands(), 0)
	})

	t.Run("returns registered commands", func(t *testing.T) {
		c, _ := newTestComponent(t, NopEditor())
		c.SubscribeCommand(testCommand("myCmd", "mySummary", "mySynopsis"),
			text.FuncCommandHandler(func(context.Context, textapi.Command) error {
				return nil
			}, nil))
		cmds := c.Commands()
		require.Len(t, cmds, 1)
		expectedMan := command.Manual{Name: "myCmd", Summary: "mySummary", Synopsis: "mySynopsis"}
		assert.Equal(t, expectedMan, cmds[0])
	})
	t.Run("returns configured aliases", func(t *testing.T) {
		cfg := text.DefaultConfig()
		cfg.ScheduleNextTick = func(fn func()) bool { fn(); return true }
		cfg.CommandAliases = map[string]text.CommandAlias{
			"blah": {Commands: []string{"myCmd"}},
		}
		c, err := text.NewComponent(NopEditor(), &testLoader{}, cfg)
		require.NoError(t, err)
		// text.Component no longer enumerates aliases in Commands();
		// alias listing is the responsibility of ide/idecmd.
		assert.Empty(t, c.Commands())
		mans := idecmd.NewExpander(c.CommandAliases(), nil).Aliases()
		require.Len(t, mans, 1)
		expectedMan := command.Manual{Name: "blah", AliasOf: []string{"myCmd"}}
		assert.Equal(t, expectedMan, mans[0])
	})
}

func testRegister(t *testing.T,
	constructor func(ed text.Editor, mu *sync.Mutex, resource workspaceapi.URI) (*text.Component, text.Editor, error)) {
	t.Run("calls subscribed command handler", func(t *testing.T) {
		var mu sync.Mutex
		resource1, err := workspaceapi.ParseURI("file:///HERS")
		require.NoError(t, err)
		myArgs := []string{"a", "bbbbbbbbbbbbbbbbbbbbb"}
		myCmd := "BUY"
		c, sut, err := constructor(NopEditor(), &mu, resource1)
		require.NoError(t, err)

		mu.Lock()
		win, _ := c.Focus()
		h1, err := c.Edit(context.Background(), resource1, cell.NewBuffer(), false, false)
		mu.Unlock()
		require.NoError(t, err)

		var called int
		var wg sync.WaitGroup
		sut.SubscribeCommand(testCommand(myCmd, "", ""),
			text.FuncCommandHandler(func(ctx context.Context, cmd textapi.Command) error {
				defer wg.Done()
				assert.Equal(t, myCmd, cmd.Name)
				assert.Equal(t, myArgs, cmd.Args)
				called++
				return nil
			}, nil))

		wg.Add(1)
		mu.Lock()
		cmd := textapi.Command{Resource: h1, URI: resource1, Name: myCmd, Args: myArgs,
			Window: win}
		ok, err := dispatchWithAliases(context.Background(), c, cmd)
		assert.True(t, ok)
		require.NoError(t, err)
		mu.Unlock()

		wg.Wait()
		assert.Equal(t, 1, called)
	})
}

func TestComponentRegister(t *testing.T) {
	testRegister(t, func(ed text.Editor, mu *sync.Mutex, res workspaceapi.URI) (*text.Component, text.Editor, error) {
		c, _, err := newTestComponentErr(ed, text.DefaultConfig())
		return c, c, err
	})
}

func TestComponentRegisterREPLCommand(t *testing.T) {
	cfgc := text.DefaultConfig()
	cfgc.ScheduleNextTick = func(fn func()) bool { fn(); return true }
	c, err := text.NewComponent(NopEditor(), &testLoader{}, cfgc)
	require.NoError(t, err)

	h := &testREPLHandler{}
	man := textapi.CommandManual{Name: "status", Summary: "show status"}

	require.NoError(t, c.RegisterREPLCommand(man, h))

	actual, ok := c.REPLCommand("status")
	require.True(t, ok)
	assert.Same(t, h, actual)
	assert.Equal(t, []textapi.CommandManual{man}, c.REPLCommands())

	err = c.RegisterREPLCommand(man, h)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already registered")
}

type testREPLHandler struct{}

func (*testREPLHandler) HandleCommand(
	context.Context, repl.Command, repl.ProgressWriter,
) (sdkiterator.Iterator[component.Responsive], error) {
	return sdkiterator.Empty[component.Responsive](), nil
}

func (*testREPLHandler) Complete(
	context.Context, string, []string,
) (sdkiterator.Iterator[string], error) {
	return sdkiterator.Empty[string](), nil
}

func (*testREPLHandler) Help(
	context.Context, []string,
) (sdkiterator.Iterator[component.Responsive], error) {
	return sdkiterator.Empty[component.Responsive](), nil
}
func TestUnregisterCommand(t *testing.T) {
	resource1, err := workspaceapi.ParseURI("file:///HERS")
	require.NoError(t, err)
	myArgs := []string{"a", "bbbbbbbbbbbbbbbbbbbbb"}
	myCmd := "BUY"
	cfgc := text.DefaultConfig()
	cfgc.ScheduleNextTick = func(fn func()) bool { fn(); return true }
	c, err := text.NewComponent(NopEditor(), &testLoader{}, cfgc)
	require.NoError(t, err)

	win, _ := c.Focus()
	h1, err := c.Edit(context.Background(), resource1, cell.NewBuffer(), false, false)
	require.NoError(t, err)

	var called int
	c.SubscribeCommand(testCommand(myCmd, "", ""),
		text.FuncCommandHandler(func(ctx context.Context, cmd textapi.Command) error {
			called++
			return nil
		}, nil))

	cmd := textapi.Command{
		Resource: h1,
		URI:      resource1,
		Name:     myCmd,
		Args:     myArgs,
		Window:   win,
	}
	ok, err := dispatchWithAliases(context.Background(), c, cmd)
	assert.True(t, ok)
	require.NoError(t, err)

	assert.Equal(t, 1, called)

	err = c.UnsubscribeCommand(myCmd)
	require.NoError(t, err)

	ok, err = dispatchWithAliases(context.Background(), c, cmd)
	assert.False(t, ok)
	require.NoError(t, err)
	assert.Equal(t, 1, called)
}

func TestTabIntegration(t *testing.T) {
	testTabIntegration(t, func(ed text.Editor, mu *sync.Mutex) (*text.Component, browser.WindowManager, error) {
		c, _, err := newTestComponentErr(ed, text.DefaultConfig())
		return c, c, err
	})
}

func testTabIntegration(t *testing.T,
	constructor func(ed text.Editor, mu *sync.Mutex) (*text.Component, browser.WindowManager, error)) {
	t.Run("switches to a tab upon call to SetContent", func(t *testing.T) {
		cases := []handlertest.SequenceTestCase{
			{"",
				`┌──────━━━━────────┐
│x $$  x ##        │
├──────────────────┤
│##################│
│##################│
│##################│
│##################│
│##################│
│##################│
└──────────────────┘`},
		}

		fn := func(t *testing.T) tui.Handler {
			var mu sync.Mutex
			c, wm, err := constructor(NopEditor(), &mu)
			require.NoError(t, err)

			resource1, err := workspaceapi.ParseURI("file:///a")
			require.NoError(t, err)
			resource2, err := workspaceapi.ParseURI("file:///b")
			require.NoError(t, err)
			b1 := browsertest.NewTestHandler()
			b1.TestHandler.Ch = '$'
			_, err = wm.Tab(resource1, 'x', "$$", b1)
			require.NoError(t, err)

			b2 := browsertest.NewTestHandler()
			b2.TestHandler.Ch = '#'
			t2, err := wm.Tab(resource2, 'x', "##", b2)
			require.NoError(t, err)

			win, err := wm.Focus()
			require.NoError(t, err)

			require.NoError(t, win.SetContent(t2))
			return handler.Sync(&mu, handler.NopFromComponent(c))
		}
		handlertest.TestHandlerIsolated(t, fn, 20, 10, cases)
	})
}

func TestFlush(t *testing.T) {
	resource1, err := workspaceapi.ParseURI("file:///a")
	require.NoError(t, err)

	t.Run("rejects saving markdown view", func(t *testing.T) {
		c, loader := newTestComponent(t, NopEditor())
		uri, err := workspaceapi.ParseURI("memory:///tmp/markdown.md")
		require.NoError(t, err)
		loader.openFile = workspace.NewMemoryFile(
			"markdown.md", 1, 0, []byte("# Markdown\n"), new(sync.Mutex),
		)
		h, err := c.OpenFileTab(uri, true)
		require.NoError(t, err)
		win, err := c.Focus()
		require.NoError(t, err)
		require.NoError(t, win.SetContent(h))

		operations := []struct {
			name string
			run  func() (<-chan error, error)
		}{
			{name: "flush", run: func() (<-chan error, error) {
				return c.Flush(context.Background(), win)
			}},
			{name: "force flush", run: func() (<-chan error, error) {
				return c.ForceFlush(context.Background(), win)
			}},
		}

		for _, operation := range operations {
			t.Run(operation.name, func(t *testing.T) {
				assert.ErrorIs(t, awaitErr(operation.run()), textapi.ErrInvalidSave)
			})
		}
	})

	t.Run("calls underlying closer Flush", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mock := NewMockHandler(ctrl)
		mockEditor := NewMockEditor(ctrl)
		mockWorkspace := NewMockWorkspace(ctrl)
		mockFlusherCloser := workspacetest.NewMockFlusherCloser(ctrl)

		cfgc := text.DefaultConfig()

		cfgc.ScheduleNextTick = func(fn func()) bool { fn(); return true }

		c, err := text.NewComponent(mockEditor, mockWorkspace, cfgc)
		require.NoError(t, err)

		win, err := c.Focus()
		require.NoError(t, err)

		mockWorkspace.EXPECT().Load(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			Return(mockFlusherCloser, nil).Times(1)
		mock.EXPECT().Resize(gomock.Any(), gomock.Any()).Times(1)
		mock.EXPECT().CursorAtScroll().
			Return(term.Coordinates{}).Times(1)
		mock.EXPECT().CellView().
			Return(cell.NewBuffer().View()).Times(1)
		mockEditor.EXPECT().Edit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Eq(true), gomock.Any()).Return(mock, nil)
		mockEditor.EXPECT().IsExternal().Return(false).AnyTimes()

		h, err := c.OpenFileTab(resource1, true)
		require.NoError(t, win.SetContent(h))

		doneCh := make(chan error, 1)
		doneCh <- nil
		close(doneCh)
		var doneRecvCh <-chan error = doneCh
		mockFlusherCloser.EXPECT().Flush(gomock.Any()).
			Return(doneRecvCh, nil).Times(1)
		require.NoError(t, awaitErr(c.Flush(context.Background(), win)))
	})

	t.Run("returns ErrInvalidSave if called on tab with nil closer handle", func(t *testing.T) {
		mock := browsertest.NewTestHandler()
		c, _ := newTestComponent(t, nil)
		win, err := c.Focus()
		require.NoError(t, err)

		h, err := c.Tab(resource1, 'x', "Rupi Kaur", mock)
		require.NoError(t, win.SetContent(h))

		require.Equal(t, textapi.ErrInvalidSave, awaitErr(c.Flush(context.Background(), win)))
	})

	t.Run("returns ErrInvalidSave if called on non-tab", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mock := NewMockHandler(ctrl)
		c, _ := newTestComponent(t, nil)
		win, err := c.Focus()
		require.NoError(t, err)

		mock.EXPECT().Resize(gomock.Any(), gomock.Any()).AnyTimes()
		_, err = c.Split(browserapi.OrientationTop, win, mock)
		require.NoError(t, err)

		require.Equal(t, textapi.ErrInvalidSave, awaitErr(c.Flush(context.Background(), win)))
	})
}

func TestReload(t *testing.T) {
	resource1, err := workspaceapi.ParseURI("file:///a")
	require.NoError(t, err)

	t.Run("reloads markdown view", func(t *testing.T) {
		c, loader := newTestComponent(t, NopEditor())

		uri, err := workspaceapi.ParseURI("memory:///tmp/markdown.md")
		require.NoError(t, err)
		loader.openFile = workspace.NewMemoryFile(
			"markdown.md", 1, 0, []byte("# Before\n"), new(sync.Mutex),
		)

		h, err := c.OpenFileTab(uri, true)
		require.NoError(t, err)
		win, err := c.Focus()
		require.NoError(t, err)
		require.NoError(t, win.SetContent(h))
		c.Resize(40, 8)

		loader.openFile = workspace.NewMemoryFile(
			"markdown.md", 2, 0, []byte("# After\n"), new(sync.Mutex),
		)
		require.NoError(t, awaitErr(c.Reload(context.Background(), win)))

		writer := term.NewStringWriter(40, 8)
		c.Draw(writer)
		require.NoError(t, writer.Flush())
		assert.Contains(t, writer.String(), "After")
		assert.NotContains(t, writer.String(), "Before")
	})

	t.Run("records markdown modification time before reading", func(t *testing.T) {
		c, loader := newTestComponent(t, NopEditor())
		uri, err := workspaceapi.ParseURI("memory:///tmp/markdown.md")
		require.NoError(t, err)

		beforeRead := time.Unix(1, 0)
		loader.openFile = &markdownReloadFile{
			File: workspace.NewMemoryFile(
				"markdown.md", 1, 0, []byte("# Markdown\n"), new(sync.Mutex),
			),
			beforeRead: beforeRead,
			afterRead:  time.Unix(2, 0),
		}

		h, err := c.OpenFileTab(uri, true)
		require.NoError(t, err)
		lastFlush, err := c.LastFlush(h)
		require.NoError(t, err)
		assert.Equal(t, beforeRead, lastFlush)
	})

	t.Run("preserves markdown view state", func(t *testing.T) {
		initialContent := "# Before\n\nneedle\n\n" + strings.Repeat("paragraph\n\n", 12)
		tests := []struct {
			name           string
			content        string
			wantSameOffset bool
			wantNextSearch bool
			wantPrevSearch bool
		}{
			{
				name:           "file got bigger",
				content:        initialContent + "needle\n\n" + strings.Repeat("more\n\n", 12),
				wantSameOffset: true,
				wantNextSearch: true,
				wantPrevSearch: true,
			},
			{
				name:           "file got smaller",
				content:        "# Short\n",
				wantPrevSearch: false,
			},
			{
				name:           "search query no longer matches",
				content:        "# After\n\n" + strings.Repeat("paragraph\n\n", 12),
				wantSameOffset: true,
				wantPrevSearch: false,
			},
		}

		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				c, loader := newTestComponent(t, NopEditor())
				uri, err := workspaceapi.ParseURI("memory:///tmp/markdown.md")
				require.NoError(t, err)
				loader.openFile = workspace.NewMemoryFile(
					"markdown.md", 1, 0, []byte(initialContent), new(sync.Mutex),
				)

				h, err := c.OpenFileTab(uri, true)
				require.NoError(t, err)
				tab, ok := h.(*browser.Tab)
				require.True(t, ok)
				markdownHandler, ok := tab.Handler().(*hmarkdown.Handler)
				require.True(t, ok)
				win, err := c.Focus()
				require.NoError(t, err)
				require.NoError(t, win.SetContent(h))
				c.Resize(20, 5)
				markdownHandler.Search("needle")
				markdownHandler.ScrollDown(8)
				initialOffset := markdownHandler.SeekOffset()
				require.Positive(t, initialOffset)

				loader.openFile = workspace.NewMemoryFile(
					"markdown.md", 2, 0, []byte(test.content), new(sync.Mutex),
				)
				require.NoError(t, awaitErr(c.Reload(context.Background(), win)))

				if test.wantSameOffset {
					assert.Equal(t, initialOffset, markdownHandler.SeekOffset())
				} else {
					assert.Equal(t, markdownHandler.MaxSeekOffset(), markdownHandler.SeekOffset())
					assert.Less(t, markdownHandler.SeekOffset(), initialOffset)
				}
				assert.Equal(t, test.wantNextSearch, markdownHandler.SeekToNextSearchResult())
				assert.Equal(t, test.wantPrevSearch, markdownHandler.SeekToPrevSearchResult())
			})
		}
	})

	t.Run("calls underlying closer Flush", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mock := NewMockHandler(ctrl)
		mockEditor := NewMockEditor(ctrl)
		mockWorkspace := NewMockWorkspace(ctrl)
		mockFlusherCloser := workspacetest.NewMockFlusherCloser(ctrl)

		cfgc := text.DefaultConfig()

		cfgc.ScheduleNextTick = func(fn func()) bool { fn(); return true }

		c, err := text.NewComponent(mockEditor, mockWorkspace, cfgc)
		require.NoError(t, err)

		win, err := c.Focus()
		require.NoError(t, err)

		mockWorkspace.EXPECT().Load(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			Return(mockFlusherCloser, nil).Times(1)
		mock.EXPECT().Resize(gomock.Any(), gomock.Any()).Times(1)
		mock.EXPECT().CursorAtScroll().
			Return(term.Coordinates{}).Times(1)
		mockEditor.EXPECT().Edit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(mock, nil)
		mockEditor.EXPECT().IsExternal().Return(false).AnyTimes()

		h, err := c.OpenFileTab(resource1, true)
		require.NoError(t, win.SetContent(h))

		mock.EXPECT().CellView().
			Return(cell.NewBuffer().View()).Times(1)

		doneCh := make(chan error, 1)
		doneCh <- nil
		close(doneCh)
		var doneRecvCh <-chan error = doneCh
		mockFlusherCloser.EXPECT().Reload(gomock.Any()).
			Return(doneRecvCh, nil).Times(1)
		require.NoError(t, awaitErr(c.Reload(context.Background(), win)))
	})

	t.Run("returns ErrInvalidSave if called on tab with nil closer handle", func(t *testing.T) {
		mock := browsertest.NewTestHandler()
		c, _ := newTestComponent(t, nil)
		win, err := c.Focus()
		require.NoError(t, err)

		h, err := c.Tab(resource1, 'x', "Rupi Kaur", mock)
		require.NoError(t, win.SetContent(h))

		require.Equal(t, textapi.ErrInvalidReload, awaitErr(c.Reload(context.Background(), win)))
	})

	t.Run("returns ErrInvalidReload if called on non-tab", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mock := NewMockHandler(ctrl)
		c, _ := newTestComponent(t, nil)
		win, err := c.Focus()
		require.NoError(t, err)

		mock.EXPECT().Resize(gomock.Any(), gomock.Any()).AnyTimes()
		_, err = c.Split(browserapi.OrientationTop, win, mock)
		require.NoError(t, err)

		require.Equal(t, textapi.ErrInvalidReload, awaitErr(c.Reload(context.Background(), win)))
	})

	t.Run("bubbles up file reload errors", func(t *testing.T) {
		c, testLoader := newTestComponent(t, NopEditor())
		win, err := c.Focus()
		require.NoError(t, err)

		testLoader.flusherCloser = &testFlusherCloser{
			reloadFn: func() error { return errors.New("boom") },
		}

		h, err := c.OpenFileTab(resource1, true)
		require.NoError(t, err)
		require.NoError(t, win.SetContent(h))

		err = awaitErr(c.Reload(context.Background(), win))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "boom")
	})

	t.Run("integration with event dispatching", func(t *testing.T) {
		ctx := context.Background()
		c, testLoader := newTestComponent(t, NopEditor())
		win, err := c.Focus()
		require.NoError(t, err)

		const content = "abc\ndef\n"
		testLoader.content = content

		h, err := c.OpenFileTab(resource1, true)
		require.NoError(t, err)
		require.NoError(t, win.SetContent(h))

		cfg := text.DefaultConfig()

		cfg.ScheduleNextTick = func(fn func()) bool { fn(); return true }
		tracker := extutil.NewResourceTracker(cfg.Tabspaces, false)
		err = c.SubscribeEvents(textapi.AllEvents(), tracker)
		require.NoError(t, err)

		ed, err := c.Editor(resource1)
		require.NoError(t, err)

		ced := ed.CellEditor()
		_, _, _ = ced.Edit(ctx, term.Coordinates{}, term.Coordinates{}, "ABC")

		assertContent := func(t *testing.T, expected string) {
			t.Helper()
			cview := ed.CellView()
			cells := cview.RawCells()
			require.NoError(t, err)
			assert.Equal(t, expected, term.CellsToString(cells))

			res, ok := tracker.Resource(resource1)
			require.True(t, ok)
			assert.Equal(t, expected, res.Buffer().String())
		}

		assertContent(t, "ABC"+content)

		require.NoError(t, awaitErr(c.Reload(context.Background(), win)))

		assertContent(t, content)
	})

	t.Run("integration with IsDirty", func(t *testing.T) {
		ctx := context.Background()
		c, testLoader := newTestComponent(t, NopEditor())
		win, err := c.Focus()
		require.NoError(t, err)

		const content = "abc\ndef\n"
		testLoader.content = content

		h, err := c.OpenFileTab(resource1, true)
		require.NoError(t, err)
		require.NoError(t, win.SetContent(h))

		ed, err := c.Editor(resource1)
		require.NoError(t, err)

		ced := ed.CellEditor()
		_, _, _ = ced.Edit(ctx, term.Coordinates{}, term.Coordinates{}, "ABC")

		isDirty, ok := c.IsDirty(resource1)
		require.True(t, ok)
		assert.True(t, isDirty)

		require.NoError(t, awaitErr(c.Reload(context.Background(), win)))

		isDirty, ok = c.IsDirty(resource1)
		require.True(t, ok)
		assert.False(t, isDirty)
	})

	t.Run("clean buffer stays clean during reload-driven OnDidEdit", func(t *testing.T) {
		// Guards the editorFlusherCloser.reloading short-circuit.
		// Before the fix, every cell edit fired by a reload raised
		// the dirty tab attribute via OnDidEdit because efc.lastFlush
		// only advances on the host-scheduled dispatchFlush tick.
		// A clean reload (e.g. an external `git rebase` rewriting an
		// already-saved file) must never flip the tab to dirty —
		// either during reload-driven edits or after the scheduled
		// dispatchFlush has run.
		cfg := text.DefaultConfig()
		var pending []func()
		cfg.ScheduleNextTick = func(fn func()) bool {
			pending = append(pending, fn)
			return true
		}
		c, testLoader := newTestComponentConfig(t, NopEditor(), cfg)
		win, err := c.Focus()
		require.NoError(t, err)

		const content = "abc\ndef\n"
		testLoader.content = content

		h, err := c.OpenFileTab(resource1, true)
		require.NoError(t, err)
		require.NoError(t, win.SetContent(h))

		dirty, ok := c.IsDirty(resource1)
		require.True(t, ok)
		require.False(t, dirty, "buffer must start clean")

		require.NoError(t, awaitErr(c.Reload(context.Background(), win)))

		// Mid-reload: dispatchFlush is queued but not yet drained.
		// OnDidEdit fired for each cell edit produced by Replace,
		// but the reloading flag must have suppressed
		// setDirtyFileAttr — so the tab is still clean.
		dirty, ok = c.IsDirty(resource1)
		require.True(t, ok)
		assert.False(t, dirty,
			"reload-driven OnDidEdit must not flip the tab to dirty")

		// Drain the scheduler: dispatchFlush advances efc.lastFlush
		// to the post-reload buffer version, and the reloading flag
		// is cleared. The tab remains clean.
		for _, fn := range pending {
			fn()
		}
		pending = nil

		dirty, ok = c.IsDirty(resource1)
		require.True(t, ok)
		assert.False(t, dirty)
	})
}

// TestDirtyStateAfterFlush covers what an observer sees once a save settles.
// The filesystem watcher asks whether a file has unflushed changes at exactly
// that moment to decide between reloading it and prompting about a conflict,
// so a tab that claims to be clean while the buffer differs from disk sends it
// down the wrong branch and silently discards the user's work.
func TestDirtyStateAfterFlush(t *testing.T) {
	resource1, err := workspaceapi.ParseURI("file:///a")
	require.NoError(t, err)

	t.Run("a failed save leaves the tab dirty", func(t *testing.T) {
		ctx := context.Background()
		c, testLoader := newTestComponent(t, NopEditor())
		win, err := c.Focus()
		require.NoError(t, err)
		var flushErr error
		testLoader.flusherCloser = &testFlusherCloser{
			flushFn: func() error { return flushErr },
		}

		h, err := c.OpenFileTab(resource1, true)
		require.NoError(t, err)
		require.NoError(t, win.SetContent(h))

		ed, err := c.Editor(resource1)
		require.NoError(t, err)
		_, _, _ = ed.CellEditor().Edit(ctx, term.Coordinates{}, term.Coordinates{}, "ABC")
		dirty, ok := c.IsDirty(resource1)
		require.True(t, ok)
		require.True(t, dirty)

		flushErr = workspaceapi.ErrStaleData
		require.ErrorIs(t, awaitErr(c.Flush(ctx, win)), workspaceapi.ErrStaleData)

		dirty, ok = c.IsDirty(resource1)
		require.True(t, ok)
		assert.True(t, dirty, "a save that failed must not report the buffer as saved")
	})

	t.Run("an edit during a save leaves the tab dirty", func(t *testing.T) {
		ctx := context.Background()
		c, testLoader := newTestComponent(t, NopEditor())
		win, err := c.Focus()
		require.NoError(t, err)
		var editDuringSave func()
		testLoader.flusherCloser = &testFlusherCloser{
			// The save writes the buffer as it was when the flush started; an
			// edit that lands while the disk write is in flight is not in it.
			flushFn: func() error {
				editDuringSave()
				return nil
			},
		}

		h, err := c.OpenFileTab(resource1, true)
		require.NoError(t, err)
		require.NoError(t, win.SetContent(h))

		ed, err := c.Editor(resource1)
		require.NoError(t, err)
		editDuringSave = func() {
			_, _, _ = ed.CellEditor().Edit(ctx, term.Coordinates{}, term.Coordinates{}, "ABC")
		}
		require.NoError(t, awaitErr(c.Flush(ctx, win)))

		dirty, ok := c.IsDirty(resource1)
		require.True(t, ok)
		assert.True(t, dirty, "an edit the save did not write must stay unflushed")
	})
}

// TestReloadClampsStaleCursor guards the editorFlusherCloser reload seam: when a
// reload replaces the buffer with a shorter file, a caret left on a now-missing
// row must be pulled back into bounds before any row-indexed buffer access runs.
// It drives the real production path — text.Component + the standard editor +
// c.Reload — rather than mutating the buffer directly, so the efc.OnDidEdit
// reload guard (gated by the reloading flag) is exercised end to end.
func TestReloadClampsStaleCursor(t *testing.T) {
	resource, err := workspaceapi.ParseURI("file:///robust.go")
	require.NoError(t, err)

	const tall = "l0\nl1\nl2\nl3\nl4\nl5\nl6\nl7\n"
	shrinks := []struct {
		name     string
		reloadTo string
	}{
		{"single char", "a"},
		{"empty", ""},
		{"two lines", "x\ny"},
		{"blank lines", "\n\n"},
	}
	for _, s := range shrinks {
		t.Run(s.name, func(t *testing.T) {
			cfg := text.DefaultConfig()
			cfg.ScheduleNextTick = func(fn func()) bool { fn(); return true }
			c, loader := newTestComponentConfig(t, standard.Editor(), cfg)
			loader.content = tall
			loader.reloadContent = s.reloadTo

			win, err := c.Focus()
			require.NoError(t, err)
			c.Resize(20, 10)

			h, err := c.OpenFileTab(resource, false)
			require.NoError(t, err)
			require.NoError(t, win.SetContent(h))
			c.Resize(20, 10)

			ed, err := c.Editor(resource)
			require.NoError(t, err)

			// Strand the caret on the last row of the tall file.
			ed.SetCursorAtScroll(term.Coordinates{Y: 7})

			require.NoError(t, awaitErr(c.Reload(context.Background(), win)))

			// The reload guard must have clamped the caret inside the shorter
			// buffer: never on a non-existent row, never negative.
			rows := ed.CellView().Rows()
			pos := ed.CursorAtScroll()
			require.GreaterOrEqual(t, pos.Y, 0)
			require.LessOrEqual(t, pos.Y, rows,
				"caret row must be within the reloaded buffer")

			// An edit that reads Columns(cursorRow) must not panic now that the
			// caret is back in bounds (this is the InsertLineBelow crash site).
			ced := ed.CellEditor()
			require.NotPanics(t, func() {
				pos := ed.CursorAtScroll()
				_, _, _ = ced.Edit(context.Background(), pos, pos, "\n")
			})
		})
	}
}

func TestOverwrite(t *testing.T) {
	resource1, err := workspaceapi.ParseURI("file:///a")
	require.NoError(t, err)

	t.Run("integration with event dispatching", func(t *testing.T) {
		ctx := context.Background()
		c, testLoader := newTestComponent(t, NopEditor())
		win, err := c.Focus()
		require.NoError(t, err)

		const content = "abc\ndef\n"
		testLoader.content = content

		h, err := c.OpenFileTab(resource1, true)
		require.NoError(t, err)
		require.NoError(t, win.SetContent(h))

		cfg := text.DefaultConfig()

		cfg.ScheduleNextTick = func(fn func()) bool { fn(); return true }
		tracker := extutil.NewResourceTracker(cfg.Tabspaces, false)
		err = c.SubscribeEvents([]textapi.EventType{
			textapi.EventTypeFlush, textapi.EventTypeOpen,
		}, tracker)
		require.NoError(t, err)

		ed, err := c.Editor(resource1)
		require.NoError(t, err)

		ced := ed.CellEditor()
		_, _, _ = ced.Edit(ctx, term.Coordinates{}, term.Coordinates{}, "ABC")

		assertContent := func(t *testing.T, expected string) {
			t.Helper()
			cview := ed.CellView()
			cells := cview.RawCells()
			assert.Equal(t, expected, term.CellsToString(cells))

			res, ok := tracker.Resource(resource1)
			require.True(t, ok)
			assert.Equal(t, expected, res.Buffer().String())
		}

		require.NoError(t, awaitErr(c.Overwrite(context.Background(), win)))
		assertContent(t, "ABC"+content)
	})

	t.Run("integration with IsDirty", func(t *testing.T) {
		ctx := context.Background()
		c, testLoader := newTestComponent(t, NopEditor())
		win, err := c.Focus()
		require.NoError(t, err)

		const content = "abc\ndef\n"
		testLoader.content = content

		h, err := c.OpenFileTab(resource1, true)
		require.NoError(t, err)
		require.NoError(t, win.SetContent(h))

		ed, err := c.Editor(resource1)
		require.NoError(t, err)

		ced := ed.CellEditor()
		_, _, _ = ced.Edit(ctx, term.Coordinates{}, term.Coordinates{}, "ABC")
		require.NoError(t, err)

		isDirty, ok := c.IsDirty(resource1)
		require.True(t, ok)
		assert.True(t, isDirty)

		require.NoError(t, awaitErr(c.Overwrite(context.Background(), win)))

		isDirty, ok = c.IsDirty(resource1)
		require.True(t, ok)
		assert.False(t, isDirty)
	})
}

func testCommand(cmd string, summary string, synopsis string) textapi.CommandManual {
	return textapi.CommandManual{
		Name:     cmd,
		Synopsis: synopsis,
		Summary:  summary,
	}
}

func assertFocusWidthHeight(t *testing.T,
	expectedWidth, expectedHeight int,
	ev textapi.Event,
) {
	assert.Equal(t, term.Coordinates{
		X: expectedWidth, Y: expectedHeight,
	}, ev.Start)
	edh, ok := ev.Resource.(*TestEditorHandler)
	require.True(t, ok)
	assert.Equal(t, expectedWidth, edh.Width)
	assert.Equal(t, expectedHeight, edh.Height)
}

func expectFocusEvents(
	t *testing.T, mock *MockEventHandler, focus, unfocus workspaceapi.URI,
	expectedWidth, expectedHeight int,
) {
	mock.EXPECT().Handle(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, ev textapi.Event) bool {
			if textapi.EventTypeFocus == ev.Type {
				assert.Equal(t, focus.String(), ev.URI.String())
				assertFocusWidthHeight(t, expectedWidth, expectedHeight, ev)
			} else if textapi.EventTypeUnfocus == ev.Type {
				assert.Equal(t, unfocus.String(), ev.URI.String())
			}
			return false
		}).Times(2)
}

func expectFocusEvent(
	t *testing.T, mock *MockEventHandler, uri workspaceapi.URI,
	expectedWidth, expectedHeight int,
) {
	mock.EXPECT().Handle(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, ev textapi.Event) bool {
			assert.Equal(t, textapi.EventTypeFocus, ev.Type)
			assert.Equal(t, uri.String(), ev.URI.String())
			assertFocusWidthHeight(t, expectedWidth, expectedHeight, ev)
			return false
		})
}

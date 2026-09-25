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

package idecursortest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/ide/idecursor"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/vi"
	"unstable.build/rune/internal/workspace"
)

const commandName = "cursorhistory"

func TestWithHistoryIntegration(t *testing.T) {
	tcases := []struct {
		name string
		run  func(*testing.T, *fixture)
	}{
		{
			name: "records vi jump and tab switch and navigates back",
			run: func(t *testing.T, f *fixture) {
				a := f.writeFile(t, "a.txt", lines(40))
				b := f.writeFile(t, "b.txt", lines(20))

				ha := f.open(t, a)
				handleKeys(t, ha, "j15j")

				hb := f.open(t, b)
				handleKeys(t, hb, "j")

				assert.Equal(t, []string{
					"a.txt:17:1",
					"a.txt:2:1",
				}, f.complete(t, "prev"))

				f.dispatch(t, "prev")
				ha = f.editor(t, a)
				assert.Equal(t, term.Coordinates{Y: 16, X: 0}, ha.CursorAtScroll())

				assert.Equal(t, []string{"b.txt:2:1", "a.txt:2:1"}, f.complete(t, "next"))
			},
		},
		{
			name: "records SetCursorAtScroll through the publisher",
			run: func(t *testing.T, f *fixture) {
				file := f.writeFile(t, "scroll.txt", lines(50))

				h := f.open(t, file)
				require.True(t, h.SetCursorAtScroll(term.Coordinates{Y: 4, X: 0}))
				require.True(t, h.SetCursorAtScroll(term.Coordinates{Y: 24, X: 3}))
				assert.Equal(t, []string{"scroll.txt:5:1"}, f.complete(t, "prev"))

				f.dispatch(t, "prev")
				h = f.editor(t, file)
				assert.Equal(t, term.Coordinates{Y: 4, X: 0}, h.CursorAtScroll())
			},
		},
		{
			name: "records the cursor location before a cross file jump after small vi motions",
			run: func(t *testing.T, f *fixture) {
				a := f.writeFile(t, "pre-jump-a.txt", lines(30))
				b := f.writeFile(t, "pre-jump-b.txt", lines(30))

				ha := f.open(t, a)
				handleKeys(t, ha, "jjjjjjj")
				assert.Equal(t, term.Coordinates{Y: 7, X: 0}, ha.CursorAtScroll())

				hb := f.open(t, b)
				require.True(t, hb.SetCursorAtScroll(term.Coordinates{Y: 12, X: 0}))

				assert.Equal(t, []string{"pre-jump-a.txt:8:1"}, f.complete(t, "prev"))
				f.dispatch(t, "prev")
				f.assertCursor(t, a, term.Coordinates{Y: 7, X: 0})
			},
		},
		{
			name: "records the cursor location before a cross file open without destination motion",
			run: func(t *testing.T, f *fixture) {
				a := f.writeFile(t, "open-a.txt", lines(30))
				b := f.writeFile(t, "open-b.txt", lines(30))

				ha := f.open(t, a)
				handleKeys(t, ha, "jjjjjjj")
				assert.Equal(t, term.Coordinates{Y: 7, X: 0}, ha.CursorAtScroll())

				_ = f.open(t, b)

				assert.Equal(t, []string{"open-a.txt:8:1"}, f.complete(t, "prev"))
				f.dispatch(t, "prev")
				f.assertCursor(t, a, term.Coordinates{Y: 7, X: 0})

				assert.Equal(t, []string{"open-b.txt:1:1"}, f.complete(t, "next"))
				f.dispatch(t, "next")
				f.assertCursor(t, b, term.Coordinates{})
			},
		},
		{
			name: "records definition jump from workspace file to external dependency",
			run: func(t *testing.T, f *fixture) {
				workspaceFile := f.writeFile(t, "callback.go", longLines(60))
				externalFile := f.writeExternalFile(t, "semanticapi/callback.go", longLines(60))

				h := f.open(t, workspaceFile)
				handleKeys(t, h, "37j24l")
				assert.Equal(t, term.Coordinates{Y: 37, X: 24}, h.CursorAtScroll())

				definition := f.open(t, externalFile)
				require.True(t, definition.SetCursorAtScroll(term.Coordinates{Y: 10, X: 2}))

				assert.Equal(t, []string{"callback.go:38:25"}, f.complete(t, "prev"))
				f.dispatch(t, "prev")
				f.assertCursor(t, workspaceFile, term.Coordinates{Y: 37, X: 24})
			},
		},
		{
			name: "loads persisted history into a new editor component",
			run: func(t *testing.T, f *fixture) {
				file := f.writeFile(t, "persisted.txt", lines(50))

				h := f.open(t, file)
				require.True(t, h.SetCursorAtScroll(term.Coordinates{Y: 3, X: 0}))
				require.True(t, h.SetCursorAtScroll(term.Coordinates{Y: 23, X: 0}))

				reopened := newFixture(t, f.dir, f.store)
				assert.Equal(t, []string{"persisted.txt:4:1"}, reopened.complete(t, "prev"))
			},
		},
		{
			name: "records gg and G jumps in one file",
			run: func(t *testing.T, f *fixture) {
				file := f.writeFile(t, "vim-jumps.txt", lines(60))
				h := f.open(t, file)

				require.True(t, h.SetCursorAtScroll(term.Coordinates{Y: 5, X: 0}))
				handleKeys(t, h, "Ggg")

				assert.Equal(t, term.Coordinates{Y: 0, X: 0}, h.CursorAtScroll())
				assert.Equal(t, []string{
					"vim-jumps.txt:60:1",
					"vim-jumps.txt:6:1",
				}, f.complete(t, "prev"))

				f.dispatch(t, "prev")
				f.assertCursor(t, file, term.Coordinates{Y: 59, X: 0})
			},
		},
		{
			name: "ignores small vi motions until a large vi jump",
			run: func(t *testing.T, f *fixture) {
				file := f.writeFile(t, "small-then-large.txt", lines(40))
				h := f.open(t, file)

				require.True(t, h.SetCursorAtScroll(term.Coordinates{Y: 5, X: 0}))
				handleKeys(t, h, "jjj")

				assert.Equal(t, term.Coordinates{Y: 8, X: 0}, h.CursorAtScroll())
				assert.Empty(t, f.complete(t, "prev"))
				assert.Empty(t, f.complete(t, "jump"))

				handleKeys(t, h, "G")
				assert.Equal(t, []string{"small-then-large.txt:9:1"}, f.complete(t, "prev"))
				assert.Equal(t, []string{
					"small-then-large.txt:40:1",
					"small-then-large.txt:9:1",
				}, f.complete(t, "jump"))
			},
		},
		{
			name: "mixes SetCursorAtScroll and vi jumps across files",
			run: func(t *testing.T, f *fixture) {
				a := f.writeFile(t, "mixed-a.txt", lines(50))
				b := f.writeFile(t, "mixed-b.txt", lines(45))

				ha := f.open(t, a)
				require.True(t, ha.SetCursorAtScroll(term.Coordinates{Y: 12, X: 0}))
				handleKeys(t, ha, "G")

				hb := f.open(t, b)
				require.True(t, hb.SetCursorAtScroll(term.Coordinates{Y: 2, X: 0}))
				require.True(t, hb.SetCursorAtScroll(term.Coordinates{Y: 20, X: 2}))
				handleKeys(t, hb, "gg")

				assert.Equal(t, []string{
					"mixed-b.txt:21:3",
					"mixed-b.txt:3:1",
					"mixed-a.txt:50:1",
					"mixed-a.txt:13:1",
				}, f.complete(t, "prev"))

				f.dispatch(t, "prev")
				f.assertCursor(t, b, term.Coordinates{Y: 20, X: 2})
				f.dispatch(t, "prev")
				f.assertCursor(t, b, term.Coordinates{Y: 2, X: 0})
				f.dispatch(t, "prev")
				f.assertCursor(t, a, term.Coordinates{Y: 49, X: 0})
			},
		},
		{
			name: "reopens closed file tabs when walking history",
			run: func(t *testing.T, f *fixture) {
				a := f.writeFile(t, "closed-a.txt", lines(35))
				b := f.writeFile(t, "closed-b.txt", lines(35))

				ha := f.open(t, a)
				require.True(t, ha.SetCursorAtScroll(term.Coordinates{Y: 4, X: 0}))
				hb := f.open(t, b)
				require.True(t, hb.SetCursorAtScroll(term.Coordinates{Y: 18, X: 0}))

				f.close(t, b)
				f.assertClosed(t, b)

				f.dispatch(t, "prev")
				f.assertCursor(t, a, term.Coordinates{Y: 4, X: 0})

				f.dispatch(t, "next")
				f.assertOpen(t, b)
				f.assertCursor(t, b, term.Coordinates{Y: 18, X: 0})

				f.close(t, a)
				f.assertClosed(t, a)
				f.dispatch(t, "prev")
				f.assertOpen(t, a)
				f.assertCursor(t, a, term.Coordinates{Y: 4, X: 0})
			},
		},
	}

	for _, tcase := range tcases {
		t.Run(tcase.name, func(t *testing.T) {
			f := newFixture(t, t.TempDir(), storagestub.NewInMemoryService())
			tcase.run(t, f)
		})
	}
}

type fixture struct {
	dir          string
	workspaceURI workspaceapi.URI
	workspace    workspace.Workspace
	component    *text.Component
	browser      testBrowserAdapter
	store        storageapi.Service
	closer       io.Closer
}

func newFixture(t *testing.T, dir string, store storageapi.Service) *fixture {
	t.Helper()
	workspaceURI, err := workspaceapi.ParseURI("file://" + dir)
	require.NoError(t, err)

	scheme, err := workspace.NewFileScheme(context.Background(), config.NopConfig(), workspaceURI)
	require.NoError(t, err)
	ws := workspace.NewSchemeWorkspace(workspaceURI, scheme, inlineSchedule)

	cfgc := text.DefaultConfig()

	cfgc.ScheduleNextTick = func(fn func()) bool { fn(); return true }

	c, err := text.NewComponent(vi.Editor(), ws, cfgc)
	require.NoError(t, err)
	c.Resize(100, 40)

	adapter := testBrowserAdapter{component: c}
	manager := testWorkspaceManager{workspaceURI: workspaceURI, workspace: ws}
	closer, err := idecursor.WithHistory(
		c, store, adapter, adapter, ws, testParser{}, manager, workspaceURI,
		func(fn func()) bool {
			go fn()
			return true
		},
	)
	require.NoError(t, err)
	return &fixture{
		dir:          dir,
		workspaceURI: workspaceURI,
		workspace:    ws,
		component:    c,
		browser:      adapter,
		store:        store,
		closer:       closer,
	}
}

func (f *fixture) writeFile(t *testing.T, name, content string) workspaceapi.URI {
	t.Helper()
	return f.writeFileAt(t, f.dir, name, content)
}

func (f *fixture) writeExternalFile(t *testing.T, name, content string) workspaceapi.URI {
	t.Helper()
	return f.writeFileAt(t, t.TempDir(), name, content)
}

func (f *fixture) writeFileAt(t *testing.T, root, name, content string) workspaceapi.URI {
	t.Helper()
	path := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	uri, err := workspaceapi.ParseURI("file://" + path)
	require.NoError(t, err)
	return uri
}

func (f *fixture) open(t *testing.T, uri workspaceapi.URI) text.Handler {
	t.Helper()
	h, err := f.browser.Open(uri)
	require.NoError(t, err)
	win, err := f.browser.Focus()
	require.NoError(t, err)
	err = f.browser.SetWindowContent(win, h)
	if err != nil && !errors.Is(err, browserapi.ErrTabNotFree) {
		require.NoError(t, err)
	}
	return f.editor(t, uri)
}

func (f *fixture) editor(t *testing.T, uri workspaceapi.URI) text.Handler {
	t.Helper()
	h, err := f.component.Editor(uri)
	require.NoError(t, err)
	return h
}

func (f *fixture) close(t *testing.T, uri workspaceapi.URI) {
	t.Helper()
	h, ok := f.component.Resource(uri)
	require.True(t, ok)
	require.NoError(t, f.component.RemoveTab(h))
}

func (f *fixture) assertCursor(t *testing.T, uri workspaceapi.URI, pos term.Coordinates) {
	t.Helper()
	assert.Equal(t, pos, f.editor(t, uri).CursorAtScroll())
}

func (f *fixture) assertOpen(t *testing.T, uri workspaceapi.URI) {
	t.Helper()
	_, ok := f.component.Resource(uri)
	assert.True(t, ok)
}

func (f *fixture) assertClosed(t *testing.T, uri workspaceapi.URI) {
	t.Helper()
	_, ok := f.component.Resource(uri)
	assert.False(t, ok)
}

func (f *fixture) dispatch(t *testing.T, args ...string) {
	t.Helper()
	win, err := f.browser.Focus()
	require.NoError(t, err)
	handled, err := f.component.DispatchCommand(context.Background(), textapi.Command{
		Name:   commandName,
		Args:   args,
		Window: win,
	})
	require.NoError(t, err)
	require.True(t, handled)
}

func (f *fixture) complete(t *testing.T, args ...string) []string {
	t.Helper()
	iter, _, err := f.component.CompleteCommand(context.Background(), textapi.Command{
		Name: commandName,
		Args: args,
	})
	require.NoError(t, err)
	var ret []string
	for v, ok := iter.Next(context.Background()); ok; v, ok = iter.Next(context.Background()) {
		ret = append(ret, v)
	}
	return ret
}

func handleKeys(t *testing.T, h text.Handler, input string) {
	t.Helper()
	keys, err := term.ParseKeys(input)
	require.NoError(t, err)
	for _, key := range keys {
		h.Handle(term.Event{Ch: key.Ch, Mod: key.Mod, Key: key.Key, Type: term.EventKey})
	}
}

func lines(n int) string {
	var b strings.Builder
	for i := range n {
		_, _ = fmt.Fprintf(&b, "line %02d\n", i)
	}
	return b.String()
}

func longLines(n int) string {
	var b strings.Builder
	for i := range n {
		_, _ = fmt.Fprintf(&b, "line %02d abcdefghijklmnopqrstuvwxyz\n", i)
	}
	return b.String()
}

type testBrowserAdapter struct {
	component *text.Component
}

func (a testBrowserAdapter) Focus() (browserapi.Window, error) {
	return a.component.Focus()
}

func (a testBrowserAdapter) Split(
	o browserapi.Orientation, w browserapi.Window, h browserapi.Handler,
) (browserapi.Window, error) {
	win, err := a.window(w)
	if err != nil {
		return nil, err
	}
	return a.component.Split(o, win, h)
}

func (a testBrowserAdapter) Floating(
	h browserapi.Floating, cfg browserapi.FloatingConfig,
) (browserapi.Window, error) {
	return a.component.Floating(h, cfg)
}

func (a testBrowserAdapter) Bar(cfg browserapi.BarConfig, h tui.Handler) error {
	return a.component.Bar(cfg, h)
}

func (a testBrowserAdapter) Tab(
	uri workspaceapi.URI, icon rune, name string, h browserapi.Handler,
) (browserapi.Handler, error) {
	return a.component.Tab(uri, icon, name, h)
}

func (a testBrowserAdapter) SetWindowContent(w browserapi.Window, h browserapi.Handler) error {
	win, err := a.window(w)
	if err != nil {
		return err
	}
	return win.SetContent(h)
}

func (a testBrowserAdapter) CloseWindow(w browserapi.Window) error {
	win, err := a.window(w)
	if err != nil {
		return err
	}
	return win.Close()
}

func (a testBrowserAdapter) SetTabActivity(workspaceapi.URI, bool) error { return nil }

func (a testBrowserAdapter) Open(uri workspaceapi.URI) (browserapi.Handler, error) {
	return a.component.Open(uri)
}

func (a testBrowserAdapter) window(w browserapi.Window) (browser.Window, error) {
	win, ok := a.component.Window(w.WindowID())
	if !ok {
		return nil, fmt.Errorf("window %d not found", w.WindowID())
	}
	return win, nil
}

type testWorkspaceManager struct {
	workspaceURI workspaceapi.URI
	workspace    workspace.Workspace
}

func (m testWorkspaceManager) RegisterScheme(string, schemeapi.SchemeFunc) error {
	return nil
}

func (m testWorkspaceManager) UnregisterScheme(string) error {
	return nil
}

func (m testWorkspaceManager) AddWorkspace(context.Context, workspaceapi.URI) (workspace.Workspace, error) {
	return m.workspace, nil
}

func (m testWorkspaceManager) Workspace(uri workspaceapi.URI) (workspace.Workspace, bool, error) {
	return m.workspace, workspaceapi.HasPrefix(uri, m.workspaceURI), nil
}

func (testWorkspaceManager) IncrementReference(workspaceapi.URI) {}

func (testWorkspaceManager) DecrementReference(workspaceapi.URI) error { return nil }

func (testWorkspaceManager) RemoveWorkspace(workspaceapi.URI) (workspace.Workspace, bool) {
	return nil, false
}

type testParser struct{}

func (testParser) Search(
	string, []string, ...string,
) (iterator.Iterator[syntaxapi.Result], error) {
	return iterator.Empty[syntaxapi.Result](), nil
}

func (testParser) ResolveSymbol(context.Context, string, syntaxapi.Progress) (
	iterator.Iterator[syntaxapi.Match], error,
) {
	return iterator.Empty[syntaxapi.Match](), nil
}

func (testParser) ListReferencedSymbols(context.Context) (iterator.Iterator[string], error) {
	return iterator.Empty[string](), nil
}

func (testParser) SearchNode(syntaxapi.NodeCaptureName, ...string) (
	iterator.Iterator[syntaxapi.Result], error,
) {
	return iterator.Empty[syntaxapi.Result](), nil
}

func (testParser) Query(
	workspaceapi.URI, string, []string,
) (iterator.Iterator[syntaxapi.Result], error) {
	return iterator.Empty[syntaxapi.Result](), nil
}

func (testParser) QueryNode(workspaceapi.URI, syntaxapi.NodeCaptureName) (
	iterator.Iterator[syntaxapi.Result], error,
) {
	return iterator.Empty[syntaxapi.Result](), nil
}

func (testParser) Highlight(workspaceapi.URI, string) (
	iterator.Iterator[textapi.Location], error,
) {
	return iterator.Empty[textapi.Location](), nil
}

var _ syntaxapi.Parser = testParser{}

var _ browserapi.ResourceOpener = testBrowserAdapter{}

var _ browserapi.WindowManager = testBrowserAdapter{}

var _ workspace.WorkspaceManager = testWorkspaceManager{}

// inlineSchedule is a synchronous workspace.ScheduleNextTick stub
// that runs fn on the calling goroutine. Test-only: production code
// must use the host event-loop scheduler so reload's buffer
// mutations do not run on a worker goroutine.
func inlineSchedule(fn func()) bool {
	fn()
	return true
}

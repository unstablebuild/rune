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

package ide

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os/user"

	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	sdkiterator "github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/browser/browsertest"
	"unstable.build/rune/internal/cell"
	tcomponent "unstable.build/rune/internal/component"
	"unstable.build/rune/internal/component/notifications"
	"unstable.build/rune/internal/debug"
	thandler "unstable.build/rune/internal/handler"
	"unstable.build/rune/internal/handler/command"
	"unstable.build/rune/internal/handler/handlertest"
	"unstable.build/rune/internal/ide/idehistory"
	"unstable.build/rune/internal/ide/ideshell"
	"unstable.build/rune/internal/ide/plugin"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/term/vte/vtereservoir"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/cmdenv"
	"unstable.build/rune/internal/text/emacs"
	"unstable.build/rune/internal/text/exoeditor"
	"unstable.build/rune/internal/text/registerset"
	"unstable.build/rune/internal/text/standard"
	"unstable.build/rune/internal/text/texttest"
	"unstable.build/rune/internal/text/vi"
	"unstable.build/rune/internal/workspace"
	"unstable.build/rune/internal/workspace/workspacetest"
)

// handlertest.TestHandlerSequence maps ':' characters to the following event
// this is to work around ex's assumptions on underlying handler.
var testCommandKey = term.KeyComb{Ch: '\\', Mod: term.ModCtrl}
var bgctx = context.Background()

type browserConstructor func(ed text.Editor, opts ...text.Option) (tui.Handler, browser.Browser, error)

type testFileBuffer struct {
	readOnly  bool
	flushErr  error
	reloadErr error
	closeErr  error
	closed    bool
	lastFlush time.Time
}

func (t *testFileBuffer) Flush(context.Context) (<-chan error, error) {
	if t.readOnly {
		return testFBDone(workspaceapi.ErrFileIsNotWritable), nil
	}
	t.lastFlush = time.Now()
	return testFBDone(t.flushErr), nil
}

func (t *testFileBuffer) Reload(context.Context) (<-chan error, error) {
	t.lastFlush = time.Now()
	return testFBDone(t.reloadErr), nil
}

func (t *testFileBuffer) ForceFlush(context.Context) (<-chan error, error) {
	if t.readOnly {
		t.readOnly = false
	}
	t.lastFlush = time.Now()
	return testFBDone(t.flushErr), nil
}

func testFBDone(err error) <-chan error {
	ch := make(chan error, 1)
	ch <- err
	close(ch)
	return ch
}

func (t *testFileBuffer) LastFlush() time.Time {
	return t.lastFlush
}

func (t *testFileBuffer) Close() error {
	t.closed = true
	return t.closeErr
}

type testLoader struct {
	buf *testFileBuffer
	// exitStartedCommands makes StartCommand's stub process exit
	// immediately with success, mirroring the Executor contract that
	// the watcher observes process exit. Tests that model a
	// long-running process (e.g. plugin-wait timeouts) leave it
	// false so the stub process never exits.
	exitStartedCommands bool
}

func (w *testLoader) Remove(string) error {
	return nil
}

func (w *testLoader) MkdirAll(string, fs.FileMode) error {
	return nil
}

func (w *testLoader) Close() error {
	return nil
}

func (w *testLoader) StartCommand(ctx context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
	if w.exitStartedCommands && cmd.Watcher != nil {
		ch := cmd.Watcher.WatchProcess()
		go debug.CapturePanicReport(func() {
			select {
			case ch <- nil:
			case <-ctx.Done():
			}
		})
	}
	return 0, nil
}

func (w *testLoader) Signal(workspaceapi.Pid, syscall.Signal) error {
	return nil
}

func (w *testLoader) InstallDataDir(context.Context) (string, error) {
	return "", errors.ErrUnsupported
}

func (w *testLoader) Load(
	filePath workspaceapi.URI, buf *cell.Buffer,
	swapDir workspaceapi.URI, readOnly bool,
) (
	workspace.FlusherCloser, error,
) {
	if w.buf != nil {
		return w.buf, nil
	}
	return &testFileBuffer{readOnly: readOnly}, nil
}

func (w *testLoader) Recover(
	filePath, swapFilePath workspaceapi.URI,
	buf *cell.Buffer, force bool,
) (workspace.FlusherCloser, error) {
	return w.Load(filePath, buf, swapFilePath, false)
}

func (w *testLoader) ReadDir(name string) ([]os.DirEntry, error) {
	return nil, nil
}

func (w *testLoader) Stat(name string) (os.FileInfo, error) {
	return testFileInfo{name: name}, nil
}

func (w *testLoader) OpenFile(
	path string, flag int, perm os.FileMode,
) (workspaceapi.File, error) {
	// Returning an error here causes text.Component's streaming
	// open path to fall back to the synchronous workspace.Load
	// flow, which testLoader.Load services. Without this, every
	// ide test that opens a file in a non-real workspace would
	// panic on the streamload pre-read.
	return nil, errors.New("ide/ex_test: testLoader.OpenFile not implemented")
}
func (w *testLoader) NewPty(context.Context) (workspaceapi.Pty, error) {
	return workspaceapi.Pty{
		Master: workspacetest.NewFile(),
		Slave:  workspacetest.NewFile(),
	}, nil
}

func (w *testLoader) SetPtySize(p workspaceapi.Pty, size workspaceapi.PtySize) error {
	return nil
}

func (w *testLoader) NewFile(fd uintptr, name string) workspaceapi.File {
	panic("unimplemented")
}

func (w *testLoader) Rename(old, new string) error {
	panic("unimplemented")
}

func (w *testLoader) Lstat(path string) (os.FileInfo, error) {
	panic("unimplemented")
}

func (w *testLoader) Readlink(path string) (string, error) {
	panic("unimplemented")
}

func (w *testLoader) Watch(
	path string, c chan<- schemeapi.EventInfo, events ...schemeapi.Event,
) (int, error) {
	return 0, nil
}

func (w *testLoader) StopWatch(int) error {
	return nil
}

func (t testLoader) Chroot(path string) (schemeapi.Scheme, error) {
	panic("unimplemented")
}

func (t testLoader) Root() string {
	panic("unimplemented")
}

func (t testLoader) Symlink(target, link string) error {
	panic("unimplemented")
}

func (t testLoader) TempFile(dir, prefix string) (workspaceapi.File, error) {
	panic("unimplemented")
}

func (t testLoader) Join(elem ...string) string {
	panic("unimplemented")
}

func (t testLoader) Create(filename string) (workspaceapi.File, error) {
	panic("unimplemented")
}

func (t testLoader) Open(filename string) (workspaceapi.File, error) {
	panic("unimplemented")
}

type testFileInfo struct {
	name string
}

func (t testFileInfo) Name() string {
	return t.name
}

func (t testFileInfo) IsDir() bool {
	return t.name == "/" || t.name == "" || t.name == "."
}

func (t testFileInfo) ModTime() time.Time {
	return time.Time{}
}

func (t testFileInfo) Mode() os.FileMode {
	return 0
}

func (t testFileInfo) Size() int64 {
	return 0
}

func (t testFileInfo) Sys() any {
	return nil
}

func (w *testLoader) URI(path string) (workspaceapi.URI, error) {
	return workspaceapi.CurrentUserHostURI(path)
}

func TestBrowserHandlerDraw(t *testing.T) {
	testBrowserHandlerDraw(t, func(ed text.Editor, opts ...text.Option) (tui.Handler, browser.Browser, error) {
		b := newExForTesting(t, ed, opts...)
		return b, b.Browser(), nil
	})
}

func TestComponentOpenEditorIntegration(t *testing.T) {
	b := newExForTesting(t, texttest.NopEditor(),
		text.WithCommandKey(testCommandKey),
		text.WithCommandOverlayConfig(testCommandOverlayConfig()),
	)
	uri, err := workspaceapi.ParseURI("file:///bugz")
	require.NoError(t, err)

	tab, err := b.editFileURI(uri, b.invokeWindow(), false)
	require.NoError(t, err)
	assert.NotPanics(t, func() {
		_ = tab.Handler().(text.Handler)
	})
	assert.NoError(t, b.Close())
}

// TestHandlerInFocusNonTextTabReturnsURI guards command routing for tabs
// whose handler is not a text.Handler (e.g. rune-agent chat tabs). The
// focused tab's URI must still be returned so command-prompt commands can
// resolve the focused resource; only the inner text.Handler is absent.
func TestHandlerInFocusNonTextTabReturnsURI(t *testing.T) {
	b := newExForTesting(t, texttest.NopEditor(),
		text.WithCommandKey(testCommandKey),
		text.WithCommandOverlayConfig(testCommandOverlayConfig()),
	)
	defer b.Close()

	uri, err := workspaceapi.ParseURI("rune-agent://openai_gpt-5/rolling-fox")
	require.NoError(t, err)

	// A non-text handler, mirroring how rune-agent wraps its chat tab.
	tab, err := b.ex.comp.Tab(uri, '+', "rolling-fox", browsertest.NewTestHandler())
	require.NoError(t, err)
	require.NoError(t, b.ex.invokeWindow().SetContent(tab))

	gotURI, h, ok := b.ex.handlerInFocus()
	assert.False(t, ok, "non-text tab must not yield a text.Handler")
	assert.Nil(t, h)
	assert.Equal(t, uri, gotURI, "tab URI must be returned even without a text.Handler")
}

func TestReadfileCommandReadsFileInOtherWorkspace(t *testing.T) {
	currentDir := t.TempDir()
	currentWorkspaceURI, err := workspaceapi.ParseURI("file://" + currentDir)
	require.NoError(t, err)

	foreignDir := t.TempDir()
	foreignPath := filepath.Join(foreignDir, "foreign.txt")
	require.NoError(t, os.WriteFile(foreignPath, []byte("foreign contents"), 0o644))
	foreignURI, err := workspaceapi.ParseURI("file://" + foreignPath)
	require.NoError(t, err)

	workspaceWithForeignRead := &readfileCrossWorkspaceLoader{
		testWorkspaceWithURI: testWorkspaceWithURI{
			testLoader: &testLoader{},
			uri:        currentWorkspaceURI,
		},
		foreignURI:      foreignURI,
		currentContents: "current\n",
	}

	b := newExForTestingWithWorkspace(t, workspaceWithForeignRead,
		texttest.NopEditor(), vte.DefaultConfig(), nopPublishEvent,
		clipboard.NewInMemory(), text.WithCommandKey(testCommandKey),
		text.WithCommandOverlayConfig(testCommandOverlayConfig()))
	defer b.Close()

	currentFileURI, err := workspaceapi.ParseURI(
		"file://" + filepath.Join(currentDir, "current.txt"),
	)
	require.NoError(t, err)
	_, err = b.editFileURI(currentFileURI, b.invokeWindow(), false)
	require.NoError(t, err)

	require.NoError(t, b.ex.dispatchCommand("readfile", foreignURI.String()))

	_, h, ok := b.ex.handlerInFocus()
	require.True(t, ok)
	assert.Equal(t, "current\nforeign contents\n", term.CellsToString(h.CellView().RawCells()))
}

func TestFileExplorerOpenFile(t *testing.T) {
	uri, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)
	scheme, err := workspace.NewMemoryScheme(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	touchTestFile(t, scheme, "alpha.go")

	// NopEditor is modeless, and an editable modeless explorer keeps
	// <enter> as a newline; lock it, as the modeless presets do, so
	// <enter> opens the node.
	explorerCfg := text.DefaultFileExplorerConfig()
	explorerCfg.ReadOnly = true
	b := newExForTestingWithWorkspace(t, workspace.NewSchemeWorkspace(uri, scheme, inlineSchedule),
		texttest.NopEditor(), vte.DefaultConfig(), nopPublishEvent,
		clipboard.NewInMemory(), text.WithCommandKey(testCommandKey),
		text.WithCommandOverlayConfig(testCommandOverlayConfig()),
		text.WithFileExplorer(explorerCfg))
	defer b.Close()

	require.Nil(t, b.fileExplorerWin)
	require.NoError(t, b.fexplorer(context.Background()))
	require.NotNil(t, b.fileExplorerWin)

	assert.NotPanics(t, func() {
		_, handled := b.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
		assert.True(t, handled)
	})

	for _, tab := range b.comp.Browser().Tabs() {
		if tab.URI().String() == "memory:///alpha.go" {
			return
		}
	}
	t.Fatalf("expected alpha.go to be opened in a tab")
}

// TestFileExplorerToggleTwice reproduces the bug where toggling the
// file explorer off and on again returns a "command already
// registered" error. Each :fexplorer invocation that opens the
// explorer calls the real editor's Edit on the same URI, which
// subscribes file-level commands (fold/location/git). The second
// call must NOT re-register those commands for the same URI.
func TestFileExplorerToggleTwice(t *testing.T) {
	uri, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)
	scheme, err := workspace.NewMemoryScheme(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)

	// Use a vi.Editor with a workspace command registry so that
	// fold/location/git file-scoped commands get registered on
	// Edit — this is what the real IDE does and what makes the
	// bug reproducible.
	ed := vi.Editor(vi.WithWorkspaceCommandRegistry(
		uri, texttest.NopWorkspaceRegistry(),
	))
	b := newExForTestingWithWorkspace(t, workspace.NewSchemeWorkspace(uri, scheme, inlineSchedule),
		ed, vte.DefaultConfig(), nopPublishEvent,
		clipboard.NewInMemory(), text.WithCommandKey(testCommandKey),
		text.WithCommandOverlayConfig(testCommandOverlayConfig()))
	defer b.Close()

	// 1st call: opens the explorer and focuses it.
	require.NoError(t, b.fexplorer(context.Background()))
	require.NotNil(t, b.fileExplorerWin)

	// 2nd call: explorer is focused, so closes it.
	require.NoError(t, b.fexplorer(context.Background()))
	require.Nil(t, b.fileExplorerWin)

	// 3rd call: must re-open without "command already registered".
	require.NoError(t, b.fexplorer(context.Background()))
	require.NotNil(t, b.fileExplorerWin)

	// 4th call: closes again.
	require.NoError(t, b.fexplorer(context.Background()))
	require.Nil(t, b.fileExplorerWin)

	// 5th call: re-opens for the second cycle.
	require.NoError(t, b.fexplorer(context.Background()))
	require.NotNil(t, b.fileExplorerWin)
}

// TestFileExplorerOpenFileThenToggle exercises the realistic
// production path: user opens fexplorer, opens a file from it (which
// registers fold/location/git for that file's URI on the shared vi
// editor's registry), closes the explorer, then reopens. Before the
// caching fix, reopen re-invoked e.ed.Edit("memory:///fexplorer",
// ...) which re-subscribed per-file commands and failed with
// "command already registered".
func TestFileExplorerOpenFileThenToggle(t *testing.T) {
	uri, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)
	scheme, err := workspace.NewMemoryScheme(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	touchTestFile(t, scheme, "alpha.go")

	ed := vi.Editor(vi.WithWorkspaceCommandRegistry(
		uri, texttest.NopWorkspaceRegistry(),
	))
	b := newExForTestingWithWorkspace(t, workspace.NewSchemeWorkspace(uri, scheme, inlineSchedule),
		ed, vte.DefaultConfig(), nopPublishEvent,
		clipboard.NewInMemory(), text.WithCommandKey(testCommandKey),
		text.WithCommandOverlayConfig(testCommandOverlayConfig()))
	defer b.Close()

	require.NoError(t, b.fexplorer(context.Background()))
	require.NotNil(t, b.fileExplorerWin)

	// Open alpha.go from the file explorer via Enter.
	_, handled := b.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	require.True(t, handled)
	found := false
	for _, tab := range b.comp.Browser().Tabs() {
		if tab.URI().String() == "memory:///alpha.go" {
			found = true
			break
		}
	}
	require.True(t, found, "alpha.go should be open in a tab")

	// Close the explorer, then re-open it. This MUST succeed.
	require.NoError(t, b.fexplorer(context.Background()))
	require.Nil(t, b.fileExplorerWin)

	require.NoError(t, b.fexplorer(context.Background()))
	require.NotNil(t, b.fileExplorerWin)
}

// TestFileExplorerToggleViaTabKey reproduces the user's exact
// reproduction: with <tab> bound to :fexplorer, pressing <tab>
// repeatedly to open and close the explorer must never fail with
// "command already registered". This exercises the full event
// routing path: the KeyTab event is dispatched to the focused
// window's handler, propagates back to ex.handleEvent, and only
// then falls through to the command-key-binding dispatcher that
// runs :fexplorer. This path differs from calling fexplorer
// directly because when the explorer is focused, <tab> is first
// delivered to the file explorer handler (and hence to the vi
// editor chain) before reaching the :fexplorer binding.
func TestFileExplorerToggleViaTabKey(t *testing.T) {
	uri, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)
	scheme, err := workspace.NewMemoryScheme(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	touchTestFile(t, scheme, "alpha.go")

	// Use a stateful workspace registry that tracks subscriptions and
	// reports duplicates — this is what the real IDE uses and what
	// would surface a double-Edit on memory:///fexplorer.
	wsReg := newTrackingWorkspaceRegistry()
	ed := vi.Editor(vi.WithWorkspaceCommandRegistry(
		uri, wsReg,
	))
	b := newExForTestingWithWorkspace(t, workspace.NewSchemeWorkspace(uri, scheme, inlineSchedule),
		ed, vte.DefaultConfig(), nopPublishEvent,
		clipboard.NewInMemory(),
		text.WithCommandKey(testCommandKey),
		text.WithCommandOverlayConfig(testCommandOverlayConfig()),
		text.WithCommandKeyBinding(
			term.KeyComb{Key: term.KeyTab}, [][]string{{"fexplorer"}}))
	defer b.Close()
	b.Resize(30, 15)

	tabEv := term.Event{Type: term.EventKey, Key: term.KeyTab}

	// 1st <tab>: open explorer.
	b.Handle(tabEv)
	require.NotNil(t, b.fileExplorerWin, "explorer should be open after 1st <tab>")
	require.Empty(t, wsReg.errors(), "no errors after 1st <tab>")

	// 2nd <tab>: close explorer (explorer is focused).
	b.Handle(tabEv)
	require.Nil(t, b.fileExplorerWin, "explorer should be closed after 2nd <tab>")
	require.Empty(t, wsReg.errors(), "no errors after 2nd <tab>")

	// 3rd <tab>: re-open. This is where "command already
	// registered" would surface if the underlying editor was
	// re-created on reopen.
	b.Handle(tabEv)
	require.NotNil(t, b.fileExplorerWin, "explorer should be open after 3rd <tab>")
	require.Empty(t, wsReg.errors(), "no errors after 3rd <tab>")

	// 4th <tab>: close again.
	b.Handle(tabEv)
	require.Nil(t, b.fileExplorerWin, "explorer should be closed after 4th <tab>")
	require.Empty(t, wsReg.errors(), "no errors after 4th <tab>")
}

// TestFileExplorerRestoredAsTabNotDuplicated exercises the bug
// surfaced by the user's real-world setup: a previous session
// persisted memory:///fexplorer as an open file. On startup, the
// workspace handler restores it as a tab via editFileURI, which
// ends up calling vi.Editor.Edit on memory:///fexplorer and
// subscribing per-file commands. When the user then presses <tab>
// to open the explorer, ex.initFileExplorer calls Edit a second
// time on the same URI, which fails with "command already
// registered".
//
// With the fix, the file explorer URI is filtered out of session
// history and restore, so the second Edit call never happens.
func TestFileExplorerRestoredAsTabNotDuplicated(t *testing.T) {
	uri, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)
	scheme, err := workspace.NewMemoryScheme(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)

	wsReg := newTrackingWorkspaceRegistry()
	ed := vi.Editor(vi.WithWorkspaceCommandRegistry(
		uri, wsReg,
	))
	b := newExForTestingWithWorkspace(t, workspace.NewSchemeWorkspace(uri, scheme, inlineSchedule),
		ed, vte.DefaultConfig(), nopPublishEvent,
		clipboard.NewInMemory(),
		text.WithCommandKey(testCommandKey),
		text.WithCommandOverlayConfig(testCommandOverlayConfig()))
	defer b.Close()
	b.Resize(30, 15)

	// Simulate the session-restore path: a stale session cache lists
	// memory:///fexplorer as an open file. The workspace handler
	// would normally call editFileURI on it, which eventually calls
	// vi.Editor.Edit and subscribes fold/location/git commands for
	// the URI on the shared workspace registry.
	fexURI, err := workspaceapi.ParseURI("memory:///fexplorer")
	require.NoError(t, err)
	_, err = b.editFileURI(fexURI, b.invokeWindow(), false)
	require.NoError(t, err)
	require.Empty(t, wsReg.errors())

	// Now trigger :fexplorer. Before the fix this would call Edit
	// on the same URI a second time and one of the per-file command
	// subscriptions would return "command already registered".
	require.NoError(t, b.fexplorer(context.Background()))
	require.Empty(t, wsReg.errors(), "fexplorer must not double-register commands")
	require.NotNil(t, b.fileExplorerWin)
}

// TestFileExplorerTabCloseClosesWindow verifies that :tabclose on the
// focused file explorer window tears down the whole window (like
// toggling it off) instead of swapping in a free tab / wallpaper.
func TestFileExplorerTabCloseClosesWindow(t *testing.T) {
	uri, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)
	scheme, err := workspace.NewMemoryScheme(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)

	b := newExForTestingWithWorkspace(t, workspace.NewSchemeWorkspace(uri, scheme, inlineSchedule),
		texttest.NopEditor(), vte.DefaultConfig(), nopPublishEvent,
		clipboard.NewInMemory(), text.WithCommandKey(testCommandKey),
		text.WithCommandOverlayConfig(testCommandOverlayConfig()))
	defer b.Close()
	b.Resize(30, 15)

	prev := b.invokeWindow()
	require.NoError(t, b.fexplorer(context.Background()))
	require.NotNil(t, b.fileExplorerWin)
	require.Equal(t, b.fileExplorerWin, b.invokeWindow())

	require.NoError(t, b.tabclose(context.Background()))
	require.Nil(t, b.fileExplorerWin)
	require.Equal(t, prev, b.invokeWindow(), "focus should return to the previous target")
}

// TestFileExplorerTabSwitchNoOp verifies that :tabnext, :tabprevious
// and :tabfocus are silent no-ops while the file explorer window is
// focused, so its raw content can never be swapped for a free tab.
func TestFileExplorerTabSwitchNoOp(t *testing.T) {
	uri, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)
	scheme, err := workspace.NewMemoryScheme(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)

	b := newExForTestingWithWorkspace(t, workspace.NewSchemeWorkspace(uri, scheme, inlineSchedule),
		texttest.NopEditor(), vte.DefaultConfig(), nopPublishEvent,
		clipboard.NewInMemory(), text.WithCommandKey(testCommandKey),
		text.WithCommandOverlayConfig(testCommandOverlayConfig()))
	defer b.Close()
	b.Resize(30, 15)

	require.NoError(t, b.fexplorer(context.Background()))
	require.Equal(t, b.fileExplorerWin, b.invokeWindow())

	contentIsExplorer := func() {
		t.Helper()
		content, err := b.fileExplorerWin.Content()
		require.NoError(t, err)
		_, ok := content.(*fileExplorerHandler)
		require.True(t, ok, "explorer window content should stay the file explorer handler")
	}

	require.NoError(t, b.tabnext(context.Background()))
	contentIsExplorer()
	require.NoError(t, b.tabprevious(context.Background()))
	contentIsExplorer()
	require.NoError(t, b.tabfocus(context.Background(), "1"))
	contentIsExplorer()

	require.NotNil(t, b.fileExplorerWin, "explorer window should still be open")
}

// TestTabSwitchUnaffectedByExplorerGuard is a regression guard that the
// new fexplorer special-casing in the tab commands does not break
// normal tab switching in an unrelated window.
func TestTabSwitchUnaffectedByExplorerGuard(t *testing.T) {
	b := newExForTesting(t, texttest.NopEditor())
	defer b.Close()
	b.Resize(30, 15)

	require.NoError(t, b.terminalnew(context.Background(), "first"))
	require.NoError(t, b.terminalnewtab(context.Background(), "second"))
	require.NotEmpty(t, b.comp.Browser().Tabs())

	require.Nil(t, b.fileExplorerWin)
	require.NoError(t, b.tabprevious(context.Background()))
	require.NoError(t, b.tabnext(context.Background()))
}

// trackingWorkspaceRegistry is a text.WorkspaceCommandRegistry that
// records every subscribe/unsubscribe and fails fast on duplicates —
// mirroring the behaviour of the real per-workspace registry that
// backs vi's file command subscriptions.
type trackingWorkspaceRegistry struct {
	mu   sync.Mutex
	cmds map[string]struct{}
	errs []string
}

type editorWorkspaceRegistry struct {
	editor text.Editor
}

func (r *editorWorkspaceRegistry) SubscribeCommandForWorkspace(
	_ workspaceapi.URI, cmd textapi.CommandManual, handler text.CommandHandler,
) error {
	return r.editor.SubscribeCommand(cmd, handler)
}

func (r *editorWorkspaceRegistry) UnsubscribeCommandForWorkspace(
	_ workspaceapi.URI, name string,
) error {
	return r.editor.UnsubscribeCommand(name)
}

func newTrackingWorkspaceRegistry() *trackingWorkspaceRegistry {
	return &trackingWorkspaceRegistry{cmds: make(map[string]struct{})}
}

func (r *trackingWorkspaceRegistry) SubscribeCommandForWorkspace(
	_ workspaceapi.URI, cmd textapi.CommandManual, _ text.CommandHandler,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.cmds[cmd.Name]; ok {
		err := fmt.Sprintf("command already registered: %s", cmd.Name)
		r.errs = append(r.errs, err)
		return errors.New(err)
	}
	r.cmds[cmd.Name] = struct{}{}
	return nil
}

func (r *trackingWorkspaceRegistry) UnsubscribeCommandForWorkspace(
	_ workspaceapi.URI, name string,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.cmds, name)
	return nil
}

func (r *trackingWorkspaceRegistry) errors() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.errs...)
}

func testBrowserHandlerDraw(t *testing.T, constructor browserConstructor) {
	cases := []handlertest.SequenceTestCase{
		{"a",
			`┌──────────────────┐
│                  │
├──────────────────┤
│                  │
│                  │
│                  │
│                  │
│                  │
│                  │
└──────────────────┘`},
		{":edit a.go>",
			`┌━━━━━━────────────┐
│o a.go            │
├──────────────────┤
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
└──────────────────┘`},
		{"a",
			`┌━━━━━━────────────┐
│o a.go            │
├──────────────────┤
│BBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBB│
└──────────────────┘`},
		{":edit /tmp/o.go>",
			`┌────────━━━━━━────┐
│o a.go  o o.go    │
├──────────────────┤
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
└──────────────────┘`},
		{"#", // simulates ctrl-h
			`┌━━━━━━────────────┐
│o a.go  o o.go    │
├──────────────────┤
│BBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBB│
└──────────────────┘`},
		{"#", // simulates ctrl-h
			`┌────────━━━━━━────┐
│o a.go  o o.go    │
├──────────────────┤
│BBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBB│
└──────────────────┘`},
		{"$", // simulates ctrl-l
			`┌━━━━━━────────────┐
│o a.go  o o.go    │
├──────────────────┤
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
└──────────────────┘`},
		{"$", // simulates ctrl-l
			`┌────────━━━━━━────┐
│o a.go  o o.go    │
├──────────────────┤
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
└──────────────────┘`},
		{":tcl>",
			`┌━━━━━━────────────┐
│o a.go            │
├──────────────────┤
│DDDDDDDDDDDDDDDDDD│
│DDDDDDDDDDDDDDDDDD│
│DDDDDDDDDDDDDDDDDD│
│DDDDDDDDDDDDDDDDDD│
│DDDDDDDDDDDDDDDDDD│
│DDDDDDDDDDDDDDDDDD│
└──────────────────┘`},
		{":wq!^^^^^",
			`┌━━━━━━────────────┐
│o a.go            │
├──────────────────┤
│EEEEEEEEEEEEEEEEEE│
│EEEEEEEEEEEEEEEEEE│
│EEEEEEEEEEEEEEEEEE│
│EEEEEEEEEEEEEEEEEE│
│EEEEEEEEEEEEEEEEEE│
│EEEEEEEEEEEEEEEEEE│
└──────────────────┘`},
		{":<",
			`┌━━━━━━────────────┐
│o a.go            │
├──────────────────┤
│EEEEEEEEEEEEEEEEEE│
│EEEEEEEEEEEEEEEEEE│
│EEEEEEEEEEEEEEEEEE│
│EEEEEEEEEEEEEEEEEE│
│EEEEEEEEEEEEEEEEEE│
│EEEEEEEEEEEEEEEEEE│
└──────────────────┘`},
		{":edit o.go>1111",
			`┌────────━━━━━━────┐
│o a.go  o o.go    │
├──────────────────┤
│EEEEEEEEEEEEEEEEEE│
│EEEEEEEEEEEEEEEEEE│
│EEEEEEEEEEEEEEEEEE│
│EEEEEEEEEEEEEEEEEE│
│EEEEEEEEEEEEEEEEEE│
│EEEEEEEEEEEEEEEEEE│
└──────────────────┘`},
		{"$$##",
			`┌────────━━━━━━────┐
│o a.go  o o.go    │
├──────────────────┤
│GGGGGGGGGGGGGGGGGG│
│GGGGGGGGGGGGGGGGGG│
│GGGGGGGGGGGGGGGGGG│
│GGGGGGGGGGGGGGGGGG│
│GGGGGGGGGGGGGGGGGG│
│GGGGGGGGGGGGGGGGGG│
└──────────────────┘`},
	}
	bh, b, err := constructor(texttest.NopEditor(),
		text.WithCommandKey(testCommandKey),
		text.WithCommandKeyBinding(term.KeyComb{Ch: '4'}, [][]string{{"windowclose"}}),
	)
	require.NoError(t, err)

	handlertest.TestHandlerSequence(t, bh, 20, 10, cases)

	focus, err := b.Focus()
	require.NoError(t, err)

	win, err := b.Split(browserapi.OrientationLeft, focus, browsertest.NewTestHandler())
	require.NoError(t, err)

	focus = win

	h := browsertest.NewTestHandler()
	h.Ch = 'Z' // helps identify in tests

	_, err = b.Split(browserapi.OrientationBottom, focus, h)
	require.NoError(t, err)

	cases = []handlertest.SequenceTestCase{
		{"",
			`┌──────────────────┐
│o a.go  o o.go    │
├────────┐┌────────┤
│AAAAAAAA││GGGGGGGG│
│AAAAAAAA││GGGGGGGG│
└────────┘│GGGGGGGG│
┌────────┐│GGGGGGGG│
│ZZZZZZZZ││GGGGGGGG│
│ZZZZZZZZ││GGGGGGGG│
└────────┘└────────┘`},
		{":<111111111",
			`┌──────────────────┐
│o a.go  o o.go    │
├────────┐┌────────┤
│AAAAAAAA││GGGGGGGG│
│AAAAAAAA││GGGGGGGG│
└────────┘│GGGGGGGG│
┌────────┐│GGGGGGGG│
│cccccccc││GGGGGGGG│
│cccccccc││GGGGGGGG│
└────────┘└────────┘`},
	}

	handlertest.TestHandlerSequence(t, bh, 20, 10, cases)

	var closed int
	hx := browsertest.NewTestHandler()
	hx.Ch = '$'
	hx.CloseCallback = func() error { closed++; return nil }
	require.NoError(t, focus.SetContent(hx))

	cases = []handlertest.SequenceTestCase{
		{"",
			`┌──────────────────┐
│o a.go  o o.go    │
├────────┐┌────────┤
│$$$$$$$$││GGGGGGGG│
│$$$$$$$$││GGGGGGGG│
└────────┘│GGGGGGGG│
┌────────┐│GGGGGGGG│
│cccccccc││GGGGGGGG│
│cccccccc││GGGGGGGG│
└────────┘└────────┘`},
	}

	handlertest.TestHandlerSequence(t, bh, 20, 10, cases)

	require.NoError(t, win.Close())
	require.NoError(t, focus.Close())

	assert.Equal(t, 1, closed)

	cases = []handlertest.SequenceTestCase{
		// test CommandKeyBindings
		{"4$$$",
			`┌━━━━━━────────────┐
│o a.go  o o.go    │
├──────────────────┤
│HHHHHHHHHHHHHHHHHH│
│HHHHHHHHHHHHHHHHHH│
│HHHHHHHHHHHHHHHHHH│
│HHHHHHHHHHHHHHHHHH│
│HHHHHHHHHHHHHHHHHH│
│HHHHHHHHHHHHHHHHHH│
└──────────────────┘`},
		{":tcall>:edit o.go>bcde####",
			`┌━━━━━━────────────┐
│o o.go            │
├──────────────────┤
│IIIIIIIIIIIIIIIIII│
│IIIIIIIIIIIIIIIIII│
│IIIIIIIIIIIIIIIIII│
│IIIIIIIIIIIIIIIIII│
│IIIIIIIIIIIIIIIIII│
│IIIIIIIIIIIIIIIIII│
└──────────────────┘`},
	}
	handlertest.TestHandlerSequence(t, bh, 20, 10, cases)

	cases = []handlertest.SequenceTestCase{
		{"", `┌──┐
│  │
├II┤
IIII`},
	}
	handlertest.TestHandlerSequence(t, bh, 4, 4, cases)

	_, err = b.Notify(browserapi.LevelInfo, "wasup: %s", "Z")
	require.NoError(t, err)
	cases = []handlertest.SequenceTestCase{
		{"",
			`┌━━━━┌─────────────┐
│o o.│ wasup: Z    │
├────└─────────────┘
│IIIIIIIIIIIIIIIIII│
│IIIIIIIIIIIIIIIIII│
│IIIIIIIIIIIIIIIIII│
│IIIIIIIIIIIIIIIIII│
│IIIIIIIIIIIIIIIIII│
│IIIIIIIIIIIIIIIIII│
└──────────────────┘`},
	}
	handlertest.TestHandlerSequence(t, bh, 20, 10, cases)

	uri, err := workspaceapi.ParseURI("file:///bugz")
	require.NoError(t, err)
	nh, err := b.Open(uri)
	require.NoError(t, err)
	focus, err = b.Focus()
	require.NoError(t, err)
	err = focus.SetContent(nh)
	require.NoError(t, err)

	cases = []handlertest.SequenceTestCase{
		{"b",
			`┌────┌─────────────┐
│o o.│ wasup: Z    │
├────└─────────────┘
│BBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBB│
└──────────────────┘`},
		{":3>",
			`┌────┌─────────────┐
│o o.│ wasup: Z    │
├────└─────────────┘
│BBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBB│
│▐BBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBB│
└──────────────────┘`},
		{":0>",
			`┌────┌─────────────┐
│o o.│ wasup: Z    │
├────└─────────────┘
│BBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBB│
└──────────────────┘`},
	}
	handlertest.TestHandlerSequence(t, bh, 20, 10, cases)

	// calls from a browser client will block until the first
	// "use" of the installed handler. Client and server
	// are tipically running in different goroutines so
	// this is not a problem elsewhere.
	testCh := make(chan struct{})
	defer close(testCh)
	go func() {
		for {
			select {
			case _, ok := <-testCh:
				if !ok {
					return
				}
			default:
				if sh, ok := bh.(*safeHandler); ok {
					sh.Draw(term.NewStringWriter(209, 100))
				}
			}
		}
	}()

	floating1, err := b.Floating(browsertest.NewTestFloating(4, 2),
		browserapi.FloatingConfig{Offset: term.Coordinates{X: 1, Y: 1}})
	require.NoError(t, err)

	focus, err = b.Focus()
	require.NoError(t, err)

	// should not be able to split over a floating window, which is currently in focus
	_, err = b.Split(browserapi.OrientationTop, focus, browsertest.NewTestHandler())
	require.Error(t, err)
	cases = []handlertest.SequenceTestCase{
		{"",
			`┌────┌─────────────┐
│o o.│ wasup: Z    │
├────└─────────────┘
│█●████BBBBBBBBBBBB│
││AAAA│BBBBBBBBBBBB│
││AAAA│BBBBBBBBBBBB│
│└────┘BBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBB│
└──────────────────┘`},
		{":e!>", // test reload non file
			`┌────┌─────────────┐
│o o.│ cannot      │
├────│ reload      │
│█●██│ this        │
││AAA│ content     │
││AAA└─────────────┘
│└───┌─────────────┐
│BBBB│ wasup: Z    │
│BBBB└─────────────┘
└──────────────────┘`},
	}

	handlertest.TestHandlerSequence(t, bh, 20, 10, cases)

	require.NoError(t, floating1.Close())

	cases = []handlertest.SequenceTestCase{
		{"",
			`┌────┌─────────────┐
│o o.│ cannot      │
├────│ reload      │
│BBBB│ this        │
│BBBB│ content     │
│BBBB└─────────────┘
│BBBB┌─────────────┐
│BBBB│ wasup: Z    │
│BBBB└─────────────┘
└──────────────────┘`},
	}
	handlertest.TestHandlerSequence(t, bh, 20, 10, cases)

	o := browserapi.BarConfig{Size: 1, Orientation: browserapi.OrientationTop}
	for i := range 4 {
		b1 := browsertest.NewTestHandler()
		b1.Ch = rune(strconv.Itoa(i)[0])
		err = b.Bar(o, b1)
		require.NoError(t, err)
		o.Orientation++
	}

	// test case for issue #27
	cases = []handlertest.SequenceTestCase{
		{":edit ait^^^aix^^^^ airsoft.map>",
			`┌────────────────━━━━━━━━━━━━━─────┌─────────────┐
│o o.go  o bugz  o airsoft.map     │ cannot      │
├──────────────────────────────────│ reload      │
│0000000000000000000000000000000000│ this        │
├─┬────────────────────────────────│ content     │
│2│AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA└─────────────┘
│2│AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA┌─────────────┐
├─┴────────────────────────────────│ wasup: Z    │
│1111111111111111111111111111111111└─────────────┘
└────────────────────────────────────────────────┘`},
	}
	handlertest.TestHandlerSequence(t, bh, 50, 10, cases)

	assert.NoError(t, bh.(io.Closer).Close())
	assert.NoError(t, b.Close())
	assert.Equal(t, 1, closed)
}

func TestShellCommandOpensTab(t *testing.T) {
	workspaceURI, err := workspaceapi.ParseURI("file://" + t.TempDir())
	require.NoError(t, err)
	w := testWorkspaceWithURI{testLoader: &testLoader{}, uri: workspaceURI}
	cfg := vte.DefaultConfig()
	scheduler := newQueuedScheduler()
	cfg.ScheduleNextTick = scheduler.ScheduleNextTick
	b := newExForTestingWithWorkspace(t, w, texttest.NopEditor(),
		cfg, nopPublishEvent, clipboard.NewInMemory(),
		text.WithCommandKey(testCommandKey),
	)
	b.mu = &sync.Mutex{}
	b.scheduler = scheduler
	defer b.Close()

	require.NoError(t, b.comp.RegisterREPLCommand(
		textapi.CommandManual{Name: "status", Summary: "show status"},
		&testShellREPLHandler{},
	))

	cases := []handlertest.SequenceTestCase{
		{
			InputSequence: "<c-\\\\>console<enter>help<enter>",
			Expected: "┌━━━━━━━━━─────────┐\n" +
				"│ console         │\n" +
				"├──────────────────┤\n" +
				"│  available       │\n" +
				"│  commands        │\n" +
				"│• status — show   │\n" +
				"│  status          │\n" +
				"│                  │\n" +
				"│> ▐               │\n" +
				"└──────────────────┘",
		},
	}

	handlertest.RunHandlerSequence(t, b, 20, 10, cases)

	tabs := b.comp.Tabs()
	require.Len(t, tabs, 1)
	_, ok := tabs[0].Handler().(*ideshell.Handler)
	assert.True(t, ok)
	assert.Equal(t, text.DefaultConfig().Icons.Shell, b.config.Icons.Shell)
	assert.Equal(t, "console://"+workspaceURI.Path(), tabs[0].URI().String())
}

func TestShellCommandComplete(t *testing.T) {
	workspaceURI, err := workspaceapi.ParseURI("file://" + t.TempDir())
	require.NoError(t, err)
	w := testWorkspaceWithURI{testLoader: &testLoader{}, uri: workspaceURI}
	cfg := vte.DefaultConfig()
	scheduler := newQueuedScheduler()
	cfg.ScheduleNextTick = scheduler.ScheduleNextTick
	b := newExForTestingWithWorkspace(t, w, texttest.NopEditor(),
		cfg, nopPublishEvent, clipboard.NewInMemory(),
		text.WithCommandKey(testCommandKey),
	)
	b.mu = &sync.Mutex{}
	b.scheduler = scheduler
	defer b.Close()

	require.NoError(t, b.comp.RegisterREPLCommand(
		textapi.CommandManual{Name: "status", Summary: "show status"},
		&testShellREPLHandler{},
	))
	require.NoError(t, b.comp.RegisterREPLCommand(
		textapi.CommandManual{Name: "deploy", Summary: "deploy"},
		&completeStubREPLHandler{candidates: []string{"prod", "staging"}},
	))

	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "top level lists sorted command names",
			args: []string{""},
			want: []string{"deploy", "status"},
		},
		{
			name: "top level filters by prefix",
			args: []string{"de"},
			want: []string{"deploy"},
		},
		{
			name: "delegates to command handler completion",
			args: []string{"deploy", "pr"},
			want: []string{"prod", "staging"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			it, _, err := b.comp.CompleteCommand(t.Context(),
				textapi.Command{Name: "console", Args: tc.args})
			require.NoError(t, err)
			defer func() { _ = it.Close() }()

			got, err := sdkiterator.ToSlice(t.Context(), it)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestShowFallbackPromptInstallsExtension verifies that the ex command
// fallback for an unhandled command opens a Yes/No prompt and, on Yes,
// opens the companion shell submitting `pkg install rune-agent`.
func TestShowFallbackPromptInstallsExtension(t *testing.T) {
	for _, command := range []string{"agent", "?"} {
		t.Run(command, func(t *testing.T) {
			workspaceURI, err := workspaceapi.ParseURI("file:///tmp/fallback-install")
			require.NoError(t, err)
			w := testWorkspaceWithURI{
				testLoader: &testLoader{exitStartedCommands: true},
				uri:        workspaceURI,
			}
			cfg := vte.DefaultConfig()
			scheduler := newQueuedScheduler()
			cfg.ScheduleNextTick = scheduler.ScheduleNextTick
			svc := storagestub.NewInMemoryService()
			b := newExForTestingWithStorage(t, w, svc, texttest.NopEditor(),
				cfg, nopPublishEvent, clipboard.NewInMemory(),
				text.WithCommandKey(testCommandKey),
			)
			b.mu = &sync.Mutex{}
			b.scheduler = scheduler
			defer b.Close()

			b.ShowFallbackPrompt(context.Background(), command)
			assert.Equal(t, 1, b.comp.Browser().FloatingWindows())

			exit, handled := b.Handle(term.Event{Type: term.EventKey, Ch: 'y'})
			assert.False(t, exit)
			assert.True(t, handled)
			assert.Equal(t, 0, b.comp.Browser().FloatingWindows())

			var doc struct{ Items []string }
			require.NoError(t, svc.Get(
				context.Background(), shellHistoryDocumentID, &doc,
			))
			require.NotEmpty(t, doc.Items)
			assert.Equal(t, "pkg install rune-agent", doc.Items[len(doc.Items)-1])
		})
	}
}

// TestShowFallbackPromptInstallsFuzzySearch verifies that the fuzzy-search
// commands fall back to a prompt that installs the fuzzy-search extension.
func TestShowFallbackPromptInstallsFuzzySearch(t *testing.T) {
	for _, command := range []string{"searchfile", "searchtext", "searchast"} {
		t.Run(command, func(t *testing.T) {
			workspaceURI, err := workspaceapi.ParseURI("file:///tmp/fallback-fuzzy")
			require.NoError(t, err)
			w := testWorkspaceWithURI{
				testLoader: &testLoader{exitStartedCommands: true},
				uri:        workspaceURI,
			}
			cfg := vte.DefaultConfig()
			scheduler := newQueuedScheduler()
			cfg.ScheduleNextTick = scheduler.ScheduleNextTick
			svc := storagestub.NewInMemoryService()
			b := newExForTestingWithStorage(t, w, svc, texttest.NopEditor(),
				cfg, nopPublishEvent, clipboard.NewInMemory(),
				text.WithCommandKey(testCommandKey),
			)
			b.mu = &sync.Mutex{}
			b.scheduler = scheduler
			defer b.Close()

			b.ShowFallbackPrompt(context.Background(), command)
			assert.Equal(t, 1, b.comp.Browser().FloatingWindows())

			exit, handled := b.Handle(term.Event{Type: term.EventKey, Ch: 'y'})
			assert.False(t, exit)
			assert.True(t, handled)
			assert.Equal(t, 0, b.comp.Browser().FloatingWindows())

			var doc struct{ Items []string }
			require.NoError(t, svc.Get(
				context.Background(), shellHistoryDocumentID, &doc,
			))
			require.NotEmpty(t, doc.Items)
			assert.Equal(t, "pkg install fuzzy-search", doc.Items[len(doc.Items)-1])
		})
	}
}

// TestHomeFallbackPromptDoesNotInstall verifies that on the home/empty
// workspace the rune-agent and fuzzy-search command fallbacks do not offer to
// install an extension. The home workspace deliberately starts no extensions,
// so the user is told to open a workspace instead of opening an install
// prompt.
func TestHomeFallbackPromptDoesNotInstall(t *testing.T) {
	commands := []string{
		"agent", "?",
		"chateffort", "chatmaxtokens", "chatskill", "chatmodel",
		"chatclear", "chatcompact", "chatfork", "chatexport", "chatlog",
		"searchfile", "searchtext", "searchast",
	}
	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			workspaceURI, err := workspaceapi.ParseURI("file:///tmp/fallback-home")
			require.NoError(t, err)
			w := testWorkspaceWithURI{testLoader: &testLoader{}, uri: workspaceURI}
			cfg := vte.DefaultConfig()
			scheduler := newQueuedScheduler()
			cfg.ScheduleNextTick = scheduler.ScheduleNextTick
			svc := storagestub.NewInMemoryService()
			b := newExForTestingWithStorage(t, w, svc, texttest.NopEditor(),
				cfg, nopPublishEvent, clipboard.NewInMemory(),
				text.WithCommandKey(testCommandKey),
			)
			b.mu = &sync.Mutex{}
			b.scheduler = scheduler
			defer b.Close()

			b.markHome()
			b.ShowFallbackPrompt(context.Background(), command)

			assert.Equal(t, 0, b.comp.Browser().FloatingWindows(),
				"home fallback must not open an install prompt")
			assert.Nil(t, b.ex.companionConsole,
				"home fallback must not open the install console")
		})
	}
}

// TestShowFallbackPromptNoDoesNothing verifies that selecting No on the
// install prompt closes the prompt without opening the companion console.
func TestShowFallbackPromptNoDoesNothing(t *testing.T) {
	workspaceURI, err := workspaceapi.ParseURI("file:///tmp/fallback-no")
	require.NoError(t, err)
	w := testWorkspaceWithURI{testLoader: &testLoader{}, uri: workspaceURI}
	cfg := vte.DefaultConfig()
	scheduler := newQueuedScheduler()
	cfg.ScheduleNextTick = scheduler.ScheduleNextTick
	svc := storagestub.NewInMemoryService()
	b := newExForTestingWithStorage(t, w, svc, texttest.NopEditor(),
		cfg, nopPublishEvent, clipboard.NewInMemory(),
		text.WithCommandKey(testCommandKey),
	)
	b.mu = &sync.Mutex{}
	b.scheduler = scheduler
	defer b.Close()

	b.ShowFallbackPrompt(context.Background(), "agent")
	assert.Equal(t, 1, b.comp.Browser().FloatingWindows())

	exit, handled := b.Handle(term.Event{Type: term.EventKey, Ch: 'n'})
	assert.False(t, exit)
	assert.True(t, handled)
	assert.Equal(t, 0, b.comp.Browser().FloatingWindows())
	assert.Nil(t, b.ex.companionConsole)
}

// TestShellCommandPastesArgument verifies that arguments passed to the
// `console` ex command are submitted to the console prompt as a single
// command line, both when the console tab is created on first use and
// when the existing companion console tab is reused.
func TestShellCommandPastesArgument(t *testing.T) {
	workspaceURI, err := workspaceapi.ParseURI("file:///tmp/shell-paste")
	require.NoError(t, err)
	w := testWorkspaceWithURI{testLoader: &testLoader{}, uri: workspaceURI}
	cfg := vte.DefaultConfig()
	scheduler := newQueuedScheduler()
	cfg.ScheduleNextTick = scheduler.ScheduleNextTick
	svc := storagestub.NewInMemoryService()
	b := newExForTestingWithStorage(t, w, svc, texttest.NopEditor(),
		cfg, nopPublishEvent, clipboard.NewInMemory(),
		text.WithCommandKey(testCommandKey),
	)
	b.mu = &sync.Mutex{}
	b.scheduler = scheduler
	defer b.Close()

	// First call creates the companion console tab and pastes the
	// command. The repl persists submitted commands to history.
	require.NoError(t, b.consolenewtab(context.Background(), "help"))
	var doc struct{ Items []string }
	require.NoError(t, svc.Get(
		context.Background(), shellHistoryDocumentID, &doc,
	))
	require.NotEmpty(t, doc.Items)
	assert.Equal(t, "help", doc.Items[len(doc.Items)-1])

	// Second call reuses the existing tab and submits a different
	// command, which should also be persisted to history.
	require.NoError(t, b.consolenewtab(
		context.Background(), "help", "help",
	))
	require.NoError(t, svc.Get(
		context.Background(), shellHistoryDocumentID, &doc,
	))
	assert.Equal(t, "help help", doc.Items[len(doc.Items)-1])

	// Calling without arguments must not submit anything new.
	prev := len(doc.Items)
	require.NoError(t, b.consolenewtab(context.Background()))
	require.NoError(t, svc.Get(
		context.Background(), shellHistoryDocumentID, &doc,
	))
	assert.Equal(t, prev, len(doc.Items))
}

// TestDebuggerCommandOpensShellWithDebugger verifies that running
// `:debugger` with no arguments behaves like `console debugger`:
// the companion console tab is opened (or focused) and the literal
// command "debugger" is submitted on its prompt. The integration
// is wired in workspace_handler.go via
// debugshell.PromptHandler.WithOpenShell(ex.consolenewtab), so
// driving consolenewtab directly with "debugger" exercises the same
// code path that the prompt handler will invoke.
func TestDebuggerCommandOpensShellWithDebugger(t *testing.T) {
	workspaceURI, err := workspaceapi.ParseURI("file:///tmp/debugger-noargs")
	require.NoError(t, err)
	w := testWorkspaceWithURI{testLoader: &testLoader{}, uri: workspaceURI}
	cfg := vte.DefaultConfig()
	scheduler := newQueuedScheduler()
	cfg.ScheduleNextTick = scheduler.ScheduleNextTick
	svc := storagestub.NewInMemoryService()
	b := newExForTestingWithStorage(t, w, svc, texttest.NopEditor(),
		cfg, nopPublishEvent, clipboard.NewInMemory(),
		text.WithCommandKey(testCommandKey),
	)
	b.mu = &sync.Mutex{}
	b.scheduler = scheduler
	defer b.Close()

	// First call: companion console tab is created and "debugger"
	// is submitted, which the repl persists to history.
	require.NoError(t, b.consolenewtab(context.Background(), "debugger"))
	var doc struct{ Items []string }
	require.NoError(t, svc.Get(
		context.Background(), shellHistoryDocumentID, &doc,
	))
	require.NotEmpty(t, doc.Items)
	assert.Equal(t, "debugger", doc.Items[len(doc.Items)-1])

	// The console tab must be the companion ideshell handler.
	tabs := b.comp.Tabs()
	require.Len(t, tabs, 1)
	_, ok := tabs[0].Handler().(*ideshell.Handler)
	assert.True(t, ok)

	// Second call reuses the existing companion console tab and
	// re-submits "debugger" on its prompt.
	require.NoError(t, b.consolenewtab(context.Background(), "debugger"))
	require.NoError(t, svc.Get(
		context.Background(), shellHistoryDocumentID, &doc,
	))
	assert.Equal(t, "debugger", doc.Items[len(doc.Items)-1])
}

// TestShellCommandPersistsHistory verifies that commands entered into
// the IDE shell are persisted to the shared storageapi.Service via the
// shellHistoryDocumentID, and that a second ex booted on the same
// storage observes the previously persisted entries.
func TestShellCommandPersistsHistory(t *testing.T) {
	workspaceURI, err := workspaceapi.ParseURI("file://" + t.TempDir())
	require.NoError(t, err)
	w := testWorkspaceWithURI{testLoader: &testLoader{}, uri: workspaceURI}

	cfg := vte.DefaultConfig()
	scheduler := newQueuedScheduler()
	cfg.ScheduleNextTick = scheduler.ScheduleNextTick

	svc := storagestub.NewInMemoryService()

	b := newExForTestingWithStorage(t, w, svc, texttest.NopEditor(),
		cfg, nopPublishEvent, clipboard.NewInMemory(),
		text.WithCommandKey(testCommandKey),
	)
	b.mu = &sync.Mutex{}
	b.scheduler = scheduler

	// Drive `:shell help<enter>` through the handler. The exact
	// layout is verified by TestShellCommandOpensTab; here we only
	// care about the persistence side-effect on storageapi.Service.
	handlertest.RunHandlerSequence(t, b, 20, 10, []handlertest.SequenceTestCase{
		{
			InputSequence: "<c-\\\\>console<enter>help<enter>",
			Expected: "┌━━━━━━━━━─────────┐\n" +
				"│\ue691 console         │\n" +
				"├──────────────────┤\n" +
				"│> help            │\n" +
				"│• help — Show     │\n" +
				"│  available       │\n" +
				"│  commands        │\n" +
				"│                  │\n" +
				"│> ▐               │\n" +
				"└──────────────────┘",
		},
	})

	// The command typed through the shell tab should have been
	// persisted by repl.WithStorage under shellHistoryDocumentID.
	var doc struct {
		Items []string
	}
	require.NoError(t, svc.Get(
		context.Background(), shellHistoryDocumentID, &doc,
	))
	require.NotEmpty(t, doc.Items)
	assert.Equal(t, "help", doc.Items[0])

	b.Close()

	// Boot a fresh ex backed by the same storage and verify the
	// repl-backed history document is still available.
	b2 := newExForTestingWithStorage(t, w, svc, texttest.NopEditor(),
		cfg, nopPublishEvent, clipboard.NewInMemory(),
		text.WithCommandKey(testCommandKey),
	)
	b2.mu = &sync.Mutex{}
	b2.scheduler = scheduler
	defer b2.Close()

	require.NoError(t, b2.consolenewtab(context.Background()))
	tabs := b2.comp.Tabs()
	require.Len(t, tabs, 1)
	_, ok := tabs[0].Handler().(*ideshell.Handler)
	assert.True(t, ok)

	require.NoError(t, svc.Get(
		context.Background(), shellHistoryDocumentID, &doc,
	))
	assert.Contains(t, doc.Items, "help")
}

func TestShellCommandRespectsMaxHistory(t *testing.T) {
	workspaceURI, err := workspaceapi.ParseURI("file://" + t.TempDir())
	require.NoError(t, err)
	w := testWorkspaceWithURI{testLoader: &testLoader{}, uri: workspaceURI}

	cfg := vte.DefaultConfig()
	scheduler := newQueuedScheduler()
	cfg.ScheduleNextTick = scheduler.ScheduleNextTick

	svc := storagestub.NewInMemoryService()
	b := newExForTestingWithStorage(t, w, svc, texttest.NopEditor(),
		cfg, nopPublishEvent, clipboard.NewInMemory(),
		text.WithCommandKey(testCommandKey),
		text.WithShellMaxHistory(2),
	)
	b.mu = &sync.Mutex{}
	b.scheduler = scheduler
	defer b.Close()

	openShell := func() {
		b.Handle(term.Event{Type: term.EventKey, Ch: '\\', Mod: term.ModCtrl})
		for _, r := range "console" {
			b.Handle(term.Event{Type: term.EventKey, Ch: r})
		}
		b.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	}
	submitHelp := func() {
		for _, r := range "help" {
			b.Handle(term.Event{Type: term.EventKey, Ch: r})
		}
		b.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	}

	openShell()
	for range 3 {
		submitHelp()
	}

	var doc struct {
		Items []string
	}
	require.NoError(t, svc.Get(context.Background(), shellHistoryDocumentID, &doc))
	require.Len(t, doc.Items, 2)
	assert.Equal(t, []string{"help", "help"}, doc.Items)
	assert.Len(t, doc.Items, 2)
}

func assertHandled(
	t *testing.T, h *browsertest.TestHandler, startingRune rune, exit, handled bool,
) {
	// test handler increments the character that it displays next
	// upon handling a new event
	require.False(t, exit)
	require.True(t, handled)
	assert.NotEqual(t, startingRune, h.Ch)
}

func TestBrowserHandlerInterrupts(t *testing.T) {
	t.Run("Interrupt calls interrupt handle", func(t *testing.T) {
		var wg sync.WaitGroup
		opts := []text.Option{text.WithEventPublisher(
			func(ev term.Event) bool {
				assert.Equal(t, term.EventInterrupt, ev.Type)
				wg.Done()
				return true
			},
		),
			text.WithCommandOverlayConfig(testCommandOverlayConfig()),
		}
		browser := newExForTesting(t, texttest.NopEditor(), opts...)
		defer browser.Close()

		wg.Add(1)
		browser.Browser().PublishEvent(term.Event{Type: term.EventInterrupt})

		wg.Wait()
	})
	t.Run("SendEventNone calls interrupt handle", func(t *testing.T) {
		var wg sync.WaitGroup
		opts := []text.Option{text.WithEventPublisher(func(ev term.Event) bool {
			assert.Equal(t, term.EventNone, ev.Type)
			wg.Done()
			return true
		}),
			text.WithCommandOverlayConfig(testCommandOverlayConfig()),
		}
		browser := newExForTesting(t, texttest.NopEditor(), opts...)
		defer browser.Close()

		wg.Add(1)
		browser.Browser().PublishEvent(term.Event{Type: term.EventNone})

		wg.Wait()
	})
}

func TestMultipleFilesStartup(t *testing.T) {
	cases := []handlertest.SequenceTestCase{
		{"",
			`┌────────━━━━━━━───┐
│o a.go  o wi.go   │
├──────────────────┤
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
└──────────────────┘`},
		{"aa",
			`┌────────━━━━━━━───┐
│o a.go  o wi.go   │
├──────────────────┤
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
└──────────────────┘`},
		{"#", // simulates ctrl-h
			`┌━━━━━━────────────┐
│o a.go  o wi.go   │
├──────────────────┤
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
└──────────────────┘`},
		{"#:reloadfile>",
			`┌────────━━━━━━━───┐
│o a.go  o wi.go   │
├──────────────────┤
│DDDDDDDDDDDDDDDDDD│
│DDDDDDDDDDDDDDDDDD│
│DDDDDDDDDDDDDDDDDD│
│DDDDDDDDDDDDDDDDDD│
│DDDDDDDDDDDDDDDDDD│
│DDDDDDDDDDDDDDDDDD│
└──────────────────┘`},
	}

	file1, err := workspaceapi.ParseURI("file:///a.go")
	require.NoError(t, err)
	file2, err := workspaceapi.ParseURI("file:///wi.go")
	require.NoError(t, err)
	opts := []text.Option{
		text.WithCommandKey(testCommandKey),
		text.WithCommandOverlayConfig(testCommandOverlayConfig()),
	}
	mockBuf := testFileBuffer{}
	workspace := testLoader{buf: &mockBuf}
	b := newExForTestingWithWorkspace(t, &workspace, texttest.NopEditor(),
		vte.DefaultConfig(), nopPublishEvent, clipboard.NewInMemory(), opts...)
	defer b.Close()
	_, err = b.editFileURI(file1, b.invokeWindow(), false)
	require.NoError(t, err)
	_, err = b.editFileURI(file2, b.invokeWindow(), false)
	require.NoError(t, err)

	handlertest.TestHandlerSequence(t, b, 20, 10, cases)

}

func TestWriteExclamationNoQuit(t *testing.T) {
	cases := []handlertest.SequenceTestCase{
		{":w!>",
			`┌────┌─────────────┐
│    │ cannot      │
├────│ flush this  │
│    │ content     │
│    └─────────────┘
│                  │
│                  │
│                  │
│                  │
└──────────────────┘`},
	}
	opts := []text.Option{
		text.WithCommandKey(testCommandKey),
		text.WithCommandOverlayConfig(testCommandOverlayConfig()),
	}
	mockBuf := testFileBuffer{}
	workspace := testLoader{buf: &mockBuf}
	b := newExForTestingWithWorkspace(t, &workspace, texttest.NopEditor(),
		vte.DefaultConfig(), nopPublishEvent, clipboard.NewInMemory(), opts...)
	defer b.Close()

	handlertest.TestHandlerSequence(t, b, 20, 10, cases)
	assert.False(t, b.exit)
}

func TestPreviewCommands(t *testing.T) {
	t.Run("reverts a preview", func(t *testing.T) {
		opts := []text.Option{
			text.WithCommandKey(testCommandKey),
			text.WithCommandOverlayConfig(testCommandOverlayConfig()),
		}
		var called, reverted bool
		previews := map[string]previewFunc{
			"setTheme": func(string, ...string) (component.Responsive, func(), bool) {
				called = true
				return nil, func() {
					reverted = true
				}, true
			},
		}
		mockBuf := testFileBuffer{}
		workspace := testLoader{buf: &mockBuf}
		b := newExForTestingCommandsPreview(t, &workspace, texttest.NopEditor(),
			vte.DefaultConfig(), nopPublishEvent, clipboard.NewInMemory(), previews, opts...)
		defer b.Close()

		_, cancel, ok := b.Preview("setTheme", "arg1")
		require.True(t, ok)

		assert.True(t, called)
		assert.False(t, reverted)

		cancel()
		assert.True(t, called)
		assert.True(t, reverted)
	})

	t.Run("ignores previews when command is not set", func(t *testing.T) {
		opts := []text.Option{
			text.WithCommandKey(testCommandKey),
			text.WithCommandOverlayConfig(testCommandOverlayConfig()),
		}
		var called, reverted bool
		previews := map[string]previewFunc{
			"setTheme": func(string, ...string) (component.Responsive, func(), bool) {
				called = true
				return nil, func() {
					reverted = true
				}, true
			},
		}
		mockBuf := testFileBuffer{}
		workspace := testLoader{buf: &mockBuf}
		b := newExForTestingCommandsPreview(t, &workspace, texttest.NopEditor(),
			vte.DefaultConfig(), nopPublishEvent, clipboard.NewInMemory(), previews, opts...)
		defer b.Close()

		_, _, ok := b.Preview("guiSetTheme", "arg1")
		require.False(t, ok)

		assert.False(t, called)
		assert.False(t, reverted)
	})

	t.Run("ignores previews when previews are disabled", func(t *testing.T) {
		opts := []text.Option{
			text.WithCommandKey(testCommandKey),
			text.WithCommandOverlayConfig(testCommandOverlayConfig()),
		}
		var called, reverted bool
		mockBuf := testFileBuffer{}
		workspace := testLoader{buf: &mockBuf}
		b := newExForTestingCommandsPreview(t, &workspace, texttest.NopEditor(),
			vte.DefaultConfig(), nopPublishEvent, clipboard.NewInMemory(),
			nil /*previews*/, opts...)
		defer b.Close()

		_, _, ok := b.Preview("guiSetTheme", "arg1")
		require.False(t, ok)

		assert.False(t, called)
		assert.False(t, reverted)
	})
}

func TestBrowserCloseLastWindow(t *testing.T) {
	cases := []handlertest.SequenceTestCase{
		// closing the last tiled window is a no-op so the command is idempotent.
		{":windowclose>",
			`┌──────────────────┐
│                  │
├──────────────────┤
│                  │
│                  │
│                  │
│                  │
│                  │
│                  │
└──────────────────┘`},
	}
	opts := []text.Option{
		text.WithCommandKey(testCommandKey),
		text.WithCommandOverlayConfig(testCommandOverlayConfig()),
	}
	mockBuf := testFileBuffer{}
	workspace := testLoader{buf: &mockBuf}
	b := newExForTestingWithWorkspace(t, &workspace, texttest.NopEditor(),
		vte.DefaultConfig(), nopPublishEvent, clipboard.NewInMemory(), opts...)
	defer b.Close()

	handlertest.TestHandlerSequence(t, b, 20, 10, cases)
	assert.False(t, b.exit)
}

func TestExCommandResponsive(t *testing.T) {
	cases := []handlertest.SequenceTestCase{
		{":edit",
			`                    
                    
                    
                    
 edit▐              
 edit               
                    
                    
                    
                    `},
		{":eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
			`                    
                    
                    
                    
 eeeeeeeeeeeeeee    
 eeeeeeeeeeeeeee    
 eeeeeeeeeeeeee▐    
                    
                    
                    `},
		{":edit eeeeeeeeeeeeeeeeeeeeeeeee",
			`                    
                    
                    
                    
 edit eeeeeeeeee    
 eeeeeeeeeeeeee▐    
                    
                    
                    
                    `},
	}

	var closeFns []func() error
	fn := func(t *testing.T) tui.Handler {
		opts := []text.Option{
			text.WithCommandKey(testCommandKey),
			text.WithWindowManagerConfig(thandler.WindowManagerConfig{
				WindowManagerConfig: tcomponent.WindowManagerConfig{Frame: false}}),
			text.WithCommandOverlayConfig(testCommandOverlayConfig()),
		}
		b := newExForTesting(t, texttest.NopEditor(), opts...)
		closeFns = append(closeFns, b.Close)
		return b
	}
	handlertest.TestHandlerIsolated(t, fn, 20, 10, cases)
	for _, close := range closeFns {
		close()
	}
}

func TestExKeySequence(t *testing.T) {
	cases := []handlertest.SequenceTestCase{
		{"zgl",
			`┌━━━━━━━━──────────┐
│o 10k.go  o 2     │
├──────────────────┤
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
└──────────────────┘`},
		{"g",
			`┌──────────━━━─────┐
│o 10k.go  o 2     │
├──────────────────┤
│BBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBB│
└──────────────────┘`},
		{"go",
			`┌──────────━━━─────┐
│o 10k.go  o 2     │
├──────────────────┤
│DDDDDDDDDDDDDDDDDD│
│DDDDDDDDDDDDDDDDDD│
│DDDDDDDDDDDDDDDDDD│
│DDDDDDDDDDDDDDDDDD│
│DDDDDDDDDDDDDDDDDD│
│DDDDDDDDDDDDDDDDDD│
└──────────────────┘`},
		// 2 seconds of wait should be plenty for sequencer to deem 'g' sequence
		// stale and re-issue event.
		{"g____________________",
			`┌──────────━━━─────┐
│o 10k.go  o 2     │
├──────────────────┤
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
└──────────────────┘`},
		{"gg",
			`┌──────────────────┐
│                  │
├──────────────────┤
│                  │
│                  │
│                  │
│                  │
│                  │
│                  │
└──────────────────┘`},
	}

	var closeFns []func() error
	fn := func(t *testing.T) tui.Handler {
		var mu sync.Mutex
		file1, err := workspaceapi.ParseURI("file:///10k.go")
		require.NoError(t, err)
		file2, err := workspaceapi.ParseURI("file:///2")
		require.NoError(t, err)
		opts := []text.Option{
			text.WithCommandKey(testCommandKey),
			text.WithCommandSequenceBinding(thandler.Sequence{
				First: term.KeyComb{Ch: 'g'},
				Last:  term.KeyComb{Ch: 'l'},
			}, [][]string{{"tabnext"}}),
			text.WithCommandSequenceBinding(thandler.Sequence{
				First: term.KeyComb{Ch: 'g'},
				Last:  term.KeyComb{Ch: 'g'},
			}, [][]string{{"tabcloseall"}}),
			text.WithSequencerTimeout(1 * time.Second),
			text.WithCommandOverlayConfig(testCommandOverlayConfig()),
		}

		ex := new(ex)
		notifications := newWorkspaceNotifications(
			storagestub.NewInMemoryService(), notificationsConfig(),
			&workspaceManagerMock{workspace: ex})
		ex.syncCommandPrompt = true
		require.NoError(t, ex.init(func(exoeditor.Reloader, schemeapi.Terminal) (text.Editor, error) { return texttest.NopEditor(), nil }, &testLoader{},
			storagestub.NewInMemoryService(), notifications, file2,
			vte.DefaultConfig(), plugin.DefaultBarConfig(), func(ev term.Event) bool {
				// do not confuse interrupt from list with sequence re-issue commands
				if ev.Type == term.EventInterrupt {
					return true
				}
				mu.Lock()
				defer mu.Unlock()
				ex.Handle(ev)
				return true
			}, 0, clipboard.NewInMemory(), nil, nil, nil, nil, testPromptEditor(), opts...))
		ex.subscribeCommands()
		b := testEx{ex: ex}
		closeFns = append(closeFns, func() error {
			mu.Lock()
			defer mu.Unlock()
			return b.Close()
		})
		_, err = b.editFileURI(file1, ex.invokeWindow(), false)
		require.NoError(t, err)
		_, err = b.editFileURI(file2, ex.invokeWindow(), false)
		require.NoError(t, err)
		return handler.Sync(&mu, b)
	}
	handlertest.TestHandlerIsolated(t, fn, 20, 10, cases)
	for _, close := range closeFns {
		close()
	}
}

// seqStubEditor wraps texttest.TestEditor so that the underlying editor
// consumes exactly the key combinations in consume. Every event the
// editor sees is appended to seen. This lets a sequencer test decide,
// per key, whether the editor claims a keystroke, mirroring the way a
// real modal editor swallows some keys but not others.
type seqStubEditor struct {
	*texttest.TestEditor
	consume map[term.KeyComb]struct{}
	recMu   *sync.Mutex
	seen    *[]term.KeyComb
}

func (e seqStubEditor) Edit(
	ctx context.Context,
	resource workspaceapi.URI, buf *cell.Buffer, readOnly, recovered bool,
) (text.Handler, error) {
	h, err := e.TestEditor.Edit(ctx, resource, buf, readOnly, recovered)
	if err != nil {
		return nil, err
	}
	eh := h.(*texttest.TestEditorHandler)
	eh.HandleOverride = func(ev term.Event) (bool, bool) {
		e.recMu.Lock()
		*e.seen = append(*e.seen, ev.KeyComb())
		e.recMu.Unlock()
		_, consumed := e.consume[ev.KeyComb()]
		return false, consumed
	}
	return eh, nil
}

// exSequencerHarness drives a real *ex through hand-built term.Events so
// that modifier-bearing keys (e.g. <ctrl-x>) can be exercised — the
// handlertest InputSequence tokenizer cannot express them.
type exSequencerHarness struct {
	ex        testEx
	fired     *[]string
	editorSaw *[]term.KeyComb
	recMu     *sync.Mutex
}

func (h exSequencerHarness) firedCommands() []string {
	h.recMu.Lock()
	defer h.recMu.Unlock()
	out := make([]string, len(*h.fired))
	copy(out, *h.fired)
	return out
}

func (h exSequencerHarness) editorConsumed() []term.KeyComb {
	h.recMu.Lock()
	defer h.recMu.Unlock()
	out := make([]term.KeyComb, len(*h.editorSaw))
	copy(out, *h.editorSaw)
	return out
}

// newExSequencerHarness builds an *ex whose sequencer knows sequences,
// whose command layer knows single-key bindings, and whose editor
// consumes the given key combinations. Commands referenced by the
// bindings are registered as sinks that record their name in fired.
func newExSequencerHarness(
	t *testing.T,
	sequences map[thandler.Sequence][][]string,
	keyBindings map[term.KeyComb][][]string,
	editorConsumes []term.KeyComb,
	timeout time.Duration,
) exSequencerHarness {
	t.Helper()
	// exMu serializes ex.Handle calls (the test goroutine plus the
	// re-issue timer goroutine). recMu independently guards the fired
	// and seen recorders, which are touched from inside ex.Handle (while
	// exMu is held) and read from the test goroutine — using exMu for
	// both would deadlock because ex.Handle synchronously invokes the
	// editor and command sinks.
	var mu sync.Mutex
	var recMu sync.Mutex
	fired := &[]string{}
	editorSaw := &[]term.KeyComb{}

	consume := make(map[term.KeyComb]struct{}, len(editorConsumes))
	for _, k := range editorConsumes {
		consume[k] = struct{}{}
	}

	opts := []text.Option{
		text.WithCommandKey(testCommandKey),
		text.WithSequencerTimeout(timeout),
		text.WithCommandOverlayConfig(testCommandOverlayConfig()),
	}
	for seq, cmds := range sequences {
		opts = append(opts, text.WithCommandSequenceBinding(seq, cmds))
	}
	for key, cmds := range keyBindings {
		opts = append(opts, text.WithCommandKeyBinding(key, cmds))
	}

	file, err := workspaceapi.ParseURI("file:///seq.go")
	require.NoError(t, err)

	ex := new(ex)
	ex.syncCommandPrompt = true
	svc := storagestub.NewInMemoryService()
	notifications := newWorkspaceNotifications(svc, notificationsConfig(),
		&workspaceManagerMock{workspace: ex})
	editor := seqStubEditor{
		TestEditor: texttest.NopEditor(),
		consume:    consume,
		recMu:      &recMu,
		seen:       editorSaw,
	}
	require.NoError(t, ex.init(
		func(exoeditor.Reloader, schemeapi.Terminal) (text.Editor, error) { return editor, nil },
		&testLoader{}, svc, notifications, file,
		vte.DefaultConfig(), plugin.DefaultBarConfig(),
		func(ev term.Event) bool {
			if ev.Type == term.EventInterrupt {
				return true
			}
			mu.Lock()
			defer mu.Unlock()
			ex.Handle(ev)
			return true
		}, 0, clipboard.NewInMemory(), nil, nil, nil, nil,
		testPromptEditor(), opts...))
	ex.subscribeCommands()

	// Register a recording sink for every command named by a binding so
	// that runCommand -> dispatchCommand resolves and appends its name.
	registered := map[string]struct{}{}
	register := func(name string) {
		if name == "" {
			return
		}
		if _, ok := registered[name]; ok {
			return
		}
		registered[name] = struct{}{}
		require.NoError(t, ex.comp.SubscribeCommand(
			textapi.CommandManual{Name: name},
			text.FuncCommandHandler(func(_ context.Context, _ textapi.Command) error {
				recMu.Lock()
				*fired = append(*fired, name)
				recMu.Unlock()
				return nil
			}, nil)))
	}
	for _, cmds := range sequences {
		for _, cmd := range cmds {
			register(cmd[0])
		}
	}
	for _, cmds := range keyBindings {
		for _, cmd := range cmds {
			register(cmd[0])
		}
	}

	b := testEx{ex: ex, mu: &mu}
	_, err = b.editFileURI(file, ex.invokeWindow(), false)
	require.NoError(t, err)
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		b.Close()
	})
	return exSequencerHarness{ex: b, fired: fired, editorSaw: editorSaw, recMu: &recMu}
}

// TestExSequencerModifierVsBarePrefix drives the ex event pipeline with
// hand-built events to cover the full matrix of first-key/second-key
// combinations a sequence prefix can encounter. A bare-character prefix
// (e.g. vi's `d`) remains subject to the re-issue timeout because it can
// also be typed as literal input; a modifier-bearing prefix (e.g.
// <ctrl-x>) waits indefinitely for its second key and is dropped, never
// re-issued, when the second key does not complete a sequence.
func TestExSequencerModifierVsBarePrefix(t *testing.T) {
	const timeout = 20 * time.Millisecond
	// well past timeout+reissuePadding so a stale bare prefix would
	// have re-issued by the time the second key is sent.
	const longGap = 80 * time.Millisecond

	ctrlX := term.KeyComb{Ch: 'x', Mod: term.ModCtrl}
	ctrlS := term.KeyComb{Ch: 's', Mod: term.ModCtrl}
	metaX := term.KeyComb{Ch: 'x', Mod: term.ModMeta}
	metaJ := term.KeyComb{Ch: 'j', Mod: term.ModMeta}

	sequences := map[thandler.Sequence][][]string{
		{First: term.KeyComb{Ch: 'd'}, Last: term.KeyComb{Ch: 'd'}}: {{"seqdd"}},
		{First: ctrlX, Last: ctrlS}:                                 {{"seqctrls"}},
		{First: metaX, Last: metaJ}:                                 {{"seqmetaj"}},
	}
	keyBindings := map[term.KeyComb][][]string{
		{Ch: 'd'}:                    {{"keyd"}},
		{Ch: 'z', Mod: term.ModCtrl}: {{"keyctrlz"}},
	}

	cases := []struct {
		name           string
		editorConsumes []term.KeyComb
		keys           []term.KeyComb
		gapBefore2nd   time.Duration
		wantFired      []string
		wantConsumed   []term.KeyComb
	}{
		{
			name:      "bare prefix completes sequence",
			keys:      []term.KeyComb{{Ch: 'd'}, {Ch: 'd'}},
			wantFired: []string{"seqdd"},
		},
		{
			name:         "bare prefix times out and re-issues standalone binding",
			keys:         []term.KeyComb{{Ch: 'd'}},
			gapBefore2nd: 0,
			wantFired:    []string{"keyd"},
		},
		{
			name:         "modifier prefix completes sequence after long gap",
			keys:         []term.KeyComb{ctrlX, ctrlS},
			gapBefore2nd: longGap,
			wantFired:    []string{"seqctrls"},
		},
		{
			name:      "modifier prefix completes sequence quickly",
			keys:      []term.KeyComb{metaX, metaJ},
			wantFired: []string{"seqmetaj"},
		},
		{
			name:         "modifier prefix dropped when second key has its own binding",
			keys:         []term.KeyComb{ctrlX, {Ch: 'z', Mod: term.ModCtrl}},
			gapBefore2nd: longGap,
			wantFired:    []string{"keyctrlz"},
		},
		{
			name:         "modifier prefix dropped when nothing binds or consumes second key",
			keys:         []term.KeyComb{ctrlX, {Ch: 'q'}},
			gapBefore2nd: longGap,
			wantFired:    nil,
			wantConsumed: []term.KeyComb{ctrlX, {Ch: 'q'}},
		},
		{
			name:           "modifier prefix dropped when editor consumes second key",
			editorConsumes: []term.KeyComb{{Ch: 'k'}},
			keys:           []term.KeyComb{ctrlX, {Ch: 'k'}},
			gapBefore2nd:   longGap,
			wantFired:      nil,
			wantConsumed:   []term.KeyComb{ctrlX, {Ch: 'k'}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newExSequencerHarness(t, sequences, keyBindings, tc.editorConsumes, timeout)
			for i, k := range tc.keys {
				if i > 0 && tc.gapBefore2nd > 0 {
					time.Sleep(tc.gapBefore2nd)
				}
				h.ex.Handle(term.Event{
					Type: term.EventKey, Mod: k.Mod, Key: k.Key, Ch: k.Ch,
				})
			}
			// Give any pending re-issue timer time to fire so bare-prefix
			// timeouts settle before assertions.
			assert.Eventually(t, func() bool {
				return len(h.firedCommands()) >= len(tc.wantFired)
			}, time.Second, 2*time.Millisecond,
				"expected %v commands, got %v", tc.wantFired, h.firedCommands())
			// Allow a late erroneous re-issue to surface before asserting
			// exact equality (guards against a modifier prefix re-firing).
			time.Sleep(timeout + reissuePadding + 20*time.Millisecond)
			assert.Equal(t, tc.wantFired, nonEmpty(h.firedCommands()))
			if tc.wantConsumed != nil {
				assert.Equal(t, tc.wantConsumed, h.editorConsumed())
			}
		})
	}
}

func TestExStandardNavigationPrecedesLayoutBindings(t *testing.T) {
	metaLeft := term.KeyComb{Key: term.KeyArrowLeft, Mod: term.ModMeta}
	shiftMetaRight := term.KeyComb{Key: term.KeyArrowRight, Mod: term.ModShiftMeta}
	layoutUp := term.KeyComb{Key: term.KeyArrowUp, Mod: term.ModCtrlMeta}

	h := newExSequencerHarness(t, nil,
		map[term.KeyComb][][]string{
			metaLeft:       {{"conflictingfocus"}},
			shiftMetaRight: {{"conflictingmove"}},
			layoutUp:       {{"layoutfocus"}},
		}, nil, 20*time.Millisecond)

	resource, err := workspaceapi.ParseURI("file:///standard-navigation.go")
	require.NoError(t, err)
	buf := new(cell.Buffer)
	buf.Init()
	buf.WriteString("  first line\nlast line")
	ed := standard.NewHandler(buf, resource, text.IndentRuneTab, 0)
	ed.Resize(40, 10)
	require.True(t, ed.SetCursorAtScroll(term.Coordinates{X: 7}))
	require.NoError(t, h.ex.invokeWindow().SetContent(ed))

	_, handled := h.ex.Handle(term.Event{
		Type: term.EventKey, Key: metaLeft.Key, Mod: metaLeft.Mod,
	})
	require.True(t, handled)
	require.Equal(t, term.Coordinates{}, ed.CursorAtScroll())
	require.Empty(t, h.firedCommands())

	_, handled = h.ex.Handle(term.Event{
		Type: term.EventKey, Key: shiftMetaRight.Key, Mod: shiftMetaRight.Mod,
	})
	require.True(t, handled)
	selection, ok := ed.Selection()
	require.True(t, ok)
	require.Equal(t, "  first line", selection)
	require.Empty(t, h.firedCommands())

	_, _ = h.ex.Handle(term.Event{
		Type: term.EventKey, Key: layoutUp.Key, Mod: layoutUp.Mod,
	})
	require.Equal(t, []string{"layoutfocus"}, h.firedCommands())
}

func TestExStandardAltLayoutBindingsReachCommandLayer(t *testing.T) {
	bindings := []struct {
		key     string
		command string
	}{
		{"<alt-i>", "focusup"},
		{"<alt-j>", "focusleft"},
		{"<alt-k>", "focusdown"},
		{"<alt-l>", "focusright"},
		{"<alt-shift-i>", "moveup"},
		{"<alt-shift-j>", "moveleft"},
		{"<alt-shift-k>", "movedown"},
		{"<alt-shift-l>", "moveright"},
		{"<alt-meta-i>", "resizeup"},
		{"<alt-meta-j>", "resizeleft"},
		{"<alt-meta-k>", "resizedown"},
		{"<alt-meta-l>", "resizeright"},
		{"<alt-n>", "createwindow"},
		{"<alt-enter>", "createterminal"},
		{"<alt-q>", "closewindow"},
		{"<alt-m>", "togglemaximize"},
		{"<alt-shift-enter>", "converttab"},
		{"<alt-t>", "createtab"},
		{"<alt-w>", "closetab"},
		{"<alt-[>", "previoustab"},
		{"<alt-]>", "nexttab"},
		{"<alt-shift-[>", "tabmoveleft"},
		{"<alt-shift-]>", "tabmoveright"},
		{"<alt-h>", "splithorizontal"},
		{"<alt-v>", "splitvertical"},
		{"<ctrl-alt-h>", "hover"},
		{"<ctrl-shift-alt-h>", "hoverbyname"},
		{"<ctrl-alt-i>", "implementation"},
		{"<ctrl-shift-alt-i>", "implementationbyname"},
		{"<ctrl-alt-v>", "variablepicker"},
		{"<ctrl-meta-i>", "gitprevious"},
		{"<ctrl-meta-k>", "gitnext"},
		{"<ctrl-meta-j>", "diagnosticprevious"},
		{"<ctrl-meta-l>", "diagnosticnext"},
	}

	keyBindings := make(map[term.KeyComb][][]string, len(bindings))
	ordered := make([]struct {
		key     term.KeyComb
		command string
	}, 0, len(bindings))
	for _, binding := range bindings {
		parsed, err := term.ParseKeys(binding.key)
		require.NoError(t, err)
		require.Len(t, parsed, 1)
		keyBindings[parsed[0]] = [][]string{{binding.command}}
		ordered = append(ordered, struct {
			key     term.KeyComb
			command string
		}{key: parsed[0], command: binding.command})
	}

	h := newExSequencerHarness(t, nil, keyBindings, nil, 20*time.Millisecond)
	resource, err := workspaceapi.ParseURI("file:///standard-alt-layout.go")
	require.NoError(t, err)
	buf := new(cell.Buffer)
	buf.Init()
	buf.WriteString("first line\nsecond line")
	ed := standard.NewHandler(buf, resource, text.IndentRuneTab, 0)
	ed.Resize(40, 10)
	require.NoError(t, h.ex.invokeWindow().SetContent(ed))

	for _, binding := range ordered {
		_, _ = h.ex.Handle(term.Event{
			Type: term.EventKey,
			Key:  binding.key.Key,
			Mod:  binding.key.Mod,
			Ch:   binding.key.Ch,
		})
	}

	want := make([]string, len(ordered))
	for i, binding := range ordered {
		want[i] = binding.command
	}
	require.Equal(t, want, h.firedCommands())
}

func TestExEmacsMetaLayoutBindingsReachCommandLayer(t *testing.T) {
	bindings := []struct {
		key     string
		command string
	}{
		{"<meta-p>", "focusup"},
		{"<meta-b>", "focusleft"},
		{"<meta-n>", "focusdown"},
		{"<meta-f>", "focusright"},
		{"<shift-meta-p>", "moveup"},
		{"<shift-meta-b>", "moveleft"},
		{"<shift-meta-n>", "movedown"},
		{"<shift-meta-f>", "moveright"},
		{"<meta-up>", "resizeup"},
		{"<meta-left>", "resizeleft"},
		{"<meta-down>", "resizedown"},
		{"<meta-right>", "resizeright"},
		{"<meta-d>", "splitbelow"},
		{"<meta-r>", "splitright"},
		{"<shift-meta-w>", "closewindow"},
		{"<meta-k>", "closeotherwindows"},
		{"<meta-e>", "togglemaximize"},
		{"<meta-w>", "closetab"},
		{"<ctrl-tab>", "nexttab"},
		{"<ctrl-shift-tab>", "previoustab"},
		{"<alt-,>", "historyback"},
		{"<ctrl-alt-,>", "historyforward"},
		{"<meta-j>", "historypicker"},
		{"<alt-.>", "definition"},
		{"<alt-shift-/>", "references"},
		{"<ctrl-alt-.>", "definitionbyname"},
		{"<ctrl-shift-alt-/>", "referencesbyname"},
		{"<shift-meta-d>", "explorer"},
		{"<shift-meta-t>", "tabpicker"},
		{"<ctrl-x>j", "symbolpicker"},
		{"<alt-s>o", "searchtext"},
		{"<meta-g>", "nextsearch"},
		{"<shift-meta-g>", "prevsearch"},
		{"<meta-i>", "implementation"},
		{"<shift-meta-i>", "implementationbyname"},
		{"<shift-meta-h>", "hoverbyname"},
		{"<ctrl-x>?", "hover"},
		{"<ctrl-alt-\\\\>", "format"},
		{"<f9>", "diagnostics"},
		{"<ctrl-alt-i>", "completealias"},
	}

	sequences := map[thandler.Sequence][][]string{}
	keyBindings := map[term.KeyComb][][]string{}
	ordered := make([]struct {
		keys    []term.KeyComb
		command string
	}, 0, len(bindings))
	for _, binding := range bindings {
		parsed, err := term.ParseKeys(binding.key)
		require.NoError(t, err)
		switch len(parsed) {
		case 1:
			keyBindings[parsed[0]] = [][]string{{binding.command}}
		case 2:
			sequences[thandler.Sequence{First: parsed[0], Last: parsed[1]}] =
				[][]string{{binding.command}}
		default:
			require.Failf(t, "unsupported test sequence", "%s parsed to %d keys",
				binding.key, len(parsed))
		}
		ordered = append(ordered, struct {
			keys    []term.KeyComb
			command string
		}{keys: parsed, command: binding.command})
	}

	h := newExSequencerHarness(t, sequences, keyBindings, nil, 20*time.Millisecond)
	resource, err := workspaceapi.ParseURI("file:///emacs-meta-layout.go")
	require.NoError(t, err)
	buf := new(cell.Buffer)
	buf.Init()
	buf.WriteString("first line\nsecond line")
	ed := emacs.NewHandler(buf, resource, text.IndentRuneTab, 0)
	ed.Resize(40, 10)
	require.NoError(t, h.ex.invokeWindow().SetContent(ed))

	for _, binding := range ordered {
		for _, key := range binding.keys {
			_, _ = h.ex.Handle(term.Event{
				Type: term.EventKey,
				Key:  key.Key,
				Mod:  key.Mod,
				Ch:   key.Ch,
			})
		}
	}

	want := make([]string, len(ordered))
	for i, binding := range ordered {
		want[i] = binding.command
	}
	require.Equal(t, want, h.firedCommands())
}

func TestExEmacsLifecycleBindingsReachCommandLayerFromTerminal(t *testing.T) {
	runeStar := readRuneStar(t)
	base, err := decodeDefaultConfig(DefaultConfig{
		src: string(runeStar), modal: true, tui: false,
	})
	require.NoError(t, err)
	overlay, err := os.ReadFile("../../cmd/rune/preset_emacs.yaml")
	require.NoError(t, err)
	cfg, err := decodeOverlayConfigFile(
		bytes.NewReader(overlay), "preset_emacs.yaml", base)
	require.NoError(t, err)
	mappings := (&ideConfig{cfg: cfg, errors: map[string]error{}}).commandKeyMappings()

	wantBindings := map[string]string{
		"<meta-up>":      "windowresize increase height",
		"<meta-left>":    "windowresize decrease width",
		"<meta-down>":    "windowresize decrease height",
		"<meta-right>":   "windowresize increase width",
		"<meta-d>":       "windownew down",
		"<meta-r>":       "windownew right",
		"<meta-k>":       "windowclose",
		"<shift-meta-k>": "windowcloseall",
		"<meta-m>":       "windowtogglemaximize",
		"<meta-o>":       "fexplorer",
	}
	for key, command := range wantBindings {
		seq := mustParseBindingKey(t, key)
		require.Equalf(t, [][]string{strings.Split(command, " ")}, mappings[seq],
			"%s must run %q", key, command)
	}
	// The GNU C-x lifecycle chords are optional duplicates: a focused terminal
	// eats them, so each one must mirror a <meta> binding above.
	for cx, meta := range map[string]string{
		"<ctrl-x>0": "<meta-k>",
		"<ctrl-x>1": "<shift-meta-k>",
		"<ctrl-x>2": "<meta-d>",
		"<ctrl-x>3": "<meta-r>",
	} {
		got, ok := mappings[mustParseBindingKey(t, cx)]
		if !ok {
			continue
		}
		require.Equalf(t, mappings[mustParseBindingKey(t, meta)], got,
			"%s must duplicate %s, not diverge from it", cx, meta)
	}
	for _, key := range []string{
		"<ctrl-x>9", "<ctrl-x>d", "<ctrl-x>b", "<ctrl-x>j", "<ctrl-x>?",
	} {
		_, ok := mappings[mustParseBindingKey(t, key)]
		require.Falsef(t, ok, "%s must not remain as a terminal-inaccessible alias", key)
	}

	keyBindings := make(map[term.KeyComb][][]string, len(wantBindings))
	ordered := make([]term.KeyComb, 0, len(wantBindings))
	for key := range wantBindings {
		seq := mustParseBindingKey(t, key)
		ordered = append(ordered, seq.First)
	}
	slices.SortFunc(ordered, func(a, b term.KeyComb) int {
		return strings.Compare(a.String(), b.String())
	})
	for i, key := range ordered {
		keyBindings[key] = [][]string{{fmt.Sprintf("layoutcommand%d", i)}}
	}
	h := newExSequencerHarness(t, nil, keyBindings, nil, 20*time.Millisecond)
	for i, key := range ordered {
		got, ok := h.ex.comp.CommandKeyBinding(key)
		require.Truef(t, ok, "%s must be installed in the command layer", key.String())
		require.Equal(t, [][]string{{fmt.Sprintf("layoutcommand%d", i)}}, got)
	}
	terminalRoot, err := workspaceapi.ParseURI("file://" + t.TempDir())
	require.NoError(t, err)
	fileScheme, err := workspace.NewFileScheme(
		context.Background(), config.NopConfig(), terminalRoot)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, fileScheme.Close()) })
	vteCfg := vte.DefaultConfig()
	// keep OnTabExit on the test goroutine, not the vte run goroutine
	installDefaultTestScheduler(&vteCfg)
	vteCfg.Modal = false
	vteCfg.CommandAndArgs = []string{"sh", "-c", "sleep 30"}
	vteHandler, err := vte.NewHandler(h.ex.Browser(), h.ex.Browser(),
		fileScheme, fileScheme, h.ex.tm, vteCfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, vteHandler.Close()) })
	require.NoError(t, h.ex.invokeWindow().SetContent(vteHandler))

	ctrlX := term.KeyComb{Ch: 'x', Mod: term.ModCtrl}
	_, handled := h.ex.Handle(term.Event{
		Type: term.EventKey, Ch: ctrlX.Ch, Mod: ctrlX.Mod, Raw: []byte{0x18},
	})
	require.True(t, handled, "the terminal must retain Ctrl-X as PTY input")
	require.Empty(t, h.firedCommands())

	for i, key := range ordered {
		_, _ = h.ex.Handle(term.Event{
			Type: term.EventKey, Key: key.Key, Mod: key.Mod, Ch: key.Ch,
		})
		require.Lenf(t, h.firedCommands(), i+1,
			"%s must dispatch through Rune from a terminal", key.String())
	}
	wantFired := make([]string, len(ordered))
	for i := range ordered {
		wantFired[i] = fmt.Sprintf("layoutcommand%d", i)
	}
	require.Equal(t, wantFired, h.firedCommands())
}

func nonEmpty(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}

func TestExEmacsCtrlXUUndoIntegration(t *testing.T) {
	ctrlX := term.KeyComb{Mod: term.ModCtrl, Ch: 'x'}
	sequence := thandler.Sequence{First: ctrlX, Last: term.KeyComb{Ch: 'u'}}
	opts := []text.Option{text.WithCommandSequenceBinding(
		sequence, [][]string{{emacs.CommandUndo, "prefix"}})}
	cwd, err := workspaceapi.ParseURI("file:///")
	require.NoError(t, err)
	registry := new(editorWorkspaceRegistry)
	e := newExForTesting(t, emacs.Editor(
		emacs.WithWorkspaceCommandRegistry(cwd, registry)), opts...)
	registry.editor = e.Editor()
	t.Cleanup(func() { require.NoError(t, e.Close()) })

	uri, err := workspaceapi.ParseURI("file:///ctrl-x-u.txt")
	require.NoError(t, err)
	_, err = e.editFileURI(uri, e.invokeWindow(), false)
	require.NoError(t, err)
	require.Contains(t, e.config.CommandSequenceBindings, sequence)
	waitCtx, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	require.NoError(t, e.editorObserver.Wait(waitCtx, emacs.CommandUndo))
	h, err := e.Editor().Editor(uri)
	require.NoError(t, err)

	_, handled := e.Handle(term.Event{Type: term.EventKey, Ch: 'X'})
	require.True(t, handled)
	require.Equal(t, "X", h.CellView().String())

	_, handled = e.Handle(term.Event{Type: term.EventKey, Mod: ctrlX.Mod, Ch: ctrlX.Ch})
	assert.False(t, handled)
	require.NotNil(t, e.cancelPartialReissue)
	_, _ = e.Handle(term.Event{Type: term.EventKey, Ch: 'u'})
	assert.Equal(t, "", h.CellView().String())
}

func TestExTabIntegration(t *testing.T) {
	cases := []handlertest.SequenceTestCase{
		{"",
			`┌━━━━━━━━━─────────┐
│x Fieshta  x Pah  │
├──────────────────┤
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
└──────────────────┘`},
		{":tabcloseall>",
			`┌──────────────────┐
│                  │
├──────────────────┤
│                  │
│                  │
│                  │
│                  │
│                  │
│                  │
└──────────────────┘`},
	}

	var closeFns []func() error
	fn := func(t *testing.T) tui.Handler {
		opts := []text.Option{
			text.WithCommandKey(testCommandKey),
			text.WithCommandOverlayConfig(testCommandOverlayConfig()),
		}
		b := newExForTesting(t, texttest.NopEditor(), opts...)
		uri1, err := workspaceapi.ParseURI("file:///Fieshta")
		require.NoError(t, err)
		uri2, err := workspaceapi.ParseURI("file:///Pahty")
		require.NoError(t, err)
		tab, err := b.comp.Tab(uri1, 'x', "Fieshta", browsertest.NewTestHandler())
		require.NoError(t, err)
		_, err = b.comp.Tab(uri2, 'x', "Pahty", browsertest.NewTestHandler())
		require.NoError(t, err)
		focus, err := b.comp.Focus()
		require.NoError(t, err)
		require.NoError(t, focus.SetContent(tab))
		closeFns = append(closeFns, b.Close)
		return b
	}
	handlertest.TestHandlerIsolated(t, fn, 20, 10, cases)
	for _, close := range closeFns {
		close()
	}
}

func TestExTabclosePromptsForDirtyTab(t *testing.T) {
	b := newExForTesting(t, texttest.NopEditor(),
		text.WithCommandKey(testCommandKey),
		text.WithCommandOverlayConfig(testCommandOverlayConfig()),
	)
	defer b.Close()

	uri, err := workspaceapi.ParseURI("file:///dirty.go")
	require.NoError(t, err)
	_, err = b.editFileURI(uri, b.invokeWindow(), false)
	require.NoError(t, err)
	editBuffer(t, b.ex, uri, "ABC")

	require.NoError(t, b.tabclose(context.Background()))

	assert.Len(t, b.comp.Tabs(), 1)
	dirty, ok := b.comp.IsDirty(uri)
	require.True(t, ok)
	assert.True(t, dirty)
	assert.Equal(t, 1, b.comp.Browser().FloatingWindows())

	exit, handled := b.Handle(term.Event{Type: term.EventKey, Ch: 'y'})
	assert.False(t, exit)
	assert.True(t, handled)
	assert.Empty(t, b.comp.Tabs())
	assert.Equal(t, 0, b.comp.Browser().FloatingWindows())
}

func TestExTabcloseClosesStandardSearchWindow(t *testing.T) {
	rootURI, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)
	scheme, err := workspace.NewMemoryScheme(context.Background(), config.NopConfig(), rootURI)
	require.NoError(t, err)
	touchTestFile(t, scheme, "search.txt")

	wm := new(exSearchWindowManager)
	searchCfg := standard.SearchConfig{WindowManager: wm}
	b := newExForTestingWithWorkspace(t,
		workspace.NewSchemeWorkspace(rootURI, scheme, inlineSchedule),
		standard.Editor(standard.WithSearchConfig(searchCfg)),
		vte.DefaultConfig(), nopPublishEvent, clipboard.NewInMemory(),
		text.WithCommandKey(testCommandKey),
		text.WithCommandOverlayConfig(testCommandOverlayConfig()),
	)
	defer b.Close()
	wm.comp = b.comp.Browser()
	b.Resize(56, 14)

	fileURI, err := workspaceapi.ParseURI("memory:///search.txt")
	require.NoError(t, err)
	tab, err := b.editFileURI(fileURI, b.invokeWindow(), false)
	require.NoError(t, err)
	tabWindow, ok := tab.Window()
	require.True(t, ok)

	exit, handled := b.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'f'})
	require.False(t, exit)
	require.True(t, handled)
	require.Equal(t, 1, b.comp.Browser().FloatingWindows())

	_, err = b.comp.SetFocus(tabWindow)
	require.NoError(t, err)
	require.NoError(t, b.ex.dispatchCommand("tabclose"))
	assert.Zero(t, b.comp.Browser().FloatingWindows())
}

func TestExTabcloseDirtyTabNoKeepsTabOpen(t *testing.T) {
	b := newExForTesting(t, texttest.NopEditor(),
		text.WithCommandKey(testCommandKey),
		text.WithCommandOverlayConfig(testCommandOverlayConfig()),
	)
	defer b.Close()

	uri, err := workspaceapi.ParseURI("file:///dirty.go")
	require.NoError(t, err)
	_, err = b.editFileURI(uri, b.invokeWindow(), false)
	require.NoError(t, err)
	editBuffer(t, b.ex, uri, "ABC")

	require.NoError(t, b.tabclose(context.Background()))

	assert.Len(t, b.comp.Tabs(), 1)
	assert.Equal(t, 1, b.comp.Browser().FloatingWindows())

	exit, handled := b.Handle(term.Event{Type: term.EventKey, Ch: 'n'})
	assert.False(t, exit)
	assert.True(t, handled)
	assert.Len(t, b.comp.Tabs(), 1)
	assert.Equal(t, 0, b.comp.Browser().FloatingWindows())
}

// TestExTabIconClick clicks and drags on the icons of the rendered tab
// bar: A one shows in the only window and B two is not shown anywhere.
func TestExTabIconClick(t *testing.T) {
	const width, height = 30, 8
	// The frame puts the icons on row 1: A at column 1, B at column 8.
	iconA := term.Coordinates{X: 1, Y: 1}
	iconB := term.Coordinates{X: 8, Y: 1}
	window := term.Coordinates{X: 8, Y: 5}
	type step struct {
		key term.Key
		pos term.Coordinates
	}
	click := func(pos term.Coordinates) []step {
		return []step{{term.MouseLeft, pos}, {term.MouseRelease, pos}}
	}
	const untouched = `┌━━━━━───────────────────────┐
│A one  B two                │
├────────────────────────────┤
│1111111111111111111111111111│
│1111111111111111111111111111│
│1111111111111111111111111111│
│1111111111111111111111111111│
└────────────────────────────┘`
	for _, tc := range []struct {
		name  string
		steps []step
		want  string
	}{
		{
			name:  "closes a tab no window shows",
			steps: click(iconB),
			want: `┌━━━━━───────────────────────┐
│A one                       │
├────────────────────────────┤
│1111111111111111111111111111│
│1111111111111111111111111111│
│1111111111111111111111111111│
│1111111111111111111111111111│
└────────────────────────────┘`,
		},
		{
			name:  "closes the shown tab and shows the next",
			steps: click(iconA),
			want: `┌━━━━━───────────────────────┐
│B two                       │
├────────────────────────────┤
│2222222222222222222222222222│
│2222222222222222222222222222│
│2222222222222222222222222222│
│2222222222222222222222222222│
└────────────────────────────┘`,
		},
		{
			name:  "closes tab after tab",
			steps: append(click(iconA), click(iconA)...),
			want: `┌────────────────────────────┐
│                            │
├────────────────────────────┤
│                            │
│                            │
│                            │
│                            │
└────────────────────────────┘`,
		},
		{
			name: "drag from the icon into the window",
			steps: []step{
				{term.MouseLeft, iconB}, {term.MouseLeft, window},
				{term.MouseRelease, window},
			},
			want: untouched,
		},
		{
			name: "drag from the window onto the icon",
			steps: []step{
				{term.MouseLeft, window}, {term.MouseLeft, iconB},
				{term.MouseRelease, iconB},
			},
			want: untouched,
		},
		{
			name: "drag between the icons",
			steps: []step{
				{term.MouseLeft, iconB}, {term.MouseLeft, iconA},
				{term.MouseRelease, iconA},
			},
			want: untouched,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newExForTesting(t, texttest.NopEditor(),
				text.WithCommandKey(testCommandKey),
				text.WithCommandOverlayConfig(testCommandOverlayConfig()),
			)
			defer b.Close()
			var tabs []*browser.Tab
			for i, name := range []string{"one", "two"} {
				uri, err := workspaceapi.ParseURI("file:///" + name)
				require.NoError(t, err)
				h := browsertest.NewTestHandler()
				h.Ch = '1' + rune(i)
				// Keep the content still so the screen only shows tab changes.
				h.HandleOverride = func(term.Event) (bool, bool) { return false, true }
				tab, err := b.comp.Tab(uri, 'A'+rune(i), name, h)
				require.NoError(t, err)
				tabs = append(tabs, tab.(*browser.Tab))
			}
			focus, err := b.comp.Focus()
			require.NoError(t, err)
			require.NoError(t, focus.SetContent(tabs[0]))
			b.Resize(width, height)
			require.Equal(t, untouched, handlertest.DrawHandler(b, width, height))

			for _, s := range tc.steps {
				b.Handle(term.Event{
					Type: term.EventMouse, Key: s.key, MouseX: s.pos.X, MouseY: s.pos.Y,
				})
				handlertest.DrawHandler(b, width, height)
			}
			assert.Equal(t, tc.want, handlertest.DrawHandler(b, width, height))
		})
	}
}

func TestExTabIconClickPromptsForDirtyTab(t *testing.T) {
	for _, tc := range []struct {
		answer   rune
		wantTabs int
	}{
		{answer: 'y', wantTabs: 0},
		{answer: 'n', wantTabs: 1},
	} {
		t.Run(string(tc.answer), func(t *testing.T) {
			b := newExForTesting(t, texttest.NopEditor(),
				text.WithCommandKey(testCommandKey),
				text.WithCommandOverlayConfig(testCommandOverlayConfig()),
			)
			defer b.Close()
			b.Resize(30, 8)

			uri, err := workspaceapi.ParseURI("file:///dirty.go")
			require.NoError(t, err)
			_, err = b.editFileURI(uri, b.invokeWindow(), false)
			require.NoError(t, err)
			editBuffer(t, b.ex, uri, "ABC")
			handlertest.DrawHandler(b, 30, 8)

			// The only tab's icon sits right past the frame.
			icon := term.Coordinates{X: 1, Y: 1}
			b.Handle(term.Event{Type: term.EventMouse, Key: term.MouseLeft, MouseX: icon.X, MouseY: icon.Y})
			b.Handle(term.Event{Type: term.EventMouse, Key: term.MouseRelease, MouseX: icon.X, MouseY: icon.Y})

			assert.Len(t, b.comp.Tabs(), 1)
			assert.Equal(t, 1, b.comp.Browser().FloatingWindows())

			exit, handled := b.Handle(term.Event{Type: term.EventKey, Ch: tc.answer})
			assert.False(t, exit)
			assert.True(t, handled)
			assert.Len(t, b.comp.Tabs(), tc.wantTabs)
			assert.Equal(t, 0, b.comp.Browser().FloatingWindows())
			dirty, ok := b.comp.IsDirty(uri)
			assert.Equal(t, tc.wantTabs == 1, ok && dirty)
		})
	}
}

func TestExTabcloseallPromptsForDirtyTabs(t *testing.T) {
	b := newExForTesting(t, texttest.NopEditor(),
		text.WithCommandKey(testCommandKey),
		text.WithCommandOverlayConfig(testCommandOverlayConfig()),
	)
	defer b.Close()

	dirtyURI, err := workspaceapi.ParseURI("file:///dirty.go")
	require.NoError(t, err)
	_, err = b.editFileURI(dirtyURI, b.invokeWindow(), false)
	require.NoError(t, err)
	editBuffer(t, b.ex, dirtyURI, "ABC")
	cleanURI, err := workspaceapi.ParseURI("file:///clean.go")
	require.NoError(t, err)
	_, err = b.editFileURI(cleanURI, b.invokeWindow(), false)
	require.NoError(t, err)

	require.NoError(t, b.tabcloseall(context.Background()))

	assert.Len(t, b.comp.Tabs(), 2)
	assert.Equal(t, 1, b.comp.Browser().FloatingWindows())

	exit, handled := b.Handle(term.Event{Type: term.EventKey, Ch: 'y'})
	assert.False(t, exit)
	assert.True(t, handled)
	assert.Empty(t, b.comp.Tabs())
	assert.Equal(t, 0, b.comp.Browser().FloatingWindows())
}

func TestExTabcloseinactivePromptsForDirtyInactiveTabs(t *testing.T) {
	b := newExForTesting(t, texttest.NopEditor(),
		text.WithCommandKey(testCommandKey),
		text.WithCommandOverlayConfig(testCommandOverlayConfig()),
	)
	defer b.Close()

	inactiveURI, err := workspaceapi.ParseURI("file:///inactive.go")
	require.NoError(t, err)
	inactiveTab, err := b.editFileURI(inactiveURI, b.invokeWindow(), false)
	require.NoError(t, err)
	editBuffer(t, b.ex, inactiveURI, "ABC")
	activeURI, err := workspaceapi.ParseURI("file:///active.go")
	require.NoError(t, err)
	activeTab, err := b.editFileURI(activeURI, b.invokeWindow(), false)
	require.NoError(t, err)
	_, active := activeTab.Window()
	require.True(t, active)
	_, inactive := inactiveTab.Window()
	require.False(t, inactive)

	require.NoError(t, b.tabcloseinactive(context.Background()))

	assert.Len(t, b.comp.Tabs(), 2)
	assert.Equal(t, 1, b.comp.Browser().FloatingWindows())

	exit, handled := b.Handle(term.Event{Type: term.EventKey, Ch: 'y'})
	assert.False(t, exit)
	assert.True(t, handled)
	tabs := b.comp.Tabs()
	require.Len(t, tabs, 1)
	assert.True(t, tabs[0].URI().Equal(activeURI))
	assert.Equal(t, 0, b.comp.Browser().FloatingWindows())
}

func TestExExit(t *testing.T) {
	commands := []string{
		"writequit",
		"writeforcequit!",
		"forcequit!",
		"quit",
	}

	for _, cmd := range commands {
		t.Run(fmt.Sprintf("ex exits %s command is issued", cmd), func(t *testing.T) {
			b := newExForTesting(t, texttest.NopEditor(),
				text.WithCommandKey(testCommandKey),
				text.WithCommandOverlayConfig(testCommandOverlayConfig()),
			)
			defer b.Close()

			// start command prompt
			ev := term.Event{
				Type: term.EventKey,
				Ch:   testCommandKey.Ch,
				Mod:  testCommandKey.Mod,
				Key:  testCommandKey.Key,
			}
			exit, handled := b.Handle(ev)
			assert.True(t, handled)
			require.False(t, exit)

			for _, ch := range cmd {
				exit, handled := b.Handle(term.Event{Ch: ch, Type: term.EventKey})
				assert.True(t, handled)
				require.False(t, exit)
			}

			exit, handled = b.Handle(
				term.Event{Key: term.KeyEnter, Type: term.EventKey})
			assert.True(t, handled)
			require.True(t, exit)

		})
	}

	t.Run("ex does not exit when inner handler returns exit=true", func(t *testing.T) {
		b := newExForTesting(t, texttest.NopEditor(),
			text.WithCommandOverlayConfig(testCommandOverlayConfig()),
		)
		defer b.Close()

		h := browsertest.NewTestHandler()
		h.Exit = true
		h.Handled = true

		uri, err := workspaceapi.ParseURI("file:///bols")
		require.NoError(t, err)

		tab, err := b.comp.Tab(uri, 'x', "bleh", h)
		require.NoError(t, err)

		w, err := b.comp.Focus()
		require.NoError(t, err)

		err = w.SetContent(tab)
		require.NoError(t, err)

		exit, handled := b.Handle(term.Event{Ch: 'a', Type: term.EventKey})
		assert.True(t, handled)
		assert.False(t, exit)
	})
}

// remove non-determinism of search.List async search
type testEx struct {
	*ex
	mu        sync.Locker
	scheduler *queuedScheduler
}

type exSearchWindowManager struct {
	comp *browser.Component
}

func (m *exSearchWindowManager) Floating(
	f browserapi.Floating, cfg browserapi.FloatingConfig,
) (browserapi.Window, error) {
	return m.comp.Floating(f, cfg), nil
}

func (m *exSearchWindowManager) CloseWindow(win browserapi.Window) error {
	return win.(browser.Window).Close()
}

func (m *exSearchWindowManager) SetTabActivity(workspaceapi.URI, bool) error { return nil }

func (t testEx) Handle(ev term.Event) (bool, bool) {
	unlock := t.lock()
	quit, handle := t.ex.Handle(ev)
	unlock()
	if t.ex.companionConsole != nil {
		t.ex.companionConsole.Wait()
	}
	t.ex.Wait()
	// Wait for async flush completions to deliver their callbacks
	// to the scheduler queue, then drain the scheduler once under
	// t.mu (mirroring the host event loop). New callbacks
	// scheduled by tasks (e.g. tcell colour resets) are intentionally
	// not awaited here — they ride the next Handle/Draw turn the
	// same way the production event loop processes them.
	t.ex.waitInflight()
	t.flushScheduled()
	t.drainAliasRuns()
	return quit, handle
}

func (t testEx) Draw(w term.Writer) {
	t.flushScheduled()
	unlock := t.lock()
	defer unlock()
	t.ex.Draw(w)
}

func (t testEx) Resize(width, height int) {
	unlock := t.lock()
	defer unlock()
	t.ex.Resize(width, height)
}

func (t testEx) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	unlock := t.lock()
	defer unlock()
	return t.ex.Cursor()
}

func (t testEx) Selection() (string, bool) {
	unlock := t.lock()
	defer unlock()
	return t.ex.Selection()
}

func (t testEx) lock() func() {
	if t.mu == nil {
		return func() {}
	}
	t.mu.Lock()
	return t.mu.Unlock
}

func (t testEx) flushScheduled() {
	if t.scheduler == nil {
		return
	}
	t.scheduler.Flush(t.mu)
}

// drainAliasRuns pumps the test scheduler until no command dispatch is
// in flight or queued, or a bound elapses. It stands in for the host
// event loop, which keeps ticking while a dispatch is parked.
func (t testEx) drainAliasRuns() {
	deadline := time.Now().Add(10 * time.Second)
	for {
		t.flushScheduled()
		unlock := t.lock()
		quiet := t.ex.runInFlight == nil && len(t.ex.runQueue) == 0
		unlock()
		if quiet || time.Now().After(deadline) {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

type queuedScheduler struct {
	mu      sync.Mutex
	pending []func()
}

func newQueuedScheduler() *queuedScheduler {
	return &queuedScheduler{}
}

func (s *queuedScheduler) ScheduleNextTick(fn func()) bool {
	s.mu.Lock()
	s.pending = append(s.pending, fn)
	s.mu.Unlock()
	return true
}

func (s *queuedScheduler) Flush(lock sync.Locker) {
	for {
		s.mu.Lock()
		if len(s.pending) == 0 {
			s.mu.Unlock()
			return
		}
		fn := s.pending[0]
		s.pending = s.pending[1:]
		s.mu.Unlock()
		// Mirror the host event loop's UserFunc dispatch
		// (gui.Update / tui.Run): the lock is held while fn
		// runs so callbacks observe a consistent IDE state.
		if lock != nil {
			lock.Lock()
		}
		fn()
		if lock != nil {
			lock.Unlock()
		}
	}
}

// installDefaultTestScheduler installs a queued, lock-serializing
// scheduler into cfg if the caller didn't supply one (i.e. cfg still
// has the inline default from vte.DefaultConfig). It returns the
// scheduler and the lock so testEx can drain pending callbacks before
// Handle/Draw assertions, mirroring the production event loop where
// scheduled callbacks run under the host's UI lock.
//
// The queue is deliberately passive (drained at explicit flush
// points on the test goroutine) rather than an active consumer like
// newTestScheduler: the ex harness dispatches re-entrantly on the
// test goroutine (echo → publishEvent → testEx.Handle), which a
// consumer goroutine holding a non-reentrant mutex would deadlock.
// The IDE-level handler and macro harnesses are not re-entrant and
// use newTestScheduler/quiesceHandler instead.
//
// We can't compare function values directly; instead we detect the
// inline default by exercising it: it runs the callback synchronously
// and returns true. A custom scheduler that queues for later won't run
// the probe inline.
func installDefaultTestScheduler(cfg *vte.Config) (*queuedScheduler, sync.Locker) {
	if cfg.ScheduleNextTick == nil {
		scheduler := newQueuedScheduler()
		mu := &sync.Mutex{}
		cfg.ScheduleNextTick = scheduler.ScheduleNextTick
		return scheduler, mu
	}
	var ran atomic.Bool
	cfg.ScheduleNextTick(func() { ran.Store(true) })
	if !ran.Load() {
		// Custom scheduler that queues; trust the caller.
		return nil, nil
	}
	scheduler := newQueuedScheduler()
	cfg.ScheduleNextTick = scheduler.ScheduleNextTick
	// Callbacks queued by scheduler.ScheduleNextTick run only when
	// testEx.Handle/Draw drains the queue (via flushScheduled). The
	// drain is on the test goroutine so there is no concurrent
	// access to serialise — we therefore pass nil for the locker,
	// which avoids deadlocks when Handle is invoked re-entrantly
	// (e.g. echo → publishEvent → testEx.Handle).
	return scheduler, nil
}

type testWorkspaceWithURI struct {
	*testLoader
	uri workspaceapi.URI
}

func (w testWorkspaceWithURI) URI(path string) (workspaceapi.URI, error) {
	return workspace.NewWorkspaceURI(w.uri, path)
}

type readfileCrossWorkspaceLoader struct {
	testWorkspaceWithURI
	foreignURI      workspaceapi.URI
	currentContents string
}

func (w *readfileCrossWorkspaceLoader) Load(
	filePath workspaceapi.URI, buf *cell.Buffer,
	swapDir workspaceapi.URI, readOnly bool,
) (workspace.FlusherCloser, error) {
	if filePath.Equal(w.foreignURI) {
		return nil, workspace.ErrOpenInOtherWorkspace
	}
	if w.currentContents != "" {
		buf.WriteString(w.currentContents)
	}
	return &testFileBuffer{readOnly: readOnly}, nil
}

func (w *readfileCrossWorkspaceLoader) OpenFile(
	path string, flag int, perm os.FileMode,
) (workspaceapi.File, error) {
	expanded, err := workspaceapi.ExpandPathWithURI(path, w.uri)
	if err == nil {
		path = expanded
	}
	return os.OpenFile(path, flag, perm)
}

func (w *readfileCrossWorkspaceLoader) Open(path string) (workspaceapi.File, error) {
	return w.OpenFile(path, os.O_RDONLY, 0)
}

func defCommandKeyBindings() (opts []text.Option) {
	opts = append(opts, text.WithCommandKeyBinding(
		term.KeyComb{Mod: term.ModCtrl, Ch: 'w'}, [][]string{{"tabclose"}}))
	opts = append(opts, text.WithCommandKeyBinding(
		term.KeyComb{Mod: term.ModCtrl, Ch: 'l'}, [][]string{{"tabnext"}}))
	opts = append(opts, text.WithCommandKeyBinding(
		term.KeyComb{Mod: term.ModCtrl, Ch: 'h'}, [][]string{{"tabprevious"}}))
	return
}

// testPromptEditor returns the prompt editor used to satisfy ex.init's
// required dependency in tests that build an *ex directly.
func testPromptEditor() command.Editor {
	return standardPromptEditor{
		tabspaces:        4,
		scheduleNextTick: func(fn func()) bool { fn(); return true },
		clipboard:        clipboard.NewInMemory(),
	}
}

func newExForTestingTerminal(
	t *testing.T, workspace workspace.Workspace,
	ed text.Editor,
	emulatorCfg vte.Config,
	publishEvent func(term.Event) bool,
	barCfg plugin.BarConfig,
	opts ...text.Option,
) testEx {
	ex := new(ex)
	ex.syncCommandPrompt = true
	svc := storagestub.NewInMemoryService()
	notifications := newWorkspaceNotifications(svc, notificationsConfig(),
		&workspaceManagerMock{workspace: ex})
	uri, err := workspace.URI(".")
	require.NoError(t, err)
	opts = append(opts, text.WithCommandOverlayConfig(testCommandOverlayConfig()))
	opts = append(opts, defCommandKeyBindings()...)
	scheduler, mu := installDefaultTestScheduler(&emulatorCfg)
	require.NoError(t, ex.init(func(exoeditor.Reloader, schemeapi.Terminal) (text.Editor, error) { return ed, nil }, workspace, svc,
		notifications, uri, emulatorCfg, barCfg, publishEvent,
		0, clipboard.NewInMemory(), nil, nil, nil, nil, testPromptEditor(), opts...))
	ex.subscribeCommands()
	return testEx{ex: ex, mu: mu, scheduler: scheduler}
}

// newExForTestingVTECapacity builds an ex through the production init
// path (no newEmulatorHandler override) with an explicit initial VTE
// reservoir capacity.
func newExForTestingVTECapacity(
	t *testing.T, ws workspace.Workspace, initialVTECapacity int,
) testEx {
	t.Helper()
	e := new(ex)
	e.syncCommandPrompt = true
	opts := defCommandKeyBindings()
	opts = append(opts, text.WithCommandOverlayConfig(testCommandOverlayConfig()))
	opts = append(opts, text.WithFloatingNoMaxSize(false))

	svc := storagestub.NewInMemoryService()
	notifications := newWorkspaceNotifications(svc, notificationsConfig(),
		&workspaceManagerMock{workspace: e})

	uri, err := ws.URI(".")
	require.NoError(t, err)

	emulatorCfg := vte.DefaultConfig()
	scheduler, mu := installDefaultTestScheduler(&emulatorCfg)
	require.NoError(t, e.init(
		func(exoeditor.Reloader, schemeapi.Terminal) (text.Editor, error) {
			return texttest.NopEditor(), nil
		}, ws, svc,
		notifications, uri, emulatorCfg, plugin.DefaultBarConfig(),
		nopPublishEvent, initialVTECapacity, clipboard.NewInMemory(),
		nil, nil, nil, nil, testPromptEditor(), opts...))
	return testEx{ex: e, mu: mu, scheduler: scheduler}
}

// blockingResizeWorkspace parks SetPtySize once armed, standing in for
// a transport that is wedged but has not surfaced a disconnect.
type blockingResizeWorkspace struct {
	*testLoader
	armed atomic.Bool
	// blockOn narrows the park to one size. The resize worker is a
	// single goroutine, so a warm-up resize that enters first parks
	// it forever and the resize under test never reaches the
	// transport.
	blockOn atomic.Pointer[[2]int]
	entered chan [2]int
	release chan struct{}
}

func newBlockingResizeWorkspace() *blockingResizeWorkspace {
	return &blockingResizeWorkspace{
		testLoader: &testLoader{},
		entered:    make(chan [2]int, 64),
		release:    make(chan struct{}),
	}
}

func (w *blockingResizeWorkspace) SetPtySize(
	_ workspaceapi.Pty, size workspaceapi.PtySize,
) error {
	width, height := size.Columns, size.Rows
	if !w.armed.Load() {
		return nil
	}
	if only := w.blockOn.Load(); only != nil && *only != [2]int{width, height} {
		return nil
	}
	select {
	case w.entered <- [2]int{width, height}:
	default:
	}
	<-w.release
	return nil
}

// armFor parks the transport only on a resize to width x height.
func (w *blockingResizeWorkspace) armFor(width, height int) {
	w.blockOn.Store(&[2]int{width, height})
	w.armed.Store(true)
}

func (w *blockingResizeWorkspace) waitEntered(t *testing.T) [2]int {
	t.Helper()
	select {
	case got := <-w.entered:
		return got
	case <-time.After(10 * time.Second):
		t.Fatal("the pty resize never reached the workspace transport")
		return [2]int{}
	}
}

// TestExResizeDoesNotBlockOnPtyResize reproduces the IDE freeze where a
// window resize fanned out to every live VTE and each
// vte.Component.Resize called SetPtySize inline on the event loop, so a
// single stalled transport RPC hung the whole UI.
func TestExResizeDoesNotBlockOnPtyResize(t *testing.T) {
	assertResizeReturns := func(t *testing.T, b testEx) {
		t.Helper()
		done := make(chan struct{})
		go debug.CapturePanicReport(func() {
			defer close(done)
			b.Resize(80, 24)
		})
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("ex.Resize blocked on the pty resize RPC")
		}
	}

	t.Run("installed terminal", func(t *testing.T) {
		ws := newBlockingResizeWorkspace()
		defer close(ws.release)
		b := newExForTestingVTECapacity(t, ws, 0)
		defer b.Close()

		b.Resize(100, 40)
		require.NoError(t, b.terminalnewtab(context.Background()))
		b.waitAsyncVTELoads()
		b.flushScheduled()

		ws.armed.Store(true)
		assertResizeReturns(t, b)
		got := ws.waitEntered(t)
		assert.Positive(t, got[0])
		assert.Positive(t, got[1])
	})

	t.Run("reservoir warm terminals", func(t *testing.T) {
		ws := newBlockingResizeWorkspace()
		defer close(ws.release)
		b := newExForTestingVTECapacity(t, ws, 1)
		defer b.Close()

		require.NotNil(t, b.reservoir)
		b.reservoir.WaitForInitialFill()

		ws.armFor(80, 24)
		assertResizeReturns(t, b)
		assert.Equal(t, [2]int{80, 24}, ws.waitEntered(t))
	})
}

// remoteURILoader is a testLoader whose workspace lives on another
// machine.
type remoteURILoader struct {
	*testLoader
}

func (remoteURILoader) URI(string) (workspaceapi.URI, error) {
	return workspaceapi.ParseURI("ssh://host/remote")
}

// TestExGraphicsTempDir pins that only a local workspace lends the
// terminal this process's temp dir, where a t=t graphics transmission
// may be deleted: a remote machine's TMPDIR is unknown.
func TestExGraphicsTempDir(t *testing.T) {
	local := newExForTestingVTECapacity(t, &testLoader{}, 0)
	defer local.Close()
	assert.Equal(t, os.TempDir(), local.emulatorConfig.TempDir)

	remote := newExForTestingVTECapacity(t, remoteURILoader{&testLoader{}}, 0)
	defer remote.Close()
	assert.Empty(t, remote.emulatorConfig.TempDir)
}

func newExForTestingWithWorkspace(
	t *testing.T, workspace workspace.Workspace,
	ed text.Editor,
	emulatorCfg vte.Config,
	publishEvent func(term.Event) bool,
	clip clipboard.Register,
	opts ...text.Option,
) testEx {
	ex := new(ex)
	ex.syncCommandPrompt = true
	// user opts override default test opts
	finalOpts := defCommandKeyBindings()
	finalOpts = append(finalOpts, text.WithCommandOverlayConfig(testCommandOverlayConfig()))
	finalOpts = append(finalOpts, text.WithFloatingNoMaxSize(false))
	finalOpts = append(finalOpts, opts...)

	svc := storagestub.NewInMemoryService()
	notifications := newWorkspaceNotifications(svc, notificationsConfig(),
		&workspaceManagerMock{workspace: ex})

	uri, err := workspace.URI(".")
	require.NoError(t, err)

	scheduler, mu := installDefaultTestScheduler(&emulatorCfg)
	require.NoError(t, ex.init(func(exoeditor.Reloader, schemeapi.Terminal) (text.Editor, error) { return ed, nil }, workspace, svc,
		notifications, uri, emulatorCfg, plugin.DefaultBarConfig(),
		publishEvent, 0, clip, nil, nil, nil, nil, testPromptEditor(), finalOpts...))
	ex.subscribeCommands()
	ex.newEmulatorHandler = func(args []string) (vtereservoir.VTE, error) {
		return newTestVteWithConfig(args), nil
	}
	ex.newPluginHandler = func(_ int, args ...string) (pluginHandler, error) {
		return newTestVteWithConfig(args), nil
	}
	ex.pluginWaitTimeout = 1 * time.Second
	return testEx{ex: ex, mu: mu, scheduler: scheduler}
}

func newExForTestingCommandsPreview(
	t *testing.T, workspace workspace.Workspace,
	ed text.Editor,
	emulatorCfg vte.Config,
	publishEvent func(term.Event) bool,
	clip clipboard.Register,
	previews map[string]previewFunc,
	opts ...text.Option,
) testEx {
	ex := new(ex)
	ex.syncCommandPrompt = true
	// user opts override default test opts
	finalOpts := defCommandKeyBindings()
	finalOpts = append(finalOpts, text.WithCommandOverlayConfig(testCommandOverlayConfig()))
	finalOpts = append(finalOpts, text.WithFloatingNoMaxSize(false))
	finalOpts = append(finalOpts, opts...)

	svc := storagestub.NewInMemoryService()
	notifications := newWorkspaceNotifications(svc, notificationsConfig(),
		&workspaceManagerMock{workspace: ex})

	uri, err := workspace.URI(".")
	require.NoError(t, err)

	scheduler, mu := installDefaultTestScheduler(&emulatorCfg)
	require.NoError(t, ex.init(func(exoeditor.Reloader, schemeapi.Terminal) (text.Editor, error) { return ed, nil }, workspace, svc,
		notifications, uri, emulatorCfg, plugin.DefaultBarConfig(),
		publishEvent, 0, clip, nil, previews, nil, nil, testPromptEditor(), finalOpts...))
	ex.subscribeCommands()
	ex.newEmulatorHandler = func(args []string) (vtereservoir.VTE, error) {
		return newTestVteWithConfig(args), nil
	}
	ex.newPluginHandler = func(_ int, args ...string) (pluginHandler, error) {
		return newTestVteWithConfig(args), nil
	}
	return testEx{ex: ex, mu: mu, scheduler: scheduler}
}

func newExForTesting(t *testing.T, ed text.Editor, opts ...text.Option) testEx {
	return newExForTestingWithWorkspace(t, &testLoader{}, ed, vte.DefaultConfig(),
		nopPublishEvent, clipboard.NewInMemory(), opts...)
}

func newExForTestingClipboard(
	t *testing.T, ed text.Editor, clip clipboard.Register, opts ...text.Option,
) testEx {
	return newExForTestingWithWorkspace(t, &testLoader{}, ed, vte.DefaultConfig(),
		nopPublishEvent, clip, opts...)
}

// newExForTestingWithStorage is a variant of newExForTestingWithWorkspace
// that accepts a caller-supplied storage service so a second session can
// be booted on the same backing storage (e.g. to exercise workspace
// layout restore flows).
func newExForTestingWithStorage(
	t *testing.T, workspace workspace.Workspace, svc storageapi.Service,
	ed text.Editor,
	emulatorCfg vte.Config,
	publishEvent func(term.Event) bool,
	clip clipboard.Register,
	opts ...text.Option,
) testEx {
	ex := new(ex)
	ex.syncCommandPrompt = true
	finalOpts := defCommandKeyBindings()
	finalOpts = append(finalOpts, text.WithCommandOverlayConfig(testCommandOverlayConfig()))
	finalOpts = append(finalOpts, text.WithFloatingNoMaxSize(false))
	finalOpts = append(finalOpts, opts...)

	notifications := newWorkspaceNotifications(svc, notificationsConfig(),
		&workspaceManagerMock{workspace: ex})

	uri, err := workspace.URI(".")
	require.NoError(t, err)

	require.NoError(t, ex.init(func(exoeditor.Reloader, schemeapi.Terminal) (text.Editor, error) { return ed, nil }, workspace, svc,
		notifications, uri, emulatorCfg, plugin.DefaultBarConfig(),
		publishEvent, 0, clip, nil, nil, nil, nil, testPromptEditor(), finalOpts...))
	ex.subscribeCommands()
	ex.newEmulatorHandler = func(args []string) (vtereservoir.VTE, error) {
		return newTestVteWithConfig(args), nil
	}
	ex.newPluginHandler = func(_ int, args ...string) (pluginHandler, error) {
		return newTestVteWithConfig(args), nil
	}
	ex.pluginWaitTimeout = 1 * time.Second
	return testEx{ex: ex}
}

func TestNewWindow(t *testing.T) {
	cases := []handlertest.SequenceTestCase{
		{":windownew>:windowdefaultsplit h>:windownew>",
			`┌────┌─────────────┐
│    │ changed     │
├────│ split       │
│    │ direction   │
│    │ to          │
│    │ horizontal  │
│    └─────────────┘
│        ││        │
│        ││        │
└────────┘└────────┘`},
		{":winclose>:winclose>aaaaaaa",
			`┌────┌─────────────┐
│    │ changed     │
├────│ split       │
│    │ direction   │
│    │ to          │
│    │ horizontal  │
│    └─────────────┘
│                  │
│                  │
└──────────────────┘`},
	}

	opts := []text.Option{
		text.WithCommandKey(testCommandKey),
	}
	b := newExForTesting(t, texttest.NopEditor(), opts...)
	defer b.Close()

	handlertest.TestHandlerSequence(t, b, 20, 10, cases)
}

func TestCloseWindowIdempotent(t *testing.T) {
	cases := []handlertest.SequenceTestCase{
		{":edit hello.go>",
			`┌━━━━━━━━━━────────┐
│o hello.go        │
├──────────────────┤
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
└──────────────────┘`},
		// closing the only tiled window is a no-op (no error popup).
		{":windowclose>",
			`┌━━━━━━━━━━────────┐
│o hello.go        │
├──────────────────┤
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
└──────────────────┘`},
		// closing all other windows when there are none is a no-op too.
		{":windowcloseall>",
			`┌━━━━━━━━━━────────┐
│o hello.go        │
├──────────────────┤
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
└──────────────────┘`},
	}

	opts := []text.Option{
		text.WithCommandKey(testCommandKey),
	}
	b := newExForTesting(t, texttest.NopEditor(), opts...)
	defer b.Close()

	handlertest.TestHandlerSequence(t, b, 20, 10, cases)
}

func TestFloatingPromptClosePrefersFloatingFocus(t *testing.T) {
	b := newExForTesting(t, texttest.NopEditor(), text.WithCommandKey(testCommandKey))
	defer b.Close()

	b.Resize(40, 12)
	browserComp := b.ex.comp.Browser()
	mainFocus, err := b.ex.Browser().Focus()
	require.NoError(t, err)
	require.False(t, mainFocus.IsFloating())

	var prompts []browser.Window
	for _, message := range []string{
		"first prompt",
		"second prompt",
		"third prompt",
		"fourth prompt",
		"fifth prompt",
	} {
		prompts = append(prompts, browserComp.Prompt(
			message,
			[]string{yesOpt, noOpt},
			yesNoKeyCombs,
			handler.NopPromptHandler(),
		))
	}

	require.Equal(t, len(prompts), browserComp.FloatingWindows())

	closed := make(map[uint64]bool, len(prompts))
	for i := len(prompts) - 1; i >= 0; i-- {
		focus, err := b.ex.Browser().Focus()
		require.NoError(t, err)
		require.True(t, focus.IsFloating())
		require.False(t, closed[focus.WindowID()])

		content, err := focus.Content()
		require.NoError(t, err)
		_, ok := content.(*handler.Prompt)
		require.True(t, ok)

		closedID := focus.WindowID()
		_, handled := b.Handle(term.Event{Type: term.EventKey, Ch: 'y'})
		require.True(t, handled)
		closed[closedID] = true
		assert.Equal(t, i, browserComp.FloatingWindows())

		if i == 0 {
			break
		}

		nextFocus, err := b.ex.Browser().Focus()
		require.NoError(t, err)
		assert.True(t, nextFocus.IsFloating())
		assert.False(t, closed[nextFocus.WindowID()])
		assert.NotEqual(t, closedID, nextFocus.WindowID())
	}

	for _, prompt := range prompts {
		assert.True(t, prompt.Closed())
	}

	focus, err := b.ex.Browser().Focus()
	require.NoError(t, err)
	assert.Equal(t, mainFocus.WindowID(), focus.WindowID())
	assert.False(t, focus.IsFloating())
}

// TestWindowCloseAllClosesFloatingWindows asserts that `windowcloseall`
// clears floating windows too, including when one of them holds focus:
// `!` program output opens as a focused floating window, so a
// "clear the layout" step that left it on screen would be useless.
func TestWindowCloseAllClosesFloatingWindows(t *testing.T) {
	b := newExForTesting(t, texttest.NopEditor(), text.WithCommandKey(testCommandKey))
	defer b.Close()

	b.Resize(40, 12)
	browserComp := b.ex.comp.Browser()
	require.NoError(t, b.ex.windownew(t.Context()))
	require.Equal(t, 2, browserComp.Tiles())

	prompt := browserComp.Prompt("keep me?", []string{yesOpt, noOpt},
		yesNoKeyCombs, handler.NopPromptHandler())
	require.Equal(t, 1, browserComp.FloatingWindows())

	focus, err := b.ex.Browser().Focus()
	require.NoError(t, err)
	require.True(t, focus.IsFloating(),
		"the prompt must hold focus for this to exercise the bug")

	require.NoError(t, b.ex.windowcloseall(t.Context()))
	assert.Equal(t, 0, browserComp.FloatingWindows(),
		"windowcloseall must close floating windows")
	assert.Equal(t, 1, browserComp.Tiles())
	assert.True(t, prompt.Closed())

	focus, err = b.ex.Browser().Focus()
	require.NoError(t, err)
	assert.False(t, focus.IsFloating(),
		"focus must land back on the surviving tile")
}

type testShellREPLHandler struct{}

func (*testShellREPLHandler) HandleCommand(
	context.Context, repl.Command, repl.ProgressWriter,
) (sdkiterator.Iterator[component.Responsive], error) {
	return sdkiterator.Empty[component.Responsive](), nil
}

func (*testShellREPLHandler) Complete(
	context.Context, string, []string,
) (sdkiterator.Iterator[string], error) {
	return sdkiterator.Empty[string](), nil
}

func (*testShellREPLHandler) Help(
	context.Context, []string,
) (sdkiterator.Iterator[component.Responsive], error) {
	return sdkiterator.Empty[component.Responsive](), nil
}

// completeStubREPLHandler returns a fixed set of completion candidates
// so completion delegation can be asserted.
type completeStubREPLHandler struct {
	candidates []string
}

func (*completeStubREPLHandler) HandleCommand(
	context.Context, repl.Command, repl.ProgressWriter,
) (sdkiterator.Iterator[component.Responsive], error) {
	return sdkiterator.Empty[component.Responsive](), nil
}

func (h *completeStubREPLHandler) Complete(
	context.Context, string, []string,
) (sdkiterator.Iterator[string], error) {
	return sdkiterator.FromSlice(h.candidates), nil
}

func (*completeStubREPLHandler) Help(
	context.Context, []string,
) (sdkiterator.Iterator[component.Responsive], error) {
	return sdkiterator.Empty[component.Responsive](), nil
}

func TestCommandHistory(t *testing.T) {
	cases := []handlertest.SequenceTestCase{
		{":edit a.go>:edit wi.go>1234",
			`┌────────━━━━━━━───┐
│o a.go  o wi.go   │
├──────────────────┤
│EEEEEEEEEEEEEEEEEE│
│EEEEEEEEEEEEEEEEEE│
│EEEEEEEEEEEEEEEEEE│
│EEEEEEEEEEEEEEEEEE│
│EEEEEEEEEEEEEEEEEE│
│EEEEEEEEEEEEEEEEEE│
└──────────────────┘`},
		{"::::>",
			`┌────────━━━━━━━───┐
│o a.go  o wi.go   │
├──────────────────┤
│EEEEEEEEEEEEEEEEEE│
│EEEEEEEEEEEEEEEEEE│
│EEEEEEEEEEEEEEEEEE│
│EEEEEEEEEEEEEEEEEE│
│EEEEEEEEEEEEEEEEEE│
│EEEEEEEEEEEEEEEEEE│
└──────────────────┘`},
	}

	opts := []text.Option{
		text.WithCommandKey(testCommandKey),
	}
	b := newExForTesting(t, texttest.NopEditor(), opts...)
	defer b.Close()

	handlertest.TestHandlerSequence(t, b, 20, 10, cases)
}

func TestCommandHistoryPrompt(t *testing.T) {
	opts := []text.Option{
		text.WithCommandKey(testCommandKey),
	}
	b := newExForTesting(t, texttest.NopEditor(), opts...)
	defer b.Close()
	b.Resize(30, 12)

	// Execute some commands to build history by driving events directly.
	// In the legacy handleTestCase encoding: ':' => command key (Ctrl+\),
	// ' ' => space, '>' => Enter.
	for _, seq := range []string{":echo a>", ":echo b>"} {
		for _, r := range seq {
			switch r {
			case ':':
				b.Handle(term.Event{Mod: term.ModCtrl, Ch: '\\', Type: term.EventKey})
			case ' ':
				b.Handle(term.Event{Key: term.KeySpace, Type: term.EventKey})
			case '>':
				b.Handle(term.Event{Key: term.KeyEnter, Type: term.EventKey})
			default:
				b.Handle(term.Event{Ch: r, Type: term.EventKey})
			}
		}
	}

	// Now open the history prompt programmatically.
	require.Nil(t, b.ex.cmd)
	err := b.ex.openCommandHistoryPrompt(context.Background())
	require.NoError(t, err)
	require.NotNil(t, b.ex.cmd)

	// Close the history prompt.
	require.NotNil(t, b.ex.cmdWin)
	require.NoError(t, b.ex.cmdWin.Close())
	assert.Nil(t, b.ex.cmd)
}

func TestCommandPromptUsesSharedStoragePartition(t *testing.T) {
	b := newExForTesting(t, texttest.NopEditor(), text.WithCommandKey(testCommandKey))
	store := &closeCountingPartitionStore{Service: storagestub.NewInMemoryService()}
	b.ex.storage = store

	b.ex.openCommandPrompt()
	require.NotNil(t, b.ex.cmdWin)
	require.NoError(t, b.ex.cmdWin.Close())
	assert.Equal(t, int32(0), store.partitionCloseCount.Load())
	assert.Nil(t, b.ex.cmd)

	b.ex.openCommandPrompt()
	require.NotNil(t, b.ex.cmd)
	require.NoError(t, b.ex.Close())
	assert.Equal(t, int32(0), store.partitionCloseCount.Load())
}

// TestCommandPromptHasNoWindowBar pins that the command prompt keeps
// its plain hand-drawn frame with the window bar feature enabled by
// default: the bar override applies only to floating windows of framed
// window managers, and the prompt overlay browser runs frameless.
func TestCommandPromptHasNoWindowBar(t *testing.T) {
	b := newExForTesting(t, texttest.NopEditor(), text.WithCommandKey(testCommandKey))
	defer b.Close()
	b.Resize(40, 20)

	b.ex.openCommandPrompt()
	require.NotNil(t, b.ex.cmd)

	w := term.NewStringWriter(40, 20)
	b.Draw(w)
	require.NoError(t, w.Flush())
	out := w.String()
	assert.NotContains(t, out, "█",
		"the command prompt must not render the window bar")
	assert.NotContains(t, out, "●",
		"the command prompt must not render the close icon")
	assert.Contains(t, out, "─",
		"the prompt keeps the configured frame charset")
	require.NoError(t, b.ex.cmdWin.Close())
}

// TestCommandPromptShaderGating verifies that the prompt shader is
// created only when commandPromptCfg.shader.enabled is set, and is
// torn down whenever the prompt closes.
func TestCommandPromptShaderGating(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		b := newExForTesting(t, texttest.NopEditor(),
			text.WithCommandKey(testCommandKey))
		defer b.Close()
		b.Resize(40, 20)
		b.ex.commandPromptCfg.shader.enabled = false
		b.ex.openCommandPrompt()
		require.NotNil(t, b.ex.cmd)
		assert.Nil(t, b.ex.promptShader,
			"promptShader must stay nil when disabled")
		require.NoError(t, b.ex.cmdWin.Close())
	})
	t.Run("enabled", func(t *testing.T) {
		b := newExForTesting(t, texttest.NopEditor(),
			text.WithCommandKey(testCommandKey))
		defer b.Close()
		b.Resize(40, 20)
		b.ex.commandPromptCfg.shader.enabled = true
		b.ex.openCommandPrompt()
		require.NotNil(t, b.ex.cmd)
		assert.NotNil(t, b.ex.promptShader,
			"promptShader must spawn when enabled")
		require.NoError(t, b.ex.cmdWin.Close())
		assert.Nil(t, b.ex.promptShader,
			"promptShader must be cleared on prompt close")
	})
}

// TestReplacingActivePromptClosesPreviousWindow pins that opening a
// second command prompt while the first is still active retires the old
// floating window (and its prompt) instead of orphaning it, without the
// old close callback clobbering the freshly installed prompt.
func TestReplacingActivePromptClosesPreviousWindow(t *testing.T) {
	b := newExForTesting(t, texttest.NopEditor(), text.WithCommandKey(testCommandKey))
	defer b.Close()
	b.Resize(40, 20)

	b.ex.openCommandPrompt()
	oldWin := b.ex.cmdWin
	oldCmd := b.ex.cmd
	require.NotNil(t, oldWin)
	require.NotNil(t, oldCmd)

	// Open a second prompt without manually closing the first.
	b.ex.openCommandPrompt()
	newWin := b.ex.cmdWin
	newCmd := b.ex.cmd
	require.NotNil(t, newWin)
	require.NotNil(t, newCmd)

	assert.True(t, oldWin.Closed(),
		"the superseded floating window must be closed")
	assert.NotSame(t, oldWin, newWin,
		"a new floating window must be installed")
	assert.NotSame(t, oldCmd, newCmd,
		"a new prompt must be installed")
	assert.False(t, newWin.Closed(),
		"the newly installed window must stay open")
	assert.Same(t, newCmd, b.ex.cmd,
		"e.cmd must reference the new prompt")

	// Closing the already-closed old window/callback must not clear the
	// new prompt state.
	require.NoError(t, oldWin.Close())
	assert.Same(t, newCmd, b.ex.cmd,
		"stale old-window close must not clear the new prompt")
	assert.Same(t, newWin, b.ex.cmdWin,
		"stale old-window close must not clear the new window")
}

// TestReplacingActivePromptStopsOldShader verifies that replacing an
// active shader-backed prompt tears down the old shader and installs a
// fresh one for the new prompt, leaving exactly one live shader.
func TestReplacingActivePromptStopsOldShader(t *testing.T) {
	b := newExForTesting(t, texttest.NopEditor(), text.WithCommandKey(testCommandKey))
	defer b.Close()
	b.Resize(40, 20)
	b.ex.commandPromptCfg.shader.enabled = true

	b.ex.openCommandPrompt()
	oldShader := b.ex.promptShader
	require.NotNil(t, oldShader)

	b.ex.openCommandPrompt()
	newShader := b.ex.promptShader
	require.NotNil(t, newShader,
		"the replacement prompt must have its own shader")
	assert.NotSame(t, oldShader, newShader,
		"the old shader must be replaced, not reused")

	require.NoError(t, b.ex.cmdWin.Close())
	assert.Nil(t, b.ex.promptShader,
		"closing the current prompt must clear its shader")
}

// TestReplacingActivePromptFromDispatch reproduces the reentrant ordering
// risk: the replacement prompt is opened from within command dispatch
// while the old prompt is still installed. The old window must still be
// retired and the new prompt must remain the active one.
func TestReplacingActivePromptFromDispatch(t *testing.T) {
	b := newExForTesting(t, texttest.NopEditor(), text.WithCommandKey(testCommandKey))
	defer b.Close()
	b.Resize(40, 20)

	b.ex.openCommandPrompt()
	oldWin := b.ex.cmdWin
	require.NotNil(t, oldWin)

	// Simulate a command handler that, while the old prompt is live,
	// installs a replacement (e.g. switching to the history prompt).
	require.NoError(t, b.ex.openCommandHistoryPrompt(context.Background()))
	newWin := b.ex.cmdWin
	newCmd := b.ex.cmd
	require.NotNil(t, newWin)
	require.NotNil(t, newCmd)

	assert.True(t, oldWin.Closed(), "the superseded window must be closed")
	assert.NotSame(t, oldWin, newWin, "a new window must be installed")
	assert.Same(t, newCmd, b.ex.cmd, "the new prompt must remain active")
}

// TestCloseCommandPrompt pins that closeCommandPrompt dismisses an open
// prompt — clearing e.cmd and marking its window closed — and is a
// no-op returning nil when no prompt is open. Menu-driven dispatch
// relies on this to reveal a command's own picker instead of leaving it
// behind the always-on-top prompt overlay.
func TestCloseCommandPrompt(t *testing.T) {
	b := newExForTesting(t, texttest.NopEditor(), text.WithCommandKey(testCommandKey))
	defer b.Close()
	b.Resize(40, 20)

	// No-op when nothing is open.
	require.Nil(t, b.ex.cmd)
	require.NoError(t, b.ex.closeCommandPrompt())

	b.ex.openCommandPrompt()
	win := b.ex.cmdWin
	require.NotNil(t, b.ex.cmd)
	require.NotNil(t, win)

	require.NoError(t, b.ex.closeCommandPrompt())
	assert.Nil(t, b.ex.cmd, "closing must clear the active prompt")
	assert.True(t, win.Closed(), "closing must retire the floating window")

	// Idempotent: a second call after the prompt is gone still succeeds.
	require.NoError(t, b.ex.closeCommandPrompt())
}

// TestCommandPromptClickOutsideDismisses pins that a mouse press
// outside the floating command prompt's screen rect dismisses it, like
// every other modal overlay in the IDE, while the click itself is
// swallowed. A press inside, a drag that starts inside and releases
// outside, and wheel events outside must not dismiss the prompt.
func TestCommandPromptClickOutsideDismisses(t *testing.T) {
	newPrompt := func(t *testing.T) (b testEx, pos term.Coordinates, width, height int) {
		b = newExForTesting(t, texttest.NopEditor(), text.WithCommandKey(testCommandKey))
		t.Cleanup(func() { _ = b.Close() })
		b.Resize(40, 20)
		b.ex.openCommandPrompt()
		b.Draw(term.NewStringWriter(40, 20))

		pos = b.ex.cmdWin.Position()
		off := b.ex.cmdV.Position()
		wmOff := b.ex.cmdV.C.WindowManagerPosition()
		pos.X += off.X + wmOff.X
		pos.Y += off.Y + wmOff.Y
		return b, pos, b.ex.cmdWin.Width(), b.ex.cmdWin.Height()
	}
	press := func(b testEx, key term.Key, x, y int) {
		b.Handle(term.Event{Type: term.EventMouse, Key: key, MouseX: x, MouseY: y})
	}

	t.Run("press outside dismisses", func(t *testing.T) {
		b, pos, _, _ := newPrompt(t)
		press(b, term.MouseLeft, pos.X-1, pos.Y)
		assert.Nil(t, b.ex.cmd)
	})
	t.Run("press inside keeps it open", func(t *testing.T) {
		b, pos, _, _ := newPrompt(t)
		press(b, term.MouseLeft, pos.X, pos.Y)
		assert.NotNil(t, b.ex.cmd)
	})
	t.Run("drag from inside released outside keeps it open", func(t *testing.T) {
		b, pos, width, _ := newPrompt(t)
		press(b, term.MouseLeft, pos.X, pos.Y)
		press(b, term.MouseLeft, pos.X+width+5, pos.Y)
		press(b, term.MouseRelease, pos.X+width+5, pos.Y)
		assert.NotNil(t, b.ex.cmd)
	})
	t.Run("wheel outside keeps it open", func(t *testing.T) {
		b, pos, _, _ := newPrompt(t)
		press(b, term.MouseWheelDown, pos.X-1, pos.Y)
		assert.NotNil(t, b.ex.cmd)
	})
	t.Run("held-button latch does not leak across prompts", func(t *testing.T) {
		b, pos, _, _ := newPrompt(t)
		// Press and hold inside, then dismiss the prompt through a
		// path other than releasing the mouse button.
		press(b, term.MouseLeft, pos.X, pos.Y)
		require.NoError(t, b.ex.closeCommandPrompt())

		b.ex.openCommandPrompt()
		b.Draw(term.NewStringWriter(40, 20))
		newPos := b.ex.cmdWin.Position()
		off := b.ex.cmdV.Position()
		wmOff := b.ex.cmdV.C.WindowManagerPosition()
		newPos.X += off.X + wmOff.X
		newPos.Y += off.Y + wmOff.Y

		press(b, term.MouseLeft, newPos.X-1, newPos.Y)
		assert.Nil(t, b.ex.cmd, "a stale held-button latch must not suppress the first outside click of a new prompt")
	})
}

// TestEchoPromptTogglesOpenPrompt pins that a bare trailing `{prompt}`
// toggles: it opens the prompt when none is active and closes an
// already-open one instead of replacing it, so a quick-menu button
// bound to `echo {prompt}` can both open and dismiss the prompt.
// Prefill bindings such as `echo {prompt}edit<space>` are not a
// toggle and must keep installing a fresh prompt.
func TestEchoPromptTogglesOpenPrompt(t *testing.T) {
	b := newExForTesting(t, texttest.NopEditor(), text.WithCommandKey(testCommandKey))
	defer b.Close()
	b.Resize(40, 20)

	require.NoError(t, b.ex.echo(bgctx, "{prompt}"))
	require.NotNil(t, b.ex.cmd)
	firstWin := b.ex.cmdWin

	require.NoError(t, b.ex.echo(bgctx, "{prompt}"))
	assert.Nil(t, b.ex.cmd, "a bare {prompt} on an open prompt must close it")
	assert.True(t, firstWin.Closed())

	require.NoError(t, b.ex.echo(bgctx, "{prompt}"))
	require.NotNil(t, b.ex.cmd, "a bare {prompt} on a closed prompt must reopen it")
	secondWin := b.ex.cmdWin

	require.NoError(t, b.ex.echo(bgctx, "{prompt}edit<space>"))
	assert.NotNil(t, b.ex.cmd, "a prefilled {prompt} is not a toggle")
	assert.NotSame(t, secondWin, b.ex.cmdWin, "a prefilled {prompt} must install a fresh prompt")
}

type closeCountingPartitionStore struct {
	storageapi.Service
	partitionCloseCount atomic.Int32
}

func (s *closeCountingPartitionStore) Partition(name string) (storageapi.Service, error) {
	partitioned, err := s.Service.Partition(name)
	if err != nil {
		return nil, err
	}
	return &closeCountingPartition{Service: partitioned, parent: s}, nil
}

type closeCountingPartition struct {
	storageapi.Service
	parent *closeCountingPartitionStore
}

func (s *closeCountingPartition) Partition(name string) (storageapi.Service, error) {
	partitioned, err := s.Service.Partition(name)
	if err != nil {
		return nil, err
	}
	return &closeCountingPartition{Service: partitioned, parent: s.parent}, nil
}

func (s *closeCountingPartition) Close() error {
	s.parent.partitionCloseCount.Add(1)
	return s.Service.Close()
}

func TestCloseOtherWindows(t *testing.T) {
	cases := []handlertest.SequenceTestCase{
		{":edit hello.go>:windowsplit>:windowsplit>:windowfocus left>:windowfocus left>",
			`┌━━━━━━━━━━────────┐
│o hello.go        │
┌────┐┌─────┐┌─────┤
│AAAA││     ││     │
│AAAA││     ││     │
│AAAA││     ││     │
│AAAA││     ││     │
│AAAA││     ││     │
│AAAA││     ││     │
└────┘└─────┘└─────┘`},
		{":windowcloseall>",
			`┌━━━━━━━━━━────────┐
│o hello.go        │
├──────────────────┤
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
└──────────────────┘`},
	}

	opts := []text.Option{
		text.WithCommandKey(testCommandKey),
	}
	b := newExForTesting(t, texttest.NopEditor(), opts...)
	defer b.Close()

	handlertest.TestHandlerSequence(t, b, 20, 10, cases)
}

func TestCommandAliases(t *testing.T) {
	cases := []handlertest.SequenceTestCase{
		{":todo>1234",
			`┌────────━━━━━━━───┐
│o a.go  o wi.go   │
├──────────────────┤
│EEEEEEEEEEEEEEEEEE│
│EEEEEEEEEEEEEEEEEE│
│EEEEEEEEEEEEEEEEEE│
│EEEEEEEEEEEEEEEEEE│
│EEEEEEEEEEEEEEEEEE│
│EEEEEEEEEEEEEEEEEE│
└──────────────────┘`},
		{":bp>",
			`┌━━━━━━────────────┐
│o a.go  o wi.go   │
├──────────────────┤
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
└──────────────────┘`},
		{":e x.go>",
			`┌──────────━━━━━━──┐
│o a  o w  o x.go  │
├──────────────────┤
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
└──────────────────┘`},
	}

	opts := []text.Option{
		text.WithCommandOverlayConfig(testCommandOverlayConfig()),
		text.WithCommandKey(testCommandKey),
		text.WithCommandAliases(map[string]text.CommandAlias{
			"todo": text.CommandAlias{Commands: []string{"edit a.go", "edit wi.go"}},
			"e":    text.CommandAlias{Commands: []string{"edit"}},
			"bp":   text.CommandAlias{Commands: []string{"tabnext"}},
		}),
	}
	b := newExForTesting(t, texttest.NopEditor(), opts...)
	defer b.Close()

	handlertest.TestHandlerSequence(t, b, 20, 10, cases)
}

func TestCommandPluginWait(t *testing.T) {
	t.Run("alias is missing arg", func(t *testing.T) {
		cases := []handlertest.SequenceTestCase{
			{"<c-\\\\>todo<enter>",
				`┌────┌─────────────┐
│    │ alias       │
├────│ expects an  │
│    │ argument    │
│    │ at          │
│    │ position 1  │
│    │ ($1)        │
│    └─────────────┘
│                  │
└──────────────────┘`},
		}

		opts := []text.Option{
			text.WithCommandOverlayConfig(testCommandOverlayConfig()),
			text.WithCommandKey(testCommandKey),
			text.WithCommandAliases(map[string]text.CommandAlias{
				"todo": {Commands: []string{
					"!! echo '$1'",
					"edit wi.go",
				}},
			}),
		}
		b := newExForTesting(t, texttest.NopEditor(), opts...)
		defer b.Close()

		handlertest.RunHandlerSequence(t, b, 20, 10, cases)
	})

	t.Run("command alias takes too long", func(t *testing.T) {
		cases := []handlertest.SequenceTestCase{
			{"<c-\\\\>todo<enter>",
				`┌────┌─────────────┐
│    │ !! sleep    │
├────│ 10:         │
│    │ command     │
│    │ was taking  │
│    │ too long    │
│    │ and so it   │
│    │ was         │
│    │ canceled    │
└────└─────────────┘`},
		}

		opts := []text.Option{
			text.WithCommandOverlayConfig(testCommandOverlayConfig()),
			text.WithCommandKey(testCommandKey),
			text.WithCommandAliases(map[string]text.CommandAlias{
				"todo": {Commands: []string{
					"!! sleep 10",
					"edit wi.go",
				}},
			}),
		}
		b := newExForTesting(t, texttest.NopEditor(), opts...)
		defer b.Close()
		// Override the production default so the test exercises the
		// timeout-cancelled branch within the test harness budget.
		b.ex.pluginWaitTimeout = 1 * time.Second

		handlertest.RunHandlerSequence(t, b, 20, 10, cases)
	})
}

// TestCommandPluginWaitDirectIsAsync pins the contract that a top-level
// (non-alias) `!!` command does NOT block the caller. executePluginWait
// only runs synchronously when the dispatch ctx is inside an alias chain
// (so a capturing step's vars land before the next step expands); a
// direct `:!! sleep 10` must hand off to a background goroutine and
// return immediately.
//
// Regression: withAliasChain previously wrapped every dispatch ctx with
// an alias chain, which made idealias.IsContext(ctx) report true for
// non-alias commands too and forced executePluginWait into the
// blocking branch.
func TestCommandPluginWaitDirectIsAsync(t *testing.T) {
	opts := []text.Option{
		text.WithCommandOverlayConfig(testCommandOverlayConfig()),
		text.WithCommandKey(testCommandKey),
	}
	b := newExForTesting(t, texttest.NopEditor(), opts...)
	defer b.Close()
	// pluginWaitTimeout bounds the background run; pick a value much
	// larger than the time dispatchCommand itself is allowed to take.
	b.ex.pluginWaitTimeout = 5 * time.Second

	start := time.Now()
	err := b.ex.dispatchCommand("!!", "sleep", "10")
	elapsed := time.Since(start)

	require.NoError(t, err)
	require.Less(t, elapsed, 500*time.Millisecond,
		"direct !! must dispatch to a goroutine and return immediately")
}

// TestCommandPluginWaitInflightNotification pins the UX contract that a
// `!!` command surfaces a progress-anchored Info notification while it
// runs, closes it (progress == total) on completion, and then posts a
// terminal success or error notification.
//
// The notification stays open for the duration of the run because the
// runtime keeps progress notifications visible until progress == total
// is observed. Without this anchor, slow commands give the user no
// feedback that anything is happening.
func TestCommandPluginWaitInflightNotification(t *testing.T) {
	opts := []text.Option{
		text.WithCommandOverlayConfig(testCommandOverlayConfig()),
		text.WithCommandKey(testCommandKey),
	}
	b := newExForTesting(t, texttest.NopEditor(), opts...)
	defer b.Close()
	rec := &pluginWaitNotifications{inner: b.ex.notifications}
	b.ex.notifications = rec
	b.ex.pluginWaitTimeout = 5 * time.Second

	require.NoError(t, b.ex.dispatchCommand("!!", "echo", "hi"))

	// The async branch posts the running notification + initial 0/1
	// progress synchronously before spawning the goroutine, so by the
	// time dispatchCommand returns those must already be visible.
	rec.assertInflightSeen(t)

	// The closing progress and terminal notification hop through
	// cfg.ScheduleNextTick. The queued test scheduler holds those
	// callbacks until we drain.
	require.Eventually(t, func() bool {
		b.flushScheduled()
		return rec.terminalReached()
	}, 2*time.Second, 10*time.Millisecond,
		"expected closing 1/1 progress and a terminal LevelSuccess notification")
}

// pluginWaitNotifications captures the Notify / progress sequence the
// ex executePluginWait path produces so tests can assert UX without
// rendering the floating notifications UI.
type pluginWaitNotifications struct {
	inner    browserapi.Notifications
	mu       sync.Mutex
	notifies []pluginWaitNote
	progress []pluginWaitProgress
}

type pluginWaitNote struct {
	id    string
	level browserapi.NotificationLevel
	msg   string
}

type pluginWaitProgress struct {
	id              string
	progress, total int64
}

func (r *pluginWaitNotifications) Notify(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	id, err := r.inner.Notify(level, msg, args...)
	r.mu.Lock()
	r.notifies = append(r.notifies, pluginWaitNote{
		id: id, level: level, msg: fmt.Sprintf(msg, args...),
	})
	r.mu.Unlock()
	return id, err
}

func (r *pluginWaitNotifications) NotifyOnce(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	return r.Notify(level, msg, args...)
}

func (r *pluginWaitNotifications) UpdateNotificationProgress(
	id, message string, progress, total int64,
) error {
	err := r.inner.UpdateNotificationProgress(id, message, progress, total)
	r.mu.Lock()
	r.progress = append(r.progress, pluginWaitProgress{
		id: id, progress: progress, total: total,
	})
	r.mu.Unlock()
	return err
}

func (r *pluginWaitNotifications) assertInflightSeen(t *testing.T) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	require.NotEmpty(t, r.notifies,
		"expected an inflight Notify call before dispatchCommand returned")
	require.Equal(t, browserapi.LevelInfo, r.notifies[0].level)
	require.NotEmpty(t, r.progress,
		"expected an initial progress update anchoring the notification")
	require.Equal(t, int64(0), r.progress[0].progress)
	require.Equal(t, int64(1), r.progress[0].total)
	require.Equal(t, r.notifies[0].id, r.progress[0].id,
		"the initial progress must target the inflight notification id")
}

func (r *pluginWaitNotifications) terminalReached() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.progress) < 2 {
		return false
	}
	last := r.progress[len(r.progress)-1]
	if last.progress != 1 || last.total != 1 {
		return false
	}
	for _, nf := range r.notifies[1:] {
		if nf.level == browserapi.LevelSuccess ||
			nf.level == browserapi.LevelError {
			return true
		}
	}
	return false
}

// errorMessages returns every LevelError notification body seen so far.
func (r *pluginWaitNotifications) errorMessages() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, nf := range r.notifies {
		if nf.level == browserapi.LevelError {
			out = append(out, nf.msg)
		}
	}
	return out
}

func TestIntegrationEphemeralTerminal(t *testing.T) {
	cases := []handlertest.SequenceTestCase{
		{":! sleep 20>",
			`┌──────────────────────────────────────┐
│                                      │
├──█●███████████ sleep 20 ███████████──┤
│  │ ▀                            0s│  │
│  │▐                               │  │
│  │                                │  │
│  │                                │  │
│  │                                │  │
│  │                                │  │
└──└────────────────────────────────┘──┘`,
		},
		{":tabclose>",
			`┌──────────────────────────────────────┐
│                                      │
├──────────────────────────────────────┤
│                                      │
│                                      │
│                                      │
│                                      │
│                                      │
│                                      │
└──────────────────────────────────────┘`,
		},
		{":! sleep 20>",
			`┌──────────────────────────────────────┐
│                                      │
├──█●███████████ sleep 20 ███████████──┤
│  │ ▀                            0s│  │
│  │▐                               │  │
│  │                                │  │
│  │                                │  │
│  │                                │  │
│  │                                │  │
└──└────────────────────────────────┘──┘`,
		},
		{":windowclose>",
			`┌──────────────────────────────────────┐
│                                      │
├──────────────────────────────────────┤
│                                      │
│                                      │
│                                      │
│                                      │
│                                      │
│                                      │
└──────────────────────────────────────┘`,
		},
		{":! sleep 20>",
			`┌──────────────────────────────────────┐
│                                      │
├──█●███████████ sleep 20 ███████████──┤
│  │ ▀                            0s│  │
│  │▐                               │  │
│  │                                │  │
│  │                                │  │
│  │                                │  │
│  │                                │  │
└──└────────────────────────────────┘──┘`,
		},
		{":windowcloseall>",
			// windowcloseall clears floating windows too, so the
			// ephemeral terminal goes away with everything else.
			`┌──────────────────────────────────────┐
│                                      │
├──────────────────────────────────────┤
│                                      │
│                                      │
│                                      │
│                                      │
│                                      │
│                                      │
└──────────────────────────────────────┘`,
		},
		{":noticloseall>:! sh -c 'sleep 20 && echo $FILE'>",
			`┌──────────────────────────────────────┐
│                                      │
├──█●█ sh -c 'sh -c sleep 20 && echo█──┤
│  │ ▀                            0s│  │
│  │▐                               │  │
│  │                                │  │
│  │                                │  │
│  │                                │  │
│  │                                │  │
└──└────────────────────────────────┘──┘`,
		},
		{":windowclose>:windowclose>:edit a>:! sh -c 'sleep 20 && echo $FILE'>",
			`┌──────────────────────────────────────┐
│o a                                   │
├──█●█ sh -c 'sh -c sleep 20 && echo█──┤
│  │ ▀                            0s│  │
│  │▐                               │  │
│  │                                │  │
│  │                                │  │
│  │                                │  │
│  │                                │  │
└──└────────────────────────────────┘──┘`,
		},
	}

	opts := []text.Option{
		text.WithCommandKey(testCommandKey),
		text.WithCommandOverlayConfig(testCommandOverlayConfig()),
		text.WithEventPublisher(nopPublishEvent),
		text.WithFloatingNoMaxSize(false),
	}

	tempDir, err := os.MkdirTemp("", "")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = os.RemoveAll(tempDir)
	})
	uri, err := workspaceapi.ParseURI(filepath.Join("file://", tempDir))
	require.NoError(t, err)
	ctx := context.Background()
	fileScheme, err := workspace.NewFileScheme(ctx, config.NopConfig(), uri)
	require.NoError(t, err)
	defer fileScheme.Close()

	workspace := workspace.NewSchemeWorkspace(uri, fileScheme, inlineSchedule)
	b := newExForTestingTerminal(t, workspace,
		standard.Editor(),
		vte.DefaultConfig(), nopPublishEvent, plugin.DefaultBarConfig(), opts...)
	defer b.Close()

	handlertest.TestHandlerSequence(t, b, 40, 10, cases)
}

// TestWindowMouseResizeIntegration asserts that dragging a floating
// window's bar and edges through the full ex stack (browser frame
// union included) moves and resizes the window.
func TestWindowMouseResizeIntegration(t *testing.T) {
	newFloatEx := func(t *testing.T) testEx {
		opts := []text.Option{
			text.WithCommandKey(testCommandKey),
			text.WithCommandOverlayConfig(testCommandOverlayConfig()),
			text.WithEventPublisher(nopPublishEvent),
			text.WithFloatingNoMaxSize(false),
		}
		tempDir, err := os.MkdirTemp("", "")
		require.NoError(t, err)
		t.Cleanup(func() { _ = os.RemoveAll(tempDir) })
		uri, err := workspaceapi.ParseURI(filepath.Join("file://", tempDir))
		require.NoError(t, err)
		fileScheme, err := workspace.NewFileScheme(
			context.Background(), config.NopConfig(), uri)
		require.NoError(t, err)
		t.Cleanup(func() { fileScheme.Close() })
		ws := workspace.NewSchemeWorkspace(uri, fileScheme, inlineSchedule)
		b := newExForTestingTerminal(t, ws, standard.Editor(),
			vte.DefaultConfig(), nopPublishEvent, plugin.DefaultBarConfig(), opts...)
		t.Cleanup(func() { _ = b.Close() })

		b.Resize(40, 10)
		handleTaskInputSequence(b, ":! sleep 20>")
		b.Draw(term.NewStringWriter(40, 10))
		return b
	}
	// the main window manager's row 0 sits below the two tab bar rows
	const wmTop = 2
	mouse := func(b testEx, key term.Key, x, y int) {
		b.Handle(term.Event{Type: term.EventMouse, Key: key, MouseX: x, MouseY: y})
	}
	floatWin := func(t *testing.T, b testEx) browser.Window {
		win := b.ex.comp.Browser().Focus()
		require.True(t, win.IsFloating())
		require.Equal(t, 34, win.Width())
		require.Equal(t, 8, win.Height())
		require.Equal(t, term.Coordinates{X: 3, Y: 0}, win.Position())
		return win
	}

	t.Run("right edge drag grows the float", func(t *testing.T) {
		b := newFloatEx(t)
		win := floatWin(t, b)
		mouse(b, term.MouseLeft, 36, wmTop+3)
		mouse(b, term.MouseLeft, 38, wmTop+3)
		mouse(b, term.MouseRelease, 38, wmTop+3)
		assert.Equal(t, 36, win.Width())
	})
	t.Run("bottom edge drag shrinks the float", func(t *testing.T) {
		b := newFloatEx(t)
		win := floatWin(t, b)
		mouse(b, term.MouseLeft, 20, wmTop+7)
		mouse(b, term.MouseLeft, 20, wmTop+5)
		mouse(b, term.MouseRelease, 20, wmTop+5)
		assert.Equal(t, 6, win.Height())
	})
	t.Run("right edge drag shrinks the float", func(t *testing.T) {
		b := newFloatEx(t)
		win := floatWin(t, b)
		mouse(b, term.MouseLeft, 36, wmTop+3)
		mouse(b, term.MouseLeft, 30, wmTop+3)
		mouse(b, term.MouseRelease, 30, wmTop+3)
		assert.Equal(t, 28, win.Width())
	})
	t.Run("left edge drag keeps the right edge fixed", func(t *testing.T) {
		b := newFloatEx(t)
		win := floatWin(t, b)
		mouse(b, term.MouseLeft, 3, wmTop+3)
		mouse(b, term.MouseLeft, 1, wmTop+3)
		mouse(b, term.MouseRelease, 1, wmTop+3)
		assert.Equal(t, 36, win.Width())
		assert.Equal(t, term.Coordinates{X: 1, Y: 0}, win.Position())
	})
	t.Run("bar drag moves the float", func(t *testing.T) {
		b := newFloatEx(t)
		win := floatWin(t, b)
		// the float spans the full manager height; shrink it first so
		// there is room to move vertically
		mouse(b, term.MouseLeft, 20, wmTop+7)
		mouse(b, term.MouseLeft, 20, wmTop+5)
		mouse(b, term.MouseRelease, 20, wmTop+5)
		require.Equal(t, 6, win.Height())
		mouse(b, term.MouseLeft, 20, wmTop)
		mouse(b, term.MouseLeft, 19, wmTop+1)
		mouse(b, term.MouseRelease, 19, wmTop+1)
		assert.Equal(t, term.Coordinates{X: 2, Y: 1}, win.Position())
	})
}

func TestIntegrationCompanionTerminal(t *testing.T) {

	cases := []handlertest.SequenceTestCase{
		{":!>_______",
			`┌──────────────────┐
│                  │
├█●████████████████┤
││sh ▐            ││
││                ││
││                ││
││                ││
││                ││
││                ││
└└────────────────┘┘`,
		},
		{"<$:noticloseall>",
			`┌──────────────────┐
│                  │
├──────────────────┤
│                  │
│                  │
│                  │
│                  │
│                  │
│                  │
└──────────────────┘`,
		},
		{":!>", // no need to wait now, it should pick previous session
			`┌──────────────────┐
│                  │
├█●████████████████┤
││sh ▐            ││
││                ││
││                ││
││                ││
││                ││
││                ││
└└────────────────┘┘`,
		},
		{"#:noticloseall>",
			`┌──────────────────┐
│                  │
├──────────────────┤
│                  │
│                  │
│                  │
│                  │
│                  │
│                  │
└──────────────────┘`,
		},
		{":!>`", // ` simulates ctrl-v
			`┌──────────────────┐
│                  │
├──────────────────┤
│                  │
│                  │
│                  │
│                  │
│                  │
│                  │
└──────────────────┘`,
		},
		{":!>:windowconverttab companion X>", // ` simulates ctrl-v
			`┌━━━━━━━━━━━───────┐
│X companion       │
├──────────────────┤
│sh ▐              │
│                  │
│                  │
│                  │
│                  │
│                  │
└──────────────────┘`,
		},
		{":!>:!>",
			`┌━━━━━━━━━━━───────┐
│X companion       │
├──────────────────┤
│sh ▐              │
│                  │
│                  │
│                  │
│                  │
│                  │
└──────────────────┘`,
		},
	}

	opts := []text.Option{
		text.WithCommandKey(testCommandKey),
		text.WithCommandKeyBinding(term.KeyComb{Mod: term.ModCtrl, Ch: 'h'},
			[][]string{{"windowclose"}}),
		text.WithCommandKeyBinding(term.KeyComb{Mod: term.ModCtrl, Ch: 'l'},
			[][]string{{"tabnext"}}),
		text.WithCommandKeyBinding(term.KeyComb{Mod: term.ModCtrl, Ch: 'v'},
			[][]string{{"tabclose"}}),
		text.WithCommandOverlayConfig(testCommandOverlayConfig()),
		text.WithFloatingNoMaxSize(false),
	}

	tempDir, err := os.MkdirTemp("", "")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = os.RemoveAll(tempDir)
	})
	uri, err := workspaceapi.ParseURI(filepath.Join("file://", tempDir))
	require.NoError(t, err)
	ctx := context.Background()
	fileScheme, err := workspace.NewFileScheme(ctx, config.NopConfig(), uri)
	require.NoError(t, err)
	defer fileScheme.Close()

	// do not depend on host shell, which can vary across hosts
	cfg := vte.DefaultConfig()
	cfg.CommandAndArgs = []string{"sh"}

	// do not depend on default shell prompt, as it can change
	// and it does change accross versions
	ps1 := os.Getenv("PS1")
	os.Setenv("PS1", "sh ")
	defer os.Setenv("PS1", ps1)

	workspace := workspace.NewSchemeWorkspace(uri, fileScheme, inlineSchedule)
	b := newExForTestingTerminal(t, workspace,
		texttest.NopEditor(), cfg, nopPublishEvent, plugin.DefaultBarConfig(), opts...)
	defer b.Close()

	handlertest.TestHandlerSequence(t, b, 20, 10, cases)
}

func TestTerminalWriteOpensSavePrompt(t *testing.T) {
	b := newExForTesting(t, texttest.NopEditor())
	defer b.Close()

	require.NoError(t, b.ex.terminalnew(context.Background(), "terminal output"))
	require.NoError(t, b.ex.flush(context.Background()))
	require.Nil(t, b.ex.cmd)

	var doc terminalSessionDocument
	require.NoError(t, b.ex.terminalSessionStorage().Get(context.Background(),
		terminalSessionDocumentID("terminal-saved"), &doc))
	require.Equal(t, "terminal-saved", doc.Name)
	require.Equal(t, terminalSessionDocumentKind, doc.Kind,
		"saved sessions must carry the kind the completer filters on")
	require.Contains(t, term.CellsToString(doc.Snapshot.ActiveCells()), "terminal output")
}

func TestTerminalNewForwardsShellArgument(t *testing.T) {
	type call struct {
		name string
		fn   func(b *testEx, args ...string) error
	}
	calls := []call{
		{
			name: "terminalnew",
			fn: func(b *testEx, args ...string) error {
				return b.ex.terminalnew(context.Background(), args...)
			},
		},
		{
			name: "terminalnewtab",
			fn: func(b *testEx, args ...string) error {
				return b.ex.terminalnewtab(context.Background(), args...)
			},
		},
		{
			name: "terminalneworsplit",
			fn: func(b *testEx, args ...string) error {
				return b.ex.terminalneworsplit(context.Background(), args...)
			},
		},
	}
	cases := []struct {
		name     string
		args     []string
		expected []string
	}{
		{name: "no args", args: nil, expected: nil},
		{name: "shell only", args: []string{"zsh"}, expected: []string{"zsh"}},
		{name: "shell with flags", args: []string{"zsh", "-i"}, expected: []string{"zsh", "-i"}},
	}
	for _, c := range calls {
		for _, tc := range cases {
			t.Run(c.name+"/"+tc.name, func(t *testing.T) {
				b := newExForTesting(t, texttest.NopEditor())
				defer b.Close()
				var captured []string
				var captureCalled bool
				b.ex.newEmulatorHandler = func(args []string) (vtereservoir.VTE, error) {
					captureCalled = true
					captured = append([]string(nil), args...)
					h := newTestVteWithConfig(args)
					uri, err := workspaceapi.ParseURI(
						fmt.Sprintf("terminaltest:///%s/%s", c.name, tc.name))
					if err != nil {
						return nil, err
					}
					h.uri = uri
					return h, nil
				}
				require.NoError(t, c.fn(&b, tc.args...))
				require.True(t, captureCalled)
				require.Equal(t, tc.expected, captured)
			})
		}
	}
}

func TestEmulatorHandlerUsesReservoir(t *testing.T) {
	// Verifies the fix for RUNE-129: when an ex is configured with a
	// non-zero initialVTECapacity, the warm reservoir is consulted on
	// terminalnew/terminalnewtab even when the call passes shell
	// arguments that match the configured shell.
	type call struct {
		name string
		fn   func(b *testEx, args ...string) error
	}
	calls := []call{
		{
			name: "terminalnew",
			fn: func(b *testEx, args ...string) error {
				return b.ex.terminalnew(context.Background(), args...)
			},
		},
		{
			name: "terminalnewtab",
			fn: func(b *testEx, args ...string) error {
				return b.ex.terminalnewtab(context.Background(), args...)
			},
		},
	}
	cases := []struct {
		name            string
		args            []string
		expectFromPool  bool
		configuredShell []string
	}{
		{name: "no args", args: nil, expectFromPool: true,
			configuredShell: []string{"sh"}},
		{name: "args match shell", args: []string{"sh"}, expectFromPool: true,
			configuredShell: []string{"sh"}},
		{name: "args match shell with flags", args: []string{"sh", "-i"},
			expectFromPool: true, configuredShell: []string{"sh", "-i"}},
		{name: "args differ from shell", args: []string{"echo", "hi"},
			expectFromPool: false, configuredShell: []string{"sh"}},
	}
	for _, c := range calls {
		for _, tc := range cases {
			t.Run(c.name+"/"+tc.name, func(t *testing.T) {
				b := newExForReservoirTesting(t, tc.configuredShell, 1)
				defer b.Close()

				before := b.reservoirGets.Load()
				newCallsBefore := b.newCalls.Load()
				require.NoError(t, c.fn(&b.testEx, tc.args...))
				gotFromPool := b.reservoirGets.Load() > before
				assert.Equal(t, tc.expectFromPool, gotFromPool,
					"reservoir Get count: before=%d after=%d",
					before, b.reservoirGets.Load())
				if tc.expectFromPool {
					// Reservoir served the request: no fresh from-scratch
					// call must have been observed by the closure.
					assert.Equal(t, newCallsBefore, b.newCalls.Load())
				}
			})
		}
	}
}

func TestSetExecutorPreservesReservoirCapacity(t *testing.T) {
	// Verifies the fix for RUNE-129: setExecutor must preserve the
	// configured initial capacity when re-creating the reservoir, and
	// must not race the in-flight initCap by reading Capacity().
	b := newExForReservoirTesting(t, []string{"sh"}, 3)
	defer b.Close()

	require.Equal(t, 3, b.ex.initialReservoirCapacity)

	// Before setExecutor, drain the reservoir's pending initCap so we
	// have a known starting state, then snapshot the configured
	// capacity.
	first := b.ex.reservoir
	require.NotNil(t, first)

	// Trigger setExecutor with the original executor; the new
	// reservoir must come up with the originally configured capacity,
	// regardless of what the (just-closed) old reservoir reports.
	b.ex.setExecutor(b.ex.executor.get(), b.ex.wsExecutor, b.ex.extensionsExecutor)

	require.NotNil(t, b.ex.reservoir)
	assert.NotSame(t, first, b.ex.reservoir)
	assert.Equal(t, 3, b.ex.reservoir.InitialCapacity())
}

type reservoirTestEx struct {
	testEx
	reservoirGets *atomic.Int64
	newCalls      *atomic.Int64
}

func newExForReservoirTesting(
	t *testing.T, shell []string, initialCapacity int,
) reservoirTestEx {
	t.Helper()

	uri, err := workspaceapi.ParseURI("file:///tmp")
	require.NoError(t, err)
	scheme, err := workspacetest.NewNopScheme("file:///tmp")(
		context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	ws := workspace.NewSchemeWorkspace(uri, scheme, inlineSchedule)

	emCfg := vte.DefaultConfig()
	emCfg.CommandAndArgs = shell

	e := new(ex)
	e.syncCommandPrompt = true
	finalOpts := defCommandKeyBindings()
	finalOpts = append(finalOpts, text.WithCommandOverlayConfig(testCommandOverlayConfig()))
	finalOpts = append(finalOpts, text.WithFloatingNoMaxSize(false))

	svc := storagestub.NewInMemoryService()
	notifications := newWorkspaceNotifications(svc, notificationsConfig(),
		&workspaceManagerMock{workspace: e})

	require.NoError(t, e.init(func(exoeditor.Reloader, schemeapi.Terminal) (text.Editor, error) { return texttest.NopEditor(), nil }, ws, svc,
		notifications, uri, emCfg, plugin.DefaultBarConfig(),
		nopPublishEvent, initialCapacity, clipboard.NewInMemory(),
		nil, nil, nil, nil, testPromptEditor(), finalOpts...))
	require.NoError(t, e.subscribeCommands())

	// Wrap the closure created by init so we can observe which branch
	// the production logic would have taken. The wrapper mirrors the
	// real routing in ex.init exactly.
	var reservoirGets atomic.Int64
	var newCalls atomic.Int64
	e.newEmulatorHandler = func(args []string) (vtereservoir.VTE, error) {
		if e.reservoir != nil && argsMatchEmulatorShell(args, e.emulatorConfig.CommandAndArgs) {
			reservoirGets.Add(1)
		} else {
			newCalls.Add(1)
		}
		// Substitute a stub VTE so we don't actually start a process.
		return newTestVteWithConfig(args), nil
	}
	e.pluginWaitTimeout = 1 * time.Second

	return reservoirTestEx{
		testEx:        testEx{ex: e},
		reservoirGets: &reservoirGets,
		newCalls:      &newCalls,
	}
}

func TestTerminalWriteUsesNextAvailableName(t *testing.T) {
	b := newExForTesting(t, texttest.NopEditor())
	defer b.Close()

	require.NoError(t, b.ex.terminalnew(context.Background(), "terminal output"))
	require.NoError(t, b.ex.flush(context.Background()))
	require.NoError(t, b.ex.flush(context.Background()))

	var doc terminalSessionDocument
	require.NoError(t, b.ex.terminalSessionStorage().Get(context.Background(),
		terminalSessionDocumentID("terminal-saved-1"), &doc))
	require.Equal(t, "terminal-saved-1", doc.Name)
}

func TestTerminalSaveAndResume(t *testing.T) {
	b := newExForTesting(t, texttest.NopEditor())
	defer b.Close()

	require.NoError(t, b.ex.terminalnew(context.Background(), "terminal output"))
	require.NoError(t, b.ex.terminalsave(context.Background(), "demo"))
	require.NoError(t, b.ex.terminalresume(context.Background(), "demo"))

	content, err := b.ex.invokeWindow().Content()
	require.NoError(t, err)
	tab, ok := content.(*browser.Tab)
	require.True(t, ok)
	session, ok := tab.Handler().(*testVte)
	require.True(t, ok)
	require.True(t, session.restoredSnapshot)
	require.Contains(t, session.initialCmd, "terminal output")
	cursor, _, _ := session.Cursor()
	require.Equal(t, term.Coordinates{X: 4, Y: 1}, cursor)
	require.Equal(t, 2, session.SeekOffset())
}

// TestTerminalResumeFindsSessionsSavedBeforeThePartitionMove covers
// sessions written when they shared the workspace-state partition.
func TestTerminalResumeFindsSessionsSavedBeforeThePartitionMove(t *testing.T) {
	b := newExForTesting(t, texttest.NopEditor())
	defer b.Close()

	require.NoError(t, b.ex.storage.Set(context.Background(),
		terminalSessionDocumentID("legacy"), terminalSessionDocument{
			Name:     "legacy",
			Snapshot: vte.Snapshot{Schema: 1, Title: "old"},
		}))
	require.NoError(t, b.ex.terminalresume(context.Background(), "legacy"))

	content, err := b.ex.invokeWindow().Content()
	require.NoError(t, err)
	tab, ok := content.(*browser.Tab)
	require.True(t, ok)
	session, ok := tab.Handler().(*testVte)
	require.True(t, ok)
	require.True(t, session.restoredSnapshot)

	var moved terminalSessionDocument
	require.NoError(t, b.ex.terminalSessionStorage().Get(context.Background(),
		terminalSessionDocumentID("legacy"), &moved))
	require.Equal(t, terminalSessionDocumentKind, moved.Kind)
	require.ErrorIs(t, b.ex.storage.Get(context.Background(),
		terminalSessionDocumentID("legacy"), &moved), storageapi.ErrNotFound,
		"a resumed legacy session must leave the listed partition")

	require.ErrorIs(t,
		b.ex.terminalresume(context.Background(), "missing"), storageapi.ErrNotFound)
}

func TestOpenTerminalSessionsPersistAndRestore(t *testing.T) {
	b := newExForTesting(t, texttest.NopEditor())
	defer b.Close()
	var terminalID int
	b.ex.newEmulatorHandler = func(args []string) (vtereservoir.VTE, error) {
		terminalID++
		h := newTestVteWithConfig(args)
		uri, err := workspaceapi.ParseURI(fmt.Sprintf("terminaltest:///%d", terminalID))
		if err != nil {
			return nil, err
		}
		h.uri = uri
		return h, nil
	}

	require.NoError(t, b.ex.terminalnewtab(context.Background(), "first terminal"))
	require.NoError(t, b.ex.terminalnewtab(context.Background(), "second terminal"))
	sessions := exSnapshotter{ex: b.ex}.Terminals()
	require.Len(t, sessions, 2)
	require.Contains(t, term.CellsToString(sessions[0].Snapshot.ActiveCells()), "first terminal")
	require.Contains(t, term.CellsToString(sessions[1].Snapshot.ActiveCells()), "second terminal")

	restored := newExForTesting(t, texttest.NopEditor())
	defer restored.Close()
	require.NoError(t, restoreOpenTerminalSessions(restored.ex, sessions, nil))

	tabs := restored.ex.comp.Tabs()
	require.Len(t, tabs, 2)

	var restoredCommands []string
	for _, tab := range tabs {
		session, ok := tab.Handler().(*testVte)
		require.True(t, ok)
		require.True(t, session.restoredSnapshot)
		restoredCommands = append(restoredCommands, session.initialCmd)
	}
	require.ElementsMatch(t, []string{"first terminal", "second terminal"}, restoredCommands)
}

func TestTerminalSessionCompletionListsUserSavedSessions(t *testing.T) {
	b := newExForTesting(t, texttest.NopEditor())
	defer b.Close()

	require.NoError(t, b.ex.terminalnew(context.Background(), "terminal output"))
	require.NoError(t, b.ex.terminalsave(context.Background(), "manual"))

	it, _, err := b.ex.completeTerminalSessions(context.Background(), textapi.Command{})
	require.NoError(t, err)
	defer it.Close()

	var names []string
	for {
		name, ok := it.Next(context.Background())
		if !ok {
			break
		}
		names = append(names, name)
	}
	require.NoError(t, it.Err())

	require.Equal(t, []string{"manual"}, names)
}

func TestTerminalSaveCommandRequiresTerminal(t *testing.T) {
	b := newExForTesting(t, texttest.NopEditor())
	defer b.Close()

	err := b.ex.terminalsave(context.Background(), "demo")
	require.EqualError(t, err, "not a terminal")
}

type closeCountingStorage struct {
	storageapi.Service
	partitionCalls int
	closeCalls     int
	partitions     map[string]*closeCountingPartitionStorage
}

func (s *closeCountingStorage) Partition(name string) (storageapi.Service, error) {
	s.partitionCalls++
	if s.partitions == nil {
		s.partitions = make(map[string]*closeCountingPartitionStorage)
	}
	svc, err := s.Service.Partition(name)
	if err != nil {
		return nil, err
	}
	s.partitions[name] = &closeCountingPartitionStorage{Service: svc}
	return s.partitions[name], nil
}

func (s *closeCountingStorage) Close() error {
	s.closeCalls++
	return s.Service.Close()
}

type closeCountingPartitionStorage struct {
	storageapi.Service
	closeCalls int
}

func (s *closeCountingPartitionStorage) Close() error {
	s.closeCalls++
	return s.Service.Close()
}

func TestExUsesSharedIDEStorage(t *testing.T) {
	storage := &closeCountingStorage{Service: storagestub.NewInMemoryService()}
	workspace := &testLoader{}
	ex := new(ex)
	ex.syncCommandPrompt = true
	notifications := newWorkspaceNotifications(storagestub.NewInMemoryService(),
		notificationsConfig(), &workspaceManagerMock{workspace: ex})
	uri, err := workspace.URI(".")
	require.NoError(t, err)
	require.NoError(t, ex.init(func(exoeditor.Reloader, schemeapi.Terminal) (text.Editor, error) { return texttest.NopEditor(), nil }, workspace, storage,
		notifications, uri, vte.DefaultConfig(), plugin.DefaultBarConfig(),
		nopPublishEvent, 0, clipboard.NewInMemory(), nil, nil, nil, nil, testPromptEditor()))

	require.Equal(t, 0, storage.partitionCalls)
	require.Same(t, storage, ex.storage)

	// Saved terminal sessions live in their own partition, opened on
	// demand and released with the ex so cached backend handles are not
	// leaked per workspace.
	_, _, err = ex.completeTerminalSessions(context.Background(), textapi.Command{})
	require.NoError(t, err)
	require.Equal(t, 1, storage.partitionCalls)
	terminals := storage.partitions[idehistory.TerminalStatePartition]
	require.NotNil(t, terminals)

	require.NoError(t, ex.Close())
	require.Equal(t, 0, storage.closeCalls)
	require.Equal(t, 1, terminals.closeCalls)
	require.NoError(t, ex.Close())
	require.Equal(t, 0, storage.closeCalls)
	require.Equal(t, 1, terminals.closeCalls)
}

func TestFullScreen(t *testing.T) {
	cases := []handlertest.SequenceTestCase{
		{":windowsplit>:edit aaa>:edit bbb>:windowtogglemaximize>",
			`┌───────━━━━━──────┐
│o aaa  o bbb      │
├─┐┌───────────────┐
│ ││AAAAAAAAAAAAAAA│
│ ││AAAAAAAAAAAAAAA│
│ ││AAAAAAAAAAAAAAA│
│ ││AAAAAAAAAAAAAAA│
│ ││AAAAAAAAAAAAAAA│
│ ││AAAAAAAAAAAAAAA│
└─┘└───────────────┘`,
		},
		{":windowmax>",
			`┌───────━━━━━──────┐
│o aaa  o bbb      │
├────────┐┌────────┐
│        ││AAAAAAAA│
│        ││AAAAAAAA│
│        ││AAAAAAAA│
│        ││AAAAAAAA│
│        ││AAAAAAAA│
│        ││AAAAAAAA│
└────────┘└────────┘`,
		},
	}

	opts := []text.Option{
		text.WithCommandKey(testCommandKey),
		text.WithCommandOverlayConfig(testCommandOverlayConfig()),
	}

	tempDir, err := os.MkdirTemp("", "")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = os.RemoveAll(tempDir)
	})
	uri, err := workspaceapi.ParseURI(filepath.Join("file://", tempDir))
	require.NoError(t, err)
	ctx := context.Background()
	fileScheme, err := workspace.NewFileScheme(ctx, config.NopConfig(), uri)
	require.NoError(t, err)
	defer fileScheme.Close()

	workspace := workspace.NewSchemeWorkspace(uri, fileScheme, inlineSchedule)
	b := newExForTestingWithWorkspace(t, workspace,
		texttest.NopEditor(), vte.DefaultConfig(), nopPublishEvent,
		clipboard.NewInMemory(), opts...)
	defer b.Close()

	handlertest.TestHandlerSequence(t, b, 20, 10, cases)
}

func TestFocusOtherWindow(t *testing.T) {
	uri, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)
	scheme, _ := workspace.NewMemoryScheme(context.Background(), config.NopConfig(), uri)
	ex := newExForTestingWithWorkspace(t, workspace.NewSchemeWorkspace(uri, scheme, inlineSchedule),
		texttest.NopEditor(), vte.DefaultConfig(), nopPublishEvent, clipboard.NewInMemory())
	t.Cleanup(func() { _ = ex.Close() })
	ex.Resize(100, 100)

	first := ex.invokeWindow()
	require.Error(t, ex.windowfocus(bgctx, "sideways"))
	require.Equal(t, first, ex.invokeWindow())

	ex.windownew(bgctx)
	second := ex.invokeWindow()
	require.NotEqual(t, first, second)

	require.NoError(t, ex.windowfocus(bgctx, "other"))
	require.Equal(t, first, ex.invokeWindow())
	require.NoError(t, ex.windowfocus(bgctx, "other"))
	require.Equal(t, second, ex.invokeWindow())
}

func TestMoveWindowContent(t *testing.T) {
	cases := []handlertest.SequenceTestCase{
		{":windowsplit>:edit aaa>:windowmove left>",
			`┌━━━━━─────────────┐
│o aaa             │
┌────────┐┌────────┤
│AAAAAAAA││        │
│AAAAAAAA││        │
│AAAAAAAA││        │
│AAAAAAAA││        │
│AAAAAAAA││        │
│AAAAAAAA││        │
└────────┘└────────┘`,
		},
		{":windowmove right>",
			`┌━━━━━─────────────┐
│o aaa             │
├────────┐┌────────┐
│        ││AAAAAAAA│
│        ││AAAAAAAA│
│        ││AAAAAAAA│
│        ││AAAAAAAA│
│        ││AAAAAAAA│
│        ││AAAAAAAA│
└────────┘└────────┘`,
		},
		{":windowsplit down>:windowfocus up>:windowmove down>",
			`┌━━━━━─────────────┐
│o aaa             │
├────────┐┌────────┤
│        ││        │
│        ││        │
│        │└────────┘
│        │┌────────┐
│        ││AAAAAAAA│
│        ││AAAAAAAA│
└────────┘└────────┘`,
		},
		{":windowmove up>",
			`┌━━━━━─────────────┐
│o aaa             │
├────────┐┌────────┐
│        ││AAAAAAAA│
│        ││AAAAAAAA│
│        │└────────┘
│        │┌────────┐
│        ││        │
│        ││        │
└────────┘└────────┘`,
		},
		{":windowmove left>",
			`┌━━━━━─────────────┐
│o aaa             │
┌────────┐┌────────┤
│AAAAAAAA││        │
│AAAAAAAA││        │
│AAAAAAAA│└────────┘
│AAAAAAAA│┌────────┐
│AAAAAAAA││        │
│AAAAAAAA││        │
└────────┘└────────┘`,
		},
		{":terminalnew>:! sh>:windowmove left>:windowmove right>",
			`┌────┌─────────────┐
│o aa│ cannot      │
├───█│ move        │
│   ││ ▐indow in   │
│   ││ this        │
│   ││ direction   │
│   │└─────────────┘
│   │          │   │
│   │          │   │
└───└──────────┘───┘`,
		},
	}

	opts := []text.Option{
		text.WithCommandKey(testCommandKey),
		text.WithCommandOverlayConfig(testCommandOverlayConfig()),
	}

	tempDir, err := os.MkdirTemp("", "")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = os.RemoveAll(tempDir)
	})
	uri, err := workspaceapi.ParseURI(filepath.Join("file://", tempDir))
	require.NoError(t, err)
	ctx := context.Background()
	fileScheme, err := workspace.NewFileScheme(ctx, config.NopConfig(), uri)
	require.NoError(t, err)
	defer fileScheme.Close()

	workspace := workspace.NewSchemeWorkspace(uri, fileScheme, inlineSchedule)
	b := newExForTestingWithWorkspace(t, workspace,
		texttest.NopEditor(), vte.DefaultConfig(), nopPublishEvent,
		clipboard.NewInMemory(), opts...)
	defer b.Close()

	handlertest.TestHandlerSequence(t, b, 20, 10, cases)
}

func TestResizeWindows(t *testing.T) {
	cases := []handlertest.SequenceTestCase{
		{":windowsplit>:edit aaa>:windowresize increase width>",
			`┌━━━━━─────────────┐
│o aaa             │
├───────┐┌─────────┐
│       ││AAAAAAAAA│
│       ││AAAAAAAAA│
│       ││AAAAAAAAA│
│       ││AAAAAAAAA│
│       ││AAAAAAAAA│
│       ││AAAAAAAAA│
└───────┘└─────────┘`,
		},
		{":windowsplit down>:windowresize min height>",
			`┌──────────────────┐
│o aaa             │
├───────┐┌─────────┤
│       ││AAAAAAAAA│
│       ││AAAAAAAAA│
│       ││AAAAAAAAA│
│       │└─────────┘
│       │┌─────────┐
│       ││         │
└───────┘└─────────┘`,
		},
		{":windowresize max height>",
			`┌──────────────────┐
│o aaa             │
├───────┐┌─────────┤
│       ││AAAAAAAAA│
│       │└─────────┘
│       │┌─────────┐
│       ││         │
│       ││         │
│       ││         │
└───────┘└─────────┘`,
		},
		{":windowresize max width>",
			`┌──────────────────┐
│o aaa             │
├─┐┌───────────────┤
│ ││AAAAAAAAAAAAAAA│
│ │└───────────────┘
│ │┌───────────────┐
│ ││               │
│ ││               │
│ ││               │
└─┘└───────────────┘`,
		},
		{":windowresize min width>",
			`┌──────────────────┐
│o aaa             │
├───────────────┐┌─┤
│               ││A│
│               │└─┘
│               │┌─┐
│               ││ │
│               ││ │
│               ││ │
└───────────────┘└─┘`,
		},
		{":windowresize reset>",
			`┌──────────────────┐
│o aaa             │
├────────┐┌────────┤
│        ││AAAAAAAA│
│        ││AAAAAAAA│
│        │└────────┘
│        │┌────────┐
│        ││        │
│        ││        │
└────────┘└────────┘`,
		},
		{":windowresize decrease height>:windowresize decrease width>",
			`┌──────────────────┐
│o aaa             │
├─────────┐┌───────┤
│         ││AAAAAAA│
│         ││AAAAAAA│
│         ││AAAAAAA│
│         │└───────┘
│         │┌───────┐
│         ││       │
└─────────┘└───────┘`,
		},
	}

	opts := []text.Option{
		text.WithCommandKey(testCommandKey),
		text.WithCommandOverlayConfig(testCommandOverlayConfig()),
	}

	tempDir, err := os.MkdirTemp("", "")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = os.RemoveAll(tempDir)
	})
	uri, err := workspaceapi.ParseURI(filepath.Join("file://", tempDir))
	require.NoError(t, err)
	ctx := context.Background()
	fileScheme, err := workspace.NewFileScheme(ctx, config.NopConfig(), uri)
	require.NoError(t, err)
	defer fileScheme.Close()

	workspace := workspace.NewSchemeWorkspace(uri, fileScheme, inlineSchedule)
	b := newExForTestingWithWorkspace(t, workspace,
		texttest.NopEditor(), vte.DefaultConfig(), nopPublishEvent,
		clipboard.NewInMemory(), opts...)
	defer b.Close()

	handlertest.TestHandlerSequence(t, b, 20, 10, cases)
}

func TestExposedRootNodeIssue(t *testing.T) {
	opts := []text.Option{
		text.WithCommandOverlayConfig(testCommandOverlayConfig()),
		text.WithCommandKey(testCommandKey),
		text.WithCommandAliases(map[string]text.CommandAlias{
			"boom": text.CommandAlias{
				Commands: []string{
					"windownew",
					"windowdefaultsplit h",
					"windownew",
					"windowdefaultsplit v",
					"windownew",
					"windowfocus left",
					"windowfocus left",
				},
			},
		}),
	}
	cases := []handlertest.SequenceTestCase{
		{":boom>",
			`┌────┌─────────────┐
│    │ changed     │
┌────│ split       │
│    │ direction   │
│    │ to vertical │
│    └─────────────┘
│    ┌─────────────┐
│    │ changed     │
│    │ split       │
└────│ direction   │`,
		},
	}

	b := newExForTesting(t, texttest.NopEditor(), opts...)
	defer b.Close()
	handlertest.TestHandlerSequence(t, b, 20, 10, cases)
}

func TestEditCompletion(t *testing.T) {
	cases := []handlertest.SequenceTestCase{
		{":edit re",
			`                    
                    
                    
                    
 edit re▐           
 retalls            
                    
                    
                    
                    `},
		{":edit dawo",
			`                    
                    
                    
                    
 edit dawo▐         
 daworg             
                    
                    
                    
                    `},
		{":edit dawo✌re",
			`                    
                    
                    
                    
 edit daworg re▐    
 retalls            
                    
                    
                    
                    `},
		{":edit dawo⬇✌re",
			`                    
                    
                    
                    
 edit daworg re▐    
 retalls            
                    
                    
                    
                    `},
		{":edit dawo⬇✌re✌^^^^^^^^^^^",
			`                    
                    
                    
                    
 edit dawo▐         
 daworg             
                    
                    
                    
                    `},
		{":edit dawo⬇✌re✌^^^^^^^^^^^✌re",
			`                    
                    
                    
                    
 edit daworg re▐    
 retalls            
                    
                    
                    
                    `},
		{":edit dawo⬇✌re✌^^^^^^^^^^^^^^^^^",
			`                    
                    
                    
                    
 edi▐               
 edit               
 keybindings        
 readfile           
 reloadfile!        
                    `},
		{":edit dawo⬇✌re✌^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^",
			`                    
                    
                    
                    
                    
                    
                    
                    
                    
                    `},
	}

	fn := func(t *testing.T) tui.Handler {
		opts := []text.Option{
			text.WithCommandKey(testCommandKey),
			text.WithWindowManagerConfig(thandler.WindowManagerConfig{
				WindowManagerConfig: tcomponent.WindowManagerConfig{Frame: false}}),
			text.WithCommandOverlayConfig(testCommandOverlayConfig()),
		}
		uri, err := workspaceapi.ParseURI("memory:///")
		require.NoError(t, err)
		scheme, err := workspace.NewMemoryScheme(context.Background(), config.NopConfig(), uri)
		require.NoError(t, err)
		touchTestFile(t, scheme, "daworg")
		touchTestFile(t, scheme, "retalls")
		b := newExForTestingWithWorkspace(t, workspace.NewSchemeWorkspace(uri, scheme, inlineSchedule),
			texttest.NopEditor(), vte.DefaultConfig(), nopPublishEvent,
			clipboard.NewInMemory(), opts...)
		t.Cleanup(func() { _ = b.Close() })
		return b
	}

	handlertest.TestHandlerIsolated(t, fn, 20, 10, cases)
}

func TestRenameTab(t *testing.T) {
	cases := []handlertest.SequenceTestCase{
		{":edit hello.go>:tabrename 8berSucks>",
			`┌━━━━━━━━━━━───────┐
│o 8berSucks       │
├──────────────────┤
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
└──────────────────┘`},
	}

	opts := []text.Option{
		text.WithCommandKey(testCommandKey),
	}
	b := newExForTesting(t, texttest.NopEditor(), opts...)
	defer b.Close()

	handlertest.TestHandlerSequence(t, b, 20, 10, cases)
}

func TestEventNone(t *testing.T) {
	t.Run("delegates to underlying handler", func(t *testing.T) {
		cases := []handlertest.SequenceTestCase{
			{"🎉edit hello.go>",
				`┌━━━━━━━━━━────────┐
│o hello.go        │
├──────────────────┤
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
└──────────────────┘`},
		}

		testCommandKey := term.KeyComb{Key: term.KeySpace, Mod: term.ModCtrl}
		opts := []text.Option{
			text.WithCommandKey(testCommandKey),
		}
		b := newExForTesting(t, texttest.NopEditor(), opts...)
		defer b.Close()

		handlertest.TestHandlerSequence(t, b, 20, 10, cases)

		b.Handle(term.Event{Type: term.EventNone})

		cases = []handlertest.SequenceTestCase{
			{"",
				`┌━━━━━━━━━━────────┐
│o hello.go        │
├──────────────────┤
│BBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBB│
└──────────────────┘`},
		}
		handlertest.TestHandlerSequence(t, b, 20, 10, cases)
	})
}

func TestMultipleCommandArgsKeyBindings(t *testing.T) {

	cases := []handlertest.SequenceTestCase{
		{"`",
			`┌──────────────────┐
│                  │
├──────────────────┤
│                  │
│                  │
│                  │
└──────────────────┘
┌──────────────────┐
│                  │
└──────────────────┘`,
		},
	}

	opts := []text.Option{
		text.WithCommandKey(testCommandKey),
		text.WithCommandKeyBinding(term.KeyComb{Mod: term.ModCtrl, Ch: 'v'},
			[][]string{
				{"windowsplit", "down"},
				{"windowresize", "min", "height"},
			}),
		text.WithCommandOverlayConfig(testCommandOverlayConfig()),
	}

	b := newExForTesting(t, texttest.NopEditor(), opts...)
	defer b.Close()

	handlertest.TestHandlerSequence(t, b, 20, 10, cases)
}

func TestTerminalOnFocus(t *testing.T) {
	t.Run("new terminal tab", func(t *testing.T) {
		uri, err := workspaceapi.ParseURI("memory:///")
		require.NoError(t, err)
		scheme, _ := workspace.NewMemoryScheme(context.Background(), config.NopConfig(), uri)
		testConfig := vte.DefaultConfig()
		ex := newExForTestingWithWorkspace(t, workspace.NewSchemeWorkspace(uri, scheme, inlineSchedule),
			texttest.NopEditor(), testConfig, nopPublishEvent, clipboard.NewInMemory())
		tvte := newTestVte()
		ex.newEmulatorHandler = func(args []string) (vtereservoir.VTE, error) {
			assert.Equal(t, "echo bla", strings.Join(args, " "))
			return tvte, nil
		}
		t.Cleanup(func() { _ = ex.Close() })

		ex.terminalnewtab(context.Background(), "echo", "bla")

		require.Len(t, tvte.onFocusChange, 2)
		assert.False(t, tvte.onFocusChange[0])
		assert.True(t, tvte.onFocusChange[1])

		// switch to some other tab, same window
		ex.editFiles(context.Background(), "a")
		require.Len(t, tvte.onFocusChange, 3)
		assert.False(t, tvte.onFocusChange[2])

		// switch back to terminal tab, same window
		ex.tabprevious(context.Background())
		require.Len(t, tvte.onFocusChange, 4)
		assert.True(t, tvte.onFocusChange[3])

		// new window, tab still in screen but not focused
		ex.windownew(context.Background())
		require.Len(t, tvte.onFocusChange, 5)
		assert.False(t, tvte.onFocusChange[4])

		// focus back to tab window
		ex.windowfocus(context.Background(), "left")
		require.Len(t, tvte.onFocusChange, 6)
		assert.True(t, tvte.onFocusChange[5])

		_, handled := ex.Handle(term.Event{Type: term.EventUnfocus})
		require.True(t, handled)
		require.Len(t, tvte.onFocusChange, 7)
		assert.False(t, tvte.onFocusChange[6])

		_, handled = ex.Handle(term.Event{Type: term.EventFocus})
		require.True(t, handled)
		require.Len(t, tvte.onFocusChange, 8)
		assert.True(t, tvte.onFocusChange[7])

		ex.tabclose(context.Background())
		require.Len(t, tvte.onFocusChange, 9)
		assert.False(t, tvte.onFocusChange[8])
	})

	t.Run("companion terminal", func(t *testing.T) {
		uri, err := workspaceapi.ParseURI("memory:///")
		require.NoError(t, err)
		scheme, _ := workspace.NewMemoryScheme(context.Background(), config.NopConfig(), uri)
		testConfig := vte.DefaultConfig()
		ex := newExForTestingWithWorkspace(t, workspace.NewSchemeWorkspace(uri, scheme, inlineSchedule),
			texttest.NopEditor(), testConfig, nopPublishEvent, clipboard.NewInMemory())
		tvte := newTestVte()
		ex.newEmulatorHandler = func([]string) (vtereservoir.VTE, error) {
			return tvte, nil
		}
		t.Cleanup(func() { _ = ex.Close() })
		ex.Resize(100, 100)

		ex.executePlugin(context.Background())
		content, err := ex.invokeWindow().Content()
		require.NoError(t, err)
		_, ok := content.(vtereservoir.VTE)
		require.True(t, ok)

		require.Len(t, tvte.onFocusChange, 2)
		assert.False(t, tvte.onFocusChange[0])
		assert.True(t, tvte.onFocusChange[1])

		// switching from floating to other window should trigger on focus change
		ex.windowfocus(bgctx, "left")
		require.Len(t, tvte.onFocusChange, 3)
		assert.False(t, tvte.onFocusChange[2])

		// switching back to floating should trigger again
		ex.executePlugin(bgctx)
		require.Len(t, tvte.onFocusChange, 4)
		assert.True(t, tvte.onFocusChange[3])

		_, handled := ex.Handle(term.Event{Type: term.EventUnfocus})
		require.True(t, handled)
		require.Len(t, tvte.onFocusChange, 5)
		assert.False(t, tvte.onFocusChange[4])

		_, handled = ex.Handle(term.Event{Type: term.EventFocus})
		require.True(t, handled)
		require.Len(t, tvte.onFocusChange, 6)
		assert.True(t, tvte.onFocusChange[5])

		// indirectly toggle terminal companion
		ex.editFiles(bgctx, "a")
		require.Len(t, tvte.onFocusChange, 7)
		assert.False(t, tvte.onFocusChange[6])
	})

	t.Run("ephemeral terminal", func(t *testing.T) {
		uri, err := workspaceapi.ParseURI("memory:///")
		require.NoError(t, err)
		scheme, _ := workspace.NewMemoryScheme(context.Background(), config.NopConfig(), uri)
		testConfig := vte.DefaultConfig()
		ex := newExForTestingWithWorkspace(t, workspace.NewSchemeWorkspace(uri, scheme, inlineSchedule),
			texttest.NopEditor(), testConfig, nopPublishEvent, clipboard.NewInMemory())
		tvte := newTestVte()
		ex.newPluginHandler = func(_ int, args ...string) (pluginHandler, error) {
			// `echo bla` has no shell metacharacters or
			// operators, so cmdenv.BuildPluginArgv passes it
			// through as plain argv. Multi-arg invocations
			// with operators take the `sh -c` path — see
			// TestExecutePluginShellInterpretsOperators.
			require.Equal(t, []string{"echo", "bla"}, args)
			return tvte, nil
		}
		t.Cleanup(func() { _ = ex.Close() })
		ex.Resize(100, 100)
		ex.editFiles(bgctx, "a", "b") // have tabs available for later

		ex.executePlugin(bgctx, "echo", "bla")
		// The plugin build runs on a background goroutine now; settle
		// it so the queued focus transitions replay onto tvte.
		ex.waitAsyncVTELoads()
		ex.flushScheduled()

		require.Len(t, tvte.onFocusChange, 2)
		assert.False(t, tvte.onFocusChange[0])
		assert.True(t, tvte.onFocusChange[1])

		// switching from floating to other window should trigger on focus change
		ex.windowfocus(bgctx, "left")
		require.Len(t, tvte.onFocusChange, 3)
		assert.False(t, tvte.onFocusChange[2])

		// switching back to floating should trigger again
		ex.Handle(term.Event{Type: term.EventMouse, MouseX: 50, MouseY: 50, Key: term.MouseLeft})
		require.Len(t, tvte.onFocusChange, 4)
		assert.True(t, tvte.onFocusChange[3])

		_, handled := ex.Handle(term.Event{Type: term.EventUnfocus})
		require.True(t, handled)
		require.Len(t, tvte.onFocusChange, 5)
		assert.False(t, tvte.onFocusChange[4])

		_, handled = ex.Handle(term.Event{Type: term.EventFocus})
		require.True(t, handled)
		require.Len(t, tvte.onFocusChange, 6)
		assert.True(t, tvte.onFocusChange[5])

		// ephemeral close should trigger another focus event
		ex.tabnext(context.Background())
		require.Len(t, tvte.onFocusChange, 7)
		assert.False(t, tvte.onFocusChange[6])
	})
}

// TestExecutePluginShellInterpretsOperators is a regression test for
// the bug where `! echo "$(...)" | tee /tmp/out` passed the `|` as a
// literal argv element instead of having the downstream shell pipe
// the output. The fix routes any `!` invocation whose args contain
// shell operators (or that has 2+ args, where re-tokenisation cannot
// be made lossless without a shell) through `sh -c <quoted-line>` so
// the surrounding pipes/redirects/&&/||/$() are interpreted by the
// shell rather than concatenated as argv.
//
// The third element passed to newPluginHandler is the line wrapped by
// cmdenv.Quote because vte.Component.startCommand re-tokenises the
// joined argv via shell.Fields; without bash-quoting the line would
// fragment back into argv pieces and the shell would never see the
// pipe as an operator. After shell.Fields runs over the joined
// `sh -c <quoted-line>`, the third arg arrives at sh -c intact.
func TestExecutePluginShellInterpretsOperators(t *testing.T) {
	bgctx := context.Background()
	cases := []struct {
		name     string
		args     []string
		wantArgv []string
	}{
		{
			name:     "single arg without operators stays direct",
			args:     []string{"htop"},
			wantArgv: []string{"htop"},
		},
		{
			name:     "two simple args stay direct",
			args:     []string{"echo", "hi"},
			wantArgv: []string{"echo", "hi"},
		},
		{
			name:     "pipe routes through sh -c",
			args:     []string{"echo", "hi", "|", "tee", "/tmp/yikes"},
			wantArgv: []string{"sh", "-c", "'echo hi | tee /tmp/yikes'"},
		},
		{
			name:     "redirect routes through sh -c",
			args:     []string{"echo", "hi", ">", "/tmp/yikes"},
			wantArgv: []string{"sh", "-c", "'echo hi > /tmp/yikes'"},
		},
		{
			name:     "and-and routes through sh -c",
			args:     []string{"true", "&&", "echo", "ok"},
			wantArgv: []string{"sh", "-c", "'true && echo ok'"},
		},
		{
			name:     "semicolon routes through sh -c",
			args:     []string{"echo", "a;", "echo", "b"},
			wantArgv: []string{"sh", "-c", "'echo a; echo b'"},
		},
		{
			name: "cmdsubst with pipe routes through sh -c",
			args: []string{"echo",
				"$(runectl llm message openai/gpt-5.5 hello)",
				"|", "tee", "/tmp/yikes"},
			wantArgv: []string{"sh", "-c",
				"'echo $(runectl llm message openai/gpt-5.5 hello) | tee /tmp/yikes'"},
		},
		{
			name:     "single arg containing pipe routes through sh -c",
			args:     []string{"echo hi | tee /tmp/yikes"},
			wantArgv: []string{"sh", "-c", "'echo hi | tee /tmp/yikes'"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			uri, err := workspaceapi.ParseURI("memory:///")
			require.NoError(t, err)
			scheme, _ := workspace.NewMemoryScheme(
				context.Background(), config.NopConfig(), uri)
			ex := newExForTestingWithWorkspace(t,
				workspace.NewSchemeWorkspace(uri, scheme, inlineSchedule),
				texttest.NopEditor(), vte.DefaultConfig(),
				nopPublishEvent, clipboard.NewInMemory())
			tvte := newTestVte()
			var got []string
			ex.newPluginHandler = func(_ int, args ...string) (pluginHandler, error) {
				got = append([]string(nil), args...)
				return tvte, nil
			}
			t.Cleanup(func() { _ = ex.Close() })
			ex.Resize(100, 100)

			require.NoError(t, ex.executePlugin(bgctx, tc.args...))
			// The plugin build runs on a background goroutine now;
			// settle it before asserting the argv it received.
			ex.waitAsyncVTELoads()
			ex.flushScheduled()
			assert.Equal(t, tc.wantArgv, got)
		})
	}
}

func TestSwitchToTab(t *testing.T) {
	cases := []handlertest.SequenceTestCase{
		{":edit hello.go>B:edit world.go>",
			`┌────────────━━━━━━━━━━──────┐
│o hello.go  o world.go      │
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
└────────────────────────────┘`},

		{":tabfocus ",
			`┌────────────━━━━━━━━━━──────┐
│o hello.go  o world.go      │
├────────────────────────────┤
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
┌────────────────────────────┐
│ tabfocus ▐<position>]      │
│ 1 hello.go                 │
│ 2 world.go                 │
└────────────────────────────┘
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAAAAAAAAAAAA│
└────────────────────────────┘`},
		{"1>",
			`┌━━━━━━━━━━──────────────────┐
│o hello.go  o world.go      │
├────────────────────────────┤
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
└────────────────────────────┘`},
		{"2 world.go>",
			`┌━━━━━━━━━━──────────────────┐
│o hello.go  o world.go      │
├────────────────────────────┤
│MMMMMMMMMMMMMMMMMMMMMMMMMMMM│
│MMMMMMMMMMMMMMMMMMMMMMMMMMMM│
│MMMMMMMMMMMMMMMMMMMMMMMMMMMM│
│MMMMMMMMMMMMMMMMMMMMMMMMMMMM│
│MMMMMMMMMMMMMMMMMMMMMMMMMMMM│
│MMMMMMMMMMMMMMMMMMMMMMMMMMMM│
│MMMMMMMMMMMMMMMMMMMMMMMMMMMM│
│MMMMMMMMMMMMMMMMMMMMMMMMMMMM│
│MMMMMMMMMMMMMMMMMMMMMMMMMMMM│
│MMMMMMMMMMMMMMMMMMMMMMMMMMMM│
│MMMMMMMMMMMMMMMMMMMMMMMMMMMM│
└────────────────────────────┘`},
		{"1 hell>",
			`┌━━━━━━━━━━──────────────────┐
│o hello.go  o world.go      │
├────────────────────────────┤
│TTTTTTTTTTTTTTTTTTTTTTTTTTTT│
│TTTTTTTTTTTTTTTTTTTTTTTTTTTT│
│TTTTTTTTTTTTTTTTTTTTTTTTTTTT│
│TTTTTTTTTTTTTTTTTTTTTTTTTTTT│
│TTTTTTTTTTTTTTTTTTTTTTTTTTTT│
│TTTTTTTTTTTTTTTTTTTTTTTTTTTT│
│TTTTTTTTTTTTTTTTTTTTTTTTTTTT│
│TTTTTTTTTTTTTTTTTTTTTTTTTTTT│
│TTTTTTTTTTTTTTTTTTTTTTTTTTTT│
│TTTTTTTTTTTTTTTTTTTTTTTTTTTT│
│TTTTTTTTTTTTTTTTTTTTTTTTTTTT│
└────────────────────────────┘`},
		{"2 notexist.go>",
			`┌━━━━━━━━━━──────────────────┐
│o hello.go  o world.go      │
├────────────────────────────┤
│bbbbbbbbbbbbbbbbbbbbbbbbbbbb│
│bbbbbbbbbbbbbbbbbbbbbbbbbbbb│
│bbbbbbbbbbbbbbbbbbbbbbbbbbbb│
│bbbbbbbbbbbbbbbbbbbbbbbbbbbb│
│bbbbbbbbbbbbbbbbbbbbbbbbbbbb│
│bbbbbbbbbbbbbbbbbbbbbbbbbbbb│
│bbbbbbbbbbbbbbbbbbbbbbbbbbbb│
│bbbbbbbbbbbbbbbbbbbbbbbbbbbb│
│bbbbbbbbbbbbbbbbbbbbbbbbbbbb│
│bbbbbbbbbbbbbbbbbbbbbbbbbbbb│
│bbbbbbbbbbbbbbbbbbbbbbbbbbbb│
└────────────────────────────┘`},
		{":tabfocus 3>",
			`┌━━━━━━━━━━──────────────────┐
│o hello.go  o world.go      │
├────────────────────────────┤
│bbbbbbbbbbbbbbbbbbbbbbbbbbbb│
│bbbbbbbbbbbbbbbbbbbbbbbbbbbb│
│bbbbbbbbbbbbbbbbbbbbbbbbbbbb│
│bbbbbbbbbbbbbbbbbbbbbbbbbbbb│
│bbbbbbbbbbbbbbbbbbbbbbbbbbbb│
│bbbbbbbbbbbbbbbbbbbbbbbbbbbb│
│bbbbbbbbbbbbbbbbbbbbbbbbbbbb│
│bbbbbbbbbbbbbbbbbbbbbbbbbbbb│
│bbbbbbbbbbbbbbbbbbbbbbbbbbbb│
│bbbbbbbbbbbbbbbbbbbbbbbbbbbb│
│bbbbbbbbbbbbbbbbbbbbbbbbbbbb│
└────────────────────────────┘`},
		{":tabfocus 0>",
			`┌━━━━━━━━━━────┌─────────────┐
│o hello.go  o │ the first   │
├──────────────│ tab is 1    │
│bbbbbbbbbbbbbb└─────────────┘
│bbbbbbbbbbbbbbbbbbbbbbbbbbbb│
│bbbbbbbbbbbbbbbbbbbbbbbbbbbb│
│bbbbbbbbbbbbbbbbbbbbbbbbbbbb│
│bbbbbbbbbbbbbbbbbbbbbbbbbbbb│
│bbbbbbbbbbbbbbbbbbbbbbbbbbbb│
│bbbbbbbbbbbbbbbbbbbbbbbbbbbb│
│bbbbbbbbbbbbbbbbbbbbbbbbbbbb│
│bbbbbbbbbbbbbbbbbbbbbbbbbbbb│
│bbbbbbbbbbbbbbbbbbbbbbbbbbbb│
│bbbbbbbbbbbbbbbbbbbbbbbbbbbb│
└────────────────────────────┘`},
		// command prompt shouldn't complete with history
		{":tabcloseall>:tabfocus ",
			`┌──────────────┌─────────────┐
│              │ the first   │
├──────────────│ tab is 1    │
│              └─────────────┘
│                            │
│                            │
│                            │
┌────────────────────────────┐
│ tabfocus ▐<position>]      │
│                            │
│                            │
└────────────────────────────┘
│                            │
│                            │
└────────────────────────────┘`},
	}

	opts := []text.Option{
		text.WithCommandKey(testCommandKey),
	}
	b := newExForTesting(t, texttest.NopEditor(), opts...)
	defer b.Close()

	handlertest.TestHandlerSequence(t, b, 30, 15, cases)
}

func TestTabFocusSwitchesToOwningWindow(t *testing.T) {
	cases := []handlertest.SequenceTestCase{
		// Two tabs across a vertical split: hello.go in the left
		// window, world.go in the right window (focused).
		{":edit hello.go>:windowsplit right>:edit world.go>",
			`┌────────────━━━━━━━━━━──────┐
│o hello.go  o world.go      │
├─────────────┐┌─────────────┐
│AAAAAAAAAAAAA││AAAAAAAAAAAAA│
│AAAAAAAAAAAAA││AAAAAAAAAAAAA│
│AAAAAAAAAAAAA││AAAAAAAAAAAAA│
│AAAAAAAAAAAAA││AAAAAAAAAAAAA│
│AAAAAAAAAAAAA││AAAAAAAAAAAAA│
│AAAAAAAAAAAAA││AAAAAAAAAAAAA│
│AAAAAAAAAAAAA││AAAAAAAAAAAAA│
│AAAAAAAAAAAAA││AAAAAAAAAAAAA│
│AAAAAAAAAAAAA││AAAAAAAAAAAAA│
│AAAAAAAAAAAAA││AAAAAAAAAAAAA│
│AAAAAAAAAAAAA││AAAAAAAAAAAAA│
└─────────────┘└─────────────┘`},
		// tabfocus 1 from the right (non-owning) window moves focus
		// to the left window that owns hello.go.
		{":tabfocus 1>",
			`┌━━━━━━━━━━──────────────────┐
│o hello.go  o world.go      │
┌─────────────┐┌─────────────┤
│AAAAAAAAAAAAA││AAAAAAAAAAAAA│
│AAAAAAAAAAAAA││AAAAAAAAAAAAA│
│AAAAAAAAAAAAA││AAAAAAAAAAAAA│
│AAAAAAAAAAAAA││AAAAAAAAAAAAA│
│AAAAAAAAAAAAA││AAAAAAAAAAAAA│
│AAAAAAAAAAAAA││AAAAAAAAAAAAA│
│AAAAAAAAAAAAA││AAAAAAAAAAAAA│
│AAAAAAAAAAAAA││AAAAAAAAAAAAA│
│AAAAAAAAAAAAA││AAAAAAAAAAAAA│
│AAAAAAAAAAAAA││AAAAAAAAAAAAA│
│AAAAAAAAAAAAA││AAAAAAAAAAAAA│
└─────────────┘└─────────────┘`},
		// tabfocus 1 again is a no-op because the tab is already in
		// the focused window.
		{":tabfocus 1>",
			`┌━━━━━━━━━━──────────────────┐
│o hello.go  o world.go      │
┌─────────────┐┌─────────────┤
│AAAAAAAAAAAAA││AAAAAAAAAAAAA│
│AAAAAAAAAAAAA││AAAAAAAAAAAAA│
│AAAAAAAAAAAAA││AAAAAAAAAAAAA│
│AAAAAAAAAAAAA││AAAAAAAAAAAAA│
│AAAAAAAAAAAAA││AAAAAAAAAAAAA│
│AAAAAAAAAAAAA││AAAAAAAAAAAAA│
│AAAAAAAAAAAAA││AAAAAAAAAAAAA│
│AAAAAAAAAAAAA││AAAAAAAAAAAAA│
│AAAAAAAAAAAAA││AAAAAAAAAAAAA│
│AAAAAAAAAAAAA││AAAAAAAAAAAAA│
│AAAAAAAAAAAAA││AAAAAAAAAAAAA│
└─────────────┘└─────────────┘`},
		// tabfocus 2 from the left window moves focus back to the
		// right window that owns world.go.
		{":tabfocus 2>",
			`┌────────────━━━━━━━━━━──────┐
│o hello.go  o world.go      │
├─────────────┐┌─────────────┐
│AAAAAAAAAAAAA││AAAAAAAAAAAAA│
│AAAAAAAAAAAAA││AAAAAAAAAAAAA│
│AAAAAAAAAAAAA││AAAAAAAAAAAAA│
│AAAAAAAAAAAAA││AAAAAAAAAAAAA│
│AAAAAAAAAAAAA││AAAAAAAAAAAAA│
│AAAAAAAAAAAAA││AAAAAAAAAAAAA│
│AAAAAAAAAAAAA││AAAAAAAAAAAAA│
│AAAAAAAAAAAAA││AAAAAAAAAAAAA│
│AAAAAAAAAAAAA││AAAAAAAAAAAAA│
│AAAAAAAAAAAAA││AAAAAAAAAAAAA│
│AAAAAAAAAAAAA││AAAAAAAAAAAAA│
└─────────────┘└─────────────┘`},
	}

	opts := []text.Option{
		text.WithCommandKey(testCommandKey),
	}
	b := newExForTesting(t, texttest.NopEditor(), opts...)
	defer b.Close()

	handlertest.TestHandlerSequence(t, b, 30, 15, cases)
}

func TestRunStopTasks(t *testing.T) {
	t.Run("newtask is called with incorrect number of args returns error", func(t *testing.T) {
		b, mu, cleanup := newExForTestingTasks(t)
		defer cleanup()

		mu.Lock()
		defer mu.Unlock()

		require.Error(t, b.newTask(bgctx, "up", "--", "make"))
		require.Error(t, b.newTask(bgctx, "newTask", "left", "make"))
		require.Error(t, b.newTask(bgctx, "--", "make", "test", "things"))
		require.Error(t, b.newTask(bgctx, "up", ".go,.md", "--", "make", "test"))
	})

	t.Run("newtask is called with correct number of args returns no error", func(t *testing.T) {
		b, mu, cleanup := newExForTestingTasks(t)
		defer cleanup()

		mu.Lock()
		defer mu.Unlock()

		require.NoError(t, b.newTask(bgctx, "myTask", "left", "--", "make"))
		require.NoError(t, b.newTask(bgctx, "myTask2", "right", "--", "make", "test", "things"))
		require.NoError(t, b.newTask(bgctx, "myTask3", "left", ".go,.md", "--", "make", "test"))
		require.NoError(t, b.newTask(bgctx, "myTask4", "right", ".go,.md", "--", "make", "test"))
	})

	// left/right alignment combined with up/down is ugly; stick to left/right only
	t.Run("newtask is called with left or right alignment is error", func(t *testing.T) {
		b, mu, cleanup := newExForTestingTasks(t)
		defer cleanup()

		mu.Lock()
		defer mu.Unlock()

		require.Error(t, b.newTask(bgctx, "myTask", "up", "--", "make"))
		require.Error(t, b.newTask(bgctx, "myTask2", "down", "--", "make", "test", "things"))
		require.Error(t, b.newTask(bgctx, "myTask3", "up", ".go,.md", "--", "make", "test"))
		require.Error(t, b.newTask(bgctx, "myTask4", "down", ".go,.md", "--", "make", "test"))
	})

	t.Run("newtask called twice with same task name opens a prompt", func(t *testing.T) {
		b, mu, cleanup := newExForTestingTasks(t)
		defer cleanup()

		mu.Lock()
		defer mu.Unlock()

		require.NoError(t, b.newTask(bgctx, "myTask", "left", "--", "make"))
		require.NoError(t, b.newTask(bgctx, "myTask", "right", "--", "make", "test", "things"))
		_, handled := b.Handle(term.Event{Type: term.EventKey, Ch: 'y'})
		assert.True(t, handled)
	})

	t.Run("integration", func(t *testing.T) {
		cases := []handlertest.SequenceTestCase{
			{":tasknew test right .go -- go test ./...>",
				`┌────────────────────────────┐
│                            │
├───────────────────────────┐┤
│                           ││
│                           ││
│                           ││
│                           ││
│                           ││
│                           ││
│                           ││
│                           ││
│                           ││
│                           ││
│                           ││
└───────────────────────────┘┘`},
			{":tasknew build left -- go build ./...>:tasknew assets left -- echo a>:tasknew validateAssets right .html,.js,.css,.ts -- echo b>",
				`┌────────────────────────────┐
│                            │
├┌┌────────────────────────┐┐┤
│││                        │││
│││                        │││
│││                        │││
│││                        │││
│││                        │││
│││                        │││
│││                        │││
│││                        │││
│││                        │││
│││                        │││
│││                        │││
└└└────────────────────────┘┘┘`},
			{":taskclose ",
				`┌────────────────────────────┐
│                            │
├┌┌────────────────────────┐┐┤
│││                        │││
│││                        │││
│││                        │││
│││                        │││
┌────────────────────────────┐
│ taskclose ▐                │
│ assets                     │
│ build                      │
│ test                       │
│ validateAssets             │
└────────────────────────────┘
└└└────────────────────────┘┘┘`},
			{" assets>:taskclose test>:taskclose ",
				`┌────────────────────────────┐
│                            │
├┌──────────────────────────┐┤
││                          ││
││                          ││
││                          ││
││                          ││
┌────────────────────────────┐
│ taskclose ▐                │
│ build                      │
│ validateAssets             │
└────────────────────────────┘
││                          ││
││                          ││
└└──────────────────────────┘┘`},
			{"<:windowfocus right>",
				`┌────────────────────────────┐
│                            │
├┌──█●███████████████████████┤
││  │ ▀       echo b         │
││  │start command: context c│
││  │anceled▐                │
││  │                        │
││  │                        │
││  │                        │
││  │                        │
││  │                        │
││  │                        │
││  │                        │
││  │                        │
└└──└────────────────────────┘`},
			{":windowclose>:tasknew validateAssets right -- echo a>", // recreate after close
				`┌────────────────────────────┐
│                            │
├┌──────────────────────────┐┤
││                          ││
││                          ││
││                          ││
││                          ││
││                          ││
││                          ││
││                          ││
││                          ││
││                          ││
││                          ││
││                          ││
└└──────────────────────────┘┘`},
			{":tasknew validateAssets right -- echo b>", // prompt to replace
				`┌────────────────────────────┐
│                            │
├┌──────────────────────────┐┤
│█●██████████████████████████│
││                          ││
││  A task with the name    ││
││  "validateAssets"        ││
││  already exists. Do you  ││
││  want to replace it?     ││
││                          ││
││                          ││
││     Yes          No      ││
│└──────────────────────────┘│
││                          ││
└└──────────────────────────┘┘`},
			{"y:windowfocus right>",
				`┌────────────────────────────┐
│                            │
├┌──█●███████████████████████┤
││  │ ▀    task   echo b   │
││  │start command: context c│
││  │anceled▐                │
││  │                        │
││  │                        │
││  │                        │
││  │                        │
││  │                        │
││  │                        │
││  │                        │
││  │                        │
└└──└────────────────────────┘`},
			{":windowconverttab asset x>",
				`┌━━━━━━━─────────────────────┐
│x asset                     │
├┌───────────────────────────┤
││ ▀     task   echo b     │
││start command: context c   │
││anceled▐                   │
││                           │
││                           │
││                           │
││                           │
││                           │
││                           │
││                           │
││                           │
└└───────────────────────────┘`},
			{":edit abc>:write>",
				`┌─────────━━━━━──────────────┐
│x asset  o abc              │
├┌───────────────────────────┤
││AAAAAAAAAAAAAAAAAAAAAAAAAAA│
││AAAAAAAAAAAAAAAAAAAAAAAAAAA│
││AAAAAAAAAAAAAAAAAAAAAAAAAAA│
││AAAAAAAAAAAAAAAAAAAAAAAAAAA│
││AAAAAAAAAAAAAAAAAAAAAAAAAAA│
││AAAAAAAAAAAAAAAAAAAAAAAAAAA│
││AAAAAAAAAAAAAAAAAAAAAAAAAAA│
││AAAAAAAAAAAAAAAAAAAAAAAAAAA│
││AAAAAAAAAAAAAAAAAAAAAAAAAAA│
││AAAAAAAAAAAAAAAAAAAAAAAAAAA│
││AAAAAAAAAAAAAAAAAAAAAAAAAAA│
└└───────────────────────────┘`},
			{":tabprevious>",
				`┌━━━━━━━─────────────────────┐
│x asset  o abc              │
├┌───────────────────────────┤
││ ▀     task   echo b     │
││start command: context c   │
││anceled▐                   │
││                           │
││                           │
││                           │
││                           │
││                           │
││                           │
││                           │
││                           │
└└───────────────────────────┘`},
			{":windowfocus left>:windowconverttab build X>",
				`┌────────────────━━━━━━━─────┐
│x asset  o abc  X build     │
├────────────────────────────┤
│ ▀     go build ./...       │
│start command: context cance│
│led    ▐                    │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
└────────────────────────────┘`},
			{":tabprevious>:tabprevious>:tabprevious>",
				`┌────────────────━━━━━━━─────┐
│x asset  o abc  X build     │
├────────────────────────────┤
│ ▀     go build ./...       │
│start command: context cance│
│led    ▐                    │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
└────────────────────────────┘`},
			{":windowsplit right>:tabnext>:tabnext>",
				`┌─────────━━━━━──────────────┐
│x asset  o abc  X build     │
├─────────────┐┌─────────────┐
│ ▀           ││AAAAAAAAAAAAA│
│start command││AAAAAAAAAAAAA│
│led          ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
└─────────────┘└─────────────┘`},
			{":taskclose validateAssets>",
				`┌━━━━━───────────────────────┐
│o abc  X build              │
├─────────────┐┌─────────────┐
│ ▀           ││AAAAAAAAAAAAA│
│start command││AAAAAAAAAAAAA│
│led          ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
└─────────────┘└─────────────┘`},
			{":taskclose build>",
				`┌━━━━━───────────────────────┐
│o abc                       │
├─────────────┐┌─────────────┐
│             ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
└─────────────┘└─────────────┘`},
			{":windowfocus left>:tasknewtab tests -- go test ./...>",
				`┌───────━━━━━━━──────────────┐
│o abc  8 tests              │
┌─────────────┐┌─────────────┤
│ ▀           ││AAAAAAAAAAAAA│
│start command││AAAAAAAAAAAAA│
│: context can││AAAAAAAAAAAAA│
│celed  ▐     ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
└─────────────┘└─────────────┘`},
			{":windowfocus right>:tasknewtab build -- go build ./...>",
				`┌────────────────━━━━━━━─────┐
│o abc  8 tests  8 build     │
├─────────────┐┌─────────────┐
│ ▀           ││ ▀           │
│start command││start command│
│: context can││: context can│
│celed        ││celed  ▐     │
│             ││             │
│             ││             │
│             ││             │
│             ││             │
│             ││             │
│             ││             │
│             ││             │
└─────────────┘└─────────────┘`},
			{":tasknewtab build -- go build ./...>",
				`┌────────────────────────────┐
│o abc  8 tests  8 build     │
├─────────────┐┌─────────────┤
█●████████████████████████████
│                            │
│  A task with the name      │
│  "build" already exists.   │
│  Do you want to replace    │
│  it?                       │
│                            │
│                            │
│      Yes          No       │
└────────────────────────────┘
│             ││             │
└─────────────┘└─────────────┘`},
			{"y",
				`┌────────────────━━━━━━━─────┐
│o abc  8 tests  8 build     │
├─────────────┐┌─────────────┐
│ ▀           ││ ▀           │
│start command││start command│
│: context can││: context can│
│celed        ││celed▐       │
│             ││             │
│             ││             │
│             ││             │
│             ││             │
│             ││             │
│             ││             │
│             ││             │
└─────────────┘└─────────────┘`},
			{":tabclose>",
				`┌━━━━━───────────────────────┐
│o abc  8 tests              │
├─────────────┐┌─────────────┐
│ ▀           ││AAAAAAAAAAAAA│
│start command││AAAAAAAAAAAAA│
│: context can││AAAAAAAAAAAAA│
│celed        ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
└─────────────┘└─────────────┘`},
			{":taskclose tests>",
				`┌━━━━━───────────────────────┐
│o abc                       │
├─────────────┐┌─────────────┐
│             ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
│             ││AAAAAAAAAAAAA│
└─────────────┘└─────────────┘`},
		}

		defaultConvertTabIcon = '8'
		e, mu, cleanup := newExForTestingTasks(t)
		runTaskSequenceSettled(t, handler.Sync(mu, e), 30, 15, cases)
		cleanup()
	})
}

// runTaskSequenceSettled drives a sequence of handlertest.SequenceTestCase like
// handlertest.TestHandlerSequence, but redraws until the rendered frame is
// stable before comparing. Task windows render a live terminal whose process
// output (e.g. a failed command's stderr) settles asynchronously, so a single
// draw immediately after Handle can race the emulator reflow. This driver lives
// in the test only; production rendering is unchanged.
func runTaskSequenceSettled(
	t *testing.T, h tui.Handler, width, height int,
	cases []handlertest.SequenceTestCase,
) {
	t.Helper()
	h.Resize(width, height)
	for i, tcase := range cases {
		handleTaskInputSequence(h, tcase.InputSequence)

		got := drawSettled(t, h, width, height, tcase.Expected)
		assert.Equal(t, maskTaskInterior(tcase.Expected), maskTaskInterior(got),
			"test case %d (input: %s)", i, tcase.InputSequence)
	}
}

// maskTaskInterior blanks the live-terminal interior of task windows so the
// golden comparison only checks the deterministic window frame and tab bar.
// A failed task renders a real process's stderr ("...context canceled") whose
// emulator reflow/wrapping is not byte-stable across runs, while the window
// frame and focus highlight (what these cases exercise) are. Frame glyphs and
// the top tab-bar rows are preserved; everything else is replaced with spaces.
func maskTaskInterior(frame string) string {
	const frameGlyphs = "│┌┐└┘├┤┬┴─━╮╭╰╯█●"
	lines := strings.Split(frame, "\n")
	for i, line := range lines {
		if i < 2 {
			continue // outer top border + tab-bar labels are deterministic
		}
		runes := []rune(line)
		for j, r := range runes {
			if r == ' ' || strings.ContainsRune(frameGlyphs, r) {
				continue
			}
			runes[j] = ' '
		}
		lines[i] = string(runes)
	}
	return strings.Join(lines, "\n")
}

// handleTaskInputSequence replays an InputSequence using the legacy
// handlertest character conventions (literal space is KeySpace, ':' opens the
// command prompt, '>' is Enter, '<' is Esc), matching the sequences embedded in
// the task golden cases.
func handleTaskInputSequence(h tui.Handler, seq string) {
	escapeNext := false
	for _, r := range seq {
		if escapeNext {
			escapeNext = false
			h.Handle(term.Event{Ch: r, Type: term.EventKey})
			continue
		}
		switch r {
		case ':':
			h.Handle(term.Event{Mod: term.ModCtrl, Ch: '\\', Type: term.EventKey})
		case ' ':
			h.Handle(term.Event{Key: term.KeySpace, Type: term.EventKey})
		case '>':
			h.Handle(term.Event{Key: term.KeyEnter, Type: term.EventKey})
		case '<':
			h.Handle(term.Event{Key: term.KeyEsc, Type: term.EventKey})
		case '\\':
			escapeNext = true
		default:
			h.Handle(term.Event{Ch: r, Type: term.EventKey})
		}
	}
}

// drawSettled redraws until two consecutive frames match (the emulator has
// stopped reflowing) or the frame equals want, whichever comes first, then
// returns the last frame. A bounded retry budget keeps a genuinely failing
// case from hanging.
func drawSettled(
	t *testing.T, h tui.Handler, width, height int, want string,
) string {
	t.Helper()
	draw := func() string {
		w := term.NewStringWriter(width, height)
		require.NoError(t, w.Clear(term.Attributes{}))
		h.Draw(w)
		if cursor, _, ok := h.Cursor(); ok {
			w.SetCursor(cursor)
		}
		require.NoError(t, w.Flush())
		return w.String()
	}
	prev := draw()
	for range 100 {
		if prev == want {
			return prev
		}
		time.Sleep(5 * time.Millisecond)
		cur := draw()
		if cur == prev {
			return cur
		}
		prev = cur
	}
	return prev
}

func TestEcho(t *testing.T) {
	t.Run("events get dispatched", func(t *testing.T) {
		cases := []handlertest.SequenceTestCase{
			{`:edit hello.go>:echo 01234>`,
				`┌━━━━━━━━━━──────────────────┐
│o hello.go                  │
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
└────────────────────────────┘`},
		}

		opts := []text.Option{
			text.WithCommandKey(testCommandKey),
		}
		var e testEx
		var i int
		publishEvent := func(ev term.Event) bool {
			if ev.Type == term.EventInterrupt {
				return true
			}
			assert.Equal(t, string(ev.Ch), strconv.Itoa(i))
			i++
			return true
		}

		e = newExForTestingWithWorkspace(t, &testLoader{},
			texttest.NopEditor(), vte.DefaultConfig(),
			publishEvent, clipboard.NewInMemory(), opts...)
		defer e.Close()

		handlertest.TestHandlerSequence(t, e, 30, 15, cases)
	})

	t.Run("recursive register expansion returns error before publishing events", func(t *testing.T) {
		clip := registerset.New(clipboard.NewInMemory())
		require.NoError(t, clip.Copy("a", clipboard.Data{Text: "{register}a"}))

		published := 0
		e := newExForTestingWithWorkspace(t, &testLoader{},
			texttest.NopEditor(), vte.DefaultConfig(),
			func(ev term.Event) bool {
				published++
				return true
			}, clip,
			text.WithCommandKey(testCommandKey),
		)
		defer e.Close()

		err := e.echo(context.Background(), "{register}a")
		require.EqualError(t, err, `expand register "a": recursive register expansion detected: a -> a`)
		require.Zero(t, published)
	})

	t.Run("mutually recursive register expansion returns error before publishing events", func(t *testing.T) {
		clip := registerset.New(clipboard.NewInMemory())
		require.NoError(t, clip.Copy("a", clipboard.Data{Text: "{register}b"}))
		require.NoError(t, clip.Copy("b", clipboard.Data{Text: "{register}a"}))

		published := 0
		e := newExForTestingWithWorkspace(t, &testLoader{},
			texttest.NopEditor(), vte.DefaultConfig(),
			func(ev term.Event) bool {
				published++
				return true
			}, clip,
			text.WithCommandKey(testCommandKey),
		)
		defer e.Close()

		err := e.echo(context.Background(), "{register}a")
		require.EqualError(t, err, `expand register "a": expand register "b": recursive register expansion detected: a -> b -> a`)
		require.Zero(t, published)
	})

	t.Run("{prompt} instruction", func(t *testing.T) {
		cases := []handlertest.SequenceTestCase{
			{`:echo {prompt}edit\<space\>hello.go\<enter\>>`,
				`┌━━━━━━━━━━──────────────────┐
│o hello.go                  │
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
└────────────────────────────┘`},
		}

		opts := []text.Option{
			text.WithCommandKey(testCommandKey),
		}
		var e testEx
		publishEvent := func(ev term.Event) bool {
			if ev.Type == term.EventInterrupt {
				return true
			}
			e.Handle(ev)
			return true
		}

		e = newExForTestingWithWorkspace(t, &testLoader{},
			texttest.NopEditor(), vte.DefaultConfig(),
			publishEvent, clipboard.NewInMemory(), opts...)
		defer e.Close()

		handlertest.TestHandlerSequence(t, e, 30, 15, cases)
	})
}

// TestExEchoMultipleArgs covers the per-argv-element parsing of the
// `echo` ex command: each argument is parsed independently with
// parseEchoKeys, and resulting key sequences are concatenated. Used
// to be a join-with-space + parse, which spuriously injected a literal
// <space> key between logically independent argv elements.
func TestExEchoMultipleArgs(t *testing.T) {
	var published []term.Event
	publishEvent := func(ev term.Event) bool {
		if ev.Type == term.EventInterrupt {
			return true
		}
		published = append(published, ev)
		return true
	}
	e := newExForTestingWithWorkspace(t, &testLoader{},
		texttest.NopEditor(), vte.DefaultConfig(),
		publishEvent, clipboard.NewInMemory(),
		text.WithCommandKey(testCommandKey),
	)
	defer e.Close()

	require.NoError(t, e.echo(context.Background(), "<space>", "<enter>"))
	require.Len(t, published, 2)
	assert.Equal(t, term.KeySpace, published[0].Key)
	assert.Equal(t, term.KeyEnter, published[1].Key)
}

// TestSearchAstAliasesFromRuneStar is the RUNE-123 regression covering
// the actual `searchfunc` / `searchvar` / `searchtype` aliases shipped
// in cmd/rune/rune.star. Their bodies use both echo's `<…>` key syntax
// and the `|` separator inside the searchast query — all of which the
// previous shell-style Layer 1 tokenizer would split incorrectly,
// causing the recursive `echo` dispatch to fail with an
// `invalid syntax` error notification. With the layered tokenizer
// each alias body survives unchanged as a single argv element, and
// parseEchoKeys accepts it.
func TestSearchAstAliasesFromRuneStar(t *testing.T) {
	// Source of truth: cmd/rune/rune.star. Keep these in sync with
	// the strings declared there.
	const (
		searchfuncBody = `echo {prompt}searchast<space>locals.scm<space>local.definition.method|local.definition.function<enter>`
		searchvarBody  = `echo {prompt}searchast<space>locals.scm<space>local.definition.var<enter>`
		searchtypeBody = `echo {prompt}searchast<space>locals.scm<space>local.definition.type<enter>`
	)
	cases := []struct {
		alias string
		body  string
	}{
		{"searchfunc", searchfuncBody},
		{"searchvar", searchvarBody},
		{"searchtype", searchtypeBody},
	}
	for _, tc := range cases {
		t.Run(tc.alias, func(t *testing.T) {
			publishEvent := func(ev term.Event) bool { return true }
			e := newExForTestingWithWorkspace(t, &testLoader{},
				texttest.NopEditor(), vte.DefaultConfig(),
				publishEvent, clipboard.NewInMemory(),
				text.WithCommandKey(testCommandKey),
				text.WithCommandAliases(map[string]text.CommandAlias{
					tc.alias: {Commands: []string{tc.body}},
				}),
			)
			defer e.Close()

			require.NoError(t, e.dispatchCommand(tc.alias))
		})
	}
}

// TestDispatchAliasCycle covers the runtime alias-recursion guard in
// ex.dispatchExpanded. The config-load validator (text.ValidateCommandAliases)
// only catches cycles whose whole target equals an alias name, so a
// cyclic step that carries arguments slips past it and must be stopped
// at dispatch time instead of recursing until the stack overflows.
func TestDispatchAliasCycle(t *testing.T) {
	cases := []struct {
		name    string
		aliases map[string]text.CommandAlias
		invoke  string
	}{
		{
			name:    "self cycle with arguments",
			aliases: map[string]text.CommandAlias{"a": {Commands: []string{"a x"}}},
			invoke:  "a",
		},
		{
			name: "mutual cycle with arguments",
			aliases: map[string]text.CommandAlias{
				"a": {Commands: []string{"b x"}},
				"b": {Commands: []string{"a y"}},
			},
			invoke: "a",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			publishEvent := func(ev term.Event) bool { return true }
			e := newExForTestingWithWorkspace(t, &testLoader{},
				texttest.NopEditor(), vte.DefaultConfig(),
				publishEvent, clipboard.NewInMemory(),
				text.WithCommandKey(testCommandKey),
				text.WithCommandAliases(tc.aliases),
			)
			defer e.Close()

			err := e.dispatchCommand(tc.invoke)
			require.Error(t, err)
			require.Contains(t, err.Error(), "alias cycle through")
		})
	}
}

// captureLoader is a testLoader that records the workspaceapi.Cmd values
// passed to StartCommand so tests can assert what argv reaches the
// VTE-bound `!!` plugin executor (post reshellQuoteArgs + shell.Fields).
type captureLoader struct {
	testLoader
	startErr error
	cmds     []workspaceapi.Cmd
}

func (c *captureLoader) StartCommand(
	ctx context.Context, cmd workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	c.cmds = append(c.cmds, cmd)
	return 0, c.startErr
}

// envSourceForURI returns a cmdenv.Source that resolves $WORKSPACE
// from the supplied URI using the production helpers in
// ide/workspace_env.go.
func envSourceForURI(uri workspaceapi.URI) cmdenv.Source {
	return func(name string) (string, bool) {
		switch name {
		case "WORKSPACE":
			return workspaceBasename(uri), true
		case "WORKSPACE_URI":
			return uri.String(), true
		case "WORKSPACE_PATH":
			return uri.Path(), true
		}
		return "", false
	}
}

// TestPluginWaitAssignmentCapturesIntoAliasChain verifies the
// alias-variable-capture feature introduced for RUNE-178: a `!!`
// step whose body assigns variables (e.g. `VAR=value`) publishes
// those values into the surrounding alias chain so subsequent steps
// in the same alias dispatch can reference them via $VAR.
//
// The capture is performed by mvdan.cc/sh/v3/interp inside
// runShellLineViaInterp; the chain env scope is plumbed through
// text.Component.DispatchCommand. Together they let aliases like
// worktreeopen feed `git worktree list`'s output into a subsequent
// `workspaceopen $WORKTREE` step.
func TestPluginWaitAssignmentCapturesIntoAliasChain(t *testing.T) {
	t.Run("literal assignment is visible to next step", func(t *testing.T) {
		captured := newExForCapturingCommand(t, []string{
			"!! WORKTREE=/tmp/foo",
			"workspaceopen $WORKTREE",
		})
		assert.Equal(t, "workspaceopen", captured.Name)
		assert.Equal(t, []string{"/tmp/foo"}, captured.Args,
			"the literal assignment from the !! step must be "+
				"visible to the next alias step via $VAR expansion")
	})

	t.Run("cmdsubst assignment is visible to next step", func(t *testing.T) {
		captured := newExForCapturingCommand(t, []string{
			// echo prints "hello"; the alias chain must see the
			// captured value rather than a literal "$(echo …)".
			// WORKTREE is the variable name used by the
			// production worktreeopen alias; intentionally picked
			// over $WORD which is a Rune-builtin name.
			"!! WORKTREE=$(/bin/echo /tmp/foo)",
			"workspaceopen $WORKTREE",
		})
		assert.Equal(t, "workspaceopen", captured.Name)
		assert.Equal(t, []string{"/tmp/foo"}, captured.Args,
			"command substitution inside a !! step must execute "+
				"and the result must be published to the chain env")
	})

	// Regression for RUNE-178 / worktreeopen: awk-internal field
	// references in a `!!` body are written as $$1/$$2 so Rune's
	// positional-arg validator does not consume them and the shell
	// sees a literal `$1` / `$2`. The alias has only one Rune
	// positional but its awk script references $$1 / $$2.
	t.Run("double-dollar escape defers $N to the shell", func(t *testing.T) {
		captured := newExForCapturingCommand(t, []string{
			`!! WORKTREE=$(/bin/echo a b | awk '$$2=="b" {print $$1}')`,
			"workspaceopen $WORKTREE",
		})
		assert.Equal(t, "workspaceopen", captured.Name)
		assert.Equal(t, []string{"a"}, captured.Args,
			"$$N must be passed to the shell as a literal $N "+
				"so awk (and other shell-internal $N consumers) "+
				"can use them without Rune intervening")
	})

	t.Run("parameter expansion uses chain var captured by previous step", func(t *testing.T) {
		captured := newExForCapturingCommand(t, []string{
			`!! ROOT=/Users/ernestrc/src/idelsp`,
			`!! ROOT_NAME=${ROOT##*/}`,
			"workspaceopen $ROOT_NAME",
		})
		assert.Equal(t, "workspaceopen", captured.Name)
		assert.Equal(t, []string{"idelsp"}, captured.Args,
			"a !! step's ${VAR##pattern} must expand against the "+
				"chain var captured by an earlier !! step (the "+
				"worktreenew alias relies on this for ROOT_NAME)")
	})
}

// newExForCapturingCommand runs aliasCommands as the body of an alias
// named "chaintest" and returns the textapi.Command dispatched by
// the *non-!!* second step. The first !! step is wired to interp via
// the production runShellLineViaInterp path; its commands (`echo`
// etc.) execute via the default ExecHandler since the workspace
// executor used by newExForTesting only records to captureLoader.
// Only the second step's dispatched command is captured here — the
// purpose is to assert what argv the alias chain produced for it.
func newExForCapturingCommand(t *testing.T, aliasCommands []string) textapi.Command {
	t.Helper()
	var got textapi.Command
	var subscribed bool
	opts := []text.Option{
		text.WithCommandOverlayConfig(testCommandOverlayConfig()),
		text.WithCommandKey(testCommandKey),
		text.WithCommandAliases(map[string]text.CommandAlias{
			"chaintest": {Commands: aliasCommands},
		}),
	}
	b := newExForTestingWithWorkspace(t, &realExecLoader{testLoader: testLoader{}},
		texttest.NopEditor(), vte.DefaultConfig(),
		nopPublishEvent, clipboard.NewInMemory(), opts...)
	defer b.Close()

	// Subscribe a sink command "workspaceopen" so we can observe the
	// fully-expanded argv that the chain produced for the
	// post-capture step.
	err := b.comp.SubscribeCommand(textapi.CommandManual{
		Name: "workspaceopen",
	}, text.FuncCommandHandler(
		func(_ context.Context, cmd textapi.Command) error {
			got = cmd
			subscribed = true
			return nil
		}, nil,
	))
	require.NoError(t, err)

	require.NoError(t, b.ex.dispatchCommand("chaintest"))
	b.drainAliasRuns()
	require.True(t, subscribed,
		"the post-capture alias step must have been dispatched")
	return got
}

// realExecLoader is a testLoader that actually runs simple commands
// via os/exec in the host filesystem so plugin tests that exercise
// mvdan/sh interp's $(…) capture path can observe real stdout. It is
// safe to use only for tests that intentionally invoke whitelisted
// host binaries (e.g. /bin/echo); it is not appropriate for tests
// that depend on workspace-scoped behaviour.
type realExecLoader struct {
	testLoader
}

func (w *realExecLoader) StartCommand(
	ctx context.Context, cmd workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	c := exec.CommandContext(ctx, cmd.Path, cmd.Args...)
	c.Stdin = cmd.Stdin
	c.Stdout = cmd.Stdout
	c.Stderr = cmd.Stderr
	// Run synchronously: interp expects stdout writes to be flushed
	// before the ExecHandler returns. Doing Start + async Wait
	// races with interp's bytes.Buffer reads when capturing $(…).
	runErr := c.Run()
	if cmd.Watcher != nil {
		cmd.Watcher.WatchProcess() <- runErr
	}
	if runErr != nil {
		return 0, runErr
	}
	if c.Process != nil {
		return workspaceapi.Pid(c.Process.Pid), nil
	}
	return 0, nil
}

// TestWorktreeRemoveAliasResolvesFromInsideWorktree is the RUNE-178
// regression: invoking worktreeremove from inside a worktree used to
// resolve a slug from the focused workspace, so the rebuilt path
// never matched git's worktree registry. Handing git the bare
// basename works from parent and from any sibling worktree.
func TestWorktreeRemoveAliasResolvesFromInsideWorktree(t *testing.T) {
	const (
		fixedAliasBody = `!! git worktree remove $1`
		worktreeName   = "tabs-refresh-gpt"
	)
	dataDir := t.TempDir()
	t.Setenv("RUNE_DATADIR", dataDir)

	// Simulate the bug's focused-workspace context: the user is sitting
	// inside the worktree workspace that worktreenew created earlier,
	// not the parent repo.
	worktreePath := filepath.Join(dataDir, "worktrees",
		"blue-abcd", worktreeName)
	worktreeURI, err := workspaceapi.ParseURI("file://" + worktreePath)
	require.NoError(t, err)

	run := func(t *testing.T, aliasBody string) []string {
		t.Helper()
		captured := &captureLoader{
			testLoader: testLoader{},
			startErr:   errors.New("captured-start-command"),
		}
		envSource := envSourceForURI(worktreeURI)
		publishEvent := func(ev term.Event) bool { return true }
		e := newExForTestingWithWorkspace(t, captured,
			texttest.NopEditor(), vte.DefaultConfig(),
			publishEvent, clipboard.NewInMemory(),
			text.WithCommandKey(testCommandKey),
			text.WithEnvSource(envSource),
			text.WithCommandAliases(map[string]text.CommandAlias{
				"worktreeremove": {Commands: []string{aliasBody}},
			}),
		)
		defer e.Close()
		_ = e.dispatchCommand("worktreeremove", worktreeName)
		e.drainAliasRuns()
		require.NotEmpty(t, captured.cmds,
			"!! must have reached the executor's StartCommand")
		got := captured.cmds[0]
		return append([]string{got.Path}, got.Args...)
	}

	t.Run("fix: alias hands git the bare basename", func(t *testing.T) {
		argv := run(t, fixedAliasBody)
		assert.Equal(t, []string{
			"git", "worktree", "remove", worktreeName,
		}, argv,
			"worktreeremove must hand git only the basename so that "+
				"git-worktree(1) resolves the target from .git/worktrees "+
				"regardless of which workspace is focused")
	})
}

func TestMoveTabs(t *testing.T) {
	cases := []handlertest.SequenceTestCase{
		{":edit caliu.go>:edit boira.go>b",
			`┌────────────━━━━━━━━━━──────┐
│o caliu.go  o boira.go      │
├────────────────────────────┤
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
└────────────────────────────┘`},
		{":tabmove 1>",
			`┌━━━━━━━━━━──────────────────┐
│o boira.go  o caliu.go      │
├────────────────────────────┤
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
└────────────────────────────┘`},
		{":tabmove 99>",
			`┌────────────━━━━━━━━━━──────┐
│o caliu.go  o boira.go      │
├────────────────────────────┤
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
└────────────────────────────┘`},
		{":tabmove left>",
			`┌━━━━━━━━━━──────────────────┐
│o boira.go  o caliu.go      │
├────────────────────────────┤
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
└────────────────────────────┘`},
		{":tabmove left>",
			`┌━━━━━━━━━━────┌─────────────┐
│o boira.go  o │ tab is      │
├──────────────│ already at  │
│BBBBBBBBBBBBBB│ the start   │
│BBBBBBBBBBBBBB│ of the list │
│BBBBBBBBBBBBBB└─────────────┘
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
└────────────────────────────┘`},
		{":notificationCloseAll>:tabmove right>",
			`┌────────────━━━━━━━━━━──────┐
│o caliu.go  o boira.go      │
├────────────────────────────┤
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
└────────────────────────────┘`},
		{":tabmove right>",
			`┌────────────━━┌─────────────┐
│o caliu.go  o │ tab is      │
├──────────────│ already at  │
│BBBBBBBBBBBBBB│ the end of  │
│BBBBBBBBBBBBBB│ the list    │
│BBBBBBBBBBBBBB└─────────────┘
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
└────────────────────────────┘`},
		{":notificationCloseAll>:windowsplit right>:tabprevious>",
			`┌━━━━━━━━━━──────────────────┐
│o caliu.go  o boira.go      │
├─────────────┐┌─────────────┐
│BBBBBBBBBBBBB││AAAAAAAAAAAAA│
│BBBBBBBBBBBBB││AAAAAAAAAAAAA│
│BBBBBBBBBBBBB││AAAAAAAAAAAAA│
│BBBBBBBBBBBBB││AAAAAAAAAAAAA│
│BBBBBBBBBBBBB││AAAAAAAAAAAAA│
│BBBBBBBBBBBBB││AAAAAAAAAAAAA│
│BBBBBBBBBBBBB││AAAAAAAAAAAAA│
│BBBBBBBBBBBBB││AAAAAAAAAAAAA│
│BBBBBBBBBBBBB││AAAAAAAAAAAAA│
│BBBBBBBBBBBBB││AAAAAAAAAAAAA│
│BBBBBBBBBBBBB││AAAAAAAAAAAAA│
└─────────────┘└─────────────┘`},
		{":tabmove right>",
			`┌────────────━━━━━━━━━━──────┐
│o boira.go  o caliu.go      │
├─────────────┐┌─────────────┐
│BBBBBBBBBBBBB││AAAAAAAAAAAAA│
│BBBBBBBBBBBBB││AAAAAAAAAAAAA│
│BBBBBBBBBBBBB││AAAAAAAAAAAAA│
│BBBBBBBBBBBBB││AAAAAAAAAAAAA│
│BBBBBBBBBBBBB││AAAAAAAAAAAAA│
│BBBBBBBBBBBBB││AAAAAAAAAAAAA│
│BBBBBBBBBBBBB││AAAAAAAAAAAAA│
│BBBBBBBBBBBBB││AAAAAAAAAAAAA│
│BBBBBBBBBBBBB││AAAAAAAAAAAAA│
│BBBBBBBBBBBBB││AAAAAAAAAAAAA│
│BBBBBBBBBBBBB││AAAAAAAAAAAAA│
└─────────────┘└─────────────┘`},
		{":windowfocus left>:tabmove right>",
			`┌────────────━━━━━━━━━━──────┐
│o caliu.go  o boira.go      │
┌─────────────┐┌─────────────┤
│BBBBBBBBBBBBB││AAAAAAAAAAAAA│
│BBBBBBBBBBBBB││AAAAAAAAAAAAA│
│BBBBBBBBBBBBB││AAAAAAAAAAAAA│
│BBBBBBBBBBBBB││AAAAAAAAAAAAA│
│BBBBBBBBBBBBB││AAAAAAAAAAAAA│
│BBBBBBBBBBBBB││AAAAAAAAAAAAA│
│BBBBBBBBBBBBB││AAAAAAAAAAAAA│
│BBBBBBBBBBBBB││AAAAAAAAAAAAA│
│BBBBBBBBBBBBB││AAAAAAAAAAAAA│
│BBBBBBBBBBBBB││AAAAAAAAAAAAA│
│BBBBBBBBBBBBB││AAAAAAAAAAAAA│
└─────────────┘└─────────────┘`},
	}

	opts := []text.Option{
		text.WithCommandKey(testCommandKey),
	}
	e := newExForTestingWithWorkspace(t, &testLoader{},
		texttest.NopEditor(), vte.DefaultConfig(),
		nopPublishEvent, clipboard.NewInMemory(), opts...)
	defer e.Close()
	handlertest.TestHandlerSequence(t, e, 30, 15, cases)
}

func TestViewForceWrite(t *testing.T) {
	cases := []handlertest.SequenceTestCase{
		{":view caliu.go>b",
			`┌━━━━━━━━━━──────────────────┐
│o caliu.go                  │
├────────────────────────────┤
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
└────────────────────────────┘`},
		{":write>",
			`┌━━━━━━━━━━────┌─────────────┐
│o caliu.go    │ save        │
├──────────────│ 'caliu.go': │
│BBBBBBBBBBBBBB│  file is    │
│BBBBBBBBBBBBBB│ not         │
│BBBBBBBBBBBBBB│ writable    │
│BBBBBBBBBBBBBB└─────────────┘
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
└────────────────────────────┘`},
		{":notificationCloseAll>:write!>",
			`┌━━━━━━━━━━──────────────────┐
│o caliu.go                  │
├────────────────────────────┤
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
└────────────────────────────┘`},
		{":write>",
			`┌━━━━━━━━━━──────────────────┐
│o caliu.go                  │
├────────────────────────────┤
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
└────────────────────────────┘`},
	}

	opts := []text.Option{
		text.WithCommandKey(testCommandKey),
	}
	e := newExForTestingWithWorkspace(t, &testLoader{},
		texttest.NopEditor(), vte.DefaultConfig(),
		nopPublishEvent, clipboard.NewInMemory(), opts...)
	defer e.Close()
	handlertest.TestHandlerSequence(t, e, 30, 15, cases)
}

func TestViewForceWriteAll(t *testing.T) {
	// Open two read-only tabs, then run :writeall and assert the
	// rendered output contains both save-failure notifications.
	// The two flushes complete on independent goroutines, so the
	// order in which their notification popups stack is not
	// deterministic; this test therefore checks for the presence of
	// each notification's text rather than a fixed golden frame.
	opts := []text.Option{
		text.WithCommandKey(testCommandKey),
	}
	e := newExForTestingWithWorkspace(t, &testLoader{},
		texttest.NopEditor(), vte.DefaultConfig(),
		nopPublishEvent, clipboard.NewInMemory(), opts...)
	defer e.Close()

	const width, height = 30, 15
	writer := term.NewStringWriter(width, height)
	setup := []handlertest.SequenceTestCase{
		{":view caliu.go>:view boira.go>b",
			`┌────────────━━━━━━━━━━──────┐
│o caliu.go  o boira.go      │
├────────────────────────────┤
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
└────────────────────────────┘`},
	}
	handlertest.TestHandlerSequenceWriter(t, writer, e, width, height, setup)

	// :writeall on two read-only files: both should produce
	// "save '<name>': file is not writable" notifications in any
	// order.
	feedAutoSaveSequence(t, e, ":writeall>")
	require.NoError(t, writer.Clear(term.Attributes{}))
	e.Draw(writer)
	require.NoError(t, writer.Flush())
	render := writer.String()
	assert.Contains(t, render, "'caliu.go'",
		"writeall should surface caliu.go failure notification")
	assert.Contains(t, render, "'boira.go'",
		"writeall should surface boira.go failure notification")
	assert.Contains(t, render, "not")
	assert.Contains(t, render, "writable")

	// After closing all notifications and forcing the write
	// (which clears the read-only flag), :writeall should succeed
	// silently.
	post := []handlertest.SequenceTestCase{
		{":notificationCloseAll>:writeall!>",
			`┌────────────━━━━━━━━━━──────┐
│o caliu.go  o boira.go      │
├────────────────────────────┤
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
└────────────────────────────┘`},
		{":writeall>",
			`┌────────────━━━━━━━━━━──────┐
│o caliu.go  o boira.go      │
├────────────────────────────┤
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBBBBBBBBBBBB│
└────────────────────────────┘`},
	}
	handlertest.TestHandlerSequenceWriter(t, writer, e, width, height, post)
}

func TestIntegrationUndoAfterOpen(t *testing.T) {
	cases := []handlertest.SequenceTestCase{
		{":edit<space>enm.go<enter>u",
			`┌━━━━━━━━────────────────────┐
│o enm.go                    │
├────────────────────────────┤
│▐                           │
└────────────────────────────┘`},
	}

	tempDir, err := os.MkdirTemp("", "")
	t.Cleanup(func() {
		_ = os.RemoveAll(tempDir)
	})
	ctx := context.Background()
	uri, err := workspaceapi.ParseURI(filepath.Join("file://", tempDir))
	require.NoError(t, err)
	fileScheme, err := workspace.NewFileScheme(ctx, config.NopConfig(), uri)
	require.NoError(t, err)
	workspace := workspace.NewSchemeWorkspace(uri, fileScheme, inlineSchedule)
	e := newExForTestingWithWorkspace(t, workspace, vi.Editor(),
		vte.DefaultConfig(), nopPublishEvent, clipboard.NewInMemory(),
		text.WithCommandKey(term.KeyComb{Ch: ':'}),
	)
	handlertest.RunHandlerSequence(t, e, 30, 5, cases)
	require.NoError(t, e.Close())
	require.NoError(t, workspace.Close())
	require.NoError(t, fileScheme.Close())
}

func TestCopyPath(t *testing.T) {
	tsuite := []struct {
		name   string
		cmd    string
		expect func() string
	}{
		{
			name:   "relative",
			cmd:    ":tabcopypath",
			expect: func() string { return "hello.go" },
		},
		{
			name: "absolute",
			cmd:  ":tabcopypath absolute",
			expect: func() string {
				absPath, _ := workspaceapi.ExpandPath(
					"hello.go", user.Current, os.Getwd)
				return absPath // e.g. /Users/ramon/Devel/go-tui/hello.go
			},
		},
	}
	for _, tcase := range tsuite {
		t.Run(tcase.name, func(t *testing.T) {
			cases := []handlertest.SequenceTestCase{
				{fmt.Sprintf(":edit hello.go>%s>", tcase.cmd),
					`┌━━━━┌─────────────┐
│o he│ file path   │
├────│ copied to   │
│AAAA│ clipboard   │
│AAAA└─────────────┘
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
└──────────────────┘`},
			}

			clip := clipboard.NewInMemory()
			opts := []text.Option{text.WithCommandKey(testCommandKey)}
			e := newExForTestingClipboard(t, texttest.NopEditor(), clip, opts...)
			defer e.Close()
			handlertest.TestHandlerSequence(t, e, 20, 10, cases)

			data, err := clip.Paste(clipboard.DefaultRegisterID)
			require.NoError(t, err)
			assert.Equal(t, tcase.expect(), data.Text)
		})
	}

}

func TestCopyPathNestedWorkspaceRelative(t *testing.T) {
	clip := clipboard.NewInMemory()
	e, fileScheme, tempDir := newExForTestingFileWorkspace(t, clip)
	defer func() { require.NoError(t, e.Close()) }()
	defer func() { require.NoError(t, fileScheme.Close()) }()

	relPath := filepath.Join("nested", "hello.go")
	require.NoError(t, os.MkdirAll(filepath.Join(tempDir, "nested"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, relPath), []byte("hello\n"), 0o644))

	uri, err := e.workspace.URI(relPath)
	require.NoError(t, err)
	_, err = e.editFileURI(uri, e.invokeWindow(), false)
	require.NoError(t, err)
	e.Resize(20, 10)
	require.NoError(t, e.tabcopypath(context.Background()))

	data, err := clip.Paste(clipboard.DefaultRegisterID)
	require.NoError(t, err)
	assert.Equal(t, filepath.ToSlash(relPath), data.Text)
}

func TestCopyLocation(t *testing.T) {
	ts := []struct {
		name   string
		cmd    string
		expect func(tempDir, relPath string) string
	}{
		{
			name: "relative",
			cmd:  ":tabcopylocation",
			expect: func(tempDir, relPath string) string {
				return filepath.ToSlash(relPath) + ":2"
			},
		},
		{
			name: "absolute",
			cmd:  ":tabcopylocation absolute",
			expect: func(tempDir, relPath string) string {
				return filepath.Join(tempDir, relPath) + ":2"
			},
		},
	}

	for _, tc := range ts {
		t.Run(tc.name, func(t *testing.T) {
			clip := clipboard.NewInMemory()
			e, fileScheme, tempDir := newExForTestingFileWorkspace(t, clip)
			defer func() { require.NoError(t, e.Close()) }()
			defer func() { require.NoError(t, fileScheme.Close()) }()

			relPath := filepath.Join("nested", "hello.go")
			require.NoError(t, os.MkdirAll(filepath.Join(tempDir, "nested"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(tempDir, relPath), []byte("one\ntwo\n"), 0o644))

			uri, err := e.workspace.URI(relPath)
			require.NoError(t, err)
			_, err = e.editFileURI(uri, e.invokeWindow(), false)
			require.NoError(t, err)
			e.Resize(20, 10)
			require.NoError(t, e.moveFocusCursor(1))
			args := strings.TrimPrefix(tc.cmd, ":tabcopylocation")
			require.NoError(t, e.tabcopylocation(context.Background(), strings.Fields(args)...))

			data, err := clip.Paste(clipboard.DefaultRegisterID)
			require.NoError(t, err)
			assert.Equal(t, tc.expect(tempDir, relPath), data.Text)
		})
	}
}

// TestClipboardCommandsPerEditor pins :clipboardcopy and :clipboardpaste
// against every built-in editor. Each editor must expose its active
// selection through tui.Handler.Selection and accept bracketed paste
// events, otherwise the commands silently report "nothing to copy".
func TestClipboardCommandsPerEditor(t *testing.T) {
	key := func(k term.Key, mod term.Modifier) term.Event {
		return term.Event{Type: term.EventKey, Key: k, Mod: mod}
	}
	ch := func(r rune) term.Event {
		return term.Event{Type: term.EventKey, Ch: r}
	}
	shiftRight := key(term.KeyArrowRight, term.ModShift)
	right := key(term.KeyArrowRight, 0)

	ts := []struct {
		name   string
		editor text.Editor
		// selects "ab" out of "abcde"
		selection []term.Event
		// vi ignores bracketed paste outside insert mode.
		pastes bool
	}{
		{"vi", vi.Editor(), []term.Event{ch('v'), ch('l')}, false},
		{"emacs shift selection", emacs.Editor(),
			[]term.Event{shiftRight, shiftRight}, true},
		{"emacs mark region", emacs.Editor(),
			[]term.Event{key(term.KeySpace, term.ModCtrl), right, right}, true},
		{"standard", standard.Editor(),
			[]term.Event{shiftRight, shiftRight}, true},
	}

	for _, tc := range ts {
		t.Run(tc.name, func(t *testing.T) {
			clip := clipboard.NewInMemory()
			tempDir := t.TempDir()
			uri, err := workspaceapi.ParseURI(filepath.Join("file://", tempDir))
			require.NoError(t, err)
			fileScheme, err := workspace.NewFileScheme(bgctx, config.NopConfig(), uri)
			require.NoError(t, err)
			defer func() { require.NoError(t, fileScheme.Close()) }()
			ws := workspace.NewSchemeWorkspace(uri, fileScheme, inlineSchedule)
			e := newExForTestingWithWorkspace(t, ws, tc.editor, vte.DefaultConfig(),
				nopPublishEvent, clip, text.WithCommandKey(testCommandKey))
			defer func() { require.NoError(t, e.Close()) }()

			require.NoError(t, os.WriteFile(
				filepath.Join(tempDir, "hello.go"), []byte("abcde\n"), 0o644))
			fileURI, err := e.workspace.URI("hello.go")
			require.NoError(t, err)
			tab, err := e.editFileURI(fileURI, e.invokeWindow(), false)
			require.NoError(t, err)
			e.Resize(40, 20)

			for _, ev := range tc.selection {
				e.Handle(ev)
			}
			require.NoError(t, e.copyToClipboard(bgctx))
			data, err := clip.Paste(clipboard.DefaultRegisterID)
			require.NoError(t, err)
			assert.Equal(t, "ab", data.Text)

			if !tc.pastes {
				return
			}
			// Clear the selection first: pasting over one replaces it.
			e.Handle(key(term.KeyEnd, 0))
			require.NoError(t, e.pasteFromClipboard(bgctx))
			h, ok := tab.Handler().(text.Handler)
			require.True(t, ok)
			assert.Contains(t, term.CellsToString(h.CellView().RawCells()), "abcdeab")
		})
	}
}

func newExForTestingFileWorkspace(
	t *testing.T, clip clipboard.Register,
) (testEx, schemeapi.Scheme, string) {
	t.Helper()
	tempDir := t.TempDir()
	ctx := context.Background()
	uri, err := workspaceapi.ParseURI(filepath.Join("file://", tempDir))
	require.NoError(t, err)
	fileScheme, err := workspace.NewFileScheme(ctx, config.NopConfig(), uri)
	require.NoError(t, err)
	workspace := workspace.NewSchemeWorkspace(uri, fileScheme, inlineSchedule)
	e := newExForTestingWithWorkspace(t, workspace, vi.Editor(),
		vte.DefaultConfig(), nopPublishEvent, clip,
		text.WithCommandKey(testCommandKey),
	)
	return e, fileScheme, tempDir
}

func TestCopyToClipboard(t *testing.T) {
	clip := clipboard.NewInMemory()
	testCopyToClipboard(t, clip, func(ed text.Editor, opts ...text.Option) (
		tui.Handler, browser.Browser, error,
	) {
		b := newExForTestingClipboard(t, texttest.NopEditor(), clip, opts...)
		defer b.Close()
		return b, b.Browser(), nil
	})
}

func testCopyToClipboard(
	t *testing.T, clip clipboard.Register, constructor browserConstructor,
) {
	cases := []handlertest.SequenceTestCase{
		{":edit hello.go>",
			`┌━━━━━━━━━━──────────────────┐
│o hello.go                  │
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
└────────────────────────────┘`},
		{":clipboardpaste>",
			`┌━━━━━━━━━━────┌─────────────┐
│o hello.go    │ nothing to  │
├──────────────│ paste       │
│AAAAAAAAAAAAAA└─────────────┘
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
└────────────────────────────┘`},
		{":noticloseall>:clipboardcopy>",
			`┌━━━━━━━━━━────┌─────────────┐
│o hello.go    │ copied to   │
├──────────────│ clipboard   │
│AAAAAAAAAAAAAA└─────────────┘
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
└────────────────────────────┘`},
		{":noticloseall>:clipboardpaste>",
			`┌━━━━━━━━━━──────────────────┐
│o hello.go                  │
├────────────────────────────┤
│DDDDDDDDDDDDDDDDDDDDDDDDDDDD│
│DDDDDDDDDDDDDDDDDDDDDDDDDDDD│
│DDDDDDDDDDDDDDDDDDDDDDDDDDDD│
│DDDDDDDDDDDDDDDDDDDDDDDDDDDD│
│DDDDDDDDDDDDDDDDDDDDDDDDDDDD│
│DDDDDDDDDDDDDDDDDDDDDDDDDDDD│
│DDDDDDDDDDDDDDDDDDDDDDDDDDDD│
│DDDDDDDDDDDDDDDDDDDDDDDDDDDD│
│DDDDDDDDDDDDDDDDDDDDDDDDDDDD│
│DDDDDDDDDDDDDDDDDDDDDDDDDDDD│
│DDDDDDDDDDDDDDDDDDDDDDDDDDDD│
└────────────────────────────┘`},
		{":####",
			`┌━━━━━━━━━━──────────────────┐
│o hello.go                  │
├────────────────────────────┤
│DDDDDDDDDDDDDDDDDDDDDDDDDDDD│
│DDDDDDDDDDDDDDDDDDDDDDDDDDDD│
│DDDDDDDDDDDDDDDDDDDDDDDDDDDD│
│DDDDDDDDDDDDDDDDDDDDDDDDDDDD│
┌────────────────────────────┐
│ AAAA▐                      │
│                            │
│                            │
└────────────────────────────┘
│DDDDDDDDDDDDDDDDDDDDDDDDDDDD│
│DDDDDDDDDDDDDDDDDDDDDDDDDDDD│
└────────────────────────────┘`},
	}

	opts := []text.Option{
		text.WithCommandKey(testCommandKey),
		text.WithCommandKeyBinding(term.KeyComb{Mod: term.ModCtrl, Ch: 'h'},
			[][]string{{cmdClipboardPaste}}),
	}
	bh, _, err := constructor(texttest.NopEditor(), opts...)
	require.NoError(t, err)

	handlertest.TestHandlerSequence(t, bh, 30, 15, cases)

	data, err := clip.Paste(clipboard.DefaultRegisterID)
	require.NoError(t, err)
	assert.Equal(t, "A", data.Text)
}

func notificationsConfig() notifications.Config {
	ret := defaultNotificationsConfig()
	ret.Width = 15
	ret.ProgressBar = false // deterministic tests
	return ret
}

func touchTestFile(t *testing.T, scheme schemeapi.Scheme, name string) {
	f, werr := scheme.OpenFile(name, os.O_CREATE, 0666)
	require.Nil(t, werr)
	require.NoError(t, f.Sync())
}

func testCommandOverlayConfig() text.CommandOverlayConfig {
	return text.CommandOverlayConfig{
		ShowManual:       false,
		ShowProgressHint: false,
	}
}

type testVte struct {
	component.String

	initialCmd       string
	calledClose      bool
	defAttr          term.Attributes
	onFocusChange    []bool
	restoredSnapshot bool
	cursor           term.Coordinates
	seekOffset       int

	isComplete bool
	uri        workspaceapi.URI
	title      string
}

func newTestVte() *testVte {
	return newTestVteWithConfig(nil)
}

func newTestVteWithConfig(initialCmd []string) *testVte {
	ret := new(testVte)
	ret.initialCmd = strings.Join(initialCmd, " ")
	ret.String = component.NewString(ret.initialCmd)
	return ret
}

func (t *testVte) Handle(ev term.Event) (bool, bool) {
	return false, false
}

func (t *testVte) SeekUp() bool {
	return false
}

func (t *testVte) SeekDown() bool {
	return false
}

func (t *testVte) SeekOffset() int {
	return t.seekOffset
}

func (t *testVte) MaxSeekOffset() int {
	return 0
}

func (v *testVte) UsedAlternateBuffer() bool {
	return false
}

func (v *testVte) ClearPrimaryBuffer() bool {
	return true
}

func (v *testVte) Snapshot() (vte.Snapshot, error) {
	title := v.title
	if title == "" {
		title = "terminal"
	}
	return vte.Snapshot{
		Schema: 1,
		Title:  title,
		Width:  10,
		Height: 10,
		ScrollOffset: term.Coordinates{
			Y: 2,
		},
		Primary: vte.ScreenSnapshot{
			Cells:  term.StringToCells(v.initialCmd),
			Cursor: term.Coordinates{X: 4, Y: 1},
		},
	}, nil
}

func (v *testVte) RestoreFromSnapshot(snapshot vte.Snapshot) error {
	v.restoredSnapshot = true
	v.initialCmd = term.CellsToString(snapshot.ActiveCells())
	v.String = component.NewString(v.initialCmd)
	v.cursor = snapshot.Primary.Cursor
	v.seekOffset = snapshot.ScrollOffset.Y
	return nil
}

func (t *testVte) Cursor() (ret term.Coordinates, style term.CursorStyle, show bool) {
	show = true
	if t.restoredSnapshot {
		ret = t.cursor
		return
	}
	ret = term.Coordinates{X: len(t.initialCmd)}
	return
}

func (t *testVte) Selection() (string, bool) {
	return "", false
}

func (t *testVte) Dimensions() (int, int) {
	return 10, 10
}

func (v *testVte) Close() error {
	if v.calledClose {
		return errors.New("called close twice")
	}
	v.calledClose = true
	return nil
}

func (v *testVte) OnFocusChange(inFocus bool) {
	v.onFocusChange = append(v.onFocusChange, inFocus)
}

func (v *testVte) SetDefaultAttributes(attr term.Attributes) {
	v.defAttr = attr
}

func (v *testVte) IsComplete() bool {
	return v.isComplete
}

func (v *testVte) URI() workspaceapi.URI {
	return v.uri
}

func (v *testVte) Title() string {
	return v.title
}

func newExForTestingTasks(t *testing.T) (testEx, *sync.Mutex, func()) {
	mu := new(sync.Mutex)
	opts := []text.Option{
		text.WithCommandKey(testCommandKey),
		text.WithFloatingNoMaxSize(false),
	}
	tempDir, err := os.MkdirTemp("", "")
	t.Cleanup(func() {
		_ = os.RemoveAll(tempDir)
	})
	require.NoError(t, err)
	uri, err := workspaceapi.ParseURI(filepath.Join("file://", tempDir))
	require.NoError(t, err)
	ctx := context.Background()
	fileScheme, err := workspace.NewFileScheme(ctx, config.NopConfig(), uri)
	require.NoError(t, err)
	defer fileScheme.Close()
	workspace := workspace.NewSchemeWorkspace(uri, fileScheme, inlineSchedule)
	vteConfig := vte.DefaultConfig()
	vteConfig.ScheduleNextTick = func(fn func()) bool {
		go func() {
			mu.Lock()
			defer mu.Unlock()
			fn()
		}()
		return true
	}
	e := newExForTestingTerminal(t, workspace, texttest.NopEditor(),
		vteConfig, nopPublishEvent, deterministicTaskBarConfig(), opts...)
	return e, mu, func() {
		mu.Lock()
		defer mu.Unlock()
		require.NoError(t, e.Close())
	}
}

// deterministicTaskBarConfig returns a plugin bar config without the
// elapsed-time component so task golden tests do not depend on wall-clock
// timing in the rendered status bar.
func deterministicTaskBarConfig() plugin.BarConfig {
	cfg := plugin.DefaultBarConfig()
	layout := make([]plugin.BarComponent, 0, len(cfg.Layout))
	for _, c := range cfg.Layout {
		if c.Type == plugin.BarElapsed {
			continue
		}
		layout = append(layout, c)
	}
	cfg.Layout = layout
	return cfg
}

// the calling workspace is always in focus
type workspaceManagerMock struct {
	workspace    *ex
	attrs        map[workspaceapi.URI]term.Attributes
	wantFocusURI workspaceapi.URI
	byURIHash    map[string]browserapi.Notifications
}

func (w *workspaceManagerMock) focusHandler() tui.Handler {
	return w.workspace
}

func (w *workspaceManagerMock) notificationsForURIHash(
	hash string,
) browserapi.Notifications {
	return w.byURIHash[hash]
}

func (w *workspaceManagerMock) focusURI() workspaceapi.URI {
	if w.workspace == nil || w.workspace.workspace == nil {
		return w.wantFocusURI
	}
	uri, _ := w.workspace.workspace.URI(".")
	return uri
}

func (w *workspaceManagerMock) setWorkspaceRequiresAttention(
	uri workspaceapi.URI, attrs term.Attributes,
) {
	if w.attrs == nil {
		w.attrs = make(map[workspaceapi.URI]term.Attributes)
	}
	w.attrs[uri] = attrs
}

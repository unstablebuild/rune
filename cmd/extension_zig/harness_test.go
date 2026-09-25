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

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	iofs "io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
)

// assertErr is a sentinel error scripted into the fake executor to
// simulate a failed command.
var assertErr = errors.New("scripted failure")

// stubConfig is a config.Config that resolves string, bool, and slice
// keys from maps and reports ErrNotFound for everything else. It embeds
// the interface so only the overridden getters exist; the embedded nil
// panics if any other method is called, which the tests never do.
type stubConfig struct {
	config.Config
	strings map[string]string
	bools   map[string]bool
	slices  map[string][]any
}

func newStubConfig(strings map[string]string) stubConfig {
	return stubConfig{strings: strings}
}

func (c stubConfig) GetString(k string) (string, error) {
	if v, ok := c.strings[k]; ok {
		return v, nil
	}
	return "", config.ErrNotFound
}

func (c stubConfig) GetBool(k string) (bool, error) {
	if v, ok := c.bools[k]; ok {
		return v, nil
	}
	return false, config.ErrNotFound
}

func (c stubConfig) GetSlice(k string) ([]any, error) {
	if v, ok := c.slices[k]; ok {
		return v, nil
	}
	return nil, config.ErrNotFound
}

// fakeFileInfo satisfies os.FileInfo for paths scripted into fakeFS.
type fakeFileInfo struct {
	name string
	dir  bool
}

func (f fakeFileInfo) Name() string       { return f.name }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) Mode() os.FileMode  { return 0o755 }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return f.dir }
func (f fakeFileInfo) Sys() any           { return nil }

type fakeDirEntry struct {
	name string
	dir  bool
}

func (e fakeDirEntry) Name() string               { return e.name }
func (e fakeDirEntry) IsDir() bool                { return e.dir }
func (e fakeDirEntry) Type() os.FileMode          { return 0 }
func (e fakeDirEntry) Info() (os.FileInfo, error) { return fakeFileInfo{name: e.name, dir: e.dir}, nil }

// memFile is a read-only workspaceapi.File over an in-memory buffer.
type memFile struct {
	*bytes.Reader
	name string
}

func (f *memFile) Name() string               { return f.name }
func (f *memFile) Stat() (os.FileInfo, error) { return fakeFileInfo{name: f.name}, nil }
func (f *memFile) Sync() error                { return nil }
func (f *memFile) Truncate(int64) error       { return errors.New("not supported") }
func (f *memFile) Fd() uintptr                { return 0 }
func (f *memFile) Close() error               { return nil }
func (f *memFile) Write([]byte) (int, error)  { return 0, errors.New("not supported") }

// fakeFS implements workspaceapi.FileSystem for resolver and detection
// tests. ReadDir returns the scripted entries for any path, except for
// directory keys registered via addReadDir. Files registered with
// addContent are readable through OpenFile.
type fakeFS struct {
	files    map[string]bool
	dirs     map[string]bool
	contents map[string][]byte
	entries  []os.DirEntry
	readDirs map[string][]os.DirEntry
}

func newFakeFS() *fakeFS {
	return &fakeFS{
		files:    map[string]bool{},
		dirs:     map[string]bool{},
		contents: map[string][]byte{},
		readDirs: map[string][]os.DirEntry{},
	}
}

func (f *fakeFS) addFile(p string) *fakeFS { f.files[p] = true; return f }
func (f *fakeFS) addDir(p string) *fakeFS  { f.dirs[p] = true; return f }

func (f *fakeFS) addContent(p string, content []byte) *fakeFS {
	f.files[p] = true
	f.contents[p] = content
	return f
}

func (f *fakeFS) addEntry(name string, dir bool) *fakeFS {
	f.entries = append(f.entries, fakeDirEntry{name: name, dir: dir})
	return f
}

func (f *fakeFS) addReadDir(path string, entries ...os.DirEntry) *fakeFS {
	f.readDirs[path] = entries
	return f
}

func (f *fakeFS) URI(p string) (workspaceapi.URI, error) {
	return workspaceapi.ParseURI("file://" + p)
}

func (f *fakeFS) OpenFile(p string, _ int, _ os.FileMode) (workspaceapi.File, error) {
	if content, ok := f.contents[p]; ok {
		return &memFile{Reader: bytes.NewReader(content), name: p}, nil
	}
	return nil, &iofs.PathError{Op: "open", Path: p, Err: os.ErrNotExist}
}

func (f *fakeFS) Remove(_ string) error { return errors.New("not supported") }

func (f *fakeFS) MkdirAll(string, os.FileMode) error { return nil }

func (f *fakeFS) Stat(name string) (os.FileInfo, error) {
	if f.files[name] {
		return fakeFileInfo{name: name, dir: false}, nil
	}
	if f.dirs[name] {
		return fakeFileInfo{name: name, dir: true}, nil
	}
	return nil, &iofs.PathError{Op: "stat", Path: name, Err: os.ErrNotExist}
}

func (f *fakeFS) ReadDir(p string) ([]os.DirEntry, error) {
	if entries, ok := f.readDirs[p]; ok {
		return entries, nil
	}
	if p == "." {
		return f.entries, nil
	}
	return nil, &iofs.PathError{Op: "readdir", Path: p, Err: os.ErrNotExist}
}

// fakeInstaller mirrors extensionapi.Workspace.FindInstalledExecutable
// against any FileSystem: it resolves <root>/bin/<name> and reports the
// path only when it exists as a regular file.
type fakeInstaller struct {
	fs   workspaceapi.FileSystem
	root string
}

func (i fakeInstaller) FindInstalledExecutable(
	_ context.Context, name string,
) (string, error) {
	p := i.root + "/bin/" + name
	info, err := i.fs.Stat(p)
	if err != nil {
		return "", err
	}
	if info == nil || info.IsDir() {
		return "", os.ErrNotExist
	}
	return p, nil
}

// scriptedCmd records the stdout/stderr payload and exit error returned
// by the fake executor for a matching command key.
type scriptedCmd struct {
	stdout string
	stderr string
	err    error
}

// fakeExecutor implements workspaceapi.Executor. It dispatches on the
// space-joined (cmd.Path, cmd.Args...) key. Unknown commands succeed
// with empty output.
type fakeExecutor struct {
	mu        sync.Mutex
	responses map[string]scriptedCmd
	calls     []string
	nextPid   workspaceapi.Pid
}

func newFakeExecutor() *fakeExecutor {
	return &fakeExecutor{responses: map[string]scriptedCmd{}, nextPid: 1}
}

func (e *fakeExecutor) respond(key string, r scriptedCmd) *fakeExecutor {
	e.responses[key] = r
	return e
}

func (e *fakeExecutor) callKey(cmd workspaceapi.Cmd) string {
	return strings.Join(append([]string{cmd.Path}, cmd.Args...), " ")
}

func (e *fakeExecutor) Start(_ context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
	e.mu.Lock()
	key := e.callKey(cmd)
	e.calls = append(e.calls, key)
	resp := e.responses[key]
	pid := e.nextPid
	e.nextPid++
	e.mu.Unlock()

	if cmd.Stdout != nil && resp.stdout != "" {
		_, _ = io.Copy(cmd.Stdout, bytes.NewBufferString(resp.stdout))
	}
	if cmd.Stderr != nil && resp.stderr != "" {
		_, _ = io.Copy(cmd.Stderr, bytes.NewBufferString(resp.stderr))
	}
	if cmd.Watcher != nil {
		ch := cmd.Watcher.WatchProcess()
		go func(err error) {
			if ch != nil {
				ch <- err
			}
		}(resp.err)
	}
	return pid, nil
}

func (e *fakeExecutor) Signal(_ workspaceapi.Pid, _ syscall.Signal) error { return nil }
func (e *fakeExecutor) Close() error                                      { return nil }

func (e *fakeExecutor) callsSnapshot() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.calls))
	copy(out, e.calls)
	return out
}

// captureLSP records the InitializeParams it received and otherwise
// no-ops every request, so tests assert against the captured params
// without running a real server.
type captureLSP struct {
	noopLSP
	mu         sync.Mutex
	initParams *semanticapi.InitializeParams
	initCount  int
	inits      chan struct{}
}

func (l *captureLSP) Initialize(
	_ context.Context, p semanticapi.InitializeParams,
) (semanticapi.InitializeResult, error) {
	l.mu.Lock()
	cp := p
	l.initParams = &cp
	l.initCount++
	signal := l.inits
	l.mu.Unlock()
	if signal != nil {
		signal <- struct{}{}
	}
	return semanticapi.InitializeResult{}, nil
}

func (l *captureLSP) captured() (semanticapi.InitializeParams, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.initParams == nil {
		return semanticapi.InitializeParams{}, l.initCount
	}
	return *l.initParams, l.initCount
}

// waitForInit blocks until the next Initialize call completes or the
// timeout elapses, so tests can synchronize on the asynchronous,
// event-driven bring-up before asserting on captured params.
func (l *captureLSP) waitForInit(t *testing.T, timeout time.Duration) {
	t.Helper()
	l.mu.Lock()
	if l.inits == nil {
		l.inits = make(chan struct{}, 1)
	}
	signal := l.inits
	l.mu.Unlock()
	select {
	case <-signal:
	case <-time.After(timeout):
		t.Fatal("timed out waiting for lsp.Initialize")
	}
}

// fakeEditor is a textapi.Editor that records the open-event
// subscription so tests can deliver synthetic open events to the
// extension's langext.Initializer, and records the edits applied to any
// resource registered with it.
type fakeEditor struct {
	mu        sync.Mutex
	handlers  []textapi.EventHandler
	resources map[string]textapi.Handler
	cells     map[textapi.Handler]*fakeCellEditor
	cursors   map[textapi.Handler]term.Coordinates
}

var _ textapi.Editor = (*fakeEditor)(nil)

func (e *fakeEditor) SubscribeEvents(_ []textapi.EventType, h textapi.EventHandler) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.handlers = append(e.handlers, h)
	return nil
}

// open delivers an EventTypeOpen for the file at path to the subscribed
// handlers, failing if no handler has subscribed yet.
func (e *fakeEditor) open(t *testing.T, path string) {
	t.Helper()
	uri, err := workspaceapi.ParseURI("file://" + path)
	require.NoError(t, err)
	e.mu.Lock()
	handlers := append([]textapi.EventHandler(nil), e.handlers...)
	e.mu.Unlock()
	require.NotEmpty(t, handlers, "no event handler subscribed")
	for _, h := range handlers {
		h.Handle(context.Background(), textapi.Event{Type: textapi.EventTypeOpen, URI: uri})
	}
}

// register makes h resolvable through Editor, so edits addressed to its
// resource can be applied and recorded.
func (e *fakeEditor) register(h textapi.Handler) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.resources == nil {
		e.resources = make(map[string]textapi.Handler)
	}
	e.resources[h.Resource().String()] = h
}

func (e *fakeEditor) Editor(uri workspaceapi.URI) (textapi.Handler, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if h, ok := e.resources[uri.String()]; ok {
		return h, nil
	}
	return nil, nil
}

// editsFor returns every edit applied to h, in order.
func (e *fakeEditor) editsFor(h textapi.Handler) []fakeEdit {
	e.mu.Lock()
	ce, ok := e.cells[h]
	e.mu.Unlock()
	if !ok {
		return nil
	}
	return ce.snapshot()
}

// seed gives h a starting buffer so edits applied to it can be asserted
// as resulting text rather than as a list of ranges.
func (e *fakeEditor) seed(h textapi.Handler, text string) {
	ce, ok := e.CellEditor(h).(*fakeCellEditor)
	if !ok {
		return
	}
	ce.mu.Lock()
	defer ce.mu.Unlock()
	ce.text = text
}

// text returns h's buffer after every applied edit.
func (e *fakeEditor) text(h textapi.Handler) string {
	ce, ok := e.CellEditor(h).(*fakeCellEditor)
	if !ok {
		return ""
	}
	ce.mu.Lock()
	defer ce.mu.Unlock()
	return ce.text
}

func (e *fakeEditor) SetLocationList(
	textapi.Handler, textapi.LocationPriority, string, textapi.LocationList,
) error {
	return nil
}
func (e *fakeEditor) MoveToNextLocation(textapi.Handler, string) error { return nil }
func (e *fakeEditor) MoveToPrevLocation(textapi.Handler, string) error { return nil }
func (e *fakeEditor) Cursor(h textapi.Handler) (term.Coordinates, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cursors[h], nil
}

func (e *fakeEditor) SetCursor(h textapi.Handler, c term.Coordinates) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cursors == nil {
		e.cursors = make(map[textapi.Handler]term.Coordinates)
	}
	e.cursors[h] = c
	return nil
}
func (e *fakeEditor) CellView(h textapi.Handler) textapi.CellView {
	ce, ok := e.CellEditor(h).(*fakeCellEditor)
	if !ok {
		return nil
	}
	return fakeCellView{ce}
}

type fakeCellView struct {
	ce *fakeCellEditor
}

func (v fakeCellView) RawCells() ([][]term.Cell, error) {
	v.ce.mu.Lock()
	defer v.ce.mu.Unlock()
	lines := strings.Split(v.ce.text, "\n")
	cells := make([][]term.Cell, len(lines))
	for i, line := range lines {
		row := make([]term.Cell, 0, len(line))
		for _, r := range line {
			row = append(row, term.Cell{
				Ch: r, Width: 1, Bytes: uint8(utf8.RuneLen(r)),
			})
		}
		cells[i] = row
	}
	return cells, nil
}

func (e *fakeEditor) CellEditor(h textapi.Handler) textapi.CellEditor {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cells == nil {
		e.cells = make(map[textapi.Handler]*fakeCellEditor)
	}
	if ce, ok := e.cells[h]; ok {
		return ce
	}
	ce := &fakeCellEditor{}
	e.cells[h] = ce
	return ce
}
func (e *fakeEditor) SetDefaultAttributes(textapi.Handler, term.Attributes) error {
	return nil
}

// fakeEdit records a single CellEditor.Edit call.
type fakeEdit struct {
	start, end term.Coordinates
	text       string
}

// stubResource is a textapi.Handler that only reports the resource it
// stands for, so a command sees a non-nil focused file.
type stubResource struct {
	uri workspaceapi.URI
}

var _ textapi.Handler = (*stubResource)(nil)

func (s *stubResource) Handle(term.Event) (bool, bool) { return false, false }
func (s *stubResource) Draw(term.Writer)               {}
func (s *stubResource) Resize(int, int)                {}
func (s *stubResource) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return term.Coordinates{}, term.CursorStyleDefault, false
}
func (s *stubResource) Selection() (string, bool)  { return "", false }
func (s *stubResource) Close() error               { return nil }
func (s *stubResource) Resource() workspaceapi.URI { return s.uri }

// fakeCellEditor is a textapi.CellEditor that records the edits it is
// asked to apply and mirrors them into a text buffer, so a test can
// assert either the edit ranges or the resulting text.
type fakeCellEditor struct {
	mu    sync.Mutex
	edits []fakeEdit
	text  string
}

var _ textapi.CellEditor = (*fakeCellEditor)(nil)

// offsetOf resolves cell coordinates to a byte offset into the buffer.
func offsetOf(text string, c term.Coordinates) int {
	lines := strings.SplitAfter(text, "\n")
	off := 0
	for i := 0; i < c.Y && i < len(lines); i++ {
		off += len(lines[i])
	}
	return min(off+c.X, len(text))
}

func (c *fakeCellEditor) Edit(
	_ context.Context, start, end term.Coordinates, str string,
) (term.Coordinates, term.Coordinates, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.edits = append(c.edits, fakeEdit{start: start, end: end, text: str})
	s, e := offsetOf(c.text, start), offsetOf(c.text, end)
	if s <= e {
		c.text = c.text[:s] + str + c.text[e:]
	}
	return start, end, "", nil
}

func (c *fakeCellEditor) snapshot() []fakeEdit {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]fakeEdit(nil), c.edits...)
}

// fakeNotifications records notify and notify-once calls for assertions.
type fakeNotifications struct {
	mu     sync.Mutex
	notifs []string
}

func newFakeNotifications() *fakeNotifications {
	return &fakeNotifications{}
}

func (n *fakeNotifications) Notify(_ browserapi.NotificationLevel, msg string, args ...any) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.notifs = append(n.notifs, msg)
	return "notif-1", nil
}

func (n *fakeNotifications) NotifyOnce(level browserapi.NotificationLevel, msg string, args ...any) (string, error) {
	return n.Notify(level, msg, args...)
}

func (n *fakeNotifications) UpdateNotificationProgress(_, _ string, _, _ int64) error {
	return nil
}

func (n *fakeNotifications) notifMessages() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]string, len(n.notifs))
	copy(out, n.notifs)
	return out
}

func (n *fakeNotifications) hasMessage(substr string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, msg := range n.notifs {
		if strings.Contains(msg, substr) {
			return true
		}
	}
	return false
}

// realFS is a minimal workspaceapi.FileSystem backed by the OS and
// rooted at a workspace directory, used by the e2e tests.
type realFS struct{ root string }

func (f realFS) resolve(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(f.root, p)
}

func (f realFS) URI(p string) (workspaceapi.URI, error) {
	return workspaceapi.ParseURI("file://" + f.resolve(p))
}

func (f realFS) Stat(p string) (os.FileInfo, error)      { return os.Stat(f.resolve(p)) }
func (f realFS) ReadDir(p string) ([]os.DirEntry, error) { return os.ReadDir(f.resolve(p)) }
func (f realFS) MkdirAll(p string, m os.FileMode) error  { return os.MkdirAll(f.resolve(p), m) }

func (f realFS) OpenFile(p string, flag int, mode os.FileMode) (workspaceapi.File, error) {
	return os.OpenFile(f.resolve(p), flag, mode)
}
func (f realFS) Remove(string) error { return errors.New("realFS: Remove not supported") }

// realExecutor spawns real subprocesses, used by the e2e tests against
// an installed zig. The watcher receives the process exit error.
type realExecutor struct{ dir string }

func newDirExecutor(dir string) realExecutor { return realExecutor{dir: dir} }

func (e realExecutor) Start(ctx context.Context, c workspaceapi.Cmd) (workspaceapi.Pid, error) {
	cmd := exec.CommandContext(ctx, c.Path, c.Args...)
	cmd.Dir = c.Dir
	if cmd.Dir == "" {
		cmd.Dir = e.dir
	}
	if c.Env != nil {
		cmd.Env = c.Env
	}
	cmd.Stdout = c.Stdout
	cmd.Stderr = c.Stderr
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := workspaceapi.Pid(cmd.Process.Pid)
	go func() {
		err := cmd.Wait()
		if c.Watcher != nil {
			c.Watcher.WatchProcess() <- err
		}
	}()
	return pid, nil
}

func (realExecutor) Signal(workspaceapi.Pid, syscall.Signal) error { return nil }
func (realExecutor) Close() error                                  { return nil }

// zigEnv is the result of running the extension's full bring-up against
// a real workspace directory: the captured LSP and notifications, plus
// the manuals registered for the `zig` command.
type zigEnv struct {
	dir     string
	lsp     *captureLSP
	notify  *fakeNotifications
	editor  *fakeEditor
	manuals []textapi.CommandManual
	cmds    []textapi.CommandManual
}

// runZigExtensionOnDir runs the extension's full bring-up against the
// prepared workspace directory dir, using a real FileSystem and Executor
// rooted there with a fake LSP and Notifications, and returns the
// resulting environment for assertions.
func runZigExtensionOnDir(t *testing.T, dir, dataDir string, cfg config.Config) zigEnv {
	t.Helper()
	lsp := &captureLSP{}
	notify := newFakeNotifications()
	editor := &fakeEditor{}
	env := zigEnv{dir: dir, lsp: lsp, notify: notify, editor: editor}

	ext := &zigExtension{}
	err := ext.extendWorkspaceWith(context.Background(),
		realFS{root: dir},
		newDirExecutor(dir),
		notify,
		lsp,
		editor,
		&fakeWM{},
		fakeInstaller{fs: realFS{root: dir}, root: dataDir},
		cfg,
		func(m textapi.CommandManual, _ textapi.REPLHandler) error {
			env.manuals = append(env.manuals, m)
			return nil
		},
		func(m textapi.CommandManual, _ textapi.CommandHandler) error {
			env.cmds = append(env.cmds, m)
			return nil
		},
	)
	require.NoError(t, err)
	return env
}

// fakeWM is a browserapi.WindowManager that records the floating handler
// it is asked to show, so tests can drive the code-action picker.
type fakeWM struct {
	mu       sync.Mutex
	floating browserapi.Floating
}

var _ browserapi.WindowManager = (*fakeWM)(nil)

// fakeWindow is the browserapi.Window fakeWM hands back.
type fakeWindow struct{ id uint64 }

func (w fakeWindow) WindowID() uint64 { return w.id }

func (m *fakeWM) Floating(
	h browserapi.Floating, _ browserapi.FloatingConfig,
) (browserapi.Window, error) {
	m.mu.Lock()
	m.floating = h
	m.mu.Unlock()
	return fakeWindow{id: 1}, nil
}

// floated returns the most recently floated handler, if any.
func (m *fakeWM) floated() browserapi.Floating {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.floating
}

// reset forgets the recorded handler, so a test that runs a command
// twice waits for the second float instead of matching the first.
func (m *fakeWM) reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.floating = nil
}

func (m *fakeWM) Focus() (browserapi.Window, error) { return fakeWindow{id: 1}, nil }
func (m *fakeWM) Split(
	browserapi.Orientation, browserapi.Window, browserapi.Handler,
) (browserapi.Window, error) {
	return nil, errors.New("not implemented")
}
func (m *fakeWM) Bar(browserapi.BarConfig, tui.Handler) error {
	return errors.New("not implemented")
}
func (m *fakeWM) Tab(
	_ workspaceapi.URI, _ rune, _ string, h browserapi.Handler,
) (browserapi.Handler, error) {
	return h, nil
}
func (m *fakeWM) SetWindowContent(browserapi.Window, browserapi.Handler) error { return nil }
func (m *fakeWM) CloseWindow(browserapi.Window) error                          { return nil }

func (m *fakeWM) SetTabActivity(workspaceapi.URI, bool) error { return nil }

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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
)

// assertErr is a sentinel error scripted into the fake executor to
// simulate a failed command.
var assertErr = errors.New("scripted failure")

// stubConfig is a config.Config that resolves lsp_path from a string map
// and reports ErrNotFound for everything else. It embeds the interface
// so only GetString needs an override; the embedded nil panics if any
// other method is called, which the tests never do.
type stubConfig struct {
	config.Config
	values map[string]string
}

func newStubConfig(values map[string]string) stubConfig {
	return stubConfig{values: values}
}

func (c stubConfig) GetString(k string) (string, error) {
	if v, ok := c.values[k]; ok {
		return v, nil
	}
	return "", config.ErrNotFound
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

// fakeFS implements workspaceapi.FileSystem for resolver and detection
// tests. ReadDir returns the scripted entries for any path, except for
// directory keys registered via addReadDir.
type fakeFS struct {
	files    map[string]bool
	dirs     map[string]bool
	entries  []os.DirEntry
	readDirs map[string][]os.DirEntry
	mkdirAll []string
	mkdirErr error
}

func newFakeFS() *fakeFS {
	return &fakeFS{
		files:    map[string]bool{},
		dirs:     map[string]bool{},
		readDirs: map[string][]os.DirEntry{},
	}
}

func (f *fakeFS) addFile(p string) *fakeFS { f.files[p] = true; return f }
func (f *fakeFS) addDir(p string) *fakeFS  { f.dirs[p] = true; return f }

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

func (f *fakeFS) OpenFile(_ string, _ int, _ os.FileMode) (workspaceapi.File, error) {
	return nil, errors.New("not supported")
}

func (f *fakeFS) Remove(_ string) error { return errors.New("not supported") }

func (f *fakeFS) MkdirAll(p string, _ os.FileMode) error {
	f.mkdirAll = append(f.mkdirAll, p)
	return f.mkdirErr
}

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

// recordingParser is a syntaxapi.Parser that records the URIs it is
// asked to highlight and returns no spans, so tests can assert that
// viewers and picker previews request syntax highlighting.
type recordingParser struct {
	mu   sync.Mutex
	uris []string
}

var _ syntaxapi.Parser = (*recordingParser)(nil)

func (p *recordingParser) Highlight(
	uri workspaceapi.URI, _ string,
) (iterator.Iterator[textapi.Location], error) {
	p.mu.Lock()
	p.uris = append(p.uris, uri.String())
	p.mu.Unlock()
	return iterator.Empty[textapi.Location](), nil
}

func (p *recordingParser) highlighted() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.uris...)
}

func (p *recordingParser) Search(string, []string, ...string) (
	iterator.Iterator[syntaxapi.Result], error,
) {
	return iterator.Empty[syntaxapi.Result](), nil
}

func (p *recordingParser) SearchNode(syntaxapi.NodeCaptureName, ...string) (
	iterator.Iterator[syntaxapi.Result], error,
) {
	return iterator.Empty[syntaxapi.Result](), nil
}

func (p *recordingParser) Query(workspaceapi.URI, string, []string) (
	iterator.Iterator[syntaxapi.Result], error,
) {
	return iterator.Empty[syntaxapi.Result](), nil
}

func (p *recordingParser) QueryNode(workspaceapi.URI, syntaxapi.NodeCaptureName) (
	iterator.Iterator[syntaxapi.Result], error,
) {
	return iterator.Empty[syntaxapi.Result](), nil
}

func (p *recordingParser) ResolveSymbol(
	context.Context, string, syntaxapi.Progress,
) (iterator.Iterator[syntaxapi.Match], error) {
	return iterator.Empty[syntaxapi.Match](), nil
}

func (p *recordingParser) ListReferencedSymbols(context.Context) (
	iterator.Iterator[string], error,
) {
	return iterator.Empty[string](), nil
}

// recordingInterrupter counts redraw requests, so tests can assert
// that asynchronous highlight passes wake the IDE event loop.
type recordingInterrupter struct {
	mu    sync.Mutex
	count int
}

var _ term.Interrupter = (*recordingInterrupter)(nil)

func (i *recordingInterrupter) Interrupt(context.Context) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.count++
	return nil
}

func (i *recordingInterrupter) interrupts() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.count
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
// with empty output so the bootstrap path runs without scripting every
// step.
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
// extension's langext.Initializer. Every other method is an unused stub.
type fakeEditor struct {
	mu       sync.Mutex
	handlers []textapi.EventHandler
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

func (e *fakeEditor) Editor(workspaceapi.URI) (textapi.Handler, error) { return nil, nil }
func (e *fakeEditor) SetLocationList(
	textapi.Handler, textapi.LocationPriority, string, textapi.LocationList,
) error {
	return nil
}
func (e *fakeEditor) MoveToNextLocation(textapi.Handler, string) error { return nil }
func (e *fakeEditor) MoveToPrevLocation(textapi.Handler, string) error { return nil }
func (e *fakeEditor) Cursor(textapi.Handler) (term.Coordinates, error) {
	return term.Coordinates{}, nil
}
func (e *fakeEditor) SetCursor(textapi.Handler, term.Coordinates) error { return nil }
func (e *fakeEditor) CellView(textapi.Handler) textapi.CellView         { return nil }
func (e *fakeEditor) CellEditor(textapi.Handler) textapi.CellEditor     { return nil }
func (e *fakeEditor) SetDefaultAttributes(textapi.Handler, term.Attributes) error {
	return nil
}

// fakeWindow is a minimal browserapi.Window used by fakeWM.
type fakeWindow struct{ id uint64 }

func (w fakeWindow) WindowID() uint64 { return w.id }

// editorWinID and floatingWinID are the window IDs fakeWM hands out,
// mirroring production where a floating window and the editor window
// underneath it are distinct.
const (
	editorWinID   = 1
	floatingWinID = 2
)

// fakeWM is a browserapi.WindowManager that records the floating handler
// it is asked to show and the target of SetWindowContent. Focus mirrors
// production: while a floating window is up, it holds the focus, so a
// jump that resolves its target window too late lands in the float.
type fakeWM struct {
	mu       sync.Mutex
	floating browserapi.Floating
	winSet   browserapi.Window
	content  browserapi.Handler
}

var _ browserapi.WindowManager = (*fakeWM)(nil)

func (m *fakeWM) Focus() (browserapi.Window, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.floating != nil {
		return fakeWindow{id: floatingWinID}, nil
	}
	return fakeWindow{id: editorWinID}, nil
}

func (m *fakeWM) Tab(
	_ workspaceapi.URI, _ rune, _ string, h browserapi.Handler,
) (browserapi.Handler, error) {
	return h, nil
}

func (m *fakeWM) SetWindowContent(w browserapi.Window, h browserapi.Handler) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.winSet, m.content = w, h
	return nil
}

// lastContent returns the window and handler of the most recent
// SetWindowContent call.
func (m *fakeWM) lastContent() (browserapi.Window, browserapi.Handler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.winSet, m.content
}

func (m *fakeWM) Split(
	browserapi.Orientation, browserapi.Window, browserapi.Handler,
) (browserapi.Window, error) {
	return nil, errors.New("not implemented")
}

func (m *fakeWM) Floating(
	h browserapi.Floating, _ browserapi.FloatingConfig,
) (browserapi.Window, error) {
	m.mu.Lock()
	m.floating = h
	m.mu.Unlock()
	return fakeWindow{id: floatingWinID}, nil
}

func (m *fakeWM) Bar(browserapi.BarConfig, tui.Handler) error {
	return errors.New("not implemented")
}

func (m *fakeWM) CloseWindow(browserapi.Window) error       { return nil }
func (m *fakeWM) SetTabName(workspaceapi.URI, string) error { return nil }

func (m *fakeWM) SetTabActivity(workspaceapi.URI, bool) error { return nil }

type progressSample struct {
	id       string
	message  string
	progress int64
	total    int64
}

// fakeNotifications records notify and progress calls for assertions.
type fakeNotifications struct {
	mu       sync.Mutex
	openID   string
	notifs   []string
	progress []progressSample
}

func newFakeNotifications() *fakeNotifications {
	return &fakeNotifications{openID: "notif-1"}
}

func (n *fakeNotifications) Notify(_ browserapi.NotificationLevel, msg string, _ ...any) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.notifs = append(n.notifs, msg)
	return n.openID, nil
}

func (n *fakeNotifications) NotifyOnce(level browserapi.NotificationLevel, msg string, args ...any) (string, error) {
	return n.Notify(level, msg, args...)
}

func (n *fakeNotifications) UpdateNotificationProgress(id, message string, progress, total int64) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.progress = append(n.progress, progressSample{id, message, progress, total})
	return nil
}

func (n *fakeNotifications) progressMessages() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]string, len(n.progress))
	for i, p := range n.progress {
		out[i] = p.message
	}
	return out
}

func (n *fakeNotifications) notifMessages() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]string, len(n.notifs))
	copy(out, n.notifs)
	return out
}

// assertMonotonicProgress checks the displayed fraction never decreases,
// so the bar cannot move backward across steps with different totals.
func assertMonotonicProgress(t *testing.T, n *fakeNotifications) {
	t.Helper()
	n.mu.Lock()
	defer n.mu.Unlock()
	prev := 0.0
	for _, p := range n.progress {
		require.NotZero(t, p.total)
		frac := float64(p.progress) / float64(p.total)
		assert.GreaterOrEqual(t, frac, prev,
			"progress fraction regressed at %q (%d/%d)", p.message, p.progress, p.total)
		prev = frac
	}
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
// an installed rustup/rustc. The watcher receives the process exit error.
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

// captureExecutor records the commands it is asked to start without
// spawning a process, and immediately signals completion. It lets run
// tests assert the reconstructed command against the real rust-analyzer
// runnable without paying for a cargo compile.
type captureExecutor struct {
	mu         sync.Mutex
	cmds       []workspaceapi.Cmd
	fakeStdout string
}

func (e *captureExecutor) Start(_ context.Context, c workspaceapi.Cmd) (workspaceapi.Pid, error) {
	e.mu.Lock()
	e.cmds = append(e.cmds, c)
	e.mu.Unlock()
	if c.Stdout != nil && e.fakeStdout != "" {
		_, _ = c.Stdout.Write([]byte(e.fakeStdout))
	}
	if c.Watcher != nil {
		go func() { c.Watcher.WatchProcess() <- nil }()
	}
	return 1, nil
}

func (*captureExecutor) Signal(workspaceapi.Pid, syscall.Signal) error { return nil }
func (*captureExecutor) Close() error                                  { return nil }

func (e *captureExecutor) lastCmd() (workspaceapi.Cmd, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.cmds) == 0 {
		return workspaceapi.Cmd{}, false
	}
	return e.cmds[len(e.cmds)-1], true
}

func findRustup(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("rustup"); err != nil {
		t.Skip("rustup not found, skipping rust e2e test")
	}
}

func findRustc(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("rustc")
	if err != nil {
		t.Skip("rustc not found, skipping rust e2e test")
	}
	return p
}

// rustEnv is the result of running the extension's full bring-up against
// a real workspace directory: the captured LSP and notifications, plus
// the manuals registered for the `rust` command.
type rustEnv struct {
	dir     string
	lsp     *captureLSP
	notify  *fakeNotifications
	editor  *fakeEditor
	manuals []textapi.CommandManual
	cmds    []textapi.CommandManual
}

// runRustExtensionOnDir runs the extension's full bring-up against the
// prepared workspace directory dir, using a real FileSystem and Executor
// rooted there with a fake LSP and Notifications, and returns the
// resulting environment for assertions. rustupHome/dataDir are passed
// through so e2e tests can exercise the toolchain path.
func runRustExtensionOnDir(t *testing.T, dir, rustupHome, cargoHome, dataDir string) rustEnv {
	t.Helper()
	lsp := &captureLSP{}
	notify := newFakeNotifications()
	editor := &fakeEditor{}
	env := rustEnv{dir: dir, lsp: lsp, notify: notify, editor: editor}

	ext := &rustExtension{}
	err := ext.extendWorkspaceWith(context.Background(),
		realFS{root: dir},
		newDirExecutor(dir),
		notify,
		lsp,
		editor,
		&fakeWM{},
		nil,
		nil,
		nil,
		fakeInstaller{fs: realFS{root: dir}, root: dataDir},
		rustupHome, cargoHome, nil,
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

// stubResource implements textapi.Handler so a code-action command sees a
// non-nil focused resource.
type stubResource struct {
	uri workspaceapi.URI
}

func (s *stubResource) Handle(_ term.Event) (bool, bool) { return false, false }
func (s *stubResource) Draw(_ term.Writer)               {}
func (s *stubResource) Resize(_, _ int)                  {}
func (s *stubResource) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return term.Coordinates{}, 0, false
}
func (s *stubResource) Selection() (string, bool)  { return "", false }
func (s *stubResource) Close() error               { return nil }
func (s *stubResource) Resource() workspaceapi.URI { return s.uri }

var _ textapi.Handler = (*stubResource)(nil)

// actionLSP is a semanticapi.LSP whose CodeAction returns a scripted set
// of results and whose ExecuteCommand records the commands it receives.
type actionLSP struct {
	noopLSP
	params   semanticapi.CodeActionParams
	results  []semanticapi.CodeActionResult
	executed []string
}

func (l *actionLSP) Initialize(
	_ context.Context, _ semanticapi.InitializeParams,
) (semanticapi.InitializeResult, error) {
	return semanticapi.InitializeResult{}, nil
}

func (l *actionLSP) CodeAction(
	_ context.Context, params semanticapi.CodeActionParams,
) ([]semanticapi.CodeActionResult, error) {
	l.params = params
	return l.results, nil
}

func (l *actionLSP) ExecuteCommand(
	_ context.Context, params semanticapi.ExecuteCommandParams,
) (string, error) {
	l.executed = append(l.executed, params.Command)
	return "", nil
}

func cmdFor(uri workspaceapi.URI, name string, args []string) textapi.Command {
	return textapi.Command{
		Name:     name,
		Args:     args,
		URI:      uri,
		Resource: &stubResource{uri: uri},
	}
}

func newTestURI(t *testing.T) workspaceapi.URI {
	t.Helper()
	uri, err := workspaceapi.ParseURI("file:///ws/src/main.rs")
	require.NoError(t, err)
	return uri
}

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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
)

// stubConfig is a config.Config resolving string keys from a map and
// reporting ErrNotFound for everything else. The embedded nil interface
// panics if any other getter is called, which the tests never do.
type stubConfig struct {
	config.Config
	strings map[string]string
	bools   map[string]bool
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

// fakeFS is a workspaceapi.FileSystem over absolute paths. Adding a
// path registers it as an entry of every ancestor directory, so Stat
// and ReadDir agree. Relative paths resolve against root. Only files
// written with writeFile can be opened.
type fakeFS struct {
	root    string
	files   map[string]bool
	dirs    map[string]bool
	entries map[string]map[string]bool
	content map[string]string
}

func newFakeFS(root string) *fakeFS {
	return &fakeFS{
		root:    root,
		files:   map[string]bool{},
		dirs:    map[string]bool{root: true},
		entries: map[string]map[string]bool{},
		content: map[string]string{},
	}
}

func (f *fakeFS) resolve(p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(f.root, p)
}

func (f *fakeFS) link(p string) {
	for {
		parent := filepath.Dir(p)
		if parent == p {
			return
		}
		if f.entries[parent] == nil {
			f.entries[parent] = map[string]bool{}
		}
		f.entries[parent][filepath.Base(p)] = true
		f.dirs[parent] = true
		p = parent
	}
}

func (f *fakeFS) addFile(p string) *fakeFS {
	p = f.resolve(p)
	f.files[p] = true
	f.link(p)
	return f
}

func (f *fakeFS) addDir(p string) *fakeFS {
	p = f.resolve(p)
	f.dirs[p] = true
	f.link(p)
	return f
}

func (f *fakeFS) writeFile(p, content string) *fakeFS {
	f.addFile(p)
	f.content[f.resolve(p)] = content
	return f
}

func (f *fakeFS) URI(p string) (workspaceapi.URI, error) {
	return workspaceapi.ParseURI("file://" + f.resolve(p))
}

func (f *fakeFS) OpenFile(p string, _ int, _ os.FileMode) (workspaceapi.File, error) {
	content, ok := f.content[f.resolve(p)]
	if !ok {
		return nil, &iofs.PathError{Op: "open", Path: p, Err: os.ErrNotExist}
	}
	return &memFile{Reader: bytes.NewReader([]byte(content)), name: f.resolve(p)}, nil
}

func (f *fakeFS) Remove(string) error                { return errors.New("not supported") }
func (f *fakeFS) MkdirAll(string, os.FileMode) error { return nil }

func (f *fakeFS) Stat(name string) (os.FileInfo, error) {
	p := f.resolve(name)
	if f.files[p] {
		return fakeFileInfo{name: filepath.Base(p)}, nil
	}
	if f.dirs[p] {
		return fakeFileInfo{name: filepath.Base(p), dir: true}, nil
	}
	return nil, &iofs.PathError{Op: "stat", Path: name, Err: os.ErrNotExist}
}

func (f *fakeFS) ReadDir(name string) ([]os.DirEntry, error) {
	p := f.resolve(name)
	if !f.dirs[p] {
		return nil, &iofs.PathError{Op: "readdir", Path: name, Err: os.ErrNotExist}
	}
	var out []os.DirEntry
	for child := range f.entries[p] {
		out = append(out, fakeDirEntry{name: child, dir: f.dirs[filepath.Join(p, child)]})
	}
	return out, nil
}

// memFile is a read-only workspaceapi.File over an in-memory buffer.
type memFile struct {
	*bytes.Reader
	name string
}

func (m *memFile) Name() string               { return m.name }
func (m *memFile) Stat() (os.FileInfo, error) { return fakeFileInfo{name: filepath.Base(m.name)}, nil }
func (m *memFile) Sync() error                { return nil }
func (m *memFile) Truncate(int64) error       { return errors.New("read-only") }
func (m *memFile) Fd() uintptr                { return 0 }
func (m *memFile) Close() error               { return nil }
func (m *memFile) Write([]byte) (int, error)  { return 0, errors.New("read-only") }

// scriptedCmd is the output and exit error the fake executor returns for
// a matching command.
type scriptedCmd struct {
	stdout string
	stderr string
	err    error
}

// fakeExecutor implements workspaceapi.Executor, dispatching on the
// space-joined (cmd.Path, cmd.Args...) key. Unknown commands fail like a
// missing binary. It records the directory of every command it runs.
type fakeExecutor struct {
	mu        sync.Mutex
	responses map[string]scriptedCmd
	calls     []string
	dirs      []string
}

func newFakeExecutor() *fakeExecutor {
	return &fakeExecutor{responses: map[string]scriptedCmd{}}
}

func (e *fakeExecutor) respond(key string, r scriptedCmd) *fakeExecutor {
	e.responses[key] = r
	return e
}

func (e *fakeExecutor) Start(_ context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
	key := strings.Join(append([]string{cmd.Path}, cmd.Args...), " ")
	e.mu.Lock()
	e.calls = append(e.calls, key)
	e.dirs = append(e.dirs, cmd.Dir)
	resp, ok := e.responses[key]
	e.mu.Unlock()
	if !ok {
		resp.err = fmt.Errorf("%s: exit status 127", key)
	}
	if cmd.Stdout != nil {
		_, _ = io.Copy(cmd.Stdout, bytes.NewBufferString(resp.stdout))
	}
	if cmd.Stderr != nil {
		_, _ = io.Copy(cmd.Stderr, bytes.NewBufferString(resp.stderr))
	}
	if cmd.Watcher != nil {
		cmd.Watcher.WatchProcess() <- resp.err
	}
	return 1, nil
}

func (e *fakeExecutor) Signal(workspaceapi.Pid, syscall.Signal) error { return nil }
func (e *fakeExecutor) Close() error                                  { return nil }

func (e *fakeExecutor) callsSnapshot() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.calls...)
}

// captureLSP records every InitializeParams it receives and otherwise
// no-ops, so tests assert against what the extension would start.
type captureLSP struct {
	noopLSP
	mu    sync.Mutex
	inits []semanticapi.InitializeParams
	ch    chan struct{}
}

func newCaptureLSP() *captureLSP {
	return &captureLSP{ch: make(chan struct{}, 16)}
}

func (l *captureLSP) Initialize(
	_ context.Context, p semanticapi.InitializeParams,
) (semanticapi.InitializeResult, error) {
	l.mu.Lock()
	l.inits = append(l.inits, p)
	l.mu.Unlock()
	l.ch <- struct{}{}
	return semanticapi.InitializeResult{}, nil
}

func (l *captureLSP) captured() []semanticapi.InitializeParams {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]semanticapi.InitializeParams(nil), l.inits...)
}

// waitForInit blocks until the next Initialize, so tests can synchronize
// on the asynchronous, event-driven bring-up.
func (l *captureLSP) waitForInit(t *testing.T) {
	t.Helper()
	select {
	case <-l.ch:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for lsp.Initialize")
	}
}

// fakeEditor records event subscriptions so tests can deliver synthetic
// open events, and the cursor moves it is asked for; it has no open
// buffers. Cursor reports live.
type fakeEditor struct {
	mu       sync.Mutex
	handlers []textapi.EventHandler
	live     term.Coordinates
	moves    []term.Coordinates
}

var _ textapi.Editor = (*fakeEditor)(nil)

func (e *fakeEditor) SubscribeEvents(_ []textapi.EventType, h textapi.EventHandler) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.handlers = append(e.handlers, h)
	return nil
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
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.live, nil
}
func (e *fakeEditor) SetCursor(_ textapi.Handler, c term.Coordinates) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.moves = append(e.moves, c)
	return nil
}
func (e *fakeEditor) CellView(textapi.Handler) textapi.CellView                   { return nil }
func (e *fakeEditor) CellEditor(textapi.Handler) textapi.CellEditor               { return nil }
func (e *fakeEditor) SetDefaultAttributes(textapi.Handler, term.Attributes) error { return nil }

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

type fakeNotifications struct {
	mu     sync.Mutex
	notifs []string
}

func (n *fakeNotifications) Notify(_ browserapi.NotificationLevel, msg string, args ...any) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.notifs = append(n.notifs, fmt.Sprintf(msg, args...))
	return "notif-1", nil
}

func (n *fakeNotifications) NotifyOnce(level browserapi.NotificationLevel, msg string, args ...any) (string, error) {
	return n.Notify(level, msg, args...)
}

func (n *fakeNotifications) UpdateNotificationProgress(_, _ string, _, _ int64) error {
	return nil
}

func (n *fakeNotifications) messages() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.notifs...)
}

type fakeWindow struct{ id uint64 }

func (w fakeWindow) WindowID() uint64 { return w.id }

const editorWinID = 1

// fakeWM records what SetWindowContent installs and the floating
// handler it is asked to show.
type fakeWM struct {
	mu       sync.Mutex
	floating browserapi.Floating
	win      browserapi.Window
	content  browserapi.Handler
}

var _ browserapi.WindowManager = (*fakeWM)(nil)

func (m *fakeWM) Focus() (browserapi.Window, error) { return fakeWindow{id: editorWinID}, nil }
func (m *fakeWM) Split(browserapi.Orientation, browserapi.Window, browserapi.Handler) (browserapi.Window, error) {
	return nil, errors.New("not implemented")
}
func (m *fakeWM) Floating(h browserapi.Floating, _ browserapi.FloatingConfig) (browserapi.Window, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.floating = h
	return fakeWindow{id: editorWinID + 1}, nil
}
func (m *fakeWM) Bar(browserapi.BarConfig, tui.Handler) error { return errors.New("not implemented") }
func (m *fakeWM) Tab(_ workspaceapi.URI, _ rune, _ string, h browserapi.Handler) (browserapi.Handler, error) {
	return h, nil
}
func (m *fakeWM) SetWindowContent(w browserapi.Window, h browserapi.Handler) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.win, m.content = w, h
	return nil
}
func (m *fakeWM) CloseWindow(browserapi.Window) error         { return nil }
func (m *fakeWM) SetTabActivity(workspaceapi.URI, bool) error { return nil }
func (m *fakeWM) SetTabName(workspaceapi.URI, string) error   { return nil }

func (m *fakeWM) shown() (browserapi.Window, browserapi.Handler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.win, m.content
}

func (m *fakeWM) picker() browserapi.Floating {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.floating
}

// stubResource is a textapi.Handler standing in for an open buffer.
type stubResource struct {
	uri workspaceapi.URI
}

var _ textapi.Handler = (*stubResource)(nil)

func (s *stubResource) Handle(term.Event) (bool, bool) { return false, false }
func (s *stubResource) Draw(term.Writer)               {}
func (s *stubResource) Resize(int, int)                {}
func (s *stubResource) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return term.Coordinates{}, 0, false
}
func (s *stubResource) Selection() (string, bool)  { return "", false }
func (s *stubResource) Close() error               { return nil }
func (s *stubResource) Resource() workspaceapi.URI { return s.uri }

// recordingOpener hands out a stubResource for every URI it opens and
// records the URIs.
type recordingOpener struct {
	mu     sync.Mutex
	opened []string
}

func (o *recordingOpener) Open(uri workspaceapi.URI) (browserapi.Handler, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.opened = append(o.opened, uri.String())
	return &stubResource{uri: uri}, nil
}

func (o *recordingOpener) openedURIs() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.opened...)
}

// answer is one scripted reply to an ExecuteRequest.
type answer struct {
	raw string
	err error
}

// requestLSP scripts the LSP calls of the command-prompt subcommands.
// ExecuteRequest pops the next answer for the method and fails once
// they run out; CodeAction and Diagnostic return fixed results. Every
// call's params are recorded.
type requestLSP struct {
	noopLSP
	mu           sync.Mutex
	answers      map[string][]answer
	requests     []semanticapi.ExecuteRequestParams
	actions      []semanticapi.CodeActionResult
	actionParams []semanticapi.CodeActionParams
	diagnostics  []semanticapi.Diagnostic
	diagErr      error
	pulls        int
}

func (l *requestLSP) ExecuteRequest(
	_ context.Context, p semanticapi.ExecuteRequestParams,
) (json.RawMessage, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.requests = append(l.requests, p)
	queue := l.answers[p.Method]
	if len(queue) == 0 {
		return nil, fmt.Errorf("unexpected %s", p.Method)
	}
	next := queue[0]
	l.answers[p.Method] = queue[1:]
	if next.err != nil {
		return nil, next.err
	}
	return json.RawMessage(next.raw), nil
}

func (l *requestLSP) Initialize(
	context.Context, semanticapi.InitializeParams,
) (semanticapi.InitializeResult, error) {
	return semanticapi.InitializeResult{}, nil
}

func (l *requestLSP) CodeAction(
	_ context.Context, p semanticapi.CodeActionParams,
) ([]semanticapi.CodeActionResult, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.actionParams = append(l.actionParams, p)
	return append([]semanticapi.CodeActionResult(nil), l.actions...), nil
}

func (l *requestLSP) Diagnostic(
	context.Context, semanticapi.DocumentDiagnosticParams,
) (semanticapi.DocumentDiagnosticReport, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pulls++
	return semanticapi.DocumentDiagnosticReport{Kind: "full", Items: l.diagnostics}, l.diagErr
}

func (l *requestLSP) recorded() []semanticapi.ExecuteRequestParams {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]semanticapi.ExecuteRequestParams(nil), l.requests...)
}

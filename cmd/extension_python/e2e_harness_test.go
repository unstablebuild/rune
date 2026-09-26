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
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/cmd/extension_python/pyshim"
	"unstable.build/rune/internal/extension/langext"
)

// fakeWindowManager replays a scripted key sequence into every floated
// handler and then closes it, matching the real window manager, which
// tears the window down once a handler reports exit. The prompt reports
// its answer over a buffered channel, so the replay can run inline and
// keep the tests deterministic.
type fakeWindowManager struct {
	events []term.Event

	mu    sync.Mutex
	calls int
}

var _ browserapi.WindowManager = (*fakeWindowManager)(nil)

func (w *fakeWindowManager) Floating(
	h browserapi.Floating, _ browserapi.FloatingConfig,
) (browserapi.Window, error) {
	w.mu.Lock()
	w.calls++
	w.mu.Unlock()
	for _, ev := range w.events {
		if exit, _ := h.Handle(ev); exit {
			break
		}
	}
	return nil, h.Close()
}

func (w *fakeWindowManager) callCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.calls
}

func (w *fakeWindowManager) Focus() (browserapi.Window, error) { return nil, nil }

func (w *fakeWindowManager) Split(
	browserapi.Orientation, browserapi.Window, browserapi.Handler,
) (browserapi.Window, error) {
	return nil, nil
}

func (w *fakeWindowManager) Bar(browserapi.BarConfig, tui.Handler) error { return nil }

func (w *fakeWindowManager) Tab(
	workspaceapi.URI, rune, string, browserapi.Handler,
) (browserapi.Handler, error) {
	return nil, nil
}

func (w *fakeWindowManager) SetWindowContent(browserapi.Window, browserapi.Handler) error {
	return nil
}

func (w *fakeWindowManager) CloseWindow(browserapi.Window) error { return nil }

func (w *fakeWindowManager) SetTabActivity(workspaceapi.URI, bool) error { return nil }

func (w *fakeWindowManager) SetTabName(workspaceapi.URI, string) error { return nil }

// realFS is a minimal workspaceapi.FileSystem backed by the OS and
// rooted at a workspace directory. Relative paths resolve against root,
// so the extension's detection and URI logic run against a real tree
// while the test controls the root.
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

func (f realFS) Stat(p string) (os.FileInfo, error) { return os.Stat(f.resolve(p)) }

func (f realFS) ReadDir(p string) ([]os.DirEntry, error) { return os.ReadDir(f.resolve(p)) }

func (f realFS) OpenFile(p string, flag int, mode os.FileMode) (workspaceapi.File, error) {
	file, err := os.OpenFile(f.resolve(p), flag, mode)
	if err != nil {
		return nil, err
	}
	return file, nil
}
func (f realFS) Remove(p string) error { return os.Remove(f.resolve(p)) }
func (f realFS) MkdirAll(p string, mode os.FileMode) error {
	return os.MkdirAll(f.resolve(p), mode)
}

// fakeEditor is a textapi.Editor that records the open-event
// subscription so tests can deliver synthetic open events to the
// extension's langext.Initializer. Every other method is an unused stub.
type fakeEditor struct {
	mu      sync.Mutex
	handler textapi.EventHandler
}

var _ textapi.Editor = (*fakeEditor)(nil)

func (e *fakeEditor) SubscribeEvents(_ []textapi.EventType, h textapi.EventHandler) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.handler = h
	return nil
}

// open delivers an EventTypeOpen for the file at path to the subscribed
// handler, returning false if no handler has subscribed yet.
func (e *fakeEditor) open(t *testing.T, path string) {
	t.Helper()
	uri, err := workspaceapi.ParseURI("file://" + path)
	require.NoError(t, err)
	e.mu.Lock()
	h := e.handler
	e.mu.Unlock()
	require.NotNil(t, h, "no event handler subscribed")
	h.Handle(context.Background(), textapi.Event{Type: textapi.EventTypeOpen, URI: uri})
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

// captureLSP is a fake semanticapi.LSP that records the InitializeParams
// it received and otherwise no-ops every request. The env+LSP suite
// asserts against the captured params instead of running a real server.
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

// scenarioEnv is the result of running the extension against a scenario:
// the workspace directory, the data dir the shims were written to, the
// executor rooted there, and the captured LSP/notifications for
// assertions.
type scenarioEnv struct {
	dir     string
	dataDir string
	exec    realExecutor
	lsp     *captureLSP
	notify  *fakeNotifications
	editor  *fakeEditor
	storage storageapi.Service
	manuals []textapi.CommandManual
	handler textapi.REPLHandler
}

// loadScenario copies testdata/<name> into a fresh temp directory so each
// run is isolated and uv side effects do not leak into the source tree.
// For the venv_only scenario it pre-seeds a populated .venv from
// venv-packages.txt, simulating a workspace that already has a virtual
// environment the extension must reuse without re-installing.
func loadScenario(t *testing.T, name string) string {
	t.Helper()
	src := filepath.Join("testdata", name)
	dir := t.TempDir()
	copyTree(t, src, dir)

	pkgs := filepath.Join(dir, "venv-packages.txt")
	if data, err := os.ReadFile(pkgs); err == nil {
		require.NoError(t, os.Remove(pkgs))
		seedVenv(t, dir, string(data))
	}
	return dir
}

// copyTree recursively copies src into dst, preserving relative layout.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	entries, err := os.ReadDir(src)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dst, 0o755))
	for _, e := range entries {
		s := filepath.Join(src, e.Name())
		d := filepath.Join(dst, e.Name())
		if e.IsDir() {
			copyTree(t, s, d)
			continue
		}
		data, err := os.ReadFile(s)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(d, data, 0o644))
	}
}

// seedVenv creates a .venv in dir and installs the given requirements
// into it via real uv, leaving the workspace in the kindVenvOnly state
// the extension must detect and reuse.
func seedVenv(t *testing.T, dir, requirements string) {
	t.Helper()
	ctx := context.Background()
	ex := newDirExecutor(dir)
	require.NoError(t, runUV(ctx, "uv", ex, "", "venv"))
	reqFile := filepath.Join(dir, ".venv-seed-requirements.txt")
	require.NoError(t, os.WriteFile(reqFile, []byte(requirements), 0o644))
	require.NoError(t, runUV(ctx, "uv", ex, "", "pip", "install", "-r", reqFile))
	require.NoError(t, os.Remove(reqFile))
}

// runExtensionOnScenario loads the named scenario, runs the extension's
// full bring-up (detection, uv env, LSP init, REPL registration) against
// a real FileSystem and Executor with a fake LSP and Notifications, and
// returns the resulting environment for assertions.
func runExtensionOnScenario(t *testing.T, name string) scenarioEnv {
	t.Helper()
	dir := loadScenario(t, name)
	return runExtensionOnDir(t, dir)
}

// seedManagedFallback pre-creates the uvbin interpreter stub the shims
// fall back to, so the extension's bring-up never runs `uv python
// install` against the ambient uv directories. The scenarios always
// resolve a project .venv, so the stub is probed for existence but
// never executed.
func seedManagedFallback(t *testing.T, dataDir string) {
	t.Helper()
	fallback := pyshim.FallbackPath(dataDir)
	require.NoError(t, os.MkdirAll(filepath.Dir(fallback), 0o755))
	require.NoError(t, os.WriteFile(fallback, []byte("#!/bin/sh\nexit 127\n"), 0o755))
}

// runExtensionOnDir runs the extension's full bring-up against the
// already-prepared workspace directory dir, using a real FileSystem and
// Executor with a fake LSP and Notifications, and returns the resulting
// environment for assertions.
func runExtensionOnDir(t *testing.T, dir string) scenarioEnv {
	t.Helper()
	storage := storagestub.NewInMemoryService()
	require.NoError(t, newEnvSetting(storage).set(
		context.Background(), dirRoot(dir), true))
	// Nested roots discovered later have no stored answer, so an
	// auto-accepting prompt keeps them managed like the workspace root.
	return runExtensionOnDirWith(t, dir, storage, &fakeWindowManager{events: enterEvents})
}

// dirRoot builds the langext.Root the extension derives for a workspace
// root directory, so tests can seed a policy for it up front.
func dirRoot(dir string) langext.Root {
	return langext.Root{Dir: dir, URI: "file://" + dir}
}

// runExtensionOnDirWith runs the extension bring-up against dir with an
// explicit storage service and window manager, so tests can control the
// environment policy and the prompt.
func runExtensionOnDirWith(
	t *testing.T, dir string, storage storageapi.Service, wm *fakeWindowManager,
) scenarioEnv {
	t.Helper()
	dataDir := t.TempDir()
	seedManagedFallback(t, dataDir)
	ex := newDirExecutor(dir)
	lsp := &captureLSP{}
	notify := newFakeNotifications()
	editor := &fakeEditor{}
	env := scenarioEnv{
		dir: dir, dataDir: dataDir,
		exec: ex, lsp: lsp, notify: notify, editor: editor,
		storage: storage,
	}
	var windows browserapi.WindowManager
	if wm != nil {
		windows = wm
	}

	ext := &pyExtension{}
	err := ext.extendWorkspaceWith(context.Background(),
		realFS{root: dir},
		ex,
		notify,
		lsp,
		editor,
		fakeInstaller{fs: realFS{root: dir}, root: ""},
		config.NopConfig(),
		dataDir,
		storage,
		windows,
		func(m textapi.CommandManual, h textapi.REPLHandler) error {
			env.manuals = append(env.manuals, m)
			env.handler = h
			return nil
		},
	)
	require.NoError(t, err)
	return env
}

// runPython runs the scenario's main.py through the synced environment
// via `uv run` and returns its stdout, so all scenarios are verified to
// produce identical results regardless of how the env was expressed.
func runPython(t *testing.T, dir string) string {
	t.Helper()
	out, err := runUVCaptureDir(t, dir, "run", "python", "main.py")
	require.NoError(t, err)
	return out
}

// runUVCaptureDir runs `uv <args>` in dir through the real executor and
// returns trimmed stdout. It reuses the handler's capture path so the
// e2e suite exercises the same code that backs the `python` REPL command.
func runUVCaptureDir(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	h := &pyHandler{exec: newDirExecutor(dir), notify: newFakeNotifications(), cwd: dir}
	return h.runUVCapture(context.Background(), args...)
}

// allScenarios lists the testdata scenario directories driven by the
// env+LSP and REPL suites. Add a folder here to cover a new in-the-wild
// environment shape.
var allScenarios = []string{"pyproject", "requirements", "venv_only"}

// realExecutor spawns real subprocesses, used by the e2e tests against
// an installed uv. The watcher receives the process exit error. dir, when
// set, is the default working directory for commands that do not specify
// their own, mirroring the workspace executor being rooted at the
// workspace directory in production.
type realExecutor struct{ dir string }

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

// newDirExecutor returns a realExecutor rooted at dir, so commands that
// do not set Cmd.Dir run in the workspace directory.
func newDirExecutor(dir string) realExecutor { return realExecutor{dir: dir} }

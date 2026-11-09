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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/ide/idelsp"
)

// findZlsBin locates the zls binary or skips the test. It mirrors
// resolveZls's search order: PATH first, then well-known locations.
func findZlsBin(t *testing.T) string {
	t.Helper()
	if bin, err := exec.LookPath("zls"); err == nil {
		return bin
	}
	for _, p := range []string{
		filepath.Join(os.Getenv("HOME"), ".rune", "bin", "zls"),
		"/opt/homebrew/bin/zls",
		"/usr/local/bin/zls",
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Skip("zls not found, skipping e2e test")
	return ""
}

// findZigBin locates the zig binary or skips the test.
func findZigBin(t *testing.T) string {
	t.Helper()
	if bin, err := exec.LookPath("zig"); err == nil {
		return bin
	}
	for _, p := range []string{
		filepath.Join(os.Getenv("HOME"), ".rune", "bin", "zig"),
		"/opt/homebrew/bin/zig",
		"/usr/local/bin/zig",
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Skip("zig not found, skipping e2e test")
	return ""
}

// setupZigWorkspace copies the testdata Zig project into a fresh temp
// directory so each test gets an isolated, writable workspace, and
// returns its path.
func setupZigWorkspace(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "zig-ext-e2e-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	src := "testdata/e2e"
	require.NoError(t, filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o644)
	}))
	return dir
}

// zlsCallback implements idelsp.Callback for the e2e tests, recording
// published diagnostics.
type zlsCallback struct {
	mu          sync.Mutex
	diagnostics []semanticapi.PublishDiagnosticsParams
}

func (c *zlsCallback) ShowMessage(_ context.Context, _ semanticapi.ShowMessageParams) error {
	return nil
}

func (c *zlsCallback) LogMessage(_ context.Context, _ semanticapi.LogMessageParams) error {
	return nil
}

func (c *zlsCallback) PublishDiagnostics(
	_ context.Context, params semanticapi.PublishDiagnosticsParams,
) error {
	c.mu.Lock()
	c.diagnostics = append(c.diagnostics, params)
	c.mu.Unlock()
	return nil
}

func (c *zlsCallback) Progress(_ context.Context, _ semanticapi.ProgressParams) error {
	return nil
}

func (c *zlsCallback) LogTrace(_ context.Context, _ semanticapi.LogTraceParams) error { return nil }

func (c *zlsCallback) ShowDocument(
	_ context.Context, _ semanticapi.ShowDocumentParams,
) (semanticapi.ShowDocumentResult, error) {
	return semanticapi.ShowDocumentResult{Success: true}, nil
}

func (c *zlsCallback) ShowMessageRequest(
	_ context.Context, _ semanticapi.ShowMessageRequestParams,
) (*semanticapi.MessageActionItem, error) {
	return nil, nil
}

func (c *zlsCallback) WorkDoneProgressCreate(
	_ context.Context, _ semanticapi.WorkDoneProgressCreateParams,
) error {
	return nil
}

func (c *zlsCallback) ApplyEdit(
	_ context.Context, _ semanticapi.ApplyWorkspaceEditParams,
) (semanticapi.ApplyWorkspaceEditResult, error) {
	return semanticapi.ApplyWorkspaceEditResult{Applied: true}, nil
}

func (c *zlsCallback) WorkspaceFolders(_ context.Context) ([]semanticapi.WorkspaceFolder, error) {
	return nil, nil
}

func (c *zlsCallback) Configuration(
	_ context.Context, params semanticapi.ConfigurationParams,
) ([]json.RawMessage, error) {
	result := make([]json.RawMessage, len(params.Items))
	for i := range result {
		result[i] = json.RawMessage(`null`)
	}
	return result, nil
}

func (c *zlsCallback) RegisterCapability(_ context.Context, _ semanticapi.RegistrationParams) error {
	return nil
}

func (c *zlsCallback) UnregisterCapability(
	_ context.Context, _ semanticapi.UnregistrationParams,
) error {
	return nil
}

func (c *zlsCallback) CodeLensRefresh(_ context.Context) error       { return nil }
func (c *zlsCallback) SemanticTokensRefresh(_ context.Context) error { return nil }
func (c *zlsCallback) InlayHintRefresh(_ context.Context) error      { return nil }
func (c *zlsCallback) DiagnosticRefresh(_ context.Context) error     { return nil }
func (c *zlsCallback) FileDidChange(_ string, _ int32, _, _ bool)    {}
func (c *zlsCallback) InvalidateAllPending()                         {}
func (c *zlsCallback) WaitFileProcessed(_ context.Context, _ string) error {
	return nil
}

func (c *zlsCallback) HandleNotification(
	_ context.Context, _ string, _ json.RawMessage,
) error {
	return nil
}

// diagnosticsFor returns the most recent published diagnostics for uri.
func (c *zlsCallback) diagnosticsFor(uri string) ([]semanticapi.Diagnostic, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.diagnostics) - 1; i >= 0; i-- {
		if c.diagnostics[i].URI == uri {
			return c.diagnostics[i].Diagnostics, true
		}
	}
	return nil, false
}

// localScheme implements schemeapi.FileSystem and schemeapi.Executor
// over the local OS, rooted at a directory so relative paths resolve
// into the test workspace.
type localScheme struct {
	root    string
	mu      sync.Mutex
	procs   map[workspaceapi.Pid]*os.Process
	nextPid workspaceapi.Pid
	started []startedProcess
}

type synchronizedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *synchronizedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *synchronizedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

type startedProcess struct {
	path   string
	args   []string
	env    []string
	stderr *synchronizedBuffer
}

func (s *localScheme) startedProcess(path string) (startedProcess, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.started) - 1; i >= 0; i-- {
		if s.started[i].path == path {
			return s.started[i], true
		}
	}
	return startedProcess{}, false
}

func newLocalScheme(root string) *localScheme {
	return &localScheme{root: root, procs: make(map[workspaceapi.Pid]*os.Process), nextPid: 1}
}

func (s *localScheme) resolve(path string) string {
	if s.root == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(s.root, path)
}

func (s *localScheme) URI(path string) (workspaceapi.URI, error) {
	return workspaceapi.ParseURI("file://" + s.resolve(path))
}
func (s *localScheme) Create(name string) (workspaceapi.File, error) {
	return os.Create(s.resolve(name))
}
func (s *localScheme) Open(name string) (workspaceapi.File, error) { return os.Open(s.resolve(name)) }
func (s *localScheme) OpenFile(name string, flag int, perm os.FileMode) (workspaceapi.File, error) {
	return os.OpenFile(s.resolve(name), flag, perm)
}
func (s *localScheme) Stat(name string) (os.FileInfo, error) { return os.Stat(s.resolve(name)) }
func (s *localScheme) Rename(o, n string) error {
	return os.Rename(s.resolve(o), s.resolve(n))
}
func (s *localScheme) Remove(name string) error   { return os.Remove(s.resolve(name)) }
func (s *localScheme) Join(elem ...string) string { return filepath.Join(elem...) }
func (s *localScheme) TempFile(dir, prefix string) (workspaceapi.File, error) {
	return os.CreateTemp(s.resolve(dir), prefix)
}
func (s *localScheme) Lstat(name string) (os.FileInfo, error) { return os.Lstat(s.resolve(name)) }
func (s *localScheme) Symlink(o, n string) error {
	return os.Symlink(s.resolve(o), s.resolve(n))
}
func (s *localScheme) Readlink(link string) (string, error) { return os.Readlink(s.resolve(link)) }
func (s *localScheme) ReadDir(path string) ([]os.DirEntry, error) {
	return os.ReadDir(s.resolve(path))
}
func (s *localScheme) MkdirAll(name string, perm os.FileMode) error {
	return os.MkdirAll(s.resolve(name), perm)
}

func (s *localScheme) Start(ctx context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
	return s.StartCommand(ctx, cmd)
}

func (s *localScheme) StartCommand(
	ctx context.Context, cmd workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	stderr := &synchronizedBuffer{}
	if cmd.Stderr == nil {
		cmd.Stderr = stderr
	}
	c := exec.CommandContext(ctx, cmd.Path, cmd.Args...)
	if cmd.Dir != "" {
		c.Dir = cmd.Dir
	}
	if cmd.Env != nil {
		c.Env = cmd.Env
	}
	c.Stdin = cmd.Stdin
	c.Stdout = cmd.Stdout
	c.Stderr = cmd.Stderr
	if cmd.SysProcAttr != nil {
		c.SysProcAttr = cmd.SysProcAttr
	}
	if err := c.Start(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	pid := s.nextPid
	s.nextPid++
	s.procs[pid] = c.Process
	s.started = append(s.started, startedProcess{
		path: cmd.Path, args: append([]string{}, cmd.Args...),
		env: append([]string{}, cmd.Env...), stderr: stderr,
	})
	s.mu.Unlock()
	if cmd.Watcher != nil {
		ch := cmd.Watcher.WatchProcess()
		go func() {
			err := c.Wait()
			if ch != nil {
				ch <- err
			}
		}()
	}
	return pid, nil
}

func (s *localScheme) Signal(pid workspaceapi.Pid, sig syscall.Signal) error {
	s.mu.Lock()
	proc, ok := s.procs[pid]
	s.mu.Unlock()
	if !ok {
		return os.ErrProcessDone
	}
	return proc.Signal(sig)
}

func (*localScheme) LookPath(context.Context, string, string) (string, error) {
	return "", errors.ErrUnsupported
}

func (s *localScheme) Close() error { return nil }

// stubPkgManager implements idelsp.PkgManager, resolving the language
// server directly to the discovered binary.
type stubPkgManager struct {
	bin string
}

func (p *stubPkgManager) LibDir(
	_ context.Context, _ string,
) (iterator.Iterator[string], error) {
	return iterator.FromSlice([]string{p.bin}), nil
}

// zigEnvE2E holds the initialized zls manager and file URIs.
type zigEnvE2E struct {
	mgr      *idelsp.Manager
	cb       *zlsCallback
	dir      string
	fileURIs map[string]string
}

// initZls creates an idelsp.Manager, initializes zls with the
// extension's zlsInitializeParams, opens the given files, and waits for
// the server to answer about them, mirroring the production bring-up in
// initializeZigRoot. bos lets each test pick its build-on-save mode;
// tests that do not assert on build-on-save disable it so they never
// spawn a zig build.
func initZls(
	t *testing.T, zlsBin, zigBin string, openFiles []string, bos buildOnSaveOptions,
) *zigEnvE2E {
	t.Helper()
	dir := setupZigWorkspace(t)
	rootURI := "file://" + dir

	uri, err := workspaceapi.ParseURI(rootURI)
	require.NoError(t, err)

	scheme := newLocalScheme(dir)
	cb := &zlsCallback{}
	cfg := idelsp.Config{MaxRetries: 1, Callback: cb, WorkDoneProgress: true}

	mgr := idelsp.New(uri, scheme, scheme, &stubPkgManager{bin: zlsBin}, nil, nil, cfg)

	ctx := context.Background()
	params, err := zlsInitializeParams(
		rootURI, filepath.Base(dir), zlsBin, zigBin, "info", bos)
	require.NoError(t, err)

	_, err = mgr.Initialize(ctx, params)
	require.NoError(t, err)

	fileURIs := make(map[string]string)
	for _, name := range openFiles {
		path := filepath.Join(dir, name)
		fileURI := "file://" + path
		fileURIs[name] = fileURI

		content, err := os.ReadFile(path)
		require.NoError(t, err)
		// Open synchronously rather than through the async editor-event
		// path: a request racing an in-flight open makes the manager
		// temp-open the file and its temp-close then closes the document
		// on the server while the manager believes it is open.
		require.NoError(t, mgr.DidOpen(ctx, semanticapi.DidOpenTextDocumentParams{
			TextDocument: semanticapi.TextDocumentItem{
				URI:        fileURI,
				LanguageID: "zig",
				Version:    0,
				Text:       string(content),
			},
		}))
	}

	env := &zigEnvE2E{mgr: mgr, cb: cb, dir: dir, fileURIs: fileURIs}
	t.Cleanup(func() { require.NoError(t, mgr.Close()) })
	waitZlsReady(t, env)
	return env
}

// waitZlsReady polls zls until it both resolves cross-file definitions
// and answers hover in the opened fixture, so assertions do not race the
// server's async startup (zls resolves the zig toolchain and build.zig
// in the background after initialize).
func waitZlsReady(t *testing.T, env *zigEnvE2E) {
	t.Helper()
	mainURI, ok := env.fileURIs["src/main.zig"]
	if !ok {
		return
	}
	pos := locateInFile(t, filepath.Join(env.dir, "src", "main.zig"), "lib.add(", "add(")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	for {
		res, err := env.mgr.Definition(ctx, semanticapi.DefinitionParams{
			TextDocument: semanticapi.TextDocumentIdentifier{URI: mainURI},
			Position:     pos,
		})
		if err == nil && (res.Location != nil || len(res.Locations) > 0 || len(res.LocationLinks) > 0) {
			hover, herr := env.mgr.Hover(ctx, semanticapi.HoverParams{
				TextDocument: semanticapi.TextDocumentIdentifier{URI: mainURI},
				Position:     pos,
			})
			if herr == nil && hover != nil {
				return
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal("timed out waiting for zls to resolve the fixture")
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// locateInFile returns the 0-based LSP position of ident on the first
// line containing marker, so fixture edits cannot desynchronize
// hard-coded coordinates.
func locateInFile(t *testing.T, path, marker, ident string) semanticapi.Position {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	for i, line := range strings.Split(string(data), "\n") {
		if !strings.Contains(line, marker) {
			continue
		}
		col := strings.Index(line, ident)
		require.GreaterOrEqual(t, col, 0)
		return semanticapi.Position{Line: uint32(i), Character: uint32(col)}
	}
	t.Fatalf("could not find %q in %s", marker, path)
	return semanticapi.Position{}
}

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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/ide/idelsp"
)

// realLSPCloseTimeout bounds Manager.Close's shutdown call against a
// real gopls. The default 3s is enough for a settled server, but on
// loaded CI runners gopls can still be indexing when shutdown arrives
// and takes longer to answer, failing the cleanup of tests that
// otherwise passed. Mirrors idelsp/go_test.go.
const realLSPCloseTimeout = 15 * time.Second

// findGopls locates the gopls binary or skips the test.
func findGopls(t *testing.T) string {
	t.Helper()
	goplsBin, err := exec.LookPath("gopls")
	if err != nil {
		for _, p := range []string{
			filepath.Join(os.Getenv("HOME"), ".rune", "bin", "gopls"),
			filepath.Join(os.Getenv("HOME"), "go", "bin", "gopls"),
		} {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
		t.Skip("gopls not found, skipping e2e test")
	}
	return goplsBin
}

// testFile describes a file to create in the test workspace.
type testFile struct {
	name    string
	content string
}

// setupWorkspace creates a temp directory with a go.mod and the given files.
// It returns the temp directory path.
func setupWorkspace(t *testing.T, moduleName string, files []testFile) string {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "go-ext-test-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })

	goMod := fmt.Sprintf("module %s\n\ngo 1.22\n", moduleName)
	require.NoError(t, os.WriteFile(
		filepath.Join(tmpDir, "go.mod"), []byte(goMod), 0o644,
	))

	for _, f := range files {
		dir := filepath.Dir(filepath.Join(tmpDir, f.name))
		if dir != tmpDir {
			require.NoError(t, os.MkdirAll(dir, 0o755))
		}
		require.NoError(t, os.WriteFile(
			filepath.Join(tmpDir, f.name), []byte(f.content), 0o644,
		))
	}

	return tmpDir
}

// testEnv holds the initialized gopls manager and file URIs for a test.
type testEnv struct {
	mgr      *idelsp.Manager
	cb       *testCallback
	dir      string
	fileURIs map[string]string // filename -> file:// URI
}

// initGopls creates a new idelsp.Manager, initializes gopls with the
// extension's goplsInitializeParams, opens the specified files via
// Handle(EventTypeOpen), and waits for gopls to finish loading.
func initGopls(t *testing.T, goplsBin string, files []testFile) *testEnv {
	t.Helper()
	dir := setupWorkspace(t, "example.com/test", files)
	return initGoplsFromDir(t, goplsBin, dir, files)
}

// initGoplsFromDir initializes gopls for a pre-existing workspace
// directory. Use this when the workspace requires custom setup (e.g.
// go mod tidy) between directory creation and gopls initialization.
// The files parameter controls which files are opened via
// Handle(EventTypeOpen); they must already exist on disk.
func initGoplsFromDir(
	t *testing.T, goplsBin string, dir string, files []testFile,
) *testEnv {
	t.Helper()

	rootURI := "file://" + dir

	uri, err := workspaceapi.ParseURI(rootURI)
	require.NoError(t, err)

	scheme := newTestScheme()

	ready := make(chan struct{})
	var once sync.Once
	cb := &testCallback{
		onShowMessage: func(params semanticapi.ShowMessageParams) {
			if strings.Contains(params.Message, "Finished loading packages") ||
				strings.Contains(params.Message, "background refresh finished") {
				once.Do(func() { close(ready) })
			}
		},
		onProgress: readyOnProgressCh(&once, ready),
	}
	cfg := idelsp.Config{MaxRetries: 1, Callback: cb,
		CloseTimeout: realLSPCloseTimeout}

	mgr := idelsp.New(
		uri,
		scheme,
		scheme,
		&stubPkgManager{bin: goplsBin},
		nil, // notifications
		nil, // opener
		cfg,
	)

	ctx := context.Background()

	params, err := goplsInitializeParams(rootURI, goplsDebugOptions{}, "")
	require.NoError(t, err)

	_, err = mgr.Initialize(ctx, params)
	require.NoError(t, err)

	fileURIs := make(map[string]string)
	for _, f := range files {
		path := filepath.Join(dir, f.name)
		fileURI := "file://" + path
		fileURIs[f.name] = fileURI

		wsURI, err := workspaceapi.ParseURI(fileURI)
		require.NoError(t, err)
		// Rune's editor trims the trailing newline from file content.
		mgr.Handle(ctx, textapi.Event{
			Type:    textapi.EventTypeOpen,
			URI:     wsURI,
			Content: strings.TrimSuffix(f.content, "\n"),
		})
	}

	waitGoplsReady(t, ready, mgr, fileURIs)

	t.Cleanup(func() {
		require.NoError(t, mgr.Close())
	})

	return &testEnv{
		mgr:      mgr,
		cb:       cb,
		dir:      dir,
		fileURIs: fileURIs,
	}
}

// applyEditEnv extends testEnv with captured workspace/applyEdit params.
type applyEditEnv struct {
	*testEnv
	cb *testCallback
}

func (e *applyEditEnv) capturedEdits() []semanticapi.ApplyWorkspaceEditParams {
	e.cb.mu.Lock()
	defer e.cb.mu.Unlock()
	return append([]semanticapi.ApplyWorkspaceEditParams{}, e.cb.appliedEdits...)
}

// simulateEditorEvents sets up onApplyEdit to fire Handle(EventTypeEdit)
// for each edit in workspace/applyEdit, simulating the real IDE where
// buffer changes from ApplyEdit trigger editor events that send
// textDocument/didChange to the language server.
func (e *applyEditEnv) simulateEditorEvents() {
	e.cb.mu.Lock()
	e.cb.onApplyEdit = func(params semanticapi.ApplyWorkspaceEditParams) {
		edit := params.Edit
		if len(edit.DocumentChanges) > 0 {
			for _, dc := range edit.DocumentChanges {
				if dc.TextDocumentEdit == nil {
					continue
				}
				wsURI, err := workspaceapi.ParseURI(dc.TextDocumentEdit.TextDocument.URI)
				if err != nil {
					continue
				}
				for _, te := range dc.TextDocumentEdit.Edits {
					e.mgr.Handle(context.Background(), textapi.Event{
						Type:    textapi.EventTypeEdit,
						URI:     wsURI,
						Content: te.NewText,
						Start:   term.Coordinates{X: int(te.Range.Start.Character), Y: int(te.Range.Start.Line)},
						End:     term.Coordinates{X: int(te.Range.End.Character), Y: int(te.Range.End.Line)},
					})
				}
			}
			return
		}
		for fileURI, edits := range edit.Changes {
			wsURI, err := workspaceapi.ParseURI(fileURI)
			if err != nil {
				continue
			}
			for _, te := range edits {
				e.mgr.Handle(context.Background(), textapi.Event{
					Type:    textapi.EventTypeEdit,
					URI:     wsURI,
					Content: te.NewText,
					Start:   term.Coordinates{X: int(te.Range.Start.Character), Y: int(te.Range.Start.Line)},
					End:     term.Coordinates{X: int(te.Range.End.Character), Y: int(te.Range.End.Line)},
				})
			}
		}
	}
	e.cb.mu.Unlock()
}

// initGoplsWithApplyEdit is like initGopls but captures workspace/applyEdit
// params and simulates editor events (Handle(EventTypeEdit)) so the language
// server's overlay stays in sync — matching real IDE behavior.
func initGoplsWithApplyEdit(
	t *testing.T, goplsBin string, files []testFile,
) *applyEditEnv {
	t.Helper()

	dir := setupWorkspace(t, "example.com/test", files)
	rootURI := "file://" + dir

	uri, err := workspaceapi.ParseURI(rootURI)
	require.NoError(t, err)

	scheme := newTestScheme()

	ready := make(chan struct{})
	var once sync.Once
	cb := &testCallback{
		onShowMessage: func(params semanticapi.ShowMessageParams) {
			if strings.Contains(params.Message, "Finished loading packages") ||
				strings.Contains(params.Message, "background refresh finished") {
				once.Do(func() { close(ready) })
			}
		},
		onProgress: readyOnProgressCh(&once, ready),
	}
	cfg := idelsp.Config{
		MaxRetries:   1,
		Callback:     cb,
		CloseTimeout: realLSPCloseTimeout,
	}

	mgr := idelsp.New(
		uri, scheme, scheme, &stubPkgManager{bin: goplsBin},
		nil, nil, cfg,
	)

	ctx := context.Background()

	params, err := goplsInitializeParams(rootURI, goplsDebugOptions{}, "")
	require.NoError(t, err)

	_, err = mgr.Initialize(ctx, params)
	require.NoError(t, err)

	fileURIs := make(map[string]string)
	for _, f := range files {
		path := filepath.Join(dir, f.name)
		fileURI := "file://" + path
		fileURIs[f.name] = fileURI

		wsURI, err := workspaceapi.ParseURI(fileURI)
		require.NoError(t, err)
		// Rune's editor trims the trailing newline from file content.
		mgr.Handle(ctx, textapi.Event{
			Type:    textapi.EventTypeOpen,
			URI:     wsURI,
			Content: strings.TrimSuffix(f.content, "\n"),
		})
	}

	waitGoplsReady(t, ready, mgr, fileURIs)
	t.Cleanup(func() { require.NoError(t, mgr.Close()) })

	env := &applyEditEnv{
		testEnv: &testEnv{mgr: mgr, dir: dir, fileURIs: fileURIs},
		cb:      cb,
	}
	env.simulateEditorEvents()
	return env
}

// autoInitLikeParams builds params that mirror what Manager.autoInitParams
// produces (no gopls-specific initializationOptions, no workspace.configuration,
// no workspace.workspaceEdit.documentChanges), plus the langID/command keys
// that Manager.Initialize requires.
func autoInitLikeParams(rootURI string) (semanticapi.InitializeParams, error) {
	initOptions := map[string]any{
		"langID":  "go",
		"command": "gopls serve",
	}
	initOptionsData, err := json.Marshal(initOptions)
	if err != nil {
		return semanticapi.InitializeParams{}, err
	}

	capabilities := map[string]any{
		"textDocument": map[string]any{
			"implementation": map[string]any{
				"linkSupport": true,
			},
			"completion":     map[string]any{},
			"hover":          map[string]any{},
			"signatureHelp":  map[string]any{},
			"definition":     map[string]any{},
			"references":     map[string]any{},
			"documentSymbol": map[string]any{},
			"formatting":     map[string]any{},
			"rename": map[string]any{
				"prepareSupport": true,
			},
			"codeAction":        map[string]any{},
			"codeLens":          map[string]any{},
			"foldingRange":      map[string]any{"lineFoldingOnly": false},
			"selectionRange":    map[string]any{},
			"documentHighlight": map[string]any{},
			"callHierarchy":     map[string]any{},
			"inlayHint":         map[string]any{},
			"semanticTokens": map[string]any{
				"requests": map[string]any{"full": true, "range": true},
				"tokenTypes": []string{
					"namespace", "type", "class", "enum", "interface", "struct",
					"typeParameter", "parameter", "variable", "property",
					"enumMember", "event", "function", "method", "macro",
					"keyword", "modifier", "comment", "string", "number",
					"regexp", "operator", "decorator", "label",
				},
				"tokenModifiers": []string{
					"declaration", "definition", "readonly", "static",
					"deprecated", "abstract", "async", "modification",
					"documentation", "defaultLibrary",
				},
				"formats": []string{"relative"},
			},
		},
		"workspace": map[string]any{
			"symbol":      map[string]any{},
			"diagnostics": map[string]any{},
		},
		"window": map[string]any{
			"workDoneProgress": true,
		},
	}
	capsData, err := json.Marshal(capabilities)
	if err != nil {
		return semanticapi.InitializeParams{}, err
	}

	return semanticapi.InitializeParams{
		RootURI:           rootURI,
		Capabilities:      json.RawMessage(capsData),
		InitializeOptions: json.RawMessage(initOptionsData),
	}, nil
}

// initGoplsWithAutoInitParams creates a gopls environment using params
// that mirror Manager.autoInitParams (no gopls-specific settings, no
// workspace.configuration, no workspace.workspaceEdit.documentChanges).
func initGoplsWithAutoInitParams(
	t *testing.T, goplsBin string, files []testFile,
) *applyEditEnv {
	t.Helper()

	dir := setupWorkspace(t, "example.com/test", files)
	rootURI := "file://" + dir

	uri, err := workspaceapi.ParseURI(rootURI)
	require.NoError(t, err)

	scheme := newTestScheme()

	ready := make(chan struct{})
	var once sync.Once
	cb := &testCallback{
		onShowMessage: func(params semanticapi.ShowMessageParams) {
			if strings.Contains(params.Message, "Finished loading packages") ||
				strings.Contains(params.Message, "background refresh finished") {
				once.Do(func() { close(ready) })
			}
		},
		onProgress: readyOnProgressCh(&once, ready),
	}
	cfg := idelsp.Config{
		MaxRetries:   1,
		Callback:     cb,
		CloseTimeout: realLSPCloseTimeout,
	}

	mgr := idelsp.New(
		uri, scheme, scheme, &stubPkgManager{bin: goplsBin},
		nil, nil, cfg,
	)

	params, err := autoInitLikeParams(rootURI)
	require.NoError(t, err)

	ctx := context.Background()
	_, err = mgr.Initialize(ctx, params)
	require.NoError(t, err)

	fileURIs := make(map[string]string)
	for _, f := range files {
		path := filepath.Join(dir, f.name)
		fileURI := "file://" + path
		fileURIs[f.name] = fileURI

		wsURI, err := workspaceapi.ParseURI(fileURI)
		require.NoError(t, err)
		// Rune's editor trims the trailing newline from file content.
		mgr.Handle(ctx, textapi.Event{
			Type:    textapi.EventTypeOpen,
			URI:     wsURI,
			Content: strings.TrimSuffix(f.content, "\n"),
		})
	}

	waitGoplsReady(t, ready, mgr, fileURIs)
	t.Cleanup(func() { require.NoError(t, mgr.Close()) })

	env := &applyEditEnv{
		testEnv: &testEnv{mgr: mgr, dir: dir, fileURIs: fileURIs},
		cb:      cb,
	}
	env.simulateEditorEvents()
	return env
}

// stubPkgManager implements idelsp.PkgManager for tests.
type stubPkgManager struct {
	bin string
}

func (p *stubPkgManager) LibDir(
	_ context.Context, _ string,
) (iterator.Iterator[string], error) {
	return iterator.FromSlice([]string{p.bin}), nil
}

// testCallback implements semanticapi.LSPCallback for e2e tests.
type testCallback struct {
	mu            sync.Mutex
	onShowMessage func(params semanticapi.ShowMessageParams)
	onLogMessage  func(params semanticapi.LogMessageParams)
	onProgress    func(semanticapi.ProgressParams)
	onApplyEdit   func(semanticapi.ApplyWorkspaceEditParams)
	onDiagnostics func(semanticapi.PublishDiagnosticsParams)
	appliedEdits  []semanticapi.ApplyWorkspaceEditParams
	diagnostics   []semanticapi.PublishDiagnosticsParams
}

func (c *testCallback) ShowMessage(
	_ context.Context, params semanticapi.ShowMessageParams,
) error {
	c.mu.Lock()
	cb := c.onShowMessage
	c.mu.Unlock()
	if cb != nil {
		cb(params)
	}
	return nil
}

func (c *testCallback) LogMessage(
	_ context.Context, params semanticapi.LogMessageParams,
) error {
	c.mu.Lock()
	cb := c.onLogMessage
	c.mu.Unlock()
	if cb != nil {
		cb(params)
	}
	return nil
}

func (c *testCallback) PublishDiagnostics(
	_ context.Context, params semanticapi.PublishDiagnosticsParams,
) error {
	c.mu.Lock()
	c.diagnostics = append(c.diagnostics, params)
	cb := c.onDiagnostics
	c.mu.Unlock()
	if cb != nil {
		cb(params)
	}
	return nil
}

func (c *testCallback) Progress(
	_ context.Context, params semanticapi.ProgressParams,
) error {
	c.mu.Lock()
	cb := c.onProgress
	c.mu.Unlock()
	if cb != nil {
		cb(params)
	}
	return nil
}

func (c *testCallback) LogTrace(
	_ context.Context, _ semanticapi.LogTraceParams,
) error {
	return nil
}

func (c *testCallback) ShowDocument(
	_ context.Context, _ semanticapi.ShowDocumentParams,
) (semanticapi.ShowDocumentResult, error) {
	return semanticapi.ShowDocumentResult{Success: true}, nil
}

func (c *testCallback) ShowMessageRequest(
	_ context.Context, _ semanticapi.ShowMessageRequestParams,
) (*semanticapi.MessageActionItem, error) {
	return nil, nil
}

func (c *testCallback) WorkDoneProgressCreate(
	_ context.Context, _ semanticapi.WorkDoneProgressCreateParams,
) error {
	return nil
}

func (c *testCallback) ApplyEdit(
	_ context.Context,
	params semanticapi.ApplyWorkspaceEditParams,
) (semanticapi.ApplyWorkspaceEditResult, error) {
	c.mu.Lock()
	c.appliedEdits = append(c.appliedEdits, params)
	cb := c.onApplyEdit
	c.mu.Unlock()
	if cb != nil {
		cb(params)
	}
	return semanticapi.ApplyWorkspaceEditResult{Applied: true}, nil
}

func (c *testCallback) WorkspaceFolders(
	_ context.Context,
) ([]semanticapi.WorkspaceFolder, error) {
	return nil, nil
}

func (c *testCallback) Configuration(
	_ context.Context, params semanticapi.ConfigurationParams,
) ([]json.RawMessage, error) {
	// Return empty settings for each requested item so gopls
	// applies its default configuration (including analyzers).
	result := make([]json.RawMessage, len(params.Items))
	for i := range result {
		result[i] = json.RawMessage(`{}`)
	}
	return result, nil
}

func (c *testCallback) RegisterCapability(
	_ context.Context, _ semanticapi.RegistrationParams,
) error {
	return nil
}

func (c *testCallback) UnregisterCapability(
	_ context.Context, _ semanticapi.UnregistrationParams,
) error {
	return nil
}

func (c *testCallback) CodeLensRefresh(_ context.Context) error {
	return nil
}

func (c *testCallback) SemanticTokensRefresh(_ context.Context) error {
	return nil
}

func (c *testCallback) InlayHintRefresh(_ context.Context) error {
	return nil
}

func (c *testCallback) DiagnosticRefresh(_ context.Context) error {
	return nil
}
func (c *testCallback) HandleNotification(_ context.Context, _ string, _ json.RawMessage) error {
	return nil
}

func (c *testCallback) FileDidChange(_ string, _ int32, _, _ bool) {}

func (c *testCallback) InvalidateAllPending() {}

func (c *testCallback) WaitFileProcessed(_ context.Context, _ string) error {
	return nil
}

// localScheme implements schemeapi.FileSystem, schemeapi.Executor,
// and workspaceapi.Executor using the local OS for e2e testing.
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

func newTestScheme() *localScheme {
	return newTestSchemeRooted("")
}

// newTestSchemeRooted returns a scheme that resolves relative paths
// against root, so a single scheme instance can back both gopls and the
// workspace file system rooted at a real directory.
func newTestSchemeRooted(root string) *localScheme {
	return &localScheme{
		root:    root,
		procs:   make(map[workspaceapi.Pid]*os.Process),
		nextPid: 1,
	}
}

// resolve turns a workspace-relative path into an absolute OS path under
// root; absolute paths pass through unchanged.
func (s *localScheme) resolve(path string) string {
	if s.root == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(s.root, path)
}

func (s *localScheme) URI(path string) (workspaceapi.URI, error) {
	return workspaceapi.ParseURI("file://" + s.resolve(path))
}

func (s *localScheme) Create(filename string) (workspaceapi.File, error) {
	return os.Create(s.resolve(filename))
}

func (s *localScheme) Open(filename string) (workspaceapi.File, error) {
	return os.Open(s.resolve(filename))
}

func (s *localScheme) OpenFile(filename string, flag int, perm os.FileMode) (workspaceapi.File, error) {
	return os.OpenFile(s.resolve(filename), flag, perm)
}

func (s *localScheme) Stat(filename string) (os.FileInfo, error) {
	return os.Stat(s.resolve(filename))
}

func (s *localScheme) Rename(oldpath, newpath string) error {
	return os.Rename(s.resolve(oldpath), s.resolve(newpath))
}

func (s *localScheme) Remove(filename string) error {
	return os.Remove(s.resolve(filename))
}

func (s *localScheme) Join(elem ...string) string {
	return filepath.Join(elem...)
}

func (s *localScheme) TempFile(dir, prefix string) (workspaceapi.File, error) {
	return os.CreateTemp(s.resolve(dir), prefix)
}

func (s *localScheme) Lstat(filename string) (os.FileInfo, error) {
	return os.Lstat(s.resolve(filename))
}

func (s *localScheme) Symlink(oldname, newname string) error {
	return os.Symlink(s.resolve(oldname), s.resolve(newname))
}

func (s *localScheme) Readlink(link string) (string, error) {
	return os.Readlink(s.resolve(link))
}

func (s *localScheme) ReadDir(path string) ([]os.DirEntry, error) {
	return os.ReadDir(s.resolve(path))
}

func (s *localScheme) MkdirAll(filename string, perm os.FileMode) error {
	return os.MkdirAll(s.resolve(filename), perm)
}

func (s *localScheme) Start(ctx context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
	return s.StartCommand(ctx, cmd)
}

func (s *localScheme) StartCommand(ctx context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
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
		return fmt.Errorf("process %d not found", pid)
	}
	return proc.Signal(sig)
}

func (*localScheme) LookPath(context.Context, string, string) (string, error) {
	return "", errors.ErrUnsupported
}

func (s *localScheme) Close() error {
	return nil
}

// readyOnProgressCh returns an onProgress callback that closes
// the ready channel (via sync.Once) when a progress sequence ends.
func readyOnProgressCh(
	once *sync.Once, ready chan struct{},
) func(semanticapi.ProgressParams) {
	return func(p semanticapi.ProgressParams) {
		var v struct {
			Kind string `json:"kind"`
		}
		if json.Unmarshal(p.Value, &v) != nil {
			return
		}
		if v.Kind == "end" {
			once.Do(func() { close(ready) })
		}
	}
}

// waitGoplsReady waits for gopls to signal that loading is complete.
// It first waits on the ready channel (fed by progress/showMessage
// callbacks). If the signal doesn't arrive within a short window,
// it falls back to polling with Hover to handle simple workspaces
// where gopls may not send progress events.
func waitGoplsReady(
	t *testing.T,
	ready <-chan struct{},
	mgr *idelsp.Manager,
	fileURIs map[string]string,
) {
	t.Helper()

	// Fast path: wait for the progress signal.
	select {
	case <-ready:
		return
	case <-time.After(5 * time.Second):
	}

	// Fallback: poll with Hover until gopls responds.
	var fileURI string
	for _, u := range fileURIs {
		fileURI = u
		break
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	for {
		_, err := mgr.Hover(ctx, semanticapi.HoverParams{
			TextDocument: semanticapi.TextDocumentIdentifier{URI: fileURI},
			Position:     semanticapi.Position{Line: 0, Character: 8},
		})
		if err == nil {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("gopls not ready: %v", err)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// ---------------------------------------------------------------------------
// Mock types for handler-level e2e tests
// ---------------------------------------------------------------------------

// parseTestURI parses a file:// URI string into a workspaceapi.URI.
func parseTestURI(t *testing.T, fileURI string) workspaceapi.URI {
	t.Helper()
	u, err := workspaceapi.ParseURI(fileURI)
	require.NoError(t, err)
	return u
}

// newTestHandler creates a go handler wired to the given testEnv's LSP
// manager with mock editor and notifications. The executor is optional;
// pass nil for tests that don't exercise test execution.
func newTestHandler(
	t *testing.T, env *testEnv, executor workspaceapi.Executor,
) (textapi.CommandHandler, *mockEditor, *mockNotifications) {
	t.Helper()
	me := newMockEditor()
	mn := &mockNotifications{}
	wm := &fakeWM{autoConfirm: true}
	_, handler, err := newGoHandler(env.mgr, me, wm, mn, nil, executor, nil, nil, nil, nil)
	require.NoError(t, err)
	return handler, me, mn
}

// newTestHandlerWithEditorEvents is like newTestHandler but wires the
// mockEditor so that every CellEditor.Edit call fires EventTypeEdit on
// the Manager, simulating the real Rune editor where buffer modifications
// trigger edit events that the Manager converts to textDocument/didChange.
func newTestHandlerWithEditorEvents(
	t *testing.T, env *testEnv,
) (textapi.CommandHandler, *mockEditor, *mockNotifications) {
	t.Helper()
	me := newMockEditor()
	me.onCellEdit = func(
		uri workspaceapi.URI,
		start, end term.Coordinates,
		text string,
	) {
		env.mgr.Handle(context.Background(), textapi.Event{
			Type:    textapi.EventTypeEdit,
			URI:     uri,
			Content: text,
			Start:   start,
			End:     end,
		})
	}
	mn := &mockNotifications{}
	wm := &fakeWM{autoConfirm: true}
	_, handler, err := newGoHandler(env.mgr, me, wm, mn, nil, nil, nil, nil, nil, nil)
	require.NoError(t, err)
	return handler, me, mn
}

// goCmd creates a textapi.Command for the "go" command with the given
// subcommand, URI, and resource.
func goCmd(
	subcommand string, uri workspaceapi.URI, resource textapi.Handler,
) textapi.Command {
	return textapi.Command{
		Name:     "go",
		Args:     []string{subcommand},
		URI:      uri,
		Resource: resource,
	}
}

// goCmdAt creates a textapi.Command with cursor at (line, char).
func goCmdAt(
	subcommand string, uri workspaceapi.URI, resource textapi.Handler,
	line, char int,
) textapi.Command {
	cmd := goCmd(subcommand, uri, resource)
	cmd.Cursor.Content = term.Coordinates{X: char, Y: line}
	return cmd
}

// collectEditText concatenates the NewText from all edits.
func collectEditText(edits []mockEdit) string {
	var b strings.Builder
	for _, e := range edits {
		b.WriteString(e.NewText)
	}
	return b.String()
}

// capturedEditText concatenates the NewText from all workspace/applyEdit params.
func capturedEditText(captured []semanticapi.ApplyWorkspaceEditParams) string {
	var b strings.Builder
	for _, ae := range captured {
		for _, edits := range ae.Edit.Changes {
			for _, e := range edits {
				b.WriteString(e.NewText)
			}
		}
		for _, dc := range ae.Edit.DocumentChanges {
			if dc.TextDocumentEdit != nil {
				for _, e := range dc.TextDocumentEdit.Edits {
					b.WriteString(e.NewText)
				}
			}
		}
	}
	return b.String()
}

// mockNotification records a single notification.
type mockNotification struct {
	Level   browserapi.NotificationLevel
	Message string
}

// mockNotifications implements browserapi.Notifications for tests.
type mockNotifications struct {
	mu       sync.Mutex
	messages []mockNotification
}

var _ browserapi.Notifications = (*mockNotifications)(nil)

func (m *mockNotifications) Notify(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = append(m.messages, mockNotification{
		Level:   level,
		Message: fmt.Sprintf(msg, args...),
	})
	return "", nil
}

func (m *mockNotifications) NotifyOnce(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	return m.Notify(level, msg, args...)
}

func (m *mockNotifications) UpdateNotificationProgress(
	_, _ string, _, _ int64,
) error {
	return nil
}

func (m *mockNotifications) getMessages() []mockNotification {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]mockNotification{}, m.messages...)
}

func (m *mockNotifications) hasMessage(substr string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, msg := range m.messages {
		if strings.Contains(msg.Message, substr) {
			return true
		}
	}
	return false
}

// mockEdit records a single edit call.
type mockEdit struct {
	Start   term.Coordinates
	End     term.Coordinates
	NewText string
}

// mockCellEditor implements textapi.CellEditor for tests.
type mockCellEditor struct {
	mu     sync.Mutex
	edits  []mockEdit
	onEdit func(start, end term.Coordinates, text string)
}

var _ textapi.CellEditor = (*mockCellEditor)(nil)

func (e *mockCellEditor) Edit(
	_ context.Context, start, end term.Coordinates, str string,
) (term.Coordinates, term.Coordinates, string, error) {
	e.mu.Lock()
	e.edits = append(e.edits, mockEdit{Start: start, End: end, NewText: str})
	cb := e.onEdit
	e.mu.Unlock()
	if cb != nil {
		cb(start, end, str)
	}
	return start, end, "", nil
}

// mockEditor implements textapi.Editor for tests.
type mockEditor struct {
	mu          sync.Mutex
	handlers    map[string]textapi.Handler // URI string -> handler
	cellEditors map[textapi.Handler]*mockCellEditor
	evHandlers  []textapi.EventHandler
	// onCellEdit is called for every CellEditor.Edit call with the
	// handler's URI. Used to simulate the real editor firing EventTypeEdit.
	onCellEdit func(uri workspaceapi.URI, start, end term.Coordinates, text string)
}

var _ textapi.Editor = (*mockEditor)(nil)

func newMockEditor() *mockEditor {
	return &mockEditor{
		handlers:    make(map[string]textapi.Handler),
		cellEditors: make(map[textapi.Handler]*mockCellEditor),
	}
}

// Register associates a handler so that Editor(uri) can find it.
func (e *mockEditor) Register(h textapi.Handler) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.handlers[h.Resource().String()] = h
}

func (e *mockEditor) SubscribeEvents(
	_ []textapi.EventType, h textapi.EventHandler,
) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.evHandlers = append(e.evHandlers, h)
	return nil
}

// fireEvent sends an event to all subscribed event handlers.
func (e *mockEditor) fireEvent(ctx context.Context, ev textapi.Event) {
	e.mu.Lock()
	handlers := append([]textapi.EventHandler{}, e.evHandlers...)
	e.mu.Unlock()
	for _, h := range handlers {
		h.Handle(ctx, ev)
	}
}

// open delivers an EventTypeOpen for the file at path to all subscribed
// handlers, so tests can drive the langext.Initializer's discovery path.
func (e *mockEditor) open(t *testing.T, path string) {
	t.Helper()
	uri, err := workspaceapi.ParseURI("file://" + path)
	require.NoError(t, err)
	e.fireEvent(context.Background(), textapi.Event{Type: textapi.EventTypeOpen, URI: uri})
}

func (e *mockEditor) Editor(uri workspaceapi.URI) (textapi.Handler, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if h, ok := e.handlers[uri.String()]; ok {
		return h, nil
	}
	return nil, errors.New("editor not found")
}

func (e *mockEditor) SetLocationList(
	_ textapi.Handler, _ textapi.LocationPriority, _ string, _ textapi.LocationList,
) error {
	return nil
}

func (e *mockEditor) MoveToNextLocation(_ textapi.Handler, _ string) error {
	return nil
}

func (e *mockEditor) MoveToPrevLocation(_ textapi.Handler, _ string) error {
	return nil
}

func (e *mockEditor) Cursor(_ textapi.Handler) (term.Coordinates, error) {
	return term.Coordinates{}, nil
}

func (e *mockEditor) SetCursor(_ textapi.Handler, _ term.Coordinates) error {
	return nil
}

func (e *mockEditor) CellView(_ textapi.Handler) textapi.CellView {
	return nil
}

func (e *mockEditor) CellEditor(h textapi.Handler) textapi.CellEditor {
	e.mu.Lock()
	defer e.mu.Unlock()
	if ce, ok := e.cellEditors[h]; ok {
		return ce
	}
	ce := &mockCellEditor{}
	if e.onCellEdit != nil {
		uri := h.Resource()
		parent := e.onCellEdit
		ce.onEdit = func(start, end term.Coordinates, text string) {
			parent(uri, start, end, text)
		}
	}
	e.cellEditors[h] = ce
	return ce
}

func (e *mockEditor) SetDefaultAttributes(_ textapi.Handler, _ term.Attributes) error {
	return nil
}

// editsFor returns all edits recorded by the CellEditor for the given handler.
func (e *mockEditor) editsFor(h textapi.Handler) []mockEdit {
	e.mu.Lock()
	ce, ok := e.cellEditors[h]
	e.mu.Unlock()
	if !ok {
		return nil
	}
	ce.mu.Lock()
	defer ce.mu.Unlock()
	return append([]mockEdit{}, ce.edits...)
}

// bufferCellEditor implements textapi.CellEditor and maintains an in-memory
// text buffer, applying edits as a real editor would. This allows tests to
// verify the final document content after a sequence of edits.
type bufferCellEditor struct {
	lines []string
}

var _ textapi.CellEditor = (*bufferCellEditor)(nil)

func newBufferCellEditor(initial string) *bufferCellEditor {
	lines := strings.Split(initial, "\n")
	return &bufferCellEditor{lines: lines}
}

func (b *bufferCellEditor) Edit(
	_ context.Context, start, end term.Coordinates, text string,
) (term.Coordinates, term.Coordinates, string, error) {
	// Clamp coordinates to buffer bounds.
	if start.Y < 0 {
		start.Y = 0
	}
	if start.Y >= len(b.lines) {
		start.Y = len(b.lines) - 1
	}
	if start.X < 0 {
		start.X = 0
	}
	if start.X > len(b.lines[start.Y]) {
		start.X = len(b.lines[start.Y])
	}
	if end.Y < 0 {
		end.Y = 0
	}
	if end.Y >= len(b.lines) {
		end.Y = len(b.lines) - 1
	}
	if end.X < 0 {
		end.X = 0
	}
	if end.X > len(b.lines[end.Y]) {
		end.X = len(b.lines[end.Y])
	}

	// Build the content before and after the replaced range.
	before := b.lines[start.Y][:start.X]
	after := b.lines[end.Y][end.X:]

	// Combine before + new text + after and re-split into lines.
	combined := before + text + after
	newLines := strings.Split(combined, "\n")

	// Replace the affected lines.
	result := make([]string, 0, start.Y+len(newLines)+len(b.lines)-end.Y-1)
	result = append(result, b.lines[:start.Y]...)
	result = append(result, newLines...)
	result = append(result, b.lines[end.Y+1:]...)
	b.lines = result

	return start, end, "", nil
}

func (b *bufferCellEditor) String() string {
	return strings.Join(b.lines, "\n")
}

// stubResource implements textapi.Handler for tests.
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

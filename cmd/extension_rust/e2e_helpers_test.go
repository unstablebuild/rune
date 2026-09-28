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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/ide/idelsp"
	"unstable.build/rune/internal/ide/idelsp/lspcmd"
)

// findRustAnalyzer locates the rust-analyzer binary or skips the test.
// It mirrors resolveRustAnalyzer's search order: PATH first, then the
// bundled and cargo-installed locations.
func findRustAnalyzer(t *testing.T) string {
	t.Helper()
	if bin, err := exec.LookPath("rust-analyzer"); err == nil {
		return bin
	}
	for _, p := range []string{
		filepath.Join(os.Getenv("HOME"), ".rune", "bin", "rust-analyzer"),
		filepath.Join(os.Getenv("HOME"), ".cargo", "bin", "rust-analyzer"),
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Skip("rust-analyzer not found, skipping e2e test")
	return ""
}

// setupCargoWorkspace copies the testdata Cargo project into a fresh temp
// directory so each test gets an isolated, writable workspace, and
// returns its path.
func setupCargoWorkspace(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "rust-ext-e2e-")
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

// rustEnvE2E holds the initialized rust-analyzer manager and file URIs.
type rustEnvE2E struct {
	mgr      *idelsp.Manager
	cb       *raCallback
	dir      string
	fileURIs map[string]string
}

func (e *rustEnvE2E) capturedEdits() []semanticapi.ApplyWorkspaceEditParams {
	e.cb.mu.Lock()
	defer e.cb.mu.Unlock()
	return append([]semanticapi.ApplyWorkspaceEditParams{}, e.cb.appliedEdits...)
}

// initRustAnalyzer creates an idelsp.Manager, initializes rust-analyzer
// with the extension's rustInitializeParams, opens the given files, and
// waits for the server to finish loading. It captures workspace/applyEdit
// and mirrors applied edits back as editor events so the server's overlay
// stays in sync, matching the real IDE.
func initRustAnalyzer(t *testing.T, raBin string, openFiles []string) *rustEnvE2E {
	t.Helper()
	dir := setupCargoWorkspace(t)
	rootURI := "file://" + dir

	uri, err := workspaceapi.ParseURI(rootURI)
	require.NoError(t, err)

	scheme := newLocalScheme(dir)

	ready := make(chan struct{})
	var once sync.Once
	cb := &raCallback{
		onServerStatus: func(s serverStatus) {
			if s.Quiescent && s.Health == "ok" {
				once.Do(func() { close(ready) })
			}
		},
	}
	cfg := idelsp.Config{MaxRetries: 1, Callback: cb, WorkDoneProgress: true}

	mgr := idelsp.New(uri, scheme, scheme, &stubPkgManager{bin: raBin}, nil, nil, cfg)

	ctx := context.Background()
	params, err := rustInitializeParams(rootURI, raBin, sysrootFor(ctx), "info", true)
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
		trimmed := strings.TrimSuffix(string(content), "\n")
		wsURI, err := workspaceapi.ParseURI(fileURI)
		require.NoError(t, err)
		mgr.Handle(ctx, textapi.Event{
			Type:    textapi.EventTypeOpen,
			URI:     wsURI,
			Content: trimmed,
		})
	}

	env := &rustEnvE2E{mgr: mgr, cb: cb, dir: dir, fileURIs: fileURIs}
	select {
	case <-ready:
	case <-time.After(120 * time.Second):
		t.Fatal("rust-analyzer did not become quiescent")
	}
	t.Cleanup(func() { require.NoError(t, mgr.Close()) })

	env.simulateEditorEvents()
	return env
}

// simulateEditorEvents mirrors workspace/applyEdit changes back into the
// manager as EventTypeEdit so rust-analyzer's overlay tracks command-driven
// edits, as the real editor would.
func (e *rustEnvE2E) simulateEditorEvents() {
	e.cb.mu.Lock()
	e.cb.onApplyEdit = func(params semanticapi.ApplyWorkspaceEditParams) {
		for _, dc := range params.Edit.DocumentChanges {
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
		for fileURI, edits := range params.Edit.Changes {
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

// sysrootFor resolves the toolchain sysroot so rust-analyzer can find the
// standard library; an empty result lets the server fall back to its own
// discovery.
func sysrootFor(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "rustc", "--print", "sysroot").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// newTestActionHandler builds the rust action command handler wired to
// the env's manager, a mock editor, and mock notifications, mirroring the
// production wiring in extendWorkspaceWith.
func newTestActionHandler(
	t *testing.T, env *rustEnvE2E,
) (textapi.CommandHandler, *mockEditor, *mockNotifications) {
	t.Helper()
	handler, me, mn, _ := newTestActionHandlerOpener(t, env)
	return handler, me, mn
}

// newTestActionHandlerOpener is newTestActionHandler that also returns the
// mock resource opener, for navigation-command tests that assert which
// files were opened.
func newTestActionHandlerOpener(
	t *testing.T, env *rustEnvE2E,
) (textapi.CommandHandler, *mockEditor, *mockNotifications, *mockResourceOpener) {
	t.Helper()
	handler, me, mn, opener, _ := newTestActionHandlerParser(t, env)
	return handler, me, mn, opener
}

// newTestActionHandlerParser is newTestActionHandlerOpener that also
// returns the recording parser, for tests that assert the viewers and
// picker previews request syntax highlighting.
func newTestActionHandlerParser(
	t *testing.T, env *rustEnvE2E,
) (
	textapi.CommandHandler, *mockEditor, *mockNotifications,
	*mockResourceOpener, *recordingParser,
) {
	t.Helper()
	me := newMockEditor()
	me.onCellEdit = func(uri workspaceapi.URI, start, end term.Coordinates, text string) {
		env.mgr.Handle(context.Background(), textapi.Event{
			Type:    textapi.EventTypeEdit,
			URI:     uri,
			Content: text,
			Start:   start,
			End:     end,
		})
	}
	mn := &mockNotifications{}
	sel := lspcmd.NewSelectionTracker()
	require.NoError(t, me.SubscribeEvents(
		[]textapi.EventType{textapi.EventTypeSelection, textapi.EventTypeCursor}, sel))
	opener := newMockResourceOpener(me)
	parser := &recordingParser{}
	_, handler := newRustActionHandler(
		env.mgr, me, &fakeWM{}, mn, opener, sel, newDirExecutor(env.dir),
		realFS{root: env.dir}, parser, nil, env.dir, true, true)
	return handler, me, mn, opener, parser
}

// newTestActionHandlerExec is newTestActionHandler wired with a caller-
// supplied executor, so run tests can capture the reconstructed command
// instead of spawning a real cargo build.
func newTestActionHandlerExec(
	t *testing.T, env *rustEnvE2E, exec workspaceapi.Executor,
) (textapi.CommandHandler, *mockEditor, *mockNotifications) {
	t.Helper()
	me := newMockEditor()
	mn := &mockNotifications{}
	sel := lspcmd.NewSelectionTracker()
	require.NoError(t, me.SubscribeEvents(
		[]textapi.EventType{textapi.EventTypeSelection, textapi.EventTypeCursor}, sel))
	opener := newMockResourceOpener(me)
	_, handler := newRustActionHandler(
		env.mgr, me, &fakeWM{}, mn, opener, sel, exec, realFS{root: env.dir},
		&recordingParser{}, nil, env.dir, true, true)
	return handler, me, mn
}

// rustCmd builds a textapi.Command for the `rust` action command.
func rustCmd(sub string, uri workspaceapi.URI, resource textapi.Handler) textapi.Command {
	return textapi.Command{
		Name:     actionCmdName,
		Args:     []string{sub},
		URI:      uri,
		Resource: resource,
	}
}

// rustCmdAt is rustCmd with the cursor positioned at (line, char).
func rustCmdAt(
	sub string, uri workspaceapi.URI, resource textapi.Handler, line, char int,
) textapi.Command {
	cmd := rustCmd(sub, uri, resource)
	cmd.Cursor.Content = term.Coordinates{X: char, Y: line}
	return cmd
}

// collectEditText concatenates the NewText of every recorded editor edit.
func collectEditText(edits []mockEdit) string {
	var b strings.Builder
	for _, e := range edits {
		b.WriteString(e.NewText)
	}
	return b.String()
}

// capturedEditText concatenates the NewText of every workspace/applyEdit change.
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

func parseTestURI(t *testing.T, fileURI string) workspaceapi.URI {
	t.Helper()
	u, err := workspaceapi.ParseURI(fileURI)
	require.NoError(t, err)
	return u
}

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

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

//go:build e2e

package main

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/extension/langext"
	"unstable.build/rune/internal/ide/idelsp"
	"unstable.build/rune/internal/ide/idelsp/lspcmd"
)

func TestE2EZlsLoggingConfigReachesProcess(t *testing.T) {
	zlsBin := findZlsBin(t)
	zigBin := findZigBin(t)
	dir := setupZigWorkspace(t)
	rootURI := "file://" + dir
	uri, err := workspaceapi.ParseURI(rootURI)
	require.NoError(t, err)

	scheme := newLocalScheme(dir)
	mgr := idelsp.New(uri, scheme, scheme, &stubPkgManager{bin: zlsBin}, nil, nil,
		idelsp.Config{MaxRetries: 1, Callback: &zlsCallback{}})
	t.Cleanup(func() { require.NoError(t, mgr.Close()) })

	cfg := config.JSONFromMap(map[string]any{
		"lsp_path": zlsBin,
		"zig_path": zigBin,
		"debug": map[string]any{
			"log_level": "debug",
		},
	})
	err = initializeZigRoot(t.Context(), scheme, scheme, newFakeNotifications(), mgr, nil, cfg,
		langext.Root{Dir: dir, URI: rootURI})
	require.NoError(t, err)

	started, ok := scheme.startedProcess(zlsBin)
	require.True(t, ok, "zls process was not started")
	assert.Equal(t, []string{"--enable-stderr-logs", "--log-level", "debug"}, started.args)
	require.Eventually(t, func() bool {
		return started.stderr.String() != ""
	}, 5*time.Second, 20*time.Millisecond, "zls did not write debug logs to stderr")
}

func TestE2E_ZlsBringUp(t *testing.T) {
	t.Parallel()
	zlsBin := findZlsBin(t)
	zigBin := findZigBin(t)

	// Build-on-save is covered by TestE2E_BuildOnSaveDiagnostics; disable
	// it here so this instance never spawns a zig build it does not need.
	disabled := false
	env := initZls(t, zlsBin, zigBin, []string{"src/main.zig", "src/lib.zig"},
		buildOnSaveOptions{Enable: &disabled})
	ctx := context.Background()
	mainURI := env.fileURIs["src/main.zig"]
	pos := locateInFile(t, filepath.Join(env.dir, "src", "main.zig"), "lib.add(", "add(")

	hover, err := env.mgr.Hover(ctx, semanticapi.HoverParams{
		TextDocument: semanticapi.TextDocumentIdentifier{URI: mainURI},
		Position:     pos,
	})
	require.NoError(t, err)
	require.NotNil(t, hover, "hover over lib.add must answer")
	assert.Contains(t, hover.Contents.Value, "add",
		"hover should describe the add function")

	res, err := env.mgr.Definition(ctx, semanticapi.DefinitionParams{
		TextDocument: semanticapi.TextDocumentIdentifier{URI: mainURI},
		Position:     pos,
	})
	require.NoError(t, err)
	var target string
	switch {
	case res.Location != nil:
		target = res.Location.URI
	case len(res.Locations) > 0:
		target = res.Locations[0].URI
	case len(res.LocationLinks) > 0:
		target = res.LocationLinks[0].TargetURI
	}
	assert.Contains(t, target, "lib.zig",
		"definition of lib.add must resolve into lib.zig")

	// workspace/symbol only searches workspaces registered from the
	// initialize params' workspaceFolders; zls ignores rootUri alone and
	// would answer [] forever if the folder were dropped. zls indexes the
	// workspace asynchronously after initialize, so poll.
	require.Eventually(t, func() bool {
		syms, err := env.mgr.WorkspaceSymbol(ctx, semanticapi.WorkspaceSymbolParams{
			Query: "add",
		})
		if err != nil {
			return false
		}
		for _, s := range syms {
			if s.Name == "add" {
				return true
			}
		}
		return false
	}, 30*time.Second, 300*time.Millisecond,
		"workspace/symbol must find the fixture symbol, proving the "+
			"workspaceFolders registration reached zls")

	// zls only pushes ast-check diagnostics when the client advertises
	// textDocument.publishDiagnostics (its build-on-save publish path is
	// not gated, so that test cannot cover this). Build-on-save is
	// disabled here, making the capability-gated path the only possible
	// source of the diagnostic below.
	orig, err := os.ReadFile(filepath.Join(env.dir, "src", "main.zig"))
	require.NoError(t, err)
	require.NoError(t, env.mgr.DidChange(ctx, semanticapi.DidChangeTextDocumentParams{
		TextDocument: semanticapi.VersionedTextDocumentIdentifier{
			URI: mainURI, Version: 2,
		},
		ContentChanges: []semanticapi.TextDocumentContentChangeEvent{
			{Text: string(orig) + "\nconst broken = ;\n"},
		},
	}))
	require.Eventually(t, func() bool {
		diags, ok := env.cb.diagnosticsFor(mainURI)
		if !ok {
			return false
		}
		for _, d := range diags {
			if d.Severity == semanticapi.DiagnosticSeverityError {
				return true
			}
		}
		return false
	}, 30*time.Second, 300*time.Millisecond,
		"expected zls to push an ast-check diagnostic, proving the "+
			"publishDiagnostics capability reached zls")
}

func TestE2E_BuildOnSaveDiagnostics(t *testing.T) {
	t.Parallel()
	zlsBin := findZlsBin(t)
	zigBin := findZigBin(t)

	env := initZls(t, zlsBin, zigBin, []string{"src/main.zig"}, buildOnSaveOptions{})
	ctx := context.Background()
	mainURI := env.fileURIs["src/main.zig"]
	mainPath := filepath.Join(env.dir, "src", "main.zig")

	orig, err := os.ReadFile(mainPath)
	require.NoError(t, err)
	broken := strings.Replace(string(orig), `lib.add(2, 3)`, `lib.add(2, "three")`, 1)
	require.NotEqual(t, string(orig), broken, "fixture must contain the call to break")

	// Build-on-save compiles from disk, so persist the broken content
	// before mirroring it into the server's overlay and saving.
	require.NoError(t, os.WriteFile(mainPath, []byte(broken), 0o644))
	require.NoError(t, env.mgr.DidChange(ctx, semanticapi.DidChangeTextDocumentParams{
		TextDocument: semanticapi.VersionedTextDocumentIdentifier{
			URI: mainURI, Version: 2,
		},
		ContentChanges: []semanticapi.TextDocumentContentChangeEvent{
			{Text: broken},
		},
	}))
	require.NoError(t, env.mgr.DidSave(ctx, semanticapi.DidSaveTextDocumentParams{
		TextDocument: semanticapi.TextDocumentIdentifier{URI: mainURI},
		Text:         broken,
	}))

	// The first build-on-save run compiles the build runner, so give it
	// a generous window.
	require.Eventually(t, func() bool {
		diags, ok := env.cb.diagnosticsFor(mainURI)
		if !ok {
			return false
		}
		for _, d := range diags {
			if d.Severity == semanticapi.DiagnosticSeverityError &&
				strings.Contains(d.Message, "expected type") {
				return true
			}
		}
		return false
	}, 120*time.Second, 500*time.Millisecond,
		"expected zls build-on-save to push the cross-file type error")
}

// zigActionEnv wires the production `zig` code-action command tree to a
// live zls plus the fakes needed to observe what it does.
type zigActionEnv struct {
	*zigEnvE2E
	handler   textapi.CommandHandler
	editor    *fakeEditor
	wm        *fakeWM
	notify    *fakeNotifications
	resources map[string]*stubResource
}

// newZigActionEnv brings up zls on the fixture, opens files, and builds
// the real router from newZigActionHandler around it.
func newZigActionEnv(t *testing.T, files []string) *zigActionEnv {
	t.Helper()
	zlsBin := findZlsBin(t)
	zigBin := findZigBin(t)

	// Build-on-save is covered by TestE2E_BuildOnSaveDiagnostics; disable
	// it here so this instance never spawns a zig build it does not need.
	disabled := false
	env := initZls(t, zlsBin, zigBin, files, buildOnSaveOptions{Enable: &disabled})

	editor := &fakeEditor{}
	wm := &fakeWM{}
	notify := newFakeNotifications()
	sel := lspcmd.NewSelectionTracker()
	require.NoError(t, editor.SubscribeEvents(
		[]textapi.EventType{textapi.EventTypeSelection, textapi.EventTypeCursor}, sel))
	_, handler := newZigActionHandler(env.mgr, editor, wm, notify, sel)

	resources := make(map[string]*stubResource, len(files))
	for _, name := range files {
		uri, err := workspaceapi.ParseURI(env.fileURIs[name])
		require.NoError(t, err)
		res := &stubResource{uri: uri}
		editor.register(res)
		content, err := os.ReadFile(filepath.Join(env.dir, name))
		require.NoError(t, err)
		editor.seed(res, string(content))
		resources[name] = res
	}
	return &zigActionEnv{
		zigEnvE2E: env, handler: handler, editor: editor,
		wm: wm, notify: notify, resources: resources,
	}
}

// command builds the command-prompt invocation of `zig <sub>` against
// the opened file, with the cursor at the given position.
func (a *zigActionEnv) command(sub, file string, cursor term.Coordinates) textapi.Command {
	res := a.resources[file]
	cmd := textapi.Command{
		Name:     zigActionCmdName,
		Args:     []string{sub},
		URI:      res.uri,
		Resource: res,
	}
	cmd.Cursor.Content = cursor
	return cmd
}

// waitCodeActions blocks until zls offers a code action for the file.
// Its ast-check runs asynchronously after the open, so a request issued
// too early legitimately answers with nothing.
func (a *zigActionEnv) waitCodeActions(t *testing.T, file string) {
	t.Helper()
	require.Eventually(t, func() bool {
		res, err := a.mgr.CodeAction(context.Background(), semanticapi.CodeActionParams{
			TextDocument: semanticapi.TextDocumentIdentifier{URI: a.fileURIs[file]},
			Context: semanticapi.CodeActionContext{
				Diagnostics: []semanticapi.Diagnostic{},
				TriggerKind: semanticapi.CodeActionTriggerKindInvoked,
			},
		})
		return err == nil && len(res) > 0
	}, 30*time.Second, 300*time.Millisecond,
		"zls never offered a code action for %s", file)
}

// waitPicker blocks until the command floats its picker.
func (a *zigActionEnv) waitPicker(t *testing.T) lspcmd.CodeActionPicker {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if picker, ok := a.wm.floated().(lspcmd.CodeActionPicker); ok {
			return picker
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the code-action picker")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// sync tells zls what the buffer holds after an applied action. The IDE
// does this through the editor event pipeline, which the fake editor is
// not wired into.
func (a *zigActionEnv) sync(t *testing.T, file string) {
	t.Helper()
	require.NoError(t, a.mgr.DidChange(context.Background(),
		semanticapi.DidChangeTextDocumentParams{
			TextDocument: semanticapi.VersionedTextDocumentIdentifier{
				URI: a.fileURIs[file], Version: 2,
			},
			ContentChanges: []semanticapi.TextDocumentContentChangeEvent{
				{Text: a.editor.text(a.resources[file])},
			},
		}))
}

// run dispatches a subcommand that applies a sole action itself, and
// pins that no picker was floated on the way.
func (a *zigActionEnv) run(t *testing.T, cmd textapi.Command) {
	t.Helper()
	a.wm.reset()
	require.NoError(t, a.handler.HandleCommand(context.Background(), cmd))
	require.Nil(t, a.wm.floated(), "the action must apply without a picker")
}

// runAction dispatches the command and applies the action titled
// wantTitle. Every action is offered through the picker, which blocks
// HandleCommand on the user's choice, so this drives it from the
// background.
func (a *zigActionEnv) runAction(t *testing.T, cmd textapi.Command, wantTitle string) {
	t.Helper()
	a.wm.reset()
	done := make(chan error, 1)
	go func() { done <- a.handler.HandleCommand(context.Background(), cmd) }()

	selectPickerTitle(t, a.waitPicker(t), wantTitle)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(20 * time.Second):
		t.Fatalf("timed out applying the picked action %q", wantTitle)
	}
}

// selectPickerTitle navigates the picker to the action titled wantTitle
// and confirms it with Enter, failing if no such action is offered.
func selectPickerTitle(t *testing.T, picker lspcmd.CodeActionPicker, wantTitle string) {
	t.Helper()
	actions := picker.Actions()
	idx := -1
	for i, action := range actions {
		if action.Title == wantTitle {
			idx = i
			break
		}
	}
	if idx < 0 {
		titles := make([]string, len(actions))
		for i, action := range actions {
			titles[i] = action.Title
		}
		t.Fatalf("action %q not offered; got %v", wantTitle, titles)
	}
	for range idx {
		picker.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowDown})
	}
	picker.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
}

func TestE2E_ZigCodeActions(t *testing.T) {
	t.Parallel()

	files := []string{
		"src/main.zig", "src/lib.zig", "src/unused.zig",
		"src/imports.zig", "src/organize.zig",
	}

	t.Run("quickfix picks one of several fixes", func(t *testing.T) {
		t.Parallel()
		a := newZigActionEnv(t, files)
		a.waitCodeActions(t, "src/unused.zig")

		a.runAction(t, a.command("quickfix", "src/unused.zig", term.Coordinates{}), "discard value")

		edits := a.editor.editsFor(a.resources["src/unused.zig"])
		require.NotEmpty(t, edits, "the chosen quickfix must reach the editor")
		assert.Contains(t, edits[0].text, "_ = leftover")
	})

	// `fix-all` names the one thing it does, so a sole result runs
	// straight away rather than floating a picker that would only echo
	// the subcommand the user just typed.
	t.Run("a lone action is applied without a picker", func(t *testing.T) {
		t.Parallel()
		a := newZigActionEnv(t, files)
		a.waitCodeActions(t, "src/unused.zig")

		a.run(t, a.command("fix-all", "src/unused.zig", term.Coordinates{}))

		assert.NotEmpty(t, a.editor.editsFor(a.resources["src/unused.zig"]))
	})

	// Dismissing the picker must leave the buffer untouched. `quickfix`
	// covers a whole family, so it always asks.
	t.Run("escaping the picker applies nothing", func(t *testing.T) {
		t.Parallel()
		a := newZigActionEnv(t, files)
		a.waitCodeActions(t, "src/unused.zig")

		cmd := a.command("quickfix", "src/unused.zig", term.Coordinates{})
		done := make(chan error, 1)
		go func() { done <- a.handler.HandleCommand(context.Background(), cmd) }()

		picker := a.waitPicker(t)
		picker.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
		require.NoError(t, <-done)
		assert.Empty(t, a.editor.editsFor(a.resources["src/unused.zig"]))
	})

	t.Run("organize-imports reorders the @import declarations", func(t *testing.T) {
		t.Parallel()
		a := newZigActionEnv(t, files)
		a.waitCodeActions(t, "src/imports.zig")

		a.run(t, a.command("organize-imports", "src/imports.zig", term.Coordinates{}))

		edits := a.editor.editsFor(a.resources["src/imports.zig"])
		require.NotEmpty(t, edits)
		assert.Contains(t, editText(edits), "std")
	})

	// zls emits "organize @import" as one insert plus several whole-line
	// deletions, and the insert shares its start position with the first
	// deletion. Applying that set in array order corrupts the file, so
	// pin the applied buffer against an independent reference that
	// resolves every edit against the original document, which is the
	// semantics the LSP spec mandates.
	t.Run("multi-edit actions land exactly as the server specified", func(t *testing.T) {
		t.Parallel()
		a := newZigActionEnv(t, files)
		a.waitCodeActions(t, "src/organize.zig")

		src, err := os.ReadFile(filepath.Join(a.dir, "src", "organize.zig"))
		require.NoError(t, err)
		edits := a.organizeEdits(t, "src/organize.zig")
		require.Greater(t, len(edits), 1, "fixture must produce a multi-edit action")
		want := applyEditsToText(string(src), edits)
		require.NotEqual(t, string(src), want, "fixture must actually be reorganized")

		a.run(t, a.command("organize-imports", "src/organize.zig", term.Coordinates{}))

		assert.Equal(t, want, a.editor.text(a.resources["src/organize.zig"]))
	})

	// zls answers source.organizeImports with a full rewrite of the
	// import block on every invocation, even once the imports are in the
	// order it wants, so a second run must leave the buffer alone rather
	// than re-dirty it with an edit set that changes nothing.
	t.Run("a second organize-imports applies nothing", func(t *testing.T) {
		t.Parallel()
		a := newZigActionEnv(t, files)
		a.waitCodeActions(t, "src/organize.zig")
		res := a.resources["src/organize.zig"]

		a.run(t, a.command("organize-imports", "src/organize.zig", term.Coordinates{}))
		organized := a.editor.text(res)
		applied := len(a.editor.editsFor(res))
		require.NotZero(t, applied)
		a.sync(t, "src/organize.zig")

		a.run(t, a.command("organize-imports", "src/organize.zig", term.Coordinates{}))

		assert.Equal(t, organized, a.editor.text(res))
		assert.Len(t, a.editor.editsFor(res), applied,
			"the second run must not touch the buffer")
	})

	// An empty Only filter must reach zls, which is what lets the picker
	// offer actions of more than one kind at once.
	t.Run("list spans several kinds", func(t *testing.T) {
		t.Parallel()
		a := newZigActionEnv(t, files)
		a.waitCodeActions(t, "src/unused.zig")

		a.runAction(t, a.command("list", "src/unused.zig", term.Coordinates{}), "apply fixall")

		picker, ok := a.wm.floated().(lspcmd.CodeActionPicker)
		require.True(t, ok)
		kinds := make(map[semanticapi.CodeActionKind]bool)
		for _, action := range picker.Actions() {
			kinds[action.Kind] = true
		}
		assert.Greater(t, len(kinds), 1, "list must not pre-filter by kind")
	})

	// zls answers no code action at all while a file fails to parse, so
	// the hint must name the syntax error rather than claim the file is
	// clean.
	t.Run("syntax error reports the hint and floats nothing", func(t *testing.T) {
		t.Parallel()
		a := newZigActionEnv(t, files)
		a.waitCodeActions(t, "src/unused.zig")

		orig, err := os.ReadFile(filepath.Join(a.dir, "src", "unused.zig"))
		require.NoError(t, err)
		require.NoError(t, a.mgr.DidChange(context.Background(), semanticapi.DidChangeTextDocumentParams{
			TextDocument: semanticapi.VersionedTextDocumentIdentifier{
				URI: a.fileURIs["src/unused.zig"], Version: 2,
			},
			ContentChanges: []semanticapi.TextDocumentContentChangeEvent{
				{Text: string(orig) + "\nconst broken = ;\n"},
			},
		}))

		cmd := a.command("quickfix", "src/unused.zig", term.Coordinates{})
		require.NoError(t, a.handler.HandleCommand(context.Background(), cmd))

		assert.Nil(t, a.wm.floated())
		assert.True(t, a.notify.hasMessage("syntax error"),
			"expected the syntax-error hint; got %v", a.notify.notifMessages())
	})

	t.Run("clean file reports the hint", func(t *testing.T) {
		t.Parallel()
		a := newZigActionEnv(t, files)

		cmd := a.command("quickfix", "src/lib.zig", term.Coordinates{})
		require.NoError(t, a.handler.HandleCommand(context.Background(), cmd))

		assert.Nil(t, a.wm.floated())
		assert.True(t, a.notify.hasMessage("No quick fixes available"),
			"expected the no-quickfix hint; got %v", a.notify.notifMessages())
	})

	// The refactor route is the only cursor-scoped one: zls offers the
	// multiline conversion only when the cursor sits inside a literal.
	t.Run("refactor converts the string literal at the cursor", func(t *testing.T) {
		t.Parallel()
		a := newZigActionEnv(t, files)

		pos := locateInFile(t, filepath.Join(a.dir, "src", "main.zig"), `"{d}`, `"{d}`)
		cursor := term.Coordinates{X: int(pos.Character) + 2, Y: int(pos.Line)}
		a.runAction(t, a.command("refactor", "src/main.zig", cursor),
			"convert to a multiline string literal")

		assert.NotEmpty(t, a.editor.editsFor(a.resources["src/main.zig"]),
			"expected the multiline-literal conversion; got %v", a.notify.notifMessages())
	})
}

// editText concatenates the replacement text of every recorded edit.
func editText(edits []fakeEdit) string {
	var sb strings.Builder
	for _, e := range edits {
		sb.WriteString(e.text)
	}
	return sb.String()
}

// organizeEdits asks zls directly for the text edits of its
// "organize @import" action, so a test can compute the expected result
// independently of the command under test.
func (a *zigActionEnv) organizeEdits(t *testing.T, file string) []semanticapi.TextEdit {
	t.Helper()
	res, err := a.mgr.CodeAction(context.Background(), semanticapi.CodeActionParams{
		TextDocument: semanticapi.TextDocumentIdentifier{URI: a.fileURIs[file]},
		Context: semanticapi.CodeActionContext{
			Diagnostics: []semanticapi.Diagnostic{},
			Only:        []semanticapi.CodeActionKind{"source.organizeImports"},
			TriggerKind: semanticapi.CodeActionTriggerKindInvoked,
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, res)
	require.NotNil(t, res[0].CodeAction)
	require.NotNil(t, res[0].CodeAction.Edit)
	for _, edits := range res[0].CodeAction.Edit.Changes {
		return edits
	}
	t.Fatal("organize action carried no changes")
	return nil
}

// applyEditsToText resolves every edit against the original text and
// splices the results together, independent of the order the server
// listed them in.
func applyEditsToText(src string, edits []semanticapi.TextEdit) string {
	type span struct {
		start, end int
		text       string
	}
	spans := make([]span, len(edits))
	for i, e := range edits {
		spans[i] = span{
			start: textOffset(src, int(e.Range.Start.Line), int(e.Range.Start.Character)),
			end:   textOffset(src, int(e.Range.End.Line), int(e.Range.End.Character)),
			text:  e.NewText,
		}
	}
	sort.SliceStable(spans, func(i, j int) bool { return spans[i].start < spans[j].start })

	var sb strings.Builder
	prev := 0
	for _, s := range spans {
		if s.start > prev {
			sb.WriteString(src[prev:s.start])
		}
		sb.WriteString(s.text)
		prev = max(prev, s.end)
	}
	sb.WriteString(src[prev:])
	return sb.String()
}

// textOffset converts a 0-based line/character position to a byte offset.
func textOffset(text string, line, char int) int {
	lines := strings.SplitAfter(text, "\n")
	off := 0
	for i := 0; i < line && i < len(lines); i++ {
		off += len(lines[i])
	}
	return min(off+char, len(text))
}

func TestE2E_ZigHandlerVersion(t *testing.T) {
	zigBin := findZigBin(t)
	dir := t.TempDir()
	_, h := newZigHandler(newDirExecutor(dir), newFakeNotifications(), dir,
		func(context.Context) string { return zigBin }, nil)
	it, err := h.HandleCommand(context.Background(),
		repl.Command{Name: "zig", Args: []string{"version"}}, repl.NopProgressWriter())
	require.NoError(t, err)
	out, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	require.Len(t, out, 1)
}

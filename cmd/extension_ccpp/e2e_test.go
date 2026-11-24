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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/extension/langext/langexttest"
	"unstable.build/rune/internal/ide/idelsp"
	"unstable.build/rune/internal/ide/idelsp/lspcmd"
)

const (
	e2eWait = 60 * time.Second
	e2eTick = 200 * time.Millisecond
)

// e2eEnv runs the whole extension against a live clangd behind a real
// idelsp.Manager, with fakes where the IDE would be.
type e2eEnv struct {
	dir       string
	clangd    string
	mgr       *idelsp.Manager
	cb        *ccppCallback
	scheme    *localScheme
	editor    *fakeEditor
	wm        *fakeWM
	notify    *fakeNotifications
	opener    *fakeOpener
	repl      *ccppHandler
	actions   map[string]textapi.CommandHandler
	resources map[string]*stubResource
	uris      map[string]string
}

// newE2EEnv brings the extension up on a copy of the fixture with the
// given extra config; lsp_path always names the clangd found on the host
// so bring-up never depends on a Rune package.
func newE2EEnv(t *testing.T, extra map[string]any) *e2eEnv {
	return newE2EEnvOn(t, "testdata/e2e", extra, &fakePrompter{}, &langexttest.Installer{})
}

// newE2EEnvOn is newE2EEnv on a copy of the fixture at src, answering
// prompts with prompter and finding the ccpp package's tools with inst.
func newE2EEnvOn(
	t *testing.T, src string, extra map[string]any, prompter *fakePrompter, inst *langexttest.Installer,
) *e2eEnv {
	t.Helper()
	clangdBin := findClangdBin(t)
	dir := setupWorkspace(t, src)
	rootURI, err := workspaceapi.ParseURI("file://" + dir)
	require.NoError(t, err)

	scheme := newLocalScheme(dir)
	cb := &ccppCallback{}
	mgr := idelsp.New(rootURI, scheme, scheme, &stubPkgManager{bin: clangdBin}, nil, nil,
		idelsp.Config{MaxRetries: 1, Callback: cb, WorkDoneProgress: true})
	t.Cleanup(func() { require.NoError(t, mgr.Close()) })

	settings := map[string]any{"lsp_path": clangdBin}
	for k, v := range extra {
		settings[k] = v
	}
	env := &e2eEnv{
		dir: dir, clangd: clangdBin, mgr: mgr, cb: cb, scheme: scheme,
		editor: &fakeEditor{}, wm: &fakeWM{}, notify: newFakeNotifications(), opener: &fakeOpener{},
		actions:   map[string]textapi.CommandHandler{},
		resources: map[string]*stubResource{},
		uris:      map[string]string{},
	}
	ext := &ccppExtension{}
	err = ext.extendWorkspaceWith(context.Background(),
		scheme, scheme, env.notify, mgr, env.editor, env.wm, prompter, env.opener,
		storagestub.NewInMemoryService(), inst, config.JSONFromMap(settings),
		func(_ textapi.CommandManual, h textapi.REPLHandler) error {
			env.repl = h.(*ccppHandler)
			return nil
		},
		func(m textapi.CommandManual, h textapi.CommandHandler) error {
			env.actions[m.Name] = h
			return nil
		})
	require.NoError(t, err)
	return env
}

// open opens the fixture files the way the IDE does: the first open
// starts clangd at the workspace root, then every file is sent to the
// manager with the language id the IDE derives from its extension.
func (e *e2eEnv) open(t *testing.T, files ...string) {
	t.Helper()
	for _, name := range files {
		path := filepath.Join(e.dir, name)
		uri, err := workspaceapi.ParseURI("file://" + path)
		require.NoError(t, err)
		res := &stubResource{uri: uri}
		e.editor.register(res)
		content, err := os.ReadFile(path)
		require.NoError(t, err)
		e.editor.seed(res, string(content))
		e.resources[name] = res
		e.uris[name] = uri.String()

		e.editor.open(t, path)
		e.editor.focus(t, path)
		e.waitRunning(t)
		langID := "cpp"
		if strings.HasSuffix(name, ".c") {
			langID = "c"
		}
		require.NoError(t, e.mgr.DidOpen(context.Background(), semanticapi.DidOpenTextDocumentParams{
			TextDocument: semanticapi.TextDocumentItem{
				URI: uri.String(), LanguageID: langID, Version: 0, Text: string(content),
			},
		}))
	}
}

// waitRunning blocks until the extension reports clangd up.
func (e *e2eEnv) waitRunning(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool {
		phase, _, err := e.repl.state.snapshot()
		require.NoError(t, err, "clangd bring-up failed")
		return phase == "running"
	}, e2eWait, e2eTick, "clangd never came up")
}

// waitReady polls clangd until it answers hover in main.cpp, so
// assertions do not race its startup.
func (e *e2eEnv) waitReady(t *testing.T) {
	t.Helper()
	pos := locateInFile(t, filepath.Join(e.dir, "src", "main.cpp"), "shape.area()", "area")
	require.Eventually(t, func() bool {
		hover, err := e.mgr.Hover(context.Background(), semanticapi.HoverParams{
			TextDocument: semanticapi.TextDocumentIdentifier{URI: e.uris["src/main.cpp"]},
			Position:     pos,
		})
		return err == nil && hover != nil && strings.Contains(hover.Contents.Value, "area")
	}, e2eWait, e2eTick, "clangd never answered hover in main.cpp")
}

// waitDiagnostic blocks until clangd publishes a diagnostic for file
// that accept passes, and returns it.
func (e *e2eEnv) waitDiagnostic(
	t *testing.T, file string, accept func(semanticapi.Diagnostic) bool,
) semanticapi.Diagnostic {
	t.Helper()
	var found semanticapi.Diagnostic
	require.Eventually(t, func() bool {
		diags, ok := e.cb.diagnosticsFor(e.uris[file])
		if !ok {
			return false
		}
		for _, d := range diags {
			if accept(d) {
				found = d
				return true
			}
		}
		return false
	}, e2eWait, e2eTick, "clangd never published the expected diagnostic for %s", file)
	return found
}

// command builds the command-prompt invocation of `<name> <sub>` against
// an opened file with the cursor at pos.
func (e *e2eEnv) command(name, sub, file string, pos semanticapi.Position) textapi.Command {
	res := e.resources[file]
	cmd := textapi.Command{Name: name, Args: []string{sub}, URI: res.uri, Resource: res}
	cmd.Cursor.Content = lspcmd.PosToCoord(pos)
	return cmd
}

// select tells the selection tracker the user selected [start, end) of
// file, as the editor would.
func (e *e2eEnv) selectRange(t *testing.T, file string, start, end semanticapi.Position) {
	t.Helper()
	res := e.resources[file]
	e.editor.mu.Lock()
	handlers := append([]textapi.EventHandler(nil), e.editor.handlers...)
	e.editor.mu.Unlock()
	for _, h := range handlers {
		h.Handle(context.Background(), textapi.Event{
			Type: textapi.EventTypeSelection, URI: res.uri, Resource: res,
			Start: lspcmd.PosToCoord(start), End: lspcmd.PosToCoord(end),
		})
	}
}

// run dispatches a subcommand that applies a lone action itself and pins
// that no picker was floated on the way.
func (e *e2eEnv) run(t *testing.T, cmd textapi.Command) {
	t.Helper()
	e.wm.reset()
	require.NoError(t, e.actions[cmd.Name].HandleCommand(context.Background(), cmd))
	require.Nil(t, e.wm.floated(), "the action must apply without a picker")
}

// pickFirst dispatches a command that floats the code-action picker and
// confirms its first entry, returning that entry's title.
func (e *e2eEnv) pickFirst(t *testing.T, cmd textapi.Command) string {
	t.Helper()
	e.wm.reset()
	done := make(chan error, 1)
	go func() { done <- e.actions[cmd.Name].HandleCommand(context.Background(), cmd) }()
	var picker lspcmd.CodeActionPicker
	require.Eventually(t, func() bool {
		p, ok := e.wm.floated().(lspcmd.CodeActionPicker)
		picker = p
		return ok
	}, e2eWait, 20*time.Millisecond, "the code-action picker never floated")
	actions := picker.Actions()
	require.NotEmpty(t, actions)
	picker.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(e2eWait):
		t.Fatal("timed out applying the picked action")
	}
	return actions[0].Title
}

// servers returns the clangd processes started to serve the workspace,
// oldest first, leaving out the `clangd --version` runs of `ccpp status`.
func (e *e2eEnv) servers() []startedProcess {
	var out []startedProcess
	for _, p := range e.scheme.startedProcesses(e.clangd) {
		if len(p.args) > 0 && p.args[0] == "--background-index" {
			out = append(out, p)
		}
	}
	return out
}

// appliedText returns the new text of every edit clangd asked the client
// to apply, joined.
func (e *e2eEnv) appliedText() string {
	var b strings.Builder
	for _, p := range e.cb.appliedEdits() {
		for _, edits := range p.Edit.Changes {
			for _, ed := range edits {
				b.WriteString(ed.NewText)
			}
		}
		for _, dc := range p.Edit.DocumentChanges {
			if dc.TextDocumentEdit == nil {
				continue
			}
			for _, ed := range dc.TextDocumentEdit.Edits {
				b.WriteString(ed.NewText)
			}
			for _, ed := range dc.TextDocumentEdit.AnnotatedEdits {
				b.WriteString(ed.NewText)
			}
		}
	}
	return b.String()
}

func TestE2E_ClangdConfigReachesProcess(t *testing.T) {
	env := newE2EEnv(t, map[string]any{
		"args":  []any{"--pch-storage=memory"},
		"debug": map[string]any{"log_level": "verbose"},
	})
	env.open(t, "src/util.c")

	started := env.servers()
	require.Len(t, started, 1, "one clangd serves the workspace")
	assert.Equal(t, []string{
		"--background-index", "--clang-tidy", "--header-insertion=iwyu",
		"--log=verbose", "--pch-storage=memory",
	}, started[0].args)
	require.Eventually(t, func() bool {
		return strings.Contains(started[0].stderr.String(), "<-- initialize")
	}, e2eWait, e2eTick, "clangd did not write verbose logs to stderr")
}

func TestE2E_CcppBringUpServesCAndCpp(t *testing.T) {
	env := newE2EEnv(t, nil)
	env.open(t, "src/main.cpp", "src/util.c", "src/util.h", "src/shape.cpp")
	env.waitReady(t)
	ctx := context.Background()

	// One clangd, started by the first open, serves every file of the
	// workspace whatever its language id.
	assert.Len(t, env.servers(), 1)

	hover, err := env.mgr.Hover(ctx, semanticapi.HoverParams{
		TextDocument: semanticapi.TextDocumentIdentifier{URI: env.uris["src/util.c"]},
		Position:     locateInFile(t, filepath.Join(env.dir, "src", "util.c"), "int util_add(", "util_add"),
	})
	require.NoError(t, err)
	require.NotNil(t, hover, "hover in the C file must answer")
	assert.Contains(t, hover.Contents.Value, "util_add")

	res, err := env.mgr.Definition(ctx, semanticapi.DefinitionParams{
		TextDocument: semanticapi.TextDocumentIdentifier{URI: env.uris["src/main.cpp"]},
		Position:     locateInFile(t, filepath.Join(env.dir, "src", "main.cpp"), "geo::Shape shape", "Shape"),
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
	assert.Contains(t, target, "shape.hpp", "definition of geo::Shape must resolve into the header")

	// The compilation database beside the sources is what clangd found:
	// util.c compiles with its -std=c11, so the clang-tidy and compiler
	// diagnostics come back clean rather than complaining about flags.
	out, err := runCcpp(t, env.repl, "status")
	require.NoError(t, err)
	assert.Contains(t, out, "running as "+env.clangd)
	assert.Contains(t, out, "src/shape.cpp")
	assert.Contains(t, out, "Project: . (CMakeLists.txt)")
	assert.Contains(t, out, "build/compile_commands.json (found by clangd)")

	out, err = runCcpp(t, env.repl, "version")
	require.NoError(t, err)
	assert.Contains(t, out, "clangd version")
}

func TestE2E_CcppSwitchSourceHeader(t *testing.T) {
	env := newE2EEnv(t, nil)
	env.open(t, "src/main.cpp", "src/shape.cpp", "src/util.h")
	env.waitReady(t)

	cmd := env.command(cppActionCmdName, "switch", "src/shape.cpp", semanticapi.Position{})
	require.NoError(t, env.actions[cppActionCmdName].HandleCommand(context.Background(), cmd))
	assert.Equal(t, []string{filepath.Join(env.dir, "src", "shape.hpp")}, env.opener.openedPaths())
	shown, ok := env.wm.shown().(*stubResource)
	require.True(t, ok)
	assert.Equal(t, filepath.Join(env.dir, "src", "shape.hpp"), shown.uri.Path())

	cmd = env.command(cActionCmdName, "switch", "src/util.h", semanticapi.Position{})
	require.NoError(t, env.actions[cActionCmdName].HandleCommand(context.Background(), cmd))
	assert.Equal(t, filepath.Join(env.dir, "src", "util.c"), env.opener.openedPaths()[1])

	// main.cpp has no header of its own.
	cmd = env.command(cppActionCmdName, "switch", "src/main.cpp", semanticapi.Position{})
	require.NoError(t, env.actions[cppActionCmdName].HandleCommand(context.Background(), cmd))
	assert.Len(t, env.opener.openedPaths(), 2)
	assert.True(t, env.notify.hasMessage("No source or header pairs with main.cpp"))
}

func TestE2E_CcppASTAndSymbolInfo(t *testing.T) {
	env := newE2EEnv(t, nil)
	env.open(t, "src/main.cpp", "src/util.c")
	env.waitReady(t)

	// With no selection clangd answers the smallest node at the cursor;
	// a selection covering `a + b` gets the whole expression.
	pos := locateInFile(t, filepath.Join(env.dir, "src", "util.c"), "return a + b;", "a + b")
	env.selectRange(t, "src/util.c", pos, semanticapi.Position{Line: pos.Line, Character: pos.Character + 5})
	env.wm.reset()
	cmd := env.command(cActionCmdName, "ast", "src/util.c", pos)
	require.NoError(t, env.actions[cActionCmdName].HandleCommand(context.Background(), cmd))
	frame := renderFloating(t, env.wm.floated(), 100, 20)
	assert.Contains(t, frame, "BinaryOperator +")
	assert.Contains(t, frame, "DeclRef a")
	assert.Contains(t, frame, "DeclRef b")

	pos = locateInFile(t, filepath.Join(env.dir, "src", "main.cpp"), "auto n = shape.area()", "area")
	env.wm.reset()
	cmd = env.command(cppActionCmdName, "symbol-info", "src/main.cpp", pos)
	require.NoError(t, env.actions[cppActionCmdName].HandleCommand(context.Background(), cmd))
	frame = renderFloating(t, env.wm.floated(), 100, 20)
	assert.Contains(t, frame, "Name: area")
	assert.Contains(t, frame, "Container: geo::Shape::")
	assert.Contains(t, frame, "USR: c:@N@geo@S@Shape@F@area#1")

	env.wm.reset()
	cmd = env.command(cppActionCmdName, "memory-usage", "src/main.cpp", pos)
	require.NoError(t, env.actions[cppActionCmdName].HandleCommand(context.Background(), cmd))
	frame = renderFloating(t, env.wm.floated(), 100, 30)
	assert.Contains(t, frame, "clangd ")
	assert.Contains(t, frame, "clangd_server")
}

func TestE2E_CcppTweaks(t *testing.T) {
	env := newE2EEnv(t, nil)
	env.open(t, "src/main.cpp", "src/util.c")
	env.waitReady(t)
	mainPath := filepath.Join(env.dir, "src", "main.cpp")

	// The subcommand names the tweak, so a lone match applies without a
	// picker; clangd then asks the client to apply the resulting edit.
	t.Run("expand-auto", func(t *testing.T) {
		pos := locateInFile(t, mainPath, "auto n = shape.area()", "auto")
		env.run(t, env.command(cppActionCmdName, "expand-auto", "src/main.cpp", pos))
		require.Eventually(t, func() bool {
			return strings.Contains(env.appliedText(), "double")
		}, e2eWait, e2eTick, "clangd must ask to replace auto with double")
	})

	t.Run("populate-switch", func(t *testing.T) {
		before := len(env.cb.appliedEdits())
		pos := locateInFile(t, mainPath, "switch (c)", "switch")
		env.run(t, env.command(cppActionCmdName, "populate-switch", "src/main.cpp", pos))
		require.Eventually(t, func() bool {
			return len(env.cb.appliedEdits()) > before && strings.Contains(env.appliedText(), "Color::Green")
		}, e2eWait, e2eTick, "clangd must add the missing enum cases")
	})

	t.Run("extract-function on a selection", func(t *testing.T) {
		before := len(env.cb.appliedEdits())
		// The selection must be whole statements whose variables are not
		// used after it, or clangd withholds the tweak.
		start := locateInFile(t, mainPath, "int scratch = util_add(3, 4);", "int scratch")
		last := `std::printf("scratch %d\n", scratch);`
		endLine := locateInFile(t, mainPath, last, "std::printf")
		end := semanticapi.Position{Line: endLine.Line, Character: endLine.Character + uint32(len(last))}
		env.selectRange(t, "src/main.cpp", start, end)
		env.run(t, env.command(cppActionCmdName, "extract-function", "src/main.cpp", start))
		require.Eventually(t, func() bool {
			return len(env.cb.appliedEdits()) > before && strings.Contains(env.appliedText(), "extracted(")
		}, e2eWait, e2eTick, "clangd must extract the selection into a function")
	})

	t.Run("expand-macro in C", func(t *testing.T) {
		before := len(env.cb.appliedEdits())
		pos := locateInFile(t, filepath.Join(env.dir, "src", "util.c"), "return SQUARE(x);", "SQUARE")
		env.run(t, env.command(cActionCmdName, "expand-macro", "src/util.c", pos))
		require.Eventually(t, func() bool {
			return len(env.cb.appliedEdits()) > before && strings.Contains(env.appliedText(), "((x) * (x))")
		}, e2eWait, e2eTick, "clangd must expand the macro")
	})

	t.Run("expand-macro inside a macro definition names the use to expand", func(t *testing.T) {
		utilPath := filepath.Join(env.dir, "src", "util.c")
		inBody := locateInFile(t, utilPath, "(SQUARE(x) *", "SQUARE")
		onName := locateInFile(t, utilPath, "#define CUBE(x)", "CUBE")

		// The hint rests on the preprocessor expanding nothing inside a
		// directive, so clangd must indeed offer no expansion there.
		for _, pos := range []semanticapi.Position{inBody, onName} {
			res, err := env.mgr.CodeAction(context.Background(), semanticapi.CodeActionParams{
				TextDocument: semanticapi.TextDocumentIdentifier{URI: env.uris["src/util.c"]},
				Range:        semanticapi.Range{Start: pos, End: pos},
				Context:      semanticapi.CodeActionContext{Diagnostics: []semanticapi.Diagnostic{}},
			})
			require.NoError(t, err)
			for _, r := range res {
				assert.NotContains(t, r.CodeAction.Title, "Expand macro", "at %v", pos)
			}
		}

		seen := len(env.notify.notifMessages())
		env.run(t, env.command(cActionCmdName, "expand-macro", "src/util.c", inBody))
		assert.Equal(t, []string{"`SQUARE` sits in the definition of `CUBE`, where nothing is expanded; " +
			"clangd expands a macro where it is used. Place the cursor on a use of `CUBE`, or of " +
			"`SQUARE` outside a `#define`."}, env.notify.notifMessages()[seen:])

		seen = len(env.notify.notifMessages())
		env.run(t, env.command(cActionCmdName, "expand-macro", "src/util.c", onName))
		assert.Equal(t, []string{"`CUBE` is defined here, not used; clangd expands a macro where it " +
			"is used. Place the cursor on a use of `CUBE`."}, env.notify.notifMessages()[seen:])
	})

	t.Run("a tweak clangd does not offer here hints", func(t *testing.T) {
		pos := locateInFile(t, mainPath, "return 0;", "return")
		env.run(t, env.command(cppActionCmdName, "scopify-enum", "src/main.cpp", pos))
		assert.True(t, env.notify.hasMessage("Place the cursor on the name of an unscoped enum"),
			"got %v", env.notify.notifMessages())
	})
}

func TestE2E_CcppQuickfixUsesPublishedDiagnostics(t *testing.T) {
	env := newE2EEnv(t, nil)
	env.open(t, "src/main.cpp", "src/broken.c")
	env.waitReady(t)

	diag := env.waitDiagnostic(t, "src/broken.c", func(d semanticapi.Diagnostic) bool {
		return d.Severity == semanticapi.DiagnosticSeverityError && strings.Contains(d.Message, "';'")
	})

	// clangd derives its quick fixes from the diagnostics the request
	// names; the manager fills them in from the ones clangd published.
	title := env.pickFirst(t, env.command(cActionCmdName, "quickfix", "src/broken.c", diag.Range.Start))
	assert.Contains(t, title, "';'")
	edits := env.editor.editsFor(env.resources["src/broken.c"])
	require.NotEmpty(t, edits, "the fix must reach the editor")
	assert.Equal(t, ";", edits[0].text)
}

func TestE2E_CcppCompdbSelectionRestartsClangd(t *testing.T) {
	env := newE2EEnv(t, nil)
	env.open(t, "src/main.cpp", "src/flags.c")
	env.waitReady(t)

	// build/compile_commands.json, which clangd finds on its own, lacks
	// the define flags.c insists on.
	env.waitDiagnostic(t, "src/flags.c", func(d semanticapi.Diagnostic) bool {
		return strings.Contains(d.Message, "pinned compilation database")
	})

	out, err := runCcpp(t, env.repl, "compdb")
	require.NoError(t, err)
	assert.Contains(t, out, "build\n")
	assert.Contains(t, out, "out/Debug\n")
	assert.NotContains(t, out, "(active)")

	out, err = runCcpp(t, env.repl, "compdb", "use", "out/Debug")
	require.NoError(t, err)
	assert.Contains(t, out, "Using out/Debug/compile_commands.json. clangd restarted.")
	require.Len(t, env.servers(), 2, "the restart starts a new clangd")

	// The new clangd reads the pinned database, so flags.c now compiles
	// with FROM_COMPDB and its diagnostics clear.
	require.Eventually(t, func() bool {
		diags, ok := env.cb.diagnosticsFor(env.uris["src/flags.c"])
		if !ok {
			return false
		}
		for _, d := range diags {
			if strings.Contains(d.Message, "pinned compilation database") {
				return false
			}
		}
		return true
	}, e2eWait, e2eTick, "the restarted clangd must compile flags.c with the pinned database")

	out, err = runCcpp(t, env.repl, "status")
	require.NoError(t, err)
	assert.Contains(t, out, "out/Debug/compile_commands.json (set with ccpp compdb use)")
	out, err = runCcpp(t, env.repl, "compdb")
	require.NoError(t, err)
	assert.Contains(t, out, "out/Debug (active)")

	out, err = runCcpp(t, env.repl, "compdb", "clear")
	require.NoError(t, err)
	assert.Contains(t, out, "clangd restarted.")
	require.Len(t, env.servers(), 3)
	env.waitDiagnostic(t, "src/flags.c", func(d semanticapi.Diagnostic) bool {
		return strings.Contains(d.Message, "pinned compilation database")
	})
}

func TestE2E_CcppLooseFileOutsideAnyProject(t *testing.T) {
	env := newE2EEnv(t, nil)
	env.open(t, "loose/stray.c")

	// A file no database covers still gets a server, and status says so.
	require.Eventually(t, func() bool {
		hover, err := env.mgr.Hover(context.Background(), semanticapi.HoverParams{
			TextDocument: semanticapi.TextDocumentIdentifier{URI: env.uris["loose/stray.c"]},
			Position:     locateInFile(t, filepath.Join(env.dir, "loose", "stray.c"), "int stray(", "stray"),
		})
		return err == nil && hover != nil
	}, e2eWait, e2eTick)
	out, err := runCcpp(t, env.repl, "status")
	require.NoError(t, err)
	assert.Contains(t, out, "loose/stray.c")
	assert.Contains(t, out, "Project: . (CMakeLists.txt)")
	assert.Contains(t, out, "build/compile_commands.json (found by clangd)",
		"clangd probes build/ beside every ancestor, the workspace root included")
}

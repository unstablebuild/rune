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
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/extension/langext/langexttest"
	"unstable.build/rune/internal/ide/idelsp"
	"unstable.build/rune/internal/ide/idelsp/lspcmd"
)

// findTsgo locates a TypeScript 7 native compiler, which serves LSP,
// or skips. An older tsc on PATH is not a candidate.
func findTsgo(t *testing.T) string {
	t.Helper()
	var candidates []string
	for _, name := range []string{"tsgo", "tsc"} {
		if bin, err := exec.LookPath(name); err == nil {
			candidates = append(candidates, bin)
		}
	}
	candidates = append(candidates,
		filepath.Join(os.Getenv("HOME"), ".rune", "lib", "typescript", "bin", "tsgo"),
		filepath.Join(os.Getenv("HOME"), ".rune", "bin", "tsgo"),
	)
	for _, bin := range candidates {
		out, err := exec.Command(bin, "--version").Output()
		if err == nil && isNativeVersion(string(out)) {
			return bin
		}
	}
	t.Skip("TypeScript 7 (tsgo) not found, skipping e2e test")
	return ""
}

// setupTSWorkspace copies testdata/<fixture> into a fresh directory,
// with symlinks resolved so file URIs match the paths tsgo reports.
func setupTSWorkspace(t *testing.T, fixture string) string {
	t.Helper()
	dir := emptyWorkspace(t)
	copyFixture(t, fixture, dir)
	return dir
}

// emptyWorkspace returns a fresh directory with symlinks resolved.
func emptyWorkspace(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	return dir
}

// copyFixture copies testdata/<fixture> into dir.
func copyFixture(t *testing.T, fixture, dir string) {
	t.Helper()
	src := filepath.Join("testdata", fixture)
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
}

// linkWorkspacePackage links the workspace package at
// packages/<target> into packages/<from>/node_modules/<name>, the
// relative symlink bun's isolated linker and pnpm create for a
// workspace:* dependency.
func linkWorkspacePackage(t *testing.T, mono, from, name, target string) {
	t.Helper()
	link := filepath.Join(mono, "packages", from, "node_modules", name)
	require.NoError(t, os.MkdirAll(filepath.Dir(link), 0o755))
	rel, err := filepath.Rel(filepath.Dir(link), filepath.Join(mono, "packages", target))
	require.NoError(t, err)
	require.NoError(t, os.Symlink(rel, link))
}

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

// tsE2E is the extension brought up the way Rune runs it: the editor's
// open events reach both the extension and the idelsp.Manager it
// initializes servers through, and the Manager runs the real tsgo.
type tsE2E struct {
	dir     string
	mgr     *idelsp.Manager
	inits   *initRecorder
	cb      *tsCallback
	editor  *bufferEditor
	notify  *fakeNotifications
	wm      *fakeWM
	opener  *recordingOpener
	console textapi.REPLHandler
	prompt  textapi.CommandHandler
}

// initRecorder hands every call to the Manager and records the roots
// the extension asks it to start a server for.
type initRecorder struct {
	*idelsp.Manager
	mu    sync.Mutex
	roots []string
}

func (r *initRecorder) Initialize(
	ctx context.Context, p semanticapi.InitializeParams,
) (semanticapi.InitializeResult, error) {
	r.mu.Lock()
	r.roots = append(r.roots, p.RootURI)
	r.mu.Unlock()
	return r.Manager.Initialize(ctx, p)
}

func (r *initRecorder) rootURIs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.roots)
}

// startTSExtension brings the extension up on the workspace at dir.
func startTSExtension(t *testing.T, dir string, cfg stubConfig) *tsE2E {
	t.Helper()
	uri, err := workspaceapi.ParseURI("file://" + dir)
	require.NoError(t, err)
	scheme := newLocalScheme(dir)
	env := &tsE2E{
		dir: dir, cb: &tsCallback{}, editor: newBufferEditor(), notify: &fakeNotifications{},
		wm: &fakeWM{}, opener: &recordingOpener{},
	}
	env.mgr = idelsp.New(uri, scheme, scheme, &stubPkgManager{}, nil, nil, idelsp.Config{
		Callback:                env.cb,
		MaxRetries:              1,
		NoInitializeServer:      true,
		InitializeTimeout:       30 * time.Second,
		PullDiagnosticsDebounce: 100 * time.Millisecond,
	})
	t.Cleanup(func() { _ = env.mgr.Close() })
	require.NoError(t, env.editor.SubscribeEvents([]textapi.EventType{
		textapi.EventTypeOpen, textapi.EventTypeClose, textapi.EventTypeEdit, textapi.EventTypeFlush,
		textapi.EventTypeCreate, textapi.EventTypeChange, textapi.EventTypeRemove, textapi.EventTypeRename,
	}, env.mgr))
	env.inits = &initRecorder{Manager: env.mgr}

	require.NoError(t, (&tsExtension{}).extendWorkspaceWith(t.Context(),
		scheme, scheme, env.notify, env.inits, env.editor, env.wm, env.opener, &langexttest.Installer{}, cfg,
		func(_ textapi.CommandManual, h textapi.REPLHandler) error {
			env.console = h
			return nil
		},
		func(_ textapi.CommandManual, h textapi.CommandHandler) error {
			env.prompt = h
			return nil
		}))
	return env
}

// runConsole runs the `ts` console command with args and returns its
// rendered output.
func (env *tsE2E) runConsole(t *testing.T, args ...string) (string, error) {
	t.Helper()
	it, err := env.console.HandleCommand(t.Context(),
		repl.Command{Name: tsCommandName, Args: args}, repl.NopProgressWriter())
	if err != nil {
		return "", err
	}
	return renderText(t, it), nil
}

func TestE2EOpenBringsUpNestedProject(t *testing.T) {
	t.Parallel()
	tsgo := findTsgo(t)
	env := startTSExtension(t, setupTSWorkspace(t, "e2e"),
		stubConfig{strings: map[string]string{"lsp_path": tsgo}})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	src := filepath.Join(env.dir, "app", "src")
	appPath, mainPath := filepath.Join(src, "App.tsx"), filepath.Join(src, "main.ts")
	appURI, mainURI := "file://"+appPath, "file://"+mainPath
	libURI := "file://" + filepath.Join(src, "lib.ts")

	// The workspace root has no marker, so nothing runs until a file of
	// the nested project opens; the Manager queues the open and flushes
	// it to the server the extension then initializes at app/.
	env.editor.open(t, appPath)
	env.editor.open(t, mainPath)

	appAdd := locateInFile(t, appPath, "{add(", "add(")
	require.Eventually(t, func() bool {
		res, err := env.mgr.Definition(ctx, semanticapi.DefinitionParams{
			TextDocument: semanticapi.TextDocumentIdentifier{URI: appURI},
			Position:     appAdd,
		})
		return err == nil && len(res.LocationLinks)+len(res.Locations) > 0
	}, 60*time.Second, 300*time.Millisecond, "tsgo never answered for App.tsx")

	t.Run("tsx buffer is parsed as JSX", func(t *testing.T) {
		report, err := env.mgr.Diagnostic(ctx, semanticapi.DocumentDiagnosticParams{
			TextDocument: semanticapi.TextDocumentIdentifier{URI: appURI},
		})
		require.NoError(t, err)
		assert.Empty(t, report.Items)
	})

	t.Run("user preferences enable inlay hints", func(t *testing.T) {
		hints, err := env.mgr.InlayHint(ctx, semanticapi.InlayHintParams{
			TextDocument: semanticapi.TextDocumentIdentifier{URI: mainURI},
			Range:        semanticapi.Range{End: semanticapi.Position{Line: 3}},
		})
		require.NoError(t, err)
		var labels []string
		for _, h := range hints {
			label := h.Label
			for _, p := range h.LabelParts {
				label += p.Value
			}
			labels = append(labels, strings.TrimSpace(label))
		}
		assert.Contains(t, labels, "a:", "parameter name hints are off by default in tsgo")
	})

	t.Run("per-file diagnostics are pulled and published", func(t *testing.T) {
		orig, err := os.ReadFile(mainPath)
		require.NoError(t, err)
		require.NoError(t, env.mgr.DidChange(ctx, semanticapi.DidChangeTextDocumentParams{
			TextDocument: semanticapi.VersionedTextDocumentIdentifier{URI: mainURI, Version: 2},
			ContentChanges: []semanticapi.TextDocumentContentChangeEvent{
				{Text: string(orig) + "export const broken: number = \"oops\";\n"},
			},
		}))
		// tsgo pushes tsconfig diagnostics only, so a published
		// per-file error can only come from the pull bridge.
		require.Eventually(t, func() bool {
			for _, d := range env.cb.diagnosticsFor(mainURI) {
				if strings.Contains(d.Message, "not assignable to type 'number'") {
					return true
				}
			}
			return false
		}, 30*time.Second, 200*time.Millisecond)
	})

	t.Run("definition crosses from tsx into ts", func(t *testing.T) {
		res, err := env.mgr.Definition(ctx, semanticapi.DefinitionParams{
			TextDocument: semanticapi.TextDocumentIdentifier{URI: appURI},
			Position:     appAdd,
		})
		require.NoError(t, err)
		var target string
		switch {
		case len(res.LocationLinks) > 0:
			target = res.LocationLinks[0].TargetURI
		case len(res.Locations) > 0:
			target = res.Locations[0].URI
		}
		assert.Equal(t, libURI, target)
	})
}

// waitForProject blocks until tsgo answers a definition request in the
// file at path, so the commands under test run against a loaded project.
func waitForProject(t *testing.T, env *tsE2E, path, marker, ident string) {
	t.Helper()
	pos := locateInFile(t, path, marker, ident)
	require.Eventually(t, func() bool {
		res, err := env.mgr.Definition(t.Context(), semanticapi.DefinitionParams{
			TextDocument: semanticapi.TextDocumentIdentifier{URI: "file://" + path},
			Position:     pos,
		})
		return err == nil && len(res.LocationLinks)+len(res.Locations) > 0
	}, 60*time.Second, 300*time.Millisecond, "tsgo never answered for %s", path)
}

func TestE2EMonorepo(t *testing.T) {
	t.Parallel()
	tsgo := findTsgo(t)
	for _, tt := range []struct {
		name string
		// sub is where the monorepo sits in the workspace.
		sub string
	}{
		{"workspace is the monorepo", ""},
		{"monorepo nested in the workspace", "web"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ws := emptyWorkspace(t)
			mono := filepath.Join(ws, tt.sub)
			copyFixture(t, "monorepo", mono)
			linkWorkspacePackage(t, mono, "b", "@mono/a", "a")
			env := startTSExtension(t, ws, stubConfig{strings: map[string]string{"lsp_path": tsgo}})

			greetPath := filepath.Join(mono, "packages", "a", "src", "index.ts")
			usePath := filepath.Join(mono, "packages", "b", "src", "use.ts")
			greetURI, useURI := "file://"+greetPath, "file://"+usePath
			env.editor.open(t, usePath)
			env.editor.open(t, greetPath)
			waitForProject(t, env, usePath, `greet("world")`, "greet")
			waitForProject(t, env, greetPath, "function greet", "greet")
			decl := locateInFile(t, greetPath, "function greet", "greet")
			pkgs := filepath.Join(tt.sub, "packages")

			t.Run("one server spans the packages", func(t *testing.T) {
				assert.Equal(t, []string{"file://" + mono}, env.inits.rootURIs())
			})

			t.Run("references reach the importing package", func(t *testing.T) {
				locs, err := env.mgr.References(t.Context(), semanticapi.ReferenceParams{
					TextDocument: semanticapi.TextDocumentIdentifier{URI: greetURI},
					Position:     decl,
					Context:      semanticapi.ReferenceContext{IncludeDeclaration: true},
				})
				require.NoError(t, err)
				var uris []string
				for _, l := range locs {
					uris = append(uris, l.URI)
				}
				assert.Contains(t, uris, greetURI)
				assert.Contains(t, uris, useURI)
			})

			t.Run("rename edits the importing package", func(t *testing.T) {
				edit, err := env.mgr.Rename(t.Context(), semanticapi.RenameParams{
					TextDocument: semanticapi.TextDocumentIdentifier{URI: greetURI},
					Position:     decl,
					NewName:      "salute",
				})
				require.NoError(t, err)
				require.NotNil(t, edit)
				var uris []string
				for uri := range edit.Changes {
					uris = append(uris, uri)
				}
				for _, c := range edit.DocumentChanges {
					if c.TextDocumentEdit != nil {
						uris = append(uris, c.TextDocumentEdit.TextDocument.URI)
					}
				}
				assert.Contains(t, uris, greetURI)
				assert.Contains(t, uris, useURI)
			})

			t.Run("check takes the focused file's project", func(t *testing.T) {
				env.editor.focus(t, greetPath)
				out, err := env.runConsole(t, "check")
				require.NoError(t, err)
				assert.Contains(t, out, "No type errors in "+filepath.Join(pkgs, "a"))

				env.editor.focus(t, usePath)
				_, err = env.runConsole(t, "check")
				require.Error(t, err)
				assert.Contains(t, err.Error(), filepath.Join(pkgs, "b", "src", "todo.ts")+
					"(1,14): error TS2322: Type 'string' is not assignable to type 'number'.")
			})

			t.Run("check covers every project below the workspace", func(t *testing.T) {
				_, err := env.runConsole(t, "check", ".")
				require.Error(t, err)
				assert.Contains(t, err.Error(), filepath.Join(pkgs, "b", "src", "todo.ts")+"(1,14): error TS2322")
				assert.NotContains(t, err.Error(), "TS5081", "the workspace root has no tsconfig.json")
			})

			t.Run("scripts run in the focused file's package with its workspace's manager", func(t *testing.T) {
				findBun(t)
				env.editor.focus(t, usePath)
				out, err := env.runConsole(t, "test")
				require.NoError(t, err)
				assert.Contains(t, out, "In "+filepath.Join(pkgs, "b")+":")
				assert.Contains(t, out, "b tests via bun/")

				out, err = env.runConsole(t, "run", filepath.Join(pkgs, "a")+"/", "test")
				require.NoError(t, err)
				assert.Contains(t, out, "a tests via bun/")
			})
		})
	}
}

func TestE2ECheck(t *testing.T) {
	t.Parallel()
	tsgo := findTsgo(t)
	for _, tt := range []struct {
		fixture string
		// link installs the fixture's workspace packages.
		link func(t *testing.T, dir string)
		want string
	}{
		{
			fixture: "vite",
			want:    "src/main.ts(1,14): error TS2322: Type 'string' is not assignable to type 'number'.",
		},
		{
			fixture: "references",
			link:    func(t *testing.T, dir string) { linkWorkspacePackage(t, dir, "b", "@refs/a", "a") },
			want:    "packages/b/src/use.ts(3,14): error TS2322: Type 'string' is not assignable to type 'number'.",
		},
	} {
		t.Run(tt.fixture, func(t *testing.T) {
			t.Parallel()
			dir := setupTSWorkspace(t, tt.fixture)
			if tt.link != nil {
				tt.link(t, dir)
			}
			env := startTSExtension(t, dir, stubConfig{strings: map[string]string{"lsp_path": tsgo}})
			_, err := env.runConsole(t, "check")
			require.Error(t, err, "the type error in a referenced project went unreported")
			assert.Contains(t, err.Error(), tt.want)
			assert.NotContains(t, err.Error(), "TS2307", "a referenced project's declarations were not built")
			assert.NotContains(t, err.Error(), "TS6310", "build mode was told not to emit")
		})
	}
}

func TestE2EProjectNotices(t *testing.T) {
	t.Parallel()
	tsgo := findTsgo(t)
	cfg := stubConfig{strings: map[string]string{"lsp_path": tsgo}}

	t.Run("an older typescript's removed options", func(t *testing.T) {
		t.Parallel()
		dir := setupTSWorkspace(t, "legacy")
		manifest := filepath.Join(dir, "node_modules", "typescript", "package.json")
		require.NoError(t, os.MkdirAll(filepath.Dir(manifest), 0o755))
		require.NoError(t, os.WriteFile(manifest, []byte(`{"name": "typescript", "version": "5.9.3"}`), 0o644))
		env := startTSExtension(t, dir, cfg)
		assert.Contains(t, env.notify.messages(), dir+" installs TypeScript 5.9.3, but Rune's language "+
			"server is TypeScript 7, which removed tsconfig options older releases accept, such as "+
			"baseUrl, target ES5 and moduleResolution node. `ts check` names the ones to change.")

		_, err := env.runConsole(t, "check")
		require.Error(t, err)
		for _, want := range []string{
			"tsconfig.json(3,15): error TS5108: Option 'target=ES5' has been removed.",
			"tsconfig.json(5,25): error TS5108: Option 'moduleResolution=node10' has been removed.",
			"tsconfig.json(6,5): error TS5102: Option 'baseUrl' has been removed.",
		} {
			assert.Contains(t, err.Error(), want)
		}

		indexPath := filepath.Join(dir, "src", "index.ts")
		env.editor.open(t, indexPath)
		report, err := env.mgr.Diagnostic(t.Context(), semanticapi.DocumentDiagnosticParams{
			TextDocument: semanticapi.TextDocumentIdentifier{URI: "file://" + indexPath},
		})
		require.NoError(t, err)
		var messages []string
		for _, d := range report.Items {
			messages = append(messages, d.Message)
		}
		assert.Contains(t, messages, "Cannot find module 'src/math' or its corresponding type declarations.",
			"the import baseUrl resolved is what the notice explains")
	})

	t.Run("a typescript file in no tsconfig.json's project", func(t *testing.T) {
		t.Parallel()
		env := startTSExtension(t, setupTSWorkspace(t, "e2e"), cfg)
		mainPath := filepath.Join(env.dir, "app", "src", "main.ts")
		env.editor.open(t, mainPath)
		waitForProject(t, env, mainPath, "add(", "add")
		env.editor.open(t, filepath.Join(env.dir, "scripts", "loose.ts"))
		want := "scripts/loose.ts is in no tsconfig.json's project, so TypeScript checks it with default " +
			"options, without path aliases or packages' types such as @types/node. " +
			"Add a tsconfig.json that includes it."
		require.Eventually(t, func() bool { return slices.Contains(env.notify.messages(), want) },
			60*time.Second, 200*time.Millisecond, "notifications: %v", env.notify.messages())
		for _, m := range env.notify.messages() {
			assert.NotContains(t, m, "main.ts", "main.ts is in app/tsconfig.json's project")
		}
	})
}

// pickAction runs a code-action command whose actions are offered in a
// picker, chooses the one titled want and waits for it to apply.
func pickAction(t *testing.T, env *tsE2E, cmd textapi.Command, want string) {
	t.Helper()
	done := make(chan error, 1)
	go debug.CapturePanicReport(func() { done <- env.prompt.HandleCommand(t.Context(), cmd) })
	var picker lspcmd.CodeActionPicker
	deadline := time.After(30 * time.Second)
	for picker == nil {
		select {
		case err := <-done:
			require.NoError(t, err)
			t.Fatalf("no picker was shown; notifications: %v", env.notify.messages())
		case <-deadline:
			t.Fatalf("timed out waiting for the picker offering %q", want)
		case <-time.After(20 * time.Millisecond):
			picker, _ = env.wm.picker().(lspcmd.CodeActionPicker)
		}
	}

	var titles []string
	for _, a := range picker.Actions() {
		titles = append(titles, a.Title)
	}
	idx := slices.Index(titles, want)
	require.GreaterOrEqual(t, idx, 0, "%q not offered; got %v", want, titles)
	for range idx {
		picker.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowDown})
	}
	picker.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatalf("timed out applying %q", want)
	}
}

func TestE2ECommandPrompt(t *testing.T) {
	t.Parallel()
	tsgo := findTsgo(t)
	env := startTSExtension(t, setupTSWorkspace(t, "e2e"), stubConfig{
		strings: map[string]string{"lsp_path": tsgo},
		bools:   map[string]bool{"developer": true},
	})
	src := filepath.Join(env.dir, "app", "src")
	importsPath := filepath.Join(src, "imports.ts")
	env.editor.open(t, importsPath)
	waitForProject(t, env, importsPath, "add(1", "add")

	for _, tt := range []struct {
		sub  string
		want string
	}{
		{"organize-imports", `import { add, sub } from "./math.js";`},
		{"remove-unused-imports", `import { sub, add } from "./math.js";`},
		{"sort-imports", `import { add, mul, sub } from "./math.js";`},
	} {
		t.Run(tt.sub, func(t *testing.T) {
			res := env.editor.open(t, importsPath)
			require.NoError(t, env.prompt.HandleCommand(t.Context(), tsCmd(t, importsPath, 0, 0, tt.sub)))
			first, _, _ := strings.Cut(env.editor.text(res), "\n")
			assert.Equal(t, tt.want, first)
		})
	}

	t.Run("organized imports are left alone", func(t *testing.T) {
		mainPath := filepath.Join(src, "main.ts")
		res := env.editor.open(t, mainPath)
		before := env.editor.text(res)
		require.NoError(t, env.prompt.HandleCommand(t.Context(), tsCmd(t, mainPath, 0, 0, "organize-imports")))
		assert.Equal(t, before, env.editor.text(res))
		assert.Contains(t, env.notify.messages(), "Imports are already organized")
	})

	missingPath := filepath.Join(src, "missing.ts")
	t.Run("quickfix imports the name under the cursor", func(t *testing.T) {
		res := env.editor.open(t, missingPath)
		pos := locateInFile(t, missingPath, "sum", "add")
		pickAction(t, env, tsCmd(t, missingPath, int(pos.Line), int(pos.Character), "quickfix"),
			`Add import from "./math.js"`)
		assert.Contains(t, env.editor.text(res), `import { add } from "./math.js";`)
	})

	t.Run("fix-all imports every missing name", func(t *testing.T) {
		res := env.editor.open(t, missingPath)
		require.NoError(t, env.prompt.HandleCommand(t.Context(), tsCmd(t, missingPath, 0, 0, "fix-all")))
		assert.Contains(t, env.editor.text(res), "mul")
		assert.Contains(t, env.editor.text(res), `from "./math.js"`)
	})

	t.Run("source-definition skips the declaration file", func(t *testing.T) {
		greetingPath := filepath.Join(src, "greeting.ts")
		env.editor.open(t, greetingPath)
		pos := locateInFile(t, greetingPath, "hello", "greet")
		require.NoError(t, env.prompt.HandleCommand(t.Context(),
			tsCmd(t, greetingPath, int(pos.Line), int(pos.Character), "source-definition")))
		impl := "file://" + filepath.Join(env.dir, "app", "vendor", "greet.js")
		assert.Contains(t, env.opener.openedURIs(), impl)
		assert.Equal(t, term.Coordinates{X: 16}, env.editor.cursorOf(impl))
	})

	t.Run("open-tsconfig opens the owning config", func(t *testing.T) {
		require.NoError(t, env.prompt.HandleCommand(t.Context(), tsCmd(t, importsPath, 0, 0, "open-tsconfig")))
		assert.Contains(t, env.opener.openedURIs(), "file://"+filepath.Join(env.dir, "app", "tsconfig.json"))
	})

	t.Run("open-tsconfig reports an inferred project", func(t *testing.T) {
		loosePath := filepath.Join(env.dir, "scripts", "loose.ts")
		env.editor.open(t, loosePath)
		require.NoError(t, env.prompt.HandleCommand(t.Context(), tsCmd(t, loosePath, 0, 0, "open-tsconfig")))
		assert.Contains(t, env.notify.messages(),
			"No tsconfig.json or jsconfig.json includes loose.ts; it is checked as an inferred project")
	})

	t.Run("profiling writes profiles", func(t *testing.T) {
		dir := t.TempDir()
		run := func(args ...string) {
			t.Helper()
			cmd := tsCmd(t, importsPath, 0, 0, args[0], args[1:]...)
			require.NoError(t, env.prompt.HandleCommand(t.Context(), cmd))
		}
		run("cpu-profile", "start", dir)
		run("gc")
		run("cpu-profile", "stop")
		run("heap-profile", dir)
		run("alloc-profile", dir)
		for _, pattern := range []string{"*-cpuprofile.pb.gz", "*-heapprofile.pb.gz", "*-allocprofile.pb.gz"} {
			matches, err := filepath.Glob(filepath.Join(dir, pattern))
			require.NoError(t, err)
			assert.NotEmpty(t, matches, pattern)
		}
		assert.Contains(t, env.notify.messages(), "Ran the garbage collector of the TypeScript language servers")
	})
}

func TestE2EConsole(t *testing.T) {
	t.Parallel()
	tsgo := findTsgo(t)
	env := startTSExtension(t, setupTSWorkspace(t, "e2e"),
		stubConfig{strings: map[string]string{"lsp_path": tsgo}})

	t.Run("check reports type errors", func(t *testing.T) {
		_, err := env.runConsole(t, "check", "app")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "app/src/missing.ts(1,20): error TS2304: Cannot find name 'add'.")
	})

	t.Run("version names the compiler", func(t *testing.T) {
		out, err := env.runConsole(t, "version")
		require.NoError(t, err)
		assert.Contains(t, out, "Language server: TypeScript 7.")
		assert.Contains(t, out, tsgo)
	})
}

// findBun locates bun on PATH, where the extension runs it from, or
// skips.
func findBun(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun not found on PATH, skipping e2e test")
	}
	return bin
}

// installBunProject installs the locked dependencies of the project at
// dir with the given bun linker, skipping when the registry cannot be
// reached.
func installBunProject(t *testing.T, bun, dir, linker string) {
	t.Helper()
	cmd := exec.Command(bun, "install", "--frozen-lockfile", "--linker", linker)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("bun install --linker %s failed: %v\n%s", linker, err, out)
	}
}

func TestE2EBunProject(t *testing.T) {
	t.Parallel()
	bun := findBun(t)
	for _, tt := range []struct {
		linker string
		// scope is where the linker puts typescript@7's platform package.
		scope string
	}{
		{"hoisted", filepath.Join("node_modules", "@typescript")},
		{"isolated", filepath.Join("node_modules", ".bun", "node_modules", "@typescript")},
	} {
		t.Run(tt.linker, func(t *testing.T) {
			t.Parallel()
			dir := setupTSWorkspace(t, "bun")
			installBunProject(t, bun, dir, tt.linker)
			compilers, err := filepath.Glob(filepath.Join(dir, tt.scope, "typescript-*", "lib", "tsc"))
			require.NoError(t, err)
			require.Len(t, compilers, 1, "no native compiler under %s", tt.scope)
			env := startTSExtension(t, dir, stubConfig{})

			t.Run("the project's compiler serves the workspace", func(t *testing.T) {
				out, err := env.runConsole(t, "version")
				require.NoError(t, err)
				assert.Contains(t, out, "Language server: TypeScript 7.0.2")
				assert.Contains(t, out, compilers[0])
			})

			t.Run("check resolves bun's types", func(t *testing.T) {
				out, err := env.runConsole(t, "check")
				require.NoError(t, err)
				assert.Contains(t, out, "No type errors in .")
			})

			t.Run("definition reaches bun's declarations", func(t *testing.T) {
				indexPath := filepath.Join(dir, "index.ts")
				env.editor.open(t, indexPath)
				waitForProject(t, env, indexPath, "Bun.serve", "serve(")
				res, err := env.mgr.Definition(t.Context(), semanticapi.DefinitionParams{
					TextDocument: semanticapi.TextDocumentIdentifier{URI: "file://" + indexPath},
					Position:     locateInFile(t, indexPath, "Bun.serve", "serve("),
				})
				require.NoError(t, err)
				var target string
				switch {
				case len(res.LocationLinks) > 0:
					target = res.LocationLinks[0].TargetURI
				case len(res.Locations) > 0:
					target = res.Locations[0].URI
				}
				assert.Contains(t, target, "/bun-types/")
			})

			t.Run("test runs bun's runner without a test script", func(t *testing.T) {
				out, err := env.runConsole(t, "test")
				require.NoError(t, err)
				assert.Contains(t, out, "math.test.ts")
				assert.Contains(t, out, "1 pass")
				assert.Contains(t, out, "0 fail")
			})
		})
	}
}

// bufferEditor is a textapi.Editor over in-memory buffers loaded from
// disk, so the text a code action leaves behind can be asserted. An
// event reaches every subscriber to its type, as it does in Rune.
type bufferEditor struct {
	mu      sync.Mutex
	subs    []subscription
	res     map[string]*stubResource
	texts   map[string]string
	cursors map[string]term.Coordinates
}

// subscription is an event handler and the event types it subscribed
// to, every type when nil.
type subscription struct {
	types   []textapi.EventType
	handler textapi.EventHandler
}

var _ textapi.Editor = (*bufferEditor)(nil)

func newBufferEditor() *bufferEditor {
	return &bufferEditor{
		res:     map[string]*stubResource{},
		texts:   map[string]string{},
		cursors: map[string]term.Coordinates{},
	}
}

// open (re)loads the file at path from disk and announces it.
func (e *bufferEditor) open(t *testing.T, path string) *stubResource {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	uri, err := workspaceapi.ParseURI("file://" + path)
	require.NoError(t, err)
	e.mu.Lock()
	res := e.resource(uri)
	e.texts[uri.String()] = string(data)
	e.mu.Unlock()
	e.fire(t, textapi.Event{Type: textapi.EventTypeOpen, URI: uri})
	return res
}

// focus moves the focus to the file at path.
func (e *bufferEditor) focus(t *testing.T, path string) {
	t.Helper()
	uri, err := workspaceapi.ParseURI("file://" + path)
	require.NoError(t, err)
	e.fire(t, textapi.Event{Type: textapi.EventTypeFocus, URI: uri})
}

func (e *bufferEditor) fire(t *testing.T, ev textapi.Event) {
	e.mu.Lock()
	subs := slices.Clone(e.subs)
	e.mu.Unlock()
	for _, s := range subs {
		if s.types == nil || slices.Contains(s.types, ev.Type) {
			s.handler.Handle(t.Context(), ev)
		}
	}
}

func (e *bufferEditor) resource(uri workspaceapi.URI) *stubResource {
	res, ok := e.res[uri.String()]
	if !ok {
		res = &stubResource{uri: uri}
		e.res[uri.String()] = res
	}
	return res
}

func (e *bufferEditor) text(res *stubResource) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.texts[res.uri.String()]
}

func (e *bufferEditor) cursorOf(uri string) term.Coordinates {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cursors[uri]
}

func (e *bufferEditor) SubscribeEvents(types []textapi.EventType, h textapi.EventHandler) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.subs = append(e.subs, subscription{types: types, handler: h})
	return nil
}

func (e *bufferEditor) Editor(uri workspaceapi.URI) (textapi.Handler, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.resource(uri), nil
}

func (e *bufferEditor) SetLocationList(
	textapi.Handler, textapi.LocationPriority, string, textapi.LocationList,
) error {
	return nil
}
func (e *bufferEditor) MoveToNextLocation(textapi.Handler, string) error { return nil }
func (e *bufferEditor) MoveToPrevLocation(textapi.Handler, string) error { return nil }

func (e *bufferEditor) Cursor(h textapi.Handler) (term.Coordinates, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cursors[h.Resource().String()], nil
}

func (e *bufferEditor) SetCursor(h textapi.Handler, c term.Coordinates) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cursors[h.Resource().String()] = c
	return nil
}

func (e *bufferEditor) CellView(h textapi.Handler) textapi.CellView {
	return bufferCellView{e: e, key: h.Resource().String()}
}

func (e *bufferEditor) CellEditor(h textapi.Handler) textapi.CellEditor {
	return bufferCellEditor{e: e, key: h.Resource().String()}
}

func (e *bufferEditor) SetDefaultAttributes(textapi.Handler, term.Attributes) error { return nil }

type bufferCellEditor struct {
	e   *bufferEditor
	key string
}

// bufferCellView lets code-action commands see the buffer's current
// text, as they do in Rune, so an edit that changes nothing is skipped.
type bufferCellView struct {
	e   *bufferEditor
	key string
}

func (v bufferCellView) RawCells() ([][]term.Cell, error) {
	v.e.mu.Lock()
	defer v.e.mu.Unlock()
	lines := strings.Split(v.e.texts[v.key], "\n")
	cells := make([][]term.Cell, len(lines))
	for i, line := range lines {
		for _, r := range line {
			cells[i] = append(cells[i], term.Cell{Ch: r, Width: 1, Bytes: uint8(utf8.RuneLen(r))})
		}
	}
	return cells, nil
}

func (c bufferCellEditor) Edit(
	_ context.Context, start, end term.Coordinates, str string,
) (term.Coordinates, term.Coordinates, string, error) {
	c.e.mu.Lock()
	defer c.e.mu.Unlock()
	text := c.e.texts[c.key]
	from, err := textOffset(text, start)
	if err != nil {
		return start, end, "", err
	}
	to, err := textOffset(text, end)
	if err != nil {
		return start, end, "", err
	}
	c.e.texts[c.key] = text[:from] + str + text[to:]
	return start, end, text[from:to], nil
}

// textOffset converts a (column, line) coordinate into a byte offset of
// text, which the fixtures keep ASCII.
func textOffset(text string, c term.Coordinates) (int, error) {
	lines := strings.SplitAfter(text, "\n")
	if c.Y >= len(lines) {
		return 0, fmt.Errorf("line %d out of range", c.Y)
	}
	off := 0
	for _, l := range lines[:c.Y] {
		off += len(l)
	}
	if c.X > len(strings.TrimSuffix(lines[c.Y], "\n")) {
		return 0, fmt.Errorf("column %d out of range on line %d", c.X, c.Y)
	}
	return off + c.X, nil
}

// tsCallback implements idelsp.Callback, recording published
// diagnostics.
type tsCallback struct {
	mu          sync.Mutex
	diagnostics []semanticapi.PublishDiagnosticsParams
}

func (c *tsCallback) diagnosticsFor(uri string) []semanticapi.Diagnostic {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.diagnostics) - 1; i >= 0; i-- {
		if c.diagnostics[i].URI == uri {
			return c.diagnostics[i].Diagnostics
		}
	}
	return nil
}

func (c *tsCallback) PublishDiagnostics(
	_ context.Context, params semanticapi.PublishDiagnosticsParams,
) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.diagnostics = append(c.diagnostics, params)
	return nil
}

func (c *tsCallback) ShowMessage(context.Context, semanticapi.ShowMessageParams) error { return nil }
func (c *tsCallback) LogMessage(context.Context, semanticapi.LogMessageParams) error   { return nil }
func (c *tsCallback) Progress(context.Context, semanticapi.ProgressParams) error       { return nil }
func (c *tsCallback) LogTrace(context.Context, semanticapi.LogTraceParams) error       { return nil }
func (c *tsCallback) ShowDocument(
	context.Context, semanticapi.ShowDocumentParams,
) (semanticapi.ShowDocumentResult, error) {
	return semanticapi.ShowDocumentResult{Success: true}, nil
}
func (c *tsCallback) ShowMessageRequest(
	context.Context, semanticapi.ShowMessageRequestParams,
) (*semanticapi.MessageActionItem, error) {
	return nil, nil
}
func (c *tsCallback) WorkDoneProgressCreate(
	context.Context, semanticapi.WorkDoneProgressCreateParams,
) error {
	return nil
}
func (c *tsCallback) ApplyEdit(
	context.Context, semanticapi.ApplyWorkspaceEditParams,
) (semanticapi.ApplyWorkspaceEditResult, error) {
	return semanticapi.ApplyWorkspaceEditResult{Applied: true}, nil
}
func (c *tsCallback) WorkspaceFolders(context.Context) ([]semanticapi.WorkspaceFolder, error) {
	return nil, nil
}
func (c *tsCallback) Configuration(
	_ context.Context, params semanticapi.ConfigurationParams,
) ([]json.RawMessage, error) {
	result := make([]json.RawMessage, len(params.Items))
	for i := range result {
		result[i] = json.RawMessage(`null`)
	}
	return result, nil
}
func (c *tsCallback) RegisterCapability(context.Context, semanticapi.RegistrationParams) error {
	return nil
}
func (c *tsCallback) UnregisterCapability(context.Context, semanticapi.UnregistrationParams) error {
	return nil
}
func (c *tsCallback) CodeLensRefresh(context.Context) error           { return nil }
func (c *tsCallback) SemanticTokensRefresh(context.Context) error     { return nil }
func (c *tsCallback) InlayHintRefresh(context.Context) error          { return nil }
func (c *tsCallback) DiagnosticRefresh(context.Context) error         { return nil }
func (c *tsCallback) FileDidChange(string, int32, bool, bool)         {}
func (c *tsCallback) InvalidateAllPending()                           {}
func (c *tsCallback) WaitFileProcessed(context.Context, string) error { return nil }
func (c *tsCallback) HandleNotification(context.Context, string, json.RawMessage) error {
	return nil
}

// localScheme implements schemeapi.FileSystem and schemeapi.Executor
// over the local OS, rooted at a directory so relative paths resolve
// into the test workspace.
type localScheme struct {
	root  string
	mu    sync.Mutex
	procs map[workspaceapi.Pid]*os.Process
	next  workspaceapi.Pid
}

func newLocalScheme(root string) *localScheme {
	return &localScheme{root: root, procs: map[workspaceapi.Pid]*os.Process{}, next: 1}
}

func (s *localScheme) resolve(path string) string {
	if filepath.IsAbs(path) {
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
func (s *localScheme) Stat(name string) (os.FileInfo, error)  { return os.Stat(s.resolve(name)) }
func (s *localScheme) Lstat(name string) (os.FileInfo, error) { return os.Lstat(s.resolve(name)) }
func (s *localScheme) Rename(o, n string) error               { return os.Rename(s.resolve(o), s.resolve(n)) }
func (s *localScheme) Remove(name string) error               { return os.Remove(s.resolve(name)) }
func (s *localScheme) Join(elem ...string) string             { return filepath.Join(elem...) }
func (s *localScheme) TempFile(dir, prefix string) (workspaceapi.File, error) {
	return os.CreateTemp(s.resolve(dir), prefix)
}
func (s *localScheme) Symlink(o, n string) error            { return os.Symlink(s.resolve(o), s.resolve(n)) }
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
	c := exec.CommandContext(ctx, cmd.Path, cmd.Args...)
	c.Dir = cmd.Dir
	if cmd.Env != nil {
		c.Env = cmd.Env
	}
	c.Stdin, c.Stdout, c.Stderr = cmd.Stdin, cmd.Stdout, cmd.Stderr
	if err := c.Start(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	pid := s.next
	s.next++
	s.procs[pid] = c.Process
	s.mu.Unlock()
	if cmd.Watcher != nil {
		ch := cmd.Watcher.WatchProcess()
		go debug.CapturePanicReport(func() { ch <- c.Wait() })
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

func (s *localScheme) Close() error { return nil }

// stubPkgManager resolves no package files: the extension hands the
// Manager an absolute server path.
type stubPkgManager struct{}

func (stubPkgManager) LibDir(context.Context, string) (iterator.Iterator[string], error) {
	return iterator.FromSlice([]string(nil)), nil
}

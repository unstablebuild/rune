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

package lspcmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/handler/locationpicker"
	idelsp "unstable.build/rune/internal/ide/idelsp"
)

// collectIter drains an iterator into a string slice.
func collectIter(t *testing.T, iter iterator.Iterator[string]) []string {
	t.Helper()
	var out []string
	for {
		v, ok := iter.Next(context.Background())
		if !ok {
			break
		}
		out = append(out, v)
	}
	return out
}

func TestRouterCompleteSymbol(t *testing.T) {
	t.Parallel()

	rootURI, err := workspaceapi.ParseURI("file:///project")
	require.NoError(t, err)

	parser := &mockParser{
		listReferencedFn: func() (iterator.Iterator[string], error) {
			return iterator.FromSlice([]string{"fmt.Stringer", "fmt.Println"}), nil
		},
	}

	newRouter := func(t *testing.T) textapi.CommandHandler {
		t.Helper()
		cfg := DefaultConfig()
		cfg.RootURI = rootURI
		cfg.ScheduleNextTick = syncTick
		cfg.Interrupter = term.NopInterrupter()
		cfg.Parser = parser
		router, err := AllHandler(
			&mockLSP{}, &mockEditor{}, &mockWindowManager{},
			&mockResourceOpener{}, &mockNotifications{},
			&mockFileSystem{}, parser, cfg,
		)
		require.NoError(t, err)
		return router
	}

	// All subcommands that use completeReferencedSymbol.
	subcommands := []string{
		"hover", "definition", "declaration",
		"type-definition", "implementation", "references",
	}

	for _, sub := range subcommands {
		t.Run(sub, func(t *testing.T) {
			t.Parallel()

			t.Run("returns referenced symbols for any query", func(t *testing.T) {
				t.Parallel()
				router := newRouter(t)
				iter, err := router.Complete(context.Background(), "lsp", []string{sub, "fmt"})
				require.NoError(t, err)
				names := collectIter(t, iter)
				assert.Contains(t, names, "fmt.Stringer")
				assert.Contains(t, names, "fmt.Println")
			})
		})
	}
}

func TestRouterNilResource(t *testing.T) {
	t.Parallel()

	rootURI, err := workspaceapi.ParseURI("file:///project")
	require.NoError(t, err)

	cfg := DefaultConfig()
	cfg.RootURI = rootURI
	cfg.ScheduleNextTick = syncTick
	cfg.Interrupter = term.NopInterrupter()
	cfg.Parser = &mockParser{}
	router, err := AllHandler(
		&mockLSP{}, &mockEditor{}, &mockWindowManager{},
		&mockResourceOpener{}, &mockNotifications{},
		&mockFileSystem{}, cfg.Parser, cfg,
	)
	require.NoError(t, err)

	// Cursor-only commands should return an error when no file is open.
	cursorOnly := []string{"format", "complete", "signature-help", "rename"}
	for _, sub := range cursorOnly {
		t.Run(sub+" no file", func(t *testing.T) {
			cmd := textapi.Command{
				Name: "lsp",
				Args: []string{sub},
			}
			err := router.HandleCommand(context.Background(), cmd)
			require.Error(t, err, "expected error for %s with nil Resource", sub)
			assert.Contains(t, err.Error(), "no file open")
		})
	}

	// Symbol-accepting commands with no args should return an error.
	symbolCmds := []string{
		"hover", "definition", "declaration",
		"type-definition", "implementation", "references",
	}
	for _, sub := range symbolCmds {
		t.Run(sub+" no args no file", func(t *testing.T) {
			cmd := textapi.Command{
				Name: "lsp",
				Args: []string{sub},
			}
			err := router.HandleCommand(context.Background(), cmd)
			require.Error(t, err, "expected error for %s with nil Resource and no args", sub)
			assert.Contains(t, err.Error(), "no file open")
		})
	}

	// Symbol-accepting commands with args should NOT error (async resolution).
	for _, sub := range symbolCmds {
		t.Run(sub+" with args no file", func(t *testing.T) {
			cmd := textapi.Command{
				Name: "lsp",
				Args: []string{sub, "fmt.Println"},
			}
			err := router.HandleCommand(context.Background(), cmd)
			require.NoError(t, err, "%s with symbol arg should proceed without error", sub)
		})
	}
}

func TestRouterCompleteReferencedSymbolFallback(t *testing.T) {
	t.Parallel()

	rootURI, err := workspaceapi.ParseURI("file:///project")
	require.NoError(t, err)

	subcommands := []string{
		"hover", "definition", "declaration",
		"type-definition", "implementation", "references",
	}

	for _, sub := range subcommands {
		t.Run(sub, func(t *testing.T) {
			t.Parallel()

			t.Run("parser fallback returns referenced symbols", func(t *testing.T) {
				t.Parallel()
				parser := &mockParser{
					listReferencedFn: func() (iterator.Iterator[string], error) {
						return iterator.FromSlice([]string{"fmt.Println"}), nil
					},
				}
				lsp := &mockLSP{
					// Document symbol should NOT be called when parser is available.
					documentSymbolFn: func(_ context.Context, _ semanticapi.DocumentSymbolParams) (semanticapi.DocumentSymbolResult, error) {
						t.Error("DocumentSymbol should not be called when parser is set")
						return semanticapi.DocumentSymbolResult{}, nil
					},
				}
				cfg := DefaultConfig()
				cfg.RootURI = rootURI
				cfg.ScheduleNextTick = syncTick
				cfg.Interrupter = term.NopInterrupter()
				cfg.Parser = parser
				router, err := AllHandler(
					lsp, &mockEditor{}, &mockWindowManager{},
					&mockResourceOpener{}, &mockNotifications{},
					&mockFileSystem{}, parser, cfg,
				)
				require.NoError(t, err)

				iter, err := router.Complete(context.Background(), "lsp", []string{sub, ""})
				require.NoError(t, err)
				assert.Equal(t, []string{"fmt.Println"}, collectIter(t, iter))
			})
		})
	}
}

func TestE2ECommands(t *testing.T) {
	t.Parallel()
	goplsBin := findGopls(t)
	tmpDir := setupTestWorkspace(t, "../testdata")

	rootURI, err := workspaceapi.ParseURI(
		"file://" + tmpDir,
	)
	require.NoError(t, err)

	mainPath := filepath.Join(tmpDir, "main.go")
	mainContent, err := os.ReadFile(mainPath)
	require.NoError(t, err)
	mainText := strings.Replace(
		string(mainContent),
		"\treturn fmt.Sprintf(\"Beep boop, I am robot %d\", r.ID)\n",
		"    return fmt.Sprintf(\"Beep boop, I am robot %d\", r.ID)\n",
		1,
	)
	require.NotEqual(t, string(mainContent), mainText)

	utilPath := filepath.Join(tmpDir, "util.go")
	utilContent, err := os.ReadFile(utilPath)
	require.NoError(t, err)

	testPath := filepath.Join(tmpDir, "main_test.go")
	testContent, err := os.ReadFile(testPath)
	require.NoError(t, err)

	mainURI := "file://" + mainPath
	mainWSURI, err := workspaceapi.ParseURI(mainURI)
	require.NoError(t, err)

	utilURI := "file://" + utilPath
	testFileURI := "file://" + testPath

	scheme := newTestScheme()

	readyCh := make(chan struct{})
	var ready sync.Once
	callback := &e2eCallback{
		onShowMessage: func(params semanticapi.ShowMessageParams) {
			if strings.Contains(params.Message, "Finished loading packages") {
				ready.Do(func() { close(readyCh) })
			}
		},
		onProgress: readyOnProgress(&ready, readyCh),
	}

	mgr := idelsp.New(
		rootURI, scheme, scheme, &stubPkgManager{bin: goplsBin},
		nil, nil, idelsp.Config{Callback: callback, MaxRetries: 1},
	)

	ctx := context.Background()

	ev := textapi.Event{
		Type:    textapi.EventTypeOpen,
		URI:     mainWSURI,
		Content: mainText,
	}
	assert.False(t, mgr.Handle(ctx, ev))

	utilWSURI, err := workspaceapi.ParseURI(utilURI)
	require.NoError(t, err)
	mgr.Handle(ctx, textapi.Event{
		Type:    textapi.EventTypeOpen,
		URI:     utilWSURI,
		Content: string(utilContent),
	})

	testWSURI, err := workspaceapi.ParseURI(testFileURI)
	require.NoError(t, err)
	mgr.Handle(ctx, textapi.Event{
		Type:    textapi.EventTypeOpen,
		URI:     testWSURI,
		Content: string(testContent),
	})

	waitReady(t, readyCh)
	t.Cleanup(func() { _ = mgr.Close() })

	editor := &mockEditor{
		editorFn: func(
			uri workspaceapi.URI,
		) (textapi.Handler, error) {
			return &mockHandler{uri: uri}, nil
		},
	}
	wm := &mockWindowManager{}
	opener := &mockResourceOpener{}
	notify := &mockNotifications{}
	fs := &mockFileSystem{
		openFileFn: func(path string, flag int, mode os.FileMode) (workspaceapi.File, error) {
			return os.OpenFile(path, flag, mode)
		},
	}
	cfg := DefaultConfig()
	cfg.RootURI = rootURI
	cfg.ScheduleNextTick = syncTick
	cfg.Parser = &mockParser{
		searchFn: func(query string, _ []string) (iterator.Iterator[syntaxapi.Result], error) {
			if strings.Contains(query, "selector_expression") {
				return iterator.FromSlice([]syntaxapi.Result{
					{File: mainWSURI, Text: "fmt", From: term.Coordinates{X: 9, Y: 27}, CaptureName: "pkg"},
					{File: mainWSURI, Text: "Sprintf", From: term.Coordinates{X: 13, Y: 27}, CaptureName: "symbol"},
					{File: mainWSURI, Text: "fmt", From: term.Coordinates{X: 1, Y: 37}, CaptureName: "pkg"},
					{File: mainWSURI, Text: "Println", From: term.Coordinates{X: 5, Y: 37}, CaptureName: "symbol"},
				}), nil
			}
			return iterator.Empty[syntaxapi.Result](), nil
		},
		resolveFn: func(name string, _ syntaxapi.Progress) ([]syntaxapi.Match, error) {
			if name == "fmt.Println" {
				return []syntaxapi.Match{
					{URI: mainWSURI.String(), Pos: term.Coordinates{X: 5, Y: 37}},
				}, nil
			}
			return nil, nil
		},
	}
	router, err := AllHandler(mgr, editor, wm, opener, notify, fs, cfg.Parser, cfg)
	require.NoError(t, err)

	makeCmd := func(name string, args []string, line, char int) textapi.Command {
		cmd := textapi.Command{
			Name:     "lsp",
			Args:     append([]string{name}, args...),
			URI:      mainWSURI,
			Resource: &mockHandler{uri: mainWSURI},
		}
		cmd.Cursor.Content = term.Coordinates{X: char, Y: line}
		cmd.Cursor.Window = term.Coordinates{X: char, Y: line}
		return cmd
	}

	t.Run("references", func(t *testing.T) {
		done := make(chan struct{}, 1)
		var floatingHandler browserapi.Floating
		wm.floatingFn = func(
			h browserapi.Floating,
			_ browserapi.FloatingConfig,
		) (browserapi.Window, error) {
			floatingHandler = h
			select {
			case done <- struct{}{}:
			default:
			}
			return nil, nil
		}
		defer func() {
			wm.floatingFn = nil
		}()

		// Add is at line 34 (0-indexed), col 5.
		cmd := makeCmd("references", nil, 31, 5)
		err := router.HandleCommand(ctx, cmd)
		require.NoError(t, err)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for async references result")
		}
		require.NotNil(t, floatingHandler)

		lh, ok := floatingHandler.(*locationpicker.Picker)
		require.True(t, ok)
		assert.GreaterOrEqual(
			t, len(lh.Entries()), 3,
			"expected at least 3 references to Add",
		)
	})

	t.Run("implementation", func(t *testing.T) {
		// Speaker has a single implementation (Robot), so the
		// handler navigates directly instead of showing a picker.
		done := make(chan struct{}, 1)
		var navigated bool
		editor.setCursorFn = func(_ textapi.Handler, _ term.Coordinates) error {
			navigated = true
			select {
			case done <- struct{}{}:
			default:
			}
			return nil
		}
		defer func() {
			editor.setCursorFn = nil
		}()

		// Speaker is at line 46 (0-indexed), col 5.
		cmd := makeCmd("implementation", nil, 43, 5)
		err := router.HandleCommand(ctx, cmd)
		require.NoError(t, err)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for async implementation result")
		}
		assert.True(t, navigated, "expected direct navigation to single implementation")
	})

	t.Run("format", func(t *testing.T) {
		var (
			cellEditorCalled bool
			cellEditorH      textapi.Handler

			editCalled bool
			editStart  term.Coordinates
			editEnd    term.Coordinates
			editText   string
		)

		editor.cellEditorFn = func(h textapi.Handler) textapi.CellEditor {
			cellEditorCalled = true
			cellEditorH = h
			return &mockCellEditor{
				editFn: func(_ context.Context, s, e term.Coordinates, text string) (
					term.Coordinates, term.Coordinates, string, error,
				) {
					editCalled = true
					editStart = s
					editEnd = e
					editText = text
					return term.Coordinates{}, term.Coordinates{}, "", nil
				},
			}
		}
		defer func() {
			editor.cellEditorFn = nil
		}()

		cmd := makeCmd("format", nil, 0, 0)
		err := router.HandleCommand(ctx, cmd)
		require.NoError(t, err)

		assert.True(t, cellEditorCalled, "CellEditor must be called")
		assert.Equal(t, cmd.Resource, cellEditorH, "CellEditor called with wrong handler")
		assert.True(t, editCalled, "Edit must be called")
		assert.Equal(t, term.Coordinates{X: 0, Y: 54}, editStart)
		assert.Equal(t, term.Coordinates{X: 4, Y: 54}, editEnd)
		assert.Equal(t, "\t", editText)
	})

	// Hover tests use line 29, col 20 of testdata/main.go which
	// is inside the "Greet" method name on:
	//   func (g *Greeter) Greet() string {
	// The two sub-cases are not table-driven because "above cursor"
	// additionally verifies rendered content while "below cursor"
	// only checks offset positioning.
	t.Run("hover", func(t *testing.T) {
		t.Run("above cursor", func(t *testing.T) {
			done := make(chan struct{}, 1)
			var gotFloating browserapi.Floating

			wm.floatingFn = func(h browserapi.Floating, cfg browserapi.FloatingConfig) (
				browserapi.Window, error,
			) {
				gotFloating = h
				select {
				case done <- struct{}{}:
				default:
				}
				return nil, nil
			}
			defer func() { wm.floatingFn = nil }()

			cmd := makeCmd("hover", nil, 26, 20)
			err := router.HandleCommand(ctx, cmd)
			require.NoError(t, err)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for async hover result")
			}
			require.NotNil(t, gotFloating, "Floating must be called")

			w, h := gotFloating.Dimensions()
			gotFloating.Resize(w, h)
			sw := term.NewStringWriter(w, h)
			gotFloating.Draw(sw)
			require.NoError(t, sw.Flush())
			rendered := sw.String()

			assert.Contains(t, rendered, "func (g *Greeter) Greet() string")
		})

		t.Run("below cursor", func(t *testing.T) {
			done := make(chan struct{}, 1)
			wm.floatingFn = func(_ browserapi.Floating, cfg browserapi.FloatingConfig) (
				browserapi.Window, error,
			) {
				select {
				case done <- struct{}{}:
				default:
				}
				return nil, nil
			}
			defer func() { wm.floatingFn = nil }()

			cmd := makeCmd("hover", nil, 26, 20)
			cmd.Cursor.Window.Y = 0
			err := router.HandleCommand(ctx, cmd)
			require.NoError(t, err)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for async hover result")
			}
		})

		t.Run("by qualified symbol name", func(t *testing.T) {
			done := make(chan struct{}, 1)
			var gotFloating browserapi.Floating

			wm.floatingFn = func(h browserapi.Floating, _ browserapi.FloatingConfig) (
				browserapi.Window, error,
			) {
				gotFloating = h
				select {
				case done <- struct{}{}:
				default:
				}
				return nil, nil
			}
			defer func() { wm.floatingFn = nil }()

			// "lsp hover fmt.Println" resolves via tree-sitter
			// and shows hover info for fmt.Println.
			cmd := textapi.Command{
				Name:     "lsp",
				Args:     []string{"hover", "fmt.Println"},
				URI:      mainWSURI,
				Resource: &mockHandler{uri: mainWSURI},
			}
			err := router.HandleCommand(ctx, cmd)
			require.NoError(t, err)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for async symbol resolution")
			}
			require.NotNil(t, gotFloating, "Floating must be called for qualified symbol hover")

			w, h := gotFloating.Dimensions()
			gotFloating.Resize(w, h)
			sw := term.NewStringWriter(w, h)
			gotFloating.Draw(sw)
			require.NoError(t, sw.Flush())
			rendered := sw.String()

			assert.Contains(t, rendered, "Println",
				"hover result should contain Println")
		})
	})
}

func TestE2EDocumentSymbolNormalization(t *testing.T) {
	t.Parallel()
	goplsBin := findGopls(t)
	tmpDir := setupTestWorkspace(t, "../testdata")

	rootURI, err := workspaceapi.ParseURI("file://" + tmpDir)
	require.NoError(t, err)

	// Collect all .go files (excluding _test.go for cleaner symbols).
	type testFile struct {
		path  string
		wsURI workspaceapi.URI
	}
	var files []testFile
	for _, rel := range []string{
		"main.go", "util.go", "mylib/mylib.go",
	} {
		p := filepath.Join(tmpDir, rel)
		uri, err := workspaceapi.ParseURI("file://" + p)
		require.NoError(t, err)
		files = append(files, testFile{path: p, wsURI: uri})
	}

	scheme := newTestScheme()

	readyCh := make(chan struct{})
	var ready sync.Once
	callback := &e2eCallback{
		onShowMessage: func(params semanticapi.ShowMessageParams) {
			if strings.Contains(params.Message, "Finished loading packages") {
				ready.Do(func() { close(readyCh) })
			}
		},
		onProgress: readyOnProgress(&ready, readyCh),
	}

	mgr := idelsp.New(
		rootURI, scheme, scheme, &stubPkgManager{bin: goplsBin},
		nil, nil, idelsp.Config{Callback: callback, MaxRetries: 1},
	)
	ctx := context.Background()

	// Open all files so gopls indexes them.
	for _, f := range files {
		content, err := os.ReadFile(f.path)
		require.NoError(t, err)
		mgr.Handle(ctx, textapi.Event{
			Type:    textapi.EventTypeOpen,
			URI:     f.wsURI,
			Content: string(content),
		})
	}
	waitReady(t, readyCh)
	t.Cleanup(func() { _ = mgr.Close() })

	// For each file, get document symbols, normalize their names,
	// and verify workspace/symbol can resolve them.
	for _, f := range files {
		t.Run(filepath.Base(f.path), func(t *testing.T) {
			docResult, err := mgr.DocumentSymbol(ctx, semanticapi.DocumentSymbolParams{
				TextDocument: TextDocID(f.wsURI),
			})
			require.NoError(t, err)

			// Collect all symbol names from the document symbol response.
			var rawNames []string
			collectRawDocSymbolNames(&rawNames, docResult.DocumentSymbols)
			for _, s := range docResult.SymbolInformation {
				rawNames = append(rawNames, s.Name)
			}
			require.NotEmpty(t, rawNames, "expected symbols in %s", f.path)

			for _, raw := range rawNames {
				normalized := normalizeMethodName(raw)

				t.Run(normalized, func(t *testing.T) {
					// Query workspace/symbol with the normalized name.
					syms, err := mgr.WorkspaceSymbol(ctx, semanticapi.WorkspaceSymbolParams{
						Query: normalized,
					})
					require.NoError(t, err)

					// At least one result must match exactly.
					var found bool
					for _, s := range syms {
						if s.Name == normalized {
							found = true
							break
						}
					}
					assert.True(t, found,
						"workspace/symbol should find %q (raw from documentSymbol: %q); got %d results",
						normalized, raw, len(syms))
				})
			}
		})
	}
}

// collectRawDocSymbolNames collects all names from a DocumentSymbol
// tree without normalization (for test verification).
func collectRawDocSymbolNames(names *[]string, syms []semanticapi.DocumentSymbol) {
	for _, s := range syms {
		*names = append(*names, s.Name)
		collectRawDocSymbolNames(names, s.Children)
	}
}

func TestE2ECompleteNormalization(t *testing.T) {
	t.Parallel()
	goplsBin := findGopls(t)
	tmpDir := setupTestWorkspace(t, "../testdata")

	rootURI, err := workspaceapi.ParseURI("file://" + tmpDir)
	require.NoError(t, err)

	// We open both main.go and util.go so gopls indexes them.
	// util.go has Counter with pointer- and value-receiver methods.
	// main.go has Greeter with a pointer-receiver method.
	type testFile struct {
		rel   string
		wsURI workspaceapi.URI
	}
	var files []testFile
	for _, rel := range []string{"main.go", "util.go"} {
		p := filepath.Join(tmpDir, rel)
		uri, parseErr := workspaceapi.ParseURI("file://" + p)
		require.NoError(t, parseErr)
		files = append(files, testFile{rel: rel, wsURI: uri})
	}

	scheme := newTestScheme()

	readyCh := make(chan struct{})
	var ready sync.Once
	callback := &e2eCallback{
		onShowMessage: func(params semanticapi.ShowMessageParams) {
			if strings.Contains(params.Message, "Finished loading packages") {
				ready.Do(func() { close(readyCh) })
			}
		},
		onProgress: readyOnProgress(&ready, readyCh),
	}

	mgr := idelsp.New(
		rootURI, scheme, scheme, &stubPkgManager{bin: goplsBin},
		nil, nil, idelsp.Config{Callback: callback, MaxRetries: 1},
	)
	ctx := context.Background()

	for _, f := range files {
		content, readErr := os.ReadFile(filepath.Join(tmpDir, f.rel))
		require.NoError(t, readErr)
		mgr.Handle(ctx, textapi.Event{
			Type:    textapi.EventTypeOpen,
			URI:     f.wsURI,
			Content: string(content),
		})
	}
	waitReady(t, readyCh)
	t.Cleanup(func() { _ = mgr.Close() })

	// Get document symbols for each file and verify that every
	// returned name is normalized and resolvable.
	for _, f := range files {
		t.Run(f.rel, func(t *testing.T) {
			result, err := mgr.DocumentSymbol(ctx, semanticapi.DocumentSymbolParams{
				TextDocument: TextDocID(f.wsURI),
			})
			require.NoError(t, err)

			var names []string
			var collect func([]semanticapi.DocumentSymbol)
			collect = func(syms []semanticapi.DocumentSymbol) {
				for _, s := range syms {
					names = append(names, normalizeMethodName(s.Name))
					collect(s.Children)
				}
			}
			collect(result.DocumentSymbols)
			for _, s := range result.SymbolInformation {
				names = append(names, normalizeMethodName(s.Name))
			}
			require.NotEmpty(t, names, "expected completions for %s", f.rel)

			for _, name := range names {
				// No gopls receiver-method syntax should survive.
				assert.NotContains(t, name, "(*",
					"completion %q still has pointer-receiver syntax", name)
				assert.NotRegexp(t, `^\(`, name,
					"completion %q still has value-receiver paren prefix", name)
			}
		})
	}
}

func TestE2ESignatureHelpAutoTrigger(t *testing.T) {
	t.Parallel()
	goplsBin := findGopls(t)
	tmpDir := setupTestWorkspace(t, "../testdata")

	rootURI, err := workspaceapi.ParseURI("file://" + tmpDir)
	require.NoError(t, err)

	mainPath := filepath.Join(tmpDir, "main.go")
	mainContent, err := os.ReadFile(mainPath)
	require.NoError(t, err)

	mainURI := "file://" + mainPath
	mainWSURI, err := workspaceapi.ParseURI(mainURI)
	require.NoError(t, err)

	scheme := newTestScheme()

	readyCh := make(chan struct{})
	var ready sync.Once
	callback := &e2eCallback{
		onShowMessage: func(params semanticapi.ShowMessageParams) {
			if strings.Contains(params.Message, "Finished loading packages") {
				ready.Do(func() { close(readyCh) })
			}
		},
		onProgress: readyOnProgress(&ready, readyCh),
	}

	mgr := idelsp.New(
		rootURI, scheme, scheme, &stubPkgManager{bin: goplsBin},
		nil, nil, idelsp.Config{Callback: callback, MaxRetries: 1},
	)
	ctx := context.Background()
	t.Cleanup(func() { _ = mgr.Close() })

	// Initialize the go server explicitly to get capabilities.
	initOpts, err := json.Marshal(map[string]any{
		"langID":  "go",
		"command": goplsBin + " serve",
	})
	require.NoError(t, err)

	capabilities, err := json.Marshal(map[string]any{
		"textDocument": map[string]any{
			"signatureHelp": map[string]any{},
			"completion":    map[string]any{},
			"hover":         map[string]any{},
		},
		"window": map[string]any{
			"workDoneProgress": true,
		},
	})
	require.NoError(t, err)

	initResult, err := mgr.Initialize(ctx, semanticapi.InitializeParams{
		RootURI:           "file://" + tmpDir,
		Capabilities:      json.RawMessage(capabilities),
		InitializeOptions: json.RawMessage(initOpts),
	})
	require.NoError(t, err)

	// Verify gopls advertises trigger characters.
	require.NotNil(t, initResult.Capabilities.SignatureHelpProvider,
		"gopls should advertise signature help provider")
	triggerChars := initResult.Capabilities.SignatureHelpProvider.TriggerCharacters
	assert.Contains(t, triggerChars, "(", "trigger chars should include '('")
	assert.Contains(t, triggerChars, ",", "trigger chars should include ','")

	// Open the test file so gopls knows about it.
	mgr.Handle(ctx, textapi.Event{
		Type:    textapi.EventTypeOpen,
		URI:     mainWSURI,
		Content: string(mainContent),
	})
	waitReady(t, readyCh)

	// Set up the editor mock to capture the subscribed event handler.
	var capturedHandler textapi.EventHandler
	var mu sync.Mutex
	var locationCalls []textapi.LocationList
	editor := &mockEditor{
		editorFn: func(uri workspaceapi.URI) (textapi.Handler, error) {
			return &mockHandler{uri: uri}, nil
		},
		subscribeEventsFn: func(
			_ []textapi.EventType, h textapi.EventHandler,
		) error {
			capturedHandler = h
			return nil
		},
		setLocationListFn: func(_ textapi.Handler, _ textapi.LocationPriority, _ string, list textapi.LocationList) error {
			mu.Lock()
			locationCalls = append(locationCalls, list)
			mu.Unlock()
			return nil
		},
	}

	wm := &mockWindowManager{}

	cfg := DefaultConfig()
	cfg.RootURI = rootURI
	cfg.ScheduleNextTick = syncTick
	cfg.SignatureHelp.TriggerCharacters = triggerChars

	_, err = AllHandler(mgr, editor, wm, &mockResourceOpener{},
		&mockNotifications{}, &mockFileSystem{
			openFileFn: func(path string, flag int, mode os.FileMode) (workspaceapi.File, error) {
				return os.OpenFile(path, flag, mode)
			},
		}, &mockParser{}, cfg)
	require.NoError(t, err)
	require.NotNil(t, capturedHandler, "SubscribeEvents should have been called")

	// Simulate typing "(" after "Add" on line 41 (0-indexed),
	// where `fmt.Println(Add(1, 2))` is.
	// The "(" after "Add" is at column 16 (0-indexed: col 16).

	// Send the edit event to gopls via mgr.Handle.
	editEv := textapi.Event{
		Type:     textapi.EventTypeEdit,
		URI:      mainWSURI,
		Resource: &mockHandler{uri: mainWSURI},
		Start:    term.Coordinates{X: 16, Y: 38},
		End:      term.Coordinates{X: 16, Y: 38},
		From:     term.Coordinates{X: 16, Y: 38},
		To:       term.Coordinates{X: 17, Y: 38},
		Content:  "(",
	}
	mgr.Handle(ctx, editEv)

	// Give gopls a moment to process the didChange.
	time.Sleep(500 * time.Millisecond)

	// Fire the same event through the captured handler.
	done := capturedHandler.Handle(ctx, editEv)
	assert.False(t, done, "handler should not signal done")

	// Wait for the async fetch to set the location.
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, l := range locationCalls {
			if l != nil {
				return true
			}
		}
		return false
	}, 5*time.Second, 50*time.Millisecond, "SetLocationList should be called with a non-nil list")

	// Verify the location message contains the Add signature with bold markers.
	mu.Lock()
	var gotLoc textapi.Location
	for _, l := range locationCalls {
		if l != nil {
			gotLoc, _ = l.Current()
			break
		}
	}
	mu.Unlock()
	assert.Contains(t, gotLoc.Message, "**", "message should contain bold markers")
	assert.Contains(t, gotLoc.Message, "Add(", "message should contain the Add signature")

	// Fire a cursor event to consume the editSeen flag (in a real
	// editor the cursor moves as a result of the edit, producing
	// this first cursor event which should NOT clear the help).
	capturedHandler.Handle(ctx, textapi.Event{
		Type: textapi.EventTypeCursor,
		URI:  mainWSURI,
		From: term.Coordinates{X: 17, Y: 38},
	})

	// Fire a second cursor event (simulating a deliberate cursor
	// navigation) and verify the location list is cleared.
	capturedHandler.Handle(ctx, textapi.Event{
		Type: textapi.EventTypeCursor,
		URI:  mainWSURI,
		From: term.Coordinates{X: 18, Y: 38},
	})

	mu.Lock()
	lastCall := locationCalls[len(locationCalls)-1]
	mu.Unlock()
	assert.Nil(t, lastCall, "cursor event should clear the location list")
}

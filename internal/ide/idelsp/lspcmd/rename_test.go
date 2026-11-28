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
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	idelsp "unstable.build/rune/internal/ide/idelsp"
	"unstable.build/rune/internal/ide/idelsp/goplstest"
)

func TestRenameHandler(t *testing.T) {
	t.Parallel()
	editAt := func(line uint32) []semanticapi.TextEdit {
		return []semanticapi.TextEdit{{
			Range: semanticapi.Range{
				Start: semanticapi.Position{Line: line, Character: 0},
				End:   semanticapi.Position{Line: line, Character: 3},
			},
			NewText: "new",
		}}
	}
	twoFiles := &semanticapi.WorkspaceEdit{Changes: map[string][]semanticapi.TextEdit{
		"file:///a.go": editAt(1),
		"file:///b.go": editAt(2),
	}}
	for _, tt := range []struct {
		name      string
		keys      string
		edit      *semanticapi.WorkspaceEdit
		renameErr error
		// wantName is the name the server is asked to rename to, none
		// when it is not asked.
		wantName string
		// wantEdits are the files edited, by URI.
		wantEdits []string
		// wantOpen is whether the prompt stays open.
		wantOpen bool
	}{
		{name: "esc dismisses", keys: "<esc>"},
		{name: "ctrl-c dismisses", keys: "<c-c>"},
		{
			name: "enter keeps the current name", keys: "<enter>", edit: twoFiles,
			wantName: "old", wantEdits: []string{"file:///a.go", "file:///b.go"},
		},
		{
			name: "edited name", keys: "<backspace><backspace><backspace>new<enter>", edit: twoFiles,
			wantName: "new", wantEdits: []string{"file:///a.go", "file:///b.go"},
		},
		{
			name: "by option", keys: "<tab><enter>", edit: twoFiles,
			wantName: "old", wantEdits: []string{"file:///a.go", "file:///b.go"},
		},
		{name: "empty name is required", keys: "<backspace><backspace><backspace><enter>", wantOpen: true},
		{name: "server error", keys: "<enter>", renameErr: errors.New("server error"), wantName: "old"},
		{name: "no edit", keys: "<enter>", wantName: "old"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var params []semanticapi.RenameParams
			lsp := &mockLSP{
				prepareRenameFn: func(
					_ context.Context, _ semanticapi.PrepareRenameParams,
				) (*semanticapi.PrepareRenameResult, error) {
					return &semanticapi.PrepareRenameResult{Placeholder: "old"}, nil
				},
				renameFn: func(
					_ context.Context, p semanticapi.RenameParams,
				) (*semanticapi.WorkspaceEdit, error) {
					params = append(params, p)
					return tt.edit, tt.renameErr
				},
			}
			var edited []string
			editor := &mockEditor{
				editorFn: func(uri workspaceapi.URI) (textapi.Handler, error) {
					return &mockHandler{uri: uri}, nil
				},
				cellEditorFn: func(h textapi.Handler) textapi.CellEditor {
					return &mockCellEditor{
						editFn: func(
							_ context.Context, s, e term.Coordinates, text string,
						) (term.Coordinates, term.Coordinates, string, error) {
							edited = append(edited, h.(*mockHandler).uri.String())
							return s, e, text, nil
						},
					}
				},
			}
			prompter := &mockPrompter{}
			uri := testURI(t, "file:///test.go")
			cmd := textapi.Command{Name: "rename", URI: uri, Resource: &mockHandler{uri: uri}}
			cmd.Cursor.Content = term.Coordinates{X: 4, Y: 5}

			h := RenameHandler(lsp, editor, prompter, nil, nil)
			require.NoError(t, h.HandleCommand(context.Background(), cmd))
			require.Len(t, prompter.configs, 1)
			assert.Equal(t, browserapi.PromptConfig{
				Message: "Rename `old` to:",
				Options: []string{"Rename"},
				Fields: []browserapi.Field{
					{Answer: "old", Placeholder: "New name", Mandatory: true},
				},
			}, prompter.configs[0])

			exited := prompter.keys(t, tt.keys)
			assert.Equal(t, tt.wantOpen, !exited, "whether the prompt is open")
			if tt.wantName == "" {
				assert.Empty(t, params, "the server was asked to rename")
			} else {
				require.Len(t, params, 1)
				assert.Equal(t, tt.wantName, params[0].NewName)
				assert.Equal(t, semanticapi.Position{Line: 5, Character: 4}, params[0].Position)
				assert.Equal(t, TextDocID(uri), params[0].TextDocument)
			}
			slices.Sort(edited)
			assert.Equal(t, tt.wantEdits, edited)
		})
	}
}

func TestApplyEditsOpensUnopenedFile(t *testing.T) {
	t.Parallel()

	opened := map[string]bool{}
	opener := &mockResourceOpener{
		openFn: func(uri workspaceapi.URI) (browserapi.Handler, error) {
			opened[uri.String()] = true
			return nil, nil
		},
	}

	editor := &mockEditor{
		editorFn: func(uri workspaceapi.URI) (textapi.Handler, error) {
			if !opened[uri.String()] {
				return nil, errors.New("not open")
			}
			return &mockHandler{uri: uri}, nil
		},
		cellEditorFn: func(_ textapi.Handler) textapi.CellEditor {
			return &mockCellEditor{
				editFn: func(
					_ context.Context,
					s, e term.Coordinates,
					text string,
				) (term.Coordinates, term.Coordinates, string, error) {
					return s, e, text, nil
				},
			}
		},
	}

	edit := &semanticapi.WorkspaceEdit{
		Changes: map[string][]semanticapi.TextEdit{
			"file:///unopened.go": {
				{
					Range: semanticapi.Range{
						Start: semanticapi.Position{Line: 1, Character: 0},
						End:   semanticapi.Position{Line: 1, Character: 3},
					},
					NewText: "bar",
				},
			},
		},
	}

	err := ApplyWorkspaceEdit(context.Background(), editor, opener, workspaceapi.URI{}, edit, nil)
	require.NoError(t, err)
	assert.True(t, opened["file:///unopened.go"],
		"opener should be called for files not already open")
}

func TestRenameHandlerWithoutTheCurrentName(t *testing.T) {
	t.Parallel()

	lsp := &mockLSP{
		prepareRenameFn: func(
			_ context.Context, _ semanticapi.PrepareRenameParams,
		) (*semanticapi.PrepareRenameResult, error) {
			return &semanticapi.PrepareRenameResult{IsRangeOnly: true}, nil
		},
	}
	prompter := &mockPrompter{}
	uri := testURI(t, "file:///test.go")
	h := RenameHandler(lsp, &mockEditor{}, prompter, nil, nil)
	require.NoError(t, h.HandleCommand(context.Background(), textapi.Command{
		Name: "rename", URI: uri, Resource: &mockHandler{uri: uri},
	}))
	require.Len(t, prompter.configs, 1)
	assert.Equal(t, browserapi.PromptConfig{
		Message: "Rename the symbol to:",
		Options: []string{"Rename"},
		Fields:  []browserapi.Field{{Placeholder: "New name", Mandatory: true}},
	}, prompter.configs[0])
}

func TestRenameHandlerPrepareRenameNil(t *testing.T) {
	t.Parallel()

	lsp := &mockLSP{
		prepareRenameFn: func(
			_ context.Context, _ semanticapi.PrepareRenameParams,
		) (*semanticapi.PrepareRenameResult, error) {
			return nil, nil
		},
	}

	h := RenameHandler(lsp, &mockEditor{}, &mockPrompter{}, nil, nil)
	uri := testURI(t, "file:///test.go")
	err := h.HandleCommand(context.Background(), textapi.Command{
		Name:     "rename",
		URI:      uri,
		Resource: &mockHandler{uri: uri},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not available")
}

func TestRenameHandlerPrepareRenameError(t *testing.T) {
	t.Parallel()

	lsp := &mockLSP{
		prepareRenameFn: func(
			_ context.Context, _ semanticapi.PrepareRenameParams,
		) (*semanticapi.PrepareRenameResult, error) {
			return nil, errors.New("not supported")
		},
	}

	h := RenameHandler(lsp, &mockEditor{}, &mockPrompter{}, nil, nil)
	uri := testURI(t, "file:///test.go")
	err := h.HandleCommand(context.Background(), textapi.Command{
		Name:     "rename",
		URI:      uri,
		Resource: &mockHandler{uri: uri},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "prepare rename")
}

func TestApplyWorkspaceEditDocumentChanges(t *testing.T) {
	t.Parallel()

	type editCall struct {
		uri  string
		text string
	}

	var calls []editCall
	editor := &mockEditor{
		editorFn: func(uri workspaceapi.URI) (textapi.Handler, error) {
			return &mockHandler{uri: uri}, nil
		},
		cellEditorFn: func(h textapi.Handler) textapi.CellEditor {
			rh := h.(*mockHandler)
			return &mockCellEditor{
				editFn: func(
					_ context.Context,
					s, e term.Coordinates,
					text string,
				) (term.Coordinates, term.Coordinates, string, error) {
					calls = append(calls, editCall{
						uri:  rh.uri.String(),
						text: text,
					})
					return s, e, text, nil
				},
			}
		},
	}

	edit := &semanticapi.WorkspaceEdit{
		// Both are set; DocumentChanges must be preferred.
		Changes: map[string][]semanticapi.TextEdit{
			"file:///should-be-ignored.go": {
				{
					Range: semanticapi.Range{
						Start: semanticapi.Position{Line: 0, Character: 0},
						End:   semanticapi.Position{Line: 0, Character: 3},
					},
					NewText: "WRONG",
				},
			},
		},
		DocumentChanges: []semanticapi.DocumentChange{
			{
				TextDocumentEdit: &semanticapi.TextDocumentEdit{
					TextDocument: semanticapi.VersionedTextDocumentIdentifier{
						URI: "file:///correct.go",
					},
					Edits: []semanticapi.TextEdit{
						{
							Range: semanticapi.Range{
								Start: semanticapi.Position{Line: 1, Character: 0},
								End:   semanticapi.Position{Line: 1, Character: 3},
							},
							NewText: "bar",
						},
					},
				},
			},
		},
	}

	err := ApplyWorkspaceEdit(context.Background(), editor, nil, workspaceapi.URI{}, edit, nil)
	require.NoError(t, err)
	require.Len(t, calls, 1, "only DocumentChanges edits should be applied")
	assert.Equal(t, "file:///correct.go", calls[0].uri)
	assert.Equal(t, "bar", calls[0].text)
}

func TestApplyWorkspaceEdit(t *testing.T) {
	t.Parallel()

	type editCall struct {
		uri   string
		start term.Coordinates
		end   term.Coordinates
		text  string
	}

	var calls []editCall
	editor := &mockEditor{
		editorFn: func(uri workspaceapi.URI) (textapi.Handler, error) {
			return &mockHandler{uri: uri}, nil
		},
		cellEditorFn: func(h textapi.Handler) textapi.CellEditor {
			rh := h.(*mockHandler)
			return &mockCellEditor{
				editFn: func(
					_ context.Context,
					s, e term.Coordinates,
					text string,
				) (term.Coordinates, term.Coordinates, string, error) {
					calls = append(calls, editCall{
						uri:   rh.uri.String(),
						start: s, end: e,
						text: text,
					})
					return s, e, text, nil
				},
			}
		},
	}

	edit := &semanticapi.WorkspaceEdit{
		Changes: map[string][]semanticapi.TextEdit{
			"file:///foo.go": {
				{
					Range: semanticapi.Range{
						Start: semanticapi.Position{Line: 10, Character: 5},
						End:   semanticapi.Position{Line: 10, Character: 8},
					},
					NewText: "baz",
				},
			},
		},
	}

	err := ApplyWorkspaceEdit(context.Background(), editor, nil, workspaceapi.URI{}, edit, nil)
	require.NoError(t, err)
	require.Len(t, calls, 1)
	assert.Equal(t, "file:///foo.go", calls[0].uri)
	assert.Equal(t, "baz", calls[0].text)
	assert.Equal(t, term.Coordinates{X: 5, Y: 10}, calls[0].start)
	assert.Equal(t, term.Coordinates{X: 8, Y: 10}, calls[0].end)
}

func TestE2ERename(t *testing.T) {
	t.Parallel()
	goplsBin := goplstest.Find(t)
	tmpDir := setupTestWorkspace(t, "../testdata")

	rootURI, err := workspaceapi.ParseURI("file://" + tmpDir)
	require.NoError(t, err)

	mainPath := filepath.Join(tmpDir, "main.go")
	mainContent, err := os.ReadFile(mainPath)
	require.NoError(t, err)

	utilPath := filepath.Join(tmpDir, "util.go")
	utilContent, err := os.ReadFile(utilPath)
	require.NoError(t, err)

	testPath := filepath.Join(tmpDir, "main_test.go")
	testContent, err := os.ReadFile(testPath)
	require.NoError(t, err)

	mainWSURI, err := workspaceapi.ParseURI("file://" + mainPath)
	require.NoError(t, err)
	utilWSURI, err := workspaceapi.ParseURI("file://" + utilPath)
	require.NoError(t, err)
	testWSURI, err := workspaceapi.ParseURI("file://" + testPath)
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

	mgr.Handle(ctx, textapi.Event{
		Type: textapi.EventTypeOpen, URI: mainWSURI,
		Content: string(mainContent),
	})
	mgr.Handle(ctx, textapi.Event{
		Type: textapi.EventTypeOpen, URI: utilWSURI,
		Content: string(utilContent),
	})
	mgr.Handle(ctx, textapi.Event{
		Type: textapi.EventTypeOpen, URI: testWSURI,
		Content: string(testContent),
	})
	waitReady(t, readyCh)
	t.Cleanup(func() { _ = mgr.Close() })

	// Track every CellEditor.Edit call across all files.
	type editCall struct {
		uri  string
		text string
	}
	var mu sync.Mutex
	var edits []editCall

	editor := &mockEditor{
		editorFn: func(uri workspaceapi.URI) (textapi.Handler, error) {
			return &mockHandler{uri: uri}, nil
		},
		cellEditorFn: func(h textapi.Handler) textapi.CellEditor {
			rh := h.(*mockHandler)
			return &mockCellEditor{
				editFn: func(
					_ context.Context,
					s, e term.Coordinates, text string,
				) (term.Coordinates, term.Coordinates, string, error) {
					mu.Lock()
					edits = append(edits, editCall{
						uri:  rh.uri.String(),
						text: text,
					})
					mu.Unlock()
					// Simulate Rune firing EventTypeEdit back to Manager.
					mgr.Handle(context.Background(), textapi.Event{
						Type:    textapi.EventTypeEdit,
						URI:     rh.uri,
						Content: text,
						Start:   s,
						End:     e,
					})
					return s, e, text, nil
				},
			}
		},
	}

	prompter := &mockPrompter{}

	cfg := DefaultConfig()
	cfg.RootURI = rootURI
	cfg.ScheduleNextTick = syncTick
	router, err := AllHandler(
		mgr, editor, &mockWindowManager{}, prompter, &mockResourceOpener{},
		&mockNotifications{}, &mockFileSystem{
			openFileFn: func(
				path string, flag int, mode os.FileMode,
			) (workspaceapi.File, error) {
				return os.OpenFile(path, flag, mode)
			},
		}, &mockParser{}, cfg,
	)
	require.NoError(t, err)

	// "Add" is at line 31 (0-indexed), col 5 in main.go.
	cmd := textapi.Command{
		Name:     "lsp",
		Args:     []string{"rename"},
		URI:      mainWSURI,
		Resource: &mockHandler{uri: mainWSURI},
	}
	cmd.Cursor.Content = term.Coordinates{X: 5, Y: 31}
	cmd.Cursor.Window = term.Coordinates{X: 5, Y: 31}

	err = router.HandleCommand(ctx, cmd)
	require.NoError(t, err)
	require.Len(t, prompter.configs, 1, "the rename prompt must be shown")
	require.Len(t, prompter.configs[0].Fields, 1)
	assert.Equal(t, "Add", prompter.configs[0].Fields[0].Answer,
		"the new name should start from the current name")

	assert.True(t, prompter.keys(t, "<backspace><backspace><backspace>Sum<enter>"))

	// Verify edits were applied.
	mu.Lock()
	got := make([]editCall, len(edits))
	copy(got, edits)
	mu.Unlock()

	require.NotEmpty(t, got, "at least one edit must be applied")

	for _, e := range got {
		assert.Equal(t, "Sum", e.text, "edit in %s", e.uri)
	}

	mainURI := "file://" + mainPath
	testFileURI := "file://" + testPath
	var hasMain, hasTest bool
	for _, e := range got {
		switch e.uri {
		case mainURI:
			hasMain = true
		case testFileURI:
			hasTest = true
		}
	}
	assert.True(t, hasMain, "main.go must have edits")
	assert.True(t, hasTest, "main_test.go must have edits")

	// Verify the LSP server's view is up-to-date by querying
	// workspace symbols. The didChange notification is synchronous
	// (Manager.DidChange forwards directly), but gopls may need
	// a moment to re-index.
	require.Eventually(t, func() bool {
		syms, err := mgr.WorkspaceSymbol(ctx, semanticapi.WorkspaceSymbolParams{
			Query: "Sum",
		})
		if err != nil {
			return false
		}
		for _, s := range syms {
			if s.Name == "Sum" {
				return true
			}
		}
		return false
	}, 5*time.Second, 100*time.Millisecond,
		"gopls should know about 'Sum' after rename")

	// The workspace "Add" query also returns stdlib symbols
	// (math/bits.Add, etc.), so filter to the test workspace.
	require.Eventually(t, func() bool {
		syms, err := mgr.WorkspaceSymbol(ctx, semanticapi.WorkspaceSymbolParams{
			Query: "Add",
		})
		if err != nil {
			return false
		}
		for _, s := range syms {
			if s.Name == "Add" && strings.HasPrefix(s.Location.URI, "file://"+tmpDir) {
				return false
			}
		}
		return true
	}, 5*time.Second, 100*time.Millisecond,
		"gopls should no longer know about 'Add' in the workspace after rename")
}

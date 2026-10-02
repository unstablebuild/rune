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
	"log/slog"
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
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/handler/inputbox"
	"github.com/unstablebuild/rune-go-sdk/term"
	idelsp "unstable.build/rune/internal/ide/idelsp"
)

func newTestRenameFloating(
	t *testing.T, text string, lsp *mockLSP, editor *mockEditor,
	notify browserapi.Notifications,
) *renameFloatingHandler {
	t.Helper()
	ib := inputbox.New(
		inputbox.WithPrompt(""),
		inputbox.WithText(text),
	)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &renameFloatingHandler{
		ib:       ib,
		lsp:      lsp,
		editor:   editor,
		notify:   notify,
		uri:      testURI(t, "file:///test.go"),
		position: semanticapi.Position{Line: 5, Character: 4},
		ctx:      ctx,
		cancel:   cancel,
		log:      slog.Default(),
	}
}

func TestRenameFloatingHandlerEsc(t *testing.T) {
	t.Parallel()

	renameCalled := false
	lsp := &mockLSP{
		renameFn: func(
			_ context.Context, _ semanticapi.RenameParams,
		) (*semanticapi.WorkspaceEdit, error) {
			renameCalled = true
			return nil, nil
		},
	}

	h := newTestRenameFloating(t, "oldName", lsp, &mockEditor{}, nil)
	exit, handled := h.Handle(term.Event{
		Type: term.EventKey, Key: term.KeyEsc,
	})

	assert.True(t, exit)
	assert.True(t, handled)
	assert.False(t, renameCalled, "Rename must not be called on Esc")
}

func TestRenameFloatingHandlerEnter(t *testing.T) {
	t.Parallel()

	var gotParams semanticapi.RenameParams
	lsp := &mockLSP{
		renameFn: func(
			_ context.Context, params semanticapi.RenameParams,
		) (*semanticapi.WorkspaceEdit, error) {
			gotParams = params
			return &semanticapi.WorkspaceEdit{
				Changes: map[string][]semanticapi.TextEdit{
					"file:///test.go": {
						{
							Range: semanticapi.Range{
								Start: semanticapi.Position{Line: 5, Character: 4},
								End:   semanticapi.Position{Line: 5, Character: 11},
							},
							NewText: "newName",
						},
					},
				},
			}, nil
		},
	}

	var editCalls []string
	editor := &mockEditor{
		editorFn: func(uri workspaceapi.URI) (textapi.Handler, error) {
			return &mockHandler{uri: uri}, nil
		},
		cellEditorFn: func(_ textapi.Handler) textapi.CellEditor {
			return &mockCellEditor{
				editFn: func(
					_ context.Context,
					s, e term.Coordinates,
					text string,
				) (term.Coordinates, term.Coordinates, string, error) {
					editCalls = append(editCalls, text)
					return s, e, text, nil
				},
			}
		},
	}

	h := newTestRenameFloating(t, "newName", lsp, editor, nil)
	exit, handled := h.Handle(term.Event{
		Type: term.EventKey, Key: term.KeyEnter,
	})

	assert.True(t, exit)
	assert.True(t, handled)
	assert.Equal(t, "newName", gotParams.NewName)
	assert.Equal(t, uint32(5), gotParams.Position.Line)
	assert.Equal(t, uint32(4), gotParams.Position.Character)
	require.Len(t, editCalls, 1)
	assert.Equal(t, "newName", editCalls[0])
}

func TestRenameFloatingHandlerRenameError(t *testing.T) {
	t.Parallel()

	lsp := &mockLSP{
		renameFn: func(
			_ context.Context, _ semanticapi.RenameParams,
		) (*semanticapi.WorkspaceEdit, error) {
			return nil, errors.New("renaming \"Foo\" to \"Bar\" failed: Bar already declared at")
		},
	}

	notify := &recordingNotifications{}
	h := newTestRenameFloating(t, "Bar", lsp, &mockEditor{}, notify)
	exit, handled := h.Handle(term.Event{
		Type: term.EventKey, Key: term.KeyEnter,
	})

	assert.True(t, exit, "should exit even on error")
	assert.True(t, handled)
	notifies, _ := notify.snapshot()
	require.Len(t, notifies, 1)
	assert.Equal(t, browserapi.LevelError, notifies[0].level)
	assert.Contains(t, notifies[0].msg, "rename:")
	assert.Contains(t, notifies[0].msg, "already declared")
}

func TestRenameFloatingHandlerNilEdit(t *testing.T) {
	t.Parallel()

	lsp := &mockLSP{
		renameFn: func(
			_ context.Context, _ semanticapi.RenameParams,
		) (*semanticapi.WorkspaceEdit, error) {
			return nil, nil
		},
	}

	notify := &recordingNotifications{}
	h := newTestRenameFloating(t, "newName", lsp, &mockEditor{}, notify)
	exit, handled := h.Handle(term.Event{
		Type: term.EventKey, Key: term.KeyEnter,
	})

	assert.True(t, exit)
	assert.True(t, handled)
	notifies, _ := notify.snapshot()
	require.Len(t, notifies, 1)
	assert.Equal(t, browserapi.LevelError, notifies[0].level)
	assert.Contains(t, notifies[0].msg, "no edits returned")
}

func TestRenameFloatingHandlerEmptyEdit(t *testing.T) {
	t.Parallel()

	lsp := &mockLSP{
		renameFn: func(
			_ context.Context, _ semanticapi.RenameParams,
		) (*semanticapi.WorkspaceEdit, error) {
			return &semanticapi.WorkspaceEdit{
				Changes: map[string][]semanticapi.TextEdit{},
			}, nil
		},
	}

	notify := &recordingNotifications{}
	h := newTestRenameFloating(t, "collide", lsp, &mockEditor{}, notify)
	exit, handled := h.Handle(term.Event{
		Type: term.EventKey, Key: term.KeyEnter,
	})

	assert.True(t, exit)
	assert.True(t, handled)
	notifies, _ := notify.snapshot()
	require.Len(t, notifies, 1)
	assert.Equal(t, browserapi.LevelError, notifies[0].level)
	assert.Contains(t, notifies[0].msg, "no edits returned for \"collide\"")
}

func TestRenameFloatingHandlerApplyError(t *testing.T) {
	t.Parallel()

	lsp := &mockLSP{
		renameFn: func(
			_ context.Context, _ semanticapi.RenameParams,
		) (*semanticapi.WorkspaceEdit, error) {
			return &semanticapi.WorkspaceEdit{
				Changes: map[string][]semanticapi.TextEdit{
					"file:///missing.go": {
						{
							Range: semanticapi.Range{
								Start: semanticapi.Position{Line: 0, Character: 0},
								End:   semanticapi.Position{Line: 0, Character: 3},
							},
							NewText: "bar",
						},
					},
				},
			}, nil
		},
	}

	editor := &mockEditor{
		editorFn: func(uri workspaceapi.URI) (textapi.Handler, error) {
			return nil, errors.New("not open")
		},
	}

	notify := &recordingNotifications{}
	h := newTestRenameFloating(t, "bar", lsp, editor, notify)
	exit, handled := h.Handle(term.Event{
		Type: term.EventKey, Key: term.KeyEnter,
	})

	assert.True(t, exit)
	assert.True(t, handled)
	notifies, _ := notify.snapshot()
	require.Len(t, notifies, 1)
	assert.Equal(t, browserapi.LevelError, notifies[0].level)
	assert.Contains(t, notifies[0].msg, "rename:")
}

func TestRenameFloatingHandlerTyping(t *testing.T) {
	t.Parallel()

	h := newTestRenameFloating(t, "", &mockLSP{}, &mockEditor{}, nil)

	exit, handled := h.Handle(term.Event{
		Type: term.EventKey, Ch: 'a',
	})
	assert.False(t, exit, "typing should not exit")
	assert.True(t, handled)
}

func TestRenameFloatingHandlerDimensions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		text     string
		wantMinW int
	}{
		{
			name:     "short text uses minimum width",
			text:     "x",
			wantMinW: 30,
		},
		{
			name:     "long text expands width",
			text:     "aVeryLongSymbolNameThatExceedsMinimum",
			wantMinW: 30,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			h := newTestRenameFloating(
				t, test.text, &mockLSP{}, &mockEditor{}, nil,
			)
			w, ht := h.Dimensions()
			assert.GreaterOrEqual(t, w, test.wantMinW)
			assert.Equal(t, 1, ht)
		})
	}
}

func TestRenameFloatingHandlerCursor(t *testing.T) {
	t.Parallel()

	h := newTestRenameFloating(t, "test", &mockLSP{}, &mockEditor{}, nil)
	h.Resize(30, 1)

	_, _, visible := h.Cursor()
	assert.True(t, visible, "inputbox cursor should be visible")
}

func TestRenameFloatingHandlerMultiFileEdit(t *testing.T) {
	t.Parallel()

	lsp := &mockLSP{
		renameFn: func(
			_ context.Context, _ semanticapi.RenameParams,
		) (*semanticapi.WorkspaceEdit, error) {
			return &semanticapi.WorkspaceEdit{
				Changes: map[string][]semanticapi.TextEdit{
					"file:///a.go": {
						{
							Range: semanticapi.Range{
								Start: semanticapi.Position{Line: 1, Character: 0},
								End:   semanticapi.Position{Line: 1, Character: 3},
							},
							NewText: "bar",
						},
					},
					"file:///b.go": {
						{
							Range: semanticapi.Range{
								Start: semanticapi.Position{Line: 2, Character: 5},
								End:   semanticapi.Position{Line: 2, Character: 8},
							},
							NewText: "bar",
						},
					},
				},
			}, nil
		},
	}

	editedURIs := map[string]bool{}
	editor := &mockEditor{
		editorFn: func(uri workspaceapi.URI) (textapi.Handler, error) {
			return &mockHandler{uri: uri}, nil
		},
		cellEditorFn: func(h textapi.Handler) textapi.CellEditor {
			rh, ok := h.(*mockHandler)
			if ok {
				editedURIs[rh.uri.String()] = true
			}
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

	h := newTestRenameFloating(t, "bar", lsp, editor, nil)
	exit, _ := h.Handle(term.Event{
		Type: term.EventKey, Key: term.KeyEnter,
	})

	assert.True(t, exit)
	assert.True(t, editedURIs["file:///a.go"], "a.go should be edited")
	assert.True(t, editedURIs["file:///b.go"], "b.go should be edited")
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

func TestRenameHandlerPrepareRenameNil(t *testing.T) {
	t.Parallel()

	lsp := &mockLSP{
		prepareRenameFn: func(
			_ context.Context, _ semanticapi.PrepareRenameParams,
		) (*semanticapi.PrepareRenameResult, error) {
			return nil, nil
		},
	}

	h := RenameHandler(lsp, &mockEditor{}, &mockWindowManager{}, nil, nil, nil)
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

	h := RenameHandler(lsp, &mockEditor{}, &mockWindowManager{}, nil, nil, nil)
	uri := testURI(t, "file:///test.go")
	err := h.HandleCommand(context.Background(), textapi.Command{
		Name:     "rename",
		URI:      uri,
		Resource: &mockHandler{uri: uri},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "prepare rename")
}

func TestRenameHandlerSuccess(t *testing.T) {
	t.Parallel()

	lsp := &mockLSP{
		prepareRenameFn: func(
			_ context.Context, _ semanticapi.PrepareRenameParams,
		) (*semanticapi.PrepareRenameResult, error) {
			return &semanticapi.PrepareRenameResult{
				Range: semanticapi.Range{
					Start: semanticapi.Position{Line: 5, Character: 4},
					End:   semanticapi.Position{Line: 5, Character: 11},
				},
				Placeholder: "oldName",
			}, nil
		},
	}

	var floatingH browserapi.Floating
	var floatingCfg browserapi.FloatingConfig
	wm := &mockWindowManager{
		floatingFn: func(
			h browserapi.Floating, cfg browserapi.FloatingConfig,
		) (browserapi.Window, error) {
			floatingH = h
			floatingCfg = cfg
			return &mockWindow{id: 1}, nil
		},
	}

	h := RenameHandler(lsp, &mockEditor{}, wm, nil, nil, nil)
	uri := testURI(t, "file:///test.go")
	cmd := textapi.Command{
		Name:     "rename",
		URI:      uri,
		Resource: &mockHandler{uri: uri},
	}
	cmd.Cursor.Content = term.Coordinates{X: 4, Y: 5}
	cmd.Cursor.Window = term.Coordinates{X: 10, Y: 3}

	err := h.HandleCommand(context.Background(), cmd)
	require.NoError(t, err)
	require.NotNil(t, floatingH)

	rh, ok := floatingH.(*renameFloatingHandler)
	require.True(t, ok)
	assert.Equal(t, "oldName", rh.ib.Text())

	assert.Equal(t, 11, floatingCfg.Offset.X)
	assert.Equal(t, 5, floatingCfg.Offset.Y)
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

// TestE2ERename exercises the full rename pipeline against a
// real gopls server: PrepareRename → inputbox → Rename → apply → didChange.
func TestE2ERename(t *testing.T) {
	t.Parallel()
	goplsBin := findGopls(t)
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

	var floatingH browserapi.Floating
	wm := &mockWindowManager{
		floatingFn: func(
			h browserapi.Floating, _ browserapi.FloatingConfig,
		) (browserapi.Window, error) {
			floatingH = h
			return &mockWindow{id: 1}, nil
		},
	}

	cfg := DefaultConfig()
	cfg.RootURI = rootURI
	cfg.ScheduleNextTick = syncTick
	router, err := AllHandler(
		mgr, editor, wm, &mockResourceOpener{},
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
	require.NotNil(t, floatingH, "floating must be shown")

	rh, ok := floatingH.(*renameFloatingHandler)
	require.True(t, ok)
	assert.Equal(t, "Add", rh.ib.Text(),
		"inputbox should be pre-filled with current name")

	// Clear and type the new name "Sum".
	rh.ib.Clear()
	for _, ch := range "Sum" {
		rh.Handle(term.Event{Type: term.EventKey, Ch: ch})
	}
	require.Equal(t, "Sum", rh.ib.Text())

	// Confirm the rename.
	exit, handled := rh.Handle(term.Event{
		Type: term.EventKey, Key: term.KeyEnter,
	})
	assert.True(t, exit)
	assert.True(t, handled)

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

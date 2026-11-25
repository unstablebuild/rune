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
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/handler/locationpicker"
	"unstable.build/rune/internal/ide/idelsp"
	"unstable.build/rune/internal/ide/syntax/grammarfixture"
	"unstable.build/rune/internal/ide/syntax/treesitter"
	"unstable.build/rune/internal/workspace"
)

var _ textapi.CommandHandler = (*referencesHandler)(nil)

// waitFloating waits for the async cursor path to float a picker.
func waitFloating(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the async LSP result")
	}
}

func TestReferencesPreviewAfterUnsavedEdit(t *testing.T) {
	const caller = "package main\nfunc main() { alpha() }\n"
	const decl = "func alpha() {}\n"
	root, err := workspaceapi.ParseURI("file://" + t.TempDir())
	require.NoError(t, err)
	fs, err := workspace.NewFileScheme(t.Context(), config.NopConfig(), root)
	require.NoError(t, err)
	t.Cleanup(func() { _ = fs.Close() })
	path := filepath.Join(root.Path(), "main.go")
	require.NoError(t, os.WriteFile(path, []byte(caller+decl), 0o600))
	uri, err := fs.URI(path)
	require.NoError(t, err)
	editor := &mockEditor{
		editorFn: func(u workspaceapi.URI) (textapi.Handler, error) {
			require.Equal(t, uri, u)
			return &mockHandler{uri: u}, nil
		},
		cellViewFn: func(textapi.Handler) textapi.CellView {
			return &mockCellView{cells: term.StringToCells(caller + "\n" + decl)}
		},
	}
	call := semanticapi.Range{Start: semanticapi.Position{Line: 1, Character: 14}, End: semanticapi.Position{Line: 1, Character: 19}}
	check := func(t *testing.T, lsp commandLSP, line int) {
		t.Helper()
		var picker browserapi.Floating
		wm := &mockWindowManager{floatingFn: func(f browserapi.Floating, _ browserapi.FloatingConfig) (browserapi.Window, error) {
			picker = f
			return nil, nil
		}}
		h := referencesHandler{lsp: lsp, editor: editor, wm: wm, fs: fs, rootURI: root, scheduleNextTick: syncTick}
		require.NoError(t, h.execute(t.Context(), uri, call.Start))
		require.NotNil(t, picker)
		w, height := picker.Dimensions()
		picker.Resize(w, height)
		screen := term.NewStringWriter(w, height)
		picker.Draw(screen)
		row := screen.Cells()[line*w : (line+1)*w]
		for i, ch := range "func alpha() {}" {
			c := row[1+i] // one column of picker padding
			require.Equal(t, ch, c.Ch, "source column %d", i)
			require.Equal(t, i >= 5 && i < 10, c.Attrs&term.AttrReverse != 0, "source column %d", i)
		}
	}
	t.Run("language server", func(t *testing.T) {
		check(t, &mockLSP{referencesFn: func(context.Context, semanticapi.ReferenceParams) ([]semanticapi.Location, error) {
			return []semanticapi.Location{
				{URI: uri.String(), Range: semanticapi.Range{Start: semanticapi.Position{Line: 3, Character: 5}, End: semanticapi.Position{Line: 3, Character: 10}}},
				{URI: uri.String(), Range: call},
			}, nil
		}}, 3)
	})
	t.Run("tree-sitter without a server", func(t *testing.T) {
		pkgs := grammarfixture.NewPackages(t, map[string][]string{"go": {"1.0.0"}})
		pkgs.Install(t, "go", "1.0.0", grammarfixture.PackageFiles(t, filepath.Join("..", "..", "syntax", "syntaxtest", "go")))
		parser := treesitter.NewParser(fs, pkgs, root)
		mgr := idelsp.New(root, fs, nil, nil, nil, nil, idelsp.Config{NoInitializeServer: true, Parser: parser})
		t.Cleanup(func() { _ = mgr.Close() })
		check(t, mgr, 2)
	})
}

func TestReferencesEnrichedDisplay(t *testing.T) {
	rootURI, err := workspaceapi.ParseURI("file:///project")
	require.NoError(t, err)

	locs := []semanticapi.Location{
		{
			URI: "file:///project/a.go",
			Range: semanticapi.Range{
				Start: semanticapi.Position{Line: 0, Character: 5},
				End:   semanticapi.Position{Line: 0, Character: 8},
			},
		},
		{
			URI: "file:///project/b.go",
			Range: semanticapi.Range{
				Start: semanticapi.Position{Line: 3, Character: 0},
				End:   semanticapi.Position{Line: 3, Character: 3},
			},
		},
	}
	lsp := &mockLSP{
		referencesFn: func(_ context.Context, _ semanticapi.ReferenceParams) ([]semanticapi.Location, error) {
			return locs, nil
		},
	}
	editor := &mockEditor{
		editorFn: func(u workspaceapi.URI) (textapi.Handler, error) {
			return &mockHandler{uri: u}, nil
		},
	}
	var fh browserapi.Floating
	done := make(chan struct{}, 1)
	wm := &mockWindowManager{
		floatingFn: func(h browserapi.Floating, _ browserapi.FloatingConfig) (browserapi.Window, error) {
			fh = h
			select {
			case done <- struct{}{}:
			default:
			}
			return nil, nil
		},
	}
	h := ReferencesHandler(
		lsp, editor, wm, &mockResourceOpener{}, &mockNotifications{}, &mockFileSystem{},
		rootURI, syncTick, nil, DefaultReferencesConfig(), nil,
	)

	uri, _ := workspaceapi.ParseURI("file:///project/a.go")
	cmd := textapi.Command{Name: "references", URI: uri, Resource: &mockHandler{uri: uri}}
	cmd.Cursor.Content = term.Coordinates{X: 5, Y: 0}

	err = h.HandleCommand(context.Background(), cmd)
	require.NoError(t, err)
	waitFloating(t, done)
	require.NotNil(t, fh)

	lh := fh.(*locationpicker.Picker)
	assert.Equal(t, "a.go:1", lh.Entries()[0].Display)
}

func TestReferencesRelativePaths(t *testing.T) {
	rootURI, err := workspaceapi.ParseURI("file:///workspace")
	require.NoError(t, err)

	locs := []semanticapi.Location{
		{
			URI: "file:///workspace/src/pkg/handler.go",
			Range: semanticapi.Range{
				Start: semanticapi.Position{Line: 10, Character: 5},
				End:   semanticapi.Position{Line: 10, Character: 11},
			},
		},
		{
			URI: "file:///workspace/cmd/main.go",
			Range: semanticapi.Range{
				Start: semanticapi.Position{Line: 3, Character: 0},
				End:   semanticapi.Position{Line: 3, Character: 6},
			},
		},
		{
			URI: "file:///other/lib/ext.go",
			Range: semanticapi.Range{
				Start: semanticapi.Position{Line: 0, Character: 0},
				End:   semanticapi.Position{Line: 0, Character: 3},
			},
		},
	}
	lsp := &mockLSP{
		referencesFn: func(_ context.Context, _ semanticapi.ReferenceParams) ([]semanticapi.Location, error) {
			return locs, nil
		},
	}
	editor := &mockEditor{
		editorFn: func(u workspaceapi.URI) (textapi.Handler, error) {
			return &mockHandler{uri: u}, nil
		},
	}
	var fh browserapi.Floating
	done := make(chan struct{}, 1)
	wm := &mockWindowManager{
		floatingFn: func(h browserapi.Floating, _ browserapi.FloatingConfig) (browserapi.Window, error) {
			fh = h
			select {
			case done <- struct{}{}:
			default:
			}
			return nil, nil
		},
	}
	h := ReferencesHandler(
		lsp, editor, wm, &mockResourceOpener{}, &mockNotifications{}, &mockFileSystem{},
		rootURI, syncTick, nil, ReferencesConfig{ListConfig: locationpicker.DefaultConfig()}, nil,
	)

	uri, _ := workspaceapi.ParseURI("file:///workspace/src/pkg/handler.go")
	cmd := textapi.Command{Name: "references", URI: uri, Resource: &mockHandler{uri: uri}}
	cmd.Cursor.Content = term.Coordinates{X: 5, Y: 10}

	err = h.HandleCommand(context.Background(), cmd)
	require.NoError(t, err)
	waitFloating(t, done)
	require.NotNil(t, fh)

	lh := fh.(*locationpicker.Picker)
	require.Len(t, lh.Entries(), 3)
	assert.Equal(t, "src/pkg/handler.go:11", lh.Entries()[0].Display, "workspace nested path should be relative")
	assert.Equal(t, "cmd/main.go:4", lh.Entries()[1].Display, "workspace path should be relative")
	assert.Equal(t, "/other/lib/ext.go:1", lh.Entries()[2].Display, "out-of-workspace path should be absolute")
}

func TestReferencesZeroRootURI(t *testing.T) {
	locs := []semanticapi.Location{
		{
			URI: "file:///workspace/pkg/foo.go",
			Range: semanticapi.Range{
				Start: semanticapi.Position{Line: 5, Character: 0},
				End:   semanticapi.Position{Line: 5, Character: 3},
			},
		},
		{
			URI: "file:///workspace/pkg/bar.go",
			Range: semanticapi.Range{
				Start: semanticapi.Position{Line: 10, Character: 0},
				End:   semanticapi.Position{Line: 10, Character: 3},
			},
		},
	}
	lsp := &mockLSP{
		referencesFn: func(_ context.Context, _ semanticapi.ReferenceParams) ([]semanticapi.Location, error) {
			return locs, nil
		},
	}
	editor := &mockEditor{
		editorFn: func(u workspaceapi.URI) (textapi.Handler, error) {
			return &mockHandler{uri: u}, nil
		},
	}
	var fh browserapi.Floating
	done := make(chan struct{}, 1)
	wm := &mockWindowManager{
		floatingFn: func(h browserapi.Floating, _ browserapi.FloatingConfig) (browserapi.Window, error) {
			fh = h
			select {
			case done <- struct{}{}:
			default:
			}
			return nil, nil
		},
	}
	// Zero RootURI (not set) — paths should fall back to absolute.
	h := ReferencesHandler(
		lsp, editor, wm, &mockResourceOpener{}, &mockNotifications{}, &mockFileSystem{},
		workspaceapi.URI{}, syncTick, nil, DefaultReferencesConfig(), nil,
	)

	uri, _ := workspaceapi.ParseURI("file:///workspace/pkg/foo.go")
	cmd := textapi.Command{Name: "references", URI: uri, Resource: &mockHandler{uri: uri}}
	cmd.Cursor.Content = term.Coordinates{X: 0, Y: 5}

	err := h.HandleCommand(context.Background(), cmd)
	require.NoError(t, err)
	waitFloating(t, done)
	require.NotNil(t, fh)

	lh := fh.(*locationpicker.Picker)
	require.Len(t, lh.Entries(), 2)
	assert.Equal(t, "/workspace/pkg/foo.go:6", lh.Entries()[0].Display, "zero RootURI should produce absolute path")
	assert.Equal(t, "/workspace/pkg/bar.go:11", lh.Entries()[1].Display, "zero RootURI should produce absolute path")
}

func TestReferencesHandler(t *testing.T) {
	rootURI, err := workspaceapi.ParseURI("file:///project")
	require.NoError(t, err)

	tests := []struct {
		name         string
		locs         []semanticapi.Location
		nilResource  bool
		wantErr      bool
		wantFloat    bool
		wantNavigate bool
		wantEntries  int
	}{
		{
			name: "single reference navigates directly",
			locs: []semanticapi.Location{
				{
					URI: "file:///project/a.go",
					Range: semanticapi.Range{
						Start: semanticapi.Position{Line: 10, Character: 5},
						End:   semanticapi.Position{Line: 10, Character: 8},
					},
				},
			},
			wantNavigate: true,
		},
		{
			name: "multiple references",
			locs: []semanticapi.Location{
				{
					URI: "file:///project/a.go",
					Range: semanticapi.Range{
						Start: semanticapi.Position{Line: 10, Character: 5},
						End:   semanticapi.Position{Line: 10, Character: 8},
					},
				},
				{
					URI: "file:///project/b.go",
					Range: semanticapi.Range{
						Start: semanticapi.Position{Line: 20, Character: 0},
						End:   semanticapi.Position{Line: 20, Character: 3},
					},
				},
			},
			wantFloat:   true,
			wantEntries: 2,
		},
		{name: "zero references"},
		{name: "nil resource", nilResource: true, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lsp := &mockLSP{
				referencesFn: func(_ context.Context, _ semanticapi.ReferenceParams) ([]semanticapi.Location, error) {
					return tt.locs, nil
				},
			}
			done := make(chan struct{}, 1)
			signal := func() {
				select {
				case done <- struct{}{}:
				default:
				}
			}
			var navigated bool
			editor := &mockEditor{
				editorFn: func(u workspaceapi.URI) (textapi.Handler, error) {
					return &mockHandler{uri: u}, nil
				},
				setCursorFn: func(_ textapi.Handler, _ term.Coordinates) error {
					navigated = true
					signal()
					return nil
				},
			}
			var fh browserapi.Floating
			wm := &mockWindowManager{
				floatingFn: func(h browserapi.Floating, _ browserapi.FloatingConfig) (browserapi.Window, error) {
					fh = h
					signal()
					return nil, nil
				},
			}
			notify := &recordingNotifications{}
			h := ReferencesHandler(
				lsp, editor, wm, &mockResourceOpener{}, notify, &mockFileSystem{},
				rootURI, syncTick, nil, DefaultReferencesConfig(), nil,
			)

			uri, _ := workspaceapi.ParseURI("file:///project/a.go")
			cmd := textapi.Command{Name: "references", URI: uri}
			if !tt.nilResource {
				cmd.Resource = &mockHandler{uri: uri}
			}
			cmd.Cursor.Content = term.Coordinates{X: 5, Y: 10}

			err := h.HandleCommand(context.Background(), cmd)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tt.wantFloat || tt.wantNavigate {
				waitFloating(t, done)
			} else {
				// Zero references complete silently; synchronise on the
				// cursor-path progress notification finishing.
				require.Eventually(t, func() bool {
					_, updates := notify.snapshot()
					for _, u := range updates {
						if u.step == 1 && u.total == 1 {
							return true
						}
					}
					return false
				}, 5*time.Second, 5*time.Millisecond,
					"cursor path must complete its progress notification")
			}
			assert.Equal(t, tt.wantFloat, fh != nil)
			assert.Equal(t, tt.wantNavigate, navigated)
			if tt.wantEntries > 0 {
				lh := fh.(*locationpicker.Picker)
				assert.Equal(t, tt.wantEntries, len(lh.Entries()))
			}
		})
	}
}

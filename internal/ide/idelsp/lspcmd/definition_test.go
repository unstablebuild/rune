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
	"strings"
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
)

var _ textapi.CommandHandler = (*definitionHandler)(nil)

func TestDefinitionHandler(t *testing.T) {
	rootURI, err := workspaceapi.ParseURI("file:///project")
	require.NoError(t, err)

	fileB, err := workspaceapi.ParseURI("file:///project/b.go")
	require.NoError(t, err)

	tests := []struct {
		name          string
		result        semanticapi.LocationResult
		nilResource   bool
		args          []string
		parser        *mockParser
		wantErr       bool
		wantNotifyErr bool
		wantFloat     bool
		wantNavigate  bool
		wantEntries   int
	}{
		{
			name: "single definition navigates directly",
			result: semanticapi.LocationResult{
				Location: &semanticapi.Location{
					URI: "file:///project/a.go",
					Range: semanticapi.Range{
						Start: semanticapi.Position{Line: 15, Character: 0},
						End:   semanticapi.Position{Line: 15, Character: 5},
					},
				},
			},
			wantNavigate: true,
		},
		{
			name: "multiple definitions",
			result: semanticapi.LocationResult{
				Locations: []semanticapi.Location{
					{URI: "file:///project/a.go", Range: semanticapi.Range{Start: semanticapi.Position{Line: 10}}},
					{URI: "file:///project/b.go", Range: semanticapi.Range{Start: semanticapi.Position{Line: 20}}},
				},
			},
			wantFloat:   true,
			wantEntries: 2,
		},
		{name: "no definitions", wantNotifyErr: true},
		{name: "nil resource", nilResource: true, wantErr: true},
		{
			name:        "definition via symbol name",
			nilResource: true,
			args:        []string{"mylib.MyFunc"},
			parser: &mockParser{
				searchFn: func(query string, _ []string) (iterator.Iterator[syntaxapi.Result], error) {
					if strings.Contains(query, "selector_expression") {
						return iterator.FromSlice([]syntaxapi.Result{
							{File: fileB, Text: "mylib", From: term.Coordinates{X: 1, Y: 42}, CaptureName: "pkg"},
							{File: fileB, Text: "MyFunc", From: term.Coordinates{X: 7, Y: 42}, CaptureName: "symbol"},
						}), nil
					}
					return iterator.Empty[syntaxapi.Result](), nil
				},
				resolveFn: func(name string, _ syntaxapi.Progress) ([]syntaxapi.Match, error) {
					if name == "mylib.MyFunc" {
						return []syntaxapi.Match{
							{URI: fileB.String(), Pos: term.Coordinates{X: 7, Y: 42}},
						}, nil
					}
					return nil, nil
				},
			},
			result: semanticapi.LocationResult{
				Location: &semanticapi.Location{
					URI: "file:///project/b.go",
					Range: semanticapi.Range{
						Start: semanticapi.Position{Line: 42, Character: 7},
						End:   semanticapi.Position{Line: 42, Character: 13},
					},
				},
			},
			wantNavigate: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lsp := &mockLSP{
				definitionFn: func(_ context.Context, _ semanticapi.DefinitionParams) (semanticapi.LocationResult, error) {
					return tt.result, nil
				},
			}
			done := make(chan struct{}, 1)
			var navigated bool
			editor := &mockEditor{
				editorFn: func(u workspaceapi.URI) (textapi.Handler, error) {
					return &mockHandler{uri: u}, nil
				},
				setCursorFn: func(_ textapi.Handler, _ term.Coordinates) error {
					navigated = true
					select {
					case done <- struct{}{}:
					default:
					}
					return nil
				},
			}
			var fh browserapi.Floating
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
			notify := &recordingNotifications{}
			h := DefinitionHandler(
				lsp, editor, wm, &mockResourceOpener{}, notify, &mockFileSystem{},
				rootURI, syncTick, tt.parser, DefaultDefinitionConfig(), nil,
			)

			uri, _ := workspaceapi.ParseURI("file:///project/a.go")
			cmd := textapi.Command{Name: "definition", URI: uri, Args: tt.args}
			if !tt.nilResource {
				cmd.Resource = &mockHandler{uri: uri}
			}
			cmd.Cursor.Content = term.Coordinates{X: 5, Y: 50}

			err := h.HandleCommand(context.Background(), cmd)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tt.wantNotifyErr {
				require.Eventually(t, func() bool {
					notifies, _ := notify.snapshot()
					for _, n := range notifies {
						if n.level == browserapi.LevelError {
							return true
						}
					}
					return false
				}, 5*time.Second, 5*time.Millisecond,
					"empty results must surface an error notification")
				assert.Nil(t, fh)
				assert.False(t, navigated)
				return
			}
			if tt.wantNavigate || tt.wantFloat {
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("timed out waiting for async resolution")
				}
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

func TestDefinitionHandlerRemoteWorkspace(t *testing.T) {
	rootURI, err := workspaceapi.ParseURI("ssh://host/home/user/src/rune")
	require.NoError(t, err)

	lsp := &mockLSP{
		definitionFn: func(_ context.Context, _ semanticapi.DefinitionParams) (semanticapi.LocationResult, error) {
			return semanticapi.LocationResult{
				Location: &semanticapi.Location{
					URI: "file:///home/user/src/rune/cell/buffer.go",
					Range: semanticapi.Range{
						Start: semanticapi.Position{Line: 15, Character: 0},
						End:   semanticapi.Position{Line: 15, Character: 5},
					},
				},
			}, nil
		},
	}
	done := make(chan struct{}, 1)
	editor := &mockEditor{
		editorFn: func(u workspaceapi.URI) (textapi.Handler, error) {
			return &mockHandler{uri: u}, nil
		},
		setCursorFn: func(_ textapi.Handler, _ term.Coordinates) error {
			select {
			case done <- struct{}{}:
			default:
			}
			return nil
		},
	}
	var opened []string
	opener := &mockResourceOpener{
		openFn: func(u workspaceapi.URI) (browserapi.Handler, error) {
			opened = append(opened, u.String())
			return nil, nil
		},
	}
	h := DefinitionHandler(
		lsp, editor, &mockWindowManager{}, opener, &recordingNotifications{},
		&mockFileSystem{}, rootURI, syncTick, nil, DefaultDefinitionConfig(), nil,
	)

	uri, err := workspaceapi.ParseURI("ssh://host/home/user/src/rune/main.go")
	require.NoError(t, err)
	cmd := textapi.Command{Name: "definition", URI: uri}
	cmd.Resource = &mockHandler{uri: uri}
	cmd.Cursor.Content = term.Coordinates{X: 5, Y: 50}

	require.NoError(t, h.HandleCommand(context.Background(), cmd))
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for navigation")
	}
	require.Len(t, opened, 1)
	assert.Equal(t, "ssh://host/home/user/src/rune/cell/buffer.go", opened[0],
		"navigation must rebase the server's file:// location onto the workspace scheme")
}

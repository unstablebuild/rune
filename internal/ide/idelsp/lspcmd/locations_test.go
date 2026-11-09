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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/handler/locationpicker"
)

func TestPresentLocationsPickerCloseLeavesItsWindow(t *testing.T) {
	var floated browserapi.Floating
	wm := &mockWindowManager{
		floatingFn: func(f browserapi.Floating, _ browserapi.FloatingConfig) (browserapi.Window, error) {
			floated = f
			return &mockWindow{id: 1}, nil
		},
	}
	entries := []locationEntry{
		{uri: "file:///a.go", display: "a.go:1"},
		{uri: "file:///b.go", display: "b.go:1"},
	}
	presentLocations("references", workspaceapi.URI{}, entries, &mockResourceOpener{}, wm,
		&mockEditor{}, &mockNotifications{}, &mockFileSystem{}, syncTick, nil,
		locationpicker.DefaultConfig(), nil)
	require.NotNil(t, floated)

	require.NoError(t, floated.Close())

	assert.Zero(t, wm.closeWindowCalls,
		"the window may already show other content when the picker is released")
}

func TestNavigateTo(t *testing.T) {
	tests := []struct {
		name    string
		entry   locationEntry
		wantURI string
		wantPos term.Coordinates
	}{
		{
			name: "navigates to position",
			entry: locationEntry{
				uri: "file:///foo/bar.go",
				rng: semanticapi.Range{
					Start: semanticapi.Position{
						Line:      10,
						Character: 5,
					},
				},
			},
			wantURI: "/foo/bar.go",
			wantPos: term.Coordinates{
				X: 5, Y: 10,
			},
		},
		{
			name: "line zero character zero",
			entry: locationEntry{
				uri: "file:///root.go",
				rng: semanticapi.Range{
					Start: semanticapi.Position{},
				},
			},
			wantURI: "/root.go",
			wantPos: term.Coordinates{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var (
				openedPath       string
				cursorSet        term.Coordinates
				setContentWindow browserapi.Window
				setContentH      browserapi.Handler
			)
			openHandler := &mockHandler{}
			opener := &mockResourceOpener{
				openFn: func(
					u workspaceapi.URI,
				) (browserapi.Handler, error) {
					openedPath = u.Path()
					return openHandler, nil
				},
			}
			focusWindow := &mockWindow{id: 1}
			wm := &mockWindowManager{
				focusFn: func() (browserapi.Window, error) {
					return focusWindow, nil
				},
				setWindowContentFn: func(w browserapi.Window, h browserapi.Handler) error {
					setContentWindow = w
					setContentH = h
					return nil
				},
			}
			editor := &mockEditor{
				editorFn: func(
					u workspaceapi.URI,
				) (textapi.Handler, error) {
					return &mockHandler{
						uri: u,
					}, nil
				},
				setCursorFn: func(
					_ textapi.Handler,
					c term.Coordinates,
				) error {
					cursorSet = c
					return nil
				},
			}
			syncTick := func(fn func()) bool { fn(); return true }
			navigateTo(
				workspaceapi.URI{}, tt.entry, opener, wm, editor, &mockNotifications{}, syncTick,
			)
			assert.Equal(
				t, tt.wantURI, openedPath,
			)
			assert.Equal(t, focusWindow, setContentWindow)
			assert.Same(t, openHandler, setContentH, "SetWindowContent should receive the handler from Open")
			assert.Equal(
				t, tt.wantPos, cursorSet,
			)
		})
	}
}

func TestLocationsFromResult(t *testing.T) {
	tests := []struct {
		name   string
		result semanticapi.LocationResult
		want   []locationEntry
	}{
		{
			name: "single location",
			result: semanticapi.LocationResult{
				Location: &semanticapi.Location{
					URI: "file:///a.go",
					Range: semanticapi.Range{
						Start: semanticapi.Position{
							Line: 9,
						},
					},
				},
			},
			want: []locationEntry{
				{
					uri: "file:///a.go",
					rng: semanticapi.Range{
						Start: semanticapi.Position{
							Line: 9,
						},
					},
					display: "/a.go:10",
				},
			},
		},
		{
			name: "multiple locations",
			result: semanticapi.LocationResult{
				Locations: []semanticapi.Location{
					{
						URI: "file:///a.go",
						Range: semanticapi.Range{
							Start: semanticapi.Position{
								Line: 1,
							},
						},
					},
					{
						URI: "file:///b.go",
						Range: semanticapi.Range{
							Start: semanticapi.Position{
								Line: 2,
							},
						},
					},
				},
			},
			want: []locationEntry{
				{
					uri: "file:///a.go",
					rng: semanticapi.Range{
						Start: semanticapi.Position{
							Line: 1,
						},
					},
					display: "/a.go:2",
				},
				{
					uri: "file:///b.go",
					rng: semanticapi.Range{
						Start: semanticapi.Position{
							Line: 2,
						},
					},
					display: "/b.go:3",
				},
			},
		},
		{
			name: "location links",
			result: semanticapi.LocationResult{
				LocationLinks: []semanticapi.LocationLink{
					{
						TargetURI: "file:///c.go",
						TargetSelectionRange: semanticapi.Range{
							Start: semanticapi.Position{
								Line: 4,
							},
						},
					},
				},
			},
			want: []locationEntry{
				{
					uri: "file:///c.go",
					rng: semanticapi.Range{
						Start: semanticapi.Position{
							Line: 4,
						},
					},
					display: "/c.go:5",
				},
			},
		},
		{
			name:   "empty result",
			result: semanticapi.LocationResult{},
		},
		{
			name: "location display line+1",
			result: semanticapi.LocationResult{
				Location: &semanticapi.Location{
					URI: "file:///src/main.go",
					Range: semanticapi.Range{
						Start: semanticapi.Position{
							Line: 41,
						},
					},
				},
			},
			want: []locationEntry{
				{
					uri: "file:///src/main.go",
					rng: semanticapi.Range{
						Start: semanticapi.Position{
							Line: 41,
						},
					},
					display: "/src/main.go:42",
				},
			},
		},
		{
			name: "link display line+1",
			result: semanticapi.LocationResult{
				LocationLinks: []semanticapi.LocationLink{
					{
						TargetURI: "file:///lib.go",
						TargetSelectionRange: semanticapi.Range{
							Start: semanticapi.Position{
								Line: 0,
							},
						},
					},
				},
			},
			want: []locationEntry{
				{
					uri:     "file:///lib.go",
					rng:     semanticapi.Range{},
					display: "/lib.go:1",
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := locationsFromResult(
				tt.result,
			)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestEnrichEntriesRelativePaths(t *testing.T) {
	rootURI, err := workspaceapi.ParseURI("file:///workspace")
	require.NoError(t, err)

	entries := []locationEntry{
		{
			uri: "file:///workspace/pkg/foo.go",
			rng: semanticapi.Range{Start: semanticapi.Position{Line: 5}},
		},
		{
			uri: "file:///other/bar.go",
			rng: semanticapi.Range{Start: semanticapi.Position{Line: 0}},
		},
	}
	got := enrichEntries(entries, rootURI)

	assert.Equal(t, "pkg/foo.go:6", got[0].display, "workspace file should be relative")
	assert.Equal(t, "/other/bar.go:1", got[1].display, "out-of-workspace file should be absolute")
}

func TestTrimFilePrefix(t *testing.T) {
	tests := []struct {
		name string
		uri  string
		want string
	}{
		{
			name: "with prefix",
			uri:  "file:///foo/bar.go",
			want: "/foo/bar.go",
		},
		{
			name: "without prefix",
			uri:  "/foo/bar.go",
			want: "/foo/bar.go",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := trimFilePrefix(tt.uri)
			assert.Equal(t, tt.want, got)
		})
	}
}

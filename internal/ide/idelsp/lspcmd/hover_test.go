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
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	runehandlertest "unstable.build/rune/internal/handler/handlertest"
)

func TestHoverFloatingHandle(t *testing.T) {
	t.Parallel()
	f := newHoverFloating(component.NewString("test content"))

	exit, handled := f.Handle(term.Event{Type: term.EventKey})
	assert.True(t, exit, "key event should exit")
	assert.True(t, handled, "key event should be handled")

	exit, handled = f.Handle(term.Event{Type: term.EventMouse})
	assert.False(t, exit, "non-key event should not exit")
	assert.False(t, handled, "non-key event should not be handled")
}

func TestHoverHandlerSymbolName(t *testing.T) {
	t.Parallel()

	fileTypes, err := workspaceapi.ParseURI("file:///project/types.go")
	require.NoError(t, err)

	var hoveredPos semanticapi.Position
	lsp := &mockLSP{}
	lsp.stubLSP = stubLSP{}
	hoverFn := func(_ context.Context, params semanticapi.HoverParams) (*semanticapi.Hover, error) {
		hoveredPos = params.Position
		return &semanticapi.Hover{
			Contents: semanticapi.MarkupContent{
				Kind:  semanticapi.MarkupKindPlainText,
				Value: "type MyType struct{}",
			},
		}, nil
	}
	wrapper := &hoverMockLSP{mockLSP: lsp, hoverFn: hoverFn}

	parser := &mockParser{
		searchFn: func(query string, _ []string) (iterator.Iterator[syntaxapi.Result], error) {
			if strings.Contains(query, "qualified_type") {
				return iterator.FromSlice([]syntaxapi.Result{
					{File: fileTypes, Text: "mylib", From: term.Coordinates{X: 5, Y: 7}, CaptureName: "pkg"},
					{File: fileTypes, Text: "MyType", From: term.Coordinates{X: 11, Y: 7}, CaptureName: "type"},
				}), nil
			}
			return iterator.Empty[syntaxapi.Result](), nil
		},
		resolveFn: func(name string, _ syntaxapi.Progress) ([]syntaxapi.Match, error) {
			if name == "mylib.MyType" {
				return []syntaxapi.Match{
					{URI: fileTypes.String(), Pos: term.Coordinates{X: 11, Y: 7}},
				}, nil
			}
			return nil, nil
		},
	}

	done := make(chan struct{}, 1)
	var floated bool
	wm := &mockWindowManager{
		floatingFn: func(_ browserapi.Floating, _ browserapi.FloatingConfig) (browserapi.Window, error) {
			floated = true
			select {
			case done <- struct{}{}:
			default:
			}
			return nil, nil
		},
	}

	h := HoverHandler(wrapper, wm, &mockNotifications{}, &mockFileSystem{},
		workspaceapi.URI{}, syncTick, parser, DefaultHoverConfig())

	cmd := textapi.Command{
		Name: "hover",
		Args: []string{"mylib.MyType"},
	}
	err = h.HandleCommand(context.Background(), cmd)
	require.NoError(t, err)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for async resolution")
	}
	assert.True(t, floated, "hover floating should be shown")
	assert.Equal(t, semanticapi.Position{Line: 7, Character: 11}, hoveredPos)
}

func TestHoverCloseLeavesItsWindow(t *testing.T) {
	t.Parallel()
	for _, kind := range []semanticapi.MarkupKind{
		semanticapi.MarkupKindPlainText, semanticapi.MarkupKindMarkdown,
	} {
		t.Run(string(kind), func(t *testing.T) {
			lsp := &hoverMockLSP{mockLSP: &mockLSP{}, hoverFn: func(
				context.Context, semanticapi.HoverParams,
			) (*semanticapi.Hover, error) {
				return &semanticapi.Hover{Contents: semanticapi.MarkupContent{
					Kind: kind, Value: "type MyType struct{}",
				}}, nil
			}}
			var floated browserapi.Floating
			wm := &mockWindowManager{
				floatingFn: func(f browserapi.Floating, _ browserapi.FloatingConfig) (browserapi.Window, error) {
					floated = f
					return &mockWindow{id: 1}, nil
				},
			}
			h := HoverHandler(lsp, wm, &mockNotifications{}, &mockFileSystem{},
				workspaceapi.URI{}, syncTick, &mockParser{}, DefaultHoverConfig()).(*hoverHandler)
			require.NoError(t, h.execute(context.Background(), testURI(t, "file:///a.go"), semanticapi.Position{}))
			require.NotNil(t, floated)

			require.NoError(t, floated.Close())

			assert.Zero(t, wm.closeWindowCalls,
				"the window may already show other content when the hover is released")
		})
	}
}

// hoverMockLSP wraps mockLSP and overrides Hover.
type hoverMockLSP struct {
	*mockLSP
	hoverFn func(context.Context, semanticapi.HoverParams) (*semanticapi.Hover, error)
}

func (m *hoverMockLSP) Hover(ctx context.Context, params semanticapi.HoverParams) (*semanticapi.Hover, error) {
	if m.hoverFn != nil {
		return m.hoverFn(ctx, params)
	}
	return nil, nil
}

func TestHoverFloatingPointerLeaves(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("func (c *Client) Do(req *Request) (*Response, error) — sends an HTTP request. ", 20)
	for _, size := range []struct{ w, h int }{{60, 15}, {1, 1}} {
		f := newHoverFloating(component.NewString(long))
		runehandlertest.RunPointerLeaves(t, f, size.w, size.h)
	}
}

func TestHoverMarkdownFloatingPointerLeaves(t *testing.T) {
	t.Parallel()
	md := "```go\nfunc (c *Client) Do(req *Request) (*Response, error)\n```\n\n" +
		strings.Repeat("Do sends an HTTP request and returns an HTTP response, following "+
			"policy (such as [redirects](https://pkg.go.dev/net/http#Client), cookies, auth) "+
			"as configured on the client. See [`Request`](https://pkg.go.dev/net/http#Request).\n\n", 8)
	lsp := &hoverMockLSP{mockLSP: &mockLSP{}, hoverFn: func(
		context.Context, semanticapi.HoverParams,
	) (*semanticapi.Hover, error) {
		return &semanticapi.Hover{Contents: semanticapi.MarkupContent{
			Kind: semanticapi.MarkupKindMarkdown, Value: md,
		}}, nil
	}}
	for _, size := range []struct{ w, h int }{{60, 15}, {1, 1}} {
		var floated browserapi.Floating
		wm := &mockWindowManager{
			floatingFn: func(f browserapi.Floating, _ browserapi.FloatingConfig) (browserapi.Window, error) {
				floated = f
				return &mockWindow{id: 1}, nil
			},
		}
		h := HoverHandler(lsp, wm, &mockNotifications{}, &mockFileSystem{},
			workspaceapi.URI{}, syncTick, &mockParser{}, DefaultHoverConfig()).(*hoverHandler)
		require.NoError(t, h.execute(context.Background(), testURI(t, "file:///a.go"), semanticapi.Position{}))
		require.NotNil(t, floated)
		runehandlertest.RunPointerLeaves(t, floated, size.w, size.h)
		for _, off := range []term.Coordinates{{X: -1, Y: 0}, {X: size.w, Y: 0}, {X: 0, Y: -300}} {
			exit, _ := floated.Handle(term.Event{Type: term.EventMouse, MouseX: off.X, MouseY: off.Y})
			assert.False(t, exit, "a leave to %+v must not close the hover", off)
		}
	}
}

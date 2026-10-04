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
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/handler/locationpicker"
)

// tickRecorder runs scheduled callbacks inline (like syncTick) while
// tracking whether one is currently executing, so tests can assert that
// blocking work happens outside the simulated event loop.
type tickRecorder struct{ inTick atomic.Bool }

func (r *tickRecorder) tick(fn func()) bool {
	r.inTick.Store(true)
	fn()
	r.inTick.Store(false)
	return true
}

func TestLocationHandlersCursorPathDoesNotBlockLoop(t *testing.T) {
	t.Parallel()

	rootURI, err := workspaceapi.ParseURI("file:///project")
	require.NoError(t, err)
	loc := semanticapi.Location{
		URI: "file:///project/a.go",
		Range: semanticapi.Range{
			Start: semanticapi.Position{Line: 3, Character: 1},
			End:   semanticapi.Position{Line: 3, Character: 4},
		},
	}
	res := semanticapi.LocationResult{Location: &loc}

	tests := []struct {
		name       string
		lsp        func(wait func()) semanticapi.LSP
		newHandler func(
			lsp semanticapi.LSP, editor textapi.Editor,
			wm browserapi.WindowManager, notify browserapi.Notifications,
			tick func(func()) bool,
		) textapi.CommandHandler
	}{
		{
			name: "definition",
			lsp: func(wait func()) semanticapi.LSP {
				return &mockLSP{definitionFn: func(context.Context, semanticapi.DefinitionParams) (semanticapi.LocationResult, error) {
					wait()
					return res, nil
				}}
			},
			newHandler: func(
				lsp semanticapi.LSP, editor textapi.Editor,
				wm browserapi.WindowManager, notify browserapi.Notifications,
				tick func(func()) bool,
			) textapi.CommandHandler {
				return DefinitionHandler(
					lsp, editor, wm, &mockResourceOpener{}, notify, &mockFileSystem{},
					rootURI, tick, nil, DefaultDefinitionConfig(), nil,
				)
			},
		},
		{
			name: "declaration",
			lsp: func(wait func()) semanticapi.LSP {
				return &mockLSP{declarationFn: func(context.Context, semanticapi.DeclarationParams) (semanticapi.LocationResult, error) {
					wait()
					return res, nil
				}}
			},
			newHandler: func(
				lsp semanticapi.LSP, editor textapi.Editor,
				wm browserapi.WindowManager, notify browserapi.Notifications,
				tick func(func()) bool,
			) textapi.CommandHandler {
				return DeclarationHandler(
					lsp, editor, wm, &mockResourceOpener{}, notify, &mockFileSystem{},
					rootURI, tick, nil, DefaultDeclarationConfig(), nil,
				)
			},
		},
		{
			name: "type-definition",
			lsp: func(wait func()) semanticapi.LSP {
				return &mockLSP{typeDefinitionFn: func(context.Context, semanticapi.TypeDefinitionParams) (semanticapi.LocationResult, error) {
					wait()
					return res, nil
				}}
			},
			newHandler: func(
				lsp semanticapi.LSP, editor textapi.Editor,
				wm browserapi.WindowManager, notify browserapi.Notifications,
				tick func(func()) bool,
			) textapi.CommandHandler {
				return TypeDefinitionHandler(
					lsp, editor, wm, &mockResourceOpener{}, notify, &mockFileSystem{},
					rootURI, tick, nil, DefaultTypeDefinitionConfig(), nil,
				)
			},
		},
		{
			name: "implementation",
			lsp: func(wait func()) semanticapi.LSP {
				return &mockLSP{implementationFn: func(context.Context, semanticapi.ImplementationParams) (semanticapi.LocationResult, error) {
					wait()
					return res, nil
				}}
			},
			newHandler: func(
				lsp semanticapi.LSP, editor textapi.Editor,
				wm browserapi.WindowManager, notify browserapi.Notifications,
				tick func(func()) bool,
			) textapi.CommandHandler {
				return ImplementationHandler(
					lsp, editor, wm, &mockResourceOpener{}, notify, &mockFileSystem{},
					rootURI, tick, nil, DefaultImplementationConfig(), nil,
				)
			},
		},
		{
			name: "references",
			lsp: func(wait func()) semanticapi.LSP {
				return &mockLSP{referencesFn: func(context.Context, semanticapi.ReferenceParams) ([]semanticapi.Location, error) {
					wait()
					return []semanticapi.Location{loc}, nil
				}}
			},
			newHandler: func(
				lsp semanticapi.LSP, editor textapi.Editor,
				wm browserapi.WindowManager, notify browserapi.Notifications,
				tick func(func()) bool,
			) textapi.CommandHandler {
				return ReferencesHandler(
					lsp, editor, wm, &mockResourceOpener{}, notify, &mockFileSystem{},
					rootURI, tick, nil, DefaultReferencesConfig(), nil,
				)
			},
		},
		{
			name: "hover",
			lsp: func(wait func()) semanticapi.LSP {
				return &hoverMockLSP{mockLSP: &mockLSP{}, hoverFn: func(context.Context, semanticapi.HoverParams) (*semanticapi.Hover, error) {
					wait()
					return &semanticapi.Hover{Contents: semanticapi.MarkupContent{
						Kind:  semanticapi.MarkupKindPlainText,
						Value: "type MyType struct{}",
					}}, nil
				}}
			},
			newHandler: func(
				lsp semanticapi.LSP, _ textapi.Editor,
				wm browserapi.WindowManager, notify browserapi.Notifications,
				tick func(func()) bool,
			) textapi.CommandHandler {
				return HoverHandler(lsp, wm, notify, &mockFileSystem{}, rootURI, tick, nil, DefaultHoverConfig())
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			release := make(chan struct{})
			rec := &tickRecorder{}
			var fetchInTick atomic.Bool
			wait := func() {
				fetchInTick.Store(rec.inTick.Load())
				<-release
			}
			completed := make(chan struct{}, 1)
			signal := func() {
				select {
				case completed <- struct{}{}:
				default:
				}
			}
			editor := &mockEditor{
				editorFn: func(u workspaceapi.URI) (textapi.Handler, error) {
					return &mockHandler{uri: u}, nil
				},
				setCursorFn: func(textapi.Handler, term.Coordinates) error {
					signal()
					return nil
				},
			}
			wm := &mockWindowManager{
				floatingFn: func(browserapi.Floating, browserapi.FloatingConfig) (browserapi.Window, error) {
					signal()
					return nil, nil
				},
			}
			h := tt.newHandler(tt.lsp(wait), editor, wm, &recordingNotifications{}, rec.tick)

			uri, err := workspaceapi.ParseURI("file:///project/a.go")
			require.NoError(t, err)
			cmd := textapi.Command{Name: tt.name, URI: uri, Resource: &mockHandler{uri: uri}}
			cmd.Cursor.Content = term.Coordinates{X: 5, Y: 10}

			handled := make(chan error, 1)
			go func() { handled <- h.HandleCommand(context.Background(), cmd) }()
			select {
			case err := <-handled:
				require.NoError(t, err)
			case <-time.After(5 * time.Second):
				t.Fatal("HandleCommand blocked on the LSP round trip")
			}
			close(release)
			select {
			case <-completed:
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for the async LSP result")
			}
			assert.False(t, fetchInTick.Load(),
				"the LSP fetch must not run inside a scheduled tick")
		})
	}
}

func TestResolvedSymbolBlockingFetchRunsOffTicks(t *testing.T) {
	t.Parallel()

	rootURI, err := workspaceapi.ParseURI("file:///project")
	require.NoError(t, err)
	fileB, err := workspaceapi.ParseURI("file:///project/b.go")
	require.NoError(t, err)

	parser := &mockParser{
		resolveFn: func(name string, _ syntaxapi.Progress) ([]syntaxapi.Match, error) {
			if name == "mylib.MyFunc" {
				return []syntaxapi.Match{
					{URI: fileB.String(), Pos: term.Coordinates{X: 7, Y: 42}},
				}, nil
			}
			return nil, nil
		},
	}
	release := make(chan struct{})
	close(release)
	rec := &tickRecorder{}
	var fetchInTick atomic.Bool
	lsp := &mockLSP{
		definitionFn: func(context.Context, semanticapi.DefinitionParams) (semanticapi.LocationResult, error) {
			fetchInTick.Store(rec.inTick.Load())
			<-release
			return semanticapi.LocationResult{Location: &semanticapi.Location{
				URI:   "file:///project/b.go",
				Range: semanticapi.Range{Start: semanticapi.Position{Line: 42, Character: 7}},
			}}, nil
		},
	}
	navigated := make(chan struct{}, 1)
	editor := &mockEditor{
		editorFn: func(u workspaceapi.URI) (textapi.Handler, error) {
			return &mockHandler{uri: u}, nil
		},
		setCursorFn: func(textapi.Handler, term.Coordinates) error {
			select {
			case navigated <- struct{}{}:
			default:
			}
			return nil
		},
	}
	h := DefinitionHandler(
		lsp, editor, &mockWindowManager{}, &mockResourceOpener{},
		&recordingNotifications{}, &mockFileSystem{},
		rootURI, rec.tick, parser, DefaultDefinitionConfig(), nil,
	)

	cmd := textapi.Command{Name: "definition", Args: []string{"mylib.MyFunc"}}
	require.NoError(t, h.HandleCommand(context.Background(), cmd))
	select {
	case <-navigated:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for async resolution")
	}
	assert.False(t, fetchInTick.Load(),
		"the LSP fetch must not run inside a scheduled tick")
}

func TestSymbolPickerSelectionDoesNotBlockLoop(t *testing.T) {
	t.Parallel()

	parser := &mockParser{
		resolveFn: func(name string, _ syntaxapi.Progress) ([]syntaxapi.Match, error) {
			return []syntaxapi.Match{
				{URI: "file:///project/a.go", Pos: term.Coordinates{X: 1, Y: 2}, Display: "a.go:3"},
				{URI: "file:///project/b.go", Pos: term.Coordinates{X: 4, Y: 5}, Display: "b.go:6"},
			}, nil
		},
	}
	pickerCh := make(chan browserapi.Floating, 1)
	wm := &mockWindowManager{
		floatingFn: func(h browserapi.Floating, _ browserapi.FloatingConfig) (browserapi.Window, error) {
			pickerCh <- h
			return nil, nil
		},
	}
	notify := &recordingNotifications{}
	picked := make(chan struct{})
	release := make(chan struct{})
	onPick := func(_ syntaxapi.Match, done func()) {
		close(picked)
		<-release
		done()
	}

	cmd := &textapi.Command{Args: []string{"mylib.MyFunc"}}
	proceed, err := resolveCommandSymbol(
		context.Background(), cmd, workspaceapi.URI{}, wm, &mockFileSystem{}, notify, syncTick, parser, onPick,
	)
	require.NoError(t, err)
	assert.False(t, proceed)

	var picker *locationpicker.Picker
	select {
	case fh := <-pickerCh:
		picker = fh.(*locationpicker.Picker)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the symbol picker")
	}

	handleReturned := make(chan struct{})
	go func() {
		picker.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
		close(handleReturned)
	}()
	select {
	case <-handleReturned:
	case <-time.After(5 * time.Second):
		t.Fatal("picker selection blocked the event loop on onPick")
	}
	select {
	case <-picked:
	case <-time.After(5 * time.Second):
		t.Fatal("onPick was never invoked")
	}
	close(release)
	require.Eventually(t, func() bool {
		_, updates := notify.snapshot()
		for _, u := range updates {
			if u.step == 4 && u.total == 4 {
				return true
			}
		}
		return false
	}, 5*time.Second, 5*time.Millisecond,
		"done must complete the progress notification")
}

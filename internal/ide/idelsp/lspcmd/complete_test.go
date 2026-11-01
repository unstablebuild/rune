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
	"testing"
	"unicode/utf8"

	"fmt"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/handlertest"
	"github.com/unstablebuild/rune-go-sdk/term"
	runehandlertest "unstable.build/rune/internal/handler/handlertest"
)

var _ browserapi.Floating = (*completionHandler)(nil)

func testItems(labels ...string) []semanticapi.CompletionItem {
	items := make([]semanticapi.CompletionItem, len(labels))
	for i, l := range labels {
		items[i] = semanticapi.CompletionItem{Label: l}
	}
	return items
}

var noIcons map[semanticapi.CompletionItemKind]string

func TestDefaultIcons(t *testing.T) {
	icons := defaultIcons()
	assert.Len(t, icons, 25)

	allKinds := []semanticapi.CompletionItemKind{
		semanticapi.CompletionItemKindText,
		semanticapi.CompletionItemKindMethod,
		semanticapi.CompletionItemKindFunction,
		semanticapi.CompletionItemKindConstructor,
		semanticapi.CompletionItemKindField,
		semanticapi.CompletionItemKindVariable,
		semanticapi.CompletionItemKindClass,
		semanticapi.CompletionItemKindInterface,
		semanticapi.CompletionItemKindModule,
		semanticapi.CompletionItemKindProperty,
		semanticapi.CompletionItemKindUnit,
		semanticapi.CompletionItemKindValue,
		semanticapi.CompletionItemKindEnum,
		semanticapi.CompletionItemKindKeyword,
		semanticapi.CompletionItemKindSnippet,
		semanticapi.CompletionItemKindColor,
		semanticapi.CompletionItemKindFile,
		semanticapi.CompletionItemKindReference,
		semanticapi.CompletionItemKindFolder,
		semanticapi.CompletionItemKindEnumMember,
		semanticapi.CompletionItemKindConstant,
		semanticapi.CompletionItemKindStruct,
		semanticapi.CompletionItemKindEvent,
		semanticapi.CompletionItemKindOperator,
		semanticapi.CompletionItemKindTypeParameter,
	}
	for _, kind := range allKinds {
		_, ok := icons[kind]
		assert.True(t, ok, "missing icon for kind %d", kind)
	}
}

func TestFormatLabel(t *testing.T) {
	icons := map[semanticapi.CompletionItemKind]string{
		semanticapi.CompletionItemKindFunction: "F",
	}
	tests := []struct {
		name string
		item semanticapi.CompletionItem
		want string
	}{
		{
			name: "known kind prepends icon",
			item: semanticapi.CompletionItem{
				Label: "foo",
				Kind:  semanticapi.CompletionItemKindFunction,
			},
			want: "F foo",
		},
		{
			name: "unknown kind returns bare label",
			item: semanticapi.CompletionItem{
				Label: "bar",
				Kind:  semanticapi.CompletionItemKindText,
			},
			want: "bar",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := formatLabel(test.item, icons)
			assert.Equal(t, test.want, got)
		})
	}
}

func TestCompletionHandlerRender(t *testing.T) {
	ch := newCompletionHandler([]string{"alpha", "beta"})
	w, h := ch.Dimensions()
	expected := "alpha  \nbeta   "
	cases := []handlertest.SequenceTestCase{
		{InputSequence: "", Expected: expected},
		{InputSequence: "<down>", Expected: expected},
		{InputSequence: "<up>", Expected: expected},
	}
	handlertest.RunHandlerSequence(t, ch, w, h, cases)
}

func TestCompletionHandlerHandle(t *testing.T) {
	tests := []struct {
		name            string
		giveItems       int
		giveOffset      int
		giveEvent       term.Event
		wantExit        bool
		wantHandled     bool
		wantFocusOffset int
	}{
		{
			name:      "esc exits",
			giveItems: 2,
			giveEvent: term.Event{
				Type: term.EventKey, Key: term.KeyEsc,
			},
			wantExit:    true,
			wantHandled: true,
		},
		{
			name:      "enter exits",
			giveItems: 2,
			giveEvent: term.Event{
				Type: term.EventKey, Key: term.KeyEnter,
			},
			wantExit:    true,
			wantHandled: true,
		},
		{
			name:      "down moves focus",
			giveItems: 3,
			giveEvent: term.Event{
				Type: term.EventKey, Key: term.KeyArrowDown,
			},
			wantHandled:     true,
			wantFocusOffset: 1,
		},
		{
			name:       "up moves focus",
			giveItems:  3,
			giveOffset: 1,
			giveEvent: term.Event{
				Type: term.EventKey, Key: term.KeyArrowUp,
			},
			wantHandled: true,
		},
		{
			name:       "down at bottom stays",
			giveItems:  2,
			giveOffset: 1,
			giveEvent: term.Event{
				Type: term.EventKey, Key: term.KeyArrowDown,
			},
			wantHandled:     true,
			wantFocusOffset: 1,
		},
		{
			name:      "up at top stays",
			giveItems: 2,
			giveEvent: term.Event{
				Type: term.EventKey, Key: term.KeyArrowUp,
			},
			wantHandled: true,
		},
		{
			name:      "ctrl-j moves focus down",
			giveItems: 3,
			giveEvent: term.Event{
				Type: term.EventKey,
				Mod:  term.ModCtrl, Ch: 'j',
			},
			wantHandled:     true,
			wantFocusOffset: 1,
		},
		{
			name:      "ctrl-n moves focus down",
			giveItems: 3,
			giveEvent: term.Event{
				Type: term.EventKey,
				Mod:  term.ModCtrl, Ch: 'n',
			},
			wantHandled:     true,
			wantFocusOffset: 1,
		},
		{
			name:       "ctrl-k moves focus up",
			giveItems:  3,
			giveOffset: 1,
			giveEvent: term.Event{
				Type: term.EventKey,
				Mod:  term.ModCtrl, Ch: 'k',
			},
			wantHandled: true,
		},
		{
			name:       "ctrl-p moves focus up",
			giveItems:  3,
			giveOffset: 1,
			giveEvent: term.Event{
				Type: term.EventKey,
				Mod:  term.ModCtrl, Ch: 'p',
			},
			wantHandled: true,
		},
		{
			name:      "non-key event ignored",
			giveItems: 2,
			giveEvent: term.Event{Type: term.EventMouse},
		},
		{
			name:      "tab exits",
			giveItems: 2,
			giveEvent: term.Event{
				Type: term.EventKey, Key: term.KeyTab,
			},
			wantExit:    true,
			wantHandled: true,
		},
		{
			name:      "unknown key not handled",
			giveItems: 2,
			giveEvent: term.Event{
				Type: term.EventKey, Key: term.KeyBackspace,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			labels := make([]string, test.giveItems)
			for i := range labels {
				labels[i] = "x"
			}
			ch := newCompletionHandler(labels)
			for i := 0; i < test.giveOffset; i++ {
				ch.list.FocusDown()
			}
			exit, handled := ch.Handle(test.giveEvent)
			assert.Equal(t, test.wantExit, exit)
			assert.Equal(t, test.wantHandled, handled)
			assert.Equal(t, test.wantFocusOffset, ch.list.FocusOffset())
		})
	}
}

func TestCompletionHandlerFocus(t *testing.T) {
	tests := []struct {
		name       string
		labels     []string
		pressEnter bool
		offset     int
		wantLabel  string
		wantOK     bool
	}{
		{
			name:       "returns label on enter",
			labels:     []string{"alpha", "beta"},
			pressEnter: true,
			wantLabel:  "alpha",
			wantOK:     true,
		},
		{
			name:       "returns second label after focus down",
			labels:     []string{"alpha", "beta"},
			pressEnter: true,
			offset:     1,
			wantLabel:  "beta",
			wantOK:     true,
		},
		{
			name:   "returns empty on esc",
			labels: []string{"alpha", "beta"},
			wantOK: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ch := newCompletionHandler(test.labels)
			for i := 0; i < test.offset; i++ {
				ch.list.FocusDown()
			}
			if test.pressEnter {
				ch.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
			} else {
				ch.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
			}
			label, ok := ch.Focus()
			assert.Equal(t, test.wantOK, ok)
			assert.Equal(t, test.wantLabel, label)
		})
	}
}

func TestCompletionHandlerDraw(t *testing.T) {
	tests := []struct {
		name            string
		giveLabels      []string
		giveFocusOffset int
		wantFg          []term.Color
	}{
		{
			name:       "first focused",
			giveLabels: []string{"a", "b"},
			wantFg:     []term.Color{term.ColorWhite, term.ColorGray},
		},
		{
			name:            "second focused",
			giveLabels:      []string{"a", "b"},
			giveFocusOffset: 1,
			wantFg:          []term.Color{term.ColorGray, term.ColorWhite},
		},
		{
			name:       "single entry focused",
			giveLabels: []string{"only"},
			wantFg:     []term.Color{term.ColorWhite},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ch := newCompletionHandler(test.giveLabels)
			for i := 0; i < test.giveFocusOffset; i++ {
				ch.list.FocusDown()
			}
			w, h := ch.Dimensions()
			ch.Resize(w, h)
			sw := term.NewStringWriter(w, h)
			ch.Draw(sw)
			require.NoError(t, sw.Flush())

			cells := sw.Cells()
			for i, want := range test.wantFg {
				cell := cells[i*w]
				assert.Equal(t, want, cell.Fg, "entry %d foreground", i)
			}
		})
	}
}

func TestCompletionHandlerDimensions(t *testing.T) {
	tests := []struct {
		name       string
		giveLabels []string
		wantW      int
		wantH      int
	}{
		{
			name:       "width is max label plus 2",
			giveLabels: []string{"short", "longer entry"},
			wantW:      utf8.RuneCountInString("longer entry") + 2,
			wantH:      2,
		},
		{
			name:       "height capped at 15",
			giveLabels: make([]string, 20),
			wantW:      2,
			wantH:      15,
		},
		{
			name:       "single entry",
			giveLabels: []string{"a.go:1"},
			wantW:      8,
			wantH:      1,
		},
		{
			name:       "unicode label uses rune count",
			giveLabels: []string{"café"},
			wantW:      6,
			wantH:      1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ch := newCompletionHandler(test.giveLabels)
			w, h := ch.Dimensions()
			assert.Equal(t, test.wantW, w)
			assert.Equal(t, test.wantH, h)
		})
	}
}

func TestCompletionHandlerApplyItem(t *testing.T) {
	tests := []struct {
		name      string
		giveItems []semanticapi.CompletionItem
		giveLabel string
		wantText  string
		wantEdit  bool
		wantStart term.Coordinates
		wantEnd   term.Coordinates
	}{
		{
			name: "text edit applied",
			giveItems: []semanticapi.CompletionItem{
				{
					Label: "foo",
					TextEdit: &semanticapi.TextEdit{
						Range: semanticapi.Range{
							Start: semanticapi.Position{
								Line: 1, Character: 2,
							},
							End: semanticapi.Position{
								Line: 1, Character: 5,
							},
						},
						NewText: "foobar",
					},
				},
			},
			giveLabel: "foo",
			wantText:  "foobar",
			wantEdit:  true,
			wantStart: term.Coordinates{X: 2, Y: 1},
			wantEnd:   term.Coordinates{X: 5, Y: 1},
		},
		{
			name: "insert text used",
			giveItems: []semanticapi.CompletionItem{
				{Label: "fn", InsertText: "func() {}"},
			},
			giveLabel: "fn",
			wantText:  "func() {}",
			wantEdit:  true,
		},
		{
			name:      "label fallback",
			giveItems: []semanticapi.CompletionItem{{Label: "myVar"}},
			giveLabel: "myVar",
			wantText:  "myVar",
			wantEdit:  true,
		},
		{
			name:      "no match returns nil",
			giveItems: []semanticapi.CompletionItem{{Label: "myVar"}},
			giveLabel: "nonexistent",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var (
				editCalled bool
				editText   string
				editStart  term.Coordinates
				editEnd    term.Coordinates
			)
			editor := &mockEditor{
				cellEditorFn: func(_ textapi.Handler) textapi.CellEditor {
					return &mockCellEditor{
						editFn: func(
							_ context.Context,
							s, e term.Coordinates, text string,
						) (term.Coordinates, term.Coordinates, string, error) {
							editCalled = true
							editText = text
							editStart = s
							editEnd = e
							return term.Coordinates{}, term.Coordinates{}, "", nil
						},
					}
				},
			}
			ch := &completionHandler{
				list:        component.NewFocusList(),
				items:       test.giveItems,
				icons:       noIcons,
				editor:      editor,
				resource:    &mockHandler{},
				interrupter: term.NopInterrupter(),
			}
			err := ch.applyItem(test.giveLabel)
			require.NoError(t, err)
			assert.Equal(t, test.wantEdit, editCalled)
			assert.Equal(t, test.wantText, editText)
			assert.Equal(t, test.wantStart, editStart)
			assert.Equal(t, test.wantEnd, editEnd)
		})
	}
}

func TestCompletionHandlerApplyItemError(t *testing.T) {
	errBoom := errors.New("boom")
	editor := &mockEditor{
		cellEditorFn: func(_ textapi.Handler) textapi.CellEditor {
			return &mockCellEditor{
				editFn: func(
					_ context.Context,
					_, _ term.Coordinates, _ string,
				) (term.Coordinates, term.Coordinates, string, error) {
					return term.Coordinates{}, term.Coordinates{}, "", errBoom
				},
			}
		},
	}

	ch := newCompletionHandler([]string{"x"})
	ch.items = []semanticapi.CompletionItem{{Label: "x"}}
	ch.icons = noIcons
	ch.editor = editor
	ch.resource = &mockHandler{}

	exit, handled := ch.Handle(term.Event{
		Type: term.EventKey, Key: term.KeyEnter,
	})
	assert.True(t, exit, "should still exit on error")
	assert.True(t, handled, "should still be handled on error")
}

func TestCompletionHandlerBackgroundDrain(t *testing.T) {
	labelCh := make(chan string, 3)
	labelCh <- "alpha"
	labelCh <- "beta"
	labelCh <- "gamma"
	close(labelCh)

	drainDone, drainDoneCancel := context.WithCancel(context.Background())
	handler := &completionHandler{
		list:        component.NewFocusList(),
		drainDone:   drainDone,
		interrupter: term.NopInterrupter(),
	}
	go func() {
		defer drainDoneCancel()
		handler.drainLoop(labelCh)
	}()

	// Wait for the drain goroutine to finish — no Handle call needed.
	require.NoError(t, handler.Close())

	assert.Equal(t, 3, handler.list.Len())
	w, h := handler.Dimensions()
	assert.Equal(t, 3, h)
	assert.True(t, w >= utf8.RuneCountInString("gamma")+2)
}

func TestCompletionHandlerApplyItemAsync(t *testing.T) {
	var editText string
	editor := &mockEditor{
		cellEditorFn: func(_ textapi.Handler) textapi.CellEditor {
			return &mockCellEditor{
				editFn: func(
					_ context.Context,
					_, _ term.Coordinates, text string,
				) (term.Coordinates, term.Coordinates, string, error) {
					editText = text
					return term.Coordinates{}, term.Coordinates{}, "", nil
				},
			}
		},
	}

	labelCh := make(chan string, 1)
	drainDone, drainDoneCancel := context.WithCancel(context.Background())

	item := semanticapi.CompletionItem{
		Label:      "myFunc",
		InsertText: "myFunc()",
	}

	handler := &completionHandler{
		list:        component.NewFocusList(),
		drainDone:   drainDone,
		interrupter: term.NopInterrupter(),
		items:       []semanticapi.CompletionItem{item},
		icons:       noIcons,
		editor:      editor,
		resource:    &mockHandler{},
	}
	go func() {
		defer drainDoneCancel()
		handler.drainLoop(labelCh)
	}()

	// Simulate goroutine delivering label.
	labelCh <- "myFunc"
	close(labelCh)

	// Wait for drain goroutine.
	require.NoError(t, handler.Close())

	err := handler.applyItem("myFunc")
	require.NoError(t, err)
	assert.Equal(t, "myFunc()", editText)
}

func TestCompleteHandlerCommandAsync(t *testing.T) {
	items := testItems("alpha", "beta")
	lsp := &mockLSP{
		completionFn: func(
			_ context.Context, _ semanticapi.CompletionParams,
		) (semanticapi.CompletionResult, error) {
			return semanticapi.CompletionResult{Items: items}, nil
		},
	}

	var gotFloating browserapi.Floating
	wm := &mockWindowManager{
		floatingFn: func(
			h browserapi.Floating, _ browserapi.FloatingConfig,
		) (browserapi.Window, error) {
			gotFloating = h
			return &mockWindow{id: 1}, nil
		},
	}

	cfg := DefaultCompleteConfig()
	h := CompleteHandler(lsp, &mockEditor{}, wm, cfg, term.NopInterrupter(), nil)

	cmd := textapi.Command{Resource: &mockHandler{}}
	err := h.HandleCommand(context.Background(), cmd)
	require.NoError(t, err)
	require.NotNil(t, gotFloating)

	t.Cleanup(func() { require.NoError(t, gotFloating.Close()) })
	floating := gotFloating.(*completionHandler)
	<-floating.fetchDone.Done()
	<-floating.drainDone.Done()

	w, height := gotFloating.Dimensions()
	gotFloating.Resize(w, height)
	sw := term.NewStringWriter(w, height)
	for _, step := range []struct {
		name  string
		key   term.Key
		focus int
	}{
		{name: "initial selection", focus: 0},
		{name: "move down", key: term.KeyArrowDown, focus: 1},
		{name: "move up", key: term.KeyArrowUp, focus: 0},
	} {
		t.Run(step.name, func(t *testing.T) {
			if step.key != 0 {
				exit, handled := gotFloating.Handle(term.Event{Type: term.EventKey, Key: step.key})
				assert.False(t, exit)
				require.True(t, handled)
			}
			gotFloating.Draw(sw)
			require.NoError(t, sw.Flush())
			cells := sw.Cells()
			for y, item := range items {
				want := term.Attributes{Fg: term.ColorGray}
				if y == step.focus {
					want = term.Attributes{Fg: term.ColorWhite, Bg: term.ColorGray}
				}
				for x := range w {
					cell := cells[y*w+x]
					assert.Equal(t, want.Bg, cell.Bg, "row %d column %d background", y, x)
					if x < len(item.Label) {
						assert.Equal(t, want.Fg, cell.Fg, "row %d column %d foreground", y, x)
					}
				}
			}
		})
	}
}

func TestCompletionCloseLeavesItsWindow(t *testing.T) {
	lsp := &mockLSP{
		completionFn: func(
			_ context.Context, _ semanticapi.CompletionParams,
		) (semanticapi.CompletionResult, error) {
			return semanticapi.CompletionResult{Items: testItems("alpha")}, nil
		},
	}
	var gotFloating browserapi.Floating
	wm := &mockWindowManager{
		floatingFn: func(
			h browserapi.Floating, _ browserapi.FloatingConfig,
		) (browserapi.Window, error) {
			gotFloating = h
			return &mockWindow{id: 1}, nil
		},
	}
	h := CompleteHandler(lsp, &mockEditor{}, wm, DefaultCompleteConfig(), term.NopInterrupter(), nil)
	require.NoError(t, h.HandleCommand(context.Background(), textapi.Command{Resource: &mockHandler{}}))
	require.NotNil(t, gotFloating)

	require.NoError(t, gotFloating.Close())

	assert.Zero(t, wm.closeWindowCalls,
		"the window may already show other content when the completion is released")
}

func TestCompleteHandlerFetchOutlivesRequestContext(t *testing.T) {
	release := make(chan struct{})
	lsp := &mockLSP{
		completionFn: func(
			ctx context.Context, _ semanticapi.CompletionParams,
		) (semanticapi.CompletionResult, error) {
			<-release
			if err := ctx.Err(); err != nil {
				return semanticapi.CompletionResult{}, err
			}
			return semanticapi.CompletionResult{Items: testItems("alpha", "beta")}, nil
		},
	}
	var gotFloating browserapi.Floating
	wm := &mockWindowManager{
		floatingFn: func(
			h browserapi.Floating, _ browserapi.FloatingConfig,
		) (browserapi.Window, error) {
			gotFloating = h
			return &mockWindow{id: 1}, nil
		},
	}
	h := CompleteHandler(lsp, &mockEditor{}, wm, DefaultCompleteConfig(), term.NopInterrupter(), nil)

	// textrpc cancels the request context once HandleCommand returns.
	ctx, cancel := context.WithCancel(t.Context())
	require.NoError(t, h.HandleCommand(ctx, textapi.Command{Resource: &mockHandler{}}))
	cancel()
	close(release)

	floating, ok := gotFloating.(*completionHandler)
	require.True(t, ok)
	<-floating.fetchDone.Done()
	<-floating.drainDone.Done()
	floating.mu.Lock()
	labels := len(floating.labels)
	floating.mu.Unlock()
	assert.Equal(t, 2, labels)
	require.NoError(t, floating.Close())
}

func TestCompletionHandlerClose(t *testing.T) {
	fetchCtx, fetchCancel := context.WithCancel(context.Background())
	fetchDone, fetchDoneCancel := context.WithCancel(context.Background())

	ch := &completionHandler{
		list:        component.NewFocusList(),
		cancel:      fetchCancel,
		fetchDone:   fetchDone,
		interrupter: term.NopInterrupter(),
	}

	var goroutineExited bool
	go func() {
		defer fetchDoneCancel()
		<-fetchCtx.Done()
		goroutineExited = true
	}()

	err := ch.Close()
	require.NoError(t, err)
	assert.True(t, goroutineExited)
}

func TestCompleteHandlerCommand(t *testing.T) {
	tests := []struct {
		name         string
		giveItems    []semanticapi.CompletionItem
		wantFloating bool
	}{
		{
			name:         "empty results still show floating",
			wantFloating: true,
		},
		{
			name:         "non-empty results show floating",
			giveItems:    testItems("alpha", "beta"),
			wantFloating: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lsp := &mockLSP{
				completionFn: func(
					_ context.Context, _ semanticapi.CompletionParams,
				) (semanticapi.CompletionResult, error) {
					return semanticapi.CompletionResult{
						Items: test.giveItems,
					}, nil
				},
			}
			var floatingCalled bool
			wm := &mockWindowManager{
				floatingFn: func(
					_ browserapi.Floating, _ browserapi.FloatingConfig,
				) (browserapi.Window, error) {
					floatingCalled = true
					return &mockWindow{id: 1}, nil
				},
			}
			cfg := DefaultCompleteConfig()
			h := CompleteHandler(lsp, &mockEditor{}, wm, cfg, term.NopInterrupter(), nil)
			cmd := textapi.Command{Resource: &mockHandler{}}
			err := h.HandleCommand(context.Background(), cmd)
			require.NoError(t, err)
			assert.Equal(t, test.wantFloating, floatingCalled)
		})
	}
}

func TestCompletionHandlerPointerLeaves(t *testing.T) {
	t.Parallel()
	labels := make([]string, 60)
	for i := range labels {
		labels[i] = fmt.Sprintf("ƒ completionCandidate%02d(ctx context.Context) error", i)
	}
	for _, size := range []struct{ w, h int }{{60, 15}, {1, 1}} {
		ch := newCompletionHandler(labels)
		ch.list.FocusDown()
		ch.list.FocusDown()
		runehandlertest.RunPointerLeaves(t, ch, size.w, size.h)
		assert.Equal(t, 2, ch.list.FocusOffset(), "the pointer must not move the focus")
		_, ok := ch.Focus()
		assert.False(t, ok, "the pointer must not accept a completion")
	}
}

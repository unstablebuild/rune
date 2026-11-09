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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component/comptest"
	"github.com/unstablebuild/rune-go-sdk/term"
	runehandlertest "unstable.build/rune/internal/handler/handlertest"
)

func testSignatureHelp(sigs []semanticapi.SignatureInformation, activeSig, activeParam uint32) *semanticapi.SignatureHelp {
	return &semanticapi.SignatureHelp{
		Signatures:      sigs,
		ActiveSignature: activeSig,
		ActiveParameter: activeParam,
	}
}

func TestSignatureHelpFloatingHandle(t *testing.T) {
	t.Parallel()
	result := testSignatureHelp([]semanticapi.SignatureInformation{
		{Label: "foo(a int, b string)"},
		{Label: "foo(a int)"},
	}, 0, 0)
	f := newSignatureHelpFloating(result, DefaultSignatureHelpConfig())

	// Esc dismisses.
	exit, handled := f.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
	assert.True(t, exit, "Esc should exit")
	assert.True(t, handled, "Esc should be handled")

	// Up at index 0 clamps.
	f.activeIdx = 0
	exit, handled = f.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowUp})
	assert.False(t, exit, "Up should not exit")
	assert.True(t, handled, "Up should be handled")
	assert.Equal(t, 0, f.activeIdx)

	// Down increments.
	exit, handled = f.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowDown})
	assert.False(t, exit, "Down should not exit")
	assert.True(t, handled, "Down should be handled")
	assert.Equal(t, 1, f.activeIdx)

	// Down at max clamps.
	exit, handled = f.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowDown})
	assert.False(t, exit, "Down at max should not exit")
	assert.True(t, handled, "Down at max should be handled")
	assert.Equal(t, 1, f.activeIdx)

	// Any other key dismisses.
	exit, handled = f.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	assert.True(t, exit, "Enter should exit")
	assert.True(t, handled, "Enter should be handled")

	// Mouse events ignored.
	exit, handled = f.Handle(term.Event{Type: term.EventMouse})
	assert.False(t, exit, "mouse should not exit")
	assert.False(t, handled, "mouse should not be handled")
}

func TestSignatureHelpFloatingDimensions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		sigs  []semanticapi.SignatureInformation
		aSig  uint32
		aPar  uint32
		wantW int
		wantH int
	}{
		{
			name: "single sig no param doc",
			sigs: []semanticapi.SignatureInformation{
				{Label: "foo(a int)", Parameters: []semanticapi.ParameterInformation{{Label: "a int"}}},
			},
			wantW: 10, // len("foo(a int)")
			wantH: 1,
		},
		{
			name: "single sig with param doc",
			sigs: []semanticapi.SignatureInformation{
				{
					Label: "foo(a int)",
					Parameters: []semanticapi.ParameterInformation{
						{Label: "a int", DocumentationString: "the first arg"},
					},
				},
			},
			aPar:  0,
			wantW: 13, // len("the first arg")
			wantH: 2,
		},
		{
			name: "multiple sigs include counter in width",
			sigs: []semanticapi.SignatureInformation{
				{Label: "foo(a int)"},
				{Label: "foo(a int, b string)"},
			},
			wantW: 10 + 4, // "foo(a int)" + " 1/2"
			wantH: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := testSignatureHelp(test.sigs, test.aSig, test.aPar)
			f := newSignatureHelpFloating(result, DefaultSignatureHelpConfig())
			w, h := f.Dimensions()
			assert.Equal(t, test.wantW, w, "width")
			assert.Equal(t, test.wantH, h, "height")
		})
	}
}

func TestSignatureHelpFloatingDraw(t *testing.T) {
	t.Parallel()
	cfg := DefaultSignatureHelpConfig()
	sig := semanticapi.SignatureInformation{
		Label: "foo(a int, b string)",
		Parameters: []semanticapi.ParameterInformation{
			{Label: "a int"},
			{Label: "b string", DocumentationString: "second param"},
		},
	}
	result := testSignatureHelp([]semanticapi.SignatureInformation{sig}, 0, 1)
	f := newSignatureHelpFloating(result, cfg)
	w, h := f.Dimensions()
	f.Resize(w, h)

	sw := term.NewStringWriter(w, h)
	comptest.TestComponent(t, f, sw, []comptest.TestCase{
		{
			Expected: "foo(a int, b string)\nsecond param        ",
		},
	})

	// Verify bold+underline on the active parameter range.
	// "b string" starts at rune index 11, ends at 19.
	cells := sw.Cells()
	for x := 11; x < 19; x++ {
		cell := cells[x]
		assert.NotZero(t, cell.Attrs&term.AttrBold, "cell %d should be bold", x)
		assert.NotZero(t, cell.Attrs&term.AttrUnderline, "cell %d should be underline", x)
	}
	// Characters outside the active param range should NOT have bold.
	assert.Zero(t, cells[0].Attrs&term.AttrBold, "cell 0 should not be bold")

	// Second line should have the param documentation in gray.
	require.True(t, h >= 2, "should have 2 lines for param doc")
	docCell := cells[w] // first cell of second row
	assert.Equal(t, term.ColorGray, docCell.Fg, "param doc should be gray")
}

func TestSignatureHelpFloatingOverloadCycling(t *testing.T) {
	t.Parallel()
	sigs := []semanticapi.SignatureInformation{
		{Label: "foo(a int)"},
		{Label: "foo(a int, b string)"},
		{Label: "foo(a int, b string, c bool)"},
	}
	result := testSignatureHelp(sigs, 0, 0)
	f := newSignatureHelpFloating(result, DefaultSignatureHelpConfig())
	assert.Equal(t, 0, f.activeIdx)

	moves := []struct {
		ev   term.Event
		want int
	}{
		{term.Event{Type: term.EventKey, Key: term.KeyArrowDown}, 1},
		{term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'n'}, 2},
		{term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'p'}, 1},
		{term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'j'}, 2},
		{term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'k'}, 1},
		{term.Event{Type: term.EventKey, Key: term.KeyArrowUp}, 0},
	}
	for _, move := range moves {
		_, handled := f.Handle(move.ev)
		assert.True(t, handled)
		assert.Equal(t, move.want, f.activeIdx)
	}

	// Leave the second signature active for the dimensions assertion.
	f.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'n'})

	// Dimensions reflect the currently active signature.
	w, _ := f.Dimensions()
	// "foo(a int, b string)" = 20 + " 2/3" = 4 → 24
	assert.Equal(t, 24, w)
}

func TestFindParamRuneRange(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		sig       semanticapi.SignatureInformation
		paramIdx  int
		wantStart int
		wantEnd   int
	}{
		{
			name: "basic match",
			sig: semanticapi.SignatureInformation{
				Label:      "foo(a int, b string)",
				Parameters: []semanticapi.ParameterInformation{{Label: "a int"}, {Label: "b string"}},
			},
			paramIdx:  0,
			wantStart: 4,
			wantEnd:   9,
		},
		{
			name: "second param",
			sig: semanticapi.SignatureInformation{
				Label:      "foo(a int, b string)",
				Parameters: []semanticapi.ParameterInformation{{Label: "a int"}, {Label: "b string"}},
			},
			paramIdx:  1,
			wantStart: 11,
			wantEnd:   19,
		},
		{
			name: "no match",
			sig: semanticapi.SignatureInformation{
				Label:      "foo(a int)",
				Parameters: []semanticapi.ParameterInformation{{Label: "z float64"}},
			},
			paramIdx:  0,
			wantStart: -1,
			wantEnd:   -1,
		},
		{
			name: "out of range index",
			sig: semanticapi.SignatureInformation{
				Label:      "foo(a int)",
				Parameters: []semanticapi.ParameterInformation{{Label: "a int"}},
			},
			paramIdx:  5,
			wantStart: -1,
			wantEnd:   -1,
		},
		{
			name: "unicode label",
			sig: semanticapi.SignatureInformation{
				Label:      "fn(café string)",
				Parameters: []semanticapi.ParameterInformation{{Label: "café string"}},
			},
			paramIdx:  0,
			wantStart: 3,
			wantEnd:   14,
		},
		{
			name: "label offsets",
			sig: semanticapi.SignatureInformation{
				Label: "foo(a int, b string)",
				Parameters: []semanticapi.ParameterInformation{
					{LabelOffsets: &[2]uint32{4, 9}},
					{LabelOffsets: &[2]uint32{11, 19}},
				},
			},
			paramIdx:  1,
			wantStart: 11,
			wantEnd:   19,
		},
		{
			name: "empty param label",
			sig: semanticapi.SignatureInformation{
				Label:      "foo(a int)",
				Parameters: []semanticapi.ParameterInformation{{Label: ""}},
			},
			paramIdx:  0,
			wantStart: -1,
			wantEnd:   -1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			start, end := findParamRuneRange(test.sig, test.paramIdx)
			assert.Equal(t, test.wantStart, start, "start")
			assert.Equal(t, test.wantEnd, end, "end")
		})
	}
}

func TestSignatureHelpHandlerCommand(t *testing.T) {
	t.Parallel()
	sig := semanticapi.SignatureInformation{
		Label:      "foo(a int, b string)",
		Parameters: []semanticapi.ParameterInformation{{Label: "a int"}, {Label: "b string"}},
	}
	lsp := &mockLSP{
		signatureHelpFn: func(
			_ context.Context, _ semanticapi.SignatureHelpParams,
		) (*semanticapi.SignatureHelp, error) {
			return testSignatureHelp([]semanticapi.SignatureInformation{sig}, 0, 0), nil
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
	h := SignatureHelpHandler(lsp, &mockEditor{}, wm, nil, DefaultSignatureHelpConfig(), nil)
	cmd := textapi.Command{Resource: &mockHandler{}}
	err := h.HandleCommand(context.Background(), cmd)
	require.NoError(t, err)
	assert.True(t, floatingCalled, "should open floating window")
}

func TestSignatureHelpCloseLeavesItsWindow(t *testing.T) {
	t.Parallel()
	sig := semanticapi.SignatureInformation{Label: "foo(a int)"}
	lsp := &mockLSP{
		signatureHelpFn: func(
			_ context.Context, _ semanticapi.SignatureHelpParams,
		) (*semanticapi.SignatureHelp, error) {
			return testSignatureHelp([]semanticapi.SignatureInformation{sig}, 0, 0), nil
		},
	}
	var floated browserapi.Floating
	wm := &mockWindowManager{
		floatingFn: func(
			f browserapi.Floating, _ browserapi.FloatingConfig,
		) (browserapi.Window, error) {
			floated = f
			return &mockWindow{id: 1}, nil
		},
	}
	h := SignatureHelpHandler(lsp, &mockEditor{}, wm, nil, DefaultSignatureHelpConfig(), nil)
	require.NoError(t, h.HandleCommand(context.Background(), textapi.Command{Resource: &mockHandler{}}))
	require.NotNil(t, floated)

	require.NoError(t, floated.Close())

	assert.Zero(t, wm.closeWindowCalls,
		"the window may already show other content when the signature help is released")
}

func TestSignatureHelpHandlerNilResult(t *testing.T) {
	t.Parallel()
	lsp := &mockLSP{
		signatureHelpFn: func(
			_ context.Context, _ semanticapi.SignatureHelpParams,
		) (*semanticapi.SignatureHelp, error) {
			return nil, nil
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
	h := SignatureHelpHandler(lsp, &mockEditor{}, wm, nil, DefaultSignatureHelpConfig(), nil)
	cmd := textapi.Command{Resource: &mockHandler{}}
	err := h.HandleCommand(context.Background(), cmd)
	require.NoError(t, err)
	assert.False(t, floatingCalled, "should not open floating for nil result")
}

func TestSignatureHelpHandlerEmptySignatures(t *testing.T) {
	t.Parallel()
	lsp := &mockLSP{
		signatureHelpFn: func(
			_ context.Context, _ semanticapi.SignatureHelpParams,
		) (*semanticapi.SignatureHelp, error) {
			return &semanticapi.SignatureHelp{}, nil
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
	h := SignatureHelpHandler(lsp, &mockEditor{}, wm, nil, DefaultSignatureHelpConfig(), nil)
	cmd := textapi.Command{Resource: &mockHandler{}}
	err := h.HandleCommand(context.Background(), cmd)
	require.NoError(t, err)
	assert.False(t, floatingCalled, "should not open floating for empty signatures")
}

func TestSignatureHelpAutoTriggerDisabled(t *testing.T) {
	t.Parallel()
	var subscribeCalled bool
	editor := &mockEditor{
		subscribeEventsFn: func(
			_ []textapi.EventType, _ textapi.EventHandler,
		) error {
			subscribeCalled = true
			return nil
		},
	}
	cfg := DefaultSignatureHelpConfig()
	cfg.AutoTrigger = false
	cfg.TriggerCharacters = []string{"(", ","}
	SignatureHelpHandler(&mockLSP{}, editor, &mockWindowManager{}, nil, cfg, nil)
	assert.False(t, subscribeCalled, "SubscribeEvents should not be called when AutoTrigger is false")
}

func TestSignatureHelpAutoTriggerNoMatch(t *testing.T) {
	t.Parallel()
	var capturedHandler textapi.EventHandler
	var setLocationCalled bool
	editor := &mockEditor{
		subscribeEventsFn: func(
			_ []textapi.EventType, h textapi.EventHandler,
		) error {
			capturedHandler = h
			return nil
		},
		editorFn: func(uri workspaceapi.URI) (textapi.Handler, error) {
			return &mockHandler{uri: uri}, nil
		},
		setLocationListFn: func(_ textapi.Handler, _ textapi.LocationPriority, _ string, _ textapi.LocationList) error {
			setLocationCalled = true
			return nil
		},
	}
	cfg := DefaultSignatureHelpConfig()
	cfg.TriggerCharacters = []string{"(", ","}
	SignatureHelpHandler(&mockLSP{}, editor, &mockWindowManager{}, syncTick, cfg, nil)
	require.NotNil(t, capturedHandler, "handler should be subscribed")

	// Fire an edit event with a non-trigger character.
	done := capturedHandler.Handle(context.Background(), textapi.Event{
		Type:    textapi.EventTypeEdit,
		Content: "x",
	})
	assert.False(t, done)
	assert.False(t, setLocationCalled, "SetLocationList should not be called for non-trigger char")
}

func TestSignatureHelpAutoTriggerNilResult(t *testing.T) {
	t.Parallel()
	lsp := &mockLSP{
		signatureHelpFn: func(
			_ context.Context, _ semanticapi.SignatureHelpParams,
		) (*semanticapi.SignatureHelp, error) {
			return nil, nil
		},
	}
	var mu sync.Mutex
	var gotList textapi.LocationList
	var setLocationCalled bool
	var capturedHandler textapi.EventHandler
	editor := &mockEditor{
		subscribeEventsFn: func(
			_ []textapi.EventType, h textapi.EventHandler,
		) error {
			capturedHandler = h
			return nil
		},
		editorFn: func(uri workspaceapi.URI) (textapi.Handler, error) {
			return &mockHandler{uri: uri}, nil
		},
		setLocationListFn: func(_ textapi.Handler, _ textapi.LocationPriority, _ string, list textapi.LocationList) error {
			mu.Lock()
			gotList = list
			setLocationCalled = true
			mu.Unlock()
			return nil
		},
	}
	cfg := DefaultSignatureHelpConfig()
	cfg.TriggerCharacters = []string{"(", ","}
	SignatureHelpHandler(lsp, editor, &mockWindowManager{}, syncTick, cfg, nil)
	require.NotNil(t, capturedHandler, "handler should be subscribed")

	// Fire an edit event with a trigger character but LSP returns nil.
	done := capturedHandler.Handle(context.Background(), textapi.Event{
		Type:    textapi.EventTypeEdit,
		Content: "(",
	})
	assert.False(t, done)

	// The fetch runs asynchronously. When LSP returns nil the
	// location should be cleared (nil list).
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return setLocationCalled
	}, time.Second, 10*time.Millisecond, "SetLocationList should be called to clear")

	mu.Lock()
	defer mu.Unlock()
	assert.Nil(t, gotList, "location list should be nil when LSP returns nil")
}

func TestSignatureHelpAutoTriggerSetsLocation(t *testing.T) {
	t.Parallel()
	sig := semanticapi.SignatureInformation{
		Label: "foo(a int, b string)",
		Parameters: []semanticapi.ParameterInformation{
			{Label: "a int"},
			{Label: "b string"},
		},
	}
	lsp := &mockLSP{
		signatureHelpFn: func(
			_ context.Context, _ semanticapi.SignatureHelpParams,
		) (*semanticapi.SignatureHelp, error) {
			return testSignatureHelp([]semanticapi.SignatureInformation{sig}, 0, 0), nil
		},
	}

	var mu sync.Mutex
	var gotList textapi.LocationList
	var gotID string
	var capturedHandler textapi.EventHandler
	editor := &mockEditor{
		subscribeEventsFn: func(
			_ []textapi.EventType, h textapi.EventHandler,
		) error {
			capturedHandler = h
			return nil
		},
		editorFn: func(uri workspaceapi.URI) (textapi.Handler, error) {
			return &mockHandler{uri: uri}, nil
		},
		setLocationListFn: func(_ textapi.Handler, _ textapi.LocationPriority, id string, list textapi.LocationList) error {
			mu.Lock()
			gotList = list
			gotID = id
			mu.Unlock()
			return nil
		},
	}

	cfg := DefaultSignatureHelpConfig()
	cfg.TriggerCharacters = []string{"(", ","}
	SignatureHelpHandler(lsp, editor, &mockWindowManager{}, syncTick, cfg, nil)
	require.NotNil(t, capturedHandler, "handler should be subscribed")

	wsURI, err := workspaceapi.ParseURI("file:///test.go")
	require.NoError(t, err)

	done := capturedHandler.Handle(context.Background(), textapi.Event{
		Type:    textapi.EventTypeEdit,
		URI:     wsURI,
		Content: "(",
		From:    term.Coordinates{X: 3, Y: 10},
		To:      term.Coordinates{X: 4, Y: 10},
	})
	assert.False(t, done)

	// Wait for the async fetch to complete.
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return gotList != nil
	}, time.Second, 10*time.Millisecond, "SetLocationList should be called")

	mu.Lock()
	defer mu.Unlock()

	assert.Equal(t, signatureHelpLocationID, gotID)
	loc, ok := gotList.Current()
	require.True(t, ok, "location list should have an entry")
	assert.Equal(t, term.Coordinates{X: 4, Y: 10}, loc.From, "From should be ev.To")
	assert.Equal(t, term.Coordinates{X: 5, Y: 10}, loc.To, "To should be one past From")
	assert.Equal(t, term.Attributes{}, loc.Attr, "attr should be empty")
	assert.Contains(t, loc.Message, "**", "message should contain bold markers")
	assert.Contains(t, loc.Message, "a int", "message should contain param")
}

func TestSignatureHelpCursorClearsLocation(t *testing.T) {
	t.Parallel()
	sig := semanticapi.SignatureInformation{
		Label: "foo(a int, b string)",
		Parameters: []semanticapi.ParameterInformation{
			{Label: "a int"},
			{Label: "b string"},
		},
	}
	lsp := &mockLSP{
		signatureHelpFn: func(
			_ context.Context, _ semanticapi.SignatureHelpParams,
		) (*semanticapi.SignatureHelp, error) {
			return testSignatureHelp([]semanticapi.SignatureInformation{sig}, 0, 0), nil
		},
	}

	var mu sync.Mutex
	var calls []textapi.LocationList
	var capturedHandler textapi.EventHandler
	editor := &mockEditor{
		subscribeEventsFn: func(
			_ []textapi.EventType, h textapi.EventHandler,
		) error {
			capturedHandler = h
			return nil
		},
		editorFn: func(uri workspaceapi.URI) (textapi.Handler, error) {
			return &mockHandler{uri: uri}, nil
		},
		setLocationListFn: func(_ textapi.Handler, _ textapi.LocationPriority, _ string, list textapi.LocationList) error {
			mu.Lock()
			calls = append(calls, list)
			mu.Unlock()
			return nil
		},
	}

	cfg := DefaultSignatureHelpConfig()
	cfg.TriggerCharacters = []string{"(", ","}
	SignatureHelpHandler(lsp, editor, &mockWindowManager{}, syncTick, cfg, nil)
	require.NotNil(t, capturedHandler, "handler should be subscribed")

	wsURI, err := workspaceapi.ParseURI("file:///test.go")
	require.NoError(t, err)

	// First trigger the edit so a location is set.
	capturedHandler.Handle(context.Background(), textapi.Event{
		Type:    textapi.EventTypeEdit,
		URI:     wsURI,
		Content: "(",
		From:    term.Coordinates{X: 3, Y: 10},
		To:      term.Coordinates{X: 4, Y: 10},
	})

	// Wait for the async fetch to set the location.
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(calls) > 0 && calls[len(calls)-1] != nil
	}, time.Second, 10*time.Millisecond, "location should be set first")

	// The cursor event that immediately follows the edit is
	// suppressed (the edit flag absorbs it). Fire it first.
	capturedHandler.Handle(context.Background(), textapi.Event{
		Type: textapi.EventTypeCursor,
		URI:  wsURI,
		From: term.Coordinates{X: 4, Y: 10},
	})

	// A second cursor event (pure navigation) should clear.
	capturedHandler.Handle(context.Background(), textapi.Event{
		Type: textapi.EventTypeCursor,
		URI:  wsURI,
		From: term.Coordinates{X: 5, Y: 10},
	})

	mu.Lock()
	lastCall := calls[len(calls)-1]
	mu.Unlock()
	assert.Nil(t, lastCall, "navigation cursor should clear the location list")
}

func TestSignatureHelpNonTriggerEditKeepsLocation(t *testing.T) {
	t.Parallel()
	sig := semanticapi.SignatureInformation{
		Label: "foo(a int, b string)",
		Parameters: []semanticapi.ParameterInformation{
			{Label: "a int"},
			{Label: "b string"},
		},
	}
	lsp := &mockLSP{
		signatureHelpFn: func(
			_ context.Context, _ semanticapi.SignatureHelpParams,
		) (*semanticapi.SignatureHelp, error) {
			return testSignatureHelp([]semanticapi.SignatureInformation{sig}, 0, 1), nil
		},
	}

	var mu sync.Mutex
	var calls []textapi.LocationList
	var capturedHandler textapi.EventHandler
	editor := &mockEditor{
		subscribeEventsFn: func(
			_ []textapi.EventType, h textapi.EventHandler,
		) error {
			capturedHandler = h
			return nil
		},
		editorFn: func(uri workspaceapi.URI) (textapi.Handler, error) {
			return &mockHandler{uri: uri}, nil
		},
		setLocationListFn: func(_ textapi.Handler, _ textapi.LocationPriority, _ string, list textapi.LocationList) error {
			mu.Lock()
			calls = append(calls, list)
			mu.Unlock()
			return nil
		},
	}

	cfg := DefaultSignatureHelpConfig()
	cfg.TriggerCharacters = []string{"(", ","}
	SignatureHelpHandler(lsp, editor, &mockWindowManager{}, syncTick, cfg, nil)
	require.NotNil(t, capturedHandler, "handler should be subscribed")

	wsURI, err := workspaceapi.ParseURI("file:///test.go")
	require.NoError(t, err)

	// Type "," — a trigger character — to set the signature help location.
	capturedHandler.Handle(context.Background(), textapi.Event{
		Type:    textapi.EventTypeEdit,
		URI:     wsURI,
		Content: ",",
		From:    term.Coordinates{X: 10, Y: 5},
		To:      term.Coordinates{X: 11, Y: 5},
	})
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(calls) > 0 && calls[len(calls)-1] != nil
	}, time.Second, 10*time.Millisecond, "location should be set after trigger")

	// Record the call count before the space edit.
	mu.Lock()
	callsBefore := len(calls)
	mu.Unlock()

	// Type " " (space) to format the next argument. This is a
	// non-trigger edit that should re-fetch and keep the location.
	capturedHandler.Handle(context.Background(), textapi.Event{
		Type:    textapi.EventTypeEdit,
		URI:     wsURI,
		Content: " ",
		From:    term.Coordinates{X: 11, Y: 5},
		To:      term.Coordinates{X: 12, Y: 5},
	})

	// The cursor event that follows the space edit.
	capturedHandler.Handle(context.Background(), textapi.Event{
		Type: textapi.EventTypeCursor,
		URI:  wsURI,
		From: term.Coordinates{X: 12, Y: 5},
	})

	// Wait for the re-fetch to complete.
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(calls) > callsBefore
	}, time.Second, 10*time.Millisecond, "re-fetch should update location")

	mu.Lock()
	lastCall := calls[len(calls)-1]
	mu.Unlock()
	assert.NotNil(t, lastCall, "space edit should re-fetch and keep the location")
}

func TestSignatureHelpNonTriggerEditClearsWhenLSPReturnsNil(t *testing.T) {
	t.Parallel()
	sig := semanticapi.SignatureInformation{
		Label: "foo(a int, b string)",
		Parameters: []semanticapi.ParameterInformation{
			{Label: "a int"},
			{Label: "b string"},
		},
	}
	var fetchCount int32
	lsp := &mockLSP{
		signatureHelpFn: func(
			_ context.Context, _ semanticapi.SignatureHelpParams,
		) (*semanticapi.SignatureHelp, error) {
			n := atomic.AddInt32(&fetchCount, 1)
			if n == 1 {
				// First call (trigger char): return signature.
				return testSignatureHelp([]semanticapi.SignatureInformation{sig}, 0, 0), nil
			}
			// Subsequent calls (non-trigger): cursor left the call.
			return nil, nil
		},
	}

	var mu sync.Mutex
	var calls []textapi.LocationList
	var capturedHandler textapi.EventHandler
	editor := &mockEditor{
		subscribeEventsFn: func(
			_ []textapi.EventType, h textapi.EventHandler,
		) error {
			capturedHandler = h
			return nil
		},
		editorFn: func(uri workspaceapi.URI) (textapi.Handler, error) {
			return &mockHandler{uri: uri}, nil
		},
		setLocationListFn: func(_ textapi.Handler, _ textapi.LocationPriority, _ string, list textapi.LocationList) error {
			mu.Lock()
			calls = append(calls, list)
			mu.Unlock()
			return nil
		},
	}

	cfg := DefaultSignatureHelpConfig()
	cfg.TriggerCharacters = []string{"(", ","}
	SignatureHelpHandler(lsp, editor, &mockWindowManager{}, syncTick, cfg, nil)
	require.NotNil(t, capturedHandler, "handler should be subscribed")

	wsURI, err := workspaceapi.ParseURI("file:///test.go")
	require.NoError(t, err)

	// Type "(" to activate signature help.
	capturedHandler.Handle(context.Background(), textapi.Event{
		Type:    textapi.EventTypeEdit,
		URI:     wsURI,
		Content: "(",
		From:    term.Coordinates{X: 3, Y: 10},
		To:      term.Coordinates{X: 4, Y: 10},
	})
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(calls) > 0 && calls[len(calls)-1] != nil
	}, time.Second, 10*time.Millisecond, "location should be set")

	mu.Lock()
	callsBefore := len(calls)
	mu.Unlock()

	// Type ")" — non-trigger, but signature help is active, so a
	// re-fetch fires. The LSP returns nil → location should clear.
	capturedHandler.Handle(context.Background(), textapi.Event{
		Type:    textapi.EventTypeEdit,
		URI:     wsURI,
		Content: ")",
		From:    term.Coordinates{X: 4, Y: 10},
		To:      term.Coordinates{X: 5, Y: 10},
	})

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(calls) > callsBefore
	}, time.Second, 10*time.Millisecond, "re-fetch should update location")

	mu.Lock()
	lastCall := calls[len(calls)-1]
	mu.Unlock()
	assert.Nil(t, lastCall, "LSP returning nil should clear the location")
}

func TestSignatureHelpNavigationCursorClearsLocation(t *testing.T) {
	t.Parallel()
	sig := semanticapi.SignatureInformation{
		Label: "foo(a int, b string)",
		Parameters: []semanticapi.ParameterInformation{
			{Label: "a int"},
			{Label: "b string"},
		},
	}
	lsp := &mockLSP{
		signatureHelpFn: func(
			_ context.Context, _ semanticapi.SignatureHelpParams,
		) (*semanticapi.SignatureHelp, error) {
			return testSignatureHelp([]semanticapi.SignatureInformation{sig}, 0, 0), nil
		},
	}

	var mu sync.Mutex
	var calls []textapi.LocationList
	var capturedHandler textapi.EventHandler
	editor := &mockEditor{
		subscribeEventsFn: func(
			_ []textapi.EventType, h textapi.EventHandler,
		) error {
			capturedHandler = h
			return nil
		},
		editorFn: func(uri workspaceapi.URI) (textapi.Handler, error) {
			return &mockHandler{uri: uri}, nil
		},
		setLocationListFn: func(_ textapi.Handler, _ textapi.LocationPriority, _ string, list textapi.LocationList) error {
			mu.Lock()
			calls = append(calls, list)
			mu.Unlock()
			return nil
		},
	}

	cfg := DefaultSignatureHelpConfig()
	cfg.TriggerCharacters = []string{"(", ","}
	SignatureHelpHandler(lsp, editor, &mockWindowManager{}, syncTick, cfg, nil)
	require.NotNil(t, capturedHandler, "handler should be subscribed")

	wsURI, err := workspaceapi.ParseURI("file:///test.go")
	require.NoError(t, err)

	// Trigger signature help with "(".
	capturedHandler.Handle(context.Background(), textapi.Event{
		Type:    textapi.EventTypeEdit,
		URI:     wsURI,
		Content: "(",
		From:    term.Coordinates{X: 3, Y: 10},
		To:      term.Coordinates{X: 4, Y: 10},
	})
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(calls) > 0 && calls[len(calls)-1] != nil
	}, time.Second, 10*time.Millisecond, "location should be set first")

	// Cursor event from the edit itself — should NOT clear.
	capturedHandler.Handle(context.Background(), textapi.Event{
		Type: textapi.EventTypeCursor,
		URI:  wsURI,
		From: term.Coordinates{X: 4, Y: 10},
	})
	mu.Lock()
	afterEditCursor := calls[len(calls)-1]
	mu.Unlock()
	assert.NotNil(t, afterEditCursor, "cursor from edit should not clear")

	// Pure navigation cursor (arrow key) — should clear.
	capturedHandler.Handle(context.Background(), textapi.Event{
		Type: textapi.EventTypeCursor,
		URI:  wsURI,
		From: term.Coordinates{X: 5, Y: 10},
	})
	mu.Lock()
	afterNavCursor := calls[len(calls)-1]
	mu.Unlock()
	assert.Nil(t, afterNavCursor, "navigation cursor should clear the location list")
}

func TestFormatSignatureMessage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		help *semanticapi.SignatureHelp
		want string
	}{
		{
			name: "single sig no active param",
			help: testSignatureHelp([]semanticapi.SignatureInformation{
				{Label: "foo()"},
			}, 0, 0),
			want: "foo()",
		},
		{
			name: "active param with bold",
			help: testSignatureHelp([]semanticapi.SignatureInformation{
				{
					Label: "foo(a int, b string)",
					Parameters: []semanticapi.ParameterInformation{
						{Label: "a int"},
						{Label: "b string"},
					},
				},
			}, 0, 0),
			want: "foo(**a int**, b string)",
		},
		{
			name: "second param bold",
			help: testSignatureHelp([]semanticapi.SignatureInformation{
				{
					Label: "foo(a int, b string)",
					Parameters: []semanticapi.ParameterInformation{
						{Label: "a int"},
						{Label: "b string"},
					},
				},
			}, 0, 1),
			want: "foo(a int, **b string**)",
		},
		{
			name: "multiple sigs with counter",
			help: testSignatureHelp([]semanticapi.SignatureInformation{
				{Label: "foo(a int)"},
				{Label: "foo(a int, b string)"},
			}, 0, 0),
			want: "foo(a int) 1/2",
		},
		{
			name: "with param doc",
			help: testSignatureHelp([]semanticapi.SignatureInformation{
				{
					Label: "foo(a int)",
					Parameters: []semanticapi.ParameterInformation{
						{Label: "a int", DocumentationString: "the first arg"},
					},
				},
			}, 0, 0),
			want: "foo(**a int**)\nthe first arg",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatSignatureMessage(tt.help)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSignatureHelpFloatingPointerLeaves(t *testing.T) {
	t.Parallel()
	result := testSignatureHelp([]semanticapi.SignatureInformation{
		{
			Label: "func Open(ctx context.Context, name string, flag int, perm os.FileMode) (*File, error)",
			Parameters: []semanticapi.ParameterInformation{
				{Label: "ctx context.Context", Documentation: &semanticapi.MarkupContent{Value: "the context bounding the open"}},
				{Label: "name string", Documentation: &semanticapi.MarkupContent{Value: "the path of the file to open"}},
			},
		},
		{Label: "func Open(name string) (*File, error)"},
	}, 0, 1)
	for _, size := range []struct{ w, h int }{{60, 15}, {1, 1}} {
		f := newSignatureHelpFloating(result, DefaultSignatureHelpConfig())
		f.activeIdx = 1
		runehandlertest.RunPointerLeaves(t, f, size.w, size.h)
		assert.Equal(t, 1, f.activeIdx, "the pointer must not change the signature")
	}
}

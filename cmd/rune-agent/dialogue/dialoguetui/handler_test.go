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

package dialoguetui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/component/asciiart"
	"unstable.build/rune/internal/handler/handlerrpc/handlerrpctest"
	"unstable.build/rune/internal/handler/handlertest"
	"unstable.build/rune/internal/text/standard"
)

var _ tui.Handler = (*dialogueHandler)(nil)

func TestHandlerGeneratedImagePreview(t *testing.T) {
	picture := image.NewNRGBA(image.Rect(0, 0, 80, 276))
	for y := range 276 {
		for x := range 80 {
			shade := uint8(0)
			if (x < 40) == (y < 138) {
				shade = 255
			}
			picture.SetNRGBA(x, y, color.NRGBA{R: shade, G: shade, B: shade, A: 255})
		}
	}
	var data bytes.Buffer
	require.NoError(t, png.Encode(&data, picture))
	for _, tc := range []struct {
		name      string
		collapsed bool
		expected  string
	}{
		{name: "expanded", expected: "✓ imagegen              \n" +
			"####....                \n" +
			"####....                \n" +
			"####....                \n" +
			"####....                \n" +
			"####....                \n" +
			"####....                \n" +
			"....####                \n" +
			"....####                \n" +
			"....####                \n" +
			"....####                \n" +
			"....####                \n" +
			"....####                \n" +
			"preview                 \n" +
			"                        \n" +
			"                        \n" +
			"                        \n" +
			"                        \n" +
			"                        \n" +
			"  ┌──────────────────┐  \n" +
			"  │▐                 │  \n" +
			"  └──────────────────┘  "},
		{name: "collapsed", collapsed: true, expected: "└─ ✓ imagegen           \n" +
			"   ####....             \n" +
			"   ####....             \n" +
			"   ####....             \n" +
			"   ####....             \n" +
			"   ####....             \n" +
			"   ####....             \n" +
			"   ....####             \n" +
			"   ....####             \n" +
			"   ....####             \n" +
			"   ....####             \n" +
			"   ....####             \n" +
			"   ....####             \n" +
			"   preview              \n" +
			"                        \n" +
			"Press <ctrl-o> to expand\n" +
			"                        \n" +
			"                        \n" +
			"                        \n" +
			"  ┌──────────────────┐  \n" +
			"  │▐                 │  \n" +
			"  └──────────────────┘  "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultMarkdownConfig()
			cfg.FS = turnImageFS{"/images/picture.png": data.Bytes()}
			cfg.Storage = storagestub.NewInMemoryService()
			cfg.Interrupter = term.NopInterrupter()
			comp := NewComponent(ComponentConfig{MarkdownConfig: cfg, StartCollapsed: tc.collapsed})
			t.Cleanup(func() { require.NoError(t, comp.Close()) })
			comp.AddToolCall("image", "imagegen", "", "")
			comp.CompleteToolCall("image", "imagegen", "", "", "", false)
			comp.AddToolCallImage("image", "/images/picture.png", "preview")
			preview := comp.currentTurn.tools["image"].images[0]
			h, incoming, _ := Handler(t.Context(), new(sync.Mutex), comp, term.NopInterrupter())
			defer close(incoming)
			h.Resize(24, 22)
			ascii := asciiart.DefaultConfig()
			ascii.DensityCharacters = ".#"
			w := asciiart.NewStringWriter(24, 22, ascii)
			w.SetContext = term.ContextWithCellPixelSize(t.Context(), 10, 23)
			require.Eventually(t, func() bool {
				h.Draw(w)
				return preview.Height(21) == 14
			}, time.Second, time.Millisecond)
			handlertest.RunHandlerSequenceWriter(t, w, h, 24, 22, []handlertest.SequenceTestCase{{Expected: tc.expected}})
			t.Logf("Rendered preview:\n%s", w.String())
		})
	}
}

func typeText(h tui.Handler, text string) {
	for _, ch := range text {
		h.Handle(term.Event{Type: term.EventKey, Ch: ch})
	}
}

func TestHandlerIntegration(t *testing.T) {
	interrupt := make(chan struct{})
	h, tx, rx := Handler(context.Background(), new(sync.Mutex),
		NewComponent(ComponentConfig{}), term.FuncInterrupter(func(context.Context) error {
			interrupt <- struct{}{}
			return nil
		}))
	defer close(tx)
	h.Resize(20, 9)

	t.Run("interrupt should be called after tx channel send msg", func(t *testing.T) {
		for _, ch := range "Hello assistant!" {
			_, handled := h.Handle(term.Event{Type: term.EventKey, Ch: ch})
			assert.True(t, handled)
		}
		go func() {
			msg := <-rx
			assert.Equal(t, "Hello assistant!", msg.Text)
		}()
		_, handled := h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
		assert.True(t, handled)

		tx <- MessageEvent{Type: MessageEventText, Text: "Well, hello Sir."}

		<-interrupt

		w := term.NewStringWriter(20, 9)
		h.Draw(w)
		err := w.Flush()
		require.NoError(t, err)

		out := w.String()
		assert.Equal(t, `Hello assistant!    
Well, hello Sir.    
                    
                    
                    
                    
 ┌──────────────┐   
 │              │   
 └──────────────┘   `, out)

		cursor, _, ok := h.Cursor()
		require.True(t, ok)
		assert.Equal(t, term.Coordinates{Y: 7, X: 2}, cursor)
	})
}

func TestHandlerCtrlOToggle(t *testing.T) {
	interrupt := make(chan struct{}, 10)
	comp := NewComponent(ComponentConfig{})
	h, tx, _ := Handler(context.Background(), new(sync.Mutex), comp, term.FuncInterrupter(func(context.Context) error {
		interrupt <- struct{}{}
		return nil
	}))
	defer close(tx)
	h.Resize(30, 10)

	// Send reasoning event
	tx <- MessageEvent{Type: MessageEventReasoning, Text: "Deep thought"}
	<-interrupt

	// Ctrl+O should be handled and toggle to collapsed
	_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'o'})
	assert.True(t, handled)

	w := term.NewStringWriter(31, 11)
	h.Draw(w)
	err := w.Flush()
	require.NoError(t, err)
	assert.Contains(t, w.String(), "ctrl-o to expand")
	assert.NotContains(t, w.String(), "Deep thought")

	// Toggle to expanded: reasoning reappears
	_, handled = h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'o'})
	assert.True(t, handled)

	err = w.Clear(term.Attributes{})
	require.NoError(t, err)
	h.Draw(w)
	err = w.Flush()
	require.NoError(t, err)
	assert.Contains(t, w.String(), "ctrl-o to collapse")
	assert.Contains(t, w.String(), "Deep thought")
}

func TestHandlerCtrlOToggleCollapsesTools(t *testing.T) {
	interrupt := make(chan struct{}, 10)
	comp := NewComponent(ComponentConfig{})
	h, tx, _ := Handler(context.Background(), new(sync.Mutex), comp, term.FuncInterrupter(func(context.Context) error {
		interrupt <- struct{}{}
		return nil
	}))
	defer close(tx)
	h.Resize(40, 12)

	// Send tool call events
	tx <- MessageEvent{Type: MessageEventToolCall, ToolCallID: "c1", ToolName: "read_file", ToolArgs: `{}`, ToolSummary: "a.go"}
	<-interrupt
	tx <- MessageEvent{Type: MessageEventToolResult, ToolCallID: "c1", ToolName: "read_file", ToolArgs: `{}`, ToolSummary: "a.go", ToolOutput: "file data"}
	<-interrupt

	// Expanded: should show full tool output
	w := term.NewStringWriter(41, 13)
	h.Draw(w)
	_ = w.Flush()
	assert.Contains(t, w.String(), "✓ read_file a.go")
	assert.Contains(t, w.String(), "file data")
	assert.NotContains(t, w.String(), "└─ ✓")

	// Ctrl+O: collapse
	_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'o'})
	assert.True(t, handled)

	_ = w.Clear(term.Attributes{})
	h.Draw(w)
	_ = w.Flush()
	assert.Contains(t, w.String(), "└─ ✓ read_file a.go")
	assert.NotContains(t, w.String(), "file data")

	// Ctrl+O: expand back
	_, handled = h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'o'})
	assert.True(t, handled)

	_ = w.Clear(term.Attributes{})
	h.Draw(w)
	_ = w.Flush()
	assert.Contains(t, w.String(), "✓ read_file a.go")
	assert.Contains(t, w.String(), "file data")
}

func TestHandlerToolResultImages(t *testing.T) {
	interrupt := make(chan struct{}, 10)
	comp := NewComponent(ComponentConfig{})
	mu := new(sync.Mutex)
	h, tx, _ := Handler(context.Background(), mu, comp, term.FuncInterrupter(func(context.Context) error {
		interrupt <- struct{}{}
		return nil
	}))
	defer close(tx)
	h.Resize(40, 16)
	tx <- MessageEvent{Type: MessageEventToolCall, ToolCallID: "image", ToolName: "capture"}
	<-interrupt
	tx <- MessageEvent{Type: MessageEventToolResult, ToolCallID: "image", ToolName: "capture",
		ToolSummary: "preview", ImagePaths: []string{"/images/one.png", "/images/two.png"}}
	<-interrupt
	mu.Lock()
	require.Len(t, comp.currentTurn.tools["image"].images, 2)
	mu.Unlock()
	w := term.NewStringWriter(40, 16)
	h.Draw(w)
	require.NoError(t, w.Flush())
	assert.Contains(t, w.String(), "preview")
}

func TestHandlerErrorEvent(t *testing.T) {
	interrupt := make(chan struct{}, 10)
	h, tx, _ := Handler(context.Background(), new(sync.Mutex),
		NewComponent(ComponentConfig{}), term.FuncInterrupter(func(context.Context) error {
			interrupt <- struct{}{}
			return nil
		}))
	defer close(tx)
	h.Resize(20, 9)

	tx <- MessageEvent{Type: MessageEventError, Text: "max iterations"}
	<-interrupt

	w := term.NewStringWriter(20, 9)
	h.Draw(w)
	err := w.Flush()
	require.NoError(t, err)
	assert.Contains(t, w.String(), "! max iterations")
}

func TestHandlerWarningEvent(t *testing.T) {
	interrupt := make(chan struct{}, 10)
	h, tx, _ := Handler(context.Background(), new(sync.Mutex),
		NewComponent(ComponentConfig{}), term.FuncInterrupter(func(context.Context) error {
			interrupt <- struct{}{}
			return nil
		}))
	defer close(tx)
	h.Resize(50, 9)

	tx <- MessageEvent{Type: MessageEventWarning, Text: "Rate limited. Waiting 5s before retrying (attempt 1/3)."}
	<-interrupt

	w := term.NewStringWriter(50, 9)
	h.Draw(w)
	err := w.Flush()
	require.NoError(t, err)
	out := w.String()
	assert.Contains(t, out, "Rate limited")
	assert.Contains(t, out, "Waiting 5s")
	// Warnings should NOT have the "! " prefix that errors have.
	assert.NotContains(t, out, "! Rate limited")
}

func newPromptHandler(t *testing.T) (tui.Handler, chan<- MessageEvent, chan struct{}) {
	t.Helper()
	interrupt := make(chan struct{}, 10)
	h, tx, _ := Handler(context.Background(), new(sync.Mutex),
		NewComponent(ComponentConfig{}), term.FuncInterrupter(func(context.Context) error {
			interrupt <- struct{}{}
			return nil
		}))
	t.Cleanup(func() { close(tx) })
	h.Resize(40, 12)
	return h, tx, interrupt
}

func TestHandlerPromptRendersSelection(t *testing.T) {
	h, tx, interrupt := newPromptHandler(t)
	resultCh := make(chan []string, 1)

	tx <- MessageEvent{
		Type:          MessageEventPrompt,
		PromptTitle:   "Pick a DB",
		PromptHeader:  "Database",
		PromptOptions: []PromptEventOption{{Label: "Postgres"}, {Label: "SQLite"}},
		PromptResult:  resultCh,
	}
	<-interrupt

	w := term.NewStringWriter(40, 12)
	h.Draw(w)
	_ = w.Flush()
	assert.Contains(t, w.String(), "Pick a DB")
	assert.Contains(t, w.String(), "Postgres")
	assert.Contains(t, w.String(), "SQLite")
}

func TestHandlerPromptDownKeysMoveCursor(t *testing.T) {
	tests := []struct {
		name string
		ev   term.Event
	}{
		{"arrow", term.Event{Type: term.EventKey, Key: term.KeyArrowDown}},
		{"ctrl-j", term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'j'}},
		{"ctrl-n", term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'n'}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, tx, interrupt := newPromptHandler(t)
			resultCh := make(chan []string, 1)

			tx <- MessageEvent{
				Type:          MessageEventPrompt,
				PromptTitle:   "Choose",
				PromptOptions: []PromptEventOption{{Label: "A"}, {Label: "B"}, {Label: "C"}},
				PromptResult:  resultCh,
			}
			<-interrupt

			_, handled := h.Handle(tt.ev)
			assert.True(t, handled)
			_, handled = h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
			assert.True(t, handled)
			assert.Equal(t, []string{"B"}, <-resultCh)
		})
	}
}

func TestHandlerPromptEnterSelectsAndSendsResult(t *testing.T) {
	h, tx, interrupt := newPromptHandler(t)
	resultCh := make(chan []string, 1)

	tx <- MessageEvent{
		Type:          MessageEventPrompt,
		PromptTitle:   "Choose",
		PromptOptions: []PromptEventOption{{Label: "First"}, {Label: "Second"}},
		PromptResult:  resultCh,
	}
	<-interrupt

	// Press Enter immediately (selects first option)
	_, handled := h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	assert.True(t, handled)

	vals := <-resultCh
	assert.Equal(t, []string{"First"}, vals)
}

func TestHandlerPromptEscDismissesSendsNil(t *testing.T) {
	h, tx, interrupt := newPromptHandler(t)
	resultCh := make(chan []string, 1)

	tx <- MessageEvent{
		Type:          MessageEventPrompt,
		PromptTitle:   "Choose",
		PromptOptions: []PromptEventOption{{Label: "A"}},
		PromptResult:  resultCh,
	}
	<-interrupt

	_, handled := h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
	assert.True(t, handled)

	vals := <-resultCh
	assert.Nil(t, vals)
}

func TestHandlerPromptAbsorbsRegularKeys(t *testing.T) {
	h, tx, interrupt := newPromptHandler(t)
	resultCh := make(chan []string, 1)

	tx <- MessageEvent{
		Type:          MessageEventPrompt,
		PromptTitle:   "Choose",
		PromptOptions: []PromptEventOption{{Label: "A"}},
		PromptResult:  resultCh,
	}
	<-interrupt

	// Regular character should be absorbed (handled=true but no effect)
	_, handled := h.Handle(term.Event{Type: term.EventKey, Ch: 'x'})
	assert.True(t, handled)

	// Prompt should still be active — select to clean up
	_, handled = h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	assert.True(t, handled)
	<-resultCh
}

func TestHandlerPromptCursorHidden(t *testing.T) {
	h, tx, interrupt := newPromptHandler(t)
	resultCh := make(chan []string, 1)

	tx <- MessageEvent{
		Type:          MessageEventPrompt,
		PromptTitle:   "Choose",
		PromptOptions: []PromptEventOption{{Label: "A"}},
		PromptResult:  resultCh,
	}
	<-interrupt

	_, _, ok := h.Cursor()
	assert.False(t, ok, "cursor should be hidden during active prompt")

	// Clean up
	h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	<-resultCh
}

func TestHandlerPromptMultiSelectSpaceToggles(t *testing.T) {
	// Input backends deliver the space bar as Key=KeySpace; a bare
	// Ch=' ' covers synthetic and legacy events. Both must toggle the
	// checkbox.
	for _, tc := range []struct {
		name  string
		space term.Event
	}{
		{"synthetic-ch", term.Event{Type: term.EventKey, Ch: ' '}},
		{"key", term.Event{Type: term.EventKey, Key: term.KeySpace}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, tx, interrupt := newPromptHandler(t)
			resultCh := make(chan []string, 1)

			tx <- MessageEvent{
				Type:              MessageEventPrompt,
				PromptTitle:       "Features",
				PromptOptions:     []PromptEventOption{{Label: "Logging"}, {Label: "Metrics"}, {Label: "Tracing"}},
				PromptMultiSelect: true,
				PromptResult:      resultCh,
			}
			<-interrupt

			// Enter with nothing checked must not resolve the prompt:
			// a nil result would be read as a dismissal.
			_, handled := h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
			assert.True(t, handled)
			select {
			case vals := <-resultCh:
				t.Fatalf("empty Enter resolved the prompt with %v", vals)
			default:
			}
			_, _, ok := h.Cursor()
			assert.False(t, ok, "prompt should still be active after empty Enter")

			// Toggle first option
			h.Handle(tc.space)
			// Move down and toggle second
			h.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowDown})
			h.Handle(tc.space)
			// Confirm
			h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})

			select {
			case vals := <-resultCh:
				assert.Equal(t, []string{"Logging", "Metrics"}, vals)
			case <-time.After(2 * time.Second):
				t.Fatal("prompt was not resolved after toggling and Enter")
			}
		})
	}
}

func TestHandlerPromptMultiSelectEnterOnOtherWithRequiresInput(t *testing.T) {
	for _, tc := range []struct {
		name   string
		checkA bool
	}{
		{"nothing checked", false},
		{"A checked", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, tx, interrupt := newPromptHandler(t)
			resultCh := make(chan []string, 1)

			tx <- MessageEvent{
				Type:        MessageEventPrompt,
				PromptTitle: "Features",
				PromptOptions: []PromptEventOption{
					{Label: "A"},
					{Label: "Other", RequiresInput: true},
				},
				PromptMultiSelect: true,
				PromptResult:      resultCh,
			}
			<-interrupt

			if tc.checkA {
				h.Handle(term.Event{Type: term.EventKey, Key: term.KeySpace})
			}

			// Move the cursor to "Other" without checking it, then submit.
			h.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowDown})
			_, handled := h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
			require.True(t, handled)

			typeText(h, "feedback")
			h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})

			select {
			case vals := <-resultCh:
				require.Len(t, vals, 2)
				assert.Equal(t, "Other", vals[0], "typed text must be attributed to the option under the cursor")
				assert.Equal(t, "feedback", vals[1])
			case <-time.After(2 * time.Second):
				t.Fatal("prompt was not resolved after typing and Enter")
			}
		})
	}
}

func TestHandlerPromptToggleRequiresNoModifier(t *testing.T) {
	for _, tc := range []struct {
		name  string
		event term.Event
	}{
		{"ctrl-space", term.Event{Type: term.EventKey, Key: term.KeySpace, Mod: term.ModCtrl}},
		{"meta-space", term.Event{Type: term.EventKey, Key: term.KeySpace, Mod: term.ModAlt}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, tx, interrupt := newPromptHandler(t)
			resultCh := make(chan []string, 1)

			tx <- MessageEvent{
				Type:              MessageEventPrompt,
				PromptTitle:       "Features",
				PromptOptions:     []PromptEventOption{{Label: "Logging"}},
				PromptMultiSelect: true,
				PromptResult:      resultCh,
			}
			<-interrupt

			h.Handle(tc.event)

			// Enter with nothing checked must not resolve the prompt; if the
			// modifier combo had toggled the box, this would send ["Logging"].
			h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
			select {
			case vals := <-resultCh:
				t.Fatalf("%s toggled the checkbox: %v", tc.name, vals)
			default:
			}
		})
	}
}

func TestHandlerPromptDismissEvent(t *testing.T) {
	h, tx, interrupt := newPromptHandler(t)
	resultCh := make(chan []string, 1)

	tx <- MessageEvent{
		Type:          MessageEventPrompt,
		PromptTitle:   "Choose",
		PromptOptions: []PromptEventOption{{Label: "A"}},
		PromptResult:  resultCh,
	}
	<-interrupt

	// Verify prompt active
	_, _, ok := h.Cursor()
	assert.False(t, ok)

	// Send dismiss event
	tx <- MessageEvent{Type: MessageEventPromptDismiss}
	<-interrupt

	// Prompt should be gone, cursor visible again
	_, _, ok = h.Cursor()
	assert.True(t, ok)

	vals := <-resultCh
	assert.Nil(t, vals)
}

func TestHandlerToolEvents(t *testing.T) {
	interrupt := make(chan struct{}, 10)
	h, tx, _ := Handler(context.Background(), new(sync.Mutex),
		NewComponent(ComponentConfig{}), term.FuncInterrupter(func(context.Context) error {
			interrupt <- struct{}{}
			return nil
		}))
	defer close(tx)
	h.Resize(20, 9)

	tx <- MessageEvent{Type: MessageEventToolCall, ToolCallID: "c1", ToolName: "read_file"}
	<-interrupt

	w := term.NewStringWriter(20, 9)
	h.Draw(w)
	err := w.Flush()
	require.NoError(t, err)
	assert.Equal(t, `⚙ read_file         
                    
                    
                    
                    
                    
 ┌──────────────┐   
 │              │   
 └──────────────┘   `, w.String())

	tx <- MessageEvent{Type: MessageEventToolResult, ToolCallID: "c1", ToolName: "read_file", ToolOutput: "file content", IsError: false}
	<-interrupt

	err = w.Clear(term.Attributes{})
	require.NoError(t, err)
	h.Draw(w)
	err = w.Flush()
	require.NoError(t, err)
	assert.Equal(t, `✓ read_file         
file content        
                    
                    
                    
                    
 ┌──────────────┐   
 │              │   
 └──────────────┘   `, w.String())
}

func TestIsCommand(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"/help", true},
		{"/model default", true},
		{"/ space", false},
		{"/", false},
		{"hello", false},
		{"", false},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			assert.Equal(t, tc.want, isCommand(tc.input))
		})
	}
}

func TestParseCommand(t *testing.T) {
	tests := []struct {
		input    string
		wantName string
		wantArgs []string
	}{
		{"/help", "help", []string{}},
		{"/model default", "model", []string{"default"}},
		{"/tools a b", "tools", []string{"a", "b"}},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			name, args := parseCommand(tc.input)
			assert.Equal(t, tc.wantName, name)
			assert.Equal(t, tc.wantArgs, args)
		})
	}
}

// mockCommandHandler is a test stub for CommandHandler.
type mockCommandHandler struct {
	handleFunc   func(ctx context.Context, name string, args []string) (CommandResult, error)
	completeFunc func(ctx context.Context, name string, args []string) (iterator.Iterator[string], error)
}

func (m *mockCommandHandler) HandleCommand(ctx context.Context, name string, args []string) (CommandResult, error) {
	return m.handleFunc(ctx, name, args)
}

func (m *mockCommandHandler) Complete(ctx context.Context, name string, args []string) (iterator.Iterator[string], error) {
	if m.completeFunc != nil {
		return m.completeFunc(ctx, name, args)
	}
	return iterator.Empty[string](), nil
}

func TestHandlerCommandIntercepted(t *testing.T) {
	interrupt := make(chan struct{}, 20)
	mock := &mockCommandHandler{
		handleFunc: func(_ context.Context, name string, _ []string) (CommandResult, error) {
			r := component.NewResponsiveString("output-"+name, component.StringResponsiveConfig{})
			return CommandResult{Display: iterator.FromSlice([]component.Responsive{r})}, nil
		},
	}
	h, tx, rx := Handler(context.Background(), new(sync.Mutex),
		NewComponent(ComponentConfig{}), term.FuncInterrupter(func(context.Context) error {
			interrupt <- struct{}{}
			return nil
		}),
		WithCommands(mock),
	)
	defer close(tx)
	h.Resize(30, 9)

	// Type /help and press enter.
	for _, ch := range "/help" {
		h.Handle(term.Event{Type: term.EventKey, Ch: ch})
	}
	_, handled := h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	assert.True(t, handled)

	// rx should NOT receive the message (it was intercepted).
	select {
	case msg := <-rx:
		t.Fatalf("expected no message on rx, got %q", msg.Text)
	case <-time.After(50 * time.Millisecond):
	}

	// Wait for the goroutine to drain and interrupt.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-interrupt:
		case <-deadline:
			t.Fatal("timed out waiting for command output")
		}
		w := term.NewStringWriter(30, 9)
		h.Draw(w)
		_ = w.Flush()
		if assert.ObjectsAreEqual(true, containsStr(w.String(), "output-help")) {
			break
		}
	}
}

func TestHandlerCommandInjected(t *testing.T) {
	interrupt := make(chan struct{}, 20)
	gotName := make(chan string, 1)
	gotArgs := make(chan []string, 1)
	mock := &mockCommandHandler{
		handleFunc: func(_ context.Context, name string, args []string) (CommandResult, error) {
			gotName <- name
			gotArgs <- args
			r := component.NewResponsiveString("output-"+name, component.StringResponsiveConfig{})
			return CommandResult{Display: iterator.FromSlice([]component.Responsive{r})}, nil
		},
	}
	h, tx, rx := Handler(context.Background(), new(sync.Mutex),
		NewComponent(ComponentConfig{}), term.FuncInterrupter(func(context.Context) error {
			interrupt <- struct{}{}
			return nil
		}),
		WithCommands(mock),
	)
	defer close(tx)
	h.Resize(30, 9)

	tx <- MessageEvent{
		Type:        MessageEventCommand,
		CommandName: "effort",
		CommandArgs: []string{"high"},
	}

	select {
	case name := <-gotName:
		assert.Equal(t, "effort", name)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for HandleCommand")
	}
	assert.Equal(t, []string{"high"}, <-gotArgs)

	// rx should NOT receive a user message (effort renders Display only).
	select {
	case msg := <-rx:
		t.Fatalf("expected no message on rx, got %q", msg.Text)
	case <-time.After(50 * time.Millisecond):
	}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-interrupt:
		case <-deadline:
			t.Fatal("timed out waiting for command output")
		}
		w := term.NewStringWriter(30, 9)
		h.Draw(w)
		_ = w.Flush()
		if assert.ObjectsAreEqual(true, containsStr(w.String(), "output-effort")) {
			break
		}
	}
}

func TestHandlerNonCommandGoesToLLM(t *testing.T) {
	interrupt := make(chan struct{}, 10)
	mock := &mockCommandHandler{
		handleFunc: func(context.Context, string, []string) (CommandResult, error) {
			t.Fatal("HandleCommand should not be called")
			return CommandResult{}, nil
		},
	}
	h, tx, rx := Handler(context.Background(), new(sync.Mutex),
		NewComponent(ComponentConfig{}), term.FuncInterrupter(func(context.Context) error {
			interrupt <- struct{}{}
			return nil
		}),
		WithCommands(mock),
	)
	defer close(tx)
	h.Resize(20, 9)

	for _, ch := range "hello" {
		h.Handle(term.Event{Type: term.EventKey, Ch: ch})
	}
	go func() {
		msg := <-rx
		assert.Equal(t, "hello", msg.Text)
	}()
	_, handled := h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	assert.True(t, handled)
}

func TestHandlerBusyQueuesUserMessageUntilIdle(t *testing.T) {
	mu := new(sync.Mutex)
	comp := NewComponent(ComponentConfig{})
	interrupt := make(chan struct{}, 10)
	h, tx, rx := Handler(context.Background(), mu, comp,
		term.FuncInterrupter(func(context.Context) error {
			interrupt <- struct{}{}
			return nil
		}))
	defer close(tx)
	h.Resize(20, 9)

	tx <- MessageEvent{Type: MessageEventBusy, Busy: true}
	<-interrupt

	typeText(h, "hello")
	_, handled := h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	assert.True(t, handled)

	select {
	case msg := <-rx:
		t.Fatalf("expected queued message to wait until idle, got %q", msg.Text)
	case <-time.After(50 * time.Millisecond):
	}

	w := term.NewStringWriter(20, 9)
	h.Draw(w)
	_ = w.Flush()
	assert.Contains(t, w.String(), "hello")
	assert.Contains(t, w.String(), "󰄝")

	go func() {
		tx <- MessageEvent{Type: MessageEventBusy, Busy: false}
	}()

	select {
	case msg := <-rx:
		assert.Equal(t, "hello", msg.Text)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for queued message to drain")
	}
	<-interrupt

	w = term.NewStringWriter(20, 9)
	h.Draw(w)
	_ = w.Flush()
	assert.NotContains(t, w.String(), "󰄝  hello")
	assert.Contains(t, w.String(), "hello")

	mu.Lock()
	assert.Equal(t, 0, comp.QueueLen())
	mu.Unlock()
}

func TestHandlerBusyQueueDrainsFIFOAcrossTurns(t *testing.T) {
	mu := new(sync.Mutex)
	comp := NewComponent(ComponentConfig{})
	interrupt := make(chan struct{}, 10)
	h, tx, rx := Handler(context.Background(), mu, comp,
		term.FuncInterrupter(func(context.Context) error {
			interrupt <- struct{}{}
			return nil
		}))
	defer close(tx)
	h.Resize(30, 10)

	tx <- MessageEvent{Type: MessageEventBusy, Busy: true}
	<-interrupt

	typeText(h, "first")
	h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	typeText(h, "second")
	h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})

	mu.Lock()
	assert.Equal(t, 2, comp.QueueLen())
	mu.Unlock()

	go func() {
		tx <- MessageEvent{Type: MessageEventBusy, Busy: false}
	}()

	select {
	case msg := <-rx:
		assert.Equal(t, "first", msg.Text)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first queued message")
	}
	<-interrupt

	tx <- MessageEvent{Type: MessageEventBusy, Busy: true}
	<-interrupt

	go func() {
		tx <- MessageEvent{Type: MessageEventBusy, Busy: false}
	}()

	select {
	case msg := <-rx:
		assert.Equal(t, "second", msg.Text)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second queued message")
	}
	<-interrupt

	mu.Lock()
	assert.Equal(t, 0, comp.QueueLen())
	mu.Unlock()
}

func TestHandlerWithoutSubmitEcho(t *testing.T) {
	type step func(t *testing.T, h tui.Handler, tx chan<- MessageEvent, rx <-chan SubmitMessage, interrupt <-chan struct{})
	receive := func(t *testing.T, rx <-chan SubmitMessage, want string) {
		t.Helper()
		select {
		case msg := <-rx:
			assert.Equal(t, want, msg.Text)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for %q", want)
		}
	}
	submitIdle := func(t *testing.T, h tui.Handler, _ chan<- MessageEvent, rx <-chan SubmitMessage, _ <-chan struct{}) {
		typeText(h, "hello")
		done := make(chan struct{})
		go func() {
			defer close(done)
			receive(t, rx, "hello")
		}()
		h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
		<-done
	}
	submitQueued := func(t *testing.T, h tui.Handler, tx chan<- MessageEvent, rx <-chan SubmitMessage, interrupt <-chan struct{}) {
		tx <- MessageEvent{Type: MessageEventBusy, Busy: true}
		<-interrupt
		typeText(h, "hello")
		h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
		go func() { tx <- MessageEvent{Type: MessageEventBusy, Busy: false} }()
		receive(t, rx, "hello")
		<-interrupt
	}
	submitCommand := func(t *testing.T, h tui.Handler, _ chan<- MessageEvent, rx <-chan SubmitMessage, _ <-chan struct{}) {
		typeText(h, "/hello")
		h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
		receive(t, rx, "/hello")
	}
	cases := []struct {
		name     string
		step     step
		noEcho   bool
		wantEcho bool
	}{
		{name: "idle submit echoes", step: submitIdle, wantEcho: true},
		{name: "idle submit without echo", step: submitIdle, noEcho: true},
		{name: "drained queue echoes", step: submitQueued, wantEcho: true},
		{name: "drained queue without echo", step: submitQueued, noEcho: true},
		{name: "command line echoes", step: submitCommand, wantEcho: true},
		{name: "command line without echo", step: submitCommand, noEcho: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mu := new(sync.Mutex)
			comp := NewComponent(ComponentConfig{})
			interrupt := make(chan struct{}, 10)
			opts := []HandlerOption{WithCommands(&mockCommandHandler{
				handleFunc: func(_ context.Context, name string, _ []string) (CommandResult, error) {
					return CommandResult{UserMessage: "/" + name}, nil
				},
			})}
			if tc.noEcho {
				opts = append(opts, WithoutSubmitEcho())
			}
			h, tx, rx := Handler(context.Background(), mu, comp,
				term.FuncInterrupter(func(context.Context) error {
					interrupt <- struct{}{}
					return nil
				}), opts...)
			defer close(tx)
			h.Resize(30, 9)

			tc.step(t, h, tx, rx, interrupt)

			w := term.NewStringWriter(30, 9)
			h.Draw(w)
			_ = w.Flush()
			messages := strings.Join(strings.Split(w.String(), "\n")[:7], "\n")
			assert.Equal(t, tc.wantEcho, strings.Contains(messages, "hello"), messages)
			mu.Lock()
			assert.Equal(t, 0, comp.QueueLen())
			mu.Unlock()
		})
	}
}

func TestHandlerUserMessageEvent(t *testing.T) {
	interrupt := make(chan struct{}, 10)
	h, tx, _ := Handler(context.Background(), new(sync.Mutex), NewComponent(ComponentConfig{}),
		term.FuncInterrupter(func(context.Context) error {
			interrupt <- struct{}{}
			return nil
		}), WithoutSubmitEcho())
	defer close(tx)
	h.Resize(40, 9)

	tx <- MessageEvent{
		Type:        MessageEventUserMessage,
		Text:        "from another viewer",
		Attachments: []Attachment{{Path: "/work/alpha.go", Name: "alpha.go"}},
	}
	<-interrupt

	w := term.NewStringWriter(40, 9)
	h.Draw(w)
	_ = w.Flush()
	assert.Contains(t, w.String(), "from another viewer")
	assert.Contains(t, w.String(), "alpha.go")
}

func TestHandlerArrowUpRecallsNewestQueuedMessage(t *testing.T) {
	mu := new(sync.Mutex)
	comp := NewComponent(ComponentConfig{})
	interrupt := make(chan struct{}, 10)
	h, tx, _ := Handler(context.Background(), mu, comp,
		term.FuncInterrupter(func(context.Context) error {
			interrupt <- struct{}{}
			return nil
		}))
	defer close(tx)
	h.Resize(30, 10)

	tx <- MessageEvent{Type: MessageEventBusy, Busy: true}
	<-interrupt

	typeText(h, "first")
	h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	typeText(h, "second")
	h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})

	_, handled := h.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowUp})
	assert.True(t, handled)

	mu.Lock()
	assert.Equal(t, "second", comp.Input().Text())
	assert.Equal(t, 1, comp.QueueLen())
	mu.Unlock()
}

func TestHandlerArrowUpRecallsLastSentUserMessage(t *testing.T) {
	h, tx, rx := Handler(context.Background(), new(sync.Mutex),
		NewComponent(ComponentConfig{}), term.FuncInterrupter(func(context.Context) error {
			return nil
		}))
	defer close(tx)
	h.Resize(30, 10)

	got := make(chan SubmitMessage, 2)
	go func() { got <- <-rx }()
	typeText(h, "first")
	_, handled := h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	assert.True(t, handled)
	assert.Equal(t, "first", (<-got).Text)

	go func() { got <- <-rx }()
	typeText(h, "second")
	_, handled = h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	assert.True(t, handled)
	assert.Equal(t, "second", (<-got).Text)

	_, handled = h.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowUp})
	assert.True(t, handled)

	w := term.NewStringWriter(30, 10)
	h.Draw(w)
	require.NoError(t, w.Flush())
	assert.Contains(t, w.String(), "second")
	assert.Contains(t, w.String(), "│second")
	assert.NotContains(t, w.String(), "│first")

	_, handled = h.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowUp})
	assert.True(t, handled)

	w = term.NewStringWriter(30, 10)
	h.Draw(w)
	require.NoError(t, w.Flush())
	assert.Contains(t, w.String(), "│first")
	assert.NotContains(t, w.String(), "│second")
}

func TestHandlerArrowUpWhileBusyRecallsSentHistoryAfterQueueExhausted(t *testing.T) {
	mu := new(sync.Mutex)
	comp := NewComponent(ComponentConfig{})
	interrupt := make(chan struct{}, 10)
	h, tx, rx := Handler(context.Background(), mu, comp,
		term.FuncInterrupter(func(context.Context) error {
			interrupt <- struct{}{}
			return nil
		}))
	defer close(tx)
	h.Resize(30, 10)

	got := make(chan SubmitMessage, 1)
	go func() { got <- <-rx }()
	typeText(h, "hello")
	_, handled := h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	assert.True(t, handled)
	assert.Equal(t, "hello", (<-got).Text)

	tx <- MessageEvent{Type: MessageEventBusy, Busy: true}
	<-interrupt

	typeText(h, "queued")
	_, handled = h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	assert.True(t, handled)

	_, handled = h.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowUp})
	assert.True(t, handled)
	_, handled = h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'c'})
	assert.True(t, handled)
	_, handled = h.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowUp})
	assert.True(t, handled)

	mu.Lock()
	assert.Equal(t, "queued", comp.Input().Text())
	assert.Equal(t, 0, comp.QueueLen())
	mu.Unlock()
}

func TestHandlerArrowUpAfterDiscardRecallsPreviousQueuedMessage(t *testing.T) {
	mu := new(sync.Mutex)
	comp := NewComponent(ComponentConfig{})
	interrupt := make(chan struct{}, 10)
	h, tx, _ := Handler(context.Background(), mu, comp,
		term.FuncInterrupter(func(context.Context) error {
			interrupt <- struct{}{}
			return nil
		}))
	defer close(tx)
	h.Resize(30, 10)

	tx <- MessageEvent{Type: MessageEventBusy, Busy: true}
	<-interrupt

	typeText(h, "first")
	h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	typeText(h, "second")
	h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})

	_, handled := h.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowUp})
	assert.True(t, handled)
	_, handled = h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'c'})
	assert.True(t, handled)
	_, handled = h.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowUp})
	assert.True(t, handled)

	mu.Lock()
	assert.Equal(t, "first", comp.Input().Text())
	assert.Equal(t, 0, comp.QueueLen())
	mu.Unlock()
}

func TestHandlerCommandError(t *testing.T) {
	interrupt := make(chan struct{}, 20)
	mock := &mockCommandHandler{
		handleFunc: func(context.Context, string, []string) (CommandResult, error) {
			return CommandResult{}, errors.New("bad command")
		},
	}
	h, tx, _ := Handler(context.Background(), new(sync.Mutex),
		NewComponent(ComponentConfig{}), term.FuncInterrupter(func(context.Context) error {
			interrupt <- struct{}{}
			return nil
		}),
		WithCommands(mock),
	)
	defer close(tx)
	h.Resize(30, 9)

	for _, ch := range "/bad" {
		h.Handle(term.Event{Type: term.EventKey, Ch: ch})
	}
	h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})

	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-interrupt:
		case <-deadline:
			t.Fatal("timed out waiting for error")
		}
		w := term.NewStringWriter(30, 9)
		h.Draw(w)
		_ = w.Flush()
		if containsStr(w.String(), "! bad command") {
			return
		}
	}
}

func TestHandlerCommandExit(t *testing.T) {
	interrupt := make(chan struct{}, 10)
	closeCalled := make(chan struct{})
	mock := &mockCommandHandler{
		handleFunc: func(context.Context, string, []string) (CommandResult, error) {
			return CommandResult{Exit: true}, nil
		},
	}
	h, tx, _ := Handler(context.Background(), new(sync.Mutex),
		NewComponent(ComponentConfig{}), term.FuncInterrupter(func(context.Context) error {
			interrupt <- struct{}{}
			return nil
		}),
		WithCommands(mock),
		WithCloseFunc(func() { close(closeCalled) }),
	)
	defer close(tx)
	h.Resize(20, 9)

	for _, ch := range "/exit" {
		h.Handle(term.Event{Type: term.EventKey, Ch: ch})
	}
	h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})

	select {
	case <-closeCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("closeFn was not called")
	}
}

func TestHandlerCommandUserMessage(t *testing.T) {
	mock := &mockCommandHandler{
		handleFunc: func(_ context.Context, name string, args []string) (CommandResult, error) {
			if name == "commit" {
				return CommandResult{
					UserMessage: "<skill_content>" + strings.Join(args, " ") + "</skill_content>",
				}, nil
			}
			return CommandResult{}, errors.New("unknown")
		},
	}
	h, tx, rx := Handler(context.Background(), new(sync.Mutex),
		NewComponent(ComponentConfig{}), term.FuncInterrupter(func(context.Context) error {
			return nil
		}),
		WithCommands(mock),
	)
	defer close(tx)
	h.Resize(40, 9)

	// Type /commit -m "fix" and press enter.
	for _, ch := range `/commit -m "fix"` {
		h.Handle(term.Event{Type: term.EventKey, Ch: ch})
	}
	go func() {
		msg := <-rx
		assert.Equal(t, `<skill_content>-m "fix"</skill_content>`, msg.Text)
	}()
	_, handled := h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	assert.True(t, handled)
}

func TestHandlerCommandUserMessageQueuedWhileBusy(t *testing.T) {
	interrupt := make(chan struct{}, 10)
	mock := &mockCommandHandler{
		handleFunc: func(_ context.Context, name string, _ []string) (CommandResult, error) {
			return CommandResult{
				UserMessage: "<skill>" + name + "</skill>",
				SkillName:   name,
			}, nil
		},
	}
	h, tx, rx := Handler(context.Background(), new(sync.Mutex),
		NewComponent(ComponentConfig{}), term.FuncInterrupter(func(context.Context) error {
			interrupt <- struct{}{}
			return nil
		}),
		WithCommands(mock),
	)
	defer close(tx)
	h.Resize(40, 9)

	tx <- MessageEvent{Type: MessageEventBusy, Busy: true}
	<-interrupt

	typeText(h, "/review")
	_, handled := h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	assert.True(t, handled)

	select {
	case msg := <-rx:
		t.Fatalf("expected command user message to queue while busy, got %q", msg.Text)
	case <-time.After(50 * time.Millisecond):
	}

	go func() {
		tx <- MessageEvent{Type: MessageEventBusy, Busy: false}
	}()

	select {
	case msg := <-rx:
		assert.Equal(t, "<skill>review</skill>", msg.Text)
		assert.Equal(t, "review", msg.SkillName)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for queued command message")
	}
	<-interrupt
}

func TestHandlerCommandUserMessageNoArgs(t *testing.T) {
	mock := &mockCommandHandler{
		handleFunc: func(_ context.Context, name string, _ []string) (CommandResult, error) {
			return CommandResult{UserMessage: "<skill>" + name + "</skill>"}, nil
		},
	}
	h, tx, rx := Handler(context.Background(), new(sync.Mutex),
		NewComponent(ComponentConfig{}), term.FuncInterrupter(func(context.Context) error {
			return nil
		}),
		WithCommands(mock),
	)
	defer close(tx)
	h.Resize(40, 9)

	for _, ch := range "/review" {
		h.Handle(term.Event{Type: term.EventKey, Ch: ch})
	}
	go func() {
		msg := <-rx
		assert.Equal(t, "<skill>review</skill>", msg.Text)
	}()
	_, handled := h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	assert.True(t, handled)
}

func TestHandlerCommandSideEffectInterrupts(t *testing.T) {
	interrupt := make(chan struct{}, 20)
	called := make(chan struct{}, 1)
	mock := &mockCommandHandler{
		handleFunc: func(context.Context, string, []string) (CommandResult, error) {
			// Simulate a command that mutates external state (e.g.
			// wm.Floating) and returns no display/user-message/exit.
			called <- struct{}{}
			return CommandResult{}, nil
		},
	}
	h, tx, _ := Handler(context.Background(), new(sync.Mutex),
		NewComponent(ComponentConfig{}), term.FuncInterrupter(func(context.Context) error {
			interrupt <- struct{}{}
			return nil
		}),
		WithCommands(mock),
	)
	defer close(tx)
	h.Resize(30, 9)

	for _, ch := range "/fork" {
		h.Handle(term.Event{Type: term.EventKey, Ch: ch})
	}
	_, handled := h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	assert.True(t, handled)

	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for HandleCommand to be called")
	}
	select {
	case <-interrupt:
	case <-time.After(2 * time.Second):
		t.Fatal("expected interrupter to be invoked after side-effect-only command")
	}
}

func TestHandlerFreeFormPromptRendersQuestion(t *testing.T) {
	h, tx, interrupt := newPromptHandler(t)
	resultCh := make(chan []string, 1)

	tx <- MessageEvent{
		Type:          MessageEventPrompt,
		PromptTitle:   "What is your name?",
		PromptHeader:  "Name",
		PromptOptions: nil, // no options → free-form
		PromptResult:  resultCh,
	}
	<-interrupt

	w := term.NewStringWriter(40, 12)
	h.Draw(w)
	_ = w.Flush()
	assert.Contains(t, w.String(), "Name: What is your name?")

	// Cursor should be visible (pointing to the main inputbox).
	_, _, ok := h.Cursor()
	assert.True(t, ok, "cursor should be visible during free-form prompt")

	// Clean up — dismiss the prompt.
	h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
	<-resultCh
}

func TestHandlerFreeFormPromptEnterSubmitsText(t *testing.T) {
	h, tx, interrupt := newPromptHandler(t)
	resultCh := make(chan []string, 1)

	tx <- MessageEvent{
		Type:          MessageEventPrompt,
		PromptTitle:   "What is your name?",
		PromptOptions: nil,
		PromptResult:  resultCh,
	}
	<-interrupt

	// Type "Alice"
	for _, ch := range "Alice" {
		h.Handle(term.Event{Type: term.EventKey, Ch: ch})
	}
	_, handled := h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	assert.True(t, handled)

	vals := <-resultCh
	assert.Equal(t, []string{"Alice"}, vals)
}

func TestHandlerFreeFormPromptEscDismisses(t *testing.T) {
	h, tx, interrupt := newPromptHandler(t)
	resultCh := make(chan []string, 1)

	tx <- MessageEvent{
		Type:          MessageEventPrompt,
		PromptTitle:   "What is your name?",
		PromptOptions: nil,
		PromptResult:  resultCh,
	}
	<-interrupt

	_, handled := h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
	assert.True(t, handled)

	vals := <-resultCh
	assert.Nil(t, vals)
}

func TestHandlerFreeFormPromptEmptyEnterIgnored(t *testing.T) {
	h, tx, interrupt := newPromptHandler(t)
	resultCh := make(chan []string, 1)

	tx <- MessageEvent{
		Type:          MessageEventPrompt,
		PromptTitle:   "What is your name?",
		PromptOptions: nil,
		PromptResult:  resultCh,
	}
	<-interrupt

	// Press Enter with empty input — should NOT submit.
	_, handled := h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	assert.False(t, handled, "empty Enter should not be handled")

	// Prompt should still be active — type and submit to clean up.
	for _, ch := range "Bob" {
		h.Handle(term.Event{Type: term.EventKey, Ch: ch})
	}
	h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	vals := <-resultCh
	assert.Equal(t, []string{"Bob"}, vals)
}

func TestHandlerFreeFormPromptDismissEvent(t *testing.T) {
	h, tx, interrupt := newPromptHandler(t)
	resultCh := make(chan []string, 1)

	tx <- MessageEvent{
		Type:          MessageEventPrompt,
		PromptTitle:   "What is your name?",
		PromptOptions: nil,
		PromptResult:  resultCh,
	}
	<-interrupt

	// Verify cursor is visible (free-form prompt uses main inputbox).
	_, _, ok := h.Cursor()
	assert.True(t, ok)

	// Send dismiss event.
	tx <- MessageEvent{Type: MessageEventPromptDismiss}
	<-interrupt

	// Prompt should be gone.
	vals := <-resultCh
	assert.Nil(t, vals)

	// Normal input should work after dismiss — cursor visible, typing works.
	_, _, ok = h.Cursor()
	assert.True(t, ok, "cursor visible after dismiss")
}

// TestHandlerPromptDismissEventRestoresHint verifies that the hint is
// restored when a MessageEventPromptDismiss event dismisses the prompt
// (e.g. context cancellation path).
func containsStr(haystack, needle string) bool {
	return len(haystack) >= len(needle) && searchStr(haystack, needle)
}

func searchStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestHandlerPromptCtrlCDismissesSelection(t *testing.T) {
	h, tx, interrupt := newPromptHandler(t)
	resultCh := make(chan []string, 1)

	tx <- MessageEvent{
		Type:          MessageEventPrompt,
		PromptTitle:   "Choose",
		PromptOptions: []PromptEventOption{{Label: "A"}, {Label: "B"}},
		PromptResult:  resultCh,
	}
	<-interrupt

	_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'c'})
	assert.True(t, handled)

	vals := <-resultCh
	assert.Nil(t, vals, "Ctrl-C should dismiss the prompt and send nil")
}

func TestHandlerPromptCtrlCDismissesRequiresInput(t *testing.T) {
	h, tx, interrupt := newPromptHandler(t)
	resultCh := make(chan []string, 1)

	tx <- MessageEvent{
		Type:        MessageEventPrompt,
		PromptTitle: "Choose",
		PromptOptions: []PromptEventOption{
			{Label: "Other", RequiresInput: true},
		},
		PromptResult: resultCh,
	}
	<-interrupt

	// Enter the requires-input text mode.
	_, handled := h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	assert.True(t, handled)

	// Ctrl-C while typing should dismiss the entire prompt.
	_, handled = h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'c'})
	assert.True(t, handled)

	vals := <-resultCh
	assert.Nil(t, vals, "Ctrl-C in input mode should dismiss the prompt")
}

func TestHandlerPromptEscIgnoredInRequiresInput(t *testing.T) {
	h, tx, interrupt := newPromptHandler(t)
	resultCh := make(chan []string, 1)

	tx <- MessageEvent{
		Type:        MessageEventPrompt,
		PromptTitle: "Choose",
		PromptOptions: []PromptEventOption{
			{Label: "Other", RequiresInput: true},
		},
		PromptResult: resultCh,
	}
	<-interrupt

	// Enter the requires-input text mode and type some feedback.
	_, handled := h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	assert.True(t, handled)
	for _, ch := range "wip" {
		h.Handle(term.Event{Type: term.EventKey, Ch: ch})
	}

	// Esc must not cancel back to selection nor dismiss the prompt.
	_, handled = h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
	assert.True(t, handled, "Esc should be absorbed in input mode")

	// The cursor is only visible while the prompt is in text-input mode, so a
	// visible cursor proves Esc did not cancel back to the selection menu.
	_, _, ok := h.Cursor()
	assert.True(t, ok, "Esc should keep the prompt in text-input mode")

	select {
	case <-resultCh:
		t.Fatal("Esc should not dismiss the prompt or send a result")
	default:
	}
}

func TestHandlerPromptCtrlCDismissesFreeForm(t *testing.T) {
	h, tx, interrupt := newPromptHandler(t)
	resultCh := make(chan []string, 1)

	tx <- MessageEvent{
		Type:          MessageEventPrompt,
		PromptTitle:   "What is your name?",
		PromptOptions: nil,
		PromptResult:  resultCh,
	}
	<-interrupt

	// Type some text so we can assert Ctrl-C does not submit it.
	for _, ch := range "Ali" {
		h.Handle(term.Event{Type: term.EventKey, Ch: ch})
	}

	_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'c'})
	assert.True(t, handled)

	vals := <-resultCh
	assert.Nil(t, vals, "Ctrl-C in free-form prompt should dismiss, not submit")
}

// linkedDraft composes "check alpha.go" with the label linked to a
// freshly keyed attachment, the shape a '#' completion leaves behind.
func linkedDraft(comp *Component) Attachment {
	a := comp.AddAttachment(NewWorkspaceFileAttachment("alpha.go"))
	comp.Input().SetDraft("check alpha.go",
		[]InlineAttachmentLink{{Key: a.Key, Start: 6, End: 14}})
	return a
}

func TestHandlerSubmitsAttachmentOnlyDraft(t *testing.T) {
	mu := new(sync.Mutex)
	comp := NewComponent(ComponentConfig{})
	h, tx, rx := Handler(context.Background(), mu, comp,
		term.FuncInterrupter(func(context.Context) error { return nil }))
	defer close(tx)
	h.Resize(30, 10)

	mu.Lock()
	a := comp.AddAttachment(NewWorkspaceFileAttachment("alpha.go"))
	mu.Unlock()

	got := make(chan SubmitMessage, 1)
	go func() { got <- <-rx }()
	_, handled := h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	assert.True(t, handled)

	msg := <-got
	assert.Equal(t, "", msg.Text)
	assert.Equal(t, []Attachment{a}, msg.Attachments)
	assert.Empty(t, comp.Attachments())
}

func TestHandlerEnterIgnoresFullyEmptyDraft(t *testing.T) {
	comp := NewComponent(ComponentConfig{})
	h, tx, rx := Handler(context.Background(), new(sync.Mutex), comp,
		term.FuncInterrupter(func(context.Context) error { return nil }))
	defer close(tx)
	h.Resize(30, 10)

	_, handled := h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	assert.True(t, handled)

	select {
	case msg := <-rx:
		t.Fatalf("empty draft must not submit, got %q", msg.Text)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestHandlerSubmitCarriesInlineLinks(t *testing.T) {
	mu := new(sync.Mutex)
	comp := NewComponent(ComponentConfig{})
	h, tx, rx := Handler(context.Background(), mu, comp,
		term.FuncInterrupter(func(context.Context) error { return nil }))
	defer close(tx)
	h.Resize(30, 10)

	mu.Lock()
	a := linkedDraft(comp)
	mu.Unlock()

	got := make(chan SubmitMessage, 1)
	go func() { got <- <-rx }()
	h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})

	msg := <-got
	assert.Equal(t, "check alpha.go", msg.Text)
	assert.Equal(t, []Attachment{a}, msg.Attachments)
	assert.Equal(t, []InlineAttachmentLink{{Key: a.Key, Start: 6, End: 14}}, msg.Links)
}

func TestHandlerQueueRecallRestoresAttachmentsAndLinks(t *testing.T) {
	mu := new(sync.Mutex)
	comp := NewComponent(ComponentConfig{})
	interrupt := make(chan struct{}, 10)
	h, tx, _ := Handler(context.Background(), mu, comp,
		term.FuncInterrupter(func(context.Context) error {
			interrupt <- struct{}{}
			return nil
		}))
	defer close(tx)
	h.Resize(30, 10)

	tx <- MessageEvent{Type: MessageEventBusy, Busy: true}
	<-interrupt

	mu.Lock()
	a := linkedDraft(comp)
	mu.Unlock()
	h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})

	mu.Lock()
	require.Empty(t, comp.Attachments(), "submitting takes the whole draft")
	mu.Unlock()

	_, handled := h.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowUp})
	assert.True(t, handled)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, "check alpha.go", comp.Input().Text())
	assert.Equal(t, []Attachment{a}, comp.Attachments())
	assert.Equal(t, []InlineAttachmentLink{{Key: a.Key, Start: 6, End: 14}},
		comp.Input().Links())
	assert.Equal(t, 0, comp.QueueLen())
}

func TestHandlerQueueRecallKeepsAttachmentOnlyDraft(t *testing.T) {
	mu := new(sync.Mutex)
	comp := NewComponent(ComponentConfig{})
	interrupt := make(chan struct{}, 10)
	h, tx, _ := Handler(context.Background(), mu, comp,
		term.FuncInterrupter(func(context.Context) error {
			interrupt <- struct{}{}
			return nil
		}))
	defer close(tx)
	h.Resize(30, 10)

	tx <- MessageEvent{Type: MessageEventBusy, Busy: true}
	<-interrupt

	typeText(h, "queued")
	h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})

	mu.Lock()
	pending := comp.AddAttachment(NewWorkspaceFileAttachment("bravo.go"))
	mu.Unlock()

	h.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowUp})

	mu.Lock()
	assert.Equal(t, 1, comp.QueueLen(),
		"an attachment-only draft must not pop the queue over itself")
	mu.Unlock()

	h.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowDown})

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []Attachment{pending}, comp.Attachments(),
		"history browsing must give the attachment-only draft back")
	assert.Equal(t, 1, comp.QueueLen())
}

func TestHandlerHistoryRecallRestoresDraftAndScratch(t *testing.T) {
	mu := new(sync.Mutex)
	comp := NewComponent(ComponentConfig{})
	h, tx, rx := Handler(context.Background(), mu, comp,
		term.FuncInterrupter(func(context.Context) error { return nil }))
	defer close(tx)
	h.Resize(30, 10)

	got := make(chan SubmitMessage, 1)
	go func() { got <- <-rx }()
	mu.Lock()
	a := linkedDraft(comp)
	mu.Unlock()
	h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	<-got

	typeText(h, "scratch")

	_, handled := h.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowUp})
	assert.True(t, handled)

	mu.Lock()
	assert.Equal(t, "check alpha.go", comp.Input().Text())
	assert.Equal(t, []Attachment{a}, comp.Attachments())
	assert.Equal(t, []InlineAttachmentLink{{Key: a.Key, Start: 6, End: 14}},
		comp.Input().Links())
	mu.Unlock()

	_, handled = h.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowDown})
	assert.True(t, handled)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, "scratch", comp.Input().Text(),
		"walking past the newest entry must restore the interrupted draft")
	assert.Empty(t, comp.Attachments())
}

func TestHandlerCommandHistoryEntriesAreTextOnly(t *testing.T) {
	mu := new(sync.Mutex)
	comp := NewComponent(ComponentConfig{})
	commands := &mockCommandHandler{
		handleFunc: func(context.Context, string, []string) (CommandResult, error) {
			return CommandResult{}, nil
		},
	}
	h, tx, _ := Handler(context.Background(), mu, comp, term.NopInterrupter(),
		WithCommands(commands))
	defer close(tx)
	h.Resize(30, 10)

	mu.Lock()
	comp.AddAttachment(NewWorkspaceFileAttachment("alpha.go"))
	mu.Unlock()

	typeText(h, "/help")
	h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})

	dh := h.(*dialogueHandler)
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, dh.history, 1)
	assert.Equal(t, Draft{Text: "/help"}, dh.history[0])
	assert.Len(t, comp.Attachments(), 1,
		"a command must not consume the pending attachments")
}

// blockingIterator stands in for a command that blocks on a slow call,
// such as the LLM summarisation behind /compact.
type blockingIterator struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingIterator) Next(context.Context) (component.Responsive, bool) {
	b.once.Do(func() { close(b.started) })
	<-b.release
	return nil, false
}

func (b *blockingIterator) Err() error   { return nil }
func (b *blockingIterator) Close() error { return nil }

func TestHandlerCommandPhaseDrivesStatusBar(t *testing.T) {
	it := &blockingIterator{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	mock := &mockCommandHandler{
		handleFunc: func(context.Context, string, []string) (CommandResult, error) {
			return CommandResult{Phase: "COMPACTING", Display: it}, nil
		},
	}
	comp := NewComponent(ComponentConfig{
		StatusBar: StatusBarConfig{Enabled: true},
	})
	h, tx, _ := Handler(context.Background(), new(sync.Mutex), comp,
		term.FuncInterrupter(func(context.Context) error { return nil }),
		WithCommands(mock),
	)
	defer close(tx)
	h.Resize(80, 9)

	require.Equal(t, StatusBarState{}, comp.StatusBarState())

	typeText(h, "/compact")
	h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})

	select {
	case <-it.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the command to start draining")
	}
	got := comp.StatusBarState()
	assert.True(t, got.Active)
	assert.Equal(t, "COMPACTING", got.Phase)
	assert.False(t, got.TurnStart.IsZero())

	close(it.release)
	assert.Eventually(t, func() bool {
		return !comp.StatusBarState().Active
	}, 2*time.Second, 10*time.Millisecond)
	assert.Empty(t, comp.StatusBarState().Phase)
}

func TestHandlerCommandWithoutPhaseLeavesStatusBar(t *testing.T) {
	mock := &mockCommandHandler{
		handleFunc: func(context.Context, string, []string) (CommandResult, error) {
			return CommandResult{Display: iterator.Empty[component.Responsive]()}, nil
		},
	}
	comp := NewComponent(ComponentConfig{
		StatusBar: StatusBarConfig{Enabled: true},
	})
	comp.SetStatusBarState(func(s *StatusBarState) { s.Model = "sonnet" })
	h, tx, _ := Handler(context.Background(), new(sync.Mutex), comp,
		term.FuncInterrupter(func(context.Context) error { return nil }),
		WithCommands(mock),
	)
	defer close(tx)
	h.Resize(80, 9)

	typeText(h, "/history")
	h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})

	assert.Never(t, func() bool {
		return comp.StatusBarState().Active
	}, 200*time.Millisecond, 10*time.Millisecond)
	assert.Equal(t, "sonnet", comp.StatusBarState().Model)
}

func TestHandlerDrawsStatusBarImagesAfterTheDialogue(t *testing.T) {
	tests := []struct {
		name       string
		directives string
		shader     string
		// expected is the clip of the image, which is cut off at the
		// dialogue's edges.
		expected image.Rectangle
	}{
		{
			name:       "cut off at the dialogue's edges",
			directives: `| width 4 | height 3`,
			// The bar is the last of 6 rows, and the image is centered
			// on its first cell.
			expected: image.Rect(0, 4, 2, 6),
		},
		{
			name:       "overflowing the dialogue's edges",
			directives: `| width 4 | height 3 | overflow`,
		},
		{
			name:       "while the shader runs over the bar",
			directives: `| width 4 | height 3`,
			shader:     "pulse",
			expected:   image.Rect(0, 4, 2, 6),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			irq := make(interrupts, 1)
			h, comp := newImageDialogue(t,
				`{{ .Image | src %q `+tt.directives+` }}`, tt.shader, irq)
			comp.SetStatusBarState(func(s *StatusBarState) { s.Active = true })
			h.Resize(20, 6)

			rec := drawLoadedDialogue(t, h, irq)
			require.Len(t, rec.Images, 1)
			img := rec.Images[0]
			assert.Equal(t, tt.expected, img.Clip)
			assert.True(t, img.VerticalRenderOffset)
			assert.NotZero(t, rec.Cells)
			assert.Zero(t, rec.CellsAfterImage, "the dialogue covers the image")
		})
	}
}

func TestHandlerHidesStatusBarImagesWithTheBar(t *testing.T) {
	h, _ := newImageDialogue(t, `{{ .Image | src %q }}`, "", make(interrupts, 1))
	h.Resize(20, minMessagesRows)
	rec := &handlerrpctest.ImageRecorder{}
	h.Draw(rec)
	assert.Empty(t, rec.Images)
}

func TestHandlerDrawsStatusBarImageAltTextOverTheDialogue(t *testing.T) {
	irq := make(interrupts, 1)
	h, _ := newImageDialogue(t,
		`{{ .Image | src %q | width 1 | y_offset -2 | alt "x" }}`, "", irq)
	h.Resize(20, 6)

	w := term.NewStringWriter(20, 6)
	timeout := time.After(5 * time.Second)
	for {
		require.NoError(t, w.Clear(term.Attributes{}))
		h.Draw(w)
		require.NoError(t, w.Flush())
		if strings.HasPrefix(strings.Split(w.String(), "\n")[3], "x") {
			return
		}
		select {
		case <-irq:
		case <-timeout:
			t.Fatalf("timed out waiting for the alt text:\n%s", w.String())
		}
	}
}

// newImageDialogue returns a dialogue whose status bar layout is
// formatted with the URI of a picture and runs shader, and which
// repaints through irq.
func newImageDialogue(
	t *testing.T, layout, shader string, irq interrupts,
) (tui.Handler, *Component) {
	t.Helper()
	parsed, err := ParseStatusBarLayout(fmt.Sprintf(layout, samplePictureURI(t)))
	require.NoError(t, err)
	comp := NewComponent(ComponentConfig{StatusBar: StatusBarConfig{
		Enabled: true,
		Layout:  parsed,
		Shader:  shader,
		Storage: storagestub.NewInMemoryService(),
	}})
	h, tx, _ := Handler(context.Background(), new(sync.Mutex), comp, irq)
	t.Cleanup(func() {
		close(tx)
		assert.NoError(t, comp.Close())
	})
	return h, comp
}

// drawLoadedDialogue draws h until its status bar image has loaded, and
// returns what that draw recorded.
func drawLoadedDialogue(
	t *testing.T, h tui.Handler, irq interrupts,
) *handlerrpctest.ImageRecorder {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		rec := &handlerrpctest.ImageRecorder{}
		h.Draw(rec)
		if len(rec.Images) > 0 {
			return rec
		}
		select {
		case <-irq:
		case <-timeout:
			t.Fatal("timed out waiting for the image to load")
		}
	}
}

type pointerLeaveChat struct {
	h           tui.Handler
	comp        *Component
	tools       *recordingToolOpener
	attachments *recordingOpener
	links       *int
	clip        clipboard.Register
}

// newPointerLeaveChat builds a handler over a transcript with every kind
// of mouse target: sent attachment chips, reasoning, tool rows with
// children and results, a code block copy icon and a link, plus a
// multi-line draft with a pending attachment.
func newPointerLeaveChat(t *testing.T) pointerLeaveChat {
	t.Helper()
	links := new(int)
	clip := clipboard.NewInMemory()
	comp := NewComponent(ComponentConfig{
		Clipboard:   clip,
		OnLinkClick: func(*url.URL) bool { *links++; return true },
		Editor:      standard.Editor(standard.WithClipboard(clipboard.NewInMemory())),
	})
	tools, attachments := &recordingToolOpener{}, &recordingOpener{}
	h, tx, _ := Handler(context.Background(), new(sync.Mutex), comp, term.NopInterrupter(),
		WithToolCallOpener(tools), WithAttachmentOpener(attachments))
	t.Cleanup(func() { close(tx) })

	comp.AddSendMessageAttachments("Fix the off-by-one in alpha.go",
		[]Attachment{NewWorkspaceFileAttachment("alpha.go")})
	comp.AddReasoningChunk("I should read **alpha.go** before editing it.")
	comp.AddToolCall("read", "read_file", `{"path":"alpha.go"}`, "alpha.go")
	comp.CompleteToolCall("read", "read_file", `{"path":"alpha.go"}`, "alpha.go",
		"package alpha\n\nfunc Last(s []int) int { return s[len(s)] }", false)
	comp.AddToolCall("agent", "agent", `{"description":"find callers"}`, "find callers")
	comp.AddChildToolCall("agent", "refs", "find_references", `{"symbol":"Last"}`, "Last")
	comp.CompleteChildToolCall("agent", "refs", "find_references", `{"symbol":"Last"}`, "Last",
		"beta.go:12: alpha.Last(xs)", false)
	comp.CompleteToolCall("agent", "agent", `{"description":"find callers"}`, "find callers",
		"One caller in beta.go", false)
	comp.AddReceiveMessage("The index is off by one:\n\n```go\nfunc Last(s []int) int {\n" +
		"\treturn s[len(s)-1]\n}\n```\n\nSee [the spec](https://go.dev/ref/spec#Index_expressions).")
	comp.Input().SetText("Also add a test\nfor the empty slice\ncase.")
	comp.AddAttachment(NewWorkspaceFileAttachment("beta.go"))
	return pointerLeaveChat{
		h: h, comp: comp, tools: tools, attachments: attachments, links: links, clip: clip,
	}
}

func TestDialogueHandlerPointerLeaves(t *testing.T) {
	t.Parallel()
	for _, size := range []struct{ width, height int }{{60, 15}, {80, 30}, {1, 1}} {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			t.Parallel()
			handlertest.RunPointerLeaves(t, newPointerLeaveChat(t).h, size.width, size.height)
		})
	}
}

func TestDialogueHandlerPointerLeaveKeepsState(t *testing.T) {
	t.Parallel()
	const width, height = 100, 70
	press := func(at term.Coordinates) []term.Event {
		return []term.Event{mouseEv(at.X, at.Y, term.MouseLeft)}
	}
	gestures := []struct {
		name   string
		events func(at term.Coordinates) []term.Event
	}{
		{name: "hover", events: func(at term.Coordinates) []term.Event {
			return []term.Event{mouseEv(at.X, at.Y, 0)}
		}},
		{name: "click", events: func(at term.Coordinates) []term.Event {
			return []term.Event{
				mouseEv(at.X, at.Y, 0), mouseEv(at.X, at.Y, term.MouseLeft),
				mouseEv(at.X, at.Y, term.MouseRelease), mouseEv(at.X, at.Y, 0),
			}
		}},
		{name: "drag", events: func(at term.Coordinates) []term.Event {
			return clickDrag(at.X, at.Y, at.X+3, at.Y)
		}},
		{name: "press", events: press},
		{name: "wheel", events: func(at term.Coordinates) []term.Event {
			return []term.Event{mouseEv(at.X, at.Y, term.MouseWheelUp)}
		}},
	}
	targets := []struct {
		name string
		find func(t *testing.T, c pointerLeaveChat, w *term.StringWriter) term.Coordinates
	}{
		{name: "tool name", find: func(t *testing.T, _ pointerLeaveChat, w *term.StringWriter) term.Coordinates {
			at := findText(t, w, width, "read_file")
			return term.Coordinates{X: at.X + 1, Y: at.Y}
		}},
		{name: "child tool name", find: func(t *testing.T, _ pointerLeaveChat, w *term.StringWriter) term.Coordinates {
			return findText(t, w, width, "find_references")
		}},
		{name: "sent chip", find: func(t *testing.T, c pointerLeaveChat, _ *term.StringWriter) term.Coordinates {
			return chipScreenPos(t, c.comp)
		}},
		{name: "copy icon", find: func(t *testing.T, _ pointerLeaveChat, w *term.StringWriter) term.Coordinates {
			return findText(t, w, width, string(DefaultMarkdownConfig().CodeBlockCopyIcon))
		}},
		{name: "link", find: func(t *testing.T, _ pointerLeaveChat, w *term.StringWriter) term.Coordinates {
			return findText(t, w, width, "the spec")
		}},
		{name: "message text", find: func(t *testing.T, _ pointerLeaveChat, w *term.StringWriter) term.Coordinates {
			return findText(t, w, width, "index is off")
		}},
		{name: "draft", find: func(t *testing.T, _ pointerLeaveChat, w *term.StringWriter) term.Coordinates {
			return findText(t, w, width, "empty slice")
		}},
	}
	leaves := []term.Coordinates{
		{X: -1, Y: height / 2}, {X: width, Y: height / 2}, {X: width / 2, Y: -1},
		{X: width / 2, Y: height}, {X: -500, Y: -500}, {X: 500, Y: 500},
	}
	type state struct {
		exit, inputFocused, dragging      bool
		selection                         string
		selected                          bool
		cursor                            term.Coordinates
		cursorStyle                       term.CursorStyle
		cursorVisible                     bool
		offset, tools, attachments, links int
		toolPressed, toolCanceled         bool
		clipboard                         string
	}
	snapshot := func(c pointerLeaveChat, exit bool) state {
		dh := c.h.(*dialogueHandler)
		sel, selected := c.h.Selection()
		cursor, shape, visible := c.h.Cursor()
		data, _ := c.clip.Paste(clipboard.DefaultRegisterID)
		return state{
			exit: exit, inputFocused: dh.inputFocused, dragging: dh.messagesDragging,
			selection: sel, selected: selected,
			cursor: cursor, cursorStyle: shape, cursorVisible: visible,
			offset: c.comp.messages.Offset(), tools: len(c.tools.opened),
			attachments: len(c.attachments.opened), links: *c.links,
			toolPressed: dh.toolCallPressed, toolCanceled: dh.toolCallCanceled,
			clipboard: data.Text,
		}
	}
	for _, target := range targets {
		for _, gesture := range gestures {
			for _, leave := range leaves {
				t.Run(fmt.Sprintf("%s %s then %v", gesture.name, target.name, leave), func(t *testing.T) {
					t.Parallel()
					c := newPointerLeaveChat(t)
					c.h.Resize(width, height)
					w := term.NewStringWriter(width, height)
					c.h.Draw(w)
					var exit bool
					for _, ev := range gesture.events(target.find(t, c, w)) {
						exit, _ = c.h.Handle(ev)
						c.h.Draw(term.NewStringWriter(width, height))
					}
					before := snapshot(c, exit)

					exit, _ = c.h.Handle(mouseEv(leave.X, leave.Y, 0))
					c.h.Draw(term.NewStringWriter(width, height))

					assert.Equal(t, before, snapshot(c, exit))
					off := term.Coordinates{X: -1, Y: -1}
					assert.False(t, c.comp.HoverToolCall(off), "a tool row stays hovered")
					assert.False(t, c.comp.HoverSentAttachment(off), "a sent chip stays hovered")
					assert.False(t, c.comp.hoverCodeCopy(off), "a copy icon stays hovered")
				})
			}
		}
	}
}

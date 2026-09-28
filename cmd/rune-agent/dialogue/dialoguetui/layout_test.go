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
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/handlertest"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
)

// TestDialogueLayout drives the dialogue handler end to end and pins the
// rendered frame for the messages/compose split. The compose box is bottom
// anchored, is cfg.InputRowColumns wide out of component.MaxCols, grows
// upwards as its content wraps, and is capped so the conversation never
// disappears; the messages region takes whatever rows are left. Frames are
// rendered with the cursor substituted in, so each one pins its placement too.
//
// Regenerate every frame after an intentional layout change with:
//
//	DIALOGUE_LAYOUT_GOLDEN=1 go test ./cmd/rune-agent/dialogue/dialoguetui \
//	    -run TestDialogueLayout -v
func TestDialogueLayout(t *testing.T) {
	cases := []layoutCase{
		{
			name:   "default split",
			width:  24,
			height: 10,
			steps: []handlertest.SequenceTestCase{{
				Expected: "                        \n" +
					"                        \n" +
					"                        \n" +
					"                        \n" +
					"                        \n" +
					"                        \n" +
					"                        \n" +
					"  ┌──────────────────┐  \n" +
					"  │▐                 │  \n" +
					"  └──────────────────┘  ",
			}},
		},
		{
			name:   "typed text fills the single box row",
			width:  24,
			height: 10,
			steps: []handlertest.SequenceTestCase{{
				InputSequence: "hello<space>there",
				Expected: "                        \n" +
					"                        \n" +
					"                        \n" +
					"                        \n" +
					"                        \n" +
					"                        \n" +
					"                        \n" +
					"  ┌──────────────────┐  \n" +
					"  │hello there▐      │  \n" +
					"  └──────────────────┘  ",
			}},
		},
		{
			name:   "wrapping grows the box upwards",
			width:  24,
			height: 10,
			steps: []handlertest.SequenceTestCase{{
				// the box is 20 columns wide, so 18 columns of content fit
				InputSequence: strings.Repeat("x", 18),
				Expected: "                        \n" +
					"                        \n" +
					"                        \n" +
					"                        \n" +
					"                        \n" +
					"                        \n" +
					"                        \n" +
					"  ┌──────────────────┐  \n" +
					"  │xxxxxxxxxxxxxxxxxx▐  \n" +
					"  └──────────────────┘  ",
			}, {
				InputSequence: "y",
				Expected: "                        \n" +
					"                        \n" +
					"                        \n" +
					"                        \n" +
					"                        \n" +
					"                        \n" +
					"  ┌──────────────────┐  \n" +
					"  │xxxxxxxxxxxxxxxxxy▐  \n" +
					"  │                  │  \n" +
					"  └──────────────────┘  ",
			}},
		},
		{
			name:   "backspace shrinks the box back",
			width:  24,
			height: 10,
			steps: []handlertest.SequenceTestCase{{
				InputSequence: strings.Repeat("x", 19),
				Expected: "                        \n" +
					"                        \n" +
					"                        \n" +
					"                        \n" +
					"                        \n" +
					"                        \n" +
					"  ┌──────────────────┐  \n" +
					"  │xxxxxxxxxxxxxxxxxx▐  \n" +
					"  │                  │  \n" +
					"  └──────────────────┘  ",
			}, {
				InputSequence: "<backspace>",
				Expected: "                        \n" +
					"                        \n" +
					"                        \n" +
					"                        \n" +
					"                        \n" +
					"                        \n" +
					"                        \n" +
					"  ┌──────────────────┐  \n" +
					"  │xxxxxxxxxxxxxxxxx▐│  \n" +
					"  └──────────────────┘  ",
			}},
		},
		{
			name:   "box is capped so the conversation keeps two rows",
			width:  24,
			height: 8,
			events: []MessageEvent{
				{Type: MessageEventText, Text: "first"},
				{Type: MessageEventBreak},
				{Type: MessageEventText, Text: "second"},
			},
			steps: []handlertest.SequenceTestCase{{
				InputSequence: strings.Repeat("z", 90),
				Expected: "second                  \n" +
					"                        \n" +
					"  ┌──────────────────┐  \n" +
					"  │zzzzzzzzzzzzzzzzzz▐  \n" +
					"  │                  │  \n" +
					"  │                  │  \n" +
					"  │                  │  \n" +
					"  └──────────────────┘  ",
			}},
		},
		{
			name:   "box keeps its three row floor on a short viewport",
			width:  24,
			height: 5,
			steps: []handlertest.SequenceTestCase{{
				InputSequence: strings.Repeat("z", 40),
				Expected: "                        \n" +
					"                        \n" +
					"  ┌──────────────────┐  \n" +
					"  │zzzzzzzzzzzzzzzzzz▐  \n" +
					"  └──────────────────┘  ",
			}},
		},
		{
			name:   "viewport shorter than the box floor drops the frame",
			width:  24,
			height: 2,
			steps: []handlertest.SequenceTestCase{{
				InputSequence: "hi",
				Expected: "  hi▐                   \n" +
					"                        ",
			}},
		},
		{
			name:   "single row viewport",
			width:  24,
			height: 1,
			steps: []handlertest.SequenceTestCase{{
				InputSequence: "hi",
				Expected:      "  hi▐                   ",
			}},
		},
		{
			name:   "odd width keeps the box on the column grid",
			width:  25,
			height: 8,
			steps: []handlertest.SequenceTestCase{{
				InputSequence: "odd",
				Expected: "                         \n" +
					"                         \n" +
					"                         \n" +
					"                         \n" +
					"                         \n" +
					"  ┌──────────────────┐   \n" +
					"  │odd▐              │   \n" +
					"  └──────────────────┘   ",
			}},
		},
		{
			name:   "viewport too narrow for a frame",
			width:  3,
			height: 6,
			steps: []handlertest.SequenceTestCase{{
				InputSequence: "ab",
				Expected: "   \n" +
					"   \n" +
					"   \n" +
					"   \n" +
					"   \n" +
					"ab▐",
			}},
		},
		{
			name:   "full width input row",
			width:  24,
			height: 8,
			cfg:    ComponentConfig{InputRowColumns: component.MaxCols},
			steps: []handlertest.SequenceTestCase{{
				InputSequence: "wide",
				Expected: "                        \n" +
					"                        \n" +
					"                        \n" +
					"                        \n" +
					"                        \n" +
					"┌──────────────────────┐\n" +
					"│wide▐                 │\n" +
					"└──────────────────────┘",
			}},
		},
		{
			name:   "single column input row",
			width:  24,
			height: 8,
			cfg:    ComponentConfig{InputRowColumns: 1},
			steps: []handlertest.SequenceTestCase{{
				InputSequence: "narrow",
				Expected: "                        \n" +
					"                        \n" +
					"                        \n" +
					"                        \n" +
					"                        \n" +
					"          ow▐           \n" +
					"                        \n" +
					"                        ",
			}},
		},
		{
			// NOTE: the compose box wraps on rune count, not display width,
			// so ten double width runes (20 columns) still fit on the single
			// 18 column row and scroll horizontally instead of wrapping.
			name:   "wide runes overflow the box row",
			width:  24,
			height: 10,
			steps: []handlertest.SequenceTestCase{{
				InputSequence: "世界世界世界世界世界",
				Expected: "                        \n" +
					"                        \n" +
					"                        \n" +
					"                        \n" +
					"                        \n" +
					"                        \n" +
					"                        \n" +
					"  ┌──────────────────┐  \n" +
					"  │界 世 界 世 界 世 界 世 界 ▐  \n" +
					"  └──────────────────┘  ",
			}},
		},
		{
			name:   "wide runes in the conversation",
			width:  24,
			height: 10,
			events: []MessageEvent{
				{Type: MessageEventText, Text: "你好世界，這是一個比較長的回覆"},
			},
			steps: []handlertest.SequenceTestCase{{
				Expected: "你 好 世 界 ， 這 是 一 個 比 較 長 \n" +
					"的 回 覆                   \n" +
					"                        \n" +
					"                        \n" +
					"                        \n" +
					"                        \n" +
					"                        \n" +
					"  ┌──────────────────┐  \n" +
					"  │▐                 │  \n" +
					"  └──────────────────┘  ",
			}},
		},
		{
			// NOTE: NUL and BEL consume no cell, so the first row renders
			// one column short of the viewport width.
			name:   "control runes in the conversation",
			width:  24,
			height: 10,
			events: []MessageEvent{
				{Type: MessageEventText, Text: "a\tb\x00c\x07d"},
			},
			steps: []handlertest.SequenceTestCase{{
				Expected: "a b cd                 \n" +
					"                        \n" +
					"                        \n" +
					"                        \n" +
					"                        \n" +
					"                        \n" +
					"                        \n" +
					"  ┌──────────────────┐  \n" +
					"  │▐                 │  \n" +
					"  └──────────────────┘  ",
			}},
		},
		{
			name:   "empty message text",
			width:  24,
			height: 10,
			events: []MessageEvent{
				{Type: MessageEventText, Text: ""},
				{Type: MessageEventBreak},
			},
			steps: []handlertest.SequenceTestCase{{
				Expected: "                        \n" +
					"                        \n" +
					"                        \n" +
					"                        \n" +
					"                        \n" +
					"                        \n" +
					"                        \n" +
					"  ┌──────────────────┐  \n" +
					"  │▐                 │  \n" +
					"  └──────────────────┘  ",
			}},
		},
		{
			name:   "conversation taller than its region keeps the tail visible",
			width:  24,
			height: 10,
			events: []MessageEvent{
				{Type: MessageEventText, Text: "one"},
				{Type: MessageEventBreak},
				{Type: MessageEventText, Text: "two"},
				{Type: MessageEventBreak},
				{Type: MessageEventText, Text: "three"},
				{Type: MessageEventBreak},
				{Type: MessageEventText, Text: "four"},
				{Type: MessageEventBreak},
				{Type: MessageEventText, Text: "five"},
				{Type: MessageEventBreak},
				{Type: MessageEventText, Text: "six"},
			},
			steps: []handlertest.SequenceTestCase{{
				Expected: "                        \n" +
					"four                    \n" +
					"                        \n" +
					"five                    \n" +
					"                        \n" +
					"six                     \n" +
					"                        \n" +
					"  ┌──────────────────┐  \n" +
					"  │▐                 │  \n" +
					"  └──────────────────┘  ",
			}},
		},
		{
			name:   "a growing box takes rows from the conversation",
			width:  24,
			height: 10,
			events: []MessageEvent{
				{Type: MessageEventText, Text: "one"},
				{Type: MessageEventBreak},
				{Type: MessageEventText, Text: "two"},
				{Type: MessageEventBreak},
				{Type: MessageEventText, Text: "three"},
			},
			steps: []handlertest.SequenceTestCase{{
				InputSequence: strings.Repeat("x", 19),
				Expected: "one                     \n" +
					"                        \n" +
					"two                     \n" +
					"                        \n" +
					"three                   \n" +
					"                        \n" +
					"  ┌──────────────────┐  \n" +
					"  │xxxxxxxxxxxxxxxxxx▐  \n" +
					"  │                  │  \n" +
					"  └──────────────────┘  ",
			}, {
				InputSequence: strings.Repeat("x", 18),
				Expected: "                        \n" +
					"two                     \n" +
					"                        \n" +
					"three                   \n" +
					"                        \n" +
					"  ┌──────────────────┐  \n" +
					"  │xxxxxxxxxxxxxxxxxx▐  \n" +
					"  │                  │  \n" +
					"  │                  │  \n" +
					"  └──────────────────┘  ",
			}},
		},
		{
			name:   "error message alongside a wrapped box",
			width:  24,
			height: 10,
			events: []MessageEvent{
				{Type: MessageEventError, Text: "boom: something went wrong"},
			},
			steps: []handlertest.SequenceTestCase{{
				InputSequence: strings.Repeat("x", 19),
				Expected: "! boom: something went  \n" +
					"wrong                   \n" +
					"                        \n" +
					"                        \n" +
					"                        \n" +
					"                        \n" +
					"  ┌──────────────────┐  \n" +
					"  │xxxxxxxxxxxxxxxxxx▐  \n" +
					"  │                  │  \n" +
					"  └──────────────────┘  ",
			}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runLayoutCase(t, tc)
		})
	}
}

// TestDialogueLayoutCursorFreshBeforeDraw covers the stale cursor bug: the
// host requests the cursor before it requests the frame, so a compose box
// that just grew must already report the new position without an
// interleaved Draw. Golden frames cannot express this because they always
// draw first.
func TestDialogueLayoutCursorFreshBeforeDraw(t *testing.T) {
	const width, height = 24, 10
	h := newLayoutHandler(t, layoutCase{width: width, height: height})
	w := term.NewStringWriter(width, height)
	h.Draw(w)

	top, _, ok := h.Cursor()
	require.True(t, ok)
	var grew bool
	for range 24 {
		h.Handle(term.Event{Type: term.EventKey, Ch: 'x'})

		preDraw, _, ok := h.Cursor()
		require.True(t, ok)
		h.Draw(w)
		postDraw, _, ok := h.Cursor()
		require.True(t, ok)

		assert.Equal(t, postDraw, preDraw,
			"cursor must not lag the frame the box grows in")
		if preDraw.Y < top.Y {
			grew = true
		}
	}
	assert.True(t, grew, "typing never wrapped the compose box")
}

// TestDialogueLayoutIdleDrawDoesNotResize pins the other half of the fix:
// a frame that changes nothing must not re-resize the conversation, which
// is what made every draw rebuild each message component.
func TestDialogueLayoutIdleDrawDoesNotResize(t *testing.T) {
	comp := NewComponent(ComponentConfig{})
	comp.Resize(24, 10)

	counter := new(resizeCounter)
	comp.AddCommandOutput(counter)

	w := term.NewStringWriter(24, 10)
	comp.Draw(w)
	settled := counter.resizes
	require.Positive(t, settled)

	for range 5 {
		comp.Draw(w)
	}
	assert.Equal(t, settled, counter.resizes, "idle frames must not resize messages")

	// content that grows must still be re-laid out
	counter.rows = 4
	comp.Draw(w)
	assert.Greater(t, counter.resizes, settled)
}

// layoutCase is a single dialogue layout scenario: a viewport size, the
// conversation events delivered before any input, and the input sequences
// whose resulting frames are pinned.
type layoutCase struct {
	name   string
	width  int
	height int
	cfg    ComponentConfig
	events []MessageEvent
	steps  []handlertest.SequenceTestCase
}

func runLayoutCase(t *testing.T, tc layoutCase) {
	t.Helper()
	h := newLayoutHandler(t, tc)
	if os.Getenv("DIALOGUE_LAYOUT_GOLDEN") == "" {
		handlertest.RunHandlerSequence(t, h, tc.width, tc.height, tc.steps)
		return
	}
	generateLayoutGolden(t, tc, h)
}

// newLayoutHandler builds a dialogue handler for tc, sizes it, and delivers
// tc.events through the real incoming-message channel.
func newLayoutHandler(t *testing.T, tc layoutCase) tui.Handler {
	t.Helper()

	interrupt := make(chan struct{})
	h, tx, rx := Handler(context.Background(), new(sync.Mutex),
		NewComponent(tc.cfg), term.FuncInterrupter(func(context.Context) error {
			interrupt <- struct{}{}
			return nil
		}))
	t.Cleanup(func() { close(tx) })

	// drain submissions so a sequence containing <enter> never blocks
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	go func() {
		for {
			select {
			case <-rx:
			case <-done:
				return
			}
		}
	}()

	// size before delivering content so messages wrap against the real width
	h.Resize(tc.width, tc.height)
	for _, ev := range tc.events {
		tx <- ev
		<-interrupt
	}
	return h
}

func generateLayoutGolden(t *testing.T, tc layoutCase, h tui.Handler) {
	t.Helper()
	h.Resize(tc.width, tc.height)
	w := term.NewStringWriter(tc.width, tc.height)
	for _, step := range tc.steps {
		require.NoError(t, w.Clear(term.Attributes{}))
		keys, err := term.ParseKeys(step.InputSequence)
		require.NoError(t, err)
		for _, k := range keys {
			h.Handle(term.Event{Ch: k.Ch, Mod: k.Mod, Key: k.Key, Type: term.EventKey})
		}
		h.Draw(w)
		if cursor, _, ok := h.Cursor(); ok {
			w.SetCursor(cursor)
		}
		require.NoError(t, w.Flush())
		fmt.Printf("@@@STEP\n%s\n@@@END\n", w.String())
	}
}

// resizeCounter is a message stub that records how many times the layout
// resized it and can report a taller content height on demand.
type resizeCounter struct {
	rows    int
	resizes int
}

func (r *resizeCounter) Resize(int, int)  { r.resizes++ }
func (r *resizeCounter) Draw(term.Writer) {}
func (r *resizeCounter) Height(int) int   { return r.rows }

// TestDialogueLayoutStatusBarRow pins the bottom row the status bar
// claims, and the row every other region gives up for it.
func TestDialogueLayoutStatusBarRow(t *testing.T) {
	const width, height = 40, 12

	tests := []struct {
		name    string
		cfg     ComponentConfig
		wantBar int
	}{
		{name: "bar disabled", cfg: ComponentConfig{}, wantBar: 0},
		{
			name:    "bar enabled",
			cfg:     ComponentConfig{StatusBar: StatusBarConfig{Enabled: true}},
			wantBar: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			comp := NewComponent(tt.cfg)
			t.Cleanup(func() { _ = comp.Close() })
			comp.Resize(width, height)

			assert.Equal(t, tt.wantBar, comp.barHeight())

			boxH := comp.boxHeight(comp.boxWidth(width))
			assert.Equal(t, height-tt.wantBar,
				comp.InputPosition().Y+boxH,
				"compose box must sit directly above the bar")

			if tt.wantBar > 0 {
				assert.Equal(t, height-1, comp.barArea.Position().Y)
				assert.Equal(t, width, comp.barArea.Width())
				assert.Equal(t, 1, comp.barArea.Height())
			}
		})
	}
}

// TestDialogueLayoutStatusBarYieldsToTranscript covers the vertical
// starvation guard: the compose box gives up rows before the transcript
// drops below minMessagesRows, and the bar gives up its row entirely
// once the viewport cannot afford it.
func TestDialogueLayoutStatusBarYieldsToTranscript(t *testing.T) {
	for height := 1; height <= 12; height++ {
		comp := NewComponent(ComponentConfig{
			StatusBar: StatusBarConfig{Enabled: true},
		})
		comp.Resize(40, height)

		barH := comp.barHeight()
		if height <= minMessagesRows {
			assert.Zero(t, barH, "height %d", height)
		} else {
			assert.Equal(t, 1, barH, "height %d", height)
		}

		boxH := comp.boxHeight(comp.boxWidth(40))
		assert.LessOrEqual(t, boxH, max(3, height-minMessagesRows-barH),
			"compose box must yield rows at height %d", height)
		assert.Equal(t, height-barH, comp.InputPosition().Y+min(boxH, height-barH),
			"height %d", height)
		_ = comp.Close()
	}
}

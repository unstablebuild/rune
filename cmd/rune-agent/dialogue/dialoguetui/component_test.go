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
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/component/comptest"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/component/markdown"
)

var (
	_ tui.Component        = (*Component)(nil)
	_ component.Responsive = (*Component)(nil)
)

func TestComponentDraw(t *testing.T) {
	for i := range 2 {
		var desc string
		if i == 0 {
			desc = "resize before adding history"
		} else {
			desc = "resize after adding history"
		}

		t.Run(desc, func(t *testing.T) {
			comp := NewComponent(ComponentConfig{})
			if i == 0 {
				comp.Resize(20, 10)
			}

			comp.AddSendMessage("wasup bro")
			comp.AddReceiveMessage("I am a AI assistant blabla.")
			comp.AddSendMessage("Speak in bro.")
			comp.AddReceiveMessage("Yo, wa'tup.")

			if i == 1 {
				comp.Resize(20, 10)
			}

			w := term.NewStringWriter(21, 11)
			tests := []comptest.TestCase{
				{
					Action:   func() {},
					Expected: "wasup bro            \nI am a AI assistant  \nblabla.              \n                     \nSpeak in bro.        \nYo, wa'tup.          \n                     \n ┌──────────────┐    \n │              │    \n └──────────────┘    \n                     ",
				},
				{
					Action: func() {
						comp.AddSendMessage("This is rather bore")
						comp.AddSendMessage("This is rather bore")
						comp.AddSendMessage("This is rather bore")
					},
					Expected: "                     \nSpeak in bro.        \nYo, wa'tup.          \n                     \nThis is rather bore  \nThis is rather bore  \nThis is rather bore  \n ┌──────────────┐    \n │              │    \n └──────────────┘    \n                     ",
				},
				{
					Action: func() {
						assert.True(t, comp.SeekUp())
						assert.True(t, comp.SeekUp())
						assert.True(t, comp.SeekUp())
						assert.False(t, comp.SeekUp())
					},
					Expected: "wasup bro            \nI am a AI assistant  \nblabla.              \n                     \nSpeak in bro.        \nYo, wa'tup.          \n                     \n ┌──────────────┐    \n │              │    \n └──────────────┘    \n                     ",
				},
				{
					Action: func() {
						assert.True(t, comp.SeekDown())
						assert.True(t, comp.SeekDown())
						assert.True(t, comp.SeekDown())
						assert.False(t, comp.SeekDown())
					},
					Expected: "                     \nSpeak in bro.        \nYo, wa'tup.          \n                     \nThis is rather bore  \nThis is rather bore  \nThis is rather bore  \n ┌──────────────┐    \n │              │    \n └──────────────┘    \n                     ",
				},
				{
					Action: func() {
						comp.AddReceiveMessageChunk("1234")
						comp.Reset()
						comp.AddReceiveMessageChunk("1234")
					},
					Expected: `1234                 
                     
                     
                     
                     
                     
                     
 ┌──────────────┐    
 │              │    
 └──────────────┘    
                     `,
				},
			}
			comptest.TestComponent(t, comp, w, tests)
		})
	}
}

func TestComponentScrollPreservedOnAppend(t *testing.T) {
	// 10 single-line messages in a 20x10 viewport.
	// Messages area = 7 rows (10 − 3 for input box).
	// MaxOffset = 10 − 7 = 3.
	type testCase struct {
		name                string
		seekUpCount         int
		expectedAfterScroll string
		expectedAfterAppend string
	}

	cases := []testCase{
		{
			name:        "at bottom",
			seekUpCount: 0,
			expectedAfterScroll: "" +
				"line4                \n" +
				"line5                \n" +
				"line6                \n" +
				"line7                \n" +
				"line8                \n" +
				"line9                \n" +
				"line10               \n" +
				" ┌──────────────┐    \n" +
				" │              │    \n" +
				" └──────────────┘    \n" +
				"                     ",
			// Not scrolled up: viewport follows new content.
			expectedAfterAppend: "" +
				"line5                \n" +
				"line6                \n" +
				"line7                \n" +
				"line8                \n" +
				"line9                \n" +
				"line10               \n" +
				"appended             \n" +
				" ┌──────────────┐    \n" +
				" │              │    \n" +
				" └──────────────┘    \n" +
				"                     ",
		},
		{
			name:        "mid scroll",
			seekUpCount: 2,
			expectedAfterScroll: "" +
				"line2                \n" +
				"line3                \n" +
				"line4                \n" +
				"line5                \n" +
				"line6                \n" +
				"line7                \n" +
				"line8                \n" +
				" ┌──────────────┐    \n" +
				" │              │    \n" +
				" └──────────────┘    \n" +
				"                     ",
			// Scrolled up: viewport stays put.
			expectedAfterAppend: "" +
				"line2                \n" +
				"line3                \n" +
				"line4                \n" +
				"line5                \n" +
				"line6                \n" +
				"line7                \n" +
				"line8                \n" +
				" ┌──────────────┐    \n" +
				" │              │    \n" +
				" └──────────────┘    \n" +
				"                     ",
		},
		{
			name:        "max scroll",
			seekUpCount: 3,
			expectedAfterScroll: "" +
				"line1                \n" +
				"line2                \n" +
				"line3                \n" +
				"line4                \n" +
				"line5                \n" +
				"line6                \n" +
				"line7                \n" +
				" ┌──────────────┐    \n" +
				" │              │    \n" +
				" └──────────────┘    \n" +
				"                     ",
			// Scrolled all the way up: viewport stays put.
			expectedAfterAppend: "" +
				"line1                \n" +
				"line2                \n" +
				"line3                \n" +
				"line4                \n" +
				"line5                \n" +
				"line6                \n" +
				"line7                \n" +
				" ┌──────────────┐    \n" +
				" │              │    \n" +
				" └──────────────┘    \n" +
				"                     ",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			comp := NewComponent(ComponentConfig{})
			comp.Resize(20, 10)
			for i := range 10 {
				comp.AddSendMessage(fmt.Sprintf("line%d", i+1))
			}
			w := term.NewStringWriter(21, 11)

			comptest.TestComponent(t, comp, w, []comptest.TestCase{
				{
					Action: func() {
						for range tc.seekUpCount {
							assert.True(t, comp.SeekUp())
						}
					},
					Expected: tc.expectedAfterScroll,
				},
				{
					Action: func() {
						comp.AddSendMessage("appended")
					},
					Expected: tc.expectedAfterAppend,
				},
			})
		})
	}
}

func TestComponentQueuedMessagesRenderAndRemove(t *testing.T) {
	comp := NewComponent(ComponentConfig{})
	comp.Resize(20, 10)

	w := term.NewStringWriter(21, 11)
	tests := []comptest.TestCase{
		{
			Action: func() {
				comp.AddQueuedMessage("first")
				comp.AddQueuedMessage("second")
			},
			Expected: "󰄝  first             \n" +
				"󰄝  second            \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				" ┌──────────────┐    \n" +
				" │              │    \n" +
				" └──────────────┘    \n" +
				"                     ",
		},
		{
			Action: func() {
				comp.RemoveLastQueuedMessage()
			},
			Expected: "󰄝  first             \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				" ┌──────────────┐    \n" +
				" │              │    \n" +
				" └──────────────┘    \n" +
				"                     ",
		},
	}
	comptest.TestComponent(t, comp, w, tests)
	assert.Equal(t, 1, comp.QueueLen())
}

func TestComponentToggleContractedPreservesVisibleAnchor(t *testing.T) {
	comp := NewComponent(ComponentConfig{})
	comp.Resize(40, 12)
	for _, id := range []string{"c1", "c2", "c3"} {
		comp.AddToolCall(id, "read_file", `{}`, id+".go")
		comp.CompleteToolCall(id, "read_file", `{}`, id+".go", strings.Repeat("file data\n", 20), false)
	}

	w := term.NewStringWriter(41, 13)
	comptest.TestComponent(t, comp, w, []comptest.TestCase{
		{
			Action: func() {},
			Expected: "file data                                \n" +
				"...                                      \n" +
				"✓ read_file c3.go                        \n" +
				"file data                                \n" +
				"file data                                \n" +
				"file data                                \n" +
				"file data                                \n" +
				"file data                                \n" +
				"...                                      \n" +
				"   ┌───────────────────────────────┐     \n" +
				"   │                               │     \n" +
				"   └───────────────────────────────┘     \n" +
				"                                         ",
		},
		{
			Action: func() {
				for range 6 {
					assert.True(t, comp.SeekUp())
				}
			},
			Expected: "...                                      \n" +
				"✓ read_file c2.go                        \n" +
				"file data                                \n" +
				"file data                                \n" +
				"file data                                \n" +
				"file data                                \n" +
				"file data                                \n" +
				"...                                      \n" +
				"✓ read_file c3.go                        \n" +
				"   ┌───────────────────────────────┐     \n" +
				"   │                               │     \n" +
				"   └───────────────────────────────┘     \n" +
				"                                         ",
		},
		{
			Action: func() {
				comp.ToggleContracted()
			},
			Expected: "├─ ✓ read_file c1.go                     \n" +
				"├─ ✓ read_file c2.go                     \n" +
				"└─ ✓ read_file c3.go                     \n" +
				"Press <ctrl-o> to expand                 \n" +
				"                                         \n" +
				"                                         \n" +
				"                                         \n" +
				"                                         \n" +
				"                                         \n" +
				"   ┌───────────────────────────────┐     \n" +
				"   │                               │     \n" +
				"   └───────────────────────────────┘     \n" +
				"                                         ",
		},
		{
			Action: func() {
				comp.ToggleContracted()
			},
			Expected: "...                                      \n" +
				"✓ read_file c2.go                        \n" +
				"file data                                \n" +
				"file data                                \n" +
				"file data                                \n" +
				"file data                                \n" +
				"file data                                \n" +
				"...                                      \n" +
				"✓ read_file c3.go                        \n" +
				"   ┌───────────────────────────────┐     \n" +
				"   │                               │     \n" +
				"   └───────────────────────────────┘     \n" +
				"                                         ",
		},
	})
}

func TestComponentQueuedMessagesStayAtBottom(t *testing.T) {
	comp := NewComponent(ComponentConfig{})
	comp.Resize(20, 10)

	w := term.NewStringWriter(21, 11)
	tests := []comptest.TestCase{
		{
			// Queue a message, then receive assistant content.
			// The queued message should stay below the assistant text.
			Action: func() {
				comp.AddQueuedMessage("follow up")
				comp.AddReceiveMessageChunk("hello world")
			},
			Expected: "hello world          \n" +
				"                     \n" +
				"󰄝  follow up         \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				" ┌──────────────┐    \n" +
				" │              │    \n" +
				" └──────────────┘    \n" +
				"                     ",
		},
		{
			// Add a tool call while queued message exists.
			// The queued message should stay below the tool call.
			Action: func() {
				comp.AddToolCall("c1", "read_file", "", "Reading file")
			},
			Expected: "hello world          \n" +
				"                     \n" +
				"⚙ read_file Reading  \n" +
				"󰄝  follow up         \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				" ┌──────────────┐    \n" +
				" │              │    \n" +
				" └──────────────┘    \n" +
				"                     ",
		},
	}
	comptest.TestComponent(t, comp, w, tests)
	assert.Equal(t, 1, comp.QueueLen())
}

func TestComponentInputPosition(t *testing.T) {
	comp := NewComponent(ComponentConfig{})
	comp.Resize(20, 10)

	assert.Equal(t, term.Coordinates{Y: 7, X: 1}, comp.InputPosition())
}

func TestComponentMessagesPosition(t *testing.T) {
	comp := NewComponent(ComponentConfig{MessagesRowConfig: component.SpanConfig{
		PadVertical:      2,
		PadHorizontal:    2,
		ContentAlignment: component.AlignmentCentered,
	}})
	comp.Resize(20, 10)

	assert.Equal(t, term.Coordinates{Y: 1, X: 1}, comp.MessagesPosition())
}

func TestComponentResetWhileStreaming(t *testing.T) {
	comp := NewComponent(ComponentConfig{MessagesRowConfig: component.SpanConfig{
		ContentAlignment: component.AlignmentCentered,
	}})
	comp.Resize(20, 10)
	assert.NotPanics(t, func() {
		comp.AddReceiveMessageChunk("1234")
		comp.Reset()
		comp.AddReceiveMessageChunk("1234")
	})
}

func TestComponentToolCall(t *testing.T) {
	comp := NewComponent(ComponentConfig{})
	comp.Resize(20, 10)

	w := term.NewStringWriter(21, 11)
	tests := []comptest.TestCase{
		{
			Action: func() {
				comp.AddReceiveMessageChunk("Let me read that.")
			},
			Expected: `Let me read that.    
                     
                     
                     
                     
                     
                     
 ┌──────────────┐    
 │              │    
 └──────────────┘    
                     `,
		},
		{
			Action: func() {
				comp.AddToolCall("c1", "read_file", "", "")
			},
			Expected: "Let me read that.    \n                     \n⚙ read_file          \n                     \n                     \n                     \n                     \n ┌──────────────┐    \n │              │    \n └──────────────┘    \n                     ",
		},
		{
			Action: func() {
				comp.CompleteToolCall("c1", "read_file", "", "", "api_key: sk-123", false)
			},
			Expected: "Let me read that.    \n                     \n✓ read_file          \napi_key: sk-123      \n                     \n                     \n                     \n ┌──────────────┐    \n │              │    \n └──────────────┘    \n                     ",
		},
		{
			Action: func() {
				comp.AddReceiveMessageChunk("Based on config.")
			},
			Expected: "Let me read that.    \n                     \n✓ read_file          \napi_key: sk-123      \nBased on config.     \n                     \n                     \n ┌──────────────┐    \n │              │    \n └──────────────┘    \n                     ",
		},
	}
	comptest.TestComponent(t, comp, w, tests)
}

func TestComponentToolCallBreaksTextStream(t *testing.T) {
	comp := NewComponent(ComponentConfig{})
	comp.Resize(20, 10)

	w := term.NewStringWriter(21, 11)
	tests := []comptest.TestCase{
		{
			Action: func() {
				comp.AddReceiveMessageChunk("Hello ")
				comp.AddToolCall("c1", "read_file", "", "")
				comp.CompleteToolCall("c1", "read_file", "", "", "output", false)
				comp.AddReceiveMessageChunk("World")
			},
			Expected: "Hello                \n                     \n✓ read_file          \noutput               \nWorld                \n                     \n                     \n ┌──────────────┐    \n │              │    \n └──────────────┘    \n                     ",
		},
	}
	comptest.TestComponent(t, comp, w, tests)
}

func TestComponentToolCallInsertedBeforeActivePrompt(t *testing.T) {
	comp := NewComponent(ComponentConfig{})
	comp.Resize(40, 12)

	resultCh := make(chan []string, 1)
	w := term.NewStringWriter(41, 13)
	tests := []comptest.TestCase{
		{
			Action: func() {
				comp.AddSendMessage("choose a db")
				comp.AddPrompt(
					"Which DB?",
					"DB",
					"questions=[...]",
					[]PromptEventOption{{Label: "Postgres"}, {Label: "SQLite"}},
					false,
					resultCh,
				)
				comp.AddToolCall("c1", "request_user_input", `{}`, "Which DB?")
			},
			Expected: "choose a db                              \n" +
				"? request_user_input Which DB?           \n" +
				"questions=[...]                          \n" +
				"                                         \n" +
				"Which DB?                           [DB] \n" +
				"                                         \n" +
				"> Postgres                               \n" +
				"  SQLite                                 \n" +
				"                                         \n" +
				"   ┌───────────────────────────────┐     \n" +
				"   │                               │     \n" +
				"   └───────────────────────────────┘     \n" +
				"                                         ",
		},
	}
	comptest.TestComponent(t, comp, w, tests)
}

func TestComponentToolCallError(t *testing.T) {
	comp := NewComponent(ComponentConfig{})
	comp.Resize(20, 10)

	w := term.NewStringWriter(21, 11)
	tests := []comptest.TestCase{
		{
			Action: func() {
				comp.AddToolCall("c1", "read_file", "", "")
				comp.CompleteToolCall("c1", "read_file", "", "", "no such file", true)
			},
			Expected: `✗ read_file          
no such file         
                     
                     
                     
                     
                     
 ┌──────────────┐    
 │              │    
 └──────────────┘    
                     `,
		},
	}
	comptest.TestComponent(t, comp, w, tests)
}

func TestComponentToolOutputTruncation(t *testing.T) {
	comp := NewComponent(ComponentConfig{ToolResultMaxLines: 3})
	comp.Resize(20, 14)

	w := term.NewStringWriter(21, 15)
	tests := []comptest.TestCase{
		{
			Action: func() {
				comp.CompleteToolCall("c1", "read_file", "", "", "line1111111111111111111"+
					"1111111111111111111111111111111111111111\nline2222222222"+
					"22222222222222222222222222\nline3\nline4\nline5", false)
			},
			Expected: "✓ read_file          \n" +
				"line1111111111111111 \n" +
				"11111111111111111111 \n" +
				"11111111111111111111 \n" +
				"111                  \n" +
				"line2222222222222222 \n" +
				"22222222222222222222 \n" +
				"line3                \n" +
				"...                  \n" +
				"                     \n" +
				"                     \n" +
				" ┌──────────────┐    \n" +
				" │              │    \n" +
				" └──────────────┘    \n" +
				"                     ",
		},
	}
	comptest.TestComponent(t, comp, w, tests)
}

func TestComponentResetWithActiveTool(t *testing.T) {
	comp := NewComponent(ComponentConfig{})
	comp.Resize(20, 10)

	w := term.NewStringWriter(21, 11)
	tests := []comptest.TestCase{
		{
			Action: func() {
				comp.AddToolCall("c1", "read_file", "", "\"y file\"")
			},
			Expected: `⚙ read_file "y file" 
                     
                     
                     
                     
                     
                     
 ┌──────────────┐    
 │              │    
 └──────────────┘    
                     `,
		},
		{
			Action: func() {
				comp.Reset()
			},
			Expected: `                     
                     
                     
                     
                     
                     
                     
 ┌──────────────┐    
 │              │    
 └──────────────┘    
                     `,
		},
	}
	comptest.TestComponent(t, comp, w, tests)
}

func TestComponentToolCallWithArgs(t *testing.T) {
	comp := NewComponent(ComponentConfig{})
	comp.Resize(20, 10)

	w := term.NewStringWriter(21, 11)
	tests := []comptest.TestCase{
		{
			Action: func() {
				comp.AddToolCall("c1", "read_file", `{"path":"/tmp/x"}`, "")
			},
			Expected: "⚙ read_file          \npath=/tmp/x          \n                     \n                     \n                     \n                     \n                     \n ┌──────────────┐    \n │              │    \n └──────────────┘    \n                     ",
		},
		{
			Action: func() {
				comp.CompleteToolCall("c1", "read_file", `{"path":"/tmp/x"}`, "", "content", false)
			},
			Expected: "✓ read_file          \npath=/tmp/x          \ncontent              \n                     \n                     \n                     \n                     \n ┌──────────────┐    \n │              │    \n └──────────────┘    \n                     ",
		},
	}
	comptest.TestComponent(t, comp, w, tests)
}

func TestComponentToolCallArgsTruncated(t *testing.T) {
	comp := NewComponent(ComponentConfig{})
	comp.Resize(20, 10)

	longArgs := ""
	for i := range 110 {
		_ = i
		longArgs += "x"
	}

	w := term.NewStringWriter(21, 11)
	tests := []comptest.TestCase{
		{
			Action: func() {
				comp.AddToolCall("c1", "rf", longArgs, "")
			},
			// 100 chars of args + "..." = 103 chars + "⚙ rf " = 108 chars
			// at width 21 this wraps across several lines
			Expected: "⚙ rf                 \nxxxxxxxxxxxxxxxxxxxx \nxxxxxxxxxxxxxxxxxxxx \nxxxxxxxxxxxxxxxxxxxx \nxxxxxxxxxxxxxxxxxxxx \nxxxxxxxxxxxxxxxxxxxx \n...                  \n ┌──────────────┐    \n │              │    \n └──────────────┘    \n                     ",
		},
	}
	comptest.TestComponent(t, comp, w, tests)
}

func TestComponentParallelToolCalls(t *testing.T) {
	comp := NewComponent(ComponentConfig{})
	comp.Resize(20, 10)

	w := term.NewStringWriter(21, 11)
	tests := []comptest.TestCase{
		{
			Action: func() {
				comp.AddToolCall("c1", "read_file", `{"path":"a"}`, "")
				comp.AddToolCall("c2", "read_file", `{"path":"b"}`, "")
			},
			Expected: "⚙ read_file          \npath=a               \n⚙ read_file          \npath=b               \n                     \n                     \n                     \n ┌──────────────┐    \n │              │    \n └──────────────┘    \n                     ",
		},
		{
			// Tools stay in original order (in-place update).
			Action: func() {
				comp.CompleteToolCall("c1", "read_file", `{"path":"a"}`, "", "data_a", false)
			},
			Expected: "✓ read_file          \npath=a               \ndata_a               \n⚙ read_file          \npath=b               \n                     \n                     \n ┌──────────────┐    \n │              │    \n └──────────────┘    \n                     ",
		},
		{
			Action: func() {
				comp.CompleteToolCall("c2", "read_file", `{"path":"b"}`, "", "data_b", false)
			},
			Expected: "✓ read_file          \npath=a               \ndata_a               \n✓ read_file          \npath=b               \ndata_b               \n                     \n ┌──────────────┐    \n │              │    \n └──────────────┘    \n                     ",
		},
	}
	comptest.TestComponent(t, comp, w, tests)
}

func TestComponentReasoningChunks(t *testing.T) {
	comp := NewComponent(ComponentConfig{})
	comp.Resize(20, 10)

	w := term.NewStringWriter(21, 11)
	tests := []comptest.TestCase{
		{
			Action: func() {
				comp.AddReasoningChunk("Think")
			},
			Expected: "Think                \n" +
				"                     \n" +
				"ctrl-o to collapse   \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				" ┌──────────────┐    \n" +
				" │              │    \n" +
				" └──────────────┘    \n" +
				"                     ",
		},
		{
			Action: func() {
				comp.AddReasoningChunk("ing hard")
			},
			Expected: "Thinking hard        \n" +
				"                     \n" +
				"ctrl-o to collapse   \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				" ┌──────────────┐    \n" +
				" │              │    \n" +
				" └──────────────┘    \n" +
				"                     ",
		},
	}
	comptest.TestComponent(t, comp, w, tests)
}

// TestComponentReasoningMarkdown verifies reasoning text is rendered as
// markdown: bold markers are stripped once closed rather than shown
// literally.
func TestComponentReasoningMarkdown(t *testing.T) {
	comp := NewComponent(ComponentConfig{})
	comp.Resize(20, 10)

	w := term.NewStringWriter(21, 11)
	tests := []comptest.TestCase{
		{
			// Partial bold — markers not yet closed.
			Action: func() {
				comp.AddReasoningChunk("Plan **st")
			},
			Expected: "Plan **st            \n" +
				"                     \n" +
				"ctrl-o to collapse   \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				" ┌──────────────┐    \n" +
				" │              │    \n" +
				" └──────────────┘    \n" +
				"                     ",
		},
		{
			// Complete bold — markers stripped.
			Action: func() {
				comp.AddReasoningChunk("eps** now")
			},
			Expected: "Plan steps now       \n" +
				"                     \n" +
				"ctrl-o to collapse   \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				" ┌──────────────┐    \n" +
				" │              │    \n" +
				" └──────────────┘    \n" +
				"                     ",
		},
	}
	comptest.TestComponent(t, comp, w, tests)
}

func TestComponentReasoningThenText(t *testing.T) {
	comp := NewComponent(ComponentConfig{})
	comp.Resize(20, 10)

	w := term.NewStringWriter(21, 11)
	tests := []comptest.TestCase{
		{
			Action: func() {
				comp.AddReasoningChunk("Let me think...")
			},
			Expected: "Let me think...      \n" +
				"                     \n" +
				"ctrl-o to collapse   \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				" ┌──────────────┐    \n" +
				" │              │    \n" +
				" └──────────────┘    \n" +
				"                     ",
		},
		{
			Action: func() {
				// Text arrival breaks reasoning; creates a new entry.
				comp.AddReceiveMessageChunk("Hello!")
			},
			Expected: "Let me think...      \n" +
				"                     \n" +
				"ctrl-o to collapse   \n" +
				"Hello!               \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				" ┌──────────────┐    \n" +
				" │              │    \n" +
				" └──────────────┘    \n" +
				"                     ",
		},
		{
			Action: func() {
				// Subsequent text chunks append to text, not reasoning.
				comp.AddReceiveMessageChunk(" World")
			},
			Expected: "Let me think...      \n" +
				"                     \n" +
				"ctrl-o to collapse   \n" +
				"Hello! World         \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				" ┌──────────────┐    \n" +
				" │              │    \n" +
				" └──────────────┘    \n" +
				"                     ",
		},
	}
	comptest.TestComponent(t, comp, w, tests)
}

// TestComponentReasoningToolCallReasoning exercises the full agent loop
// pattern for reasoning models: reasoning → tool calls → more reasoning → text.
func TestComponentReasoningToolCallReasoning(t *testing.T) {
	comp := NewComponent(ComponentConfig{})
	comp.Resize(20, 10)

	w := term.NewStringWriter(21, 11)
	tests := []comptest.TestCase{
		{
			// Iteration 1: reasoning before tool call
			Action: func() {
				comp.AddReasoningChunk("I should read")
				comp.AddReasoningChunk(" the file")
			},
			Expected: "I should read the    \n" +
				"file                 \n" +
				"                     \n" +
				"ctrl-o to collapse   \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				" ┌──────────────┐    \n" +
				" │              │    \n" +
				" └──────────────┘    \n" +
				"                     ",
		},
		{
			// Tool call breaks reasoning
			Action: func() {
				comp.AddToolCall("c1", "read_file", "", "")
			},
			Expected: "I should read the    \n" +
				"file                 \n" +
				"                     \n" +
				"ctrl-o to collapse   \n" +
				"⚙ read_file          \n" +
				"                     \n" +
				"                     \n" +
				" ┌──────────────┐    \n" +
				" │              │    \n" +
				" └──────────────┘    \n" +
				"                     ",
		},
		{
			Action: func() {
				comp.CompleteToolCall("c1", "read_file", "", "", "data", false)
			},
			Expected: "I should read the    \n" +
				"file                 \n" +
				"                     \n" +
				"ctrl-o to collapse   \n" +
				"✓ read_file          \n" +
				"data                 \n" +
				"                     \n" +
				" ┌──────────────┐    \n" +
				" │              │    \n" +
				" └──────────────┘    \n" +
				"                     ",
		},
		{
			// Iteration 2: new reasoning block after tool result
			Action: func() {
				comp.AddReasoningChunk("Now I know")
			},
			Expected: "file                 \n" +
				"                     \n" +
				"✓ read_file          \n" +
				"data                 \n" +
				"Now I know           \n" +
				"                     \n" +
				"ctrl-o to collapse   \n" +
				" ┌──────────────┐    \n" +
				" │              │    \n" +
				" └──────────────┘    \n" +
				"                     ",
		},
		{
			// Text breaks the second reasoning block
			Action: func() {
				comp.AddReceiveMessageChunk("The answer is 42.")
			},
			Expected: "✓ read_file          \n" +
				"data                 \n" +
				"Now I know           \n" +
				"                     \n" +
				"ctrl-o to collapse   \n" +
				"The answer is 42.    \n" +
				"                     \n" +
				" ┌──────────────┐    \n" +
				" │              │    \n" +
				" └──────────────┘    \n" +
				"                     ",
		},
	}
	comptest.TestComponent(t, comp, w, tests)
}

func TestComponentReasoningOnlyThenBreak(t *testing.T) {
	comp := NewComponent(ComponentConfig{})
	comp.Resize(20, 10)

	w := term.NewStringWriter(21, 11)
	tests := []comptest.TestCase{
		{
			Action: func() {
				comp.AddReasoningChunk("Deep thought")
				// Break without any text (reasoning-only response + EventDone)
				comp.AddReceiveMessageBreak()
			},
			Expected: "Deep thought         \n" +
				"                     \n" +
				"ctrl-o to collapse   \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				" ┌──────────────┐    \n" +
				" │              │    \n" +
				" └──────────────┘    \n" +
				"                     ",
		},
		{
			// New reasoning after break starts fresh
			Action: func() {
				comp.AddReasoningChunk("New thought")
			},
			Expected: "Deep thought         \n" +
				"                     \n" +
				"New thought          \n" +
				"                     \n" +
				"ctrl-o to collapse   \n" +
				"                     \n" +
				"                     \n" +
				" ┌──────────────┐    \n" +
				" │              │    \n" +
				" └──────────────┘    \n" +
				"                     ",
		},
	}
	comptest.TestComponent(t, comp, w, tests)
}

func TestComponentResetDuringReasoning(t *testing.T) {
	comp := NewComponent(ComponentConfig{})
	comp.Resize(20, 10)

	w := term.NewStringWriter(21, 11)
	tests := []comptest.TestCase{
		{
			Action: func() {
				comp.AddReasoningChunk("thinking")
			},
			Expected: "thinking             \n" +
				"                     \n" +
				"ctrl-o to collapse   \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				" ┌──────────────┐    \n" +
				" │              │    \n" +
				" └──────────────┘    \n" +
				"                     ",
		},
		{
			Action: func() {
				comp.Reset()
			},
			Expected: "                     \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				" ┌──────────────┐    \n" +
				" │              │    \n" +
				" └──────────────┘    \n" +
				"                     ",
		},
		{
			// Reasoning after reset is independent
			Action: func() {
				comp.AddReasoningChunk("fresh")
			},
			Expected: "fresh                \n" +
				"                     \n" +
				"ctrl-o to collapse   \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				" ┌──────────────┐    \n" +
				" │              │    \n" +
				" └──────────────┘    \n" +
				"                     ",
		},
	}
	comptest.TestComponent(t, comp, w, tests)
}

func TestComponentReasoningToggle(t *testing.T) {
	comp := NewComponent(ComponentConfig{})
	comp.Resize(25, 10)

	w := term.NewStringWriter(26, 11)
	tests := []comptest.TestCase{
		{
			Action: func() {
				comp.AddReasoningChunk("Thinking hard")
			},
			Expected: "Thinking hard             \n" +
				"                          \n" +
				"ctrl-o to collapse        \n" +
				"                          \n" +
				"                          \n" +
				"                          \n" +
				"                          \n" +
				"  ┌──────────────────┐    \n" +
				"  │                  │    \n" +
				"  └──────────────────┘    \n" +
				"                          ",
		},
		{
			// Finalize reasoning, then add text
			Action: func() {
				comp.AddReceiveMessageChunk("Hello!")
			},
			Expected: "Thinking hard             \n" +
				"                          \n" +
				"ctrl-o to collapse        \n" +
				"Hello!                    \n" +
				"                          \n" +
				"                          \n" +
				"                          \n" +
				"  ┌──────────────────┐    \n" +
				"  │                  │    \n" +
				"  └──────────────────┘    \n" +
				"                          ",
		},
		{
			// Toggle to collapsed: reasoning hidden
			Action: func() {
				comp.ToggleContracted()
			},
			Expected: "Hello!                    \n" +
				"                          \n" +
				"ctrl-o to expand          \n" +
				"                          \n" +
				"                          \n" +
				"                          \n" +
				"                          \n" +
				"  ┌──────────────────┐    \n" +
				"  │                  │    \n" +
				"  └──────────────────┘    \n" +
				"                          ",
		},
		{
			// Toggle to expanded: reasoning reappears
			Action: func() {
				comp.ToggleContracted()
			},
			Expected: "Thinking hard             \n" +
				"                          \n" +
				"Hello!                    \n" +
				"                          \n" +
				"ctrl-o to collapse        \n" +
				"                          \n" +
				"                          \n" +
				"  ┌──────────────────┐    \n" +
				"  │                  │    \n" +
				"  └──────────────────┘    \n" +
				"                          ",
		},
	}
	comptest.TestComponent(t, comp, w, tests)
}

func TestComponentReasoningToggleDuringStream(t *testing.T) {
	comp := NewComponent(ComponentConfig{})
	comp.Resize(25, 10)

	w := term.NewStringWriter(26, 11)
	tests := []comptest.TestCase{
		{
			Action: func() {
				comp.AddReasoningChunk("Think")
			},
			Expected: "Think                     \n" +
				"                          \n" +
				"ctrl-o to collapse        \n" +
				"                          \n" +
				"                          \n" +
				"                          \n" +
				"                          \n" +
				"  ┌──────────────────┐    \n" +
				"  │                  │    \n" +
				"  └──────────────────┘    \n" +
				"                          ",
		},
		{
			// Toggle hidden mid-stream
			Action: func() {
				comp.ToggleContracted()
			},
			Expected: "ctrl-o to expand          \n" +
				"                          \n" +
				"                          \n" +
				"                          \n" +
				"                          \n" +
				"                          \n" +
				"                          \n" +
				"  ┌──────────────────┐    \n" +
				"  │                  │    \n" +
				"  └──────────────────┘    \n" +
				"                          ",
		},
		{
			// Continue streaming while hidden
			Action: func() {
				comp.AddReasoningChunk("ing more")
			},
			Expected: "ctrl-o to expand          \n" +
				"                          \n" +
				"                          \n" +
				"                          \n" +
				"                          \n" +
				"                          \n" +
				"                          \n" +
				"  ┌──────────────────┐    \n" +
				"  │                  │    \n" +
				"  └──────────────────┘    \n" +
				"                          ",
		},
		{
			// Toggle to expanded: full accumulated text appears
			Action: func() {
				comp.ToggleContracted()
			},
			Expected: "Thinking more             \n" +
				"                          \n" +
				"ctrl-o to collapse        \n" +
				"                          \n" +
				"                          \n" +
				"                          \n" +
				"                          \n" +
				"  ┌──────────────────┐    \n" +
				"  │                  │    \n" +
				"  └──────────────────┘    \n" +
				"                          ",
		},
	}
	comptest.TestComponent(t, comp, w, tests)
}

func TestComponentReasoningToggleWithMixedContent(t *testing.T) {
	comp := NewComponent(ComponentConfig{})
	comp.Resize(25, 10)

	w := term.NewStringWriter(26, 11)
	tests := []comptest.TestCase{
		{
			// Reasoning → text → tool call → reasoning → text
			Action: func() {
				comp.AddReasoningChunk("First thought")
				comp.AddReceiveMessageChunk("Response 1")
				comp.AddReceiveMessageBreak()
				comp.AddToolCall("c1", "read_file", "", "")
				comp.CompleteToolCall("c1", "read_file", "", "", "ok", false)
				comp.AddReasoningChunk("Second thought")
				comp.AddReceiveMessageChunk("Response 2")
				comp.AddReceiveMessageBreak()
			},
			Expected: "✓ read_file               \n" +
				"ok                        \n" +
				"Second thought            \n" +
				"                          \n" +
				"ctrl-o to collapse        \n" +
				"Response 2                \n" +
				"                          \n" +
				"  ┌──────────────────┐    \n" +
				"  │                  │    \n" +
				"  └──────────────────┘    \n" +
				"                          ",
		},
		{
			// Toggle to collapsed: reasoning hidden, tools collapsed
			Action: func() {
				comp.ToggleContracted()
			},
			Expected: "                          \n" +
				"└─ ✓ read_file            \n" +
				"Press <ctrl-o> to expand  \n" +
				"                          \n" +
				"Response 2                \n" +
				"                          \n" +
				"ctrl-o to expand          \n" +
				"  ┌──────────────────┐    \n" +
				"  │                  │    \n" +
				"  └──────────────────┘    \n" +
				"                          ",
		},
		{
			// Toggle to expanded
			Action: func() {
				comp.ToggleContracted()
			},
			Expected: "✓ read_file               \n" +
				"ok                        \n" +
				"Second thought            \n" +
				"                          \n" +
				"Response 2                \n" +
				"                          \n" +
				"ctrl-o to collapse        \n" +
				"  ┌──────────────────┐    \n" +
				"  │                  │    \n" +
				"  └──────────────────┘    \n" +
				"                          ",
		},
	}
	comptest.TestComponent(t, comp, w, tests)
}

func TestComponentReasoningAnnotationAppearsOnFirstChunk(t *testing.T) {
	comp := NewComponent(ComponentConfig{})
	comp.Resize(25, 10)

	w := term.NewStringWriter(26, 11)
	tests := []comptest.TestCase{
		{
			// Before any reasoning: no annotation
			Action: func() {
				comp.AddSendMessage("Hello")
			},
			Expected: "Hello                     \n" +
				"                          \n" +
				"                          \n" +
				"                          \n" +
				"                          \n" +
				"                          \n" +
				"                          \n" +
				"  ┌──────────────────┐    \n" +
				"  │                  │    \n" +
				"  └──────────────────┘    \n" +
				"                          ",
		},
		{
			// First reasoning chunk: annotation appears
			Action: func() {
				comp.AddReasoningChunk("Hmm")
			},
			Expected: "Hello                     \n" +
				"Hmm                       \n" +
				"                          \n" +
				"ctrl-o to collapse        \n" +
				"                          \n" +
				"                          \n" +
				"                          \n" +
				"  ┌──────────────────┐    \n" +
				"  │                  │    \n" +
				"  └──────────────────┘    \n" +
				"                          ",
		},
	}
	comptest.TestComponent(t, comp, w, tests)
}

func TestComponentMarkdownStreaming(t *testing.T) {
	comp := NewComponent(ComponentConfig{})
	comp.Resize(20, 10)

	w := term.NewStringWriter(21, 11)
	tests := []comptest.TestCase{
		{
			// Partial bold — markers not yet closed.
			Action: func() {
				comp.AddReceiveMessageChunk("Hello **bo")
			},
			Expected: "Hello **bo           \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				" ┌──────────────┐    \n" +
				" │              │    \n" +
				" └──────────────┘    \n" +
				"                     ",
		},
		{
			// Complete bold — markers stripped.
			Action: func() {
				comp.AddReceiveMessageChunk("ld** world")
			},
			Expected: "Hello bold world     \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				" ┌──────────────┐    \n" +
				" │              │    \n" +
				" └──────────────┘    \n" +
				"                     ",
		},
	}
	comptest.TestComponent(t, comp, w, tests)
}

func TestComponentMarkdownCodeBlock(t *testing.T) {
	comp := NewComponent(ComponentConfig{})
	comp.Resize(20, 10)

	w := term.NewStringWriter(21, 11)
	tests := []comptest.TestCase{
		{
			Action: func() {
				comp.AddReceiveMessage("```\nfoo\n```")
			},
			Expected: "foo                  \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				" ┌──────────────┐    \n" +
				" │              │    \n" +
				" └──────────────┘    \n" +
				"                     ",
		},
	}
	comptest.TestComponent(t, comp, w, tests)
}

func TestComponentMarkdownMultiParagraph(t *testing.T) {
	comp := NewComponent(ComponentConfig{})
	comp.Resize(20, 10)

	w := term.NewStringWriter(21, 11)
	tests := []comptest.TestCase{
		{
			Action: func() {
				comp.AddReceiveMessage("Para 1\n\nPara 2")
			},
			Expected: "Para 1               \n" +
				"                     \n" +
				"Para 2               \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				" ┌──────────────┐    \n" +
				" │              │    \n" +
				" └──────────────┘    \n" +
				"                     ",
		},
	}
	comptest.TestComponent(t, comp, w, tests)
}

func TestComponentMarkdownCustomConfig(t *testing.T) {
	cfg := markdown.DefaultConfig()
	cfg.ParagraphSpacing = 0
	cfg.HeaderPrefix = false
	comp := NewComponent(ComponentConfig{
		MarkdownConfig: &cfg,
	})
	comp.Resize(20, 10)

	w := term.NewStringWriter(21, 11)
	tests := []comptest.TestCase{
		{
			// With ParagraphSpacing: 0, no trailing blank line.
			Action: func() {
				comp.AddReceiveMessage("No spacing")
			},
			Expected: "No spacing           \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				" ┌──────────────┐    \n" +
				" │              │    \n" +
				" └──────────────┘    \n" +
				"                     ",
		},
	}
	comptest.TestComponent(t, comp, w, tests)
}

func TestComponentErrorMessage(t *testing.T) {
	comp := NewComponent(ComponentConfig{})
	comp.Resize(20, 10)

	w := term.NewStringWriter(21, 11)
	tests := []comptest.TestCase{
		{
			Action: func() {
				comp.AddErrorMessage("something broke")
			},
			Expected: "! something broke    \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				" ┌──────────────┐    \n" +
				" │              │    \n" +
				" └──────────────┘    \n" +
				"                     ",
		},
	}
	comptest.TestComponent(t, comp, w, tests)
}

// TestComponentToolCallOrderings exercises every combination of tool calls,
// child agent tool calls, standard messages, completion orderings, and
// message breaks to verify the final rendering is correct in each case.
func TestComponentToolCallOrderings(t *testing.T) {
	const (
		W = 30
		H = 20
	)
	mkW := func() *term.StringWriter { return term.NewStringWriter(W+1, H+1) }

	t.Run("single tool running", func(t *testing.T) {
		comp := NewComponent(ComponentConfig{})
		comp.Resize(W, H)
		w := mkW()
		tests := []comptest.TestCase{
			{
				Action: func() {
					comp.AddToolCall("t1", "read", "{}", "a.go")
				},
				Expected: "⚙ read a.go                    \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
		}
		comptest.TestComponent(t, comp, w, tests)
	})

	t.Run("single tool complete", func(t *testing.T) {
		comp := NewComponent(ComponentConfig{})
		comp.Resize(W, H)
		w := mkW()
		tests := []comptest.TestCase{
			{
				Action: func() {
					comp.AddToolCall("t1", "read", "{}", "a.go")
					comp.CompleteToolCall("t1", "read", "{}", "a.go", "data", false)
				},
				Expected: "✓ read a.go                    \n" +
					"data                           \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
		}
		comptest.TestComponent(t, comp, w, tests)
	})

	t.Run("single tool error", func(t *testing.T) {
		comp := NewComponent(ComponentConfig{})
		comp.Resize(W, H)
		w := mkW()
		tests := []comptest.TestCase{
			{
				Action: func() {
					comp.AddToolCall("t1", "read", "{}", "a.go")
					comp.CompleteToolCall("t1", "read", "{}", "a.go", "fail", true)
				},
				Expected: "✗ read a.go                    \n" +
					"fail                           \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
		}
		comptest.TestComponent(t, comp, w, tests)
	})

	t.Run("two parallel tools running", func(t *testing.T) {
		comp := NewComponent(ComponentConfig{})
		comp.Resize(W, H)
		w := mkW()
		tests := []comptest.TestCase{
			{
				Action: func() {
					comp.AddToolCall("t1", "read", "{}", "a")
					comp.AddToolCall("t2", "write", "{}", "b")
				},
				Expected: "⚙ read a                       \n" +
					"⚙ write b                      \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
		}
		comptest.TestComponent(t, comp, w, tests)
	})

	t.Run("two parallel complete in order", func(t *testing.T) {
		comp := NewComponent(ComponentConfig{})
		comp.Resize(W, H)
		w := mkW()
		tests := []comptest.TestCase{
			{
				Action: func() {
					comp.AddToolCall("t1", "read", "{}", "a")
					comp.AddToolCall("t2", "write", "{}", "b")
				},
				Expected: "⚙ read a                       \n" +
					"⚙ write b                      \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
			{
				Action: func() {
					comp.CompleteToolCall("t1", "read", "{}", "a", "r1", false)
				},
				Expected: "✓ read a                       \n" +
					"r1                             \n" +
					"⚙ write b                      \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
			{
				Action: func() {
					comp.CompleteToolCall("t2", "write", "{}", "b", "r2", false)
				},
				Expected: "✓ read a                       \n" +
					"r1                             \n" +
					"✓ write b                      \n" +
					"r2                             \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
		}
		comptest.TestComponent(t, comp, w, tests)
	})

	t.Run("two parallel complete in reverse", func(t *testing.T) {
		comp := NewComponent(ComponentConfig{})
		comp.Resize(W, H)
		w := mkW()
		tests := []comptest.TestCase{
			{
				Action: func() {
					comp.AddToolCall("t1", "read", "{}", "a")
					comp.AddToolCall("t2", "write", "{}", "b")
					comp.CompleteToolCall("t2", "write", "{}", "b", "ok", false)
				},
				Expected: "⚙ read a                       \n" +
					"✓ write b                      \n" +
					"ok                             \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
			{
				Action: func() {
					comp.CompleteToolCall("t1", "read", "{}", "a", "data", false)
				},
				Expected: "✓ read a                       \n" +
					"data                           \n" +
					"✓ write b                      \n" +
					"ok                             \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
		}
		comptest.TestComponent(t, comp, w, tests)
	})

	t.Run("three parallel complete in reverse", func(t *testing.T) {
		comp := NewComponent(ComponentConfig{})
		comp.Resize(W, H)
		w := mkW()
		tests := []comptest.TestCase{
			{
				Action: func() {
					comp.AddToolCall("t1", "read", "{}", "a")
					comp.AddToolCall("t2", "write", "{}", "b")
					comp.AddToolCall("t3", "search", "{}", "c")
					comp.CompleteToolCall("t3", "search", "{}", "c", "r3", false)
					comp.CompleteToolCall("t1", "read", "{}", "a", "r1", false)
					comp.CompleteToolCall("t2", "write", "{}", "b", "r2", false)
				},
				Expected: "✓ read a                       \n" +
					"r1                             \n" +
					"✓ write b                      \n" +
					"r2                             \n" +
					"✓ search c                     \n" +
					"r3                             \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
		}
		comptest.TestComponent(t, comp, w, tests)
	})

	t.Run("three parallel complete middle first", func(t *testing.T) {
		comp := NewComponent(ComponentConfig{})
		comp.Resize(W, H)
		w := mkW()
		tests := []comptest.TestCase{
			{
				Action: func() {
					comp.AddToolCall("t1", "read", "{}", "a")
					comp.AddToolCall("t2", "write", "{}", "b")
					comp.AddToolCall("t3", "search", "{}", "c")
					comp.CompleteToolCall("t2", "write", "{}", "b", "r2", false)
				},
				Expected: "⚙ read a                       \n" +
					"✓ write b                      \n" +
					"r2                             \n" +
					"⚙ search c                     \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
		}
		comptest.TestComponent(t, comp, w, tests)
	})

	t.Run("parent with 1 child both running", func(t *testing.T) {
		comp := NewComponent(ComponentConfig{})
		comp.Resize(W, H)
		w := mkW()
		tests := []comptest.TestCase{
			{
				Action: func() {
					comp.AddToolCall("p1", "agent", "{}", "task")
					comp.AddChildToolCall("p1", "c1", "read", "{}", "f")
				},
				Expected: "⚙ agent task                   \n" +
					"⚙ read f                       \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
		}
		comptest.TestComponent(t, comp, w, tests)
	})

	t.Run("parent with 1 child complete child then parent", func(t *testing.T) {
		comp := NewComponent(ComponentConfig{})
		comp.Resize(W, H)
		w := mkW()
		tests := []comptest.TestCase{
			{
				Action: func() {
					comp.AddToolCall("p1", "agent", "{}", "task")
					comp.AddChildToolCall("p1", "c1", "read", "{}", "f")
				},
				Expected: "⚙ agent task                   \n" +
					"⚙ read f                       \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
			{
				Action: func() {
					comp.CompleteChildToolCall("p1", "c1", "read", "{}", "f", "ok", false)
				},
				Expected: "⚙ agent task                   \n" +
					"✓ read f                       \n" +
					"ok                             \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
			{
				Action: func() {
					comp.CompleteToolCall("p1", "agent", "{}", "task", "done", false)
				},
				Expected: "✓ agent task                   \n" +
					"done                           \n" +
					"✓ read f                       \n" +
					"ok                             \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
		}
		comptest.TestComponent(t, comp, w, tests)
	})

	t.Run("parent with 2 children all running", func(t *testing.T) {
		comp := NewComponent(ComponentConfig{})
		comp.Resize(W, H)
		w := mkW()
		tests := []comptest.TestCase{
			{
				Action: func() {
					comp.AddToolCall("p1", "agent", "{}", "task")
					comp.AddChildToolCall("p1", "c1", "read", "{}", "a")
					comp.AddChildToolCall("p1", "c2", "write", "{}", "b")
				},
				Expected: "⚙ agent task                   \n" +
					"⚙ read a                       \n" +
					"⚙ write b                      \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
		}
		comptest.TestComponent(t, comp, w, tests)
	})

	t.Run("parent with 2 children complete second first", func(t *testing.T) {
		comp := NewComponent(ComponentConfig{})
		comp.Resize(W, H)
		w := mkW()
		tests := []comptest.TestCase{
			{
				Action: func() {
					comp.AddToolCall("p1", "agent", "{}", "task")
					comp.AddChildToolCall("p1", "c1", "read", "{}", "a")
					comp.AddChildToolCall("p1", "c2", "write", "{}", "b")
					comp.CompleteChildToolCall("p1", "c2", "write", "{}", "b", "r2", false)
				},
				Expected: "⚙ agent task                   \n" +
					"⚙ read a                       \n" +
					"✓ write b                      \n" +
					"r2                             \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
			{
				Action: func() {
					comp.CompleteChildToolCall("p1", "c1", "read", "{}", "a", "r1", false)
				},
				Expected: "⚙ agent task                   \n" +
					"✓ read a                       \n" +
					"r1                             \n" +
					"✓ write b                      \n" +
					"r2                             \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
			{
				Action: func() {
					comp.CompleteToolCall("p1", "agent", "{}", "task", "done", false)
				},
				Expected: "✓ agent task                   \n" +
					"done                           \n" +
					"✓ read a                       \n" +
					"r1                             \n" +
					"✓ write b                      \n" +
					"r2                             \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
		}
		comptest.TestComponent(t, comp, w, tests)
	})

	t.Run("parent with 2 children complete first first", func(t *testing.T) {
		comp := NewComponent(ComponentConfig{})
		comp.Resize(W, H)
		w := mkW()
		tests := []comptest.TestCase{
			{
				Action: func() {
					comp.AddToolCall("p1", "agent", "{}", "task")
					comp.AddChildToolCall("p1", "c1", "read", "{}", "a")
					comp.AddChildToolCall("p1", "c2", "write", "{}", "b")
					comp.CompleteChildToolCall("p1", "c1", "read", "{}", "a", "r1", false)
					comp.CompleteChildToolCall("p1", "c2", "write", "{}", "b", "r2", false)
					comp.CompleteToolCall("p1", "agent", "{}", "task", "done", false)
				},
				Expected: "✓ agent task                   \n" +
					"done                           \n" +
					"✓ read a                       \n" +
					"r1                             \n" +
					"✓ write b                      \n" +
					"r2                             \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
		}
		comptest.TestComponent(t, comp, w, tests)
	})

	t.Run("parent with 3 children all running", func(t *testing.T) {
		comp := NewComponent(ComponentConfig{})
		comp.Resize(W, H)
		w := mkW()
		tests := []comptest.TestCase{
			{
				Action: func() {
					comp.AddToolCall("p1", "agent", "{}", "task")
					comp.AddChildToolCall("p1", "c1", "read", "{}", "a")
					comp.AddChildToolCall("p1", "c2", "write", "{}", "b")
					comp.AddChildToolCall("p1", "c3", "search", "{}", "c")
				},
				Expected: "⚙ agent task                   \n" +
					"⚙ read a                       \n" +
					"⚙ write b                      \n" +
					"⚙ search c                     \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
		}
		comptest.TestComponent(t, comp, w, tests)
	})

	t.Run("parent with 3 children complete all reverse order", func(t *testing.T) {
		comp := NewComponent(ComponentConfig{})
		comp.Resize(W, H)
		w := mkW()
		tests := []comptest.TestCase{
			{
				Action: func() {
					comp.AddToolCall("p1", "agent", "{}", "task")
					comp.AddChildToolCall("p1", "c1", "read", "{}", "a")
					comp.AddChildToolCall("p1", "c2", "write", "{}", "b")
					comp.AddChildToolCall("p1", "c3", "search", "{}", "c")
					comp.CompleteChildToolCall("p1", "c3", "search", "{}", "c", "r3", false)
					comp.CompleteChildToolCall("p1", "c2", "write", "{}", "b", "r2", false)
					comp.CompleteChildToolCall("p1", "c1", "read", "{}", "a", "r1", false)
					comp.CompleteToolCall("p1", "agent", "{}", "task", "done", false)
				},
				Expected: "✓ agent task                   \n" +
					"done                           \n" +
					"✓ read a                       \n" +
					"r1                             \n" +
					"✓ write b                      \n" +
					"r2                             \n" +
					"✓ search c                     \n" +
					"r3                             \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
		}
		comptest.TestComponent(t, comp, w, tests)
	})

	t.Run("2 parents each 1 child all running", func(t *testing.T) {
		comp := NewComponent(ComponentConfig{})
		comp.Resize(W, H)
		w := mkW()
		tests := []comptest.TestCase{
			{
				Action: func() {
					comp.AddToolCall("p1", "agent", "{}", "t1")
					comp.AddToolCall("p2", "agent", "{}", "t2")
					comp.AddChildToolCall("p1", "c1", "read", "{}", "a")
					comp.AddChildToolCall("p2", "c2", "write", "{}", "b")
				},
				Expected: "⚙ agent t1                     \n" +
					"⚙ read a                       \n" +
					"⚙ agent t2                     \n" +
					"⚙ write b                      \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
		}
		comptest.TestComponent(t, comp, w, tests)
	})

	t.Run("2 parents p1=1child p2=2children", func(t *testing.T) {
		comp := NewComponent(ComponentConfig{})
		comp.Resize(W, H)
		w := mkW()
		tests := []comptest.TestCase{
			{
				Action: func() {
					comp.AddToolCall("p1", "agent", "{}", "t1")
					comp.AddToolCall("p2", "agent", "{}", "t2")
					comp.AddChildToolCall("p1", "c1", "read", "{}", "a")
					comp.AddChildToolCall("p2", "c2", "write", "{}", "b")
					comp.AddChildToolCall("p2", "c3", "search", "{}", "c")
				},
				Expected: "⚙ agent t1                     \n" +
					"⚙ read a                       \n" +
					"⚙ agent t2                     \n" +
					"⚙ write b                      \n" +
					"⚙ search c                     \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
		}
		comptest.TestComponent(t, comp, w, tests)
	})

	t.Run("2 parents each 2 children all complete", func(t *testing.T) {
		comp := NewComponent(ComponentConfig{})
		comp.Resize(W, H)
		w := mkW()
		tests := []comptest.TestCase{
			{
				Action: func() {
					comp.AddToolCall("p1", "agent", "{}", "t1")
					comp.AddToolCall("p2", "agent", "{}", "t2")
					comp.AddChildToolCall("p1", "c1", "read", "{}", "a")
					comp.AddChildToolCall("p1", "c2", "write", "{}", "b")
					comp.AddChildToolCall("p2", "c3", "read", "{}", "c")
					comp.AddChildToolCall("p2", "c4", "write", "{}", "d")
					comp.CompleteChildToolCall("p1", "c1", "read", "{}", "a", "r1", false)
					comp.CompleteChildToolCall("p1", "c2", "write", "{}", "b", "r2", false)
					comp.CompleteChildToolCall("p2", "c3", "read", "{}", "c", "r3", false)
					comp.CompleteChildToolCall("p2", "c4", "write", "{}", "d", "r4", false)
					comp.CompleteToolCall("p1", "agent", "{}", "t1", "d1", false)
					comp.CompleteToolCall("p2", "agent", "{}", "t2", "d2", false)
				},
				Expected: "✓ agent t1                     \n" +
					"d1                             \n" +
					"✓ read a                       \n" +
					"r1                             \n" +
					"✓ write b                      \n" +
					"r2                             \n" +
					"✓ agent t2                     \n" +
					"d2                             \n" +
					"✓ read c                       \n" +
					"r3                             \n" +
					"✓ write d                      \n" +
					"r4                             \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
		}
		comptest.TestComponent(t, comp, w, tests)
	})

	t.Run("2 parents each 3 children all running", func(t *testing.T) {
		comp := NewComponent(ComponentConfig{})
		comp.Resize(W, H)
		w := mkW()
		tests := []comptest.TestCase{
			{
				Action: func() {
					comp.AddToolCall("p1", "agent", "{}", "t1")
					comp.AddToolCall("p2", "agent", "{}", "t2")
					comp.AddChildToolCall("p1", "c1", "r", "{}", "a")
					comp.AddChildToolCall("p1", "c2", "w", "{}", "b")
					comp.AddChildToolCall("p1", "c3", "s", "{}", "c")
					comp.AddChildToolCall("p2", "c4", "r", "{}", "d")
					comp.AddChildToolCall("p2", "c5", "w", "{}", "e")
					comp.AddChildToolCall("p2", "c6", "s", "{}", "f")
				},
				Expected: "⚙ agent t1                     \n" +
					"⚙ r a                          \n" +
					"⚙ w b                          \n" +
					"⚙ s c                          \n" +
					"⚙ agent t2                     \n" +
					"⚙ r d                          \n" +
					"⚙ w e                          \n" +
					"⚙ s f                          \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
		}
		comptest.TestComponent(t, comp, w, tests)
	})

	t.Run("text before tool call", func(t *testing.T) {
		comp := NewComponent(ComponentConfig{})
		comp.Resize(W, H)
		w := mkW()
		tests := []comptest.TestCase{
			{
				Action: func() {
					comp.AddReceiveMessageChunk("thinking")
					comp.AddToolCall("t1", "read", "{}", "a")
				},
				Expected: "thinking                       \n" +
					"                               \n" +
					"⚙ read a                       \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
		}
		comptest.TestComponent(t, comp, w, tests)
	})

	t.Run("text after tool call", func(t *testing.T) {
		comp := NewComponent(ComponentConfig{})
		comp.Resize(W, H)
		w := mkW()
		tests := []comptest.TestCase{
			{
				Action: func() {
					comp.AddReceiveMessageChunk("thinking")
					comp.AddToolCall("t1", "read", "{}", "a")
					comp.CompleteToolCall("t1", "read", "{}", "a", "data", false)
					comp.AddReceiveMessageChunk(" more")
				},
				Expected: "thinking                       \n" +
					"                               \n" +
					"✓ read a                       \n" +
					"data                           \n" +
					"more                           \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
		}
		comptest.TestComponent(t, comp, w, tests)
	})

	t.Run("send then tool then recv then send", func(t *testing.T) {
		comp := NewComponent(ComponentConfig{})
		comp.Resize(W, H)
		w := mkW()
		tests := []comptest.TestCase{
			{
				Action: func() {
					comp.AddSendMessage("question")
					comp.AddReceiveMessageChunk("thinking")
					comp.AddToolCall("p1", "agent", "{}", "job")
					comp.AddChildToolCall("p1", "c1", "read", "{}", "f")
					comp.CompleteChildToolCall("p1", "c1", "read", "{}", "f", "ok", false)
					comp.CompleteToolCall("p1", "agent", "{}", "job", "done", false)
					comp.AddReceiveMessageChunk(" reply")
					comp.AddSendMessage("thanks")
				},
				Expected: "question                       \n" +
					"thinking                       \n" +
					"                               \n" +
					"✓ agent job                    \n" +
					"done                           \n" +
					"✓ read f                       \n" +
					"ok                             \n" +
					"reply                          \n" +
					"                               \n" +
					"thanks                         \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
		}
		comptest.TestComponent(t, comp, w, tests)
	})

	t.Run("two turns separated by break", func(t *testing.T) {
		comp := NewComponent(ComponentConfig{})
		comp.Resize(W, H)
		w := mkW()
		tests := []comptest.TestCase{
			{
				Action: func() {
					comp.AddToolCall("t1", "read", "{}", "a")
					comp.CompleteToolCall("t1", "read", "{}", "a", "d1", false)
				},
				Expected: "✓ read a                       \n" +
					"d1                             \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
			{
				Action: func() {
					comp.AddReceiveMessageBreak()
					comp.AddToolCall("t2", "write", "{}", "b")
					comp.CompleteToolCall("t2", "write", "{}", "b", "d2", false)
				},
				Expected: "✓ read a                       \n" +
					"d1                             \n" +
					"✓ write b                      \n" +
					"d2                             \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
		}
		comptest.TestComponent(t, comp, w, tests)
	})

	t.Run("orphan child falls back to top-level", func(t *testing.T) {
		comp := NewComponent(ComponentConfig{})
		comp.Resize(W, H)
		w := mkW()
		tests := []comptest.TestCase{
			{
				Action: func() {
					comp.AddChildToolCall("missing", "c1", "read", "{}", "x")
				},
				Expected: "⚙ read x                       \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
		}
		comptest.TestComponent(t, comp, w, tests)
	})

	t.Run("child tool call error", func(t *testing.T) {
		comp := NewComponent(ComponentConfig{})
		comp.Resize(W, H)
		w := mkW()
		tests := []comptest.TestCase{
			{
				Action: func() {
					comp.AddToolCall("p1", "agent", "{}", "t")
					comp.AddChildToolCall("p1", "c1", "read", "{}", "f")
					comp.CompleteChildToolCall("p1", "c1", "read", "{}", "f", "not found", true)
				},
				Expected: "⚙ agent t                      \n" +
					"✗ read f                       \n" +
					"not found                      \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
		}
		comptest.TestComponent(t, comp, w, tests)
	})

	t.Run("parent error after child ok", func(t *testing.T) {
		comp := NewComponent(ComponentConfig{})
		comp.Resize(W, H)
		w := mkW()
		tests := []comptest.TestCase{
			{
				Action: func() {
					comp.AddToolCall("p1", "agent", "{}", "t")
					comp.AddChildToolCall("p1", "c1", "read", "{}", "f")
					comp.CompleteChildToolCall("p1", "c1", "read", "{}", "f", "ok", false)
					comp.CompleteToolCall("p1", "agent", "{}", "t", "fail", true)
				},
				Expected: "✗ agent t                      \n" +
					"fail                           \n" +
					"✓ read f                       \n" +
					"ok                             \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
		}
		comptest.TestComponent(t, comp, w, tests)
	})

	t.Run("complete tool without prior add (replay)", func(t *testing.T) {
		comp := NewComponent(ComponentConfig{})
		comp.Resize(W, H)
		w := mkW()
		tests := []comptest.TestCase{
			{
				Action: func() {
					comp.CompleteToolCall("t1", "read", "{}", "a", "data", false)
				},
				Expected: "✓ read a                       \n" +
					"data                           \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
		}
		comptest.TestComponent(t, comp, w, tests)
	})

	t.Run("tool with args vs tool with summary", func(t *testing.T) {
		comp := NewComponent(ComponentConfig{})
		comp.Resize(W, H)
		w := mkW()
		tests := []comptest.TestCase{
			{
				Action: func() {
					comp.AddToolCall("t1", "read", `{"path":"x"}`, "")
					comp.AddToolCall("t2", "write", `{}`, "file.go")
				},
				Expected: "⚙ read                         \n" +
					"path=x                         \n" +
					"⚙ write file.go                \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
		}
		comptest.TestComponent(t, comp, w, tests)
	})

	t.Run("error message between tool turns", func(t *testing.T) {
		comp := NewComponent(ComponentConfig{})
		comp.Resize(W, H)
		w := mkW()
		tests := []comptest.TestCase{
			{
				Action: func() {
					comp.AddToolCall("t1", "read", "{}", "a")
					comp.CompleteToolCall("t1", "read", "{}", "a", "d1", false)
					comp.AddErrorMessage("rate limit")
					comp.AddToolCall("t2", "write", "{}", "b")
				},
				Expected: "✓ read a                       \n" +
					"d1                             \n" +
					"⚙ write b                      \n" +
					"! rate limit                   \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
		}
		comptest.TestComponent(t, comp, w, tests)
	})

	t.Run("send recv tool child-error recv", func(t *testing.T) {
		comp := NewComponent(ComponentConfig{})
		comp.Resize(W, H)
		w := mkW()
		tests := []comptest.TestCase{
			{
				Action: func() {
					comp.AddSendMessage("help")
					comp.AddReceiveMessageChunk("sure")
					comp.AddToolCall("p1", "agent", "{}", "job")
					comp.AddChildToolCall("p1", "c1", "run", "{}", "cmd")
					comp.CompleteChildToolCall("p1", "c1", "run", "{}", "cmd", "exit 1", true)
					comp.CompleteToolCall("p1", "agent", "{}", "job", "fail", true)
					comp.AddReceiveMessageChunk(" sorry")
				},
				Expected: "help                           \n" +
					"sure                           \n" +
					"                               \n" +
					"✗ agent job                    \n" +
					"fail                           \n" +
					"✗ run cmd                      \n" +
					"exit 1                         \n" +
					"sorry                          \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
		}
		comptest.TestComponent(t, comp, w, tests)
	})

	t.Run("incremental child additions across steps", func(t *testing.T) {
		comp := NewComponent(ComponentConfig{})
		comp.Resize(W, H)
		w := mkW()
		tests := []comptest.TestCase{
			{
				Action: func() {
					comp.AddToolCall("p1", "agent", "{}", "task")
				},
				Expected: "⚙ agent task                   \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
			{
				Action: func() {
					comp.AddChildToolCall("p1", "c1", "read", "{}", "a")
				},
				Expected: "⚙ agent task                   \n" +
					"⚙ read a                       \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
			{
				Action: func() {
					comp.AddChildToolCall("p1", "c2", "write", "{}", "b")
				},
				Expected: "⚙ agent task                   \n" +
					"⚙ read a                       \n" +
					"⚙ write b                      \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
			{
				Action: func() {
					comp.AddChildToolCall("p1", "c3", "search", "{}", "c")
				},
				Expected: "⚙ agent task                   \n" +
					"⚙ read a                       \n" +
					"⚙ write b                      \n" +
					"⚙ search c                     \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
			{
				Action: func() {
					comp.CompleteChildToolCall("p1", "c2", "write", "{}", "b", "r2", false)
				},
				Expected: "⚙ agent task                   \n" +
					"⚙ read a                       \n" +
					"✓ write b                      \n" +
					"r2                             \n" +
					"⚙ search c                     \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
			{
				Action: func() {
					comp.CompleteChildToolCall("p1", "c1", "read", "{}", "a", "r1", false)
					comp.CompleteChildToolCall("p1", "c3", "search", "{}", "c", "r3", false)
				},
				Expected: "⚙ agent task                   \n" +
					"✓ read a                       \n" +
					"r1                             \n" +
					"✓ write b                      \n" +
					"r2                             \n" +
					"✓ search c                     \n" +
					"r3                             \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
			{
				Action: func() {
					comp.CompleteToolCall("p1", "agent", "{}", "task", "all done", false)
				},
				Expected: "✓ agent task                   \n" +
					"all done                       \n" +
					"✓ read a                       \n" +
					"r1                             \n" +
					"✓ write b                      \n" +
					"r2                             \n" +
					"✓ search c                     \n" +
					"r3                             \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
		}
		comptest.TestComponent(t, comp, w, tests)
	})

	t.Run("2 parents each 2 children incremental completion", func(t *testing.T) {
		comp := NewComponent(ComponentConfig{})
		comp.Resize(W, H)
		w := mkW()
		tests := []comptest.TestCase{
			{
				Action: func() {
					comp.AddToolCall("p1", "agent", "{}", "t1")
					comp.AddToolCall("p2", "agent", "{}", "t2")
					comp.AddChildToolCall("p1", "c1", "read", "{}", "a")
					comp.AddChildToolCall("p1", "c2", "write", "{}", "b")
					comp.AddChildToolCall("p2", "c3", "read", "{}", "c")
					comp.AddChildToolCall("p2", "c4", "write", "{}", "d")
				},
				Expected: "⚙ agent t1                     \n" +
					"⚙ read a                       \n" +
					"⚙ write b                      \n" +
					"⚙ agent t2                     \n" +
					"⚙ read c                       \n" +
					"⚙ write d                      \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
			{
				// Complete p2's children first
				Action: func() {
					comp.CompleteChildToolCall("p2", "c4", "write", "{}", "d", "r4", false)
					comp.CompleteChildToolCall("p2", "c3", "read", "{}", "c", "r3", false)
				},
				Expected: "⚙ agent t1                     \n" +
					"⚙ read a                       \n" +
					"⚙ write b                      \n" +
					"⚙ agent t2                     \n" +
					"✓ read c                       \n" +
					"r3                             \n" +
					"✓ write d                      \n" +
					"r4                             \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
			{
				// Complete p2 parent
				Action: func() {
					comp.CompleteToolCall("p2", "agent", "{}", "t2", "d2", false)
				},
				Expected: "⚙ agent t1                     \n" +
					"⚙ read a                       \n" +
					"⚙ write b                      \n" +
					"✓ agent t2                     \n" +
					"d2                             \n" +
					"✓ read c                       \n" +
					"r3                             \n" +
					"✓ write d                      \n" +
					"r4                             \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
			{
				// Now complete p1's children and parent
				Action: func() {
					comp.CompleteChildToolCall("p1", "c1", "read", "{}", "a", "r1", false)
					comp.CompleteChildToolCall("p1", "c2", "write", "{}", "b", "r2", false)
					comp.CompleteToolCall("p1", "agent", "{}", "t1", "d1", false)
				},
				Expected: "✓ agent t1                     \n" +
					"d1                             \n" +
					"✓ read a                       \n" +
					"r1                             \n" +
					"✓ write b                      \n" +
					"r2                             \n" +
					"✓ agent t2                     \n" +
					"d2                             \n" +
					"✓ read c                       \n" +
					"r3                             \n" +
					"✓ write d                      \n" +
					"r4                             \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
		}
		comptest.TestComponent(t, comp, w, tests)
	})

	t.Run("text tool text break text tool child text", func(t *testing.T) {
		comp := NewComponent(ComponentConfig{})
		comp.Resize(W, H)
		w := mkW()
		tests := []comptest.TestCase{
			{
				Action: func() {
					comp.AddReceiveMessageChunk("intro")
					comp.AddToolCall("t1", "read", "{}", "a")
					comp.CompleteToolCall("t1", "read", "{}", "a", "d1", false)
					comp.AddReceiveMessageChunk(" middle")
				},
				Expected: "intro                          \n" +
					"                               \n" +
					"✓ read a                       \n" +
					"d1                             \n" +
					"middle                         \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
			{
				Action: func() {
					comp.AddReceiveMessageBreak()
					comp.AddReceiveMessageChunk("continue")
					comp.AddToolCall("p1", "agent", "{}", "job")
					comp.AddChildToolCall("p1", "c1", "write", "{}", "f")
					comp.CompleteChildToolCall("p1", "c1", "write", "{}", "f", "ok", false)
					comp.CompleteToolCall("p1", "agent", "{}", "job", "done", false)
					comp.AddReceiveMessageChunk(" end")
				},
				Expected: "intro                          \n" +
					"                               \n" +
					"✓ read a                       \n" +
					"d1                             \n" +
					"middle                         \n" +
					"                               \n" +
					"continue                       \n" +
					"                               \n" +
					"✓ agent job                    \n" +
					"done                           \n" +
					"✓ write f                      \n" +
					"ok                             \n" +
					"end                            \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
		}
		comptest.TestComponent(t, comp, w, tests)
	})

	t.Run("complete child for nonexistent parent", func(t *testing.T) {
		comp := NewComponent(ComponentConfig{})
		comp.Resize(W, H)
		w := mkW()
		tests := []comptest.TestCase{
			{
				Action: func() {
					// CompleteChildToolCall with no parent should be a no-op
					// (currentTurn is nil, so it returns early).
					comp.CompleteChildToolCall("missing", "c1", "read", "{}", "f", "data", false)
				},
				Expected: "                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
		}
		comptest.TestComponent(t, comp, w, tests)
	})

	t.Run("tool with empty output shows no result line", func(t *testing.T) {
		comp := NewComponent(ComponentConfig{})
		comp.Resize(W, H)
		w := mkW()
		tests := []comptest.TestCase{
			{
				Action: func() {
					comp.AddToolCall("t1", "read", "{}", "a")
					comp.CompleteToolCall("t1", "read", "{}", "a", "", false)
				},
				Expected: "✓ read a                       \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
		}
		comptest.TestComponent(t, comp, w, tests)
	})

	t.Run("reset clears turn state", func(t *testing.T) {
		comp := NewComponent(ComponentConfig{})
		comp.Resize(W, H)
		w := mkW()
		tests := []comptest.TestCase{
			{
				Action: func() {
					comp.AddToolCall("t1", "read", "{}", "a")
					comp.AddToolCall("t2", "write", "{}", "b")
				},
				Expected: "⚙ read a                       \n" +
					"⚙ write b                      \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
			{
				Action: func() {
					comp.Reset()
				},
				Expected: "                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
			{
				// After reset, new tools work correctly
				Action: func() {
					comp.AddToolCall("t3", "search", "{}", "c")
				},
				Expected: "⚙ search c                     \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
		}
		comptest.TestComponent(t, comp, w, tests)
	})

	t.Run("send message between two tool turns", func(t *testing.T) {
		comp := NewComponent(ComponentConfig{})
		comp.Resize(W, H)
		w := mkW()
		tests := []comptest.TestCase{
			{
				// Tool calls are placed organically: text between
				// tools appears in chronological order.
				Action: func() {
					comp.AddToolCall("t1", "read", "{}", "a")
					comp.CompleteToolCall("t1", "read", "{}", "a", "d1", false)
					comp.AddReceiveMessageChunk(" answer")
					comp.AddSendMessage("followup")
					comp.AddToolCall("t2", "write", "{}", "b")
					comp.CompleteToolCall("t2", "write", "{}", "b", "d2", false)
				},
				Expected: "✓ read a                       \n" +
					"d1                             \n" +
					"answer                         \n" +
					"                               \n" +
					"followup                       \n" +
					"✓ write b                      \n" +
					"d2                             \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
		}
		comptest.TestComponent(t, comp, w, tests)
	})

	t.Run("multiple replayed completions without adds", func(t *testing.T) {
		comp := NewComponent(ComponentConfig{})
		comp.Resize(W, H)
		w := mkW()
		tests := []comptest.TestCase{
			{
				Action: func() {
					comp.CompleteToolCall("t1", "read", "{}", "a", "d1", false)
					comp.CompleteToolCall("t2", "write", "{}", "b", "d2", false)
					comp.CompleteToolCall("t3", "search", "{}", "c", "d3", true)
				},
				Expected: "✓ read a                       \n" +
					"d1                             \n" +
					"✓ write b                      \n" +
					"d2                             \n" +
					"✗ search c                     \n" +
					"d3                             \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"                               \n" +
					"  ┌───────────────────────┐    \n" +
					"  │                       │    \n" +
					"  └───────────────────────┘    \n" +
					"                               ",
			},
		}
		comptest.TestComponent(t, comp, w, tests)
	})
}

func TestComponentErrorBreaksTextStream(t *testing.T) {
	comp := NewComponent(ComponentConfig{})
	comp.Resize(20, 10)

	w := term.NewStringWriter(21, 11)
	tests := []comptest.TestCase{
		{
			Action: func() {
				comp.AddReceiveMessageChunk("Hello ")
				comp.AddErrorMessage("oops")
				// New text after error starts a fresh message.
				comp.AddReceiveMessageChunk("World")
			},
			Expected: "Hello                \n" +
				"                     \n" +
				"! oops               \n" +
				"World                \n" +
				"                     \n" +
				"                     \n" +
				"                     \n" +
				" ┌──────────────┐    \n" +
				" │              │    \n" +
				" └──────────────┘    \n" +
				"                     ",
		},
	}
	comptest.TestComponent(t, comp, w, tests)
}

// benchChunks returns nChunks small pieces of text that, concatenated,
// form valid markdown with occasional paragraph breaks.
func benchChunks(nChunks int) []string {
	chunks := make([]string, nChunks)
	for i := range chunks {
		if i%50 == 0 {
			chunks[i] = "\n\n"
		} else {
			chunks[i] = "word "
		}
	}
	return chunks
}

// BenchmarkAddReceiveMessageChunk measures the cost of streaming 500 chunks
// into a single message. The optimised path reuses the markdown component
// in-place via Init, so the majority of iterations should hit the fast path.
func BenchmarkAddReceiveMessageChunk(b *testing.B) {
	chunks := benchChunks(500)

	comp := NewComponent(ComponentConfig{})
	comp.Resize(80, 40)

	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		for _, ch := range chunks {
			comp.AddReceiveMessageChunk(ch)
		}
		comp.AddReceiveMessageBreak()
	}
}

// BenchmarkAddReceiveMessageChunkReset measures a full stream-then-reset
// cycle, which is what happens between consecutive assistant turns.
func BenchmarkAddReceiveMessageChunkReset(b *testing.B) {
	chunks := benchChunks(500)

	comp := NewComponent(ComponentConfig{})
	comp.Resize(80, 40)

	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		for _, ch := range chunks {
			comp.AddReceiveMessageChunk(ch)
		}
		comp.Reset()
	}
}

// BenchmarkAddReceiveMessageChunkDraw measures the full chunk+render cycle:
// each chunk is followed by a Resize and Draw, which is the pattern the
// real rendering pipeline exercises. The optimisation avoids list Remove/PushBack
// on the fast path, so the layout pass walks a stable list.
func BenchmarkAddReceiveMessageChunkDraw(b *testing.B) {
	chunks := benchChunks(500)
	w := term.NewStringWriter(80, 40)

	comp := NewComponent(ComponentConfig{})
	comp.Resize(80, 40)

	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		for _, ch := range chunks {
			comp.AddReceiveMessageChunk(ch)
			comp.Resize(80, 40)
			comp.Draw(w)
		}
		comp.Reset()
	}
}

func TestFormatToolArgs(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{``, ""},
		{`{}`, ""},
		{`{"path":"/tmp/x"}`, "path=/tmp/x"},
		{`{"path":"a","content":"hello"}`, "content=hello path=a"},
		{`{"count":5}`, "count=5"},
		{`{"files":["a","b"]}`, `files=["a","b"]`},
		{`{"content":"line1\nline2"}`, "content=line1 line2"},
		{`{"command":"ls","timeout":null,"working_dir":null}`, "command=ls"},
		{`{"command":"ls","description":"","timeout":null}`, "command=ls"},
		{`{"a":null,"b":"","c":"val"}`, "c=val"},
		{`not json`, "not json"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expected, formatToolArgs(tt.input))
		})
	}
}

func TestComponentToggleContracted(t *testing.T) {
	comp := NewComponent(ComponentConfig{})
	comp.Resize(30, 10)
	w := term.NewStringWriter(31, 11)

	tests := []comptest.TestCase{
		{
			Action: func() {
				comp.AddToolCall("c1", "read_file", `{}`, "a.go")
				comp.CompleteToolCall("c1", "read_file", `{}`, "a.go", "data", false)
				comp.AddToolCall("c2", "search", `{}`, "pattern")
				comp.CompleteToolCall("c2", "search", `{}`, "pattern", "found", true)
			},
			Expected: "✓ read_file a.go               \n" +
				"data                           \n" +
				"✗ search pattern               \n" +
				"found                          \n" +
				"                               \n" +
				"                               \n" +
				"                               \n" +
				"  ┌───────────────────────┐    \n" +
				"  │                       │    \n" +
				"  └───────────────────────┘    \n" +
				"                               ",
		},
		{
			// Toggle to collapsed
			Action: func() {
				comp.ToggleContracted()
			},
			Expected: "├─ ✓ read_file a.go            \n" +
				"└─ ✗ search pattern            \n" +
				"Press <ctrl-o> to expand       \n" +
				"                               \n" +
				"                               \n" +
				"                               \n" +
				"                               \n" +
				"  ┌───────────────────────┐    \n" +
				"  │                       │    \n" +
				"  └───────────────────────┘    \n" +
				"                               ",
		},
		{
			// Toggle to expanded: tools back to full view
			Action: func() {
				comp.ToggleContracted()
			},
			Expected: "✓ read_file a.go               \n" +
				"data                           \n" +
				"✗ search pattern               \n" +
				"found                          \n" +
				"                               \n" +
				"                               \n" +
				"                               \n" +
				"  ┌───────────────────────┐    \n" +
				"  │                       │    \n" +
				"  └───────────────────────┘    \n" +
				"                               ",
		},
	}
	comptest.TestComponent(t, comp, w, tests)
}

func TestComponentStartCollapsed(t *testing.T) {
	comp := NewComponent(ComponentConfig{StartCollapsed: true})
	comp.Resize(30, 10)
	w := term.NewStringWriter(31, 11)

	tests := []comptest.TestCase{
		{
			// Tools start in collapsed tree view
			Action: func() {
				comp.AddToolCall("c1", "read_file", `{}`, "a.go")
				comp.CompleteToolCall("c1", "read_file", `{}`, "a.go", "data", false)
				comp.AddToolCall("c2", "search", `{}`, "pattern")
				comp.CompleteToolCall("c2", "search", `{}`, "pattern", "found", true)
			},
			Expected: "├─ ✓ read_file a.go            \n" +
				"└─ ✗ search pattern            \n" +
				"Press <ctrl-o> to expand       \n" +
				"                               \n" +
				"                               \n" +
				"                               \n" +
				"                               \n" +
				"  ┌───────────────────────┐    \n" +
				"  │                       │    \n" +
				"  └───────────────────────┘    \n" +
				"                               ",
		},
		{
			// ctrl-o expands to full view
			Action: func() {
				comp.ToggleContracted()
			},
			Expected: "✓ read_file a.go               \n" +
				"data                           \n" +
				"✗ search pattern               \n" +
				"found                          \n" +
				"                               \n" +
				"                               \n" +
				"                               \n" +
				"  ┌───────────────────────┐    \n" +
				"  │                       │    \n" +
				"  └───────────────────────┘    \n" +
				"                               ",
		},
		{
			// ctrl-o collapses back to collapsed
			Action: func() {
				comp.ToggleContracted()
			},
			Expected: "├─ ✓ read_file a.go            \n" +
				"└─ ✗ search pattern            \n" +
				"Press <ctrl-o> to expand       \n" +
				"                               \n" +
				"                               \n" +
				"                               \n" +
				"                               \n" +
				"  ┌───────────────────────┐    \n" +
				"  │                       │    \n" +
				"  └───────────────────────┘    \n" +
				"                               ",
		},
	}
	comptest.TestComponent(t, comp, w, tests)
}

func TestComponentBreakCompletesRunningChildTools(t *testing.T) {
	comp := NewComponent(ComponentConfig{})
	comp.Resize(30, 20)
	w := term.NewStringWriter(31, 21)

	blank := "                               \n"
	tests := []comptest.TestCase{
		{
			// Parent tool with two children; only one child completes.
			Action: func() {
				comp.AddToolCall("p1", "agent", "{}", "task")
				comp.AddChildToolCall("p1", "c1", "read", "{}", "a")
				comp.AddChildToolCall("p1", "c2", "write", "{}", "b")
				comp.CompleteChildToolCall("p1", "c1", "read", "{}", "a", "ok", false)
				// c2 is still running, p1 is still running
			},
			Expected: "⚙ agent task                   \n" +
				"✓ read a                       \n" +
				"ok                             \n" +
				"⚙ write b                      \n" +
				blank + blank + blank + blank + blank + blank + blank + blank + blank + blank + blank + blank + blank +
				"  ┌───────────────────────┐    \n" +
				"  │                       │    \n" +
				"  └───────────────────────┘    \n" +
				"                               ",
		},
		{
			// Break resolves running child (c2) and parent (p1) as
			// canceled: their results never arrived.
			Action: func() {
				comp.AddReceiveMessageBreak()
			},
			Expected: "✗ agent task                   \n" +
				"canceled                       \n" +
				"✓ read a                       \n" +
				"ok                             \n" +
				"✗ write b                      \n" +
				"canceled                       \n" +
				blank + blank + blank + blank + blank + blank + blank + blank + blank + blank + blank +
				"  ┌───────────────────────┐    \n" +
				"  │                       │    \n" +
				"  └───────────────────────┘    \n" +
				"                               ",
		},
	}
	comptest.TestComponent(t, comp, w, tests)
}

// TestComponentBreakCancelsRunningTool verifies a tool call whose
// result never arrives (cancellation stops the event loop before the
// result is delivered) renders as canceled rather than successful.
// Regression for RUNE-305.
func TestComponentBreakCancelsRunningTool(t *testing.T) {
	comp := NewComponent(ComponentConfig{})
	comp.Resize(30, 6)
	w := term.NewStringWriter(31, 7)

	blank := "                               \n"
	tests := []comptest.TestCase{
		{
			Action: func() {
				comp.AddToolCall("t1", "grep_files", "{}", "pattern")
				comp.AddReceiveMessageBreak()
			},
			Expected: "✗ grep_files pattern           \n" +
				"canceled                       \n" +
				blank +
				"  ┌───────────────────────┐    \n" +
				"  │                       │    \n" +
				"  └───────────────────────┘    \n" +
				"                               ",
		},
	}
	comptest.TestComponent(t, comp, w, tests)
}

func TestComponentNewToolWhileCollapsed(t *testing.T) {
	comp := NewComponent(ComponentConfig{})
	comp.Resize(30, 10)
	w := term.NewStringWriter(31, 11)

	tests := []comptest.TestCase{
		{
			// Start with one tool, collapse
			Action: func() {
				comp.AddToolCall("c1", "read_file", `{}`, "a.go")
				comp.CompleteToolCall("c1", "read_file", `{}`, "a.go", "data", false)
				comp.ToggleContracted()
			},
			Expected: "└─ ✓ read_file a.go            \n" +
				"Press <ctrl-o> to expand       \n" +
				"                               \n" +
				"                               \n" +
				"                               \n" +
				"                               \n" +
				"                               \n" +
				"  ┌───────────────────────┐    \n" +
				"  │                       │    \n" +
				"  └───────────────────────┘    \n" +
				"                               ",
		},
		{
			// Add new tool while collapsed — shows in collapsed view
			Action: func() {
				comp.AddReceiveMessageBreak()
				comp.AddToolCall("c2", "search", `{}`, "pat")
				comp.CompleteToolCall("c2", "search", `{}`, "pat", "", false)
			},
			Expected: "└─ ✓ read_file a.go            \n" +
				"Press <ctrl-o> to expand       \n" +
				"                               \n" +
				"└─ ✓ search pat                \n" +
				"Press <ctrl-o> to expand       \n" +
				"                               \n" +
				"                               \n" +
				"  ┌───────────────────────┐    \n" +
				"  │                       │    \n" +
				"  └───────────────────────┘    \n" +
				"                               ",
		},
	}
	comptest.TestComponent(t, comp, w, tests)
}

// TestComponentMessageSpacing verifies the vertical spacing between
// different message types when ReceiveMessageBottomPad and
// SendMessageBottomPad are configured (matching the real extension).
//
// The expected layout (30 wide, 20 tall) shows one spacer row between
// each element — no double-padding at any boundary:
//
//	Row  0: "What files exist?"        (user message)
//	Row  1: (spacer — SendMessageBottomPad)
//	Row  2: "Let me check."            (assistant text)
//	Row  3: (spacer — ReceiveMessageBottomPad, from tool call break)
//	Row  4: (spacer — ReceiveMessageBottomPad, from AddReceiveMessageBreak)
//	Row  5: "✓ list_dir"               (completed tool call)
//	Row  6: "main.go"                  (tool result)
//	Row  7: "Found main.go."           (assistant text, second chunk)
//	Row  8: (spacer — ReceiveMessageBottomPad, from break)
//	Row  9: (spacer — ReceiveMessageBottomPad, from AddReceiveMessageBreak)
//	Row 10: "How about tests?"         (user message)
//	Row 11: (spacer — SendMessageBottomPad)
//	Row 12: "Sure."                    (assistant text)
//
// TestComponentMessageSpacing verifies the vertical spacing between
// different message types when ReceiveMessageBottomPad and
// SendMessageBottomPad are configured (matching the real extension).
func TestComponentMessageSpacing(t *testing.T) {
	const (
		width  = 30
		height = 20
	)
	comp := NewComponent(ComponentConfig{
		SendMessageBottomPad: 1,
	})
	comp.Resize(width, height)

	w := term.NewStringWriter(width+1, height+1)
	tests := []comptest.TestCase{
		{
			Action: func() {
				// Turn 1: user → assistant text → tool → result → more text → break
				comp.AddSendMessage("What files exist?")
				comp.AddReceiveMessageChunk("Let me check.")
				comp.AddToolCall("t1", "list_dir", "", "")
				comp.CompleteToolCall("t1", "list_dir", "", "", "main.go", false)
				comp.AddReceiveMessageChunk("Found main.go.")
				comp.AddReceiveMessageBreak()

				// Turn 2: user → assistant text
				comp.AddSendMessage("How about tests?")
				comp.AddReceiveMessageChunk("Sure.")
			},
			// Uniform 1-cell spacing: markdown's trailing line provides
			// the gap after receive messages; SendMessageBottomPad
			// provides it after send messages. No double-padding.
			Expected: "" +
				"What files exist?              \n" +
				"                               \n" + // SendMessageBottomPad
				"Let me check.                  \n" +
				"                               \n" + // markdown trailing line
				"✓ list_dir                     \n" +
				"main.go                        \n" +
				"Found main.go.                 \n" +
				"                               \n" + // markdown trailing line
				"How about tests?               \n" +
				"                               \n" + // SendMessageBottomPad
				"Sure.                          \n" +
				"                               \n" + // markdown trailing line
				"                               \n" +
				"                               \n" +
				"                               \n" +
				"                               \n" +
				"                               \n" +
				"  ┌───────────────────────┐    \n" +
				"  │                       │    \n" +
				"  └───────────────────────┘    \n" +
				"                               ",
		},
	}
	comptest.TestComponent(t, comp, w, tests)
}

func TestSendMessageBackgroundDoesNotLeakIntoPadding(t *testing.T) {
	const (
		width  = 20
		height = 10
		bg     = term.ColorGray
	)
	comp := NewComponent(ComponentConfig{
		SendMessageStringConfig: component.StringConfig{
			Alignment:            component.AlignmentLeft,
			BackgroundAttributes: term.Attributes{Bg: bg},
		},
		SendMessageBottomPad: 1,
	})
	comp.Resize(width, height)

	comp.AddSendMessage("hello")

	w := term.NewStringWriter(width, height)
	comp.Draw(w)
	_ = w.Flush()

	cells := w.Cells()
	cell := func(x, y int) term.Cell { return cells[y*width+x] }

	// Row 0 should carry the send-message background.
	assert.Equal(t, bg, cell(0, 0).Bg, "text row should have send-message bg")

	// Row 1 is the spacer — it must NOT carry the background.
	assert.NotEqual(t, bg, cell(0, 1).Bg, "spacer row should not have send-message bg")
}

// TestComponentHintRestoredAfterPromptSelect verifies that when a prompt is
// shown during an active agent turn, the receive-message hint is saved, hidden,
// and then automatically restored when the user selects an option.
func TestComponentTaskActiveFormSuppressesVisibleProgress(t *testing.T) {
	comp := NewComponent(ComponentConfig{})

	assert.Equal(t, "", comp.TaskActiveForm())

	comp.UpdateTaskProgress(ProgressTaskEntry{
		ID:         "1",
		Subject:    "Write tests",
		ActiveForm: "Writing tests",
		Status:     "in_progress",
	})

	assert.Equal(t, "Writing tests", comp.progress.ActiveForm())
	assert.Equal(t, "", comp.TaskActiveForm())
}

func TestComponentAttachmentKeysAreStable(t *testing.T) {
	comp := NewComponent(ComponentConfig{})
	comp.Resize(40, 10)

	first := comp.AddAttachment(NewWorkspaceFileAttachment("alpha.go"))
	second := comp.AddAttachment(NewWorkspaceFileAttachment("bravo.go"))
	require.NotEqual(t, first.Key, second.Key)

	comp.RemoveAttachment(0)
	require.Len(t, comp.Attachments(), 1)
	assert.Equal(t, second.Key, comp.Attachments()[0].Key,
		"removing an earlier chip must not renumber the later ones")

	third := comp.AddAttachment(NewWorkspaceFileAttachment("charlie.go"))
	assert.NotEqual(t, first.Key, third.Key,
		"a freed key must not be handed out again")
	assert.NotEqual(t, second.Key, third.Key)
}

func TestComponentRemoveAttachmentUnlinksButKeepsText(t *testing.T) {
	comp := NewComponent(ComponentConfig{})
	comp.Resize(40, 10)

	a := comp.AddAttachment(NewWorkspaceFileAttachment("alpha.go"))
	comp.Input().SetDraft("check alpha.go",
		[]InlineAttachmentLink{{Key: a.Key, Start: 6, End: 14}})

	comp.RemoveAttachment(0)

	assert.Equal(t, "check alpha.go", comp.Input().Text(),
		"dropping a chip must not rewrite what the user typed")
	assert.Empty(t, comp.Input().Links())
	assert.Empty(t, comp.Attachments())
}

func TestComponentInlineLabelDeletionKeepsChip(t *testing.T) {
	comp := NewComponent(ComponentConfig{})
	comp.Resize(40, 10)

	a := comp.AddAttachment(NewWorkspaceFileAttachment("alpha.go"))
	comp.Input().SetDraft("check alpha.go",
		[]InlineAttachmentLink{{Key: a.Key, Start: 6, End: 14}})

	in := comp.Input().(*textHandlerInput)
	edit(in, 6, 14, "")

	assert.Equal(t, "check ", comp.Input().Text())
	assert.Empty(t, comp.Input().Links())
	assert.Len(t, comp.Attachments(), 1,
		"deleting one inline label must not drop the attachment")
}

func TestComponentTakeAndRestoreDraft(t *testing.T) {
	comp := NewComponent(ComponentConfig{})
	comp.Resize(40, 10)

	a := comp.AddAttachment(NewWorkspaceFileAttachment("alpha.go"))
	comp.Input().SetDraft("check alpha.go",
		[]InlineAttachmentLink{{Key: a.Key, Start: 6, End: 14}})

	d := comp.TakeDraft()

	assert.Equal(t, "check alpha.go", d.Text)
	assert.Equal(t, []Attachment{a}, d.Attachments)
	assert.Equal(t, []InlineAttachmentLink{{Key: a.Key, Start: 6, End: 14}}, d.Links)
	assert.Equal(t, "", comp.Input().Text())
	assert.Empty(t, comp.Attachments())
	assert.Empty(t, comp.Input().Links())

	comp.RestoreDraft(d)

	assert.Equal(t, d.Text, comp.Input().Text())
	assert.Equal(t, d.Attachments, comp.Attachments())
	assert.Equal(t, d.Links, comp.Input().Links(),
		"restoring must preserve the keys the links point at")
}

func TestComponentRestoreDraftDoesNotSynthesizeLinks(t *testing.T) {
	comp := NewComponent(ComponentConfig{})
	comp.Resize(40, 10)

	comp.RestoreDraft(Draft{
		Text:        "check alpha.go",
		Attachments: []Attachment{NewWorkspaceFileAttachment("alpha.go")},
	})

	assert.Empty(t, comp.Input().Links(),
		"plain text that happens to match a chip must stay unlinked")
	assert.Len(t, comp.Attachments(), 1)
}

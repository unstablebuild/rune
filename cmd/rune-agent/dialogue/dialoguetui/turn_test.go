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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/component/comptest"
	"github.com/unstablebuild/rune-go-sdk/term"
)

var _ component.Responsive = (*Turn)(nil)

func newTestTurn() *Turn {
	return NewTurn(&ComponentConfig{}, nil)
}

func TestTurnAddToolCall(t *testing.T) {
	turn := newTestTurn()

	turn.AddToolCall("c1", "read_file", `{"path":"a.go"}`, "")
	assert.True(t, turn.HasTools())
	assert.Contains(t, turn.tools, "c1")
	assert.False(t, turn.tools["c1"].done)
}

func TestTurnCompleteToolCall(t *testing.T) {
	turn := newTestTurn()

	turn.AddToolCall("c1", "read_file", `{}`, "")
	turn.CompleteToolCall("c1", "read_file", `{}`, "", "file data", false)

	assert.True(t, turn.tools["c1"].done)
	assert.False(t, turn.tools["c1"].isError)
}

func TestTurnCompleteToolCallError(t *testing.T) {
	turn := newTestTurn()

	turn.AddToolCall("c1", "read_file", `{}`, "")
	turn.CompleteToolCall("c1", "read_file", `{}`, "", "not found", true)

	assert.True(t, turn.tools["c1"].done)
	assert.True(t, turn.tools["c1"].isError)
}

func TestTurnCompleteUnknownToolCall(t *testing.T) {
	turn := newTestTurn()
	// Should not panic.
	turn.CompleteToolCall("nonexistent", "t", "{}", "", "out", false)
}

func TestTurnAddChildToolCall(t *testing.T) {
	turn := newTestTurn()

	turn.AddToolCall("parent", "spawn_agent", `{}`, "")
	turn.AddChildToolCall("parent", "child1", "read_file", `{}`, "")

	assert.NotNil(t, turn.tools["parent"].childTurn)
	assert.Contains(t, turn.tools["parent"].childTurn.tools, "child1")
}

func TestTurnAddChildToolCallNoParent(t *testing.T) {
	turn := newTestTurn()

	// No parent — should add as top-level.
	turn.AddChildToolCall("nonexistent", "child1", "read_file", `{}`, "")
	assert.Contains(t, turn.tools, "child1")
}

func TestTurnCompleteChildToolCall(t *testing.T) {
	turn := newTestTurn()

	turn.AddToolCall("parent", "spawn_agent", `{}`, "")
	turn.AddChildToolCall("parent", "child1", "read_file", `{}`, "")
	turn.CompleteChildToolCall("parent", "child1", "read_file", `{}`, "", "data", false)

	child := turn.tools["parent"].childTurn.tools["child1"]
	assert.True(t, child.done)
	assert.False(t, child.isError)
}

func TestTurnCompleteParentAfterChildren(t *testing.T) {
	turn := newTestTurn()

	turn.AddToolCall("parent", "spawn_agent", `{}`, "")
	turn.AddChildToolCall("parent", "child1", "read_file", `{}`, "")
	turn.AddChildToolCall("parent", "child2", "edit_file", `{}`, "")
	turn.CompleteChildToolCall("parent", "child1", "read_file", `{}`, "", "data1", false)
	turn.CompleteChildToolCall("parent", "child2", "edit_file", `{}`, "", "data2", false)

	// Completing the parent after children must update the
	// parent's own node, not the last child's node.
	turn.CompleteToolCall("parent", "spawn_agent", `{}`, "", "done", false)

	assert.True(t, turn.tools["parent"].done)
	assert.False(t, turn.tools["parent"].isError)
}

func TestTurnHeight(t *testing.T) {
	turn := newTestTurn()
	assert.Equal(t, 0, turn.Height(80))

	turn.AddToolCall("c1", "read_file", `{}`, "")
	h := turn.Height(80)
	assert.Greater(t, h, 0)
}

func TestWriteRuneLineAttr(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		x         int
		maxWidth  int
		wantNextX int
		wantCells map[int]term.Cell
	}{
		{
			name:      "ascii",
			text:      "ab",
			maxWidth:  10,
			wantNextX: 2,
			wantCells: map[int]term.Cell{
				0: {Ch: 'a', Width: 1},
				1: {Ch: 'b', Width: 1},
			},
		},
		{
			name:      "empty string",
			text:      "",
			maxWidth:  10,
			wantNextX: 0,
			wantCells: map[int]term.Cell{0: {}},
		},
		{
			name:      "emoji advances two columns",
			text:      "a🚀b",
			maxWidth:  10,
			wantNextX: 4,
			wantCells: map[int]term.Cell{
				0: {Ch: 'a', Width: 1},
				1: {Ch: '🚀', Width: 2},
				2: {},
				3: {Ch: 'b', Width: 1},
			},
		},
		{
			name:      "nerd icon prefix advances two columns",
			text:      "󰗠 x",
			maxWidth:  10,
			wantNextX: 4,
			wantCells: map[int]term.Cell{
				0: {Ch: '󰗠', Width: 2},
				1: {},
				2: {Ch: ' ', Width: 1},
				3: {Ch: 'x', Width: 1},
			},
		},
		{
			name:      "cjk in tool title",
			text:      "中x",
			maxWidth:  10,
			wantNextX: 3,
			wantCells: map[int]term.Cell{
				0: {Ch: '中', Width: 2},
				1: {},
				2: {Ch: 'x', Width: 1},
			},
		},
		{
			name:      "family emoji stays one cell",
			text:      "👨‍👩‍👧",
			maxWidth:  10,
			wantNextX: 2,
			wantCells: map[int]term.Cell{
				0: {Ch: '👨', Width: 2},
				1: {},
			},
		},
		{
			name:      "tab occupies one cell",
			text:      "\ta",
			maxWidth:  10,
			wantNextX: 2,
			wantCells: map[int]term.Cell{
				0: {Ch: '\t', Width: 1},
				1: {Ch: 'a', Width: 1},
			},
		},
		{
			name:      "wide cluster does not straddle maxWidth",
			text:      "a🚀",
			maxWidth:  2,
			wantNextX: 1,
			wantCells: map[int]term.Cell{
				0: {Ch: 'a', Width: 1},
				1: {},
			},
		},
		{
			name:      "maxWidth is an absolute column",
			text:      "abc",
			x:         4,
			maxWidth:  5,
			wantNextX: 5,
			wantCells: map[int]term.Cell{
				4: {Ch: 'a', Width: 1},
				5: {},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := term.NewStringWriter(10, 1)
			x := writeRuneLineAttr(w, tt.x, 0, tt.text, tt.maxWidth,
				term.Attributes{})
			assert.Equal(t, tt.wantNextX, x)
			cells := w.Cells()
			for col, want := range tt.wantCells {
				assert.Equal(t, want.Ch, cells[col].Ch, "cell %d rune", col)
				assert.Equal(t, want.Width, cells[col].Width, "cell %d width", col)
			}
		})
	}
}

func TestTurnDrawRunningAndComplete(t *testing.T) {
	comp := NewComponent(ComponentConfig{})
	comp.Resize(30, 10)
	w := term.NewStringWriter(31, 11)

	tests := []comptest.TestCase{
		{
			Action: func() {
				comp.AddToolCall("c1", "read_file", `{}`, "summary")
			},
			Expected: "⚙ read_file summary            \n" +
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
				comp.CompleteToolCall("c1", "read_file", `{}`, "summary", "ok", false)
			},
			Expected: "✓ read_file summary            \n" +
				"ok                             \n" +
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

func TestTurnDrawExpandedShowsSummaryAndArgs(t *testing.T) {
	comp := NewComponent(ComponentConfig{})
	comp.Resize(40, 10)
	w := term.NewStringWriter(41, 11)

	tests := []comptest.TestCase{
		{
			Action: func() {
				// Bash-like tool: summary=command, args contain description.
				comp.AddToolCall("c1", "bash",
					`{"command":"go test ./...","description":"Run all tests"}`,
					"go test ./...")
			},
			Expected: "⚙ bash go test ./...                     \n" +
				"command=go test ./... description=Run    \n" +
				"all tests                                \n" +
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
				comp.CompleteToolCall("c1", "bash",
					`{"command":"go test ./...","description":"Run all tests"}`,
					"go test ./...", "PASS", false)
			},
			Expected: "✓ bash go test ./...                     \n" +
				"command=go test ./... description=Run    \n" +
				"all tests                                \n" +
				"PASS                                     \n" +
				"                                         \n" +
				"                                         \n" +
				"                                         \n" +
				"   ┌───────────────────────────────┐     \n" +
				"   │                               │     \n" +
				"   └───────────────────────────────┘     \n" +
				"                                         ",
		},
	}
	comptest.TestComponent(t, comp, w, tests)
}

func TestTurnNestedChildToolCalls(t *testing.T) {
	comp := NewComponent(ComponentConfig{})
	comp.Resize(30, 10)
	w := term.NewStringWriter(31, 11)

	tests := []comptest.TestCase{
		{
			Action: func() {
				comp.AddToolCall("p1", "spawn_agent", `{}`, "")
				comp.AddChildToolCall("p1", "c1", "read_file", `{}`, "a.go")
			},
			Expected: "⚙ spawn_agent                  \n" +
				"⚙ read_file a.go               \n" +
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
				comp.CompleteChildToolCall("p1", "c1", "read_file", `{}`, "a.go", "data", false)
			},
			Expected: "⚙ spawn_agent                  \n" +
				"✓ read_file a.go               \n" +
				"data                           \n" +
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

func TestTurnToolOrder(t *testing.T) {
	turn := newTestTurn()

	turn.AddToolCall("c1", "read_file", `{}`, "")
	turn.AddToolCall("c2", "edit_file", `{}`, "")
	turn.AddToolCall("c3", "search", `{}`, "")

	assert.Equal(t, []string{"c1", "c2", "c3"}, turn.toolOrder)
}

func TestTurnArgsStored(t *testing.T) {
	turn := newTestTurn()

	turn.AddToolCall("c1", "read_file", `{"path":"a.go"}`, "")
	assert.Equal(t, `{"path":"a.go"}`, turn.tools["c1"].args)
}

func TestTurnCollapsedHeight(t *testing.T) {
	t.Run("flat tools", func(t *testing.T) {
		turn := newTestTurn()
		turn.AddToolCall("c1", "read_file", `{}`, "")
		turn.AddToolCall("c2", "edit_file", `{}`, "")
		turn.mode = collapseModeCollapsed
		assert.Equal(t, 2, turn.collapsedHeight())
		assert.Equal(t, 3, turn.Height(80)) // +1 bottom padding
	})

	t.Run("nested tools", func(t *testing.T) {
		turn := newTestTurn()
		turn.AddToolCall("p1", "spawn_agent", `{}`, "")
		turn.AddChildToolCall("p1", "c1", "read_file", `{}`, "")
		turn.AddChildToolCall("p1", "c2", "edit_file", `{}`, "")
		turn.mode = collapseModeCollapsed
		// 1 (parent) + 2 (children) = 3
		assert.Equal(t, 3, turn.collapsedHeight())
	})

	t.Run("deeply nested tools", func(t *testing.T) {
		turn := newTestTurn()
		turn.AddToolCall("p1", "spawn_agent", `{}`, "")
		turn.AddChildToolCall("p1", "c1", "spawn_agent", `{}`, "")
		// grandchild: manually build the nested turn
		childTurn := turn.tools["p1"].childTurn
		childTurn.tools["c1"].childTurn = NewTurn(childTurn.cfg, nil)
		childTurn.tools["c1"].childTurn.AddToolCall("g1", "read_file", `{}`, "")
		turn.mode = collapseModeCollapsed
		// 1 (p1) + 1 (c1) + 1 (g1) = 3
		assert.Equal(t, 3, turn.collapsedHeight())
	})
}

func TestTurnSetCollapsedRecurse(t *testing.T) {
	turn := newTestTurn()
	turn.AddToolCall("p1", "spawn_agent", `{}`, "")
	turn.AddChildToolCall("p1", "c1", "read_file", `{}`, "")

	turn.SetCollapseMode(collapseModeCollapsed)
	assert.Equal(t, collapseModeCollapsed, turn.mode)
	assert.Equal(t, collapseModeCollapsed, turn.tools["p1"].childTurn.mode)

	turn.SetCollapseMode(collapseModeExpanded)
	assert.Equal(t, collapseModeExpanded, turn.mode)
	assert.Equal(t, collapseModeExpanded, turn.tools["p1"].childTurn.mode)
}

func TestTurnAnimationLifecycle(t *testing.T) {
	interrupter := term.FuncInterrupter(func(context.Context) error { return nil })
	turn := NewTurn(&ComponentConfig{}, interrupter)

	// Adding a tool while not collapsed: no animation.
	turn.AddToolCall("c1", "read_file", `{}`, "a.go")
	assert.Nil(t, turn.animation)

	// Collapsing with running tools: animation starts.
	turn.SetCollapseMode(collapseModeCollapsed)
	assert.NotNil(t, turn.animation)

	// Completing the tool: animation stops.
	turn.CompleteToolCall("c1", "read_file", `{}`, "a.go", "data", false)
	assert.Nil(t, turn.animation)

	// Adding a new running tool while collapsed: animation restarts.
	turn.AddToolCall("c2", "search", `{}`, "")
	assert.NotNil(t, turn.animation)

	// Expanding: animation stops.
	turn.SetCollapseMode(collapseModeExpanded)
	assert.Nil(t, turn.animation)
}

func TestTurnAnimationNilInterrupter(t *testing.T) {
	turn := NewTurn(&ComponentConfig{}, nil)
	turn.AddToolCall("c1", "read_file", `{}`, "a.go")
	turn.SetCollapseMode(collapseModeCollapsed)
	// No panic, no animation.
	assert.Nil(t, turn.animation)
}

func TestTurnClose(t *testing.T) {
	interrupter := term.FuncInterrupter(func(context.Context) error { return nil })
	turn := NewTurn(&ComponentConfig{}, interrupter)
	turn.AddToolCall("c1", "read_file", `{}`, "a.go")
	turn.SetCollapseMode(collapseModeCollapsed)
	assert.NotNil(t, turn.animation)

	turn.Close()
	assert.Nil(t, turn.animation)
}

func TestTurnCollapsedSpinner(t *testing.T) {
	turn := newTestTurn()
	turn.AddToolCall("c1", "read_file", `{}`, "a.go")
	turn.SetCollapseMode(collapseModeCollapsed)
	turn.Resize(30, 1)

	// Draw twice and verify the spinner frame advances.
	w1 := term.NewStringWriter(30, 1)
	turn.Draw(w1)
	_ = w1.Flush()
	got1 := w1.String()

	w2 := term.NewStringWriter(30, 1)
	turn.Draw(w2)
	_ = w2.Flush()
	got2 := w2.String()

	assert.Contains(t, got1, "└─ ⠋ read_file a.go")
	assert.Contains(t, got2, "└─ ⠙ read_file a.go")
	assert.NotEqual(t, got1, got2)
}

func TestTurnCollapsedSpinnerCompleted(t *testing.T) {
	turn := newTestTurn()
	turn.AddToolCall("c1", "read_file", `{}`, "a.go")
	turn.AddToolCall("c2", "search", `{}`, "b.go")
	turn.CompleteToolCall("c1", "read_file", `{}`, "a.go", "", false)
	turn.SetCollapseMode(collapseModeCollapsed)
	turn.Resize(30, 2)

	w := term.NewStringWriter(30, 2)
	turn.Draw(w)
	_ = w.Flush()
	got := w.String()

	// Completed tool uses ✓, running tool uses spinner
	assert.Contains(t, got, "├─ ✓ read_file a.go")
	assert.Contains(t, got, "└─ ⠋ search b.go")
}

func TestTurnCollapsedDraw(t *testing.T) {
	turn := newTestTurn()
	turn.AddToolCall("c1", "read_file", `{}`, "a.go")
	turn.CompleteToolCall("c1", "read_file", `{}`, "a.go", "data", false)
	turn.AddToolCall("c2", "search", `{"pattern":"foo"}`, "")
	turn.CompleteToolCall("c2", "search", `{"pattern":"foo"}`, "", "not found", true)

	turn.SetCollapseMode(collapseModeCollapsed)
	turn.Resize(30, 2)

	w := term.NewStringWriter(30, 2)
	turn.Draw(w)
	_ = w.Flush()

	got := w.String()
	assert.Contains(t, got, "├─ ✓ read_file a.go")
	assert.Contains(t, got, "└─ ✗ search")
	assert.Contains(t, got, "pattern=foo")
}

func TestTurnCollapsedDrawBashShowsCommand(t *testing.T) {
	turn := newTestTurn()
	turn.Resize(50, 4)
	w := term.NewStringWriter(50, 4)

	comptest.TestComponent(t, turn, w, []comptest.TestCase{
		{
			Action: func() {
				turn.AddToolCall("c1", "bash",
					`{"command":"go test ./...","description":"Run all tests"}`,
					"go test ./...")
				turn.CompleteToolCall("c1", "bash",
					`{"command":"go test ./...","description":"Run all tests"}`,
					"go test ./...", "PASS", false)
				turn.AddToolCall("c2", "bash",
					`{"command":"cat README.md","description":"Read readme"}`,
					"cat README.md")
				turn.CompleteToolCall("c2", "bash",
					`{"command":"cat README.md","description":"Read readme"}`,
					"cat README.md", "", true)
				turn.SetCollapseMode(collapseModeCollapsed)
			},
			// Collapsed mode shows command (summary), not description.
			Expected: "├─ ✓ bash go test ./...                           \n" +
				"└─ ✗ bash cat README.md                           \n" +
				"                                                  \n" +
				"                                                  ",
		},
	})
}

func TestTurnCollapsedDrawNested(t *testing.T) {
	turn := newTestTurn()
	turn.AddToolCall("c1", "read_file", `{}`, "a.go")
	turn.CompleteToolCall("c1", "read_file", `{}`, "a.go", "", false)
	turn.AddToolCall("p1", "spawn_agent", `{}`, "refactor")
	turn.AddChildToolCall("p1", "ch1", "read_file", `{}`, "auth.go")
	turn.CompleteChildToolCall("p1", "ch1", "read_file", `{}`, "auth.go", "", false)
	turn.AddChildToolCall("p1", "ch2", "edit_file", `{}`, "auth.go")
	turn.CompleteChildToolCall("p1", "ch2", "edit_file", `{}`, "auth.go", "", false)
	turn.CompleteToolCall("p1", "spawn_agent", `{}`, "refactor", "", false)
	turn.AddToolCall("c3", "search", `{}`, "pattern")
	turn.CompleteToolCall("c3", "search", `{}`, "pattern", "", true)

	turn.SetCollapseMode(collapseModeCollapsed)
	turn.Resize(40, 10)

	w := term.NewStringWriter(40, 10)
	turn.Draw(w)
	_ = w.Flush()

	got := w.String()
	// Parent tree structure
	assert.Contains(t, got, "├─ ✓ read_file a.go")
	assert.Contains(t, got, "├─ ✓ spawn_agent refactor")
	assert.Contains(t, got, "└─ ✗ search pattern")
	// Children are indented under spawn_agent
	assert.Contains(t, got, "├─ ✓ read_file auth.go")
	assert.Contains(t, got, "└─ ✓ edit_file auth.go")
	// Continuation line for parent (│) since spawn_agent is not last
	assert.Contains(t, got, "│")
}

func TestTurnCollapsedDrawDeepNest(t *testing.T) {
	turn := newTestTurn()
	turn.AddToolCall("p1", "agent", `{}`, "outer")
	turn.AddChildToolCall("p1", "c1", "agent", `{}`, "inner")
	// grandchild: manually build the nested turn
	childTurn := turn.tools["p1"].childTurn
	childTurn.tools["c1"].childTurn = NewTurn(childTurn.cfg, nil)
	childTurn.tools["c1"].childTurn.AddToolCall("g1", "read_file", `{}`, "deep.go")
	childTurn.tools["c1"].childTurn.CompleteToolCall("g1", "read_file", `{}`, "deep.go", "", false)
	turn.CompleteChildToolCall("p1", "c1", "agent", `{}`, "inner", "", false)
	turn.CompleteToolCall("p1", "agent", `{}`, "outer", "", false)

	turn.SetCollapseMode(collapseModeCollapsed)
	turn.Resize(50, 10)

	w := term.NewStringWriter(50, 10)
	turn.Draw(w)
	_ = w.Flush()

	got := w.String()
	assert.Contains(t, got, "└─ ✓ agent outer")
	assert.Contains(t, got, "└─ ✓ agent inner")
	assert.Contains(t, got, "└─ ✓ read_file deep.go")
	// 3 levels: root → child → grandchild
	assert.Equal(t, 3, turn.collapsedHeight())
}

func TestTurnCollapseModeCycle(t *testing.T) {
	assert.Equal(t, collapseModeExpanded, collapseModeCollapsed.next())
	assert.Equal(t, collapseModeCollapsed, collapseModeExpanded.next())
}

func TestTurnSetCollapseModeRecurse(t *testing.T) {
	turn := newTestTurn()
	turn.AddToolCall("p1", "agent", `{}`, "")
	turn.AddChildToolCall("p1", "c1", "read_file", `{}`, "")

	turn.SetCollapseMode(collapseModeCollapsed)
	assert.Equal(t, collapseModeCollapsed, turn.mode)
	assert.Equal(t, collapseModeCollapsed, turn.tools["p1"].childTurn.mode)

	turn.SetCollapseMode(collapseModeExpanded)
	assert.Equal(t, collapseModeExpanded, turn.mode)
	assert.Equal(t, collapseModeExpanded, turn.tools["p1"].childTurn.mode)
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"},
		{500 * time.Microsecond, "500µs"},
		{999 * time.Microsecond, "999µs"},
		{500 * time.Millisecond, "500ms"},
		{999 * time.Millisecond, "999ms"},
		{1 * time.Second, "1s"},
		{1200 * time.Millisecond, "1s"},
		{1500 * time.Millisecond, "2s"},
		{9999 * time.Millisecond, "10s"},
		{10 * time.Second, "10s"},
		{45 * time.Second, "45s"},
		{59 * time.Second, "59s"},
		{60 * time.Second, "1m0s"},
		{62 * time.Second, "1m2s"},
		{125 * time.Second, "2m5s"},
	}
	for _, tt := range tests {
		t.Run(tt.d.String(), func(t *testing.T) {
			assert.Equal(t, tt.want, formatDuration(tt.d, 0))
		})
	}
}

func TestTurnSetToolStartTime(t *testing.T) {
	turn := newTestTurn()
	now := time.Now()
	turn.AddToolCall("c1", "read_file", `{}`, "")
	turn.SetToolStartTime("c1", now)
	assert.Equal(t, now, turn.tools["c1"].startTime)
}

func TestTurnSetToolStartTimeChild(t *testing.T) {
	turn := newTestTurn()
	now := time.Now()
	turn.AddToolCall("p1", "agent", `{}`, "")
	turn.AddChildToolCall("p1", "c1", "read_file", `{}`, "")
	turn.SetToolStartTime("c1", now)
	assert.Equal(t, now, turn.tools["p1"].childTurn.tools["c1"].startTime)
}

func TestTurnSetToolDuration(t *testing.T) {
	turn := newTestTurn()
	dur := 2 * time.Second
	turn.AddToolCall("c1", "read_file", `{}`, "")
	turn.SetToolDuration("c1", dur)
	assert.Equal(t, dur, turn.tools["c1"].duration)
}

func TestTurnSetToolDurationChild(t *testing.T) {
	turn := newTestTurn()
	dur := 3 * time.Second
	turn.AddToolCall("p1", "agent", `{}`, "")
	turn.AddChildToolCall("p1", "c1", "read_file", `{}`, "")
	turn.SetToolDuration("c1", dur)
	assert.Equal(t, dur, turn.tools["p1"].childTurn.tools["c1"].duration)
}

func TestTurnCollapsedDrawWithDuration(t *testing.T) {
	comp := NewComponent(ComponentConfig{StartCollapsed: true})
	comp.Resize(30, 10)
	w := term.NewStringWriter(31, 11)

	tests := []comptest.TestCase{
		{
			Action: func() {
				comp.AddToolCall("c1", "read_file", `{}`, "a.go")
				comp.SetToolDuration("c1", 1200*time.Millisecond)
				comp.CompleteToolCall("c1", "read_file", `{}`, "a.go", "data", false)
			},
			Expected: "└─ ✓ read_file 1s a.go         \n" +
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
	}
	comptest.TestComponent(t, comp, w, tests)
}

func TestTurnCollapsedDrawWithDurationError(t *testing.T) {
	comp := NewComponent(ComponentConfig{StartCollapsed: true})
	comp.Resize(30, 10)
	w := term.NewStringWriter(31, 11)

	tests := []comptest.TestCase{
		{
			Action: func() {
				comp.AddToolCall("c1", "search", `{}`, "pattern")
				comp.SetToolDuration("c1", 45*time.Second)
				comp.CompleteToolCall("c1", "search", `{}`, "pattern", "not found", true)
			},
			Expected: "└─ ✗ search 45s pattern        \n" +
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
	}
	comptest.TestComponent(t, comp, w, tests)
}

func TestTurnCollapsedDrawNoDurationWhenZero(t *testing.T) {
	comp := NewComponent(ComponentConfig{StartCollapsed: true})
	comp.Resize(30, 10)
	w := term.NewStringWriter(31, 11)

	tests := []comptest.TestCase{
		{
			Action: func() {
				comp.AddToolCall("c1", "read_file", `{}`, "a.go")
				comp.CompleteToolCall("c1", "read_file", `{}`, "a.go", "data", false)
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
	}
	comptest.TestComponent(t, comp, w, tests)
}

func TestTurnExpandedNoDuration(t *testing.T) {
	comp := NewComponent(ComponentConfig{})
	comp.Resize(30, 10)
	w := term.NewStringWriter(31, 11)

	tests := []comptest.TestCase{
		{
			Action: func() {
				comp.AddToolCall("c1", "read_file", `{}`, "summary")
				comp.SetToolDuration("c1", 5*time.Second)
				comp.CompleteToolCall("c1", "read_file", `{}`, "summary", "ok", false)
			},
			Expected: "✓ read_file summary            \n" +
				"ok                             \n" +
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

func TestTurnMarkDropped(t *testing.T) {
	turn := newTestTurn()
	turn.AddToolCall("c1", "read_file", `{}`, "a.go")
	turn.CompleteToolCall("c1", "read_file", `{}`, "a.go", "data", false)

	turn.MarkDropped([]string{"c1"})

	assert.True(t, turn.tools["c1"].dropped)
	assert.Equal(t, defaultDroppedAttr, turn.tools["c1"].header.prefixAttr)
}

func TestTurnMarkDroppedChild(t *testing.T) {
	turn := newTestTurn()
	turn.AddToolCall("p1", "spawn_agent", `{}`, "")
	turn.AddChildToolCall("p1", "c1", "read_file", `{}`, "a.go")
	turn.CompleteChildToolCall("p1", "c1", "read_file", `{}`, "a.go", "data", false)

	turn.MarkDropped([]string{"c1"})

	child := turn.tools["p1"].childTurn.tools["c1"]
	assert.True(t, child.dropped)
	assert.Equal(t, defaultDroppedAttr, child.header.prefixAttr)
}

func TestTurnCollapsedDrawDropped(t *testing.T) {
	comp := NewComponent(ComponentConfig{StartCollapsed: true})
	comp.Resize(30, 10)
	w := term.NewStringWriter(31, 11)

	tests := []comptest.TestCase{
		{
			Action: func() {
				comp.AddToolCall("c1", "read_file", `{}`, "a.go")
				comp.CompleteToolCall("c1", "read_file", `{}`, "a.go", "data", false)
				comp.MarkToolsDropped([]string{"c1"})
			},
			// Glyph stays ✓ but rendered with gray (droppedAttr).
			// Visually same characters; the attr is verified by
			// the unit test above.
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
	}
	comptest.TestComponent(t, comp, w, tests)
}

func TestTurnMarkDroppedExpandedHeader(t *testing.T) {
	turn := newTestTurn()
	turn.AddToolCall("c1", "read_file", `{}`, "a.go")
	turn.CompleteToolCall("c1", "read_file", `{}`, "a.go", "data", false)

	// Before marking, header has success attr.
	assert.Equal(t, defaultSuccessAttr, turn.tools["c1"].header.prefixAttr)

	turn.MarkDropped([]string{"c1"})

	// After marking, header has dropped attr (gray).
	assert.Equal(t, defaultDroppedAttr, turn.tools["c1"].header.prefixAttr)
}

func TestTurnAddChildResult(t *testing.T) {
	turn := newTestTurn()
	turn.AddToolCall("parent", "agent", `{}`, "explore")
	turn.AddChildToolCall("parent", "ch1", "read_file", `{}`, "a.go")
	turn.CompleteChildToolCall("parent", "ch1", "read_file", `{}`, "a.go", "data", false)

	turn.AddChildResult("parent", "The exploration is complete.", false)

	parent := turn.tools["parent"]
	assert.NotNil(t, parent.childTurn)
	resultNode := parent.childTurn.tools["parent:result"]
	assert.NotNil(t, resultNode)
	assert.True(t, resultNode.done)
	assert.True(t, resultNode.isResult)
	assert.False(t, resultNode.isError)
	assert.Equal(t, "The exploration is complete.", resultNode.name)
}

func TestTurnAddChildResultError(t *testing.T) {
	turn := newTestTurn()
	turn.AddToolCall("parent", "agent", `{}`, "explore")

	turn.AddChildResult("parent", "sub-agent error: timeout", true)

	parent := turn.tools["parent"]
	assert.NotNil(t, parent.childTurn)
	resultNode := parent.childTurn.tools["parent:result"]
	assert.NotNil(t, resultNode)
	assert.True(t, resultNode.done)
	assert.True(t, resultNode.isResult)
	assert.True(t, resultNode.isError)
}

func TestTurnAddChildResultNoParent(t *testing.T) {
	turn := newTestTurn()

	// No parent — should be a no-op.
	turn.AddChildResult("nonexistent", "some output", false)

	assert.Empty(t, turn.tools)
}

func TestTurnAddChildResultCollapsed(t *testing.T) {
	turn := newTestTurn()
	turn.AddToolCall("p1", "agent", `{}`, "explore")
	turn.AddChildToolCall("p1", "ch1", "read_file", `{}`, "a.go")
	turn.CompleteChildToolCall("p1", "ch1", "read_file", `{}`, "a.go", "", false)
	turn.AddChildResult("p1", "Done exploring the code.", false)
	turn.CompleteToolCall("p1", "agent", `{}`, "explore", "Done exploring the code.", false)

	turn.SetCollapseMode(collapseModeCollapsed)
	turn.Resize(60, 10)

	w := term.NewStringWriter(60, 10)
	turn.Draw(w)
	_ = w.Flush()

	got := w.String()
	// Parent tool
	assert.Contains(t, got, "└─ ✓ agent explore")
	// Child tool
	assert.Contains(t, got, "├─ ✓ read_file a.go")
	// Result leaf node with success icon
	assert.Contains(t, got, "└─ 󰆈  Done exploring the code.")
}

func TestTurnAddChildResultCollapsedError(t *testing.T) {
	turn := newTestTurn()
	turn.AddToolCall("p1", "agent", `{}`, "explore")
	turn.AddChildResult("p1", "something failed", true)
	turn.CompleteToolCall("p1", "agent", `{}`, "explore", "", true)

	turn.SetCollapseMode(collapseModeCollapsed)
	turn.Resize(60, 10)

	w := term.NewStringWriter(60, 10)
	turn.Draw(w)
	_ = w.Flush()

	got := w.String()
	// Result leaf node with error icon
	assert.Contains(t, got, "└─ 󰅽  something failed")
}

func TestTurnCollapsedHeightWithChildResult(t *testing.T) {
	turn := newTestTurn()
	turn.AddToolCall("p1", "agent", `{}`, "explore")
	turn.AddChildToolCall("p1", "ch1", "read_file", `{}`, "a.go")
	turn.CompleteChildToolCall("p1", "ch1", "read_file", `{}`, "a.go", "", false)
	turn.AddChildResult("p1", "done", false)

	// 1 parent + 2 children (ch1 + result)
	assert.Equal(t, 3, turn.collapsedHeight())
}

func TestTruncateFirstLine(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"single line", "hello world", "hello world"},
		{"multi line", "first line\nsecond line\nthird", "first line"},
		{"empty", "", ""},
		{"long line", string(make([]rune, 100)), string(make([]rune, 60)) + "..."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateFirstLine(tt.input)
			assert.Equal(t, tt.expected, got)
		})
	}
}

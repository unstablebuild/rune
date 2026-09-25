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

package starlarktutorial

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unstablebuild/rune-go-sdk/component/comptest"
	"github.com/unstablebuild/rune-go-sdk/term"

	"unstable.build/rune/internal/ide/idetutorial"

	"unstable.build/rune/internal/handler/command"
	"unstable.build/rune/internal/handler/handlertest"
)

// attrGridWriter captures both runes and term.Attributes per cell so
// tests can assert AttrReverse on the title bar.
type attrGridWriter struct {
	w, h  int
	cells [][]term.Cell
}

func newAttrGridWriter(w, h int) *attrGridWriter {
	cells := make([][]term.Cell, h)
	for i := range cells {
		cells[i] = make([]term.Cell, w)
	}
	return &attrGridWriter{w: w, h: h, cells: cells}
}

func (g *attrGridWriter) SetCell(pos term.Coordinates, c term.Cell) {
	if pos.X < 0 || pos.Y < 0 || pos.X >= g.w || pos.Y >= g.h {
		return
	}
	g.cells[pos.Y][pos.X] = c
}

func (g *attrGridWriter) Context() context.Context                              { return context.Background() }
func (g *attrGridWriter) UnionAttributes(_ term.Coordinates, _ term.Attributes) {}
func (g *attrGridWriter) DrawImage(term.Image) bool                             { return false }

func (g *attrGridWriter) rowRunes(y int) string {
	if y < 0 || y >= g.h {
		return ""
	}
	out := make([]rune, g.w)
	for x, c := range g.cells[y] {
		if c.Ch == 0 {
			out[x] = ' '
		} else {
			out[x] = c.Ch
		}
	}
	return string(out)
}

func (g *attrGridWriter) rowAttrs(y int) []term.Attributes {
	if y < 0 || y >= g.h {
		return nil
	}
	out := make([]term.Attributes, g.w)
	for x, c := range g.cells[y] {
		out[x] = c.Attributes()
	}
	return out
}

// TestFloatingWindowContentPadding asserts that a screen body is
// anchored at the top, gutted by one column on each side, and reflows
// inside what is left, so no glyph ever touches the tile's frame.
func TestFloatingWindowContentPadding(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		body     string
		expected string
	}{
		{
			name: "short body",
			body: "body",
			expected: " body       \n" +
				"            \n" +
				"            \n" +
				"            \n" +
				"            ",
		},
		{
			name: "body reflows inside the reserved column",
			body: "0123456789AB",
			expected: " 0123456789 \n" +
				" AB         \n" +
				"            \n" +
				"            \n" +
				"            ",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			md, ok := newScreenMarkdown(tc.body)
			require.True(t, ok)
			_, content := newScreenContent(md)

			w := term.NewStringWriter(12, 5)
			comptest.TestComponent(t, content, w, []comptest.TestCase{
				{
					Action:   func() { content.Resize(12, 5) },
					Expected: tc.expected,
				},
			})
		})
	}
}

func TestFloatingWindowContentHandlerSequence(t *testing.T) {
	t.Parallel()
	md, ok := newScreenMarkdown("Line1\n\nLine2\n\nLine3\n\nLine4")
	require.True(t, ok)
	_, content := newScreenContent(md)

	cases := []handlertest.SingleTestCase{
		{
			Event: term.Event{Type: term.EventKey, Ch: 'k'},
			Expected: " Line1      \n" +
				"            \n" +
				" Line2      \n" +
				"            \n" +
				" Line3      ",
		},
		{
			Event: term.Event{Type: term.EventKey, Ch: 'j'},
			Expected: "            \n" +
				" Line2      \n" +
				"            \n" +
				" Line3      \n" +
				"            ",
		},
		{
			Event: term.Event{Type: term.EventKey, Key: term.KeyArrowDown},
			Expected: " Line2      \n" +
				"            \n" +
				" Line3      \n" +
				"            \n" +
				" Line4      ",
		},
		{
			Event: term.Event{Type: term.EventKey, Ch: 'k'},
			Expected: "            \n" +
				" Line2      \n" +
				"            \n" +
				" Line3      \n" +
				"            ",
		},
	}

	sequence := make([]handlertest.SequenceTestCase, 0, len(cases))
	for _, tc := range cases {
		sequence = append(sequence, handlertest.SequenceTestCase{
			InputSequence: (term.KeyComb{
				Ch: tc.Event.Ch, Mod: tc.Event.Mod, Key: tc.Event.Key,
			}).String(),
			Expected: tc.Expected,
		})
	}

	handlertest.RunHandlerSequence(t, content, 12, 5, sequence)
}

// TestWaitCommandHintRendersInBody asserts that the wait_command hint
// renders its markdown body into the tile body. The prefix line
// ("Waiting for you …") must be present and the configured command
// key must be expanded in place of the `<cmd>` token.
func TestWaitCommandHintRendersInBody(t *testing.T) {
	t.Parallel()
	src := `
def run():
    wait_command(command="wopen")
tutorial(entry=run)
`
	tut, _ := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	defer tut.Stop()

	g := newAttrGridWriter(bodyW, bodyH)
	tut.Draw(g)

	// The body must include the prefix line with the expanded
	// command key and the bare command name, and must not leak the
	// raw `<cmd>` template token.
	wantKey := PrettyKeySpec((term.KeyComb{Ch: ':'}).String())
	combined := gridText(g)
	assert.Contains(t, combined, wantKey,
		"wait_command hint must mention the configured command key")
	assert.Contains(t, combined, "wopen",
		"wait_command hint must mention the command name")
	assert.NotContains(t, combined, "<cmd>",
		"`<cmd>` template token must not leak into the rendered body")
}

// gridText returns the concatenated rows of g separated by newlines
// so callers can assert substring presence anywhere in the rendered
// frame.
func gridText(g *attrGridWriter) string {
	var b strings.Builder
	for y := range g.h {
		b.WriteString(g.rowRunes(y))
		b.WriteByte('\n')
	}
	return b.String()
}

// TestWaitCommandHintIncludesManual asserts that when a command
// manual lookup is provided, the wait_command hint window also
// renders the command's usage and summary as markdown.
func TestWaitCommandHintIncludesManual(t *testing.T) {
	t.Parallel()
	man := command.Manual{
		Name:     "wopen",
		Synopsis: "<directory>",
		Summary:  "Open the workspace at the given directory.",
	}
	lookup := func(name string) (command.Manual, bool) {
		if name == "wopen" {
			return man, true
		}
		return command.Manual{}, false
	}
	src := `
def run():
    wait_command(command="wopen")
tutorial(entry=run)
`
	tut, err := New(
		"manual-test", src,
		idetutorial.PromptStyle{}, nil, nil, nil,
		nil, nil, term.KeyComb{Ch: ':'},
		"standard", "", nil,
		lookup,
		nil,
		nil,
	)
	require.NoError(t, err)

	// A tall body so the whole manual fits beneath the instruction
	// line without scrolling.
	const bodyWidth, bodyHeight = 40, 60
	tut.Resize(bodyWidth, bodyHeight)
	resetAndWait(t, tut, time.Second)
	defer tut.Stop()

	g := newAttrGridWriter(bodyWidth, bodyHeight)
	tut.Draw(g)
	body := gridText(g)

	assert.Contains(t, body, "Usage",
		"hint must include a Usage section from the manual")
	assert.Contains(t, body, "<directory>",
		"hint must include the manual's synopsis")
	assert.Contains(t, body, "Open the workspace",
		"hint must include the manual's summary")
}

// TestWaitCommandHintIncludesBoundKey asserts that when the awaited
// command has a key binding, the default hint tells the user they can
// press that key to run it.
func TestWaitCommandHintIncludesBoundKey(t *testing.T) {
	t.Parallel()
	keyFor := func(cmd string, args []string) string {
		if cmd == "windownew" && len(args) == 0 {
			return "<meta-n>"
		}
		return ""
	}
	src := `
def run():
    wait_command(command="windownew")
tutorial(entry=run)
`
	tut, err := New(
		"boundkey-test", src,
		idetutorial.PromptStyle{}, nil, nil, nil,
		nil, nil, term.KeyComb{Ch: ':'},
		"standard", "", keyFor,
		nil,
		nil,
		nil,
	)
	require.NoError(t, err)

	const bodyWidth, bodyHeight = 40, 60
	tut.Resize(bodyWidth, bodyHeight)
	resetAndWait(t, tut, time.Second)
	defer tut.Stop()

	g := newAttrGridWriter(bodyWidth, bodyHeight)
	tut.Draw(g)
	body := gridText(g)

	assert.Contains(t, body, "windownew",
		"hint must name the awaited command")
	assert.Contains(t, body, "<meta-n>",
		"hint must mention the command's bound key")
}

// TestWaitCommandArgsSuppressBoundKey is a regression test for the
// `! git log` lesson: `!` alone is bound to the companion terminal,
// so offering that key as a way to "run it" sends the user somewhere
// the step is not asking for. An argument-qualified awaited command
// resolves the key for that exact invocation, and still matches on
// the command name alone.
func TestWaitCommandArgsSuppressBoundKey(t *testing.T) {
	t.Parallel()
	keyFor := func(cmd string, args []string) string {
		if cmd == "!" && len(args) == 0 {
			return "<shift-meta-enter>"
		}
		return ""
	}
	src := `
def run():
    wait_command(command="! git log")
tutorial(entry=run)
`
	tut, err := New(
		"bang-args-test", src,
		idetutorial.PromptStyle{}, nil, nil, nil,
		nil, nil, term.KeyComb{Ch: ':'},
		"standard", "", keyFor,
		nil,
		nil,
		nil,
	)
	require.NoError(t, err)

	const bodyWidth, bodyHeight = 40, 60
	tut.Resize(bodyWidth, bodyHeight)
	resetAndWait(t, tut, time.Second)
	defer tut.Stop()

	g := newAttrGridWriter(bodyWidth, bodyHeight)
	tut.Draw(g)
	body := gridText(g)

	assert.Contains(t, body, "! git log",
		"hint must name the invocation the step asks for")
	assert.NotContains(t, body, "<shift-meta-enter>",
		"a key bound to the bare command runs something else")

	assert.True(t, tut.ObserveCommand("!", "!", []string{"git", "log"}, nil),
		"the step matches on the command name alone and is the last one")
}

func TestWaitCommandArgsIncludeExactBoundKey(t *testing.T) {
	t.Parallel()
	keyFor := func(cmd string, args []string) string {
		if cmd == "windownew" && slices.Equal(args, []string{"right"}) {
			return "<meta-r>"
		}
		return ""
	}
	r := &request{command: "windownew right"}

	hint := buildWaitCommandHint(r, "<alt-x>", nil, keyFor)

	assert.Contains(t, hint, "windownew right")
	assert.Contains(t, hint, "<meta-r>")
}

// TestWaitCommandTextRendersAsWritten asserts a step that declares its
// own copy gets exactly that copy: nothing generated is added around
// it, so the author owns what the user reads.
func TestWaitCommandTextRendersAsWritten(t *testing.T) {
	t.Parallel()
	keyFor := func(string, []string) string { return "<meta-r>" }
	lookup := func(name string) (command.Manual, bool) {
		return command.Manual{Name: name, Summary: "generated"}, true
	}
	r := &request{command: "windownew right", text: "Split with `<cmd>`."}

	assert.Equal(t, "Split with `:`.",
		buildWaitCommandHint(r, ":", lookup, keyFor))
}

// TestWaitCommandTextReplacesManualUpFront is a regression test for a
// lesson that asks for two directions of the same command in a row:
// once the first was dispatched, the second step fell back to the
// generic "try the <command> command" hint plus its manual, which
// cannot say which direction is still due. A step's own text must
// show before any failed dispatch.
func TestWaitCommandTextReplacesManualUpFront(t *testing.T) {
	t.Parallel()
	lookup := func(name string) (command.Manual, bool) {
		return command.Manual{
			Name:     name,
			Synopsis: "(right|left|up|down)",
			Summary:  "Switch focus to the window on the given side.",
		}, true
	}
	src := `
def run():
    wait_command(command="windowfocus", text="Now focus the editor on the left.")
tutorial(entry=run)
`
	tut, err := New(
		"hint-text-test", src,
		idetutorial.PromptStyle{}, nil, nil, nil,
		nil, nil, term.KeyComb{Ch: ':'},
		"standard", "", nil,
		lookup,
		nil,
		nil,
	)
	require.NoError(t, err)

	const bodyWidth, bodyHeight = 40, 60
	tut.Resize(bodyWidth, bodyHeight)
	resetAndWait(t, tut, time.Second)
	defer tut.Stop()

	g := newAttrGridWriter(bodyWidth, bodyHeight)
	tut.Draw(g)
	body := gridText(g)

	assert.Contains(t, body, "Now focus the editor on the left.",
		"the step's own instruction must show before any failed dispatch")
	assert.NotContains(t, body, "Switch focus to the window on the given side.",
		"the generic manual must not stand in for the step's instruction")
}

// TestWaitShellHintShowsStepText asserts a console step shows its own
// instruction when it has one, and names the command to run otherwise.
func TestWaitShellHintShowsStepText(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "own text",
			src:  `wait_shell(args=["pkg", "install", "rune-agent"], text="Type it and press Enter.")`,
			want: "Type it and press Enter.",
		},
		{
			name: "generated",
			src:  `wait_shell(args=["pkg", "install", "rune-agent"])`,
			want: "pkg install rune-agent",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tut, _ := newTutorial(t, "def run():\n    "+tc.src+"\ntutorial(entry=run)\n")
			// A body wide enough for the instruction to render on a
			// single row, so the assertion reads the copy, not the wrapping.
			const bodyWidth, bodyHeight = 40, 24
			tut.Resize(bodyWidth, bodyHeight)
			resetAndWait(t, tut, time.Second)
			defer tut.Stop()

			g := newAttrGridWriter(bodyWidth, bodyHeight)
			tut.Draw(g)
			assert.Contains(t, gridText(g), tc.want)
		})
	}

	tut, _ := newTutorial(t, `
def run():
    wait_shell(args=["pkg", "install", "rune-agent"])
tutorial(entry=run)
`)
	resetAndWait(t, tut, time.Second)
	defer tut.Stop()

	handled, _ := tut.Handle(term.Event{Type: term.EventKey, Ch: 'p'})
	assert.False(t, handled,
		"a console step must let the user type the command")
}

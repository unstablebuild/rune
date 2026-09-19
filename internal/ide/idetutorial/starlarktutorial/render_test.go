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
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/component/comptest"
	"github.com/unstablebuild/rune-go-sdk/term"

	"unstable.build/rune/internal/browser"
	tuicomp "unstable.build/rune/internal/component"
	"unstable.build/rune/internal/handler/command"
	"unstable.build/rune/internal/handler/handlertest"
	"unstable.build/rune/internal/ide/idetutorial"
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

// drawProbe builds a Tutorial that publishes a single floating_window
// with the given title/text, advances to the active request, and
// renders it into g through the overlay browser.
func drawProbe(t *testing.T, title, text string, w, h int) (*Tutorial, *attrGridWriter) {
	t.Helper()
	titleArg := ""
	if title != "" {
		titleArg = ", title=\"" + title + "\""
	}
	src := "def run():\n" +
		"    floating_window(text=\"" + text + "\"" + titleArg + ")\n" +
		"tutorial(entry=run)\n"
	tut, _ := newTutorial(t, src)
	tut.Resize(w, h)
	resetAndWait(t, tut, time.Second)
	g := newAttrGridWriter(w, h)
	tut.Draw(g)
	tut.winOverlay.Draw(g)
	return tut, g
}

func TestFloatingWindowContentPadding(t *testing.T) {
	t.Parallel()
	md, ok := newHintMarkdown("body")
	require.True(t, ok)
	content := newFloatingWindowContent(md, 80, 24, nil, nil)

	w := term.NewStringWriter(12, 5)
	comptest.TestComponent(t, content, w, []comptest.TestCase{
		{
			Action: func() { content.Resize(12, 5) },
			Expected: "            \n" +
				" body       \n" +
				"            \n" +
				"            \n" +
				"            ",
		},
	})
}

func TestFloatingWindowContentHandlerSequence(t *testing.T) {
	t.Parallel()
	md, ok := newHintMarkdown("Line1\n\nLine2\n\nLine3\n\nLine4")
	require.True(t, ok)
	content := newFloatingWindowContent(md, 80, 24, nil, nil)

	cases := []handlertest.SingleTestCase{
		{
			Event: term.Event{Type: term.EventKey, Ch: 'k'},
			Expected: "            \n" +
				" Line1      \n" +
				"            \n" +
				" Line2      \n" +
				"            ",
		},
		{
			Event: term.Event{Type: term.EventKey, Ch: 'j'},
			Expected: "            \n" +
				"            \n" +
				" Line2      \n" +
				"            \n" +
				" Line3      ",
		},
		{
			Event: term.Event{Type: term.EventKey, Key: term.KeyArrowDown},
			Expected: "            \n" +
				" Line2      \n" +
				"            \n" +
				" Line3      \n" +
				"            ",
		},
		{
			Event: term.Event{Type: term.EventKey, Ch: 'k'},
			Expected: "            \n" +
				"            \n" +
				" Line2      \n" +
				"            \n" +
				" Line3      ",
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

// activeWindowRect returns the screen-space rectangle of the active
// request's overlay window.
func activeWindowRect(t *testing.T, tut *Tutorial) (term.Coordinates, int, int) {
	t.Helper()
	tut.mu.Lock()
	r := tut.active
	tut.mu.Unlock()
	require.NotNil(t, r, "expected an active request")
	pos, w, h, ok := tut.winOverlay.WindowRect(r.win)
	require.True(t, ok, "active request must have a live window")
	return pos, w, h
}

// findTopLeftFrame finds the first row that starts with a vertical
// edge glyph (└, │, ┌) at any X, returns (frameStartX, frameY) where
// the side edge is observed. Returns (-1, -1) when no frame found.
func findFrameSideX(g *attrGridWriter) (int, int) {
	fcs := component.FrameCharSetDefault()
	for y := range g.h {
		row := g.rowRunes(y)
		for x, r := range row {
			if r == fcs.VerticalLeft {
				return x, y
			}
		}
	}
	return -1, -1
}

// TestComponentAtMatchesDrawnOverlay asserts that the overlay browser
// reports coverage exactly for coordinates inside the step window, so
// the host hides the root cursor only when the window actually covers
// it, and reports nothing once the tutorial finishes (the window is
// closed on resolve).
func TestComponentAtMatchesDrawnOverlay(t *testing.T) {
	t.Parallel()
	tut, g := drawProbe(t, "Welcome", "body line", 80, 24)
	defer tut.Stop()

	sideX, sideY := findFrameSideX(g)
	require.NotEqual(t, -1, sideX,
		"expected a left frame edge somewhere in the rendered grid")
	assert.True(t, tut.winOverlay.Covers(term.Coordinates{X: sideX, Y: sideY}),
		"frame edge must be covered by the overlay window")
	inside := term.Coordinates{X: sideX + 5, Y: sideY}
	assert.True(t, tut.winOverlay.Covers(inside),
		"window interior must be covered by the overlay window")
	assert.False(t, tut.winOverlay.Covers(term.Coordinates{X: sideX - 1, Y: sideY}),
		"cell left of the window must not be covered")
	assert.False(t, tut.winOverlay.Covers(term.Coordinates{X: 0, Y: 23}),
		"cell below the window must not be covered")
	_, ok := tut.ComponentAt(term.Coordinates{X: sideX, Y: sideY})
	assert.False(t, ok,
		"floating windows are hosted by the browser, not the bespoke overlay")

	_, _ = tut.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	waitFinished(t, tut, time.Second)
	assert.False(t, tut.winOverlay.Covers(inside),
		"a finished tutorial must leave no overlay window behind")
	assert.Zero(t, tut.winOverlay.Windows(),
		"resolving the last step must close its window")
}

// TestComponentAtReturnsPromptForChoiceOverlay asserts that a
// confirm/choice step opens a prompt window on the overlay browser
// covering the centre of the screen, and that the bespoke overlay
// reports nothing there.
func TestComponentAtReturnsPromptForChoiceOverlay(t *testing.T) {
	t.Parallel()
	src := `
def run():
    pick = choice(message="pick one", options=["A", "B"])
tutorial(entry=run)
`
	tut, _ := newTutorial(t, src)
	tut.Resize(80, 24)
	resetAndWait(t, tut, time.Second)
	defer tut.Stop()
	require.Equal(t, "choice", activeKindFor(tut))

	require.Equal(t, 1, tut.winOverlay.Windows(),
		"a choice step must open exactly one prompt window")
	center := term.Coordinates{X: 40, Y: 12}
	assert.True(t, tut.winOverlay.Covers(center),
		"the centered prompt window must cover 40x12")
	_, ok := tut.ComponentAt(center)
	assert.False(t, ok,
		"prompt windows are hosted by the browser, not the bespoke overlay")
}

// TestFloatingWindowTitleBarPresent asserts the window bar carries
// the composed "Step N — title" caption and the close icon.
func TestFloatingWindowTitleBarPresent(t *testing.T) {
	t.Parallel()
	tut, g := drawProbe(t, "Welcome", "body line", 80, 24)
	defer tut.Stop()

	pos, _, _ := activeWindowRect(t, tut)
	row := g.rowRunes(pos.Y)
	assert.Contains(t, row, "Step 1 — Welcome",
		"window bar must carry the composed step caption, got %q", row)
	assert.Contains(t, row, "●",
		"window bar must render the close icon, got %q", row)
}

// TestFloatingWindowNoTitleShowsStepCaption asserts that without a
// title the window bar still identifies the step.
func TestFloatingWindowNoTitleShowsStepCaption(t *testing.T) {
	t.Parallel()
	tut, g := drawProbe(t, "", "body line", 80, 24)
	defer tut.Stop()

	pos, _, _ := activeWindowRect(t, tut)
	row := g.rowRunes(pos.Y)
	assert.Contains(t, row, "Step 1",
		"untitled window bar must still show the step number, got %q", row)
}

// TestFloatingWindowTitleBarTruncatesOnNarrow asserts that an
// overlong caption is truncated to the bar's interior instead of
// overflowing the window.
func TestFloatingWindowTitleBarTruncatesOnNarrow(t *testing.T) {
	t.Parallel()
	tut, g := drawProbe(t, "A very lengthy title text",
		"body", 36, 12)
	defer tut.Stop()

	pos, _, _ := activeWindowRect(t, tut)
	row := g.rowRunes(pos.Y)
	assert.NotContains(t, row, "A very lengthy title text",
		"overlong caption must be truncated, got %q", row)
	assert.Contains(t, row, "Step 1",
		"truncated caption must keep the step prefix, got %q", row)
}

// TestWaitCommandHintTitleBarPresent asserts that a wait_command with
// a title renders it on the hint window's bar, with the step number
// carried over from the preceding visible step.
func TestWaitCommandHintTitleBarPresent(t *testing.T) {
	t.Parallel()
	src := "def run():\n" +
		"    floating_window(text=\"intro\", title=\"Manage windows\")\n" +
		"    wait_command(command=\"windownew\", title=\"Manage windows\")\n" +
		"tutorial(entry=run)\n"
	tut, _ := newTutorial(t, src)
	const w, h = 80, 24
	tut.Resize(w, h)
	resetAndWait(t, tut, time.Second)

	// Dismiss the floating window so the run goroutine blocks on
	// wait_command, which is the state we want to render.
	tut.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && activeKindFor(tut) != "wait_command" {
		time.Sleep(time.Millisecond)
	}
	require.Equal(t, "wait_command", activeKindFor(tut))

	g := newAttrGridWriter(w, h)
	tut.Draw(g)
	tut.winOverlay.Draw(g)

	pos, _, _ := activeWindowRect(t, tut)
	row := g.rowRunes(pos.Y)
	assert.Contains(t, row, "Step 1 — Manage windows",
		"wait_command hint bar must carry the step caption, got %q", row)
}

// TestFloatingWindowStepCounterIncrements asserts that consecutive
// floating_window publications produce stepNum 1, 2, 3.
func TestFloatingWindowStepCounterIncrements(t *testing.T) {
	t.Parallel()
	src := `
def run():
    floating_window(title="one", text="a")
    floating_window(title="two", text="b")
    floating_window(title="three", text="c")
tutorial(entry=run)
`
	tut, _ := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)

	for want := 1; want <= 3; want++ {
		tut.mu.Lock()
		require.NotNil(t, tut.active,
			"step %d: active request must be set", want)
		assert.Equal(t, want, tut.active.stepNum,
			"step %d: snapshot must equal step counter", want)
		tut.mu.Unlock()
		if want < 3 {
			_, _ = tut.Handle(term.Event{
				Type: term.EventKey, Key: term.KeyEnter,
			})
			waitNextActive(t, tut, "floating_window", time.Second)
		}
	}
	tut.Stop()
}

// TestMarkdownStepCounter asserts that markdown() also bumps the
// visible-content step counter.
func TestMarkdownStepCounter(t *testing.T) {
	t.Parallel()
	src := `
def run():
    markdown(text="hello")
tutorial(entry=run)
`
	tut, _ := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	tut.mu.Lock()
	require.NotNil(t, tut.active)
	got := tut.active.stepNum
	tut.mu.Unlock()
	assert.Equal(t, 1, got,
		"markdown must bump the visible-content step counter")
	tut.Stop()
}

// TestSideEffectsDoNotBumpCounter asserts that notify() between two
// floating_window calls keeps the counter at 1 → 2 (notify is not a
// "step").
func TestSideEffectsDoNotBumpCounter(t *testing.T) {
	t.Parallel()
	src := `
def run():
    floating_window(title="one", text="a")
    notify(message="side effect")
    floating_window(title="two", text="b")
tutorial(entry=run)
`
	tut, _ := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	tut.mu.Lock()
	step1 := tut.active.stepNum
	tut.mu.Unlock()
	require.Equal(t, 1, step1)

	_, _ = tut.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	waitNextActive(t, tut, "floating_window", time.Second)
	tut.mu.Lock()
	step2 := tut.active.stepNum
	tut.mu.Unlock()
	assert.Equal(t, 2, step2,
		"notify between steps must not bump the counter")
	tut.Stop()
}

// TestWaitCommandHintRendersAsFramedBox asserts that the
// wait_command hint renders as a browser window at the bottom of the
// screen with markdown body. The prefix line ("Waiting for you …")
// must be present and the configured command key must be expanded
// in place of the `<cmd>` token.
func TestWaitCommandHintRendersAsFramedBox(t *testing.T) {
	t.Parallel()
	src := `
def run():
    wait_command(command="wopen")
tutorial(entry=run)
`
	tut, _ := newTutorial(t, src)
	const screenW, screenH = 80, 24
	tut.Resize(screenW, screenH)
	resetAndWait(t, tut, time.Second)
	defer tut.Stop()

	g := newAttrGridWriter(screenW, screenH)
	tut.Draw(g)
	tut.winOverlay.Draw(g)

	pos, _, h := activeWindowRect(t, tut)
	assert.Equal(t, screenH, pos.Y+h,
		"hint window must sit flush against the bottom of the screen")
	fcs := component.FrameCharSetDefault()
	bottomFound := false
	for y := range screenH {
		if strings.ContainsRune(g.rowRunes(y), fcs.BottomLeft) {
			bottomFound = true
			break
		}
	}
	require.True(t, bottomFound,
		"wait_command hint must render a framed window")

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
	overlay := idetutorial.NewOverlayBrowser(
		browser.NewComponent(idetutorial.DefaultOverlayBrowserConfig()))
	tut, err := New(
		"manual-test", src,
		overlay, nil, nil, nil,
		term.Attributes{},
		nil, nil, term.KeyComb{Ch: ':'},
		"standard", "", nil,
		lookup,
		nil,
		nil,
	)
	require.NoError(t, err)

	// A tall screen so the prompt-aware height cap (0.2*height) leaves
	// room for the full manual body beneath the instruction line.
	const screenW, screenH = 80, 60
	tut.Resize(screenW, screenH)
	resetAndWait(t, tut, time.Second)
	defer tut.Stop()

	g := newAttrGridWriter(screenW, screenH)
	tut.Draw(g)
	tut.winOverlay.Draw(g)
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
	overlay := idetutorial.NewOverlayBrowser(
		browser.NewComponent(idetutorial.DefaultOverlayBrowserConfig()))
	tut, err := New(
		"boundkey-test", src,
		overlay, nil, nil, nil,
		term.Attributes{},
		nil, nil, term.KeyComb{Ch: ':'},
		"standard", "", keyFor,
		nil,
		nil,
		nil,
	)
	require.NoError(t, err)

	const screenW, screenH = 80, 60
	tut.Resize(screenW, screenH)
	resetAndWait(t, tut, time.Second)
	defer tut.Stop()

	g := newAttrGridWriter(screenW, screenH)
	tut.Draw(g)
	tut.winOverlay.Draw(g)
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
	overlay := idetutorial.NewOverlayBrowser(
		browser.NewComponent(idetutorial.DefaultOverlayBrowserConfig()))
	tut, err := New(
		"bang-args-test", src,
		overlay, nil, nil, nil,
		term.Attributes{},
		nil, nil, term.KeyComb{Ch: ':'},
		"standard", "", keyFor,
		nil,
		nil,
		nil,
	)
	require.NoError(t, err)

	const screenW, screenH = 80, 60
	tut.Resize(screenW, screenH)
	resetAndWait(t, tut, time.Second)
	defer tut.Stop()

	g := newAttrGridWriter(screenW, screenH)
	tut.Draw(g)
	tut.winOverlay.Draw(g)
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
	r := &request{command: "windownew right", text: "Split to the right."}

	hint := buildWaitCommandHint(r, "<alt-x>", nil, keyFor)

	assert.Contains(t, hint, "windownew right")
	assert.Contains(t, hint, "<meta-r>")
}

func TestWaitCommandTextDoesNotRepeatExactBoundKey(t *testing.T) {
	t.Parallel()
	keyFor := func(string, []string) string { return "<meta-r>" }
	r := &request{
		command: "windownew right",
		text:    "Split to the right with `<meta-r>`.",
	}

	hint := buildWaitCommandHint(r, "<alt-x>", nil, keyFor)

	assert.Equal(t, 1, strings.Count(hint, "<meta-r>"))
	assert.NotContains(t, hint, "Or you can press")
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
	overlay := idetutorial.NewOverlayBrowser(
		browser.NewComponent(idetutorial.DefaultOverlayBrowserConfig()))
	tut, err := New(
		"hint-text-test", src,
		overlay, nil, nil, nil,
		term.Attributes{},
		nil, nil, term.KeyComb{Ch: ':'},
		"standard", "", nil,
		lookup,
		nil,
		nil,
	)
	require.NoError(t, err)

	const screenW, screenH = 80, 60
	tut.Resize(screenW, screenH)
	resetAndWait(t, tut, time.Second)
	defer tut.Stop()

	g := newAttrGridWriter(screenW, screenH)
	tut.Draw(g)
	tut.winOverlay.Draw(g)
	body := gridText(g)

	assert.Contains(t, body, "Now focus the editor on the left.",
		"the step's own instruction must show before any failed dispatch")
	assert.NotContains(t, body, "Switch focus to the window on the given side.",
		"the generic manual must not stand in for the step's instruction")
}

// TestWaitCommandHintStaysClearOfCommandPrompt asserts that a hint
// window never covers the command prompt the step asks the user to
// open. The prompt anchors at 0.2*height, so the hint must start
// below that row — including on short screens, where a long hint body
// used to spill over the prompt.
func TestWaitCommandHintStaysClearOfCommandPrompt(t *testing.T) {
	t.Parallel()
	longHint := strings.Repeat("This is a long recovery hint line. ", 40)
	src := `
def run():
    wait_command(command="wopen", on_error="` + longHint + `")
tutorial(entry=run)
`
	for _, screenH := range []int{24, 50} {
		t.Run(fmt.Sprintf("height-%d", screenH), func(t *testing.T) {
			t.Parallel()
			tut, _ := newTutorial(t, src)
			const screenW = 80
			tut.Resize(screenW, screenH)
			resetAndWait(t, tut, time.Second)
			defer tut.Stop()

			// Swap in the (long) on_error hint by simulating a
			// failed dispatch.
			tut.ObserveCommand("wopen", "wopen", nil,
				fmt.Errorf("missing argument"))

			g := newAttrGridWriter(screenW, screenH)
			tut.Draw(g)
			tut.winOverlay.Draw(g)

			pos, _, h := activeWindowRect(t, tut)
			promptTopY := int(float64(screenH) * commandPromptTopFraction)
			assert.Greater(t, pos.Y, promptTopY,
				"hint window top (row %d) must stay below the command "+
					"prompt anchor (row %d) so the prompt stays visible",
				pos.Y, promptTopY)
			assert.LessOrEqual(t, pos.Y+h, screenH,
				"hint window must stay on screen")
		})
	}
}

// TestWaitEventHintShowsWholeBody is a regression test for a
// wait_event step whose instructions were cut off mid-body: the
// prompt-aware height cap (0.2*height) applied to every hint kind,
// so a step that carries its call to action in the text lost the
// last lines. Only a wait_command hint has a command prompt to stay
// clear of.
func TestWaitEventHintShowsWholeBody(t *testing.T) {
	t.Parallel()
	body := "Line one of the instructions.\n\n" +
		"Line two of the instructions.\n\n" +
		"Line three of the instructions.\n\n" +
		"Line four of the instructions.\n\n" +
		"Line five of the instructions.\n\n" +
		"Finally, save the file."
	src := fmt.Sprintf("def run():\n"+
		"    wait_event(event=\"flush\", title=\"Make it stick\", text=%q)\n"+
		"tutorial(entry=run)\n", body)
	tut, _ := newTutorial(t, src)
	const screenW, screenH = 80, 40
	tut.Resize(screenW, screenH)
	resetAndWait(t, tut, time.Second)
	defer tut.Stop()

	tut.mu.Lock()
	content := tut.active.winContent
	tut.mu.Unlock()
	require.NotNil(t, content)

	contentW, contentH := content.Dimensions()
	want := content.Handler.Height(max(contentW-2, 1))
	assert.GreaterOrEqual(t, contentH, want,
		"wait_event hint must be tall enough for its whole body "+
			"(%d rows), otherwise the closing instruction is cut off",
		want)
}

// TestStepWindowsShareBottomAnchor asserts that a teaching window and
// the hint window of the step that follows it both anchor at the
// bottom, so the tutorial does not jump between the top and bottom of
// the screen between steps.
func TestStepWindowsShareBottomAnchor(t *testing.T) {
	t.Parallel()
	src := "def run():\n" +
		"    floating_window(text=\"intro\", title=\"Manage windows\")\n" +
		"    wait_command(command=\"windownew\", title=\"Manage windows\")\n" +
		"tutorial(entry=run)\n"
	tut, _ := newTutorial(t, src)
	const screenW, screenH = 80, 24
	tut.Resize(screenW, screenH)
	resetAndWait(t, tut, time.Second)
	defer tut.Stop()

	pos, _, h := activeWindowRect(t, tut)
	assert.Equal(t, screenH, pos.Y+h,
		"teaching window must sit flush against the bottom of the screen")

	tut.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	waitNextActive(t, tut, "wait_command", time.Second)

	pos, _, h = activeWindowRect(t, tut)
	assert.Equal(t, screenH, pos.Y+h,
		"hint window must keep the teaching window's bottom anchor")
}

// TestWaitShellHintAnchorsAtTop asserts that steps waiting on a
// console command anchor their hint at the top: Rune's console draws
// its prompt at the bottom of the screen, which a bottom-anchored
// hint would cover.
func TestWaitShellHintAnchorsAtTop(t *testing.T) {
	t.Parallel()
	src := `
def run():
    wait_shell(args=["pkg", "install", "rune-agent"])
tutorial(entry=run)
`
	tut, _ := newTutorial(t, src)
	const screenW, screenH = 80, 24
	tut.Resize(screenW, screenH)
	resetAndWait(t, tut, time.Second)
	defer tut.Stop()

	pos, _, h := activeWindowRect(t, tut)
	assert.Equal(t, 1, pos.Y,
		"console hint must sit just below the top of the screen")
	assert.Less(t, pos.Y+h, screenH,
		"console hint must leave the console prompt at the bottom visible")
}

// TestWaitShellHintShowsStepText asserts a console step can carry its
// own instruction, so a lesson does not need a key-swallowing page to
// explain what to type while the console is focused.
func TestWaitShellHintShowsStepText(t *testing.T) {
	t.Parallel()
	src := `
def run():
    wait_shell(args=["pkg", "install", "rune-agent"],
               text="Type it and press Enter.")
tutorial(entry=run)
`
	tut, _ := newTutorial(t, src)
	const screenW, screenH = 80, 24
	tut.Resize(screenW, screenH)
	resetAndWait(t, tut, time.Second)
	defer tut.Stop()

	g := newAttrGridWriter(screenW, screenH)
	tut.Draw(g)
	tut.winOverlay.Draw(g)
	body := gridText(g)

	assert.Contains(t, body, "pkg install")
	assert.Contains(t, body, "Type it and press Enter.")

	handled, _ := tut.Handle(term.Event{Type: term.EventKey, Ch: 'p'})
	assert.False(t, handled,
		"a console step must let the user type the command")
}

// TestConfirmOverlayMeetsMinimumSize asserts that even with a very
// short confirm message the prompt window honours the overlay
// browser's configured minimum prompt width and is tall enough for
// the message and options.
func TestConfirmOverlayMeetsMinimumSize(t *testing.T) {
	t.Parallel()
	src := `
def run():
    confirm("ok?")
tutorial(entry=run)
`
	tut, _ := newTutorial(t, src)
	const screenW, screenH = 80, 24
	tut.Resize(screenW, screenH)
	resetAndWait(t, tut, time.Second)
	defer tut.Stop()

	minWidth := idetutorial.DefaultOverlayBrowserConfig().PromptConfig.MinWidth
	_, w, h := activeWindowRect(t, tut)
	assert.GreaterOrEqual(t, w, minWidth,
		"confirm window must honour the configured minimum prompt width")
	assert.GreaterOrEqual(t, h, 5,
		"confirm window must fit the message and options")
}

// mouseEvent builds a mouse event at screen coordinates (x, y).
func mouseEvent(key term.Key, x, y int) term.Event {
	return term.Event{Type: term.EventMouse, Key: key, MouseX: x, MouseY: y}
}

// TestFloatingWindowCloseIconClickResolves asserts that clicking the
// window bar's ✕ icon closes the step window through the browser's
// bookkeeping path and that the next event reaps the closed window,
// resolving the floating_window step like Esc would.
func TestFloatingWindowCloseIconClickResolves(t *testing.T) {
	t.Parallel()
	tut, _ := drawProbe(t, "Welcome", "body line", 80, 24)
	defer tut.Stop()
	pos, _, _ := activeWindowRect(t, tut)

	handled, routed := tut.winOverlay.HandleMouse(mouseEvent(
		term.MouseLeft, pos.X+tuicomp.WindowBarCloseIconX, pos.Y))
	require.True(t, routed, "a press on the window bar must route to the browser")
	require.True(t, handled, "the close-icon press must be consumed")
	_, _ = tut.winOverlay.HandleMouse(mouseEvent(
		term.MouseRelease, pos.X+tuicomp.WindowBarCloseIconX, pos.Y))

	assert.Zero(t, tut.winOverlay.Windows(),
		"the ✕ click must close the window through the browser path")

	exit, handled := tut.Handle(mouseEvent(term.MouseRelease, 0, 0))
	assert.True(t, handled, "the reaping event must be consumed")
	assert.True(t, exit, "closing the only step must finish the tutorial")
	waitFinished(t, tut, time.Second)
	assert.Zero(t, tut.winOverlay.Windows(),
		"a finished tutorial must leave no overlay window behind")
}

// TestFloatingWindowDragMovesShaderGeometry asserts that dragging the
// step window by its bar moves the armed hint-pulse geometry with it:
// Shader() derives its spec from the live window rectangle, so the
// composing handler's value comparison restages the pulse after a
// move.
func TestFloatingWindowDragMovesShaderGeometry(t *testing.T) {
	t.Parallel()
	tut, _ := drawProbe(t, "Welcome", "body line", 80, 24)
	defer tut.Stop()

	// Arm the pulse with a stray key, then capture the spec.
	_, _ = tut.Handle(term.Event{Type: term.EventKey, Ch: 'x'})
	spec1, ok := tut.Shader()
	require.True(t, ok, "a stray key must arm the hint pulse")

	pos, w, _ := activeWindowRect(t, tut)
	require.Greater(t, w, 8, "window too narrow to grab the bar")
	// Step windows anchor flush to the bottom, so drag upwards: a
	// downward drag would be clamped by the screen edge.
	const dx, dy = 3, -2
	grabX, grabY := pos.X+5, pos.Y
	_, routed := tut.winOverlay.HandleMouse(mouseEvent(term.MouseLeft, grabX, grabY))
	require.True(t, routed, "the bar press must route to the browser")
	_, _ = tut.winOverlay.HandleMouse(mouseEvent(term.MouseLeft, grabX+dx, grabY+dy))
	_, _ = tut.winOverlay.HandleMouse(mouseEvent(term.MouseRelease, grabX+dx, grabY+dy))

	newPos, _, _ := activeWindowRect(t, tut)
	require.Equal(t, term.Coordinates{X: pos.X + dx, Y: pos.Y + dy}, newPos,
		"the bar drag must move the window by the drag delta")

	spec2, ok := tut.Shader()
	require.True(t, ok, "the pulse must stay armed across a drag")
	assert.Equal(t, spec1.Offset.X+dx, spec2.Offset.X,
		"the pulse geometry must follow the window horizontally")
	assert.Equal(t, spec1.Offset.Y+dy, spec2.Offset.Y,
		"the pulse geometry must follow the window vertically")
	assert.NotEqual(t, spec1, spec2,
		"the moved spec must differ so the composing handler restages it")
}

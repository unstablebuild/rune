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

package ide

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"

	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/ide/idetutorial"
	"unstable.build/rune/internal/ide/idetutorial/starlarktutorial"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/texttest"
)

// tutStub is a Tutorial whose milestones the test triggers by hand:
// finish flips Finished, and the observers report it.
type tutStub struct {
	width, height int
	resetCount    int
	stopCount     int
	skipCount     int
	backCount     int
	events        []term.Event
	finished      bool
	completed     bool
	// observed records every event the runner forwarded, so a test can
	// tell what a lesson was asked to resolve on.
	observed []string
	// refuseReset makes Reset end the run immediately, the way a
	// lesson written for an older Rune refuses to start.
	refuseReset bool
	// prompt makes the stub report a question waiting on the tile.
	prompt bool
	// viewingPast makes the stub report an earlier screen on show,
	// and counts the steps forward the runner asks for.
	viewingPast  bool
	forwardCount int
}

func (t *tutStub) Resize(width, height int) { t.width, t.height = width, height }
func (t *tutStub) Draw(_ term.Writer)       {}
func (t *tutStub) Handle(ev term.Event) (bool, bool) {
	t.events = append(t.events, ev)
	return false, false
}

func (t *tutStub) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return term.Coordinates{}, term.CursorStyleDefault, false
}
func (t *tutStub) Selection() (string, bool) { return "", false }
func (t *tutStub) Reset()                    { t.resetCount++; t.finished = t.refuseReset }
func (t *tutStub) Stop()                     { t.stopCount++ }
func (t *tutStub) Skip() bool                { t.skipCount++; return t.finished }
func (t *tutStub) Back() bool                { t.backCount++; return true }
func (t *tutStub) Forward() bool             { t.forwardCount++; return true }
func (t *tutStub) ViewingPast() bool         { return t.viewingPast }
func (t *tutStub) Finished() bool            { return t.finished }
func (t *tutStub) Completed() bool           { return t.completed }
func (t *tutStub) PromptActive() bool        { return t.prompt }
func (t *tutStub) SeekUp() bool              { return false }
func (t *tutStub) SeekDown() bool            { return false }
func (t *tutStub) SeekOffset() int           { return 0 }
func (t *tutStub) MaxSeekOffset() int        { return 0 }
func (t *tutStub) ObserveCommand(_, _ string, _ []string, _ error) bool {
	return t.finished
}
func (t *tutStub) ObserveEvent(eventType, uri string) bool {
	t.observed = append(t.observed, eventType+" "+uri)
	return t.finished
}

// finish ends the stub's run the way a real lesson ends: completed
// reports whether it returned normally.
func (t *tutStub) finish(completed bool) {
	t.finished = true
	t.completed = completed
}

// newTestRunner wires a runner around a single workspace, the way
// the IDE wires it around the workspace handler.
func newTestRunner(
	t *testing.T, tutorials map[string]idetutorial.Tutorial,
	onCompleted ...func(string),
) (*tutorialRunner, *ex) {
	t.Helper()
	b := newExForTesting(t, texttest.NopEditor(),
		text.WithCommandKey(testCommandKey))
	t.Cleanup(func() { b.Close() })
	r := &tutorialRunner{}
	var completed func(string)
	if len(onCompleted) > 0 {
		completed = onCompleted[0]
	}
	r.init(b.ex, tutorials, testTileStyle(), 0, b.ex.setRightInset, completed)
	b.ex.commandObserver = r
	r.Resize(120, 30)
	return r, b.ex
}

// testTileStyle frames the tile with a focus charset distinct from
// the default one so a test can tell a focused tile apart.
func testTileStyle() idetutorial.TileStyle {
	return idetutorial.TileStyle{
		Frame:             true,
		FrameCharSet:      component.FrameCharSetDefault(),
		FocusFrameCharSet: component.FrameCharSetHighlight(),
	}
}

func startTutorial(t *testing.T, r *tutorialRunner, name string) {
	t.Helper()
	require.NoError(t, r.HandleCommand(context.Background(),
		textapi.Command{Name: "tutorial", Args: []string{"start", name}}))
}

// tileColumn is the width of the tile's column on a screen total
// cells wide: the lesson's width plus its frame.
func tileColumn(total int) int {
	return idetutorial.TileWidth(total) + 2
}

// findCell returns the screen position of want in the drawn rows.
func findCell(t *testing.T, rows []string, want string) term.Coordinates {
	t.Helper()
	for y, row := range rows {
		if x := strings.Index(row, want); x >= 0 {
			return term.Coordinates{X: len([]rune(row[:x])), Y: y}
		}
	}
	t.Fatalf("%q not drawn", want)
	return term.Coordinates{}
}

// findButton returns the screen position of the tile's footer button
// labelled want. The footer is the bottom row of the tile, so the
// search runs upwards: a lesson's copy may name the same button.
func findButton(t *testing.T, rows []string, want string) term.Coordinates {
	t.Helper()
	for y := len(rows) - 1; y >= 0; y-- {
		if x := strings.Index(rows[y], want); x >= 0 {
			return term.Coordinates{X: len([]rune(rows[y][:x])), Y: y}
		}
	}
	t.Fatalf("%q not drawn", want)
	return term.Coordinates{}
}

func clickRunner(r *tutorialRunner, pos term.Coordinates) {
	_, _ = r.Handle(term.Event{
		Type: term.EventMouse, Key: term.MouseLeft, MouseX: pos.X, MouseY: pos.Y,
	})
	_, _ = r.Handle(term.Event{
		Type: term.EventMouse, Key: term.MouseRelease, MouseX: pos.X, MouseY: pos.Y,
	})
}

// focusTile clicks inside the tile, which is how the user focuses it,
// and forgets the click the lesson was handed.
func focusTile(t *testing.T, r *tutorialRunner, tut *tutStub) {
	t.Helper()
	top, _ := r.Handler.(*ex).windowRows()
	clickRunner(r, term.Coordinates{X: r.tileColumn() + 5, Y: top + 3})
	require.True(t, r.tileFocused())
	tut.events = nil
}

func drawRunner(t *testing.T, r *tutorialRunner, width, height int) []string {
	t.Helper()
	w := term.NewStringWriter(width, height)
	r.Draw(w)
	require.NoError(t, w.Flush())
	return strings.Split(w.String(), "\n")
}

func TestTutorialRunnerLaysTheTileOutBesideTheWorkspace(t *testing.T) {
	t.Parallel()
	tut := &tutStub{}
	r, e := newTestRunner(t, map[string]idetutorial.Tutorial{"basics": tut})
	editor := e.invokeWindow()
	tiles := e.comp.Browser().Tiles()

	startTutorial(t, r, "basics")

	require.True(t, r.running())
	assert.Equal(t, tiles, e.comp.Browser().Tiles(),
		"the tile is not a window of the workspace")
	assert.Equal(t, editor, e.invokeWindow(), "the user keeps their window")
	assert.Equal(t, 1, tut.resetCount)
	top, rows := e.windowRows()
	require.Positive(t, top, "sanity: the tab bar pads the workspace's windows")
	assert.Equal(t, idetutorial.TileWidth(120), tut.width,
		"the lesson gets a quarter of the screen")
	assert.Equal(t, rows-4, tut.height, "less the frame and the footer")
	assert.Equal(t, 120-tileColumn(120), e.width, "the workspace gives the column up")
	assert.Equal(t, 30, e.height)

	screen := drawRunner(t, r, 120, 30)
	x := 120 - tileColumn(120)
	cell := func(x, y int) string { return string([]rune(screen[y])[x]) }
	assert.Equal(t, "┌", cell(x, top),
		"the tile starts on the row the workspace's windows start on")
	assert.Equal(t, "┘", cell(119, top+rows-1), "and ends on the row they end on")
	assert.Equal(t, " ", cell(x, top-1),
		"the rows the tab bar takes are left clear beside it")
	assert.Contains(t, screen[top+rows-2], "Skip")
	assert.Contains(t, screen[top+rows-2], "Stop")

	r.Resize(200, 40)
	assert.Equal(t, idetutorial.TileWidth(200), tut.width, "the tile follows the screen size")
	assert.Equal(t, 200-tileColumn(200), e.width)

	require.NoError(t, r.HandleCommand(context.Background(),
		textapi.Command{Name: "tutorial", Args: []string{"stop"}}))
	assert.False(t, r.running())
	assert.Equal(t, 200, e.width, "stopping gives the workspace the screen back")
	assert.Equal(t, 1, tut.stopCount)
}

// TestTutorialRunnerSurvivesTheWindowCommandsItTeaches pins the reason
// the tile lives outside the workspace: a lesson that asks the user to
// clear or rearrange their layout must still be there afterwards. The
// window commands act on the workspace's windows even while the tile
// has focus: only Stop and `tutorial stop` end a lesson.
func TestTutorialRunnerSurvivesTheWindowCommandsItTeaches(t *testing.T) {
	t.Parallel()
	tests := []struct {
		cmds  []string
		tiles int // the workspace's windows once the commands ran
	}{
		{cmds: []string{"windowcloseall"}, tiles: 1},
		{cmds: []string{"windownew", "windowcloseall"}, tiles: 1},
		{cmds: []string{"windowclose"}, tiles: 1},
		{cmds: []string{"windownew", "windowclose"}, tiles: 1},
		{cmds: []string{"tabclose"}, tiles: 1},
		{cmds: []string{"windownew", "windowmove left"}, tiles: 2},
		{cmds: []string{"windowfocus right"}, tiles: 1},
		{cmds: []string{"windownew", "windowfocus left", "windowfocus right",
			"windowfocus right"}, tiles: 2},
		{cmds: []string{"windowtogglemaximize"}, tiles: 1},
		{cmds: []string{"fexplorer"}, tiles: 2},
	}
	for _, focused := range []bool{false, true} {
		for _, tt := range tests {
			name := strings.Join(tt.cmds, ", ")
			if focused {
				name += " with the tile focused"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				tut := &tutStub{}
				r, e := newTestRunner(t, map[string]idetutorial.Tutorial{"basics": tut})
				startTutorial(t, r, "basics")
				if focused {
					focusTile(t, r, tut)
				}

				for _, cmd := range tt.cmds {
					fields := strings.Fields(cmd)
					require.NoError(t, e.dispatchCommand(fields[0], fields[1:]...))
				}

				assert.True(t, r.running())
				assert.Zero(t, tut.stopCount)
				assert.Equal(t, tt.tiles, e.comp.Browser().Tiles(),
					"the commands act on the workspace's windows")
				assert.Equal(t, idetutorial.TileWidth(120), tut.width,
					"the tile keeps its width")
				assert.Equal(t, 120-tileColumn(120), e.width)
				assert.Equal(t, focused, r.tileFocused(),
					"only the mouse moves focus onto or off the tile")
			})
		}
	}
}

// TestTutorialRunnerAgentLessonStartsWithWindowCloseAll drives the
// first step of the agent tutorial, which clears the layout, through
// a real lesson: the milestone must resolve instead of ending the
// lesson.
func TestTutorialRunnerAgentLessonStartsWithWindowCloseAll(t *testing.T) {
	t.Parallel()
	tut := newStarlarkTutorial(t, `
def run():
    wait_command(command="windowcloseall", text="clear the layout")
    wait_command(command="edit", text="now edit")
tutorial(entry=run)
`)
	r, e := newTestRunner(t, map[string]idetutorial.Tutorial{"agent": tut})
	startTutorial(t, r, "agent")
	require.True(t, tut.WaitActive("wait_command", time.Second))
	require.Equal(t, "clear the layout", tut.ActiveText())

	require.NoError(t, e.dispatchCommand("windownew"))
	require.NoError(t, e.dispatchCommand("windowcloseall"))

	require.True(t, r.running(), "clearing the layout is the step, not the end")
	require.True(t, tut.WaitActive("wait_command", time.Second))
	assert.Equal(t, "now edit", tut.ActiveText())
	assert.Equal(t, 1, e.comp.Browser().Tiles())
}

func TestTutorialRunnerMouseFocusesAndBlursTheTile(t *testing.T) {
	t.Parallel()
	tut := &tutStub{}
	r, _ := newTestRunner(t, map[string]idetutorial.Tutorial{"basics": tut})
	startTutorial(t, r, "basics")
	x := 120 - tileColumn(120)
	click := func(px, py int) {
		_, _ = r.Handle(term.Event{Type: term.EventMouse, Key: term.MouseLeft, MouseX: px, MouseY: py})
		_, _ = r.Handle(term.Event{Type: term.EventMouse, Key: term.MouseRelease, MouseX: px, MouseY: py})
	}

	top, _ := r.Handler.(*ex).windowRows()
	click(x+5, top+3)
	assert.True(t, r.tileFocused(), "a click on the tile focuses it")
	rows := drawRunner(t, r, 120, 30)
	assert.Equal(t, "┃", string([]rune(rows[15])[x]), "the tile draws the focus frame")
	require.Len(t, tut.events, 2)
	assert.Equal(t, 4, tut.events[0].MouseX, "the event is translated into the lesson")
	assert.Equal(t, 2, tut.events[0].MouseY, "past the padding and the frame")

	click(10, top+3)
	assert.False(t, r.tileFocused(), "a click elsewhere blurs it")
	rows = drawRunner(t, r, 120, 30)
	assert.Equal(t, "│", string([]rune(rows[15])[x]))
	assert.Len(t, tut.events, 2, "and does not reach it")

	// A drag that starts on the tile ends there too.
	_, _ = r.Handle(term.Event{
		Type: term.EventMouse, Key: term.MouseLeft, MouseX: x + 5, MouseY: top + 3,
	})
	_, _ = r.Handle(term.Event{
		Type: term.EventMouse, Key: term.MouseRelease, MouseX: 10, MouseY: top + 3,
	})
	assert.Len(t, tut.events, 4)
	assert.Equal(t, term.MouseRelease, tut.events[3].Key)
}

// TestTutorialRunnerKeysAlwaysReachTheWorkspace pins that the lesson
// never takes the keyboard: it is read with the mouse, so whatever the
// user types goes on reaching the editor, a terminal or the command
// prompt even while the tile is the pane in focus.
func TestTutorialRunnerKeysAlwaysReachTheWorkspace(t *testing.T) {
	t.Parallel()
	tut := &tutStub{}
	r, e := newTestRunner(t, map[string]idetutorial.Tutorial{"basics": tut})
	startTutorial(t, r, "basics")
	focusTile(t, r, tut)

	_, _ = r.Handle(term.Event{Type: term.EventKey, Ch: 'j'})
	assert.Empty(t, tut.events, "the tile is never given a key")

	_, _ = r.Handle(term.Event{Type: term.EventKey, Ch: testCommandKey.Ch, Mod: testCommandKey.Mod})
	assert.NotNil(t, e.cmd, "the command prompt opens from a focused tile")
	assert.True(t, r.tileFocused(), "and the tile keeps the focus it was given")
}

// recordingEditor is a texttest.TestEditor whose handlers record the
// keys they are given, so a test can tell whether the editor saw one.
type recordingEditor struct {
	*texttest.TestEditor
	seen *[]term.KeyComb
}

func (e recordingEditor) Edit(
	ctx context.Context,
	resource workspaceapi.URI, buf *cell.Buffer, readOnly, recovered bool,
) (text.Handler, error) {
	h, err := e.TestEditor.Edit(ctx, resource, buf, readOnly, recovered)
	if err != nil {
		return nil, err
	}
	eh := h.(*texttest.TestEditorHandler)
	eh.HandleOverride = func(ev term.Event) (bool, bool) {
		*e.seen = append(*e.seen, ev.KeyComb())
		return false, false
	}
	return eh, nil
}

// TestTutorialRunnerFocusedTileStillTypesIntoTheEditor asserts the
// tile's focus never takes the keyboard: the editor beside it goes on
// receiving what the user types, bindings included.
func TestTutorialRunnerFocusedTileStillTypesIntoTheEditor(t *testing.T) {
	t.Parallel()
	var seen []term.KeyComb
	b := newExForTesting(t, recordingEditor{TestEditor: texttest.NopEditor(), seen: &seen},
		text.WithCommandKey(testCommandKey))
	t.Cleanup(func() { b.Close() })
	tut := &tutStub{}
	r := &tutorialRunner{}
	r.init(b.ex, map[string]idetutorial.Tutorial{"basics": tut}, testTileStyle(), 0,
		b.ex.setRightInset, nil)
	b.ex.commandObserver = r
	r.Resize(120, 30)
	file, err := workspaceapi.ParseURI("file:///notes.md")
	require.NoError(t, err)
	_, err = b.editFileURI(file, b.ex.invokeWindow(), false)
	require.NoError(t, err)
	startTutorial(t, r, "basics")
	key := term.Event{Type: term.EventKey, Ch: 'x'}

	_, _ = r.Handle(key)
	require.Equal(t, []term.KeyComb{key.KeyComb()}, seen, "sanity: the editor sees keys")

	focusTile(t, r, tut)
	_, _ = r.Handle(key)
	assert.Len(t, seen, 2, "a key typed while the tile is focused still edits")
	assert.Empty(t, tut.events, "and the lesson sees none of it")

	_, _ = r.Handle(term.Event{Type: term.EventKey, Ch: 'w', Mod: term.ModCtrl})
	assert.True(t, r.running(), "the tabclose binding closes the editor's tab, not the tile")
	assert.Empty(t, b.ex.comp.Tabs())
}

func TestTutorialRunnerWindowCloseElsewhereKeepsTheLesson(t *testing.T) {
	t.Parallel()
	tut := &tutStub{}
	r, e := newTestRunner(t, map[string]idetutorial.Tutorial{"basics": tut})
	startTutorial(t, r, "basics")
	require.NoError(t, e.dispatchCommand("windownew"))
	require.Equal(t, 2, e.comp.Browser().Tiles())

	require.NoError(t, e.dispatchCommand("windowclose"))

	assert.True(t, r.running())
	assert.Equal(t, 1, e.comp.Browser().Tiles())
}

func TestTutorialRunnerHidesTheTileOnANarrowScreen(t *testing.T) {
	t.Parallel()
	tut := &tutStub{}
	r, e := newTestRunner(t, map[string]idetutorial.Tutorial{"basics": tut})
	startTutorial(t, r, "basics")

	r.Resize(60, 20)
	assert.Equal(t, 60, e.width, "the workspace keeps the whole screen")
	assert.True(t, r.running(), "the lesson goes on regardless")
	_, _ = r.Handle(term.Event{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 59, MouseY: 5})
	assert.Empty(t, tut.events, "there is no tile to click")
	assert.False(t, r.tileFocused())

	r.Resize(120, 30)
	assert.Equal(t, 120-tileColumn(120), e.width, "the tile is back once there is room")
	focusTile(t, r, tut)
}

// TestTutorialRunnerNeverOpensARefusedLesson covers a lesson written
// for an older Rune: it reports itself finished at Reset, and the
// runner must not leave a tile behind for it.
func TestTutorialRunnerNeverOpensARefusedLesson(t *testing.T) {
	t.Parallel()
	var completed []string
	tut := &tutStub{refuseReset: true}
	r, e := newTestRunner(t, map[string]idetutorial.Tutorial{"legacy": tut},
		func(name string) { completed = append(completed, name) })
	startTutorial(t, r, "legacy")

	assert.False(t, r.running(), "a refused lesson never becomes the active one")
	assert.Equal(t, 120, e.width, "the workspace keeps the whole screen")
	assert.Empty(t, completed, "a refused lesson is not a completed one")
}

// TestTutorialRunnerIgnoresRuneOwnBuffers covers a lesson that waits
// for the user to open a file from the explorer: the explorer's own
// tree, and gitshow's diffs, publish open events of their own, and
// resolving a step on one of those advances the lesson behind the
// user's back.
func TestTutorialRunnerIgnoresRuneOwnBuffers(t *testing.T) {
	t.Parallel()
	tut := &tutStub{}
	r, _ := newTestRunner(t, map[string]idetutorial.Tutorial{"basics": tut})
	startTutorial(t, r, "basics")

	r.observeEvent("open", fileExplorerURI)
	r.observeEvent("open", gitshowBaseURI().String()+"/a.go.diff?n=2")
	assert.Empty(t, tut.observed,
		"Rune's own buffers must not resolve a step")

	r.observeEvent("open", "file:///workspace/main.go")
	assert.Equal(t, []string{"open file:///workspace/main.go"}, tut.observed)
}

func TestTutorialRunnerMovesTheRightInsetToTheTile(t *testing.T) {
	t.Parallel()
	tut := &tutStub{}
	b := newExForTesting(t, texttest.NopEditor())
	t.Cleanup(func() { b.Close() })
	var rootInsets []int
	r := &tutorialRunner{}
	r.init(b.ex, map[string]idetutorial.Tutorial{"basics": tut}, testTileStyle(), 3,
		func(cells int) { rootInsets = append(rootInsets, cells) }, nil)
	r.Resize(120, 30)

	r.setRightInset(4)
	assert.Equal(t, []int{4}, rootInsets, "without a lesson the workspaces reserve the column")

	startTutorial(t, r, "basics")
	assert.Equal(t, []int{4, 0}, rootInsets, "the tile takes the column over")
	assert.Equal(t, idetutorial.TileWidth(120), tut.width, "the lesson is no narrower for it")
	assert.Equal(t, 120-tileColumn(120)-4, b.ex.width,
		"the workspace gives up the tile and the column")
	rows := drawRunner(t, r, 120, 30)
	assert.Equal(t, "    ", string([]rune(rows[15])[116:]), "the column stays clear")

	r.setRightInset(0)
	assert.Equal(t, []int{4, 0}, rootInsets)
	assert.Equal(t, 120-tileColumn(120), b.ex.width)

	require.NoError(t, r.HandleCommand(context.Background(),
		textapi.Command{Name: "tutorial", Args: []string{"stop"}}))
	assert.Equal(t, []int{4, 0, 0}, rootInsets, "the workspaces get the column back")
}

// attrRecorder keeps the attributes last written at each cell.
type attrRecorder struct {
	cells map[term.Coordinates]term.Cell
}

func (r *attrRecorder) SetCell(pos term.Coordinates, c term.Cell) {
	if r.cells == nil {
		r.cells = make(map[term.Coordinates]term.Cell)
	}
	r.cells[pos] = c
}
func (r *attrRecorder) UnionAttributes(term.Coordinates, term.Attributes) {}
func (r *attrRecorder) Context() context.Context                          { return context.Background() }
func (r *attrRecorder) DrawImage(term.Image) bool                         { return false }

// TestTutorialRunnerTileIsNeverDimmed asserts the lesson stays at full
// brightness while the command prompt dims the workspace behind it:
// that is the moment the user is reading it.
func TestTutorialRunnerTileIsNeverDimmed(t *testing.T) {
	t.Parallel()
	tut := &tutStub{}
	r, e := newTestRunner(t, map[string]idetutorial.Tutorial{"basics": tut})
	startTutorial(t, r, "basics")
	editorCorner := term.Coordinates{X: 0, Y: 29}
	tileCorner := term.Coordinates{X: 119, Y: 29}

	_, _ = r.Handle(term.Event{Type: term.EventKey, Ch: testCommandKey.Ch, Mod: testCommandKey.Mod})
	require.NotNil(t, e.cmd, "the command prompt must be open")
	rec := &attrRecorder{}
	r.Draw(rec)
	assert.NotZero(t, rec.cells[editorCorner].Attrs&term.AttrDim,
		"the prompt dims the workspace behind it")
	assert.Zero(t, rec.cells[tileCorner].Attrs&term.AttrDim,
		"the tile stays bright under the prompt")
}

func TestTutorialRunnerFooterButtons(t *testing.T) {
	t.Parallel()
	t.Run("skip", func(t *testing.T) {
		t.Parallel()
		var completed []string
		tut := &tutStub{}
		r, e := newTestRunner(t, map[string]idetutorial.Tutorial{"basics": tut},
			func(name string) { completed = append(completed, name) })
		startTutorial(t, r, "basics")

		r.advance()
		r.reconcile()
		assert.Equal(t, 1, tut.skipCount)
		assert.True(t, r.running(), "skipping a middle step keeps the lesson up")

		tut.completed = true
		tut.finished = true
		r.advance()
		r.reconcile()
		assert.True(t, r.running(),
			"skipping the last step leaves the lesson up to be closed")
		r.requestStop()
		r.reconcile()
		assert.False(t, r.running())
		assert.Equal(t, 120, e.width)
		assert.Equal(t, []string{"basics"}, completed)
	})
	t.Run("stop", func(t *testing.T) {
		t.Parallel()
		var completed []string
		tut := &tutStub{completed: true}
		r, e := newTestRunner(t, map[string]idetutorial.Tutorial{"basics": tut},
			func(name string) { completed = append(completed, name) })
		startTutorial(t, r, "basics")

		r.requestStop()
		assert.True(t, r.running(), "the button's own frame is left alone")
		r.reconcile()
		assert.False(t, r.running())
		assert.Equal(t, 120, e.width)
		assert.Empty(t, completed, "a stopped lesson is not completed")
	})
	t.Run("back", func(t *testing.T) {
		t.Parallel()
		tut := &tutStub{}
		r, _ := newTestRunner(t, map[string]idetutorial.Tutorial{"basics": tut})
		startTutorial(t, r, "basics")

		rows := drawRunner(t, r, 120, 30)
		clickRunner(r, findButton(t, rows, "Back"))
		assert.Equal(t, 1, tut.backCount)
		assert.True(t, r.running(), "re-reading a step never ends the lesson")
	})
	t.Run("next", func(t *testing.T) {
		t.Parallel()
		tut := &tutStub{viewingPast: true}
		r, _ := newTestRunner(t, map[string]idetutorial.Tutorial{"basics": tut})
		startTutorial(t, r, "basics")

		rows := drawRunner(t, r, 120, 30)
		clickRunner(r, findButton(t, rows, "Next"))
		assert.Equal(t, 1, tut.forwardCount)
		assert.Zero(t, tut.skipCount,
			"a step the user cannot see must not be skipped")
	})
}

// TestTutorialRunnerPromptTakesTheKeyboard asserts a step that asks a
// question can be answered without the mouse: the keys a prompt walks
// its options with reach the tile while one is up. Everything else,
// and every key while a copy step is up, belongs to the workspace.
func TestTutorialRunnerPromptTakesTheKeyboard(t *testing.T) {
	t.Parallel()
	answers := []term.Event{
		{Type: term.EventKey, Key: term.KeyArrowLeft},
		{Type: term.EventKey, Key: term.KeyArrowRight},
		{Type: term.EventKey, Key: term.KeyEnter},
		{Type: term.EventKey, Ch: 'h', Mod: term.ModCtrl},
		{Type: term.EventKey, Ch: 'l', Mod: term.ModCtrl},
	}
	// Esc leaves a modal editor's insert mode and Tab indents; a
	// question on the tile is not worth taking either away.
	others := []term.Event{
		{Type: term.EventKey, Key: term.KeyEsc},
		{Type: term.EventKey, Key: term.KeyTab},
		{Type: term.EventKey, Key: term.KeyArrowDown},
		{Type: term.EventKey, Ch: 'l'},
	}

	t.Run("a copy step leaves the keyboard alone", func(t *testing.T) {
		t.Parallel()
		tut := &tutStub{}
		r, _ := newTestRunner(t, map[string]idetutorial.Tutorial{"basics": tut})
		startTutorial(t, r, "basics")
		for _, ev := range append(append([]term.Event{}, answers...), others...) {
			_, _ = r.Handle(ev)
		}
		assert.Empty(t, tut.events)
	})

	t.Run("a prompt answers its own keys", func(t *testing.T) {
		t.Parallel()
		tut := &tutStub{prompt: true}
		r, _ := newTestRunner(t, map[string]idetutorial.Tutorial{"basics": tut})
		startTutorial(t, r, "basics")
		for _, ev := range answers {
			_, _ = r.Handle(ev)
		}
		assert.Len(t, tut.events, len(answers))

		tut.events = nil
		for _, ev := range others {
			_, _ = r.Handle(ev)
		}
		assert.Empty(t, tut.events)
	})

	t.Run("a hidden tile answers nothing", func(t *testing.T) {
		t.Parallel()
		tut := &tutStub{prompt: true}
		r, _ := newTestRunner(t, map[string]idetutorial.Tutorial{"basics": tut})
		startTutorial(t, r, "basics")
		r.Resize(minRootWidth, 30)
		for _, ev := range answers {
			_, _ = r.Handle(ev)
		}
		assert.Empty(t, tut.events)
	})
}

// TestTutorialRunnerChoiceIsPickedWithTheKeyboard drives a real
// lesson's choice step from the keyboard, end to end.
func TestTutorialRunnerChoiceIsPickedWithTheKeyboard(t *testing.T) {
	t.Parallel()
	tut := newStarlarkTutorial(t, `
def run():
    pick = choice(message="pick one", options=["Alpha", "Bravo"])
    wait_command(command="nonesuch", text="picked " + pick.value)
tutorial(entry=run)
`)
	r, _ := newTestRunner(t, map[string]idetutorial.Tutorial{"basics": tut})
	startTutorial(t, r, "basics")
	require.True(t, tut.WaitActive("choice", time.Second))

	_, _ = r.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowRight})
	_, _ = r.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	require.True(t, tut.WaitActive("wait_command", time.Second))
	assert.Equal(t, "picked Bravo", tut.ActiveText())
}

// TestTutorialRunnerKeepsAFinishedLessonOnScreen covers the end of a
// lesson: the last milestone must not take the tile away, since the
// user may still be working through what it just taught. Only Stop
// closes it, and that is when the run counts as completed.
func TestTutorialRunnerKeepsAFinishedLessonOnScreen(t *testing.T) {
	t.Parallel()
	var completed []string
	tut := &tutStub{}
	r, e := newTestRunner(t, map[string]idetutorial.Tutorial{"basics": tut},
		func(name string) { completed = append(completed, name) })
	startTutorial(t, r, "basics")

	tut.finish(true)
	r.observeCommand("cheatsheet", "cheatsheet", nil, nil)
	assert.True(t, r.running(), "the lesson stays up after its last step")
	assert.Equal(t, 120-tileColumn(120), e.width, "the tile keeps its column")
	assert.Empty(t, completed, "completion is reported when the tile closes")

	rows := drawRunner(t, r, 120, 30)
	clickRunner(r, findButton(t, rows, "Stop"))
	r.reconcile()
	assert.False(t, r.running())
	assert.Equal(t, 120, e.width)
	assert.Equal(t, []string{"basics"}, completed)
}

func TestTutorialRunnerReportsSuccessfulCompletion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		end  func(*tutorialRunner, *tutStub)
	}{
		{
			name: "command observation",
			end: func(r *tutorialRunner, tut *tutStub) {
				tut.finish(true)
				r.observeCommand("edit", "edit", nil, nil)
			},
		},
		{
			name: "event observation",
			end: func(r *tutorialRunner, tut *tutStub) {
				tut.finish(true)
				r.observeEvent("open", "file:///example.go")
			},
		},
		{
			name: "asynchronous end noticed on the next frame",
			end: func(r *tutorialRunner, tut *tutStub) {
				tut.finish(true)
				r.Draw(term.NewStringWriter(120, 30))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var r *tutorialRunner
			var completed []string
			clearedBeforeCallback := false
			tut := &tutStub{}
			r, e := newTestRunner(t, map[string]idetutorial.Tutorial{"basics": tut},
				func(name string) {
					clearedBeforeCallback = !r.running() && r.activeName == ""
					completed = append(completed, name)
				})
			startTutorial(t, r, "basics")

			tt.end(r, tut)
			// The lesson waits for the user to close it, whichever way
			// it reached its end.
			r.requestStop()
			r.reconcile()

			assert.Equal(t, []string{"basics"}, completed)
			assert.True(t, clearedBeforeCallback)
			assert.Equal(t, 120, e.width)
		})
	}
}

func TestTutorialRunnerDoesNotReportUnsuccessfulOrStoppedTutorial(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		end  func(*tutorialRunner, *tutStub) error
	}{
		{
			name: "unsuccessful exit",
			end: func(r *tutorialRunner, tut *tutStub) error {
				tut.finish(false)
				r.observeCommand("edit", "edit", nil, nil)
				return nil
			},
		},
		{
			name: "explicit stop",
			end: func(r *tutorialRunner, _ *tutStub) error {
				return r.HandleCommand(context.Background(),
					textapi.Command{Name: "tutorial", Args: []string{"stop"}})
			},
		},
		{
			name: "replacement",
			end: func(r *tutorialRunner, _ *tutStub) error {
				return r.HandleCommand(context.Background(),
					textapi.Command{Name: "tutorial", Args: []string{"start", "other"}})
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var completed []string
			tut := &tutStub{completed: true}
			r, _ := newTestRunner(t, map[string]idetutorial.Tutorial{
				"basics": tut,
				"other":  &tutStub{},
			},
				func(name string) { completed = append(completed, name) })
			startTutorial(t, r, "basics")
			require.NoError(t, tt.end(r, tut))
			assert.Empty(t, completed)
		})
	}
}

func TestTutorialRunnerUnknownTutorial(t *testing.T) {
	t.Parallel()
	r, _ := newTestRunner(t, nil)
	err := r.HandleCommand(context.Background(),
		textapi.Command{Name: "tutorial", Args: []string{"start", "nope"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown tutorial "nope"`)
}

func TestTutorialRunnerStop(t *testing.T) {
	t.Parallel()
	r, _ := newTestRunner(t, map[string]idetutorial.Tutorial{"basics": &tutStub{}})

	err := r.HandleCommand(context.Background(),
		textapi.Command{Name: "tutorial", Args: []string{"stop"}})
	require.Error(t, err)

	startTutorial(t, r, "basics")
	require.True(t, r.running())
	require.NoError(t, r.HandleCommand(context.Background(),
		textapi.Command{Name: "tutorial", Args: []string{"stop"}}))
	assert.False(t, r.running())
	assert.Equal(t, "", r.activeName)
}

// TestTutorialRunnerRunningIsRaceFree pins that running() may be read
// off the event loop: the package-install gate consults it from the
// background syntax and LSP goroutines while the event loop starts and
// stops tutorials.
func TestTutorialRunnerRunningIsRaceFree(t *testing.T) {
	t.Parallel()
	r, _ := newTestRunner(t, map[string]idetutorial.Tutorial{"basics": &tutStub{}})

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 1000 {
			_ = r.running()
		}
	}()

	ctx := context.Background()
	for range 100 {
		require.NoError(t, r.HandleCommand(ctx,
			textapi.Command{Name: "tutorial", Args: []string{"start", "basics"}}))
		assert.True(t, r.running())
		require.NoError(t, r.HandleCommand(ctx,
			textapi.Command{Name: "tutorial", Args: []string{"stop"}}))
		assert.False(t, r.running())
	}
	<-done
}

func TestTutorialRunnerComplete(t *testing.T) {
	t.Parallel()
	r, _ := newTestRunner(t, map[string]idetutorial.Tutorial{
		"basics":   &tutStub{},
		"advanced": &tutStub{},
	})

	ctx := context.Background()
	drain := func(it iterator.Iterator[string]) []string {
		out := []string{}
		for {
			v, ok := it.Next(ctx)
			if !ok {
				break
			}
			out = append(out, v)
		}
		return out
	}

	it, _, err := r.Complete(ctx, textapi.Command{Name: "tutorial"})
	require.NoError(t, err)
	assert.Equal(t, []string{"start", "stop"}, drain(it))

	it, _, err = r.Complete(ctx,
		textapi.Command{Name: "tutorial", Args: []string{"start", ""}})
	require.NoError(t, err)
	assert.Equal(t, []string{"advanced", "basics"}, drain(it))

	it, _, err = r.Complete(ctx,
		textapi.Command{Name: "tutorial", Args: []string{"stop", ""}})
	require.NoError(t, err)
	assert.Empty(t, drain(it))

	it, _, err = r.Complete(ctx,
		textapi.Command{Name: "tutorial", Args: []string{"start", "basics", "extra"}})
	require.NoError(t, err)
	assert.Empty(t, drain(it))
}

func TestTutorialRunnerRegister(t *testing.T) {
	t.Parallel()
	r, _ := newTestRunner(t, nil)
	assert.False(t, r.has("intro"))

	tut := &tutStub{}
	assert.True(t, r.register("intro", tut), "first register should add")
	assert.True(t, r.has("intro"))

	assert.False(t, r.register("intro", &tutStub{}),
		"register must not overwrite an existing tutorial")

	startTutorial(t, r, "intro")
	assert.True(t, r.running(), "registered tutorial should be startable")
	assert.Equal(t, "intro", r.activeName)
}

func newStarlarkTutorial(t *testing.T, src string) *starlarktutorial.Tutorial {
	t.Helper()
	tut, err := starlarktutorial.New(
		"under-test", src,
		idetutorial.PromptStyle{}, nil, nil, nil,
		nil, nil,
		term.KeyComb{Ch: ':'},
		"standard", "", nil,
		nil,
		nil,
		nil,
	)
	require.NoError(t, err)
	return tut
}

// TestTutorialRunnerBasicsFlowEndToEnd drives a real starlark lesson
// through the runner: every step ends on its milestone and the tile
// comes down once the last one does.
func TestTutorialRunnerBasicsFlowEndToEnd(t *testing.T) {
	t.Parallel()
	tut := newStarlarkTutorial(t, `
def run():
    ws = wait_command(command="wopen", title="Welcome", text="open something")
    edit = wait_command(command="edit", text="now edit")
    notify(level=success, message="done " + ws.args[0] + " " + edit.args[0])
tutorial(entry=run)
`)
	var completed []string
	r, e := newTestRunner(t, map[string]idetutorial.Tutorial{"basics": tut},
		func(name string) { completed = append(completed, name) })
	startTutorial(t, r, "basics")
	require.True(t, tut.WaitActive("wait_command", time.Second))

	drawn := term.NewStringWriter(120, 30)
	r.Draw(drawn)
	require.NoError(t, drawn.Flush())
	assert.Contains(t, drawn.String(), "Welcome", "the step's title heads the tile")
	assert.Contains(t, drawn.String(), "open something")
	assert.Contains(t, drawn.String(), "Skip")
	assert.Contains(t, drawn.String(), "Stop")

	r.observeCommand("wopen", "wopen", []string{"~/proj"}, nil)
	require.True(t, tut.WaitActive("wait_command", time.Second))
	assert.Equal(t, "now edit", tut.ActiveText())
	assert.True(t, r.running())

	r.observeCommand("edit", "edit", []string{"main.go"}, nil)
	require.True(t, tut.WaitFinished(time.Second))
	assert.True(t, r.running(), "the last milestone leaves the tile up")
	r.requestStop()
	r.reconcile()
	assert.False(t, r.running())
	assert.Equal(t, 120, e.width)
	assert.Equal(t, []string{"basics"}, completed)
}

// TestTutorialRunnerObserveCommandKeepsArmedOnError asserts a failed
// dispatch keeps the step armed and the tile up.
func TestTutorialRunnerObserveCommandKeepsArmedOnError(t *testing.T) {
	t.Parallel()
	tut := newStarlarkTutorial(t, `
def run():
    wait_command(command="wopen", text="open something")
    notify(level=success, message="advanced")
tutorial(entry=run)
`)
	r, _ := newTestRunner(t, map[string]idetutorial.Tutorial{"basics": tut})
	startTutorial(t, r, "basics")
	require.True(t, tut.WaitActive("wait_command", time.Second))

	r.observeCommand("wopen", "wopen", nil, errors.New("missing directory"))
	assert.True(t, r.running())
	assert.True(t, tut.WaitActive("wait_command", time.Second))

	r.observeCommand("wopen", "wopen", []string{"~/p"}, nil)
	assert.True(t, r.running(), "a finished lesson waits to be closed")
	r.requestStop()
	r.reconcile()
	assert.False(t, r.running())
}

// TestTutorialRunnerPromptStepEndsOnTheTile asserts a confirm step is
// answered on the tile, with the mouse.
func TestTutorialRunnerPromptStepEndsOnTheTile(t *testing.T) {
	t.Parallel()
	tut := newStarlarkTutorial(t, `
def run():
    if confirm(message="continue?"):
        notify(level=success, message="yes")
tutorial(entry=run)
`)
	var completed []string
	r, _ := newTestRunner(t, map[string]idetutorial.Tutorial{"basics": tut},
		func(name string) { completed = append(completed, name) })
	startTutorial(t, r, "basics")
	require.True(t, tut.WaitActive("confirm", time.Second))

	rows := drawRunner(t, r, 120, 30)
	assert.Contains(t, strings.Join(rows, "\n"), "continue?")
	yes := findCell(t, rows, "Yes")

	clickRunner(r, yes)
	require.True(t, tut.WaitFinished(time.Second))
	// The lesson ends on its own goroutine; the next frame notices,
	// and leaves the finished lesson up for the user to close.
	rows = drawRunner(t, r, 120, 30)
	assert.True(t, r.running())
	clickRunner(r, findButton(t, rows, "Stop"))
	r.reconcile()
	assert.False(t, r.running())
	assert.Equal(t, []string{"basics"}, completed)
}

func TestTutorialEventObserverDefersToEventLoop(t *testing.T) {
	t.Parallel()

	var queued []func()
	sched := func(fn func()) bool {
		queued = append(queued, fn)
		return true
	}
	var observed [][2]string
	observe := func(eventType, uri string) {
		observed = append(observed, [2]string{eventType, uri})
	}

	handler := tutorialEventObserver(sched, observe)

	uri, err := workspaceapi.ParseURI("file:///x.go")
	require.NoError(t, err)
	consumed := handler.Handle(context.Background(), textapi.Event{
		Type: textapi.EventTypeOpen,
		URI:  uri,
	})

	assert.False(t, consumed,
		"the tutorial observer must never consume the event")
	assert.Empty(t, observed,
		"observe must be deferred to the scheduler, not called inline "+
			"on the background subscriber goroutine")
	require.Len(t, queued, 1, "observe must be scheduled exactly once")

	queued[0]()
	require.Len(t, observed, 1)
	assert.Equal(t, "open", observed[0][0])
	assert.Equal(t, uri.String(), observed[0][1])
}

func TestCommandObserverRegistryLateSubscribe(t *testing.T) {
	t.Parallel()
	reg := newCommandObserverRegistry()

	var exObs commandObserver = reg
	exObs.observeCommand("noop", "noop", nil, nil)

	stub := &recordingObserver{}
	reg.subscribe(stub)

	exObs.observeCommand("wopen", "wopen", []string{"~/proj"}, nil)
	require.Len(t, stub.calls, 1,
		"subscriber registered after construction must still receive dispatches")
	assert.Equal(t, "wopen", stub.calls[0])

	reg.subscribe(nil)
	assert.Len(t, reg.subscribers, 1)
}

// TestFuncCommandObserver asserts the callback adapter fires once per
// dispatched command regardless of the command's name, args or outcome.
func TestFuncCommandObserver(t *testing.T) {
	t.Parallel()
	reg := newCommandObserverRegistry()

	var calls int
	reg.subscribe(funcCommandObserver(func() { calls++ }))

	reg.observeCommand("wopen", "wopen", []string{"~/proj"}, nil)
	reg.observeCommand("bogus", "bogus", nil, errors.New("no such command"))

	assert.Equal(t, 2, calls, "failed commands still count as dispatched")
}

type recordingObserver struct {
	calls []string
}

func (r *recordingObserver) observeCommand(
	typed, resolved string, args []string, err error,
) {
	r.calls = append(r.calls, typed)
}

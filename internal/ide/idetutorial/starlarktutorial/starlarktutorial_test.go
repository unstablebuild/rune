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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/term"

	"unstable.build/rune/internal/ide/idetutorial"
)

// fakeNotis captures every Notify / NotifyOnce call so tests can
// assert on message text and severity level after a tutorial run.
type fakeNotis struct {
	mu       sync.Mutex
	captured []notifyCall
}

type notifyCall struct {
	level browserapi.NotificationLevel
	msg   string
	args  []any
}

func (c notifyCall) rendered() string {
	return fmt.Sprintf(c.msg, c.args...)
}

func (f *fakeNotis) Notify(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	f.mu.Lock()
	f.captured = append(f.captured, notifyCall{level: level, msg: msg, args: args})
	f.mu.Unlock()
	return "", nil
}

func (f *fakeNotis) NotifyOnce(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	return f.Notify(level, msg, args...)
}

func (f *fakeNotis) UpdateNotificationProgress(_, _ string, _, _ int64) error {
	return nil
}

func (f *fakeNotis) renderedCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.captured))
	for i, c := range f.captured {
		out[i] = c.rendered()
	}
	return out
}

func (f *fakeNotis) containsSubstring(s string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.captured {
		if strings.Contains(c.rendered(), s) {
			return true
		}
	}
	return false
}

func (f *fakeNotis) len() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.captured)
}

func (f *fakeNotis) containsLevel(level browserapi.NotificationLevel) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.captured {
		if c.level == level {
			return true
		}
	}
	return false
}

// gridWriter is a deterministic term.Writer used to assert rendered
// output in tests. Cells written out of range are dropped silently.
type gridWriter struct {
	w, h  int
	cells [][]rune
}

func newGridWriter(w, h int) *gridWriter {
	cells := make([][]rune, h)
	for i := range cells {
		cells[i] = make([]rune, w)
	}
	return &gridWriter{w: w, h: h, cells: cells}
}

func (g *gridWriter) SetCell(pos term.Coordinates, c term.Cell) {
	if pos.X < 0 || pos.Y < 0 || pos.X >= g.w || pos.Y >= g.h {
		return
	}
	g.cells[pos.Y][pos.X] = c.Ch
}

func (g *gridWriter) Context() context.Context                              { return context.Background() }
func (g *gridWriter) UnionAttributes(_ term.Coordinates, _ term.Attributes) {}
func (g *gridWriter) DrawImage(term.Image) bool                             { return false }

func (g *gridWriter) row(y int) []rune {
	if y < 0 || y >= g.h {
		return nil
	}
	return g.cells[y]
}

func gridContains(g *gridWriter, needle string) bool {
	for y := range g.h {
		if strings.Contains(string(g.row(y)), needle) {
			return true
		}
	}
	return false
}

func newTutorial(t *testing.T, src string) (*Tutorial, *fakeNotis) {
	t.Helper()
	notis := &fakeNotis{}
	tut, err := New(
		"tutorial-under-test", src,
		idetutorial.PromptStyle{}, nil, notis, nil,
		nil, nil,
		term.KeyComb{Ch: ':'},
		"standard", "", nil,
		nil,
		nil,
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, tut)
	tut.Resize(bodyW, bodyH)
	return tut, notis
}

// bodyW and bodyH are the tile-body dimensions the tests render into:
// what idetutorial.Handler hands a tutorial on an 80x24 screen.
const (
	bodyW = 22
	bodyH = 21
)

// resetAndWait spawns the runtime via Reset and waits until either
// the first blocking request is published or the entry exits. Tests
// use this in place of bare Reset to avoid racing with the run
// goroutine on initial active publication.
func resetAndWait(t *testing.T, tut *Tutorial, d time.Duration) {
	t.Helper()
	tut.Reset()
	deadline := time.After(d)
	for {
		tut.mu.Lock()
		active := tut.active
		finished := tut.finished
		tut.mu.Unlock()
		if active != nil || finished {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("entry never published a request within %s", d)
		case <-time.After(time.Millisecond):
		}
	}
}

// activeKindFor returns the kind name of the currently active
// request, or "" when none is set.
func activeKindFor(t *Tutorial) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.active == nil {
		return ""
	}
	return t.active.kind.String()
}

// waitFinished waits up to d for the tutorial's run goroutine to
// exit. Tests use this to assert clean exit on exit() / fail() / normal
// return / Stop().
func waitFinished(t *testing.T, tut *Tutorial, d time.Duration) {
	t.Helper()
	deadline := time.After(d)
	for {
		tut.mu.Lock()
		finished := tut.finished
		done := tut.runDone
		tut.mu.Unlock()
		if finished {
			if done == nil {
				return
			}
			select {
			case <-done:
			case <-deadline:
				t.Fatalf("tutorial did not finish within %s", d)
			}
			return
		}
		if done != nil {
			select {
			case <-done:
				continue
			case <-deadline:
				t.Fatalf("tutorial did not finish within %s", d)
			}
		}
		select {
		case <-deadline:
			t.Fatalf("tutorial did not finish within %s", d)
		case <-time.After(time.Millisecond):
		}
	}
}

// waitNextActive waits up to d for tut.active to change kind to
// wantKind (or for the tutorial to finish, when wantKind is "").
// Tests use this after delivering a prompt OnSelect/OnClose because
// the response is consumed asynchronously by the run goroutine.
func waitNextActive(t *testing.T, tut *Tutorial, wantKind string, d time.Duration) {
	t.Helper()
	deadline := time.After(d)
	for {
		tut.mu.Lock()
		finished := tut.finished
		var kind string
		if tut.active != nil {
			kind = tut.active.kind.String()
		}
		tut.mu.Unlock()
		if wantKind == "" {
			if finished {
				return
			}
		} else if kind == wantKind {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("active=%q finished=%t; want active=%q within %s",
				kind, finished, wantKind, d)
		case <-time.After(time.Millisecond):
		}
	}
}

// TestEntryRequired asserts that tutorial() requires entry=.
func TestEntryRequired(t *testing.T) {
	t.Parallel()
	_, err := New(
		"x", `tutorial(id="x")`,
		idetutorial.PromptStyle{}, nil, nil, nil,
		nil, nil, term.KeyComb{Ch: ':'},
		"standard", "", nil,
		nil,
		nil,
		nil,
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "entry")
}

// TestEntryMustBeCallable asserts that tutorial() rejects a non-
// function entry.
func TestEntryMustBeCallable(t *testing.T) {
	t.Parallel()
	_, err := New(
		"x", `tutorial(entry="not a func")`,
		idetutorial.PromptStyle{}, nil, nil, nil,
		nil, nil, term.KeyComb{Ch: ':'},
		"standard", "", nil,
		nil,
		nil,
		nil,
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "entry must be a function")
}

// TestEntryMustTakeZeroArgs asserts that tutorial() rejects an entry
// that declares parameters.
func TestEntryMustTakeZeroArgs(t *testing.T) {
	t.Parallel()
	_, err := New(
		"x", `tutorial(entry=lambda x: 1)`,
		idetutorial.PromptStyle{}, nil, nil, nil,
		nil, nil, term.KeyComb{Ch: ':'},
		"standard", "", nil,
		nil,
		nil,
		nil,
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "zero")
}

// TestDuplicateTutorialRejected asserts that calling tutorial() twice
// fails at parse time.
func TestDuplicateTutorialRejected(t *testing.T) {
	t.Parallel()
	_, err := New(
		"x",
		"def a(): pass\n"+
			"def b(): pass\n"+
			"tutorial(entry=a)\n"+
			"tutorial(entry=b)\n",
		idetutorial.PromptStyle{}, nil, nil, nil,
		nil, nil, term.KeyComb{Ch: ':'},
		"standard", "", nil,
		nil,
		nil,
		nil,
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already called")
}

// TestEmptySourceRejected asserts that New rejects an empty source.
func TestEmptySourceRejected(t *testing.T) {
	t.Parallel()
	_, err := New(
		"x", "",
		idetutorial.PromptStyle{}, nil, nil, nil,
		nil, nil, term.KeyComb{Ch: ':'},
		"standard", "", nil,
		nil,
		nil,
		nil,
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty source")
}

// TestEntryRunsToCompletion asserts that an entry built only from
// non-blocking builtins finishes cleanly without manual key input.
func TestEntryRunsToCompletion(t *testing.T) {
	t.Parallel()
	src := `
def run():
    notify(level=info, message="hello")
    notify(level=success, message="bye")
tutorial(entry=run)
`
	tut, notis := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	waitFinished(t, tut, time.Second)
	assert.True(t, tut.Completed())
	assert.Equal(t, 2, notis.len())
}

// TestNotifyIsNonBlocking asserts that notify() returns immediately
// and that the next request is the one after it.
func TestNotifyIsNonBlocking(t *testing.T) {
	t.Parallel()
	src := `
def run():
    notify(level=info, message="one")
    wait_command(command="ok")
tutorial(entry=run)
`
	tut, notis := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	assert.Equal(t, "wait_command", activeKindFor(tut),
		"notify must not block the runtime; wait_command must "+
			"be active immediately after Reset")
	assert.Equal(t, 1, notis.len(),
		"notify must fire eagerly even while wait_command is active")
}

// TestNotifyLevelConstants asserts that the `success` constant maps
// to browserapi.LevelSuccess.
func TestNotifyLevelConstants(t *testing.T) {
	t.Parallel()
	src := `
def run():
    notify(level=success, message="done")
tutorial(entry=run)
`
	tut, notis := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	waitFinished(t, tut, time.Second)
	require.Equal(t, 1, notis.len())
	assert.Equal(t, browserapi.LevelSuccess, notis.captured[0].level)
}

// TestWaitCommandObserveResolvesResult asserts that ObserveCommand
// returns a command_result with the typed name and args.
func TestWaitCommandObserveResolvesResult(t *testing.T) {
	t.Parallel()
	src := `
def run():
    r = wait_command(command="wopen")
    notify(message=r.name + ":" + r.args[0])
tutorial(entry=run)
`
	tut, notis := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	require.Equal(t, "wait_command", activeKindFor(tut))

	tut.ObserveCommand("wopen", "wopen", []string{"~/proj"}, nil)
	waitFinished(t, tut, time.Second)
	assert.True(t, notis.containsSubstring("wopen:~/proj"),
		"command_result.args[0] must reach the script, got %v",
		notis.renderedCalls())
}

// TestWaitCommandObserveErrorStaysArmed asserts that a dispatch error
// keeps the request active and does not surface as a notification.
func TestWaitCommandObserveErrorStaysArmed(t *testing.T) {
	t.Parallel()
	src := `
def run():
    wait_command(command="wopen", text="` + "<cmd>" + `wopen <dir>")
    notify(level=success, message="advanced")
tutorial(entry=run)
`
	tut, notis := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)

	exit := tut.ObserveCommand("wopen", "wopen", nil,
		fmt.Errorf("missing directory"))
	assert.False(t, exit)
	assert.Equal(t, "wait_command", activeKindFor(tut),
		"dispatch error must keep wait_command armed")
	assert.Equal(t, 0, notis.len(),
		"dispatch error must not surface as a notification")

	exit = tut.ObserveCommand("wopen", "wopen", []string{"~/p"}, nil)
	// Whether ObserveCommand returns exit=true depends on whether
	// the runtime advances to another blocking builtin before we
	// observe the active state. Wait for completion either way.
	_ = exit
	waitFinished(t, tut, time.Second)
	assert.True(t, notis.containsSubstring("advanced"),
		"successful dispatch must return control to Starlark, which "+
			"then runs the next builtin")
}

// TestWaitCommandResolvesViaAlias asserts that typed="e"
// resolved="edit" matches wait_command(command="edit").
func TestWaitCommandResolvesViaAlias(t *testing.T) {
	t.Parallel()
	src := `
def run():
    r = wait_command(command="edit")
    notify(message="opened " + r.args[0])
tutorial(entry=run)
`
	tut, notis := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)

	tut.ObserveCommand("e", "edit", []string{"main.go"}, nil)
	waitFinished(t, tut, time.Second)
	assert.True(t, notis.containsSubstring("opened main.go"),
		"alias dispatch must reach wait_command via resolved name")
}

// TestWaitEventNeverResolvesFromKeys asserts that a wait_event step
// never resolves and never swallows keystrokes: every key falls
// through to the IDE root while the step stays armed.
func TestWaitEventNeverResolvesFromKeys(t *testing.T) {
	t.Parallel()
	src := `
def run():
    wait_event(event="open")
    notify(level=success, message="opened")
tutorial(entry=run)
`
	tut, _ := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	require.Equal(t, "wait_event", activeKindFor(tut))

	exit, handled := tut.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	assert.False(t, exit)
	assert.False(t, handled,
		"wait_event must not swallow keys; they reach the IDE root")
	assert.Equal(t, "wait_event", activeKindFor(tut),
		"a keystroke must not resolve a wait_event step")

	exit, handled = tut.Handle(term.Event{Type: term.EventKey, Ch: 'x'})
	assert.False(t, exit)
	assert.False(t, handled)
	assert.Equal(t, "wait_event", activeKindFor(tut))
}

// TestWaitEventResolvesOnMatchingEvent asserts that ObserveEvent
// resolves a wait_event step only when the observed event-type name
// matches the armed name; a non-matching event keeps it armed.
func TestWaitEventResolvesOnMatchingEvent(t *testing.T) {
	t.Parallel()
	src := `
def run():
    wait_event(event="open")
    notify(level=success, message="opened")
tutorial(entry=run)
`
	tut, notis := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	require.Equal(t, "wait_event", activeKindFor(tut))

	exit := tut.ObserveEvent("close", "file:///x.go")
	assert.False(t, exit, "a non-matching event must not resolve wait_event")
	assert.Equal(t, "wait_event", activeKindFor(tut),
		"a non-matching event must keep wait_event armed")
	assert.Equal(t, 0, notis.len())

	_ = tut.ObserveEvent("open", "file:///x.go")
	waitFinished(t, tut, time.Second)
	assert.True(t, notis.containsSubstring("opened"),
		"the matching event must resolve wait_event and resume the script")
}

// TestWaitEventURIFilter asserts that the optional uri kwarg narrows a
// wait_event step to events whose URI contains it, so a step can await
// a write to one specific file rather than any buffer flush.
func TestWaitEventURIFilter(t *testing.T) {
	t.Parallel()
	src := `
def run():
    wait_event(event="flush", uri="config")
    notify(level=success, message="saved")
tutorial(entry=run)
`
	tut, notis := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	require.Equal(t, "wait_event", activeKindFor(tut))

	exit := tut.ObserveEvent("flush", "file:///workspace/README.md")
	assert.False(t, exit, "a non-matching URI must not resolve wait_event")
	assert.Equal(t, "wait_event", activeKindFor(tut),
		"a non-matching URI must keep wait_event armed")
	assert.Equal(t, 0, notis.len())

	_ = tut.ObserveEvent("flush", "file:///home/u/.rune/config.yaml")
	waitFinished(t, tut, time.Second)
	assert.True(t, notis.containsSubstring("saved"),
		"a URI containing the filter must resolve wait_event")
}

// TestWaitEventWithoutURIFilterMatchesAnyURI asserts that omitting the
// uri kwarg keeps the historical behaviour of matching on event type
// alone.
func TestWaitEventWithoutURIFilterMatchesAnyURI(t *testing.T) {
	t.Parallel()
	src := `
def run():
    wait_event(event="flush")
    notify(level=success, message="saved")
tutorial(entry=run)
`
	tut, notis := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	require.Equal(t, "wait_event", activeKindFor(tut))

	_ = tut.ObserveEvent("flush", "file:///workspace/anything.txt")
	waitFinished(t, tut, time.Second)
	assert.True(t, notis.containsSubstring("saved"),
		"an empty filter must match any URI")
}

// TestConfirmYesReturnsTrue asserts that confirm returns True when
// the user picks Yes (the highlighted option at index 0).
func TestConfirmYesReturnsTrue(t *testing.T) {
	t.Parallel()
	src := `
def run():
    if confirm("ok?"):
        notify(message="yes")
    else:
        notify(message="no")
tutorial(entry=run)
`
	tut, notis := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	require.Equal(t, "confirm", activeKindFor(tut),
		"confirm must publish a request as active")
	_, _ = tut.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	waitFinished(t, tut, time.Second)
	assert.True(t, notis.containsSubstring("yes"),
		"confirm Yes must take the True branch, got %v", notis.renderedCalls())
}

// TestConfirmNoReturnsFalse asserts that confirm returns False on
// the user picking the No option (index 1).
func TestConfirmNoReturnsFalse(t *testing.T) {
	t.Parallel()
	src := `
def run():
    if confirm("ok?"):
        notify(message="yes")
    else:
        notify(message="no")
tutorial(entry=run)
`
	tut, notis := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	require.Equal(t, "confirm", activeKindFor(tut))
	_, _ = tut.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowRight})
	_, _ = tut.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	waitFinished(t, tut, time.Second)
	assert.True(t, notis.containsSubstring("no"),
		"confirm No must take the False branch")
}

// TestConfirmDismissReturnsFalse asserts that closing the prompt
// without selecting (Esc) yields False (== dismissed).
func TestConfirmDismissReturnsFalse(t *testing.T) {
	t.Parallel()
	src := `
def run():
    if confirm("ok?"):
        notify(message="yes")
    else:
        notify(message="dismissed")
tutorial(entry=run)
`
	tut, notis := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	require.Equal(t, "confirm", activeKindFor(tut))
	_, _ = tut.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
	waitFinished(t, tut, time.Second)
	assert.True(t, notis.containsSubstring("dismissed"),
		"confirm dismissal must take the False branch")
}

// TestChoiceDrawsPromptInBody asserts that choice() renders the
// message and each option label into the tile body.
func TestChoiceDrawsPromptInBody(t *testing.T) {
	t.Parallel()
	src := `
def run():
    pick = choice(message="pick one", options=["A", "B", "C"])
    notify(message="value=" + pick.value)
tutorial(entry=run)
`
	tut, _ := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	require.Equal(t, "choice", activeKindFor(tut))
	g := newGridWriter(bodyW, bodyH)
	tut.Draw(g)
	assert.True(t, gridContains(g, "pick one"),
		"choice screen must render the message")
	for _, opt := range []string{"A", "B", "C"} {
		assert.True(t, gridContains(g, opt),
			"choice screen must render option %q", opt)
	}
	tut.Stop()
}

// TestChoiceOptionsFollowTheMessage asserts the buttons sit right
// under the copy that asks the question rather than at the foot of
// the tile, where a tall pane would strand them far from what they
// answer.
func TestChoiceOptionsFollowTheMessage(t *testing.T) {
	t.Parallel()
	src := `
def run():
    choice(message="pick one\n\nsome more copy here", options=["A", "B"])
tutorial(entry=run)
`
	tut, _ := newTutorial(t, src)
	const width, height = 40, 40
	tut.Resize(width, height)
	resetAndWait(t, tut, time.Second)
	require.Equal(t, "choice", activeKindFor(tut))

	g := newGridWriter(width, height)
	tut.Draw(g)
	copyRow := gridRow(g, "some more copy here")
	optsRow := gridRow(g, "A")
	require.GreaterOrEqual(t, copyRow, 0)
	require.Greater(t, optsRow, copyRow, "the buttons come after the copy")
	assert.LessOrEqual(t, optsRow-copyRow, 3,
		"the buttons must follow the copy, not the body's bottom edge")
	tut.Stop()
}

// TestPromptActiveOnlyForTheLiveQuestion asserts the host is told to
// hand the keyboard over for a question the run is blocked on, and
// for nothing else: not for copy, and not for a screen the user paged
// back to.
func TestPromptActiveOnlyForTheLiveQuestion(t *testing.T) {
	t.Parallel()
	src := `
def run():
    wait_command(command="alpha", text="first step")
    choice(message="pick one", options=["A", "B"])
tutorial(entry=run)
`
	tut, _ := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	require.Equal(t, "wait_command", activeKindFor(tut))
	assert.False(t, tut.PromptActive(), "copy answers no question")

	tut.ObserveCommand("alpha", "alpha", nil, nil)
	waitNextActive(t, tut, "choice", time.Second)
	assert.True(t, tut.PromptActive())

	require.True(t, tut.Back())
	assert.False(t, tut.PromptActive(), "a step already read is read-only")
	require.True(t, tut.Back())
	assert.True(t, tut.PromptActive(), "the live question is back on show")
	tut.Stop()
}

// TestChoiceOptionsPaddedButValueUnpadded asserts that prompt option
// labels render with surrounding space padding while the selected
// value reported to the script stays the original unpadded label.
func TestChoiceOptionsPaddedButValueUnpadded(t *testing.T) {
	t.Parallel()
	src := `
def run():
    pick = choice(message="pick one", options=["Alpha", "Beta"])
    notify(message="value=[" + pick.value + "]")
tutorial(entry=run)
`
	tut, notis := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	require.Equal(t, "choice", activeKindFor(tut))

	g := newGridWriter(bodyW, bodyH)
	tut.Draw(g)
	assert.True(t, gridContains(g, " Alpha "),
		"option label must render with surrounding space padding")

	_, _ = tut.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	waitFinished(t, tut, time.Second)
	assert.True(t, notis.containsSubstring("value=[Alpha]"),
		"selected value must be the unpadded label, got %v",
		notis.renderedCalls())
}

// TestPromptUsesConfiguredStyling asserts that confirm/choice prompts
// pick up the host's PromptStyle: the highlighted option is
// drawn with the configured HighlightAttr background, matching the
// IDE's browser-driven prompts instead of the SDK's reverse-video
// default.
func TestPromptUsesConfiguredStyling(t *testing.T) {
	t.Parallel()
	notis := &fakeNotis{}
	style := idetutorial.PromptStyle{
		HighlightAttr: term.Attributes{Bg: term.ColorRed, Fg: term.ColorWhite},
	}
	tut, err := New(
		"styled", `
def run():
    choice(message="pick", options=["Alpha", "Beta"])
tutorial(entry=run)
`,
		style, nil, notis, nil,
		nil, nil,
		term.KeyComb{Ch: ':'},
		"standard", "", nil,
		nil,
		nil,
		nil,
	)
	require.NoError(t, err)
	tut.Resize(bodyW, bodyH)
	resetAndWait(t, tut, time.Second)
	require.Equal(t, "choice", activeKindFor(tut))

	g := newAttrGridWriter(bodyW, bodyH)
	tut.Draw(g)

	found := false
	for y := 0; y < bodyH && !found; y++ {
		runes := []rune(g.rowRunes(y))
		attrs := g.rowAttrs(y)
		for x := 0; x+4 < len(runes); x++ {
			if string(runes[x:x+5]) == "Alpha" {
				if attrs[x].Bg == term.ColorRed {
					found = true
				}
				break
			}
		}
	}
	assert.True(t, found,
		"the highlighted option must render with the configured "+
			"HighlightAttr background")
	tut.Stop()
}

// TestPromptOptionsNeverTouch asserts a choice keeps its buttons apart
// however tight the tile is. The prompt spreads whatever width is left
// after the buttons over the gaps between them, so labels padded wider
// than the tile can hold take those gaps with them.
func TestPromptOptionsNeverTouch(t *testing.T) {
	t.Parallel()
	style := idetutorial.PromptStyle{
		TextAttr:      term.Attributes{Bg: term.ColorBlue},
		HighlightAttr: term.Attributes{Bg: term.ColorBlue},
	}
	options := []string{"Alpha", "Bravo", "Delta", "Gamma", "Sigma"}
	tut, err := New(
		"models", `
def run():
    choice(message="pick", options=["Alpha", "Bravo", "Delta", "Gamma", "Sigma"])
tutorial(entry=run)
`,
		style, nil, &fakeNotis{}, nil,
		nil, nil,
		term.KeyComb{Ch: ':'},
		"standard", "", nil,
		nil,
		nil,
		nil,
	)
	require.NoError(t, err)
	t.Cleanup(tut.Stop)
	tut.Resize(bodyW, bodyH)
	resetAndWait(t, tut, time.Second)
	require.Equal(t, "choice", activeKindFor(tut))

	// The narrowest of these still fits every label with a cell
	// between each pair of buttons and at both edges.
	for _, width := range []int{60, 50, 44, 40, 36} {
		tut.Resize(width, bodyH)
		g := newAttrGridWriter(width, bodyH)
		tut.Draw(g)

		y := -1
		for row := 0; row < bodyH && y < 0; row++ {
			if strings.Contains(g.rowRunes(row), "Bravo") {
				y = row
			}
		}
		require.GreaterOrEqual(t, y, 0, "width=%d: the options must be drawn", width)

		buttons, inButton := 0, false
		for _, attr := range g.rowAttrs(y) {
			if attr.Bg == term.ColorBlue {
				if !inButton {
					buttons++
				}
				inButton = true
				continue
			}
			inButton = false
		}
		assert.Equal(t, len(options), buttons,
			"width=%d: every button must be a run of its own, "+
				"with at least a cell of air between them", width)
	}
}

// TestChoiceOnSelectResolvesResult asserts that selecting an option
// the choice_result attributes.
func TestChoiceOnSelectResolvesResult(t *testing.T) {
	t.Parallel()
	src := `
def run():
    pick = choice(message="pick one", options=["A", "B", "C"])
    notify(message="value=" + pick.value + " idx=" + str(pick.index))
tutorial(entry=run)
`
	tut, notis := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	require.Equal(t, "choice", activeKindFor(tut))
	_, _ = tut.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowRight})
	_, _ = tut.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	waitFinished(t, tut, time.Second)
	assert.True(t, notis.containsSubstring("value=B idx=1"),
		"selecting option 1 must populate value and index, got %v",
		notis.renderedCalls())
}

// TestChoiceEscWithoutSelectIsDismissal asserts that Esc
// alone yields selected=False, index=-1, value="".
func TestChoiceEscWithoutSelectIsDismissal(t *testing.T) {
	t.Parallel()
	src := `
def run():
    pick = choice(message="pick", options=["x"])
    if not pick.selected:
        notify(message="dismissed idx=" + str(pick.index) + " v=" + repr(pick.value))
tutorial(entry=run)
`
	tut, notis := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	require.Equal(t, "choice", activeKindFor(tut))
	_, _ = tut.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
	waitFinished(t, tut, time.Second)
	assert.True(t, notis.containsSubstring(`dismissed idx=-1 v=""`),
		"Esc alone must produce a dismissal sentinel, got %v",
		notis.renderedCalls())
}

// TestChoiceEscAfterEnterIsNoOp asserts that delivering Esc after
// Enter has already finalised the request is a safe no-op: the
// first selection wins via sync.Once on r.deliver.
func TestChoiceEscAfterEnterIsNoOp(t *testing.T) {
	t.Parallel()
	src := `
def run():
    pick = choice(message="pick", options=["A", "B"])
    notify(message="value=" + pick.value)
    notify(message="done")
tutorial(entry=run)
`
	tut, notis := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	require.Equal(t, "choice", activeKindFor(tut))
	_, _ = tut.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	// After Enter has resolved the prompt the runtime advances
	// past choice; a stray Esc on a different active step (or none)
	// must not corrupt the recorded selection.
	_, _ = tut.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
	waitFinished(t, tut, time.Second)
	assert.True(t, notis.containsSubstring("value=A"),
		"the initial Enter selection must win the delivery")
}

// TestChoiceBranchesInStarlark asserts that a choice result drives a
// real if/elif branch in Starlark.
func TestChoiceBranchesInStarlark(t *testing.T) {
	t.Parallel()
	src := `
def run():
    pick = choice(message="pick", options=["go", "stay"])
    if pick.value == "go":
        wait_event(event="open", text="go path")
    else:
        wait_event(event="open", text="stay path")
tutorial(entry=run)
`
	tut, _ := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	require.Equal(t, "choice", activeKindFor(tut))
	_, _ = tut.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})

	waitNextActive(t, tut, "wait_event", time.Second)
	assert.Equal(t, "go path", tut.ActiveText(),
		"the `go` branch must reach the step with the right copy")
	tut.Stop()
}

// TestCancelOnDismissShortCircuits asserts that cancel_on_dismiss
// ends the entry on a dismissed choice.
func TestCancelOnDismissShortCircuits(t *testing.T) {
	t.Parallel()
	src := `
def run():
    pick = cancel_on_dismiss(choice(message="pick", options=["x"]))
    notify(message="unreachable: " + pick.value)
tutorial(entry=run)
`
	tut, notis := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	require.Equal(t, "choice", activeKindFor(tut))
	_, _ = tut.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
	waitFinished(t, tut, time.Second)
	assert.False(t, tut.Completed())
	assert.False(t, notis.containsSubstring("unreachable"),
		"cancel_on_dismiss must exit before the next notify runs")
}

// TestCancelOnDismissPassesThroughOnSelect asserts that cancel_on_dismiss
// returns the result unchanged on a real selection.
func TestCancelOnDismissPassesThroughOnSelect(t *testing.T) {
	t.Parallel()
	src := `
def run():
    pick = cancel_on_dismiss(choice(message="pick", options=["A"]))
    notify(message="picked " + pick.value)
tutorial(entry=run)
`
	tut, notis := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	require.Equal(t, "choice", activeKindFor(tut))
	_, _ = tut.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	waitFinished(t, tut, time.Second)
	assert.True(t, notis.containsSubstring("picked A"),
		"selected result must pass through cancel_on_dismiss")
}

// TestExitTerminatesTutorialCleanly asserts that exit() ends the
// entry without surfacing an error notification.
func TestExitTerminatesTutorialCleanly(t *testing.T) {
	t.Parallel()
	src := `
def run():
    notify(level=info, message="before")
    exit()
    notify(level=info, message="after")
tutorial(entry=run)
`
	tut, notis := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	waitFinished(t, tut, time.Second)
	assert.False(t, tut.Completed())
	assert.True(t, notis.containsSubstring("before"))
	assert.False(t, notis.containsSubstring("after"),
		"exit() must skip the rest of the entry")
	for _, c := range notis.captured {
		assert.NotEqual(t, browserapi.LevelError, c.level,
			"exit() must be a clean exit, not surface an error")
	}
}

// TestExitFromNestedHelper asserts that exit() called from a helper
// terminates the whole tutorial.
func TestExitFromNestedHelper(t *testing.T) {
	t.Parallel()
	src := `
def helper():
    exit()

def run():
    helper()
    notify(message="never")
tutorial(entry=run)
`
	tut, notis := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	waitFinished(t, tut, time.Second)
	assert.False(t, notis.containsSubstring("never"),
		"exit() must propagate out of nested calls")
}

// TestStopCancelsBlockedBuiltin asserts that Stop() unblocks a
// builtin that is waiting on user input.
func TestStopCancelsBlockedBuiltin(t *testing.T) {
	t.Parallel()
	src := `
def run():
    wait_event(event="open")
    notify(message="never")
tutorial(entry=run)
`
	tut, notis := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	require.Equal(t, "wait_event", activeKindFor(tut))

	done := make(chan struct{})
	go func() {
		tut.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop() did not return within 1s; runLoop is stuck")
	}
	assert.False(t, tut.Completed())
	assert.False(t, notis.containsSubstring("never"),
		"Stop() must skip the rest of the entry")
}

// TestResetRestartsThread asserts that Reset() can be called twice
// and the entry runs from the start each time.
func TestResetRestartsThread(t *testing.T) {
	t.Parallel()
	src := `
def run():
    notify(message="run")
tutorial(entry=run)
`
	tut, notis := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	waitFinished(t, tut, time.Second)
	require.Equal(t, 1, notis.len())

	resetAndWait(t, tut, time.Second)
	waitFinished(t, tut, time.Second)
	assert.Equal(t, 2, notis.len(),
		"Reset() must re-run the entry from the beginning")
}

// TestStarlarkFailSurfacedAsNotification asserts that fail() in the
// entry surfaces as an error notification and the tutorial exits.
func TestStarlarkFailSurfacedAsNotification(t *testing.T) {
	t.Parallel()
	src := `
def run():
    fail("bad")
tutorial(entry=run)
`
	tut, notis := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	waitFinished(t, tut, time.Second)
	assert.False(t, tut.Completed())
	require.Greater(t, notis.len(), 0)
	found := false
	for _, c := range notis.captured {
		if c.level == browserapi.LevelError &&
			strings.Contains(c.rendered(), "bad") {
			found = true
		}
	}
	assert.True(t, found,
		"fail() must surface as an error notification, got %v",
		notis.renderedCalls())
}

// TestStarlarkRuntimeErrorSurfaced asserts that a runtime error
// (e.g. indexing None) is reported as an error notification.
func TestStarlarkRuntimeErrorSurfaced(t *testing.T) {
	t.Parallel()
	src := `
def run():
    x = None
    notify(message=x[0])
tutorial(entry=run)
`
	tut, notis := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	waitFinished(t, tut, time.Second)
	assert.False(t, tut.Completed())
	require.Greater(t, notis.len(), 0)
	hasError := false
	for _, c := range notis.captured {
		if c.level == browserapi.LevelError {
			hasError = true
		}
	}
	assert.True(t, hasError,
		"runtime error must surface as LevelError notification")
}

// TestCommandKeyBuiltin asserts that command_key() returns the
// configured prompt key.
func TestCommandKeyBuiltin(t *testing.T) {
	t.Parallel()
	src := `
def run():
    notify(message="key=" + command_key())
tutorial(entry=run)
`
	tut, notis := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	waitFinished(t, tut, time.Second)
	wantKey := ":"
	assert.True(t, notis.containsSubstring("key="+wantKey),
		"command_key() must expand to the configured key, got %v",
		notis.renderedCalls())
}

// TestPrettyKeySpec asserts the display-only rename rewrites <shift-;>
// to ":" and passes everything else through unchanged.
func TestPrettyKeySpec(t *testing.T) {
	t.Parallel()
	assert.Equal(t, ":", PrettyKeySpec("<shift-;>"))
	assert.Equal(t, "<meta-n>", PrettyKeySpec("<meta-n>"))
	assert.Equal(t, "", PrettyKeySpec(""))
}

// newTutorialWith builds a Tutorial with an explicit editor mode and
// key_for lookup so tests can exercise editor_mode() and key_for().
func newTutorialWith(
	t *testing.T, src, mode string,
	keyFor func(string, []string) string,
) (*Tutorial, *fakeNotis) {
	t.Helper()
	notis := &fakeNotis{}
	tut, err := New(
		"tutorial-under-test", src,
		idetutorial.PromptStyle{}, nil, notis, nil,
		nil, nil,
		term.KeyComb{Ch: ':'},
		mode, "", keyFor,
		nil,
		nil,
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, tut)
	tut.Resize(80, 24)
	return tut, notis
}

// TestEditorModeBuiltin asserts that editor_mode() returns the
// injected mode string.
func TestEditorModeBuiltin(t *testing.T) {
	t.Parallel()
	src := `
def run():
    notify(message="mode=" + editor_mode())
tutorial(entry=run)
`
	tut, notis := newTutorialWith(t, src, "modal", nil)
	resetAndWait(t, tut, time.Second)
	waitFinished(t, tut, time.Second)
	assert.True(t, notis.containsSubstring("mode=modal"),
		"editor_mode() must expand to the injected mode, got %v",
		notis.renderedCalls())
}

// TestOSBuiltin asserts that os() returns the injected host OS string.
func TestOSBuiltin(t *testing.T) {
	t.Parallel()
	src := `
def run():
    notify(message="os=" + os())
tutorial(entry=run)
`
	notis := &fakeNotis{}
	tut, err := New(
		"tutorial-under-test", src,
		idetutorial.PromptStyle{}, nil, notis, nil,
		nil, nil,
		term.KeyComb{Ch: ':'},
		"standard", "darwin", nil,
		nil,
		nil,
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, tut)
	tut.Resize(80, 24)
	resetAndWait(t, tut, time.Second)
	waitFinished(t, tut, time.Second)
	assert.True(t, notis.containsSubstring("os=darwin"),
		"os() must expand to the injected OS, got %v",
		notis.renderedCalls())
}

// TestKeyForBuiltin asserts that key_for() returns the bound key,
// resolves the bare-command fallback, and yields "" when unbound.
func TestKeyForBuiltin(t *testing.T) {
	t.Parallel()
	lookup := func(cmd string, args []string) string {
		switch {
		case cmd == "windownew" && len(args) == 0:
			return "<meta-n>"
		case cmd == "windowfocus" && len(args) == 1 && args[0] == "left":
			return "<ctrl-x><ctrl-h>"
		default:
			return ""
		}
	}
	src := `
def run():
    notify(message="a=[" + key_for("windownew") + "]")
    notify(message="b=[" + key_for("windowfocus", "left") + "]")
    notify(message="c=[" + key_for("tabclose") + "]")
tutorial(entry=run)
`
	tut, notis := newTutorialWith(t, src, "standard", lookup)
	resetAndWait(t, tut, time.Second)
	waitFinished(t, tut, time.Second)
	assert.True(t, notis.containsSubstring("a=[<meta-n>]"),
		"key_for(windownew) must return its bound key, got %v",
		notis.renderedCalls())
	assert.True(t, notis.containsSubstring("b=[<ctrl-x><ctrl-h>]"),
		"key_for(windowfocus, left) must return its chord, got %v",
		notis.renderedCalls())
	assert.True(t, notis.containsSubstring("c=[]"),
		"key_for for an unbound command must return empty, got %v",
		notis.renderedCalls())
}

// TestKeyForBuiltinNilLookup asserts that key_for() returns "" when no
// lookup func is wired.
func TestKeyForBuiltinNilLookup(t *testing.T) {
	t.Parallel()
	src := `
def run():
    notify(message="k=[" + key_for("windownew") + "]")
tutorial(entry=run)
`
	tut, notis := newTutorialWith(t, src, "standard", nil)
	resetAndWait(t, tut, time.Second)
	waitFinished(t, tut, time.Second)
	assert.True(t, notis.containsSubstring("k=[]"),
		"key_for must return empty when the lookup func is nil, got %v",
		notis.renderedCalls())
}

// newTutorialWorkspace builds a Tutorial with an explicit workspace_open
// closure so tests can exercise the workspace_open() builtin.
func newTutorialWorkspace(
	t *testing.T, src string, workspaceOpen func() bool,
) (*Tutorial, *fakeNotis) {
	t.Helper()
	notis := &fakeNotis{}
	tut, err := New(
		"tutorial-under-test", src,
		idetutorial.PromptStyle{}, nil, notis, nil,
		nil, nil,
		term.KeyComb{Ch: ':'},
		"standard", "", nil,
		nil,
		workspaceOpen,
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, tut)
	tut.Resize(80, 24)
	return tut, notis
}

// TestWorkspaceOpenBuiltin asserts that workspace_open() returns the
// wired closure's value and returns True when no closure is wired.
func TestWorkspaceOpenBuiltin(t *testing.T) {
	t.Parallel()
	src := `
def run():
    notify(message="open=" + str(workspace_open()))
tutorial(entry=run)
`
	cases := []struct {
		name          string
		workspaceOpen func() bool
		want          string
	}{
		{name: "open", workspaceOpen: func() bool { return true }, want: "open=True"},
		{name: "closed", workspaceOpen: func() bool { return false }, want: "open=False"},
		{name: "nil defaults to open", workspaceOpen: nil, want: "open=True"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tut, notis := newTutorialWorkspace(t, src, tc.workspaceOpen)
			resetAndWait(t, tut, time.Second)
			waitFinished(t, tut, time.Second)
			assert.True(t, notis.containsSubstring(tc.want),
				"workspace_open() must expand to %q, got %v",
				tc.want, notis.renderedCalls())
		})
	}
}

// TestConfigPathBuiltin asserts config_path() reports the file the
// host wired, which follows the data directory, and is empty when no
// host wired one.
func TestConfigPathBuiltin(t *testing.T) {
	t.Parallel()
	src := `
def run():
    notify(message="config=" + config_path())
tutorial(entry=run)
`
	cases := []struct {
		name string
		opts []Option
		want string
	}{
		{
			name: "wired",
			opts: []Option{WithConfigPath("/data/rune-alt/rune.star")},
			want: "config=/data/rune-alt/rune.star",
		},
		{name: "unwired", want: "config="},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			notis := &fakeNotis{}
			tut, err := New(
				"tutorial-under-test", src,
				idetutorial.PromptStyle{}, nil, notis, nil,
				nil, nil,
				term.KeyComb{Ch: ':'},
				"standard", "", nil,
				nil, nil, nil,
				tc.opts...,
			)
			require.NoError(t, err)
			tut.Resize(80, 24)
			resetAndWait(t, tut, time.Second)
			waitFinished(t, tut, time.Second)
			assert.True(t, notis.containsSubstring(tc.want),
				"config_path() must expand to %q, got %v",
				tc.want, notis.renderedCalls())
		})
	}
}

// newTutorialLSP builds a Tutorial with an explicit lspServerRunning
// closure so tests can exercise the is_lsp_server_running() builtin.
func newTutorialLSP(
	t *testing.T, src string, lspServerRunning func() bool,
) (*Tutorial, *fakeNotis) {
	t.Helper()
	notis := &fakeNotis{}
	tut, err := New(
		"tutorial-under-test", src,
		idetutorial.PromptStyle{}, nil, notis, nil,
		nil, nil,
		term.KeyComb{Ch: ':'},
		"standard", "", nil,
		nil,
		nil,
		lspServerRunning,
	)
	require.NoError(t, err)
	require.NotNil(t, tut)
	tut.Resize(80, 24)
	return tut, notis
}

// TestLSPServerRunningBuiltin asserts that is_lsp_server_running()
// returns the wired closure's value and returns True when no closure is
// wired.
func TestLSPServerRunningBuiltin(t *testing.T) {
	t.Parallel()
	src := `
def run():
    notify(message="lsp=" + str(is_lsp_server_running()))
tutorial(entry=run)
`
	cases := []struct {
		name             string
		lspServerRunning func() bool
		want             string
	}{
		{name: "running", lspServerRunning: func() bool { return true }, want: "lsp=True"},
		{name: "not running", lspServerRunning: func() bool { return false }, want: "lsp=False"},
		{name: "nil defaults to running", lspServerRunning: nil, want: "lsp=True"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tut, notis := newTutorialLSP(t, src, tc.lspServerRunning)
			resetAndWait(t, tut, time.Second)
			waitFinished(t, tut, time.Second)
			assert.True(t, notis.containsSubstring(tc.want),
				"is_lsp_server_running() must expand to %q, got %v",
				tc.want, notis.renderedCalls())
		})
	}
}

// TestStopUnblocksWhileFinalizingOnTUI is a regression test for a
// deadlock: a tutorial that finishes with a runtime error surfaces the
// error via runOnTUI, which blocks on the host event loop. If the host
// event loop concurrently calls Stop (e.g. a command observer tears the
// tutorial down), Stop waits for the run goroutine to exit while the run
// goroutine's runOnTUI waits for the event loop to drain the queued
// notification — a cycle. Stop must return without waiting for a tick
// that the wedged loop can never deliver.
func TestStopUnblocksWhileFinalizingOnTUI(t *testing.T) {
	t.Parallel()

	// queued holds callbacks scheduled onto the "event loop" but never
	// runs them: this models the loop being wedged inside Stop, so the
	// finalizing runOnTUI cannot complete on its own.
	queued := make(chan func(), 8)
	sched := func(fn func()) bool {
		queued <- fn
		return true
	}
	notis := &fakeNotis{}
	tut, err := New(
		"deadlock", "def run():\n    fail('boom')\ntutorial(entry=run)\n",
		idetutorial.PromptStyle{}, nil, notis, nil,
		sched, nil,
		term.KeyComb{Ch: ':'},
		"standard", "", nil,
		nil,
		nil,
		nil,
	)
	require.NoError(t, err)
	tut.Resize(80, 24)

	tut.Reset()

	// The fail() surfaces through handleRunResult -> runOnTUI, which
	// queues the error notification and blocks. Wait for that queued
	// callback to prove the run goroutine is parked in runOnTUI.
	select {
	case <-queued:
	case <-time.After(time.Second):
		t.Fatal("finalizing runOnTUI never scheduled its notification")
	}

	// Stop is called from the same logical event loop that owns the
	// queued callback, so nothing will drain it. Stop must still return.
	done := make(chan struct{})
	go func() {
		tut.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop deadlocked waiting for a wedged event loop tick")
	}
}

// TestWaitCommandHintExpandsCmdToken asserts that <cmd> in the step's
// copy expands to the configured key, and that a failed dispatch
// leaves that copy exactly as the user was reading it.
func TestWaitCommandHintExpandsCmdToken(t *testing.T) {
	t.Parallel()
	src := `
def run():
    wait_command(command="wopen", text="` + "`<cmd>wopen` `<directory>`" + `")
tutorial(entry=run)
`
	tut, _ := newTutorial(t, src)
	tut.Resize(80, 60)
	resetAndWait(t, tut, time.Second)

	draw := func() string {
		t.Helper()
		w := term.NewStringWriter(80, 60)
		tut.Draw(w)
		require.NoError(t, w.Flush())
		return w.String()
	}

	wantKey := PrettyKeySpec((term.KeyComb{Ch: ':'}).String())
	before := draw()
	assert.Contains(t, before, wantKey,
		"the hint must mention the configured command key")
	assert.Contains(t, before, "wopen",
		"the hint must mention the command name")
	assert.Contains(t, before, "<directory>",
		"the hint must preserve author placeholders")
	assert.NotContains(t, before, "<cmd>",
		"<cmd> token must not leak into the rendered hint")

	tut.ObserveCommand("wopen", "wopen", nil,
		fmt.Errorf("missing directory argument"))
	assert.Equal(t, "wait_command", activeKindFor(tut),
		"a dispatch error must keep the step armed")
	assert.Equal(t, before, draw(),
		"a dispatch error must leave the step's copy unchanged so the "+
			"instructions the user is reading do not move")
	tut.Stop()
}

// TestScreenDrawsTitleAsHeading asserts that a step's title heads its
// copy in the tile body, since the tile has no title bar, and that
// markdown headers render without the leading `#` markup glyphs.
func TestScreenDrawsTitleAsHeading(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		src  string
	}{
		{
			name: "wait_command",
			src:  `wait_command(command="wopen", title="WelcomeSentinel", text="## SubSentinel\n\nbody")`,
		},
		{
			name: "wait_shell",
			src:  `wait_shell(args=["pkg"], title="WelcomeSentinel", text="## SubSentinel\n\nbody")`,
		},
		{
			name: "wait_event",
			src:  `wait_event(event="open", title="WelcomeSentinel", text="## SubSentinel\n\nbody")`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tut, _ := newTutorial(t, "def run():\n    "+tc.src+"\ntutorial(entry=run)\n")
			tut.Resize(60, bodyH)
			resetAndWait(t, tut, time.Second)
			defer tut.Stop()

			g := newGridWriter(60, bodyH)
			tut.Draw(g)
			assert.True(t, gridContains(g, "WelcomeSentinel"),
				"the title must be drawn in the body")
			assert.True(t, gridContains(g, "SubSentinel"),
				"the copy's own headings must be drawn too")
			assert.True(t, gridContains(g, "body"))
			assert.False(t, gridContains(g, "# "),
				"headings must not render their leading '# '")
			assert.Less(t, gridRow(g, "WelcomeSentinel"), gridRow(g, "SubSentinel"),
				"the title must come before the copy")
		})
	}
}

// gridRow returns the first row of g containing needle, or -1.
func gridRow(g *gridWriter, needle string) int {
	for y := range g.h {
		if strings.Contains(string(g.row(y)), needle) {
			return y
		}
	}
	return -1
}

// TestStarlarkControlFlow asserts that the entry can use for, if,
// and helper functions to drive a sequence of builtins.
func TestStarlarkControlFlow(t *testing.T) {
	t.Parallel()
	src := `
def banner(msg, lvl=info):
    notify(level=lvl, message=msg)

def run():
    for i in range(3):
        if i % 2 == 0:
            banner("even-%d" % i, lvl=success)
        else:
            banner("odd-%d" % i, lvl=warn)
tutorial(entry=run)
`
	tut, notis := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	waitFinished(t, tut, time.Second)
	require.Equal(t, 3, notis.len())
	assert.Equal(t, "even-0", notis.captured[0].rendered())
	assert.Equal(t, browserapi.LevelSuccess, notis.captured[0].level)
	assert.Equal(t, "odd-1", notis.captured[1].rendered())
	assert.Equal(t, browserapi.LevelWarn, notis.captured[1].level)
	assert.Equal(t, "even-2", notis.captured[2].rendered())
}

// TestLoadIsRejected asserts that load() is not allowed in tutorial
// scripts.
func TestLoadIsRejected(t *testing.T) {
	t.Parallel()
	_, err := New(
		"x", `load("other.star", "thing")`,
		idetutorial.PromptStyle{}, nil, nil, nil,
		nil, nil, term.KeyComb{Ch: ':'},
		"standard", "", nil,
		nil,
		nil,
		nil,
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "load()")
}

// TestEveryBlockingKindRoundTrips asserts that each blocking builtin
// can be invoked and resolved through its own milestone.
func TestEveryBlockingKindRoundTrips(t *testing.T) {
	t.Parallel()
	src := `
def run():
    wait_event(event="open")
    wait_shell(args=["pkg", "install", "x"])
    if confirm(message="sure?"):
        pick = choice(message="pick", options=["a", "b"])
        r = wait_command(command="wopen")
        notify(message="got " + pick.value + " " + r.args[0])
tutorial(entry=run)
`
	tut, notis := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)

	require.Equal(t, "wait_event", activeKindFor(tut))
	assert.False(t, tut.Finished())
	tut.ObserveEvent("open", "file:///x")
	waitNextActive(t, tut, "wait_shell", time.Second)
	tut.ObserveCommand("console", "console", []string{"pkg", "install", "x"}, nil)
	waitNextActive(t, tut, "confirm", time.Second)
	_, _ = tut.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	waitNextActive(t, tut, "choice", time.Second)
	_, _ = tut.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowRight})
	_, _ = tut.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	waitNextActive(t, tut, "wait_command", time.Second)
	tut.ObserveCommand("wopen", "wopen", []string{"~/p"}, nil)
	waitFinished(t, tut, time.Second)
	assert.True(t, tut.Finished())
	assert.True(t, notis.containsSubstring("got b ~/p"), "%v", notis.renderedCalls())
}

// TestWaitScreensNeverExitOrResolveFromKeys asserts that a wait_*
// step is only ever ended by its milestone: none of the keys that used
// to dismiss a copy screen resolve it, and none report exit, which
// would close the tile.
func TestWaitScreensNeverExitOrResolveFromKeys(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		src  string
	}{
		{name: "wait_command", src: `wait_command(command="wopen", text="body")`},
		{name: "wait_shell", src: `wait_shell(args=["pkg"], text="body")`},
		{name: "wait_event", src: `wait_event(event="open", text="body")`},
	}
	keys := []term.Event{
		{Type: term.EventKey, Key: term.KeyEnter},
		{Type: term.EventKey, Key: term.KeySpace, Ch: ' '},
		{Type: term.EventKey, Key: term.KeyEsc},
		{Type: term.EventKey, Ch: 'q'},
		{Type: term.EventKey, Ch: ':'},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tut, _ := newTutorial(t, "def run():\n    "+tc.src+"\ntutorial(entry=run)\n")
			resetAndWait(t, tut, time.Second)
			defer tut.Stop()
			for _, ev := range keys {
				exit, _ := tut.Handle(ev)
				assert.False(t, exit, "%v must not exit", ev.KeyComb())
				assert.Equal(t, tc.name, activeKindFor(tut),
					"%v must not resolve the step", ev.KeyComb())
			}
			exit, handled := tut.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
			assert.False(t, exit)
			assert.False(t, handled, "a dropped quit key must fall through")
		})
	}
}

// TestScrollableFollowsTheActiveScreen asserts the tutorial scrolls
// like any other browser tile: a long wait_* screen seeks through its
// viewer, while a prompt screen has nothing to scroll.
func TestScrollableFollowsTheActiveScreen(t *testing.T) {
	t.Parallel()
	long := strings.Repeat(`line\n\n`, 40)
	src := `
def run():
    wait_command(command="wopen", text="` + long + `")
    confirm(message="done?")
tutorial(entry=run)
`
	tut, _ := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	defer tut.Stop()

	require.Equal(t, "wait_command", activeKindFor(tut))
	assert.Positive(t, tut.MaxSeekOffset())
	assert.Equal(t, 0, tut.SeekOffset())
	assert.True(t, tut.SeekDown())
	assert.Equal(t, 1, tut.SeekOffset())
	assert.True(t, tut.SeekUp())
	assert.False(t, tut.SeekUp(), "already at the top")

	tut.ObserveCommand("wopen", "wopen", nil, nil)
	waitNextActive(t, tut, "confirm", time.Second)
	assert.Equal(t, 0, tut.MaxSeekOffset())
	assert.False(t, tut.SeekDown())
}

// TestPromptRendersOnTutorialLayer asserts that the confirm/choice
// prompt is drawn by the tutorial itself, not by the IDE host.
// Drawing the tutorial alone into a buffer must paint the prompt's
// message and options — that is what makes the prompt survive a
// workspace switch without the IDE host needing any per-tutorial
// prompter integration.
func TestPromptRendersOnTutorialLayer(t *testing.T) {
	t.Parallel()
	src := `
def run():
    confirm("continue?")
tutorial(entry=run)
`
	tut, _ := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	require.Equal(t, "confirm", activeKindFor(tut))

	g := newGridWriter(bodyW, bodyH)
	tut.Draw(g)
	assert.True(t, gridContains(g, "continue?"),
		"prompt message must be rendered on the tutorial layer")
	assert.True(t, gridContains(g, "Yes"),
		"prompt Yes option must be rendered on the tutorial layer")
	assert.True(t, gridContains(g, "No"),
		"prompt No option must be rendered on the tutorial layer")
	tut.Stop()
}

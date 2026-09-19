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

	"unstable.build/rune/internal/browser"
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
	overlay := idetutorial.NewOverlayBrowser(
		browser.NewComponent(idetutorial.DefaultOverlayBrowserConfig()))
	tut, err := New(
		"tutorial-under-test", src,
		overlay, nil, notis, nil,
		term.Attributes{},
		nil, nil,
		term.KeyComb{Ch: ':'},
		"standard", "", nil,
		nil,
		nil,
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, tut)
	tut.Resize(80, 24)
	return tut, notis
}

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

// resetAndWaitNamed is the same as resetAndWait but takes a label
// so a test that resets multiple tutorials can tell them apart in
// failure messages.
func resetAndWaitNamed(t *testing.T, tut *Tutorial, d time.Duration, name string) {
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
			t.Fatalf("entry %q never published a request within %s", name, d)
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
		nil, nil, nil, nil,
		term.Attributes{},
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
		nil, nil, nil, nil,
		term.Attributes{},
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
		nil, nil, nil, nil,
		term.Attributes{},
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
		nil, nil, nil, nil,
		term.Attributes{},
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
		nil, nil, nil, nil,
		term.Attributes{},
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
    floating_window(text="ok")
tutorial(entry=run)
`
	tut, notis := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	assert.Equal(t, "floating_window", activeKindFor(tut),
		"notify must not block the runtime; floating_window must "+
			"be active immediately after Reset")
	assert.Equal(t, 1, notis.len(),
		"notify must fire eagerly even while floating_window is active")
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

// TestFloatingWindowBlocksUntilEnter asserts that floating_window
// stays active until Enter is delivered, then the next builtin
// becomes active.
func TestFloatingWindowBlocksUntilEnter(t *testing.T) {
	t.Parallel()
	src := `
def run():
    floating_window(text="hi", title="welcome")
    floating_window(text="next")
tutorial(entry=run)
`
	tut, _ := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	require.Equal(t, "floating_window", activeKindFor(tut))

	exit, handled := tut.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	assert.False(t, exit, "first dismissal must not finish the tutorial")
	assert.True(t, handled)
	waitNextActive(t, tut, "floating_window", time.Second)

	exit, handled = tut.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	assert.True(t, handled)
	waitFinished(t, tut, time.Second)
	assert.True(t, exit || tut.finished,
		"tutorial must exit after final builtin")
}

// TestFloatingWindowSwallowsStrayKeys asserts that stray keys (those
// not listed in allow_keys or dismiss_keys) do not advance and
// report handled=true so the IDE root never opens a command prompt
// under the overlay.
func TestFloatingWindowSwallowsStrayKeys(t *testing.T) {
	t.Parallel()
	src := `
def run():
    floating_window(text="hi")
tutorial(entry=run)
`
	tut, _ := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)

	exit, handled := tut.Handle(term.Event{Type: term.EventKey, Ch: ':'})
	assert.False(t, exit)
	assert.True(t, handled,
		"stray ':' on floating_window must be swallowed by the "+
			"tutorial so the IDE root never opens a prompt")
	assert.Equal(t, "floating_window", activeKindFor(tut),
		"stray key must not advance")
}

// TestFloatingWindowStrayKeyArmsHintPulse asserts that a stray key
// on floating_window arms the hint pulse shader.
func TestFloatingWindowStrayKeyArmsHintPulse(t *testing.T) {
	t.Parallel()
	src := `
def run():
    floating_window(text="hello", title="welcome")
tutorial(entry=run)
`
	tut, _ := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)

	_, ok := tut.Shader()
	require.False(t, ok, "fresh floating_window must not pulse")

	_, _ = tut.Handle(term.Event{Type: term.EventKey, Ch: 'x'})
	spec, ok := tut.Shader()
	require.True(t, ok, "wrong key must stage a hint pulse")
	assert.NotZero(t, spec.Width)
	assert.Equal(t, 1, spec.Height,
		"hint pulse should target the dismissal-hint row only")
}

// TestFloatingWindowAllowKeysPassesThrough asserts that keys listed in
// the allow_keys kwarg are NOT swallowed by the overlay: Handle reports
// handled=false so the IDE root can act on them (e.g. <meta-1>..<meta-9>
// while the welcome screen tells the user to try them).
func TestFloatingWindowAllowKeysPassesThrough(t *testing.T) {
	t.Parallel()
	src := `
def run():
    floating_window(
        text="press meta-1",
        allow_keys=["<meta-1>", "<meta-enter>"],
    )
tutorial(entry=run)
`
	tut, _ := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)

	// <meta-1> matches an allow_keys entry: must propagate.
	exit, handled := tut.Handle(term.Event{
		Type: term.EventKey,
		Mod:  term.ModMeta,
		Ch:   '1',
	})
	assert.False(t, exit)
	assert.False(t, handled,
		"allow_keys entries must fall through to the IDE root")
	assert.Equal(t, "floating_window", activeKindFor(tut),
		"allowed key must not advance the tutorial")

	// A key NOT in allow_keys is still swallowed.
	exit, handled = tut.Handle(term.Event{Type: term.EventKey, Ch: 'x'})
	assert.False(t, exit)
	assert.True(t, handled,
		"stray keys outside allow_keys must remain swallowed")
}

// TestFloatingWindowAllowKeysReclaimsEsc asserts that <esc> listed in
// allow_keys falls through to the IDE root WITHOUT dismissing the
// overlay, overriding the default Enter/Esc/Space dismissal. The
// modal-surfaces step relies on this so <esc> reaches a focused
// terminal or console (switching it to NORMAL mode) instead of
// closing the tutorial window.
func TestFloatingWindowAllowKeysReclaimsEsc(t *testing.T) {
	t.Parallel()
	src := `
def run():
    floating_window(text="press esc", allow_keys=["<esc>"])
    wait_command(command="wopen")
tutorial(entry=run)
`
	tut, _ := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	require.Equal(t, "floating_window", activeKindFor(tut))

	exit, handled := tut.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
	assert.False(t, exit)
	assert.False(t, handled,
		"<esc> in allow_keys must fall through to the IDE root")
	assert.Equal(t, "floating_window", activeKindFor(tut),
		"<esc> in allow_keys must NOT dismiss the overlay")
}

// TestFloatingWindowAllowKeysInvalidErrors asserts that bad key
// strings surface as a Starlark error at load time so authors find
// typos before users hit them.
func TestFloatingWindowAllowKeysInvalidErrors(t *testing.T) {
	t.Parallel()
	src := `
def run():
    floating_window(text="x", allow_keys=["<not-a-key>"])
tutorial(entry=run)
`
	tut, notis := newTutorial(t, src)
	tut.Reset()
	waitFinished(t, tut, 2*time.Second)
	require.Greater(t, notis.len(), 0,
		"invalid allow_keys must surface via notifications")
	assert.True(t, notis.containsSubstring("allow_keys"),
		"notification should mention allow_keys; got %v",
		notis.renderedCalls())
}

// TestFloatingWindowDismissKeysAdvancesAndFallsThrough asserts that
// keys listed in dismiss_keys resolve the floating_window AND let
// the event reach the IDE root, so a "Press `:`" floating_window
// can both advance the tutorial and open the command prompt the
// user pressed `:` to open.
func TestFloatingWindowDismissKeysAdvancesAndFallsThrough(t *testing.T) {
	t.Parallel()
	src := `
def run():
    floating_window(text="press colon", dismiss_keys=[":"])
    wait_command(command="wopen")
tutorial(entry=run)
`
	tut, _ := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	require.Equal(t, "floating_window", activeKindFor(tut))

	exit, handled := tut.Handle(term.Event{Type: term.EventKey, Ch: ':'})
	assert.False(t, exit)
	assert.False(t, handled,
		"dismiss_keys entries must fall through to the IDE root "+
			"so the action they describe actually fires")

	// The floating_window must have resolved; the run goroutine
	// should now be blocked on wait_command.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if activeKindFor(tut) == "wait_command" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	assert.Equal(t, "wait_command", activeKindFor(tut),
		"dismiss_keys entries must also advance the tutorial")
}

// TestFloatingWindowDismissKeysModifiedEnterFallsThrough guards that a
// modified Enter chord listed in dismiss_keys (e.g. <shift-meta-enter>,
// the companion-terminal binding) is matched against dismiss_keys and
// falls through to the IDE root, instead of being swallowed by the
// plain Enter/Space/Esc "continue" shortcut.
func TestFloatingWindowDismissKeysModifiedEnterFallsThrough(t *testing.T) {
	t.Parallel()
	src := `
def run():
    floating_window(text="press shift-meta-enter", dismiss_keys=["<shift-meta-enter>"])
    wait_command(command="!")
tutorial(entry=run)
`
	tut, _ := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	require.Equal(t, "floating_window", activeKindFor(tut))

	exit, handled := tut.Handle(term.Event{
		Type: term.EventKey,
		Key:  term.KeyEnter,
		Mod:  term.ModShiftMeta,
	})
	assert.False(t, exit)
	assert.False(t, handled,
		"a modified Enter in dismiss_keys must fall through to the IDE "+
			"root so the bound command (e.g. companion terminal) fires")

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if activeKindFor(tut) == "wait_command" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	assert.Equal(t, "wait_command", activeKindFor(tut),
		"a modified Enter in dismiss_keys must also advance the tutorial")
}

// TestFloatingWindowDismissKeysSequenceFirstChordFallsThrough guards the
// emacs `<c-x>` prefix family: a dismiss_keys entry that is a two-key
// sequence (e.g. "<c-x>3", the split-window binding) must register its
// first chord so pressing <c-x> dismisses the teaching window and falls
// through, letting the sequencer complete the <c-x>3 chord. Before the
// fix, parseKeyList rejected the sequence string outright and the whole
// floating_window step failed.
func TestFloatingWindowDismissKeysSequenceFirstChordFallsThrough(t *testing.T) {
	t.Parallel()
	src := `
def run():
    floating_window(text="press ctrl-x 3", dismiss_keys=["<c-x>3"])
    wait_command(command="windowdefaultsplit")
tutorial(entry=run)
`
	tut, _ := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	require.Equal(t, "floating_window", activeKindFor(tut))

	exit, handled := tut.Handle(term.Event{
		Type: term.EventKey,
		Mod:  term.ModCtrl,
		Ch:   'x',
	})
	assert.False(t, exit)
	assert.False(t, handled,
		"the first chord of a sequence dismiss_key must fall through to "+
			"the IDE root so the sequencer can complete the chord")

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if activeKindFor(tut) == "wait_command" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	assert.Equal(t, "wait_command", activeKindFor(tut),
		"a sequence dismiss_key's first chord must also advance the tutorial")
}

// TestFloatingWindowDismissForKeyForSequenceFallsThrough exercises the
// exact basics.star pattern: dismiss_keys is built from key_for(), which
// under the emacs preset resolves window commands to two-key `<c-x>`
// chords. Pressing the first chord must dismiss the window, fall through,
// and advance the following wait_command.
func TestFloatingWindowDismissForKeyForSequenceFallsThrough(t *testing.T) {
	t.Parallel()
	src := `
def run():
    k = key_for("windowdefaultsplit", "h")
    floating_window(text="split right", dismiss_keys=[command_key(), k])
    wait_command(command="windowdefaultsplit")
tutorial(entry=run)
`
	keyFor := func(cmd string, args []string) string {
		if cmd == "windowdefaultsplit" && len(args) == 1 && args[0] == "h" {
			return "<c-x>3"
		}
		return ""
	}
	tut, _ := newTutorialWith(t, src, "emacs", keyFor)
	resetAndWait(t, tut, time.Second)
	require.Equal(t, "floating_window", activeKindFor(tut))

	exit, handled := tut.Handle(term.Event{
		Type: term.EventKey, Mod: term.ModCtrl, Ch: 'x',
	})
	assert.False(t, exit)
	assert.False(t, handled,
		"key_for-resolved sequence dismiss_key must fall through on its "+
			"first chord so the sequencer can complete the chord")

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if activeKindFor(tut) == "wait_command" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	assert.Equal(t, "wait_command", activeKindFor(tut),
		"the tutorial must advance past the floating window")
}

// TestFloatingWindowAlignmentAndOffset asserts the (x, y) placement of
// the browser window for a handful of alignments. The overlay's
// window manager starts one row below the top of the screen (its
// single-row tab bar), so vertical expectations are offset by wmY.
func TestFloatingWindowAlignmentAndOffset(t *testing.T) {
	t.Parallel()
	const (
		screenW = 80
		screenH = 24
		wmY     = 1
	)
	probeTut, _ := newTutorial(t, `
def run():
    floating_window(text="hi")
tutorial(entry=run)
`)
	probeTut.Resize(screenW, screenH)
	resetAndWaitNamed(t, probeTut, time.Second, "probe")
	probeTut.mu.Lock()
	probeReq := probeTut.active
	probeTut.mu.Unlock()
	require.NotNil(t, probeReq, "probe must publish a floating window")
	_, innerW, innerH, ok := probeTut.winOverlay.WindowRect(probeReq.win)
	require.True(t, ok, "probe must open a live window")
	probeTut.Stop()

	cases := []struct {
		name      string
		alignment string
		offset    string
		wantX     int
		wantY     int
	}{
		{"bottom default", "", "", (screenW - innerW) / 2,
			screenH - innerH},
		{"center explicit", "center", "", (screenW - innerW) / 2,
			wmY + (screenH-wmY-innerH)/2},
		{"top-left flush", "top-left", "", 0, wmY},
		{"top-right flush", "top-right", "", screenW - innerW, wmY},
		{"bottom-left flush", "bottom-left", "", 0, screenH - innerH},
		{"bottom-right with offset", "bottom-right", "(3, 2)",
			screenW - innerW - 3, screenH - innerH - 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			alignArg := ""
			if tc.alignment != "" {
				alignArg = fmt.Sprintf(", alignment=%q", tc.alignment)
			}
			offsetArg := ""
			if tc.offset != "" {
				offsetArg = ", offset=" + tc.offset
			}
			src := fmt.Sprintf(`
def run():
    floating_window(text="hi"%s%s)
tutorial(entry=run)
`, alignArg, offsetArg)
			tut, _ := newTutorial(t, src)
			tut.Resize(screenW, screenH)
			resetAndWait(t, tut, time.Second)
			tut.mu.Lock()
			req := tut.active
			tut.mu.Unlock()
			require.NotNil(t, req)
			pos, _, _, ok := tut.winOverlay.WindowRect(req.win)
			require.True(t, ok, "step must open a live window")
			assert.Equal(t, tc.wantX, pos.X, "x")
			assert.Equal(t, tc.wantY, pos.Y, "y")
			tut.Stop()
		})
	}
}

// TestWaitStepAlignment asserts that wait_command and wait_event honour
// the alignment kwarg, so a lesson can keep its page and its hint on
// the same side of the screen instead of jumping between the two.
func TestWaitStepAlignment(t *testing.T) {
	t.Parallel()
	const (
		screenW = 80
		screenH = 24
		wmY     = 1
	)
	cases := []struct {
		name string
		step string
	}{
		{"wait_command", `wait_command(command="edit", alignment="top-left")`},
		{"wait_event", `wait_event(event="open", text="hi", alignment="top-left")`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src := fmt.Sprintf("def run():\n    %s\ntutorial(entry=run)\n", tc.step)
			tut, _ := newTutorial(t, src)
			tut.Resize(screenW, screenH)
			resetAndWait(t, tut, time.Second)
			tut.mu.Lock()
			req := tut.active
			tut.mu.Unlock()
			require.NotNil(t, req)
			pos, _, _, ok := tut.winOverlay.WindowRect(req.win)
			require.True(t, ok, "step must open a live hint window")
			assert.Equal(t, 0, pos.X, "x")
			assert.Equal(t, wmY, pos.Y, "y")
			tut.Stop()
		})
	}
}

// TestFloatingWindowAlignmentParses asserts that the alignment kwarg
// accepts every documented keyword.
func TestFloatingWindowAlignmentParses(t *testing.T) {
	t.Parallel()
	good := []string{
		"", "center", "centered", "top", "bottom", "left", "right",
		"top-left", "top-right", "bottom-left", "bottom-right",
	}
	for _, a := range good {
		src := fmt.Sprintf(`
def run():
    floating_window(text="hi", alignment=%q)
tutorial(entry=run)
`, a)
		notis := &fakeNotis{}
		tut, err := New(
			"align_"+a, src,
			nil, nil, notis, nil,
			term.Attributes{},
			nil, nil, term.KeyComb{Ch: ':'},
			"standard", "", nil,
			nil,
			nil,
			nil,
		)
		require.NoError(t, err, "alignment %q must parse", a)
		tut.Resize(80, 24)
		resetAndWait(t, tut, time.Second)
		tut.Stop()
	}

	src := `
def run():
    floating_window(text="hi", alignment="bogus")
tutorial(entry=run)
`
	notis := &fakeNotis{}
	tut, err := New(
		"align_bogus", src,
		nil, nil, notis, nil,
		term.Attributes{},
		nil, nil, term.KeyComb{Ch: ':'},
		"standard", "", nil,
		nil,
		nil,
		nil,
	)
	require.NoError(t, err, "unknown alignment must defer to the entry call")
	tut.Resize(80, 24)
	resetAndWait(t, tut, time.Second)
	// The bad alignment surfaces at runtime: the entry call fails
	// and the runtime emits an error notification.
	waitFinished(t, tut, time.Second)
	assert.True(t, notis.containsSubstring(`alignment "bogus"`),
		"want runtime error notification mentioning the bad alignment, "+
			"got %v", notis.renderedCalls())
}

// TestMarkdownBlocksUntilEnter asserts that markdown gates on
// Enter / Esc / Space and reports handled=true on stray keys.
func TestMarkdownBlocksUntilEnter(t *testing.T) {
	t.Parallel()
	src := `
def run():
    markdown(text="line one")
tutorial(entry=run)
`
	tut, _ := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	require.Equal(t, "markdown", activeKindFor(tut))

	exit, handled := tut.Handle(term.Event{Type: term.EventKey, Ch: 'z'})
	assert.False(t, exit)
	assert.False(t, handled,
		"markdown must not claim stray keys; root handles them")
	require.Equal(t, "markdown", activeKindFor(tut))

	_, _ = tut.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	waitFinished(t, tut, time.Second)
}

// TestWaitKeyBlocksUntilExactKey asserts that wait_key only advances
// on the exact key match.
func TestWaitKeyBlocksUntilExactKey(t *testing.T) {
	t.Parallel()
	src := `
def run():
    wait_key(key="<enter>")
tutorial(entry=run)
`
	tut, _ := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	require.Equal(t, "wait_key", activeKindFor(tut))

	exit, _ := tut.Handle(term.Event{Type: term.EventKey, Ch: 'a'})
	assert.False(t, exit)
	require.Equal(t, "wait_key", activeKindFor(tut))

	_, _ = tut.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	waitFinished(t, tut, time.Second)
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
    wait_command(command="wopen", on_error="` + "<cmd>" + `wopen <dir>")
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

// TestChoiceDrawsPromptOverlay asserts that choice() renders the
// message and each option label into the tutorial overlay grid.
func TestChoiceDrawsPromptOverlay(t *testing.T) {
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
	g := newGridWriter(80, 24)
	tut.Draw(g)
	tut.winOverlay.Draw(g)
	assert.True(t, gridContains(g, "pick one"),
		"choice overlay must render the message")
	for _, opt := range []string{"A", "B", "C"} {
		assert.True(t, gridContains(g, opt),
			"choice overlay must render option %q", opt)
	}
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

	g := newGridWriter(80, 24)
	tut.Draw(g)
	tut.winOverlay.Draw(g)
	assert.True(t, gridContains(g, " Alpha "),
		"option label must render with surrounding space padding")

	_, _ = tut.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	waitFinished(t, tut, time.Second)
	assert.True(t, notis.containsSubstring("value=[Alpha]"),
		"selected value must be the unpadded label, got %v",
		notis.renderedCalls())
}

// TestPromptUsesConfiguredStyling asserts that confirm/choice prompts
// pick up the overlay browser's prompt styling: the highlighted
// option is drawn with the configured HighlightAttr background,
// matching the IDE's browser-driven prompts instead of the SDK's
// reverse-video default.
func TestPromptUsesConfiguredStyling(t *testing.T) {
	t.Parallel()
	notis := &fakeNotis{}
	overlayCfg := idetutorial.DefaultOverlayBrowserConfig()
	overlayCfg.PromptConfig = browser.PromptConfig{
		HighlightAttr: term.Attributes{Bg: term.ColorRed, Fg: term.ColorWhite},
		MinWidth:      60,
	}
	overlay := idetutorial.NewOverlayBrowser(browser.NewComponent(overlayCfg))
	tut, err := New(
		"styled", `
def run():
    choice(message="pick", options=["Alpha", "Beta"])
tutorial(entry=run)
`,
		overlay, nil, notis, nil,
		term.Attributes{},
		nil, nil,
		term.KeyComb{Ch: ':'},
		"standard", "", nil,
		nil,
		nil,
		nil,
	)
	require.NoError(t, err)
	tut.Resize(80, 24)
	resetAndWait(t, tut, time.Second)
	require.Equal(t, "choice", activeKindFor(tut))

	g := newAttrGridWriter(80, 24)
	tut.Draw(g)
	tut.winOverlay.Draw(g)

	found := false
	for y := 0; y < 24 && !found; y++ {
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
        floating_window(text="go path")
    else:
        floating_window(text="stay path")
tutorial(entry=run)
`
	tut, _ := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	require.Equal(t, "choice", activeKindFor(tut))
	_, _ = tut.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})

	waitNextActive(t, tut, "floating_window", time.Second)
	tut.mu.Lock()
	body := tut.active.text
	tut.mu.Unlock()
	assert.Equal(t, "go path", body,
		"the `go` branch must reach the floating_window with the right body")
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
    wait_key(key="<f12>")
    notify(message="never")
tutorial(entry=run)
`
	tut, notis := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	require.Equal(t, "wait_key", activeKindFor(tut))

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
		nil, nil, notis, nil,
		term.Attributes{},
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
		nil, nil, notis, nil,
		term.Attributes{},
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
		nil, nil, notis, nil,
		term.Attributes{},
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

// newTutorialLSP builds a Tutorial with an explicit lspServerRunning
// closure so tests can exercise the is_lsp_server_running() builtin.
func newTutorialLSP(
	t *testing.T, src string, lspServerRunning func() bool,
) (*Tutorial, *fakeNotis) {
	t.Helper()
	notis := &fakeNotis{}
	tut, err := New(
		"tutorial-under-test", src,
		nil, nil, notis, nil,
		term.Attributes{},
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
		nil, nil, notis, nil,
		term.Attributes{},
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

// TestShaderClearsAfterFloatingWindow asserts that the hint pulse
// goes away once floating_window is dismissed.
func TestShaderClearsAfterFloatingWindow(t *testing.T) {
	t.Parallel()
	src := `
def run():
    floating_window(text="hello", title="welcome")
tutorial(entry=run)
`
	tut, _ := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)

	_, _ = tut.Handle(term.Event{Type: term.EventKey, Ch: 'x'})
	_, ok := tut.Shader()
	require.True(t, ok, "wrong key must arm the pulse")

	_, _ = tut.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	waitFinished(t, tut, time.Second)
	_, ok = tut.Shader()
	assert.False(t, ok,
		"after dismissal the shader must clear")
}

// TestWaitCommandHintExpandsCmdToken asserts that <cmd> in the
// default hint and in on_error expands to the configured key.
func TestWaitCommandHintExpandsCmdToken(t *testing.T) {
	t.Parallel()
	src := `
def run():
    wait_command(command="wopen", on_error="` + "`<cmd>wopen` `<directory>`" + `")
tutorial(entry=run)
`
	tut, _ := newTutorial(t, src)
	tut.Resize(80, 60)
	resetAndWait(t, tut, time.Second)

	w := term.NewStringWriter(80, 60)
	tut.Draw(w)
	tut.winOverlay.Draw(w)
	require.NoError(t, w.Flush())
	wantKey := PrettyKeySpec((term.KeyComb{Ch: ':'}).String())
	assert.Contains(t, w.String(), wantKey,
		"default hint must mention the configured command key")
	assert.Contains(t, w.String(), "wopen",
		"default hint must mention the command name")
	assert.NotContains(t, w.String(), "<cmd>",
		"<cmd> token must not leak into the default hint")

	tut.ObserveCommand("wopen", "wopen", nil,
		fmt.Errorf("missing directory argument"))
	w = term.NewStringWriter(80, 60)
	tut.Draw(w)
	tut.winOverlay.Draw(w)
	require.NoError(t, w.Flush())
	assert.Contains(t, w.String(), wantKey,
		"on_error hint must expand <cmd> to the configured key")
	assert.Contains(t, w.String(), "wopen",
		"on_error hint must mention the command name")
	assert.Contains(t, w.String(), "<directory>",
		"on_error hint must preserve author placeholders")
	tut.Stop()
}

// TestFloatingWindowDropsHeaderPrefix asserts that floating_window
// renders markdown headers without the leading `#` markup glyphs.
func TestFloatingWindowDropsHeaderPrefix(t *testing.T) {
	t.Parallel()
	src := `
def run():
    floating_window(text="# WelcomeSentinel\n\nbody")
tutorial(entry=run)
`
	tut, _ := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)

	g := newGridWriter(80, 24)
	tut.Draw(g)
	tut.winOverlay.Draw(g)
	assert.True(t, gridContains(g, "WelcomeSentinel"),
		"floating_window must render header text somewhere")
	assert.False(t, gridContains(g, "# WelcomeSentinel"),
		"floating_window must not render the leading '# '")
	tut.Stop()
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
		nil, nil, nil, nil,
		term.Attributes{},
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
// can be invoked and resolved through its TUI path.
func TestEveryBlockingKindRoundTrips(t *testing.T) {
	t.Parallel()
	src := `
def run():
    floating_window(text="fw")
    markdown(text="md")
    wait_key(key="<enter>")
    r = wait_command(command="wopen")
    notify(message="got " + r.args[0])
tutorial(entry=run)
`
	tut, notis := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)

	require.Equal(t, "floating_window", activeKindFor(tut))
	_, _ = tut.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	waitNextActive(t, tut, "markdown", time.Second)
	_, _ = tut.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	waitNextActive(t, tut, "wait_key", time.Second)
	_, _ = tut.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	waitNextActive(t, tut, "wait_command", time.Second)
	tut.ObserveCommand("wopen", "wopen", []string{"~/p"}, nil)
	waitFinished(t, tut, time.Second)
	assert.True(t, notis.containsSubstring("got ~/p"))
}

// TestPromptOverlayRendersOnTutorialLayer asserts that the
// confirm/choice prompt is drawn by the tutorial itself, not by the
// IDE host. Drawing the tutorial alone into a buffer must paint the
// prompt's message and options — that is what makes the overlay
// survive a workspace switch without the IDE host needing any
// per-tutorial prompter integration.
func TestPromptOverlayRendersOnTutorialLayer(t *testing.T) {
	t.Parallel()
	src := `
def run():
    confirm("continue?")
tutorial(entry=run)
`
	tut, _ := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	require.Equal(t, "confirm", activeKindFor(tut))

	g := newGridWriter(80, 24)
	tut.Draw(g)
	tut.winOverlay.Draw(g)
	assert.True(t, gridContains(g, "continue?"),
		"prompt message must be rendered on the tutorial layer")
	assert.True(t, gridContains(g, "Yes"),
		"prompt Yes option must be rendered on the tutorial layer")
	assert.True(t, gridContains(g, "No"),
		"prompt No option must be rendered on the tutorial layer")
	tut.Stop()
}

// TestSkipButtonRendersOnStepWindow asserts that every step window
// carries the "Skip" button on its last content row.
func TestSkipButtonRendersOnStepWindow(t *testing.T) {
	t.Parallel()
	src := `
def run():
    floating_window(text="hi")
tutorial(entry=run)
`
	tut, _ := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	require.Equal(t, "floating_window", activeKindFor(tut))

	g := newGridWriter(80, 24)
	tut.Draw(g)
	tut.winOverlay.Draw(g)
	assert.True(t, gridContains(g, "Skip"),
		"step window must render the skip button")
	tut.Stop()
}

// TestSkipButtonClickExitsTutorial asserts that clicking the button
// region of a floating_window step exits the whole tutorial, while a
// click elsewhere in the window does not.
func TestSkipButtonClickExitsTutorial(t *testing.T) {
	t.Parallel()
	src := `
def run():
    floating_window(text="hi")
    notify(message="reached")
tutorial(entry=run)
`
	tut, notis := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	tut.mu.Lock()
	req := tut.active
	tut.mu.Unlock()
	require.NotNil(t, req)
	pos, winW, winH, ok := tut.winOverlay.WindowRect(req.win)
	require.True(t, ok)

	// A press inside the window but off the button row (the frame
	// insets content by one cell) must not arm a skip.
	_, _ = tut.winOverlay.HandleMouse(term.Event{
		Type: term.EventMouse, Key: term.MouseLeft,
		MouseX: pos.X + 1, MouseY: pos.Y + 1,
	})
	assert.False(t, req.skipRequested.Load(),
		"click off the button must not arm a skip")
	_, _ = tut.winOverlay.HandleMouse(term.Event{
		Type: term.EventMouse, Key: term.MouseRelease,
		MouseX: pos.X + 1, MouseY: pos.Y + 1,
	})

	// The button occupies the last content row's rightmost cells.
	_, _ = tut.winOverlay.HandleMouse(term.Event{
		Type: term.EventMouse, Key: term.MouseLeft,
		MouseX: pos.X + winW - 2, MouseY: pos.Y + winH - 2,
	})
	require.True(t, req.skipRequested.Load(),
		"click on the button must arm a skip")

	exit, _ := tut.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
	assert.True(t, exit, "skip must exit the tutorial")
	waitFinished(t, tut, time.Second)
	assert.False(t, notis.containsSubstring("reached"),
		"steps after the skipped one must not run")
}

// TestSkipButtonClickOnWaitHint asserts the same button works on the
// non-modal hint window of a wait_* step, the surface that previously
// had no working exit affordance.
func TestSkipButtonClickOnWaitHint(t *testing.T) {
	t.Parallel()
	src := `
def run():
    wait_key(key="<f1>")
    notify(message="reached")
tutorial(entry=run)
`
	tut, notis := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	tut.mu.Lock()
	req := tut.active
	tut.mu.Unlock()
	require.NotNil(t, req)
	require.Equal(t, "wait_key", req.kind.String())
	pos, winW, winH, ok := tut.winOverlay.WindowRect(req.win)
	require.True(t, ok)

	_, _ = tut.winOverlay.HandleMouse(term.Event{
		Type: term.EventMouse, Key: term.MouseLeft,
		MouseX: pos.X + winW - 2, MouseY: pos.Y + winH - 2,
	})
	require.True(t, req.skipRequested.Load())

	exit, _ := tut.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
	assert.True(t, exit)
	waitFinished(t, tut, time.Second)
	assert.False(t, notis.containsSubstring("reached"))
}

// TestPromptSkipOptionExitsTutorial asserts that the appended "Skip
// tutorial" option on a choice prompt exits the whole tutorial rather
// than resolving the step with a value.
func TestPromptSkipOptionExitsTutorial(t *testing.T) {
	t.Parallel()
	src := `
def run():
    pick = choice(message="pick", options=["A", "B"])
    notify(message="reached")
tutorial(entry=run)
`
	tut, notis := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	require.Equal(t, "choice", activeKindFor(tut))

	// The appended skip option sits one past the last real option.
	_, _ = tut.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowRight})
	_, _ = tut.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowRight})
	exit, _ := tut.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	assert.True(t, exit, "skip option must exit the tutorial")
	waitFinished(t, tut, time.Second)
	assert.False(t, notis.containsSubstring("reached"),
		"steps after the skipped prompt must not run")
}

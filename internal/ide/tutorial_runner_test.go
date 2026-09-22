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
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"

	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/ide/idetutorial"
	"unstable.build/rune/internal/ide/idetutorial/starlarktutorial"
)

type rootStub struct {
	handled int
	exit    bool
}

func (r *rootStub) Resize(_, _ int)    {}
func (r *rootStub) Draw(_ term.Writer) {}
func (r *rootStub) Handle(_ term.Event) (bool, bool) {
	r.handled++
	return r.exit, true
}

func (r *rootStub) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return term.Coordinates{}, term.CursorStyleDefault, false
}
func (r *rootStub) Selection() (string, bool) { return "", false }

// exitRequested mirrors workspaceManagerHandler.exitRequested. The
// stub root has no key bindings, so nothing is a quit request.
func (r *rootStub) exitRequested(term.Event) bool { return false }

type tutStub struct {
	exitOn      rune
	handleCount int
	resetCount  int
	stopCount   int
	completed   bool
	commandExit bool
	eventExit   bool
	passThrough bool
}

func (t *tutStub) Resize(_, _ int)    {}
func (t *tutStub) Draw(_ term.Writer) {}
func (t *tutStub) Handle(ev term.Event) (bool, bool) {
	t.handleCount++
	return ev.Ch == t.exitOn, !t.passThrough
}
func (t *tutStub) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return term.Coordinates{}, term.CursorStyleDefault, false
}
func (t *tutStub) Selection() (string, bool) { return "", false }
func (t *tutStub) Reset()                    { t.resetCount++ }
func (t *tutStub) Stop()                     { t.stopCount++ }
func (t *tutStub) Completed() bool           { return t.completed }
func (t *tutStub) ObserveCommand(_, _ string, _ []string, _ error) bool {
	return t.commandExit
}
func (t *tutStub) ObserveEvent(_, _ string) bool {
	return t.eventExit
}
func (t *tutStub) Shader() (idetutorial.Shader, bool) {
	return idetutorial.Shader{}, false
}
func (t *tutStub) SetDefaultAttributes(_ term.Attributes) {}
func (t *tutStub) ComponentAt(_ term.Coordinates) (tui.Handler, bool) {
	return nil, false
}

func newTestRunner(tutorials map[string]idetutorial.Tutorial, onCompleted ...func(string)) (
	*tutorialRunner, *rootStub,
) {
	root := &rootStub{}
	r := &tutorialRunner{}
	var completed func(string)
	if len(onCompleted) > 0 {
		completed = onCompleted[0]
	}
	r.init(root, tutorials, nil, term.NopInterrupter(), completed,
		root.exitRequested)
	r.Resize(20, 5)
	return r, root
}

// TestTutorialRunnerRequiresExitRequested pins that the quit predicate
// is a required dependency: without it a tutorial overlay silently
// makes the IDE unquittable.
func TestTutorialRunnerRequiresExitRequested(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() {
		(&tutorialRunner{}).init(&rootStub{}, nil, nil,
			term.NopInterrupter(), nil, nil)
	})
}

// TestTutorialRunnerPropagatesRootExit guards the quit path: the IDE
// root's exit signal must survive the tutorial overlay frame.
func TestTutorialRunnerPropagatesRootExit(t *testing.T) {
	t.Parallel()
	tut := &tutStub{passThrough: true}
	r, root := newTestRunner(map[string]idetutorial.Tutorial{"basics": tut})
	root.exit = true
	require.NoError(t, r.HandleCommand(context.Background(),
		textapi.Command{Name: "tutorial", Args: []string{"start", "basics"}}))
	require.NotNil(t, r.overlay)

	exit, handled := r.Handle(term.Event{Type: term.EventKey, Ch: 'q'})
	assert.True(t, exit, "the root's exit must not be dropped by the overlay")
	assert.True(t, handled)
}

// TestTutorialRunnerExitRequestAbortsTutorial covers the case the
// overlay would otherwise swallow: a quit chord on a floating-window
// step must tear the tutorial down and reach the root, so the
// confirm-exit prompt is visible and answerable.
func TestTutorialRunnerExitRequestAbortsTutorial(t *testing.T) {
	t.Parallel()
	tut := &tutStub{}
	root := &rootStub{}
	r := &tutorialRunner{}
	var completed []string
	quit := term.Event{Type: term.EventKey, Ch: 'q', Mod: term.ModMeta}
	isQuit := func(ev term.Event) bool {
		return ev.Type == term.EventKey && ev.Ch == 'q' && ev.Mod == term.ModMeta
	}
	r.init(root, map[string]idetutorial.Tutorial{"basics": tut}, nil,
		term.NopInterrupter(),
		func(name string) { completed = append(completed, name) },
		isQuit)
	r.Resize(20, 5)
	require.NoError(t, r.HandleCommand(context.Background(),
		textapi.Command{Name: "tutorial", Args: []string{"start", "basics"}}))
	require.NotNil(t, r.overlay)

	_, handled := r.Handle(quit)
	assert.True(t, handled)
	assert.Nil(t, r.overlay, "a quit request must end the tutorial")
	assert.Equal(t, 1, root.handled, "quit must reach the IDE root")
	assert.Zero(t, tut.handleCount, "the tutorial must not swallow the quit chord")
	assert.Empty(t, completed, "an aborted tutorial is not completed")
}

func TestTutorialRunnerReportsSuccessfulCompletion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		tut  *tutStub
		exit func(*tutorialRunner)
	}{
		{
			name: "input",
			tut:  &tutStub{exitOn: 'q', completed: true},
			exit: func(r *tutorialRunner) {
				r.Handle(term.Event{Type: term.EventKey, Ch: 'q'})
			},
		},
		{
			name: "command observation",
			tut:  &tutStub{completed: true, commandExit: true},
			exit: func(r *tutorialRunner) {
				r.observeCommand("edit", "edit", nil, nil)
			},
		},
		{
			name: "event observation",
			tut:  &tutStub{completed: true, eventExit: true},
			exit: func(r *tutorialRunner) {
				r.observeEvent("open", "file:///example.go")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var r *tutorialRunner
			var completed []string
			clearedBeforeCallback := false
			r, _ = newTestRunner(map[string]idetutorial.Tutorial{"basics": tt.tut},
				func(name string) {
					clearedBeforeCallback = r.overlay == nil && r.activeName == ""
					completed = append(completed, name)
				})
			require.NoError(t, r.HandleCommand(context.Background(),
				textapi.Command{Name: "tutorial", Args: []string{"start", "basics"}}))

			tt.exit(r)

			assert.Equal(t, []string{"basics"}, completed)
			assert.True(t, clearedBeforeCallback)
			assert.Nil(t, r.overlay)
		})
	}
}

func TestTutorialRunnerDoesNotReportUnsuccessfulOrStoppedTutorial(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		tut  *tutStub
		exit func(*tutorialRunner) error
	}{
		{
			name: "unsuccessful exit",
			tut:  &tutStub{exitOn: 'q'},
			exit: func(r *tutorialRunner) error {
				r.Handle(term.Event{Type: term.EventKey, Ch: 'q'})
				return nil
			},
		},
		{
			name: "explicit stop",
			tut:  &tutStub{completed: true},
			exit: func(r *tutorialRunner) error {
				return r.HandleCommand(context.Background(),
					textapi.Command{Name: "tutorial", Args: []string{"stop"}})
			},
		},
		{
			name: "replacement",
			tut:  &tutStub{completed: true},
			exit: func(r *tutorialRunner) error {
				return r.HandleCommand(context.Background(),
					textapi.Command{Name: "tutorial", Args: []string{"start", "other"}})
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var completed []string
			r, _ := newTestRunner(map[string]idetutorial.Tutorial{
				"basics": tt.tut,
				"other":  &tutStub{},
			},
				func(name string) { completed = append(completed, name) })
			require.NoError(t, r.HandleCommand(context.Background(),
				textapi.Command{Name: "tutorial", Args: []string{"start", "basics"}}))
			require.NoError(t, tt.exit(r))
			assert.Empty(t, completed)
		})
	}
}

func TestTutorialRunnerStartsAndStopsOnExit(t *testing.T) {
	t.Parallel()
	tut := &tutStub{exitOn: 'q'}
	r, root := newTestRunner(map[string]idetutorial.Tutorial{"basics": tut})

	require.NoError(t, r.HandleCommand(context.Background(),
		textapi.Command{Name: "tutorial", Args: []string{"start", "basics"}}))
	assert.NotNil(t, r.overlay, "expected overlay after start")
	assert.Equal(t, "basics", r.activeName)
	assert.Equal(t, 1, tut.resetCount,
		"runner should Reset the tutorial on every dispatch")

	_, handled := r.Handle(term.Event{Type: term.EventKey, Ch: 'x'})
	assert.True(t, handled)
	assert.Equal(t, 1, tut.handleCount)
	assert.Equal(t, 0, root.handled,
		"root should not see events while tutorial is active")

	_, handled = r.Handle(term.Event{Type: term.EventKey, Ch: 'q'})
	assert.True(t, handled)
	assert.Nil(t, r.overlay)
	assert.Equal(t, "", r.activeName)

	_, _ = r.Handle(term.Event{Type: term.EventKey, Ch: 'y'})
	assert.Equal(t, 1, root.handled)

	require.NoError(t, r.HandleCommand(context.Background(),
		textapi.Command{Name: "tutorial", Args: []string{"start", "basics"}}))
	// Each dispatch installs a fresh overlay and calls Reset once.
	// The runner used to also call Reset during clearActive to
	// release per-step resources; that's now Stop's job, so the
	// total Reset count is one per dispatch.
	assert.Equal(t, 2, tut.resetCount)
	assert.Equal(t, 1, tut.stopCount,
		"clearActive forwards Stop through Handler.Close")
}

func TestTutorialRunnerUnknownTutorial(t *testing.T) {
	t.Parallel()
	r, _ := newTestRunner(nil)
	err := r.HandleCommand(context.Background(),
		textapi.Command{Name: "tutorial", Args: []string{"start", "nope"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown tutorial "nope"`)
}

func TestTutorialRunnerStop(t *testing.T) {
	t.Parallel()
	r, _ := newTestRunner(map[string]idetutorial.Tutorial{
		"basics": &tutStub{exitOn: 'q'},
	})

	err := r.HandleCommand(context.Background(),
		textapi.Command{Name: "tutorial", Args: []string{"stop"}})
	require.Error(t, err)

	require.NoError(t, r.HandleCommand(context.Background(),
		textapi.Command{Name: "tutorial", Args: []string{"start", "basics"}}))
	require.NotNil(t, r.overlay)
	require.NoError(t, r.HandleCommand(context.Background(),
		textapi.Command{Name: "tutorial", Args: []string{"stop"}}))
	assert.Nil(t, r.overlay)
}

// TestTutorialRunnerRunningIsRaceFree pins that running() may be read
// off the event loop: the package-install gate consults it from the
// background syntax and LSP goroutines while the event loop starts and
// stops tutorials.
func TestTutorialRunnerRunningIsRaceFree(t *testing.T) {
	t.Parallel()
	r, _ := newTestRunner(map[string]idetutorial.Tutorial{"basics": &tutStub{}})

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
	r, _ := newTestRunner(map[string]idetutorial.Tutorial{
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

	// First arg: subcommands.
	it, _, err := r.Complete(ctx, textapi.Command{Name: "tutorial"})
	require.NoError(t, err)
	assert.Equal(t,
		[]string{"start", "stop"},
		drain(it))

	// `tutorial start <TAB>`: tutorial names.
	it, _, err = r.Complete(ctx,
		textapi.Command{Name: "tutorial", Args: []string{"start", ""}})
	require.NoError(t, err)
	assert.Equal(t, []string{"advanced", "basics"}, drain(it))

	// `tutorial stop <TAB>`: no completions.
	it, _, err = r.Complete(ctx,
		textapi.Command{Name: "tutorial", Args: []string{"stop", ""}})
	require.NoError(t, err)
	assert.Empty(t, drain(it))

	// Too many args: no completions.
	it, _, err = r.Complete(ctx,
		textapi.Command{Name: "tutorial", Args: []string{"start", "basics", "extra"}})
	require.NoError(t, err)
	assert.Empty(t, drain(it))
}

func TestTutorialRunnerRegister(t *testing.T) {
	t.Parallel()
	r, _ := newTestRunner(nil)
	assert.False(t, r.has("intro"))

	tut := &tutStub{exitOn: 'q'}
	assert.True(t, r.register("intro", tut), "first register should add")
	assert.True(t, r.has("intro"))

	assert.False(t, r.register("intro", &tutStub{}),
		"register must not overwrite an existing tutorial")

	require.NoError(t, r.HandleCommand(context.Background(),
		textapi.Command{Name: "tutorial", Args: []string{"start", "intro"}}))
	assert.NotNil(t, r.overlay, "registered tutorial should be startable")
	assert.Equal(t, "intro", r.activeName)
}

// TestTutorialRunnerBasicsFlowEndToEnd asserts that the runner drives
// a real starlark tutorial through the full basics flow and clears the
// overlay once the final step exits.
//
// Regression: wait_command(edit) previously failed to advance after
// wait_command(wopen) succeeded.
func TestTutorialRunnerBasicsFlowEndToEnd(t *testing.T) {
	t.Parallel()
	src := `
def run():
    floating_window(title="welcome", text="open something")
    ws = wait_command(command="wopen")
    notify(level=info, message="opened " + ws.args[0])
    floating_window(title="edit", text="now edit a file")
    ed = wait_command(command="edit")
    notify(level=info, message="edited " + ed.args[0])
tutorial(entry=run)
`
	tut, err := starlarktutorial.New(
		"basics", src,
		nil, nil, nil, nil,
		term.Attributes{},
		nil, nil,
		term.KeyComb{Ch: ':'},
		"standard", "", nil,
		nil,
		nil,
		nil,
	)
	require.NoError(t, err)

	r, root := newTestRunner(
		map[string]idetutorial.Tutorial{"basics": tut})

	require.NoError(t, r.HandleCommand(context.Background(),
		textapi.Command{Name: "tutorial", Args: []string{"start", "basics"}}))
	require.NotNil(t, r.overlay)

	enter := term.Event{Type: term.EventKey, Key: term.KeyEnter}
	feed := func(t *testing.T, evs ...term.Event) {
		t.Helper()
		for _, ev := range evs {
			r.Handle(ev)
		}
	}

	require.True(t, tut.WaitActive("floating_window", time.Second),
		"first floating_window did not become active")
	feed(t, enter)
	require.True(t, tut.WaitActive("wait_command", time.Second),
		"wait_command(wopen) did not become active")
	// The IDE's command observer (not the tutorial's Handle) is the
	// source of truth for command dispatches: it carries the real
	// args after alias expansion. Drive the observer the way ex.go
	// would after the user dispatched `:wopen ~/proj`.
	r.observeCommand("wopen", "wopen", []string{"~/proj"}, nil)
	require.True(t, tut.WaitActive("floating_window", time.Second),
		"second floating_window did not become active")
	feed(t, enter)
	require.True(t, tut.WaitActive("wait_command", time.Second),
		"wait_command(edit) did not become active")
	r.observeCommand("edit", "edit", []string{"somefile.go"}, nil)
	require.True(t, tut.WaitFinished(time.Second),
		"tutorial did not finish after edit dispatch")

	// The script's finish publishes an interrupt in production; the
	// overlay is cleared when the event loop delivers that wakeup,
	// not by the observer call itself.
	_, _ = r.Handle(term.Event{Type: term.EventInterrupt})
	assert.Nil(t, r.overlay,
		"runner overlay should be cleared once basics flow completes")
	_ = root
}

// TestTutorialRunnerWaitCommandArgsComeFromObserver is a regression
// test for an empty-tuple panic users hit when `ws.args[0]` was
// evaluated after `wait_command(command="wopen")`. The tutorial's
// Handle used to greedily resolve a wait_command after seeing
// `<cmd-key>wopen<enter>` in its own buffer; that resolve carried no
// args because Handle only saw keystrokes. The fix is to rely on the
// host's command observer — which fires with the post-expansion args
// — as the only source of resolution.
func TestTutorialRunnerWaitCommandArgsComeFromObserver(t *testing.T) {
	t.Parallel()
	src := `
def run():
    ws = wait_command(command="wopen")
    notify(level=info, message="opened " + ws.args[0])
tutorial(entry=run)
`
	tut, err := starlarktutorial.New(
		"argpanic", src,
		nil, nil, nil, nil,
		term.Attributes{},
		nil, nil,
		term.KeyComb{Ch: ':'},
		"standard", "", nil,
		nil,
		nil,
		nil,
	)
	require.NoError(t, err)

	r, _ := newTestRunner(
		map[string]idetutorial.Tutorial{"argpanic": tut})
	require.NoError(t, r.HandleCommand(context.Background(),
		textapi.Command{Name: "tutorial", Args: []string{"start", "argpanic"}}))
	require.NotNil(t, r.overlay)

	require.True(t, tut.WaitActive("wait_command", time.Second),
		"wait_command(wopen) did not become active")

	// Drive the same keystrokes ex.go would forward to the tutorial
	// during a `:wopen ~/proj<enter>` dispatch. The tutorial must
	// NOT resolve from these — args would be empty and the entry
	// would panic on ws.args[0].
	colon := term.Event{Type: term.EventKey, Ch: ':'}
	r.Handle(colon)
	for _, ch := range "wopen ~/proj" {
		r.Handle(term.Event{Type: term.EventKey, Ch: ch})
	}
	r.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})

	// The host's observer fires next — with the real args after
	// alias expansion. This is the only place wait_command resolves.
	r.observeCommand("wopen", "wopen", []string{"~/proj"}, nil)

	require.True(t, tut.WaitFinished(time.Second),
		"tutorial did not finish after observer fired with args")
	// The script's finish publishes an interrupt in production; the
	// overlay is cleared when the event loop delivers that wakeup,
	// not by the observer call itself.
	_, _ = r.Handle(term.Event{Type: term.EventInterrupt})
	assert.Nil(t, r.overlay)
}

// TestTutorialRunnerQuitOnFloatingWindowStep is the regression test
// for the user report "Cmd+Q does nothing during the tutorial": a
// floating_window step swallows every stray key, so the quit chord
// never reached the IDE root and the confirm-exit prompt never
// appeared.
func TestTutorialRunnerQuitOnFloatingWindowStep(t *testing.T) {
	src := `
def run():
    floating_window(title="welcome", text="press enter")
    floating_window(title="done", text="press enter")
tutorial(entry=run)
`
	tut, err := starlarktutorial.New(
		"basics", src,
		nil, nil, nil, nil,
		term.Attributes{},
		nil, nil,
		term.KeyComb{Ch: ':'},
		"standard", "", nil,
		nil,
		nil,
		nil,
	)
	require.NoError(t, err)

	cc := defaultCfg()
	cc.cfg["command"].(map[string]any)["key_bindings"].(map[string]any)["<m-q>"] = "quit"
	m := newTestWorkspaceManagerHandler(t, cc, nil, nopShutdownShaderConfig())
	t.Cleanup(func() { require.NoError(t, m.Close()) })
	require.NoError(t, m.openFile("notes.txt", true))

	root := m.workspaceManagerHandler
	r := &tutorialRunner{}
	r.init(root, map[string]idetutorial.Tutorial{"basics": tut}, nil,
		term.NopInterrupter(), nil, root.exitRequested)
	r.Resize(40, 12)

	require.NoError(t, r.HandleCommand(context.Background(),
		textapi.Command{Name: "tutorial", Args: []string{"start", "basics"}}))
	require.True(t, tut.WaitActive("floating_window", time.Second),
		"floating_window step did not become active")
	require.NotNil(t, r.overlay)

	handle := func(ev term.Event) (bool, bool) {
		m.mu.Lock()
		defer m.mu.Unlock()
		return r.Handle(ev)
	}

	exit, handled := handle(term.Event{
		Type: term.EventKey, Ch: 'q', Mod: term.ModMeta})
	assert.False(t, exit, "quit opens the confirm prompt first")
	assert.True(t, handled)
	require.Nil(t, r.overlay,
		"the tutorial must be torn down so the confirm prompt is visible")

	exit, handled = handle(term.Event{Type: term.EventKey, Ch: 'y'})
	assert.True(t, exit, "answering Yes must exit the IDE")
	assert.True(t, handled)
}

// TestTutorialRunnerObserveCommandAdvancesWaitCommand asserts that an
// observed alias dispatch reaches the overlay's ObserveCommand and
// advances the active wait_command step.
func TestTutorialRunnerObserveCommandAdvancesWaitCommand(t *testing.T) {
	t.Parallel()
	src := `
def run():
    wait_command(command="edit")
    notify(level=info, message="advanced via observer")
tutorial(entry=run)
`
	tut, err := starlarktutorial.New(
		"observe", src,
		nil, nil, nil, nil,
		term.Attributes{},
		nil, nil,
		term.KeyComb{Ch: ':'},
		"standard", "", nil,
		nil,
		nil,
		nil,
	)
	require.NoError(t, err)

	r, _ := newTestRunner(
		map[string]idetutorial.Tutorial{"observe": tut})
	require.NoError(t, r.HandleCommand(context.Background(),
		textapi.Command{Name: "tutorial", Args: []string{"start", "observe"}}))
	require.NotNil(t, r.overlay)

	r.observeCommand("e", "edit", []string{"somefile.go"}, nil)

	// notify is non-blocking, so the entry function returns and the
	// runtime is finished. The next Handle observes finished=true and
	// clears the overlay.
	deadline := time.After(time.Second)
	for r.overlay != nil {
		_, _ = r.Handle(term.Event{Type: term.EventKey, Ch: 'x'})
		select {
		case <-deadline:
			t.Fatal("runner did not clear overlay within 1s")
		default:
		}
	}
	assert.Nil(t, r.overlay,
		"runner should clear overlay once tutorial exits")
}

// TestTutorialRunnerObserveCommandKeepsArmedOnError asserts that a
// failed dispatch forwarded through observeCommand keeps the overlay
// mounted and does not advance the wait_command step.
func TestTutorialRunnerObserveCommandKeepsArmedOnError(t *testing.T) {
	t.Parallel()
	src := `
def run():
    wait_command(command="wopen", on_error="wopen <directory>")
    notify(level=info, message="should not fire")
tutorial(entry=run)
`
	tut, err := starlarktutorial.New(
		"on_error", src,
		nil, nil, nil, nil,
		term.Attributes{},
		nil, nil,
		term.KeyComb{Ch: ':'},
		"standard", "", nil,
		nil,
		nil,
		nil,
	)
	require.NoError(t, err)

	r, _ := newTestRunner(
		map[string]idetutorial.Tutorial{"on_error": tut})
	require.NoError(t, r.HandleCommand(context.Background(),
		textapi.Command{Name: "tutorial", Args: []string{"start", "on_error"}}))
	require.NotNil(t, r.overlay)

	r.observeCommand("wopen", "wopen", nil,
		errors.New("missing directory argument"))
	assert.NotNil(t, r.overlay,
		"failed dispatch must keep the overlay mounted")

	r.observeCommand("wopen", "wopen", []string{"~/proj"}, nil)
	deadline := time.After(time.Second)
	for r.overlay != nil {
		_, _ = r.Handle(term.Event{Type: term.EventKey, Ch: 'x'})
		select {
		case <-deadline:
			t.Fatal("runner did not drain overlay within 1s")
		default:
		}
	}
	assert.Nil(t, r.overlay,
		"successful dispatch + notify drains the overlay")
}

// TestTutorialRunnerObserveEventAdvancesWaitEvent asserts that an
// observed editor event reaches the overlay's ObserveEvent and
// advances the active wait_event step, then the runner clears the
// overlay once the tutorial finishes. A non-matching event observed
// first must keep the overlay armed.
func TestTutorialRunnerObserveEventAdvancesWaitEvent(t *testing.T) {
	t.Parallel()
	src := `
def run():
    wait_event(event="open")
    notify(level=info, message="advanced via event observer")
tutorial(entry=run)
`
	tut, err := starlarktutorial.New(
		"observe-event", src,
		nil, nil, nil, nil,
		term.Attributes{},
		nil, nil,
		term.KeyComb{Ch: ':'},
		"standard", "", nil,
		nil,
		nil,
		nil,
	)
	require.NoError(t, err)

	r, _ := newTestRunner(
		map[string]idetutorial.Tutorial{"observe-event": tut})
	require.NoError(t, r.HandleCommand(context.Background(),
		textapi.Command{Name: "tutorial", Args: []string{"start", "observe-event"}}))
	require.NotNil(t, r.overlay)

	r.observeEvent("close", "file:///x.go")
	assert.NotNil(t, r.overlay,
		"a non-matching event must keep the overlay mounted")

	r.observeEvent("open", "file:///x.go")
	deadline := time.After(time.Second)
	for r.overlay != nil {
		_, _ = r.Handle(term.Event{Type: term.EventKey, Ch: 'x'})
		select {
		case <-deadline:
			t.Fatal("runner did not clear overlay within 1s")
		default:
		}
	}
	assert.Nil(t, r.overlay,
		"runner should clear overlay once the wait_event step resolves")
}

// TestTutorialRunnerObserveEventNoOverlay asserts that observeEvent is
// a no-op when no tutorial overlay is mounted.
func TestTutorialRunnerObserveEventNoOverlay(t *testing.T) {
	t.Parallel()
	r, _ := newTestRunner(map[string]idetutorial.Tutorial{})
	require.Nil(t, r.overlay)
	r.observeEvent("open", "file:///x.go")
	assert.Nil(t, r.overlay)
}

// TestCommandObserverRegistryLateSubscribe asserts that a subscriber
// registered after the registry was already handed to ex still
// receives subsequent dispatches.
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

// TestTutorialRunnerWrongKeyOnFloatingWindowSwallowedAndPulses asserts
// that a stray key on an active floating_window step is swallowed by
// the tutorial (never reaching the IDE root) and flips Shader() from
// false to true through the full runner -> handler -> tutorial path.
func TestTutorialRunnerWrongKeyOnFloatingWindowSwallowedAndPulses(t *testing.T) {
	t.Parallel()
	src := `
def run():
    floating_window(title="welcome", text="press enter or esc")
    notify(level=info, message="done")
tutorial(entry=run)
`
	overlay := idetutorial.NewOverlayBrowser(
		browser.NewComponent(idetutorial.DefaultOverlayBrowserConfig()))
	tut, err := starlarktutorial.New(
		"wrongkey", src,
		overlay, nil, nil, nil,
		term.Attributes{},
		nil, nil,
		term.KeyComb{Ch: ':'},
		"standard", "", nil,
		nil,
		nil,
		nil,
	)
	require.NoError(t, err)

	r, root := newTestRunner(
		map[string]idetutorial.Tutorial{"wrongkey": tut})
	r.browserOverlay = overlay
	r.Resize(80, 24)

	require.NoError(t, r.HandleCommand(context.Background(),
		textapi.Command{Name: "tutorial", Args: []string{"start", "wrongkey"}}))
	require.NotNil(t, r.overlay)

	_, want := tut.Shader()
	require.False(t, want,
		"fresh floating_window must not declare a shader")

	_, handled := r.Handle(term.Event{Type: term.EventKey, Ch: ':'})
	assert.True(t, handled,
		"stray ':' on floating_window must be swallowed by the "+
			"tutorial — the IDE root must never see it")
	assert.Equal(t, 0, root.handled,
		"root must not have observed the stray ':'")

	_, want = tut.Shader()
	assert.True(t, want,
		"wrong key on floating_window must stage a hint pulse")
}

// TestTutorialRunnerPromptSurvivesRootRewire asserts that a
// confirm/choice overlay rendered by the tutorial does not depend on
// the runner's root handler. After the runner's root handler is
// swapped (simulating a workspace switch), the tutorial overlay
// continues to render the prompt's message and options, and Enter
// still resolves the prompt.
//
// Regression for RUNE-188: the old design installed the prompt as a
// browser.Window via idetutorial.Prompter; that handle was attached
// to whichever workspace was focused at the time and was lost across
// workspace switches. The new design renders the prompt directly on
// the tutorial layer so the runner's root is irrelevant.
func TestTutorialRunnerPromptSurvivesRootRewire(t *testing.T) {
	t.Parallel()
	src := `
def run():
    yes = confirm("continue?")
    if yes:
        notify(level=info, message="ok")
tutorial(entry=run)
`
	overlay := idetutorial.NewOverlayBrowser(
		browser.NewComponent(idetutorial.DefaultOverlayBrowserConfig()))
	tut, err := starlarktutorial.New(
		"survives", src,
		overlay, nil, nil, nil,
		term.Attributes{},
		nil, nil,
		term.KeyComb{Ch: ':'},
		"standard", "", nil,
		nil,
		nil,
		nil,
	)
	require.NoError(t, err)

	r, _ := newTestRunner(
		map[string]idetutorial.Tutorial{"survives": tut})
	r.browserOverlay = overlay
	r.Resize(80, 24)
	require.NoError(t, r.HandleCommand(context.Background(),
		textapi.Command{Name: "tutorial", Args: []string{"start", "survives"}}))
	require.NotNil(t, r.overlay)

	require.True(t, tut.WaitActive("confirm", time.Second),
		"confirm did not become active")

	// Simulate a workspace switch by swapping the runner's root.
	// The tutorial overlay captured the original root at setActive
	// time but renders its own prompt; neither the captured root
	// nor the runner's current root contribute to the prompt's
	// pixels.
	r.Handler = &rootStub{}
	r.Resize(80, 24)

	g := newGridWriter80x24()
	// Draw the overlay directly: that's what the runner ends up
	// calling. The overlay's tut.Draw paints the prompt regardless
	// of which root is underneath, which is the contract we are
	// asserting.
	r.overlay.Draw(g)
	assert.True(t, gridStringContains(g, "continue?"),
		"prompt message must be drawn on the overlay layer, not the root")
	assert.True(t, gridStringContains(g, "Yes"),
		"prompt option must be drawn on the overlay layer, not the root")

	_, _ = r.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	require.True(t, tut.WaitFinished(time.Second),
		"tutorial must finish after Enter resolves the confirm")
}

// TestTutorialRunnerWaitKeyHintRoutesMouseKeysFallThrough asserts the
// non-modal contract of wait_* hint windows through the full runner
// -> handler -> overlay path: keys fall through to the IDE root while
// the hint window is shown, mouse events over the hint route to the
// browser (so the user can drag it aside) without reaching the root,
// and mouse events elsewhere still reach the root.
func TestTutorialRunnerWaitKeyHintRoutesMouseKeysFallThrough(t *testing.T) {
	t.Parallel()
	src := `
def run():
    wait_key(key="<f6>")
tutorial(entry=run)
`
	overlay := idetutorial.NewOverlayBrowser(
		browser.NewComponent(idetutorial.DefaultOverlayBrowserConfig()))
	tut, err := starlarktutorial.New(
		"waitkey", src,
		overlay, nil, nil, nil,
		term.Attributes{},
		nil, nil,
		term.KeyComb{Ch: ':'},
		"standard", "", nil,
		nil,
		nil,
		nil,
	)
	require.NoError(t, err)

	r, root := newTestRunner(
		map[string]idetutorial.Tutorial{"waitkey": tut})
	r.browserOverlay = overlay
	r.Resize(80, 24)
	require.NoError(t, r.HandleCommand(context.Background(),
		textapi.Command{Name: "tutorial", Args: []string{"start", "waitkey"}}))
	require.True(t, tut.WaitActive("wait_key", time.Second),
		"wait_key did not become active")
	require.Equal(t, 1, overlay.Windows(),
		"a wait_key step must show its hint window")

	_, handled := r.Handle(term.Event{Type: term.EventKey, Ch: 'x'})
	assert.True(t, handled)
	assert.Equal(t, 1, root.handled,
		"keys must fall through to the IDE root while the hint is shown")
	assert.Equal(t, 1, overlay.Windows(),
		"a fallen-through key must not close the hint window")

	inside := term.Coordinates{}
	found := false
	for y := 0; y < 24 && !found; y++ {
		for x := 0; x < 80 && !found; x++ {
			if overlay.Covers(term.Coordinates{X: x, Y: y}) {
				inside = term.Coordinates{X: x, Y: y}
				found = true
			}
		}
	}
	require.True(t, found, "the hint window must cover some screen cell")
	_, _ = r.Handle(term.Event{Type: term.EventMouse, Key: term.MouseLeft,
		MouseX: inside.X, MouseY: inside.Y})
	_, _ = r.Handle(term.Event{Type: term.EventMouse, Key: term.MouseRelease,
		MouseX: inside.X, MouseY: inside.Y})
	assert.Equal(t, 1, root.handled,
		"mouse events over the hint window must route to the browser, "+
			"never reaching the IDE root")

	outside := term.Coordinates{X: 0, Y: 23}
	require.False(t, overlay.Covers(outside))
	_, _ = r.Handle(term.Event{Type: term.EventMouse, Key: term.MouseLeft,
		MouseX: outside.X, MouseY: outside.Y})
	assert.Equal(t, 2, root.handled,
		"mouse events outside the hint window must reach the IDE root")

	_, _ = r.Handle(term.Event{Type: term.EventKey, Key: term.KeyF6})
	require.True(t, tut.WaitFinished(time.Second),
		"the awaited key must resolve the wait_key step")
	assert.Zero(t, overlay.Windows(),
		"resolving the wait_key step must close its hint window")
}

// TestTutorialEventObserverDefersToEventLoop is a regression test for a
// nil-pointer crash in (*Handler).Reset reached from setActive: text/LSP
// events are delivered on background subscriber goroutines, so calling
// observeEvent inline let it race the event-loop setActive/clearActive
// on the runner's overlay. The subscriber must instead marshal observe
// onto the event loop via the host scheduler.
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

// gridWriter80x24 is a minimal recording term.Writer used by
// TestTutorialRunnerPromptSurvivesRootRewire so we don't have to
// import the starlarktutorial test helpers.
type gridWriter80x24 struct {
	w, h  int
	cells [][]rune
}

func newGridWriter80x24() *gridWriter80x24 {
	const w, h = 80, 24
	cells := make([][]rune, h)
	for i := range cells {
		cells[i] = make([]rune, w)
	}
	return &gridWriter80x24{w: w, h: h, cells: cells}
}

func (g *gridWriter80x24) SetCell(pos term.Coordinates, c term.Cell) {
	if pos.X < 0 || pos.Y < 0 || pos.X >= g.w || pos.Y >= g.h {
		return
	}
	g.cells[pos.Y][pos.X] = c.Ch
}

func (g *gridWriter80x24) Context() context.Context                              { return context.Background() }
func (g *gridWriter80x24) UnionAttributes(_ term.Coordinates, _ term.Attributes) {}

func gridStringContains(g *gridWriter80x24, needle string) bool {
	for y := range g.h {
		row := string(g.cells[y])
		if strings.Contains(row, needle) {
			return true
		}
	}
	return false
}

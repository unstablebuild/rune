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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSkipResolvesEveryScreenKind pins the value a skipped step hands
// back to the script for each blocking builtin. wait_command and
// wait_shell report the action they asked for; every other kind
// resolves with its own dismissal.
func TestSkipResolvesEveryScreenKind(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "wait_command reports the command it asked for",
			src: `
def run():
    r = wait_command(command="windownew right")
    notify(level=info, message="ran " + r.name + " " + " ".join(r.args))
`,
			want: "ran windownew right",
		},
		{
			name: "wait_shell reports its argv",
			src: `
def run():
    r = wait_shell(args=["pkg", "install", "x"])
    notify(level=info, message="ran " + " ".join(r.args))
`,
			want: "ran pkg install x",
		},
		{
			name: "wait_event",
			src: `
def run():
    wait_event(event="open")
    notify(level=info, message="after")
`,
			want: "after",
		},
		{
			name: "confirm resolves as declined",
			src: `
def run():
    if confirm(message="sure?"):
        notify(level=info, message="yes")
    else:
        notify(level=info, message="no")
`,
			want: "no",
		},
		{
			name: "choice resolves as dismissed",
			src: `
def run():
    c = choice(message="pick", options=["a", "b"])
    notify(level=info, message="idx " + str(c.index))
`,
			want: "idx -1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tut, notis := newTutorial(t, tt.src+"\ntutorial(entry=run)\n")
			resetAndWait(t, tut, time.Second)
			tut.Skip()
			require.True(t, tut.WaitFinished(time.Second))
			assert.True(t, notis.containsSubstring(tt.want),
				"want %q in %q", tt.want, notis.renderedCalls())
		})
	}
}

// TestSkipOnAnIdleTutorialIsANoOp asserts Skip is safe to call when
// no step is armed: the tile's Skip button is always clickable.
func TestSkipOnAnIdleTutorialIsANoOp(t *testing.T) {
	t.Parallel()
	tut, _ := newTutorial(t, `
def run():
    wait_event(event="open")
tutorial(entry=run)
`)
	assert.NotPanics(t, func() { tut.Skip() })
	resetAndWait(t, tut, time.Second)
	tut.Skip()
	require.True(t, tut.WaitFinished(time.Second))
	assert.NotPanics(t, func() { tut.Skip() })
}

// TestSkipWalksAWholeLesson asserts that skipping every step in turn
// drains a multi-step lesson to a normal completion.
func TestSkipWalksAWholeLesson(t *testing.T) {
	t.Parallel()
	tut, notis := newTutorial(t, `
def run():
    wait_command(command="edit file.go")
    wait_shell(args=["pkg", "install", "x"])
    wait_event(event="open")
    confirm(message="sure?")
    notify(level=success, message="done")
tutorial(entry=run)
`)
	resetAndWait(t, tut, time.Second)
	for _, kind := range []string{
		"wait_command", "wait_shell", "wait_event", "confirm",
	} {
		require.True(t, tut.WaitActive(kind, time.Second),
			"lesson never armed %s", kind)
		tut.Skip()
	}
	require.True(t, tut.WaitFinished(time.Second))
	assert.True(t, tut.Completed())
	assert.True(t, notis.containsSubstring("done"),
		"lesson never reached its final notify: %q", notis.renderedCalls())
}

// TestSkipWarnsWhenItMayStrandTheLesson asserts that skipping a
// wait_command whose spec names no arguments tells the user the rest
// of the lesson may misbehave, because the live path would have
// resolved with whatever the user typed.
func TestSkipWarnsWhenItMayStrandTheLesson(t *testing.T) {
	t.Parallel()
	tut, notis := newTutorial(t, `
def run():
    r = wait_command(command="edit")
    notify(level=info, message="opened " + r.args[0])
tutorial(entry=run)
`)
	resetAndWait(t, tut, time.Second)
	tut.Skip()
	require.True(t, tut.WaitFinished(time.Second))
	assert.True(t, notis.containsSubstring("skipped step"),
		"want a stranded-skip notification, got %q", notis.renderedCalls())
}

// TestSkipDoesNotWarnWhenTheSpecNamesItsArguments asserts the
// stranded-skip notification is reserved for the one case that can
// actually strand a lesson.
func TestSkipDoesNotWarnWhenTheSpecNamesItsArguments(t *testing.T) {
	t.Parallel()
	tut, notis := newTutorial(t, `
def run():
    r = wait_command(command="edit file.go")
    notify(level=success, message="opened " + r.args[0])
tutorial(entry=run)
`)
	resetAndWait(t, tut, time.Second)
	tut.Skip()
	require.True(t, tut.WaitFinished(time.Second))
	assert.True(t, notis.containsSubstring("opened file.go"),
		"want the lesson to run on, got %q", notis.renderedCalls())
	assert.False(t, notis.containsSubstring("skipped step"),
		"a fully specified wait_command must not warn: %q", notis.renderedCalls())
}

// TestSkipReportsTheRunEnding asserts Skip tells its caller when the
// skipped step was the last one. Without it the host cannot tear the
// tile down until some unrelated event arrives.
func TestSkipReportsTheRunEnding(t *testing.T) {
	t.Parallel()
	tut, _ := newTutorial(t, `
def run():
    wait_event(event="open", title="one")
    wait_event(event="flush", title="two")
tutorial(entry=run)
`)
	resetAndWait(t, tut, time.Second)
	assert.False(t, tut.Skip(), "a middle step leaves the lesson running")
	require.True(t, tut.WaitActive("wait_event", time.Second))
	assert.True(t, tut.Skip(), "skipping the last step ends the lesson")
	assert.True(t, tut.WaitFinished(time.Second))
}

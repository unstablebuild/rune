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

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// agentOnboardingSrc mirrors the install → provider → credentials
// shape of teach_agent() in cmd/rune/tutorials/agent.star so the DSL
// mechanics the real tutorial relies on (the wait_shell builtin's
// open-shell gate, its token-containment re-arm, and choice branching)
// are exercised here without linking the cmd/rune native libraries.
const agentOnboardingSrc = `
def run():
    wait_command(command="console", text="open the console")
    wait_shell(args=["pkg", "install", "rune-agent"],
               text="run pkg install rune-agent")
    notify(level=success, message="Rune Agent installed.")

    pick = choice(message="provider?",
                  options=["OpenAI", "Anthropic", "Gemini", "Codex", "Claude", "Skip"])
    if not pick.selected or pick.value == "Skip":
        notify(level=info, message="skipped provider setup")
        return

    wait_shell(args=["models", "providers", "openai", "add"],
               text="connect " + pick.value)
    notify(level=success, message="OpenAI connected.")
    wait_command(command="agent", text="open the agent")
    notify(level=success, message="Rune Agent is ready.")

tutorial(entry=run)
`

// TestAgentOnboardingInstallGateReArmsOnWrongCommand drives the agent
// onboarding flow: the install gate must ignore an unrelated shell
// submission, advance on the correct "pkg install rune-agent"
// submission, branch on the provider choice, and complete the
// credentials gate.
func TestAgentOnboardingInstallGateReArmsOnWrongCommand(t *testing.T) {
	t.Parallel()
	tut, notis := newTutorial(t, agentOnboardingSrc)
	resetAndWait(t, tut, time.Second)

	require.Equal(t, "wait_command", activeKindFor(tut))

	// Open the companion shell, then advance to the install step.
	tut.ObserveCommand("console", "console", nil, nil)
	waitNextActive(t, tut, "wait_shell", time.Second)

	// A shell submission that is not the install command keeps the
	// gate armed without resolving it.
	exit := tut.ObserveCommand("console", "console", []string{"ls"}, nil)
	assert.False(t, exit)
	assert.Equal(t, "wait_shell", activeKindFor(tut),
		"a non-install shell command must keep the install gate armed")

	// The correct install command advances to the provider choice.
	tut.ObserveCommand("console", "console",
		[]string{"pkg", "install", "rune-agent"}, nil)
	waitNextActive(t, tut, "choice", time.Second)
	assert.True(t, notis.containsSubstring("Rune Agent installed."))

	// Pick OpenAI (index 0, already highlighted).
	_, _ = tut.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	waitNextActive(t, tut, "wait_shell", time.Second)

	tut.ObserveCommand("console", "console",
		[]string{"models", "providers", "openai", "add", "default"}, nil)
	waitNextActive(t, tut, "wait_command", time.Second)
	assert.True(t, notis.containsSubstring("OpenAI connected."))

	// The final step waits for the user to open the agent.
	tut.ObserveCommand("agent", "agent", nil, nil)
	waitFinished(t, tut, time.Second)
	assert.True(t, notis.containsSubstring("Rune Agent is ready."))
}

// TestAgentOnboardingSkipProviderExits asserts that choosing Skip at
// the provider prompt short-circuits credential setup with an info
// notification.
func TestAgentOnboardingSkipProviderExits(t *testing.T) {
	t.Parallel()
	tut, notis := newTutorial(t, agentOnboardingSrc)
	resetAndWait(t, tut, time.Second)

	tut.ObserveCommand("console", "console", nil, nil)
	waitNextActive(t, tut, "wait_shell", time.Second)
	tut.ObserveCommand("console", "console",
		[]string{"pkg", "install", "rune-agent"}, nil)
	waitNextActive(t, tut, "choice", time.Second)

	// Skip is the last option (index 5).
	for range 5 {
		_, _ = tut.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowRight})
	}
	_, _ = tut.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	waitFinished(t, tut, time.Second)
	assert.True(t, notis.containsSubstring("skipped provider setup"),
		"Skip must short-circuit credential setup, got %v",
		notis.renderedCalls())
}

// TestAgentOnboardingDismissedProviderExits asserts that dismissing the
// provider prompt (Esc, selected=False) also short-circuits setup.
func TestAgentOnboardingDismissedProviderExits(t *testing.T) {
	t.Parallel()
	tut, notis := newTutorial(t, agentOnboardingSrc)
	resetAndWait(t, tut, time.Second)

	tut.ObserveCommand("console", "console", nil, nil)
	waitNextActive(t, tut, "wait_shell", time.Second)
	tut.ObserveCommand("console", "console",
		[]string{"pkg", "install", "rune-agent"}, nil)
	waitNextActive(t, tut, "choice", time.Second)

	_, _ = tut.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
	waitFinished(t, tut, time.Second)
	assert.True(t, notis.containsSubstring("skipped provider setup"),
		"dismissing the provider prompt must short-circuit setup, got %v",
		notis.renderedCalls())
}

// TestWaitShellRequiresArgs asserts that wait_shell is exclusively for
// commands run inside the console: calling it without args is a runtime
// error (opening the console is wait_command(command="console")).
func TestWaitShellRequiresArgs(t *testing.T) {
	t.Parallel()
	src := `
def run():
    wait_shell()
    notify(message="should not reach here")
tutorial(entry=run)
`
	tut, notis := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)
	waitFinished(t, tut, time.Second)
	hasError := false
	for _, c := range notis.captured {
		if c.level == browserapi.LevelError {
			hasError = true
		}
	}
	assert.True(t, hasError,
		"wait_shell without args must surface as a runtime error, got %v",
		notis.renderedCalls())
	assert.False(t, notis.containsSubstring("should not reach here"))
}

// TestWaitShellIgnoresNonShellObservations asserts that a wait_shell
// step does not resolve on a regular (non-shell) command dispatch.
func TestWaitShellIgnoresNonShellObservations(t *testing.T) {
	t.Parallel()
	src := `
def run():
    wait_shell(args=["pkg", "install", "rune-agent"])
    notify(message="installed")
tutorial(entry=run)
`
	tut, notis := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)

	exit := tut.ObserveCommand("edit", "edit", []string{"main.go"}, nil)
	assert.False(t, exit)
	assert.Equal(t, "wait_shell", activeKindFor(tut),
		"a non-shell command must not resolve a wait_shell step")
	assert.Equal(t, 0, notis.len())

	tut.ObserveCommand("console", "console",
		[]string{"pkg", "install", "rune-agent"}, nil)
	waitFinished(t, tut, time.Second)
	assert.True(t, notis.containsSubstring("installed"))
}

// TestWaitShellTokenContainmentReArms asserts that a shell submission
// missing an expected token keeps the step armed, and that a later
// submission containing every token (plus extras) resolves it. The
// resolved command_result carries the full observed args.
func TestWaitShellTokenContainmentReArms(t *testing.T) {
	t.Parallel()
	src := `
def run():
    r = wait_shell(args=["pkg", "install", "rune-agent"])
    notify(message="installed " + r.args[2])
tutorial(entry=run)
`
	tut, notis := newTutorial(t, src)
	resetAndWait(t, tut, time.Second)

	// Missing "rune-agent" keeps the step armed.
	exit := tut.ObserveCommand("console", "console", []string{"pkg", "install"}, nil)
	assert.False(t, exit)
	assert.Equal(t, "wait_shell", activeKindFor(tut))

	// Containment with extra tokens (e.g. a flag) still matches.
	tut.ObserveCommand("console", "console",
		[]string{"pkg", "install", "rune-agent", "--force"}, nil)
	waitFinished(t, tut, time.Second)
	assert.True(t, notis.containsSubstring("installed rune-agent"),
		"command_result.args must carry the full observed args, got %v",
		notis.renderedCalls())
}

// TestWaitShellErrorStaysArmedWithUnchangedCopy asserts that a
// dispatch error keeps the step armed and leaves the step's copy
// exactly as the user was reading it: a screen that rewrites itself
// mid-step moves the instructions out from under them.
func TestWaitShellErrorStaysArmedWithUnchangedCopy(t *testing.T) {
	t.Parallel()
	src := `
def run():
    wait_shell(args=["pkg", "install", "rune-agent"],
               text="Type it and press Enter.")
    notify(message="installed")
tutorial(entry=run)
`
	tut, _ := newTutorial(t, src)
	// A body wide enough for the step's instruction to render on a
	// single row, so the assertion reads the copy, not the wrapping.
	const bodyWidth, bodyHeight = 40, 24
	tut.Resize(bodyWidth, bodyHeight)
	resetAndWait(t, tut, time.Second)

	draw := func() string {
		g := newAttrGridWriter(bodyWidth, bodyHeight)
		tut.Draw(g)
		return gridText(g)
	}
	before := draw()
	assert.Contains(t, before, "Type it and press Enter.",
		"the step's own instruction must render before any dispatch")

	exit := tut.ObserveCommand("console", "console",
		[]string{"pkg", "install", "rune-agent"}, assert.AnError)
	assert.False(t, exit)
	assert.Equal(t, "wait_shell", activeKindFor(tut),
		"a dispatch error must keep the wait_shell step armed")
	assert.Equal(t, before, draw(),
		"a dispatch error must leave the step's copy unchanged")
	tut.Stop()
}

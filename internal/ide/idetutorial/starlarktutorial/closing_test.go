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

// TestCompletedLessonAsksForStop asserts the last milestone does not
// take the lesson away: the tile stays up on a closing screen that
// tells the user to press Stop, so someone still practising the final
// step keeps the copy in front of them.
func TestCompletedLessonAsksForStop(t *testing.T) {
	t.Parallel()
	tut := newBackTutorial(t, backSrc)

	require.True(t, tut.WaitActive("wait_command", time.Second))
	tut.ObserveCommand("alpha", "alpha", nil, nil)
	require.True(t, tut.WaitActive("wait_command", time.Second))
	tut.ObserveCommand("bravo", "bravo", nil, nil)
	require.True(t, tut.WaitActive("wait_command", time.Second))
	tut.ObserveCommand("charlie", "charlie", nil, nil)
	require.True(t, tut.WaitFinished(time.Second))
	require.True(t, tut.Completed())

	assert.Contains(t, drawTutorial(t, tut), "Stop")
}

// TestCompletedLessonStillPagesBack asserts the reader can walk the
// whole lesson from the closing screen and wrap back to it.
func TestCompletedLessonStillPagesBack(t *testing.T) {
	t.Parallel()
	tut := newBackTutorial(t, backSrc)

	require.True(t, tut.WaitActive("wait_command", time.Second))
	tut.ObserveCommand("alpha", "alpha", nil, nil)
	require.True(t, tut.WaitActive("wait_command", time.Second))
	tut.ObserveCommand("bravo", "bravo", nil, nil)
	require.True(t, tut.WaitActive("wait_command", time.Second))
	tut.ObserveCommand("charlie", "charlie", nil, nil)
	require.True(t, tut.WaitFinished(time.Second))

	require.True(t, tut.Back())
	assert.Contains(t, drawTutorial(t, tut), "third step")
	require.True(t, tut.Back())
	assert.Contains(t, drawTutorial(t, tut), "second step")
	require.True(t, tut.Back())
	assert.Contains(t, drawTutorial(t, tut), "first step")
	require.True(t, tut.Back())
	assert.Contains(t, drawTutorial(t, tut), "Stop")
}

// TestFailedLessonHasNoClosingScreen asserts a run that ended badly
// leaves the tile empty: the host tears it down and the error is a
// notification, not a screen.
func TestFailedLessonHasNoClosingScreen(t *testing.T) {
	t.Parallel()
	const src = `
def run():
    wait_command(title = "One", command = "alpha", text = "first step")
    fail("boom")
tutorial(entry=run)
`
	tut := newBackTutorial(t, src)
	require.True(t, tut.WaitActive("wait_command", time.Second))
	tut.ObserveCommand("alpha", "alpha", nil, nil)
	require.True(t, tut.WaitFinished(time.Second))
	require.False(t, tut.Completed())

	assert.Empty(t, drawTutorial(t, tut))
	assert.False(t, tut.Back(), "a lesson that ended badly has nothing to page")
}

// TestResetClearsTheClosingScreen asserts a second run starts on its
// own first step rather than the previous run's closing screen.
func TestResetClearsTheClosingScreen(t *testing.T) {
	t.Parallel()
	tut := newBackTutorial(t, backSrc)

	require.True(t, tut.WaitActive("wait_command", time.Second))
	tut.ObserveCommand("alpha", "alpha", nil, nil)
	require.True(t, tut.WaitActive("wait_command", time.Second))
	tut.ObserveCommand("bravo", "bravo", nil, nil)
	require.True(t, tut.WaitActive("wait_command", time.Second))
	tut.ObserveCommand("charlie", "charlie", nil, nil)
	require.True(t, tut.WaitFinished(time.Second))

	tut.Reset()
	require.True(t, tut.WaitActive("wait_command", time.Second))
	assert.Contains(t, drawTutorial(t, tut), "first step")
}

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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unstablebuild/rune-go-sdk/term"

	"unstable.build/rune/internal/ide/idetutorial"
)

const backSrc = `
def run():
    wait_command(title = "One", command = "alpha", text = "first step")
    wait_command(title = "Two", command = "bravo", text = "second step")
    wait_command(title = "Three", command = "charlie", text = "third step")
tutorial(entry=run)
`

// TestBackPagesThroughCopyAlreadyRead asserts the Back button shows
// earlier steps without rewinding the lesson: the live step stays
// armed underneath and reaching its milestone brings the tile back to
// it.
func TestBackPagesThroughCopyAlreadyRead(t *testing.T) {
	t.Parallel()
	tut := newBackTutorial(t, backSrc)

	require.True(t, tut.WaitActive("wait_command", time.Second))
	assert.False(t, tut.Back(), "the first step has nothing behind it")

	tut.ObserveCommand("alpha", "alpha", nil, nil)
	require.True(t, tut.WaitActive("wait_command", time.Second))
	tut.ObserveCommand("bravo", "bravo", nil, nil)
	require.True(t, tut.WaitActive("wait_command", time.Second))
	assert.Contains(t, drawTutorial(t, tut), "third step")

	assert.True(t, tut.Back())
	assert.Contains(t, drawTutorial(t, tut), "second step")
	assert.True(t, tut.Back())
	assert.Contains(t, drawTutorial(t, tut), "first step")

	// The oldest screen wraps back to the live step, so the reader is
	// never stranded in the past with only one button.
	assert.True(t, tut.Back())
	assert.Contains(t, drawTutorial(t, tut), "third step")

	// The live step is still the one that advances the lesson.
	assert.True(t, tut.Back())
	assert.Contains(t, drawTutorial(t, tut), "second step")
	tut.ObserveCommand("charlie", "charlie", nil, nil)
	require.True(t, tut.WaitFinished(time.Second))
	assert.True(t, tut.Completed())
}

// TestForwardWalksBackTowardsTheLiveStep asserts the reader can come
// back the way they went: forward through the copy they paged past,
// landing on the live step rather than on a read-only copy of it.
func TestForwardWalksBackTowardsTheLiveStep(t *testing.T) {
	t.Parallel()
	tut := newBackTutorial(t, backSrc)

	require.True(t, tut.WaitActive("wait_command", time.Second))
	tut.ObserveCommand("alpha", "alpha", nil, nil)
	require.True(t, tut.WaitActive("wait_command", time.Second))
	tut.ObserveCommand("bravo", "bravo", nil, nil)
	require.True(t, tut.WaitActive("wait_command", time.Second))

	assert.False(t, tut.ViewingPast(), "the live step is not the past")
	assert.False(t, tut.Forward(), "there is nothing ahead of the live step")

	require.True(t, tut.Back())
	require.True(t, tut.Back())
	assert.Contains(t, drawTutorial(t, tut), "first step")
	assert.True(t, tut.ViewingPast())

	assert.True(t, tut.Forward())
	assert.Contains(t, drawTutorial(t, tut), "second step")
	assert.True(t, tut.ViewingPast())

	assert.True(t, tut.Forward())
	assert.Contains(t, drawTutorial(t, tut), "third step")
	assert.False(t, tut.ViewingPast(), "the newest screen is the live step")

	// Landing on the live step means landing on the armed one.
	tut.ObserveCommand("charlie", "charlie", nil, nil)
	require.True(t, tut.WaitFinished(time.Second))
	assert.True(t, tut.Completed())
}

// TestForwardReturnsToTheClosingScreen asserts a finished lesson pages
// forward the same way, with its closing screen as the newest one.
func TestForwardReturnsToTheClosingScreen(t *testing.T) {
	t.Parallel()
	tut := newBackTutorial(t, backSrc)

	for _, cmd := range []string{"alpha", "bravo", "charlie"} {
		require.True(t, tut.WaitActive("wait_command", time.Second))
		tut.ObserveCommand(cmd, cmd, nil, nil)
	}
	require.True(t, tut.WaitFinished(time.Second))
	assert.False(t, tut.ViewingPast())

	require.True(t, tut.Back())
	assert.Contains(t, drawTutorial(t, tut), "third step")
	assert.True(t, tut.ViewingPast())

	assert.True(t, tut.Forward())
	assert.Contains(t, drawTutorial(t, tut), "Stop")
	assert.False(t, tut.ViewingPast())
}

// TestBackSnapsToTheLiveStep asserts a lesson that moves on while the
// user reads an earlier screen brings the tile forward with it, and
// that Skip does the same.
func TestBackSnapsToTheLiveStep(t *testing.T) {
	t.Parallel()
	t.Run("a new step", func(t *testing.T) {
		t.Parallel()
		tut := newBackTutorial(t, backSrc)
		require.True(t, tut.WaitActive("wait_command", time.Second))
		tut.ObserveCommand("alpha", "alpha", nil, nil)
		require.True(t, tut.WaitActive("wait_command", time.Second))

		require.True(t, tut.Back())
		assert.Contains(t, drawTutorial(t, tut), "first step")

		tut.ObserveCommand("bravo", "bravo", nil, nil)
		require.True(t, tut.WaitActive("wait_command", time.Second))
		assert.Contains(t, drawTutorial(t, tut), "third step")
	})

	t.Run("skip", func(t *testing.T) {
		t.Parallel()
		tut := newBackTutorial(t, backSrc)
		require.True(t, tut.WaitActive("wait_command", time.Second))
		tut.ObserveCommand("alpha", "alpha", nil, nil)
		require.True(t, tut.WaitActive("wait_command", time.Second))

		require.True(t, tut.Back())
		tut.Skip()
		require.True(t, tut.WaitActive("wait_command", time.Second))
		assert.Contains(t, drawTutorial(t, tut), "third step")
	})
}

func newBackTutorial(t *testing.T, src string) *Tutorial {
	t.Helper()
	tut, err := New(
		"back", src,
		idetutorial.PromptStyle{}, nil, &fakeNotis{}, nil,
		nil, nil,
		term.KeyComb{Ch: ':'},
		"standard", "", nil,
		nil, nil, nil,
	)
	require.NoError(t, err)
	tut.Resize(40, 20)
	tut.Reset()
	t.Cleanup(tut.Stop)
	return tut
}

func drawTutorial(t *testing.T, tut *Tutorial) string {
	t.Helper()
	w := term.NewStringWriter(40, 20)
	tut.Draw(w)
	require.NoError(t, w.Flush())
	return strings.Join(strings.Fields(w.String()), " ")
}

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

package ide_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"

	"unstable.build/rune/internal/ide"
)

// quitTutorialSrc parks on window commands the e2e IDE can run, so
// the lesson can be walked to its end from the command prompt.
const quitTutorialSrc = `
def run():
    wait_command(command="windownew", title="welcome", text="hello there")
    wait_command(command="windowclose", title="second", text="almost done")
tutorial(entry=run)
`

// waitTutorialSrc parks on a wait_command step that never resolves in
// the e2e IDE, so a typed :quit is decided by the IDE under a live
// lesson.
const waitTutorialSrc = `
def run():
    wait_command(command="edit")
    wait_event(event="open", title="done", text="finished up")
tutorial(entry=run)
`

// The tutorial tile is a quarter of the screen, so the e2e screen has
// to be wide enough for the editor to keep a usable width beside it.
const (
	quitE2EWidth  = 100
	quitE2EHeight = 24
)

// newQuitE2EIDE builds a real IDE with <m-q> bound to quit and the
// tutorials above registered under the `tutorials:` config key, then
// returns the root handler wrapped the way the host event loop drives
// it.
func newQuitE2EIDE(t *testing.T) (e2eLockedHandler, *ide.IDE) {
	t.Helper()
	dir := t.TempDir()

	tutPath := filepath.Join(dir, "basics.star")
	require.NoError(t, os.WriteFile(tutPath, []byte(quitTutorialSrc), 0o600))
	waitPath := filepath.Join(dir, "waiting.star")
	require.NoError(t, os.WriteFile(waitPath, []byte(waitTutorialSrc), 0o600))

	cfgPath := filepath.Join(dir, "rune.yaml")
	require.NoError(t, os.WriteFile(cfgPath, fmt.Appendf(nil, `
editor:
  mode: modal
command:
  key: ":"
  key_bindings:
    "<m-q>": quit
tutorials:
  basics: %s
  waiting: %s
`, tutPath, waitPath), 0o600))

	var mu sync.Mutex
	i, drain := newHostIDE(t, &mu, dir, cfgPath)
	h := e2eLockedHandler{Handler: i.Ready(), mu: &mu, ide: i, drain: drain}
	h.Resize(quitE2EWidth, quitE2EHeight)
	i.WaitWorkspaces()
	drain()
	return h, i
}

func e2eRender(t *testing.T, h e2eLockedHandler) string {
	t.Helper()
	w := term.NewStringWriter(quitE2EWidth, quitE2EHeight)
	h.Draw(w)
	require.NoError(t, w.Flush())
	return w.String()
}

// e2eWaitFor polls the rendered frame until it contains want. A
// tutorial step becomes active on its own goroutine, so the frame it
// paints is not observable synchronously after the start command.
func e2eWaitFor(t *testing.T, h e2eLockedHandler, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		last = e2eRender(t, h)
		if strings.Contains(last, want) {
			return
		}
		h.Handle(term.Event{Type: term.EventInterrupt})
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q in frame:\n%s", want, last)
}

// e2eWaitGone is e2eWaitFor's inverse: it polls until unwanted has
// disappeared from the frame, asserting no exit is reported meanwhile.
func e2eWaitGone(t *testing.T, h e2eLockedHandler, unwanted string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		last = e2eRender(t, h)
		if !strings.Contains(last, unwanted) {
			return
		}
		exit, _ := h.Handle(term.Event{Type: term.EventInterrupt})
		require.False(t, exit, "the IDE must not exit on its own")
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q to disappear from frame:\n%s", unwanted, last)
}

func e2eFeed(t *testing.T, h e2eLockedHandler, seq string) (exit bool) {
	t.Helper()
	keys, err := term.ParseKeys(seq)
	require.NoError(t, err)
	for _, k := range keys {
		quit, _ := h.Handle(term.Event{
			Type: term.EventKey, Ch: k.Ch, Mod: k.Mod, Key: k.Key})
		exit = exit || quit
	}
	return exit
}

// TestE2EQuit drives a real IDE end to end and pins that the quit
// chord is always escapable, including while a tutorial tile is up,
// and that a tutorial finishing on its own never exits the IDE.
func TestE2EQuit(t *testing.T) {
	t.Parallel()

	t.Run("quit chord exits with no tutorial running", func(t *testing.T) {
		t.Parallel()
		h, i := newQuitE2EIDE(t)
		t.Cleanup(func() { _ = i.Close() })

		require.False(t, e2eFeed(t, h, "<m-q>"),
			"quit must open the confirm prompt before exiting")
		e2eWaitFor(t, h, "Yes")

		assert.True(t, e2eFeed(t, h, "y"),
			"answering Yes must exit the IDE")
	})

	t.Run("quit chord exits while a tutorial tile is up",
		func(t *testing.T) {
			t.Parallel()
			h, i := newQuitE2EIDE(t)
			t.Cleanup(func() { _ = i.Close() })

			require.False(t, e2eFeed(t, h, ":tutorial<space>start<space>basics<enter>"))
			e2eWaitFor(t, h, "hello there")

			require.False(t, e2eFeed(t, h, "<m-q>"),
				"quit must open the confirm prompt before exiting")
			frame := e2eRender(t, h)
			assert.Contains(t, frame, "hello there",
				"the lesson stays up under the confirm prompt")
			e2eWaitFor(t, h, "Yes")

			assert.True(t, e2eFeed(t, h, "y"),
				"answering Yes must exit the IDE")
		})

	t.Run("typed :quit exits while a tutorial is waiting for a command",
		func(t *testing.T) {
			t.Parallel()
			h, i := newQuitE2EIDE(t)
			t.Cleanup(func() { _ = i.Close() })

			require.False(t, e2eFeed(t, h, ":tutorial<space>start<space>waiting<enter>"))

			// The quit is decided by the IDE under a live lesson; its
			// exit signal has to survive the runner's frame.
			require.False(t, e2eFeed(t, h, ":quit<enter>"),
				"quit must open the confirm prompt before exiting")
			e2eWaitFor(t, h, "Yes")

			assert.True(t, e2eFeed(t, h, "y"),
				"answering Yes must exit the IDE")
		})

	t.Run("tutorial finishing does not exit the IDE", func(t *testing.T) {
		t.Parallel()
		h, i := newQuitE2EIDE(t)
		t.Cleanup(func() { _ = i.Close() })

		require.False(t, e2eFeed(t, h, ":tutorial<space>start<space>basics<enter>"))
		e2eWaitFor(t, h, "hello there")

		require.False(t, e2eFeed(t, h, "<enter>"),
			"a key never advances a step, nor exits the IDE")
		assert.Contains(t, e2eRender(t, h), "hello there")

		require.False(t, e2eFeed(t, h, ":windownew<enter>"),
			"advancing a tutorial step must not exit the IDE")
		e2eWaitFor(t, h, "almost done")

		require.False(t, e2eFeed(t, h, ":windowclose<enter>"),
			"the tutorial's final step must not exit the IDE")
		e2eWaitGone(t, h, "almost done")

		// The IDE is still alive and still quittable.
		require.False(t, e2eFeed(t, h, "<m-q>"))
		e2eWaitFor(t, h, "Yes")
		assert.True(t, e2eFeed(t, h, "y"))
	})
}

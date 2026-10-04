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

//go:build e2e

package ide

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/handler/handlertest"
	"unstable.build/rune/internal/ide/plugin"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/workspace"
)

// TestE2ETerminalTabExit runs a real process in a terminal on a real
// pty and lets it exit. A tab whose process failed must stay open with
// an editor on its output, drained of color, showing the error as the
// executor reported it; a clean exit, or a failure outside a tab,
// drops the terminal.
func TestE2ETerminalTabExit(t *testing.T) {
	const width, height = 40, 14

	const liveTab = `┌━━━━━━━───────────────────────────────┐
│$ build                               │
├──────────────────────────────────────┤
│building                              │
│error: missing dependency libfoo      │
│  required by app/cmd/server          │
│  required by app/cmd/cli             │
│make: *** [build] Error 3             │
│▐                                     │
│                                      │
│                                      │
│                                      │
│                                      │
└──────────────────────────────────────┘`
	const liveWindow = `┌──────────────────────────────────────┐
│                                      │
├──────────────────────────────────────┤
│building                              │
│error: missing dependency libfoo      │
│  required by app/cmd/server          │
│  required by app/cmd/cli             │
│make: *** [build] Error 3             │
│▐                                     │
│                                      │
│                                      │
│                                      │
│                                      │
└──────────────────────────────────────┘`
	const empty = `┌──────────────────────────────────────┐
│                                      │
├──────────────────────────────────────┤
│                                      │
│                                      │
│                                      │
│                                      │
│                                      │
│                                      │
│                                      │
│                                      │
│                                      │
│                                      │
└──────────────────────────────────────┘`
	// Typing into the dead tab edits its output; only closing it
	// removes it.
	keptFrames := func(frame, edited string) []handlertest.SequenceTestCase {
		return []handlertest.SequenceTestCase{
			{InputSequence: "", Expected: frame},
			{InputSequence: "make<enter>", Expected: edited},
			{InputSequence: "<c-w>", Expected: empty},
		}
	}

	// The build prints a line in red, which a failed tab drains.
	const build = "echo building\n" +
		"printf '\\033[31merror: missing dependency libfoo\\033[0m\\n'\n" +
		"echo '  required by app/cmd/server'\n" +
		"echo '  required by app/cmd/cli'\n" +
		"echo 'make: *** [build] Error 3'\n"
	const redLine = "error: missing dependency libfoo"

	suite := []struct {
		desc string
		// output is the script printing, defaulting to build, and
		// printed the last of it to be drawn.
		output, printed string
		// exit is how the script ends once released.
		exit   string
		window bool
		live   string
		// wantErr is the error a kept tab shows, if kept.
		wantErr string
		frames  []handlertest.SequenceTestCase
	}{
		{
			desc:    "failed tab",
			exit:    "exit 3",
			live:    liveTab,
			wantErr: "exit status 3",
			frames: keptFrames(`┌━━━━━━━───────────────────────────────┐
│$ build                               │
├──────────────────────────────────────┤
│building                              │
│error: missing dependency libfoo      │
│  required by app/cmd/server          │
│  required by app/cmd/cli             │
│make: *** [build] Error 3             │
│▐                                     │
│                                      │
│                                      │
│                                      │
│                         exit status 3│
└──────────────────────────────────────┘`,
				`┌━━━━━━━───────────────────────────────┐
│$ build                               │
├──────────────────────────────────────┤
│building                              │
│error: missing dependency libfoo      │
│  required by app/cmd/server          │
│  required by app/cmd/cli             │
│make: *** [build] Error 3             │
│make                                  │
│▐                                     │
│                                      │
│                                      │
│                         exit status 3│
└──────────────────────────────────────┘`),
		},
		{
			desc:    "killed tab",
			exit:    "kill -9 $$",
			live:    liveTab,
			wantErr: "signal: killed",
			frames: keptFrames(`┌━━━━━━━───────────────────────────────┐
│$ build                               │
├──────────────────────────────────────┤
│building                              │
│error: missing dependency libfoo      │
│  required by app/cmd/server          │
│  required by app/cmd/cli             │
│make: *** [build] Error 3             │
│▐                                     │
│                                      │
│                                      │
│                                      │
│                        signal: killed│
└──────────────────────────────────────┘`,
				`┌━━━━━━━───────────────────────────────┐
│$ build                               │
├──────────────────────────────────────┤
│building                              │
│error: missing dependency libfoo      │
│  required by app/cmd/server          │
│  required by app/cmd/cli             │
│make: *** [build] Error 3             │
│make                                  │
│▐                                     │
│                                      │
│                                      │
│                        signal: killed│
└──────────────────────────────────────┘`),
		},
		{
			// The screen keeps rows below a cursor moved up, and the
			// view is at the bottom of the scrollback.
			desc:    "failed tab with scrollback and the cursor moved up",
			output:  "seq -f 'line %02g' 1 30\nprintf '\\033[3A'\n",
			printed: "line 30",
			exit:    "exit 3",
			live: `┌━━━━━━━───────────────────────────────┐
│$ build                               │
├──────────────────────────────────────┤
│line 22                               │
│line 23                               │
│line 24                               │
│line 25                               │
│line 26                               │
│line 27                               │
│▐ine 28                               │
│line 29                               │
│line 30                               │
│                                      │
└──────────────────────────────────────┘`,
			wantErr: "exit status 3",
			frames: []handlertest.SequenceTestCase{
				{InputSequence: "", Expected: `┌━━━━━━━───────────────────────────────┐
│$ build                               │
├──────────────────────────────────────┤
│line 22                               │
│line 23                               │
│line 24                               │
│line 25                               │
│line 26                               │
│line 27                               │
│▐ine 28                               │
│line 29                               │
│line 30                               │
│                         exit status 3│
└──────────────────────────────────────┘`},
			},
		},
		{
			desc:   "clean tab",
			exit:   "exit 0",
			live:   liveTab,
			frames: []handlertest.SequenceTestCase{{Expected: empty}},
		},
		{
			desc:   "failed window terminal",
			exit:   "exit 3",
			window: true,
			live:   liveWindow,
			frames: []handlertest.SequenceTestCase{{Expected: empty}},
		},
	}

	for _, tc := range suite {
		t.Run(tc.desc, func(t *testing.T) {
			dir := t.TempDir()
			release := filepath.Join(dir, "release")
			script := filepath.Join(dir, "build.sh")
			output, printed := build, "make: *** [build] Error 3"
			if tc.output != "" {
				output, printed = tc.output, tc.printed
			}
			require.NoError(t, os.WriteFile(script, []byte(
				"#!/bin/sh\n"+output+
					"while [ ! -f "+release+" ]; do sleep 0.05; done\n"+
					tc.exit+"\n",
			), 0o755))
			t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o644) })

			b, woke := newTerminalExitE2EEx(t, dir)
			b.Resize(width, height)
			// Stands in for the host event loop, which runs what the
			// terminal scheduled before the wake-up it publishes after.
			hostLoop := func() {
				b.flushScheduled()
				if woke.Swap(false) {
					b.Handle(term.Event{Type: term.EventNone})
				}
			}
			drawn := func() string {
				return handlertest.DrawHandler(b, width, height)
			}

			open := b.ex.terminalnewtab
			if tc.window {
				open = b.ex.terminalnew
			}
			require.NoError(t, open(context.Background(), script))
			require.Eventually(t, func() bool {
				hostLoop()
				return terminalLive(b) && strings.Contains(drawn(), printed)
			}, 30*time.Second, 10*time.Millisecond,
				"the process never printed its output")
			if !tc.window {
				require.NoError(t, b.ex.tabrename(context.Background(), "build"))
			}
			handlertest.RunHandlerSequence(t, b, width, height,
				[]handlertest.SequenceTestCase{{Expected: tc.live}})

			require.NoError(t, os.WriteFile(release, nil, 0o644))
			require.Eventually(t, func() bool {
				hostLoop()
				return !terminalLive(b)
			}, 30*time.Second, 10*time.Millisecond,
				"the terminal outlived its process")

			if tc.wantErr != "" {
				assertTabIconColor(t, b, width, height, term.ColorRed)
			}
			if tc.wantErr != "" && output == build {
				// Drained of the red the process wrote it in.
				assertDrawnColor(t, b, width, height, redLine,
					term.NewRGBColor(38, 38, 38))
			}
			handlertest.RunHandlerSequence(t, b, width, height, tc.frames)
		})
	}
}

// newTerminalExitE2EEx builds an ex whose terminals run on dir through
// the production spawn path. The returned flag is raised whenever a
// terminal publishes a wake-up for the host event loop.
func newTerminalExitE2EEx(t *testing.T, dir string) (testEx, *atomic.Bool) {
	t.Helper()
	uri, err := workspaceapi.ParseURI("file://" + dir)
	require.NoError(t, err)
	fileScheme, err := workspace.NewFileScheme(
		context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = fileScheme.Close() })

	woke := new(atomic.Bool)
	b := newExForTestingTerminal(t,
		workspace.NewSchemeWorkspace(uri, fileScheme, inlineSchedule),
		exitTestEditor(), vte.DefaultConfig(), nopPublishEvent,
		plugin.DefaultBarConfig(),
		text.WithEventPublisher(func(ev term.Event) bool {
			if ev.Type == term.EventNone {
				woke.Store(true)
			}
			return true
		}))
	t.Cleanup(func() { _ = b.Close() })
	return b, woke
}

// terminalLive reports whether a running terminal is in the focused
// window, in a tab or directly.
func terminalLive(b testEx) bool {
	content, err := b.ex.invokeWindow().Content()
	if err != nil {
		return false
	}
	if tab, ok := content.(*browser.Tab); ok {
		content = tab.Handler()
	}
	av, ok := content.(*asyncVTE)
	return ok && av.real != nil && !av.closed
}

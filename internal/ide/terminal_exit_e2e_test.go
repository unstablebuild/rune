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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/browser/browsertest"
	"unstable.build/rune/internal/handler/handlertest"
	"unstable.build/rune/internal/ide/plugin"
	"unstable.build/rune/internal/term/programstatus"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/workspace"
)

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
	return newTerminalE2EEx(t, dir, vte.DefaultConfig())
}

// newTerminalE2EEx is newTerminalExitE2EEx with terminals configured by
// cfg.
func newTerminalE2EEx(t *testing.T, dir string, cfg vte.Config) (testEx, *atomic.Bool) {
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
		exitTestEditor(), cfg, nopPublishEvent,
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

func TestE2ETerminalDetach(t *testing.T) {
	const width, height = 60, 16
	ctx := context.Background()
	type fixture struct {
		b       testEx
		release string
		// loop stands in for the host event loop.
		loop  func()
		drawn func() string
	}
	waitFor := func(t *testing.T, f fixture, cond func() bool, msg string) {
		t.Helper()
		require.Eventually(t, func() bool {
			f.loop()
			return cond()
		}, 30*time.Second, 10*time.Millisecond, msg)
	}
	// open shows a new shell in the focused window, once it prompts.
	open := func(t *testing.T, f fixture) *asyncVTE {
		t.Helper()
		require.NoError(t, f.b.ex.terminalnewtab(ctx))
		var av *asyncVTE
		waitFor(t, f, func() bool {
			h, ok := f.b.ex.terminalInFocus()
			if ok {
				av, _ = h.(*asyncVTE)
			}
			return av != nil && av.real != nil && strings.Contains(f.drawn(), "$")
		}, "the shell never prompted")
		return av
	}
	// run types line into the shell av shows.
	run := func(av *asyncVTE, line string) {
		for _, r := range line {
			_, _ = av.Handle(term.Event{Type: term.EventKey, Ch: r, Raw: []byte(string(r))})
		}
		_, _ = av.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	}
	info := func(f fixture, av *asyncVTE) (vte.Info, bool) {
		return f.b.ex.terminals.info(av.ID())
	}
	liveness := func(f fixture, av *asyncVTE) vte.Liveness {
		l, _ := f.b.ex.terminals.vtes.VTELiveness(ctx, av.ID())
		return l
	}
	hostState := func(f fixture, uri workspaceapi.URI) programstatus.State {
		for _, r := range f.b.ex.tabAliases.pending[uri.String()] {
			if r.ID == hostReportID {
				return r.State
			}
		}
		return programstatus.Clear
	}
	cases := []struct {
		desc string
		run  func(t *testing.T, f fixture)
	}{{
		desc: "closing an idle shell's tab ends it",
		run: func(t *testing.T, f fixture) {
			av := open(t, f)
			waitFor(t, f, func() bool { return liveness(f, av) == vte.LivenessIdle },
				"the shell never went idle")
			require.NoError(t, f.b.ex.closeWindowContent(f.b.ex.invokeWindow()))
			waitFor(t, f, func() bool {
				_, ok := info(f, av)
				return !ok
			}, "closing the tab must end the shell")
		},
	}, {
		desc: "a job outlives its tab and is reported done",
		run: func(t *testing.T, f fixture) {
			av := open(t, f)
			// The quotes keep the typed command from reading as its output.
			run(av, "sh -c 'while [ ! -f "+f.release+" ]; do sleep 0.05; done; "+
				"yes | head -n 50000; echo fin''ished'")
			waitFor(t, f, func() bool { return liveness(f, av) == vte.LivenessRunning },
				"the job never ran")
			uri := av.real.URI()
			require.NoError(t, f.b.ex.closeWindowContent(f.b.ex.invokeWindow()))
			waitFor(t, f, func() bool { return hostState(f, uri) == programstatus.Working },
				"the detached job must be listed")
			i, ok := info(f, av)
			require.True(t, ok, "the job's terminal outlives its tab")
			assert.Equal(t, 0, i.Attachments)
			assert.Equal(t, 1, f.b.runningDetachedCount())

			require.NoError(t, os.WriteFile(f.release, nil, 0o644))
			waitFor(t, f, func() bool { return hostState(f, uri) == programstatus.Done },
				"the detached job's output must keep flowing until it is done")
			assert.Zero(t, f.b.runningDetachedCount())

			require.NoError(t, f.b.ex.terminalattach(ctx))
			waitFor(t, f, func() bool { return strings.Contains(f.drawn(), "finished") },
				"attaching shows what the job printed")
			assert.Equal(t, programstatus.Clear, hostState(f, uri))
		},
	}, {
		desc: "two views share a shell, its size follows focus",
		run: func(t *testing.T, f fixture) {
			first := open(t, f)
			firstWin := f.b.ex.invokeWindow()
			b := f.b.comp.Browser()
			secondWin, ok := b.Split(browserapi.OrientationDefault, firstWin,
				browsertest.NewTestHandler())
			require.True(t, ok)
			i, ok := info(f, first)
			require.True(t, ok)
			require.NoError(t, f.b.ex.attachTerminalTab(i, secondWin))
			b.SetFocus(secondWin)
			var second *asyncVTE
			waitFor(t, f, func() bool {
				h, ok := f.b.ex.terminalInFocus()
				if ok {
					second, _ = h.(*asyncVTE)
				}
				return second != nil && second.real != nil
			}, "the second view never attached")
			i, _ = info(f, first)
			assert.Equal(t, 2, i.Attachments)

			run(second, "echo sha''red")
			waitFor(t, f, func() bool { return strings.Count(f.drawn(), "shared") == 2 },
				"both views show what either is typed")
			for _, win := range []browser.Window{firstWin, secondWin, firstWin} {
				b.SetFocus(win)
				f.loop()
				w, h := b.ContentSize(win)
				i, _ = info(f, first)
				assert.Equal(t, []int{w, h}, []int{i.Width, i.Height},
					"the terminal takes the size of the view focused last")
			}
		},
	}}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			dir := t.TempDir()
			cfg := vte.DefaultConfig()
			cfg.CommandAndArgs = []string{"/bin/sh"}
			b, woke := newTerminalE2EEx(t, dir, cfg)
			b.Resize(width, height)
			f := fixture{
				b:       b,
				release: filepath.Join(dir, "release"),
				loop: func() {
					b.flushScheduled()
					if woke.Swap(false) {
						b.Handle(term.Event{Type: term.EventNone})
					}
				},
				drawn: func() string { return handlertest.DrawHandler(b, width, height) },
			}
			t.Cleanup(func() { _ = os.WriteFile(f.release, nil, 0o644) })
			tc.run(t, f)
		})
	}
}

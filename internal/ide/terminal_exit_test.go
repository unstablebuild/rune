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
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/shader/shaderutils"
	"unstable.build/rune/internal/handler/handlertest"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/term/vte/vtereservoir"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/emacs"
	"unstable.build/rune/internal/text/helix"
	"unstable.build/rune/internal/text/standard"
	"unstable.build/rune/internal/text/texttest"
	"unstable.build/rune/internal/text/vi"
)

// exitingVTE is a terminal whose process already exited with exitErr.
// Like vte.Handler once its pty closed, it reports exit from Handle and
// hides its cursor, while still drawing the output the process left.
type exitingVTE struct {
	*testVte
	exitErr error
	screen  terminalScreen
	fg      term.Color
	width   int
	height  int
}

// terminalScreen is the buffer of a terminal as vte.Component keeps
// it: its screen is its last rows, and scrolling back moves the view,
// not the screen.
type terminalScreen struct {
	// rows are written in the terminal's fg, scrollback included.
	rows         []string
	cursorRow    int
	scrolledBack int
}

func (v *exitingVTE) ExitErr() error { return v.exitErr }

func (v *exitingVTE) CursorAtScroll() term.Coordinates {
	return term.Coordinates{Y: v.screen.cursorRow}
}

// SeekOffset returns the row at the top of the view, as
// vte.Handler's does.
func (v *exitingVTE) SeekOffset() int {
	return max(0, len(v.screen.rows)-v.height-v.screen.scrolledBack)
}

func (v *exitingVTE) Snapshot() (vte.Snapshot, error) {
	cells := make([][]term.Cell, len(v.screen.rows))
	for y, line := range v.screen.rows {
		for _, r := range line {
			cells[y] = append(cells[y], term.NewCell(r, 1, term.Attributes{Fg: v.fg}))
		}
	}
	return vte.Snapshot{Schema: 1, Primary: vte.ScreenSnapshot{Cells: cells}}, nil
}

func (v *exitingVTE) Handle(term.Event) (bool, bool) { return true, false }

func (v *exitingVTE) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return term.Coordinates{}, 0, false
}

func (v *exitingVTE) Resize(width, height int) {
	v.width, v.height = width, height
}

func (v *exitingVTE) Draw(w term.Writer) {
	top := v.SeekOffset()
	rows := v.screen.rows[top:min(len(v.screen.rows), top+v.height)]
	for y, line := range rows {
		for x, r := range []rune(line) {
			if x >= v.width {
				break
			}
			w.SetCell(term.Coordinates{X: x, Y: y},
				term.NewCell(r, 1, term.Attributes{Fg: v.fg}))
		}
	}
}

// exitTestEditor is an editor configured with every bar, so frames
// show which of them a dead terminal tab is left with, and centering
// the cursor it is given, as the IDE configures its editors.
func exitTestEditor() text.Editor {
	tick := func(fn func()) bool { fn(); return true }
	return standard.Editor(
		standard.WithAutoCenter(true),
		standard.WithAuxiliaryBar(true, text.AuxBarConfig{
			LinesEnabled: true, ScheduleNextTick: tick,
		}),
		standard.WithStatusBarConfig(true, text.StatusBarConfig{
			Publisher:        &texttest.TestEditor{},
			ScheduleNextTick: tick,
			Layout: []text.StatusBarComponent{
				{Type: text.StatusBarTotalLines, Template: "%d lines"},
			},
		}),
	)
}

type terminalExitHarness struct {
	testEx
	terminal *exitingVTE
	av       *asyncVTE
}

// newTerminalExitHarness builds an ex editing with ed whose next
// terminal is a process that left screen and exited with exitErr.
func newTerminalExitHarness(
	t *testing.T, ed text.Editor, screen terminalScreen, exitErr error,
) *terminalExitHarness {
	t.Helper()
	h := &terminalExitHarness{testEx: newExForTesting(t, ed)}
	t.Cleanup(func() { _ = h.Close() })
	h.terminal = &exitingVTE{
		testVte: newTestVte(),
		exitErr: exitErr,
		screen:  screen,
		fg:      term.ColorGreen,
	}
	h.terminal.uri = mustURI(t, "file:///dev/ttys077")
	h.terminal.title = "make"
	h.ex.newEmulatorHandler = func([]string) (vtereservoir.VTE, error) {
		h.av = newAsyncVTE(h.ex, func() (vtereservoir.VTE, error) {
			return h.terminal, nil
		})
		return h.av, nil
	}
	return h
}

// terminalOpener opens the harness terminal and returns the URI of the
// tab holding it, which for a terminal outside a tab keys no tab.
type terminalOpener func(*testing.T, *terminalExitHarness) workspaceapi.URI

// terminalExit delivers one of the notifications of a terminal exit.
type terminalExit func(*testing.T, *terminalExitHarness)

func TestTerminalTabExit(t *testing.T) {
	const width, height = 40, 14
	failure := errors.New("exit status 2")

	build := terminalScreen{
		rows: []string{
			"$ make",
			"go build ./...",
			"./main.go:12:2: undefined: fooBarBazQux",
			"./main.go:14:9: too many arguments",
			"make: *** [build] Error 2",
			"",
		},
		cursorRow: 5,
	}
	lines := func(n int) []string {
		ret := make([]string, n)
		for i := range ret {
			ret[i] = fmt.Sprintf("line %02d", i+1)
		}
		return ret
	}
	scrollback := terminalScreen{rows: append(lines(20), ""), cursorRow: 20}
	// A program moved the cursor up and left rows below it.
	cursorAboveBottom := terminalScreen{
		rows: append(lines(18), "", "", "", "", "", "", ""), cursorRow: 18,
	}
	scrolledBack := terminalScreen{
		rows: append(lines(29), ""), cursorRow: 29, scrolledBack: 12,
	}

	openTab := func(t *testing.T, h *terminalExitHarness) workspaceapi.URI {
		require.NoError(t, h.ex.terminalnewtab(context.Background(), "make"))
		return h.av.URI()
	}
	openWindow := func(t *testing.T, h *terminalExitHarness) workspaceapi.URI {
		require.NoError(t, h.ex.terminalnew(context.Background(), "make"))
		return h.av.URI()
	}
	openSession := func(t *testing.T, h *terminalExitHarness) workspaceapi.URI {
		doc := terminalSessionDocument{
			Name:     "build",
			Snapshot: vte.Snapshot{Schema: 1, Title: "build"},
		}
		_, err := h.ex.restoreTerminalSessionTab(doc, h.ex.invokeWindow())
		require.NoError(t, err)
		uri, err := terminalSessionURI(doc.Name)
		require.NoError(t, err)
		return uri
	}
	openBackgroundTab := func(t *testing.T, h *terminalExitHarness) workspaceapi.URI {
		uri := openTab(t, h)
		require.NoError(t, h.ex.editFiles(context.Background(), "a"))
		return uri
	}
	openTabInUnfocusedWindow := func(t *testing.T, h *terminalExitHarness) workspaceapi.URI {
		uri := openTab(t, h)
		require.NoError(t, h.ex.windownew(context.Background()))
		return uri
	}

	ptyClosed := func(_ *testing.T, h *terminalExitHarness) {
		h.ex.tm.OnTabExit(h.terminal.uri)
	}
	wokenUp := func(_ *testing.T, h *terminalExitHarness) {
		h.Handle(term.Event{Type: term.EventNone})
	}
	userClosesTab := func(t *testing.T, h *terminalExitHarness) {
		require.NoError(t, h.ex.tabclose(context.Background()))
	}

	// The output is left in an editor, with the error in its message
	// line.
	const errorFrame = `┌━━━━━━────────────────────────────────┐
│$ make                                │
├──────────────────────────────────────┤
│$ make                                │
│go build ./...                        │
│./main.go:12:2: undefined: fooBarBazQu│
│./main.go:14:9: too many arguments    │
│make: *** [build] Error 2             │
│▐                                     │
│                                      │
│                                      │
│                                      │
│                         exit status 2│
└──────────────────────────────────────┘`
	const editedFrame = `┌━━━━━━────────────────────────────────┐
│$ make                                │
├──────────────────────────────────────┤
│$ make                                │
│go build ./...                        │
│./main.go:12:2: undefined: fooBarBazQu│
│./main.go:14:9: too many arguments    │
│make: *** [build] Error 2             │
│make                                  │
│▐                                     │
│                                      │
│                                      │
│                         exit status 2│
└──────────────────────────────────────┘`
	const emptyFrame = `┌──────────────────────────────────────┐
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
	keptFrames := []handlertest.SequenceTestCase{
		{InputSequence: "", Expected: errorFrame},
		{InputSequence: "make<enter>", Expected: editedFrame},
		{InputSequence: "<c-w>", Expected: emptyFrame},
	}
	droppedFrames := []handlertest.SequenceTestCase{
		{InputSequence: "", Expected: emptyFrame},
	}

	suite := []struct {
		desc    string
		exitErr error
		// screen defaults to build.
		screen   *terminalScreen
		open     terminalOpener
		exits    []terminalExit
		wantKept bool
		// gray is output drawn once the terminal exits, which must be
		// drained of color.
		gray   string
		frames []handlertest.SequenceTestCase
	}{
		{
			desc:     "failed tab, pty closed",
			exitErr:  failure,
			open:     openTab,
			exits:    []terminalExit{ptyClosed},
			wantKept: true,
			gray:     "make: *** [build] Error 2",
			frames:   keptFrames,
		},
		{
			desc:     "failed tab, woken up",
			exitErr:  failure,
			open:     openTab,
			exits:    []terminalExit{wokenUp},
			wantKept: true,
			frames:   keptFrames,
		},
		{
			desc:     "failed tab, pty closed then woken up",
			exitErr:  failure,
			open:     openTab,
			exits:    []terminalExit{ptyClosed, wokenUp},
			wantKept: true,
			frames:   keptFrames,
		},
		{
			desc:     "failed tab, woken up then pty closed",
			exitErr:  failure,
			open:     openTab,
			exits:    []terminalExit{wokenUp, ptyClosed},
			wantKept: true,
			frames:   keptFrames,
		},
		{
			desc:     "failed tab with scrollback",
			exitErr:  failure,
			screen:   &scrollback,
			open:     openTab,
			exits:    []terminalExit{ptyClosed},
			wantKept: true,
			gray:     "line 20",
			frames: []handlertest.SequenceTestCase{
				{
					// It opens where the terminal left off.
					InputSequence: "",
					Expected: `┌━━━━━━────────────────────────────────┐
│$ make                                │
├──────────────────────────────────────┤
│line 12                               │
│line 13                               │
│line 14                               │
│line 15                               │
│line 16                               │
│line 17                               │
│line 18                               │
│line 19                               │
│line 20                               │
│▐                        exit status 2│
└──────────────────────────────────────┘`,
				},
				{
					// The error follows the cursor up the output.
					InputSequence: "<pgup><pgup>",
					Expected: `┌━━━━━━────────────────────────────────┐
│$ make                                │
├──────────────────────────────────────┤
│line 01                               │
│line 02                               │
│▐ine 03                               │
│line 04                               │
│line 05                               │
│line 06                               │
│line 07                               │
│line 08                               │
│line 09                               │
│                         exit status 2│
└──────────────────────────────────────┘`,
				},
			},
		},
		{
			desc:     "failed tab whose cursor is above the bottom",
			exitErr:  failure,
			screen:   &cursorAboveBottom,
			open:     openTab,
			exits:    []terminalExit{ptyClosed},
			wantKept: true,
			frames: []handlertest.SequenceTestCase{{
				// It keeps the rows the terminal showed.
				InputSequence: "",
				Expected: `┌━━━━━━────────────────────────────────┐
│$ make                                │
├──────────────────────────────────────┤
│line 16                               │
│line 17                               │
│line 18                               │
│▐                                     │
│                                      │
│                                      │
│                                      │
│                                      │
│                                      │
│                         exit status 2│
└──────────────────────────────────────┘`,
			}},
		},
		{
			desc:     "failed tab scrolled back",
			exitErr:  failure,
			screen:   &scrolledBack,
			open:     openTab,
			exits:    []terminalExit{ptyClosed},
			wantKept: true,
			frames: []handlertest.SequenceTestCase{{
				// The cursor stays in view instead of scrolling it.
				InputSequence: "",
				Expected: `┌━━━━━━────────────────────────────────┐
│$ make                                │
├──────────────────────────────────────┤
│line 09                               │
│line 10                               │
│line 11                               │
│line 12                               │
│line 13                               │
│line 14                               │
│line 15                               │
│line 16                               │
│line 17                               │
│▐                        exit status 2│
└──────────────────────────────────────┘`,
			}},
		},
		{
			desc:     "failed restored session tab",
			exitErr:  failure,
			open:     openSession,
			exits:    []terminalExit{ptyClosed, wokenUp},
			wantKept: true,
			frames:   keptFrames,
		},
		{
			desc:     "failed tab in an unfocused window",
			exitErr:  failure,
			open:     openTabInUnfocusedWindow,
			exits:    []terminalExit{ptyClosed, wokenUp},
			wantKept: true,
			frames: []handlertest.SequenceTestCase{{
				InputSequence: "",
				Expected: `┌──────────────────────────────────────┐
│$ make                                │
├──────────────────┐┌──────────────────┐
│$ make            ││                  │
│go build ./...    ││                  │
│./main.go:12:2: un││                  │
│./main.go:14:9: to││                  │
│make: *** [build] ││                  │
│                  ││                  │
│                  ││                  │
│                  ││                  │
│                  ││                  │
│     exit status 2││                  │
└──────────────────┘└──────────────────┘`,
			}},
		},
		{
			desc:     "failed tab in the background",
			exitErr:  failure,
			open:     openBackgroundTab,
			exits:    []terminalExit{ptyClosed},
			wantKept: true,
			frames: []handlertest.SequenceTestCase{
				{
					InputSequence: "",
					Expected: `┌────────━━━───────────────────────────┐
│$ make  o a                           │
├──────────────────────────────────────┤
│1 ▐                                   │
│1                                     │
│2                                     │
│3                                     │
│4                                     │
│5                                     │
│6                                     │
│7                                     │
│8                                     │
│1 lines                               │
└──────────────────────────────────────┘`,
				},
				{
					InputSequence: "<c-h>",
					Expected: `┌━━━━━━────────────────────────────────┐
│$ make  o a                           │
├──────────────────────────────────────┤
│$ make                                │
│go build ./...                        │
│./main.go:12:2: undefined: fooBarBazQu│
│./main.go:14:9: too many arguments    │
│make: *** [build] Error 2             │
│▐                                     │
│                                      │
│                                      │
│                                      │
│                         exit status 2│
└──────────────────────────────────────┘`,
				},
			},
		},
		{
			desc:   "clean tab, pty closed",
			open:   openTab,
			exits:  []terminalExit{ptyClosed},
			frames: droppedFrames,
		},
		{
			desc:   "clean tab, woken up",
			open:   openTab,
			exits:  []terminalExit{wokenUp},
			frames: droppedFrames,
		},
		{
			desc:   "clean restored session tab",
			open:   openSession,
			exits:  []terminalExit{ptyClosed},
			frames: droppedFrames,
		},
		{
			desc:    "failed window terminal, pty closed",
			exitErr: failure,
			open:    openWindow,
			exits:   []terminalExit{ptyClosed},
			frames:  droppedFrames,
		},
		{
			// No frames: a window terminal that learns of its exit from
			// the wake-up before its close request keeps its last frame
			// until the close request runs, which production queues
			// first.
			desc:    "failed window terminal, woken up",
			exitErr: failure,
			open:    openWindow,
			exits:   []terminalExit{wokenUp},
		},
		{
			desc:    "failed tab the user closed first",
			exitErr: failure,
			open:    openTab,
			exits:   []terminalExit{userClosesTab, ptyClosed},
			frames:  droppedFrames,
		},
	}

	for _, tc := range suite {
		t.Run(tc.desc, func(t *testing.T) {
			screen := build
			if tc.screen != nil {
				screen = *tc.screen
			}
			h := newTerminalExitHarness(t, exitTestEditor(), screen, tc.exitErr)
			h.Resize(width, height)
			tabURI := tc.open(t, h)
			h.waitAsyncVTELoads()
			h.flushScheduled()
			require.NotNil(t, h.av.real)

			for _, exit := range tc.exits {
				exit(t, h)
			}

			assert.True(t, h.terminal.calledClose,
				"the dead terminal must be released either way")
			tab, kept := h.comp.Browser().Tab(tabURI)
			require.Equal(t, tc.wantKept, kept)
			if kept {
				_, isEditor := tab.Handler().(text.Handler)
				assert.True(t, isEditor, "a failed tab must be left with an editor")
				_, isTerminal := tab.Handler().(vtereservoir.VTE)
				assert.False(t, isTerminal,
					"a dead tab must not be saved or resumed as a terminal session")
				assertTabIconColor(t, h.testEx, width, height, term.ColorRed)
			}
			if tc.gray != "" {
				assertDrawnColor(t, h.testEx, width, height, tc.gray,
					shaderutils.DesaturateColor(term.ColorGreen, 1, term.ColorDefault))
			}

			if len(tc.frames) != 0 {
				handlertest.RunHandlerSequence(t, h.testEx, width, height, tc.frames)
			}
			if kept {
				closesTab := slices.ContainsFunc(tc.frames,
					func(f handlertest.SequenceTestCase) bool {
						return f.InputSequence == "<c-w>"
					})
				_, stillKept := h.comp.Browser().Tab(tabURI)
				assert.Equal(t, !closesTab, stillKept,
					"only closing the tab removes it")
			}
		})
	}
}

func TestExitedTerminalView(t *testing.T) {
	const width, height = 40, 14
	lines := func(n int) []string {
		ret := make([]string, n)
		for i := range ret {
			ret[i] = fmt.Sprintf("line %02d", i+1)
		}
		return ret
	}

	editors := []struct {
		name string
		new  func() text.Editor
	}{
		{"vi", func() text.Editor { return vi.Editor(vi.WithAutoCenter(true)) }},
		{"helix", func() text.Editor { return helix.Editor(helix.WithAutoCenter(true)) }},
		{"standard", func() text.Editor {
			return standard.Editor(standard.WithAutoCenter(true))
		}},
		{"emacs", func() text.Editor { return emacs.Editor(emacs.WithAutoCenter(true)) }},
	}
	screens := []struct {
		desc   string
		screen terminalScreen
	}{
		{
			desc:   "cursor on the bottom row",
			screen: terminalScreen{rows: append(lines(30), ""), cursorRow: 30},
		},
		{
			desc: "cursor above the bottom row",
			screen: terminalScreen{
				rows: append(lines(25), "", "", "", "", ""), cursorRow: 25,
			},
		},
		{
			desc: "scrolled back",
			screen: terminalScreen{
				rows: append(lines(29), ""), cursorRow: 29, scrolledBack: 12,
			},
		},
		{
			desc:   "output shorter than the tab",
			screen: terminalScreen{rows: append(lines(3), ""), cursorRow: 3},
		},
	}

	for _, ed := range editors {
		for _, sc := range screens {
			t.Run(ed.name+"/"+sc.desc, func(t *testing.T) {
				h := newTerminalExitHarness(t, ed.new(), sc.screen,
					errors.New("exit status 2"))
				h.Resize(width, height)
				require.NoError(t, h.ex.terminalnewtab(context.Background(), "make"))
				h.waitAsyncVTELoads()
				h.flushScheduled()
				h.ex.tm.OnTabExit(h.terminal.uri)

				tab, ok := h.comp.Browser().Tab(h.av.URI())
				require.True(t, ok)
				edh, ok := tab.Handler().(text.Handler)
				require.True(t, ok)

				w, ht := h.terminal.width, h.terminal.height
				drawn := func(c interface{ Draw(term.Writer) }) []string {
					sw := term.NewStringWriter(w, ht)
					c.Draw(sw)
					require.NoError(t, sw.Flush())
					return strings.Split(sw.String(), "\n")
				}
				got := drawn(edh)
				// The last row carries the exit status.
				assert.Equal(t, drawn(h.terminal)[:ht-1], got[:ht-1],
					"the editor must show the rows the terminal showed")
				assert.True(t, strings.HasSuffix(got[ht-1], "exit status 2"),
					"the exit status must be drawn bottom right, got %q", got[ht-1])
				wantCursor := min(sc.screen.cursorRow, h.terminal.SeekOffset()+ht-1)
				assert.Equal(t, term.Coordinates{Y: wantCursor}, edh.CursorAtScroll())
			})
		}
	}
}

func TestGrayTerminalCells(t *testing.T) {
	gray := func(l int32) term.Color { return term.NewRGBColor(l, l, l) }
	defaultFg := term.NewRGBColor(200, 100, 50)

	suite := []struct {
		desc           string
		fg, bg         term.Color
		wantFg, wantBg term.Color
	}{
		{
			desc: "rgb",
			fg:   term.NewRGBColor(255, 0, 0), bg: term.NewRGBColor(0, 0, 255),
			wantFg: gray(76), wantBg: gray(29),
		},
		{
			desc: "palette",
			fg:   term.PaletteColor(1), bg: term.PaletteColor(4),
			wantFg: gray(38), wantBg: gray(15),
		},
		{
			desc: "default foreground takes the theme's",
			fg:   term.ColorDefault, bg: term.ColorDefault,
			wantFg: gray(124), wantBg: term.ColorDefault,
		},
		{
			desc: "already gray",
			fg:   gray(90), bg: gray(10),
			wantFg: gray(90), wantBg: gray(10),
		},
	}

	for _, tc := range suite {
		t.Run(tc.desc, func(t *testing.T) {
			c := term.NewCell('x', 1, term.Attributes{
				Fg: tc.fg, Bg: tc.bg, Attrs: term.AttrBold,
			})
			cells := [][]term.Cell{{c, c}, nil, {c}}
			grayTerminalCells(cells, defaultFg)

			want := term.NewCell('x', 1, term.Attributes{
				Fg: tc.wantFg, Bg: tc.wantBg, Attrs: term.AttrBold,
			})
			assert.Equal(t, [][]term.Cell{{want, want}, nil, {want}}, cells)
		})
	}
}

// assertTabIconColor asserts the color of the icon of the first
// terminal tab in the tab bar.
func assertTabIconColor(
	t *testing.T, b testEx, width, height int, want term.Color,
) {
	t.Helper()
	w := term.NewStringWriter(width, height)
	b.Draw(w)
	require.NoError(t, w.Flush())
	cells := w.Cells()
	icon := slices.IndexFunc(cells, func(c term.Cell) bool {
		return c.Ch == b.ex.config.Icons.Terminal
	})
	require.NotEqual(t, -1, icon, "the terminal tab icon must be drawn:\n%s", w.String())
	assert.Equal(t, want, cells[icon].Attributes().Fg)
}

// assertDrawnColor asserts msg is drawn in want wherever it appears.
func assertDrawnColor(
	t *testing.T, b testEx, width, height int, msg string, want term.Color,
) {
	t.Helper()
	w := term.NewStringWriter(width, height)
	b.Draw(w)
	require.NoError(t, w.Flush())
	lines := strings.Split(w.String(), "\n")
	cells := w.Cells()
	found := false
	for y, line := range lines {
		x := strings.Index(line, msg)
		if x == -1 {
			continue
		}
		found = true
		col := len([]rune(line[:x]))
		for i := range len([]rune(msg)) {
			assert.Equal(t, want, cells[y*width+col+i].Attributes().Fg,
				"%q must be drawn in %v", msg, want)
		}
	}
	require.True(t, found, "%q must be drawn:\n%s", msg, w.String())
}

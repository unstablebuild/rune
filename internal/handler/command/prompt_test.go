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

package command

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/handler/handlertest"
)

func TestCommandHandlerManualsDrawTooSmallForManual(t *testing.T) {
	cfg := testDefaultConfig()
	cfg.ShowManual = true
	cfg.HistoryCycleKey = term.KeyComb{Ch: '@'}
	cfg.FrameCharSet = component.FrameCharSetDefault()
	cfg.Sync = true

	tsuite := []struct {
		desc         string
		sequence     string
		commands     []Manual
		expectedDraw string
	}{
		{"initializes no commands empty", "", nil, `
▐                   
                    
                    
                    
                    
                    
                    
                    
                    
                    `},
		{"initializes no commands empty search yields 0", "a", nil, `
a▐                  
                    
                    
                    
                    
                    
                    
                    
                    
                    `},
		{"initializes with some commands", "", goodTestCommands,
			`
▐                   
subaru              
jeep                
────────────────────
USAGE               
subaru outback      
touring xt          
                    
DESCRIPTION         
2021 top of the     `},
		{"initializes with some commands search match", "e", goodTestCommands,
			`
e▐                  
jeep                
mercedes            
────────────────────
USAGE               
jeep gladiator      
sport s             
                    
DESCRIPTION         
2022 bottom of the  `},
		{"initializes with lots of commands", "", goodLotsTestCommands,
			`
▐                   
0                   
1                   
────────────────────
USAGE               
0 <nothing>         
                    
DESCRIPTION         
The void.           
                    `},
		{"draw command NOT in list with no args",
			"1", nil, `
1▐                  
                    
                    
                    
                    
                    
                    
                    
                    
                    `},
		{"draw fully typed command with args with auto-complete with expanded last arg and delete in the middle",
			"merce ~^^^^^^^^^erce my", goodTestCommands, `
mercedes my▐        
                    
                    
────────────────────
USAGE               
mercedes GL 450     
                    
DESCRIPTION         
2014 old luxury car.
                    `},
	}

	for _, tcase := range tsuite {
		t.Run(tcase.desc, func(t *testing.T) {
			t.Parallel()
			dispatchFn, cleanup := nopDispatch()
			defer cleanup(t)

			completeFn, cleanupComplete := nopComplete()
			defer cleanupComplete(t)

			storage := storagestub.NewInMemoryService()
			b := NewPrompt(
				storage, FuncCompleter(completeFn), FuncDispatcher(dispatchFn),
				term.NopInterrupter(), tcase.commands, cfg,
			)
			defer b.Close()
			cases := []handlertest.SequenceTestCase{
				{InputSequence: "_" + tcase.sequence + "_", Expected: tcase.expectedDraw[1:]},
			}
			handlertest.TestHandlerSequence(t, testCommandHandler{b}, 20, 10, cases)
		})
	}
}

func TestCommandHandlerPreview(t *testing.T) {
	t.Run("esc at the end", func(t *testing.T) {
		storage := storagestub.NewInMemoryService()
		cfg := testDefaultConfig()
		cfg.ShowManual = false
		cfg.HistoryCycleKey = term.KeyComb{Ch: '@'}
		cfg.Sync = true

		dispatchFn := func(cmd string, args ...string) bool {
			return true
		}

		var dispatches []string
		var state string
		previewFn := func(cmd string, args ...string) (component.Responsive, func(), bool) {
			prevState := state
			state = args[0]
			dispatches = append(dispatches, strings.Join(append([]string{cmd}, args...), " "))
			return nil, func() {
				state = prevState
			}, true
		}

		completeFn, cleanupComplete := completeWith("arg1", "arg2")()
		defer cleanupComplete(t)

		cmd := Manual{Name: "kotomichi"}
		interrupter := term.NopInterrupter()
		b := NewPrompt(
			storage, FuncCompleter(completeFn), FuncDispatcherWithPreview(dispatchFn, previewFn),
			interrupter, []Manual{cmd}, cfg,
		)
		defer b.Close()

		cases := []handlertest.SequenceTestCase{
			{InputSequence: "kotomichi ⬇⬇", Expected: `kotomichi ▐         
arg1                
arg2                
                    
                    
                    
                    
                    
                    
                    `},
		}
		handlertest.TestHandlerSequence(t, testCommandHandler{b}, 20, 10, cases)
		b.Wait()
		assert.Equal(t, "arg2", state)
		assert.Equal(t, []string{"kotomichi arg1", "kotomichi arg2"}, dispatches)

		cases = []handlertest.SequenceTestCase{
			{InputSequence: "⬆", Expected: `kotomichi ▐         
arg1                
arg2                
                    
                    
                    
                    
                    
                    
                    `},
		}
		handlertest.TestHandlerSequence(t, testCommandHandler{b}, 20, 10, cases)
		b.Wait()
		assert.Equal(t, "arg1", state)
		assert.Equal(t, []string{"kotomichi arg1", "kotomichi arg2", "kotomichi arg1"}, dispatches)

		cases = []handlertest.SequenceTestCase{
			{InputSequence: "⬆", Expected: `kotomichi ▐         
arg1                
arg2                
                    
                    
                    
                    
                    
                    
                    `},
		}
		handlertest.TestHandlerSequence(t, testCommandHandler{b}, 20, 10, cases)
		b.Wait()
		assert.Equal(t, "", state)
		assert.Equal(t, []string{"kotomichi arg1", "kotomichi arg2", "kotomichi arg1"}, dispatches)

		cases = []handlertest.SequenceTestCase{
			{InputSequence: "<", Expected: `kotomichi ▐         
arg1                
arg2                
                    
                    
                    
                    
                    
                    
                    `},
		}
		handlertest.TestHandlerSequence(t, testCommandHandler{b}, 20, 10, cases)
		b.Wait()
		assert.Equal(t, "", state)
		assert.Equal(t, []string{"kotomichi arg1", "kotomichi arg2", "kotomichi arg1"}, dispatches)
	})

	t.Run("dispatch at the end", func(t *testing.T) {
		storage := storagestub.NewInMemoryService()
		cfg := testDefaultConfig()
		cfg.ShowManual = false
		cfg.HistoryCycleKey = term.KeyComb{Ch: '@'}
		cfg.Sync = true

		var state string
		dispatchFn := func(cmd string, args ...string) bool {
			state = args[0]
			return true
		}

		var dispatches []string
		previewFn := func(cmd string, args ...string) (component.Responsive, func(), bool) {
			prevState := state
			state = args[0]
			dispatches = append(dispatches, strings.Join(append([]string{cmd}, args...), " "))
			return nil, func() {
				state = prevState
			}, true
		}

		completeFn, cleanupComplete := completeWith("arg1", "arg2")()
		defer cleanupComplete(t)

		cmd := Manual{Name: "kotomichi"}
		interrupter := term.NopInterrupter()
		b := NewPrompt(
			storage, FuncCompleter(completeFn), FuncDispatcherWithPreview(dispatchFn, previewFn),
			interrupter, []Manual{cmd}, cfg,
		)
		defer b.Close()

		cases := []handlertest.SequenceTestCase{
			{InputSequence: "kotomichi ⬇⬇", Expected: `kotomichi ▐         
arg1                
arg2                
                    
                    
                    
                    
                    
                    
                    `},
		}
		handlertest.TestHandlerSequence(t, testCommandHandler{b}, 20, 10, cases)
		b.Wait()
		assert.Equal(t, "arg2", state)
		assert.Equal(t, []string{"kotomichi arg1", "kotomichi arg2"}, dispatches)

		cases = []handlertest.SequenceTestCase{
			{InputSequence: "⬆ar✌>", Expected: `▐                   
kotomichi           
                    
                    
                    
                    
                    
                    
                    
                    `}, // unimportant after dispatching
		}
		handlertest.TestHandlerSequence(t, testCommandHandler{b}, 20, 10, cases)
		b.Wait()
		assert.Equal(t, "arg1", state)
		assert.Equal(t, []string{"kotomichi arg1", "kotomichi arg2", "kotomichi arg1"}, dispatches)
	})

	t.Run("preview returns manual", func(t *testing.T) {
		storage := storagestub.NewInMemoryService()
		cfg := testDefaultConfig()
		cfg.ShowManual = true
		cfg.HistoryCycleKey = term.KeyComb{Ch: '@'}
		cfg.Sync = true

		dispatchFn := func(cmd string, args ...string) bool {
			return true
		}

		previewFn := func(cmd string, args ...string) (component.Responsive, func(), bool) {
			str := fmt.Sprintf("YAY %v", args)
			comp := component.NewResponsiveString(str,
				component.StringResponsiveConfig{})
			return comp, nil, true
		}

		completeFn, cleanupComplete := completeWith("arg1", "arg2")()
		defer cleanupComplete(t)

		cmd := Manual{Name: "kotomichi"}
		interrupter := term.NopInterrupter()
		b := NewPrompt(
			storage, FuncCompleter(completeFn), FuncDispatcherWithPreview(dispatchFn, previewFn),
			interrupter, []Manual{cmd}, cfg,
		)
		defer b.Close()

		cases := []handlertest.SequenceTestCase{
			{InputSequence: "<tab><down>", Expected: `kotomichi ▐         
arg1                
arg2                
                    
                    
                    
                    
                    
                    
YAY [arg1]          `},
		}
		handlertest.RunHandlerSequence(t, testCommandHandler{b}, 20, 10, cases)

		cases = []handlertest.SequenceTestCase{
			{InputSequence: "<up>", Expected: `kotomichi ▐         
arg1                
arg2                
                    
USAGE               
kotomichi           
                    
DESCRIPTION         
                    
                    `},
		}
		handlertest.RunHandlerSequence(t, testCommandHandler{b}, 20, 10, cases)

		cases = []handlertest.SequenceTestCase{
			{InputSequence: "<down><down>", Expected: `kotomichi ▐         
arg1                
arg2                
                    
                    
                    
                    
                    
                    
YAY [arg2]          `},
		}
		handlertest.RunHandlerSequence(t, testCommandHandler{b}, 20, 10, cases)

		cases = []handlertest.SequenceTestCase{
			{InputSequence: "<backspace>", Expected: `kotomichi▐          
kotomichi           
                    
                    
USAGE               
kotomichi           
                    
DESCRIPTION         
                    
                    `},
		}
		handlertest.RunHandlerSequence(t, testCommandHandler{b}, 20, 10, cases)

		cases = []handlertest.SequenceTestCase{
			{InputSequence: "<space>a<down>2", Expected: `kotomichi a2▐       
arg2                
                    
                    
USAGE               
kotomichi           
                    
DESCRIPTION         
                    
                    `},
		}
		handlertest.RunHandlerSequence(t, testCommandHandler{b}, 20, 10, cases)

		cases = []handlertest.SequenceTestCase{
			{InputSequence: "<down>", Expected: `kotomichi a2▐       
arg2                
                    
                    
                    
                    
                    
                    
                    
YAY [arg2]          `},
		}
		handlertest.RunHandlerSequence(t, testCommandHandler{b}, 20, 10, cases)
	})
}

func TestCommandHandlerCursorWrapping(t *testing.T) {
	// At width=20 the prompt's input field has leftWidgetWidth=17
	// (animationWidth=3 reserved on the right). These cases exercise
	// the cursor placement when the typed buffer wraps onto multiple
	// rendered rows or contains multi-byte runes whose byte length
	// drifts from their display-cell width.
	cfg := testDefaultConfig()
	cfg.ShowManual = false
	cfg.HistoryCycleKey = term.KeyComb{Ch: '@'}
	cfg.Sync = true

	type wrapCase struct {
		desc     string
		sequence string
		expected string
	}
	tsuite := []wrapCase{
		{
			// 25 ASCII chars on width 20 (leftWidget=17): row 0 holds
			// 17 cells, row 1 holds the remaining 8; cursor sits at
			// the end of the wrapped text (col 8, row 1).
			"25 ascii chars wrap to two rows",
			"abcdefghijklmnopqrstuvwxy",
			`abcdefghijklmnopq   
rstuvwxy▐           
                    
                    
                    
                    
                    
                    
                    
                    `,
		},
		{
			// Exactly leftWidgetWidth cells: input fits in a single
			// rendered row. The cursor must stay on row 0 (clamped
			// to col leftWidgetWidth-1) instead of falling onto row
			// 1 which belongs to the search-list area.
			"exactly leftWidgetWidth cells stays on input row",
			"abcdefghijklmnopq",
			`abcdefghijklmnop▐   
                    
                    
                    
                    
                    
                    
                    
                    
                    `,
		},
		{
			// Multi-byte rune ('é' is 2 bytes, 1 cell) before a wrap
			// boundary exercises the byte-vs-cell drift: 18 cells
			// total, wraps to two rows of 17 + 1 cells, cursor at
			// (1, 1).
			"multi-byte rune before wrap boundary",
			"éabcdefghijklmnopq",
			`éabcdefghijklmnop   
q▐                  
                    
                    
                    
                    
                    
                    
                    
                    `,
		},
		{
			// In args mode the full buffer (cmd + space + arg) wraps
			// across rows; the cursor must land at the end of the
			// wrapped argument relative to the input row group.
			"command + long argument wraps across rows",
			"ko<space>abcdefghijklmnopqrstuv",
			`ko abcdefghijklmn   
opqrstuv▐           
                    
                    
                    
                    
                    
                    
                    
                    `,
		},
	}

	for _, tcase := range tsuite {
		t.Run(tcase.desc, func(t *testing.T) {
			dispatchFn, cleanup := nopDispatch()
			defer cleanup(t)
			completeFn, cleanupComplete := nopComplete()
			defer cleanupComplete(t)

			storage := storagestub.NewInMemoryService()
			b := NewPrompt(
				storage, FuncCompleter(completeFn), FuncDispatcher(dispatchFn),
				term.NopInterrupter(), nil, cfg,
			)
			defer b.Close()

			cases := []handlertest.SequenceTestCase{
				{InputSequence: tcase.sequence, Expected: tcase.expected},
			}
			handlertest.RunHandlerSequence(t, testCommandHandler{b}, 20, 10, cases)
		})
	}

	// Multi-row buffers are reachable when the edit-mode editor
	// inserts a newline and the buffer is replayed back through the
	// prompt; each newline starts a fresh visual row, so the cursor
	// must land at the end of the LAST buffer row, not at a column
	// derived from summing all cells in the buffer.
	bufCases := []struct {
		desc string
		// buf is written verbatim into the underlying cell.Buffer,
		// bypassing the regular keystroke handlers so we can stage
		// configurations (newlines, raw control bytes, …) that
		// users can produce indirectly via the modal editor or
		// pasted history but cannot type one rune at a time.
		buf  string
		want term.Coordinates
	}{
		{
			// Two-row buffer: cursor must land on the last buffer
			// row, not at a column derived from summing all cells.
			"newline before short text",
			"hello\nwor",
			term.Coordinates{X: 3, Y: 1},
		},
		{
			// Trailing newline opens a new (empty) row; cursor
			// belongs at column 0 of that row.
			"trailing newline",
			"abc\n",
			term.Coordinates{X: 0, Y: 1},
		},
		{
			// Several blank lines: cursor must sit on the last
			// row, not on row 0.
			"only newlines",
			"\n\n\n",
			term.Coordinates{X: 0, Y: 3},
		},
		{
			// Wide CJK runes count as 2 display columns each. Eight
			// CJK glyphs occupy the full 16 visible columns of the
			// 17-cell input row, so the cursor sits at column 16.
			"eight cjk wide chars fill the row",
			"中中中中中中中中",
			term.Coordinates{X: 16, Y: 0},
		},
		{
			// Nine CJK runes overflow the 17-column input row by
			// one display cell; the renderer truncates the trailing
			// glyph and the cursor must clamp to the last visible
			// column instead of escaping past the input area.
			"nine cjk overflows display width",
			"中中中中中中中中中",
			term.Coordinates{X: 16, Y: 0},
		},
		{
			// Twenty CJK glyphs (40 display columns) wrap at the
			// cell-count boundary (17), not the display-width
			// boundary: row 0 holds cells [0..16] (the renderer
			// truncates the trailing glyph that would overflow
			// past column 17), row 1 holds cells [17..19] which
			// occupy columns 0..5; the cursor must land at (6, 1),
			// not at the far right of the row.
			"twenty cjk wraps by cell count",
			"中中中中中中中中中中中中中中中中中中中中",
			term.Coordinates{X: 6, Y: 1},
		},
		{
			// 18 CJK = 18 cells: row 0 holds 17 cells, row 1 holds
			// the lone trailing CJK (display width 2). Cursor sits
			// at column 2 of row 1, NOT at the far-right clamp —
			// dividing total display width (36) by leftWidgetWidth
			// (17) would push the cursor to row 2 and trigger a
			// spurious clamp to (16, 1).
			"eighteen cjk wraps to second row at column 2",
			"中中中中中中中中中中中中中中中中中中",
			term.Coordinates{X: 2, Y: 1},
		},
		{
			// 17 ASCII + 1 CJK = 18 cells: same wrap shape as the
			// 18-CJK case but with mixed widths on row 0. Verifies
			// the algorithm tracks per-row cell counts rather than
			// summing display widths across rows.
			"ascii row plus trailing wide rune wraps",
			"abcdefghijklmnopq中",
			term.Coordinates{X: 2, Y: 1},
		},
		{
			// Mixed-width row that exceeds display width but fits
			// by cell count: 16 CJK + 1 ASCII = 17 cells (display
			// 33). One visual row, cursor clamps to last visible
			// column.
			"sixteen cjk plus ascii fills exactly one row",
			"中中中中中中中中中中中中中中中中a",
			term.Coordinates{X: 16, Y: 0},
		},
		{
			// CJK row followed by a short ASCII row exercises the
			// per-row wrap accumulation: row 0 wraps once and
			// occupies one visual row; the cursor must land on the
			// short row at column 1.
			"cjk row then short ascii row",
			"中中中中\nx",
			term.Coordinates{X: 1, Y: 1},
		},
		{
			// Two leading blank rows count as two visual rows; the
			// cursor on the wrapped third row must land at (8, 3).
			"two blank rows then wrapping ascii",
			"\n\nabcdefghijklmnopqrstuvwxy",
			term.Coordinates{X: 8, Y: 3},
		},
		{
			// A wrapping ASCII row followed by a short row: the
			// cursor must offset by the wrapped row count rather
			// than dividing by leftWidgetWidth across the whole
			// buffer.
			"wrapped row followed by short row",
			"abcdefghijklmnopqrstuvwxy\nfoo",
			term.Coordinates{X: 3, Y: 2},
		},
		{
			// A leading empty row still counts as one visual row,
			// pushing the cursor onto the row below.
			"empty leading row",
			"\nabc",
			term.Coordinates{X: 3, Y: 1},
		},
		{
			// Tabs are stored as a single cell of zero display
			// width by ReadFrom; treat them as occupying one column
			// so the cursor advances rather than collapsing onto a
			// previous glyph.
			"tab then text",
			"\thi",
			term.Coordinates{X: 3, Y: 0},
		},
		{
			// NUL bytes survive the byte-stream as zero-width cells
			// (same shape as tabs from ReadFrom). The cursor must
			// still advance one column per cell.
			"null byte between letters",
			"a\x00b",
			term.Coordinates{X: 3, Y: 0},
		},
		{
			// Buffers larger than the visible input area must clamp
			// the cursor to the last visible row's last column
			// instead of returning an out-of-bounds row that would
			// fall onto the search list.
			"buffer overflows visible rows",
			strings.Repeat("a", 17*12),
			term.Coordinates{X: 16, Y: 9},
		},
	}
	for _, tc := range bufCases {
		t.Run(tc.desc, func(t *testing.T) {
			dispatchFn, cleanup := nopDispatch()
			defer cleanup(t)
			completeFn, cleanupComplete := nopComplete()
			defer cleanupComplete(t)

			storage := storagestub.NewInMemoryService()
			b := NewPrompt(
				storage, FuncCompleter(completeFn), FuncDispatcher(dispatchFn),
				term.NopInterrupter(), nil, cfg,
			)
			defer b.Close()

			b.Resize(20, 10)
			b.buf.WriteString(tc.buf)

			cur, _, ok := b.Cursor()
			require.True(t, ok)
			assert.Equal(t, tc.want, cur)
		})
	}
}

func TestCommandHandlerDispatch(t *testing.T) {
	storage := storagestub.NewInMemoryService()
	cfg := testDefaultConfig()
	cfg.ShowManual = false
	cfg.HistoryCycleKey = term.KeyComb{Ch: '@'}
	cfg.Sync = true
	dirs := testDirTree(t)

	tsuite := []struct {
		desc        string
		sequence    string
		commands    []string
		completeCmd func() (func(ctx context.Context, args []string) (iterator.Iterator[string], string, error), func(*testing.T))
		dispatchCmd func() (func(command string, args ...string) bool, func(*testing.T))
	}{
		{"dispatches command NOT in list with no args",
			"1>", []string{"lane", "lorelai", "rori"},
			nopComplete, expectDispatch("1")},
		{"dispatches command in list with no args",
			"lo>", []string{"lane", "lorelai", "rori"},
			nopComplete, expectDispatch("lorelai")},
		{"dispatches command NOT in list with args no auto-complete",
			"1 /tmp/a>", []string{"lane", "lorelai", "rori"},
			nopComplete, expectDispatch("1", "/tmp/a")},
		{"dispatches command with args no auto-complete",
			"lo /tmp/a>", []string{"lane", "lorelai", "rori"},
			nopComplete, expectDispatch("lorelai", "/tmp/a")},
		{"dispatches command with args with auto-complete",
			"ro my#>", []string{"lane", "lorelai", "rori"},
			completeWith("myArg"), expectDispatch("rori", "myArg")},
		{"dispatches fully typed command with args with auto-complete",
			"rori my#>", []string{"lane", "lorelai", "rori"},
			completeWith("myArg"), expectDispatch("rori", "myArg")},
		{"dispatches fully typed command with args with auto-complete and delete in the middle",
			"rori ^ my#>", []string{"lane", "lorelai", "rori"},
			completeWith("myArg"), expectDispatch("rori", "myArg")},
		{"dispatches command with args with auto-complete one last space",
			"ro my# >", []string{"lane", "lorelai", "rori"},
			completeRespectively([]string{"myArg", "myArg"}), expectDispatch("rori", "myArg")},
		{"dispatches command with args with auto-complete space that's removed",
			"ro my# ^>", []string{"lane", "lorelai", "rori"},
			completeRespectively([]string{"myArg", "myArg"}), expectDispatch("rori", "myArg")},
		{"keeps command when deleting trailing space after normalized first argument",
			"workspaceopen ssh://10.0.0.6/~/src/rune ^>", []string{"workspaceopen"},
			completeNormalizing("ssh://10.0.0.6/~/src/rune"),
			expectDispatch("workspaceopen", "ssh://10.0.0.6/~/src/rune")},
		{"keeps preceding argument when deleting trailing space after normalized later argument",
			"command fixed normalized ^>", []string{"command"},
			completeNormalizing("normalized"),
			expectDispatch("command", "fixed", "normalized")},
		{"dispatches command with args with auto-complete delete and re-typed all",
			"ro my# ^^^^^^^^^^^^ro my# a>", []string{"lane", "lorelai", "rori"},
			completeWith("myArg"), expectDispatch("rori", "myArg", "a")},
		{"dispatches command from history no autocomplete",
			"rori myArg>lorelai myArg>@ oArg>", []string{"lane", "lorelai", "rori"},
			nopComplete, expectDispatch("lorelai", "myArg", "oArg")},
		{"dispatches command with extra spaces in args no auto-complete",
			"lo   /tmp/a>", []string{"lane", "lorelai", "rori"},
			nopComplete, expectDispatch("lorelai", "/tmp/a")},
		{"dispatches command with extra spaces in args that are deleted no auto-complete",
			"lo   ^^/tmp/a>", []string{"lane", "lorelai", "rori"},
			nopComplete, expectDispatch("lorelai", "/tmp/a")},
		{"dispatches command with multiple args and completer gets called for every character",
			"ro my#oro#>", []string{"lane", "lorelai", "rori"},
			expectCompleteWith(
				[][]string{
					{""}, {"m"}, {"myArg", ""}, {"myArg", "o"},
					{"myArg", "oregano", ""},
				},
				[][]string{
					{"myArg"}, {"myArg"}, {"myArg"}, {"oregano", "oregani"}, {"oregano", "oregani"},
					{"oregano", "oregani"}, {"oregano", "oregani"}, {"oregano", "oregani"}, {},
				}),
			expectDispatch("rori", "myArg", "oregano")},
		{"dispatch delete and re-type all with no autocomplete",
			"rori myArg ^^^^^^^^^^^rori myArg a>", []string{"lane", "lorelai", "rori"},
			nopComplete, expectDispatch("rori", "myArg", "a")},
		{"dispatch from history with autocomplete",
			"lo my#>ro my#>@ oArg>", []string{"lane", "lorelai", "rori"},
			completeWith("myArg"), expectDispatch("rori", "myArg", "oArg")},
		{"dispatch delete after load from history with autocomplete",
			"lo my#>ro my#>@^^^^^oArg>", []string{"lane", "lorelai", "rori"},
			completeWith("myArg"), expectDispatch("rori", "oArg")},
		{"dispatch delete after load from history with autocomplete scroll through history",
			"lo my#>ro my#>@@^^^^^oArg>", []string{"lane", "lorelai", "rori"},
			completeWith("myArg"), expectDispatch("lorelai", "oArg")},
		{"dispatch literal arg if history has option but user IS NOT scrolling",
			"lo my#>ro my#>lo m>", []string{"lane", "lorelai", "rori"},
			completeWith("myArg"), expectDispatch("lorelai", "m")},
		{"dispatch complete arg if history has option but user IS scrolling",
			"lo my#>ro my#>lo m*>", []string{"lane", "lorelai", "rori"},
			completeWith("myArg"), expectDispatch("lorelai", "myArg")},
		{"dispatch auto-complete with enter regardless of whether user IS scrolling",
			"lo>", []string{"lane", "lorelai", "rori"},
			completeWith("myArg"), expectDispatch("lorelai")},
		{"dispatch auto-complete with tab and enter regardless of whether user IS scrolling",
			"lo#>", []string{"lane", "lorelai", "rori"},
			completeWith("myArg"), expectDispatch("lorelai")},
		{"dispatch backslash-escaped space stays in same arg",
			`lo path\ with\ space>`, []string{"lane", "lorelai", "rori"},
			nopComplete, expectDispatch("lorelai", "path with space")},
		{"dispatch single-quoted arg stays in same arg",
			"lo 'path with space'>", []string{"lane", "lorelai", "rori"},
			nopComplete, expectDispatch("lorelai", "path with space")},
		{"dispatch double-quoted arg stays in same arg",
			`lo "path with space">`, []string{"lane", "lorelai", "rori"},
			nopComplete, expectDispatch("lorelai", "path with space")},
		{"dispatch tab on a directory candidate descends instead of terminating",
			"wo a#>", []string{"workspaceopen"},
			completeDirsUnder(dirs), expectDispatch("workspaceopen", "alpha/")},
		{"dispatch repeated tabs descend one level per tab",
			"wo a#b#>", []string{"workspaceopen"},
			completeDirsUnder(dirs), expectDispatch("workspaceopen", "alpha/beta/")},
		{"dispatch tabs descend to the bottom of the tree",
			"wo a#b#g#>", []string{"workspaceopen"},
			completeDirsUnder(dirs), expectDispatch("workspaceopen", "alpha/beta/gamma/")},
		{"dispatch typing after a descent narrows within that directory",
			"wo a#d#>", []string{"workspaceopen"},
			completeDirsUnder(dirs), expectDispatch("workspaceopen", "alpha/delta/")},
		{"dispatch descent into a directory whose name has a space",
			"wo m#>", []string{"workspaceopen"},
			completeDirsUnder(dirs), expectDispatch("workspaceopen", "my dir/")},
		{"dispatch descent past a directory whose name has a space",
			"wo m##>", []string{"workspaceopen"},
			completeDirsUnder(dirs), expectDispatch("workspaceopen", "my dir/inner/")},
		{"dispatch backspace after a descent edits the same argument",
			"wo a#^>", []string{"workspaceopen"},
			completeDirsUnder(dirs), expectDispatch("workspaceopen", "alpha")},
		{"dispatch space after a descent terminates the argument",
			"wo a# x>", []string{"workspaceopen"},
			completeDirsUnder(dirs), expectDispatch("workspaceopen", "alpha/", "x")},
		{"dispatch tab with no matching directory keeps the typed token",
			"wo zzz#>", []string{"workspaceopen"},
			completeDirsUnder(dirs), expectDispatch("workspaceopen", "zzz")},
		{"dispatch tab on a terminal candidate still terminates the argument",
			"wo p#x>", []string{"workspaceopen"},
			completeWith("/prev/ws", "alpha/"),
			expectDispatch("workspaceopen", "/prev/ws", "x")},
		{"dispatch tab on a partial candidate picked out of a mixed list",
			"wo a#>", []string{"workspaceopen"},
			completeWith("/prev/ws", "alpha/"),
			expectDispatch("workspaceopen", "alpha/")},
		{"dispatch tab on a command name ending in a separator still commits",
			"ws#>", []string{"ws/"},
			completeDirsUnder(dirs), expectDispatch("ws/")},
	}

	for _, tcase := range tsuite {
		t.Run(tcase.desc, func(t *testing.T) {
			dispatchFn, cleanup := tcase.dispatchCmd()
			defer cleanup(t)

			completeFn, cleanupComplete := tcase.completeCmd()
			defer cleanupComplete(t)

			interrupter := term.NopInterrupter()
			b := NewPrompt(
				storage, FuncCompleter(completeFn), FuncDispatcher(dispatchFn),
				interrupter, testNoManualCommands(tcase.commands), cfg,
			)
			defer b.Close()
			for _, ch := range tcase.sequence {
				b.Wait()
				switch ch {
				case '#':
					b.Handle(term.Event{Type: term.EventKey, Key: term.KeyTab})
				case '*':
					b.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowDown})
				case '>':
					b.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
				case '^':
					b.Handle(term.Event{Type: term.EventKey, Key: term.KeyBackspace})
				default:
					b.Handle(term.Event{Type: term.EventKey, Ch: ch})
				}
			}
		})
	}
}

// TestCommandHandlerPartialCompletionState pins the prompt state that a
// partial candidate must leave behind. Accepting one may not append the
// argument separator, commit the token into commandAndArgs, or advance
// the completion mode, because any of those resets the next completion
// back to the completer's root instead of descending.
func TestCommandHandlerPartialCompletionState(t *testing.T) {
	cfg := testDefaultConfig()
	cfg.ShowManual = false
	cfg.Sync = true
	dirs := testDirTree(t)

	// mode counts the command plus every committed argument, so the
	// command alone leaves it at 1 and a committed argument at 2.
	const (
		argInProgress commandPromptMode = 1
		argCommitted  commandPromptMode = 2
	)

	tsuite := []struct {
		desc        string
		sequence    string
		completeCmd func() (func(context.Context, []string) (iterator.Iterator[string], string, error), func(*testing.T))
		wantBuf     string
		wantArgs    []string
		wantMode    commandPromptMode
	}{
		{"descent leaves the argument in progress",
			"a<tab>", completeDirsUnder(dirs),
			"workspaceopen alpha/", []string{"workspaceopen"}, argInProgress},
		{"a second descent advances the same argument",
			"a<tab>b<tab>", completeDirsUnder(dirs),
			"workspaceopen alpha/beta/", []string{"workspaceopen"}, argInProgress},
		{"a third descent advances the same argument",
			"a<tab>b<tab>g<tab>", completeDirsUnder(dirs),
			"workspaceopen alpha/beta/gamma/", []string{"workspaceopen"}, argInProgress},
		{"a quoted candidate carries the marker inside the quotes",
			"m<tab>", completeDirsUnder(dirs),
			"workspaceopen 'my dir/'", []string{"workspaceopen"}, argInProgress},
		{"typing after a descent extends the same argument",
			"a<tab>d", completeDirsUnder(dirs),
			"workspaceopen alpha/d", []string{"workspaceopen"}, argInProgress},
		{"backspace after a descent edits the same argument",
			"a<tab><backspace>", completeDirsUnder(dirs),
			"workspaceopen alpha", []string{"workspaceopen"}, argInProgress},
		{"space after a descent terminates the argument",
			"a<tab><space>", completeDirsUnder(dirs),
			"workspaceopen alpha/ ",
			[]string{"workspaceopen", "alpha/"}, argCommitted},
		{"a terminal candidate still terminates the argument",
			"p<tab>", completeWith("/prev/ws", "alpha/"),
			"workspaceopen /prev/ws ",
			[]string{"workspaceopen", "/prev/ws"}, argCommitted},
		{"a tab with no matches commits the typed token",
			"zzz<tab>", completeDirsUnder(dirs),
			"workspaceopen zzz", []string{"workspaceopen", "zzz"}, argCommitted},
	}

	for _, tcase := range tsuite {
		t.Run(tcase.desc, func(t *testing.T) {
			completeFn, cleanupComplete := tcase.completeCmd()
			defer cleanupComplete(t)

			b := NewPrompt(
				storagestub.NewInMemoryService(), FuncCompleter(completeFn),
				FuncDispatcher(nopDispatchFn), term.NopInterrupter(),
				testNoManualCommands([]string{"workspaceopen"}), cfg,
			)
			defer b.Close()

			keys, err := term.ParseKeys("workspaceopen<space>" + tcase.sequence)
			require.NoError(t, err)
			for _, key := range keys {
				testCommandHandler{b}.Handle(term.Event{
					Type: term.EventKey,
					Ch:   key.Ch,
					Mod:  key.Mod,
					Key:  key.Key,
				})
			}

			assert.Equal(t, tcase.wantBuf, b.buf.String())
			assert.Equal(t, tcase.wantArgs, b.commandAndArgs)
			assert.Equal(t, tcase.wantMode, b.mode)
		})
	}
}

func nopDispatchFn(string, ...string) bool { return false }

func TestCommandHandlerBackspacePreservesRemoteWorkspaceURI(t *testing.T) {
	cfg := testDefaultConfig()
	cfg.ShowManual = false
	cfg.HistoryCycleKey = term.KeyComb{Ch: '@'}
	cfg.Sync = true

	const remote = "ssh://10.0.0.6/~/src/rune"
	dispatchFn, cleanup := expectDispatch("workspaceopen", remote)()
	defer cleanup(t)

	b := NewPrompt(
		storagestub.NewInMemoryService(),
		DirsCompleter(newFSReader(t.TempDir())),
		FuncDispatcher(dispatchFn), term.NopInterrupter(),
		testNoManualCommands([]string{"workspaceopen"}), cfg,
	)
	defer b.Close()

	keys, err := term.ParseKeys(
		"workspaceopen<space>" + remote + "<space><backspace><enter>")
	require.NoError(t, err)
	for _, key := range keys {
		testCommandHandler{b}.Handle(term.Event{
			Type: term.EventKey,
			Ch:   key.Ch,
			Mod:  key.Mod,
			Key:  key.Key,
		})
	}
}

// newLineEditingPrompt builds a prompt with a completer that never
// suggests anything, so every assertion below is about the literal
// text the user typed rather than about completion.
func newLineEditingPrompt(t *testing.T, commands []string) *Prompt {
	t.Helper()
	cfg := testDefaultConfig()
	cfg.ShowManual = false
	cfg.Sync = true

	completeFn, cleanupComplete := nopComplete()
	t.Cleanup(func() { cleanupComplete(t) })
	dispatchFn, cleanupDispatch := nopDispatch()
	t.Cleanup(func() { cleanupDispatch(t) })

	b := NewPrompt(
		storagestub.NewInMemoryService(),
		FuncCompleter(completeFn), FuncDispatcher(dispatchFn),
		term.NopInterrupter(), testNoManualCommands(commands), cfg,
	)
	t.Cleanup(func() { _ = b.Close() })
	return b
}

func feedKeys(t *testing.T, b *Prompt, sequence string) (quit, handled bool) {
	t.Helper()
	keys, err := term.ParseKeys(sequence)
	require.NoError(t, err)
	h := testCommandHandler{b}
	for _, key := range keys {
		q, ok := h.Handle(term.Event{
			Type: term.EventKey,
			Ch:   key.Ch,
			Mod:  key.Mod,
			Key:  key.Key,
		})
		quit = quit || q
		handled = ok
	}
	return
}

// TestCommandPromptLineEditing covers the shell-style editing keys
// (<c-w>, <c-h>, <c-backspace>, <a-backspace> and <c-u>) alongside
// plain <backspace>, which they share a deletion primitive with. The
// assertions pin all four pieces of prompt state at once because word
// deletion can walk backwards across the argument boundary and unwind
// the completion stack.
func TestCommandPromptLineEditing(t *testing.T) {
	tsuite := []struct {
		desc string
		// commands seeds the command list; the first token typed in a
		// sequence is completed against it on <space>.
		commands []string
		// width resizes the prompt when non-zero so that sequences
		// longer than the prompt wrap on screen.
		width     int
		sequence  string
		wantBuf   string
		wantToken string
		wantArgs  []string
		wantMode  commandPromptMode
		wantQuit  bool
	}{
		// empty prompt
		{desc: "c-w on an empty prompt is a no-op",
			commands: []string{"edit"}, sequence: "<c-w>",
			wantBuf: "", wantToken: "", wantArgs: []string{}},
		{desc: "c-u on an empty prompt is a no-op",
			commands: []string{"edit"}, sequence: "<c-u>",
			wantBuf: "", wantToken: "", wantArgs: []string{}},
		{desc: "c-w on an empty prompt with no commands is a no-op",
			commands: nil, sequence: "<c-w>",
			wantBuf: "", wantToken: "", wantArgs: []string{}},
		{desc: "backspace on an empty prompt still closes the prompt",
			commands: []string{"edit"}, sequence: "<backspace>",
			wantBuf: "", wantToken: "", wantArgs: []string{}, wantQuit: true},
		{desc: "c-w draining the line does not close the prompt",
			commands: []string{"edit"}, sequence: "edi<c-w><c-w>",
			wantBuf: "", wantToken: "", wantArgs: []string{}},

		// command mode
		{desc: "c-w deletes a partially typed command",
			commands: []string{"edit"}, sequence: "edi<c-w>",
			wantBuf: "", wantToken: "", wantArgs: []string{}},
		{desc: "c-u deletes a partially typed command",
			commands: []string{"edit"}, sequence: "edi<c-u>",
			wantBuf: "", wantToken: "", wantArgs: []string{}},
		{desc: "backspace deletes one cell of a partially typed command",
			commands: []string{"edit"}, sequence: "edi<backspace>",
			wantBuf: "ed", wantToken: "ed", wantArgs: []string{}},

		// argument mode
		{desc: "c-w deletes the argument and stops at the separator",
			commands: []string{"edit"}, sequence: "edit<space>foo<c-w>",
			wantBuf: "edit ", wantToken: "", wantArgs: []string{"edit"}, wantMode: 1},
		{desc: "backspace deletes one cell of the argument",
			commands: []string{"edit"}, sequence: "edit<space>foo<backspace>",
			wantBuf: "edit fo", wantToken: "fo", wantArgs: []string{"edit"}, wantMode: 1},
		{desc: "c-w crossing the separator unwinds to command mode",
			commands: []string{"edit"}, sequence: "edit<space>foo<c-w><c-w>",
			wantBuf: "", wantToken: "", wantArgs: []string{}},
		{desc: "c-w unwinds one completed argument at a time",
			commands: []string{"edit"}, sequence: "edit<space>one<space>two<c-w>",
			wantBuf: "edit one ", wantToken: "", wantArgs: []string{"edit", "one"}, wantMode: 2},
		{desc: "c-w deletes the separator together with the preceding argument",
			commands: []string{"edit"}, sequence: "edit<space>one<space>two<c-w><c-w>",
			wantBuf: "edit ", wantToken: "", wantArgs: []string{"edit"}, wantMode: 1},
		{desc: "c-u clears every completed argument",
			commands: []string{"edit"}, sequence: "edit<space>one<space>two<c-u>",
			wantBuf: "", wantToken: "", wantArgs: []string{}},
		{desc: "c-u clears a half-typed argument",
			commands: []string{"edit"}, sequence: "edit<space>src/main<c-u>",
			wantBuf: "", wantToken: "", wantArgs: []string{}},

		// path components
		{desc: "c-w walks back one path component",
			commands: []string{"edit"}, sequence: "edit<space>a/bb/ccc<c-w>",
			wantBuf: "edit a/bb/", wantToken: "a/bb/", wantArgs: []string{"edit"}, wantMode: 1},
		{desc: "repeated c-w walks back each path component",
			commands: []string{"edit"}, sequence: "edit<space>a/bb/ccc<c-w><c-w>",
			wantBuf: "edit a/", wantToken: "a/", wantArgs: []string{"edit"}, wantMode: 1},
		{desc: "c-w on a trailing slash removes it with the component before it",
			commands: []string{"edit"}, sequence: "edit<space>a/bb/<c-w>",
			wantBuf: "edit a/", wantToken: "a/", wantArgs: []string{"edit"}, wantMode: 1},
		{desc: "c-w drains a whole path one component per press",
			commands: []string{"edit"}, sequence: "edit<space>a/bb/ccc<c-w><c-w><c-w>",
			wantBuf: "edit ", wantToken: "", wantArgs: []string{"edit"}, wantMode: 1},
		{desc: "c-w keeps a scheme prefix intact until its own press",
			commands:  []string{"workspaceopen"},
			sequence:  "workspaceopen<space>ssh://host/src/rune<c-w>",
			wantBuf:   "workspaceopen ssh://host/src/",
			wantToken: "ssh://host/src/",
			wantArgs:  []string{"workspaceopen"}, wantMode: 1},

		// wrapped input
		{desc: "c-w operates on logical cells when the line wraps on screen",
			commands: []string{"edit"}, width: 12,
			sequence: "edit<space>aaaa/bbbb/cccc<c-w>",
			wantBuf:  "edit aaaa/bbbb/", wantToken: "aaaa/bbbb/",
			wantArgs: []string{"edit"}, wantMode: 1},

		// quoting
		{desc: "c-w stops at an escaped space inside one argument",
			commands: []string{"edit"}, sequence: `edit<space>my\\<space>file<c-w>`,
			wantBuf: `edit my\ `, wantToken: `my\ `,
			wantArgs: []string{"edit"}, wantMode: 1},

		// wide and non-alphanumeric runes
		{desc: "c-w deletes an ascii extension without splitting wide runes",
			commands: []string{"edit"}, sequence: "edit<space>世界.txt<c-w>",
			wantBuf: "edit 世界.", wantToken: "世界.",
			wantArgs: []string{"edit"}, wantMode: 1},
		{desc: "c-w treats wide letters as word runes",
			commands: []string{"edit"}, sequence: "edit<space>世界.txt<c-w><c-w>",
			wantBuf: "edit ", wantToken: "", wantArgs: []string{"edit"}, wantMode: 1},
		{desc: "c-w deletes a precomposed accent with its word",
			commands: []string{"edit"}, sequence: "edit<space>a/café<c-w>",
			wantBuf: "edit a/", wantToken: "a/", wantArgs: []string{"edit"}, wantMode: 1},
		{desc: "c-w deletes a decomposed accent with its word",
			commands: []string{"edit"}, sequence: "edit<space>a/cafe\u0301<c-w>",
			wantBuf: "edit a/", wantToken: "a/", wantArgs: []string{"edit"}, wantMode: 1},
		{desc: "c-w treats a zwj emoji cluster as one separator",
			commands: []string{"edit"}, sequence: "edit<space>ab\U0001F468\u200D\U0001F4BB<c-w>",
			wantBuf: "edit ", wantToken: "", wantArgs: []string{"edit"}, wantMode: 1},
		{desc: "c-w skips a trailing emoji before deleting the word",
			commands: []string{"edit"}, sequence: "edit<space>ab🙂<c-w>",
			wantBuf: "edit ", wantToken: "", wantArgs: []string{"edit"}, wantMode: 1},
		{desc: "c-w on only separators keeps deleting into the previous word",
			commands: []string{"edit"}, sequence: "edit<space>🙂<c-w>",
			wantBuf: "", wantToken: "", wantArgs: []string{}},
		{desc: "c-w keeps digits and underscores in the same word",
			commands: []string{"edit"}, sequence: "edit<space>a/my_file2<c-w>",
			wantBuf: "edit a/", wantToken: "a/", wantArgs: []string{"edit"}, wantMode: 1},

		// alternate bindings
		{desc: "alt-backspace deletes a word",
			commands: []string{"edit"}, sequence: "edit<space>a/bb<a-backspace>",
			wantBuf: "edit a/", wantToken: "a/", wantArgs: []string{"edit"}, wantMode: 1},
		{desc: "ctrl-backspace deletes a word",
			commands: []string{"edit"}, sequence: "edit<space>a/bb<c-backspace>",
			wantBuf: "edit a/", wantToken: "a/", wantArgs: []string{"edit"}, wantMode: 1},
		{desc: "ctrl-h deletes a word for terminals that collapse ctrl-backspace",
			commands: []string{"edit"}, sequence: "edit<space>a/bb<c-h>",
			wantBuf: "edit a/", wantToken: "a/", wantArgs: []string{"edit"}, wantMode: 1},

		// retyping after a deletion
		{desc: "typing resumes on the argument left behind by c-w",
			commands: []string{"edit"}, sequence: "edit<space>a/bb/ccc<c-w>dd",
			wantBuf: "edit a/bb/dd", wantToken: "a/bb/dd",
			wantArgs: []string{"edit"}, wantMode: 1},
		{desc: "typing resumes on the argument restored by c-w unwinding",
			commands: []string{"edit"}, sequence: "edit<space>one<space>two<c-w><c-w>x",
			wantBuf: "edit x", wantToken: "x", wantArgs: []string{"edit"}, wantMode: 1},
		{desc: "typing resumes in command mode after c-u",
			commands: []string{"edit"}, sequence: "edit<space>one<c-u>ed",
			wantBuf: "ed", wantToken: "ed", wantArgs: []string{}},
	}

	for _, tcase := range tsuite {
		t.Run(tcase.desc, func(t *testing.T) {
			b := newLineEditingPrompt(t, tcase.commands)
			if tcase.width != 0 {
				b.Resize(tcase.width, 10)
			}

			quit, handled := feedKeys(t, b, tcase.sequence)

			assert.True(t, handled, "last key must not leak to the ide")
			assert.Equal(t, tcase.wantQuit, quit, "quit")
			assert.Equal(t, tcase.wantBuf, b.buf.String(), "prompt buffer")
			assert.Equal(t, tcase.wantToken, b.list.Buffer().String(), "completion token")
			assert.Equal(t, tcase.wantArgs,
				append([]string{}, b.commandAndArgs...), "completed args")
			assert.Equal(t, tcase.wantMode, b.mode, "prompt mode")
		})
	}
}

// TestCommandPromptLineEditingDispatch asserts that a line repaired
// with the editing keys dispatches the arguments the prompt displays,
// i.e. that the buffer and the completion stack stay in agreement.
func TestCommandPromptLineEditingDispatch(t *testing.T) {
	tsuite := []struct {
		desc     string
		commands []string
		sequence string
		dispatch func() (func(string, ...string) bool, func(*testing.T))
	}{
		{"retyped path component after c-w",
			[]string{"edit"}, "edit<space>src/mian<c-w>main.go<enter>",
			expectDispatch("edit", "src/main.go")},
		{"argument replaced after c-w unwinding",
			[]string{"edit"}, "edit<space>one<space>two<c-w><c-w>three<enter>",
			expectDispatch("edit", "three")},
		{"command retyped after c-u",
			[]string{"edit", "quit"}, "edit<space>one<c-u>quit<enter>",
			expectDispatch("quit")},
		{"alt-backspace correction",
			[]string{"edit"}, "edit<space>a/bb<a-backspace>cc<enter>",
			expectDispatch("edit", "a/cc")},
	}

	for _, tcase := range tsuite {
		t.Run(tcase.desc, func(t *testing.T) {
			cfg := testDefaultConfig()
			cfg.ShowManual = false
			cfg.Sync = true

			completeFn, cleanupComplete := nopComplete()
			defer cleanupComplete(t)
			dispatchFn, cleanupDispatch := tcase.dispatch()
			defer cleanupDispatch(t)

			b := NewPrompt(
				storagestub.NewInMemoryService(),
				FuncCompleter(completeFn), FuncDispatcher(dispatchFn),
				term.NopInterrupter(), testNoManualCommands(tcase.commands), cfg,
			)
			defer b.Close()

			feedKeys(t, b, tcase.sequence)
		})
	}
}

// TestCommandPromptLineEditingInEditMode pins that the editing keys
// are only the prompt's own fallback: while the modal edit session is
// active every one of them belongs to the spawned editor.
func TestCommandPromptLineEditingInEditMode(t *testing.T) {
	var seen []term.Event
	cfg := testDefaultConfig()
	cfg.ShowManual = false
	cfg.Sync = true
	cfg.Editor = stubEditorImpl{seen: &seen}

	completeFn, cleanupComplete := nopComplete()
	defer cleanupComplete(t)
	dispatchFn, cleanupDispatch := nopDispatch()
	defer cleanupDispatch(t)

	b := NewPrompt(
		storagestub.NewInMemoryService(),
		FuncCompleter(completeFn), FuncDispatcher(dispatchFn),
		term.NopInterrupter(), testNoManualCommands([]string{"edit"}), cfg,
	)
	defer b.Close()

	feedKeys(t, b, "edit<shift-esc><c-w><c-u><a-backspace><c-backspace>")

	require.Len(t, seen, 4)
	assert.Equal(t, []term.Event{
		{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'w'},
		{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'u'},
		{Type: term.EventKey, Mod: term.ModAlt, Key: term.KeyBackspace},
		{Type: term.EventKey, Mod: term.ModCtrl, Key: term.KeyBackspace},
	}, seen)
}

// TestCommandPromptWordDeleteKeepsHistoryEntries pins the one place
// where word deletion deliberately diverges from <backspace>: while
// scrolling history-backed argument suggestions, <backspace> removes
// the focused entry from history, whereas <c-w> must only edit text.
func TestCommandPromptWordDeleteKeepsHistoryEntries(t *testing.T) {
	cfg := testDefaultConfig()
	cfg.ShowManual = false
	cfg.Sync = true

	completeFn, cleanupComplete := nopComplete()
	defer cleanupComplete(t)

	b := NewPrompt(
		storagestub.NewInMemoryService(),
		FuncCompleter(completeFn),
		FuncDispatcher(func(string, ...string) bool { return true }),
		term.NopInterrupter(), testNoManualCommands([]string{"edit"}), cfg,
	)
	defer b.Close()

	feedKeys(t, b, "edit<space>src/main.go<enter>")
	require.Equal(t, []string{"edit src/main.go"}, b.history.Slice())

	// re-enter argument mode so the history-backed suggestion is
	// focused, which is what arms the removal path.
	armHistoryFocus := func() {
		feedKeys(t, b, "edit<space><down>")
		require.True(t, b.completingWithHistory.Load())
		require.True(t, b.userScrolling)
	}

	armHistoryFocus()
	feedKeys(t, b, "<c-w>")
	assert.Equal(t, []string{"edit src/main.go"}, b.history.Slice())

	armHistoryFocus()
	feedKeys(t, b, "<backspace>")
	assert.Empty(t, b.history.Slice())
}

// TestCommandHandlerCancelsCompletionBeforeDispatch ensures that the
// active completion's context is canceled before the dispatcher runs
// when the user presses Enter. This prevents an in-flight completion
// (e.g. a recursive walkdir traversal) from continuing to fight for
// resources while the synchronous dispatcher does its work.
//
// The test runs in async mode (cfg.Sync = false) so that the
// completer's iterator can stay in flight while the dispatcher runs;
// in sync mode the iterator is always drained before the dispatcher
// is invoked, which masks the regression.
func TestCommandHandlerCancelsCompletionBeforeDispatch(t *testing.T) {
	storage := storagestub.NewInMemoryService()
	cfg := testDefaultConfig()
	cfg.ShowManual = false
	cfg.Sync = false

	dispatchCalled := make(chan struct{})
	var (
		mu               sync.Mutex
		lastCompCtx      context.Context
		ctxErrAtDispatch error
	)

	completer := FuncCompleter(func(
		ctx context.Context, _ []string,
	) (iterator.Iterator[string], string, error) {
		mu.Lock()
		lastCompCtx = ctx
		mu.Unlock()
		return &fakeBlockingIter{ctx: ctx}, "", nil
	})
	dispatcher := FuncDispatcher(func(_ string, _ ...string) bool {
		mu.Lock()
		ctxErrAtDispatch = lastCompCtx.Err()
		mu.Unlock()
		close(dispatchCalled)
		return false
	})

	b := NewPrompt(
		storage, completer, dispatcher,
		term.NopInterrupter(),
		testNoManualCommands([]string{"workspaceopen"}),
		cfg,
	)
	defer b.Close()

	for _, ch := range "workspaceopen /tmp" {
		b.Handle(term.Event{Type: term.EventKey, Ch: ch})
	}
	b.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})

	select {
	case <-dispatchCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("dispatcher was not called")
	}

	mu.Lock()
	defer mu.Unlock()
	require.NotNil(t, lastCompCtx, "completer must have been invoked")
	assert.ErrorIs(t, ctxErrAtDispatch, context.Canceled,
		"completion context must be canceled before dispatcher runs")
}

// TestCommandHandlerIncArgsCompleteModeDoesNotDeadlock verifies that
// Tab in command mode completes without hanging when the manual is
// primed and the async completion goroutine is still in flight, even
// when buildManualComponent ends up calling h.list.Wait().
func TestCommandHandlerIncArgsCompleteModeDoesNotDeadlock(t *testing.T) {
	storage := storagestub.NewInMemoryService()
	cfg := testDefaultConfig()
	cfg.ShowManual = false
	cfg.ShowProgressHint = true
	cfg.Sync = false

	iterDelay := 50 * time.Millisecond
	completer := FuncCompleter(func(
		ctx context.Context, _ []string,
	) (iterator.Iterator[string], string, error) {
		return &delayedIter{ctx: ctx, delay: iterDelay, value: "opt"}, "", nil
	})

	cmd := Manual{Name: "kotomichi", Summary: "doc"}
	b := NewPrompt(
		storage,
		completer,
		FuncDispatcher(func(string, ...string) bool { return true }),
		term.NopInterrupter(),
		[]Manual{cmd},
		cfg,
	)
	defer b.Close()

	// Prime manualComponent so the defer in incArgsCompleteMode actually
	// reaches newManualComponent → manualForCommandInFocus → list.Wait().
	b.setManualComponent(b.buildManualComponent(""))

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Tab in command mode with empty buffer triggers
		// incArgsCompleteMode; buildManualComponent then takes the
		// first branch (cmdAndArgs len 0) and calls h.list.Wait().
		b.Handle(term.Event{Type: term.EventKey, Key: term.KeyTab})
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Handle deadlocked")
	}
}

// delayedIter delivers a single value after a delay, then signals exhaustion.
type delayedIter struct {
	ctx       context.Context
	delay     time.Duration
	value     string
	delivered bool
}

func (d *delayedIter) Next(ctx context.Context) (string, bool) {
	if d.delivered {
		return "", false
	}
	d.delivered = true
	select {
	case <-time.After(d.delay):
	case <-ctx.Done():
		return "", false
	}
	return d.value, true
}

func (d *delayedIter) Close() error { return nil }
func (d *delayedIter) Err() error   { return d.ctx.Err() }

// TestCommandHandlerPreviewCancelCallsBackIntoPrompt guards against a
// self-deadlock where the preview-cancel callback synchronously re-enters
// the Prompt (e.g. theme preview cancel triggers a GUI resize that calls
// Prompt.Dimensions).
func TestCommandHandlerPreviewCancelCallsBackIntoPrompt(t *testing.T) {
	storage := storagestub.NewInMemoryService()
	cfg := testDefaultConfig()
	cfg.ShowManual = false
	cfg.Sync = true

	var b *Prompt
	cancelInvoked := make(chan struct{}, 1)
	previewFn := func(_ string, _ ...string) (component.Responsive, func(), bool) {
		return nil, func() {
			// Mirror the production bootstrap_handler theme preview cancel
			// which ends up re-entering Prompt.Dimensions through the GUI
			// resize pipeline.
			b.Dimensions()
			select {
			case cancelInvoked <- struct{}{}:
			default:
			}
		}, true
	}

	completeFn, cleanupComplete := completeWith("arg1", "arg2")()
	defer cleanupComplete(t)

	cmd := Manual{Name: "kotomichi"}
	b = NewPrompt(
		storage,
		FuncCompleter(completeFn),
		FuncDispatcherWithPreview(func(string, ...string) bool { return true }, previewFn),
		term.NopInterrupter(),
		[]Manual{cmd},
		cfg,
	)
	defer b.Close()

	for _, ch := range "kotomichi " {
		b.Handle(term.Event{Type: term.EventKey, Ch: ch})
	}
	b.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowDown})
	b.Wait()

	done := make(chan struct{})
	go func() {
		defer close(done)
		b.Handle(term.Event{Type: term.EventKey, Key: term.KeyBackspace})
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Handle deadlocked while running preview cancel callback")
	}

	select {
	case <-cancelInvoked:
	default:
		t.Fatal("preview cancel was not invoked")
	}
}

// fakeBlockingIter is an iterator.Iterator[string] that emits a
// single value and then blocks on Next until its context is canceled.
type fakeBlockingIter struct {
	ctx       context.Context
	delivered bool
}

func (f *fakeBlockingIter) Next(ctx context.Context) (string, bool) {
	if !f.delivered {
		f.delivered = true
		return "opt", true
	}
	select {
	case <-ctx.Done():
	case <-f.ctx.Done():
	}
	return "", false
}

func (f *fakeBlockingIter) Close() error { return nil }

func (f *fakeBlockingIter) Err() error {
	if err := f.ctx.Err(); err != nil {
		return err
	}
	return nil
}

func TestCommandHandlerEditMode(t *testing.T) {
	feedKeys := func(t *testing.T, h interface {
		Handle(term.Event) (bool, bool)
	}, seq string) {
		t.Helper()
		keys, err := term.ParseKeys(seq)
		require.NoError(t, err)
		for _, k := range keys {
			h.Handle(term.Event{Type: term.EventKey, Ch: k.Ch, Mod: k.Mod, Key: k.Key})
		}
	}

	// stubEditor returns an Editor that produces a minimal tui.Handler
	// appending typed runes to the end of the buffer. It also records
	// every event it receives so tests can assert that the prompt is
	// correctly delegating input.
	stubEditor := func(seen *[]term.Event) Editor {
		return stubEditorImpl{seen: seen}
	}

	t.Run("edits buffer and replays through handler on exit", func(t *testing.T) {
		storage := storagestub.NewInMemoryService()
		cfg := testDefaultConfig()
		cfg.ShowManual = false
		cfg.HistoryCycleKey = term.KeyComb{Ch: '@'}
		cfg.Sync = true
		cfg.Editor = stubEditor(nil)

		var dispatched [][]string
		dispatchFn := func(cmd string, args ...string) bool {
			out := append([]string{cmd}, args...)
			dispatched = append(dispatched, out)
			return true
		}

		var completeCalls int
		completeFn := func(ctx context.Context, args []string) (iterator.Iterator[string], string, error) {
			completeCalls++
			return iterator.FromSlice([]string{"myArg"}), "", nil
		}

		cmds := testNoManualCommands([]string{"rori", "lorelai"})
		interrupter := term.NopInterrupter()
		b := NewPrompt(
			storage, FuncCompleter(completeFn), FuncDispatcher(dispatchFn),
			interrupter, cmds, cfg,
		)
		defer b.Close()

		h := testCommandHandler{b}

		// Type "rori myArg " into the prompt (auto-complete via tab),
		// then enter edit mode, append a literal "X" and dispatch.
		feedKeys(t, h, "rori<space>my<tab><shift-esc>X<enter>")

		require.Equal(t, [][]string{{"rori", "myArg", "X"}}, dispatched)
		assert.Greater(t, completeCalls, 0)
	})

	t.Run("delegates events to edit handler and freezes completion", func(t *testing.T) {
		storage := storagestub.NewInMemoryService()
		cfg := testDefaultConfig()
		cfg.ShowManual = false
		cfg.HistoryCycleKey = term.KeyComb{Ch: '@'}
		cfg.Sync = true
		var seen []term.Event
		cfg.Editor = stubEditor(&seen)

		dispatchFn := func(cmd string, args ...string) bool {
			return true
		}

		var completeCalls int
		completeFn := func(ctx context.Context, args []string) (iterator.Iterator[string], string, error) {
			completeCalls++
			return iterator.FromSlice[string](nil), "", nil
		}

		cmds := testNoManualCommands([]string{"rori", "lorelai"})
		interrupter := term.NopInterrupter()
		b := NewPrompt(
			storage, FuncCompleter(completeFn), FuncDispatcher(dispatchFn),
			interrupter, cmds, cfg,
		)
		defer b.Close()

		h := testCommandHandler{b}

		// Prime with "rori " so completer runs at least once.
		feedKeys(t, h, "rori<space>")

		baseline := completeCalls

		// In edit mode every event is forwarded to the spawned
		// handler and completions stay frozen.
		feedKeys(t, h, "<shift-esc>abc<left><backspace>")

		assert.Equal(t, baseline, completeCalls,
			"completer must not be called while in edit mode")

		// The stub editor appends every printable rune, ignores arrow
		// keys (they are still forwarded), and removes the last rune
		// on backspace. Starting from "rori ":
		//   abc          -> "rori abc"
		//   <left>       -> (no-op; recorded)
		//   <backspace>  -> "rori ab"
		assert.Equal(t, "rori ab", b.buf.String())
		// the edit handler must have received every key after entering
		// edit mode (a, b, c, left, backspace) — five events.
		assert.Len(t, seen, 5)
	})

	t.Run("panics when Editor is nil", func(t *testing.T) {
		storage := storagestub.NewInMemoryService()
		cfg := testDefaultConfig()
		cfg.Editor = nil

		dispatchFn := func(cmd string, args ...string) bool { return true }
		completeFn := func(ctx context.Context, args []string) (iterator.Iterator[string], string, error) {
			return iterator.FromSlice[string](nil), "", nil
		}
		assert.Panics(t, func() {
			_ = NewPrompt(
				storage, FuncCompleter(completeFn), FuncDispatcher(dispatchFn),
				term.NopInterrupter(), nil, cfg,
			)
		})
	})

	t.Run("places editor cursor at end of buffer on entry", func(t *testing.T) {
		storage := storagestub.NewInMemoryService()
		cfg := testDefaultConfig()
		cfg.ShowManual = false
		cfg.HistoryCycleKey = term.KeyComb{Ch: '@'}
		cfg.Sync = true

		// Capture the spawned handler so we can inspect its cursor.
		var captured *stubEditHandler
		cfg.Editor = capturingEditor{captured: &captured}

		dispatchFn := func(cmd string, args ...string) bool { return true }
		completeFn := func(ctx context.Context, args []string) (iterator.Iterator[string], string, error) {
			return iterator.FromSlice[string](nil), "", nil
		}

		cmds := testNoManualCommands([]string{"rori"})
		b := NewPrompt(
			storage, FuncCompleter(completeFn), FuncDispatcher(dispatchFn),
			term.NopInterrupter(), cmds, cfg,
		)
		defer b.Close()

		feedKeys(t, testCommandHandler{b}, "rori<space>my<shift-esc>")
		require.NotNil(t, captured)
		// "rori my" -> 7 columns; cursor must land just after the y.
		assert.Equal(t, 7, captured.cursorX)
	})

	t.Run("forwards editor selection while edit mode is active", func(t *testing.T) {
		storage := storagestub.NewInMemoryService()
		cfg := testDefaultConfig()
		cfg.ShowManual = false
		cfg.HistoryCycleKey = term.KeyComb{Ch: '@'}
		cfg.Sync = true

		var captured *stubEditHandler
		cfg.Editor = capturingEditor{captured: &captured}

		dispatchFn := func(cmd string, args ...string) bool { return true }
		completeFn := func(ctx context.Context, args []string) (iterator.Iterator[string], string, error) {
			return iterator.FromSlice[string](nil), "", nil
		}

		cmds := testNoManualCommands([]string{"rori"})
		b := NewPrompt(
			storage, FuncCompleter(completeFn), FuncDispatcher(dispatchFn),
			term.NopInterrupter(), cmds, cfg,
		)
		defer b.Close()

		// Outside edit mode the prompt has no selection to surface.
		sel, ok := b.Selection()
		assert.False(t, ok)
		assert.Empty(t, sel)

		// Enter edit mode and stage a selection on the spawned editor.
		feedKeys(t, testCommandHandler{b}, "rori<space>my<shift-esc>")
		require.NotNil(t, captured)
		captured.selection = "rori my"

		// While in edit mode the prompt must report the editor's
		// selection so the runtime can render the highlight.
		sel, ok = b.Selection()
		assert.True(t, ok, "edit-mode selection must be visible to the runtime")
		assert.Equal(t, "rori my", sel)
	})

	t.Run("paints selection highlight in wrapped prompt geometry", func(t *testing.T) {
		// The prompt renders the buffer through a responsive
		// component that strips per-cell attributes, and bypasses
		// the editor's own DrawLocations selection-rendering
		// pipeline. Without an explicit overlay the highlight is
		// invisible; this test pins the overlay to the wrap layout.
		storage := storagestub.NewInMemoryService()
		cfg := testDefaultConfig()
		cfg.ShowManual = false
		cfg.HistoryCycleKey = term.KeyComb{Ch: '@'}
		cfg.Sync = true

		var captured *stubEditHandler
		cfg.Editor = capturingEditor{captured: &captured}

		dispatchFn := func(cmd string, args ...string) bool { return true }
		completeFn := func(ctx context.Context, args []string) (iterator.Iterator[string], string, error) {
			return iterator.FromSlice[string](nil), "", nil
		}

		cmds := testNoManualCommands([]string{"rori"})
		b := NewPrompt(
			storage, FuncCompleter(completeFn), FuncDispatcher(dispatchFn),
			term.NopInterrupter(), cmds, cfg,
		)
		defer b.Close()

		// Type 25 ones so the buffer wraps to 2 visual rows at
		// width 20 (leftWidgetWidth = 20-3 animationWidth = 17).
		// Then enter edit mode.
		feedKeys(t, testCommandHandler{b},
			strings.Repeat("1", 25)+"<shift-esc>")
		require.NotNil(t, captured)

		// The half-open range [15, 20) crosses the wrap boundary:
		//   buf 15 -> visual (15, 0)
		//   buf 16 -> visual (16, 0)
		//   buf 17 -> visual (0, 1)
		//   buf 18 -> visual (1, 1)
		//   buf 19 -> visual (2, 1)
		captured.hasSelection = true
		captured.selectionFrom = term.Coordinates{X: 15}
		captured.selectionTo = term.Coordinates{X: 20}

		const width, height = 20, 5
		b.Resize(width, height)
		w := term.NewStringWriter(width, height)
		b.Draw(w)

		// The overlay paints reverse-video on each visible cell
		// covered by the selection. Map back (X, Y) → cell index
		// in the writer's buffer and assert AttrReverse is set.
		cells := w.Cells()
		cellAt := func(x, y int) term.Cell { return cells[y*width+x] }

		// At width 20 the prompt's responsive component renders at
		// the top of the writer (y=0..bufHeight-1). 17-cell-wide
		// rows wrap "11111...(25)" to two rows of 17 / 8 cells.
		want := []term.Coordinates{
			{X: 15, Y: 0},
			{X: 16, Y: 0},
			{X: 0, Y: 1},
			{X: 1, Y: 1},
			{X: 2, Y: 1},
		}
		for _, pos := range want {
			c := cellAt(pos.X, pos.Y)
			assert.NotZerof(t, c.Attrs&term.AttrReverse,
				"selection cell at %v must have AttrReverse set", pos)
		}

		// Cells outside the selection must NOT be reverse-video.
		for _, pos := range []term.Coordinates{
			{X: 14, Y: 0},
			{X: 3, Y: 1},
		} {
			c := cellAt(pos.X, pos.Y)
			assert.Zerof(t, c.Attrs&term.AttrReverse,
				"non-selection cell at %v must not have AttrReverse set", pos)
		}
	})

	t.Run("ctrl-c exits edit mode without forwarding to editor", func(t *testing.T) {
		storage := storagestub.NewInMemoryService()
		cfg := testDefaultConfig()
		cfg.ShowManual = false
		cfg.HistoryCycleKey = term.KeyComb{Ch: '@'}
		cfg.Sync = true
		var seen []term.Event
		cfg.Editor = stubEditor(&seen)

		dispatchFn := func(cmd string, args ...string) bool { return true }
		completeFn := func(ctx context.Context, args []string) (iterator.Iterator[string], string, error) {
			return iterator.FromSlice[string](nil), "", nil
		}

		cmds := testNoManualCommands([]string{"rori"})
		b := NewPrompt(
			storage, FuncCompleter(completeFn), FuncDispatcher(dispatchFn),
			term.NopInterrupter(), cmds, cfg,
		)
		defer b.Close()

		h := testCommandHandler{b}
		feedKeys(t, h, "rori<shift-esc>X<ctrl-c>")
		// editor must have received only "X" — ctrl-c is consumed
		// upstream and the prompt drops back to command mode.
		require.Len(t, seen, 1)
		assert.Equal(t, 'X', seen[0].Ch)

		// after ctrl-c we are back in command mode; further keys
		// flow through the regular handler again.
		feedKeys(t, h, "Y")
		assert.Len(t, seen, 1, "no further events after ctrl-c")
		assert.Equal(t, "roriXY", b.buf.String())
	})

	t.Run("tab exits edit mode without forwarding to editor", func(t *testing.T) {
		storage := storagestub.NewInMemoryService()
		cfg := testDefaultConfig()
		cfg.ShowManual = false
		cfg.HistoryCycleKey = term.KeyComb{Ch: '@'}
		cfg.Sync = true
		var seen []term.Event
		cfg.Editor = stubEditor(&seen)

		dispatchFn := func(cmd string, args ...string) bool { return true }
		completeFn := func(ctx context.Context, args []string) (iterator.Iterator[string], string, error) {
			return iterator.FromSlice[string](nil), "", nil
		}

		cmds := testNoManualCommands([]string{"rori"})
		b := NewPrompt(
			storage, FuncCompleter(completeFn), FuncDispatcher(dispatchFn),
			term.NopInterrupter(), cmds, cfg,
		)
		defer b.Close()

		feedKeys(t, testCommandHandler{b}, "rori<shift-esc>X<tab>")
		// editor must have received only "X" — tab is consumed
		// upstream so the editor never sees it (it would otherwise
		// insert a tab character or move into completion).
		require.Len(t, seen, 1)
		assert.Equal(t, 'X', seen[0].Ch)
	})
}

func TestCommandHandlerDraw(t *testing.T) {
	cfg := testDefaultConfig()
	cfg.ShowManual = false
	cfg.HistoryCycleKey = term.KeyComb{Ch: '@'}
	cfg.Sync = true
	dirs := testDirTree(t)

	tsuite := []struct {
		desc         string
		sequence     string
		commands     []string
		completeCmd  func() (func(ctx context.Context, args []string) (iterator.Iterator[string], string, error), func(*testing.T))
		dispatchCmd  func() (func(command string, args ...string) bool, func(*testing.T))
		expectedDraw string
	}{
		{"initializes no commands empty", "", nil, nopComplete, nopDispatch, `
▐                   
                    
                    
                    
                    
                    
                    
                    
                    
                    `},
		{"initializes no commands empty search yields 0", "a", nil, nopComplete, nopDispatch, `
a▐                  
                    
                    
                    
                    
                    
                    
                    
                    
                    `},
		{"initializes with some commands",
			"", []string{"lane", "lorelai", "rori"},
			nopComplete, nopDispatch, `
▐                   
lane                
lorelai             
rori                
                    
                    
                    
                    
                    
                    `},
		{"initializes with some commands search match",
			"l", []string{"lane", "lorelai", "rori"},
			nopComplete, nopDispatch, `
l▐                  
lane                
lorelai             
                    
                    
                    
                    
                    
                    
                    `},
		{"initializes with lots of commands", "", lotsOfCommands,
			nopComplete, nopDispatch, `
▐                   
0                   
1                   
2                   
3                   
4                   
5                   
6                   
7                   
8                   `},
		{"draw command NOT in list with no args",
			"1", []string{"lane", "lorelai", "rori"},
			nopComplete, nopDispatch, `
1▐                  
                    
                    
                    
                    
                    
                    
                    
                    
                    `},
		{"draw command in list with no args",
			"lo", []string{"lane", "lorelai", "rori"},
			nopComplete, nopDispatch, `
lo▐                 
lorelai             
                    
                    
                    
                    
                    
                    
                    
                    `},
		{"draw command NOT in list with args no auto-complete",
			"1 /tmp/a", []string{"lane", "lorelai", "rori"},
			nopComplete, nopDispatch, `
1 /tmp/a▐           
                    
                    
                    
                    
                    
                    
                    
                    
                    `},
		{"draw command in list with args no auto-complete",
			"lo /tmp/a", []string{"lane", "lorelai", "rori"},
			nopComplete, nopDispatch, `
lorelai /tmp/a▐     
                    
                    
                    
                    
                    
                    
                    
                    
                    `},
		{"draw command with args with auto-complete",
			"rori my", []string{"lane", "lorelai", "rori"},
			completeWith("myArg"), nopDispatch, `
rori my▐            
myArg               
                    
                    
                    
                    
                    
                    
                    
                    `},
		{"draw command with args with auto-complete, expand last arg",
			"rori ~my", []string{"lane", "lorelai", "rori"},
			completeWith("expanded/myArg"), nopDispatch, `
rori expanded/my▐   
expanded/myArg      
                    
                    
                    
                    
                    
                    
                    
                    `},
		{"draw fully typed command with args with auto-complete",
			"rori my", []string{"lane", "lorelai", "rori"},
			completeWith("myArg"), nopDispatch, `
rori my▐            
myArg               
                    
                    
                    
                    
                    
                    
                    
                    `},
		{"draw fully typed command with args with auto-complete with expanded last arg and delete in the middle",
			"rori ~^^^^^^^^^my", []string{"lane", "lorelai", "rori"},
			completeWith("myArg"), nopDispatch, `
rori my▐            
myArg               
                    
                    
                    
                    
                    
                    
                    
                    `},
		{"draw fully typed command with args with auto-complete and delete in the middle",
			"rori ^ my", []string{"lane", "lorelai", "rori"},
			completeWith("myArg"), nopDispatch, `
rori my▐            
myArg               
                    
                    
                    
                    
                    
                    
                    
                    `},
		{"draw command with args with auto-complete one last space",
			"ro my ", []string{"lane", "lorelai", "rori"},
			completeRespectively([]string{"myArg"}), nopDispatch, `
rori my ▐           
                    
                    
                    
                    
                    
                    
                    
                    
                    `},
		{"draw command with args with auto-complete one last space that's removed",
			"ro my ^", []string{"lane", "lorelai", "rori"},
			completeRespectively([]string{"myArg"}), nopDispatch, `
rori my▐            
myArg               
                    
                    
                    
                    
                    
                    
                    
                    `},
		{"draw command with args with auto-complete delete and re-typed all",
			"ro my ^^^^^^^^^^^ro my a", []string{"lane", "lorelai", "rori"},
			completeRespectively([]string{"myArg"}), nopDispatch, `
rori my a▐          
                    
                    
                    
                    
                    
                    
                    
                    
                    `},
		{"draw command with args with auto-complete delete and re-typed all with expanded last arg",
			"ro ~my ^^^^^^^^^^^^^^^^^^^^ro my", []string{"lane", "lorelai", "rori"},
			completeWith("myArg"), nopDispatch, `
rori my▐            
myArg               
                    
                    
                    
                    
                    
                    
                    
                    `},
		{"draw delete and re-type all with no autocomplete",
			"rori myArg ^^^^^^^^^^^rori myArg a", []string{"lane", "lorelai", "rori"},
			nopComplete, nopDispatch, `
rori myArg a▐       
                    
                    
                    
                    
                    
                    
                    
                    
                    `},
		{"draw command from history no autocomplete",
			"rori myArg>lorelai myArg>@ oArg", []string{"lane", "lorelai", "rori"},
			nopComplete, expectDispatch("lorelai", "myArg"), `
lorelai myArg oAr   
g▐                  
                    
                    
                    
                    
                    
                    
                    
                    `},
		{"draw from history with autocomplete",
			"lo my✌>ro my✌>@", []string{"lane", "lorelai", "rori"},
			completeWith("myArg"), expectDispatch("rori", "myArg"), `
rori myArg▐         
myArg               
                    
                    
                    
                    
                    
                    
                    
                    `},
		{"draw from history with autocomplete with expanded last arg",
			"lo my✌>ro ~my✌>@", []string{"lane", "lorelai", "rori"},
			completeWith("expanded/myArg"), expectDispatch("rori", "expanded/myArg"), `
rori expanded/myA   
rg▐                 
expanded/myArg      
                    
                    
                    
                    
                    
                    
                    `},
		{"draw delete after load from history with autocomplete",
			"lo my✌>ro my✌>@^^^^^", []string{"lane", "lorelai", "rori"},
			completeWith("myArg"), expectDispatch("rori", "myArg"), `
rori ▐              
myArg               
                    
                    
                    
                    
                    
                    
                    
                    `},
		{"draw delete after load from history with autocomplete scroll through history",
			"lo my✌>ro my✌>@@^^^^^oArg", []string{"lane", "lorelai", "rori"},
			completeWith("myArg"), expectDispatch("rori", "myArg"), `
lorelai oArg▐       
                    
                    
                    
                    
                    
                    
                    
                    
                    `},
		{"draw command in list with extra spaces in args no auto-complete",
			"lo   /tmp/a", []string{"lane", "lorelai", "rori"},
			nopComplete, nopDispatch, `
lorelai   /tmp/a▐   
                    
                    
                    
                    
                    
                    
                    
                    
                    `},
		{"draw command in list with extra spaces in args that are deleted no auto-complete",
			"lo   ^^^ /tmp/a", []string{"lane", "lorelai", "rori"},
			nopComplete, nopDispatch, `
lorelai /tmp/a▐     
                    
                    
                    
                    
                    
                    
                    
                    
                    `},
		{"draw command with multiple args with auto-complete",
			"ro my✌ oro", []string{"lane", "lorelai", "rori"},
			expectCompleteWith(
				[][]string{
					{""}, {"m"}, {"myArg", ""}, {"myArg", ""},
					{"myArg", "o"},
				},
				[][]string{
					{"myArg"}, {"myArg"}, {"myArg"}, {"oregano", "oregani"}, {"oregano", "oregani"},
					{"oregano", "oregani"}, {"oregano", "oregani"}, {"oregano", "oregani"},
				}),
			nopDispatch, `
rori myArg  oro▐    
oregano             
                    
                    
                    
                    
                    
                    
                    
                    `},
		{"no completion uses historical positional args as completion list items",
			"ro myArg>ro my✌", []string{"lane", "lorelai", "rori"},
			expectCompleteWith(
				[][]string{
					{""}, {"m"}, {""}, {"m"}, {"myArg", ""},
				},
				[][]string{}),
			expectDispatch("rori", "myArg"), `
rori myArg ▐        
                    
                    
                    
                    
                    
                    
                    
                    
                    `},
		{"no completion uses historical positional args as completion list items (3rd argument)",
			"ro myArg oro>ro myArg or", []string{"lane", "lorelai", "rori"},
			expectCompleteWith(
				[][]string{
					{""}, {"m"}, {"myArg", ""}, {"myArg", "o"},
					{""}, {"m"}, {"myArg", ""}, {"myArg", "o"},
				},
				[][]string{}),
			expectDispatch("rori", "myArg", "oro"), `
rori myArg or▐      
oro                 
                    
                    
                    
                    
                    
                    
                    
                    `},
		{"no completion uses historical positional args as completion list items (3rd argument, tab)",
			"ro myArg oro>ro myArg o✌", []string{"lane", "lorelai", "rori"},
			expectCompleteWith(
				[][]string{
					{""}, {"m"}, {"myArg", ""}, {"myArg", "o"},
					{""}, {"m"}, {"myArg", ""}, {"myArg", "o"}, {"myArg", "oro", ""},
				},
				[][]string{}),
			expectDispatch("rori", "myArg", "oro"), `
rori myArg oro ▐    
                    
                    
                    
                    
                    
                    
                    
                    
                    `},
		{"fuzzy complete tab expands from historical args",
			"ro myArg oro>ro mo✌", []string{"lane", "lorelai", "rori"},
			expectCompleteWith(
				[][]string{
					{""}, {"m"}, {"myArg", ""}, {"myArg", "o"},
					{""}, {"m"}, {"myArg", "oro", ""},
				},
				[][]string{}),
			expectDispatch("rori", "myArg", "oro"), `
rori myArg oro ▐    
                    
                    
                    
                    
                    
                    
                    
                    
                    `},
		{"history items can be deleted on backspace keypress when scrolling",
			"ro myArg 1>ro myArg 2>ro myArg 3>ro myArg 4>ro myArg 5>ro my⬇⬇^",
			[]string{"lane", "lorelai", "rori"},
			nopComplete,
			expectDispatch("rori", "myArg", "5"), `
rori my▐            
myArg 1             
myArg 3             
myArg 4             
myArg 5             
                    
                    
                    
                    
                    `},

		{"when you deleted all list elements and keep pressing backspace you delete prompt chars",
			"ro myArg 1>ro myArg 2>ro myArg 3>ro my⬇^^^^",
			[]string{"lane", "lorelai", "rori"},
			nopComplete,
			expectDispatch("rori", "myArg", "3"), `
rori m▐             
                    
                    
                    
                    
                    
                    
                    
                    
                    `},
		{"non historical items cannot be deleted, instead command prompt takes the backspace as a char remove",
			"ro my✌ ore⬇^", []string{"lane", "lorelai", "rori"},
			expectCompleteWith(
				[][]string{
					{""}, {"m"}, {"myArg", ""}, {"myArg", ""}, {"myArg", "o"},
				},
				[][]string{
					{"myArg"}, {"myArg"}, {"myArg"}, {"oregano", "oregani"}, {"oregano", "oregani"},
					{"oregano", "oregani"}, {"oregano", "oregani"}, {"oregano", "oregani"}, {"oregano", "oregani"},
				}),
			nopDispatch, `
rori myArg  or▐     
oregani             
oregano             
                    
                    
                    
                    
                    
                    
                    `},

		{"backspace deletes characters instead of history elements when not scrolling",
			"ro myArg 1>ro myArg 2>ro myArg 3>ro myArg 4>ro myArg 5>ro myAr^^^",
			[]string{"lane", "lorelai", "rori"},
			nopComplete,
			expectDispatch("rori", "myArg", "5"), `
rori m▐             
myArg 1             
myArg 2             
myArg 3             
myArg 4             
myArg 5             
                    
                    
                    
                    `},

		{"tab on a directory candidate descends and lists its children",
			"wo a✌", []string{"wo"},
			completeDirsUnder(dirs), nopDispatch, `
wo alpha/▐          
alpha/beta/         
alpha/delta/        
                    
                    
                    
                    
                    
                    
                    `},
		{"tab on a directory candidate twice descends two levels",
			"wo a✌b✌", []string{"wo"},
			completeDirsUnder(dirs), nopDispatch, `
wo alpha/beta/▐     
alpha/beta/gamma/   
                    
                    
                    
                    
                    
                    
                    
                    `},
	}

	for _, tcase := range tsuite {
		t.Run(tcase.desc, func(t *testing.T) {
			dispatchFn, cleanup := tcase.dispatchCmd()
			defer cleanup(t)

			completeFn, cleanupComplete := tcase.completeCmd()
			defer cleanupComplete(t)

			storage := storagestub.NewInMemoryService()
			b := NewPrompt(
				storage, FuncCompleter(completeFn), FuncDispatcher(dispatchFn),
				term.NopInterrupter(), testNoManualCommands(tcase.commands), cfg,
			)
			defer b.Close()
			cases := []handlertest.SequenceTestCase{
				{InputSequence: tcase.sequence, Expected: tcase.expectedDraw[1:]},
			}
			handlertest.TestHandlerSequence(t, testCommandHandler{b}, 20, 10, cases)
		})
	}
}

type testNeverEndingIterator struct {
}

func (c testNeverEndingIterator) Next(context.Context) (string, bool) {
	time.Sleep(10 * time.Millisecond)
	return "hola 123", true
}

func (c testNeverEndingIterator) Err() error {
	return nil
}

func (c testNeverEndingIterator) Close() error {
	return nil
}

func neverEndingComplete() (func(context.Context, []string) (iterator.Iterator[string], string, error), func(*testing.T)) {
	return func(ctx context.Context, args []string) (iterator.Iterator[string], string, error) {
		it := testNeverEndingIterator{}
		return it, "123", nil
	}, func(*testing.T) {}
}

type closeTrackingCompletionIterator struct {
	closed bool
}

func (c *closeTrackingCompletionIterator) Next(context.Context) (string, bool) {
	return "", false
}

func (c *closeTrackingCompletionIterator) Err() error { return nil }

func (c *closeTrackingCompletionIterator) Close() error {
	c.closed = true
	return nil
}

// A completer may return a non-nil iterator alongside an error; the
// prompt must close it instead of dropping it.
func TestCommandHandlerClosesCompletionIteratorOnError(t *testing.T) {
	dispatchFn, cleanup := nopDispatch()
	defer cleanup(t)

	it := &closeTrackingCompletionIterator{}
	completeFn := func(context.Context, []string) (iterator.Iterator[string], string, error) {
		return it, "", errors.New("completer failed")
	}

	b := NewPrompt(
		storagestub.NewInMemoryService(), FuncCompleter(completeFn),
		FuncDispatcher(dispatchFn), term.NopInterrupter(), nil,
		testDefaultConfig(),
	)
	defer b.Close()

	for _, runeValue := range "hello " {
		_, handled := b.handle(term.Event{Type: term.EventKey, Ch: runeValue}, false)
		require.True(t, handled)
	}

	assert.True(t, it.closed)
}

func TestCommandHandlerCancel(t *testing.T) {
	storage := storagestub.NewInMemoryService()
	t.Run("ctrl-c once cancels search; twice closes window", func(t *testing.T) {
		dispatchFn, cleanup := nopDispatch()
		defer cleanup(t)

		completeFn, cleanupComplete := neverEndingComplete()
		defer cleanupComplete(t)

		b := NewPrompt(
			storage, FuncCompleter(completeFn), FuncDispatcher(dispatchFn),
			term.NopInterrupter(), nil, testDefaultConfig(),
		)

		// Type in the command to stimulate the `neverEndingComplete` completion iterator
		for _, runeValue := range "hello " {
			quit, handled := b.handle(term.Event{Type: term.EventKey, Ch: runeValue}, false)
			require.False(t, quit)
			require.True(t, handled)
		}

		// must not quit window, instead it must cancel the never ending completion we set up
		quit, handled := b.handle(term.Event{Type: term.EventKey, Ch: 'c', Mod: term.ModCtrl}, true)
		assert.False(t, quit)
		assert.True(t, handled)

		// check the completion is canceled
		var completionCanceled bool
		select {
		case <-b.completionCtx.Done():
			completionCanceled = true
		default:
			completionCanceled = false
		}
		require.True(t, completionCanceled)

		// must quit window, since the completion is already canceled by the previous Ctrl-C
		quit, handled = b.handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'c'}, false)
		assert.True(t, quit)
		assert.True(t, handled)
	})
}

// TestCommandHandlerHistoryCanonicalizesArguments asserts that the
// partial marker never reaches history. Storing it would split one
// workspace into two entries — the form recorded before the
// partial-candidate convention existed and the form recorded after —
// and force the user to work around the stale one.
func TestCommandHandlerHistoryCanonicalizesArguments(t *testing.T) {
	cfg := testDefaultConfig()
	cfg.ShowManual = false
	cfg.Sync = true
	dirs := testDirTree(t)

	tsuite := []struct {
		desc     string
		sequence string
		want     string
	}{
		{"descended argument drops the marker",
			"a<tab><enter>", "workspaceopen alpha"},
		{"twice-descended argument drops the marker",
			"a<tab>b<tab><enter>", "workspaceopen alpha/beta"},
		{"quoted descended argument drops the marker inside the quotes",
			"m<tab><enter>", "workspaceopen 'my dir'"},
		{"a hand-typed trailing separator is canonicalized too",
			"alpha/<enter>", "workspaceopen alpha"},
		{"an argument without a marker is stored verbatim",
			"alpha<enter>", "workspaceopen alpha"},
		{"the filesystem root survives canonicalization",
			"/<enter>", "workspaceopen /"},
	}

	for _, tcase := range tsuite {
		t.Run(tcase.desc, func(t *testing.T) {
			b := NewPrompt(
				storagestub.NewInMemoryService(),
				FuncCompleter(NonRecursiveDirsCompleter(newFSReader(dirs)).Complete),
				FuncDispatcher(nopDispatchFn), term.NopInterrupter(),
				testNoManualCommands([]string{"workspaceopen"}), cfg,
			)
			defer b.Close()

			keys, err := term.ParseKeys("workspaceopen<space>" + tcase.sequence)
			require.NoError(t, err)
			for _, key := range keys {
				testCommandHandler{b}.Handle(term.Event{
					Type: term.EventKey,
					Ch:   key.Ch,
					Mod:  key.Mod,
					Key:  key.Key,
				})
			}

			assert.Equal(t, []string{tcase.want}, b.history.Slice())
		})
	}
}

func TestCommandHandlerHideProgressHint(t *testing.T) {
	dispatchFn, cleanup := nopDispatch()
	defer cleanup(t)

	completeFn, cleanupComplete := neverEndingComplete()
	defer cleanupComplete(t)

	cfg := testDefaultConfig()
	cfg.ShowProgressHint = false
	b := NewPrompt(
		storagestub.NewInMemoryService(),
		FuncCompleter(completeFn),
		FuncDispatcher(dispatchFn),
		term.NopInterrupter(),
		nil,
		cfg,
	)
	defer b.Close()

	for _, runeValue := range "hello " {
		quit, handled := b.handle(term.Event{Type: term.EventKey, Ch: runeValue}, false)
		require.False(t, quit)
		require.True(t, handled)
	}

	w := term.NewStringWriter(20, 3)
	b.Resize(20, 3)
	b.Draw(w)
	assert.NotContains(t, w.String(), "⠃")
	assert.NotContains(t, w.String(), "⠋")
	assert.NotContains(t, w.String(), "⠙")
}

type testFeederIterator struct {
	feeder chan string
}

func (c testFeederIterator) Next(context.Context) (string, bool) {
	s, ok := <-c.feeder
	return s, ok
}

func (c testFeederIterator) Err() error {
	return errors.New("bang")
}

func (c testFeederIterator) Close() error {
	return nil
}

func TestCommandHandlerHistory(t *testing.T) {
	t.Run("remove historical item (start, middle and end of list)", func(t *testing.T) {
		dispatchFn, cleanup := nopDispatch()
		defer cleanup(t)

		completeFn, cleanupComplete := nopComplete()
		defer cleanupComplete(t)

		b := NewPrompt(
			storagestub.NewInMemoryService(),
			FuncCompleter(completeFn),
			FuncDispatcher(dispatchFn),
			term.NopInterrupter(),
			nil,
			testDefaultConfig(),
		)
		defer b.Close()

		// only historical items can be removed
		b.completingWithHistory.Store(true)

		// mock an async iterator we can feed elements to using a channel
		var slice []string
		ctx, cancel := context.WithCancel(b.ctx)
		for i := range 10 {
			slice = append(slice, fmt.Sprintf("! echo xyz_%d", i))
		}
		it := iterator.FromSlice(slice)
		b.pushCompletionListSync(ctx, cancel, []string{"! echo"}, it)

		require.Equal(t, 10, b.list.TotalCount())

		// focus end and check it's xyz_9
		ok := b.list.FocusEnd()
		require.True(t, ok)
		match, ok := b.list.Focus()
		require.True(t, ok)
		require.Equal(t, "! echo xyz_9", string(match.Data()))

		// remove it!
		ok = b.list.RemoveFocus() // remove "! echo xyz_9"
		assert.True(t, ok)

		// focus should go up to xyz_8 because there was no more nodes after de
		// removed one
		match, ok = b.list.Focus()
		require.True(t, ok)
		assert.Equal(t, "! echo xyz_8", string(match.Data()))

		// focus end and check it's xyz_0
		ok = b.list.FocusStart()
		require.True(t, ok)
		match, ok = b.list.Focus()
		require.True(t, ok)
		require.Equal(t, "! echo xyz_0", string(match.Data()))

		// remove it!
		ok = b.list.RemoveFocus() // remove "! echo xyz_0"
		assert.True(t, ok)

		// focus should go up to xyz_1 because it's what comes next
		match, ok = b.list.Focus()
		require.True(t, ok)
		assert.Equal(t, "! echo xyz_1", string(match.Data()))

		// focus middle of list
		ok = b.list.FocusDown() // ! echo xyz_2
		require.True(t, ok)
		ok = b.list.FocusDown() // ! echo xyz_3
		require.True(t, ok)
		match, ok = b.list.Focus()
		require.True(t, ok)
		require.Equal(t, "! echo xyz_3", string(match.Data()))

		// remove it!
		ok = b.list.RemoveFocus() // remove "! echo xyz_0"
		assert.True(t, ok)

		// focus should go up to xyz_4 because it's what comes next
		match, ok = b.list.Focus()
		require.True(t, ok)
		assert.Equal(t, "! echo xyz_4", string(match.Data()))
	})

	t.Run("history concurrently pushing and removing doesn't "+
		"panic nor cause data races", func(t *testing.T) {
		dispatchFn, cleanup := nopDispatch()
		defer cleanup(t)

		completeFn, cleanupComplete := nopComplete()
		defer cleanupComplete(t)

		b := NewPrompt(
			storagestub.NewInMemoryService(),
			FuncCompleter(completeFn),
			FuncDispatcher(dispatchFn),
			term.NopInterrupter(),
			nil,
			testDefaultConfig(),
		)
		defer b.Close()

		// only historical items can be removed
		b.completingWithHistory.Store(true)

		// mock an async iterator we can feed elements to using a channel
		it := testFeederIterator{feeder: make(chan string)}

		// connect the consumption of the iterator to the population of the search list
		ctx, cancel := context.WithCancel(b.ctx)
		ch := b.list.Push(ctx)
		go pushCompletionList(ctx, b.log, &b.completingWithHistory,
			b.history.Slice(), b.mode, ch, cancel, []string{"echo"}, it)

		startingPistol := make(chan struct{})

		var wg sync.WaitGroup
		wg.Add(2)

		numAdditions := 500
		numRemovals := 300

		go func() {
			defer wg.Done()
			<-startingPistol

			defer close(it.feeder)
			for i := range numAdditions {
				it.feeder <- fmt.Sprintf("abc_%d", i)
			}
		}()

		go func() {
			defer wg.Done()
			<-startingPistol

			time.Sleep(5 * time.Millisecond)
			b.list.FocusStart()

			// copy var so we don't introduce side effects when changing a variable
			// that's used in the for loop iteration scope
			nr := numRemovals

			for range numRemovals {
				b.list.FocusDown()
				if ok := b.list.RemoveFocus(); !ok {
					nr--
				}
			}
		}()

		close(startingPistol)
		wg.Wait()
	})
}

func TestCommandHandlerResetHistory(t *testing.T) {
	cfg := testDefaultConfig()
	cfg.HistoryCycleKey = term.KeyComb{Ch: ':'}
	cfg.Sync = true

	dispatched := make([]string, 0)
	dispatchFn := func(command string, args ...string) bool {
		dispatched = append(dispatched, command)
		return true
	}

	completeFn := func(ctx context.Context, args []string) (iterator.Iterator[string], string, error) {
		return iterator.FromSlice[string](nil), "", nil
	}

	commands := testNoManualCommands([]string{"edit", "quit", "write"})

	b := NewPrompt(
		storagestub.NewInMemoryService(),
		FuncCompleter(completeFn),
		FuncDispatcher(dispatchFn),
		term.NopInterrupter(),
		commands,
		cfg,
	)
	defer b.Close()

	// Dispatch some commands via Handle to populate history.
	// Type "edit" + Enter, then reopen prompt, type "write" + Enter,
	// then reopen prompt, type "quit" + Enter.
	for _, cmd := range []string{"edit", "write", "quit"} {
		b.Reset(commands)
		for _, ch := range cmd {
			b.Handle(term.Event{Type: term.EventKey, Ch: ch})
		}
		b.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	}

	require.Equal(t, []string{"edit", "write", "quit"}, dispatched)

	// Now call ResetHistory — the list should contain the history entries newest-first.
	b.ResetHistory()

	require.Equal(t, 3, b.list.TotalCount())

	// Check the order is newest-first: quit, write, edit
	ok := b.list.FocusStart()
	require.True(t, ok)

	match, ok := b.list.Focus()
	require.True(t, ok)
	assert.Equal(t, "quit", string(match.Data()))

	ok = b.list.FocusDown()
	require.True(t, ok)
	match, ok = b.list.Focus()
	require.True(t, ok)
	assert.Equal(t, "write", string(match.Data()))

	ok = b.list.FocusDown()
	require.True(t, ok)
	match, ok = b.list.Focus()
	require.True(t, ok)
	assert.Equal(t, "edit", string(match.Data()))
}

func TestCommandHandlerHistoryToggleKey(t *testing.T) {
	toggleKey := term.KeyComb{Mod: term.ModMeta, Ch: 'r'}

	cfg := testDefaultConfig()
	cfg.HistoryCycleKey = term.KeyComb{Ch: ':'}
	cfg.HistoryToggleKey = toggleKey
	cfg.Sync = true

	dispatched := make([]string, 0)
	dispatchFn := func(command string, args ...string) bool {
		dispatched = append(dispatched, command)
		return true
	}

	completeFn := func(ctx context.Context, args []string) (iterator.Iterator[string], string, error) {
		return iterator.FromSlice[string](nil), "", nil
	}

	commands := testNoManualCommands([]string{"edit", "quit", "write"})

	b := NewPrompt(
		storagestub.NewInMemoryService(),
		FuncCompleter(completeFn),
		FuncDispatcher(dispatchFn),
		term.NopInterrupter(),
		commands,
		cfg,
	)
	defer b.Close()

	// Dispatch commands to populate history.
	for _, cmd := range []string{"edit", "write"} {
		b.Reset(commands)
		for _, ch := range cmd {
			b.Handle(term.Event{Type: term.EventKey, Ch: ch})
		}
		b.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	}
	require.Equal(t, []string{"edit", "write"}, dispatched)

	// Reset prompt to show commands.
	b.Reset(commands)
	require.Equal(t, 3, b.list.TotalCount())
	assert.False(t, b.showingHistory)

	// Press toggle key — should switch to history.
	b.Handle(term.Event{Type: term.EventKey, Mod: toggleKey.Mod, Ch: toggleKey.Ch})
	assert.True(t, b.showingHistory)
	require.Equal(t, 2, b.list.TotalCount())

	ok := b.list.FocusStart()
	require.True(t, ok)
	match, ok := b.list.Focus()
	require.True(t, ok)
	assert.Equal(t, "write", string(match.Data()))

	ok = b.list.FocusDown()
	require.True(t, ok)
	match, ok = b.list.Focus()
	require.True(t, ok)
	assert.Equal(t, "edit", string(match.Data()))

	// Press toggle key again — should switch back to commands.
	b.Handle(term.Event{Type: term.EventKey, Mod: toggleKey.Mod, Ch: toggleKey.Ch})
	assert.False(t, b.showingHistory)
	require.Equal(t, 3, b.list.TotalCount())

	ok = b.list.FocusStart()
	require.True(t, ok)
	match, ok = b.list.Focus()
	require.True(t, ok)
	assert.Equal(t, "edit", string(match.Data()))
}

type testCommandHandler struct {
	*Prompt
}

// testDefaultConfig returns a Config suitable for prompt tests that
// don't otherwise care about edit mode: it wires a no-op stub Editor
// so NewPrompt's required-Editor invariant is satisfied.
func testDefaultConfig() Config {
	cfg := DefaultConfig()
	cfg.Editor = stubEditorImpl{}
	// most draw assertions predate the shadow argument hint and expect
	// the input row to stay blank past the cursor.
	cfg.ShowArgHint = false
	return cfg
}

func (t testCommandHandler) Handle(ev term.Event) (bool, bool) {
	t.Wait()
	quit, handled := t.Prompt.Handle(ev)
	t.Wait()
	return quit, handled
}

// stubEditorImpl is a minimal command.Editor used to verify that the
// command Prompt correctly delegates events while in modal edit mode.
type stubEditorImpl struct {
	seen *[]term.Event
}

func (e stubEditorImpl) Edit(buf *cell.Buffer) EditHandler {
	return &stubEditHandler{buf: buf, seen: e.seen}
}

// stubEditHandler is the tui.Handler returned by stubEditorImpl. It
// appends typed runes to the end of buf and removes the last rune on
// backspace; everything else is recorded but otherwise ignored.
type stubEditHandler struct {
	buf  *cell.Buffer
	seen *[]term.Event
	// cursor X within the buffer; updated by SetCursorAtScroll and
	// recorded by tests asserting that the prompt restores the
	// command-mode cursor on entry.
	cursorX int
	// selection is returned by Selection() so tests can verify the
	// Prompt forwards Selection from the active EditHandler.
	selection string
	// selectionFrom/selectionTo back SelectionBounds so tests can
	// stage a buffer-relative selection range and assert that the
	// Prompt overlays the highlight in its own coordinate system.
	selectionFrom term.Coordinates
	selectionTo   term.Coordinates
	hasSelection  bool
}

func (s *stubEditHandler) Resize(width, height int) {}
func (s *stubEditHandler) Draw(term.Writer)         {}
func (s *stubEditHandler) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return term.Coordinates{X: s.cursorX}, term.CursorStyleBlinkingBlock, true
}
func (s *stubEditHandler) CursorAtScroll() term.Coordinates {
	return term.Coordinates{X: s.cursorX}
}
func (s *stubEditHandler) SetCursorAtScroll(pos term.Coordinates) bool {
	s.cursorX = pos.X
	return true
}
func (s *stubEditHandler) Selection() (string, bool) { return s.selection, s.selection != "" }

// SelectionBounds satisfies the optional SelectionBoundsHandler
// capability.
func (s *stubEditHandler) SelectionBounds() (from, to term.Coordinates, ok bool) {
	if !s.hasSelection {
		return term.Coordinates{}, term.Coordinates{}, false
	}
	return s.selectionFrom, s.selectionTo, true
}
func (s *stubEditHandler) Handle(ev term.Event) (bool, bool) {
	if s.seen != nil {
		*s.seen = append(*s.seen, ev)
	}
	if ev.Type != term.EventKey {
		return false, true
	}
	switch ev.Key {
	case term.KeyBackspace:
		cols := s.buf.Columns(0)
		if cols > 0 {
			s.buf.DeleteCell(term.Coordinates{X: cols - 1})
		}
		return false, true
	case term.KeySpace:
		s.buf.WriteString(" ")
		return false, true
	}
	if ev.Ch != 0 {
		s.buf.WriteString(string(ev.Ch))
	}
	return false, true
}

var _ EditHandler = (*stubEditHandler)(nil)

// capturingEditor is a command.Editor that records the most recently
// returned stubEditHandler so tests can assert on its observed state
// (cursor position, etc.).
type capturingEditor struct {
	captured **stubEditHandler
}

func (c capturingEditor) Edit(buf *cell.Buffer) EditHandler {
	h := &stubEditHandler{buf: buf}
	if c.captured != nil {
		*c.captured = h
	}
	return h
}

var (
	lotsOfCommands []string
)

func init() {
	for i := range 100 {
		lotsOfCommands = append(lotsOfCommands, strconv.Itoa(i))
	}
}

func nopComplete() (
	func(context.Context, []string) (iterator.Iterator[string], string, error), func(*testing.T),
) {
	return func(ctx context.Context, args []string) (iterator.Iterator[string], string, error) {
		return iterator.FromSlice[string](nil), "", nil
	}, func(*testing.T) {}
}

// testDirTree lays out the fixture the directory-descent cases walk.
// Entries are ordered so the first candidate of every listing is
// deterministic: alpha before "my dir", beta before delta. The tree is
// built with the host separator, but the candidates the cases assert on
// are URI paths and therefore always slash-separated.
func testDirTree(tb testing.TB) string {
	tb.Helper()
	root := tb.TempDir()
	for _, dir := range []string{
		filepath.Join("alpha", "beta", "gamma"),
		filepath.Join("alpha", "delta"),
		filepath.Join("my dir", "inner"),
	} {
		require.NoError(tb, os.MkdirAll(filepath.Join(root, dir), 0o700))
	}
	return root
}

// completeDirsUnder drives the cases through the production
// non-recursive directory completer rather than a stub, so the partial
// marker the prompt reacts to is the one real completions carry.
func completeDirsUnder(root string) func() (
	func(context.Context, []string) (iterator.Iterator[string], string, error), func(*testing.T),
) {
	return func() (
		func(context.Context, []string) (iterator.Iterator[string], string, error), func(*testing.T),
	) {
		return NonRecursiveDirsCompleter(newFSReader(root)).Complete, func(*testing.T) {}
	}
}

func completeWith(data ...string) func() (
	func(context.Context, []string) (iterator.Iterator[string], string, error), func(*testing.T),
) {
	return func() (
		func(ctx context.Context, args []string) (iterator.Iterator[string], string, error), func(*testing.T),
	) {
		return func(ctx context.Context, args []string) (
				iterator.Iterator[string], string, error,
			) {
				args = args[1:]
				if len(args) != 0 && args[len(args)-1] == "~" {
					return iterator.FromSlice(data), "expanded/", nil
				}
				return iterator.FromSlice(data), "", nil
			},
			func(*testing.T) {}
	}
}

func completeRespectively(data []string) func() (
	func(context.Context, []string) (iterator.Iterator[string], string, error), func(*testing.T),
) {
	return func() (func(context.Context, []string) (iterator.Iterator[string], string, error), func(*testing.T)) {
		return func(ctx context.Context, args []string) (iterator.Iterator[string], string, error) {
			args = args[1:]
			if len(args) > len(data) {
				return iterator.FromSlice[string](nil), "", nil
			}
			completing := []string{data[len(args)-1]}
			return iterator.FromSlice(completing), "", nil
		}, func(*testing.T) {}
	}
}

func completeNormalizing(arg string) func() (
	func(context.Context, []string) (iterator.Iterator[string], string, error), func(*testing.T),
) {
	return func() (func(context.Context, []string) (iterator.Iterator[string], string, error), func(*testing.T)) {
		return func(ctx context.Context, args []string) (iterator.Iterator[string], string, error) {
			if len(args) > 1 && args[len(args)-1] == arg {
				return iterator.FromSlice[string](nil), arg, nil
			}
			return iterator.FromSlice[string](nil), "", nil
		}, func(*testing.T) {}
	}
}

func expectCompleteWith(expectedArgs [][]string, data [][]string) func() (
	func(ctx context.Context, args []string) (iterator.Iterator[string], string, error), func(*testing.T),
) {
	var actualArgsSlice [][]string
	var called int
	return func() (func(context.Context, []string) (iterator.Iterator[string], string, error), func(*testing.T)) {
		return func(ctx context.Context, args []string) (iterator.Iterator[string], string, error) {
				args = args[1:]
				if called >= len(data) {
					called++ // cleanup will catch it
					actualArgsSlice = append(actualArgsSlice, args)
					return iterator.FromSlice[string](nil), "", nil
				}
				actualArgsSlice = append(actualArgsSlice, args)
				ret := iterator.FromSlice(data[called])
				called++
				return ret, "", nil
			}, func(t *testing.T) {
				require.Equal(t, len(expectedArgs), called,
					"actual => %v", actualArgsSlice)
				for i, actualArgs := range actualArgsSlice {
					assert.Equal(t, expectedArgs[i], actualArgs, i)
				}
			}
	}
}

func nopDispatch() (func(string, ...string) bool, func(*testing.T)) {
	var called bool
	ret := func(command string, args ...string) bool {
		called = true
		return true
	}
	return ret, func(t *testing.T) {
		assert.False(t, called)
	}
}

func expectDispatch(expectedCmd string, expectedArgs ...string) func() (func(string, ...string) bool, func(*testing.T)) {
	return func() (func(string, ...string) bool, func(*testing.T)) {
		var called bool
		var actualCmd string
		var actualArgs []string
		ret := func(command string, args ...string) bool {
			called = true
			actualCmd = command
			actualArgs = args
			return true
		}
		return ret, func(t *testing.T) {
			assert.True(t, called)
			assert.Equal(t, expectedCmd, actualCmd)
			assert.Equal(t, append([]string{}, expectedArgs...), append([]string{}, actualArgs...))
		}
	}
}

func testNoManualCommands(cmds []string) (ret []Manual) {
	for _, cmd := range cmds {
		ret = append(ret, Manual{Name: cmd})
	}
	return
}

var keybindingTestCommands = []Manual{
	{Name: "edit", Summary: "Open a file.", Synopsis: "<file>"},
	{Name: "quit", Summary: "Close the editor."},
	{Name: "lsp", Summary: "Language server ops.", Synopsis: "<cmd>",
		Commands: []Manual{
			{Name: "diagnostics", Summary: "Show diagnostics."},
			{Name: "hover", Summary: "Show hover info."},
		},
	},
}

func keybindingTestConfig() Config {
	cfg := testDefaultConfig()
	cfg.Sync = true
	cfg.ShowManual = false
	return cfg
}

// TestCommandHandlerKeyBindingHintsDraw exercises the right-aligned key
// hint overlay geometry across the horizontal-space corner cases: the
// hint requires at least one blank cell after the row text and is
// dropped entirely when the command plus gap plus label do not fit.
func TestCommandHandlerKeyBindingHintsDraw(t *testing.T) {
	tsuite := []struct {
		desc      string
		sequence  string
		commands  []Manual
		hint      func(string) string
		completer func(context.Context, []string) (
			iterator.Iterator[string], string, error)
		width, height int
		expectedDraw  string
	}{
		{
			desc:     "bound rows right-aligned, unbound rows bare",
			commands: keybindingTestCommands,
			hint: func(line string) string {
				switch line {
				case "edit":
					return "<c-e>"
				case "lsp":
					return "<m-l>"
				}
				return ""
			},
			width: 20, height: 6,
			expectedDraw: `
▐                   
edit           <c-e>
quit                
lsp            <m-l>
                    
                    `,
		},
		{
			desc:     "hint keeps a single gap cell at minimum width",
			commands: []Manual{{Name: "edit"}},
			hint:     func(string) string { return "<c-e>" },
			width:    10, height: 4,
			expectedDraw: `
▐         
edit <c-e>
          
          `,
		},
		{
			desc:     "hint dropped when the gap cell vanishes",
			commands: []Manual{{Name: "edit"}},
			hint:     func(string) string { return "<c-e>" },
			width:    9, height: 4,
			expectedDraw: `
▐        
edit     
         
         `,
		},
		{
			desc:     "hint dropped when label spans the full width",
			commands: []Manual{{Name: "a"}},
			hint:     func(string) string { return "<ctrl-e>" },
			width:    8, height: 4,
			expectedDraw: `
▐       
a       
        
        `,
		},
		{
			desc:     "hint dropped when the command overflows the row",
			commands: []Manual{{Name: "windowconvert"}},
			hint:     func(string) string { return "<c-w>" },
			width:    12, height: 4,
			expectedDraw: `
▐           
windowconver
            
            `,
		},
		{
			desc:     "subcommand rows resolve through the committed prefix",
			sequence: "lsp<space>",
			commands: keybindingTestCommands,
			hint: func(line string) string {
				if line == "lsp diagnostics" {
					return "<alt-shift-e>"
				}
				return ""
			},
			width: 30, height: 5,
			expectedDraw: `
lsp ▐                         
diagnostics      <alt-shift-e>
hover                         
                              
                              `,
		},
		{
			desc:     "dynamic argument rows are never hinted",
			sequence: "edit<space>",
			commands: keybindingTestCommands,
			hint: func(line string) string {
				if line == "edit arg1" {
					return "<c-1>"
				}
				return ""
			},
			completer: func(_ context.Context, cmdAndArgs []string) (
				iterator.Iterator[string], string, error,
			) {
				if len(cmdAndArgs) > 0 && cmdAndArgs[0] == "edit" {
					return iterator.FromSlice([]string{"arg1", "arg2"}), "", nil
				}
				return iterator.FromSlice[string](nil), "", nil
			},
			width: 20, height: 5,
			expectedDraw: `
edit ▐              
arg1                
arg2                
                    
                    `,
		},
		{
			desc:     "nil resolver draws no hints",
			commands: keybindingTestCommands,
			hint:     nil,
			width:    20, height: 5,
			expectedDraw: `
▐                   
edit                
quit                
lsp                 
                    `,
		},
		{
			desc:     "fuzzy filtered rows keep their hints",
			sequence: "q",
			commands: keybindingTestCommands,
			hint: func(line string) string {
				if line == "quit" {
					return "<c-q>"
				}
				return ""
			},
			width: 20, height: 4,
			expectedDraw: `
q▐                  
quit           <c-q>
                    
                    `,
		},
	}

	for _, tcase := range tsuite {
		t.Run(tcase.desc, func(t *testing.T) {
			t.Parallel()
			cfg := keybindingTestConfig()
			cfg.KeyBindingHint = tcase.hint

			dispatchFn, cleanup := nopDispatch()
			defer cleanup(t)

			completeFn := tcase.completer
			if completeFn == nil {
				var cleanupComplete func(*testing.T)
				completeFn, cleanupComplete = nopComplete()
				defer cleanupComplete(t)
			}

			b := NewPrompt(
				storagestub.NewInMemoryService(), FuncCompleter(completeFn),
				FuncDispatcher(dispatchFn), term.NopInterrupter(),
				tcase.commands, cfg,
			)
			defer b.Close()
			cases := []handlertest.SequenceTestCase{
				{InputSequence: tcase.sequence, Expected: tcase.expectedDraw[1:]},
			}
			handlertest.RunHandlerSequence(t, testCommandHandler{b},
				tcase.width, tcase.height, cases)
		})
	}
}

// hintFgAt returns the foreground color of the rightmost non-space cell
// on the row identified by prefix, i.e. the last rune of the key hint.
func hintFgAt(t *testing.T, w *term.StringWriter, width int, prefix string) term.Color {
	t.Helper()
	lines := strings.Split(w.String(), "\n")
	cells := w.Cells()
	for y, l := range lines {
		if !strings.HasPrefix(strings.TrimLeft(l, " "), prefix) {
			continue
		}
		for x := width - 1; x >= 0; x-- {
			c := cells[y*width+x]
			if c.Ch != 0 && c.Ch != ' ' {
				return c.Fg
			}
		}
	}
	t.Fatalf("no hinted row found for prefix %q", prefix)
	return 0
}

// TestKeyBindingHintFocusColor draws the focused row's hint with the
// focus color and other rows' hints with the default color.
func TestKeyBindingHintFocusColor(t *testing.T) {
	cfg := keybindingTestConfig()
	cfg.KeyBindingHintAttr = term.Attributes{Fg: term.ColorGray}
	cfg.KeyBindingHintFocusAttr = term.Attributes{Fg: term.ColorSilver}
	cfg.KeyBindingHint = func(line string) string {
		switch line {
		case "edit":
			return "<ctrl-e>"
		case "quit":
			return "<ctrl-q>"
		}
		return ""
	}

	dispatchFn, cleanup := nopDispatch()
	defer cleanup(t)
	completeFn, cleanupComplete := nopComplete()
	defer cleanupComplete(t)
	p := NewPrompt(
		storagestub.NewInMemoryService(), FuncCompleter(completeFn),
		FuncDispatcher(dispatchFn), term.NopInterrupter(),
		keybindingTestCommands, cfg,
	)
	defer p.Close()

	// empty input keeps focus on the first row (edit)
	w := term.NewStringWriter(40, 8)
	p.Resize(40, 8)
	p.Wait()
	p.Draw(w)
	require.NoError(t, w.Flush())
	assert.Equal(t, term.ColorSilver, hintFgAt(t, w, 40, "edit"),
		"focused row hint must use the focus color")
	assert.Equal(t, term.ColorGray, hintFgAt(t, w, 40, "quit"),
		"non-focused row hint must use the default color")
}

// TestKeyBindingHintHistoryMode draws no hints while the prompt shows
// the command history list.
func TestKeyBindingHintHistoryMode(t *testing.T) {
	cfg := keybindingTestConfig()
	called := false
	cfg.KeyBindingHint = func(string) string {
		called = true
		return "<ctrl-e>"
	}

	dispatchFn, cleanup := nopDispatch()
	defer cleanup(t)
	completeFn, cleanupComplete := nopComplete()
	defer cleanupComplete(t)
	p := NewPrompt(
		storagestub.NewInMemoryService(), FuncCompleter(completeFn),
		FuncDispatcher(dispatchFn), term.NopInterrupter(),
		keybindingTestCommands, cfg,
	)
	defer p.Close()
	p.ResetHistory()

	w := term.NewStringWriter(40, 8)
	p.Resize(40, 8)
	p.Wait()
	p.Draw(w)
	require.NoError(t, w.Flush())
	assert.False(t, called, "history mode must not query key hints")
}

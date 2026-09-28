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

package standard

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component/comptest"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/handler/handlertest"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/texttest"
)

func TestFoldsIntegration(t *testing.T) {
	uri, err := workspaceapi.ParseURI("file:///vi_test")
	require.NoError(t, err)

	t.Run("alt shift brackets remain available to commands", func(t *testing.T) {
		buf := cell.NewBuffer()
		buf.WriteString(snippet)
		fs := &testFoldsService{}
		fs.view = buf.WithView(fs)
		h := NewHandler(buf, uri, '\t', 0)
		h.Resize(50, 10)
		require.True(t, h.SetCursorAtScroll(term.Coordinates{Y: 7}))

		for _, ch := range []rune{'{', '}'} {
			_, handled := h.Handle(term.Event{
				Type: term.EventKey,
				Mod:  term.ModAlt,
				Ch:   ch,
			})
			require.Falsef(t, handled,
				"Alt+Shift+bracket must reach the command layer")
		}
	})

	t.Run("initial folds", func(t *testing.T) {
		buf := cell.NewBuffer()
		buf.WriteString(snippet)
		fs := &testFoldsService{}
		fs.view = buf.WithView(fs)
		var wg sync.WaitGroup
		var mu sync.Mutex
		cb := func(fn func()) bool {
			// run async to guarantee we can lock below:
			// sometimes this cb can run in the main goroutine
			go func() {
				defer wg.Done()
				mu.Lock()
				defer mu.Unlock()
				fn()
			}()
			return true
		}
		wg.Add(1)
		mu.Lock()
		h := NewHandler(buf, uri,
			'\t', 0,
			WithHideInitialFolds(true),
			WithScheduleNextTick(cb),
			WithAutoCenter(true),
		)
		cfg := text.StatusBarConfig{
			Publisher: &texttest.TestEditor{},
			ScheduleNextTick: func(cb func()) bool {
				mu.Lock()
				defer mu.Unlock()
				cb()
				return true
			},
		}
		bar := text.WithStatusBar(h, buf, h.(*standardHandler).less.Scroll(),
			false, false, cfg)
		bar.Resize(20, 10)
		mu.Unlock()

		tests := []comptest.TestCase{
			{Expected: `
                    
/* [4 lines] */     
    void            
diff_buf_adjust(win_
{                   
    win_T    *wp;   
    int             
                    
    if (!win->w_p_di
                    `,
			},
		}

		wg.Wait()
		w := term.NewStringWriter(20, 10)

		mu.Lock()
		defer mu.Unlock()

		comptest.TestComponent(t, bar, w, tests)
	})

	t.Run("fold operations with auxiliary bar", func(t *testing.T) {
		buf := cell.NewBuffer()
		buf.WriteString(snippet)
		fs := &testFoldsService{}
		fs.view = buf.WithView(fs)
		var wg sync.WaitGroup
		var mu sync.Mutex
		cb := func(fn func()) bool {
			go func() {
				defer wg.Done()
				mu.Lock()
				fn()
				mu.Unlock()
			}()
			return true
		}
		mu.Lock()
		// text.Editor only installs auxiliary chrome when called via
		// text.Component (which threads text.WithBars in the
		// context). Tests that want to exercise the bar wiring stack
		// the bars on top of a bare standard handler directly.
		root := NewHandler(buf, uri, '\t', 0,
			WithScheduleNextTick(cb),
			WithAutoCenter(true),
		)
		scroll := root.(*standardHandler).less.Scroll()
		auxCfg := text.AuxBarConfig{FoldsEnabled: true, ScheduleNextTick: cb}
		statusCfg := text.StatusBarConfig{
			Publisher: &texttest.TestEditor{},
			ScheduleNextTick: func(cb func()) bool {
				cb()
				return true
			},
		}
		wg.Add(1)
		var h text.Handler = text.WithAuxBar(root, buf, scroll, auxCfg)
		h = text.WithStatusBar(h, buf, scroll, false, false, statusCfg)
		h.Resize(50, 10)
		mu.Unlock()
		wg.Wait() // wait for bar

		t.Run("zA", func(t *testing.T) {
			tests := []comptest.TestCase{
				{Expected: `
                                                  
 /* [4 lines] */                                 
      void                                        
  diff_buf_adjust(win_T *win)                     
 { [25 lines] }                                  
                                                  
                                                  
                                                  
                                                  
                                                  `,
				},
			}

			wg.Add(3)
			mu.Lock()
			_, handled := h.Handle(term.Event{Type: term.EventKey,
				Mod: term.ModMeta, Key: term.KeyArrowDown})
			require.True(t, handled)
			_, handled = h.Handle(term.Event{Type: term.EventKey,
				Mod: term.ModCtrl, Ch: 'A'})
			require.True(t, handled)
			_, handled = h.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowUp})
			require.True(t, handled)
			mu.Unlock()
			wg.Wait()
			w := term.NewStringWriter(50, 10)
			mu.Lock()
			comptest.TestComponent(t, h, w, tests)
			mu.Unlock()
		})

		t.Run("zo", func(t *testing.T) {
			tests := []comptest.TestCase{
				{Expected: `
                                                  
 /*                                              
  * Check if the current buffer should be added t
   * diff buffers.                                
   */                                             
      void                                        
  diff_buf_adjust(win_T *win)                     
 { [25 lines] }                                  
                                                  
                                                  `,
				},
			}

			wg.Add(1)
			mu.Lock()
			_, handled := h.Handle(term.Event{Type: term.EventKey,
				Mod: term.ModMeta, Key: term.KeyArrowUp})
			require.True(t, handled)
			_, handled = h.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowDown})
			require.True(t, handled)
			_, handled = h.Handle(term.Event{Type: term.EventKey,
				Mod: term.ModCtrl, Ch: '}'})
			mu.Unlock()
			wg.Wait()
			w := term.NewStringWriter(50, 10)
			mu.Lock()
			comptest.TestComponent(t, h, w, tests)
			mu.Unlock()
		})
	})
	t.Run("initial folds with initial cursor position", func(t *testing.T) {
		buf := cell.NewBuffer()
		buf.WriteString(snippet)
		fs := &testFoldsService{}
		fs.view = buf.WithView(fs)
		var wg sync.WaitGroup
		var mu sync.Mutex
		cb := func(fn func()) bool {
			// run async to guarantee we can lock below:
			// sometimes this cb can run in the main goroutine
			go func() {
				defer wg.Done()
				mu.Lock()
				defer mu.Unlock()
				fn()
			}()
			return true
		}
		wg.Add(1)
		mu.Lock()
		cfg := text.StatusBarConfig{
			Publisher: &texttest.TestEditor{},
			ScheduleNextTick: func(cb func()) bool {
				cb()
				return true
			},
		}
		h := NewHandler(buf, uri,
			'\t', 0,
			WithHideInitialFolds(true),
			WithAutoCenter(true),
			WithScheduleNextTick(cb),
		)
		bar := text.WithStatusBar(h, buf, h.(*standardHandler).less.Scroll(),
			false, false, cfg)
		bar.Resize(20, 10)
		require.True(t, h.SetCursorAtScroll(term.Coordinates{Y: 7}))
		mu.Unlock()

		tests := []comptest.TestCase{
			{Expected: `
                    
/* [4 lines] */     
    void            
diff_buf_adjust(win_
{                   
    win_T    *wp;   
    int             
                    
    if (!win->w_p_di
                    `,
			},
		}

		wg.Wait()
		w := term.NewStringWriter(20, 10)

		mu.Lock()
		defer mu.Unlock()

		comptest.TestComponent(t, bar, w, tests)
		pos := h.CursorAtScroll()
		assert.Equal(t, pos, term.Coordinates{Y: 7})
	})
}

func TestFindUsesStatusBarAcrossEditorChrome(t *testing.T) {
	tests := []struct {
		name    string
		content string
		cases   []handlertest.SequenceTestCase
	}{
		{
			name:    "refinement navigation failure and acceptance",
			content: "foo x foo\n界 foo\nlast",
			cases: []handlertest.SequenceTestCase{
				{InputSequence: "<meta-f>", Expected: "  1 ▐oo x foo             \n" +
					"  1 界  foo                \n" +
					"  2 last                  \n" +
					"  3                       \n" +
					"Find:                     "},
				{InputSequence: "foo", Expected: "  1 foo▐x foo             \n" +
					"  1 界  foo                \n" +
					"  2 last                  \n" +
					"  3                       \n" +
					"Find: foo  1/3            "},
				{InputSequence: "<enter>", Expected: "  1 foo x foo▐            \n" +
					"  1 界  foo                \n" +
					"  2 last                  \n" +
					"  3                       \n" +
					"Find: foo  2/3            "},
				{InputSequence: "<shift-enter>", Expected: "  1 foo▐x foo             \n" +
					"  1 界  foo                \n" +
					"  2 last                  \n" +
					"  3                       \n" +
					"Find: foo  1/3            "},
				{InputSequence: "z", Expected: "  1 ▐oo x foo             \n" +
					"  1 界  foo                \n" +
					"  2 last                  \n" +
					"  3                       \n" +
					"Find: fooz  no matches    "},
				{InputSequence: "<backspace>", Expected: "  1 foo▐x foo             \n" +
					"  1 界  foo                \n" +
					"  2 last                  \n" +
					"  3                       \n" +
					"Find: foo  1/3            "},
				{InputSequence: "<esc>", Expected: "  1 foo▐x foo             \n" +
					"  1 界  foo                \n" +
					"  2 last                  \n" +
					"  3                       \n" +
					"                          "},
			},
		},
		{
			name:    "wide queries spaces wrapping and empty query",
			content: "界 foo bar\nfoo bar 界\nend",
			cases: []handlertest.SequenceTestCase{
				{InputSequence: "<ctrl-f>界", Expected: "  1 界 ▐foo bar            \n" +
					"  1 foo bar 界             \n" +
					"  2 end                   \n" +
					"  3                       \n" +
					"Find: 界   1/2             "},
				{InputSequence: "<enter>", Expected: "  1 界  foo bar            \n" +
					"  2 foo bar 界 ▐           \n" +
					"  1 end                   \n" +
					"  2                       \n" +
					"Find: 界   2/2             "},
				{InputSequence: "<shift-enter>", Expected: "  1 界 ▐foo bar            \n" +
					"  1 foo bar 界             \n" +
					"  2 end                   \n" +
					"  3                       \n" +
					"Find: 界   1/2             "},
				{InputSequence: "<backspace>", Expected: "  1 ▐  foo bar            \n" +
					"  1 foo bar 界             \n" +
					"  2 end                   \n" +
					"  3                       \n" +
					"Find:                     "},
				{InputSequence: "foo<space>bar", Expected: "  1 界  foo bar▐           \n" +
					"  1 foo bar 界             \n" +
					"  2 end                   \n" +
					"  3                       \n" +
					"Find: foo bar  1/2        "},
				{InputSequence: "<enter>", Expected: "  1 界  foo bar            \n" +
					"  2 foo bar▐界             \n" +
					"  1 end                   \n" +
					"  2                       \n" +
					"Find: foo bar  2/2        "},
				{InputSequence: "q", Expected: "  1 ▐  foo bar            \n" +
					"  1 foo bar 界             \n" +
					"  2 end                   \n" +
					"  3                       \n" +
					"Find: foo barq  no matches"},
				{InputSequence: "<backspace><esc>", Expected: "  1 界  foo bar▐           \n" +
					"  1 foo bar 界             \n" +
					"  2 end                   \n" +
					"  3                       \n" +
					"                          "},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newStatusFindIntegrationHandler(t, tt.content)
			handlertest.RunHandlerSequence(t, h, 26, 5, tt.cases)
		})
	}
}

func newStatusFindIntegrationHandler(t *testing.T, content string) text.Handler {
	t.Helper()
	buf := cell.NewBuffer()
	buf.WriteString(content)
	uri, err := workspaceapi.ParseURI("memory:///find-status.txt")
	require.NoError(t, err)
	root := NewHandler(buf, uri, '\t', 0,
		WithBarAttr(term.Attributes{Fg: term.ColorBlack, Bg: term.ColorWhite}),
		WithCommandBar(true),
	)
	standard := root.(*standardHandler)
	scroll := standard.less.Scroll()
	var h text.Handler = text.WithAuxBar(root, buf, scroll, text.AuxBarConfig{
		LinesEnabled:     true,
		ScheduleNextTick: func(fn func()) bool { fn(); return true },
	})
	h = text.WithIconsBar(nil, false, h, buf, scroll, text.IconsBarConfig{
		ScheduleNextTick: func(fn func()) bool { fn(); return true },
	})
	bar := text.WithStatusBar(h, buf, scroll, false, false, text.StatusBarConfig{
		Publisher:        &texttest.TestEditor{},
		ScheduleNextTick: func(fn func()) bool { fn(); return true },
		Layout:           []text.StatusBarComponent{{Type: text.StatusBarStatus, Template: "%s"}},
	})
	standard.setStatusBar(bar)
	return bar
}

var _ = (foldsService)(testFoldsService{})

type testFoldsService struct {
	view cell.View
}

func (f testFoldsService) Rows() int {
	return f.view.Rows()
}

func (f testFoldsService) Columns(row int) int {
	return f.view.Columns(row)
}

func (f testFoldsService) Cell(at term.Coordinates) (term.Cell, bool) {
	return f.view.Cell(at)
}

func (f testFoldsService) RawCells() [][]term.Cell {
	return f.view.RawCells()
}

func (f testFoldsService) String() string {
	return f.view.String()
}

func (f testFoldsService) FoldsFrom(pos term.Coordinates) (
	iterator.Iterator[term.Range], bool,
) {
	folds, ok := f.Folds()
	if !ok {
		return nil, false
	}
	return iterator.Filter(folds, func(rng term.Range) bool {
		return rng.End.Y > pos.Y || (rng.Start.Y == pos.Y && rng.End.X > pos.X)
	}), true
}

func (f testFoldsService) Folds() (iterator.Iterator[term.Range], bool) {
	return iterator.FromSlice([]term.Range{
		{Start: term.Coordinates{Y: 1, X: 0}, End: term.Coordinates{Y: 4}},
		{Start: term.Coordinates{Y: 2, X: 3}, End: term.Coordinates{Y: 3, X: 15}},
		{Start: term.Coordinates{Y: 7, X: 0}, End: term.Coordinates{Y: 31, X: 0}},
		{Start: term.Coordinates{Y: 12, X: 3}, End: term.Coordinates{Y: 28, X: 3}},
		{Start: term.Coordinates{Y: 19, X: 6}, End: term.Coordinates{Y: 27, X: 6}},
		{Start: term.Coordinates{Y: 22, X: 6}, End: term.Coordinates{Y: 26, X: 9}},
		{Start: term.Coordinates{Y: 29, X: 3}, End: term.Coordinates{Y: 30, X: 3}},
	}), true
}

func (f testFoldsService) InitialFolds() (iterator.Iterator[term.Range], bool) {
	return iterator.FromSlice([]term.Range{
		{Start: term.Coordinates{Y: 1, X: 0}, End: term.Coordinates{Y: 4}},
	}), true
}

const snippet = `
/*
 * Check if the current buffer should be added to or removed from the list of
 * diff buffers.
 */
	void
diff_buf_adjust(win_T *win)
{
	win_T	*wp;
	int				i;

	if (!win->w_p_diff)
	{
	/* When there is no window showing a diff for this buffer, remove
	 * it from the diffs... */
	FOR_ALL_WINDOWS(wp)
		if (wp->w_buffer == win->w_buffer && wp->w_p_diff)
		break;
	if (wp == NULL)
	{
		i = diff_buf_idx(win->w_buffer);
		if (i != DB_COUNT)
		{
		curtab->tp_diffbuf[i] = NULL;
		curtab->tp_diff_invalid = TRUE;
		diff_redraw(TRUE);
		}
	}
	}
	else
	diff_buf_add(win->w_buffer);
}`

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

package emacs

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
		bar := text.WithStatusBar(h, buf, h.(*emacsHandler).less.Scroll(),
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
		// the bars on top of a bare emacs handler directly.
		root := NewHandler(buf, uri, '\t', 0,
			WithScheduleNextTick(cb),
			WithAutoCenter(true),
		)
		scroll := root.(*emacsHandler).less.Scroll()
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
				Mod: term.ModAlt, Ch: '>'})
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
				Mod: term.ModAlt, Ch: '<'})
			require.True(t, handled)
			_, handled = h.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowDown})
			require.True(t, handled)
			_, handled = h.Handle(term.Event{Type: term.EventKey,
				Mod: term.ModCtrl, Ch: 'Z'})
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
		bar := text.WithStatusBar(h, buf, h.(*emacsHandler).less.Scroll(),
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

func TestIncrementalSearchSplitsMessageAndModeAcrossEditorChrome(t *testing.T) {
	tests := []struct {
		name    string
		content string
		cases   []handlertest.SequenceTestCase
	}{
		{
			name:    "forward refinement navigation failure and acceptance",
			content: "foo x foo\n界 foo\nlast",
			cases: []handlertest.SequenceTestCase{
				{InputSequence: "<ctrl-s>", Expected: "  1 ▐oo x foo             \n" +
					"  1 界  foo                \n" +
					"  2 last                  \n" +
					"  3             I-search: \n" +
					"ISEARCH                   "},
				{InputSequence: "foo", Expected: "  1 foo▐x foo             \n" +
					"  1 界  foo                \n" +
					"  2 last                  \n" +
					"  3          I-search: foo\n" +
					"ISEARCH                   "},
				{InputSequence: "<ctrl-s>", Expected: "  1 foo x foo▐            \n" +
					"  1 界  foo                \n" +
					"  2 last                  \n" +
					"  3          I-search: foo\n" +
					"ISEARCH                   "},
				{InputSequence: "z", Expected: "  1 ▐oo x foo             \n" +
					"  1 界  foo                \n" +
					"  2 last                  \n" +
					"  3 Failing I-search: fooz\n" +
					"ISEARCH                   "},
				{InputSequence: "<backspace>", Expected: "  1 foo▐x foo             \n" +
					"  1 界  foo                \n" +
					"  2 last                  \n" +
					"  3          I-search: foo\n" +
					"ISEARCH                   "},
				{InputSequence: "<enter>", Expected: "  1 foo▐x foo             \n" +
					"  1 界  foo                \n" +
					"  2 last                  \n" +
					"  3                       \n" +
					"                          "},
			},
		},
		{
			name:    "wide queries spaces direction changes and abort",
			content: "界 foo bar\nfoo bar 界\nend",
			cases: []handlertest.SequenceTestCase{
				{InputSequence: "<ctrl-s>界", Expected: "  1 界 ▐foo bar            \n" +
					"  1 foo bar 界             \n" +
					"  2 end                   \n" +
					"  3           I-search: 界 \n" +
					"ISEARCH                   "},
				{InputSequence: "<ctrl-s>", Expected: "  1 界  foo bar            \n" +
					"  2 foo bar 界 ▐           \n" +
					"  1 end                   \n" +
					"  2           I-search: 界 \n" +
					"ISEARCH                   "},
				{InputSequence: "<ctrl-r>", Expected: "  1 ▐  foo bar            \n" +
					"  1 foo bar 界             \n" +
					"  2 end                   \n" +
					"  3  I-search backward: 界 \n" +
					"ISEARCH                   "},
				{InputSequence: "<backspace>", Expected: "  1 ▐  foo bar            \n" +
					"  1 foo bar 界             \n" +
					"  2 end                   \n" +
					"  3    I-search backward: \n" +
					"ISEARCH                   "},
				{InputSequence: "foo<space>bar", Expected: "  1 界  foo bar            \n" +
					"  2 ▐oo bar 界             \n" +
					"  1 I-search backward: foo\n" +
					"  2  bar                  \n" +
					"ISEARCH                   "},
				{InputSequence: "<ctrl-s>", Expected: "  1 界  foo bar▐           \n" +
					"  1 foo bar 界             \n" +
					"  2 end                   \n" +
					"  3      I-search: foo bar\n" +
					"ISEARCH                   "},
				{InputSequence: "q", Expected: "  1 ▐  foo bar            \n" +
					"  1 foo bar 界             \n" +
					"  2 Failing I-search: foo \n" +
					"  3 barq                  \n" +
					"ISEARCH                   "},
				{InputSequence: "<ctrl-g>", Expected: "  1 ▐  foo bar            \n" +
					"  1 foo bar 界             \n" +
					"  2 end                   \n" +
					"  3                       \n" +
					"                          "},
			},
		},
		{
			name:    "tabs and nulls in the buffer",
			content: "a\tb foo\n\x00foo\x00\nfoo\tbar",
			cases: []handlertest.SequenceTestCase{
				{InputSequence: "<ctrl-s>foo", Expected: "  1 a    b foo▐           \n" +
					"  1  foo                  \n" +
					"  2 foo    bar            \n" +
					"  3          I-search: foo\n" +
					"ISEARCH                   "},
				{InputSequence: "<ctrl-s>", Expected: "  1 a    b foo            \n" +
					"  2  foo▐                 \n" +
					"  1 foo    bar            \n" +
					"  2          I-search: foo\n" +
					"ISEARCH                   "},
				{InputSequence: "<ctrl-s>", Expected: "  2 a    b foo            \n" +
					"  1  foo                  \n" +
					"  3 foo   ▐bar            \n" +
					"  1          I-search: foo\n" +
					"ISEARCH                   "},
				{InputSequence: "<enter>", Expected: "  2 a    b foo            \n" +
					"  1  foo                  \n" +
					"  3 foo   ▐bar            \n" +
					"  1                       \n" +
					"                          "},
			},
		},
		{
			name:    "wide runes and tabs interleaved",
			content: "界\t界 foo\n\t界界\nfoo",
			cases: []handlertest.SequenceTestCase{
				{InputSequence: "<ctrl-s>界界", Expected: "  1 界     界  foo          \n" +
					"  2     界 界 ▐             \n" +
					"  1 foo                   \n" +
					"  2         I-search: 界 界 \n" +
					"ISEARCH                   "},
				{InputSequence: "<backspace>", Expected: "  1 界    ▐界  foo          \n" +
					"  1     界 界               \n" +
					"  2 foo                   \n" +
					"  3           I-search: 界 \n" +
					"ISEARCH                   "},
				{InputSequence: "<ctrl-g>", Expected: "  1 ▐     界  foo          \n" +
					"  1     界 界               \n" +
					"  2 foo                   \n" +
					"  3                       \n" +
					"                          "},
			},
		},
		{
			name:    "empty buffer",
			content: "",
			cases: []handlertest.SequenceTestCase{
				{InputSequence: "<ctrl-s>", Expected: "  1 ▐                     \n" +
					"  1                       \n" +
					"  2                       \n" +
					"  3             I-search: \n" +
					"ISEARCH                   "},
				{InputSequence: "foo", Expected: "  1 ▐                     \n" +
					"  1                       \n" +
					"  2                       \n" +
					"  3  Failing I-search: foo\n" +
					"ISEARCH                   "},
				{InputSequence: "<ctrl-g>", Expected: "  1 ▐                     \n" +
					"  1                       \n" +
					"  2                       \n" +
					"  3                       \n" +
					"                          "},
			},
		},
		{
			name:    "control keys inside the query",
			content: "a\tb\nab\nend",
			cases: []handlertest.SequenceTestCase{
				{InputSequence: "<ctrl-s>", Expected: "  1 ▐    b                \n" +
					"  1 ab                    \n" +
					"  2 end                   \n" +
					"  3             I-search: \n" +
					"ISEARCH                   "},
				{InputSequence: "a", Expected: "  1 a   ▐b                \n" +
					"  1 ab                    \n" +
					"  2 end                   \n" +
					"  3            I-search: a\n" +
					"ISEARCH                   "},
				{InputSequence: "<tab>", Expected: "  1 a       ▐b            \n" +
					"  1 ab                    \n" +
					"  2 end                   \n" +
					"  3                       \n" +
					"                          "},
				{InputSequence: "b", Expected: "  1 a    b   ▐b           \n" +
					"  1 ab                    \n" +
					"  2 end                   \n" +
					"  3                       \n" +
					"                          "},
				{InputSequence: "<ctrl-g>", Expected: "  1 a    b   ▐b           \n" +
					"  1 ab                    \n" +
					"  2 end                   \n" +
					"  3                       \n" +
					"                          "},
			},
		},
		{
			name:    "query longer than the editor width",
			content: "supercalifragilistic\nother",
			cases: []handlertest.SequenceTestCase{
				{InputSequence: "<ctrl-s>supercalifragilistic", Expected: "  1 supercalifragilistic▐ \n" +
					"  1 other                 \n" +
					"  2 I-search: supercalifra\n" +
					"  3 gilistic              \n" +
					"ISEARCH                   "},
				{InputSequence: "<backspace>", Expected: "  1 supercalifragilisti▐  \n" +
					"  1 other                 \n" +
					"  2 I-search: supercalifra\n" +
					"  3 gilisti               \n" +
					"ISEARCH                   "},
				{InputSequence: "<enter>", Expected: "  1 supercalifragilisti▐  \n" +
					"  1 other                 \n" +
					"  2                       \n" +
					"  3                       \n" +
					"                          "},
			},
		}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newStatusIsearchIntegrationHandler(t, tt.content)
			handlertest.RunHandlerSequence(t, h, 26, 5, tt.cases)
		})
	}
}

// TestTransientModesSplitMessageAndModeAcrossEditorChrome covers the remaining
// transient states. M-% is not expressible as a handlertest input sequence, so
// scenarios that need it seed the state through prelude events.
func TestTransientModesSplitMessageAndModeAcrossEditorChrome(t *testing.T) {
	tests := []struct {
		name    string
		content string
		prelude []term.Event
		cases   []handlertest.SequenceTestCase
	}{
		{
			name:    "query replace decision loop",
			content: "foo bar\n界 foo\nfoo qux",
			prelude: []term.Event{alt('%')},
			cases: []handlertest.SequenceTestCase{
				{InputSequence: "", Expected: "  1 ▐oo bar               \n" +
					"  1 界  foo                \n" +
					"  2 foo qux               \n" +
					"  3        Query replace: \n" +
					"QUERY                     "},
				{InputSequence: "foo", Expected: "  1 ▐oo bar               \n" +
					"  1 界  foo                \n" +
					"  2 foo qux               \n" +
					"  3     Query replace: foo\n" +
					"QUERY                     "},
				{InputSequence: "<enter>", Expected: "  1 ▐oo bar               \n" +
					"  1 界  foo                \n" +
					"  2 Query replace foo with\n" +
					"  3 :                     \n" +
					"QUERY                     "},
				{InputSequence: "界", Expected: "  1 ▐oo bar               \n" +
					"  1 界  foo                \n" +
					"  2 Query replace foo with\n" +
					"  3 : 界                   \n" +
					"QUERY                     "},
				{InputSequence: "<enter>", Expected: "  1 ▐oo bar               \n" +
					"  1 界  foo                \n" +
					"  2 Query replacing foo wi\n" +
					"  3 th 界  (y/n/!/./q)     \n" +
					"QUERY                     "},
				{InputSequence: "y", Expected: "  1 界  bar                \n" +
					"  2 界  ▐oo                \n" +
					"  1 Query replacing foo wi\n" +
					"  2 th 界  (y/n/!/./q)     \n" +
					"QUERY                     "},
				{InputSequence: "n", Expected: "  1 界  foo                \n" +
					"  3 ▐oo qux               \n" +
					"  1 Query replacing foo wi\n" +
					"  2 th 界  (y/n/!/./q)     \n" +
					"QUERY                     "},
				{InputSequence: "q", Expected: "  1 界  foo                \n" +
					"  3 ▐oo qux               \n" +
					"  1                       \n" +
					"  2                   Done\n" +
					"                          "},
			},
		},
		{
			name:    "zap to char",
			content: "alpha beta gamma",
			cases: []handlertest.SequenceTestCase{
				{InputSequence: "<alt-z>", Expected: "  1 ▐lpha beta gamma      \n" +
					"  1                       \n" +
					"  2                       \n" +
					"  3          Zap to char: \n" +
					"ZAP                       "},
				{InputSequence: "a", Expected: "  1 ▐pha beta gamma       \n" +
					"  1                       \n" +
					"  2                       \n" +
					"  3                       \n" +
					"                          "},
			},
		},
		{
			name:    "zap aborted",
			content: "alpha beta",
			cases: []handlertest.SequenceTestCase{
				{InputSequence: "<alt-z>", Expected: "  1 ▐lpha beta            \n" +
					"  1                       \n" +
					"  2                       \n" +
					"  3          Zap to char: \n" +
					"ZAP                       "},
				{InputSequence: "<ctrl-g>", Expected: "  1 ▐lpha beta            \n" +
					"  1                       \n" +
					"  2                       \n" +
					"  3                       \n" +
					"                          "},
			},
		},
		{
			name:    "goto line",
			content: "one\ntwo\nthree\nfour",
			cases: []handlertest.SequenceTestCase{
				{InputSequence: "<alt-g>", Expected: "  1 ▐ne                   \n" +
					"  1 two                   \n" +
					"  2 three                 \n" +
					"  3 four                  \n" +
					"GOTO                      "},
				{InputSequence: "g", Expected: "  1 ▐ne                   \n" +
					"  1 two                   \n" +
					"  2 three                 \n" +
					"  3            Goto line: \n" +
					"GOTO                      "},
				{InputSequence: "3", Expected: "  1 ▐ne                   \n" +
					"  1 two                   \n" +
					"  2 three                 \n" +
					"  3           Goto line: 3\n" +
					"GOTO                      "},
				{InputSequence: "<enter>", Expected: "  2 one                   \n" +
					"  1 two                   \n" +
					"  3 ▐hree                 \n" +
					"  1 four                  \n" +
					"                          "},
			},
		},
		{
			name:    "goto prefix aborted",
			content: "one\ntwo",
			cases: []handlertest.SequenceTestCase{
				{InputSequence: "<alt-g>", Expected: "  1 ▐ne                   \n" +
					"  1 two                   \n" +
					"  2                       \n" +
					"  3                       \n" +
					"GOTO                      "},
				{InputSequence: "x", Expected: "  1 ▐ne                   \n" +
					"  1 two                   \n" +
					"  2                       \n" +
					"  3                       \n" +
					"                          "},
			},
		},
		{
			name:    "prefix argument aborted",
			content: "abc\ndef",
			cases: []handlertest.SequenceTestCase{
				{InputSequence: "<ctrl-u>", Expected: "  1 ▐bc                   \n" +
					"  1 def                   \n" +
					"  2                       \n" +
					"  3                    C-u\n" +
					"ARG                       "},
				{InputSequence: "4", Expected: "  1 ▐bc                   \n" +
					"  1 def                   \n" +
					"  2                       \n" +
					"  3                  C-u 4\n" +
					"ARG                       "},
				{InputSequence: "<ctrl-g>", Expected: "  1 ▐bc                   \n" +
					"  1 def                   \n" +
					"  2                       \n" +
					"  3                   Quit\n" +
					"                          "},
			},
		},
		{
			name:    "prefix argument consumed",
			content: "abc\ndef",
			cases: []handlertest.SequenceTestCase{
				{InputSequence: "<ctrl-u>", Expected: "  1 ▐bc                   \n" +
					"  1 def                   \n" +
					"  2                       \n" +
					"  3                    C-u\n" +
					"ARG                       "},
				{InputSequence: "3", Expected: "  1 ▐bc                   \n" +
					"  1 def                   \n" +
					"  2                       \n" +
					"  3                  C-u 3\n" +
					"ARG                       "},
				{InputSequence: "z", Expected: "  1 zzz▐bc                \n" +
					"  1 def                   \n" +
					"  2                       \n" +
					"  3                       \n" +
					"                          "},
			},
		}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newStatusIsearchIntegrationHandler(t, tt.content)
			h.Resize(26, 5)
			for _, ev := range tt.prelude {
				h.Handle(ev)
			}
			handlertest.RunHandlerSequence(t, h, 26, 5, tt.cases)
		})
	}
}

func newStatusIsearchIntegrationHandler(t *testing.T, content string) text.Handler {
	t.Helper()
	buf := cell.NewBuffer()
	buf.WriteString(content)
	uri, err := workspaceapi.ParseURI("memory:///isearch-status.txt")
	require.NoError(t, err)
	root := NewHandler(buf, uri, '\t', 0,
		WithBarAttr(term.Attributes{Fg: term.ColorBlack, Bg: term.ColorWhite}),
		WithCommandBar(true),
	).(*emacsHandler)
	scroll := root.less.Scroll()
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
	root.setStatusBar(bar)
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

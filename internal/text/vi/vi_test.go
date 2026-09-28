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

package vi

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/component/comptest"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/handler/handlertest"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/registerset"
	"unstable.build/rune/internal/text/texttest"
)

var uri workspaceapi.URI

func init() {
	var err error
	uri, err = workspaceapi.ParseURI("file:///vi_test")
	if err != nil {
		panic(err)
	}
}

func TestCursorExternalEdit(t *testing.T) {
	t.Run("if external insert above, moves cursor to keep cursor in current logical line", func(t *testing.T) {
		width, height := 20, 10

		buf := cell.NewBuffer()
		buf.ReadFrom(strings.NewReader(snippet))
		vi := New(buf, uri)
		vi.Resize(width, height)

		_, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: 'j'})
		require.True(t, handled)
		_, handled = vi.Handle(term.Event{Type: term.EventKey, Ch: 'j'})
		require.True(t, handled)

		assert.Equal(t, term.Coordinates{Y: 2}, vi.CursorAtScroll())
		at := term.Coordinates{}
		vi.CellEditor().Edit(context.Background(), at, at, "a\nb")
		assert.Equal(t, term.Coordinates{Y: 3}, vi.CursorAtScroll())
	})

	t.Run("if external insert below, it does nothing", func(t *testing.T) {
		width, height := 20, 10

		buf := cell.NewBuffer()
		buf.ReadFrom(strings.NewReader(snippet))
		vi := New(buf, uri)
		vi.Resize(width, height)

		_, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: 'j'})
		require.True(t, handled)
		_, handled = vi.Handle(term.Event{Type: term.EventKey, Ch: 'j'})
		require.True(t, handled)

		assert.Equal(t, term.Coordinates{Y: 2}, vi.CursorAtScroll())
		at := term.Coordinates{Y: 3}
		vi.CellEditor().Edit(context.Background(), at, at, "a\nb")
		assert.Equal(t, term.Coordinates{Y: 2}, vi.CursorAtScroll())
	})

	t.Run("if external delete below, it does nothing", func(t *testing.T) {
		width, height := 20, 10

		buf := cell.NewBuffer()
		buf.ReadFrom(strings.NewReader(snippet))
		vi := New(buf, uri)
		vi.Resize(width, height)

		_, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: 'j'})
		require.True(t, handled)
		_, handled = vi.Handle(term.Event{Type: term.EventKey, Ch: 'j'})
		require.True(t, handled)

		assert.Equal(t, term.Coordinates{Y: 2}, vi.CursorAtScroll())
		from := term.Coordinates{Y: 3}
		to := term.Coordinates{Y: 4}
		vi.CellEditor().Edit(context.Background(), from, to, "")
		assert.Equal(t, term.Coordinates{Y: 2}, vi.CursorAtScroll())
	})

	t.Run("if external delete above, it keeps cursor at logical line", func(t *testing.T) {
		width, height := 20, 10

		buf := cell.NewBuffer()
		buf.ReadFrom(strings.NewReader(snippet))
		vi := New(buf, uri)
		vi.Resize(width, height)

		_, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: 'j'})
		require.True(t, handled)
		_, handled = vi.Handle(term.Event{Type: term.EventKey, Ch: 'j'})
		require.True(t, handled)

		assert.Equal(t, term.Coordinates{Y: 2}, vi.CursorAtScroll())
		from := term.Coordinates{Y: 0}
		to := term.Coordinates{Y: 1}
		vi.CellEditor().Edit(context.Background(), from, to, "")
		assert.Equal(t, term.Coordinates{Y: 1}, vi.CursorAtScroll())
	})

	t.Run("if external delete to current line, it keeps cursor at logical line", func(t *testing.T) {
		width, height := 20, 10

		buf := cell.NewBuffer()
		buf.ReadFrom(strings.NewReader(snippet))
		vi := New(buf, uri)
		vi.Resize(width, height)

		_, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: 'j'})
		require.True(t, handled)
		_, handled = vi.Handle(term.Event{Type: term.EventKey, Ch: 'j'})
		require.True(t, handled)
		_, handled = vi.Handle(term.Event{Type: term.EventKey, Ch: 'l'})
		require.True(t, handled)

		assert.Equal(t, term.Coordinates{Y: 2, X: 1}, vi.CursorAtScroll())
		from := term.Coordinates{Y: 0}
		to := term.Coordinates{Y: 2, X: 5}
		vi.CellEditor().Edit(context.Background(), from, to, "")
		assert.Equal(t, term.Coordinates{Y: 0, X: 0}, vi.CursorAtScroll())
	})

	t.Run("if external insert to current line, it keeps cursor at logical line", func(t *testing.T) {
		width, height := 20, 10

		buf := cell.NewBuffer()
		buf.ReadFrom(strings.NewReader(snippet))
		vi := New(buf, uri)
		vi.Resize(width, height)

		_, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: 'j'})
		require.True(t, handled)
		_, handled = vi.Handle(term.Event{Type: term.EventKey, Ch: 'j'})
		require.True(t, handled)
		_, handled = vi.Handle(term.Event{Type: term.EventKey, Ch: 'l'})
		require.True(t, handled)

		assert.Equal(t, term.Coordinates{Y: 2, X: 1}, vi.CursorAtScroll())
		at := term.Coordinates{Y: 2, X: 0}
		vi.CellEditor().Edit(context.Background(), at, at, "a\nbbb")
		assert.Equal(t, term.Coordinates{Y: 3, X: 3}, vi.CursorAtScroll())
	})

	t.Run("if external replace to current line, it keeps cursor at logical line", func(t *testing.T) {
		width, height := 20, 10

		buf := cell.NewBuffer()
		buf.ReadFrom(strings.NewReader(snippet))
		vi := New(buf, uri)
		vi.Resize(width, height)

		_, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: 'j'})
		require.True(t, handled)
		_, handled = vi.Handle(term.Event{Type: term.EventKey, Ch: 'j'})
		require.True(t, handled)
		_, handled = vi.Handle(term.Event{Type: term.EventKey, Ch: 'l'})
		require.True(t, handled)

		assert.Equal(t, term.Coordinates{Y: 2, X: 1}, vi.CursorAtScroll())
		start := term.Coordinates{Y: 1, X: 0}
		end := term.Coordinates{Y: 2, X: 1}
		vi.CellEditor().Edit(context.Background(), start, end, "a\nbbb")
		assert.Equal(t, term.Coordinates{Y: 2, X: 3}, vi.CursorAtScroll())
	})

	t.Run("if external replace only cols to current line, it keeps cursor at logical position", func(t *testing.T) {
		width, height := 20, 10

		buf := cell.NewBuffer()
		buf.ReadFrom(strings.NewReader(snippet))
		vi := New(buf, uri)
		vi.Resize(width, height)

		_, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: 'j'})
		require.True(t, handled)
		_, handled = vi.Handle(term.Event{Type: term.EventKey, Ch: 'j'})
		require.True(t, handled)
		for range 3 {
			_, handled = vi.Handle(term.Event{Type: term.EventKey, Ch: 'l'})
			require.True(t, handled)
		}

		assert.Equal(t, term.Coordinates{Y: 2, X: 3}, vi.CursorAtScroll())
		start := term.Coordinates{Y: 2, X: 0}
		end := term.Coordinates{Y: 2, X: 4}
		vi.CellEditor().Edit(context.Background(), start, end, "bbb")
		assert.Equal(t, term.Coordinates{Y: 2, X: 3}, vi.CursorAtScroll())
	})
}

func TestFoldsIntegration(t *testing.T) {
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
		wg.Add(2)
		mu.Lock()
		vi := New(buf, uri,
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
		bar := text.WithStatusBar(vi, buf, vi.less.Scroll(),
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
		// the bars on top of a bare vi handler directly.
		root := New(buf, uri,
			WithScheduleNextTick(cb),
			WithAutoCenter(true),
		)
		scroll := root.less.Scroll()
		auxCfg := text.AuxBarConfig{FoldsEnabled: true, ScheduleNextTick: cb}
		statusCfg := text.StatusBarConfig{
			Publisher: &texttest.TestEditor{},
			ScheduleNextTick: func(cb func()) bool {
				cb()
				return true
			},
		}
		wg.Add(1)
		var vi text.Handler = text.WithAuxBar(root, buf, scroll, auxCfg)
		vi = text.WithStatusBar(vi, buf, scroll, false, false, statusCfg)
		root.setStatusBar(vi.(*text.StatusBar))
		vi.Resize(50, 10)
		mu.Unlock()
		wg.Wait() // wait for bar

		t.Run("zA", func(t *testing.T) {
			tests := []comptest.TestCase{
				{Expected: `
                                                  
 /* [4 lines] */                                 
      void                                        
  diff_buf_adjust(win_T *win)                     
 { [25 lines] }                                  
                                                  
                                                  
                                                  
                                                  
                                            NORMAL`,
				},
			}

			wg.Add(6)
			for _, ch := range "GzAkklhj" {
				mu.Lock()
				_, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: ch})
				mu.Unlock()
				require.True(t, handled)
			}
			wg.Wait()
			w := term.NewStringWriter(50, 10)
			mu.Lock()
			comptest.TestComponent(t, vi, w, tests)
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
                                                  
                                            NORMAL`,
				},
			}

			wg.Add(2)
			for _, ch := range "ggjzo" {
				mu.Lock()
				_, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: ch})
				mu.Unlock()
				require.True(t, handled)
			}
			wg.Wait()
			w := term.NewStringWriter(50, 10)
			mu.Lock()
			comptest.TestComponent(t, vi, w, tests)
			mu.Unlock()
		})

		t.Run("moving up and down after hidding/making visible", func(t *testing.T) {
			t.SkipNow()
			tests := []comptest.TestCase{
				{Expected: `
                                                  
 /*                                              
  * Check if the current buffer should be added t
   * diff buffers.                                
   */                                             
      void                                        
  diff_buf_adjust(win_T *win)                     
 {                                               
      win_T    *wp;                               
                                                  `,
				},
			}

			wg.Add(10)
			for _, ch := range "jjjjzAkzo" { // goes and stays at "void" line, move up again and unfold
				mu.Lock()
				_, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: ch})
				mu.Unlock()
				require.True(t, handled)
			}
			wg.Wait()
			w := term.NewStringWriter(50, 10)
			mu.Lock()
			comptest.TestComponent(t, vi, w, tests)
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
		wg.Add(2)
		mu.Lock()
		cfg := text.StatusBarConfig{
			Publisher: &texttest.TestEditor{},
			ScheduleNextTick: func(cb func()) bool {
				cb()
				return true
			},
		}
		vi := New(buf, uri,
			WithHideInitialFolds(true),
			WithAutoCenter(true),
			WithScheduleNextTick(cb),
		)
		bar := text.WithStatusBar(vi, buf, vi.less.Scroll(),
			false, false, cfg)
		bar.Resize(20, 10)
		require.True(t, vi.SetCursorAtScroll(term.Coordinates{Y: 7}))
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
		pos := vi.CursorAtScroll()
		assert.Equal(t, pos, term.Coordinates{Y: 7})
	})
}

func TestSetCursorAtScrollCenter(t *testing.T) {
	buf := cell.NewBuffer()
	buf.WriteString(snippet)
	vi := New(buf, uri,
		WithAutoCenter(true),
	)
	vi.Resize(20, 10)
	vi.SetCursorAtScroll(term.Coordinates{Y: 10, X: 5})

	cases := []handlertest.SequenceTestCase{
		{"",
			`    void            
diff_buf_adjust(win_
{                   
    win_T    *wp;   
    int             
▐                   
    if (!win->w_p_di
    {               
    /* When there is
     * it from the d`},
		{"j", // free mark works after SetCursorAtScroll
			`    void            
diff_buf_adjust(win_
{                   
    win_T    *wp;   
    int             
                    
   ▐if (!win->w_p_di
    {               
    /* When there is
     * it from the d`},
	}

	handlertest.TestHandlerSequence(t, vi, 20, 10, cases)
}

type mockHandler struct {
	texttest.MockHandler
	h viHandlerImpl // used for mode parsing only

	received []term.Event
}

func newMockHandler(buf *cell.Buffer) (ret *mockHandler) {
	ret = new(mockHandler)
	config := defaultviHandlerImplConfig()
	ret.h.init(buf, config)
	return ret
}

func (h *mockHandler) setStatusBar(bar statusBar) {
}

func (h *mockHandler) Resize(width, height int) {
	h.h.Resize(width, height)
}

func (h *mockHandler) setNormalMode() bool {
	return false
}

func (h *mockHandler) unselect() bool {
	return false
}

func (h *mockHandler) copySuppressed() bool {
	return false
}

func (h *mockHandler) search(string) {
}

func (h *mockHandler) Handle(ev term.Event) (bool, bool) {
	h.received = append(h.received, ev)
	switch ev.Ch {
	case '#': // map for convenience
		ev = term.Event{Type: term.EventKey, Key: term.KeyEsc}
	case '&':
		ev = term.Event{Type: term.EventKey, Key: term.KeyEnter}
	}
	h.h.Handle(ev)
	return false, true
}

func (h *mockHandler) mode() viMode {
	return h.h.mode()
}
func (h *mockHandler) moveToNextLocation(ID string) bool {
	return false
}
func (h *mockHandler) moveToPrevLocation(ID string) bool {
	return false
}
func (h *mockHandler) setLocationList(pri textapi.LocationPriority, ID string, l text.LocationList) {
}
func (h *mockHandler) moveToBounds() {
}
func (h *mockHandler) setCursorAtScroll(pos term.Coordinates) bool {
	return false
}
func (h *mockHandler) cursorAtScroll() term.Coordinates {
	return term.Coordinates{}
}

func TestViHandle100(t *testing.T) {
	testViHandleSize(t, 100, 99)
}

func TestViHandle10(t *testing.T) {
	testViHandleSize(t, 10, 9)
}

func TestViHandle5(t *testing.T) {
	testViHandleSize(t, 5, 4)
}

func testViHandleSize(t *testing.T, width, height int) {
	tsuite := []struct {
		desc string
		in   string
		want string
	}{
		{
			desc: "delegates events to underlying handler in normal mode",
			in:   "j",
			want: "j",
		},
		{
			desc: "does not propagate normal events upon call to repeat",
			in:   "j.",
			want: "j",
		},
		{
			desc: "repeats update events upon call to repeat",
			in:   "jia#...",
			want: "jia#ia#ia#ia#",
		},
		{
			desc: "repeats insert events upon call to repeat",
			in:   "ia#.io#.",
			want: "ia#ia#io#io#",
		},
		{
			desc: "repeats delete events upon call to repeat",
			in:   "dd.",
			want: "dddd",
		},
		{
			desc: "repeats select + delete events upon call to repeat",
			in:   "jjjvllld..",
			want: "jjjvllldvllldvllld",
		},
		{
			desc: "repeats replace event upon call to repeat",
			in:   "jrl..",
			want: "jrlrlrl",
		},
		{
			desc: "repeats shift-line operator upon call to repeat",
			in:   ">>...",
			want: ">>>>>>>>",
		},
		{
			desc: "does no repeat undo",
			in:   ">>u...",
			want: ">>>>>>>>",
		},
		{
			desc: "does not repeat search events",
			in:   ">>/hello#.",
			want: ">>/hello#>>",
		},
		{
			desc: "repeats select + insert events upon call to repeat",
			in:   "jlvllchello#h.",
			want: "jlvllchello#hvllchello#",
		},
		{
			desc: "repeats delete a word to insert events",
			in:   "jjwcwhello#b.",
			want: "jjwcwhello#bcwhello#",
		},
		{
			desc: "does not capture combination if there was no update",
			in:   "jjj>>i#.",
			want: "jjj>>i#>>",
		},
		{
			desc: "handles search mode correctly",
			in:   "/put&>>i#/put&.",
			want: "/put&>>i#/put&>>",
		},
		{
			desc: "propagates '.' in search mode",
			in:   "/.&>>i#/.&.",
			want: "/.&>>i#/.&>>",
		},
		{
			desc: "does not repeat select events that did not wind up updating",
			in:   ">>jjvlllll#..ihell#.",
			want: ">>jjvlllll#>>>>ihell#ihell#",
		},
		{
			desc: "has infinite loop repeat protection",
			in:   "jjjjjjjjjjjjjjdf.u+udf.uu++.+",
			want: "jjjjjjjjjjjjjjdf.df.f.",
		},
	}

	testHandle := func(t *testing.T, in, want string) {
		buf := cell.NewBuffer()
		buf.ReadFrom(strings.NewReader(snippet))
		vi := New(buf, uri)
		mock := newMockHandler(buf)
		vi.handler = mock
		vi.Resize(width, height)
		for _, ch := range in {
			var ev term.Event
			if ch == '+' {
				ev = term.Event{Type: term.EventKey, Ch: 'r', Mod: term.ModCtrl}
			} else {
				ev = term.Event{Type: term.EventKey, Ch: ch}
			}
			vi.Handle(ev)
		}
		var received strings.Builder
		for _, ev := range mock.received {
			received.WriteRune(ev.Ch)
		}
		assert.Equal(t, want, received.String())
	}

	for _, tcase := range tsuite {
		t.Run(tcase.desc, func(t *testing.T) {
			testHandle(t, tcase.in, tcase.want)
		})
	}
}

// TestNormalCtrlScrollDispatch covers
// https://github.com/unstablebuild/rune/issues/60: Vi.Handle used to
// intercept 'u' in normal mode regardless of the modifier, so <c-u> was
// swallowed as undo and never reached the half-page-up handler.
func TestNormalCtrlScrollDispatch(t *testing.T) {
	const fileContent = "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk"

	suite := []struct {
		name      string
		setCursor term.Coordinates
		key       term.Event
		expect    string
	}{
		{
			name:      "ctrl-u moves screen up half page",
			setCursor: term.Coordinates{Y: 7},
			key:       term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'u'},
			expect:    "c\nd\ne\nX",
		},
		{
			name:      "ctrl-d moves screen down half page",
			setCursor: term.Coordinates{Y: 1},
			key:       term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'd'},
			expect:    "X\ne\nf\ng",
		},
	}

	for _, tcase := range suite {
		t.Run(tcase.name, func(t *testing.T) {
			buf := cell.NewBuffer()
			buf.ReadFrom(strings.NewReader(fileContent))
			vi := New(buf, uri)
			vi.Resize(1, 4)
			require.True(t, vi.SetCursorAtScroll(tcase.setCursor))

			quit, handled := vi.Handle(tcase.key)
			require.False(t, quit)
			require.True(t, handled)

			w := term.NewStringWriter(1, 4)
			vi.Draw(w)
			c, _, ok := vi.Cursor()
			require.True(t, ok)
			w.SetCell(c, term.Cell{Width: 1, Ch: 'X'})
			w.Flush()
			assert.Equal(t, tcase.expect, w.String())
		})
	}

	t.Run("ctrl-u does not undo the last change", func(t *testing.T) {
		buf := cell.NewBuffer()
		buf.ReadFrom(strings.NewReader(fileContent))
		vi := New(buf, uri)
		vi.Resize(20, 4)

		handleRunes(t, vi, "xG")
		edited := buf.String()
		require.Equal(t, "\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk", edited)

		quit, handled := vi.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'u'})
		require.False(t, quit)
		require.True(t, handled)
		assert.Equal(t, edited, buf.String())

		quit, handled = vi.Handle(testKey('u'))
		require.False(t, quit)
		require.True(t, handled)
		assert.Equal(t, fileContent, buf.String())
	})
}

// TestNormalCtrlDotDoesNotRepeat guards the same modifier-blind dispatch
// for '.': only an unmodified '.' repeats the last change, so <c-.> stays
// available to outer keybindings.
func TestNormalCtrlDotDoesNotRepeat(t *testing.T) {
	buf := cell.NewBuffer()
	buf.ReadFrom(strings.NewReader("alpha\nbravo\ncharlie"))
	vi := New(buf, uri)
	vi.Resize(20, 10)

	handleRunes(t, vi, "x")
	edited := buf.String()
	require.Equal(t, "lpha\nbravo\ncharlie", edited)

	quit, handled := vi.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: '.'})
	require.False(t, quit)
	assert.False(t, handled)
	assert.Equal(t, edited, buf.String())

	handleRunes(t, vi, ".")
	assert.Equal(t, "pha\nbravo\ncharlie", buf.String())
}

func TestUndo100(t *testing.T) {
	testUndoSize(t, 100, 99)
}
func TestUndo10(t *testing.T) {
	testUndoSize(t, 10, 9)
}
func TestUndo5(t *testing.T) {
	testUndoSize(t, 5, 4)
}

func testUndoSize(t *testing.T, width, height int) {
	const undoFortune = `Love in your heart wasn't put there to stay.
Love isn't love 'til you give it away.
		-- Oscar Hammerstein 中国`
	suite := []struct {
		name string
		cmd  string
	}{
		{"Insert", "jji\t"},
		{"InsertRowAt", "ji\n"},
		{"DeleteCell", "jjllllx"},
		{"ConflateRow", "ggJ"},
		{"TruncateRowFrom", "jlD"},
		{"TruncateFrom", "lllllldG"},
		{"DeleteRow", "dd"},
		{"Edit which effectively replaces", "jjlvllllchello"},
		{"Repeat", "jji\t#..."},
		{"ReplaceAll", "kkcGhello\nworld"},
		{"InsertRowBelow", "Gohello"},
	}

	for _, _tcase := range suite {
		tcase := _tcase
		t.Run(fmt.Sprintf("undo %s", tcase.name), func(t *testing.T) {
			buf := cell.NewBuffer()
			buf.ReadFrom(strings.NewReader(undoFortune))

			vi := New(buf, uri)
			vi.Resize(width, height)

			for range 5 {
				for _, ch := range tcase.cmd {
					ev := term.Event{Type: term.EventKey, Ch: ch}
					vi.Handle(ev)
				}
				assert.NotEqual(t, undoFortune, buf.String())
				vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
				quit, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: 'u'})
				assert.False(t, quit)
				assert.True(t, handled)
			}

			assert.Equal(t, undoFortune, buf.String())
		})
	}

	t.Run("undo/redo repeats", func(t *testing.T) {
		buf := cell.NewBuffer()
		buf.ReadFrom(strings.NewReader(undoFortune))
		vi := New(buf, uri)
		vi.Resize(width, height)

		for _, ch := range "iasdfgh#.." {
			if ch == '#' {
				vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
			} else {
				vi.Handle(term.Event{Type: term.EventKey, Ch: ch})
			}
		}
		assert.NotEqual(t, undoFortune, buf.String())
		for i := range 3 {
			quit, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: 'u'})
			assert.False(t, quit, i)
			assert.True(t, handled, i)
		}
		assert.Equal(t, undoFortune, buf.String())
		for i := range 3 {
			quit, handled := vi.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'r'})
			assert.False(t, quit, i)
			assert.True(t, handled, i)
		}
		assert.NotEqual(t, undoFortune, buf.String())
		for i := range 3 {
			quit, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: 'u'})
			assert.False(t, quit, i)
			assert.True(t, handled, i)
		}
		assert.Equal(t, undoFortune, buf.String())
	})

	t.Run("undo/redo oob edits", func(t *testing.T) {
		buf := cell.NewBuffer()
		buf.ReadFrom(strings.NewReader(undoFortune))
		vi := New(buf, uri)
		vi.Resize(width, height)

		buf.Edit(context.Background(), term.Coordinates{}, term.Coordinates{}, "abc")
		assert.Equal(t, "abc"+undoFortune, buf.String())

		quit, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: 'u'})
		assert.False(t, quit)
		assert.True(t, handled)
		assert.Equal(t, undoFortune, buf.String())

		quit, handled = vi.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'r'})
		assert.False(t, quit)
		assert.True(t, handled)
		assert.Equal(t, "abc"+undoFortune, buf.String())

		quit, handled = vi.Handle(term.Event{Type: term.EventKey, Ch: 'u'})
		assert.False(t, quit)
		assert.True(t, handled)
		assert.Equal(t, undoFortune, buf.String())
	})

	t.Run("undo/redo oob edits with regular edits", func(t *testing.T) {
		buf := cell.NewBuffer()
		buf.ReadFrom(strings.NewReader(undoFortune))
		vi := New(buf, uri)
		vi.Resize(width, height)

		buf.Edit(context.Background(), term.Coordinates{}, term.Coordinates{}, "abc")
		assert.Equal(t, "abc"+undoFortune, buf.String())
		for _, ch := range "iasdfgh#" {
			if ch == '#' {
				vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
			} else {
				vi.Handle(term.Event{Type: term.EventKey, Ch: ch})
			}
		}
		assert.Equal(t, "asdfghabc"+undoFortune, buf.String())

		for range 2 {
			quit, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: 'u'})
			assert.False(t, quit)
			assert.True(t, handled)
		}
		assert.Equal(t, undoFortune, buf.String())

		for range 2 {
			quit, handled := vi.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'r'})
			assert.False(t, quit)
			assert.True(t, handled)
		}
		assert.Equal(t, "asdfghabc"+undoFortune, buf.String())

		for range 2 {
			quit, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: 'u'})
			assert.False(t, quit)
			assert.True(t, handled)
		}
		assert.Equal(t, undoFortune, buf.String())
	})

	t.Run("undo/redo a series of updates", func(t *testing.T) {
		buf := cell.NewBuffer()
		buf.ReadFrom(strings.NewReader(undoFortune))
		prev := buf.String()

		vi := New(buf, uri)
		vi.Resize(width, height)

		for _, tcase := range suite {
			for _, ch := range tcase.cmd {
				ev := term.Event{Type: term.EventKey, Ch: ch}
				vi.Handle(ev)
			}
			vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
		}

		middle := buf.String()

		for i := range suite {
			quit, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: 'u'})
			assert.False(t, quit, i)
			assert.True(t, handled, i)
		}

		after := buf.String()
		assert.Equal(t, prev, after)

		for i := range suite {
			quit, handled := vi.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'r'})
			assert.False(t, quit, i)
			assert.True(t, handled, i)
		}

		afterRedo := buf.String()
		assert.Equal(t, middle, afterRedo)

		for i := range suite {
			quit, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: 'u'})
			assert.False(t, quit, i)
			assert.True(t, handled, i)
		}

		after = buf.String()
		assert.Equal(t, prev, after)
	})
}

func TestMoveAfterClick(t *testing.T) {
	width, height := 20, 10

	buf := cell.NewBuffer()
	buf.ReadFrom(strings.NewReader(snippet))
	vi := New(buf, uri)
	vi.Resize(width, height)

	for _, r := range "jjjjjjj" {
		_, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: r})
		require.True(t, handled)
	}
	pos, _, ok := vi.Cursor()
	require.True(t, ok)
	assert.Equal(t, term.Coordinates{Y: 7}, pos)

	_, handled := vi.Handle(term.Event{
		Type:   term.EventMouse,
		Key:    term.MouseLeft,
		MouseY: 2,
		MouseX: 0,
	})
	pos, _, ok = vi.Cursor()
	require.True(t, ok)
	require.Equal(t, term.Coordinates{Y: 2, X: 0}, pos)
	require.True(t, handled)

	for _, r := range "jk" {
		_, handled := vi.Handle(term.Event{Type: term.EventKey, Ch: r})
		require.True(t, handled)
	}
	pos, _, ok = vi.Cursor()
	require.True(t, ok)
	assert.Equal(t, term.Coordinates{Y: 2, X: 0}, pos)
}

func TestMatchBraceAfterClick(t *testing.T) {
	width, height := 20, 10

	buf := cell.NewBuffer()
	buf.ReadFrom(strings.NewReader(snippet))
	vi := New(buf, uri)
	vi.Resize(width, height)

	_, handled := vi.Handle(term.Event{
		Type:   term.EventMouse,
		Key:    term.MouseLeft,
		MouseY: 7,
		MouseX: 0,
	})
	pos := vi.CursorAtScroll()
	require.Equal(t, term.Coordinates{Y: 7, X: 0}, pos)
	require.True(t, handled)

	_, handled = vi.Handle(term.Event{Type: term.EventKey, Ch: '%'})
	require.True(t, handled)
	pos = vi.CursorAtScroll()
	assert.Equal(t, term.Coordinates{Y: 31, X: 0}, pos)

	_, handled = vi.Handle(term.Event{Type: term.EventKey, Ch: '%'})
	require.True(t, handled)
	pos = vi.CursorAtScroll()
	assert.Equal(t, term.Coordinates{Y: 7, X: 0}, pos)
}

func TestCopyDelete(t *testing.T) {
	tsuite := []struct {
		desc     string
		content  string
		in       string
		wantCopy string
	}{
		{"copies delete data", "a", "x", "a"},
		{"does not copy insert data", "a", "ihello", ""},
		{"does not copy undo of an insert", "", "ihello#u", ""},
		{"does not copy undo of an insert (preserve old data)", "a", "xihello#u", "a"},
		{"copies remove portion of a delete select", "a", "clb", "a"},
		{"copies redo of a delete (or leaves previous delete)", "a", "xuR", "a"},
		{"does not copy backspace within an insert", "x", "ddihella<o#", "x"},
		{"does copy text deleted through C", "x", "Ca#", "x"},
		{"does copy text deleted through c", "x", "cla#", "x"},
		{"does copy text deleted through cc", "x\ny", "ccz#", "x"},
		{"does copy text deleted through S", "x\ny", "Sz#", "x"},
	}

	for _, tcase := range tsuite {
		t.Run(tcase.desc, func(t *testing.T) {
			buf := cell.NewBuffer()
			buf.ReadFrom(strings.NewReader(tcase.content))

			mock := new(mockClip)
			vi := New(buf, uri, WithClipboard(registerset.New(mock)))
			vi.Resize(10, 10)

			for _, ch := range tcase.in {
				ev := term.Event{Type: term.EventKey, Ch: ch}
				if ch == '#' {
					ev = term.Event{Type: term.EventKey, Key: term.KeyEsc}
				} else if ch == '<' {
					ev = term.Event{Type: term.EventKey, Key: term.KeyBackspace}
				} else if ch == 'R' {
					ev = term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'r'}
				}
				vi.Handle(ev)
			}

			assert.Equal(t, tcase.wantCopy, mock.data.Text)
		})
	}
}

var _ = (foldsService)(testFoldsService{})

func TestVisualBlockDeleteThenPaste(t *testing.T) {
	ctrlV := term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'v'}

	type blockCase struct {
		name         string
		content      string
		setupKeys    string
		selectKeys   string
		deleteKeys   string
		moveKeys     string
		pasteKey     rune
		wantRegister string
		wantDeleted  string
		wantBuffer   string
	}

	drive := func(t *testing.T, vi *Vi, keys string) {
		t.Helper()
		for _, ch := range keys {
			quit, handled := vi.Handle(testKey(ch))
			require.False(t, quit)
			require.True(t, handled, "key %q should be handled", ch)
		}
	}

	for _, tc := range []blockCase{
		{
			name:         "d then P reinserts the whole block",
			content:      "abcd\nefgh\nijkl\nmnop",
			selectKeys:   "lj",
			deleteKeys:   "d",
			moveKeys:     "jj",
			pasteKey:     'P',
			wantRegister: "ab\nef",
			wantDeleted:  "cd\ngh\nijkl\nmnop",
			wantBuffer:   "cd\ngh\nabijkl\nefmnop",
		},
		{
			name:         "x then p reinserts the whole block",
			content:      "abcd\nefgh\nijkl\nmnop",
			selectKeys:   "lj",
			deleteKeys:   "x",
			moveKeys:     "jj",
			pasteKey:     'p',
			wantRegister: "ab\nef",
			wantDeleted:  "cd\ngh\nijkl\nmnop",
			wantBuffer:   "cd\ngh\niabjkl\nmefnop",
		},
		{
			name:         "d on a ragged block keeps every row",
			content:      "abcdefgh\nxy\n12345678",
			selectKeys:   "llljj",
			deleteKeys:   "d",
			moveKeys:     "jj",
			pasteKey:     'P',
			wantRegister: "abcd\nxy\n1234",
			wantDeleted:  "efgh\n\n5678",
			wantBuffer:   "efgh\n\nabcd5678\nxy\n1234",
		},
		{
			name:         "black-hole block delete preserves unnamed",
			content:      "abcd\nefgh\nijkl",
			setupKeys:    "ljy^jj",
			selectKeys:   "l",
			deleteKeys:   "\"_d",
			moveKeys:     "",
			pasteKey:     'P',
			wantRegister: "ab\nef",
			wantDeleted:  "abcd\nefgh\nkl",
			wantBuffer:   "abcd\nefgh\nabkl\nef",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf := cell.NewBuffer()
			_, err := buf.ReadFrom(strings.NewReader(tc.content))
			require.NoError(t, err)

			mock := new(mockClip)
			vi := New(buf, uri, WithClipboard(registerset.New(mock)))
			vi.Resize(80, 24)

			if tc.setupKeys != "" {
				quit, handled := vi.Handle(ctrlV)
				require.False(t, quit)
				require.True(t, handled)
				drive(t, vi, tc.setupKeys)
			}

			quit, handled := vi.Handle(ctrlV)
			require.False(t, quit)
			require.True(t, handled)
			drive(t, vi, tc.selectKeys+tc.deleteKeys)

			data, err := vi.Paste(clipboard.DefaultRegisterID)
			require.NoError(t, err)
			assert.Equal(t, tc.wantRegister, data.Text)
			mode, ok := data.Metadata.(text.SelectMode)
			require.True(t, ok, "register should carry a selection mode")
			assert.Equal(t, text.BlockSelection, mode)
			assert.Equal(t, tc.wantDeleted, buf.String())

			if tc.moveKeys != "" {
				drive(t, vi, tc.moveKeys)
			}
			if tc.pasteKey != 0 {
				quit, handled := vi.Handle(testKey(tc.pasteKey))
				require.False(t, quit)
				require.True(t, handled)
				assert.Equal(t, tc.wantBuffer, buf.String())
			}
		})
	}
}

func TestVisualBlockChangePreservesRegister(t *testing.T) {
	buf := cell.NewBuffer()
	_, err := buf.ReadFrom(strings.NewReader("abcd\nefgh\nijkl"))
	require.NoError(t, err)
	vi := New(buf, uri, WithClipboard(registerset.New(new(mockClip))))
	vi.Resize(80, 24)

	quit, handled := vi.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'v'})
	require.False(t, quit)
	require.True(t, handled)
	handleRunes(t, vi, "ljc")

	assert.Equal(t, insertMode, vi.handler.mode())
	assert.Equal(t, "cd\ngh\nijkl", buf.String())
	data, err := vi.Paste(clipboard.DefaultRegisterID)
	require.NoError(t, err)
	assert.Equal(t, "", data.Text)
}

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

type mockClip struct {
	data clipboard.Data
}

func (m *mockClip) Paste(registerID string) (clipboard.Data, error) {
	return m.data, nil
}

func (m *mockClip) Copy(registerID string, data clipboard.Data) error {
	m.data = data
	return nil
}

func testKey(ch rune) term.Event {
	return term.Event{Type: term.EventKey, Ch: ch}
}

func testEsc() term.Event {
	return term.Event{Type: term.EventKey, Key: term.KeyEsc}
}

func handleRunes(t *testing.T, vi *Vi, keys string) {
	t.Helper()
	for _, ch := range keys {
		quit, handled := vi.Handle(testKey(ch))
		require.False(t, quit)
		require.True(t, handled, "key %q should be handled", ch)
	}
}

func handleInsert(t *testing.T, vi *Vi, text string) {
	t.Helper()
	for _, ch := range text {
		quit, handled := vi.Handle(testKey(ch))
		require.False(t, quit)
		require.True(t, handled, "insert key %q should be handled", ch)
	}
	quit, handled := vi.Handle(testEsc())
	require.False(t, quit)
	require.True(t, handled)
}

func assertCursorAtScroll(t *testing.T, vi *Vi, want term.Coordinates) {
	t.Helper()
	assert.Equal(t, want, vi.CursorAtScroll())
}

func currentLocation(t *testing.T, vi *Vi, id string) textapi.Location {
	t.Helper()
	list, ok := vi.cursor.LocationList(id)
	require.True(t, ok, "location list %q should exist", id)
	loc, ok := list.Current()
	require.True(t, ok, "location list %q should have a current location", id)
	return loc
}

func seedChangeList(vi *Vi, locs []term.Coordinates, cursor int) {
	vi.changeList.locations = append([]term.Coordinates(nil), locs...)
	vi.changeList.cursor = cursor
	vi.pendingChangeExists = false
}

func editBufferWithoutChangeRecord(vi *Vi, edit func()) {
	vi.resetting = true
	edit()
	vi.resetting = false
	vi.pendingChangeExists = false
	vi.oobEdited = false
	vi.currEdited = false
	vi.evEdited = false
}

func TestLastChangeMark(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T, vi *Vi)
	}{
		{
			name: "insert records dot mark",
			run: func(t *testing.T, vi *Vi) {
				handleRunes(t, vi, "j")
				assertCursorAtScroll(t, vi, term.Coordinates{Y: 1})

				handleRunes(t, vi, "i")
				handleInsert(t, vi, "X")

				loc := currentLocation(t, vi, lastChangeLocationListID)
				assert.Equal(t, term.Coordinates{Y: 1}, loc.From)
			},
		},
		{
			name: "undo does not update dot mark",
			run: func(t *testing.T, vi *Vi) {
				handleRunes(t, vi, "i")
				handleInsert(t, vi, "X")
				before := currentLocation(t, vi, lastChangeLocationListID)

				quit, handled := vi.Handle(testKey('u'))
				require.False(t, quit)
				require.True(t, handled)

				after := currentLocation(t, vi, lastChangeLocationListID)
				assert.Equal(t, before, after)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			buf := cell.NewBuffer()
			buf.ReadFrom(strings.NewReader("hello\nworld\nfoo"))
			vi := New(buf, uri)
			vi.Resize(20, 10)

			tc.run(t, vi)
		})
	}
}

// TestVisualMarks verifies that vi records the `<` and `>` visual
// marks whenever it leaves a visual mode (operator, <esc>, or
// motion-driven exit), and that the corresponding `'<` / `'>` /
// “ `< “ / “ `> “ keybindings can jump back to those positions —
// matching Vim's :help visual-marks.
func TestVisualMarks(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		run       func(t *testing.T, vi *Vi)
		wantLess  *term.Coordinates
		wantGreat *term.Coordinates
	}{
		{
			name:    "visual exit via <esc> records selection bounds",
			content: "alpha beta\ngamma delta\nepsilon zeta\n",
			run: func(t *testing.T, vi *Vi) {
				// v + 2 j extends a charwise selection from (0,0) to (2,0).
				handleRunes(t, vi, "vjj")
				quit, handled := vi.Handle(testEsc())
				require.False(t, quit)
				require.True(t, handled)
			},
			wantLess:  &term.Coordinates{Y: 0, X: 0},
			wantGreat: &term.Coordinates{Y: 2, X: 0},
		},
		{
			name:    "visual line exit via operator records selection bounds",
			content: "alpha beta gamma delta epsilon zeta\nsecond line\nthird line\n",
			run: func(t *testing.T, vi *Vi) {
				// V j enters visual-line and extends one line down,
				// then `y` yanks and leaves visual.
				handleRunes(t, vi, "Vjy")
			},
			// Linewise selection runs from start of line 0 to last
			// column of line 1.
			wantLess: &term.Coordinates{Y: 0, X: 0},
			wantGreat: &term.Coordinates{
				Y: 1, X: len("second line"),
			},
		},
		{
			name:    "visual block exit via gq records selection bounds",
			content: "alpha beta gamma delta epsilon zeta\nsecond line\nthird line\n",
			run: func(t *testing.T, vi *Vi) {
				// <c-v> j l l gq — block select rows 0..1, columns
				// 0..2, then run gq which exits visual.
				quit, handled := vi.Handle(term.Event{
					Type: term.EventKey, Mod: term.ModCtrl, Ch: 'v',
				})
				require.False(t, quit)
				require.True(t, handled)
				handleRunes(t, vi, "jllgq")
			},
			wantLess:  &term.Coordinates{Y: 0, X: 0},
			wantGreat: &term.Coordinates{Y: 1, X: 2},
		},
		{
			name:    "backwards visual selection stores sorted bounds",
			content: "alpha beta\ngamma delta\nepsilon zeta\n",
			run: func(t *testing.T, vi *Vi) {
				// move to (2,3), then v + 2 k h h h to extend back.
				handleRunes(t, vi, "jjlllvkkhhh")
				quit, handled := vi.Handle(testEsc())
				require.False(t, quit)
				require.True(t, handled)
			},
			// Even though the user extended upward and leftward,
			// `'<` ends up at the smaller coordinate.
			wantLess:  &term.Coordinates{Y: 0, X: 0},
			wantGreat: &term.Coordinates{Y: 2, X: 3},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			buf := cell.NewBuffer()
			buf.ReadFrom(strings.NewReader(tc.content))
			vi := New(buf, uri)
			vi.Resize(40, 20)

			tc.run(t, vi)

			less := currentLocation(t, vi, visualSelectionStartMarkID)
			greater := currentLocation(t, vi, visualSelectionEndMarkID)

			if tc.wantLess != nil {
				assert.Equal(t, *tc.wantLess, less.From, "'< mark from")
			}
			if tc.wantGreat != nil {
				assert.Equal(t, *tc.wantGreat, greater.From, "'> mark from")
			}
		})
	}
}

// TestVisualMarksJump verifies that visual marks can be used as
// targets for cursor-level navigation. Keybinding-level dispatch is
// covered in keybindings_test.go; this exercises the underlying
// MoveToNextLocation API that the keybindings invoke.
func TestVisualMarksJump(t *testing.T) {
	tests := []struct {
		name       string
		setupKeys  string
		jumpListID string
		wantCursor term.Coordinates
	}{
		{
			name:       "less mark jumps to start of last selection",
			setupKeys:  "vjj", // (0,0) -> (2,0)
			jumpListID: visualSelectionStartMarkID,
			wantCursor: term.Coordinates{Y: 0, X: 0},
		},
		{
			name:       "greater mark jumps to end of last selection",
			setupKeys:  "vjj",
			jumpListID: visualSelectionEndMarkID,
			wantCursor: term.Coordinates{Y: 2, X: 0},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			buf := cell.NewBuffer()
			buf.ReadFrom(strings.NewReader("alpha\nbeta\ngamma\n"))
			vi := New(buf, uri)
			vi.Resize(40, 20)

			handleRunes(t, vi, tc.setupKeys)
			quit, handled := vi.Handle(testEsc())
			require.False(t, quit)
			require.True(t, handled)

			// Move cursor away so the jump produces a visible change.
			handleRunes(t, vi, "G")
			require.NotEqual(t, tc.wantCursor, vi.CursorAtScroll())

			require.True(t, vi.MoveToNextLocation(tc.jumpListID))
			assert.Equal(t, tc.wantCursor, vi.CursorAtScroll())
		})
	}
}

func TestChangeListOperations(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T, l *changeList)
	}{
		{
			name: "zero value has null data and no navigation",
			run: func(t *testing.T, l *changeList) {
				_, ok := l.older()
				assert.False(t, ok)
				_, ok = l.newer()
				assert.False(t, ok)
				assert.Empty(t, l.locations)
			},
		},
		{
			name: "single entry has no older or newer entry",
			run: func(t *testing.T, l *changeList) {
				l.append(term.Coordinates{Y: 1})
				_, ok := l.older()
				assert.False(t, ok)
				_, ok = l.newer()
				assert.False(t, ok)
				assert.Equal(t, []term.Coordinates{{Y: 1}}, l.locations)
				assert.Equal(t, 0, l.cursor)
			},
		},
		{
			name: "older and newer traverse in order with boundary no-ops",
			run: func(t *testing.T, l *changeList) {
				l.append(term.Coordinates{})
				l.append(term.Coordinates{Y: 1})
				l.append(term.Coordinates{Y: 2})

				pos, ok := l.older()
				require.True(t, ok)
				assert.Equal(t, term.Coordinates{Y: 1}, pos)
				pos, ok = l.older()
				require.True(t, ok)
				assert.Equal(t, term.Coordinates{}, pos)
				_, ok = l.older()
				assert.False(t, ok)

				pos, ok = l.newer()
				require.True(t, ok)
				assert.Equal(t, term.Coordinates{Y: 1}, pos)
				pos, ok = l.newer()
				require.True(t, ok)
				assert.Equal(t, term.Coordinates{Y: 2}, pos)
				_, ok = l.newer()
				assert.False(t, ok)
			},
		},
		{
			name: "duplicate latest append does not add an entry",
			run: func(t *testing.T, l *changeList) {
				l.append(term.Coordinates{X: 2, Y: 3})
				l.append(term.Coordinates{X: 2, Y: 3})
				assert.Equal(t, []term.Coordinates{{X: 2, Y: 3}}, l.locations)
				assert.Equal(t, 0, l.cursor)
			},
		},
		{
			name: "append after going older truncates future entries",
			run: func(t *testing.T, l *changeList) {
				l.append(term.Coordinates{})
				l.append(term.Coordinates{Y: 1})
				l.append(term.Coordinates{Y: 2})
				_, ok := l.older()
				require.True(t, ok)

				l.append(term.Coordinates{Y: 9})
				assert.Equal(t, []term.Coordinates{{}, {Y: 1}, {Y: 9}}, l.locations)
				assert.Equal(t, 2, l.cursor)
			},
		},
		{
			name: "append same as current after going older truncates future without duplicate",
			run: func(t *testing.T, l *changeList) {
				l.append(term.Coordinates{})
				l.append(term.Coordinates{Y: 1})
				l.append(term.Coordinates{Y: 2})
				_, ok := l.older()
				require.True(t, ok)

				l.append(term.Coordinates{Y: 1})
				assert.Equal(t, []term.Coordinates{{}, {Y: 1}}, l.locations)
				assert.Equal(t, 1, l.cursor)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.run(t, newChangeList())
		})
	}
}

func TestChangeListNavigation(t *testing.T) {
	type commandAssertion struct {
		keys string
		want term.Coordinates
	}

	tests := []struct {
		name      string
		content   string
		arrange   func(t *testing.T, vi *Vi, buf *cell.Buffer)
		wantList  []term.Coordinates
		commands  []commandAssertion
		wantFinal []term.Coordinates
	}{
		{
			name:    "normal edits move older then newer through history",
			content: "alpha\nbravo\ncharlie",
			arrange: func(t *testing.T, vi *Vi, buf *cell.Buffer) {
				handleRunes(t, vi, "i")
				handleInsert(t, vi, "A")
				handleRunes(t, vi, "j")
				handleRunes(t, vi, "i")
				handleInsert(t, vi, "B")
				handleRunes(t, vi, "j")
				handleRunes(t, vi, "i")
				handleInsert(t, vi, "C")
			},
			wantList: []term.Coordinates{{}, {Y: 1}, {Y: 2}},
			commands: []commandAssertion{
				{keys: "g;", want: term.Coordinates{Y: 1}},
				{keys: "g;", want: term.Coordinates{}},
				{keys: "g;", want: term.Coordinates{}},
				{keys: "g,", want: term.Coordinates{Y: 1}},
				{keys: "g,", want: term.Coordinates{Y: 2}},
				{keys: "g,", want: term.Coordinates{Y: 2}},
			},
		},
		{
			name:    "insert mode edits are grouped into one change entry",
			content: "alpha\nbravo",
			arrange: func(t *testing.T, vi *Vi, buf *cell.Buffer) {
				handleRunes(t, vi, "i")
				handleInsert(t, vi, "ABC")
				handleRunes(t, vi, "j")
				handleRunes(t, vi, "i")
				handleInsert(t, vi, "D")
			},
			wantList: []term.Coordinates{{}, {X: 2, Y: 1}},
			commands: []commandAssertion{
				{keys: "g;", want: term.Coordinates{}},
				{keys: "g,", want: term.Coordinates{X: 2, Y: 1}},
			},
		},
		{
			name:    "dot repeat edits create change entries",
			content: "alpha\nbravo\ncharlie",
			arrange: func(t *testing.T, vi *Vi, buf *cell.Buffer) {
				handleRunes(t, vi, "i")
				handleInsert(t, vi, "A")
				handleRunes(t, vi, "j.")
			},
			wantList: []term.Coordinates{{}, {Y: 1}},
			commands: []commandAssertion{
				{keys: "g;", want: term.Coordinates{}},
				{keys: "g,", want: term.Coordinates{Y: 1}},
			},
		},
		{
			name:    "open line below records inserted text start",
			content: "alpha\nbravo\ncharlie",
			arrange: func(t *testing.T, vi *Vi, buf *cell.Buffer) {
				handleRunes(t, vi, "i")
				handleInsert(t, vi, "A")
				handleRunes(t, vi, "j")
				handleRunes(t, vi, "o")
				handleInsert(t, vi, "//hello")
				handleRunes(t, vi, "j")
				handleRunes(t, vi, "i")
				handleInsert(t, vi, "C")
			},
			commands: []commandAssertion{
				{keys: "g;", want: term.Coordinates{Y: 2}},
			},
		},
		{
			name:    "open line above records inserted text start",
			content: "alpha\nbravo\ncharlie",
			arrange: func(t *testing.T, vi *Vi, buf *cell.Buffer) {
				handleRunes(t, vi, "i")
				handleInsert(t, vi, "A")
				handleRunes(t, vi, "jj")
				handleRunes(t, vi, "O")
				handleInsert(t, vi, "//above")
				handleRunes(t, vi, "jj")
				handleRunes(t, vi, "i")
				handleInsert(t, vi, "D")
			},
			commands: []commandAssertion{
				{keys: "g;", want: term.Coordinates{Y: 2}},
			},
		},
		{
			name:    "enter in insert mode records split line start",
			content: "alpha\nbravo\ncharlie",
			arrange: func(t *testing.T, vi *Vi, buf *cell.Buffer) {
				handleRunes(t, vi, "i")
				handleInsert(t, vi, "A")
				handleRunes(t, vi, "j")
				handleRunes(t, vi, "i")
				quit, handled := vi.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
				require.False(t, quit)
				require.True(t, handled)
				handleInsert(t, vi, "//split")
				handleRunes(t, vi, "j")
				handleRunes(t, vi, "i")
				handleInsert(t, vi, "D")
			},
			commands: []commandAssertion{
				{keys: "g;", want: term.Coordinates{Y: 2}},
			},
		},
		{
			name:    "out-of-band edits are captured on the next event",
			content: "alpha\nbravo\ncharlie",
			arrange: func(t *testing.T, vi *Vi, buf *cell.Buffer) {
				handleRunes(t, vi, "i")
				handleInsert(t, vi, "A")
				buf.Edit(context.Background(), term.Coordinates{Y: 2}, term.Coordinates{Y: 2}, "C")

				// The next event snapshots the out-of-band edit.
				handleRunes(t, vi, "j")
			},
			wantList: []term.Coordinates{{}, {Y: 2}},
			commands: []commandAssertion{
				{keys: "g;", want: term.Coordinates{}},
				{keys: "g,", want: term.Coordinates{Y: 2}},
			},
		},
		{
			name:    "empty change list handles commands as no-ops",
			content: "alpha\nbravo",
			arrange: func(t *testing.T, vi *Vi, buf *cell.Buffer) {
				handleRunes(t, vi, "j")
			},
			commands: []commandAssertion{
				{keys: "g;", want: term.Coordinates{Y: 1}},
				{keys: "g,", want: term.Coordinates{Y: 1}},
			},
		},
		{
			name:    "single change entry has older and newer boundary no-ops",
			content: "alpha\nbravo",
			arrange: func(t *testing.T, vi *Vi, buf *cell.Buffer) {
				handleRunes(t, vi, "i")
				handleInsert(t, vi, "A")
				handleRunes(t, vi, "j")
			},
			wantList: []term.Coordinates{{}},
			commands: []commandAssertion{
				{keys: "g;", want: term.Coordinates{Y: 1}},
				{keys: "g,", want: term.Coordinates{Y: 1}},
			},
		},
		{
			name:    "undo does not append or rewrite change entries",
			content: "alpha\nbravo\ncharlie",
			arrange: func(t *testing.T, vi *Vi, buf *cell.Buffer) {
				handleRunes(t, vi, "i")
				handleInsert(t, vi, "A")
				handleRunes(t, vi, "j")
				handleRunes(t, vi, "i")
				handleInsert(t, vi, "B")

				quit, handled := vi.Handle(testKey('u'))
				require.False(t, quit)
				require.True(t, handled)
			},
			wantList: []term.Coordinates{{}, {Y: 1}},
			commands: []commandAssertion{
				{keys: "g;", want: term.Coordinates{}},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			buf := cell.NewBuffer()
			buf.ReadFrom(strings.NewReader(tc.content))
			vi := New(buf, uri)
			vi.Resize(20, 10)

			if tc.arrange != nil {
				tc.arrange(t, vi, buf)
			}
			if tc.wantList != nil {
				assert.Equal(t, tc.wantList, vi.changeList.locations)
			}

			for _, cmd := range tc.commands {
				handleRunes(t, vi, cmd.keys)
				assertCursorAtScroll(t, vi, cmd.want)
				assert.Equal(t, normalMode, vi.handler.mode())
			}
			if tc.wantFinal != nil {
				assert.Equal(t, tc.wantFinal, vi.changeList.locations)
			}
		})
	}
}

func TestChangeListNavigationWithStaleLocations(t *testing.T) {
	tests := []struct {
		name    string
		content string
		arrange func(t *testing.T, vi *Vi, buf *cell.Buffer)
		seed    []term.Coordinates
		cursor  int
		start   term.Coordinates
		keys    string
		want    func(vi *Vi) term.Coordinates
	}{
		{
			name:    "older location past last line clamps after text was deleted",
			content: "zero\none\ntwo\nthree",
			arrange: func(t *testing.T, vi *Vi, buf *cell.Buffer) {
				editBufferWithoutChangeRecord(vi, func() {
					buf.Edit(context.Background(), term.Coordinates{Y: 2}, term.Coordinates{X: 5, Y: 3}, "")
				})
				require.Less(t, buf.Rows(), 4)
			},
			seed:   []term.Coordinates{{Y: 3}, {}},
			cursor: 1,
			keys:   "g;",
			want: func(vi *Vi) term.Coordinates {
				return term.Coordinates{Y: vi.buf.Rows() - 1}
			},
		},
		{
			name:    "newer location past last column clamps to line end",
			content: "zero\nab",
			seed:    []term.Coordinates{{}, {X: 999, Y: 1}},
			cursor:  0,
			keys:    "g,",
			want: func(vi *Vi) term.Coordinates {
				return term.Coordinates{X: vi.buf.Columns(1), Y: 1}
			},
		},
		{
			name:    "newer location past last column clamps after line was shortened",
			content: "zero\nabcdef",
			arrange: func(t *testing.T, vi *Vi, buf *cell.Buffer) {
				editBufferWithoutChangeRecord(vi, func() {
					buf.Edit(context.Background(), term.Coordinates{X: 2, Y: 1}, term.Coordinates{X: 6, Y: 1}, "")
				})
				require.Equal(t, 2, buf.Columns(1))
			},
			seed:   []term.Coordinates{{}, {X: 6, Y: 1}},
			cursor: 0,
			keys:   "g,",
			want: func(vi *Vi) term.Coordinates {
				return term.Coordinates{X: 2, Y: 1}
			},
		},
		{
			name:    "negative stale location clamps to origin",
			content: "zero\none",
			seed:    []term.Coordinates{{X: -10, Y: -10}, {Y: 1}},
			cursor:  1,
			start:   term.Coordinates{Y: 1},
			keys:    "g;",
			want: func(vi *Vi) term.Coordinates {
				return term.Coordinates{}
			},
		},
		{
			name:    "nil locations are handled as no-op null data",
			content: "zero\none",
			cursor:  -1,
			start:   term.Coordinates{Y: 1},
			keys:    "g;g,",
			want: func(vi *Vi) term.Coordinates {
				return term.Coordinates{Y: 1}
			},
		},
		{
			name:    "one stale entry cannot move older or newer",
			content: "zero\none",
			seed:    []term.Coordinates{{X: 99, Y: 99}},
			cursor:  0,
			start:   term.Coordinates{Y: 1},
			keys:    "g;g,",
			want: func(vi *Vi) term.Coordinates {
				return term.Coordinates{Y: 1}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			buf := cell.NewBuffer()
			buf.ReadFrom(strings.NewReader(tc.content))
			vi := New(buf, uri)
			vi.Resize(20, 10)

			if tc.arrange != nil {
				tc.arrange(t, vi, buf)
			}
			seedChangeList(vi, tc.seed, tc.cursor)
			vi.SetCursorAtScroll(tc.start)

			handleRunes(t, vi, tc.keys)
			assertCursorAtScroll(t, vi, tc.want(vi))
			assert.Equal(t, normalMode, vi.handler.mode())
		})
	}
}

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

package browsertest

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/component/comptest"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	tcomponent "unstable.build/rune/internal/component"

	"unstable.build/rune/internal/browser"
)

func splitVerticalLeft(c *browser.Component, h browserapi.Handler) (browser.Window, bool) {
	return c.Split(browserapi.OrientationLeft, c.Focus(), h)
}
func splitVerticalRight(c *browser.Component, h browserapi.Handler) (browser.Window, bool) {
	return c.Split(browserapi.OrientationRight, c.Focus(), h)
}
func splitHorizontalAbove(c *browser.Component, h browserapi.Handler) (browser.Window, bool) {
	return c.Split(browserapi.OrientationTop, c.Focus(), h)
}
func splitHorizontalBelow(c *browser.Component, h browserapi.Handler) (browser.Window, bool) {
	return c.Split(browserapi.OrientationBottom, c.Focus(), h)
}

var splitSuite = []struct {
	method string
	split  func(c *browser.Component, h browserapi.Handler) (browser.Window, bool)
}{
	{"SplitVerticalLeft:", splitVerticalLeft},
	{"SplitVerticalRight:", splitVerticalRight},
	{"SplitHorizontalAbove:", splitHorizontalAbove},
	{"SplitHorizontalBelow:", splitHorizontalBelow},
}

func freeTabs(c *browser.Component) (ret int) {
	for _, t := range c.Tabs() {
		_, ok := t.Window()
		if !ok {
			ret++
		}
	}
	return
}

func TestComponentCloseWindow(t *testing.T) {

	t.Run("closing the last window returns error", func(t *testing.T) {
		c := browser.NewComponent(browserConfig())
		assert.Error(t, c.Focus().Close())

		// makes sure that window list is not corrupted
		c.RemoveAllTabs()
	})

	for _, _tcase := range splitSuite {
		tcase := _tcase
		t.Run(tcase.method+"closes window correctly", func(t *testing.T) {
			c := browser.NewComponent(browserConfig())
			uri, err := workspaceapi.ParseURI("file:///OAK")
			require.NoError(t, err)

			var h browserapi.Handler
			h = NewTestHandler()
			h = c.NewTab(uri, 'x', "OAK", h, nil)

			win, ok := tcase.split(c, h)
			require.True(t, ok)
			require.Equal(t, 2, c.Tiles())

			assert.NoError(t, win.Close())
			require.Equal(t, 1, c.Tiles())

			assert.Equal(t, 1, freeTabs(c))
		})

		t.Run(tcase.method+"Close is idempotent", func(t *testing.T) {
			c := browser.NewComponent(browserConfig())
			win, ok := tcase.split(c, NewTestHandler())
			require.True(t, ok)
			require.NoError(t, win.Close())
			assert.NoError(t, win.Close())
		})

		t.Run(tcase.method+"close window on handler exit", func(t *testing.T) {
			c := browser.NewComponent(browserConfig())
			var h browserapi.Handler
			h = NewTestHandler()
			h.(*TestHandler).Exit = true
			h.(*TestHandler).Handled = true
			win, ok := tcase.split(c, h)
			require.True(t, ok)

			assert.Equal(t, 0, len(c.Tabs()))
			assert.Equal(t, 2, c.Tiles())

			exit, handled := c.Handle(term.Event{})
			require.True(t, handled)
			require.False(t, exit)

			assert.Equal(t, 0, len(c.Tabs()))
			assert.Equal(t, 1, c.Tiles())
			require.Error(t, win.Close())
			assert.Equal(t, 1, c.Tiles())
		})

		t.Run(tcase.method+"trying to close last window does not corrupt state", func(t *testing.T) {
			c := browser.NewComponent(browserConfig())

			require.Error(t, c.Focus().Close())

			win, ok := tcase.split(c, NewTestHandler())
			require.True(t, ok)

			require.NoError(t, win.Close())
			require.Error(t, c.Focus().Close())
		})
	}
}

func updateWithNextFreeTab(c *browser.Component, win browser.Window) bool {
	return c.NextTab(win)
}

func TestComponentRemoveAllTabs(t *testing.T) {
	w1 := browser.NewComponent(browserConfig())
	w2 := browser.NewComponent(browserConfig())
	w2.Split(browserapi.OrientationDefault, w2.Focus(), NewTestHandler())

	tsuite := []struct {
		description string
		c           *browser.Component
		before      int
		after       int
	}{
		{"removes all tabs with one window", w1, 3, 0},
		{"removes all tabs with multiple windows", w2, 3, 0},
	}

	for _, tcase := range tsuite {
		c := tcase.c
		before := tcase.before
		after := tcase.after
		t.Run(tcase.description, func(t *testing.T) {
			uri1, err := workspaceapi.ParseURI("file:///a")
			require.NoError(t, err)
			uri2, err := workspaceapi.ParseURI("file:///b")
			require.NoError(t, err)
			uri3, err := workspaceapi.ParseURI("file:///c")
			require.NoError(t, err)

			handlers := [3]testCloser{}
			c.NewTab(uri1, 'x', "a", &handlers[0], &handlers[0])
			c.NewTab(uri2, 'x', "b", &handlers[1], &handlers[1])
			c.NewTab(uri3, 'x', "c", &handlers[2], &handlers[2])
			updateWithNextFreeTab(c, c.Focus())
			c.ShiftFocus()
			updateWithNextFreeTab(c, c.Focus())

			assert.Equal(t, before, len(c.Tabs()))

			for _, h := range handlers {
				assert.Equal(t, 0, h.closed)
			}

			c.RemoveAllTabs()

			assert.Equal(t, after, len(c.Tabs()))
			for _, h := range handlers {
				// closer + handler
				assert.Equal(t, 2, h.closed)
			}
		})
	}
}

type testCloser struct {
	handler.TestHandler
	closed int
}

func (t *testCloser) Close() error {
	t.closed++
	return nil
}

func assertFreeTab(t *testing.T, h tui.Handler, free bool) {
	_, ok := h.(*browser.Tab).Window()
	assert.Equal(t, free, !ok)
}

func assertWindowContent(t *testing.T, win browser.Window, expected browserapi.Handler) {
	content, err := win.Content()
	require.NoError(t, err)
	assert.Equal(t, expected, content)
}

func TestComponentSetContent(t *testing.T) {
	for _, _tcase := range splitSuite {
		tcase := _tcase
		t.Run(tcase.method, func(t *testing.T) {
			c := browser.NewComponent(browserConfig())
			win0 := c.Focus()
			uri, err := workspaceapi.ParseURI("file:///Merry_Christmas")
			require.NoError(t, err)

			christmasTab := c.NewTab(uri, 'x', "Merry Christmas", NewTestHandler(), nil)

			assertFreeTab(t, christmasTab, true)
			require.NoError(t, win0.SetContent(christmasTab))
			assertFreeTab(t, christmasTab, false)
			assertWindowContent(t, win0, christmasTab)
			assert.False(t, c.NextTab(win0))

			h2 := browser.NopHandler(&handler.TestHandler{})
			win1, ok := tcase.split(c, h2)
			require.True(t, ok)
			assertWindowContent(t, win1, h2)
			require.Error(t, win1.SetContent(christmasTab))
			assertWindowContent(t, win1, h2)
			assertFreeTab(t, christmasTab, false)

			h3 := browser.NopHandler(&handler.TestHandler{})
			require.NoError(t, win1.SetContent(h3))
			assertWindowContent(t, win1, h3)
			assertFreeTab(t, christmasTab, false)

			h4 := browser.NopHandler(&handler.TestHandler{})
			require.NoError(t, win0.SetContent(h4))
			assertWindowContent(t, win0, h4)
			assertFreeTab(t, christmasTab, true)
		})
	}
}

type testTabSubscriber struct {
	t browser.Tab
}

func (s *testTabSubscriber) OnFocus(t *browser.Tab) {
	s.t = *t
}

func (s *testTabSubscriber) OnFree(t *browser.Tab) {
	s.t = *t
}

func TestComponentEditWindowTab(t *testing.T) {
	for _, _tcase := range splitSuite {
		tcase := _tcase
		t.Run(tcase.method, func(t *testing.T) {
			c := browser.NewComponent(browserConfig())
			win0 := c.Focus()
			uri1, err := workspaceapi.ParseURI("file:///AMZN")
			require.NoError(t, err)
			uri2, err := workspaceapi.ParseURI("file:///TSLA")
			require.NoError(t, err)
			uri3, err := workspaceapi.ParseURI("file:///GOOG")
			require.NoError(t, err)

			amzn := c.NewTab(uri1, 'x', "AMZN", NewTestHandler(), nil)
			updateWithNextFreeTab(c, win0)
			tsla := c.NewTab(uri2, 'x', "TSLA", NewTestHandler(), nil)
			goog := c.NewTab(uri3, 'x', "GOOG", NewTestHandler(), nil)
			win, ok := tcase.split(c, goog)
			require.True(t, ok)

			googSubs := testTabSubscriber{}
			goog.Subscribe(&googSubs)

			assertFreeTab(t, amzn, false)
			assertFreeTab(t, tsla, true)
			assertFreeTab(t, goog, false)
			assertFreeTab(t, &googSubs.t, false)

			for range 3 {
				assert.True(t, c.NextTab(win))
			}

			assertFreeTab(t, amzn, false)
			assertFreeTab(t, tsla, false)
			assertFreeTab(t, goog, true)
			assertFreeTab(t, &googSubs.t, true)

			for range 3 {
				assert.True(t, c.PreviousTab(win0))
			}

			assertFreeTab(t, amzn, true)
			assertFreeTab(t, tsla, false)
			assertFreeTab(t, goog, false)
			assertFreeTab(t, &googSubs.t, false)

			assert.True(t, updateWithNextFreeTab(c, win0))

			assertFreeTab(t, amzn, false)
			assertFreeTab(t, tsla, false)
			assertFreeTab(t, goog, true)
			assertFreeTab(t, &googSubs.t, true)

			assert.True(t, updateWithNextFreeTab(c, win0))

			assertFreeTab(t, amzn, true)
			assertFreeTab(t, tsla, false)
			assertFreeTab(t, goog, false)
			assertFreeTab(t, &googSubs.t, false)

			assert.Error(t, win0.SetContent(tsla))
			assert.NoError(t, win0.SetContent(amzn))

			assertFreeTab(t, amzn, false)
			assertFreeTab(t, tsla, false)
			assertFreeTab(t, goog, true)
			assertFreeTab(t, &googSubs.t, true)

			assertWindowContent(t, win0, amzn)
			assertWindowContent(t, win, tsla)
		})
	}
}

func TestComponentSetContentUnmount(t *testing.T) {
	c := browser.NewComponent(browserConfig())
	win0 := c.Focus()
	h1 := NewTestHandler()
	h2 := NewTestHandler()
	var closed int
	h1.CloseCallback = func() error {
		closed++
		return nil
	}
	assert.NoError(t, win0.SetContent(h1))
	assertWindowContent(t, win0, h1)
	assert.NoError(t, win0.SetContent(h2))
	assertWindowContent(t, win0, h2)
	assert.Equal(t, 1, closed)

	assert.NoError(t, win0.SetContent(h1))
	assertWindowContent(t, win0, h1)
	assert.Equal(t, 1, closed)
	content1, err := win0.Content()
	require.NoError(t, err)

	assert.NoError(t, win0.SetContent(content1))
	assertWindowContent(t, win0, h1)
	// closed and mounted again
	assert.Equal(t, 2, closed)

	win1, ok := c.Split(browserapi.OrientationBottom, c.Focus(), h2)
	require.True(t, ok)
	assert.Equal(t, 2, closed)

	assert.NoError(t, win0.Close())
	assert.Equal(t, 3, closed)

	assert.Equal(t, c.Focus(), win1)
}

func TestComponentHandlerClose(t *testing.T) {
	cfg := browser.DefaultConfig()

	for _, _tcase := range splitSuite {
		split := _tcase.split
		t.Run(_tcase.method, func(t *testing.T) {
			tsuite := []struct {
				name string
				fn   func(*testing.T, *browser.Component, browser.Window, *testCloser)
			}{
				{
					"nextFreeTab",
					func(t *testing.T, c *browser.Component, win browser.Window, h *testCloser) {
						assert.True(t, updateWithNextFreeTab(c, win))
					},
				}, {
					"EditWindowTabNext",
					func(t *testing.T, c *browser.Component, win browser.Window, h *testCloser) {
						assert.True(t, c.NextTab(win))
					},
				}, {
					"EditWindowTabPrev",
					func(t *testing.T, c *browser.Component, win browser.Window, h *testCloser) {
						assert.True(t, c.PreviousTab(win))
					},
				}, {
					"Window.SetContent",
					func(t *testing.T, c *browser.Component, win browser.Window, h *testCloser) {
						assert.NoError(t, win.SetContent(NewTestHandler()))
					},
				}, {
					"Window.Close",
					func(t *testing.T, c *browser.Component, win browser.Window, h *testCloser) {
						assert.NoError(t, win.Close())
					},
				}, {
					"RemoveWindowContent",
					func(t *testing.T, c *browser.Component, win browser.Window, h *testCloser) {
						assert.True(t, c.RemoveWindowContent(win))
					},
				}, {
					"Handle(exit=true)",
					func(t *testing.T, c *browser.Component, win browser.Window, h *testCloser) {
						h.Exit = true
						h.Handled = true
						exit, handled := c.Handle(term.Event{})
						assert.False(t, exit)
						assert.True(t, handled)
					},
				},
			}

			for _, tcase := range tsuite {
				t.Run(tcase.name, func(t *testing.T) {
					mock := &testCloser{}
					c := browser.NewComponent(cfg)
					uri1, err := workspaceapi.ParseURI("file:///Robinhood")
					require.NoError(t, err)
					uri2, err := workspaceapi.ParseURI("file:///Stash")
					require.NoError(t, err)

					c.NewTab(uri1, 'x', "Robinhood", NewTestHandler(), nil)
					c.NewTab(uri2, 'x', "Stash", NewTestHandler(), nil)
					win, ok := split(c, mock)
					require.True(t, ok)

					tcase.fn(t, c, win, mock)
					assert.Equal(t, 1, mock.closed)
				})
			}
		})
	}
}

func TestComponentMultipleWindow(t *testing.T) {
	w := term.NewStringWriter(20, 8)

	cfg := browser.DefaultConfig()
	cfg.WindowManagerConfig.TopLeft = 'O'
	cfg.WindowManagerConfig.FocusFrameCharSet.TopLeft = 'X'
	c := browser.NewComponent(cfg)
	c.Resize(20, 8)

	// w1 := c.Focus()
	var w2 browser.Window
	var ok bool
	// var w3 Window
	h2 := NewTestHandler()
	h3 := NewTestHandler()
	h3.Ch = 'C'

	tests := []comptest.TestCase{
		{
			nil /* X on top left window is overriden by union */, `
O──────────────────┐
│                  │
├──────────────────┤
│                  │
│                  │
│                  │
│                  │
└──────────────────┘`,
		}, {func() {
			w2, ok = c.Split(browserapi.OrientationBottom, c.Focus(), h2)
			require.True(t, ok)
		}, `
O──────────────────┐
│                  │
├──────────────────┤
│                  │
└──────────────────┘
X──────────────────┐
│AAAAAAAAAAAAAAAAAA│
└──────────────────┘`,
		}, {func() {
			/*w3 =*/ c.Split(browserapi.OrientationRight, c.Focus(), h3)
		}, `
O──────────────────┐
│                  │
├──────────────────┤
│                  │
└──────────────────┘
O────────┐X────────┐
│AAAAAAAA││CCCCCCCC│
└────────┘└────────┘`,
		}, {func() {
			assert.True(t, c.FocusLeft())

			var closeCallbacked int
			h2.Exit = true
			h2.CloseCallback = func() error {
				assert.NoError(t, w2.Close())
				closeCallbacked++
				return nil
			}
			h2.Handled = true
			_, handled := c.Handle(term.Event{})
			assert.True(t, handled)
			assert.Equal(t, 1, closeCallbacked)
		}, /* same as case 1, topleft on window X is overriden */ `
O──────────────────┐
│                  │
├──────────────────┤
│                  │
└──────────────────┘
X──────────────────┐
│CCCCCCCCCCCCCCCCCC│
└──────────────────┘`,
		},
	}

	comptest.TestComponent(t, c, w, tests)
}

func TestComponentPrompt(t *testing.T) {
	t.Run("panics if options are zero in length", func(t *testing.T) {
		c := browser.NewComponent(browser.DefaultConfig())
		assert.Panics(t, func() {
			c.Prompt("bla", []string{}, nil, nil)
		})

	})
	t.Run("panics if bindings and options are different lengths", func(t *testing.T) {
		c := browser.NewComponent(browser.DefaultConfig())
		assert.Panics(t, func() {
			c.Prompt("bla", []string{"a", "b"}, []term.KeyComb{{}}, nil)
		})
	})
	t.Run("panics if message is empty", func(t *testing.T) {
		c := browser.NewComponent(browser.DefaultConfig())
		assert.Panics(t, func() {
			c.Prompt("", []string{"a", "b"}, []term.KeyComb{{}, {}}, nil)
		})
	})
	t.Run("runs close callback when closed", func(t *testing.T) {
		c := browser.NewComponent(browser.DefaultConfig())
		c.Resize(20, 12)
		closeCalled := false
		c.Prompt("Albert Pla?", []string{"Buah!", "Hmm"}, nil,
			handler.FuncPromptHandler(
				func(idx int, option string) {},
				func() error {
					closeCalled = true
					return nil
				}))
		c.Close()
		assert.True(t, closeCalled)
	})
	t.Run("close callback can reopen the same prompt", func(t *testing.T) {
		c := browser.NewComponent(browser.DefaultConfig())
		c.Resize(20, 12)
		reopens := 0
		var open func()
		open = func() {
			c.Prompt("Stay?", []string{"Ok"}, nil,
				handler.FuncPromptHandler(
					func(int, string) {},
					func() error {
						if reopens == 0 {
							reopens++
							open()
						}
						return nil
					}))
		}
		open()
		require.Equal(t, 1, c.FloatingWindows())
		c.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
		assert.Equal(t, 1, reopens, "Esc must run the close callback once")
		assert.Equal(t, 1, c.FloatingWindows(),
			"prompt reopened from its close callback must stay on screen")
	})
	t.Run("select callback can reopen the same prompt", func(t *testing.T) {
		// Mirrors the stay-in-this-prompt pattern (e.g. a "copy URL"
		// button): OnSelect reopens the same message and OnClose
		// reopens unless the select advanced past the prompt. The
		// reopen from OnSelect must survive the dying window's
		// OnClose without stacking a duplicate.
		c := browser.NewComponent(browser.DefaultConfig())
		c.Resize(20, 12)
		advanced := false
		var open func()
		open = func() {
			c.Prompt("Stay?", []string{"Copy"}, nil,
				handler.FuncPromptHandler(
					func(int, string) {
						advanced = false
						open()
					},
					func() error {
						if !advanced {
							open()
						}
						return nil
					}))
		}
		open()
		require.Equal(t, 1, c.FloatingWindows())
		c.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
		assert.Equal(t, 1, c.FloatingWindows(),
			"exactly one prompt must remain after a stay-in-prompt select")
	})
	t.Run("Draw", func(t *testing.T) {
		w := term.NewStringWriter(24, 12)

		cfg := browser.DefaultConfig()
		cfg.WindowManagerConfig.NoMaxSize = false
		c := browser.NewComponent(cfg)
		c.Resize(20, 12)
		uri, err := workspaceapi.ParseURI("file:///Music")
		require.NoError(t, err)

		h := NewTestHandler()
		h.Ch = '8'
		tab := c.NewTab(uri, 'x', "music", h, nil)
		require.NoError(t, c.Focus().SetContent(tab))

		tests := []comptest.TestCase{
			{
				nil, `
┌━━━━━━━───────────┐    
│x music           │    
├──────────────────┤    
│888888888888888888│    
│888888888888888888│    
│888888888888888888│    
│888888888888888888│    
│888888888888888888│    
│888888888888888888│    
│888888888888888888│    
│888888888888888888│    
└──────────────────┘    `,
			}, {func() {
				c.Prompt("Virgen Maria?", []string{"Boh", "Meh"}, nil, handler.NopPromptHandler())
			}, `
┌──────────────────┐    
│x music           │    
├──────────────────┤    
█●██████████████████    
│                  │    
│  Virgen Maria?   │    
│                  │    
│                  │    
│    Boh    Meh    │    
└──────────────────┘    
│888888888888888888│    
└──────────────────┘    `,
			}, {func() {
				c.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
			}, `
┌━━━━━━━───────────┐    
│x music           │    
├──────────────────┤    
│888888888888888888│    
│888888888888888888│    
│888888888888888888│    
│888888888888888888│    
│888888888888888888│    
│888888888888888888│    
│888888888888888888│    
│888888888888888888│    
└──────────────────┘    `,
			}, {func() {
				c.Prompt("Tokischa?", []string{"Yay", "Nay"}, nil,
					handler.FuncPromptHandler(
						func(idx int, option string) {
							c.Prompt("Robert Love", []string{"YAS!"}, nil,
								handler.NopPromptHandler())
						},
						func() error { return nil }))
				c.Prompt("Rosalia?", []string{"Yay", "Nay"}, nil, handler.NopPromptHandler())
			}, `
┌──────────────────┐    
│x music           │    
├──────────────────┤    
█●██████████████████    
│                  │    
│  Rosalia?        │    
│                  │    
│                  │    
│    Yay    Nay    │    
└──────────────────┘    
│888888888888888888│    
└──────────────────┘    `,
			}, {func() {
				c.Resize(10, 6)
			}, `
┌────────┐              
│x musi  │              
├●███████┤              
│  Rosa  │              
│ YayNay │              
└────────┘              
                        
                        
                        
                        
                        
                        `,
			}, {func() {
				c.Resize(24, 8)
			}, `
┌──────────────────────┐
│x music               │
├●█████████████████████┤
│                      │
│  Rosalia?            │
│                      │
│     Yay      Nay     │
└──────────────────────┘
                        
                        
                        
                        `,
			}, {func() {
				c.Resize(20, 12)
			}, `
┌──────────────────┐    
│x music           │    
├──────────────────┤    
█●██████████████████    
│                  │    
│  Rosalia?        │    
│                  │    
│                  │    
│    Yay    Nay    │    
└──────────────────┘    
│888888888888888888│    
└──────────────────┘    `,
			}, {func() {
				c.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
			}, `
┌──────────────────┐    
│x music           │    
├──────────────────┤    
█●██████████████████    
│                  │    
│  Tokischa?       │    
│                  │    
│                  │    
│    Yay    Nay    │    
└──────────────────┘    
│888888888888888888│    
└──────────────────┘    `,
			}, {func() {
				c.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
			}, `
┌──────────────────┐    
│x music           │    
├──────────────────┤    
█●██████████████████    
│                  │    
│  Robert Love     │    
│                  │    
│                  │    
│       YAS!       │    
└──────────────────┘    
│888888888888888888│    
└──────────────────┘    `,
			}, {func() {
				c.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
			}, `
┌━━━━━━━───────────┐    
│x music           │    
├──────────────────┤    
│888888888888888888│    
│888888888888888888│    
│888888888888888888│    
│888888888888888888│    
│888888888888888888│    
│888888888888888888│    
│888888888888888888│    
│888888888888888888│    
└──────────────────┘    `,
			},
		}

		comptest.TestComponent(t, c, w, tests)
	})

	t.Run("does not open a prompt twice", func(t *testing.T) {
		w := term.NewStringWriter(24, 12)

		cfg := browser.DefaultConfig()
		cfg.NoMaxSize = false
		c := browser.NewComponent(cfg)
		c.Resize(20, 12)
		uri, err := workspaceapi.ParseURI("file:///Music")
		require.NoError(t, err)

		h := NewTestHandler()
		h.Ch = '8'
		tab := c.NewTab(uri, 'x', "music", h, nil)
		require.NoError(t, c.Focus().SetContent(tab))

		tests := []comptest.TestCase{
			{
				nil, `
┌━━━━━━━───────────┐    
│x music           │    
├──────────────────┤    
│888888888888888888│    
│888888888888888888│    
│888888888888888888│    
│888888888888888888│    
│888888888888888888│    
│888888888888888888│    
│888888888888888888│    
│888888888888888888│    
└──────────────────┘    `,
			}, {func() {
				c.Prompt("Twitch Streaming?", []string{"Yes", "No"}, nil,
					handler.NopPromptHandler())
				c.Prompt("Twitch Streaming?", []string{"Yes", "No"}, nil,
					handler.NopPromptHandler())
				c.Prompt("Twitch Streaming?", []string{"Yes", "No"}, nil,
					handler.NopPromptHandler())
			}, `
┌──────────────────┐    
│x music           │    
├──────────────────┤    
█●██████████████████    
│                  │    
│  Twitch          │    
│  Streaming?      │    
│                  │    
│                  │    
│    Yes    No     │    
└──────────────────┘    
└──────────────────┘    `,
			}, {func() {
				c.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
			}, `
┌━━━━━━━───────────┐    
│x music           │    
├──────────────────┤    
│888888888888888888│    
│888888888888888888│    
│888888888888888888│    
│888888888888888888│    
│888888888888888888│    
│888888888888888888│    
│888888888888888888│    
│888888888888888888│    
└──────────────────┘    `,
			}, {func() {
				c.Prompt("Twitch Streaming?", []string{"Yes", "No"}, nil, handler.NopPromptHandler())
				c.Prompt("Twitch Streaming?", []string{"Yes", "No"}, nil, handler.NopPromptHandler())
				c.Prompt("Twitch Streaming?", []string{"Yes", "No"}, nil, handler.NopPromptHandler())
			}, `
┌──────────────────┐    
│x music           │    
├──────────────────┤    
█●██████████████████    
│                  │    
│  Twitch          │    
│  Streaming?      │    
│                  │    
│                  │    
│    Yes    No     │    
└──────────────────┘    
└──────────────────┘    `,
			}, {func() {
				c.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
			}, `
┌━━━━━━━───────────┐    
│x music           │    
├──────────────────┤    
│888888888888888888│    
│888888888888888888│    
│888888888888888888│    
│888888888888888888│    
│888888888888888888│    
│888888888888888888│    
│888888888888888888│    
│888888888888888888│    
└──────────────────┘    `,
			},
		}

		comptest.TestComponent(t, c, w, tests)
	})
}

func TestComponentSetFocus(t *testing.T) {
	cfg := browser.DefaultConfig()
	c := browser.NewComponent(cfg)
	c.Resize(20, 8)
	win0 := c.Focus()
	win1, _ := c.Split(browserapi.OrientationDefault, c.Focus(), nil)
	assert.Equal(t, win1, c.Focus())

	prev := c.SetFocus(win0)
	assert.Equal(t, win1, prev)
	assert.Equal(t, win0, c.Focus())

	win2, _ := c.Split(browserapi.OrientationDefault, c.Focus(), nil)
	assert.Equal(t, win2, c.Focus())
	prev = c.SetFocus(win1)
	assert.Equal(t, win2, prev)
	assert.Equal(t, win1, c.Focus())
}

func TestComponentOnTabsClick(t *testing.T) {
	cfg := browser.DefaultConfig()
	var called int
	cfg.OnTabsClick = func(i int) bool {
		called++
		return true
	}
	c := browser.NewComponent(cfg)
	c.Resize(20, 8)

	require.Equal(t, 0, called)

	_, handled := c.Handle(term.Event{Type: term.EventMouse, Key: term.MouseLeft})
	assert.True(t, handled)
	assert.Equal(t, 1, called)
}

func TestComponentSplitNil(t *testing.T) {
	t.Run("Split with nil sets a wallpaper", func(t *testing.T) {
		w := term.NewStringWriter(24, 8)
		cfg := browser.DefaultConfig()
		cfg.Wallpaper = browser.Wallpaper{
			NewComponent: func() tui.Component {
				return component.NewStringWithConfig("BART", component.StringConfig{
					Alignment: component.AlignmentCentered,
				})
			},
		}
		c := browser.NewComponent(cfg)
		c.SetDefaultSplit(browserapi.OrientationLeft)
		c.Resize(20, 8)
		c.Split(browserapi.OrientationDefault, c.Focus(), nil)
		tests := []comptest.TestCase{
			{
				nil, `
┌──────────────────┐    
│                  │    
┌────────┐┌────────┤    
│        ││        │    
│  BART  ││  BART  │    
│        ││        │    
│        ││        │    
└────────┘└────────┘    `,
			},
		}
		comptest.TestComponent(t, c, w, tests)
	})
}

func TestFloatingPanicWallpaper(t *testing.T) {
	cfg := browser.DefaultConfig()
	c := browser.NewComponent(cfg)
	c.Resize(20, 8)

	fw := c.Floating(browser.StaticFloating(NewTestHandler(), 10, 10), browserapi.FloatingConfig{})
	c.SetFocus(fw)
	assert.NotPanics(t, func() {
		c.RemoveWindowContent(c.Focus())
	})
}

// this happens if content swaps the content of its own
// window before returning exit=true, in which case the underlying
// handler.wm closes the window but we miss updating i.e. a Tab
// and the Tab is never again accessible.
func TestHandleExitAfterContentSetIssue(t *testing.T) {
	cfg := browser.DefaultConfig()
	c := browser.NewComponent(cfg)
	c.Resize(20, 8)

	uri, err := workspaceapi.ParseURI("my:///thing")
	require.NoError(t, err)

	tab := c.NewTab(uri, 'x', "bla", NewTestHandler(), nil)

	mockHandler := contentSwapper{c: c, tab: tab}
	require.NoError(t, c.Focus().SetContent(&mockHandler))

	c.Handle(term.Event{})

	_, ok := tab.Window()
	assert.False(t, ok)
}

func prepareFocusShift(c *browser.Component, cmdWin browser.Window) {
	//if cmd {
	c.Focus().Close()
	//}
	c.SetFocus(invokeWindow(c, cmdWin))
}

func invokeWindow(c *browser.Component, cmdWin browser.Window) browser.Window {
	//if cmd {
	return cmdWin
	//}
	//return c.Focus()
}

func TestExposedRootNodeIssue(t *testing.T) {
	/* configured the following alias which causes a panic
	   layoutWorkspace:
	       - newWindow
	       - changeSplitOrientation h
	       - newWindow
	       - changeSplitOrientation v
	       - newWindow
	       - focusPrevWindow
	       - focusPrevWindow
	*/
	cfg := browser.DefaultConfig()
	c := browser.NewComponent(cfg)
	c.Resize(20, 8)

	cmdWin := c.Focus()

	focus := invokeWindow(c, cmdWin)
	_, ok := c.Split(browserapi.OrientationDefault, focus, NewTestHandler())
	assert.True(t, ok)

	c.SetDefaultSplit(browserapi.OrientationBottom)

	focus = invokeWindow(c, cmdWin)
	_, ok = c.Split(browserapi.OrientationDefault, focus, NewTestHandler())
	assert.True(t, ok)

	c.SetDefaultSplit(browserapi.OrientationRight)

	focus = invokeWindow(c, cmdWin)
	_, ok = c.Split(browserapi.OrientationDefault, focus, NewTestHandler())
	assert.True(t, ok)

	prepareFocusShift(c, cmdWin)
	c.FocusLeft()

	assert.PanicsWithValue(t, "trying to set focus to a closed window", func() {
		prepareFocusShift(c, cmdWin)
		c.FocusLeft()
	})
}

type contentSwapper struct {
	TestHandler
	tab *browser.Tab
	c   *browser.Component
}

func (c *contentSwapper) Handle(ev term.Event) (bool, bool) {
	_ = c.c.Focus().SetContent(c.tab)
	return true, true
}

func (c *contentSwapper) Close() error {
	return nil
}

func browserConfig() browser.Config {
	return browser.Config{}
}

// TestComponentRestoreTileLayoutEmptyLayout reproduces the
// "corrupted browser: cannot find focus window" panic raised from
// browser.Component.focus after RestoreTileLayout is invoked with a
// layout that does not yield any leaf with a non-zero WindowID.
//
// In production this surfaced when a user's session restored a
// normalized empty layout: the underlying handler.WindowManager kept
// a stale focus pointing at the pre-restore tile (now discarded), and
// any subsequent call to e.comp.Focus() in the IDE event loop would
// look up a Window ID missing from c.windows and panic.
func TestComponentRestoreTileLayoutEmptyLayout(t *testing.T) {
	c := browser.NewComponent(browserConfig())
	c.Resize(40, 20)

	_ = c.RestoreTileLayout(tcomponent.TileLayout{}, func(windowID uint64) browserapi.Handler {
		return nil
	})

	require.NotNil(t, c.Focus())
}

// TestComponentTabAutoCloseOnExit verifies that when a tab's inner
// handler returns exit=true the tab is dropped from the browser's
// tab list, not just from the focused window. Without this the tab
// would persist as an orphan with a closed handler.
func TestComponentTabAutoCloseOnExit(t *testing.T) {
	c := browser.NewComponent(browserConfig())
	c.Resize(40, 20)

	_, ok := c.Split(browserapi.OrientationRight, c.Focus(), NewTestHandler())
	require.True(t, ok)

	uri, err := workspaceapi.ParseURI("file:///exiting")
	require.NoError(t, err)

	h := NewTestHandler()
	h.Exit = true
	h.Handled = true
	tab := c.NewTab(uri, 'x', "exit-me", h, nil)
	require.NoError(t, c.Focus().SetContent(tab))

	require.Len(t, c.Tabs(), 1)

	exit, handled := c.Handle(term.Event{})
	require.True(t, handled)
	require.False(t, exit)

	assert.Empty(t, c.Tabs())
}

// TestComponentOnTabExit verifies that when a terminal is dropped by
// URI as a free tab or as direct window content, without relying on
// event routing.
func TestComponentOnTabExit(t *testing.T) {
	deadURI, err := workspaceapi.ParseURI("terminal:///dead")
	require.NoError(t, err)
	liveURI, err := workspaceapi.ParseURI("terminal:///live")
	require.NoError(t, err)

	t.Run("free tab", func(t *testing.T) {
		c := browser.NewComponent(browserConfig())
		c.Resize(40, 20)

		live := c.NewTab(liveURI, 'x', "live", NewTestHandler(), nil)
		require.NoError(t, c.Focus().SetContent(live))
		c.NewTab(deadURI, 'x', "dead", NewTestHandler(), nil)

		require.True(t, c.OnTabExit(deadURI))
		assert.Equal(t, liveURI.String(), c.Tabs()[0].URI().String())
	})

	t.Run("direct window content", func(t *testing.T) {
		c := browser.NewComponent(browserConfig())
		c.Resize(80, 20)

		first := c.Focus()
		_, ok := c.Split(browserapi.OrientationRight, first, NewTestHandler())
		require.True(t, ok)

		dead := NewTestHandler()
		dead.URIVal = deadURI
		require.NoError(t, first.SetContent(dead))

		require.True(t, c.OnTabExit(deadURI))
		assert.Equal(t, 2, c.Tiles(), "window must survive with swapped content")
	})
}

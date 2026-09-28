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

package gui

import (
	"context"
	"image"
	"sync"
	"testing"
	"time"

	ebiten "github.com/hajimehoshi/ebiten/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"github.com/unstablebuild/tcell/v3"
	"unstable.build/rune/internal/debug"
)

func TestUpdate(t *testing.T) {
	t.Run("events published before the first frame do not wake ebiten", func(t *testing.T) {
		var events []term.Event
		mock := mockHandler{
			assertDraw: func(term.Writer) {},
			assertEvent: func(ev term.Event) (bool, bool) {
				events = append(events, ev)
				return false, true
			},
		}
		gui, _ := newTestGUI(t, &mock)

		require.False(t, gui.started.Load())
		require.True(t, gui.PublishEvent(term.Event{Type: term.EventInterrupt}))
		require.True(t, gui.PublishEvent(term.Event{Type: term.EventKey, Ch: 'a', Raw: []byte("a")}))
		require.False(t, gui.started.Load(),
			"publishing must not mark ebiten as running")
		require.True(t, gui.interruptPending.Load())

		require.NoError(t, gui.Update())
		assert.True(t, gui.started.Load())
		assert.False(t, gui.interruptPending.Load(),
			"the first frame picks up interrupts published before it")
		require.Len(t, events, 1)
		assert.Equal(t, term.EventKey, events[0].Type)
	})

	t.Run("passes iteration in Draw context to root handler", func(t *testing.T) {
		var called int
		mock := mockHandler{assertDraw: func(w term.Writer) {
			actualIteration, ok := tui.IterationFromContext(w.Context())
			require.True(t, ok)
			assert.Equal(t, int64(0), actualIteration)
			called++
		}}
		gui, _ := newTestGUI(t, &mock)

		require.NoError(t, gui.Update())
		require.Equal(t, 1, called)
	})

	t.Run("Update DOES call Draw if ebiten calls Layout with DIFFERENT height/width", func(t *testing.T) {
		var called int
		mock := mockHandler{assertDraw: func(w term.Writer) {
			called++
		}}
		gui, _ := newTestGUI(t, &mock)

		require.NoError(t, gui.Update())
		require.Equal(t, 1, called)

		gui.Layout(1600, 900)

		require.NoError(t, gui.Update())
		require.Equal(t, 2, called)
	})

	t.Run("Update DOES NOT calls Draw if ebiten calls Layout with SAME height/width", func(t *testing.T) {
		var called int
		mock := mockHandler{assertDraw: func(w term.Writer) {
			called++
		}}
		gui, _ := newTestGUI(t, &mock)

		require.NoError(t, gui.Update())
		require.Equal(t, 1, called)

		gui.Layout(defaultWidth, defaultHeight)

		require.NoError(t, gui.Update())
		require.Equal(t, 1, called)
	})

	t.Run("delegates events to handler", func(t *testing.T) {
		var expectedIterationID int64
		var called int
		mock := mockHandler{
			assertDraw: func(w term.Writer) {
				actualIteration, ok := tui.IterationFromContext(w.Context())
				require.True(t, ok)
				assert.Equal(t, expectedIterationID, actualIteration)
			},
			assertEvent: func(ev term.Event) (exit, handled bool) {
				called++
				assert.Equal(t, term.EventKey, ev.Type)
				assert.Equal(t, term.KeyEnter, ev.Key)
				return
			},
		}
		gui, input := newTestGUI(t, &mock)

		input.events = action(press(ebiten.KeyEnter))
		require.NoError(t, gui.Update())
		require.Equal(t, 1, called)

		expectedIterationID++
		input.events = action(press(ebiten.KeyEnter, ebiten.KeyModSuper))
		require.NoError(t, gui.Update())
		require.Equal(t, 2, called)

		input.events = nil
		require.NoError(t, gui.Update())
		require.Equal(t, 2, called)
	})

	t.Run("delegates events to handler", func(t *testing.T) {
		var expectedIterationID int64
		var called int
		mock := mockHandler{
			assertDraw: func(w term.Writer) {
				actualIteration, ok := tui.IterationFromContext(w.Context())
				require.True(t, ok)
				assert.Equal(t, expectedIterationID, actualIteration)
			},
			assertEvent: func(ev term.Event) (exit, handled bool) {
				called++
				assert.Equal(t, term.EventKey, ev.Type)
				assert.Equal(t, term.KeyEnter, ev.Key)
				return
			},
		}
		gui, input := newTestGUI(t, &mock)

		input.events = action(press(ebiten.KeyEnter))
		require.NoError(t, gui.Update())
		require.Equal(t, 1, called)

		expectedIterationID++
		input.events = action(press(ebiten.KeyEnter, ebiten.KeyModSuper))
		require.NoError(t, gui.Update())
		require.Equal(t, 2, called)

		input.events = nil
		require.NoError(t, gui.Update())
		require.Equal(t, 2, called)
	})

	t.Run("process interrupts by calling Draw, with reset context", func(t *testing.T) {
		var called int
		mock := mockHandler{assertDraw: func(w term.Writer) {
			if called != 0 {
				_, ok := tui.IterationFromContext(w.Context())
				require.False(t, ok)

				_, ok = term.PayloadFromContext(w.Context())
				require.False(t, ok)
			}
			called++
		}}
		gui, _ := newTestGUI(t, &mock)

		require.NoError(t, gui.Update())
		require.Equal(t, 1, called)

		// simulate publish
		gui.pendingEvents = append(gui.pendingEvents, term.Event{Type: term.EventInterrupt})

		require.NoError(t, gui.Update())
		require.Equal(t, 2, called)
	})

	t.Run("if interrupt contains .Raw payload, this is passed along in next call to Draw", func(t *testing.T) {
		var called int
		mock := mockHandler{assertDraw: func(w term.Writer) {
			if called != 0 {
				payload, ok := term.PayloadFromContext(w.Context())
				require.True(t, ok)
				assert.Equal(t, "X1234", string(payload))
			}

			called++
		}}
		gui, _ := newTestGUI(t, &mock)

		require.NoError(t, gui.Update())
		require.Equal(t, 1, called)

		// simulate publish
		gui.pendingEvents = append(gui.pendingEvents, term.Event{
			Type: term.EventInterrupt,
			Raw:  []byte("X1234"),
		})

		require.NoError(t, gui.Update())
		require.Equal(t, 2, called)
	})

	t.Run("if interrupt contains iteration ID, this is passed along in next call to Draw", func(t *testing.T) {
		var called int
		var actualDrawContext context.Context
		mock := mockHandler{assertDraw: func(w term.Writer) {
			if called != 0 {
				actualIterationID, ok := tui.IterationFromContext(w.Context())
				require.True(t, ok)
				assert.Equal(t, int64(0), actualIterationID)
			}

			actualDrawContext = w.Context()
			called++
		}}
		gui, _ := newTestGUI(t, &mock)

		require.NoError(t, gui.Update())
		require.Equal(t, 1, called)

		payload, ok := term.PayloadFromContext(actualDrawContext)
		require.True(t, ok)

		// simulate publish
		gui.pendingEvents = append(gui.pendingEvents, term.Event{
			Type: term.EventInterrupt,
			Raw:  payload,
		})

		require.NoError(t, gui.Update())
		require.Equal(t, 2, called)
	})

	t.Run("if interrupt without iteration ID, mixed with regular event, calls Draw twice one with, one without iterationID", func(t *testing.T) {
		var called int
		mock := mockHandler{assertDraw: func(w term.Writer) {
			switch called {
			case 0:
				actualIterationID, ok := tui.IterationFromContext(w.Context())
				require.True(t, ok)
				assert.Equal(t, int64(0), actualIterationID)
			case 1:
				_, ok := tui.IterationFromContext(w.Context())
				require.False(t, ok)
			case 2:
				actualIterationID, ok := tui.IterationFromContext(w.Context())
				require.True(t, ok)
				assert.Equal(t, int64(1), actualIterationID)
			}
			called++
		}, assertEvent: func(ev term.Event) (bool, bool) {
			return false, true
		}}
		gui, _ := newTestGUI(t, &mock)

		require.NoError(t, gui.Update())
		require.Equal(t, 1, called)

		gui.pendingEvents = append(gui.pendingEvents, term.Event{
			Type: term.EventInterrupt,
		})
		gui.pendingEvents = append(gui.pendingEvents, term.Event{
			Type: term.EventKey,
			Ch:   'a',
			Raw:  []byte{'a'},
		})

		require.NoError(t, gui.Update())
		require.Equal(t, 3, called)
	})

	t.Run("if interrupt contains user function this is called before next call to Draw", func(t *testing.T) {
		var drawCalled int
		var userFnCalled int
		mock := mockHandler{assertDraw: func(w term.Writer) {
			if drawCalled != 0 {
				assert.Equal(t, 1, userFnCalled)
			}
			drawCalled++
		}}
		gui, _ := newTestGUI(t, &mock)

		require.NoError(t, gui.Update())
		require.Equal(t, 1, drawCalled)

		// simulate publish
		gui.pendingEvents = append(gui.pendingEvents, term.Event{
			Type: term.EventInterrupt,
			UserFunc: func() {
				userFnCalled++
			},
		})

		require.NoError(t, gui.Update())
		require.Equal(t, 2, drawCalled)
		assert.Equal(t, 1, userFnCalled)

		gui.PublishEvent(term.Event{Type: term.EventInterrupt})
		require.NoError(t, gui.Update())
		require.Equal(t, 3, drawCalled)
	})

	t.Run("atomic collapses N client interrupts into one repaint", func(t *testing.T) {
		var drawCalled int
		var userFnCalled int
		mock := mockHandler{assertDraw: func(w term.Writer) { drawCalled++ }}
		gui, _ := newTestGUI(t, &mock)

		require.NoError(t, gui.Update())
		require.Equal(t, 1, drawCalled)

		require.True(t, gui.PublishEvent(term.Event{Type: term.EventInterrupt}))
		require.True(t, gui.PublishEvent(term.Event{Type: term.EventInterrupt}))
		require.True(t, gui.PublishEvent(term.Event{Type: term.EventInterrupt}))
		require.NoError(t, gui.Update())
		require.Equal(t, 2, drawCalled)

		// the atomic was cleared by the previous tick's swap, so a tick
		// with no new interrupts must not repaint
		require.NoError(t, gui.Update())
		require.Equal(t, 2, drawCalled)

		// an interrupt carrying a UserFunc is not a client interrupt and
		// must still flow through the channel and run its callback
		require.True(t, gui.PublishEvent(term.Event{
			Type:     term.EventInterrupt,
			UserFunc: func() { userFnCalled++ },
		}))
		require.NoError(t, gui.Update())
		assert.Equal(t, 1, userFnCalled)
	})

	t.Run("returns ErrHandlerExited if handler exits", func(t *testing.T) {
		mock := mockHandler{
			assertEvent: func(ev term.Event) (bool, bool) {
				return true, true
			},
		}
		gui, input := newTestGUI(t, &mock)

		input.events = action(press(ebiten.KeyEnter))
		require.Equal(t, ErrHandlerExited, gui.Update())
	})
}

func TestSetForceFullRepaint(t *testing.T) {
	mock := mockHandler{
		assertDraw:  func(term.Writer) {},
		assertEvent: func(term.Event) (bool, bool) { return false, false },
	}
	g, _ := newTestGUI(t, &mock)
	g.Layout(640, 480)
	g.needsRender = false

	g.SetForceFullRepaint(true)

	assert.True(t, g.forceFullRepaint)
	assert.True(t, g.renderer.forceFullRepaint)
	assert.True(t, g.NeedsRender())
}

func TestRootHandlerSynchronization(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var handler *mockHandler
	handler = &mockHandler{
		assertDraw: func(_ term.Writer) {
			_ = handler.width
			_ = handler.height
		},
		assertEvent: func(ev term.Event) (bool, bool) {
			_ = handler.width
			_ = handler.height
			return false, false
		},
	}
	gui, err := New(handler, WithLocker(&mu))
	require.NoError(t, err)

	// simulates a write, with Resize
	go func() {
		for {
			select {
			case <-ctx.Done():
			default:
			}
			mu.Lock()
			handler.Resize(105, 47)
			mu.Unlock()
		}
	}()

	// read via gui.Draw, Update, and Layout
	for n := 0; n < 1000; n++ {
		gui.Layout(1000, 1000)
		gui.Update()
		gui.Draw(ebiten.NewImage(1000, 1000))
	}

	// running with -race should result in no data races
}

func TestLayout(t *testing.T) {
	t.Run("does not panic on layout 0 width and height", func(t *testing.T) {
		mock := mockHandler{}
		gui, _ := newTestGUI(t, &mock)
		assert.NotPanics(t, func() {
			gui.Layout(0, 0)
		})
	})

	t.Run("resizes underlying handler if width/height are different", func(t *testing.T) {
		mock := mockHandler{}
		gui, _ := newTestGUI(t, &mock)
		previousRenderer := gui.renderer

		mock.width = 0
		mock.height = 0
		gui.Layout(1200, 900)
		assert.NotZero(t, mock.width)
		assert.NotZero(t, mock.height)
		assert.NotSame(t, previousRenderer, gui.renderer)
		assert.Nil(t, previousRenderer.frame)
		assert.Nil(t, previousRenderer.drawer)
	})

	t.Run("does not resize underlying handler if width/height are the same", func(t *testing.T) {
		mock := mockHandler{}
		gui, _ := newTestGUI(t, &mock)

		mock.width = 0
		mock.height = 0
		gui.Layout(defaultWidth, defaultHeight)
		assert.Zero(t, mock.width)
		assert.Zero(t, mock.height)
	})
}

// TestSetFontUnknownFamilyDoesNotPanic is a regression test for
// RUNE-51. Attempting to switch to a font family that either does not
// exist on the system or produces degenerate metrics must surface an
// error through SetFont instead of crashing the process in
// cell.NewBufferWriter, and must leave the GUI in a usable state so
// subsequent draws still work.
func TestSetFontUnknownFamilyDoesNotPanic(t *testing.T) {
	mock := mockHandler{}
	gui, _ := newTestGUI(t, &mock)

	previousWriter := gui.writer
	require.NotNil(t, previousWriter)

	var err error
	assert.NotPanics(t, func() {
		err = gui.SetFont("this-font-family-does-not-exist-RUNE-51")
	})
	assert.Error(t, err,
		"SetFont must return an error for an unknown family")

	// The GUI must stay functional: subsequent resizes and draws must
	// not panic, which means the font manager was restored to a
	// known-good state by the normal reload path.
	assert.NotPanics(t, func() {
		gui.resize(1200, 900, gui.fontManager.DeviceScale())
	})
}

func TestCloseRestoresColorValues(t *testing.T) {
	original := tcell.GetColorValues()
	t.Cleanup(func() { tcell.SetColorValues(original) })

	theme := Theme{
		Foreground: tcell.ColorWhite,
		Background: tcell.ColorBlack,
		Cursor:     tcell.ColorRed,
		Colors: map[tcell.Color]tcell.Color{
			tcell.ColorRed: tcell.NewHexColor(0x123456),
		},
	}
	g, err := New(&mockHandler{}, WithColorThemes("test", map[string]Theme{"test": theme}))
	require.NoError(t, err)
	require.NotEqual(t, original[tcell.ColorRed], tcell.GetColorValues()[tcell.ColorRed])

	require.NoError(t, g.Close())
	assert.Equal(t, original, tcell.GetColorValues())
	assert.Nil(t, g.renderer)
	require.NoError(t, g.Close(), "Close must be idempotent")
}

// TestCellPixelSizeMatchesImagePlacement pins that the cell size the
// kitty graphics protocol advertises is the pitch image placements are
// scaled by, so a client sizing an image to N cells gets exactly N
// cells, and that it follows font changes.
func TestCellPixelSizeMatchesImagePlacement(t *testing.T) {
	gui, _ := newTestGUI(t, &mockHandler{})

	w, h := gui.CellPixelSize()
	require.Positive(t, w)
	require.Positive(t, h)
	cells := cellRectToPixels(image.Rect(0, 0, 3, 2), gui.fontManager, 0, 0)
	assert.Equal(t, 3*w, cells.Dx())
	assert.Equal(t, 2*h, cells.Dy())

	require.NoError(t, gui.IncreaseFontSize())
	w2, h2 := gui.CellPixelSize()
	assert.Greater(t, w2*h2, w*h, "a larger font means larger cells")
}

// stubWindowClosing installs a processWindowClosed that reports one
// pending close request for ev, mirroring ebiten consuming the closing
// flag on the first read of a frame.
func stubWindowClosing(gui *GUI, ev term.Event) {
	closing := true
	gui.processWindowClosed = func() []term.Event {
		if !closing {
			return nil
		}
		closing = false
		return []term.Event{ev}
	}
}

func TestCloseRequest(t *testing.T) {
	quit := term.Event{Type: term.EventKey, Mod: term.ModMeta, Ch: 'q'}

	t.Run("publishes the configured event to the handler", func(t *testing.T) {
		var got []term.Event
		mock := mockHandler{
			assertDraw:  func(term.Writer) {},
			assertEvent: func(ev term.Event) (bool, bool) { got = append(got, ev); return false, true },
		}
		gui, _ := newTestGUI(t, &mock)
		stubWindowClosing(gui, quit)

		require.NoError(t, gui.Update())
		assert.Equal(t, []term.Event{quit}, got)

		// The request is consumed: a later tick must not republish it.
		require.NoError(t, gui.Update())
		assert.Equal(t, []term.Event{quit}, got)
	})

	t.Run("keeps running when the handler declines to exit", func(t *testing.T) {
		mock := mockHandler{
			assertDraw:  func(term.Writer) {},
			assertEvent: func(term.Event) (bool, bool) { return false, true },
		}
		gui, _ := newTestGUI(t, &mock)
		stubWindowClosing(gui, quit)

		assert.NoError(t, gui.Update())
	})

	t.Run("exits when the handler exits", func(t *testing.T) {
		mock := mockHandler{
			assertDraw:  func(term.Writer) {},
			assertEvent: func(term.Event) (bool, bool) { return true, true },
		}
		gui, _ := newTestGUI(t, &mock)
		stubWindowClosing(gui, quit)

		assert.ErrorIs(t, gui.Update(), ErrHandlerExited)
	})

	t.Run("without the option no close event is produced", func(t *testing.T) {
		var got []term.Event
		mock := mockHandler{
			assertDraw:  func(term.Writer) {},
			assertEvent: func(ev term.Event) (bool, bool) { got = append(got, ev); return false, true },
		}
		gui, _ := newTestGUI(t, &mock)

		require.NoError(t, gui.Update())
		assert.Empty(t, got)
	})

	t.Run("option installs the close request processor", func(t *testing.T) {
		mock := mockHandler{
			assertDraw:  func(term.Writer) {},
			assertEvent: func(term.Event) (bool, bool) { return false, true },
		}
		gui, _ := newTestGUI(t, &mock)
		require.NoError(t, WithCloseRequestEvent(quit)(gui))

		require.True(t, gui.closingHandled)
		require.NotNil(t, gui.processWindowClosed)
		// Headless: no close request is pending.
		assert.Empty(t, gui.processWindowClosed())
	})
}

func newTestGUI(t *testing.T, mock *mockHandler) (*GUI, *mockInputManager) {
	gui, err := New(mock)
	require.NoError(t, err)

	ret := &mockInputManager{}
	gui.input.input = ret

	return gui, ret
}

func newLockedTestGUI(t *testing.T, mu sync.Locker) *GUI {
	t.Helper()
	mock := &mockHandler{
		assertDraw:  func(term.Writer) {},
		assertEvent: func(term.Event) (bool, bool) { return false, true },
	}
	gui, err := New(mock, WithLocker(mu))
	require.NoError(t, err)
	gui.input.input = &mockInputManager{}
	// Settle any redraw owed by construction so the next tick is idle.
	require.NoError(t, gui.Update())
	return gui
}

// updateWhileHeld runs one tick on another goroutine while the caller
// holds the UI lock, reporting whether it finished without acquiring it.
func updateWhileHeld(t *testing.T, gui *GUI) bool {
	t.Helper()
	done := make(chan error, 1)
	go debug.CapturePanicReport(func() { done <- gui.Update() })
	select {
	case err := <-done:
		require.NoError(t, err)
		return true
	case <-time.After(2 * time.Second):
		return false
	}
}

// The render loop wakes on every vsync regardless of whether anything
// happened. Taking the UI lock on a frame with nothing to do contends
// with the extension RPC goroutines that need it, for no benefit.
func TestUpdateSkipsUILockOnIdleFrame(t *testing.T) {
	var mu sync.Mutex
	gui := newLockedTestGUI(t, &mu)

	mu.Lock()
	defer mu.Unlock()
	assert.True(t, updateWhileHeld(t, gui),
		"an idle tick must not wait on the UI lock")
}

// The skip must be an optimization, not a hole in the locking: a tick
// with an event to route still mutates UI state and must serialize.
func TestUpdateTakesUILockWhenEventPending(t *testing.T) {
	var mu sync.Mutex
	gui := newLockedTestGUI(t, &mu)
	gui.updateChan <- term.Event{Type: term.EventKey}

	mu.Lock()
	assert.False(t, updateWhileHeld(t, gui),
		"a tick that routes an event must hold the UI lock")
	mu.Unlock()
}

// A drag in flight produces no pending events and owes no redraw, so the
// drag probe is the only thing keeping its observer callbacks — which
// reach into browser window state — under the UI lock.
func TestUpdateTakesUILockWhileDragging(t *testing.T) {
	t.Run("drag in progress", func(t *testing.T) {
		var mu sync.Mutex
		gui := newLockedTestGUI(t, &mu)
		gui.drag.position = func() (int, int, bool) { return 10, 10, true }

		mu.Lock()
		assert.False(t, updateWhileHeld(t, gui),
			"a tick with a drag transition must hold the UI lock")
		mu.Unlock()
	})

	t.Run("drop pending after the drag ended", func(t *testing.T) {
		var mu sync.Mutex
		gui := newLockedTestGUI(t, &mu)
		// The host reports paths a tick or more after the drag ends, by
		// which point nothing else marks the frame as busy.
		gui.drag.paths = func() []string { return []string{"/tmp/a.png"} }

		mu.Lock()
		assert.False(t, updateWhileHeld(t, gui),
			"a tick that delivers a drop must hold the UI lock")
		mu.Unlock()
	})
}

type mockHandler struct {
	width, height int
	assertDraw    func(term.Writer)
	assertEvent   func(term.Event) (bool, bool)
}

func (m *mockHandler) Resize(width, height int) {
	m.width, m.height = width, height
}

func (m *mockHandler) Draw(w term.Writer) {
	m.assertDraw(w)
}

func (m *mockHandler) Handle(ev term.Event) (exit, handled bool) {
	return m.assertEvent(ev)
}

func (m *mockHandler) Cursor() (c term.Coordinates, s term.CursorStyle, show bool) {
	return
}

func (m *mockHandler) Selection() (string, bool) {
	return "", false
}

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

package handler

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/handler/handlertest"
	"github.com/unstablebuild/rune-go-sdk/term"
	rhandlertest "unstable.build/rune/internal/handler/handlertest"
	"unstable.build/rune/internal/sounds"
)

func TestTabsOnClick(t *testing.T) {
	t.Run("dispatches click event out of any tabs as -1 index", func(t *testing.T) {
		h := NewTabs()
		var called int
		h.OnClick = func(tabIdx int) bool {
			called++
			assert.Equal(t, -1, tabIdx)
			return true
		}

		_, handled := h.Handle(term.Event{
			Type:   term.EventMouse,
			Key:    term.MouseLeft,
			MouseX: 100000,
		})
		assert.True(t, handled)

		assert.Equal(t, 1, called)
	})

	t.Run("dispatches click event on tab with tab index", func(t *testing.T) {
		h := NewTabs()
		h.Resize(30, 2)
		var called int
		h.OnClick = func(tabIdx int) bool {
			called++
			assert.Equal(t, 1, tabIdx)
			return true
		}

		assert.Equal(t, 0, h.Add(0, "Burning"))
		assert.Equal(t, 1, h.Add(0, "Man"))
		assert.Equal(t, 2, h.Add('x', "2024"))
		handlertest.DrawHandler(h, 30, 2)

		_, handled := h.Handle(term.Event{
			Type:   term.EventMouse,
			Key:    term.MouseLeft,
			MouseX: 10,
		})
		assert.True(t, handled)
		assert.Zero(t, called, "a press is no click")

		_, handled = h.Handle(term.Event{
			Type:   term.EventMouse,
			Key:    term.MouseRelease,
			MouseX: 10,
		})
		assert.True(t, handled)
		assert.Equal(t, 1, called)
	})

	t.Run("mouse drag dispatches OnClick only once", func(t *testing.T) {
		h := NewTabs()
		var called int
		h.OnClick = func(tabIdx int) bool {
			called++
			return true
		}

		_, handled := h.Handle(term.Event{Type: term.EventMouse, Key: term.MouseLeft})
		assert.True(t, handled)

		handled, _ = h.Handle(term.Event{Type: term.EventMouse, Key: term.MouseLeft})
		assert.False(t, handled)

		handled, _ = h.Handle(term.Event{Type: term.EventMouse, Key: term.MouseLeft})
		assert.False(t, handled)

		handled, _ = h.Handle(term.Event{Type: term.EventMouse, Key: term.MouseLeft})
		assert.False(t, handled)

		assert.Equal(t, 1, called)
	})

	t.Run("MouseLeft, MouseRelease, MouseLeft dispatches OnClick twice", func(t *testing.T) {
		h := NewTabs()
		var called int
		h.OnClick = func(tabIdx int) bool {
			called++
			return true
		}

		_, handled := h.Handle(term.Event{Type: term.EventMouse, Key: term.MouseLeft})
		assert.True(t, handled)

		_, handled = h.Handle(term.Event{Type: term.EventMouse, Key: term.MouseLeft})
		assert.False(t, handled)

		_, handled = h.Handle(term.Event{Type: term.EventMouse, Key: term.MouseRelease})
		assert.False(t, handled)

		_, handled = h.Handle(term.Event{Type: term.EventMouse, Key: term.MouseLeft})
		assert.True(t, handled)

		assert.Equal(t, 2, called)
	})
}

func TestTabsOnIconClick(t *testing.T) {
	const width, height = 30, 2
	const (
		focusA = `━━━━━━━                       
A alpha  B beta  C gamma      `
		focusB = `         ━━━━━━               
A alpha  B beta  C gamma      `
	)
	// Icons sit at A:0, B:9 and C:17 on row 1, under row 0's highlight.
	mouse := func(key term.Key, mod term.Modifier, x, y int) func(*Tabs) {
		return func(h *Tabs) {
			h.Handle(term.Event{
				Type: term.EventMouse, Key: key, Mod: mod, MouseX: x, MouseY: y,
			})
		}
	}
	press := func(x, y int) func(*Tabs) { return mouse(term.MouseLeft, 0, x, y) }
	release := func(x, y int) func(*Tabs) { return mouse(term.MouseRelease, 0, x, y) }
	// hover is the motion reported with no button held.
	hover := func(x, y int) func(*Tabs) { return mouse(0, 0, x, y) }

	for _, tc := range []struct {
		name        string
		wide        bool
		noIconClick bool
		// removeOnIconClick closes the clicked tab like the browser.
		removeOnIconClick bool
		steps             []func(*Tabs)
		wantIconClicks    []int
		wantClicks        []int
		want              string
	}{
		{
			name:           "click on an unfocused tab's icon",
			steps:          []func(*Tabs){press(9, 1), release(9, 1)},
			wantIconClicks: []int{1},
			want:           focusA,
		},
		{
			name:           "click on the focused tab's icon",
			steps:          []func(*Tabs){press(0, 1), release(0, 1)},
			wantIconClicks: []int{0},
			want:           focusA,
		},
		{
			name:  "press without release",
			steps: []func(*Tabs){press(9, 1)},
			want:  focusA,
		},
		{
			name:  "release without press",
			steps: []func(*Tabs){release(9, 1)},
			want:  focusA,
		},
		{
			name:           "drag reported on the same cell",
			steps:          []func(*Tabs){press(9, 1), press(9, 1), release(9, 1)},
			wantIconClicks: []int{1},
			want:           focusA,
		},
		{
			name:  "drag onto the tab's name",
			steps: []func(*Tabs){press(9, 1), press(11, 1), release(11, 1)},
			want:  focusA,
		},
		{
			name: "drag off and back onto the icon",
			steps: []func(*Tabs){
				press(9, 1), press(10, 1), press(9, 1), release(9, 1),
			},
			want: focusA,
		},
		{
			name:  "drag onto another tab's icon",
			steps: []func(*Tabs){press(9, 1), press(17, 1), release(17, 1)},
			want:  focusA,
		},
		{
			name:  "drag onto the highlight row",
			steps: []func(*Tabs){press(9, 1), press(9, 0), release(9, 0)},
			want:  focusA,
		},
		{
			name:  "release on another tab's icon",
			steps: []func(*Tabs){press(9, 1), release(0, 1)},
			want:  focusA,
		},
		{
			name:  "release past the last tab",
			steps: []func(*Tabs){press(9, 1), release(27, 1)},
			want:  focusA,
		},
		{
			name: "wheel while held",
			steps: []func(*Tabs){
				press(9, 1), mouse(term.MouseWheelDown, 0, 9, 1), release(9, 1),
			},
			want: focusA,
		},
		{
			name: "right button while held",
			steps: []func(*Tabs){
				press(9, 1), mouse(term.MouseRight, 0, 9, 1), release(9, 1),
			},
			want: focusA,
		},
		{
			// The release landed outside the bar, and the hover that
			// followed shows the button is up.
			name:  "missed release then hover",
			steps: []func(*Tabs){press(9, 1), hover(9, 1), release(9, 1)},
			want:  focusA,
		},
		{
			name: "missed release then a click on the icon",
			steps: []func(*Tabs){
				press(9, 1), hover(9, 1), press(9, 1), release(9, 1),
			},
			wantIconClicks: []int{1},
			want:           focusA,
		},
		{
			name: "missed release then a click on the name",
			steps: []func(*Tabs){
				press(9, 1), hover(11, 1), press(11, 1), release(11, 1),
			},
			wantClicks: []int{1},
			want:       focusB,
		},
		{
			name: "double click",
			steps: []func(*Tabs){
				press(9, 1), release(9, 1), press(9, 1), release(9, 1),
			},
			wantIconClicks: []int{1, 1},
			want:           focusA,
		},
		{
			name:  "right click on the icon",
			steps: []func(*Tabs){mouse(term.MouseRight, 0, 9, 1), release(9, 1)},
			want:  focusA,
		},
		{
			name: "modified click on the icon",
			steps: []func(*Tabs){
				mouse(term.MouseLeft, term.ModCtrl, 9, 1), release(9, 1),
			},
			want: focusA,
		},
		{
			name:       "click on the name focuses the tab",
			steps:      []func(*Tabs){press(11, 1), release(11, 1)},
			wantClicks: []int{1},
			want:       focusB,
		},
		{
			name:       "click on the highlight row above the icon focuses the tab",
			steps:      []func(*Tabs){press(9, 0), release(9, 0)},
			wantClicks: []int{1},
			want:       focusB,
		},
		{
			name:       "click past the last tab",
			steps:      []func(*Tabs){press(27, 1), release(27, 1)},
			wantClicks: []int{-1},
			want:       focusA,
		},
		{
			name:        "without OnIconClick the icon focuses the tab",
			noIconClick: true,
			steps:       []func(*Tabs){press(9, 1), release(9, 1)},
			wantClicks:  []int{1},
			want:        focusB,
		},
		{
			// C slides under the pointer once B is gone.
			name: "tab removed while held",
			steps: []func(*Tabs){
				press(9, 1), func(h *Tabs) { h.Remove(1) }, release(9, 1),
			},
			want: `━━━━━━━                       
A alpha  C gamma              `,
		},
		{
			name: "tab moved while held",
			steps: []func(*Tabs){
				press(9, 1), func(h *Tabs) { h.MoveRight(1) }, release(9, 1),
			},
			want: `━━━━━━━                       
A alpha  C gamma  B beta      `,
		},
		{
			name: "tab moved left while held",
			steps: []func(*Tabs){
				press(9, 1), func(h *Tabs) { h.MoveLeft(2) }, release(9, 1),
			},
			want: `━━━━━━━                       
A alpha  C gamma  B beta      `,
		},
		{
			name: "tab moved to another index while held",
			steps: []func(*Tabs){
				press(9, 1), func(h *Tabs) { h.MoveTo(2, 1) }, release(9, 1),
			},
			want: `━━━━━━━                       
A alpha  C gamma  B beta      `,
		},
		{
			name: "all tabs removed while held",
			steps: []func(*Tabs){
				press(9, 1), func(h *Tabs) { h.RemoveAll() }, release(9, 1),
			},
			want: `                              
                              `,
		},
		{
			// Appending leaves every index in place.
			name: "tab added while held",
			steps: []func(*Tabs){
				press(9, 1), func(h *Tabs) { h.Add('D', "delta") }, release(9, 1),
			},
			wantIconClicks: []int{1},
			want: `━━━━━━━                       
A alpha  B bet  C gam  D del  `,
		},
		{
			// The next tab slides under the pointer, and clicking
			// again closes it too.
			name:              "clicks close tab after tab",
			removeOnIconClick: true,
			steps: []func(*Tabs){
				press(9, 1), release(9, 1), press(9, 1), release(9, 1),
			},
			wantIconClicks: []int{1, 1},
			want: `━━━━━━━                       
A alpha                       `,
		},
		{
			name:           "wide icon drag across its cells",
			wide:           true,
			steps:          []func(*Tabs){press(10, 1), press(11, 1), release(11, 1)},
			wantIconClicks: []int{1},
		},
		{
			name:  "wide icon drag off its cells",
			wide:  true,
			steps: []func(*Tabs){press(11, 1), press(12, 1), release(12, 1)},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := NewTabs()
			h.SetBorder(false)
			h.Resize(width, height)
			for i, name := range []string{"alpha", "beta", "gamma"} {
				icon := 'A' + rune(i)
				if tc.wide {
					icon = '界'
				}
				h.Add(icon, name)
			}
			var iconClicks, clicks []int
			h.OnClick = func(idx int) bool {
				clicks = append(clicks, idx)
				return true
			}
			if !tc.noIconClick {
				h.OnIconClick = func(idx int) {
					iconClicks = append(iconClicks, idx)
					if tc.removeOnIconClick {
						h.Remove(idx)
					}
				}
			}

			handlertest.DrawHandler(h, width, height)
			for _, step := range tc.steps {
				step(h)
				handlertest.DrawHandler(h, width, height)
			}

			assert.Equal(t, tc.wantIconClicks, iconClicks, "icon clicks")
			assert.Equal(t, tc.wantClicks, clicks, "clicks")
			if tc.want != "" {
				assert.Equal(t, tc.want, handlertest.DrawHandler(h, width, height))
			}
		})
	}
}

func TestTabsIconPressHandled(t *testing.T) {
	h := NewTabs()
	h.SetBorder(false)
	h.Resize(30, 2)
	h.Add('A', "alpha")
	h.Add('B', "beta")
	h.OnIconClick = func(int) {}
	handlertest.DrawHandler(h, 30, 2)

	for _, ev := range []term.Event{
		{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 9, MouseY: 1},
		{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 9, MouseY: 1},
		{Type: term.EventMouse, Key: term.MouseRelease, MouseX: 9, MouseY: 1},
	} {
		quit, handled := h.Handle(ev)
		assert.False(t, quit)
		assert.True(t, handled, "%+v", ev)
	}
}

func TestTabsPointerShape(t *testing.T) {
	// Icons sit at A:0, B:9 and C:17 on row 1, as in TestTabsOnIconClick.
	mouse := func(key term.Key, x, y int) term.Event {
		return term.Event{Type: term.EventMouse, Key: key, MouseX: x, MouseY: y}
	}
	for _, tc := range []struct {
		name        string
		noIconClick bool
		// steps lead to the event whose pointer shape is checked.
		steps []term.Event
		ev    term.Event
		want  term.PointerShape
		set   bool
	}{
		{name: "hover over an icon", ev: mouse(0, 9, 1), want: term.PointerLink, set: true},
		{
			name: "press on an icon", ev: mouse(term.MouseLeft, 17, 1),
			want: term.PointerLink, set: true,
		},
		{name: "hover over a name", ev: mouse(0, 11, 1), want: term.PointerLink, set: true},
		{
			name: "hover over the highlight row", ev: mouse(0, 11, 0),
			want: term.PointerLink, set: true,
		},
		{
			name: "hover over the tab in focus", ev: mouse(0, 2, 1),
			want: term.PointerLink, set: true,
		},
		{name: "hover past the tabs", ev: mouse(0, 28, 1)},
		{name: "a leave below the bar", ev: mouse(0, 11, 2)},
		{
			name: "hover over an icon that does not close", noIconClick: true,
			ev: mouse(0, 9, 1), want: term.PointerLink, set: true,
		},
		{
			name: "press on a name", ev: mouse(term.MouseLeft, 11, 1),
			want: term.PointerLink, set: true,
		},
		{
			name:  "a press dragged onto another tab",
			steps: []term.Event{mouse(term.MouseLeft, 11, 1)},
			ev:    mouse(term.MouseLeft, 19, 1),
		},
		{
			name:  "a press dragged back onto its tab",
			steps: []term.Event{mouse(term.MouseLeft, 11, 1), mouse(term.MouseLeft, 19, 1)},
			ev:    mouse(term.MouseLeft, 12, 1), want: term.PointerLink, set: true,
		},
		{
			name:  "a press past the tabs dragged onto one",
			steps: []term.Event{mouse(term.MouseLeft, 28, 1)},
			ev:    mouse(term.MouseLeft, 11, 1),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := NewTabs()
			h.SetBorder(false)
			h.Resize(30, 2)
			for i, name := range []string{"alpha", "beta", "gamma"} {
				h.Add('A'+rune(i), name)
			}
			if !tc.noIconClick {
				h.OnIconClick = func(int) {}
			}
			handlertest.DrawHandler(h, 30, 2)
			for _, ev := range tc.steps {
				h.Handle(ev)
				handlertest.DrawHandler(h, 30, 2)
			}

			p := new(term.Pointer)
			ev := tc.ev
			ev.Context = term.ContextWithPointer(context.Background(), p)
			h.Handle(ev)
			shape, set := p.Shape()
			assert.Equal(t, tc.set, set)
			assert.Equal(t, tc.want, shape)
		})
	}
}

func TestTabsClickSounds(t *testing.T) {
	const width, height = 30, 2
	// Tabs sit at A:0-6, B:9-14 and C:17-23, each separator part of the
	// tab before it, with A in focus.
	mouse := func(key term.Key, x, y int) func(*Tabs) {
		return func(h *Tabs) {
			h.Handle(term.Event{Type: term.EventMouse, Key: key, MouseX: x, MouseY: y})
		}
	}
	press := func(x int) func(*Tabs) { return mouse(term.MouseLeft, x, 1) }
	release := func(x int) func(*Tabs) { return mouse(term.MouseRelease, x, 1) }
	hover := func(x int) func(*Tabs) { return mouse(0, x, 1) }
	for _, tc := range []struct {
		name       string
		iconClick  bool
		steps      []func(*Tabs)
		wantClicks []int
		wantIcons  []int
		wantSounds []string
	}{
		{
			name:       "a click on a tab not in focus",
			steps:      []func(*Tabs){hover(11), press(11), release(11)},
			wantClicks: []int{1},
			wantSounds: []string{"hover", "click"},
		},
		{
			name:       "a click on the tab in focus",
			steps:      []func(*Tabs){hover(2), press(2), release(2)},
			wantClicks: []int{0},
			wantSounds: []string{"click"},
		},
		{
			name:       "a press is no click",
			steps:      []func(*Tabs){hover(11), press(11)},
			wantSounds: []string{"hover"},
		},
		{
			name:       "a press dragged onto another tab",
			steps:      []func(*Tabs){hover(11), press(11), press(19), release(19)},
			wantSounds: []string{"hover", "hover"},
		},
		{
			name: "a press dragged off its tab and back",
			steps: []func(*Tabs){
				hover(11), press(11), press(19), press(12), release(12),
			},
			wantClicks: []int{1},
			wantSounds: []string{"hover", "click"},
		},
		{
			name: "a release below the bar",
			steps: []func(*Tabs){
				hover(11), press(11), mouse(term.MouseLeft, 11, 4), mouse(term.MouseRelease, 11, 4),
			},
			wantSounds: []string{"hover"},
		},
		{
			// The release went elsewhere, and the motion that followed
			// shows the button is up.
			name:       "a missed release, then a motion",
			steps:      []func(*Tabs){press(11), hover(11), release(11)},
			wantSounds: []string{"hover"},
		},
		{
			name: "the wheel ends a press",
			steps: []func(*Tabs){
				hover(11), press(11), mouse(term.MouseWheelDown, 11, 1), release(11),
			},
			wantSounds: []string{"hover"},
		},
		{
			name: "a modified press is no click",
			steps: []func(*Tabs){
				hover(11), func(h *Tabs) {
					h.Handle(term.Event{
						Type: term.EventMouse, Key: term.MouseLeft, Mod: term.ModCtrl, MouseX: 11, MouseY: 1,
					})
				}, release(11),
			},
			wantSounds: []string{"hover"},
		},
		{
			name:       "moving across a tab plays its sound once",
			steps:      []func(*Tabs){hover(9), hover(11), hover(14), hover(15)},
			wantSounds: []string{"hover"},
		},
		{
			name:       "moving from tab to tab",
			steps:      []func(*Tabs){hover(11), hover(19)},
			wantSounds: []string{"hover", "hover"},
		},
		{
			name:  "the tab in focus and past the tabs are silent",
			steps: []func(*Tabs){hover(2), hover(27)},
		},
		{
			name:       "leaving the bar and coming back",
			steps:      []func(*Tabs){hover(11), mouse(0, 11, height), hover(11)},
			wantSounds: []string{"hover", "hover"},
		},
		{
			name:       "a click on an icon",
			iconClick:  true,
			steps:      []func(*Tabs){press(9), release(9)},
			wantIcons:  []int{1},
			wantSounds: []string{"click"},
		},
		{
			name: "a bar rebuilt after a click keeps the pointer on the clicked tab",
			steps: []func(*Tabs){
				hover(11), press(11), release(11), func(h *Tabs) {
					h.RemoveAll()
					for i, name := range []string{"alpha", "beta", "gamma"} {
						h.Add('A'+rune(i), name)
					}
					h.SetFocus(1)
				}, hover(12),
			},
			wantClicks: []int{1},
			wantSounds: []string{"hover", "click"},
		},
		{
			name: "the clicked tab is silent until the pointer comes back to it",
			steps: []func(*Tabs){
				hover(11), press(11), release(11), func(h *Tabs) {
					h.ResetFocus()
					h.SetFocus(0)
				}, hover(12), hover(19), hover(12),
			},
			wantClicks: []int{1},
			wantSounds: []string{"hover", "click", "hover", "hover"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := NewTabs()
			h.SetBorder(false)
			h.Resize(width, height)
			for i, name := range []string{"alpha", "beta", "gamma"} {
				h.Add('A'+rune(i), name)
			}
			var clicks, icons []int
			var played []string
			h.OnClick = func(idx int) bool {
				clicks = append(clicks, idx)
				return true
			}
			if tc.iconClick {
				h.OnIconClick = func(idx int) { icons = append(icons, idx) }
			}
			h.Player = sounds.PlayerFunc(func(name string) { played = append(played, name) })
			h.Sounds = TabSounds{Hover: "hover", Click: "click"}

			handlertest.DrawHandler(h, width, height)
			for _, step := range tc.steps {
				step(h)
				handlertest.DrawHandler(h, width, height)
			}
			assert.Equal(t, tc.wantClicks, clicks, "clicks")
			assert.Equal(t, tc.wantIcons, icons, "icon clicks")
			assert.Equal(t, tc.wantSounds, played, "sounds")
		})
	}
}

func TestTabsWithoutSounds(t *testing.T) {
	h := NewTabs()
	h.SetBorder(false)
	h.Resize(30, 2)
	h.Add('A', "alpha")
	h.Add('B', "beta")
	var played []string
	h.Player = sounds.PlayerFunc(func(name string) { played = append(played, name) })
	handlertest.DrawHandler(h, 30, 2)
	for _, key := range []term.Key{0, term.MouseLeft, term.MouseRelease} {
		h.Handle(term.Event{Type: term.EventMouse, Key: key, MouseX: 11, MouseY: 1})
	}
	assert.Empty(t, played, "\"\" plays none")

	h.Player = nil
	h.Sounds = TabSounds{Hover: "hover", Click: "click"}
	assert.NotPanics(t, func() {
		for _, key := range []term.Key{0, term.MouseLeft, term.MouseRelease} {
			h.Handle(term.Event{Type: term.EventMouse, Key: key, MouseX: 2, MouseY: 1})
		}
	}, "a nil Player plays none")
}

// newPointerTabs returns Tabs with more tabs than fit any test width,
// every one of them with an icon that takes clicks.
func newPointerTabs(clicks, iconClicks *int) *Tabs {
	tabs := NewTabs()
	for i := range 12 {
		tabs.Add('A'+rune(i), fmt.Sprintf("tab number %d", i))
	}
	tabs.SetFocus(3)
	tabs.OnClick = func(int) bool { *clicks++; return true }
	tabs.OnIconClick = func(int) { *iconClicks++ }
	return tabs
}

func TestTabsPointerLeaves(t *testing.T) {
	for _, size := range [][2]int{{60, 15}, {60, 1}, {1, 1}} {
		var clicks, iconClicks int
		tabs := newPointerTabs(&clicks, &iconClicks)
		rhandlertest.RunPointerLeaves(t, tabs, size[0], size[1])

		clicks, iconClicks = 0, 0
		for _, x := range []int{-1, size[0], 1000} {
			tabs.Handle(term.Event{Type: term.EventMouse, MouseX: x})
		}
		assert.Zero(t, clicks, "a leave is not a click")
		assert.Zero(t, iconClicks, "a leave is not an icon click")
	}
}

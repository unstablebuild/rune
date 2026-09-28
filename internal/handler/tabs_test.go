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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/handler/handlertest"
	"github.com/unstablebuild/rune-go-sdk/term"
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
		var called int
		h.OnClick = func(tabIdx int) bool {
			called++
			assert.Equal(t, 1, tabIdx)
			return true
		}

		assert.Equal(t, 0, h.Add(0, "Burning"))
		assert.Equal(t, 1, h.Add(0, "Man"))
		assert.Equal(t, 2, h.Add('x', "2024"))

		_, handled := h.Handle(term.Event{
			Type:   term.EventMouse,
			Key:    term.MouseLeft,
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

// TestTabsOnIconClick drives mouse gestures through Handle, redrawing
// after each one like the event loop does, and checks that only a press
// and release that stay on one icon click it, and that the tab under the
// icon never takes focus.
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

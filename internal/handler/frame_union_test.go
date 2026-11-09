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
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	rhandlertest "unstable.build/rune/internal/handler/handlertest"
)

// unionMember records every event delivered to it as a dispatch.
type unionMember struct {
	*handler.TestHandler
	name string
	got  *[]dispatch
}

func (m *unionMember) Handle(ev term.Event) (bool, bool) {
	*m.got = append(*m.got, to(m.name, ev.Key, ev.MouseX, ev.MouseY))
	return false, true
}

func TestFrameUnionCaptureDrags(t *testing.T) {
	wheel := func(x, y int) mouseEvent {
		return mouseEvent{key: term.MouseWheelDown, x: x, y: y}
	}
	for _, tc := range []struct {
		name   string
		events []mouseEvent
		want   []dispatch
	}{
		{
			name:   "click on the capturing member",
			events: []mouseEvent{press(3, 1), release(3, 1)},
			want: []dispatch{
				to("T", term.MouseLeft, 3, 1),
				to("T", term.MouseRelease, 3, 1),
			},
		},
		{
			name: "drag leaving the capturing member keeps unclamped coordinates",
			events: []mouseEvent{
				press(3, 1), drag(3, 4), drag(12, 9), release(2, 7),
			},
			want: []dispatch{
				to("T", term.MouseLeft, 3, 1),
				to("T", term.MouseLeft, 3, 4),
				to("T", term.MouseLeft, 12, 9),
				to("T", term.MouseRelease, 2, 7),
			},
		},
		{
			name: "drag from main never reaches the capturing member",
			events: []mouseEvent{
				press(3, 4), drag(3, 1), drag(4, 0), release(4, 0), press(4, 0),
			},
			want: []dispatch{
				to("M", term.MouseLeft, 3, 2),
				dropped(),
				dropped(),
				dropped(),
				to("M", 0, 4, -2),
				to("T", term.MouseLeft, 4, 0),
			},
		},
		{
			name:   "drag from a member that does not capture routes by position",
			events: []mouseEvent{press(3, 4), drag(3, 7), release(3, 7)},
			want: []dispatch{
				to("M", term.MouseLeft, 3, 2),
				to("B", term.MouseLeft, 3, 0),
				to("B", term.MouseRelease, 3, 0),
			},
		},
		{
			name:   "release ends the gesture",
			events: []mouseEvent{press(3, 1), release(3, 1), press(3, 4)},
			want: []dispatch{
				to("T", term.MouseLeft, 3, 1),
				to("T", term.MouseRelease, 3, 1),
				to("T", 0, 3, 4),
				to("M", term.MouseLeft, 3, 2),
			},
		},
		{
			// The release happened where this union could not see it.
			name:   "hover ends a gesture whose release was missed",
			events: []mouseEvent{press(3, 1), hover(3, 4), press(3, 4)},
			want: []dispatch{
				to("T", term.MouseLeft, 3, 1),
				to("T", 0, 3, 4),
				to("M", 0, 3, 2),
				to("M", term.MouseLeft, 3, 2),
			},
		},
		{
			name:   "wheel while held routes by position",
			events: []mouseEvent{press(3, 1), wheel(3, 4), release(3, 4)},
			want: []dispatch{
				to("T", term.MouseLeft, 3, 1),
				to("M", term.MouseWheelDown, 3, 2),
				to("T", term.MouseRelease, 3, 4),
			},
		},
		{
			name:   "press outside every member",
			events: []mouseEvent{press(20, 20), drag(3, 1), release(3, 1)},
			want:   []dispatch{dropped(), dropped(), dropped()},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []dispatch
			member := func(name string) *unionMember {
				return &unionMember{
					TestHandler: handler.NewTestHandler(), name: name, got: &got,
				}
			}
			top := member("T")
			u := handler.NewFrameUnion(member("M"))
			u.UnionTopFrame(top, 2, false)
			u.UnionBottomFrame(member("B"), 1, false)
			u.CaptureDrags(top)
			u.Resize(10, 8)

			assert.Equal(t, tc.want, handleUnionEvents(u, &got, tc.events))
		})
	}
}

func TestFrameUnionPointerLeaves(t *testing.T) {
	newUnion := func(got *[]dispatch) *handler.FrameUnion {
		member := func(name string) *unionMember {
			return &unionMember{TestHandler: handler.NewTestHandler(), name: name, got: got}
		}
		top := member("T")
		u := handler.NewFrameUnion(member("M"))
		u.UnionTopFrame(top, 2, false)
		u.UnionBottomFrame(member("B"), 1, false)
		u.CaptureDrags(top)
		u.Resize(10, 8)
		return u
	}
	for _, tc := range []struct {
		name   string
		events []mouseEvent
		want   []dispatch
	}{
		{
			name:   "onto another member",
			events: []mouseEvent{hover(3, 1), hover(3, 4)},
			want: []dispatch{
				to("T", 0, 3, 1),
				to("T", 0, 3, 4),
				to("M", 0, 3, 2),
			},
		},
		{
			name:   "within a member",
			events: []mouseEvent{hover(3, 4), hover(5, 5)},
			want:   []dispatch{to("M", 0, 3, 2), to("M", 0, 5, 3)},
		},
		{
			name:   "off every member",
			events: []mouseEvent{hover(3, 4), hover(12, 4), hover(13, 4)},
			want:   []dispatch{to("M", 0, 3, 2), to("M", 0, 12, 2), dropped()},
		},
		{
			name:   "above the union",
			events: []mouseEvent{hover(3, 1), hover(3, -1)},
			want:   []dispatch{to("T", 0, 3, 1), to("T", 0, 3, -1)},
		},
		{
			name:   "a parent's leave far off",
			events: []mouseEvent{hover(3, 7), hover(-5, -5)},
			want:   []dispatch{to("B", 0, 3, 0), to("B", 0, -5, -12)},
		},
		{
			name:   "a press on another member",
			events: []mouseEvent{hover(3, 4), press(3, 7), release(3, 7)},
			want: []dispatch{
				to("M", 0, 3, 2),
				to("M", 0, 3, 5),
				to("B", term.MouseLeft, 3, 0),
				to("B", term.MouseRelease, 3, 0),
			},
		},
		{
			name: "not while a drag is held",
			events: []mouseEvent{
				press(3, 4), drag(3, 7), release(3, 7), hover(3, 7),
			},
			want: []dispatch{
				to("M", term.MouseLeft, 3, 2),
				to("B", term.MouseLeft, 3, 0),
				to("B", term.MouseRelease, 3, 0),
				to("M", 0, 3, 5),
				to("B", 0, 3, 0),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []dispatch
			assert.Equal(t, tc.want, handleUnionEvents(newUnion(&got), &got, tc.events))
		})
	}

	t.Run("from a member the union moved since", func(t *testing.T) {
		var got []dispatch
		u := newUnion(&got)
		handleUnionEvents(u, &got, []mouseEvent{hover(3, 7)})
		u.Resize(10, 12)
		got = nil
		assert.Equal(t,
			[]dispatch{to("B", 0, 3, -4), to("M", 0, 3, 5)},
			handleUnionEvents(u, &got, []mouseEvent{hover(3, 7)}),
			"the leave lands where the member is now, outside it")
	})
}

// handleUnionEvents sends events to u and returns what its members got,
// dropped() for an event that reached none of them.
func handleUnionEvents(u *handler.FrameUnion, got *[]dispatch, events []mouseEvent) []dispatch {
	var dispatched []dispatch
	for _, ev := range events {
		before := len(*got)
		u.Handle(term.Event{
			Type: term.EventMouse, Key: ev.key, MouseX: ev.x, MouseY: ev.y,
		})
		if len(*got) == before {
			dispatched = append(dispatched, dropped())
		}
		dispatched = append(dispatched, (*got)[before:]...)
	}
	return dispatched
}

func TestFrameUnionKeysGoToMain(t *testing.T) {
	var got []dispatch
	top := &unionMember{TestHandler: handler.NewTestHandler(), name: "T", got: &got}
	u := handler.NewFrameUnion(&unionMember{
		TestHandler: handler.NewTestHandler(), name: "M", got: &got,
	})
	u.UnionTopFrame(top, 2, false)
	u.CaptureDrags(top)
	u.Resize(10, 8)

	u.Handle(term.Event{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 3, MouseY: 1})
	u.Handle(term.Event{Type: term.EventKey, Ch: 'a'})
	assert.Equal(t, []dispatch{
		to("T", term.MouseLeft, 3, 1),
		to("M", 0, 0, 0),
	}, got)
}

func TestFrameUnionPointerLeavesBattery(t *testing.T) {
	for _, size := range [][2]int{{60, 15}, {1, 1}} {
		var clicks, iconClicks int
		tabs := newPointerTabs(&clicks, &iconClicks)
		u := handler.NewFrameUnion(newPointerLess(t))
		u.UnionTopFrame(tabs, 1, false)
		u.UnionBottomFrame(newPointerLess(t), 2, false)
		u.CaptureDrags(tabs)
		rhandlertest.RunPointerLeaves(t, u, size[0], size[1])
	}
}

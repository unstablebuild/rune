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

package dialoguetui

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/handler/handlertest"
)

func newTestInput(t *testing.T, attr term.Attributes) (*Component, *textHandlerInput) {
	t.Helper()
	comp := NewComponent(ComponentConfig{InlineAttachmentAttr: attr})
	comp.Resize(40, 10)
	in, ok := comp.Input().(*textHandlerInput)
	require.True(t, ok)
	return comp, in
}

// edit mutates the compose buffer the way an editor keybinding would,
// so range tracking is exercised through the same publisher the real
// editor writes through.
func edit(in *textHandlerInput, startX, endX int, s string) {
	in.Handler.CellEditor().Edit(context.Background(),
		term.Coordinates{X: startX}, term.Coordinates{X: endX}, s)
	in.refreshLinks()
}

func inlineLocations(t *testing.T, in *textHandlerInput) []textapi.Location {
	t.Helper()
	for _, set := range in.Handler.LocationLists() {
		if set.ID == inlineAttachmentLocationID {
			return set.Locations
		}
	}
	return nil
}

func TestInputTextOffsetConversion(t *testing.T) {
	_, in := newTestInput(t, term.Attributes{})
	in.SetText("café go\nsecond line")

	for _, tc := range []struct {
		name string
		pos  term.Coordinates
		off  int
	}{
		{"start", term.Coordinates{}, 0},
		{"before wide rune", term.Coordinates{X: 3}, 3},
		{"after wide rune", term.Coordinates{X: 4}, 5},
		{"end of first row", term.Coordinates{X: 7}, 8},
		{"start of second row", term.Coordinates{Y: 1}, 9},
		{"inside second row", term.Coordinates{Y: 1, X: 6}, 15},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view := in.buf.View()
			assert.Equal(t, tc.off, textOffset(view, tc.pos))
			assert.Equal(t, tc.pos, textCoordinates(view, tc.off))
		})
	}
}

func TestInputReplaceBeforeCursorMidSentence(t *testing.T) {
	_, in := newTestInput(t, term.Attributes{})
	in.SetText("check #al later")
	require.True(t, in.SetCursorAtScroll(term.Coordinates{X: 9}))

	in.ReplaceBeforeCursor(3, "alpha.go", 4)

	assert.Equal(t, "check alpha.go later", in.Text())
	assert.Equal(t, []InlineAttachmentLink{{Key: 4, Start: 6, End: 14}}, in.Links())
}

func TestInputReplaceBeforeCursorUnicodeLabel(t *testing.T) {
	_, in := newTestInput(t, term.Attributes{})
	in.SetText("check #ca later")
	require.True(t, in.SetCursorAtScroll(term.Coordinates{X: 9}))

	in.ReplaceBeforeCursor(3, "café.go", 1)

	assert.Equal(t, "check café.go later", in.Text())
	links := in.Links()
	require.Len(t, links, 1)
	assert.Equal(t, "café.go", in.Text()[links[0].Start:links[0].End])
}

func TestInputReplaceBeforeCursorWithoutLabelDropsToken(t *testing.T) {
	_, in := newTestInput(t, term.Attributes{})
	in.SetText("check #al later")
	require.True(t, in.SetCursorAtScroll(term.Coordinates{X: 9}))

	in.ReplaceBeforeCursor(3, "", 4)

	assert.Equal(t, "check  later", in.Text())
	assert.Empty(t, in.Links())
}

// linkedInput composes "check alpha.go later" with "alpha.go" linked to
// key 4, the canonical shape for the edit-boundary tests below.
func linkedInput(t *testing.T, attr term.Attributes) *textHandlerInput {
	t.Helper()
	_, in := newTestInput(t, attr)
	in.SetDraft("check alpha.go later",
		[]InlineAttachmentLink{{Key: 4, Start: 6, End: 14}})
	return in
}

func TestInputLinkEditBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name         string
		startX, endX int
		text         string
		wantText     string
		wantLinks    []InlineAttachmentLink
	}{{
		name: "insert strictly before shifts", startX: 0, endX: 0, text: "hey ",
		wantText:  "hey check alpha.go later",
		wantLinks: []InlineAttachmentLink{{Key: 4, Start: 10, End: 18}},
	}, {
		name: "insert at start shifts", startX: 6, endX: 6, text: "X",
		wantText:  "check Xalpha.go later",
		wantLinks: []InlineAttachmentLink{{Key: 4, Start: 7, End: 15}},
	}, {
		name: "insert at end does not expand", startX: 14, endX: 14, text: "X",
		wantText:  "check alpha.goX later",
		wantLinks: []InlineAttachmentLink{{Key: 4, Start: 6, End: 14}},
	}, {
		name: "insert strictly after leaves range", startX: 16, endX: 16, text: "X",
		wantText:  "check alpha.go lXater",
		wantLinks: []InlineAttachmentLink{{Key: 4, Start: 6, End: 14}},
	}, {
		name: "delete strictly before shifts", startX: 0, endX: 2, text: "",
		wantText:  "eck alpha.go later",
		wantLinks: []InlineAttachmentLink{{Key: 4, Start: 4, End: 12}},
	}, {
		name: "insert inside invalidates", startX: 8, endX: 8, text: "X",
		wantText:  "check alXpha.go later",
		wantLinks: nil,
	}, {
		name: "delete across start invalidates", startX: 4, endX: 8, text: "",
		wantText:  "checpha.go later",
		wantLinks: nil,
	}, {
		name: "delete across end invalidates", startX: 12, endX: 16, text: "",
		wantText:  "check alpha.ater",
		wantLinks: nil,
	}, {
		name: "delete inside invalidates", startX: 7, endX: 9, text: "",
		wantText:  "check aha.go later",
		wantLinks: nil,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			in := linkedInput(t, term.Attributes{Fg: term.ColorAqua})
			edit(in, tc.startX, tc.endX, tc.text)

			assert.Equal(t, tc.wantText, in.Text())
			assert.Equal(t, tc.wantLinks, in.Links())
			if len(tc.wantLinks) == 0 {
				assert.Empty(t, inlineLocations(t, in),
					"an invalidated link must stop being styled")
			}
		})
	}
}

func TestInputLinkStylingFollowsRange(t *testing.T) {
	attr := term.Attributes{Fg: term.ColorAqua}
	in := linkedInput(t, attr)

	assert.Equal(t, []textapi.Location{{
		From: term.Coordinates{X: 6},
		To:   term.Coordinates{X: 14},
		Attr: attr,
	}}, inlineLocations(t, in))

	edit(in, 0, 0, "hey ")

	assert.Equal(t, []textapi.Location{{
		From: term.Coordinates{X: 10},
		To:   term.Coordinates{X: 18},
		Attr: attr,
	}}, inlineLocations(t, in), "styling must follow the shifted range")
}

func TestInputLinkStylingRefreshedThroughHandle(t *testing.T) {
	attr := term.Attributes{Fg: term.ColorAqua}
	in := linkedInput(t, attr)
	require.True(t, in.SetCursorAtScroll(term.Coordinates{}))

	in.Handle(term.Event{Type: term.EventKey, Ch: 'X'})

	assert.Equal(t, "Xcheck alpha.go later", in.Text())
	assert.Equal(t, []textapi.Location{{
		From: term.Coordinates{X: 7},
		To:   term.Coordinates{X: 15},
		Attr: attr,
	}}, inlineLocations(t, in))
}

func TestInputUndoIsObservedButKeepsLinksInvalid(t *testing.T) {
	in := linkedInput(t, term.Attributes{})

	edit(in, 0, 0, "hey ")
	require.Equal(t, []InlineAttachmentLink{{Key: 4, Start: 10, End: 18}}, in.Links())

	ok, _ := in.buf.Undo()
	require.True(t, ok)
	in.refreshLinks()

	assert.Equal(t, "check alpha.go later", in.Text())
	assert.Equal(t, []InlineAttachmentLink{{Key: 4, Start: 6, End: 14}}, in.Links(),
		"undo of an edit before the range must restore its offsets")

	edit(in, 8, 8, "X")
	require.Empty(t, in.Links())

	ok, _ = in.buf.Undo()
	require.True(t, ok)
	in.refreshLinks()

	assert.Equal(t, "check alpha.go later", in.Text())
	assert.Empty(t, in.Links(),
		"an overlapping edit unlinks permanently, even when undone")
}

func TestInputUnlinkKeepsText(t *testing.T) {
	in := linkedInput(t, term.Attributes{Fg: term.ColorAqua})

	in.Unlink(4)

	assert.Equal(t, "check alpha.go later", in.Text())
	assert.Empty(t, in.Links())
	assert.Empty(t, inlineLocations(t, in))
}

func TestInputMultipleLinksToSameAttachment(t *testing.T) {
	_, in := newTestInput(t, term.Attributes{})
	in.SetDraft("alpha.go and alpha.go", []InlineAttachmentLink{
		{Key: 2, Start: 0, End: 8},
		{Key: 2, Start: 13, End: 21},
	})

	edit(in, 15, 15, "X")

	assert.Equal(t, []InlineAttachmentLink{{Key: 2, Start: 0, End: 8}}, in.Links(),
		"editing one occurrence must not unlink the other")

	in.Unlink(2)
	assert.Empty(t, in.Links())
}

func TestInputSetDraftDropsInvalidLinks(t *testing.T) {
	_, in := newTestInput(t, term.Attributes{})

	in.SetDraft("short", []InlineAttachmentLink{
		{Key: 1, Start: 0, End: 5},
		{Key: 2, Start: 4, End: 40},
	})

	assert.Equal(t, "short", in.Text())
	assert.Equal(t, []InlineAttachmentLink{{Key: 1, Start: 0, End: 5}}, in.Links())
}

func TestInputClearDropsLinks(t *testing.T) {
	in := linkedInput(t, term.Attributes{Fg: term.ColorAqua})

	in.Clear()

	assert.Equal(t, "", in.Text())
	assert.Empty(t, in.Links())
	assert.Empty(t, inlineLocations(t, in))
}

const pointerLeaveDraft = "Please review #alpha.go and\nthe failing test in beta_test.go,\n" +
	"then explain why the index is off by one when the slice is empty\nThanks"

// newPointerLeaveInput returns a compose input holding a multi-line draft
// with an inline attachment link and long wrapped lines.
func newPointerLeaveInput(t *testing.T) *textHandlerInput {
	t.Helper()
	_, in := newTestInput(t, term.Attributes{Fg: term.ColorAqua})
	in.SetText(pointerLeaveDraft)
	require.True(t, in.SetCursorAtScroll(term.Coordinates{X: 23}))
	in.ReplaceBeforeCursor(len("#alpha.go"), "alpha.go", 7)
	require.Len(t, in.Links(), 1)
	return in
}

func TestTextHandlerInputPointerLeaves(t *testing.T) {
	t.Parallel()
	for _, size := range []struct{ width, height int }{{60, 15}, {80, 30}, {1, 1}} {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			t.Parallel()
			handlertest.RunPointerLeaves(t, newPointerLeaveInput(t), size.width, size.height)
		})
	}
}

func TestTextHandlerInputPointerLeaveKeepsState(t *testing.T) {
	t.Parallel()
	const width, height = 30, 4
	gestures := []struct {
		name   string
		events func(at term.Coordinates) []term.Event
	}{
		{name: "hover", events: func(at term.Coordinates) []term.Event {
			return []term.Event{mouseEv(at.X, at.Y, 0)}
		}},
		{name: "click", events: func(at term.Coordinates) []term.Event {
			return []term.Event{mouseEv(at.X, at.Y, term.MouseLeft), mouseEv(at.X, at.Y, term.MouseRelease)}
		}},
		{name: "drag", events: func(at term.Coordinates) []term.Event {
			return clickDrag(at.X, at.Y, width-1, height-1)
		}},
		{name: "press", events: func(at term.Coordinates) []term.Event {
			return []term.Event{mouseEv(at.X, at.Y, term.MouseLeft)}
		}},
		{name: "wheel", events: func(at term.Coordinates) []term.Event {
			return []term.Event{mouseEv(at.X, at.Y, term.MouseWheelDown)}
		}},
	}
	type state struct {
		exit      bool
		text      string
		links     []InlineAttachmentLink
		selection string
		selected  bool
		cursor    term.Coordinates
		visible   bool
		buffer    term.Coordinates
		offset    int
	}
	snapshot := func(in *textHandlerInput, exit bool) state {
		sel, selected := in.Selection()
		cursor, _, visible := in.Cursor()
		return state{
			exit: exit, text: in.Text(), links: in.Links(),
			selection: sel, selected: selected, cursor: cursor, visible: visible,
			buffer: in.CursorAtScroll(), offset: in.SeekOffset(),
		}
	}
	for _, gesture := range gestures {
		for _, at := range []term.Coordinates{{}, {X: 5, Y: 1}, {X: width - 1, Y: height - 1}} {
			for _, leave := range []term.Coordinates{
				{X: -1, Y: 1}, {X: width, Y: 1}, {X: 5, Y: -1}, {X: 5, Y: height},
				{X: -300, Y: -300}, {X: 300, Y: 300},
			} {
				t.Run(fmt.Sprintf("%s at %v then %v", gesture.name, at, leave), func(t *testing.T) {
					t.Parallel()
					in := newPointerLeaveInput(t)
					in.Resize(width, height)
					var exit bool
					for _, ev := range gesture.events(at) {
						exit, _ = in.Handle(ev)
						in.Draw(term.NewStringWriter(width, height))
					}
					before := snapshot(in, exit)

					exit, _ = in.Handle(mouseEv(leave.X, leave.Y, 0))
					in.Draw(term.NewStringWriter(width, height))

					assert.Equal(t, before, snapshot(in, exit))
				})
			}
		}
	}
}

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

package texttest

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component/comptest"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component"
	"unstable.build/rune/internal/handler/handlertest"
	"unstable.build/rune/internal/ide/vctrl"
	"unstable.build/rune/internal/text"
)

func TestIconsBarSetLocationListDrivesWidth(t *testing.T) {
	buf := cell.NewBuffer()
	buf.WriteString("a\nb\nc")
	scroll := component.NewScroll(buf)
	h := &interceptOnlyLocationListsHandler{testHandler: newTestHandler(scroll)}

	bar := text.WithIconsBar(vctrl.NopService(), false, h, buf, scroll, text.IconsBarConfig{
		ScheduleNextTick: func(fn func()) bool {
			fn()
			return true
		},
	})
	bar.Resize(7, 3)
	w := term.NewStringWriter(7, 3)

	comptest.TestComponent(t, bar, w, []comptest.TestCase{{Expected: `
  a    
  b    
  c    `}})

	bar.SetLocationList(textapi.LocationPriorityInfo, "marks", text.LocationSlice([]textapi.Location{{
		From: term.Coordinates{Y: 1},
		To:   term.Coordinates{Y: 2},
		Icon: "!!",
	}}))
	comptest.TestComponent(t, bar, w, []comptest.TestCase{{Expected: `
   a   
!! b   
   c   `}})

	bar.SetLocationList(textapi.LocationPriorityWarning, "breakpoints", text.LocationSlice([]textapi.Location{{
		From: term.Coordinates{Y: 1},
		To:   term.Coordinates{Y: 2},
		Icon: "+",
	}}))
	comptest.TestComponent(t, bar, w, []comptest.TestCase{{Expected: `
   a   
 + b   
   c   `}})

	bar.SetLocationList(textapi.LocationPriorityInfo, "marks", nil)
	comptest.TestComponent(t, bar, w, []comptest.TestCase{{Expected: `
  a    
+ b    
  c    `}})

	bar.SetLocationList(textapi.LocationPriorityWarning, "breakpoints", nil)
	comptest.TestComponent(t, bar, w, []comptest.TestCase{{Expected: `
  a    
  b    
  c    `}})
}

func TestIconsBarUsesIconWidthAndForegroundAttr(t *testing.T) {
	buf := cell.NewBuffer()
	buf.WriteString("a\nb")
	scroll := component.NewScroll(buf)
	h := &interceptOnlyLocationListsHandler{testHandler: newTestHandler(scroll)}

	bar := text.WithIconsBar(vctrl.NopService(), false, h, buf, scroll, text.IconsBarConfig{
		ScheduleNextTick: func(fn func()) bool {
			fn()
			return true
		},
	})
	bar.Resize(7, 2)
	bar.SetLocationList(textapi.LocationPriorityInfo, "diagnostics", text.LocationSlice([]textapi.Location{{
		From: term.Coordinates{Y: 1},
		To:   term.Coordinates{Y: 2},
		Icon: "",
		Attr: term.Attributes{Bg: term.ColorRed},
	}}))

	w := term.NewStringWriter(7, 2)
	comptest.TestComponent(t, bar, w, []comptest.TestCase{{Expected: `
   a   
  b   `}})

	w = term.NewStringWriter(7, 2)
	w.ForegroundCh = 'F'
	w.BackgroundCh = 'B'
	bar.Draw(w)
	require.NoError(t, w.Flush())
	assert.Equal(t, "   a   \nF  b   ", w.String())
	iconCell := w.Cells()[7]
	assert.Equal(t, term.ColorRed, iconCell.Fg)
	assert.Equal(t, term.ColorDefault, iconCell.Bg)
}

func TestIconsBarDrawsAllIconsWhenWrappedHandlerConsumesList(t *testing.T) {
	buf := cell.NewBuffer()
	buf.WriteString("a\nb\nc")
	scroll := component.NewScroll(buf)
	h := &consumeLocationListsHandler{testHandler: newTestHandler(scroll)}

	bar := text.WithIconsBar(vctrl.NopService(), false, h, buf, scroll, text.IconsBarConfig{
		ScheduleNextTick: func(fn func()) bool {
			fn()
			return true
		},
	})
	bar.Resize(7, 3)
	bar.SetLocationList(textapi.LocationPriorityInfo, "diagnostics", text.LocationSlice([]textapi.Location{
		{From: term.Coordinates{Y: 0}, To: term.Coordinates{Y: 1}, Icon: "1"},
		{From: term.Coordinates{Y: 1}, To: term.Coordinates{Y: 2}, Icon: "2"},
		{From: term.Coordinates{Y: 2}, To: term.Coordinates{Y: 3}, Icon: "3"},
	}))

	w := term.NewStringWriter(7, 3)
	comptest.TestComponent(t, bar, w, []comptest.TestCase{{Expected: `
1 a    
2 b    
3 c    `}})
	assert.Equal(t, 3, h.consumed)
}

func TestIconsBarReusesMaterializedLocationBufferWithoutAliasingForwardedList(t *testing.T) {
	buf := cell.NewBuffer()
	buf.WriteString("a\nb\nc")
	scroll := component.NewScroll(buf)
	h := &recordingLocationListsHandler{testHandler: newTestHandler(scroll)}

	bar := text.WithIconsBar(vctrl.NopService(), false, h, buf, scroll, text.IconsBarConfig{
		ScheduleNextTick: func(fn func()) bool {
			fn()
			return true
		},
	})

	bar.SetLocationList(textapi.LocationPriorityInfo, "syntax", text.LocationSlice([]textapi.Location{{
		From: term.Coordinates{Y: 0}, To: term.Coordinates{Y: 1}, Icon: "1",
	}}))
	bar.SetLocationList(textapi.LocationPriorityInfo, "syntax", text.LocationSlice([]textapi.Location{{
		From: term.Coordinates{Y: 1}, To: term.Coordinates{Y: 2}, Icon: "2",
	}}))

	require.Len(t, h.replaced, 1)
	assert.Equal(t, []textapi.Location{{
		From: term.Coordinates{Y: 0}, To: term.Coordinates{Y: 1}, Icon: "1",
	}}, h.replaced[0])
	assert.Equal(t, []textapi.Location{{
		From: term.Coordinates{Y: 1}, To: term.Coordinates{Y: 2}, Icon: "2",
	}}, materializeLocations(h.current))
}

func TestIconsBarRendersOneIconForMultilineLocation(t *testing.T) {
	buf := cell.NewBuffer()
	buf.WriteString("a\nb\nc\nd")
	scroll := component.NewScroll(buf)
	h := &interceptOnlyLocationListsHandler{testHandler: newTestHandler(scroll)}

	bar := text.WithIconsBar(vctrl.NopService(), false, h, buf, scroll, text.IconsBarConfig{
		ScheduleNextTick: func(fn func()) bool {
			fn()
			return true
		},
	})
	bar.Resize(7, 4)
	bar.SetLocationList(textapi.LocationPriorityInfo, "diagnostics", text.LocationSlice([]textapi.Location{{
		From: term.Coordinates{Y: 0},
		To:   term.Coordinates{Y: 3},
		Icon: "!",
	}}))

	w := term.NewStringWriter(7, 4)
	comptest.TestComponent(t, bar, w, []comptest.TestCase{{Expected: `
! a    
  b    
  c    
  d    `}})
}

func TestIconsBarNormalizesReversedLocationCoordinates(t *testing.T) {
	buf := cell.NewBuffer()
	buf.WriteString("a\nb\nc\nd")
	scroll := component.NewScroll(buf)
	h := &interceptOnlyLocationListsHandler{testHandler: newTestHandler(scroll)}

	bar := text.WithIconsBar(vctrl.NopService(), false, h, buf, scroll, text.IconsBarConfig{
		ScheduleNextTick: func(fn func()) bool {
			fn()
			return true
		},
	})
	bar.Resize(7, 4)
	bar.SetLocationList(textapi.LocationPriorityInfo, "diagnostics", text.LocationSlice([]textapi.Location{{
		From: term.Coordinates{Y: 3},
		To:   term.Coordinates{Y: 1},
		Icon: "!",
	}}))

	w := term.NewStringWriter(7, 4)
	comptest.TestComponent(t, bar, w, []comptest.TestCase{{Expected: `
  a    
! b    
  c    
  d    `}})
}

func TestIconsBarPointerLeaves(t *testing.T) {
	t.Parallel()
	newBar := func() text.Handler {
		ed := newPointerEditor()
		bar := text.WithIconsBar(vctrl.NopService(), false, ed, ed.buf, ed.Scroll,
			text.IconsBarConfig{ScheduleNextTick: func(fn func()) bool { fn(); return true }})
		bar.SetLocationList(textapi.LocationPriorityInfo, "marks", text.LocationSlice(
			[]textapi.Location{
				{From: term.Coordinates{Y: 2}, To: term.Coordinates{Y: 2, X: 3}, Icon: "日"},
				{From: term.Coordinates{Y: 9}, To: term.Coordinates{Y: 11}, Icon: "🙂"},
			}))
		return bar
	}
	for _, size := range pointerSizes {
		handlertest.RunPointerLeaves(t, newBar(), size.width, size.height)
	}
	requirePointerLeavesKeepState(t, newBar(), 40, 10,
		term.Coordinates{X: 10, Y: 2}, term.Coordinates{X: 16, Y: 4}, nil)
}

func TestGitBarDraw(t *testing.T) {
	buf := cell.NewBuffer()
	buf.WriteString(copy)
	fs := &testFoldsService{}
	fs.view = buf.WithView(fs)
	scroll := component.NewScroll(buf)
	h := newTestHandler(scroll)
	var wg sync.WaitGroup
	var mu sync.Mutex
	cb := func(fn func()) bool {
		mu.Lock()
		defer mu.Unlock()
		fn()
		wg.Done()
		return true
	}
	uri, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)

	registry := text.NewFileCommandRegistry(uri, newWorkspaceRegistry())
	ed := &TestEditor{}
	mockSvc := &differ{}
	wg.Add(1)
	mu.Lock()
	cfg := text.IconsBarConfig{CommandRegistry: registry, ScheduleNextTick: cb, Publisher: ed}
	bar := text.WithGitBar(mockSvc, h, buf, scroll, cfg)
	bar.Resize(15, 10)
	mu.Unlock()
	w := term.NewStringWriter(15, 10)

	tests := []comptest.TestCase{
		{Expected: `
  package main 
               
  import (     
      "fmt"    
+              
      "github.c
  )            
               
  func main() {
-     fmt.Print`,
		},
	}
	wg.Wait()
	mu.Lock()
	comptest.TestComponent(t, bar, w, tests)
	require.True(t, scroll.SeekDown())
	mu.Unlock()

	tests = []comptest.TestCase{
		{Expected: `
               
  import (     
      "fmt"    
+              
      "github.c
  )            
               
  func main() {
-     fmt.Print
      for i := `,
		},
	}
	mu.Lock()
	comptest.TestComponent(t, bar, w, tests)
	mu.Unlock()

	require.NoError(t, bar.Close())

	// unsubscribes
	require.Equal(t, 2, len(ed.subs))
	assert.Equal(t, 0, len(ed.subs[textapi.EventTypeFlush]))
	assert.Equal(t, 0, len(ed.subs[textapi.EventTypeFocus]))
}

type interceptOnlyLocationListsHandler struct {
	*testHandler
}

func (h *interceptOnlyLocationListsHandler) SetLocationList(
	pri textapi.LocationPriority, ID string, loc text.LocationList,
) {
	h.testHandler.SetLocationList(pri, ID, loc)
}

func (*interceptOnlyLocationListsHandler) LocationLists() []text.LocationSet {
	panic("icons bar should not read back LocationLists")
}

type consumeLocationListsHandler struct {
	*testHandler
	consumed int
}

func (h *consumeLocationListsHandler) SetLocationList(
	pri textapi.LocationPriority, ID string, loc text.LocationList,
) {
	h.consumed = 0
	for curr, ok := loc.Current(); ok; curr, ok = loc.Next() {
		if curr.Icon != "" {
			h.consumed++
		}
	}
	h.testHandler.SetLocationList(pri, ID, loc)
}

func (*consumeLocationListsHandler) LocationLists() []text.LocationSet {
	panic("icons bar should not read back LocationLists")
}

type recordingLocationListsHandler struct {
	*testHandler
	current  text.LocationList
	replaced [][]textapi.Location
}

func (h *recordingLocationListsHandler) SetLocationList(
	_ textapi.LocationPriority, _ string, loc text.LocationList,
) {
	if h.current != nil {
		h.replaced = append(h.replaced, materializeLocations(h.current))
	}
	h.current = loc
}

func materializeLocations(loc text.LocationList) []textapi.Location {
	var locations []textapi.Location
	for curr, ok := loc.Current(); ok; curr, ok = loc.Next() {
		locations = append(locations, curr)
	}
	return locations
}

type noDiffDiffer struct{}

func (noDiffDiffer) ListRemotes(context.Context, workspaceapi.URI) ([]string, error) {
	return nil, nil
}

func (noDiffDiffer) ShortRef(context.Context, workspaceapi.URI) (string, error) {
	return "main", nil
}

func (noDiffDiffer) Diff(context.Context, workspaceapi.URI) (vctrl.FileDiff, error) {
	return vctrl.FileDiff{}, nil
}

func (noDiffDiffer) WorkingDiff(
	context.Context, workspaceapi.URI, int,
) ([]vctrl.FileDiff, error) {
	panic("unimplemented")
}

func (noDiffDiffer) CurrentCommit(context.Context, workspaceapi.URI) (string, error) {
	panic("unimplemented")
}

func (noDiffDiffer) RemoteURL(context.Context, workspaceapi.URI, string) (string, error) {
	panic("unimplemented")
}

func (noDiffDiffer) RelPath(context.Context, string) (string, error) {
	panic("unimplemented")
}

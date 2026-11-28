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

package idestatus

import (
	"cmp"
	"context"
	"fmt"
	"image"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/term/graphemecluster"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/browser"
	tcomponent "unstable.build/rune/internal/component"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/term/programstatus"
)

// InboxStyle styles the chrome of the status inbox. Colors left at the
// default keep those of what they are drawn over.
type InboxStyle struct {
	// SelectedAttr styles the selected item, and HoverAttr the one under
	// the mouse, all of it: their colors win over those of its text, and
	// SelectedAttr's over HoverAttr's on an item both style. Marker is
	// drawn down the left of the selected item in MarkerAttr, over them.
	Marker                              rune
	MarkerAttr, SelectedAttr, HoverAttr term.Attributes
	// WorkspaceAttr styles the name of the workspace above the name of
	// an item, and WorkspaceHighlightAttr that of the item selected or
	// under the mouse. Their colors win over the item's.
	WorkspaceAttr, WorkspaceHighlightAttr term.Attributes
	// ProgressBar draws progress bars, their ends included, over
	// ProgressAttr, which keeps its colors over the item's: in
	// ProgressDoneAttr as far as they are done and in
	// ProgressRemainingAttr past that, or in the selection in
	// ProgressRemainingSelectedAttr, which ProgressRemainingAttr would
	// blend into. ProgressLabelAttr styles the percentage on their right
	// as the item's text.
	ProgressBar                                           component.ProgressBarCharSet
	ProgressAttr, ProgressDoneAttr, ProgressRemainingAttr term.Attributes
	ProgressRemainingSelectedAttr, ProgressLabelAttr      term.Attributes
	// MoreBelow is drawn in MoreBelowAttr in the row below the items when
	// there are more out of sight below.
	MoreBelow     rune
	MoreBelowAttr term.Attributes
	// SidebarAttr fills the sidebar, which the preview beside it stands
	// apart from by keeping the window's background. Its colors stand in
	// for the default colors of whatever is drawn in the sidebar. A
	// default background takes the store's shade of the theme, as
	// Store.SetDefaultAttributes sets it, and none without a theme.
	SidebarAttr term.Attributes
	// SidebarMargin is the cells around the sidebar's fill that keep the
	// window's background: above it, on its left, below it, and between
	// it and the preview, which takes the view's full height. The Focus
	// style's is ignored, for the layout to stay put as the focus moves.
	// Where the sidebar is drawn half a cell lower, the fill reaches half
	// a cell into what is below the view, as into a frame's bottom row.
	SidebarMargin Margin
	// PlaceholderAttr styles the text that stands in for the programs
	// and the preview when there is nothing to show.
	PlaceholderAttr term.Attributes
	// Images are drawn under each item, from the marker's column to the
	// right edge of the selection: ButtonSelected's under the selected
	// item, ButtonHover's under the one under the mouse and ButtonIdle's
	// under the rest. They stand in for the backgrounds of SelectedAttr
	// and HoverAttr, which an item keeps while its images do not draw.
	Images browser.PromptImages
	// MarkerGraphics are drawn down the marker.
	MarkerGraphics tcomponent.LineGraphics
	// ProgressBarGraphics are drawn over the progress bars.
	ProgressBarGraphics ProgressBarGraphics
	// Focus styles an inbox while the writer's context tells it its
	// window holds the focus, as term.WindowFocusFromContext reads it.
	// Nil keeps this style, and its own Focus is ignored.
	Focus *InboxStyle
}

// Margin is a number of cells on each side of a rectangle.
type Margin struct {
	Top, Left, Bottom, Right int
}

// ProgressBarGraphics names the images drawn over the cells of a progress
// bar: Left and Right over its ends, Filled over the cells done and Empty
// over the others. An end left out takes the image of the cell it stands
// in. Overlay keeps the characters of the bar under the images rather
// than blanking them.
type ProgressBarGraphics struct {
	Left, Filled, Empty, Right string
	Overlay                    bool
}

// URIs returns the URIs g names, empty ones included.
func (g ProgressBarGraphics) URIs() []string {
	return []string{g.Left, g.Filled, g.Empty, g.Right}
}

// cell returns the image of cell i of a bar of barW cells, filled of
// them done, as a line of one cell.
func (g ProgressBarGraphics) cell(i, barW, filled int) tcomponent.LineGraphics {
	uri := g.Empty
	if i < filled {
		uri = g.Filled
	}
	switch i {
	case 0:
		uri = cmp.Or(g.Left, uri)
	case barW - 1:
		uri = cmp.Or(g.Right, uri)
	}
	return tcomponent.LineGraphics{Start: uri, Overlay: g.Overlay}
}

// DefaultInboxStyle returns the style of an unconfigured inbox.
func DefaultInboxStyle() InboxStyle {
	return InboxStyle{
		Marker:                 '▌',
		MarkerAttr:             browser.DefaultConfig().FocusTabHighlightAttr,
		SelectedAttr:           term.Attributes{Bg: term.ColorGray},
		HoverAttr:              term.Attributes{Attrs: term.AttrBold},
		WorkspaceAttr:          statusMutedAttr,
		WorkspaceHighlightAttr: term.Attributes{Fg: term.ColorSilver},
		ProgressBar: component.ProgressBarCharSet{
			Left: statusProgressGlyph, Filled: statusProgressGlyph,
			Empty: statusProgressGlyph, Right: statusProgressGlyph,
		},
		ProgressDoneAttr:              term.Attributes{Fg: term.ColorGreen},
		ProgressRemainingAttr:         statusMutedAttr,
		ProgressRemainingSelectedAttr: term.Attributes{Fg: term.ColorSilver},
		MoreBelow:                     statusMoreBelow,
		// The GUI draws the sidebar half a cell lower, under a window bar
		// and over half its frame's bottom row, and its frame's left
		// column is a cell wide. A cell is about twice as tall as wide, so
		// a column on the right makes the fill as far from every side.
		SidebarMargin:   Margin{Right: 1},
		PlaceholderAttr: statusMutedAttr,
	}
}

// Preview is what the inbox shows of a program.
//
// Open, if not nil, opens a view of the program for the inbox alone,
// width by height, and returns it with what closes it, or a nil view
// when it has none to show. The inbox resizes that view as the preview
// changes size, never sends it input, and closes it once it previews
// something else or closes. Width and Height, when not zero, are the
// size the view would rather have, such as that of a terminal another
// view sizes, which a smaller view crops.
//
// Without Open, the inbox borrows Content, shown elsewhere at Width by
// Height: it draws it at that size, cutting the top rows and right
// columns that do not fit, and never resizes it, as that would reflow
// it where it is shown. Content without a size shows no preview.
type Preview struct {
	Open          func(width, height int) (tui.Component, func())
	Content       tui.Component
	Width, Height int
}

func (p Preview) borrowed() bool {
	return p.Open == nil && p.Content != nil && p.Width > 0 && p.Height > 0
}

// View is the status inbox: a sidebar listing every program that
// reported a status, beside a live preview of the selected one, kept up
// to date until it is closed. The preview is a view of the program of
// its own when its host opens one, and otherwise what shows the program
// elsewhere, drawn as it looks there; it never gets input. Use
// Store.View to create one.
type View struct {
	store   *Store
	entries []statusEntry
	sel     statusRef
	// selIdx is where sel was last found, for the selection to stay in
	// place when its program goes away.
	selIdx int
	top    int
	// scrolled stops the sidebar from keeping the selection in sight
	// once the wheel scrolls it, until the selection moves.
	scrolled bool
	rows     []int
	hover    int
	// drawing stops a preview that draws the view, as what the view
	// floats over does, from recursing.
	drawing bool
	// ticker wakes the host while the view is open, for the spinners to
	// turn and the preview to follow a workspace out of focus.
	ticker *statusTicker
	// shown is the preview of the selection.
	shown         statusShown
	minWidth      func() int
	width, height int
	// focused is whether the window of the writer last drawn to holds
	// the focus, which selects the style drawn.
	focused bool
}

// Select selects uri in the workspace at ws, and reports whether the
// view lists it. It leaves the selection alone when it does not.
func (v *View) Select(ws, uri workspaceapi.URI) bool {
	ref := statusRef{ws: ws.String(), uri: uri.String()}
	i := slices.IndexFunc(v.entries, func(e statusEntry) bool { return e.ref() == ref })
	v.selectAt(i)
	return i >= 0
}

// Resize sizes the view, and the view of the selected program it
// previews, if it opened one, to the preview.
func (v *View) Resize(width, height int) {
	v.width, v.height = width, height
	v.sizePreview()
}

// Draw draws the view. Called again from within its own Draw, as by a
// preview of what the view floats over, it draws nothing.
func (v *View) Draw(w term.Writer) {
	if v.drawing {
		return
	}
	v.drawing = true
	defer func() { v.drawing = false }()
	v.focused, _ = term.WindowFocusFromContext(w.Context())
	m := v.margin()
	sideW, sideH := v.sidebarWidth(), v.sidebarHeight()
	side := &sidebarWriter{
		Writer: &component.VirtualWriter{
			Writer: w, Offset: term.Coordinates{X: m.Left, Y: m.Top}, Width: sideW, Height: sideH,
		},
		attr: v.sidebarAttr(),
	}
	blank := term.NewCell(' ', 1, term.Attributes{})
	for y := range sideH {
		for x := range sideW {
			side.SetCell(term.Coordinates{X: x, Y: y}, blank)
		}
	}
	v.drawSidebar(side, sideW)
	x := v.previewX()
	if x >= v.width {
		return
	}
	v.drawPreview(&component.VirtualWriter{
		Writer: w, Offset: term.Coordinates{X: x}, Width: v.width - x, Height: v.height,
	}, v.width-x, v.height)
}

// Handle moves the selection, wrapping around, with the arrows, j and
// k, or ctrl with j, k, n and p; enter brings the selected program in
// sight and exits; delete and backspace close it, asking first while it
// is working or blocked; esc, q and ctrl-c exit. A click selects the
// program under it, and the wheel scrolls the sidebar without moving
// the selection.
func (v *View) Handle(ev term.Event) (exit, handled bool) {
	if ev.Type == term.EventMouse {
		return false, v.handleMouse(ev)
	}
	if ev.Type != term.EventKey {
		return false, false
	}
	switch ev.Key {
	case term.KeyEsc:
		return true, true
	case term.KeyEnter:
		v.choose()
		return true, true
	case term.KeyArrowUp:
		v.move(-1)
		return false, true
	case term.KeyArrowDown:
		v.move(1)
		return false, true
	case term.KeyDelete, term.KeyBackspace:
		v.closeSelected()
		return false, true
	}
	if ev.Mod == term.ModCtrl {
		switch ev.Ch {
		case 'j', 'n':
			v.move(1)
			return false, true
		case 'k', 'p':
			v.move(-1)
			return false, true
		case 'c':
			return true, true
		}
	}
	switch ev.Ch {
	case 'j':
		v.move(1)
		return false, true
	case 'k':
		v.move(-1)
		return false, true
	case 'q':
		return true, true
	}
	return false, false
}

// Cursor reports no cursor.
func (v *View) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return term.Coordinates{}, term.CursorStyleDefault, false
}

// style returns the style the view draws with and the images of its
// buttons: those of the focus style while its window holds the focus.
func (v *View) style() (*InboxStyle, *component.ButtonImages) {
	if v.focused && v.store.style.Focus != nil {
		return v.store.style.Focus, v.store.focusImages
	}
	return &v.store.style, v.store.images
}

// sidebarAttr returns the colors that fill the sidebar.
func (v *View) sidebarAttr() term.Attributes {
	style, _ := v.style()
	ret := style.SidebarAttr
	if ret.Bg == term.ColorDefault {
		ret.Bg = v.store.sidebarShade
	}
	return ret
}

// margin returns the sidebar's margin, which the focus style leaves alone.
func (v *View) margin() Margin {
	return v.store.style.SidebarMargin
}

// Selection returns the name of the selected program, or false when
// nothing is listed.
func (v *View) Selection() (string, bool) {
	i := v.resolve()
	if i < 0 {
		return "", false
	}
	return v.entries[i].name, true
}

// Close stops updating the view and turning its spinners, and closes
// the view of the selected program it previews, if any. It returns nil.
func (v *View) Close() error {
	delete(v.store.views, v)
	v.setTicking(false)
	v.closePreview()
	return nil
}

// Dimensions returns the size the view asks for: the sidebar in its
// margin beside the preview at the size it asks for, at least as wide as
// minWidth asks and 40 rows high.
func (v *View) Dimensions() (int, int) {
	m := v.margin()
	sideW, sideH := v.sidebarSize()
	w, h := v.previewSize()
	width := m.Left + sideW + m.Right + w
	if v.minWidth != nil {
		width = max(width, v.minWidth())
	}
	return width, max(m.Top+sideH+m.Bottom, h, statusMinHeight)
}

const (
	statusSubSeparator  = " · "
	statusViewEmpty     = "No program reported a status"
	statusPreviewNone   = "No preview"
	statusPreviewWidth  = 80
	statusPreviewHeight = 21
	statusSidebarMin    = 24
	statusSidebarMax    = 40
	// statusMinHeight keeps the inbox from changing size as programs
	// come and go.
	statusMinHeight = 40
	statusPadTop    = 1
	statusPadLeft   = 1
	// statusPadRight is twice statusPadTop, as cells are about twice as
	// tall as wide, for the selection to look as far from the preview as
	// from the top.
	statusPadRight     = 2
	statusSelectionPad = 1
	statusIconX        = statusPadLeft + 2
	statusTextX        = statusIconX + 2
	statusGap          = 1
	// statusProgressGlyph, BLOCK SEXTANT-34 (🬋), fills the middle third
	// of a cell edge to edge: a run of them draws one thick bar centered
	// on the text, which colors alone split.
	statusProgressGlyph = '\U0001FB0B'
	// statusMoreBelow is nf-fa-arrow_down, as tutorials point at the rest
	// of a step with.
	statusMoreBelow = '\uf063'
)

var statusMutedAttr = term.Attributes{Fg: term.ColorGray}

func (v *View) set(entries []statusEntry) {
	v.entries = entries
	v.resolve()
	v.setTicking(len(entries) > 0)
	v.sizePreview()
}

func (v *View) resolve() int {
	if len(v.entries) == 0 {
		return -1
	}
	if i := slices.IndexFunc(v.entries, func(e statusEntry) bool { return e.ref() == v.sel }); i >= 0 {
		v.selIdx = i
		return i
	}
	v.selIdx = min(max(v.selIdx, 0), len(v.entries)-1)
	v.sel = v.entries[v.selIdx].ref()
	return v.selIdx
}

func (v *View) selectAt(i int) {
	if i < 0 || i >= len(v.entries) {
		return
	}
	v.sel, v.selIdx = v.entries[i].ref(), i
	v.scrolled = false
	v.sizePreview()
}

func (v *View) move(delta int) {
	n := len(v.entries)
	if n == 0 {
		return
	}
	v.selectAt(((v.resolve()+delta)%n + n) % n)
}

func (v *View) setTicking(ticking bool) {
	if ticking == (v.ticker != nil) {
		return
	}
	if !ticking {
		v.ticker.cancel()
		v.ticker = nil
		return
	}
	if v.store.interrupter == nil {
		return
	}
	fps := v.store.cfg.SpinnerFPS
	if fps <= 0 {
		fps = browser.DefaultTabStatusConfig().SpinnerFPS
	}
	v.ticker = newStatusTicker(v.store.interrupter, fps)
}

func (v *View) spinnerFrame() (string, bool) {
	frames := v.store.cfg.Spinner
	if v.ticker == nil || len(frames) == 0 {
		return "", false
	}
	return string(frames[int(v.ticker.epoch.Load()%int64(len(frames)))]), true
}

func (v *View) entryHeights() []int {
	ret := make([]int, len(v.entries))
	for i, e := range v.entries {
		ret[i] = e.height()
	}
	return ret
}

func (v *View) sidebarSize() (int, int) {
	if len(v.entries) == 0 {
		w := statusIconX + graphemecluster.StringWidth(statusViewEmpty) + statusPadRight
		return min(max(w, statusSidebarMin), statusSidebarMax), statusPadTop + 1
	}
	w := 0
	for _, e := range v.entries {
		for _, s := range []string{e.wsName, e.name, e.sub} {
			w = max(w, statusTextX+graphemecluster.StringWidth(s))
		}
	}
	return min(max(w+statusSelectionPad+statusPadRight, statusSidebarMin), statusSidebarMax),
		statusPadTop + sectionHeight(v.entryHeights())
}

func (v *View) sidebarWidth() int {
	w, _ := v.sidebarSize()
	return max(min(w, v.width-v.margin().Left), 0)
}

// sidebarHeight returns the rows of the sidebar's fill.
func (v *View) sidebarHeight() int {
	m := v.margin()
	return max(v.height-m.Top-m.Bottom, 0)
}

// previewX returns the column the preview starts at.
func (v *View) previewX() int {
	m := v.margin()
	return m.Left + v.sidebarWidth() + m.Right
}

// preview returns the preview of the selection, or nil when nothing is
// listed. What the view opens is kept while the selection stays; what it
// borrows is asked for again, as it follows what shows it.
func (v *View) preview() *statusShown {
	i := v.resolve()
	if i < 0 {
		v.closePreview()
		return nil
	}
	e := v.entries[i]
	if ref := e.ref(); v.shown.ref == ref && v.shown.preview.Open != nil {
		return &v.shown
	}
	v.closePreview()
	p, ok := v.store.host.Preview(e.ws, e.uri)
	if !ok {
		p = Preview{}
	}
	v.shown = statusShown{ref: e.ref(), preview: p}
	return &v.shown
}

func (v *View) closePreview() {
	if v.shown.close != nil {
		v.shown.close()
	}
	v.shown = statusShown{}
}

func (v *View) previewSize() (int, int) {
	s := v.preview()
	switch {
	case s == nil || (s.preview.Open == nil && !s.preview.borrowed()):
		return graphemecluster.StringWidth(statusPreviewNone) + 4, 0
	case s.preview.Width <= 0 || s.preview.Height <= 0:
		return statusPreviewWidth, statusPreviewHeight
	}
	return s.preview.Width, s.preview.Height
}

// sizePreview opens the view of the selection the preview shows, or
// resizes it to the preview. It must run whenever the selection or the
// size changes.
func (v *View) sizePreview() {
	s := v.preview()
	if s == nil || s.preview.Open == nil {
		return
	}
	width := v.width - v.previewX()
	if width <= 0 || v.height <= 0 || (s.opened && s.width == width && s.height == v.height) {
		return
	}
	s.width, s.height = width, v.height
	if !s.opened {
		s.opened = true
		s.comp, s.close = s.preview.Open(width, v.height)
		return
	}
	if s.comp != nil {
		s.comp.Resize(width, v.height)
	}
}

func (v *View) drawSidebar(w term.Writer, width int) {
	v.rows = slices.Repeat([]int{-1}, v.sidebarHeight())
	style, images := v.style()
	if len(v.entries) == 0 {
		statusText(w, statusIconX, statusPadTop, statusViewEmpty, style.PlaceholderAttr, width-statusPadRight)
		return
	}
	sel := v.resolve()
	heights := v.entryHeights()
	rows, arrow := v.listRows(heights)
	follow := sel
	if v.scrolled {
		follow = -1
	}
	v.top = statusScroll(v.top, follow, heights, rows)
	selEnd := width - statusPadRight
	spans := v.layout(heights)
	// A program cut short stops above the arrow's row.
	list := &component.VirtualWriter{Writer: w, Width: width, Height: statusPadTop + rows}
	g := v.store.graphics
	for _, s := range spans {
		st := component.ButtonIdle
		var base term.Attributes
		if s.i == v.hover {
			base, st = style.HoverAttr, component.ButtonHover
		}
		if s.i == sel {
			base, st = term.AttributesUnion(base, style.SelectedAttr), component.ButtonSelected
		}
		// Images go under what is written before them, and the cells
		// written after them cover them: the item is written first, then
		// its images, then the images over its marker and progress bar.
		imaged := false
		if images != nil && !images.Image(st).Empty() {
			// Over its images, the item keeps the colors of its state but
			// its background, which would paint over them.
			over := base
			over.Bg = term.ColorDefault
			v.drawItem(list, s, sel, width, over)
			imaged = images.Draw(list, image.Rect(statusPadLeft, s.y, selEnd, s.y+s.n), st)
			if imaged {
				base = over
			}
		}
		if !imaged {
			v.drawItem(list, s, sel, width, base)
		}
		if g == nil {
			continue
		}
		if s.i == sel {
			g.DrawLine(list, style.MarkerGraphics, term.Coordinates{X: statusPadLeft, Y: s.y}, s.n,
				true, v.markerCell(base), term.ImageLayerAboveText)
		}
		if bar, ok := v.progressBar(s.i, s.y, width, base, s.i == sel); ok {
			bar.drawGraphics(list, g)
		}
	}
	if !arrow {
		return
	}
	if last := spans[len(spans)-1]; last.i < len(heights)-1 || last.n < heights[last.i] {
		statusText(w, statusPadLeft+(selEnd-statusPadLeft-1)/2, statusPadTop+rows,
			string(style.MoreBelow), style.MoreBelowAttr, selEnd)
	}
}

func (v *View) listRows(heights []int) (int, bool) {
	rows := v.sidebarHeight() - statusPadTop
	// The arrow's row is kept while the programs do not all fit, however
	// far the sidebar is scrolled, for them not to shift as the arrow
	// comes and goes.
	if rows > 1 && sectionHeight(heights) > rows {
		return rows - 1, true
	}
	return rows, false
}

func (v *View) layout(heights []int) []statusSpan {
	rows, _ := v.listRows(heights)
	spans := statusLayout(v.top, heights, statusPadTop+rows)
	v.rows = slices.Repeat([]int{-1}, v.sidebarHeight())
	for _, s := range spans {
		for dy := range s.n {
			v.rows[s.y+dy] = s.i
		}
	}
	return spans
}

// drawItem writes the cells of the item s lays out in base, the
// attributes of its state, sel being the selected item.
func (v *View) drawItem(w term.Writer, s statusSpan, sel, width int, base term.Attributes) {
	if s.i == v.hover || s.i == sel {
		blank := term.NewCell(' ', 1, base)
		for dy := range s.n {
			for x := statusPadLeft; x < width-statusPadRight; x++ {
				w.SetCell(term.Coordinates{X: x, Y: s.y + dy}, blank)
			}
		}
	}
	v.drawEntry(w, s.i, s.y, width, base, s.i == sel)
	if s.i == sel {
		style, _ := v.style()
		style.MarkerGraphics.DrawCells(w, term.Coordinates{X: statusPadLeft, Y: s.y}, s.n,
			true, v.markerCell(base))
	}
}

// markerCell returns the marker drawn over base.
func (v *View) markerCell(base term.Attributes) term.Cell {
	style, _ := v.style()
	return term.NewCell(style.Marker, uint8(max(graphemecluster.StringWidth(string(style.Marker)), 1)),
		term.AttributesUnion(base, style.MarkerAttr))
}

func (v *View) scroll(down bool) bool {
	heights := v.entryHeights()
	rows, _ := v.listRows(heights)
	top := min(v.top, max(len(heights)-1, 0))
	switch {
	case down && top < len(heights)-1 && sectionHeight(heights[top:]) > rows:
		top++
	case !down && top > 0:
		top--
	default:
		return false
	}
	v.top, v.scrolled = top, true
	v.layout(heights)
	return true
}

func (v *View) drawEntry(w term.Writer, i, y, width int, base term.Attributes, selected bool) {
	e := v.entries[i]
	style, _ := v.style()
	if bar, ok := v.progressBar(i, y, width, base, selected); ok {
		bar.draw(w)
	}
	// Short of room, text keeps clear of the padding, as it does when it
	// fits, and never splits a wide character there.
	textEnd := width - statusPadRight - statusSelectionPad
	if e.wsName != "" {
		ws := style.WorkspaceAttr
		if selected || i == v.hover {
			ws = style.WorkspaceHighlightAttr
		}
		statusText(w, statusTextX, y, e.wsName, term.AttributesUnion(base, ws), textEnd)
		y++
	}
	icon := e.icon
	if e.state == programstatus.Working {
		if frame, ok := v.spinnerFrame(); ok {
			icon = frame
		}
	}
	statusText(w, statusIconX, y, icon, term.AttributesUnion(e.iconAttr, base), statusTextX)
	statusText(w, statusTextX, y, e.name, term.AttributesUnion(e.nameAttr, base), textEnd)
	statusText(w, statusTextX, y+1, e.sub, term.AttributesUnion(e.subAttr, base), textEnd)
}

// progressBar returns the progress bar of item i, which is drawn from
// row y in base, or false when it has none.
func (v *View) progressBar(i, y, width int, base term.Attributes, selected bool) (statusProgressBar, bool) {
	e := v.entries[i]
	if e.progress < 0 {
		return statusProgressBar{}, false
	}
	// It is below the name of the workspace, the item's and its state.
	y += 2
	if e.wsName != "" {
		y++
	}
	style, _ := v.style()
	textEnd := width - statusPadRight - statusSelectionPad
	bar := term.AttributesUnion(base, style.ProgressAttr)
	remaining := style.ProgressRemainingAttr
	if selected {
		remaining = style.ProgressRemainingSelectedAttr
	}
	return statusProgressBar{
		x: statusTextX, y: y, width: textEnd - statusTextX, pct: e.progress,
		chars:     style.ProgressBar,
		done:      term.AttributesUnion(bar, style.ProgressDoneAttr),
		remaining: term.AttributesUnion(bar, remaining),
		label:     term.AttributesUnion(style.ProgressLabelAttr, base),
		graphics:  style.ProgressBarGraphics,
	}, true
}

func (v *View) drawPreview(w term.Writer, width, height int) {
	// What the selection previews may only just have become something
	// to open, such as a terminal done loading.
	v.sizePreview()
	s := v.preview()
	switch {
	case s != nil && s.comp != nil:
		s.comp.Draw(&component.VirtualWriter{Writer: w, Width: width, Height: height})
		return
	case s != nil && s.preview.borrowed():
		// The bottom of a program, such as its prompt, matters the most:
		// the top rows are the ones cut.
		p := s.preview
		p.Content.Draw(&component.VirtualWriter{
			Writer: w,
			Offset: term.Coordinates{Y: -max(p.Height-height, 0)},
			Width:  p.Width,
			Height: p.Height,
		})
		return
	}
	style, _ := v.style()
	none := component.NewStringWithConfig(statusPreviewNone, component.StringConfig{
		Alignment:  component.AlignmentCentered,
		Attributes: style.PlaceholderAttr,
	})
	none.Resize(width, height)
	none.Draw(w)
}

func (v *View) handleMouse(ev term.Event) bool {
	m := v.margin()
	x, y := ev.MouseX-m.Left, ev.MouseY-m.Top
	inSidebar := x >= 0 && x < v.sidebarWidth() && y >= 0 && y < v.sidebarHeight()
	handled := true
	switch ev.Key {
	case 0, term.MouseLeft:
	case term.MouseWheelDown, term.MouseWheelUp:
		handled = inSidebar && v.scroll(ev.Key == term.MouseWheelDown)
	default:
		return false
	}
	v.hover = -1
	if inSidebar && y < len(v.rows) {
		v.hover = v.rows[y]
	}
	if ev.Key == term.MouseLeft {
		v.selectAt(v.hover)
	}
	return handled
}

func (v *View) choose() {
	i := v.resolve()
	if i < 0 {
		return
	}
	e := v.entries[i]
	// The window of the view closes once Handle returns: show it on the
	// next tick, for the window to close in the workspace that opened it.
	_ = v.store.schedule(func() { v.store.host.Show(e.ws, e.uri) })
}

func (v *View) closeSelected() {
	i := v.resolve()
	if i < 0 {
		return
	}
	e := v.entries[i]
	host := v.store.host
	closeIt := func() { host.Close(e.ws, e.uri) }
	// On the next tick, as closing may close windows of the browser that
	// is dispatching to the view.
	switch e.state {
	case programstatus.Working, programstatus.Blocked:
		// Closing would interrupt it.
		_ = v.store.schedule(func() { host.Confirm(statusCloseMessage(e), closeIt) })
	default:
		_ = v.store.schedule(closeIt)
	}
}

type statusEntry struct {
	ws, uri workspaceapi.URI
	wsName  string
	state   programstatus.State
	// status is everything reported, for the user to know what closing
	// it would interrupt.
	status                      programstatus.Summary
	icon                        string
	name                        string
	sub                         string
	progress                    int
	nameAttr, iconAttr, subAttr term.Attributes
}

func (e statusEntry) height() int {
	ret := 2
	if e.wsName != "" {
		ret++
	}
	if e.progress >= 0 {
		ret++
	}
	return ret
}

func (e statusEntry) ref() statusRef {
	return statusRef{ws: e.ws.String(), uri: e.uri.String()}
}

type statusInbox struct {
	cfg browser.TabStatusConfig
}

func (in statusInbox) entries(items []Item) []statusEntry {
	items = slices.Clone(items)
	slices.SortStableFunc(items, func(a, b Item) int {
		return cmp.Or(
			programstatus.Precedence(b.Status.State)-programstatus.Precedence(a.Status.State),
			a.Slot-b.Slot,
			strings.Compare(a.Name, b.Name))
	})
	ret := make([]statusEntry, 0, len(items))
	for _, it := range items {
		ret = append(ret, in.entry(it))
	}
	return ret
}

func (in statusInbox) entry(it Item) statusEntry {
	st := it.Status
	app := cmp.Or(st.App, it.App)
	name := it.Name
	if (name == "" || name == detachedStatusLabel) && app != "" {
		name = app
	}
	// The title and the message would bury the name: the preview shows
	// them.
	sub := st.State.String()
	if app != "" && app != name {
		sub += statusSubSeparator + app
	}
	nameAttr, iconAttr := in.cfg.ContentTab.BarAttrs(st)
	subAttr := nameAttr
	if subAttr == (term.Attributes{}) {
		subAttr = iconAttr
	}
	progress := -1
	if st.State == programstatus.Working && st.Progress >= 0 {
		progress = min(st.Progress, 100)
	}
	return statusEntry{
		ws:       it.WS,
		uri:      it.URI,
		wsName:   it.WorkspaceName,
		state:    st.State,
		status:   st,
		icon:     in.glyph(st.State, st.Kind, it.Icon),
		name:     name,
		sub:      sub,
		progress: progress,
		nameAttr: nameAttr,
		iconAttr: iconAttr,
		subAttr:  subAttr,
	}
}

func (in statusInbox) glyph(state programstatus.State, kind programstatus.Kind, fallback rune) string {
	var icon rune
	switch state {
	case programstatus.Working:
		if len(in.cfg.Spinner) > 0 {
			icon = in.cfg.Spinner[0]
		}
	case programstatus.Blocked:
		icon = in.cfg.BlockedIcons[kind]
		if icon == 0 {
			icon = in.cfg.BlockedIcons[programstatus.KindNone]
		}
	case programstatus.Done:
		icon = in.cfg.DoneIcon
	case programstatus.Error:
		icon = in.cfg.ErrorIcon
	}
	if icon == 0 {
		icon = fallback
	}
	// No icon leaves the cell empty: the state is spelled out beneath the
	// name, and no word fits the one cell of an icon.
	if icon == 0 {
		return ""
	}
	return string(icon)
}

// statusShown is the preview of the selection ref, and the view it
// opened, if it did, width by height, which close closes.
type statusShown struct {
	ref           statusRef
	preview       Preview
	opened        bool
	comp          tui.Component
	close         func()
	width, height int
}

type statusRef struct {
	ws, uri string
}

type statusSpan struct{ i, y, n int }

type statusTicker struct {
	epoch  atomic.Int64
	cancel context.CancelFunc
}

func newStatusTicker(interrupter term.Interrupter, fps int) *statusTicker {
	ctx, cancel := context.WithCancel(context.Background())
	t := &statusTicker{cancel: cancel}
	go debug.CapturePanicReport(func() {
		ticker := time.NewTicker(time.Second / time.Duration(fps))
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				t.epoch.Add(1)
				_ = interrupter.Interrupt(ctx)
			case <-ctx.Done():
				return
			}
		}
	})
	return t
}

func sectionHeight(heights []int) int {
	ret := 0
	for i, h := range heights {
		if i > 0 {
			ret += statusGap
		}
		ret += h
	}
	return ret
}

func statusLayout(top int, heights []int, height int) []statusSpan {
	var ret []statusSpan
	y := statusPadTop
	for i := top; i < len(heights) && y < height; i++ {
		ret = append(ret, statusSpan{i: i, y: y, n: min(heights[i], height-y)})
		y += heights[i] + statusGap
	}
	return ret
}

func statusScroll(top, sel int, heights []int, rows int) int {
	top = min(top, max(len(heights)-1, 0))
	if sel >= 0 && sel < len(heights) {
		top = min(top, sel)
		for top < sel && sectionHeight(heights[top:sel+1]) > rows {
			top++
		}
	}
	// With room below the last item, scroll back up rather than leave
	// rows empty while items above are hidden.
	for top > 0 && sectionHeight(heights[top-1:]) <= rows {
		top--
	}
	return top
}

func statusText(w term.Writer, x, y int, s string, attr term.Attributes, end int) int {
	lines := term.StringToCells(s)
	if len(lines) == 0 {
		return x
	}
	for _, c := range lines[0] {
		cw := max(int(c.Width), 1)
		if x+cw > end {
			break
		}
		c.SetAttributes(attr)
		w.SetCell(term.Coordinates{X: x, Y: y}, c)
		x += cw
	}
	return x
}

// sidebarWriter writes to Writer with the colors of attr in place of
// the default colors of each cell, but for a cell that keeps the
// background under it, which keeps it whole. Its cells and images are
// drawn half a cell lower where the writer can, for the fill to stand
// as far from the window bar above it as from the frame's line below.
type sidebarWriter struct {
	term.Writer
	attr term.Attributes
}

func (w *sidebarWriter) SetCell(pos term.Coordinates, c term.Cell) {
	if c.Attrs&term.AttrKeepBackground == 0 {
		if c.Bg == term.ColorDefault {
			c.Bg = w.attr.Bg
		}
		if c.Fg == term.ColorDefault {
			c.Fg = w.attr.Fg
		}
	}
	c.Attrs |= term.AttrVerticalRenderOffset
	w.Writer.SetCell(pos, c)
}

func (w *sidebarWriter) DrawImage(img term.Image) bool {
	img.VerticalRenderOffset = true
	return w.Writer.DrawImage(img)
}

// statusProgressBar is a progress bar at pct, with its percentage on its
// right, width cells wide from x, y: in done as far as it is done, in
// remaining past that, and the percentage in label.
type statusProgressBar struct {
	x, y, width, pct       int
	chars                  component.ProgressBarCharSet
	done, remaining, label term.Attributes
	graphics               ProgressBarGraphics
}

// draw draws the bar, but blanks in place of the characters its images
// replace, or its percentage alone when the bar does not fit.
func (b statusProgressBar) draw(w term.Writer) {
	barW, filled, ok := b.layout()
	if !ok {
		statusText(w, b.x, b.y, strconv.Itoa(b.pct)+"%", b.label, b.x+b.width)
		return
	}
	for i := range barW {
		pos, c := b.cell(i, barW, filled)
		b.graphics.cell(i, barW, filled).DrawCells(w, pos, 1, false, c)
	}
	statusText(w, b.x+barW+1, b.y, b.text(), b.label, b.x+b.width)
}

// drawGraphics draws the images of the bar over what draw drew.
func (b statusProgressBar) drawGraphics(w term.Writer, g *tcomponent.FrameGraphics) {
	barW, filled, ok := b.layout()
	if !ok {
		return
	}
	for i := range barW {
		pos, c := b.cell(i, barW, filled)
		g.DrawLine(w, b.graphics.cell(i, barW, filled), pos, 1, false, c, term.ImageLayerAboveText)
	}
}

// layout returns how many cells the bar takes and how many of them are
// done, or false when only the percentage fits.
func (b statusProgressBar) layout() (barW, filled int, ok bool) {
	// Padded, the bar keeps its length as the percentage grows.
	barW = b.width - 1 - len(b.text())
	if barW < 2 {
		return 0, 0, false
	}
	// Rounding down keeps the bar from looking full before it is.
	return barW, b.pct * barW / 100, true
}

func (b statusProgressBar) text() string {
	return fmt.Sprintf("%3d%%", b.pct)
}

// cell returns where cell i of the bar is and what it shows, filled of
// its barW cells being done.
func (b statusProgressBar) cell(i, barW, filled int) (term.Coordinates, term.Cell) {
	r, attr := b.chars.Empty, b.remaining
	if i < filled {
		r, attr = b.chars.Filled, b.done
	}
	// The ends keep the colors of the cells they stand in, for a bar of
	// one glyph all along to read as one.
	switch i {
	case 0:
		r = b.chars.Left
	case barW - 1:
		r = b.chars.Right
	}
	return term.Coordinates{X: b.x + i, Y: b.y}, term.NewCell(r, 1, attr)
}

func statusCloseMessage(e statusEntry) string {
	var b strings.Builder
	b.WriteString("Are you sure you want to close `")
	b.WriteString(e.name)
	b.WriteString("`")
	if e.wsName != "" {
		b.WriteString(" in `")
		b.WriteString(e.wsName)
		b.WriteString("`")
	}
	b.WriteString("? It is ")
	b.WriteString(e.state.String())
	b.WriteString(".\n")
	for _, rec := range e.status.Records {
		b.WriteString("\n- ")
		b.WriteString(rec.State.String())
		if rec.App != "" {
			b.WriteString(statusSubSeparator)
			b.WriteString(rec.App)
		}
		if rec.ID != "" {
			b.WriteString(" (")
			b.WriteString(rec.ID)
			b.WriteString(")")
		}
		var text []string
		if rec.Title != "" {
			text = append(text, rec.Title)
		}
		if rec.Msg != "" {
			text = append(text, rec.Msg)
		}
		if rec.Progress >= 0 {
			text = append(text, strconv.Itoa(min(rec.Progress, 100))+"%")
		}
		if len(text) > 0 {
			b.WriteString(": ")
			b.WriteString(strings.Join(text, " — "))
		}
	}
	return b.String()
}

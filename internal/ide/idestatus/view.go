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
	// Divider draws the line between the programs and the preview in
	// DividerAttr, joined to the frame of the inbox's window, and
	// PlaceholderAttr the text that stands in for them when there is
	// nothing to show.
	Divider                      tcomponent.DividerCharSet
	DividerAttr, PlaceholderAttr term.Attributes
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
	vertical := component.FrameCharSetDefault().VerticalLeft
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
		Divider: tcomponent.DividerCharSet{
			Top: '┬', VerticalTop: vertical, Vertical: vertical, VerticalBottom: vertical, Bottom: '┴',
		},
		DividerAttr:     statusMutedAttr,
		PlaceholderAttr: statusMutedAttr,
	}
}

// Preview is what the inbox shows of a program: Content, drawn at Width
// by Height. Fit, if not nil, sizes Content when it was never sized, as
// it would draw nothing; content shown elsewhere must have none, as
// resizing it would reflow it there. Hidden content, shown nowhere, is
// fit to the preview and drawn whole.
type Preview struct {
	Content       tui.Component
	Width, Height int
	Fit           func(width, height int)
	Hidden        bool
}

// View is the status inbox: a sidebar listing every program that
// reported a status, beside a live preview of the selected one, kept up
// to date until it is closed. The preview draws its content at the
// content's own size, as it looks in its window, cutting the top rows
// and right columns that do not fit, and never sends it input. Use
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
	// fitted is the size hidden content was last fit to, for it to be
	// resized only as the preview changes size.
	fitted        statusFit
	minWidth      func() int
	width, height int
}

// Select selects uri in the workspace at ws, and reports whether the
// view lists it. It leaves the selection alone when it does not.
func (v *View) Select(ws, uri workspaceapi.URI) bool {
	ref := statusRef{ws: ws.String(), uri: uri.String()}
	i := slices.IndexFunc(v.entries, func(e statusEntry) bool { return e.ref() == ref })
	v.selectAt(i)
	return i >= 0
}

// Resize sizes the view, and fits the previewed content to the preview
// when that content was never sized or is hidden.
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
	sideW := v.sidebarWidth()
	v.drawSidebar(&component.VirtualWriter{Writer: w, Width: sideW, Height: v.height}, sideW)
	if sideW >= v.width {
		return
	}
	style := v.store.style
	divider := term.NewCell(style.Divider.Vertical, 1, style.DividerAttr)
	for y := range v.height {
		w.SetCell(term.Coordinates{X: sideW, Y: y}, divider)
	}
	x := sideW + statusDividerWidth
	if x >= v.width {
		return
	}
	v.drawPreview(&component.VirtualWriter{
		Writer: w, Offset: term.Coordinates{X: x}, Width: v.width - x, Height: v.height,
	}, v.width-x, v.height)
}

// StitchFrame joins the divider to the frame f drew width by height
// around the view, and draws the divider's images with g, unless it is
// nil. Called from within the view's own Draw, it draws nothing, as Draw
// does.
func (v *View) StitchFrame(
	w term.Writer, f *component.Frame, g *tcomponent.FrameGraphics, width, height int, bar bool,
) {
	sideW := v.sidebarWidth()
	if v.drawing || sideW >= v.width {
		return
	}
	x := f.ContentPosition().X + sideW
	if width < 3 || height < 3 || x <= 0 || x >= width-1 {
		return
	}
	style := v.store.style
	cs := style.Divider
	if !bar {
		w.SetCell(term.Coordinates{X: x}, term.NewCell(cs.Top, 1, f.Attributes))
		w.SetCell(term.Coordinates{X: x, Y: 1}, term.NewCell(cs.VerticalTop, 1, style.DividerAttr))
	}
	w.SetCell(term.Coordinates{X: x, Y: height - 2}, term.NewCell(cs.VerticalBottom, 1, style.DividerAttr))
	w.SetCell(term.Coordinates{X: x, Y: height - 1}, term.NewCell(cs.Bottom, 1, f.Attributes))
	if g != nil {
		g.DrawDivider(w, f, cs, style.DividerAttr, width, height, x, !bar)
	}
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

// Selection returns the name of the selected program, or false when
// nothing is listed.
func (v *View) Selection() (string, bool) {
	i := v.resolve()
	if i < 0 {
		return "", false
	}
	return v.entries[i].name, true
}

// Close stops updating the view and turning its spinners. It returns
// nil.
func (v *View) Close() error {
	delete(v.store.views, v)
	v.setTicking(false)
	return nil
}

// Dimensions returns the size the view asks for: the sidebar beside the
// previewed content at its own size, at least as wide as minWidth asks
// and 40 rows high.
func (v *View) Dimensions() (int, int) {
	sideW, sideH := v.sidebarSize()
	w, h := v.previewSize()
	width := sideW + statusDividerWidth + w
	if v.minWidth != nil {
		width = max(width, v.minWidth())
	}
	return width, max(sideH, h, statusMinHeight)
}

const (
	statusSubSeparator  = " · "
	statusViewEmpty     = "No program reported a status"
	statusPreviewNone   = "No preview"
	statusPreviewWidth  = 80
	statusPreviewHeight = 21
	statusSidebarMin    = 24
	statusSidebarMax    = 40
	statusDividerWidth  = 1
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
	v.fitted = statusFit{}
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
	return min(w, v.width)
}

func (v *View) preview() (Preview, bool) {
	i := v.resolve()
	if i < 0 {
		return Preview{}, false
	}
	return v.store.host.Preview(v.entries[i].ws, v.entries[i].uri)
}

func (v *View) previewSize() (int, int) {
	p, ok := v.preview()
	switch {
	case !ok:
		return graphemecluster.StringWidth(statusPreviewNone) + 4, 0
	case p.Hidden, p.Width == 0 && p.Height == 0:
		return statusPreviewWidth, statusPreviewHeight
	}
	return p.Width, p.Height
}

// sizePreview must run whenever the selection or the size changes.
func (v *View) sizePreview() {
	p, ok := v.preview()
	if !ok || p.Fit == nil {
		return
	}
	width := v.width - v.sidebarWidth() - statusDividerWidth
	if width <= 0 || v.height <= 0 {
		return
	}
	switch fit := (statusFit{ref: v.sel, width: width, height: v.height}); {
	case p.Hidden:
		if v.fitted != fit {
			v.fitted = fit
			p.Fit(width, v.height)
		}
	case p.Width == 0 && p.Height == 0:
		p.Fit(width, v.height)
	}
}

func (v *View) drawSidebar(w term.Writer, width int) {
	v.rows = slices.Repeat([]int{-1}, v.height)
	style := v.store.style
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
	g, images := v.store.graphics, v.store.images
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
	rows := v.height - statusPadTop
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
	v.rows = slices.Repeat([]int{-1}, v.height)
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
		v.store.style.MarkerGraphics.DrawCells(w, term.Coordinates{X: statusPadLeft, Y: s.y}, s.n,
			true, v.markerCell(base))
	}
}

// markerCell returns the marker drawn over base.
func (v *View) markerCell(base term.Attributes) term.Cell {
	style := v.store.style
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
	style := v.store.style
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
	style := v.store.style
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
	p, ok := v.preview()
	if ok && p.Hidden {
		p.Content.Draw(&component.VirtualWriter{Writer: w, Width: width, Height: height})
		return
	}
	if !ok || p.Width == 0 || p.Height == 0 {
		s := component.NewStringWithConfig(statusPreviewNone, component.StringConfig{
			Alignment:  component.AlignmentCentered,
			Attributes: v.store.style.PlaceholderAttr,
		})
		s.Resize(width, height)
		s.Draw(w)
		return
	}
	// The bottom of a program, such as its prompt, matters the most: the
	// top rows are the ones cut.
	p.Content.Draw(&component.VirtualWriter{
		Writer: w,
		Offset: term.Coordinates{Y: -max(p.Height-height, 0)},
		Width:  p.Width,
		Height: p.Height,
	})
}

func (v *View) handleMouse(ev term.Event) bool {
	inSidebar := ev.MouseX >= 0 && ev.MouseX < v.sidebarWidth() && ev.MouseY >= 0
	handled := true
	switch ev.Key {
	case 0, term.MouseLeft:
	case term.MouseWheelDown, term.MouseWheelUp:
		handled = inSidebar && v.scroll(ev.Key == term.MouseWheelDown)
	default:
		return false
	}
	v.hover = -1
	if inSidebar && ev.MouseY < len(v.rows) {
		v.hover = v.rows[ev.MouseY]
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

type statusFit struct {
	ref           statusRef
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

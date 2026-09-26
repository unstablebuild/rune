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

package searchbox

import (
	"runtime"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/term/graphemecluster"
	"unstable.build/rune/internal/cell"
	inputhandler "unstable.build/rune/internal/handler/input"
)

const (
	inputWidth = 32
	paddingX   = 1
	rowGap     = 1
	colGap     = 1
)

type focus uint8

const (
	focusQuery focus = iota
	focusReplacement
)

type button uint8

const (
	buttonNone button = iota
	buttonUpgrade
	buttonNext
	buttonAll
)

type rect struct {
	x, y, width, height int
}

func (r rect) contains(x, y int) bool {
	return x >= r.x && x < r.x+r.width && y >= r.y && y < r.y+r.height
}

type layout struct {
	width, height, contentHeight int
	query, replacement           rect
	upgrade, next, all           rect
}

type textInput struct {
	box             *inputhandler.Box
	buf             *cell.Buffer
	placeholder     string
	attr            term.Attributes
	placeholderAttr term.Attributes
	width, height   int
}

func newTextInput(value, placeholder string, cfg Config) *textInput {
	buf := cell.NewBuffer()
	buf.WriteString(value)
	box := inputhandler.NewBox(buf, cfg.Editor, inputhandler.BoxConfig{
		Placeholder:       placeholder,
		PlaceholderConfig: component.StringConfig{Attributes: cfg.PlaceholderAttr},
		DefaultFrameAttr:  cfg.FrameAttr,
		ContentConfig:     cfg.InputAttr,
	})
	box.Resize(inputWidth, 3)
	lines := strings.Split(value, "\n")
	if len(value) > 0 {
		_ = box.SetCursorAtScroll(term.Coordinates{
			X: len([]rune(lines[len(lines)-1])),
			Y: len(lines) - 1,
		})
	}
	return &textInput{
		box:             box,
		buf:             buf,
		placeholder:     placeholder,
		attr:            cfg.InputAttr,
		placeholderAttr: cfg.PlaceholderAttr,
	}
}

func (i *textInput) text() string { return i.buf.String() }

func (i *textInput) Resize(width, height int) {
	i.width, i.height = max(0, width), max(0, height)
	i.box.Resize(i.width+2, i.height+2)
}

func displayRows(value string, width int) int {
	if width <= 0 {
		return 0
	}
	if value == "" {
		return 1
	}
	rows, x := 1, 0
	for _, ch := range value {
		if ch == '\n' {
			rows, x = rows+1, 0
			continue
		}
		cw := max(1, graphemecluster.StringWidth(string(ch)))
		if x > 0 && x+cw > width {
			rows, x = rows+1, 0
		}
		x += cw
		if x >= width {
			rows, x = rows+1, 0
		}
	}
	return max(1, rows)
}

func (i *textInput) Draw(w term.Writer) {
	for y := range i.height {
		for x := range i.width {
			w.UnionAttributes(term.Coordinates{X: x, Y: y}, i.attr)
		}
	}
	value, attr := i.text(), i.attr
	if value == "" {
		value, attr = i.placeholder, i.placeholderAttr
	}
	from, to, selected := i.box.SelectionBounds()
	if selected {
		from, to = term.CoordinatesSort(from, to)
	}
	x, y, logicalX, logicalY := 0, 0, 0, 0
	for _, ch := range value {
		if y >= i.height {
			break
		}
		if ch == '\n' {
			x, y = 0, y+1
			logicalX, logicalY = 0, logicalY+1
			continue
		}
		cw := max(1, graphemecluster.StringWidth(string(ch)))
		if x > 0 && x+cw > i.width {
			x, y = 0, y+1
		}
		if y >= i.height || x >= i.width {
			break
		}
		w.SetCell(term.Coordinates{X: x, Y: y}, term.NewCell(ch, uint8(cw), attr))
		logical := term.Coordinates{X: logicalX, Y: logicalY}
		if selected && coordinatesInRange(logical, from, to) {
			for dx := range cw {
				w.UnionAttributes(term.Coordinates{X: x + dx, Y: y}, term.Attributes{Attrs: term.AttrReverse})
			}
		}
		logicalX++
		x += cw
		if x >= i.width {
			x, y = 0, y+1
		}
	}
}

func coordinatesInRange(pos, from, to term.Coordinates) bool {
	if pos.Y < from.Y || pos.Y > to.Y {
		return false
	}
	if pos.Y == from.Y && pos.X < from.X {
		return false
	}
	if pos.Y == to.Y && pos.X >= to.X {
		return false
	}
	return true
}

func (i *textInput) Handle(ev term.Event) (bool, bool) {
	return i.box.Handle(ev)
}

func (i *textInput) cursor() (term.Coordinates, term.CursorStyle, bool) {
	logical := i.box.CursorAtScroll()
	idx := 0
	for y, line := range strings.Split(i.text(), "\n") {
		if y == logical.Y {
			idx += min(logical.X, len([]rune(line)))
			break
		}
		idx += len([]rune(line)) + 1
	}
	x, y := 0, 0
	for n, ch := range []rune(i.text()) {
		if n >= idx {
			break
		}
		if ch == '\n' {
			x, y = 0, y+1
			continue
		}
		cw := max(1, graphemecluster.StringWidth(string(ch)))
		if x > 0 && x+cw > i.width {
			x, y = 0, y+1
		}
		x += cw
		if x >= i.width {
			x, y = 0, y+1
		}
	}
	_, style, show := i.box.Cursor()
	return term.Coordinates{X: x, Y: y}, style, show
}

func (i *textInput) clearCollapsedSelection() {
	from, to, ok := i.box.SelectionBounds()
	if ok && from == to {
		_, _ = i.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
	}
}

type floating struct {
	owner       *Box
	mode        Mode
	focus       focus
	query       *textInput
	replacement *textInput
	layout      layout
	seek        int
	hover       button
	pressed     button
	mouseInput  *textInput
	closed      bool
}

func newFloating(owner *Box, mode Mode, query string) *floating {
	f := &floating{owner: owner, mode: mode, query: newTextInput(query, "Find", owner.config)}
	if mode == ModeReplace {
		f.replacement = newTextInput("", "Replace with", owner.config)
	}
	f.setFocus(focusQuery)
	return f
}

func (f *floating) setFocus(focus focus) {
	f.focus = focus
}

func (f *floating) upgrade() {
	if f.mode == ModeReplace || f.owner.replace == nil {
		return
	}
	f.mode = ModeReplace
	f.replacement = newTextInput("", "Replace with", f.owner.config)
	f.setFocus(focusQuery)
	f.Resize(f.layout.width, f.layout.height)
}

func naturalInputWidth(f *floating) int {
	w := graphemecluster.StringWidth("Find")
	if query := f.query.text(); query != "" {
		w = graphemecluster.StringWidth(query) + 1
	}
	if f.mode == ModeReplace {
		replacementWidth := graphemecluster.StringWidth("Replace with")
		if replacement := f.replacement.text(); replacement != "" {
			replacementWidth = graphemecluster.StringWidth(replacement) + 1
		}
		w = max(w, replacementWidth)
	}
	return max(inputWidth, w+2)
}

func (f *floating) controlsWidth() int {
	if f.owner.replace == nil {
		return 0
	}
	if f.mode == ModeReplace {
		return 15
	}
	return 9
}

// Dimensions satisfies handler.Floating.
func (f *floating) Dimensions() (int, int) {
	width := 2*paddingX + naturalInputWidth(f)
	if controls := f.controlsWidth(); controls > 0 {
		width += colGap + controls
	}
	return width, f.measure(width, 0).contentHeight
}

// Height satisfies handler.Responsive.
func (f *floating) Height(width int) int {
	return f.measure(max(0, width), 0).contentHeight
}

func (f *floating) measure(width, height int) layout {
	ret := layout{width: width, height: height}
	controls := f.controlsWidth()
	available := max(0, width-2*paddingX)
	if controls > 0 {
		available -= colGap
	}
	inWidth := min(naturalInputWidth(f), max(3, available-controls))
	inWidth = min(inWidth, max(0, width-paddingX))
	innerWidth := max(1, inWidth-2)
	queryH := displayRows(f.query.text(), innerWidth) + 2
	paddingTop := f.owner.config.PaddingTop
	ret.query = rect{x: paddingX, y: paddingTop, width: inWidth, height: queryH}
	buttonX := ret.query.x + inWidth + colGap
	if f.owner.replace != nil {
		ret.upgrade = rect{x: buttonX, y: ret.query.y + (queryH-1)/2, width: 9, height: 1}
	}
	ret.contentHeight = paddingTop + queryH
	if f.mode == ModeReplace {
		replaceH := displayRows(f.replacement.text(), innerWidth) + 2
		ret.replacement = rect{x: paddingX, y: ret.query.y + queryH + rowGap,
			width: inWidth, height: replaceH}
		by := ret.replacement.y + (replaceH-1)/2
		ret.next = rect{x: buttonX, y: by, width: 9, height: 1}
		ret.all = rect{x: buttonX + 10, y: by, width: 5, height: 1}
		ret.contentHeight = ret.replacement.y + replaceH
	}
	return ret
}

// Resize satisfies tui.Component.
func (f *floating) Resize(width, height int) {
	f.layout = f.measure(max(0, width), max(0, height))
	f.query.Resize(max(0, f.layout.query.width-2), max(0, f.layout.query.height-2))
	if f.replacement != nil {
		f.replacement.Resize(max(0, f.layout.replacement.width-2),
			max(0, f.layout.replacement.height-2))
	}
	f.seek = min(f.seek, f.MaxSeekOffset())
	f.ensureFocusVisible()
}

func (f *floating) focusedRect() rect {
	if f.focus == focusReplacement && f.mode == ModeReplace {
		return f.layout.replacement
	}
	return f.layout.query
}

func (f *floating) ensureFocusVisible() {
	r := f.focusedRect()
	if r.y < f.seek {
		f.seek = r.y
	} else if f.layout.height > 0 && r.y+r.height > f.seek+f.layout.height {
		f.seek = r.y + r.height - f.layout.height
	}
	in := f.query
	if f.focus == focusReplacement && f.replacement != nil {
		in = f.replacement
	}
	pos, _, _ := in.cursor()
	cursorY := r.y + 1 + pos.Y
	if cursorY < f.seek {
		f.seek = cursorY
	} else if f.layout.height > 0 && cursorY >= f.seek+f.layout.height {
		f.seek = cursorY - f.layout.height + 1
	}
	f.seek = max(0, min(f.seek, f.MaxSeekOffset()))
}

func drawFrame(w term.Writer, r rect, attr term.Attributes) {
	if r.width < 3 || r.height < 3 {
		return
	}
	component.DrawFrame(w, component.FrameCharSetDefault(), attr, r.width-1, r.height-1)
}

func translatedWriter(w term.Writer, r rect, seek int) component.VirtualWriter {
	return component.VirtualWriter{Writer: w, Offset: term.Coordinates{X: r.x, Y: r.y - seek},
		Width: r.width, Height: r.height}
}

// Draw satisfies tui.Component.
func (f *floating) Draw(w term.Writer) {
	viewport := component.VirtualWriter{Writer: w, Width: f.layout.width, Height: f.layout.height}
	for y := range f.layout.height {
		for x := range f.layout.width {
			viewport.UnionAttributes(term.Coordinates{X: x, Y: y}, f.owner.config.Attr)
		}
	}
	f.drawInput(&viewport, f.query, f.layout.query)
	if f.owner.replace == nil {
		return
	}
	if f.mode == ModeFind {
		f.drawButton(&viewport, f.layout.upgrade, " Replace ", buttonUpgrade)
		return
	}
	f.drawInput(&viewport, f.replacement, f.layout.replacement)
	f.drawButton(&viewport, f.layout.next, " Replace ", buttonNext)
	f.drawButton(&viewport, f.layout.all, " All ", buttonAll)
}

func (f *floating) drawInput(w term.Writer, in *textInput, r rect) {
	frameAttr := f.owner.config.FrameAttr
	if (in == f.query && f.focus == focusQuery) ||
		(in == f.replacement && f.focus == focusReplacement) {
		frameAttr = f.owner.config.FocusFrameAttr
	}
	fw := translatedWriter(w, r, f.seek)
	drawFrame(&fw, rect{width: r.width, height: r.height}, frameAttr)
	inner := rect{x: r.x + 1, y: r.y + 1, width: max(0, r.width-2), height: max(0, r.height-2)}
	iw := translatedWriter(w, inner, f.seek)
	in.Draw(&iw)
}

func (f *floating) drawButton(w term.Writer, r rect, label string, b button) {
	if r.x+r.width > f.layout.width || r.y-f.seek < 0 || r.y-f.seek >= f.layout.height {
		return
	}
	attr := f.owner.config.ButtonAttr
	if f.hover == b {
		attr = f.owner.config.ButtonHoverAttr
	}
	x := r.x
	for _, ch := range label {
		w.SetCell(term.Coordinates{X: x, Y: r.y - f.seek},
			term.NewCell(ch, 1, attr))
		x++
	}
}

// Cursor satisfies tui.Handler.
func (f *floating) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	in, r := f.query, f.layout.query
	if f.focus == focusReplacement && f.replacement != nil {
		in, r = f.replacement, f.layout.replacement
	}
	pos, style, show := in.cursor()
	pos.X += r.x + 1
	pos.Y += r.y + 1 - f.seek
	if pos.Y < 0 || pos.Y >= f.layout.height || pos.X >= f.layout.width {
		show = false
	}
	return pos, style, show
}

// Selection satisfies tui.Handler.
func (f *floating) Selection() (string, bool) {
	in := f.query
	if f.focus == focusReplacement && f.replacement != nil {
		in = f.replacement
	}
	if selected, ok := in.box.Selection(); ok {
		return selected, true
	}
	return f.owner.controller.Selection()
}

// Handle satisfies tui.Handler.
func (f *floating) Handle(ev term.Event) (bool, bool) {
	if f.owner.matchesFind(ev) {
		f.setFocus(focusQuery)
		f.owner.controller.AdvanceSearch()
		f.ensureFocusVisible()
		return false, true
	}
	if f.owner.replace != nil && keyMatches(ev, f.owner.config.ReplaceKey) {
		if f.mode == ModeFind {
			f.upgrade()
		} else {
			f.setFocus(focusReplacement)
			f.ensureFocusVisible()
		}
		return false, true
	}
	in := f.query
	if f.focus == focusReplacement && f.replacement != nil {
		in = f.replacement
	}
	if ev.Type == term.EventKey {
		if ev.Key == term.KeyEsc ||
			(ev.Ch == 'c' && ev.Mod == term.ModCtrl && !copiesOwnSelection(in, ev)) {
			// ctrl-c is the terminal's own cancel gesture, so it closes
			// the box like <esc> instead of being treated as a request
			// to edit the query, unless it is also the copy chord and
			// something inside the box is selected.
			return true, true
		}
		if ev.Key == term.KeyTab {
			if f.mode == ModeReplace {
				if f.focus == focusQuery {
					f.setFocus(focusReplacement)
				} else {
					f.setFocus(focusQuery)
				}
				f.ensureFocusVisible()
			}
			return false, true
		}
		if ev.Key == term.KeyEnter {
			f.owner.controller.AdvanceSearch()
			return false, true
		}
	}
	if ev.Type == term.EventMouse {
		return f.handleMouse(ev)
	}
	if isCopyShortcut(ev) && !copiesOwnSelection(in, ev) {
		// nothing is selected in the query/replacement input, so this
		// is not a request to copy them: let the host's own copy
		// binding see the event and act on whatever else is selected.
		return false, false
	}
	before := in.text()
	_, handled := in.Handle(ev)
	if in.text() != before {
		if in == f.query {
			f.owner.controller.SetSearchQuery(in.text())
		}
		f.Resize(f.layout.width, f.layout.height)
	}
	return false, handled
}

// copyShortcut is the query/replacement editor's own
// empty-selection-copies-the-line chord: cmd-c on macOS and ctrl-c
// elsewhere, where the standard editor leaves Super to the desktop.
// Config always wires these inputs to the standard editor, so the
// combination is fixed regardless of what edits the content behind the
// box.
var copyShortcut = hostCopyShortcut(runtime.GOOS)

func hostCopyShortcut(goos string) term.KeyComb {
	if goos == "darwin" {
		return term.KeyComb{Mod: term.ModMeta, Ch: 'c'}
	}
	return term.KeyComb{Mod: term.ModCtrl, Ch: 'c'}
}

func isCopyShortcut(ev term.Event) bool {
	return ev.Type == term.EventKey && ev.Ch == copyShortcut.Ch && ev.Mod == copyShortcut.Mod
}

// copiesOwnSelection reports whether ev copies a selection made inside
// in, which is the only copy the box itself claims.
func copiesOwnSelection(in *textInput, ev term.Event) bool {
	if !isCopyShortcut(ev) {
		return false
	}
	_, ok := in.box.Selection()
	return ok
}

func (f *floating) buttonAt(x, y int) button {
	y += f.seek
	for _, item := range []struct {
		button button
		rect   rect
	}{{buttonUpgrade, f.layout.upgrade}, {buttonNext, f.layout.next}, {buttonAll, f.layout.all}} {
		if item.rect.width > 0 && item.rect.x+item.rect.width <= f.layout.width &&
			item.rect.contains(x, y) {
			return item.button
		}
	}
	return buttonNone
}

func (f *floating) handleMouse(ev term.Event) (bool, bool) {
	b := f.buttonAt(ev.MouseX, ev.MouseY)
	contentY := ev.MouseY + f.seek
	if f.mouseInput != nil && (ev.Key == term.MouseLeft || ev.Key == term.MouseRelease) {
		in := f.mouseInput
		r := f.layout.query
		if in == f.replacement {
			r = f.layout.replacement
		}
		ev.MouseX -= r.x
		ev.MouseY = contentY - r.y
		_, handled := in.Handle(ev)
		if ev.Key == term.MouseRelease {
			in.clearCollapsedSelection()
			f.mouseInput = nil
		}
		f.ensureFocusVisible()
		return false, handled
	}
	if b == buttonNone && ev.Key == term.MouseLeft {
		var in *textInput
		var r rect
		switch {
		case f.layout.query.contains(ev.MouseX, contentY):
			f.setFocus(focusQuery)
			in, r = f.query, f.layout.query
		case f.mode == ModeReplace && f.layout.replacement.contains(ev.MouseX, contentY):
			f.setFocus(focusReplacement)
			in, r = f.replacement, f.layout.replacement
		}
		if in != nil {
			ev.MouseX -= r.x
			ev.MouseY = contentY - r.y
			if ev.MouseX > 0 && ev.MouseY > 0 && ev.MouseX < r.width-1 && ev.MouseY < r.height-1 {
				f.mouseInput = in
				_, handled := in.Handle(ev)
				f.ensureFocusVisible()
				return false, handled
			}
		}
	}
	if ev.Key == 0 {
		f.hover = b
		return false, b != buttonNone
	}
	if ev.Key == term.MouseLeft {
		f.pressed = b
		return false, b != buttonNone
	}
	if ev.Key != term.MouseRelease {
		return false, false
	}
	pressed := f.pressed
	f.pressed = buttonNone
	if pressed == buttonNone || pressed != b {
		return false, b != buttonNone
	}
	switch b {
	case buttonUpgrade:
		f.upgrade()
	case buttonNext:
		f.owner.replace.ReplaceNext(f.replacement.text())
	case buttonAll:
		f.owner.replace.ReplaceAll(f.replacement.text())
	}
	return false, true
}

// Close satisfies tui.Component.
func (f *floating) Close() error {
	if f.closed {
		return nil
	}
	f.closed = true
	return f.owner.Finish(true)
}

// SeekUp satisfies component.Scrollable.
func (f *floating) SeekUp() bool {
	if f.seek == 0 {
		return false
	}
	f.seek--
	return true
}

// SeekDown satisfies component.Scrollable.
func (f *floating) SeekDown() bool {
	if f.seek >= f.MaxSeekOffset() {
		return false
	}
	f.seek++
	return true
}

// SeekOffset satisfies component.Scrollable.
func (f *floating) SeekOffset() int { return f.seek }

// MaxSeekOffset satisfies component.Scrollable.
func (f *floating) MaxSeekOffset() int {
	return max(0, f.layout.contentHeight-f.layout.height)
}

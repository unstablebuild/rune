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

package text

import (
	"context"
	"errors"
	"math"
	"strconv"
	"sync"

	"github.com/ernestrc/go-multierror"
	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/blue/logging"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/ide/vctrl"
)

// AuxBarConfig holds configuration for the auxiliary bar created
// by WithAuxBar.
type AuxBarConfig struct {
	GitEnabled          bool
	LinesEnabled        bool
	FoldsEnabled        bool
	AbsoluteLines       bool
	HighlightCursor     bool
	ScheduleNextTick    func(func()) bool
	DelAttr             term.Attributes
	AddAttr             term.Attributes
	DelOverlayAttr      term.Attributes
	AddOverlayAttr      term.Attributes
	HighlightCursorAttr term.Attributes
	LineNumberAttr      term.Attributes
	// Publisher is used to subscribe to events that refresh the Git diff.
	// It's optional if git is disabled by setting GitEnabled to false.
	Publisher EventPublisher
	// CommandRegistry is used to register commands to the parent workspace.
	// It's optional if git is disabled by setting GitEnabled to false.
	CommandRegistry FileCommandRegistry
	// Service is used to calculate diffs. It's optional if git is
	// disabled by setting GitEnabled to false.
	Service vctrl.Service
}

// WithAuxBar wraps the given editor with an auxiliary bar. The given buffer,
// and scroll should correspond to the buffer and scroll used by the given editor.
func WithAuxBar(
	handler Handler, buf *cell.Buffer,
	scroll *component.Scroll, config AuxBarConfig,
) Handler {
	if config.ScheduleNextTick == nil ||
		((config.GitEnabled && config.LinesEnabled) &&
			(config.Publisher == nil || config.CommandRegistry == nil)) {
		panic("auxbar configuration is missing key dependencies")
	}
	ret := new(auxBar)
	ret.pub = config.Publisher
	ret.buf = buf
	ret.file = handler.Resource()
	ret.scroll = scroll
	ret.Handler = handler
	ret.vhandler.C = handler
	ret.registry = config.CommandRegistry
	ret.scheduleNextTick = config.ScheduleNextTick
	ret.foldsEnabled = config.FoldsEnabled
	ret.gitEnabled = config.GitEnabled && config.LinesEnabled
	ret.linesEnabled = config.LinesEnabled
	ret.absoluteLines = config.AbsoluteLines
	ret.highlightCursor = config.HighlightCursor
	ret.folds = make(map[term.Coordinates]term.Coordinates)
	ret.prevRows = buf.View().Rows()
	ret.setLinesWidth()
	ret.svc = vctrl.NewCache(config.Service)
	if config.DelAttr == (term.Attributes{}) {
		config.DelAttr = term.Attributes{Fg: term.ColorMaroon}
	}
	if config.AddAttr == (term.Attributes{}) {
		config.AddAttr = term.Attributes{Fg: term.ColorGreen}
	}
	ret.delAttr = config.DelAttr
	ret.addAttr = config.AddAttr
	if config.DelOverlayAttr == (term.Attributes{}) {
		config.DelOverlayAttr = term.Attributes{Bg: term.ColorMaroon}
	}
	if config.AddOverlayAttr == (term.Attributes{}) {
		config.AddOverlayAttr = term.Attributes{Bg: term.ColorGreen}
	}
	if config.HighlightCursorAttr == (term.Attributes{}) {
		config.HighlightCursorAttr = term.Attributes{
			Fg:    term.ColorWhite,
			Bg:    term.ColorGray,
			Attrs: term.AttrBold,
		}
	}
	ret.cursorAttr = config.HighlightCursorAttr
	if config.LineNumberAttr == (term.Attributes{}) {
		config.LineNumberAttr = term.Attributes{Fg: term.ColorGray}
	}
	ret.barLineAttr = config.LineNumberAttr
	ret.config = config

	b := new(cell.Buffer)
	b.InitPerformance(buf.Rows(), foldsWidth+ret.linesWidth, ' ')

	ret.bar = new(component.Scroll)
	ret.bar.InitPerformance(b)
	if !ret.linesEnabled || ret.absoluteLines {
		ret.bar.SetOffset(term.Coordinates{Y: scroll.Offset().Y})
	}
	ret.bar.Attributes.Bg = ret.barLineAttr.Bg
	ret.bar.Attributes.Fg = ret.barLineAttr.Fg

	scroll.Subscribe(ret)
	buf.Subscribe(ret)

	// set before in case SubsribeEvents calls Handle
	ret.cancelBuild = func() {}
	ret.dirty = true
	evs := []textapi.EventType{
		textapi.EventTypeFocus,
		textapi.EventTypeChange,
		textapi.EventTypeRename,
		textapi.EventTypeCreate,
		textapi.EventTypeFlush,
	}
	if ret.linesEnabled && ret.gitEnabled {
		_ = ret.pub.SubscribeEvents(evs, (*auxBarSubscriber)(ret))
		for _, cmd := range gitCommands {
			// this could fail if gitbar is also enabled
			_ = ret.registry.SubscribeCommandForFile(ret.file, cmd, ret)
		}
	}

	if ret.dirty {
		ret.rebuildBar()
	}
	return ret
}

const foldsWidth = 2

const (
	hiddenFoldIcon  = ''
	visibleFoldIcon = ''
)

type auxBar struct {
	Handler
	pub              EventPublisher
	scheduleNextTick func(func()) bool
	file             workspaceapi.URI
	svc              *vctrl.Cache
	registry         FileCommandRegistry
	foldsEnabled     bool
	linesEnabled     bool
	gitEnabled       bool
	absoluteLines    bool
	highlightCursor  bool
	config           AuxBarConfig

	buf    *cell.Buffer
	scroll *component.Scroll

	dirty       bool
	closed      bool
	cancelBuild func()
	vhandler    handler.Virtual[Handler]
	bar         *component.Scroll
	folds       map[term.Coordinates]term.Coordinates
	linesWidth  int
	height      int
	barWidth    int
	prevCursor  term.Coordinates
	prevOffset  term.Coordinates
	prevRows    int
	delLocAttr  term.Attributes
	addLocAttr  term.Attributes
	delAttr     term.Attributes
	addAttr     term.Attributes
	cursorAttr  term.Attributes
	barLineAttr term.Attributes
}

func (b *auxBar) Selection() (string, bool) {
	return b.vhandler.Selection()
}

func (b *auxBar) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return b.vhandler.Cursor()
}

func (b *auxBar) Draw(w term.Writer) {
	b.vhandler.Draw(w)
	b.bar.Draw(w)
	if b.highlightCursor {
		cursor, _, _ := b.vhandler.Cursor()
		y := cursor.Y
		for x := range b.barWidth {
			w.UnionAttributes(term.Coordinates{Y: y, X: x}, b.cursorAttr)
		}
	}
	if b.bar.Width() != 0 {
		bg := term.Attributes{Bg: b.scroll.Attributes.Bg}
		for y := range b.bar.SizeHeight() {
			for x := range b.bar.Width() {
				w.UnionAttributes(term.Coordinates{Y: y, X: x}, bg)
			}
		}
	}
}

func (b *auxBar) Handle(ev term.Event) (quit, handled bool) {
	fold := b.foldsEnabled && ev.Type == term.EventMouse && ev.Key == term.MouseLeft &&
		ev.MouseX >= b.linesWidth && ev.MouseX < b.linesWidth+foldsWidth
	if !fold {
		quit, handled = b.vhandler.Handle(ev)
		prevCursor := b.prevCursor
		prevOffset := b.prevOffset
		b.prevOffset = b.scroll.Offset()
		b.prevCursor, _, _ = b.vhandler.C.Cursor()
		if b.linesEnabled && !b.absoluteLines &&
			(prevCursor.Y != b.prevCursor.Y || prevOffset.Y != b.prevOffset.Y) {
			b.rebuildBar()
		}
		return
	}

	posAtWindow := term.Coordinates{Y: ev.MouseY, X: ev.MouseX}
	posAtScroll := b.scroll.WindowToScrollCoordinates(posAtWindow)
	posAtScroll.X = 0
	folded, ok := b.foldAt(posAtWindow)
	b.log(log.TraceLevel, "received mouse click at bar,"+
		" win pos: %+v, scroll pos: %+v, folded: %t, ok: %t",
		posAtWindow, posAtScroll, folded, ok)
	if ok && folded {
		handled = b.scroll.MarkVisible(posAtScroll.Y)
	} else if ok {
		if end, ok := b.folds[posAtScroll]; ok {
			handled = b.scroll.MarkHidden(posAtScroll.Y, end.Y)
		} else {
			b.log(log.DebugLevel, "received mouse click at drawn fold: %+v,"+
				" but no fold in map", posAtScroll)
		}
	}
	return
}

func (b *auxBar) Resize(width, height int) {
	rebuild := b.height != height && b.linesEnabled && !b.absoluteLines
	b.height = height
	b.setLinesWidth()
	b.barWidth = b.linesWidth
	if b.foldsEnabled {
		b.barWidth += foldsWidth
	}
	const padding = 2
	if width < padding+b.barWidth {
		b.barWidth = 0
	}
	b.bar.Resize(b.barWidth, height)
	b.vhandler.Move(term.Coordinates{X: b.barWidth})
	b.vhandler.Resize(width-b.barWidth, height)
	if rebuild {
		b.rebuildBar()
	}
}

// Dimensions satisfies text.Handler: reports the ideal dimensions
// of the inner handler plus the horizontal cells claimed by the
// auxiliary line-number bar (and folds strip) for the buffer's
// current row count.
func (b *auxBar) Dimensions() (int, int) {
	w, h := b.vhandler.C.Dimensions()
	if !b.linesEnabled {
		return w, h
	}
	bar := len(strconv.Itoa(b.buf.View().Rows())) + 1
	if b.foldsEnabled {
		bar += foldsWidth
	}
	return w + bar, h
}

func (b *auxBar) HandleCommand(ctx context.Context, cmd textapi.Command) error {
	switch cmd.Name {
	case commandToggleOverlay:
		if b.addLocAttr == (term.Attributes{}) {
			b.addLocAttr = b.config.AddOverlayAttr
		} else {
			b.addLocAttr = term.Attributes{}
		}
		if b.delLocAttr == (term.Attributes{}) {
			b.delLocAttr = b.config.DelOverlayAttr
		} else {
			b.delLocAttr = term.Attributes{}
		}
		b.rebuildBar()
		return nil
	default:
		return nil
	}
}

func (b *auxBar) Complete(ctx context.Context, cmd textapi.Command) (
	iterator.Iterator[string], string, error,
) {
	return iterator.Empty[string](), "", nil
}

func (b *auxBar) Close() (ret error) {
	if b.closed {
		return nil
	}
	b.closed = true
	if b.gitEnabled {
		ok, err := b.pub.UnsubscribeEvents((*auxBarSubscriber)(b))
		if err != nil {
			ret = multierror.Append(ret, err)
		} else if !ok {
			ret = multierror.Append(ret, errors.New("could not unsubscribe auxiliary bar"))
		}
		for _, cmd := range gitCommands {
			// could return error if git bar is enabled
			_ = b.registry.UnsubscribeCommandForFile(b.file, cmd.Name)
		}
	}
	if err := b.vhandler.C.Close(); err != nil {
		ret = multierror.Append(ret, err)
	}
	return
}

func (b *auxBar) SetCursorAtScroll(pos term.Coordinates) bool {
	ok := b.Handler.SetCursorAtScroll(pos)
	b.rebuildBar()
	b.prevCursor, _, _ = b.vhandler.Cursor()
	return ok
}

func (b *auxBar) MoveToNextLocation(ID string) bool {
	ok := b.Handler.MoveToNextLocation(ID)
	b.rebuildBar()
	return ok
}

func (b *auxBar) MoveToPrevLocation(ID string) bool {
	ok := b.Handler.MoveToPrevLocation(ID)
	b.rebuildBar()
	return ok
}

type auxBarSubscriber auxBar

func (b *auxBarSubscriber) Handle(ctx context.Context, ev textapi.Event) bool {
	if !ev.URI.Equal(b.file) {
		return false
	}
	switch ev.Type {
	case textapi.EventTypeFocus:
	case textapi.EventTypeChange, textapi.EventTypeRename,
		textapi.EventTypeCreate:
		b.svc.Purge()
		(*auxBar)(b).log(log.TraceLevel, "invalidated by event: %s", ev.Type.String())
		return false
	case textapi.EventTypeFlush:
		b.svc.Purge()
	default:
		return false
	}
	(*auxBar)(b).log(log.TraceLevel, "received event: %s", ev.Type.String())
	(*auxBar)(b).rebuildBar()
	return false
}

func (b *auxBar) foldAt(posAtWindow term.Coordinates) (folded, ok bool) {
	posAtBar := posAtWindow
	if !b.linesEnabled || b.absoluteLines {
		posAtBar = b.windowToBarCoordinates(posAtWindow)
	}
	cells := b.bar.Buffer().RawCells()
	if posAtBar.Y >= len(cells) {
		return
	}
	if posAtBar.X >= len(cells[posAtBar.Y]) {
		return
	}
	switch cells[posAtBar.Y][posAtBar.X].Ch {
	case hiddenFoldIcon:
		ok = true
		folded = true
	case visibleFoldIcon:
		ok = true
	default:
	}
	return
}

// The build outlives whatever triggered it: an edit applied by a code
// action carries the command's context, which is cancelled the moment
// the command returns, and a cancelled build leaves the gutter blank
// until something else rebuilds it.
func (b *auxBar) rebuildBar() {
	b.cancelBuild()
	ctx, cancel := context.WithCancel(context.Background())
	b.cancelBuild = cancel
	uri := b.Handler.Resource()
	b.dirty = false

	var diff textapi.LocationList
	var foldsIterator iterator.Iterator[term.Range]
	var wg sync.WaitGroup

	b.bar.Buffer().ResetPerformance()
	b.rebuildLines(ctx)

	if b.gitEnabled {
		wg.Add(1)
		go debug.CapturePanicReport(func() {
			defer wg.Done()
			// perform diff in a separate gouroutine in case
			// scheme is remote and diff performs network I/O.
			filediff, err := b.svc.Diff(ctx, uri)
			if err != nil {
				b.log(log.DebugLevel, "compute diff: %v", err)
				return
			}
			b.log(log.TraceLevel, "computed diff: %v", filediff)
			diff = filediff.LocationList(b.delLocAttr, b.addLocAttr)
		})
	}

	svc, ok := b.buf.View().(foldsService)
	if b.foldsEnabled && ok {
		wg.Add(1)
		offset := b.scroll.Offset()
		go debug.CapturePanicReport(func() {
			defer wg.Done()
			var folds iterator.Iterator[term.Range]
			var ok bool
			if !b.linesEnabled || b.absoluteLines {
				folds, ok = svc.Folds()
			} else {
				// relative bar doesn't need folds above current view,
				// and it ignores the folds below current view.
				// This makes recalculating on every cursor change much
				// more efficient.
				folds, ok = svc.FoldsFrom(offset)
			}
			if !ok {
				return
			}
			folds, isEmpty := iterator.IsEmpty(ctx, folds)
			if isEmpty {
				return
			}
			// once first fold has been returned, this should not block on I/O anymore
			// run on next loop tick, so we don't need to worry about synchronization
			foldsIterator = folds
		})
	}

	// rebuild it synchronously if there's no pending work
	pendingWork := (b.foldsEnabled && ok) || b.gitEnabled
	if !pendingWork {
		return
	}

	go debug.CapturePanicReport(func() {
		wg.Wait()
		b.scheduleNextTick(func() {
			select {
			case <-ctx.Done():
				return
			default:
			}

			b.bar.Buffer().ResetPerformance()
			b.rebuildLines(ctx)
			if diff != nil {
				if b.linesEnabled && !b.absoluteLines {
					b.rebuildGitRelative(diff)
				} else {
					b.rebuildGitAbsolute(diff)
				}
			}
			if foldsIterator != nil {
				defer foldsIterator.Close()
				if b.linesEnabled && !b.absoluteLines {
					b.rebuildFoldsRelative(ctx, foldsIterator)
				} else {
					b.rebuildFoldsAbsolute(ctx, foldsIterator)
				}
			}
		})
	})
}

func (b *auxBar) rebuildGitRelative(ll textapi.LocationList) {
	cells := b.bar.Buffer().RawCells()
	for loc, ok := ll.Current(); ok; loc, ok = ll.Next() {
		from := term.Coordinates{Y: loc.From.Y}
		to := term.Coordinates{Y: loc.To.Y}
		if from == to {
			at, ok := b.scroll.ScrollToWindowCoordinates(from)
			if !ok || at.Y < 0 || at.Y >= b.height || at.Y >= len(cells) {
				continue
			}
			for x := 0; x < b.linesWidth; x++ {
				if x >= len(cells[at.Y]) {
					break
				}
				cells[at.Y][x].SetAttributes(term.AttributesUnion(
					cells[at.Y][x].Attributes(), b.delAttr))
			}
			continue
		}

		for y := from.Y; y < to.Y; y++ {
			at, _ := b.scroll.ScrollToWindowCoordinates(term.Coordinates{Y: y})
			if !ok || at.Y < 0 || at.Y >= b.height || at.Y >= len(cells) {
				continue
			}
			for x := 0; x < b.linesWidth; x++ {
				if x >= len(cells[at.Y]) {
					break
				}
				cells[at.Y][x].SetAttributes(term.AttributesUnion(
					cells[at.Y][x].Attributes(), b.addAttr))
			}
		}
	}
	b.Handler.SetLocationList(textapi.LocationPriorityInfo, gitLocationsID, ll)
}

func (b *auxBar) rebuildGitAbsolute(ll textapi.LocationList) {
	cells := b.bar.Buffer().RawCells()
	for loc, ok := ll.Current(); ok; loc, ok = ll.Next() {
		from := term.Coordinates{Y: loc.From.Y}
		to := term.Coordinates{Y: loc.To.Y}
		if from == to {
			at, _ := b.scrollToBarCoordinates(from)
			// hidden || lines removed between ticks
			if at.Y >= len(cells) {
				continue
			}
			for x := 0; x < b.linesWidth; x++ {
				if x >= len(cells[at.Y]) {
					break
				}
				cells[at.Y][x].SetAttributes(term.AttributesUnion(
					cells[at.Y][x].Attributes(), b.delAttr))
			}
			continue
		}

		for y := from.Y; y < to.Y; y++ {
			at, _ := b.scrollToBarCoordinates(term.Coordinates{Y: y})
			// hidden || lines removed between ticks
			if at.Y >= len(cells) {
				continue
			}
			for x := 0; x < b.linesWidth; x++ {
				if x >= len(cells[at.Y]) {
					break
				}
				cells[at.Y][x].SetAttributes(term.AttributesUnion(
					cells[at.Y][x].Attributes(), b.addAttr))
			}
		}
	}
	b.Handler.SetLocationList(textapi.LocationPriorityInfo, gitLocationsID, ll)
}

func (b *auxBar) rebuildLines(ctx context.Context) {
	if !b.linesEnabled {
		return
	}
	if b.absoluteLines {
		b.rebuildLinesAbsolute(ctx)
	} else {
		b.rebuildLinesRelative(ctx)
	}
}

func (b *auxBar) rebuildLinesAbsolute(ctx context.Context) {
	for y := range b.buf.View().Rows() {
		from, ok := b.scrollToBarCoordinates(term.Coordinates{Y: y})
		if !ok {
			// inside hidden block
			continue
		}
		from.X = 0
		to := from
		number := strconv.Itoa(y + 1)
		b.bar.Buffer().EditWithAttr(ctx, from, to, number, b.barLineAttr)
	}
}

func (b *auxBar) rebuildLinesRelative(ctx context.Context) {
	cursorAtWindow, _, _ := b.vhandler.Cursor()
	for y := range b.height {
		n := int(math.Abs(float64(cursorAtWindow.Y - y)))
		var number string
		if n == 0 {
			cursorAtScroll := b.scroll.WindowToScrollCoordinates(cursorAtWindow)
			number = strconv.Itoa(cursorAtScroll.Y + 1)
		} else {
			number = strconv.Itoa(n)
		}
		from := term.Coordinates{Y: y}
		b.bar.Buffer().EditWithAttr(ctx, from, from, number, b.barLineAttr)
	}
}

func (b *auxBar) rebuildFoldsRelative(
	ctx context.Context, folds iterator.Iterator[term.Range],
) {
	clear(b.folds)
	var i int
	for {
		fold, ok := folds.Next(ctx)
		if !ok {
			break
		}
		i++

		// we're not interested in these
		if fold.Start.Y >= fold.End.Y {
			continue
		}

		// avoid ambiguity at bar
		fold.Start.X = 0
		if end, exists := b.folds[fold.Start]; exists && end.Y > fold.End.Y {
			continue
		}
		b.folds[fold.Start] = fold.End

		// convert folds which are scroll coordinates
		// to window coordinates
		foldStart, startOk := b.scroll.ScrollToWindowCoordinates(fold.Start)
		foldEnd, endOk := b.scroll.ScrollToWindowCoordinates(fold.End)
		if foldStart.Y != foldEnd.Y && (!startOk || !endOk) {
			// inside hidden block
			continue
		}

		if foldStart.Y < 0 {
			continue
		}
		if foldStart.Y >= b.height {
			break
		}
		foldStart.X = 0

		var icon rune
		if foldStart.Y == foldEnd.Y {
			icon = hiddenFoldIcon
		} else {
			icon = visibleFoldIcon
		}
		from := foldStart
		from.X += b.linesWidth
		to := from
		to.X++ // replace
		b.bar.Buffer().Edit(ctx, from, to, "")
		b.bar.Buffer().InsertWithAttr(from, icon, b.barLineAttr)
	}
	if err := folds.Err(); err != nil {
		b.log(log.ErrorLevel, "error rebuilding auxiliary bar: %v", err)
	}
	b.log(log.TraceLevel, "got %d folds", i)
}

func (b *auxBar) rebuildFoldsAbsolute(
	ctx context.Context, folds iterator.Iterator[term.Range],
) {
	clear(b.folds)
	for {
		fold, ok := folds.Next(ctx)
		if !ok {
			break
		}

		// we're not interested in these
		if fold.Start.Y >= fold.End.Y {
			continue
		}

		// avoid ambiguity at bar
		fold.Start.X = 0
		if end, exists := b.folds[fold.Start]; exists && end.Y > fold.End.Y {
			continue
		}
		b.folds[fold.Start] = fold.End

		// convert folds which are scroll coordinates
		// to window coordinates
		foldStart, startOk := b.scroll.ScrollToWindowCoordinates(fold.Start)
		foldEnd, endOk := b.scroll.ScrollToWindowCoordinates(fold.End)
		if foldStart.Y != foldEnd.Y && (!startOk || !endOk) {
			// inside hidden block
			continue
		}

		// convert to a scroll coordinates with hidden lines taken into account
		offset := b.scroll.Offset()
		foldStart.Y += offset.Y
		foldEnd.Y += offset.Y
		foldStart.X = 0

		if foldStart.Y < 0 {
			panic("invalid aux bar coordinates after conversion")
		}

		var icon rune
		if foldStart.Y == foldEnd.Y {
			icon = hiddenFoldIcon
		} else {
			icon = visibleFoldIcon
		}
		from := foldStart
		from.X += b.linesWidth
		to := from
		to.X++ // replace
		b.bar.Buffer().Edit(ctx, from, to, "")
		b.bar.Buffer().InsertWithAttr(from, icon, b.barLineAttr)
	}
	if err := folds.Err(); err != nil {
		b.log(log.ErrorLevel, "error rebuilding auxiliary bar: %v", err)
	}
}

func (b *auxBar) OnDidSeek(from, to term.Coordinates) {
	if !b.linesEnabled || b.absoluteLines {
		b.bar.SetOffset(term.Coordinates{Y: to.Y})
		return
	}
	if from.Y == to.Y {
		return
	}
	prevOffset := b.prevOffset
	b.prevOffset = b.scroll.Offset()
	if prevOffset.Y != b.prevOffset.Y {
		b.rebuildBar()
	}
}

func (b *auxBar) OnWillSeek(_ term.Coordinates) {
}

func (b *auxBar) OnWillHide(start, end int) {
}

func (b *auxBar) OnWillVisible(start int) {
}

func (b *auxBar) OnDidHide(start, end int) {
	b.rebuildBar()
}

func (b *auxBar) OnDidVisible(start int) {
	b.rebuildBar()
}

func (b *auxBar) OnWillEdit(
	ctx context.Context, from, to term.Coordinates, str string,
) {
}

func (b *auxBar) OnDidEdit(
	ctx context.Context, start, end term.Coordinates, old string,
) {
	prevRows := b.prevRows
	b.prevRows = b.buf.View().Rows()
	if prevRows != b.prevRows {
		b.setLinesWidth()
		b.rebuildBar()
	}
}

func (b *auxBar) setLinesWidth() {
	if b.linesEnabled {
		b.linesWidth = len(strconv.Itoa(b.buf.View().Rows())) + 1
	} else {
		b.linesWidth = 0
	}
}

func (b *auxBar) windowToBarCoordinates(pos term.Coordinates) term.Coordinates {
	pos.Y += b.scroll.Offset().Y
	// this method is used for cursor coordinates,
	// which hovers across both bar and content
	if pos.X >= b.barWidth {
		pos.X += b.scroll.Offset().X
	}
	return pos
}

func (b *auxBar) scrollToBarCoordinates(pos term.Coordinates) (ret term.Coordinates, ok bool) {
	pos, ok = b.scroll.ScrollToWindowCoordinates(pos)
	ret = pos
	ret.Y += b.scroll.Offset().Y
	ret.X += b.scroll.Offset().X
	return
}

func (b *auxBar) log(level log.Level, msg string, args ...any) {
	if !log.IsLevelEnabled(level) {
		return
	}
	log.WithField(logging.KeyClass, "text.auxBar").Logf(level, msg, args...)
}

type foldsService interface {
	Folds() (iterator.Iterator[term.Range], bool)
	FoldsFrom(from term.Coordinates) (iterator.Iterator[term.Range], bool)
}

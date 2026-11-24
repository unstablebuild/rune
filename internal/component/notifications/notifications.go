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

package notifications

import (
	"image"
	"math"
	"slices"
	"time"

	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	tcomponent "unstable.build/rune/internal/component"
)

var _ component.Responsive = (*notificationComp)(nil)

type notificationComp struct {
	component.Responsive
	cfg                    Config
	level                  Level
	msg                    string
	cancel                 func()
	width                  int
	height                 int
	duration               time.Duration
	manualProgress         int64
	manualProgressTotal    int64
	end                    time.Time
	pausedAt               time.Time
	progressCellStart      term.Cell
	progressCellEnd        term.Cell
	progressCellCurrent    term.Cell
	progressCellCurrentTip term.Cell
	progressCellRemain     term.Cell
	// bar holds the cells of the progress bar from left to right, and
	// keep those of its cells under an image, as Draw last laid them out.
	bar  []barCell
	keep []term.Coordinates
	// masks paints the inside of the lines of the frame's images.
	masks tcomponent.FrameMasks
}

// barCell is a cell of the progress bar and the image that goes over it.
type barCell struct {
	cell term.Cell
	uri  string
}

func newString(cfg Config, msg string) component.Responsive {
	strConfig := component.StringResponsiveConfig{
		NoSplitWords: true,
		StringConfig: component.StringConfig{
			Alignment:            component.AlignmentCentered,
			BackgroundRune:       ' ',
			Attributes:           cfg.Attributes,
			BackgroundAttributes: cfg.BackgroundAttributes,
			PaddingHorizontal:    2,
			FrameCharSet:         cfg.FrameCharSet,
			MinWidth:             cfg.Width,
		},
	}
	return component.NewResponsiveString(msg, strConfig)
}

func newNotification(
	level Level, msg string, cfg Config, duration time.Duration,
	cancel func(),
) *notificationComp {
	ret := &notificationComp{
		level:    level,
		msg:      msg,
		cancel:   cancel,
		duration: duration,
		end:      time.Now().Add(duration),
	}
	ret.style(cfg)
	return ret
}

// style draws n with the style of cfg from the next Draw on.
func (n *notificationComp) style(cfg Config) {
	// template for each progress rune
	var progressCell term.Cell
	switch n.level {
	case LevelInfo:
		progressCell.Attrs = cfg.ColorInfo.Attrs
		progressCell.Fg = cfg.ColorInfo.Fg
		progressCell.Bg = cfg.ColorInfo.Bg
	case LevelSuccess:
		progressCell.Attrs = cfg.ColorSuccess.Attrs
		progressCell.Fg = cfg.ColorSuccess.Fg
		progressCell.Bg = cfg.ColorSuccess.Bg
	case LevelWarn:
		progressCell.Attrs = cfg.ColorWarning.Attrs
		progressCell.Fg = cfg.ColorWarning.Fg
		progressCell.Bg = cfg.ColorWarning.Bg
	case LevelError:
		progressCell.Attrs = cfg.ColorError.Attrs
		progressCell.Fg = cfg.ColorError.Fg
		progressCell.Bg = cfg.ColorError.Bg
	default:
		panic("unknown level")
	}
	progressCell.Width = 1

	n.progressCellStart = progressCell
	n.progressCellStart.Ch = cfg.ProgressRunes.Start
	n.progressCellEnd = progressCell
	n.progressCellEnd.Ch = cfg.ProgressRunes.End
	n.progressCellRemain = progressCell
	n.progressCellRemain.Ch = cfg.ProgressRunes.Remain
	n.progressCellCurrent = progressCell
	n.progressCellCurrent.Ch = cfg.ProgressRunes.Current
	n.progressCellCurrentTip = progressCell
	n.progressCellCurrentTip.Ch = cfg.ProgressRunes.CurrentTip

	n.cfg = cfg
	n.setMessage(n.msg)
}

func (n *notificationComp) setMessage(msg string) {
	n.msg = msg
	n.Responsive = newString(n.cfg, msg)
	n.Responsive.Resize(n.width, n.height)
}

func (n *notificationComp) Resize(width, height int) {
	n.width = width
	n.height = height
	n.Responsive.Resize(width, height)
}

// inside returns the colour inside the notification's frame.
func (n *notificationComp) inside() term.Color {
	if bg := n.cfg.BackgroundAttributes.Bg; bg != term.ColorDefault {
		return bg
	}
	return n.cfg.Attributes.Bg
}

func (n *notificationComp) Draw(w term.Writer) {
	var g *tcomponent.FrameGraphics
	if n.cfg.Graphics != nil && n.cfg.hasGraphics() {
		g = n.cfg.Graphics()
	}
	frame := component.Frame{
		FrameCharSet: n.cfg.FrameCharSet,
		Attributes:   n.cfg.Attributes,
	}
	if n.cfg.ProgressBar {
		frame.Attributes = term.Attributes{
			Attrs: n.progressCellStart.Attrs,
			Fg:    n.progressCellStart.Fg,
			Bg:    n.progressCellStart.Bg,
		}
		frame.Graphics.BottomLeft = ""
		frame.Graphics.HorizontalBottom = ""
		frame.Graphics.BottomRight = ""
	}
	n.layoutBar()
	// The images of the frame and of the progress bar go over what is
	// under the notification, which their transparent pixels show
	// outside their lines, and the notification's colour inside them.
	cells := w
	if g != nil {
		n.keep = n.keep[:0]
		for x, c := range n.bar {
			if c.uri != "" {
				n.keep = append(n.keep, term.Coordinates{X: x, Y: n.height - 1})
			}
		}
		cells = &tcomponent.FrameUnderWriter{
			Writer: w, Graphics: frame.Graphics, Width: n.width, Height: n.height,
			Keep: n.keep,
		}
	}
	n.Responsive.Draw(cells)
	if n.cfg.ProgressBar && n.width >= 3 && n.height >= 3 {
		component.DrawFrame(cells, n.cfg.FrameCharSet, frame.Attributes, n.width-1, n.height-1)
	}
	area := image.Rect(0, 0, n.width, n.height)
	if g != nil {
		g.DrawOver(&tcomponent.FrameInsideWriter{
			Writer: w, Area: area, Inside: n.inside(), Masks: &n.masks,
		}, &frame, n.width, n.height)
	}
	if len(n.bar) == 0 {
		return
	}

	bar := &tcomponent.FrameInsideWriter{Writer: w, Area: area, Inside: n.inside(), Masks: &n.masks}
	if n.cfg.ProgressRunes.Graphics.Overlay {
		// The level's colour, which the runes would show through the
		// images, fills what they enclose: their clear pixels open to
		// the outside show what is under the notification instead.
		bar.Inside, bar.Enclosed = n.progressCellStart.Fg, true
	}
	for x, c := range n.bar {
		pos := term.Coordinates{X: x, Y: n.height - 1}
		if g == nil || c.uri == "" {
			w.SetCell(pos, c.cell)
			continue
		}
		g.DrawCell(bar, c.uri, pos, c.cell)
	}
}

// layoutBar lays the progress bar out in n.bar, which it leaves empty
// when n shows none or its progress is past the end.
func (n *notificationComp) layoutBar() {
	n.bar = n.bar[:0]
	if !n.cfg.ProgressBar || n.width <= 0 {
		return
	}
	remaining := time.Until(n.end)
	if !n.pausedAt.IsZero() {
		remaining = n.end.Sub(n.pausedAt)
	}
	total := int64(n.duration)
	current := total - int64(remaining)
	size := n.width

	if n.manualProgressTotal != 0 {
		current = n.manualProgress
		total = n.manualProgressTotal
	}
	currCount := max(0, int(math.Ceil(
		float64(current)/float64(total)*float64(size),
	)))
	if size < currCount {
		return
	}

	pg := n.cfg.ProgressRunes.Graphics
	n.bar = slices.Grow(n.bar, size)[:size]
	n.bar[0] = barCell{n.progressCellStart, pg.Start}
	for x := 1; x < currCount; x++ {
		n.bar[x] = barCell{n.progressCellCurrent, pg.Current}
	}
	if currCount != 0 && currCount < size {
		n.bar[currCount] = barCell{n.progressCellCurrentTip, pg.CurrentTip}
	}
	for x := currCount + 1; x < size-1; x++ {
		n.bar[x] = barCell{n.progressCellRemain, pg.Remain}
	}
	n.bar[size-1] = barCell{n.progressCellEnd, pg.End}
}

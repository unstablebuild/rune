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
	"math"
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

func (n *notificationComp) Draw(w term.Writer) {
	n.Responsive.Draw(w)

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
		if n.width >= 3 && n.height >= 3 {
			component.DrawFrame(w, n.cfg.FrameCharSet, frame.Attributes, n.width-1, n.height-1)
		}
	}
	if g != nil {
		g.Draw(w, &frame, n.width, n.height)
	}
	if !n.cfg.ProgressBar {
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
	currCount := int(math.Ceil(
		float64(current) / float64(total) * float64(size),
	))
	if size < currCount {
		return
	}
	remaCount := size - currCount

	pg := n.cfg.ProgressRunes.Graphics
	set := func(x int, cell term.Cell, uri string) {
		pos := term.Coordinates{X: x, Y: n.height - 1}
		w.SetCell(pos, cell)
		if g != nil {
			g.DrawCell(w, uri, pos, cell, pg.Overlay)
		}
	}

	set(0, n.progressCellStart, pg.Start)

	for x := 1; x < currCount; x++ {
		set(x, n.progressCellCurrent, pg.Current)
	}
	if currCount != 0 {
		set(currCount, n.progressCellCurrentTip, pg.CurrentTip)
	}

	for x := currCount + 1; x < currCount+remaCount-1; x++ {
		set(x, n.progressCellRemain, pg.Remain)
	}

	set(size-1, n.progressCellEnd, pg.End)
}

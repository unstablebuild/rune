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
	"strings"

	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// pinnedEllipsis ends a pinned message that did not fit its rows.
const pinnedEllipsis = "…"

// pinnedMessage draws a user message over the top rows of the
// transcript. A message can be as tall as the whole viewport, so it is
// flattened onto as few rows as it wraps to and cut short with an
// ellipsis past the rows it is given.
type pinnedMessage struct {
	strCfg  component.StringResponsiveConfig
	spanCfg component.SpanConfig
	// hoverBg replaces the message background while hovered, to tell the
	// pin apart from the transcript as something that can be clicked.
	hoverBg term.Color
	hovered bool

	// The last layout is kept because the pin is redrawn on every frame
	// while its text and size rarely change.
	text        string
	width       int
	maxRows     int
	laidHovered bool
	rows        int
	comp        component.Responsive
}

// layout prepares text to be drawn in at most maxRows rows of width and
// returns how many rows it takes.
func (p *pinnedMessage) layout(text string, width, maxRows int) int {
	if width <= 0 || maxRows <= 0 {
		return 0
	}
	if p.comp == nil || p.text != text || p.width != width || p.maxRows != maxRows ||
		p.laidHovered != p.hovered {
		p.text, p.width, p.maxRows, p.laidHovered = text, width, maxRows, p.hovered
		p.comp = p.fit(strings.Join(strings.Fields(text), " "), width, maxRows)
		p.rows = min(p.comp.Height(width), maxRows)
	}
	return p.rows
}

// fit returns the component showing the longest prefix of flat that
// wraps to at most maxRows rows, ellipsized when anything was cut.
func (p *pinnedMessage) fit(flat string, width, maxRows int) component.Responsive {
	full := p.newComp(flat)
	if full.Height(width) <= maxRows {
		return full
	}
	runes := []rune(flat)
	lo, hi := 0, len(runes)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if p.newComp(string(runes[:mid])+pinnedEllipsis).Height(width) <= maxRows {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return p.newComp(strings.TrimRight(string(runes[:lo]), " ") + pinnedEllipsis)
}

func (p *pinnedMessage) newComp(s string) component.Responsive {
	cfg := p.strCfg
	cfg.Bg = p.background()
	cfg.BackgroundAttributes.Bg = cfg.Bg
	return component.NewSpan(component.NewResponsiveString(s, cfg), p.spanCfg)
}

// background is the color the pin's message is drawn on.
func (p *pinnedMessage) background() term.Color {
	if p.hovered {
		return p.hoverBg
	}
	return p.strCfg.BackgroundAttributes.Bg
}

// draw paints the last layout over the top rows of w.
func (p *pinnedMessage) draw(w term.Writer) {
	if p.comp == nil || p.rows <= 0 {
		return
	}
	blank := term.NewCell(' ', 1, term.Attributes{Bg: p.background()})
	for y := range p.rows {
		for x := range p.width {
			w.SetCell(term.Coordinates{X: x, Y: y}, blank)
		}
	}
	p.comp.Resize(p.width, p.rows)
	p.comp.Draw(w)
}

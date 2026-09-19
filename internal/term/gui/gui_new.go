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

package gui

import (
	"context"
	"fmt"
	"sync"

	ebiten "github.com/hajimehoshi/ebiten/v2"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"github.com/unstablebuild/tcell/v3"
	"unstable.build/rune/internal/term/gui/drawrect"
	"unstable.build/rune/internal/term/gui/font"
)

// New allocates storage for a new GUI and initializes it with the given
// tui.Handler and options.
func New(handler tui.Handler, options ...Option) (*GUI, error) {
	drawrect.Init()
	const (
		cellOverlapX = 0
		cellOverlapY = 0
	)
	fontManager, err := font.NewManager(cellOverlapX, cellOverlapY)
	if err != nil {
		return nil, fmt.Errorf("font manager: %v", err)
	}
	ret := &GUI{
		mu:               new(sync.Mutex),
		handler:          handler,
		updateChan:       make(chan term.Event, 4096),
		bgOpacity:        1,
		fgOpacity:        1,
		fontManager:      fontManager,
		enableLigatures:  true,
		cursorAttributes: term.Attributes{Bg: term.FromTcellColor(tcell.ColorRed)},
		startPositionX:   0,
		startPositionY:   0,
		defaultWidth:     defaultWidth,
		defaultHeight:    defaultHeight,
	}
	ret.input = newInput(ret.fontManager)
	ret.mouse = newMouse(ret.fontManager)
	ret.drag = newDragPoller(ret.mouse)
	ret.links = newLinkScanner()
	ret.ctx = context.Background()
	ret.ctx, ret.cancelCtx = context.WithCancel(ret.ctx)

	ret.defaultAttr.Fg = term.FromTcellColor(tcell.ColorWhite)
	ret.defaultAttr.Bg = term.FromTcellColor(tcell.ColorBlack)
	for _, option := range options {
		if err := option(ret); err != nil {
			return nil, fmt.Errorf("option: %w", err)
		}
	}
	if ret.processWindowClosed == nil {
		ret.processWindowClosed = func() []term.Event { return nil }
	}

	ret.originalColorValues = tcell.GetColorValues()
	ret.originalValuesColor = tcell.GetValuesColor()

	if ret.initialTheme != "" {
		ret.setTheme(ret.initialTheme, ret.colorThemes[ret.initialTheme])
	}

	ret.resize(ret.defaultWidth, ret.defaultHeight, ret.fontManager.DeviceScale())

	return ret, nil
}

// Run starts the main loop and runs the graphical TUI with the specified options.
func (g *GUI) Run(title string) error {
	defer func() { _ = g.Close() }()

	prepareNativeWindow(title)

	ebiten.SetScreenClearedEveryFrame(false)
	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeEnabled)
	ebiten.SetRunnableOnUnfocused(true)
	ebiten.SetFPSMode(ebiten.FPSModeVsyncOffMinimum)
	ebiten.SetTPS(ebiten.SyncWithFPS)

	ebiten.SetWindowPosition(g.startPositionX, g.startPositionY)
	width, height := g.defaultWidth, g.defaultHeight
	if !g.explicitSize {
		if m := ebiten.Monitor(); m != nil {
			if w, h := m.Size(); w > 0 && h > 0 {
				width, height = w, h
			}
		}
	}
	ebiten.SetWindowSize(width, height)

	if g.bgBlurRadius != 0 && g.enableTransparent {
		ebiten.SetWindowBackgroundBlur(g.bgBlurRadius)
	}
	ebiten.SetWindowDecorations(ebiten.DecorationsButtonsOnly)
	if g.closingHandled {
		ebiten.SetWindowClosingHandled(true)
	}

	gameOpts := g.buildRunGameOptions()
	return ebiten.RunGameWithOptions(g, &gameOpts)
}

// Close releases GUI-owned resources and restores process-global color state.
// It is safe to call more than once.
func (g *GUI) Close() error {
	g.closeOnce.Do(func() {
		g.cancelCtx()
		if g.renderer != nil {
			g.renderer.deallocate()
			g.renderer = nil
		}
		tcell.SetColorValues(g.originalColorValues)
	})
	return nil
}

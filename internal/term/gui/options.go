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
	"fmt"
	"net/url"
	"sync"

	ebiten "github.com/hajimehoshi/ebiten/v2"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/tcell/v3"
)

// Option allows configuring an instance of GUI.
type Option func(g *GUI) error

// AltModifier names the Alt key reserved for layout characters — Option on
// macOS, AltGr elsewhere. The zero value reserves neither.
type AltModifier uint8

const (
	AltModifierNone AltModifier = iota
	AltModifierRight
	AltModifierLeft
)

// String returns the config spelling of m.
func (m AltModifier) String() string {
	switch m {
	case AltModifierRight:
		return "right"
	case AltModifierLeft:
		return "left"
	}
	return "none"
}

// ParseAltModifier resolves a config value; "" and "none" reserve neither.
func ParseAltModifier(s string) (AltModifier, error) {
	switch s {
	case "", "none":
		return AltModifierNone, nil
	case "right":
		return AltModifierRight, nil
	case "left":
		return AltModifierLeft, nil
	}
	return AltModifierNone,
		fmt.Errorf("expected 'left', 'right' or 'none', got %q", s)
}

// WithFontFamily defines the opentype font family to use.
// See font.Manager.SetFontByFamilyName for more details.
func WithFontFamily(family string) Option {
	return func(g *GUI) error {
		return g.fontManager.SetFontByFamilyName(family)
	}
}

// WithOpacity sets the initial background opacity.
func WithOpacity(fg, bg float64) Option {
	return func(g *GUI) error {
		g.fgOpacity = fg
		g.bgOpacity = bg
		return nil
	}
}

// WithBackgroundBlur sets the initial background blur.
// It only takes effect if WithTransparentWindow is set to true,
// and WithOpacity has been used to set a non 1 background opacity.
func WithBackgroundBlur(radius int) Option {
	return func(g *GUI) error {
		g.bgBlurRadius = radius
		return nil
	}
}

// WithKeyMapping installs a key remapping table applied to GUI input before
// events reach the event loop. The map is keyed by the term.KeyComb a physical
// key would naturally produce (including synthetic physical keys such as
// CapsLock) and maps it to the target combination to deliver instead.
func WithKeyMapping(m map[term.KeyComb]term.KeyComb) Option {
	return func(g *GUI) error {
		g.input.setKeyMapping(m)
		return nil
	}
}

// WithAltModifier selects which Alt key produces layout characters.
func WithAltModifier(modifier AltModifier) Option {
	return func(g *GUI) error {
		g.input.setAltModifier(modifier)
		return nil
	}
}

// WithSize sets the initial width and height of the window in pixels.
func WithSize(width, height int) Option {
	return func(g *GUI) error {
		g.defaultWidth = width
		g.defaultHeight = height
		g.explicitSize = true
		return nil
	}
}

// WithPosition sets the initial position of the window in pixels offset.
func WithPosition(x, y int) Option {
	return func(g *GUI) error {
		g.startPositionX = x
		g.startPositionY = y
		return nil
	}
}

// WithFontSize defines the size of the default font
// or the font set via WithFontFamily.
//
// A size of 0 selects the DPI-aware automatic default, where the point
// size is resolved from the display's device scale (larger on low-DPI
// displays).
func WithFontSize(size float64) Option {
	return func(g *GUI) error {
		return g.fontManager.SetSize(size)
	}
}

// WithFontDPI sets the font DPI of the default font
// or the font set via WithFontFamily.
// If value is 0, the DPI is automatically calculated.
func WithFontDPI(dpi float64) Option {
	return func(g *GUI) error {
		return g.fontManager.SetDPI(dpi)
	}
}

// WithDeviceScale sets the device scale factor of the monitor.
// If value is 0, the device scale is automatically detected.
func WithDeviceScale(scale float64) Option {
	return func(g *GUI) error {
		g.fontManager.SetDeviceScale(scale)
		return nil
	}
}

// WithLigatures enables or disables font ligatures.
func WithLigatures(enable bool) Option {
	return func(g *GUI) error {
		g.enableLigatures = enable
		return nil
	}
}

// WithTransparentWindow enables or disables the ability to
// change the window foreground and background opacity.
func WithTransparentWindow(enable bool) Option {
	return func(g *GUI) error {
		g.enableTransparent = enable
		return nil
	}
}

// WithCursorAttributes defines the colors of the cursor.
func WithCursorAttributes(attr term.Attributes) Option {
	return func(g *GUI) error {
		g.cursorAttributes = attr
		return nil
	}
}

// WithLocker defines the locker to be used to synchronize
// access to the GUI's root tui.Handler.
func WithLocker(mu sync.Locker) Option {
	return func(g *GUI) error {
		g.mu = mu
		return nil
	}
}

// WithCloseRequestEvent routes window close requests (the window's
// close button, or the platform asking the application to quit) to the
// handler as ev instead of ending the run loop, so the handler decides
// whether to exit. Without it, a close request tears the window down
// immediately.
func WithCloseRequestEvent(ev term.Event) Option {
	return func(g *GUI) error {
		g.closingHandled = true
		// ebiten consumes the closing flag on the first read of a
		// frame, so each request yields the event exactly once.
		g.processWindowClosed = func() []term.Event {
			if ebiten.IsWindowBeingClosed() {
				return []term.Event{ev}
			}
			return nil
		}
		return nil
	}
}

// WithLineHeightOffset defines positive or negative offset given
// to the font's default line height.
// See font.Manager.SetOffset for more details.
func WithLineHeightOffset(offset float64) Option {
	return func(g *GUI) error {
		return g.fontManager.SetOffsetY(offset)
	}
}

// WithColumnWidthOffset defines positive or negative offset given
// to the font's default column width.
// See font.Manager.SetOffset for more details.
func WithColumnWidthOffset(offset float64) Option {
	return func(g *GUI) error {
		return g.fontManager.SetOffsetX(offset)
	}
}

// WithRenderOffset defines the render offset in pixels.
// Default is no offset.
func WithRenderOffset(x, y int) Option {
	return func(g *GUI) error {
		g.renderOffset.X = x
		g.renderOffset.Y = y
		return nil
	}
}

// WithPrintFPS prints the current FPS in the resulting graphical screen.
func WithPrintFPS(print bool) Option {
	return func(g *GUI) error {
		g.printFPS = print
		return nil
	}
}

// WithForceFullRepaint disables the renderer's row-damage tracking so
// every frame repaints the whole grid. It is a debug/benchmark knob:
// the differential correctness harness compares the damage-tracked path
// against this reference full-repaint path, and it doubles as a runtime
// fallback if damage tracking is ever suspected of a rendering bug.
func WithForceFullRepaint(force bool) Option {
	return func(g *GUI) error {
		g.forceFullRepaint = force
		return nil
	}
}

// WithScrollMultiplier sets the mouse wheel scroll multiplier: the number of
// lines scrolled per unit of wheel movement reported by the host. Higher values
// scroll faster. Fractional wheel deltas from high-resolution devices such as
// trackpads are accumulated, so the multiplier scales smooth scrolling as well
// as discrete notches. Values <= 0 are ignored. Default is 3.
func WithScrollMultiplier(multiplier float64) Option {
	return func(g *GUI) error {
		if multiplier > 0 {
			g.mouse.multiplier = multiplier
		}
		return nil
	}
}

// Theme is a color theme which defines the default foreground and background colors
// as well as color mappings between colors. Tipically the initial 16-bit colors supported
// by XTERM/ECMA are mapped to arbitrary RGB colors.
type Theme struct {
	Foreground tcell.Color
	Background tcell.Color
	Cursor     tcell.Color
	Colors     map[tcell.Color]tcell.Color
}

// WithColorThemes defines the color themes available for later calls to GUI.SetTheme,
// and initial is used as the default theme.
func WithColorThemes(initial string, themes map[string]Theme) Option {
	return func(g *GUI) error {
		g.initialTheme = initial
		g.colorThemes = themes
		return nil
	}
}

// WithDragObserver installs a callback invoked when files are dragged over
// or dropped on the window. It runs on the GUI loop goroutine, in the same
// context as handler events. Hosts that do not report file drags never
// invoke it. A nil observer is ignored.
func WithDragObserver(observer func(DragEvent)) Option {
	return func(g *GUI) error {
		if observer != nil {
			g.drag.observer = observer
		}
		return nil
	}
}

// WithLinkObserver installs a callback invoked when a URL rendered in the
// frame is clicked while the meta modifier is held. It runs on the GUI
// loop goroutine, in the same context as handler events. A nil observer
// is ignored.
func WithLinkObserver(observer func(*url.URL)) Option {
	return func(g *GUI) error {
		if observer != nil {
			g.links.observer = observer
		}
		return nil
	}
}

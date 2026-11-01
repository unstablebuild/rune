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

package ide

import (
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/browser"
	tcomponent "unstable.build/rune/internal/component"
	"unstable.build/rune/internal/handler/command"
)

// commandPromptShaderConfig bundles the gating flag and the optional
// RadarFrameParams overrides for the command-prompt shader. Unset
// overrides (colorSet == false, angularWidth == 0, cycles == 0) mean
// "keep the default from glslshader.DefaultRadarFrameParams".
type commandPromptShaderConfig struct {
	enabled      bool
	color        term.Color
	colorSet     bool
	angularWidth float64
	cycles       int
}

// commandPromptSeparatorCharset configures the four glyphs that
// stitch the prompt's manual/list separator row into the surrounding
// frame. From left to right within the separator row they are placed
// at columns 0, 1, W-2, W-1 of the floating window. The defaults
// produce a single-line T-junction stitch (├ ─ ─ ┤).
type commandPromptSeparatorCharset struct {
	Left            rune
	HorizontalLeft  rune
	HorizontalRight rune
	Right           rune
}

// defaultCommandPromptSeparatorCharset returns the stitch glyphs used
// when no override is configured.
func defaultCommandPromptSeparatorCharset() commandPromptSeparatorCharset {
	return commandPromptSeparatorCharset{
		Left:            '├',
		HorizontalLeft:  '─',
		HorizontalRight: '─',
		Right:           '┤',
	}
}

// commandPromptConfig groups every command-prompt-only knob the ex
// constructor consumes. It is intentionally kept out of text.Config
// because none of these settings need to round-trip through the
// editor's text configuration.
type commandPromptConfig struct {
	shader    commandPromptShaderConfig
	separator commandPromptSeparatorCharset
	// frameCharSet is what the prompt draws its frame and separator row
	// with, and the runes its radar shades. When nil, the prompt draws
	// the window frame charset and its radar shades the focused one.
	frameCharSet *component.FrameCharSet
	// keyBindingHint returns the long-form key label bound to a full
	// command line, or "" when unbound. nil disables the hints.
	keyBindingHint     func(commandLine string) string
	keyBindingHintAttr term.Attributes
	// keyBindingHintFocusAttr styles the key hint on the focused row.
	keyBindingHintFocusAttr term.Attributes
}

func newCommandPromptHandler(
	cmd *command.Prompt, e *ex, onClose func() error,
) browser.Floating {
	padded := handler.NewSpan(cmd, component.SpanConfig{
		PadHorizontal:    2,
		ContentAlignment: component.AlignmentCentered,
	})
	bg := component.NewBackground(padded, term.NewCell(0, 0, term.Attributes{
		Bg:    e.config.CommandOverlay.ElementAttr.Bg,
		Attrs: e.config.CommandOverlay.ElementAttr.Attrs,
	}))
	if !e.config.WindowManagerConfig.Frame {
		return browser.FuncFloating(
			browser.FuncHandler(
				handler.WithComponent(padded, bg),
				onClose,
			),
			padded.Dimensions,
		)
	}
	framed := component.NewFrame(bg)
	framed.FrameCharSet, _ = e.promptFrameCharSets()
	framed.Attributes = e.config.FrameAttr

	stitched := &promptStitcher{
		cmd:       cmd,
		padded:    padded,
		framed:    framed,
		separator: e.commandPromptCfg.separator,
	}
	if !framed.Graphics.Empty() {
		stitched.graphics = e.promptFrameGraphics()
	}
	return browser.FuncFloating(
		browser.FuncHandler(stitched, onClose),
		framed.Dimensions,
	)
}

type promptStitcher struct {
	cmd    *command.Prompt
	padded *handler.Span
	framed *component.Frame
	// graphics is nil when the frame names no images.
	graphics  *tcomponent.FrameGraphics
	separator commandPromptSeparatorCharset
	width     int
	height    int
}

var _ tui.Handler = (*promptStitcher)(nil)

func (s *promptStitcher) Resize(width, height int) {
	s.width, s.height = width, height
	s.framed.Resize(width, height)
}

func (s *promptStitcher) Draw(w term.Writer) {
	s.framed.Draw(w)
	// Images that replace their runes go under the separator, which then
	// covers the side images on its row. Overlays go over it, as they go
	// over every rune.
	overlay := s.graphics != nil && s.framed.Graphics.Overlay
	if s.graphics != nil && !overlay {
		s.graphics.Draw(w, s.framed, s.width, s.height)
	}
	s.drawSeparator(w)
	if overlay {
		s.graphics.Draw(w, s.framed, s.width, s.height)
	}
}

func (s *promptStitcher) drawSeparator(w term.Writer) {
	if s.width < 3 || s.height < 3 {
		return
	}
	sepY, ok := s.cmd.SeparatorY()
	if !ok {
		return
	}
	y := sepY + 1
	if y <= 0 || y >= s.height-1 {
		return
	}
	w.SetCell(term.Coordinates{X: 0, Y: y},
		term.NewCell(s.separator.Left, 0, s.framed.Attributes))
	w.SetCell(term.Coordinates{X: 1, Y: y},
		term.NewCell(s.separator.HorizontalLeft, 0, s.framed.Attributes))
	w.SetCell(term.Coordinates{X: s.width - 2, Y: y},
		term.NewCell(s.separator.HorizontalRight, 0, s.framed.Attributes))
	w.SetCell(term.Coordinates{X: s.width - 1, Y: y},
		term.NewCell(s.separator.Right, 0, s.framed.Attributes))
}

func (s *promptStitcher) Handle(ev term.Event) (bool, bool) {
	if ev.Type == term.EventMouse {
		// The frame border occupies row 0 / W-1 and column 0 / H-1
		// of the outer window. Translate mouse coordinates into
		// the inner content space the padded handler expects, and
		// clamp into bounds for press/release on border cells.
		ev.MouseX = clampMouseAxis(ev.MouseX-1, s.width-2)
		ev.MouseY = clampMouseAxis(ev.MouseY-1, s.height-2)
	}
	return s.padded.Handle(ev)
}

func (s *promptStitcher) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	pos, style, show := s.padded.Cursor()
	pos.X++
	pos.Y++
	return pos, style, show
}

func (s *promptStitcher) Selection() (string, bool) {
	return s.padded.Selection()
}

func clampMouseAxis(v, max int) int {
	if v < 0 {
		return 0
	}
	if max < 0 {
		return 0
	}
	if v > max {
		return max
	}
	return v
}

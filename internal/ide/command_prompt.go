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
	"image"

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

// defaultCommandPromptSeparatorCharset returns the stitch glyphs used
// when no override is configured.
func defaultCommandPromptSeparatorCharset() tcomponent.SeparatorCharSet {
	return tcomponent.SeparatorCharSet{
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
	separator tcomponent.SeparatorCharSet
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
		inside:    e.promptInside,
	}
	if !framed.Graphics.Empty() || !stitched.separator.Graphics.Empty() {
		stitched.graphics = e.frameGraphics()
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
	// graphics is nil when neither the frame nor the separator names images.
	graphics  *tcomponent.FrameGraphics
	separator tcomponent.SeparatorCharSet
	// inside returns the colour inside the frame, which the side of the
	// frame's lines facing it takes, while the other side shows what is
	// under the prompt. It follows the theme while the prompt is open,
	// as a guitheme preview changes it.
	inside func() term.Color
	masks  tcomponent.FrameMasks
	width  int
	height int
}

var _ tui.Handler = (*promptStitcher)(nil)

func (s *promptStitcher) Resize(width, height int) {
	s.width, s.height = width, height
	s.framed.Resize(width, height)
}

func (s *promptStitcher) Draw(w term.Writer) {
	if s.graphics == nil {
		s.framed.Draw(w)
		s.drawSeparator(w, w)
		return
	}
	cells := &tcomponent.FrameUnderWriter{
		Writer: w, Graphics: s.framed.Graphics, Width: s.width, Height: s.height,
		Keep: s.junctions(),
	}
	images := &tcomponent.FrameInsideWriter{
		Writer: w, Area: image.Rect(0, 0, s.width, s.height), Inside: s.inside(), Masks: &s.masks,
	}
	s.framed.Draw(cells)
	s.graphics.DrawOver(images, s.framed, s.width, s.height)
	s.drawSeparator(cells, images)
}

// drawSeparator draws the separator's cells to cells and its images to
// images.
func (s *promptStitcher) drawSeparator(cells, images term.Writer) {
	y, ok := s.separatorRow()
	if !ok {
		return
	}
	cells.SetCell(term.Coordinates{X: 0, Y: y},
		term.NewCell(s.separator.Left, 0, s.framed.Attributes))
	cells.SetCell(term.Coordinates{X: 1, Y: y},
		term.NewCell(s.separator.HorizontalLeft, 0, s.framed.Attributes))
	cells.SetCell(term.Coordinates{X: s.width - 2, Y: y},
		term.NewCell(s.separator.HorizontalRight, 0, s.framed.Attributes))
	cells.SetCell(term.Coordinates{X: s.width - 1, Y: y},
		term.NewCell(s.separator.Right, 0, s.framed.Attributes))
	if s.graphics != nil {
		// The junctions' images go over the background under the
		// prompt, as the frame's do.
		over := *s.framed
		over.Attributes.Attrs |= term.AttrKeepBackground
		s.graphics.DrawSeparator(images, &over, s.separator, s.width, s.height, y)
	}
}

// separatorRow returns the row of the prompt the separator is drawn on,
// or false when it draws none.
func (s *promptStitcher) separatorRow() (int, bool) {
	if s.width < 3 || s.height < 3 {
		return 0, false
	}
	sepY, ok := s.cmd.SeparatorY()
	y := sepY + 1
	return y, ok && y > 0 && y < s.height-1
}

// junctions returns the cells of the separator's junctions that name an
// image, which goes over what is under the prompt as the frame's do,
// even where the frame's side names none.
func (s *promptStitcher) junctions() []term.Coordinates {
	y, ok := s.separatorRow()
	if !ok {
		return nil
	}
	var keep []term.Coordinates
	if s.separator.Graphics.Left != "" {
		keep = append(keep, term.Coordinates{X: 0, Y: y})
	}
	if s.separator.Graphics.Right != "" {
		keep = append(keep, term.Coordinates{X: s.width - 1, Y: y})
	}
	return keep
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

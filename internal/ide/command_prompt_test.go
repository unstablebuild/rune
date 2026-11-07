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
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/imageuri/imageuritest"
	"unstable.build/rune/internal/handler/handlertest"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/texttest"
)

func TestPromptStitcherPointerLeaves(t *testing.T) {
	t.Parallel()
	b := newExForTesting(t, texttest.NopEditor(), text.WithCommandKey(testCommandKey))
	t.Cleanup(func() { _ = b.Close() })
	unlock := b.lock()
	defer unlock()
	b.ex.openCommandPrompt()
	require.NotNil(t, b.ex.cmd)
	b.ex.config.WindowManagerConfig.Frame = true
	h := newCommandPromptHandler(b.ex.cmd, b.ex, func() error { return nil })
	handlertest.RunPointerLeaves(t, h, 60, 12)
	handlertest.RunPointerLeaves(t, h, 2, 2)
}

func TestPromptStitcherDrawsFrameGraphics(t *testing.T) {
	path := filepath.Join(t.TempDir(), "frame.png")
	require.NoError(t, os.WriteFile(path, imageuritest.SamplePNG, 0o600))
	uri := (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
	const width, height = 40, 12
	every := component.FrameCharSetGraphics{
		TopLeft: uri, TopRight: uri, BottomLeft: uri, BottomRight: uri,
		HorizontalTop: uri, HorizontalBottom: uri,
		VerticalLeft: uri, VerticalRight: uri,
	}
	overlay := every
	overlay.Overlay = true

	tests := []struct {
		name     string
		graphics component.FrameCharSetGraphics
		// images reports whether every border cell carries an image.
		images bool
	}{
		{
			name:     "the separator preserves every side-frame image",
			graphics: every,
			images:   true,
		},
		{
			name:     "overlay draws over the separator's ends too",
			graphics: overlay,
			images:   true,
		},
		{name: "no graphics draws no images"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newExForTesting(t, texttest.NopEditor(),
				text.WithCommandKey(testCommandKey),
				text.WithCommandOverlayConfig(text.CommandOverlayConfig{ShowManual: true}))
			t.Cleanup(func() { _ = b.Close() })
			unlock := b.lock()
			defer unlock()
			b.ex.openCommandPrompt()
			require.NotNil(t, b.ex.cmd)
			b.ex.config.WindowManagerConfig.Frame = true
			b.ex.config.FrameCharSet.Graphics = tt.graphics
			h := newCommandPromptHandler(b.ex.cmd, b.ex, func() error { return nil })
			assert.Equal(t, tt.images, b.ex.graphics != nil,
				"the frame images partition is opened only for a frame with images")
			h.Resize(width, height)
			for _, ch := range "edit" {
				h.Handle(term.Event{Type: term.EventKey, Ch: ch})
			}
			sepY, ok := b.ex.cmd.SeparatorY()
			require.True(t, ok, "the prompt shows a manual")
			sepY++

			// The test event stream drops the interrupt that reports the
			// image loaded, so wait for the image itself: a draw that the
			// load lands in shows it on only the cells drawn after it.
			if tt.images {
				img := b.ex.graphics.Image(uri)
				img.Resize(1, 1)
				require.Eventually(t, func() bool {
					w := newCoveringImageWriter(1, 1)
					img.Draw(w)
					return len(w.images) == 1
				}, 5*time.Second, 10*time.Millisecond)
			}
			w := newCoveringImageWriter(width, height)
			h.Draw(w)

			want := map[term.Coordinates]bool{}
			if tt.images {
				for x := range width {
					want[term.Coordinates{X: x}] = true
					want[term.Coordinates{X: x, Y: height - 1}] = true
				}
				for y := 1; y < height-1; y++ {
					want[term.Coordinates{Y: y}] = true
					want[term.Coordinates{X: width - 1, Y: y}] = true
				}
			}
			assert.Equal(t, want, w.images)
			sep := b.ex.commandPromptCfg.separator
			for x, ch := range map[int]rune{
				0: sep.Left, 1: sep.HorizontalLeft,
				width - 2: sep.HorizontalRight, width - 1: sep.Right,
			} {
				if tt.images && !tt.graphics.Overlay && (x == 0 || x == width-1) {
					ch = ' '
				}
				assert.Equal(t, string(ch), string(w.Cells()[sepY*width+x].Ch),
					"separator cell %d", x)
			}
		})
	}
}

func TestPromptStitcherDrawsSeparatorGraphics(t *testing.T) {
	path := filepath.Join(t.TempDir(), "separator.png")
	require.NoError(t, os.WriteFile(path, imageuritest.SamplePNG, 0o600))
	uri := (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
	const width = 40
	for _, tt := range []struct {
		name, config         string
		noFrame, hideManual  bool
		height               int
		separator, bar, ends bool
	}{
		{
			name: "inherits the window frame bar",
			config: `{"browser": {"window_manager": {"frame_charset": {
				"horizontalbottom": "=", "graphics": {"horizontalbottom": %[1]q},
			}}}}`,
			separator: true, bar: true,
		},
		{
			name: "inherits the command frame bar and overlay mode",
			config: `{"command": {"frame_charset": {
				"horizontalbottom": "~", "graphics": {"horizontalbottom": %[1]q, "mode": "overlay"},
			}}}`,
			separator: true, bar: true,
		},
		{
			name: "separator-only sprites initialize the image cache",
			config: `{"command": {"separator_charset": {"graphics": {
				"left": %[1]q, "horizontal": %[1]q, "right": %[1]q,
			}}}}`,
			separator: true, bar: true, ends: true,
		},
		{
			name: "custom junctions reuse the frame bar",
			config: `{"command": {
				"frame_charset": {"graphics": {"horizontalbottom": %[1]q}},
				"separator_charset": {"graphics": {"left": %[1]q, "right": %[1]q}},
			}}`,
			separator: true, bar: true, ends: true,
		},
		{
			name: "cleared bar leaves the glyph rule",
			config: `{"command": {
				"frame_charset": {"horizontalbottom": "=", "graphics": {"horizontalbottom": %[1]q}},
				"separator_charset": {"graphics": {"horizontal": ""}},
			}}`,
			separator: true,
		},
		{
			name: "hidden manual draws no separator sprites",
			config: `{"command": {"separator_charset": {"graphics": {
				"left": %[1]q, "horizontal": %[1]q, "right": %[1]q,
			}}}}`,
			hideManual: true,
		},
		{
			name: "disabled frame draws no separator sprites",
			config: `{"command": {"separator_charset": {"graphics": {
				"left": %[1]q, "horizontal": %[1]q, "right": %[1]q,
			}}}}`,
			noFrame: true, separator: true,
		},
		{
			name: "short prompt draws no separator sprites",
			config: `{"command": {"separator_charset": {"graphics": {
				"left": %[1]q, "horizontal": %[1]q, "right": %[1]q,
			}}}}`,
			height: 3,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := newAnimConfig(t, fmt.Sprintf("config = "+tt.config, uri))
			b := newExForTesting(t, texttest.NopEditor(),
				text.WithCommandKey(testCommandKey),
				text.WithCommandOverlayConfig(text.CommandOverlayConfig{ShowManual: !tt.hideManual}))
			t.Cleanup(func() { _ = b.Close() })
			unlock := b.lock()
			defer unlock()
			b.ex.config.WindowManagerConfig.Frame = !tt.noFrame
			b.ex.config.FrameCharSet = cfg.windowFrameCharset()
			b.ex.commandPromptCfg = cfg.commandPromptCfg()
			require.Empty(t, cfg.errors)
			b.ex.openCommandPrompt()
			require.NotNil(t, b.ex.cmd)
			h := newCommandPromptHandler(b.ex.cmd, b.ex, func() error { return nil })
			height := tt.height
			if height == 0 {
				height = 12
			}
			h.Resize(width, height)
			for _, ch := range "edit" {
				h.Handle(term.Event{Type: term.EventKey, Ch: ch})
			}
			sepY, ok := b.ex.cmd.SeparatorY()
			require.Equal(t, tt.separator, ok)
			sepY++

			if tt.noFrame {
				assert.Nil(t, b.ex.graphics)
			} else {
				require.NotNil(t, b.ex.graphics)
				// Load even hidden sprites so visibility checks cannot pass
				// merely because a loader has not finished yet.
				img := b.ex.graphics.Image(uri)
				img.Resize(1, 1)
				require.Eventually(t, func() bool {
					w := newCoveringImageWriter(1, 1)
					img.Draw(w)
					return len(w.images) == 1
				}, 5*time.Second, 10*time.Millisecond)
			}
			w := newCoveringImageWriter(width, height)
			h.Draw(w)
			want := map[term.Coordinates]bool{}
			frame, _ := b.ex.promptFrameCharSets()
			if !tt.noFrame && frame.Graphics.HorizontalBottom != "" {
				for x := 1; x < width-1; x++ {
					want[term.Coordinates{X: x, Y: height - 1}] = true
				}
			}
			if tt.bar {
				for x := 1; x < width-1; x++ {
					want[term.Coordinates{X: x, Y: sepY}] = true
				}
			}
			if tt.ends {
				want[term.Coordinates{Y: sepY}] = true
				want[term.Coordinates{X: width - 1, Y: sepY}] = true
			}
			assert.Equal(t, want, w.images)

			if tt.separator && !tt.noFrame {
				terminal := term.NewStringWriter(width, height)
				h.Draw(terminal)
				sep := b.ex.commandPromptCfg.separator
				for x := range width {
					ch := frame.HorizontalBottom
					switch x {
					case 0:
						ch = sep.Left
					case 1:
						ch = sep.HorizontalLeft
					case width - 2:
						ch = sep.HorizontalRight
					case width - 1:
						ch = sep.Right
					}
					assert.Equal(t, ch, terminal.Cells()[sepY*width+x].Ch, "terminal cell %d", x)
					if want[term.Coordinates{X: x, Y: sepY}] && !sep.Graphics.Overlay {
						ch = ' '
					}
					assert.Equal(t, ch, w.Cells()[sepY*width+x].Ch, "GUI cell %d", x)
				}
			}
		})
	}
}

// coveringImageWriter records where images are drawn, and drops those
// that a later cell covers.
type coveringImageWriter struct {
	*term.StringWriter
	images map[term.Coordinates]bool
}

func newCoveringImageWriter(width, height int) *coveringImageWriter {
	return &coveringImageWriter{
		StringWriter: term.NewStringWriter(width, height),
		images:       map[term.Coordinates]bool{},
	}
}

func (w *coveringImageWriter) SetCell(pos term.Coordinates, c term.Cell) {
	delete(w.images, pos)
	w.StringWriter.SetCell(pos, c)
}

func (w *coveringImageWriter) DrawImage(img term.Image) bool {
	w.images[img.Pos] = true
	return true
}

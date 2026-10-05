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

package template

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"text/template/parse"
	"time"

	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/imageuri"
)

// ImageField is the layout field that anchors an image. The image
// directives only apply to it.
const ImageField = "Image"

// The values of the image directives an ImageField element leaves out.
const (
	// DefaultImageAlt is a cross from the Nerd Fonts Font Awesome set.
	DefaultImageAlt   = "\uf00d"
	DefaultImageWidth = 2
	DefaultImageTTL   = 24 * time.Hour
)

// ZIndexBackground is the lowest ZIndex of an image drawn over the
// layout's backgrounds, as in the kitty graphics protocol: an image with
// a lower ZIndex is drawn under them.
const ZIndexBackground = math.MinInt32 / 2

// Image is an image anchored in a layout by an ImageField element. The
// element takes up Reserve cells in the layout, and its anchor is the
// cell right after the element's leading text: the first reserved cell,
// or the next element's first cell when nothing is reserved. The image
// is a Width x Height rectangle of cells centered on the anchor cell,
// then moved XOffset cells right and YOffset rows down, and then
// XOffsetPixels and YOffsetPixels device pixels further, so that it can
// be aligned with the text around it to the pixel; negative offsets move
// it left and up. A side with an even number of cells cannot be centered
// on a cell, so it is placed half a cell left of, or above, the anchor's
// center. Whatever falls outside the layout's surroundings is cut off,
// unless Overflow is set: then the image extends over whatever is drawn
// around them, up to the edges of the screen. Reserve defaults to the
// width when the width is a number of cells, and to 0 when it is a
// percentage.
//
// ZIndex stacks the image with the layout's text and the other images,
// as in the kitty graphics protocol: an image with a ZIndex of 0 or more
// is drawn over the text, one with a negative ZIndex under the text but
// over the backgrounds, and one under ZIndexBackground under the
// backgrounds as well, where only cells without a background color show
// it. Where images overlap, the one with the highest ZIndex is on top,
// and the one later in the layout when they tie. The alt text is drawn
// over the text whatever the ZIndex, because it stands in for the image.
// It is moved by XOffset and YOffset but not by the pixel offsets, which
// text cannot be moved by.
type Image struct {
	// URI is the file, http or https URI the image is read from.
	URI string
	// Width is how wide the image is drawn.
	Width Length
	// Height is how many rows the image covers.
	Height int
	// Reserve is how many cells the element takes up in the layout.
	Reserve int
	// XOffset moves the image right, or left when negative.
	XOffset Length
	// YOffset moves the image down, or up when negative, in rows.
	YOffset int
	// XOffsetPixels and YOffsetPixels move the image further right and
	// down, or left and up when negative, by device pixels.
	XOffsetPixels, YOffsetPixels int
	// Fit selects how the image is scaled into its cells.
	Fit term.ImageFit
	// ZIndex stacks the image with the text and the other images.
	ZIndex int32
	// Overflow lets the image extend past the layout's surroundings.
	Overflow bool
	// Alt is drawn centered over the image's cells when the image cannot
	// be shown. An empty Alt draws nothing.
	Alt string
	// TTL is how long downloaded bytes are reused before they are fetched
	// again.
	TTL time.Duration
}

// Layer returns the layer ZIndex draws the image on.
func (img Image) Layer() term.ImageLayer {
	switch {
	case img.ZIndex >= 0:
		return term.ImageLayerAboveText
	case img.ZIndex >= ZIndexBackground:
		return term.ImageLayerBelowText
	default:
		return term.ImageLayerBelowBackground
	}
}

// Length is a number of cells, or a percentage of the layout's width.
type Length struct {
	N       int
	Percent bool
}

// Cells resolves l in a layout total cells wide. A percentage is
// truncated toward zero.
func (l Length) Cells(total int) int {
	if l.Percent {
		return total * l.N / 100
	}
	return l.N
}

// imageDirectives are the pipeline functions of an ImageField element.
var imageDirectives = map[string]struct{}{
	"src": {}, "width": {}, "height": {}, "reserve": {},
	"x_offset": {}, "y_offset": {}, "x_pixel_offset": {}, "y_pixel_offset": {},
	"fit": {}, "z_index": {}, "overflow": {}, "alt": {}, "ttl": {},
}

type imageBuilder struct {
	img        Image
	reserveSet bool
}

func newImageBuilder() *imageBuilder {
	return &imageBuilder{img: Image{
		Width:  Length{N: DefaultImageWidth},
		Height: 1,
		Fit:    term.ImageFitContain,
		Alt:    DefaultImageAlt,
		TTL:    DefaultImageTTL,
	}}
}

func (b *imageBuilder) apply(name string, args []parse.Node) (err error) {
	switch name {
	case "src":
		var uri string
		if uri, err = stringArg(name, args); err != nil {
			return err
		}
		if err := imageuri.ValidateURI(uri); err != nil {
			return fmt.Errorf("src: %w", err)
		}
		b.img.URI = uri
	case "width":
		b.img.Width, err = lengthArg(name, args, 1, 1)
	case "height":
		b.img.Height, err = intArg(name, args, 1)
	case "reserve":
		b.img.Reserve, err = intArg(name, args, 0)
		b.reserveSet = true
	case "x_offset":
		b.img.XOffset, err = lengthArg(name, args, math.MinInt, -100)
	case "y_offset":
		b.img.YOffset, err = intArg(name, args, math.MinInt)
	case "x_pixel_offset":
		b.img.XOffsetPixels, err = intArg(name, args, math.MinInt)
	case "y_pixel_offset":
		b.img.YOffsetPixels, err = intArg(name, args, math.MinInt)
	case "fit":
		b.img.Fit, err = enumArg(name, args, map[string]term.ImageFit{
			"contain": term.ImageFitContain,
			"fill":    term.ImageFitFill,
		})
	case "z_index":
		var z int
		if z, err = intArg(name, args, math.MinInt); err != nil {
			return err
		}
		if z < math.MinInt32 || z > math.MaxInt32 {
			return fmt.Errorf("z_index must be from %d to %d, got %d",
				math.MinInt32, math.MaxInt32, z)
		}
		b.img.ZIndex = int32(z)
	case "overflow":
		if len(args) != 0 {
			return fmt.Errorf("%s takes no arguments", name)
		}
		b.img.Overflow = true
	case "alt":
		b.img.Alt, err = stringArg(name, args)
	case "ttl":
		var s string
		if s, err = stringArg(name, args); err != nil {
			return err
		}
		b.img.TTL, err = time.ParseDuration(s)
		if err == nil && b.img.TTL < 0 {
			err = fmt.Errorf("ttl must not be negative, got %s", s)
		}
	}
	return err
}

func (b *imageBuilder) build() (Image, error) {
	if b.img.URI == "" {
		return Image{}, fmt.Errorf(".%s requires src", ImageField)
	}
	if !b.reserveSet && !b.img.Width.Percent {
		b.img.Reserve = b.img.Width.N
	}
	return b.img, nil
}

func stringArg(name string, args []parse.Node) (string, error) {
	if len(args) != 1 {
		return "", fmt.Errorf("%s takes one argument", name)
	}
	s, ok := args[0].(*parse.StringNode)
	if !ok {
		return "", fmt.Errorf("%s argument must be a string", name)
	}
	return s.Text, nil
}

func intArg(name string, args []parse.Node, least int) (int, error) {
	if len(args) != 1 {
		return 0, fmt.Errorf("%s takes one argument", name)
	}
	n, ok := args[0].(*parse.NumberNode)
	if !ok || !n.IsInt {
		return 0, fmt.Errorf("%s argument must be an integer", name)
	}
	if n.Int64 < int64(least) {
		return 0, fmt.Errorf("%s must be at least %d, got %d", name, least, n.Int64)
	}
	return int(n.Int64), nil
}

// lengthArg parses a number of cells of at least leastCells, or a
// percentage from leastPercent to 100.
func lengthArg(name string, args []parse.Node, leastCells, leastPercent int) (Length, error) {
	if len(args) == 1 {
		if s, ok := args[0].(*parse.StringNode); ok {
			digits, found := strings.CutSuffix(s.Text, "%")
			pct, err := strconv.Atoi(digits)
			if !found || err != nil || pct < leastPercent || pct > 100 {
				return Length{}, fmt.Errorf(
					"%s must be a number of cells or a percentage from %d%% to 100%%, got %q",
					name, leastPercent, s.Text)
			}
			return Length{N: pct, Percent: true}, nil
		}
	}
	n, err := intArg(name, args, leastCells)
	return Length{N: n}, err
}

func enumArg[T any](name string, args []parse.Node, values map[string]T) (T, error) {
	var zero T
	s, err := stringArg(name, args)
	if err != nil {
		return zero, err
	}
	v, ok := values[s]
	if !ok {
		names := make([]string, 0, len(values))
		for k := range values {
			names = append(names, strconv.Quote(k))
		}
		slices.Sort(names)
		return zero, fmt.Errorf("%s must be one of %s, got %q",
			name, strings.Join(names, ", "), s)
	}
	return v, nil
}

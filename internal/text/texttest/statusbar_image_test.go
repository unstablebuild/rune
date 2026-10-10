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

package texttest

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	sdkcomponent "github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/component/comptest"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"go.uber.org/goleak"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component"
	"unstable.build/rune/internal/component/asciiart"
	"unstable.build/rune/internal/component/imageuri/imageuritest"
	"unstable.build/rune/internal/text"
)

func TestStatusBarImage(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	dir := t.TempDir()
	logo := filepath.Join(dir, "logo.png")
	require.NoError(t, os.WriteFile(logo, imageuritest.SamplePNG, 0o600))
	fileURI := func(path string) string {
		return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
	}

	tests := []struct {
		name string
		// layout is formatted with the URI of the sample picture.
		layout string
		// images reports whether the writer can draw images, which it
		// encodes as ASCII art.
		images bool
		hide   bool
		// src overrides the URI layout is formatted with.
		src      string
		expected string
	}{
		{
			name:   "is centered on its first reserved cell",
			layout: `{{ .Status }}{{ .ShiftRight }}{{ .Image | src "%s" | width 4 }}]`,
			images: true,
			expected: `
first line              
second line             
third line              
NORMAL            @@   ]`,
		},
		{
			name:   "is moved over its reserved cells by half its width",
			layout: `{{ .Status }}{{ .ShiftRight }}{{ .Image | src "%s" | width 4 | x_offset 2 }}]`,
			images: true,
			expected: `
first line              
second line             
third line              
NORMAL              @@ ]`,
		},
		{
			name:   "is not moved by less than half a cell",
			layout: `{{ .Status }}{{ .ShiftRight }}{{ .Image | src "%s" | width 4 | x_pixel_offset 4 | y_pixel_offset -11 }}]`,
			images: true,
			expected: `
first line              
second line             
third line              
NORMAL            @@   ]`,
		},
		{
			name: "is moved by its pixel offsets after its cell offsets",
			layout: `{{ .Status }}{{ .ShiftRight }}{{ .Image | src "%s" | width 4 | x_offset 2 | ` +
				`x_pixel_offset 10 }}]`,
			images: true,
			expected: `
first line              
second line             
third line              
NORMAL               @@]`,
		},
		{
			name: "is drawn under the text of the whole bar",
			layout: `{{ .Image | src "%s" | width "100%%" | x_offset "50%%" | fit "fill" | ` +
				`z_index -1 }}{{ .Status }}`,
			images: true,
			expected: `
first line              
second line             
third line              
NORMAL@@W:@@@0:@@@@@@@@@`,
		},
		{
			name: "is centered on the bar and cut off below it",
			layout: `{{ .Status }}  [{{ .Image | src "%s" | width 6 | height 3 | ` +
				`reserve 2 | fit "fill" }}]`,
			images: true,
			expected: `
first line              
second line             
third @bc#;@            
NORMAL@@b;@@            `,
		},
		{
			name: "is moved right and up over the editor",
			layout: `{{ .Status }}  [{{ .Image | src "%s" | width 6 | height 3 | ` +
				`reserve 2 | x_offset 3 | y_offset -1 | fit "fill" }}]`,
			images: true,
			expected: `
first line              
second li@bc#;@         
third lin@@b;@@         
NORMAL  [::@+##         `,
		},
		{
			name: "is moved left and up with negative offsets",
			layout: `{{ .Status }}{{ .ShiftRight }}{{ .Image | src "%s" | width 4 | ` +
				`reserve 0 | x_offset -3 | y_offset -2 | fit "fill" }}]`,
			images: true,
			expected: `
first line              
second line       @c2@  
third line              
NORMAL                 ]`,
		},
		{
			name: "is placed half a cell up and left when its size is even",
			layout: `{{ .Status }}  [{{ .Image | src "%s" | width 2 | height 2 | ` +
				`reserve 0 | fit "fill" }}]`,
			images: true,
			expected: `
first line              
second line             
third lib#              
NORMAL  @@              `,
		},
		{
			name:   "is cut off at the edge of the bar",
			layout: `{{ .Status }}{{ .ShiftRight }}{{ .Image | src "%s" | width 8 | reserve 2 | fit "fill" }}`,
			images: true,
			expected: `
first line              
second line             
third line              
NORMAL            @@@@0@`,
		},
		{
			name:   "draws its alt text when the writer cannot draw images",
			layout: `{{ .Status }}{{ .ShiftRight }}{{ .Image | src "%s" | width 4 }}]`,
			expected: `
first line              
second line             
third line              
NORMAL            ` + "\uf00d" + `    ]`,
		},
		{
			name: "draws its alt text moved by its cell offsets but not by its pixel offsets",
			layout: `{{ .Status }}{{ .ShiftRight }}{{ .Image | src "%s" | width 4 | x_offset -2 | ` +
				`x_pixel_offset 30 | y_pixel_offset -46 }}]`,
			expected: `
first line              
second line             
third line              
NORMAL          ` + "\uf00d" + `      ]`,
		},
		{
			name:   "draws nothing for an empty alt text",
			layout: `{{ .Status }}{{ .ShiftRight }}{{ .Image | src "%s" | width 4 | alt "" }}]`,
			expected: `
first line              
second line             
third line              
NORMAL                 ]`,
		},
		{
			name:   "draws its alt text when the image is missing",
			layout: `{{ .Status }}{{ .ShiftRight }}{{ .Image | src "%s" | width 4 | alt "?" }}]`,
			images: true,
			src:    fileURI(filepath.Join(dir, "missing.png")),
			expected: `
first line              
second line             
third line              
NORMAL            ?    ]`,
		},
		{
			name:   "cuts off its alt text with the image",
			layout: `{{ .Image | src "%s" | width 3 | reserve 0 | alt "abc" }}{{ .Status }}`,
			images: true,
			src:    fileURI(filepath.Join(dir, "missing.png")),
			expected: `
first line              
second line             
third line              
bcRMAL                  `,
		},
		{
			name:   "is hidden with the bar",
			layout: `{{ .Status }}{{ .ShiftRight }}{{ .Image | src "%s" | width 4 }}]`,
			images: true,
			hide:   true,
			expected: `
first line              
second line             
third line              
                        `,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := tt.src
			if src == "" {
				src = fileURI(logo)
			}
			f := newImageBarFixture(t, fmt.Sprintf(tt.layout, src),
				storagestub.NewInMemoryService(), tt.images, term.ColorDefault)
			if tt.hide {
				f.bar.ShowCommandBar(false)
			}
			f.settle(t)
			comptest.TestComponent(t, f.bar, f.w, []comptest.TestCase{{Expected: tt.expected}})
		})
	}
}

func TestStatusBarImageZIndex(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	dir := t.TempDir()
	// half is the second shade on its left half and transparent on its
	// right half.
	half := image.NewNRGBA(image.Rect(0, 0, 8, 2))
	draw.Draw(half, image.Rect(0, 0, 4, 2), image.NewUniform(color.Gray{Y: 200}),
		image.Point{}, draw.Src)
	files := map[string][]byte{
		"a.png":      encodePNG(t, solid(4, 4, 128)),
		"b.png":      encodePNG(t, solid(4, 4, 200)),
		"c.png":      encodePNG(t, solid(4, 4, 80)),
		"half.png":   encodePNG(t, half),
		"clear.png":  encodePNG(t, image.NewNRGBA(image.Rect(0, 0, 4, 4))),
		"tiny.png":   encodePNG(t, solid(1, 1, 128)),
		"huge.png":   encodePNG(t, solid(2048, 512, 128)),
		"thin.png":   encodePNG(t, solid(1000, 1, 128)),
		"empty.png":  {},
		"page.png":   []byte("<html>not an image</html>"),
		"sample.png": imageuritest.SamplePNG,
	}
	var pairs []string
	for name, data := range files {
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, data, 0o600))
		pairs = append(pairs, "{"+strings.TrimSuffix(name, ".png")+"}",
			(&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String())
	}
	pairs = append(pairs, "{missing}", (&url.URL{
		Scheme: "file", Path: filepath.ToSlash(filepath.Join(dir, "missing.png")),
	}).String())
	uris := strings.NewReplacer(pairs...)

	tests := []struct {
		name string
		// layout refers to the images by name, as in {a}.
		layout string
		// noImages draws with a writer that cannot draw images.
		noImages bool
		// bg is the bar's background color.
		bg       term.Color
		hide     bool
		expected string
	}{
		// Stacking with the bar's text.
		{
			name:     "is drawn over the text by default",
			layout:   `{{ .Image | src "{a}" | width 6 | reserve 0 | x_offset 3 | fit "fill" }}{{ .Status }}`,
			expected: onBar("aaaaaa"),
		},
		{
			name:     "is drawn over the text with z_index 0",
			layout:   `{{ .Image | src "{a}" | width 6 | reserve 0 | x_offset 5 | fit "fill" | z_index 0 }}{{ .Status }}`,
			expected: onBar("NOaaaaaa"),
		},
		{
			name:     "is drawn over the text with a positive z_index",
			layout:   `{{ .Image | src "{a}" | width 4 | reserve 0 | x_offset 4 | fit "fill" | z_index 9 }}{{ .Status }}`,
			expected: onBar("NOaaaa"),
		},
		{
			name:     "is drawn under the text with a negative z_index",
			layout:   `{{ .Image | src "{a}" | width 10 | reserve 0 | x_offset 5 | fit "fill" | z_index -1 }}{{ .Status }}`,
			expected: onBar("NORMALaaaa"),
		},
		{
			name: "is drawn over the background at the lowest z_index above it",
			layout: `{{ .Image | src "{a}" | width 10 | reserve 0 | x_offset 5 | fit "fill" | ` +
				`z_index -1073741824 }}{{ .Status }}`,
			bg:       term.ColorNavy,
			expected: onBar("NORMALaaaa"),
		},
		{
			name: "is drawn under the background below the lowest z_index above it",
			layout: `{{ .Image | src "{a}" | width 10 | height 3 | reserve 0 | x_offset 5 | ` +
				`y_offset -1 | fit "fill" | z_index -1073741825 }}{{ .Status }}`,
			bg:       term.ColorNavy,
			expected: frame("first line", "secondaline", "thirdaline", "NORMAL"),
		},
		{
			name: "shows under the background where the bar has no background color",
			layout: `{{ .Image | src "{a}" | width 10 | reserve 0 | x_offset 5 | fit "fill" | ` +
				`z_index -1073741825 }}{{ .Status }}`,
			expected: onBar("NORMALaaaa"),
		},
		{
			name: "shows under the background at the lowest z_index",
			layout: `{{ .Image | src "{a}" | width "100%" | reserve 0 | x_offset "50%" | fit "fill" | ` +
				`z_index -2147483648 }}{{ .Status }}`,
			expected: onBar("NORMALaaaaaaaaaaaaaaaaaa"),
		},
		{
			name: "is drawn over the text at the highest z_index",
			layout: `{{ .Image | src "{a}" | width "100%" | reserve 0 | x_offset "50%" | fit "fill" | ` +
				`z_index 2147483647 }}{{ .Status }}`,
			expected: onBar("aaaaaaaaaaaaaaaaaaaaaaaa"),
		},

		// Stacking with other images.
		{
			name: "the higher z_index is on top whatever the layout order",
			layout: `{{ .Image | src "{b}" | width 6 | reserve 0 | x_offset 3 | fit "fill" | z_index 2 }}` +
				`{{ .Image | src "{a}" | width 6 | reserve 0 | x_offset 6 | fit "fill" | z_index 1 }}{{ .Status }}`,
			expected: onBar("666666aaa"),
		},
		{
			name: "the later image in the layout is on top when zindexes tie",
			layout: `{{ .Image | src "{b}" | width 6 | reserve 0 | x_offset 3 | fit "fill" | z_index 1 }}` +
				`{{ .Image | src "{a}" | width 6 | reserve 0 | x_offset 6 | fit "fill" | z_index 1 }}{{ .Status }}`,
			expected: onBar("666aaaaaa"),
		},
		{
			name: "images are stacked by z_index and then by layout",
			layout: `{{ .Image | src "{c}" | width 6 | reserve 0 | x_offset 3 | fit "fill" | z_index 3 }}` +
				`{{ .Image | src "{b}" | width 10 | reserve 0 | x_offset 7 | fit "fill" | z_index 1 }}` +
				`{{ .Image | src "{a}" | width 6 | reserve 0 | x_offset 7 | fit "fill" | z_index 2 }}{{ .Status }}`,
			expected: onBar("======aaaa66"),
		},
		{
			name: "an image over the text stays over one under it whatever the layout order",
			layout: `{{ .Image | src "{b}" | width 4 | reserve 0 | x_offset 4 | fit "fill" }}` +
				`{{ .Image | src "{a}" | width 10 | reserve 0 | x_offset 5 | fit "fill" | z_index -1 }}{{ .Status }}`,
			expected: onBar("NO6666aaaa"),
		},
		{
			name: "images under the text are stacked by z_index",
			layout: `{{ .Image | src "{a}" | width 10 | reserve 0 | x_offset 5 | fit "fill" | z_index -1 }}` +
				`{{ .Image | src "{b}" | width 12 | reserve 0 | x_offset 6 | fit "fill" | z_index -2 }}{{ .Status }}`,
			expected: onBar("NORMALaaaa66"),
		},
		{
			name: "images under the background are stacked by z_index",
			layout: `{{ .Image | src "{b}" | width 8 | reserve 0 | x_offset 4 | fit "fill" | z_index -1073741825 }}` +
				`{{ .Image | src "{c}" | width 12 | reserve 0 | x_offset 6 | fit "fill" | z_index -2147483648 }}` +
				`{{ .Status }}`,
			expected: onBar("NORMAL66===="),
		},
		{
			name: "an image under the text covers one under the background",
			layout: `{{ .Image | src "{b}" | width 8 | reserve 0 | x_offset 4 | fit "fill" | z_index -1 }}` +
				`{{ .Image | src "{c}" | width 12 | reserve 0 | x_offset 6 | fit "fill" | z_index -1073741825 }}` +
				`{{ .Status }}`,
			expected: onBar("NORMAL66===="),
		},
		{
			name: "the transparent pixels of the top image show the image under it",
			layout: `{{ .Image | src "{a}" | width 12 | reserve 0 | x_offset 6 | fit "fill" }}` +
				`{{ .Image | src "{half}" | width 8 | reserve 0 | x_offset 4 | fit "fill" | z_index 1 }}{{ .Status }}`,
			expected: onBar("6666aaaaaaaa"),
		},
		{
			name: "a fully transparent image hides nothing",
			layout: `{{ .Image | src "{a}" | width 4 | reserve 0 | x_offset 10 | fit "fill" }}` +
				`{{ .Image | src "{clear}" | width "100%" | reserve 0 | x_offset "50%" | fit "fill" | z_index 5 }}` +
				`{{ .Status }}`,
			expected: onBar("NORMAL  aaaa"),
		},
		{
			name: "images that do not overlap are drawn whatever their z_index",
			layout: `{{ .Image | src "{a}" | width 4 | reserve 0 | x_offset 10 | fit "fill" | z_index -1 }}` +
				`{{ .Image | src "{b}" | width 4 | reserve 0 | x_offset 16 | fit "fill" | z_index 3 }}{{ .Status }}`,
			expected: onBar("NORMAL  aaaa  6666"),
		},

		// Stacking with the editor.
		{
			name: "an image under the text shows between the editor's words",
			layout: `{{ .Image | src "{a}" | width 14 | height 3 | reserve 0 | x_offset 13 | y_offset -1 | ` +
				`fit "fill" | z_index -1 }}{{ .Status }}`,
			expected: frame("first line", "secondalineaaaaaaaaa", "third lineaaaaaaaaaa",
				"NORMALaaaaaaaaaaaaaa"),
		},
		{
			name: "an image over the text covers the editor's words",
			layout: `{{ .Image | src "{a}" | width 14 | height 3 | reserve 0 | x_offset 13 | y_offset -1 | ` +
				`fit "fill" }}{{ .Status }}`,
			expected: frame("first line", "secondaaaaaaaaaaaaaa", "third aaaaaaaaaaaaaa",
				"NORMALaaaaaaaaaaaaaa"),
		},
		{
			name: "an image over the editor stacks with one on the bar",
			layout: `{{ .Image | src "{a}" | width 6 | height 3 | reserve 0 | x_offset 3 | y_offset -1 | ` +
				`fit "fill" | z_index 1 }}` +
				`{{ .Image | src "{b}" | width 6 | reserve 0 | x_offset 6 | fit "fill" | z_index 2 }}{{ .Status }}`,
			expected: frame("first line", "aaaaaa line", "aaaaaaline", "aaa666666"),
		},

		// Sizes and positions.
		{
			name:     "an image whose width rounds to no cells draws nothing",
			layout:   `{{ .Image | src "{a}" | width "1%" | reserve 0 | fit "fill" }}{{ .Status }}`,
			expected: onBar("NORMAL"),
		},
		{
			name:     "a one-pixel image fills its cells",
			layout:   `{{ .Image | src "{tiny}" | width 4 | reserve 0 | x_offset 2 | fit "fill" }}{{ .Status }}`,
			expected: onBar("aaaaAL"),
		},
		{
			name:     "a large image is scaled down to its cells",
			layout:   `{{ .Image | src "{huge}" | width 4 | reserve 0 | x_offset 2 | fit "fill" }}{{ .Status }}`,
			expected: onBar("aaaaAL"),
		},
		{
			// Cells are about 2.3 times taller than wide, so a 4:1 image
			// in 12x2 cells takes 9x1 of them.
			name:     "a large image keeps its aspect ratio",
			layout:   `{{ .Image | src "{huge}" | width 12 | height 2 | reserve 0 | x_offset 6 }}{{ .Status }}`,
			expected: frame("first line", "second line", "taaaaaaaaa", "NORMAL"),
		},
		{
			name:     "an image too thin to cover a cell keeps its aspect ratio",
			layout:   `{{ .Image | src "{thin}" | width 8 | height 2 | reserve 0 | x_offset 4 }}{{ .Status }}`,
			expected: onBar("NORMAL"),
		},
		{
			name: "a picture keeps its aspect ratio",
			layout: `{{ .Status }}{{ .ShiftRight }}{{ .Image | src "{sample}" | width 12 | height 3 | ` +
				`reserve 0 | x_offset -6 | y_offset -1 | z_index -1 }}`,
			expected: frame("first line", "second line   @b@cc@@", "third line    @@?@@@@",
				"NORMAL        ;+++@+:"),
		},
		{
			name: "an image larger than the editor and the bar is cut off at every edge",
			layout: `{{ .Image | src "{a}" | width 40 | height 9 | reserve 0 | x_offset 12 | ` +
				`fit "fill" }}{{ .Status }}`,
			expected: frame("aaaaaaaaaaaaaaaaaaaaaaaa", "aaaaaaaaaaaaaaaaaaaaaaaa",
				"aaaaaaaaaaaaaaaaaaaaaaaa", "aaaaaaaaaaaaaaaaaaaaaaaa"),
		},
		{
			name:     "an image left of the bar is cut off",
			layout:   `{{ .Image | src "{a}" | width 6 | reserve 0 | fit "fill" }}{{ .Status }}`,
			expected: onBar("aaaMAL"),
		},
		{
			name:     "an image below the bar is cut off",
			layout:   `{{ .Image | src "{a}" | width 4 | height 3 | reserve 0 | x_offset 2 | fit "fill" }}{{ .Status }}`,
			expected: frame("first line", "second line", "aaaad line", "aaaaAL"),
		},
		{
			name:     "an image right of the bar draws nothing",
			layout:   `{{ .Image | src "{a}" | width 4 | reserve 0 | x_offset 100 | fit "fill" }}{{ .Status }}`,
			expected: onBar("NORMAL"),
		},
		{
			name: "an image above the editor draws nothing",
			layout: `{{ .Image | src "{a}" | width 4 | height 2 | reserve 0 | x_offset 2 | y_offset -10 | ` +
				`fit "fill" }}{{ .Status }}`,
			expected: onBar("NORMAL"),
		},

		// Alt text.
		{
			name:     "an empty file draws its alt text",
			layout:   `{{ .Image | src "{empty}" | width 6 | reserve 0 | x_offset 3 | alt "?" }}{{ .Status }}`,
			expected: onBar("NO?MAL"),
		},
		{
			name:     "a file that is not an image draws its alt text",
			layout:   `{{ .Image | src "{page}" | width 6 | reserve 0 | x_offset 3 | alt "?" }}{{ .Status }}`,
			expected: onBar("NO?MAL"),
		},
		{
			name: "the alt text is drawn over the text whatever the z_index",
			layout: `{{ .Image | src "{missing}" | width 6 | reserve 0 | x_offset 3 | alt "?" | ` +
				`z_index -1 }}{{ .Status }}`,
			expected: onBar("NO?MAL"),
		},
		{
			name: "an image over the text covers the alt text of one under it",
			layout: `{{ .Image | src "{b}" | width 2 | reserve 0 | x_offset 3 | fit "fill" | z_index 1 }}` +
				`{{ .Image | src "{missing}" | width 6 | reserve 0 | x_offset 3 | alt "?" }}{{ .Status }}`,
			expected: onBar("NO66AL"),
		},
		{
			name: "an image under the text stays under the alt text of another",
			layout: `{{ .Image | src "{missing}" | width 6 | reserve 0 | x_offset 3 | alt "?" | z_index -5 }}` +
				`{{ .Image | src "{b}" | width 12 | reserve 0 | x_offset 6 | fit "fill" | z_index -1 }}{{ .Status }}`,
			expected: onBar("NO?MAL666666"),
		},
		{
			name: "the alt text of the higher z_index is on top",
			layout: `{{ .Image | src "{missing}" | width 6 | reserve 0 | x_offset 3 | alt "x" | z_index 2 }}` +
				`{{ .Image | src "{missing}" | width 6 | reserve 0 | x_offset 3 | alt "y" | z_index 1 }}{{ .Status }}`,
			expected: onBar("NOxMAL"),
		},
		{
			name: "a writer that cannot draw images stacks the alt texts by z_index",
			layout: `{{ .Image | src "{a}" | width 6 | reserve 0 | x_offset 3 | alt "2" | z_index 2 }}` +
				`{{ .Image | src "{b}" | width 6 | reserve 0 | x_offset 3 | alt "1" | z_index 1 }}{{ .Status }}`,
			noImages: true,
			expected: onBar("NO2MAL"),
		},
		{
			name: "a writer that cannot draw images draws nothing for an empty alt text",
			layout: `{{ .Image | src "{a}" | width 6 | reserve 0 | x_offset 3 | alt "" | ` +
				`z_index -1 }}{{ .Status }}`,
			noImages: true,
			expected: onBar("NORMAL"),
		},

		// Hidden bar.
		{
			name: "images of every z_index are hidden with the bar",
			layout: `{{ .Image | src "{a}" | width 6 | height 3 | reserve 0 | x_offset 3 | y_offset -1 | ` +
				`fit "fill" | z_index 1 }}` +
				`{{ .Image | src "{b}" | width 12 | height 3 | reserve 0 | x_offset 6 | y_offset -1 | ` +
				`fit "fill" | z_index -1 }}{{ .Status }}`,
			hide:     true,
			expected: frame("first line", "second line", "third line", ""),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newImageBarFixture(t, uris.Replace(tt.layout),
				storagestub.NewInMemoryService(), !tt.noImages, tt.bg)
			if tt.hide {
				f.bar.ShowCommandBar(false)
			}
			f.settle(t)
			comptest.TestComponent(t, f.bar, f.w, []comptest.TestCase{
				{Expected: tt.expected},
				{Expected: tt.expected},
			})
		})
	}
}

// frame is a frame of the image bar fixture with the given rows, padded
// to its width.
func frame(rows ...string) string {
	var sb strings.Builder
	for _, row := range rows {
		sb.WriteString("\n")
		sb.WriteString(row)
		sb.WriteString(strings.Repeat(" ", imageBarWidth-utf8.RuneCountInString(row)))
	}
	return sb.String()
}

// onBar is a frame of the image bar fixture with bar on its bar and the
// editor's text untouched.
func onBar(bar string) string {
	return frame("first line", "second line", "third line", bar)
}

func TestStatusBarImageOverflow(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	dir := t.TempDir()
	files := map[string][]byte{
		"a.png": encodePNG(t, solid(4, 4, 128)),
		"b.png": encodePNG(t, solid(4, 4, 200)),
	}
	var pairs []string
	for name, data := range files {
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, data, 0o600))
		pairs = append(pairs, "{"+strings.TrimSuffix(name, ".png")+"}",
			(&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String())
	}
	pairs = append(pairs, "{missing}", (&url.URL{
		Scheme: "file", Path: filepath.ToSlash(filepath.Join(dir, "missing.png")),
	}).String())
	uris := strings.NewReplacer(pairs...)

	dots := strings.Repeat(".", screenWidth)
	tests := []struct {
		name string
		// layout refers to the images by name, as in {a}.
		layout string
		// noImages draws with a writer that cannot draw images.
		noImages bool
		hide     bool
		expected string
	}{
		{
			name:   "an image is cut off below the bar without overflow",
			layout: `{{ .Image | src "{a}" | width 4 | height 3 | reserve 0 | x_offset 2 | fit "fill" }}{{ .Status }}`,
			expected: screen(dots, dots, inBar("first line"), inBar("second line"),
				inBar("aaaad line"), inBar("aaaaAL"), dots, dots),
		},
		{
			name: "an image overflows below the bar",
			layout: `{{ .Image | src "{a}" | width 4 | height 3 | reserve 0 | x_offset 2 | overflow | ` +
				`fit "fill" }}{{ .Status }}`,
			expected: screen(dots, dots, inBar("first line"), inBar("second line"),
				inBar("aaaad line"), inBar("aaaaAL"), "....aaaa"+strings.Repeat(".", 24), dots),
		},
		{
			name:   "an image overflows left of the bar",
			layout: `{{ .Image | src "{a}" | width 6 | reserve 0 | overflow | fit "fill" }}{{ .Status }}`,
			expected: screen(dots, dots, inBar("first line"), inBar("second line"),
				inBar("third line"), ".aaa"+padBar("aaaMAL")+"....", dots, dots),
		},
		{
			name: "an image overflows right of the bar",
			layout: `{{ .Status }}{{ .ShiftRight }}{{ .Image | src "{a}" | width 8 | reserve 2 | overflow | ` +
				`fit "fill" }}`,
			expected: screen(dots, dots, inBar("first line"), inBar("second line"),
				inBar("third line"), "....NORMAL            aaaaaaaa..", dots, dots),
		},
		{
			name: "an image overflows above the editor",
			layout: `{{ .Image | src "{a}" | width 4 | height 2 | reserve 0 | x_offset 2 | y_offset -4 | ` +
				`overflow | fit "fill" }}{{ .Status }}`,
			expected: screen("....aaaa"+strings.Repeat(".", 24), "....aaaa"+strings.Repeat(".", 24),
				inBar("first line"), inBar("second line"), inBar("third line"), inBar("NORMAL"),
				dots, dots),
		},
		{
			name: "an image is cut off at the edges of the screen",
			layout: `{{ .Image | src "{a}" | width 40 | height 12 | reserve 0 | x_offset 12 | overflow | ` +
				`fit "fill" }}{{ .Status }}`,
			expected: screen(slices.Repeat([]string{strings.Repeat("a", screenWidth)}, screenHeight)...),
		},
		{
			name: "an image under the text stays under the text past the bar",
			layout: `{{ .Status }}{{ .ShiftRight }}{{ .Image | src "{a}" | width 8 | reserve 2 | overflow | ` +
				`fit "fill" | z_index -1 }}`,
			expected: screen(dots, dots, inBar("first line"), inBar("second line"),
				inBar("third line"), inBar("NORMAL            aaaaaa"), dots, dots),
		},
		{
			name: "overflowing images are stacked by z_index",
			layout: `{{ .Image | src "{b}" | width 4 | height 3 | reserve 0 | x_offset 2 | overflow | ` +
				`fit "fill" | z_index 2 }}` +
				`{{ .Image | src "{a}" | width 4 | height 3 | reserve 0 | x_offset 4 | overflow | ` +
				`fit "fill" | z_index 1 }}{{ .Status }}`,
			expected: screen(dots, dots, inBar("first line"), inBar("second line"),
				inBar("6666aaline"), inBar("6666aa"), "....6666aa"+strings.Repeat(".", 22), dots),
		},
		{
			name:     "the alt text of a missing image is cut off at the bar",
			layout:   `{{ .Image | src "{missing}" | width 3 | reserve 0 | overflow | alt "abc" }}{{ .Status }}`,
			expected: screen(dots, dots, inBar("first line"), inBar("second line"), inBar("third line"), inBar("bcRMAL"), dots, dots),
		},
		{
			name:     "a writer that cannot draw images cuts off the alt text at the bar",
			layout:   `{{ .Image | src "{a}" | width 3 | reserve 0 | overflow | alt "abc" }}{{ .Status }}`,
			noImages: true,
			expected: screen(dots, dots, inBar("first line"), inBar("second line"), inBar("third line"), inBar("bcRMAL"), dots, dots),
		},
		{
			name: "an overflowing image is hidden with the bar",
			layout: `{{ .Image | src "{a}" | width 4 | height 3 | reserve 0 | x_offset 2 | overflow | ` +
				`fit "fill" }}{{ .Status }}`,
			hide: true,
			expected: screen(dots, dots, inBar("first line"), inBar("second line"),
				inBar("third line"), inBar(""), dots, dots),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newImageBarFixture(t, uris.Replace(tt.layout),
				storagestub.NewInMemoryService(), !tt.noImages, term.ColorDefault)
			f.surround(!tt.noImages)
			if tt.hide {
				f.bar.ShowCommandBar(false)
			}
			f.settle(t)
			comptest.TestComponent(t, f.view, f.w, []comptest.TestCase{
				{Expected: tt.expected},
				{Expected: tt.expected},
			})
		})
	}
}

const (
	screenWidth  = imageBarWidth + 8
	screenHeight = imageBarHeight + 4
)

func TestStatusBarImageMovesWithTheBar(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	path := filepath.Join(t.TempDir(), "a.png")
	require.NoError(t, os.WriteFile(path, encodePNG(t, solid(4, 4, 128)), 0o600))
	uri := (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()

	tests := []struct {
		name       string
		directives string
	}{
		{name: "on the bar"},
		{name: "over the bar and the rows around it", directives: `| height 3`},
		{name: "overflowing over the bar and the rows around it", directives: `| height 3 | overflow`},
		{name: "above the bar", directives: `| height 2 | y_offset -2`},
		{name: "overflowing below the bar", directives: `| height 2 | y_offset 2 | overflow`},
		{name: "under the text", directives: `| height 3 | z_index -1`},
		{name: "under the backgrounds", directives: `| height 3 | z_index -2147483648`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newImageBarFixture(t, fmt.Sprintf(
				`{{ .Status }}{{ .Image | src %q | width 4 | reserve 0 | x_offset 8 | fit "fill" %s }}`,
				uri, tt.directives), storagestub.NewInMemoryService(), true, term.ColorDefault)
			f.settle(t)

			rec := &placementRecorder{StringerWriter: f.w}
			f.bar.Draw(rec)
			require.NotEmpty(t, rec.images)
			for _, img := range rec.images {
				assert.True(t, img.VerticalRenderOffset, "%+v", img)
			}
		})
	}
}

// placementRecorder records the images drawn through it.
type placementRecorder struct {
	comptest.StringerWriter
	images []term.Image
}

func (w *placementRecorder) DrawImage(img term.Image) bool {
	w.images = append(w.images, img)
	return w.StringerWriter.DrawImage(img)
}

func TestStatusBarImageOffset(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	path := filepath.Join(t.TempDir(), "a.png")
	require.NoError(t, os.WriteFile(path, encodePNG(t, solid(4, 4, 128)), 0o600))
	uri := (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()

	// placed is what the bar decides about a placement.
	type placed struct {
		Pos           term.Coordinates
		Width, Height int
		Offset        image.Point
		Clip          image.Rectangle
		Overflow      bool
	}
	bar := image.Rect(0, 0, imageBarWidth, imageBarHeight)
	tests := []struct {
		name       string
		directives string
		expected   placed
	}{
		{
			name: "without offsets",
			expected: placed{
				Pos: term.Coordinates{X: 4, Y: 3}, Width: 4, Height: 1,
				Clip: image.Rect(4, 3, 8, 4),
			},
		},
		{
			name:       "moved by cells",
			directives: `| x_offset 2 | y_offset -1`,
			expected: placed{
				Pos: term.Coordinates{X: 6, Y: 2}, Width: 4, Height: 1,
				Clip: image.Rect(6, 2, 10, 3),
			},
		},
		{
			name:       "moved by pixels",
			directives: `| x_pixel_offset 13 | y_pixel_offset -7`,
			expected: placed{
				Pos: term.Coordinates{X: 4, Y: 3}, Width: 4, Height: 1,
				Offset: image.Pt(13, -7), Clip: bar,
			},
		},
		{
			name:       "moved by pixels left and down",
			directives: `| height 3 | x_pixel_offset -45 | y_pixel_offset 2`,
			expected: placed{
				Pos: term.Coordinates{X: 4, Y: 2}, Width: 4, Height: 3,
				Offset: image.Pt(-45, 2), Clip: bar,
			},
		},
		{
			name:       "moved by cells and pixels",
			directives: `| x_offset -3 | y_offset -1 | x_pixel_offset 5 | y_pixel_offset -3`,
			expected: placed{
				Pos: term.Coordinates{X: 1, Y: 2}, Width: 4, Height: 1,
				Offset: image.Pt(5, -3), Clip: bar,
			},
		},
		{
			name:       "moved by pixels and overflowing",
			directives: `| x_pixel_offset 13 | y_pixel_offset 40 | overflow`,
			expected: placed{
				Pos: term.Coordinates{X: 4, Y: 3}, Width: 4, Height: 1,
				Offset: image.Pt(13, 40), Overflow: true,
			},
		},
		{
			name:       "moved by a percentage of the bar",
			directives: `| x_offset "50%"`,
			expected: placed{
				Pos: term.Coordinates{X: 16, Y: 3}, Width: 4, Height: 1,
				Clip: image.Rect(16, 3, 20, 4),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newImageBarFixture(t, fmt.Sprintf(
				`{{ .Status }}{{ .Image | src %q | width 4 | reserve 0 | fit "fill" %s }}`,
				uri, tt.directives), storagestub.NewInMemoryService(), true, term.ColorDefault)
			f.settle(t)

			rec := &placementRecorder{StringerWriter: f.w}
			f.bar.Draw(rec)
			require.Len(t, rec.images, 1)
			img := rec.images[0]
			assert.Equal(t, tt.expected, placed{
				Pos: img.Pos, Width: img.Width, Height: img.Height,
				Offset: img.Offset, Clip: img.Clip, Overflow: img.Overflow,
			})
		})
	}
}

// barPos is where surround puts the bar on the screen.
var barPos = term.Coordinates{X: 4, Y: 2}

// screen is a frame of the surrounded image bar fixture with the given
// rows.
func screen(rows ...string) string {
	if len(rows) != screenHeight {
		panic(fmt.Sprintf("%d rows; expected %d", len(rows), screenHeight))
	}
	for _, row := range rows {
		if utf8.RuneCountInString(row) != screenWidth {
			panic(fmt.Sprintf("row %q is not %d wide", row, screenWidth))
		}
	}
	return "\n" + strings.Join(rows, "\n")
}

// inBar is a row of the surrounded image bar fixture with row on the
// editor or its bar and dots on either side.
func inBar(row string) string {
	return "...." + padBar(row) + "...."
}

func padBar(row string) string {
	return row + strings.Repeat(" ", imageBarWidth-utf8.RuneCountInString(row))
}

// dotted fills the screen with dots around the bar it draws.
type dotted struct {
	bar *sdkcomponent.Virtual[*text.StatusBar]
}

func (dotted) Resize(int, int) {}

func (d dotted) Draw(w term.Writer) {
	pos := d.bar.Position()
	bar := image.Rect(pos.X, pos.Y, pos.X+d.bar.Width(), pos.Y+d.bar.Height())
	dot := term.NewCell('.', 1, term.Attributes{})
	for y := range screenHeight {
		for x := range screenWidth {
			if !image.Pt(x, y).In(bar) {
				w.SetCell(term.Coordinates{X: x, Y: y}, dot)
			}
		}
	}
	d.bar.Draw(w)
}

func solid(width, height int, gray uint8) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	draw.Draw(img, img.Rect, image.NewUniform(color.Gray{Y: gray}), image.Point{}, draw.Src)
	return img
}

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

func TestStatusBarImageSharesDownloads(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	var fetches atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetches.Add(1)
		_, _ = w.Write(imageuritest.SamplePNG)
	}))
	defer srv.Close()

	layout := fmt.Sprintf(`{{ .Status }}{{ .ShiftRight }}{{ .Image | src "%s/logo.png" | width 4 }}`, srv.URL)
	storage := storagestub.NewInMemoryService()
	var frames []string
	for range 2 {
		f := newImageBarFixture(t, layout, storage, true, term.ColorDefault)
		f.settle(t)
		frames = append(frames, f.frame())
	}
	assert.Equal(t, int32(1), fetches.Load())
	assert.Equal(t, frames[0], frames[1])
}

const (
	imageBarTimeout = 5 * time.Second
	// imageBarQuiet is how long settle waits for another interrupt. It
	// spans a few frames of the loading animation.
	imageBarQuiet  = 100 * time.Millisecond
	imageBarWidth  = 24
	imageBarHeight = 4
)

var spinnerFrames, _ = sdkcomponent.ProgressAnimationFrames()

type imageBarFixture struct {
	bar *text.StatusBar
	// view is what the fixture draws: the bar, or the bar surrounded.
	view tui.Component
	w    comptest.StringerWriter
	irq  chan struct{}
}

// newImageBarFixture returns an imageBarWidth x imageBarHeight status bar
// over three lines of text with status NORMAL and background color bg,
// which redraws on every scheduled tick.
func newImageBarFixture(
	t *testing.T, layout string, storage storageapi.Service, images bool,
	bg term.Color,
) *imageBarFixture {
	t.Helper()
	uri, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)
	components, err := text.ParseStatusBarLayout(layout)
	require.NoError(t, err)

	f := &imageBarFixture{irq: make(chan struct{}, 1)}
	cfg := text.StatusBarConfig{
		ScheduleNextTick: func(fn func()) bool {
			fn()
			return true
		},
		Workspace:       uri,
		Publisher:       &TestEditor{},
		GitService:      &differ{},
		Layout:          components,
		Storage:         storage,
		Interrupter:     interrupts(f.irq),
		BackgroundColor: bg,
	}
	buf := cell.NewBuffer()
	buf.WriteString("first line\nsecond line\nthird line\n")
	scroll := component.NewScroll(buf)
	f.bar = text.WithStatusBar(newTestHandler(scroll), buf, scroll, false, false, cfg)
	t.Cleanup(func() { assert.NoError(t, f.bar.Close()) })
	f.bar.SetStatus("NORMAL", term.Attributes{})
	f.bar.Resize(imageBarWidth, imageBarHeight)
	f.view = f.bar
	f.w = imageBarWriter(imageBarWidth, imageBarHeight, images)
	return f
}

// imageBarWriter returns a writer that encodes images as ASCII art, or
// one that cannot draw them.
func imageBarWriter(width, height int, images bool) comptest.StringerWriter {
	if images {
		return asciiart.NewStringWriter(width, height, asciiart.DefaultConfig())
	}
	return term.NewStringWriter(width, height)
}

// surround puts the bar at barPos on a screenWidth x screenHeight screen
// of dots, which show what the bar's images cover past it.
func (f *imageBarFixture) surround(images bool) {
	bar := &sdkcomponent.Virtual[*text.StatusBar]{C: f.bar}
	bar.Move(barPos)
	bar.Resize(imageBarWidth, imageBarHeight)
	f.view = dotted{bar: bar}
	f.w = imageBarWriter(screenWidth, screenHeight, images)
}

func (f *imageBarFixture) frame() string {
	f.view.Draw(f.w)
	_ = f.w.Flush()
	defer func() { _ = f.w.Clear(term.Attributes{}) }()
	return f.w.String()
}

// settle redraws on every interrupt, as the host event loop does, until
// no image is loading: no loading animation is drawn, and none has
// interrupted for imageBarQuiet, which one does on every frame even
// where it is cut off.
func (f *imageBarFixture) settle(t *testing.T) {
	t.Helper()
	timeout := time.After(imageBarTimeout)
	for {
		frame := f.frame()
		select {
		case <-f.irq:
		case <-time.After(imageBarQuiet):
			if !loading(frame) {
				return
			}
		case <-timeout:
			t.Fatal("timed out waiting for the image to load")
		}
	}
}

func loading(frame string) bool {
	for _, s := range spinnerFrames {
		if s = strings.TrimSpace(s); s != "" && strings.Contains(frame, s) {
			return true
		}
	}
	return false
}

// interrupts is a term.Interrupter that coalesces pending interrupts
// like the host event loop.
type interrupts chan struct{}

func (i interrupts) Interrupt(context.Context) error {
	select {
	case i <- struct{}{}:
	default:
	}
	return nil
}

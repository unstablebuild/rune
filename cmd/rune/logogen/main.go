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

// Command logogen generates the art of the built-in logos consumed by the
// wallpapers. For the Rune logo it emits cmd/rune/logo_base.gen.go and
// cmd/rune/logo_hopper.gen.go, each holding a precomputed *image.NRGBA so
// the default wallpaper needs no PNG decode, embedded filesystem, or runtime
// image processing at startup.
//
// The base PNG is cropped to its opaque pixels and scaled down to at most
// maxHeight pixels tall: the wallpaper pads the logo itself, and its braille
// rendering never samples more dots than that.
//
// It also emits cmd/rune/logo_base.gen.png and cmd/rune/logo_hopper.gen.png,
// the same crop at full resolution, for the wallpaper to draw as a picture
// where the screen can draw images. Those are only decoded when drawn.
//
// The Unstable Build logo is emitted as is, as cmd/rune/logo_unstable_build.gen.png
// and cmd/rune/logo_unstable_build_hopper.gen.png, which are only decoded
// when it is the wallpaper.
//
// Every theme reuses the shared base logo except hopper, which is rendered as
// a grayscale variant, with a high contrast for the Unstable Build logo.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/image/draw"
)

const maxHeight = 320

// unstableBuildHopperContrast is the contrast of the Unstable Build logo's
// hopper variant, which reads as black and white.
const unstableBuildHopperContrast = 1.8

const header = `// Copyright (C) 2017-2026 The Rune Authors
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

`

func main() {
	_, self, _, _ := runtime.Caller(0)
	genDir := filepath.Dir(self)
	defaultBase := filepath.Join(genDir, "assets", "base.png")
	defaultUnstableBuild := filepath.Join(genDir, "assets", "unstable_build.png")
	defaultOut := filepath.Dir(genDir)

	base := flag.String("base", defaultBase, "path to the base Rune logo PNG")
	unstableBuild := flag.String("unstable-build", defaultUnstableBuild,
		"path to the Unstable Build logo PNG")
	out := flag.String("out", defaultOut, "directory to write logo_<name>.gen.* files")
	flag.Parse()

	picture := crop(loadPNG(*base))
	small := fit(picture)
	emit(*out, "base", small)
	emitPNG(*out, "base", picture)
	emit(*out, "hopper", gray(small, 1))
	emitPNG(*out, "hopper", gray(picture, 1))

	ub := toNRGBA(loadPNG(*unstableBuild))
	emitPNG(*out, "unstable_build", ub)
	emitPNG(*out, "unstable_build_hopper", gray(ub, unstableBuildHopperContrast))
}

func loadPNG(path string) image.Image {
	f, err := os.Open(path)
	must(err)
	defer f.Close()
	img, err := png.Decode(f)
	must(err)
	return img
}

func toNRGBA(src image.Image) *image.NRGBA {
	b := src.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			r, g, bb, a := src.At(b.Min.X+x, b.Min.Y+y).RGBA()
			c := color.NRGBAModel.Convert(color.RGBA64{R: uint16(r), G: uint16(g), B: uint16(bb), A: uint16(a)}).(color.NRGBA)
			dst.SetNRGBA(x, y, c)
		}
	}
	return dst
}

// crop copies the opaque pixels of src at full resolution.
func crop(src image.Image) *image.NRGBA {
	nrgba, ok := src.(*image.NRGBA)
	if !ok {
		panic(fmt.Sprintf("base logo is a %T, want an *image.NRGBA", src))
	}
	r := opaqueBounds(src)
	dst := image.NewNRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	for y := range r.Dy() {
		copy(dst.Pix[y*dst.Stride:][:dst.Stride], nrgba.Pix[nrgba.PixOffset(r.Min.X, r.Min.Y+y):])
	}
	return dst
}

// fit scales src down to maxHeight.
func fit(src *image.NRGBA) *image.NRGBA {
	w, h := src.Rect.Dx(), src.Rect.Dy()
	if h > maxHeight {
		w, h = (w*maxHeight+h/2)/h, maxHeight
	}
	// Scale in premultiplied RGBA so transparent pixels don't bleed
	// their color into the logo's edges.
	scaled := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(scaled, scaled.Rect, src, src.Rect, draw.Src, nil)
	dst := image.NewNRGBA(scaled.Rect)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, bb, a := scaled.At(x, y).RGBA()
			c := color.NRGBAModel.Convert(color.RGBA64{R: uint16(r), G: uint16(g), B: uint16(bb), A: uint16(a)}).(color.NRGBA)
			dst.SetNRGBA(x, y, c)
		}
	}
	return dst
}

func opaqueBounds(src image.Image) image.Rectangle {
	var r image.Rectangle
	b := src.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := src.At(x, y).RGBA(); a > 0 {
				r = r.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	if r.Empty() {
		panic("base logo has no opaque pixels")
	}
	return r
}

// gray renders a grayscale logo: each pixel is converted to its Rec. 601
// luminance, pushed away from mid-gray by contrast.
func gray(src *image.NRGBA, contrast float64) *image.NRGBA {
	b := src.Bounds()
	dst := image.NewNRGBA(b)
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			c := src.NRGBAAt(x, y)
			lum := 0.299*float64(c.R) + 0.587*float64(c.G) + 0.114*float64(c.B)
			g := uint8(min(max((lum-128)*contrast+128, 0), 255) + 0.5)
			dst.SetNRGBA(x, y, color.NRGBA{R: g, G: g, B: g, A: c.A})
		}
	}
	return dst
}

func title(name string) string {
	return strings.ToUpper(name[:1]) + name[1:]
}

func emit(outDir, name string, img *image.NRGBA) {
	width, height := img.Bounds().Dx(), img.Bounds().Dy()
	if img.Stride != width*4 || img.Bounds().Min != (image.Point{}) {
		panic(fmt.Sprintf("%s: bad dims %v stride=%d", name, img.Bounds(), img.Stride))
	}
	path := filepath.Join(outDir, fmt.Sprintf("logo_%s.gen.go", name))
	f, err := os.Create(path)
	must(err)
	defer f.Close()
	w := bufio.NewWriter(f)
	fmt.Fprint(w, header)
	fmt.Fprintln(w, "// Code generated by logogen; DO NOT EDIT.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "package main")
	fmt.Fprintln(w)
	fmt.Fprintln(w, `import "image"`)
	fmt.Fprintln(w)
	fmt.Fprintf(w, "var logoImage%s = &image.NRGBA{\n", title(name))
	fmt.Fprintf(w, "\tStride: %d,\n", img.Stride)
	fmt.Fprintf(w, "\tRect:   image.Rect(0, 0, %d, %d),\n", width, height)
	fmt.Fprint(w, "\tPix:    []uint8{")
	for i, b := range img.Pix {
		if i > 0 {
			fmt.Fprint(w, ", ")
		}
		fmt.Fprintf(w, "%d", b)
	}
	fmt.Fprintln(w, "},")
	fmt.Fprintln(w, "}")
	must(w.Flush())
}

func emitPNG(outDir, name string, img *image.NRGBA) {
	f, err := os.Create(filepath.Join(outDir, fmt.Sprintf("logo_%s.gen.png", name)))
	must(err)
	defer f.Close()
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	must(enc.Encode(f, img))
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

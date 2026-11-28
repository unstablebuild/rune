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

package kageshader_test

import (
	"image"
	"image/color"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component/gshader"
	"unstable.build/rune/internal/component/gshader/kageshader"
	"unstable.build/rune/internal/term/gui/shaderdevice/shaderdevicetest"
)

// size is small for the GPU to read back quickly, and wider than tall as
// windows are.
var size = image.Pt(12, 4)

func builtin(t testing.TB, name string) gshader.Shader {
	t.Helper()
	b, ok := kageshader.Lookup(name)
	require.True(t, ok, name)
	return gshader.FromProgram(b.Program, b.Uniforms)
}

func TestNames(t *testing.T) {
	assert.Equal(t, []string{"blaze", "gradient", "inferno", "noise", "plasma"}, kageshader.Names())
}

func TestLookup(t *testing.T) {
	for _, name := range kageshader.Names() {
		t.Run(name, func(t *testing.T) {
			b, ok := kageshader.Lookup(name)
			require.True(t, ok)
			assert.Equal(t, name+".kage", b.Program.Name())
			for u := range b.Uniforms {
				assert.True(t, b.Program.Declares(u), "%s declares %s", name, u)
			}
			_, err := b.Uniforms.Values(b.Program, gshader.Defaults{}, func(term.Color) color.RGBA { return color.RGBA{} })
			assert.NoError(t, err, "its uniforms have the types it declares")

			b.Uniforms["Changed"] = float32(1)
			again, _ := kageshader.Lookup(name)
			assert.NotContains(t, again.Uniforms, "Changed", "each lookup has uniforms of its own")
		})
	}
	_, ok := kageshader.Lookup("nope")
	assert.False(t, ok)
}

func TestBuiltinsAnimateWithTheTimeAlone(t *testing.T) {
	const total = 8
	for _, name := range kageshader.Names() {
		t.Run(name, func(t *testing.T) {
			sh := builtin(t, name)
			shaderdevicetest.AssertDeterministic(t, sh, total, size)
			shaderdevicetest.AssertOrderIndependent(t, sh, total, size)
			shaderdevicetest.AssertSeamless(t, sh, total, size)
		})
	}
}

func TestBuiltinsCombine(t *testing.T) {
	const total = 8
	tests := []struct {
		name string
		sh   func(t *testing.T) gshader.Shader
	}{
		{name: "cross fade", sh: func(t *testing.T) gshader.Shader {
			return gshader.CrossFade(gshader.CrossFadeParams{ChangeAtPerc: 0.5, OverlapPerc: 1},
				builtin(t, "plasma"), builtin(t, "noise"))
		}},
		{name: "cut", sh: func(t *testing.T) gshader.Shader {
			return gshader.Cut(gshader.DefaultCutParams(), builtin(t, "gradient"), builtin(t, "inferno"))
		}},
		{name: "virtual", sh: func(t *testing.T) gshader.Shader {
			return gshader.Mix(builtin(t, "gradient"), gshader.Virtual(builtin(t, "blaze"), image.Rect(2, 1, 8, 3)))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sh := tt.sh(t)
			shaderdevicetest.AssertDeterministic(t, sh, total, size)
			shaderdevicetest.AssertOrderIndependent(t, sh, total, size)
		})
	}
}

func TestBuiltinsAnimate(t *testing.T) {
	shaderdevicetest.RequireGPU(t)
	for _, name := range []string{"noise", "plasma", "blaze", "inferno"} {
		t.Run(name, func(t *testing.T) {
			h := shaderdevicetest.New(t)
			first, err := h.Render(builtin(t, name), 0, 8, size)
			require.NoError(t, err)
			middle, err := h.Render(builtin(t, name), 4, 8, size)
			require.NoError(t, err)
			assert.NotEqual(t, first.Pix, middle.Pix, "half way through differs from the start")
		})
	}
}

func TestBuiltinSeedsDiffer(t *testing.T) {
	shaderdevicetest.RequireGPU(t)
	for _, name := range []string{"noise", "blaze", "inferno"} {
		t.Run(name, func(t *testing.T) {
			h := shaderdevicetest.New(t)
			b, _ := kageshader.Lookup(name)
			first, err := h.Render(gshader.FromProgram(b.Program, b.Uniforms), 0, 8, size)
			require.NoError(t, err)
			b.Uniforms[gshader.UniformSeed] = float32(42)
			other, err := h.Render(gshader.FromProgram(b.Program, b.Uniforms), 0, 8, size)
			require.NoError(t, err)
			assert.NotEqual(t, first.Pix, other.Pix)
		})
	}
}

// BenchmarkBuiltins measures a frame of each built-in over a 1920x1088
// window, which only counts under -tags e2e. Reading pixels back waits for
// the GPU, once every batch of frames so that it adds little to each.
func BenchmarkBuiltins(b *testing.B) {
	const batch = 30
	for _, name := range kageshader.Names() {
		b.Run(name, func(b *testing.B) {
			sh := builtin(b, name)
			h := shaderdevicetest.New(b)
			h.PixelSize = cell.PixelSize{Width: 8, Height: 16}
			key := term.NewImageID()
			frame := 0
			for b.Loop() {
				h.Draw(func() {
					var f gshader.Frame
					for range batch {
						frame = (frame + 1) % 300
						var err error
						f, err = h.Device.Render(key, image.Pt(240, 68), nil, sh.Shade(frame, 300, gshader.Source()))
						if err != nil {
							b.Fatal(err)
						}
					}
					f.Src.At(0, 0)
				})
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*batch), "ns/frame")
		})
	}
}

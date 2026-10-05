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

package drawrect

import (
	"testing"

	ebiten "github.com/hajimehoshi/ebiten/v2"
	"github.com/stretchr/testify/assert"
)

// blendFactor resolves a BlendFactor the same way ebiten's internal
// driver does (see ebiten's blend.go): BlendFactorDefault means
// source-over, which is BlendFactorOne on the source side and
// BlendFactorOneMinusSourceAlpha on the destination side.
func blendFactor(f ebiten.BlendFactor, source bool, srcAlpha float64) float64 {
	switch f {
	case ebiten.BlendFactorDefault:
		if source {
			return 1
		}
		return 1 - srcAlpha
	case ebiten.BlendFactorZero:
		return 0
	case ebiten.BlendFactorOne:
		return 1
	case ebiten.BlendFactorOneMinusSourceAlpha:
		return 1 - srcAlpha
	default:
		panic("blendFactor: unsupported factor in test simulator")
	}
}

func blendOperation(op ebiten.BlendOperation, src, dst float64) float64 {
	switch op {
	case ebiten.BlendOperationAdd:
		return src + dst
	case ebiten.BlendOperationMax:
		return max(src, dst)
	default:
		panic("blendOperation: unsupported operation in test simulator")
	}
}

// compositeAlpha applies b's formula (as documented on ebiten.Blend) to
// the alpha channel only, given a premultiplied source alpha srcA over a
// destination alpha dstA.
func compositeAlpha(b ebiten.Blend, srcA, dstA float64) float64 {
	srcFactor := blendFactor(b.BlendFactorSourceAlpha, true, srcA)
	dstFactor := blendFactor(b.BlendFactorDestinationAlpha, false, srcA)
	return blendOperation(b.BlendOperationAlpha, srcFactor*srcA, dstFactor*dstA)
}

func TestDefaultDrawTrianglesOptionsAccumulateAlpha(t *testing.T) {
	for _, tc := range []struct {
		name      string
		srcAlpha  float64
		bgOpacity float64
	}{
		{name: "translucent rect over half-opaque window", srcAlpha: 0.6, bgOpacity: 0.5},
		{name: "opaque rect over half-opaque window", srcAlpha: 1, bgOpacity: 0.5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := tc.srcAlpha + (1-tc.srcAlpha)*tc.bgOpacity
			got := compositeAlpha(defaultDrawTrianglesOptions.Blend, tc.srcAlpha, tc.bgOpacity)
			assert.InDeltaf(t, want, got, 1e-9,
				"rect alpha should follow source-over, not clamp to max(src, dst)")
		})
	}
}

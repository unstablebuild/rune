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

package glslshader

import (
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLatticeRepeatsEveryPeriod(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for _, period := range []vec2D{vec2(-10, -10), vec2(29, 0), vec2(9, 38), vec2(1, 0), vec2(-29, 3)} {
		l := newLattice(period)
		for range 200 {
			n := vec2(rng.Float64()*80-40, rng.Float64()*80-40)
			for _, k := range []float{1, -1, 3} {
				assert.InDelta(t, l.noise(n), l.noise(n.add(period.multSc(k))), 1e-9,
					"period %v: noise at %v and %v periods on", period, n, k)
			}
		}
	}
}

func TestLatticeRepeatsNowhereElse(t *testing.T) {
	for _, period := range []vec2D{vec2(-10, -10), vec2(29, 0), vec2(9, 38), {}} {
		l := newLattice(period)
		for sx := int64(-12); sx <= 12; sx++ {
			for sy := int64(-12); sy <= 12; sy++ {
				if sx == 0 && sy == 0 ||
					l.n != 0 && sx*l.b == sy*l.a && (sx*l.a+sy*l.b)%l.n == 0 {
					continue // a whole number of periods
				}
				same := 0
				for i := int64(-20); i < 20; i++ {
					for j := int64(-20); j < 20; j++ {
						a, _, _ := l.corners(i, j)
						b, _, _ := l.corners(i+sx, j+sy)
						if a == b {
							same++
						}
					}
				}
				assert.Zero(t, same, "period %v: shifting by %d, %d keeps %d of 1600 values",
					period, sx, sy, same)
			}
		}
	}
}

func TestDriftsEndWhereTheyStart(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	drifts := newDrifts(-13.8, 28.56, vec2(-1, 0), 5, 2.7)
	for range 100 {
		n := vec2(rng.Float64()*8, rng.Float64()*8)
		assert.InDelta(t, fbm(n, 0, drifts, 2.7, 0.47), fbm(n, 1, drifts, 2.7, 0.47), 1e-9)
		assert.NotEqual(t, fbm(n, 0, drifts, 2.7, 0.47), fbm(n, 0.5, drifts, 2.7, 0.47))
	}
	scale := 1.0
	for k, d := range drifts {
		assert.InDelta(t, -13.8*scale, d.period.y, 0.5, "octave %d drifts by about its share", k)
		assert.InDelta(t, -13.8*scale-28.56*scale, d.period.x, 1, "octave %d drifts by about its share", k)
		scale *= 2.7
	}
}

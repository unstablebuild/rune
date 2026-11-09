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

import "math"

// lattice is value noise whose values repeat every period and nowhere
// else: the noise at n and at n plus a whole number of periods is the
// same. The zero period repeats nowhere.
type lattice struct {
	// a, b is the period, and n its length squared.
	a, b, n int64
}

func newLattice(period vec2D) lattice {
	a, b := int64(period.x), int64(period.y)
	return lattice{a: a, b: b, n: a*a + b*b}
}

// noise is the value noise the fire shaders were tuned with: its top
// left corner reads the value of the bottom right one.
func (l lattice) noise(n vec2D) float {
	b := n.floor()
	f := smoothstep2D(vec2FromScalar(0.0), vec2FromScalar(1.0), fract2D(n))
	h00, h10, h11 := l.corners(int64(b.x), int64(b.y))
	return mix(mix(h00, h10, f.x), mix(h10, h11, f.x), f.y)
}

// corners returns the values at lattice points i, j, at i+1, j and at
// i+1, j+1.
func (l lattice) corners(i, j int64) (h00, h10, h11 float) {
	if l.n == 0 {
		return latticeValue(i, j), latticeValue(i+1, j), latticeValue(i+1, j+1)
	}
	// Points a period apart share both their cross product with the
	// period and, modulo its squared length, their dot product with it,
	// and no other two points do.
	c := i*l.b - j*l.a
	d := (i*l.a + j*l.b) % l.n
	if d < 0 {
		d += l.n
	}
	return latticeValue(c, d),
		latticeValue(c+l.b, l.wrap(d+l.a)),
		latticeValue(c+l.b-l.a, l.wrap(d+l.a+l.b))
}

// wrap brings d, which is at most n out of [0, n), back into it.
func (l lattice) wrap(d int64) int64 {
	if d >= l.n {
		return d - l.n
	}
	if d < 0 {
		return d + l.n
	}
	return d
}

// latticeValue scrambles c and d into a value in [0, 1) that looks random
// but is always the same for the same c and d.
func latticeValue(c, d int64) float {
	h := uint64(c)*0x9e3779b97f4a7c15 + uint64(d)*0xc2b2ae3d27d4eb4f
	h ^= h >> 30
	h *= 0xbf58476d1ce4e5b9
	h ^= h >> 27
	h *= 0x94d049bb133111eb
	h ^= h >> 31
	return float(h>>11) / (1 << 53)
}

// drift is an octave of noise that moves by its lattice's period over a
// run, so that the run ends where it started.
type drift struct {
	period  vec2D
	lattice lattice
}

func (d drift) noise(n vec2D, phase float) float {
	return d.lattice.noise(n.add(d.period.multSc(phase)))
}

// newDrifts returns the drifts of the octaves of an fbm whose input moves
// over a run by diagonal along (1, 1) and by pan along panDir, an axis.
// Octave k sees the move lacunarity^k times bigger, rounded to a whole
// lattice step along each so that it repeats exactly.
func newDrifts(diagonal, pan float, panDir vec2D, octaves int, lacunarity float) []drift {
	drifts := make([]drift, octaves)
	scale := 1.0
	for k := range drifts {
		period := vec2FromScalar(math.Round(scale * diagonal)).
			add(panDir.multSc(math.Round(scale * pan)))
		drifts[k] = drift{period: period, lattice: newLattice(period)}
		scale *= lacunarity
	}
	return drifts
}

// fbm sums octaves of noise at n, each lacunarity times finer and gain
// times fainter than the last, drifting as drifts say, phase of the way
// through a run.
func fbm(n vec2D, phase float, drifts []drift, lacunarity, gain float) float {
	total := 0.0
	amplitude := 1.0
	for _, d := range drifts {
		total += d.noise(n, phase) * amplitude
		n = n.multSc(lacunarity)
		amplitude *= gain
	}
	return total
}

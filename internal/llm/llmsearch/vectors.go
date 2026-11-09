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

package llmsearch

import (
	"encoding/binary"
	"fmt"
	"math"
	"slices"
)

const (
	// dims is the length of every vector: EmbeddingGemma's 768.
	dims = 768
	// vectorBlockRows rows are turned into float32 at a time for scoring.
	vectorBlockRows = 1024
	// minDeviation keeps a query whose scores barely differ from making
	// every small difference stand out.
	minDeviation = 0.02
)

// halfBytes stores v as little-endian IEEE 754 binary16 values.
func halfBytes(v []float32) []byte {
	out := make([]byte, 2*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint16(out[2*i:], float32ToHalf(f))
	}
	return out
}

// float32ToHalf rounds f to the nearest binary16, ties to even.
func float32ToHalf(f float32) uint16 {
	b := math.Float32bits(f)
	sign := uint16(b>>16) & 0x8000
	exp := int(b>>23) & 0xff
	mant := b & 0x7fffff
	if exp == 0xff {
		if mant != 0 {
			return sign | 0x7e00
		}
		return sign | 0x7c00
	}
	if e := exp - 127 + 15; e > 0 {
		if e >= 0x1f {
			return sign | 0x7c00
		}
		h := uint32(e)<<10 | mant>>13
		if rem := mant & 0x1fff; rem > 0x1000 || (rem == 0x1000 && h&1 == 1) {
			h++ // a carry into the exponent is still the right value, up to infinity
		}
		return sign | uint16(h)
	}
	shift := uint(126 - exp)
	if shift >= 25 {
		return sign
	}
	m := mant | 0x800000
	h := m >> shift
	rem, half := m&(1<<shift-1), uint32(1)<<(shift-1)
	if rem > half || (rem == half && h&1 == 1) {
		h++
	}
	return sign | uint16(h)
}

// halfToFloat32 decodes a binary16 value exactly.
func halfToFloat32(h uint16) float32 {
	sign := uint32(h&0x8000) << 16
	exp := uint32(h>>10) & 0x1f
	mant := uint32(h & 0x3ff)
	switch exp {
	case 0:
		if mant == 0 {
			return math.Float32frombits(sign)
		}
		e := uint32(127 - 15 + 1)
		for mant&0x400 == 0 {
			mant <<= 1
			e--
		}
		return math.Float32frombits(sign | e<<23 | (mant&0x3ff)<<13)
	case 0x1f:
		return math.Float32frombits(sign | 0x7f800000 | mant<<13)
	}
	return math.Float32frombits(sign | (exp+127-15)<<23 | mant<<13)
}

// vectorMatrix holds the index's vectors as stored (binary16, half the
// memory of float32), one row per segment that has one. It is not safe for
// concurrent use.
type vectorMatrix struct {
	halves  []uint16
	rowSeg  []uint32
	rowFile []uint32
}

func (m *vectorMatrix) rows() int { return len(m.rowSeg) }

// add appends the stored vector vec of segment seg of file.
func (m *vectorMatrix) add(seg, file uint32, vec []byte) error {
	if len(vec) != 2*dims {
		return fmt.Errorf("vector of segment %d has %d bytes, want %d", seg, len(vec), 2*dims)
	}
	for i := range dims {
		m.halves = append(m.halves, binary.LittleEndian.Uint16(vec[2*i:]))
	}
	m.rowSeg = append(m.rowSeg, seg)
	m.rowFile = append(m.rowFile, file)
	return nil
}

// removeFile drops the rows of file, keeping the others in order.
func (m *vectorMatrix) removeFile(file uint32) {
	n := 0
	for row := range m.rowSeg {
		if m.rowFile[row] == file {
			continue
		}
		if n != row {
			copy(m.halves[n*dims:(n+1)*dims], m.halves[row*dims:(row+1)*dims])
			m.rowSeg[n], m.rowFile[n] = m.rowSeg[row], m.rowFile[row]
		}
		n++
	}
	m.halves = slices.Clip(m.halves[:n*dims])
	m.rowSeg, m.rowFile = m.rowSeg[:n], m.rowFile[:n]
}

// score is the cosine similarity of query with every row (all vectors are
// L2-normalized, so it is the dot product), converting a block of rows to
// float32 at a time.
func (m *vectorMatrix) score(query []float32) []float32 {
	rows := m.rows()
	scores := make([]float32, rows)
	block := make([]float32, min(rows, vectorBlockRows)*dims)
	for start := 0; start < rows; start += vectorBlockRows {
		count := min(vectorBlockRows, rows-start)
		for i, h := range m.halves[start*dims : (start+count)*dims] {
			block[i] = halfToFloat32(h)
		}
		for r := range count {
			var dot float32
			for i, q := range query {
				dot += block[r*dims+i] * q
			}
			scores[start+r] = dot
		}
	}
	return scores
}

// meaning puts one query's scores on its own scale: each score minus their
// average, in standard deviations of all of them (at least minDeviation).
// Only vectors that stand out (Searcher.zGate) count as meaning matches.
func meaning(scores []float32) []float64 {
	if len(scores) == 0 {
		return nil
	}
	n := float64(len(scores))
	sum := 0.0
	for _, s := range scores {
		sum += float64(s)
	}
	mean := sum / n
	squares := 0.0
	for _, s := range scores {
		d := float64(s) - mean
		squares += d * d
	}
	deviation := max(math.Sqrt(squares/n), minDeviation)
	out := make([]float64, len(scores))
	for i, s := range scores {
		out[i] = (float64(s) - mean) / deviation
	}
	return out
}

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

package vtesnap

import (
	"encoding/binary"
	"fmt"
	"math"
)

// enc appends little-endian primitives to a growable payload buffer.
type enc struct{ b []byte }

func (e *enc) u8(v uint8)   { e.b = append(e.b, v) }
func (e *enc) bool(v bool)  { e.b = append(e.b, boolByte(v)) }
func (e *enc) u16(v uint16) { e.b = binary.LittleEndian.AppendUint16(e.b, v) }
func (e *enc) u32(v uint32) { e.b = binary.LittleEndian.AppendUint32(e.b, v) }

func (e *enc) i32(v int) { e.u32(uint32(int32(v))) }

func (e *enc) rune(v rune) { e.u32(uint32(v)) }

func (e *enc) str(v string) {
	e.u32(uint32(len(v)))
	e.b = append(e.b, v...)
}

func boolByte(v bool) uint8 {
	if v {
		return 1
	}
	return 0
}

// dec reads little-endian primitives from a payload. The first failure
// latches, so a caller may read a whole payload and check err once.
type dec struct {
	b   []byte
	err error
}

func (d *dec) fail(format string, args ...any) {
	if d.err == nil {
		d.err = fmt.Errorf("%w: %s", ErrMalformed, fmt.Sprintf(format, args...))
	}
}

func (d *dec) take(n int) []byte {
	if d.err != nil {
		return nil
	}
	if len(d.b) < n {
		d.fail("want %d bytes, have %d", n, len(d.b))
		return nil
	}
	out := d.b[:n]
	d.b = d.b[n:]
	return out
}

func (d *dec) u8() uint8 {
	v := d.take(1)
	if v == nil {
		return 0
	}
	return v[0]
}

func (d *dec) bool() bool { return d.u8() == 1 }

func (d *dec) u16() uint16 {
	v := d.take(2)
	if v == nil {
		return 0
	}
	return binary.LittleEndian.Uint16(v)
}

func (d *dec) u32() uint32 {
	v := d.take(4)
	if v == nil {
		return 0
	}
	return binary.LittleEndian.Uint32(v)
}

func (d *dec) i32() int { return int(int32(d.u32())) }

func (d *dec) rune() rune { return rune(int32(d.u32())) }

// count reads a length prefix and rejects one that cannot be backed by
// the remaining payload, so a corrupt length never drives a large
// allocation.
func (d *dec) count(elemSize int) int {
	n := d.u32()
	if d.err != nil {
		return 0
	}
	if n > math.MaxInt32 || int(n)*elemSize > len(d.b) {
		d.fail("length %d exceeds remaining %d bytes", n, len(d.b))
		return 0
	}
	return int(n)
}

func (d *dec) str() string {
	n := d.count(1)
	if d.err != nil {
		return ""
	}
	return string(d.take(n))
}

// done reports the first failure, also rejecting trailing bytes the
// layout did not account for.
func (d *dec) done() error {
	if d.err != nil {
		return d.err
	}
	if len(d.b) != 0 {
		return fmt.Errorf("%w: %d trailing bytes", ErrMalformed, len(d.b))
	}
	return nil
}

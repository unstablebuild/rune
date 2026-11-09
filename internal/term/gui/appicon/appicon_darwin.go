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

//go:build darwin && cgo

package appicon

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa

void runeSetApplicationIcon(const unsigned char *pix, int width, int height);
*/
import "C"

import "unsafe"

func setApplicationIcon(pix []byte, width, height int) {
	var p *C.uchar
	if len(pix) > 0 {
		p = (*C.uchar)(unsafe.Pointer(&pix[0]))
	}
	C.runeSetApplicationIcon(p, C.int(width), C.int(height))
}

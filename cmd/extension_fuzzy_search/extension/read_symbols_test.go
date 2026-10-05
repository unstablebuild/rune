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

package extension

import (
	"testing"

	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
)

func TestMakeSymbolItemEndOfBufferRow(t *testing.T) {
	buf := new(cell.Buffer)
	buf.Init()
	buf.WriteString("hello")

	// from.Y == buf.Rows() is the valid "one past last row" sentinel.
	from := term.Coordinates{Y: buf.Rows(), X: 0}
	to := term.Coordinates{Y: buf.Rows(), X: 0}

	if _, err := makeSymbolItem(from, to, "file.go", buf, "name"); err != nil {
		t.Fatalf("makeSymbolItem: %v", err)
	}
}

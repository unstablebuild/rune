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

package shadertest

import (
	"testing"

	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/shader"
)

// fixedBrailleArt draws the same cells whatever its size, like art that
// went out of sync with the screen.
type fixedBrailleArt struct{}

func (fixedBrailleArt) Resize(int, int) {}

func (fixedBrailleArt) Draw(w term.Writer) {
	w.SetCell(term.Coordinates{}, term.NewCell('⣿', 1, term.Attributes{}))
	w.SetCell(term.Coordinates{X: 1}, term.NewCell('⡇', 1, term.Attributes{}))
	w.SetCell(term.Coordinates{Y: 1}, term.NewCell('⠛', 1, term.Attributes{}))
}

func TestBrailleSwap(t *testing.T) {
	TestShader(t, shader.BrailleSwap(fixedBrailleArt{}))
}

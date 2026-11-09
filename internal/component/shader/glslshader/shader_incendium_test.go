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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/shader"
	"unstable.build/rune/internal/component/shader/shadertest"
)

func TestIncendium(t *testing.T) {
	sh := Incendium(DefaultIncendiumParams(), term.Attributes{})
	shadertest.TestShader(t, sh)
}

func TestIncendiumDependsOnProgressOnly(t *testing.T) {
	build := func() shader.Shader { return Incendium(DefaultIncendiumParams(), term.Attributes{}) }
	shadertest.AssertProgressOnly(t, build, shadertest.MakeTextCanvas(12, 6))
	shadertest.AssertOrderIndependent(t, build, shadertest.MakeTextCanvas(12, 6), 20)
}

func TestIncendiumFrontAdvancesEveryFrame(t *testing.T) {
	for _, total := range []int{90, 300, 900} {
		sh := Incendium(DefaultIncendiumParams(), term.Attributes{})
		assert.Zero(t, frontStall(sh, shadertest.MakeTextCanvas(40, 14), total),
			"frames in a row the front stands still over %d frames", total)
	}
}

func BenchmarkIncendium(b *testing.B) {
	sh := Incendium(DefaultIncendiumParams(), term.Attributes{})
	shadertest.BenchmarkShader(b, sh, 120, 40, shadertest.MakeTextCanvas(120, 40))
}

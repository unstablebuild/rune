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

	"unstable.build/rune/internal/component/shader/shadertest"
)

func TestBlaze(t *testing.T) {
	sh := Blaze(DefaultBlazeParams(), 30)
	shadertest.TestShader(t, sh)
}

func TestBlazePaintForeground(t *testing.T) {
	params := DefaultBlazeParams()
	params.PaintForeground = true

	t.Run("suite", func(t *testing.T) {
		shadertest.TestShader(t, Blaze(params, 30))
	})

	t.Run("paints the channel it was asked for", func(t *testing.T) {
		for _, tc := range []struct {
			name            string
			paintForeground bool
		}{
			{name: "background by default"},
			{name: "foreground when enabled", paintForeground: true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				p := DefaultBlazeParams()
				p.PaintForeground = tc.paintForeground
				assertPaintsChannel(t, Blaze(p, 30), tc.paintForeground)
			})
		}
	})

	t.Run("leaves background glyphs bare", func(t *testing.T) {
		assertPaintsOnlyText(t, Blaze(params, 30))
	})
}

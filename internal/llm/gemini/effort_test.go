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

package gemini

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDefaultEffortFor(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		// Gemini 4 Neon and Hydrogen document high as their default.
		{Gemini_4_Neon, "high"},
		{Gemini_4_Hydrogen, "high"},
		{Gemini_4_Carbon, "medium"},
		{Gemini_4_Argon, "medium"},
		{Gemini_3_8_Flash, "medium"},
		{Gemini_3_6_Flash, "medium"},
		{Gemini_3_1_Pro_Preview, "medium"},
		{"gemini-4-unreleased", "medium"},
		// Gemini 2.x does not require a thinking config.
		{Gemini_2_5_FlashLite, ""},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			assert.Equal(t, tt.want, DefaultEffortFor(tt.model))
		})
	}
}

func TestDefaultEffortForMatchesNormalize(t *testing.T) {
	for model := range AvailableModels() {
		normalized, _ := NormalizeEffort(model, "")
		assert.Equal(t, normalized, DefaultEffortFor(model), "model %s", model)
	}
}

func TestDefaultEffortForIsSupported(t *testing.T) {
	for model := range AvailableModels() {
		def := DefaultEffortFor(model)
		if def == "" {
			continue
		}
		normalized, warning := NormalizeEffort(model, def)
		assert.Equal(t, def, normalized, "model %s", model)
		assert.Empty(t, warning, "model %s", model)
	}
}

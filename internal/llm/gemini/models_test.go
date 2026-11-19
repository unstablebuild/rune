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

func TestFlagshipModelInCatalog(t *testing.T) {
	_, ok := AvailableModels()[FlagshipModel()]
	assert.True(t, ok, "flagship %q must be in the catalog", FlagshipModel())
	assert.Equal(t, Gemini_4_Carbon, FlagshipModel())
}

func TestCurrentFlashModelsInCatalog(t *testing.T) {
	models := AvailableModels()
	assert.Equal(t, 1_048_576, models[Gemini_3_6_Flash])
	assert.Equal(t, 1_048_576, models[Gemini_3_5_FlashLite])
}

func TestNewModelsCatalog(t *testing.T) {
	tests := []struct {
		constant string
		slug     string
	}{
		{Gemini_4_Carbon, "gemini-4-carbon"},
		{Gemini_4_Neon, "gemini-4-neon"},
		{Gemini_4_Hydrogen, "gemini-4-hydrogen"},
		{Gemini_4_Argon, "gemini-4-argon"},
		{Gemini_3_8_Flash, "gemini-3.8-flash"},
		{Gemini_3_1_FlashLite, "gemini-3.1-flash-lite"},
	}
	for _, tt := range tests {
		t.Run(tt.slug, func(t *testing.T) {
			assert.Equal(t, tt.slug, tt.constant)
			assert.Equal(t, 1_048_576, AvailableModels()[tt.constant])
			assert.Equal(t, 65536, MaxOutputTokens(tt.constant))
		})
	}
}

func TestRetiredModelsNotInCatalog(t *testing.T) {
	retired := []string{
		"gemini-3.1-flash-lite-preview", "gemini-2.5-pro", "gemini-2.5-flash",
		"gemini-2.0-flash", "gemini-2.0-flash-lite",
	}
	avail := AvailableModels()
	for _, model := range retired {
		t.Run(model, func(t *testing.T) {
			_, ok := avail[model]
			assert.False(t, ok)
			assert.Zero(t, MaxOutputTokens(model))
		})
	}
}

func TestVerificationModelInCatalog(t *testing.T) {
	_, ok := AvailableModels()[VerificationModel]
	assert.True(t, ok, "verification model %q must be in the catalog", VerificationModel)
	assert.Equal(t, Gemini_3_5_FlashLite, VerificationModel)
}

func TestMaxOutputTokens(t *testing.T) {
	tests := []struct {
		model string
		want  int
	}{
		{Gemini_4_Carbon, 65536},
		{Gemini_3_6_Flash, 65536},
		{Gemini_3_5_FlashLite, 65536},
		{Gemini_3_1_Pro_Preview, 65536},
		{Gemini_2_5_FlashLite, 65536},
		{"unknown-model", 0},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			assert.Equal(t, tt.want, MaxOutputTokens(tt.model))
		})
	}
}

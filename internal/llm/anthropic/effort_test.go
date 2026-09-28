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

package anthropic

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDefaultEffortFor(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		// Adaptive-thinking models document high as the level the
		// provider applies when the request omits one; Opus 5.5
		// documents medium.
		{ClaudeOpus5Dot5, "medium"},
		{ClaudeOpus5, "high"},
		{ClaudeSonnet5, "high"},
		{ClaudeFable5, "high"},
		{ClaudeFable5Dot1, "high"},
		{ClaudeOpus4Dot8, "high"},
		{ClaudeOpus4Dot7, "high"},
		{ClaudeOpus4Dot6, "high"},
		{ClaudeSonnet4Dot6, "high"},
		// Older models take an explicit thinking budget instead, and
		// publish no default effort level.
		{ClaudeOpus4Dot5, ""},
		{ClaudeSonnet4, ""},
		{ClaudeOpus4, ""},
		// Claude 3 has no reasoning effort at all.
		{ClaudeOpus3, ""},
		{ClaudeHaiku3, ""},
		{"not-a-model", ""},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			assert.Equal(t, tt.want, DefaultEffortFor(tt.model))
		})
	}
}

// A reported default must be a level the model actually accepts, or
// the status bar would advertise an effort the user cannot select.
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

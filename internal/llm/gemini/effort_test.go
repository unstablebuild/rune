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

// Gemini 3 always needs a thinking config, so Rune sends a concrete
// level when the caller asks for none; that level is the default the
// user is actually running under.
func TestDefaultEffortFor(t *testing.T) {
	for model := range AvailableModels() {
		want := ""
		if isGemini3(model) {
			want = defaultGemini3Effort
		}
		assert.Equal(t, want, DefaultEffortFor(model), "model %s", model)
	}
}

// The reported default must match what NormalizeEffort actually sends
// for an unset effort.
func TestDefaultEffortForMatchesNormalize(t *testing.T) {
	for model := range AvailableModels() {
		normalized, _ := NormalizeEffort(model, "")
		assert.Equal(t, normalized, DefaultEffortFor(model), "model %s", model)
	}
}

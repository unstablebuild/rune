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

package dreamcomponent

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unstablebuild/rune-go-sdk/term"

	"unstable.build/rune/cmd/rune-agent/memory/dream"
)

func TestNew_Render(t *testing.T) {
	const width = 40
	const height = 1
	tests := []struct {
		name     string
		event    dream.Progress
		expected string
	}{
		{
			name:     "bootstrap is success",
			event:    dream.Progress{Type: dream.ProgressBootstrap, Message: "initializing memory workspace"},
			expected: "✓ initializing memory workspace         ",
		},
		{
			name:     "phase-start uses working glyph",
			event:    dream.Progress{Type: dream.ProgressPhaseStart, Message: "Starting phase: Extract memories"},
			expected: "⚙ Phase: Extract memories               ",
		},
		{
			name:     "phase-finish uses success glyph",
			event:    dream.Progress{Type: dream.ProgressPhaseFinish, Message: "Completed phase: Extract memories"},
			expected: "✓ Phase: Extract memories               ",
		},
		{
			name:     "analyzing renders running with counter",
			event:    dream.Progress{Type: dream.ProgressAnalyzing, DialogueID: "d1", Message: "Analyzing d1", Progress: 0, Total: 2, Units: "conversations"},
			expected: "⚙ Analyzing d1 (1/2 conversations)      ",
		},
		{
			name:     "tool-call renders running with sub-step",
			event:    dream.Progress{Type: dream.ProgressToolCall, DialogueID: "d1", ToolName: "read_file", Message: "main.go"},
			expected: "└─ ⚙ read_file                          ",
		},
		{
			name:     "tool-result success collapses to checkmark",
			event:    dream.Progress{Type: dream.ProgressToolResult, DialogueID: "d1", ToolName: "read_file", Message: "main.go", Duration: 250 * time.Millisecond},
			expected: "└─ ✓ read_file (in 250ms)               ",
		},
		{
			name:     "tool-result IsError marks row as error",
			event:    dream.Progress{Type: dream.ProgressToolResult, DialogueID: "d1", ToolName: "edit", IsError: true, Message: "boom", Duration: 1500 * time.Millisecond},
			expected: "└─ ✗ edit (in 1.5s)                     ",
		},
		{
			name:     "error event indents and uses error glyph",
			event:    dream.Progress{Type: dream.ProgressError, DialogueID: "d1", Message: "boom"},
			expected: "└─ ✗ boom                               ",
		},
		{
			name:     "done renders the terminal block glyph",
			event:    dream.Progress{Type: dream.ProgressDone, Message: "Dream complete"},
			expected: "■ Dream complete                        ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := New(tt.event)
			assert.Equal(t, 1, r.Height(width))
			r.Resize(width, height)
			w := term.NewStringWriter(width, height)
			require.NoError(t, w.Clear(term.Attributes{}))
			r.Draw(w)
			require.NoError(t, w.Flush())
			assert.Equal(t, tt.expected, w.String())
		})
	}
}

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

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"unstable.build/rune/cmd/rune-agent/headless"
)

func TestParseHeadlessArgs(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		want    headless.Options
		wantErr string
	}{
		{
			name: "instructions only",
			args: []string{"--headless", "task.md"},
			want: headless.Options{InstructionsFile: "task.md"},
		},
		{
			name: "model and effort",
			args: []string{"--headless", "task.md",
				"--model", "anthropic/claude-opus-4-6", "--effort", "high"},
			want: headless.Options{
				InstructionsFile: "task.md",
				Model:            "anthropic/claude-opus-4-6",
				Effort:           "high",
			},
		},
		{
			name:    "missing instructions",
			args:    []string{"--headless"},
			wantErr: "usage: rune-agent --headless",
		},
		{
			name:    "flag in place of instructions",
			args:    []string{"--headless", "--model", "openai/gpt-5"},
			wantErr: "usage: rune-agent --headless",
		},
		{
			name:    "headless not first",
			args:    []string{"--model", "openai/gpt-5", "--headless", "task.md"},
			wantErr: "usage: rune-agent --headless",
		},
		{
			name:    "value-less flag",
			args:    []string{"--headless", "task.md", "--model"},
			wantErr: `--model requires a value`,
		},
		{
			name:    "unknown flag",
			args:    []string{"--headless", "task.md", "--memory"},
			wantErr: `unknown argument "--memory"`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseHeadlessArgs(tc.args)

			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestRunHeadlessExitCodes(t *testing.T) {
	t.Run("bad usage exits 1", func(t *testing.T) {
		assert.Equal(t, 1, runHeadless([]string{"--headless"}))
	})

	t.Run("no plugin environment exits 1", func(t *testing.T) {
		t.Setenv("RUNE_SOCKET", "")
		t.Setenv("RUNE_DATADIR", "")
		path := filepath.Join(t.TempDir(), "task.md")
		require.NoError(t, os.WriteFile(path, []byte("do it"), 0o600))

		assert.Equal(t, 1, runHeadless([]string{"--headless", path}))
	})
}

func TestRunACPExitCodes(t *testing.T) {
	t.Run("unexpected arguments exit 1", func(t *testing.T) {
		assert.Equal(t, 1, runACP([]string{"--model", "x"}))
	})

	t.Run("no plugin environment exits 1", func(t *testing.T) {
		t.Setenv("RUNE_SOCKET", "")
		t.Setenv("RUNE_DATADIR", "")
		stdout := os.Stdout

		assert.Equal(t, 1, runACP(nil))
		assert.Same(t, stdout, os.Stdout)
	})
}

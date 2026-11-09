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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHasProjectReferences(t *testing.T) {
	tests := []struct {
		name    string
		config  string
		want    bool
		wantErr string
	}{
		{"plain project", `{"compilerOptions": {"strict": true}, "include": ["src"]}`, false, ""},
		{"empty references", `{"references": []}`, false, ""},
		{"solution with comments and trailing commas", `{
  /* Vite */
  "files": [],
  "references": [
    { "path": "./tsconfig.app.json" }, // the app
  ],
}`, true, ""},
		{"malformed", `{"references": [`, false, "parse /ws/tsconfig.json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := newFakeFS("/ws").writeFile("tsconfig.json", tt.config)
			got, err := hasProjectReferences(fs, "/ws/tsconfig.json")
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}

	_, err := hasProjectReferences(newFakeFS("/ws"), "/ws/tsconfig.json")
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestCheckArgs(t *testing.T) {
	fs := newFakeFS("/ws").
		writeFile("app/tsconfig.json", `{"include": ["src"]}`).
		writeFile("tsconfig.json", `{"files": [], "references": [{"path": "./app"}]}`).
		writeFile("broken/tsconfig.json", `{`)
	assert.Equal(t, []string{"-b", "--pretty", "false", "tsconfig.json"},
		checkArgs(fs, "/ws/tsconfig.json", "tsconfig.json"))
	assert.Equal(t, []string{"--noEmit", "--pretty", "false", "-p", "app/tsconfig.json"},
		checkArgs(fs, "/ws/app/tsconfig.json", "app/tsconfig.json"))
	assert.Equal(t, []string{"--noEmit", "--pretty", "false", "-p", "broken/tsconfig.json"},
		checkArgs(fs, "/ws/broken/tsconfig.json", "broken/tsconfig.json"),
		"tsgo reports a config it cannot parse")
}

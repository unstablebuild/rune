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
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolve(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
		wantOut string
		wantDir string
	}{
		{
			name:    "found",
			args:    []string{"resolve", "go"},
			wantOut: "/usr/local/go/bin/go\n",
		},
		{
			name:    "dir",
			args:    []string{"resolve", "--dir", "/tmp", "./run.sh"},
			wantOut: "/tmp/run.sh\n",
			wantDir: "/tmp",
		},
		{
			name:    "json",
			args:    []string{"resolve", "-F", "json", "go"},
			wantOut: "{\"Path\":\"/usr/local/go/bin/go\",\"success\":true}\n",
		},
		{
			name:    "err/not found",
			args:    []string{"resolve", "nope"},
			wantErr: "file does not exist",
		},
		{
			name:    "err/args",
			args:    []string{"resolve"},
			wantErr: "accepts 1 arg",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newTestEnv(t)
			defer env.cleanup()
			env.executor.paths = map[string]string{
				"go":       "/usr/local/go/bin/go",
				"./run.sh": "/tmp/run.sh",
			}

			out, err := env.run(tt.args...)

			if tt.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantOut, out)
			require.Equal(t, tt.wantDir, env.executor.lastLookPath.GetDir())
		})
	}
}

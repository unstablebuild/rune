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

func TestEditor(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
		wantOut string
		check   func(*testing.T, *testEnv)
	}{
		// print
		{
			name:    "print",
			args:    []string{"editor", "print", "file:///tmp/test.txt"},
			wantOut: "hello world\n",
		},
		{
			name:    "print/path",
			args:    []string{"editor", "print", "/tmp/test.txt"},
			wantOut: "hello world\n",
		},
		{
			name:    "print/j",
			args:    []string{"editor", "print", "-F", "json", "file:///tmp/test.txt"},
			wantOut: "{\"Content\":\"hello world\",\"success\":true}\n",
		},

		// color
		{
			name:    "color",
			args:    []string{"editor", "color", "file:///tmp/test.txt", "blue", "red"},
			wantOut: "OK\n",
			check: func(t *testing.T, env *testEnv) {
				require.True(t, env.editor.setAttrsCalled)
			},
		},
		{
			name:    "color/j",
			args:    []string{"editor", "color", "-F", "json", "file:///tmp/test.txt", "blue"},
			wantOut: "{\"success\":true}\n",
		},

		// edit
		{
			name:    "edit",
			args:    []string{"editor", "edit", "file:///tmp/test.txt", "0", "0", "5", "0", "replacement"},
			wantOut: "0 0 5 0 old-text\n",
		},
		{
			name: "edit/j",
			args: []string{"editor", "edit", "-F", "json", "file:///tmp/test.txt", "0", "0", "5", "0", "replacement"},
			wantOut: "{\"from_x\":0,\"from_y\":0,\"old\":\"old-text\"," +
				"\"success\":true,\"to_x\":5,\"to_y\":0}\n",
		},
		{
			name:    "edit/tpl",
			args:    []string{"editor", "edit", "-F", "{{.Old}}", "file:///tmp/test.txt", "0", "0", "5", "0", "replacement"},
			wantOut: "old-text\n",
		},
		{
			name:    "edit/err",
			args:    []string{"editor", "edit", "file:///tmp/test.txt", "a", "0", "5", "0", "text"},
			wantErr: "invalid start-x",
		},

		// cursor get
		{
			name:    "cursor/get",
			args:    []string{"editor", "cursor", "get", "file:///tmp/test.txt"},
			wantOut: "10 20\n",
		},
		{
			name:    "cursor/get/j",
			args:    []string{"editor", "cursor", "get", "-F", "json", "file:///tmp/test.txt"},
			wantOut: "{\"X\":10,\"Y\":20,\"success\":true}\n",
		},
		{
			name:    "cursor/get/tpl",
			args:    []string{"editor", "cursor", "get", "-F", "{{.X}} {{.Y}}", "file:///tmp/test.txt"},
			wantOut: "10 20\n",
		},

		// cursor set
		{
			name:    "cursor/set",
			args:    []string{"editor", "cursor", "set", "file:///tmp/test.txt", "5", "10"},
			wantOut: "OK\n",
			check: func(t *testing.T, env *testEnv) {
				require.True(t, env.editor.setCursorCalled)
			},
		},
		{
			name:    "cursor/set/j",
			args:    []string{"editor", "cursor", "set", "-F", "json", "file:///tmp/test.txt", "5", "10"},
			wantOut: "{\"success\":true}\n",
		},
		{
			name:    "cursor/set/err",
			args:    []string{"editor", "cursor", "set", "file:///tmp/test.txt", "a", "0"},
			wantErr: "invalid x",
		},

		// locations set
		{
			name: "locations/set",
			args: []string{
				"editor", "locations", "set",
				"file:///tmp/test.txt", "warning", "lint",
				`[{"from":{"x":0,"y":0},"to":{"x":5,"y":0},"message":"err"}]`,
			},
			wantOut: "OK\n",
			check: func(t *testing.T, env *testEnv) {
				require.True(t, env.editor.setLocCalled)
			},
		},
		{
			name: "locations/set/j",
			args: []string{
				"editor", "locations", "set", "-F", "json",
				"file:///tmp/test.txt", "warning", "lint",
				`[{"from":{"x":0,"y":0},"to":{"x":5,"y":0},"message":"err"}]`,
			},
			wantOut: "{\"success\":true}\n",
		},
		{
			name: "locations/set/err",
			args: []string{
				"editor", "locations", "set",
				"file:///tmp/test.txt", "bad", "lint", "[]",
			},
			wantErr: "invalid priority",
		},

		// locations next
		{
			name:    "locations/next",
			args:    []string{"editor", "locations", "next", "file:///tmp/test.txt", "lint"},
			wantOut: "OK\n",
			check: func(t *testing.T, env *testEnv) {
				require.True(t, env.editor.moveNextCalled)
			},
		},
		{
			name:    "locations/next/j",
			args:    []string{"editor", "locations", "next", "-F", "json", "file:///tmp/test.txt", "lint"},
			wantOut: "{\"success\":true}\n",
		},

		// locations prev
		{
			name:    "locations/prev",
			args:    []string{"editor", "locations", "prev", "file:///tmp/test.txt", "lint"},
			wantOut: "OK\n",
			check: func(t *testing.T, env *testEnv) {
				require.True(t, env.editor.movePrevCalled)
			},
		},
		{
			name:    "locations/prev/j",
			args:    []string{"editor", "locations", "prev", "-F", "json", "file:///tmp/test.txt", "lint"},
			wantOut: "{\"success\":true}\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newTestEnv(t)
			defer env.cleanup()

			out, err := env.run(tt.args...)

			if tt.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantOut, out)
			if tt.check != nil {
				tt.check(t, env)
			}
		})
	}
}

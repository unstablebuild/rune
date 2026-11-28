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
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMisc(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
		wantOut string // use $DATADIR for substitution
		check   func(*testing.T, *testEnv, string)
	}{
		// datadir
		{
			name:    "datadir",
			args:    []string{"datadir"},
			wantOut: "$DATADIR\n",
		},
		{
			name: "datadir/j",
			args: []string{"datadir", "-F", "json"},
			check: func(t *testing.T, env *testEnv, out string) {
				require.Contains(t, out, `"success":true`)
				require.Contains(t, out, env.datadir)
			},
		},
		{
			name:    "datadir/tpl",
			args:    []string{"datadir", "-F", "{{.DataDir}}"},
			wantOut: "$DATADIR\n",
		},

		// uri
		{
			name:    "uri",
			args:    []string{"uri", "/tmp/test.txt"},
			wantOut: "file:///tmp/test.txt\n",
		},
		{
			name:    "uri/j",
			args:    []string{"uri", "-F", "json", "/tmp/test.txt"},
			wantOut: "{\"URI\":\"file:///tmp/test.txt\",\"success\":true}\n",
		},
		{
			name:    "uri/tpl",
			args:    []string{"uri", "-F", "{{.URI}}", "/tmp/test.txt"},
			wantOut: "file:///tmp/test.txt\n",
		},

		// open
		{
			name:    "open",
			args:    []string{"open", "file:///tmp/test.txt"},
			wantOut: "file:///tmp/test.txt\n",
		},
		{
			name:    "open/path",
			args:    []string{"open", "/tmp/test.txt"},
			wantOut: "file:///tmp/test.txt\n",
		},
		{
			name:    "open/j",
			args:    []string{"open", "-F", "json", "file:///tmp/test.txt"},
			wantOut: "{\"Resource\":\"file:///tmp/test.txt\",\"success\":true}\n",
		},

		// notify
		{
			name:    "notify",
			args:    []string{"notify", "info", "hello world"},
			wantOut: "OK\n",
			check: func(t *testing.T, env *testEnv, out string) {
				require.Equal(t, uint32(2), env.notif.lastLevel)
				require.Equal(t, "hello world", env.notif.lastMsg)
			},
		},
		{
			name:    "notify/j",
			args:    []string{"notify", "-F", "json", "info", "hello"},
			wantOut: "{\"success\":true}\n",
		},
		{
			name:    "notify/err",
			args:    []string{"notify", "bad", "msg"},
			wantErr: "invalid level",
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

			wantOut := tt.wantOut
			if wantOut != "" {
				wantOut = strings.ReplaceAll(wantOut, "$DATADIR", env.datadir)
				require.Equal(t, wantOut, out)
			}
			if tt.check != nil {
				tt.check(t, env, out)
			}
		})
	}
}

func TestMissingEnvVars(t *testing.T) {
	t.Setenv("RUNE_SOCKET", "")
	t.Setenv("RUNE_DATADIR", "")
	a := &app{}
	_, err := a.getWorkspace()
	require.ErrorIs(t, err, errNotInRune)
}

func TestLooksLikeURI(t *testing.T) {
	tests := []struct {
		name string
		arg  string
		want bool
	}{
		{name: "file_uri", arg: "file:///tmp/test.txt", want: true},
		{name: "http_uri", arg: "http://example.com/path", want: true},
		{name: "https_uri", arg: "https://example.com/path", want: true},
		{name: "custom_scheme", arg: "rune://workspace/file.txt", want: true},
		{name: "absolute_path", arg: "/tmp/test.txt", want: false},
		{name: "relative_path", arg: "./test.txt", want: false},
		{name: "relative_no_dot", arg: "test.txt", want: false},
		{name: "windows_path", arg: "C:\\Users\\test.txt", want: false},
		{name: "empty_string", arg: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := looksLikeURI(tt.arg)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestResolveURIArg(t *testing.T) {
	tests := []struct {
		name    string
		arg     string
		wantURI string
		wantErr bool
	}{
		{name: "file_uri", arg: "file:///tmp/test.txt", wantURI: "file:///tmp/test.txt"},
		{name: "rune_uri", arg: "rune://workspace/src/main.go", wantURI: "rune://workspace/src/main.go"},
		{name: "uri_query", arg: "file:///tmp/test.txt?line=10", wantURI: "file:///tmp/test.txt?line=10"},
		{name: "invalid_uri", arg: "://invalid", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &app{}
			got, err := a.resolveURIArg(context.Background(), tt.arg)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantURI, got.String())
		})
	}
}

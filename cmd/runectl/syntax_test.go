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
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSyntax(t *testing.T) {
	tests := []struct {
		name    string
		args    []string // $FILE will be substituted with test file path
		wantErr string
		check   func(*testing.T, *testEnv, string)
	}{
		// search
		{
			name: "search",
			args: []string{"syntax", "search", "(function_declaration) @fn"},
			check: func(t *testing.T, env *testEnv, out string) {
				require.Contains(t, out, "file:///src/main.go")
				require.Contains(t, out, "func main()")
			},
		},
		{
			name: "search/capture",
			args: []string{"syntax", "search", "-c", "fn", "(function_declaration) @fn"},
			check: func(t *testing.T, env *testEnv, out string) {
				require.Equal(t, "fn", env.syntax.lastCaptures[0])
			},
		},
		{
			name: "search/tpl",
			args: []string{"syntax", "search", "-F", "{{.File}} {{.Text}}", "(function_declaration) @fn"},
			check: func(t *testing.T, env *testEnv, out string) {
				require.Equal(t, "file:///src/main.go func main()\n", out)
			},
		},

		// searchnode
		{
			name: "searchnode",
			args: []string{"syntax", "searchnode", "func"},
			check: func(t *testing.T, env *testEnv, out string) {
				require.Contains(t, out, "MyFunc")
				require.Equal(t, uint32(8), env.syntax.lastNodeTypes)
			},
		},
		{
			name: "searchnode/combined",
			args: []string{"syntax", "searchnode", "scope|func"},
			check: func(t *testing.T, env *testEnv, out string) {
				require.Equal(t, uint32(9), env.syntax.lastNodeTypes)
			},
		},
		{
			name:    "searchnode/err",
			args:    []string{"syntax", "searchnode", "invalid"},
			wantErr: "invalid node type",
		},
		{
			name:    "searchnode/err_cmb",
			args:    []string{"syntax", "searchnode", "func|invalid"},
			wantErr: "invalid node type",
		},

		// query
		{
			name: "query",
			args: []string{"syntax", "query", "$FILE", "(package_clause) @pkg"},
			check: func(t *testing.T, env *testEnv, out string) {
				require.Contains(t, out, "package main")
				require.Contains(t, env.syntax.lastQueryURI, "test.go")
				require.Equal(t, "(package_clause) @pkg", env.syntax.lastQuery)
			},
		},
		{
			name: "query/uri",
			args: []string{"syntax", "query", "$URI", "(package_clause) @pkg"},
			check: func(t *testing.T, env *testEnv, out string) {
				require.Contains(t, env.syntax.lastQueryURI, "test.go")
			},
		},
		{
			name: "query/capture",
			args: []string{"syntax", "query", "-c", "pkg", "$FILE", "(package_clause) @pkg"},
			check: func(t *testing.T, env *testEnv, out string) {
				require.Equal(t, "pkg", env.syntax.lastCaptures[0])
			},
		},

		// querynode
		{
			name: "querynode",
			args: []string{"syntax", "querynode", "$FILE", "namespace"},
			check: func(t *testing.T, env *testEnv, out string) {
				require.Contains(t, out, "main")
				require.Contains(t, env.syntax.lastQueryURI, "test.go")
				require.Equal(t, uint32(2), env.syntax.lastNodeTypes)
			},
		},
		{
			name: "querynode/uri",
			args: []string{"syntax", "querynode", "$URI", "namespace"},
			check: func(t *testing.T, env *testEnv, out string) {
				require.Contains(t, env.syntax.lastQueryURI, "test.go")
			},
		},
		{
			name: "querynode/combined",
			args: []string{"syntax", "querynode", "$FILE", "namespace|var|type"},
			check: func(t *testing.T, env *testEnv, out string) {
				require.Equal(t, uint32(82), env.syntax.lastNodeTypes)
			},
		},
		{
			name:    "querynode/err",
			args:    []string{"syntax", "querynode", "$FILE", "invalid"},
			wantErr: "invalid node type",
		},

		// resolve
		{
			name: "resolve",
			args: []string{"syntax", "resolve", "pkg.Symbol"},
			check: func(t *testing.T, env *testEnv, out string) {
				require.Equal(t, "pkg.Symbol", env.syntax.lastResolve)
				require.Contains(t, out, "file:///src/main.go")
				require.Contains(t, out, "pkg.Symbol")
				require.Contains(t, out, "example.com/pkg")
			},
		},
		{
			name: "resolve/tpl",
			args: []string{"syntax", "resolve", "-F", "{{.Display}} {{.ImportPath}}", "pkg.Symbol"},
			check: func(t *testing.T, env *testEnv, out string) {
				require.Equal(t, "pkg.Symbol example.com/pkg\n", out)
			},
		},

		// symbols
		{
			name: "symbols",
			args: []string{"syntax", "symbols"},
			check: func(t *testing.T, env *testEnv, out string) {
				require.Contains(t, out, "pkg.Symbol")
				require.Contains(t, out, "other.Thing")
			},
		},
		{
			name: "symbols/tpl",
			args: []string{"syntax", "symbols", "-F", "{{.Name}}"},
			check: func(t *testing.T, env *testEnv, out string) {
				require.Equal(t, "pkg.Symbol\nother.Thing\n", out)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newTestEnv(t)
			defer env.cleanup()

			testFile := filepath.Join(env.datadir, "test.go")
			testURI := "file://" + testFile

			// Substitute placeholders
			args := make([]string, len(tt.args))
			for i, arg := range tt.args {
				switch arg {
				case "$FILE":
					args[i] = testFile
				case "$URI":
					args[i] = testURI
				default:
					args[i] = arg
				}
			}

			out, err := env.run(args...)

			if tt.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			if tt.check != nil {
				tt.check(t, env, out)
			}
		})
	}
}

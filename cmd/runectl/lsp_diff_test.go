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

func TestFormatUnifiedDiff(t *testing.T) {
	tests := []struct {
		name  string
		uri   string
		old   string
		new   string
		color bool
		want  string
	}{
		{
			name: "no changes",
			uri:  "file:///src/main.go",
			old:  "hello\n",
			new:  "hello\n",
			want: "",
		},
		{
			name: "line update",
			uri:  "file:///src/main.go",
			old:  "hello\n",
			new:  "hellX\n",
			want: "--- a/src/main.go\n" +
				"+++ b/src/main.go\n" +
				"@@ -1,1 +1,1 @@\n" +
				"-hello\n" +
				"+hellX\n",
		},
		{
			name: "single line change",
			uri:  "file:///src/main.go",
			old:  "hello\n",
			new:  "world\n",
			want: "--- a/src/main.go\n" +
				"+++ b/src/main.go\n" +
				"@@ -1,1 +1,1 @@\n" +
				"-hello\n" +
				"+world\n",
		},
		{
			name: "addition",
			uri:  "file:///src/main.go",
			old:  "line1\nline2\n",
			new:  "line1\nline2\nline3\n",
			want: "--- a/src/main.go\n" +
				"+++ b/src/main.go\n" +
				"@@ -1,2 +1,3 @@\n" +
				" line1\n" +
				" line2\n" +
				"+line3\n",
		},
		{
			name: "deletion",
			uri:  "file:///src/main.go",
			old:  "line1\nline2\nline3\n",
			new:  "line1\nline3\n",
			want: "--- a/src/main.go\n" +
				"+++ b/src/main.go\n" +
				"@@ -1,3 +1,2 @@\n" +
				" line1\n" +
				"-line2\n" +
				" line3\n",
		},
		{
			name:  "with color",
			uri:   "file:///src/main.go",
			old:   "hello\n",
			new:   "world\n",
			color: true,
			want: "\033[1m\033[31m--- a/src/main.go\033[0m\n" +
				"\033[1m\033[32m+++ b/src/main.go\033[0m\n" +
				"\033[36m@@ -1,1 +1,1 @@\033[0m\n" +
				"\033[31m-hello\033[0m\n" +
				"\033[32m+world\033[0m\n",
		},
		{
			name: "multiple hunks",
			uri:  "file:///src/main.go",
			old: "line1\nline2\nline3\nline4\n" +
				"line5\nline6\nline7\nline8\n" +
				"line9\nline10\n",
			new: "LINE1\nline2\nline3\nline4\n" +
				"line5\nline6\nline7\nline8\n" +
				"line9\nLINE10\n",
			want: "--- a/src/main.go\n" +
				"+++ b/src/main.go\n" +
				"@@ -1,4 +1,4 @@\n" +
				"-line1\n" +
				"+LINE1\n" +
				" line2\n" +
				" line3\n" +
				" line4\n" +
				"@@ -7,4 +7,4 @@\n" +
				" line7\n" +
				" line8\n" +
				" line9\n" +
				"-line10\n" +
				"+LINE10\n",
		},
		{
			name: "indentation change",
			uri:  "file:///src/main.go",
			old:  "func foo(\n\t\tbar string,\n) {\n}\n",
			new:  "func foo(\n\tbar string,\n) {\n}\n",
			want: "--- a/src/main.go\n" +
				"+++ b/src/main.go\n" +
				"@@ -1,4 +1,4 @@\n" +
				" func foo(\n" +
				"-\t\tbar string,\n" +
				"+\tbar string,\n" +
				" ) {\n" +
				" }\n",
		},
		{
			name: "no trailing newline",
			uri:  "file:///src/main.go",
			old:  "hello",
			new:  "world",
			want: "--- a/src/main.go\n" +
				"+++ b/src/main.go\n" +
				"@@ -1,1 +1,1 @@\n" +
				"-hello\n" +
				"+world\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatUnifiedDiff(
				tt.uri, tt.old, tt.new, tt.color,
			)
			require.Equal(t, tt.want, got)
		})
	}
}

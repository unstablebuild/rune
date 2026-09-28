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

package vctrl

import (
	"strings"
	"testing"

	godiff "github.com/sourcegraph/go-diff/diff"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGitFileDiff pins the translation between git's hunk numbering and
// this package's: both Service implementations feed the same consumers,
// so a removal has to name the new-side line it sat at either way.
func TestGitFileDiff(t *testing.T) {
	for _, tc := range []struct {
		name  string
		patch string
		want  []Hunk
	}{
		{
			name: "a modification is copied verbatim",
			patch: "--- a/a.go\n+++ b/a.go\n" +
				"@@ -2 +2 @@ func main() {\n-old\n+new\n",
			want: []Hunk{{
				OrigStartLine: 2, OrigLines: 1,
				NewStartLine: 2, NewLines: 1,
				Section: "func main() {", Body: "-old\n+new\n",
			}},
		},
		{
			name: "a removal moves to the line it sat at",
			patch: "--- a/a.go\n+++ b/a.go\n" +
				"@@ -5,2 +4,0 @@ keep4\n-gone1\n-gone2\n",
			want: []Hunk{{
				OrigStartLine: 5, OrigLines: 2,
				NewStartLine: 5, NewLines: 0,
				Section: "keep4", Body: "-gone1\n-gone2\n",
			}},
		},
		{
			name: "a removal at the head of the file",
			patch: "--- a/a.go\n+++ b/a.go\n" +
				"@@ -1,2 +0,0 @@\n-gone1\n-gone2\n",
			want: []Hunk{{
				OrigStartLine: 1, OrigLines: 2,
				NewStartLine: 1, NewLines: 0,
				Body: "-gone1\n-gone2\n",
			}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := godiff.NewFileDiffReader(
				strings.NewReader(tc.patch)).Read()
			require.NoError(t, err)
			got := gitFileDiff(d)
			assert.Equal(t, tc.want, got.Hunks)
			assert.Equal(t, "a/a.go", got.OrigName)
			assert.Equal(t, "b/a.go", got.NewName)
		})
	}
}

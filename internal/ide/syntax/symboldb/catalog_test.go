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

package symboldb

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func paths(entries []entry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.path)
	}
	return out
}

func TestCatalog(t *testing.T) {
	c := newCatalog()
	two := encodeDefs([]record{{Kind: KindVar, Line: 1, Col: 1, Name: "a"}, {Kind: KindVar, Line: 2, Col: 1, Name: "b"}})
	c.put("b/x.go", &fileEntry{defs: two, count: 2})
	c.put("a.go", &fileEntry{count: 0, skipped: skippedParse + ": boom"})
	c.put("b-c.go", &fileEntry{skipped: skippedParse + ": bang"})
	c.put("b/y.go", &fileEntry{defs: two, count: 2})

	assert.Equal(t, []string{"a.go", "b/x.go", "b/y.go", "b-c.go"}, paths(c.snapshot([]string{"."})))
	assert.Equal(t, []string{"b/x.go", "b/y.go"}, paths(c.snapshot([]string{"b"})))
	assert.ElementsMatch(t, []string{"b/x.go", "b/y.go"}, c.under("b"))

	files, records, skipped := c.counts()
	assert.Equal(t, 4, files)
	assert.Equal(t, 4, records)
	assert.Equal(t, map[string]int{skippedParse: 2}, skipped)

	c.put("b/x.go", &fileEntry{})
	assert.True(t, c.remove("b/y.go"))
	assert.False(t, c.remove("b/y.go"))
	files, records, _ = c.counts()
	assert.Equal(t, 3, files)
	assert.Equal(t, 0, records)
	assert.Equal(t, []string{"a.go", "b/x.go", "b-c.go"}, paths(c.snapshot([]string{"."})))

	c.clear()
	files, records, _ = c.counts()
	assert.Zero(t, files)
	assert.Zero(t, records)
}

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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func decodeAll(t *testing.T, b []byte) []record {
	t.Helper()
	var out []record
	require.NoError(t, decodeDefs(b, func(r record) bool {
		out = append(out, r)
		return true
	}))
	return out
}

func TestEncodeDefsRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		recs []record
	}{
		{name: "none"},
		{name: "one", recs: []record{{Kind: KindFunction, Line: 1, Col: 1, Name: "main"}}},
		{name: "many", recs: []record{
			{Kind: KindType, Line: 3, Col: 6, Name: "Invoice"},
			{Kind: KindMethod, Line: 70000, Col: 300, Name: "Tötal"},
			{Kind: KindVar, Line: 9, Col: 5, Name: strings.Repeat("x", 200)},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := encodeDefs(tt.recs)
			assert.Equal(t, len(tt.recs), defsCount(b))
			assert.Equal(t, tt.recs, decodeAll(t, b))
		})
	}
}

func TestDecodeDefsStops(t *testing.T) {
	b := encodeDefs([]record{{Kind: KindVar, Line: 1, Col: 1, Name: "a"}, {Kind: KindVar, Line: 2, Col: 1, Name: "b"}})
	var seen []string
	require.NoError(t, decodeDefs(b, func(r record) bool {
		seen = append(seen, r.Name)
		return false
	}))
	assert.Equal(t, []string{"a"}, seen)
}

func TestDecodeDefsCorrupt(t *testing.T) {
	b := encodeDefs([]record{{Kind: KindVar, Line: 1, Col: 1, Name: "name"}})
	for n := 1; n < len(b); n++ {
		err := decodeDefs(b[:n], func(record) bool { return true })
		assert.ErrorIs(t, err, errCorrupt, "truncated at %d", n)
	}
}

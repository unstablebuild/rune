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

package hostenv

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func stdRuneLookup(key string) (string, bool) {
	switch key {
	case "RUNE_DATADIR":
		return "/data", true
	case "RUNE_PKG_ID":
		return "python", true
	case "RUNE_PKG_VERSION":
		return "1.2.3", true
	case "EMPTY":
		return "", true
	}
	return "", false
}

func TestExpandVars(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		// --- basic substitution ---
		{"empty string", "", ""},
		{"no variables", "just plain text", "just plain text"},
		{"single known var", "$RUNE_DATADIR", "/data"},
		{"known var with suffix", "$RUNE_DATADIR/bin", "/data/bin"},
		{"known var with prefix", "prefix$RUNE_DATADIR", "prefix/data"},
		{"brace form", "${RUNE_DATADIR}", "/data"},
		{"brace form with suffix letters", "${RUNE_DATADIR}X", "/dataX"},
		{"brace form mid-word", "${RUNE_PKG_VERSION}-rc", "1.2.3-rc"},

		// --- the real PATH scenario ---
		{
			"datadir paths preserve PATH",
			"$RUNE_DATADIR/python/bin:$RUNE_DATADIR/bin:$PATH",
			"/data/python/bin:/data/bin:$PATH",
		},
		{
			"multiple unknown vars preserved",
			"$FOO:$BAR:$PATH",
			"$FOO:$BAR:$PATH",
		},

		// --- unknown variables left verbatim ---
		{"single unknown var", "$PATH", "$PATH"},
		{"unknown var with text", "$UNKNOWN/rest", "$UNKNOWN/rest"},
		{"unknown brace form", "${UNKNOWN}/rest", "${UNKNOWN}/rest"},
		{"known and unknown mixed", "$RUNE_DATADIR:$PATH", "/data:$PATH"},
		{
			"prose with mixed vars",
			"text with $RUNE_DATADIR and ${UNKNOWN} mixed",
			"text with /data and ${UNKNOWN} mixed",
		},

		// --- adjacency / name-boundary ---
		{"longer name is distinct var", "$RUNE_DATADIRX", "$RUNE_DATADIRX"},
		{"brace isolates name", "${RUNE_DATADIR}IRX", "/dataIRX"},
		{"two known vars adjacent", "$RUNE_DATADIR$RUNE_PKG_VERSION", "/data1.2.3"},
		{"repeated known var", "$RUNE_DATADIR:$RUNE_DATADIR", "/data:/data"},

		// --- empty-valued known var ---
		{"known empty var", "$EMPTY/bin", "/bin"},
		{"known empty var brace", "${EMPTY}x", "x"},

		// --- whitespace preservation ---
		{"leading and trailing spaces", "  $RUNE_DATADIR  ", "  /data  "},
		{"surrounded by spaces", "a $RUNE_DATADIR b", "a /data b"},
		{"tabs preserved", "$RUNE_DATADIR\twith\ttabs", "/data\twith\ttabs"},
		{"newline separated statements", "$RUNE_DATADIR\n$PATH", "/data\n$PATH"},

		// --- escaping / literal dollars ---
		{"escaped dollar preserved", "\\$RUNE_DATADIR", "\\$RUNE_DATADIR"},
		{"lone dollar", "$", "$"},
		{"double dollar", "$$", "$$"},
		{"dollar digit", "price: $5", "price: $5"},
		{"percent literal", "100%", "100%"},
		{"tilde literal", "~/foo", "~/foo"},

		// --- quotes are treated as literal characters ---
		{"single quotes kept", "'$RUNE_DATADIR'", "'/data'"},
		{"double quotes kept", "\"$RUNE_DATADIR\"", "\"/data\""},

		// --- shell metacharacters left intact ---
		{"hash is literal not comment", "$RUNE_DATADIR # x", "/data # x"},
		{"semicolon literal", "a; $RUNE_DATADIR", "a; /data"},
		{"pipe literal", "a | $RUNE_DATADIR", "a | /data"},
		{"redirect literal", "a > $RUNE_DATADIR", "a > /data"},
		{"glob literal", "glob* $RUNE_DATADIR", "glob* /data"},

		// --- constructs that should NOT be touched (no ParamExp name we know) ---
		{"command substitution untouched", "$(echo hi)", "$(echo hi)"},
		{"backtick substitution untouched", "a `echo hi` b", "a `echo hi` b"},
		{"arithmetic untouched", "$((1+2))", "$((1+2))"},
		{"unknown array index untouched", "${arr[0]}", "${arr[0]}"},

		// --- malformed input returns unchanged (parse-error path) ---
		{"unterminated brace", "${RUNE_DATADIR", "${RUNE_DATADIR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, ExpandVars(tt.input, stdRuneLookup))
		})
	}
}

func TestExpandVarsParamExpOperators(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"default op on known var uses value", "${RUNE_DATADIR:-fallback}", "/data"},
		{"default op on unknown var preserved", "${UNKNOWN:-fallback}", "${UNKNOWN:-fallback}"},
		{"length op on known var uses value", "${#RUNE_DATADIR}", "/data"},
		{"replace op on known var uses value", "${RUNE_DATADIR/data/d}", "/data"},
		{"required op on known var uses value", "${RUNE_DATADIR:?err}", "/data"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, ExpandVars(tt.input, stdRuneLookup))
		})
	}
}

func TestExpandDataDir(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		dataDir  string
		expected string
	}{
		{"plain", "$RUNE_DATADIR/bin", "/h/.rune", "/h/.rune/bin"},
		{"brace", "${RUNE_DATADIR}/bin", "/h/.rune", "/h/.rune/bin"},
		{"other vars literal", "$RUNE_DATADIR/bin:$PATH:$HOME", "/d", "/d/bin:$PATH:$HOME"},
		{"positional literal", "echo $1 $RUNE_DATADIR", "/d", "echo $1 /d"},
		{"escaped literal", "\\$RUNE_DATADIR", "/d", "\\$RUNE_DATADIR"},
		{"empty dataDir unchanged", "$RUNE_DATADIR/bin", "", "$RUNE_DATADIR/bin"},
		{"no reference", "/usr/bin", "/d", "/usr/bin"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, ExpandDataDir(tt.input, tt.dataDir))
		})
	}
}

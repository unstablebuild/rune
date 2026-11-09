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

package mcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExpandConfig(t *testing.T) {
	env := map[string]string{
		"A":          "alpha",
		"EMPTY":      "",
		"RUNE_TOKEN": "secret-token",
		"IDE_TOKEN":  "secret-ide",
	}
	getenv := func(k string) (string, bool) {
		v, ok := env[k]
		return v, ok
	}

	tests := []struct {
		name           string
		value          string
		want           string
		wantErr        string
		wantReferenced []string
		wantMissing    []string
	}{
		{name: "plain", value: "https://x/${A}", want: "https://x/alpha", wantReferenced: []string{"A"}},
		{name: "short form", value: "$A-1", want: "alpha-1", wantReferenced: []string{"A"}},
		{name: "default unused", value: "${A:-d}", want: "alpha", wantReferenced: []string{"A"}},
		{name: "default used", value: "${UNSET:-d}", want: "d", wantReferenced: []string{"UNSET"}},
		{name: "empty takes default", value: "${EMPTY:-d}", want: "d", wantReferenced: []string{"EMPTY"}},
		{name: "empty is not missing", value: "[${EMPTY}]", want: "[]", wantReferenced: []string{"EMPTY"}},
		{
			name: "missing reported", value: "${UNSET}", want: "",
			wantReferenced: []string{"UNSET"}, wantMissing: []string{"UNSET"},
		},
		{
			name: "credential reads empty", value: "Bearer ${RUNE_TOKEN}", want: "Bearer ",
			wantReferenced: []string{"RUNE_TOKEN"},
		},
		{
			name: "credential ignores default", value: "${IDE_TOKEN:-fallback}", want: "",
			wantReferenced: []string{"IDE_TOKEN"},
		},
		{name: "command substitution rejected", value: "$(cat /etc/passwd)", wantErr: "command substitution"},
		{name: "backticks rejected", value: "$A `id`", wantErr: "command substitution"},
		{name: "no dollar kept verbatim", value: `C:\\path "q"`, want: `C:\\path "q"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, exp, err := ExpandConfig(ServerConfig{URL: tt.value}, getenv)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got.URL)
			assert.Equal(t, tt.wantReferenced, exp.Referenced)
			assert.Equal(t, tt.wantMissing, exp.Missing)
		})
	}
}

func TestExpandConfigFields(t *testing.T) {
	getenv := func(k string) (string, bool) { return "v-" + k, true }
	cfg := ServerConfig{
		Command:       "${CMD}",
		Args:          []string{"--x=${ARG}"},
		Env:           map[string]string{"K": "${ENV}"},
		URL:           "https://${HOST}/mcp",
		Headers:       map[string]string{"X-Key": "${KEY}"},
		HeadersHelper: "echo ${NOT_EXPANDED}",
		OAuth:         &OAuthConfig{ClientID: "${NOT_EXPANDED}", ClientSecret: "${SECRET}"},
	}

	got, exp, err := ExpandConfig(cfg, getenv)
	require.NoError(t, err)
	assert.Equal(t, "v-CMD", got.Command)
	assert.Equal(t, []string{"--x=v-ARG"}, got.Args)
	assert.Equal(t, map[string]string{"K": "v-ENV"}, got.Env)
	assert.Equal(t, "https://v-HOST/mcp", got.URL)
	assert.Equal(t, map[string]string{"X-Key": "v-KEY"}, got.Headers)
	assert.Equal(t, "echo ${NOT_EXPANDED}", got.HeadersHelper,
		"the helper runs in a shell that expands it on the workspace host")
	assert.Equal(t, "${NOT_EXPANDED}", got.OAuth.ClientID)
	assert.Equal(t, "v-SECRET", got.OAuth.ClientSecret)
	assert.Equal(t, []string{"ARG", "CMD", "ENV", "HOST", "KEY", "SECRET"}, exp.Referenced)

	assert.Equal(t, "${ARG}", cfg.Args[0][4:], "the input config must not be modified")
	assert.Equal(t, "${KEY}", cfg.Headers["X-Key"])
	assert.Equal(t, "${SECRET}", cfg.OAuth.ClientSecret)
}

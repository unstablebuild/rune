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

package idepkg

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const testIncludePath = "$RUNE_DATADIR/etc/go.yaml"

func TestAddInclude(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		configPath string
		src        string
		want       string
		wantErr    string
	}{
		{
			name:       "missing yaml config",
			configPath: "config.yaml",
			src:        "",
			want:       "include:\n  - $RUNE_DATADIR/etc/go.yaml\n",
		},
		{
			name:       "comments-only yaml config keeps them",
			configPath: "config.yaml",
			src:        "# my config\n",
			want:       "# my config\ninclude:\n  - $RUNE_DATADIR/etc/go.yaml\n",
		},
		{
			name:       "yaml without include gets a block before the first key",
			configPath: "config.yaml",
			src:        "# header\n\n# about editor\neditor:\n  mode: vim\n",
			want:       "# header\n\n# about editor\ninclude:\n  - $RUNE_DATADIR/etc/go.yaml\neditor:\n  mode: vim\n",
		},
		{
			name:       "yaml include list gets the path appended with its indent",
			configPath: "config.yaml",
			src:        "include:\n    - a.yaml\n    - b.yaml  # keep\neditor:\n  mode: vim\n",
			want:       "include:\n    - a.yaml\n    - b.yaml  # keep\n    - $RUNE_DATADIR/etc/go.yaml\neditor:\n  mode: vim\n",
		},
		{
			name:       "yaml include list not at the top",
			configPath: "config.yaml",
			src:        "editor:\n  mode: vim\ninclude:\n  - a.yaml\nlog_level: debug\n",
			want:       "editor:\n  mode: vim\ninclude:\n  - a.yaml\n  - $RUNE_DATADIR/etc/go.yaml\nlog_level: debug\n",
		},
		{
			name:       "yaml empty include key becomes a list",
			configPath: "config.yaml",
			src:        "include:  # none yet\neditor:\n  mode: vim\n",
			want:       "include:  # none yet\n  - $RUNE_DATADIR/etc/go.yaml\neditor:\n  mode: vim\n",
		},
		{
			name:       "yaml flow include list",
			configPath: "config.yaml",
			src:        "include: [a.yaml, b.yaml]\n",
			want:       "include: [a.yaml, b.yaml, $RUNE_DATADIR/etc/go.yaml]\n",
		},
		{
			name:       "yaml empty flow include list",
			configPath: "config.yaml",
			src:        "include: []\n",
			want:       "include: [$RUNE_DATADIR/etc/go.yaml]\n",
		},
		{
			name:       "yaml flow include list with a trailing comma over lines",
			configPath: "config.yaml",
			src:        "include: [\n  'a.yaml',\n]\n",
			want:       "include: [\n  'a.yaml',\n $RUNE_DATADIR/etc/go.yaml]\n",
		},
		{
			name:       "yaml empty flow mapping is replaced",
			configPath: "config.yaml",
			src:        "{}\n",
			want:       "include:\n  - $RUNE_DATADIR/etc/go.yaml\n",
		},
		{
			name:       "yaml include that is a scalar is an error",
			configPath: "config.yaml",
			src:        "include: a.yaml\n",
			wantErr:    "include is not a list",
		},
		{
			name:       "yaml that does not parse is an error",
			configPath: "config.yaml",
			src:        "a: [\n",
			wantErr:    "parse config",
		},
		{
			name:       "already included is left alone",
			configPath: "config.yaml",
			src:        "include:\n  - $RUNE_DATADIR/etc/go.yaml\n",
			want:       "include:\n  - $RUNE_DATADIR/etc/go.yaml\n",
		},
		{
			name:       "commented out stays off",
			configPath: "config.yaml",
			src:        "include:\n  # - $RUNE_DATADIR/etc/go.yaml\n  - a.yaml\n",
			want:       "include:\n  # - $RUNE_DATADIR/etc/go.yaml\n  - a.yaml\n",
		},
		{
			name:       "expanded spelling counts as included",
			configPath: "config.yaml",
			src:        "include:\n  - /data/etc/go.yaml\n",
			want:       "include:\n  - /data/etc/go.yaml\n",
		},
		{
			name:       "missing starlark config",
			configPath: "config.star",
			src:        "",
			want:       "include(\"$RUNE_DATADIR/etc/go.yaml\")\n",
		},
		{
			name:       "starlark include goes after the leading comments",
			configPath: "config.star",
			src:        "# my config\n\n# the editor\nconfig[\"editor\"][\"mode\"] = \"vim\"\n",
			want:       "# my config\n\n# the editor\ninclude(\"$RUNE_DATADIR/etc/go.yaml\")\nconfig[\"editor\"][\"mode\"] = \"vim\"\n",
		},
		{
			name:       "starlark comments only",
			configPath: "config.star",
			src:        "# nothing",
			want:       "# nothing\ninclude(\"$RUNE_DATADIR/etc/go.yaml\")\n",
		},
		{
			name:       "starlark already included with single quotes",
			configPath: "config.star",
			src:        "include('$RUNE_DATADIR/etc/go.yaml')\n",
			want:       "include('$RUNE_DATADIR/etc/go.yaml')\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := addInclude([]byte(tt.src), tt.configPath, testIncludePath, "/data")
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, string(got))
			if tt.configPath == "config.yaml" && tt.want != tt.src {
				var doc map[string]any
				require.NoError(t, yaml.Unmarshal(got, &doc), "the result must parse")
				assert.Contains(t, doc["include"], testIncludePath)
			}
		})
	}
}

func TestRemoveInclude(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		configPath string
		src        string
		want       string
	}{
		{
			name:       "yaml block item",
			configPath: "config.yaml",
			src:        "include:\n  - a.yaml\n  - $RUNE_DATADIR/etc/go.yaml\n  - b.yaml\n",
			want:       "include:\n  - a.yaml\n  - b.yaml\n",
		},
		{
			name:       "yaml quoted block item",
			configPath: "config.yaml",
			src:        "include:\n  - \"$RUNE_DATADIR/etc/go.yaml\"\n  - '$RUNE_DATADIR/etc/go.yaml'\n",
			want:       "include:\n",
		},
		{
			name:       "yaml expanded spelling",
			configPath: "config.yaml",
			src:        "include:\n  - /data/etc/go.yaml\neditor: {}\n",
			want:       "include:\neditor: {}\n",
		},
		{
			name:       "yaml flow items",
			configPath: "config.yaml",
			src:        "include: [a.yaml, $RUNE_DATADIR/etc/go.yaml, b.yaml]\n",
			want:       "include: [a.yaml, b.yaml]\n",
		},
		{
			name:       "yaml first flow item",
			configPath: "config.yaml",
			src:        "include: [$RUNE_DATADIR/etc/go.yaml, b.yaml]\n",
			want:       "include: [b.yaml]\n",
		},
		{
			name:       "yaml only flow item",
			configPath: "config.yaml",
			src:        "include: [ $RUNE_DATADIR/etc/go.yaml ]\n",
			want:       "include: [  ]\n",
		},
		{
			name:       "yaml commented out stays",
			configPath: "config.yaml",
			src:        "include:\n  # - $RUNE_DATADIR/etc/go.yaml\n",
			want:       "include:\n  # - $RUNE_DATADIR/etc/go.yaml\n",
		},
		{
			name:       "yaml not included",
			configPath: "config.yaml",
			src:        "editor:\n  mode: vim\n",
			want:       "editor:\n  mode: vim\n",
		},
		{
			name:       "starlark call",
			configPath: "config.star",
			src:        "# header\ninclude(\"$RUNE_DATADIR/etc/go.yaml\")\ninclude('/data/etc/go.yaml')\nconfig[\"a\"] = 1\n",
			want:       "# header\nconfig[\"a\"] = 1\n",
		},
		{
			name:       "starlark commented out stays",
			configPath: "config.star",
			src:        "# include(\"$RUNE_DATADIR/etc/go.yaml\")\n",
			want:       "# include(\"$RUNE_DATADIR/etc/go.yaml\")\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := removeInclude([]byte(tt.src), tt.configPath, testIncludePath, "/data")
			assert.Equal(t, tt.want, string(got))
		})
	}
}

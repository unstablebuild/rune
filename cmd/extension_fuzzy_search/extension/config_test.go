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

package extension

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"unstable.build/rune/internal/ide/starlarkconfig"
)

// TestBundledConfigStar locks in the mode/tui-aware fuzzy-search package
// config shipped at cmd/extension_fuzzy_search/config.star. RUNE-137 moved
// the search* aliases and bindings out of cmd/rune/rune.star and into this
// file, so the host now relies on it for every search command.
func TestBundledConfigStar(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("../config.star")
	require.NoError(t, err)

	cases := []struct {
		name string
		mode string
	}{
		{"vim", "vim"},
		{"helix", "helix"},
		{"standard", "standard"},
		{"emacs", "emacs"},
		{"empty_mode_acts_as_non_standard", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := starlarkconfig.Decode(starlarkconfig.Source{
				Src:      data,
				Filename: "config.star",
				Params: map[string]any{
					"RUNE_DATADIR":     "/data",
					"RUNE_PKG_ID":      "fuzzy_search",
					"RUNE_PKG_VERSION": "1",
					"RUNE_EDITOR_MODE": tc.mode,
				},
			})
			require.NoError(t, err)

			cmd := cfg["command"].(map[string]any)
			kb := cmd["key_bindings"].(map[string]any)

			if tc.mode == "emacs" {
				assert.Equal(t, "searchfile", kb["<c-x><c-f>"])
				assert.Equal(t, "searchtext", kb["<a-s>o"])
				for _, key := range []string{
					"<m-p>", "<m-\\\\>", "<a-s-f>", "<a-s-v>", "<a-s-s>",
				} {
					assert.NotContains(t, kb, key,
						"the Emacs package map must not replace preset or editing keys")
				}
			} else {
				assert.Equal(t, "searchfile", kb["<m-p>"])
				assert.Equal(t, "searchtext", kb["<m-\\\\>"])
				assert.Equal(t, "searchfunc", kb["<a-s-f>"])
				assert.Equal(t, "searchvar", kb["<a-s-v>"])
				assert.Equal(t, "searchtype", kb["<a-s-s>"])
			}

			// Aliases present in all modes.
			aliases := cmd["aliases"].(map[string]any)
			assert.Contains(t, aliases, "searchfunc")
			assert.Contains(t, aliases, "searchvar")
			assert.Contains(t, aliases, "searchtype")

			assert.NotContains(t, aliases, "tabsearch")
			assert.NotContains(t, aliases, "workspacesearch")

			// <s-m-f>: searchtext is the only standard-specific override
			// (Sublime-style project search); modal must NOT bind it. The
			// other standard bindings (<m-f>, <m-;>, <a-g>, <s-m-r>) live
			// in the user-editable standard override preset.
			if tc.mode == "standard" {
				assert.Equal(t, "searchtext", kb["<s-m-f>"],
					"standard should bind <s-m-f> to searchtext")
			} else {
				_, ok := kb["<s-m-f>"]
				assert.False(t, ok, "non-standard must not bind <s-m-f>")
			}

			_, hasCxp := kb["<c-x><c-p>"]
			assert.False(t, hasCxp, "fuzzy_search must not bind TUI <c-x><c-p>")

			// Legacy YAML values for the finder config block survive.
			ext := cfg["extensions"].(map[string]any)
			fs := ext["fuzzy_search"].(map[string]any)
			fsCfg := fs["config"].(map[string]any)
			fileCfg := fsCfg["file"].(map[string]any)
			assert.Equal(t, "fuzzy", fileCfg["algo"])
			assert.Equal(t, "<m-p>", fileCfg["history_key"])
			assert.Equal(t, true, fileCfg["case_sensitive"])

			// The bundled onboarding tutorial is registered so Rune offers
			// to run it right after install (RUNE-268). The $RUNE_* refs are
			// expanded later by the package-merge path, so decode keeps them
			// literal, mirroring the extension path value above.
			tutorials := cfg["tutorials"].(map[string]any)
			assert.Equal(t,
				"$RUNE_DATADIR/pkg/$RUNE_PKG_ID/$RUNE_PKG_VERSION/fuzzy_search.star",
				tutorials["fuzzy_search"])
		})
	}
}

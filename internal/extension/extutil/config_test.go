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

package extutil

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"unstable.build/rune/internal/text/emacs"
	"unstable.build/rune/internal/text/helix"
	"unstable.build/rune/internal/text/standard"
	"unstable.build/rune/internal/text/vi"
)

func TestEditor(t *testing.T) {
	modalType := reflect.TypeOf(vi.Editor())
	standardType := reflect.TypeOf(standard.Editor())
	emacsType := reflect.TypeOf(emacs.Editor())
	helixType := reflect.TypeOf(helix.Editor())

	tests := []struct {
		name string
		cfg  map[string]any
		want reflect.Type
	}{
		{
			name: "no editor config",
			cfg:  map[string]any{},
			want: modalType,
		},
		{
			name: "editor config without mode defaults to modal",
			cfg:  map[string]any{"editor": map[string]any{}},
			want: modalType,
		},
		{
			name: "modal",
			cfg:  map[string]any{"editor": map[string]any{"mode": "modal"}},
			want: modalType,
		},
		{
			name: "standard",
			cfg:  map[string]any{"editor": map[string]any{"mode": "standard"}},
			want: standardType,
		},
		{
			name: "deprecated modeless alias resolves to standard",
			cfg:  map[string]any{"editor": map[string]any{"mode": "modeless"}},
			want: standardType,
		},
		{
			name: "emacs",
			cfg:  map[string]any{"editor": map[string]any{"mode": "emacs"}},
			want: emacsType,
		},
		{
			name: "helix",
			cfg:  map[string]any{"editor": map[string]any{"mode": "helix"}},
			want: helixType,
		},
		{
			name: "exo no fallback defaults to standard",
			cfg:  map[string]any{"editor": map[string]any{"mode": "exo"}},
			want: standardType,
		},
		{
			name: "exo fallback modal",
			cfg: map[string]any{"editor": map[string]any{
				"mode": "exo",
				"exo":  map[string]any{"fallback": "modal"},
			}},
			want: modalType,
		},
		{
			name: "exo fallback standard",
			cfg: map[string]any{"editor": map[string]any{
				"mode": "exo",
				"exo":  map[string]any{"fallback": "standard"},
			}},
			want: standardType,
		},
		{
			name: "exo fallback deprecated modeless resolves to standard",
			cfg: map[string]any{"editor": map[string]any{
				"mode": "exo",
				"exo":  map[string]any{"fallback": "modeless"},
			}},
			want: standardType,
		},
		{
			name: "exo fallback emacs",
			cfg: map[string]any{"editor": map[string]any{
				"mode": "exo",
				"exo":  map[string]any{"fallback": "emacs"},
			}},
			want: emacsType,
		},
		{
			name: "exo fallback helix",
			cfg: map[string]any{"editor": map[string]any{
				"mode": "exo",
				"exo":  map[string]any{"fallback": "helix"},
			}},
			want: helixType,
		},
		{
			name: "exo unknown fallback defaults to standard",
			cfg: map[string]any{"editor": map[string]any{
				"mode": "exo",
				"exo":  map[string]any{"fallback": "bogus"},
			}},
			want: standardType,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ed, err := Editor(clipboard.NewInMemory(), config.MapConfig(tc.cfg))
			require.NoError(t, err)
			require.Equal(t, tc.want, reflect.TypeOf(ed))
		})
	}
}

func TestEditorModal(t *testing.T) {
	tests := []struct {
		name string
		cfg  map[string]any
		want bool
	}{
		{"no editor config", map[string]any{}, true},
		{"empty editor config defaults to modal", map[string]any{"editor": map[string]any{}}, true},
		{"modal", map[string]any{"editor": map[string]any{"mode": "modal"}}, true},
		{"standard", map[string]any{"editor": map[string]any{"mode": "standard"}}, false},
		{"deprecated modeless alias", map[string]any{"editor": map[string]any{"mode": "modeless"}}, false},
		{"emacs", map[string]any{"editor": map[string]any{"mode": "emacs"}}, false},
		{"helix", map[string]any{"editor": map[string]any{"mode": "helix"}}, true},
		{"exo no fallback defaults to standard", map[string]any{"editor": map[string]any{"mode": "exo"}}, false},
		{
			name: "exo fallback modal",
			cfg: map[string]any{"editor": map[string]any{
				"mode": "exo",
				"exo":  map[string]any{"fallback": "modal"},
			}},
			want: true,
		},
		{
			name: "exo fallback standard",
			cfg: map[string]any{"editor": map[string]any{
				"mode": "exo",
				"exo":  map[string]any{"fallback": "standard"},
			}},
			want: false,
		},
		{
			name: "exo fallback emacs",
			cfg: map[string]any{"editor": map[string]any{
				"mode": "exo",
				"exo":  map[string]any{"fallback": "emacs"},
			}},
			want: false,
		},
		{
			name: "exo fallback helix",
			cfg: map[string]any{"editor": map[string]any{
				"mode": "exo",
				"exo":  map[string]any{"fallback": "helix"},
			}},
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			modal, err := EditorModal(config.MapConfig(tc.cfg))
			require.NoError(t, err)
			require.Equal(t, tc.want, modal)
		})
	}
}

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

package idepreset

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"unstable.build/rune/internal/ide/keymeta"
)

const withMeta = `gui:
  default_theme: romero
  meta_key: "[[.Meta]]"
#   layout: ' {{ .Status | bg "red" }} '
`

const withAltModifier = `gui:
  default_theme: romero
  alt_modifier: "[[.AltModifier]]"
`

func TestRender(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		data    Data
		want    string
		wantErr string
	}{
		{
			name: "meta and telemetry",
			body: withMeta,
			data: Data{Meta: keymeta.Alt, Telemetry: true},
			want: "gui:\n  default_theme: romero\n  meta_key: \"<alt>\"\n" +
				"#   layout: ' {{ .Status | bg \"red\" }} '\n" + footerFor("true"),
		},
		{
			name: "ctrl-super",
			body: withMeta,
			data: Data{Meta: keymeta.CtrlSuper},
			want: "gui:\n  default_theme: romero\n  meta_key: \"<ctrl-super>\"\n" +
				"#   layout: ' {{ .Status | bg \"red\" }} '\n" + footerFor("false"),
		},
		{
			name: "no field reads as super",
			body: "gui:\n  default_theme: romero\n",
			data: Data{Meta: keymeta.Super},
			want: "gui:\n  default_theme: romero\n" + footerFor("false"),
		},
		{
			name:    "no field cannot carry another meta",
			body:    "gui:\n  default_theme: romero\n",
			data:    Data{Meta: keymeta.Alt},
			wantErr: "gui.meta_key is missing",
		},
		{
			name:    "a hard-coded value must match",
			body:    "gui:\n  meta_key: \"<super>\"\n",
			data:    Data{Meta: keymeta.Alt},
			wantErr: "gui.meta_key is \"<super>\"",
		},
		{
			name:    "unknown field",
			body:    "x: [[.Nope]]\n",
			wantErr: "render preset",
		},
		{
			name: "alt modifier",
			body: withAltModifier,
			data: Data{AltModifier: "left"},
			want: "gui:\n  default_theme: romero\n  alt_modifier: \"left\"\n" + footerFor("false"),
		},
		{
			name: "no field reads as no alt modifier",
			body: "gui:\n  default_theme: romero\n",
			want: "gui:\n  default_theme: romero\n" + footerFor("false"),
		},
		{
			name:    "no field cannot carry an alt modifier",
			body:    "gui:\n  default_theme: romero\n",
			data:    Data{AltModifier: "right"},
			wantErr: "gui.alt_modifier is missing",
		},
		{
			name:    "a hard-coded alt modifier must match",
			body:    "gui:\n  alt_modifier: left\n",
			wantErr: `gui.alt_modifier is "left", want ""`,
		},
		{
			name:    "a body overriding telemetry",
			body:    "telemetry:\n  enabled: true\n",
			data:    Data{Telemetry: false},
			wantErr: "rendered preset",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Render(tc.body, tc.data)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func footerFor(enabled string) string {
	return "\ntelemetry:\n" +
		"  # Report anonymous usage and system information. See the Telemetry\n" +
		"  # page in the Rune docs for the full list of what is reported.\n" +
		"  enabled: " + enabled + "\n"
}

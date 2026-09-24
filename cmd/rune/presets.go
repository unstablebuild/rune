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
	_ "embed"
	"fmt"
)

//go:embed preset_modal.yaml
var presetModalYAML string

//go:embed preset_helix.yaml
var presetHelixYAML string

//go:embed preset_emacs.yaml
var presetEmacsYAML string

// renderPreset returns the preset-config file body for the given
// editor choice and telemetry preference. The modal choice enables vim
// mode everywhere; the helix choice uses Helix's selection-first grammar
// with its <space> leader menu; the standard choice uses platform-native
// standard editor bindings; the emacs choice uses an Emacs keymap. The
// deprecated "modeless" alias resolves to the standard preset.
func renderPreset(editor string, telemetry bool) (string, error) {
	var body string
	switch editor {
	case editorModal:
		body = presetModalYAML
	case editorHelix:
		body = presetHelixYAML
	case editorStandard, editorModeless:
		body = presetStandardYAML
	case editorEmacs:
		body = presetEmacsYAML
	default:
		return "", fmt.Errorf("unknown editor choice: %q", editor)
	}
	const tmpl = "%s\ntelemetry:\n" +
		"  # Report anonymous usage and system information. See the Telemetry\n" +
		"  # page in the Rune docs for the full list of what is reported.\n" +
		"  enabled: %t\n"
	return fmt.Sprintf(tmpl, body, telemetry), nil
}

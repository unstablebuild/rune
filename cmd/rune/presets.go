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
	"fmt"
	"runtime"
	"slices"

	"unstable.build/rune/internal/ide/idepreset"
	"unstable.build/rune/internal/ide/keymeta"
)

func renderPreset(editor string, meta keymeta.Meta, telemetry bool) (string, error) {
	var body, mode string
	switch editor {
	case editorVim:
		body, mode = presetModalYAML, editorVim
	case editorHelix:
		body, mode = presetHelixYAML, editorHelix
	case editorStandard, editorModeless:
		body, mode = presetStandardYAML, editorStandard
	case editorEmacs:
		body, mode = presetEmacsYAML, editorEmacs
	default:
		return "", fmt.Errorf("unknown editor choice: %q", editor)
	}
	if !slices.Contains(keymeta.Options(runtime.GOOS, mode), meta) {
		return "", fmt.Errorf("meta key %s is not offered for %s on %s",
			meta, mode, runtime.GOOS)
	}
	return idepreset.Render(body, idepreset.Data{Meta: meta, Telemetry: telemetry})
}

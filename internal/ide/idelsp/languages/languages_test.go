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

package languages

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExtensionForLanguage(t *testing.T) {
	tests := []struct {
		language string
		wantExt  string
		wantOK   bool
	}{
		{"python", ".py", true},
		{"javascript", ".js", true},
		{"typescript", ".ts", true},
		{"rust", ".rs", true},
		{"c", ".c", true},
		{"css", ".css", true},
		{"yaml", ".yaml", true},
		// Implicit languages (ext[1:] == languageID) are not in the map.
		{"go", "", false},
		{"somethingmadeup", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.language, func(t *testing.T) {
			ext, ok := ExtensionForLanguage(tt.language)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.wantExt, ext)
		})
	}
}

func TestExtensionForLanguageWithFallback(t *testing.T) {
	tests := []struct {
		language string
		wantExt  string
	}{
		{"python", ".py"},
		{"rust", ".rs"},
		// Implicit languages fall back to "." + language.
		{"go", ".go"},
		{"json", ".json"},
		{"somethingmadeup", ".somethingmadeup"},
	}
	for _, tt := range tests {
		t.Run(tt.language, func(t *testing.T) {
			assert.Equal(t, tt.wantExt, ExtensionForLanguageWithFallback(tt.language))
		})
	}
}

func TestFilenameForLanguage(t *testing.T) {
	tests := []struct {
		language string
		want     string
	}{
		{"go", "foo.go"},
		{"python", "foo.py"},
		{"javascript", "foo.js"},
		{"rust", "foo.rs"},
		{"unknown_lang", "foo.unknown_lang"},
	}
	for _, tt := range tests {
		t.Run(tt.language, func(t *testing.T) {
			assert.Equal(t, tt.want, FilenameForLanguage(tt.language))
		})
	}
}

func TestLanguageForFile(t *testing.T) {
	tests := []struct {
		filename string
		wantID   string
		wantErr  bool
	}{
		// Recognized exact filenames (mapped via filenameToLanguageID).
		{"Makefile", "make", false},
		// Recognized extensions.
		{"main.go", "go", false},
		{"app.py", "python", false},
		{"index.js", "javascript", false},
		{"snippet.rs", "rust", false},

		// Regression: .log files must NOT be parsed as Salesforce
		// sflog. The mapping has been removed; .log falls through to
		// the bare-extension fallback ("log") which has no installed
		// parser package on a normal system.
		{"debug.log", "log", false},
		{"server.log", "log", false},

		// Unknown extensions still fall back to ext[1:] for now.
		{"weird.totallymadeup", "totallymadeup", false},

		// No extension and not a recognized filename → error.
		{"README", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			id, err := LanguageForFile(tt.filename)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tt.wantID, id)
		})
	}
}

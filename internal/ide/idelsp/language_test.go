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

package idelsp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLanguageForFilename(t *testing.T) {
	tests := []struct {
		filename   string
		wantID     string
		wantDocID  string
		wantBinary string
	}{
		{"/ws/main.go", "go", "go", "gopls"},
		{"/ws/main.py", "python", "python", "pyright-langserver"},
		{"/ws/src/index.ts", "typescript", "typescript", "tsgo"},
		{"/ws/src/index.mts", "typescript", "typescript", "tsgo"},
		{"/ws/src/index.cts", "typescript", "typescript", "tsgo"},
		{"/ws/src/App.tsx", "typescript", "typescriptreact", "tsgo"},
		{"/ws/src/index.js", "typescript", "javascript", "tsgo"},
		{"/ws/src/index.mjs", "typescript", "javascript", "tsgo"},
		{"/ws/src/index.cjs", "typescript", "javascript", "tsgo"},
		{"/ws/src/App.jsx", "typescript", "javascriptreact", "tsgo"},
	}
	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			lang, err := languageForFilename(tt.filename)
			require.NoError(t, err)
			assert.Equal(t, tt.wantID, lang.id)
			assert.Equal(t, tt.wantDocID, lang.documentID)
			assert.Equal(t, tt.wantBinary, lang.command)
		})
	}
}

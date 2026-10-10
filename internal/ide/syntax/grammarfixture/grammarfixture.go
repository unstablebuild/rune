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

// Package grammarfixture resolves the checked-in tree-sitter parser
// fixtures for the host platform.
package grammarfixture

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"unstable.build/rune/internal/ide/syntax/treesitter"
)

// ParserPath returns the tree-sitter parser fixture in langDir built for
// the host platform, skipping the test when the platform has no fixture.
//
// The darwin fixture is a universal binary at the root of langDir; the
// Linux ones live in a GOOS_GOARCH subdirectory. The parser is always
// named treesitter.ParserFilename because the production loader matches on
// that base name.
func ParserPath(t testing.TB, langDir string) string {
	t.Helper()
	p := Parser(langDir)
	if _, err := os.Stat(p); err != nil {
		t.Skipf("no tree-sitter fixture for %s/%s at %s",
			runtime.GOOS, runtime.GOARCH, p)
	}
	return p
}

// Parser is ParserPath without the skip, for callers that have no
// testing.TB, such as a PkgManager implementation.
func Parser(langDir string) string {
	if runtime.GOOS == "darwin" {
		return filepath.Join(langDir, treesitter.ParserFilename)
	}
	return filepath.Join(
		langDir, runtime.GOOS+"_"+runtime.GOARCH, treesitter.ParserFilename)
}

// LibDir returns the file list a syntax.PkgManager must expose for
// langDir: the host parser plus every query file next to it.
func LibDir(t testing.TB, langDir string) []string {
	t.Helper()
	files := []string{ParserPath(t, langDir)}
	for _, q := range []string{
		treesitter.LocalsFilename, treesitter.HighlightsFilename,
		treesitter.IndentsFilename, treesitter.FoldsFilename,
	} {
		p := filepath.Join(langDir, q)
		if _, err := os.Stat(p); err == nil {
			files = append(files, p)
		}
	}
	return files
}

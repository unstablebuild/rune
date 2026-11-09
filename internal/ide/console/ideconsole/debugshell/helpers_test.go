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

package debugshell

import (
	"context"
	"os"
	"path/filepath"
	"runtime"

	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/ide/syntax/treesitter"
)

// stubPkgManager satisfies both idedebug.PkgManager and
// syntax.PkgManager. For the "go" key it returns the configured
// dlv binary path along with the tree-sitter grammar files in
// `grammar` so the parser can parse Go source. For other keys
// it returns just the dlv path.
type stubPkgManager struct {
	bin     string
	grammar string
}

func (p *stubPkgManager) LibDir(
	_ context.Context, langID string,
) (iterator.Iterator[string], error) {
	files := []string{p.bin}
	if p.grammar != "" && langID == "go" {
		files = append(files, filepath.Join(p.grammar, hostParserRel()))
		entries, err := os.ReadDir(p.grammar)
		if err == nil {
			for _, e := range entries {
				if e.IsDir() || e.Name() == treesitter.ParserFilename {
					continue
				}
				files = append(files, filepath.Join(p.grammar, e.Name()))
			}
		}
	}
	return iterator.FromSlice(files), nil
}

// hostParserRel is the fixture-relative path of the tree-sitter parser
// built for the host platform. The committed darwin fixture sits at the
// root of the language directory; the Linux ones live in a
// GOOS_GOARCH subdirectory.
func hostParserRel() string {
	if runtime.GOOS == "darwin" {
		return treesitter.ParserFilename
	}
	return filepath.Join(runtime.GOOS+"_"+runtime.GOARCH, treesitter.ParserFilename)
}

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

package symbolresolve_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"

	"unstable.build/rune/internal/ide/idelsp/symbolresolve"
	"unstable.build/rune/internal/ide/syntax/treesitter"
	"unstable.build/rune/internal/workspace"
)

// setupBenchWorkspace writes a definition package plus n consumer files that
// all import and reference mylib.MyType, so resolving the symbol exercises the
// reference, package-clause and import (dedup) queries together.
func setupBenchWorkspace(b *testing.B, n int) treesitter.Parser {
	b.Helper()

	root := b.TempDir()
	require.NoError(b, os.MkdirAll(filepath.Join(root, "mylib"), 0o755))
	require.NoError(b, os.WriteFile(
		filepath.Join(root, "mylib", "mylib.go"),
		[]byte("package mylib\n\ntype MyType struct{}\n"),
		0o644,
	))
	for i := range n {
		src := fmt.Sprintf(
			"package consumer%d\n\nimport \"example.com/bench/mylib\"\n\n"+
				"func use%d(v mylib.MyType) mylib.MyType { return v }\n",
			i, i,
		)
		require.NoError(b, os.WriteFile(
			filepath.Join(root, fmt.Sprintf("use%d.go", i)), []byte(src), 0o644,
		))
	}

	rootURI := "file://" + root
	uri, err := workspaceapi.ParseURI(rootURI)
	require.NoError(b, err)
	scheme, err := workspace.NewFileScheme(context.Background(), config.NopConfig(), uri)
	require.NoError(b, err)
	b.Cleanup(func() { _ = scheme.Close() })

	return treesitter.NewParser(scheme, treeSitterPkgManager(b), uri)
}

func BenchmarkResolve(b *testing.B) {
	for _, n := range []int{8, 32, 128} {
		b.Run(fmt.Sprintf("files=%d", n), func(b *testing.B) {
			parser := setupBenchWorkspace(b, n)
			b.ResetTimer()
			for range b.N {
				matches, err := symbolresolve.Resolve(
					context.Background(), parser, symbolresolve.QualifierContext{},
					specIter(symbolresolve.Go), "mylib.MyType", nil,
				)
				require.NoError(b, err)
				require.NotEmpty(b, matches)
			}
		})
	}
}

// BenchmarkResolveSerialBaseline models the prior implementation's cost: the
// reference, package-clause and import path/alias queries each issued as their
// own full-workspace Search pass. It exists to quantify the single-pass win
// over the previous five serial walks.
func BenchmarkResolveSerialBaseline(b *testing.B) {
	for _, n := range []int{8, 32, 128} {
		b.Run(fmt.Sprintf("files=%d", n), func(b *testing.B) {
			parser := setupBenchWorkspace(b, n)
			spec := symbolresolve.Go
			passes := [][2]any{
				{spec.RefQueries[0].Query, spec.RefQueries[0].Captures},
				{spec.RefQueries[1].Query, spec.RefQueries[1].Captures},
				{spec.PackageClauseQuery, spec.PackageClauseCaptures},
				{spec.ImportPathQuery, spec.ImportPathCaptures},
				{spec.ImportAliasQuery, spec.ImportAliasCaptures},
			}
			b.ResetTimer()
			for range b.N {
				for _, p := range passes {
					it, err := parser.Search(p[0].(string), p[1].([]string), spec.LangID)
					require.NoError(b, err)
					_, err = iterator.ToSlice(context.Background(), it)
					require.NoError(b, err)
				}
			}
		})
	}
}

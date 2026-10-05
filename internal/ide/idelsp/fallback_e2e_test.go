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

//go:build e2e

package idelsp

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"

	"unstable.build/rune/internal/ide/syntax"
	"unstable.build/rune/internal/ide/vctrl/testgit"
	"unstable.build/rune/internal/workspace"
)

// The corpus and the grammar are frozen at specific commits so cursor
// coordinates in the golden tables below are stable forever.
const (
	fallbackE2EEnvVar      = "RUNE_LSP_FALLBACK_E2E"
	fallbackE2ECacheEnvVar = "RUNE_LSP_FALLBACK_E2E_CACHE"

	zigCorpusRemote = "https://github.com/ziglang/zig"
	// Tag 0.14.0.
	zigCorpusCommit = "5ad91a646a753cc3eecd8751e61cf458dadd9ac4"

	zigGrammarRemote = "https://github.com/tree-sitter-grammars/tree-sitter-zig"
	// Tag v1.1.2.
	zigGrammarCommit = "b670c8df85a1568f498aa5c8cae42f51a90473c0"
)

const (
	sweepMaxFiles    = 30
	sweepMaxFileSize = 64 * 1024
	sweepMaxDefs     = 8
	sweepMaxRefs     = 12
	sweepMaxRefDefs  = 3
)

// fallbackE2E skips unless the suite is explicitly enabled, then
// returns a Manager with zero language servers whose fallback parses
// the pinned ziglang/zig checkout with a real zig tree-sitter
// grammar, exercising the entire production request path.
func fallbackE2E(t *testing.T) (*Manager, string, syntaxapi.Parser) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping E2E test in short mode")
	}
	if os.Getenv(fallbackE2EEnvVar) != "1" {
		t.Skipf("set %s=1 to run the zig corpus E2E", fallbackE2EEnvVar)
	}
	cache := fallbackE2ECache(t)
	corpus := ensurePinnedCheckout(
		t, filepath.Join(cache, "zig-"+zigCorpusCommit),
		zigCorpusRemote, zigCorpusCommit,
	)
	grammarDir := ensurePinnedCheckout(
		t, filepath.Join(cache, "tree-sitter-zig-"+zigGrammarCommit),
		zigGrammarRemote, zigGrammarCommit,
	)
	so := ensureZigGrammar(t, grammarDir)

	wd, err := os.Getwd()
	require.NoError(t, err)
	locals := filepath.Join(wd, "testdata", "zig", "locals.scm")
	require.FileExists(t, locals)

	uri, err := workspaceapi.ParseURI("file://" + corpus)
	require.NoError(t, err)
	scheme, err := workspace.NewFileScheme(
		context.Background(), config.NopConfig(), uri,
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })

	parser := syntax.NewParser(
		scheme, zigPkgManager{so: so, locals: locals}, uri,
	)
	m := New(uri, newTestScheme(), nil, nil, nil, nil, Config{
		NoInitializeServer: true, Parser: parser,
	})
	t.Cleanup(func() { _ = m.Close() })
	return m, corpus, parser
}

func fallbackE2ECache(t *testing.T) string {
	t.Helper()
	if dir := os.Getenv(fallbackE2ECacheEnvVar); dir != "" {
		return dir
	}
	base, err := os.UserCacheDir()
	require.NoError(t, err)
	return filepath.Join(base, "rune-tests", "lsp-fallback")
}

// ensurePinnedCheckout materializes remote@commit under dir exactly
// once; later runs are fully offline. Fetching a raw commit keeps the
// checkout deterministic even if the remote's tags move.
func ensurePinnedCheckout(
	t *testing.T, dir, remote, commit string,
) string {
	t.Helper()
	marker := filepath.Join(dir, ".rune-e2e-"+commit)
	if _, err := os.Stat(marker); err == nil {
		return dir
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	require.NoError(t, os.MkdirAll(dir, 0o755))
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		testgit.Run(t, dir, "init", "-q")
		testgit.Run(t, dir, "remote", "add", "origin", remote)
	}
	testgit.Run(t, dir, "fetch", "-q", "--depth", "1", "origin", commit)
	testgit.Run(t, dir, "checkout", "-q", commit)
	require.NoError(t, os.WriteFile(marker, nil, 0o644))
	return dir
}

// ensureZigGrammar compiles the checked-out grammar into a
// tree-sitter.so loadable by the production parser.
func ensureZigGrammar(t *testing.T, dir string) string {
	t.Helper()
	so := filepath.Join(dir, "tree-sitter.so")
	if _, err := os.Stat(so); err == nil {
		return so
	}
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("cc not available")
	}
	cmd := exec.Command(
		cc, "-shared", "-fPIC", "-I", "src", "src/parser.c", "-o", so,
	)
	cmd.Dir = dir
	out, cerr := cmd.CombinedOutput()
	require.NoErrorf(t, cerr, "compile zig grammar: %s", out)
	return so
}

// zigPkgManager serves the zig grammar assets to the parser the same
// way an installed language package would.
type zigPkgManager struct {
	so     string
	locals string
}

func (p zigPkgManager) LibDir(
	_ context.Context, pkgID string,
) (iterator.Iterator[string], error) {
	if pkgID != "zig" {
		return nil, fmt.Errorf("package %s not installed", pkgID)
	}
	return iterator.FromSlice([]string{p.so, p.locals}), nil
}

func corpusURI(corpus, rel string) string {
	return "file://" + filepath.Join(corpus, rel)
}

func corpusLines(t *testing.T, corpus, rel string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(corpus, rel))
	require.NoError(t, err)
	return strings.Split(string(data), "\n")
}

// textAt extracts the source text under a single-line range, in rune
// columns to match tree-sitter coordinates.
func textAt(
	t *testing.T, lines []string, r semanticapi.Range,
) string {
	t.Helper()
	require.Equal(t, r.Start.Line, r.End.Line,
		"expected single-line range %+v", r)
	require.Less(t, int(r.Start.Line), len(lines))
	line := []rune(lines[r.Start.Line])
	require.LessOrEqual(t, int(r.End.Character), len(line))
	return string(line[r.Start.Character:r.End.Character])
}

func TestFallbackZigDefinitionGolden(t *testing.T) {
	m, corpus, _ := fallbackE2E(t)

	tests := []struct {
		name              string
		file              string
		line, col         int
		wantLine, wantCol int
		wantText          string
		wantEmpty         bool
	}{
		{
			name: "parameter reference resolves to parameter",
			file: "lib/std/ascii.zig",
			line: 110, col: 11,
			wantLine: 109, wantCol: 17, wantText: "c",
		},
		{
			name: "second reference on same line resolves too",
			file: "lib/std/ascii.zig",
			line: 110, col: 35,
			wantLine: 109, wantCol: 17, wantText: "c",
		},
		{
			name: "function call resolves to hoisted declaration",
			file: "lib/std/ascii.zig",
			line: 132, col: 27,
			wantLine: 109, wantCol: 7, wantText: "isControl",
		},
		{
			name: "argument resolves to enclosing fn parameter",
			file: "lib/std/ascii.zig",
			line: 132, col: 37,
			wantLine: 131, wantCol: 15, wantText: "c",
		},
		{
			name: "file const resolves from test block",
			file: "lib/std/ascii.zig",
			line: 150, col: 9,
			wantLine: 147, wantCol: 10, wantText: "whitespace",
		},
		{
			name: "for payload resolves within loop",
			file: "lib/std/ascii.zig",
			line: 150, col: 64,
			wantLine: 150, wantCol: 22, wantText: "char",
		},
		{
			name: "block var resolves in while condition",
			file: "lib/std/ascii.zig",
			line: 153, col: 19,
			wantLine: 152, wantCol: 8, wantText: "i",
		},
		{
			name: "struct-typed const resolves at file scope",
			file: "lib/std/ascii.zig",
			line: 110, col: 16,
			wantLine: 15, wantCol: 10, wantText: "control_code",
		},
		{
			name: "cursor on declaration resolves to itself",
			file: "lib/std/ascii.zig",
			line: 109, col: 7,
			wantLine: 109, wantCol: 7, wantText: "isControl",
		},
		{
			name: "struct member behind field access is out of reach",
			file: "lib/std/ascii.zig",
			line: 110, col: 29,
			wantEmpty: true,
		},
		{
			name: "import alias resolves to its const",
			file: "lib/std/base64.zig",
			line: 3, col: 15,
			wantLine: 2, wantCol: 6, wantText: "std",
		},
		{
			name: "file const resolves inside function body",
			file: "lib/std/base64.zig",
			line: 28, col: 40,
			wantLine: 26, wantCol: 10, wantText: "standard_alphabet_chars",
		},
		{
			name: "parameter resolves in return expression",
			file: "lib/std/base64.zig",
			line: 28, col: 70,
			wantLine: 27, wantCol: 35, wantText: "ignore",
		},
		{
			name: "private fn resolves from struct initializer",
			file: "lib/std/base64.zig",
			line: 35, col: 25,
			wantLine: 27, wantCol: 3,
			wantText: "standardBase64DecoderWithIgnore",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			uri := corpusURI(corpus, tt.file)
			res, err := m.Definition(
				context.Background(), semanticapi.DefinitionParams{
					TextDocument: semanticapi.TextDocumentIdentifier{
						URI: uri,
					},
					Position: semanticapi.Position{
						Line:      uint32(tt.line),
						Character: uint32(tt.col),
					},
				})
			require.NoError(t, err)
			if tt.wantEmpty {
				assert.Nil(t, res.Location)
				assert.Empty(t, res.Locations)
				return
			}
			require.NotNil(t, res.Location,
				"no definition found; locations=%+v", res.Locations)
			assert.Equal(t, uri, res.Location.URI)
			assert.Equal(t, uint32(tt.wantLine), res.Location.Range.Start.Line)
			assert.Equal(t, uint32(tt.wantCol), res.Location.Range.Start.Character)
			lines := corpusLines(t, corpus, tt.file)
			assert.Equal(t, tt.wantText,
				textAt(t, lines, res.Location.Range))
		})
	}
}

func TestFallbackZigReferencesGolden(t *testing.T) {
	m, corpus, _ := fallbackE2E(t)

	refsAt := func(
		t *testing.T, file string, line, col int, includeDecl bool,
	) []semanticapi.Location {
		t.Helper()
		locs, err := m.References(
			context.Background(), semanticapi.ReferenceParams{
				TextDocument: semanticapi.TextDocumentIdentifier{
					URI: corpusURI(corpus, file),
				},
				Position: semanticapi.Position{
					Line:      uint32(line),
					Character: uint32(col),
				},
				Context: semanticapi.ReferenceContext{
					IncludeDeclaration: includeDecl,
				},
			})
		require.NoError(t, err)
		return locs
	}

	t.Run("parameter references are exact", func(t *testing.T) {
		locs := refsAt(t, "lib/std/ascii.zig", 109, 17, true)
		got := make([][2]uint32, 0, len(locs))
		for _, l := range locs {
			got = append(got, [2]uint32{
				l.Range.Start.Line, l.Range.Start.Character,
			})
		}
		assert.ElementsMatch(t, [][2]uint32{
			{109, 17}, // declaration
			{110, 11},
			{110, 35},
		}, got)
	})

	t.Run("declaration excluded on request", func(t *testing.T) {
		locs := refsAt(t, "lib/std/ascii.zig", 109, 17, false)
		require.Len(t, locs, 2)
		for _, l := range locs {
			assert.NotEqual(t, uint32(109), l.Range.Start.Line)
		}
	})

	t.Run("file const references span test blocks", func(t *testing.T) {
		locs := refsAt(t, "lib/std/ascii.zig", 147, 10, true)
		require.GreaterOrEqual(t, len(locs), 2)
		lines := corpusLines(t, corpus, "lib/std/ascii.zig")
		found := false
		for _, l := range locs {
			assert.Equal(t, "whitespace", textAt(t, lines, l.Range))
			if l.Range.Start.Line == 150 && l.Range.Start.Character == 9 {
				found = true
			}
		}
		assert.True(t, found, "expected reference at 150:9, got %+v", locs)
	})
}

func TestFallbackZigDocumentSymbolOutline(t *testing.T) {
	m, corpus, _ := fallbackE2E(t)
	const rel = "lib/std/ascii.zig"
	res, err := m.DocumentSymbol(
		context.Background(), semanticapi.DocumentSymbolParams{
			TextDocument: semanticapi.TextDocumentIdentifier{
				URI: corpusURI(corpus, rel),
			},
		})
	require.NoError(t, err)
	require.NotEmpty(t, res.DocumentSymbols)
	lines := corpusLines(t, corpus, rel)

	var walk func(syms []semanticapi.DocumentSymbol, parent semanticapi.Range, nested bool)
	walk = func(syms []semanticapi.DocumentSymbol, parent semanticapi.Range, nested bool) {
		for _, s := range syms {
			assert.Equal(t, s.Name, textAt(t, lines, s.SelectionRange))
			assert.True(t, rangeContains(s.Range, s.SelectionRange),
				"%s selection %+v outside range %+v",
				s.Name, s.SelectionRange, s.Range)
			if nested {
				assert.True(t, rangeContains(parent, s.Range),
					"%s range %+v outside parent %+v",
					s.Name, s.Range, parent)
			}
			walk(s.Children, s.Range, true)
		}
	}
	walk(res.DocumentSymbols, semanticapi.Range{}, false)

	isControl, ok := findOutlineSymbol(res.DocumentSymbols, "isControl")
	require.True(t, ok, "isControl missing from outline")
	assert.Equal(t, semanticapi.SymbolKindFunction, isControl.Kind)
	assert.Equal(t, uint32(109), isControl.SelectionRange.Start.Line)
	param, ok := findOutlineSymbol(isControl.Children, "c")
	require.True(t, ok, "parameter c not nested under isControl")
	assert.Equal(t, uint32(17), param.SelectionRange.Start.Character)
}

func findOutlineSymbol(
	syms []semanticapi.DocumentSymbol, name string,
) (semanticapi.DocumentSymbol, bool) {
	for _, s := range syms {
		if s.Name == name {
			return s, true
		}
	}
	return semanticapi.DocumentSymbol{}, false
}

func rangeContains(outer, inner semanticapi.Range) bool {
	if positionBefore(inner.Start, outer.Start) {
		return false
	}
	return !positionBefore(outer.End, inner.End)
}

func positionBefore(a, b semanticapi.Position) bool {
	if a.Line != b.Line {
		return a.Line < b.Line
	}
	return a.Character < b.Character
}

func TestFallbackZigCorpusSweep(t *testing.T) {
	m, corpus, parser := fallbackE2E(t)
	files := sweepFiles(t, corpus)
	require.NotEmpty(t, files)

	for _, rel := range files {
		t.Run(rel, func(t *testing.T) {
			uri := corpusURI(corpus, rel)
			lines := corpusLines(t, corpus, rel)
			defs, refs := sweepCaptures(t, parser, uri)

			definitionAt := func(pos semanticapi.Position) semanticapi.LocationResult {
				res, err := m.Definition(
					context.Background(), semanticapi.DefinitionParams{
						TextDocument: semanticapi.TextDocumentIdentifier{
							URI: uri,
						},
						Position: pos,
					})
				require.NoError(t, err)
				return res
			}

			for i, d := range defs {
				if i >= sweepMaxDefs {
					break
				}
				res := definitionAt(semanticapi.Position{
					Line:      uint32(d.From.Y),
					Character: uint32(d.From.X),
				})
				require.NotNilf(t, res.Location,
					"definition of definition %q at %d:%d",
					d.Text, d.From.Y, d.From.X)
				assert.Equalf(t, uint32(d.From.Y), res.Location.Range.Start.Line,
					"definition %q must resolve to itself", d.Text)
				assert.Equalf(t, uint32(d.From.X), res.Location.Range.Start.Character,
					"definition %q must resolve to itself", d.Text)
			}

			for i, r := range refs {
				if i >= sweepMaxRefs {
					break
				}
				res := definitionAt(semanticapi.Position{
					Line:      uint32(r.From.Y),
					Character: uint32(r.From.X),
				})
				if res.Location == nil || res.Location.URI != uri {
					continue
				}
				assert.Equalf(t, r.Text,
					textAt(t, lines, res.Location.Range),
					"definition of reference %q at %d:%d",
					r.Text, r.From.Y, r.From.X)
			}

			for i, d := range defs {
				if i >= sweepMaxRefDefs {
					break
				}
				locs, err := m.References(
					context.Background(), semanticapi.ReferenceParams{
						TextDocument: semanticapi.TextDocumentIdentifier{
							URI: uri,
						},
						Position: semanticapi.Position{
							Line:      uint32(d.From.Y),
							Character: uint32(d.From.X),
						},
						Context: semanticapi.ReferenceContext{
							IncludeDeclaration: true,
						},
					})
				require.NoError(t, err)
				require.NotEmptyf(t, locs,
					"references of %q at %d:%d",
					d.Text, d.From.Y, d.From.X)
				foundSelf := false
				for _, l := range locs {
					assert.Equal(t, d.Text, textAt(t, lines, l.Range))
					if l.Range.Start.Line == uint32(d.From.Y) &&
						l.Range.Start.Character == uint32(d.From.X) {
						foundSelf = true
					}
				}
				assert.Truef(t, foundSelf,
					"references of %q must include its declaration", d.Text)
			}
		})
	}
}

// sweepFiles returns a deterministic, bounded selection of zig
// sources under lib/std of the pinned corpus.
func sweepFiles(t *testing.T, corpus string) []string {
	t.Helper()
	root := filepath.Join(corpus, "lib", "std")
	var files []string
	err := filepath.WalkDir(root, func(
		path string, d fs.DirEntry, err error,
	) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".zig") {
			return err
		}
		info, ierr := d.Info()
		if ierr != nil || info.Size() > sweepMaxFileSize {
			return ierr
		}
		rel, rerr := filepath.Rel(corpus, path)
		if rerr != nil {
			return rerr
		}
		files = append(files, rel)
		return nil
	})
	require.NoError(t, err)
	sort.Strings(files)
	if len(files) <= sweepMaxFiles {
		return files
	}
	// Stride-sample the sorted list so the sweep spans the whole
	// alphabet of std instead of clustering on the first directories.
	stride := len(files) / sweepMaxFiles
	sampled := make([]string, 0, sweepMaxFiles)
	for i := 0; i < len(files) && len(sampled) < sweepMaxFiles; i += stride {
		sampled = append(sampled, files[i])
	}
	return sampled
}

// sweepCaptures drains one locals.scm pass through the same parser
// the fallback uses, split into definitions and references. Discards
// (`_`) are skipped.
func sweepCaptures(
	t *testing.T, parser syntaxapi.Parser, uri string,
) (defs, refs []syntaxapi.Result) {
	t.Helper()
	parsed, err := workspaceapi.ParseURI(uri)
	require.NoError(t, err)
	it, err := parser.QueryNode(parsed, fallbackNodeCaptures)
	require.NoError(t, err)
	defer it.Close() // nolint:errcheck
	for {
		v, ok := it.Next(context.Background())
		if !ok {
			break
		}
		if v.Text == "_" {
			continue
		}
		switch {
		case isDefinitionCapture(v):
			defs = append(defs, v)
		case isReferenceCapture(v):
			refs = append(refs, v)
		}
	}
	require.NoError(t, it.Err())
	return defs, refs
}

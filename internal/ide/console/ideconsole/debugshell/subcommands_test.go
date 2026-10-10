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
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/ide/syntax/treesitter"
	"unstable.build/rune/internal/workspace"
)

func TestNextStatementLine_LiteralOnlyStatements(t *testing.T) {
	t.Parallel()
	grammar := unitGrammarDir(t)

	cases := []struct {
		name       string
		source     string
		cursorLine int
		wantLine   int
	}{
		{
			name: "return false",
			source: `package main

func F() bool {
	return false
}
`,
			cursorLine: 4,
			wantLine:   4,
		},
		{
			name: "return true",
			source: `package main

func F() bool {
	return true
}
`,
			cursorLine: 4,
			wantLine:   4,
		},
		{
			name: "return nil",
			source: `package main

func F() any {
	return nil
}
`,
			cursorLine: 4,
			wantLine:   4,
		},
		{
			name: "return integer literal",
			source: `package main

func F() int {
	return 0
}
`,
			cursorLine: 4,
			wantLine:   4,
		},
		{
			name: "bare break in for",
			source: `package main

func F() {
	for {
		break
	}
}
`,
			cursorLine: 5,
			wantLine:   5,
		},
		{
			name: "bare continue in for",
			source: `package main

func F() {
	for {
		continue
	}
}
`,
			cursorLine: 5,
			wantLine:   5,
		},
		{
			name: "fallthrough in switch",
			source: `package main

func F(n int) int {
	switch n {
	case 1:
		fallthrough
	case 2:
		return n
	}
	return 0
}
`,
			cursorLine: 6,
			wantLine:   6,
		},
		{
			name: "blank line before literal return",
			source: `package main

func F() bool {

	return false
}
`,
			cursorLine: 4,
			wantLine:   5,
		},
		{
			// Cursor sits on a comment-only line. The
			// algorithm must skip the comment and land on
			// the next executable line, which is itself a
			// literal-only `return`. This exercises the
			// language-agnostic `(comment)` skip path
			// together with the literal-only acceptance.
			name: "comment-only line before literal return",
			source: `package main

func F() bool {
	// step over me
	return false
}
`,
			cursorLine: 4,
			wantLine:   5,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parser, fs, uri, path := newGoParser(t, grammar, tc.source)
			got, err := nextStatementLine(parser, fs, uri, path, tc.cursorLine)
			require.NoError(t, err)
			assert.Equal(t, tc.wantLine, got)
		})
	}
}

func TestNextStatementLine_ClosingBraceStillErrors(t *testing.T) {
	t.Parallel()
	grammar := unitGrammarDir(t)
	src := `package main

func F() bool {
	return false
}
`
	parser, fs, uri, path := newGoParser(t, grammar, src)
	// Line 5 is the closing brace of F, the last function in
	// the file.
	_, err := nextStatementLine(parser, fs, uri, path, 5)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no statement at or after line")
}

// unitGrammarDir locates the Go tree-sitter grammar shipped in
// ide/syntax/syntaxtest/go. The unit tests can run without
// dlv, but they still depend on the prebuilt grammar; skip
// when the .so is unavailable (e.g. cross-compiled CI).
func unitGrammarDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	abs, err := filepath.Abs(filepath.Join(
		wd, "..", "..", "syntax", "syntaxtest", "go"))
	require.NoError(t, err)
	if _, err := os.Stat(filepath.Join(abs, hostParserRel())); err != nil {
		t.Skipf("tree-sitter grammar not found at %s: %v", abs, err)
	}
	return abs
}

// newGoParser writes source to file.go in a fresh temp dir
// and returns a syntaxapi.Parser, the workspace FileSystem,
// the file URI, and the in-workspace path to the file.
func newGoParser(t *testing.T, grammar, source string) (
	syntaxapi.Parser, workspaceapi.FileSystem, workspaceapi.URI, string,
) {
	t.Helper()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "file.go")
	require.NoError(t, os.WriteFile(path, []byte(source), 0o644))

	wuri, err := workspaceapi.ParseURI("file://" + tmp)
	require.NoError(t, err)
	fs, err := workspace.NewFileScheme(
		context.Background(), config.NopConfig(), wuri,
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = fs.Close() })

	pkg := &stubPkgManager{grammar: grammar}
	p := treesitter.NewParser(fs, pkg, wuri)

	uri, err := workspaceapi.ParseURI("file://" + path)
	require.NoError(t, err)
	return p, fs, uri, path
}

// passThroughParser satisfies syntaxapi.Parser for tests
// that construct a Handler but do not exercise tree-sitter
// behaviour. Every method returns an empty iterator so
// nextStatementLine sees no scopes (unbounded search), no
// comments, and no anchors. Paired with passThroughFS it
// makes normalizeBreakpointLine return the requested line
// unchanged, preserving the pre-fix "trust the caller"
// behaviour that the breakpoint-storage tests rely on.
type passThroughParser struct{}

func (passThroughParser) Search(string, []string, ...string) (
	iterator.Iterator[syntaxapi.Result], error,
) {
	return iterator.FromSlice([]syntaxapi.Result(nil)), nil
}

func (passThroughParser) ResolveSymbol(context.Context, string, syntaxapi.Progress) (
	iterator.Iterator[syntaxapi.Match], error,
) {
	return iterator.Empty[syntaxapi.Match](), nil
}

func (passThroughParser) ListReferencedSymbols(context.Context) (iterator.Iterator[string], error) {
	return iterator.Empty[string](), nil
}

func (passThroughParser) SearchNode(syntaxapi.NodeCaptureName, ...string) (
	iterator.Iterator[syntaxapi.Result], error,
) {
	return iterator.FromSlice([]syntaxapi.Result(nil)), nil
}

func (passThroughParser) Query(workspaceapi.URI, string, []string) (
	iterator.Iterator[syntaxapi.Result], error,
) {
	return iterator.FromSlice([]syntaxapi.Result(nil)), nil
}

func (passThroughParser) QueryNode(workspaceapi.URI, syntaxapi.NodeCaptureName) (
	iterator.Iterator[syntaxapi.Result], error,
) {
	return iterator.FromSlice([]syntaxapi.Result(nil)), nil
}

func (passThroughParser) Highlight(workspaceapi.URI, string) (
	iterator.Iterator[textapi.Location], error,
) {
	return iterator.FromSlice([]textapi.Location(nil)), nil
}

// passThroughFS satisfies workspaceapi.FileSystem with the
// minimum behaviour the Handler exercises. OpenFile returns
// a buffer of non-blank lines so firstCodeLine, when paired
// with passThroughParser, returns the requested line
// unchanged. Tests that need real file contents must use
// newGoParser instead.
type passThroughFS struct{}

func (passThroughFS) URI(path string) (workspaceapi.URI, error) {
	return workspaceapi.ParseURI("file://" + path)
}

func (passThroughFS) OpenFile(string, int, os.FileMode) (workspaceapi.File, error) {
	// Return a buffer with enough non-blank lines that any
	// cursor row passed by a test resolves to itself in
	// firstCodeLine. 4096 is comfortably above any line
	// number a unit test would set.
	const lines = 4096
	return &nopFile{Reader: strings.NewReader(
		strings.Repeat("x\n", lines))}, nil
}

func (passThroughFS) Remove(string) error                   { return nil }
func (passThroughFS) Stat(string) (os.FileInfo, error)      { return nil, os.ErrNotExist }
func (passThroughFS) ReadDir(string) ([]os.DirEntry, error) { return nil, nil }
func (passThroughFS) MkdirAll(string, os.FileMode) error    { return nil }

// nopFile adapts a strings.Reader to workspaceapi.File for
// passThroughFS.OpenFile. Only Read and Close are exercised
// by firstCodeLine (io.ReadAll + Close).
type nopFile struct {
	*strings.Reader
}

func (*nopFile) Close() error               { return nil }
func (*nopFile) Write([]byte) (int, error)  { return 0, errors.New("readonly") }
func (*nopFile) Sync() error                { return nil }
func (*nopFile) Name() string               { return "" }
func (*nopFile) Fd() uintptr                { return 0 }
func (*nopFile) Stat() (os.FileInfo, error) { return nil, os.ErrNotExist }
func (*nopFile) Truncate(int64) error       { return nil }

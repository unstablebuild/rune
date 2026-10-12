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

package agentools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/cmd/rune-agent/agent"
)

type walkCountingFS struct {
	localFS
	readDirCalls atomic.Int32
}

func (w *walkCountingFS) ReadDir(name string) ([]os.DirEntry, error) {
	w.readDirCalls.Add(1)
	return w.localFS.ReadDir(name)
}

type parserSpy struct {
	fakeParser
	resolveCalls atomic.Int32
}

func (p *parserSpy) ResolveSymbol(ctx context.Context, name string, prog syntaxapi.Progress) (iterator.Iterator[syntaxapi.Match], error) {
	p.resolveCalls.Add(1)
	return p.fakeParser.ResolveSymbol(ctx, name, prog)
}

func TestSearchContent_DottedRedirect(t *testing.T) {
	ctx := agent.WithParentToolCallID(context.Background(), "call_123")
	dir := t.TempDir()

	// Write a file in the workspace containing both symbol and pattern text.
	filePath := filepath.Join(dir, "main.go")
	fileContent := "package main\n\nimport \"pkg\"\n\nfunc main() {\n\tpkg.Symbol()\n}\n"
	if err := os.WriteFile(filePath, []byte(fileContent), 0o644); err != nil {
		t.Fatal(err)
	}

	setupSpy := func() (*walkCountingFS, *parserSpy, *stubLSP, *FileTracker) {
		wfs := &walkCountingFS{localFS: localFS{root: dir}}
		fileURI := "file://" + filePath

		pSpy := &parserSpy{
			fakeParser: fakeParser{
				resolveFn: func(sym string) ([]syntaxapi.Match, error) {
					if sym == "pkg.Symbol" {
						return []syntaxapi.Match{
							{
								URI: fileURI,
								Pos: term.Coordinates{X: 1, Y: 5},
							},
						}, nil
					}
					if sym == "pkg.Unreferenced" {
						return []syntaxapi.Match{
							{
								URI: fileURI,
								Pos: term.Coordinates{X: 0, Y: 0},
							},
						}, nil
					}
					return nil, nil
				},
			},
		}

		lsp := &stubLSP{
			referencesFn: func(p semanticapi.ReferenceParams) ([]semanticapi.Location, error) {
				if p.TextDocument.URI == fileURI && p.Position.Line == 5 {
					return []semanticapi.Location{
						loc(fileURI, 5, 1),
					}, nil
				}
				return nil, nil
			},
		}

		tracker := NewFileTracker()
		return wfs, pSpy, lsp, tracker
	}

	expectedNote := `Note: "pkg.Symbol" resolved to a known symbol; showing find_references results`

	t.Run("Row 1: Dotted symbol resolves with references -> note present, walk skipped", func(t *testing.T) {
		wfs, pSpy, lsp, tracker := setupSpy()
		tool := newSearch(wfs, dirURI(dir), tracker, nil, lsp, pSpy)

		res := tool.Execute(ctx, `{"pattern": "pkg.Symbol", "path": null, "include": null}`)
		if res.IsError {
			t.Fatalf("unexpected error: %s", res.Content)
		}
		if !strings.Contains(res.Content, expectedNote) {
			t.Errorf("expected educational note in output, got:\n%s", res.Content)
		}
		if !strings.Contains(res.Content, "main.go:6:") {
			t.Errorf("expected references output, got:\n%s", res.Content)
		}
		if wfs.readDirCalls.Load() != 0 {
			t.Errorf("expected filesystem walk to be skipped (0 ReadDir calls), got %d", wfs.readDirCalls.Load())
		}
		if pSpy.resolveCalls.Load() == 0 {
			t.Error("expected parser ResolveSymbol to be invoked")
		}

		// Verify discovery was tracked for filePath
		consumed := tracker.ConsumeDiscoveries(filePath)
		if len(consumed) == 0 || consumed[0] != "call_123" {
			t.Errorf("expected discovery call_123 tracked for %s, got %v", filePath, consumed)
		}
	})

	t.Run("Row 2: Dotted symbol not found in parser -> regex walk, no note", func(t *testing.T) {
		wfs, pSpy, lsp, tracker := setupSpy()
		tool := newSearch(wfs, dirURI(dir), tracker, nil, lsp, pSpy)

		res := tool.Execute(ctx, `{"pattern": "pkg.NonExistent", "path": null, "include": null}`)
		if strings.Contains(res.Content, "Note: ") {
			t.Errorf("did not expect educational note, got:\n%s", res.Content)
		}
		if wfs.readDirCalls.Load() == 0 {
			t.Error("expected filesystem walk to run")
		}
		if pSpy.resolveCalls.Load() == 0 {
			t.Error("expected parser ResolveSymbol to be called")
		}
	})

	t.Run("Row 3: Dotted symbol resolves but 0 references -> regex walk, no note", func(t *testing.T) {
		wfs, pSpy, lsp, tracker := setupSpy()
		tool := newSearch(wfs, dirURI(dir), tracker, nil, lsp, pSpy)

		res := tool.Execute(ctx, `{"pattern": "pkg.Unreferenced", "path": null, "include": null}`)
		if strings.Contains(res.Content, "Note: ") {
			t.Errorf("did not expect educational note, got:\n%s", res.Content)
		}
		if wfs.readDirCalls.Load() == 0 {
			t.Error("expected filesystem walk to run")
		}
		if pSpy.resolveCalls.Load() == 0 {
			t.Error("expected parser ResolveSymbol to be called")
		}
	})

	t.Run("Row 4: Pattern without dot -> normal walk, parser NEVER called", func(t *testing.T) {
		wfs, pSpy, lsp, tracker := setupSpy()
		tool := newSearch(wfs, dirURI(dir), tracker, nil, lsp, pSpy)

		res := tool.Execute(ctx, `{"pattern": "Symbol", "path": null, "include": null}`)
		if strings.Contains(res.Content, "Note: ") {
			t.Errorf("did not expect educational note, got:\n%s", res.Content)
		}
		if wfs.readDirCalls.Load() == 0 {
			t.Error("expected filesystem walk to run")
		}
		if pSpy.resolveCalls.Load() != 0 {
			t.Errorf("expected parser NEVER to be called, got %d calls", pSpy.resolveCalls.Load())
		}
	})

	t.Run("Row 5: Nil parser or LSP -> normal walk, no panic", func(t *testing.T) {
		wfs, _, _, tracker := setupSpy()
		tool := newSearch(wfs, dirURI(dir), tracker, nil, nil, nil)

		res := tool.Execute(ctx, `{"pattern": "pkg.Symbol", "path": null, "include": null}`)
		if strings.Contains(res.Content, "Note: ") {
			t.Errorf("did not expect educational note, got:\n%s", res.Content)
		}
		if wfs.readDirCalls.Load() == 0 {
			t.Error("expected filesystem walk to run")
		}
	})
}

func TestGrepFiles_DottedRedirect(t *testing.T) {
	ctx := agent.WithParentToolCallID(context.Background(), "call_456")
	dir := t.TempDir()

	filePath := filepath.Join(dir, "main.go")
	fileContent := "package main\n\nimport \"pkg\"\n\nfunc main() {\n\tpkg.Symbol()\n}\n"
	if err := os.WriteFile(filePath, []byte(fileContent), 0o644); err != nil {
		t.Fatal(err)
	}

	setupSpy := func() (*walkCountingFS, *parserSpy, *stubLSP, *FileTracker) {
		wfs := &walkCountingFS{localFS: localFS{root: dir}}
		fileURI := "file://" + filePath

		pSpy := &parserSpy{
			fakeParser: fakeParser{
				resolveFn: func(sym string) ([]syntaxapi.Match, error) {
					if sym == "pkg.Symbol" {
						return []syntaxapi.Match{
							{
								URI: fileURI,
								Pos: term.Coordinates{X: 1, Y: 5},
							},
						}, nil
					}
					if sym == "pkg.Unreferenced" {
						return []syntaxapi.Match{
							{
								URI: fileURI,
								Pos: term.Coordinates{X: 0, Y: 0},
							},
						}, nil
					}
					return nil, nil
				},
			},
		}

		lsp := &stubLSP{
			referencesFn: func(p semanticapi.ReferenceParams) ([]semanticapi.Location, error) {
				if p.TextDocument.URI == fileURI && p.Position.Line == 5 {
					return []semanticapi.Location{
						loc(fileURI, 5, 1),
					}, nil
				}
				return nil, nil
			},
		}

		tracker := NewFileTracker()
		return wfs, pSpy, lsp, tracker
	}

	expectedNote := `Note: "pkg.Symbol" resolved to a known symbol; showing find_references results`

	t.Run("Row 1: Dotted symbol resolves with references -> note present, walk skipped", func(t *testing.T) {
		wfs, pSpy, lsp, tracker := setupSpy()
		tool := NewGrepFiles(wfs, dirURI(dir), tracker, lsp, pSpy)

		res := tool.Execute(ctx, `{"pattern": "pkg.Symbol"}`)
		if res.IsError {
			t.Fatalf("unexpected error: %s", res.Content)
		}
		if !strings.Contains(res.Content, expectedNote) {
			t.Errorf("expected educational note in output, got:\n%s", res.Content)
		}
		if !strings.Contains(res.Content, "main.go") {
			t.Errorf("expected main.go in output, got:\n%s", res.Content)
		}
		if wfs.readDirCalls.Load() != 0 {
			t.Errorf("expected filesystem walk to be skipped (0 ReadDir calls), got %d", wfs.readDirCalls.Load())
		}
		if pSpy.resolveCalls.Load() == 0 {
			t.Error("expected parser ResolveSymbol to be invoked")
		}

		consumed := tracker.ConsumeDiscoveries(filePath)
		if len(consumed) == 0 || consumed[0] != "call_456" {
			t.Errorf("expected discovery call_456 tracked for %s, got %v", filePath, consumed)
		}
	})

	t.Run("Row 1 with truncation limit: Truncation notice added", func(t *testing.T) {
		_, pSpy, _, tracker := setupSpy()
		wfs := &walkCountingFS{localFS: localFS{root: dir}}
		secondFile := filepath.Join(dir, "second.go")
		_ = os.WriteFile(secondFile, []byte("package main"), 0o644)
		lsp2 := &stubLSP{
			referencesFn: func(p semanticapi.ReferenceParams) ([]semanticapi.Location, error) {
				return []semanticapi.Location{
					loc("file://"+filePath, 5, 1),
					loc("file://"+secondFile, 0, 0),
				}, nil
			},
		}
		tool2 := NewGrepFiles(wfs, dirURI(dir), tracker, lsp2, pSpy)
		res2 := tool2.Execute(ctx, `{"pattern": "pkg.Symbol", "limit": 1}`)
		if !strings.Contains(res2.Content, "(results truncated at 1 files)") {
			t.Errorf("expected truncation notice in output, got:\n%s", res2.Content)
		}
	})

	t.Run("Row 2: Dotted symbol not found in parser -> regex walk, no note", func(t *testing.T) {
		wfs, pSpy, lsp, tracker := setupSpy()
		tool := NewGrepFiles(wfs, dirURI(dir), tracker, lsp, pSpy)

		res := tool.Execute(ctx, `{"pattern": "pkg.NonExistent"}`)
		if strings.Contains(res.Content, "Note: ") {
			t.Errorf("did not expect educational note, got:\n%s", res.Content)
		}
		if wfs.readDirCalls.Load() == 0 {
			t.Error("expected filesystem walk to run")
		}
		if pSpy.resolveCalls.Load() == 0 {
			t.Error("expected parser ResolveSymbol to be called")
		}
	})

	t.Run("Row 3: Dotted symbol resolves but 0 references -> regex walk, no note", func(t *testing.T) {
		wfs, pSpy, lsp, tracker := setupSpy()
		tool := NewGrepFiles(wfs, dirURI(dir), tracker, lsp, pSpy)

		res := tool.Execute(ctx, `{"pattern": "pkg.Unreferenced"}`)
		if strings.Contains(res.Content, "Note: ") {
			t.Errorf("did not expect educational note, got:\n%s", res.Content)
		}
		if wfs.readDirCalls.Load() == 0 {
			t.Error("expected filesystem walk to run")
		}
		if pSpy.resolveCalls.Load() == 0 {
			t.Error("expected parser ResolveSymbol to be called")
		}
	})

	t.Run("Row 4: Pattern without dot -> normal walk, parser NEVER called", func(t *testing.T) {
		wfs, pSpy, lsp, tracker := setupSpy()
		tool := NewGrepFiles(wfs, dirURI(dir), tracker, lsp, pSpy)

		res := tool.Execute(ctx, `{"pattern": "Symbol"}`)
		if strings.Contains(res.Content, "Note: ") {
			t.Errorf("did not expect educational note, got:\n%s", res.Content)
		}
		if wfs.readDirCalls.Load() == 0 {
			t.Error("expected filesystem walk to run")
		}
		if pSpy.resolveCalls.Load() != 0 {
			t.Errorf("expected parser NEVER to be called, got %d calls", pSpy.resolveCalls.Load())
		}
	})

	t.Run("Row 5: Nil parser or LSP -> normal walk, no panic", func(t *testing.T) {
		wfs, _, _, tracker := setupSpy()
		tool := NewGrepFiles(wfs, dirURI(dir), tracker, nil, nil)

		res := tool.Execute(ctx, `{"pattern": "pkg.Symbol"}`)
		if strings.Contains(res.Content, "Note: ") {
			t.Errorf("did not expect educational note, got:\n%s", res.Content)
		}
		if wfs.readDirCalls.Load() == 0 {
			t.Error("expected filesystem walk to run")
		}
	})
}
